// Package browsertool provides browser automation tools that implement tool.Tool.
// Each tool receives a browser.Service at construction time and extracts the
// owner (parent session) from the context via jobs.SessionFromContext.
package browsertool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"workground2/internal/browser"
	"workground2/internal/jobs"
)

// ToolResponse is the uniform JSON envelope for all browser tool results.
type ToolResponse[T any] struct {
	OK     bool       `json:"ok"`
	Result *T         `json:"result,omitempty"`
	Error  *ErrorInfo `json:"error,omitempty"`
}

// ErrorInfo is the structured error in the envelope.
type ErrorInfo struct {
	Code         browser.ErrorCode      `json:"code"`
	Message      string                 `json:"message"`
	Recoverable  bool                   `json:"recoverable"`
	OutcomeKnown bool                   `json:"outcome_known"`
	Next         string                 `json:"next,omitempty"`
	Dialog       *browser.DialogContext `json:"dialog,omitempty"`
}

// ownerFromContext extracts the parent session ID from the context.
func ownerFromContext(ctx context.Context) string {
	return jobs.SessionFromContext(ctx)
}

func decodeArgs(args json.RawMessage, dst any) *browser.Error {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalidArgs(err.Error())
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return invalidArgs("arguments must contain exactly one JSON object")
		}
		return invalidArgs(err.Error())
	}
	return nil
}

func invalidArgs(message string) *browser.Error {
	return browser.NewError(browser.ErrInvalidArguments, message, nil)
}

func validRequestID(id string) bool { return len(id) >= 1 && len(id) <= 128 }

func marshalBrowserError[T any](err error) (string, error) {
	return marshalError[T](toBrowserError(err))
}

// marshalOK marshals a successful result.
func marshalOK[T any](result T) (string, error) {
	resp := ToolResponse[T]{OK: true, Result: &result}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// marshalError marshals an error result. Always returns a non-nil Go error
// alongside the structured JSON.
func marshalError[T any](err *browser.Error) (string, error) {
	resp := ToolResponse[T]{
		OK: false,
		Error: &ErrorInfo{
			Code:         err.Code,
			Message:      err.Message,
			Recoverable:  err.Recoverable,
			OutcomeKnown: err.OutcomeKnown,
			Next:         err.Next,
			Dialog:       err.Dialog,
		},
	}
	b, marshalErr := json.Marshal(resp)
	if marshalErr != nil {
		return "", fmt.Errorf("marshal error: %w (original: %v)", marshalErr, err)
	}
	return string(b), err
}

// toBrowserError converts a generic error to a browser.Error.
func toBrowserError(err error) *browser.Error {
	var be *browser.Error
	if errors.As(err, &be) {
		return be
	}
	return browser.NewError(browser.ErrConfig, err.Error(), err)
}
