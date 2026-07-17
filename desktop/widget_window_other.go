//go:build !windows

package main

import (
	"context"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func setDesktopWindowBounds(ctx context.Context, width, height, x, y int) error {
	runtime.WindowSetSize(ctx, width, height)
	runtime.WindowSetPosition(ctx, x, y)
	return nil
}

func nativeDefaultWidgetWindowState(context.Context) (WidgetWindowState, bool) {
	return WidgetWindowState{}, false
}

// setWidgetWindowRegion is a no-op on non-Windows platforms.
func setWidgetWindowRegion(width, height int) error { return nil }

// clearWidgetWindowRegion is a no-op on non-Windows platforms.
func clearWidgetWindowRegion() error { return nil }
