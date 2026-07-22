package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"workground2/internal/control"
	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

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
