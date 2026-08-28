package assistant

import (
	"testing"
	"time"
)

func TestDueRoutineFiresIdempotentAndCreatesNoRun(t *testing.T) {
	t.Parallel()
	store := newFireTestStore(t, CatchUpCoalesceLatest)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	now := start.Add(3*time.Hour + time.Minute)

	fires, err := store.DueRoutineFires(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || !fires[0].ScheduledFor.Equal(start.Add(3*time.Hour)) {
		t.Fatalf("fires = %#v", fires)
	}
	if fires[0].FireID == "" || fires[0].Prompt != "Scan now" || fires[0].Title != "Scan" {
		t.Fatalf("fire identity incomplete: %#v", fires[0])
	}
	if fires[0].State != FirePending {
		t.Fatalf("fire state = %s, want pending", fires[0].State)
	}
	expectedKey := OccurrenceKey("ast-fire", "rtn-fire", start.Add(3*time.Hour))
	if fires[0].FireID != StableID("fire", expectedKey) || fires[0].OccurrenceKey != expectedKey {
		t.Fatalf("fire key mismatch: %#v", fires[0])
	}

	// Replay at the same instant must return the same pending fire, not a
	// second one.
	again, err := store.DueRoutineFires(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].FireID != fires[0].FireID {
		t.Fatalf("replay produced duplicate fires: %#v", again)
	}

	// The fire path must never create a Run.
	snapshot, err := store.Get("ast-fire")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 0 {
		t.Fatalf("DueRoutineFires created %d Runs", len(snapshot.Runs))
	}
	if len(snapshot.Fires) != 1 || snapshot.Fires[0].State != FirePending {
		t.Fatalf("durable fires = %#v", snapshot.Fires)
	}
}

// TestDueRoutineFiresCrashBeforeSessionCreate models a crash after the fire is
// recorded but before a Session is created: a fresh Store over the same root
// must still return the pending fire so it can be compensated.
func TestDueRoutineFiresCrashBeforeSessionCreate(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	seedFireTestStoreAt(t, root, start, CatchUpCoalesceLatest)
	now := start.Add(2*time.Hour + time.Minute)

	first, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	fires, err := first.DueRoutineFires(now)
	if err != nil || len(fires) != 1 {
		t.Fatalf("first pass fires=%#v err=%v", fires, err)
	}
	fireID := fires[0].FireID

	// A fresh Store over the same root models a process restart before the
	// Session was created: the durable fire must come back as pending.
	second, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.DueRoutineFires(now)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 || again[0].FireID != fireID || again[0].State != FirePending {
		t.Fatalf("restart replay lost or duplicated the fire: %#v", again)
	}
	snapshot, err := second.Get("ast-fire")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Runs) != 0 {
		t.Fatalf("fire ledger created %d Runs", len(snapshot.Runs))
	}
}

// TestDueRoutineFiresCrashBeforeBindRetriesOnce models a crash after the
// Session was created but before the fire was bound: the fire stays pending, so
// the retry re-uses the fire-derived request ID and ends with a single binding.
func TestDueRoutineFiresCrashBeforeBindRetriesOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	seedFireTestStoreAt(t, root, start, CatchUpCoalesceLatest)
	now := start.Add(2*time.Hour + time.Minute)

	first, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	fires, err := first.DueRoutineFires(now)
	if err != nil || len(fires) != 1 {
		t.Fatalf("fires=%#v err=%v", fires, err)
	}
	fire := fires[0]
	requestID := StableID("request", "routine-fire/"+fire.FireID)

	// Crash after Session create, before bind: a new process still sees the
	// pending fire and binds it once.
	second, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	again, err := second.DueRoutineFires(now)
	if err != nil || len(again) != 1 || again[0].State != FirePending {
		t.Fatalf("pending fire not replayed: %#v err=%v", again, err)
	}
	bound, err := second.ConsumeRoutineFire(fire.AssistantID, fire.FireID, "session-1", requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != FireConsumed || bound.SessionID != "session-1" {
		t.Fatalf("bound = %#v", bound)
	}
	// Replaying the same request ID returns the same binding, so the fire_id
	// idempotency yields exactly one Session.
	replay, err := second.ConsumeRoutineFire(fire.AssistantID, fire.FireID, "session-1", requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if replay.SessionID != "session-1" {
		t.Fatalf("replay rebinding = %#v", replay)
	}
	if remaining, err := second.DueRoutineFires(now); err != nil || len(remaining) != 0 {
		t.Fatalf("consumed fire still pending: %#v err=%v", remaining, err)
	}
}

func TestConsumeRoutineFireIdempotent(t *testing.T) {
	t.Parallel()
	store := newFireTestStore(t, CatchUpCoalesceLatest)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	now := start.Add(3*time.Hour + time.Minute)
	fires, err := store.DueRoutineFires(now)
	if err != nil || len(fires) != 1 {
		t.Fatalf("fires=%#v err=%v", fires, err)
	}
	fire := fires[0]
	requestID := StableID("request", "routine-fire/"+fire.FireID)

	consumed, err := store.ConsumeRoutineFire(fire.AssistantID, fire.FireID, "session-1", requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if consumed.State != FireConsumed || consumed.SessionID != "session-1" || consumed.FireID != fire.FireID {
		t.Fatalf("consumed = %#v", consumed)
	}
	if remaining, err := store.DueRoutineFires(now); err != nil || len(remaining) != 0 {
		t.Fatalf("consumed fire still pending: %#v err=%v", remaining, err)
	}

	// Lost-response replay: the same request ID returns the same result.
	replay, err := store.ConsumeRoutineFire(fire.AssistantID, fire.FireID, "session-1", requestID, now)
	if err != nil {
		t.Fatal(err)
	}
	if replay.SessionID != "session-1" {
		t.Fatalf("replay = %#v", replay)
	}
	// A repeated consume with a different request ID is still idempotent: the
	// first binding wins.
	other, err := store.ConsumeRoutineFire(fire.AssistantID, fire.FireID, "session-2", "request-2", now)
	if err != nil {
		t.Fatal(err)
	}
	if other.SessionID != "session-1" {
		t.Fatalf("re-consume rebound the fire: %#v", other)
	}

	snapshot, err := store.Get("ast-fire")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Fires) != 1 || snapshot.Fires[0].State != FireConsumed || snapshot.Fires[0].SessionID != "session-1" {
		t.Fatalf("durable fires = %#v", snapshot.Fires)
	}
}

// TestDueRoutineFiresKeepsPendingAfterRoutineChange verifies that editing,
// pausing or deleting a routine only affects the future: an already-fired,
// still-pending fire keeps coming back so it can be compensated.
func TestDueRoutineFiresKeepsPendingAfterRoutineChange(t *testing.T) {
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	now := start.Add(2*time.Hour + time.Minute)

	t.Run("modify", func(t *testing.T) {
		store := newFireTestStore(t, CatchUpCoalesceLatest)
		fire := dueFire(t, store, now)
		snapshot, err := store.Get("ast-fire")
		if err != nil {
			t.Fatal(err)
		}
		r := snapshot.Routines[0]
		r.Title = "Renamed"
		r.Prompt = "Renamed prompt"
		if _, err := store.PutRoutine(RoutineInput{RequestID: "modify", Routine: r, ExpectedRevision: r.Revision, Now: now}); err != nil {
			t.Fatal(err)
		}
		again, err := store.DueRoutineFires(now)
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != 1 || again[0].FireID != fire.FireID || again[0].State != FirePending {
			t.Fatalf("pending fire lost after modify: %#v", again)
		}
		if again[0].Title != "Scan" || again[0].Prompt != "Scan now" {
			t.Fatalf("fired intent was mutated by a later edit: %#v", again[0])
		}
	})

	t.Run("pause", func(t *testing.T) {
		store := newFireTestStore(t, CatchUpCoalesceLatest)
		fire := dueFire(t, store, now)
		snapshot, err := store.Get("ast-fire")
		if err != nil {
			t.Fatal(err)
		}
		r := snapshot.Routines[0]
		r.Enabled = false
		if _, err := store.PutRoutine(RoutineInput{RequestID: "pause", Routine: r, ExpectedRevision: r.Revision, Now: now}); err != nil {
			t.Fatal(err)
		}
		assertPendingFire(t, store, now, fire.FireID)
	})

	t.Run("delete", func(t *testing.T) {
		store := newFireTestStore(t, CatchUpCoalesceLatest)
		fire := dueFire(t, store, now)
		snapshot, err := store.Get("ast-fire")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.DeleteRoutine(DeleteRoutineInput{
			RequestID: "delete", AssistantID: "ast-fire", RoutineID: "rtn-fire",
			ExpectedRevision: snapshot.Routines[0].Revision, Now: now,
		}); err != nil {
			t.Fatal(err)
		}
		assertPendingFire(t, store, now, fire.FireID)
	})
}

func TestDueRoutineFiresSkipAdvancesCursorWithoutFire(t *testing.T) {
	t.Parallel()
	store := newFireTestStore(t, CatchUpSkip)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	fires, err := store.DueRoutineFires(start.Add(3*time.Hour + time.Minute))
	if err != nil || len(fires) != 0 {
		t.Fatalf("skip produced fires=%#v err=%v", fires, err)
	}
	snapshot, err := store.Get("ast-fire")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Routines[0].LastScheduledFor.Equal(start.Add(3 * time.Hour)) {
		t.Fatalf("skip cursor = %s", snapshot.Routines[0].LastScheduledFor)
	}
}

func dueFire(t *testing.T, store *Store, now time.Time) RoutineFire {
	t.Helper()
	fires, err := store.DueRoutineFires(now)
	if err != nil || len(fires) != 1 {
		t.Fatalf("DueRoutineFires(%s) = %#v, %v", now, fires, err)
	}
	if fires[0].State != FirePending {
		t.Fatalf("fire state = %s, want pending", fires[0].State)
	}
	return fires[0]
}

func assertPendingFire(t *testing.T, store *Store, now time.Time, fireID string) {
	t.Helper()
	fires, err := store.DueRoutineFires(now)
	if err != nil {
		t.Fatalf("DueRoutineFires: %v", err)
	}
	for _, fire := range fires {
		if fire.FireID == fireID {
			if fire.State != FirePending {
				t.Fatalf("fire %s state = %s, want pending", fireID, fire.State)
			}
			return
		}
	}
	t.Fatalf("fire %s was not returned as pending", fireID)
}

func newFireTestStore(t *testing.T, catchUp CatchUpPolicy) *Store {
	t.Helper()
	store := seedFireTestStoreAt(t, t.TempDir(), time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC), catchUp)
	return store
}

func seedFireTestStoreAt(t *testing.T, root string, start time.Time, catchUp CatchUpPolicy) *Store {
	t.Helper()
	store, err := NewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(CreateInput{
		RequestID: "create-fire",
		Assistant: Assistant{
			ID: "ast-fire", Name: "Fire", Mission: "Keep scanning",
			Scope: ScopeGlobal, Lifecycle: LifecycleActive, Policy: DefaultPolicy(),
		},
		Routines: []Routine{{
			ID: "rtn-fire", AssistantID: "ast-fire", Title: "Scan", Prompt: "Scan now",
			Enabled: true, CatchUp: catchUp, Schedule: Schedule{Kind: ScheduleInterval, IntervalSeconds: 3600},
		}},
		Now: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	return store
}
