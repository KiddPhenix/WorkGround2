package main

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestIconEnterReturnsWithoutLegacySnapshot(t *testing.T) {
	applied := make(chan struct{}, 1)
	app := widgetStyleTestApp(t, &widgetWindowOps{
		applyWidget: func(_ WidgetWindowState, _ bool, _ bool) error { applied <- struct{}{}; return nil },
	})
	app.ctx = context.Background()
	app.runtimeEvents.emit = func(context.Context, string, ...interface{}) {}
	// A legacy pager refresh may be busy with model/system probes. Actual native
	// entry, its mode acknowledgement and subsequent entries must not wait for it.
	app.widgetActionMu.Lock()
	defer app.widgetActionMu.Unlock()
	done := make(chan error, 1)
	go func() {
		snapshot, err := app.EnterWidgetMode()
		if err == nil && !snapshot.Mode {
			err = fmt.Errorf("entry acknowledgement has mode=false")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("native entry binding waited for the legacy pager snapshot")
	}
	select {
	case <-applied:
	default:
		t.Fatal("entry returned without applying native widget geometry")
	}
}

func TestIconSurfaceRejectsPreviousModeLifetime(t *testing.T) {
	state := WidgetWindowState{Width: 1080, Height: 720, X: 100, Y: 100}
	regions := 0
	app := &App{ctx: context.Background(), widgetStyle: "icons", widgetSurface: newDesktopIconSurfaceRuntime(state),
		widgetWindowOps: &widgetWindowOps{regions: func([]DesktopIconRect) error { regions++; return nil }}}
	apply := func() error { return nil }
	app.transitionWidgetMode(true, apply)
	first := app.GetWidgetModeState()
	old := app.desktopIconSurfaceResult(state)
	app.transitionWidgetMode(false, apply)
	app.transitionWidgetMode(true, apply)
	current := app.GetWidgetModeState()
	if current.Revision <= first.Revision || !current.Active {
		t.Fatalf("reentry lost native identity: %+v -> %+v", first, current)
	}
	rects := []DesktopIconRect{{X: 20, Y: 20, Width: 60, Height: 60}}
	if err := app.SetDesktopIconHitRegions(DesktopIconHitRegionsInput{Rects: rects, Surface: old}); err != nil || regions != 0 {
		t.Fatalf("old equal-sized surface clipped new mode: calls=%d err=%v", regions, err)
	}
	if _, err := app.SetDesktopIconSurface(DesktopIconSurfaceInput{Revision: first.Revision, Width: 2000, Height: 1800}); err == nil {
		t.Fatal("old resize was accepted into the new mode lifetime")
	}
	if err := app.SetDesktopIconHitRegions(DesktopIconHitRegionsInput{Rects: rects, Surface: app.desktopIconSurfaceResult(state)}); err != nil || regions != 1 {
		t.Fatalf("current mode cannot install its regions: calls=%d err=%v", regions, err)
	}
	app.transitionWidgetMode(true, apply)
	if app.GetWidgetModeState() != current {
		t.Fatal("duplicate entry changed mode identity")
	}
	app.transitionWidgetMode(false, apply)
	if err := app.SetDesktopIconHitRegions(DesktopIconHitRegionsInput{Rects: rects, Surface: app.desktopIconSurfaceResult(state)}); err != nil || regions != 1 {
		t.Fatalf("main window accepted icon regions: calls=%d err=%v", regions, err)
	}
}

func TestIconEntryDoesNotWaitForProjectScan(t *testing.T) {
	for _, attention := range []int64{0, 1000} {
		t.Run(time.Duration(attention).String(), func(t *testing.T) {
			tab, path := completionTestTab(t, attention)
			app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
			app.widgetMode = true
			app.retainActiveSessionForWidgetEnter(true)
			old := DesktopIconItem{ID: "workspace:existing", Kind: "workspace", Title: "Existing project", Position: DesktopIconPosition{Row: "bottom", Zone: "workspace"}}
			app.iconWidgetLastSnapshot = DesktopIconSnapshot{Items: []DesktopIconItem{old}}
			app.iconWidgetSnapshotReady = true

			scanning, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			app.desktopIconProjectTree = func() []ProjectNode { close(scanning); <-release; return nil }
			go func() { app.GetDesktopIconSnapshot(); close(done) }()
			t.Cleanup(func() { close(release); <-done })
			<-scanning
			result := make(chan DesktopIconSnapshot, 1)
			go func() { result <- app.GetDesktopIconEntrySnapshot() }()
			select {
			case snapshot := <-result:
				count := 0
				for _, item := range snapshot.Items {
					if item.Kind == "task" && item.SessionRef != nil && sessionRuntimeKey(item.SessionRef.SessionPath) == sessionRuntimeKey(path) {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("entry has %d current Session icons: %+v", count, snapshot.Items)
				}
				if got := findDesktopIconItem(snapshot.Items, old.ID); got == nil || !reflect.DeepEqual(*got, old) {
					t.Fatalf("entry lost the existing project icon: %+v", snapshot.Items)
				}
				page, err := app.RecentSessions(RecentSessionsRequest{})
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("sidebar did not observe entry task: %+v %v", page, err)
				}
				if next := app.GetDesktopIconEntrySnapshot(); next.Revision != snapshot.Revision {
					t.Fatal("repeated entry changed unchanged projection")
				}
			case <-time.After(time.Second):
				t.Fatal("entry waited for the blocked project scan")
			}
		})
	}
}

func TestIconEntryColdStartUsesRealTasks(t *testing.T) {
	tab, path := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = true
	app.iconWidgetState.WorkspaceSlots = 4
	app.retainActiveSessionForWidgetEnter(true)
	app.desktopIconProjectTree = func() []ProjectNode { t.Fatal("entry scanned project tree"); return nil }
	snapshot := app.GetDesktopIconEntrySnapshot()
	if findDesktopIconItem(snapshot.Items, desktopIconKeptID(path)) == nil {
		t.Fatal("cold entry lost current Session")
	}
	for _, item := range snapshot.Items {
		if item.Kind == "workspace" {
			t.Fatal("cold entry fabricated a workspace shortcut")
		}
	}
}

func TestIconRemoveThenEnterDoesNotScanProjects(t *testing.T) {
	tab, path := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	window := widgetStyleTestApp(t, &widgetWindowOps{})
	app.widgetWindowOps = window.widgetWindowOps
	app.widgetTaskbarToggle = window.widgetTaskbarToggle
	app.ctx = context.Background()
	entered := make(chan DesktopIconSnapshot, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name == "widget:mode" && len(payload) == 1 && payload[0] == true {
			entered <- app.GetDesktopIconEntrySnapshot()
		}
	}
	app.widgetMode = true
	app.rememberDesktopIconTask(tab.ID)
	item := findDesktopIconItem(app.GetDesktopIconEntrySnapshot().Items, desktopIconKeptID(path))
	if item == nil {
		t.Fatal("missing retained task")
	}
	if err := app.ExitWidgetMode(""); err != nil {
		t.Fatal(err)
	}
	app.desktopIconProjectTree = func() []ProjectNode {
		t.Fatal("remove scanned projects while holding the icon state lock")
		return nil
	}
	input := DesktopIconActionInput{ItemID: item.ID, Revision: item.Revision, RequestID: "remove-enter", Action: "remove"}
	result := app.ApplyDesktopIconAction(input)
	if result.Status != "accepted" || findDesktopIconItem(result.Snapshot.Items, item.ID) != nil {
		t.Fatalf("remove = %+v", result)
	}
	page, err := app.RecentSessions(RecentSessionsRequest{})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("sidebar retained removed task: %+v, %v", page, err)
	}
	// The next minimize explicitly retains the active Session again. A delayed
	// retry of the old remove must not delete this newly retained icon.
	if snapshot, err := app.EnterWidgetMode(); err != nil || !snapshot.Mode {
		t.Fatalf("entry acknowledgement = %+v, %v", snapshot, err)
	}
	select {
	case snapshot := <-entered:
		if findDesktopIconItem(snapshot.Items, input.ItemID) == nil {
			t.Fatal("mode event preceded the restored active Session icon")
		}
	case <-time.After(time.Second):
		t.Fatal("native window changed but widget mode event was not published")
	}
	if item := findDesktopIconItem(app.GetDesktopIconEntrySnapshot().Items, input.ItemID); item == nil {
		t.Fatal("next entry did not immediately restore the active Session")
	}
	result = app.ApplyDesktopIconAction(input)
	if result.Status != "already_applied" || findDesktopIconItem(result.Snapshot.Items, input.ItemID) == nil {
		t.Fatalf("old remove retry deleted the re-retained Session: %+v", result)
	}
	conflict := input
	conflict.Values = []string{"different intent"}
	if result := app.ApplyDesktopIconAction(conflict); result.Status != "invalid" {
		t.Fatalf("request ID reuse = %+v", result)
	}
}
