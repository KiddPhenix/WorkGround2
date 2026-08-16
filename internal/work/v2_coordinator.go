package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// V2Coordinator is the domain production entry that joins committed
// Input/Definition/Patch mutations to authoritative runtime invalidation and
// affected-only scheduling. Business events remain the durable recovery
// record; retrying the same request or RecoverScheduling safely resumes wake.
type V2Coordinator struct {
	store WorkStore

	mu             sync.RWMutex
	defs           DefinitionRevisionStore
	inputs         *InputService
	patches        *PatchService
	scheduler      *V2Scheduler
	commitObserver v2CommitObserver
	skillResolver  SkillResolver
}

type v2CommitObserver func(workID string, baseRevision int64, requestID string) error

// V2RecoveryFailure is one independently retryable boot-recovery failure.
type V2RecoveryFailure struct {
	WorkID string `json:"workId"`
	Error  string `json:"error"`
}

// V2RecoveryReport makes boot recovery observable without making one damaged
// Work hide the recovery outcome of every other Work.
type V2RecoveryReport struct {
	Scanned   int                 `json:"scanned"`
	Recovered int                 `json:"recovered"`
	Failures  []V2RecoveryFailure `json:"failures,omitempty"`
}

func newV2Coordinator(
	store WorkStore,
	defs DefinitionRevisionStore,
	cornerstones *CornerstoneManager,
) *V2Coordinator {
	inputs := NewInputService(store, cornerstones)
	patches := NewPatchService(store)
	if defs != nil {
		inputs.SetDefinitionStore(defs)
		patches.SetDefinitionStore(defs)
	}
	return &V2Coordinator{
		store: store, defs: defs, inputs: inputs, patches: patches,
	}
}

func (c *V2Coordinator) SetDefinitionStore(defs DefinitionRevisionStore) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.defs = defs
	c.inputs.SetDefinitionStore(defs)
	c.patches.SetDefinitionStore(defs)
}

func (c *V2Coordinator) SetExecutor(executor TaskExecutor) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if executor == nil {
		c.scheduler = nil
		return
	}
	c.scheduler = NewV2Scheduler(executor)
	if c.skillResolver != nil {
		c.scheduler.SetSkillResolver(c.skillResolver)
	}
}

func (c *V2Coordinator) SetCommitObserver(observer v2CommitObserver) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.commitObserver = observer
	c.mu.Unlock()
}

func (c *V2Coordinator) SetSkillResolver(resolver SkillResolver) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.skillResolver = resolver
	if c.scheduler != nil {
		c.scheduler.SetSkillResolver(resolver)
	}
}

func (c *V2Coordinator) skillResolverSnapshot() SkillResolver {
	if c == nil {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.skillResolver
}

func (c *V2Coordinator) SetPatchPlanner(planner PatchPlanner) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.patches.SetPlanner(planner)
}

func (c *V2Coordinator) SetCornerstones(cornerstones *CornerstoneManager) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inputs.cornerstones = cornerstones
}

func (c *V2Coordinator) enabled() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.store != nil && c.defs != nil && c.scheduler != nil
}

// SubmitInput commits through the real InputService, then derives all scheduler
// inputs from the authoritative projection. Callers never assemble runtime
// digests or supply a stale input snapshot.
func (c *V2Coordinator) SubmitInput(
	ctx context.Context,
	request SubmitInputRequest,
) (*SubmitInputResult, error) {
	if c == nil || c.inputs == nil {
		return nil, errors.New("work: V2 input coordinator is not configured")
	}
	wasReadyForStart := false
	if !request.DeferStart {
		before, loadErr := c.store.LoadProjection(request.WorkID)
		if loadErr != nil {
			return nil, fmt.Errorf("work: load input start gate: %w", loadErr)
		}
		if index := findInputIndex(before, request.InputID); index >= 0 {
			wasReadyForStart = before.V2Inputs[index].ReadyForStart
		}
	}
	result, err := c.inputs.SubmitInput(ctx, request)
	if err != nil || result == nil || result.Error != "" {
		return result, err
	}
	if request.DeferStart {
		return result, nil
	}
	runID := ""
	if result.Input != nil {
		runID = result.Input.RunID
	}
	if runID == "" {
		projection, loadErr := c.store.LoadProjection(request.WorkID)
		if loadErr != nil {
			return result, committedRecovery("v2-input-wake", request.WorkID, request.RequestID, result.Revision, loadErr)
		}
		runID = runIDForAffectedTasks(projection, result.AffectedTaskIDs)
	}
	cause := V2WakeInput
	if c.inputKind(request.WorkID, result.Input) == InputApproval {
		cause = V2WakeApproval
	}
	if wasReadyForStart {
		projection, loadErr := c.store.LoadProjection(request.WorkID)
		if loadErr != nil {
			return result, committedRecovery("v2-ready-input-reload", request.WorkID, request.RequestID, result.Revision, loadErr)
		}
		for _, input := range projection.V2Inputs {
			if input.RunID == runID && input.ReadyForStart {
				return result, nil
			}
		}
		if wakeErr := c.RecoverScheduling(ctx, request.WorkID); wakeErr != nil {
			return result, committedRecovery("v2-ready-input-wake", request.WorkID, request.RequestID, result.Revision, wakeErr)
		}
		return result, nil
	}
	if wakeErr := c.continueRun(ctx, request.WorkID, runID, result.AffectedTaskIDs, cause); wakeErr != nil {
		return result, committedRecovery("v2-input-wake", request.WorkID, request.RequestID, result.Revision, wakeErr)
	}
	return result, nil
}

func (c *V2Coordinator) AddCustomInput(
	ctx context.Context,
	request AddCustomWorkInputRequest,
) (*SubmitInputResult, error) {
	if c == nil || c.inputs == nil {
		return nil, errors.New("work: V2 input coordinator is not configured")
	}
	return c.inputs.AddCustomInput(ctx, request)
}

// ApplyPatch commits through the real PatchService and automatically resumes
// only the preview's invalidated task subgraph.
func (c *V2Coordinator) ApplyPatch(
	ctx context.Context,
	input ApplyWorkPatchInput,
) (*ApplyWorkPatchResult, error) {
	if c == nil || c.patches == nil {
		return nil, errors.New("work: V2 patch coordinator is not configured")
	}
	before, err := c.store.LoadProjection(input.WorkID)
	if err != nil {
		return nil, err
	}
	preview, ok := before.V2PatchPreviews[input.PatchID]
	if !ok {
		return nil, fmt.Errorf("work: V2 patch coordinator: preview %q not found", input.PatchID)
	}
	result, err := c.patches.ApplyWorkPatch(ctx, input)
	if err != nil || result == nil || result.Error != "" {
		return result, err
	}
	definitionRev := before.V2CurrentRevision
	runID := preview.RunID
	changedIDs := result.InvalidatedTaskIDs
	if input.Scope == PatchWorkflow && result.NewRevision > 0 {
		definitionRev = result.NewRevision
		after, loadErr := c.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			return result, committedRecovery("v2-patch-wake", input.WorkID, input.RequestID, result.WorkRevision, loadErr)
		}
		definition, loadErr := c.loadDefinition(input.WorkID, definitionRev)
		if loadErr != nil {
			return result, committedRecovery("v2-patch-wake", input.WorkID, input.RequestID, result.WorkRevision, loadErr)
		}
		runID = activeDefinitionRunID(after, definition.Digest)
		if reconcileErr := c.reconcilePatchArtifacts(
			ctx,
			input.WorkID,
			runID,
			input.RequestID,
			preview,
			definition,
		); reconcileErr != nil {
			return result, committedRecovery(
				"v2-patch-artifacts",
				input.WorkID,
				input.RequestID,
				result.WorkRevision,
				reconcileErr,
			)
		}
		after, loadErr = c.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			return result, committedRecovery("v2-patch-wake", input.WorkID, input.RequestID, result.WorkRevision, loadErr)
		}
		changedIDs = definitionRunSeeds(
			definition,
			after,
			runID,
			changedIDs,
		)
	}
	if wakeErr := c.continueRunAt(
		ctx,
		input.WorkID,
		runID,
		changedIDs,
		V2WakePatch,
		definitionRev,
	); wakeErr != nil {
		return result, committedRecovery("v2-patch-wake", input.WorkID, input.RequestID, result.WorkRevision, wakeErr)
	}
	return result, nil
}

func (c *V2Coordinator) PreviewPatch(
	ctx context.Context,
	input PreviewWorkPatchInput,
) (*PreviewWorkPatchResult, error) {
	if c == nil || c.patches == nil {
		return nil, errors.New("work: V2 patch coordinator is not configured")
	}
	return c.patches.PreviewWorkPatch(ctx, input)
}

// SetInputCornerstone routes the public V2 request through InputService so the
// WorkInput link, Cornerstone mutation, receipt, and partial-failure semantics
// stay on the same authoritative production path.
func (c *V2Coordinator) SetInputCornerstone(
	ctx context.Context,
	input SetInputCornerstoneRequest,
) (*CornerstonePinResult, error) {
	if c == nil || c.inputs == nil || c.store == nil {
		return nil, errors.New("work: V2 input coordinator is not configured")
	}
	request := PinInputRequest{
		WorkID:           input.WorkID,
		InputID:          input.InputID,
		DefinitionRev:    input.DefinitionRevision,
		InputRevision:    input.InputRevision,
		ExpectedRevision: input.ExpectedRevision,
		RequestID:        input.RequestID,
	}
	oppositeRequestID := strings.TrimSpace(input.RequestID) + "/pin-cs"
	if input.Pin {
		oppositeRequestID = strings.TrimSpace(input.RequestID) + "/unpin-cs"
	}
	if _, state, err := c.store.LoadState(strings.TrimSpace(input.WorkID), oppositeRequestID); err == nil && state.RequestFound {
		return nil, fmt.Errorf(
			"%w: SetInputCornerstone requestID %q was already committed with the opposite pin intent",
			ErrWorkRequestIDConflict,
			strings.TrimSpace(input.RequestID),
		)
	}
	partialRequestID := strings.TrimSpace(input.RequestID) + "/cs/cs"
	oppositePartialType := EventCornerstoneUpserted
	if input.Pin {
		partialRequestID = strings.TrimSpace(input.RequestID) + "/cs/cs-remove"
		oppositePartialType = EventCornerstoneRemoved
	}
	if _, state, err := c.store.LoadState(strings.TrimSpace(input.WorkID), partialRequestID); err == nil && state.RequestFound {
		oppositePartial := state.RequestType == oppositePartialType
		if oppositePartial {
			return nil, fmt.Errorf(
				"%w: SetInputCornerstone requestID %q already partially committed with the opposite pin intent",
				ErrWorkRequestIDConflict,
				strings.TrimSpace(input.RequestID),
			)
		}
	}
	var (
		result *PinInputResult
		err    error
	)
	if input.Pin {
		result, err = c.inputs.PinInput(ctx, request)
	} else {
		result, err = c.inputs.UnpinInput(ctx, request)
	}
	if result == nil {
		return nil, err
	}
	return &CornerstonePinResult{
		CornerstoneID: result.CornerstoneID,
		Receipt:       cloneInputIntentReceipt(result.Receipt),
		Pinned:        result.Pinned,
		Revision:      result.Revision,
		Duplicate:     result.Duplicate,
		Error:         result.Error,
		Committed:     result.Revision > 0,
	}, err
}

// RetryNode makes the retry intent durable before scheduling. Replaying the
// same request validates the stored intent but never executes the task twice;
// a wake failure after commit is returned as an explicit recoverable error.
func (c *V2Coordinator) RetryNode(
	ctx context.Context,
	input RetryWorkNodeRequest,
) (*Task, error) {
	if c == nil || c.store == nil || c.schedulerSnapshot() == nil {
		return nil, errors.New("work: V2 retry coordinator is not configured")
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.RunID = strings.TrimSpace(input.RunID)
	input.TaskID = strings.TrimSpace(input.TaskID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.RunID == "" || input.TaskID == "" || input.RequestID == "" {
		return nil, errors.New("work: RetryWorkNode: workID/runID/taskID/requestID are required")
	}
	if input.ExpectedRevision <= 0 {
		return nil, errors.New("work: RetryWorkNode: expectedRevision must be positive")
	}

	projection, state, err := c.store.LoadState(input.WorkID, input.RequestID+"/retry-node")
	if err != nil {
		return nil, err
	}
	if err := CheckSchemaVersionV2("Work", projection.SchemaVersion); err != nil {
		return nil, err
	}
	if projection.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: RetryWorkNode is not allowed while ArchiveState=%s", projection.ArchiveState)
	}
	runtime := projection.V2TaskRuntimes[input.TaskID]
	definitionRev := projection.V2CurrentRevision
	if runtime != nil {
		definitionRev = runtime.DefinitionRev
	}

	payload, err := json.Marshal(TaskReadyPayload{
		TaskID: input.TaskID,
		WorkID: input.WorkID,
		RunID:  input.RunID,
	})
	if err != nil {
		return nil, fmt.Errorf("work: RetryWorkNode: encode retry intent: %w", err)
	}
	event := newServiceEventV2(
		input.WorkID,
		input.RequestID+"/retry-node",
		EventTaskReady,
		payload,
		time.Now().UTC(),
	)
	event.Object = ObjectContext{
		Kind:               ObjectTask,
		ID:                 input.TaskID,
		WorkID:             input.WorkID,
		RunID:              input.RunID,
		TaskID:             input.TaskID,
		ExpectedRevision:   int64Ptr(input.ExpectedRevision),
		DefinitionRevision: int64Ptr(definitionRev),
	}

	if state.RequestFound {
		if loader, ok := c.store.(interface {
			LoadRequestEvent(string, string) (WorkEvent, error)
		}); ok {
			persisted, loadErr := loader.LoadRequestEvent(input.WorkID, event.RequestID)
			if loadErr != nil {
				return nil, loadErr
			}
			if err := validateRetryNodeReplay(input, persisted); err != nil {
				return nil, err
			}
		} else if _, err := c.store.CommitEvent(input.WorkID, event); err != nil {
			var conflict *ErrWorkEventConflict
			if errors.As(err, &conflict) && conflict.Kind == WorkEventRequestConflict {
				return nil, fmt.Errorf("%w: %w", ErrWorkRequestIDConflict, err)
			}
			return nil, err
		}
		current, loadErr := c.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			return nil, loadErr
		}
		currentRuntime := current.V2TaskRuntimes[input.TaskID]
		if currentRuntime == nil || currentRuntime.RunID != input.RunID {
			return nil, fmt.Errorf("work: RetryWorkNode: persisted retry task %q not found in run %q", input.TaskID, input.RunID)
		}
		if !isActiveRetryRuntime(current, currentRuntime) {
			return v2TaskRuntimeToTask(currentRuntime), nil
		}
		if currentRuntime.State == TaskReady {
			if prepareErr := c.prepareV2NodeArtifacts(
				ctx,
				input.WorkID,
				currentRuntime.DefinitionRev,
				currentRuntime.NodeID,
				input.RequestID,
			); prepareErr != nil {
				return v2TaskRuntimeToTask(currentRuntime),
					committedRecovery("retry-work-node-artifacts", input.WorkID, input.RequestID, state.RequestRevision, prepareErr)
			}
			if _, wakeErr := c.ScheduleRun(ctx, input.WorkID, input.RunID, []string{currentRuntime.NodeID}); wakeErr != nil {
				return v2TaskRuntimeToTask(currentRuntime),
					committedRecovery("retry-work-node-wake", input.WorkID, input.RequestID, state.RequestRevision, wakeErr)
			}
			current, loadErr = c.store.LoadProjection(input.WorkID)
			if loadErr != nil {
				return nil, committedRecovery("retry-work-node-reload", input.WorkID, input.RequestID, state.RequestRevision, loadErr)
			}
			currentRuntime = current.V2TaskRuntimes[input.TaskID]
		}
		return v2TaskRuntimeToTask(currentRuntime), nil
	}
	if runtime == nil || runtime.RunID != input.RunID {
		return nil, fmt.Errorf("work: RetryWorkNode: task %q not found in run %q", input.TaskID, input.RunID)
	}
	if !isActiveRetryRuntime(projection, runtime) {
		return nil, fmt.Errorf(
			"work: RetryWorkNode: task %q belongs to historical run %q or definition revision %d; active revision is %d",
			input.TaskID,
			runtime.RunID,
			runtime.DefinitionRev,
			projection.V2CurrentRevision,
		)
	}
	// RetryWorkNodeRequest is part of the frozen Controller contract. Existing
	// domain callers use the task runtime revision, while Desktop receives only
	// the aggregate WorkView revision. Both are authoritative current versions:
	// accept either, but reject a value stale against both. This preserves the
	// object-level guard and lets Desktop recover after unrelated Work events.
	if runtime.Revision != input.ExpectedRevision && state.Revision != input.ExpectedRevision {
		return nil, revisionConflict(input.WorkID, input.ExpectedRevision, state.Revision)
	}
	switch runtime.State {
	case TaskFailedRetryable, TaskInvalidated:
	default:
		return nil, fmt.Errorf("work: RetryWorkNode requires failed_retryable or invalidated task, current state: %s", runtime.State)
	}

	revision, err := c.store.CommitEvent(input.WorkID, event)
	if err != nil {
		return nil, err
	}
	if err := c.prepareV2NodeArtifacts(
		ctx,
		input.WorkID,
		runtime.DefinitionRev,
		runtime.NodeID,
		input.RequestID,
	); err != nil {
		current, loadErr := c.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			err = errors.Join(err, loadErr)
			return nil, committedRecovery("retry-work-node-artifacts", input.WorkID, input.RequestID, revision, err)
		}
		return v2TaskRuntimeToTask(current.V2TaskRuntimes[input.TaskID]),
			committedRecovery("retry-work-node-artifacts", input.WorkID, input.RequestID, revision, err)
	}
	if _, err := c.ScheduleRun(ctx, input.WorkID, input.RunID, []string{runtime.NodeID}); err != nil {
		current, loadErr := c.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			err = errors.Join(err, loadErr)
			return nil, committedRecovery("retry-work-node-wake", input.WorkID, input.RequestID, revision, err)
		}
		return v2TaskRuntimeToTask(current.V2TaskRuntimes[input.TaskID]),
			committedRecovery("retry-work-node-wake", input.WorkID, input.RequestID, revision, err)
	}
	current, err := c.store.LoadProjection(input.WorkID)
	if err != nil {
		return nil, committedRecovery("retry-work-node-reload", input.WorkID, input.RequestID, revision, err)
	}
	return v2TaskRuntimeToTask(current.V2TaskRuntimes[input.TaskID]), nil
}

func validateRetryNodeReplay(input RetryWorkNodeRequest, event WorkEvent) error {
	if event.Type != EventTaskReady {
		return fmt.Errorf(
			"%w: RetryWorkNode request %q was committed as %s",
			ErrWorkRequestIDConflict,
			input.RequestID,
			event.Type,
		)
	}
	var payload TaskReadyPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("%w: decode RetryWorkNode event: %v", ErrWorkNeedsRepair, err)
	}
	expected := int64(0)
	if event.Object.ExpectedRevision != nil {
		expected = *event.Object.ExpectedRevision
	}
	if payload.WorkID != input.WorkID || payload.RunID != input.RunID || payload.TaskID != input.TaskID ||
		expected != input.ExpectedRevision {
		return fmt.Errorf("%w: RetryWorkNode request %q intent changed", ErrWorkRequestIDConflict, input.RequestID)
	}
	return nil
}

// SetNodeSkill binds a Skill name to a Work node. The binding is persisted as a
// V2 event and projected onto Work.V2NodeSkillBindings. Repeated calls with the
// same requestID+skillName are idempotent (duplicate); different skillName with
// the same requestID is a conflict.
func (c *V2Coordinator) SetNodeSkill(
	ctx context.Context,
	input SetNodeSkillRequest,
) (*SetNodeSkillResult, error) {
	if c == nil || c.store == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.SkillName = strings.TrimSpace(input.SkillName)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.NodeID == "" || input.SkillName == "" || input.RequestID == "" {
		return nil, errors.New("work: SetNodeSkill: workID/nodeID/skillName/requestID are required")
	}
	if input.ExpectedRevision <= 0 {
		return nil, errors.New("work: SetNodeSkill: expectedRevision must be positive")
	}

	projection, state, err := c.store.LoadState(input.WorkID, input.RequestID+"/set-node-skill")
	if err != nil {
		return nil, err
	}
	if err := CheckSchemaVersionV2("Work", projection.SchemaVersion); err != nil {
		return nil, err
	}
	if projection.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: SetNodeSkill is not allowed while ArchiveState=%s", projection.ArchiveState)
	}

	payload, err := json.Marshal(NodeSkillBoundPayload{
		WorkID:    input.WorkID,
		NodeID:    input.NodeID,
		SkillName: input.SkillName,
	})
	if err != nil {
		return nil, fmt.Errorf("work: SetNodeSkill: encode payload: %w", err)
	}
	event := newServiceEventV2(
		input.WorkID,
		input.RequestID+"/set-node-skill",
		EventNodeSkillBound,
		payload,
		time.Now().UTC(),
	)
	event.Object = ObjectContext{
		Kind:               ObjectNode,
		ID:                 input.NodeID,
		WorkID:             input.WorkID,
		NodeID:             input.NodeID,
		DefinitionID:       input.WorkID,
		ExpectedRevision:   int64Ptr(input.ExpectedRevision),
		DefinitionRevision: int64Ptr(projection.V2CurrentRevision),
	}

	result := &SetNodeSkillResult{}
	if state.RequestFound {
		if loader, ok := c.store.(interface {
			LoadRequestEvent(string, string) (WorkEvent, error)
		}); ok {
			persisted, loadErr := loader.LoadRequestEvent(input.WorkID, event.RequestID)
			if loadErr != nil {
				return nil, loadErr
			}
			var p NodeSkillBoundPayload
			if err := json.Unmarshal(persisted.Payload, &p); err != nil {
				return nil, fmt.Errorf("%w: decode SetNodeSkill event: %v", ErrWorkNeedsRepair, err)
			}
			if p.SkillName != input.SkillName || p.NodeID != input.NodeID {
				return nil, fmt.Errorf("%w: SetNodeSkill request %q intent changed", ErrWorkRequestIDConflict, input.RequestID)
			}
		} else {
			if _, err := c.store.CommitEvent(input.WorkID, event); err != nil {
				var conflict *ErrWorkEventConflict
				if errors.As(err, &conflict) && conflict.Kind == WorkEventRequestConflict {
					return nil, fmt.Errorf("%w: %w", ErrWorkRequestIDConflict, err)
				}
				return nil, err
			}
		}
		result.Duplicate = true
		result.Revision = state.RequestRevision
		result.Committed = true
		return result, nil
	}
	if err := c.validateNodeSkillMutation(projection, input.NodeID); err != nil {
		return nil, err
	}

	if state.Revision != input.ExpectedRevision {
		return nil, revisionConflict(input.WorkID, input.ExpectedRevision, state.Revision)
	}

	revision, err := c.store.CommitEvent(input.WorkID, event)
	if err != nil {
		return nil, err
	}
	result.Revision = revision
	result.Committed = true
	return result, nil
}

// ClearNodeSkill removes a Skill binding from a Work node. idempotent:
// clearing an already-unbound node is a success (no-op).
func (c *V2Coordinator) ClearNodeSkill(
	ctx context.Context,
	input ClearNodeSkillRequest,
) (*ClearNodeSkillResult, error) {
	if c == nil || c.store == nil {
		return nil, errors.New("work: V2 coordinator is not configured")
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.NodeID == "" || input.RequestID == "" {
		return nil, errors.New("work: ClearNodeSkill: workID/nodeID/requestID are required")
	}
	if input.ExpectedRevision <= 0 {
		return nil, errors.New("work: ClearNodeSkill: expectedRevision must be positive")
	}

	projection, state, err := c.store.LoadState(input.WorkID, input.RequestID+"/clear-node-skill")
	if err != nil {
		return nil, err
	}
	if err := CheckSchemaVersionV2("Work", projection.SchemaVersion); err != nil {
		return nil, err
	}
	if projection.ArchiveState != ArchiveActive {
		return nil, fmt.Errorf("work: ClearNodeSkill is not allowed while ArchiveState=%s", projection.ArchiveState)
	}

	payload, err := json.Marshal(NodeSkillClearedPayload{
		WorkID: input.WorkID,
		NodeID: input.NodeID,
	})
	if err != nil {
		return nil, fmt.Errorf("work: ClearNodeSkill: encode payload: %w", err)
	}
	event := newServiceEventV2(
		input.WorkID,
		input.RequestID+"/clear-node-skill",
		EventNodeSkillCleared,
		payload,
		time.Now().UTC(),
	)
	event.Object = ObjectContext{
		Kind:               ObjectNode,
		ID:                 input.NodeID,
		WorkID:             input.WorkID,
		NodeID:             input.NodeID,
		DefinitionID:       input.WorkID,
		ExpectedRevision:   int64Ptr(input.ExpectedRevision),
		DefinitionRevision: int64Ptr(projection.V2CurrentRevision),
	}

	result := &ClearNodeSkillResult{}
	if state.RequestFound {
		if loader, ok := c.store.(interface {
			LoadRequestEvent(string, string) (WorkEvent, error)
		}); ok {
			persisted, loadErr := loader.LoadRequestEvent(input.WorkID, event.RequestID)
			if loadErr != nil {
				return nil, loadErr
			}
			if persisted.Type != EventNodeSkillCleared {
				return nil, fmt.Errorf("%w: ClearNodeSkill request %q operation changed", ErrWorkRequestIDConflict, input.RequestID)
			}
			var p NodeSkillClearedPayload
			if err := json.Unmarshal(persisted.Payload, &p); err != nil {
				return nil, fmt.Errorf("%w: decode ClearNodeSkill event: %v", ErrWorkNeedsRepair, err)
			}
			if p.NodeID != input.NodeID {
				return nil, fmt.Errorf("%w: ClearNodeSkill request %q intent changed", ErrWorkRequestIDConflict, input.RequestID)
			}
		}
		result.Duplicate = true
		result.Revision = state.RequestRevision
		result.Committed = true
		return result, nil
	}
	if err := c.validateNodeSkillMutation(projection, input.NodeID); err != nil {
		return nil, err
	}

	if state.Revision != input.ExpectedRevision {
		return nil, revisionConflict(input.WorkID, input.ExpectedRevision, state.Revision)
	}

	revision, err := c.store.CommitEvent(input.WorkID, event)
	if err != nil {
		return nil, err
	}
	result.Revision = revision
	result.Committed = true
	return result, nil
}

func (c *V2Coordinator) validateNodeSkillMutation(projection *Work, nodeID string) error {
	if projection == nil || projection.V2CurrentRevision <= 0 {
		return errors.New("work: node Skill binding requires an active V2 definition")
	}
	definition, err := c.loadDefinition(projection.ID, projection.V2CurrentRevision)
	if err != nil {
		return err
	}
	if findNodeDef(definition.Nodes, nodeID) == nil {
		return fmt.Errorf("work: node %q does not exist in active definition", nodeID)
	}
	for _, runtime := range projection.V2TaskRuntimes {
		if runtime == nil || runtime.NodeID != nodeID || runtime.DefinitionRev != projection.V2CurrentRevision {
			continue
		}
		switch runtime.State {
		case TaskRunning, TaskWaitingInput, TaskWaitingApproval:
			return fmt.Errorf("work: node %q Skill cannot change while task state is %s", nodeID, runtime.State)
		}
	}
	return nil
}

func isActiveRetryRuntime(projection *Work, runtime *V2TaskRuntime) bool {
	if projection == nil || runtime == nil || runtime.DefinitionRev != projection.V2CurrentRevision {
		return false
	}
	activeRun := activeDefinitionRunID(projection, projection.Definition.Digest)
	return activeRun == "" || runtime.RunID == activeRun
}

func (c *V2Coordinator) ScheduleRun(
	ctx context.Context,
	workID, runID string,
	changedNodeIDs []string,
) (V2ScheduleResult, error) {
	if c == nil || !c.enabled() {
		return V2ScheduleResult{}, errors.New("work: V2 scheduler is not configured")
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return V2ScheduleResult{}, err
	}
	definition, err := c.loadDefinition(workID, projection.V2CurrentRevision)
	if err != nil {
		return V2ScheduleResult{}, err
	}
	if err := c.commitV2RunState(ctx, workID, runID, definition, RunRunning); err != nil {
		return V2ScheduleResult{}, err
	}
	projection, err = c.store.LoadProjection(workID)
	if err != nil {
		return V2ScheduleResult{}, err
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	prepared := false
	for _, node := range definition.Nodes {
		runtime := runtimes[node.ID]
		if runtime == nil ||
			(runtime.State != TaskFailedRetryable && runtime.State != TaskInvalidated) ||
			len(node.ProducesSlotIDs) == 0 {
			continue
		}
		if err := c.prepareV2NodeArtifacts(
			ctx,
			workID,
			definition.Revision,
			node.ID,
			fmt.Sprintf("%s/v2/schedule/%s/%d", runID, runtime.TaskID, runtime.Revision),
		); err != nil {
			return V2ScheduleResult{}, err
		}
		prepared = true
	}
	if prepared {
		projection, err = c.store.LoadProjection(workID)
		if err != nil {
			return V2ScheduleResult{}, err
		}
	}
	if len(changedNodeIDs) == 0 {
		changedNodeIDs = make([]string, 0, len(definition.Nodes))
		for _, node := range definition.Nodes {
			changedNodeIDs = append(changedNodeIDs, node.ID)
		}
	}
	authority := c.runtimeAuthority(workID)
	result, scheduleErr := c.schedulerSnapshot().ScheduleAffected(
		ctx,
		workID,
		runID,
		definition.Nodes,
		projection.V2TaskRuntimes,
		definition.Revision,
		projection.V2Inputs,
		definition.InputSpecs,
		definition.ArtifactSlots,
		changedNodeIDs,
		definition.Goal,
		authority,
	)
	inputErr := c.materializeV2WaitingInputs(ctx, workID, runID, definition)
	_, artifactErr := c.reconcileV2Artifacts(ctx, workID, runID, definition)
	settleErr := c.settleV2Run(ctx, workID, runID, definition)
	err = errors.Join(scheduleErr, authority.ObserverError(), inputErr, artifactErr, settleErr)
	if err != nil {
		result.Error = err
	}
	return result, err
}

func (c *V2Coordinator) ContinueDefinition(
	ctx context.Context,
	workID, runID, requestID string,
	definition *WorkDefinitionRevision,
	affectedNodeIDs []string,
) error {
	if c == nil || !c.enabled() {
		return nil
	}
	if definition == nil {
		return errors.New("work: V2 definition continuation requires definition")
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	affectedNodeIDs = definitionRunSeeds(
		definition,
		projection,
		runID,
		affectedNodeIDs,
	)
	if len(affectedNodeIDs) == 0 {
		return nil
	}
	_, err = c.ScheduleRun(ctx, workID, runID, affectedNodeIDs)
	if err != nil {
		return committedRecovery("v2-definition-wake", workID, requestID, 0, err)
	}
	return nil
}

// RecoverScheduling scans durable business/runtime projection state after
// restart. It resumes ready/input-released tasks and an active definition run
// that committed before its first runtime could be materialized.
func (c *V2Coordinator) RecoverScheduling(ctx context.Context, workID string) error {
	if c == nil || !c.enabled() {
		return errors.New("work: V2 scheduling recovery is not configured")
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	definition, err := c.loadDefinition(workID, projection.V2CurrentRevision)
	if err != nil {
		return err
	}
	activeRunID := activeDefinitionRunID(projection, definition.Digest)
	if recoverErr := c.recoverPatchArtifacts(ctx, workID, activeRunID, projection, definition); recoverErr != nil {
		return committedRecovery("v2-patch-artifacts-recovery", workID, "", 0, recoverErr)
	}
	projection, err = c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	repaired, repairErr := c.reconcileV2Artifacts(ctx, workID, activeRunID, definition)
	if repairErr != nil {
		return repairErr
	}
	if repaired {
		projection, err = c.store.LoadProjection(workID)
		if err != nil {
			return err
		}
	}
	inputByRun := make(map[string][]string)
	approvalByRun := make(map[string][]string)
	var recoverErr error
	for _, runtime := range projection.V2TaskRuntimes {
		if runtime == nil || (activeRunID != "" && runtime.RunID != activeRunID) {
			continue
		}
		switch runtime.State {
		case TaskWaitingInput, TaskReady:
			if !V2ReceiptRequired(runtime.SideEffectClass) {
				inputByRun[runtime.RunID] = append(inputByRun[runtime.RunID], runtime.NodeID)
			}
		case TaskFailedRetryable:
			if len(runtime.Attempts) >= maxV2AutomaticRecoveryAttempts {
				recoverErr = errors.Join(recoverErr, fmt.Errorf(
					"work: automatic recovery paused for task %s after %d attempts; retry it manually",
					runtime.TaskID,
					len(runtime.Attempts),
				))
				continue
			}
			if !V2ReceiptRequired(runtime.SideEffectClass) {
				inputByRun[runtime.RunID] = append(inputByRun[runtime.RunID], runtime.NodeID)
			}
		case TaskInvalidated:
			if !isMissingV2ArtifactRuntime(runtime) && !V2ReceiptRequired(runtime.SideEffectClass) {
				inputByRun[runtime.RunID] = append(inputByRun[runtime.RunID], runtime.NodeID)
			}
		case TaskRunning:
			if !V2ReceiptRequired(runtime.SideEffectClass) && hasSupersededRunningAttempt(runtime) {
				inputByRun[runtime.RunID] = append(inputByRun[runtime.RunID], runtime.NodeID)
			}
		case TaskWaitingApproval:
			if hasSubmittedApproval(projection.V2Inputs, definition.InputSpecs, runtime.TaskID) {
				approvalByRun[runtime.RunID] = append(approvalByRun[runtime.RunID], runtime.NodeID)
			}
		}
	}
	if !repaired && len(inputByRun) == 0 && len(approvalByRun) == 0 {
		if activeRunID != "" {
			for _, node := range definition.Nodes {
				if !V2ReceiptRequired(DeriveV2SideEffectClass(node.ToolHints)) {
					inputByRun[activeRunID] = append(inputByRun[activeRunID], node.ID)
				}
			}
		}
	}
	for runID, seeds := range inputByRun {
		if err := c.continueRunAt(
			ctx, workID, runID, seeds, V2WakeInput, definition.Revision,
		); err != nil {
			recoverErr = errors.Join(recoverErr, err)
		}
	}
	for runID, seeds := range approvalByRun {
		if err := c.continueRunAt(
			ctx, workID, runID, seeds, V2WakeApproval, definition.Revision,
		); err != nil {
			recoverErr = errors.Join(recoverErr, err)
		}
	}
	if activeRunID != "" {
		recoverErr = errors.Join(recoverErr, c.settleV2Run(ctx, workID, activeRunID, definition))
	}
	return recoverErr
}

// RecoverAllScheduling scans active persisted V2 Works. Planning-only and V1
// Works are intentionally skipped; each active definition is recovered from
// authoritative FileWorkStore state.
func (c *V2Coordinator) RecoverAllScheduling(ctx context.Context) V2RecoveryReport {
	var report V2RecoveryReport
	if c == nil || c.store == nil {
		report.Failures = append(report.Failures, V2RecoveryFailure{Error: "work: V2 recovery store is not configured"})
		return report
	}
	active := ArchiveActive
	filter := WorkFilter{ArchiveState: &active, Limit: 500}
	for {
		if err := checkServiceContext(ctx); err != nil {
			report.Failures = append(report.Failures, V2RecoveryFailure{Error: err.Error()})
			return report
		}
		items, err := c.store.List(filter)
		if err != nil {
			report.Failures = append(report.Failures, V2RecoveryFailure{Error: err.Error()})
			return report
		}
		for _, item := range items {
			report.Scanned++
			projection, err := c.store.LoadProjection(item.ID)
			if err != nil {
				report.Failures = append(report.Failures, V2RecoveryFailure{WorkID: item.ID, Error: err.Error()})
				continue
			}
			if projection.SchemaVersion < SchemaVersionV2 || projection.V2CurrentRevision == 0 {
				continue
			}
			if err := c.RecoverScheduling(ctx, item.ID); err != nil {
				report.Failures = append(report.Failures, V2RecoveryFailure{WorkID: item.ID, Error: err.Error()})
				continue
			}
			report.Recovered++
		}
		if len(items) < filter.Limit {
			return report
		}
		filter.Cursor = items[len(items)-1].ID
	}
}

func (c *V2Coordinator) continueRun(
	ctx context.Context,
	workID, runID string,
	changedIDs []string,
	cause V2WakeCause,
) error {
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	return c.continueRunAt(ctx, workID, runID, changedIDs, cause, projection.V2CurrentRevision)
}

func (c *V2Coordinator) continueRunAt(
	ctx context.Context,
	workID, runID string,
	changedIDs []string,
	cause V2WakeCause,
	definitionRev int64,
) error {
	if strings.TrimSpace(runID) == "" || len(changedIDs) == 0 {
		return nil
	}
	scheduler := c.schedulerSnapshot()
	if scheduler == nil {
		return errors.New("work: V2 scheduler is not configured")
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	definition, err := c.loadDefinition(workID, definitionRev)
	if err != nil {
		return err
	}
	if err := c.commitV2RunState(ctx, workID, runID, definition, RunRunning); err != nil {
		return err
	}
	projection, err = c.store.LoadProjection(workID)
	if err != nil {
		return err
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	prepared := false
	for _, node := range definition.Nodes {
		runtime := runtimes[node.ID]
		if runtime == nil ||
			(runtime.State != TaskFailedRetryable && runtime.State != TaskInvalidated) ||
			len(node.ProducesSlotIDs) == 0 {
			continue
		}
		if err := c.prepareV2NodeArtifacts(
			ctx,
			workID,
			definition.Revision,
			node.ID,
			fmt.Sprintf("%s/v2/wake/%s/%d", runID, runtime.TaskID, runtime.Revision),
		); err != nil {
			return err
		}
		prepared = true
	}
	if prepared {
		projection, err = c.store.LoadProjection(workID)
		if err != nil {
			return err
		}
	}
	authority := c.runtimeAuthority(workID)
	_, scheduleErr := scheduler.WakeAndScheduleAffected(
		ctx,
		workID,
		runID,
		definition.Nodes,
		definition.Revision,
		projection.V2Inputs,
		definition.InputSpecs,
		definition.ArtifactSlots,
		changedIDs,
		cause,
		definition.Goal,
		authority,
	)
	inputErr := c.materializeV2WaitingInputs(ctx, workID, runID, definition)
	_, artifactErr := c.reconcileV2Artifacts(ctx, workID, runID, definition)
	settleErr := c.settleV2Run(ctx, workID, runID, definition)
	return errors.Join(scheduleErr, authority.ObserverError(), inputErr, artifactErr, settleErr)
}

func (c *V2Coordinator) schedulerSnapshot() *V2Scheduler {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.scheduler
}

func (c *V2Coordinator) runtimeAuthority(workID string) *workStoreV2Authority {
	c.mu.RLock()
	observer := c.commitObserver
	c.mu.RUnlock()
	return &workStoreV2Authority{
		store: c.store, workID: workID, observer: observer,
	}
}

func (c *V2Coordinator) loadDefinition(workID string, revision int64) (*WorkDefinitionRevision, error) {
	c.mu.RLock()
	defs := c.defs
	c.mu.RUnlock()
	if defs == nil {
		return nil, errors.New("work: V2 definition store is not configured")
	}
	return defs.LoadRevision(workID, revision)
}

func (c *V2Coordinator) inputKind(workID string, input *WorkInput) InputKind {
	if input == nil {
		return ""
	}
	projection, err := c.store.LoadProjection(workID)
	if err != nil {
		return ""
	}
	definition, err := c.loadDefinition(workID, projection.V2CurrentRevision)
	if err != nil {
		return ""
	}
	for _, spec := range definition.InputSpecs {
		if spec.ID == input.SpecID {
			return spec.Kind
		}
	}
	return ""
}

type workStoreV2Authority struct {
	store    WorkStore
	workID   string
	observer v2CommitObserver

	observerMu  sync.Mutex
	observerErr error
}

func (a *workStoreV2Authority) CommitV2Event(event WorkEvent) (int64, error) {
	if a == nil || a.store == nil || event.WorkID != a.workID {
		return 0, errors.New("work: V2 authority work mismatch")
	}
	revision, err := a.store.CommitEvent(a.workID, event)
	if err != nil || a.observer == nil || revision <= 0 {
		return revision, err
	}
	if observeErr := a.observer(a.workID, revision-1, event.RequestID); observeErr != nil {
		a.observerMu.Lock()
		a.observerErr = errors.Join(a.observerErr, observeErr)
		a.observerMu.Unlock()
	}
	return revision, nil
}

func (a *workStoreV2Authority) ObserverError() error {
	if a == nil {
		return nil
	}
	a.observerMu.Lock()
	defer a.observerMu.Unlock()
	return a.observerErr
}

func (a *workStoreV2Authority) LoadV2Projection() (*Work, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("work: V2 authority is not configured")
	}
	return a.store.LoadProjection(a.workID)
}

func (a *workStoreV2Authority) CommitV2Artifact(ctx context.Context, input TaskArtifactCommitInput) (*ArtifactSlotResult, error) {
	if a == nil || a.store == nil || input.WorkID != a.workID {
		return nil, errors.New("work: V2 artifact authority work mismatch")
	}
	return commitArtifactWithStore(ctx, a.store, input)
}

func (a *workStoreV2Authority) ReadV2ArtifactBlob(ctx context.Context, digest string) ([]byte, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("work: V2 artifact reader is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	blobs, ok := a.store.(BlobStore)
	if !ok {
		return nil, errors.New("work: V2 store cannot read artifact blobs")
	}
	return blobs.Get(a.workID, digest)
}

func runIDForAffectedTasks(work *Work, taskIDs []string) string {
	if work == nil {
		return ""
	}
	for _, taskID := range taskIDs {
		if runtime := work.V2TaskRuntimes[taskID]; runtime != nil {
			return runtime.RunID
		}
	}
	return ""
}

func hasSubmittedApproval(inputs []WorkInput, specs []InputSpec, taskID string) bool {
	approvalSpecs := make(map[string]InputSpec)
	for _, spec := range specs {
		if spec.Kind == InputApproval {
			approvalSpecs[spec.ID] = spec
		}
	}
	for _, input := range inputs {
		spec, ok := approvalSpecs[input.SpecID]
		if input.TaskID == taskID && ok && inputSatisfiesSpec(input, spec) {
			return true
		}
	}
	return false
}

func activeDefinitionRunID(work *Work, digest string) string {
	if work == nil {
		return ""
	}
	for i := len(work.Runs) - 1; i >= 0; i-- {
		if work.Runs[i].DefinitionDigest == digest {
			return work.Runs[i].ID
		}
	}
	return ""
}

func hasSupersededRunningAttempt(runtime *V2TaskRuntime) bool {
	if runtime == nil {
		return false
	}
	index := lastRunningAttempt(runtime)
	if index < 0 {
		return false
	}
	return ValidateStaleCompletion(&runtime.Attempts[index], DefTokenSet{
		DefinitionRev:    runtime.DefinitionRev,
		InputDigest:      runtime.InputDigest,
		DependencyDigest: runtime.DependencyDigest,
		ExecutionToken:   runtime.ExecutionToken,
	})
}

func definitionRunSeeds(
	definition *WorkDefinitionRevision,
	projection *Work,
	runID string,
	changedNodeIDs []string,
) []string {
	if definition == nil || projection == nil || strings.TrimSpace(runID) == "" {
		return append([]string(nil), changedNodeIDs...)
	}
	seen := make(map[string]bool, len(changedNodeIDs)+len(definition.Nodes))
	for _, nodeID := range changedNodeIDs {
		if nodeID = strings.TrimSpace(nodeID); nodeID != "" {
			seen[nodeID] = true
		}
	}
	runtimes := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)
	for _, node := range definition.Nodes {
		if runtimes[node.ID] == nil {
			seen[node.ID] = true
		}
	}
	seeds := make([]string, 0, len(seen))
	for nodeID := range seen {
		seeds = append(seeds, nodeID)
	}
	sort.Strings(seeds)
	return seeds
}
