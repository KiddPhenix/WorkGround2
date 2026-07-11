package control

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/event"
	"workground2/internal/store"
)

func TestTaskMemoryRevisionDedupAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	c := New(Options{SessionPath: path, Sink: event.Discard})
	c.updateTaskMemory(taskMemoryPatch{goal: stringPtr("ship it"), goalSource: stringPtr("explicit_goal")})
	first := c.TaskMemory()
	if first.Revision != 1 || first.Goal != "ship it" {
		t.Fatalf("first memory = %+v", first)
	}
	c.updateTaskMemory(taskMemoryPatch{goal: stringPtr("ship it"), goalSource: stringPtr("explicit_goal")})
	if got := c.TaskMemory().Revision; got != first.Revision {
		t.Fatalf("duplicate revision = %d, want %d", got, first.Revision)
	}

	restored := New(Options{SessionPath: path, Sink: event.Discard}).TaskMemory()
	if restored.Revision != first.Revision || restored.Goal != first.Goal {
		t.Fatalf("restored = %+v, want %+v", restored, first)
	}
}

func TestTaskMemoryCorruptSidecarDoesNotBlockSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(store.SessionTaskMemory(path), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := New(Options{SessionPath: path, Sink: event.Discard})
	if got := c.TaskMemory(); got.Goal != "" || got.Revision != 0 {
		t.Fatalf("corrupt memory should recover empty, got %+v", got)
	}
}

func TestTaskMemoryPromptIsBoundedAndSessionScoped(t *testing.T) {
	c := New(Options{Sink: event.Discard})
	c.captureTaskGoal("  inspect   the project  ")
	got := c.TaskMemory()
	if got.Goal != "inspect the project" || got.GoalSource != "user_prompt" {
		t.Fatalf("prompt memory = %+v", got)
	}
	c.clearTaskMemory()
	if got := c.TaskMemory(); got.Goal != "" || got.Revision <= 1 {
		t.Fatalf("cleared memory = %+v", got)
	}
}

func TestTaskMemorySessionSwitchResetsRevisionNamespace(t *testing.T) {
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.jsonl")
	secondPath := filepath.Join(dir, "second.jsonl")
	c := New(Options{SessionPath: firstPath, Sink: event.Discard})
	c.updateTaskMemory(taskMemoryPatch{goal: stringPtr("first"), goalSource: stringPtr("user_prompt")})
	if got := c.TaskMemory(); got.SessionKey != "first" || got.Revision != 1 {
		t.Fatalf("first namespace = %+v", got)
	}
	c.SetSessionPath(secondPath)
	if got := c.TaskMemory(); got.SessionKey != "second" || got.Revision != 0 || got.Goal != "" {
		t.Fatalf("second namespace = %+v", got)
	}
}
