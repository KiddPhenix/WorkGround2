package assistant

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreSessionCreationIdempotent(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")

	if _, ok, err := store.ResolveSessionCreation("helper-a", "create-1"); err != nil || ok {
		t.Fatalf("no binding should exist yet: ok=%v err=%v", ok, err)
	}

	first, err := store.RecordSessionCreation("helper-a", "create-1", "session-a")
	if err != nil || first != "session-a" {
		t.Fatalf("first = %q err=%v", first, err)
	}
	// Replay returns the same Session ID.
	again, err := store.RecordSessionCreation("helper-a", "create-1", "session-a")
	if err != nil || again != "session-a" {
		t.Fatalf("replay = %q err=%v", again, err)
	}
	if resolved, ok, err := store.ResolveSessionCreation("helper-a", "create-1"); err != nil || !ok || resolved != "session-a" {
		t.Fatalf("resolve = %q ok=%v err=%v", resolved, ok, err)
	}

	// Reusing the same request ID with a different Session ID is first-wins:
	// the original binding is returned, so a racing second create never leaks a
	// duplicate Session.
	if got, err := store.RecordSessionCreation("helper-a", "create-1", "session-b"); err != nil || got != "session-a" {
		t.Fatalf("first-wins = %q err=%v, want session-a", got, err)
	}
}

func TestStoreSessionCreationRequiresRequestID(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	if _, err := store.RecordSessionCreation("helper-a", " ", "session-a"); err == nil {
		t.Fatal("empty request id should be rejected")
	}
	_ = time.Second
}
