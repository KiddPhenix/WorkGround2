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
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return nil, err
	}
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
	// The run was claimed under an older work generation: its completion is a
	// late result and must not move the plan (the pause/resume fence moved).
	if wc, err := s.WorkControl(); err != nil {
		return nil, err
	} else if err := checkWorkEpoch(run.WorkEpoch, wc.Epoch); err != nil {
		return nil, err
	}
	// New plan writes are refused while the gate is QUIESCING or PAUSED;
	// recovery-driven write-back (RECOVERING) is admitted by requireResumeRunning.
	if err := s.requireResumeRunning(); err != nil {
		return nil, err
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

// RecordProgressInput applies a progress patch to the plan without a Run — the
// converged write-back path for supervisor-driven managed Sessions.
type RecordProgressInput struct {
	RequestID   string
	AssistantID string
	SessionID   string // optional, for traceability of which Session produced the patch
	Progress    ProgressBlock
	Now         time.Time
}

// RecordProgress applies a validated progress block to the plan atomically and
// idempotently by request ID. It is the Session-completion write-back for the
// new flow: the supervisor parses a completed managed Session's
// <assistant-progress> and records it here, so the plan stays the single source
// of decision state while the Session stays the single source of execution.
func (s *Store) RecordProgress(in RecordProgressInput) error {
	if err := validateRequestID(in.RequestID); err != nil {
		return err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return err
	}
	if err := validateProgressBlock(in.Progress); err != nil {
		return err
	}
	fp, err := inputFingerprint(struct {
		AssistantID, SessionID string
		Progress               ProgressBlock
	}{in.AssistantID, strings.TrimSpace(in.SessionID), in.Progress})
	if err != nil {
		return err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return err
	}
	if _, ok, receiptErr := receiptResult[struct{}](agg, in.RequestID, "record_progress", fp); ok || receiptErr != nil {
		return receiptErr
	}
	// Plan write-back is refused while the gate is QUIESCING or PAUSED;
	// RECOVERING (resume_all re-drive) is admitted.
	if err := s.requireResumeRunning(); err != nil {
		return err
	}
	now := storeNow(in.Now)
	if _, err := applyProgress(agg, in.Progress, "", now); err != nil {
		return err
	}
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "record_progress", fp, struct{}{}, now); err != nil {
		return err
	}
	if err := validateAggregate(agg); err != nil {
		return fmt.Errorf("%w: record progress: %v", ErrCorrupt, err)
	}
	return s.write(agg)
}

// SetResponsibilityDispositionInput moves one responsibility to a new durable
// decision state (planned|waiting|review|done|dropped). It is the supervisor's
// plan-decision write: work completion evidence arrives via RecordProgress
// (Complete), verification outcome via this operation (review->done), and a
// failed/abandoned direction via dropped. It never records execution state.
type SetResponsibilityDispositionInput struct {
	RequestID        string
	AssistantID      string
	ResponsibilityID string
	Disposition      ResponsibilityDisposition
	ExpectedPlanRev  int64
	Now              time.Time
}

// SetResponsibilityDisposition applies one decision-state transition under a
// plan revision CAS, idempotently by request ID. A terminal decision (done or
// dropped) is final; everything else may move among the decision states. The
// derived Status projection is refreshed before the single atomic write.
func (s *Store) SetResponsibilityDisposition(in SetResponsibilityDispositionInput) (Responsibility, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Responsibility{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Responsibility{}, err
	}
	if err := validateID("responsibility", in.ResponsibilityID); err != nil {
		return Responsibility{}, err
	}
	if !validDisposition(in.Disposition) || in.Disposition == "" {
		return Responsibility{}, fmt.Errorf("assistant: invalid disposition %q", in.Disposition)
	}
	fp, err := inputFingerprint(struct {
		AssistantID, ResponsibilityID string
		Disposition                   ResponsibilityDisposition
		ExpectedPlanRev               int64
	}{in.AssistantID, in.ResponsibilityID, in.Disposition, in.ExpectedPlanRev})
	if err != nil {
		return Responsibility{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Responsibility{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Responsibility{}, err
	}
	if result, ok, receiptErr := receiptResult[Responsibility](agg, in.RequestID, "set_resp_disposition", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	if err := s.requireResumeRunning(); err != nil {
		return Responsibility{}, err
	}
	if in.ExpectedPlanRev != 0 && agg.Plan.Revision != in.ExpectedPlanRev {
		return Responsibility{}, conflict("plan", in.AssistantID, in.ExpectedPlanRev, agg.Plan.Revision)
	}
	idx := responsibilityIndex(agg, in.ResponsibilityID)
	if idx < 0 {
		return Responsibility{}, ErrNotFound
	}
	r := &agg.Plan.Responsibilities[idx]
	if r.Disposition == in.Disposition {
		return clone(*r), nil // same decision; idempotent no-op
	}
	if r.Disposition == DispositionDone || r.Disposition == DispositionDropped {
		return Responsibility{}, fmt.Errorf("%w: responsibility %s is terminal (%s)", ErrTransition, in.ResponsibilityID, r.Disposition)
	}
	now := storeNow(in.Now)
	r.Disposition = in.Disposition
	if in.Disposition == DispositionDone || in.Disposition == DispositionDropped {
		r.BlockReason = ""
	}
	r.Revision++
	r.UpdatedAt = now
	agg.Plan.Revision++
	deriveResponsibilityStatuses(&agg.Plan)
	result := clone(*r)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "set_resp_disposition", fp, result, now); err != nil {
		return Responsibility{}, err
	}
	if err := validateAggregate(agg); err != nil {
		return Responsibility{}, fmt.Errorf("%w: set responsibility disposition: %v", ErrCorrupt, err)
	}
	if err := s.write(agg); err != nil {
		return Responsibility{}, err
	}
	return result, nil
}

// AdoptOpportunityInput promotes one opportunity to a durable responsibility
// (the Adopt stage of the expansion loop). The caller must have passed the
// Rank gate (continuous mode + policy + evidence); the store re-checks the
// duplicate and evidence invariants so adoption is safe under replay.
type AdoptOpportunityInput struct {
	RequestID     string
	AssistantID   string
	OpportunityID string
	Alias         string // optional stable alias for the new responsibility
	Now           time.Time
}

// AdoptOpportunity converts one opportunity into a planned responsibility,
// idempotently by request ID. Adoption marks the opportunity as adopted and
// never executes anything itself: the Execute stage creates the managed Session
// through the normal advance path. Duplicate objectives and missing evidence
// are refused so a replayed or guessed adoption cannot create a second plan
// item or promote a model guess.
func (s *Store) AdoptOpportunity(in AdoptOpportunityInput) (Responsibility, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Responsibility{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Responsibility{}, err
	}
	if err := validateID("opportunity", in.OpportunityID); err != nil {
		return Responsibility{}, err
	}
	in.Alias = strings.TrimSpace(in.Alias)
	if in.Alias != "" && !validAlias(in.Alias) {
		return Responsibility{}, fmt.Errorf("assistant: invalid responsibility alias %q", in.Alias)
	}
	fp, err := inputFingerprint(struct {
		AssistantID, OpportunityID, Alias string
	}{in.AssistantID, in.OpportunityID, in.Alias})
	if err != nil {
		return Responsibility{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return Responsibility{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Responsibility{}, err
	}
	if result, ok, receiptErr := receiptResult[Responsibility](agg, in.RequestID, "adopt_opportunity", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	if err := s.requireResumeRunning(); err != nil {
		return Responsibility{}, err
	}
	now := storeNow(in.Now)
	idx := -1
	for i := range agg.Opportunities {
		if agg.Opportunities[i].ID == in.OpportunityID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return Responsibility{}, ErrNotFound
	}
	opp := &agg.Opportunities[idx]
	if !opp.AdoptedAt.IsZero() {
		// Already adopted by a prior pass: return the resulting responsibility
		// if one exists, else report as already applied.
		for _, r := range agg.Plan.Responsibilities {
			if strings.TrimSpace(r.Objective) == strings.TrimSpace(opp.Objective) {
				return clone(r), nil
			}
		}
		return Responsibility{}, nil
	}
	objective := strings.TrimSpace(opp.Objective)
	if objective == "" {
		return Responsibility{}, errors.New("assistant: opportunity has no objective to adopt")
	}
	if duplicateObjective(agg, objective, in.OpportunityID) {
		return Responsibility{}, fmt.Errorf("assistant: duplicate opportunity objective %q: %w", objective, ErrConflict)
	}
	if !opportunityHasEvidence(snapshotOf(agg), *opp) {
		return Responsibility{}, fmt.Errorf("assistant: opportunity %s lacks verified evidence: %w", in.OpportunityID, ErrBlocked)
	}
	alias := in.Alias
	if alias == "" {
		alias = objectiveAlias(objective)
	}
	// Ensure the alias is unique; fall back to a suffixed form on collision.
	base := alias
	for i := 1; duplicateAlias(agg, alias); i++ {
		alias = fmt.Sprintf("%s-%d", base, i)
	}
	resp := Responsibility{
		ID:           StableID("resp", agg.Assistant.ID+"/"+alias),
		AssistantID:  agg.Assistant.ID,
		Alias:        alias,
		Objective:    objective,
		DoneCriteria: opp.Reason,
		Disposition:  DispositionPlanned,
		Revision:     1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	agg.Plan.Responsibilities = append(agg.Plan.Responsibilities, resp)
	agg.Plan.Revision++
	opp.AdoptedAt = now
	opp.Revision++
	deriveResponsibilityStatuses(&agg.Plan)
	result := clone(resp)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "adopt_opportunity", fp, result, now); err != nil {
		return Responsibility{}, err
	}
	if err := validateAggregate(agg); err != nil {
		return Responsibility{}, fmt.Errorf("%w: adopt opportunity: %v", ErrCorrupt, err)
	}
	if err := s.write(agg); err != nil {
		return Responsibility{}, err
	}
	return result, nil
}

func duplicateAlias(agg *aggregate, alias string) bool {
	for _, r := range agg.Plan.Responsibilities {
		if r.Alias == alias {
			return true
		}
	}
	return false
}

// objectiveAlias derives a stable alias from an objective: lowercased,
// non-alphanumeric runs collapsed to '-', capped at 64 chars. Empty results
// fall back to "opportunity".
func objectiveAlias(objective string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range objective {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
			lastDash = false
		case r == '_' || r == '-':
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		default:
			if !lastDash {
				b.WriteRune('-')
				lastDash = true
			}
		}
	}
	alias := strings.Trim(b.String(), "-")
	if alias == "" {
		return "opportunity"
	}
	if len(alias) > 64 {
		alias = alias[:64]
	}
	return alias
}

// RecordSessionTranscriptInput parses a completed managed Session's transcript
// and applies its <assistant-progress> to the plan.
type RecordSessionTranscriptInput struct {
	RequestID   string
	AssistantID string
	SessionID   string
	Transcript  string
	Now         time.Time
}

// RecordSessionTranscript is the write-back glue for the new flow: it extracts
// every <assistant-progress> block from a completed Session's raw transcript,
// merges them, and applies them to the plan via RecordProgress. It is idempotent
// under RequestID, and malformed blocks are surfaced rather than silently
// dropped.
func (s *Store) RecordSessionTranscript(in RecordSessionTranscriptInput) error {
	if err := validateRequestID(in.RequestID); err != nil {
		return err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return err
	}
	blocks, errs := ParseProgressBlocks(in.Transcript)
	if len(blocks) == 0 {
		if len(errs) > 0 {
			return fmt.Errorf("assistant: parse session progress: %w", errors.Join(errs...))
		}
		return nil
	}
	merged := MergeProgressBlocks(blocks)
	if err := s.RecordProgress(RecordProgressInput{
		RequestID: in.RequestID, AssistantID: in.AssistantID, SessionID: in.SessionID,
		Progress: merged, Now: in.Now,
	}); err != nil {
		return err
	}
	if len(errs) > 0 {
		return fmt.Errorf("assistant: some session progress blocks were malformed: %w", errors.Join(errs...))
	}
	return nil
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
			Disposition:  DispositionPlanned,
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
		if r.Disposition == DispositionDone && !depsDone(agg, deps) {
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

	for _, d := range b.Opportunities {
		respID, err := resolveRespAlias(aliasToID, d.Resp)
		if err != nil {
			return "", err
		}
		objective := strings.TrimSpace(d.Objective)
		// Stable dedup: an identical objective already present (as an
		// opportunity or a responsibility) is a no-op, never a second entry.
		if duplicateObjective(agg, objective) {
			continue
		}
		agg.Opportunities = append(agg.Opportunities, Opportunity{
			// The objective participates in the stable ID so distinct
			// opportunities never collide when runID is empty (RecordProgress)
			// while the same objective stays deterministically identical.
			ID:          StableID("opp", fmt.Sprintf("%s/%s/%s", runID, d.Resp, objective)),
			AssistantID: agg.Assistant.ID,
			RespID:      respID,
			RunID:       runID,
			Objective:   objective,
			Reason:      strings.TrimSpace(d.Reason),
			Revision:    1,
			CreatedAt:   now,
		})
		changed = true
	}

	proposalChanged, err := appendProposals(agg, b.Proposals, runID, now)
	if err != nil {
		return "", err
	}
	changed = changed || proposalChanged

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
		if r.Disposition == DispositionDone {
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
		if r.Disposition == DispositionDone {
			continue
		}
		// Completion is a durable decision, not an execution state: only the
		// disposition moves to done. The Status projection is recomputed from
		// the whole plan below.
		r.Disposition = DispositionDone
		r.BlockReason = ""
		r.Revision++
		r.UpdatedAt = now
		changed = true
	}

	recomputeReadiness(agg, idToAlias, now, &changed)

	for _, alias := range b.Active {
		alias = strings.TrimSpace(alias)
		// A model may report a responsibility as both active and complete in the
		// same progress block. Completion is the terminal, authoritative state;
		// treating the stale active marker as a no-op keeps the patch idempotent
		// and prevents an otherwise valid plan update from being discarded.
		if completeSet[alias] {
			continue
		}
		respID, err := resolveRespAlias(aliasToID, alias)
		if err != nil {
			return "", err
		}
		idx := responsibilityIndex(agg, respID)
		if idx < 0 {
			return "", ErrNotFound
		}
		r := &agg.Plan.Responsibilities[idx]
		if r.Disposition == DispositionDone {
			return "", fmt.Errorf("assistant: done responsibility %s cannot be activated: %w", alias, ErrTransition)
		}
		if !depsDone(agg, r.DependsOn) {
			return "", fmt.Errorf("assistant: responsibility %s dependencies are incomplete: %w", alias, ErrBlocked)
		}
		// "active" is execution state and derives from the associated Session,
		// so it is never persisted to the plan (design 4.2). The marker is
		// validated for a real, workable responsibility and then dropped.
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
	// The plan only persists decision states; recompute the derived Status
	// projection so the aggregate (and the written JSON) stays consistent with
	// the decisions.
	deriveResponsibilityStatuses(&agg.Plan)
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
	if agg.Plan.Responsibilities[idx].Disposition == DispositionDone {
		return fmt.Errorf("assistant: responsibility %s is already done: %w", alias, ErrTransition)
	}
	return nil
}

// recomputeReadiness refreshes the derived BlockReason evidence from the
// dependency graph. Decision states never move here: ready/blocked are derived
// projections (deriveResponsibilityStatuses), and done/dropped are terminal.
func recomputeReadiness(agg *aggregate, idToAlias map[string]string, now time.Time, changed *bool) {
	for i := range agg.Plan.Responsibilities {
		r := &agg.Plan.Responsibilities[i]
		if r.Disposition == DispositionDone || r.Disposition == DispositionDropped {
			continue
		}
		reason := ""
		if !depsDone(agg, r.DependsOn) {
			reason = blockReason(agg, r.DependsOn, idToAlias)
		}
		if r.BlockReason != reason {
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
		if idx < 0 || agg.Plan.Responsibilities[idx].Disposition != DispositionDone {
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
		if agg.Plan.Responsibilities[idx].Disposition == DispositionDone {
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

// duplicateObjective reports whether an objective (trimmed) is already present
// as a responsibility objective/alias or an opportunity objective (optionally
// excluding one opportunity ID — the candidate being adopted), so repeated
// discovery never floods the opportunity pool and adoption never counts the
// candidate against itself.
func duplicateObjective(agg *aggregate, objective string, excludeOpportunityID ...string) bool {
	objective = strings.TrimSpace(objective)
	if objective == "" {
		return false
	}
	for _, r := range agg.Plan.Responsibilities {
		if strings.TrimSpace(r.Objective) == objective || strings.TrimSpace(r.Alias) == objective {
			return true
		}
	}
	exclude := ""
	if len(excludeOpportunityID) > 0 {
		exclude = excludeOpportunityID[0]
	}
	for _, o := range agg.Opportunities {
		if o.ID == exclude {
			continue
		}
		if strings.TrimSpace(o.Objective) == objective {
			return true
		}
	}
	return false
}

func blockReason(agg *aggregate, deps []string, idToAlias map[string]string) string {
	var pending []string
	for _, dep := range deps {
		idx := responsibilityIndex(agg, dep)
		if idx < 0 || agg.Plan.Responsibilities[idx].Disposition != DispositionDone {
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
