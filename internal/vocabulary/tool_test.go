package vocabulary

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRebuildToolRebuildsAndRefreshesWorkspaceVocabulary(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, ".WorkGround2")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, ProjectFile), []byte(`version = 1

[[terms]]
text = "approval cannot expand authority"
kind = "phrase"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("名词：多模态生视频V5"), 0o644); err != nil {
		t.Fatal(err)
	}

	service := New(Options{WorkspaceRoot: root, StateDir: filepath.Join(t.TempDir(), "state")})
	rebuild := NewRebuildTool(service)
	if rebuild.Name() != "rebuild_vocabulary" || rebuild.ReadOnly() {
		t.Fatalf("tool contract: name=%q readOnly=%v", rebuild.Name(), rebuild.ReadOnly())
	}
	raw, err := rebuild.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var result RefreshResult
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatalf("decode result %q: %v", raw, err)
	}
	if !result.Updated || result.Scanned < 1 || result.Added < 1 || result.Path == "" {
		t.Fatalf("result = %+v", result)
	}
	body, err := os.ReadFile(filepath.Join(projectDir, ProjectFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "多模态生视频V5") {
		t.Fatalf("rebuilt vocabulary missing generated term: %s", body)
	}
	if got := service.Complete("多模", 5); len(got) != 1 || got[0].Text != "多模态生视频V5" {
		t.Fatalf("session vocabulary was not refreshed: %+v", got)
	}
}

func TestValidTermAllowsShortMultiWordPhraseButRejectsProse(t *testing.T) {
	if !validTerm("approval cannot expand authority") {
		t.Fatal("short four-word policy phrase should be valid")
	}
	if validTerm("one two three four five six seven") {
		t.Fatal("seven-word prose should remain invalid")
	}
}
