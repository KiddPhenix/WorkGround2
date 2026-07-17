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
	procFindWindowExW     = user32.NewProc("FindWindowExW")
	procWindowProcessID   = user32.NewProc("GetWindowThreadProcessId")
	procGetDpiForWindow   = user32.NewProc("GetDpiForWindow")
	procGetWindowRect     = user32.NewProc("GetWindowRect")
	procSetWindowPos      = user32.NewProc("SetWindowPos")
	procMonitorFromWindow = user32.NewProc("MonitorFromWindow")
	procGetMonitorInfoW   = user32.NewProc("GetMonitorInfoW")
	procSetWindowRgn      = user32.NewProc("SetWindowRgn")
	procCreatePolygonRgn  = gdi32.NewProc("CreatePolygonRgn")
	procDeleteObject      = gdi32.NewProc("DeleteObject")
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
	windowPosNoZOrder     = 0x0004
	windowPosNoActivate   = 0x0010
)

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
	return nil
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
	return nil
}
