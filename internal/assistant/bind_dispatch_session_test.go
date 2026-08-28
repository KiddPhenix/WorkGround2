package assistant

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreBindDispatchSessionIdempotent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	dispatch, err := store.OpenDispatch(OpenDispatchInput{AssistantID: "helper-a", RequestID: "d-1", Input: "ship it", Now: testEpoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClassifyDispatch(ClassifyDispatchInput{
		AssistantID: "helper-a", DispatchID: dispatch.ID, RequestID: "c-1",
		Kind: DispatchTask, Reply: "ok", Now: testEpoch,
	}); err != nil {
		t.Fatal(err)
	}

	bound, err := store.BindDispatchSession(BindDispatchSessionInput{
		RequestID: "bind-1", AssistantID: "helper-a", DispatchID: dispatch.ID,
		SessionID: "session-1", Now: testEpoch,
	})
	if err != nil || bound.SessionID != "session-1" {
		t.Fatalf("bind = %+v err=%v", bound, err)
	}

	// Replay is idempotent.
	again, err := store.BindDispatchSession(BindDispatchSessionInput{
		RequestID: "bind-1", AssistantID: "helper-a", DispatchID: dispatch.ID,
		SessionID: "session-1", Now: testEpoch.Add(time.Second),
	})
	if err != nil || again.SessionID != "session-1" {
		t.Fatalf("replay = %+v err=%v", again, err)
	}

	snapshot, _ := store.Get("helper-a")
	if snapshot.Dispatches[0].SessionID != "session-1" {
		t.Fatalf("dispatch session = %q", snapshot.Dispatches[0].SessionID)
	}
}
