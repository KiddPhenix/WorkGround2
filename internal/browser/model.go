package browser

import "time"

// ── Page state ──────────────────────────────────────────────────────────────

// PageState is the immutable page snapshot returned to the model.
type PageState struct {
	SessionID  string         `json:"session_id"`
	Revision   uint64         `json:"revision"`
	URL        string         `json:"url"`
	Title      string         `json:"title"`
	ActiveTab  string         `json:"active_tab"`
	Tabs       []TabInfo      `json:"tabs"`
	Text       string         `json:"text,omitempty"`
	Elements   []Element      `json:"elements"`
	Warnings   []StateWarning `json:"warnings,omitempty"`
	Truncated  bool           `json:"truncated"`
	CapturedAt time.Time      `json:"captured_at"`
	// NextElementIndex is the index of the first element NOT included in this
	// response because it was trimmed to fit the model-facing byte budget
	// (0 when every element of the page is present). Pass it back as
	// StateRequest.ElementStart with the same revision to fetch the next page
	// of elements from the same snapshot.
	NextElementIndex int `json:"next_element_index,omitempty"`
	// RemainingElements counts the elements not included in this response
	// (0 when every element of the page is present).
	RemainingElements int `json:"remaining_elements,omitempty"`
}

// StateWarning describes a non-fatal observation issue.
type StateWarning struct {
	Code    string `json:"code"`
	FrameID string `json:"frame_id,omitempty"`
	Message string `json:"message"`
}

// TabInfo describes a single browser tab.
type TabInfo struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// Element is an interactive element visible in the page.
type Element struct {
	Index       int    `json:"index"`
	Role        string `json:"role,omitempty"`
	Tag         string `json:"tag,omitempty"`
	InputType   string `json:"input_type,omitempty"`
	Name        string `json:"name,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Href        string `json:"href,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
	Checked     *bool  `json:"checked,omitempty"`
	Editable    bool   `json:"editable,omitempty"`
	Bounds      Rect   `json:"bounds"`
}

// Rect is a bounding rectangle in CSS pixels.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ── Internal snapshot ───────────────────────────────────────────────────────

// Snapshot holds the immutable page snapshot plus internal node mapping.
type Snapshot struct {
	State       PageState
	Nodes       map[int]NodeRef
	Fingerprint string
	Generation  uint64
}

// NodeRef is an opaque reference to a DOM node used for actions.
type NodeRef struct {
	TargetID      string
	FrameID       string
	BackendNodeID int64
	Bounds        Rect
}

// ── Driver observation ──────────────────────────────────────────────────────

// Observation is the raw data Driver.Observe returns.
type Observation struct {
	URL         string
	Title       string
	ActiveTab   string
	Tabs        []TabInfo
	Text        string
	Nodes       []ObservedNode
	Warnings    []StateWarning
	Fingerprint string
	Truncated   bool
}

// ObservedNode is a raw interactive node from CDP before indexing.
type ObservedNode struct {
	Ref         NodeRef
	Role        string
	Tag         string
	InputType   string
	Name        string
	Placeholder string
	Href        string
	Disabled    bool
	Checked     *bool
	Editable    bool
}

// ── Requests ────────────────────────────────────────────────────────────────

// OpenRequest starts or reuses a browser session.
type OpenRequest struct {
	URL       string
	RequestID string
}

// OpenResult describes the outcome of a browser_open.
type OpenResult struct {
	SessionID string      `json:"session_id"`
	Created   bool        `json:"created"`
	Revision  uint64      `json:"revision"`
	URL       string      `json:"url"`
	Title     string      `json:"title"`
	Browser   BrowserInfo `json:"browser"`
}

// ActionOptions carries per-action policy overrides. The zero value keeps the
// safe default behavior for every action.
type ActionOptions struct {
	// AllowLeave accepts a beforeunload dialog triggered by the action
	// (navigate / click / tab close), letting the page leave. Default false:
	// the dialog is dismissed (stay), the action is NOT completed, and the
	// driver returns dialog_blocked.
	AllowLeave bool
}

// NavigateRequest navigates the active tab to a URL.
type NavigateRequest struct {
	URL       string
	RequestID string
	// AllowLeave accepts a beforeunload dialog triggered by this navigation.
	AllowLeave bool
}

// StateRequest requests a page state snapshot.
type StateRequest struct {
	Refresh  bool
	MaxChars int
	// Revision pins the request to an existing snapshot: when set, the call
	// serves from that snapshot only and returns ErrStaleState if the current
	// revision differs or the snapshot was invalidated — it never refreshes
	// and substitutes fresh data. The Refresh flag is ignored when Revision
	// is set.
	Revision *uint64
	// ElementStart returns only the elements whose Index is >= ElementStart,
	// preserving their original indices. <= 0 returns all elements.
	ElementStart int
}

// ClickRequest clicks an interactive element.
type ClickRequest struct {
	Revision  uint64
	Index     int
	RequestID string
	// AllowLeave accepts a beforeunload dialog triggered by this click.
	AllowLeave bool
}

// TypeRequest types text into an editable element.
type TypeRequest struct {
	Revision   uint64
	Index      int
	Text       string
	Clear      bool
	PressEnter bool
	RequestID  string
}

// UploadRequest sets local files on a file input. Files must be absolute paths
// (or paths relative to the WorkGround2 process working directory) to existing
// regular files; 1..20 entries.
type UploadRequest struct {
	Revision  uint64
	Index     int
	Files     []string
	RequestID string
}

// ScrollRequest scrolls the page or an element.
type ScrollRequest struct {
	Revision  uint64
	Index     int
	DeltaY    int
	RequestID string
}

// TabAction is the operation for browser_tab.
type TabAction string

const (
	TabNew      TabAction = "new"
	TabActivate TabAction = "activate"
	TabClose    TabAction = "close"
)

// TabRequest manipulates browser tabs.
type TabRequest struct {
	Revision  uint64
	Action    TabAction
	TabID     string
	URL       string
	RequestID string
	// AllowLeave accepts a beforeunload dialog triggered by a tab close.
	AllowLeave bool
}

// ── Results ─────────────────────────────────────────────────────────────────

// ActionResult is the common result for write operations.
type ActionResult struct {
	SessionID      string `json:"session_id"`
	RequestID      string `json:"request_id"`
	BeforeRevision uint64 `json:"before_revision"`
	AfterRevision  uint64 `json:"after_revision"`
	Changed        bool   `json:"changed"`
	Method         string `json:"method,omitempty"`
	URL            string `json:"url"`
	Title          string `json:"title"`
	Next           string `json:"next"`
}

// CloseResult describes the outcome of a browser_close.
type CloseResult struct {
	SessionID string `json:"session_id"`
	Closed    bool   `json:"closed"`
}

// AttachResult describes the outcome of a browser_attach. Endpoint is the
// loopback CDP HTTP endpoint usable with Playwright's
// chromium.connectOverCDP(); it never carries PID, profile path or credentials.
type AttachResult struct {
	SessionID string      `json:"session_id"`
	Endpoint  string      `json:"endpoint"`
	Browser   BrowserInfo `json:"browser"`
}
