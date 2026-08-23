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
