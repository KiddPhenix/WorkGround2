package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Service owns every Work lifecycle write. The event log is authoritative;
// projections, manifests, indexes and archive files are derived side effects
// repaired by WorkStore on retry or reload.
type Service struct {
	store     WorkStore
	blueprint *BlueprintRegistry
	tools     ToolCatalog
	sink      ViewSink
}

// NewService creates a Work lifecycle service. A nil sink discards view events.
func NewService(store WorkStore, blueprint *BlueprintRegistry, sink ViewSink) *Service {
	return NewServiceWithTools(store, blueprint, nil, sink)
}

// NewServiceWithTools enables creation from Blueprints that declare required
// ToolContracts. The simple constructor remains sufficient for blank and
// tool-free Blueprints.
func NewServiceWithTools(store WorkStore, blueprint *BlueprintRegistry, tools ToolCatalog, sink ViewSink) *Service {
	if IsNilViewSink(sink) {
		sink = ViewSinkDiscard
	}
	return &Service{store: store, blueprint: blueprint, tools: tools, sink: sink}
}

// Create atomically creates a complete Work from an exact Blueprint version.
// The Work ID and persisted create digest make requestID retries stable across
// process restarts; reusing the requestID with different content is rejected.
func (s *Service) Create(ctx context.Context, input CreateWorkInput) (*Work, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("Create", input.RequestID)
	if err != nil {
		return nil, err
	}
	if s.blueprint == nil {
		return nil, errors.New("work: Create: BlueprintRegistry is required")
	}

	bp, err := s.blueprint.LookupRef(input.BlueprintRef)
	if err != nil {
		return nil, fmt.Errorf("work: Create: %w", err)
	}
	var definition *WorkDefinitionSnapshot
	if s.tools == nil {
		definition, err = CreateDefinitionSnapshot(bp, input.Inputs)
	} else {
		definition, err = CreateDefinitionSnapshotWithTools(ctx, bp, input.Inputs, s.tools)
	}
	if err != nil {
		return nil, fmt.Errorf("work: Create: freeze definition: %w", err)
	}
	inputs, err := cloneJSONMap(input.Inputs)
	if err != nil {
		return nil, fmt.Errorf("work: Create: copy inputs: %w", err)
	}

	now := time.Now().UTC()
	workID := workIDForRequest(requestID)
	blocks, placements := buildInitialBlocks(bp.BlockSpecs, now)
	value := &Work{
		SchemaVersion: SchemaVersion,
		ID:            workID,
		Name:          input.Name,
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		BlueprintRef:  input.BlueprintRef,
		Definition:    *definition,
		Inputs:        inputs,
		Blocks:        blocks,
		Placements:    placements,
		Prompt:        bp.PromptTemplate,
		CreatedWith: RuntimeFingerprint{
			WorkSchemaVersion:  SchemaVersion,
			EventSchemaVersion: WorkEventSchemaVersion,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	createdPayload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("work: Create: encode Work: %w", err)
	}
	definitionPayload, err := json.Marshal(definition)
	if err != nil {
		return nil, fmt.Errorf("work: Create: encode definition: %w", err)
	}
	events := []WorkEvent{
		newServiceEvent(workID, requestID+"/created", EventWorkCreated, createdPayload, now),
		newServiceEvent(workID, requestID+"/definition", EventDefinitionFrozen, definitionPayload, now),
	}
	if err := s.store.CreateWorkDir(CreateWorkDirInput{
		RequestID:  requestID,
		Work:       value,
		Definition: definition,
		Events:     events,
	}); err != nil {
		return nil, fmt.Errorf("work: Create: %w", err)
	}

	view, err := s.loadView(workID)
	if err != nil {
		return nil, fmt.Errorf("work: Create: reload committed Work: %w", err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("create-view", workID, requestID, view.Revision, err)
	}
	return view.Work, nil
}

// Get returns one coherent projection/revision pair.
func (s *Service) Get(ctx context.Context, workID string) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workID) == "" {
		return nil, errors.New("work: Get: workID is required")
	}
	return s.loadView(workID)
}

// List returns filtered active/archived summaries from the Store index.
func (s *Service) List(ctx context.Context, filter WorkFilter) (WorkPage, error) {
	if err := checkServiceContext(ctx); err != nil {
		return WorkPage{}, err
	}
	if err := s.requireStore(); err != nil {
		return WorkPage{}, err
	}
	items, err := s.store.List(filter)
	if err != nil {
		return WorkPage{}, err
	}
	if items == nil {
		items = []WorkSummary{}
	}
	return WorkPage{Items: items, Total: len(items)}, nil
}

// UpdateDraft applies editable fields with persisted requestID idempotency and
// expectedRevision optimistic concurrency. Conflicts return the latest view.
func (s *Service) UpdateDraft(ctx context.Context, input UpdateDraftInput) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("UpdateDraft", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: UpdateDraft: workID is required")
	}
	eventRequestID := requestID + "/draft"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: UpdateDraft: Work %s is %s", workID, current.ArchiveState)
	}

	targetState, err := draftTargetState(current.State)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"expectedRevision": input.ExpectedRevision}
	if input.Name != nil {
		payload["name"] = *input.Name
	}
	if input.Prompt != nil {
		payload["prompt"] = *input.Prompt
	}
	if input.Inputs != nil {
		payload["inputs"] = input.Inputs
	}
	if targetState != current.State {
		payload["state"] = targetState
	}
	if len(payload) == 1 {
		return viewFromState(current, state), errors.New("work: UpdateDraft: at least one editable field is required")
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("work: UpdateDraft: encode intent: %w", err)
	}
	event := newServiceEvent(workID, eventRequestID, EventDraftUpdated, payloadBytes, time.Now().UTC())

	if state.RequestFound {
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return s.latestOnConflict(workID, err)
		}
		return s.loadView(workID)
	}
	if input.ExpectedRevision != state.Revision {
		return viewFromState(current, state), revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return s.latestOnConflict(workID, err)
	}
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("draft-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// Archive appends the lifecycle fact first, then materializes an immutable
// WorkRecord from the authoritative archived projection. A failed archive-file
// write is therefore recoverable by retrying the same request after restart.
func (s *Service) Archive(ctx context.Context, workID, requestID string) (*WorkRecord, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("Archive", requestID)
	if err != nil {
		return nil, err
	}
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("work: Archive: workID is required")
	}
	eventRequestID := requestID + "/archive"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.ArchiveState == ArchiveArchived {
		if state.RequestFound && !lifecycleRequestCurrent(state, EventWorkArchived) {
			record, archiveErr := s.store.LoadArchive(workID)
			if archiveErr == nil {
				return record, nil
			}
			if !errors.Is(archiveErr, ErrWorkNotFound) {
				return nil, archiveErr
			}
			return nil, lifecycleRequestConflict("Archive", workID, requestID, state)
		}
		return s.loadOrRepairArchive(workID, current, state.Revision, requestID)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: Archive: Work %s is %s", workID, current.ArchiveState)
	}
	if _, archiveErr := s.store.LoadArchive(workID); archiveErr == nil {
		return nil, fmt.Errorf("%w: %s was restored after its immutable archive was created", ErrWorkArchiveExists, workID)
	} else if !errors.Is(archiveErr, ErrWorkNotFound) {
		return nil, archiveErr
	}

	payload := json.RawMessage(`{"archiveState":"archived"}`)
	event := newServiceEvent(workID, eventRequestID, EventWorkArchived, payload, time.Now().UTC())
	if state.RequestFound {
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return nil, err
		}
	} else {
		event.BaseRevision = state.Revision
		event.Revision = state.Revision + 1
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return nil, err
		}
	}
	archived, archivedState, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if archived.ArchiveState != ArchiveArchived || !lifecycleRequestCurrent(archivedState, EventWorkArchived) {
		return nil, lifecycleRequestConflict("Archive", workID, requestID, archivedState)
	}
	return s.loadOrRepairArchive(workID, archived, archivedState.Revision, requestID)
}

// Restore restores a trashed Work directory when necessary, then appends a
// lifecycle event that returns ArchiveState to active. Work.State is untouched.
func (s *Service) Restore(ctx context.Context, workID, requestID string) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("Restore", requestID)
	if err != nil {
		return nil, err
	}
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("work: Restore: workID is required")
	}
	eventRequestID := requestID + "/restore"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if errors.Is(err, ErrWorkNotFound) {
		current, state, err = s.store.LoadTrashState(workID, eventRequestID)
		if err != nil {
			return nil, fmt.Errorf("work: Restore: inspect Trash: %w", err)
		}
		if state.RequestFound && !lifecycleRequestCurrent(state, EventWorkRestored) {
			return nil, lifecycleRequestConflict("Restore", workID, requestID, state)
		}
		if moveErr := s.store.RestoreFromTrash(workID, requestID+"/move"); moveErr != nil {
			return nil, fmt.Errorf("work: Restore: %w", moveErr)
		}
		current, state, err = s.store.LoadState(workID, eventRequestID)
	}
	if err != nil {
		return nil, err
	}
	if state.RequestFound {
		if current.ArchiveState != ArchiveActive || !lifecycleRequestCurrent(state, EventWorkRestored) {
			return nil, lifecycleRequestConflict("Restore", workID, requestID, state)
		}
		return viewFromState(current, state), nil
	}
	if err := ValidateArchiveTransition(current.ArchiveState, ArchiveActive); err != nil {
		return nil, fmt.Errorf("work: Restore: %w", err)
	}
	event := newServiceEvent(workID, eventRequestID, EventWorkRestored, json.RawMessage(`{"archiveState":"active"}`), time.Now().UTC())
	if !state.RequestFound {
		event.BaseRevision = state.Revision
		event.Revision = state.Revision + 1
	}
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return s.latestOnConflict(workID, err)
	}
	restored, restoredState, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if restored.ArchiveState != ArchiveActive || !lifecycleRequestCurrent(restoredState, EventWorkRestored) {
		return nil, lifecycleRequestConflict("Restore", workID, requestID, restoredState)
	}
	view := viewFromState(restored, restoredState)
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("restore-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// Delete persists work.deleted before moving the directory to trash. If the
// move fails, the deleted projection is observable and the cleanup marker is
// safely resumed by retrying the same requestID.
func (s *Service) Delete(ctx context.Context, workID, requestID string) error {
	if err := checkServiceContext(ctx); err != nil {
		return err
	}
	if err := s.requireStore(); err != nil {
		return err
	}
	requestID, err := requireRequestID("Delete", requestID)
	if err != nil {
		return err
	}
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return errors.New("work: Delete: workID is required")
	}
	eventRequestID := requestID + "/delete"
	current, state, loadErr := s.store.LoadState(workID, eventRequestID)
	inTrash := false
	if errors.Is(loadErr, ErrWorkNotFound) {
		current, state, loadErr = s.store.LoadTrashState(workID, eventRequestID)
		inTrash = true
	}
	if loadErr != nil {
		return loadErr
	}
	if state.RequestFound && !lifecycleRequestCurrent(state, EventWorkDeleted) {
		return nil
	}
	if !state.RequestFound && (inTrash || current.ArchiveState == ArchiveDeleted) {
		return lifecycleRequestConflict("Delete", workID, requestID, state)
	}

	revision := state.RequestRevision
	if inTrash {
		if err := s.store.MoveToTrash(workID, requestID+"/move"); err != nil {
			return fmt.Errorf("work: Delete: %w", err)
		}
		return nil
	}
	if current.ArchiveState != ArchiveDeleted {
		if err := ValidateArchiveTransition(current.ArchiveState, ArchiveDeleted); err != nil {
			return fmt.Errorf("work: Delete: %w", err)
		}
	}
	event := newServiceEvent(workID, eventRequestID, EventWorkDeleted, json.RawMessage(`{"archiveState":"deleted"}`), time.Now().UTC())
	if !state.RequestFound {
		event.BaseRevision = state.Revision
		event.Revision = state.Revision + 1
	}
	revision, err = s.store.CommitEvent(workID, event)
	if err != nil {
		return fmt.Errorf("work: Delete: append event: %w", err)
	}

	latest, latestState, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return fmt.Errorf("work: Delete: verify committed lifecycle: %w", err)
	}
	if !latestState.RequestFound || latestState.RequestRevision != revision {
		return fmt.Errorf("%w: Delete request %q has inconsistent persisted revision", ErrWorkNeedsRepair, requestID)
	}
	if latest.ArchiveState != ArchiveDeleted || !lifecycleRequestCurrent(latestState, EventWorkDeleted) {
		return nil
	}
	if err := s.store.MoveToTrash(workID, requestID+"/move"); err != nil {
		return fmt.Errorf("work: Delete: %w", err)
	}
	s.emitRemoved(workID, revision, requestID)
	return nil
}

func (s *Service) requireStore() error {
	if s == nil || s.store == nil {
		return errors.New("work: Service requires a WorkStore")
	}
	return nil
}

func (s *Service) loadView(workID string) (*WorkView, error) {
	value, state, err := s.store.LoadState(workID, "")
	if err != nil {
		return nil, err
	}
	return viewFromState(value, state), nil
}

func viewFromState(value *Work, state WorkEventState) *WorkView {
	return &WorkView{SchemaVersion: SchemaVersion, Work: value, Revision: state.Revision}
}

func (s *Service) latestOnConflict(workID string, cause error) (*WorkView, error) {
	var conflict *ErrWorkEventConflict
	if !errors.As(cause, &conflict) {
		return nil, cause
	}
	latest, err := s.loadView(workID)
	if err != nil {
		return nil, errors.Join(cause, fmt.Errorf("work: load latest projection after conflict: %w", err))
	}
	return latest, cause
}

func (s *Service) loadOrRepairArchive(workID string, archived *Work, revision int64, requestID string) (*WorkRecord, error) {
	record, err := s.store.LoadArchive(workID)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, ErrWorkNotFound) {
		return nil, err
	}
	record, err = recordFromArchived(archived)
	if err != nil {
		return nil, err
	}
	if err := s.store.WriteArchive(workID, record); err != nil {
		return nil, committedRecovery("archive", workID, requestID, revision, err)
	}
	view := &WorkView{SchemaVersion: SchemaVersion, Work: archived, Revision: revision}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("archive-view", workID, requestID, revision, err)
	}
	return record, nil
}

func recordFromArchived(value *Work) (*WorkRecord, error) {
	if value == nil || value.ArchiveState != ArchiveArchived || value.ArchivedAt == nil {
		return nil, errors.New("work: cannot create WorkRecord from a non-archived projection")
	}
	snapshot, err := cloneWork(value)
	if err != nil {
		return nil, fmt.Errorf("work: clone archive projection: %w", err)
	}
	fallbacks := make([]BlockFallback, len(snapshot.Blocks))
	for i := range snapshot.Blocks {
		fallbacks[i] = snapshot.Blocks[i].Fallback
	}
	return &WorkRecord{
		ArchiveSchemaVersion: SchemaVersion,
		WorkID:               snapshot.ID,
		Snapshot:             *snapshot,
		RendererSetVersion:   snapshot.CreatedWith.RendererSetVersion,
		FallbackBlocks:       fallbacks,
		ArchivedAt:           *snapshot.ArchivedAt,
	}, nil
}

func (s *Service) emitSnapshot(view *WorkView, requestID string) error {
	if view == nil || view.Work == nil {
		return errors.New("work: cannot emit a nil Work snapshot")
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("work: encode WorkView snapshot: %w", err)
	}
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersion,
		Type:          ViewSnapshot,
		WorkID:        view.Work.ID,
		EventID:       fmt.Sprintf("work-view-%s-%d", view.Work.ID, view.Revision),
		Revision:      view.Revision,
		BaseRevision:  0,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectWork, ID: view.Work.ID},
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	})
	return nil
}

func (s *Service) emitRemoved(workID string, revision int64, requestID string) {
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: WorkViewSchemaVersion,
		Type:          ViewRemoved,
		WorkID:        workID,
		EventID:       fmt.Sprintf("work-view-%s-%d", workID, revision),
		Revision:      revision,
		BaseRevision:  revision - 1,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectWork, ID: workID},
		Payload:       json.RawMessage(`{"archiveState":"deleted"}`),
		CreatedAt:     time.Now().UTC(),
	})
}

func newServiceEvent(workID, requestID string, eventType WorkEventType, payload []byte, createdAt time.Time) WorkEvent {
	digest := sha256.Sum256([]byte(requestID))
	return WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            fmt.Sprintf("event-%s-%x", workID, digest[:8]),
		RequestID:     requestID,
		WorkID:        workID,
		Type:          eventType,
		Payload:       append(json.RawMessage(nil), payload...),
		CreatedAt:     createdAt,
	}
}

func workIDForRequest(requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("work-%x", digest[:12])
}

func requireRequestID(operation, requestID string) (string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return "", fmt.Errorf("%w: %s", ErrWorkRequestIDRequired, operation)
	}
	return requestID, nil
}

func checkServiceContext(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}

func revisionConflict(workID string, expected, actual int64) error {
	return &ErrWorkEventConflict{
		WorkID: workID,
		Reason: fmt.Sprintf("expected revision %d, current revision %d", expected, actual),
	}
}

func lifecycleRequestCurrent(state WorkEventState, eventType WorkEventType) bool {
	return state.RequestFound && state.RequestRevision > 0 &&
		state.RequestRevision == state.LifecycleRevision && state.RequestType == eventType
}

func lifecycleRequestConflict(operation, workID, requestID string, state WorkEventState) error {
	return fmt.Errorf("%w: %s request %q for Work %s was superseded at lifecycle revision %d",
		ErrWorkRequestIDConflict, operation, requestID, workID, state.LifecycleRevision)
}

func draftTargetState(state WorkState) (WorkState, error) {
	switch state {
	case WorkDraft:
		return WorkDraft, nil
	case WorkReady, WorkFailed, WorkCancelled:
		if err := ValidateWorkTransition(state, WorkDraft); err != nil {
			return "", err
		}
		return WorkDraft, nil
	default:
		return "", fmt.Errorf("work: UpdateDraft is not allowed while Work.State=%s", state)
	}
}

func buildInitialBlocks(specs []BlockSpec, now time.Time) ([]BlockInstance, []BlockPlacement) {
	blocks := make([]BlockInstance, 0, len(specs))
	placements := make([]BlockPlacement, 0, len(specs))
	for _, spec := range specs {
		blockID := spec.ID
		if blockID == "" {
			blockID = fmt.Sprintf("block-%s-%d", spec.Kind, len(blocks)+1)
		}
		data := append(json.RawMessage(nil), spec.DefaultData...)
		if len(data) == 0 {
			data = json.RawMessage("{}")
		}
		blocks = append(blocks, BlockInstance{
			ID:            blockID,
			Kind:          spec.Kind,
			SchemaVersion: spec.SchemaVersion,
			Revision:      1,
			Title:         spec.Label,
			Status:        BlockEmpty,
			Data:          data,
			Source:        BlockSource{Provider: "user", Mode: "snapshot"},
			CreatedAt:     now,
			UpdatedAt:     now,
		})
		placement := spec.Placement
		placement.BlockID = blockID
		if placement.Slot == "" {
			placement.Slot = "primary"
		}
		placements = append(placements, placement)
	}
	return blocks, placements
}

func cloneJSONMap(value map[string]any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned map[string]any
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return cloned, nil
}

func cloneWork(value *Work) (*Work, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var cloned Work
	if err := json.Unmarshal(data, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}
