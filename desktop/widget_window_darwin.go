//go:build darwin

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa
#include <stdint.h>

int workGround2SetDesktopIconMode(int active);
int workGround2SetDesktopIconHitRegions(const int32_t *rects, int count);
int workGround2CurrentWorkArea(int *width, int *height);
*/
import "C"

import (
	"context"
	"fmt"
	"unsafe"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func setDesktopWindowBounds(ctx context.Context, width, height, x, y int) error {
	runtime.WindowSetSize(ctx, width, height)
	runtime.WindowSetPosition(ctx, x, y)
	return nil
}

func darwinCurrentWorkArea() (int, int, bool) {
	var width, height C.int
	if C.workGround2CurrentWorkArea(&width, &height) == 0 {
		return 0, 0, false
	}
	return int(width), int(height), width > 0 && height > 0
}

func nativeDefaultWidgetWindowState(context.Context) (WidgetWindowState, bool) {
	width, height, ok := darwinCurrentWorkArea()
	if !ok {
		return WidgetWindowState{}, false
	}
	return defaultWidgetWindowStateForScreens(width, height), true
}

func normalizeWidgetWindowState(_ context.Context, state WidgetWindowState) (WidgetWindowState, error) {
	width, height, ok := darwinCurrentWorkArea()
	if !ok {
		return state, nil
	}
	state.Width = min(state.Width, width)
	state.Height = min(state.Height, height)
	state.X = max(0, min(state.X, width-state.Width))
	state.Y = max(0, min(state.Y, height-state.Height))
	return state, nil
}

// AppKit uses a clear NSWindow rather than a native shape for the compact pager.
func setWidgetWindowRegion(int, int) error { return nil }

// Darwin click-through is controlled independently by the current icon hit
// rectangles, so resizing the transparent canvas needs no region reset.
func clearWidgetWindowRegion() error { return nil }

func setDesktopIconNativeMode(active bool) error {
	value := C.int(0)
	if active {
		value = 1
	}
	if C.workGround2SetDesktopIconMode(value) == 0 {
		return fmt.Errorf("set macOS desktop icon window mode: native window unavailable")
	}
	return nil
}

func setDesktopIconHitRegions(rects []DesktopIconRect) error {
	if len(rects) == 0 {
		return fmt.Errorf("set macOS desktop icon hit regions: no rectangles")
	}
	raw := make([]C.int32_t, 0, len(rects)*4)
	for _, rect := range rects {
		raw = append(raw, C.int32_t(rect.X), C.int32_t(rect.Y), C.int32_t(rect.Width), C.int32_t(rect.Height))
	}
	if C.workGround2SetDesktopIconHitRegions(
		(*C.int32_t)(unsafe.Pointer(&raw[0])),
		C.int(len(rects)),
	) == 0 {
		return fmt.Errorf("set macOS desktop icon hit regions: native window unavailable")
	}
	return nil
}

func defaultDarwinDesktopIconWindowState(width, height int) WidgetWindowState {
	widgetWidth := min(desktopIconWidth, max(desktopIconMinWidth, width-widgetEdgeGap*2))
	widgetHeight := min(desktopIconHeight, max(desktopIconMinHeight, height-widgetBottomGap*2))
	widgetWidth = min(widgetWidth, width)
	widgetHeight = min(widgetHeight, height)
	return WidgetWindowState{
		Width:  widgetWidth,
		Height: widgetHeight,
		X:      max(0, width-widgetWidth-widgetEdgeGap),
		Y:      max(0, height-widgetHeight-widgetBottomGap),
	}
}

func nativeDefaultDesktopIconWindowState(context.Context) (WidgetWindowState, bool) {
	width, height, ok := darwinCurrentWorkArea()
	if !ok {
		return WidgetWindowState{}, false
	}
	return defaultDarwinDesktopIconWindowState(width, height), true
}

func darwinDesktopIconSurfaceState(width, height int, input DesktopIconSurfaceInput) WidgetWindowState {
	contentWidth := max(0, input.Width) + max(0, input.Envelope)*2
	contentHeight := max(0, input.Height) + max(0, input.Envelope)*2
	targetWidth := min(max(desktopIconMinWidth, contentWidth), width)
	targetHeight := min(max(desktopIconMinHeight, contentHeight), height)
	return WidgetWindowState{
		Width:  targetWidth,
		Height: targetHeight,
		X:      max(0, width-targetWidth-widgetEdgeGap),
		Y:      max(0, height-targetHeight-widgetBottomGap),
	}
}

// applyDesktopIconSurface keeps the transparent AppKit window bounded and
// bottom-right anchored, matching the Windows icon canvas contract.
func applyDesktopIconSurface(ctx context.Context, input DesktopIconSurfaceInput) (WidgetWindowState, error) {
	width, height, ok := darwinCurrentWorkArea()
	if !ok {
		return WidgetWindowState{}, fmt.Errorf("apply macOS desktop icon surface: current screen unavailable")
	}
	state := darwinDesktopIconSurfaceState(width, height, input)
	if err := setDesktopWindowBounds(ctx, state.Width, state.Height, state.X, state.Y); err != nil {
		return WidgetWindowState{}, err
	}
	return state, nil
}

// macOS has no taskbar button equivalent for this window mode.
func setWidgetTaskbarHidden(bool) error { return nil }
