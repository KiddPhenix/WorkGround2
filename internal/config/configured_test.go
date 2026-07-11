package config

import "testing"

// TestProviderConfigured verifies Configured tracks whether the provider can be
// selected. Providers with no api_key_env are explicit no-auth providers; if an
// env var is configured, it must resolve to a non-empty value.
func TestProviderConfigured(t *testing.T) {
	cases := []struct {
		name string
		p    ProviderEntry
		want bool
	}{
		{"key set", ProviderEntry{APIKeyEnv: "WorkGround2_TEST_KEY", resolvedAPIKey: "secret"}, true},
		{"key env empty", ProviderEntry{APIKeyEnv: "WorkGround2_TEST_EMPTY"}, false},
		{"key env unset", ProviderEntry{APIKeyEnv: "WorkGround2_TEST_MISSING"}, false},
		{"loopback key env unset", ProviderEntry{BaseURL: "http://127.0.0.1:23333/v1", APIKeyEnv: "WorkGround2_TEST_MISSING"}, true},
		{"official endpoint without key env", ProviderEntry{BaseURL: "https://api.deepseek.com"}, false},
		{"local cli without key env", ProviderEntry{Kind: "cli", Command: "local-llm", Model: "default"}, true},
		{"no api_key_env", ProviderEntry{}, true},
	}
	for _, c := range cases {
		if got := c.p.Configured(); got != c.want {
			t.Errorf("%s: Configured() = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestValidateAllowsLocalCLIProvider(t *testing.T) {
	c := &Config{
		DefaultModel: "local-cli/default",
		Providers: []ProviderEntry{{
			Name:     "local-cli",
			Kind:     "cli",
			Command:  "local-llm",
			Model:    "default",
			Protocol: "json",
		}},
	}

	if err := c.Validate("local-cli/default"); err != nil {
		t.Fatalf("Validate local CLI provider: %v", err)
	}
}

func TestValidateAllowsNoAuthProvider(t *testing.T) {
	c := &Config{
		DefaultModel: "local/model-a",
		Providers: []ProviderEntry{{
			Name:    "local",
			Kind:    "openai",
			BaseURL: "http://127.0.0.1:23333/v1",
			Models:  []string{"model-a", "model-b"},
			Default: "model-a",
		}},
	}

	if err := c.Validate("local/model-b"); err != nil {
		t.Fatalf("Validate no-auth local provider: %v", err)
	}

	c.Providers[0].APIKeyEnv = "LOCAL_API_KEY"
	if err := c.Validate("local/model-b"); err != nil {
		t.Fatalf("Validate loopback local provider with missing key env: %v", err)
	}
}
