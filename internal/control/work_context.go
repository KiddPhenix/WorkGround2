package control

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"workground2/internal/work"
)

const (
	workChatContextMaxBytes = 8000
	workChatFieldMaxBytes   = 1000
	workChatItemMaxBytes    = 600
	workChatListMaxItems    = 32
)

// BuildWorkChatContext returns a deterministic, bounded snapshot for one Work
// chat turn. Runtime failures and waits are placed before planning details so
// the most actionable state survives truncation.
func BuildWorkChatContext(view *work.WorkView) string {
	if view == nil || view.Work == nil {
		return ""
	}
	sections := []string{
		buildWorkIdentity(view),
		buildWorkTasks(view),
		buildWorkBlock(view),
		buildWorkDefinition(view),
		buildWorkInputs(view),
		buildWorkArtifacts(view),
	}
	var kept []string
	for _, value := range sections {
		if value = strings.TrimSpace(value); value != "" {
			kept = append(kept, value)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	return truncateContext(strings.Join(kept, "\n\n")+"\n", workChatContextMaxBytes)
}

func buildWorkIdentity(view *work.WorkView) string {
	var b strings.Builder
	w := view.Work
	section(&b, "Current Work")
	kv(&b, "ID", w.ID)
	kv(&b, "Name", w.Name)
	kv(&b, "State", string(w.State))
	kv(&b, "Revision", fmt.Sprintf("%d", view.Revision))
	if w.V2CurrentRevision > 0 {
		kv(&b, "DefinitionRev", fmt.Sprintf("%d", w.V2CurrentRevision))
	}
	kv(&b, "OriginalPrompt", w.Prompt)
	return b.String()
}

func buildWorkTasks(view *work.WorkView) string {
	var lines []string
	seen := make(map[string]struct{}, len(view.Tasks))
	for _, task := range view.Tasks {
		if task.State == work.TaskPending || task.State == work.TaskCanceled {
			continue
		}
		seen[task.ID] = struct{}{}
		line := fmt.Sprintf("- %s (%s): %s",
			safeText(task.ID, 160),
			task.State,
			safeText(task.Title, 240),
		)
		if task.Error != "" {
			line += " — Error: " + safeText(task.Error, workChatItemMaxBytes)
		}
		if task.Progress != "" {
			line += " — Progress: " + safeText(task.Progress, workChatItemMaxBytes)
		}
		if len(task.WaitingInputIDs) > 0 {
			line += " — Waiting inputs: " + safeText(strings.Join(task.WaitingInputIDs, ", "), 400)
		}
		if task.Retryable {
			line += " — retryable"
		}
		lines = append(lines, safeText(line, workChatItemMaxBytes))
	}

	keys := make([]string, 0, len(view.Work.V2TaskRuntimes))
	for key := range view.Work.V2TaskRuntimes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		runtime := view.Work.V2TaskRuntimes[key]
		if runtime == nil || runtime.State == work.TaskPending || runtime.State == work.TaskCanceled {
			continue
		}
		_, alreadyProjected := seen[key]
		line := fmt.Sprintf("- %s runtime (%s)", safeText(key, 160), runtime.State)
		if runtime.Error != "" {
			line += " — Error: " + safeText(runtime.Error, workChatItemMaxBytes)
		}
		if runtime.Progress != "" {
			line += " — Progress: " + safeText(runtime.Progress, workChatItemMaxBytes)
		}
		if len(runtime.WaitingInputIDs) > 0 {
			line += " — Waiting inputs: " + safeText(strings.Join(runtime.WaitingInputIDs, ", "), 400)
		}
		if len(runtime.Attempts) > 0 {
			last := runtime.Attempts[len(runtime.Attempts)-1]
			if last.Error != "" && !strings.Contains(line, last.Error) {
				line += " — Latest attempt error: " + safeText(last.Error, workChatItemMaxBytes)
			}
		}
		if !alreadyProjected || runtime.Error != "" || runtime.Progress != "" ||
			len(runtime.WaitingInputIDs) > 0 || len(runtime.Attempts) > 0 {
			lines = append(lines, safeText(line, workChatItemMaxBytes))
		}
	}
	return listSection("Run Status", lines)
}

func buildWorkBlock(view *work.WorkView) string {
	if view.RunBlock == nil || !view.RunBlock.Blocked {
		return ""
	}
	lines := make([]string, 0, len(view.RunBlock.Items))
	for _, item := range view.RunBlock.Items {
		line := "- " + string(item.Code)
		if item.Detail != "" {
			line += ": " + safeText(item.Detail, workChatItemMaxBytes)
		}
		lines = append(lines, line)
	}
	return listSection("Blocked", lines)
}

func buildWorkDefinition(view *work.WorkView) string {
	if view.Definition == nil {
		return ""
	}
	var b strings.Builder
	section(&b, "Definition")
	kv(&b, "Goal", view.Definition.Goal)
	nodes := view.Definition.Nodes
	limit := min(len(nodes), workChatListMaxItems)
	for _, node := range nodes[:limit] {
		line := fmt.Sprintf("- %s: %s", safeText(node.ID, 160), safeText(node.Title, 240))
		if node.Description != "" {
			line += " — " + safeText(node.Description, workChatItemMaxBytes)
		}
		b.WriteString(safeText(line, workChatItemMaxBytes))
		b.WriteByte('\n')
	}
	appendOmitted(&b, len(nodes)-limit)
	return b.String()
}

func buildWorkInputs(view *work.WorkView) string {
	lines := make([]string, 0, len(view.Inputs))
	limit := min(len(view.Inputs), workChatListMaxItems)
	for _, input := range view.Inputs[:limit] {
		line := fmt.Sprintf("- %s: %s", safeText(input.ID, 160), input.State)
		if input.CornerstoneID != "" {
			line += " — pinned"
		}
		if input.Error != "" {
			line += " — Error: " + safeText(input.Error, workChatItemMaxBytes)
		}
		value := strings.TrimSpace(string(input.Value))
		if value != "" && value != "null" && value != `""` {
			line += " — Value: " + safeText(value, 300)
		}
		lines = append(lines, safeText(line, workChatItemMaxBytes))
	}
	return listSectionWithOmitted("Inputs", lines, len(view.Inputs)-limit)
}

func buildWorkArtifacts(view *work.WorkView) string {
	var lines []string
	for _, slot := range view.ArtifactSlots {
		if slot.State == work.SlotReserved {
			continue
		}
		line := fmt.Sprintf("- %s (%s): %s",
			safeText(slot.ID, 160),
			safeText(slot.Title, 240),
			slot.State,
		)
		lines = append(lines, safeText(line, workChatItemMaxBytes))
	}
	omitted := 0
	if len(lines) > workChatListMaxItems {
		omitted = len(lines) - workChatListMaxItems
		lines = lines[:workChatListMaxItems]
	}
	return listSectionWithOmitted("Artifacts", lines, omitted)
}

func section(b *strings.Builder, heading string) {
	b.WriteString("## ")
	b.WriteString(heading)
	b.WriteByte('\n')
}

func kv(b *strings.Builder, key, value string) {
	value = safeText(value, workChatFieldMaxBytes)
	if value == "" {
		return
	}
	b.WriteString("- ")
	b.WriteString(key)
	b.WriteString(": ")
	b.WriteString(value)
	b.WriteByte('\n')
}

func listSection(heading string, lines []string) string {
	return listSectionWithOmitted(heading, lines, 0)
}

func listSectionWithOmitted(heading string, lines []string, omitted int) string {
	if len(lines) == 0 && omitted == 0 {
		return ""
	}
	var b strings.Builder
	section(&b, heading)
	limit := min(len(lines), workChatListMaxItems)
	for _, line := range lines[:limit] {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	appendOmitted(&b, omitted+len(lines)-limit)
	return b.String()
}

func appendOmitted(b *strings.Builder, omitted int) {
	if omitted > 0 {
		fmt.Fprintf(b, "- … %d more omitted\n", omitted)
	}
}

func safeText(value string, maxBytes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, "�"))
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	const marker = "…"
	cut := maxBytes - len(marker)
	if cut < 0 {
		return ""
	}
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + marker
}

func truncateContext(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	const marker = "\n[Context truncated]\n"
	if maxBytes <= len(marker) {
		return safeText(marker, maxBytes)
	}
	cut := maxBytes - len(marker)
	for cut > 0 && !utf8.ValidString(value[:cut]) {
		cut--
	}
	return value[:cut] + marker
}
