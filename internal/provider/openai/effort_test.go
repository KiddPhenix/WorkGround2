package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workground2/internal/provider"
)

func newClient(t *testing.T, baseURL, effort string) *client {
	return newClientWithModel(t, baseURL, "m", effort)
}

func newClientWithModel(t *testing.T, baseURL, model, effort string) *client {
	t.Helper()
	extra := map[string]any{}
	if effort != "" {
		extra["effort"] = effort
	}
	p, err := New(provider.Config{Name: "p", BaseURL: baseURL, Model: model, APIKey: "k", Extra: extra})
	if err != nil {
		t.Fatalf("New(%q, model=%q, effort=%q): %v", baseURL, model, effort, err)
	}
	return p.(*client)
}

func TestEffortNormalization(t *testing.T) {
	const mimo = "https://api.xiaomimimo.com/v1"
	const deepseek = "https://api.deepseek.com/v1"

	tests := []struct {
		base, effort, want string
	}{
		{mimo, "max", "high"}, // DeepSeek-ism clamped to the OpenAI ceiling — MiMo 400s on "max"
		{mimo, "high", "high"},
		{mimo, "medium", "medium"},
		{mimo, "low", "low"},
		{mimo, "MAX", "high"}, // case-insensitive
		{mimo, "auto", ""},    // UI/config auto means omit provider-specific effort
		{mimo, "", ""},        // unset stays omitted
		{deepseek, "max", "max"},
		{deepseek, "high", "high"},
		{deepseek, "auto", "high"},
		{deepseek, "", "high"}, // DeepSeek default depth
	}
	for _, tc := range tests {
		if got := newClient(t, tc.base, tc.effort).effort; got != tc.want {
			t.Errorf("base=%s effort=%q: got %q, want %q", tc.base, tc.effort, got, tc.want)
		}
	}
}

func TestEffortInvalidRejected(t *testing.T) {
	_, err := New(provider.Config{
		Name: "p", BaseURL: "https://api.xiaomimimo.com/v1", Model: "m", APIKey: "k",
		Extra: map[string]any{"effort": "turbo"},
	})
	if err == nil || !strings.Contains(err.Error(), "low, medium, or high") {
		t.Fatalf("expected a low/medium/high validation error, got: %v", err)
	}
}

func TestDeepSeekFlashEffortLowAccepted(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"

	tests := []struct {
		effort, want string
	}{
		{"low", "low"},
		{"high", "high"},
		{"max", "max"},
		{"auto", "high"},
		{"", "high"},
		{"off", "high"},
	}
	for _, tc := range tests {
		c := newClientWithModel(t, deepseek, "deepseek-v4-flash", tc.effort)
		if c.effort != tc.want {
			t.Errorf("Flash effort=%q: got %q, want %q", tc.effort, c.effort, tc.want)
		}
	}

	// Flash rejects invalid efforts.
	if _, err := New(provider.Config{
		Name: "flash", BaseURL: deepseek, Model: "deepseek-v4-flash", APIKey: "k",
		Extra: map[string]any{"effort": "medium"},
	}); err == nil || !strings.Contains(err.Error(), "low, high, or max") {
		t.Fatalf("Flash should reject medium effort, got: %v", err)
	}
}

func TestDeepSeekFlashVisionExpEffortLowAccepted(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"

	// Flash vision variants keep Flash's effort scale (low|high|max).
	for _, effort := range []string{"low", "high", "max"} {
		c := newClientWithModel(t, deepseek, "deepseek-v4-flash-vision-exp", effort)
		if c.effort != effort {
			t.Errorf("Flash-vision effort=%q: got %q, want %q", effort, c.effort, effort)
		}
	}

	// Flash-vision still rejects non-Flash efforts like medium.
	if _, err := New(provider.Config{
		Name: "flash-vision", BaseURL: deepseek, Model: "deepseek-v4-flash-vision-exp", APIKey: "k",
		Extra: map[string]any{"effort": "medium"},
	}); err == nil || !strings.Contains(err.Error(), "low, high, or max") {
		t.Fatalf("Flash-vision should reject medium effort, got: %v", err)
	}
}

func TestDeepSeekProRejectsLowEffort(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"

	// Pro accepts high and max.
	for _, effort := range []string{"high", "max"} {
		c := newClientWithModel(t, deepseek, "deepseek-v4-pro", effort)
		if c.effort != effort {
			t.Errorf("Pro effort=%q: got %q, want %q", effort, c.effort, effort)
		}
	}

	// Pro rejects low.
	if _, err := New(provider.Config{
		Name: "pro", BaseURL: deepseek, Model: "deepseek-v4-pro", APIKey: "k",
		Extra: map[string]any{"effort": "low"},
	}); err == nil || !strings.Contains(err.Error(), "high or max") {
		t.Fatalf("Pro should reject low effort, got: %v", err)
	}
}

func TestDeepSeekUnknownModelRejectsLowEffort(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"

	// Unknown DeepSeek model (not in known model list) follows the conservative path.
	if _, err := New(provider.Config{
		Name: "unknown", BaseURL: deepseek, Model: "deepseek-v4", APIKey: "k",
		Extra: map[string]any{"effort": "low"},
	}); err == nil || !strings.Contains(err.Error(), "high or max") {
		t.Fatalf("unknown DeepSeek model should reject low effort, got: %v", err)
	}
}

func TestDeepSeekV41TrialFlashEffortLowAccepted(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"
	const trial = "deepseek-v4.1-flash-expires-on-0910"

	// The official-API trial SKU keeps Flash's effort scale (low|high|max).
	for _, effort := range []string{"low", "high", "max"} {
		c := newClientWithModel(t, deepseek, trial, effort)
		if c.effort != effort {
			t.Errorf("trial effort=%q: got %q, want %q", effort, c.effort, effort)
		}
	}
	// auto/unset fall back to the DeepSeek default depth.
	for _, effort := range []string{"auto", ""} {
		c := newClientWithModel(t, deepseek, trial, effort)
		if c.effort != "high" {
			t.Errorf("trial effort=%q: got %q, want high", effort, c.effort)
		}
	}
	// Flash-family validation still rejects non-Flash efforts.
	if _, err := New(provider.Config{
		Name: "trial", BaseURL: deepseek, Model: trial, APIKey: "k",
		Extra: map[string]any{"effort": "medium"},
	}); err == nil || !strings.Contains(err.Error(), "low, high, or max") {
		t.Fatalf("trial Flash should reject medium effort, got: %v", err)
	}
}

func TestDeepSeekV41NonFlashSiblingRejectsLowEffort(t *testing.T) {
	const deepseek = "https://api.deepseek.com/v1"

	// A v4.1 name that is not Flash must not be misjudged as low-capable.
	if _, err := New(provider.Config{
		Name: "pro", BaseURL: deepseek, Model: "deepseek-v4.1-pro", APIKey: "k",
		Extra: map[string]any{"effort": "low"},
	}); err == nil || !strings.Contains(err.Error(), "high or max") {
		t.Fatalf("deepseek-v4.1-pro should reject low effort, got: %v", err)
	}
}

func TestDeepSeekV41TrialWireRequestSendsExactModelAndEffort(t *testing.T) {
	const trial = "deepseek-v4.1-flash-expires-on-0910"
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	p, err := New(provider.Config{
		Name: "trial", BaseURL: "https://api.deepseek.com", Model: trial, APIKey: "k",
		Extra: map[string]any{"chat_url": srv.URL, "effort": "low"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range ch {
		if chunk.Type == provider.ChunkError {
			t.Fatalf("stream error: %v", chunk.Err)
		}
	}

	if model, _ := got["model"].(string); model != trial {
		t.Fatalf("wire model = %q, want exact %q", model, trial)
	}
	if effort, _ := got["reasoning_effort"].(string); effort != "low" {
		t.Fatalf("wire reasoning_effort = %q, want low", effort)
	}
	thinking, ok := got["thinking"].(map[string]any)
	if !ok || thinking["type"] != "enabled" {
		t.Fatalf("wire thinking = %#v, want type=enabled (DeepSeek CoT)", got["thinking"])
	}
}

func TestReasoningProtocolOverridesEndpointHeuristic(t *testing.T) {
	p, err := New(provider.Config{
		Name:    "deepseek-proxy",
		BaseURL: "https://proxy.example.com/v1",
		Model:   "deepseek-v4-flash",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "deepseek"},
	})
	if err != nil {
		t.Fatalf("New deepseek protocol: %v", err)
	}
	c := p.(*client)
	if !c.deepseek || c.effort != "high" {
		t.Fatalf("deepseek=%v effort=%q, want true/high", c.deepseek, c.effort)
	}

	p, err = New(provider.Config{
		Name:    "deepseek-direct",
		BaseURL: "https://api.deepseek.com/v1",
		Model:   "deepseek-v4-flash",
		APIKey:  "k",
		Extra:   map[string]any{"reasoning_protocol": "none", "effort": "max"},
	})
	if err != nil {
		t.Fatalf("New none protocol: %v", err)
	}
	c = p.(*client)
	if c.deepseek || c.effort != "" {
		t.Fatalf("deepseek=%v effort=%q, want false/empty", c.deepseek, c.effort)
	}
}
