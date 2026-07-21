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
	return s.upsertBlock(ctx, input, true)
}

func (s *Service) upsertBlock(ctx context.Context, input BlockUpsertInput, requireEditable bool) (*WorkView, error) {
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
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: UpsertBlock: %w", err)
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
	if requireEditable && !canEditBlocks(current.State) {
		return nil, fmt.Errorf("work: UpsertBlock: Work %s is in state %s; blocks are immutable", workID, current.State)
	}

	// Validate against definition spec.
	spec := blockSpecForWork(current, blockID)
	if err := validateBlockSpecMatch(spec, input.Kind, input.SchemaVersion); err != nil {
		return nil, fmt.Errorf("work: UpsertBlock: %w", err)
	}
	if requireEditable && !isUserEditable(spec) {
		return nil, fmt.Errorf("work: UpsertBlock: block %s is not user-editable", blockID)
	}

	var currentBlock *BlockInstance
	for i := range current.Blocks {
		if current.Blocks[i].ID == blockID {
			currentBlock = &current.Blocks[i]
			break
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
		return s.reconcileUpsertCommit(ctx, input, event, incoming, incomingDigest, err)
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

func (s *Service) emitBlockSnapshot(view *WorkView, blockID, requestID string, createdAt time.Time) error {
	if view == nil || view.Work == nil {
		return errors.New("work: cannot emit a nil block snapshot")
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("work: encode block WorkView snapshot: %w", err)
	}
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersion,
		Type:          ViewSnapshot,
		WorkID:        view.Work.ID,
		EventID:       fmt.Sprintf("work-view-%s-%d", view.Work.ID, view.Revision),
		Revision:      view.Revision,
		BaseRevision:  0,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectBlock, ID: blockID, ParentID: view.Work.ID},
		Payload:       payload,
		CreatedAt:     createdAt,
	})
	return nil
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
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: RemoveBlock: %w", err)
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
		return s.reconcileRemoveCommit(ctx, input, event, err)
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
	normalized := normalizeBlockPlacements(input.Placements)
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
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: UpdatePlacements: %w", err)
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

	sorted, err = validateBlockPlacements(current, sorted)
	if err != nil {
		return nil, fmt.Errorf("work: UpdatePlacements: %w", err)
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
		return s.reconcilePlacementCommit(ctx, input, event, sorted, err)
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

// reconcileUpsertCommit performs one cancellable reload after a persisted
// revision-chain race. It never retries a write: semantic convergence either
// proves the incoming block is already applied or returns the latest conflict.
func (s *Service) reconcileUpsertCommit(
	ctx context.Context,
	input BlockUpsertInput,
	event WorkEvent,
	incoming BlockInstance,
	incomingDigest string,
	cause error,
) (*WorkView, error) {
	if !revisionChainConflict(cause) {
		var conflict *ErrWorkEventConflict
		if errors.As(cause, &conflict) {
			return s.latestBlockConflict(input.WorkID, input.BlockID, input.Revision, input.ExpectedRevision, cause, false)
		}
		return nil, fmt.Errorf("work: UpsertBlock: commit event: %w", cause)
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, errors.Join(cause, err)
	}
	current, state, err := s.store.LoadState(input.WorkID, event.RequestID)
	if err != nil {
		return nil, errors.Join(cause, fmt.Errorf("work: reload after block upsert race: %w", err))
	}
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: UpsertBlock: %w", err)
	}
	if state.RequestFound {
		if _, err := s.store.CommitEvent(input.WorkID, event); err != nil {
			var conflict *ErrWorkEventConflict
			if errors.As(err, &conflict) {
				return s.latestBlockConflict(input.WorkID, input.BlockID, input.Revision, input.ExpectedRevision, err, false)
			}
			return nil, fmt.Errorf("work: UpsertBlock: verify concurrent request: %w", err)
		}
		return viewFromState(current, state), nil
	}
	var currentBlock *BlockInstance
	for i := range current.Blocks {
		if current.Blocks[i].ID == input.BlockID {
			currentBlock = &current.Blocks[i]
			break
		}
	}
	result, _, err := mergeBlock(currentBlock, &incoming, incomingDigest)
	if err != nil {
		return nil, err
	}
	switch result {
	case blockMergeSkipOlder, blockMergeIdempotent:
		return viewFromState(current, state), nil
	case blockMergeConflict:
		return viewFromState(current, state), newBlockConflict(
			input.WorkID, input.BlockID, "same revision has different content", input.Revision,
			currentBlock.Revision, input.ExpectedRevision, state.Revision, true, cause,
		)
	default:
		currentRevision := int64(0)
		if currentBlock != nil {
			currentRevision = currentBlock.Revision
		}
		return viewFromState(current, state), newBlockConflict(
			input.WorkID, input.BlockID, "work revision changed during block upsert", input.Revision,
			currentRevision, input.ExpectedRevision, state.Revision, true, cause,
		)
	}
}

func (s *Service) reconcileRemoveCommit(
	ctx context.Context,
	input BlockRemoveInput,
	event WorkEvent,
	cause error,
) (*WorkView, error) {
	if !revisionChainConflict(cause) {
		var conflict *ErrWorkEventConflict
		if errors.As(cause, &conflict) {
			return s.latestBlockConflict(input.WorkID, input.BlockID, input.Revision, input.ExpectedRevision, cause, false)
		}
		return nil, fmt.Errorf("work: RemoveBlock: commit event: %w", cause)
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, errors.Join(cause, err)
	}
	current, state, err := s.store.LoadState(input.WorkID, event.RequestID)
	if err != nil {
		return nil, errors.Join(cause, fmt.Errorf("work: reload after block remove race: %w", err))
	}
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: RemoveBlock: %w", err)
	}
	if state.RequestFound {
		if _, err := s.store.CommitEvent(input.WorkID, event); err != nil {
			var conflict *ErrWorkEventConflict
			if errors.As(err, &conflict) {
				return s.latestBlockConflict(input.WorkID, input.BlockID, input.Revision, input.ExpectedRevision, err, false)
			}
			return nil, fmt.Errorf("work: RemoveBlock: verify concurrent request: %w", err)
		}
		return viewFromState(current, state), nil
	}
	for i := range current.Blocks {
		block := &current.Blocks[i]
		if block.ID != input.BlockID {
			continue
		}
		if input.Revision < block.Revision || (input.Revision == block.Revision && block.Tombstone) {
			return viewFromState(current, state), nil
		}
		if input.Revision == block.Revision {
			return viewFromState(current, state), newBlockConflict(
				input.WorkID, input.BlockID, "same revision is not a tombstone", input.Revision,
				block.Revision, input.ExpectedRevision, state.Revision, true, cause,
			)
		}
		return viewFromState(current, state), newBlockConflict(
			input.WorkID, input.BlockID, "work revision changed during block remove", input.Revision,
			block.Revision, input.ExpectedRevision, state.Revision, true, cause,
		)
	}
	return viewFromState(current, state), newBlockConflict(
		input.WorkID, input.BlockID, "block disappeared during remove", input.Revision,
		0, input.ExpectedRevision, state.Revision, true, cause,
	)
}

func (s *Service) reconcilePlacementCommit(
	ctx context.Context,
	input BlockPlacementInput,
	event WorkEvent,
	placements []BlockPlacement,
	cause error,
) (*WorkView, error) {
	if !revisionChainConflict(cause) {
		var conflict *ErrWorkEventConflict
		if errors.As(cause, &conflict) {
			return s.latestBlockConflict(input.WorkID, "", 0, input.ExpectedRevision, cause, false)
		}
		return nil, fmt.Errorf("work: UpdatePlacements: commit event: %w", cause)
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, errors.Join(cause, err)
	}
	current, state, err := s.store.LoadState(input.WorkID, event.RequestID)
	if err != nil {
		return nil, errors.Join(cause, fmt.Errorf("work: reload after placement race: %w", err))
	}
	if err := requireWritableBlockSchemas(current); err != nil {
		return viewFromState(current, state), fmt.Errorf("work: UpdatePlacements: %w", err)
	}
	if state.RequestFound {
		if _, err := s.store.CommitEvent(input.WorkID, event); err != nil {
			var conflict *ErrWorkEventConflict
			if errors.As(err, &conflict) {
				return s.latestBlockConflict(input.WorkID, "", 0, input.ExpectedRevision, err, false)
			}
			return nil, fmt.Errorf("work: UpdatePlacements: verify concurrent request: %w", err)
		}
		return viewFromState(current, state), nil
	}
	validated, err := validateBlockPlacements(current, placements)
	if err != nil {
		return viewFromState(current, state), newBlockConflict(
			input.WorkID, "", "placement became invalid after concurrent update: "+err.Error(), 0, 0,
			input.ExpectedRevision, state.Revision, true, errors.Join(cause, err),
		)
	}
	if placementSetsEqual(current.Placements, validated) {
		return viewFromState(current, state), nil
	}
	return viewFromState(current, state), newBlockConflict(
		input.WorkID, "", "work revision changed during placement update", 0, 0,
		input.ExpectedRevision, state.Revision, true, cause,
	)
}

// ── RefreshBlock ────────────────────────────────────────────────────────────

const (
	BlockInlineMaxBytes = 64 * 1024
	blockErrorMaxBytes  = 1024
)

// RefreshBlock fetches one source snapshot and persists either its validated
// result or an observable stale/failed state through block.upserted. The
// request, source and retry timestamps are controller-owned context.
func (s *Service) RefreshBlock(ctx context.Context, input RefreshBlockInput, adapter BlockSourceAdapter) (*BlockInstance, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("RefreshBlock", input.RequestID)
	if err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.BlockID = strings.TrimSpace(input.BlockID)
	if input.WorkID == "" || input.BlockID == "" {
		return nil, errors.New("work: RefreshBlock: workID and blockID are required")
	}
	if adapter == nil {
		return nil, errors.New("work: RefreshBlock: BlockSourceAdapter is required")
	}
	if input.CheckedAt.IsZero() {
		input.CheckedAt = time.Now().UTC()
	} else {
		input.CheckedAt = input.CheckedAt.UTC()
	}
	if input.RetryAt != nil {
		retryAt := input.RetryAt.UTC()
		input.RetryAt = &retryAt
	}
	input.RequestID = requestID
	eventRequestID := requestID + "/block-refresh/" + input.BlockID

	current, state, err := s.store.LoadState(input.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if err := requireWritableBlockSchemas(current); err != nil {
		return nil, fmt.Errorf("work: RefreshBlock: %w", err)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("%w: Work %s is %s", ErrBlockRefreshStopped, input.WorkID, current.ArchiveState)
	}
	currentBlock := findBlock(current, input.BlockID)
	if currentBlock == nil || currentBlock.Tombstone {
		return nil, fmt.Errorf("%w: block %s not found or removed in Work %s", ErrBlockRefreshStopped, input.BlockID, input.WorkID)
	}
	if state.RequestFound {
		block := cloneBlock(currentBlock)
		if block.Freshness != nil && block.Freshness.StaleReason != "" &&
			(block.Status == BlockStale || block.Status == BlockFailed) {
			replayErr := fmt.Errorf("%w: %s", ErrBlockRefreshFailed, block.Freshness.StaleReason)
			if block.Status == BlockStale {
				replayErr = errors.Join(replayErr, ErrSourceUnavailable)
			}
			return block, replayErr
		}
		return block, nil
	}

	result, sourceErr := adapter.FetchBlock(ctx, input.WorkID, *cloneBlock(currentBlock))
	if sourceErr != nil && (errors.Is(sourceErr, context.Canceled) || errors.Is(sourceErr, context.DeadlineExceeded)) {
		return nil, sourceErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if sourceErr == nil {
		sourceErr = validateRefreshResult(currentBlock, result)
	}
	next := cloneBlock(currentBlock)
	next.Revision++
	next.UpdatedAt = input.CheckedAt
	if sourceErr == nil {
		next.Status = result.Status
		next.Data = append(json.RawMessage(nil), result.Data...)
		next.Source = normalizeRefreshSource(input.Source)
		next.Freshness = &BlockFreshness{CheckedAt: timePtr(input.CheckedAt)}
		if result.Freshness != nil && result.Freshness.ExpiresAt != nil {
			expiresAt := result.Freshness.ExpiresAt.UTC()
			next.Freshness.ExpiresAt = &expiresAt
		}
		next.Fallback = cloneBlockFallback(result.Fallback)
	} else {
		next.Status = BlockFailed
		if errors.Is(sourceErr, ErrSourceUnavailable) && len(currentBlock.Data) > 0 && currentBlock.Status != BlockLoading && currentBlock.Status != BlockFailed {
			next.Status = BlockStale
		}
		next.Freshness = &BlockFreshness{
			CheckedAt:   timePtr(input.CheckedAt),
			RetryAt:     input.RetryAt,
			StaleReason: clipBlockError(sourceErr.Error()),
		}
	}

	committed, commitErr := s.commitRefreshedBlock(ctx, current, state, next, eventRequestID, requestID)
	if commitErr != nil {
		if sourceErr != nil {
			return committed, errors.Join(fmt.Errorf("work: RefreshBlock: source: %w", sourceErr), commitErr)
		}
		return committed, commitErr
	}
	if sourceErr != nil {
		return committed, fmt.Errorf("work: RefreshBlock: source: %w", sourceErr)
	}
	return committed, nil
}

// CancelBlockRefresh persists removal of the polling intent by clearing
// Freshness. Reopen recovery therefore cannot resurrect an explicitly canceled
// subscription. The current data and status remain available to the user.
func (s *Service) CancelBlockRefresh(ctx context.Context, workID, blockID, requestID string) (*BlockInstance, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("CancelBlockRefresh", requestID)
	if err != nil {
		return nil, err
	}
	workID, blockID = strings.TrimSpace(workID), strings.TrimSpace(blockID)
	if workID == "" || blockID == "" {
		return nil, errors.New("work: CancelBlockRefresh: workID and blockID are required")
	}
	eventRequestID := requestID + "/block-refresh-cancel/" + blockID
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	block := findBlock(current, blockID)
	if block == nil || block.Tombstone {
		return nil, fmt.Errorf("%w: block %s not found or removed", ErrBlockRefreshStopped, blockID)
	}
	if state.RequestFound || block.Freshness == nil {
		return cloneBlock(block), nil
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("%w: Work %s is %s", ErrBlockRefreshStopped, workID, current.ArchiveState)
	}
	next := cloneBlock(block)
	next.Revision++
	next.Freshness = nil
	next.UpdatedAt = time.Now().UTC()
	return s.commitRefreshedBlock(ctx, current, state, next, eventRequestID, requestID)
}

func (s *Service) commitRefreshedBlock(ctx context.Context, current *Work, state WorkEventState, next *BlockInstance, eventRequestID, requestID string) (*BlockInstance, error) {
	if err := validateBlockSpecMatch(blockSpecForWork(current, next.ID), next.Kind, next.SchemaVersion); err != nil {
		return nil, fmt.Errorf("work: RefreshBlock: %w", err)
	}
	payload, err := json.Marshal(next)
	if err != nil {
		return nil, fmt.Errorf("work: RefreshBlock: encode block: %w", err)
	}
	event := newServiceEvent(current.ID, eventRequestID, EventBlockUpserted, payload, next.UpdatedAt)
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := s.store.CommitEvent(current.ID, event); err != nil {
		view, conflictErr := s.latestBlockConflict(current.ID, next.ID, next.Revision, state.Revision, err, true)
		if view != nil && view.Work != nil {
			return cloneBlock(findBlock(view.Work, next.ID)), conflictErr
		}
		return nil, conflictErr
	}
	view, err := s.loadView(current.ID)
	if err != nil {
		return nil, err
	}
	if err := s.emitBlockSnapshot(view, next.ID, requestID, next.UpdatedAt); err != nil {
		return cloneBlock(findBlock(view.Work, next.ID)), committedRecovery("block-refresh-view", current.ID, requestID, view.Revision, err)
	}
	return cloneBlock(findBlock(view.Work, next.ID)), nil
}

// validateRefreshResult rejects over-sized, wrong-schema and UI/execution
// shaped adapter data before it can enter the persisted projection.
func validateRefreshResult(block *BlockInstance, result BlockRefreshResult) error {
	if result.Kind != block.Kind {
		return fmt.Errorf("adapter returned kind %q, block expects %q", result.Kind, block.Kind)
	}
	if result.SchemaVersion != block.SchemaVersion {
		return fmt.Errorf("adapter returned schemaVersion %d, block expects %d", result.SchemaVersion, block.SchemaVersion)
	}
	if !coreBlockKinds[result.Kind] {
		return fmt.Errorf("adapter returned unknown block kind %q", result.Kind)
	}
	if err := CheckSchemaVersion("BlockInstance", result.SchemaVersion); err != nil {
		return err
	}
	if !validBlockStatus(result.Status) {
		return fmt.Errorf("adapter returned invalid status %q", result.Status)
	}
	if len(result.Data) == 0 || len(result.Data) > BlockInlineMaxBytes {
		return fmt.Errorf("adapter data size %d is outside 1..%d bytes", len(result.Data), BlockInlineMaxBytes)
	}
	if len(result.Fallback.Data) > BlockInlineMaxBytes || len(result.Fallback.Summary) > 4096 {
		return errors.New("adapter fallback exceeds the safe inline limit")
	}
	var data map[string]any
	if err := json.Unmarshal(result.Data, &data); err != nil || data == nil {
		return errors.New("adapter data must be a JSON object")
	}
	if err := rejectRefreshInjection(data, "data"); err != nil {
		return err
	}
	if len(result.Fallback.Data) > 0 {
		var fallback any
		if err := json.Unmarshal(result.Fallback.Data, &fallback); err != nil {
			return errors.New("adapter fallback data is invalid JSON")
		}
		if err := rejectRefreshInjection(fallback, "fallback.data"); err != nil {
			return err
		}
	}
	return validateCoreBlockData(result.Kind, data)
}

var forbiddenRefreshKeys = map[string]bool{
	"action": true, "actions": true, "command": true, "component": true,
	"css": true, "exec": true, "executable": true, "html": true,
	"intent": true, "react": true, "renderer": true, "script": true, "style": true,
}

func rejectRefreshInjection(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenRefreshKeys[strings.ToLower(strings.TrimSpace(key))] {
				return fmt.Errorf("adapter data contains forbidden UI/execution field %s.%s", path, key)
			}
			if err := rejectRefreshInjection(child, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for i, child := range typed {
			if err := rejectRefreshInjection(child, fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCoreBlockData(kind string, data map[string]any) error {
	requireString := func(key string) error {
		if _, ok := data[key].(string); !ok {
			return fmt.Errorf("adapter %s data requires string %q", kind, key)
		}
		return nil
	}
	requireArray := func(key string) error {
		if _, ok := data[key].([]any); !ok {
			return fmt.Errorf("adapter %s data requires array %q", kind, key)
		}
		return nil
	}
	switch kind {
	case "markdown", "notice":
		return requireString("content")
	case "code":
		return requireString("content")
	case "graph":
		if data["format"] != "mermaid" {
			return errors.New("adapter graph data requires format=mermaid")
		}
		return requireString("source")
	case "item", "list", "checklist", "file_list", "status", "key_value", "progress", "timeline":
		key := "items"
		if kind == "file_list" {
			key = "files"
		}
		return requireArray(key)
	case "git_status":
		if err := requireString("branch"); err != nil {
			return err
		}
		return requireArray("changes")
	case "table":
		if err := requireArray("columns"); err != nil {
			return err
		}
		return requireArray("rows")
	case "chart":
		typ, ok := data["type"].(string)
		if !ok || (typ != "bar" && typ != "line" && typ != "pie") {
			return errors.New("adapter chart data requires type bar, line, or pie")
		}
		return requireArray("series")
	case "artifact":
		return requireString("artifactRef")
	case "action_entry":
		return requireString("description")
	case "decision", "approval", "input":
		return nil
	default:
		return fmt.Errorf("adapter returned unsupported core block kind %q", kind)
	}
}

func normalizeRefreshSource(source BlockSource) BlockSource {
	source.Provider = strings.TrimSpace(source.Provider)
	source.Ref = strings.TrimSpace(source.Ref)
	source.Mode = strings.TrimSpace(source.Mode)
	if source.Provider == "" {
		source.Provider = "controller"
	}
	if source.Mode == "" {
		source.Mode = "snapshot"
	}
	source.Verified = true
	return source
}

func findBlock(value *Work, blockID string) *BlockInstance {
	if value == nil {
		return nil
	}
	for i := range value.Blocks {
		if value.Blocks[i].ID == blockID {
			return &value.Blocks[i]
		}
	}
	return nil
}

func cloneBlock(value *BlockInstance) *BlockInstance {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Data = append(json.RawMessage(nil), value.Data...)
	clone.Actions = append([]BlockActionSpec(nil), value.Actions...)
	for i := range clone.Actions {
		clone.Actions[i].Payload = append(json.RawMessage(nil), value.Actions[i].Payload...)
	}
	clone.Fallback = cloneBlockFallback(value.Fallback)
	if value.Freshness != nil {
		freshness := *value.Freshness
		if value.Freshness.CheckedAt != nil {
			checkedAt := *value.Freshness.CheckedAt
			freshness.CheckedAt = &checkedAt
		}
		if value.Freshness.ExpiresAt != nil {
			expiresAt := *value.Freshness.ExpiresAt
			freshness.ExpiresAt = &expiresAt
		}
		if value.Freshness.RetryAt != nil {
			retryAt := *value.Freshness.RetryAt
			freshness.RetryAt = &retryAt
		}
		clone.Freshness = &freshness
	}
	return &clone
}

func cloneBlockFallback(value BlockFallback) BlockFallback {
	value.Data = append(json.RawMessage(nil), value.Data...)
	return value
}

func clipBlockError(value string) string {
	if len(value) <= blockErrorMaxBytes {
		return value
	}
	return value[:blockErrorMaxBytes]
}

func timePtr(value time.Time) *time.Time { return &value }
