package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/config"
)

func writeTitleReconcileSession(t *testing.T, root, name, topicID, topicTitle, customTitle, preview, sessionTitle string, updatedAt time.Time) string {
	t.Helper()
	dir := config.ProjectSessionDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	// Deliberately invalid JSON proves ListProjectTree trusts listing sidecars
	// instead of decoding the transcript during title reconciliation.
	if err := os.WriteFile(path, []byte("not-json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
		CreatedAt:     updatedAt.Add(-time.Minute),
		UpdatedAt:     updatedAt,
		Scope:         "project",
		WorkspaceRoot: root,
		TopicID:       topicID,
		TopicTitle:    topicTitle,
		CustomTitle:   customTitle,
		SchemaVersion: agent.BranchMetaCountsVersion,
		Turns:         1,
		Preview:       preview,
	}); err != nil {
		t.Fatal(err)
	}
	if sessionTitle != "" {
		if err := setSessionTitle(dir, path, sessionTitle); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func setupTitleReconcileTopic(t *testing.T, title, source string) (string, string) {
	t.Helper()
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	topicID := "topic_title_reconcile"
	if err := prependTopicInProjectsFile(root, topicID, true); err != nil {
		t.Fatal(err)
	}
	if title != "" {
		if err := setTopicTitleWithSource(root, topicID, title, source); err != nil {
			t.Fatal(err)
		}
	}
	return root, topicID
}

func reconciledProjectTopic(t *testing.T, root, topicID string) ProjectNode {
	t.Helper()
	for _, project := range NewApp().ListProjectTree() {
		if project.Kind != "project" || project.Root != root {
			continue
		}
		for _, topic := range project.Children {
			if topic.TopicID == topicID {
				return topic
			}
		}
	}
	t.Fatalf("topic %q under %q was not listed", topicID, root)
	return ProjectNode{}
}

func TestProjectTreeReconcilesDefaultTitleFromSessionTitle(t *testing.T) {
	root, topicID := setupTitleReconcileTopic(t, defaultTopicTitle, topicTitleSourceAuto)
	writeTitleReconcileSession(t, root, "named.jsonl", topicID, defaultTopicTitle, "", "ignored preview", "CLI 命名会话", time.Now())

	topic := reconciledProjectTopic(t, root, topicID)
	if topic.Label != "CLI 命名会话" {
		t.Fatalf("topic label = %q, want CLI session title", topic.Label)
	}
	if got := loadTopicTitle(root, topicID); got != "CLI 命名会话" {
		t.Fatalf("persisted title = %q, want CLI session title", got)
	}
	if got := loadTopicTitleSource(root, topicID); got != topicTitleSourceManual {
		t.Fatalf("persisted source = %q, want manual", got)
	}
}

func TestProjectTreeReconcilesMissingTopicTitle(t *testing.T) {
	root, topicID := setupTitleReconcileTopic(t, "", "")
	writeTitleReconcileSession(t, root, "missing.jsonl", topicID, defaultTopicTitle, "", "ignored preview", "已命名会话", time.Now())

	if got := reconciledProjectTopic(t, root, topicID).Label; got != "已命名会话" {
		t.Fatalf("topic label = %q, want reconciled missing title", got)
	}
}

func TestProjectTreeKeepsManualDefaultTitle(t *testing.T) {
	root, topicID := setupTitleReconcileTopic(t, defaultTopicTitle, topicTitleSourceManual)
	writeTitleReconcileSession(t, root, "manual.jsonl", topicID, defaultTopicTitle, "", "ignored preview", "不应覆盖", time.Now())

	if got := reconciledProjectTopic(t, root, topicID).Label; got != defaultTopicTitle {
		t.Fatalf("manual default title changed to %q", got)
	}
	if got := loadTopicTitleSource(root, topicID); got != topicTitleSourceManual {
		t.Fatalf("manual source changed to %q", got)
	}
}

func TestProjectTreeReconcileUsesLatestValidSession(t *testing.T) {
	root, topicID := setupTitleReconcileTopic(t, defaultTopicTitle, topicTitleSourceAuto)
	writeTitleReconcileSession(t, root, "old.jsonl", topicID, defaultTopicTitle, "", "old preview", "旧会话", time.Now().Add(-time.Hour))
	writeTitleReconcileSession(t, root, "new.jsonl", topicID, defaultTopicTitle, "", "new preview", "最新会话", time.Now())

	if got := reconciledProjectTopic(t, root, topicID).Label; got != "最新会话" {
		t.Fatalf("topic label = %q, want latest valid session title", got)
	}
}

func TestProjectTreeReconcileKeepsDefaultWithoutCandidate(t *testing.T) {
	root, topicID := setupTitleReconcileTopic(t, defaultTopicTitle, topicTitleSourceAuto)
	writeTitleReconcileSession(t, root, "no-candidate.jsonl", topicID, defaultTopicTitle, "", defaultTopicTitle, "", time.Now())

	if got := reconciledProjectTopic(t, root, topicID).Label; got != defaultTopicTitle {
		t.Fatalf("topic label = %q, want unchanged default", got)
	}
}
