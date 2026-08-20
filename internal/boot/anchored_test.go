package boot

import (
	"testing"

	"workground2/internal/config"
)

// TestIsDeepSeekProvider exercises the provider-family gate that decides
// whether the two-phase bootstrap applies: DeepSeek-family openai entries
// only, with the same rule the openai provider uses (explicit deepseek
// reasoning_protocol, or unset protocol against a deepseek base URL).
func TestIsDeepSeekProvider(t *testing.T) {
	cases := []struct {
		name  string
		entry *config.ProviderEntry
		want  bool
	}{
		{
			name:  "builtin deepseek pro",
			entry: &config.ProviderEntry{Kind: "openai", BaseURL: "https://api.deepseek.com", Model: "deepseek-v4-pro"},
			want:  true,
		},
		{
			name:  "deepseek protocol explicit over arbitrary base",
			entry: &config.ProviderEntry{Kind: "openai", BaseURL: "https://gateway.example.com", ReasoningProtocol: "deepseek"},
			want:  true,
		},
		{
			name:  "plain openai",
			entry: &config.ProviderEntry{Kind: "openai", BaseURL: "https://api.openai.com"},
			want:  false,
		},
		{
			name:  "anthropic kind",
			entry: &config.ProviderEntry{Kind: "anthropic", BaseURL: "https://api.deepseek.com"},
			want:  false,
		},
		{
			name:  "nil entry",
			entry: nil,
			want:  false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isDeepSeekProvider(tc.entry); got != tc.want {
				t.Fatalf("isDeepSeekProvider(%+v) = %v, want %v", tc.entry, got, tc.want)
			}
		})
	}
}
