package vocabulary

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRebuildProjectPreservesAuthoredTermsAndIsIdempotent(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".WorkGround2", ProjectFile)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	authored := "# keep this comment\nversion = 1\n\n[[terms]]\ntext = \"手工术语\"\nkind = \"noun\"\n"
	if err := os.WriteFile(target, []byte(authored), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("项目节点叫做多模态生视频V5。"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := RebuildProject(root)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Updated || first.Generated != 1 || first.Scanned != 1 {
		t.Fatalf("first rebuild = %+v", first)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(body, []byte(authored)) || !bytes.Contains(body, []byte("多模态生视频V5")) || !bytes.Contains(body, []byte(generatedBegin)) {
		t.Fatalf("rebuilt vocabulary did not preserve authored prefix or generated term:\n%s", body)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}

	second, err := RebuildProject(root)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(target)
	if second.Updated || !bytes.Equal(body, after) {
		t.Fatalf("second rebuild should be byte-identical: %+v", second)
	}
}

func TestRebuildProjectReplacesOldGeneratedSection(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "notes.md")
	if err := os.WriteFile(file, []byte("功能：旧节点V5"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildProject(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("功能：新节点V6"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildProject(root); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(root, ".WorkGround2", ProjectFile))
	if bytes.Contains(body, []byte("旧节点V5")) || !bytes.Contains(body, []byte("新节点V6")) {
		t.Fatalf("generated section was not replaced:\n%s", body)
	}
}

func TestRebuildProjectDoesNotOverwriteMalformedAuthoredFile(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".WorkGround2", ProjectFile)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("[[terms]\ntext = broken\n")
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RebuildProject(root); err == nil {
		t.Fatal("malformed authored TOML should fail")
	}
	after, _ := os.ReadFile(target)
	if !bytes.Equal(original, after) {
		t.Fatalf("malformed authored file was overwritten: %q", after)
	}
}

func TestRebuildWorkspaceReloadsCurrentSession(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "terms.md"), []byte("名词：会话热更新V7"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{WorkspaceRoot: root, StateDir: filepath.Join(t.TempDir(), "state")})
	result, err := svc.RebuildWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	if result.Added != 1 || len(svc.Complete("会话", 5)) != 1 {
		t.Fatalf("current session was not reloaded: result=%+v matches=%+v", result, svc.Complete("会话", 5))
	}
}

func TestActivateSkillReplacesVocabularyIdempotently(t *testing.T) {
	svc := New(Options{WorkspaceRoot: t.TempDir(), StateDir: filepath.Join(t.TempDir(), "state")})
	first := svc.ActivateSkill(SkillSource{Name: "gpt", Terms: []string{"多模态生视频V5"}})
	if first.Added != 1 || len(svc.Complete("多模", 5)) != 1 {
		t.Fatalf("first activation failed: %+v", first)
	}
	second := svc.ActivateSkill(SkillSource{Name: "GPT", Terms: []string{"角色设定Pro"}})
	if second.Added != 1 || len(svc.Complete("多模", 5)) != 0 || len(svc.Complete("角色", 5)) != 1 {
		t.Fatalf("replacement activation failed: %+v", second)
	}
	if strings.TrimSpace(second.Skill) != "GPT" {
		t.Fatalf("skill = %q", second.Skill)
	}
}
