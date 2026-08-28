package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
)

// fakeRecordingHost is a SupervisorHost stub that records managed sessions and
// the supervisor ref so App-API tests can drive the diagnostic/managed-session
// DTOs without a live desktop session registry.
type fakeRecordingHost struct {
	ref     assistant.SupervisorSessionRef
	managed []assistant.ManagedSessionSummary
	running []assistant.SupervisorSessionSummary
	failed  []assistant.SupervisorSessionSummary
}

func (h *fakeRecordingHost) FindSupervisorSession(string) (assistant.SupervisorSessionRef, bool) {
	if h.ref.ID == "" {
		return assistant.SupervisorSessionRef{}, false
	}
	return h.ref, true
}
func (h *fakeRecordingHost) EnsureSupervisorSession(a assistant.Assistant) (assistant.SupervisorSessionRef, error) {
	return h.ref, nil
}
func (h *fakeRecordingHost) ManagedSessions(string) []assistant.ManagedSessionSummary {
	return h.managed
}
func (h *fakeRecordingHost) SupervisorHistoryLen(ref assistant.SupervisorSessionRef) (int, error) {
	return 1, nil
}
func (h *fakeRecordingHost) RunSupervisorTurn(ref assistant.SupervisorSessionRef, prompt string, budget time.Duration) assistant.SupervisorTurnOutcome {
	return assistant.SupervisorTurnOutcome{HistoryLen: 1}
}
func (h *fakeRecordingHost) SettleSupervisorTurn(ref assistant.SupervisorSessionRef) assistant.SupervisorTurnOutcome {
	return assistant.SupervisorTurnOutcome{HistoryLen: 1}
}

// newAssistantRuntimeWithHost builds a test runtime whose executor host is the
// recording stub, so the App-API diagnostic/managed-session DTOs read real
// backend state through the same paths the production desktop host uses.
func newAssistantRuntimeWithHost(t *testing.T, host *fakeRecordingHost) (*AssistantRuntime, *assistant.Store) {
	t.Helper()
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatalf("NewSupervisorEventQueue: %v", err)
	}
	executor, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:      store,
		Events:     events,
		Host:       host,
		Diagnostic: func(string, error) {},
	})
	if err != nil {
		t.Fatalf("NewSupervisorExecutor: %v", err)
	}
	service := &AssistantRuntime{store: store, executor: executor, tick: time.Hour, wake: make(chan struct{}, 1), viewport: assistant.NewViewport()}
	return service, store
}

// TestAssistantManagedSessionsProjection verifies the read-only managed-Session
// DTO: it lists running/waiting/failed/completed sessions with owner, purpose,
// bound responsibility, workspace and update time, ordered by status then by
// recency, and never fabricates fields for sessions without meta.
func TestAssistantManagedSessionsProjection(t *testing.T) {
	root := t.TempDir()
	host := &fakeRecordingHost{}
	service, store := newAssistantRuntimeWithHost(t, host)

	// Create one real managed Session file with BranchMeta so the DTO derives
	// purpose/responsibility/workspace/updatedAt from the Session subsystem.
	sessionPath := filepath.Join(root, "managed-1.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.AssistantID = "assistant-a"
	meta.Purpose = agent.PurposeManaged
	meta.ResponsibilityID = "resp-scan"
	meta.WorkspaceRoot = "C:/proj"
	meta.Status = agent.SessionStatusRunning
	meta.UpdatedAt = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		t.Fatal(err)
	}
	host.managed = []assistant.ManagedSessionSummary{
		{ID: "managed-1", Path: sessionPath, Title: "扫描修改", Status: "running", Turns: 1},
		{ID: "managed-2", Path: "/tmp/nope.jsonl", Title: "发布", Status: "failed", Turns: 2},
	}
	service.store = store

	app := &App{assistant: service}
	views, err := app.AssistantManagedSessions("assistant-a")
	if err != nil {
		t.Fatalf("AssistantManagedSessions: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %d, want 2", len(views))
	}
	// running ranks before failed.
	if views[0].Status != "running" || views[1].Status != "failed" {
		t.Fatalf("status ordering = %s, %s", views[0].Status, views[1].Status)
	}
	first := views[0]
	if first.ID != "managed-1" || first.OwnerID != "assistant-a" || first.Purpose != "managed" {
		t.Fatalf("managed-1 projection = %+v", first)
	}
	if first.ResponsibilityID != "resp-scan" || first.WorkspaceRoot != "C:/proj" {
		t.Fatalf("managed-1 meta not derived: %+v", first)
	}
	if !first.UpdatedAt.Equal(meta.UpdatedAt) {
		t.Fatalf("managed-1 updated_at = %v, want %v", first.UpdatedAt, meta.UpdatedAt)
	}
	// managed-2 has no meta: purpose stays the explicit default, update time zero.
	if views[1].Purpose != "managed" || !views[1].UpdatedAt.IsZero() {
		t.Fatalf("managed-2 no-meta projection = %+v", views[1])
	}
}

// TestAssistantSupervisorDiagnosticDTO verifies the diagnostic view exposes the
// supervisor Session identity, cycle observation revisions, pending events,
// recent decision/action receipts, next step, failures and retry counts from
// backend authoritative state.
func TestAssistantSupervisorDiagnosticDTO(t *testing.T) {
	host := &fakeRecordingHost{
		ref: assistant.SupervisorSessionRef{ID: "supervisor-1", Path: "/tmp/supervisor-1.jsonl"},
		managed: []assistant.ManagedSessionSummary{
			{ID: "s1", Title: "扫描", Status: "running", Turns: 1},
			{ID: "s2", Title: "发布", Status: "failed", Turns: 2},
		},
	}
	service, store := newAssistantRuntimeWithHost(t, host)
	now := time.Now()

	// Open a cycle so the diagnostic has observation revisions + next step.
	_, err := store.Create(assistant.CreateInput{
		RequestID: "create-diag",
		Assistant: assistant.Assistant{
			ID: "assistant-diag", Name: "Diag", Mission: "m",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	cycle, err := store.OpenCycle(assistant.OpenCycleInput{
		AssistantID: "assistant-diag",
		RequestID:   "cycle-1",
		Observed:    assistant.CycleObservation{PlanRevision: 2, AssistantRevision: 3, MemoryRevision: 4, WorkEpoch: 1},
		Now:         now,
	})
	if err != nil {
		t.Fatalf("OpenCycle: %v", err)
	}
	if _, err := store.CheckpointCycle(assistant.CheckpointCycleInput{
		AssistantID: "assistant-diag", CycleID: cycle.ID,
		RequestID: "checkpoint-1", Fence: cycle.Fence, NextStep: "advance 1 executable responsibilities",
		Now: now,
	}); err != nil {
		t.Fatalf("CheckpointCycle: %v", err)
	}
	if err := service.executor.EnqueueUserInput("assistant-diag", "req-1", "hello"); err != nil {
		t.Fatalf("EnqueueUserInput: %v", err)
	}
	if _, err := store.RecordInteractionDecision(assistant.InteractionDecisionRecord{
		ID: "decision-1", AssistantID: "assistant-diag", SessionID: "s1", InteractionID: "q1",
		Source: assistant.DecisionInfer, Confidence: 0.9, Result: "answer-a", CreatedAt: now,
	}); err != nil {
		t.Fatalf("RecordInteractionDecision: %v", err)
	}

	app := &App{assistant: service}
	diag, err := app.AssistantSupervisorDiagnostic("assistant-diag")
	if err != nil {
		t.Fatalf("AssistantSupervisorDiagnostic: %v", err)
	}
	if diag.Supervisor == nil || diag.Supervisor.ID != "supervisor-1" {
		t.Fatalf("supervisor ref = %+v", diag.Supervisor)
	}
	if diag.Cycle == nil || diag.Cycle.Observed.PlanRevision != 2 || diag.Cycle.Observed.WorkEpoch != 1 {
		t.Fatalf("cycle observation = %+v", diag.Cycle)
	}
	if diag.NextStep != "advance 1 executable responsibilities" {
		t.Fatalf("next step = %q", diag.NextStep)
	}
	if len(diag.PendingEvents) != 1 || diag.PendingEvents[0].Kind != "user_input" {
		t.Fatalf("pending events = %+v", diag.PendingEvents)
	}
	if len(diag.RecentDecisions) != 1 || diag.RecentDecisions[0].Result != "answer-a" {
		t.Fatalf("recent decisions = %+v", diag.RecentDecisions)
	}
	if len(diag.FailedSessions) != 1 || diag.FailedSessions[0].ID != "s2" {
		t.Fatalf("failed sessions = %+v", diag.FailedSessions)
	}
	if diag.RetryDue < 0 {
		t.Fatalf("retry due = %d", diag.RetryDue)
	}
	// The executor's SessionSummaries come from the host: running includes s1.
	if len(diag.RunningSessions) != 1 || diag.RunningSessions[0].ID != "s1" {
		t.Fatalf("running sessions = %+v", diag.RunningSessions)
	}
}

// TestAssistantAppViewportLateRejection verifies the App API round-trips the
// user's window observation through the runtime viewport at the binding
// boundary: a stale ui_revision never regresses the view, a genuinely newer
// revision wins, and an unknown runtime is an explicit unknown. The exact
// same-revision/earlier-observed_at rejection (which needs explicit
// timestamps) is exercised by the package-level viewport tests.
func TestAssistantAppViewportLateRejection(t *testing.T) {
	host := &fakeRecordingHost{}
	service, _ := newAssistantRuntimeWithHost(t, host)
	app := &App{assistant: service}

	app.AssistantPublishViewport(AssistantPublishViewportRequest{WindowID: "w1", UIRevision: 5, SelectedSessionID: "new"})
	// A stale revision is ignored at the binding boundary.
	app.AssistantPublishViewport(AssistantPublishViewportRequest{WindowID: "w1", UIRevision: 3, SelectedSessionID: "old"})
	cur, ok := app.AssistantViewport()
	if !ok || cur.SelectedSessionID != "new" || cur.UIRevision != 5 {
		t.Fatalf("stale revision regressed the viewport: %+v ok=%v", cur, ok)
	}
	// A genuinely newer revision wins.
	app.AssistantPublishViewport(AssistantPublishViewportRequest{WindowID: "w1", UIRevision: 6, SelectedSessionID: "fresher"})
	cur, ok = app.AssistantViewport()
	if !ok || cur.SelectedSessionID != "fresher" || cur.UIRevision != 6 {
		t.Fatalf("newer revision lost: %+v ok=%v", cur, ok)
	}
	// A same-revision re-observation (later observed_at) replaces the view
	// instead of regressing it.
	app.AssistantPublishViewport(AssistantPublishViewportRequest{WindowID: "w1", UIRevision: 6, SelectedSessionID: "refreshed"})
	if cur, ok = app.AssistantViewport(); !ok || cur.SelectedSessionID != "refreshed" {
		t.Fatalf("same-revision re-observation did not replace the view: %+v ok=%v", cur, ok)
	}
	// Unknown window / no runtime is an explicit unknown, never a fake value.
	if _, ok := (&App{}).AssistantViewport(); ok {
		t.Fatal("App without an assistant runtime reported a viewport")
	}
}

// TestAssistantSessionControlOutcomeVocabulary verifies the unified write
// outcomes map backend errors to the design vocabulary (accepted / invalid /
// blocked_by_policy / stale / retryable_error / already_applied) and that a
// missing Session target is an explicit error rather than a silent success.
func TestAssistantSessionControlOutcomeVocabulary(t *testing.T) {
	app := &App{}
	at := time.Now()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "ok", err: nil, want: "accepted"},
		{name: "outcome unknown", err: agent.ErrOutcomeUnknown, want: "invalid"},
		{name: "blocked policy", err: agent.ErrBlockedPolicy, want: "blocked_by_policy"},
		{name: "blocked dependency", err: agent.ErrBlockedDependency, want: "retryable_error"},
		{name: "lease lost", err: assistant.ErrLeaseLost, want: "stale"},
		{name: "receipt conflict", err: &agent.SessionReceiptConflictError{RequestID: "r", SessionID: "s"}, want: "invalid"},
		{name: "op receipt conflict", err: &agent.OpReceiptConflictError{Key: "r"}, want: "invalid"},
		{name: "already applied", err: errors.New("request already applied"), want: "already_applied"},
		{name: "generic", err: context.Canceled, want: "retryable_error"},
	}
	if got, invalid := invalidSessionAction("s1", "", at); !invalid || got.Outcome != "invalid" {
		t.Fatalf("empty request id = %+v invalid=%v", got, invalid)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sessionControlOutcome("s1", "req-1", tc.err, at)
			if got.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q (message %q)", got.Outcome, tc.want, got.Message)
			}
		})
	}

	// A missing session is a real error surfaced to the caller.
	if _, err := app.AssistantSessionStatus("missing-session"); err == nil {
		t.Fatal("AssistantSessionStatus(missing) = nil error, want explicit not-found")
	}
}

func TestAssistantSessionActionReceiptReplaysWithoutRepeating(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	apply := func(fingerprint string) (string, bool, error) {
		return applyAssistantSessionAction(dir, "session_steer", "request-1", fingerprint, func() (string, error) {
			calls++
			return "session-1", nil
		})
	}
	firstID, replayed, err := apply("same-input")
	if err != nil || replayed || firstID != "session-1" || calls != 1 {
		t.Fatalf("first apply = id %q replayed=%v calls=%d err=%v", firstID, replayed, calls, err)
	}
	secondID, replayed, err := apply("same-input")
	if err != nil || !replayed || secondID != firstID || calls != 1 {
		t.Fatalf("replay = id %q replayed=%v calls=%d err=%v", secondID, replayed, calls, err)
	}
	_, _, err = apply("changed-input")
	var conflict *agent.OpReceiptConflictError
	if !errors.As(err, &conflict) || calls != 1 {
		t.Fatalf("changed input err=%v calls=%d", err, calls)
	}
}
