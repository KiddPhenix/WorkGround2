package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func TestStopCapabilityNeverTargetsReusedCallID(t *testing.T) {
	a := New(testutil.NewMock("m"), tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	first, finish := a.beginToolCall(context.Background(), "reused", "wait")
	oldID := stopIDFor(t, a, "reused")
	if finish() {
		t.Fatal("normal completion reported stopped")
	}
	if first.Err() == nil {
		t.Fatal("completed child context leaked")
	}
	second, finishSecond := a.beginToolCall(context.Background(), "reused", "wait")
	defer finishSecond()
	newID := stopIDFor(t, a, "reused")
	if oldID == newID {
		t.Fatal("stop capability reused")
	}
	for _, id := range []string{oldID, "reused"} {
		if got := a.StopToolCall(id); got.Status != ToolStopFinished {
			t.Fatalf("stale stop = %+v", got)
		}
		if second.Err() != nil {
			t.Fatal("stale request cancelled new invocation")
		}
	}
	if got := a.StopToolCall(newID); got.Status != ToolStopAccepted {
		t.Fatalf("live stop = %+v", got)
	}
	if second.Err() == nil || !finishSecond() {
		t.Fatal("live cancellation was not settled")
	}
}

func TestProgressUsageReconcilesFramesAndPollingIsReadOnly(t *testing.T) {
	a := New(testutil.NewMock("m"), tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	empty, _ := json.Marshal(a.ProgressSnapshot())
	if strings.Contains(string(empty), "inputTokens") || strings.Contains(string(empty), "0001-") {
		t.Fatalf("unknown values fabricated: %s", empty)
	}
	record := a.progressUsage("executor")
	record(&provider.Usage{PromptTokens: 100, CompletionTokens: 3}, false)
	snap := a.ProgressSnapshot()
	if snap.UsageMode != "stream_reported" || *snap.OutputTokens != 3 {
		t.Fatalf("live usage = %+v", snap)
	}
	for i := 0; i < 5; i++ {
		if a.ProgressSnapshot().ProgressSeq != snap.ProgressSeq {
			t.Fatal("polling advanced progress")
		}
	}
	record(&provider.Usage{PromptTokens: 100, CompletionTokens: 8}, false)
	record(&provider.Usage{PromptTokens: 100, CompletionTokens: 8}, true)
	snap = a.ProgressSnapshot()
	if *snap.InputTokens != 100 || *snap.OutputTokens != 8 || snap.UsageMode != "response_complete" {
		t.Fatalf("double-counted request = %+v", snap)
	}
	next := a.progressUsage("executor")
	next(&provider.Usage{PromptTokens: 50, CompletionTokens: 6}, false)
	next(&provider.Usage{PromptTokens: 49, CompletionTokens: 5}, true)
	snap = a.ProgressSnapshot()
	if *snap.InputTokens != 149 || *snap.OutputTokens != 13 {
		t.Fatalf("final reconciliation = %+v", snap)
	}
	a.progressUsage("executor")(nil, true)
	snap = a.ProgressSnapshot()
	if snap.UsageMode != "unavailable" || *snap.OutputTokens != 13 {
		t.Fatal("unreported request corrupted cumulative counts")
	}
	a.SetSession(NewSession("new"))
	record(&provider.Usage{PromptTokens: 999}, true)
	if a.ProgressSnapshot().UsageSeen {
		t.Fatal("late usage leaked into replacement session")
	}
}

type progressProvider struct {
	provider.Provider
	chunks chan provider.Chunk
}

func (p *progressProvider) Stream(context.Context, provider.Request) (<-chan provider.Chunk, error) {
	return p.chunks, nil
}

func TestModelStreamUpdatesProgressBeforeCompletion(t *testing.T) {
	p := &progressProvider{Provider: testutil.NewMock("live"), chunks: make(chan provider.Chunk, 8)}
	a := New(p, tool.NewRegistry(), NewSession("sys"), Options{}, event.Discard)
	done := make(chan error, 1)
	go func() { _, _, _, _, _, _, _, err := a.stream(context.Background(), 0); done <- err }()
	await := func(check func(ProgressSnapshot) bool) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if check(a.ProgressSnapshot()) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatalf("progress not delivered: %+v", a.ProgressSnapshot())
	}
	p.chunks <- provider.Chunk{Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 40, CompletionTokens: 2}}
	await(func(s ProgressSnapshot) bool { return s.OutputTokens != nil && *s.OutputTokens == 2 })
	before := a.ProgressSnapshot().ProgressSeq
	p.chunks <- provider.Chunk{Type: provider.ChunkProgress}
	await(func(s ProgressSnapshot) bool { return s.ProgressSeq > before })
	select {
	case <-done:
		t.Fatal("stream finished before terminal chunk")
	default:
	}
	p.chunks <- provider.Chunk{Type: provider.ChunkUsage, Usage: &provider.Usage{PromptTokens: 40, CompletionTokens: 7}}
	p.chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(p.chunks)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not finish")
	}
	if snap := a.ProgressSnapshot(); *snap.InputTokens != 40 || *snap.OutputTokens != 7 {
		t.Fatalf("stream usage double counted: %+v", snap)
	}
}

type panicTool struct{ quickTool }

func (panicTool) Execute(context.Context, json.RawMessage) (string, error) { panic("tool panic") }

func TestPanickingToolReleasesStopCapability(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Add(panicTool{})
	a := New(testutil.NewMock("m"), reg, NewSession("sys"), Options{}, event.Discard)
	func() {
		defer func() { _ = recover() }()
		a.ExecuteSyntheticToolCall(context.Background(), "run", provider.ToolCall{ID: "panic", Name: "quick_tool", Arguments: `{}`})
	}()
	if len(a.ProgressSnapshot().ActiveTools) != 0 {
		t.Fatal("panic leaked active tool")
	}
}
