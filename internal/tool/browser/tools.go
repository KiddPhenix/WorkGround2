package browsertool

import (
	"context"
	"encoding/json"
	"unicode/utf8"

	"workground2/internal/browser"
	"workground2/internal/tool"
)

// NewTools creates all browser tools bound to a Service.
func NewTools(svc browser.Service) []tool.Tool {
	return []tool.Tool{
		&openTool{svc: svc},
		&navigateTool{svc: svc},
		&stateTool{svc: svc},
		&clickTool{svc: svc},
		&typeTool{svc: svc},
		&scrollTool{svc: svc},
		&tabTool{svc: svc},
		&closeTool{svc: svc},
	}
}

// ── browser_open ────────────────────────────────────────────────────────────

type openTool struct{ svc browser.Service }

func (t *openTool) Name() string       { return "browser_open" }
func (t *openTool) ReadOnly() bool     { return false }
func (t *openTool) PlanModeSafe() bool { return false }

func (t *openTool) Description() string {
	return "Open or reuse a browser session for the current agent. " +
		"Returns the session ID, page revision, and browser info. " +
		"The browser process is created on first call and reused for the same session."
}

func (t *openTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "url":{"type":"string","description":"Optional URL to navigate to after opening. Defaults to about:blank."},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars. Reuse the same value for safe retries; use a new value for a new intent.","minLength":1,"maxLength":128}
},
"required":["request_id"]
}`)
}

func (t *openTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		URL       string `json:"url"`
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.OpenResult](err)
	}
	if !validRequestID(p.RequestID) {
		return marshalError[browser.OpenResult](invalidArgs("request_id must contain 1-128 characters"))
	}
	if len(p.URL) > 8192 {
		return marshalError[browser.OpenResult](invalidArgs("url must not exceed 8192 characters"))
	}
	result, err := t.svc.Open(ctx, owner, browser.OpenRequest{URL: p.URL, RequestID: p.RequestID})
	if err != nil {
		return marshalBrowserError[browser.OpenResult](err)
	}
	return marshalOK(result)
}

// ── browser_navigate ────────────────────────────────────────────────────────

type navigateTool struct{ svc browser.Service }

func (t *navigateTool) Name() string       { return "browser_navigate" }
func (t *navigateTool) ReadOnly() bool     { return false }
func (t *navigateTool) PlanModeSafe() bool { return false }

func (t *navigateTool) Description() string {
	return "Navigate the active browser tab to a URL. Returns before/after revision."
}

func (t *navigateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "url":{"type":"string","description":"Absolute http/https URL to navigate to, up to 8192 characters.","maxLength":8192},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars.","minLength":1,"maxLength":128}
},
"required":["url","request_id"]
}`)
}

func (t *navigateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		URL       string `json:"url"`
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.ActionResult](err)
	}
	if p.URL == "" || len(p.URL) > 8192 {
		return marshalError[browser.ActionResult](invalidArgs("url is required and must not exceed 8192 characters"))
	}
	if !validRequestID(p.RequestID) {
		return marshalError[browser.ActionResult](invalidArgs("request_id must contain 1-128 characters"))
	}
	result, err := t.svc.Navigate(ctx, owner, browser.NavigateRequest{URL: p.URL, RequestID: p.RequestID})
	if err != nil {
		return marshalBrowserError[browser.ActionResult](err)
	}
	return marshalOK(result)
}

// ── browser_state ───────────────────────────────────────────────────────────

type stateTool struct{ svc browser.Service }

func (t *stateTool) Name() string       { return "browser_state" }
func (t *stateTool) ReadOnly() bool     { return true }
func (t *stateTool) PlanModeSafe() bool { return true }

func (t *stateTool) Description() string {
	return "Get the current page state: URL, title, text content, and indexed interactive elements. " +
		"Use this before any click/type/scroll action to get fresh element indices. " +
		"Returns a revision number that must be passed to write actions."
}

func (t *stateTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "refresh":{"type":"boolean","description":"Force a fresh observation. Default true."},
  "max_chars":{"type":"integer","description":"Max text characters, 1000-60000. Default uses config (20000).","minimum":1000,"maximum":60000}
}
}`)
}

func (t *stateTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		Refresh  *bool `json:"refresh"`
		MaxChars int   `json:"max_chars"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.PageState](err)
	}
	if p.MaxChars != 0 && (p.MaxChars < 1000 || p.MaxChars > 60000) {
		return marshalError[browser.PageState](invalidArgs("max_chars must be between 1000 and 60000"))
	}
	refresh := true
	if p.Refresh != nil {
		refresh = *p.Refresh
	}
	result, err := t.svc.State(ctx, owner, browser.StateRequest{Refresh: refresh, MaxChars: p.MaxChars})
	if err != nil {
		return marshalBrowserError[browser.PageState](err)
	}
	return marshalOK(result)
}

func (t *stateTool) SnipHint() tool.SnipHint {
	return tool.SnipHint{Head: 60, Tail: 10, HeadChars: 8000, TailChars: 2000}
}

// ── browser_click ───────────────────────────────────────────────────────────

type clickTool struct{ svc browser.Service }

func (t *clickTool) Name() string       { return "browser_click" }
func (t *clickTool) ReadOnly() bool     { return false }
func (t *clickTool) PlanModeSafe() bool { return false }

func (t *clickTool) Description() string {
	return "Click an interactive element identified by its index from browser_state. " +
		"Must pass the current revision to prevent stale element clicks."
}

func (t *clickTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "revision":{"type":"integer","description":"Page revision from browser_state. Minimum 1.","minimum":1},
  "index":{"type":"integer","description":"Element index from browser_state. Minimum 1.","minimum":1},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars.","minLength":1,"maxLength":128}
},
"required":["revision","index","request_id"]
}`)
}

func (t *clickTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		Revision  uint64 `json:"revision"`
		Index     int    `json:"index"`
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.ActionResult](err)
	}
	if p.Revision < 1 || p.Index < 1 || !validRequestID(p.RequestID) {
		return marshalError[browser.ActionResult](invalidArgs("revision and index must be at least 1, and request_id must contain 1-128 characters"))
	}
	result, err := t.svc.Click(ctx, owner, browser.ClickRequest{
		Revision: p.Revision, Index: p.Index, RequestID: p.RequestID,
	})
	if err != nil {
		return marshalBrowserError[browser.ActionResult](err)
	}
	return marshalOK(result)
}

// ── browser_type ────────────────────────────────────────────────────────────

type typeTool struct{ svc browser.Service }

func (t *typeTool) Name() string       { return "browser_type" }
func (t *typeTool) ReadOnly() bool     { return false }
func (t *typeTool) PlanModeSafe() bool { return false }

func (t *typeTool) Description() string {
	return "Type text into an editable element. Do NOT type passwords or secrets as plain text. " +
		"Optionally clear the field first and/or press Enter after typing."
}

func (t *typeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "revision":{"type":"integer","description":"Page revision from browser_state. Minimum 1.","minimum":1},
  "index":{"type":"integer","description":"Element index from browser_state. Minimum 1.","minimum":1},
  "text":{"type":"string","description":"Text to type, 0-20000 chars.","maxLength":20000},
  "clear":{"type":"boolean","description":"Clear the field before typing. Default false."},
  "press_enter":{"type":"boolean","description":"Press Enter after typing. Default false."},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars.","minLength":1,"maxLength":128}
},
"required":["revision","index","text","request_id"]
}`)
}

func (t *typeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		Revision   uint64  `json:"revision"`
		Index      int     `json:"index"`
		Text       *string `json:"text"`
		Clear      bool    `json:"clear"`
		PressEnter bool    `json:"press_enter"`
		RequestID  string  `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.ActionResult](err)
	}
	if p.Revision < 1 || p.Index < 1 || p.Text == nil || utf8.RuneCountInString(*p.Text) > 20000 || !validRequestID(p.RequestID) {
		return marshalError[browser.ActionResult](invalidArgs("revision/index/text/request_id do not satisfy the tool Schema"))
	}
	if *p.Text == "" && !p.Clear && !p.PressEnter {
		return marshalError[browser.ActionResult](invalidArgs("empty text requires clear=true or press_enter=true"))
	}
	result, err := t.svc.Type(ctx, owner, browser.TypeRequest{
		Revision: p.Revision, Index: p.Index, Text: *p.Text,
		Clear: p.Clear, PressEnter: p.PressEnter, RequestID: p.RequestID,
	})
	if err != nil {
		return marshalBrowserError[browser.ActionResult](err)
	}
	return marshalOK(result)
}

// ── browser_scroll ──────────────────────────────────────────────────────────

type scrollTool struct{ svc browser.Service }

func (t *scrollTool) Name() string       { return "browser_scroll" }
func (t *scrollTool) ReadOnly() bool     { return false }
func (t *scrollTool) PlanModeSafe() bool { return false }

func (t *scrollTool) Description() string {
	return "Scroll the page or a specific element. index=0 scrolls the viewport."
}

func (t *scrollTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "revision":{"type":"integer","description":"Page revision from browser_state. Minimum 1.","minimum":1},
  "index":{"type":"integer","description":"Element index (0 = viewport). Default 0.","minimum":0},
  "delta_y":{"type":"integer","description":"Scroll amount in pixels, -4000 to 4000. Must not be 0.","minimum":-4000,"maximum":4000},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars.","minLength":1,"maxLength":128}
},
"required":["revision","delta_y","request_id"]
}`)
}

func (t *scrollTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		Revision  uint64 `json:"revision"`
		Index     int    `json:"index"`
		DeltaY    int    `json:"delta_y"`
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.ActionResult](err)
	}
	if p.Revision < 1 || p.Index < 0 || p.DeltaY == 0 || p.DeltaY < -4000 || p.DeltaY > 4000 || !validRequestID(p.RequestID) {
		return marshalError[browser.ActionResult](invalidArgs("revision/index/delta_y/request_id do not satisfy the tool Schema"))
	}
	result, err := t.svc.Scroll(ctx, owner, browser.ScrollRequest{
		Revision: p.Revision, Index: p.Index, DeltaY: p.DeltaY, RequestID: p.RequestID,
	})
	if err != nil {
		return marshalBrowserError[browser.ActionResult](err)
	}
	return marshalOK(result)
}

// ── browser_tab ─────────────────────────────────────────────────────────────

type tabTool struct{ svc browser.Service }

func (t *tabTool) Name() string       { return "browser_tab" }
func (t *tabTool) ReadOnly() bool     { return false }
func (t *tabTool) PlanModeSafe() bool { return false }

func (t *tabTool) Description() string {
	return "Manage browser tabs: new (create), activate (switch to), close. Cannot close the last tab."
}

func (t *tabTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{
  "revision":{"type":"integer","description":"Page revision from browser_state. Minimum 1.","minimum":1},
  "action":{"type":"string","enum":["new","activate","close"],"description":"Tab action: new, activate, or close."},
  "tab_id":{"type":"string","description":"Target tab ID. Required for activate and close."},
  "url":{"type":"string","description":"URL for new tab. Default about:blank.","maxLength":8192},
  "request_id":{"type":"string","description":"Idempotency key, 1-128 chars.","minLength":1,"maxLength":128}
},
"required":["revision","action","request_id"],
"oneOf":[
  {"properties":{"action":{"const":"new"},"tab_id":{"not":{}}}},
  {"properties":{"action":{"const":"activate"},"url":{"not":{}}},"required":["tab_id"]},
  {"properties":{"action":{"const":"close"},"url":{"not":{}}},"required":["tab_id"]}
]
}`)
}

func (t *tabTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct {
		Revision  uint64 `json:"revision"`
		Action    string `json:"action"`
		TabID     string `json:"tab_id"`
		URL       string `json:"url"`
		RequestID string `json:"request_id"`
	}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.ActionResult](err)
	}
	if p.Revision < 1 || !validRequestID(p.RequestID) || len(p.URL) > 8192 {
		return marshalError[browser.ActionResult](invalidArgs("revision/request_id/url do not satisfy the tool Schema"))
	}
	// Validate constraints manually.
	action := browser.TabAction(p.Action)
	if action != browser.TabNew && action != browser.TabActivate && action != browser.TabClose {
		return marshalError[browser.ActionResult](invalidArgs("action must be new, activate, or close"))
	}
	if (action == browser.TabActivate || action == browser.TabClose) && p.TabID == "" {
		return marshalError[browser.ActionResult](invalidArgs("tab_id is required for activate/close"))
	}
	if action == browser.TabNew && p.TabID != "" {
		return marshalError[browser.ActionResult](invalidArgs("tab_id must not be set for new"))
	}
	if action != browser.TabNew && p.URL != "" {
		return marshalError[browser.ActionResult](invalidArgs("url is only valid for action=new"))
	}

	result, err := t.svc.Tab(ctx, owner, browser.TabRequest{
		Revision: p.Revision, Action: action, TabID: p.TabID,
		URL: p.URL, RequestID: p.RequestID,
	})
	if err != nil {
		return marshalBrowserError[browser.ActionResult](err)
	}
	return marshalOK(result)
}

// ── browser_close ───────────────────────────────────────────────────────────

type closeTool struct{ svc browser.Service }

func (t *closeTool) Name() string       { return "browser_close" }
func (t *closeTool) ReadOnly() bool     { return false }
func (t *closeTool) PlanModeSafe() bool { return false }

func (t *closeTool) Description() string {
	return "Close the browser session for the current agent. Idempotent — safe to call multiple times."
}

func (t *closeTool) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"additionalProperties":false,
"properties":{}
}`)
}

func (t *closeTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	owner := ownerFromContext(ctx)
	var p struct{}
	if err := decodeArgs(args, &p); err != nil {
		return marshalError[browser.CloseResult](err)
	}
	result, err := t.svc.CloseSession(ctx, owner)
	if err != nil {
		return marshalBrowserError[browser.CloseResult](err)
	}
	return marshalOK(result)
}
