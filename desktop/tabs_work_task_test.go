package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
)

func TestIsWorkTaskSessionSource(t *testing.T) {
	tests := []struct {
		source string
		want   bool
	}{
		{"work:work-1/run:run-1/stage:stage-1/task:task-1/attempt:1/request:req-1", true},
		{"work:w-2/run:r-3/stage:s-4/task:t-5/attempt:0/request:r-6", true},
		{"work:", true},             // edge: bare prefix
		{"work", false},             // no colon
		{" Work:w-1/run:r-1", true}, // persisted metadata is normalized on read
		{"", false},
		{"cli", false},
		{"bot", false},
		{"external", false},
		{"auto", false},
	}
	for _, tt := range tests {
		got := isWorkTaskSessionSource(tt.source)
		if got != tt.want {
			t.Errorf("isWorkTaskSessionSource(%q) = %v, want %v", tt.source, got, tt.want)
		}
	}
}

func TestListProjectTreeExcludesWorkTaskSessions(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "TestProject"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	sessionDir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	// Normal session with "external" source.
	normalPath := writeTopicSession(t, sessionDir, "20250101-120000.000000000-normal.jsonl",
		"topic-normal", "Normal Topic", projectRoot)

	// Set SessionSource on normal session.
	normalMeta, ok, err := agent.LoadBranchMeta(normalPath)
	if err != nil || !ok {
		t.Fatalf("load normal meta: err=%v ok=%v", err, ok)
	}
	normalMeta.SessionSource = "external"
	if err := agent.SaveBranchMetaPreserveUpdated(normalPath, normalMeta); err != nil {
		t.Fatalf("save normal source: %v", err)
	}

	// Work task session with "work:" source.
	workTaskPath := writeTopicSession(t, sessionDir, "20250101-130000.000000000-work.jsonl",
		"topic-work", "Work Task Topic", projectRoot)

	workTaskMeta, ok, err := agent.LoadBranchMeta(workTaskPath)
	if err != nil || !ok {
		t.Fatalf("load work task meta: err=%v ok=%v", err, ok)
	}
	workTaskMeta.SessionSource = "work:work-1/run:run-1/stage:stage-1/task:task-1/attempt:0/request:req-1"
	if err := agent.SaveBranchMetaPreserveUpdated(workTaskPath, workTaskMeta); err != nil {
		t.Fatalf("save work task source: %v", err)
	}

	// Manually verify infos listing excludes work: sessions.
	infos, err := agent.ListSessions(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	sessionInfos := map[string]agent.SessionInfo{}
	sessionTitles := map[string]string{}
	topicSummaries := map[string]topicSummary{}
	workTaskTopics := map[string]bool{}
	visibleSessionTopics := map[string]bool{}
	mergeSessionInfos(sessionDir, infos, nil, sessionInfos, sessionTitles, topicSummaries, workTaskTopics, visibleSessionTopics)

	// Normal session should be in sessionInfos and topicSummaries.
	if _, ok := sessionInfos[sessionRuntimeKey(normalPath)]; !ok {
		t.Error("normal session missing from sessionInfos")
	}
	if _, ok := topicSummaries[topicSummaryKey("project", projectRoot, "topic-normal")]; !ok {
		t.Error("normal topic missing from topicSummaries")
	}

	// Work task metadata remains available for runtime filtering, but it must
	// not contribute a visible topic summary.
	if _, ok := sessionInfos[sessionRuntimeKey(workTaskPath)]; !ok {
		t.Error("work task metadata should remain available for runtime filtering")
	}
	if _, ok := topicSummaries[topicSummaryKey("project", projectRoot, "topic-work")]; ok {
		t.Error("work task topic should be excluded from topicSummaries")
	}
	if !workTaskTopics[topicSummaryKey("project", projectRoot, "topic-work")] {
		t.Error("work task topic should be recorded as hidden")
	}
	if visibleSessionTopics[topicSummaryKey("project", projectRoot, "topic-work")] {
		t.Error("work task-only topic should not be recorded as visible")
	}

	if err := prependTopicsInProjectsFile(projectRoot, []string{"topic-normal", "topic-work"}, false); err != nil {
		t.Fatalf("persist topic list: %v", err)
	}
	if err := saveTopicTitles(projectRoot, map[string]string{
		"topic-normal": "Normal Topic",
		"topic-work":   "Work Task Topic",
	}); err != nil {
		t.Fatalf("persist topic titles: %v", err)
	}
	projectSessionCache.invalidate()
	tree := NewApp().ListProjectTree()
	if !projectTreeContainsTopic(tree, "topic-normal") {
		t.Error("normal external session should remain visible in project tree")
	}
	if projectTreeContainsTopic(tree, "topic-work") {
		t.Error("already-migrated work task topic should be hidden from project tree")
	}
}

func TestListSessionsExcludesWorkTaskSessions(t *testing.T) {
	isolateDesktopUserDirs(t)

	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "TestProject"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	sessionDir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	normalPath := writeTopicSession(t, sessionDir, "normal.jsonl", "topic-normal", "Normal Topic", projectRoot)
	workTaskPath := writeTopicSession(t, sessionDir, "work-task.jsonl", "topic-work", "Work Task Topic", projectRoot)
	workTaskMeta, ok, err := agent.LoadBranchMeta(workTaskPath)
	if err != nil || !ok {
		t.Fatalf("load work task meta: err=%v ok=%v", err, ok)
	}
	workTaskMeta.SessionSource = "work:work-1/run:run-1/stage:stage-1/task:task-1/attempt:0/request:req-1"
	if err := agent.SaveBranchMetaPreserveUpdated(workTaskPath, workTaskMeta); err != nil {
		t.Fatalf("save work task source: %v", err)
	}

	app := NewApp()
	tab := &WorkspaceTab{
		ID: "test", SessionID: "session-test", Scope: "project", WorkspaceRoot: projectRoot,
		TopicID: "topic-normal", TopicTitle: "Normal Topic", SessionPath: normalPath, Ready: true,
	}
	app.tabs[tab.ID] = tab
	app.activeTabID = tab.ID
	app.trackSession(tab)

	sessions := app.ListSessionsForSession(tab.SessionID)
	foundNormal := false
	for _, session := range sessions {
		if sessionRuntimeKey(session.Path) == sessionRuntimeKey(workTaskPath) {
			t.Error("work task session should be hidden from the general session history")
		}
		if sessionRuntimeKey(session.Path) == sessionRuntimeKey(normalPath) {
			foundNormal = true
		}
	}
	if !foundNormal {
		t.Error("normal session should remain visible in the general session history")
	}
}

func TestLegacyMigrationSkipsWorkTaskSessions(t *testing.T) {
	isolateDesktopUserDirs(t)

	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	now := time.Now()

	// Legacy session without meta — should be migrated.
	legacyPath := writeLegacySession(t, dir, "20250101-150000.000000000-legacy.jsonl",
		"legacy chat", now.Add(-2*time.Hour))

	// Work task session — should be skipped by migration.
	workTaskPath := filepath.Join(dir, "20250101-160000.000000000-work-migrate.jsonl")
	if err := os.WriteFile(workTaskPath, []byte(`{"role":"user","content":"task"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write work task session: %v", err)
	}
	if err := os.Chtimes(workTaskPath, now.Add(-1*time.Hour), now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("chtimes work task: %v", err)
	}
	if err := agent.SaveBranchMeta(workTaskPath, agent.BranchMeta{
		CreatedAt:     now.Add(-1 * time.Hour),
		UpdatedAt:     now,
		SessionSource: "work:work-2/run:run-2/stage:stage-2/task:task-2/attempt:0/request:req-2",
	}); err != nil {
		t.Fatalf("save work task branch meta: %v", err)
	}

	app := NewApp()
	app.ListProjectTree() // triggers migration

	// Legacy session should have been migrated.
	legacyMeta, ok, err := agent.LoadBranchMeta(legacyPath)
	if err != nil {
		t.Fatalf("load legacy meta: %v", err)
	}
	if !ok || strings.TrimSpace(legacyMeta.TopicID) == "" {
		t.Error("legacy session was not migrated")
	}

	// Work task session should NOT have been assigned a topic.
	workMeta, ok, err := agent.LoadBranchMeta(workTaskPath)
	if err != nil {
		t.Fatalf("load work task meta: %v", err)
	}
	if ok && strings.TrimSpace(workMeta.TopicID) != "" {
		t.Errorf("work task session was incorrectly assigned topic %q", workMeta.TopicID)
	}
	if ok && workMeta.SessionSource != "work:work-2/run:run-2/stage:stage-2/task:task-2/attempt:0/request:req-2" {
		t.Errorf("work task session source was overwritten to %q", workMeta.SessionSource)
	}
}
