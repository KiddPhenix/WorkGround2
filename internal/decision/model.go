// Package decision implements the process-wide, durable human decision broker.
package decision

import "time"

const SchemaVersion = 1

type Status string

const (
	StatusQueued      Status = "queued"
	StatusPresented   Status = "presented"
	StatusDecided     Status = "decided"
	StatusApplied     Status = "applied"
	StatusDeferred    Status = "deferred"
	StatusCancelled   Status = "cancelled"
	StatusOrphaned    Status = "orphaned"
	StatusApplyFailed Status = "apply_failed"
)

type ExternalMode string

const (
	ExternalSmart     ExternalMode = "smart"
	ExternalAlways    ExternalMode = "always"
	ExternalLocalOnly ExternalMode = "local_only_until"
	ExternalOff       ExternalMode = "off"
)

type DeliveryStatus string

const (
	DeliveryPending DeliveryStatus = "pending"
	DeliverySending DeliveryStatus = "sending"
	DeliverySent    DeliveryStatus = "sent"
	DeliveryFailed  DeliveryStatus = "failed"
)

type DeliveryEvent string

const (
	DeliveryPresented DeliveryEvent = "presented"
	DeliveryResolved  DeliveryEvent = "resolved"
	DeliveryCancelled DeliveryEvent = "cancelled"
)

type Origin struct {
	Kind           string `json:"kind"`
	WorkspaceRoot  string `json:"workspace_root,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	SessionPath    string `json:"session_path,omitempty"`
	SessionTitle   string `json:"session_title,omitempty"`
	ControllerGen  string `json:"controller_generation,omitempty"`
	AgentID        string `json:"agent_id,omitempty"`
	ThreadID       string `json:"thread_id,omitempty"`
	LocalRequestID string `json:"local_request_id,omitempty"`
	ResumePayload  string `json:"resume_payload,omitempty"`
	Snapshot       string `json:"snapshot,omitempty"`
}

type Reference struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URI   string `json:"uri"`
}

type Option struct {
	Label  string `json:"label"`
	Impact string `json:"impact"`
}

type Question struct {
	ID          string   `json:"id"`
	Header      string   `json:"header"`
	Prompt      string   `json:"prompt"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multi_select,omitempty"`
}

type Recommendation struct {
	QuestionID string `json:"question_id"`
	Option     string `json:"option"`
	Reason     string `json:"reason"`
}

type Presentation struct {
	Title          string          `json:"title"`
	TaskSummary    string          `json:"task_summary"`
	WhyNow         string          `json:"why_now"`
	Questions      []Question      `json:"questions"`
	Recommendation *Recommendation `json:"recommendation,omitempty"`
	NoAnswerPolicy string          `json:"no_answer_policy"`
	References     []Reference     `json:"references,omitempty"`
}

type Selection struct {
	QuestionID string   `json:"question_id"`
	Selected   []string `json:"selected"`
}

type Answer struct {
	Selections []Selection `json:"selections"`
}

type Responder struct {
	Kind       string `json:"kind"`
	ID         string `json:"id,omitempty"`
	Label      string `json:"label,omitempty"`
	EndpointID string `json:"endpoint_id,omitempty"`
}

type Decision struct {
	ID             string       `json:"id"`
	IdempotencyKey string       `json:"idempotency_key"`
	Kind           string       `json:"kind"`
	Origin         Origin       `json:"origin"`
	Presentation   Presentation `json:"presentation"`
	Status         Status       `json:"status"`
	Answer         *Answer      `json:"answer,omitempty"`
	Responder      *Responder   `json:"responder,omitempty"`
	Revision       int64        `json:"revision"`
	QueueSeq       int64        `json:"queue_seq"`
	CreatedAt      time.Time    `json:"created_at"`
	PresentedAt    *time.Time   `json:"presented_at,omitempty"`
	DecidedAt      *time.Time   `json:"decided_at,omitempty"`
	AppliedAt      *time.Time   `json:"applied_at,omitempty"`
	BusinessDueAt  *time.Time   `json:"business_due_at,omitempty"`
	StaleAfter     *time.Time   `json:"stale_after,omitempty"`
	LastError      string       `json:"last_error,omitempty"`
}

type Channel struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Kind         string `json:"kind"`
	Enabled      bool   `json:"enabled"`
	ConnectionID string `json:"connection_id,omitempty"`
	Domain       string `json:"domain,omitempty"`
	ChatID       string `json:"chat_id,omitempty"`
	ChatType     string `json:"chat_type,omitempty"`
}

type Settings struct {
	ExternalMode   ExternalMode  `json:"external_mode"`
	LocalOnlyUntil *time.Time    `json:"local_only_until,omitempty"`
	SmartGrace     time.Duration `json:"smart_grace"`
}

type Delivery struct {
	ID            string         `json:"id"`
	DecisionID    string         `json:"decision_id"`
	EndpointID    string         `json:"endpoint_id"`
	Sequence      int64          `json:"sequence"`
	Event         DeliveryEvent  `json:"event"`
	Status        DeliveryStatus `json:"status"`
	RemoteMessage string         `json:"remote_message,omitempty"`
	Attempts      int            `json:"attempts"`
	NextRetryAt   time.Time      `json:"next_retry_at,omitempty"`
	LastError     string         `json:"last_error,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type AuditEntry struct {
	ID         string    `json:"id"`
	DecisionID string    `json:"decision_id,omitempty"`
	Kind       string    `json:"kind"`
	Revision   int64     `json:"revision"`
	Detail     string    `json:"detail,omitempty"`
	At         time.Time `json:"at"`
}

type Snapshot struct {
	Version      int          `json:"version"`
	Revision     int64        `json:"revision"`
	NextQueueSeq int64        `json:"next_queue_seq"`
	Decisions    []Decision   `json:"decisions"`
	Channels     []Channel    `json:"channels,omitempty"`
	Deliveries   []Delivery   `json:"deliveries,omitempty"`
	Audit        []AuditEntry `json:"audit,omitempty"`
	Settings     Settings     `json:"settings"`
	UpdatedAt    time.Time    `json:"updated_at"`
}

type CreateRequest struct {
	IdempotencyKey string
	Kind           string
	Origin         Origin
	Presentation   Presentation
	BusinessDueAt  *time.Time
	StaleAfter     *time.Time
}

type CreateResult struct {
	Decision  Decision
	Duplicate bool
}

type ResolveResult struct {
	Decision        Decision
	AlreadyResolved bool
	Promoted        *Decision
}

type Transition struct {
	Decision Decision
	Promoted *Decision
}

type ListFilter struct {
	Statuses []Status
	Origin   *Origin
}

type Change struct {
	Revision int64
	Kind     string
	Decision Decision
	Promoted *Decision
}
