package runhub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestHub(t *testing.T) (*Hub, string) {
	t.Helper()
	dir := t.TempDir()
	h, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, dir
}

func TestLaunchIdempotent(t *testing.T) {
	h, _ := newTestHub(t)
	intent := LaunchIntent{RequestID: "req-1", Source: SourceDSH, Workspace: "w"}

	r1, run1 := h.Launch(intent)
	if r1.Status != ReceiptAccepted {
		t.Fatalf("first launch status = %s, want accepted", r1.Status)
	}
	r2, run2 := h.Launch(intent)
	if r2.Status != ReceiptAlreadyApplied {
		t.Fatalf("second launch status = %s, want already_applied", r2.Status)
	}
	if run2.ID != run1.ID {
		t.Fatalf("second launch run id %q != %q", run2.ID, run1.ID)
	}
	if got := h.List(Filter{}); len(got) != 1 {
		t.Fatalf("List after duplicate launch: n=%d, want 1", len(got))
	}
}

func TestLaunchInvalidRequestID(t *testing.T) {
	h, _ := newTestHub(t)
	r, _ := h.Launch(LaunchIntent{RequestID: "../bad"})
	if r.Status != ReceiptInvalid {
		t.Fatalf("status = %s, want invalid", r.Status)
	}
}

func TestReportDuplicateEventIsAlreadyApplied(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	evt := RunEvent{EventID: "evt-1", RunID: run.ID, Type: EventStarting}
	r1, after1 := h.Report(evt)
	if r1.Status != ReceiptAccepted || after1.Revision != run.Revision+1 {
		t.Fatalf("first report: status=%s rev=%d", r1.Status, after1.Revision)
	}

	r2, after2 := h.Report(evt)
	if r2.Status != ReceiptAlreadyApplied {
		t.Fatalf("duplicate status = %s, want already_applied", r2.Status)
	}
	if after2.Revision != after1.Revision {
		t.Fatalf("duplicate bumped revision: %d -> %d", after1.Revision, after2.Revision)
	}
}

func TestOutOfOrderLateEventCannotRegressTerminal(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	h.Report(RunEvent{EventID: "evt-success", RunID: run.ID, Type: EventSucceeded})
	rs, _ := h.Report(RunEvent{EventID: "evt-running", RunID: run.ID, Type: EventRunning})
	if rs.Status != ReceiptStale {
		t.Fatalf("late non-terminal status = %s, want stale", rs.Status)
	}

	got, ok := h.Get(run.ID)
	if !ok || got.State != StateSucceeded {
		t.Fatalf("state after late event = %s, want succeeded", got.State)
	}
	// A late terminal event also cannot replace the first terminal state.
	rs2, _ := h.Report(RunEvent{EventID: "evt-failed", RunID: run.ID, Type: EventFailed})
	if rs2.Status != ReceiptStale {
		t.Fatalf("late terminal status = %s, want stale", rs2.Status)
	}
	if got, _ := h.Get(run.ID); got.State != StateSucceeded {
		t.Fatalf("state after late terminal = %s, want succeeded", got.State)
	}
}

func TestInvalidTransitionFailsExplicitly(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	if r, _ := h.Report(RunEvent{EventID: "evt-run", RunID: run.ID, Type: EventRunning}); r.Status != ReceiptAccepted {
		t.Fatalf("running status = %s", r.Status)
	}
	r, _ := h.Report(RunEvent{EventID: "evt-start", RunID: run.ID, Type: EventStarting})
	if r.Status != ReceiptInvalid {
		t.Fatalf("running -> starting status = %s, want invalid", r.Status)
	}
}

func TestStaleRevision(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	if r, _ := h.Report(RunEvent{EventID: "evt-run", RunID: run.ID, Type: EventRunning}); r.Status != ReceiptAccepted {
		t.Fatalf("running status = %s", r.Status)
	}
	r, _ := h.Report(RunEvent{EventID: "evt-wait", RunID: run.ID, Type: EventWaitingUser, ExpectRevision: 1})
	if r.Status != ReceiptStale {
		t.Fatalf("stale revision status = %s, want stale", r.Status)
	}
	got, _ := h.Get(run.ID)
	if got.Revision != 2 {
		t.Fatalf("revision after stale = %d, want 2", got.Revision)
	}
}

func TestReloadPreservesStateAndReceipts(t *testing.T) {
	dir := t.TempDir()
	h1, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, run := h1.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})
	h1.Report(RunEvent{EventID: "evt-run", RunID: run.ID, Type: EventRunning})
	h1.Report(RunEvent{EventID: "evt-done", RunID: run.ID, Type: EventSucceeded})

	h2, err := New(dir)
	if err != nil {
		t.Fatalf("reload New: %v", err)
	}
	got, ok := h2.Get(run.ID)
	if !ok {
		t.Fatalf("run missing after reload")
	}
	if got.State != StateSucceeded || got.Revision != 3 {
		t.Fatalf("reloaded state=%s rev=%d, want succeeded/3", got.State, got.Revision)
	}

	// Idempotency survives the reload.
	if r, _ := h2.Report(RunEvent{EventID: "evt-done", RunID: run.ID, Type: EventSucceeded}); r.Status != ReceiptAlreadyApplied {
		t.Fatalf("duplicate after reload = %s", r.Status)
	}
	if r, _ := h2.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH}); r.Status != ReceiptAlreadyApplied {
		t.Fatalf("duplicate launch after reload = %s", r.Status)
	}
}

func TestReloadReconcilesPendingReceipt(t *testing.T) {
	dir := t.TempDir()
	h1, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, run := h1.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	// Simulate a crash that persisted the event-receipt claim but not the run
	// snapshot: write the receipt directly at revision 2.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	evt := RunEvent{EventID: "evt-done", RunID: run.ID, Type: EventSucceeded, OccurredAt: time.Now()}
	if err := s.SaveEventReceipt(evt.EventID, EventReceipt{
		EventID: evt.EventID, RunID: evt.RunID, Status: ReceiptAccepted,
		Revision: 2, Event: evt,
	}); err != nil {
		t.Fatal(err)
	}

	h2, err := New(dir)
	if err != nil {
		t.Fatalf("reconcile New: %v", err)
	}
	got, ok := h2.Get(run.ID)
	if !ok || got.State != StateSucceeded || got.Revision != 2 {
		t.Fatalf("reconciled run = %+v (ok=%v), want succeeded/2", got, ok)
	}
	logged, err := h2.store.ListEvents(run.ID)
	if err != nil || len(logged) != 1 || logged[0].EventID != evt.EventID {
		t.Fatalf("reconciled event log = %+v, err=%v", logged, err)
	}
}

func TestReportMaterializationFailureDoesNotReuseRevision(t *testing.T) {
	h, dir := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})
	eventsPath := filepath.Join(dir, "runs", string(run.ID), "events.jsonl")
	if err := os.Mkdir(eventsPath, 0o700); err != nil {
		t.Fatal(err)
	}

	evt := RunEvent{EventID: "evt-run", RunID: run.ID, Source: SourceDSH, Type: EventRunning}
	rec, advanced := h.Report(evt)
	if rec.Status != ReceiptRetryable || advanced.Revision != 2 {
		t.Fatalf("materialization failure = %+v run=%+v", rec, advanced)
	}
	if got, _ := h.Get(run.ID); got.Revision != 2 || got.State != StateRunning {
		t.Fatalf("in-memory committed run = %+v", got)
	}

	if err := os.Remove(eventsPath); err != nil {
		t.Fatal(err)
	}
	rec, repaired := h.Report(evt)
	if rec.Status != ReceiptAlreadyApplied || repaired.Revision != 2 {
		t.Fatalf("duplicate repair = %+v run=%+v", rec, repaired)
	}
	rec, next := h.Report(RunEvent{EventID: "evt-wait", RunID: run.ID, Source: SourceDSH, Type: EventWaitingUser})
	if rec.Status != ReceiptAccepted || next.Revision != 3 {
		t.Fatalf("next event reused revision: %+v run=%+v", rec, next)
	}
}

func TestReloadCorruptFails(t *testing.T) {
	dir := t.TempDir()
	h1, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, run := h1.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	meta := filepath.Join(dir, "runs", string(run.ID), "meta.json")
	if err := os.WriteFile(meta, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := New(dir); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("reload on corrupt meta: got %v, want corrupt error", err)
	}
}

func TestUnknownMetadataEventDoesNotBumpRevision(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	r, _ := h.Report(RunEvent{EventID: "evt-unknown", RunID: run.ID, Type: EventType("mystery"), Payload: EventPayload{Summary: "x"}})
	if r.Status != ReceiptInvalid {
		t.Fatalf("unknown metadata status = %s, want invalid", r.Status)
	}
	got, _ := h.Get(run.ID)
	if got.Revision != run.Revision {
		t.Fatalf("unknown metadata bumped revision %d -> %d", run.Revision, got.Revision)
	}
}

func TestConflictingEventSourceRejected(t *testing.T) {
	h, _ := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})

	r, _ := h.Report(RunEvent{EventID: "evt-codex", RunID: run.ID, Type: EventRunning, Source: SourceCodex})
	if r.Status != ReceiptInvalid {
		t.Fatalf("conflicting source status = %s, want invalid", r.Status)
	}
	got, _ := h.Get(run.ID)
	if got.Source != SourceDSH {
		t.Fatalf("run source mutated to %q", got.Source)
	}
}

func TestOpaqueColonIDsRoundTrip(t *testing.T) {
	h, dir := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req:abc:123", Source: SourceDSH})
	evt := RunEvent{EventID: "evt:1:2", RunID: run.ID, Type: EventRunning}
	if r, _ := h.Report(evt); r.Status != ReceiptAccepted {
		t.Fatalf("report with colon event id = %s", r.Status)
	}

	for _, sub := range []string{"launches", "inbox"} {
		entries, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() == "req:abc:123.json" || e.Name() == "evt:1:2.json" {
				t.Fatalf("%s/%s used raw opaque id as filename", sub, e.Name())
			}
		}
	}

	h2, err := New(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r, _ := h2.Launch(LaunchIntent{RequestID: "req:abc:123", Source: SourceDSH}); r.Status != ReceiptAlreadyApplied {
		t.Fatalf("reload launch idempotency = %s", r.Status)
	}
	if r, _ := h2.Report(evt); r.Status != ReceiptAlreadyApplied {
		t.Fatalf("reload event idempotency = %s", r.Status)
	}
}

func TestLaunchBackfillFailureReturnsRetryable(t *testing.T) {
	dir := t.TempDir()

	// Simulate a crash that wrote the run snapshot but not its launch receipt.
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run := AgentRun{
		ID: DeriveRunID("req-1"), Source: SourceDSH, Ownership: OwnershipManaged,
		State: StateQueued, Activity: ActivityIdle, Revision: 1,
		LastSeenAt: time.Now(), CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := s.SaveRun(run); err != nil {
		t.Fatal(err)
	}

	h, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Replace the (empty) launches dir with a regular file so the missing
	// receipt cannot be backfilled.
	launchDir := filepath.Join(dir, "launches")
	if err := os.RemoveAll(launchDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launchDir, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, _ := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})
	if r.Status != ReceiptRetryable {
		t.Fatalf("backfill failure status = %s, want retryable_error", r.Status)
	}
}

func TestSubscribeNotifiesCreatedAndUpdated(t *testing.T) {
	h, _ := newTestHub(t)
	ch, cancel := h.Subscribe()
	defer cancel()

	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})
	if c := <-ch; c.Kind != ChangeCreated || c.Run.ID != run.ID {
		t.Fatalf("first change = %+v", c)
	}

	h.Report(RunEvent{EventID: "evt-run", RunID: run.ID, Type: EventRunning})
	if c := <-ch; c.Kind != ChangeUpdated || c.Run.State != StateRunning {
		t.Fatalf("second change = %+v", c)
	}
}

func TestRecoverBindingsSettlesOrphansWithoutRestarting(t *testing.T) {
	h, dir := newTestHub(t)

	// A running run with a persisted binding becomes interrupted.
	_, running := h.Launch(LaunchIntent{RequestID: "req-running", Source: SourceDSH})
	h.Report(RunEvent{EventID: "evt-run", RunID: running.ID, Type: EventRunning})
	if err := h.store.SaveBinding(BindingRecord{
		RunID:   running.ID,
		Binding: RunnerBinding{RunID: running.ID, NativeSessionID: "sess-running", ProtocolVersion: "2.0", ProcessRef: "fake", Attempt: 1},
		State:   StateRunning, SavedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// A queued run with no binding becomes stale.
	_, queued := h.Launch(LaunchIntent{RequestID: "req-queued", Source: SourceDSH})

	n, err := h.RecoverBindings()
	if err != nil {
		t.Fatalf("RecoverBindings: %v", err)
	}
	if n != 2 {
		t.Fatalf("RecoverBindings settled %d, want 2", n)
	}
	if got, _ := h.Get(running.ID); got.State != StateInterrupted {
		t.Fatalf("running orphan state = %s, want interrupted", got.State)
	}
	if got, _ := h.Get(queued.ID); got.State != StateStale {
		t.Fatalf("queued orphan state = %s, want stale", got.State)
	}

	// Re-running is idempotent and never revives a terminal run.
	n, err = h.RecoverBindings()
	if err != nil || n != 0 {
		t.Fatalf("second RecoverBindings = (%d, %v), want (0, nil)", n, err)
	}
	if got, _ := h.Get(running.ID); got.State != StateInterrupted {
		t.Fatalf("state regressed to %s", got.State)
	}

	// A fresh reload also remembers the settlements and stays idempotent.
	h2, err := New(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got, _ := h2.Get(running.ID); got.State != StateInterrupted {
		t.Fatalf("reloaded orphan state = %s", got.State)
	}
	if n, _ := h2.RecoverBindings(); n != 0 {
		t.Fatalf("reload RecoverBindings settled %d, want 0", n)
	}
}

func TestReloadRejectsEventLogReceiptDivergence(t *testing.T) {
	h, dir := newTestHub(t)
	_, run := h.Launch(LaunchIntent{RequestID: "req-1", Source: SourceDSH})
	evt := RunEvent{EventID: "evt-1", RunID: run.ID, Source: SourceDSH, Type: EventRunning}
	if rec, _ := h.Report(evt); rec.Status != ReceiptAccepted {
		t.Fatalf("Report: %s", rec.Status)
	}

	evt.Type = EventWaitingUser
	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "runs", string(run.ID), "events.jsonl"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(dir); err == nil || !strings.Contains(err.Error(), "diverges from its receipt") {
		t.Fatalf("reload divergent event log: %v", err)
	}
}
