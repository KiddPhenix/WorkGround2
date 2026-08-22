package boot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/event"
)

func TestBuildWiresWorkspaceAndAgentVocabulary(t *testing.T) {
	isolateConfigHome(t)
	root := robustTempDir(t)
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", "vocabulary.toml"), []byte(`[[terms]]
text = "多模态生视频V5"
description = "workspace node"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("# 词汇表\n\n- 批量跑图 — agent verb\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{WorkspaceRoot: root, Model: "deepseek-flash", Sink: event.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if got := ctrl.CompleteVocabulary("多模", 5); len(got) != 1 || got[0].Text != "多模态生视频V5" {
		t.Fatalf("workspace vocabulary = %+v", got)
	}
	if got := ctrl.CompleteVocabulary("批量", 5); len(got) != 1 || got[0].Source != "agent" {
		t.Fatalf("agent vocabulary = %+v", got)
	}
}
