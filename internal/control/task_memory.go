package control

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/fileutil"
	"workground2/internal/store"
)

// taskMemoryState is the single owner of the recoverable task briefing. It is
// kept outside Controller.mu so small sidecar writes never extend run-state
// critical sections.
type taskMemoryState struct {
	mu    sync.Mutex
	value event.TaskMemory
}

type taskMemoryPatch struct {
	goal, current, nextStep                   *string
	goalSource, currentSource, nextStepSource *string
}

func stringPtr(v string) *string { return &v }

// TaskMemory returns an immutable snapshot for any frontend.
func (c *Controller) TaskMemory() event.TaskMemory {
	c.taskMemory.mu.Lock()
	defer c.taskMemory.mu.Unlock()
	return c.taskMemory.value
}

func (c *Controller) updateTaskMemory(p taskMemoryPatch) event.TaskMemory {
	c.taskMemory.mu.Lock()
	old := c.taskMemory.value
	next := old
	if key := taskMemorySessionKey(c.SessionPath()); key != "" {
		next.SessionKey = key
	}
	if p.goal != nil {
		next.Goal = strings.TrimSpace(*p.goal)
	}
	if p.current != nil {
		next.Current = strings.TrimSpace(*p.current)
	}
	if p.nextStep != nil {
		next.NextStep = strings.TrimSpace(*p.nextStep)
	}
	if p.goalSource != nil {
		next.GoalSource = strings.TrimSpace(*p.goalSource)
	}
	if p.currentSource != nil {
		next.CurrentSource = strings.TrimSpace(*p.currentSource)
	}
	if p.nextStepSource != nil {
		next.NextStepSource = strings.TrimSpace(*p.nextStepSource)
	}
	if sameTaskMemoryContent(old, next) {
		c.taskMemory.mu.Unlock()
		return old
	}
	next.Revision = old.Revision + 1
	next.UpdatedAt = time.Now().UnixMilli()
	c.taskMemory.value = next
	c.taskMemory.mu.Unlock()

	c.persistTaskMemory(next)
	return next
}

func sameTaskMemoryContent(a, b event.TaskMemory) bool {
	return a.SessionKey == b.SessionKey && a.Goal == b.Goal && a.Current == b.Current && a.NextStep == b.NextStep &&
		a.GoalSource == b.GoalSource && a.CurrentSource == b.CurrentSource && a.NextStepSource == b.NextStepSource
}

func (c *Controller) persistTaskMemory(memory event.TaskMemory) {
	path := store.SessionTaskMemory(c.SessionPath())
	if path == "" {
		return
	}
	data, err := json.MarshalIndent(memory, "", "  ")
	if err == nil {
		err = fileutil.AtomicWriteFile(path, data, 0o600)
	}
	if err != nil {
		slog.Warn("controller: persist task memory", "path", path, "err", err)
	}
}

func (c *Controller) loadTaskMemory(sessionPath string) {
	var memory event.TaskMemory
	path := store.SessionTaskMemory(sessionPath)
	data, err := os.ReadFile(path)
	if err == nil {
		err = json.Unmarshal(data, &memory)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		slog.Warn("controller: load task memory", "path", path, "err", err)
	}
	if err != nil {
		memory = event.TaskMemory{}
	}
	memory.SessionKey = taskMemorySessionKey(sessionPath)
	c.taskMemory.mu.Lock()
	c.taskMemory.value = memory
	c.taskMemory.mu.Unlock()
}

func (c *Controller) clearTaskMemory() {
	c.taskMemory.mu.Lock()
	revision := c.taskMemory.value.Revision + 1
	c.taskMemory.value = event.TaskMemory{SessionKey: taskMemorySessionKey(c.SessionPath()), Revision: revision, UpdatedAt: time.Now().UnixMilli()}
	memory := c.taskMemory.value
	c.taskMemory.mu.Unlock()
	c.persistTaskMemory(memory)
}

func taskMemorySessionKey(sessionPath string) string { return agent.BranchID(sessionPath) }

func (c *Controller) inheritTaskMemory(sessionPath string) {
	c.taskMemory.mu.Lock()
	memory := c.taskMemory.value
	memory.SessionKey = taskMemorySessionKey(sessionPath)
	memory.Revision++
	memory.UpdatedAt = time.Now().UnixMilli()
	c.taskMemory.value = memory
	c.taskMemory.mu.Unlock()
	c.persistTaskMemory(memory)
}

func (c *Controller) captureTaskGoal(input string) {
	if strings.TrimSpace(c.Goal()) != "" {
		return
	}
	current := c.TaskMemory()
	if strings.TrimSpace(current.Goal) != "" {
		return
	}
	goal := boundedTaskText(input, 240)
	if goal == "" {
		return
	}
	c.updateTaskMemory(taskMemoryPatch{goal: stringPtr(goal), goalSource: stringPtr("user_prompt")})
}

func boundedTaskText(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" || maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}
	runes := []rune(text)
	return strings.TrimSpace(string(runes[:maxRunes])) + "…"
}

func (c *Controller) nextTaskStep() string {
	if summary, ok := c.AutoResearchSummary(); ok && summary != nil {
		if next := strings.TrimSpace(summary.NextRequiredAction); next != "" {
			return next
		}
	}
	for _, item := range c.Todos() {
		if item.Status != "completed" && item.Status != "cancelled" {
			return strings.TrimSpace(item.Content)
		}
	}
	return ""
}

func (c *Controller) nextTaskStepSource() string {
	if summary, ok := c.AutoResearchSummary(); ok && summary != nil && strings.TrimSpace(summary.NextRequiredAction) != "" {
		return "autoresearch"
	}
	if c.nextTaskStep() != "" {
		return "todo"
	}
	return ""
}
