package addonhost

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ── Tasks API ───────────────────────────────────────────────────────────────

// TaskStatus is a terminal or active lifecycle status.
type TaskStatus string

const (
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
)

// Task represents an AddOn-initiated long-running task tracked by the host.
type Task struct {
	ID        string     `json:"taskId"`
	AddOn     string     `json:"addon"`
	Kind      string     `json:"kind"`
	Title     string     `json:"title"`
	Phase     string     `json:"phase"`
	Status    TaskStatus `json:"status"`
	Progress  int        `json:"progress"` // 0-100
	Message   string     `json:"message,omitempty"`
	Retryable bool       `json:"retryable,omitempty"`
	Result    string     `json:"result,omitempty"`
	Error     string     `json:"error,omitempty"`

	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

// TaskStartInput is the request payload for starting a task.
type TaskStartInput struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// TasksStart creates a new task and returns its id.
func (h *Host) TasksStart(in TaskStartInput) (string, error) {
	kind := strings.TrimSpace(in.Kind)
	title := strings.TrimSpace(in.Title)
	if kind == "" || title == "" {
		return "", fmt.Errorf("%w: task kind and title are required", ErrBadRequest)
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	h.taskSeq++
	id := fmt.Sprintf("%s-%d", h.Ctx.Namespace, h.taskSeq)
	now := time.Now().UTC().Format(time.RFC3339)
	t := &Task{
		ID:        id,
		AddOn:     h.Ctx.AddOnName,
		Kind:      kind,
		Title:     title,
		Status:    TaskRunning,
		StartedAt: now,
	}
	h.tasks[id] = t
	h.emit("addon.task.changed", "", id, nil)
	return id, nil
}

// TaskUpdateInput carries a mid-task progress update.
type TaskUpdateInput struct {
	TaskID    string `json:"taskId"`
	Phase     string `json:"phase,omitempty"`
	Status    string `json:"status,omitempty"` // optional early terminal status
	Progress  int    `json:"progress,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`
}

// TasksUpdate applies an in-progress update.  If status is set to a terminal
// value, the task is implicitly finished.
func (h *Host) TasksUpdate(in TaskUpdateInput) error {
	taskID := strings.TrimSpace(in.TaskID)
	if taskID == "" {
		return fmt.Errorf("%w: taskId is required", ErrBadRequest)
	}

	h.mu.Lock()
	t, ok := h.tasks[taskID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("%w: task %q not found", ErrNotFound, taskID)
	}
	if in.Phase != "" {
		t.Phase = in.Phase
	}
	if in.Progress > 0 {
		t.Progress = min(in.Progress, 100)
	}
	if in.Message != "" {
		t.Message = in.Message
	}
	t.Retryable = in.Retryable

	// Allow early terminal status via update.
	if ts := TaskStatus(in.Status); ts == TaskSucceeded || ts == TaskFailed {
		t.Status = ts
		t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	}
	h.mu.Unlock()

	h.emit("addon.task.changed", "", taskID, nil)
	return nil
}

// TasksFinish marks a task as completed (succeeded or failed).
func (h *Host) TasksFinish(taskID string, status TaskStatus, result, errText string) error {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("%w: taskId is required", ErrBadRequest)
	}

	h.mu.Lock()
	t, ok := h.tasks[taskID]
	if !ok {
		h.mu.Unlock()
		return fmt.Errorf("%w: task %q not found", ErrNotFound, taskID)
	}
	t.Status = status
	t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	t.Result = result
	t.Error = errText
	if status == TaskFailed && errText != "" {
		t.Message = errText
	}
	h.mu.Unlock()

	h.emit("addon.task.changed", "", taskID, nil)
	return nil
}

// TasksCancelled marks a task as cancellable/cancelled.
func (h *Host) TasksCancelled(taskID string) error {
	return h.TasksFinish(taskID, TaskFailed, "", "cancelled")
}

// Task returns a snapshot of the task state (for UI reading).
func (h *Host) Task(taskID string) (*Task, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	t, ok := h.tasks[taskID]
	if !ok {
		return nil, ErrNotFound
	}
	copy := *t
	return &copy, nil
}

// Tasks returns all tasks for this addon.
func (h *Host) Tasks() []*Task {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]*Task, 0, len(h.tasks))
	for _, t := range h.tasks {
		copy := *t
		out = append(out, &copy)
	}
	return out
}

// Ensure sync and time are used (imported via the host.go already, but listed here for clarity).
var _ = sync.Mutex{}
var _ = time.Now
