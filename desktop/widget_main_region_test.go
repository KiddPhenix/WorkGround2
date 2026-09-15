package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

// widgetRegionRecorder captures native window operations in call order so the
// region-restore ordering contract is asserted deterministically without any
// real Win32 window.
type widgetRegionRecorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *widgetRegionRecorder) add(name string) {
	r.mu.Lock()
	r.calls = append(r.calls, name)
	r.mu.Unlock()
}

func (r *widgetRegionRecorder) index(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, call := range r.calls {
		if call == name {
			return i
		}
	}
	return -1
}

func (r *widgetRegionRecorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, call := range r.calls {
		if call == name {
			n++
		}
	}
	return n
}

func (r *widgetRegionRecorder) callsCopy() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.calls))
	copy(out, r.calls)
	return out
}

// waitCall polls until the recorder saw name; asyncRuntimeEmitter delivers
// events on a background goroutine, so ordering assertions need the event to
// land before comparing indices.
func (r *widgetRegionRecorder) waitCall(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.index(name) >= 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in %v", name, r.callsCopy())
}

// newWidgetRegionExitApp builds an App whose whole native window interaction is
// replaced by the recorder, so ExitWidgetMode/EnterWidgetMode exercise the real
// Go orchestration (locks, reconcile, region re-assert) with no real window.
func newWidgetRegionExitApp(t *testing.T, rec *widgetRegionRecorder, widgetMode bool) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := &App{
		ctx:         context.Background(),
		widgetMode:  widgetMode,
		widgetStyle: "icons",
		widgetWindowOps: &widgetWindowOps{
			read: func() (WidgetWindowState, bool) {
				// Icon-mode saves reject surfaces below desktopIconMinWidth/
				// Height, so the fake window reports a valid icon-surface size.
				return WidgetWindowState{Width: 1080, Height: 720, X: 1300, Y: 760}, false
			},
			normalize: func(state WidgetWindowState) (WidgetWindowState, error) {
				rec.add("normalize")
				return state, nil
			},
			restoreMain: func(DesktopWindowState, bool) error {
				rec.add("main")
				return nil
			},
			applyWidget: func(WidgetWindowState, bool, bool) error {
				rec.add("widget")
				return nil
			},
			repaintWindow: func() error {
				rec.add("repaint")
				return nil
			},
			clearRegion: func() error {
				rec.add("clear")
				return nil
			},
		},
		widgetTaskbarToggle: func(hide bool) error {
			if hide {
				rec.add("hide")
			} else {
				rec.add("show")
			}
			return nil
		},
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {
		rec.add("emit:" + name)
	}
	return app
}

// TestRunRestoreMainGeometryClearsRegionAfterFinalGeometry locks the ordering
// contract behind the partial-presentation defect: every geometry step runs
// before the HRGN is cleared, so the compositor derives the full-window clip at
// the final geometry instead of carrying the widget/icon clip across a resize.
func TestRunRestoreMainGeometryClearsRegionAfterFinalGeometry(t *testing.T) {
	t.Run("maximised restore clears last", func(t *testing.T) {
		rec := &widgetRegionRecorder{}
		ops := restoreMainWindowOps{
			clearAlwaysOnTop: func() { rec.add("always-on-top-off") },
			clearIconMode:    func() error { rec.add("icon-mode-off"); return nil },
			setMinSize:       func(int, int) { rec.add("min-size") },
			setFallbackSize:  func() { rec.add("fallback-size") },
			center:           func() { rec.add("center") },
			unmaximise:       func() { rec.add("unmaximise") },
			setBounds:        func(DesktopWindowState) error { rec.add("bounds"); return nil },
			maximise:         func() { rec.add("maximise") },
			clearRegion:      func() error { rec.add("clear"); return nil },
		}
		err := runRestoreMainGeometry(DesktopWindowState{Width: 1280, Height: 800, X: 100, Y: 50, Maximised: true}, true, ops)
		if err != nil {
			t.Fatal(err)
		}
		for _, step := range []string{"unmaximise", "bounds", "maximise"} {
			if rec.index("clear") < rec.index(step) {
				t.Fatalf("region clear ran before %s: %v", step, rec.callsCopy())
			}
		}
		if rec.count("clear") != 1 {
			t.Fatalf("region clear count = %d, want exactly one: %v", rec.count("clear"), rec.callsCopy())
		}
		if got := rec.callsCopy()[len(rec.callsCopy())-1]; got != "clear" {
			t.Fatalf("last restore step = %q, want clear (calls %v)", got, rec.callsCopy())
		}
	})

	t.Run("fallback restore centers before clear", func(t *testing.T) {
		rec := &widgetRegionRecorder{}
		ops := restoreMainWindowOps{
			clearAlwaysOnTop: func() {},
			clearIconMode:    func() error { return nil },
			setMinSize:       func(int, int) {},
			setFallbackSize:  func() { rec.add("fallback-size") },
			center:           func() { rec.add("center") },
			unmaximise:       func() { rec.add("unmaximise") },
			setBounds:        func(DesktopWindowState) error { return nil },
			maximise:         func() { rec.add("maximise") },
			clearRegion:      func() error { rec.add("clear"); return nil },
		}
		if err := runRestoreMainGeometry(DesktopWindowState{}, false, ops); err != nil {
			t.Fatal(err)
		}
		if rec.index("clear") < rec.index("center") || rec.index("clear") < rec.index("fallback-size") {
			t.Fatalf("fallback restore must clear after centering: %v", rec.callsCopy())
		}
		if got := rec.callsCopy()[len(rec.callsCopy())-1]; got != "clear" {
			t.Fatalf("last fallback restore step = %q, want clear", got)
		}
	})

	t.Run("geometry failure still clears region", func(t *testing.T) {
		rec := &widgetRegionRecorder{}
		wantBoundsErr := errors.New("bounds failed")
		ops := restoreMainWindowOps{
			clearAlwaysOnTop: func() {},
			clearIconMode:    func() error { return errors.New("icon mode failed") },
			setMinSize:       func(int, int) {},
			setFallbackSize:  func() {},
			center:           func() {},
			unmaximise:       func() {},
			setBounds:        func(DesktopWindowState) error { rec.add("bounds"); return wantBoundsErr },
			maximise:         func() {},
			clearRegion:      func() error { rec.add("clear"); return nil },
		}
		err := runRestoreMainGeometry(DesktopWindowState{Width: 1280, Height: 800, X: 0, Y: 0}, true, ops)
		if !errors.Is(err, wantBoundsErr) {
			t.Fatalf("err = %v, want joined bounds failure", err)
		}
		if rec.count("clear") != 1 {
			t.Fatalf("region clear skipped after geometry failure: %v", rec.callsCopy())
		}
	})
}

// TestExitWidgetModeEndsWithClearedRegionAfterTaskbarShow reproduces the exit
// shape behind the defect: the region re-assert must be the last native step
// after restoreMainGeometry AND after the taskbar show dance, and it must
// happen before the frontend is told the main window is live.
func TestExitWidgetModeEndsWithClearedRegionAfterTaskbarShow(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := newWidgetRegionExitApp(t, rec, true)

	if err := app.ExitWidgetMode(""); err != nil {
		t.Fatalf("ExitWidgetMode: %v", err)
	}
	if app.IsWidgetMode() {
		t.Fatal("widget mode still active after exit")
	}
	rec.waitCall(t, "emit:widget:mode")
	if rec.index("clear") < rec.index("main") || rec.index("clear") < rec.index("show") {
		t.Fatalf("region clear must follow main restore and taskbar show: %v", rec.callsCopy())
	}
	if rec.index("emit:widget:mode") < rec.index("clear") {
		t.Fatalf("main window revealed before the region re-assert: %v", rec.callsCopy())
	}
}

// TestRepeatedExitWidgetModeKeepsClearingRegion covers repeated exits: the
// transition path and the reconcile path both end with a cleared region, so a
// second exit is an idempotent recovery of a stale partial presentation.
func TestRepeatedExitWidgetModeKeepsClearingRegion(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := newWidgetRegionExitApp(t, rec, true)

	if err := app.ExitWidgetMode(""); err != nil {
		t.Fatalf("first exit: %v", err)
	}
	rec.waitCall(t, "emit:widget:mode")
	firstClear := rec.count("clear")
	if firstClear < 1 {
		t.Fatalf("first exit did not clear the region: %v", rec.callsCopy())
	}

	before := len(rec.callsCopy())
	if err := app.ExitWidgetMode(""); err != nil {
		t.Fatalf("second exit: %v", err)
	}
	// Wait until the second exit's reveal lands, then inspect its tail.
	for i := 0; i < 400 && rec.count("emit:widget:mode") < 2; i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if rec.count("emit:widget:mode") < 2 {
		t.Fatalf("second exit reveal missing: %v", rec.callsCopy())
	}
	second := rec.callsCopy()[before:]
	if app.IsWidgetMode() {
		t.Fatal("widget mode active after repeated exit")
	}
	// Reconcile path: restore main -> taskbar show -> region clear (reconcile
	// inline) -> region re-assert (exit commit). Both clears must be present.
	if rec.count("clear") <= firstClear {
		t.Fatalf("repeated exit did not re-clear the region: %v", second)
	}
	if last := second[len(second)-1]; last != "emit:widget:mode" {
		t.Fatalf("second exit must end with the reveal after clearing: %v", second)
	}
}

// seedDesktopIconWindowState makes the first icon-mode entry resolve its
// surface from the persisted state instead of falling back to the Wails
// runtime screen query, which requires a real Wails context and aborts the
// process under `go test`.
func seedDesktopIconWindowState(t *testing.T) {
	t.Helper()
	path := desktopIconWindowStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(WidgetWindowState{Width: 1080, Height: 720, X: 1300, Y: 760})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestRapidEnterExitSettlesToMainWithClearedRegion drives a fast
// enter -> exit -> enter -> exit cycle through the real orchestration and
// asserts every exit leaves the window in main mode with a final region clear.
func TestRapidEnterExitSettlesToMainWithClearedRegion(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := newWidgetRegionExitApp(t, rec, false)
	seedDesktopIconWindowState(t)

	for i := 0; i < 2; i++ {
		if _, err := app.EnterWidgetMode(); err != nil {
			t.Fatalf("enter #%d: %v", i+1, err)
		}
		if !app.IsWidgetMode() {
			t.Fatalf("enter #%d did not commit widget mode", i+1)
		}
		if err := app.ExitWidgetMode(""); err != nil {
			t.Fatalf("exit #%d: %v", i+1, err)
		}
		if app.IsWidgetMode() {
			t.Fatalf("exit #%d left widget mode active", i+1)
		}
	}
	if rec.count("clear") != 2 {
		t.Fatalf("region clear count = %d, want one per exit: %v", rec.count("clear"), rec.callsCopy())
	}
}

// TestReassertMainWindowRegionSkipsWhileWidgetMode guards the mode check: the
// post-switch refresh ticks run in both modes, and a tick must never clear the
// widget/icon clip that the current widget mode owns.
func TestReassertMainWindowRegionSkipsWhileWidgetMode(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := &App{
		widgetMode:      true,
		widgetWindowOps: &widgetWindowOps{clearRegion: func() error { rec.add("clear"); return nil }},
	}
	if err := app.reassertMainWindowRegion(); err != nil {
		t.Fatal(err)
	}
	if rec.count("clear") != 0 {
		t.Fatalf("re-assert cleared the region while widget mode owns the clip: %v", rec.callsCopy())
	}
}

// TestWidgetPostSwitchRefreshIsModeAware covers the mode-aware tick: main mode
// re-asserts the cleared HRGN, widget/icon modes repaint in place without
// touching the clip.
func TestWidgetPostSwitchRefreshIsModeAware(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := &App{widgetWindowOps: &widgetWindowOps{
		clearRegion:   func() error { rec.add("clear"); return nil },
		repaintWindow: func() error { rec.add("repaint"); return nil },
	}}
	if err := app.widgetPostSwitchRefresh(); err != nil {
		t.Fatal(err)
	}
	if got := rec.callsCopy(); !reflect.DeepEqual(got, []string{"clear"}) {
		t.Fatalf("main-mode tick calls = %v, want [clear]", got)
	}

	app.widgetMode = true
	if err := app.widgetPostSwitchRefresh(); err != nil {
		t.Fatal(err)
	}
	if got := rec.callsCopy(); !reflect.DeepEqual(got, []string{"clear", "repaint"}) {
		t.Fatalf("widget-mode tick calls = %v, want [clear repaint]", got)
	}
}

// TestLateIconHitRegionsAfterExitDoNotClipMainWindow pins the stale-request
// guarantee: a hit-region snapshot arriving after the exit committed must be
// rejected by the mode guard and never re-clip the restored main window.
func TestLateIconHitRegionsAfterExitDoNotClipMainWindow(t *testing.T) {
	rec := &widgetRegionRecorder{}
	app := &App{
		ctx:             context.Background(),
		widgetWindowOps: &widgetWindowOps{regions: func([]DesktopIconRect) error { rec.add("regions"); return nil }},
	}
	input := DesktopIconHitRegionsInput{
		Rects:   []DesktopIconRect{{X: 0, Y: 0, Width: 120, Height: 80}},
		Surface: DesktopIconSurfaceResult{Revision: 1, Width: 1080, Height: 720, X: 0, Y: 0},
	}
	if err := app.SetDesktopIconHitRegions(input); err != nil {
		t.Fatalf("late hit-region request: %v", err)
	}
	if rec.count("regions") != 0 {
		t.Fatalf("late hit-region request clipped the main window: %v", rec.callsCopy())
	}
}
