package session

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/store"
)

func TestArtifactsIncludesTaskMemory(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "test.jsonl")
	want := store.SessionTaskMemory(sessionPath)
	for _, artifact := range Artifacts(sessionPath, filepath.Base(sessionPath)) {
		if artifact.Src == want && artifact.Name == "test.task-memory.json" {
			return
		}
	}
	t.Fatalf("Artifacts() missing task-memory sidecar %q", want)
}

func TestArtifactsIncludesPendingAsk(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "test.jsonl")
	want := store.SessionPendingAsk(sessionPath)
	for _, artifact := range Artifacts(sessionPath, filepath.Base(sessionPath)) {
		if artifact.Src == want && artifact.Name == "test.pending-ask.json" {
			return
		}
	}
	t.Fatalf("Artifacts() missing pending-ask sidecar %q", want)
}

func TestTrashRestoreMovesPendingAsk(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "test.jsonl")
	pendingAskPath := store.SessionPendingAsk(sessionPath)
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.WriteFile(pendingAskPath, []byte(`{"id":"7","questions":[]}`), 0o600); err != nil {
		t.Fatalf("write pending ask: %v", err)
	}

	if err := TrashSession(dir, sessionPath); err != nil {
		t.Fatalf("TrashSession: %v", err)
	}
	trashedPath := filepath.Join(TrashPath(dir), filepath.Base(sessionPath), filepath.Base(sessionPath))
	trashedPendingAsk := filepath.Join(filepath.Dir(trashedPath), "test.pending-ask.json")
	if _, err := os.Stat(pendingAskPath); !os.IsNotExist(err) {
		t.Fatalf("live pending ask still exists after trash: %v", err)
	}
	if _, err := os.Stat(trashedPendingAsk); err != nil {
		t.Fatalf("trashed pending ask missing: %v", err)
	}

	if err := RestoreTrashedSession(dir, trashedPath); err != nil {
		t.Fatalf("RestoreTrashedSession: %v", err)
	}
	if _, err := os.Stat(pendingAskPath); err != nil {
		t.Fatalf("restored pending ask missing: %v", err)
	}
}

func TestTrashRestoreMovesTaskMemory(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "test.jsonl")
	taskMemoryPath := store.SessionTaskMemory(sessionPath)
	if err := os.WriteFile(sessionPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := os.WriteFile(taskMemoryPath, []byte(`{"revision":1}`), 0o600); err != nil {
		t.Fatalf("write task memory: %v", err)
	}

	if err := TrashSession(dir, sessionPath); err != nil {
		t.Fatalf("TrashSession: %v", err)
	}
	trashedPath := filepath.Join(TrashPath(dir), filepath.Base(sessionPath), filepath.Base(sessionPath))
	trashedTaskMemory := filepath.Join(filepath.Dir(trashedPath), "test.task-memory.json")
	if _, err := os.Stat(taskMemoryPath); !os.IsNotExist(err) {
		t.Fatalf("live task memory still exists after trash: %v", err)
	}
	if _, err := os.Stat(trashedTaskMemory); err != nil {
		t.Fatalf("trashed task memory missing: %v", err)
	}

	if err := RestoreTrashedSession(dir, trashedPath); err != nil {
		t.Fatalf("RestoreTrashedSession: %v", err)
	}
	if _, err := os.Stat(taskMemoryPath); err != nil {
		t.Fatalf("restored task memory missing: %v", err)
	}
}
