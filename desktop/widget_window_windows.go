//go:build windows

package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	gdi32                = syscall.NewLazyDLL("gdi32.dll")
	procFindWindowExW    = user32.NewProc("FindWindowExW")
	procWindowProcessID  = user32.NewProc("GetWindowThreadProcessId")
	procGetDpiForWindow  = user32.NewProc("GetDpiForWindow")
	procSetWindowRgn     = user32.NewProc("SetWindowRgn")
	procCreatePolygonRgn = gdi32.NewProc("CreatePolygonRgn")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
)

type w32Point struct {
	X int32
	Y int32
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
	if dpi == 0 {
		dpi = 96
	}
	scale := func(value int) int32 {
		return int32((value*int(dpi) + 48) / 96)
	}
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
