package config

import "testing"

func TestDefaultAutoPlanOff(t *testing.T) {
	if got := Default().Agent.AutoPlan; got != "off" {
		t.Fatalf("default auto_plan = %q, want off", got)
	}
}

func TestDefaultReasoningLanguageAuto(t *testing.T) {
	if got := Default().ReasoningLanguage(); got != "auto" {
		t.Fatalf("default reasoning_language = %q, want auto", got)
	}
}

func TestDefaultMemoryCompilerEnabled(t *testing.T) {
	cfg := Default()
	if !cfg.MemoryCompilerEnabled() {
		t.Fatal("default memory compiler = false, want true")
	}
	if got := cfg.MemoryCompilerVerbosity(); got != MemoryCompilerVerbosityObserve {
		t.Fatalf("default memory compiler verbosity = %q, want observe", got)
	}
}

func TestAnchoredBootstrapDefaultsEnabled(t *testing.T) {
	cfg := Default()
	if !cfg.AnchoredBootstrapEnabled() {
		t.Fatal("anchored_bootstrap must default to enabled for deepseek providers")
	}
	off := false
	cfg.Agent.AnchoredBootstrap = &off
	if cfg.AnchoredBootstrapEnabled() {
		t.Fatal("anchored_bootstrap = false must disable the mechanism")
	}
}

func TestDefaultDesktopAppearanceAutoGraphite(t *testing.T) {
	cfg := Default()
	if got := cfg.DesktopTheme(); got != "auto" {
		t.Fatalf("default desktop theme = %q, want auto", got)
	}
	if got := cfg.DesktopThemeStyle(); got != "" {
		t.Fatalf("default desktop theme style = %q, want empty so frontend resolves graphite", got)
	}
}

func TestDefaultDesktopMetricsOn(t *testing.T) {
	cfg := Default()
	if !cfg.DesktopMetrics() {
		t.Fatal("default desktop metrics = false, want true")
	}
	disabled := false
	cfg.Desktop.Metrics = &disabled
	if cfg.DesktopMetrics() {
		t.Fatal("desktop metrics explicit false = true, want false")
	}
}
