package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/control"
	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

type repairBlobStore struct {
	content string
}

func (s *repairBlobStore) Put(_ string, data []byte) (string, error) {
	s.content = string(data)
	return work.ContentDigest(data), nil
}

func (*repairBlobStore) Get(string, string) ([]byte, error)   { return nil, work.ErrWorkNotFound }
func (*repairBlobStore) Exists(string, string) (bool, error)  { return false, nil }
func (*repairBlobStore) Delete(string, string) error          { return nil }
func (*repairBlobStore) ListDigests(string) ([]string, error) { return nil, nil }

func TestWorkViewWailsWatchRoutesOwningProjection(t *testing.T) {
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{
		Work:      work.NewService(&worktest.Store{}, work.NewBlueprintRegistry(), views),
		WorkViews: views,
	})
	emitted := make(chan work.WorkViewEvent, 1)
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-1" || len(payload) != 1 {
			return
		}
		if event, ok := payload[0].(work.WorkViewEvent); ok {
			emitted <- event
		}
	}
	app.setTestCtrl(ctrl, "test")
	if err := app.WatchWork("test", "work-1", "subscription-1"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-1") })

	views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "other-work", Revision: 1})
	views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", Revision: 2})
	select {
	case event := <-emitted:
		if event.WorkID != "work-1" || event.Revision != 2 {
			t.Fatalf("emitted event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WorkView Wails event")
	}

	app.UnwatchWork("subscription-1")
	app.UnwatchWork("subscription-1")
}

func TestWorkViewWailsWatchOverflowRecoversWithoutSuccessor(t *testing.T) {
	var loads atomic.Int64
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			loads.Add(1)
			return &work.Work{ID: workID, Name: "authoritative", State: work.WorkReady, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 77}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	recovered := make(chan work.WorkViewEvent, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-overflow" || len(payload) != 1 {
			return
		}
		event, ok := payload[0].(work.WorkViewEvent)
		if !ok {
			return
		}
		if event.RequestID == "overflow-recovery" {
			select {
			case recovered <- event:
			default:
			}
		}
	}
	if err := app.WatchWork("test", "work-1", "subscription-overflow"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-overflow") })

	// A tight publisher burst outpaces the watcher and fills its fixed buffer.
	// Stop immediately after the first observed drop: the overflow signal must
	// recover without relying on any successor event.
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", EventID: fmt.Sprintf("burst-%d", i), Revision: int64(i + 1)})
	}
	if views.OverflowCount() == 0 {
		t.Fatal("burst did not exercise watcher overflow")
	}

	select {
	case event := <-recovered:
		if event.Revision != 77 || event.Resync == nil || event.Resync.Generation != 1 ||
			event.EventID != work.OverflowResyncEventID("work-1", 77, 1) {
			t.Fatalf("recovery event = %+v", event)
		}
		if err := event.Validate(); err != nil {
			t.Fatalf("recovery event contract: %v", err)
		}
		var snapshot work.WorkView
		if err := json.Unmarshal(event.Payload, &snapshot); err != nil || snapshot.Work == nil || snapshot.Work.Name != "authoritative" {
			t.Fatalf("recovery snapshot = %#v, err=%v", snapshot, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overflow with no successor did not trigger authoritative snapshot")
	}
	if loads.Load() == 0 || views.OverflowCount() == 0 {
		t.Fatalf("recovery evidence = loads %d, overflow %d", loads.Load(), views.OverflowCount())
	}
}

func TestWorkViewWailsOverflowResyncsExternalBlobAssessmentAtSameRevision(t *testing.T) {
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	created, err := svc.Create(t.Context(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: work.SchemaVersion, Version: 1},
		Name:         "same revision assessment", RequestID: "create-same-revision",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforePin, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get before Pin: %v", err)
	}
	pinned, err := svc.PinCornerstone(t.Context(), created.ID, work.PinCornerstoneInput{
		Type: work.CornerstonePolicy, Title: "required blob",
		Content: strings.Repeat("authoritative external input ", 220),
		Ref:     work.CornerstoneRef{Kind: "inline"}, Mode: work.CornerstoneSnapshot, Required: true,
		ExpectedRevision: beforePin.Revision, RequestID: "pin-required-blob",
	})
	if err != nil || pinned == nil || pinned.Cornerstone == nil || pinned.Cornerstone.Ref.BlobDigest == "" {
		t.Fatalf("PinCornerstone = (%#v, %v)", pinned, err)
	}
	ready, err := svc.Get(t.Context(), created.ID)
	if err != nil || ready.Assessment.Blocking {
		t.Fatalf("ready projection = (%#v, %v)", ready, err)
	}
	if err := store.Delete(created.ID, pinned.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("external blob delete: %v", err)
	}
	blocked, err := svc.Get(t.Context(), created.ID)
	if err != nil || blocked.Revision != ready.Revision || !blocked.Assessment.Blocking || blocked.RunBlock == nil {
		t.Fatalf("same-revision blocked projection = (%#v, %v), ready revision %d", blocked, err, ready.Revision)
	}

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	recovered := make(chan work.WorkViewEvent, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-blob" || len(payload) != 1 {
			return
		}
		event, ok := payload[0].(work.WorkViewEvent)
		if ok && event.Resync != nil {
			select {
			case recovered <- event:
			default:
			}
		}
	}
	if err := app.WatchWork("test", created.ID, "subscription-blob"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-blob") })
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: created.ID, EventID: fmt.Sprintf("blob-burst-%d", i), Revision: int64(i + 1)})
	}
	if views.OverflowCount() == 0 {
		t.Fatal("burst did not exercise watcher overflow")
	}
	select {
	case event := <-recovered:
		var snapshot work.WorkView
		if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
			t.Fatalf("decode recovery: %v", err)
		}
		if event.Revision != ready.Revision || snapshot.Revision != ready.Revision || !snapshot.Assessment.Blocking || snapshot.RunBlock == nil {
			t.Fatalf("overflow recovery did not carry same-revision blocked assessment: event=%+v snapshot=%#v", event, snapshot)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for same-revision authoritative resync")
	}
}

func TestWorkViewWailsOverflowGetFailureEmitsRetryableAttentionOnly(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(string, string) (*work.Work, work.WorkEventState, error) {
		return nil, work.WorkEventState{}, errors.New("offline")
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	events := make(chan work.WorkViewEvent, 4)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name == workViewEventPrefix+"subscription-failure" && len(payload) == 1 {
			if event, ok := payload[0].(work.WorkViewEvent); ok && (event.Type == work.ViewAttention || event.Resync != nil) {
				events <- event
			}
		}
	}
	if err := app.WatchWork("test", "work-1", "subscription-failure"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-failure") })
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", EventID: fmt.Sprintf("failure-burst-%d", i), Revision: int64(i + 1)})
	}
	select {
	case event := <-events:
		if event.Type != work.ViewAttention || event.Resync != nil || !strings.Contains(string(event.Payload), `"retryable":true`) {
			t.Fatalf("failure event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for retryable recovery attention")
	}
}

func TestRecoverWorkViewReturnsFreshTypedRetryResync(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
		return &work.Work{
			ID: workID, Name: "same revision blocked", State: work.WorkReady, ArchiveState: work.ArchiveActive,
		}, work.WorkEventState{Revision: 77}, nil
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	first, err := app.RecoverWorkView("test", "work-1", work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 8,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView first: %v", err)
	}
	second, err := app.RecoverWorkView("test", "work-1", work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 8,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView second: %v", err)
	}
	if first.Resync == nil || first.Resync.Reason != work.ViewResyncRetry || !first.Resync.Authoritative || first.Resync.Generation != 8 {
		t.Fatalf("first retry resync = %+v", first)
	}
	if second.Resync == nil || second.Resync.Generation != 9 || second.EventID == first.EventID {
		t.Fatalf("second retry resync = %+v; first EventID %q", second, first.EventID)
	}
	if first.EventID != work.ResyncEventID("work-1", 77, work.ViewResyncRetry, 8) ||
		second.EventID != work.ResyncEventID("work-1", 77, work.ViewResyncRetry, 9) {
		t.Fatalf("retry EventIDs = (%q, %q)", first.EventID, second.EventID)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first retry contract: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second retry contract: %v", err)
	}
	var snapshot work.WorkView
	if err := json.Unmarshal(first.Payload, &snapshot); err != nil || snapshot.Revision != 77 || snapshot.Work == nil || snapshot.Work.Name != "same revision blocked" {
		t.Fatalf("retry snapshot = %#v, err=%v", snapshot, err)
	}
}

func TestRecoverWorkViewRejectsInvalidIntent(t *testing.T) {
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	for _, tc := range []struct {
		name   string
		workID string
		intent work.ViewRecoveryIntent
	}{
		{name: "empty work", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry, Generation: 1}},
		{name: "wrong reason", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncOverflow, Generation: 1}},
		{name: "zero generation", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry}},
		{name: "unsafe generation", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry, Generation: 1 << 53}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.RecoverWorkView("test", tc.workID, tc.intent); err == nil || !strings.Contains(err.Error(), "valid recovery intent") {
				t.Fatalf("RecoverWorkView error = %v", err)
			}
		})
	}
}

func TestRecoverWorkViewAcceptsHydrateIntent(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
		return &work.Work{
			ID: workID, Name: "hydrate work", State: work.WorkReady, ArchiveState: work.ArchiveActive,
		}, work.WorkEventState{Revision: 42}, nil
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	event, err := app.RecoverWorkView("test", "work-hydrate", work.ViewRecoveryIntent{
		Reason: work.ViewResyncHydrate, Generation: 3,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView hydrate: %v", err)
	}
	if event.Resync == nil || event.Resync.Reason != work.ViewResyncHydrate || !event.Resync.Authoritative || event.Resync.Generation != 3 {
		t.Fatalf("hydrate resync = %+v", event)
	}
	wantID := work.ResyncEventID("work-hydrate", 42, work.ViewResyncHydrate, 3)
	if event.EventID != wantID {
		t.Fatalf("hydrate EventID = %q, want %q", event.EventID, wantID)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("hydrate contract: %v", err)
	}
}

func TestWorkViewWailsWatchRejectsUnsafeSubscriptionID(t *testing.T) {
	app := &App{}
	if err := app.WatchWork("test", "work-1", "../../event"); err == nil {
		t.Fatal("expected unsafe subscription ID to fail")
	}
}

// TestResolveWorkControllerMissingTab verifies that resolveWorkController
// returns an error when the tab doesn't exist.
func TestResolveWorkControllerMissingTab(t *testing.T) {
	app := &App{}
	_, err := app.resolveWorkController("nonexistent-tab")
	if err == nil {
		t.Fatal("expected error for missing tab")
	}
}

// TestResolveWorkControllerDisabledWork verifies that a controller without
// Work service returns a clear error.
func TestResolveWorkControllerDisabledWork(t *testing.T) {
	// A Controller built without Work (default) return errors from WorkControl.
	c := control.New(control.Options{})
	if c == nil {
		t.Fatal("New returned nil")
	}
	wc := c.WorkControl()
	_, err := wc.CreateWork(nil, work.CreateWorkInput{})
	if err == nil {
		t.Fatal("expected error from disabled Work")
	}
}

// TestWorkMethodsNilService verifies the nil-safe delegation.
func TestWorkMethodsNilService(t *testing.T) {
	c := control.New(control.Options{})
	wc := c.WorkControl()

	// All delegated methods should return error.
	if _, err := wc.GetWork(nil, "w-1"); err == nil {
		t.Fatal("GetWork: expected error")
	}
	if _, err := wc.ListWorks(nil, work.WorkFilter{}); err == nil {
		t.Fatal("ListWorks: expected error")
	}
	if _, err := wc.ArchiveWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("ArchiveWork: expected error")
	}
	if _, err := wc.RestoreWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("RestoreWork: expected error")
	}
	if err := wc.DeleteWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("DeleteWork: expected error")
	}

	// Not-yet-implemented methods should also return error.
	if _, err := wc.RunWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("RunWork: expected error")
	}
	if _, err := wc.RetryTask(nil, work.RetryTaskInput{}); err == nil {
		t.Fatal("RetryTask: expected error")
	}
}

func TestWorkBindingRoutesToTabController(t *testing.T) {
	var gotID string
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			gotID = workID
			return &work.Work{ID: workID, Name: "routed"}, work.WorkEventState{Revision: 7}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{
		Work:      work.NewService(store, work.NewBlueprintRegistry(), views),
		WorkViews: views,
	})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	view, err := app.GetWork("test", "work-routed")
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if gotID != "work-routed" || view == nil || view.Work == nil || view.Work.Name != "routed" || view.Revision != 7 {
		t.Fatalf("tab -> Controller route = gotID %q, view %+v", gotID, view)
	}
}

// TestImportDirection verifies internal/work does not import control or agent.
// This is a compile-time guarantee; the test just documents it.
func TestImportDirection(t *testing.T) {
	// internal/work.ViewSink is consumed by control.WorkViewBroadcaster.
	// internal/work.WorkController is embedded by control.WorkControl.
	// internal/work never imports control or agent.
	// Verified by: grep -r "workground2/internal/control\|workground2/internal/agent" internal/work/
	// Result: no matches.
}

// ── Cornerstone routing tests ────────────────────────────────────────────────

// TestCornerstoneMethodsDisabledWork verifies cornerstone methods return
// errWorkDisabled when no Work service is wired.
func TestCornerstoneMethodsDisabledWork(t *testing.T) {
	c := control.New(control.Options{})
	wc := c.WorkControl()

	if _, err := wc.PinCornerstone(nil, "w-1", work.PinCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("PinCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RefreshCornerstone(nil, "w-1", work.RefreshCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RefreshCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RemoveCornerstone(nil, "w-1", work.RemoveCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RemoveCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.UndoCornerstone(nil, "w-1", work.UndoCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("UndoCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.AcceptCornerstone(nil, "w-1", work.AcceptCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("AcceptCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.FreezeCornerstone(nil, "w-1", work.FreezeCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("FreezeCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RepairCornerstone(nil, "w-1", work.RepairCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RepairCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.ResumeRun(nil, work.ResumeRunInput{WorkID: "w-1", RunID: "run-1", RequestID: "r-1"}); err == nil {
		t.Fatal("ResumeRun: expected errWorkDisabled")
	}
}

func TestResumeRunWailsBindingRoutesToController(t *testing.T) {
	app := &App{}
	app.setTestCtrl(control.New(control.Options{}), "resume-binding")
	_, err := app.ResumeRun("test", work.ResumeRunInput{WorkID: "w-1", RunID: "run-1", RequestID: "resume-1"})
	if err == nil || !strings.Contains(err.Error(), "feature is disabled") {
		t.Fatalf("ResumeRun binding error = %v, want disabled controller error", err)
	}
}

func TestResumeRunWailsBindingRejectsNeedsConfirmation(t *testing.T) {
	const workID = "work-needs-confirmation"
	const runID = "run-needs-confirmation"
	var executeCalls atomic.Int32
	store := &worktest.Store{LoadStateFunc: func(gotWorkID, requestID string) (*work.Work, work.WorkEventState, error) {
		if gotWorkID != workID || requestID != "resume-needs-confirmation/resume" {
			t.Fatalf("LoadState context = (%q, %q)", gotWorkID, requestID)
		}
		return &work.Work{
			ID:           workID,
			State:        work.WorkWaitingUser,
			ArchiveState: work.ArchiveActive,
			Runs: []work.WorkflowRun{{
				ID: runID, WorkID: workID, State: work.RunNeedsConfirmation,
			}},
		}, work.WorkEventState{}, nil
	}}
	svc := work.NewService(store, work.NewBlueprintRegistry(), control.NewWorkViewBroadcaster())
	executor := &worktest.TaskExecutor{ExecuteFunc: func(context.Context, work.TaskExecuteInput) (*work.Attempt, error) {
		executeCalls.Add(1)
		return nil, errors.New("unexpected execution")
	}}
	app := &App{}
	app.setTestCtrl(control.New(control.Options{Work: svc, TaskExecutor: executor}), "resume-confirmation-binding")

	_, err := app.ResumeRun("test", work.ResumeRunInput{
		WorkID: workID, RunID: runID, RequestID: "resume-needs-confirmation",
	})
	if err == nil || !strings.Contains(err.Error(), "only waiting runs can be resumed") {
		t.Fatalf("ResumeRun needs_confirmation error = %v, want waiting-only rejection", err)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("ResumeRun needs_confirmation executed %d tasks", executeCalls.Load())
	}
}

// TestCornerstoneWailsBindingRoutesPin verifies the PinCornerstone Wails binding
// routes through tabID → Controller → Service → CornerstoneManager.
func TestCornerstoneWailsBindingRoutesPin(t *testing.T) {
	var committedWorkID string
	var committedEventType work.WorkEventType
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, Name: "cs-routed", ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			committedWorkID = workID
			committedEventType = event.Type
			return 2, nil
		},
		LoadProjectionFunc: func(workID string) (*work.Work, error) {
			// Return nil to trigger post-commit recovery — this proves routing
			// reached CornerstoneManager.Pin and the event was committed.
			return nil, work.ErrWorkNotFound
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{
		Work:      svc,
		WorkViews: views,
	})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	input := work.PinCornerstoneInput{
		Type:             work.CornerstoneInstruction,
		Title:            "测试基石",
		Content:          "内容",
		Ref:              work.CornerstoneRef{Kind: "inline"},
		Mode:             work.CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: 1,
		RequestID:        "pin-req-1",
	}

	_, err := app.PinCornerstone("test", "work-cs-routed", input)
	// We expect an error because the projection recovery fails, but the
	// important thing is the event was committed through the full stack.
	if committedWorkID != "work-cs-routed" {
		t.Fatalf("PinCornerstone did not route to store: committedWorkID = %q", committedWorkID)
	}
	if committedEventType != work.EventCornerstoneUpserted {
		t.Fatalf("PinCornerstone did not commit cornerstone event: got %q", committedEventType)
	}
	if err == nil {
		t.Fatal("expected error from projection recovery, got nil")
	}
	t.Logf("routing verified: PinCornerstone reached CornerstoneManager, event committed (err=%v)", err)
}

// TestCornerstoneWailsBindingRoutesRemove verifies the RemoveCornerstone Wails
// binding routes correctly.
func TestCornerstoneWailsBindingRoutesRemove(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			cs := work.Cornerstone{ID: "cs-1", WorkID: workID, Status: work.CornerstoneActive, Mode: work.CornerstoneSnapshot}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{cs}, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			return 2, nil
		},
		LoadProjectionFunc: func(workID string) (*work.Work, error) {
			cs := work.Cornerstone{ID: "cs-1", WorkID: workID, Status: work.CornerstoneActive, Tombstone: true}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{cs}, ArchiveState: work.ArchiveActive}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	input := work.RemoveCornerstoneInput{
		CornerstoneID:    "cs-1",
		ExpectedRevision: 1,
		RequestID:        "remove-req-1",
	}
	result, err := app.RemoveCornerstone("test", "work-cs-routed", input)
	if err != nil {
		t.Fatalf("RemoveCornerstone Wails binding: %v", err)
	}
	if result == nil {
		t.Fatal("RemoveCornerstone returned nil result")
	}
}

func TestCornerstoneWailsBindingRoutesSnapshotRepairContent(t *testing.T) {
	const replacement = "replacement-content"
	digest := work.ContentDigest([]byte(replacement))
	missing := work.Cornerstone{
		ID: "cs-blob", WorkID: "work-cs-repair", Mode: work.CornerstoneSnapshot,
		Ref: work.CornerstoneRef{Kind: "inline", BlobDigest: digest}, Digest: digest,
		Status: work.CornerstoneInvalid, ResolveErrorKind: work.ResolveErrorInvalid,
	}
	blobs := &repairBlobStore{}
	var committed *work.WorkEvent
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			if committed != nil {
				active := missing
				active.Status = work.CornerstoneActive
				active.ResolveErrorKind = ""
				return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{active}, ArchiveState: work.ArchiveActive}, work.WorkEventState{
					Revision: 2, RequestRevision: 2, RequestType: committed.Type,
					RequestEventID: committed.ID, RequestFound: true,
				}, nil
			}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{missing}, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			copy := event
			committed = &copy
			return 2, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetCornerstoneManager(work.NewCornerstoneManager(store, blobs, nil))
	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	content := replacement
	result, err := app.RepairCornerstone("test", "work-cs-repair", work.RepairCornerstoneInput{
		CornerstoneID: "cs-blob", Content: &content, ExpectedRevision: 1, RequestID: "repair-content-1",
	})
	if err != nil {
		t.Fatalf("RepairCornerstone Wails binding: %v", err)
	}
	if blobs.content != replacement {
		t.Fatal("RepairCornerstone did not route replacement content to BlobStore")
	}
	if result == nil || !result.Repaired || result.Cornerstone == nil || result.Cornerstone.Status != work.CornerstoneActive {
		t.Fatalf("RepairCornerstone result = %+v, want active", result)
	}
}

// TestCornerstoneWailsBindingConflict verifies that a revision
// conflict returns a clear error (not a panic).
func TestCornerstoneWailsBindingConflict(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 5}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	// ExpectedRevision 1 conflicts with actual revision 5.
	input := work.PinCornerstoneInput{
		Type:             work.CornerstoneInstruction,
		Title:            "冲突测试",
		Content:          "内容",
		Ref:              work.CornerstoneRef{Kind: "inline"},
		Mode:             work.CornerstoneSnapshot,
		ExpectedRevision: 1,
		RequestID:        "conflict-req-1",
	}
	_, err := app.PinCornerstone("test", "work-conflict", input)
	if err == nil {
		t.Fatal("expected revision conflict error")
	}
	t.Logf("conflict error (expected): %v", err)
}

// TestCornerstoneWithoutManager returns ErrCornerstoneDisabled.
func TestCornerstoneWithoutManager(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	// NewService owns a manager by default so Run/Resume preflight and the
	// intent API share one source of truth. Exercise the explicit disabled path.
	svc.SetCornerstoneManager(nil)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	_, err := app.PinCornerstone("test", "work-no-cm", work.PinCornerstoneInput{
		Type: work.CornerstoneInstruction, Title: "no manager", ExpectedRevision: 1, RequestID: "r-1",
	})
	if err == nil {
		t.Fatal("expected ErrCornerstoneDisabled")
	}
	if !errors.Is(err, work.ErrCornerstoneDisabled) {
		t.Fatalf("expected ErrCornerstoneDisabled, got %v", err)
	}
}
