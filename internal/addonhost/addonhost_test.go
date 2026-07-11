package addonhost

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHostStorage(t *testing.T) {
	dir := t.TempDir()
	ctx := HostContext{
		AddOnName:    "test-addon",
		AddOnVersion: "0.1.0",
		Namespace:    "test-addon",
		Home:         dir,
	}
	h := New(ctx, nil, nil)

	// Put → Get
	etag, err := h.StoragePut("settings", `{"theme":"dark"}`, "")
	if err != nil {
		t.Fatalf("StoragePut: %v", err)
	}
	if etag == "" {
		t.Fatal("expected non-empty etag")
	}

	val, gotEtag, err := h.StorageGet("settings")
	if err != nil {
		t.Fatalf("StorageGet: %v", err)
	}
	if val != `{"theme":"dark"}` {
		t.Fatalf("StorageGet value = %q", val)
	}
	if gotEtag != etag {
		t.Fatalf("etag mismatch: got %q want %q", gotEtag, etag)
	}

	// Conditional Put with matching etag → success
	newEtag, err := h.StoragePut("settings", `{"theme":"light"}`, etag)
	if err != nil {
		t.Fatalf("conditional StoragePut: %v", err)
	}
	if newEtag == etag {
		t.Fatal("expected new etag after conditional put")
	}

	// Conditional Put with stale etag → conflict
	_, err = h.StoragePut("settings", `{"theme":"blue"}`, etag) // old etag
	if err != ErrConflict {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// Patch
	patchEtag, err := h.StoragePatch("settings", `{"language":"en"}`, newEtag)
	if err != nil {
		t.Fatalf("StoragePatch: %v", err)
	}
	val, _, err = h.StorageGet("settings")
	if err != nil {
		t.Fatalf("StorageGet after patch: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(val), &parsed); err != nil {
		t.Fatalf("unmarshal patched value: %v", err)
	}
	if parsed["theme"] != "light" || parsed["language"] != "en" {
		t.Fatalf("patched value = %+v", parsed)
	}
	_ = patchEtag

	// Delete
	if err := h.StorageDelete("settings"); err != nil {
		t.Fatalf("StorageDelete: %v", err)
	}
	_, _, err = h.StorageGet("settings")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete is idempotent
	if err := h.StorageDelete("settings"); err != nil {
		t.Fatalf("idempotent StorageDelete: %v", err)
	}

	// List
	if _, err := h.StoragePut("item-a", `"a"`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.StoragePut("item-b", `"b"`, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.StoragePut("other", `"x"`, ""); err != nil {
		t.Fatal(err)
	}
	list, err := h.StorageList("item")
	if err != nil {
		t.Fatalf("StorageList: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("StorageList('item') = %d items, want 2", len(list))
	}

	// Get missing
	_, _, err = h.StorageGet("nonexistent")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing key, got %v", err)
	}

	// Etag is on disk
	etagPath := filepath.Join(h.storageDir(), "item-a.json")
	data, err := os.ReadFile(etagPath)
	if err != nil {
		t.Fatal(err)
	}
	var onDisk etagEntry
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Etag == "" {
		t.Fatal("etag not persisted on disk")
	}
}

func TestHostTasks(t *testing.T) {
	dir := t.TempDir()
	ctx := HostContext{
		AddOnName: "test-addon",
		Namespace: "test-addon",
		Home:      dir,
	}
	var events []EventPayload
	e := &testEmitter{fn: func(e EventPayload) { events = append(events, e) }}
	h := New(ctx, nil, e)

	// Start
	taskID, err := h.TasksStart(TaskStartInput{Kind: "sync", Title: "Syncing profiles"})
	if err != nil {
		t.Fatalf("TasksStart: %v", err)
	}
	if taskID == "" {
		t.Fatal("expected non-empty task id")
	}

	// Update
	if err := h.TasksUpdate(TaskUpdateInput{TaskID: taskID, Phase: "cloning", Progress: 50, Message: "Cloning repository..."}); err != nil {
		t.Fatalf("TasksUpdate: %v", err)
	}

	task, err := h.Task(taskID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if task.Phase != "cloning" {
		t.Fatalf("Task.Phase = %q, want cloning", task.Phase)
	}
	if task.Progress != 50 {
		t.Fatalf("Task.Progress = %d, want 50", task.Progress)
	}
	if task.Status != TaskRunning {
		t.Fatalf("Task.Status = %q, want %q", task.Status, TaskRunning)
	}

	// Finish (success)
	if err := h.TasksFinish(taskID, TaskSucceeded, "sync complete", ""); err != nil {
		t.Fatalf("TasksFinish: %v", err)
	}
	task, err = h.Task(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.Status != TaskSucceeded {
		t.Fatalf("Task.Status = %q, want succeeded", task.Status)
	}

	// Cancelled
	taskID2, _ := h.TasksStart(TaskStartInput{Kind: "generate", Title: "Generating image"})
	if err := h.TasksCancelled(taskID2); err != nil {
		t.Fatalf("TasksCancelled: %v", err)
	}
	task2, _ := h.Task(taskID2)
	if task2.Error != "cancelled" {
		t.Fatalf("cancelled task error = %q, want cancelled", task2.Error)
	}

	// Events
	if len(events) < 4 {
		t.Fatalf("expected at least 4 events, got %d", len(events))
	}

	// Tasks list
	all := h.Tasks()
	if len(all) != 2 {
		t.Fatalf("Tasks() = %d, want 2", len(all))
	}
}

func TestHostEvents(t *testing.T) {
	dir := t.TempDir()
	ctx := HostContext{AddOnName: "evt-addon", Namespace: "evt-addon", Home: dir}
	var events []EventPayload
	e := &testEmitter{fn: func(e EventPayload) { events = append(events, e) }}
	h := New(ctx, nil, e)

	h.RecordsChanged("evt-addon/profiles.json")
	h.TaskChanged("evt-addon-1")
	h.SkillsChanged()
	h.ToolsChanged()

	if len(events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(events))
	}
	if events[0].Kind != EventRecordsChanged {
		t.Fatalf("event[0].Kind = %q, want %q", events[0].Kind, EventRecordsChanged)
	}
	if events[1].Kind != EventTaskChanged {
		t.Fatalf("event[1].Kind = %q, want %q", events[1].Kind, EventTaskChanged)
	}
	if events[2].Kind != EventSkillsChanged {
		t.Fatalf("event[2].Kind = %q", events[2].Kind)
	}
	if events[3].Kind != EventToolsChanged {
		t.Fatalf("event[3].Kind = %q", events[3].Kind)
	}
}

type testEmitter struct {
	fn func(EventPayload)
}

func (e *testEmitter) Emit(p EventPayload) {
	if e.fn != nil {
		e.fn(p)
	}
}
