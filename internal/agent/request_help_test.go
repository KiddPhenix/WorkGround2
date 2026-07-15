package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

// TestRequestHelpProgressFirstCandidate verifies structured progress is emitted
// for the first candidate attempt.
func TestRequestHelpProgressFirstCandidate(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	prov := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := newTestRequestHelpTool(t, cfg, prov, tool.NewRegistry(), resolve)

	var chunks []string
	ctx := tool.WithProgress(testRequestHelpContext(), func(c string) {
		chunks = append(chunks, c)
	})
	_, err := helper.Execute(ctx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 progress chunks (attempting + succeeded), got %d: %v", len(chunks), chunks)
	}
	first, ok := parseRhProgress(chunks[0])
	if !ok {
		t.Fatalf("first chunk is not rh progress: %q", chunks[0])
	}
	if first.State != "attempting" {
		t.Fatalf("first chunk state should be attempting, got %q", first.State)
	}
	if first.Capability != "web_search" {
		t.Fatalf("capability: %q", first.Capability)
	}
	if first.Attempt != 1 || first.Total != 1 {
		t.Fatalf("attempt/total: %d/%d", first.Attempt, first.Total)
	}
	last, ok := parseRhProgress(chunks[len(chunks)-1])
	if !ok {
		t.Fatalf("last chunk is not rh progress: %q", chunks[len(chunks)-1])
	}
	if last.State != "completed" {
		t.Fatalf("last chunk state should be completed, got %q", last.State)
	}
}

// TestRequestHelpProgressFailureSwitch verifies that when a candidate
// fails and another is available, both "failed" and the next "attempting"
// progress chunks are emitted.
func TestRequestHelpProgressFailureSwitch(t *testing.T) {
	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "ws1", Kind: "openai", BaseURL: "https://api.example.com", Model: "m1",
		Capabilities: []string{"web_search"},
	}, config.ProviderEntry{
		Name: "ws2", Kind: "openai", BaseURL: "https://api.example.com", Model: "m2",
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
			return nil, nil, 0, fmt.Errorf("ws1 down")
		}
		return goodProv, nil, 0, nil
	}
	helper := newTestRequestHelpTool(t, cfg, nil, tool.NewRegistry(), resolve)

	var chunks []string
	ctx := tool.WithProgress(testRequestHelpContext(), func(c string) {
		chunks = append(chunks, c)
	})
	_, err := helper.Execute(ctx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	states := collectRhProgressStates(chunks)
	if len(states) < 4 {
		t.Fatalf("expected at least 4 progress events (attempting failed attempting succeeded), got %d: %v", len(states), states)
	}
	if states[0] != "attempting" {
		t.Fatalf("state[0] = %q, want attempting", states[0])
	}
	if states[1] != "candidate_failed" {
		t.Fatalf("state[1] = %q, want candidate_failed", states[1])
	}
	if states[2] != "attempting" {
		t.Fatalf("state[2] = %q, want attempting", states[2])
	}
	if states[3] != "completed" {
		t.Fatalf("state[3] = %q, want completed", states[3])
	}
}

// TestRequestHelpProgressSuccess verifies that a single-candidate success
// emits both attempting and succeeded.
func TestRequestHelpProgressSuccess(t *testing.T) {
	cfg := requestHelpConfig("web_search", "ws1", "m1")
	prov := &mockProvider{name: "ws1/m1", chunks: successChunks}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := newTestRequestHelpTool(t, cfg, prov, tool.NewRegistry(), resolve)

	var chunks []string
	ctx := tool.WithProgress(testRequestHelpContext(), func(c string) {
		chunks = append(chunks, c)
	})
	out, err := helper.Execute(ctx, []byte(`{"capability":"web_search","prompt":"search"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	_ = out

	states := collectRhProgressStates(chunks)
	if len(states) != 2 {
		t.Fatalf("expected 2 progress events, got %d: %v", len(states), states)
	}
	if states[0] != "attempting" || states[1] != "completed" {
		t.Fatalf("expected attempting→completed, got %v", states)
	}
}

// TestRhProgressPrefixIsolation verifies that the progress prefix cannot be
// confused with ordinary tool output.
func TestRhProgressPrefixIsolation(t *testing.T) {
	if strings.HasPrefix("normal bash output\n", RequestHelpProgressPrefix) {
		t.Fatal("ordinary text should not match rh progress prefix")
	}
	if strings.HasPrefix("{\"version\":1", RequestHelpProgressPrefix) {
		t.Fatal("JSON should not match rh progress prefix")
	}
	var got string
	ctx := tool.WithProgress(context.Background(), func(chunk string) { got = chunk })
	emitRequestHelpProgress(ctx, requestHelpProgress{Version: 1, State: "attempting", RequestID: "r1", Capability: config.CapWebSearch, FromModel: "gpt", Model: "gpt-search", Attempt: 1, Total: 3})
	if !strings.HasPrefix(got, RequestHelpProgressPrefix) {
		t.Fatal("emitRequestHelpProgress must use the dedicated prefix")
	}
}

type rhProgress struct {
	State      string `json:"state"`
	Capability string `json:"capability"`
	FromModel  string `json:"from_model"`
	Model      string `json:"model"`
	Attempt    int    `json:"attempt"`
	Total      int    `json:"total"`
	RequestID  string `json:"request_id"`
}

func parseRhProgress(chunk string) (rhProgress, bool) {
	if !strings.HasPrefix(chunk, RequestHelpProgressPrefix) {
		return rhProgress{}, false
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(chunk, RequestHelpProgressPrefix), "\n")
	var p rhProgress
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		return rhProgress{}, false
	}
	return p, true
}

func collectRhProgressStates(chunks []string) []string {
	var states []string
	for _, c := range chunks {
		if p, ok := parseRhProgress(c); ok {
			states = append(states, p.State)
		}
	}
	return states
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

// writeMinimalPNG writes a 1×1 pixel PNG to path and returns the path.
func writeMinimalPNG(t *testing.T, path string) string {
	t.Helper()
	// Minimal valid 1×1 blue PNG (IHDR+IDAT+IEND).
	png := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk length 13
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // width=1 height=1
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53, // bit depth=8 color=2
		0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // CRC + IDAT chunk
		0x54, 0x08, 0xD7, 0x63, 0x68, 0x00, 0x00, 0x00,
		0x02, 0x00, 0x01, 0xE5, 0x27, 0xDE, 0xFC, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, 0x44, 0xAE, // IEND chunk
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, png, 0644); err != nil {
		t.Fatalf("writeMinimalPNG: %v", err)
	}
	return path
}

// setupCodexArtifactEnv creates a temp CODEX_HOME with generated_images/
// containing a minimal PNG, and sets CODEX_HOME in the environment.
// Returns the png path and a cleanup function.
func setupCodexArtifactEnv(t *testing.T) (pngPath string, cleanup func()) {
	t.Helper()
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	if err := os.MkdirAll(genDir, 0755); err != nil {
		t.Fatalf("mkdir generated_images: %v", err)
	}
	pngPath = filepath.Join(genDir, "output.png")
	writeMinimalPNG(t, pngPath)

	oldCodexHome := os.Getenv("CODEX_HOME")
	os.Setenv("CODEX_HOME", codexHome)
	cleanup = func() {
		if oldCodexHome == "" {
			os.Unsetenv("CODEX_HOME")
		} else {
			os.Setenv("CODEX_HOME", oldCodexHome)
		}
	}
	return pngPath, cleanup
}

// TestRequestHelpImageRecoveryFromChunkError verifies that when
// image_generation's CLI provider reports a valid PNG via ArtifactCollector
// but the text stream returns a ChunkError, Execute recovers: returns
// success with the artifact, a recovery diagnostic, and the transcript
// stored as completed. Only one candidate is resolved.
func TestRequestHelpImageRecoveryFromChunkError(t *testing.T) {
	pngPath, cleanup := setupCodexArtifactEnv(t)
	defer cleanup()

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "img1", Kind: "openai", BaseURL: "https://api.example.com", Model: "m1",
		Capabilities: []string{"image_generation"},
	}, config.ProviderEntry{
		Name: "img2", Kind: "openai", BaseURL: "https://api.example.com", Model: "m2",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"img1/m1", "img2/m2"},
	}

	prov := &mockProvider{
		name:          "img1/m1",
		artifactPaths: []string{pngPath},
		chunks: []provider.Chunk{
			{Type: provider.ChunkError, Err: fmt.Errorf("text stream timeout")},
			{Type: provider.ChunkDone},
		},
	}
	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		return prov, nil, 0, nil
	}

	sessionDir := t.TempDir()
	store := NewSubagentStore(filepath.Join(sessionDir, "subagents"))
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "current-model", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(store, t.TempDir(), "base-model", "")

	out, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"image_generation","prompt":"draw a cat"}`))
	if err != nil {
		t.Fatalf("Execute should succeed via artifact recovery: %v", err)
	}
	if resolveCount != 1 {
		t.Fatalf("should only resolve 1 candidate, got %d", resolveCount)
	}
	if !strings.Contains(out, `"path"`) || !strings.Contains(out, filepath.Base(pngPath)) {
		t.Fatalf("output should contain artifact with path, got: %s", out)
	}
	if !strings.Contains(out, "Recovery diagnostic") || !strings.Contains(out, "text stream timeout") {
		t.Fatalf("output should contain recovery diagnostic with original error, got: %s", out)
	}

	// Transcript must be completed.
	ref := subagentRefFromOutput(t, out)
	meta, err := store.LoadMeta(ref)
	if err != nil {
		t.Fatalf("LoadMeta: %v", err)
	}
	if meta.Status != SubagentCompleted {
		t.Fatalf("transcript status = %q, want completed", meta.Status)
	}
}

// TestRequestHelpImageNoRecoveryWithInvalidArtifact verifies that when
// image_generation fails and the artifact collector has an invalid or
// nonexistent artifact, Execute fails normally and only one candidate is tried.
func TestRequestHelpImageNoRecoveryWithInvalidArtifact(t *testing.T) {
	_, cleanup := setupCodexArtifactEnv(t)
	defer cleanup()

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name: "img1", Kind: "openai", BaseURL: "https://api.example.com", Model: "m1",
		Capabilities: []string{"image_generation"},
	}, config.ProviderEntry{
		Name: "img2", Kind: "openai", BaseURL: "https://api.example.com", Model: "m2",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"img1/m1", "img2/m2"},
	}

	// The artifact path does not exist on disk — validateCodexArtifact fails.
	prov := &mockProvider{
		name:          "img1/m1",
		artifactPaths: []string{`Z:\nonexistent\fake.png`},
		chunks: []provider.Chunk{
			{Type: provider.ChunkError, Err: fmt.Errorf("text stream timeout")},
			{Type: provider.ChunkDone},
		},
	}
	resolveCount := 0
	resolve := func(ref, effort string) (provider.Provider, *provider.Pricing, int, error) {
		resolveCount++
		return prov, nil, 0, nil
	}

	helper := newTestRequestHelpTool(t, cfg, nil, tool.NewRegistry(), resolve)
	_, err := helper.Execute(testRequestHelpContext(), []byte(`{"capability":"image_generation","prompt":"draw a cat"}`))
	if err == nil {
		t.Fatal("Execute should fail — artifact is invalid")
	}
	if resolveCount != 1 {
		t.Fatalf("should only resolve 1 candidate, got %d", resolveCount)
	}
	if !strings.Contains(err.Error(), "all 1 candidate(s) failed") {
		t.Fatalf("error should report all candidates failed, got: %v", err)
	}
}
