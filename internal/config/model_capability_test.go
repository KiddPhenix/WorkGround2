package config

import "testing"

func TestDeepSeekV4FlashVisionExpBuiltinCapabilities(t *testing.T) {
	entry := &ProviderEntry{Kind: "openai", Model: "deepseek-v4-flash-vision-exp"}

	for _, capability := range []ModelCapability{CapVision, CapReasoning} {
		if !entry.HasCapability(capability) {
			t.Errorf("deepseek-v4-flash-vision-exp missing built-in capability %q", capability)
		}
	}
	for _, capability := range []ModelCapability{CapWebSearch, CapImageGeneration} {
		if entry.HasCapability(capability) {
			t.Errorf("deepseek-v4-flash-vision-exp unexpectedly has capability %q", capability)
		}
	}
}
