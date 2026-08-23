package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/config"
)

func newDismissWindowTestApp(t *testing.T, kept map[string]desktopIconKept) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	path := filepath.Join(t.TempDir(), "active.jsonl")
	tab := &WorkspaceTab{
		ID: "tab-active", SessionID: "session-active", Scope: "project",
		WorkspaceRoot: t.TempDir(), TopicID: "topic-active", SessionPath: path,
	}
	app := &App{
		ctx:                   context.Background(),
		tabs:                  map[string]*WorkspaceTab{tab.ID: tab},
		tabOrder:              []string{tab.ID},
		activeTabID:           tab.ID,
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{}, Kept: kept,
			CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
	}
	return app
}

func TestNativeMinimiseEntersWidgetWithoutDismissingActiveIcon(t *testing.T) {
	app := newDismissWindowTestApp(t, map[string]desktopIconKept{
		"task:tab-active": {
			ItemID: "task:tab-active", SourceID: "tab-active", SessionID: "session-active", Title: "active",
		},
	})
	entered := 0
	app.widgetModeEnter = func() error { entered++; return nil }

	if err := app.performNativeWindowAction(nativeWindowActionMinimise); err != nil {
		t.Fatalf("performNativeWindowAction(minimise): %v", err)
	}
	if entered != 1 {
		t.Fatalf("widget entry calls = %d, want 1", entered)
	}
	if _, ok := app.iconWidgetState.Kept["task:tab-active"]; !ok {
		t.Fatal("minimise-to-widget removed the active icon")
	}
}

func TestNativeMinimiseFallsBackWhenWidgetDisabled(t *testing.T) {
	isolateDesktopUserDirs(t)
	userCfg := config.LoadForEdit(config.UserConfigPath())
	if err := userCfg.SetDesktopWidgetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := userCfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	minimised := 0
	app.windowMinimise = func() { minimised++ }
	app.widgetModeEnter = func() error {
		t.Fatal("disabled widget must not enter widget mode")
		return nil
	}

	if err := app.performNativeWindowAction(nativeWindowActionMinimise); err != nil {
		t.Fatalf("performNativeWindowAction(minimise): %v", err)
	}
	if minimised != 1 {
		t.Fatalf("native minimise calls = %d, want 1", minimised)
	}
}

func TestNativeWindowActionCollapsesDuplicateClicks(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	app.widgetModeEnter = func() error {
		close(started)
		<-release
		close(finished)
		return nil
	}

	app.requestNativeWindowAction(nativeWindowActionMinimise)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first native window action did not start")
	}
	app.requestNativeWindowAction(nativeWindowActionDismiss)
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("native window action did not finish")
	}
	deadline := time.After(time.Second)
	for app.nativeWindowActionInFlight.Load() {
		select {
		case <-time.After(5 * time.Millisecond):
		case <-deadline:
			t.Fatal("native window action guard did not clear")
		}
	}
}

func TestDismissMainWindowRemovesMatchingSessionIconThenEntersWidget(t *testing.T) {
	app := newDismissWindowTestApp(t, map[string]desktopIconKept{
		"task:tab-active": {
			ItemID: "task:tab-active", SourceID: "tab-active", SessionID: "session-active",
			Title: "active", SessionPath: filepath.Join(t.TempDir(), "active.jsonl"),
		},
		"task:tab-other": {
			ItemID: "task:tab-other", SourceID: "tab-other", SessionID: "session-other",
			Title: "other", SessionPath: filepath.Join(t.TempDir(), "other.jsonl"),
		},
	})
	entered := 0
	app.widgetModeEnter = func() error { entered++; return nil }

	if err := app.DismissMainWindow(); err != nil {
		t.Fatalf("DismissMainWindow: %v", err)
	}
	if _, ok := app.iconWidgetState.Kept["task:tab-active"]; ok {
		t.Fatal("matching session icon was not removed")
	}
	if _, ok := app.iconWidgetState.Kept["task:tab-other"]; !ok {
		t.Fatal("another session icon was removed")
	}
	if entered != 1 {
		t.Fatalf("widget entry calls = %d, want 1", entered)
	}
}

func TestBeforeCloseDismissesMatchingIconThenEntersWidget(t *testing.T) {
	app := newDismissWindowTestApp(t, map[string]desktopIconKept{
		"task:tab-active": {
			ItemID: "task:tab-active", SourceID: "tab-active", SessionID: "session-active", Title: "active",
		},
	})
	entered := 0
	app.widgetModeEnter = func() error { entered++; return nil }

	if prevent := app.beforeClose(context.Background()); !prevent {
		t.Fatal("native close with widget enabled should be absorbed")
	}
	if entered != 1 {
		t.Fatalf("widget entry calls = %d, want 1", entered)
	}
	if _, ok := app.iconWidgetState.Kept["task:tab-active"]; ok {
		t.Fatal("native close did not dismiss the active icon")
	}
}

func TestDismissMainWindowWithoutMatchingIconIsIdempotent(t *testing.T) {
	app := newDismissWindowTestApp(t, map[string]desktopIconKept{
		"task:tab-other": {
			ItemID: "task:tab-other", SourceID: "tab-other", SessionID: "session-other", Title: "other",
		},
	})
	entered := 0
	app.widgetModeEnter = func() error { entered++; return nil }

	for i := 0; i < 2; i++ {
		if err := app.DismissMainWindow(); err != nil {
			t.Fatalf("DismissMainWindow call %d: %v", i+1, err)
		}
	}
	if len(app.iconWidgetState.Kept) != 1 {
		t.Fatalf("unrelated icons changed: %+v", app.iconWidgetState.Kept)
	}
	if entered != 2 {
		t.Fatalf("widget entry calls = %d, want 2", entered)
	}
}

func TestDismissMainWindowKeepsMatchingSessionIconOutsideWidgetArea(t *testing.T) {
	kept := map[string]desktopIconKept{}
	for i := 0; i < desktopIconMaxTasks; i++ {
		id := fmt.Sprintf("task:%02d", i)
		kept[id] = desktopIconKept{ItemID: id, SourceID: fmt.Sprintf("tab-%02d", i), SessionID: fmt.Sprintf("session-%02d", i), Title: id}
	}
	kept["task:zz-active"] = desktopIconKept{
		ItemID: "task:zz-active", SourceID: "tab-active", SessionID: "session-active", Title: "active outside capacity",
	}
	app := newDismissWindowTestApp(t, kept)
	entered := 0
	app.widgetModeEnter = func() error { entered++; return nil }

	if err := app.DismissMainWindow(); err != nil {
		t.Fatalf("DismissMainWindow: %v", err)
	}
	if _, ok := app.iconWidgetState.Kept["task:zz-active"]; !ok {
		t.Fatal("matching session icon outside the widget area was removed")
	}
	if entered != 1 {
		t.Fatalf("widget entry calls = %d, want 1", entered)
	}
}

func TestDismissMainWindowWidgetFailureCanRetryAfterRemoval(t *testing.T) {
	app := newDismissWindowTestApp(t, map[string]desktopIconKept{
		"task:tab-active": {
			ItemID: "task:tab-active", SourceID: "tab-active", SessionID: "session-active", Title: "active",
		},
	})
	attempts := 0
	app.widgetModeEnter = func() error {
		attempts++
		if attempts == 1 {
			return errors.New("window resize failed")
		}
		return nil
	}

	if err := app.DismissMainWindow(); err == nil {
		t.Fatal("first widget entry failure was hidden")
	}
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("matching icon removal was rolled back after widget failure: %+v", app.iconWidgetState.Kept)
	}
	if err := app.DismissMainWindow(); err != nil {
		t.Fatalf("retry DismissMainWindow: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("widget entry attempts = %d, want 2", attempts)
	}
}
