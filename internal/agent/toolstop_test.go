package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// stoppableTool blocks until its context is cancelled (a stand-in for bash /
// playwright / CDP-style long calls) and reports how it ended. delay is the
// natural completion time when nobody cancels (0 = 30s).
type stoppableTool struct {
	started   chan struct{}
	cancelObs atomic.Bool
	delay     time.Duration
	release   <-chan struct{}
}

func (*stoppableTool) Name() string        { return "stoppable" }
func (*stoppableTool) Description() string { return "blocks until cancelled" }
func (*stoppableTool) ReadOnly() bool      { return false }
func (*stoppableTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}}}`)
}
func (t *stoppableTool) Execute(ctx context.Context, _ json.RawMessage) (string, error) {
	if t.started != nil {
		select {
		case t.started <- struct{}{}:
		default:
		}
	}
	delay := t.delay
	if delay <= 0 {
		delay = 30 * time.Second
	}
	select {
	case <-ctx.Done():
		t.cancelObs.Store(true)
		if t.release != nil {
			<-t.release
		}
		return "partial-before-stop", ctx.Err()
	case <-time.After(delay):
		return "completed", nil
	}
}

func stoppedTurnToolCalls(t *testing.T, a *Agent) []provider.Message {
	t.Helper()
	var out []provider.Message
	for _, m := range a.Session().Messages {
		if m.Role == provider.RoleTool {
			out = append(out, m)
		}
	}
	return out
}

func TestStopToolCallCancelsCallAndTurnContinues(t *testing.T) {
	release := make(chan struct{})
	st := &stoppableTool{started: make(chan struct{}, 1), release: release}
	reg := tool.NewRegistry()
	reg.Add(st)
	mp := testutil.NewMock("stop-mock",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "stoppable", Arguments: `{}`}}},
		testutil.Turn{Text: "final after stop"},
	)
	sink := &recordSink{}
	a := New(mp, reg, NewSession("sys"), Options{}, sink)

	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), "run") }()

	select {
	case <-st.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool never started")
	}
	// A progress snapshot taken mid-run shows the active call with no stop yet.
	snap := a.ProgressSnapshot()
	if len(snap.ActiveTools) != 1 || snap.ActiveTools[0].CallID != "c1" || snap.ActiveTools[0].Name != "stoppable" {
		t.Fatalf("active tools = %+v, want c1/stoppable", snap.ActiveTools)
	}
	if snap.ActiveTools[0].StopRequested {
		t.Fatal("stop requested before any stop call")
	}
	if snap.Phase != "tool" {
		t.Fatalf("phase = %q, want tool", snap.Phase)
	}

	stopID := stopIDFor(t, a, "c1")
	if got := a.StopToolCall(stopID); got.Status != ToolStopAccepted {
		t.Fatalf("first stop = %+v, want accepted", got)
	}
	if got := a.StopToolCall(snap.ActiveTools[0].StopID); got.Status != ToolStopAlreadyStopped {
		t.Fatalf("repeat stop = %+v, want already_stopped", got)
	}
	// While stopping, the snapshot reflects the request.
	snap = a.ProgressSnapshot()
	if len(snap.ActiveTools) != 1 || !snap.ActiveTools[0].StopRequested {
		t.Fatalf("active tools after stop = %+v, want stopRequested=true", snap.ActiveTools)
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after tool stop = %v, want nil (turn must continue)", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish after single-tool stop")
	}
	if !st.cancelObs.Load() {
		t.Fatal("tool did not observe its context cancellation")
	}
	// The model received exactly one tool result with the user-stopped wording.
	results := stoppedTurnToolCalls(t, a)
	if len(results) != 1 || results[0].ToolCallID != "c1" {
		t.Fatalf("tool results = %+v, want exactly one for c1", results)
	}
	if !strings.Contains(results[0].Content, "user manually stopped") {
		t.Fatalf("result content = %q, want user-stopped wording", results[0].Content)
	}
	if !strings.Contains(results[0].Content, "partial-before-stop") {
		t.Fatalf("result content lost partial output: %q", results[0].Content)
	}
	// The turn produced a final answer afterwards (mock turn 2 consumed).
	if last := lastAssistantText(a); !strings.Contains(last, "final after stop") {
		t.Fatalf("final answer = %q", last)
	}
	for _, ev := range sink.kinds(event.ToolResult) {
		if ev.Tool.ID == "c1" && !ev.Tool.Stopped {
			t.Fatal("stopped result lacks typed stop flag")
		}
	}
	// Once finished, a late stop reports finished and never touches a new call.
	if got := a.StopToolCall(stopID); got.Status != ToolStopFinished {
		t.Fatalf("post-finish stop = %+v, want finished", got)
	}
}

func TestStopToolCallFinishedForUnknownAndParallelIsolation(t *testing.T) {
	// Stopping an unknown ID is finished, never a spurious cancel.
	reg := tool.NewRegistry()
	a := New(testutil.NewMock("m", testutil.Turn{Text: "hi"}), reg, NewSession("sys"), Options{}, &recordSink{})
	if got := a.StopToolCall("never-existed"); got.Status != ToolStopFinished {
		t.Fatalf("unknown call stop = %+v, want finished", got)
	}

	// Parallel read-only fan-out: stopping one call leaves the sibling running
	// to its natural completion.
	one := &stoppableTool{started: make(chan struct{}, 1)}
	two := &stoppableTool{started: make(chan struct{}, 1), delay: 400 * time.Millisecond}
	reg2 := tool.NewRegistry()
	reg2.Add(roStoppable{name: "ro_one", inner: one})
	reg2.Add(roStoppable{name: "ro_two", inner: two})
	mp := testutil.NewMock("m2",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "p1", Name: "ro_one", Arguments: `{}`},
			{ID: "p2", Name: "ro_two", Arguments: `{}`},
		}},
		testutil.Turn{Text: "done"},
	)
	a2 := New(mp, reg2, NewSession("sys"), Options{}, &recordSink{})
	done := make(chan error, 1)
	go func() { done <- a2.Run(context.Background(), "run") }()
	<-one.started
	<-two.started
	if got := a2.StopToolCall(stopIDFor(t, a2, "p1")); got.Status != ToolStopAccepted {
		t.Fatalf("stop p1 = %+v, want accepted", got)
	}
	// p1's stop must not touch p2: p2 is still active and not marked for stop
	// (p1 may already be finishing, so it may or may not still be listed).
	snap := a2.ProgressSnapshot()
	var p2Entry *ActiveToolInfo
	for i := range snap.ActiveTools {
		if snap.ActiveTools[i].CallID == "p2" {
			p2Entry = &snap.ActiveTools[i]
		}
		if snap.ActiveTools[i].CallID == "p1" && !snap.ActiveTools[i].StopRequested {
			t.Fatalf("p1 should be marked stopRequested: %+v", snap.ActiveTools)
		}
	}
	if p2Entry == nil {
		t.Fatalf("p2 vanished after stopping p1: %+v", snap.ActiveTools)
	}
	if p2Entry.StopRequested {
		t.Fatalf("p2 marked stopRequested after stopping p1: %+v", snap.ActiveTools)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("turn did not finish")
	}
	results := stoppedTurnToolCalls(t, a2)
	var p1Stopped, p2Stopped bool
	for _, r := range results {
		switch r.ToolCallID {
		case "p1":
			p1Stopped = strings.Contains(r.Content, "user manually stopped")
		case "p2":
			p2Stopped = strings.Contains(r.Content, "user manually stopped") || !strings.Contains(r.Content, "completed")
		}
	}
	if !p1Stopped {
		t.Fatal("p1 result lacks user-stopped wording")
	}
	if p2Stopped {
		t.Fatal("p2 was stopped together with p1 — parallel isolation broken")
	}
}

// roStoppable is a ReadOnly wrapper so both calls fan out across goroutines.
// Each parallel instance must register under a distinct name (registry is keyed
// by tool name).
type roStoppable struct {
	name  string
	inner *stoppableTool
}

func (r roStoppable) Name() string {
	if r.name != "" {
		return r.name
	}
	return "ro_stoppable"
}
func (roStoppable) Description() string { return "blocking read-only" }
func (roStoppable) ReadOnly() bool      { return true }
func (roStoppable) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (r roStoppable) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return r.inner.Execute(ctx, args)
}

func TestWholeTurnCancelStillTerminatesTurn(t *testing.T) {
	st := &stoppableTool{started: make(chan struct{}, 1)}
	reg := tool.NewRegistry()
	reg.Add(st)
	mp := testutil.NewMock("m", testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "stoppable", Arguments: `{}`}}})
	a := New(mp, reg, NewSession("sys"), Options{}, &recordSink{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx, "run") }()
	<-st.started
	cancel() // whole-turn stop: global Cancel path
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("Run after global cancel = %v, want context canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("global cancel did not end the turn")
	}
	for _, r := range stoppedTurnToolCalls(t, a) {
		if strings.Contains(r.Content, "user manually stopped") {
			t.Fatalf("global cancel must not produce the single-tool stopped wording: %q", r.Content)
		}
	}
}

func lastAssistantText(a *Agent) string {
	for i := len(a.Session().Messages) - 1; i >= 0; i-- {
		if a.Session().Messages[i].Role == provider.RoleAssistant {
			return a.Session().Messages[i].Content
		}
	}
	return ""
}

func TestProgressSnapshotTracksUsageOncePerRequest(t *testing.T) {
	// A usage-carrying provider: two requests (tool-call turn then final text),
	// each with provider-reported usage. The cumulative snapshot must equal the
	// sum of both requests — recorded once per completed request.
	reg := tool.NewRegistry()
	reg.Add(quickTool{})
	mp := testutil.NewMock("usage-mock",
		testutil.Turn{
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "quick_tool", Arguments: `{}`}},
			Usage:     &provider.Usage{PromptTokens: 30, CompletionTokens: 6, TotalTokens: 36},
		},
		testutil.Turn{
			Text:  "answer",
			Usage: &provider.Usage{PromptTokens: 20, CompletionTokens: 4, TotalTokens: 24},
		},
	)
	a := New(mp, reg, NewSession("sys"), Options{}, &recordSink{})
	if err := a.Run(context.Background(), "run"); err != nil {
		t.Fatalf("Run = %v", err)
	}
	snap := a.ProgressSnapshot()
	if !snap.UsageSeen {
		t.Fatal("no usage observed")
	}
	if snap.UsageSource != "executor" {
		t.Fatalf("usage source = %q, want executor", snap.UsageSource)
	}
	if snap.InputTokens == nil || *snap.InputTokens != 50 {
		t.Fatalf("input tokens = %v, want 50 (30+20, no double count)", snap.InputTokens)
	}
	if snap.OutputTokens == nil || *snap.OutputTokens != 10 {
		t.Fatalf("output tokens = %v, want 10 (6+4, no double count)", snap.OutputTokens)
	}
	if len(snap.ActiveTools) != 0 {
		t.Fatalf("active tools linger after turn: %+v", snap.ActiveTools)
	}
	if snap.Phase != "idle" {
		t.Fatalf("phase = %q, want idle after turn", snap.Phase)
	}
	if snap.Provider != "usage-mock" {
		t.Fatalf("provider = %q, want usage-mock", snap.Provider)
	}
}

type quickTool struct{}

func (quickTool) Name() string        { return "quick_tool" }
func (quickTool) Description() string { return "instant tool" }
func (quickTool) ReadOnly() bool      { return false }
func (quickTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (quickTool) Execute(context.Context, json.RawMessage) (string, error) { return "quick-ok", nil }

func TestProgressUsageRecordAndCompactionSource(t *testing.T) {
	a := New(testutil.NewMock("u", testutil.Turn{Text: "x"}), tool.NewRegistry(), NewSession("sys"), Options{}, &recordSink{})
	if snap := a.ProgressSnapshot(); snap.UsageSeen {
		t.Fatal("usage seen before any request")
	}
	a.recordProgressUsage(&provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, event.UsageSourceExecutor)
	a.recordProgressUsage(&provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}, event.UsageSourceExecutor)
	a.recordProgressUsage(&provider.Usage{PromptTokens: 100, CompletionTokens: 0, TotalTokens: 100}, event.UsageSourceCompaction)
	snap := a.ProgressSnapshot()
	if !snap.UsageSeen {
		t.Fatal("usageSeen false after records")
	}
	if snap.InputTokens == nil || *snap.InputTokens != 120 {
		t.Fatalf("input tokens = %v, want 120 (10+10+100)", snap.InputTokens)
	}
	if snap.OutputTokens == nil || *snap.OutputTokens != 10 {
		t.Fatalf("output tokens = %v, want 10", snap.OutputTokens)
	}
	if snap.UsageSource != event.UsageSourceCompaction {
		t.Fatalf("usage source = %q, want compaction (last record)", snap.UsageSource)
	}
	if snap.Provider != "u" {
		t.Fatalf("provider = %q, want u", snap.Provider)
	}
	if snap.LastTokenGrowthAt.IsZero() || snap.UsageAt.IsZero() {
		t.Fatal("growth/usage timestamps missing")
	}
	if snap.ProgressSeq < 3 {
		t.Fatalf("progressSeq = %d, want >=3 real events", snap.ProgressSeq)
	}
}

func TestResetProgressOnSessionSwap(t *testing.T) {
	a := New(testutil.NewMock("r", testutil.Turn{Text: "x"}), tool.NewRegistry(), NewSession("sys"), Options{}, &recordSink{})
	a.recordProgressUsage(&provider.Usage{PromptTokens: 9, CompletionTokens: 1, TotalTokens: 10}, "executor")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, finish := a.beginToolCall(ctx, "c9", "bash"); finish() {
		t.Fatal("unexpected stop on fresh call")
	}
	if snap := a.ProgressSnapshot(); snap.InputTokens == nil || *snap.InputTokens != 9 {
		t.Fatalf("pre-reset tokens = %v", snap.InputTokens)
	}
	a.SetSession(NewSession("other"))
	snap := a.ProgressSnapshot()
	if snap.UsageSeen {
		t.Fatal("usage leaked across session swap")
	}
	if snap.InputTokens != nil || snap.OutputTokens != nil {
		t.Fatalf("counts leaked across session swap: %+v", snap)
	}
	if len(snap.ActiveTools) != 0 {
		t.Fatalf("active calls leaked across session swap: %+v", snap.ActiveTools)
	}
	if got := a.StopToolCall("c9"); got.Status != ToolStopFinished {
		t.Fatalf("stale call after swap = %+v, want finished", got)
	}
}

var _ = event.Event{} // guard: event import used by the source table above

// TestStopRealBashCallReturnsPromptly proves the per-call stop reaches the bash
// tool's own process-tree cancellation: stopping the call returns well before
// the sleep's natural duration and the tool result carries the user-stopped
// wording, with the turn continuing to a final answer.
func TestStopRealBashCallReturnsPromptly(t *testing.T) {
	if !hasStopWaitShell() {
		t.Skip("no shell for long-running bash on this host")
	}
	bashTool, ok := tool.LookupBuiltin("bash")
	if !ok {
		t.Fatal("bash builtin missing")
	}
	reg := tool.NewRegistry()
	reg.Add(bashTool)
	mp := testutil.NewMock("bash-stop",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "c1", Name: "bash", Arguments: `{"command":"` + waitSleepCommand() + `"}`}}},
		testutil.Turn{Text: "final after bash stop"},
	)
	a := New(mp, reg, NewSession("sys"), Options{}, &recordSink{})
	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- a.Run(context.Background(), "run") }()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, at := range a.ProgressSnapshot().ActiveTools {
			if at.CallID == "c1" {
				goto found
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("bash call never became active")
found:
	if got := a.StopToolCall(stopIDFor(t, a, "c1")); got.Status != ToolStopAccepted {
		t.Fatalf("stop bash = %+v, want accepted", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("bash call did not end promptly after per-call stop")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("whole turn took %v — the stopped sleep still ran", elapsed)
	}
	foundStopped := false
	for _, msg := range a.Session().Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "c1" && strings.Contains(msg.Content, "user manually stopped") {
			foundStopped = true
		}
	}
	if !foundStopped {
		t.Fatal("session lacks the user-stopped result for the bash call")
	}
}

func stopIDFor(t *testing.T, a *Agent, callID string) string {
	t.Helper()
	for _, call := range a.ProgressSnapshot().ActiveTools {
		if call.CallID == callID {
			return call.StopID
		}
	}
	t.Fatalf("no active call %q", callID)
	return ""
}
