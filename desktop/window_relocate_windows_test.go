//go:build windows

package main

import "testing"

func TestClampMainWindowStateKeepsVisibleGeometry(t *testing.T) {
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96}
	state := DesktopWindowState{Width: 1240, Height: 800, X: 120, Y: 80}
	if got := clampMainWindowStateToMonitor(state, monitor); got != state {
		t.Fatalf("got %#v, want unchanged %#v", got, state)
	}
}

func TestClampMainWindowStateFitsOversizedWindow(t *testing.T) {
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96}
	state := DesktopWindowState{Width: 2560, Height: 1440, X: -300, Y: -200}
	want := DesktopWindowState{Width: 1920, Height: 1040, X: 0, Y: 0}
	if got := clampMainWindowStateToMonitor(state, monitor); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestClampMainWindowStateNegativeCoordinateMonitor(t *testing.T) {
	// A monitor left of the primary has valid negative origins; the window must
	// clamp inside that monitor instead of snapping to 0.
	monitor := widgetMonitor{Work: w32Rect{Left: -1920, Top: 0, Right: 0, Bottom: 1040}, DPI: 96}
	state := DesktopWindowState{Width: 1280, Height: 800, X: -1500, Y: 100}
	if got := clampMainWindowStateToMonitor(state, monitor); got != state {
		t.Fatalf("got %#v, want unchanged %#v", got, state)
	}
	oversized := DesktopWindowState{Width: 2560, Height: 1440, X: -1600, Y: 500}
	got := clampMainWindowStateToMonitor(oversized, monitor)
	if got.Width != 1920 || got.Height != 1040 {
		t.Fatalf("oversized window not fitted: got %#v", got)
	}
	if got.X < -1920 || got.Y < 0 || got.X+got.Width > 0 || got.Y+got.Height > 1040 {
		t.Fatalf("oversized window leaves the negative work area: %#v", got)
	}
}

func TestClampMainWindowStateSmallWorkAreaWins(t *testing.T) {
	// A work area below the readable minimum still wins: the window shrinks to
	// fit so nothing stays unreachable after a resolution drop.
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 600, Bottom: 500}, DPI: 96}
	state := DesktopWindowState{Width: 1280, Height: 800, X: 50, Y: 60}
	got := clampMainWindowStateToMonitor(state, monitor)
	if got != (DesktopWindowState{Width: 600, Height: 500, X: 0, Y: 0}) {
		t.Fatalf("got %#v, want 600x500@0,0", got)
	}
}

func TestClampMainWindowStateRaisesBelowNativeMinimum(t *testing.T) {
	// Sizes below the native 760x480 minimum are raised to it so the recovery
	// yields a geometry restoreMainGeometry can actually apply.
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96}
	state := DesktopWindowState{Width: 700, Height: 450, X: 30, Y: 40}
	want := DesktopWindowState{Width: mainWindowMinWidth, Height: mainWindowMinHeight, X: 30, Y: 40}
	if got := clampMainWindowStateToMonitor(state, monitor); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestClampMainWindowStateSmallAreaBoundedMinimum(t *testing.T) {
	// A work area below the native minimum is the bound: the window cannot hold
	// 760px, so it is raised toward the minimum only as far as the work area
	// allows instead of overflowing it.
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 700, Bottom: 500}, DPI: 96}
	state := DesktopWindowState{Width: 500, Height: 400, X: 10, Y: 20}
	got := clampMainWindowStateToMonitor(state, monitor)
	if got != (DesktopWindowState{Width: 700, Height: 480, X: 0, Y: 20}) {
		t.Fatalf("got %#v, want 700x480 bounded inside the work area", got)
	}
	if got.Width > 700 || got.Height > 500 || got.X < 0 || got.Y < 0 ||
		got.X+got.Width > 700 || got.Y+got.Height > 500 {
		t.Fatalf("bounded result %#v still leaves the work area", got)
	}
}

func TestClampMainWindowStateHighDPI(t *testing.T) {
	// 125% DPI: the physical 1920x1080 work area is 1536x864 logical units.
	monitor := widgetMonitor{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1080}, DPI: 120}
	state := DesktopWindowState{Width: 1800, Height: 1000, X: 100, Y: 50}
	got := clampMainWindowStateToMonitor(state, monitor)
	if got != (DesktopWindowState{Width: 1536, Height: 864, X: 0, Y: 0}) {
		t.Fatalf("got %#v, want 1536x864@0,0", got)
	}
}

func TestNormalizeMainWindowStateSelectsBestMonitor(t *testing.T) {
	monitors := []widgetMonitor{
		{Work: w32Rect{Left: -1920, Top: 0, Right: 0, Bottom: 1040}, DPI: 96},
		{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96, Primary: true},
	}
	// Fully inside the left monitor: the origin stays negative and nothing moves.
	state := DesktopWindowState{Width: 1280, Height: 800, X: -1500, Y: 100}
	if got := normalizeMainWindowStateForMonitors(state, monitors, 1); got != state {
		t.Fatalf("visible window moved: got %#v, want %#v", got, state)
	}
}

func TestNormalizeMainWindowStateFallsBackWhenRemovedDisplay(t *testing.T) {
	monitors := []widgetMonitor{
		{Work: w32Rect{Left: -1920, Top: 0, Right: 0, Bottom: 1040}, DPI: 96},
		{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96, Primary: true},
	}
	// Window left behind on a removed third monitor: no intersection anywhere,
	// so the primary fallback pulls it back into the visible work area.
	state := DesktopWindowState{Width: 1280, Height: 800, X: 5000, Y: 3000}
	want := DesktopWindowState{Width: 1280, Height: 800, X: 640, Y: 240}
	if got := normalizeMainWindowStateForMonitors(state, monitors, 1); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestNormalizeMainWindowStateClampsNegativeOffscreenOrigin(t *testing.T) {
	monitors := []widgetMonitor{
		{Work: w32Rect{Left: -1920, Top: 0, Right: 0, Bottom: 1040}, DPI: 96},
		{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96, Primary: true},
	}
	// Fully beyond the left edge of the left monitor: only a sliver still
	// intersects it, so the window is clamped back inside that monitor.
	state := DesktopWindowState{Width: 1280, Height: 800, X: -3000, Y: -500}
	want := DesktopWindowState{Width: 1280, Height: 800, X: -1920, Y: 0}
	if got := normalizeMainWindowStateForMonitors(state, monitors, 1); got != want {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
