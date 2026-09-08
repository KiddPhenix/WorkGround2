package main

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

type mainRelocateRecord struct {
	state DesktopWindowState
	ok    bool
}

func relocateWidgetTestApp(t *testing.T, ops *widgetWindowOps, style string) *App {
	t.Helper()
	app := widgetStyleTestApp(t, ops)
	app.widgetMode = true
	app.widgetStyle = style
	return app
}

// blockStateFilePath makes every write to the persisted state file at path
// fail by replacing it with a directory; unblockStateFilePath restores it.
func blockStateFilePath(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func unblockStateFilePath(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
}

// TestRelocateWidgetPagerOffscreenClampsAndPersists: a pager window left on a
// removed display is shown, re-normalized against the live monitors, re-applied
// with the persisted always-on-top flag, and the corrected geometry persisted
// so the stale saved position cannot undo the recovery on a later mode switch.
func TestRelocateWidgetPagerOffscreenClampsAndPersists(t *testing.T) {
	var applies []widgetApplyRecord
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: -2000, Y: -500}, false
		},
		normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
			// Simulates the Windows live-monitor clamp: off-screen origins are
			// pulled back into the visible work area.
			if state.X < 0 {
				state.X = 0
			}
			if state.Y < 0 {
				state.Y = 0
			}
			return state, nil
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	publish, err := app.relocateWidgetWindowLocked()
	if err != nil {
		t.Fatalf("relocate widget: %v", err)
	}
	if publish {
		t.Fatal("pager relocation must not publish a widget:mode event")
	}
	want := WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 0, Y: 0}
	if len(applies) != 1 || applies[0].state != want || applies[0].icons {
		t.Fatalf("applies = %#v, want one pager apply of %#v", applies, want)
	}
	if !applies[0].alwaysOnTop {
		t.Fatal("relocate must preserve the persisted always-on-top flag")
	}
	if !reflect.DeepEqual(shows, []bool{false}) {
		t.Fatalf("widget restore-show calls = %v, want exactly one non-maximised show", shows)
	}
	persisted, ok := loadWidgetWindowState()
	if !ok || persisted != want {
		t.Fatalf("persisted widget state = %#v (ok=%v), want %#v", persisted, ok, want)
	}
	if !app.IsWidgetMode() || app.widgetStyle != "pager" {
		t.Fatalf("relocate changed mode/style: mode=%v style=%q", app.IsWidgetMode(), app.widgetStyle)
	}
}

// TestRelocateWidgetIconsRefreshesSurfaceAndRevision: the desktop-icon surface
// is recovered through the icon geometry path; a real geometry change refreshes
// the authoritative widgetSurface runtime and bumps the revision so the
// republished mode event re-establishes regions and stale requests are
// rejected.
func TestRelocateWidgetIconsRefreshesSurfaceAndRevision(t *testing.T) {
	var applies []widgetApplyRecord
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 4000, Y: 2000}, false
		},
		normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
			state.X = 100
			state.Y = 80
			return state, nil
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := relocateWidgetTestApp(t, ops, "icons")
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }
	before := app.widgetRevision

	publish, err := app.relocateWidgetWindowLocked()
	if err != nil {
		t.Fatalf("relocate icons: %v", err)
	}
	if !publish {
		t.Fatal("icon geometry change must publish a widget:mode event")
	}
	want := WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 100, Y: 80}
	if len(applies) != 1 || applies[0].state != want || !applies[0].icons {
		t.Fatalf("applies = %#v, want one icons apply of %#v", applies, want)
	}
	if !applies[0].alwaysOnTop {
		t.Fatal("relocate icons must preserve the always-on-top flag")
	}
	if app.widgetSurface.State != want {
		t.Fatalf("widgetSurface.State = %#v, want %#v", app.widgetSurface.State, want)
	}
	if app.widgetRevision != before+1 {
		t.Fatalf("widgetRevision = %d, want %d", app.widgetRevision, before+1)
	}
	if !reflect.DeepEqual(shows, []bool{false}) {
		t.Fatalf("widget restore-show calls = %v, want exactly one non-maximised show", shows)
	}
	persisted, ok, loadErr := loadDesktopIconWindowState()
	if loadErr != nil || !ok || persisted != want {
		t.Fatalf("persisted icon state = %#v (ok=%v err=%v), want %#v", persisted, ok, loadErr, want)
	}
}

// TestRelocateIconsPublishesModeEvent: relocateActiveWindow emits the
// widget:mode event (with the current mode) after the geometry work finishes,
// so React re-establishes the surface/regions and rejects stale requests.
func TestRelocateIconsPublishesModeEvent(t *testing.T) {
	var shows []bool
	type modeEvent struct {
		name    string
		payload bool
	}
	events := make(chan modeEvent, 4)
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 4000, Y: 2000}, false
		},
		normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
			state.X = 100
			state.Y = 80
			return state, nil
		},
		applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
	}
	app := relocateWidgetTestApp(t, ops, "icons")
	app.ctx = context.Background()
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		event := modeEvent{name: name}
		if len(payload) == 1 {
			event.payload, _ = payload[0].(bool)
		}
		events <- event
	}
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateActiveWindow(); err != nil {
		t.Fatalf("relocateActiveWindow: %v", err)
	}
	select {
	case event := <-events:
		if event.name != "widget:mode" || !event.payload {
			t.Fatalf("event = %#v, want widget:mode with the current mode (true)", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no widget:mode republish was emitted")
	}
	select {
	case event := <-events:
		t.Fatalf("unexpected extra event %#v", event)
	default:
	}
	if len(shows) != 1 {
		t.Fatalf("restore-show calls = %v, want exactly one", shows)
	}
}

// TestRelocateWidgetAlreadyVisibleStillShowsAndPersists: a fully visible widget
// window is still shown and its geometry persisted (healing an earlier
// apply-success/save-failure), but never re-applied.
func TestRelocateWidgetAlreadyVisibleStillShowsAndPersists(t *testing.T) {
	applies := 0
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 120, Y: 80}, false
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies++
			return nil
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	publish, err := app.relocateWidgetWindowLocked()
	if err != nil {
		t.Fatalf("relocate visible widget: %v", err)
	}
	if publish || applies != 0 {
		t.Fatalf("publish=%v applies=%d, want no republish and no re-apply", publish, applies)
	}
	if !reflect.DeepEqual(shows, []bool{false}) {
		t.Fatalf("restore-show calls = %v, want exactly one show", shows)
	}
	persisted, ok := loadWidgetWindowState()
	if !ok || persisted.X != 120 || persisted.Y != 80 {
		t.Fatalf("persisted widget geometry changed: %#v (ok=%v)", persisted, ok)
	}
}

// TestRelocateWidgetRepeatedClicksSettle: once the native window sits at the
// corrected geometry, repeated clicks are safe no-ops (exactly one apply, one
// republish), with persistence on every retry.
func TestRelocateWidgetRepeatedClicksSettle(t *testing.T) {
	applies := 0
	publishes := 0
	current := WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: -2000, Y: -500}
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) { return current, false },
		normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
			if state.X < 0 {
				state.X = 0
			}
			if state.Y < 0 {
				state.Y = 0
			}
			return state, nil
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies++
			current = state
			return nil
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(bool) {}
	for i := 0; i < 3; i++ {
		publish, err := app.relocateWidgetWindowLocked()
		if err != nil {
			t.Fatalf("click %d: %v", i, err)
		}
		if publish {
			publishes++
		}
	}
	if applies != 1 {
		t.Fatalf("applied %d times, want exactly 1", applies)
	}
	if publishes != 0 {
		t.Fatalf("republished %d times, want 0 for the pager", publishes)
	}
	if _, ok := loadWidgetWindowState(); !ok {
		t.Fatal("relocate did not persist on the settled retries")
	}
}

// TestRelocateIconsPublishesEvenWhenPersistFails: the icon surface change is
// published even when the follow-up persistence fails; the error is returned
// explicitly so the next click retries the save.
func TestRelocateIconsPublishesEvenWhenPersistFails(t *testing.T) {
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 4000, Y: 2000}, false
		},
		normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
			state.X = 100
			state.Y = 80
			return state, nil
		},
		applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
	}
	app := relocateWidgetTestApp(t, ops, "icons")
	app.windowRestoreShow = func(bool) {}
	blockStateFilePath(t, desktopIconWindowStatePath())
	defer unblockStateFilePath(t, desktopIconWindowStatePath())

	publish, err := app.relocateWidgetWindowLocked()
	if err == nil || !strings.Contains(err.Error(), "persist geometry") {
		t.Fatalf("error = %v, want the widget persist failure surfaced", err)
	}
	if !publish {
		t.Fatal("icon surface change must publish even when persistence failed")
	}
}

// TestRelocateWidgetRepairsSavedMainWindow: while the widget is active the
// saved main-window geometry is normalized and persisted, so exiting widget
// mode (which restores that file without normalization) lands back on screen.
func TestRelocateWidgetRepairsSavedMainWindow(t *testing.T) {
	applies := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 120, Y: 80}, false
		},
		applyWidget: func(WidgetWindowState, bool, bool) error {
			applies++
			return nil
		},
		normalizeMain: func(state DesktopWindowState) (DesktopWindowState, error) {
			state.Width = 1920
			state.Height = 1040
			state.X = 640
			state.Y = 240
			return state, nil
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(bool) {}
	if err := saveMainWindowState(DesktopWindowState{Width: 2560, Height: 1440, X: 5000, Y: 3000}); err != nil {
		t.Fatal(err)
	}

	publish, err := app.relocateWidgetWindowLocked()
	if err != nil {
		t.Fatalf("relocate widget: %v", err)
	}
	if publish || applies != 0 {
		t.Fatalf("publish=%v applies=%d, want no republish/no widget apply", publish, applies)
	}
	want := DesktopWindowState{Width: 1920, Height: 1040, X: 640, Y: 240}
	persisted, ok := loadWindowState()
	if !ok || persisted != want {
		t.Fatalf("saved main state = %#v (ok=%v), want %#v", persisted, ok, want)
	}
}

// TestRelocateWidgetSavedMainRepairErrorSurfaces: a failure to normalize the
// saved main geometry is returned explicitly (retryable), never silent.
func TestRelocateWidgetSavedMainRepairErrorSurfaces(t *testing.T) {
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 120, Y: 80}, false
		},
		normalizeMain: func(DesktopWindowState) (DesktopWindowState, error) {
			return DesktopWindowState{}, errors.New("window not found")
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(bool) {}
	if err := saveMainWindowState(DesktopWindowState{Width: 1280, Height: 800, X: 5000, Y: 3000}); err != nil {
		t.Fatal(err)
	}

	_, err := app.relocateWidgetWindowLocked()
	if err == nil || !strings.Contains(err.Error(), "normalize saved main window") {
		t.Fatalf("error = %v, want the saved-main normalize failure surfaced", err)
	}
}

// TestRelocateWidgetNormalizeErrorSurfaces: a live-screen query failure is
// returned explicitly instead of being swallowed.
func TestRelocateWidgetNormalizeErrorSurfaces(t *testing.T) {
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: -2000, Y: -500}, false
		},
		normalize: func(WidgetWindowState) (WidgetWindowState, error) {
			return WidgetWindowState{}, errors.New("no visible monitors")
		},
	}
	app := relocateWidgetTestApp(t, ops, "pager")
	app.windowRestoreShow = func(bool) {}
	publish, err := app.relocateWidgetWindowLocked()
	if err == nil || !strings.Contains(err.Error(), "no visible monitors") {
		t.Fatalf("error = %v, want the live-screen failure surfaced", err)
	}
	if publish {
		t.Fatal("failed normalization must not publish")
	}
}

// TestRelocateMainWindowOffscreenShowsFitsAndPersists: an oversized session
// window left on a removed display is shown, fitted into the live work area,
// repositioned and persisted as the authoritative main-window state.
func TestRelocateMainWindowOffscreenShowsFitsAndPersists(t *testing.T) {
	var restores []mainRelocateRecord
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: 2560, Height: 1440, X: 5000, Y: 3000}, false
		},
		normalizeMain: func(state DesktopWindowState) (DesktopWindowState, error) {
			// Simulates the Windows live clamp into a 1920x1040 work area.
			state.Width = 1920
			state.Height = 1040
			state.X = 640
			state.Y = 240
			return state, nil
		},
		restoreMain: func(state DesktopWindowState, ok bool) error {
			restores = append(restores, mainRelocateRecord{state: state, ok: ok})
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("relocate main: %v", err)
	}
	want := DesktopWindowState{Width: 1920, Height: 1040, X: 640, Y: 240}
	if len(restores) != 1 || restores[0].state != want || !restores[0].ok {
		t.Fatalf("restores = %#v, want one restore of %#v with ok=true", restores, want)
	}
	if !reflect.DeepEqual(shows, []bool{false}) {
		t.Fatalf("restore-show calls = %v, want exactly one non-maximised show", shows)
	}
	persisted, ok := loadWindowState()
	if !ok || persisted != want {
		t.Fatalf("persisted main state = %#v (ok=%v), want %#v", persisted, ok, want)
	}
}

// TestRelocateMainWindowReadsGeometryAfterShow: a minimized window reports
// meaningless coordinates (-32000) before it is restored; the correction must
// start from the live geometry read after the show.
func TestRelocateMainWindowReadsGeometryAfterShow(t *testing.T) {
	reads := 0
	var normalizedInput DesktopWindowState
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			reads++
			if reads == 1 {
				// Pre-show snapshot of a minimized window.
				return WidgetWindowState{Width: 1280, Height: 800, X: -32000, Y: -32000}, false
			}
			return WidgetWindowState{Width: 1280, Height: 800, X: 30, Y: 40}, false
		},
		normalizeMain: func(state DesktopWindowState) (DesktopWindowState, error) {
			normalizedInput = state
			return state, nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("relocate minimized main: %v", err)
	}
	if len(shows) != 1 || shows[0] {
		t.Fatalf("restore-show calls = %v, want one non-maximised show", shows)
	}
	want := DesktopWindowState{Width: 1280, Height: 800, X: 30, Y: 40}
	if normalizedInput != want {
		t.Fatalf("normalize input = %#v, want post-show geometry %#v", normalizedInput, want)
	}
	persisted, ok := loadWindowState()
	if !ok || persisted != want {
		t.Fatalf("persisted main state = %#v (ok=%v), want %#v", persisted, ok, want)
	}
}

// TestRelocateMainWindowHonorsBackgroundMaximised: the hidden-to-background
// maximize record drives the restore plan even when the live state cannot
// report it, and is consumed so a later tray Open does not double-restore.
func TestRelocateMainWindowHonorsBackgroundMaximised(t *testing.T) {
	var shows []bool
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: 1280, Height: 800, X: 30, Y: 40}, false
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.backgroundMaximised.Store(true)
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("relocate main: %v", err)
	}
	if !reflect.DeepEqual(shows, []bool{true}) {
		t.Fatalf("restore-show calls = %v, want one maximised show", shows)
	}
	if app.backgroundMaximised.Load() {
		t.Fatal("backgroundMaximised must be consumed by the restore")
	}
}

// TestRelocateMainWindowMaximisedShowsOnly: a maximised window is bounded by
// its monitor work area (Windows re-homes it when a display disappears), so
// relocate only restores visibility and never rewrites geometry.
func TestRelocateMainWindowMaximisedShowsOnly(t *testing.T) {
	restores := 0
	normalizes := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: 1280, Height: 800, X: 30, Y: 40}, true
		},
		normalizeMain: func(state DesktopWindowState) (DesktopWindowState, error) {
			normalizes++
			return state, nil
		},
		restoreMain: func(DesktopWindowState, bool) error {
			restores++
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	var shows []bool
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("relocate maximised main: %v", err)
	}
	if !reflect.DeepEqual(shows, []bool{true}) {
		t.Fatalf("restore-show calls = %v, want one maximised show", shows)
	}
	if restores != 0 || normalizes != 0 {
		t.Fatalf("maximised relocate rewrote geometry: restores=%d normalizes=%d", restores, normalizes)
	}
	if _, ok := loadWindowState(); ok {
		t.Fatal("maximised relocate must not persist un-maximised geometry")
	}
}

// TestRelocateMainWindowUnchangedStillPersists: when the post-show geometry
// already fits the live monitors the window is still shown but no bounds write
// happens; the geometry is still persisted so an earlier apply-success/
// save-failure heals on retry.
func TestRelocateMainWindowUnchangedStillPersists(t *testing.T) {
	restores := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: 1280, Height: 800, X: 30, Y: 40}, false
		},
		restoreMain: func(DesktopWindowState, bool) error {
			restores++
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	var shows []bool
	app.windowRestoreShow = func(wasMaximised bool) { shows = append(shows, wasMaximised) }

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("relocate visible main: %v", err)
	}
	if len(shows) != 1 {
		t.Fatalf("restore-show calls = %v, want exactly one", shows)
	}
	if restores != 0 {
		t.Fatalf("visible main window geometry was rewritten %d times", restores)
	}
	persisted, ok := loadWindowState()
	if !ok || persisted != (DesktopWindowState{Width: 1280, Height: 800, X: 30, Y: 40}) {
		t.Fatalf("persisted main state = %#v (ok=%v), want the live geometry", persisted, ok)
	}
}

// TestRelocateMainWindowPersistRetryHealsSaveFailure: an apply that succeeds
// but whose persist fails returns an explicit error; the next click finds the
// geometry already corrected, persists it, and succeeds.
func TestRelocateMainWindowPersistRetryHealsSaveFailure(t *testing.T) {
	live := WidgetWindowState{Width: 2560, Height: 1440, X: 5000, Y: 3000}
	restores := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) { return live, false },
		normalizeMain: func(state DesktopWindowState) (DesktopWindowState, error) {
			state.Width = 1920
			state.Height = 1040
			state.X = 640
			state.Y = 240
			return state, nil
		},
		restoreMain: func(state DesktopWindowState, ok bool) error {
			restores++
			live = WidgetWindowState{Width: state.Width, Height: state.Height, X: state.X, Y: state.Y}
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.windowRestoreShow = func(bool) {}
	path := windowStatePath()
	blockStateFilePath(t, path)

	err := app.relocateMainWindowLocked()
	if err == nil || !strings.Contains(err.Error(), "persist geometry") {
		t.Fatalf("first relocate error = %v, want the persist failure surfaced", err)
	}
	if restores != 1 {
		t.Fatalf("restores = %d, want the geometry applied once before the save failure", restores)
	}
	unblockStateFilePath(t, path)

	if err := app.relocateMainWindowLocked(); err != nil {
		t.Fatalf("retry relocate: %v", err)
	}
	want := DesktopWindowState{Width: 1920, Height: 1040, X: 640, Y: 240}
	persisted, ok := loadWindowState()
	if !ok || persisted != want {
		t.Fatalf("persisted main state = %#v (ok=%v), want %#v", persisted, ok, want)
	}
	if restores != 1 {
		t.Fatalf("retry re-applied geometry %d times, want the settled no-op", restores)
	}
}

// TestRelocateMainWindowNormalizeErrorSurfaces: a live-screen query failure in
// main mode is returned explicitly.
func TestRelocateMainWindowNormalizeErrorSurfaces(t *testing.T) {
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			return WidgetWindowState{Width: 1280, Height: 800, X: 5000, Y: 3000}, false
		},
		normalizeMain: func(DesktopWindowState) (DesktopWindowState, error) {
			return DesktopWindowState{}, errors.New("window not found")
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.windowRestoreShow = func(bool) {}
	err := app.relocateMainWindowLocked()
	if err == nil || !strings.Contains(err.Error(), "window not found") {
		t.Fatalf("error = %v, want the live-screen failure surfaced", err)
	}
}
