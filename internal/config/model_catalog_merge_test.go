package config

import (
	"reflect"
	"testing"
)

func TestNormalizeOfficialNewModelsAddsDeepSeekVisionExp(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Models:    []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:   "deepseek-v4-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}}}
	normalizeOfficialNewModels(c)
	p, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if !p.HasModel(deepSeekVisionExpModel) {
		t.Fatalf("models = %v, want %q merged in", p.ModelList(), deepSeekVisionExpModel)
	}
	if p.Default != "deepseek-v4-flash" {
		t.Fatalf("default = %q, want preserved deepseek-v4-flash", p.Default)
	}
	if !reflect.DeepEqual(p.VisionModels, []string{deepSeekVisionExpModel}) {
		t.Fatalf("vision_models = %v, want only %q", p.VisionModels, deepSeekVisionExpModel)
	}
}

func TestNormalizeOfficialNewModelsAddsOpenAIGPT56(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "openai",
		Kind:      "openai",
		BaseURL:   "https://api.openai.com/v1",
		Models:    []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "o3-mini"},
		Default:   "gpt-4o",
		APIKeyEnv: "OPENAI_API_KEY",
	}}}
	normalizeOfficialNewModels(c)
	p, ok := c.Provider("openai")
	if !ok {
		t.Fatal("openai provider missing")
	}
	for _, model := range openAIGPT56Models() {
		if !p.HasModel(model) {
			t.Fatalf("models = %v, want %q merged in", p.ModelList(), model)
		}
	}
	if p.Default != "gpt-4o" {
		t.Fatalf("default = %q, want preserved gpt-4o", p.Default)
	}
	// Existing old models are preserved for compatibility.
	for _, model := range []string{"gpt-4o", "gpt-4o-mini", "gpt-4.1", "o4-mini", "o3-mini"} {
		if !p.HasModel(model) {
			t.Fatalf("models = %v, want existing %q preserved", p.ModelList(), model)
		}
	}
}

func TestNormalizeOfficialNewModelsIsIdempotent(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "openai",
		Kind:      "openai",
		BaseURL:   "https://api.openai.com/v1",
		Models:    []string{"gpt-4o", "gpt-5.6"},
		Default:   "gpt-4o",
		APIKeyEnv: "OPENAI_API_KEY",
	}}}
	normalizeOfficialNewModels(c)
	first := append([]string(nil), c.Providers[0].ModelList()...)
	normalizeOfficialNewModels(c)
	second := c.Providers[0].ModelList()
	if !stringSlicesEqual(first, second) {
		t.Fatalf("repeated merge produced duplicates: %v -> %v", first, second)
	}
	if len(second) != len(first) {
		t.Fatalf("repeated merge changed model count: %v -> %v", first, second)
	}
}

func TestNormalizeOfficialNewModelsSkipsCustomGateway(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{
		{Name: "proxy", Kind: "openai", BaseURL: "https://proxy.example.com/v1", Models: []string{"gpt-4o"}, Default: "gpt-4o"},
		{Name: "deepseek-proxy", Kind: "openai", BaseURL: "https://proxy.example.com/v1", Models: []string{"deepseek-v4-flash"}, Default: "deepseek-v4-flash"},
	}}
	normalizeOfficialNewModels(c)
	for _, name := range []string{"proxy", "deepseek-proxy"} {
		p, ok := c.Provider(name)
		if !ok {
			t.Fatalf("%s provider missing", name)
		}
		if p.HasModel("gpt-5.6") || p.HasModel(deepSeekVisionExpModel) {
			t.Fatalf("%s models = %v, want no official catalog injection", name, p.ModelList())
		}
	}
}

func TestNormalizeOfficialNewModelsSkipsLegacyDeepSeekEntries(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{
		{Name: "deepseek-flash", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY"},
		{Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY"},
	}}
	normalizeOfficialNewModels(c)
	for _, name := range []string{"deepseek-flash", "deepseek-pro"} {
		p, ok := c.Provider(name)
		if !ok {
			t.Fatalf("%s provider missing", name)
		}
		if p.HasModel(deepSeekVisionExpModel) {
			t.Fatalf("%s models = %v, want legacy single-model entry untouched", name, p.ModelList())
		}
	}
}

func TestNormalizeOfficialNewModelsPreservesExplicitVisionSelection(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Models:       []string{"deepseek-v4-flash", "deepseek-v4-pro"},
		Default:      "deepseek-v4-flash",
		APIKeyEnv:    "DEEPSEEK_API_KEY",
		VisionModels: []string{}, // explicit empty list: user disabled vision
	}}}
	normalizeOfficialNewModels(c)
	p, _ := c.Provider("deepseek")
	if p.VisionModels == nil || len(p.VisionModels) != 0 {
		t.Fatalf("vision_models = %#v, want explicit empty list preserved", p.VisionModels)
	}
}

func TestMergeOfficialNewModelsPreservesCustomModelAndDefault(t *testing.T) {
	p := &ProviderEntry{
		Name:    "openai",
		Kind:    "openai",
		BaseURL: "https://api.openai.com/v1",
		Models:  []string{"my-custom", "gpt-4o"},
		Default: "my-custom",
	}
	mergeOfficialNewModels(p, openAIGPT56Models(), openAIGPT56Models())
	if p.Default != "my-custom" {
		t.Fatalf("default = %q, want preserved my-custom", p.Default)
	}
	if !p.HasModel("my-custom") {
		t.Fatalf("models = %v, want custom model preserved", p.ModelList())
	}
	if !p.HasModel("gpt-5.6-sol") {
		t.Fatalf("models = %v, want gpt-5.6-sol merged in", p.ModelList())
	}
}

func TestNormalizeConfigForEditAddsOfficialModels(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name: "openai", Kind: "openai", BaseURL: "https://api.openai.com/v1",
		Models: []string{"gpt-4o"}, Default: "gpt-4o",
	}}}
	normalizeConfigForEdit(c)
	if !c.Providers[0].HasModel("gpt-5.6-sol") {
		t.Fatalf("editable settings models = %v, want GPT-5.6 catalog merged", c.Providers[0].ModelList())
	}
}

func TestNormalizeOfficialNewModelsAddsModelsToExistingCodexCLI(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name: "local-codex", Kind: "cli", Command: `C:\\Tools\\codex.exe`,
		Models: []string{"gpt-5.5"}, Default: "gpt-5.5",
	}}}
	normalizeOfficialNewModels(c)
	p := &c.Providers[0]
	if !p.HasModel("gpt-5.6-terra") {
		t.Fatalf("existing Codex CLI models = %v, want GPT-5.6 catalog merged", p.ModelList())
	}
	if p.Default != "gpt-5.5" {
		t.Fatalf("existing Codex CLI default = %q, want gpt-5.5 preserved", p.Default)
	}
	normalizeOfficialNewModels(c)
	if got, want := len(p.ModelList()), 5; got != want {
		t.Fatalf("repeated Codex CLI merge model count = %d, want %d: %v", got, want, p.ModelList())
	}
}

func TestNormalizeOfficialNewModelsSkipsNonCodexCLI(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name: "local-claude", Kind: "cli", Command: "claude", Models: []string{"default"},
	}}}
	normalizeOfficialNewModels(c)
	if c.Providers[0].HasModel("gpt-5.6-sol") {
		t.Fatalf("non-Codex CLI models = %v, want unchanged", c.Providers[0].ModelList())
	}
}
