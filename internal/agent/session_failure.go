package agent

import (
	"errors"
	"strings"
	"time"
)

// FailureClass is the durable classification of a session's last failure,
// following the Assistant design (§15 失败、重试与自愈). It is the single
// source of truth a retry decision reads: only RetryableKnown may be retried
// in place; every other class must surface as an explicit, typed, observable
// outcome instead of guessing a redo of external actions.
type FailureClass string

const (
	// FailRetryableKnown means the failure outcome is fully known and the
	// original action can be safely retried (e.g. a network blip that aborted
	// the model round before any tool side effect was committed).
	FailRetryableKnown FailureClass = "retryable_known"
	// FailFailedKnown means the failure outcome is known and a retry in place
	// is pointless — the approach itself needs to change.
	FailFailedKnown FailureClass = "failed_known"
	// FailOutcomeUnknown means the external side effect may or may not have
	// happened (crash mid-tool, lost acknowledgement). Redoing it automatically
	// could double-apply an external action, so retry must refuse.
	FailOutcomeUnknown FailureClass = "outcome_unknown"
	// FailBlockedPolicy means the Assistant's Policy or the global work gate
	// refused the work; other responsibilities continue, this one waits.
	FailBlockedPolicy FailureClass = "blocked_policy"
	// FailBlockedDependency means a required dependency was unavailable; the
	// failure is retryable after the dependency arrives (bounded backoff).
	FailBlockedDependency FailureClass = "blocked_dependency"
)

// SessionFailure is the durable failure record persisted in BranchMeta. It
// captures the classification, a short human-readable reason, the provider
// and tool context when known, and when the failure happened. Retry reads
// this record instead of guessing from transcript shape.
type SessionFailure struct {
	Class    FailureClass `json:"class"`
	Code     string       `json:"code,omitempty"`
	Message  string       `json:"message,omitempty"`
	Provider string       `json:"provider,omitempty"`
	Tool     string       `json:"tool,omitempty"`
	At       time.Time    `json:"at"`
}

// Retryable reports whether this failure may be retried in place.
func (f *SessionFailure) Retryable() bool {
	return f != nil && f.Class == FailRetryableKnown
}

// Typed retry refusal errors. A host's session_retry surfaces these as
// explicit, observable outcomes (outcome_unknown / blocked_policy / invalid)
// instead of silently re-running the turn.
var (
	// ErrNoFailureCheckpoint means the session has no recorded failure to
	// retry (never failed, or failed without a durable checkpoint).
	ErrNoFailureCheckpoint = errors.New("session has no recorded failure checkpoint")
	// ErrOutcomeUnknown means the failure's external outcome is unknown; an
	// automatic redo could double-apply an external side effect.
	ErrOutcomeUnknown = errors.New("session failure outcome is unknown; automatic retry refused")
	// ErrNotRetryable means the failure was classified as non-retryable in
	// place (failed_known / blocked_dependency without the dependency).
	ErrNotRetryable = errors.New("session failure is not retryable in place")
	// ErrBlockedPolicy means the failure was caused by the Assistant's Policy
	// or the global work gate; continuing other work is the correct response.
	ErrBlockedPolicy = errors.New("session failure blocked by policy")
	// ErrBlockedDependency means the failure was caused by an unavailable
	// dependency; retry after it arrives.
	ErrBlockedDependency = errors.New("session failure blocked by missing dependency")
)

// NewSessionFailure builds a SessionFailure from a raw error, deriving a safe
// default classification. Unclassified errors default to RetryableKnown only
// when the error is not a policy/dependency/unknown-outcome shape; hosts that
// know better should pass an explicit class.
func NewSessionFailure(class FailureClass, code, message string, at time.Time) SessionFailure {
	if class == "" {
		class = FailRetryableKnown
	}
	return SessionFailure{Class: class, Code: code, Message: message, At: at}
}

// ClassifySessionError maps a raw turn error to a failure classification. It
// is a conservative, observable default: unknown shapes stay RetryableKnown
// only when the error carries no "outcome unknown" or "policy" signal;
// otherwise they surface as the stronger class so retry refuses rather than
// guessing.
func ClassifySessionError(err error) FailureClass {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, ErrOutcomeUnknown):
		return FailOutcomeUnknown
	case errors.Is(err, ErrBlockedPolicy):
		return FailBlockedPolicy
	case errors.Is(err, ErrBlockedDependency):
		return FailBlockedDependency
	case containsAny(msg, "work paused", "work epoch changed", "blocked by policy", "policy blocked"):
		return FailBlockedPolicy
	case containsAny(msg, "outcome unknown", "external outcome", "lease expired", "acknowledgement lost", "ack lost"):
		return FailOutcomeUnknown
	case containsAny(msg, "dependency", "not installed", "unavailable", "connection refused", "timeout"):
		return FailBlockedDependency
	default:
		return FailRetryableKnown
	}
}

// RetryErrFromClass converts a failure classification into the typed error a
// session_retry surfaces. A retryable_known class yields nil (retry allowed).
func RetryErrFromClass(class FailureClass) error {
	switch class {
	case "", FailRetryableKnown:
		return nil
	case FailFailedKnown:
		return ErrNotRetryable
	case FailOutcomeUnknown:
		return ErrOutcomeUnknown
	case FailBlockedPolicy:
		return ErrBlockedPolicy
	case FailBlockedDependency:
		return ErrBlockedDependency
	default:
		return ErrNotRetryable
	}
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// RecordSessionFailure durably persists a failure classification on the
// session's BranchMeta. It is the single entry point a host uses to record
// "this session failed with this class" so a later session_retry reads the
// classification instead of guessing.
func RecordSessionFailure(sessionPath string, failure SessionFailure) error {
	if sessionPath == "" {
		return errors.New("record session failure: empty session path")
	}
	unlock := lockSessionSavePath(sessionPath)
	defer unlock()
	m, ok, err := LoadBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	if !ok {
		m = BranchMeta{ID: BranchID(sessionPath), CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	}
	if failure.At.IsZero() {
		failure.At = time.Now().UTC()
	}
	m.Failure = &failure
	m.Status = SessionStatusFailed
	return SaveBranchMetaPreserveUpdated(sessionPath, m)
}

// ClearSessionFailure removes a previously recorded failure and clears the
// durable failed status (resetting to the derived state). It is idempotent.
func ClearSessionFailure(sessionPath string) error {
	if sessionPath == "" {
		return errors.New("clear session failure: empty session path")
	}
	unlock := lockSessionSavePath(sessionPath)
	defer unlock()
	m, ok, err := LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return err
	}
	if m.Failure == nil && m.Status != SessionStatusFailed {
		return nil
	}
	m.Failure = nil
	m.Status = ""
	return SaveBranchMetaPreserveUpdated(sessionPath, m)
}

// LoadSessionFailure returns the recorded failure of a session, or nil.
func LoadSessionFailure(sessionPath string) *SessionFailure {
	m, ok, err := LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return nil
	}
	return m.Failure
}
