package assistanttool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"workground2/internal/assistant"
	"workground2/internal/tool"
)

func newWorkspaceStore(t *testing.T, root string) *assistant.Store {
	t.Helper()
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Create(assistant.CreateInput{
		RequestID: "create-1",
		Assistant: assistant.Assistant{
			ID: "helper-ws", Name: "ws helper", Mission: "keep the project healthy",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: root,
			Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: testNow,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store
}

func execProjectTool(t *testing.T, tl tool.Tool, args map[string]any) projectResult {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s Execute: %v", tl.Name(), err)
	}
	var r projectResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%s returned invalid JSON %q: %v", tl.Name(), out, err)
	}
	return r
}

func TestProjectConstraintsLifecycle(t *testing.T) {
	ws := t.TempDir()
	store := newWorkspaceStore(t, ws)

	status := NewProjectStatusTool(store, "helper-ws")
	s := execProjectTool(t, status, nil)
	if s.Status != "accepted" || s.Workspace != ws {
		t.Fatalf("status = %+v", s)
	}

	get := NewProjectConstraintsGetTool(store, "helper-ws")
	g := execProjectTool(t, get, nil)
	if g.Status != "accepted" || g.Revision != 0 {
		t.Fatalf("get = %+v", g)
	}

	patch := NewProjectConstraintsPatchTool(store, "helper-ws")
	p := execProjectTool(t, patch, map[string]any{
		"constraints": []string{"use tabs", "no force push"}, "expected_revision": 0, "request_id": "p1",
	})
	if p.Status != "accepted" || p.Revision != 1 {
		t.Fatalf("patch = %+v", p)
	}

	g2 := execProjectTool(t, get, nil)
	if len(g2.Constraints) != 2 || g2.Revision != 1 {
		t.Fatalf("get after patch = %+v", g2)
	}

	// Stale revision rejected.
	stale := execProjectTool(t, patch, map[string]any{
		"constraints": []string{"x"}, "expected_revision": 0, "request_id": "p2",
	})
	if stale.Status != "stale" {
		t.Fatalf("stale patch = %+v", stale)
	}
}

func TestProjectConstraintsRequiresWorkspaceScope(t *testing.T) {
	// Global-scope assistant has no workspace -> explicit error, not a panic.
	store := newTestStore(t)
	get := NewProjectConstraintsGetTool(store, "helper-a")
	g := execProjectTool(t, get, nil)
	if g.Status != "invalid" && g.Status != "retryable_error" {
		t.Fatalf("global-scope get = %+v, want invalid/retryable_error", g)
	}
}

func newWorkspaceStoreWithPolicy(t *testing.T, root string, policy assistant.Policy) *assistant.Store {
	t.Helper()
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if _, err := store.Create(assistant.CreateInput{
		RequestID: "create-1",
		Assistant: assistant.Assistant{
			ID: "helper-ws", Name: "ws helper", Mission: "keep the project healthy",
			Scope: assistant.ScopeWorkspace, WorkspaceRoot: root,
			Lifecycle: assistant.LifecycleActive, Policy: policy,
		},
		Now: testNow,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return store
}

func TestProjectConstraintsUpdatePolicyDenied(t *testing.T) {
	ws := t.TempDir()
	policy := assistant.DefaultPolicy()
	policy.ConstraintEdit = assistant.AccessDeny
	store := newWorkspaceStoreWithPolicy(t, ws, policy)

	update := NewProjectConstraintsUpdateTool(store, "helper-ws")
	p := execProjectTool(t, update, map[string]any{
		"constraints": []string{"x"}, "expected_revision": 0, "request_id": "denied-1",
	})
	if p.Status != "policy_denied" {
		t.Fatalf("denied update = %+v, want policy_denied", p)
	}
}

func TestProjectConstraintsUpdateReplayAndConflict(t *testing.T) {
	ws := t.TempDir()
	store := newWorkspaceStore(t, ws)
	update := NewProjectConstraintsUpdateTool(store, "helper-ws")

	first := execProjectTool(t, update, map[string]any{
		"constraints": []string{"use tabs"}, "expected_revision": 0, "request_id": "r1",
	})
	if first.Status != "accepted" || first.Revision != 1 {
		t.Fatalf("first = %+v", first)
	}

	// Replay the same request_id -> already_applied with the prior result.
	replay := execProjectTool(t, update, map[string]any{
		"constraints": []string{"use tabs"}, "expected_revision": 0, "request_id": "r1",
	})
	if replay.Status != "already_applied" || replay.Revision != 1 {
		t.Fatalf("replay = %+v, want already_applied rev 1", replay)
	}

	// Same request_id, different input -> conflict (never a silent re-edit).
	conflict := execProjectTool(t, update, map[string]any{
		"constraints": []string{"different"}, "expected_revision": 0, "request_id": "r1",
	})
	if conflict.Status != "conflict" {
		t.Fatalf("conflict = %+v, want conflict", conflict)
	}
}

func TestProjectConstraintsUpdateToolName(t *testing.T) {
	ws := t.TempDir()
	store := newWorkspaceStore(t, ws)
	if got := NewProjectConstraintsUpdateTool(store, "helper-ws").Name(); got != "project_constraints_update" {
		t.Fatalf("update tool name = %q", got)
	}
	if got := NewProjectConstraintsPatchTool(store, "helper-ws").Name(); got != "project_constraints_patch" {
		t.Fatalf("patch alias name = %q", got)
	}
}

func TestProjectConstraintsRejectsEscapingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges on Windows")
	}
	ws := t.TempDir()
	outside := t.TempDir()
	store := newWorkspaceStore(t, ws)

	// A symlink at <ws>/.workground2/constraints.json pointing outside the
	// workspace must be rejected, not followed.
	if err := os.MkdirAll(filepath.Join(ws, ".workground2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "evil.json"), []byte(`{"revision":0,"constraints":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, ".workground2", "constraints.json")
	if err := os.Symlink(filepath.Join(outside, "evil.json"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}

	get := NewProjectConstraintsGetTool(store, "helper-ws")
	g := execProjectTool(t, get, nil)
	if g.Status == "accepted" && strings.Contains(g.Source, outside) {
		t.Fatalf("get followed an escaping symlink: %+v", g)
	}
	if g.Status != "retryable_error" && g.Status != "invalid" {
		t.Fatalf("get over escaping symlink = %+v, want error", g)
	}
}
