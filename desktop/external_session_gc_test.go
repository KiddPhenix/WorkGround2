package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/session"
)

func TestExternalSessionIdleLimit(t *testing.T) {
	tests := []struct {
		name string
		meta agent.BranchMeta
		want time.Duration
		ok   bool
	}{
		{name: "cli", meta: agent.BranchMeta{SessionSource: "cli"}, want: cliSessionIdleLimit, ok: true},
		{name: "im", meta: agent.BranchMeta{SessionSource: "auto", Channel: "weixin"}, want: imSessionIdleLimit, ok: true},
		{name: "auto without channel", meta: agent.BranchMeta{SessionSource: "auto"}},
		{name: "local", meta: agent.BranchMeta{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := externalSessionIdleLimit(tt.meta)
			if got != tt.want || ok != tt.ok {
				t.Fatalf("externalSessionIdleLimit(%+v) = (%s, %v), want (%s, %v)", tt.meta, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestShouldTrashExternalSessionHonorsSourceAndPin(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	write := func(name, source string, pinned bool) string {
		path := filepath.Join(dir, name+".jsonl")
		if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
			ID:            name,
			CreatedAt:     now.Add(-7 * time.Hour),
			UpdatedAt:     now.Add(-7 * time.Hour),
			SessionSource: source,
			Pinned:        pinned,
		}); err != nil {
			t.Fatalf("save %s meta: %v", name, err)
		}
		return path
	}
	if !app.shouldTrashExternalSession(write("cli", "cli", false), now) {
		t.Fatal("expired CLI session was not selected")
	}
	if app.shouldTrashExternalSession(write("pinned", "cli", true), now) {
		t.Fatal("pinned session was selected")
	}
	if app.shouldTrashExternalSession(write("local", "", false), now) {
		t.Fatal("local session was selected")
	}
}

func TestSweepExternalSessionsRemovesLastSessionTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_expired_cli"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Expired CLI"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(dir, "expired-cli.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"done"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	now := time.Now().UTC()
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		CreatedAt:     now.Add(-8 * time.Hour),
		UpdatedAt:     now.Add(-7 * time.Hour),
		SessionSource: "cli",
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
		TopicTitle:    "Expired CLI",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	if moved := app.sweepExternalSessions(now); moved != 1 {
		t.Fatalf("sweepExternalSessions() moved %d sessions, want 1", moved)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expired session still live: %v", err)
	}
	for _, project := range loadProjectsFile().Projects {
		if containsDesktopString(project.Topics, topicID) {
			t.Fatalf("expired session topic %q remains indexed", topicID)
		}
	}
}

// ---------------------------------------------------------------------------
// removeOrphanExternalTopic tests
// ---------------------------------------------------------------------------

func removeOrphanTopicForTest(t *testing.T, app *App, topicID, scope, workspaceRoot string) {
	t.Helper()
	if err := app.removeOrphanExternalTopic(topicID, scope, workspaceRoot); err != nil {
		t.Fatalf("removeOrphanExternalTopic(%q): %v", topicID, err)
	}
}

func TestMaybeRemoveOrphanTopicKeepsTopicWithLiveSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_keep_live"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// An empty but valid live session must still protect the topic: a late first
	// message may arrive after this listing snapshot.
	path := filepath.Join(dir, "keep.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write empty live session: %v", err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
		TopicTitle:    "Keep me",
	}); err != nil {
		t.Fatalf("save empty live session meta: %v", err)
	}

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	// Invalidate any cached index so findTopicContentSessionForTarget sees the file.
	invalidateTopicSessionIndex(dir)

	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)

	// Topic should still be in the projects file.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			return // passed
		}
	}
	t.Fatalf("topic %q was removed even though a live session remains", topicID)
}

func TestMaybeRemoveOrphanTopicRemovesOrphanTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_remove_orphan"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Remove me"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	// No live session for this topic.
	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)

	// Topic should be removed from projects file.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			t.Fatalf("orphan topic %q was not removed from projects file", topicID)
		}
	}

	// Topic title should be cleared.
	titles := loadTopicTitles(projectRoot)
	if _, ok := titles[topicID]; ok {
		t.Fatalf("orphan topic %q title was not removed", topicID)
	}
}

func TestMaybeRemoveOrphanTopicKeepsTopicWithRuntimeTab(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_runtime_tab"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	invalidateTopicSessionIndex(dir)

	// A runtime tab holds the topic even though there's no session file.
	app := &App{
		tabs: map[string]*WorkspaceTab{
			"active": {
				ID:            "active",
				TopicID:       topicID,
				Scope:         "project",
				WorkspaceRoot: projectRoot,
			},
		},
		detachedSessions: map[string]*WorkspaceTab{},
	}

	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)

	// Topic should still be in the projects file.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			return // passed
		}
	}
	t.Fatalf("topic %q was removed even though a runtime tab references it", topicID)
}

func TestMaybeRemoveOrphanTopicKeepsTopicWithDetachedRuntime(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_detached"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	invalidateTopicSessionIndex(dir)

	app := &App{
		tabs: map[string]*WorkspaceTab{},
		detachedSessions: map[string]*WorkspaceTab{
			"detached": {
				ID:            "detached",
				TopicID:       topicID,
				Scope:         "project",
				WorkspaceRoot: projectRoot,
			},
		},
	}

	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)

	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			return
		}
	}
	t.Fatalf("topic %q was removed even though a detached runtime tab references it", topicID)
}

func TestMaybeRemoveOrphanTopicIdempotent(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_idempotent"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}

	// First call removes the topic.
	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)
	// Second call must not panic or error.
	removeOrphanTopicForTest(t, app, topicID, "project", projectRoot)

	// Topic should be gone.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			t.Fatalf("orphan topic %q should be removed after first call", topicID)
		}
	}
}

// ---------------------------------------------------------------------------
// reconcileOrphanTopics tests
// ---------------------------------------------------------------------------

func TestReconcileOrphanTopicsCleansTrashedExternalSessionTopic(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_reconcile_cli"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "CLI topic"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	now := time.Now()
	path := filepath.Join(dir, "cli-session.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		CreatedAt:     now.Add(-8 * time.Hour),
		UpdatedAt:     now.Add(-7 * time.Hour),
		SessionSource: "cli",
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
		TopicTitle:    "CLI topic",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	// Trash the session manually (simulating what GC would do).
	if err := session.TrashSession(dir, path); err != nil {
		t.Fatalf("trash session: %v", err)
	}

	// Verify the session is in trash.
	trashed, err := session.ListTrashed(dir)
	if err != nil {
		t.Fatalf("list trashed: %v", err)
	}
	if len(trashed) == 0 {
		t.Fatalf("session should be in trash after TrashSession")
	}

	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	app.reconcileOrphanTopics()

	// Topic should be removed.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			t.Fatalf("orphan topic %q should be removed by reconcileOrphanTopics", topicID)
		}
	}
}

func TestReconcileOrphanTopicsSkipsNonExternalTrashedSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_local_trashed"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Local topic"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	now := time.Now()
	path := filepath.Join(dir, "local-session.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"local work"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	// No SessionSource → local session, not eligible for GC.
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		CreatedAt:     now.Add(-8 * time.Hour),
		UpdatedAt:     now.Add(-7 * time.Hour),
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
		TopicTitle:    "Local topic",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}

	if err := session.TrashSession(dir, path); err != nil {
		t.Fatalf("trash session: %v", err)
	}

	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	app.reconcileOrphanTopics()

	// Topic should NOT be removed (it's a local session, not external).
	f := loadProjectsFile()
	found := false
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("non-external topic %q should NOT be removed by reconcileOrphanTopics", topicID)
	}
}

func TestReconcileOrphanTopicsKeepsTopicWithRemainingLiveSession(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_multi_session"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := setTopicTitle(projectRoot, topicID, "Multi session"); err != nil {
		t.Fatalf("set topic title: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	now := time.Now()

	// Trashed external session.
	trashPath := filepath.Join(dir, "trashed-cli.jsonl")
	if err := os.WriteFile(trashPath, []byte(`{"role":"user","content":"old"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := agent.SaveBranchMeta(trashPath, agent.BranchMeta{
		CreatedAt:     now.Add(-8 * time.Hour),
		UpdatedAt:     now.Add(-7 * time.Hour),
		SessionSource: "cli",
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
		TopicTitle:    "Multi session",
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	if err := session.TrashSession(dir, trashPath); err != nil {
		t.Fatalf("trash session: %v", err)
	}

	// Live session for the same topic.
	writeTopicSession(t, dir, "keep.jsonl", topicID, "Multi session", projectRoot)

	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	app.reconcileOrphanTopics()

	// Topic should still be present.
	f := loadProjectsFile()
	found := false
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("topic %q should remain when a live session exists", topicID)
	}
}

func TestReconcileOrphanTopicsIdempotent(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	topicID := "topic_reconcile_idem"
	if err := addProject(projectRoot, ""); err != nil {
		t.Fatalf("add project: %v", err)
	}
	if err := prependTopicInProjectsFile(projectRoot, topicID, true); err != nil {
		t.Fatalf("prepend topic: %v", err)
	}

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	now := time.Now()
	path := filepath.Join(dir, "cli-idem.jsonl")
	if err := os.WriteFile(path, []byte(`{"role":"user","content":"hello"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		CreatedAt:     now.Add(-8 * time.Hour),
		UpdatedAt:     now.Add(-7 * time.Hour),
		SessionSource: "cli",
		Scope:         "project",
		WorkspaceRoot: projectRoot,
		TopicID:       topicID,
	}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	if err := session.TrashSession(dir, path); err != nil {
		t.Fatalf("trash session: %v", err)
	}

	invalidateTopicSessionIndex(dir)

	app := &App{tabs: map[string]*WorkspaceTab{}, detachedSessions: map[string]*WorkspaceTab{}}
	// Call twice.
	app.reconcileOrphanTopics()
	app.reconcileOrphanTopics()

	// Topic should be gone, no panic.
	f := loadProjectsFile()
	for _, p := range f.Projects {
		if containsDesktopString(p.Topics, topicID) {
			t.Fatalf("orphan topic %q should be removed", topicID)
		}
	}
}
