package assistant

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound    = errors.New("assistant: not found")
	ErrConflict    = errors.New("assistant: revision conflict")
	ErrBusy        = errors.New("assistant: run already active")
	ErrIdempotency = errors.New("assistant: request id reused with different input")
	ErrLeaseLost   = errors.New("assistant: run lease lost")
	ErrTransition  = errors.New("assistant: invalid run state transition")
	ErrCorrupt     = errors.New("assistant: corrupt aggregate")
)

type ConflictError struct {
	Entity   string
	ID       string
	Expected int64
	Actual   int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("assistant: %s %s revision conflict: expected %d, actual %d", e.Entity, e.ID, e.Expected, e.Actual)
}

func (e *ConflictError) Unwrap() error { return ErrConflict }

type IdempotencyError struct {
	RequestID string
	Operation string
}

func (e *IdempotencyError) Error() string {
	return fmt.Sprintf("assistant: request %q was already used for different %s input", e.RequestID, e.Operation)
}

func (e *IdempotencyError) Unwrap() error { return ErrIdempotency }
