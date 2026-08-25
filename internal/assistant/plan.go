package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResponsibilityStatus is the lifecycle of one responsibility inside a plan.
type ResponsibilityStatus string

const (
	RespBlocked ResponsibilityStatus = "blocked" // waiting on an upstream dependency
	RespReady   ResponsibilityStatus = "ready"   // dependencies satisfied, actionable
	RespActive  ResponsibilityStatus = "active"  // selected and being worked
	RespDone    ResponsibilityStatus = "done"    // objective delivered
	RespFailed  ResponsibilityStatus = "failed"  // recorded failure, recoverable
)

// Responsibility is one durable objective within an assistant-level plan. It
// carries enough context to be independently actionable: what done looks like,
// the immediate next action, its dependencies, and why it is blocked.
type Responsibility struct {
	ID           string               `json:"id"`
	AssistantID  string               `json:"assistant_id"`
	Alias        string               `json:"alias,omitempty"`
	Objective    string               `json:"objective"`
	DoneCriteria string               `json:"done_criteria,omitempty"`
	NextAction   string               `json:"next_action,omitempty"`
	Status       ResponsibilityStatus `json:"status"`
	DependsOn    []string             `json:"depends_on,omitempty"`
	BlockReason  string               `json:"block_reason,omitempty"`
	Revision     int64                `json:"revision"`
	CreatedAt    time.Time            `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time            `json:"updated_at" ts_type:"string"`
}

// Artifact is a durable output or piece of evidence published by finishing
// work on a responsibility. It is tied to the assistant, the responsibility,
// and the source run that produced it.
type Artifact struct {
	ID          string    `json:"id"`
	AssistantID string    `json:"assistant_id"`
	RespID      string    `json:"resp_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Title       string    `json:"title"`
	Kind        string    `json:"kind,omitempty"`
	Content     string    `json:"content,omitempty"`
	Evidence    string    `json:"evidence,omitempty"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}

// Opportunity is a durable proposal or downstream wake-up signal pointing at a
// responsibility that has become actionable or worth considering.
type Opportunity struct {
	ID          string    `json:"id"`
	AssistantID string    `json:"assistant_id"`
	RespID      string    `json:"resp_id,omitempty"`
	RunID       string    `json:"run_id,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}

// Plan is the durable assistant-level responsibility graph. Its revision guards
// optimistic progress application: a progress patch composed against an older
// plan revision is rejected so no stale view silently rewrites the graph.
type Plan struct {
	Revision         int64            `json:"revision"`
	Responsibilities []Responsibility `json:"responsibilities"`
}

func emptyPlan() Plan {
	return Plan{Revision: 1, Responsibilities: []Responsibility{}}
}

// ProgressBlock is the bounded, structured assistant-to-plan protocol. The
// model emits one or more of these in its final answer after a successful turn;
// the runner parses and applies them before the next turn composes its context.
// Responsibilities are referenced by stable aliases, never opaque IDs, so
// re-declaring an existing alias is an idempotent no-op.
type ProgressBlock struct {
	PlanRevision     int64             `json:"plan_revision,omitempty"`
	Responsibility   string            `json:"responsibility,omitempty"` // selected alias this run worked on
	Responsibilities []RespDecl        `json:"responsibilities,omitempty"`
	Active           []string          `json:"active,omitempty"`
	Complete         []string          `json:"complete,omitempty"`
	Artifacts        []ArtifactDecl    `json:"artifacts,omitempty"`
	Opportunities    []OpportunityDecl `json:"opportunities,omitempty"`
}

// RespDecl declares (or re-declares) a responsibility by alias. Re-declaring an
// existing alias with unchanged objective/done_criteria/next_action/depends_on
// is a no-op. depends_on is a list of aliases: omitted (nil) leaves the current
// dependencies unchanged, while an explicit empty list clears them.
type RespDecl struct {
	Alias        string   `json:"alias"`
	Objective    string   `json:"objective"`
	DoneCriteria string   `json:"done_criteria,omitempty"`
	NextAction   string   `json:"next_action,omitempty"`
	DependsOn    []string `json:"depends_on,omitempty"` // aliases; nil = unchanged, [] = clear
}

// ArtifactDecl declares one durable output or evidence of a responsibility.
type ArtifactDecl struct {
	Resp     string `json:"resp"` // alias
	Title    string `json:"title"`
	Kind     string `json:"kind,omitempty"`
	Content  string `json:"content,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

// OpportunityDecl declares one downstream wake-up signal or proposal.
type OpportunityDecl struct {
	Resp   string `json:"resp"` // alias
	Reason string `json:"reason,omitempty"`
}

const (
	progressOpen  = "<assistant-progress>"
	progressClose = "</assistant-progress>"

	maxProgressBlocks    = 64
	maxProgressBytes     = 256 * 1024
	maxRespDeclarations  = 256
	maxProgressArtifacts = 256
)

// validAlias reports whether s is an acceptable responsibility alias: a short,
// stable handle restricted to letters, digits, '_' and '-' so it is unambiguous
// in block JSON and context display.
func validAlias(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ParseProgressBlocks extracts and decodes every <assistant-progress> block in
// text. Blocks are bounded in count and size, and every malformed block yields
// an explicit error so a bad patch is never silently dropped.
func ParseProgressBlocks(text string) ([]ProgressBlock, []error) {
	var blocks []ProgressBlock
	var errs []error
	rest := text
	for len(blocks) < maxProgressBlocks {
		start := strings.Index(rest, progressOpen)
		if start < 0 {
			return blocks, errs
		}
		rest = rest[start+len(progressOpen):]
		end := strings.Index(rest, progressClose)
		if end < 0 {
			errs = append(errs, errors.New("assistant: unterminated <assistant-progress> block"))
			return blocks, errs
		}
		raw := strings.TrimSpace(rest[:end])
		rest = rest[end+len(progressClose):]
		if len(raw) > maxProgressBytes {
			errs = append(errs, errors.New("assistant: <assistant-progress> block exceeds size limit"))
			continue
		}
		if raw == "" {
			continue
		}
		var b ProgressBlock
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			errs = append(errs, fmt.Errorf("assistant: malformed <assistant-progress> block: %w", err))
			continue
		}
		blocks = append(blocks, b)
	}
	if len(blocks) >= maxProgressBlocks {
		errs = append(errs, errors.New("assistant: too many <assistant-progress> blocks"))
	}
	return blocks, errs
}

// StripProgressBlocks removes every <assistant-progress> span from display text
// while leaving the raw text (and therefore the protocol blocks) intact in the
// session history. A dangling open tag drops that tag and everything after it.
func StripProgressBlocks(text string) string {
	var b strings.Builder
	rest := text
	for {
		start := strings.Index(rest, progressOpen)
		if start < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:start])
		afterOpen := rest[start+len(progressOpen):]
		end := strings.Index(afterOpen, progressClose)
		if end < 0 {
			return strings.TrimRight(b.String(), " \t\r\n")
		}
		rest = afterOpen[end+len(progressClose):]
	}
}

// MergeProgressBlocks folds the slice fields of several blocks into one. The
// plan revision and selected responsibility come from the last block that
// carries them, so callers can still override them after the merge.
func MergeProgressBlocks(blocks []ProgressBlock) ProgressBlock {
	var out ProgressBlock
	for _, b := range blocks {
		if b.PlanRevision != 0 {
			out.PlanRevision = b.PlanRevision
		}
		if b.Responsibility != "" {
			out.Responsibility = b.Responsibility
		}
		out.Responsibilities = append(out.Responsibilities, b.Responsibilities...)
		out.Active = append(out.Active, b.Active...)
		out.Complete = append(out.Complete, b.Complete...)
		out.Artifacts = append(out.Artifacts, b.Artifacts...)
		out.Opportunities = append(out.Opportunities, b.Opportunities...)
	}
	return out
}

// RebaseProgress aligns a model progress patch with the authoritative plan so
// a stale plan revision or a re-declared alias never rewrites an existing
// objective. The current plan's revision wins, and for an alias already present
// the plan's objective is authoritative while the declaration's done_criteria,
// next_action and depends_on still apply. It reports whether the patch changed.
func RebaseProgress(plan Plan, progress *ProgressBlock) bool {
	if progress == nil {
		return false
	}
	changed := false
	if progress.PlanRevision != plan.Revision {
		progress.PlanRevision = plan.Revision
		changed = true
	}
	byAlias := make(map[string]string, len(plan.Responsibilities))
	for _, r := range plan.Responsibilities {
		if alias := strings.TrimSpace(r.Alias); alias != "" {
			byAlias[alias] = r.Objective
		}
	}
	for i := range progress.Responsibilities {
		d := &progress.Responsibilities[i]
		alias := strings.TrimSpace(d.Alias)
		if alias != d.Alias {
			d.Alias = alias
			changed = true
		}
		if objective, ok := byAlias[alias]; ok && objective != strings.TrimSpace(d.Objective) {
			d.Objective = objective
			changed = true
		}
	}
	return changed
}

func progressBlockEmpty(b ProgressBlock) bool {
	return b.PlanRevision == 0 && b.Responsibility == "" && len(b.Responsibilities) == 0 &&
		len(b.Active) == 0 && len(b.Complete) == 0 && len(b.Artifacts) == 0 && len(b.Opportunities) == 0
}

func validateProgressBlock(b ProgressBlock) error {
	if len(b.Responsibilities) > maxRespDeclarations {
		return errors.New("assistant: too many responsibility declarations")
	}
	if len(b.Artifacts) > maxProgressArtifacts {
		return errors.New("assistant: too many artifact declarations")
	}
	return nil
}

func validResponsibilityStatus(s ResponsibilityStatus) bool {
	switch s {
	case RespBlocked, RespReady, RespActive, RespDone, RespFailed:
		return true
	}
	return false
}

// validatePlan checks the plan, its responsibilities, and the durable artifacts
// and opportunities against the rest of the aggregate. It is called on every
// read so a corrupt graph is surfaced instead of silently loaded.
func validatePlan(agg *aggregate) error {
	plan := agg.Plan
	if plan.Revision < 1 {
		return errors.New("plan revision must be positive")
	}
	ids := make(map[string]bool, len(plan.Responsibilities))
	aliases := make(map[string]string, len(plan.Responsibilities))
	for _, r := range plan.Responsibilities {
		if err := validateID("responsibility", r.ID); err != nil {
			return err
		}
		if r.AssistantID != agg.Assistant.ID {
			return fmt.Errorf("responsibility %s belongs to %s", r.ID, r.AssistantID)
		}
		if strings.TrimSpace(r.Objective) == "" {
			return fmt.Errorf("responsibility %s has no objective", r.ID)
		}
		if !validResponsibilityStatus(r.Status) {
			return fmt.Errorf("responsibility %s has invalid status %q", r.ID, r.Status)
		}
		if r.Revision < 1 || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
			return fmt.Errorf("responsibility %s has invalid revision or timestamps", r.ID)
		}
		if r.Alias != "" {
			if !validAlias(r.Alias) {
				return fmt.Errorf("responsibility %s has invalid alias %q", r.ID, r.Alias)
			}
			if prior, ok := aliases[r.Alias]; ok && prior != r.ID {
				return fmt.Errorf("duplicate responsibility alias %q", r.Alias)
			}
			aliases[r.Alias] = r.ID
		}
		if ids[r.ID] {
			return fmt.Errorf("duplicate responsibility %s", r.ID)
		}
		ids[r.ID] = true
	}
	for _, r := range plan.Responsibilities {
		seen := make(map[string]bool, len(r.DependsOn))
		for _, dep := range r.DependsOn {
			if dep == r.ID {
				return fmt.Errorf("responsibility %s depends on itself", r.ID)
			}
			if !ids[dep] {
				return fmt.Errorf("responsibility %s references missing dependency %s", r.ID, dep)
			}
			if seen[dep] {
				return fmt.Errorf("responsibility %s has duplicate dependency %s", r.ID, dep)
			}
			seen[dep] = true
		}
	}
	for _, r := range plan.Responsibilities {
		if detectsCycle(agg, r.ID, r.DependsOn) {
			return fmt.Errorf("responsibility graph contains a cycle involving %s", r.ID)
		}
	}
	runIDs := make(map[string]bool, len(agg.Runs))
	for _, run := range agg.Runs {
		runIDs[run.ID] = true
		if run.ResponsibilityID != "" && !ids[run.ResponsibilityID] {
			return fmt.Errorf("run %s references missing responsibility %s", run.ID, run.ResponsibilityID)
		}
	}
	for _, a := range agg.Artifacts {
		if err := validateID("artifact", a.ID); err != nil {
			return err
		}
		if a.AssistantID != agg.Assistant.ID {
			return fmt.Errorf("artifact %s belongs to %s", a.ID, a.AssistantID)
		}
		if strings.TrimSpace(a.Title) == "" {
			return fmt.Errorf("artifact %s has no title", a.ID)
		}
		if a.RespID != "" && !ids[a.RespID] {
			return fmt.Errorf("artifact %s references missing responsibility %s", a.ID, a.RespID)
		}
		if a.RunID != "" && !runIDs[a.RunID] {
			return fmt.Errorf("artifact %s references missing run %s", a.ID, a.RunID)
		}
		if a.Revision < 1 || a.CreatedAt.IsZero() {
			return fmt.Errorf("artifact %s has invalid revision or timestamp", a.ID)
		}
	}
	for _, o := range agg.Opportunities {
		if err := validateID("opportunity", o.ID); err != nil {
			return err
		}
		if o.AssistantID != agg.Assistant.ID {
			return fmt.Errorf("opportunity %s belongs to %s", o.ID, o.AssistantID)
		}
		if o.RespID != "" && !ids[o.RespID] {
			return fmt.Errorf("opportunity %s references missing responsibility %s", o.ID, o.RespID)
		}
		if o.RunID != "" && !runIDs[o.RunID] {
			return fmt.Errorf("opportunity %s references missing run %s", o.ID, o.RunID)
		}
		if o.Revision < 1 || o.CreatedAt.IsZero() {
			return fmt.Errorf("opportunity %s has invalid revision or timestamp", o.ID)
		}
	}
	return nil
}
