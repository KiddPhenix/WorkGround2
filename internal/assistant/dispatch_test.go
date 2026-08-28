package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func roleModel(text string) RoleModel {
	return RoleModelFunc(func(context.Context, string) (string, error) { return text, nil })
}

func roleModelErr(err error) RoleModel {
	return RoleModelFunc(func(context.Context, string) (string, error) { return "", err })
}

func dispatcherJSON(kind, reply, jobs string) string {
	if jobs == "" {
		jobs = "null"
	}
	return fmt.Sprintf(`{"kind":%q,"reply":%q,"jobs":%s}`, kind, reply, jobs)
}

func taskDispatcherOutput() string {
	return dispatcherJSON("task", "收到，我来处理。", `[{"name":"execute","kind":"task","prompt":"请扫描项目最近修改并跑测试"}]`)
}

func feedbackDispatcherOutput() string {
	return dispatcherJSON("feedback", "谢谢你的反馈，已记录。", "null")
}

func reflectOutput(conclusion string) string {
	return fmt.Sprintf(`{"conclusion":%q,"evidence":["tests passed"]}`, conclusion)
}

func ideatorOutput() string {
	return `{"summary":"换个发布策略","strategy_memory":"发布前先跑一次冒烟测试","responsibility":"smoke-before-publish","objective":"发布前跑冒烟","done_criteria":"冒烟通过","next_action":"写冒烟脚本"}`
}

func mustDispatch(t *testing.T, d *Dispatcher, assistantID, requestID, input string, now time.Time) Dispatch {
	t.Helper()
	dispatch, err := d.Dispatch(context.Background(), OpenDispatchInput{AssistantID: assistantID, RequestID: requestID, Input: input, Now: now})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	return dispatch
}

func classifyTask(t *testing.T, store *Store, assistantID, requestID string, now time.Time) Dispatch {
	t.Helper()
	d, err := NewDispatcher(store, roleModel(taskDispatcherOutput()))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return mustDispatch(t, d, assistantID, requestID, "请扫描项目最近修改并跑测试", now)
}

// classifyTaskWithJob opens and classifies a Dispatch directly through the
// Store with exactly one queued Runner Job, bypassing the Dispatcher. It exists
// for JobRunner tests that still exercise the legacy frozen-Job path.
func classifyTaskWithJob(t *testing.T, store *Store, assistantID, requestID string, now time.Time) Dispatch {
	t.Helper()
	dispatch, err := store.OpenDispatch(OpenDispatchInput{AssistantID: assistantID, RequestID: requestID, Input: "请扫描项目最近修改并跑测试", Now: now})
	if err != nil {
		t.Fatalf("OpenDispatch: %v", err)
	}
	if _, err := store.ClassifyDispatch(ClassifyDispatchInput{
		AssistantID: assistantID, DispatchID: dispatch.ID, RequestID: "classify:" + requestID,
		Kind: DispatchTask, Reply: "ok",
		Jobs: []JobSpec{{Name: "execute", Kind: DispatchTask, Prompt: "do it"}},
		Now:  now,
	}); err != nil {
		t.Fatalf("ClassifyDispatch: %v", err)
	}
	return dispatch
}

// markDispatchExecuted transitions a classified Dispatch to DispatchExecuted so
// it becomes reflection-ready under the supervisor-managed Session flow.
func markDispatchExecuted(t *testing.T, store *Store, assistantID, dispatchID string, now time.Time) {
	t.Helper()
	executed, err := store.MarkDispatchExecuted(MarkDispatchExecutedInput{
		RequestID:   "executed:" + dispatchID,
		AssistantID: assistantID,
		DispatchID:  dispatchID,
		Now:         now,
	})
	if err != nil {
		t.Fatalf("MarkDispatchExecuted: %v", err)
	}
	if executed.State != DispatchExecuted {
		t.Fatalf("expected dispatch executed, got %s", executed.State)
	}
}

func finishDispatchJob(t *testing.T, store *Store, owner string, now time.Time) {
	t.Helper()
	r, err := NewJobRunner(store, owner, time.Hour)
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	acquired, err := r.Acquire(now)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if acquired.Job == nil {
		t.Fatalf("expected a claimable job")
	}
	if _, err := r.Finish(*acquired.Job, "done", now.Add(time.Minute)); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

func TestDispatcherClassifiesAndIsIdempotent(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	d, err := NewDispatcher(store, roleModel(taskDispatcherOutput()))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	now := testEpoch
	first := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目最近修改并跑测试", now)
	if first.State != DispatchClassified || first.Kind != DispatchTask {
		t.Fatalf("expected classified task, got state=%s kind=%s", first.State, first.Kind)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	jobs := jobsForDispatch(snapshot.Jobs, first.ID)
	if len(jobs) != 0 {
		t.Fatalf("expected zero jobs (dispatcher no longer freezes jobs), got %+v", jobs)
	}
	again := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目最近修改并跑测试", now.Add(time.Minute))
	if again.ID != first.ID || again.State != DispatchClassified {
		t.Fatalf("replay diverged: %s vs %s", again.ID, first.ID)
	}
	snapshot, _ = store.Get("helper-a")
	if got := len(jobsForDispatch(snapshot.Jobs, first.ID)); got != 0 {
		t.Fatalf("expected zero jobs after replay, got %d", got)
	}
}

func TestDispatcherConcurrentReplayCallsModelOnce(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	d, err := NewDispatcher(store, RoleModelFunc(func(context.Context, string) (string, error) {
		calls.Add(1)
		once.Do(func() { close(started) })
		<-release
		return taskDispatcherOutput(), nil
	}))
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	results := make(chan error, 2)
	call := func() {
		_, err := d.Dispatch(context.Background(), OpenDispatchInput{AssistantID: "helper-a", RequestID: "req-1", Input: "请扫描项目最近修改并跑测试", Now: testEpoch})
		results <- err
	}
	go call()
	<-started
	go call()
	close(release)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent replay: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected one role-model call, got %d", got)
	}
}

func TestDispatcherRejectsSameRequestIDDifferentInput(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	d, _ := NewDispatcher(store, roleModel(taskDispatcherOutput()))
	_ = mustDispatch(t, d, "helper-a", "req-1", "请扫描项目", testEpoch)
	_, err := d.Dispatch(context.Background(), OpenDispatchInput{AssistantID: "helper-a", RequestID: "req-1", Input: "不同输入", Now: testEpoch})
	if !errors.Is(err, ErrIdempotency) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestDispatcherZeroJobFeedbackInput(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	d, _ := NewDispatcher(store, roleModel(feedbackDispatcherOutput()))
	dispatch := mustDispatch(t, d, "helper-a", "req-1", "做得不错，继续保持", testEpoch)
	if dispatch.Kind != DispatchFeedback {
		t.Fatalf("expected feedback, got %s", dispatch.Kind)
	}
	snapshot, _ := store.Get("helper-a")
	if jobs := jobsForDispatch(snapshot.Jobs, dispatch.ID); len(jobs) != 0 {
		t.Fatalf("expected zero jobs for feedback, got %d", len(jobs))
	}
	if strings.TrimSpace(dispatch.Reply) == "" {
		t.Fatal("feedback dispatch must carry a user-facing reply")
	}
}

func TestDispatcherModelUnavailableKeepsInputRetryable(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	d, _ := NewDispatcher(store, roleModelErr(errors.New("model unavailable")))
	dispatch := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目", testEpoch)
	if dispatch.State != DispatchClassificationFailed {
		t.Fatalf("expected classification_failed, got %s", dispatch.State)
	}
	if dispatch.Error == nil || !dispatch.Error.Retryable {
		t.Fatalf("expected retryable error, got %+v", dispatch.Error)
	}
	if dispatch.ClassificationAttempt != 1 || !dispatch.RetryAt.Equal(testEpoch.Add(time.Minute)) {
		t.Fatalf("expected first bounded classification backoff, got attempt=%d retry_at=%s", dispatch.ClassificationAttempt, dispatch.RetryAt)
	}
	if dispatch.Input != "请扫描项目" {
		t.Fatalf("input must be preserved, got %q", dispatch.Input)
	}
}

func TestDispatcherMalformedJSONKeepsInputRetryable(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	d, _ := NewDispatcher(store, roleModel("this is not json"))
	dispatch := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目", testEpoch)
	if dispatch.State != DispatchClassificationFailed || dispatch.Error == nil || !dispatch.Error.Retryable {
		t.Fatalf("expected retryable failure for malformed JSON, got %+v", dispatch)
	}
}

// TestDispatcherRejectsMoreThanThreeJobs was removed: the Dispatcher no longer
// freezes Runner Jobs (it classifies with Jobs: nil), so rejecting a >3-job
// classification is obsolete. ParseDispatcherOutput still bounds the classifier
// job count via maxDispatcherJobs independently of the Store.

func TestDispatcherRejectsPermissionFieldInjection(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	out := `{"kind":"task","reply":"ok","policy":{"local_write":"allow"},"jobs":[{"name":"execute","kind":"task","prompt":"x"}]}`
	d, _ := NewDispatcher(store, roleModel(out))
	dispatch := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目", testEpoch)
	if dispatch.State != DispatchClassificationFailed {
		t.Fatalf("expected classification_failed for permission field injection, got %s", dispatch.State)
	}
}

func TestDispatcherRetryAfterFailure(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	calls := 0
	model := RoleModelFunc(func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("model unavailable")
		}
		return taskDispatcherOutput(), nil
	})
	d, _ := NewDispatcher(store, model)
	first := mustDispatch(t, d, "helper-a", "req-1", "请扫描项目", testEpoch)
	if first.State != DispatchClassificationFailed {
		t.Fatalf("expected classification_failed on first call, got %s", first.State)
	}
	retried, err := d.RetryDispatch(context.Background(), "helper-a", first.ID, testEpoch.Add(time.Minute))
	if err != nil || retried.State != DispatchClassified {
		t.Fatalf("expected retry to classify, got %+v err=%v", retried, err)
	}
}

func TestJobRunnerEnforcesConcurrencyCap(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	// Dispatcher rejects >3 jobs, so craft a classification with 4 jobs via a
	// store-level ClassifyDispatch using a Dispatch opened directly.
	dispatch, err := store.OpenDispatch(OpenDispatchInput{AssistantID: "helper-a", RequestID: "req-1", Input: "请扫描项目", Now: testEpoch})
	if err != nil {
		t.Fatalf("OpenDispatch: %v", err)
	}
	_, err = store.ClassifyDispatch(ClassifyDispatchInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "classify-1", Kind: DispatchTask, Reply: "ok",
		Jobs: []JobSpec{
			{Name: "a", Kind: DispatchTask, Prompt: "1"},
			{Name: "b", Kind: DispatchTask, Prompt: "2"},
			{Name: "c", Kind: DispatchTask, Prompt: "3"},
			{Name: "d", Kind: DispatchTask, Prompt: "4"},
		},
		Now: testEpoch,
	})
	if err != nil {
		t.Fatalf("ClassifyDispatch: %v", err)
	}
	r, _ := NewJobRunner(store, "owner", time.Hour)
	for i := 0; i < maxConcurrentJobs; i++ {
		acquired, err := r.Acquire(testEpoch)
		if err != nil || acquired.Job == nil {
			t.Fatalf("claim %d failed: job=%v err=%v", i, acquired.Job, err)
		}
	}
	acquired, err := r.Acquire(testEpoch)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if acquired.Job != nil {
		t.Fatalf("expected concurrency cap to block the 4th claim, got %s", acquired.Job.ID)
	}
}

func TestJobRunnerLeaseRecovery(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	_ = classifyTaskWithJob(t, store, "helper-a", "req-1", testEpoch)
	r, _ := NewJobRunner(store, "owner", time.Minute)
	acquired, err := r.Acquire(testEpoch)
	if err != nil || acquired.Job == nil {
		t.Fatalf("Acquire: %v", err)
	}
	recovered, err := store.RecoverJobs(testEpoch.Add(2 * time.Hour))
	if err != nil {
		t.Fatalf("RecoverJobs: %v", err)
	}
	if len(recovered) != 1 || recovered[0].State != JobWaitingAttention {
		t.Fatalf("expected recovered job in waiting_attention, got %+v", recovered)
	}
}

// TestJobRunnerLeaseLossRetryRecovery covers the reported dead-end: a job whose
// lease expired mid-turn is recovered to waiting_attention (outcome unknown),
// and the user retry must produce a fresh, finishable fenced execution while
// stale/late completions from the old lease can never overwrite it.
func TestJobRunnerLeaseLossRetryRecovery(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	_ = classifyTaskWithJob(t, store, "helper-a", "req-1", testEpoch)
	r, _ := NewJobRunner(store, "owner", time.Minute)
	acquired, err := r.Acquire(testEpoch)
	if err != nil || acquired.Job == nil {
		t.Fatalf("Acquire: %v", err)
	}
	first := *acquired.Job
	if first.LeaseFence != 1 || first.State != JobRunning {
		t.Fatalf("expected first claim with fence 1, got %+v", first)
	}

	// Lease expires while the turn is still running: recovery converts the job
	// to waiting_attention with an unknown outcome.
	recovered, err := store.RecoverJobs(testEpoch.Add(2 * time.Hour))
	if err != nil || len(recovered) != 1 || recovered[0].State != JobWaitingAttention {
		t.Fatalf("RecoverJobs: %+v err=%v", recovered, err)
	}

	// A late completion from the old lease must fail with ErrLeaseLost and must
	// not overwrite the recovered state.
	if _, err := r.Finish(first, "late completion", testEpoch.Add(2*time.Hour+time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Finish after recovery must fail with ErrLeaseLost, got %v", err)
	}
	if _, err := r.Fail(first, Failure{Code: "late", Message: "late failure", Retryable: true, OutcomeKnown: true, Now: testEpoch.Add(2*time.Hour + time.Minute)}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale Fail after recovery must fail with ErrLeaseLost, got %v", err)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil || len(snapshot.Jobs) != 1 || snapshot.Jobs[0].State != JobWaitingAttention {
		t.Fatalf("recovered state must survive stale completions, got %+v err=%v", snapshot.Jobs, err)
	}
	if snapshot.Jobs[0].Error == nil || snapshot.Jobs[0].Error.Code != "outcome_unknown" || snapshot.Jobs[0].Error.OutcomeKnown {
		t.Fatalf("recovered job keeps an explicit unknown-outcome error, got %+v", snapshot.Jobs[0].Error)
	}

	// Repeated retry clicks are idempotent: the same request ID is a no-op.
	retried, err := store.RetryJob(RetryJobInput{JobID: first.ID, RequestID: "retry-1", Now: testEpoch.Add(3 * time.Hour)})
	if err != nil || retried.State != JobQueued || retried.Error != nil {
		t.Fatalf("RetryJob: %+v err=%v", retried, err)
	}
	again, err := store.RetryJob(RetryJobInput{JobID: first.ID, RequestID: "retry-1", Now: testEpoch.Add(3*time.Hour + time.Minute)})
	if err != nil || again.State != JobQueued {
		t.Fatalf("idempotent RetryJob replayed: %+v err=%v", again, err)
	}

	// The retried execution claims the same job under a fresh fence and can
	// finish normally; the old fence can no longer be used.
	acquired2, err := r.Acquire(testEpoch.Add(3*time.Hour + 2*time.Minute))
	if err != nil || acquired2.Job == nil {
		t.Fatalf("Acquire after retry: %v", err)
	}
	if acquired2.Job.ID != first.ID || acquired2.Job.LeaseFence <= first.LeaseFence {
		t.Fatalf("retried claim must reuse the job with a fresh fence, got %+v (was %+v)", acquired2.Job, first)
	}
	done, err := r.Finish(*acquired2.Job, "done", testEpoch.Add(3*time.Hour+2*time.Minute+30*time.Second))
	if err != nil || done.State != JobSucceeded {
		t.Fatalf("Finish after retry: %+v err=%v", done, err)
	}
	if _, err := r.Finish(first, "old fence after retry", testEpoch.Add(3*time.Hour+3*time.Minute)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old fence must stay stale after retry claim, got %v", err)
	}
}

// TestJobRunnerBindSession proves the job session path is bound under the
// current lease fence, replays idempotently, rejects conflicting or stale
// payloads, and persists through snapshot/read.
func TestJobRunnerBindSession(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	_ = classifyTaskWithJob(t, store, "helper-a", "req-1", testEpoch)
	r, _ := NewJobRunner(store, "owner", time.Minute)
	acquired, err := r.Acquire(testEpoch)
	if err != nil || acquired.Job == nil {
		t.Fatalf("Acquire: %v", err)
	}
	job := *acquired.Job

	bound, err := r.BindSession(job, "bind-1", "sessions/job-1.json", testEpoch.Add(time.Second))
	if err != nil || bound.SessionPath != "sessions/job-1.json" {
		t.Fatalf("BindSession: %+v err=%v", bound, err)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := snapshot.Jobs[0].SessionPath; got != "sessions/job-1.json" {
		t.Fatalf("session path not persisted through snapshot, got %q", got)
	}

	// Same request replays idempotently without a second revision bump.
	again, err := r.BindSession(job, "bind-1", "sessions/job-1.json", testEpoch.Add(2*time.Second))
	if err != nil || again.SessionPath != "sessions/job-1.json" || again.Revision != bound.Revision {
		t.Fatalf("idempotent replay: %+v err=%v", again, err)
	}

	// Same request with a different path is an explicit conflict.
	if _, err := r.BindSession(job, "bind-1", "sessions/other.json", testEpoch.Add(3*time.Second)); !errors.Is(err, ErrIdempotency) {
		t.Fatalf("expected ErrIdempotency for conflicting payload, got %v", err)
	}

	// A stale fence from an older execution must be rejected and must not
	// overwrite the bound path.
	stale := job
	stale.LeaseFence = job.LeaseFence - 1
	if _, err := r.BindSession(stale, "bind-stale", "sessions/stale.json", testEpoch.Add(4*time.Second)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale fence bind must fail with ErrLeaseLost, got %v", err)
	}

	// A late bind after lease expiry must be rejected.
	if _, err := r.BindSession(job, "bind-late", "sessions/late.json", testEpoch.Add(2*time.Hour)); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("late bind must fail with ErrLeaseLost, got %v", err)
	}

	// The original bound path survives every rejected attempt.
	snapshot, _ = store.Get("helper-a")
	if got := snapshot.Jobs[0].SessionPath; got != "sessions/job-1.json" {
		t.Fatalf("session path changed after rejected binds, got %q", got)
	}
}

func TestJobRunnerFailureRetriesAndCompletes(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	_ = classifyTaskWithJob(t, store, "helper-a", "req-1", testEpoch)
	r, _ := NewJobRunner(store, "owner", time.Hour)
	acquired, _ := r.Acquire(testEpoch)
	failed, err := r.Fail(*acquired.Job, Failure{Code: "boom", Message: "failed", Retryable: true, OutcomeKnown: true, RetryAfter: time.Minute, Now: testEpoch})
	if err != nil || failed.State != JobRetryWait {
		t.Fatalf("expected retry_wait, got %+v err=%v", failed, err)
	}
	due, err := store.RetryJobsDue(testEpoch.Add(2 * time.Minute))
	if err != nil || len(due) != 1 || due[0].State != JobQueued {
		t.Fatalf("expected due job back to queued, got %+v err=%v", due, err)
	}
	acquired2, _ := r.Acquire(testEpoch.Add(3 * time.Minute))
	if acquired2.Job == nil {
		t.Fatal("expected retried job claimable")
	}
	done, err := r.Finish(*acquired2.Job, "done", testEpoch.Add(4*time.Minute))
	if err != nil || done.State != JobSucceeded {
		t.Fatalf("expected succeeded, got %+v err=%v", done, err)
	}
}

func TestReflectDispatchExactlyOnce(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)
	r, _ := NewReflector(store, roleModel(reflectOutput("done")))
	pack, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-1", testEpoch.Add(time.Hour))
	if err != nil || pack.Conclusion != "done" {
		t.Fatalf("reflect: %v %+v", err, pack)
	}
	pack2, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-2", testEpoch.Add(2*time.Hour))
	if err != nil || pack2.ID != pack.ID {
		t.Fatalf("expected same pack, got %+v err=%v", pack2, err)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.ContextPacks) != 1 {
		t.Fatalf("expected exactly one context pack, got %d", len(snapshot.ContextPacks))
	}
	if snapshot.Dispatches[0].State != DispatchReflected {
		t.Fatalf("expected reflected dispatch, got %s", snapshot.Dispatches[0].State)
	}
}

// TestReflectDispatchRejectsNonExecutedDispatch proves a classified Dispatch
// that has neither reached DispatchExecuted nor had all of its legacy frozen
// jobs terminate is refused by reflection.
func TestReflectDispatchRejectsNonExecutedDispatch(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	r, _ := NewReflector(store, roleModel(reflectOutput("done")))
	_, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-1", testEpoch)
	if !errors.Is(err, ErrTransition) {
		t.Fatalf("expected transition error for non-executed dispatch, got %v", err)
	}
}

func TestReflectorModelFailurePersistsBackoff(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)
	r, _ := NewReflector(store, roleModelErr(errors.New("model unavailable")))
	_, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-1", testEpoch.Add(time.Hour))
	if err == nil {
		t.Fatal("expected reflection model failure")
	}
	snapshot, _ := store.Get("helper-a")
	d := snapshot.Dispatches[0]
	if d.State != DispatchReflectionFailed || d.RetryAt.IsZero() || !d.RetryAt.After(testEpoch.Add(time.Hour)) {
		t.Fatalf("expected reflection_failed with backoff, got state=%s retry_at=%v", d.State, d.RetryAt)
	}
	// The supervisor retries a failed reflection after the bounded backoff by
	// re-marking the Dispatch executed (MarkDispatchExecuted accepts the
	// reflection_failed state), which makes it reflection-ready again.
	if _, err := store.MarkDispatchExecuted(MarkDispatchExecutedInput{
		RequestID: "executed-retry", AssistantID: "helper-a", DispatchID: dispatch.ID, Now: testEpoch.Add(2 * time.Hour),
	}); err != nil {
		t.Fatalf("MarkDispatchExecuted retry: %v", err)
	}
	snapshot, _ = store.Get("helper-a")
	ready := DispatchesReadyForReflection(snapshot, testEpoch.Add(2*time.Hour))
	if len(ready) != 1 || ready[0].State != DispatchExecuted {
		t.Fatalf("expected reflection to be retryable after backoff, got %+v", ready)
	}
}

func TestReflectorInvalidJSONPersistsBackoff(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)
	r, _ := NewReflector(store, roleModel("not json"))
	if _, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-1", testEpoch.Add(time.Hour)); err == nil {
		t.Fatal("expected invalid JSON reflection failure")
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Dispatches[0].State != DispatchReflectionFailed {
		t.Fatalf("expected reflection_failed, got %s", snapshot.Dispatches[0].State)
	}
}

func TestContextPackBoundedAndOwnershipFiltered(t *testing.T) {
	packA1 := ContextPack{ID: "pack-a1", AssistantID: "helper-a", DispatchID: "d1", Revision: 1, Conclusion: "a1", CreatedAt: testEpoch}
	packA2 := ContextPack{ID: "pack-a2", AssistantID: "helper-a", DispatchID: "d2", Revision: 2, Conclusion: "a2", CreatedAt: testEpoch.Add(time.Hour)}
	packB := ContextPack{ID: "pack-b", AssistantID: "helper-b", DispatchID: "d3", Revision: 1, Conclusion: "b", CreatedAt: testEpoch}
	got := ApplicableContextPacks([]ContextPack{packA1, packA2, packB}, "helper-a", "d2", 2, 1, 16)
	if len(got) != 1 || got[0].ID != "pack-a1" {
		t.Fatalf("expected only pack-a1 (revision<=1, exclude d2), got %+v", got)
	}
	gotAll := ApplicableContextPacks([]ContextPack{packA1, packA2, packB}, "helper-a", "", 0, 10, 1<<20)
	for _, p := range gotAll {
		if p.AssistantID != "helper-a" {
			t.Fatalf("cross-assistant pack leaked: %+v", p)
		}
	}
	tooLarge := ContextPack{ID: "pack-large", AssistantID: "helper-a", DispatchID: "d4", Revision: 3, Conclusion: strings.Repeat("x", 17), CreatedAt: testEpoch.Add(2 * time.Hour)}
	if got := ApplicableContextPacks([]ContextPack{packA1, tooLarge}, "helper-a", "", 0, 10, 16); len(got) != 1 || got[0].ID != "pack-a1" {
		t.Fatalf("expected oversized pack to be skipped without exceeding byte budget, got %+v", got)
	}
}

func TestStoreRejectsOverboundedContextPack(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)
	_, err := store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "reflect-1",
		Content: ContextPackContent{Conclusion: strings.Repeat("x", contextPackMaxBytes+1)},
		Now:     testEpoch.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected over-bounded context pack to be rejected")
	}
	_, err = store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "reflect-total",
		Content: ContextPackContent{
			Conclusion: "bounded fields but excessive total",
			Evidence: []string{
				strings.Repeat("a", contextPackMaxBytes),
				strings.Repeat("b", contextPackMaxBytes),
				strings.Repeat("c", contextPackMaxBytes),
			},
		},
		Now: testEpoch.Add(time.Hour),
	})
	if err == nil {
		t.Fatal("expected excessive total context pack size to be rejected")
	}
}

func TestReflectorPromptBoundsJobResults(t *testing.T) {
	snapshot := Snapshot{Assistant: Assistant{Mission: "mission"}}
	jobs := []RunnerJob{
		{Name: "a", State: JobSucceeded, Summary: strings.Repeat("一", roleMaxJobResultBytes)},
		{Name: "b", State: JobSucceeded, Summary: strings.Repeat("二", roleMaxJobResultBytes)},
	}
	prompt := ReflectorPrompt(snapshot, Dispatch{Kind: DispatchTask, Input: "input", Reply: "reply"}, jobs)
	if len(prompt) > roleMaxJobResultBytes+4096 {
		t.Fatalf("reflector prompt exceeded bounded job-result budget: %d", len(prompt))
	}
	if !utf8.ValidString(prompt) {
		t.Fatal("bounded reflector prompt is not valid UTF-8")
	}
}

func TestIdeationCadenceFiveSuccessfulTasks(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	due, _, _, err := store.ShouldIdeate("helper-a", testEpoch)
	if err != nil || due {
		t.Fatalf("expected not due at start, got due=%v err=%v", due, err)
	}
	for i := 0; i < ideaCadenceSuccessfulTasks; i++ {
		// The ideation cadence counts "successful tasks" via succeeded frozen
		// Jobs (successfulTaskDispatchesSince), so exercise the legacy job path.
		dispatch := classifyTaskWithJob(t, store, "helper-a", "req-"+string(rune('a'+i)), testEpoch.Add(time.Duration(i)*time.Minute))
		finishDispatchJob(t, store, "owner", testEpoch.Add(time.Duration(i)*time.Minute))
		r, _ := NewReflector(store, roleModel(reflectOutput("done")))
		if _, err := r.Reflect(context.Background(), "helper-a", dispatch.ID, "reflect-"+string(rune('a'+i)), testEpoch.Add(time.Hour+time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("reflect %d: %v", i, err)
		}
	}
	due, reason, count, err := store.ShouldIdeate("helper-a", testEpoch.Add(2*time.Hour))
	if err != nil || !due || count < ideaCadenceSuccessfulTasks {
		t.Fatalf("expected due after %d tasks, got due=%v reason=%s count=%d err=%v", ideaCadenceSuccessfulTasks, due, reason, count, err)
	}
}

func TestIdeationCadenceSevenDays(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	due, reason, _, err := store.ShouldIdeate("helper-a", testEpoch.Add(ideaCadenceInterval))
	if err != nil || !due || reason != "interval" {
		t.Fatalf("expected interval ideation due, got due=%v reason=%s err=%v", due, reason, err)
	}
	if _, err := store.OpenIdea(OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-1", Trigger: IdeaTriggerManual, Summary: "reset", Now: testEpoch.Add(ideaCadenceInterval)}); err != nil {
		t.Fatalf("OpenIdea: %v", err)
	}
	due, _, _, _ = store.ShouldIdeate("helper-a", testEpoch.Add(ideaCadenceInterval+time.Hour))
	if due {
		t.Fatal("expected interval baseline reset after manual idea")
	}
}

func TestIdeatorCadenceGatedAndManualAllowed(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	id, _ := NewIdeator(store, roleModel(ideatorOutput()))
	if _, err := id.Ideate(context.Background(), OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-cadence", Trigger: IdeaTriggerCadence, Now: testEpoch}); !errors.Is(err, ErrTransition) {
		t.Fatalf("expected cadence gating, got %v", err)
	}
	if _, err := id.Ideate(context.Background(), OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-manual", Trigger: IdeaTriggerManual, Now: testEpoch}); err != nil {
		t.Fatalf("manual ideate: %v", err)
	}
}

func TestIdeatorReplaySkipsModelAndRejectsTriggerConflict(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	var calls atomic.Int32
	id, _ := NewIdeator(store, RoleModelFunc(func(context.Context, string) (string, error) {
		if calls.Add(1) == 1 {
			return ideatorOutput(), nil
		}
		return `{"summary":"different nondeterministic output"}`, nil
	}))
	in := OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-replay", Trigger: IdeaTriggerManual, Now: testEpoch}
	first, err := id.Ideate(context.Background(), in)
	if err != nil {
		t.Fatalf("first Ideate: %v", err)
	}
	second, err := id.Ideate(context.Background(), in)
	if err != nil {
		t.Fatalf("replayed Ideate: %v", err)
	}
	if second.ID != first.ID || second.Summary != first.Summary || calls.Load() != 1 {
		t.Fatalf("replay = %+v, first = %+v, model calls = %d", second, first, calls.Load())
	}
	_, err = id.Ideate(context.Background(), OpenIdeaInput{
		AssistantID: "helper-a", RequestID: in.RequestID, Trigger: IdeaTriggerCadence, Now: testEpoch,
	})
	if !errors.Is(err, ErrIdempotency) || calls.Load() != 1 {
		t.Fatalf("trigger conflict err = %v, model calls = %d", err, calls.Load())
	}
}

func TestIdeatorCadenceFailurePersistsBackoff(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	id, _ := NewIdeator(store, roleModelErr(errors.New("model unavailable")))
	_, err := id.Ideate(context.Background(), OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-cadence", Trigger: IdeaTriggerCadence, Now: testEpoch.Add(ideaCadenceInterval)})
	if err == nil {
		t.Fatal("expected cadence ideation model failure")
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Ideation.RetryAt.IsZero() || !snapshot.Ideation.RetryAt.After(testEpoch.Add(ideaCadenceInterval)) {
		t.Fatalf("expected ideation backoff, got %+v", snapshot.Ideation)
	}
	due, _, _, _ := store.ShouldIdeate("helper-a", testEpoch.Add(ideaCadenceInterval))
	if due {
		t.Fatal("expected cadence suppressed by backoff")
	}
}

func TestResolveIdeaCASAndNoPermissionEscalation(t *testing.T) {
	store := testStore(t, t.TempDir())
	created := mustCreate(t, store, "helper-a")
	idea, err := store.OpenIdea(OpenIdeaInput{
		AssistantID: "helper-a", RequestID: "idea-1", Trigger: IdeaTriggerManual,
		Summary: "换个发布策略", StrategyMemory: "发布前先跑一次冒烟测试", Responsibility: "smoke-before-publish",
		Objective: "发布前跑冒烟", DoneCriteria: "冒烟通过", NextAction: "写冒烟脚本", Now: testEpoch,
	})
	if err != nil {
		t.Fatalf("OpenIdea: %v", err)
	}
	if _, err := store.ResolveIdea(ResolveIdeaInput{AssistantID: "helper-a", IdeaID: idea.ID, RequestID: "resolve-stale", ExpectedRevision: idea.Revision + 1, Decision: IdeaAccept, Now: testEpoch}); !errors.Is(err, ErrConflict) {
		t.Fatalf("expected CAS conflict, got %v", err)
	}
	resolved, err := store.ResolveIdea(ResolveIdeaInput{AssistantID: "helper-a", IdeaID: idea.ID, RequestID: "resolve-1", ExpectedRevision: idea.Revision, Decision: IdeaAccept, Resolution: "ok", Now: testEpoch})
	if err != nil || resolved.State != IdeaAccepted {
		t.Fatalf("accept: %v %+v", err, resolved)
	}
	snapshot, _ := store.Get("helper-a")
	foundStrategy := false
	for _, m := range snapshot.Memory.Items {
		if m.Kind == MemoryStrategy && strings.Contains(m.Body, "冒烟测试") {
			foundStrategy = true
		}
	}
	if !foundStrategy {
		t.Fatal("expected strategy memory written on accept")
	}
	foundResp := false
	for _, resp := range snapshot.Plan.Responsibilities {
		if resp.Alias == "smoke-before-publish" {
			foundResp = true
		}
	}
	if !foundResp {
		t.Fatal("expected responsibility candidate written on accept")
	}
	if snapshot.Assistant.Policy != created.Assistant.Policy {
		t.Fatalf("policy escalated: %+v", snapshot.Assistant.Policy)
	}
	if snapshot.Assistant.Mission != created.Assistant.Mission {
		t.Fatalf("mission changed: %q", snapshot.Assistant.Mission)
	}
	if snapshot.Assistant.WorkspaceRoot != created.Assistant.WorkspaceRoot {
		t.Fatalf("workspace changed: %q", snapshot.Assistant.WorkspaceRoot)
	}
}

func TestResolveIdeaRejectClosesWithoutSideEffects(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	idea, _ := store.OpenIdea(OpenIdeaInput{AssistantID: "helper-a", RequestID: "idea-1", Trigger: IdeaTriggerManual, Summary: "idea", StrategyMemory: "strategy", Now: testEpoch})
	resolved, err := store.ResolveIdea(ResolveIdeaInput{AssistantID: "helper-a", IdeaID: idea.ID, RequestID: "resolve-1", ExpectedRevision: idea.Revision, Decision: IdeaReject, Resolution: "no", Now: testEpoch})
	if err != nil || resolved.State != IdeaRejected {
		t.Fatalf("reject: %v %+v", err, resolved)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Memory.Items) != 0 {
		t.Fatalf("reject must not write memory, got %d", len(snapshot.Memory.Items))
	}
	if len(snapshot.Plan.Responsibilities) != 0 {
		t.Fatalf("reject must not write responsibilities, got %d", len(snapshot.Plan.Responsibilities))
	}
}

func TestResolveIdeaSupersedesConflictingResponsibilityWithoutPartialMemory(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	first, err := store.OpenIdea(OpenIdeaInput{
		AssistantID: "helper-a", RequestID: "idea-first", Trigger: IdeaTriggerManual,
		Summary: "first", StrategyMemory: "must not be written", Responsibility: "shared-alias",
		Objective: "first objective", Now: testEpoch,
	})
	if err != nil {
		t.Fatalf("OpenIdea first: %v", err)
	}
	second, err := store.OpenIdea(OpenIdeaInput{
		AssistantID: "helper-a", RequestID: "idea-second", Trigger: IdeaTriggerManual,
		Summary: "second", Responsibility: "shared-alias", Objective: "second objective", Now: testEpoch,
	})
	if err != nil {
		t.Fatalf("OpenIdea second: %v", err)
	}
	if _, err := store.ResolveIdea(ResolveIdeaInput{AssistantID: "helper-a", IdeaID: second.ID, RequestID: "resolve-second", ExpectedRevision: second.Revision, Decision: IdeaAccept, Now: testEpoch}); err != nil {
		t.Fatalf("accept second: %v", err)
	}
	resolved, err := store.ResolveIdea(ResolveIdeaInput{AssistantID: "helper-a", IdeaID: first.ID, RequestID: "resolve-first", ExpectedRevision: first.Revision, Decision: IdeaAccept, Now: testEpoch})
	if err != nil || resolved.State != IdeaSuperseded {
		t.Fatalf("expected superseded conflict, got err=%v idea=%+v", err, resolved)
	}
	snapshot, err := store.Get("helper-a")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(snapshot.Plan.Responsibilities) != 1 {
		t.Fatalf("expected one responsibility, got %d", len(snapshot.Plan.Responsibilities))
	}
	for _, item := range snapshot.Memory.Items {
		if item.Body == "must not be written" {
			t.Fatal("superseded idea applied partial strategy memory")
		}
	}
}

func TestRestartRecoveryPreservesDispatch(t *testing.T) {
	root := t.TempDir()
	store := testStore(t, root)
	mustCreate(t, store, "helper-a")
	_ = classifyTask(t, store, "helper-a", "req-1", testEpoch)
	reopened := testStore(t, root)
	snapshot, err := reopened.Get("helper-a")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if len(snapshot.Dispatches) != 1 || len(snapshot.Jobs) != 0 {
		t.Fatalf("expected one dispatch and zero jobs after reopen, got %d dispatches %d jobs", len(snapshot.Dispatches), len(snapshot.Jobs))
	}
	if snapshot.Dispatches[0].State != DispatchClassified {
		t.Fatalf("unexpected state after reopen: %+v", snapshot.Dispatches[0])
	}
}

func TestParseDispatcherOutputStrict(t *testing.T) {
	if _, err := ParseDispatcherOutput(taskDispatcherOutput()); err != nil {
		t.Fatalf("valid dispatcher output rejected: %v", err)
	}
	if _, err := ParseDispatcherOutput(`{"kind":"task","reply":"ok","jobs":[{"name":"a","kind":"task","prompt":"x"},{"name":"a","kind":"task","prompt":"y"}]}`); err == nil {
		t.Fatal("expected duplicate job names to be rejected")
	}
	if _, err := ParseDispatcherOutput(`{"kind":"task","reply":"ok","unknown_field":true}`); err == nil {
		t.Fatal("expected unknown field to be rejected")
	}
	if _, err := ParseDispatcherOutput(`{"kind":"task","reply":"ok","workspace_root":"/etc"}`); err == nil {
		t.Fatal("expected workspace field to be rejected")
	}
}

func TestParseIdeatorOutputRejectsPermissionField(t *testing.T) {
	if _, err := ParseIdeatorOutput(ideatorOutput()); err != nil {
		t.Fatalf("valid ideator output rejected: %v", err)
	}
	if _, err := ParseIdeatorOutput(`{"summary":"x","policy":{"local_write":"allow"}}`); err == nil {
		t.Fatal("expected ideator policy field to be rejected")
	}
	if _, err := ParseIdeatorOutput(`{"summary":"x","responsibility":"bad alias!"}`); err == nil {
		t.Fatal("expected invalid responsibility alias to be rejected")
	}
}

func TestRoleBackoffBounded(t *testing.T) {
	for i := 0; i < 100; i++ {
		if d := roleBackoff(i); d <= 0 || d > 30*time.Minute {
			t.Fatalf("backoff %d out of bounds: %v", i, d)
		}
	}
}

func TestReflectDispatchConvergesOnExistingPackSameRequest(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)

	first, err := store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "reflect-1",
		Content: ContextPackContent{Conclusion: "first"},
		Now:     testEpoch.Add(time.Hour),
	})
	if err != nil || first.Conclusion != "first" {
		t.Fatalf("first reflect: %v %+v", err, first)
	}

	second, err := store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "reflect-1",
		Content: ContextPackContent{Conclusion: "second"},
		Now:     testEpoch.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("duplicate reflect with different content: %v", err)
	}
	if second.ID != first.ID || second.Conclusion != "first" {
		t.Fatalf("expected existing pack %+v, got %+v", first, second)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.ContextPacks) != 1 {
		t.Fatalf("expected exactly one context pack, got %d", len(snapshot.ContextPacks))
	}
}

func TestReflectDispatchSameRequestDifferentDispatchConflicts(t *testing.T) {
	store := testStore(t, t.TempDir())
	mustCreate(t, store, "helper-a")
	dispatch := classifyTask(t, store, "helper-a", "req-1", testEpoch)
	markDispatchExecuted(t, store, "helper-a", dispatch.ID, testEpoch)
	if _, err := store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "reflect-1",
		Content: ContextPackContent{Conclusion: "done"},
		Now:     testEpoch.Add(time.Hour),
	}); err != nil {
		t.Fatalf("first reflect: %v", err)
	}

	other := classifyTask(t, store, "helper-a", "req-2", testEpoch.Add(30*time.Minute))
	markDispatchExecuted(t, store, "helper-a", other.ID, testEpoch.Add(30*time.Minute))
	_, err := store.ReflectDispatch(ReflectInput{
		AssistantID: "helper-a", DispatchID: other.ID, RequestID: "reflect-1",
		Content: ContextPackContent{Conclusion: "other"},
		Now:     testEpoch.Add(2 * time.Hour),
	})
	if !errors.Is(err, ErrIdempotency) {
		t.Fatalf("expected ErrIdempotency for reused request id on different dispatch, got %v", err)
	}
}

func TestIdeatorPromptStatesResponsibilityAliasGrammar(t *testing.T) {
	snapshot := Snapshot{Assistant: Assistant{Mission: "mission"}}
	prompt := IdeatorPrompt(snapshot, IdeaTriggerManual)
	for _, want := range []string{"1..64", "ASCII", "下划线", "连字符", "smoke-before-publish", "留空"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("ideator prompt missing alias grammar %q:\n%s", want, prompt)
		}
	}
}
