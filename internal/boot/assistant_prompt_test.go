package boot

import (
	"strings"
	"testing"

	"workground2/internal/config"
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
