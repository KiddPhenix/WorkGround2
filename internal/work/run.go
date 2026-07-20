package work

import "time"

// WorkflowRun is one execution of a Work. It records stage/task/attempt
// hierarchy, the definition digest it ran against, and an optional Conclusion.
type WorkflowRun struct {
	ID               string      `json:"id"`
	WorkID           string      `json:"workId"`
	DefinitionDigest string      `json:"definitionDigest"`
	State            RunState    `json:"state"`
	Stages           []Stage     `json:"stages"`
	StartedAt        time.Time   `json:"startedAt"`
	FinishedAt       *time.Time  `json:"finishedAt,omitempty"`
	Conclusion       *Conclusion `json:"conclusion,omitempty"`
}

// Stage is one phase inside a WorkflowRun.
type Stage struct {
	Name       string     `json:"name"`
	State      RunState   `json:"state"`
	Tasks      []Task     `json:"tasks"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
}

// Task is one task inside a Stage.
type Task struct {
	Name     string    `json:"name"`
	State    RunState  `json:"state"`
	Attempts []Attempt `json:"attempts"`
}

// Attempt is one try of a Task, linked to a SessionRef.
type Attempt struct {
	Index      int        `json:"index"`
	State      RunState   `json:"state"`
	SessionRef SessionRef `json:"sessionRef"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	Error      string     `json:"error,omitempty"`
}
