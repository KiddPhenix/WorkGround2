package assistant

import (
	"path/filepath"
	"testing"
	"time"
)

func TestRecordInteractionDecisionIdempotentAndLatest(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-dec")
	now := testEpoch

	deferred := InteractionDecisionRecord{
		ID:            StableID("decision", "helper-dec/sess-1/ask-1/deferred"),
		AssistantID:   "helper-dec",
		SessionID:     "sess-1",
		InteractionID: "ask-1",
		Source:        DecisionDeferred,
		Result:        "deferred",
		DueAt:         now.Add(time.Minute),
		CreatedAt:     now,
	}
	got, err := store.RecordInteractionDecision(deferred)
	if err != nil {
		t.Fatal(err)
	}
	if got.CreatedAt.IsZero() {
		t.Fatalf("CreatedAt not stamped: %+v", got)
	}

	// Replaying the same record ID returns the stored record, not a duplicate.
	replay, err := store.RecordInteractionDecision(deferred)
	if err != nil {
		t.Fatal(err)
	}
	if replay.ID != got.ID || !replay.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("replay changed record: %+v vs %+v", replay, got)
	}

	latest, ok, err := store.LatestDecision("helper-dec", "sess-1", "ask-1")
	if err != nil || !ok {
		t.Fatalf("LatestDecision ok=%v err=%v", ok, err)
	}
	if latest.ID != deferred.ID || !latest.DueAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("latest = %+v", latest)
	}

	// A later real decision supersedes the deferral.
	real := InteractionDecisionRecord{
		ID:            StableID("decision", "helper-dec/sess-1/ask-1/infer"),
		AssistantID:   "helper-dec",
		SessionID:     "sess-1",
		InteractionID: "ask-1",
		Source:        DecisionInfer,
		Confidence:    0.9,
		Candidates:    []string{"B"},
		Result:        "answered",
		CreatedAt:     now.Add(2 * time.Minute),
	}
	if _, err := store.RecordInteractionDecision(real); err != nil {
		t.Fatal(err)
	}
	latest, ok, err = store.LatestDecision("helper-dec", "sess-1", "ask-1")
	if err != nil || !ok || latest.Source != DecisionInfer {
		t.Fatalf("latest = %+v ok=%v err=%v", latest, ok, err)
	}

	// Unrelated interactions do not leak into the lookup.
	if _, ok, err := store.LatestDecision("helper-dec", "sess-1", "ask-2"); err != nil || ok {
		t.Fatalf("unrelated interaction ok=%v err=%v", ok, err)
	}
}
