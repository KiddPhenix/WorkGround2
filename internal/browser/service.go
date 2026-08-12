// Package browser provides the browser automation service, managing per-owner
// Chromium sessions with CDP-backed page observation and action execution.
package browser

import (
	"context"
	"time"
)

// Service is the tool-level interface for browser operations. Each method
// receives an ownerID (the WorkGround2 parent session) and a typed request.
// The Manager implements this interface.
type Service interface {
	Open(ctx context.Context, ownerID string, req OpenRequest) (OpenResult, error)
	Navigate(ctx context.Context, ownerID string, req NavigateRequest) (ActionResult, error)
	State(ctx context.Context, ownerID string, req StateRequest) (PageState, error)
	Click(ctx context.Context, ownerID string, req ClickRequest) (ActionResult, error)
	Type(ctx context.Context, ownerID string, req TypeRequest) (ActionResult, error)
	Scroll(ctx context.Context, ownerID string, req ScrollRequest) (ActionResult, error)
	Tab(ctx context.Context, ownerID string, req TabRequest) (ActionResult, error)
	CloseSession(ctx context.Context, ownerID string) (CloseResult, error)
	Close() error
}

// DriverFactory creates a new Driver for a browser session.
type DriverFactory interface {
	New(ctx context.Context, opts DriverOptions) (Driver, error)
}

// Driver controls a single Chromium instance. Its lifecycle context comes from
// the Manager, not from individual tool calls. Single-action contexts are
// passed per call for cancellation/timeout.
type Driver interface {
	Info() BrowserInfo
	Navigate(ctx context.Context, url string) error
	Observe(ctx context.Context, opts ObserveOptions) (Observation, error)
	Click(ctx context.Context, ref NodeRef) error
	Type(ctx context.Context, ref NodeRef, input TypeInput) error
	Scroll(ctx context.Context, input ScrollInput) error
	NewTab(ctx context.Context, url string) (string, error)
	ActivateTab(ctx context.Context, targetID string) error
	CloseTab(ctx context.Context, targetID string) error
	Invalidations() <-chan Invalidation
	Close() error
}

// Options configures a Manager instance.
type Options struct {
	Factory        DriverFactory
	Profiles       ProfileProvider
	Credentials    CredentialProvider
	BrowserKind    BrowserKind
	ExecutablePath string
	Headless       bool
	ProfileRoot    string
	IdleTimeout    time.Duration
	ActionTimeout  time.Duration
	StateTimeout   time.Duration
	SettleWindow   time.Duration
	MaxTextChars   int
	MaxElements    int
}

// DriverOptions configures a single Driver (Chromium process).
type DriverOptions struct {
	BrowserKind    BrowserKind
	ExecutablePath string
	Headless       bool
	UserDataDir    string
	ProfileName    string
	DebugURL       string
	OwnProcess     bool
	DenyDownloads  bool
	SettleWindow   time.Duration
}

// ObserveOptions limits what Driver.Observe collects.
type ObserveOptions struct {
	MaxTextChars int
	MaxElements  int
}

// TypeInput describes a keystroke action on an editable element.
type TypeInput struct {
	Text       string
	Clear      bool
	PressEnter bool
}

// ScrollInput describes a scroll action. Node may be nil for viewport scroll.
type ScrollInput struct {
	Node   *NodeRef
	DeltaY int
}

// InvalidationKind categorises a CDP event that may invalidate the current snapshot.
type InvalidationKind string

const (
	InvalidationDocument InvalidationKind = "document"
	InvalidationFrame    InvalidationKind = "frame"
	InvalidationTarget   InvalidationKind = "target"
	InvalidationClosed   InvalidationKind = "closed"
)

// Invalidation is sent by the Driver when the page state has changed.
type Invalidation struct {
	Kind     InvalidationKind
	TargetID string
	FrameID  string
	At       time.Time
}

// BrowserInfo describes the connected browser.
type BrowserInfo struct {
	Kind            BrowserKind `json:"kind"`
	Product         string      `json:"product"`
	Version         string      `json:"version"`
	ProtocolVersion string      `json:"protocol_version"`
	ExecutablePath  string      `json:"executable_path,omitempty"`
}

// BrowserKind identifies a supported Chromium-based browser.
type BrowserKind string

const (
	BrowserAuto             BrowserKind = "auto"
	BrowserChrome           BrowserKind = "chrome"
	BrowserChromium         BrowserKind = "chromium"
	BrowserEdge             BrowserKind = "edge"
	BrowserChromeForTesting BrowserKind = "chrome_for_testing"
)

// BrowserAutoOrder is the discovery order for BrowserAuto.
var BrowserAutoOrder = []BrowserKind{
	BrowserChrome,
	BrowserEdge,
	BrowserChromium,
	BrowserChromeForTesting,
}

// SessionState tracks the lifecycle phase of a browser session.
type SessionState string

const (
	SessionStarting SessionState = "starting"
	SessionReady    SessionState = "ready"
	SessionBroken   SessionState = "broken"
	SessionClosing  SessionState = "closing"
	SessionClosed   SessionState = "closed"
)
