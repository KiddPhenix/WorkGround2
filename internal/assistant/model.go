package assistant

import "time"

type Lifecycle string

const (
	LifecycleActive   Lifecycle = "active"
	LifecyclePaused   Lifecycle = "paused"
	LifecycleArchived Lifecycle = "archived"
)

type Scope string

const (
	ScopeGlobal    Scope = "global"
	ScopeWorkspace Scope = "workspace"
)

type Access string

const (
	AccessDeny    Access = "deny"
	AccessAllow   Access = "allow"
	AccessApprove Access = "approve"
)

type Policy struct {
	LocalWrite Access `json:"local_write"`
	Network    Access `json:"network"`
	Publish    Access `json:"publish"`
	Delete     Access `json:"delete"`
	Payment    Access `json:"payment"`
	Secrets    Access `json:"secrets"`
	Private    Access `json:"private_data"`
}

func DefaultPolicy() Policy {
	return Policy{
		LocalWrite: AccessDeny,
		Network:    AccessDeny,
		Publish:    AccessApprove,
		Delete:     AccessApprove,
		Payment:    AccessApprove,
		Secrets:    AccessApprove,
		Private:    AccessApprove,
	}
}

type Assistant struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description,omitempty"`
	Mission       string    `json:"mission"`
	Scope         Scope     `json:"scope"`
	WorkspaceRoot string    `json:"workspace_root,omitempty"`
	Lifecycle     Lifecycle `json:"lifecycle"`
	Policy        Policy    `json:"policy"`
	MemoryRev     int64     `json:"memory_revision"`
	Revision      int64     `json:"revision"`
	CreatedAt     time.Time `json:"created_at" ts_type:"string"`
	UpdatedAt     time.Time `json:"updated_at" ts_type:"string"`
}

type CatchUpPolicy string

const (
	CatchUpCoalesceLatest CatchUpPolicy = "coalesce_latest"
	CatchUpSkip           CatchUpPolicy = "skip"
)

type ScheduleKind string

const (
	ScheduleManual   ScheduleKind = "manual"
	ScheduleInterval ScheduleKind = "interval"
	ScheduleDaily    ScheduleKind = "daily"
	ScheduleWeekly   ScheduleKind = "weekly"
	ScheduleBiweekly ScheduleKind = "biweekly"
	ScheduleMonthly  ScheduleKind = "monthly"
	ScheduleYearly   ScheduleKind = "yearly"
)

type TimeWindow struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type Schedule struct {
	Kind            ScheduleKind `json:"kind"`
	IntervalSeconds int64        `json:"interval_seconds,omitempty"`
	Timezone        string       `json:"timezone,omitempty"`
	At              string       `json:"at,omitempty"`
	Weekday         time.Weekday `json:"weekday,omitempty"`
	Day             int          `json:"day,omitempty"`
	Month           time.Month   `json:"month,omitempty"`
	StartAt         time.Time    `json:"start_at,omitempty" ts_type:"string"`
	Window          TimeWindow   `json:"window,omitempty"`
}

type Routine struct {
	ID               string        `json:"id"`
	AssistantID      string        `json:"assistant_id"`
	Title            string        `json:"title"`
	Prompt           string        `json:"prompt"`
	Schedule         Schedule      `json:"schedule"`
	Enabled          bool          `json:"enabled"`
	CatchUp          CatchUpPolicy `json:"catch_up"`
	LastScheduledFor time.Time     `json:"last_scheduled_for,omitempty" ts_type:"string"`
	Revision         int64         `json:"revision"`
	CreatedAt        time.Time     `json:"created_at" ts_type:"string"`
	UpdatedAt        time.Time     `json:"updated_at" ts_type:"string"`
}

type TriggerKind string

const (
	TriggerManual    TriggerKind = "manual"
	TriggerScheduled TriggerKind = "scheduled"
	TriggerRetry     TriggerKind = "retry"
)

type RunState string

const (
	RunQueued           RunState = "queued"
	RunRunning          RunState = "running"
	RunSucceeded        RunState = "succeeded"
	RunWaitingApproval  RunState = "waiting_approval"
	RunRetryWait        RunState = "retry_wait"
	RunWaitingAttention RunState = "waiting_attention"
	RunFailed           RunState = "failed"
	RunCancelled        RunState = "cancelled"
)

type RunError struct {
	Code         string    `json:"code"`
	Message      string    `json:"message"`
	Provider     string    `json:"provider,omitempty"`
	Retryable    bool      `json:"retryable"`
	OutcomeKnown bool      `json:"outcome_known"`
	At           time.Time `json:"at" ts_type:"string"`
}

type Run struct {
	ID                string      `json:"id"`
	AssistantID       string      `json:"assistant_id"`
	RoutineID         string      `json:"routine_id,omitempty"`
	ResponsibilityID  string      `json:"responsibility_id,omitempty"`
	RequestID         string      `json:"request_id"`
	OccurrenceKey     string      `json:"occurrence_key,omitempty"`
	Occurrences       []string    `json:"occurrences,omitempty"`
	Trigger           TriggerKind `json:"trigger"`
	AssistantRevision int64       `json:"assistant_revision"`
	Scope             Scope       `json:"scope"`
	WorkspaceRoot     string      `json:"workspace_root,omitempty"`
	RoutineRevision   int64       `json:"routine_revision,omitempty"`
	Prompt            string      `json:"prompt,omitempty"`
	Mission           string      `json:"mission"`
	Policy            Policy      `json:"policy"`
	State             RunState    `json:"state"`
	Attempt           int         `json:"attempt"`
	MaxAttempts       int         `json:"max_attempts"`
	SessionPath       string      `json:"session_path,omitempty"`
	ResumeToken       string      `json:"resume_token,omitempty"`
	LeaseOwner        string      `json:"lease_owner,omitempty"`
	LeaseFence        int64       `json:"lease_fence"`
	LeaseUntil        time.Time   `json:"lease_until,omitempty" ts_type:"string"`
	ScheduledFor      time.Time   `json:"scheduled_for,omitempty" ts_type:"string"`
	RetryAt           time.Time   `json:"retry_at,omitempty" ts_type:"string"`
	StartedAt         time.Time   `json:"started_at,omitempty" ts_type:"string"`
	FinishedAt        time.Time   `json:"finished_at,omitempty" ts_type:"string"`
	Summary           string      `json:"summary,omitempty"`
	Error             *RunError   `json:"error,omitempty"`
	Revision          int64       `json:"revision"`
	CreatedAt         time.Time   `json:"created_at" ts_type:"string"`
	UpdatedAt         time.Time   `json:"updated_at" ts_type:"string"`
}

type MemoryKind string

const (
	MemoryCharter  MemoryKind = "charter"
	MemoryFact     MemoryKind = "facts"
	MemoryStrategy MemoryKind = "strategy"
	MemoryOpenLoop MemoryKind = "open_loops"
	MemoryMetric   MemoryKind = "metrics"
)

type MemoryItem struct {
	ID        string     `json:"id"`
	Kind      MemoryKind `json:"kind"`
	Body      string     `json:"body"`
	SourceRun string     `json:"source_run,omitempty"`
	Evidence  string     `json:"evidence,omitempty"`
	Locked    bool       `json:"locked"`
	Revision  int64      `json:"revision"`
	CreatedAt time.Time  `json:"created_at" ts_type:"string"`
	UpdatedAt time.Time  `json:"updated_at" ts_type:"string"`
}

type Memory struct {
	Revision int64        `json:"revision"`
	Items    []MemoryItem `json:"items"`
}

type MemoryPatch struct {
	Upsert []MemoryItem `json:"upsert,omitempty"`
	Delete []string     `json:"delete,omitempty"`
}

type AttentionState string

const (
	AttentionOpen      AttentionState = "open"
	AttentionApproved  AttentionState = "approved"
	AttentionRejected  AttentionState = "rejected"
	AttentionCancelled AttentionState = "cancelled"
)

const (
	AttentionActionRebindWorkspace = "rebind_workspace"
	AttentionActionCancelRecreate  = "cancel_recreate"
)

type AttentionItem struct {
	ID          string         `json:"id"`
	AssistantID string         `json:"assistant_id"`
	RunID       string         `json:"run_id,omitempty"`
	RequestID   string         `json:"request_id"`
	Action      string         `json:"action"`
	Summary     string         `json:"summary"`
	Tool        string         `json:"tool,omitempty"`
	Subject     string         `json:"subject,omitempty"`
	ResumeToken string         `json:"resume_token,omitempty"`
	State       AttentionState `json:"state"`
	Resolution  string         `json:"resolution,omitempty"`
	Revision    int64          `json:"revision"`
	CreatedAt   time.Time      `json:"created_at" ts_type:"string"`
	UpdatedAt   time.Time      `json:"updated_at" ts_type:"string"`
}

type CreateInput struct {
	RequestID string
	Assistant Assistant
	Routines  []Routine
	Now       time.Time
}

type RoutineInput struct {
	RequestID        string
	Routine          Routine
	ExpectedRevision int64
	Now              time.Time
}

type TriggerInput struct {
	AssistantID string
	RoutineID   string
	// Prompt carries the original direct user input for a manual, non-routine
	// run ("对助手说"). It must be empty for routine runs and for the
	// "continue mission" intent, which passes neither routine nor prompt.
	Prompt       string
	RequestID    string
	Trigger      TriggerKind
	ScheduledFor time.Time
	MaxAttempts  int
	Now          time.Time
}

type Failure struct {
	Code         string
	Message      string
	Provider     string
	Retryable    bool
	OutcomeKnown bool
	RetryAfter   time.Duration
	Now          time.Time
}

type FinishInput struct {
	RequestID   string
	RunID       string
	LeaseOwner  string
	LeaseFence  int64
	Summary     string
	SessionPath string
	Now         time.Time
}

type FailInput struct {
	RequestID  string
	RunID      string
	LeaseOwner string
	LeaseFence int64
	Failure    Failure
}

type BindSessionInput struct {
	RequestID   string
	RunID       string
	LeaseOwner  string
	LeaseFence  int64
	SessionPath string
	Now         time.Time
}

type CancelInput struct {
	RequestID string
	RunID     string
	Reason    string
	Now       time.Time
}

type ApprovalInput struct {
	RequestID   string
	RunID       string
	LeaseOwner  string
	LeaseFence  int64
	Action      string
	Summary     string
	Tool        string
	Subject     string
	SessionPath string
	ResumeToken string
	Now         time.Time
}

type RequireAttentionInput struct {
	RequestID   string
	RunID       string
	LeaseOwner  string
	LeaseFence  int64
	Action      string
	Summary     string
	SessionPath string
	ResumeToken string
	Now         time.Time
}

type ResolveAttentionInput struct {
	RequestID        string
	AssistantID      string
	AttentionID      string
	ExpectedRevision int64
	State            AttentionState
	Resolution       string
	Now              time.Time
}

type ResumeInput struct {
	RequestID string
	RunID     string
	Now       time.Time
}
