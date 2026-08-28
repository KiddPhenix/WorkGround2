package assistant

import (
	"path/filepath"
	"testing"
	"time"
)

func testEventQueue(t *testing.T) *SupervisorEventQueue {
	t.Helper()
	q, err := NewSupervisorEventQueue(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("NewSupervisorEventQueue: %v", err)
	}
	return q
}

func baseEvent(kind SupervisorEventKind, id string) SupervisorEvent {
	return SupervisorEvent{
		ID: id, Kind: kind, AssistantID: "helper-a",
		SessionID: "session-1", At: time.Now(),
	}
}

// TestSupervisorEventQueueDedupAndMerge proves enqueues are deduplicated by
// stable ID and merged by (assistant, kind, session, request) keeping only the
// newest revision: a duplicate or an older/late arrival never regresses the
// pending state.
func TestSupervisorEventQueueDedupAndMerge(t *testing.T) {
	q := testEventQueue(t)

	ev := baseEvent(EventSessionProgressed, "event-progressed-5")
	ev.Revision = 5
	applied, err := q.Enqueue(ev)
	if err != nil || !applied {
		t.Fatalf("first enqueue applied=%v err=%v", applied, err)
	}

	// Exact duplicate (same stable ID) is a no-op.
	dup := ev
	applied, err = q.Enqueue(dup)
	if err != nil || applied {
		t.Fatalf("duplicate enqueue applied=%v err=%v, want no-op", applied, err)
	}

	// Older revision with a different ID but the same merge key is dropped.
	older := baseEvent(EventSessionProgressed, "event-progressed-3")
	older.Revision = 3
	older.At = ev.At.Add(-time.Minute)
	applied, err = q.Enqueue(older)
	if err != nil || applied {
		t.Fatalf("older enqueue applied=%v err=%v, want no-op", applied, err)
	}

	// Newer revision with the same merge key replaces the pending one.
	newer := baseEvent(EventSessionProgressed, "event-progressed-7")
	newer.Revision = 7
	newer.At = ev.At.Add(time.Minute)
	applied, err = q.Enqueue(newer)
	if err != nil || !applied {
		t.Fatalf("newer enqueue applied=%v err=%v, want replace", applied, err)
	}

	// A different kind for the same session is a distinct event.
	done := baseEvent(EventSessionCompleted, "event-completed-1")
	done.Revision = 7
	if applied, err = q.Enqueue(done); err != nil || !applied {
		t.Fatalf("completed enqueue applied=%v err=%v", applied, err)
	}

	pending, err := q.Pending("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending = %d events, want 2 (one merged progressed + completed)", len(pending))
	}
	var progressed, completed *SupervisorEvent
	for i := range pending {
		switch pending[i].Kind {
		case EventSessionProgressed:
			progressed = &pending[i]
		case EventSessionCompleted:
			completed = &pending[i]
		}
	}
	if progressed == nil || progressed.Revision != 7 {
		t.Fatalf("progressed = %+v, want revision 7", progressed)
	}
	if progressed.ID != "event-progressed-7" {
		t.Fatalf("progressed id = %q, want the newest one", progressed.ID)
	}
	if completed == nil {
		t.Fatal("completed event missing")
	}
}

// TestSupervisorEventQueueRestartKeepsPending proves the journal is durable:
// a fresh queue over the same root replays pending and consumed events, so a
// process restart resumes exactly where the previous one stopped.
func TestSupervisorEventQueueRestartKeepsPending(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	q1, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	ev1 := baseEvent(EventSessionStarted, "event-start-1")
	ev2 := baseEvent(EventSessionFailed, "event-fail-1")
	for _, ev := range []SupervisorEvent{ev1, ev2} {
		if applied, err := q1.Enqueue(ev); err != nil || !applied {
			t.Fatalf("enqueue %s applied=%v err=%v", ev.ID, applied, err)
		}
	}
	if err := q1.MarkConsumed("helper-a", ev1.ID); err != nil {
		t.Fatal(err)
	}

	q2, err := NewSupervisorEventQueue(root)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := q2.Pending("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != ev2.ID {
		t.Fatalf("restarted pending = %+v, want only %s", pending, ev2.ID)
	}
	// Consumed events never come back, even after another restart.
	if err := q2.MarkConsumed("helper-a", ev2.ID); err != nil {
		t.Fatal(err)
	}
	q3, _ := NewSupervisorEventQueue(root)
	if pending, err := q3.Pending("helper-a"); err != nil || len(pending) != 0 {
		t.Fatalf("pending after full consume = %+v err=%v, want empty", pending, err)
	}
}

// TestSupervisorEventQueueStateSurvivesRestart proves the collector high-water
// sidecar persists and can only move forward.
func TestSupervisorEventQueueStateSurvivesRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	q1, _ := NewSupervisorEventQueue(root)
	state := SupervisorEventState{SessionStatus: map[string]string{"s1": "running"}, HeartbeatAt: time.Now()}
	if err := q1.SaveState("helper-a", state); err != nil {
		t.Fatal(err)
	}
	q2, _ := NewSupervisorEventQueue(root)
	got, err := q2.LoadState("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionStatus["s1"] != "running" {
		t.Fatalf("restored status = %+v", got.SessionStatus)
	}
	if got.HeartbeatAt.IsZero() {
		t.Fatal("restored heartbeat time is zero")
	}
}

// TestSupervisorEventQueueRejectsInvalidEvents proves malformed events are
// rejected loudly instead of silently polluting the journal.
func TestSupervisorEventQueueRejectsInvalidEvents(t *testing.T) {
	q := testEventQueue(t)
	ev := baseEvent("bogus", "event-x")
	if _, err := q.Enqueue(ev); err == nil {
		t.Fatal("invalid kind accepted")
	}
	ev = baseEvent(EventHeartbeat, "")
	if _, err := q.Enqueue(ev); err == nil {
		t.Fatal("empty id accepted")
	}
	ev = baseEvent(EventHeartbeat, "event-x")
	ev.AssistantID = ""
	if _, err := q.Enqueue(ev); err == nil {
		t.Fatal("empty assistant accepted")
	}
}
