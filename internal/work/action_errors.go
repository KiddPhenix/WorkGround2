package work

import (
	"errors"
	"fmt"
)

// ErrActionPermissionUnavailable reports that a gated action has no usable
// permission checker. The action is not executed and remains safely retryable.
var ErrActionPermissionUnavailable = errors.New("work: action permission checker is unavailable")

// ErrActionUnknownIntent reports that no trusted handler owns the requested intent.
type ErrActionUnknownIntent struct {
	BlockKind string
	ActionID  string
}

// ErrActionUnknownAction reports that a block does not advertise an action ID.
type ErrActionUnknownAction struct {
	BlockID  string
	ActionID string
}

func (e *ErrActionUnknownAction) Error() string {
	return fmt.Sprintf("work: action %q is not available on block %q", e.ActionID, e.BlockID)
}

func (e *ErrActionUnknownIntent) Error() string {
	return fmt.Sprintf("work: action: no trusted handler for %q/%q", e.BlockKind, e.ActionID)
}

// ErrActionDefinitionMismatch reports an untrusted intent that differs from the registry.
type ErrActionDefinitionMismatch struct {
	BlockKind string
	ActionID  string
	Declared  string
	Canonical string
}

func (e *ErrActionDefinitionMismatch) Error() string {
	return fmt.Sprintf("work: action: %q/%q declares intent %q; trusted intent is %q", e.BlockKind, e.ActionID, e.Declared, e.Canonical)
}

// ErrActionFingerprintConflict reports request-ID reuse with different content.
type ErrActionFingerprintConflict struct {
	RequestID  string
	ExistingFP string
	IncomingFP string
}

func (e *ErrActionFingerprintConflict) Error() string {
	return fmt.Sprintf("work: action: requestID %q reused with different content (existing %s, incoming %s)", e.RequestID, e.ExistingFP, e.IncomingFP)
}

// ErrActionOutcomeUnknown requires inspection/verification before a new
// request retries an action that may already have changed external state.
type ErrActionOutcomeUnknown struct {
	RequestID string
	Message   string
}

// ErrActionRejected reports a permission or approval rejection before execution.
type ErrActionRejected struct {
	RequestID string
	Message   string
}

func (e *ErrActionRejected) Error() string {
	return fmt.Sprintf("work: action: requestID %q rejected: %s", e.RequestID, e.Message)
}

func (e *ErrActionOutcomeUnknown) Error() string {
	return fmt.Sprintf("work: action: outcome for requestID %q is unknown: %s", e.RequestID, e.Message)
}

// ErrActionRevisionConflict carries the latest projection so callers can
// recover without guessing which view or tab originated the request.
type ErrActionRevisionConflict struct {
	WorkID   string
	Expected int64
	Current  int64
	Latest   *WorkView
}

func (e *ErrActionRevisionConflict) Error() string {
	return fmt.Sprintf("work: action: expected revision %d, current revision %d on Work %s", e.Expected, e.Current, e.WorkID)
}

func (e *ErrActionRevisionConflict) Unwrap() error {
	return revisionConflict(e.WorkID, e.Expected, e.Current)
}
