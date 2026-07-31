package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

type workChatCaptureRunner struct {
	mu      sync.Mutex
	inputs  []string
	session *agent.Session
}

func (r *workChatCaptureRunner) Run(_ context.Context, input string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inputs = append(r.inputs, input)
	if r.session != nil {
		r.session.Add(provider.Message{Role: provider.RoleUser, Content: input})
	}
	return nil
}

func (r *workChatCaptureRunner) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.inputs) == 0 {
		return ""
	}
	return r.inputs[len(r.inputs)-1]
}

func TestSendWorkChat_EmptyText(t *testing.T) {
	app := &App{}
	err := app.SendWorkChat("tab-1", "work-1", "  ", "  ")
	if err != nil {
		t.Errorf("empty text should be no-op, got: %v", err)
	}
}

func TestSendWorkChat_MissingTabID(t *testing.T) {
	app := &App{}
	err := app.SendWorkChat("", "work-1", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "tabID is required") {
		t.Errorf("expected tabID required error, got: %v", err)
	}
}

func TestSendWorkChat_MissingWorkID(t *testing.T) {
	app := &App{}
	err := app.SendWorkChat("tab-1", "", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "workID is required") {
		t.Errorf("expected workID required error, got: %v", err)
	}
}

func TestSendWorkChat_TabNotFound(t *testing.T) {
	app := &App{}
	err := app.SendWorkChat("nonexistent", "work-1", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected tab not found error, got: %v", err)
	}
}

func TestSendWorkChat_ReadOnlyTab(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"ro-tab": {ID: "ro-tab", ReadOnly: true},
	}
	app.mu.Unlock()
	err := app.SendWorkChat("ro-tab", "work-1", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("expected read-only error, got: %v", err)
	}
}

func TestSendWorkChat_NotWorkSession(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"normal-tab": {ID: "normal-tab", ReadOnly: false, sessionKind: agent.SessionKindNormal},
	}
	app.mu.Unlock()
	err := app.SendWorkChat("normal-tab", "work-1", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "not a Work Session") {
		t.Errorf("expected not Work Session error, got: %v", err)
	}
}

func TestSendWorkChat_WrongWorkIDBinding(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"work-tab": {ID: "work-tab", ReadOnly: false, sessionKind: agent.SessionKindWork, workID: "work-other"},
	}
	app.mu.Unlock()
	err := app.SendWorkChat("work-tab", "work-1", "hello", "hello")
	if err == nil || !strings.Contains(err.Error(), "bound to Work") {
		t.Errorf("expected wrong workID binding error, got: %v", err)
	}
}

func TestSendWorkChat_NoController(t *testing.T) {
	app := &App{}
	app.mu.Lock()
	app.tabs = map[string]*WorkspaceTab{
		"no-ctrl-tab": {ID: "no-ctrl-tab", ReadOnly: false, sessionKind: agent.SessionKindWork, workID: "work-1"},
	}
	app.mu.Unlock()
	err := app.SendWorkChat("no-ctrl-tab", "work-1", "hello", "hello")
	if err == nil {
		t.Errorf("expected controller unavailable error, got nil")
	}
}

func TestSendWorkChat_LoadsAuthoritativeContextAndPreservesDisplay(t *testing.T) {
	value := &work.Work{
		ID:     "work-1",
		Name:   "修复登录任务",
		Prompt: "排查登录超时",
		State:  work.WorkFailed,
	}
	store := &worktest.Store{
		LoadStateFunc: func(id, _ string) (*work.Work, work.WorkEventState, error) {
			if id != value.ID {
				t.Fatalf("LoadState id = %q, want %q", id, value.ID)
			}
			copy := *value
			return &copy, work.WorkEventState{Revision: 7}, nil
		},
	}
	session := agent.NewSession("system")
	executor := agent.New(nil, nil, session, agent.Options{}, event.Discard)
	runner := &workChatCaptureRunner{session: session}
	events := make(chan event.Event, 8)
	controller := control.New(control.Options{
		AutoPlan: "off",
		Runner:   runner,
		Executor: executor,
		Work:     work.NewService(store, work.NewBlueprintRegistry(), work.ViewSinkDiscard),
		Sink: event.FuncSink(func(value event.Event) {
			events <- value
		}),
	})
	app := &App{}
	app.setTestCtrl(controller, "test-model")
	app.tabs["test"].sessionKind = agent.SessionKindWork
	app.tabs["test"].workID = value.ID
	var display string
	controller.SetDisplayRecorder(func(_, shown string) {
		display = shown
	})

	if err := app.SendWorkChat("test", value.ID, "这个运行错误", "请定位失败原因"); err != nil {
		t.Fatalf("SendWorkChat: %v", err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case value := <-events:
			if value.Kind == event.TurnDone {
				goto done
			}
		case <-deadline:
			t.Fatal("timed out waiting for Work chat turn")
		}
	}

done:
	modelInput := runner.last()
	for _, expected := range []string{"work-1", "修复登录任务", "排查登录超时", "请定位失败原因"} {
		if !strings.Contains(modelInput, expected) {
			t.Fatalf("model input missing %q:\n%s", expected, modelInput)
		}
	}
	if display != "这个运行错误" {
		t.Fatalf("display = %q, want original text", display)
	}
	if strings.Contains(display, "work-1") {
		t.Fatalf("display leaked Work context: %q", display)
	}
}
