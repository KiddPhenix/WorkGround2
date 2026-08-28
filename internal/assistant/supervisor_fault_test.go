package assistant

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// newTestExecutorQ builds a supervisor executor over a caller-provided queue so
// fault-injection tests can arm the queue hooks before wiring.
func newTestExecutorQ(t *testing.T, store *Store, host *fakeSupervisorHost, control *fakeSessionControl, q *SupervisorEventQueue) *SupervisorExecutor {
	t.Helper()
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: host,
		Control:           func() SessionControl { return control },
		HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ex
}

// planWithScan gives the assistant one ready responsibility "scan" so advance
// decisions have a target.
func planWithScan(t *testing.T, store *Store, snapshot Snapshot, requestID string) {
	t.Helper()
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: requestID, AssistantID: snapshot.Assistant.ID,
		Progress: ProgressBlock{
			PlanRevision:     snapshot.Plan.Revision,
			Responsibilities: []RespDecl{{Alias: "scan", Objective: "Scan", NextAction: "run the scan"}},
		},
		Now: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisorExecutorEnqueueWriteFailure proves an enqueue IO failure is
// never swallowed: EnqueueUserInput and EnqueueRoutineFires return the real
// error and nothing is durably recorded.
func TestSupervisorExecutorEnqueueWriteFailure(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{}
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	q.failAppend = func(assistantID string, rec eventRecord) error {
		return errors.New("injected journal write failure")
	}
	ex := newTestExecutorQ(t, store, host, &fakeSessionControl{}, q)

	if err := ex.EnqueueUserInput("helper-a", "req-1", "scan"); err == nil {
		t.Fatal("EnqueueUserInput swallowed the journal write failure")
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("failed enqueue still recorded: pending=%v err=%v", hasPending, err)
	}
	if err := ex.EnqueueRoutineFires([]RoutineFire{{FireID: "fire-1", AssistantID: "helper-a"}}, time.Now()); err == nil {
		t.Fatal("EnqueueRoutineFires swallowed the journal write failure")
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("failed routine enqueue still recorded: pending=%v err=%v", hasPending, err)
	}
}

// TestSupervisorExecutorRouteFailureKeepsEventsPending proves defect 1: a
// routed action that fails (or a missing capability) never consumes the turn's
// trigger events. They stay pending, no batch receipt is written, and the
// cycle stays open — the durable, observable retry state.
func TestSupervisorExecutorRouteFailureKeepsEventsPending(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"steer","target":"session-1","rationale":"nudge"}`})
	control := &fakeSessionControl{steerErr: errors.New("injected steer failure")}
	var diags []string
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: host,
		Control:           func() SessionControl { return control },
		HeartbeatInterval: time.Hour,
		Diagnostic: func(operation string, err error) {
			diags = append(diags, operation)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ex.EnqueueUserInput("helper-a", "req-1", "go"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), time.Now())

	if hasPending, err := q.HasPending("helper-a"); err != nil || !hasPending {
		t.Fatalf("failed route consumed the trigger events: pending=%v err=%v", hasPending, err)
	}
	events, _ := q.Pending("helper-a")
	batchID := eventBatchID(events)
	if _, ok, err := q.LoadBatchReceipt("helper-a", batchID); err != nil || ok {
		t.Fatalf("failed route wrote a batch receipt: ok=%v err=%v", ok, err)
	}
	cycle, ok := store.LatestCycle("helper-a")
	if !ok || cycle.State == CycleCompleted {
		t.Fatalf("failed route completed the cycle: %+v ok=%v, want it open (retry state)", cycle, ok)
	}
	sawSteerDiag := false
	for _, op := range diags {
		if op == "supervisor_steer" {
			sawSteerDiag = true
		}
	}
	if !sawSteerDiag {
		t.Fatalf("steer failure was not observable: diagnostics = %v", diags)
	}
}

// TestSupervisorExecutorCrashBetweenActionAndConsumeReplaysOnce proves defect
// 4: a crash between a successful action and MarkConsumed leaves the events
// pending; the next pass resolves the batch receipt as already_applied and
// consumes the events WITHOUT re-running the model turn or the external
// action.
func TestSupervisorExecutorCrashBetweenActionAndConsumeReplaysOnce(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-a")
	planWithScan(t, store, snapshot, "plan-crash")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"advance","target":"scan","rationale":"scan is ready"}`})
	control := &fakeSessionControl{}
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the crash window: the action succeeds and the batch receipt is
	// written, but every consume write fails.
	q.failAppend = func(assistantID string, rec eventRecord) error {
		if rec.Op == "consume" {
			return errors.New("injected consume write failure")
		}
		return nil
	}
	ex := newTestExecutorQ(t, store, host, control, q)

	if err := ex.EnqueueUserInput("helper-a", "req-1", "scan"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), time.Now())

	if control.createdCount() != 1 {
		t.Fatalf("first pass created %d sessions, want 1", control.createdCount())
	}
	events, _ := q.Pending("helper-a")
	if len(events) != 1 {
		t.Fatalf("events after failed consume = %d, want 1 still pending", len(events))
	}
	batchID := eventBatchID(events)
	if rec, ok, err := q.LoadBatchReceipt("helper-a", batchID); err != nil || !ok {
		t.Fatalf("batch receipt missing after routed action: ok=%v err=%v", ok, err)
	} else if rec.Outcome != RouteApplied {
		t.Fatalf("batch receipt outcome = %q, want applied", rec.Outcome)
	}

	// "Restart": the process comes back, the injection is gone, and the same
	// pending events are settled through the receipt.
	q.failAppend = nil
	ex.RunTurns(context.Background(), time.Now())

	if control.createdCount() != 1 {
		t.Fatalf("replay created %d sessions, want still 1 (no duplicate external action)", control.createdCount())
	}
	if got := host.submitCount(); got != 1 {
		t.Fatalf("replay ran %d model turns, want 1 (receipt settled it)", got)
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("events not consumed on replay: pending=%v err=%v", hasPending, err)
	}
	cycle, ok := store.LatestCycle("helper-a")
	if !ok || cycle.State != CycleCompleted {
		t.Fatalf("cycle after replay = %+v ok=%v, want completed", cycle, ok)
	}
}

// TestSupervisorExecutorBudgetedTurnResumesAfterRestart proves defect 3: a
// turn that outlived its budget leaves a durable checkpoint (turn id, event
// batch, Session history baseline); after a restart the next tick settles the
// old turn outcome instead of submitting a second model round, and only
// re-submits when the checkpoint proves the submission never durably landed.
func TestSupervisorExecutorBudgetedTurnResumesAfterRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	snapshot := mustCreate(t, store, "helper-a")
	planWithScan(t, store, snapshot, "plan-budget")
	ref := SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}

	host := &fakeSupervisorHost{ref: ref, historyLen: 5}
	host.setOutcome(SupervisorTurnOutcome{Running: true, HistoryLen: 5})
	control := &fakeSessionControl{}
	q1, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	ex1 := newTestExecutorQ(t, store, host, control, q1)
	if err := ex1.EnqueueUserInput("helper-a", "req-1", "scan"); err != nil {
		t.Fatal(err)
	}
	ex1.RunTurns(context.Background(), time.Now())

	if got := host.submitCount(); got != 1 {
		t.Fatalf("budgeted turn submitted %d times on the first pass, want 1", got)
	}
	cp, ok, err := q1.LoadTurnCheckpoint("helper-a")
	if err != nil || !ok {
		t.Fatalf("no durable turn checkpoint after budget expiry: ok=%v err=%v", ok, err)
	}
	if cp.HistoryLen != 5 || len(cp.EventIDs) != 1 {
		t.Fatalf("checkpoint = %+v, want the event batch and the history baseline 5", cp)
	}
	if hasPending, err := q1.HasPending("helper-a"); err != nil || !hasPending {
		t.Fatalf("trigger events lost while the turn was in flight: pending=%v err=%v", hasPending, err)
	}
	if control.createdCount() != 0 {
		t.Fatalf("in-flight turn already acted: %d creates", control.createdCount())
	}

	// The turn finished while the process was down. A fresh executor over the
	// same root (no in-process state shared) must settle, never re-submit. The
	// settled outcome carries the post-turn history length (grew past the
	// checkpoint baseline), proving the submission durably landed.
	host2 := &fakeSupervisorHost{ref: ref}
	host2.setOutcome(SupervisorTurnOutcome{Text: `{"action":"advance","target":"scan","rationale":"settled"}`, HistoryLen: 9})
	q2, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	ex2 := newTestExecutorQ(t, store, host2, control, q2)
	ex2.RunTurns(context.Background(), time.Now().Add(time.Minute))

	if got := host2.submitCount(); got != 0 {
		t.Fatalf("restart submitted %d new turns, want 0 (settle only)", got)
	}
	if control.createdCount() != 1 {
		t.Fatalf("restart routed %d advances, want 1", control.createdCount())
	}
	if hasPending, err := q2.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("events not consumed after settle: pending=%v err=%v", hasPending, err)
	}
	if _, ok, err := q2.LoadTurnCheckpoint("helper-a"); err != nil || ok {
		t.Fatalf("checkpoint not cleared after settle: ok=%v err=%v", ok, err)
	}
}

// TestSupervisorExecutorBudgetedTurnReSubmitsWhenNeverDurable proves a
// checkpoint whose Session history never grew (crash between the intent and
// the submit) is confirmed not-submitted: the checkpoint is cleared and the
// events stay pending so a fresh turn can follow.
func TestSupervisorExecutorBudgetedTurnReSubmitsWhenNeverDurable(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	// The first pass reports the turn in flight on an empty Session: the
	// pre-submit baseline probe saw 0 messages (a crash between the intent
	// and a durable submit left the transcript unchanged).
	host.setOutcome(SupervisorTurnOutcome{Running: true, HistoryLen: 0})
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	ex := newTestExecutorQ(t, store, host, &fakeSessionControl{}, q)
	if err := ex.EnqueueUserInput("helper-a", "req-1", "scan"); err != nil {
		t.Fatal(err)
	}
	ex.RunTurns(context.Background(), time.Now())
	if _, ok, err := q.LoadTurnCheckpoint("helper-a"); err != nil || !ok {
		t.Fatalf("no checkpoint after the in-flight pass: ok=%v err=%v", ok, err)
	}

	// A settle with no history growth proves the submission never durably
	// landed: the checkpoint is cleared and the events stay pending.
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`, HistoryLen: 0})
	ex.RunTurns(context.Background(), time.Now().Add(time.Minute))

	if _, ok, err := q.LoadTurnCheckpoint("helper-a"); err != nil || ok {
		t.Fatalf("stale checkpoint not cleared: ok=%v err=%v", ok, err)
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || !hasPending {
		t.Fatalf("events consumed by a never-submitted turn: pending=%v err=%v", hasPending, err)
	}
	// A fresh turn may now submit and consume normally.
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`})
	ex.RunTurns(context.Background(), time.Now().Add(2*time.Minute))
	if hasPending, err := q.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("fresh turn did not consume the events: pending=%v err=%v", hasPending, err)
	}
}

// TestSupervisorExecutorCrashAfterCheckpointBeforeSubmitWithPriorHistory proves
// the checkpoint baseline is captured BEFORE the submission: a crash between the
// intent checkpoint and the submit — on a supervisor Session that ALREADY has
// durable history from earlier turns — must never be misjudged as a landed turn.
// The stale transcript is not routed and the trigger events are not consumed;
// the next tick re-submits.
func TestSupervisorExecutorCrashAfterCheckpointBeforeSubmitWithPriorHistory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	snapshot := mustCreate(t, store, "helper-a")
	planWithScan(t, store, snapshot, "plan-crash-old-history")
	ref := SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}

	// The supervisor Session already carries durable history (e.g. three
	// earlier turns settled). The pre-submit baseline probe must observe it.
	host := &fakeSupervisorHost{ref: ref, historyLen: 42}
	control := &fakeSessionControl{}
	q1, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	ex1 := newTestExecutorQ(t, store, host, control, q1)
	if err := ex1.EnqueueUserInput("helper-a", "req-1", "scan"); err != nil {
		t.Fatal(err)
	}

	// The process dies right after the intent checkpoint was saved and before
	// the submission could durably land: RunSupervisorTurn panics (nothing is
	// submitted, the transcript does not grow).
	host.panicOnRun = true
	func() {
		defer func() { _ = recover() }()
		ex1.RunTurns(context.Background(), time.Now())
	}()

	// The checkpoint survived the crash and carries the PRE-submit baseline,
	// not the zero value.
	cp, ok, err := q1.LoadTurnCheckpoint("helper-a")
	if err != nil || !ok {
		t.Fatalf("no durable checkpoint after the crash: ok=%v err=%v", ok, err)
	}
	if cp.HistoryLen != 42 {
		t.Fatalf("checkpoint baseline = %d, want 42 (the durable history BEFORE the submit)", cp.HistoryLen)
	}

	// Restart: a fresh executor settles the checkpoint. The Session history did
	// NOT grow (the submit never landed), so the settle must confirm
	// not-submitted: clear the checkpoint, keep the events pending, and never
	// route the stale transcript or run a second submit.
	host2 := &fakeSupervisorHost{ref: ref, historyLen: 42}
	host2.setOutcome(SupervisorTurnOutcome{Text: `{"action":"advance","target":"scan","rationale":"stale"}`, HistoryLen: 42})
	q2, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	ex2 := newTestExecutorQ(t, store, host2, control, q2)
	ex2.RunTurns(context.Background(), time.Now().Add(time.Minute))

	if got := host2.submitCount(); got != 0 {
		t.Fatalf("settle pass submitted %d new turns, want 0 (not-submitted confirmed)", got)
	}
	if control.createdCount() != 0 {
		t.Fatalf("stale transcript was routed: %d sessions created without a real turn", control.createdCount())
	}
	if hasPending, err := q2.HasPending("helper-a"); err != nil || !hasPending {
		t.Fatalf("events consumed by a never-submitted turn: pending=%v err=%v", hasPending, err)
	}
	if _, ok, err := q2.LoadTurnCheckpoint("helper-a"); err != nil || ok {
		t.Fatalf("not-submitted checkpoint not cleared: ok=%v err=%v", ok, err)
	}

	// The next tick submits a fresh turn, which lands durably and routes once.
	host2.setOutcome(SupervisorTurnOutcome{Text: `{"action":"advance","target":"scan","rationale":"fresh"}`, HistoryLen: 60})
	ex2.RunTurns(context.Background(), time.Now().Add(2*time.Minute))
	if got := host2.submitCount(); got != 1 {
		t.Fatalf("fresh tick submitted %d turns, want 1", got)
	}
	if control.createdCount() != 1 {
		t.Fatalf("fresh turn routed %d advances, want 1", control.createdCount())
	}
	if hasPending, err := q2.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("events not consumed after the fresh turn: pending=%v err=%v", hasPending, err)
	}
	cycle, ok := store.LatestCycle("helper-a")
	if !ok || cycle.State != CycleCompleted {
		t.Fatalf("cycle after fresh turn = %+v ok=%v, want completed", cycle, ok)
	}
}

// TestSupervisorExecutorExpandRequestIDsStablePerBatch proves the expand
// action's request IDs are derived from assistantID + batchID, never from the
// wall clock: a replay of the same durable event batch (a crash between the
// ideation and the batch receipt — the receipt file is never written) reuses
// the same IDs, so the idea receipt and the expansion record are deduped:
// exactly one idea and one recorded pass, no duplicate ideation. Empty-batch
// manual/test calls keep per-call uniqueness.
func TestSupervisorExecutorExpandRequestIDsStablePerBatch(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	snapshot := mustCreate(t, store, "helper-a")
	ideator, err := NewIdeator(store, roleModel(ideatorOutput()))
	if err != nil {
		t.Fatal(err)
	}
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	ex, err := NewSupervisorExecutor(SupervisorExecutorOptions{
		Store: store, Events: q, Host: &fakeSupervisorHost{},
		Ideator: ideator, HeartbeatInterval: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	a := snapshot.Assistant
	batchID := eventBatchID([]SupervisorEvent{{ID: "ev-1"}, {ID: "ev-2"}})
	now1 := time.Now()

	// First pass: the ideation and the expansion record durably land, but the
	// process crashes BEFORE the batch receipt is saved (the receipt file is
	// never written, so the next pass sees no receipt).
	res1 := ex.expand(a, batchID, now1)
	if res1.Outcome != RouteApplied {
		t.Fatalf("first expand = %+v, want applied", res1)
	}
	// Replay: the same durable batch is routed again. The stable IDs make the
	// idea receipt and the expansion receipt dedup instead of re-ideating.
	res2 := ex.expand(a, batchID, now1.Add(time.Hour))
	if res2.Outcome != RouteApplied {
		t.Fatalf("replayed expand = %+v, want applied", res2)
	}

	snap, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Ideas) != 1 {
		t.Fatalf("replayed expand created %d ideas, want exactly 1 (dedup by stable request id)", len(snap.Ideas))
	}
	if snap.Expansion.Attempt != 0 || snap.Expansion.LastTrigger != ExpansionPlanEmpty {
		t.Fatalf("expansion state after replay = %+v, want the single recorded success", snap.Expansion)
	}

	// Empty batch (manual/test calls): per-call uniqueness is preserved.
	res3 := ex.expand(a, "", now1)
	res4 := ex.expand(a, "", now1.Add(time.Minute))
	if res3.Outcome != RouteApplied || res4.Outcome != RouteApplied {
		t.Fatalf("empty-batch expands = %+v, %+v, want both applied", res3, res4)
	}
	snap, err = store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Ideas) != 3 {
		t.Fatalf("empty-batch expands created %d ideas total, want 3 (1 batched + 2 manual)", len(snap.Ideas))
	}
}

// TestSupervisorExecutorHeartbeatNoFloodOnStateSaveFailure proves defect 5:
// the heartbeat uses a stable watermark ID, so when SaveState fails between
// the enqueue and the state sidecar, repeated collects never mint fresh event
// IDs (no flood), and after a consume the same ID fires again once the
// interval passed.
func TestSupervisorExecutorHeartbeatNoFloodOnStateSaveFailure(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{}
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	q.failStateSave = func(assistantID string) error {
		return errors.New("injected state save failure")
	}
	ex := newTestExecutorQ(t, store, host, &fakeSessionControl{}, q)
	now := time.Now()
	stableID := eventID(EventHeartbeat, "helper-a", "idle")

	if n := ex.CollectSupervisorEvents(now); n != 1 {
		t.Fatalf("idle collect enqueued %d, want 1 heartbeat", n)
	}
	events, _ := q.Pending("helper-a")
	if len(events) != 1 || events[0].ID != stableID {
		t.Fatalf("pending = %+v, want the stable heartbeat id %s", events, stableID)
	}
	// SaveState keeps failing: consecutive collects must not flood new events.
	if n := ex.CollectSupervisorEvents(now.Add(time.Minute)); n != 0 {
		t.Fatalf("state-save failure flooded %d new events", n)
	}
	if n := ex.CollectSupervisorEvents(now.Add(2 * time.Minute)); n != 0 {
		t.Fatalf("state-save failure flooded %d new events on a third collect", n)
	}
	events, _ = q.Pending("helper-a")
	if len(events) != 1 {
		t.Fatalf("pending heartbeat count = %d, want exactly 1", len(events))
	}
	// Once the state saves again and the heartbeat is consumed, the same
	// stable ID fires after the interval — the watermark is not stuck.
	q.failStateSave = nil
	if err := q.MarkConsumed("helper-a", events[0].ID); err != nil {
		t.Fatal(err)
	}
	if n := ex.CollectSupervisorEvents(now.Add(2 * time.Hour)); n != 1 {
		t.Fatalf("post-interval collect enqueued %d, want 1 heartbeat", n)
	}
	events, _ = q.Pending("helper-a")
	if len(events) != 1 || events[0].ID != stableID {
		t.Fatalf("re-fired heartbeat = %+v, want the stable id %s", events, stableID)
	}
}

// TestSupervisorExecutorConcurrentTicksSubmitOnce proves defect 3's concurrency
// guard: many overlapping ticks for one assistant submit exactly one
// supervisor turn; the losers settle the winner's checkpoint.
func TestSupervisorExecutorConcurrentTicksSubmitOnce(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{ref: SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"}}
	host.setOutcome(SupervisorTurnOutcome{Running: true, HistoryLen: 3})
	control := &fakeSessionControl{}
	q, err := NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	ex := newTestExecutorQ(t, store, host, control, q)
	if err := ex.EnqueueUserInput("helper-a", "req-1", "go"); err != nil {
		t.Fatal(err)
	}

	const n = 8
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ex.RunTurns(context.Background(), time.Now())
		}()
	}
	wg.Wait()

	if got := host.submitCount(); got != 1 {
		t.Fatalf("concurrent ticks submitted %d supervisor turns, want 1", got)
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || !hasPending {
		t.Fatalf("events lost during the concurrent pass: pending=%v err=%v", hasPending, err)
	}

	// The in-flight turn finishes; the next tick settles it — still exactly
	// one model round total.
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`, HistoryLen: 7})
	ex.RunTurns(context.Background(), time.Now())
	if got := host.submitCount(); got != 1 {
		t.Fatalf("settle pass submitted %d more turns, want still 1 total", got)
	}
	if hasPending, err := q.HasPending("helper-a"); err != nil || hasPending {
		t.Fatalf("events not consumed after the settle pass: pending=%v err=%v", hasPending, err)
	}
	cycle, ok := store.LatestCycle("helper-a")
	if !ok || cycle.State != CycleCompleted {
		t.Fatalf("cycle after settle = %+v ok=%v, want completed", cycle, ok)
	}
}
