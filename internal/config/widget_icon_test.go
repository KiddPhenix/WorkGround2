package config

import "testing"

func TestDesktopWidgetStyleAndHoverDelay(t *testing.T) {
	cfg := &Config{}
	if got := cfg.DesktopWidgetStyle(); got != "pager" {
		t.Fatalf("default style = %q", got)
	}
	if got := cfg.DesktopHoverStatusDelayMs(); got != 1200 {
		t.Fatalf("default delay = %d", got)
	}
	if err := cfg.SetDesktopWidgetStyle("icons"); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DesktopWidgetStyle(); got != "icons" {
		t.Fatalf("style = %q", got)
	}
	if err := cfg.SetDesktopHoverStatusDelayMs(0); err != nil {
		t.Fatal(err)
	}
	if got := cfg.DesktopHoverStatusDelayMs(); got != 0 {
		t.Fatalf("delay = %d", got)
	}
	if err := cfg.SetDesktopWidgetStyle("dock"); err == nil {
		t.Fatal("invalid style accepted")
	}
	if err := cfg.SetDesktopHoverStatusDelayMs(10001); err == nil {
		t.Fatal("invalid delay accepted")
	}
}
