package work

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ── RetryTask tests ────────────────────────────────────────────────────────

func TestRetryTaskSuccess(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-ok", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-ok")

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("first failure")
	}
	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-ok")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunFailed {
		t.Fatalf("expected run failed, got %s", run.State)
	}
	task := run.Stages[0].Tasks[0]
	if len(task.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(task.Attempts))
	}

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	attempt, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-1",
	})
	if err != nil {
		t.Fatalf("RetryTask: %v", err)
	}
	if attempt.State != RunCompleted {
		t.Errorf("expected attempt completed, got %s", attempt.State)
	}
	if attempt.Index != 1 {
		t.Errorf("expected attempt index 1, got %d", attempt.Index)
	}

	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	updatedTask := view.Work.Runs[0].Stages[0].Tasks[0]
	if len(updatedTask.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(updatedTask.Attempts))
	}
	if updatedTask.Attempts[1].State != RunCompleted {
		t.Errorf("expected attempt[1] completed, got %s", updatedTask.Attempts[1].State)
	}
}

func TestRetryTaskIdempotent(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-idem", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-idem")

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("failure")
	}
	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-idem")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	task := run.Stages[0].Tasks[0]

	var execCalls atomic.Int32
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		execCalls.Add(1)
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}

	a1, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-idem-1",
	})
	if err != nil {
		t.Fatalf("first RetryTask: %v", err)
	}

	a2, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-idem-1",
	})
	if err != nil {
		t.Fatalf("second RetryTask with same requestID: %v", err)
	}
	if a1.ID != a2.ID {
		t.Errorf("same requestID returned different attempts: %q vs %q", a1.ID, a2.ID)
	}
	if got := execCalls.Load(); got != 1 {
		t.Errorf("expected 1 execution, got %d", got)
	}
}

func TestRetryTaskNewRequestIDNewAttempt(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-new", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-new")

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("fail")
	}
	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-new")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	task := run.Stages[0].Tasks[0]

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("retry a failed")
	}

	a1, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-new-a",
	})
	if err == nil || a1 == nil || a1.State != RunFailed {
		t.Fatalf("first RetryTask = (%+v, %v), want persisted failure", a1, err)
	}

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("fail again")
	}
	a2, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-new-b",
	})
	if err == nil || a2 == nil || a2.State != RunFailed {
		t.Fatalf("second RetryTask = (%+v, %v), want persisted failure", a2, err)
	}

	now := time.Now()
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	a3, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-new-c",
	})
	if err != nil {
		t.Fatalf("third RetryTask: %v", err)
	}

	if a1.ID == a3.ID {
		t.Errorf("different requestIDs returned same attempt %q", a1.ID)
	}
	view, _ := f.svc.Get(context.Background(), work.ID)
	nt := view.Work.Runs[0].Stages[0].Tasks[0]
	if len(nt.Attempts) != 4 {
		t.Errorf("expected 4 attempts, got %d", len(nt.Attempts))
	}
}

func TestRetryTaskWrongState(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-wrong", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-wrong")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-wrong")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	task := run.Stages[0].Tasks[0]

	_, err = f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-wrong",
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled and completed") {
		t.Errorf("expected 'cancelled and completed' error for completed run, got %v", err)
	}
}

func TestRetryTaskConcurrent(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-conc", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-conc")

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("fail")
	}
	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-conc")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	task := run.Stages[0].Tasks[0]

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}

	type result struct {
		a   *Attempt
		err error
	}
	results := make(chan result, 2)
	go func() {
		a, e := f.svc.RetryTask(context.Background(), RetryTaskInput{
			WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-conc-a",
		})
		results <- result{a, e}
	}()
	<-started
	go func() {
		a, e := f.svc.RetryTask(context.Background(), RetryTaskInput{
			WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-conc-a",
		})
		results <- result{a, e}
	}()
	close(release)

	var ids []string
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("concurrent RetryTask: %v", r.err)
		}
		if r.a != nil {
			ids = append(ids, r.a.ID)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Errorf("same concurrent request must return one attempt, got %v", ids)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("same concurrent request executed %d times", got)
	}
}

// ── CancelRun tests ────────────────────────────────────────────────────────

func TestCancelRunRunning(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-cancel-run", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-run")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-cancel-run")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunWaiting {
		t.Fatalf("expected run waiting, got %s", run.State)
	}

	err = f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-1")
	if err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get after cancel: %v", err)
	}
	cancelled := view.Work.Runs[0]
	if cancelled.State != RunCancelled {
		t.Errorf("expected run cancelled, got %s", cancelled.State)
	}
	if view.Work.State != WorkCancelled {
		t.Errorf("expected work cancelled, got %s", view.Work.State)
	}
}

func TestCancelRunTerminalNoop(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-cancel-noop", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-noop")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-cancel-noop")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunCompleted {
		t.Fatalf("expected run completed, got %s", run.State)
	}

	err = f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-noop")
	if err != nil {
		t.Fatalf("CancelRun on terminal run: %v", err)
	}
}

func TestCancelRunIdempotent(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-cancel-idem", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-idem")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-cancel-idem")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	if err := f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-idem-1"); err != nil {
		t.Fatalf("first CancelRun: %v", err)
	}
	if err := f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-idem-1"); err != nil {
		t.Fatalf("second CancelRun: %v", err)
	}
}

func TestCancelRunWaitingHasNoActiveTask(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-cancel-exec", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-exec")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-cancel-exec")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	var cancelCalled atomic.Bool
	f.executor.CancelFunc = func(ctx context.Context, input TaskCancelInput) error {
		cancelCalled.Store(true)
		return nil
	}

	if err := f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-exec-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}
	if cancelCalled.Load() {
		t.Fatal("waiting gate has no active attempt to cancel")
	}
	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get cancelled Work: %v", err)
	}
	if receipt := view.Work.Runs[0].Cancel; receipt == nil || receipt.Status != CancelDelivered {
		t.Fatalf("waiting cancel receipt = %+v", receipt)
	}
}

func TestRestartRunIdempotent(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-restart-idem", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-restart-idem")
	first, err := f.svc.RunWork(context.Background(), value.ID, "run-restart-idem")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	restarted, err := f.svc.RestartRun(context.Background(), value.ID, first.ID, "restart-idem")
	if err != nil {
		t.Fatalf("RestartRun: %v", err)
	}
	duplicate, err := f.svc.RestartRun(context.Background(), value.ID, first.ID, "restart-idem")
	if err != nil {
		t.Fatalf("duplicate RestartRun: %v", err)
	}
	if duplicate.ID != restarted.ID {
		t.Fatalf("duplicate restart IDs = %q, %q", restarted.ID, duplicate.ID)
	}

	view, err := f.svc.Get(context.Background(), value.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if len(view.Work.Runs) != 2 {
		t.Fatalf("runs after duplicate restart = %d, want 2", len(view.Work.Runs))
	}
	if view.Work.Runs[0].State != RunCancelled {
		t.Fatalf("original run state = %s, want %s", view.Work.Runs[0].State, RunCancelled)
	}
}

// ── PauseRun tests ─────────────────────────────────────────────────────────

func TestPauseRunRejectsNonRunning(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-pause-reject", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-pause-reject")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-pause-reject")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunCompleted {
		t.Fatalf("expected completed, got %s", run.State)
	}

	err = f.svc.PauseRun(context.Background(), work.ID, run.ID, "pause-reject-1")
	if err == nil || !strings.Contains(err.Error(), "only running runs can be paused") {
		t.Errorf("expected 'only running runs' error, got %v", err)
	}
}

// ── ResumeRun tests ────────────────────────────────────────────────────────

func TestResumeRunGateResolution(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-gate-resume", 1, testWorkflowWithGate("approval_stage", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-gate-resume")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-gate-resume")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunWaiting {
		t.Fatalf("expected run waiting at gate, got %s", run.State)
	}

	resumed, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID:    work.ID,
		RunID:     run.ID,
		RequestID: "resume-gate-1",
		GateResolutions: map[string]GateResolution{
			"approval_stage": {StageID: "approval_stage", Outcome: "approved"},
		},
	})
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if resumed.State != RunCompleted {
		t.Errorf("expected run completed after gate resolution, got %s", resumed.State)
	}
}

func TestResumeRunInputGate(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-input-gate", 1, testWorkflowWithGate("input_stage", "run", "input"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-input-gate")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-input-gate")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunWaiting {
		t.Fatalf("expected run waiting at input gate, got %s", run.State)
	}

	resumed, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID:    work.ID,
		RunID:     run.ID,
		RequestID: "resume-input-1",
		GateResolutions: map[string]GateResolution{
			"input_stage": {StageID: "input_stage", Outcome: "input_provided", Input: map[string]any{"key": "value"}},
		},
	})
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	if resumed.State != RunCompleted {
		t.Errorf("expected run completed after input gate, got %s", resumed.State)
	}
}

func TestResumeRunIdempotent(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-resume-idem", 1, testWorkflowWithGate("approval_stage", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-resume-idem")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-resume-idem")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	r1, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: work.ID, RunID: run.ID, RequestID: "resume-idem-1",
		GateResolutions: map[string]GateResolution{"approval_stage": {Outcome: "approved"}},
	})
	if err != nil {
		t.Fatalf("first ResumeRun: %v", err)
	}

	r2, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: work.ID, RunID: run.ID, RequestID: "resume-idem-1",
		GateResolutions: map[string]GateResolution{"approval_stage": {Outcome: "approved"}},
	})
	if err != nil {
		t.Fatalf("second ResumeRun: %v", err)
	}
	if r1.State != r2.State {
		t.Errorf("idempotent resume returned different states: %s vs %s", r1.State, r2.State)
	}
}

func TestResumeRunRejectsNonWaiting(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-resume-reject", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-resume-reject")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-resume-reject")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunCompleted {
		t.Fatalf("expected completed, got %s", run.State)
	}

	_, err = f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: work.ID, RunID: run.ID, RequestID: "resume-reject-1",
	})
	if err == nil || !strings.Contains(err.Error(), "only waiting runs") {
		t.Errorf("expected 'only waiting runs' error, got %v", err)
	}
}

// ── Late result / cancel race tests ────────────────────────────────────────

func TestLateResultAfterCancel(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-late-result", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-late-result")

	started := make(chan struct{})
	release := make(chan struct{})
	var execDone sync.WaitGroup
	execDone.Add(1)
	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		close(started)
		<-release
		defer execDone.Done()
		now := time.Now()
		return &Attempt{State: RunCompleted, SessionRef: SessionRef{BranchID: "late-result"}, FinishedAt: &now}, nil
	}

	go f.svc.RunWork(context.Background(), work.ID, "run-late-result")
	<-started

	view, _ := f.svc.Get(context.Background(), work.ID)
	run := view.Work.Runs[0]

	if err := f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-late-1"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	close(release)
	execDone.Wait()

	view, err := f.svc.Get(context.Background(), work.ID)
	if err != nil {
		t.Fatalf("Get after late result: %v", err)
	}
	finalRun := view.Work.Runs[0]
	if finalRun.State != RunCancelled {
		t.Errorf("terminal state overwritten by late result: %s", finalRun.State)
	}
}

func TestLateApprovalAfterResume(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-late-approval", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-late-approval")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-late-approval")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}

	_, err = f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: work.ID, RunID: run.ID, RequestID: "approval-real-1",
		GateResolutions: map[string]GateResolution{"gate": {Outcome: "approved"}},
	})
	if err != nil {
		t.Fatalf("first ResumeRun: %v", err)
	}

	restarted := f.restart(t)
	restarted.SetTaskExecutor(&fakeRunnerExecutor{})
	_, err = restarted.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: work.ID, RunID: run.ID, RequestID: "approval-late-1",
		GateResolutions: map[string]GateResolution{"gate": {Outcome: "approved"}},
	})
	if err != nil {
		if !strings.Contains(err.Error(), "only waiting runs") {
			t.Errorf("expected 'only waiting runs' error, got %v", err)
		}
	}
}

// ── Receipt / needs_confirmation tests ─────────────────────────────────────

func TestReceiptMissingNeedsConfirmation(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-receipt", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-receipt")

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		now := time.Now()
		return &Attempt{
			State:      RunNeedsConfirmation,
			SessionRef: SessionRef{BranchID: "needs-confirm"},
			FinishedAt: &now,
		}, nil
	}

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-receipt")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	task := run.Stages[0].Tasks[0]
	attempt := task.Attempts[0]
	if attempt.State != RunNeedsConfirmation {
		t.Errorf("expected attempt needs_confirmation, got %s", attempt.State)
	}

	f.executor.ExecuteFunc = func(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
		now := time.Now()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	retried, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-receipt",
	})
	if err != nil {
		t.Fatalf("RetryTask on needs_confirmation: %v", err)
	}
	if retried.State != RunCompleted {
		t.Errorf("expected retried attempt completed, got %s", retried.State)
	}
}

// ── RetryTask on cancelled run ─────────────────────────────────────────────

func TestRetryTaskTerminalRun(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-terminal", 1, testWorkflowWithGate("gate", "run", "approval"))
	f.registerBlueprint(t, bp)

	work := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-terminal")

	run, err := f.svc.RunWork(context.Background(), work.ID, "run-retry-terminal")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if err := f.svc.CancelRun(context.Background(), work.ID, run.ID, "cancel-terminal"); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	view, _ := f.svc.Get(context.Background(), work.ID)
	if view.Work.Runs[0].State != RunCancelled {
		t.Fatalf("expected cancelled state, got %s", view.Work.Runs[0].State)
	}

	task := view.Work.Runs[0].Stages[0].Tasks[0]
	_, err = f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: work.ID, RunID: run.ID, StageID: view.Work.Runs[0].Stages[0].ID, TaskID: task.ID, RequestID: "retry-terminal-1",
	})
	if err == nil || !strings.Contains(err.Error(), "cancelled and completed") {
		t.Errorf("expected 'cancelled and completed' error, got %v", err)
	}
}

// ── Unit tests ─────────────────────────────────────────────────────────────

func TestStateMachineRunNeedsConfirmation(t *testing.T) {
	if err := ValidateRunTransition(RunRunning, RunNeedsConfirmation); err != nil {
		t.Errorf("RunRunning→RunNeedsConfirmation should be valid: %v", err)
	}
	if err := ValidateRunTransition(RunNeedsConfirmation, RunRunning); err != nil {
		t.Errorf("RunNeedsConfirmation→RunRunning should be valid: %v", err)
	}
	if err := ValidateRunTransition(RunNeedsConfirmation, RunCancelled); err != nil {
		t.Errorf("RunNeedsConfirmation→RunCancelled should be valid: %v", err)
	}
	if err := ValidateRunTransition(RunNeedsConfirmation, RunFailed); err != nil {
		t.Errorf("RunNeedsConfirmation→RunFailed should be valid: %v", err)
	}
	if err := ValidateRunTransition(RunNeedsConfirmation, RunCompleted); err == nil {
		t.Error("RunNeedsConfirmation→RunCompleted should be invalid")
	}
	if IsTerminalRunState(RunNeedsConfirmation) {
		t.Error("RunNeedsConfirmation should not be terminal")
	}
	if !isRunState(RunNeedsConfirmation) {
		t.Error("RunNeedsConfirmation should be a valid run state")
	}
}

func TestResumeRunInputJSON(t *testing.T) {
	input := ResumeRunInput{
		WorkID:    "w1",
		RunID:     "r1",
		RequestID: "req1",
		GateResolutions: map[string]GateResolution{
			"approval_stage": {StageID: "approval_stage", Outcome: "approved", Note: "looks good"},
		},
	}
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded ResumeRunInput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.WorkID != "w1" || decoded.RunID != "r1" || decoded.RequestID != "req1" {
		t.Errorf("field mismatch: %+v", decoded)
	}
	if g, ok := decoded.GateResolutions["approval_stage"]; !ok || g.Outcome != "approved" {
		t.Errorf("gate resolution mismatch: %+v", decoded.GateResolutions)
	}
}

func TestRetryTaskInputAccessors(t *testing.T) {
	input := RetryTaskInput{
		WorkID: "w1", RunID: "r1", StageID: "s1", TaskID: "t1", RequestID: "req1",
	}
	if input.RequestID != "req1" {
		t.Error("RequestID field access broken")
	}
}

type runControlStore struct {
	WorkStore
	failCancel atomic.Bool
}

func (s *runControlStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	if s.failCancel.Load() && event.Type == EventRunChanged && strings.HasSuffix(event.RequestID, "/cancel") {
		return 0, errors.New("injected cancel commit failure")
	}
	return s.WorkStore.CommitEvent(workID, event)
}

type runControlDualFailureStore struct {
	WorkStore
	mu sync.Mutex

	failCommitType WorkEventType
	commitErr      error
	commitFailed   bool
	loadsOnFailure []error

	armCompletedRun bool
	completedArmed  bool
	loadsOnComplete []error
	loadErrors      []error
}

func (s *runControlDualFailureStore) LoadState(workID, requestID string) (*Work, WorkEventState, error) {
	s.mu.Lock()
	if len(s.loadErrors) > 0 {
		err := s.loadErrors[0]
		s.loadErrors = s.loadErrors[1:]
		s.mu.Unlock()
		return nil, WorkEventState{}, err
	}
	s.mu.Unlock()
	return s.WorkStore.LoadState(workID, requestID)
}

func (s *runControlDualFailureStore) CommitEvent(workID string, event WorkEvent) (int64, error) {
	s.mu.Lock()
	if !s.commitFailed && s.commitErr != nil && event.Type == s.failCommitType {
		s.commitFailed = true
		s.loadErrors = append([]error(nil), s.loadsOnFailure...)
		err := s.commitErr
		s.mu.Unlock()
		return 0, err
	}
	s.mu.Unlock()

	revision, err := s.WorkStore.CommitEvent(workID, event)
	if err != nil || !s.armCompletedRun || event.Type != EventRunChanged {
		return revision, err
	}
	var payload runEventPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.Run.State != RunCompleted {
		return revision, nil
	}
	s.mu.Lock()
	if !s.completedArmed {
		s.completedArmed = true
		s.loadErrors = append([]error(nil), s.loadsOnComplete...)
	}
	s.mu.Unlock()
	return revision, nil
}

func TestRetryTaskRunnerReloadJoinsErrors(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-retry-dual-failure", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-retry-dual-failure")
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		return nil, errors.New("initial task failure")
	}
	run, err := f.svc.RunWork(context.Background(), value.ID, "run-retry-dual-failure")
	if err != nil || run.State != RunFailed {
		t.Fatalf("RunWork = (%+v, %v), want failed projection", run, err)
	}

	primaryErr := errors.New("injected retry runner failure")
	reloadErr := errors.New("injected retry reload failure")
	f.svc.store = &runControlDualFailureStore{
		WorkStore: f.svc.store, failCommitType: EventTaskChanged, commitErr: primaryErr,
		loadsOnFailure: []error{reloadErr},
	}
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	task := run.Stages[0].Tasks[0]
	attempt, err := f.svc.RetryTask(context.Background(), RetryTaskInput{
		WorkID: value.ID, RunID: run.ID, StageID: run.Stages[0].ID, TaskID: task.ID, RequestID: "retry-dual-failure",
	})
	if attempt == nil || attempt.State != RunCompleted {
		t.Fatalf("RetryTask attempt = %+v, want committed completed attempt", attempt)
	}
	assertJoinedRunControlErrors(t, err, primaryErr, reloadErr, "RetryTask", "retry-runner-reload", value.ID)
}

func TestResumeRunRunnerReloadJoinsErrors(t *testing.T) {
	f, value, run := newWaitingRunFixture(t, "resume-runner-dual-failure")
	primaryErr := errors.New("injected resume runner failure")
	reloadErr := errors.New("injected resume runner reload failure")
	f.svc.store = &runControlDualFailureStore{
		WorkStore: f.svc.store, failCommitType: EventTaskChanged, commitErr: primaryErr,
		loadsOnFailure: []error{reloadErr},
	}

	latest, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: value.ID, RunID: run.ID, RequestID: "resume-runner-dual-failure",
		GateResolutions: map[string]GateResolution{"approval": {Outcome: "approved"}},
	})
	if latest == nil || latest.ID != run.ID || latest.State != RunRunning {
		t.Fatalf("ResumeRun latest = %+v, want last known running projection", latest)
	}
	assertJoinedRunControlErrors(t, err, primaryErr, reloadErr, "ResumeRun", "resume-runner-reload", value.ID)
}

func TestResumeRunViewReloadJoinsErrors(t *testing.T) {
	f, value, run := newWaitingRunFixture(t, "resume-view-dual-failure")
	viewErr := errors.New("injected resume view failure")
	reloadErr := errors.New("injected resume view reload failure")
	f.svc.store = &runControlDualFailureStore{
		WorkStore: f.svc.store, armCompletedRun: true,
		loadsOnComplete: []error{viewErr, reloadErr},
	}

	latest, err := f.svc.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: value.ID, RunID: run.ID, RequestID: "resume-view-dual-failure",
		GateResolutions: map[string]GateResolution{"approval": {Outcome: "approved"}},
	})
	if latest == nil || latest.ID != run.ID {
		t.Fatalf("ResumeRun latest = %+v, want last known run", latest)
	}
	assertJoinedRunControlErrors(t, err, viewErr, reloadErr, "resume-view", "resume-view-reload", value.ID)
}

func TestRunControlNoIgnoredSecondaryLoadStateErrors(t *testing.T) {
	source, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatalf("read service.go: %v", err)
	}
	for _, ignored := range []string{"_, _ = s.store.LoadState", "_ = s.store.LoadState"} {
		if strings.Contains(string(source), ignored) {
			t.Errorf("service.go still ignores a secondary LoadState error via %q", ignored)
		}
	}
}

func newWaitingRunFixture(t *testing.T, suffix string) (*runnerFixture, *Work, *WorkflowRun) {
	t.Helper()
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-"+suffix, 1, testWorkflowWithGate("approval", "run", "approval"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-"+suffix)
	run, err := f.svc.RunWork(context.Background(), value.ID, "run-"+suffix)
	if err != nil || run.State != RunWaiting {
		t.Fatalf("RunWork = (%+v, %v), want waiting projection", run, err)
	}
	return f, value, run
}

func assertJoinedRunControlErrors(t *testing.T, err, primaryErr, reloadErr error, contexts ...string) {
	t.Helper()
	if err == nil || !errors.Is(err, primaryErr) || !errors.Is(err, reloadErr) {
		t.Fatalf("joined error = %v; primary=%v secondary=%v", err, errors.Is(err, primaryErr), errors.Is(err, reloadErr))
	}
	for _, context := range contexts {
		if !strings.Contains(err.Error(), context) {
			t.Errorf("joined error %q lacks context %q", err, context)
		}
	}
}

func TestCancelCommitFailurePreventsSideEffect(t *testing.T) {
	f := newRunnerFixture(t)
	store := &runControlStore{WorkStore: f.svc.store}
	f.svc.store = store
	bp := testBlueprint("blueprint:test-cancel-commit", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-commit")

	started, release := make(chan struct{}), make(chan struct{})
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		close(started)
		<-release
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	var cancelCalls atomic.Int32
	f.executor.CancelFunc = func(context.Context, TaskCancelInput) error {
		cancelCalls.Add(1)
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RunWork(context.Background(), value.ID, "run-cancel-commit")
		done <- err
	}()
	<-started
	view, _ := f.svc.Get(context.Background(), value.ID)
	run := view.Work.Runs[0]
	store.failCancel.Store(true)
	err := f.svc.CancelRun(context.Background(), value.ID, run.ID, "cancel-commit")
	if err == nil || !strings.Contains(err.Error(), "injected cancel commit failure") {
		t.Fatalf("CancelRun error = %v", err)
	}
	if got := cancelCalls.Load(); got != 0 {
		t.Fatalf("cancel side effect ran %d times before intent commit", got)
	}
	store.failCancel.Store(false)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("RunWork after failed cancel: %v", err)
	}
}

func TestCancelDeliveryFailureRetry(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-cancel-delivery", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-cancel-delivery")

	started, release := make(chan struct{}), make(chan struct{})
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		close(started)
		<-release
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	var cancelCalls atomic.Int32
	var target TaskCancelInput
	f.executor.CancelFunc = func(_ context.Context, input TaskCancelInput) error {
		target = input
		if cancelCalls.Add(1) == 1 {
			return errors.New("injected cancel delivery failure")
		}
		return nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RunWork(context.Background(), value.ID, "run-cancel-delivery")
		done <- err
	}()
	<-started
	view, _ := f.svc.Get(context.Background(), value.ID)
	run := view.Work.Runs[0]
	err := f.svc.CancelRun(context.Background(), value.ID, run.ID, "cancel-delivery")
	if err == nil || !strings.Contains(err.Error(), "injected cancel delivery failure") {
		t.Fatalf("first CancelRun error = %v", err)
	}
	view, _ = f.svc.Get(context.Background(), value.ID)
	if got := view.Work.Runs[0].Cancel; got == nil || got.Status != CancelFailed || got.Attempts != 1 {
		t.Fatalf("failed cancel receipt = %+v", got)
	}
	if target.AttemptID == "" || target.Session.SessionPath != "" {
		t.Fatalf("stable cancel target = %+v; wanted Attempt ID without SessionRef dependency", target)
	}
	if err := f.svc.CancelRun(context.Background(), value.ID, run.ID, "cancel-delivery"); err != nil {
		t.Fatalf("retry CancelRun: %v", err)
	}
	view, _ = f.svc.Get(context.Background(), value.ID)
	if got := view.Work.Runs[0].Cancel; got == nil || got.Status != CancelDelivered || got.Attempts != 2 {
		t.Fatalf("delivered cancel receipt = %+v", got)
	}
	if got := cancelCalls.Load(); got != 2 {
		t.Fatalf("cancel delivery calls = %d, want 2", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("late RunWork result: %v", err)
	}
	view, _ = f.svc.Get(context.Background(), value.ID)
	if view.Work.Runs[0].State != RunCancelled {
		t.Fatalf("late result overwrote cancel: %s", view.Work.Runs[0].State)
	}
}

func TestPauseResumeRecovery(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-pause-recovery", 1, testWorkflow("main", "run"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-pause-recovery")

	started, release := make(chan struct{}), make(chan struct{})
	var executeCalls atomic.Int32
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		executeCalls.Add(1)
		close(started)
		<-release
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, SessionRef: SessionRef{BranchID: "pause-session"}, FinishedAt: &now}, nil
	}
	done := make(chan error, 1)
	go func() {
		_, err := f.svc.RunWork(context.Background(), value.ID, "run-pause-recovery")
		done <- err
	}()
	<-started
	view, _ := f.svc.Get(context.Background(), value.ID)
	run := view.Work.Runs[0]
	if err := f.svc.PauseRun(context.Background(), value.ID, run.ID, "pause-recovery"); err != nil {
		t.Fatalf("PauseRun: %v", err)
	}
	view, _ = f.svc.Get(context.Background(), value.ID)
	paused := view.Work.Runs[0]
	if paused.State != RunWaiting || view.Work.State != WorkPaused || paused.Pause == nil || paused.Pause.Notice != pauseRecoveryNotice {
		t.Fatalf("paused projection = %+v work=%s", paused.Pause, view.Work.State)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("paused RunWork: %v", err)
	}

	restarted := f.restart(t)
	restartedExecutor := &fakeRunnerExecutor{}
	restarted.SetTaskExecutor(restartedExecutor)
	resumed, err := restarted.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: value.ID, RunID: run.ID, RequestID: "resume-recovery",
	})
	if err != nil {
		t.Fatalf("ResumeRun after restart: %v", err)
	}
	if resumed.State != RunCompleted || executeCalls.Load() != 1 {
		t.Fatalf("resumed state=%s original execute calls=%d", resumed.State, executeCalls.Load())
	}
	if got := resumed.Stages[0].Tasks[0].Attempts[0].SessionRef.BranchID; got != "pause-session" {
		t.Fatalf("resumed context branch = %q", got)
	}
}

func TestWaitingResolutionRecovery(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-waiting-recovery", 1, testWorkflowWithGate("approval", "run", "approval"))
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-waiting-recovery")
	run, err := f.svc.RunWork(context.Background(), value.ID, "run-waiting-recovery")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	restarted := f.restart(t)
	restarted.SetTaskExecutor(&fakeRunnerExecutor{})
	resumed, err := restarted.ResumeRun(context.Background(), ResumeRunInput{
		WorkID: value.ID, RunID: run.ID, RequestID: "resume-waiting-recovery",
		GateResolutions: map[string]GateResolution{
			"approval": {Outcome: "approved", Note: "reviewed after restart"},
		},
	})
	if err != nil {
		t.Fatalf("ResumeRun: %v", err)
	}
	resolution := resumed.Stages[0].Resolution
	if resolution == nil || resolution.StageID != resumed.Stages[0].ID || resolution.Outcome != "approved" || resolution.Note != "reviewed after restart" {
		t.Fatalf("persisted gate resolution = %+v", resolution)
	}
}

func TestReceiptMissingRecoveryNeedsConfirmation(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:test-receipt-recovery", 1, testWorkflow("main", "run"))
	bp.ToolContracts = []ToolContractRef{{Name: "remote_write", ContractVersion: 1, SideEffectClass: "external_write"}}
	f.registerBlueprint(t, bp)
	value := f.createWork(t, BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1}, "create-receipt-recovery")
	var calls atomic.Int32
	f.executor.ExecuteFunc = func(context.Context, TaskExecuteInput) (*Attempt, error) {
		calls.Add(1)
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, FinishedAt: &now}, nil
	}
	run, err := f.svc.RunWork(context.Background(), value.ID, "run-receipt-recovery")
	if err != nil {
		t.Fatalf("RunWork: %v", err)
	}
	if run.State != RunNeedsConfirmation || run.Stages[0].Tasks[0].Attempts[0].State != RunNeedsConfirmation {
		t.Fatalf("missing receipt states: run=%s attempt=%s", run.State, run.Stages[0].Tasks[0].Attempts[0].State)
	}

	restarted := f.restart(t)
	restarted.SetTaskExecutor(f.executor)
	replayed, err := restarted.RunWork(context.Background(), value.ID, "run-receipt-recovery")
	if err != nil {
		t.Fatalf("idempotent recovery RunWork: %v", err)
	}
	if replayed.State != RunNeedsConfirmation || calls.Load() != 1 {
		t.Fatalf("unsafe automatic replay: state=%s calls=%d", replayed.State, calls.Load())
	}

	f.executor.ExecuteFunc = func(_ context.Context, input TaskExecuteInput) (*Attempt, error) {
		calls.Add(1)
		now := time.Now().UTC()
		return &Attempt{State: RunCompleted, FinishedAt: &now, Receipt: &AttemptReceipt{
			RequestID: input.RequestID, Outcome: "succeeded", SideEffectClass: input.SideEffectClass, ConfirmedAt: now,
		}}, nil
	}
	task := replayed.Stages[0].Tasks[0]
	attempt, err := restarted.RetryTask(context.Background(), RetryTaskInput{
		WorkID: value.ID, RunID: replayed.ID, StageID: replayed.Stages[0].ID,
		TaskID: task.ID, RequestID: "retry-receipt-confirmed",
	})
	if err != nil || attempt.State != RunCompleted || attempt.Receipt == nil {
		t.Fatalf("explicit receipt retry = (%+v, %v)", attempt, err)
	}
}

func TestReducerRejectsLateAttemptAfterCancel(t *testing.T) {
	now := time.Now().UTC()
	value := &Work{SchemaVersion: SchemaVersion, ID: "work-late", State: WorkCancelled, ArchiveState: ArchiveActive,
		Runs: []WorkflowRun{{ID: "run-late", State: RunCancelled, Stages: []Stage{{ID: "stage-late", State: RunRunning,
			Tasks: []Task{{ID: "task-late", State: RunRunning, Attempts: []Attempt{{ID: "attempt-late", State: RunRunning}}}},
		}}}},
	}
	payload, _ := json.Marshal(attemptEventPayload{RunID: "run-late", StageID: "stage-late", TaskID: "task-late",
		Attempt: Attempt{ID: "attempt-late", State: RunCompleted, FinishedAt: &now}})
	_, err := DefaultReducer()(WorkEvent{Type: EventAttemptChanged, Payload: payload, CreatedAt: now}, value)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("late attempt reducer error = %v", err)
	}
}
