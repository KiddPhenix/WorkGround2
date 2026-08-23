//go:build darwin

package main

import (
	"os"
	"strings"
	"testing"
)

func TestDarwinNativeWidgetClearsAndRestoresWebViewUnderPageBackground(t *testing.T) {
	source, err := os.ReadFile("widget_window_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		`setValue:[NSColor clearColor] forKey:@"underPageBackgroundColor"`,
		`setValue:@NO forKey:@"drawsBackground"`,
		`workGround2EnsureWebViewUnderPageTransparent(window)`,
		`if (!workGround2ColorIsTransparent([window backgroundColor]))`,
		`workGround2RestoreWebViewUnderPageBackground(window)`,
	} {
		if !strings.Contains(text, contract) {
			t.Fatalf("Darwin native widget source missing WebKit transparency contract %q", contract)
		}
	}
}

func TestDarwinNativeWidgetRemovesAndRestoresWindowFrame(t *testing.T) {
	source, err := os.ReadFile("widget_window_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		`workGround2SavedStyleMask = [window styleMask]`,
		`[window setStyleMask:NSWindowStyleMaskBorderless]`,
		`[window setStyleMask:workGround2SavedStyleMask]`,
		`[window setHasShadow:NO]`,
	} {
		if !strings.Contains(text, contract) {
			t.Fatalf("Darwin native widget source missing borderless-window contract %q", contract)
		}
	}
}

func TestDarwinNativeTrafficLightsRouteWidgetActionsAndRestore(t *testing.T) {
	source, err := os.ReadFile("widget_window_darwin.m")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, contract := range []string{
		`standardWindowButton:NSWindowMiniaturizeButton`,
		`standardWindowButton:NSWindowCloseButton`,
		`[mini setAction:@selector(minimiseToWidget:)]`,
		`[close setAction:@selector(dismissToWidget:)]`,
		`workGround2HandleNativeWindowAction(1)`,
		`workGround2HandleNativeWindowAction(2)`,
		`[menuItem setAction:@selector(minimiseToWidget:)]`,
		`[mini setAction:workGround2SavedMiniAction]`,
		`[close setAction:workGround2SavedCloseAction]`,
		`[workGround2MinimiseMenuItem setAction:workGround2SavedMenuAction]`,
		`setAccessibilityHelp:@"保留当前任务图标并切换到桌面小组件"`,
		`setAccessibilityHelp:@"移除当前任务图标并切换到桌面小组件"`,
	} {
		if !strings.Contains(text, contract) {
			t.Fatalf("Darwin native window controls missing contract %q", contract)
		}
	}
	start := strings.Index(text, "int workGround2ConfigureNativeWindowControls")
	if start < 0 {
		t.Fatal("Darwin native window control install section not found")
	}
	endOffset := strings.Index(text[start:], "void workGround2RestoreNativeWindowControls")
	if endOffset < 0 {
		t.Fatal("Darwin native window control restore section not found")
	}
	if strings.Contains(text[start:start+endOffset], "NSWindowZoomButton") {
		t.Fatal("Darwin bridge must not override the native green zoom button")
	}
}

func TestDefaultDarwinDesktopIconWindowStateIsBoundedAndAnchored(t *testing.T) {
	state := defaultDarwinDesktopIconWindowState(1512, 950)
	if state.Width != desktopIconWidth || state.Height != desktopIconHeight {
		t.Fatalf("default size = %dx%d, want %dx%d", state.Width, state.Height, desktopIconWidth, desktopIconHeight)
	}
	if state.X != 1512-desktopIconWidth-widgetEdgeGap || state.Y != 950-desktopIconHeight-widgetBottomGap {
		t.Fatalf("default position = (%d,%d), want bottom-right gaps", state.X, state.Y)
	}

	small := defaultDarwinDesktopIconWindowState(700, 580)
	if small.Width > 700 || small.Height > 580 || small.X < 0 || small.Y < 0 {
		t.Fatalf("small-screen state escapes work area: %+v", small)
	}
}

func TestDarwinDesktopIconSurfaceStateClampsAndKeepsBottomRightGaps(t *testing.T) {
	state := darwinDesktopIconSurfaceState(1512, 950, DesktopIconSurfaceInput{
		Width: 720, Height: 420, Envelope: 20,
	})
	if state.Width != 760 || state.Height != desktopIconMinHeight {
		t.Fatalf("surface size = %dx%d, want 760x%d", state.Width, state.Height, desktopIconMinHeight)
	}
	if state.X != 1512-state.Width-widgetEdgeGap || state.Y != 950-state.Height-widgetBottomGap {
		t.Fatalf("surface position = (%d,%d), want bottom-right gaps", state.X, state.Y)
	}

	clamped := darwinDesktopIconSurfaceState(900, 620, DesktopIconSurfaceInput{
		Width: 1200, Height: 900, Envelope: 40,
	})
	if clamped.Width != 900 || clamped.Height != 620 || clamped.X != 0 || clamped.Y != 0 {
		t.Fatalf("clamped surface = %+v, want full available work area", clamped)
	}
}
