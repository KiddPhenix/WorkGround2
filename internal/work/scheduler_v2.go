package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"workground2/internal/artifact"
	"workground2/internal/nilutil"
)

// V2Scheduler advances a V2 definition through its explicit DAG. Ready nodes
// execute concurrently, while a scheduler instance admits at most one
// execution for each stable task ID.
type V2Scheduler struct {
	executor TaskExecutor
	clock    Clock
}

var v2FlightRegistry = struct {
	sync.Mutex
	tasks map[string]struct{}
}{tasks: make(map[string]struct{})}

// NewV2Scheduler creates a V2 scheduler over the narrow task execution port.
func NewV2Scheduler(executor TaskExecutor) *V2Scheduler {
	return &V2Scheduler{
		executor: executor,
		clock:    RealClock{},
	}
}

// V2ScheduleResult reports one scheduling invocation.
type V2ScheduleResult struct {
	Ready       []string
	Executed    []string
	Waiting     []string
	GlobalBlock string
	Error       error
}

// V2RuntimeAuthority is the scheduler's authoritative persistence boundary.
// Completion always reloads this projection before accepting a result.
type V2RuntimeAuthority interface {
	CommitV2Event(WorkEvent) (int64, error)
	LoadV2Projection() (*Work, error)
}

// V2ArtifactAuthority is the production artifact persistence capability.
// Keeping it separate preserves small in-memory scheduler fakes while the
// FileWorkStore path always provides durable slot receipts.
type V2ArtifactAuthority interface {
	CommitV2Artifact(context.Context, TaskArtifactCommitInput) (*ArtifactSlotResult, error)
}

type TaskArtifactCommitInput struct {
	WorkID        string
	DefinitionRev int64
	TaskID        string
	AttemptID     string
	InputDigest   string
	Output        TaskArtifactOutput
}

// FileV2RuntimeAuthority binds a FileWorkStore to one Work.
type FileV2RuntimeAuthority struct {
	store  *FileWorkStore
	workID string
}

// NewFileV2RuntimeAuthority creates the production scheduler persistence port.
func NewFileV2RuntimeAuthority(store *FileWorkStore, workID string) (*FileV2RuntimeAuthority, error) {
	if store == nil {
		return nil, errors.New("work: V2 runtime authority requires FileWorkStore")
	}
	workID = strings.TrimSpace(workID)
	if workID == "" {
		return nil, errors.New("work: V2 runtime authority requires workID")
	}
	return &FileV2RuntimeAuthority{store: store, workID: workID}, nil
}

func (a *FileV2RuntimeAuthority) CommitV2Event(event WorkEvent) (int64, error) {
	if a == nil || a.store == nil || event.WorkID != a.workID {
		return 0, errors.New("work: V2 runtime authority work mismatch")
	}
	return a.store.CommitEvent(a.workID, event)
}

func (a *FileV2RuntimeAuthority) LoadV2Projection() (*Work, error) {
	if a == nil || a.store == nil {
		return nil, errors.New("work: V2 runtime authority is not configured")
	}
	return a.store.LoadProjection(a.workID)
}

func (a *FileV2RuntimeAuthority) CommitV2Artifact(ctx context.Context, input TaskArtifactCommitInput) (*ArtifactSlotResult, error) {
	if a == nil || a.store == nil || input.WorkID != a.workID {
		return nil, errors.New("work: V2 artifact authority work mismatch")
	}
	return commitArtifactWithStore(ctx, a.store, input)
}

func commitArtifactWithStore(ctx context.Context, store WorkStore, input TaskArtifactCommitInput) (*ArtifactSlotResult, error) {
	requestID := fmt.Sprintf("%s/artifact/%s", input.AttemptID, input.Output.SlotID)
	for tries := 0; tries < 4; tries++ {
		projection, state, err := store.LoadState(input.WorkID, "")
		if err != nil {
			return nil, err
		}
		slot, _ := FindArtifactSlotRevision(projection, input.DefinitionRev, input.Output.SlotID)
		if slot == nil {
			return nil, fmt.Errorf("work: executor output targets undeclared slot %q", input.Output.SlotID)
		}
		result, err := (&Service{store: store}).UpdateArtifactSlot(ctx, UpdateArtifactSlotInput{
			WorkID:           input.WorkID,
			SlotID:           input.Output.SlotID,
			RequestID:        requestID,
			State:            SlotReady,
			Refs:             input.Output.Refs,
			UpstreamDigest:   input.InputDigest,
			Summary:          input.Output.Summary,
			Revision:         slot.Revision + 1,
			ExpectedRevision: state.Revision,
			DefinitionRev:    input.DefinitionRev,
		})
		if err == nil {
			return result, nil
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(err, &conflict) || conflict.Kind != WorkEventRevisionConflict {
			return result, err
		}
	}
	return nil, errors.New("work: V2 artifact commit exceeded conflict retries")
}

// Schedule evaluates the complete DAG. A failed_retryable node is attempted at
// most once per invocation; callers explicitly retry by invoking Schedule
// again.
func (s *V2Scheduler) Schedule(
	ctx context.Context,
	workID, runID string,
	nodes []NodeDef,
	runtimes map[string]*V2TaskRuntime,
	defRev int64,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	authority V2RuntimeAuthority,
) (V2ScheduleResult, error) {
	return s.schedule(ctx, workID, runID, nodes, runtimes, defRev, inputs, specs, slotDefs, nil, authority)
}

// ScheduleAffected reevaluates only changed seed nodes and their descendants.
// It is the entry point used after Input, Approval, Patch, or Definition
// changes. An explicit global gate retains global scope.
func (s *V2Scheduler) ScheduleAffected(
	ctx context.Context,
	workID, runID string,
	nodes []NodeDef,
	runtimes map[string]*V2TaskRuntime,
	defRev int64,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	changedNodeIDs []string,
	authority V2RuntimeAuthority,
) (V2ScheduleResult, error) {
	return s.schedule(ctx, workID, runID, nodes, runtimes, defRev, inputs, specs, slotDefs, changedNodeIDs, authority)
}

// V2WakeCause identifies the authoritative event that may release a waiting
// task.
type V2WakeCause string

const (
	V2WakeInput      V2WakeCause = "input"
	V2WakeApproval   V2WakeCause = "approval"
	V2WakePatch      V2WakeCause = "patch"
	V2WakeDefinition V2WakeCause = "definition"
)

const v2ExecutionStageID = "v2-dag"

// WakeAndScheduleAffected is the production wake path after Input, Approval,
// Patch, or Definition changes. It touches only the changed seeds and their
// descendants, persists waiting→ready transitions, reloads the projection, and
// then schedules that affected subgraph.
func (s *V2Scheduler) WakeAndScheduleAffected(
	ctx context.Context,
	workID, runID string,
	nodes []NodeDef,
	defRev int64,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	changedIDs []string,
	cause V2WakeCause,
	authority V2RuntimeAuthority,
) (V2ScheduleResult, error) {
	if authority == nil {
		return V2ScheduleResult{}, errors.New("work: V2 wake requires runtime authority")
	}
	switch cause {
	case V2WakeInput, V2WakeApproval, V2WakePatch, V2WakeDefinition:
	default:
		return V2ScheduleResult{}, fmt.Errorf("work: invalid V2 wake cause %q", cause)
	}
	projection, err := authority.LoadV2Projection()
	if err != nil {
		return V2ScheduleResult{}, fmt.Errorf("work: load V2 wake projection: %w", err)
	}
	seeds := resolveChangedNodeIDs(changedIDs, projection.V2TaskRuntimes)
	affected := AffectedNodes(nodes, seeds)
	seedSet := make(map[string]bool, len(seeds))
	for _, nodeID := range seeds {
		seedSet[nodeID] = true
	}
	for _, nodeID := range affected {
		if err := s.wakeRuntime(
			nodeID,
			runID,
			nodes,
			inputs,
			specs,
			cause,
			seedSet[nodeID],
			authority,
		); err != nil {
			return V2ScheduleResult{}, err
		}
	}
	refreshed, err := authority.LoadV2Projection()
	if err != nil {
		return V2ScheduleResult{}, fmt.Errorf("work: reload V2 wake projection: %w", err)
	}
	return s.ScheduleAffected(
		ctx,
		workID,
		runID,
		nodes,
		refreshed.V2TaskRuntimes,
		defRev,
		inputs,
		specs,
		slotDefs,
		seeds,
		authority,
	)
}

func (s *V2Scheduler) schedule(
	ctx context.Context,
	workID, runID string,
	nodes []NodeDef,
	runtimes map[string]*V2TaskRuntime,
	defRev int64,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	changedNodeIDs []string,
	authority V2RuntimeAuthority,
) (V2ScheduleResult, error) {
	if ctx == nil {
		return V2ScheduleResult{}, errors.New("work: V2Scheduler: context is required")
	}
	if s == nil || nilutil.IsNil(s.executor) {
		return V2ScheduleResult{}, errors.New("work: V2Scheduler: TaskExecutor is not configured")
	}
	if authority == nil {
		return V2ScheduleResult{}, errors.New("work: V2Scheduler: runtime authority is required")
	}

	if changedNodeIDs != nil {
		changedNodeIDs = resolveChangedNodeIDs(changedNodeIDs, runtimes)
	}
	rtMap := normalizeV2RuntimesForRun(runtimes, runID)
	var emitMu sync.Mutex
	lockedEmit := func(event WorkEvent) (int64, error) {
		emitMu.Lock()
		defer emitMu.Unlock()
		return authority.CommitV2Event(event)
	}
	if err := s.recoverInterrupted(rtMap, lockedEmit); err != nil {
		return V2ScheduleResult{Error: err}, err
	}

	var result V2ScheduleResult
	readySeen := make(map[string]bool)
	attempted := make(map[string]bool)
	fullAfterGlobal := changedNodeIDs != nil &&
		containsCompletedGlobalGate(nodes, rtMap, changedNodeIDs)
	for {
		var ready ReadySetResult
		if changedNodeIDs == nil || fullAfterGlobal {
			ready = EvaluateReadySet(nodes, rtMap)
		} else {
			ready = EvaluateAffectedReadySet(nodes, rtMap, changedNodeIDs)
		}
		if ready.HasGlobalBlock {
			// Once this invocation observes a global gate, its release must
			// rescan the complete DAG. Unrelated nodes were blocked by the gate
			// even though they are not descendants of the gate node.
			fullAfterGlobal = true
		}
		result.Waiting = ready.Waiting
		result.GlobalBlock = ready.GlobalGate

		batch := make([]string, 0, len(ready.Ready))
		for _, nodeID := range ready.Ready {
			if !readySeen[nodeID] {
				result.Ready = append(result.Ready, nodeID)
				readySeen[nodeID] = true
			}
			if !attempted[nodeID] {
				batch = append(batch, nodeID)
				attempted[nodeID] = true
			}
		}
		if len(batch) == 0 {
			break
		}

		type nodeResult struct {
			nodeID   string
			runtime  *V2TaskRuntime
			executed bool
			err      error
		}
		results := make(chan nodeResult, len(batch))
		var wg sync.WaitGroup
		for _, nodeID := range batch {
			node := findNodeDef(nodes, nodeID)
			if node == nil {
				continue
			}
			local := cloneV2RuntimeMap(rtMap)
			wg.Add(1)
			go func(nodeID string, node NodeDef) {
				defer wg.Done()
				executed, err := s.executeNode(
					ctx, workID, runID, &node, local, defRev, inputs, specs, slotDefs,
					lockedEmit, authority.LoadV2Projection, authority,
				)
				results <- nodeResult{
					nodeID:   nodeID,
					runtime:  local[nodeID],
					executed: executed,
					err:      err,
				}
			}(nodeID, *node)
		}
		wg.Wait()
		close(results)

		var batchErr error
		for item := range results {
			if item.runtime != nil {
				rtMap[item.nodeID] = item.runtime
			}
			if item.executed {
				result.Executed = append(result.Executed, item.nodeID)
			}
			if item.err != nil {
				batchErr = errors.Join(batchErr, item.err)
			}
		}
		sort.Strings(result.Executed)
		if batchErr != nil {
			result.Error = batchErr
			return result, batchErr
		}
	}

	sort.Strings(result.Ready)
	return result, nil
}

func (s *V2Scheduler) executeNode(
	ctx context.Context,
	workID, runID string,
	node *NodeDef,
	rtMap map[string]*V2TaskRuntime,
	defRev int64,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	emit V2EventEmitter,
	load func() (*Work, error),
	authority V2RuntimeAuthority,
) (bool, error) {
	taskID, err := DeriveTaskID(runID, node.ID)
	if err != nil {
		return false, err
	}
	flightKey := workID + "\x00" + taskID
	if !s.acquire(flightKey) {
		return false, nil
	}
	defer s.release(flightKey)

	now := s.clock.Now().UTC()
	rt := rtMap[node.ID]
	if rt == nil {
		rt = V2NewTaskRuntime(workID, runID, node.ID, defRev, DeriveV2SideEffectClass(node.ToolHints), now)
		if err := emitRuntimeCreated(emit, rt, now); err != nil {
			return false, err
		}
		rtMap[node.ID] = rt
	}

	inputDigest := ComputeInputDigest(inputs, workID, runID, taskID, node.InputSpecIDs)
	dependencyDigest := ComputeDependencyDigest(rtMap, node.DependsOn)
	token := GenerateExecutionToken(taskID, defRev, inputDigest, dependencyDigest)
	setContext := func(next *V2TaskRuntime) {
		next.DefinitionRev = defRev
		next.InputDigest = inputDigest
		next.DependencyDigest = dependencyDigest
		next.ExecutionToken = token
		next.SideEffectClass = UpgradeV2SideEffectClass(
			next.SideEffectClass,
			DeriveV2SideEffectClass(node.ToolHints),
		)
	}

	if ok, missing := HasAllRequiredInputs(
		inputs, specs, workID, runID, taskID, node.InputSpecIDs,
	); !ok {
		return true, updateRuntime(emit, rt, TaskWaitingInput, nil, now, func(next *V2TaskRuntime) {
			setContext(next)
			next.WaitingInputIDs = append([]string(nil), missing...)
			next.Error = ""
		})
	}
	if hasRunningV2Attempt(rt) {
		return false, nil
	}
	if rt.State != TaskReady {
		if err := updateRuntime(emit, rt, TaskReady, nil, now, func(next *V2TaskRuntime) {
			setContext(next)
			next.WaitingInputIDs = nil
			next.ApprovalToken = ""
			next.Error = ""
		}); err != nil {
			return false, err
		}
	}

	promptWork, loadErr := load()
	if loadErr != nil {
		return false, fmt.Errorf("work: load Work locale before V2 execution: %w", loadErr)
	}
	locale := ""
	if promptWork != nil {
		locale = promptWork.Locale
	}

	attempt := V2Attempt{
		ID:               V2RunAttemptID(taskID, len(rt.Attempts)),
		RequestID:        fmt.Sprintf("%s/run/v2/attempt/%s/%d", runID, taskID, len(rt.Attempts)),
		Index:            len(rt.Attempts),
		State:            TaskRunning,
		StartedAt:        now,
		DefinitionRev:    defRev,
		InputDigest:      inputDigest,
		DependencyDigest: dependencyDigest,
		ExecutionToken:   token,
		SideEffectClass:  rt.SideEffectClass,
	}
	if err := updateRuntime(emit, rt, TaskRunning, &attempt, now, setContext); err != nil {
		return false, err
	}

	taskPrompt := v2NodePromptLocale(
		node, inputs, specs, slotDefs, workID, runID, taskID, locale,
	)
	var liveErr error
	reportLive := func(update TaskLiveUpdate) error {
		output := strings.TrimSpace(update.Output)
		sessionChanged := update.SessionRef != nil &&
			(rt.SessionRef == nil || *rt.SessionRef != *update.SessionRef)
		if !sessionChanged && (output == "" || output == rt.Progress) {
			return nil
		}
		err := updateRuntime(emit, rt, TaskRunning, nil, s.clock.Now().UTC(), func(next *V2TaskRuntime) {
			if sessionChanged {
				ref := *update.SessionRef
				next.SessionRef = &ref
			}
			if output != "" {
				next.Progress = output
			}
		})
		if err != nil {
			liveErr = errors.Join(liveErr, err)
		}
		return err
	}
	execResult, execErr := safeExecuteTask(s.executor, ctx, TaskExecuteInput{
		WorkID:           workID,
		RunID:            runID,
		StageID:          v2ExecutionStageID,
		TaskID:           taskID,
		AttemptID:        attempt.ID,
		AttemptIndex:     attempt.Index,
		RequestID:        attempt.RequestID,
		DefinitionDigest: fmt.Sprintf("%d:%s", defRev, token),
		SideEffectClass:  rt.SideEffectClass,
		Operation:        node.ID,
		ProducesSlotIDs:  append([]string(nil), node.ProducesSlotIDs...),
		SlotPreflights:   BuildSlotPreflights(slotDefs, node.ProducesSlotIDs, taskPrompt),
		Prompt:           taskPrompt,
		Live:             reportLive,
	})
	execErr = errors.Join(execErr, liveErr)
	finishedAt := s.clock.Now().UTC()
	finalAttempt := rt.Attempts[len(rt.Attempts)-1]
	finalAttempt.FinishedAt = &finishedAt
	finalAttempt.Error = ""
	finalAttempt.ResultRef = resultRefFromAttempt(execResult)
	switch {
	case execErr != nil:
		finalAttempt.State = TaskFailedRetryable
		finalAttempt.Error = execErr.Error()
	case execResult == nil:
		finalAttempt.State = TaskCompleted
	default:
		finalAttempt.Error = execResult.Error
		finalAttempt.Receipt = execResult.Receipt
		finalAttempt.SideEffectClass = UpgradeV2SideEffectClass(
			rt.SideEffectClass,
			execResult.SideEffectClass,
		)
		switch execResult.State {
		case "", RunCompleted:
			finalAttempt.State = TaskCompleted
		case RunFailed:
			finalAttempt.State = TaskFailedRetryable
		case RunCancelled:
			finalAttempt.State = TaskCanceled
		case RunNeedsConfirmation, RunRunning, RunPending:
			finalAttempt.State = TaskWaitingApproval
			if finalAttempt.Error == "" {
				finalAttempt.Error = "execution outcome is uncertain; human confirmation required"
			}
		default:
			finalAttempt.State = TaskFailedRetryable
			finalAttempt.Error = fmt.Sprintf("work: V2 executor returned invalid state %q", execResult.State)
		}
	}
	if V2ReceiptRequired(finalAttempt.SideEffectClass) &&
		(finalAttempt.Receipt == nil ||
			strings.TrimSpace(finalAttempt.Receipt.RequestID) != strings.TrimSpace(attempt.RequestID) ||
			strings.TrimSpace(finalAttempt.Receipt.Operation) != strings.TrimSpace(node.ID)) {
		finalAttempt.State = TaskWaitingApproval
		finalAttempt.Error = "external outcome has no matching request/operation receipt; human confirmation required"
	}

	var artifactOutputs []TaskArtifactOutput
	if finalAttempt.State == TaskCompleted {
		if reporter, ok := s.executor.(TaskArtifactReporter); ok {
			outputs, outputErr := reporter.TaskArtifacts(ctx, TaskExecuteInput{
				WorkID:           workID,
				RunID:            runID,
				StageID:          v2ExecutionStageID,
				TaskID:           taskID,
				AttemptID:        attempt.ID,
				AttemptIndex:     attempt.Index,
				RequestID:        attempt.RequestID,
				DefinitionDigest: fmt.Sprintf("%d:%s", defRev, token),
				SideEffectClass:  rt.SideEffectClass,
				Operation:        node.ID,
				ProducesSlotIDs:  append([]string(nil), node.ProducesSlotIDs...),
				Prompt:           taskPrompt,
			}, execResult)
			if outputErr != nil {
				finalAttempt.State = TaskFailedRetryable
				finalAttempt.Error = outputErr.Error()
			} else if err := validateTaskArtifactOutputs(node.ProducesSlotIDs, outputs); err != nil {
				finalAttempt.State = TaskFailedRetryable
				finalAttempt.Error = err.Error()
			} else if len(outputs) > 0 {
				artifactOutputs = outputs
			}
		} else if len(node.ProducesSlotIDs) > 0 {
			finalAttempt.State = TaskFailedRetryable
			finalAttempt.Error = "work: V2 executor cannot report declared artifact outputs"
		}
	}

	projection, loadErr := load()
	if loadErr != nil {
		return false, fmt.Errorf("work: reload authoritative V2 runtime after execution: %w", loadErr)
	}
	authoritative := projection.V2TaskRuntimes[taskID]
	if authoritative == nil {
		return false, fmt.Errorf("work: authoritative V2 runtime %q disappeared after execution", taskID)
	}
	current := DefTokenSet{
		DefinitionRev:    authoritative.DefinitionRev,
		InputDigest:      authoritative.InputDigest,
		DependencyDigest: authoritative.DependencyDigest,
		ExecutionToken:   authoritative.ExecutionToken,
	}
	if ValidateStaleCompletion(&finalAttempt, current) {
		finalAttempt.StaleResult = true
		if err := emitStaleResult(emit, authoritative, finalAttempt, current, finishedAt); err != nil {
			return false, err
		}
		return true, nil
	}

	if finalAttempt.State == TaskCompleted && len(artifactOutputs) > 0 {
		artifactAuthority, ok := authority.(V2ArtifactAuthority)
		if !ok {
			finalAttempt.State = TaskFailedRetryable
			finalAttempt.Error = "V2 runtime authority cannot persist artifact outputs"
		} else if err := commitTaskArtifacts(
			ctx, artifactAuthority, workID, defRev, taskID, attempt.ID, inputDigest,
			node.ProducesSlotIDs, artifactOutputs,
		); err != nil {
			finalAttempt.State = TaskFailedRetryable
			finalAttempt.Error = err.Error()
		}
	}

	return true, updateRuntime(emit, rt, finalAttempt.State, nil, finishedAt, func(next *V2TaskRuntime) {
		next.Attempts[len(next.Attempts)-1] = finalAttempt
		next.SideEffectClass = UpgradeV2SideEffectClass(next.SideEffectClass, finalAttempt.SideEffectClass)
		next.Error = finalAttempt.Error
		if finalAttempt.State == TaskWaitingApproval {
			next.ApprovalToken = approvalToken(finalAttempt)
		}
	})
}

func commitTaskArtifacts(
	ctx context.Context,
	authority V2ArtifactAuthority,
	workID string,
	defRev int64,
	taskID, attemptID, inputDigest string,
	declared []string,
	outputs []TaskArtifactOutput,
) error {
	if err := validateTaskArtifactOutputs(declared, outputs); err != nil {
		return err
	}
	allowed := make(map[string]bool, len(declared))
	for _, slotID := range declared {
		allowed[slotID] = true
	}
	seen := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		output.SlotID = strings.TrimSpace(output.SlotID)
		if !allowed[output.SlotID] {
			return fmt.Errorf("work: executor output targets undeclared slot %q", output.SlotID)
		}
		if seen[output.SlotID] {
			return fmt.Errorf("work: executor returned duplicate slot output %q", output.SlotID)
		}
		seen[output.SlotID] = true
		if len(output.Refs) == 0 {
			return fmt.Errorf("work: executor returned no artifact refs for slot %q", output.SlotID)
		}
		if _, err := authority.CommitV2Artifact(ctx, TaskArtifactCommitInput{
			WorkID:        workID,
			DefinitionRev: defRev,
			TaskID:        taskID,
			AttemptID:     attemptID,
			InputDigest:   inputDigest,
			Output:        output,
		}); err != nil {
			return fmt.Errorf("work: persist artifact slot %q: %w", output.SlotID, err)
		}
	}
	return nil
}

func validateTaskArtifactOutputs(declared []string, outputs []TaskArtifactOutput) error {
	allowed := make(map[string]bool, len(declared))
	for _, slotID := range declared {
		slotID = strings.TrimSpace(slotID)
		if slotID == "" {
			return errors.New("work: V2 node declares an empty artifact slot ID")
		}
		allowed[slotID] = true
	}
	seen := make(map[string]bool, len(outputs))
	for _, output := range outputs {
		slotID := strings.TrimSpace(output.SlotID)
		if !allowed[slotID] {
			return fmt.Errorf("work: executor output targets undeclared slot %q", slotID)
		}
		if seen[slotID] {
			return fmt.Errorf("work: executor returned duplicate slot output %q", slotID)
		}
		if len(output.Refs) == 0 {
			return fmt.Errorf("work: executor returned no artifact refs for slot %q", slotID)
		}
		seen[slotID] = true
	}
	var missing []string
	for slotID := range allowed {
		if !seen[slotID] {
			missing = append(missing, slotID)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("work: executor omitted declared artifact outputs: %s", strings.Join(missing, ", "))
	}
	return nil
}

func (s *V2Scheduler) wakeRuntime(
	nodeID string,
	runID string,
	nodes []NodeDef,
	inputs []WorkInput,
	specs []InputSpec,
	cause V2WakeCause,
	directSeed bool,
	authority V2RuntimeAuthority,
) error {
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		projection, err := authority.LoadV2Projection()
		if err != nil {
			return fmt.Errorf("work: load V2 wake runtime %q: %w", nodeID, err)
		}
		runtime := normalizeV2RuntimesForRun(projection.V2TaskRuntimes, runID)[nodeID]
		if runtime == nil {
			return nil
		}
		wake := false
		switch runtime.State {
		case TaskWaitingInput:
			node := findNodeDef(nodes, nodeID)
			if node != nil {
				wake, _ = HasAllRequiredInputs(
					inputs, specs, runtime.WorkID, runtime.RunID, runtime.TaskID, node.InputSpecIDs,
				)
			}
		case TaskWaitingApproval:
			// Approval is object-specific. A resolved upstream approval may
			// schedule descendants, but must not release their own gates.
			wake = cause == V2WakeApproval && directSeed
		default:
			// Duplicate/late wake after another scheduler already advanced the
			// runtime is successful and must not emit a second transition.
			return nil
		}
		if !wake {
			return nil
		}
		err = updateRuntime(
			authority.CommitV2Event,
			runtime,
			TaskReady,
			nil,
			s.clock.Now().UTC(),
			func(next *V2TaskRuntime) {
				next.WaitingInputIDs = nil
				next.ApprovalToken = ""
				next.Error = ""
			},
		)
		if err == nil {
			return nil
		}
		// Optimistic conflict from a concurrent wake is recoverable. Reloading
		// distinguishes a successfully advanced duplicate from a real failure.
		if attempt == maxAttempts-1 {
			return err
		}
	}
	return nil
}

func containsCompletedGlobalGate(
	nodes []NodeDef,
	runtimes map[string]*V2TaskRuntime,
	seeds []string,
) bool {
	seedSet := make(map[string]bool, len(seeds))
	for _, nodeID := range seeds {
		seedSet[nodeID] = true
	}
	for _, node := range nodes {
		if node.GlobalGate == "" || !seedSet[node.ID] {
			continue
		}
		if runtime := runtimes[node.ID]; runtime != nil && runtime.State == TaskCompleted {
			return true
		}
	}
	return false
}

func v2NodePrompt(
	node *NodeDef,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	workID, runID, taskID string,
) string {
	return v2NodePromptLocale(node, inputs, specs, slotDefs, workID, runID, taskID, "")
}

func v2NodePromptLocale(
	node *NodeDef,
	inputs []WorkInput,
	specs []InputSpec,
	slotDefs []ArtifactSlotDef,
	workID, runID, taskID, locale string,
) string {
	if node == nil {
		return "Execute the V2 work node."
	}
	title := strings.TrimSpace(node.Title)
	description := strings.TrimSpace(node.Description)
	var prompt string
	switch {
	case title != "" && description != "":
		prompt = title + "\n\n" + description
	case description != "":
		prompt = description
	case title != "":
		prompt = title
	case strings.TrimSpace(node.ID) != "":
		prompt = "Execute work node " + strings.TrimSpace(node.ID) + "."
	default:
		prompt = "Execute the V2 work node."
	}

	guidanceByKind, guidanceByCapability := buildSlotGuidanceMaps()
	slotByID := make(map[string]ArtifactSlotDef, len(slotDefs))
	for _, slot := range slotDefs {
		slotByID[slot.ID] = slot
	}
	autoCapabilities := make(map[string]bool)
	for _, slotID := range node.ProducesSlotIDs {
		slot, ok := slotByID[slotID]
		if !ok {
			continue
		}
		if guidance, ok := guidanceByKind[strings.ToLower(strings.TrimSpace(slot.Kind))]; ok {
			autoCapabilities[guidance.Capability] = true
		}
	}

	// Append explicit tool hints that are not already supplied by slot-driven
	// capability guidance.
	if hints := v2NodePromptToolHints(node.ToolHints, autoCapabilities, guidanceByCapability); hints != "" {
		prompt += hints
	}
	if directive := LocaleDirective(locale); directive != "" {
		prompt += "\n\n--- Work language ---\n" + directive
	}

	// Append submitted WorkInput values owned by this Work/Run/Task,
	// limited to the spec IDs this node declares.  Order follows
	// node.InputSpecIDs for determinism.
	if len(node.InputSpecIDs) > 0 {
		specMap := make(map[string]InputSpec, len(specs))
		for _, s := range specs {
			specMap[s.ID] = s
		}
		specSet := make(map[string]bool, len(node.InputSpecIDs))
		for _, id := range node.InputSpecIDs {
			specSet[id] = true
		}
		// Collect inputs keyed by SpecID; only submitted/accepted, only this scope.
		bySpec := make(map[string][]WorkInput, len(node.InputSpecIDs))
		for _, in := range inputs {
			if in.WorkID != workID || in.RunID != runID || in.TaskID != taskID {
				continue
			}
			if !specSet[in.SpecID] {
				continue
			}
			if in.State != InputSubmitted && in.State != InputAccepted {
				continue
			}
			bySpec[in.SpecID] = append(bySpec[in.SpecID], in)
		}

		var parts []string
		for _, specID := range node.InputSpecIDs {
			ins, ok := bySpec[specID]
			if !ok {
				continue
			}
			sort.Slice(ins, func(i, j int) bool { return ins[i].ID < ins[j].ID })
			spec, hasSpec := specMap[specID]
			for _, in := range ins {
				label := specID
				kind := ""
				if hasSpec {
					if strings.TrimSpace(spec.Label) != "" {
						label = spec.Label
					}
					kind = string(spec.Kind)
				}
				valueStr := string(in.Value)
				if !json.Valid(in.Value) {
					valueStr = fmt.Sprintf("%q", valueStr)
				}
				line := fmt.Sprintf("Input %q (spec: %s", label, specID)
				if kind != "" {
					line += fmt.Sprintf(", kind: %s", kind)
				}
				line += fmt.Sprintf("): %s", valueStr)
				parts = append(parts, line)
			}
		}
		if len(parts) > 0 {
			prompt += "\n\n--- Submitted inputs ---\n" + strings.Join(parts, "\n")
		}
	}

	if len(node.ProducesSlotIDs) == 0 {
		return prompt
	}

	var slotLines []string
	for _, sid := range node.ProducesSlotIDs {
		sd, ok := slotByID[sid]
		if !ok {
			slotLines = append(slotLines, fmt.Sprintf("- %s (no definition)", sid))
			continue
		}
		kind := sd.Kind
		if kind == "" {
			kind = "text"
		}
		line := fmt.Sprintf("- %s (%s)", sd.Title, kind)
		if sd.ExpectedCount > 1 {
			line += fmt.Sprintf(" ×%d", sd.ExpectedCount)
		}
		slotLines = append(slotLines, line)
	}

	// Determine which slots are structured (need tool-produced artifacts) vs text
	// (your final response is authoritative).
	var structuredLines, textLines []string
	for _, sid := range node.ProducesSlotIDs {
		sd, ok := slotByID[sid]
		if !ok {
			textLines = append(textLines, sid)
			continue
		}
		kind := sd.Kind
		if kind == "" {
			kind = "text"
		}
		if g, hasGuidance := guidanceByKind[strings.ToLower(strings.TrimSpace(kind))]; hasGuidance {
			structuredLines = append(structuredLines,
				fmt.Sprintf("Slot %q (%s): %s", sd.Title, sd.Kind, g.Guidance))
		} else if kind == "text" || kind == "document" || kind == "code" || kind == "xlsx" || kind == "markdown" {
			textLines = append(textLines, sd.Title)
		} else {
			// Unknown structured kind — treat as file artifact.
			structuredLines = append(structuredLines,
				fmt.Sprintf("Slot %q (%s): produce via appropriate tools; the artifact file will be collected from tool results.", sd.Title, sd.Kind))
		}
	}

	var parts []string
	parts = append(parts,
		"Your response will be saved into these Work artifact slots:")
	parts = append(parts, slotLines...)

	if len(structuredLines) > 0 {
		parts = append(parts, "", "Structured slots — must be produced by tools:",
			strings.Join(structuredLines, "\n"))
	}
	if len(textLines) > 0 {
		parts = append(parts, "",
			"Text slots — your final response text is the authoritative content for: "+
				strings.Join(textLines, ", ")+
				". Include the complete deliverable in your final response; do not reply with only a summary or a claim that a file was created.")
	}

	return prompt + "\n\n" + strings.Join(parts, "\n")
}

// buildSlotGuidanceMaps returns shared kind and capability indexes from
// artifact producers that implement CapabilityProducer.
func buildSlotGuidanceMaps() (map[string]artifact.SlotGuidance, map[string]string) {
	guidance := artifact.CollectSlotGuidance(artifact.DefaultProducers())
	byKind := make(map[string]artifact.SlotGuidance, len(guidance))
	byCapability := make(map[string]string, len(guidance))
	for _, g := range guidance {
		if _, exists := byKind[g.Kind]; !exists {
			byKind[g.Kind] = g
		}
		if _, exists := byCapability[g.Capability]; !exists {
			byCapability[g.Capability] = g.Guidance
		}
	}
	return byKind, byCapability
}

// BuildSlotPreflights returns preflight requests for slots whose Kind matches a
// registered CapabilityProducer. Slots without a capability producer or with
// Kind empty/"text"/"document"/"code"/"xlsx"/"markdown" are skipped — those are
// satisfied by the main model's final response or Shell file discovery.
//
// Preflights are emitted in slot-definition order, with per-slot indices for
// ExpectedCount > 1. The Prompt field is built from the slot title, kind, and
// the node's overall task prompt so the capability helper has full context.
func BuildSlotPreflights(slotDefs []ArtifactSlotDef, producesSlotIDs []string, taskPrompt string) []SlotPreflight {
	if len(slotDefs) == 0 || len(producesSlotIDs) == 0 {
		return nil
	}
	byKind, _ := buildSlotGuidanceMaps()
	if len(byKind) == 0 {
		return nil
	}
	produced := make(map[string]struct{}, len(producesSlotIDs))
	for _, slotID := range producesSlotIDs {
		produced[slotID] = struct{}{}
	}
	var out []SlotPreflight
	for _, sd := range slotDefs {
		if _, ok := produced[sd.ID]; !ok {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(sd.Kind))
		g, hasCap := byKind[kind]
		if !hasCap {
			continue
		}
		count := sd.ExpectedCount
		if count <= 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			prompt := buildPreflightPrompt(sd, i, count, taskPrompt)
			out = append(out, SlotPreflight{
				SlotID:     sd.ID,
				SlotIndex:  i,
				Capability: g.Capability,
				Prompt:     prompt,
			})
		}
	}
	return out
}

// buildPreflightPrompt constructs a self-contained generation prompt for one
// capability-produced slot index. When count > 1 it adds positional hints so
// the helper can differentiate (e.g. "image 2 of 4").
func buildPreflightPrompt(sd ArtifactSlotDef, index, count int, taskPrompt string) string {
	var b strings.Builder
	b.WriteString("Generate the artifact for slot ")
	b.WriteString(strconv.Quote(sd.Title))
	b.WriteString(" (")
	b.WriteString(sd.Kind)
	b.WriteString(")")
	if count > 1 {
		fmt.Fprintf(&b, " — item %d of %d", index+1, count)
	}
	b.WriteString(".\n\nContext from the task:\n")
	b.WriteString(taskPrompt)
	return b.String()
}

// v2NodePromptToolHints returns guidance text for a node's tool hints.
// web_search prefers search already available to the task model or registry,
// then falls back to the shared request_help capability router.
func v2NodePromptToolHints(
	hints []string,
	skipCapabilities map[string]bool,
	capabilityGuidance map[string]string,
) string {
	if len(hints) == 0 {
		return ""
	}
	var parts []string
	seen := make(map[string]bool, len(hints))
	for _, h := range hints {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		key := strings.ToLower(h)
		if seen[key] {
			continue
		}
		seen[key] = true
		if skipCapabilities[key] {
			continue
		}
		switch key {
		case "web_search":
			parts = append(parts, "Use native web search or an available web_search tool for current information, documentation, and public references. If this model cannot search directly, call request_help with capability web_search. Include source URLs in the result; if no search path is available, fail explicitly so the task can be retried.")
		default:
			if guidance := strings.TrimSpace(capabilityGuidance[key]); guidance != "" {
				parts = append(parts, guidance)
			} else {
				parts = append(parts, "Tool hint: "+h)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n--- Tool guidance ---\n" + strings.Join(parts, "\n")
}

// recoverInterrupted makes restart behavior explicit. Safe local work becomes
// retryable. External/destructive work without a conclusive receipt requires
// human takeover and is never replayed automatically.
func (s *V2Scheduler) recoverInterrupted(runtimes map[string]*V2TaskRuntime, emit V2EventEmitter) error {
	nodeIDs := make([]string, 0, len(runtimes))
	for nodeID := range runtimes {
		nodeIDs = append(nodeIDs, nodeID)
	}
	sort.Strings(nodeIDs)
	for _, nodeID := range nodeIDs {
		rt := runtimes[nodeID]
		if rt == nil || rt.State != TaskRunning || !hasRunningV2Attempt(rt) {
			continue
		}
		if v2TaskInFlight(rt.WorkID + "\x00" + rt.TaskID) {
			// Another scheduler instance in this process still owns the
			// execution. A projected running state alone is not crash evidence.
			continue
		}
		index := lastRunningAttempt(rt)
		attempt := rt.Attempts[index]
		now := s.clock.Now().UTC()
		target := TaskFailedRetryable
		message := "execution interrupted by restart; safe retry required"
		if V2ReceiptRequired(attempt.SideEffectClass) {
			target = TaskWaitingApproval
			message = "external outcome is uncertain after restart; human confirmation required"
		}
		if err := updateRuntime(emit, rt, target, nil, now, func(next *V2TaskRuntime) {
			next.Attempts[index].State = target
			next.Attempts[index].Error = message
			next.Attempts[index].FinishedAt = &now
			next.Error = message
			if target == TaskWaitingApproval {
				next.ApprovalToken = approvalToken(next.Attempts[index])
			}
		}); err != nil {
			return err
		}
	}
	return nil
}

func v2TaskInFlight(taskID string) bool {
	v2FlightRegistry.Lock()
	defer v2FlightRegistry.Unlock()
	_, active := v2FlightRegistry.tasks[taskID]
	return active
}

// V2EventEmitter persists one event and returns the resulting Work revision.
type V2EventEmitter func(WorkEvent) (int64, error)

func emitRuntimeCreated(emit V2EventEmitter, rt *V2TaskRuntime, now time.Time) error {
	next, event, err := newRuntimeCreatedEvent(rt, now)
	if err != nil {
		return err
	}
	_, err = emit(event)
	if err == nil {
		*rt = *next
	}
	return err
}

func newRuntimeCreatedEvent(rt *V2TaskRuntime, now time.Time) (*V2TaskRuntime, WorkEvent, error) {
	next := cloneV2Runtime(rt)
	next.Revision = 1
	next.UpdatedAt = now
	payload, err := json.Marshal(TaskRuntimeCreatedPayload{
		TaskID:           next.TaskID,
		WorkID:           next.WorkID,
		RunID:            next.RunID,
		NodeID:           next.NodeID,
		ExpectedRevision: 0,
		DefinitionRev:    next.DefinitionRev,
		SideEffectClass:  next.SideEffectClass,
		InputDigest:      next.InputDigest,
		DependencyDigest: next.DependencyDigest,
		ExecutionToken:   next.ExecutionToken,
		Runtime:          *next,
	})
	if err != nil {
		return nil, WorkEvent{}, fmt.Errorf("work: encode runtime_created: %w", err)
	}
	event := WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            fmt.Sprintf("rt-create-%s", next.TaskID),
		Type:          EventTaskRuntimeCreated,
		WorkID:        next.WorkID,
		RequestID:     fmt.Sprintf("%s/rt/create/%s", next.RunID, next.TaskID),
		Payload:       payload,
		Object: ObjectContext{
			Kind:               ObjectTask,
			WorkID:             next.WorkID,
			ID:                 next.TaskID,
			RunID:              next.RunID,
			TaskID:             next.TaskID,
			ExpectedRevision:   int64Ptr(0),
			DefinitionRevision: int64Ptr(next.DefinitionRev),
		},
	}
	return next, event, nil
}

func updateRuntime(
	emit V2EventEmitter,
	rt *V2TaskRuntime,
	state TaskStateV2,
	attempt *V2Attempt,
	now time.Time,
	mutate func(*V2TaskRuntime),
) error {
	if err := ValidateTaskV2Transition(rt.State, state); err != nil {
		return err
	}
	next := cloneV2Runtime(rt)
	next.State = state
	next.Revision = rt.Revision + 1
	next.UpdatedAt = now
	if attempt != nil {
		next.Attempts = append(next.Attempts, *attempt)
	}
	if mutate != nil {
		mutate(next)
	}
	payload, err := json.Marshal(TaskRuntimeUpdatedPayload{
		TaskID:           next.TaskID,
		WorkID:           next.WorkID,
		RunID:            next.RunID,
		ExpectedRevision: rt.Revision,
		State:            next.State,
		Runtime:          *next,
		Attempt:          attempt,
	})
	if err != nil {
		return fmt.Errorf("work: encode runtime_updated: %w", err)
	}
	_, err = emit(WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            fmt.Sprintf("rt-update-%s-%d", next.TaskID, next.Revision),
		Type:          EventTaskRuntimeUpdated,
		WorkID:        next.WorkID,
		RequestID:     fmt.Sprintf("%s/rt/update/%s/%d", next.RunID, next.TaskID, next.Revision),
		Payload:       payload,
		Object: ObjectContext{
			Kind:               ObjectTask,
			WorkID:             next.WorkID,
			ID:                 next.TaskID,
			RunID:              next.RunID,
			TaskID:             next.TaskID,
			ExpectedRevision:   int64Ptr(rt.Revision),
			DefinitionRevision: int64Ptr(next.DefinitionRev),
		},
	})
	if err == nil {
		*rt = *next
	}
	return err
}

// emitRuntimeUpdated remains the small test/helper seam used by existing V2
// store tests.
func emitRuntimeUpdated(emit V2EventEmitter, rt *V2TaskRuntime, state TaskStateV2, attempt *V2Attempt, now time.Time) error {
	return updateRuntime(emit, rt, state, attempt, now, nil)
}

func emitStaleResult(
	emit V2EventEmitter,
	rt *V2TaskRuntime,
	attempt V2Attempt,
	current DefTokenSet,
	now time.Time,
) error {
	payload, err := json.Marshal(TaskStaleResultPayload{
		TaskID:           rt.TaskID,
		WorkID:           rt.WorkID,
		RunID:            rt.RunID,
		ExpectedRevision: rt.Revision,
		AttemptID:        attempt.ID,
		StaleToken:       attempt.ExecutionToken,
		CurrentToken:     current.ExecutionToken,
		ResultRef:        attempt.ResultRef,
		PreviousReceipt:  attempt.Receipt,
	})
	if err != nil {
		return fmt.Errorf("work: encode task.stale_result: %w", err)
	}
	_, err = emit(WorkEvent{
		SchemaVersion: SchemaVersionV2,
		ID:            fmt.Sprintf("rt-stale-%s-%s-%s", rt.TaskID, attempt.ID, attempt.ExecutionToken),
		Type:          EventTaskStaleResult,
		WorkID:        rt.WorkID,
		RequestID:     fmt.Sprintf("%s/rt/stale/%s/%s", rt.RunID, rt.TaskID, attempt.ExecutionToken),
		CreatedAt:     now,
		Payload:       payload,
		Object: ObjectContext{
			Kind:               ObjectTask,
			WorkID:             rt.WorkID,
			ID:                 rt.TaskID,
			RunID:              rt.RunID,
			TaskID:             rt.TaskID,
			ExpectedRevision:   int64Ptr(rt.Revision),
			DefinitionRevision: int64Ptr(rt.DefinitionRev),
		},
	})
	return err
}

func (s *V2Scheduler) acquire(taskID string) bool {
	v2FlightRegistry.Lock()
	defer v2FlightRegistry.Unlock()
	if _, exists := v2FlightRegistry.tasks[taskID]; exists {
		return false
	}
	v2FlightRegistry.tasks[taskID] = struct{}{}
	return true
}

func (s *V2Scheduler) release(taskID string) {
	v2FlightRegistry.Lock()
	delete(v2FlightRegistry.tasks, taskID)
	v2FlightRegistry.Unlock()
}

func normalizeV2Runtimes(runtimes map[string]*V2TaskRuntime) map[string]*V2TaskRuntime {
	return normalizeV2RuntimesForRun(runtimes, "")
}

func normalizeV2RuntimesForRun(runtimes map[string]*V2TaskRuntime, runID string) map[string]*V2TaskRuntime {
	normalized := make(map[string]*V2TaskRuntime, len(runtimes))
	for key, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		if runID != "" && runtime.RunID != runID {
			continue
		}
		nodeID := runtime.NodeID
		if nodeID == "" {
			nodeID = key
		}
		normalized[nodeID] = cloneV2Runtime(runtime)
	}
	return normalized
}

func resolveChangedNodeIDs(changedIDs []string, runtimes map[string]*V2TaskRuntime) []string {
	resolved := make(map[string]bool, len(changedIDs))
	for _, id := range changedIDs {
		if runtime := runtimes[id]; runtime != nil && runtime.NodeID != "" {
			resolved[runtime.NodeID] = true
			continue
		}
		resolved[id] = true
	}
	ids := make([]string, 0, len(resolved))
	for id := range resolved {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func cloneV2RuntimeMap(runtimes map[string]*V2TaskRuntime) map[string]*V2TaskRuntime {
	cloned := make(map[string]*V2TaskRuntime, len(runtimes))
	for nodeID, runtime := range runtimes {
		cloned[nodeID] = cloneV2Runtime(runtime)
	}
	return cloned
}

func cloneV2Runtime(runtime *V2TaskRuntime) *V2TaskRuntime {
	if runtime == nil {
		return nil
	}
	cloned := *runtime
	cloned.Attempts = append([]V2Attempt(nil), runtime.Attempts...)
	cloned.WaitingInputIDs = append([]string(nil), runtime.WaitingInputIDs...)
	if runtime.SessionRef != nil {
		sessionRef := *runtime.SessionRef
		cloned.SessionRef = &sessionRef
	}
	return &cloned
}

func lastRunningAttempt(runtime *V2TaskRuntime) int {
	for index := len(runtime.Attempts) - 1; index >= 0; index-- {
		if runtime.Attempts[index].State == TaskRunning {
			return index
		}
	}
	return -1
}

func resultRefFromAttempt(attempt *Attempt) string {
	if attempt == nil {
		return ""
	}
	return attempt.SessionRef.SessionPath
}

func approvalToken(attempt V2Attempt) string {
	return fmt.Sprintf("approval:%s:%s", attempt.ID, attempt.ExecutionToken)
}

func findNodeDef(nodes []NodeDef, nodeID string) *NodeDef {
	for index := range nodes {
		if nodes[index].ID == nodeID {
			return &nodes[index]
		}
	}
	return nil
}

func hasRunningV2Attempt(runtime *V2TaskRuntime) bool {
	return lastRunningAttempt(runtime) >= 0
}
