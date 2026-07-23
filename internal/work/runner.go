package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"workground2/internal/nilutil"
)

// WorkRunner advances one durable WorkflowRun through its frozen definition.
// It owns no persistence: every mutation is first accepted by eventEmitter.
type WorkRunner struct {
	executor TaskExecutor
	clock    Clock
}

// NewWorkRunner creates a runner over the narrow task execution port.
func NewWorkRunner(executor TaskExecutor) *WorkRunner {
	return &WorkRunner{executor: executor, clock: RealClock{}}
}

type eventEmitter func(WorkEvent) (int64, error)

// Run advances run until it reaches a gate, a terminal state, or an attempt
// whose external outcome is unknown. A committed running Attempt is never
// replayed: recovery exposes that state for a later explicit reconciliation.
func (r *WorkRunner) Run(ctx context.Context, value *Work, run *WorkflowRun, emit eventEmitter) (*WorkflowRun, error) {
	if ctx == nil {
		return nil, errors.New("work: runner: context is required")
	}
	if value == nil || run == nil {
		return nil, errors.New("work: runner: Work and WorkflowRun are required")
	}
	if r == nil || nilutil.IsNil(r.executor) {
		return nil, errors.New("work: runner: TaskExecutor is not configured")
	}
	if emit == nil {
		return nil, errors.New("work: runner: event emitter is required")
	}
	if err := validateDefForRun(value.Definition.Workflow); err != nil {
		return nil, err
	}
	if run.DefinitionDigest != value.Definition.Digest {
		return nil, fmt.Errorf("work: runner: definition drift: run has %q, Work has %q", run.DefinitionDigest, value.Definition.Digest)
	}
	if err := ensureRunShape(value, run); err != nil {
		return nil, err
	}
	if IsTerminalRunState(run.State) || run.State == RunWaiting || hasRunningAttempt(run) {
		return run, nil
	}
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if run.State == RunPending {
		if err := r.setRunState(run, RunRunning, emit); err != nil {
			return nil, err
		}
	}

	for stageIndex := range value.Definition.Workflow.Stages {
		stageSpec := &value.Definition.Workflow.Stages[stageIndex]
		stage := &run.Stages[stageIndex]
		switch stage.State {
		case RunCompleted:
			continue
		case RunFailed, RunCancelled:
			if err := r.setRunState(run, stage.State, emit); err != nil {
				return nil, err
			}
			return run, nil
		case RunWaiting:
			if run.State != RunWaiting {
				if err := r.setRunState(run, RunWaiting, emit); err != nil {
					return nil, err
				}
			}
			return run, nil
		case RunNeedsConfirmation:
			if run.State != RunNeedsConfirmation {
				if err := r.setRunState(run, RunNeedsConfirmation, emit); err != nil {
					return nil, err
				}
			}
			return run, nil
		}

		if stage.State == RunPending && stageSpec.Gate != "" {
			if err := r.setStageState(run, stage, RunWaiting, emit); err != nil {
				return nil, err
			}
			if err := r.setRunState(run, RunWaiting, emit); err != nil {
				return nil, err
			}
			return run, nil
		}

		failed, err := r.executeStage(ctx, value, run, stageIndex, emit)
		if err != nil {
			return nil, err
		}
		if failed || hasRunningAttempt(run) {
			return run, nil
		}
	}

	if err := r.setRunState(run, RunCompleted, emit); err != nil {
		return nil, err
	}
	return run, nil
}

func (r *WorkRunner) executeStage(ctx context.Context, value *Work, run *WorkflowRun, stageIndex int, emit eventEmitter) (bool, error) {
	stage := &run.Stages[stageIndex]
	if stage.State == RunPending {
		if err := r.setStageState(run, stage, RunRunning, emit); err != nil {
			return false, err
		}
	}

	for taskIndex := range stage.Tasks {
		if err := checkServiceContext(ctx); err != nil {
			return false, err
		}
		task := &stage.Tasks[taskIndex]
		switch task.State {
		case RunCompleted:
			continue
		case RunFailed, RunCancelled:
			return r.failStage(run, stage, task.State, emit)
		case RunWaiting:
			return false, nil
		case RunNeedsConfirmation:
			return r.failStage(run, stage, RunNeedsConfirmation, emit)
		}

		if task.State == RunRunning && len(task.Attempts) > 0 {
			last := task.Attempts[len(task.Attempts)-1]
			if last.State == RunRunning {
				return false, nil
			}
			if last.State == RunNeedsConfirmation {
				if err := r.setTaskState(run, stage, task, RunNeedsConfirmation, emit); err != nil {
					return false, err
				}
				return r.failStage(run, stage, RunNeedsConfirmation, emit)
			}
			if IsTerminalRunState(last.State) {
				if err := r.setTaskState(run, stage, task, last.State, emit); err != nil {
					return false, err
				}
				if last.State != RunCompleted {
					return r.failStage(run, stage, last.State, emit)
				}
				continue
			}
		}

		if err := r.executeTask(ctx, value, run, stage, task, emit); err != nil {
			return false, err
		}
		if task.State != RunCompleted {
			return r.failStage(run, stage, task.State, emit)
		}
	}

	if err := r.setStageState(run, stage, RunCompleted, emit); err != nil {
		return false, err
	}
	return false, nil
}

func (r *WorkRunner) executeTask(ctx context.Context, value *Work, run *WorkflowRun, stage *Stage, task *Task, emit eventEmitter) error {
	if task.State == RunPending {
		if err := r.setTaskState(run, stage, task, RunRunning, emit); err != nil {
			return err
		}
	}

	attemptID := runChildID(task.ID, "attempt", fmt.Sprintf("%d", len(task.Attempts)))
	executeRequestID := runEventRequest(run, "execute", attemptID)
	attempt := Attempt{
		ID:              attemptID,
		RequestID:       executeRequestID,
		Index:           len(task.Attempts),
		State:           RunRunning,
		StartedAt:       r.clock.Now().UTC(),
		SideEffectClass: workSideEffectClass(value.Definition.ToolContracts),
	}
	payload, err := json.Marshal(attemptEventPayload{RunID: run.ID, StageID: stage.ID, TaskID: task.ID, Attempt: attempt})
	if err != nil {
		return fmt.Errorf("work: runner: encode Attempt: %w", err)
	}
	if _, err := emit(WorkEvent{
		Type:      EventAttemptChanged,
		RequestID: runEventRequest(run, "attempt", attempt.ID, string(RunRunning)),
		Payload:   payload,
	}); err != nil {
		return err
	}
	task.Attempts = append(task.Attempts, attempt)
	attemptIndex := len(task.Attempts) - 1

	result, execErr := safeExecuteTask(r.executor, ctx, TaskExecuteInput{
		WorkID:           value.ID,
		RunID:            run.ID,
		StageID:          stage.ID,
		TaskID:           task.ID,
		AttemptID:        attempt.ID,
		AttemptIndex:     attempt.Index,
		RequestID:        executeRequestID,
		DefinitionDigest: run.DefinitionDigest,
		SideEffectClass:  attempt.SideEffectClass,
		Prompt:           value.Prompt,
	})

	next := task.Attempts[attemptIndex]
	next.FinishedAt = timePtr(r.clock.Now().UTC())
	switch {
	case execErr != nil:
		next.State = RunFailed
		next.Error = execErr.Error()
	case result == nil:
		next.State = RunCompleted
	default:
		next.SessionRef = result.SessionRef
		next.Error = result.Error
		next.Receipt = result.Receipt
		if strings.TrimSpace(result.SideEffectClass) != "" {
			next.SideEffectClass = result.SideEffectClass
		}
		if result.FinishedAt != nil {
			next.FinishedAt = result.FinishedAt
		}
		switch result.State {
		case "", RunCompleted:
			next.State = RunCompleted
		case RunFailed, RunCancelled:
			next.State = result.State
		case RunNeedsConfirmation:
			next.State = RunNeedsConfirmation
		default:
			next.State = RunFailed
			next.Error = fmt.Sprintf("work: executor returned non-terminal Attempt state %q", result.State)
		}
	}
	applyReceiptGuard(&next, TaskExecuteInput{
		RequestID:       executeRequestID,
		SideEffectClass: attempt.SideEffectClass,
	})

	payload, err = json.Marshal(attemptEventPayload{RunID: run.ID, StageID: stage.ID, TaskID: task.ID, Attempt: next})
	if err != nil {
		return fmt.Errorf("work: runner: encode Attempt result: %w", err)
	}
	if _, err := emit(WorkEvent{
		Type:      EventAttemptChanged,
		RequestID: runEventRequest(run, "attempt", next.ID, string(next.State)),
		Payload:   payload,
	}); err != nil {
		return err
	}
	task.Attempts[attemptIndex] = next
	return r.setTaskState(run, stage, task, next.State, emit)
}

func (r *WorkRunner) failStage(run *WorkflowRun, stage *Stage, state RunState, emit eventEmitter) (bool, error) {
	if state != RunCancelled && state != RunNeedsConfirmation {
		state = RunFailed
	}
	if !IsTerminalRunState(stage.State) {
		if err := r.setStageState(run, stage, state, emit); err != nil {
			return false, err
		}
	}
	if !IsTerminalRunState(run.State) {
		if err := r.setRunState(run, state, emit); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (r *WorkRunner) setRunState(run *WorkflowRun, state RunState, emit eventEmitter) error {
	if run.State == state {
		return nil
	}
	if err := ValidateRunTransition(run.State, state); err != nil {
		return err
	}
	next := *run
	next.State = state
	if state == RunRunning && next.StartedAt.IsZero() {
		next.StartedAt = r.clock.Now().UTC()
	}
	if IsTerminalRunState(state) {
		next.FinishedAt = timePtr(r.clock.Now().UTC())
	}
	payload, err := json.Marshal(runEventPayload{Run: next, WorkState: workStateForRun(state)})
	if err != nil {
		return fmt.Errorf("work: runner: encode WorkflowRun: %w", err)
	}
	if _, err := emit(WorkEvent{
		Type:      EventRunChanged,
		RequestID: runEventRequest(run, "run", run.ID, string(state), payloadRequestPart(payload)),
		Payload:   payload,
	}); err != nil {
		return err
	}
	*run = next
	return nil
}

func (r *WorkRunner) setStageState(run *WorkflowRun, stage *Stage, state RunState, emit eventEmitter) error {
	if stage.State == state {
		return nil
	}
	if err := ValidateRunTransition(stage.State, state); err != nil {
		return err
	}
	next := *stage
	next.State = state
	if state == RunRunning && next.StartedAt.IsZero() {
		next.StartedAt = r.clock.Now().UTC()
	}
	if IsTerminalRunState(state) {
		next.FinishedAt = timePtr(r.clock.Now().UTC())
	}
	payload, err := json.Marshal(stageEventPayload{RunID: run.ID, Stage: next})
	if err != nil {
		return fmt.Errorf("work: runner: encode Stage: %w", err)
	}
	if _, err := emit(WorkEvent{
		Type:      EventStageChanged,
		RequestID: runEventRequest(run, "stage", next.ID, string(state), payloadRequestPart(payload)),
		Payload:   payload,
	}); err != nil {
		return err
	}
	*stage = next
	return nil
}

func (r *WorkRunner) setTaskState(run *WorkflowRun, stage *Stage, task *Task, state RunState, emit eventEmitter) error {
	if task.State == state {
		return nil
	}
	if err := ValidateRunTransition(task.State, state); err != nil {
		return err
	}
	next := *task
	next.State = state
	if state == RunRunning && next.StartedAt == nil {
		next.StartedAt = timePtr(r.clock.Now().UTC())
	}
	if IsTerminalRunState(state) {
		next.FinishedAt = timePtr(r.clock.Now().UTC())
	}
	payload, err := json.Marshal(taskEventPayload{RunID: run.ID, StageID: stage.ID, Task: next})
	if err != nil {
		return fmt.Errorf("work: runner: encode Task: %w", err)
	}
	if _, err := emit(WorkEvent{
		Type:      EventTaskChanged,
		RequestID: runEventRequest(run, "task", next.ID, string(state), payloadRequestPart(payload)),
		Payload:   payload,
	}); err != nil {
		return err
	}
	*task = next
	return nil
}

func safeExecuteTask(executor TaskExecutor, ctx context.Context, input TaskExecuteInput) (result *Attempt, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("work: TaskExecutor panic: %v", recovered)
		}
	}()
	return executor.ExecuteTask(ctx, input)
}

func ensureRunShape(value *Work, run *WorkflowRun) error {
	if run.WorkID != value.ID {
		return fmt.Errorf("work: runner: run %q belongs to Work %q, not %q", run.ID, run.WorkID, value.ID)
	}
	if run.State == "" {
		run.State = RunPending
	}
	if !isRunState(run.State) {
		return fmt.Errorf("work: runner: invalid run state %q", run.State)
	}
	existingStages := make(map[string]Stage, len(run.Stages))
	for _, stage := range run.Stages {
		key := stage.Name
		if key == "" {
			key = stage.ID
		}
		if _, duplicate := existingStages[key]; duplicate {
			return fmt.Errorf("work: runner: duplicate persisted stage %q", key)
		}
		existingStages[key] = stage
	}
	if len(existingStages) > len(value.Definition.Workflow.Stages) {
		return fmt.Errorf("work: runner: definition drift: persisted run has extra stages")
	}

	stages := make([]Stage, 0, len(value.Definition.Workflow.Stages))
	for _, spec := range value.Definition.Workflow.Stages {
		stage, found := existingStages[spec.ID]
		if !found {
			stage = Stage{Name: spec.ID, State: RunPending}
		}
		delete(existingStages, spec.ID)
		if stage.State == "" {
			stage.State = RunPending
		}
		stage.ID = runChildID(run.ID, "stage", spec.ID)
		stage.Name = spec.ID
		stage.Gate = spec.Gate
		if err := ensureTaskShape(run, &stage, spec); err != nil {
			return err
		}
		stages = append(stages, stage)
	}
	if len(existingStages) != 0 {
		return fmt.Errorf("work: runner: definition drift: persisted run references unknown stage")
	}
	run.Stages = stages
	return nil
}

func ensureTaskShape(run *WorkflowRun, stage *Stage, spec StageSpec) error {
	existing := make(map[string]Task, len(stage.Tasks))
	for _, task := range stage.Tasks {
		key := task.Name
		if key == "" {
			key = task.ID
		}
		if _, duplicate := existing[key]; duplicate {
			return fmt.Errorf("work: runner: duplicate persisted task %q in stage %q", key, spec.ID)
		}
		existing[key] = task
	}
	if len(existing) > len(spec.Tasks) {
		return fmt.Errorf("work: runner: definition drift: stage %q has extra tasks", spec.ID)
	}
	tasks := make([]Task, 0, len(spec.Tasks))
	for _, taskSpec := range spec.Tasks {
		task, found := existing[taskSpec.ID]
		if !found {
			task = Task{Name: taskSpec.ID, State: RunPending}
		}
		delete(existing, taskSpec.ID)
		if task.State == "" {
			task.State = RunPending
		}
		task.ID = runChildID(stage.ID, "task", taskSpec.ID)
		task.Name = taskSpec.ID
		for i := range task.Attempts {
			if task.Attempts[i].ID == "" {
				task.Attempts[i].ID = runChildID(task.ID, "attempt", fmt.Sprintf("%d", task.Attempts[i].Index))
			}
		}
		tasks = append(tasks, task)
	}
	if len(existing) != 0 {
		return fmt.Errorf("work: runner: definition drift: stage %q references unknown task", spec.ID)
	}
	stage.Tasks = tasks
	return nil
}

func validateDefForRun(def WorkflowDef) error {
	if len(def.Stages) == 0 {
		return errors.New("work: workflow has no stages")
	}
	stageIDs := make(map[string]struct{}, len(def.Stages))
	for stageIndex, stage := range def.Stages {
		stage.ID = strings.TrimSpace(stage.ID)
		if stage.ID == "" {
			return fmt.Errorf("work: stage[%d] has no ID", stageIndex)
		}
		if _, duplicate := stageIDs[stage.ID]; duplicate {
			return fmt.Errorf("work: duplicate stage ID %q", stage.ID)
		}
		stageIDs[stage.ID] = struct{}{}
		if stage.Gate != "" && stage.Gate != "input" && stage.Gate != "approval" {
			return fmt.Errorf("work: stage %q has unknown gate %q", stage.ID, stage.Gate)
		}
		if len(stage.Tasks) == 0 {
			return fmt.Errorf("work: stage %q has no tasks", stage.ID)
		}
		taskIDs := make(map[string]struct{}, len(stage.Tasks))
		for taskIndex, task := range stage.Tasks {
			task.ID = strings.TrimSpace(task.ID)
			if task.ID == "" {
				return fmt.Errorf("work: stage %q task[%d] has no ID", stage.ID, taskIndex)
			}
			if _, duplicate := taskIDs[task.ID]; duplicate {
				return fmt.Errorf("work: stage %q has duplicate task ID %q", stage.ID, task.ID)
			}
			taskIDs[task.ID] = struct{}{}
		}
	}
	return nil
}

func hasRunningAttempt(run *WorkflowRun) bool {
	for _, stage := range run.Stages {
		for _, task := range stage.Tasks {
			for _, attempt := range task.Attempts {
				if attempt.State == RunRunning || attempt.State == RunNeedsConfirmation {
					return true
				}
			}
		}
	}
	return false
}

func workStateForRun(state RunState) WorkState {
	switch state {
	case RunWaiting:
		return WorkWaitingUser
	case RunNeedsConfirmation:
		return WorkWaitingUser
	case RunCompleted:
		return WorkCompleted
	case RunFailed:
		return WorkFailed
	case RunCancelled:
		return WorkCancelled
	default:
		return WorkRunning
	}
}

func payloadRequestPart(payload []byte) string {
	digest := sha256.Sum256(payload)
	return fmt.Sprintf("%x", digest[:10])
}

func workSideEffectClass(contracts []ToolContractRef) string {
	rank := map[string]int{"read": 1, "workspace_write": 2, "external_write": 3, "destructive": 4}
	class, maxRank := "read", 1
	for _, contract := range contracts {
		candidate := strings.TrimSpace(contract.SideEffectClass)
		if rank[candidate] > maxRank {
			class, maxRank = candidate, rank[candidate]
		}
	}
	return class
}

func receiptRequired(class string) bool {
	switch strings.TrimSpace(class) {
	case "external_write", "destructive":
		return true
	default:
		return false
	}
}

func applyReceiptGuard(attempt *Attempt, input TaskExecuteInput) {
	if attempt == nil || !receiptRequired(input.SideEffectClass) {
		return
	}
	receipt := attempt.Receipt
	if receipt != nil && strings.TrimSpace(receipt.RequestID) == strings.TrimSpace(input.RequestID) {
		return
	}
	attempt.State = RunNeedsConfirmation
	attempt.Error = "external outcome has no matching receipt; human confirmation is required before retry"
}

func runEventRequest(run *WorkflowRun, parts ...string) string {
	root := strings.TrimSpace(run.RequestID)
	if root == "" {
		root = run.ID
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%s/run/event/%x", root, hash[:10])
}

func runChildID(parent, kind, source string) string {
	hash := sha256.Sum256([]byte(parent + "\x00" + kind + "\x00" + source))
	return fmt.Sprintf("%s-%x", kind, hash[:12])
}
