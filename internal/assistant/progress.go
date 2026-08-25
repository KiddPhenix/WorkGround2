package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// CompleteRunInput atomically finishes a successful run and applies its
// progress patch to the plan. Run completion, plan changes, artifacts and
// opportunities all commit in a single write guarded by one request receipt, so
// a crash cannot leave the aggregate half-applied.
type CompleteRunInput struct {
	RequestID        string
	RunID            string
	LeaseOwner       string
	LeaseFence       int64
	Summary          string
	SessionPath      string
	ResponsibilityID string
	Progress         ProgressBlock
	Now              time.Time
}

// CompleteRunWithProgress is the single converged progress mutation. It is
// idempotent under the caller request ID, validates the dependency graph,
// recomputes downstream readiness, rejects stale plan revisions, and records a
// receipt before the single atomic write.
func (s *Store) CompleteRunWithProgress(in CompleteRunInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	if err := validateProgressBlock(in.Progress); err != nil {
		return nil, err
	}
	in.Summary = strings.TrimSpace(in.Summary)
	in.SessionPath = strings.TrimSpace(in.SessionPath)
	if in.ResponsibilityID != "" {
		if err := validateID("responsibility", in.ResponsibilityID); err != nil {
			return nil, err
		}
	}
	fp, err := inputFingerprint(struct {
		RunID, Owner, Summary, SessionPath, ResponsibilityID string
		Fence                                                int64
		Progress                                             ProgressBlock
	}{in.RunID, in.LeaseOwner, in.Summary, in.SessionPath, in.ResponsibilityID, in.LeaseFence, in.Progress})
	if err != nil {
		return nil, err
	}
	assistantID, err := s.runOwner(in.RunID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[Run](agg, in.RequestID, "complete_run_with_progress", fp); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := runIndex(agg, in.RunID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	run := &agg.Runs[idx]
	now := storeNow(in.Now)
	if run.State != RunRunning || run.LeaseOwner != in.LeaseOwner || run.LeaseFence != in.LeaseFence || !now.Before(run.LeaseUntil) {
		return nil, fmt.Errorf("assistant: run %s fence %d is stale: %w", in.RunID, in.LeaseFence, ErrLeaseLost)
	}
	selected, err := applyProgress(agg, in.Progress, run.ID, now)
	if err != nil {
		return nil, err
	}
	if in.ResponsibilityID != "" {
		if selected != "" && selected != in.ResponsibilityID {
			return nil, fmt.Errorf("assistant: progress selected responsibility %s but run targeted %s: %w", selected, in.ResponsibilityID, ErrConflict)
		}
		if responsibilityIndex(agg, in.ResponsibilityID) < 0 {
			return nil, fmt.Errorf("assistant: run %s references missing responsibility %s: %w", in.RunID, in.ResponsibilityID, ErrNotFound)
		}
		selected = in.ResponsibilityID
	}
	if selected != "" {
		run.ResponsibilityID = selected
	}
	if err := moveRun(run, RunSucceeded); err != nil {
		return nil, err
	}
	run.Summary = in.Summary
	run.SessionPath = in.SessionPath
	run.FinishedAt = now
	clearLease(run)
	run.Revision++
	run.UpdatedAt = now
	result := clone(*run)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "complete_run_with_progress", fp, result, now); err != nil {
		return nil, err
	}
	if err := validateAggregate(agg); err != nil {
		return nil, fmt.Errorf("%w: complete run %s: %v", ErrCorrupt, in.RunID, err)
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

// applyProgress applies one progress block to the plan in place. It returns the
// resolved responsibility ID the run was working on (when the block declares
// one). Every validation happens before the caller's single write.
//
// The patch is order-independent: declarations, dependency updates, completions
// and activations are each resolved against the whole patch, so completing a
// downstream and its upstream in one block works regardless of slice order. A
// re-declared alias whose fields and dependencies are unchanged is a no-op.
func applyProgress(agg *aggregate, b ProgressBlock, runID string, now time.Time) (string, error) {
	if progressBlockEmpty(b) {
		return "", nil
	}
	if b.PlanRevision != 0 && b.PlanRevision != agg.Plan.Revision {
		return "", conflict("plan", agg.Assistant.ID, b.PlanRevision, agg.Plan.Revision)
	}

	aliasToID := make(map[string]string, len(agg.Plan.Responsibilities)+len(b.Responsibilities))
	idToAlias := make(map[string]string, len(agg.Plan.Responsibilities)+len(b.Responsibilities))
	for _, r := range agg.Plan.Responsibilities {
		if r.Alias != "" {
			aliasToID[r.Alias] = r.ID
			idToAlias[r.ID] = r.Alias
		}
	}

	changed := false

	type decl struct {
		alias string
		id    string
		deps  []string // nil means unchanged
	}
	decls := make([]decl, 0, len(b.Responsibilities))
	for _, d := range b.Responsibilities {
		alias := strings.TrimSpace(d.Alias)
		if alias == "" {
			return "", errors.New("assistant: responsibility alias is required")
		}
		if !validAlias(alias) {
			return "", fmt.Errorf("assistant: invalid responsibility alias %q", alias)
		}
		objective := strings.TrimSpace(d.Objective)
		if objective == "" {
			return "", fmt.Errorf("assistant: responsibility %s objective is required", alias)
		}
		if existing, ok := aliasToID[alias]; ok {
			idx := responsibilityIndex(agg, existing)
			if idx >= 0 {
				r := &agg.Plan.Responsibilities[idx]
				if r.Objective != objective {
					return "", fmt.Errorf("assistant: alias %s already used for a different objective: %w", alias, ErrConflict)
				}
				if done := strings.TrimSpace(d.DoneCriteria); done != "" && done != r.DoneCriteria {
					r.DoneCriteria = done
					r.Revision++
					r.UpdatedAt = now
					changed = true
				}
				if next := strings.TrimSpace(d.NextAction); next != "" && next != r.NextAction {
					r.NextAction = next
					r.Revision++
					r.UpdatedAt = now
					changed = true
				}
				if d.DependsOn != nil {
					decls = append(decls, decl{alias: alias, id: existing, deps: d.DependsOn})
				}
			}
			continue
		}
		r := Responsibility{
			ID:           StableID("resp", agg.Assistant.ID+"/"+alias),
			AssistantID:  agg.Assistant.ID,
			Alias:        alias,
			Objective:    objective,
			DoneCriteria: strings.TrimSpace(d.DoneCriteria),
			NextAction:   strings.TrimSpace(d.NextAction),
			Status:       RespBlocked,
			Revision:     1,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		agg.Plan.Responsibilities = append(agg.Plan.Responsibilities, r)
		aliasToID[alias] = r.ID
		idToAlias[r.ID] = alias
		changed = true
		if d.DependsOn != nil {
			decls = append(decls, decl{alias: alias, id: r.ID, deps: d.DependsOn})
		}
	}

	// Resolve explicit depends_on after every declaration exists, so forward
	// references and cycle detection never depend on declaration order. A nil
	// depends_on leaves the current set unchanged; an empty one clears it.
	for _, dc := range decls {
		idx := responsibilityIndex(agg, dc.id)
		if idx < 0 {
			continue
		}
		r := &agg.Plan.Responsibilities[idx]
		deps, err := resolveDeps(agg, r.ID, dc.alias, dc.deps, aliasToID)
		if err != nil {
			return "", err
		}
		if sameStrings(r.DependsOn, deps) {
			continue
		}
		if r.Status == RespDone && !depsDone(agg, deps) {
			return "", fmt.Errorf("assistant: done responsibility %s cannot gain incomplete dependencies: %w", dc.alias, ErrTransition)
		}
		r.DependsOn = deps
		r.Revision++
		r.UpdatedAt = now
		changed = true
	}

	for i, d := range b.Artifacts {
		respID, err := resolveRespAlias(aliasToID, d.Resp)
		if err != nil {
			return "", err
		}
		if err := guardRespActive(agg, respID, d.Resp); err != nil {
			return "", err
		}
		title := strings.TrimSpace(d.Title)
		if title == "" {
			return "", errors.New("assistant: artifact title is required")
		}
		agg.Artifacts = append(agg.Artifacts, Artifact{
			ID:          StableID("artifact", fmt.Sprintf("%s/%s/%s/%d", runID, d.Resp, title, i)),
			AssistantID: agg.Assistant.ID,
			RespID:      respID,
			RunID:       runID,
			Title:       title,
			Kind:        strings.TrimSpace(d.Kind),
			Content:     d.Content,
			Evidence:    strings.TrimSpace(d.Evidence),
			Revision:    1,
			CreatedAt:   now,
		})
		changed = true
	}

	for i, d := range b.Opportunities {
		respID, err := resolveRespAlias(aliasToID, d.Resp)
		if err != nil {
			return "", err
		}
		agg.Opportunities = append(agg.Opportunities, Opportunity{
			ID:          StableID("opp", fmt.Sprintf("%s/%s/%d", runID, d.Resp, i)),
			AssistantID: agg.Assistant.ID,
			RespID:      respID,
			RunID:       runID,
			Reason:      strings.TrimSpace(d.Reason),
			Revision:    1,
			CreatedAt:   now,
		})
		changed = true
	}

	// Validate every completion before marking any, so a patch that completes an
	// upstream and its downstream is order-independent.
	completeSet := make(map[string]bool, len(b.Complete))
	completeOrder := make([]string, 0, len(b.Complete))
	for _, alias := range b.Complete {
		alias = strings.TrimSpace(alias)
		if alias == "" {
			return "", errors.New("assistant: responsibility alias is required")
		}
		if completeSet[alias] {
			continue
		}
		completeSet[alias] = true
		completeOrder = append(completeOrder, alias)
	}
	for _, alias := range completeOrder {
		respID, err := resolveRespAlias(aliasToID, alias)
		if err != nil {
			return "", err
		}
		idx := responsibilityIndex(agg, respID)
		if idx < 0 {
			return "", ErrNotFound
		}
		r := &agg.Plan.Responsibilities[idx]
		if r.Status == RespDone {
			continue
		}
		if !depsDoneIncluding(agg, r.DependsOn, completeSet, idToAlias) {
			return "", fmt.Errorf("assistant: responsibility %s dependencies are incomplete: %w", alias, ErrBlocked)
		}
	}
	for _, alias := range completeOrder {
		respID, _ := resolveRespAlias(aliasToID, alias)
		idx := responsibilityIndex(agg, respID)
		r := &agg.Plan.Responsibilities[idx]
		if r.Status == RespDone {
			continue
		}
		r.Status = RespDone
		r.BlockReason = ""
		r.Revision++
		r.UpdatedAt = now
		changed = true
	}

	recomputeReadiness(agg, idToAlias, now, &changed)

	for _, alias := range b.Active {
		alias = strings.TrimSpace(alias)
		respID, err := resolveRespAlias(aliasToID, alias)
		if err != nil {
			return "", err
		}
		idx := responsibilityIndex(agg, respID)
		if idx < 0 {
			return "", ErrNotFound
		}
		r := &agg.Plan.Responsibilities[idx]
		if r.Status == RespActive {
			continue
		}
		if r.Status == RespDone {
			return "", fmt.Errorf("assistant: done responsibility %s cannot be activated: %w", alias, ErrTransition)
		}
		if !depsDone(agg, r.DependsOn) {
			return "", fmt.Errorf("assistant: responsibility %s dependencies are incomplete: %w", alias, ErrBlocked)
		}
		r.Status = RespActive
		r.BlockReason = ""
		r.Revision++
		r.UpdatedAt = now
		changed = true
	}

	selected := ""
	if b.Responsibility != "" {
		respID, err := resolveRespAlias(aliasToID, strings.TrimSpace(b.Responsibility))
		if err != nil {
			return "", err
		}
		selected = respID
	}
	if changed {
		agg.Plan.Revision++
	}
	return selected, nil
}

func resolveDeps(agg *aggregate, respID, alias string, depAliases []string, aliasToID map[string]string) ([]string, error) {
	deps := make([]string, 0, len(depAliases))
	for _, depAlias := range depAliases {
		depAlias = strings.TrimSpace(depAlias)
		if depAlias == alias {
			return nil, fmt.Errorf("assistant: responsibility %s cannot depend on itself", alias)
		}
		depID, ok := aliasToID[depAlias]
		if !ok {
			return nil, fmt.Errorf("assistant: dependency %s: %w", depAlias, ErrNotFound)
		}
		deps = append(deps, depID)
	}
	deps = dedupe(deps)
	if detectsCycle(agg, respID, deps) {
		return nil, fmt.Errorf("assistant: dependency cycle involving %s: %w", alias, ErrTransition)
	}
	return deps, nil
}

func resolveRespAlias(aliasToID map[string]string, alias string) (string, error) {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", errors.New("assistant: responsibility alias is required")
	}
	id, ok := aliasToID[alias]
	if !ok {
		return "", fmt.Errorf("assistant: responsibility alias %s: %w", alias, ErrNotFound)
	}
	return id, nil
}

func guardRespActive(agg *aggregate, respID, alias string) error {
	idx := responsibilityIndex(agg, respID)
	if idx < 0 {
		return ErrNotFound
	}
	if agg.Plan.Responsibilities[idx].Status == RespDone {
		return fmt.Errorf("assistant: responsibility %s is already done: %w", alias, ErrTransition)
	}
	return nil
}

// recomputeReadiness keeps readiness consistent with the dependency graph. It
// promotes blocked responsibilities whose dependencies are all done and demotes
// ready or active responsibilities that gained an incomplete dependency. Done
// and failed responsibilities are terminal and are left untouched.
func recomputeReadiness(agg *aggregate, idToAlias map[string]string, now time.Time, changed *bool) {
	for i := range agg.Plan.Responsibilities {
		r := &agg.Plan.Responsibilities[i]
		if r.Status == RespDone || r.Status == RespFailed {
			continue
		}
		if depsDone(agg, r.DependsOn) {
			if r.Status == RespBlocked {
				r.Status = RespReady
				r.BlockReason = ""
				r.Revision++
				r.UpdatedAt = now
				*changed = true
			}
			continue
		}
		reason := blockReason(agg, r.DependsOn, idToAlias)
		if r.Status != RespBlocked {
			r.Status = RespBlocked
			r.BlockReason = reason
			r.Revision++
			r.UpdatedAt = now
			*changed = true
		} else if r.BlockReason != reason {
			r.BlockReason = reason
			r.Revision++
			r.UpdatedAt = now
			*changed = true
		}
	}
}

func depsDone(agg *aggregate, deps []string) bool {
	for _, dep := range deps {
		idx := responsibilityIndex(agg, dep)
		if idx < 0 || agg.Plan.Responsibilities[idx].Status != RespDone {
			return false
		}
	}
	return true
}

// depsDoneIncluding reports whether every dependency is either already done or
// is being completed in the same patch, so a completion that unblocks a
// downstream in one block is accepted regardless of slice order.
func depsDoneIncluding(agg *aggregate, deps []string, doneAliases map[string]bool, idToAlias map[string]string) bool {
	for _, dep := range deps {
		idx := responsibilityIndex(agg, dep)
		if idx < 0 {
			return false
		}
		if agg.Plan.Responsibilities[idx].Status == RespDone {
			continue
		}
		if doneAliases[idToAlias[dep]] {
			continue
		}
		return false
	}
	return true
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func blockReason(agg *aggregate, deps []string, idToAlias map[string]string) string {
	var pending []string
	for _, dep := range deps {
		idx := responsibilityIndex(agg, dep)
		if idx < 0 || agg.Plan.Responsibilities[idx].Status != RespDone {
			label := dep
			if alias, ok := idToAlias[dep]; ok {
				label = alias
			}
			pending = append(pending, label)
		}
	}
	if len(pending) == 0 {
		return ""
	}
	return "waiting on: " + strings.Join(pending, ", ")
}

// detectsCycle reports whether adding deps to respID would close a dependency
// cycle: any dependency already (transitively) depends on respID.
func detectsCycle(agg *aggregate, respID string, deps []string) bool {
	for _, dep := range deps {
		if depReaches(agg, dep, respID, map[string]bool{}) {
			return true
		}
	}
	return false
}

func depReaches(agg *aggregate, from, target string, seen map[string]bool) bool {
	if from == target {
		return true
	}
	if seen[from] {
		return false
	}
	seen[from] = true
	idx := responsibilityIndex(agg, from)
	if idx < 0 {
		return false
	}
	for _, next := range agg.Plan.Responsibilities[idx].DependsOn {
		if depReaches(agg, next, target, seen) {
			return true
		}
	}
	return false
}

func responsibilityIndex(agg *aggregate, id string) int {
	for i := range agg.Plan.Responsibilities {
		if agg.Plan.Responsibilities[i].ID == id {
			return i
		}
	}
	return -1
}

func dedupe(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
