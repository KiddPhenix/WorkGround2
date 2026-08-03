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

	// Historical snapshot-conflict recovery branches lost the work source and
	// were later stamped external. Parent lineage must still keep them out of
	// the general SessionList.
	recoveryPath := writeTopicSession(t, sessionDir, "20250101-140000.000000000-recovery.jsonl",
		"topic-recovery", "Recovered Work Task", projectRoot)
	recoveryMeta, ok, err := agent.LoadBranchMeta(recoveryPath)
	if err != nil || !ok {
		t.Fatalf("load recovery meta: err=%v ok=%v", err, ok)
	}
	recoveryMeta.Recovered = true
	recoveryMeta.ParentID = string(agent.BranchID(workTaskPath))
	recoveryMeta.SessionSource = "external"
	if err := agent.SaveBranchMetaPreserveUpdated(recoveryPath, recoveryMeta); err != nil {
		t.Fatalf("save recovery source: %v", err)
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
	if _, ok := topicSummaries[topicSummaryKey("project", projectRoot, "topic-recovery")]; ok {
		t.Error("recovered work task topic should be excluded from topicSummaries")
	}
	if !workTaskTopics[topicSummaryKey("project", projectRoot, "topic-recovery")] {
		t.Error("recovered work task topic should be recorded as hidden")
	}

	if err := prependTopicsInProjectsFile(projectRoot, []string{"topic-normal", "topic-work", "topic-recovery"}, false); err != nil {
		t.Fatalf("persist topic list: %v", err)
	}
	if err := saveTopicTitles(projectRoot, map[string]string{
		"topic-normal":   "Normal Topic",
		"topic-work":     "Work Task Topic",
		"topic-recovery": "Recovered Work Task",
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
	if projectTreeContainsTopic(tree, "topic-recovery") {
		t.Error("legacy external recovery of a work task should be hidden from project tree")
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

func TestListProjectTree_WorkIconOnColdStart(t *testing.T) {
	isolateDesktopUserDirs(t)

	// Project-scope Work session — cold start, no tab open.
	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "WorkProject"); err != nil {
		t.Fatalf("add project: %v", err)
	}
	sessionDir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}

	workPath := writeTopicSessionWithPrompt(t, sessionDir,
		"20250101-120000.000000000-work.jsonl",
		"topic-work", "Work Topic", projectRoot,
		"work item", time.Now(),
	)
	workMeta, ok, err := agent.LoadBranchMeta(workPath)
	if err != nil || !ok {
		t.Fatalf("load work meta: err=%v ok=%v", err, ok)
	}
	workMeta.SessionKind = agent.SessionKindWork
	workMeta.WorkID = "work-cold-1"
	if err := agent.SaveBranchMetaPreserveUpdated(workPath, workMeta); err != nil {
		t.Fatalf("save work meta: %v", err)
	}

	if err := prependTopicsInProjectsFile(projectRoot, []string{"topic-work"}, false); err != nil {
		t.Fatalf("persist topic list: %v", err)
	}
	if err := saveTopicTitles(projectRoot, map[string]string{"topic-work": "Work Topic"}); err != nil {
		t.Fatalf("persist topic titles: %v", err)
	}

	projectSessionCache.invalidate()
	tree := NewApp().ListProjectTree()

	// Find the project node → topic child.
	var workNode *ProjectNode
	for _, proj := range tree {
		if proj.Kind != "project" || proj.Root != projectRoot {
			continue
		}
		for i := range proj.Children {
			child := &proj.Children[i]
			if child.TopicID == "topic-work" {
				workNode = child
				break
			}
		}
	}
	if workNode == nil {
		t.Fatal("work session topic not found in project tree")
	}
	if workNode.Kind != "work_session" {
		t.Fatalf("cold-start work node Kind = %q, want work_session", workNode.Kind)
	}
	if workNode.SessionKind != string(agent.SessionKindWork) {
		t.Fatalf("SessionKind = %q, want work", workNode.SessionKind)
	}
	if workNode.WorkID != "work-cold-1" {
		t.Fatalf("WorkID = %q, want work-cold-1", workNode.WorkID)
	}
	if workNode.SessionPath != workPath {
		t.Fatalf("SessionPath = %q, want %q", workNode.SessionPath, workPath)
	}
}

func TestListProjectTree_WorkIconGlobalColdStart(t *testing.T) {
	isolateDesktopUserDirs(t)

	// Global-scope Work session — cold start, no tab open.
	globalDir := config.SessionDir()
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir global sessions: %v", err)
	}

	workPath := writeTopicSessionWithPrompt(t, globalDir,
		"20250101-120000.000000000-global-work.jsonl",
		"topic-global-work", "Global Work", "",
		"global work item", time.Now(),
	)
	workMeta, ok, err := agent.LoadBranchMeta(workPath)
	if err != nil || !ok {
		t.Fatalf("load work meta: err=%v ok=%v", err, ok)
	}
	workMeta.SessionKind = agent.SessionKindWork
	workMeta.WorkID = "global-work-1"
	if err := agent.SaveBranchMetaPreserveUpdated(workPath, workMeta); err != nil {
		t.Fatalf("save work meta: %v", err)
	}

	// Register the topic in global topics.
	if err := prependTopicsInProjectsFile("", []string{"topic-global-work"}, true); err != nil {
		t.Fatalf("persist global topic list: %v", err)
	}
	if err := saveTopicTitles("", map[string]string{"topic-global-work": "Global Work"}); err != nil {
		t.Fatalf("persist global topic titles: %v", err)
	}

	projectSessionCache.invalidate()
	tree := NewApp().ListProjectTree()

	var workNode *ProjectNode
	for _, node := range tree {
		if node.Kind != "global_folder" {
			continue
		}
		for i := range node.Children {
			child := &node.Children[i]
			if child.TopicID == "topic-global-work" {
				workNode = child
				break
			}
		}
	}
	if workNode == nil {
		t.Fatal("global work session topic not found in project tree")
	}
	if workNode.Kind != "global_work_session" {
		t.Fatalf("cold-start global work node Kind = %q, want global_work_session", workNode.Kind)
	}
	if workNode.SessionKind != string(agent.SessionKindWork) {
		t.Fatalf("SessionKind = %q, want work", workNode.SessionKind)
	}
	if workNode.WorkID != "global-work-1" {
		t.Fatalf("WorkID = %q, want global-work-1", workNode.WorkID)
	}
}

func TestListProjectTree_WorkIconLegacyRecoveryColdStart(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "RecoveryProject"); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := writeTopicSessionWithPrompt(t, dir, "work-root.jsonl", "topic-recovery", "Recovered Work", projectRoot, "root", time.Now().Add(-time.Minute))
	rootMeta, _, err := agent.LoadBranchMeta(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	rootMeta.SessionKind = agent.SessionKindWork
	rootMeta.WorkID = "work-recovery"
	rootMeta.WorkRequestID = "request-recovery"
	if err := agent.SaveBranchMetaPreserveUpdated(rootPath, rootMeta); err != nil {
		t.Fatal(err)
	}
	recoveryPath := writeTopicSessionWithPrompt(t, dir, "work-recovery.jsonl", "topic-recovery", "Recovered Work", projectRoot, "recovered", time.Now())
	recoveryMeta, _, err := agent.LoadBranchMeta(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	recoveryMeta.ParentID = agent.BranchID(rootPath)
	recoveryMeta.Recovered = true
	if err := agent.SaveBranchMetaPreserveUpdated(recoveryPath, recoveryMeta); err != nil {
		t.Fatal(err)
	}
	if err := prependTopicsInProjectsFile(projectRoot, []string{"topic-recovery"}, false); err != nil {
		t.Fatal(err)
	}
	if err := saveTopicTitles(projectRoot, map[string]string{"topic-recovery": "Recovered Work"}); err != nil {
		t.Fatal(err)
	}

	projectSessionCache.invalidate()
	node := findProjectTreeTopic(NewApp().ListProjectTree(), projectRoot, "topic-recovery")
	if node == nil || node.Kind != "work_session" || node.SessionKind != string(agent.SessionKindWork) || node.WorkID != "work-recovery" {
		t.Fatalf("legacy recovery cold-start node = %+v", node)
	}
}

func TestListProjectTree_MixedSessionsDoNotPromoteTopicToWork(t *testing.T) {
	isolateDesktopUserDirs(t)
	projectRoot := t.TempDir()
	if err := addProject(projectRoot, "MixedProject"); err != nil {
		t.Fatal(err)
	}
	dir := desktopSessionDir(projectRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTopicSessionWithPrompt(t, dir, "normal.jsonl", "topic-mixed", "Mixed", projectRoot, "normal", time.Now().Add(-time.Minute))
	workPath := writeTopicSessionWithPrompt(t, dir, "work.jsonl", "topic-mixed", "Mixed", projectRoot, "work", time.Now())
	workMeta, _, err := agent.LoadBranchMeta(workPath)
	if err != nil {
		t.Fatal(err)
	}
	workMeta.SessionKind = agent.SessionKindWork
	workMeta.WorkID = "work-mixed"
	if err := agent.SaveBranchMetaPreserveUpdated(workPath, workMeta); err != nil {
		t.Fatal(err)
	}
	if err := prependTopicsInProjectsFile(projectRoot, []string{"topic-mixed"}, false); err != nil {
		t.Fatal(err)
	}
	if err := saveTopicTitles(projectRoot, map[string]string{"topic-mixed": "Mixed"}); err != nil {
		t.Fatal(err)
	}

	projectSessionCache.invalidate()
	node := findProjectTreeTopic(NewApp().ListProjectTree(), projectRoot, "topic-mixed")
	if node == nil || node.Kind != "topic" || node.SessionKind == string(agent.SessionKindWork) {
		t.Fatalf("mixed topic should stay normal: %+v", node)
	}
}

func findProjectTreeTopic(tree []ProjectNode, projectRoot, topicID string) *ProjectNode {
	for _, project := range tree {
		if project.Kind != "project" || project.Root != projectRoot {
			continue
		}
		for i := range project.Children {
			if project.Children[i].TopicID == topicID {
				return &project.Children[i]
			}
		}
	}
	return nil
}
