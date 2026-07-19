package config

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestParseCodexCapabilities(t *testing.T) {
	output := `browser_use stable true
image_generation stable true
standalone_web_search under development false`
	got := parseCodexCapabilities(output)
	want := []string{"web_search", "image_generation"}
	if !reflect.DeepEqual(got, want) {
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

func TestParseCodexCapabilitiesImageOnly(t *testing.T) {
	output := `browser_use stable false
image_generation stable true
standalone_web_search under development false`
	got := parseCodexCapabilities(output)
	if len(got) != 1 || got[0] != "image_generation" {
		t.Fatalf("parseCodexCapabilities() = %v, want [image_generation]", got)
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

func TestCLICapabilityCacheIncludesFailuresAndExpires(t *testing.T) {
	command := "codex-cache-test"
	cliCapabilityCache.Delete(command)
	t.Cleanup(func() { cliCapabilityCache.Delete(command) })
	now := time.Now()
	wantErr := errors.New("probe failed")

	storeCLICapabilityCache(command, nil, wantErr, now)
	capabilities, err, ok := loadCLICapabilityCache(command, now.Add(time.Minute))
	if !ok || err == nil || err.Error() != wantErr.Error() || len(capabilities) != 0 {
		t.Fatalf("cached failure = (%v, %v, %v), want visible failure", capabilities, err, ok)
	}
	if _, _, ok := loadCLICapabilityCache(command, now.Add(cliCapabilityCacheTTL)); ok {
		t.Fatal("expired capability probe failure remained cached")
	}
}

func TestCLICapabilityCacheCopiesCapabilities(t *testing.T) {
	command := "codex-cache-copy-test"
	cliCapabilityCache.Delete(command)
	t.Cleanup(func() { cliCapabilityCache.Delete(command) })
	now := time.Now()
	want := []string{"web_search"}

	storeCLICapabilityCache(command, want, nil, now)
	want[0] = "mutated"
	got, err, ok := loadCLICapabilityCache(command, now)
	if !ok || err != nil || !reflect.DeepEqual(got, []string{"web_search"}) {
		t.Fatalf("cached capabilities = (%v, %v, %v)", got, err, ok)
	}
	got[0] = "mutated-again"
	again, _, _ := loadCLICapabilityCache(command, now)
	if !reflect.DeepEqual(again, []string{"web_search"}) {
		t.Fatalf("cached capabilities exposed mutable state: %v", again)
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
