package assistanttool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/assistant"
	"workground2/internal/tool"
)

var testNow = time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)

func newTestStore(t *testing.T) *assistant.Store {
	t.Helper()
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Create(assistant.CreateInput{
		RequestID: "create-1",
		Assistant: assistant.Assistant{
			ID: "helper-a", Name: "helper", Mission: "keep the project healthy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: testNow,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store
}

func execTool(t *testing.T, tl tool.Tool, args map[string]any) scheduleResult {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s Execute: %v", tl.Name(), err)
	}
	var res scheduleResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%s returned invalid JSON %q: %v", tl.Name(), out, err)
	}
	return res
}

func TestScheduleToolsLifecycle(t *testing.T) {
	store := newTestStore(t)
	id := "helper-a"

	create := NewScheduleCreateTool(store, id)
	created := execTool(t, create, map[string]any{
		"routine_id": "daily-check",
		"title":      "Daily build check",
		"prompt":     "Run the build and report failures",
		"schedule":   map[string]any{"kind": "daily", "at": "09:00", "timezone": "Asia/Shanghai"},
		"enabled":    true,
		"request_id": "create-daily",
	})
	if created.Status != "accepted" || created.Routine == nil || created.Revision != 1 {
		t.Fatalf("create = %+v", created)
	}

	list := NewScheduleListTool(store, id)
	listed := execTool(t, list, map[string]any{})
	if len(listed.Routines) != 1 || listed.Routines[0].ID != "daily-check" {
		t.Fatalf("list = %+v", listed)
	}

	// Replay of create is idempotent: same routine, no duplicate.
	replayed := execTool(t, create, map[string]any{
		"routine_id": "daily-check",
		"title":      "Daily build check",
		"prompt":     "Run the build and report failures",
		"schedule":   map[string]any{"kind": "daily", "at": "09:00", "timezone": "Asia/Shanghai"},
		"enabled":    true,
		"request_id": "create-daily",
	})
	if replayed.Revision != 1 {
		t.Fatalf("replay = %+v, want unchanged revision 1", replayed)
	}

	// Pause then resume.
	pause := NewSchedulePauseTool(store, id)
	paused := execTool(t, pause, map[string]any{"routine_id": "daily-check", "expected_revision": 1, "request_id": "pause-1"})
	if paused.Status != "accepted" || paused.Routine == nil || paused.Routine.Enabled {
		t.Fatalf("pause = %+v", paused)
	}
	resume := NewScheduleResumeTool(store, id)
	resumed := execTool(t, resume, map[string]any{"routine_id": "daily-check", "expected_revision": paused.Revision, "request_id": "resume-1"})
	if !resumed.Routine.Enabled {
		t.Fatalf("resume = %+v", resumed)
	}

	// run_now is idempotent.
	runNow := NewScheduleRunNowTool(store, id)
	first := execTool(t, runNow, map[string]any{"routine_id": "daily-check", "request_id": "fire-1"})
	second := execTool(t, runNow, map[string]any{"routine_id": "daily-check", "request_id": "fire-1"})
	if first.Run == nil || first.Run.ID != second.Run.ID {
		t.Fatalf("run_now replay = %+v vs %+v", first, second)
	}

	// delete.
	del := NewScheduleDeleteTool(store, id)
	deleted := execTool(t, del, map[string]any{"routine_id": "daily-check", "expected_revision": resumed.Revision, "request_id": "delete-1"})
	if deleted.Status != "accepted" {
		t.Fatalf("delete = %+v", deleted)
	}
	after := execTool(t, list, map[string]any{})
	if len(after.Routines) != 0 {
		t.Fatalf("routines after delete = %+v", after.Routines)
	}
}

func TestScheduleUpdateRejectsStaleRevision(t *testing.T) {
	store := newTestStore(t)
	id := "helper-a"
	create := NewScheduleCreateTool(store, id)
	execTool(t, create, map[string]any{
		"routine_id": "r1", "title": "t", "prompt": "p",
		"schedule": map[string]any{"kind": "manual"}, "request_id": "c1",
	})
	update := NewScheduleUpdateTool(store, id)
	stale := execTool(t, update, map[string]any{
		"routine_id": "r1", "title": "new", "prompt": "p",
		"schedule":          map[string]any{"kind": "manual"},
		"expected_revision": 99, "request_id": "u1",
	})
	if stale.Status != "stale" {
		t.Fatalf("stale update = %+v, want status stale", stale)
	}
}
