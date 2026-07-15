package config

import "testing"

func TestGeminiVisionInferred(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash",
		"gemini-2.5-flash-preview-05-20", "gemini-1.5-pro-latest",
		"gemini-2.5-flash-image", "gemini-2.0-flash-preview-image-generation",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if !e.HasCapability(CapVision) {
			t.Fatalf("gemini model %q should have vision", model)
		}
	}
}

func TestGeminiWebSearchInferred(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash",
		"gemini-2.5-flash-preview-05-20", "gemini-1.5-pro-latest",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if !e.HasCapability(CapWebSearch) {
			t.Fatalf("gemini chat model %q should have web_search (Google Search grounding)", model)
		}
	}
}

func TestGeminiImageModelsGetImageGeneration(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-flash-image", "gemini-2.0-flash-preview-image-generation",
		"imagen-3.0-generate-002", "imagen-4.0-generate-001",
		"nano-banana-250", "nano-banana",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if !e.HasCapability(CapImageGeneration) {
			t.Fatalf("image model %q should have image_generation", model)
		}
	}
}

func TestGeminiTextModelsDoNotGetImageGeneration(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.0-flash",
		"gemini-2.5-flash-preview-05-20", "gemini-1.5-pro-latest",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if e.HasCapability(CapImageGeneration) {
			t.Fatalf("text model %q must not get image_generation", model)
		}
	}
}

func TestGeminiImageModelsDoNotGetWebSearch(t *testing.T) {
	for _, model := range []string{
		"gemini-2.5-flash-image", "imagen-3.0-generate-002", "nano-banana-250",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if e.HasCapability(CapWebSearch) {
			t.Fatalf("image model %q must not get web_search", model)
		}
	}
}

func TestGemini3ImageModelsGetWebSearch(t *testing.T) {
	for _, model := range []string{
		"gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview", "gemini-3.1-flash-lite-image",
	} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if !e.HasCapability(CapWebSearch) {
			t.Fatalf("current image model %q should have web_search grounding", model)
		}
	}
}

func TestNonGeminiModelsDoNotInferWebSearchOrImage(t *testing.T) {
	for _, model := range []string{"gpt-5", "gpt-4o", "claude-sonnet-4-20250514", "deepseek-chat"} {
		e := &ProviderEntry{Kind: "openai", Model: model}
		if e.HasCapability(CapWebSearch) {
			t.Fatalf("non-gemini model %q must not infer web_search", model)
		}
		if e.HasCapability(CapImageGeneration) {
			t.Fatalf("non-gemini model %q must not infer image_generation", model)
		}
	}
}

func TestExplicitCapabilitiesSuppressGeminiInference(t *testing.T) {
	// Explicit empty list suppresses all inference.
	e := &ProviderEntry{Kind: "openai", Model: "gemini-2.5-pro", Capabilities: []string{}}
	if e.HasCapability(CapVision) || e.HasCapability(CapWebSearch) || e.HasCapability(CapImageGeneration) {
		t.Fatal("explicit empty Capabilities should suppress all inference")
	}
	// Explicit partial list returns only what's declared.
	e2 := &ProviderEntry{Kind: "openai", Model: "gemini-2.5-pro", Capabilities: []string{"vision"}}
	if !e2.HasCapability(CapVision) {
		t.Fatal("explicit vision should be detected")
	}
	if e2.HasCapability(CapWebSearch) {
		t.Fatal("explicit list without web_search should suppress web_search inference")
	}
}

func TestIsGeminiImageModel(t *testing.T) {
	cases := map[string]bool{
		"gemini-2.5-flash-image":                    true,
		"gemini-2.0-flash-preview-image-generation": true,
		"imagen-3.0-generate-002":                   true,
		"imagen-4.0":                                true,
		"nano-banana-250":                           true,
		"nano-banana":                               true,
		"gemini-2.5-pro":                            false,
		"gemini-2.5-flash":                          false,
		"gemini-image-understanding":                false,
		"gpt-4o":                                    false,
		"deepseek-chat":                             false,
	}
	for model, want := range cases {
		if got := isGeminiImageModel(model); got != want {
			t.Fatalf("isGeminiImageModel(%q) = %v, want %v", model, got, want)
		}
	}
}
