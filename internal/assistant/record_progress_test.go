package assistant

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreRecordProgressAppliesPlanIdempotently(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	in := RecordProgressInput{
		RequestID: "progress-1", AssistantID: "helper-a", SessionID: "s1",
		Progress: ProgressBlock{
			PlanRevision: 1, Responsibility: "scan",
			Responsibilities: []RespDecl{
				{Alias: "scan", Objective: "scan changes", DoneCriteria: "report written", NextAction: "run scan"},
			},
			Complete: []string{"scan"},
		},
		Now: testEpoch,
	}
	if err := store.RecordProgress(in); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := store.Get("helper-a")
	if snapshot.Plan.Revision != 2 || len(snapshot.Plan.Responsibilities) != 1 {
		t.Fatalf("plan = %+v", snapshot.Plan)
	}
	resp := snapshot.Plan.Responsibilities[0]
	if resp.Alias != "scan" || resp.Status != RespDone {
		t.Fatalf("responsibility = %+v", resp)
	}

	// Replay is idempotent.
	if err := store.RecordProgress(in); err != nil {
		t.Fatalf("replay: %v", err)
	}

	// Stale plan revision is rejected.
	stale := in
	stale.RequestID = "progress-2"
	stale.Progress.PlanRevision = 1
	if err := store.RecordProgress(stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale error = %v, want ErrConflict", err)
	}
}

func TestStoreRecordProgressRejectsInvalidBlock(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	// dependency on a missing alias is invalid.
	if err := store.RecordProgress(RecordProgressInput{
		RequestID: "p", AssistantID: "helper-a",
		Progress: ProgressBlock{
			Responsibilities: []RespDecl{{Alias: "x", Objective: "o", DependsOn: []string{"missing"}}},
		},
		Now: testEpoch,
	}); err == nil {
		t.Fatal("RecordProgress accepted a block referencing a missing dependency")
	}
}
