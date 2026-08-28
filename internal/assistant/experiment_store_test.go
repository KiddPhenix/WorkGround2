package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRecordExperimentIdempotentAndCAS(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	exp := Experiment{
		ID: "exp-1", AssistantID: "helper-a", Hypothesis: "subject line A converts better",
		Isolation: "worktree", Metric: "click-through", Status: ExperimentRunning,
	}
	first, err := store.RecordExperiment(RecordExperimentInput{RequestID: "exp-1", Experiment: exp, Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 {
		t.Fatalf("first revision = %d", first.Revision)
	}

	// Replay is idempotent.
	again, err := store.RecordExperiment(RecordExperimentInput{RequestID: "exp-1", Experiment: exp, Now: testEpoch.Add(time.Second)})
	if err != nil || again.Revision != 1 {
		t.Fatalf("replay = %+v err=%v", again, err)
	}

	// Conclude under CAS.
	exp.Conclusion = "A won"
	exp.Status = ExperimentConcluded
	concluded, err := store.RecordExperiment(RecordExperimentInput{RequestID: "exp-2", Experiment: exp, ExpectedRevision: 1, Now: testEpoch.Add(2 * time.Second)})
	if err != nil || concluded.Revision != 2 || concluded.Status != ExperimentConcluded {
		t.Fatalf("conclude = %+v err=%v", concluded, err)
	}

	// Stale revision rejected.
	if _, err := store.RecordExperiment(RecordExperimentInput{RequestID: "exp-3", Experiment: exp, ExpectedRevision: 1, Now: testEpoch}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale error = %v, want ErrConflict", err)
	}

	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Experiments) != 1 || snapshot.Experiments[0].Status != ExperimentConcluded {
		t.Fatalf("experiments = %+v", snapshot.Experiments)
	}
}
