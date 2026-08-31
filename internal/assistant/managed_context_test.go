package assistant

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestManagedSessionPromptCarriesBoundedDurableContext(t *testing.T) {
	now := time.Date(2026, 8, 28, 7, 0, 0, 0, time.UTC)
	snapshot := Snapshot{
		Revision:     9,
		Assistant:    Assistant{ID: "helper", Name: "项目助手", Mission: "维护发布质量", Scope: ScopeWorkspace, WorkspaceRoot: `D:\Work\demo`, Revision: 3},
		Plan:         Plan{Revision: 4, Responsibilities: []Responsibility{{Alias: "release", Status: RespReady, Objective: "完成发布", DoneCriteria: "测试通过", NextAction: "运行测试"}}},
		Memory:       Memory{Revision: 5, Items: []MemoryItem{{ID: "mem-1", Kind: MemoryStrategy, Body: "先跑冒烟", SourceRun: "session-old", Evidence: "test.log", UpdatedAt: now}}},
		ContextPacks: []ContextPack{{ID: "pack-1", DispatchID: "dispatch-old", Revision: 6, Conclusion: "构建已完成", Evidence: []string{"dist/app.exe"}, OpenLoops: []string{"待签名"}}},
		Artifacts:    []Artifact{{ID: "artifact-1", Kind: "binary", Title: "安装包", Content: "dist/app.exe", Evidence: "sha256:abc", Revision: 7}},
	}
	got := ManagedSessionPrompt(snapshot, "继续发布")
	for _, want := range []string{"维护发布质量", `D:\Work\demo`, "继续发布", "project_status", "memory_search", "session_read", "memory_remember", "完成标准：测试通过", "先跑冒烟", "构建已完成", "dist/app.exe", "<assistant-progress>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("managed prompt missing %q:\n%s", want, got)
		}
	}
	if got2 := ManagedSessionPrompt(snapshot, "继续发布"); got2 != got {
		t.Fatal("stable snapshot produced a different prompt")
	}
}

func TestManagedSessionIntentRecoversLegacyEnvelope(t *testing.T) {
	prompt := ManagedSessionPrompt(Snapshot{Assistant: Assistant{Name: "Helper", ID: "a-1"}}, "  扫描项目最近修改并跑测试  ")
	got, ok := ManagedSessionIntent(prompt)
	if !ok || got != "扫描项目最近修改并跑测试" {
		t.Fatalf("intent = %q, ok = %v", got, ok)
	}
	for _, ordinary := range []string{
		"原始任务：\n普通消息",
		managedPromptIntro + "\n\n原始任务：\n缺少 Assistant 身份与执行契约",
		managedPromptIntro + "\n\nAssistant：Helper（a-1）\n原始任务：\n缺少执行契约",
	} {
		if got, ok := ManagedSessionIntent(ordinary); ok || got != "" {
			t.Fatalf("ordinary message parsed as managed intent: %q", ordinary)
		}
	}
}

func TestManagedSessionPromptIsUTF8SafeAndBounded(t *testing.T) {
	long := strings.Repeat("界", managedPromptMaxBytes)
	snapshot := Snapshot{
		Assistant: Assistant{ID: long, Name: long, Mission: long, Scope: ScopeWorkspace, WorkspaceRoot: long},
		Memory:    Memory{Items: []MemoryItem{{ID: "m", Kind: MemoryFact, Body: long}}},
		Artifacts: []Artifact{{ID: "a", Title: long, Content: long, Evidence: long}},
	}
	got := ManagedSessionPrompt(snapshot, long)
	if len(got) > managedPromptMaxBytes {
		t.Fatalf("prompt bytes = %d, max = %d", len(got), managedPromptMaxBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("prompt truncation split a UTF-8 rune")
	}
}

func TestDispatcherRoutesContextDependentQuestionsToManagedTask(t *testing.T) {
	prompt := DispatcherPrompt(Snapshot{Assistant: Assistant{Mission: "维护项目"}}, "以前的成果在哪")
	for _, want := range []string{"历史 Session", "即使是问句也必须分类为 task", "jobs 是兼容字段", `"jobs":[]`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("dispatcher prompt missing %q:\n%s", want, prompt)
		}
	}
}
