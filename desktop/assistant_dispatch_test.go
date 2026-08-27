package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/event"
)

// TestProductionAssemblyRequiresRoleModel proves the production wiring has no
// heuristic classifier or fixed generator: constructing any role without a real
// RoleModel fails, and NewAssistantRuntime wires all three roles (plus the job
// runner) through the real controller-backed role model.
func TestProductionAssemblyRequiresRoleModel(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := assistant.NewDispatcher(store, nil); err == nil {
		t.Fatal("NewDispatcher must reject a nil role model; no heuristic fallback exists")
	}
	if _, err := assistant.NewReflector(store, nil); err == nil {
		t.Fatal("NewReflector must reject a nil role model")
	}
	if _, err := assistant.NewIdeator(store, nil); err == nil {
		t.Fatal("NewIdeator must reject a nil role model")
	}

	runtime, err := NewAssistantRuntime(&App{}, t.TempDir())
	if err != nil {
		t.Fatalf("NewAssistantRuntime: %v", err)
	}
	if runtime.dispatcher == nil || runtime.reflector == nil || runtime.ideator == nil || runtime.jobRunner == nil {
		t.Fatal("production runtime must wire dispatcher/reflector/ideator/jobRunner")
	}
}

// TestAssistantRetryDispatchAPI proves the Wails submit entry durably opens a
// pending Dispatch without waiting for the model, and the retry entry re-runs a
// failed classification and returns the classified Dispatch.
func TestAssistantRetryDispatchAPI(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	created, err := store.Create(assistant.CreateInput{
		RequestID: "create-retry",
		Assistant: assistant.Assistant{ID: "helper-a", Name: "Helper", Mission: "keep healthy", Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy()},
		Now:       nowTest(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	calls := 0
	roleModel := assistant.RoleModelFunc(func(context.Context, string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("model unavailable")
		}
		return `{"kind":"task","reply":"ok","jobs":[{"name":"execute","kind":"task","prompt":"x"}]}`, nil
	})
	dispatcher, _ := assistant.NewDispatcher(store, roleModel)
	reflector, _ := assistant.NewReflector(store, roleModel)
	ideator, _ := assistant.NewIdeator(store, roleModel)
	service := &AssistantRuntime{store: store, dispatcher: dispatcher, reflector: reflector, ideator: ideator, wake: make(chan struct{}, 1)}
	app := &App{assistant: service}

	submitted, err := app.AssistantSubmit(AssistantSubmitRequest{AssistantID: created.Assistant.ID, RequestID: "submit-1", Input: "请扫描项目"})
	if err != nil {
		t.Fatalf("AssistantSubmit: %v", err)
	}
	if submitted.State != assistant.DispatchPendingClassification {
		t.Fatalf("expected pending_classification immediately after submit, got %s", submitted.State)
	}

	// The first classification attempt fails, leaving an explicit retryable state.
	failed, err := app.AssistantRetryDispatch(created.Assistant.ID, submitted.ID, "retry-1")
	if err != nil {
		t.Fatalf("AssistantRetryDispatch (failure): %v", err)
	}
	if failed.State != assistant.DispatchClassificationFailed {
		t.Fatalf("expected classification_failed, got %s", failed.State)
	}

	retried, err := app.AssistantRetryDispatch(created.Assistant.ID, submitted.ID, "retry-2")
	if err != nil {
		t.Fatalf("AssistantRetryDispatch (success): %v", err)
	}
	if retried.State != assistant.DispatchClassified {
		t.Fatalf("expected classified after retry, got %s", retried.State)
	}
}

func nowTest() time.Time {
	return time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
}

// TestAssistantJobRunnerBindsSessionPath covers the executeJob call chain at its
// only externally observable seam: the desktop job runner records the session
// path on a running job with the same bind call executeJob makes before the
// model turn, and the path survives a fresh snapshot read — the refresh/recovery
// path the timeline uses to render a navigable job row while running.
func TestAssistantJobRunnerBindsSessionPath(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	created, err := store.Create(assistant.CreateInput{
		RequestID: "create-bind",
		Assistant: assistant.Assistant{ID: "helper-a", Name: "Helper", Mission: "keep healthy", Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy()},
		Now:       nowTest(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	roleModel := assistant.RoleModelFunc(func(context.Context, string) (string, error) {
		return `{"kind":"task","reply":"ok","jobs":[{"name":"collect-resumes","kind":"task","prompt":"collect resumes"}]}`, nil
	})
	dispatcher, _ := assistant.NewDispatcher(store, roleModel)
	dispatch, err := dispatcher.Dispatch(context.Background(), assistant.OpenDispatchInput{
		AssistantID: created.Assistant.ID, RequestID: "submit-bind", Input: "collect resumes", Now: nowTest(),
	})
	if err != nil || dispatch.State != assistant.DispatchClassified {
		t.Fatalf("Dispatch: %+v err=%v", dispatch, err)
	}
	jobRunner, err := assistant.NewJobRunner(store, "desktop-test", time.Minute)
	if err != nil {
		t.Fatalf("NewJobRunner: %v", err)
	}
	acquired, err := jobRunner.Acquire(nowTest())
	if err != nil || acquired.Job == nil {
		t.Fatalf("Acquire: %v", err)
	}
	job := *acquired.Job
	// The same request-ID shape executeJob uses right before the model turn.
	bound, err := jobRunner.BindSession(job, fmt.Sprintf("bind-session:%s:%d", job.ID, job.LeaseFence), "sessions/collect-resumes.json", nowTest())
	if err != nil || bound.SessionPath != "sessions/collect-resumes.json" {
		t.Fatalf("BindSession: %+v err=%v", bound, err)
	}
	snapshot, err := store.Get(created.Assistant.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(snapshot.Jobs) != 1 || snapshot.Jobs[0].SessionPath != "sessions/collect-resumes.json" {
		t.Fatalf("bound session path must survive snapshot read, got %+v", snapshot.Jobs)
	}
}

func TestRoleCaptureRejectsOversizedOutput(t *testing.T) {
	capture := &roleCapture{}
	capture.sink(event.Event{Kind: event.Message, Text: strings.Repeat("x", assistantRoleMaxOutputBytes+1)})
	if !capture.overflow || capture.text != "" {
		t.Fatalf("expected oversized role output to fail closed, got %+v", capture)
	}
}

// TestRunTurnWithLeaseRenewsUntilCompletion proves a long turn keeps its lease
// alive under the current fence and completes normally.
func TestRunTurnWithLeaseRenewsUntilCompletion(t *testing.T) {
	release := make(chan struct{})
	time.AfterFunc(50*time.Millisecond, func() { close(release) })
	renews := 0
	err, lost := runTurnWithLease(context.Background(), time.Millisecond, func() error {
		renews++
		return nil
	}, func(runCtx context.Context) error {
		<-release
		if err := runCtx.Err(); err != nil {
			return err
		}
		return errors.New("turn failed")
	})
	if lost {
		t.Fatalf("turn must not be reported lease-lost while renewals succeed, err=%v", err)
	}
	if err == nil || err.Error() != "turn failed" {
		t.Fatalf("expected the turn's own error, got %v", err)
	}
	if renews < 2 {
		t.Fatalf("expected the lease to be renewed during the long turn, got %d renewals", renews)
	}
}

// TestRunTurnWithLeaseLeaseLossCancelsTurn proves that when the lease can no
// longer be renewed (the run/job was already recovered to attention), the turn
// is cancelled so no late side effect or stale completion can land, and the
// caller is told the outcome must stay unrecorded.
func TestRunTurnWithLeaseLeaseLossCancelsTurn(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	err, lost := runTurnWithLease(context.Background(), time.Millisecond, func() error {
		return assistant.ErrLeaseLost
	}, func(runCtx context.Context) error {
		close(started)
		<-runCtx.Done()
		close(cancelled)
		return runCtx.Err()
	})
	if !lost {
		t.Fatalf("expected lease loss to be reported, got err=%v lost=%v", err, lost)
	}
	if !errors.Is(err, assistant.ErrLeaseLost) {
		t.Fatalf("expected the renew error to surface, got %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("turn never started")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("lease loss must cancel the running turn")
	}
}
