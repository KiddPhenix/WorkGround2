package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// testRequestHelpContext returns a ctx for unit tests of request_help.
// It carries a parent session so transcript persistence works.
func testRequestHelpContext() context.Context {
	return WithParentSession(context.Background(), "test-session")
}

// newTestRequestHelpTool builds a RequestHelpTool wired with test defaults.
func newTestRequestHelpTool(t *testing.T, cfg *config.Config, prov provider.Provider, parentReg *tool.Registry, resolveProvider func(string, string) (provider.Provider, *provider.Pricing, int, error)) *RequestHelpTool {
	t.Helper()
	if cfg == nil {
		cfg = config.Default()
	}
	return NewRequestHelpTool(
		cfg, parentReg, "current-model",
		resolveProvider,
		20, 0, 0, 0, 0, 0, 0, 0,
		nil, 0,
		2,
	).WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "base-effort")
}

// successChunks is a short text-turn that qualifies as a final answer.
var successChunks = []provider.Chunk{
	{Type: provider.ChunkText, Text: "here is the search result: https://example.com/source"},
	{Type: provider.ChunkDone},
}

func TestRequestHelpWebSearchRequiresSourceURL(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	prov := &mockProvider{name: "ws1/m1", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "trust me, I searched"}, {Type: provider.ChunkDone},
	}}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := newTestRequestHelpTool(t, cfg, prov, tool.NewRegistry(), resolve)
	_, err := helper.Execute(testRequestHelpContext(), []byte(`{"capability":"web_search","prompt":"search"}`))
	if err == nil || !strings.Contains(err.Error(), "no http(s) source URL") {
		t.Fatalf("expected source validation error, got %v", err)
	}
}

// TestRequestHelpFirstFailsSecondSucceeds verifies that when the first
// candidate's provider construction fails, the tool tries the second.
func TestRequestHelpFirstFailsSecondSucceeds(t *testing.T) {
	cfg := config.Default()
	// Add a provider that actually has web_search.
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "ws1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"web_search"},
	}, config.ProviderEntry{
		Name:         "ws2",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m2",
		Capabilities: []string{"web_search"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"web_search": {"ws1/m1", "ws2/m2"},
	}

	goodProv := &mockProvider{name: "ws2/m2", chunks: successChunks}
	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		if ref == "ws1/m1" {
			return nil, nil, 0, fmt.Errorf("ws1 is down")
		}
		return goodProv, nil, 0, nil
	}

	parentReg := tool.NewRegistry()
	th := newTestRequestHelpTool(t, cfg, nil, parentReg, resolve)

	out, err := th.Execute(testRequestHelpContext(), []byte(`{"capability":"web_search","prompt":"search for foo"}`))
	if err != nil {
		t.Fatalf("Execute should succeed: %v", err)
	}
	if !strings.Contains(out, "here is the search result") {
		t.Fatalf("unexpected output: %q", out)
	}
	if resolveCount != 2 {
		t.Fatalf("should have tried 2 candidates, got %d", resolveCount)
	}
	if !strings.Contains(out, "previous_failures:") || !strings.Contains(out, "ws1/m1") {
		t.Fatalf("success output should expose the failed attempt: %s", out)
	}
}

// TestRequestHelpAllFailuresReportAttemptedRefs verifies error messages
// include capability and attempted candidate refs.
func TestRequestHelpAllFailuresReportAttemptedRefs(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "ws1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"web_search"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"web_search": {"ws1/m1"},
	}

	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		return nil, nil, 0, fmt.Errorf("always down")
	}

	parentReg := tool.NewRegistry()
	th := newTestRequestHelpTool(t, cfg, nil, parentReg, resolve)

	_, err := th.Execute(testRequestHelpContext(), []byte(`{"capability":"web_search","prompt":"search for foo"}`))
	if err == nil {
		t.Fatal("Execute should fail")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, `web_search`) {
		t.Fatalf("error should mention capability: %s", errStr)
	}
	if !strings.Contains(errStr, `ws1/m1`) {
		t.Fatalf("error should mention attempted ref: %s", errStr)
	}
	if !strings.Contains(errStr, `all 1 candidate`) {
		t.Fatalf("error should mention candidate count: %s", errStr)
	}
}

// TestRequestHelpImageRunFailsNoSecondCandidate verifies that when
// image_generation subagent runs but fails, a second candidate is NOT tried.
func TestRequestHelpImageRunFailsNoSecondCandidate(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "img1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"image_generation"},
	}, config.ProviderEntry{
		Name:         "img2",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m2",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"img1/m1", "img2/m2"},
	}

	// First provider streams an error — subagent ran but failed.
	badProv := &mockProvider{name: "img1/m1", chunks: []provider.Chunk{
		{Type: provider.ChunkError, Err: fmt.Errorf("content policy violation")},
		{Type: provider.ChunkDone},
	}}
	_ = &mockProvider{name: "img2/m2", chunks: successChunks} // unused — should not be reached

	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		if ref == "img1/m1" {
			return badProv, nil, 0, nil
		}
		return &mockProvider{name: "img2/m2", chunks: successChunks}, nil, 0, nil
	}

	parentReg := tool.NewRegistry()
	th := newTestRequestHelpTool(t, cfg, nil, parentReg, resolve)

	_, err := th.Execute(testRequestHelpContext(), []byte(`{"capability":"image_generation","prompt":"generate a cat"}`))
	if err == nil {
		t.Fatal("Execute should fail")
	}
	if resolveCount != 1 {
		t.Fatalf("image_generation should NOT try second candidate after run failure, got %d resolves", resolveCount)
	}
	errStr := err.Error()
	if !strings.Contains(errStr, `image_generation`) {
		t.Fatalf("error should mention capability: %s", errStr)
	}
}

// TestRequestHelpImageProvFailTriesSecond verifies that image_generation
// provider-resolution failure DOES try the second candidate before artifact
// validation rejects a text-only result.
func TestRequestHelpImageProvFailTriesSecond(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "img1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"image_generation"},
	}, config.ProviderEntry{
		Name:         "img2",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m2",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"img1/m1", "img2/m2"},
	}

	goodProv := &mockProvider{name: "img2/m2", chunks: successChunks}
	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		if ref == "img1/m1" {
			return nil, nil, 0, fmt.Errorf("img1 provider down")
		}
		return goodProv, nil, 0, nil
	}

	parentReg := tool.NewRegistry()
	th := newTestRequestHelpTool(t, cfg, nil, parentReg, resolve)

	_, err := th.Execute(testRequestHelpContext(), []byte(`{"capability":"image_generation","prompt":"generate a cat"}`))
	if err == nil || !strings.Contains(err.Error(), "artifact validation") {
		t.Fatalf("text-only image result should fail artifact validation: %v", err)
	}
	if resolveCount != 2 {
		t.Fatalf("should have tried 2 candidates, got %d", resolveCount)
	}
}

// TestRequestHelpDepthBoundary verifies that the request_help tool respects
// max_subagent_depth and builds registry with SubagentToolRegistryForDepth.
func TestRequestHelpDepthBoundary(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "ws1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"web_search"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"web_search": {"ws1/m1"},
	}
	cfg.Agent.MaxSubagentDepth = 1 // only one layer

	goodProv := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		return goodProv, nil, 0, nil
	}

	parentReg := tool.NewRegistry()
	th := NewRequestHelpTool(
		cfg, parentReg, "current-model",
		resolve,
		20, 0, 0, 0, 0, 0, 0, 0,
		nil, 0,
		1, // maxSubagentDepth = 1
	)

	// Running at depth 0 (root): childDepth=1 <= maxDepth=1 → should succeed.
	rootCtx := WithSubagentDepth(context.Background(), 0)

	out, err := th.Execute(rootCtx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("depth 0→1 should succeed: %v", err)
	}
	if !strings.Contains(out, "here is the search result") {
		t.Fatalf("unexpected output: %q", out)
	}

	// Running at depth 1 (inside a subagent): childDepth=2 > maxDepth=1 → should fail.
	deepCtx := WithSubagentDepth(context.Background(), 1)

	_, err = th.Execute(deepCtx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err == nil {
		t.Fatal("depth 1→2 should fail with limit reached")
	}
	if !strings.Contains(err.Error(), "depth limit") {
		t.Fatalf("error should mention depth limit: %s", err)
	}
}

// TestRequestHelpInvalidCapability verifies the enum validation.
func TestRequestHelpInvalidCapability(t *testing.T) {
	cfg := config.Default()
	th := newTestRequestHelpTool(t, cfg, nil, nil, nil)

	_, err := th.Execute(testRequestHelpContext(), []byte(`{"capability":"invalid_cap","prompt":"x"}`))
	if err == nil {
		t.Fatal("should fail on invalid capability")
	}
	if !strings.Contains(err.Error(), "unknown capability") {
		t.Fatalf("error should say unknown capability: %s", err)
	}
	if !strings.Contains(err.Error(), "web_search") {
		t.Fatalf("error should list valid capabilities: %s", err)
	}
}

func TestRequestHelpRejectsRoutingWhenCurrentModelHasCapability(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	called := false
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		called = true
		return &mockProvider{name: "unused"}, nil, 0, nil
	}
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "ws1/m1", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2)
	_, err := helper.Execute(context.Background(), []byte(`{"capability":"web_search","prompt":"search"}`))
	if err == nil || !strings.Contains(err.Error(), "already provides") {
		t.Fatalf("expected direct-handling error, got %v", err)
	}
	if called {
		t.Fatal("provider resolver should not run when current model has the capability")
	}
}

// TestRequestHelpRegistryExcludesRequestHelpAtBoundary ensures the sub-registry
// does not contain request_help when at the subagent depth boundary.
func TestRequestHelpRegistryExcludesRequestHelpAtBoundary(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "ws1",
		Kind:         "openai",
		BaseURL:      "https://api.example.com",
		Model:        "m1",
		Capabilities: []string{"web_search"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"web_search": {"ws1/m1"},
	}

	goodProv := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		return goodProv, nil, 0, nil
	}

	parentReg := tool.NewRegistry()
	th := NewRequestHelpTool(
		cfg, parentReg, "current-model",
		resolve,
		20, 0, 0, 0, 0, 0, 0, 0,
		nil, 0,
		1, // maxSubagentDepth = 1 — so depth=1 IS the boundary
	)
	// Register request_help so SubagentToolRegistryForDepth can exclude it.
	parentReg.Add(th)

	// Run at depth 0 → childDepth=1 which equals maxDepth=1 → boundary → exclude recursive tools.
	ctx := WithSubagentDepth(context.Background(), 0)
	ctx = WithParentSession(ctx, "test-session")
	out, err := th.Execute(ctx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = out

	for _, tool := range goodProv.lastReq.Tools {
		if tool.Name == "request_help" {
			t.Fatal("subagent at depth boundary should not have request_help in its tool set")
		}
	}
}

func TestRequestHelpRegistryAlwaysExcludesRequestHelp(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	prov := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	parentReg := tool.NewRegistry()
	helper := NewRequestHelpTool(cfg, parentReg, "current-model", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2)
	parentReg.Add(helper)
	if _, err := helper.Execute(WithSubagentDepth(context.Background(), 0), []byte(`{"capability":"web_search","prompt":"search"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, schema := range prov.lastReq.Tools {
		if schema.Name == "request_help" {
			t.Fatal("capability helper must never receive request_help")
		}
	}
}

func TestRequestHelpPersistsCompletedTranscript(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	prov := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	sessionDir := t.TempDir()
	store := NewSubagentStore(filepath.Join(sessionDir, "subagents"))
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "current-model", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(store, t.TempDir(), "base-model", "")
	out, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	ref := subagentRefFromOutput(t, out)
	meta, err := store.LoadMeta(ref)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Status != SubagentCompleted || meta.Model != "ws1/m1" || meta.Kind != "request_help" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestRequestHelpPersistsFailedTranscript(t *testing.T) {
	cfg := requestHelpConfig("image_generation", "img1", "m1")
	prov := &mockProvider{name: "img1/m1", chunks: []provider.Chunk{{Type: provider.ChunkError, Err: fmt.Errorf("failed")}}}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	sessionDir := t.TempDir()
	store := NewSubagentStore(filepath.Join(sessionDir, "subagents"))
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "current-model", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(store, t.TempDir(), "base-model", "")
	if _, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"image_generation","prompt":"draw"}`)); err == nil {
		t.Fatal("Execute should fail")
	}
	artifacts, err := ListSubagentsByParent(sessionDir, "parent")
	if err != nil {
		t.Fatalf("ListSubagentsByParent: %v", err)
	}
	if len(artifacts) != 1 || artifacts[0].Meta.Status != SubagentFailed || artifacts[0].Meta.Model != "img1/m1" {
		t.Fatalf("artifacts = %+v", artifacts)
	}
}

func requestHelpConfig(capability, name, model string) *config.Config {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: name, Kind: "openai", BaseURL: "https://api.example.com", Model: model,
		Capabilities: []string{capability},
	})
	cfg.Agent.AssistModels = map[string][]string{capability: {name + "/" + model}}
	return cfg
}
