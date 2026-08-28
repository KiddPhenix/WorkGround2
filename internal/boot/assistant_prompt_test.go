package boot

import (
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/control"
)

func TestAssistantSystemPromptStripsCodingAgentRole(t *testing.T) {
	host := config.DefaultSystemPrompt + "\n\n# My policy\ncustom rules"
	got := assistantSystemPrompt(host)
	if strings.Contains(got, "coding agent focused on executing code tasks") {
		t.Fatalf("assistant prompt still contains the coding-agent role:\n%s", got)
	}
	if !strings.Contains(got, "long-running outcome executor") {
		t.Fatalf("assistant prompt missing the executor role:\n%s", got)
	}
	if !strings.Contains(got, "custom rules") {
		t.Fatalf("assistant prompt dropped user policy context:\n%s", got)
	}
	if !strings.Contains(got, "live_web") {
		t.Fatalf("assistant prompt must state the live_web evidence rule:\n%s", got)
	}
}

func TestAssistantSystemPromptEmptyHost(t *testing.T) {
	got := assistantSystemPrompt("")
	if got != AssistantSystemPrompt {
		t.Fatalf("empty host returned %q, want the raw AssistantSystemPrompt", got)
	}
}

func TestAssistantSystemPromptNeverSilentlyLocalInspection(t *testing.T) {
	got := assistantSystemPrompt("")
	if !strings.Contains(got, "Never silently replace requested live website inspection with local cache") {
		t.Fatalf("assistant prompt missing the no-local-substitution rule:\n%s", got)
	}
}

func TestAssistantSystemPromptPrefersAutonomousExecution(t *testing.T) {
	got := assistantSystemPrompt("")
	if !strings.Contains(got, "Use allowed, reversible tools without") || !strings.Contains(got, "decision that genuinely belongs to them") {
		t.Fatalf("assistant prompt missing autonomous execution rule:\n%s", got)
	}
}

func TestAssistantSkipsCodingOnlyAnchoredBootstrap(t *testing.T) {
	if shouldUseAnchoredBootstrap(true, true, agent.SessionKindAssistant) {
		t.Fatal("Assistant first turn must expose the full tool catalog")
	}
	for _, kind := range []agent.SessionKind{"", agent.SessionKindNormal, agent.SessionKindWork, agent.SessionKindCollaboration} {
		if !shouldUseAnchoredBootstrap(true, true, kind) {
			t.Fatalf("ordinary session kind %q unexpectedly lost anchored bootstrap", kind)
		}
	}
	if shouldUseAnchoredBootstrap(false, true, agent.SessionKindNormal) {
		t.Fatal("disabled bootstrap was enabled")
	}
	if shouldUseAnchoredBootstrap(true, false, agent.SessionKindNormal) {
		t.Fatal("non-DeepSeek session enabled DeepSeek bootstrap")
	}
}

func TestAssistantSessionDefaultsToAutoApproval(t *testing.T) {
	assistantCtrl := control.New(control.Options{Label: "assistant"})
	defer assistantCtrl.Close()
	applySessionApprovalDefault(assistantCtrl, agent.SessionKindAssistant)
	if got := assistantCtrl.ToolApprovalMode(); got != control.ToolApprovalAuto {
		t.Fatalf("assistant approval mode = %q, want auto", got)
	}

	normalCtrl := control.New(control.Options{Label: "normal"})
	defer normalCtrl.Close()
	applySessionApprovalDefault(normalCtrl, agent.SessionKindNormal)
	if got := normalCtrl.ToolApprovalMode(); got != control.ToolApprovalAsk {
		t.Fatalf("normal approval mode = %q, want ask", got)
	}
}
