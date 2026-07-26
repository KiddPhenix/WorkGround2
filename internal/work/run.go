package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// WorkflowRun is one execution of a Work. It records stage/task/attempt
// hierarchy, the definition digest it ran against, and an optional Conclusion.
type WorkflowRun struct {
	ID               string            `json:"id"`
	WorkID           string            `json:"workId"`
	RequestID        string            `json:"requestId,omitempty"`
	DefinitionDigest string            `json:"definitionDigest"`
	State            RunState          `json:"state"`
	Stages           []Stage           `json:"stages"`
	StartedAt        time.Time         `json:"startedAt"`
	FinishedAt       *time.Time        `json:"finishedAt,omitempty"`
	Conclusion       *Conclusion       `json:"conclusion,omitempty"`
	Cancel           *RunCancelReceipt `json:"cancel,omitempty"`
	Pause            *RunPauseReceipt  `json:"pause,omitempty"`
}

// CancelDelivery is the persisted delivery state of a run cancel intent.
type CancelDelivery string

const (
	CancelPending   CancelDelivery = "pending"
	CancelDelivered CancelDelivery = "delivered"
	CancelFailed    CancelDelivery = "failed"
)

// RunCancelReceipt makes the event-first cancellation side effect observable
// and retryable after the Run has already entered its terminal state.
type RunCancelReceipt struct {
	RequestID string         `json:"requestId"`
	Status    CancelDelivery `json:"status"`
	Error     string         `json:"error,omitempty"`
	Attempts  int            `json:"attempts"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// RunPauseReceipt records the cooperative pause boundary and its recovery
// limitation. Session checkpoints restore local code/conversation state only;
// they never claim to roll back network, database, deployment, or other
// external effects.
type RunPauseReceipt struct {
	RequestID string    `json:"requestId"`
	PausedAt  time.Time `json:"pausedAt"`
	Notice    string    `json:"notice"`
}

// Stage is one phase inside a WorkflowRun.
type Stage struct {
	ID         string          `json:"id,omitempty"`
	Name       string          `json:"name"`
	Gate       string          `json:"gate,omitempty"`
	State      RunState        `json:"state"`
	Tasks      []Task          `json:"tasks"`
	StartedAt  time.Time       `json:"startedAt"`
	FinishedAt *time.Time      `json:"finishedAt,omitempty"`
	Resolution *GateResolution `json:"resolution,omitempty"`
}

// Task is one task inside a Stage.
type Task struct {
	ID         string     `json:"id,omitempty"`
	Name       string     `json:"name"`
	State      RunState   `json:"state"`
	Attempts   []Attempt  `json:"attempts"`
	StartedAt  *time.Time `json:"startedAt,omitempty"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Attempt is one try of a Task, linked to a SessionRef.
type Attempt struct {
	ID              string          `json:"id,omitempty"`
	RequestID       string          `json:"requestId,omitempty"`
	Index           int             `json:"index"`
	State           RunState        `json:"state"`
	SessionRef      SessionRef      `json:"sessionRef"`
	StartedAt       time.Time       `json:"startedAt"`
	FinishedAt      *time.Time      `json:"finishedAt,omitempty"`
	Error           string          `json:"error,omitempty"`
	Receipt         *AttemptReceipt `json:"receipt,omitempty"`
	SideEffectClass string          `json:"sideEffectClass,omitempty"`
}

// AttemptReceipt records execution or human-confirmed evidence for an attempt.
// External/destructive attempts without a matching receipt must enter
// needs_confirmation and must never be replayed automatically.
type AttemptReceipt struct {
	// RequestID is the idempotency key for the resolution action.
	RequestID string `json:"requestId"`
	// Operation binds the receipt to the exact executor operation.
	Operation string `json:"operation,omitempty"`
	// Outcome records the user-confirmed outcome: retry | accept | skip | cancel.
	Outcome string `json:"outcome"`
	// Evidence is an optional human-readable note about the resolution.
	Evidence        string `json:"evidence,omitempty"`
	SideEffectClass string `json:"sideEffectClass,omitempty"`
	// ConfirmedAt is when the evidence was recorded.
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// ── Event payload wrappers ────────────────────────────────────────────────
// Each carry enough context for the reducer to locate the unique target
// object within a Work projection, avoiding global name/index matching.

// runEventPayload persists a run together with the root Work state derived
// from it. Legacy run.started events used a bare WorkflowRun payload.
type runEventPayload struct {
	Run       WorkflowRun      `json:"run"`
	WorkState WorkState        `json:"workState"`
	V2Receipt *V2IntentReceipt `json:"v2Receipt,omitempty"`
}

// stageEventPayload wraps a Stage with its parent Run ID.
type stageEventPayload struct {
	RunID      string          `json:"runId"`
	Stage      Stage           `json:"stage"`
	Resolution *GateResolution `json:"resolution,omitempty"`
}

// taskEventPayload wraps a Task with its parent Run and Stage context.
type taskEventPayload struct {
	RunID   string `json:"runId"`
	StageID string `json:"stageId"`
	Task    Task   `json:"task"`
}

// attemptEventPayload wraps an Attempt with its parent Run, Stage, and Task context.
type attemptEventPayload struct {
	RunID   string  `json:"runId"`
	StageID string  `json:"stageId"`
	TaskID  string  `json:"taskId"`
	Attempt Attempt `json:"attempt"`
}

func decodeRunEventPayload(raw json.RawMessage) (runEventPayload, bool, error) {
	var wrapped runEventPayload
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return runEventPayload{}, false, err
	}
	if wrapped.Run.ID != "" {
		return wrapped, false, nil
	}
	var legacy WorkflowRun
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return runEventPayload{}, false, err
	}
	if legacy.ID == "" {
		return runEventPayload{}, false, fmt.Errorf("run ID is required")
	}
	return runEventPayload{Run: legacy}, true, nil
}

func decodeStageEventPayload(raw json.RawMessage) (stageEventPayload, bool, error) {
	var wrapped stageEventPayload
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return stageEventPayload{}, false, err
	}
	if wrapped.RunID != "" {
		return wrapped, false, nil
	}
	var legacy Stage
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return stageEventPayload{}, false, err
	}
	if legacy.Name == "" && legacy.ID == "" {
		return stageEventPayload{}, false, fmt.Errorf("stage identity is required")
	}
	return stageEventPayload{Stage: legacy}, true, nil
}

func decodeTaskEventPayload(raw json.RawMessage) (taskEventPayload, bool, error) {
	var wrapped taskEventPayload
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return taskEventPayload{}, false, err
	}
	if wrapped.RunID != "" {
		return wrapped, false, nil
	}
	var legacy Task
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return taskEventPayload{}, false, err
	}
	if legacy.Name == "" && legacy.ID == "" {
		return taskEventPayload{}, false, fmt.Errorf("task identity is required")
	}
	return taskEventPayload{Task: legacy}, true, nil
}

func decodeAttemptEventPayload(raw json.RawMessage) (attemptEventPayload, bool, error) {
	var wrapped attemptEventPayload
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return attemptEventPayload{}, false, err
	}
	if wrapped.RunID != "" {
		return wrapped, false, nil
	}
	var legacy Attempt
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return attemptEventPayload{}, false, err
	}
	return attemptEventPayload{Attempt: legacy}, true, nil
}

func findRunStage(run *WorkflowRun, stageID string) *Stage {
	if run == nil {
		return nil
	}
	for i := range run.Stages {
		if run.Stages[i].ID == stageID || (run.Stages[i].ID == "" && run.Stages[i].Name == stageID) {
			return &run.Stages[i]
		}
	}
	return nil
}

func findStageTask(stage *Stage, taskID string) *Task {
	if stage == nil {
		return nil
	}
	for i := range stage.Tasks {
		if stage.Tasks[i].ID == taskID || (stage.Tasks[i].ID == "" && stage.Tasks[i].Name == taskID) {
			return &stage.Tasks[i]
		}
	}
	return nil
}

func findTaskAttempt(task *Task, attemptID string) *Attempt {
	if task == nil {
		return nil
	}
	for i := range task.Attempts {
		if task.Attempts[i].ID == attemptID {
			return &task.Attempts[i]
		}
	}
	return nil
}
