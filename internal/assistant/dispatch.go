package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// DispatchKind is the Dispatcher's stable classification of one direct user
// input. Classification is persisted, never inferred again from the raw text by
// a later consumer.
type DispatchKind string

const (
	DispatchTask        DispatchKind = "task"        // an actionable piece of work
	DispatchQuestion    DispatchKind = "question"    // a question expecting an answer
	DispatchFeedback    DispatchKind = "feedback"    // praise/critique about past work
	DispatchImprovement DispatchKind = "improvement" // a suggested method/workflow change
	DispatchCorrection  DispatchKind = "correction"  // a factual correction of a prior result
	DispatchControl     DispatchKind = "control"     // pause/resume/retry/cancel intent
)

// DispatchState is the durable lifecycle of a dispatched direct input.
type DispatchState string

const (
	// DispatchPendingClassification persists the raw input and awaits the
	// Dispatcher. The input is never lost and can be safely retried.
	DispatchPendingClassification DispatchState = "pending_classification"
	// DispatchClassified has a kind, a user-facing first-level reply and 0..N
	// frozen Runner Jobs.
	DispatchClassified DispatchState = "classified"
	// DispatchClassificationFailed keeps the raw input plus an explicit,
	// retryable error (for example the model was unavailable).
	DispatchClassificationFailed DispatchState = "classification_failed"
	// DispatchReflected means every Job reached a terminal state and the
	// Reflector produced exactly one bounded ContextPack.
	DispatchReflected DispatchState = "reflected"
	// DispatchReflectionFailed means every Job is terminal but the Reflector
	// model call failed. It carries a bounded-backoff retry time.
	DispatchReflectionFailed DispatchState = "reflection_failed"
)

// Dispatch is the durable single source of truth for one direct user input.
// It carries the raw text verbatim, the classification, the user-facing reply,
// and the identity of the Jobs it spawned.
type Dispatch struct {
	ID                    string        `json:"id"`
	AssistantID           string        `json:"assistant_id"`
	RequestID             string        `json:"request_id"`
	Input                 string        `json:"input"`
	Kind                  DispatchKind  `json:"kind,omitempty"`
	Reply                 string        `json:"reply,omitempty"`
	State                 DispatchState `json:"state"`
	Error                 *RunError     `json:"error,omitempty"`
	RetryAt               time.Time     `json:"retry_at,omitempty" ts_type:"string"`
	ClassificationAttempt int           `json:"classification_attempt,omitempty"`
	ReflectionAttempt     int           `json:"reflection_attempt,omitempty"`
	Revision              int64         `json:"revision"`
	CreatedAt             time.Time     `json:"created_at" ts_type:"string"`
	UpdatedAt             time.Time     `json:"updated_at" ts_type:"string"`
	ClassifiedAt          time.Time     `json:"classified_at,omitempty" ts_type:"string"`
}

// JobState is the lifecycle of a Runner Job. It mirrors the Run lifecycle so
// the same recovery rules (lease fence, retry, attention) apply uniformly.
type JobState string

const (
	JobQueued           JobState = "queued"
	JobRunning          JobState = "running"
	JobSucceeded        JobState = "succeeded"
	JobRetryWait        JobState = "retry_wait"
	JobWaitingAttention JobState = "waiting_attention"
	JobFailed           JobState = "failed"
	JobCancelled        JobState = "cancelled"
)

// RunnerJob freezes the input classification, target, runner name, permission,
// workspace, and ContextPack revision into one parallel unit of work. Frozen
// values never change after classification, so late user edits cannot rewrite a
// job mid-flight.
type RunnerJob struct {
	ID                  string       `json:"id"`
	AssistantID         string       `json:"assistant_id"`
	DispatchID          string       `json:"dispatch_id"`
	Name                string       `json:"name"`
	Kind                DispatchKind `json:"kind"`
	Target              string       `json:"target,omitempty"`
	Prompt              string       `json:"prompt"`
	Scope               Scope        `json:"scope"`
	WorkspaceRoot       string       `json:"workspace_root,omitempty"`
	SessionPath         string       `json:"session_path,omitempty"`
	Policy              Policy       `json:"policy"`
	ContextPackRevision int64        `json:"context_pack_revision,omitempty"`
	State               JobState     `json:"state"`
	Attempt             int          `json:"attempt"`
	MaxAttempts         int          `json:"max_attempts"`
	LeaseOwner          string       `json:"lease_owner,omitempty"`
	LeaseFence          int64        `json:"lease_fence"`
	LeaseUntil          time.Time    `json:"lease_until,omitempty" ts_type:"string"`
	RetryAt             time.Time    `json:"retry_at,omitempty" ts_type:"string"`
	StartedAt           time.Time    `json:"started_at,omitempty" ts_type:"string"`
	FinishedAt          time.Time    `json:"finished_at,omitempty" ts_type:"string"`
	Summary             string       `json:"summary,omitempty"`
	Error               *RunError    `json:"error,omitempty"`
	Revision            int64        `json:"revision"`
	CreatedAt           time.Time    `json:"created_at" ts_type:"string"`
	UpdatedAt           time.Time    `json:"updated_at" ts_type:"string"`
}

// ContextPack is the Reflector's bounded, durable synthesis of one Dispatch. It
// is injected into later Jobs instead of unbounded raw history. It stays bound
// to its Assistant and source Dispatch.
type ContextPack struct {
	ID            string    `json:"id"`
	AssistantID   string    `json:"assistant_id"`
	DispatchID    string    `json:"dispatch_id"`
	Revision      int64     `json:"revision"`
	Conclusion    string    `json:"conclusion"`
	Evidence      []string  `json:"evidence,omitempty"`
	Failures      []string  `json:"failures,omitempty"`
	Strategies    []string  `json:"strategies,omitempty"`
	OpenLoops     []string  `json:"open_loops,omitempty"`
	RunnerContext string    `json:"runner_context,omitempty"`
	BoundJobIDs   []string  `json:"bound_job_ids,omitempty"`
	CreatedAt     time.Time `json:"created_at" ts_type:"string"`
}

// IdeaTrigger records why the Ideator produced a proposal.
type IdeaTrigger string

const (
	IdeaTriggerManual  IdeaTrigger = "manual"  // user explicitly asked
	IdeaTriggerCadence IdeaTrigger = "cadence" // 5 successful task dispatches or 7 days elapsed
)

// IdeaState is the lifecycle of an idea proposal. Only pending may transition;
// accept writes strategy memory or a responsibility candidate and never mutates
// mission/policy/workspace/credentials/publish config or executes external work.
type IdeaState string

const (
	IdeaPending    IdeaState = "pending"
	IdeaAccepted   IdeaState = "accepted"
	IdeaRejected   IdeaState = "rejected"
	IdeaSuperseded IdeaState = "superseded"
)

// IdeaDecision is the user's resolution of a pending idea.
type IdeaDecision string

const (
	IdeaAccept IdeaDecision = "accept"
	IdeaReject IdeaDecision = "reject"
)

// IdeaProposal is a bounded, human-decided idea. Acceptance only turns it into a
// strategy memory item and/or a new responsibility candidate; it cannot change
// the assistant mission, policy, workspace, credentials, or publish config.
type IdeaProposal struct {
	ID             string      `json:"id"`
	AssistantID    string      `json:"assistant_id"`
	RequestID      string      `json:"request_id"`
	Trigger        IdeaTrigger `json:"trigger"`
	Summary        string      `json:"summary"`
	Rationale      string      `json:"rationale,omitempty"`
	StrategyMemory string      `json:"strategy_memory,omitempty"`
	Responsibility string      `json:"responsibility,omitempty"`
	Objective      string      `json:"objective,omitempty"`
	DoneCriteria   string      `json:"done_criteria,omitempty"`
	NextAction     string      `json:"next_action,omitempty"`
	State          IdeaState   `json:"state"`
	Resolution     string      `json:"resolution,omitempty"`
	Revision       int64       `json:"revision"`
	CreatedAt      time.Time   `json:"created_at" ts_type:"string"`
	UpdatedAt      time.Time   `json:"updated_at" ts_type:"string"`
	ResolvedAt     time.Time   `json:"resolved_at,omitempty" ts_type:"string"`
}

// JobSpec declares one Runner Job the Dispatcher wants created. The Store
// freezes policy/workspace/context-pack revision from the assistant snapshot, so
// the classifier cannot smuggle in escalated permissions.
type JobSpec struct {
	Name        string       `json:"name"`
	Kind        DispatchKind `json:"kind"`
	Target      string       `json:"target,omitempty"`
	Prompt      string       `json:"prompt"`
	MaxAttempts int          `json:"max_attempts,omitempty"`
}

// Classification is the Dispatcher's bounded output: one user-facing reply and
// zero or more job specs.
type Classification struct {
	Kind  DispatchKind `json:"kind"`
	Reply string       `json:"reply"`
	Jobs  []JobSpec    `json:"jobs,omitempty"`
}

// ContextPackContent is the Reflector's bounded synthesis input. The Store
// enforces per-field and total UTF-8 byte bounds.
type ContextPackContent struct {
	Conclusion    string   `json:"conclusion"`
	Evidence      []string `json:"evidence,omitempty"`
	Failures      []string `json:"failures,omitempty"`
	Strategies    []string `json:"strategies,omitempty"`
	OpenLoops     []string `json:"open_loops,omitempty"`
	RunnerContext string   `json:"runner_context,omitempty"`
}

// OpenDispatchInput persists a raw direct input without classification.
type OpenDispatchInput struct {
	AssistantID string
	RequestID   string
	Input       string
	Now         time.Time
}

// ClassifyDispatchInput applies a completed classification.
type ClassifyDispatchInput struct {
	AssistantID string
	DispatchID  string
	RequestID   string
	Kind        DispatchKind
	Reply       string
	Jobs        []JobSpec
	Now         time.Time
}

// FailDispatchInput records a retryable classification failure.
type FailDispatchInput struct {
	AssistantID string
	DispatchID  string
	RequestID   string
	Failure     Failure
}

// ReflectInput persists the Reflector's bounded synthesis for a Dispatch whose
// jobs are all terminal.
type ReflectInput struct {
	AssistantID string
	DispatchID  string
	RequestID   string
	Content     ContextPackContent
	Now         time.Time
}

// ClaimJobInput claims one queued Job under a lease, honoring the per-assistant
// concurrency cap.
type ClaimJobInput struct {
	Owner string
	Now   time.Time
	Lease time.Duration
}

// FinishJobInput completes a running Job under its lease fence.
type FinishJobInput struct {
	RequestID  string
	JobID      string
	LeaseOwner string
	LeaseFence int64
	Summary    string
	Now        time.Time
}

// BindJobSessionInput durably records the execution session path on a running
// Job under its lease fence.
type BindJobSessionInput struct {
	RequestID   string
	JobID       string
	LeaseOwner  string
	LeaseFence  int64
	SessionPath string
	Now         time.Time
}

// FailJobInput records a Job failure under its lease fence.
type FailJobInput struct {
	RequestID  string
	JobID      string
	LeaseOwner string
	LeaseFence int64
	Failure    Failure
}

// CancelJobInput cancels a Job by stable request ID.
type CancelJobInput struct {
	RequestID string
	JobID     string
	Reason    string
	Now       time.Time
}

// RetryJobInput re-queues a failed/cancelled/waiting Job by stable request ID.
type RetryJobInput struct {
	RequestID string
	JobID     string
	Now       time.Time
}

// OpenIdeaInput creates an IdeaProposal. Manual trigger is always allowed;
// cadence trigger is rejected unless the ideation cadence is due.
type OpenIdeaInput struct {
	AssistantID    string
	RequestID      string
	Trigger        IdeaTrigger
	Summary        string
	Rationale      string
	StrategyMemory string
	Responsibility string
	Objective      string
	DoneCriteria   string
	NextAction     string
	Now            time.Time
}

// ResolveIdeaInput applies an accept/reject decision under revision CAS.
type ResolveIdeaInput struct {
	RequestID        string
	AssistantID      string
	IdeaID           string
	ExpectedRevision int64
	Decision         IdeaDecision
	Resolution       string
	Now              time.Time
}

// IdeaContent is the bounded payload of a pending idea produced by the Ideator
// model.
type IdeaContent struct {
	Summary        string
	Rationale      string
	StrategyMemory string
	Responsibility string
	Objective      string
	DoneCriteria   string
	NextAction     string
}

// Ideation is the persisted, observable state of the cadence Ideator. A failed
// cadence call sets RetryAt so the host does not retry on every tick.
type Ideation struct {
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty" ts_type:"string"`
	RetryAt       time.Time `json:"retry_at,omitempty" ts_type:"string"`
	Attempt       int       `json:"attempt"`
	Error         string    `json:"error,omitempty"`
}

// FailReflectionInput records a retryable reflection failure for a Dispatch.
type FailReflectionInput struct {
	AssistantID string
	DispatchID  string
	RequestID   string
	Failure     Failure
}

// FailIdeationInput records a retryable cadence-ideation failure.
type FailIdeationInput struct {
	AssistantID string
	RequestID   string
	Message     string
	Now         time.Time
}

const (
	// maxConcurrentJobs is the default per-assistant parallel Runner Job cap.
	maxConcurrentJobs = 3
	// ideaCadenceSuccessfulTasks triggers an ideation after this many successful
	// task Dispatches since the last idea.
	ideaCadenceSuccessfulTasks = 5
	// ideaCadenceInterval triggers an ideation at least this long after the last.
	ideaCadenceInterval = 7 * 24 * time.Hour
)

// maxContextPackItems bounds each list inside a ContextPack.
const maxContextPackItems = 24

// contextPackMaxBytes bounds each string field of a ContextPack by UTF-8 bytes.
const contextPackMaxBytes = 24 * 1024

// contextPackTotalMaxBytes bounds the full persisted ContextPack payload.
const contextPackTotalMaxBytes = 64 * 1024

// validateDispatchKind rejects unknown classifications at the type boundary.
func validateDispatchKind(kind DispatchKind) error {
	switch kind {
	case DispatchTask, DispatchQuestion, DispatchFeedback, DispatchImprovement,
		DispatchCorrection, DispatchControl:
		return nil
	default:
		return fmt.Errorf("assistant: invalid dispatch kind %q", kind)
	}
}

func validateDispatchState(state DispatchState) error {
	switch state {
	case DispatchPendingClassification, DispatchClassified, DispatchClassificationFailed, DispatchReflected, DispatchReflectionFailed:
		return nil
	default:
		return fmt.Errorf("assistant: invalid dispatch state %q", state)
	}
}

func validateJobState(state JobState) error {
	switch state {
	case JobQueued, JobRunning, JobSucceeded, JobRetryWait, JobWaitingAttention, JobFailed, JobCancelled:
		return nil
	default:
		return fmt.Errorf("assistant: invalid job state %q", state)
	}
}

func validateIdeaState(state IdeaState) error {
	switch state {
	case IdeaPending, IdeaAccepted, IdeaRejected, IdeaSuperseded:
		return nil
	default:
		return fmt.Errorf("assistant: invalid idea state %q", state)
	}
}

func validateDispatch(d Dispatch) error {
	if err := validateID("dispatch", d.ID); err != nil {
		return err
	}
	if err := validateID("assistant", d.AssistantID); err != nil {
		return err
	}
	if err := validateRequestID(d.RequestID); err != nil {
		return err
	}
	if strings.TrimSpace(d.Input) == "" {
		return errors.New("assistant: dispatch input must not be empty")
	}
	if len(d.Input) > maxDirectPromptBytes {
		return fmt.Errorf("assistant: dispatch input exceeds %d bytes", maxDirectPromptBytes)
	}
	if err := validateDispatchState(d.State); err != nil {
		return err
	}
	if d.State == DispatchClassified || d.State == DispatchReflected || d.State == DispatchReflectionFailed {
		if err := validateDispatchKind(d.Kind); err != nil {
			return err
		}
		if strings.TrimSpace(d.Reply) == "" {
			return errors.New("assistant: classified dispatch requires a user-facing reply")
		}
	}
	if d.ClassificationAttempt < 0 || d.ReflectionAttempt < 0 {
		return errors.New("assistant: dispatch attempts must not be negative")
	}
	if d.Revision < 1 || d.CreatedAt.IsZero() || d.UpdatedAt.IsZero() {
		return errors.New("assistant: dispatch revision and timestamps are required")
	}
	return nil
}

func validateJob(job RunnerJob) error {
	if err := validateID("job", job.ID); err != nil {
		return err
	}
	if err := validateID("assistant", job.AssistantID); err != nil {
		return err
	}
	if err := validateID("dispatch", job.DispatchID); err != nil {
		return err
	}
	if strings.TrimSpace(job.Name) == "" || strings.TrimSpace(job.Prompt) == "" {
		return errors.New("assistant: job name and prompt are required")
	}
	if err := validateDispatchKind(job.Kind); err != nil {
		return err
	}
	if err := validateJobState(job.State); err != nil {
		return err
	}
	if job.State == JobRunning && (job.LeaseOwner == "" || job.LeaseFence < 1 || job.LeaseUntil.IsZero()) {
		return errors.New("assistant: running job requires a fenced lease")
	}
	if job.Attempt < 0 || job.MaxAttempts < 1 || job.Revision < 1 {
		return errors.New("assistant: invalid job counters")
	}
	if job.Scope == "" {
		return errors.New("assistant: job requires frozen scope")
	}
	if err := validatePolicy(job.Policy); err != nil {
		return err
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
		return errors.New("assistant: job timestamps are required")
	}
	return nil
}

func validateContextPack(pack ContextPack) error {
	if err := validateID("contextpack", pack.ID); err != nil {
		return err
	}
	if err := validateID("assistant", pack.AssistantID); err != nil {
		return err
	}
	if err := validateID("dispatch", pack.DispatchID); err != nil {
		return err
	}
	if strings.TrimSpace(pack.Conclusion) == "" {
		return errors.New("assistant: context pack requires a conclusion")
	}
	if pack.Revision < 1 || pack.CreatedAt.IsZero() {
		return errors.New("assistant: context pack revision and timestamp are required")
	}
	return nil
}

func validateIdeaProposal(idea IdeaProposal) error {
	if err := validateID("idea", idea.ID); err != nil {
		return err
	}
	if err := validateID("assistant", idea.AssistantID); err != nil {
		return err
	}
	if err := validateRequestID(idea.RequestID); err != nil {
		return err
	}
	switch idea.Trigger {
	case IdeaTriggerManual, IdeaTriggerCadence:
	default:
		return fmt.Errorf("assistant: invalid idea trigger %q", idea.Trigger)
	}
	if strings.TrimSpace(idea.Summary) == "" {
		return errors.New("assistant: idea summary is required")
	}
	if err := validateIdeaState(idea.State); err != nil {
		return err
	}
	if idea.Revision < 1 || idea.CreatedAt.IsZero() || idea.UpdatedAt.IsZero() {
		return errors.New("assistant: idea revision and timestamps are required")
	}
	return nil
}
