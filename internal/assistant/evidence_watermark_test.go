package assistant

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func recordEvidenceResearch(t *testing.T, store *Store, assistantID, requestID string, at time.Time) {
	t.Helper()
	if _, err := store.RecordResearch(RecordResearchInput{
		RequestID:   requestID,
		AssistantID: assistantID,
		Research: Research{
			Kind: ResearchWeb, SourceURL: "https://example.com/" + requestID,
			Evidence: "seen", Verification: ResearchUnverified,
		},
		Now: at,
	}); err != nil {
		t.Fatal(err)
	}
}

// TestSupervisorExecutorEvidenceWakesOnce proves historical evidence wakes the
// supervisor exactly once. The first tick has a zero observation watermark (the
// legacy-aggregate case), so the historical research is "new": it runs one turn
// and durably advances the watermark. Subsequent ticks with no new evidence must
// not call the host again.
func TestSupervisorExecutorEvidenceWakesOnce(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{
		ref:     SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"},
		managed: []ManagedSessionSummary{{ID: "s1", Status: "running"}},
	}
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`})
	ex := newTestExecutor(t, store, host, &fakeSessionControl{})

	recordEvidenceResearch(t, store, "helper-a", "research-old", testEpoch)

	// First tick: zero watermark -> historical evidence is new.
	ex.RunTurns(context.Background(), testEpoch.Add(time.Minute))
	if got := host.submitCount(); got != 1 {
		t.Fatalf("first tick submitted %d turns, want 1", got)
	}

	// The watermark advanced past the evidence; further ticks must stay quiet.
	for i := 2; i <= 4; i++ {
		ex.RunTurns(context.Background(), testEpoch.Add(time.Duration(i)*time.Minute))
	}
	if got := host.submitCount(); got != 1 {
		t.Fatalf("after first wake submitted %d turns, want still 1", got)
	}
}

// TestSupervisorExecutorNewEvidenceWakesAgain proves evidence created after the
// observation watermark wakes the supervisor exactly one more time, and then the
// loop goes quiet again.
func TestSupervisorExecutorNewEvidenceWakesAgain(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{
		ref:     SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"},
		managed: []ManagedSessionSummary{{ID: "s1", Status: "running"}},
	}
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`})
	ex := newTestExecutor(t, store, host, &fakeSessionControl{})

	recordEvidenceResearch(t, store, "helper-a", "research-old", testEpoch)
	ex.RunTurns(context.Background(), testEpoch.Add(time.Minute))
	if got := host.submitCount(); got != 1 {
		t.Fatalf("first tick submitted %d turns, want 1", got)
	}

	// New evidence arrives after the first observation watermark.
	recordEvidenceResearch(t, store, "helper-a", "research-new", testEpoch.Add(90*time.Second))
	ex.RunTurns(context.Background(), testEpoch.Add(2*time.Minute))
	if got := host.submitCount(); got != 2 {
		t.Fatalf("new evidence submitted %d turns, want 2", got)
	}

	ex.RunTurns(context.Background(), testEpoch.Add(3*time.Minute))
	if got := host.submitCount(); got != 2 {
		t.Fatalf("after new-evidence wake submitted %d turns, want still 2", got)
	}
}

// TestSupervisorExecutorLateEvidenceNotSwallowed proves the watermark advances
// to the snapshot-derived evidence boundary, not wall-clock now: a record
// written after the snapshot but stamped earlier than the tick's now is not
// swallowed, and wakes the supervisor on the next tick.
func TestSupervisorExecutorLateEvidenceNotSwallowed(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{
		ref:     SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"},
		managed: []ManagedSessionSummary{{ID: "s1", Status: "running"}},
	}
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`})
	ex := newTestExecutor(t, store, host, &fakeSessionControl{})

	recordEvidenceResearch(t, store, "helper-a", "research-early", testEpoch)
	ex.RunTurns(context.Background(), testEpoch.Add(time.Minute))
	if got := host.submitCount(); got != 1 {
		t.Fatalf("first tick submitted %d turns, want 1", got)
	}
	// The watermark advanced to the evidence boundary (testEpoch), not the
	// tick's wall-clock now (testEpoch+1m).
	snap, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Expansion.EvidenceObservedAt.Equal(testEpoch) {
		t.Fatalf("watermark = %v, want evidence boundary %v", snap.Expansion.EvidenceObservedAt, testEpoch)
	}

	// A late record stamped between the boundary and the first tick's now must
	// not be swallowed: it was never in the first snapshot.
	recordEvidenceResearch(t, store, "helper-a", "research-late", testEpoch.Add(30*time.Second))
	ex.RunTurns(context.Background(), testEpoch.Add(2*time.Minute))
	if got := host.submitCount(); got != 2 {
		t.Fatalf("late evidence submitted %d turns, want 2", got)
	}
}

// TestSupervisorExecutorCheckpointPreservesEvidenceBoundary proves the snapshot
// boundary survives a budget-expiry (Running) turn through its durable
// checkpoint, and the settle advances the watermark to exactly that boundary —
// not the settle tick's now.
func TestSupervisorExecutorCheckpointPreservesEvidenceBoundary(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	host := &fakeSupervisorHost{
		ref:     SupervisorSessionRef{ID: "supervisor-helper-a", Path: "/tmp/s-helper-a.jsonl"},
		managed: []ManagedSessionSummary{{ID: "s1", Status: "running"}},
	}
	ex := newTestExecutor(t, store, host, &fakeSessionControl{})

	recordEvidenceResearch(t, store, "helper-a", "research-early", testEpoch)

	// First tick runs in flight (budget expired): the checkpoint persists the
	// snapshot-derived evidence boundary.
	host.setOutcome(SupervisorTurnOutcome{Running: true, HistoryLen: 1})
	ex.RunTurns(context.Background(), testEpoch.Add(time.Minute))
	if got := host.submitCount(); got != 1 {
		t.Fatalf("first tick submitted %d turns, want 1", got)
	}
	cp, ok, err := ex.events.LoadTurnCheckpoint("helper-a")
	if err != nil || !ok {
		t.Fatalf("LoadTurnCheckpoint: ok=%v err=%v", ok, err)
	}
	if !cp.EvidenceObservedThrough.Equal(testEpoch) {
		t.Fatalf("checkpoint boundary = %v, want %v", cp.EvidenceObservedThrough, testEpoch)
	}

	// Settle the in-flight turn: the watermark must advance to the checkpoint
	// boundary (testEpoch), not the settle tick's now (testEpoch+2m).
	host.setOutcome(SupervisorTurnOutcome{Text: `{"action":"wait"}`, HistoryLen: 2})
	ex.RunTurns(context.Background(), testEpoch.Add(2*time.Minute))
	snap, err := store.Get("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Expansion.EvidenceObservedAt.Equal(testEpoch) {
		t.Fatalf("settled watermark = %v, want checkpoint boundary %v", snap.Expansion.EvidenceObservedAt, testEpoch)
	}
}

// TestRecordEvidenceObservationMonotonic proves the watermark only moves
// forward: a replay (or clock skew) never regresses it, so a crash between the
// write and the caller's next step re-observes the same state instead of
// re-triggering stale evidence.
func TestRecordEvidenceObservationMonotonic(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	first, err := store.RecordEvidenceObservation(RecordEvidenceObservationInput{
		RequestID: "observe-1", AssistantID: "helper-a", ObservedThrough: testEpoch.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordEvidenceObservation: %v", err)
	}
	if !first.EvidenceObservedAt.Equal(testEpoch.Add(time.Minute)) {
		t.Fatalf("watermark = %v, want %v", first.EvidenceObservedAt, testEpoch.Add(time.Minute))
	}

	// A stale observation must not move the watermark backwards.
	replayed, err := store.RecordEvidenceObservation(RecordEvidenceObservationInput{
		RequestID: "observe-2", AssistantID: "helper-a", ObservedThrough: testEpoch,
	})
	if err != nil {
		t.Fatalf("RecordEvidenceObservation stale: %v", err)
	}
	if !replayed.EvidenceObservedAt.Equal(testEpoch.Add(time.Minute)) {
		t.Fatalf("stale observation regressed watermark to %v", replayed.EvidenceObservedAt)
	}

	// A genuinely later observation advances it.
	later, err := store.RecordEvidenceObservation(RecordEvidenceObservationInput{
		RequestID: "observe-3", AssistantID: "helper-a", ObservedThrough: testEpoch.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("RecordEvidenceObservation later: %v", err)
	}
	if !later.EvidenceObservedAt.Equal(testEpoch.Add(2 * time.Minute)) {
		t.Fatalf("later observation watermark = %v, want %v", later.EvidenceObservedAt, testEpoch.Add(2*time.Minute))
	}

	// A zero boundary (no evidence observed) is a no-op: it never fabricates a
	// future watermark.
	noop, err := store.RecordEvidenceObservation(RecordEvidenceObservationInput{
		RequestID: "observe-4", AssistantID: "helper-a", ObservedThrough: time.Time{},
	})
	if err != nil {
		t.Fatalf("RecordEvidenceObservation zero: %v", err)
	}
	if !noop.EvidenceObservedAt.Equal(testEpoch.Add(2 * time.Minute)) {
		t.Fatalf("zero boundary moved watermark to %v", noop.EvidenceObservedAt)
	}

	// Persistence survives reopening the store.
	reopened := testStore(t, store.Root())
	snap, err := reopened.Get("helper-a")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if !snap.Expansion.EvidenceObservedAt.Equal(testEpoch.Add(2 * time.Minute)) {
		t.Fatalf("watermark after reopen = %v", snap.Expansion.EvidenceObservedAt)
	}
}
