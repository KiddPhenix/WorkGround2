package config

import (
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestAssistEnabledDefaults(t *testing.T) {
	c := Default()
	if !c.AssistEnabled() {
		t.Fatal("default config should have assist enabled")
	}
}

func TestAssistEnabledOff(t *testing.T) {
	c := Default()
	c.Agent.AssistMode = "off"
	if c.AssistEnabled() {
		t.Fatal("assist_mode=off should disable assist")
	}
}

func TestAssistEnabledCaseInsensitive(t *testing.T) {
	c := Default()
	c.Agent.AssistMode = "OFF"
	if c.AssistEnabled() {
		t.Fatal("assist_mode=OFF should disable assist")
	}
}

func TestAssistMaxAttemptsDefault(t *testing.T) {
	c := Default()
	if n := c.AssistMaxAttempts(); n != 3 {
		t.Fatalf("default AssistMaxAttempts: want 3, got %d", n)
	}
}

func TestAssistMaxAttemptsCustom(t *testing.T) {
	c := Default()
	c.Agent.AssistMaxAttempts = 5
	if n := c.AssistMaxAttempts(); n != 5 {
		t.Fatalf("custom AssistMaxAttempts: want 5, got %d", n)
	}
}

func TestAssistMaxAttemptsZero(t *testing.T) {
	c := Default()
	c.Agent.AssistMaxAttempts = 0
	if n := c.AssistMaxAttempts(); n != 3 {
		t.Fatalf("zero AssistMaxAttempts should default to 3, got %d", n)
	}
}

func TestAssistMaxAttemptsCapped(t *testing.T) {
	c := Default()
	c.Agent.AssistMaxAttempts = 999
	if n := c.AssistMaxAttempts(); n != 10 {
		t.Fatalf("AssistMaxAttempts should be capped at 10, got %d", n)
	}
}

func TestAssistCandidatesOffMode(t *testing.T) {
	c := Default()
	c.Agent.AssistMode = "off"
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("off mode should return no candidates, got %v", cands)
	}
}

func TestAssistCandidatesNilConfig(t *testing.T) {
	var c *Config
	cands := c.AssistCandidates("any/model", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("nil config should return no candidates, got %v", cands)
	}
}

func TestAssistCandidatesNoRoute(t *testing.T) {
	c := Default()
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("no web_search providers should return empty, got %v", cands)
	}
}

func TestAssistCandidatesSkipsUnconfiguredProvider(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name: "missing-key", Kind: "openai", BaseURL: "https://api.openai.com/v1", Model: "search-model",
		APIKeyEnv: "MISSING_ASSIST_KEY", Capabilities: []string{"web_search"},
	})
	t.Setenv("MISSING_ASSIST_KEY", "")
	if got := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch); len(got) != 0 {
		t.Fatalf("unconfigured provider should be excluded: %v", got)
	}
}

func TestAssistCandidatesSkipsCLIWithoutCommand(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name: "missing-cli", Kind: "cli", Model: "default", Capabilities: []string{"web_search"},
	})
	if got := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch); len(got) != 0 {
		t.Fatalf("CLI without command should be excluded: %v", got)
	}
}

func TestAssistCandidatesExplicitRouteRequiresCapability(t *testing.T) {
	// Explicit routes must have HasCapability for the requested cap.
	// DeepSeek does NOT have web_search, so this explicit route yields nothing.
	c := Default()
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"deepseek-pro/deepseek-v4-pro"},
	}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("explicit route without capability should yield empty, got %v", cands)
	}
}

func TestAssistCandidatesExplicitRouteWithCapability(t *testing.T) {
	c := Default()
	// Add a provider that actually has web_search.
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"search-pro/search-model"},
	}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 {
		t.Fatalf("explicit route with capability should yield 1 candidate, got %d", len(cands))
	}
	if cands[0] != "search-pro/search-model" {
		t.Fatalf("unexpected candidate: %q", cands[0])
	}
}

func TestAssistCandidatesExcludesCurrent(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"search-pro/search-model", "deepseek-pro/deepseek-v4-pro"},
	}
	cands := c.AssistCandidates("search-pro/search-model", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("should exclude current model, got %d candidates: %v", len(cands), cands)
	}
}

func TestAssistCandidatesDedup(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"search-pro/search-model", "search-pro/search-model"},
	}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 {
		t.Fatalf("dedup should yield 1 candidate, got %d: %v", len(cands), cands)
	}
}

func TestAssistCandidatesUnresolvableRef(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"nonexistent/model", "search-pro/search-model"},
	}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 {
		t.Fatalf("should skip unresolvable ref, got %d", len(cands))
	}
	if cands[0] != "search-pro/search-model" {
		t.Fatalf("unexpected candidate: %q", cands[0])
	}
}

func TestAssistCandidatesExplicitOverridesAuto(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	// Even with an auto-discoverable provider, explicit routes suppress it.
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"search-pro/search-model"},
	}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 {
		t.Fatalf("explicit route should suppress auto-discovery, got %d", len(cands))
	}
}

func TestAssistCandidatesAutoDiscovery(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 {
		t.Fatalf("auto-discovery should find 1 candidate, got %d", len(cands))
	}
	if cands[0] != "search-pro/search-model" {
		t.Fatalf("unexpected candidate: %q", cands[0])
	}
}

func TestAssistCandidatesEmptyRouteUsesAutoDiscovery(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	c.Agent.AssistModels = map[string][]string{"web_search": {}}
	cands := c.AssistCandidates("deepseek-flash/deepseek-v4-flash", CapWebSearch)
	if len(cands) != 1 || cands[0] != "search-pro/search-model" {
		t.Fatalf("empty explicit route should use auto-discovery, got %v", cands)
	}
}

func TestAssistCandidatesAutoDiscoveryExcludesCurrent(t *testing.T) {
	c := Default()
	c.Providers = append(c.Providers, ProviderEntry{
		Name:         "search-pro",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "search-model",
		Capabilities: []string{"web_search"},
	})
	cands := c.AssistCandidates("search-pro/search-model", CapWebSearch)
	if len(cands) != 0 {
		t.Fatalf("auto-discovery should exclude current model, got %d", len(cands))
	}
}

func TestEntryCapabilitiesIncludesNewCaps(t *testing.T) {
	e := &ProviderEntry{
		Capabilities: []string{"web_search", "image_generation"},
	}
	caps := EntryCapabilities(e)
	hasWS := false
	hasIG := false
	for _, c := range caps {
		if c == string(CapWebSearch) {
			hasWS = true
		}
		if c == string(CapImageGeneration) {
			hasIG = true
		}
	}
	if !hasWS {
		t.Fatal("EntryCapabilities should include web_search")
	}
	if !hasIG {
		t.Fatal("EntryCapabilities should include image_generation")
	}
}

func TestHasCapabilityWebSearchExplicit(t *testing.T) {
	e := &ProviderEntry{
		Capabilities: []string{"web_search"},
	}
	if !e.HasCapability(CapWebSearch) {
		t.Fatal("explicit web_search capability should be detected")
	}
	if e.HasCapability(CapImageGeneration) {
		t.Fatal("should not have image_generation when not declared")
	}
}

func TestHasCapabilityWebSearchNotInferred(t *testing.T) {
	e := &ProviderEntry{
		Kind:  "openai",
		Model: "gpt-5",
	}
	if e.HasCapability(CapWebSearch) {
		t.Fatal("web_search should never be inferred from model brand")
	}
	if e.HasCapability(CapImageGeneration) {
		t.Fatal("image_generation should never be inferred from model brand")
	}
}

func TestRenderTOMLAssistFields(t *testing.T) {
	c := Default()
	c.Agent.AssistMode = "off"
	c.Agent.AssistModels = map[string][]string{
		"web_search": {"p1/m1", "p2/m2"},
	}
	c.Agent.AssistMaxAttempts = 5
	out := RenderTOML(c)
	if !strings.Contains(out, `assist_mode = "off"`) {
		t.Fatal("rendered TOML should contain assist_mode")
	}
	if !strings.Contains(out, `assist_models =`) {
		t.Fatal("rendered TOML should contain assist_models")
	}
	if !strings.Contains(out, `assist_max_attempts = 5`) {
		t.Fatal("rendered TOML should contain assist_max_attempts")
	}
	var got Config
	if _, err := toml.Decode(out, &got); err != nil {
		t.Fatalf("round-trip load: %v", err)
	}
	if got.Agent.AssistMode != "off" {
		t.Fatalf("round-trip assist_mode: want off, got %q", got.Agent.AssistMode)
	}
	if len(got.Agent.AssistModels) != 1 {
		t.Fatalf("round-trip assist_models: want 1 entry, got %d", len(got.Agent.AssistModels))
	}
	if got.Agent.AssistMaxAttempts != 5 {
		t.Fatalf("round-trip assist_max_attempts: want 5, got %d", got.Agent.AssistMaxAttempts)
	}
}

func TestRenderTOMLAssistFieldsDefaultCommented(t *testing.T) {
	c := Default()
	out := RenderTOML(c)
	if strings.Contains(out, "\nassist_mode = ") {
		t.Fatal("default config should have commented assist_mode, not active")
	}
	if !strings.Contains(out, "\n# assist_mode = ") {
		t.Fatal("default config should have commented assist_mode hint")
	}
	if strings.Contains(out, "\nassist_max_attempts = ") {
		t.Fatal("default config should have commented assist_max_attempts, not active")
	}
	if !strings.Contains(out, "\n# assist_max_attempts = ") {
		t.Fatal("default config should have commented assist_max_attempts hint")
	}
	if !strings.Contains(out, "\n# assist_models = ") {
		t.Fatal("default config should have commented assist_models hint")
	}
}

func TestRenderTOMLPersistsProviderCapabilities(t *testing.T) {
	c := Default()
	c.Providers[0].Capabilities = []string{"vision", "web_search"}
	out := RenderTOML(c)
	if !strings.Contains(out, `capabilities = ["vision", "web_search"]`) {
		t.Fatalf("rendered TOML missing capabilities:\n%s", out)
	}
	var got Config
	if _, err := toml.Decode(out, &got); err != nil {
		t.Fatalf("round-trip load: %v", err)
	}
	if len(got.Providers) == 0 || !got.Providers[0].HasCapability(CapWebSearch) {
		t.Fatalf("round-trip provider capabilities lost: %+v", got.Providers)
	}
}

func TestRenderTOMLPersistsExplicitEmptyCapabilities(t *testing.T) {
	c := Default()
	c.Providers[0].Capabilities = []string{}
	out := RenderTOML(c)
	if !strings.Contains(out, `capabilities = []`) {
		t.Fatalf("rendered TOML should preserve explicit empty capabilities:\n%s", out)
	}
}

func TestRemoveProviderDropsAssistRoute(t *testing.T) {
	c := testModelFallbackConfig(t)
	c.Providers[0].Capabilities = []string{"web_search"}
	c.Agent.AssistModels = map[string][]string{"web_search": {"prov-a/model-a1", "prov-b/model-b1"}}
	if err := c.RemoveProvider("prov-a"); err != nil {
		t.Fatalf("RemoveProvider: %v", err)
	}
	refs := c.Agent.AssistModels["web_search"]
	if len(refs) != 1 || refs[0] != "prov-b/model-b1" {
		t.Fatalf("assist refs = %v", refs)
	}
}
