package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"workground2/internal/nilutil"
)

// Service owns every Work lifecycle write. The event log is authoritative;
// projections, manifests, indexes and archive files are derived side effects
// repaired by WorkStore on retry or reload.
type Service struct {
	store       WorkStore
	blueprint   *BlueprintRegistry
	tools       ToolCatalog
	sink        ViewSink
	actions     *ActionRegistry
	permissions PermissionChecker
	runner      *WorkRunner
	runMu       sync.Mutex
	runFlights  map[string]*runFlight
	actionCfgMu sync.RWMutex
	actionMu    sync.Mutex
	actionRuns  map[string]*actionFlight
}

type runFlight struct{ done chan struct{} }

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
	return &Service{
		store: store, blueprint: blueprint, tools: tools, sink: sink,
		actionRuns: make(map[string]*actionFlight), runFlights: make(map[string]*runFlight),
	}
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
	if err := requireWritableBlockSchemas(current); err != nil {
		return nil, fmt.Errorf("work: UpdateDraft: %w", err)
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
		return s.store.CommitEvent(workID, event)
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
	if err := requireWritableBlockSchemas(current); err != nil {
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
		if err := requireWritableBlockSchemas(current); err != nil {
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
	if err := requireWritableBlockSchemas(current); err != nil {
		return nil, fmt.Errorf("work: Restore: %w", err)
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
	if err := requireWritableBlockSchemas(current); err != nil {
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
