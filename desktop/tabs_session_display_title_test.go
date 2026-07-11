package main

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/agent"
)

func TestTabSessionDisplayTitle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{Preview: "first user message"}); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{TopicTitle: "新的会话", SessionPath: path}
	if got := tabSessionDisplayTitle(tab); got != "first user message" {
		t.Fatalf("preview title = %q, want first user message", got)
	}

	if err := setSessionTitle(dir, path, "custom session title"); err != nil {
		t.Fatal(err)
	}
	if got := tabSessionDisplayTitle(tab); got != "custom session title" {
		t.Fatalf("custom title = %q, want custom session title", got)
	}
}
