package agent

import (
	"strings"

	"workground2/internal/provider"
	"workground2/internal/skill"
)

const protectedToolCallBlockedArgs = `{"protected_output_blocked":true}`

func redactProtectedText(s string) string {
	return skill.RedactProtectedContent(s)
}

func ProtectedOutputBlockedText() string {
	return skill.ProtectedOutputBlocked
}

func sanitizeMessageForPersistence(m provider.Message) provider.Message {
	m.Content = redactProtectedText(m.Content)
	m.ReasoningContent = redactProtectedText(m.ReasoningContent)
	for i := range m.ToolCalls {
		m.ToolCalls[i].Arguments = redactProtectedText(m.ToolCalls[i].Arguments)
		m.ToolCalls[i].Diff = redactProtectedText(m.ToolCalls[i].Diff)
	}
	return m
}

func (a *Agent) hasProtectedSkillContext() bool {
	if a == nil || a.session == nil {
		return false
	}
	for _, m := range a.session.Messages {
		if skill.ContainsProtectedContent(m.Content) || skill.ContainsProtectedContent(m.ReasoningContent) {
			return true
		}
	}
	return false
}

func (a *Agent) guardProtectedOutput(text string) (string, bool) {
	if a == nil || a.session == nil || !a.hasProtectedSkillContext() {
		return text, false
	}
	if skill.LooksLikeCredential(text) {
		return skill.ProtectedOutputBlocked, true
	}
	for _, m := range a.session.Messages {
		if skill.ProtectedOutputLeaks(m.Content, text) || skill.ProtectedOutputLeaks(m.ReasoningContent, text) {
			return skill.ProtectedOutputBlocked, true
		}
	}
	return text, false
}

func protectedToolCallBlocked(call provider.ToolCall) bool {
	return strings.Contains(call.Arguments, `"protected_output_blocked":true`)
}

func (a *Agent) guardProtectedToolCalls(calls []provider.ToolCall) ([]provider.ToolCall, bool) {
	if len(calls) == 0 || a == nil || !a.hasProtectedSkillContext() {
		return calls, false
	}
	var out []provider.ToolCall
	blockedAny := false
	for i, call := range calls {
		if _, blocked := a.guardProtectedOutput(call.Arguments + "\n" + call.Diff); !blocked {
			continue
		}
		if out == nil {
			out = append([]provider.ToolCall(nil), calls...)
		}
		out[i].Arguments = protectedToolCallBlockedArgs
		out[i].Diff = ""
		out[i].Added = 0
		out[i].Removed = 0
		blockedAny = true
	}
	if out != nil {
		return out, blockedAny
	}
	return calls, blockedAny
}
