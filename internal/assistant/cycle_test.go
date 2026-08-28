package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSupervisorCycleLifecycle(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	now := testEpoch

	if _, ok := store.LatestCycle("helper-a"); ok {
		t.Fatal("no cycle should exist before OpenCycle")
	}

	first, err := store.OpenCycle(OpenCycleInput{
		AssistantID: "helper-a", RequestID: "open-1",
		Observed: CycleObservation{PlanRevision: 1, AssistantRevision: 1, MemoryRevision: 1, WorkEpoch: 1},
		Now:      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Fence != 1 || first.State != CycleStarted {
		t.Fatalf("first cycle = %+v", first)
	}

	// Replay is idempotent.
	again, err := store.OpenCycle(OpenCycleInput{
		AssistantID: "helper-a", RequestID: "open-1",
		Observed: CycleObservation{PlanRevision: 1, AssistantRevision: 1, MemoryRevision: 1, WorkEpoch: 1},
		Now:      now.Add(time.Second),
	})
	if err != nil || again.Fence != 1 || again.ID != first.ID {
		t.Fatalf("replay = %+v err=%v", again, err)
	}

	cp, err := store.CheckpointCycle(CheckpointCycleInput{
		AssistantID: "helper-a", CycleID: first.ID, RequestID: "cp-1",
		Fence: first.Fence, NextStep: "advance responsibility A", Now: now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if cp.State != CycleCheckpointed || cp.NextStep != "advance responsibility A" {
		t.Fatalf("checkpoint = %+v", cp)
	}

	// A stale fence cannot checkpoint a newer cycle.
	second, err := store.OpenCycle(OpenCycleInput{
		AssistantID: "helper-a", RequestID: "open-2",
		Observed: CycleObservation{PlanRevision: 2, AssistantRevision: 1, MemoryRevision: 1, WorkEpoch: 1},
		Now:      now.Add(3 * time.Second),
	})
	if err != nil || second.Fence != 2 {
		t.Fatalf("second cycle = %+v err=%v", second, err)
	}
	if _, err := store.CheckpointCycle(CheckpointCycleInput{
		AssistantID: "helper-a", CycleID: first.ID, RequestID: "cp-stale",
		Fence: first.Fence, NextStep: "stale", Now: now.Add(4 * time.Second),
	}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale checkpoint error = %v, want ErrLeaseLost", err)
	}

	done, err := store.CompleteCycle(CompleteCycleInput{
		AssistantID: "helper-a", CycleID: second.ID, RequestID: "done-1",
		Fence: second.Fence, Now: now.Add(5 * time.Second),
	})
	if err != nil || done.State != CycleCompleted {
		t.Fatalf("complete = %+v err=%v", done, err)
	}
	if latest, ok := store.LatestCycle("helper-a"); !ok || latest.ID != second.ID {
		t.Fatalf("latest = %+v ok=%v", latest, ok)
	}
}
