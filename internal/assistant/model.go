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

// AssistantMode is the long-term operating mode of an Assistant (design 4.1).
// finite completes the current batch and then stops (done) or enters
// maintenance; continuous keeps discovering and executing the next batch.
type AssistantMode string

const (
	// ModeFinite stops after the current plan is complete (or enters
	// maintenance when the plan has ongoing review/maintenance items).
	ModeFinite AssistantMode = "finite"
	// ModeContinuous auto-expands when the plan is empty: evaluate, discover,
	// research, rank, adopt (policy-gated) and execute the next batch.
	ModeContinuous AssistantMode = "continuous"
	// defaultAssistantMode is applied to legacy data whose Mode predates this
	// field. Continuous is the product promise (a long-lived high-autonomy
	// assistant), and auto-adoption is still gated by Policy.
	defaultAssistantMode = ModeContinuous
)

type Access string

const (
	AccessDeny    Access = "deny"
	AccessAllow   Access = "allow"
	AccessApprove Access = "approve"
)

// AutoAnswerPolicy is the Assistant's strategy for answering ordinary pending
// interactions on managed Sessions (design section 9). "auto" runs the full
// decision sequence (infer -> isolated trial -> most reversible -> fail-closed);
// "ask" is the user-declared hard gate: the Assistant waits for the user.
type AutoAnswerPolicy string

const (
	AutoAnswerAuto AutoAnswerPolicy = "auto"
	AutoAnswerAsk  AutoAnswerPolicy = "ask"
)

// defaultMaxConcurrentSessions is the per-assistant cap on concurrently running
// managed Sessions when the policy does not set an explicit value.
const defaultMaxConcurrentSessions = 3

type Policy struct {
	LocalWrite Access `json:"local_write"`
	Network    Access `json:"network"`
	Publish    Access `json:"publish"`
	Delete     Access `json:"delete"`
	Payment    Access `json:"payment"`
	Secrets    Access `json:"secrets"`
	Private    Access `json:"private_data"`
	// ConstraintEdit gates whether the Assistant may modify the project's
	// authoritative constraints (project_constraints_update). It is a dedicated
	// dimension so a project can allow constraint edits independently of general
	// local writes. deny blocks the tool; allow/approve permit it.
	ConstraintEdit Access `json:"constraint_edit"`
	// MaxConcurrentSessions is the per-assistant cap on concurrently running
	// managed Sessions (0 = the default cap). Raising it widens the policy and
	// is refused for Assistant self-updates.
	MaxConcurrentSessions int `json:"max_concurrent_sessions,omitempty"`
	// AutoAnswer is the ordinary-question strategy. ask turns every ordinary
	// interaction into a hard gate, so the Assistant never infers an answer.
	AutoAnswer AutoAnswerPolicy `json:"auto_answer,omitempty"`
	// Isolation gates reversible multi-candidate trials (fork Session / worktree
	// / sandbox) for low-confidence decisions. deny disables isolated trials and
	// forces the most-reversible single inference.
	Isolation Access `json:"isolation,omitempty"`
	// ExternalVoiceEnabled is the user's direct switch for publishing or
	// speaking on the user's behalf. Only the user may flip it; the Assistant
	// tools never expose it. false makes publish tools refuse outright while
	// research, drafts and plan work continue.
	ExternalVoiceEnabled bool `json:"external_voice_enabled"`
}

func DefaultPolicy() Policy {
	return Policy{
		LocalWrite:            AccessDeny,
		Network:               AccessDeny,
		Publish:               AccessApprove,
		Delete:                AccessApprove,
		Payment:               AccessApprove,
		Secrets:               AccessApprove,
		Private:               AccessApprove,
		ConstraintEdit:        AccessApprove,
		MaxConcurrentSessions: defaultMaxConcurrentSessions,
		AutoAnswer:            AutoAnswerAuto,
		Isolation:             AccessAllow,
	}
}

// NormalizePolicy fills policy dimensions that predate the current model with
// their documented defaults. It is stable and replayable: values already set
// are never overwritten. Legacy aggregates and partial constructions go through
// it on every read so the policy is always explicit and valid. Hosts and tools
// that compare policy changes normalize before judging widen/narrow.
func NormalizePolicy(p Policy) Policy {
	if p.ConstraintEdit == "" {
		p.ConstraintEdit = AccessApprove
	}
	if p.MaxConcurrentSessions == 0 {
		p.MaxConcurrentSessions = defaultMaxConcurrentSessions
	}
	if p.AutoAnswer == "" {
		p.AutoAnswer = AutoAnswerAuto
	}
	if p.Isolation == "" {
		p.Isolation = AccessAllow
	}
	return p
}

// normalizePolicy is the internal alias used by the store's read path.
func normalizePolicy(p Policy) Policy { return NormalizePolicy(p) }

type Assistant struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Description   string        `json:"description,omitempty"`
	Mission       string        `json:"mission"`
	Mode          AssistantMode `json:"mode,omitempty"`
	Scope         Scope         `json:"scope"`
	WorkspaceRoot string        `json:"workspace_root,omitempty"`
	Lifecycle     Lifecycle     `json:"lifecycle"`
	Policy        Policy        `json:"policy"`
	MemoryRev     int64         `json:"memory_revision"`
	Revision      int64         `json:"revision"`
	CreatedAt     time.Time     `json:"created_at" ts_type:"string"`
	UpdatedAt     time.Time     `json:"updated_at" ts_type:"string"`
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
	WorkEpoch         int64       `json:"work_epoch,omitempty"`
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
	RequestID     string
	Assistant     Assistant
	Routines      []Routine
	InitialPrompt string
	Now           time.Time
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
