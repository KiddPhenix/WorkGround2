package main

import (
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/control"
	"workground2/internal/vocabulary"
)

func TestVocabularyCompletionIsTabScopedAndRecordsUse(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", vocabulary.ProjectFile), []byte(`[[terms]]
text = "多模态生视频V5"
kind = "noun"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	svc := vocabulary.New(vocabulary.Options{WorkspaceRoot: root, StateDir: state})
	ctrl := control.New(control.Options{WorkspaceRoot: root, Vocabulary: svc})
	app := NewApp()
	app.setTestCtrl(ctrl, "test")

	got, err := app.CompleteVocabularyForTab("test", "多模", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "多模态生视频V5" || got[0].Suffix != "态生视频V5" {
		t.Fatalf("completion = %+v", got)
	}
	if err := app.RecordVocabularyUseForTab("test", got[0].ID, "use-1"); err != nil {
		t.Fatal(err)
	}
	reloaded := vocabulary.New(vocabulary.Options{WorkspaceRoot: root, StateDir: state})
	if ranked := reloaded.Complete("多模", 1); len(ranked) != 1 {
		t.Fatalf("recorded vocabulary missing after reload: %+v", ranked)
	}
}

func TestVocabularyCompletionRejectsMissingTabIdentity(t *testing.T) {
	app := NewApp()
	if _, err := app.CompleteVocabularyForTab("", "多模", 5); err == nil {
		t.Fatal("missing tab id should fail explicitly")
	}
	if err := app.RecordVocabularyUseForTab("", "term", "use-1"); err == nil {
		t.Fatal("missing tab id use should fail explicitly")
	}
}
