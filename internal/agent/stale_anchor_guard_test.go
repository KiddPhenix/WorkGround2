package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func TestEditFileAllowsConsecutiveSameFileWrites(t *testing.T) {
	// Consecutive edit_file calls on the same file are allowed without an
	// explicit read_file between them: edit_file re-reads the file before
	// applying the edit (it verifies old_string uniqueness), so the anchor
	// is always fresh.
	var editCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})

	args := `{"path":"src/map.html","old_string":"before","new_string":"after"}`
	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", args),
			toolCallChunk("c2", "edit_file", args),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "edit the map"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&editCalls); got != 2 {
		t.Fatalf("edit_file executed %d times, want both calls (2)", got)
	}
}

func TestAnchorEditAllowedAfterFreshRead(t *testing.T) {
	var editCalls int32
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})

	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", `{"path":"src/map.html","old_string":"before","new_string":"after"}`),
			toolCallChunk("c2", "read_file", `{"path":"src/map.html"}`),
			toolCallChunk("c3", "edit_file", `{"path":"src/map.html","old_string":"current","new_string":"final"}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "edit the map with a read between edits"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&readCalls); got != 1 {
		t.Fatalf("read_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&editCalls); got != 2 {
		t.Fatalf("edit_file executed %d times, want 2 after fresh read", got)
	}
	if last := lastToolResult(a.session, "edit_file"); strings.Contains(last, "[fresh read required]") {
		t.Fatalf("fresh read should allow the second edit, got %q", last)
	}
}

func TestEditFileAllowsWindowedReadAfterSameFileWrite(t *testing.T) {
	// A windowed read is not a full refresh, but consecutive edit_file calls
	// on the same file skip the fresh-read guard anyway (edit_file re-reads
	// the file internally before each apply). So the second edit_file is
	// allowed even with only a windowed read in between.
	var editCalls int32
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})

	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", `{"path":"src/map.html","old_string":"before","new_string":"after"}`),
			toolCallChunk("c2", "read_file", `{"path":"src/map.html","offset":400,"limit":20}`),
			toolCallChunk("c3", "edit_file", `{"path":"src/map.html","old_string":"current","new_string":"final"}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "edit the map with a narrow read between edits"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&readCalls); got != 1 {
		t.Fatalf("read_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&editCalls); got != 2 {
		t.Fatalf("edit_file executed %d times, want 2 (consecutive edit_file skip the guard)", got)
	}
}

func TestMultiEditAllowedAfterSameTurnWrite(t *testing.T) {
	var editCalls int32
	var multiCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})
	reg.Add(fakeTool{name: "multi_edit", readOnly: false, calls: &multiCalls})

	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", `{"path":"src/map.html","old_string":"before","new_string":"after"}`),
			toolCallChunk("c2", "multi_edit", `{"path":"src/map.html","edits":[{"old_string":"current","new_string":"final"}]}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "edit the map atomically"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&editCalls); got != 1 {
		t.Fatalf("edit_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&multiCalls); got != 1 {
		t.Fatalf("multi_edit executed %d times, want 1", got)
	}
}

func TestDeleteRangeRequiresReadAfterSameTurnWrite(t *testing.T) {
	// delete_range still requires a refresh read after a same-file write
	// because line ranges shift after edits.
	var editCalls int32
	var deleteCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})
	reg.Add(fakeTool{name: "delete_range", readOnly: false, calls: &deleteCalls})

	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", `{"path":"src/map.html","old_string":"before","new_string":"after"}`),
			toolCallChunk("c2", "delete_range", `{"path":"src/map.html","start_anchor":"L1","end_anchor":"L3"}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "delete a range after editing"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&editCalls); got != 1 {
		t.Fatalf("edit_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&deleteCalls); got != 0 {
		t.Fatalf("delete_range executed %d times, want 0 (blocked by fresh-read guard)", got)
	}
	last := lastToolResult(a.session, "delete_range")
	if !strings.Contains(last, "[fresh read required]") {
		t.Fatalf("delete_range should be blocked after same-file write, got %q", last)
	}
}

func TestDeleteRangeAllowedAfterFreshRead(t *testing.T) {
	var editCalls int32
	var readCalls int32
	var deleteCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})
	reg.Add(fakeTool{name: "delete_range", readOnly: false, calls: &deleteCalls})

	prov := &scriptedProvider{name: "p", turns: [][]provider.Chunk{
		{
			toolCallChunk("c1", "edit_file", `{"path":"src/map.html","old_string":"before","new_string":"after"}`),
			toolCallChunk("c2", "read_file", `{"path":"src/map.html"}`),
			toolCallChunk("c3", "delete_range", `{"path":"src/map.html","start_anchor":"L1","end_anchor":"L3"}`),
			{Type: provider.ChunkDone},
		},
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "delete a range after editing and reading"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&editCalls); got != 1 {
		t.Fatalf("edit_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&readCalls); got != 1 {
		t.Fatalf("read_file executed %d times, want 1", got)
	}
	if got := atomic.LoadInt32(&deleteCalls); got != 1 {
		t.Fatalf("delete_range executed %d times, want 1 after fresh read", got)
	}
}
