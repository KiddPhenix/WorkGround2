package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/event"
)

// TestProductionAssemblyRequiresRoleModel proves the production wiring has no
// heuristic classifier or fixed generator: constructing any role without a real
// RoleModel fails, and NewAssistantRuntime wires all three roles through the
// real controller-backed role model. The legacy Runner/JobRunner are no longer
// part of the runtime.
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
	if runtime.dispatcher == nil || runtime.reflector == nil || runtime.ideator == nil {
		t.Fatal("production runtime must wire dispatcher/reflector/ideator")
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

	// The Dispatcher no longer freezes Jobs: classification records the reply and
	// kind only, leaving Jobs empty for the converged Session path.
	snapshot, err := store.Get(created.Assistant.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Jobs) != 0 {
		t.Fatalf("classification froze %d Jobs", len(snapshot.Jobs))
	}
}

func nowTest() time.Time {
	return time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
}

func TestRoleCaptureRejectsOversizedOutput(t *testing.T) {
	capture := &roleCapture{}
	capture.sink(event.Event{Kind: event.Message, Text: strings.Repeat("x", assistantRoleMaxOutputBytes+1)})
	if !capture.overflow || capture.text != "" {
		t.Fatalf("expected oversized role output to fail closed, got %+v", capture)
	}
}
