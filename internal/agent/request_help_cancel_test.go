package agent

import (
	"context"
	"errors"
	"testing"

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// TestRequestHelpStopsIteratingCandidatesAfterCancel verifies that a stopped
// parent turn aborts the capability-assist candidate loop before resolving or
// launching any subagent: the provider resolver must not be consulted, and the
// returned error is the cancellation itself — not a misleading wrapped
// "all candidates failed" report from burning the remaining candidates.
func TestRequestHelpStopsIteratingCandidatesAfterCancel(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers,
		config.ProviderEntry{Name: "ws1", Kind: "openai", BaseURL: "https://api.example.com", Model: "m1", Capabilities: []string{"web_search"}},
		config.ProviderEntry{Name: "ws2", Kind: "openai", BaseURL: "https://api.example.com", Model: "m2", Capabilities: []string{"web_search"}},
	)
	cfg.Agent.AssistModels = map[string][]string{"web_search": {"ws1/m1", "ws2/m2"}}

	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		return nil, nil, 0, nil
	}
	th := newTestRequestHelpTool(t, cfg, nil, tool.NewRegistry(), resolve)

	ctx, cancel := context.WithCancel(testRequestHelpContext())
	cancel()

	_, err := th.Execute(ctx, []byte(`{"capability":"web_search","prompt":"search for foo"}`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute error = %v, want context.Canceled", err)
	}
	if resolveCount != 0 {
		t.Fatalf("resolveProvider called %d times after cancel, want 0 (no candidate iteration after Stop)", resolveCount)
	}
}
