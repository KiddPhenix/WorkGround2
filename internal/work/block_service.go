package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ── UpsertBlock ─────────────────────────────────────────────────────────────

// UpsertBlock applies a BlockInstance with revision-based merge semantics.
// Idempotent by requestID; conflicts return the latest WorkView alongside a
// reason that carries the current block revision and Work revision.
func (s *Service) UpsertBlock(ctx context.Context, input BlockUpsertInput) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("UpsertBlock", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: UpsertBlock: workID is required")
	}
	blockID := strings.TrimSpace(input.BlockID)
	if blockID == "" {
		return nil, errors.New("work: UpsertBlock: blockID is required")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		return nil, errors.New("work: UpsertBlock: kind is required")
	}
	if err := CheckSchemaVersion("BlockInstance", input.SchemaVersion); err != nil {
		return nil, fmt.Errorf("work: UpsertBlock: %w", err)
	}
	if !coreBlockKinds[kind] {
		return nil, fmt.Errorf("work: UpsertBlock: unsupported block kind %q", kind)
	}
	if input.Revision <= 0 {
		return nil, errors.New("work: UpsertBlock: revision must be positive")
	}
	if input.Tombstone {
		return nil, errors.New("work: UpsertBlock: tombstones must use RemoveBlock")
	}
	if !validBlockStatus(input.Status) {
		return nil, fmt.Errorf("work: UpsertBlock: invalid status %q", input.Status)
	}
	input.WorkID = workID
	input.BlockID = blockID
	input.Kind = kind
	eventRequestID := requestID + "/block-upsert/" + blockID

	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	incoming := inputToBlock(input)
	incomingDigest, err := blockContentDigest(&incoming)
	if err != nil {
		return nil, fmt.Errorf("work: UpsertBlock: %w", err)
	}
	payload, err := json.Marshal(incoming)
	if err != nil {
		return nil, fmt.Errorf("work: UpsertBlock: encode block: %w", err)
	}
	event := newServiceEvent(workID, eventRequestID, EventBlockUpserted, payload, time.Now().UTC())
	if state.RequestFound {
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return s.latestBlockConflict(workID, blockID, input.Revision, input.ExpectedRevision, err, false)
		}
		return s.loadView(workID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: UpsertBlock: Work %s is %s", workID, current.ArchiveState)
	}
	if !canEditBlocks(current.State) {
		return nil, fmt.Errorf("work: UpsertBlock: Work %s is in state %s; blocks are immutable", workID, current.State)
	}

	// Validate against definition spec.
	spec := blockSpecForWork(current, blockID)
	if err := validateBlockSpecMatch(spec, input.Kind, input.SchemaVersion); err != nil {
		return nil, fmt.Errorf("work: UpsertBlock: %w", err)
	}
	if !isUserEditable(spec) {
		return nil, fmt.Errorf("work: UpsertBlock: block %s is not user-editable", blockID)
	}

	var currentBlock *BlockInstance
	for i := range current.Blocks {
		if current.Blocks[i].ID == blockID {
			currentBlock = &current.Blocks[i]
			break
		}
	}
	if currentBlock != nil {
		if err := CheckSchemaVersion("BlockInstance", currentBlock.SchemaVersion); err != nil {
			return viewFromState(current, state), fmt.Errorf("work: UpsertBlock: %w", err)
		}
	}

	mergeResult, _, mergeErr := mergeBlock(currentBlock, &incoming, incomingDigest)
	if mergeErr != nil {
		return nil, mergeErr
	}

	switch mergeResult {
	case blockMergeSkipOlder:
		// Late delivery is a successful no-op.
		return viewFromState(current, state), nil
	case blockMergeIdempotent:
		// Same revision and digest is a successful no-op, even when delivered
		// under a new transport request ID.
		return viewFromState(current, state), nil
	case blockMergeConflict:
		return viewFromState(current, state), newBlockConflict(
			workID, blockID, "same revision has different content", input.Revision,
			currentBlock.Revision, input.ExpectedRevision, state.Revision, true, nil,
		)
	default:
		// blockMergeNew or blockMergeApplied — proceed.
	}

	if input.ExpectedRevision != state.Revision {
		actual := int64(0)
		if currentBlock != nil {
			actual = currentBlock.Revision
		}
		return viewFromState(current, state), newBlockConflict(
			workID, blockID, "work revision changed", input.Revision, actual,
			input.ExpectedRevision, state.Revision, true,
			revisionConflict(workID, input.ExpectedRevision, state.Revision),
		)
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return s.latestBlockConflict(workID, blockID, input.Revision, input.ExpectedRevision, err, true)
	}
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("block-upsert-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// ── RemoveBlock ─────────────────────────────────────────────────────────────

// RemoveBlock sets tombstone=true on a BlockInstance via a block.removed event.
// Lower-revision remove requests are successful no-ops. The block is never
// physically removed from the projection.
func (s *Service) RemoveBlock(ctx context.Context, input BlockRemoveInput) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("RemoveBlock", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: RemoveBlock: workID is required")
	}
	blockID := strings.TrimSpace(input.BlockID)
	if blockID == "" {
		return nil, errors.New("work: RemoveBlock: blockID is required")
	}
	if input.Revision <= 0 {
		return nil, errors.New("work: RemoveBlock: revision must be positive")
	}
	input.WorkID = workID
	input.BlockID = blockID
	eventRequestID := requestID + "/block-remove/" + blockID

	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(blockRemovedPayload{BlockID: blockID, Revision: input.Revision})
	if err != nil {
		return nil, fmt.Errorf("work: RemoveBlock: encode tombstone: %w", err)
	}
	event := newServiceEvent(workID, eventRequestID, EventBlockRemoved, payload, time.Now().UTC())
	if state.RequestFound {
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return s.latestBlockConflict(workID, blockID, input.Revision, input.ExpectedRevision, err, false)
		}
		return s.loadView(workID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: RemoveBlock: Work %s is %s", workID, current.ArchiveState)
	}
	if !canEditBlocks(current.State) {
		return nil, fmt.Errorf("work: RemoveBlock: Work %s is in state %s; blocks are immutable", workID, current.State)
	}

	var currentBlock *BlockInstance
	for i := range current.Blocks {
		if current.Blocks[i].ID == blockID {
			currentBlock = &current.Blocks[i]
			break
		}
	}
	if currentBlock == nil {
		return nil, fmt.Errorf("work: RemoveBlock: block %s not found in Work %s", blockID, workID)
	}
	if err := CheckSchemaVersion("BlockInstance", currentBlock.SchemaVersion); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: RemoveBlock: %w", err)
	}

	// RemoveBlock is a user edit and only Blueprint-editable blocks allow it.
	spec := blockSpecForWork(current, blockID)
	if !isUserEditable(spec) {
		return nil, fmt.Errorf("work: RemoveBlock: block %s is not user-removable", blockID)
	}

	if input.Revision < currentBlock.Revision {
		return viewFromState(current, state), nil
	}
	if input.Revision == currentBlock.Revision {
		if currentBlock.Tombstone {
			return viewFromState(current, state), nil
		} else {
			return viewFromState(current, state), newBlockConflict(
				workID, blockID, "same revision is not a tombstone", input.Revision,
				currentBlock.Revision, input.ExpectedRevision, state.Revision, true, nil,
			)
		}
	}
	if input.ExpectedRevision != state.Revision {
		return viewFromState(current, state), newBlockConflict(
			workID, blockID, "work revision changed", input.Revision, currentBlock.Revision,
			input.ExpectedRevision, state.Revision, true,
			revisionConflict(workID, input.ExpectedRevision, state.Revision),
		)
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return s.latestBlockConflict(workID, blockID, input.Revision, input.ExpectedRevision, err, true)
	}
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("block-remove-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// ── UpdatePlacements ────────────────────────────────────────────────────────

// UpdatePlacements replaces the Work's placement set after validating that
// every referenced block exists. Placements are sorted by
// (Slot, Order, BlockID) for deterministic output.
func (s *Service) UpdatePlacements(ctx context.Context, input BlockPlacementInput) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("UpdatePlacements", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: UpdatePlacements: workID is required")
	}
	normalized := append([]BlockPlacement(nil), input.Placements...)
	for i := range normalized {
		normalized[i].BlockID = strings.TrimSpace(normalized[i].BlockID)
		normalized[i].Slot = strings.TrimSpace(normalized[i].Slot)
	}
	if err := validatePlacementShape(normalized); err != nil {
		return nil, fmt.Errorf("work: UpdatePlacements: %w", err)
	}
	sorted := sortPlacements(normalized)
	payload, err := json.Marshal(map[string]any{"placements": sorted})
	if err != nil {
		return nil, fmt.Errorf("work: UpdatePlacements: encode placements: %w", err)
	}
	eventRequestID := requestID + "/placements"

	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	event := newServiceEvent(workID, eventRequestID, EventDraftUpdated, payload, time.Now().UTC())
	if state.RequestFound {
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return s.latestBlockConflict(workID, "", 0, input.ExpectedRevision, err, false)
		}
		return s.loadView(workID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: UpdatePlacements: Work %s is %s", workID, current.ArchiveState)
	}
	if !canEditBlocks(current.State) {
		return nil, fmt.Errorf("work: UpdatePlacements: Work %s is in state %s; placements are immutable", workID, current.State)
	}

	// Validate placements: every blockID must reference an existing block.
	blocks := make(map[string]BlockInstance, len(current.Blocks))
	for _, block := range current.Blocks {
		blocks[block.ID] = block
	}
	for _, placement := range sorted {
		block, ok := blocks[placement.BlockID]
		if !ok {
			return nil, fmt.Errorf("work: UpdatePlacements: placement references unknown block %s", placement.BlockID)
		}
		if block.Tombstone {
			return nil, fmt.Errorf("work: UpdatePlacements: placement references tombstoned block %s", placement.BlockID)
		}
	}
	currentPlacements := make(map[string]BlockPlacement, len(current.Placements))
	for _, placement := range current.Placements {
		currentPlacements[placement.BlockID] = placement
	}
	nextPlacements := make(map[string]BlockPlacement, len(sorted))
	for _, placement := range sorted {
		nextPlacements[placement.BlockID] = placement
	}
	for _, block := range current.Blocks {
		if isUserEditable(blockSpecForWork(current, block.ID)) {
			continue
		}
		before, hadBefore := currentPlacements[block.ID]
		after, hasAfter := nextPlacements[block.ID]
		if hadBefore != hasAfter || (hadBefore && !placementEqual(before, after)) {
			return nil, fmt.Errorf("work: UpdatePlacements: block %s is not user-editable", block.ID)
		}
	}
	if placementSetsEqual(current.Placements, sorted) {
		return viewFromState(current, state), nil
	}
	if input.ExpectedRevision != state.Revision {
		return viewFromState(current, state), newBlockConflict(
			workID, "", "work revision changed", 0, 0, input.ExpectedRevision,
			state.Revision, true, revisionConflict(workID, input.ExpectedRevision, state.Revision),
		)
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return s.latestBlockConflict(workID, "", 0, input.ExpectedRevision, err, true)
	}
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("placements-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────────

// canEditBlocks reports whether blocks/placements can be modified in the
// current WorkState. Only draft, ready, failed, cancelled allow edits.
func canEditBlocks(state WorkState) bool {
	switch state {
	case WorkDraft, WorkReady, WorkFailed, WorkCancelled:
		return true
	default:
		return false
	}
}

type blockRemovedPayload struct {
	BlockID  string `json:"blockId"`
	Revision int64  `json:"revision"`
}

func validBlockStatus(status BlockStatus) bool {
	switch status {
	case BlockLoading, BlockReady, BlockEmpty, BlockStale, BlockBlocked, BlockFailed:
		return true
	default:
		return false
	}
}

func newBlockConflict(
	workID, blockID, reason string,
	incomingRevision, currentRevision, expectedWorkRevision, currentWorkRevision int64,
	retryable bool,
	cause error,
) *ErrBlockConflict {
	return &ErrBlockConflict{
		WorkID:               workID,
		BlockID:              blockID,
		Reason:               reason,
		IncomingRevision:     incomingRevision,
		CurrentRevision:      currentRevision,
		ExpectedWorkRevision: expectedWorkRevision,
		CurrentWorkRevision:  currentWorkRevision,
		Retryable:            retryable,
		cause:                cause,
	}
}

func (s *Service) latestBlockConflict(
	workID, blockID string,
	incomingRevision, expectedWorkRevision int64,
	cause error,
	retryable bool,
) (*WorkView, error) {
	latest, loadErr := s.loadView(workID)
	if loadErr != nil {
		return nil, errors.Join(cause, fmt.Errorf("work: load latest block snapshot after conflict: %w", loadErr))
	}
	currentRevision := int64(0)
	for i := range latest.Work.Blocks {
		if latest.Work.Blocks[i].ID == blockID {
			currentRevision = latest.Work.Blocks[i].Revision
			break
		}
	}
	return latest, newBlockConflict(
		workID, blockID, cause.Error(), incomingRevision, currentRevision,
		expectedWorkRevision, latest.Revision, retryable, cause,
	)
}
