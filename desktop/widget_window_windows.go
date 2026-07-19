//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	user32                = syscall.NewLazyDLL("user32.dll")
	gdi32                 = syscall.NewLazyDLL("gdi32.dll")
	shcore                = syscall.NewLazyDLL("shcore.dll")
	dwmapi                = syscall.NewLazyDLL("dwmapi.dll")
	procFindWindowExW     = user32.NewProc("FindWindowExW")
	procWindowProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procGetDpiForWindow   = user32.NewProc("GetDpiForWindow")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procRedrawWindow      = user32.NewProc("RedrawWindow")
	procEnumDisplay       = user32.NewProc("EnumDisplayMonitors")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
	procSetWindowRgn      = user32.NewProc("SetWindowRgn")
	procCreatePolygonRgn  = gdi32.NewProc("CreatePolygonRgn")
	procDeleteObject      = gdi32.NewProc("DeleteObject")
	procGetDpiForMonitor  = shcore.NewProc("GetDpiForMonitor")
	procDwmFlush          = dwmapi.NewProc("DwmFlush")
)

type w32Point struct {
	X int32
	Y int32
}

type w32Rect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type w32MonitorInfo struct {
	Size    uint32
	Monitor w32Rect
	Work    w32Rect
	Flags   uint32
}

const (
	monitorDefaultNearest = 2
	windowPosNoSize       = 0x0001
	windowPosNoMove       = 0x0002
	windowPosNoZOrder     = 0x0004
	windowPosNoActivate   = 0x0010
	windowPosFrameChanged = 0x0020
	windowPosNoOwnerOrder = 0x0200
	redrawInvalidate      = 0x0001
	redrawErase           = 0x0004
	redrawAllChildren     = 0x0080
	redrawUpdateNow       = 0x0100
	redrawFrame           = 0x0400
	widgetRedrawFlags     = redrawInvalidate | redrawErase | redrawAllChildren | redrawUpdateNow | redrawFrame
)

type widgetMonitor struct {
	Handle  uintptr
	Work    w32Rect
	DPI     uint32
	Primary bool
}

// findWidgetHWND returns this process's Wails window. Filtering by process ID
// prevents one WorkGround2 instance from clipping another instance's window.
func findWidgetHWND() syscall.Handle {
	title, err := syscall.UTF16PtrFromString("WorkGround2")
	if err != nil {
		return 0
	}
	pid := uint32(os.Getpid())
	var after uintptr
	for {
		hwnd, _, _ := procFindWindowExW.Call(0, after, 0, uintptr(unsafe.Pointer(title)))
		if hwnd == 0 {
			return 0
		}
		var owner uint32
		procWindowProcessID.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
		if owner == pid {
			return syscall.Handle(hwnd)
		}
		after = hwnd
	}
}

func widgetRegionPoints(width, height int, dpi uint32) ([8]w32Point, error) {
	if width < widgetMinWidth || height < widgetMinHeight {
		return [8]w32Point{}, fmt.Errorf("widget window too small (%dx%d)", width, height)
	}
	scale := func(value int) int32 { return int32(scaleForDPI(value, dpi)) }
	return [8]w32Point{
		{scale(17), 0},
		{scale(width - 12), 0},
		{scale(width), scale(12)},
		{scale(width), scale(height - 13)},
		{scale(width - 17), scale(height)},
		{scale(17), scale(height)},
		{0, scale(height - 15)},
		{0, scale(17)},
	}, nil
}

func scaleForDPI(value int, dpi uint32) int {
	if dpi == 0 {
		dpi = 96
	}
	return (value*int(dpi) + 48) / 96
}

func scaleToDefaultDPI(value int, dpi uint32) int {
	if dpi == 0 {
		dpi = 96
	}
	return (value*96 + int(dpi)/2) / int(dpi)
}

func widgetStateRect(state WidgetWindowState, dpi uint32) w32Rect {
	return w32Rect{
		Left:   int32(state.X),
		Top:    int32(state.Y),
		Right:  int32(state.X + scaleForDPI(state.Width, dpi)),
		Bottom: int32(state.Y + scaleForDPI(state.Height, dpi)),
	}
}

func rectIntersectionArea(a, b w32Rect) int64 {
	left := max(a.Left, b.Left)
	top := max(a.Top, b.Top)
	right := min(a.Right, b.Right)
	bottom := min(a.Bottom, b.Bottom)
	if right <= left || bottom <= top {
		return 0
	}
	return int64(right-left) * int64(bottom-top)
}

func clampWidgetStateToMonitor(state WidgetWindowState, monitor widgetMonitor) WidgetWindowState {
	workWidth := int(monitor.Work.Right - monitor.Work.Left)
	workHeight := int(monitor.Work.Bottom - monitor.Work.Top)
	state.Width = min(state.Width, max(widgetMinWidth, scaleToDefaultDPI(workWidth, monitor.DPI)))
	state.Height = min(state.Height, max(widgetMinHeight, scaleToDefaultDPI(workHeight, monitor.DPI)))
	width := scaleForDPI(state.Width, monitor.DPI)
	height := scaleForDPI(state.Height, monitor.DPI)
	maxX := int(monitor.Work.Right) - width
	maxY := int(monitor.Work.Bottom) - height
	state.X = max(int(monitor.Work.Left), min(state.X, maxX))
	state.Y = max(int(monitor.Work.Top), min(state.Y, maxY))
	return state
}

func normalizeWidgetStateForMonitors(state WidgetWindowState, monitors []widgetMonitor, fallback int) WidgetWindowState {
	if len(monitors) == 0 {
		return state
	}
	if fallback < 0 || fallback >= len(monitors) {
		fallback = 0
	}
	selected := fallback
	var bestArea int64
	for i, monitor := range monitors {
		area := rectIntersectionArea(widgetStateRect(state, monitor.DPI), monitor.Work)
		if area > bestArea {
			bestArea = area
			selected = i
		}
	}
	return clampWidgetStateToMonitor(state, monitors[selected])
}

func monitorDPI(handle uintptr, fallback uint32) uint32 {
	var x, y uint32
	hr, _, _ := procGetDpiForMonitor.Call(handle, 0, uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y)))
	if hr == 0 && x > 0 {
		return x
	}
	if fallback == 0 {
		return 96
	}
	return fallback
}

func normalizeWidgetWindowState(_ context.Context, state WidgetWindowState) (WidgetWindowState, error) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: window not found")
	}
	fallbackDPI, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	current, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	monitors := make([]widgetMonitor, 0, 4)
	callback := syscall.NewCallback(func(handle, _, _, _ uintptr) uintptr {
		info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
		ret, _, _ := procGetMonitorInfoW.Call(handle, uintptr(unsafe.Pointer(&info)))
		if ret != 0 {
			monitors = append(monitors, widgetMonitor{
				Handle:  handle,
				Work:    info.Work,
				DPI:     monitorDPI(handle, uint32(fallbackDPI)),
				Primary: info.Flags&1 != 0,
			})
		}
		return 1
	})
	ret, _, callErr := procEnumDisplay.Call(0, 0, callback, 0)
	if ret == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: enumerate monitors failed: %w", callErr)
	}
	if len(monitors) == 0 {
		return state, fmt.Errorf("normalizeWidgetWindowState: no visible monitors")
	}
	fallback := -1
	for i, monitor := range monitors {
		if monitor.Handle == current {
			fallback = i
			break
		}
		if fallback < 0 && monitor.Primary {
			fallback = i
		}
	}
	return normalizeWidgetStateForMonitors(state, monitors, fallback), nil
}

func runWidgetWindowRefresh(frame, redraw, flush func() error) error {
	if err := frame(); err != nil {
		return err
	}
	if err := redraw(); err != nil {
		return err
	}
	return flush()
}

func redrawWidgetWindow(hwnd syscall.Handle) error {
	return runWidgetWindowRefresh(
		func() error {
			flags := uintptr(windowPosNoMove | windowPosNoSize | windowPosNoZOrder | windowPosNoActivate | windowPosFrameChanged | windowPosNoOwnerOrder)
			ret, _, callErr := procSetWindowPos.Call(uintptr(hwnd), 0, 0, 0, 0, 0, flags)
			if ret == 0 {
				return fmt.Errorf("redrawWidgetWindow: frame refresh failed: %w", callErr)
			}
			return nil
		},
		func() error {
			ret, _, callErr := procRedrawWindow.Call(uintptr(hwnd), 0, 0, widgetRedrawFlags)
			if ret == 0 {
				return fmt.Errorf("redrawWidgetWindow: redraw failed: %w", callErr)
			}
			return nil
		},
		func() error {
			hr, _, _ := procDwmFlush.Call()
			if hr != 0 {
				return fmt.Errorf("redrawWidgetWindow: DwmFlush failed: HRESULT 0x%x", hr)
			}
			return nil
		},
	)
}

// setDesktopWindowBounds bypasses Wails' Windows SetPos implementation. Wails
// reads absolute screen coordinates but writes monitor-work-area-relative
// coordinates, so persisting and restoring its values drifts on every cycle.
// Width and height remain Wails logical units; x and y are absolute pixels.
func setDesktopWindowBounds(_ context.Context, width, height, x, y int) error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("setDesktopWindowBounds: window not found")
	}
	flags := uintptr(windowPosNoZOrder | windowPosNoActivate)
	ret, _, callErr := procSetWindowPos.Call(uintptr(hwnd), 0, uintptr(x), uintptr(y), 0, 0, flags|windowPosNoSize)
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: move failed: %w", callErr)
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	physicalWidth := scaleForDPI(width, uint32(dpi))
	physicalHeight := scaleForDPI(height, uint32(dpi))
	ret, _, callErr = procSetWindowPos.Call(
		uintptr(hwnd), 0, uintptr(x), uintptr(y), uintptr(physicalWidth), uintptr(physicalHeight), flags,
	)
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: resize failed: %w", callErr)
	}
	var rect w32Rect
	ret, _, callErr = procGetWindowRect.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return fmt.Errorf("setDesktopWindowBounds: verify failed: %w", callErr)
	}
	if !nearWindowValue(int(rect.Left), x) || !nearWindowValue(int(rect.Top), y) ||
		!nearWindowValue(int(rect.Right-rect.Left), physicalWidth) || !nearWindowValue(int(rect.Bottom-rect.Top), physicalHeight) {
		return fmt.Errorf(
			"setDesktopWindowBounds: got (%d,%d %dx%d), want (%d,%d %dx%d)",
			rect.Left, rect.Top, rect.Right-rect.Left, rect.Bottom-rect.Top,
			x, y, physicalWidth, physicalHeight,
		)
	}
	return nil
}

func nearWindowValue(got, want int) bool {
	delta := got - want
	return delta >= -1 && delta <= 1
}

func nativeDefaultWidgetWindowState(_ context.Context) (WidgetWindowState, bool) {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return WidgetWindowState{}, false
	}
	monitor, _, _ := procMonitorFromWindow.Call(uintptr(hwnd), monitorDefaultNearest)
	if monitor == 0 {
		return WidgetWindowState{}, false
	}
	info := w32MonitorInfo{Size: uint32(unsafe.Sizeof(w32MonitorInfo{}))}
	ret, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return WidgetWindowState{}, false
	}
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	return defaultWidgetWindowStateForWorkArea(info.Work, uint32(dpi)), true
}

func defaultWidgetWindowStateForWorkArea(work w32Rect, dpi uint32) WidgetWindowState {
	logicalWidth := scaleToDefaultDPI(int(work.Right-work.Left), dpi)
	logicalHeight := scaleToDefaultDPI(int(work.Bottom-work.Top), dpi)
	state := defaultWidgetWindowStateForScreens(logicalWidth, logicalHeight)
	state.X = int(work.Right) - scaleForDPI(state.Width+widgetEdgeGap, dpi)
	state.Y = int(work.Bottom) - scaleForDPI(state.Height+widgetBottomGap, dpi)
	return state
}

// setWidgetWindowRegion clips the native window to the same octagonal shape as
// the CSS clip-path on .widget-shell. width and height come from Wails window
// coordinates; SetWindowRgn needs them scaled by the target window DPI.
// Idempotent — subsequent calls replace the previous region.
func setWidgetWindowRegion(width, height int) error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("setWidgetWindowRegion: window not found")
	}
	// Polygon matching the CSS clip-path octagon, mapped from 200% shell-space
	// to native window coordinates (÷2).  Points are window-relative.
	dpi, _, _ := procGetDpiForWindow.Call(uintptr(hwnd))
	points, err := widgetRegionPoints(width, height, uint32(dpi))
	if err != nil {
		return fmt.Errorf("setWidgetWindowRegion: %w", err)
	}

	hrgn, _, _ := procCreatePolygonRgn.Call(
		uintptr(unsafe.Pointer(&points[0])),
		8,
		1, // ALTERNATE; fill mode is equivalent for this convex polygon.
	)
	if hrgn == 0 {
		return fmt.Errorf("setWidgetWindowRegion: CreatePolygonRgn failed")
	}

	// bRedraw=TRUE → Windows takes ownership of the region and repaints.
	// The old region (if any) is freed by the system.
	ret, _, _ := procSetWindowRgn.Call(uintptr(hwnd), hrgn, 1)
	if ret == 0 {
		procDeleteObject.Call(hrgn)
		return fmt.Errorf("setWidgetWindowRegion: SetWindowRgn failed")
	}
	return redrawWidgetWindow(hwnd)
}

// clearWidgetWindowRegion restores the native window to a full rectangle.
// Idempotent — safe to call even when no region is active.
func clearWidgetWindowRegion() error {
	hwnd := findWidgetHWND()
	if hwnd == 0 {
		return fmt.Errorf("clearWidgetWindowRegion: window not found")
	}
	// NULL region → full rectangular window.
	ret, _, _ := procSetWindowRgn.Call(uintptr(hwnd), 0, 1)
	if ret == 0 {
		return fmt.Errorf("clearWidgetWindowRegion: SetWindowRgn failed")
	}
	return redrawWidgetWindow(hwnd)
}
