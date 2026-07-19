//go:build windows

package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestWidgetRegionPointsMatchShell(t *testing.T) {
	points, err := widgetRegionPoints(widgetMinWidth, widgetMinHeight, 96)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]w32Point{
		{17, 0}, {508, 0}, {520, 12}, {520, 147},
		{503, 160}, {17, 160}, {0, 145}, {0, 17},
	}
	if points != want {
		t.Fatalf("points = %#v, want %#v", points, want)
	}
}

func TestWidgetRegionPointsScaleForWindowDPI(t *testing.T) {
	points, err := widgetRegionPoints(764, 225, 120)
	if err != nil {
		t.Fatal(err)
	}
	want := [8]w32Point{
		{21, 0}, {940, 0}, {955, 15}, {955, 265},
		{934, 281}, {21, 281}, {0, 263}, {0, 21},
	}
	if points != want {
		t.Fatalf("points = %#v, want %#v", points, want)
	}
}

func TestWidgetRegionPointsRejectSmallWindow(t *testing.T) {
	if _, err := widgetRegionPoints(widgetMinWidth-1, widgetMinHeight, 96); err == nil {
		t.Fatal("expected width validation error")
	}
	if _, err := widgetRegionPoints(widgetMinWidth, widgetMinHeight-1, 96); err == nil {
		t.Fatal("expected height validation error")
	}
}

func TestDefaultWidgetWindowStateUsesAbsoluteWorkArea(t *testing.T) {
	work := w32Rect{Left: -1920, Top: 48, Right: 0, Bottom: 1080}
	state := defaultWidgetWindowStateForWorkArea(work, 120)
	want := WidgetWindowState{Width: 590, Height: 176, X: -758, Y: 830}
	if state != want {
		t.Fatalf("state = %#v, want %#v", state, want)
	}
}

func TestScaleForDPIRoundTrip(t *testing.T) {
	for _, value := range []int{16, 176, 590, 1536} {
		physical := scaleForDPI(value, 120)
		if got := scaleToDefaultDPI(physical, 120); got != value {
			t.Fatalf("round trip %d -> %d -> %d", value, physical, got)
		}
	}
}

func TestNormalizeWidgetStateKeepsVisiblePosition(t *testing.T) {
	monitors := []widgetMonitor{{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96}}
	state := WidgetWindowState{Width: 590, Height: 176, X: 1200, Y: 700}
	if got := normalizeWidgetStateForMonitors(state, monitors, 0); got != state {
		t.Fatalf("state = %#v, want unchanged %#v", got, state)
	}
}

func TestNormalizeWidgetStateClampsEveryEdge(t *testing.T) {
	monitor := widgetMonitor{Work: w32Rect{Left: 100, Top: 50, Right: 1700, Bottom: 950}, DPI: 96}
	tests := []struct {
		name  string
		state WidgetWindowState
		wantX int
		wantY int
	}{
		{name: "left", state: WidgetWindowState{Width: 590, Height: 176, X: 20, Y: 300}, wantX: 100, wantY: 300},
		{name: "top", state: WidgetWindowState{Width: 590, Height: 176, X: 300, Y: 0}, wantX: 300, wantY: 50},
		{name: "right", state: WidgetWindowState{Width: 590, Height: 176, X: 1500, Y: 300}, wantX: 1110, wantY: 300},
		{name: "bottom", state: WidgetWindowState{Width: 590, Height: 176, X: 300, Y: 900}, wantX: 300, wantY: 774},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeWidgetStateForMonitors(tt.state, []widgetMonitor{monitor}, 0)
			if got.X != tt.wantX || got.Y != tt.wantY {
				t.Fatalf("position = (%d,%d), want (%d,%d)", got.X, got.Y, tt.wantX, tt.wantY)
			}
		})
	}
}

func TestNormalizeWidgetStateKeepsNegativeCoordinateMonitor(t *testing.T) {
	monitors := []widgetMonitor{
		{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96},
		{Work: w32Rect{Left: -1920, Top: 40, Right: 0, Bottom: 1080}, DPI: 96},
	}
	state := WidgetWindowState{Width: 590, Height: 176, X: -1500, Y: 700}
	if got := normalizeWidgetStateForMonitors(state, monitors, 0); got != state {
		t.Fatalf("state = %#v, want unchanged %#v", got, state)
	}
}

func TestNormalizeWidgetStateFallsBackWhenMonitorWasRemoved(t *testing.T) {
	monitors := []widgetMonitor{
		{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 96},
		{Work: w32Rect{Left: -1920, Top: 0, Right: 0, Bottom: 1040}, DPI: 96},
	}
	state := WidgetWindowState{Width: 590, Height: 176, X: 3000, Y: 1500}
	got := normalizeWidgetStateForMonitors(state, monitors, 1)
	if got.X != -590 || got.Y != 864 {
		t.Fatalf("position = (%d,%d), want (-590,864)", got.X, got.Y)
	}
}

func TestNormalizeWidgetStateUsesTargetMonitorDPI(t *testing.T) {
	monitors := []widgetMonitor{{Work: w32Rect{Left: 0, Top: 0, Right: 1920, Bottom: 1040}, DPI: 144}}
	state := WidgetWindowState{Width: 590, Height: 176, X: 1500, Y: 900}
	got := normalizeWidgetStateForMonitors(state, monitors, 0)
	if got.X != 1035 || got.Y != 776 {
		t.Fatalf("position = (%d,%d), want (1035,776)", got.X, got.Y)
	}
}

func TestWidgetRedrawFlagsRefreshFrameAndChildrenImmediately(t *testing.T) {
	want := uintptr(redrawInvalidate | redrawErase | redrawAllChildren | redrawUpdateNow | redrawFrame)
	if widgetRedrawFlags != want {
		t.Fatalf("flags = %#x, want %#x", widgetRedrawFlags, want)
	}
}

func TestRunWidgetWindowRefreshOrderAndFailure(t *testing.T) {
	var calls []string
	step := func(name string, err error) func() error {
		return func() error {
			calls = append(calls, name)
			return err
		}
	}
	if err := runWidgetWindowRefresh(step("frame", nil), step("redraw", nil), step("flush", nil)); err != nil {
		t.Fatal(err)
	}
	if want := []string{"frame", "redraw", "flush"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}

	calls = nil
	wantErr := errors.New("redraw failed")
	err := runWidgetWindowRefresh(step("frame", nil), step("redraw", wantErr), step("flush", nil))
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if want := []string{"frame", "redraw"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}
