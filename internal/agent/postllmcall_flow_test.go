package agent

import (
	"context"
	"strings"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func reasoningTurn() [][]provider.Chunk {
	return [][]provider.Chunk{{
		{Type: provider.ChunkReasoning, Text: "think A "},
		{Type: provider.ChunkReasoning, Text: "think B"},
		{Type: provider.ChunkText, Text: "the answer"},
		{Type: provider.ChunkDone},
	}}
}

func recordReasoning(events *[]string) event.Sink {
	return event.FuncSink(func(e event.Event) {
		if e.Kind == event.Reasoning {
			*events = append(*events, e.Text)
		}
	})
}

func assistantReasoning(msgs []provider.Message) string {
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant {
			return m.ReasoningContent
		}
	}
	return ""
}

// deepseekProvider wraps a scriptedProvider and reports
// RequiresReasoningReplay() == true, simulating a DeepSeek thinking-mode
// provider. The agent gates warning/preservation logic on this capability.
type deepseekProvider struct {
	*scriptedProvider
}

func (d *deepseekProvider) RequiresReasoningReplay() bool { return true }

func recordWarnings(warnings *[]string) event.Sink {
	return event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice && e.Level == event.LevelWarn {
			*warnings = append(*warnings, e.Text)
		}
	})
}

// TestPostLLMCallAbsentStreamsReasoningLive is the regression guard: with no
// PostLLMCall hook, reasoning must still stream chunk-by-chunk (one Reasoning
// event per delta) so the live "thinking…" display keeps working.
func TestPostLLMCallAbsentStreamsReasoningLive(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: reasoningTurn()}
	var reasoningEvents []string
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(reasoningEvents) != 2 {
		t.Fatalf("want 2 live reasoning events (one per chunk), got %d: %v", len(reasoningEvents), reasoningEvents)
	}
	if joined := strings.Join(reasoningEvents, ""); joined != "think A think B" {
		t.Fatalf("streamed reasoning = %q, want the full chain", joined)
	}
	if got := assistantReasoning(a.session.Messages); got != "think A think B" {
		t.Fatalf("stored reasoning = %q, want the untransformed chain", got)
	}
}

// TestPostLLMCallTransformsReasoningOnce proves a configured hook suppresses the
// live stream, sees the full reasoning, and its output replaces both the single
// emitted Reasoning event and the stored reasoning_content.
func TestPostLLMCallTransformsReasoningOnce(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: reasoningTurn()}
	var reasoningEvents []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "TRANSLATED"}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(reasoningEvents) != 1 || reasoningEvents[0] != "TRANSLATED" {
		t.Fatalf("want one transformed reasoning event, got %v", reasoningEvents)
	}
	if len(h.postLLMSeen) != 1 || h.postLLMSeen[0] != "think A think B" {
		t.Fatalf("hook saw %v, want the full original reasoning once", h.postLLMSeen)
	}
	if len(h.postLLMTurns) != 1 || h.postLLMTurns[0] != 1 {
		t.Fatalf("hook turns = %v, want [1]", h.postLLMTurns)
	}
	if got := assistantReasoning(a.session.Messages); got != "TRANSLATED" {
		t.Fatalf("stored reasoning = %q, want the hook's replacement", got)
	}
}

// TestPostLLMCallConfiguredButNoReasoning makes sure a hook with an empty
// reasoning chain neither calls the hook nor emits a stray Reasoning event.
func TestPostLLMCallConfiguredButNoReasoning(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{{
		{Type: provider.ChunkText, Text: "answer, no thinking"},
		{Type: provider.ChunkDone},
	}}}
	var reasoningEvents []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "should not be used"}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(reasoningEvents) != 0 {
		t.Fatalf("no reasoning should emit no Reasoning events, got %v", reasoningEvents)
	}
	if len(h.postLLMSeen) != 0 {
		t.Fatalf("hook should not fire on empty reasoning, saw %v", h.postLLMSeen)
	}
}

// TestPostLLMCallKeepsSignedReasoningOriginal proves that when the reasoning is
// pinned by a provider signature (Anthropic extended thinking), a transform hook
// changes only the live display — the stored reasoning_content stays the original
// so the signed thinking block can be replayed verbatim on the next tool-call
// turn. Storing the transformed text under the original signature is a 400.
func TestPostLLMCallKeepsSignedReasoningOriginal(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{{
		{Type: provider.ChunkReasoning, Text: "think A "},
		{Type: provider.ChunkReasoning, Text: "think B", Signature: "sig-xyz"},
		{Type: provider.ChunkText, Text: "answer"},
		{Type: provider.ChunkDone},
	}}}
	var reasoningEvents []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "TRANSLATED"}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(reasoningEvents) != 1 || reasoningEvents[0] != "TRANSLATED" {
		t.Fatalf("want the transformed reasoning shown live, got %v", reasoningEvents)
	}
	if got := assistantReasoning(a.session.Messages); got != "think A think B" {
		t.Fatalf("stored reasoning = %q, want the original (signature pins it)", got)
	}
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleAssistant && m.ReasoningSignature != "sig-xyz" {
			t.Fatalf("stored signature = %q, want sig-xyz alongside its original text", m.ReasoningSignature)
		}
	}
}

// TestPostLLMCallKeepsOriginalReasoningOnToolCallTurn proves that when a
// provider requires reasoning replay (DeepSeek thinking mode), a PostLLMCall
// hook that transforms reasoning does not affect what is stored on tool-call
// turns — the original provider reasoning is preserved for replay while the
// transformed text is shown for display only.
func TestPostLLMCallKeepsOriginalReasoningOnToolCallTurn(t *testing.T) {
	prov := &deepseekProvider{&scriptedProvider{name: "deepseek", turns: [][]provider.Chunk{
		// Turn 1: reasoning + tool call.
		{
			{Type: provider.ChunkReasoning, Text: "think A "},
			{Type: provider.ChunkReasoning, Text: "think B"},
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "bash", Arguments: `{}`}},
			{Type: provider.ChunkDone},
		},
		// Turn 2: plain answer to finish the run.
		{
			{Type: provider.ChunkText, Text: "done"},
			{Type: provider.ChunkDone},
		},
	}}}
	var reasoningEvents []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "TRANSLATED"}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Display should show the transformed version.
	if len(reasoningEvents) != 1 || reasoningEvents[0] != "TRANSLATED" {
		t.Fatalf("want one transformed reasoning event, got %v", reasoningEvents)
	}

	// The stored reasoning on the tool-call turn must be the original.
	var found bool
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			found = true
			if m.ReasoningContent != "think A think B" {
				t.Fatalf("DeepSeek tool-call turn stored reasoning = %q, want original %q", m.ReasoningContent, "think A think B")
			}
		}
	}
	if !found {
		t.Fatal("no tool-call assistant message found in session")
	}
}

// TestPostLLMCallOrdinaryProviderKeepsTransformedReasoning is the negative
// counterpart: when the provider does NOT require reasoning replay, a
// PostLLMCall hook transforms the stored reasoning as before — no tool-call
// preservation happens.
func TestPostLLMCallOrdinaryProviderKeepsTransformedReasoning(t *testing.T) {
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			{Type: provider.ChunkReasoning, Text: "think A "},
			{Type: provider.ChunkReasoning, Text: "think B"},
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "bash", Arguments: `{}`}},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "done"},
			{Type: provider.ChunkDone},
		},
	}}
	var reasoningEvents []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "TRANSLATED"}
	a := New(prov, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordReasoning(&reasoningEvents))

	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Ordinary provider: stored reasoning should be the TRANSFORMED version.
	var found bool
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleAssistant && len(m.ToolCalls) > 0 {
			found = true
			if m.ReasoningContent != "TRANSLATED" {
				t.Fatalf("ordinary provider tool-call turn stored reasoning = %q, want transformed %q", m.ReasoningContent, "TRANSLATED")
			}
		}
	}
	if !found {
		t.Fatal("no tool-call assistant message found in session")
	}
}

// TestEmptyReasoningWarnsOnlyForReplayProvider proves requirement 5: the
// missing-reasoning warning fires only when the active provider requires
// DeepSeek-style reasoning replay. Ordinary providers with tool-call turns
// and no reasoning must not warn.
func TestEmptyReasoningWarnsOnlyForReplayProvider(t *testing.T) {
	// DeepSeek-like provider: warning expected.
	dsProv := &deepseekProvider{&scriptedProvider{name: "deepseek", turns: [][]provider.Chunk{
		{
			// No reasoning chunks at all.
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "bash", Arguments: `{}`}},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "done"},
			{Type: provider.ChunkDone},
		},
	}}}
	var dsWarnings []string
	h := &stubHooks{hasPostLLM: true, postLLMOut: "ok"}
	a := New(dsProv, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordWarnings(&dsWarnings))
	if err := a.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(dsWarnings) == 0 {
		t.Fatal("DeepSeek-like provider with empty reasoning on tool-call turn must emit a warning")
	}

	// Ordinary provider: no warning expected.
	ordProv := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "bash", Arguments: `{}`}},
			{Type: provider.ChunkDone},
		},
		{
			{Type: provider.ChunkText, Text: "done"},
			{Type: provider.ChunkDone},
		},
	}}
	var ordWarnings []string
	a2 := New(ordProv, tool.NewRegistry(), NewSession(""), Options{Hooks: h}, recordWarnings(&ordWarnings))
	if err := a2.Run(context.Background(), "go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(ordWarnings) != 0 {
		t.Fatalf("ordinary provider must not emit reasoning-replay warning, got %v", ordWarnings)
	}
}
