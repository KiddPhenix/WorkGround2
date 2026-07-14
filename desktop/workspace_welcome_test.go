package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestScanWorkspaceWelcomeClassifiesMixedContentAndSkipsNoise(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod":                     "module example\n",
		"main.go":                    "package main\n",
		"notes/brief.md":             "brief\n",
		"data/report.csv":            "a,b\n",
		"assets/preview.png":         "png",
		"node_modules/noise/file.js": "ignored",
	}
	for name, body := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	first := scanWorkspaceWelcome(root)
	second := scanWorkspaceWelcome(root)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("scan is not deterministic: first=%+v second=%+v", first, second)
	}
	if first.files != 5 {
		t.Fatalf("files = %d, want 5 (noise skipped)", first.files)
	}
	wantKinds := []string{"code", "docs", "data", "media"}
	if !reflect.DeepEqual(first.kinds, wantKinds) {
		t.Fatalf("kinds = %v, want %v", first.kinds, wantKinds)
	}
	if first.confidence != 0.95 {
		t.Fatalf("confidence = %v, want marker confidence", first.confidence)
	}
}

func TestScanWorkspaceWelcomeEmpty(t *testing.T) {
	profile := scanWorkspaceWelcome(t.TempDir())
	if !reflect.DeepEqual(profile.kinds, []string{"empty"}) || profile.confidence != 0.9 {
		t.Fatalf("empty profile = %+v", profile)
	}
}

func TestWelcomeRecentActivityUsesSameWorkspaceAndSkipsCurrent(t *testing.T) {
	tree := []ProjectNode{
		{
			Kind: "project",
			Root: `D:\work\alpha`,
			Children: []ProjectNode{
				{Kind: "topic", TopicID: "current", Label: "Current", LastActivityAt: 30},
				{Kind: "topic", TopicID: "previous", Label: "Previous task", LastActivityAt: 20},
			},
		},
	}
	count, title, at := welcomeRecentActivity(tree, "project", `D:\work\alpha`, "current")
	if count != 2 || title != "Previous task" || at != 20 {
		t.Fatalf("activity = (%d, %q, %d)", count, title, at)
	}
}

func TestWelcomeRecentActivityCountsAllSessions(t *testing.T) {
	tree := []ProjectNode{
		{
			Kind: "project",
			Root: `D:\work\alpha`,
			Children: []ProjectNode{
				{Kind: "topic", TopicID: "previous", Label: "Previous task", LastActivityAt: 30},
				{Kind: "topic", TopicID: "older", Label: "Older task", LastActivityAt: 20},
				{Kind: "topic", TopicID: "current", Label: "Current", LastActivityAt: 10},
			},
		},
	}
	count, title, at := welcomeRecentActivity(tree, "project", `D:\work\alpha`, "current")
	if count != 3 || title != "Previous task" || at != 30 {
		t.Fatalf("activity = (%d, %q, %d)", count, title, at)
	}
}

func TestWorkspaceWelcomeMissingTabDegradesWithoutPaths(t *testing.T) {
	got := (&App{}).WorkspaceWelcome("missing")
	if !got.Degraded || got.DegradedReason != "workspace unavailable" {
		t.Fatalf("missing tab = %+v", got)
	}
	if got.ContentKinds == nil {
		t.Fatal("contentKinds must serialize as []")
	}
}
