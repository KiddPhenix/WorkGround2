package work

import (
	"errors"
	"fmt"
)

// ErrActionPermissionUnavailable reports that a gated action has no usable
// permission checker. The action is not executed and remains safely retryable.
var ErrActionPermissionUnavailable = errors.New("work: action permission checker is unavailable")

// ErrActionHandlerRegistrationConflict rejects an in-process replacement that
// reuses the same stable handler identity/version for an action key.
type ErrActionHandlerRegistrationConflict struct {
	BlockKind      string
	ActionID       string
	HandlerID      string
	HandlerVersion string
}

func (e *ErrActionHandlerRegistrationConflict) Error() string {
	return fmt.Sprintf("work: action register: %s/%s already uses handler %s@%s", e.BlockKind, e.ActionID, e.HandlerID, e.HandlerVersion)
}

// ErrActionHandlerVersionConflict reports that a reserved action cannot be
// resumed by the currently registered handler. Latest is the persisted receipt
// after the safe failure/unknown transition.
type ErrActionHandlerVersionConflict struct {
	RequestID      string
	HandlerID      string
	HandlerVersion string
	CurrentID      string
	CurrentVersion string
	Retryable      bool
	Latest         *ActionReceipt
	Reason         string
}

func (e *ErrActionHandlerVersionConflict) Error() string {
	expected := e.HandlerID + "@" + e.HandlerVersion
	current := e.CurrentID + "@" + e.CurrentVersion
	return fmt.Sprintf("work: action: handler version conflict for requestID %q (reserved %s, current %s): %s", e.RequestID, expected, current, e.Reason)
}

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
