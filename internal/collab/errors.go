package collab

import "fmt"

type ErrorCode string

const (
	CodeInvalid      ErrorCode = "invalid_request"
	CodeUnauthorized ErrorCode = "unauthorized"
	CodeForbidden    ErrorCode = "forbidden"
	CodeNotFound     ErrorCode = "not_found"
	CodeConflict     ErrorCode = "conflict"
	CodeUnavailable  ErrorCode = "unavailable"
	CodeInternal     ErrorCode = "internal"
)

// Error is safe to return through the HTTP boundary.
type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	Cause     error     `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

func fail(code ErrorCode, message string) error { return &Error{Code: code, Message: message} }
func retryable(message string, cause error) error {
	return &Error{Code: CodeUnavailable, Message: message, Retryable: true, Cause: cause}
}
