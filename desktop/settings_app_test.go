package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/hook"
	"workground2/internal/provider"

	"workground2/desktop/internal/memhttp"
)

type captureTurnRunner struct {
	inputs []string
}

func (r *captureTurnRunner) Run(_ context.Context, input string) error {
	r.inputs = append(r.inputs, input)
	return nil
}

func TestWithFreshSystemPromptReplacesExistingSystemMessage(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: "old", ReasoningContent: "stale", ReasoningSignature: "sig", ToolCalls: []provider.ToolCall{{ID: "call", Name: "noop"}}, ToolCallID: "tool", Name: "name"},
		{Role: provider.RoleUser, Content: "hello"},
	}

	got := withFreshSystemPrompt(msgs, "new")
	if got[0].Content != "new" {
		t.Fatalf("system prompt = %q, want new", got[0].Content)
	}
	if got[0].ReasoningContent != "" || got[0].ReasoningSignature != "" || len(got[0].ToolCalls) != 0 || got[0].ToolCallID != "" || got[0].Name != "" {
		t.Fatalf("system metadata should be cleared, got %+v", got[0])
	}
	if got[1].Content != "hello" {
		t.Fatalf("non-system message changed: %+v", got[1])
	}
	if msgs[0].Content != "old" {
		t.Fatalf("input slice was mutated: %+v", msgs[0])
	}
}

func TestWithFreshSystemPromptPrependsMissingSystemMessage(t *testing.T) {
	msgs := []provider.Message{{Role: provider.RoleUser, Content: "hello"}}

	got := withFreshSystemPrompt(msgs, "new")
	if len(got) != 2 || got[0].Role != provider.RoleSystem || got[0].Content != "new" {
		t.Fatalf("expected prepended system prompt, got %+v", got)
	}
	if got[1].Content != "hello" {
		t.Fatalf("existing user message changed: %+v", got[1])
	}
}

func TestProviderViewFromEntry_FiltersNonChatModels(t *testing.T) {
	p := config.ProviderEntry{
		Name: "mimo-api",
		Models: []string{
			"mimo-v2", "mimo-v2-pro",
			"mimo-v2-asr", "mimo-v2-tts",
			"mimo-v2-tts-voiceclone", "mimo-v2-tts-voicedesign",
		},
		VisionModels: []string{"mimo-v2", "mimo-v2-asr", "mimo-v2-omni"},
	}
	view := providerViewFromEntry(p, true, false)
	want := []string{"mimo-v2", "mimo-v2-pro"}
	if !reflect.DeepEqual(view.Models, want) {
		t.Errorf("ProviderView.Models = %v, want %v", view.Models, want)
	}
	if got, want := view.VisionModels, []string{"mimo-v2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ProviderView.VisionModels = %v, want %v", got, want)
	}
	if !view.VisionModelsSet {
		t.Fatal("ProviderView.VisionModelsSet = false, want true for configured vision_models")
	}
}

func TestProviderViewFromEntry_MigratesProviderWideVision(t *testing.T) {
	p := config.ProviderEntry{
		Name:   "custom",
		Models: []string{"text-only", "qwen-vl-plus"},
		Vision: true,
	}
	view := providerViewFromEntry(p, false, true)
	if got, want := view.VisionModels, []string{"text-only", "qwen-vl-plus"}; !reflect.DeepEqual(got, want) {
		t.Errorf("ProviderView.VisionModels = %v, want %v", got, want)
	}
	if !view.VisionModelsSet {
		t.Fatal("ProviderView.VisionModelsSet = false, want true for provider-wide vision")
	}
}

func TestProviderViewFromEntryShowsKeySource(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("TEST_PROVIDER_KEY_SOURCE", "")
	os.Unsetenv("TEST_PROVIDER_KEY_SOURCE")
	if _, err := config.SetCredential("TEST_PROVIDER_KEY_SOURCE", "sk-test"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	view := providerViewFromEntry(config.ProviderEntry{
		Name:      "custom",
		APIKeyEnv: "TEST_PROVIDER_KEY_SOURCE",
	}, false, true)
	if !view.KeySet {
		t.Fatal("KeySet = false, want true")
	}
	if !view.Configured {
		t.Fatal("Configured = false, want true from resolved credentials")
	}
	if view.KeySource == "" || !strings.Contains(view.KeySource, "credentials") {
		t.Fatalf("KeySource = %q, want credentials source", view.KeySource)
	}
}

func TestProviderViewFromEntryExposesNoAuthAvailability(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("LOCAL_API_KEY", "")
	os.Unsetenv("LOCAL_API_KEY")

	noAuth := providerViewFromEntry(config.ProviderEntry{
		Name:    "local",
		Kind:    "openai",
		BaseURL: "http://127.0.0.1:23333/v1",
		Models:  []string{"model-a"},
	}, false, true)
	if noAuth.RequiresKey {
		t.Fatal("no-auth provider RequiresKey = true, want false")
	}
	if !noAuth.Configured {
		t.Fatal("no-auth provider Configured = false, want true")
	}
	if noAuth.KeySet {
		t.Fatal("no-auth provider KeySet = true, want false")
	}

	legacyLoopback := providerViewFromEntry(config.ProviderEntry{
		Name:      "local",
		Kind:      "openai",
		BaseURL:   "http://127.0.0.1:23333/v1",
		Models:    []string{"model-a"},
		APIKeyEnv: "LOCAL_API_KEY",
	}, false, true)
	if legacyLoopback.RequiresKey {
		t.Fatal("loopback provider with missing legacy key env RequiresKey = true, want false")
	}
	if !legacyLoopback.Configured {
		t.Fatal("loopback provider with missing legacy key env Configured = false, want true")
	}

	official := providerViewFromEntry(config.ProviderEntry{
		Name:    "deepseek",
		Kind:    "openai",
		BaseURL: "https://api.deepseek.com",
		Models:  []string{"deepseek-v4-flash"},
	}, true, true)
	if !official.RequiresKey {
		t.Fatal("official provider RequiresKey = false, want true")
	}
	if official.Configured {
		t.Fatal("official provider without key Configured = true, want false")
	}
}

func TestProviderViewFromEntryUpgradesLegacyCodexCLI(t *testing.T) {
	view := providerViewFromEntry(config.ProviderEntry{
		Name:     "local-codex",
		Kind:     "cli",
		Command:  `C:\Users\admin\AppData\Local\OpenAI\Codex\bin\codex.exe`,
		Args:     []string{"exec", "--ignore-user-config", "--skip-git-repo-check"},
		Protocol: "text",
		Models:   []string{"default"},
	}, false, true)
	if view.Protocol != "jsonl" {
		t.Fatalf("Protocol = %q, want jsonl", view.Protocol)
	}
	if len(view.Args) < 2 || view.Args[0] != "exec" || view.Args[1] != "--json" {
		t.Fatalf("Args = %+v, want --json after exec", view.Args)
	}
}

func TestSetProviderKeyDoesNotWarnWhenProjectEnvAlsoDefinesSavedKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("TEST_PROVIDER_SHADOW=old-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PROVIDER_SHADOW", "")
	os.Unsetenv("TEST_PROVIDER_SHADOW")

	app := &App{
		tabs:        map[string]*WorkspaceTab{"project": {ID: "project", WorkspaceRoot: project}},
		activeTabID: "project",
	}
	warning, err := app.SetProviderKey("TEST_PROVIDER_SHADOW", "new-key")
	if err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("SetProviderKey warning = %q, want no warning because provider keys use global credentials only", warning)
	}
	data, readErr := os.ReadFile(config.UserCredentialsPath())
	if readErr != nil {
		t.Fatalf("read credentials: %v", readErr)
	}
	if !strings.Contains(string(data), "TEST_PROVIDER_SHADOW=new-key") {
		t.Fatalf("saved credentials missing new key:\n%s", data)
	}
}

func TestSetProviderKeyDoesNotWarnWhenEnvironmentAlsoDefinesSavedKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("TEST_PROVIDER_EMPTY_ENV", "")

	app := &App{}
	warning, err := app.SetProviderKey("TEST_PROVIDER_EMPTY_ENV", "new-key")
	if err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("SetProviderKey warning = %q, want no warning because provider keys use global credentials only", warning)
	}
	data, readErr := os.ReadFile(config.UserCredentialsPath())
	if readErr != nil {
		t.Fatalf("read credentials: %v", readErr)
	}
	if !strings.Contains(string(data), "TEST_PROVIDER_EMPTY_ENV=new-key") {
		t.Fatalf("saved credentials missing new key:\n%s", data)
	}
}

func TestSetProviderKeyDoesNotWarnWhenEmptyProjectEnvAlsoDefinesSavedKey(t *testing.T) {
	isolateDesktopUserDirs(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, ".env"), []byte("TEST_PROVIDER_EMPTY_PROJECT=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_PROVIDER_EMPTY_PROJECT", "")
	os.Unsetenv("TEST_PROVIDER_EMPTY_PROJECT")

	app := &App{
		tabs:        map[string]*WorkspaceTab{"project": {ID: "project", WorkspaceRoot: project}},
		activeTabID: "project",
	}
	warning, err := app.SetProviderKey("TEST_PROVIDER_EMPTY_PROJECT", "new-key")
	if err != nil {
		t.Fatalf("SetProviderKey: %v", err)
	}
	if warning != "" {
		t.Fatalf("SetProviderKey warning = %q, want no warning because provider keys use global credentials only", warning)
	}
}

func TestFetchProviderModelsFiltersNonChatModels(t *testing.T) {
	isolateDesktopUserDirs(t)
	if _, err := config.SetCredential("TEST_PROVIDER_KEY", "test-key"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	srv := memhttp.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": "mimo-v2.5-pro", "object": "model"},
				{"id": "mimo-v2.5-asr", "object": "model"},
				{"id": "mimo-v2.5-tts", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	got, err := NewApp().FetchProviderModels(ProviderView{
		Name:      "mimo-api",
		BaseURL:   srv.URL,
		APIKeyEnv: "TEST_PROVIDER_KEY",
	})
	if err != nil {
		t.Fatalf("FetchProviderModels: %v", err)
	}
	want := []string{"mimo-v2.5-pro"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FetchProviderModels = %v, want %v", got, want)
	}
}

func TestFetchProviderModelsUsesSavedCredentialBeforeEnvironment(t *testing.T) {
	isolateDesktopUserDirs(t)
	const keyEnv = "TEST_PROVIDER_FETCH_KEY"
	if _, err := config.SetCredential(keyEnv, "saved-key"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	t.Setenv(keyEnv, "stale-env-key")

	srv := memhttp.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer saved-key" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]string{
				{"id": "model-a", "object": "model"},
			},
		})
	}))
	defer srv.Close()

	got, err := NewApp().FetchProviderModels(ProviderView{
		Name:      "custom",
		BaseURL:   srv.URL,
		APIKeyEnv: keyEnv,
	})
	if err != nil {
		t.Fatalf("FetchProviderModels: %v", err)
	}
	if want := []string{"model-a"}; !reflect.DeepEqual(got, want) {
		t.Errorf("FetchProviderModels = %v, want %v", got, want)
	}
	if got := os.Getenv(keyEnv); got != "stale-env-key" {
		t.Fatalf("process env = %q, want stale env left untouched", got)
	}
}

func TestSaveProviderFiltersNonChatModels(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SaveProvider(ProviderView{
		Name:    "mimo-api",
		Kind:    "openai",
		BaseURL: "https://api.xiaomimimo.com/v1",
		Models:  []string{"mimo-v2.5-asr", "mimo-v2.5-pro", "mimo-v2.5-tts"},
		VisionModels: []string{
			"mimo-v2.5-asr",
			"mimo-v2.5-pro",
			"mimo-v2.5-tts",
		},
		VisionModelsSet: true,
		Default:         "mimo-v2.5-asr",
		APIKeyEnv:       "MIMO_API_KEY",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("mimo-api")
	if !ok {
		t.Fatal("saved provider not found")
	}
	want := []string{"mimo-v2.5-pro"}
	if !reflect.DeepEqual(got.ModelList(), want) {
		t.Errorf("saved provider models = %v, want %v", got.ModelList(), want)
	}
	if got.DefaultModel() != "mimo-v2.5-pro" {
		t.Errorf("saved provider default = %q, want mimo-v2.5-pro", got.DefaultModel())
	}
	if got, want := got.VisionModels, []string{"mimo-v2.5-pro"}; !reflect.DeepEqual(got, want) {
		t.Errorf("saved provider vision_models = %v, want %v", got, want)
	}
	raw, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	saved := string(raw)
	blockStart := strings.Index(saved, "\n[[providers]]\nname        = \"mimo-api\"")
	if blockStart < 0 {
		t.Fatalf("saved config missing mimo-api provider block:\n%s", raw)
	}
	block := saved[blockStart:]
	if next := strings.Index(block[len("\n[[providers]]"):], "\n[[providers]]"); next >= 0 {
		block = block[:len("\n[[providers]]")+next]
	}
	if !strings.Contains(block, `models      = ["mimo-v2.5-pro"]`) {
		t.Fatalf("saved provider block did not persist single selection as models array:\n%s", block)
	}
	if strings.Contains(block, `model       = "mimo-v2.5-pro"`) {
		t.Fatalf("saved provider block should not persist explicit single selection as legacy model:\n%s", block)
	}
	if !strings.Contains(block, `vision_models = ["mimo-v2.5-pro"]`) {
		t.Fatalf("saved provider block did not persist filtered vision_models:\n%s", block)
	}
}

func TestSaveProviderUpgradesLegacyCodexCLI(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SaveProvider(ProviderView{
		Name:     "local-codex",
		Kind:     "cli",
		Command:  `C:\Users\admin\AppData\Local\OpenAI\Codex\bin\codex.exe`,
		Args:     []string{"exec", "--ignore-user-config", "--skip-git-repo-check"},
		Protocol: "text",
		Models:   []string{"default"},
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("local-codex")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.Protocol != "jsonl" {
		t.Fatalf("Protocol = %q, want jsonl", got.Protocol)
	}
	if len(got.Args) < 2 || got.Args[0] != "exec" || got.Args[1] != "--json" {
		t.Fatalf("Args = %+v, want --json after exec", got.Args)
	}
}

func TestSaveProviderPersistsAssistCapabilities(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := NewApp().SaveProvider(ProviderView{
		Name: "helper", Kind: "openai", BaseURL: "https://example.com/v1",
		Models: []string{"gpt-5.5"}, Capabilities: []string{"web_search", "image_generation"},
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}
	got, ok := config.LoadForEdit(config.UserConfigPath()).Provider("helper")
	if !ok {
		t.Fatal("saved provider not found")
	}
	for _, capability := range []config.ModelCapability{config.CapVision, config.CapReasoning, config.CapWebSearch, config.CapImageGeneration} {
		if !got.HasCapability(capability) {
			t.Fatalf("provider should have %q: %v", capability, got.Capabilities)
		}
	}
	if err := NewApp().SaveProvider(ProviderView{
		Name: "helper", Kind: "openai", BaseURL: "https://example.com/v1",
		Models: []string{"gpt-5.5"}, Capabilities: []string{},
	}); err != nil {
		t.Fatalf("clear assist capabilities: %v", err)
	}
	got, _ = config.LoadForEdit(config.UserConfigPath()).Provider("helper")
	if got.HasCapability(config.CapWebSearch) || got.HasCapability(config.CapImageGeneration) {
		t.Fatalf("action capabilities should be cleared: %v", got.Capabilities)
	}
	if !got.HasCapability(config.CapVision) || !got.HasCapability(config.CapReasoning) {
		t.Fatalf("built-in baseline should be preserved: %v", got.Capabilities)
	}
}

func TestSaveProviderPersistsCustomEndpointURLs(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SaveProvider(ProviderView{
		Name:      "sub2api",
		Kind:      "openai",
		BaseURL:   "https://proxy.example.com/v1",
		ChatURL:   " https://proxy.example.com/custom/chat/completions ",
		ModelsURL: " https://proxy.example.com/v1/models ",
		Models:    []string{"model-a"},
		Default:   "model-a",
		APIKeyEnv: "SUB2API_KEY",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("sub2api")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.ChatURL != "https://proxy.example.com/custom/chat/completions" {
		t.Fatalf("saved chat_url = %q", got.ChatURL)
	}
	if got.ModelsURL != "https://proxy.example.com/v1/models" {
		t.Fatalf("saved models_url = %q", got.ModelsURL)
	}

	view := app.Settings()
	for _, provider := range view.Providers {
		if provider.Name != "sub2api" {
			continue
		}
		if provider.ChatURL != "https://proxy.example.com/custom/chat/completions" {
			t.Fatalf("Settings chatUrl = %q", provider.ChatURL)
		}
		if provider.ModelsURL != "https://proxy.example.com/v1/models" {
			t.Fatalf("Settings modelsUrl = %q", provider.ModelsURL)
		}
		return
	}
	t.Fatalf("Settings providers missing sub2api: %+v", view.Providers)
}

func TestSaveProviderPreservesHiddenProviderFields(t *testing.T) {
	isolateDesktopUserDirs(t)

	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.Providers = []config.ProviderEntry{{
		Name:         "custom",
		Kind:         "openai",
		BaseURL:      "https://proxy.example.com/v1",
		Models:       []string{"model-a", "model-b"},
		Default:      "model-a",
		APIKeyEnv:    "CUSTOM_API_KEY",
		Price:        &provider.Pricing{Input: 1, Output: 2, Currency: "$"},
		Prices:       map[string]*provider.Pricing{"model-b": {Input: 3, Output: 4, Currency: "$"}},
		Thinking:     "adaptive",
		Effort:       "high",
		VisionDetail: "low",
		NoProxy:      true,
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	app := NewApp()
	settings := app.Settings()
	var view ProviderView
	found := false
	for _, p := range settings.Providers {
		if p.Name == "custom" {
			view = p
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Settings providers missing custom: %+v", settings.Providers)
	}

	if err := app.SaveProvider(view); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	gotCfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := gotCfg.Provider("custom")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.Price == nil || got.Price.Input != 1 || got.Price.Output != 2 || got.Price.Currency != "$" {
		t.Fatalf("provider-wide price = %+v, want preserved", got.Price)
	}
	if got.Prices["model-b"] == nil || got.Prices["model-b"].Input != 3 || got.Prices["model-b"].Output != 4 || got.Prices["model-b"].Currency != "$" {
		t.Fatalf("per-model prices = %+v, want model-b price preserved", got.Prices)
	}
	if got.Thinking != "adaptive" || got.Effort != "high" {
		t.Fatalf("thinking/effort = %q/%q, want adaptive/high", got.Thinking, got.Effort)
	}
	if got.VisionDetail != "low" {
		t.Fatalf("vision_detail = %q, want low", got.VisionDetail)
	}
	if !got.NoProxy {
		t.Fatal("no_proxy = false, want preserved true")
	}
}

func TestSaveProviderClearsProviderWideVisionForPerModelSelection(t *testing.T) {
	isolateDesktopUserDirs(t)

	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.Providers = []config.ProviderEntry{{
		Name:    "custom",
		Kind:    "openai",
		BaseURL: "https://proxy.example.com/v1",
		Models:  []string{"text-only", "qwen-vl-plus"},
		Default: "text-only",
		Vision:  true,
	}}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	if err := NewApp().SaveProvider(ProviderView{
		Name:            "custom",
		Kind:            "openai",
		BaseURL:         "https://proxy.example.com/v1",
		Models:          []string{"text-only", "qwen-vl-plus"},
		VisionModels:    []string{"qwen-vl-plus"},
		VisionModelsSet: true,
		Default:         "text-only",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	gotCfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := gotCfg.Provider("custom")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.Vision {
		t.Fatal("saved provider kept provider-wide vision=true")
	}
	if got, want := got.VisionModels, []string{"qwen-vl-plus"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("saved provider vision_models = %v, want %v", got, want)
	}
	textOnly := *got
	textOnly.Model = "text-only"
	if config.EffectiveVision(&textOnly) {
		t.Fatal("unchecked text-only model should not inherit image input")
	}
	vision := *got
	vision.Model = "qwen-vl-plus"
	if !config.EffectiveVision(&vision) {
		t.Fatal("checked vision model should keep image input")
	}
}

func TestSaveProviderPreservesExplicitEmptyVisionModels(t *testing.T) {
	isolateDesktopUserDirs(t)

	if err := NewApp().SaveProvider(ProviderView{
		Name:            "custom",
		Kind:            "openai",
		BaseURL:         "https://proxy.example.com/v1",
		Models:          []string{"text-only", "qwen-vl-plus"},
		VisionModels:    []string{},
		VisionModelsSet: true,
		Default:         "text-only",
	}); err != nil {
		t.Fatalf("SaveProvider: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	got, ok := cfg.Provider("custom")
	if !ok {
		t.Fatal("saved provider not found")
	}
	if got.VisionModels == nil || len(got.VisionModels) != 0 {
		t.Fatalf("saved provider vision_models = %#v, want explicit empty list", got.VisionModels)
	}
	raw, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatalf("read saved config: %v", err)
	}
	if !strings.Contains(string(raw), `vision_models = []`) {
		t.Fatalf("saved config did not persist explicit empty vision_models:\n%s", raw)
	}
}

func TestOfficialMimoAPITemplateRemoved(t *testing.T) {
	if entries, keyEnv, err := officialProviderTemplate("mimo-api", "en"); err == nil {
		t.Fatalf("officialProviderTemplate(mimo-api) = entries=%v key=%q nil error, want unknown template", entries, keyEnv)
	}
}

func TestOfficialDeepSeekTemplateDefaultsToRMBPricing(t *testing.T) {
	entries, keyEnv, err := officialProviderTemplate("deepseek", "en")
	if err != nil {
		t.Fatalf("officialProviderTemplate: %v", err)
	}
	if keyEnv != "DEEPSEEK_API_KEY" || len(entries) != 1 {
		t.Fatalf("template = %v/%q, want one DEEPSEEK_API_KEY entry", entries, keyEnv)
	}
	got := entries[0]
	if got.Prices["deepseek-v4-flash"] == nil || got.Prices["deepseek-v4-flash"].Currency != "¥" || got.Prices["deepseek-v4-flash"].Output != 2 {
		t.Fatalf("deepseek-v4-flash price = %+v, want RMB pricing", got.Prices["deepseek-v4-flash"])
	}
	if got.Prices["deepseek-v4-pro"] == nil || got.Prices["deepseek-v4-pro"].Currency != "¥" || got.Prices["deepseek-v4-pro"].Output != 6 {
		t.Fatalf("deepseek-v4-pro price = %+v, want RMB pricing", got.Prices["deepseek-v4-pro"])
	}
}

func TestOfficialDeepSeekTemplateIncludesVisionExp(t *testing.T) {
	entries, _, err := officialProviderTemplate("deepseek", "en")
	if err != nil {
		t.Fatalf("officialProviderTemplate: %v", err)
	}
	got := entries[0]
	if !reflect.DeepEqual(got.Models, []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"}) {
		t.Fatalf("deepseek template models = %v, want vision-exp appended", got.Models)
	}
	if !reflect.DeepEqual(got.VisionModels, []string{"deepseek-v4-flash-vision-exp"}) {
		t.Fatalf("deepseek template vision_models = %v, want only vision-exp", got.VisionModels)
	}
}

func TestOfficialOpenAITemplateIncludesGPT56(t *testing.T) {
	entries, keyEnv, err := officialProviderTemplate("openai", "en")
	if err != nil {
		t.Fatalf("officialProviderTemplate: %v", err)
	}
	if keyEnv != "OPENAI_API_KEY" {
		t.Fatalf("key env = %q, want OPENAI_API_KEY", keyEnv)
	}
	got := entries[0]
	if got.ContextWindow != 1_050_000 {
		t.Fatalf("openai template context window = %d, want 1050000", got.ContextWindow)
	}
	for _, model := range []string{"gpt-5.6", "gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"} {
		if !slices.Contains(got.Models, model) {
			t.Fatalf("openai template models = %v, want %q", got.Models, model)
		}
		if !slices.Contains(got.VisionModels, model) {
			t.Fatalf("openai template vision_models = %v, want %q", got.VisionModels, model)
		}
	}
}

func TestSetAgentParamsPersistsStepLimitsToUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetAgentParams(0.35, 37, 9, "custom system"); err != nil {
		t.Fatalf("SetAgentParams: %v", err)
	}

	view := app.Settings()
	if view.Agent.MaxSteps != 37 || view.Agent.PlannerMaxSteps != 9 {
		t.Fatalf("Settings().Agent = %+v, want maxSteps=37 plannerMaxSteps=9", view.Agent)
	}
	if view.Agent.Temperature != 0.35 || view.Agent.SystemPrompt != "custom system" {
		t.Fatalf("Settings().Agent did not preserve other agent params: %+v", view.Agent)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Agent.MaxSteps != 37 || cfg.Agent.PlannerMaxSteps != 9 {
		t.Fatalf("saved config agent steps = max:%d planner:%d, want 37/9", cfg.Agent.MaxSteps, cfg.Agent.PlannerMaxSteps)
	}
	if cfg.Agent.Temperature != 0.35 || cfg.Agent.SystemPrompt != "custom system" {
		t.Fatalf("saved config did not preserve other agent params: %+v", cfg.Agent)
	}
}

func TestSetReasoningLanguagePersistsToUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetReasoningLanguage("zh"); err != nil {
		t.Fatalf("SetReasoningLanguage: %v", err)
	}

	view := app.Settings()
	if view.Agent.ReasoningLanguage != "zh" {
		t.Fatalf("Settings().Agent.ReasoningLanguage = %q, want zh", view.Agent.ReasoningLanguage)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Agent.ReasoningLanguage != "zh" || cfg.ReasoningLanguage() != "zh" {
		t.Fatalf("saved reasoning language = %q/%q, want zh", cfg.Agent.ReasoningLanguage, cfg.ReasoningLanguage())
	}
}

func TestSetAgentPromptStylesCanonicalizesAndPersists(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	// Duplicate and out-of-order IDs are deduped and returned in catalog order.
	if err := app.SetAgentPromptStyles([]string{"obsessive_compulsive", "paranoid", "PARANOID"}); err != nil {
		t.Fatalf("SetAgentPromptStyles: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if want := []string{"paranoid", "obsessive_compulsive"}; !reflect.DeepEqual(cfg.Agent.PromptStyles, want) {
		t.Fatalf("saved prompt_styles = %v, want %v", cfg.Agent.PromptStyles, want)
	}

	view := app.Settings()
	if len(view.AgentPromptStyles) != 10 {
		t.Fatalf("AgentPromptStyles catalog length = %d, want 10", len(view.AgentPromptStyles))
	}
	selected := map[string]bool{}
	for _, st := range view.AgentPromptStyles {
		if st.Selected {
			selected[st.ID] = true
		}
	}
	if len(selected) != 2 || !selected["paranoid"] || !selected["obsessive_compulsive"] {
		t.Fatalf("selected styles = %v, want {paranoid, obsessive_compulsive}", selected)
	}
	// Labels stay the exact Chinese product data, never a translated fallback.
	paranoid := view.AgentPromptStyles[0]
	if paranoid.Disorder != "偏执型" || paranoid.StyleName != "风险审查者" {
		t.Fatalf("catalog[0] label = %q｜%q, want 偏执型｜风险审查者", paranoid.Disorder, paranoid.StyleName)
	}
	if !strings.Contains(paranoid.Capability, "保持高度警觉") {
		t.Fatalf("catalog[0] capability lost the exact text: %q", paranoid.Capability)
	}
}

func TestSetAgentPromptStylesRejectsUnknownIDsWithoutPersisting(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetAgentPromptStyles([]string{"paranoid"}); err != nil {
		t.Fatalf("seed SetAgentPromptStyles: %v", err)
	}
	if err := app.SetAgentPromptStyles([]string{"paranoid", "bogus-style"}); err == nil {
		t.Fatal("expected error for unknown style ID")
	} else if !strings.Contains(err.Error(), "bogus-style") {
		t.Fatalf("error %q does not name the unknown ID", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if want := []string{"paranoid"}; !reflect.DeepEqual(cfg.Agent.PromptStyles, want) {
		t.Fatalf("config mutated on invalid set: %v, want %v", cfg.Agent.PromptStyles, want)
	}
}

func TestSetAgentPromptStylesClearPersistsEmpty(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetAgentPromptStyles([]string{"paranoid"}); err != nil {
		t.Fatalf("seed SetAgentPromptStyles: %v", err)
	}
	if err := app.SetAgentPromptStyles(nil); err != nil {
		t.Fatalf("clear SetAgentPromptStyles: %v", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if len(cfg.Agent.PromptStyles) != 0 {
		t.Fatalf("prompt_styles not cleared: %v", cfg.Agent.PromptStyles)
	}
}

func TestSetAgentPromptStylesPersistsWhileControllerBusy(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	ctrl := newBackgroundJobController(t, "agent-prompt-style-job")
	app.setTestCtrl(ctrl, "")

	if err := app.SetAgentPromptStyles([]string{"paranoid"}); err != nil {
		t.Fatalf("SetAgentPromptStyles while busy: %v", err)
	}
	if app.activeCtrl() != ctrl {
		t.Fatal("active controller changed while background work was running")
	}
	if !app.configRebuildNeeded.Load() {
		t.Fatal("busy save did not schedule a deferred controller rebuild")
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if want := []string{"paranoid"}; !reflect.DeepEqual(cfg.Agent.PromptStyles, want) {
		t.Fatalf("saved prompt_styles = %v, want %v", cfg.Agent.PromptStyles, want)
	}
}

func TestSetProviderKeyPersistsWhileControllerBusy(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("TEST_BUSY_PROVIDER_KEY", "")
	os.Unsetenv("TEST_BUSY_PROVIDER_KEY")

	app := NewApp()
	ctrl := newBackgroundJobController(t, "provider-key-job")
	app.setTestCtrl(ctrl, "")

	warning, err := app.SetProviderKey("TEST_BUSY_PROVIDER_KEY", "sk-busy")
	if err != nil {
		t.Fatalf("SetProviderKey while busy: %v", err)
	}
	if warning != "" {
		t.Fatalf("unexpected warning %q", warning)
	}
	if app.activeCtrl() != ctrl {
		t.Fatal("active controller changed while background work was running")
	}
	if !app.configRebuildNeeded.Load() {
		t.Fatal("busy key save did not schedule a deferred controller rebuild")
	}
	if !config.CredentialStored("TEST_BUSY_PROVIDER_KEY") {
		t.Fatal("provider credential not persisted while busy")
	}
}

func TestClearProviderKeyPersistsWhileControllerBusy(t *testing.T) {
	isolateDesktopUserDirs(t)
	t.Setenv("TEST_BUSY_CLEAR_KEY", "")
	os.Unsetenv("TEST_BUSY_CLEAR_KEY")

	app := NewApp()
	if _, err := app.SetProviderKey("TEST_BUSY_CLEAR_KEY", "sk-busy"); err != nil {
		t.Fatalf("seed SetProviderKey: %v", err)
	}
	if !config.CredentialStored("TEST_BUSY_CLEAR_KEY") {
		t.Fatal("seed provider credential missing")
	}

	ctrl := newBackgroundJobController(t, "provider-clear-job")
	app.setTestCtrl(ctrl, "")

	if err := app.ClearProviderKey("TEST_BUSY_CLEAR_KEY"); err != nil {
		t.Fatalf("ClearProviderKey while busy: %v", err)
	}
	if app.activeCtrl() != ctrl {
		t.Fatal("active controller changed while background work was running")
	}
	if !app.configRebuildNeeded.Load() {
		t.Fatal("busy key clear did not schedule a deferred controller rebuild")
	}
	if config.CredentialStored("TEST_BUSY_CLEAR_KEY") {
		t.Fatal("provider credential still present after clear while busy")
	}
}

func TestSetDesktopLanguagePersistsResponseLanguageAndUpdatesLiveTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "WorkGround2.toml"), []byte("language = \"zh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	userCtrl := control.New(control.Options{})
	projectCtrl := control.New(control.Options{})
	app.tabs = map[string]*WorkspaceTab{
		"user": {
			ID:          "user",
			Scope:       "global",
			Ctrl:        userCtrl,
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		},
		"project": {
			ID:            "project",
			Scope:         "project",
			WorkspaceRoot: projectRoot,
			Ctrl:          projectCtrl,
			Ready:         true,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "user"

	if err := app.SetDesktopLanguage("en"); err != nil {
		t.Fatalf("SetDesktopLanguage: %v", err)
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.DesktopLanguage() != "en" || cfg.Language != "en" {
		t.Fatalf("saved language prefs = desktop:%q response:%q, want en/en", cfg.DesktopLanguage(), cfg.Language)
	}
	got := userCtrl.Compose("解释这个函数")
	if !strings.Contains(got, "<response-language>") || !strings.Contains(got, "use English") {
		t.Fatalf("live controller Compose = %q, want English response language", got)
	}
	projectComposed := projectCtrl.Compose("explain this function")
	if !strings.Contains(projectComposed, "use Simplified Chinese") {
		t.Fatalf("project controller Compose = %q, want project zh response language", projectComposed)
	}
}

func TestSetReasoningLanguageUpdatesLiveTabControllers(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "WorkGround2.toml"), []byte("[agent]\nreasoning_language = \"en\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	userCtrl := control.New(control.Options{ReasoningLanguage: "auto"})
	projectCtrl := control.New(control.Options{ReasoningLanguage: "auto"})
	app.tabs = map[string]*WorkspaceTab{
		"user": {
			ID:          "user",
			Scope:       "global",
			Ctrl:        userCtrl,
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		},
		"project": {
			ID:            "project",
			Scope:         "project",
			WorkspaceRoot: projectRoot,
			Ctrl:          projectCtrl,
			Ready:         true,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "user"

	if err := app.SetReasoningLanguage("zh"); err != nil {
		t.Fatalf("SetReasoningLanguage: %v", err)
	}

	userComposed := userCtrl.Compose("hi")
	if !strings.Contains(userComposed, "Simplified Chinese") {
		t.Fatalf("user-level tab Compose = %q, want zh reasoning language", userComposed)
	}
	projectComposed := projectCtrl.Compose("hi")
	if !strings.Contains(projectComposed, "use English") {
		t.Fatalf("project override tab Compose = %q, want en reasoning language", projectComposed)
	}
}

func TestSetAutoPlanUpdatesLiveTabControllers(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	userRunner := &captureTurnRunner{}
	projectRunner := &captureTurnRunner{}
	userCtrl := control.New(control.Options{AutoPlan: "on", Runner: userRunner})
	projectCtrl := control.New(control.Options{AutoPlan: "on", Runner: projectRunner})
	app.tabs = map[string]*WorkspaceTab{
		"user": {
			ID:          "user",
			Scope:       "global",
			Ctrl:        userCtrl,
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		},
		"project": {
			ID:            "project",
			Scope:         "project",
			WorkspaceRoot: t.TempDir(),
			Ctrl:          projectCtrl,
			Ready:         true,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "user"

	if err := app.SetAutoPlan("off"); err != nil {
		t.Fatalf("SetAutoPlan: %v", err)
	}

	input := "实现 GitHub issue #2395：\n- 新增配置项\n- 自动判断复杂任务\n- 补测试和文档"
	if err := userCtrl.RunTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := projectCtrl.RunTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(userRunner.inputs) != 1 || strings.HasPrefix(userRunner.inputs[0], control.PlanModeMarker) {
		t.Fatalf("user tab should use updated auto_plan=off, inputs=%q", userRunner.inputs)
	}
	if len(projectRunner.inputs) != 1 || strings.HasPrefix(projectRunner.inputs[0], control.PlanModeMarker) {
		t.Fatalf("project tab without override should use updated auto_plan=off, inputs=%q", projectRunner.inputs)
	}
}

func TestSetAutoPlanIgnoresProjectOverrideForLiveTab(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectRoot, "WorkGround2.toml"), []byte("[agent]\nauto_plan = \"on\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	userRunner := &captureTurnRunner{}
	projectRunner := &captureTurnRunner{}
	userCtrl := control.New(control.Options{AutoPlan: "on", Runner: userRunner})
	projectCtrl := control.New(control.Options{AutoPlan: "on", Runner: projectRunner})
	app.tabs = map[string]*WorkspaceTab{
		"user": {
			ID:          "user",
			Scope:       "global",
			Ctrl:        userCtrl,
			Ready:       true,
			disabledMCP: map[string]ServerView{},
		},
		"project": {
			ID:            "project",
			Scope:         "project",
			WorkspaceRoot: projectRoot,
			Ctrl:          projectCtrl,
			Ready:         true,
			disabledMCP:   map[string]ServerView{},
		},
	}
	app.activeTabID = "user"

	if err := app.SetAutoPlan("off"); err != nil {
		t.Fatalf("SetAutoPlan: %v", err)
	}

	input := "实现 GitHub issue #2395：\n- 新增配置项\n- 自动判断复杂任务\n- 补测试和文档"
	if err := userCtrl.RunTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := projectCtrl.RunTurn(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(userRunner.inputs) != 1 || strings.HasPrefix(userRunner.inputs[0], control.PlanModeMarker) {
		t.Fatalf("user tab should use updated auto_plan=off, inputs=%q", userRunner.inputs)
	}
	if len(projectRunner.inputs) != 1 || strings.HasPrefix(projectRunner.inputs[0], control.PlanModeMarker) {
		t.Fatalf("project auto_plan should be ignored, inputs=%q", projectRunner.inputs)
	}
}

func TestSetAutoPlanEnablingClassifierRebuildsActiveController(t *testing.T) {
	isolateDesktopUserDirs(t)

	cfg := config.LoadForEdit(config.UserConfigPath())
	cfg.Agent.AutoPlan = "off"
	cfg.Agent.AutoPlanClassifier = "deepseek-flash"
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatalf("save config: %v", err)
	}

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{AutoPlan: "off", Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetAutoPlan("on"); err != nil {
		t.Fatalf("SetAutoPlan(on): %v", err)
	}
	if c := app.activeCtrl(); c == nil {
		t.Fatal("SetAutoPlan should leave a rebuilt controller")
	}
	if c := app.activeCtrl(); c == old {
		t.Fatal("SetAutoPlan should rebuild when enabling a configured classifier")
	}

	got := config.LoadForEdit(config.UserConfigPath())
	if got.Agent.AutoPlan != "on" {
		t.Fatalf("saved auto_plan = %q, want on", got.Agent.AutoPlan)
	}
}

func TestSetReasoningLanguageRejectsBackgroundJobsBeforeSavingConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.setTestCtrl(newBackgroundJobController(t, "reasoning-language-job"), "")

	err := app.SetReasoningLanguage("zh")
	if err == nil || !strings.Contains(err.Error(), "stop background jobs") {
		t.Fatalf("SetReasoningLanguage with background job error = %v, want active-work guard", err)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.ReasoningLanguage() != "auto" {
		t.Fatalf("reasoning language changed after rejected update: %q", cfg.ReasoningLanguage())
	}
}

func TestSetDesktopCheckUpdatesPersistsToUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if !app.Settings().CheckUpdates {
		t.Fatal("Settings().CheckUpdates default = false, want true")
	}
	if err := app.SetDesktopCheckUpdates(false); err != nil {
		t.Fatalf("SetDesktopCheckUpdates: %v", err)
	}
	view := app.Settings()
	if view.CheckUpdates {
		t.Fatal("Settings().CheckUpdates = true, want false")
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Desktop.CheckUpdates == nil || *cfg.Desktop.CheckUpdates {
		t.Fatalf("desktop.check_updates = %+v, want false", cfg.Desktop.CheckUpdates)
	}
	if cfg.DesktopCheckUpdates() {
		t.Fatal("DesktopCheckUpdates() = true, want false")
	}
}

func TestSetDefaultToolApprovalModePersistsToUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if app.Settings().DefaultToolApprovalMode != control.ToolApprovalAsk {
		t.Fatalf("Settings().DefaultToolApprovalMode = %q, want ask", app.Settings().DefaultToolApprovalMode)
	}
	if err := app.SetDefaultToolApprovalMode(control.ToolApprovalAuto); err != nil {
		t.Fatalf("SetDefaultToolApprovalMode: %v", err)
	}
	view := app.Settings()
	if view.DefaultToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("Settings().DefaultToolApprovalMode = %q, want auto", view.DefaultToolApprovalMode)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Desktop.DefaultToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("desktop.default_tool_approval_mode = %q, want auto", cfg.Desktop.DefaultToolApprovalMode)
	}
	if cfg.DesktopDefaultToolApprovalMode() != control.ToolApprovalAuto {
		t.Fatalf("DesktopDefaultToolApprovalMode() = %q, want auto", cfg.DesktopDefaultToolApprovalMode())
	}
}

func TestSetDesktopComposerSubmitKeyPersistsToUserConfig(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if app.Settings().ComposerSubmitKey != "enter" {
		t.Fatalf("Settings().ComposerSubmitKey = %q, want enter", app.Settings().ComposerSubmitKey)
	}
	if err := app.SetDesktopComposerSubmitKey("ctrl_enter"); err != nil {
		t.Fatalf("SetDesktopComposerSubmitKey: %v", err)
	}
	view := app.Settings()
	if view.ComposerSubmitKey != "ctrl_enter" {
		t.Fatalf("Settings().ComposerSubmitKey = %q, want ctrl_enter", view.ComposerSubmitKey)
	}
	startup := app.DesktopStartupSettings()
	if startup.ComposerSubmitKey != "ctrl_enter" {
		t.Fatalf("DesktopStartupSettings().ComposerSubmitKey = %q, want ctrl_enter", startup.ComposerSubmitKey)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Desktop.ComposerSubmitKey != "ctrl_enter" {
		t.Fatalf("desktop.composer_submit_key = %q, want ctrl_enter", cfg.Desktop.ComposerSubmitKey)
	}
}

func TestUserConfigMutationsSerializeWithoutLostUpdates(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- app.applyConfigOnly(func(cfg *config.Config) error {
			if err := cfg.SetDesktopComposerSubmitKey("ctrl_enter"); err != nil {
				return err
			}
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondEntered := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- app.applyConfigOnly(func(cfg *config.Config) error {
			close(secondEntered)
			return cfg.SetDesktopDefaultToolApprovalMode(control.ToolApprovalAuto)
		})
	}()
	<-secondStarted

	select {
	case <-secondEntered:
		t.Fatal("second config mutation entered before the first transaction saved")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)

	for name, done := range map[string]<-chan error{"first": firstDone, "second": secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s config mutation: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for %s config mutation", name)
		}
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if got := cfg.DesktopComposerSubmitKey(); got != "ctrl_enter" {
		t.Fatalf("composer submit key after concurrent saves = %q, want ctrl_enter", got)
	}
	if got := cfg.DesktopDefaultToolApprovalMode(); got != control.ToolApprovalAuto {
		t.Fatalf("default approval after concurrent saves = %q, want auto", got)
	}

	restarted := NewApp().Settings()
	if restarted.ComposerSubmitKey != "ctrl_enter" || restarted.DefaultToolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("settings after restart = submit %q, approval %q", restarted.ComposerSubmitKey, restarted.DefaultToolApprovalMode)
	}
}

func TestSetDesktopMetricsDefaultsOnAndPersistsOff(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if !app.Settings().Metrics {
		t.Fatal("Settings().Metrics default = false, want true")
	}
	if err := app.SetDesktopMetrics(false); err != nil {
		t.Fatalf("SetDesktopMetrics: %v", err)
	}
	view := app.Settings()
	if view.Metrics {
		t.Fatal("Settings().Metrics = true, want false")
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Desktop.Metrics == nil || *cfg.Desktop.Metrics {
		t.Fatalf("desktop.metrics = %+v, want false", cfg.Desktop.Metrics)
	}
	if cfg.DesktopMetrics() {
		t.Fatal("DesktopMetrics() = true, want false")
	}
}

func TestSettingsBrowserPermissionsDefaultTrue(t *testing.T) {
	isolateDesktopUserDirs(t)

	view := NewApp().Settings()
	if !view.Permissions.Browser.AllowPasswordInput || !view.Permissions.Browser.AllowFileUpload {
		t.Fatalf("default browser permissions = %+v, want both true", view.Permissions.Browser)
	}
	if view.BrowserLaunch.Incognito {
		t.Fatalf("default browser incognito = true, want false")
	}
}

func TestSetBrowserPermissionsPersistsExplicitValuesAndRebuilds(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetBrowserPermissions(BrowserPermissionsView{AllowPasswordInput: false, AllowFileUpload: false}); err != nil {
		t.Fatalf("SetBrowserPermissions: %v", err)
	}

	view := app.Settings()
	if view.Permissions.Browser.AllowPasswordInput || view.Permissions.Browser.AllowFileUpload {
		t.Fatalf("Settings() browser permissions = %+v, want false false", view.Permissions.Browser)
	}

	raw, err := os.ReadFile(config.UserConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"allow_password_input = false", "allow_file_upload = false"} {
		if !strings.Contains(text, want) {
			t.Fatalf("saved TOML missing %q:\n%s", want, text)
		}
	}

	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Tools.Browser.AllowPasswordInput == nil || *cfg.Tools.Browser.AllowPasswordInput {
		t.Fatalf("saved allow_password_input = %v, want explicit false", cfg.Tools.Browser.AllowPasswordInput)
	}
	if cfg.Tools.Browser.AllowFileUpload == nil || *cfg.Tools.Browser.AllowFileUpload {
		t.Fatalf("saved allow_file_upload = %v, want explicit false", cfg.Tools.Browser.AllowFileUpload)
	}

	if c := app.activeCtrl(); c == nil || c == old {
		t.Fatal("SetBrowserPermissions must rebuild the controller")
	}
}

func TestSetBrowserLaunchPersistsIncognitoAndRebuilds(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	app.ctx = context.Background()
	app.readyHook = func() {}
	old := control.New(control.Options{Label: "old-controller"})
	app.setTestCtrl(old, "deepseek-flash/deepseek-v4-flash")
	defer func() {
		if c := app.activeCtrl(); c != nil {
			c.Close()
		}
	}()

	if err := app.SetBrowserLaunch(BrowserLaunchView{Incognito: true}); err != nil {
		t.Fatalf("SetBrowserLaunch: %v", err)
	}
	if !app.Settings().BrowserLaunch.Incognito {
		t.Fatal("Settings() browser launch incognito = false, want true")
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Tools.Browser.Incognito == nil || !*cfg.Tools.Browser.Incognito {
		t.Fatalf("saved incognito = %v, want explicit true", cfg.Tools.Browser.Incognito)
	}
	if c := app.activeCtrl(); c == nil || c == old {
		t.Fatal("SetBrowserLaunch must rebuild the controller")
	}
}

func TestSetBrowserPermissionsSwitchesAreIndependent(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if err := app.SetBrowserPermissions(BrowserPermissionsView{AllowPasswordInput: false, AllowFileUpload: true}); err != nil {
		t.Fatalf("SetBrowserPermissions: %v", err)
	}
	view := app.Settings()
	if view.Permissions.Browser.AllowPasswordInput || !view.Permissions.Browser.AllowFileUpload {
		t.Fatalf("independent switch save = %+v, want password=false file=true", view.Permissions.Browser)
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Tools.Browser.AllowPasswordInput == nil || *cfg.Tools.Browser.AllowPasswordInput {
		t.Fatalf("saved allow_password_input = %v, want explicit false", cfg.Tools.Browser.AllowPasswordInput)
	}
	if cfg.Tools.Browser.AllowFileUpload == nil || !*cfg.Tools.Browser.AllowFileUpload {
		t.Fatalf("saved allow_file_upload = %v, want explicit true", cfg.Tools.Browser.AllowFileUpload)
	}
}

func TestSetMemoryCompilerDefaultsOnAndPersistsOff(t *testing.T) {
	isolateDesktopUserDirs(t)

	app := NewApp()
	if !app.Settings().MemoryCompiler {
		t.Fatal("Settings().MemoryCompiler default = false, want true")
	}
	if err := app.SetMemoryCompilerEnabled(false); err != nil {
		t.Fatalf("SetMemoryCompilerEnabled: %v", err)
	}
	view := app.Settings()
	if view.MemoryCompiler {
		t.Fatal("Settings().MemoryCompiler = true, want false")
	}
	cfg := config.LoadForEdit(config.UserConfigPath())
	if cfg.Agent.MemoryCompiler.Enabled == nil || *cfg.Agent.MemoryCompiler.Enabled {
		t.Fatalf("agent.memory_compiler.enabled = %+v, want false", cfg.Agent.MemoryCompiler.Enabled)
	}
	if cfg.MemoryCompilerEnabled() {
		t.Fatal("MemoryCompilerEnabled() = true, want false")
	}
}

type memoryCompilerTargetFake struct {
	calls []bool
}

func (f *memoryCompilerTargetFake) SetMemoryCompilerEnabled(enabled bool) {
	f.calls = append(f.calls, enabled)
}

func TestApplyMemoryCompilerToControllersBroadcastsToAllTargets(t *testing.T) {
	first := &memoryCompilerTargetFake{}
	second := &memoryCompilerTargetFake{}

	applyMemoryCompilerToControllers(false, []memoryCompilerTarget{first, nil, second})

	if !reflect.DeepEqual(first.calls, []bool{false}) {
		t.Fatalf("first calls = %v, want [false]", first.calls)
	}
	if !reflect.DeepEqual(second.calls, []bool{false}) {
		t.Fatalf("second calls = %v, want [false]", second.calls)
	}
}

func TestSaveHooksSettingsPreservesUnknownSettingsKeys(t *testing.T) {
	isolateDesktopUserDirs(t)
	path := hook.GlobalSettingsPath("")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"theme":"dark","hooks":{"Stop":[{"command":"old"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	app := NewApp()
	if err := app.SaveHooksSettings("global", []HookConfigView{{
		Event:   string(hook.PreToolUse),
		Match:   "bash",
		Command: "echo guard",
	}}); err != nil {
		t.Fatalf("SaveHooksSettings: %v", err)
	}

	var raw map[string]json.RawMessage
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if string(raw["theme"]) != `"dark"` {
		t.Fatalf("theme key was not preserved: %s", raw["theme"])
	}
	view := app.HooksSettings("global")
	if len(view.Hooks) != 1 || view.Hooks[0].Event != string(hook.PreToolUse) || view.Hooks[0].Command != "echo guard" {
		t.Fatalf("HooksSettings = %+v, want saved PreToolUse hook", view)
	}
}

func TestProjectHooksSettingsUseActiveWorkspaceRootAndTrust(t *testing.T) {
	isolateDesktopUserDirs(t)
	project := t.TempDir()
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"project": {ID: "project", Scope: "project", WorkspaceRoot: project, Ready: true},
	}
	app.activeTabID = "project"

	if err := app.SaveHooksSettings("project", []HookConfigView{{
		Event:       string(hook.Stop),
		Command:     "echo done",
		Description: "Turn done",
	}}); err != nil {
		t.Fatalf("SaveHooksSettings(project): %v", err)
	}
	if err := app.TrustProjectHooks(); err != nil {
		t.Fatalf("TrustProjectHooks: %v", err)
	}
	if !hook.IsTrusted(project, "") {
		t.Fatal("project hooks were not trusted")
	}
	view := app.HooksSettings("project")
	if view.Scope != "project" || view.ProjectRoot != project || !view.Trusted {
		t.Fatalf("project hook view metadata = %+v", view)
	}
	if len(view.Hooks) != 1 || view.Hooks[0].Event != string(hook.Stop) || view.Hooks[0].Description != "Turn done" {
		t.Fatalf("project hooks = %+v", view.Hooks)
	}
	if _, err := os.Stat(filepath.Join(project, ".WorkGround2", "settings.json")); err != nil {
		t.Fatalf("project hooks settings file missing: %v", err)
	}
}

func TestTrustProjectHooksForRootUsesDisplayedProjectRoot(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectA := t.TempDir()
	projectB := t.TempDir()
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"a": {ID: "a", Scope: "project", WorkspaceRoot: projectA, Ready: true},
		"b": {ID: "b", Scope: "project", WorkspaceRoot: projectB, Ready: true},
	}
	app.activeTabID = "b"

	if err := app.TrustProjectHooksForRoot(projectA); err != nil {
		t.Fatalf("TrustProjectHooksForRoot: %v", err)
	}
	if !hook.IsTrusted(projectA, "") {
		t.Fatal("displayed project root was not trusted")
	}
	if hook.IsTrusted(projectB, "") {
		t.Fatal("active project root was trusted instead of displayed project root")
	}
}

func TestSaveHooksSettingsForRootUsesDisplayedProjectRoot(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectA := t.TempDir()
	projectB := t.TempDir()
	app := NewApp()
	app.tabs = map[string]*WorkspaceTab{
		"a": {ID: "a", Scope: "project", WorkspaceRoot: projectA, Ready: true},
		"b": {ID: "b", Scope: "project", WorkspaceRoot: projectB, Ready: true},
	}
	app.activeTabID = "b"

	if err := app.SaveHooksSettingsForRoot("project", projectA, []HookConfigView{{
		Event:   string(hook.Stop),
		Command: "echo done",
	}}); err != nil {
		t.Fatalf("SaveHooksSettingsForRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectA, ".WorkGround2", "settings.json")); err != nil {
		t.Fatalf("displayed project root settings missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectB, ".WorkGround2", "settings.json")); err == nil {
		t.Fatal("active project root was written instead of displayed project root")
	}
}

func TestSetCollaborationPersistsRelaySettingsAndInsecureConsent(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()

	secure := CollaborationSettingsView{PreferLAN: true, ConnectTimeoutSeconds: 10, RouteStableSeconds: 60, Relays: []RelayView{{
		ID: "official-sg", Name: "Singapore", URL: "wss://relay.example.test/relay/v1/connect",
		Enabled: true, Priority: 100, Discovery: true, AccessTokenEnv: "WG2_RELAY_TOKEN",
	}}}
	if err := app.SetCollaboration(secure); err != nil {
		t.Fatalf("SetCollaboration secure: %v", err)
	}
	got := app.Settings().Collaboration
	if !got.PreferLAN || got.ConnectTimeoutSeconds != 10 || got.RouteStableSeconds != 60 || len(got.Relays) != 1 || got.Relays[0].ID != "official-sg" || !got.Relays[0].Discovery || got.Relays[0].AllowInsecure {
		t.Fatalf("Settings collaboration = %+v", got)
	}

	insecure := secure
	insecure.Relays = append([]RelayView(nil), secure.Relays...)
	insecure.Relays[0].URL = "ws://relay.example.test:8443"
	if err := app.SetCollaboration(insecure); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("SetCollaboration insecure error = %v, want explicit-risk error", err)
	}
	if persisted := config.LoadForEdit(config.UserConfigPath()).Collaboration.Relays; len(persisted) != 1 || persisted[0].URL != secure.Relays[0].URL {
		t.Fatalf("rejected relay update changed persisted config: %+v", persisted)
	}

	insecure.Relays[0].AllowInsecure = true
	if err := app.SetCollaboration(insecure); err != nil {
		t.Fatalf("SetCollaboration insecure with consent: %v", err)
	}
	got = app.Settings().Collaboration
	if len(got.Relays) != 1 || !got.Relays[0].AllowInsecure || got.Relays[0].URL != "ws://relay.example.test:8443" {
		t.Fatalf("Settings insecure collaboration = %+v", got)
	}
}

func TestSettingsDoesNotSeedRelaysFromProjectConfig(t *testing.T) {
	isolateDesktopUserDirs(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "WorkGround2.toml"), []byte(`
[[collaboration.relays]]
id = "project"
name = "Project relay"
url = "ws://192.168.1.2:8443"
enabled = true
priority = 100
allow_insecure = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(project); err != nil {
		t.Fatal(err)
	}

	got := NewApp().Settings().Collaboration
	if !got.PreferLAN || len(got.Relays) != 0 {
		t.Fatalf("Settings collaboration = %+v, want user defaults", got)
	}
}

// SetDesktopWidgetAlwaysOnTop applies the runtime flag immediately while widget
// mode is active, persists the config in the same serialized step as widget
// transitions, and rolls the runtime flag back when the persist fails. Outside
// widget mode (or without a live window) it only persists, as before.
func TestSetDesktopWidgetAlwaysOnTopRuntimeWhileWidgetActive(t *testing.T) {
	isolateDesktopUserDirs(t)
	var applied []bool
	app := &App{ctx: context.Background(), widgetMode: true, windowSetAlwaysOnTop: func(on bool) error { applied = append(applied, on); return nil }}
	if err := app.SetDesktopWidgetAlwaysOnTop(false); err != nil {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(false): %v", err)
	}
	if want := []bool{false}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("runtime calls = %v, want %v", applied, want)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); got {
		t.Fatal("config still always-on-top after SetDesktopWidgetAlwaysOnTop(false)")
	}
	if err := app.SetDesktopWidgetAlwaysOnTop(true); err != nil {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(true): %v", err)
	}
	if want := []bool{false, true}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("runtime calls = %v, want %v", applied, want)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); !got {
		t.Fatal("config not always-on-top after SetDesktopWidgetAlwaysOnTop(true)")
	}
	// Repeated calls are safe: the runtime re-applies and the persist is idempotent.
	if err := app.SetDesktopWidgetAlwaysOnTop(true); err != nil {
		t.Fatalf("repeat SetDesktopWidgetAlwaysOnTop(true): %v", err)
	}
}

func TestSetDesktopWidgetAlwaysOnTopRollsBackRuntimeOnPersistFailure(t *testing.T) {
	isolateDesktopUserDirs(t)
	// Occupy the config directory with a regular file so the atomic persist
	// (which MkdirAll's the parent) fails after the runtime flag was applied.
	configDir := filepath.Dir(config.UserConfigPath())
	if err := os.MkdirAll(filepath.Dir(configDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configDir, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	var applied []bool
	app := &App{ctx: context.Background(), widgetMode: true, windowSetAlwaysOnTop: func(on bool) error { applied = append(applied, on); return nil }}
	if err := app.SetDesktopWidgetAlwaysOnTop(false); err == nil {
		t.Fatal("SetDesktopWidgetAlwaysOnTop(false) succeeded with a blocked config write")
	}
	// The default config keeps always-on-top enabled, so the failed toggle
	// must roll the runtime flag back to true.
	if want := []bool{false, true}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("runtime calls = %v, want apply-then-rollback %v", applied, want)
	}
}

func TestSetDesktopWidgetAlwaysOnTopOutsideWidgetPersistsOnly(t *testing.T) {
	isolateDesktopUserDirs(t)
	var applied []bool
	seam := func(on bool) error { applied = append(applied, on); return nil }
	// Not in widget mode: no runtime call, only the persisted config.
	app := &App{windowSetAlwaysOnTop: seam}
	if err := app.SetDesktopWidgetAlwaysOnTop(false); err != nil {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(false): %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("runtime calls outside widget mode = %v, want none", applied)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); got {
		t.Fatal("config still always-on-top outside widget mode")
	}
	// Widget mode without a live window (ctx nil) also persists only.
	app = &App{widgetMode: true, windowSetAlwaysOnTop: seam}
	if err := app.SetDesktopWidgetAlwaysOnTop(true); err != nil {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(true) without ctx: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("runtime calls without a live window = %v, want none", applied)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); !got {
		t.Fatal("config not always-on-top after persist-only set")
	}
}

// --- orchestration: the always-on-top setter, widget transitions and the
// style switch all serialize on widgetMu so a concurrent toggle can never
// interleave between a config read and its dependent native apply ---

// TestAlwaysOnTopToggleSerialisesWithWidgetTransition locks the toggle's
// runtime apply + config persist behind widgetMu and proves a concurrent
// widget transition cannot interleave (and vice versa).
func TestAlwaysOnTopToggleSerialisesWithWidgetTransition(t *testing.T) {
	t.Run("toggle in flight blocks transition", func(t *testing.T) {
		isolateDesktopUserDirs(t)
		app := &App{ctx: context.Background(), widgetMode: true}
		applied := make(chan bool, 2)
		release := make(chan struct{})
		app.windowSetAlwaysOnTop = func(on bool) error {
			applied <- on
			<-release
			return nil
		}
		toggleDone := make(chan error, 1)
		go func() { toggleDone <- app.SetDesktopWidgetAlwaysOnTop(false) }()
		if got := <-applied; got {
			t.Fatalf("toggle applied %v, want false", got)
		}
		transitionStarted := make(chan struct{})
		transitionDone := make(chan error, 1)
		go func() {
			_, err := app.transitionWidgetMode(false, func() error {
				close(transitionStarted)
				return nil
			})
			transitionDone <- err
		}()
		select {
		case <-transitionStarted:
			t.Fatal("widget transition interleaved with the toggle's apply/persist")
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if err := <-toggleDone; err != nil {
			t.Fatalf("toggle: %v", err)
		}
		if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); got {
			t.Fatal("config still always-on-top after the serialized toggle")
		}
		select {
		case <-transitionStarted:
		case <-time.After(time.Second):
			t.Fatal("transition did not resume after the toggle")
		}
		if err := <-transitionDone; err != nil {
			t.Fatalf("transition: %v", err)
		}
	})

	t.Run("transition in flight blocks toggle runtime apply", func(t *testing.T) {
		isolateDesktopUserDirs(t)
		app := &App{ctx: context.Background(), widgetMode: true}
		readDone := make(chan struct{})
		release := make(chan struct{})
		// Mirror applyEnterWidgetMode: the transition reads the persisted
		// always-on-top preference inside its widgetMu critical section.
		transitionApply := func() error {
			if _, _, err := app.desktopWidgetPreferences(); err != nil {
				return err
			}
			close(readDone)
			<-release
			return nil
		}
		transitionDone := make(chan error, 1)
		go func() {
			_, err := app.transitionWidgetMode(false, transitionApply)
			transitionDone <- err
		}()
		<-readDone

		applied := make(chan bool, 2)
		app.windowSetAlwaysOnTop = func(on bool) error { applied <- on; return nil }
		toggleDone := make(chan error, 1)
		go func() { toggleDone <- app.SetDesktopWidgetAlwaysOnTop(false) }()
		select {
		case on := <-applied:
			t.Fatalf("toggle applied %v while the transition held widgetMu", on)
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		if err := <-transitionDone; err != nil {
			t.Fatalf("transition: %v", err)
		}
		// The transition exited widget mode, so the queued toggle degrades to
		// the inactive persist-only path: no runtime apply, config persisted.
		if err := <-toggleDone; err != nil {
			t.Fatalf("toggle: %v", err)
		}
		select {
		case on := <-applied:
			t.Fatalf("toggle applied runtime %v after widget mode exited", on)
		default:
		}
		if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); got {
			t.Fatal("config not always-on-top=false after the queued toggle")
		}
	})
}

// TestAlwaysOnTopToggleRuntimeFailureDoesNotPersist: a runtime apply failure
// returns immediately and must never persist the unapplied preference.
func TestAlwaysOnTopToggleRuntimeFailureDoesNotPersist(t *testing.T) {
	isolateDesktopUserDirs(t)
	applyErr := errors.New("window always-on-top apply failed")
	var applied []bool
	app := &App{
		ctx:        context.Background(),
		widgetMode: true,
		windowSetAlwaysOnTop: func(on bool) error {
			applied = append(applied, on)
			return applyErr
		},
	}
	if err := app.SetDesktopWidgetAlwaysOnTop(false); !errors.Is(err, applyErr) {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(false) = %v, want %v", err, applyErr)
	}
	if want := []bool{false}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("runtime calls = %v, want %v", applied, want)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetAlwaysOnTop(); !got {
		t.Fatal("config persisted the always-on-top change that failed at runtime")
	}
}

// TestAlwaysOnTopTogglePersistAndRollbackFailureReturnsBoth: when the persist
// fails and the runtime rollback also fails, the caller receives both errors.
func TestAlwaysOnTopTogglePersistAndRollbackFailureReturnsBoth(t *testing.T) {
	isolateDesktopUserDirs(t)
	configDir := filepath.Dir(config.UserConfigPath())
	if err := os.MkdirAll(filepath.Dir(configDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configDir, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	rollbackErr := errors.New("rollback always-on-top failed")
	calls := 0
	app := &App{
		ctx:        context.Background(),
		widgetMode: true,
		windowSetAlwaysOnTop: func(on bool) error {
			calls++
			if calls > 1 {
				return rollbackErr
			}
			return nil
		},
	}
	err := app.SetDesktopWidgetAlwaysOnTop(false)
	if err == nil {
		t.Fatal("SetDesktopWidgetAlwaysOnTop(false) succeeded with a blocked config write")
	}
	// The returned error must carry both the persist failure and the failed
	// rollback; if only the persist error were returned, the rollback failure
	// would be invisible.
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("error must surface the rollback failure: %v", err)
	}
	if calls != 2 {
		t.Fatalf("runtime calls = %d, want apply-then-rollback (2)", calls)
	}
}

// TestDesktopStartupSettingsProjectsAlwaysOnTopFalse: the startup contract must
// project the persisted false value, never assume the default true.
func TestDesktopStartupSettingsProjectsAlwaysOnTopFalse(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{}
	if got := app.DesktopStartupSettings().WidgetAlwaysOnTop; !got {
		t.Fatal("default startup settings must keep always-on-top enabled")
	}
	if err := app.SetDesktopWidgetAlwaysOnTop(false); err != nil {
		t.Fatalf("SetDesktopWidgetAlwaysOnTop(false): %v", err)
	}
	if got := app.DesktopStartupSettings().WidgetAlwaysOnTop; got {
		t.Fatal("startup settings must project the persisted false value")
	}
}

// TestSwitchWidgetStyleBranchesAreDeterministic covers the switchDesktopWidgetStyle
// branches that need no live native window: outside widget mode it is a no-op,
// and a style that already matches the runtime mirror reads config under
// widgetMu and returns without touching the window.
func TestSwitchWidgetStyleBranchesAreDeterministic(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{ctx: context.Background()}
	previous, err := app.switchDesktopWidgetStyle("icons")
	if err != nil || previous != "" {
		t.Fatalf("switch outside widget mode = (%q, %v), want no-op", previous, err)
	}
	app = &App{ctx: context.Background(), widgetMode: true, widgetStyle: "icons"}
	previous, err = app.switchDesktopWidgetStyle("icons")
	if err != nil || previous != "icons" {
		t.Fatalf("no-op switch = (%q, %v), want previous style retained", previous, err)
	}
}

// TestSetWidgetStylePersistsWithoutWindow: with widget mode inactive the style
// setter only persists (the switch is a no-op), and a blocked persist rolls
// back through the same no-op path without touching the window.
func TestSetWidgetStylePersistsWithoutWindow(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{}
	if err := app.SetDesktopWidgetStyle("icons"); err != nil {
		t.Fatalf("SetDesktopWidgetStyle(icons): %v", err)
	}
	if got := config.LoadForEdit(config.UserConfigPath()).DesktopWidgetStyle(); got != "icons" {
		t.Fatalf("config widget style = %q, want icons", got)
	}
	// Block the next persist the same way the toggle tests do: replace the
	// config directory with a regular file so the atomic save cannot MkdirAll
	// its parent. The style setter must surface the failure and leave the
	// previous persisted value intact.
	configPath := config.UserConfigPath()
	configDir := filepath.Dir(configPath)
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(configDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configDir, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := app.SetDesktopWidgetStyle("icons"); err == nil {
		t.Fatal("SetDesktopWidgetStyle succeeded with a blocked config write")
	}
}

// --- widget style orchestration tests ---
//
// These exercise the real SetDesktopWidgetStyle / applyEnterWidgetMode /
// applyExitWidgetMode paths with the native window geometry replaced by the
// widgetWindowOps seam, instead of mirroring the orchestration logic.

type widgetApplyRecord struct {
	state       WidgetWindowState
	alwaysOnTop bool
	icons       bool
}

// widgetStyleTestApp builds an App whose native window geometry is fully
// replaced by the given ops seam, so the real Enter/Exit/SetDesktopWidgetStyle
// orchestration runs without a live Wails/Win32 window. Both widget geometry
// files are pre-seeded so the transitions never fall back to
// default-window computation (which needs a live window).
func widgetStyleTestApp(t *testing.T, ops *widgetWindowOps) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	app := &App{
		// ctx intentionally left nil: every runtime access in the exercised
		// paths routes through the ops seam, and a nil ctx keeps
		// runtime.EventsEmit behind its guard instead of panicking on an
		// unbound context.
		widgetWindowOps:     ops,
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	if ops.read == nil {
		ops.read = func() (WidgetWindowState, bool) {
			// The current window geometry always matches the active style, as
			// in production: icon geometry when the mirror says icons (the
			// exit/save path validates against desktopIconMin*), pager
			// geometry otherwise (the switch path reloads it within the pager
			// bounds validation).
			if app.widgetStyle == "icons" {
				return WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 10, Y: 10}, false
			}
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 10, Y: 10}, false
		}
	}
	if ops.normalize == nil {
		ops.normalize = func(state WidgetWindowState) (WidgetWindowState, error) { return state, nil }
	}
	if ops.applyWidget == nil {
		ops.applyWidget = func(WidgetWindowState, bool, bool) error { return nil }
	}
	if ops.restoreMain == nil {
		ops.restoreMain = func(DesktopWindowState, bool) error { return nil }
	}
	if err := saveWidgetWindowState(WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 120, Y: 80}); err != nil {
		t.Fatal(err)
	}
	if err := saveDesktopIconWindowState(WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 120, Y: 80}); err != nil {
		t.Fatal(err)
	}
	return app
}

// blockConfigWrite makes the next user-config persist fail the same way the
// always-on-top toggle tests do: the config directory is replaced by a regular
// file so the atomic save cannot MkdirAll its parent.
func blockConfigWrite(t *testing.T) {
	t.Helper()
	configPath := config.UserConfigPath()
	configDir := filepath.Dir(configPath)
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(configDir); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.WriteFile(configDir, []byte("occupied"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// persistedWidgetStyleRaw returns the raw persisted desktop.widget_style field
// (not the icons-only getter), so tests can tell whether the setter actually
// wrote the config.
func persistedWidgetStyleRaw(t *testing.T) string {
	t.Helper()
	return config.LoadForEdit(config.UserConfigPath()).Desktop.WidgetStyle
}

// TestSetWidgetStyleActiveSwitchAppliesGeometryAndPersists: an active native
// switch runs the real switchDesktopWidgetStyleLocked path — it reads the
// persisted always-on-top flag, applies the target geometry once, updates the
// runtime mirror, and persists the style under the same widgetMu lock.
func TestSetWidgetStyleActiveSwitchAppliesGeometryAndPersists(t *testing.T) {
	var applies []widgetApplyRecord
	reads := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			reads++
			// Pager-legal geometry: the switch saves and reloads this size
			// (the runtime mirror is still the legacy pager style).
			return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: 10, Y: 10}, false
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	// Legacy runtime mirror: a pre-icons build could leave widgetStyle as the
	// old pager value; the setter must switch it to icons in one step.
	app.widgetMode = true
	app.widgetStyle = "pager"

	if err := app.SetDesktopWidgetStyle("icons"); err != nil {
		t.Fatalf("SetDesktopWidgetStyle(icons): %v", err)
	}
	if len(applies) != 1 {
		t.Fatalf("geometry applies = %d, want exactly the switch (no rollback)", len(applies))
	}
	if !applies[0].icons {
		t.Fatalf("switch to icons applied pager geometry (icons=%v)", applies[0].icons)
	}
	if !applies[0].alwaysOnTop {
		t.Fatal("the switch must carry the persisted always-on-top flag")
	}
	if app.widgetStyle != "icons" {
		t.Fatalf("runtime mirror = %q, want icons", app.widgetStyle)
	}
	if got := persistedWidgetStyleRaw(t); got != "icons" {
		t.Fatalf("persisted style = %q, want icons", got)
	}
	if reads == 0 {
		t.Fatal("the switch never read the current window geometry")
	}
}

// TestSetWidgetStylePersistFailureRollsBackRuntime: when the persist fails the
// setter restores the runtime to the previous style under the same widgetMu
// boundary, so the window and the stored preference never diverge.
func TestSetWidgetStylePersistFailureRollsBackRuntime(t *testing.T) {
	var applies []widgetApplyRecord
	ops := &widgetWindowOps{
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.widgetMode = true
	app.widgetStyle = "pager"

	blockConfigWrite(t)
	if err := app.SetDesktopWidgetStyle("icons"); err == nil {
		t.Fatal("SetDesktopWidgetStyle succeeded with a blocked config write")
	}
	if len(applies) != 2 {
		t.Fatalf("geometry applies = %d, want switch then rollback (2)", len(applies))
	}
	if !applies[0].icons || applies[1].icons {
		t.Fatalf("apply order wrong: first icons=%v (switch), second icons=%v (rollback to pager)", applies[0].icons, applies[1].icons)
	}
	if app.widgetStyle != "pager" {
		t.Fatalf("runtime mirror = %q, want the pre-switch pager restored", app.widgetStyle)
	}
	if got := persistedWidgetStyleRaw(t); got == "icons" {
		t.Fatal("a failed persist must not write the new style")
	}
}

// TestSetWidgetStylePersistAndRollbackFailureReturnsBoth: when the persist and
// the runtime rollback both fail, the caller receives both errors.
func TestSetWidgetStylePersistAndRollbackFailureReturnsBoth(t *testing.T) {
	rollbackErr := errors.New("rollback to pager geometry failed")
	var applies []widgetApplyRecord
	ops := &widgetWindowOps{
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			if !icons {
				return rollbackErr
			}
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.widgetMode = true
	app.widgetStyle = "pager"

	blockConfigWrite(t)
	err := app.SetDesktopWidgetStyle("icons")
	if err == nil {
		t.Fatal("SetDesktopWidgetStyle succeeded with a blocked config write")
	}
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("error must surface the rollback failure: %v", err)
	}
	// Sequence: the icons switch, the failed pager rollback, then the
	// switch-internal recovery that puts the window back on the icons surface.
	if len(applies) != 3 {
		t.Fatalf("geometry applies = %d, want switch, failed pager rollback, and icons recovery (3)", len(applies))
	}
	if !applies[0].icons || applies[1].icons || !applies[2].icons {
		t.Fatalf("apply order wrong: want icons, pager, icons; got %+v", applies)
	}
	if app.widgetStyle != "icons" {
		t.Fatalf("runtime mirror = %q, want icons (the failed rollback must not flip it)", app.widgetStyle)
	}
}

// TestSetWidgetStyleInactivePersistFailureNeverTouchesWindow: an inactive
// setter (previous == "") never switched the window, so a failed persist must
// not roll anything back — and in particular must never switch an active
// window to the empty style.
func TestSetWidgetStyleInactivePersistFailureNeverTouchesWindow(t *testing.T) {
	var applies []widgetApplyRecord
	ops := &widgetWindowOps{
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.widgetMode = false

	blockConfigWrite(t)
	if err := app.SetDesktopWidgetStyle("icons"); err == nil {
		t.Fatal("SetDesktopWidgetStyle succeeded with a blocked config write")
	}
	if len(applies) != 0 {
		t.Fatalf("inactive setter applied %d geometries, want none (no rollback to an empty style)", len(applies))
	}
	if app.widgetStyle != "" {
		t.Fatalf("runtime mirror = %q, want untouched", app.widgetStyle)
	}
}

// TestSetWidgetStyleSetterSerializedAgainstEnter: the inactive setter holds
// widgetMu across its persist, so a concurrent Enter cannot read the config
// between the switch and the save and apply a stale style to the window.
func TestSetWidgetStyleSetterSerializedAgainstEnter(t *testing.T) {
	applied := make(chan widgetApplyRecord, 8)
	entered := make(chan struct{})
	release := make(chan struct{})
	ops := &widgetWindowOps{
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applied <- widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons}
			close(entered)
			<-release
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)

	enterDone := make(chan error, 1)
	go func() {
		_, err := app.transitionWidgetMode(true, func() error { return app.applyEnterWidgetMode() })
		enterDone <- err
	}()
	<-entered

	setDone := make(chan error, 1)
	go func() { setDone <- app.SetDesktopWidgetStyle("icons") }()
	select {
	case err := <-setDone:
		t.Fatalf("setter completed while the enter transition held widgetMu: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-enterDone; err != nil {
		t.Fatalf("enter transition: %v", err)
	}
	if !app.IsWidgetMode() {
		t.Fatal("enter transition did not activate widget mode")
	}
	if err := <-setDone; err != nil {
		t.Fatalf("setter after enter: %v", err)
	}
	// The setter ran after the enter transition under the same lock: the
	// runtime mirror and the persisted config both say icons, and the style
	// switch itself was a no-op (enter already applied icons).
	if app.widgetStyle != "icons" {
		t.Fatalf("runtime mirror = %q, want icons", app.widgetStyle)
	}
	if got := persistedWidgetStyleRaw(t); got != "icons" {
		t.Fatalf("persisted style = %q, want icons", got)
	}
	if len(applied) != 1 {
		t.Fatalf("geometry applies = %d, want only the enter transition (the style switch was a no-op)", len(applied))
	}
}

// TestSetWidgetStyleSetterSerializedAgainstExit: the same single widgetMu
// boundary serializes an inactive setter against a concurrent Exit, so the
// persist cannot release the lock early and race the exit's state publish.
func TestSetWidgetStyleSetterSerializedAgainstExit(t *testing.T) {
	restoreStarted := make(chan struct{})
	release := make(chan struct{})
	ops := &widgetWindowOps{
		restoreMain: func(DesktopWindowState, bool) error {
			close(restoreStarted)
			<-release
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.widgetMode = true
	app.widgetStyle = "icons"

	exitDone := make(chan error, 1)
	go func() {
		_, err := app.transitionWidgetMode(false, func() error { return app.applyExitWidgetMode() })
		exitDone <- err
	}()
	<-restoreStarted

	setDone := make(chan error, 1)
	go func() { setDone <- app.SetDesktopWidgetStyle("icons") }()
	select {
	case err := <-setDone:
		t.Fatalf("setter completed while the exit transition held widgetMu: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if err := <-exitDone; err != nil {
		t.Fatalf("exit transition: %v", err)
	}
	if app.IsWidgetMode() {
		t.Fatal("exit transition did not leave widget mode")
	}
	if err := <-setDone; err != nil {
		t.Fatalf("setter after exit: %v", err)
	}
	// The queued setter persisted after the exit under the same lock; the
	// inactive runtime and the config cannot diverge.
	if got := persistedWidgetStyleRaw(t); got != "icons" {
		t.Fatalf("persisted style = %q, want icons", got)
	}
}

// TestApplyEnterWidgetModeRealPath: the real enter orchestration reads the
// persisted preferences and geometry, applies the icons surface with the
// persisted always-on-top flag, hides the taskbar, and updates the runtime
// mirror — all inside transitionWidgetMode's widgetMu critical section.
func TestApplyEnterWidgetModeRealPath(t *testing.T) {
	var applies []widgetApplyRecord
	var taskbars []bool
	reads := 0
	ops := &widgetWindowOps{
		read: func() (WidgetWindowState, bool) {
			reads++
			return WidgetWindowState{Width: 1280, Height: 800, X: 10, Y: 10}, true
		},
		applyWidget: func(state WidgetWindowState, alwaysOnTop bool, icons bool) error {
			applies = append(applies, widgetApplyRecord{state: state, alwaysOnTop: alwaysOnTop, icons: icons})
			return nil
		},
	}
	app := widgetStyleTestApp(t, ops)
	app.widgetTaskbarToggle = func(hide bool) error {
		taskbars = append(taskbars, hide)
		return nil
	}

	changed, err := app.transitionWidgetMode(true, func() error { return app.applyEnterWidgetMode() })
	if err != nil {
		t.Fatalf("applyEnterWidgetMode: %v", err)
	}
	if !changed || !app.IsWidgetMode() {
		t.Fatalf("enter transition: changed=%v mode=%v", changed, app.IsWidgetMode())
	}
	if len(applies) != 1 || !applies[0].icons {
		t.Fatalf("enter applied %d geometries (icons=%v), want exactly the icons surface", len(applies), len(applies) > 0 && applies[0].icons)
	}
	if !applies[0].alwaysOnTop {
		t.Fatal("enter must apply the persisted always-on-top flag")
	}
	if app.widgetStyle != "icons" {
		t.Fatalf("runtime mirror = %q, want icons", app.widgetStyle)
	}
	if reads == 0 {
		t.Fatal("enter never read the current window geometry")
	}
	if !reflect.DeepEqual(taskbars, []bool{true}) {
		t.Fatalf("taskbar calls = %v, want exactly one hide", taskbars)
	}
	// The main geometry was preserved into the real state file.
	if _, ok := loadWindowState(); !ok {
		t.Fatal("enter did not preserve the main window geometry")
	}
}
