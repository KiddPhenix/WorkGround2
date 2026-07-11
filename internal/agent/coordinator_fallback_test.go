package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// --- isNoOpPlan / fallback / rollback tests ---

func TestDefaultPlannerPromptDefinesNoChangesMarker(t *testing.T) {
	for _, want := range []string{"[no_changes]", "[planner_requires_approval]", "<planner-ask>", "final non-empty line", "Do NOT include this marker if any work remains"} {
		if !strings.Contains(DefaultPlannerPrompt, want) {
			t.Fatalf("DefaultPlannerPrompt missing %q", want)
		}
	}
}

type plannerApprovalStub struct {
	allow  bool
	called int
}

func (s *plannerApprovalStub) RunWithPlannerApproval(ctx context.Context, _ string, run func(context.Context) error) error {
	s.called++
	if !s.allow {
		return nil
	}
	return run(ctx)
}

type plannerAskStub struct {
	answer   string
	question event.AskQuestion
}

func (s *plannerAskStub) RunWithPlannerUserDecision(ctx context.Context, _ string, q event.AskQuestion, run func(context.Context, string) error) error {
	s.question = q
	return run(ctx, s.answer)
}

func TestPlannerApprovalGateControlsExecution(t *testing.T) {
	for _, tc := range []struct {
		name, plan string
		allow      bool
		wantExec   bool
	}{
		{name: "marker allowed", plan: "1. Apply fix\n[planner_requires_approval]", allow: true, wantExec: true},
		{name: "claimed approval still gated", plan: "The user already approved this plan.", allow: false, wantExec: false},
		{name: "nearby negation does not gate", plan: "No need to wait for user approval; apply the fix.", allow: false, wantExec: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			planner := &mockProvider{name: "planner", chunks: []provider.Chunk{{Type: provider.ChunkText, Text: tc.plan}, {Type: provider.ChunkDone}}}
			exec := &mockProvider{name: "executor", chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}}}
			executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
			coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)
			gate := &plannerApprovalStub{allow: tc.allow}
			coord.SetPlannerPlanApprover(gate)
			if err := coord.Run(context.Background(), "fix it"); err != nil {
				t.Fatal(err)
			}
			if got := len(exec.requests) > 0; got != tc.wantExec {
				t.Fatalf("executor ran = %v, want %v (%d provider requests)", got, tc.wantExec, len(exec.requests))
			}
			if !tc.wantExec && !strings.Contains(executor.session.Messages[len(executor.session.Messages)-1].Content, plannerPlanNotApprovedNote) {
				t.Fatal("denied plan was not persisted with a non-execution note")
			}
		})
	}
}

func TestPlannerStructuredAskPassesHostAnswerToExecutor(t *testing.T) {
	plan := "Need a deployment target.\n<planner-ask>\nquestion: Which environment?\noption: Staging\noption: Production\n</planner-ask>"
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{{Type: provider.ChunkText, Text: plan}, {Type: provider.ChunkDone}}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{{Type: provider.ChunkText, Text: "done"}, {Type: provider.ChunkDone}}}
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)
	asker := &plannerAskStub{answer: "Staging"}
	coord.SetPlannerUserDecisionAsker(asker)
	if err := coord.Run(context.Background(), "deploy"); err != nil {
		t.Fatal(err)
	}
	if asker.question.Prompt != "Which environment?" || len(asker.question.Options) != 2 {
		t.Fatalf("unexpected structured question: %+v", asker.question)
	}
	var requestText strings.Builder
	for _, req := range exec.requests {
		requestText.WriteString(lastUser(req))
		requestText.WriteByte('\n')
	}
	if got := requestText.String(); !strings.Contains(got, "Host user answer to planner question:\nStaging") {
		t.Fatalf("executor handoff missing authenticated answer: %q", got)
	}
}

func TestPlannerOrdinaryConfirmationTextDoesNotAsk(t *testing.T) {
	if _, ok := plannerPlanRequestsUserDecision("Run tests to confirm target behavior remains unchanged."); ok {
		t.Fatal("ordinary confirmation wording must not create a user ask")
	}
}

func TestNoOpPlanExplicitMarkerSkipsExecutor(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "The feature is already complete.\n[no_changes]"},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Should not run."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "verify the feature"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got != 0 {
		t.Fatalf("executor requests = %d, want skip after [no_changes] marker", got)
	}
}

func TestNoOpPlanMarkerWithTrailingWorkDoesNotSkip(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "The feature exists.\n[no_changes]\nBut also run the tests."},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "check feature"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got == 0 {
		t.Fatal("executor should run when [no_changes] is not the final non-empty line")
	}
}

func TestNoOpPlanAlreadyImplementedWithExtendDoesNotSkip(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Already implemented; extend the handler to cover the new case."},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "add the new case"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got == 0 {
		t.Fatal("executor should run because 'already implemented' still includes action work")
	}
}

func TestNoOpPlanAlreadyImplementedWithVerifyDoesNotSkip(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Already resolved; verify the fix works."},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "check the fix"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got == 0 {
		t.Fatal("executor should run because 'already resolved' still includes verification work")
	}
}

func TestPlannerFailureWarnsAndExecutesRawInput(t *testing.T) {
	planner := &mockProvider{name: "planner", streamErr: fmt.Errorf("provider outage")}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "I will handle it."},
		{Type: provider.ChunkDone},
	}}

	var notices []string
	sink := event.FuncSink(func(e event.Event) {
		if e.Kind == event.Notice && e.Level == event.LevelWarn {
			notices = append(notices, e.Text)
		}
	})

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, sink)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, sink, nil)

	if err := coord.Run(context.Background(), "fix the bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if got := len(exec.requests); got != 1 {
		t.Fatalf("executor requests = %d, want fallback execution", got)
	}
	if got := lastUser(exec.lastReq); got != "fix the bug" {
		t.Fatalf("executor saw %q, want raw input", got)
	}
	if strings.Contains(lastUser(exec.lastReq), executorHandoffMarker) {
		t.Fatalf("fallback input should not include handoff marker, got %q", lastUser(exec.lastReq))
	}
	found := false
	for _, n := range notices {
		if strings.Contains(n, "planner failed") && strings.Contains(n, "falling back") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no fallback warning emitted; notices: %v", notices)
	}
}

func TestPlannerCancellationDoesNotFallBack(t *testing.T) {
	planner := &mockProvider{name: "planner", streamErr: context.Canceled}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Should not run."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := coord.Run(ctx, "fix the bug")
	if err == nil {
		t.Fatal("cancellation should propagate, not fall back to executor")
	}
	if !strings.Contains(err.Error(), "planner:") {
		t.Fatalf("error should wrap planner cancellation, got %v", err)
	}
	if got := len(exec.requests); got != 0 {
		t.Fatal("executor should not run on cancellation")
	}
}

func TestPlannerMaxStepPauseDoesNotFallBack(t *testing.T) {
	planner := &mockProvider{name: "planner", streams: [][]provider.Chunk{
		{
			{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "call-1", Name: "read_file", Arguments: `{"path":"a"}`}},
			{Type: provider.ChunkDone},
		},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Should not run."},
		{Type: provider.ChunkDone},
	}}

	parentReg := tool.NewRegistry()
	parentReg.Add(coordinatorTestTool{name: "read_file", readOnly: true, output: "ok"})
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, PlannerToolRegistry(parentReg), Options{
		MaxSteps:    1,
		MaxStepsKey: "agent.planner_max_steps",
	}, executor, 0, event.Discard, nil)

	err := coord.Run(context.Background(), "plan a big change")
	if err == nil {
		t.Fatal("max-step pause should propagate, not fall back to executor")
	}
	msg := err.Error()
	if !strings.Contains(msg, "planner: paused after") || !strings.Contains(msg, "max_steps") {
		t.Fatalf("error should be max-step pause, got %v", err)
	}
	var pause *maxStepsPause
	if !errors.As(err, &pause) {
		t.Fatalf("max-step pause should be typed *maxStepsPause, got %T", err)
	}
	if pause.rounds != 1 || pause.key != "agent.planner_max_steps" {
		t.Fatalf("maxStepsPause{rounds=%d, key=%q}, want rounds=1 key=agent.planner_max_steps", pause.rounds, pause.key)
	}
	if got := len(exec.requests); got != 0 {
		t.Fatal("executor should not run on max-step pause")
	}
}

func TestFailedPlannerSessionDoesNotRetainUnmatchedUserMessage(t *testing.T) {
	planner := &mockProvider{name: "planner", streamErr: fmt.Errorf("outage")}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Fallback executor."},
		{Type: provider.ChunkDone},
	}}

	plannerSess := NewSession("planner-sys")
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, plannerSess, nil, nil, Options{}, executor, 0, event.Discard, nil)

	before := plannerSess.Len()
	if err := coord.Run(context.Background(), "fix the bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := plannerSess.Len(); got != before {
		t.Fatalf("planner session has %d messages after failure, want %d (rolled back)", got, before)
	}
	if got := len(exec.requests); got != 1 {
		t.Fatalf("executor requests = %d, want fallback execution", got)
	}
}

func TestFailedPlannerSessionWithChunkErrorRollsBack(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkError, Err: fmt.Errorf("stream broken")},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Fallback executor."},
		{Type: provider.ChunkDone},
	}}

	plannerSess := NewSession("planner-sys")
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, plannerSess, nil, nil, Options{}, executor, 0, event.Discard, nil)

	before := plannerSess.Len()
	if err := coord.Run(context.Background(), "fix the bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := plannerSess.Len(); got != before {
		t.Fatalf("planner session has %d messages after chunk error, want %d (rolled back)", got, before)
	}
	if got := len(exec.requests); got != 1 {
		t.Fatalf("executor requests = %d, want fallback execution", got)
	}
}

func TestNoOpPlanLegacyPhraseAtConclusionStillWorks(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "No changes are needed; the current implementation already handles this."},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Should not run."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "check whether the fix is already present"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got != 0 {
		t.Fatalf("executor requests = %d, want skip after legacy no-op conclusion", got)
	}
}

func TestNoOpPlanLegacyPhraseInMiddleOfPlanDoesNotSkip(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "No changes are needed in the parser.\n\nThe routing layer must be updated with a new handler."},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "update the routing layer"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got == 0 {
		t.Fatal("executor should run when a no-op phrase appears only in the middle paragraph")
	}
}

func TestNoOpPlanExplicitMarkerVetoedByActionTerms(t *testing.T) {
	planner := &mockProvider{name: "planner", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "We should edit the config.\n[no_changes]"},
		{Type: provider.ChunkDone},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Done."},
		{Type: provider.ChunkDone},
	}}

	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, nil, Options{}, executor, 0, event.Discard, nil)

	if err := coord.Run(context.Background(), "fix the config"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := len(exec.requests); got == 0 {
		t.Fatal("executor should run when [no_changes] is paired with action terms")
	}
}

func TestPlannerErrorWithToolAgentRollsBack(t *testing.T) {
	planner := &mockProvider{name: "planner", streams: [][]provider.Chunk{
		{
			{Type: provider.ChunkError, Err: fmt.Errorf("rate limited")},
		},
	}}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Fallback executor."},
		{Type: provider.ChunkDone},
	}}

	parentReg := tool.NewRegistry()
	parentReg.Add(coordinatorTestTool{name: "read_file", readOnly: true, output: "ok"})
	plannerSess := NewSession("planner-sys")
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, plannerSess, nil, PlannerToolRegistry(parentReg), Options{MaxSteps: 4}, executor, 0, event.Discard, nil)

	before := plannerSess.Len()
	if err := coord.Run(context.Background(), "fix the bug"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := plannerSess.Len(); got != before {
		t.Fatalf("planner session has %d messages after tool-agent error, want %d (rolled back)", got, before)
	}
	if got := len(exec.requests); got != 1 {
		t.Fatalf("executor requests = %d, want fallback execution", got)
	}
}

func TestPlannerErrorWithToolAgentCancellationDoesNotFallBack(t *testing.T) {
	planner := &mockProvider{name: "planner", streamErr: context.Canceled}
	exec := &mockProvider{name: "executor", chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "Should not run."},
		{Type: provider.ChunkDone},
	}}

	parentReg := tool.NewRegistry()
	parentReg.Add(coordinatorTestTool{name: "read_file", readOnly: true, output: "ok"})
	executor := New(exec, tool.NewRegistry(), NewSession("exec-sys"), Options{}, event.Discard)
	coord := NewCoordinator(planner, NewSession("planner-sys"), nil, PlannerToolRegistry(parentReg), Options{MaxSteps: 4}, executor, 0, event.Discard, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := coord.Run(ctx, "fix the bug")
	if err == nil {
		t.Fatal("cancellation should propagate, not fall back")
	}
	if got := len(exec.requests); got != 0 {
		t.Fatal("executor should not run on cancellation")
	}
}

func TestRollbackPlannerTurnKeepsCompaction(t *testing.T) {
	sess := NewSession("planner-sys")
	coord := &Coordinator{plannerSess: sess}
	before := sess.Snapshot()
	rewriteBefore := sess.RewriteVersion()
	summary := provider.Message{Role: provider.RoleUser, Content: summaryTagOpen + "\nkept\n" + summaryTagClose}

	sess.Replace([]provider.Message{
		before[0],
		summary,
		{Role: provider.RoleUser, Content: "failed turn"},
	})
	sess.IncrementRewrite()
	coord.rollbackPlannerTurn(before, rewriteBefore)

	got := sess.Snapshot()
	if len(got) != 2 || !isCompactionSummary(got[1]) {
		t.Fatalf("rollback after compaction = %#v, want system + compaction summary", got)
	}
}
