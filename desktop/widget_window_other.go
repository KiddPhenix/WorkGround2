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

func normalizeWidgetWindowState(_ context.Context, state WidgetWindowState) (WidgetWindowState, error) {
	return state, nil
}

// setWidgetWindowRegion is a no-op on non-Windows platforms.
func setWidgetWindowRegion(width, height int) error { return nil }

// clearWidgetWindowRegion is a no-op on non-Windows platforms.
func clearWidgetWindowRegion() error { return nil }

func setDesktopIconHitRegions([]DesktopIconRect) error { return nil }

func nativeDefaultDesktopIconWindowState(context.Context) (WidgetWindowState, bool) {
	return WidgetWindowState{}, false
}

// applyDesktopIconSurface is a no-op on non-Windows platforms: the bounded
// transparent surface (and its DWM/GPU cost) is Windows-only. It still reports
// the clamped size so the frontend contract stays uniform.
func applyDesktopIconSurface(_ context.Context, input DesktopIconSurfaceInput) (WidgetWindowState, error) {
	input.Width = max(0, input.Width)
	input.Height = max(0, input.Height)
	input.Envelope = max(0, input.Envelope)
	return WidgetWindowState{
		Width:  min(desktopIconWidth, max(desktopIconMinWidth, input.Width+input.Envelope*2)),
		Height: min(desktopIconHeight, max(desktopIconMinHeight, input.Height+input.Envelope*2)),
	}, nil
}

// setWidgetTaskbarHidden is a no-op on non-Windows platforms: there is no
// Windows taskbar button to hide from.
func setWidgetTaskbarHidden(bool) error { return nil }
