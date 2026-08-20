// Package runhub is the transport-agnostic core for tracking external-agent
// runs. Reporters submit launch intents or normalized lifecycle events; RunHub
// owns persistence, dedup, state reduction, capability projection and change
// notification. Desktop, HTTP, CLI and bots consume the same Run projections.
//
// This package is deliberately a leaf over the standard library plus
// workground2/internal/fileutil: it must not import workground2/internal/runhub/dsh,
// Desktop, or any vendor adapter. Boot/Desktop register concrete Runner
// implementations so adapter logic stays out of the core state machine.
package runhub

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// RunID identifies one AgentRun. It is opaque to the state machine but must be
// a filesystem-safe single path element because it names the run's on-disk
// directory.
type RunID string

// EventID identifies one RunEvent. It is the dedup key for Report. Durable
// receipt filenames are derived from its hash, so normal opaque ids may contain
// colons without becoming filesystem path elements.
type EventID string

// Source names the external agent family that produced a run.
type Source string

const (
	SourceDSH    Source = "dsh"
	SourceCodex  Source = "codex"
	SourceClaude Source = "claude"
)

// Ownership records who owns the run's lifecycle.
type Ownership string

const (
	// OwnershipManaged marks a run created and driven by WorkGround2.
	OwnershipManaged Ownership = "managed"
	// OwnershipObserved marks a run created outside WorkGround2 that we only watch.
	OwnershipObserved Ownership = "observed"
)

// RunState is the coarse lifecycle phase of a run.
type RunState string

const (
	StateQueued      RunState = "queued"
	StateStarting    RunState = "starting"
	StateRunning     RunState = "running"
	StateWaitingUser RunState = "waiting_user"
	StateSucceeded   RunState = "succeeded"
	StateFailed      RunState = "failed"
	StateCancelled   RunState = "cancelled"
	StateInterrupted RunState = "interrupted"
	StateStale       RunState = "stale"
)

// IsTerminal reports whether s is one of the monotonic end states. The set is
// expressed as immutable switch logic so callers cannot mutate the terminal set.
func (s RunState) IsTerminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateCancelled, StateInterrupted, StateStale:
		return true
	}
	return false
}

// Valid reports whether s is a known run state.
func (s RunState) Valid() bool {
	switch s {
	case StateQueued, StateStarting, StateRunning, StateWaitingUser,
		StateSucceeded, StateFailed, StateCancelled, StateInterrupted, StateStale:
		return true
	}
	return false
}

// Valid reports whether s is a known external agent family.
func (s Source) Valid() bool {
	switch s {
	case SourceDSH, SourceCodex, SourceClaude:
		return true
	}
	return false
}

// Valid reports whether o is a known ownership value.
func (o Ownership) Valid() bool {
	switch o {
	case OwnershipManaged, OwnershipObserved:
		return true
	}
	return false
}

// Valid reports whether a is a known activity value.
func (a Activity) Valid() bool {
	switch a {
	case ActivityThinking, ActivityTool, ActivityResponding, ActivityBackground, ActivityIdle:
		return true
	}
	return false
}

// Valid reports whether t is a known event type.
func (t EventType) Valid() bool {
	switch t {
	case EventQueued, EventStarting, EventRunning, EventWaitingUser,
		EventActivity, EventSummary, EventTitle,
		EventSucceeded, EventFailed, EventCancelled, EventInterrupted, EventStale:
		return true
	}
	return false
}

// Valid reports whether s is a known receipt status.
func (s ReceiptStatus) Valid() bool {
	switch s {
	case ReceiptAccepted, ReceiptAlreadyApplied, ReceiptStale, ReceiptRetryable, ReceiptInvalid:
		return true
	}
	return false
}

// Activity is the fine-grained phase within a running state.
type Activity string

const (
	ActivityThinking   Activity = "thinking"
	ActivityTool       Activity = "tool"
	ActivityResponding Activity = "responding"
	ActivityBackground Activity = "background"
	ActivityIdle       Activity = "idle"
)

// EventType is the normalized lifecycle event kind submitted to Report.
type EventType string

const (
	EventQueued      EventType = "queued"
	EventStarting    EventType = "starting"
	EventRunning     EventType = "running"
	EventWaitingUser EventType = "waiting_user"
	EventActivity    EventType = "activity"
	EventSummary     EventType = "summary"
	EventTitle       EventType = "title"
	EventSucceeded   EventType = "succeeded"
	EventFailed      EventType = "failed"
	EventCancelled   EventType = "cancelled"
	EventInterrupted EventType = "interrupted"
	EventStale       EventType = "stale"
)

// Capabilities is the adapter-declared control surface. The UI only renders
// actions whose capability is true; missing protocol support must not fake them.
type Capabilities struct {
	Cancel  bool `json:"cancel"`
	Open    bool `json:"open"`
	Retry   bool `json:"retry"`
	Resume  bool `json:"resume"`
	Approve bool `json:"approve"`
	Send    bool `json:"send"`
}

// Has reports whether the named action capability is declared.
func (c Capabilities) Has(action Action) bool {
	switch action {
	case ActionCancel:
		return c.Cancel
	case ActionOpen:
		return c.Open
	case ActionRetry:
		return c.Retry
	case ActionResume:
		return c.Resume
	case ActionApprove:
		return c.Approve
	case ActionSend:
		return c.Send
	}
	return false
}

// AgentRun is the single trusted projection of one external-agent task.
type AgentRun struct {
	ID              RunID        `json:"id"`
	Source          Source       `json:"source"`
	NativeSessionID string       `json:"nativeSessionId,omitempty"`
	Ownership       Ownership    `json:"ownership"`
	Workspace       string       `json:"workspace,omitempty"`
	Title           string       `json:"title,omitempty"`
	State           RunState     `json:"state"`
	Activity        Activity     `json:"activity,omitempty"`
	ActivityLabel   string       `json:"activityLabel,omitempty"`
	Summary         string       `json:"summary,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
	Revision        uint64       `json:"revision"`
	LastSeenAt      time.Time    `json:"lastSeenAt"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
}

// EventPayload carries the typed, sanitized extras an event may update. It
// intentionally holds no transcript, reasoning, tool arguments or tool results,
// and exposes no field (such as a raw JSON payload) that could persist an
// arbitrary transcript or tool payload verbatim.
type EventPayload struct {
	Activity Activity `json:"activity,omitempty"`
	Label    string   `json:"label,omitempty"`
	Summary  string   `json:"summary,omitempty"`
	Title    string   `json:"title,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// RunEvent is one normalized lifecycle event. ExpectRevision is a delivery-time
// optimistic-concurrency hint, not persisted: when non-zero and different from
// the run's current Revision the event is rejected as stale.
type RunEvent struct {
	EventID        EventID      `json:"eventId"`
	RunID          RunID        `json:"runId"`
	Source         Source       `json:"source,omitempty"`
	NativeSeq      string       `json:"nativeSeq,omitempty"`
	OccurredAt     time.Time    `json:"occurredAt"`
	Type           EventType    `json:"type"`
	Payload        EventPayload `json:"payload,omitempty"`
	ExpectRevision uint64       `json:"-"`
}

// LaunchIntent is the idempotent request to start one managed run.
type LaunchIntent struct {
	RequestID         string `json:"requestId"`
	RunnerProfileID   string `json:"runnerProfileId"`
	Source            Source `json:"source,omitempty"`
	Workspace         string `json:"workspace,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	PermissionProfile string `json:"permissionProfile,omitempty"`
}

// RunnerBinding ties a run to the concrete runner/process that drives it.
type RunnerBinding struct {
	RunID           RunID  `json:"runId"`
	NativeSessionID string `json:"nativeSessionId,omitempty"`
	ProtocolVersion string `json:"protocolVersion,omitempty"`
	ProcessRef      string `json:"processRef,omitempty"`
	Attempt         uint32 `json:"attempt"`
}

// ReceiptStatus is the outcome of a write operation.
type ReceiptStatus string

const (
	ReceiptAccepted       ReceiptStatus = "accepted"
	ReceiptAlreadyApplied ReceiptStatus = "already_applied"
	ReceiptStale          ReceiptStatus = "stale"
	ReceiptRetryable      ReceiptStatus = "retryable_error"
	ReceiptInvalid        ReceiptStatus = "invalid"
)

// Receipt is the uniform write acknowledgement returned by Launch and Report.
// It carries enough context to make retries safe without re-reading state.
type Receipt struct {
	Status   ReceiptStatus `json:"status"`
	RunID    RunID         `json:"runId,omitempty"`
	EventID  EventID       `json:"eventId,omitempty"`
	Revision uint64        `json:"revision,omitempty"`
	Message  string        `json:"message,omitempty"`
}

// Action is a user-facing control action routed through a Runner by capability.
type Action string

const (
	ActionCancel  Action = "cancel"
	ActionOpen    Action = "open"
	ActionRetry   Action = "retry"
	ActionResume  Action = "resume"
	ActionApprove Action = "approve"
	ActionSend    Action = "send"
)

// Filter narrows List results. Zero values match everything.
type Filter struct {
	Source    Source
	Ownership Ownership
	State     RunState
	Active    bool // when true, only non-terminal runs
}

// RunProjection is the normalized view handed to consumers. It is a subset of
// AgentRun and never exposes internal store state.
type RunProjection struct {
	ID              RunID        `json:"id"`
	Source          Source       `json:"source"`
	NativeSessionID string       `json:"nativeSessionId,omitempty"`
	Ownership       Ownership    `json:"ownership"`
	Workspace       string       `json:"workspace,omitempty"`
	Title           string       `json:"title,omitempty"`
	State           RunState     `json:"state"`
	Activity        Activity     `json:"activity,omitempty"`
	ActivityLabel   string       `json:"activityLabel,omitempty"`
	Summary         string       `json:"summary,omitempty"`
	Capabilities    Capabilities `json:"capabilities,omitempty"`
	Revision        uint64       `json:"revision"`
	CreatedAt       time.Time    `json:"createdAt"`
	UpdatedAt       time.Time    `json:"updatedAt"`
}

// ChangeKind is the coarse shape of a subscription notification.
type ChangeKind string

const (
	ChangeCreated ChangeKind = "created"
	ChangeUpdated ChangeKind = "updated"
)

// Change is one notification emitted after an accepted mutation.
type Change struct {
	Kind ChangeKind    `json:"kind"`
	Run  RunProjection `json:"run"`
}

// TransitionCode classifies a reducer rejection.
type TransitionCode string

const (
	TransitionStale   TransitionCode = "stale"
	TransitionInvalid TransitionCode = "invalid"
)

// TransitionError reports why Reduce refused an event. Its Code maps to the
// ReceiptStatus of the same name so callers can relay the outcome verbatim.
type TransitionError struct {
	Code TransitionCode
	Msg  string
}

func (e *TransitionError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("runhub: %s: %s", e.Code, e.Msg)
}

// ErrUnsupported marks an action the runner does not declare or implement.
var ErrUnsupported = errors.New("runhub: action not supported")

// runIDPattern constrains RunID, which names the run's on-disk directory and
// therefore must remain a filesystem-safe single path element. It forbids path
// separators and traversal.
var runIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// validRunID reports whether name is a safe single path element for the store.
func validRunID(name string) bool {
	return runIDPattern.MatchString(name) && name != "." && name != ".."
}

// opaqueIDPattern constrains opaque request/event ids, which are not themselves
// used as filesystem path elements. Opaque ids may contain colons and other
// punctuation because external agents (notably DSH session ids) use them, but
// they still forbid empty ids, whitespace, path separators, control characters
// and unreasonable length. The store derives a receipt filename from a hash
// rather than the id itself.
var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,511}$`)

// validOpaqueID reports whether name is a safe opaque request or event id.
func validOpaqueID(name string) bool {
	return opaqueIDPattern.MatchString(name)
}

// ValidateLaunchIntent returns a human-readable error if intent cannot be
// persisted safely. It never mutates intent.
func ValidateLaunchIntent(intent LaunchIntent) error {
	if strings.TrimSpace(intent.RequestID) == "" {
		return fmt.Errorf("runhub: launch requestId is empty")
	}
	if !validOpaqueID(intent.RequestID) {
		return fmt.Errorf("runhub: launch requestId %q is not a safe id", intent.RequestID)
	}
	if !intent.Source.Valid() {
		return fmt.Errorf("runhub: invalid source %q", intent.Source)
	}
	return nil
}

// ValidateEvent returns a human-readable error if evt cannot be reduced safely.
func ValidateEvent(evt RunEvent) error {
	if strings.TrimSpace(string(evt.EventID)) == "" {
		return fmt.Errorf("runhub: eventId is empty")
	}
	if !validOpaqueID(string(evt.EventID)) {
		return fmt.Errorf("runhub: eventId %q is not a safe id", evt.EventID)
	}
	if strings.TrimSpace(string(evt.RunID)) == "" {
		return fmt.Errorf("runhub: runId is empty")
	}
	if !validRunID(string(evt.RunID)) {
		return fmt.Errorf("runhub: runId %q is not a safe id", evt.RunID)
	}
	if !evt.Type.Valid() {
		return fmt.Errorf("runhub: unknown event type %q", evt.Type)
	}
	if evt.Source != "" && !evt.Source.Valid() {
		return fmt.Errorf("runhub: invalid source %q", evt.Source)
	}
	if evt.Payload.Activity != "" && !evt.Payload.Activity.Valid() {
		return fmt.Errorf("runhub: invalid activity %q", evt.Payload.Activity)
	}
	return nil
}

// validate reports durable-content corruption in a reloaded run snapshot. State
// and ownership must be set to known values; activity may be empty but must be
// known when present.
func (r AgentRun) validate() error {
	if !validRunID(string(r.ID)) {
		return fmt.Errorf("runhub: run %q has unsafe id", r.ID)
	}
	if !r.State.Valid() {
		return fmt.Errorf("runhub: run %q has invalid state %q", r.ID, r.State)
	}
	if !r.Ownership.Valid() {
		return fmt.Errorf("runhub: run %q has invalid ownership %q", r.ID, r.Ownership)
	}
	if !r.Source.Valid() {
		return fmt.Errorf("runhub: run %q has invalid source %q", r.ID, r.Source)
	}
	if r.Activity != "" && !r.Activity.Valid() {
		return fmt.Errorf("runhub: run %q has invalid activity %q", r.ID, r.Activity)
	}
	if r.Revision == 0 {
		return fmt.Errorf("runhub: run %q has zero revision", r.ID)
	}
	if r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() || r.LastSeenAt.IsZero() {
		return fmt.Errorf("runhub: run %q has missing timestamps", r.ID)
	}
	return nil
}

// Projection reduces a run to its public consumer view.
func (r AgentRun) Projection() RunProjection {
	return RunProjection{
		ID:              r.ID,
		Source:          r.Source,
		NativeSessionID: r.NativeSessionID,
		Ownership:       r.Ownership,
		Workspace:       r.Workspace,
		Title:           r.Title,
		State:           r.State,
		Activity:        r.Activity,
		ActivityLabel:   r.ActivityLabel,
		Summary:         r.Summary,
		Capabilities:    r.Capabilities,
		Revision:        r.Revision,
		CreatedAt:       r.CreatedAt,
		UpdatedAt:       r.UpdatedAt,
	}
}
