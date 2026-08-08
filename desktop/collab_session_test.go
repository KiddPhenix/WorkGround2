package main

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/agent"
)

func TestBindCollaborationSessionPersistsWorkspaceSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := agent.NewSessionPath(dir, "test")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID:            "tab-collaboration",
		SessionID:     "session-collaboration",
		Scope:         "project",
		WorkspaceRoot: root,
		TopicID:       "topic-collaboration",
		TopicTitle:    defaultTopicTitle,
		SessionPath:   path,
	}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, activeTabID: tab.ID}
	app.trackSession(tab)

	for i := 0; i < 2; i++ {
		if err := app.bindCollaborationSession(tab.SessionID, "角色换装联调"); err != nil {
			t.Fatalf("bind attempt %d: %v", i+1, err)
		}
	}
	if tab.sessionKind != agent.SessionKindCollaboration {
		t.Fatalf("tab session kind = %q", tab.sessionKind)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta = (%+v, %v, %v)", meta, ok, err)
	}
	if meta.SessionKind != agent.SessionKindCollaboration || meta.SessionSource != "collaboration" || meta.CustomTitle != "角色换装联调" || meta.WorkspaceRoot != filepath.Clean(root) {
		t.Fatalf("persisted collaboration metadata = %+v", meta)
	}
	tabMeta := app.tabMeta(tab, true)
	if tabMeta.SessionKind != string(agent.SessionKindCollaboration) || tabMeta.WorkspaceRoot != root || tabMeta.SessionID != tab.SessionID {
		t.Fatalf("tab metadata = %+v", tabMeta)
	}

	nodes := app.ListProjectTree()
	found := false
	for _, project := range nodes {
		for _, topic := range project.Children {
			if topic.TopicID == tab.TopicID && topic.SessionKind == string(agent.SessionKindCollaboration) && topic.SessionID == tab.SessionID && topic.SessionPath == path {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("collaboration session missing from project tree: %+v", nodes)
	}

	// Simulate a restart where this Room tab is not the single restored surface.
	// The persisted collaboration session must still own a visible Session List
	// entry even though its local transcript has no user turns yet.
	restarted := &App{tabs: map[string]*WorkspaceTab{}}
	nodes = restarted.ListProjectTree()
	found = false
	for _, project := range nodes {
		for _, topic := range project.Children {
			if topic.TopicID == tab.TopicID && topic.SessionKind == string(agent.SessionKindCollaboration) && topic.SessionPath == path {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("persisted collaboration session missing after restart: %+v", nodes)
	}
}

func TestBindCollaborationSessionRejectsNonBlankAndWorkSessions(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	dir := desktopSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	newTab := func(id, content string, kind agent.SessionKind) (*App, *WorkspaceTab) {
		path := agent.NewSessionPath(dir, id)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		tab := &WorkspaceTab{ID: id, SessionID: "session-" + id, Scope: "project", WorkspaceRoot: root, TopicID: "topic-" + id, SessionPath: path, sessionKind: kind}
		app := &App{tabs: map[string]*WorkspaceTab{id: tab}, activeTabID: id}
		app.trackSession(tab)
		return app, tab
	}

	nonBlankApp, nonBlank := newTab("nonblank", `{"type":"user","content":"hello"}`+"\n", agent.SessionKindNormal)
	if err := nonBlankApp.bindCollaborationSession(nonBlank.SessionID, "Room"); err == nil {
		t.Fatal("non-blank session unexpectedly converted")
	}
	workApp, work := newTab("work", "", agent.SessionKindWork)
	if err := workApp.bindCollaborationSession(work.SessionID, "Room"); err == nil {
		t.Fatal("Work session unexpectedly converted")
	}
}
