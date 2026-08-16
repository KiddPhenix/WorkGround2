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
	ErrRelayUnavailable           ErrorCode = "relay_unavailable"
	ErrDialogBlocked              ErrorCode = "dialog_blocked"
	ErrDialogResolutionFailed     ErrorCode = "dialog_resolution_failed"
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
	ErrRelayUnavailable:           {true, true, "重试 HTTP 导航"},
	ErrDialogBlocked:              {true, true, "使用新 request_id 并传 allow_leave=true 重试"},
	ErrDialogResolutionFailed:     {true, false, "调用 browser_state 对账，禁止盲重试"},
}

// DialogType identifies the kind of JavaScript dialog that interrupted an action.
type DialogType string

const (
	DialogAlert        DialogType = "alert"
	DialogConfirm      DialogType = "confirm"
	DialogPrompt       DialogType = "prompt"
	DialogBeforeUnload DialogType = "beforeunload"
)

// DialogContext carries structured context about a JavaScript dialog that
// interrupted an operation. It is exposed on dialog_blocked errors so callers
// can report and decide next steps without re-parsing error text.
type DialogContext struct {
	TargetID      string     `json:"target_id"`
	Type          DialogType `json:"type"`
	Message       string     `json:"message,omitempty"`
	DefaultPrompt string     `json:"default_prompt,omitempty"`
}

// Error is a structured browser error.
type Error struct {
	Code         ErrorCode      `json:"code"`
	Message      string         `json:"message"`
	Recoverable  bool           `json:"recoverable"`
	OutcomeKnown bool           `json:"outcome_known"`
	Next         string         `json:"next,omitempty"`
	Dialog       *DialogContext `json:"dialog,omitempty"`
	Cause        error          `json:"-"`
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

// NewDialogBlockedError reports that a JavaScript dialog blocked the requested
// continuation and was safely dismissed. Navigate and tab-close callers know
// that the page stayed; clicks use NewDialogBlockedUnknownError because their
// handlers may already have produced side effects before the dialog opened.
func NewDialogBlockedError(dialog DialogContext, message string, cause error) *Error {
	e := NewError(ErrDialogBlocked, message, cause)
	e.Dialog = &dialog
	if dialog.Type != DialogBeforeUnload {
		e.Next = "该 confirm/prompt 已安全取消；如需接受，请改用 browser_attach 后通过 Playwright 处理"
	}
	return e
}

// NewDialogBlockedUnknownError is used for clicks because the DOM click was
// already dispatched before its dialog appeared. The requested continuation
// was blocked, but arbitrary click handlers may already have produced side
// effects, so callers must reconcile state before deciding whether to retry.
func NewDialogBlockedUnknownError(dialog DialogContext, message string, cause error) *Error {
	e := NewDialogBlockedError(dialog, message, cause)
	e.OutcomeKnown = false
	e.Next = "先调用 browser_state 对账；确认点击可安全重复后，再使用新 request_id 重试"
	return e
}

// NewDialogResolutionError reports that a native dialog was detected but CDP
// could not confirm whether it was accepted or dismissed. Callers must
// reconcile browser state before retrying because the page outcome is unknown.
func NewDialogResolutionError(dialog DialogContext, cause error) *Error {
	e := NewError(ErrDialogResolutionFailed, "failed to resolve JavaScript dialog; browser outcome is unknown", cause)
	e.Dialog = &dialog
	return e
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
