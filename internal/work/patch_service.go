package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ── PatchService ───────────────────────────────────────────────────────────

// PatchService owns discussion patch preview and apply operations. It reads
// and writes authoritative V2 events through the WorkStore.
type PatchService struct {
	store    WorkStore
	defStore DefinitionRevisionStore
	planner  PatchPlanner
	clock    Clock
}

const patchPreviewTTL = 24 * time.Hour

// NewPatchService creates a PatchService backed by the given store.
func NewPatchService(store WorkStore) *PatchService {
	s := &PatchService{
		store: store,
		clock: RealClock{},
	}
	if defStore, ok := store.(DefinitionRevisionStore); ok {
		s.defStore = defStore
	}
	return s
}

// SetDefinitionStore configures the V2 definition revision store.
func (s *PatchService) SetDefinitionStore(defStore DefinitionRevisionStore) {
	s.defStore = defStore
}

// SetPlanner configures the planner used to turn discussion instructions into
// untrusted structured operations.
func (s *PatchService) SetPlanner(planner PatchPlanner) {
	s.planner = planner
}

// SetClock configures the clock used for preview expiry checks.
func (s *PatchService) SetClock(clock Clock) {
	if clock != nil {
		s.clock = clock
	}
}

// ── PreviewWorkPatch ──────────────────────────────────────────────────────

// PreviewWorkPatchInput carries the parameters for generating a patch preview.
// The planner receives the discussion plus the complete object context. Callers
// cannot inject PatchOps directly.
type PreviewWorkPatchInput struct {
	WorkID             string     `json:"workId"`
	RunID              string     `json:"runId,omitempty"`
	TaskID             string     `json:"taskId,omitempty"`
	BlockID            string     `json:"blockId,omitempty"`
	SessionID          string     `json:"sessionId"`
	Instruction        string     `json:"instruction"`
	DefinitionRevision int64      `json:"definitionRevision"`
	BlockRevision      int64      `json:"blockRevision"`
	Scope              PatchScope `json:"scope"`
	RequestID          string     `json:"requestId"`
}

// PreviewWorkPatchResult is the outcome of a PreviewWorkPatch call.
type PreviewWorkPatchResult struct {
	Preview        *WorkPatchPreview   `json:"preview"`
	Receipt        *PatchIntentReceipt `json:"receipt,omitempty"`
	Revision       int64               `json:"revision"`
	Duplicate      bool                `json:"duplicate"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	Error          string              `json:"error,omitempty"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// PreviewWorkPatch validates the proposed operations, computes impact analysis,
// and persists a structured WorkPatchPreview as a patch_previewed event.
// It never mutates the Work projection.
func (s *PatchService) PreviewWorkPatch(ctx context.Context, input PreviewWorkPatchInput) (*PreviewWorkPatchResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.BlockID = strings.TrimSpace(input.BlockID)
	input.SessionID = strings.TrimSpace(input.SessionID)
	input.Instruction = strings.TrimSpace(input.Instruction)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" {
		return nil, errors.New("work: PreviewWorkPatch: workID is required")
	}
	if input.RequestID == "" {
		return nil, errors.New("work: PreviewWorkPatch: requestID is required")
	}
	if input.Scope != PatchBlock && input.Scope != PatchWorkflow {
		return nil, fmt.Errorf("work: PreviewWorkPatch: invalid scope %q", input.Scope)
	}
	if input.RunID == "" || input.TaskID == "" || input.BlockID == "" || input.SessionID == "" {
		return nil, errors.New("work: PreviewWorkPatch: runID/taskID/blockID/sessionID are required")
	}
	if input.Instruction == "" {
		return nil, errors.New("work: PreviewWorkPatch: instruction is required")
	}
	if input.DefinitionRevision <= 0 {
		return nil, errors.New("work: PreviewWorkPatch: definitionRevision must be positive")
	}

	eventRequestID := input.RequestID + "/preview"
	current, state, err := s.store.LoadState(input.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}

	intentDigest := hashPatchIntent("PreviewWorkPatch", input)

	// Idempotency check.
	if state.RequestFound {
		if state.RequestType != EventPatchPreviewed {
			return nil, fmt.Errorf("%w: PreviewWorkPatch requestID %q already used for %q",
				ErrWorkRequestIDConflict, input.RequestID, state.RequestType)
		}
		return s.replayPreview(current, input, intentDigest)
	}

	if current.SchemaVersion < SchemaVersionV2 {
		return nil, errors.New("work: PreviewWorkPatch: Work is schema V1")
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: PreviewWorkPatch: Work is %s", current.ArchiveState)
	}

	// Validate definition revision exists.
	def, err := s.loadDefinition(input.WorkID, input.DefinitionRevision)
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: %w", err)
	}

	if input.DefinitionRevision != current.V2CurrentRevision {
		return nil, fmt.Errorf("work: PreviewWorkPatch: base definition revision mismatch: requested %d, current %d",
			input.DefinitionRevision, current.V2CurrentRevision)
	}
	now := s.clock.Now().UTC()
	run, task, block, targetNodeID, materializeBlock, err := resolveDiscussionBlock(
		current,
		def,
		input.RunID,
		input.TaskID,
		input.BlockID,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: %w", err)
	}
	if input.BlockRevision != block.Revision {
		return nil, fmt.Errorf("work: PreviewWorkPatch: base block revision mismatch: requested %d, current %d",
			input.BlockRevision, block.Revision)
	}
	if s.planner == nil {
		return nil, errors.New("work: PreviewWorkPatch: no PatchPlanner configured")
	}
	plannerWork := cloneWorkForPatch(current)
	if materializeBlock && plannerWork != nil {
		plannerWork.Blocks = append(plannerWork.Blocks, cloneDiscussionBlock(*block))
	}
	plan, err := s.planner.PlanPatch(ctx, PatchPlanInput{
		Instruction:  input.Instruction,
		SessionID:    input.SessionID,
		Scope:        input.Scope,
		TargetNodeID: targetNodeID,
		Work:         plannerWork,
		Definition:   def,
		Run:          run,
		Task:         task,
		Block:        block,
	})
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: planner: %w", err)
	}
	if plan == nil {
		return nil, errors.New("work: PreviewWorkPatch: planner returned no plan")
	}
	for _, action := range plan.Actions {
		if action.Action == PatchActionAskUser {
			return nil, fmt.Errorf("work: PreviewWorkPatch: coordinator requires user input: %s",
				strings.TrimSpace(action.Question))
		}
	}
	operations, err := normalizePatchOps(def, block, input.Scope, input.BlockID, plan.Operations)
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: normalize: %w", err)
	}
	actions, err := normalizePatchActions(current, def, operations, input.Scope, plan.Actions)
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: semantic actions: %w", err)
	}
	if input.Scope == PatchWorkflow {
		candidate := CopyOnWriteRevision(def)
		if err := applyPatchOpsToDefinition(candidate, operations); err != nil {
			return nil, fmt.Errorf("work: PreviewWorkPatch: apply preview: %w", err)
		}
		if err := validatePatchedDefinition(def, candidate); err != nil {
			return nil, fmt.Errorf("work: PreviewWorkPatch: invalid workflow change: %w", err)
		}
	}

	// Compute impact analysis.
	impact := s.computePatchImpactWithActions(def, operations, input.Scope, targetNodeID, actions)
	affectedNodes := impact.affectedNodes
	invalidatedTasks := impact.invalidatedTasks
	requiresRerun := impact.requiresRerun

	// Build preview.
	preview := &WorkPatchPreview{
		ID:                      input.RequestID + "-patch",
		WorkID:                  input.WorkID,
		RunID:                   input.RunID,
		TaskID:                  input.TaskID,
		BlockID:                 input.BlockID,
		SessionID:               input.SessionID,
		BaseDefinitionRev:       input.DefinitionRevision,
		BaseBlockRev:            input.BlockRevision,
		Scope:                   input.Scope,
		Operations:              clonePatchOps(operations),
		Actions:                 clonePatchActions(actions),
		AffectedNodeIDs:         clonePatchStrings(affectedNodes),
		AffectedBlockIDs:        clonePatchStrings(impact.affectedBlocks),
		AffectedArtifactSlotIDs: clonePatchStrings(impact.affectedSlots),
		StaleArtifactSlotIDs:    clonePatchStrings(impact.staleSlots),
		InvalidatedTaskIDs:      clonePatchStrings(invalidatedTasks),
		RequiresRerun:           requiresRerun,
		ExpiresAt:               now.Add(patchPreviewTTL),
	}
	preview.Digest = hashPatchPreviewDigest(preview)

	previewRevision := state.Revision + 1
	if materializeBlock {
		previewRevision++
	}
	receipt := &PatchIntentReceipt{
		RequestID:      input.RequestID,
		Operation:      "PreviewWorkPatch",
		IntentDigest:   intentDigest,
		PatchID:        preview.ID,
		ResultRevision: previewRevision,
		ResultDigest:   preview.Digest,
		ResultPatch:    preview,
		Scope:          input.Scope,
		RequiresRerun:  preview.RequiresRerun,
		CreatedAt:      now,
	}

	payload, err := json.Marshal(PatchPreviewedPayload{
		PatchID:                 preview.ID,
		WorkID:                  input.WorkID,
		RunID:                   input.RunID,
		TaskID:                  input.TaskID,
		BlockID:                 input.BlockID,
		SessionID:               input.SessionID,
		Scope:                   input.Scope,
		BaseDefinitionRev:       input.DefinitionRevision,
		BaseBlockRev:            input.BlockRevision,
		Operations:              clonePatchOps(operations),
		Actions:                 clonePatchActions(actions),
		AffectedNodeIDs:         clonePatchStrings(affectedNodes),
		AffectedBlockIDs:        clonePatchStrings(impact.affectedBlocks),
		AffectedArtifactSlotIDs: clonePatchStrings(impact.affectedSlots),
		StaleArtifactSlotIDs:    clonePatchStrings(impact.staleSlots),
		InvalidatedTasks:        clonePatchStrings(invalidatedTasks),
		RequiresRerun:           requiresRerun,
		Digest:                  preview.Digest,
		ExpiresAt:               &preview.ExpiresAt,
		Receipt:                 receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: encode event: %w", err)
	}

	events := make([]WorkEvent, 0, 2)
	nextRevision := state.Revision
	if materializeBlock {
		blockPayload, blockErr := json.Marshal(block)
		if blockErr != nil {
			return nil, fmt.Errorf("work: PreviewWorkPatch: encode discussion block: %w", blockErr)
		}
		blockEvent := newServiceEvent(
			input.WorkID,
			input.RequestID+"/discussion-block",
			EventBlockUpserted,
			blockPayload,
			now,
		)
		blockEvent.BaseRevision, blockEvent.Revision = nextRevision, nextRevision+1
		blockEvent.Object = ObjectContext{
			Kind: ObjectBlock, ID: block.ID, ParentID: input.WorkID,
			WorkID: input.WorkID, BlockID: block.ID,
		}
		events = append(events, blockEvent)
		nextRevision = blockEvent.Revision
	}
	event := newServiceEventV2(input.WorkID, eventRequestID, EventPatchPreviewed, payload, now)
	event.BaseRevision, event.Revision = nextRevision, nextRevision+1
	event.Object = ObjectContext{
		Kind: ObjectPatch, ID: preview.ID, WorkID: input.WorkID,
		RunID: input.RunID, TaskID: input.TaskID, BlockID: input.BlockID,
		PatchID: preview.ID, DefinitionRevision: int64Ptr(input.DefinitionRevision),
	}

	events = append(events, event)
	revisions, err := s.store.CommitEvents(input.WorkID, events)
	if err != nil {
		return nil, fmt.Errorf("work: PreviewWorkPatch: commit: %w", err)
	}
	revision := revisions[len(revisions)-1]
	receipt.ResultRevision = revision

	cpy := *preview
	cpy.Operations = clonePatchOps(preview.Operations)
	cpy.Actions = clonePatchActions(preview.Actions)
	cpy.AffectedNodeIDs = clonePatchStrings(preview.AffectedNodeIDs)
	cpy.AffectedBlockIDs = clonePatchStrings(preview.AffectedBlockIDs)
	cpy.AffectedArtifactSlotIDs = clonePatchStrings(preview.AffectedArtifactSlotIDs)
	cpy.StaleArtifactSlotIDs = clonePatchStrings(preview.StaleArtifactSlotIDs)
	cpy.InvalidatedTaskIDs = clonePatchStrings(preview.InvalidatedTaskIDs)
	return &PreviewWorkPatchResult{Preview: &cpy, Revision: revision, Committed: true}, nil
}

func (s *PatchService) replayPreview(current *Work, input PreviewWorkPatchInput, intentDigest string) (*PreviewWorkPatchResult, error) {
	if current == nil || current.V2PatchReceipts == nil {
		return nil, fmt.Errorf("%w: patch preview receipt for %q is missing", ErrWorkNeedsRepair, input.RequestID)
	}
	receipt, ok := current.V2PatchReceipts[input.RequestID]
	if !ok {
		return nil, fmt.Errorf("%w: patch preview receipt for %q is missing", ErrWorkNeedsRepair, input.RequestID)
	}
	if receipt.Operation != "PreviewWorkPatch" || receipt.IntentDigest != intentDigest {
		return nil, fmt.Errorf("%w: requestID %q already used by a different preview intent",
			ErrWorkRequestIDConflict, input.RequestID)
	}
	if receipt.ResultPatch != nil {
		cpy := *receipt.ResultPatch
		cpy.Operations = clonePatchOps(receipt.ResultPatch.Operations)
		cpy.Actions = clonePatchActions(receipt.ResultPatch.Actions)
		cpy.AffectedNodeIDs = clonePatchStrings(receipt.ResultPatch.AffectedNodeIDs)
		cpy.AffectedBlockIDs = clonePatchStrings(receipt.ResultPatch.AffectedBlockIDs)
		cpy.AffectedArtifactSlotIDs = clonePatchStrings(receipt.ResultPatch.AffectedArtifactSlotIDs)
		cpy.StaleArtifactSlotIDs = clonePatchStrings(receipt.ResultPatch.StaleArtifactSlotIDs)
		cpy.InvalidatedTaskIDs = clonePatchStrings(receipt.ResultPatch.InvalidatedTaskIDs)
		return &PreviewWorkPatchResult{
			Preview: &cpy, Revision: receipt.ResultRevision, Duplicate: true, Committed: true,
		}, nil
	}
	return nil, fmt.Errorf("%w: patch preview receipt %q missing result", ErrWorkNeedsRepair, input.RequestID)
}

// ── ApplyWorkPatch ────────────────────────────────────────────────────────

// ApplyWorkPatchInput carries the parameters for applying a previewed patch.
type ApplyWorkPatchInput struct {
	WorkID           string     `json:"workId"`
	PatchID          string     `json:"patchId"`
	PreviewDigest    string     `json:"previewDigest"`
	Scope            PatchScope `json:"scope"`
	ExpectedRevision int64      `json:"expectedRevision"`
	RequestID        string     `json:"requestId"`
}

// ApplyWorkPatchResult is the outcome of an ApplyWorkPatch call.
type ApplyWorkPatchResult struct {
	Receipt                 *PatchIntentReceipt `json:"receipt,omitempty"`
	WorkRevision            int64               `json:"workRevision"`
	NewRevision             int64               `json:"newRevision"`
	InvalidatedTaskIDs      []string            `json:"invalidatedTaskIds,omitempty"`
	AffectedBlockIDs        []string            `json:"affectedBlockIds,omitempty"`
	AffectedArtifactSlotIDs []string            `json:"affectedArtifactSlotIds,omitempty"`
	StaleArtifactSlotIDs    []string            `json:"staleArtifactSlotIds,omitempty"`
	RequiresRerun           bool                `json:"requiresRerun"`
	Duplicate               bool                `json:"duplicate"`
	Error                   string              `json:"error,omitempty"`
	Committed               bool                `json:"committed"`
	Recoverable             bool                `json:"recoverable"`
	TransportError          *WorkTransportError `json:"transportError,omitempty"`
}

// ApplyWorkPatch applies a previously previewed patch. It validates:
// - expectedRevision matches current Work revision
// - preview exists and has not expired
// - base revisions match current definition and block revisions
// - scope matches the preview
// - request is idempotent (same requestID + same intent replays result)
// - request is not conflicting (same requestID + different intent → conflict)
//
// For block scope: atomically modifies the target Block.
// For workflow scope: creates a new definition revision via copy-on-write
// and applies it.
func (s *PatchService) ApplyWorkPatch(ctx context.Context, input ApplyWorkPatchInput) (*ApplyWorkPatchResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.PatchID = strings.TrimSpace(input.PatchID)
	input.PreviewDigest = strings.TrimSpace(input.PreviewDigest)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.PatchID == "" || input.PreviewDigest == "" || input.RequestID == "" {
		return nil, errors.New("work: ApplyWorkPatch: workID/patchID/previewDigest/requestID are required")
	}
	if input.Scope != PatchBlock && input.Scope != PatchWorkflow {
		return nil, fmt.Errorf("work: ApplyWorkPatch: invalid scope %q", input.Scope)
	}

	eventRequestID := input.RequestID + "/apply"
	current, state, err := s.store.LoadState(input.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}

	intentDigest := hashPatchIntent("ApplyWorkPatch", input)

	// Idempotency check.
	if state.RequestFound {
		if state.RequestType != EventPatchApplied {
			return nil, fmt.Errorf("%w: ApplyWorkPatch requestID %q already used for %q",
				ErrWorkRequestIDConflict, input.RequestID, state.RequestType)
		}
		return s.replayApply(current, input, intentDigest)
	}

	if current.SchemaVersion < SchemaVersionV2 {
		return nil, errors.New("work: ApplyWorkPatch: Work is schema V1")
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: ApplyWorkPatch: Work is %s", current.ArchiveState)
	}

	// Validate expected revision.
	if input.ExpectedRevision != state.Revision {
		return &ApplyWorkPatchResult{WorkRevision: state.Revision,
			Error: revisionConflict(input.WorkID, input.ExpectedRevision, state.Revision).Error()}, nil
	}

	// Load the preview.
	preview, err := s.loadPreview(current, input.PatchID)
	if err != nil {
		return nil, fmt.Errorf("work: ApplyWorkPatch: %w", err)
	}

	// Validate scope match.
	if preview.Scope != input.Scope {
		return nil, fmt.Errorf("work: ApplyWorkPatch: scope mismatch: preview is %q, apply is %q",
			preview.Scope, input.Scope)
	}

	// Validate expiry.
	now := s.clock.Now().UTC()
	if !preview.ExpiresAt.IsZero() && now.After(preview.ExpiresAt) {
		return nil, fmt.Errorf("work: ApplyWorkPatch: preview %q expired at %s",
			input.PatchID, preview.ExpiresAt.Format(time.RFC3339))
	}

	// Validate base revisions.
	if preview.BaseDefinitionRev > 0 && preview.BaseDefinitionRev != current.V2CurrentRevision {
		return nil, fmt.Errorf("work: ApplyWorkPatch: base definition revision mismatch: preview %d, current %d",
			preview.BaseDefinitionRev, current.V2CurrentRevision)
	}
	_, _, block, resolveErr := resolvePatchContext(current, preview.RunID, preview.TaskID, preview.BlockID)
	if resolveErr != nil {
		return nil, fmt.Errorf("work: ApplyWorkPatch: %w", resolveErr)
	}
	if preview.BaseBlockRev != block.Revision {
		return nil, fmt.Errorf("work: ApplyWorkPatch: base block revision mismatch: preview %d, current %d",
			preview.BaseBlockRev, block.Revision)
	}

	// Bind apply to the exact immutable preview body.
	if preview.Digest == "" || input.PreviewDigest != preview.Digest ||
		hashPatchPreviewDigest(preview) != preview.Digest {
		return nil, fmt.Errorf("work: ApplyWorkPatch: preview digest mismatch")
	}
	baseDefinition, err := s.loadDefinition(input.WorkID, preview.BaseDefinitionRev)
	if err != nil {
		return nil, fmt.Errorf("work: ApplyWorkPatch: load base definition: %w", err)
	}
	if _, err := normalizePatchOps(baseDefinition, block, preview.Scope, preview.BlockID, preview.Operations); err != nil {
		return nil, fmt.Errorf("work: ApplyWorkPatch: invalid preview body: %w", err)
	}

	var newRevision int64
	var invalidatedIDs []string

	switch input.Scope {
	case PatchBlock:
		invalidatedIDs = append([]string(nil), preview.InvalidatedTaskIDs...)
		newRevision, err = s.applyBlockPatch(ctx, current, preview, input, eventRequestID, intentDigest, state, now)
	case PatchWorkflow:
		newRevision, invalidatedIDs, err = s.applyWorkflowPatch(ctx, current, preview, input, eventRequestID, intentDigest, state, now)
	}
	if err != nil {
		return nil, err
	}

	return &ApplyWorkPatchResult{
		WorkRevision:            newRevision,
		NewRevision:             patchTargetRevision(current, preview, newRevision),
		InvalidatedTaskIDs:      invalidatedIDs,
		AffectedBlockIDs:        append([]string(nil), preview.AffectedBlockIDs...),
		AffectedArtifactSlotIDs: append([]string(nil), preview.AffectedArtifactSlotIDs...),
		StaleArtifactSlotIDs:    append([]string(nil), preview.StaleArtifactSlotIDs...),
		RequiresRerun:           preview.RequiresRerun,
		Committed:               true,
	}, nil
}

func (s *PatchService) applyBlockPatch(ctx context.Context, current *Work, preview *WorkPatchPreview,
	input ApplyWorkPatchInput, eventRequestID, intentDigest string, state WorkEventState, now time.Time) (int64, error) {
	_, _, block, err := resolvePatchContext(current, preview.RunID, preview.TaskID, preview.BlockID)
	if err != nil {
		return 0, fmt.Errorf("work: ApplyWorkPatch: %w", err)
	}
	updated := *block
	updated.Data = append(json.RawMessage(nil), block.Data...)
	if err := applyPatchOpsToBlock(&updated, preview.Operations); err != nil {
		return 0, fmt.Errorf("work: ApplyWorkPatch: apply block ops: %w", err)
	}
	updated.Revision = block.Revision + 1
	updated.UpdatedAt = now
	blockPayload, err := json.Marshal(updated)
	if err != nil {
		return 0, fmt.Errorf("work: ApplyWorkPatch: encode block: %w", err)
	}
	blockEvent := newServiceEvent(input.WorkID, input.RequestID+"/block", EventBlockUpserted, blockPayload, now)
	blockEvent.BaseRevision, blockEvent.Revision = state.Revision, state.Revision+1

	receipt := &PatchIntentReceipt{
		RequestID:               input.RequestID,
		Operation:               "ApplyWorkPatch",
		IntentDigest:            intentDigest,
		PatchID:                 input.PatchID,
		ResultRevision:          state.Revision + 2,
		Scope:                   PatchBlock,
		NewRevision:             updated.Revision,
		InvalidatedIDs:          append([]string(nil), preview.InvalidatedTaskIDs...),
		AffectedBlockIDs:        append([]string(nil), preview.AffectedBlockIDs...),
		AffectedArtifactSlotIDs: append([]string(nil), preview.AffectedArtifactSlotIDs...),
		StaleArtifactSlotIDs:    append([]string(nil), preview.StaleArtifactSlotIDs...),
		RequiresRerun:           preview.RequiresRerun,
		CreatedAt:               now,
	}

	payload, err := json.Marshal(PatchAppliedPayload{
		PatchID:            input.PatchID,
		WorkID:             input.WorkID,
		RunID:              preview.RunID,
		TaskID:             preview.TaskID,
		BlockID:            preview.BlockID,
		Scope:              PatchBlock,
		NewRevision:        updated.Revision,
		ExpectedRevision:   input.ExpectedRevision,
		InvalidatedTaskIDs: append([]string(nil), preview.InvalidatedTaskIDs...),
		Receipt:            receipt,
	})
	if err != nil {
		return 0, fmt.Errorf("work: ApplyWorkPatch: encode event: %w", err)
	}

	event := newServiceEventV2(input.WorkID, eventRequestID, EventPatchApplied, payload, now)
	event.BaseRevision, event.Revision = blockEvent.Revision, blockEvent.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectPatch, ID: input.PatchID, WorkID: input.WorkID,
		RunID: preview.RunID, TaskID: preview.TaskID, BlockID: preview.BlockID,
		PatchID: input.PatchID, ExpectedRevision: int64Ptr(input.ExpectedRevision),
		DefinitionRevision: int64Ptr(preview.BaseDefinitionRev),
	}

	revisions, err := s.store.CommitEvents(input.WorkID, []WorkEvent{blockEvent, event})
	if err != nil {
		return 0, fmt.Errorf("work: ApplyWorkPatch: commit block patch: %w", err)
	}
	return revisions[len(revisions)-1], nil
}

func (s *PatchService) applyWorkflowPatch(ctx context.Context, current *Work, preview *WorkPatchPreview,
	input ApplyWorkPatchInput, eventRequestID, intentDigest string, state WorkEventState, now time.Time) (int64, []string, error) {

	if s.defStore == nil {
		return 0, nil, errors.New("work: ApplyWorkPatch: no definition store configured for workflow scope patch")
	}

	// Load the current definition.
	parent, err := s.defStore.LoadRevision(input.WorkID, current.V2CurrentRevision)
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: load current definition revision %d: %w",
			current.V2CurrentRevision, err)
	}

	// Create a copy-on-write revision.
	newRev := CopyOnWriteRevision(parent)
	newRev.CreatedBy = "patch:" + input.PatchID + "/request:" + input.RequestID
	newRev.CreatedAt = preview.ExpiresAt.Add(-patchPreviewTTL).UTC()

	// Apply patch operations to the new revision.
	if err := applyPatchOpsToDefinition(newRev, preview.Operations); err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: apply ops: %w", err)
	}

	// Compute impact.
	impact := classifyRunImpactWithActions(parent, newRev, preview.Actions)
	invalidatedIDs := mergeSortedIDs(impact.InvalidatedNodeIDs, preview.InvalidatedTaskIDs)

	// Compute digest.
	digest, err := ComputeV2RevisionDigest(newRev)
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: compute digest: %w", err)
	}
	newRev.Digest = digest
	if err := validatePatchedDefinition(parent, newRev); err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: invalid patched definition: %w", err)
	}

	// The immutable body is written before the authoritative event batch. A
	// failed batch may leave only an unreferenced body, never a partially
	// applied business projection. CreatedAt is derived from the immutable
	// preview, so the same intent recreates identical bytes after restart.
	existing, loadErr := s.defStore.LoadRevision(input.WorkID, newRev.Revision)
	switch {
	case loadErr == nil:
		if existing.Digest != newRev.Digest || existing.CreatedBy != newRev.CreatedBy {
			return 0, nil, fmt.Errorf("%w: orphan definition revision %d belongs to a different patch intent",
				ErrWorkRequestIDConflict, newRev.Revision)
		}
	case errors.Is(loadErr, ErrWorkNotFound):
		if err := s.defStore.StoreRevision(input.WorkID, newRev); err != nil {
			return 0, nil, fmt.Errorf("work: ApplyWorkPatch: store revision: %w", err)
		}
	default:
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: inspect revision %d: %w", newRev.Revision, loadErr)
	}
	persisted, err := s.defStore.LoadRevision(input.WorkID, newRev.Revision)
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: reload revision: %w", err)
	}
	impact = classifyRunImpactWithActions(parent, persisted, preview.Actions)
	invalidatedIDs = mergeSortedIDs(impact.InvalidatedNodeIDs, preview.InvalidatedTaskIDs)

	createPayload, _ := json.Marshal(DefRevisionCreatedPayload{
		WorkID: input.WorkID, Revision: newRev.Revision,
		ParentRevision: newRev.ParentRevision, Digest: newRev.Digest,
	})
	createEvent := newServiceEventV2(input.WorkID, input.RequestID+"/definition-created",
		EventDefRevisionCreated, createPayload, now)
	createEvent.BaseRevision, createEvent.Revision = state.Revision, state.Revision+1
	createEvent.Object = ObjectContext{
		Kind: ObjectDefinition, ID: input.WorkID, WorkID: input.WorkID,
		DefinitionID: input.WorkID, DefinitionRevision: int64Ptr(newRev.Revision),
	}

	definitionRequestID := input.RequestID + "/definition"
	runID := workflowRunID(input.WorkID, definitionRequestID)
	run := WorkflowRun{
		ID: runID, WorkID: input.WorkID, RequestID: definitionRequestID,
		DefinitionDigest: newRev.Digest, State: RunPending,
		Stages: buildV2Stages(newRev, now), StartedAt: now,
	}
	defReceipt := &V2IntentReceipt{
		RequestID: definitionRequestID, Operation: "ApplyDefinition",
		IntentDigest: "patch-" + intentDigest, ResultRevision: newRev.Revision,
		ResultDigest: newRev.Digest, ResultRunID: runID,
		Impact: impactToJSON(impact), CreatedAt: now,
	}
	runPayload, _ := json.Marshal(runEventPayload{
		Run: run, WorkState: WorkRunning, V2Receipt: defReceipt,
	})
	runEvent := newServiceEvent(input.WorkID, definitionRequestID+"/run", EventRunStarted, runPayload, now)
	runEvent.BaseRevision, runEvent.Revision = createEvent.Revision, createEvent.Revision+1

	applyPayload, _ := json.Marshal(DefRevisionAppliedPayload{
		WorkID: input.WorkID, Revision: newRev.Revision,
		PreviousRevision: newRev.ParentRevision, ExpectedRevision: input.ExpectedRevision,
		InvalidatedTasks: append([]string(nil), invalidatedIDs...),
	})
	applyEvent := newServiceEventV2(input.WorkID, definitionRequestID+"/apply",
		EventDefRevisionApplied, applyPayload, now)
	applyEvent.BaseRevision, applyEvent.Revision = runEvent.Revision, runEvent.Revision+1
	applyEvent.Object = ObjectContext{
		Kind: ObjectDefinition, ID: input.WorkID, WorkID: input.WorkID,
		DefinitionID: input.WorkID, DefinitionRevision: int64Ptr(newRev.Revision),
		ExpectedRevision: int64Ptr(input.ExpectedRevision),
	}

	events := []WorkEvent{createEvent, runEvent, applyEvent}
	reuseEvents, err := buildKeptContextEvents(
		current,
		parent,
		persisted,
		runID,
		impact,
		now,
	)
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: project kept contexts: %w", err)
	}
	lastRevision := applyEvent.Revision
	for i := range reuseEvents {
		reuseEvents[i].BaseRevision = lastRevision
		reuseEvents[i].Revision = lastRevision + 1
		lastRevision = reuseEvents[i].Revision
		events = append(events, reuseEvents[i])
	}
	slotEvents := BuildDeclareEvents(input.WorkID, newRev.Revision, input.RequestID+"/slots",
		newRev.ArtifactSlots, now)
	for i := range slotEvents {
		slotEvents[i].BaseRevision = lastRevision
		slotEvents[i].Revision = lastRevision + 1
		lastRevision = slotEvents[i].Revision
		events = append(events, slotEvents[i])
	}

	receipt := &PatchIntentReceipt{
		RequestID:               input.RequestID,
		Operation:               "ApplyWorkPatch",
		IntentDigest:            intentDigest,
		PatchID:                 input.PatchID,
		ResultRevision:          lastRevision + 1,
		ResultDigest:            digest,
		Scope:                   PatchWorkflow,
		NewRevision:             newRev.Revision,
		InvalidatedIDs:          invalidatedIDs,
		AffectedBlockIDs:        append([]string(nil), preview.AffectedBlockIDs...),
		AffectedArtifactSlotIDs: append([]string(nil), preview.AffectedArtifactSlotIDs...),
		StaleArtifactSlotIDs:    append([]string(nil), preview.StaleArtifactSlotIDs...),
		RequiresRerun:           preview.RequiresRerun,
		CreatedAt:               now,
	}

	payload, err := json.Marshal(PatchAppliedPayload{
		PatchID:            input.PatchID,
		WorkID:             input.WorkID,
		RunID:              preview.RunID,
		TaskID:             preview.TaskID,
		BlockID:            preview.BlockID,
		Scope:              PatchWorkflow,
		NewRevision:        newRev.Revision,
		ExpectedRevision:   input.ExpectedRevision,
		InvalidatedTaskIDs: invalidatedIDs,
		Receipt:            receipt,
	})
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: encode event: %w", err)
	}

	event := newServiceEventV2(input.WorkID, eventRequestID, EventPatchApplied, payload, now)
	event.BaseRevision, event.Revision = lastRevision, lastRevision+1
	event.Object = ObjectContext{
		Kind: ObjectPatch, ID: input.PatchID, WorkID: input.WorkID,
		RunID: preview.RunID, TaskID: preview.TaskID, BlockID: preview.BlockID,
		PatchID: input.PatchID, ExpectedRevision: int64Ptr(input.ExpectedRevision),
		DefinitionRevision: int64Ptr(current.V2CurrentRevision),
	}

	events = append(events, event)
	revisions, err := s.store.CommitEvents(input.WorkID, events)
	if err != nil {
		return 0, nil, fmt.Errorf("work: ApplyWorkPatch: commit workflow patch: %w", err)
	}
	return revisions[len(revisions)-1], invalidatedIDs, nil
}

func (s *PatchService) replayApply(current *Work, input ApplyWorkPatchInput, intentDigest string) (*ApplyWorkPatchResult, error) {
	if current == nil || current.V2PatchReceipts == nil {
		return nil, fmt.Errorf("%w: patch apply receipt for %q is missing", ErrWorkNeedsRepair, input.RequestID)
	}
	receipt, ok := current.V2PatchReceipts[input.RequestID]
	if !ok {
		return nil, fmt.Errorf("%w: patch apply receipt for %q is missing", ErrWorkNeedsRepair, input.RequestID)
	}
	if receipt.Operation != "ApplyWorkPatch" || receipt.IntentDigest != intentDigest {
		return nil, fmt.Errorf("%w: requestID %q already used by a different apply intent",
			ErrWorkRequestIDConflict, input.RequestID)
	}
	return &ApplyWorkPatchResult{
		WorkRevision:            receipt.ResultRevision,
		NewRevision:             receipt.NewRevision,
		InvalidatedTaskIDs:      append([]string(nil), receipt.InvalidatedIDs...),
		AffectedBlockIDs:        append([]string(nil), receipt.AffectedBlockIDs...),
		AffectedArtifactSlotIDs: append([]string(nil), receipt.AffectedArtifactSlotIDs...),
		StaleArtifactSlotIDs:    append([]string(nil), receipt.StaleArtifactSlotIDs...),
		RequiresRerun:           receipt.RequiresRerun,
		Duplicate:               true,
		Committed:               true,
	}, nil
}

// ── Impact analysis ──────────────────────────────────────────────────────

type patchImpact struct {
	affectedNodes    []string
	affectedBlocks   []string
	affectedSlots    []string
	staleSlots       []string
	invalidatedTasks []string
	requiresRerun    bool
}

func (s *PatchService) computePatchImpact(
	def *WorkDefinitionRevision,
	ops []PatchOp,
	_ PatchScope,
	targetNodeID string,
) patchImpact {
	affected := make(map[string]bool)
	affectedBlocks := make(map[string]bool)
	affectedSlots := make(map[string]bool)
	staleSlots := make(map[string]bool)
	removedSlots := make(map[string]bool)
	for _, op := range ops {
		pp, err := CompilePatchPath(op.Path)
		if err != nil {
			continue
		}
		switch pp.Kind {
		case PathNodes:
			if len(pp.Segments) >= 2 {
				nodeID := pp.Segments[1]
				affected[nodeID] = true
			}
		case PathSpecs:
			// Find which nodes reference this spec.
			if len(pp.Segments) >= 2 {
				specID := pp.Segments[1]
				for _, node := range def.Nodes {
					for _, isID := range node.InputSpecIDs {
						if isID == specID {
							affected[node.ID] = true
						}
					}
				}
			}
		case PathBlocks:
			if len(pp.Segments) >= 2 {
				blockID := pp.Segments[1]
				affectedBlocks[blockID] = true
				matched := false
				for _, node := range def.Nodes {
					if containsID(node.BlockIDs, blockID) || node.ID == targetNodeID {
						affected[node.ID] = true
						matched = true
					}
				}
				if !matched {
					for _, node := range def.Nodes {
						affected[node.ID] = true
					}
				}
			}
		case PathSlots:
			if len(pp.Segments) >= 2 {
				slotID := pp.Segments[1]
				affectedSlots[slotID] = true
				if op.Op == "replace" {
					staleSlots[slotID] = true
				} else if op.Op == "remove" {
					removedSlots[slotID] = true
				}
				matched := false
				for _, node := range def.Nodes {
					if containsID(node.ProducesSlotIDs, slotID) || containsID(node.ConsumesSlotIDs, slotID) {
						affected[node.ID] = true
						matched = true
					}
				}
				if !matched {
					for _, node := range def.Nodes {
						affected[node.ID] = true
					}
				}
			}
		case PathRoot:
			// Goal change potentially affects all nodes.
			for _, node := range def.Nodes {
				affected[node.ID] = true
			}
		}
	}

	// An affected producer makes its declared artifacts stale. Descendants are
	// invalidated for both block and workflow patches.
	for _, node := range def.Nodes {
		if !affected[node.ID] {
			continue
		}
		for _, slotID := range node.ProducesSlotIDs {
			affectedSlots[slotID] = true
			staleSlots[slotID] = true
		}
	}
	for slotID := range removedSlots {
		delete(staleSlots, slotID)
	}
	invalidatedSet := descendantsOf(def.Nodes, affected)
	affectedList := sortedIDSet(affected)
	invalidated := sortedIDSet(invalidatedSet)
	requiresRerun := len(invalidated) > 0 || len(staleSlots) > 0

	return patchImpact{
		affectedNodes:    affectedList,
		affectedBlocks:   sortedIDSet(affectedBlocks),
		affectedSlots:    sortedIDSet(affectedSlots),
		staleSlots:       sortedIDSet(staleSlots),
		invalidatedTasks: invalidated,
		requiresRerun:    requiresRerun,
	}
}

func (s *PatchService) computePatchImpactWithActions(
	def *WorkDefinitionRevision,
	ops []PatchOp,
	scope PatchScope,
	targetNodeID string,
	actions []PatchAction,
) patchImpact {
	impact := s.computePatchImpact(def, ops, scope, targetNodeID)
	if len(actions) == 0 {
		return impact
	}
	rerunRoots := make(map[string]bool)
	for _, action := range actions {
		if action.Action == PatchActionRerun {
			rerunRoots[action.NodeID] = true
		}
		if action.Action == PatchActionReformat {
			impact.affectedSlots = mergeSortedIDs(impact.affectedSlots, []string{action.ArtifactSlotID})
			impact.staleSlots = mergeSortedIDs(impact.staleSlots, []string{action.ArtifactSlotID})
		}
	}
	invalidated := descendantsOf(def.Nodes, rerunRoots)
	impact.invalidatedTasks = sortedIDSet(invalidated)
	impact.requiresRerun = len(impact.invalidatedTasks) > 0
	return impact
}

func normalizePatchActions(
	current *Work,
	def *WorkDefinitionRevision,
	ops []PatchOp,
	scope PatchScope,
	actions []PatchAction,
) ([]PatchAction, error) {
	if len(actions) == 0 {
		return nil, nil // Backward-compatible safe mechanical impact.
	}
	if len(actions) > 64 {
		return nil, errors.New("planner returned more than 64 semantic actions")
	}
	nodes := indexNodes(def.Nodes)
	slots := indexSlots(def.ArtifactSlots)
	nodeActions := make(map[string]PatchActionKind)
	slotActions := make(map[string]PatchActionKind)
	normalized := make([]PatchAction, 0, len(actions))
	for i, action := range actions {
		action.NodeID = strings.TrimSpace(action.NodeID)
		action.ArtifactSlotID = strings.TrimSpace(action.ArtifactSlotID)
		action.Question = strings.TrimSpace(action.Question)
		action.Reason = strings.TrimSpace(action.Reason)
		switch action.Action {
		case PatchActionReuse, PatchActionRerun:
			if action.NodeID == "" || action.ArtifactSlotID != "" {
				return nil, fmt.Errorf("action[%d] %q requires only nodeId", i, action.Action)
			}
			if _, ok := nodes[action.NodeID]; !ok {
				return nil, fmt.Errorf("action[%d] references unknown node %q", i, action.NodeID)
			}
			if previous, exists := nodeActions[action.NodeID]; exists && previous != action.Action {
				return nil, fmt.Errorf("node %q has conflicting actions %q and %q",
					action.NodeID, previous, action.Action)
			}
			nodeActions[action.NodeID] = action.Action
		case PatchActionReformat:
			if scope != PatchWorkflow || action.ArtifactSlotID == "" || action.NodeID != "" {
				return nil, fmt.Errorf("action[%d] reformat requires workflow scope and only artifactSlotId", i)
			}
			if _, ok := slots[action.ArtifactSlotID]; !ok {
				return nil, fmt.Errorf("action[%d] references unknown artifact slot %q", i, action.ArtifactSlotID)
			}
			if previous, exists := slotActions[action.ArtifactSlotID]; exists && previous != action.Action {
				return nil, fmt.Errorf("artifact slot %q has conflicting actions", action.ArtifactSlotID)
			}
			if !patchChangesSlotKind(ops, action.ArtifactSlotID) {
				return nil, fmt.Errorf("reformat action for %q requires an artifact kind change", action.ArtifactSlotID)
			}
			slot, _ := FindArtifactSlotRevision(current, def.Revision, action.ArtifactSlotID)
			if !v2ArtifactDelivered(slot) {
				return nil, fmt.Errorf("reformat action for %q requires a ready source artifact", action.ArtifactSlotID)
			}
			slotActions[action.ArtifactSlotID] = action.Action
		case PatchActionAskUser:
			return nil, fmt.Errorf("ask_user must be handled before patch normalization")
		default:
			return nil, fmt.Errorf("action[%d] has invalid action %q", i, action.Action)
		}
		normalized = append(normalized, action)
	}
	if err := validatePatchActionCoverage(def, ops, nodeActions, slotActions); err != nil {
		return nil, err
	}
	sort.Slice(normalized, func(i, j int) bool {
		left := string(normalized[i].Action) + "\x00" + normalized[i].NodeID + "\x00" + normalized[i].ArtifactSlotID
		right := string(normalized[j].Action) + "\x00" + normalized[j].NodeID + "\x00" + normalized[j].ArtifactSlotID
		return left < right
	})
	return normalized, nil
}

func validatePatchActionCoverage(
	def *WorkDefinitionRevision,
	ops []PatchOp,
	nodeActions map[string]PatchActionKind,
	slotActions map[string]PatchActionKind,
) error {
	requireNode := func(nodeID, path string) error {
		if nodeActions[nodeID] == "" {
			return fmt.Errorf("planner did not decide reuse or rerun for node %q affected by %q", nodeID, path)
		}
		return nil
	}
	for _, op := range ops {
		path, err := CompilePatchPath(op.Path)
		if err != nil {
			continue
		}
		switch path.Kind {
		case PathNodes:
			if err := requireNode(path.Segments[1], op.Path); err != nil {
				return err
			}
		case PathSpecs:
			specID := path.Segments[1]
			for _, node := range def.Nodes {
				if containsID(node.InputSpecIDs, specID) {
					if err := requireNode(node.ID, op.Path); err != nil {
						return err
					}
				}
			}
		case PathSlots:
			slotID := path.Segments[1]
			if slotActions[slotID] == PatchActionReformat {
				continue
			}
			for _, node := range def.Nodes {
				if containsID(node.ProducesSlotIDs, slotID) || containsID(node.ConsumesSlotIDs, slotID) {
					if err := requireNode(node.ID, op.Path); err != nil {
						return err
					}
				}
			}
		case PathRoot:
			for _, node := range def.Nodes {
				if err := requireNode(node.ID, op.Path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func patchChangesSlotKind(ops []PatchOp, slotID string) bool {
	path := "artifactSlots/" + slotID + "/kind"
	for _, op := range ops {
		if op.Op == "replace" && op.Path == path && !jsonValuesEqual(op.OldValue, op.NewValue) {
			return true
		}
	}
	return false
}

func classifyRunImpactWithActions(
	oldRev, newRev *WorkDefinitionRevision,
	actions []PatchAction,
) *RunImpact {
	if len(actions) == 0 {
		return ClassifyRunImpact(oldRev, newRev)
	}
	base := ClassifyRunImpact(oldRev, newRev)
	rerunRoots := make(map[string]bool)
	for _, action := range actions {
		if action.Action == PatchActionRerun {
			rerunRoots[action.NodeID] = true
		}
	}
	invalidated := descendantsOf(newRev.Nodes, rerunRoots)
	oldNodes := indexNodes(oldRev.Nodes)
	newNodes := indexNodes(newRev.Nodes)
	base.KeptNodeIDs = base.KeptNodeIDs[:0]
	base.InvalidatedNodeIDs = base.InvalidatedNodeIDs[:0]
	for nodeID := range oldNodes {
		if _, exists := newNodes[nodeID]; !exists {
			continue
		}
		if invalidated[nodeID] {
			base.InvalidatedNodeIDs = append(base.InvalidatedNodeIDs, nodeID)
		} else {
			base.KeptNodeIDs = append(base.KeptNodeIDs, nodeID)
		}
	}
	sort.Strings(base.KeptNodeIDs)
	sort.Strings(base.InvalidatedNodeIDs)
	base.RequiresRerun = len(base.InvalidatedNodeIDs) > 0
	return base
}

func containsID(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func descendantsOf(nodes []NodeDef, roots map[string]bool) map[string]bool {
	descendants := make(map[string][]string)
	for _, node := range nodes {
		for _, dep := range node.DependsOn {
			descendants[dep] = append(descendants[dep], node.ID)
		}
	}
	out := make(map[string]bool, len(roots))
	queue := make([]string, 0, len(roots))
	for id := range roots {
		out[id] = true
		queue = append(queue, id)
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		for _, descendant := range descendants[id] {
			if !out[descendant] {
				out[descendant] = true
				queue = append(queue, descendant)
			}
		}
	}
	return out
}

func sortedIDSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for id := range values {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func mergeSortedIDs(groups ...[]string) []string {
	set := make(map[string]bool)
	for _, group := range groups {
		for _, id := range group {
			if id != "" {
				set[id] = true
			}
		}
	}
	return sortedIDSet(set)
}

func validatePatchedDefinition(parent, candidate *WorkDefinitionRevision) error {
	if err := ValidateDefinitionRevision(candidate); err != nil {
		return err
	}
	parentSlots := make(map[string]bool, len(parent.ArtifactSlots))
	before := make(map[string]int, len(parent.ArtifactSlots))
	after := make(map[string]int, len(candidate.ArtifactSlots))
	for _, slot := range parent.ArtifactSlots {
		parentSlots[slot.ID] = true
	}
	for _, node := range parent.Nodes {
		for _, slotID := range node.ProducesSlotIDs {
			before[slotID]++
		}
	}
	for _, node := range candidate.Nodes {
		for _, slotID := range node.ProducesSlotIDs {
			after[slotID]++
		}
	}
	for _, slot := range candidate.ArtifactSlots {
		if !parentSlots[slot.ID] || before[slot.ID] != after[slot.ID] {
			if after[slot.ID] != 1 {
				return fmt.Errorf("artifact slot %q must have exactly one producer after this change, got %d",
					slot.ID, after[slot.ID])
			}
		}
	}
	return nil
}

// ── Patch application helpers ─────────────────────────────────────────────

// applyPatchOpsToDefinition applies typed patch operations to a definition
// revision in-place. Only whitelisted paths are allowed.
func applyPatchOpsToDefinition(rev *WorkDefinitionRevision, ops []PatchOp) error {
	nodeIdx := make(map[string]int, len(rev.Nodes))
	for i, n := range rev.Nodes {
		nodeIdx[n.ID] = i
	}
	slotIdx := make(map[string]int, len(rev.ArtifactSlots))
	for i, s := range rev.ArtifactSlots {
		slotIdx[s.ID] = i
	}
	specIdx := make(map[string]int, len(rev.InputSpecs))
	for i, s := range rev.InputSpecs {
		specIdx[s.ID] = i
	}

	for _, op := range ops {
		pp, err := CompilePatchPath(op.Path)
		if err != nil {
			return fmt.Errorf("work: apply op %q %q: %w", op.Op, op.Path, err)
		}
		if err := ValidatePatchOpVerb(op.Op); err != nil {
			return fmt.Errorf("work: apply op %q %q: %w", op.Op, op.Path, err)
		}
		if pp.Kind == PathSlots && len(pp.Segments) == 2 {
			id := pp.Segments[1]
			idx, exists := slotIdx[id]
			switch op.Op {
			case "add":
				if exists {
					return fmt.Errorf("work: apply: slot %q already exists", id)
				}
				if len(op.OldValue) > 0 && !jsonValuesEqual(op.OldValue, json.RawMessage("null")) {
					return fmt.Errorf("work: stale before value for %q", op.Path)
				}
				var slot ArtifactSlotDef
				if err := json.Unmarshal(op.NewValue, &slot); err != nil {
					return fmt.Errorf("work: apply slot %q: %w", id, err)
				}
				if slot.ID != id {
					return fmt.Errorf("work: apply slot %q: newValue id %q does not match path", id, slot.ID)
				}
				rev.ArtifactSlots = append(rev.ArtifactSlots, slot)
				slotIdx[id] = len(rev.ArtifactSlots) - 1
			case "remove":
				if !exists {
					return fmt.Errorf("work: apply: slot %q not found", id)
				}
				before, err := json.Marshal(rev.ArtifactSlots[idx])
				if err != nil {
					return fmt.Errorf("work: apply slot %q: %w", id, err)
				}
				if !jsonValuesEqual(before, op.OldValue) {
					return fmt.Errorf("work: stale before value for %q", op.Path)
				}
				rev.ArtifactSlots = append(rev.ArtifactSlots[:idx], rev.ArtifactSlots[idx+1:]...)
				delete(slotIdx, id)
			default:
				return fmt.Errorf("work: apply op %q %q: object path only allows add or remove", op.Op, op.Path)
			}
			continue
		}
		if op.Op != "replace" {
			return fmt.Errorf("work: apply op %q %q: only artifact slot object paths allow add or remove", op.Op, op.Path)
		}
		before, err := readDefinitionPatchValue(rev, pp)
		if err != nil {
			return err
		}
		if !jsonValuesEqual(before, op.OldValue) {
			return fmt.Errorf("work: stale before value for %q", op.Path)
		}

		switch pp.Kind {
		case PathRoot:
			if pp.Leaf == "goal" {
				if op.Op == "replace" {
					var goal string
					if err := json.Unmarshal(op.NewValue, &goal); err != nil {
						return fmt.Errorf("work: apply goal: %w", err)
					}
					rev.Goal = goal
				}
			}

		case PathNodes:
			id := pp.Segments[1]
			idx, ok := nodeIdx[id]
			if !ok {
				return fmt.Errorf("work: apply: node %q not found", id)
			}
			if err := applyNodeOp(&rev.Nodes[idx], pp.Leaf, op); err != nil {
				return err
			}

		case PathSlots:
			id := pp.Segments[1]
			idx, ok := slotIdx[id]
			if !ok {
				return fmt.Errorf("work: apply: slot %q not found", id)
			}
			if err := applySlotOp(&rev.ArtifactSlots[idx], pp.Leaf, op); err != nil {
				return err
			}

		case PathSpecs:
			id := pp.Segments[1]
			idx, ok := specIdx[id]
			if !ok {
				return fmt.Errorf("work: apply: spec %q not found", id)
			}
			if err := applySpecOp(&rev.InputSpecs[idx], pp.Leaf, op); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyNodeOp(node *NodeDef, field string, op PatchOp) error {
	switch field {
	case "title":
		return applyStringField(&node.Title, op)
	case "description":
		return applyStringField(&node.Description, op)
	case "dependsOn":
		return applyStringSliceField(&node.DependsOn, op)
	case "inputSpecIds":
		return applyStringSliceField(&node.InputSpecIDs, op)
	case "toolHints":
		return applyStringSliceField(&node.ToolHints, op)
	case "blockIds":
		return applyStringSliceField(&node.BlockIDs, op)
	case "producesSlotIds":
		return applyStringSliceField(&node.ProducesSlotIDs, op)
	case "consumesSlotIds":
		return applyStringSliceField(&node.ConsumesSlotIDs, op)
	default:
		return fmt.Errorf("work: unknown node field %q", field)
	}
}

func applySlotOp(slot *ArtifactSlotDef, field string, op PatchOp) error {
	switch field {
	case "title":
		return applyStringField(&slot.Title, op)
	case "kind":
		return applyStringField(&slot.Kind, op)
	case "expectedCount":
		var v int
		if err := json.Unmarshal(op.NewValue, &v); err != nil {
			return fmt.Errorf("work: expectedCount: %w", err)
		}
		slot.ExpectedCount = v
		return nil
	case "required":
		var v bool
		if err := json.Unmarshal(op.NewValue, &v); err != nil {
			return fmt.Errorf("work: required: %w", err)
		}
		slot.Required = v
		return nil
	default:
		return fmt.Errorf("work: unknown slot field %q", field)
	}
}

func applySpecOp(spec *InputSpec, field string, op PatchOp) error {
	switch field {
	case "label":
		return applyStringField(&spec.Label, op)
	case "description":
		return applyStringField(&spec.Description, op)
	case "kind":
		var kind InputKind
		if err := json.Unmarshal(op.NewValue, &kind); err != nil {
			return fmt.Errorf("work: kind: %w", err)
		}
		if !isValidInputKind(kind) {
			return fmt.Errorf("work: invalid input kind %q", kind)
		}
		spec.Kind = kind
		return nil
	case "required":
		var v bool
		if err := json.Unmarshal(op.NewValue, &v); err != nil {
			return fmt.Errorf("work: required: %w", err)
		}
		spec.Required = v
		return nil
	case "valueSchema":
		spec.ValueSchema = append(json.RawMessage(nil), op.NewValue...)
		return nil
	case "defaultValue":
		spec.DefaultValue = append(json.RawMessage(nil), op.NewValue...)
		return nil
	case "pinEligible":
		var v bool
		if err := json.Unmarshal(op.NewValue, &v); err != nil {
			return fmt.Errorf("work: pinEligible: %w", err)
		}
		spec.PinEligible = v
		return nil
	default:
		return fmt.Errorf("work: unknown spec field %q", field)
	}
}

func applyStringField(target *string, op PatchOp) error {
	var v string
	if err := json.Unmarshal(op.NewValue, &v); err != nil {
		return fmt.Errorf("work: string field: %w", err)
	}
	*target = v
	return nil
}

func applyStringSliceField(target *[]string, op PatchOp) error {
	var v []string
	if err := json.Unmarshal(op.NewValue, &v); err != nil {
		return fmt.Errorf("work: string slice field: %w", err)
	}
	*target = append([]string(nil), v...)
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func (s *PatchService) loadDefinition(workID string, revision int64) (*WorkDefinitionRevision, error) {
	if s.defStore == nil {
		return nil, errors.New("no definition store configured")
	}
	return s.defStore.LoadRevision(workID, revision)
}

func (s *PatchService) loadPreview(current *Work, patchID string) (*WorkPatchPreview, error) {
	if current.V2PatchPreviews == nil {
		return nil, fmt.Errorf("preview %q not found", patchID)
	}
	preview, ok := current.V2PatchPreviews[patchID]
	if !ok {
		return nil, fmt.Errorf("preview %q not found", patchID)
	}
	return &preview, nil
}

func resolvePatchRunTask(current *Work, runID, taskID string) (*WorkflowRun, *Task, error) {
	if current == nil {
		return nil, nil, errors.New("patch context Work is missing")
	}
	var run *WorkflowRun
	var task *Task
	for i := range current.Runs {
		if current.Runs[i].ID != runID {
			continue
		}
		runCopy := current.Runs[i]
		run = &runCopy
		for _, stage := range current.Runs[i].Stages {
			for _, candidate := range stage.Tasks {
				if candidate.ID == taskID {
					taskCopy := candidate
					task = &taskCopy
					break
				}
			}
		}
		break
	}
	if run == nil {
		return nil, nil, fmt.Errorf("run %q not found", runID)
	}
	if task == nil {
		if runtime := current.V2TaskRuntimes[taskID]; runtime != nil && runtime.RunID == runID {
			task = v2TaskRuntimeToTask(runtime)
		}
	}
	if task == nil {
		return nil, nil, fmt.Errorf("task %q not found in run %q", taskID, runID)
	}
	return run, task, nil
}

func resolvePatchContext(current *Work, runID, taskID, blockID string) (*WorkflowRun, *Task, *BlockInstance, error) {
	run, task, err := resolvePatchRunTask(current, runID, taskID)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range current.Blocks {
		if current.Blocks[i].ID == blockID && !current.Blocks[i].Tombstone {
			blockCopy := cloneDiscussionBlock(current.Blocks[i])
			return run, task, &blockCopy, nil
		}
	}
	return nil, nil, nil, fmt.Errorf("block %q not found", blockID)
}

func cloneWorkForPatch(current *Work) *Work {
	if current == nil {
		return nil
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return nil
	}
	var clone Work
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil
	}
	return &clone
}

func clonePatchOps(ops []PatchOp) []PatchOp {
	out := make([]PatchOp, len(ops))
	for i := range ops {
		out[i] = ops[i]
		out[i].OldValue = append(json.RawMessage(nil), ops[i].OldValue...)
		out[i].NewValue = append(json.RawMessage(nil), ops[i].NewValue...)
	}
	return out
}

func clonePatchActions(actions []PatchAction) []PatchAction {
	return append([]PatchAction(nil), actions...)
}

func clonePatchStrings(values []string) []string {
	return append([]string{}, values...)
}

func normalizePatchOps(def *WorkDefinitionRevision, block *BlockInstance, scope PatchScope, blockID string, ops []PatchOp) ([]PatchOp, error) {
	if len(ops) == 0 {
		return nil, errors.New("planner returned no operations")
	}
	if len(ops) > 64 {
		return nil, errors.New("planner returned more than 64 operations")
	}
	normalized := make([]PatchOp, 0, len(ops))
	seen := make(map[string]struct{}, len(ops))
	for i, candidate := range ops {
		candidate.Op = strings.TrimSpace(candidate.Op)
		candidate.Path = strings.TrimSpace(candidate.Path)
		if err := ValidatePatchOpVerb(candidate.Op); err != nil {
			return nil, fmt.Errorf("op[%d]: %w", i, err)
		}
		path, err := CompilePatchPath(candidate.Path)
		if err != nil {
			return nil, fmt.Errorf("op[%d]: %w", i, err)
		}
		if _, duplicate := seen[candidate.Path]; duplicate {
			return nil, fmt.Errorf("op[%d]: duplicate path %q", i, candidate.Path)
		}
		seen[candidate.Path] = struct{}{}
		slotObjectOp := path.Kind == PathSlots && len(path.Segments) == 2
		if candidate.Op != "replace" && !slotObjectOp {
			return nil, fmt.Errorf("op[%d]: add/remove is only allowed for artifactSlots/<id>", i)
		}
		if candidate.Op == "replace" && slotObjectOp {
			return nil, fmt.Errorf("op[%d]: artifactSlots/<id> object path only allows add/remove", i)
		}
		if candidate.Op != "remove" && (len(candidate.NewValue) == 0 || !json.Valid(candidate.NewValue)) {
			return nil, fmt.Errorf("op[%d]: newValue must be valid JSON", i)
		}
		if candidate.Op != "remove" {
			if scope == PatchBlock && path.Kind == PathBlocks && path.Leaf == "data" {
				normalized, err := normalizeBlockDataValue(candidate.NewValue)
				if err != nil {
					return nil, fmt.Errorf("op[%d]: %w", i, err)
				}
				candidate.NewValue = normalized
				if block == nil {
					return nil, fmt.Errorf("op[%d]: block context is missing", i)
				}
				if err := validateBlockData(block.Kind, block.SchemaVersion, candidate.NewValue); err != nil {
					return nil, fmt.Errorf("op[%d]: %w", i, err)
				}
			}
			if err := rejectForbiddenJSON(candidate.NewValue); err != nil {
				return nil, fmt.Errorf("op[%d]: %w", i, err)
			}
		}
		if candidate.Op == "remove" && len(candidate.NewValue) > 0 &&
			(!json.Valid(candidate.NewValue) || !jsonValuesEqual(candidate.NewValue, json.RawMessage("null"))) {
			return nil, fmt.Errorf("op[%d]: remove newValue must be omitted or null", i)
		}
		if scope == PatchBlock && candidate.Op != "replace" {
			return nil, fmt.Errorf("op[%d]: block scope only allows replace", i)
		}

		var before json.RawMessage
		switch {
		case candidate.Op == "add":
			if scope != PatchWorkflow || !slotObjectOp {
				return nil, fmt.Errorf("op[%d]: add requires workflow artifactSlots/<id>", i)
			}
			if _, readErr := readDefinitionPatchValue(def, path); readErr == nil {
				return nil, fmt.Errorf("op[%d]: artifact slot %q already exists", i, path.Segments[1])
			}
			var slot ArtifactSlotDef
			if err := json.Unmarshal(candidate.NewValue, &slot); err != nil {
				return nil, fmt.Errorf("op[%d]: artifact slot newValue: %w", i, err)
			}
			if slot.ID != path.Segments[1] {
				return nil, fmt.Errorf("op[%d]: artifact slot id %q does not match path", i, slot.ID)
			}
			before = json.RawMessage("null")
		case candidate.Op == "remove":
			if scope != PatchWorkflow || !slotObjectOp {
				return nil, fmt.Errorf("op[%d]: remove requires workflow artifactSlots/<id>", i)
			}
			before, err = readDefinitionPatchValue(def, path)
		default:
			switch scope {
			case PatchBlock:
				if path.Kind != PathBlocks || len(path.Segments) != 3 || path.Segments[1] != blockID {
					return nil, fmt.Errorf("op[%d]: block scope may only patch block %q", i, blockID)
				}
				before, err = readBlockPatchValue(block, path.Leaf)
			case PatchWorkflow:
				if path.Kind == PathBlocks {
					return nil, fmt.Errorf("op[%d]: workflow scope cannot patch a runtime Block", i)
				}
				before, err = readDefinitionPatchValue(def, path)
			default:
				return nil, fmt.Errorf("op[%d]: invalid scope %q", i, scope)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("op[%d]: %w", i, err)
		}
		if len(candidate.OldValue) > 0 && !jsonValuesEqual(candidate.OldValue, before) {
			return nil, fmt.Errorf("op[%d]: before value does not match authoritative state", i)
		}
		candidate.OldValue = append(json.RawMessage(nil), before...)
		if candidate.Op == "remove" {
			candidate.NewValue = nil
		} else {
			candidate.NewValue = append(json.RawMessage(nil), candidate.NewValue...)
		}
		normalized = append(normalized, candidate)
	}
	return normalized, nil
}

func readBlockPatchValue(block *BlockInstance, field string) (json.RawMessage, error) {
	if block == nil {
		return nil, errors.New("block context is missing")
	}
	switch field {
	case "title":
		return json.Marshal(block.Title)
	case "data":
		return append(json.RawMessage(nil), block.Data...), nil
	default:
		return nil, fmt.Errorf("block field %q is not patchable", field)
	}
}

func readDefinitionPatchValue(def *WorkDefinitionRevision, path PatchPath) (json.RawMessage, error) {
	if def == nil {
		return nil, errors.New("definition context is missing")
	}
	if path.Kind == PathRoot {
		return json.Marshal(def.Goal)
	}
	id := path.Segments[1]
	switch path.Kind {
	case PathNodes:
		for i := range def.Nodes {
			if def.Nodes[i].ID == id {
				switch path.Leaf {
				case "title":
					return json.Marshal(def.Nodes[i].Title)
				case "description":
					return json.Marshal(def.Nodes[i].Description)
				case "dependsOn":
					return json.Marshal(def.Nodes[i].DependsOn)
				case "inputSpecIds":
					return json.Marshal(def.Nodes[i].InputSpecIDs)
				case "toolHints":
					return json.Marshal(def.Nodes[i].ToolHints)
				case "blockIds":
					return json.Marshal(def.Nodes[i].BlockIDs)
				case "producesSlotIds":
					return json.Marshal(def.Nodes[i].ProducesSlotIDs)
				case "consumesSlotIds":
					return json.Marshal(def.Nodes[i].ConsumesSlotIDs)
				}
			}
		}
	case PathSlots:
		for i := range def.ArtifactSlots {
			if def.ArtifactSlots[i].ID == id {
				if len(path.Segments) == 2 {
					return json.Marshal(def.ArtifactSlots[i])
				}
				switch path.Leaf {
				case "title":
					return json.Marshal(def.ArtifactSlots[i].Title)
				case "kind":
					return json.Marshal(def.ArtifactSlots[i].Kind)
				case "expectedCount":
					return json.Marshal(def.ArtifactSlots[i].ExpectedCount)
				case "required":
					return json.Marshal(def.ArtifactSlots[i].Required)
				}
			}
		}
	case PathSpecs:
		for i := range def.InputSpecs {
			if def.InputSpecs[i].ID == id {
				switch path.Leaf {
				case "label":
					return json.Marshal(def.InputSpecs[i].Label)
				case "description":
					return json.Marshal(def.InputSpecs[i].Description)
				case "kind":
					return json.Marshal(def.InputSpecs[i].Kind)
				case "required":
					return json.Marshal(def.InputSpecs[i].Required)
				case "valueSchema":
					return append(json.RawMessage(nil), def.InputSpecs[i].ValueSchema...), nil
				case "defaultValue":
					return append(json.RawMessage(nil), def.InputSpecs[i].DefaultValue...), nil
				case "pinEligible":
					return json.Marshal(def.InputSpecs[i].PinEligible)
				}
			}
		}
	}
	return nil, fmt.Errorf("patch target %q not found", strings.Join(path.Segments, "/"))
}

func rejectForbiddenJSON(raw json.RawMessage) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var visit func(any) error
	visit = func(current any) error {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				for forbidden, reason := range forbiddenRoots {
					if strings.EqualFold(key, forbidden) {
						return fmt.Errorf("newValue key %q is forbidden: %s", key, reason)
					}
				}
				if err := visit(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(value)
}

func jsonValuesEqual(left, right json.RawMessage) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	ca, _ := json.Marshal(a)
	cb, _ := json.Marshal(b)
	return bytes.Equal(ca, cb)
}

// normalizeBlockDataValue unwraps a single layer of JSON string encoding for
// blocks/<id>/data newValue. If newValue is a JSON string whose content is a
// valid JSON object, it returns the re-serialized object. Plain strings,
// arrays, numbers, and nested encodings are rejected so a broken planner
// output is surfaced instead of silently persisted.
func normalizeBlockDataValue(raw json.RawMessage) (json.RawMessage, error) {
	var strValue string
	if err := json.Unmarshal(raw, &strValue); err != nil {
		// Not a JSON string — nothing to unwrap, keep as-is.
		return raw, nil
	}
	var inner any
	if err := json.Unmarshal([]byte(strValue), &inner); err != nil {
		return nil, fmt.Errorf("blocks data newValue is a JSON string but its content is not valid JSON")
	}
	if _, ok := inner.(map[string]any); !ok {
		return nil, fmt.Errorf("blocks data newValue is a JSON string wrapping a non-object — only objects are allowed")
	}
	normalized, err := json.Marshal(inner)
	if err != nil {
		return nil, fmt.Errorf("blocks data normalization failed: %w", err)
	}
	return json.RawMessage(normalized), nil
}

// validateBlockData checks every candidate through the authoritative writable
// Block schema registry. Future and unsupported schemas remain readable, but
// cannot be mutated. Markdown v1 also keeps the frontend's exact-key contract.
func validateBlockData(kind string, schemaVersion int, data json.RawMessage) error {
	if err := builtinBlockSchemas.Validate(kind, schemaVersion, data); err != nil {
		return err
	}
	if kind == "markdown" && schemaVersion == 1 {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(data, &obj); err != nil {
			return fmt.Errorf("markdown block data must be a JSON object")
		}
		if len(obj) != 1 {
			return fmt.Errorf("markdown block data has unexpected fields: only {content:string} is allowed")
		}
	}
	return nil
}

func applyPatchOpsToBlock(block *BlockInstance, ops []PatchOp) error {
	for _, op := range ops {
		path, err := CompilePatchPath(op.Path)
		if err != nil || path.Kind != PathBlocks || path.Segments[1] != block.ID {
			return fmt.Errorf("work: invalid block patch path %q", op.Path)
		}
		before, err := readBlockPatchValue(block, path.Leaf)
		if err != nil {
			return err
		}
		if !jsonValuesEqual(before, op.OldValue) {
			return fmt.Errorf("work: stale before value for %q", op.Path)
		}
		switch path.Leaf {
		case "title":
			if err := json.Unmarshal(op.NewValue, &block.Title); err != nil {
				return fmt.Errorf("work: block title: %w", err)
			}
		case "data":
			if err := validateBlockData(block.Kind, block.SchemaVersion, op.NewValue); err != nil {
				return fmt.Errorf("work: block data: %w", err)
			}
			block.Data = append(json.RawMessage(nil), op.NewValue...)
		}
	}
	return nil
}

func patchTargetRevision(current *Work, preview *WorkPatchPreview, workRevision int64) int64 {
	if preview.Scope == PatchWorkflow {
		return preview.BaseDefinitionRev + 1
	}
	if preview.Scope == PatchBlock {
		return preview.BaseBlockRev + 1
	}
	return workRevision
}
