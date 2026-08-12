package browser

import (
	"errors"
	"fmt"
)

// ErrorCode identifies a known browser error category.
type ErrorCode string

const (
	ErrMissingSessionScope        ErrorCode = "missing_session_scope"
	ErrBrowserNotOpen             ErrorCode = "browser_not_open"
	ErrBrowserLaunchFailed        ErrorCode = "browser_launch_failed"
	ErrUnsupportedBrowser         ErrorCode = "unsupported_browser"
	ErrProfileUnavailable         ErrorCode = "profile_unavailable"
	ErrCredentialProviderDisabled ErrorCode = "credential_provider_disabled"
	ErrBrowserDisconnected        ErrorCode = "browser_disconnected"
	ErrInvalidURL                 ErrorCode = "invalid_url"
	ErrUnsupportedScheme          ErrorCode = "unsupported_scheme"
	ErrNavigationTimeout          ErrorCode = "navigation_timeout"
	ErrStateTimeout               ErrorCode = "state_timeout"
	ErrStaleState                 ErrorCode = "stale_state"
	ErrElementNotFound            ErrorCode = "element_not_found"
	ErrElementNotInteractable     ErrorCode = "element_not_interactable"
	ErrSensitiveInputBlocked      ErrorCode = "sensitive_input_blocked"
	ErrInvalidArguments           ErrorCode = "invalid_arguments"
	ErrTargetClosed               ErrorCode = "target_closed"
	ErrLastTab                    ErrorCode = "last_tab"
	ErrRequestIDConflict          ErrorCode = "request_id_conflict"
	ErrOutcomeUnknown             ErrorCode = "outcome_unknown"
	ErrConfig                     ErrorCode = "config_error"
)

// ErrorSpec maps a code to its recoverable/outcome_known/next defaults.
type ErrorSpec struct {
	Recoverable  bool
	OutcomeKnown bool
	Next         string
}

// DefaultErrorSpecs is the authoritative error-code table.
var DefaultErrorSpecs = map[ErrorCode]ErrorSpec{
	ErrMissingSessionScope:        {false, true, "host wiring"},
	ErrBrowserNotOpen:             {true, true, "browser_open"},
	ErrBrowserLaunchFailed:        {true, true, "检查 executable/config 后重试"},
	ErrUnsupportedBrowser:         {true, true, "修改 kind/executable_path"},
	ErrProfileUnavailable:         {true, true, "修正 Profile 配置或授权"},
	ErrCredentialProviderDisabled: {false, true, "配置凭据 Provider"},
	ErrBrowserDisconnected:        {true, true, "browser_open"},
	ErrInvalidURL:                 {true, true, "修正 URL"},
	ErrUnsupportedScheme:          {true, true, "使用 HTTP/HTTPS"},
	ErrNavigationTimeout:          {true, false, "browser_state"},
	ErrStateTimeout:               {true, true, "browser_state"},
	ErrStaleState:                 {true, true, "browser_state"},
	ErrElementNotFound:            {true, true, "browser_state"},
	ErrElementNotInteractable:     {true, true, "选择其他元素"},
	ErrSensitiveInputBlocked:      {false, true, "使用未来的专用凭据/上传工具"},
	ErrInvalidArguments:           {true, true, "按工具 Schema 修正参数"},
	ErrTargetClosed:               {true, true, "browser_state"},
	ErrLastTab:                    {true, true, "browser_close 或保留标签页"},
	ErrRequestIDConflict:          {true, true, "使用新 request_id"},
	ErrOutcomeUnknown:             {true, false, "browser_state，禁止盲重试"},
	ErrConfig:                     {true, true, "检查配置"},
}

// Error is a structured browser error.
type Error struct {
	Code         ErrorCode `json:"code"`
	Message      string    `json:"message"`
	Recoverable  bool      `json:"recoverable"`
	OutcomeKnown bool      `json:"outcome_known"`
	Next         string    `json:"next,omitempty"`
	Cause        error     `json:"-"`
}

func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code, e.Message)
}

// Unwrap returns the underlying cause for errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// NewError creates an Error with defaults from DefaultErrorSpecs.
func NewError(code ErrorCode, message string, cause error) *Error {
	spec, ok := DefaultErrorSpecs[code]
	if !ok {
		spec = ErrorSpec{Recoverable: true, OutcomeKnown: false, Next: "browser_state"}
	}
	return &Error{
		Code:         code,
		Message:      message,
		Recoverable:  spec.Recoverable,
		OutcomeKnown: spec.OutcomeKnown,
		Next:         spec.Next,
		Cause:        cause,
	}
}

// DispatchError reports whether an action reached the browser before failing.
// Callers must reconcile state instead of retrying when Dispatched is true.
type DispatchError struct {
	Dispatched bool
	Cause      error
}

func (e *DispatchError) Error() string {
	if e.Cause == nil {
		return "browser action dispatch failed"
	}
	return e.Cause.Error()
}

func (e *DispatchError) Unwrap() error { return e.Cause }

func outcomeUnknown(err error) bool {
	var dispatchErr *DispatchError
	return errors.As(err, &dispatchErr) && dispatchErr.Dispatched
}
