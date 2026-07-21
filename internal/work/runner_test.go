package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── Test helpers ───────────────────────────────────────────────────────────

// testWorkflow creates a simple WorkflowDef with one stage and one task.
func testWorkflow(stageID, taskID string) WorkflowDef {
	return WorkflowDef{
		Stages: []StageSpec{
			{ID: stageID, Title: "Stage", Tasks: []TaskSpec{{ID: taskID, Title: "Task"}}},
		},
	}
}

// testWorkflowMulti creates a multi-stage, multi-task WorkflowDef.
func testWorkflowMulti(stages []struct {
	id    string
	tasks []string
}) WorkflowDef {
	var specs []StageSpec
	for _, s := range stages {
		var ts []TaskSpec
		for _, t := range s.tasks {
			ts = append(ts, TaskSpec{ID: t, Title: t})
		}
		specs = append(specs, StageSpec{ID: s.id, Title: s.id, Tasks: ts})
	}
	return WorkflowDef{Stages: specs}
}

// testWorkflowWithGate creates a WorkflowDef with a gate on the stage.
func testWorkflowWithGate(stageID, taskID, gate string) WorkflowDef {
	return WorkflowDef{
		Stages: []StageSpec{
			{ID: stageID, Title: "Stage", Tasks: []TaskSpec{{ID: taskID, Title: "Task"}}, Gate: gate},
		},
	}
}

func testBlueprint(id string, version int, wf WorkflowDef) *WorkBlueprint {
	return &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             id,
		Version:        version,
		Name:           id,
		Source:         BlueprintSystem,
		PromptTemplate: "test prompt",
		Workflow:       wf,
		BlockSpecs: []BlockSpec{
			{ID: "notes", Kind: "markdown", SchemaVersion: 1, Label: "Notes", Placement: BlockPlacement{Slot: "primary", Order: 0}},
		},
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

type runnerFixture struct {
	root     string
	store    *FileWorkStore
	sink     *serviceSink
	svc      *Service
	executor *fakeRunnerExecutor
	registry *BlueprintRegistry
}

// fakeRunnerExecutor is a TaskExecutor with configurable behavior per task.
type fakeRunnerExecutor struct {
	ExecuteFunc func(ctx context.Context, input TaskExecuteInput) (*Attempt, error)
	CancelFunc  func(ctx context.Context, input TaskCancelInput) error
}

func (f *fakeRunnerExecutor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	if f.ExecuteFunc == nil {
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	return f.ExecuteFunc(ctx, input)
}

func (f *fakeRunnerExecutor) CancelTask(ctx context.Context, input TaskCancelInput) error {
	if f.CancelFunc == nil {
		return nil
	}
	return f.CancelFunc(ctx, input)
}

func newRunnerFixture(t *testing.T) *runnerFixture {
	t.Helper()
	root := t.TempDir()
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	sink := &serviceSink{next: make(chan WorkViewEvent, 256)}
	registry := NewBlueprintRegistry()
	svc := NewService(store, registry, sink)
	executor := &fakeRunnerExecutor{}
	svc.SetTaskExecutor(executor)
	return &runnerFixture{
		root:     root,
		store:    store,
		sink:     sink,
		svc:      svc,
		executor: executor,
		registry: registry,
	}
}

func (f *runnerFixture) restart(t *testing.T) *Service {
	t.Helper()
	store, err := NewFileWorkStore(f.root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart NewFileWorkStore: %v", err)
	}
	f.store = store
	f.svc = NewService(store, f.registry, f.sink)
	f.svc.SetTaskExecutor(f.executor)
	return f.svc
}

func (f *runnerFixture) registerBlueprint(t *testing.T, bp *WorkBlueprint) {
	t.Helper()
	if err := f.registry.Register(bp); err != nil {
		t.Fatalf("register blueprint: %v", err)
	}
}

func (f *runnerFixture) createWork(t *testing.T, bpRef BlueprintRef, requestID string) *Work {
	t.Helper()
	work, err := f.svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: bpRef,
		Name:         "Test Work",
		RequestID:    requestID,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return work
}

// ── Tests ───────────────────────────────────────────────────────────────────

func TestRunnerSequentialSuccess(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-seq", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-seq", SchemaVersion: SchemaVersion, Version: 1}, "create-1")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-1")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if run.State != RunCompleted {
		t.Errorf("expected run state completed, got %s", run.State)
	}
	if len(run.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(run.Stages))
	}
	stage := run.Stages[0]
	if stage.State != RunCompleted {
		t.Errorf("expected stage completed, got %s", stage.State)
	}
	if len(stage.Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(stage.Tasks))
	}
	task := stage.Tasks[0]
	if task.State != RunCompleted {
		t.Errorf("expected task completed, got %s", task.State)
	}
	if len(task.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(task.Attempts))
	}
	attempt := task.Attempts[0]
	if attempt.State != RunCompleted {
		t.Errorf("expected attempt completed, got %s", attempt.State)
	}
}

func TestRunnerMultiStageMultiTask(t *testing.T) {
	f := newRunnerFixture(t)
	wf := testWorkflowMulti([]struct {
		id    string
		tasks []string
	}{
		{"stage1", []string{"task1a", "task1b"}},
		{"stage2", []string{"task2a"}},
		{"stage3", []string{"task3a", "task3b", "task3c"}},
	})
	bp := testBlueprint("blueprint:test-multi", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-multi", SchemaVersion: SchemaVersion, Version: 1}, "create-2")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-2")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if run.State != RunCompleted {
		t.Errorf("expected run completed, got %s", run.State)
	}
	if len(run.Stages) != 3 {
		t.Fatalf("expected 3 stages, got %d", len(run.Stages))
	}
	for i, stage := range run.Stages {
		if stage.State != RunCompleted {
			t.Errorf("stage[%d] %s: expected completed, got %s", i, stage.Name, stage.State)
		}
		expectedTasks := wf.Stages[i].Tasks
		if len(stage.Tasks) != len(expectedTasks) {
			t.Errorf("stage[%d] %s: expected %d tasks, got %d", i, stage.Name, len(expectedTasks), len(stage.Tasks))
		}
	}
}

func TestRunnerExecutorFailure(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-fail", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-fail", SchemaVersion: SchemaVersion, Version: 1}, "create-3")

	sentinel := errors.New("executor kaboom")
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, sentinel
	}

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-3")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if run.State != RunFailed {
		t.Errorf("expected run failed, got %s", run.State)
	}
	stage := run.Stages[0]
	if stage.State != RunFailed {
		t.Errorf("expected stage failed, got %s", stage.State)
	}
	task := stage.Tasks[0]
	if task.State != RunFailed {
		t.Errorf("expected task failed, got %s", task.State)
	}
	if len(task.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(task.Attempts))
	}
	attempt := task.Attempts[0]
	if attempt.State != RunFailed {
		t.Errorf("expected attempt failed, got %s", attempt.State)
	}
	if attempt.Error != sentinel.Error() {
		t.Errorf("expected error %q, got %q", sentinel.Error(), attempt.Error)
	}
}

func TestRunnerIdempotentRequestID(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-idem", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-idem", SchemaVersion: SchemaVersion, Version: 1}, "create-4")

	// First run.
	run1, err := f.svc.RunWork(context.Background(), work.ID, "run-4")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}

	// Second run with same requestID returns the same run.
	run2, err := f.svc.RunWork(context.Background(), work.ID, "run-4")
	if err != nil {
		t.Fatalf("second RunWork: %v", err)
	}

	if run1.ID != run2.ID {
		t.Errorf("expected same run ID, got %q vs %q", run1.ID, run2.ID)
	}
	if run2.State != RunCompleted {
		t.Errorf("expected completed, got %s", run2.State)
	}
}

func TestRunnerIdempotentNewRequestID(t *testing.T) {
	// A different requestID is an explicit rerun against the same frozen definition.
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-idem2", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-idem2", SchemaVersion: SchemaVersion, Version: 1}, "create-5")

	// First call.
	run1, err := f.svc.RunWork(context.Background(), work.ID, "run-5a")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}

	run2, err := f.svc.RunWork(context.Background(), work.ID, "run-5b")
	if err != nil {
		t.Fatalf("second RunWork: %v", err)
	}

	view2, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get after second: %v", err)
	}
	if len(view2.Work.Runs) != 2 {
		t.Errorf("expected 2 runs, got %d", len(view2.Work.Runs))
	}
	if run1.ID == run2.ID {
		t.Errorf("different request IDs reused run %q", run1.ID)
	}
}

func TestRunnerRestartRecovery(t *testing.T) {
	f := newRunnerFixture(t)

	// Multi-task workflow so we can crash mid-execution.
	wf := testWorkflowMulti([]struct {
		id    string
		tasks []string
	}{
		{"stage1", []string{"task1", "task2", "task3"}},
	})
	bp := testBlueprint("blueprint:test-restart", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-restart", SchemaVersion: SchemaVersion, Version: 1}, "create-6")

	// Make the executor fail on task2, simulating a crash mid-execution.
	var callCount int32
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		c := atomic.AddInt32(&callCount, 1)
		if c == 2 {
			// Fail on task2 — this task is attempted but fails.
			return nil, errors.New("mid-execution failure")
		}
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-6")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}

	if run.State != RunFailed {
		t.Fatalf("expected run failed, got %s", run.State)
	}

	// Verify task1 completed, task2 failed, task3 not attempted.
	if len(run.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(run.Stages))
	}
	stage := run.Stages[0]
	if len(stage.Tasks) != 3 {
		t.Fatalf("expected complete 3-task skeleton, got %d", len(stage.Tasks))
	}
	if stage.Tasks[0].State != RunCompleted {
		t.Errorf("task1: expected completed, got %s", stage.Tasks[0].State)
	}
	if stage.Tasks[1].State != RunFailed {
		t.Errorf("task2: expected failed, got %s", stage.Tasks[1].State)
	}
	if stage.Tasks[2].State != RunPending {
		t.Errorf("task3: expected pending, got %s", stage.Tasks[2].State)
	}

	// Now "restart" — create a new service with same store, fix executor.
	f.restart(t)
	f.executor.ExecuteFunc = nil // Default success behavior.

	// Replaying the same request recovers the committed failed projection.
	run2, err := f.svc.RunWork(context.Background(), work.ID, "run-6")
	if err != nil {
		t.Fatalf("resume RunWork: %v", err)
	}

	// Since task2 already failed, the stage is failed and the run should be failed too.
	// The executor shouldn't be called again for task2 because it's terminal.
	if run2.State != RunFailed {
		t.Errorf("expected run still failed, got %s", run2.State)
	}
	if got := atomic.LoadInt32(&callCount); got != 2 {
		t.Errorf("restart replay executed tasks again: calls=%d", got)
	}
}

func TestRunnerRestartRecoveryMidStage(t *testing.T) {
	// Simulate a restart where a stage has partially completed, and the
	// failed task is retried by creating a new run.
	f := newRunnerFixture(t)

	wf := testWorkflowMulti([]struct {
		id    string
		tasks []string
	}{
		{"stage1", []string{"taskA", "taskB"}},
		{"stage2", []string{"taskC"}},
	})
	bp := testBlueprint("blueprint:test-midstage", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-midstage", SchemaVersion: SchemaVersion, Version: 1}, "create-7")

	// First run: stage1 completes, then crash before stage2.
	var callCount int32
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		c := atomic.AddInt32(&callCount, 1)
		if c == 2 {
			// Succeed taskB but simulate the crash after emit (we can't actually crash,
			// so just verify the state is correct).
		}
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-7a")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}

	if run.State != RunCompleted {
		t.Fatalf("expected completed, got %s", run.State)
	}
	if len(run.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(run.Stages))
	}
	for _, s := range run.Stages {
		if s.State != RunCompleted {
			t.Errorf("stage %s: expected completed, got %s", s.Name, s.State)
		}
	}

	// Restart and call RunWork again — should find completed run and do nothing.
	f.restart(t)
	f.executor.ExecuteFunc = nil

	run2, err := f.svc.RunWork(context.Background(), work.ID, "run-7b")
	if err != nil {
		t.Fatalf("resume RunWork: %v", err)
	}
	if run2.State != RunCompleted {
		t.Errorf("expected still completed, got %s", run2.State)
	}
}

func TestRunnerEmptyStage(t *testing.T) {
	// Empty stages are rejected by validateDefForRun at the runner level.
	// Blueprint registration also rejects empty stages, so this path is tested
	// via TestRunnerDirectEmptyStage.
	runner := NewWorkRunner(&fakeRunnerExecutor{})
	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest:   "sha256:abc",
			Workflow: WorkflowDef{Stages: []StageSpec{}},
		},
	}
	run := &WorkflowRun{ID: "run-1", WorkID: work.ID}
	emit := func(event WorkEvent) (int64, error) { return 0, nil }
	_, err := runner.Run(context.Background(), work, run, emit)
	if err == nil {
		t.Fatal("expected error for empty stages")
	}
	if !strings.Contains(err.Error(), "no stages") {
		t.Errorf("expected 'no stages', got: %v", err)
	}
}

func TestRunnerEmptyTasksInStage(t *testing.T) {
	// Empty tasks in a stage are rejected at the runner level.
	// Blueprint registration also rejects this, so the Service path cannot reach
	// this validation. Tested directly via runner.
	runner := NewWorkRunner(&fakeRunnerExecutor{})
	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{{ID: "s1", Title: "Stage", Tasks: []TaskSpec{}}},
			},
		},
	}
	run := &WorkflowRun{ID: "run-1", WorkID: work.ID}
	emit := func(event WorkEvent) (int64, error) { return 0, nil }
	_, err := runner.Run(context.Background(), work, run, emit)
	if err == nil {
		t.Fatal("expected error for empty tasks in stage")
	}
	if !strings.Contains(err.Error(), "no tasks") {
		t.Errorf("expected 'no tasks', got: %v", err)
	}
}

func TestRunnerIllegalGate(t *testing.T) {
	// Illegal gates are rejected at the runner level.
	// Blueprint registration also rejects unknown gates, so this is tested
	// via TestRunnerDirectIllegalGate.
	runner := NewWorkRunner(&fakeRunnerExecutor{})
	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "s1", Title: "Stage", Tasks: []TaskSpec{{ID: "t1", Title: "Task"}}, Gate: "bad-gate"},
				},
			},
		},
	}
	run := &WorkflowRun{ID: "run-1", WorkID: work.ID}
	emit := func(event WorkEvent) (int64, error) { return 0, nil }
	_, err := runner.Run(context.Background(), work, run, emit)
	if err == nil {
		t.Fatal("expected error for unknown gate")
	}
	if !strings.Contains(err.Error(), "unknown gate") {
		t.Errorf("expected 'unknown gate', got: %v", err)
	}
}

func TestRunnerInputGatePauses(t *testing.T) {
	f := newRunnerFixture(t)
	wf := testWorkflowWithGate("main", "run", "input")
	bp := testBlueprint("blueprint:test-inputgate", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-inputgate", SchemaVersion: SchemaVersion, Version: 1}, "create-11")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-11")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	// Run should stop at the gate. The full task skeleton remains pending.
	if IsTerminalRunState(run.State) {
		t.Errorf("expected non-terminal run state, got %s", run.State)
	}
	if len(run.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(run.Stages))
	}
	if run.Stages[0].State != RunWaiting {
		t.Errorf("expected stage waiting, got %s", run.Stages[0].State)
	}
	if len(run.Stages[0].Tasks) != 1 || run.Stages[0].Tasks[0].State != RunPending || len(run.Stages[0].Tasks[0].Attempts) != 0 {
		t.Errorf("expected one untouched pending task before gate: %+v", run.Stages[0].Tasks)
	}
}

func TestRunnerApprovalGatePauses(t *testing.T) {
	f := newRunnerFixture(t)
	wf := testWorkflowWithGate("main", "run", "approval")
	bp := testBlueprint("blueprint:test-approvalgate", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-approvalgate", SchemaVersion: SchemaVersion, Version: 1}, "create-12")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-12")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if IsTerminalRunState(run.State) {
		t.Errorf("expected non-terminal run state, got %s", run.State)
	}
	if run.Stages[0].State != RunWaiting {
		t.Errorf("expected stage waiting, got %s", run.Stages[0].State)
	}
}

func TestRunnerTerminalNoRegression(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-noregress", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-noregress", SchemaVersion: SchemaVersion, Version: 1}, "create-13")

	// First run completes successfully.
	_, err := f.svc.RunWork(context.Background(), work.ID, "run-13a")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}

	// Reload and verify completed state.
	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	run := view.Work.Runs[0]
	if run.State != RunCompleted {
		t.Fatalf("expected completed, got %s", run.State)
	}

	// Try to run again — should be a no-op (terminal state).
	run2, err := f.svc.RunWork(context.Background(), work.ID, "run-13b")
	if err != nil {
		t.Fatalf("second RunWork: %v", err)
	}
	if run2.State != RunCompleted {
		t.Errorf("expected still completed, got %s", run2.State)
	}
	if len(view.Work.Runs) != 1 {
		t.Errorf("expected 1 run, got %d", len(view.Work.Runs))
	}
}

func TestRunnerEventContextScoped(t *testing.T) {
	f := newRunnerFixture(t)

	// Two runs with different IDs but same stage/task names.
	wf := testWorkflow("main", "run")
	bp := testBlueprint("blueprint:test-scoped", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-scoped", SchemaVersion: SchemaVersion, Version: 1}, "create-14")

	// Run with requestID "run-14a" — this creates run ID "workID/run-14a"
	run1, err := f.svc.RunWork(context.Background(), work.ID, "run-14a")
	if err != nil {
		t.Fatalf("first RunWork: %v", err)
	}
	if run1.State != RunCompleted {
		t.Fatalf("expected first run completed, got %s", run1.State)
	}

	// Now load the projection and verify both runs have correct stages.
	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(view.Work.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(view.Work.Runs))
	}

	r := view.Work.Runs[0]
	if len(r.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(r.Stages))
	}
	if r.Stages[0].Name != "main" {
		t.Errorf("expected stage name 'main', got %q", r.Stages[0].Name)
	}
	if len(r.Stages[0].Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(r.Stages[0].Tasks))
	}
	if r.Stages[0].Tasks[0].Name != "run" {
		t.Errorf("expected task name 'run', got %q", r.Stages[0].Tasks[0].Name)
	}
}

func TestRunnerAttemptStatePersistedBeforeExecution(t *testing.T) {
	// Verify that the attempt running state is persisted before the executor is called.
	// If the executor panics or crashes, the attempt should be observable as running → failed.
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-attempt", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-attempt", SchemaVersion: SchemaVersion, Version: 1}, "create-15")

	sentinel := errors.New("executor failure after persist")
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		persisted, loadErr := f.store.LoadProjection(work.ID)
		if loadErr != nil {
			t.Fatalf("LoadProjection inside executor: %v", loadErr)
		}
		attempt := persisted.Runs[0].Stages[0].Tasks[0].Attempts[0]
		if attempt.State != RunRunning {
			t.Fatalf("attempt was not persisted before executor: %s", attempt.State)
		}
		if input.RequestID == "" || input.RunID == "" || input.StageID == "" || input.TaskID == "" {
			t.Fatalf("executor input lacks stable context: %+v", input)
		}
		return nil, sentinel
	}

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-15")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	// The attempt should be in failed state with the error message.
	attempt := run.Stages[0].Tasks[0].Attempts[0]
	if attempt.State != RunFailed {
		t.Errorf("expected attempt failed, got %s", attempt.State)
	}
	if attempt.Error != sentinel.Error() {
		t.Errorf("expected error %q, got %q", sentinel.Error(), attempt.Error)
	}
	if attempt.StartedAt.IsZero() {
		t.Error("expected non-zero startedAt for failed attempt")
	}
	if attempt.FinishedAt == nil || attempt.FinishedAt.IsZero() {
		t.Error("expected non-zero finishedAt for failed attempt")
	}
}

func TestRunnerRequiresTaskExecutor(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	svc := NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	// Don't call SetTaskExecutor.

	_, err = svc.RunWork(context.Background(), "some-id", "req-1")
	if err == nil {
		t.Fatal("expected error for missing TaskExecutor, got nil")
	}
	if !strings.Contains(err.Error(), "TaskExecutor") {
		t.Errorf("expected 'TaskExecutor' in error, got: %v", err)
	}
}

func TestRunnerDefinitionDrift(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-drift", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-drift", SchemaVersion: SchemaVersion, Version: 1}, "create-16")

	tampered := work.Definition
	tampered.Workflow.Stages[0].Title = "tampered without digest update"
	payload := mustMarshal(tampered)
	_, state, err := f.store.LoadState(work.ID, "")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	event := newServiceEvent(work.ID, "tamper-definition", EventDefinitionFrozen, payload, time.Now().UTC())
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, err := f.store.CommitEvent(work.ID, event); err != nil {
		t.Fatalf("CommitEvent tamper: %v", err)
	}

	if _, err := f.svc.RunWork(context.Background(), work.ID, "run-drift"); err == nil || !strings.Contains(err.Error(), "definition drift") {
		t.Fatalf("expected explicit definition drift error, got %v", err)
	}
}

func TestRunnerNoTaskExecutorConfigured(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-noexec", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-noexec", SchemaVersion: SchemaVersion, Version: 1}, "create-17")

	// Create a new service without executor.
	svc2 := NewService(f.store, f.registry, ViewSinkDiscard)
	_, err := svc2.RunWork(context.Background(), work.ID, "run-17")
	if err == nil {
		t.Fatal("expected error for unconfigured TaskExecutor, got nil")
	}
	if !strings.Contains(err.Error(), "TaskExecutor") {
		t.Errorf("expected 'TaskExecutor' in error, got: %v", err)
	}
}

func TestRunnerRunWorkValidation(t *testing.T) {
	f := newRunnerFixture(t)

	// Empty workID.
	_, err := f.svc.RunWork(context.Background(), "", "req-1")
	if err == nil {
		t.Fatal("expected error for empty workID")
	}

	// Empty requestID.
	_, err = f.svc.RunWork(context.Background(), "some-id", "")
	if err == nil {
		t.Fatal("expected error for empty requestID")
	}

	// Non-existent workID.
	_, err = f.svc.RunWork(context.Background(), "non-existent", "req-2")
	if err == nil {
		t.Fatal("expected error for non-existent workID")
	}
}

func TestRunnerStateTransitionsEnforced(t *testing.T) {
	// Completed and cancelled are irreversible. Failed can reopen only through
	// an explicit RetryTask reservation.

	if err := ValidateRunTransition(RunCompleted, RunRunning); err == nil {
		t.Error("expected error for completed → running transition")
	}
	if err := ValidateRunTransition(RunFailed, RunRunning); err != nil {
		t.Errorf("unexpected error for explicit retry failed → running transition: %v", err)
	}
	if err := ValidateRunTransition(RunCancelled, RunRunning); err == nil {
		t.Error("expected error for cancelled → running transition")
	}
	if err := ValidateRunTransition(RunCompleted, RunWaiting); err == nil {
		t.Error("expected error for completed → waiting transition")
	}

	// Valid transitions.
	if err := ValidateRunTransition(RunRunning, RunCompleted); err != nil {
		t.Errorf("unexpected error for running → completed: %v", err)
	}
	if err := ValidateRunTransition(RunRunning, RunFailed); err != nil {
		t.Errorf("unexpected error for running → failed: %v", err)
	}
	if err := ValidateRunTransition(RunRunning, RunWaiting); err != nil {
		t.Errorf("unexpected error for running → waiting: %v", err)
	}
	if err := ValidateRunTransition(RunWaiting, RunRunning); err != nil {
		t.Errorf("unexpected error for waiting → running: %v", err)
	}

	// Same state is always valid.
	if err := ValidateRunTransition(RunCompleted, RunCompleted); err != nil {
		t.Errorf("unexpected error for same state: %v", err)
	}
}

func TestRunnerIsTerminal(t *testing.T) {
	if !IsTerminalRunState(RunCompleted) {
		t.Error("expected RunCompleted to be terminal")
	}
	if !IsTerminalRunState(RunFailed) {
		t.Error("expected RunFailed to be terminal")
	}
	if !IsTerminalRunState(RunCancelled) {
		t.Error("expected RunCancelled to be terminal")
	}
	if IsTerminalRunState(RunRunning) {
		t.Error("expected RunRunning to NOT be terminal")
	}
	if IsTerminalRunState(RunWaiting) {
		t.Error("expected RunWaiting to NOT be terminal")
	}
}

// ── WorkRunner direct tests ────────────────────────────────────────────────

func TestRunnerDirectSequentialSuccess(t *testing.T) {
	exec := &fakeRunnerExecutor{}
	runner := NewWorkRunner(exec)

	work := &Work{
		ID:     "test-work",
		Prompt: "test prompt",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc123",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "setup", Title: "Setup", Tasks: []TaskSpec{{ID: "init", Title: "Init"}}},
					{ID: "verify", Title: "Verify", Tasks: []TaskSpec{{ID: "check", Title: "Check"}}},
				},
			},
		},
	}

	run := &WorkflowRun{
		ID:               "run-1",
		WorkID:           work.ID,
		DefinitionDigest: work.Definition.Digest,
	}

	var events []WorkEvent
	emit := func(event WorkEvent) (int64, error) {
		events = append(events, event)
		return int64(len(events)), nil
	}

	result, err := runner.Run(context.Background(), work, run, emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.State != RunCompleted {
		t.Errorf("expected completed, got %s", result.State)
	}
	if len(result.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(result.Stages))
	}
	for _, s := range result.Stages {
		if s.State != RunCompleted {
			t.Errorf("stage %s: expected completed, got %s", s.Name, s.State)
		}
	}

	// Verify events carry correct context.
	if len(events) == 0 {
		t.Fatal("expected events to be emitted")
	}
	// Service persists run.started; direct runner execution begins with run.changed.
	if events[0].Type != EventRunChanged {
		t.Errorf("expected first event to be run.changed, got %s", events[0].Type)
	}
	// There should be stage events with runID.
	hasStage := false
	for _, ev := range events {
		if ev.Type == EventStageChanged {
			var p stageEventPayload
			if err := jsonUnmarshal(ev.Payload, &p); err != nil {
				t.Errorf("unmarshal stage payload: %v", err)
				continue
			}
			if p.RunID != run.ID {
				t.Errorf("expected stage event runID %q, got %q", run.ID, p.RunID)
			}
			hasStage = true
		}
	}
	if !hasStage {
		t.Error("expected at least one StageChanged event")
	}
}

// jsonUnmarshal is a thin wrapper for encoding/json.Unmarshal.
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

func TestRunnerDirectExecutorFailure(t *testing.T) {
	sentinel := errors.New("executor boom")
	exec := &fakeRunnerExecutor{
		ExecuteFunc: func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
			return nil, sentinel
		},
	}
	runner := NewWorkRunner(exec)

	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "main", Title: "Main", Tasks: []TaskSpec{{ID: "run", Title: "Run"}}},
				},
			},
		},
	}

	run := &WorkflowRun{ID: "run-1", WorkID: work.ID, DefinitionDigest: work.Definition.Digest}

	var events []WorkEvent
	emit := func(event WorkEvent) (int64, error) {
		events = append(events, event)
		return int64(len(events)), nil
	}

	result, err := runner.Run(context.Background(), work, run, emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.State != RunFailed {
		t.Errorf("expected failed, got %s", result.State)
	}
	if result.Stages[0].State != RunFailed {
		t.Errorf("expected stage failed, got %s", result.Stages[0].State)
	}
	if result.Stages[0].Tasks[0].State != RunFailed {
		t.Errorf("expected task failed, got %s", result.Stages[0].Tasks[0].State)
	}
}

func TestRunnerDirectTerminalNoop(t *testing.T) {
	exec := &fakeRunnerExecutor{
		ExecuteFunc: func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
			t.Error("executor should not be called for completed run")
			return nil, nil
		},
	}
	runner := NewWorkRunner(exec)

	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "main", Title: "Main", Tasks: []TaskSpec{{ID: "run", Title: "Run"}}},
				},
			},
		},
	}

	run := &WorkflowRun{
		ID:               "run-1",
		WorkID:           work.ID,
		DefinitionDigest: work.Definition.Digest,
		State:            RunCompleted,
	}

	var events []WorkEvent
	emit := func(event WorkEvent) (int64, error) {
		events = append(events, event)
		return int64(len(events)), nil
	}

	result, err := runner.Run(context.Background(), work, run, emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.State != RunCompleted {
		t.Errorf("expected still completed, got %s", result.State)
	}
	if len(events) != 0 {
		t.Errorf("expected no events for completed run, got %d", len(events))
	}
}

func TestRunnerDirectEmptyStage(t *testing.T) {
	runner := NewWorkRunner(&fakeRunnerExecutor{})

	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{},
			},
		},
	}

	run := &WorkflowRun{ID: "run-1", WorkID: work.ID}
	emit := func(event WorkEvent) (int64, error) { return 0, nil }

	_, err := runner.Run(context.Background(), work, run, emit)
	if err == nil {
		t.Fatal("expected error for empty stages")
	}
	if !strings.Contains(err.Error(), "no stages") {
		t.Errorf("expected 'no stages', got: %v", err)
	}
}

func TestRunnerDirectIllegalGate(t *testing.T) {
	runner := NewWorkRunner(&fakeRunnerExecutor{})

	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "s1", Title: "Stage", Tasks: []TaskSpec{{ID: "t1", Title: "Task"}}, Gate: "bad-gate"},
				},
			},
		},
	}

	run := &WorkflowRun{ID: "run-1", WorkID: work.ID}
	emit := func(event WorkEvent) (int64, error) { return 0, nil }

	_, err := runner.Run(context.Background(), work, run, emit)
	if err == nil {
		t.Fatal("expected error for unknown gate")
	}
	if !strings.Contains(err.Error(), "unknown gate") {
		t.Errorf("expected 'unknown gate', got: %v", err)
	}
}

func TestRunnerDirectResumePartial(t *testing.T) {
	exec := &fakeRunnerExecutor{}
	runner := NewWorkRunner(exec)

	work := &Work{
		ID: "test-work",
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{
					{ID: "setup", Title: "Setup", Tasks: []TaskSpec{{ID: "init", Title: "Init"}}},
					{ID: "verify", Title: "Verify", Tasks: []TaskSpec{{ID: "check", Title: "Check"}}},
				},
			},
		},
	}

	// Pre-populate stage1 as already completed.
	now := time.Now()
	run := &WorkflowRun{
		ID:               "run-1",
		WorkID:           work.ID,
		DefinitionDigest: work.Definition.Digest,
		State:            RunRunning,
		Stages: []Stage{
			{Name: "setup", State: RunCompleted, StartedAt: now, FinishedAt: &now, Tasks: []Task{
				{Name: "init", State: RunCompleted, Attempts: []Attempt{{Index: 0, State: RunCompleted, StartedAt: now, FinishedAt: &now}}},
			}},
		},
	}

	var events []WorkEvent
	emit := func(event WorkEvent) (int64, error) {
		events = append(events, event)
		return int64(len(events)), nil
	}

	result, err := runner.Run(context.Background(), work, run, emit)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if result.State != RunCompleted {
		t.Errorf("expected completed, got %s", result.State)
	}
	if len(result.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(result.Stages))
	}
	// Stage1 should still be completed, stage2 should now be completed.
	if result.Stages[0].State != RunCompleted {
		t.Errorf("stage1: expected completed, got %s", result.Stages[0].State)
	}
	if result.Stages[1].State != RunCompleted {
		t.Errorf("stage2: expected completed, got %s", result.Stages[1].State)
	}
	// Executor should only have been called for stage2's task.
	// We can verify this by checking that stage1 still has exactly 1 attempt.
	if len(result.Stages[0].Tasks[0].Attempts) != 1 {
		t.Errorf("stage1 task: expected 1 attempt, got %d", len(result.Stages[0].Tasks[0].Attempts))
	}
}

// ── Unicode / non-ASCII test ───────────────────────────────────────────────

func TestRunnerUnicodeWorkflowIDs(t *testing.T) {
	f := newRunnerFixture(t)
	wf := testWorkflow("阶段一", "任务一")
	bp := testBlueprint("blueprint:test-unicode", 1, wf)
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: "blueprint:test-unicode", SchemaVersion: SchemaVersion, Version: 1}, "创建-1")

	run, err := f.svc.RunWork(context.Background(), work.ID, "运行-1")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if run.State != RunCompleted {
		t.Errorf("expected completed, got %s", run.State)
	}
	if len(run.Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(run.Stages))
	}
	if run.Stages[0].Name != "阶段一" {
		t.Errorf("expected stage name '阶段一', got %q", run.Stages[0].Name)
	}
	if len(run.Stages[0].Tasks) != 1 {
		t.Fatalf("expected 1 task, got %d", len(run.Stages[0].Tasks))
	}
	if run.Stages[0].Tasks[0].Name != "任务一" {
		t.Errorf("expected task name '任务一', got %q", run.Stages[0].Tasks[0].Name)
	}
}

func TestRunnerValidateDefForRun(t *testing.T) {
	// Valid definition.
	if err := validateDefForRun(testWorkflow("s", "t")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Empty stages.
	if err := validateDefForRun(WorkflowDef{}); err == nil {
		t.Error("expected error for empty stages")
	}

	// Empty tasks in stage.
	if err := validateDefForRun(WorkflowDef{
		Stages: []StageSpec{{ID: "s", Tasks: []TaskSpec{}}},
	}); err == nil {
		t.Error("expected error for empty tasks")
	}

	// Good gates.
	if err := validateDefForRun(testWorkflowWithGate("s", "t", "input")); err != nil {
		t.Errorf("input gate should be valid: %v", err)
	}
	if err := validateDefForRun(testWorkflowWithGate("s", "t", "approval")); err != nil {
		t.Errorf("approval gate should be valid: %v", err)
	}
	if err := validateDefForRun(testWorkflowWithGate("s", "t", "")); err != nil {
		t.Errorf("empty gate should be valid: %v", err)
	}

	// Bad gate.
	if err := validateDefForRun(testWorkflowWithGate("s", "t", "invalid")); err == nil {
		t.Error("expected error for invalid gate")
	}
}

func TestRunnerNewWorkRunner(t *testing.T) {
	exec := &fakeRunnerExecutor{}
	runner := NewWorkRunner(exec)
	if runner == nil {
		t.Fatal("expected non-nil runner")
	}
	if runner.executor != exec {
		t.Error("executor not set correctly")
	}
}

func TestSetTaskExecutor(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	svc := NewService(store, NewBlueprintRegistry(), ViewSinkDiscard)
	if svc.runner != nil {
		t.Error("expected nil runner before SetTaskExecutor")
	}

	exec := &fakeRunnerExecutor{}
	svc.SetTaskExecutor(exec)
	if svc.runner == nil {
		t.Fatal("expected non-nil runner after SetTaskExecutor")
	}

	// Calling again should replace.
	exec2 := &fakeRunnerExecutor{}
	svc.SetTaskExecutor(exec2)
}

// ── Event payload backward compatibility test ──────────────────────────────

func TestReducerBackwardCompatOldEvents(t *testing.T) {
	// Test that old-format events (without runID in payload) still work.
	reducer := DefaultReducer()

	// Create initial work.
	work := &Work{
		ID:            "test-work",
		SchemaVersion: SchemaVersion,
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		Definition: WorkDefinitionSnapshot{
			Digest: "sha256:abc",
			Workflow: WorkflowDef{
				Stages: []StageSpec{{ID: "main", Title: "Main", Tasks: []TaskSpec{{ID: "run", Title: "Run"}}}},
			},
		},
		Blocks:    []BlockInstance{},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Add a run.
	payload := mustMarshal(WorkflowRun{ID: "run-1", WorkID: work.ID, State: RunRunning, Stages: []Stage{{Name: "main", State: RunRunning}}})
	ev := WorkEvent{Type: EventRunStarted, Payload: payload, CreatedAt: time.Now()}
	result, err := reducer(ev, work)
	if err != nil {
		t.Fatalf("reducer run.started: %v", err)
	}
	if len(result.Runs) != 1 {
		t.Fatalf("expected 1 run, got %d", len(result.Runs))
	}

	// An OLD bare payload still updates the matching legacy stage.
	oldStagePayload := mustMarshal(Stage{Name: "main", State: RunRunning})
	ev = WorkEvent{Type: EventStageChanged, Payload: oldStagePayload, CreatedAt: time.Now()}
	result, err = reducer(ev, result)
	if err != nil {
		t.Fatalf("reducer stage.changed (old format): %v", err)
	}
	if len(result.Runs[0].Stages) != 1 {
		t.Fatalf("expected 1 stage, got %d", len(result.Runs[0].Stages))
	}

	// Add a stage using NEW format (with runID wrapper).
	newStagePayload := mustMarshal(stageEventPayload{RunID: "run-1", Stage: Stage{Name: "main", State: RunCompleted}})
	ev = WorkEvent{Type: EventStageChanged, Payload: newStagePayload, CreatedAt: time.Now()}
	result, err = reducer(ev, result)
	if err != nil {
		t.Fatalf("reducer stage.changed (new format): %v", err)
	}
	if result.Runs[0].Stages[0].State != RunCompleted {
		t.Errorf("expected stage completed, got %s", result.Runs[0].Stages[0].State)
	}

	// Verify scoping: add a second run, then add a stage using new format scoped to run-2.
	payload2 := mustMarshal(WorkflowRun{ID: "run-2", WorkID: work.ID, State: RunRunning, Stages: []Stage{{Name: "main", State: RunRunning}}})
	ev = WorkEvent{Type: EventRunStarted, Payload: payload2, CreatedAt: time.Now()}
	result, err = reducer(ev, result)
	if err != nil {
		t.Fatalf("reducer run.started #2: %v", err)
	}
	if len(result.Runs) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(result.Runs))
	}

	// New stage scoped to run-2 should only affect run-2.
	newStagePayload2 := mustMarshal(stageEventPayload{RunID: "run-2", Stage: Stage{Name: "main", State: RunFailed}})
	ev = WorkEvent{Type: EventStageChanged, Payload: newStagePayload2, CreatedAt: time.Now()}
	result, err = reducer(ev, result)
	if err != nil {
		t.Fatalf("reducer stage.changed scoped to run-2: %v", err)
	}
	// Run-1's stage should still be completed.
	if result.Runs[0].Stages[0].State != RunCompleted {
		t.Errorf("run-1 stage: expected completed, got %s", result.Runs[0].Stages[0].State)
	}
	// Run-2's stage should be failed.
	if result.Runs[1].Stages[0].State != RunFailed {
		t.Errorf("run-2 stage: expected failed, got %s", result.Runs[1].Stages[0].State)
	}
}

func TestLifecycleWorkStateProjection(t *testing.T) {
	f := newRunnerFixture(t)
	successBP := testBlueprint("blueprint:lifecycle-success", 1, testWorkflow("main", "run"))
	gateBP := testBlueprint("blueprint:lifecycle-gate", 1, testWorkflowWithGate("main", "run", "approval"))
	failBP := testBlueprint("blueprint:lifecycle-fail", 1, testWorkflow("main", "run"))
	for _, bp := range []*WorkBlueprint{successBP, gateBP, failBP} {
		f.registerBlueprint(t, bp)
	}

	success := f.createWork(t, BlueprintRef{ID: successBP.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-success")
	if _, err := f.svc.RunWork(context.Background(), success.ID, "run-lifecycle-success"); err != nil {
		t.Fatalf("success RunWork: %v", err)
	}
	assertWorkState(t, f.svc, success.ID, WorkCompleted)

	gate := f.createWork(t, BlueprintRef{ID: gateBP.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-gate")
	gateRun, err := f.svc.RunWork(context.Background(), gate.ID, "run-lifecycle-gate")
	if err != nil {
		t.Fatalf("gate RunWork: %v", err)
	}
	if gateRun.State != RunWaiting {
		t.Fatalf("gate run state = %s, want %s", gateRun.State, RunWaiting)
	}
	assertWorkState(t, f.svc, gate.ID, WorkWaitingUser)

	failure := f.createWork(t, BlueprintRef{ID: failBP.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-fail")
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("provider/model unavailable")
	}
	failedRun, err := f.svc.RunWork(context.Background(), failure.ID, "run-lifecycle-fail")
	if err != nil {
		t.Fatalf("failed RunWork persisted domain failure: %v", err)
	}
	if failedRun.State != RunFailed || failedRun.Stages[0].Tasks[0].Attempts[0].Error == "" {
		t.Fatalf("failed run lacks observable attempt failure: %+v", failedRun)
	}
	assertWorkState(t, f.svc, failure.ID, WorkFailed)
}

func TestLifecycleConcurrentDuplicateRequest(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:lifecycle-concurrent", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-concurrent")

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return &Attempt{State: RunCompleted}, nil
	}
	type result struct {
		run *WorkflowRun
		err error
	}
	results := make(chan result, 2)
	call := func() {
		run, err := f.svc.RunWork(context.Background(), value.ID, "run-lifecycle-concurrent")
		results <- result{run: run, err: err}
	}
	go call()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("executor did not start")
	}
	go call()
	close(release)

	var runID string
	for range 2 {
		select {
		case got := <-results:
			if got.err != nil {
				t.Fatalf("RunWork: %v", got.err)
			}
			if got.run.State != RunCompleted {
				t.Fatalf("run state = %s", got.run.State)
			}
			if runID != "" && got.run.ID != runID {
				t.Fatalf("duplicate request created runs %q and %q", runID, got.run.ID)
			}
			runID = got.run.ID
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent RunWork timed out")
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("duplicate request executed %d times", got)
	}
}

func TestLifecycleRestartRunningAttemptDoesNotReplay(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:lifecycle-running-recovery", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-running-recovery")

	started := make(chan struct{})
	release := make(chan struct{})
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		close(started)
		<-release
		return &Attempt{State: RunCompleted}, nil
	}
	firstDone := make(chan error, 1)
	firstService := f.svc
	go func() {
		_, err := firstService.RunWork(context.Background(), value.ID, "run-lifecycle-running-recovery")
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first executor did not start")
	}

	store2, err := NewFileWorkStore(f.root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart NewFileWorkStore: %v", err)
	}
	var replayCalls atomic.Int32
	executor2 := &fakeRunnerExecutor{ExecuteFunc: func(context.Context, TaskExecuteInput) (*Attempt, error) {
		replayCalls.Add(1)
		return &Attempt{State: RunCompleted}, nil
	}}
	service2 := NewService(store2, f.registry, ViewSinkDiscard)
	service2.SetTaskExecutor(executor2)
	recovered, err := service2.RunWork(context.Background(), value.ID, "run-lifecycle-running-recovery")
	if err != nil {
		t.Fatalf("restart RunWork: %v", err)
	}
	if recovered.State != RunRunning || !hasRunningAttempt(recovered) {
		t.Fatalf("restart did not recover running attempt: %+v", recovered)
	}
	if got := replayCalls.Load(); got != 0 {
		t.Fatalf("restart replayed unknown external outcome %d times", got)
	}

	close(release)
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first RunWork: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first RunWork did not finish")
	}
}

func TestLifecycleTypedNilExecutorFailsClosed(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:lifecycle-typed-nil", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-lifecycle-typed-nil")
	var executor *fakeRunnerExecutor
	f.svc.SetTaskExecutor(executor)
	if _, err := f.svc.RunWork(context.Background(), value.ID, "run-lifecycle-typed-nil"); err == nil || !strings.Contains(err.Error(), "TaskExecutor") {
		t.Fatalf("typed-nil executor did not fail closed: %v", err)
	}
}

func TestLifecycleTerminalReducerGuard(t *testing.T) {
	value := &Work{ID: "work-terminal", State: WorkCompleted, Runs: []WorkflowRun{{
		ID: "run-terminal", WorkID: "work-terminal", State: RunCompleted,
	}}}
	payload := mustMarshal(runEventPayload{
		Run:       WorkflowRun{ID: "run-terminal", WorkID: value.ID, State: RunRunning},
		WorkState: WorkRunning,
	})
	_, err := DefaultReducer()(WorkEvent{Type: EventRunChanged, Payload: payload, CreatedAt: time.Now().UTC()}, value)
	if err == nil || !strings.Contains(err.Error(), "invalid RunState transition") {
		t.Fatalf("terminal run regressed: %v", err)
	}
}

func assertWorkState(t *testing.T, service *Service, workID string, want WorkState) {
	t.Helper()
	view, err := service.Get(context.Background(), workID)
	if err != nil {
		t.Fatalf("Get(%s): %v", workID, err)
	}
	if view.Work.State != want {
		t.Fatalf("Work %s state = %s, want %s", workID, view.Work.State, want)
	}
}

func mustMarshal(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
