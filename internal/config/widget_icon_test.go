package config

import "testing"

func TestDesktopWidgetStyleAndHoverDelay(t *testing.T) {
	cfg := &Config{}
	if got := cfg.DesktopWidgetStyle(); got != "icons" {
		t.Fatalf("default style = %q, want icons", got)
	}
	if got := cfg.DesktopHoverStatusDelayMs(); got != 1200 {
		t.Fatalf("default delay = %d", got)
	}
	// Setting icons is idempotent and always succeeds.
	for i := 0; i < 2; i++ {
		if err := cfg.SetDesktopWidgetStyle("icons"); err != nil {
			t.Fatal(err)
		}
		if got := cfg.DesktopWidgetStyle(); got != "icons" {
			t.Fatalf("style = %q, want icons", got)
		}
	}
	if err := cfg.SetDesktopHoverStatusDelayMs(0); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DesktopHoverStatusDelayMs(); got != 0 {
		t.Fatalf("delay = %d", got)
	}
	// New writes may never select pager or any unknown/empty style.
	for _, style := range []string{"pager", "PAGER", "dock", ""} {
		if err := cfg.SetDesktopWidgetStyle(style); err == nil {
			t.Fatalf("invalid style %q accepted", style)
		}
	}
	// Whitespace-padded "icons" trims and stays idempotent.
	if err := cfg.SetDesktopWidgetStyle(" icons "); err != nil {
		t.Fatalf("trimmed icons rejected: %v", err)
	}
	if err := cfg.SetDesktopHoverStatusDelayMs(10001); err == nil {
		t.Fatal("invalid delay accepted")
	}
}

func TestDesktopWidgetStyleNormalizesLegacyValues(t *testing.T) {
	// Legacy persisted "pager", empty and unknown values all normalize to
	// icons on read, so old configurations never enter the pager at runtime.
	for _, raw := range []string{"pager", "", "icons", "ICONS", "dock"} {
		cfg := &Config{Desktop: DesktopConfig{WidgetStyle: raw}}
		if got := cfg.DesktopWidgetStyle(); got != "icons" {
			t.Fatalf("raw %q normalizes to %q, want icons", raw, got)
		}
	}
}
