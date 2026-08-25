package control

import (
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/provider"
)

func TestHistoryStripsAssistantProgressBlocks(t *testing.T) {
	exec := agent.New(nil, nil, agent.NewSession("sys"), agent.Options{}, event.Discard)
	exec.Session().Add(provider.Message{Role: provider.RoleUser, Content: "scan the project"})
	exec.Session().Add(provider.Message{
		Role:    provider.RoleAssistant,
		Content: "Scan complete\n<assistant-progress>{\"complete\":[\"scan\"]}</assistant-progress>",
	})
	c := New(Options{Executor: exec, SessionDir: t.TempDir(), Label: "test"})
	msgs := c.History()
	if len(msgs) != 3 {
		t.Fatalf("history length = %d, want 3", len(msgs))
	}
	if got := msgs[2].Content; strings.Contains(got, "<assistant-progress>") {
		t.Fatalf("history leaked raw protocol: %q", got)
	}
	if !strings.Contains(msgs[2].Content, "Scan complete") {
		t.Fatalf("history stripped the visible answer too: %q", msgs[2].Content)
	}
}
