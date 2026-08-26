package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProposalTarget identifies the configuration object a durable improvement
// proposal may change. The allow-list is intentionally small: proposals cannot
// mutate an assistant mission, workspace, policy, channel endpoint, or secret.
type ProposalTarget string

const (
	ProposalTargetRoutine ProposalTarget = "routine"
	ProposalTargetChannel ProposalTarget = "channel"
)

type ProposalState string

const (
	ProposalPending    ProposalState = "pending"
	ProposalApplied    ProposalState = "applied"
	ProposalRejected   ProposalState = "rejected"
	ProposalSuperseded ProposalState = "superseded"
)

type ProposalDecision string

const (
	ProposalAccept ProposalDecision = "accept"
	ProposalReject ProposalDecision = "reject"
)

// RoutineProposalPatch is a typed, allow-listed patch. Nil means unchanged;
// pointers also let an enabled=false proposal remain distinct from omission.
type RoutineProposalPatch struct {
	Prompt   *string   `json:"prompt,omitempty"`
	Schedule *Schedule `json:"schedule,omitempty"`
	Enabled  *bool     `json:"enabled,omitempty"`
}

type ChannelProposalPatch struct {
	CollectIntervalSeconds *int64 `json:"collect_interval_seconds,omitempty"`
	Enabled                *bool  `json:"enabled,omitempty"`
}

type RoutineProposalChange struct {
	Before RoutineProposalPatch `json:"before"`
	After  RoutineProposalPatch `json:"after"`
}

type ChannelProposalChange struct {
	Before ChannelProposalPatch `json:"before"`
	After  ChannelProposalPatch `json:"after"`
}

// ChangeProposal freezes the target fields seen by the source run. A user
// decision later applies the typed patch only when the touched fields are still
// compatible, so unrelated scheduler-owned changes do not create false CAS
// conflicts and user edits are never overwritten.
type ChangeProposal struct {
	ID           string                 `json:"id"`
	AssistantID  string                 `json:"assistant_id"`
	RunID        string                 `json:"run_id"`
	TargetKind   ProposalTarget         `json:"target_kind"`
	TargetID     string                 `json:"target_id"`
	BaseRevision int64                  `json:"base_revision"`
	Routine      *RoutineProposalChange `json:"routine,omitempty"`
	Channel      *ChannelProposalChange `json:"channel,omitempty"`
	Summary      string                 `json:"summary"`
	Reason       string                 `json:"reason"`
	Evidence     []string               `json:"evidence"`
	State        ProposalState          `json:"state"`
	Resolution   string                 `json:"resolution,omitempty"`
	Revision     int64                  `json:"revision"`
	CreatedAt    time.Time              `json:"created_at" ts_type:"string"`
	UpdatedAt    time.Time              `json:"updated_at" ts_type:"string"`
	ResolvedAt   time.Time              `json:"resolved_at,omitempty" ts_type:"string"`
}

// ProposalDecl is the bounded model-to-store declaration carried by an
// <assistant-progress> block. The Store captures Before and BaseRevision; the
// model can only declare the desired allow-listed After values.
type ProposalDecl struct {
	TargetKind ProposalTarget        `json:"target_kind"`
	TargetID   string                `json:"target_id"`
	Routine    *RoutineProposalPatch `json:"routine,omitempty"`
	Channel    *ChannelProposalPatch `json:"channel,omitempty"`
	Summary    string                `json:"summary"`
	Reason     string                `json:"reason"`
	Evidence   []string              `json:"evidence"`
}

type ResolveProposalInput struct {
	RequestID        string
	AssistantID      string
	ProposalID       string
	ExpectedRevision int64
	Decision         ProposalDecision
	Resolution       string
	Now              time.Time
}

const (
	maxProposalText     = 16 * 1024
	maxProposalEvidence = 16
)

func validateProposalDecl(raw ProposalDecl) error {
	d := normalizeProposalDecl(raw)
	if err := validateID("proposal target", d.TargetID); err != nil {
		return err
	}
	if d.Summary == "" || d.Reason == "" {
		return errors.New("assistant: proposal summary and reason are required")
	}
	if len(d.Summary) > maxProposalText || len(d.Reason) > maxProposalText {
		return errors.New("assistant: proposal summary or reason exceeds size limit")
	}
	if len(d.Evidence) == 0 || len(d.Evidence) > maxProposalEvidence {
		return fmt.Errorf("assistant: proposal evidence must contain 1 to %d items", maxProposalEvidence)
	}
	for _, evidence := range d.Evidence {
		if evidence == "" || len(evidence) > maxProposalText {
			return errors.New("assistant: proposal evidence is empty or exceeds size limit")
		}
	}
	switch d.TargetKind {
	case ProposalTargetRoutine:
		if d.Routine == nil || d.Channel != nil {
			return errors.New("assistant: routine proposal requires exactly one routine patch")
		}
		if routinePatchEmpty(*d.Routine) {
			return errors.New("assistant: routine proposal patch is empty")
		}
		if d.Routine.Prompt != nil && strings.TrimSpace(*d.Routine.Prompt) == "" {
			return errors.New("assistant: proposed routine prompt is empty")
		}
		if d.Routine.Prompt != nil && len(*d.Routine.Prompt) > maxProposalText {
			return errors.New("assistant: proposed routine prompt exceeds size limit")
		}
		if d.Routine.Schedule != nil {
			if err := validateSchedule(*d.Routine.Schedule); err != nil {
				return fmt.Errorf("assistant: proposed routine schedule: %w", err)
			}
		}
	case ProposalTargetChannel:
		if d.Channel == nil || d.Routine != nil {
			return errors.New("assistant: channel proposal requires exactly one channel patch")
		}
		if channelPatchEmpty(*d.Channel) {
			return errors.New("assistant: channel proposal patch is empty")
		}
		if seconds := d.Channel.CollectIntervalSeconds; seconds != nil && (*seconds < 300 || *seconds > 7*24*3600) {
			return errors.New("assistant: proposed channel interval must be between 5 minutes and 7 days")
		}
	default:
		return fmt.Errorf("assistant: invalid proposal target %q", d.TargetKind)
	}
	return nil
}

func normalizeProposalDecl(in ProposalDecl) ProposalDecl {
	out := clone(in)
	out.TargetID = strings.TrimSpace(out.TargetID)
	out.Summary = strings.TrimSpace(out.Summary)
	out.Reason = strings.TrimSpace(out.Reason)
	for i := range out.Evidence {
		out.Evidence[i] = strings.TrimSpace(out.Evidence[i])
	}
	if out.Routine != nil && out.Routine.Prompt != nil {
		value := strings.TrimSpace(*out.Routine.Prompt)
		out.Routine.Prompt = &value
	}
	return out
}

func routinePatchEmpty(p RoutineProposalPatch) bool {
	return p.Prompt == nil && p.Schedule == nil && p.Enabled == nil
}

func channelPatchEmpty(p ChannelProposalPatch) bool {
	return p.CollectIntervalSeconds == nil && p.Enabled == nil
}

func appendProposals(agg *aggregate, decls []ProposalDecl, runID string, now time.Time) (bool, error) {
	changed := false
	for i, raw := range decls {
		d := normalizeProposalDecl(raw)
		if err := validateProposalDecl(d); err != nil {
			return false, err
		}
		proposal := ChangeProposal{
			AssistantID: agg.Assistant.ID,
			RunID:       runID,
			TargetKind:  d.TargetKind,
			TargetID:    d.TargetID,
			Summary:     d.Summary,
			Reason:      d.Reason,
			Evidence:    clone(d.Evidence),
			State:       ProposalPending,
			Revision:    1,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		switch d.TargetKind {
		case ProposalTargetRoutine:
			idx := routineIndex(agg, d.TargetID)
			if idx < 0 {
				return false, fmt.Errorf("assistant: proposal routine %s: %w", d.TargetID, ErrNotFound)
			}
			target := agg.Routines[idx]
			before := routineBefore(target, *d.Routine)
			if routinePatchesEqual(before, *d.Routine) {
				return false, fmt.Errorf("assistant: proposal routine %s has no change", d.TargetID)
			}
			candidate := target
			applyRoutinePatch(&candidate, *d.Routine)
			if err := validateRoutine(candidate); err != nil {
				return false, fmt.Errorf("assistant: proposed routine %s: %w", d.TargetID, err)
			}
			proposal.BaseRevision = target.Revision
			proposal.Routine = &RoutineProposalChange{Before: before, After: clone(*d.Routine)}
		case ProposalTargetChannel:
			idx := channelIndex(agg, d.TargetID)
			if idx < 0 {
				return false, fmt.Errorf("assistant: proposal channel %s: %w", d.TargetID, ErrNotFound)
			}
			target := agg.Channels[idx]
			before := channelBefore(target, *d.Channel)
			if channelPatchesEqual(before, *d.Channel) {
				return false, fmt.Errorf("assistant: proposal channel %s has no change", d.TargetID)
			}
			candidate := target
			applyChannelPatch(&candidate, *d.Channel)
			if err := validateChannel(candidate); err != nil {
				return false, fmt.Errorf("assistant: proposed channel %s: %w", d.TargetID, err)
			}
			proposal.BaseRevision = target.Revision
			proposal.Channel = &ChannelProposalChange{Before: before, After: clone(*d.Channel)}
		}
		intentHash, err := inputFingerprint(struct {
			TargetKind ProposalTarget
			TargetID   string
			Routine    *RoutineProposalChange
			Channel    *ChannelProposalChange
		}{proposal.TargetKind, proposal.TargetID, proposal.Routine, proposal.Channel})
		if err != nil {
			return false, err
		}
		proposal.ID = StableID("proposal", fmt.Sprintf("%s/%d/%s", runID, i, intentHash))
		if proposalIndex(agg, proposal.ID) >= 0 || hasPendingProposal(agg, proposal) {
			continue
		}
		if err := validateProposal(agg, proposal); err != nil {
			return false, err
		}
		agg.Proposals = append(agg.Proposals, proposal)
		changed = true
	}
	return changed, nil
}

func routineBefore(target Routine, after RoutineProposalPatch) RoutineProposalPatch {
	var before RoutineProposalPatch
	if after.Prompt != nil {
		value := target.Prompt
		before.Prompt = &value
	}
	if after.Schedule != nil {
		value := target.Schedule
		before.Schedule = &value
	}
	if after.Enabled != nil {
		value := target.Enabled
		before.Enabled = &value
	}
	return before
}

func channelBefore(target ChannelBinding, after ChannelProposalPatch) ChannelProposalPatch {
	var before ChannelProposalPatch
	if after.CollectIntervalSeconds != nil {
		value := target.CollectIntervalSeconds
		before.CollectIntervalSeconds = &value
	}
	if after.Enabled != nil {
		value := target.Enabled
		before.Enabled = &value
	}
	return before
}

func applyRoutinePatch(target *Routine, patch RoutineProposalPatch) {
	if patch.Prompt != nil {
		target.Prompt = *patch.Prompt
	}
	if patch.Schedule != nil {
		target.Schedule = *patch.Schedule
	}
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
}

func applyChannelPatch(target *ChannelBinding, patch ChannelProposalPatch) {
	if patch.CollectIntervalSeconds != nil {
		target.CollectIntervalSeconds = *patch.CollectIntervalSeconds
	}
	if patch.Enabled != nil {
		target.Enabled = *patch.Enabled
	}
}

func routineMatchesPatch(target Routine, patch RoutineProposalPatch) bool {
	return (patch.Prompt == nil || target.Prompt == *patch.Prompt) &&
		(patch.Schedule == nil || target.Schedule == *patch.Schedule) &&
		(patch.Enabled == nil || target.Enabled == *patch.Enabled)
}

func channelMatchesPatch(target ChannelBinding, patch ChannelProposalPatch) bool {
	return (patch.CollectIntervalSeconds == nil || target.CollectIntervalSeconds == *patch.CollectIntervalSeconds) &&
		(patch.Enabled == nil || target.Enabled == *patch.Enabled)
}

func routinePatchesEqual(a, b RoutineProposalPatch) bool {
	return sameStringPtr(a.Prompt, b.Prompt) && sameSchedulePtr(a.Schedule, b.Schedule) && sameBoolPtr(a.Enabled, b.Enabled)
}

func channelPatchesEqual(a, b ChannelProposalPatch) bool {
	return sameInt64Ptr(a.CollectIntervalSeconds, b.CollectIntervalSeconds) && sameBoolPtr(a.Enabled, b.Enabled)
}

func sameStringPtr(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func sameBoolPtr(a, b *bool) bool   { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func sameInt64Ptr(a, b *int64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func sameSchedulePtr(a, b *Schedule) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func hasPendingProposal(agg *aggregate, candidate ChangeProposal) bool {
	for _, current := range agg.Proposals {
		if current.State != ProposalPending || current.TargetKind != candidate.TargetKind || current.TargetID != candidate.TargetID {
			continue
		}
		switch candidate.TargetKind {
		case ProposalTargetRoutine:
			if current.Routine != nil && candidate.Routine != nil && routinePatchesEqual(current.Routine.After, candidate.Routine.After) {
				return true
			}
		case ProposalTargetChannel:
			if current.Channel != nil && candidate.Channel != nil && channelPatchesEqual(current.Channel.After, candidate.Channel.After) {
				return true
			}
		}
	}
	return false
}

func validateProposal(agg *aggregate, p ChangeProposal) error {
	if err := validateID("proposal", p.ID); err != nil {
		return err
	}
	if p.AssistantID != agg.Assistant.ID {
		return fmt.Errorf("assistant: proposal %s belongs to %s", p.ID, p.AssistantID)
	}
	if err := validateID("proposal run", p.RunID); err != nil {
		return err
	}
	if runIndex(agg, p.RunID) < 0 {
		return fmt.Errorf("assistant: proposal %s references missing run %s", p.ID, p.RunID)
	}
	if p.BaseRevision < 1 || p.Revision < 1 || p.CreatedAt.IsZero() || p.UpdatedAt.IsZero() {
		return fmt.Errorf("assistant: proposal %s has invalid revision or timestamps", p.ID)
	}
	decl := ProposalDecl{TargetKind: p.TargetKind, TargetID: p.TargetID, Summary: p.Summary, Reason: p.Reason, Evidence: p.Evidence}
	switch p.TargetKind {
	case ProposalTargetRoutine:
		if p.Routine == nil || p.Channel != nil || routineIndex(agg, p.TargetID) < 0 {
			return fmt.Errorf("assistant: proposal %s has invalid routine target", p.ID)
		}
		decl.Routine = &p.Routine.After
		if routinePatchEmpty(p.Routine.Before) || !sameRoutineMask(p.Routine.Before, p.Routine.After) || routinePatchesEqual(p.Routine.Before, p.Routine.After) {
			return fmt.Errorf("assistant: proposal %s has invalid routine baseline", p.ID)
		}
	case ProposalTargetChannel:
		if p.Channel == nil || p.Routine != nil || channelIndex(agg, p.TargetID) < 0 {
			return fmt.Errorf("assistant: proposal %s has invalid channel target", p.ID)
		}
		decl.Channel = &p.Channel.After
		if channelPatchEmpty(p.Channel.Before) || !sameChannelMask(p.Channel.Before, p.Channel.After) || channelPatchesEqual(p.Channel.Before, p.Channel.After) {
			return fmt.Errorf("assistant: proposal %s has invalid channel baseline", p.ID)
		}
	default:
		return fmt.Errorf("assistant: proposal %s has invalid target", p.ID)
	}
	if err := validateProposalDecl(decl); err != nil {
		return fmt.Errorf("assistant: proposal %s: %w", p.ID, err)
	}
	switch p.State {
	case ProposalPending:
		if p.Resolution != "" || !p.ResolvedAt.IsZero() {
			return fmt.Errorf("assistant: pending proposal %s has a resolution", p.ID)
		}
	case ProposalApplied, ProposalRejected, ProposalSuperseded:
		if strings.TrimSpace(p.Resolution) == "" || p.ResolvedAt.IsZero() {
			return fmt.Errorf("assistant: resolved proposal %s lacks resolution metadata", p.ID)
		}
	default:
		return fmt.Errorf("assistant: proposal %s has invalid state %q", p.ID, p.State)
	}
	return nil
}

func sameRoutineMask(a, b RoutineProposalPatch) bool {
	return (a.Prompt == nil) == (b.Prompt == nil) && (a.Schedule == nil) == (b.Schedule == nil) && (a.Enabled == nil) == (b.Enabled == nil)
}

func sameChannelMask(a, b ChannelProposalPatch) bool {
	return (a.CollectIntervalSeconds == nil) == (b.CollectIntervalSeconds == nil) && (a.Enabled == nil) == (b.Enabled == nil)
}

// ResolveProposal is the only proposal decision entry point. It records an
// idempotency receipt together with the proposal terminal state and any target
// update in the same aggregate replacement.
func (s *Store) ResolveProposal(in ResolveProposalInput) (ChangeProposal, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return ChangeProposal{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return ChangeProposal{}, err
	}
	if err := validateID("proposal", in.ProposalID); err != nil {
		return ChangeProposal{}, err
	}
	if in.ExpectedRevision < 1 {
		return ChangeProposal{}, errors.New("assistant: proposal expected revision must be positive")
	}
	if in.Decision != ProposalAccept && in.Decision != ProposalReject {
		return ChangeProposal{}, errors.New("assistant: proposal decision must be accept or reject")
	}
	note := strings.TrimSpace(in.Resolution)
	fp, err := inputFingerprint(struct {
		ProposalID string
		Expected   int64
		Decision   ProposalDecision
		Resolution string
	}{in.ProposalID, in.ExpectedRevision, in.Decision, note})
	if err != nil {
		return ChangeProposal{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return ChangeProposal{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return ChangeProposal{}, err
	}
	if result, ok, receiptErr := receiptResult[ChangeProposal](agg, in.RequestID, "resolve_proposal", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := proposalIndex(agg, in.ProposalID)
	if idx < 0 {
		return ChangeProposal{}, ErrNotFound
	}
	proposal := &agg.Proposals[idx]
	if proposal.Revision != in.ExpectedRevision {
		return ChangeProposal{}, conflict("proposal", proposal.ID, in.ExpectedRevision, proposal.Revision)
	}
	if proposal.State != ProposalPending {
		return ChangeProposal{}, fmt.Errorf("assistant: proposal %s is %s: %w", proposal.ID, proposal.State, ErrTransition)
	}
	now := storeNow(in.Now)
	if in.Decision == ProposalReject {
		proposal.State = ProposalRejected
		proposal.Resolution = defaultResolution(note, "rejected by user")
	} else {
		state, resolution, applyErr := applyProposalTarget(agg, *proposal, now)
		if applyErr != nil {
			return ChangeProposal{}, applyErr
		}
		proposal.State = state
		if state == ProposalSuperseded {
			proposal.Resolution = resolution
			if note != "" {
				proposal.Resolution += "; decision note: " + note
			}
		} else {
			proposal.Resolution = defaultResolution(note, resolution)
		}
	}
	proposal.Revision++
	proposal.UpdatedAt = now
	proposal.ResolvedAt = now
	result := clone(*proposal)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "resolve_proposal", fp, result, now); err != nil {
		return ChangeProposal{}, err
	}
	if err := validateAggregate(agg); err != nil {
		return ChangeProposal{}, fmt.Errorf("%w: resolve proposal %s: %v", ErrCorrupt, proposal.ID, err)
	}
	if err := s.write(agg); err != nil {
		return ChangeProposal{}, err
	}
	return result, nil
}

func applyProposalTarget(agg *aggregate, proposal ChangeProposal, now time.Time) (ProposalState, string, error) {
	switch proposal.TargetKind {
	case ProposalTargetRoutine:
		idx := routineIndex(agg, proposal.TargetID)
		if idx < 0 {
			return ProposalSuperseded, "target routine no longer exists", nil
		}
		target := &agg.Routines[idx]
		change := proposal.Routine
		if routineMatchesPatch(*target, change.After) {
			return ProposalApplied, "target already matched the proposal", nil
		}
		if target.Revision != proposal.BaseRevision && !routineMatchesPatch(*target, change.Before) {
			return ProposalSuperseded, "target routine changed after the proposal was created", nil
		}
		candidate := *target
		applyRoutinePatch(&candidate, change.After)
		candidate.Revision++
		candidate.UpdatedAt = now
		if err := validateRoutine(candidate); err != nil {
			return "", "", err
		}
		*target = candidate
		return ProposalApplied, "applied by user", nil
	case ProposalTargetChannel:
		idx := channelIndex(agg, proposal.TargetID)
		if idx < 0 {
			return ProposalSuperseded, "target channel no longer exists", nil
		}
		target := &agg.Channels[idx]
		change := proposal.Channel
		if channelMatchesPatch(*target, change.After) {
			return ProposalApplied, "target already matched the proposal", nil
		}
		if target.Revision != proposal.BaseRevision && !channelMatchesPatch(*target, change.Before) {
			return ProposalSuperseded, "target channel changed after the proposal was created", nil
		}
		candidate := *target
		applyChannelPatch(&candidate, change.After)
		candidate.Revision++
		candidate.UpdatedAt = now
		if err := validateChannel(candidate); err != nil {
			return "", "", err
		}
		*target = candidate
		return ProposalApplied, "applied by user", nil
	default:
		return "", "", errors.New("assistant: invalid proposal target")
	}
}

func defaultResolution(note, fallback string) string {
	if note != "" {
		return note
	}
	return fallback
}
