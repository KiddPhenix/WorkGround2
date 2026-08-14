package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/skill"
	"workground2/internal/vocabulary"
)

func TestComposeInjectsOnlyMentionedVocabularyDefinition(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", vocabulary.ProjectFile), []byte(`[[terms]]
text = "多模态生视频V5"
description = "工作区的视频生成节点"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl := New(Options{WorkspaceRoot: root, Vocabulary: vocabulary.New(vocabulary.Options{WorkspaceRoot: root})})
	composed := ctrl.Compose("请使用多模态生视频V5")
	if !strings.Contains(composed, "<workspace-vocabulary>") || !strings.Contains(composed, "工作区的视频生成节点") {
		t.Fatalf("Compose did not inject matching vocabulary: %q", composed)
	}
	if got := ctrl.Compose("普通问题"); strings.Contains(got, "<workspace-vocabulary>") {
		t.Fatalf("Compose injected unrelated vocabulary: %q", got)
	}
}

func TestObserveVocabularyUsesStableEventIdentity(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	svc := vocabulary.New(vocabulary.Options{WorkspaceRoot: t.TempDir(), StateDir: state})
	ctrl := New(Options{SessionPath: "session.jsonl", Vocabulary: svc})
	ctrl.observeVocabulary(3, "user", "使用“多模态生视频V5”")
	ctrl.observeVocabulary(3, "user", "使用“多模态生视频V5”")
	got := ctrl.CompleteVocabulary("多模", 5)
	if len(got) != 1 || got[0].Text != "多模态生视频V5" {
		t.Fatalf("completion = %+v", got)
	}
}

func TestActivateDynamicallyDiscoveredSkillVocabulary(t *testing.T) {
	root := t.TempDir()
	store := skill.New(skill.Options{ProjectRoot: root, HomeDir: t.TempDir(), DisableBuiltins: true})
	svc := vocabulary.New(vocabulary.Options{WorkspaceRoot: root, StateDir: filepath.Join(t.TempDir(), "state")})
	ctrl := New(Options{WorkspaceRoot: root, SkillStore: store, Vocabulary: svc})
	skillDir := filepath.Join(root, ".WorkGround2", "skills", "GPT")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: GPT
description: delegated workflow
vocabulary: 多模态生视频V5
---
Use GPT.
`), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := ctrl.ActivateSkillVocabulary("gpt")
	if err != nil {
		t.Fatal(err)
	}
	if result.Skill != "GPT" || len(ctrl.CompleteVocabulary("多模", 5)) != 1 {
		t.Fatalf("dynamic activation = %+v, matches = %+v", result, ctrl.CompleteVocabulary("多模", 5))
	}
}
