//go:build windows

package main

import (
	"errors"
	"reflect"
	"strings"
	"syscall"
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

func TestDefaultDesktopIconWindowStateUsesWorkAreaAndDPI(t *testing.T) {
	work := w32Rect{Left: -1920, Top: 48, Right: 0, Bottom: 1080}
	state := defaultDesktopIconWindowStateForWorkArea(work, 120)
	want := WidgetWindowState{Width: 1080, Height: 720, X: -1370, Y: 150}
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

const (
	testAppWindowStyle  = uint32(wsExAppWindow | wsExTransparent | wsExLayered | wsExNoActivate)
	testToolWindowStyle = uint32(wsExToolWindow | wsExTransparent | wsExLayered | wsExNoActivate)
)

// fakeWidgetTaskbarOps records every operation and can fail a chosen number of
// times per operation, so rollback paths are exercised without a real window.
type fakeWidgetTaskbarOps struct {
	state     widgetTaskbarStyleState
	hwnd      syscall.Handle
	style     uint32
	visible   bool
	calls     []string
	failGet   int
	failSet   int
	failHide  int
	failShow  int
	failFrame int
}

func (f *fakeWidgetTaskbarOps) ops() widgetTaskbarOps {
	return widgetTaskbarOps{
		findHWND: func() syscall.Handle { return f.hwnd },
		getStyle: func(syscall.Handle) (uint32, error) {
			f.calls = append(f.calls, "get")
			if f.failGet > 0 {
				f.failGet--
				return 0, errors.New("get style failed")
			}
			return f.style, nil
		},
		setStyle: func(_ syscall.Handle, style uint32) error {
			f.calls = append(f.calls, "set")
			if f.failSet > 0 {
				f.failSet--
				return errors.New("set style failed")
			}
			f.style = style
			return nil
		},
		visible: func(syscall.Handle) bool {
			f.calls = append(f.calls, "visible")
			return f.visible
		},
		show: func(_ syscall.Handle, show bool) error {
			if show {
				f.calls = append(f.calls, "show")
				if f.failShow > 0 {
					f.failShow--
					return errors.New("show window failed")
				}
			} else {
				f.calls = append(f.calls, "hide")
				if f.failHide > 0 {
					f.failHide--
					return errors.New("hide window failed")
				}
			}
			f.visible = show
			return nil
		},
		frame: func(syscall.Handle) error {
			f.calls = append(f.calls, "frame")
			if f.failFrame > 0 {
				f.failFrame--
				return errors.New("frame refresh failed")
			}
			return nil
		},
	}
}

func (f *fakeWidgetTaskbarOps) apply(hide bool) error {
	return setWidgetTaskbarStyle(&f.state, f.ops(), hide)
}

func TestWidgetTaskbarTargetStylePreservesUnrelatedBits(t *testing.T) {
	unrelated := uint32(wsExTransparent | wsExControlParent | wsExLayered | wsExNoRedirectionBitmap | wsExNoActivate)
	base := unrelated | wsExAppWindow
	hidden := widgetTaskbarTargetStyle(base, true)
	if hidden&wsExAppWindow != 0 || hidden&wsExToolWindow == 0 {
		t.Fatalf("hide = %#x, want APPWINDOW removed and TOOLWINDOW added", hidden)
	}
	if hidden&unrelated != unrelated {
		t.Fatalf("hide dropped unrelated bits: %#x", hidden&unrelated)
	}
	if shown := widgetTaskbarTargetStyle(hidden, false); shown != base {
		t.Fatalf("show = %#x, want base %#x", shown, base)
	}
	if widgetTaskbarTargetStyle(base, false) != base {
		t.Fatal("show must be a no-op on an already app window")
	}
	if widgetTaskbarTargetStyle(hidden, true) != hidden {
		t.Fatal("hide must be a no-op on an already tool window")
	}
}

func TestWidgetTaskbarStyleVisibleWindowFollowsRefreshOrder(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true}
	if err := fake.apply(true); err != nil {
		t.Fatal(err)
	}
	want := []string{"get", "visible", "hide", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testToolWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleShowRestoresAppWindow(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testToolWindowStyle, visible: true}
	if err := fake.apply(false); err != nil {
		t.Fatal(err)
	}
	want := []string{"get", "visible", "hide", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleHiddenStartupNeverShows(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: false}
	if err := fake.apply(true); err != nil {
		t.Fatal(err)
	}
	want := []string{"get", "visible", "set", "frame"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v (hidden startup window must never be shown early)", fake.calls, want)
	}
	if fake.style != testToolWindowStyle || fake.visible {
		t.Fatalf("style = %#x visible = %v", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleIdempotentWhenAlreadyConsistent(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testToolWindowStyle, visible: true}
	if err := fake.apply(true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"get"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
}

func TestWidgetTaskbarStyleWindowNotFound(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 0, style: testAppWindowStyle, visible: true}
	err := fake.apply(true)
	if err == nil || !strings.Contains(err.Error(), "window not found") {
		t.Fatalf("err = %v, want window-not-found", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("no ops may run for a missing window, got %v", fake.calls)
	}
}

func TestWidgetTaskbarStyleWrappersRejectInvalidHandle(t *testing.T) {
	// Real user32 calls with an invalid handle have no window or taskbar side
	// effects; they must surface the API error instead of misreading a 0
	// return as success (GetWindowLongW/SetWindowLongW return 0 for both
	// "style is 0" and "failure").
	if _, err := widgetTaskbarGetStyle(0); err == nil || !strings.Contains(err.Error(), "GetWindowLongW") {
		t.Fatalf("GetWindowLongW(0) err = %v", err)
	}
	if err := widgetTaskbarSetStyle(0, wsExToolWindow); err == nil || !strings.Contains(err.Error(), "SetWindowLongW") {
		t.Fatalf("SetWindowLongW(0) err = %v", err)
	}
	if err := widgetTaskbarShowWindow(0, true); err == nil || !strings.Contains(err.Error(), "ShowWindow") {
		t.Fatalf("ShowWindow(0) err = %v", err)
	}
}

func TestWidgetTaskbarStyleGetFailureChangesNothing(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failGet: 1}
	err := fake.apply(true)
	if err == nil {
		t.Fatal("expected read failure")
	}
	if want := []string{"get"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleHideFailureChangesNothing(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failHide: 1}
	err := fake.apply(true)
	if err == nil || !strings.Contains(err.Error(), "hide window before style change") {
		t.Fatalf("err = %v, want pre-change hide failure", err)
	}
	if want := []string{"get", "visible", "hide", "show", "visible"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleSetStyleFailureRollsBack(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failSet: 1}
	err := fake.apply(true)
	if err == nil || !strings.Contains(err.Error(), "apply hide style") {
		t.Fatalf("err = %v, want named style step failure", err)
	}
	// Pre-change hide, failed set, then rollback: set(original) + frame + show.
	want := []string{"get", "visible", "hide", "set", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v, want original restored", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleFrameFailureRollsBack(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failFrame: 1}
	err := fake.apply(true)
	if err == nil || !strings.Contains(err.Error(), "refresh frame cache") {
		t.Fatalf("err = %v, want named frame step failure", err)
	}
	want := []string{"get", "visible", "hide", "set", "frame", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v, want original restored", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleShowFailureRollsBack(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failShow: 1}
	err := fake.apply(true)
	if err == nil || !strings.Contains(err.Error(), "show window after style change") {
		t.Fatalf("err = %v, want named show step failure", err)
	}
	want := []string{"get", "visible", "hide", "set", "frame", "show", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.style != testAppWindowStyle || !fake.visible {
		t.Fatalf("style = %#x visible = %v, want original restored", fake.style, fake.visible)
	}
}

func TestWidgetTaskbarStyleTwoShowFailuresRetryRestoresVisibility(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{
		hwnd:     1,
		style:    testAppWindowStyle,
		visible:  true,
		failShow: 2,
	}
	if err := fake.apply(true); err == nil {
		t.Fatal("expected final and rollback Show failures")
	}
	if fake.visible || !fake.state.pendingVisible {
		t.Fatalf("visible=%v pending=%v, want retained visible intent", fake.visible, fake.state.pendingVisible)
	}

	fake.calls = nil
	if err := fake.apply(true); err != nil {
		t.Fatalf("retry: %v", err)
	}
	want := []string{"get", "visible", "set", "frame", "show", "visible"}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("retry calls = %v, want %v", fake.calls, want)
	}
	if !fake.visible || fake.state.pendingVisible || fake.style != testToolWindowStyle {
		t.Fatalf("style=%#x visible=%v pending=%v", fake.style, fake.visible, fake.state.pendingVisible)
	}
}

func TestWidgetTaskbarStyleMatchingTargetStillRestoresPendingVisibility(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testToolWindowStyle}
	fake.state.hwnd = fake.hwnd
	fake.state.pendingVisible = true

	if err := fake.apply(true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"get", "show", "visible"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if !fake.visible || fake.state.pendingVisible {
		t.Fatalf("visible=%v pending=%v", fake.visible, fake.state.pendingVisible)
	}
}

func TestWidgetTaskbarStyleHWNDChangeDropsStaleVisibilityIntent(t *testing.T) {
	fake := &fakeWidgetTaskbarOps{hwnd: 2, style: testToolWindowStyle}
	fake.state.hwnd = 1
	fake.state.pendingVisible = true

	if err := fake.apply(true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"get"}; !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("calls = %v, want %v", fake.calls, want)
	}
	if fake.visible || fake.state.pendingVisible {
		t.Fatal("a replacement HWND inherited stale visibility intent")
	}
}

func TestWidgetTaskbarStyleRollbackFailureStillSurfacesPrimaryError(t *testing.T) {
	// The primary style change and every rollback step fail: the caller must
	// still see the primary error joined with each rollback error.
	fake := &fakeWidgetTaskbarOps{hwnd: 1, style: testAppWindowStyle, visible: true, failSet: 2, failFrame: 5, failShow: 1}
	err := fake.apply(true)
	if err == nil {
		t.Fatal("expected failure")
	}
	for _, want := range []string{"apply hide style", "restore window style", "restore frame cache", "restore window visibility"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err %v does not mention %q", err, want)
		}
	}
	if fake.style != testAppWindowStyle {
		t.Fatalf("style = %#x, want original preserved", fake.style)
	}
}
