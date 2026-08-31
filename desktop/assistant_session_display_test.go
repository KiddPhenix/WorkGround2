package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/provider"
)

// TestManagedSessionHistoryShowsIntentSkillAndSteerOnce is the regression for
// the 委托 Session content-visibility bug: the raw delegation (IntentPrompt) is
// the first user message (never the internal execution envelope), an already
// persisted run_skill call keeps its displayable subject, and a mid-turn steer
// surfaces exactly once as a notice — never as a user bubble or a duplicate.
func TestManagedSessionHistoryShowsIntentSkillAndSteerOnce(t *testing.T) {
	const intent = "扫描项目最近修改并跑测试"
	envelope := assistant.ManagedSessionPrompt(assistant.Snapshot{Assistant: assistant.Assistant{Name: "Helper", ID: "a-1"}}, intent)

	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: envelope, Origin: provider.MessageOriginUser},
		{Role: provider.RoleAssistant, Content: "开始", ToolCalls: []provider.ToolCall{{
			ID: "call_skill", Name: "run_skill", Arguments: `{"name":"code-reviewer","arguments":"review this branch"}`,
		}}},
		{Role: provider.RoleTool, Name: "run_skill", ToolCallID: "call_skill", Content: "Skill completed"},
		{Role: provider.RoleUser, Content: agent.MidTurnSteerPrefix + "\n再检查一遍", Origin: provider.MessageOriginHost},
		{Role: provider.RoleAssistant, Content: "完成"},
	}

	// No display sidecar exists: this is the legacy/already-running Session path.
	resolve := sessionDisplayResolverFromMap(sessionDisplayMap{}, "legacy-managed.jsonl")
	got := historyMessagesWithPlannerDisplays(msgs, resolve, nil, nil)

	// First user message is the raw delegation, never the internal envelope.
	if len(got) == 0 || got[0].Role != "user" {
		t.Fatalf("first history message = %+v, want the raw delegation user message", got)
	}
	if got[0].Content != intent {
		t.Fatalf("first user content = %q, want %q", got[0].Content, intent)
	}
	if strings.Contains(got[0].Content, "执行契约") {
		t.Fatalf("first user content leaked the internal execution envelope: %q", got[0].Content)
	}

	// The run_skill call stays visible with its displayable subject; its bulky
	// arguments stay archived (the pre-existing product behaviour, #4044).
	var sawSkillCall, sawSkillResult bool
	var steerNotices int
	for _, m := range got {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, call := range m.ToolCalls {
				if call.Name == "run_skill" {
					sawSkillCall = true
					if !call.ArgumentsArchived || call.Arguments != "" {
						t.Fatalf("run_skill arguments should be archived: %+v", call)
					}
					if call.Subject != "code-reviewer" {
						t.Fatalf("run_skill subject = %q, want code-reviewer", call.Subject)
					}
				}
			}
		}
		if m.Role == "tool" && m.ToolName == "run_skill" {
			sawSkillResult = true
		}
		if m.Role == "notice" && strings.Contains(m.Content, "再检查一遍") {
			steerNotices++
		}
	}
	if !sawSkillCall || !sawSkillResult {
		t.Fatalf("run_skill call/result missing from history: %+v", got)
	}
	if steerNotices != 1 {
		t.Fatalf("steer notices = %d, want exactly 1: %+v", steerNotices, got)
	}
}

// TestManagedSessionCreateDisplaysRawIntent proves the Create submit path keeps
// the model input (the full managed-context envelope) separate from the
// persisted display text (the raw delegation). It drives SubmitDisplayToTab —
// the exact call appAssistantSessionControl.Create now makes — and asserts the
// replayable history shows the intent while the model saw the envelope.
func TestManagedSessionCreateDisplaysRawIntent(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := config.SessionDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "managed-display.jsonl")
	sess := agent.NewSession("sys")
	exec := agent.New(nil, nil, sess, agent.Options{}, event.Discard)
	runner := &appendingDesktopRunner{session: sess, started: make(chan string, 1)}
	ctrl := control.New(control.Options{
		Runner:      runner,
		Executor:    exec,
		Sink:        event.Discard,
		SessionDir:  dir,
		SessionPath: path,
		Label:       "test",
	})
	defer ctrl.Close()

	app := NewApp()
	app.setTestCtrl(ctrl, "deepseek/test")

	const intent = "扫描项目最近修改并跑测试"
	const envelope = "你正在执行一个长期 Assistant 委派的受管 Session。\n\n执行契约：\n1. 工作区任务先调用 project_status…"
	if err := app.SubmitDisplayToTab("test", intent, envelope); err != nil {
		t.Fatalf("SubmitDisplayToTab: %v", err)
	}
	modelInput := <-runner.started
	waitNotRunning(t, ctrl)

	if !strings.Contains(modelInput, "执行契约") {
		t.Fatalf("model input lost the managed envelope: %q", modelInput)
	}

	got := app.HistoryForTab("test")
	var userContent string
	for _, m := range got {
		if m.Role == "user" {
			userContent = m.Content
			break
		}
	}
	if userContent != intent {
		t.Fatalf("replayed first user content = %q, want %q", userContent, intent)
	}
	if strings.Contains(userContent, "执行契约") {
		t.Fatalf("replayed first user content leaked the envelope: %q", userContent)
	}
}
