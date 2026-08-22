package vocabulary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServiceMergesWorkspaceSkillAgentAndLearnedTerms(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	projectDir := filepath.Join(root, ".WorkGround2")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project := `version = 1
[[terms]]
text = "多模态生视频V5"
kind = "noun"
description = "项目描述"
aliases = ["多模V5"]
preferred = true
`
	if err := os.WriteFile(filepath.Join(projectDir, ProjectFile), []byte(project), 0o644); err != nil {
		t.Fatal(err)
	}
	skillDir := filepath.Join(root, ".WorkGround2", "skills", "video")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, SidecarFile), []byte(`[[terms]]
text = "角色设定Pro"
kind = "noun"
description = "Skill 描述"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{
		WorkspaceRoot: root,
		StateDir:      state,
		Skills:        []SkillSource{{Name: "video", Path: filepath.Join(skillDir, "SKILL.md"), Terms: []string{"运镜 — Skill 动词"}}},
		Agents:        []AgentSource{{Name: "AGENTS.md", Path: filepath.Join(root, "AGENTS.md"), Body: "---\nvocabulary: 多模态生视频V5\n---\n# 词汇表\n这里是词汇表的介绍文字\n- 批量跑图 — Agent 动词\n"}},
	})

	matches := svc.Complete("多模", 8)
	if len(matches) != 1 || matches[0].Text != "多模态生视频V5" || matches[0].Suffix != "态生视频V5" {
		t.Fatalf("workspace completion = %+v", matches)
	}
	if matches[0].Source != "agent" {
		t.Fatalf("higher-priority agent source not selected: %+v", matches[0])
	}
	if got := svc.Complete("角色", 8); len(got) != 1 || got[0].Source != "skill" {
		t.Fatalf("skill sidecar completion = %+v", got)
	}
	if got := svc.Complete("批量", 8); len(got) != 1 || got[0].Text != "批量跑图" {
		t.Fatalf("agent heading completion = %+v", got)
	}
	if got := svc.Complete("这里", 8); len(got) != 0 {
		t.Fatalf("agent prose leaked into vocabulary: %+v", got)
	}
}

func TestObserveIsIdempotentPersistsAndRanksUse(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	svc := New(Options{WorkspaceRoot: root, StateDir: state})
	text := "这里的专有节点叫做“多模态生视频V5”，另一个版本是 角色设定Pro。"
	if err := svc.Observe("session:1:user", text, "user"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Observe("session:1:user", text, "user"); err != nil {
		t.Fatal(err)
	}
	got := svc.Complete("多模", 8)
	if len(got) != 1 || got[0].Text != "多模态生视频V5" {
		t.Fatalf("learned completion = %+v", got)
	}
	if err := svc.RecordUse(got[0].ID, "use-1"); err != nil {
		t.Fatal(err)
	}
	if err := svc.RecordUse(got[0].ID, "use-1"); err != nil {
		t.Fatal(err)
	}
	reloaded := New(Options{WorkspaceRoot: root, StateDir: state})
	reloaded.mu.RLock()
	entry := reloaded.learned.Terms[keyOf("多模态生视频V5")]
	reloaded.mu.RUnlock()
	if entry == nil || entry.Evidence != 1 || entry.UseCount != 1 {
		t.Fatalf("persisted learned entry = %+v", entry)
	}
}

func TestRecordUseCreatesOverlayForExplicitTerm(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", ProjectFile), []byte(`[[terms]]
text = "多模态生视频V5"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "state")
	svc := New(Options{WorkspaceRoot: root, StateDir: state})
	match := svc.Complete("多模", 1)[0]
	if err := svc.RecordUse(match.ID, "use-explicit"); err != nil {
		t.Fatal(err)
	}
	reloaded := New(Options{WorkspaceRoot: root, StateDir: state})
	entry := reloaded.learned.Terms[keyOf(match.Text)]
	if entry == nil || entry.UseCount != 1 {
		t.Fatalf("explicit use overlay = %+v", entry)
	}
}

func TestServicesMergeConcurrentWorkspaceWrites(t *testing.T) {
	root := t.TempDir()
	state := filepath.Join(t.TempDir(), "state")
	first := New(Options{WorkspaceRoot: root, StateDir: state})
	second := New(Options{WorkspaceRoot: root, StateDir: state})
	if err := first.Observe("event-1", "使用“多模态生视频V5”", "user"); err != nil {
		t.Fatal(err)
	}
	if err := second.Observe("event-2", "使用“角色设定Pro”", "assistant"); err != nil {
		t.Fatal(err)
	}
	if got := first.Complete("角色", 5); len(got) != 1 {
		t.Fatalf("existing Session did not refresh workspace terms: %+v", got)
	}
	reloaded := New(Options{WorkspaceRoot: root, StateDir: state})
	if len(reloaded.Complete("多模", 5)) != 1 || len(reloaded.Complete("角色", 5)) != 1 {
		t.Fatalf("cross-service writes were lost: %+v", reloaded.learned.Terms)
	}
}

func TestExtractRejectsOrdinarySentencesAndFindsDistinctiveTerms(t *testing.T) {
	got := Extract("请处理普通中文描述，它是一个名词的时候，并调用 多模态生视频V5 和 ImagePipelinePro；动词：跑图。密钥是“sk-1234567890abcdef1234567890”。")
	texts := map[string]Kind{}
	for _, entry := range got {
		texts[entry.Text] = entry.Kind
	}
	for _, want := range []string{"多模态生视频V5", "ImagePipelinePro", "跑图"} {
		if _, ok := texts[want]; !ok {
			t.Fatalf("missing %q in %+v", want, got)
		}
	}
	if _, noisy := texts["请处理普通中文描述"]; noisy {
		t.Fatalf("ordinary sentence leaked into terms: %+v", got)
	}
	if _, noisy := texts["的时候"]; noisy {
		t.Fatalf("noun prose leaked into terms: %+v", got)
	}
	if _, secret := texts["sk-1234567890abcdef1234567890"]; secret {
		t.Fatalf("secret-like token leaked into terms: %+v", got)
	}
}

func TestContextInjectsOnlyMentionedDefinedTerms(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", ProjectFile), []byte(`[[terms]]
text = "多模态生视频V5"
kind = "noun"
description = "多模态视频生成节点"
[[terms]]
text = "角色设定Pro"
description = "角色生成节点"
`), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{WorkspaceRoot: root})
	context := svc.Context("请使用多模态生视频V5", 5)
	if !strings.Contains(context, "多模态视频生成节点") || strings.Contains(context, "角色生成节点") {
		t.Fatalf("context = %q", context)
	}
}

func TestMinimumPrefixUsesRunes(t *testing.T) {
	svc := &Service{entries: []Entry{{ID: "x", Text: "多模态生视频V5", Kind: KindNoun}}}
	if got := svc.Complete("多", 8); len(got) != 0 {
		t.Fatalf("one CJK rune should not complete: %+v", got)
	}
	if got := svc.Complete("多模", 8); len(got) != 1 {
		t.Fatalf("two CJK runes should complete: %+v", got)
	}
}

func TestMalformedOptionalVocabularyIsVisibleAndOtherSourcesSurvive(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".WorkGround2", ProjectFile), []byte("[[terms]]\ntext = \"bad\"\nkind = \"object\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := New(Options{WorkspaceRoot: root, Skills: []SkillSource{{Name: "valid", Terms: []string{"角色设定Pro"}}}})
	if len(svc.Warnings()) != 1 {
		t.Fatalf("warnings = %+v", svc.Warnings())
	}
	if got := svc.Complete("角色", 5); len(got) != 1 {
		t.Fatalf("valid source was lost after malformed optional source: %+v", got)
	}
}
