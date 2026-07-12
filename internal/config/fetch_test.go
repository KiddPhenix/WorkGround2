package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildModelFetchURLs(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		override string
		want     []string
	}{
		{
			name: "root endpoint keeps legacy models path first",
			base: "https://api.deepseek.com",
			want: []string{"https://api.deepseek.com/models", "https://api.deepseek.com/v1/models"},
		},
		{
			name: "versioned endpoint uses models under version",
			base: "https://api.example.com/v1",
			want: []string{"https://api.example.com/v1/models"},
		},
		{
			name: "non-v1 version keeps v1 fallback",
			base: "https://open.bigmodel.cn/api/coding/paas/v4",
			want: []string{
				"https://open.bigmodel.cn/api/coding/paas/v4/models",
				"https://open.bigmodel.cn/api/coding/paas/v4/v1/models",
			},
		},
		{
			name: "anthropic compatible subpath adds root candidates",
			base: "https://api.deepseek.com/anthropic",
			want: []string{
				"https://api.deepseek.com/anthropic/models",
				"https://api.deepseek.com/anthropic/v1/models",
				"https://api.deepseek.com/models",
				"https://api.deepseek.com/v1/models",
			},
		},
		{
			name:     "override wins",
			base:     "https://api.deepseek.com",
			override: "https://api.deepseek.com/custom/models",
			want:     []string{"https://api.deepseek.com/custom/models"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildModelFetchURLs(tt.base, tt.override)
			if err != nil {
				t.Fatalf("BuildModelFetchURLs: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestProviderFetchModelsFallsBackToV1Models(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "model-b"}, {"id": "model-a"}},
		})
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "test", BaseURL: srv.URL, APIKeyEnv: "FETCH_MODELS_TEST_KEY", resolvedAPIKey: "test-key"}
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 || got[0] != "model-a" || got[1] != "model-b" {
		t.Fatalf("got %v, want [model-a model-b]", got)
	}
}

func TestProviderFetchModelsContinuesAfterRootAuthFailure(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/models":
			http.Error(w, `{"error":"wrong endpoint"}`, http.StatusUnauthorized)
		case "/v1/models":
			if r.Header.Get("Authorization") != "Bearer test-key" {
				http.Error(w, "bad key", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "model-a"}},
			})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "test", BaseURL: srv.URL, APIKeyEnv: "FETCH_MODELS_TEST_KEY", resolvedAPIKey: "test-key"}
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 1 || got[0] != "model-a" {
		t.Fatalf("got %v, want [model-a]", got)
	}
	if len(paths) != 2 || paths[0] != "/models" || paths[1] != "/v1/models" {
		t.Fatalf("paths = %v, want [/models /v1/models]", paths)
	}
}

func TestProviderFetchModelsUsesSetupProbeEnv(t *testing.T) {
	const key = "FETCH_MODELS_PROBE_KEY"
	t.Setenv(key, "probe-key")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer probe-key" {
			http.Error(w, "bad key", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "probe-model"}},
		})
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "probe", BaseURL: srv.URL, APIKeyEnv: key}
	p.ResolveAPIKeyFromProcessEnvForProbe()
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 1 || got[0] != "probe-model" {
		t.Fatalf("models = %v, want [probe-model]", got)
	}
}

func TestProviderFetchModelsAllowsNoAuthEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			http.Error(w, "unexpected auth header", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "local-b"}, {"id": "local-a"}},
		})
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "local", BaseURL: srv.URL}
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels no-auth: %v", err)
	}
	if len(got) != 2 || got[0] != "local-a" || got[1] != "local-b" {
		t.Fatalf("got %v, want [local-a local-b]", got)
	}
}

func TestProviderFetchModelsUsesAnthropicModelEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("path = %q, want /v1/models", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "anthropic-key" {
			t.Fatalf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", r.Header.Get("anthropic-version"))
		}
		if r.Header.Get("Authorization") != "" {
			t.Fatalf("unexpected Authorization header %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "claude-sonnet"}, {"id": "claude-haiku"}},
		})
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "anthropic", Kind: "anthropic", BaseURL: srv.URL, APIKeyEnv: "ANTHROPIC_API_KEY", resolvedAPIKey: "anthropic-key"}
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 || got[0] != "claude-haiku" || got[1] != "claude-sonnet" {
		t.Fatalf("models = %v", got)
	}
}

func TestProviderFetchModelsUsesGoogleOpenAICompatibilityEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/openai/models" {
			t.Fatalf("path = %q, want /v1beta/openai/models", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer google-key" {
			t.Fatalf("Authorization = %q", r.Header.Get("Authorization"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{{"id": "gemini-pro"}, {"id": "gemini-flash"}},
		})
	}))
	defer srv.Close()

	p := ProviderEntry{Name: "google", Kind: "google", BaseURL: srv.URL, APIKeyEnv: "GEMINI_API_KEY", resolvedAPIKey: "google-key"}
	got, err := p.FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got) != 2 || got[0] != "gemini-flash" || got[1] != "gemini-pro" {
		t.Fatalf("models = %v", got)
	}
}
