package assistant

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreDeleteRoutineIdempotentAndCAS(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	// Add a second routine so deleting one leaves the other untouched.
	if _, err := store.PutRoutine(RoutineInput{
		RequestID: "put-2",
		Routine: Routine{
			ID: "routine-b", AssistantID: "helper-a", Title: "Second",
			Prompt: "do the other thing", Schedule: Schedule{Kind: ScheduleManual},
			Enabled: true, CatchUp: CatchUpSkip,
		},
		Now: testEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	deleted, err := store.DeleteRoutine(DeleteRoutineInput{
		RequestID: "del-1", AssistantID: "helper-a", RoutineID: "routine-b",
		ExpectedRevision: 1, Now: testEpoch,
	})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ID != "routine-b" {
		t.Fatalf("deleted = %+v", deleted)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Routines) != 1 || snapshot.Routines[0].ID != "routine-a" {
		t.Fatalf("routines after delete = %+v", snapshot.Routines)
	}

	// Replay is idempotent.
	again, err := store.DeleteRoutine(DeleteRoutineInput{
		RequestID: "del-1", AssistantID: "helper-a", RoutineID: "routine-b",
		ExpectedRevision: 1, Now: testEpoch.Add(time.Second),
	})
	if err != nil || again.ID != "routine-b" {
		t.Fatalf("replay = %+v err=%v", again, err)
	}

	// A new request for an already-missing routine reports not found.
	if _, err := store.DeleteRoutine(DeleteRoutineInput{
		RequestID: "del-2", AssistantID: "helper-a", RoutineID: "routine-b",
		ExpectedRevision: 1, Now: testEpoch,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing error = %v, want ErrNotFound", err)
	}
}

func TestStoreDeleteRoutineRejectsStaleRevision(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if _, err := store.DeleteRoutine(DeleteRoutineInput{
		RequestID: "del-stale", AssistantID: "helper-a", RoutineID: "routine-a",
		ExpectedRevision: 99, Now: testEpoch,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete error = %v, want ErrConflict", err)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Routines) != 1 {
		t.Fatalf("stale delete removed a routine: %+v", snapshot.Routines)
	}
}

func TestStoreRunNowIsIdempotent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	first, err := store.RunNow(RunNowInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "fire-1", Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	// Replay (double-click / lost response / leader switch) returns the same Run.
	again, err := store.RunNow(RunNowInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "fire-1", Now: testEpoch.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID {
		t.Fatalf("run_now replay created a second run: %s vs %s", first.ID, again.ID)
	}
	snapshot, _ := store.Get("helper-a")
	if len(snapshot.Runs) != 1 {
		t.Fatalf("run_now replay created %d runs", len(snapshot.Runs))
	}

	// A distinct fire is a distinct Run.
	third, err := store.RunNow(RunNowInput{AssistantID: "helper-a", RoutineID: "routine-a", RequestID: "fire-2", Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatalf("distinct fire reused the same run ID %s", third.ID)
	}
}
