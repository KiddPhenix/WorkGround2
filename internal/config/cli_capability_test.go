package config

import (
	"context"
	"reflect"
	"testing"
)

func TestParseCodexCapabilities(t *testing.T) {
	output := `browser_use stable true
image_generation stable true
standalone_web_search under development false`
	if got, want := parseCodexCapabilities(output), []string{"web_search"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parseCodexCapabilities() = %v, want %v", got, want)
	}
}

func TestParseCodexCapabilitiesDisabled(t *testing.T) {
	output := `browser_use stable false
standalone_web_search under development false`
	if got := parseCodexCapabilities(output); len(got) != 0 {
		t.Fatalf("parseCodexCapabilities() = %v, want none", got)
	}
}

func TestProbeCLICapabilitiesHonorsExplicitConfig(t *testing.T) {
	entry := &ProviderEntry{Kind: "cli", Command: "codex", Capabilities: []string{"vision"}}
	got, err := ProbeCLICapabilities(context.Background(), entry)
	if err != nil {
		t.Fatalf("ProbeCLICapabilities: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("explicit capabilities should disable probing: %v", got)
	}
}

func TestAddCapabilitiesPreservesBaseline(t *testing.T) {
	entry := &ProviderEntry{Kind: "cli", Model: "gpt-5.5"}
	entry.AddCapabilities("web_search")
	for _, capability := range []ModelCapability{CapVision, CapReasoning, CapWebSearch} {
		if !entry.HasCapability(capability) {
			t.Fatalf("merged entry should have %q: %v", capability, entry.Capabilities)
		}
	}
}
