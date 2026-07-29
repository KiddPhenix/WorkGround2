package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workground2/internal/nilutil"
)

// Service owns every Work lifecycle write. The event log is authoritative;
// projections, manifests, indexes and archive files are derived side effects
// repaired by WorkStore on retry or reload.
type Service struct {
	store         WorkStore
	blueprint     *BlueprintRegistry
	blockSchemas  *BlockSchemaRegistry
	blockSchemaMu sync.RWMutex
	tools         ToolCatalog
	sink          ViewSink
	actions       *ActionRegistry
	permissions   PermissionChecker
	runner        *WorkRunner
	runMu         sync.Mutex
	runFlights    map[string]*runFlight
	cornerstones  *CornerstoneManager
	defStore      DefinitionRevisionStore
	defStoreMu    sync.Mutex
	defPlanner    DefinitionPlanner
	defPlannerMu  sync.RWMutex
	actionCfgMu   sync.RWMutex
	actionMu      sync.Mutex
	actionRuns    map[string]*actionFlight
	sessionRefs   *SessionRefCoordinator
	refScope      string
	rerunMu       sync.Mutex
	rerunPlans    map[string]preparedRerun
	v2            *V2Coordinator
	v2Transport   atomic.Bool
	previewSvc    *PreviewService
}

type runFlight struct{ done chan struct{} }

type preparedRerun struct {
	record        *WorkRecord
	plan          RerunPlan
	definition    WorkDefinitionSnapshot
	blocks        []BlockInstance
	placements    []BlockPlacement
	migrationPath []int
	upgraded      bool
}

var errRunSuspended = errors.New("work: run was suspended or superseded")

const pauseRecoveryNotice = "pause/checkpoint restores local run and Session context only; it does not roll back network, database, deployment, or other external side effects"

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
	var blobs BlobStore
	if value, ok := store.(BlobStore); ok {
		blobs = value
	}
	cornerstones := NewCornerstoneManager(store, blobs, RealClock{})
	cornerstones.SetResolver(unavailableCornerstoneResolver{})
	service := &Service{
		store: store, blueprint: blueprint, tools: tools, sink: sink,
		blockSchemas: NewBlockSchemaRegistry(),
		cornerstones: cornerstones,
		actionRuns:   make(map[string]*actionFlight), runFlights: make(map[string]*runFlight),
		rerunPlans: make(map[string]preparedRerun),
	}
	if revisions, ok := store.(DefinitionRevisionStore); ok {
		service.defStore = revisions
	}
	service.v2 = newV2Coordinator(store, service.defStore, cornerstones)
	service.v2.SetCommitObserver(service.emitV2RuntimeCommit)
	// Artifact sources are injected separately; preview never treats WorkStore
	// internals as the user workspace.
	service.previewSvc = NewPreviewService(store, "")
	return service
}

// SetBlockSchemaRegistry replaces the kind-specific schema and migration
// capabilities used by Block writes and rerun upgrades. Nil restores the core
// registry so callers cannot accidentally disable validation.
func (s *Service) SetBlockSchemaRegistry(registry *BlockSchemaRegistry) {
	if s == nil {
		return
	}
	if registry == nil {
		registry = NewBlockSchemaRegistry()
	}
	s.blockSchemaMu.Lock()
	s.blockSchemas = registry
	s.blockSchemaMu.Unlock()
}

func (s *Service) blockSchemaRegistry() *BlockSchemaRegistry {
	if s == nil {
		return NewBlockSchemaRegistry()
	}
	s.blockSchemaMu.RLock()
	registry := s.blockSchemas
	s.blockSchemaMu.RUnlock()
	if registry != nil {
		return registry
	}
	s.blockSchemaMu.Lock()
	defer s.blockSchemaMu.Unlock()
	if s.blockSchemas == nil {
		s.blockSchemas = NewBlockSchemaRegistry()
	}
	return s.blockSchemas
}

// SetCornerstoneResolver configures the authoritative live-ref source used by
// every RunWork preflight. Configure it during boot before serving requests.
// Nil remains fail-closed and is persisted as a retryable stale result.
func (s *Service) SetCornerstoneResolver(resolver CornerstoneResolver) {
	if resolver == nil {
		resolver = unavailableCornerstoneResolver{}
	}
	if s.cornerstones != nil {
		s.cornerstones.SetResolver(resolver)
	}
}

// SetArtifactSourceResolver configures the authoritative binary artifact source
// used by V2 preview and conversion. Nil remains fail-closed.
func (s *Service) SetArtifactSourceResolver(resolver ArtifactSourceResolver) {
	if s == nil {
		return
	}
	if s.previewSvc == nil {
		s.previewSvc = NewPreviewService(s.store, "")
	}
	s.previewSvc.SetArtifactSourceResolver(resolver)
}

// SetPreviewApprovalVerifier installs the optional external-conversion approval
// verifier. Nil keeps external conversion disabled.
func (s *Service) SetPreviewApprovalVerifier(verifier ApprovalVerifier) {
	if s == nil {
		return
	}
	if s.previewSvc == nil {
		s.previewSvc = NewPreviewService(s.store, "")
	}
	s.previewSvc.SetApprovalVerifier(verifier)
}

// SetDefinitionRevisionStore configures the V2 definition revision storage.
// Nil clears any previously configured store and falls back to an in-memory map.
func (s *Service) SetDefinitionRevisionStore(store DefinitionRevisionStore) {
	s.defStoreMu.Lock()
	defer s.defStoreMu.Unlock()
	s.defStore = store
	if s.v2 != nil {
		s.v2.SetDefinitionStore(store)
	}
}

func (s *Service) definitionStore() DefinitionRevisionStore {
	s.defStoreMu.Lock()
	defer s.defStoreMu.Unlock()
	if s.defStore == nil {
		s.defStore = newMapDefinitionRevisionStore()
	}
	return s.defStore
}

// SetV2DefinitionPlanner configures the production natural-language
// definition planner. Nil keeps the endpoint fail-closed and retryable.
func (s *Service) SetV2DefinitionPlanner(planner DefinitionPlanner) {
	if s == nil {
		return
	}
	s.defPlannerMu.Lock()
	s.defPlanner = planner
	s.defPlannerMu.Unlock()
}

func (s *Service) definitionPlanner() DefinitionPlanner {
	if s == nil {
		return nil
	}
	s.defPlannerMu.RLock()
	defer s.defPlannerMu.RUnlock()
	return s.defPlanner
}

// SetTaskExecutor replaces the narrow task execution adapter. Nil and typed-nil
// values disable execution. An active run keeps the adapter snapshot it began
// with; later calls observe the replacement.
func (s *Service) SetTaskExecutor(executor TaskExecutor) {
	if nilutil.IsNil(executor) {
		executor = nil
	}
	s.runMu.Lock()
	s.runner = NewWorkRunner(executor)
	s.runMu.Unlock()
	if s.v2 != nil {
		s.v2.SetExecutor(executor)
	}
}

// SetSessionRefStore connects Work lifecycle writes to the process-wide
// Session owner index. Call it during boot before serving requests.
func (s *Service) SetSessionRefStore(store SessionRefStore, scopeID string) error {
	if nilutil.IsNil(store) {
		return errors.New("work: SessionRefStore is required")
	}
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return errors.New("work: SessionRef scope is required")
	}
	s.sessionRefs = NewSessionRefCoordinator(store)
	s.refScope = scopeID
	return nil
}

// SetCornerstoneManager wires the CornerstoneManager into this Service.
// A nil manager disables Cornerstone operations — all cornerstone methods
// return ErrCornerstoneDisabled. Call during boot before serving requests.
func (s *Service) SetCornerstoneManager(cm *CornerstoneManager) {
	s.cornerstones = cm
	if s.v2 != nil {
		s.v2.SetCornerstones(cm)
	}
}

// SetV2PatchPlanner configures the production V2 discussion-patch planner.
func (s *Service) SetV2PatchPlanner(planner PatchPlanner) {
	if s.v2 != nil {
		s.v2.SetPatchPlanner(planner)
	}
}

// SetV2TransportEnabled selects the frontend projection contract. It is wired
// once during boot from collaboration_workbench_v2 and remains independent of
// the persisted Work schema.
func (s *Service) SetV2TransportEnabled(enabled bool) {
	if s != nil {
		s.v2Transport.Store(enabled)
	}
}

// SubmitV2Input commits through InputService and automatically resumes only
// the affected V2 task subgraph from authoritative storage.
func (s *Service) SubmitV2Input(ctx context.Context, input SubmitInputRequest) (*SubmitInputResult, error) {
	if s.v2 == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	result, err := s.v2.SubmitInput(ctx, input)
	if result == nil {
		result = &SubmitInputResult{}
	}
	if result != nil && result.Revision > input.ExpectedRevision {
		err = errors.Join(err, s.emitV2MutationView(input.WorkID, input.ExpectedRevision, input.RequestID))
	}
	_, requestState, stateErr := s.store.LoadState(
		strings.TrimSpace(input.WorkID),
		strings.TrimSpace(input.RequestID)+"/submit",
	)
	if stateErr != nil {
		err = errors.Join(err, fmt.Errorf("work: verify SubmitV2Input receipt: %w", stateErr))
	} else {
		result.Committed = requestState.RequestFound &&
			(requestState.RequestType == EventInputSubmitted || requestState.RequestType == EventInputRejected)
	}
	if result.Committed {
		// SubmitInput may synchronously wake the scheduler, which can append
		// task/runtime events after the input receipt. Callers serialize a
		// multi-field Block with this revision, so return the latest
		// authoritative Work revision instead of the earlier receipt revision.
		// The receipt keeps ResultRevision as the immutable input commit point.
		result.Revision = requestState.Revision
		projection, receiptErr := s.store.LoadProjection(strings.TrimSpace(input.WorkID))
		if receiptErr != nil {
			err = errors.Join(err, committedRecovery(
				"submit-input-receipt", input.WorkID, input.RequestID, result.Revision, receiptErr,
			))
		} else if receipt, ok := projection.V2InputReceipts[strings.TrimSpace(input.RequestID)]; ok {
			result.Receipt = cloneInputIntentReceipt(&receipt)
		} else {
			err = errors.Join(err, committedRecovery(
				"submit-input-receipt", input.WorkID, input.RequestID, result.Revision,
				fmt.Errorf("authoritative typed receipt %q is unavailable", input.RequestID),
			))
		}
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

func (s *Service) emitV2RuntimeCommit(workID string, baseRevision int64, requestID string) error {
	return s.emitV2MutationView(workID, baseRevision, requestID)
}

// ApplyV2WorkPatch commits the preview and automatically resumes its affected
// V2 task subgraph.
func (s *Service) ApplyV2WorkPatch(ctx context.Context, input ApplyWorkPatchInput) (*ApplyWorkPatchResult, error) {
	if s.v2 == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	result, err := s.v2.ApplyPatch(ctx, input)
	if result == nil {
		result = &ApplyWorkPatchResult{}
	}
	if result != nil && result.WorkRevision > input.ExpectedRevision {
		err = errors.Join(err, s.emitV2MutationView(input.WorkID, input.ExpectedRevision, input.RequestID))
	}
	_, requestState, stateErr := s.store.LoadState(
		strings.TrimSpace(input.WorkID),
		strings.TrimSpace(input.RequestID)+"/apply",
	)
	if stateErr != nil {
		err = errors.Join(err, fmt.Errorf("work: verify ApplyV2WorkPatch receipt: %w", stateErr))
	} else {
		result.Committed = requestState.RequestFound && requestState.RequestType == EventPatchApplied
	}
	if result.Committed {
		if result.WorkRevision == 0 {
			result.WorkRevision = requestState.RequestRevision
		}
		projection, receiptErr := s.store.LoadProjection(strings.TrimSpace(input.WorkID))
		if receiptErr != nil {
			err = errors.Join(err, committedRecovery(
				"apply-work-patch-receipt", input.WorkID, input.RequestID, result.WorkRevision, receiptErr,
			))
		} else if receipt, ok := projection.V2PatchReceipts[strings.TrimSpace(input.RequestID)]; ok {
			copy := clonePatchIntentReceipt(receipt)
			result.Receipt = &copy
		} else {
			err = errors.Join(err, committedRecovery(
				"apply-work-patch-receipt", input.WorkID, input.RequestID, result.WorkRevision,
				fmt.Errorf("authoritative typed receipt %q is unavailable", input.RequestID),
			))
		}
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

// PreviewV2WorkPatch creates the immutable preview consumed by
// ApplyV2WorkPatch.
func (s *Service) PreviewV2WorkPatch(ctx context.Context, input PreviewWorkPatchInput) (*PreviewWorkPatchResult, error) {
	if s.v2 == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	_, beforeState, beforeErr := s.store.LoadState(strings.TrimSpace(input.WorkID), "")
	if beforeErr != nil {
		return nil, beforeErr
	}
	result, err := s.v2.PreviewPatch(ctx, input)
	if result == nil {
		if err != nil {
			return nil, err
		}
		result = &PreviewWorkPatchResult{}
	}
	if !result.Duplicate && result.Revision > beforeState.Revision {
		err = errors.Join(err, s.emitV2MutationView(input.WorkID, beforeState.Revision, input.RequestID))
	}
	_, requestState, stateErr := s.store.LoadState(
		strings.TrimSpace(input.WorkID),
		strings.TrimSpace(input.RequestID)+"/preview",
	)
	if stateErr != nil {
		err = errors.Join(err, fmt.Errorf("work: verify PreviewV2WorkPatch receipt: %w", stateErr))
	} else {
		result.Committed = requestState.RequestFound && requestState.RequestType == EventPatchPreviewed
	}
	if result.Committed {
		if result.Revision == 0 {
			result.Revision = requestState.RequestRevision
		}
		projection, receiptErr := s.store.LoadProjection(strings.TrimSpace(input.WorkID))
		if receiptErr != nil {
			err = errors.Join(err, committedRecovery(
				"preview-work-patch-receipt", input.WorkID, input.RequestID, result.Revision, receiptErr,
			))
		} else if receipt, ok := projection.V2PatchReceipts[strings.TrimSpace(input.RequestID)]; ok {
			copy := clonePatchIntentReceipt(receipt)
			result.Receipt = &copy
		} else {
			err = errors.Join(err, committedRecovery(
				"preview-work-patch-receipt", input.WorkID, input.RequestID, result.Revision,
				fmt.Errorf("authoritative typed receipt %q is unavailable", input.RequestID),
			))
		}
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

// ScheduleV2Run starts or resumes a V2 run using only authoritative Work and
// definition state. Callers provide object identity, never input snapshots.
func (s *Service) ScheduleV2Run(
	ctx context.Context,
	workID, runID string,
	changedNodeIDs []string,
) (V2ScheduleResult, error) {
	if s.v2 == nil {
		return V2ScheduleResult{}, errors.New("work: V2 coordinator is not configured")
	}
	return s.v2.ScheduleRun(ctx, workID, runID, changedNodeIDs)
}

// RecoverV2Scheduling resumes durable post-commit wake work after restart.
func (s *Service) RecoverV2Scheduling(ctx context.Context, workID string) error {
	if s.v2 == nil {
		return errors.New("work: V2 coordinator is not configured")
	}
	schedulingErr := s.v2.RecoverScheduling(ctx, workID)
	_, conversionErr := s.RecoverArtifactConversions(ctx, workID)
	return errors.Join(schedulingErr, conversionErr)
}

// RecoverArtifactConversions resumes pending and expired-running conversions
// through the same durable claim path used by live requests.
func (s *Service) RecoverArtifactConversions(ctx context.Context, workID string) (int, error) {
	return s.recoverArtifactConversionsLimit(ctx, workID, maxConversionPump)
}

func (s *Service) recoverArtifactConversionsLimit(ctx context.Context, workID string, limit int) (int, error) {
	if s == nil || s.previewSvc == nil {
		return 0, errors.New("work: artifact conversion recovery is not configured")
	}
	return s.previewSvc.pumpConversions(ctx, workID, limit)
}

// RecoverAllV2Scheduling is the boot-owned recovery entry. Frontends cannot
// invoke it; boot reports individual failures while successful Works continue.
func (s *Service) RecoverAllV2Scheduling(ctx context.Context) V2RecoveryReport {
	if s == nil || s.v2 == nil {
		return V2RecoveryReport{Failures: []V2RecoveryFailure{{Error: "work: V2 coordinator is not configured"}}}
	}
	report := s.v2.RecoverAllScheduling(ctx)
	active := ArchiveActive
	filter := WorkFilter{ArchiveState: &active, Limit: 500}
	remaining := maxConversionPump
	for {
		if err := checkServiceContext(ctx); err != nil {
			report.Failures = append(report.Failures, V2RecoveryFailure{Error: err.Error()})
			return report
		}
		items, err := s.store.List(filter)
		if err != nil {
			report.Failures = append(report.Failures, V2RecoveryFailure{Error: err.Error()})
			return report
		}
		for _, item := range items {
			recovered, err := s.recoverArtifactConversionsLimit(ctx, item.ID, remaining)
			if err != nil {
				report.Failures = append(report.Failures, V2RecoveryFailure{
					WorkID: item.ID,
					Error:  "artifact conversion recovery: " + err.Error(),
				})
			}
			remaining -= recovered
			if remaining <= 0 {
				report.Failures = append(report.Failures, V2RecoveryFailure{
					WorkID: item.ID,
					Error:  "artifact conversion recovery batch limit reached; retry through RecoverArtifactConversions",
				})
				return report
			}
		}
		if len(items) < filter.Limit {
			return report
		}
		filter.Cursor = items[len(items)-1].ID
	}
}

// ── V2 Controller adapter methods ─────────────────────────────────────────

// SubmitWorkInput is the public adapter for SubmitV2Input.
func (s *Service) SubmitWorkInput(ctx context.Context, input SubmitInputRequest) (*SubmitInputResult, error) {
	return s.SubmitV2Input(ctx, input)
}

// PreviewWorkPatch is the public adapter for PreviewV2WorkPatch.
func (s *Service) PreviewWorkPatch(ctx context.Context, input PreviewWorkPatchInput) (*PreviewWorkPatchResult, error) {
	return s.PreviewV2WorkPatch(ctx, input)
}

// ApplyWorkPatch is the public adapter for ApplyV2WorkPatch.
func (s *Service) ApplyWorkPatch(ctx context.Context, input ApplyWorkPatchInput) (*ApplyWorkPatchResult, error) {
	return s.ApplyV2WorkPatch(ctx, input)
}

// SetInputCornerstone pins or unpins a submitted WorkInput as a Cornerstone.
// Pin and input submission are independent operations; pin failure does not
// roll back the input. requestID makes repeated calls idempotent.
func (s *Service) SetInputCornerstone(ctx context.Context, input SetInputCornerstoneRequest) (*CornerstonePinResult, error) {
	if s.v2 == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	result, err := s.v2.SetInputCornerstone(ctx, input)
	if result == nil {
		result = &CornerstonePinResult{}
	}
	eventSuffix := "/unpin-cs"
	if input.Pin {
		eventSuffix = "/pin-cs"
	}
	_, requestState, stateErr := s.store.LoadState(
		strings.TrimSpace(input.WorkID),
		strings.TrimSpace(input.RequestID)+eventSuffix,
	)
	if stateErr != nil {
		err = errors.Join(err, fmt.Errorf("work: verify SetInputCornerstone receipt: %w", stateErr))
	} else {
		result.Committed = requestState.RequestFound && requestState.RequestType == EventInputCornerstoneChanged
	}
	if result.Committed {
		if result.Revision == 0 {
			result.Revision = requestState.RequestRevision
		}
		projection, receiptErr := s.store.LoadProjection(strings.TrimSpace(input.WorkID))
		if receiptErr != nil {
			err = errors.Join(err, committedRecovery(
				"set-input-cornerstone-receipt", input.WorkID, input.RequestID, result.Revision, receiptErr,
			))
		} else if receipt, ok := projection.V2InputReceipts[strings.TrimSpace(input.RequestID)]; ok {
			result.Receipt = cloneInputIntentReceipt(&receipt)
			result.CornerstoneID = receipt.CornerstoneID
			result.Pinned = receipt.Pinned
		} else {
			err = errors.Join(err, committedRecovery(
				"set-input-cornerstone-receipt", input.WorkID, input.RequestID, result.Revision,
				fmt.Errorf("authoritative typed receipt %q is unavailable", input.RequestID),
			))
		}
	}
	if result.Revision > input.ExpectedRevision {
		err = errors.Join(err, s.emitV2MutationView(input.WorkID, input.ExpectedRevision, input.RequestID))
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

// RetryWorkNode retries a failed or invalidated V2 task node.
// expectedRevision guards against lost updates. On success the affected node
// is rescheduled.
func (s *Service) RetryWorkNode(ctx context.Context, input RetryWorkNodeRequest) (*RetryWorkNodeResult, error) {
	if s.v2 == nil || s.store == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	_, before, err := s.store.LoadState(input.WorkID, strings.TrimSpace(input.RequestID)+"/retry-node")
	if err != nil {
		return nil, err
	}
	task, retryErr := s.v2.RetryNode(ctx, input)
	result := &RetryWorkNodeResult{Result: task, Duplicate: before.RequestFound}
	if task != nil {
		result.Committed = true
		err = errors.Join(err, s.emitV2MutationView(input.WorkID, input.ExpectedRevision, input.RequestID))
	}
	_, after, stateErr := s.store.LoadState(input.WorkID, strings.TrimSpace(input.RequestID)+"/retry-node")
	if stateErr == nil {
		result.Revision = after.Revision
		result.Committed = result.Committed || after.RequestFound
	} else {
		err = errors.Join(err, stateErr)
	}
	err = errors.Join(retryErr, err)
	result.Error = TransportErrorFrom(err)
	if result.Error != nil {
		result.Recoverable = result.Error.Recoverable
		result.Committed = result.Committed || result.Error.Committed
	}
	return result, err
}

func (s *Service) emitV2MutationView(workID string, baseRevision int64, requestID string) error {
	if s == nil || !s.v2Transport.Load() {
		return nil
	}
	view, err := s.loadView(workID)
	if err != nil {
		return err
	}
	if err := s.emitV2MutationSnapshot(view, baseRevision, requestID); err != nil {
		return committedRecovery("v2-view-snapshot", workID, requestID, view.Revision, err)
	}
	return nil
}

// v2TaskRuntimeToTask converts a V2TaskRuntime to a plain Task for the
// WorkController interface. It preserves identity and state without the
// V2-specific scheduling fields.
func v2TaskRuntimeToTask(r *V2TaskRuntime) *Task {
	if r == nil {
		return nil
	}
	t := &Task{
		ID:    r.TaskID,
		Name:  r.NodeID,
		State: v2StateToRunState(r.State),
	}
	if len(r.Attempts) > 0 {
		t.StartedAt = &r.Attempts[0].StartedAt
		if last := &r.Attempts[len(r.Attempts)-1]; last.FinishedAt != nil {
			t.FinishedAt = last.FinishedAt
		}
	}
	return t
}

// v2StateToRunState maps TaskStateV2 to the legacy RunState.
func v2StateToRunState(s TaskStateV2) RunState {
	switch s {
	case TaskPending:
		return RunState("pending")
	case TaskReady:
		return RunState("ready")
	case TaskRunning:
		return RunState("running")
	case TaskWaitingInput, TaskWaitingApproval:
		return RunState("waiting")
	case TaskCompleted:
		return RunState("completed")
	case TaskFailedRetryable:
		return RunState("failed")
	case TaskFailedTerminal:
		return RunState("failed")
	case TaskCanceled:
		return RunState("canceled")
	case TaskInvalidated:
		return RunState("canceled")
	default:
		return RunState("unknown")
	}
}

// RebuildSessionRefs repairs this Work store's slice of the shared reverse
// index from authoritative projections, including Work trash.
func (s *Service) RebuildSessionRefs(ctx context.Context) error {
	if s.sessionRefs == nil {
		return nil
	}
	if err := s.requireStore(); err != nil {
		return err
	}
	var summaries []WorkProjectionSummary
	filter := WorkFilter{Limit: 500}
	for {
		if err := checkServiceContext(ctx); err != nil {
			return err
		}
		items, err := s.store.List(filter)
		if err != nil {
			return fmt.Errorf("work: rebuild Session refs: list projections: %w", err)
		}
		for _, item := range items {
			var value *Work
			if item.ArchiveState == ArchiveDeleted {
				value, _, err = s.store.LoadTrashState(item.ID, "")
			} else {
				value, _, err = s.store.LoadState(item.ID, "")
			}
			if err != nil {
				return fmt.Errorf("work: rebuild Session refs for Work %s: %w", item.ID, err)
			}
			summaries = append(summaries, WorkSessionRefSummary(s.refScope, value))
		}
		if len(items) < filter.Limit {
			break
		}
		filter.Cursor = items[len(items)-1].ID
	}
	if trash, ok := s.store.(WorkTrashLister); ok {
		items, err := trash.ListTrash()
		if err != nil {
			return fmt.Errorf("work: rebuild Session refs: list trash: %w", err)
		}
		for _, item := range items {
			if err := checkServiceContext(ctx); err != nil {
				return err
			}
			value, _, err := s.store.LoadTrashState(item.ID, "")
			if err != nil {
				return fmt.Errorf("work: rebuild Session refs for trashed Work %s: %w", item.ID, err)
			}
			summaries = append(summaries, WorkSessionRefSummary(s.refScope, value))
		}
	}
	if err := s.sessionRefs.ReconcileScope(s.refScope, summaries); err != nil {
		return fmt.Errorf("work: rebuild Session refs: %w", err)
	}
	return nil
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
	prompt := strings.TrimSpace(input.Prompt)
	if prompt == "" {
		prompt = bp.PromptTemplate
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = workNameFromPrompt(prompt, bp.Name)
	}
	value := &Work{
		SchemaVersion: SchemaVersion,
		ID:            workID,
		Name:          name,
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		BlueprintRef:  input.BlueprintRef,
		Definition:    *definition,
		Inputs:        inputs,
		Blocks:        blocks,
		Placements:    placements,
		Prompt:        prompt,
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
	if err := s.syncSessionRefs(view.Work, requestID+"/session-refs"); err != nil {
		return nil, committedRecovery("create-session-refs", workID, requestID, view.Revision, err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("create-view", workID, requestID, view.Revision, err)
	}
	return view.Work, nil
}

// ListBlueprints returns immutable registry copies for frontend selection.
func (s *Service) ListBlueprints(ctx context.Context) ([]WorkBlueprint, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if s == nil || s.blueprint == nil {
		return nil, errors.New("work: ListBlueprints: BlueprintRegistry is required")
	}
	items := s.blueprint.List()
	result := make([]WorkBlueprint, 0, len(items))
	for _, item := range items {
		if item != nil {
			result = append(result, *item)
		}
	}
	return result, nil
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
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	if err := s.syncSessionRefs(view.Work, "get:"+workID); err != nil {
		return nil, fmt.Errorf("work: Get: reconcile Session refs: %w", err)
	}
	return view, nil
}

// BuildCornerstoneContext loads a fresh Work projection and validates snapshot
// blobs through the Service-owned store before returning transient context.
func (s *Service) BuildCornerstoneContext(ctx context.Context, workID string, config CornerstoneContextConfig) (CornerstoneContextBlock, error) {
	if err := checkServiceContext(ctx); err != nil {
		return CornerstoneContextBlock{}, err
	}
	if err := s.requireStore(); err != nil {
		return CornerstoneContextBlock{}, err
	}
	view, err := s.loadView(workID)
	if err != nil {
		return CornerstoneContextBlock{}, err
	}
	if config.BlobStore == nil {
		if blobs, ok := s.store.(BlobStore); ok {
			config.BlobStore = blobs
		}
	}
	return BuildCornerstoneContext(view.Work.Cornerstones, config)
}

// SessionCornerstoneCount derives cleanup notices from authoritative Work
// projections. A Session may belong to several Works; every matching Work is
// included and duplicate attempts/cornerstones are counted once.
func (s *Service) SessionCornerstoneCount(ctx context.Context, sessionPath string) (count int, associated bool, err error) {
	if err := checkServiceContext(ctx); err != nil {
		return 0, false, err
	}
	if err := s.requireStore(); err != nil {
		return 0, false, err
	}
	target := normSessionPath(sessionPath)
	if target == "" {
		return 0, false, nil
	}
	seenWorks := make(map[string]struct{})
	seenCornerstones := make(map[string]struct{})
	visit := func(value *Work) {
		if value == nil || !workReferencesSession(value, target) {
			return
		}
		if _, ok := seenWorks[value.ID]; ok {
			return
		}
		seenWorks[value.ID] = struct{}{}
		associated = true
		for _, cs := range activeCornerstonesDeduped(value.Cornerstones) {
			key := value.ID + "\x00" + cs.ID
			if _, ok := seenCornerstones[key]; ok {
				continue
			}
			seenCornerstones[key] = struct{}{}
			count++
		}
	}

	filter := WorkFilter{Limit: 500}
	for {
		if err := checkServiceContext(ctx); err != nil {
			return 0, false, err
		}
		items, listErr := s.store.List(filter)
		if listErr != nil {
			return 0, false, fmt.Errorf("work: count Session cornerstones: list projections: %w", listErr)
		}
		for _, item := range items {
			value, _, loadErr := s.store.LoadState(item.ID, "")
			if loadErr != nil {
				return 0, false, fmt.Errorf("work: count Session cornerstones for Work %s: %w", item.ID, loadErr)
			}
			visit(value)
		}
		if len(items) < filter.Limit {
			break
		}
		filter.Cursor = items[len(items)-1].ID
	}
	if trash, ok := s.store.(WorkTrashLister); ok {
		items, listErr := trash.ListTrash()
		if listErr != nil {
			return 0, false, fmt.Errorf("work: count Session cornerstones: list trash: %w", listErr)
		}
		for _, item := range items {
			if err := checkServiceContext(ctx); err != nil {
				return 0, false, err
			}
			value, _, loadErr := s.store.LoadTrashState(item.ID, "")
			if loadErr != nil {
				return 0, false, fmt.Errorf("work: count Session cornerstones for trashed Work %s: %w", item.ID, loadErr)
			}
			visit(value)
		}
	}
	return count, associated, nil
}

func workReferencesSession(value *Work, target string) bool {
	for _, cs := range activeCornerstonesDeduped(value.Cornerstones) {
		if cs.Ref.Kind == "session_turn" && normSessionPath(cs.Ref.SessionID) == target {
			return true
		}
	}
	for _, path := range WorkSessionRefSummary("", value).SessionPaths {
		if normSessionPath(path) == target {
			return true
		}
	}
	return false
}

// List returns filtered active/archived summaries from the Store index.
func (s *Service) List(ctx context.Context, filter WorkFilter) (WorkPage, error) {
	if err := checkServiceContext(ctx); err != nil {
		return WorkPage{}, err
	}
	if err := s.requireStore(); err != nil {
		return WorkPage{}, err
	}
	var items []WorkSummary
	var err error
	if filter.ArchiveState != nil && *filter.ArchiveState == ArchiveDeleted {
		trash, ok := s.store.(WorkTrashLister)
		if !ok {
			return WorkPage{}, errors.New("work: List: Store does not support Trash listing")
		}
		items, err = trash.ListTrash()
		if err == nil {
			filtered := items[:0]
			for i := range items {
				if filter.Matches(&items[i]) {
					filtered = append(filtered, items[i])
				}
			}
			items = filtered
			if filter.Cursor != "" {
				start := -1
				for i := range items {
					if items[i].ID == filter.Cursor {
						start = i + 1
						break
					}
				}
				if start < 0 {
					items = nil
				} else {
					items = items[start:]
				}
			}
			limit := filter.Limit
			if limit <= 0 || limit > 500 {
				limit = 100
			}
			if len(items) > limit {
				items = items[:limit]
			}
		}
	} else {
		items, err = s.store.List(filter)
	}
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
	if err := requireWritableBlockSchemas(current, s.blockSchemaRegistry()); err != nil {
		return nil, fmt.Errorf("work: UpdateDraft: %w", err)
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: UpdateDraft: Work %s is %s", workID, current.ArchiveState)
	}

	targetState, err := updateDraftTargetState(current)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{"expectedRevision": input.ExpectedRevision}
	if input.Prompt != nil {
		payload["prompt"] = *input.Prompt
		// Keep following the prompt while the title is still automatic. Once
		// the user has renamed the Work, later prompt edits must not silently
		// replace that explicit choice.
		if input.Name != nil {
			payload["name"] = *input.Name
		} else if workNameIsAutomatic(current) {
			payload["name"] = workNameFromPrompt(*input.Prompt, current.Name)
		}
	} else if input.Name != nil {
		payload["name"] = *input.Name
	}
	if input.Inputs != nil {
		payload["inputs"] = input.Inputs
	}
	if strings.TrimSpace(input.Locale) != "" {
		locale, normalizeErr := NormalizeLocale(input.Locale)
		if normalizeErr != nil {
			return viewFromState(current, state), fmt.Errorf("work: UpdateDraft: %w", normalizeErr)
		}
		payload["locale"] = locale
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

// RunWork starts or resumes execution of a Work's frozen WorkflowDef. It is
// idempotent by requestID: repeated calls with the same requestID return the
// already-created run. Already-terminal runs are a safe no-op.
//
// The Work must have a frozen definition. Empty stages, unknown gates, and
// definition drift are hard failures.
func (s *Service) RunWork(ctx context.Context, workID, requestID string) (*WorkflowRun, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("RunWork", requestID)
	if err != nil {
		return nil, err
	}
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("work: RunWork: workID is required")
	}
	for {
		flight, owner := s.beginRunFlight(workID)
		if owner {
			defer s.finishRunFlight(workID, flight)
			return s.runWork(ctx, workID, requestID)
		}
		if err := waitRunFlight(ctx, flight); err != nil {
			return nil, err
		}
	}
}

func (s *Service) runWork(ctx context.Context, workID, requestID string) (*WorkflowRun, error) {
	runner := s.taskRunner()
	if runner == nil || runner.executor == nil {
		return nil, errors.New("work: RunWork: TaskExecutor is not configured; call SetTaskExecutor first")
	}
	eventRequestID := requestID + "/run"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: RunWork: Work %s is %s", workID, current.ArchiveState)
	}
	if strings.TrimSpace(current.Prompt) == "" {
		return nil, errors.New("work: RunWork: prompt is required; edit and save the Work prompt before running")
	}

	if err := validateDefForRun(current.Definition.Workflow); err != nil {
		return nil, fmt.Errorf("work: RunWork: %w", err)
	}
	if current.Definition.Digest == "" {
		return nil, fmt.Errorf("work: RunWork: Work %s has no frozen definition digest", workID)
	}
	computedDigest, err := ComputeDigest(&current.Definition)
	if err != nil {
		return nil, fmt.Errorf("work: RunWork: compute frozen definition digest: %w", err)
	}
	if computedDigest != current.Definition.Digest {
		return nil, fmt.Errorf("work: RunWork: definition drift: stored %s, computed %s", current.Definition.Digest, computedDigest)
	}

	runID := workflowRunID(workID, requestID)
	if state.RequestFound {
		if state.RequestType != EventRunStarted {
			return nil, fmt.Errorf("work: RunWork: request %q was already used by %s", requestID, state.RequestType)
		}
		run := findWorkflowRun(current, runID)
		if run == nil {
			return nil, fmt.Errorf("work: RunWork: committed request %q has no run %q", requestID, runID)
		}
		if run.RequestID != "" && run.RequestID != requestID {
			return nil, fmt.Errorf("work: RunWork: run %q belongs to request %q", run.ID, run.RequestID)
		}
	} else {
		for i := range current.Runs {
			if !IsTerminalRunState(current.Runs[i].State) {
				return nil, fmt.Errorf("work: RunWork: Work %s already has active run %s in state %s", workID, current.Runs[i].ID, current.Runs[i].State)
			}
		}
		now := time.Now().UTC()
		run := newPendingRun(current, requestID)
		payload, marshalErr := json.Marshal(runEventPayload{Run: run, WorkState: WorkRunning})
		if marshalErr != nil {
			return nil, fmt.Errorf("work: RunWork: encode initial run: %w", marshalErr)
		}
		event := newServiceEvent(workID, eventRequestID, EventRunStarted, payload, now)
		event.BaseRevision = state.Revision
		event.Revision = state.Revision + 1
		if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
			return nil, fmt.Errorf("work: RunWork: commit run reservation: %w", commitErr)
		}
		current, _, err = s.store.LoadState(workID, "")
		if err != nil {
			return nil, committedRecovery("run-load", workID, requestID, event.Revision, err)
		}
	}

	run := findWorkflowRun(current, runID)
	if run == nil {
		return nil, fmt.Errorf("work: RunWork: run %q disappeared from projection", runID)
	}
	emit := s.runEmitter(workID, runID, "RunWork")

	// Pre-flight cornerstone check: required cornerstones that are missing,
	// denied, stale, or invalid must block before any Task Session is created.
	current, err = s.resolveRunCornerstones(ctx, current, requestID)
	if err != nil {
		return nil, fmt.Errorf("work: RunWork: resolve cornerstones: %w", err)
	}
	if blocked := s.checkRunCornerstones(current); blocked != nil {
		if emitErr := s.emitCornerstoneBlockedRun(workID, runID, requestID, blocked.Assessment, cornerstoneBlockInitialRun); emitErr != nil {
			return nil, errors.Join(blocked, fmt.Errorf("work: RunWork: commit cornerstone blocked state: %w", emitErr))
		}
		view, loadErr := s.loadView(workID)
		if loadErr != nil {
			return nil, errors.Join(blocked, committedRecovery("run-cornerstone-view", workID, requestID, 0, loadErr))
		}
		if emitErr := s.emitSnapshot(view, requestID); emitErr != nil {
			return nil, errors.Join(blocked, committedRecovery("run-cornerstone-view", workID, requestID, view.Revision, emitErr))
		}
		return nil, blocked
	}

	_, runErr := runner.Run(ctx, current, run, emit)

	view, err := s.loadView(workID)
	if err != nil {
		return nil, committedRecovery("run-view", workID, requestID, 0, err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("run-view", workID, requestID, view.Revision, err)
	}
	persisted := findWorkflowRun(view.Work, runID)
	if persisted == nil {
		return nil, committedRecovery("run-view", workID, requestID, view.Revision, fmt.Errorf("run %q missing", runID))
	}
	result := *persisted
	if runErr != nil && persisted.State != RunWaiting && !IsTerminalRunState(persisted.State) {
		return &result, fmt.Errorf("work: RunWork: %w", runErr)
	}
	return &result, nil
}

// RetryTask adds a new Attempt to a failed or needs_confirmation Task and
// executes it through the TaskExecutor. Same requestID returns the same
// Attempt idempotently; different requestID creates a new Attempt.
func (s *Service) RetryTask(ctx context.Context, input RetryTaskInput) (*Attempt, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("RetryTask", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: RetryTask: workID is required")
	}
	runID := strings.TrimSpace(input.RunID)
	stageID := strings.TrimSpace(input.StageID)
	taskID := strings.TrimSpace(input.TaskID)
	if runID == "" || stageID == "" || taskID == "" {
		return nil, errors.New("work: RetryTask: runID, stageID, and taskID are required")
	}

	for {
		flight, owner := s.beginRunFlight(workID)
		if owner {
			defer s.finishRunFlight(workID, flight)
			return s.retryTask(ctx, workID, runID, stageID, taskID, requestID)
		}
		if err := waitRunFlight(ctx, flight); err != nil {
			return nil, err
		}
	}
}

func (s *Service) retryTask(ctx context.Context, workID, runID, stageID, taskID, requestID string) (*Attempt, error) {
	eventRequestID := requestID + "/retry"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: RetryTask: Work %s is %s", workID, current.ArchiveState)
	}

	run := findWorkflowRun(current, runID)
	if run == nil {
		return nil, fmt.Errorf("work: RetryTask: run %q not found", runID)
	}
	var stage *Stage
	for i := range run.Stages {
		if run.Stages[i].ID == stageID || (run.Stages[i].ID == "" && run.Stages[i].Name == stageID) {
			stage = &run.Stages[i]
			break
		}
	}
	if stage == nil {
		return nil, fmt.Errorf("work: RetryTask: stage %q not found in run %q", stageID, runID)
	}

	var task *Task
	for i := range stage.Tasks {
		if stage.Tasks[i].ID == taskID || (stage.Tasks[i].ID == "" && stage.Tasks[i].Name == taskID) {
			task = &stage.Tasks[i]
			break
		}
	}
	if task == nil {
		return nil, fmt.Errorf("work: RetryTask: task %q not found in stage %q", taskID, stageID)
	}

	runner := s.taskRunner()
	if runner == nil || runner.executor == nil {
		return nil, errors.New("work: RetryTask: TaskExecutor is not configured")
	}

	// Idempotent: same requestID returns the same attempt.
	if state.RequestFound {
		for i := range task.Attempts {
			if task.Attempts[i].RequestID == requestID+"/execute" {
				return &task.Attempts[i], nil
			}
		}
		return nil, fmt.Errorf("work: RetryTask: committed request %q has no traceable attempt", requestID)
	}
	if run.State == RunCancelled || run.State == RunCompleted {
		return nil, fmt.Errorf("work: RetryTask: run %q is %s; cancelled and completed runs cannot be retried", runID, run.State)
	}
	if task.State != RunFailed && task.State != RunNeedsConfirmation {
		return nil, fmt.Errorf("work: RetryTask: task %q is %s; only failed or needs_confirmation tasks can be retried", taskID, task.State)
	}

	attemptIndex := len(task.Attempts)
	attemptID := runChildID(task.ID, "attempt", requestID)
	executeRequestID := requestID + "/execute"
	attempt := Attempt{
		ID:              attemptID,
		RequestID:       executeRequestID,
		Index:           attemptIndex,
		State:           RunRunning,
		StartedAt:       time.Now().UTC(),
		SideEffectClass: workSideEffectClass(current.Definition.ToolContracts),
	}

	// Reopen the failed/uncertain path and reserve the new Attempt in one event,
	// before the executor can produce another side effect.
	nextRun := *run
	nextRun.Stages = append([]Stage(nil), run.Stages...)
	nextStage := findRunStage(&nextRun, stage.ID)
	nextStage.Tasks = append([]Task(nil), stage.Tasks...)
	nextTask := findStageTask(nextStage, task.ID)
	nextTask.Attempts = append(append([]Attempt(nil), task.Attempts...), attempt)
	nextTask.State, nextTask.FinishedAt = RunRunning, nil
	nextStage.State, nextStage.FinishedAt = RunRunning, nil
	nextRun.State, nextRun.FinishedAt = RunRunning, nil
	payload, marshalErr := json.Marshal(runEventPayload{Run: nextRun, WorkState: WorkRunning})
	if marshalErr != nil {
		return nil, fmt.Errorf("work: RetryTask: encode attempt: %w", marshalErr)
	}
	event := newServiceEvent(workID, eventRequestID, EventRunChanged, payload, time.Now().UTC())
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
		return nil, fmt.Errorf("work: RetryTask: commit attempt reservation: %w", commitErr)
	}

	result, execErr := safeExecuteTask(runner.executor, ctx, TaskExecuteInput{
		WorkID:           current.ID,
		RunID:            run.ID,
		StageID:          stage.ID,
		TaskID:           task.ID,
		AttemptID:        attemptID,
		AttemptIndex:     attemptIndex,
		RequestID:        executeRequestID,
		DefinitionDigest: run.DefinitionDigest,
		SideEffectClass:  attempt.SideEffectClass,
		Prompt:           current.Prompt,
	})

	finished := attempt
	finished.FinishedAt = timePtr(time.Now().UTC())
	switch {
	case execErr != nil:
		finished.State = RunFailed
		finished.Error = execErr.Error()
	case result == nil:
		finished.State = RunCompleted
	default:
		finished.SessionRef = result.SessionRef
		finished.Error = result.Error
		finished.Receipt = result.Receipt
		if strings.TrimSpace(result.SideEffectClass) != "" {
			finished.SideEffectClass = result.SideEffectClass
		}
		if result.FinishedAt != nil {
			finished.FinishedAt = result.FinishedAt
		}
		switch result.State {
		case "", RunCompleted:
			finished.State = RunCompleted
		case RunFailed, RunCancelled:
			finished.State = result.State
		case RunNeedsConfirmation:
			finished.State = RunNeedsConfirmation
		default:
			finished.State = RunFailed
			finished.Error = fmt.Sprintf("work: executor returned non-terminal Attempt state %q", result.State)
		}
	}
	applyReceiptGuard(&finished, TaskExecuteInput{RequestID: executeRequestID, SideEffectClass: attempt.SideEffectClass})

	resultPayload, marshalErr := json.Marshal(attemptEventPayload{RunID: run.ID, StageID: stage.ID, TaskID: task.ID, Attempt: finished})
	if marshalErr != nil {
		return nil, fmt.Errorf("work: RetryTask: encode attempt result: %w", marshalErr)
	}
	resultEmitter := s.runEmitter(workID, runID, "RetryTask")
	if _, commitErr := resultEmitter(WorkEvent{
		RequestID: requestID + "/result", Type: EventAttemptChanged, Payload: resultPayload,
	}); commitErr != nil {
		return nil, fmt.Errorf("work: RetryTask: commit attempt result: %w", commitErr)
	}

	current, _, err = s.store.LoadState(workID, "")
	if err != nil {
		return &finished, committedRecovery("retry-result-load", workID, requestID, 0, err)
	}
	persistedRun := findWorkflowRun(current, runID)
	if persistedRun == nil {
		return &finished, fmt.Errorf("work: RetryTask: run %q disappeared after result", runID)
	}
	if _, runErr := runner.Run(ctx, current, persistedRun, resultEmitter); runErr != nil {
		primaryErr := fmt.Errorf("work: RetryTask: advance retried task: %w", runErr)
		latestState, _, reloadErr := s.store.LoadState(workID, "")
		if reloadErr != nil {
			return &finished, errors.Join(primaryErr, committedRecovery("retry-runner-reload", workID, requestID, 0, reloadErr))
		}
		if latest := findAttempt(latestState, runID, stageID, taskID, attemptID); latest != nil {
			finished = *latest
		}
		return &finished, primaryErr
	}

	view, viewErr := s.loadView(workID)
	if viewErr != nil {
		return &finished, committedRecovery("retry-view", workID, requestID, 0, viewErr)
	}
	if emitErr := s.emitSnapshot(view, requestID); emitErr != nil {
		return &finished, committedRecovery("retry-view", workID, requestID, view.Revision, emitErr)
	}
	if persisted := findAttempt(view.Work, runID, stageID, taskID, attemptID); persisted != nil {
		finished = *persisted
	}
	if execErr != nil {
		return &finished, fmt.Errorf("work: RetryTask: %w", execErr)
	}
	return &finished, nil
}

// CancelRun persists a cancel intent and cancels any active Task Session.
// Terminal runs are a safe no-op. Repeated requestIDs return the same result.
func (s *Service) CancelRun(ctx context.Context, workID, runID, requestID string) error {
	if err := checkServiceContext(ctx); err != nil {
		return err
	}
	if err := s.requireStore(); err != nil {
		return err
	}
	requestID, err := requireRequestID("CancelRun", requestID)
	if err != nil {
		return err
	}
	workID = strings.TrimSpace(workID)
	runID = strings.TrimSpace(runID)
	if workID == "" || runID == "" {
		return errors.New("work: CancelRun: workID and runID are required")
	}

	return s.cancelRun(ctx, workID, runID, requestID)
}

func (s *Service) cancelRun(ctx context.Context, workID, runID, requestID string) error {
	eventRequestID := requestID + "/cancel"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return err
	}
	if current.ArchiveState != ArchiveActive {
		return fmt.Errorf("work: CancelRun: Work %s is %s", workID, current.ArchiveState)
	}

	run := findWorkflowRun(current, runID)
	if run == nil {
		return fmt.Errorf("work: CancelRun: run %q not found", runID)
	}

	if state.RequestFound {
		if state.RequestType != EventRunChanged {
			return fmt.Errorf("work: CancelRun: request %q was already used by %s", requestID, state.RequestType)
		}
		if run.Cancel == nil || run.Cancel.RequestID != requestID {
			return fmt.Errorf("work: CancelRun: committed request %q has no matching cancel receipt", requestID)
		}
		if run.Cancel.Status == CancelDelivered {
			return nil
		}
		return s.deliverRunCancel(ctx, workID, run, requestID)
	}

	if IsTerminalRunState(run.State) {
		return nil
	}

	// Persist the terminal cancel intent before touching any Session.
	now := time.Now().UTC()
	next := *run
	next.State = RunCancelled
	next.FinishedAt = timePtr(now)
	next.Cancel = &RunCancelReceipt{RequestID: requestID, Status: CancelPending, UpdatedAt: now}
	workState := WorkCancelled
	payload, marshalErr := json.Marshal(runEventPayload{Run: next, WorkState: workState})
	if marshalErr != nil {
		return fmt.Errorf("work: CancelRun: encode run: %w", marshalErr)
	}
	event := newServiceEvent(workID, eventRequestID, EventRunChanged, payload, now)
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
		return fmt.Errorf("work: CancelRun: commit cancel: %w", commitErr)
	}

	return s.deliverRunCancel(ctx, workID, &next, requestID)
}

func (s *Service) deliverRunCancel(ctx context.Context, workID string, run *WorkflowRun, requestID string) error {
	cancelErr := s.cancelRunActiveAttempts(ctx, workID, run, requestID)
	current, state, loadErr := s.store.LoadState(workID, "")
	if loadErr != nil {
		return errors.Join(cancelErr, committedRecovery("cancel-delivery-load", workID, requestID, 0, loadErr))
	}
	persisted := findWorkflowRun(current, run.ID)
	if persisted == nil {
		return errors.Join(cancelErr, fmt.Errorf("work: CancelRun: run %q disappeared after cancel intent", run.ID))
	}
	next := *persisted
	receipt := RunCancelReceipt{RequestID: requestID, Status: CancelDelivered, UpdatedAt: time.Now().UTC()}
	if persisted.Cancel != nil && persisted.Cancel.RequestID == requestID {
		receipt.Attempts = persisted.Cancel.Attempts
	}
	receipt.Attempts++
	if cancelErr != nil {
		receipt.Status = CancelFailed
		receipt.Error = cancelErr.Error()
	}
	next.Cancel = &receipt
	payload, err := json.Marshal(runEventPayload{Run: next, WorkState: WorkCancelled})
	if err != nil {
		return errors.Join(cancelErr, fmt.Errorf("work: CancelRun: encode delivery receipt: %w", err))
	}
	deliveryID := fmt.Sprintf("%s/cancel/delivery/%d", requestID, receipt.Attempts)
	event := newServiceEvent(workID, deliveryID, EventRunChanged, payload, receipt.UpdatedAt)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	if _, err := s.store.CommitEvent(workID, event); err != nil {
		return errors.Join(cancelErr, committedRecovery("cancel-delivery", workID, requestID, state.Revision, err))
	}
	view, err := s.loadView(workID)
	if err != nil {
		return errors.Join(cancelErr, committedRecovery("cancel-view", workID, requestID, event.Revision, err))
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return errors.Join(cancelErr, committedRecovery("cancel-view", workID, requestID, view.Revision, err))
	}
	if cancelErr != nil {
		return fmt.Errorf("work: CancelRun: cancel delivery failed and can be retried with request %q: %w", requestID, cancelErr)
	}
	return nil
}

func (s *Service) cancelRunActiveAttempts(ctx context.Context, workID string, run *WorkflowRun, requestID string) error {
	var targets []TaskCancelInput
	for si := range run.Stages {
		for ti := range run.Stages[si].Tasks {
			for ai := range run.Stages[si].Tasks[ti].Attempts {
				a := &run.Stages[si].Tasks[ti].Attempts[ai]
				if a.State == RunRunning {
					targets = append(targets, TaskCancelInput{
						WorkID: workID, RunID: run.ID, StageID: run.Stages[si].ID,
						TaskID: run.Stages[si].Tasks[ti].ID, AttemptID: a.ID,
						Session: a.SessionRef, RequestID: requestID + "/attempt/" + a.ID,
					})
				}
			}
		}
	}
	if len(targets) == 0 {
		return nil
	}
	runner := s.taskRunner()
	if runner == nil || runner.executor == nil {
		return errors.New("work: CancelRun: TaskExecutor is not configured")
	}
	var cancelErr error
	for _, target := range targets {
		err := runner.executor.CancelTask(ctx, target)
		if err != nil && !errors.Is(err, ErrTaskNotRunning) {
			cancelErr = errors.Join(cancelErr, fmt.Errorf("attempt %s: %w", target.AttemptID, err))
		}
	}
	return cancelErr
}

// PauseRun persists a pause intent, transitioning the run to RunWaiting and
// Work to WorkPaused. Only running runs can be paused.
func (s *Service) PauseRun(ctx context.Context, workID, runID, requestID string) error {
	if err := checkServiceContext(ctx); err != nil {
		return err
	}
	if err := s.requireStore(); err != nil {
		return err
	}
	requestID, err := requireRequestID("PauseRun", requestID)
	if err != nil {
		return err
	}
	workID = strings.TrimSpace(workID)
	runID = strings.TrimSpace(runID)
	if workID == "" || runID == "" {
		return errors.New("work: PauseRun: workID and runID are required")
	}

	return s.pauseRun(ctx, workID, runID, requestID)
}

func (s *Service) pauseRun(ctx context.Context, workID, runID, requestID string) error {
	eventRequestID := requestID + "/pause"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return err
	}
	if current.ArchiveState != ArchiveActive {
		return fmt.Errorf("work: PauseRun: Work %s is %s", workID, current.ArchiveState)
	}

	run := findWorkflowRun(current, runID)
	if run == nil {
		return fmt.Errorf("work: PauseRun: run %q not found", runID)
	}

	if state.RequestFound {
		if state.RequestType != EventRunChanged {
			return fmt.Errorf("work: PauseRun: request %q was already used by %s", requestID, state.RequestType)
		}
		return nil
	}

	if run.State != RunRunning {
		return fmt.Errorf("work: PauseRun: run %q is %s; only running runs can be paused", runID, run.State)
	}

	next := *run
	next.State = RunWaiting
	next.Pause = &RunPauseReceipt{RequestID: requestID, PausedAt: time.Now().UTC(), Notice: pauseRecoveryNotice}
	workState := WorkPaused
	payload, marshalErr := json.Marshal(runEventPayload{Run: next, WorkState: workState})
	if marshalErr != nil {
		return fmt.Errorf("work: PauseRun: encode run: %w", marshalErr)
	}
	event := newServiceEvent(workID, eventRequestID, EventRunChanged, payload, time.Now().UTC())
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
		return fmt.Errorf("work: PauseRun: commit pause: %w", commitErr)
	}

	view, viewErr := s.loadView(workID)
	if viewErr != nil {
		return committedRecovery("pause-view", workID, requestID, 0, viewErr)
	}
	if emitErr := s.emitSnapshot(view, requestID); emitErr != nil {
		return committedRecovery("pause-view", workID, requestID, view.Revision, emitErr)
	}
	return nil
}

// ResumeRun transitions a paused or waiting run back to running and continues
// execution through the runner. Gate resolutions are persisted before resuming.
func (s *Service) ResumeRun(ctx context.Context, input ResumeRunInput) (*WorkflowRun, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("ResumeRun", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	runID := strings.TrimSpace(input.RunID)
	if workID == "" || runID == "" {
		return nil, errors.New("work: ResumeRun: workID and runID are required")
	}

	for {
		flight, owner := s.beginRunFlight(workID)
		if owner {
			defer s.finishRunFlight(workID, flight)
			return s.resumeRun(ctx, workID, runID, requestID, input.GateResolutions)
		}
		if err := waitRunFlight(ctx, flight); err != nil {
			return nil, err
		}
	}
}

func (s *Service) resumeRun(ctx context.Context, workID, runID, requestID string, resolutions map[string]GateResolution) (*WorkflowRun, error) {
	runner := s.taskRunner()
	if runner == nil || runner.executor == nil {
		return nil, errors.New("work: ResumeRun: TaskExecutor is not configured")
	}
	eventRequestID := requestID + "/resume"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: ResumeRun: Work %s is %s", workID, current.ArchiveState)
	}

	run := findWorkflowRun(current, runID)
	if run == nil {
		return nil, fmt.Errorf("work: ResumeRun: run %q not found", runID)
	}

	alreadyResumed := state.RequestFound
	if alreadyResumed {
		if state.RequestType != EventRunChanged {
			return nil, fmt.Errorf("work: ResumeRun: request %q was already used by %s", requestID, state.RequestType)
		}
		if IsTerminalRunState(run.State) || run.State == RunNeedsConfirmation {
			return run, nil
		}
	}

	if !alreadyResumed && run.State != RunWaiting {
		return nil, fmt.Errorf("work: ResumeRun: run %q is %s; only waiting runs can be resumed", runID, run.State)
	}

	if !alreadyResumed {
		current, err = s.resolveRunCornerstones(ctx, current, requestID)
		if err != nil {
			return nil, fmt.Errorf("work: ResumeRun: resolve cornerstones: %w", err)
		}
		if blocked := s.checkRunCornerstones(current); blocked != nil {
			if emitErr := s.emitCornerstoneBlockedRun(workID, runID, requestID, blocked.Assessment, cornerstoneBlockResume); emitErr != nil {
				return nil, errors.Join(blocked, fmt.Errorf("work: ResumeRun: commit cornerstone blocked state: %w", emitErr))
			}
			view, loadErr := s.loadView(workID)
			if loadErr != nil {
				return nil, errors.Join(blocked, committedRecovery("resume-cornerstone-view", workID, requestID, 0, loadErr))
			}
			if emitErr := s.emitSnapshot(view, requestID); emitErr != nil {
				return nil, errors.Join(blocked, committedRecovery("resume-cornerstone-view", workID, requestID, view.Revision, emitErr))
			}
			return nil, blocked
		}
		run = findWorkflowRun(current, runID)
		if run == nil {
			return nil, fmt.Errorf("work: ResumeRun: run %q disappeared after cornerstone preflight", runID)
		}
		if err := ensureRunShape(current, run); err != nil {
			return nil, fmt.Errorf("work: ResumeRun: restore run shape after cornerstone preflight: %w", err)
		}

		// Resolve gates: persist the full decision context before resuming.
		for si := range run.Stages {
			stage := &run.Stages[si]
			if stage.State != RunWaiting || stage.Gate == "" {
				continue
			}
			resolution, ok := resolutions[stage.ID]
			if !ok {
				resolution, ok = resolutions[stage.Name]
			}
			if !ok {
				return nil, fmt.Errorf("work: ResumeRun: gate resolution is required for stage %q", stage.ID)
			}
			if err := validateGateResolution(stage, resolution); err != nil {
				return nil, fmt.Errorf("work: ResumeRun: %w", err)
			}
			resolution.StageID = stage.ID
			inputCopy, copyErr := cloneJSONMap(resolution.Input)
			if copyErr != nil {
				return nil, fmt.Errorf("work: ResumeRun: copy gate input: %w", copyErr)
			}
			resolution.Input = inputCopy
			nextStage := *stage
			nextStage.State = RunRunning
			nextStage.Resolution = &resolution
			stagePayload, marshalErr := json.Marshal(stageEventPayload{RunID: run.ID, Stage: nextStage, Resolution: &resolution})
			if marshalErr != nil {
				return nil, fmt.Errorf("work: ResumeRun: encode gate resolution: %w", marshalErr)
			}
			gateEvent := newServiceEvent(workID, requestID+"/gate/"+stage.ID, EventStageChanged, stagePayload, time.Now().UTC())
			if _, commitErr := s.store.CommitEvent(workID, gateEvent); commitErr != nil {
				return nil, fmt.Errorf("work: ResumeRun: commit gate resolution: %w", commitErr)
			}
		}

		// Reload to get gate-resolved stages before building the resume event.
		current, _, err = s.store.LoadState(workID, "")
		if err != nil {
			return nil, committedRecovery("resume-gate-reload", workID, requestID, 0, err)
		}
		run = findWorkflowRun(current, runID)
		if run == nil {
			return nil, fmt.Errorf("work: ResumeRun: run %q disappeared after gate resolution", runID)
		}
		if err := ensureRunShape(current, run); err != nil {
			return nil, fmt.Errorf("work: ResumeRun: persist restored run shape: %w", err)
		}

		// Transition run from waiting back to running.
		next := *run
		next.State = RunRunning
		if next.StartedAt.IsZero() {
			next.StartedAt = time.Now().UTC()
		}
		payload, marshalErr := json.Marshal(runEventPayload{Run: next, WorkState: WorkRunning})
		if marshalErr != nil {
			return nil, fmt.Errorf("work: ResumeRun: encode run: %w", marshalErr)
		}
		event := newServiceEvent(workID, eventRequestID, EventRunChanged, payload, time.Now().UTC())
		if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
			return nil, fmt.Errorf("work: ResumeRun: commit resume: %w", commitErr)
		}
	}

	// Reload to get the committed state.
	current, _, err = s.store.LoadState(workID, "")
	if err != nil {
		return nil, committedRecovery("resume-load", workID, requestID, 0, err)
	}
	persisted := findWorkflowRun(current, runID)
	if persisted == nil {
		return nil, fmt.Errorf("work: ResumeRun: run %q disappeared from projection", runID)
	}

	emit := s.runEmitter(workID, runID, "ResumeRun")
	if _, runErr := runner.Run(ctx, current, persisted, emit); runErr != nil {
		primaryErr := fmt.Errorf("work: ResumeRun: runner: %w", runErr)
		latestState, _, reloadErr := s.store.LoadState(workID, "")
		if reloadErr != nil {
			return persisted, errors.Join(primaryErr, committedRecovery("resume-runner-reload", workID, requestID, 0, reloadErr))
		}
		persisted = findWorkflowRun(latestState, runID)
		if persisted == nil {
			return nil, errors.Join(primaryErr, fmt.Errorf("work: ResumeRun: run %q disappeared after runner error", runID))
		}
		return persisted, primaryErr
	}

	view, viewErr := s.loadView(workID)
	if viewErr != nil {
		primaryErr := committedRecovery("resume-view", workID, requestID, 0, viewErr)
		latestState, _, reloadErr := s.store.LoadState(workID, "")
		if reloadErr != nil {
			return persisted, errors.Join(primaryErr, committedRecovery("resume-view-reload", workID, requestID, 0, reloadErr))
		}
		persisted = findWorkflowRun(latestState, runID)
		if persisted == nil {
			return nil, errors.Join(primaryErr, fmt.Errorf("work: ResumeRun: run %q disappeared after view error", runID))
		}
		return persisted, primaryErr
	}
	if emitErr := s.emitSnapshot(view, requestID); emitErr != nil {
		return findWorkflowRun(view.Work, runID), committedRecovery("resume-view", workID, requestID, view.Revision, emitErr)
	}
	return findWorkflowRun(view.Work, runID), nil
}

func (s *Service) taskRunner() *WorkRunner {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	return s.runner
}

func (s *Service) runEmitter(workID, runID, operation string) eventEmitter {
	return func(input WorkEvent) (int64, error) {
		if strings.TrimSpace(input.RequestID) == "" {
			return 0, fmt.Errorf("work: %s: runner emitted an event without requestID", operation)
		}
		current, state, err := s.store.LoadState(workID, "")
		if err != nil {
			return 0, err
		}
		run := findWorkflowRun(current, runID)
		if run == nil {
			return 0, fmt.Errorf("work: %s: run %q disappeared", operation, runID)
		}
		if IsTerminalRunState(run.State) || run.State == RunNeedsConfirmation {
			return state.Revision, fmt.Errorf("%w: run %q is %s", errRunSuspended, runID, run.State)
		}
		if run.State == RunWaiting && !pausedAttemptResult(run, input) {
			return state.Revision, fmt.Errorf("%w: run %q is waiting", errRunSuspended, runID)
		}
		event := newServiceEvent(workID, input.RequestID, input.Type, input.Payload, time.Now().UTC())
		event.BaseRevision, event.Revision = state.Revision, state.Revision+1
		revision, err := s.store.CommitEvent(workID, event)
		if err != nil {
			return revision, err
		}
		if s.sessionRefs == nil {
			return revision, nil
		}
		latest, _, loadErr := s.store.LoadState(workID, "")
		if loadErr != nil {
			return revision, committedRecovery("run-session-refs", workID, input.RequestID, revision, loadErr)
		}
		if refErr := s.syncSessionRefs(latest, input.RequestID+"/session-refs"); refErr != nil {
			return revision, committedRecovery("run-session-refs", workID, input.RequestID, revision, refErr)
		}
		return revision, nil
	}
}

func pausedAttemptResult(run *WorkflowRun, event WorkEvent) bool {
	if run == nil || event.Type != EventAttemptChanged {
		return false
	}
	payload, legacy, err := decodeAttemptEventPayload(event.Payload)
	if err != nil || legacy || payload.RunID != run.ID || payload.Attempt.State == RunRunning {
		return false
	}
	stage := findRunStage(run, payload.StageID)
	task := findStageTask(stage, payload.TaskID)
	current := findTaskAttempt(task, payload.Attempt.ID)
	return current != nil && current.State == RunRunning
}

func validateGateResolution(stage *Stage, resolution GateResolution) error {
	if stage == nil {
		return errors.New("gate stage is required")
	}
	outcome := strings.TrimSpace(resolution.Outcome)
	switch stage.Gate {
	case "approval":
		if outcome != "approved" {
			return fmt.Errorf("stage %q approval outcome must be approved", stage.ID)
		}
	case "input":
		if outcome != "input_provided" {
			return fmt.Errorf("stage %q input outcome must be input_provided", stage.ID)
		}
		if len(resolution.Input) == 0 {
			return fmt.Errorf("stage %q input is required", stage.ID)
		}
	default:
		return fmt.Errorf("stage %q has unsupported gate %q", stage.ID, stage.Gate)
	}
	return nil
}

func (s *Service) beginRunFlight(workID string) (*runFlight, bool) {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if flight := s.runFlights[workID]; flight != nil {
		return flight, false
	}
	flight := &runFlight{done: make(chan struct{})}
	if s.runFlights == nil {
		s.runFlights = make(map[string]*runFlight)
	}
	s.runFlights[workID] = flight
	return flight, true
}

func (s *Service) finishRunFlight(workID string, flight *runFlight) {
	s.runMu.Lock()
	if s.runFlights[workID] == flight {
		delete(s.runFlights, workID)
		close(flight.done)
	}
	s.runMu.Unlock()
}

func waitRunFlight(ctx context.Context, flight *runFlight) error {
	select {
	case <-flight.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func workflowRunID(workID, requestID string) string {
	digest := sha256.Sum256([]byte(workID + "\x00" + requestID))
	return fmt.Sprintf("run-%x", digest[:12])
}

func newPendingRun(value *Work, requestID string) WorkflowRun {
	run := WorkflowRun{
		ID:               workflowRunID(value.ID, requestID),
		WorkID:           value.ID,
		RequestID:        requestID,
		DefinitionDigest: value.Definition.Digest,
		State:            RunPending,
		Stages:           make([]Stage, 0, len(value.Definition.Workflow.Stages)),
	}
	for _, stageSpec := range value.Definition.Workflow.Stages {
		stage := Stage{
			ID:    runChildID(run.ID, "stage", stageSpec.ID),
			Name:  stageSpec.ID,
			Gate:  stageSpec.Gate,
			State: RunPending,
			Tasks: make([]Task, 0, len(stageSpec.Tasks)),
		}
		for _, taskSpec := range stageSpec.Tasks {
			stage.Tasks = append(stage.Tasks, Task{
				ID:       runChildID(stage.ID, "task", taskSpec.ID),
				Name:     taskSpec.ID,
				State:    RunPending,
				Attempts: []Attempt{},
			})
		}
		run.Stages = append(run.Stages, stage)
	}
	return run
}

func findWorkflowRun(value *Work, runID string) *WorkflowRun {
	if value == nil {
		return nil
	}
	for i := range value.Runs {
		if value.Runs[i].ID == runID {
			return &value.Runs[i]
		}
	}
	return nil
}

func findAttempt(value *Work, runID, stageID, taskID, attemptID string) *Attempt {
	run := findWorkflowRun(value, runID)
	stage := findRunStage(run, stageID)
	task := findStageTask(stage, taskID)
	return findTaskAttempt(task, attemptID)
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
	if err := requireWritableBlockSchemas(current, s.blockSchemaRegistry()); err != nil {
		return nil, fmt.Errorf("work: Archive: %w", err)
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
		if err := requireWritableBlockSchemas(current, s.blockSchemaRegistry()); err != nil {
			return nil, fmt.Errorf("work: Restore: %w", err)
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
	if err := requireWritableBlockSchemas(current, s.blockSchemaRegistry()); err != nil {
		return nil, fmt.Errorf("work: Restore: %w", err)
	}
	if state.RequestFound {
		if current.ArchiveState != ArchiveActive || !lifecycleRequestCurrent(state, EventWorkRestored) {
			return nil, lifecycleRequestConflict("Restore", workID, requestID, state)
		}
		view := viewFromState(current, state)
		if err := s.syncSessionRefs(view.Work, requestID+"/session-refs"); err != nil {
			return nil, committedRecovery("restore-session-refs", workID, requestID, view.Revision, err)
		}
		return view, nil
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
	if err := s.syncSessionRefs(view.Work, requestID+"/session-refs"); err != nil {
		return nil, committedRecovery("restore-session-refs", workID, requestID, view.Revision, err)
	}
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
	if err := requireWritableBlockSchemas(current, s.blockSchemaRegistry()); err != nil {
		return fmt.Errorf("work: Delete: %w", err)
	}
	if state.RequestFound && !lifecycleRequestCurrent(state, EventWorkDeleted) {
		return nil
	}
	if !state.RequestFound && (inTrash || current.ArchiveState == ArchiveDeleted) {
		return lifecycleRequestConflict("Delete", workID, requestID, state)
	}

	revision := state.RequestRevision
	if inTrash {
		if err := s.syncSessionRefs(current, requestID+"/session-refs"); err != nil {
			return committedRecovery("delete-session-refs", workID, requestID, revision, err)
		}
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
	if err := s.syncSessionRefs(latest, requestID+"/session-refs"); err != nil {
		return committedRecovery("delete-session-refs", workID, requestID, revision, err)
	}
	if err := s.store.MoveToTrash(workID, requestID+"/move"); err != nil {
		return fmt.Errorf("work: Delete: %w", err)
	}
	s.emitRemoved(workID, revision, requestID, current.SchemaVersion >= SchemaVersionV2)
	return nil
}

func (s *Service) requireStore() error {
	if s == nil || s.store == nil {
		return errors.New("work: Service requires a WorkStore")
	}
	return nil
}

func (s *Service) syncSessionRefs(value *Work, requestID string) error {
	if s.sessionRefs == nil {
		return nil
	}
	return s.sessionRefs.ReconcileWork(s.refScope, value, requestID)
}

func (s *Service) loadView(workID string) (*WorkView, error) {
	value, state, err := s.store.LoadState(workID, "")
	if err != nil {
		return nil, err
	}
	view := viewFromState(value, state)
	if err := s.prepareTransportView(view); err != nil {
		return nil, err
	}
	s.assessView(view)
	return view, nil
}

func (s *Service) prepareTransportView(view *WorkView) error {
	if s == nil || view == nil || view.Work == nil || view.Work.SchemaVersion < SchemaVersionV2 {
		return nil
	}
	if !s.v2Transport.Load() {
		stripV2PersistenceFields(view.Work)
		return nil
	}
	if view.SchemaVersion == WorkViewSchemaVersionV2 {
		return nil
	}
	var definition *WorkDefinitionRevision
	loadRev := view.Work.V2CurrentRevision
	if loadRev == 0 && view.Work.V2LatestRevision > 0 {
		loadRev = view.Work.V2LatestRevision // draft-only: use latest draft revision
	}
	if loadRev > 0 {
		s.defStoreMu.Lock()
		store := s.defStore
		s.defStoreMu.Unlock()
		if store == nil {
			return errors.New("work: V2 definition store is not configured")
		}
		var err error
		definition, err = store.LoadRevision(view.Work.ID, loadRev)
		if err != nil {
			return fmt.Errorf("work: load V2 transport definition: %w", err)
		}
		// Project authoritative status from V2RevisionStates.
		// The stored definition body is immutable (always "draft" at creation);
		// the runtime lifecycle status is tracked separately.
		if view.Work.V2RevisionStates != nil {
			if st, ok := view.Work.V2RevisionStates[loadRev]; ok {
				definition = cloneDefinitionForView(definition)
				definition.Status = st
			}
		}
	}
	promoteV2View(view, definition)
	stripV2PersistenceFields(view.Work)
	return nil
}

func viewFromState(value *Work, state WorkEventState) *WorkView {
	value = workForView(value)
	var cornerstones []Cornerstone
	if value != nil {
		cornerstones = value.Cornerstones
	}
	assessment := AssessCornerstones(cornerstones)
	return &WorkView{
		SchemaVersion: SchemaVersion,
		Work:          value,
		Revision:      state.Revision,
		Assessment:    assessment,
		RunBlock:      computeRunBlockReason(assessment, value),
	}
}

// workForView keeps the JSON contract stable for every frontend. Go nil slices
// encode as null, while WorkView's required collections are arrays in the
// shared transport contract. Normalize a shallow projection copy so loading an
// older persisted Work never mutates or rewrites its authoritative data.
func workForView(value *Work) *Work {
	if value == nil {
		return nil
	}
	view := *value
	view.Definition.Workflow.Stages = append([]StageSpec{}, value.Definition.Workflow.Stages...)
	for stageIndex := range view.Definition.Workflow.Stages {
		sourceStage := value.Definition.Workflow.Stages[stageIndex]
		view.Definition.Workflow.Stages[stageIndex].Tasks = append([]TaskSpec{}, sourceStage.Tasks...)
	}
	view.Definition.BlockSpecs = append([]BlockSpec{}, value.Definition.BlockSpecs...)
	view.Blocks = append([]BlockInstance{}, value.Blocks...)
	view.Placements = append([]BlockPlacement{}, value.Placements...)
	view.Cornerstones = append([]Cornerstone{}, value.Cornerstones...)
	view.V2ArtifactSlots = append([]ArtifactSlot{}, value.V2ArtifactSlots...)
	view.Runs = append([]WorkflowRun{}, value.Runs...)
	for runIndex := range view.Runs {
		view.Runs[runIndex].Stages = append([]Stage{}, value.Runs[runIndex].Stages...)
		for stageIndex := range view.Runs[runIndex].Stages {
			sourceStage := value.Runs[runIndex].Stages[stageIndex]
			view.Runs[runIndex].Stages[stageIndex].Tasks = append([]Task{}, sourceStage.Tasks...)
			for taskIndex := range view.Runs[runIndex].Stages[stageIndex].Tasks {
				sourceTask := sourceStage.Tasks[taskIndex]
				view.Runs[runIndex].Stages[stageIndex].Tasks[taskIndex].Attempts = append([]Attempt{}, sourceTask.Attempts...)
			}
		}
	}
	return &view
}

// assessView enriches a WorkView with authoritative blob-integrity checks and
// budget validation that a pure-function AssessCornerstones cannot perform.
// Must be called after viewFromState by callers that hold a BlobStore.
func (s *Service) assessView(view *WorkView) {
	if view == nil || view.Work == nil {
		return
	}
	// Rebuild from persisted facts on every call so Get/snapshot/retry are
	// idempotent and never accumulate synthetic issues.
	view.Assessment = AssessCornerstones(view.Work.Cornerstones)
	blobs, _ := s.store.(BlobStore)
	s.enrichBlobAssessment(view, blobs)

	// Blob integrity is checked above. Build without a BlobStore here to apply
	// the exact production context budgets without doing the same I/O twice.
	config := productionCornerstoneContextConfig()
	block, err := BuildCornerstoneContext(view.Work.Cornerstones, config)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "budget") {
		view.Assessment.Issues = append(view.Assessment.Issues, CornerstoneIssue{
			Problem: "budget_exhausted", Blocking: true,
		})
		view.Assessment.Blocking = true
		view.Assessment.State = CornerstoneUseBlocked
	} else if err == nil && block.Degraded {
		view.Assessment.Degraded = true
		if view.Assessment.State == CornerstoneUseReady {
			view.Assessment.State = CornerstoneUseDegraded
		}
	}
	view.RunBlock = computeRunBlockReason(view.Assessment, view.Work)
}

// enrichBlobAssessment verifies snapshot blob integrity through the BlobStore.
// Required active cornerstones whose blobs are missing/invalid are downgraded
// to blocked in the assessment.
func (s *Service) enrichBlobAssessment(view *WorkView, blobs BlobStore) {
	if blobs == nil {
		return
	}
	for _, cs := range view.Work.Cornerstones {
		if cs.Tombstone || cs.Status != CornerstoneActive || cs.Mode != CornerstoneSnapshot {
			continue
		}
		if cs.Ref.BlobDigest == "" {
			continue
		}
		workID := cs.WorkID
		if workID == "" {
			workID = view.Work.ID
		}
		exists, err := blobs.Exists(workID, cs.Ref.BlobDigest)
		if err != nil || !exists {
			issue := CornerstoneIssue{
				CornerstoneID: cs.ID,
				Title:         cs.Title,
				Problem:       "blob_missing",
				Blocking:      cs.Required,
			}
			view.Assessment.Issues = append(view.Assessment.Issues, issue)
			if cs.Required {
				view.Assessment.Blocking = true
				view.Assessment.State = CornerstoneUseBlocked
			} else {
				view.Assessment.Degraded = true
				if view.Assessment.State == CornerstoneUseReady {
					view.Assessment.State = CornerstoneUseDegraded
				}
			}
		}
	}
}

// computeRunBlockReason builds the authoritative run-block projection from the
// Cornerstone assessment and Work state. It uses stable codes so the UI can map
// each item to icons/labels without parsing English detail strings.
func computeRunBlockReason(assessment CornerstoneAssessment, w *Work) *RunBlockReason {
	if w == nil {
		return nil
	}
	var items []RunBlockItem

	// waiting_user is the authoritative terminal blocked state.
	if w.State == WorkWaitingUser {
		items = append(items, RunBlockItem{
			Code:   RunBlockWaitingUser,
			Detail: "work is waiting for user input",
		})
	}

	// Required cornerstone failures from the assessment.
	for _, issue := range assessment.Issues {
		if !issue.Blocking {
			continue
		}
		code, detail := mapCornerstoneIssueToCode(issue.Problem)
		var status CornerstoneStatus
		for _, cs := range w.Cornerstones {
			if cs.ID == issue.CornerstoneID {
				status = cs.Status
				break
			}
		}
		items = append(items, RunBlockItem{
			Code:          code,
			CornerstoneID: issue.CornerstoneID,
			Status:        status,
			Detail:        detail,
		})
	}

	// Failed Work cannot run without retry.
	if w.State == WorkFailed {
		items = append(items, RunBlockItem{Code: RunBlockFailed, Detail: "work has failed"})
	}
	// Archived or deleted Work cannot run.
	if w.ArchiveState != ArchiveActive {
		items = append(items, RunBlockItem{Code: RunBlockArchived, Detail: "work is " + string(w.ArchiveState)})
	}

	if len(items) == 0 {
		return nil
	}
	return &RunBlockReason{Blocked: true, Items: items}
}

// mapCornerstoneIssueToCode converts an issue problem string to a stable code.
func mapCornerstoneIssueToCode(problem string) (RunBlockCode, string) {
	switch {
	case problem == "blob_missing":
		return RunBlockBlobMissing, "snapshot blob is missing or invalid"
	case problem == "budget_exhausted":
		return RunBlockBudgetExhausted, "required cornerstone context exceeds the production budget"
	case strings.Contains(problem, ":network"):
		return RunBlockResolverUnavailable, "cornerstone resolver is temporarily unavailable"
	case strings.Contains(problem, "missing"):
		return RunBlockCornerstoneMissing, "required cornerstone source is missing"
	case strings.Contains(problem, "denied"):
		return RunBlockCornerstoneDenied, "required cornerstone source is denied"
	case strings.Contains(problem, "invalid"):
		return RunBlockCornerstoneInvalid, "required cornerstone is invalid"
	case strings.Contains(problem, "stale"):
		return RunBlockCornerstoneStale, "required cornerstone is stale"
	default:
		return RunBlockCornerstoneInvalid, "required cornerstone is unavailable"
	}
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
	if err := s.syncSessionRefs(archived, requestID+"/session-refs"); err != nil {
		return nil, committedRecovery("archive-session-refs", workID, requestID, revision, err)
	}
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
	view := viewFromState(archived, WorkEventState{Revision: revision})
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
	if err := s.prepareTransportView(view); err != nil {
		return err
	}
	s.assessView(view)
	payload, err := json.Marshal(view)
	if err != nil {
		return fmt.Errorf("work: encode WorkView snapshot: %w", err)
	}
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: view.SchemaVersion,
		Type:          ViewSnapshot,
		WorkID:        view.Work.ID,
		EventID:       fmt.Sprintf("work-view-%s-%d", view.Work.ID, view.Revision),
		Revision:      view.Revision,
		BaseRevision:  0,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectWork, ID: view.Work.ID, WorkID: view.Work.ID},
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	})
	return nil
}

func (s *Service) emitRemoved(workID string, revision int64, requestID string, v2 bool) {
	schemaVersion := WorkViewSchemaVersion
	if v2 && s.v2Transport.Load() {
		schemaVersion = WorkViewSchemaVersionV2
	}
	s.sink.EmitWorkView(WorkViewEvent{
		SchemaVersion: schemaVersion,
		Type:          ViewRemoved,
		WorkID:        workID,
		EventID:       fmt.Sprintf("work-view-%s-%d", workID, revision),
		Revision:      revision,
		BaseRevision:  revision - 1,
		RequestID:     requestID,
		Object:        ObjectContext{Kind: ObjectWork, ID: workID, WorkID: workID},
		Payload:       json.RawMessage(`{"archiveState":"deleted"}`),
		CreatedAt:     time.Now().UTC(),
	})
}

// ── Cornerstone delegate methods ─────────────────────────────────────────────

// ErrCornerstoneDisabled is returned by cornerstone methods when no
// CornerstoneManager is wired into the Service.
var ErrCornerstoneDisabled = errors.New("work: Cornerstone feature is disabled; wire a CornerstoneManager via SetCornerstoneManager")

func (s *Service) requireCornerstone() error {
	if s == nil || s.cornerstones == nil {
		return ErrCornerstoneDisabled
	}
	return nil
}

func (s *Service) PinCornerstone(ctx context.Context, workID string, input PinCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Pin(workID, input))
}

func (s *Service) RefreshCornerstone(ctx context.Context, workID string, input RefreshCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Refresh(ctx, workID, input))
}

func (s *Service) RemoveCornerstone(ctx context.Context, workID string, input RemoveCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Remove(workID, input))
}

func (s *Service) UndoCornerstone(ctx context.Context, workID string, input UndoCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Undo(workID, input))
}

func (s *Service) AcceptCornerstone(ctx context.Context, workID string, input AcceptCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Accept(ctx, workID, input))
}

func (s *Service) FreezeCornerstone(ctx context.Context, workID string, input FreezeCornerstoneInput) (*CornerstoneResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneMutation(s.cornerstones.Freeze(ctx, workID, input))
}

func (s *Service) RepairCornerstone(ctx context.Context, workID string, input RepairCornerstoneInput) (*RepairResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireCornerstone(); err != nil {
		return nil, err
	}
	return s.finishCornerstoneRepair(s.cornerstones.Repair(ctx, workID, input))
}

// finishCornerstoneMutation is the single production boundary for mutation
// projections returned by CornerstoneManager. Manager views intentionally
// contain persisted facts only; Service owns authoritative I/O-backed
// assessment (blob integrity and production context budget). Enrich partial
// results even when err is non-nil so retryable recovery never exposes a view
// that disagrees with Get at the same revision.
func (s *Service) finishCornerstoneMutation(result *CornerstoneResult, err error) (*CornerstoneResult, error) {
	if result != nil && result.WorkView != nil {
		s.assessView(result.WorkView)
		result.Revision = result.WorkView.Revision
		result.Assessment = result.WorkView.Assessment
	}
	return result, err
}

func (s *Service) finishCornerstoneRepair(result *RepairResult, err error) (*RepairResult, error) {
	if result != nil && result.WorkView != nil {
		s.assessView(result.WorkView)
		result.Revision = result.WorkView.Revision
		result.Assessment = result.WorkView.Assessment
	}
	return result, err
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
		Kind:   WorkEventRevisionConflict,
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

func updateDraftTargetState(current *Work) (WorkState, error) {
	if current == nil {
		return "", errors.New("work: UpdateDraft: Work is required")
	}
	if current.SchemaVersion >= SchemaVersionV2 {
		switch current.State {
		case WorkRunning, WorkWaitingUser, WorkPaused, WorkCompleted:
			// V2 edits prepare a candidate Definition while the current run and
			// lifecycle remain authoritative. Applying that candidate performs
			// the explicit transition back to running.
			return current.State, nil
		}
	}
	return draftTargetState(current.State)
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
	return blocks, sortPlacements(placements)
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

func workNameFromPrompt(prompt, fallback string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if line == "" {
		line = strings.TrimSpace(fallback)
	}
	if line == "" {
		line = "未命名 Work"
	}
	const maxRunes = 32
	runes := []rune(line)
	if len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return line
}

func workNameIsAutomatic(current *Work) bool {
	if current == nil || strings.TrimSpace(current.Prompt) == "" {
		return true
	}
	return strings.TrimSpace(current.Name) == workNameFromPrompt(current.Prompt, "")
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
