package agent

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// makeReadOnlyTurns returns n turns where each turn calls read_file once,
// followed by a final turn that returns a text answer.
func makeReadOnlyTurns(n int) [][]provider.Chunk {
	turns := make([][]provider.Chunk, 0, n+1)
	for i := 0; i < n; i++ {
		turns = append(turns, []provider.Chunk{
			toolCallChunk("c1", "read_file", `{"path":"f.txt"}`),
			{Type: provider.ChunkDone},
		})
	}
	turns = append(turns, []provider.Chunk{
		{Type: provider.ChunkText, Text: "done"},
		{Type: provider.ChunkDone},
	})
	return turns
}

func TestReadOnlyStreakBelowThresholdNoNudge(t *testing.T) {
	// 7 rounds of read_file: streak becomes 7, below threshold of 8.
	// No convergence nudge should appear in the session.
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})

	prov := &scriptedProvider{name: "p", turns: makeReadOnlyTurns(7)}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&readCalls); got != 7 {
		t.Fatalf("read_file executed %d times, want 7", got)
	}
	if sessionHasUserMessageContaining(a.session, "purely reading context") {
		t.Fatal("nudge should not appear before threshold")
	}
}

func TestReadOnlyStreakAtThresholdNudgeOnce(t *testing.T) {
	// 8 rounds of read_file: streak reaches 8, which equals the threshold.
	// The convergence nudge is injected after round 8, before the final turn.
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})

	prov := &scriptedProvider{name: "p", turns: makeReadOnlyTurns(8)}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&readCalls); got != 8 {
		t.Fatalf("read_file executed %d times, want 8", got)
	}
	if !sessionHasUserMessageContaining(a.session, "purely reading context") {
		t.Fatal("nudge should appear at threshold (8 rounds)")
	}
	// Count occurrences: should be exactly one
	count := 0
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "purely reading context") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("nudge count = %d, want exactly 1", count)
	}
	for i, m := range a.session.Messages {
		if m.Role != provider.RoleUser || !strings.Contains(m.Content, "purely reading context") {
			continue
		}
		if i == 0 || a.session.Messages[i-1].Role != provider.RoleTool {
			t.Fatalf("nudge at message %d must follow the paired tool result", i)
		}
	}
}

func TestReadOnlyStreakNudgeOnlyOncePerStreak(t *testing.T) {
	// 10 rounds of read_file: streak hits 8 → nudge, then 9 and 10
	// should not produce additional nudges because readOnlyNudgeSent is true.
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})

	prov := &scriptedProvider{name: "p", turns: makeReadOnlyTurns(10)}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&readCalls); got != 10 {
		t.Fatalf("read_file executed %d times, want 10", got)
	}
	count := 0
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "purely reading context") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("nudge count = %d, want exactly 1 (only once per streak)", count)
	}
}

func TestReadOnlyStreakResetsOnProgress(t *testing.T) {
	// 8 reads trigger once, a successful edit rearms the guard, and another
	// 8 reads trigger a second nudge.
	var readCalls int32
	var editCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})
	reg.Add(fakeTool{name: "edit_file", readOnly: false, calls: &editCalls})

	turns := [][]provider.Chunk{
		// First read-only streak.
		{toolCallChunk("c1", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c2", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c3", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c4", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c5", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c6", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c7", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c8", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		// 1 progress round (edit_file resets streak)
		{toolCallChunk("c9", "edit_file", `{"path":"f.txt","old_string":"a","new_string":"b"}`), {Type: provider.ChunkDone}},
		// 8 read-only rounds (new streak → nudge at the 8th)
		{toolCallChunk("c10", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c11", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c12", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c13", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c14", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c15", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c16", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c17", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		// final answer
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}

	prov := &scriptedProvider{name: "p", turns: turns}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "read then edit then read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := atomic.LoadInt32(&editCalls); got != 1 {
		t.Fatalf("edit_file executed %d times, want 1 (progress round)", got)
	}
	count := 0
	for _, m := range a.session.Messages {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, "purely reading context") {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("nudge count = %d, want 2 after successful progress rearms the guard", count)
	}
}

func TestReadOnlyStreakFailedWriterDoesNotReset(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true})
	reg.Add(fakeTool{name: "edit_file", readOnly: false, err: errors.New("write failed")})
	turns := makeReadOnlyTurns(7)[:7]
	turns = append(turns,
		[]provider.Chunk{toolCallChunk("write", "edit_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		[]provider.Chunk{toolCallChunk("read", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		[]provider.Chunk{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	)
	a := New(&scriptedProvider{name: "p", turns: turns}, reg, NewSession(""), Options{}, event.Discard)
	if err := a.Run(context.Background(), "read, fail to write, then read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !sessionHasUserMessageContaining(a.session, "purely reading context") {
		t.Fatal("failed writer should not clear accumulated read-only streak")
	}
}

func TestReadOnlyStreakBashResetsStreak(t *testing.T) {
	// bash has ReadOnly=false, so every bash call resets the streak.
	var readCalls int32
	reg := tool.NewRegistry()
	reg.Add(fakeTool{name: "read_file", readOnly: true, calls: &readCalls})
	reg.Add(fakeTool{name: "bash", readOnly: false})

	turns := [][]provider.Chunk{
		// 4 read-only rounds
		{toolCallChunk("c1", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c2", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c3", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c4", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		// 1 bash round → resets streak
		{toolCallChunk("c5", "bash", `{"command":"go test ./..."}`), {Type: provider.ChunkDone}},
		// 4 more read-only rounds → streak=4 (< threshold), no nudge
		{toolCallChunk("c6", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c7", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c8", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		{toolCallChunk("c9", "read_file", `{"path":"f.txt"}`), {Type: provider.ChunkDone}},
		// final answer
		{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}},
	}

	prov := &scriptedProvider{name: "p", turns: turns}
	a := New(prov, reg, NewSession(""), Options{}, event.Discard)

	if err := a.Run(context.Background(), "read bash read"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sessionHasUserMessageContaining(a.session, "purely reading context") {
		t.Fatal("nudge should not appear when bash resets streak (only 4 after reset < 8)")
	}
}
