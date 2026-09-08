package config

import (
	"reflect"
	"testing"
)

// deepSeekV41TrialFlashModel is defined in load.go next to
// deepSeekVisionExpModel; these tests pin its catalog behaviour end to end.

func TestDeepSeekV41TrialFlashMergeAddsToOfficialProvider(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:      "deepseek",
		Kind:      "openai",
		BaseURL:   "https://api.deepseek.com",
		Models:    []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"},
		Default:   "deepseek-v4-flash",
		APIKeyEnv: "DEEPSEEK_API_KEY",
	}}}
	normalizeOfficialNewModels(c)
	p, ok := c.Provider("deepseek")
	if !ok {
		t.Fatal("deepseek provider missing")
	}
	if !p.HasModel(deepSeekV41TrialFlashModel) {
		t.Fatalf("models = %v, want %q merged in", p.ModelList(), deepSeekV41TrialFlashModel)
	}
	if p.Default != "deepseek-v4-flash" {
		t.Fatalf("default = %q, want preserved deepseek-v4-flash", p.Default)
	}
	// Existing models stay, vision metadata auto-populated only when unset.
	if !reflect.DeepEqual(p.VisionModels, []string{deepSeekVisionExpModel, deepSeekV41TrialFlashModel}) {
		t.Fatalf("vision_models = %v, want vision-exp + trial", p.VisionModels)
	}

	// Idempotent: a second pass must not duplicate entries.
	before := append([]string(nil), p.ModelList()...)
	normalizeOfficialNewModels(c)
	if !stringSlicesEqual(before, p.ModelList()) {
		t.Fatalf("repeated merge produced duplicates: %v -> %v", before, p.ModelList())
	}
}

func TestDeepSeekV41TrialFlashSkipsLegacyAndCustomEntries(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{
		{Name: "deepseek-flash", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-flash", APIKeyEnv: "DEEPSEEK_API_KEY"},
		{Name: "deepseek-pro", Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro", APIKeyEnv: "DEEPSEEK_API_KEY"},
		{Name: "deepseek-proxy", Kind: "openai", BaseURL: "https://proxy.example.com/v1", Models: []string{"deepseek-v4-flash"}, Default: "deepseek-v4-flash"},
	}}
	normalizeOfficialNewModels(c)
	for _, name := range []string{"deepseek-flash", "deepseek-pro", "deepseek-proxy"} {
		p, ok := c.Provider(name)
		if !ok {
			t.Fatalf("%s provider missing", name)
		}
		if p.HasModel(deepSeekV41TrialFlashModel) {
			t.Fatalf("%s models = %v, want no trial SKU injection", name, p.ModelList())
		}
	}
}

func TestDeepSeekV41TrialFlashPreservesExplicitVisionSelection(t *testing.T) {
	c := &Config{Providers: []ProviderEntry{{
		Name:         "deepseek",
		Kind:         "openai",
		BaseURL:      "https://api.deepseek.com",
		Models:       []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"},
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

func TestDeepSeekV41TrialFlashBuiltinCapabilities(t *testing.T) {
	entry := &ProviderEntry{Kind: "openai", Model: deepSeekV41TrialFlashModel}
	for _, capability := range []ModelCapability{CapVision, CapReasoning} {
		if !entry.HasCapability(capability) {
			t.Errorf("%s missing built-in capability %q", deepSeekV41TrialFlashModel, capability)
		}
	}
	for _, capability := range []ModelCapability{CapWebSearch, CapImageGeneration} {
		if entry.HasCapability(capability) {
			t.Errorf("%s unexpectedly has capability %q", deepSeekV41TrialFlashModel, capability)
		}
	}
	if !IsLikelyChatModel(deepSeekV41TrialFlashModel) {
		t.Errorf("IsLikelyChatModel(%s) = false, want true (selectable chat model)", deepSeekV41TrialFlashModel)
	}
}

func TestDeepSeekV41TrialFlashEffortScale(t *testing.T) {
	entry := &ProviderEntry{Kind: "openai", BaseURL: "https://api.deepseek.com", Model: deepSeekV41TrialFlashModel}
	cap := EffortCapabilityForEntry(entry)
	wantLevels := []string{"auto", "low", "high", "max"}
	if !cap.Supported || !reflect.DeepEqual(cap.Levels, wantLevels) {
		t.Fatalf("effort capability = %+v, want supported levels %v", cap, wantLevels)
	}
	if cap.Default != "high" {
		t.Fatalf("default effort = %q, want high (Flash default depth)", cap.Default)
	}
	for _, raw := range []string{"low", "high", "max"} {
		got, err := NormalizeEffort(entry, raw)
		if err != nil || got != raw {
			t.Errorf("NormalizeEffort(%q) = %q, %v; want %q", raw, got, err, raw)
		}
	}
}

func TestDeepSeekV41TrialFlashPriceMatchesFlash(t *testing.T) {
	flash := deepSeekV4FlashPrice()
	trial := deepSeekV4Prices()[deepSeekV41TrialFlashModel]
	if trial == nil || !samePricing(trial, flash) {
		t.Fatalf("CNY price for %s = %+v, want identical to deepseek-v4-flash %+v", deepSeekV41TrialFlashModel, trial, flash)
	}
	if got := DeepSeekV4PricesForLanguage("zh")[deepSeekV41TrialFlashModel]; got == nil || !samePricing(got, flash) {
		t.Fatalf("template price for %s = %+v, want flash RMB pricing", deepSeekV41TrialFlashModel, got)
	}
	usd := deepSeekV4PricesUSD()[deepSeekV41TrialFlashModel]
	if usd == nil || !samePricing(usd, deepSeekV4FlashPriceUSD()) {
		t.Fatalf("USD price for %s = %+v, want identical to deepseek-v4-flash USD", deepSeekV41TrialFlashModel, usd)
	}
	// The stored default must be recognised as official so upgrade refresh
	// keeps it in sync instead of treating it as a custom price.
	if !isKnownDeepSeekOfficialPricing(deepSeekV41TrialFlashModel, flash) {
		t.Fatalf("flash-priced %s must count as known official pricing", deepSeekV41TrialFlashModel)
	}
	if got := deepSeekV4PriceForModel("zh", deepSeekV41TrialFlashModel); got == nil || !samePricing(got, flash) {
		t.Fatalf("deepSeekV4PriceForModel(%s) = %+v, want flash price", deepSeekV41TrialFlashModel, got)
	}
}
