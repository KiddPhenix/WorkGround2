package main

import (
	"testing"

	"workground2/internal/control"
	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

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
