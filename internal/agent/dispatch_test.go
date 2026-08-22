package agent

import (
	"context"
	"strings"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// TestEarlyToolDispatch proves a ChunkToolCallStart surfaces a ToolDispatch
// immediately (Partial, name only) so the card shows while the arguments are
// still streaming, and that a second, full dispatch (with args) follows once the
// call completes — the fix for "the edit_file card only appears after everything
// is written".
func TestEarlyToolDispatch(t *testing.T) {
	prov := &mockProvider{name: "p", chunks: []provider.Chunk{
		{Type: provider.ChunkToolCallStart, ToolCall: &provider.ToolCall{ID: "c1", Name: "read_file"}},
		{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c1", Name: "read_file", Arguments: `{"path":"/x"}`}},
		{Type: provider.ChunkDone},
	}}
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})

	var got []event.Event
	sink := event.FuncSink(func(e event.Event) { got = append(got, e) })
	a := New(prov, reg, NewSession(""), Options{MaxSteps: 1}, sink)
	_ = a.Run(context.Background(), "go") // errors at the 1-step cap; we only want the events

	var partial, full int
	for _, e := range got {
		if e.Kind != event.ToolDispatch {
			continue
		}
		if e.Tool.Partial {
			partial++
			if e.Tool.Name != "read_file" {
				t.Errorf("partial dispatch name = %q, want read_file", e.Tool.Name)
			}
		} else {
			full++
			if e.Tool.Args == "" {
				t.Errorf("full dispatch should carry args, got none")
			}
		}
	}
	if partial != 2 {
		t.Errorf("want 2 partial (early) dispatches (original + grace-round replay), got %d", partial)
	}
	if full != 1 {
		t.Errorf("want 1 full dispatch, got %d", full)
	}
	var found bool
	for _, msg := range a.Session().Snapshot() {
		if strings.Contains(msg.Content, "Tool-round limit reached") {
			found = true
			if msg.Origin != provider.MessageOriginHost {
				t.Fatalf("tool-round nudge origin = %q, want host", msg.Origin)
			}
		}
	}
	if !found {
		t.Fatal("tool-round nudge missing from session")
	}
}
