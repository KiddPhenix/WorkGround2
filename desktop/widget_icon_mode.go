package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/fileutil"
	"workground2/internal/provider"
	"workground2/internal/runhub"
	"workground2/internal/unread"
)

const (
	desktopIconActionLimit = 128
	desktopIconMaxPeople   = 6
	desktopIconMaxTasks    = 8
	desktopIconMaxSpaces   = desktopWorkspacePinLimit
	desktopIconWidth       = 1080
	desktopIconHeight      = 720
	desktopIconMinWidth    = 640
	desktopIconMinHeight   = 540
	legacyIconWidth        = 900
	legacyIconHeight       = 600
)

// DesktopIconNotice is one durable or Controller-backed event attached to its
// source icon. Runtime progress is deliberately separate and never increments
// UnreadCount.
type DesktopIconNotice struct {
	ID            string               `json:"id"`
	Revision      string               `json:"revision"`
	Kind          string               `json:"kind"`
	Priority      int                  `json:"priority"`
	Attention     unread.ItemAttention `json:"attention,omitempty"`
	Title         string               `json:"title"`
	Body          string               `json:"body"`
	CreatedAt     int64                `json:"createdAt"`
	TabID         string               `json:"tabId,omitempty"`
	Conversation  string               `json:"conversation,omitempty"`
	ReadSequence  uint64               `json:"readSequence,omitempty"`
	InteractionID string               `json:"interactionId,omitempty"`
	QuestionID    string               `json:"questionId,omitempty"`
	Questions     []WidgetQuestion     `json:"questions,omitempty"`
	Options       []WidgetOption       `json:"options"`
	Retryable     bool                 `json:"retryable,omitempty"`
	// SummaryStatus reports the news-style summary state for completion
	// notices: empty while generating (mechanical body), "ready" once the
	// LLM summary is cached, or "failed" after a visible generation error.
	SummaryStatus string `json:"summaryStatus,omitempty"`
}

type DesktopIconRuntime struct {
	Phase     string `json:"phase"`
	Summary   string `json:"summary"`
	ElapsedMs int64  `json:"elapsedMs"`
	UpdatedAt int64  `json:"updatedAt"`
}

type DesktopIconPosition struct {
	Row   string `json:"row"`
	Zone  string `json:"zone"`
	Order int    `json:"order"`
}

type DesktopIconRect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// DesktopIconHitRegionsInput binds one hit-region snapshot to the native
// surface generation whose client coordinates it was measured against.
type DesktopIconHitRegionsInput struct {
	Rects      []DesktopIconRect `json:"rects"`
	Generation int64             `json:"generation"`
}

func normalizeDesktopIconRects(rects []DesktopIconRect, width, height int) []DesktopIconRect {
	out := make([]DesktopIconRect, 0, min(len(rects), 64))
	for _, rect := range rects {
		if len(out) == 64 {
			break
		}
		if rect.Width <= 0 || rect.Height <= 0 {
			continue
		}
		x1, y1 := max(0, rect.X), max(0, rect.Y)
		x2, y2 := min(width, rect.X+rect.Width), min(height, rect.Y+rect.Height)
		if x2 <= x1 || y2 <= y1 {
			continue
		}
		out = append(out, DesktopIconRect{X: x1, Y: y1, Width: x2 - x1, Height: y2 - y1})
	}
	return out
}

// SetDesktopIconHitRegions makes only visible controls participate in native
// hit testing, so transparent gaps remain part of the real desktop.
func (a *App) SetDesktopIconHitRegions(input DesktopIconHitRegionsInput) error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	if !a.widgetMode || a.widgetStyle != "icons" {
		a.widgetMu.Unlock()
		return nil
	}
	// A bottom-right anchored resize changes the client origin. Requests
	// measured against an older surface must never reinstall the old HRGN after
	// SetDesktopIconSurface cleared it.
	if input.Generation != a.widgetSurfaceGen {
		a.widgetMu.Unlock()
		return nil
	}
	// Transfer from the mode lock to the native-region lock without leaving a
	// gap. An exit may proceed while Win32 applies this update, but it must wait
	// and clear this region afterwards, so a late frontend request cannot clip
	// the restored main window again.
	a.widgetRegionMu.Lock()
	a.widgetMu.Unlock()
	defer a.widgetRegionMu.Unlock()
	// The frontend reports physical WebView pixels using devicePixelRatio. Native
	// code owns the final client-bound clamp; applying WindowGetSize/GetDpiForWindow
	// here would mix Wails logical units into that physical coordinate contract.
	return setDesktopIconHitRegions(input.Rects)
}

// DesktopIconSurfaceInput is one monotonic request to resize the native icon
// canvas. Width/Height are the content's logical bounds (icons plus any open
// transient surface) and Envelope is the safety margin added on every side so
// content never sits on the transparent edge. Generation is the frontend's
// request token; it is echoed back unchanged so late responses can be dropped.
type DesktopIconSurfaceInput struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	Envelope   int   `json:"envelope"`
	Generation int64 `json:"generation"`
}

// DesktopIconSurfaceResult reports the geometry that actually took effect.
type DesktopIconSurfaceResult struct {
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	X          int   `json:"x"`
	Y          int   `json:"y"`
	Generation int64 `json:"generation"`
}

func growDesktopIconSurfaceInput(input DesktopIconSurfaceInput, current WidgetWindowState) DesktopIconSurfaceInput {
	input.Width = max(input.Width, current.Width-input.Envelope*2)
	input.Height = max(input.Height, current.Height-input.Envelope*2)
	return input
}

// SetDesktopIconSurface applies a bounded icon-surface geometry request and
// returns the geometry that actually took effect. It is idempotent: repeating
// the same request reapplies the same clamped bounds with no drift, and every
// request is anchored to the current monitor work area's bottom-right corner.
func (a *App) SetDesktopIconSurface(input DesktopIconSurfaceInput) (DesktopIconSurfaceResult, error) {
	if a.ctx == nil {
		return DesktopIconSurfaceResult{}, errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	if !a.widgetMode || a.widgetStyle != "icons" {
		return DesktopIconSurfaceResult{}, errors.New("desktop icon surface is not active")
	}
	if input.Generation < a.widgetSurfaceGen {
		state := a.widgetSurfaceState
		return DesktopIconSurfaceResult{Width: state.Width, Height: state.Height, X: state.X, Y: state.Y, Generation: input.Generation}, nil
	}
	if input.Generation == a.widgetSurfaceGen && a.widgetSurfaceGen > 0 {
		state := a.widgetSurfaceState
		return DesktopIconSurfaceResult{Width: state.Width, Height: state.Height, X: state.X, Y: state.Y, Generation: input.Generation}, nil
	}
	// The native state is the final authority for monotonic growth, including a
	// frontend remount that does not know how far an earlier instance expanded.
	// Preserve each current axis before adding this request's envelope.
	input = growDesktopIconSurfaceInput(input, a.widgetSurfaceState)
	a.widgetRegionMu.Lock()
	defer a.widgetRegionMu.Unlock()
	state, err := applyDesktopIconSurface(a.ctx, input)
	if err != nil {
		return DesktopIconSurfaceResult{}, err
	}
	// A Win32 HRGN uses coordinates relative to the old client origin. Resizing
	// a bottom-right anchored window moves that origin, so clear the old region
	// before React mounts the prepared content; the normal hit-region sync will
	// install the new precise union immediately after commit.
	if err := clearWidgetWindowRegion(); err != nil {
		return DesktopIconSurfaceResult{}, err
	}
	a.widgetSurfaceGen = input.Generation
	a.widgetSurfaceState = state
	return DesktopIconSurfaceResult{
		Width:      state.Width,
		Height:     state.Height,
		X:          state.X,
		Y:          state.Y,
		Generation: input.Generation,
	}, nil
}

// DesktopIconTaskRef is the typed session identity every task icon snapshot
// carries: scope/workspaceRoot/topicID/sessionPath. Live and retained icons
// share the same ref, which the backend generates from the live tab meta or
// the retained kept entry, so opening an icon never depends on SourceID/tabID
// being live. Frontend actions still submit only itemId/revision/requestId/
// action; the backend re-derives the ref from the snapshot item by itemId.
type DesktopIconTaskRef struct {
	Scope         string `json:"scope,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TopicID       string `json:"topicId,omitempty"`
	SessionPath   string `json:"sessionPath,omitempty"`
}

// desktopIconTaskRef canonicalizes the durable session identity exactly the
// way OpenTopicSession will interpret it: any non-project scope becomes global
// with an empty workspace root. This keeps the snapshot ref — and therefore
// the item revision — stable across the lazy tab-scope reconciliation that
// snapshot generation itself may trigger.
func desktopIconTaskRef(scope, workspaceRoot, topicID, sessionPath string) *DesktopIconTaskRef {
	scope = strings.TrimSpace(scope)
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	if scope != "project" {
		scope = "global"
		workspaceRoot = ""
	} else {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
	}
	return &DesktopIconTaskRef{
		Scope:         scope,
		WorkspaceRoot: workspaceRoot,
		TopicID:       strings.TrimSpace(topicID),
		SessionPath:   strings.TrimSpace(sessionPath),
	}
}

// DesktopIconItem is the single frontend model for rooms, people, tasks,
// workspaces and fixed actions.
type DesktopIconItem struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	SourceID string `json:"sourceId"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Icon     string `json:"icon,omitempty"`
	// SessionID is the stable identity seed for the Agent Icon (live tab
	// SessionID, or the retained kept SessionID; legacy kept entries leave it
	// empty and the frontend falls back to sessionRef/sessionPath). It is
	// display-only: opening an icon still routes through SessionRef.
	SessionID string `json:"sessionId,omitempty"`
	// AppearanceSeed overrides the stable session identity only for Agent Icon
	// appearance selection. It is persisted by durable session path and changes
	// only after an explicit "换个样子" action succeeds.
	AppearanceSeed string `json:"appearanceSeed,omitempty"`
	// WorkspaceIcon is the normalized project icon key of the task's workspace
	// (global-scope tasks carry the global project icon). The frontend reuses
	// it for the Agent Icon workspace badge; display-only, stable per session.
	WorkspaceIcon string              `json:"workspaceIcon,omitempty"`
	Status        string              `json:"status"`
	UnreadCount   int                 `json:"unreadCount"`
	ActivityCount int                 `json:"activityCount,omitempty"`
	Notifications []DesktopIconNotice `json:"notifications"`
	Runtime       *DesktopIconRuntime `json:"runtimeStatus,omitempty"`
	Position      DesktopIconPosition `json:"position"`
	Revision      string              `json:"revision"`
	Retained      bool                `json:"retained,omitempty"`
	SessionRef    *DesktopIconTaskRef `json:"sessionRef,omitempty"`
	// Actions is the exact capability-derived surface for external runs and
	// launch profiles. Local task actions continue to use their existing typed
	// Controller path. An absent action must never be manufactured by the UI.
	Actions []string `json:"actions,omitempty"`
	// SourceRevision lets externally-owned projections participate in the icon
	// snapshot revision even when consecutive events keep the same phase.
	SourceRevision uint64 `json:"sourceRevision,omitempty"`
	// ConversationSequence is the unread watermark represented by a Room or
	// personal-conversation icon. It lets remove acknowledge and hide exactly
	// the visible version; a later message has a larger sequence and therefore
	// makes the icon reappear instead of being hidden forever.
	ConversationSequence uint64 `json:"conversationSequence,omitempty"`
}

type DesktopIconSnapshot struct {
	Items              []DesktopIconItem       `json:"items"`
	Delegations        []DesktopIconDelegation `json:"delegations"`
	DelegationError    string                  `json:"delegationError,omitempty"`
	Revision           string                  `json:"revision"`
	HoverStatusDelayMs int                     `json:"hoverStatusDelayMs"`
	Style              string                  `json:"style"`
	UnreadRevision     uint64                  `json:"unreadRevision"`
	Error              string                  `json:"error,omitempty"`
}

// DesktopIconDelegation is one running delegation projected by the backend.
// SessionRef is the exact session the fixed entry must open.
type DesktopIconDelegation struct {
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	Content       string              `json:"content"`
	Status        string              `json:"status"`
	SessionTitle  string              `json:"sessionTitle"`
	WorkspaceName string              `json:"workspaceName,omitempty"`
	UpdatedAt     int64               `json:"updatedAt,omitempty"`
	SessionRef    *DesktopIconTaskRef `json:"sessionRef,omitempty"`
}

type DesktopIconActionInput struct {
	ItemID       string               `json:"itemId"`
	NoticeID     string               `json:"noticeId,omitempty"`
	Revision     string               `json:"revision"`
	RequestID    string               `json:"requestId"`
	Action       string               `json:"action"`
	Values       []string             `json:"values,omitempty"`
	Answers      []QuestionAnswer     `json:"answers,omitempty"`
	Position     *DesktopIconPosition `json:"position,omitempty"`
	Conversation string               `json:"conversation,omitempty"`
	ReadSequence uint64               `json:"readSequence,omitempty"`
}

type DesktopIconActionResult struct {
	Status   string              `json:"status"`
	Error    string              `json:"error,omitempty"`
	Snapshot DesktopIconSnapshot `json:"snapshot"`
}

// DesktopIconSearchItem is an independently indexed navigation target. It is
// deliberately not a DesktopIconItem: visible-icon caps, acknowledgement and
// custom positions must never remove an object from search.
type DesktopIconSearchItem struct {
	ID             string `json:"id"`
	Kind           string `json:"kind"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle,omitempty"`
	SourceID       string `json:"sourceId"`
	LastActivityAt int64  `json:"lastActivityAt,omitempty"`
}

type DesktopIconSearchResult struct {
	Items []DesktopIconSearchItem `json:"items"`
	Error string                  `json:"error,omitempty"`
}

type desktopIconReceipt struct {
	RequestID     string `json:"requestId"`
	Intent        string `json:"intent"`
	Status        string `json:"status"`
	Action        string `json:"action,omitempty"`
	ItemID        string `json:"itemId,omitempty"`
	TabID         string `json:"tabId,omitempty"`
	SessionPath   string `json:"sessionPath,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TargetScope   string `json:"targetScope,omitempty"`
	TargetTopicID string `json:"targetTopicId,omitempty"`
	TargetKind    string `json:"targetKind,omitempty"`
	// Task session identity recorded at apply time so a continuation receipt
	// can reopen the exact session after its tab was closed or pruned. These
	// mirror DesktopIconTaskRef and stay empty for non-task receipts.
	Scope         string `json:"scope,omitempty"`
	TopicID       string `json:"topicId,omitempty"`
	Conversation  string `json:"conversation,omitempty"`
	ReadSequence  uint64 `json:"readSequence,omitempty"`
	Text          string `json:"text,omitempty"`
	ReplyKey      string `json:"replyKey,omitempty"`
	Delivery      string `json:"delivery,omitempty"`
	BaseUserTurns int    `json:"baseUserTurns,omitempty"`
	AppliedAt     int64  `json:"appliedAt"`
}

type desktopIconKept struct {
	ItemID    string `json:"itemId"`
	SourceID  string `json:"sourceId"`
	SessionID string `json:"sessionId,omitempty"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	// CompletionKey reconnects a retained icon to the async summary cache after
	// opening its Session has cleared NeedsAttention. CompletedAt keeps the
	// synthetic completion notice stable across snapshots and restarts.
	CompletionKey string `json:"completionKey,omitempty"`
	CompletedAt   int64  `json:"completedAt,omitempty"`
	Order         int    `json:"order"`
	Revision      string `json:"revision"`
	// Session identity recorded at retain time. A tab can be closed while its
	// kept icon stays visible; these fields let a later open reopen (or reuse)
	// the same session instead of falling back to whatever tab is active.
	Scope         string `json:"scope,omitempty"`
	WorkspaceRoot string `json:"workspaceRoot,omitempty"`
	TopicID       string `json:"topicId,omitempty"`
	SessionPath   string `json:"sessionPath,omitempty"`
}

type desktopIconPersistedState struct {
	Positions map[string]DesktopIconPosition `json:"positions,omitempty"`
	Kept      map[string]desktopIconKept     `json:"kept,omitempty"`
	// AppearanceSeeds is keyed by durable session identity, so live and retained
	// projections render the same explicitly randomized Agent Icon after restart.
	AppearanceSeeds map[string]string `json:"appearanceSeeds,omitempty"`
	// DismissedConversations stores the last explicitly removed conversation
	// sequence by durable conversation key. Snapshot projection suppresses only
	// that version; later messages safely make the icon visible again.
	DismissedConversations map[string]uint64 `json:"dismissedConversations,omitempty"`
	// DismissedExternalRuns stores the last explicitly removed source revision
	// by Run ID. Refresh and restart suppress that revision, while a later
	// authoritative update can project the run again.
	DismissedExternalRuns map[string]uint64    `json:"dismissedExternalRuns,omitempty"`
	Applied               []desktopIconReceipt `json:"applied,omitempty"`
	// WorkspaceSlots is the user-selected number of project shortcuts shown on
	// the desktop. Zero is a valid explicit value; legacy files default to four
	// by being unmarshaled into newDesktopIconState's initialized value.
	WorkspaceSlots int `json:"workspaceSlots"`
	// CompletionSummaries caches LLM news-style summaries keyed by
	// desktopIconCompletionKey; entries survive restarts so already-generated
	// summaries are never regenerated and failed ones retry on backoff.
	CompletionSummaries map[string]desktopIconCompletionSummary `json:"completionSummaries,omitempty"`
}

func newDesktopIconState() desktopIconPersistedState {
	return desktopIconPersistedState{
		Positions:              map[string]DesktopIconPosition{},
		Kept:                   map[string]desktopIconKept{},
		AppearanceSeeds:        map[string]string{},
		DismissedConversations: map[string]uint64{},
		DismissedExternalRuns:  map[string]uint64{},
		WorkspaceSlots:         desktopWorkspacePinLimit,
		CompletionSummaries:    map[string]desktopIconCompletionSummary{},
	}
}

func desktopIconStatePath() string {
	return widgetActionStatePath() + ".icons"
}

func desktopIconWindowStatePath() string { return widgetWindowStatePath() + ".icons" }

func loadDesktopIconWindowState() (WidgetWindowState, bool, error) {
	raw, err := readFileUTF8(desktopIconWindowStatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return WidgetWindowState{}, false, nil
		}
		return WidgetWindowState{}, false, fmt.Errorf("load desktop icon window state: %w", err)
	}
	var state WidgetWindowState
	if err := json.Unmarshal(raw, &state); err != nil {
		return WidgetWindowState{}, false, fmt.Errorf("load desktop icon window state: %w", err)
	}
	// The original icon surface was 900×600. Recompute the enlarged default
	// from the active monitor instead of reusing its old bottom-right position,
	// which could move the wider/taller window outside the work area.
	if state.Width == legacyIconWidth && state.Height == legacyIconHeight {
		return WidgetWindowState{}, false, nil
	}
	if state.Width < desktopIconMinWidth || state.Height < desktopIconMinHeight {
		return WidgetWindowState{}, false, fmt.Errorf("load desktop icon window state: saved size %dx%d is below %dx%d", state.Width, state.Height, desktopIconMinWidth, desktopIconMinHeight)
	}
	return state, true, nil
}

func (a *App) recordDesktopIconWindowError(err error) {
	a.iconWidgetMu.Lock()
	a.iconWidgetWindowErr = err
	a.iconWidgetMu.Unlock()
}

func saveDesktopIconWindowState(state WidgetWindowState) error {
	if state.Width < desktopIconMinWidth || state.Height < desktopIconMinHeight {
		return errors.New("desktop icon window is smaller than its readable minimum")
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(desktopIconWindowStatePath(), raw, 0o644)
}

func (a *App) loadDesktopIconStateLocked() {
	if a.iconWidgetStateLoaded {
		return
	}
	a.iconWidgetStateLoaded = true
	a.iconWidgetState = newDesktopIconState()
	raw, err := readFileUTF8(desktopIconStatePath())
	if err == nil {
		if err := json.Unmarshal(raw, &a.iconWidgetState); err != nil {
			a.iconWidgetStateErr = fmt.Errorf("load desktop icon state: %w", err)
			a.iconWidgetState = newDesktopIconState()
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		a.iconWidgetStateErr = fmt.Errorf("load desktop icon state: %w", err)
	}
	if slots := a.iconWidgetState.WorkspaceSlots; slots < 0 || slots > desktopWorkspacePinLimit {
		a.iconWidgetStateErr = fmt.Errorf("load desktop icon state: workspaceSlots %d is outside 0..%d", slots, desktopWorkspacePinLimit)
		a.iconWidgetState.WorkspaceSlots = desktopWorkspacePinLimit
	}
	if a.iconWidgetState.Positions == nil {
		a.iconWidgetState.Positions = map[string]DesktopIconPosition{}
	}
	if a.iconWidgetState.Kept == nil {
		a.iconWidgetState.Kept = map[string]desktopIconKept{}
	}
	if a.iconWidgetState.AppearanceSeeds == nil {
		a.iconWidgetState.AppearanceSeeds = map[string]string{}
	}
	if a.iconWidgetState.DismissedConversations == nil {
		a.iconWidgetState.DismissedConversations = map[string]uint64{}
	}
	if a.iconWidgetState.DismissedExternalRuns == nil {
		a.iconWidgetState.DismissedExternalRuns = map[string]uint64{}
	}
	if a.iconWidgetState.CompletionSummaries == nil {
		a.iconWidgetState.CompletionSummaries = map[string]desktopIconCompletionSummary{}
	}
}

func (a *App) saveDesktopIconStateLocked() error {
	raw, err := json.Marshal(a.iconWidgetState)
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFile(desktopIconStatePath(), raw, 0o600); err != nil {
		return err
	}
	a.iconWidgetStateErr = nil
	return nil
}

// rememberDesktopIconTask durably retains the task icon for a conversation
// opened from the icon widget. Opening a task (ExitWidgetMode with a tab ID)
// must not change the icon's existence: the icon stays in the widget even
// after the turn stops running and is only removed through an explicit
// dismiss/remove. It is idempotent — an already-retained item keeps its
// original summary — and only applies to regular task tabs (the same
// cli/collaboration filter as buildDesktopIconSnapshot).
func (a *App) rememberDesktopIconTask(tabID string) {
	tabID = strings.TrimSpace(tabID)
	if tabID == "" {
		return
	}
	a.widgetMu.Lock()
	widgetMode := a.widgetMode
	a.widgetMu.Unlock()
	if !widgetMode {
		return
	}
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	a.rememberDesktopIconTaskLocked(tabID)
}

// rememberDesktopIconTaskLocked is the lock-aware implementation used by
// icon actions that already own iconWidgetMu. Keeping this separate prevents
// search navigation and its crash-recovery replay from recursively locking the
// same mutex while they exit widget mode.
func (a *App) rememberDesktopIconTaskLocked(tabID string) {
	var tab *WorkspaceTab
	rank := 0
	a.mu.RLock()
	for i, t := range a.runtimeTabsLocked() {
		if t != nil && t.ID == tabID {
			tab, rank = t, i
			break
		}
	}
	a.mu.RUnlock()
	if tab == nil {
		return
	}
	meta := a.tabMeta(tab, false)
	if strings.EqualFold(meta.SessionSource, "cli") || meta.SessionKind == "collaboration" {
		return
	}
	id := "task:" + meta.ID
	summary, completionKey, completedAt := "", "", int64(0)
	if tab.Ctrl != nil {
		result := lastWidgetAssistantText(tab.Ctrl.History())
		summary, _ = completionSummaryFallback(result)
		if summary != "" && meta.NeedsAttentionAt > 0 {
			completionKey = desktopIconCompletionKey(meta.ID, "completed", meta.NeedsAttentionAt)
			completedAt = meta.NeedsAttentionAt
			summary, _ = desktopIconCompletionSummaryFor(a.iconWidgetState.CompletionSummaries, completionKey, summary)
		}
	}
	entry := desktopIconKept{
		ItemID:        id,
		SourceID:      meta.ID,
		SessionID:     strings.TrimSpace(meta.SessionID),
		Title:         firstNonEmpty(strings.TrimSpace(meta.SessionDisplayTitle), strings.TrimSpace(meta.TopicTitle), "当前任务"),
		Summary:       summary,
		CompletionKey: completionKey,
		CompletedAt:   completedAt,
		Order:         rank,
		Scope:         meta.Scope,
		WorkspaceRoot: meta.WorkspaceRoot,
		TopicID:       meta.TopicID,
		SessionPath:   strings.TrimSpace(meta.SessionPath),
	}
	if existing, exists := a.iconWidgetState.Kept[id]; exists {
		existing.SourceID = entry.SourceID
		existing.SessionID = entry.SessionID
		existing.Scope = entry.Scope
		existing.WorkspaceRoot = entry.WorkspaceRoot
		existing.TopicID = entry.TopicID
		existing.SessionPath = entry.SessionPath
		if entry.CompletionKey != "" && entry.CompletionKey != existing.CompletionKey {
			existing.Title = entry.Title
			existing.Summary = entry.Summary
			existing.CompletionKey = entry.CompletionKey
			existing.CompletedAt = entry.CompletedAt
		}
		a.iconWidgetState.Kept[id] = existing
		if err := a.saveDesktopIconStateLocked(); err != nil {
			slog.Error("desktop: refresh retained task icon", "tabID", tabID, "err", err)
		}
		return
	}
	// Reopening a closed task must not accumulate duplicate kept icons for the
	// same session: refresh the existing entry's tab identity instead. A tab can
	// be recreated with a new ID after being closed, so matching by session path
	// (the durable identity) keeps the icon pointing at the live tab.
	for existingID, existing := range a.iconWidgetState.Kept {
		if existing.SessionPath == "" {
			continue
		}
		if sessionRuntimeKey(existing.SessionPath) == sessionRuntimeKey(entry.SessionPath) {
			existing.SourceID = entry.SourceID
			existing.SessionID = entry.SessionID
			existing.Scope = entry.Scope
			existing.WorkspaceRoot = entry.WorkspaceRoot
			existing.TopicID = entry.TopicID
			existing.SessionPath = entry.SessionPath
			if entry.CompletionKey != "" && entry.CompletionKey != existing.CompletionKey {
				existing.Title = entry.Title
				existing.Summary = entry.Summary
				existing.CompletionKey = entry.CompletionKey
				existing.CompletedAt = entry.CompletedAt
			}
			a.iconWidgetState.Kept[existingID] = existing
			if err := a.saveDesktopIconStateLocked(); err != nil {
				slog.Error("desktop: refresh retained task icon", "tabID", tabID, "err", err)
			}
			return
		}
	}
	a.iconWidgetState.Kept[id] = entry
	if err := a.saveDesktopIconStateLocked(); err != nil {
		slog.Error("desktop: retain task icon", "tabID", tabID, "err", err)
	}
}

type desktopIconSessionRef struct {
	sessionID   string
	tabID       string
	sessionPath string
}

func (a *App) activeDesktopIconSessionRef() desktopIconSessionRef {
	a.mu.RLock()
	defer a.mu.RUnlock()
	tab := a.tabs[a.activeTabID]
	if tab == nil {
		return desktopIconSessionRef{}
	}
	return desktopIconSessionRef{
		sessionID:   strings.TrimSpace(tab.SessionID),
		tabID:       tab.ID,
		sessionPath: strings.TrimSpace(tab.currentSessionPath()),
	}
}

// removeActiveSessionDesktopIcon removes only a currently visible retained
// task icon for the active session. Missing icons are an idempotent no-op. The
// stable SessionID is authoritative; tab/path matching is limited to legacy
// kept entries written before SessionID was persisted.
func (a *App) removeActiveSessionDesktopIcon() (bool, error) {
	ref := a.activeDesktopIconSessionRef()
	if ref.sessionID == "" {
		return false, nil
	}
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()

	visible := map[string]bool{}
	for _, item := range a.desktopIconSnapshotLocked().Items {
		if item.Kind == "task" {
			visible[item.ID] = true
			visible["task:"+item.SourceID] = true
		}
	}
	before := cloneDesktopIconState(a.iconWidgetState)
	removed := false
	for key, kept := range a.iconWidgetState.Kept {
		if !visible[key] && !visible[kept.ItemID] && !visible["task:"+kept.SourceID] {
			continue
		}
		match := strings.TrimSpace(kept.SessionID) == ref.sessionID
		if kept.SessionID == "" {
			match = kept.SourceID == ref.tabID ||
				(ref.sessionPath != "" && sessionRuntimeKey(kept.SessionPath) == sessionRuntimeKey(ref.sessionPath))
		}
		if !match {
			continue
		}
		delete(a.iconWidgetState.Kept, key)
		removed = true
	}
	if !removed {
		return false, nil
	}
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState = before
		return false, fmt.Errorf("remove active session icon: %w", err)
	}
	return true, nil
}

// applyDesktopIconKeptRenameLocked projects a successful Session rename onto
// every retained task icon whose durable session identity matches, so the
// retained title stays consistent with the Session titles sidecar. The sidecar
// remains the single source of truth; Kept.Title is only a recoverable
// projection. Callers hold iconWidgetMu.
func (a *App) applyDesktopIconKeptRenameLocked(sessionPath, title string) bool {
	sessionPath = strings.TrimSpace(sessionPath)
	title = strings.TrimSpace(title)
	if sessionPath == "" || title == "" {
		return false
	}
	key := sessionRuntimeKey(sessionPath)
	changed := false
	for id, kept := range a.iconWidgetState.Kept {
		keptPath := strings.TrimSpace(kept.SessionPath)
		if keptPath == "" || sessionRuntimeKey(keptPath) != key || kept.Title == title {
			continue
		}
		kept.Title = title
		a.iconWidgetState.Kept[id] = kept
		changed = true
	}
	return changed
}

// resolveDesktopIconTaskTab maps a task icon back to a live tab through its
// snapshot session ref. Every task icon — live or retained — carries the same
// typed identity (scope/workspaceRoot/topicID/sessionPath) generated by the
// backend snapshot, so the open never consults SourceID/tabID, never falls
// back to whatever tab happens to be active, and reopens the exact session
// even after its tab was closed or its SourceID went stale. OpenTopicSession
// reuses an existing tab for the same session path and returns its actual
// meta.ID. Missing identity is an explicit failure. Callers hold iconWidgetMu.
func (a *App) resolveDesktopIconTaskTab(item DesktopIconItem) (string, error) {
	ref := item.SessionRef
	if ref == nil {
		return "", errors.New("task session identity is unavailable")
	}
	sessionPath := strings.TrimSpace(ref.SessionPath)
	if sessionPath == "" {
		return "", errors.New("task session has no recorded identity; reopen it from the session list")
	}
	meta, err := a.OpenTopicSession(ref.Scope, ref.WorkspaceRoot, ref.TopicID, sessionPath)
	if err != nil {
		return "", fmt.Errorf("open task session: %w", err)
	}
	return meta.ID, nil
}

func (a *App) resolveDesktopIconDelegation(item DesktopIconDelegation) (string, error) {
	ref := item.SessionRef
	if ref == nil || strings.TrimSpace(ref.SessionPath) == "" {
		return "", errors.New("delegation session identity is unavailable; refresh and retry")
	}
	var meta TabMeta
	var err error
	if item.Kind == "background" || item.Kind == "cli" {
		meta, err = a.OpenLinkedSession(ref.Scope, ref.WorkspaceRoot, ref.TopicID, ref.SessionPath)
	} else {
		meta, err = a.OpenTopicSession(ref.Scope, ref.WorkspaceRoot, ref.TopicID, ref.SessionPath)
	}
	if err != nil {
		return "", fmt.Errorf("open delegation session: %w", err)
	}
	return meta.ID, nil
}

func (a *App) advanceDesktopIconDelegation(receipt *desktopIconReceipt) error {
	ref := DesktopIconTaskRef{Scope: receipt.TargetScope, WorkspaceRoot: receipt.WorkspaceRoot, TopicID: receipt.TargetTopicID, SessionPath: receipt.SessionPath}
	tabID, err := a.resolveDesktopIconDelegation(DesktopIconDelegation{Kind: receipt.TargetKind, SessionRef: &ref})
	if err != nil {
		return err
	}
	receipt.TabID = tabID
	if receipt.TargetKind == "background" || receipt.TargetKind == "cli" {
		return a.exitWidgetMode(tabID)
	}
	return a.exitDesktopIconModeLocked(tabID)
}

// resolveDesktopIconRoomTab maps a Room icon back to its live tab through the
// snapshot session ref, exactly like resolveDesktopIconTaskTab. The ref is
// generated by the backend snapshot from the persisted project tree or live
// collaboration runtime, so the open never consults the first notice's TabID,
// never falls back to the active tab, and reopens the exact Room session even
// after restart, detach or tab close. OpenTopicSession reuses an existing tab
// for the same session path and returns its actual meta.ID; a missing identity
// fails explicitly and leaves the unread intact for a later retry. Callers
// hold iconWidgetMu.
func (a *App) resolveDesktopIconRoomTab(item DesktopIconItem) (string, error) {
	ref := item.SessionRef
	if ref == nil {
		return "", errors.New("Room session identity is unavailable")
	}
	sessionPath := strings.TrimSpace(ref.SessionPath)
	if sessionPath == "" {
		return "", errors.New("Room session has no recorded identity; reopen it from the Rooms list")
	}
	meta, err := a.OpenTopicSession(ref.Scope, ref.WorkspaceRoot, ref.TopicID, sessionPath)
	if err != nil {
		return "", fmt.Errorf("open Room session: %w", err)
	}
	return meta.ID, nil
}

// exitDesktopIconModeLocked is the single exit path for actions that already
// own iconWidgetMu. It retains task identity without recursively acquiring the
// mutex, then performs the native mode transition through the lock-free
// internal exit implementation.
func (a *App) exitDesktopIconModeLocked(tabID string) error {
	tabID = strings.TrimSpace(tabID)
	if tabID != "" {
		a.rememberDesktopIconTaskLocked(tabID)
	}
	return a.exitWidgetMode(tabID)
}

// activateDesktopIconWorkspace resolves a workspace icon to the tab that must
// become visible in the main window. Project icons use the same idempotent
// workspace switch as the sidebar. Global reuses an existing global tab when
// possible and creates one only when none exists.
func (a *App) activateDesktopIconWorkspace(sourceID string) (string, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" {
		return "", errors.New("workspace icon has no target")
	}
	if sourceID == widgetWorkspaceGlobal {
		a.mu.RLock()
		tabID := ""
		if active := a.tabs[a.activeTabID]; active != nil && active.Scope == "global" {
			tabID = active.ID
		}
		if tabID == "" {
			ordered, _ := a.orderedTabIDsSnapshotLocked()
			for _, id := range ordered {
				if tab := a.tabs[id]; tab != nil && tab.Scope == "global" {
					tabID = id
					break
				}
			}
		}
		a.mu.RUnlock()
		if tabID != "" {
			if a.singleSurfaceLayoutEnabled() {
				meta, err := a.keepOnlyVisibleTab(tabID)
				if err != nil {
					return "", err
				}
				return meta.ID, nil
			}
			if err := a.SetActiveTab(tabID); err != nil {
				return "", err
			}
			return tabID, nil
		}
		if err := a.openTransientBlankRuntime("global", ""); err != nil {
			return "", err
		}
	} else if _, err := a.SwitchWorkspace(sourceID); err != nil {
		return "", fmt.Errorf("switch workspace %q: %w", sourceID, err)
	}

	a.mu.RLock()
	tabID := a.activeTabID
	tab := a.tabs[tabID]
	activeScope, activeRoot := "", ""
	if tab != nil {
		activeScope, activeRoot = tab.Scope, tab.WorkspaceRoot
	}
	a.mu.RUnlock()
	if tab == nil {
		return "", errors.New("selected workspace did not produce an active session")
	}
	if sourceID == widgetWorkspaceGlobal {
		if activeScope != "global" {
			return "", errors.New("selected Global workspace activated a non-global session")
		}
	} else if normalizeProjectRoot(activeRoot) != normalizeProjectRoot(sourceID) {
		return "", fmt.Errorf("selected workspace %q activated %q", sourceID, activeRoot)
	}
	return tabID, nil
}

// GetDesktopIconSnapshot projects existing Controller, unread and workspace
// state. It never consumes events and is therefore safe to poll and retry.
func (a *App) GetDesktopIconSnapshot() DesktopIconSnapshot {
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	return a.desktopIconSnapshotLocked()
}

// GetDesktopWorkspaceSlots returns the persisted number of project shortcuts
// shown on the desktop. It is safe to poll and defaults legacy state to four.
func (a *App) GetDesktopWorkspaceSlots() int {
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	return a.iconWidgetState.WorkspaceSlots
}

// SetDesktopWorkspaceSlots updates the desktop project shortcut capacity.
// Repeating the same value is a no-op; a failed save restores the prior value
// so frontend retries cannot leave runtime and persisted state divergent.
func (a *App) SetDesktopWorkspaceSlots(slots int) error {
	if slots < 0 || slots > desktopWorkspacePinLimit {
		return fmt.Errorf("desktop workspace slots must be between 0 and %d", desktopWorkspacePinLimit)
	}
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	if a.iconWidgetState.WorkspaceSlots == slots && a.iconWidgetStateErr == nil {
		return nil
	}
	previous := a.iconWidgetState.WorkspaceSlots
	a.iconWidgetState.WorkspaceSlots = slots
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState.WorkspaceSlots = previous
		return fmt.Errorf("save desktop workspace slots: %w", err)
	}
	return nil
}

func (a *App) desktopIconSnapshotLocked() DesktopIconSnapshot {
	recoveryErr := a.recoverDesktopIconActionsLocked()
	sources := a.widgetSources()
	// Completion summaries are generated asynchronously: only the request
	// collection and goroutine start happen under iconWidgetMu, and the
	// network calls run inside the goroutines, never under an App/widget lock.
	a.maybeGenerateCompletionSummariesLocked(a.completionSummaryRequestsLocked(sources))
	// Subagent metadata scanning happens after widgetSources released a.mu,
	// so file I/O never runs under the App's main lock.
	delegations, subagentCounts, subagentErr := a.widgetDelegations(sources)
	unreadState := a.UnreadState()
	projectTree := a.ListProjectTree()
	spaces := desktopIconWorkspaces(projectTree, desktopIconActiveWorkspace(sources), a.iconWidgetState.WorkspaceSlots)
	style, hover, showDelegation, showExternalTools := a.desktopIconPreferences()
	roomPresentations := a.desktopRoomNoticePresentations()
	roomRefs := a.desktopIconRoomRefs(projectTree)
	roomPins, roomPinsErr := a.GetDesktopRoomPins()
	roomIcons, roomIconsErr := a.GetDesktopRoomIcons()
	roomDescriptors := desktopIconRoomDescriptors(projectTree, roomIcons)
	// Resident runtimes are the strongest liveness signal: a Room still
	// consuming its stream offscreen must keep its unread visible even when the
	// project-tree scan is cold or the tab is not mounted. Tree descriptors
	// stay authoritative when both exist.
	roomDescriptors = a.mergeResidentRoomDescriptors(roomDescriptors)
	// Pinned Rooms must be derived after the resident merge so a runtime's live
	// SessionID alias is present on the fixed entry and the unread merge
	// resolves the same Room instead of emitting a second default bubble.
	pinnedRooms := desktopIconPinnedRoomsFromDescriptors(roomDescriptors, roomPins)
	sessionPresentations := desktopIconSessionPresentations(sources, projectTree)
	snapshot := buildDesktopIconSnapshotWithPresentations(sources, unreadState, spaces, a.iconWidgetState, hover, roomPresentations, roomRefs, subagentCounts, sessionPresentations, pinnedRooms, delegations, roomDescriptors)
	external := a.GetExternalRunSnapshot()
	appendExternalRunIcons(&snapshot, external, a.iconWidgetState.Positions, a.iconWidgetState.DismissedExternalRuns)
	if a.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		// The snapshot just pinned brand-new task icons, so rebuild once: the
		// current response must already reflect the pinned (stable) orders,
		// otherwise the very first render would still use the ephemeral ones.
		snapshot = buildDesktopIconSnapshotWithPresentations(sources, unreadState, spaces, a.iconWidgetState, hover, roomPresentations, roomRefs, subagentCounts, sessionPresentations, pinnedRooms, delegations, roomDescriptors)
		appendExternalRunIcons(&snapshot, external, a.iconWidgetState.Positions, a.iconWidgetState.DismissedExternalRuns)
	}
	filterDesktopIconVisibility(&snapshot, showDelegation, showExternalTools)
	snapshot.Style = style
	if recoveryErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, recoveryErr.Error())
	}
	if subagentErr != nil {
		snapshot.DelegationError = subagentErr.Error()
		snapshot.Error = firstNonEmpty(snapshot.Error, subagentErr.Error())
		snapshot.Revision = widgetRevision(snapshot.Revision, snapshot.DelegationError)
	}
	if a.iconWidgetStateErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, a.iconWidgetStateErr.Error())
	}
	if a.iconWidgetWindowErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, a.iconWidgetWindowErr.Error())
	}
	if external.Error != "" {
		snapshot.Error = firstNonEmpty(snapshot.Error, external.Error)
	}
	if roomPinsErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, roomPinsErr.Error())
	}
	if roomIconsErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, roomIconsErr.Error())
	}
	return snapshot
}

func appendExternalRunIcons(snapshot *DesktopIconSnapshot, external ExternalRunSnapshot, positions map[string]DesktopIconPosition, dismissed map[string]uint64) {
	runningOrder, fixedOrder := 0, 0
	for _, item := range snapshot.Items {
		if item.Position.Zone == "running" {
			runningOrder = max(runningOrder, item.Position.Order+1)
		}
		if item.Position.Zone == "fixed" {
			fixedOrder = max(fixedOrder, item.Position.Order+1)
		}
	}
	profileStatus := "idle"
	profileSubtitle := "DSH rc.8 未就绪"
	profileActions := []string(nil)
	if external.DSH.Ready {
		profileSubtitle = firstNonEmpty(external.DSH.Version, "DSH rc.8") + " · 快速启动"
		profileActions = []string{"launch"}
	} else if external.DSH.Error != "" || len(external.DSH.Missing) > 0 {
		profileStatus = "failed"
		profileSubtitle = firstNonEmpty(external.DSH.Error, dshIssues(external.DSH.Missing))
	}
	launcher := DesktopIconItem{
		ID: "fixed:dsh", Kind: "fixed", SourceID: "dsh", Title: "DSH", Icon: "terminal",
		Subtitle: profileSubtitle, Status: profileStatus, Notifications: []DesktopIconNotice{}, Actions: profileActions,
		Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: fixedOrder},
	}
	launcher.Revision = desktopIconItemRevision(launcher)
	snapshot.Items = append(snapshot.Items, launcher)

	terminal := 0
	for _, projection := range external.Runs {
		if revision, ok := dismissed[string(projection.ID)]; ok && revision >= projection.Revision {
			continue
		}
		if projection.State.IsTerminal() {
			if terminal >= 3 {
				continue
			}
			terminal++
		}
		item := externalRunIcon(projection, runningOrder)
		runningOrder++
		if position, ok := positions[item.ID]; ok && validDesktopIconPosition(item, position) {
			item.Position = position
		}
		item.Revision = desktopIconItemRevision(item)
		snapshot.Items = append(snapshot.Items, item)
	}
	sort.SliceStable(snapshot.Items, func(i, j int) bool {
		left, right := snapshot.Items[i].Position, snapshot.Items[j].Position
		if left.Row != right.Row {
			return left.Row < right.Row
		}
		if left.Zone != right.Zone {
			return desktopIconZoneRank(left.Zone) < desktopIconZoneRank(right.Zone)
		}
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return snapshot.Items[i].ID < snapshot.Items[j].ID
	})
	snapshot.Revision = desktopIconSnapshotRevision(*snapshot)
}

// filterDesktopIconVisibility removes the 委托 and external AI tool (DSH) icons
// from the projection when the matching widget settings are off. It only hides
// the icon entries: running delegations and external tasks keep running and their
// state stays intact. Both switches are independent, and the snapshot revision
// is recomputed so the frontend sees the change on the next poll.
func filterDesktopIconVisibility(snapshot *DesktopIconSnapshot, showDelegation, showExternalTools bool) {
	if snapshot == nil || (showDelegation && showExternalTools) {
		return
	}
	items := snapshot.Items[:0]
	for _, item := range snapshot.Items {
		if !showDelegation && item.ID == "fixed:delegate" {
			continue
		}
		if !showExternalTools && (item.ID == "fixed:dsh" || item.Kind == "external") {
			continue
		}
		items = append(items, item)
	}
	snapshot.Items = items
	snapshot.Revision = desktopIconSnapshotRevision(*snapshot)
}

func externalRunIcon(run runhub.RunProjection, order int) DesktopIconItem {
	status := "idle"
	switch run.State {
	case runhub.StateStarting, runhub.StateRunning:
		status = "running"
		if run.Activity == runhub.ActivityThinking || run.Activity == runhub.ActivityResponding {
			status = "thinking"
		}
	case runhub.StateWaitingUser:
		status = "needs_input"
	case runhub.StateSucceeded, runhub.StateCancelled:
		status = "done"
	case runhub.StateFailed, runhub.StateInterrupted, runhub.StateStale:
		status = "failed"
	}
	item := DesktopIconItem{
		ID: "external:" + string(run.ID), Kind: "external", SourceID: string(run.ID),
		Title:    firstNonEmpty(strings.TrimSpace(run.Title), strings.ToUpper(string(run.Source))),
		Subtitle: filepath.Base(run.Workspace), Icon: "terminal", Status: status,
		Notifications: []DesktopIconNotice{}, Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: order},
		SourceRevision: run.Revision,
	}
	if run.Capabilities.Cancel && !run.State.IsTerminal() {
		item.Actions = append(item.Actions, "cancel")
	}
	if run.State.IsTerminal() {
		item.Actions = append(item.Actions, "remove")
	}
	if !run.State.IsTerminal() {
		item.Runtime = &DesktopIconRuntime{
			Phase:     firstNonEmpty(string(run.Activity), string(run.State)),
			Summary:   firstNonEmpty(strings.TrimSpace(run.ActivityLabel), strings.TrimSpace(run.Summary), "DSH 正在执行"),
			ElapsedMs: max(int64(0), time.Since(run.CreatedAt).Milliseconds()), UpdatedAt: run.UpdatedAt.UnixMilli(),
		}
	} else if strings.TrimSpace(run.Summary) != "" {
		item.Subtitle = conciseWidgetText(run.Summary, 80)
	}
	return item
}

// pinNewDesktopIconTaskOrdersLocked durably pins every newly visible live task
// at the left edge of the running zone. Existing positions (including retained
// and capacity-capped icons) are densely shifted right without changing their
// relative order. Multiple tasks discovered in one snapshot are newest-first,
// based on their ephemeral tab order. The write is idempotent; a failed save
// restores the full prior state so the next snapshot can safely retry.
func (a *App) pinNewDesktopIconTaskOrdersLocked(snapshot DesktopIconSnapshot) bool {
	newItems := make([]DesktopIconItem, 0)
	for _, item := range snapshot.Items {
		if item.Kind != "task" || item.Retained || item.Position.Row != "bottom" || item.Position.Zone != "running" {
			continue
		}
		if _, pinned := a.iconWidgetState.Positions[item.ID]; !pinned {
			newItems = append(newItems, item)
		}
	}
	if len(newItems) == 0 {
		return false
	}
	sort.SliceStable(newItems, func(i, j int) bool {
		if newItems[i].Position.Order != newItems[j].Position.Order {
			return newItems[i].Position.Order > newItems[j].Position.Order
		}
		return newItems[i].ID > newItems[j].ID
	})

	type orderedTask struct {
		id       string
		order    int
		retained bool
	}
	existing := make([]orderedTask, 0, len(a.iconWidgetState.Positions)+len(a.iconWidgetState.Kept))
	seen := make(map[string]struct{}, len(a.iconWidgetState.Positions)+len(a.iconWidgetState.Kept))
	for id, position := range a.iconWidgetState.Positions {
		if position.Row != "bottom" || position.Zone != "running" {
			continue
		}
		existing = append(existing, orderedTask{id: id, order: position.Order})
		seen[id] = struct{}{}
	}
	for id, kept := range a.iconWidgetState.Kept {
		if _, ok := seen[id]; ok {
			continue
		}
		existing = append(existing, orderedTask{id: id, order: kept.Order, retained: true})
	}
	sort.SliceStable(existing, func(i, j int) bool {
		if existing[i].order != existing[j].order {
			return existing[i].order < existing[j].order
		}
		return existing[i].id < existing[j].id
	})

	before := cloneDesktopIconState(a.iconWidgetState)
	for order, item := range newItems {
		a.iconWidgetState.Positions[item.ID] = DesktopIconPosition{Row: "bottom", Zone: "running", Order: order}
	}
	for index, item := range existing {
		order := len(newItems) + index
		if item.retained {
			kept := a.iconWidgetState.Kept[item.id]
			kept.Order = order
			a.iconWidgetState.Kept[item.id] = kept
			continue
		}
		a.iconWidgetState.Positions[item.id] = DesktopIconPosition{Row: "bottom", Zone: "running", Order: order}
	}
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState = before
		a.iconWidgetStateErr = fmt.Errorf("pin desktop icon order: %w", err)
		return false
	}
	return true
}

type desktopRoomNoticePresentation struct {
	Author string
	Body   string
}

type desktopRoomNoticePresentations map[string]map[string]desktopRoomNoticePresentation

func addDesktopRoomNoticePresentation(out desktopRoomNoticePresentations, sessionID, itemID string, value desktopRoomNoticePresentation) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || strings.TrimSpace(value.Body) == "" {
		return
	}
	if out[sessionID] == nil {
		out[sessionID] = map[string]desktopRoomNoticePresentation{}
	}
	out[sessionID][strings.TrimSpace(itemID)] = value
}

// desktopRoomNoticePresentations reads display text from the authoritative
// Room snapshot. Presentations are keyed by Session and timeline item ID so
// multiple pending messages never reuse whichever chat happened to be latest.
// The empty item ID remains a compatibility fallback for non-Room conversation
// sources whose complete payload lives in their bound Session history.
func (a *App) desktopRoomNoticePresentations() desktopRoomNoticePresentations {
	out := desktopRoomNoticePresentations{}
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || tab.Ctrl == nil {
			continue
		}
		text := ""
		history := tab.Ctrl.History()
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == provider.RoleUser && strings.TrimSpace(history[i].Content) != "" {
				text = conciseWidgetText(history[i].Content, 100)
				break
			}
		}
		if text == "" {
			continue
		}
		for _, key := range []string{tab.ID, tab.SessionID, tab.currentSessionPath(), "path:" + tab.currentSessionPath()} {
			addDesktopRoomNoticePresentation(out, key, "", desktopRoomNoticePresentation{Body: text})
		}
	}
	a.mu.RUnlock()
	a.collaborationMu.Lock()
	runtimes := make([]*desktopCollaboration, 0, len(a.collaborations))
	for _, runtime := range a.collaborations {
		runtimes = append(runtimes, runtime)
	}
	a.collaborationMu.Unlock()
	for _, runtime := range runtimes {
		runtime.mu.RLock()
		sessionID := firstNonEmpty(runtime.ownerSessionID, runtime.state.SessionID)
		sessionKeys := desktopIconRoomSessionKeys(sessionID, &DesktopIconTaskRef{SessionPath: runtime.ownerSessionPath})
		authors := map[string]string{}
		for _, member := range runtime.state.Snapshot.Members {
			authors[member.ID] = firstNonEmpty(strings.TrimSpace(member.Name), strings.TrimSpace(member.ID))
			if agentID := strings.TrimSpace(member.Agent.ID); agentID != "" {
				authors[agentID] = firstNonEmpty(strings.TrimSpace(member.Agent.Name), agentID)
			}
		}
		for _, item := range runtime.state.Snapshot.Timeline {
			presentation := desktopRoomTimelinePresentation(item, authors)
			if strings.TrimSpace(presentation.Body) != "" {
				for _, key := range sessionKeys {
					addDesktopRoomNoticePresentation(out, key, desktopRoomTimelineItemID(item), presentation)
				}
			}
		}
		runtime.mu.RUnlock()
	}
	return out
}

func desktopRoomTimelineItemID(item collab.TimelineItem) string {
	if id := strings.TrimSpace(item.ID); id != "" {
		return id
	}
	return string(item.Type) + ":" + strconv.FormatUint(item.Sequence, 10)
}

func desktopRoomTimelinePresentation(item collab.TimelineItem, authors map[string]string) desktopRoomNoticePresentation {
	author := ""
	body := ""
	switch item.Type {
	case collab.TimelineChat:
		if item.Chat != nil {
			author, body = item.Chat.AuthorID, item.Chat.Text
		}
	case collab.TimelineContribution:
		if item.Contribution != nil {
			author = item.Contribution.AuthorID
			body = firstNonEmpty(strings.TrimSpace(item.Contribution.Body), strings.TrimSpace(item.Contribution.Title))
		}
	case collab.TimelineAgentRequest:
		if item.AgentRequest != nil {
			author, body = item.AgentRequest.AuthorID, item.AgentRequest.Instruction
		}
	case collab.TimelineAgentResult:
		if item.AgentResult != nil {
			author, body = item.AgentResult.OwnerID, item.AgentResult.Summary
		}
	case collab.TimelineFile:
		if item.File != nil {
			author, body = item.File.OwnerID, item.File.Name
		}
	}
	author = firstNonEmpty(strings.TrimSpace(authors[author]), strings.TrimSpace(author))
	return desktopRoomNoticePresentation{Author: author, Body: strings.TrimSpace(body)}
}

// desktopIconRoomRefs maps every persisted or live collaboration session ID to
// the durable identity of the Room's own session. Persisted project-tree nodes
// keep historical Rooms openable after a restart; live runtimes fill the short
// cache window before a newly-created Room reaches that tree.
func (a *App) desktopIconRoomRefs(tree []ProjectNode) map[string]*DesktopIconTaskRef {
	out := map[string]*DesktopIconTaskRef{}
	if a == nil {
		return out
	}
	var addTreeNodes func([]ProjectNode)
	addTreeNodes = func(nodes []ProjectNode) {
		for _, node := range nodes {
			if node.SessionKind == string(agent.SessionKindCollaboration) {
				sessionPath := strings.TrimSpace(node.SessionPath)
				if ref, meta := desktopIconRoomRef(sessionPath); ref != nil {
					sessionID := firstNonEmpty(strings.TrimSpace(node.SessionID), strings.TrimSpace(meta.ID))
					if sessionID != "" {
						out[sessionID] = ref
					}
				}
			}
			addTreeNodes(node.Children)
		}
	}
	addTreeNodes(tree)

	a.collaborationMu.Lock()
	runtimes := make([]*desktopCollaboration, 0, len(a.collaborations))
	for _, runtime := range a.collaborations {
		runtimes = append(runtimes, runtime)
	}
	a.collaborationMu.Unlock()
	for _, runtime := range runtimes {
		runtime.mu.RLock()
		sessionID := firstNonEmpty(strings.TrimSpace(runtime.ownerSessionID), strings.TrimSpace(runtime.state.SessionID))
		sessionPath := strings.TrimSpace(runtime.ownerSessionPath)
		runtime.mu.RUnlock()
		if sessionID == "" {
			continue
		}
		if sessionPath == "" {
			sessionPath = a.runtimeSessionPath(sessionID)
		}
		if ref, _ := desktopIconRoomRef(sessionPath); ref != nil {
			out[sessionID] = ref
		}
	}
	return out
}

type desktopIconRoomDescriptor struct {
	TopicID   string
	Title     string
	SessionID string
	Icon      string
	Ref       *DesktopIconTaskRef
	// SessionAliases are additional live session identities that resolve to
	// this same Room (the resident runtime SessionID when it differs from the
	// durable tree SessionID). They only widen the identity lookup for pinned/
	// unread merging; they never replace the display identity or dedupe by title.
	SessionAliases []string
}

// desktopIconPinnedRooms joins the durable pin order onto authoritative Room
// topic nodes. Missing/deleted topics are skipped without mutating the pin
// file, allowing a later restore or project reappearance to recover the pin.
func desktopIconPinnedRooms(tree []ProjectNode, topicIDs []string) []desktopIconRoomDescriptor {
	return desktopIconPinnedRoomsFromDescriptors(desktopIconRoomDescriptors(tree, nil), topicIDs)
}

func desktopIconRoomDescriptors(tree []ProjectNode, icons map[string]string) map[string]desktopIconRoomDescriptor {
	byTopic := map[string]desktopIconRoomDescriptor{}
	var walk func([]ProjectNode)
	walk = func(nodes []ProjectNode) {
		for _, node := range nodes {
			kind := strings.TrimSpace(node.Kind)
			topicID := strings.TrimSpace(node.TopicID)
			sessionPath := strings.TrimSpace(node.SessionPath)
			if (kind == "topic" || kind == "global_topic") && topicID != "" && sessionPath != "" && node.SessionKind == string(agent.SessionKindCollaboration) {
				if _, exists := byTopic[topicID]; !exists {
					ref, meta := desktopIconRoomRef(sessionPath)
					byTopic[topicID] = desktopIconRoomDescriptor{
						TopicID:   topicID,
						Title:     firstNonEmpty(strings.TrimSpace(node.Label), strings.TrimSpace(meta.CustomTitle), "Room"),
						SessionID: firstNonEmpty(strings.TrimSpace(node.SessionID), strings.TrimSpace(meta.ID)),
						Icon:      normalizeProjectIcon(icons[topicID]),
						Ref:       ref,
					}
				}
			}
			walk(node.Children)
		}
	}
	walk(tree)
	return byTopic
}

// mergeResidentRoomDescriptors folds active collaboration runtimes into the
// widget Room projection when the project tree does not yet carry their
// session identity. A resident runtime is the strongest liveness signal: it is
// consuming the Room's stream in the background even while the Room tab is not
// mounted and the project-tree scan is cold or incomplete. Without this, a
// Room that received a remote message offscreen stays invisible on the desktop
// until the user opens it (which mounts the tab and backfills the tree node).
//
// The merge is deliberately conservative:
//   - Only runtimes that still hold a Room identity participate; leave/close
//     clears Room and Snapshot, so left rooms cannot be resurrected here.
//   - Runtimes whose session identity already resolves to an existing tree
//     descriptor attach their live SessionID as an identity alias on that
//     descriptor instead of creating a second Room. The project-tree descriptor
//     stays authoritative for title/icon/ref.
func (a *App) mergeResidentRoomDescriptors(roomDescriptors map[string]desktopIconRoomDescriptor) map[string]desktopIconRoomDescriptor {
	if a == nil {
		return roomDescriptors
	}
	a.collaborationMu.Lock()
	runtimes := make([]*desktopCollaboration, 0, len(a.collaborations))
	for _, runtime := range a.collaborations {
		runtimes = append(runtimes, runtime)
	}
	a.collaborationMu.Unlock()
	if len(runtimes) == 0 {
		return roomDescriptors
	}
	out := roomDescriptors
	if out == nil {
		out = map[string]desktopIconRoomDescriptor{}
	}
	// Index every existing descriptor by all of its identity keys, so a
	// resident runtime can find the descriptor it belongs to even when it only
	// shares the session path and not the tree SessionID.
	descriptorByKey := map[string]string{}
	for topicID, room := range out {
		for _, key := range desktopIconRoomDescriptorKeys(room) {
			if _, exists := descriptorByKey[key]; !exists {
				descriptorByKey[key] = topicID
			}
		}
	}
	for _, runtime := range runtimes {
		runtime.mu.RLock()
		sessionID := firstNonEmpty(strings.TrimSpace(runtime.ownerSessionID), strings.TrimSpace(runtime.state.SessionID))
		sessionPath := strings.TrimSpace(runtime.ownerSessionPath)
		room := strings.TrimSpace(runtime.state.Room)
		if room == "" {
			room = strings.TrimSpace(runtime.state.Snapshot.Room.ID)
		}
		roomName := strings.TrimSpace(runtime.state.Snapshot.Room.Name)
		runtime.mu.RUnlock()
		if sessionID == "" || room == "" {
			continue
		}
		ref, meta := desktopIconRoomRef(sessionPath)
		// A resident runtime and the project tree may share one stable TopicID
		// while living on different session paths (the main session versus the
		// recovery fork the tree selected). Prefer that durable TopicID so the
		// runtime unread lands on the existing tree descriptor; SessionID/path
		// stay the fallback when the sidecar is unreadable.
		if topicID := strings.TrimSpace(meta.TopicID); topicID != "" {
			if existing, ok := out[topicID]; ok {
				if !slices.Contains(existing.SessionAliases, sessionID) {
					existing.SessionAliases = append(existing.SessionAliases, sessionID)
					out[topicID] = existing
				}
				continue
			}
		}
		if topicID, ok := desktopIconRoomDescriptorForSession(descriptorByKey, sessionID, sessionPath); ok {
			existing := out[topicID]
			if !slices.Contains(existing.SessionAliases, sessionID) {
				existing.SessionAliases = append(existing.SessionAliases, sessionID)
				out[topicID] = existing
			}
			continue
		}
		title := firstNonEmpty(roomName, strings.TrimSpace(meta.CustomTitle), strings.TrimSpace(meta.TopicTitle), sessionID)
		// The descriptor map is keyed by TopicID for project-tree dedupe;
		// resident entries use a synthetic key so a missing sidecar can never
		// collide with another resident or tree entry.
		out["resident:"+sessionID] = desktopIconRoomDescriptor{
			TopicID:   strings.TrimSpace(meta.TopicID),
			Title:     title,
			SessionID: sessionID,
			Ref:       ref,
		}
	}
	return out
}

// desktopIconRoomDescriptorForSession returns the descriptor map key that
// already resolves a resident runtime's session identity, if any. The session
// path key is what makes a tree descriptor and a live runtime the same Room
// even when their SessionIDs differ.
func desktopIconRoomDescriptorForSession(index map[string]string, sessionID, sessionPath string) (string, bool) {
	for _, key := range desktopIconRoomSessionKeys(sessionID, &DesktopIconTaskRef{SessionPath: sessionPath}) {
		if topicID, ok := index[key]; ok {
			return topicID, true
		}
	}
	return "", false
}

func desktopIconPinnedRoomsFromDescriptors(byTopic map[string]desktopIconRoomDescriptor, topicIDs []string) []desktopIconRoomDescriptor {
	out := make([]desktopIconRoomDescriptor, 0, min(len(topicIDs), desktopRoomPinLimit))
	for _, topicID := range topicIDs {
		if len(out) == desktopRoomPinLimit {
			break
		}
		if room, ok := byTopic[strings.TrimSpace(topicID)]; ok {
			out = append(out, room)
		}
	}
	return out
}

func desktopIconRoomForConversation(index map[string]desktopIconRoomDescriptor, conversation unread.Conversation) (desktopIconRoomDescriptor, bool) {
	for _, key := range desktopIconRoomSessionKeys(conversation.SessionID, nil) {
		if room, ok := index[key]; ok {
			return room, true
		}
	}
	return desktopIconRoomDescriptor{}, false
}

func desktopIconRoomSessionKeys(sessionID string, ref *DesktopIconTaskRef) []string {
	seen := map[string]bool{}
	keys := make([]string, 0, 6)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" || value == "path:" || seen[value] {
			return
		}
		seen[value] = true
		keys = append(keys, value)
	}
	add(sessionID)
	path := strings.TrimSpace(strings.TrimPrefix(sessionID, "path:"))
	add(path)
	add("path:" + path)
	add(sessionRuntimeKey(path))
	add("path:" + sessionRuntimeKey(path))
	if ref != nil {
		path = strings.TrimSpace(ref.SessionPath)
		add(path)
		add("path:" + path)
		add(sessionRuntimeKey(path))
		add("path:" + sessionRuntimeKey(path))
	}
	return keys
}

// desktopIconRoomDescriptorKeys returns every identity key that resolves to a
// Room descriptor: the durable SessionID, the session path, and any live
// SessionID aliases attached by the resident-runtime merge. Consumers dedupe,
// so overlapping aliases are harmless.
func desktopIconRoomDescriptorKeys(room desktopIconRoomDescriptor) []string {
	keys := desktopIconRoomSessionKeys(room.SessionID, room.Ref)
	for _, alias := range room.SessionAliases {
		keys = append(keys, desktopIconRoomSessionKeys(alias, nil)...)
	}
	return keys
}

func desktopIconPinnedRoomIndex(index map[string]int, conversation unread.Conversation) (int, bool) {
	for _, key := range desktopIconRoomSessionKeys(conversation.SessionID, nil) {
		if i, ok := index[key]; ok {
			return i, true
		}
	}
	return 0, false
}

// desktopIconRoomRef derives the Room identity from its persisted sidecar.
func desktopIconRoomRef(sessionPath string) (*DesktopIconTaskRef, agent.BranchMeta) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return nil, agent.BranchMeta{}
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return nil, agent.BranchMeta{}
	}
	return desktopIconTaskRef(meta.DefaultScope(), meta.WorkspaceRoot, meta.TopicID, sessionPath), meta
}

func (a *App) recoverDesktopIconActionsLocked() error {
	var recoveryErr error
	changed := false
	pendingRemovals := map[string]bool{}
	for i := range a.iconWidgetState.Applied {
		receipt := a.iconWidgetState.Applied[i]
		if receipt.Status == "pending" && receipt.Action == "remove" && receipt.Conversation != "" {
			pendingRemovals[receipt.RequestID] = true
		}
	}
	for i := range a.iconWidgetState.Applied {
		receipt := &a.iconWidgetState.Applied[i]
		if receipt.Status != "pending" {
			continue
		}
		if receipt.Action == "open_delegation" && receipt.SessionPath != "" {
			if err := a.advanceDesktopIconDelegation(receipt); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover delegation open %s: %w", receipt.RequestID, err))
				continue
			}
			receipt.Status = "applied"
			if err := a.saveDesktopIconStateLocked(); err != nil {
				receipt.Status = "pending"
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("finish recovered delegation open %s: %w", receipt.RequestID, err))
			}
			continue
		}
		if receipt.Action == "open_workspace" && receipt.WorkspaceRoot != "" {
			if receipt.TabID == "" {
				if err := a.createDesktopIconWorkspaceSessionLocked(receipt); err != nil {
					recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover workspace session %s: %w", receipt.RequestID, err))
					continue
				}
			}
			if err := a.exitDesktopIconModeLocked(receipt.TabID); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover workspace open %s: %w", receipt.RequestID, err))
				continue
			}
			receipt.Status = "applied"
			if err := a.saveDesktopIconStateLocked(); err != nil {
				receipt.Status = "pending"
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("finish recovered workspace open %s: %w", receipt.RequestID, err))
			}
			continue
		}
		if receipt.Action == "continue" && receipt.Text != "" {
			beforeDelivery := receipt.Delivery
			progress, err := a.advanceDesktopIconTaskContinue(receipt)
			if err != nil {
				// A completed task's controller may simply not be booted yet at
				// startup. Keep the receipt pending and retry on a later
				// snapshot instead of surfacing an expected boot state as a
				// recovery error. Real failures (reopen, history mismatch, …)
				// still surface below.
				if errors.Is(err, errDesktopIconTaskControllerNotReady) {
					continue
				}
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon task continuation %s: %w", receipt.RequestID, err))
				continue
			}
			applyDesktopIconReplyProgress(receipt, progress)
			changed = changed || receipt.Status == "applied" || receipt.Delivery != beforeDelivery
			continue
		}
		if receipt.Action == "reply" && receipt.Text != "" {
			beforeDelivery, beforeTab := receipt.Delivery, receipt.TabID
			progress, err := a.advanceDesktopIconPersonReply(receipt)
			if err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon reply %s: %w", receipt.RequestID, err))
				continue
			}
			applyDesktopIconReplyProgress(receipt, progress)
			changed = changed || receipt.Status == "applied" || receipt.Delivery != beforeDelivery || receipt.TabID != beforeTab
			continue
		}
		if receipt.Action == "open_search" && receipt.Text != "" {
			if err := a.openDesktopIconSearchItemLocked(receipt.Text); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon search navigation %s: %w", receipt.RequestID, err))
				continue
			}
			receipt.Status = "applied"
			changed = true
			continue
		}
		if receipt.Action == "rename" && receipt.SessionPath != "" && receipt.Text != "" {
			if err := a.RenameSession(receipt.SessionPath, receipt.Text); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon session rename %s: %w", receipt.RequestID, err))
				continue
			}
			a.applyDesktopIconKeptRenameLocked(receipt.SessionPath, receipt.Text)
			receipt.Status = "applied"
			changed = true
			continue
		}
		if receipt.Action == "remove" && receipt.Conversation != "" {
			if err := a.finishDesktopIconConversationRemoveLocked(receipt.Conversation, receipt.ReadSequence); err != nil {
				recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover personal icon removal %s: %w", receipt.RequestID, err))
				continue
			}
			receipt.Status = "applied"
			changed = true
			continue
		}
		if receipt.Action != "ok" && receipt.Action != "dismiss" {
			continue
		}
		if receipt.TabID == "" {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon action %s: task id is missing", receipt.RequestID))
			continue
		}
		// Legacy receipts predate sessionPath persistence and their tab ID is
		// random (never restored across restarts), so once the tab is no longer
		// live and no session path survives there is nothing left to clear: the
		// dismiss already removed the kept item. Settle such receipts instead of
		// surfacing the same error on every snapshot.
		if receipt.SessionPath == "" && a.tabByID(receipt.TabID) == nil {
			receipt.Status = "applied"
			changed = true
			continue
		}
		if err := a.finishDesktopIconCompletionLocked(DesktopIconActionInput{ItemID: receipt.ItemID}, nil, receipt.TabID, receipt.SessionPath, receipt.Conversation, receipt.ReadSequence); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon action %s: %w", receipt.RequestID, err))
			continue
		}
		receipt.Status = "applied"
		changed = true
	}
	if changed {
		if err := a.saveDesktopIconStateLocked(); err != nil {
			// Keep removals visible and retryable when the applied receipt cannot
			// be persisted. The watermark side effect is idempotent, so the next
			// snapshot can safely retry without losing the user's action target.
			for i := range a.iconWidgetState.Applied {
				if pendingRemovals[a.iconWidgetState.Applied[i].RequestID] {
					a.iconWidgetState.Applied[i].Status = "pending"
				}
			}
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("save recovered icon actions: %w", err))
		}
	}
	return recoveryErr
}

func (a *App) desktopIconPreferences() (style string, hover int, showDelegation, showExternalTools bool) {
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return "icons", 1200, false, false
	}
	return cfg.DesktopWidgetStyle(), cfg.DesktopHoverStatusDelayMs(), cfg.DesktopWidgetShowDelegation(), cfg.DesktopWidgetShowExternalTools()
}

// DesktopIconSearch searches the durable session index plus the complete
// workspace list. It remains useful after a task is dismissed or omitted from
// the compact icon projection.
func (a *App) DesktopIconSearch(query string) DesktopIconSearchResult {
	infos, spaces, err := a.desktopIconSearchData()
	result := DesktopIconSearchResult{Items: buildDesktopIconSearchItems(query, infos, spaces)}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func (a *App) desktopIconSearchData() ([]agent.SessionInfo, []WidgetWorkspaceOption, error) {
	seen := map[string]struct{}{}
	infos := []agent.SessionInfo{}
	var searchErr error
	for _, dir := range a.knownSessionDirs() {
		listed, err := agent.ListSessions(dir)
		if err != nil {
			searchErr = errors.Join(searchErr, fmt.Errorf("index %s: %w", dir, err))
			continue
		}
		for _, info := range listed {
			key := sessionRuntimeKey(info.Path)
			if key == "" {
				key = filepath.Clean(info.Path)
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			infos = append(infos, info)
		}
	}
	return infos, a.ListWidgetWorkspaces(), searchErr
}

func buildDesktopIconSearchItems(query string, infos []agent.SessionInfo, spaces []WidgetWorkspaceOption) []DesktopIconSearchItem {
	needle := strings.ToLower(strings.TrimSpace(query))
	type rankedItem struct {
		item  DesktopIconSearchItem
		score int
	}
	ranked := make([]rankedItem, 0, len(infos)+len(spaces))
	add := func(item DesktopIconSearchItem, haystack string) {
		haystack = strings.ToLower(haystack)
		if needle != "" && !strings.Contains(haystack, needle) {
			return
		}
		score := 0
		if strings.Contains(strings.ToLower(item.Title), needle) {
			score = 2
		}
		if strings.EqualFold(strings.TrimSpace(item.Title), needle) {
			score = 3
		}
		ranked = append(ranked, rankedItem{item: item, score: score})
	}
	for _, info := range infos {
		title := firstNonEmpty(strings.TrimSpace(info.CustomTitle), strings.TrimSpace(info.TopicTitle), strings.TrimSpace(info.Preview), filepath.Base(info.Path))
		subtitle := firstNonEmpty(strings.TrimSpace(info.Preview), strings.TrimSpace(info.WorkspaceRoot), "历史会话")
		kind := "session"
		switch {
		case info.SessionKind == agent.SessionKindWork:
			kind = "task"
		case info.SessionKind == agent.SessionKindCollaboration:
			kind = "room"
		case strings.TrimSpace(info.Channel) != "":
			kind = "person"
		}
		item := DesktopIconSearchItem{
			ID: "search:" + widgetRevision("session", info.Path), Kind: kind, Title: title,
			Subtitle: subtitle, SourceID: info.Path, LastActivityAt: info.LastActivityAt.UnixMilli(),
		}
		add(item, strings.Join([]string{title, subtitle, info.TopicTitle, info.WorkspaceRoot, info.Path, info.WorkID}, " "))
	}
	for _, space := range spaces {
		if space.Scope != widgetWorkspaceProject || strings.TrimSpace(space.Root) == "" {
			continue
		}
		item := DesktopIconSearchItem{ID: "search:" + widgetRevision("workspace", space.Root), Kind: "workspace", Title: space.Name, Subtitle: space.Root, SourceID: space.Root}
		add(item, space.Name+" "+space.Root)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		if ranked[i].item.LastActivityAt != ranked[j].item.LastActivityAt {
			return ranked[i].item.LastActivityAt > ranked[j].item.LastActivityAt
		}
		return ranked[i].item.Title < ranked[j].item.Title
	})
	if len(ranked) > 64 {
		ranked = ranked[:64]
	}
	out := make([]DesktopIconSearchItem, len(ranked))
	for i := range ranked {
		out[i] = ranked[i].item
	}
	return out
}

// openDesktopIconSearchItemLocked is called only while iconWidgetMu is held.
func (a *App) openDesktopIconSearchItemLocked(id string) error {
	infos, spaces, err := a.desktopIconSearchData()
	if err != nil && len(infos) == 0 {
		return err
	}
	for _, space := range spaces {
		if space.Scope != widgetWorkspaceProject || "search:"+widgetRevision("workspace", space.Root) != id {
			continue
		}
		if _, err := a.SwitchWorkspace(space.Root); err != nil {
			return err
		}
		return a.exitDesktopIconModeLocked("")
	}
	for _, info := range infos {
		if "search:"+widgetRevision("session", info.Path) != id {
			continue
		}
		meta, ok, err := agent.LoadBranchMeta(info.Path)
		if err != nil || !ok {
			return fmt.Errorf("search result session metadata unavailable: %w", err)
		}
		tab, err := a.ActivateTopic(meta.DefaultScope(), meta.WorkspaceRoot, meta.TopicID, info.Path)
		if err != nil {
			return err
		}
		return a.exitDesktopIconModeLocked(tab.ID)
	}
	return errors.New("search result is no longer available")
}

// desktopIconWorkspaces is the single projection for the configurable desktop
// slots. Pinned projects keep the backend pin order; the current project
// follows, and remaining slots are filled by persisted activity time.
func desktopIconWorkspaces(tree []ProjectNode, activeRoot string, slots int) []WidgetWorkspaceOption {
	slots = max(0, min(desktopWorkspacePinLimit, slots))
	if slots == 0 {
		return nil
	}
	activeRoot = normalizeProjectRoot(activeRoot)
	spaces := make([]WidgetWorkspaceOption, 0, len(tree))
	for _, node := range tree {
		root := normalizeProjectRoot(node.Root)
		if node.Kind != "project" || root == "" {
			continue
		}
		// Unpinned transient shells stay out of automatic backfill, but an
		// explicitly pinned workspace must project with its stable identity so
		// the management dialog's pin never silently disappears from the desktop.
		if widgetIsTransientRoot(root, node.Label) && !node.Pinned {
			continue
		}
		spaces = append(spaces, WidgetWorkspaceOption{
			Scope: widgetWorkspaceProject, Name: firstNonEmpty(strings.TrimSpace(node.Label), workspaceName(root)), Root: root,
			Icon: node.ProjectIcon, Pinned: node.Pinned, LastActivityAt: desktopIconProjectActivity(node),
		})
	}
	sort.SliceStable(spaces, func(i, j int) bool {
		left, right := spaces[i], spaces[j]
		if left.Pinned != right.Pinned {
			return left.Pinned
		}
		if left.Pinned {
			return false
		}
		leftActive, rightActive := left.Root == activeRoot, right.Root == activeRoot
		if leftActive != rightActive {
			return leftActive
		}
		return left.LastActivityAt > right.LastActivityAt
	})
	return spaces[:min(slots, len(spaces))]
}

func desktopIconActiveWorkspace(sources []widgetSource) string {
	for _, source := range sources {
		if source.meta.Active && source.meta.Scope == widgetWorkspaceProject {
			return source.meta.WorkspaceRoot
		}
	}
	return ""
}

func desktopIconProjectActivity(node ProjectNode) int64 {
	latest := node.LastActivityAt
	for _, child := range node.Children {
		latest = max(latest, desktopIconProjectActivity(child))
	}
	return latest
}

type desktopIconSessionPresentation struct {
	sessionID, workspaceIcon string
	ref                      *DesktopIconTaskRef
}

// desktopIconSessionPresentations indexes both runtime and persisted tree
// identities. Runtime entries win when both exist, while the project tree
// keeps an unopened historical IM session visually stable after restart.
func desktopIconSessionPresentations(sources []widgetSource, tree []ProjectNode) map[string]desktopIconSessionPresentation {
	out := map[string]desktopIconSessionPresentation{}
	add := func(presentation desktopIconSessionPresentation, keys ...string) {
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key != "" && key != "path:" {
				out[key] = presentation
			}
		}
	}
	var addTree func([]ProjectNode)
	addTree = func(nodes []ProjectNode) {
		for _, node := range nodes {
			sessionPath := strings.TrimSpace(node.SessionPath)
			if sessionPath != "" {
				scope := "global"
				if strings.TrimSpace(node.Root) != "" {
					scope = "project"
				}
				add(desktopIconSessionPresentation{
					sessionID: strings.TrimSpace(node.SessionID), workspaceIcon: strings.TrimSpace(node.ProjectIcon),
					ref: desktopIconTaskRef(scope, node.Root, node.TopicID, sessionPath),
				}, node.SessionID, sessionPath, "path:"+sessionPath, sessionRuntimeKey(sessionPath), "path:"+sessionRuntimeKey(sessionPath))
			}
			addTree(node.Children)
		}
	}
	addTree(tree)
	for _, source := range sources {
		meta := source.meta
		sessionPath := strings.TrimSpace(meta.SessionPath)
		add(desktopIconSessionPresentation{
			sessionID: strings.TrimSpace(meta.SessionID), workspaceIcon: strings.TrimSpace(meta.ProjectIcon),
			ref: desktopIconTaskRef(meta.Scope, meta.WorkspaceRoot, meta.TopicID, sessionPath),
		}, meta.ID, meta.SessionID, meta.WorkID, sessionPath, "path:"+sessionPath, sessionRuntimeKey(sessionPath), "path:"+sessionRuntimeKey(sessionPath))
	}
	return out
}

func desktopIconSessionPresentationFor(presentations map[string]desktopIconSessionPresentation, sessionID string) (desktopIconSessionPresentation, bool) {
	sessionID = strings.TrimSpace(sessionID)
	path := strings.TrimSpace(strings.TrimPrefix(sessionID, "path:"))
	for _, key := range []string{sessionID, path, sessionRuntimeKey(path), "path:" + sessionRuntimeKey(path)} {
		if presentation, ok := presentations[key]; ok {
			return presentation, true
		}
	}
	return desktopIconSessionPresentation{}, false
}

func buildDesktopIconSnapshot(sources []widgetSource, unreadState UnreadState, spaces []WidgetWorkspaceOption, persisted desktopIconPersistedState, hover int, roomPresentations desktopRoomNoticePresentations, roomRefs map[string]*DesktopIconTaskRef, subagentCounts map[widgetSubagentKey]int, delegations []DesktopIconDelegation) DesktopIconSnapshot {
	return buildDesktopIconSnapshotWithPresentations(sources, unreadState, spaces, persisted, hover, roomPresentations, roomRefs, subagentCounts, desktopIconSessionPresentations(sources, nil), nil, delegations)
}

func buildDesktopIconSnapshotWithPresentations(sources []widgetSource, unreadState UnreadState, spaces []WidgetWorkspaceOption, persisted desktopIconPersistedState, hover int, roomPresentations desktopRoomNoticePresentations, roomRefs map[string]*DesktopIconTaskRef, subagentCounts map[widgetSubagentKey]int, sessionPresentations map[string]desktopIconSessionPresentation, pinnedRooms []desktopIconRoomDescriptor, delegations []DesktopIconDelegation, roomDescriptorSets ...map[string]desktopIconRoomDescriptor) DesktopIconSnapshot {
	snapshot := DesktopIconSnapshot{HoverStatusDelayMs: hover, UnreadRevision: unreadState.Summary.Revision, Delegations: []DesktopIconDelegation{}}
	if delegations != nil {
		snapshot.Delegations = append(snapshot.Delegations, delegations...)
	}
	items := make([]DesktopIconItem, 0, len(sources)+len(spaces)+8)
	taskBySource := map[string]int{}
	pinnedRoomBySession := map[string]int{}
	roomBySession := map[string]desktopIconRoomDescriptor{}
	addLiveRoom := func(room desktopIconRoomDescriptor) {
		for _, key := range desktopIconRoomDescriptorKeys(room) {
			if _, exists := roomBySession[key]; !exists {
				roomBySession[key] = room
			}
		}
	}
	for _, room := range pinnedRooms {
		addLiveRoom(room)
	}
	liveRoomGate := len(roomDescriptorSets) > 0
	if len(roomDescriptorSets) > 0 {
		for _, room := range roomDescriptorSets[0] {
			addLiveRoom(room)
		}
	}
	delegatedRunning := 0
	countedParents := map[widgetSubagentKey]bool{}
	countedCLI := map[string]bool{}
	hiddenKeptIDs := map[string]bool{}
	hiddenKeptPaths := map[string]bool{}
	for _, source := range sources {
		if !strings.EqualFold(strings.TrimSpace(source.meta.SessionSource), "cli") && !widgetSourceIsSubagent(source) {
			continue
		}
		for _, id := range []string{source.meta.ID, source.meta.SessionID, source.meta.WorkID} {
			if id = strings.TrimSpace(id); id != "" {
				hiddenKeptIDs[id] = true
			}
		}
		if path := strings.TrimSpace(source.meta.SessionPath); path != "" {
			hiddenKeptPaths[sessionRuntimeKey(path)] = true
		}
	}
	spaceCount := 0
	for _, space := range spaces {
		if space.Scope != "auto" && spaceCount < desktopIconMaxSpaces {
			spaceCount++
		}
	}
	taskLimit := min(desktopIconMaxTasks, max(1, (desktopIconWidth-36)/68-4-spaceCount))
	taskCount := 0
	for order, room := range pinnedRooms {
		item := DesktopIconItem{
			ID: "room:" + room.TopicID, Kind: "room", SourceID: room.TopicID,
			Title: firstNonEmpty(strings.TrimSpace(room.Title), "Room"), Icon: room.Icon, Status: "idle",
			SessionID: strings.TrimSpace(room.SessionID), Notifications: []DesktopIconNotice{},
			Position: DesktopIconPosition{Row: "top", Zone: "conversation", Order: order}, SessionRef: room.Ref,
		}
		items = append(items, item)
		index := len(items) - 1
		for _, key := range desktopIconRoomDescriptorKeys(room) {
			if _, exists := pinnedRoomBySession[key]; !exists {
				pinnedRoomBySession[key] = index
			}
		}
	}

	for _, source := range sources {
		meta := source.meta
		if meta.SessionKind == "collaboration" {
			continue
		}
		// Real running sub-agents owned by this session are the authoritative
		// delegation signal. Foreground parents keep their own task icon below
		// and still contribute their running sub-agents to the fixed entry.
		parentKey := newWidgetSubagentKey(source.sessionDir, source.branchID)
		knownRunning := subagentCounts[parentKey]
		realRunning := knownRunning
		if countedParents[parentKey] {
			realRunning = 0
		} else if knownRunning > 0 {
			countedParents[parentKey] = true
		}
		if strings.EqualFold(strings.TrimSpace(meta.SessionSource), "cli") {
			// CLI/external dispatch is intentionally hidden as an independent
			// task icon and projects onto the delegation entry: its own running
			// turn counts unless it already owns real running sub-agents, which
			// are the authoritative (non-double-counted) signal.
			delegatedRunning += realRunning
			cliKey := firstNonEmpty(strings.TrimSpace(meta.SessionID), strings.TrimSpace(meta.ID))
			if knownRunning == 0 && meta.RunningWork && !countedCLI[cliKey] {
				delegatedRunning++
				countedCLI[cliKey] = true
			}
			continue
		}
		if widgetSourceIsSubagent(source) {
			continue
		}
		delegatedRunning += realRunning
		if !meta.RunningWork && !meta.NeedsAttention && strings.TrimSpace(meta.StartupErr) == "" && !source.has {
			continue
		}
		item := desktopTaskItem(source, len(items), persisted.CompletionSummaries)
		items = append(items, item)
		taskCount++
		index := len(items) - 1
		sessionPath := strings.TrimSpace(meta.SessionPath)
		for _, key := range []string{meta.ID, meta.SessionID, meta.WorkID, sessionPath, "path:" + sessionPath, sessionRuntimeKey(sessionPath), "path:" + sessionRuntimeKey(sessionPath)} {
			if key = strings.TrimSpace(key); key != "" && key != "path:" {
				taskBySource[key] = index
			}
		}
		if taskCount >= taskLimit {
			break
		}
	}

	keptIDs := make([]string, 0, len(persisted.Kept))
	for id := range persisted.Kept {
		keptIDs = append(keptIDs, id)
	}
	sort.Strings(keptIDs)
	for _, id := range keptIDs {
		if taskCount >= taskLimit {
			break
		}
		kept := persisted.Kept[id]
		if desktopIconKeptIsDelegation(kept, hiddenKeptIDs, hiddenKeptPaths) {
			continue
		}
		if _, live := taskBySource[kept.SourceID]; live {
			continue
		}
		notice := desktopIconNoticeForKept(kept, persisted.CompletionSummaries)
		items = append(items, DesktopIconItem{
			ID: kept.ItemID, Kind: "task", SourceID: kept.SourceID, Title: kept.Title,
			Subtitle: kept.Summary, Status: "done", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: kept.Order},
			Revision: kept.Revision, Retained: true, Notifications: []DesktopIconNotice{notice},
			SessionID: strings.TrimSpace(kept.SessionID), WorkspaceIcon: projectIcon(kept.WorkspaceRoot),
			SessionRef: desktopIconTaskRef(kept.Scope, kept.WorkspaceRoot, kept.TopicID, kept.SessionPath),
		})
		taskCount++
	}

	roomOrder := len(pinnedRooms)
	regularRoomCount := len(pinnedRooms)
	personCount := 0
	for _, conversation := range unreadState.Summary.Conversations {
		if sequence, dismissed := persisted.DismissedConversations[conversation.Key]; dismissed && sequence >= conversation.LatestSequence && !desktopIconConversationRemovePending(persisted.Applied, conversation.Key) {
			continue
		}
		if index, ok := desktopTaskIndex(taskBySource, conversation); ok && conversation.UnreadCount > 0 {
			// Task attention is projected from Controller/BranchMeta. The unread
			// store normally supplies only its durable watermark so the same
			// completion or prompt is not rendered twice. An IM message can arrive
			// while the session has no Controller attention notice; in that case
			// project the unread message onto the real task row instead of dropping
			// it or rendering a duplicate generic person icon.
			if len(items[index].Notifications) > 0 {
				items[index].Notifications[0].Conversation = conversation.Key
				items[index].Notifications[0].ReadSequence = conversation.LatestSequence
			} else {
				items[index].Notifications = noticesForConversation(conversation, roomPresentations[conversation.SessionID])
			}
			continue
		}
		if conversation.Source != unread.SourceRoom && conversation.Source != unread.SourceIM {
			continue
		}
		kind := "room"
		if conversation.Source == unread.SourceIM {
			kind = "person"
		}
		if kind == "room" {
			_, live := desktopIconRoomForConversation(roomBySession, conversation)
			if liveRoomGate && !live {
				// The unread store is durable and intentionally outlives a Room
				// membership. Only the current project-tree projection may turn
				// that history back into a desktop icon; stale pins, read markers
				// and notice presentations cannot resurrect a removed Room.
				continue
			}
			if index, ok := desktopIconPinnedRoomIndex(pinnedRoomBySession, conversation); ok {
				items[index].Notifications = noticesForConversation(conversation, roomPresentations[conversation.SessionID])
				items[index].ConversationSequence = conversation.LatestSequence
				if conversation.UnreadCount > 0 {
					items[index].Status = "unread"
				}
				continue
			}
			// Pins own the seven durable Room slots. Legacy/read Room projections
			// may fill remaining slots, while every unread Room is appended even
			// when all seven durable positions are occupied.
			if conversation.UnreadCount == 0 {
				if regularRoomCount >= desktopRoomPinLimit {
					continue
				}
				regularRoomCount++
			}
		} else {
			if personCount >= desktopIconMaxPeople {
				continue
			}
			personCount++
		}
		item := DesktopIconItem{
			ID: "conversation:" + conversation.Key, Kind: kind, SourceID: conversation.Key,
			Title: firstNonEmpty(strings.TrimSpace(conversation.Title), "消息"), Status: "unread",
			Notifications:        noticesForConversation(conversation, roomPresentations[conversation.SessionID]),
			Position:             DesktopIconPosition{Row: "top", Zone: "conversation", Order: roomOrder},
			ConversationSequence: conversation.LatestSequence,
		}
		if kind == "room" {
			// Room items carry the same backend-generated session identity as
			// task icons (scope/workspaceRoot/topicID/sessionPath), so opening
			// never depends on the first notice's TabID, a read Room with no
			// notice, or whatever tab happens to be active.
			item.SessionRef = roomRefs[conversation.SessionID]
			if room, ok := desktopIconRoomForConversation(roomBySession, conversation); ok {
				item.Icon = room.Icon
				item.SessionID = room.SessionID
				if room.Ref != nil {
					item.SessionRef = room.Ref
				}
			}
		} else if presentation, ok := desktopIconSessionPresentationFor(sessionPresentations, conversation.SessionID); ok {
			// Reuse the corresponding session's exact Agent Icon seed, workspace
			// badge and durable ref. Opening still uses ResolveUnreadSession so a
			// stale IM binding keeps its existing repair/retry behavior.
			item.SessionID = presentation.sessionID
			item.WorkspaceIcon = presentation.workspaceIcon
			item.SessionRef = presentation.ref
		}
		items = append(items, item)
		roomOrder++
	}

	spaceOrder := 0
	for _, space := range spaces {
		if space.Scope == "auto" {
			continue
		}
		if spaceOrder >= desktopIconMaxSpaces {
			break
		}
		id := "workspace:" + firstNonEmpty(space.Root, space.Scope)
		items = append(items, DesktopIconItem{
			ID: id, Kind: "workspace", SourceID: firstNonEmpty(space.Root, space.Scope), Title: space.Name,
			Icon: space.Icon, Status: "idle", Notifications: []DesktopIconNotice{},
			Position: DesktopIconPosition{Row: "bottom", Zone: "workspace", Order: spaceOrder},
		})
		spaceOrder++
	}

	// The fixed bottom bar is the declared order of the stable source ids:
	// 新建 → 工作区 → Rooms → 委托 → 搜索. Position.Order is derived from
	// this slice index, never from map iteration, so the bar order is a Go
	// contract.
	if delegations != nil {
		delegatedRunning = len(snapshot.Delegations)
	}
	fixed := []struct{ id, title, icon string }{
		{"new", "新建", "plus"}, {"workspace", "工作区", "workspace"}, {"rooms", "Rooms", "rooms"}, {"delegate", "委托", "users"}, {"search", "搜索", "search"},
	}
	for i, entry := range fixed {
		status, count := "idle", 0
		if entry.id == "delegate" && delegatedRunning > 0 {
			status, count = "running", delegatedRunning
		}
		items = append(items, DesktopIconItem{
			ID: "fixed:" + entry.id, Kind: "fixed", SourceID: entry.id, Title: entry.title, Icon: entry.icon,
			Status: status, ActivityCount: count, Notifications: []DesktopIconNotice{},
			Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: i},
		})
	}

	for i := range items {
		item := &items[i]
		if position, ok := persisted.Positions[item.ID]; ok && validDesktopIconPosition(*item, position) {
			item.Position = position
		}
		if key := desktopIconAppearanceKey(*item); key != "" {
			item.AppearanceSeed = persisted.AppearanceSeeds[key]
		}
		sortDesktopIconNotices(item.Notifications)
		item.UnreadCount = desktopIconUnreadCount(*item)
		if item.UnreadCount > 0 {
			item.Status = desktopIconStatus(item.Notifications[0].Kind, item.Status)
		}
		item.Revision = desktopIconItemRevision(*item)
		if item.ID == "fixed:delegate" {
			item.Revision = widgetRevision(item.Revision, desktopIconDelegationRevision(snapshot.Delegations))
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i].Position, items[j].Position
		if left.Row != right.Row {
			return left.Row < right.Row
		}
		if left.Zone != right.Zone {
			return desktopIconZoneRank(left.Zone) < desktopIconZoneRank(right.Zone)
		}
		if left.Order != right.Order {
			return left.Order < right.Order
		}
		return items[i].ID < items[j].ID
	})
	snapshot.Items = items
	snapshot.Revision = desktopIconSnapshotRevision(snapshot)
	if unreadState.Error != "" {
		snapshot.Error = unreadState.Error
	}
	return snapshot
}

func desktopIconKeptIsDelegation(kept desktopIconKept, hiddenIDs, hiddenPaths map[string]bool) bool {
	if hiddenIDs[strings.TrimSpace(kept.SourceID)] || hiddenIDs[strings.TrimSpace(kept.SessionID)] {
		return true
	}
	path := strings.TrimSpace(kept.SessionPath)
	if path == "" {
		return false
	}
	if strings.EqualFold(filepath.Base(filepath.Dir(filepath.Clean(path))), "subagents") {
		return true
	}
	return hiddenPaths[sessionRuntimeKey(path)]
}

// desktopIconUnreadCount keeps presentation-only retained completion notices
// out of the unread badge. They exist solely so an opened Session reuses the
// completion-card popup; live completion and actionable notices still count.
func desktopIconUnreadCount(item DesktopIconItem) int {
	count := 0
	for _, notice := range item.Notifications {
		if item.Retained && notice.Kind == "completed" && strings.HasPrefix(notice.ID, "retained:") {
			continue
		}
		count++
	}
	return count
}

func desktopTaskItem(source widgetSource, order int, summaries map[string]desktopIconCompletionSummary) DesktopIconItem {
	meta := source.meta
	title := firstNonEmpty(strings.TrimSpace(meta.SessionDisplayTitle), strings.TrimSpace(meta.TopicTitle), "当前任务")
	item := DesktopIconItem{
		ID: "task:" + meta.ID, Kind: "task", SourceID: meta.ID, Title: title,
		Subtitle: firstNonEmpty(strings.TrimSpace(meta.WorkspaceName), "WorkGround2"), Status: "idle",
		Notifications: []DesktopIconNotice{}, Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: order},
		SessionID: strings.TrimSpace(meta.SessionID), WorkspaceIcon: strings.TrimSpace(meta.ProjectIcon),
		SessionRef: desktopIconTaskRef(meta.Scope, meta.WorkspaceRoot, meta.TopicID, meta.SessionPath),
	}
	if source.has {
		message := messageForPending(source)
		kind := "needs_input"
		if message.StateCode == "confirm" {
			kind = "needs_confirm"
		}
		item.Notifications = append(item.Notifications, desktopNoticeForMessage(message, kind, 1, meta.NeedsAttentionAt))
	} else if text := strings.TrimSpace(meta.StartupErr); text != "" {
		body, summaryStatus := desktopIconCompletionSummaryFor(summaries, desktopIconFailureKey(meta.ID, text), conciseWidgetText(text, 110))
		message := baseWidgetMessage(meta, "error", "任务失败", body)
		message.ID = "error:" + meta.ID
		message.Revision = widgetMessageRevision(message)
		notice := desktopNoticeForMessage(message, "failed", 2, meta.NeedsAttentionAt)
		notice.SummaryStatus = summaryStatus
		item.Notifications = append(item.Notifications, notice)
	} else if meta.NeedsAttention {
		body, needsSummary := completionSummaryFallback(source.resultText)
		if body == "" {
			body = "任务已完成，记录仍可在搜索中找到。"
		}
		summaryStatus := ""
		if needsSummary {
			body, summaryStatus = desktopIconCompletionSummaryFor(summaries, desktopIconCompletionKey(meta.ID, "completed", meta.NeedsAttentionAt), body)
		}
		message := baseWidgetMessage(meta, "result", "任务完成", body)
		message.ID = fmt.Sprintf("result:%s:%d", meta.ID, meta.NeedsAttentionAt)
		message.Revision = widgetMessageRevision(message)
		notice := desktopNoticeForMessage(message, "completed", 2, meta.NeedsAttentionAt)
		notice.SummaryStatus = summaryStatus
		item.Notifications = append(item.Notifications, notice)
	}
	if meta.RunningWork {
		now := time.Now().UnixMilli()
		elapsed := int64(0)
		if meta.TurnStartedAt > 0 {
			elapsed = max(int64(0), now-meta.TurnStartedAt)
		}
		phase, status := "Running", "running"
		summary := firstNonEmpty(strings.TrimSpace(meta.ActivityText), "正在执行当前任务")
		if meta.ActivityStatus == topicStatusThinking || meta.ActivityStatus == topicStatusStreaming ||
			(meta.ActivityStatus == "" && meta.ForegroundActive && meta.RuntimeMode == "") {
			phase, status = "Thinking", "thinking"
			summary = firstNonEmpty(strings.TrimSpace(meta.ActivityText), "正在等待思考内容")
		}
		item.Runtime = &DesktopIconRuntime{Phase: phase, Summary: summary, ElapsedMs: elapsed, UpdatedAt: now}
		item.Status = status
	}
	return item
}

// desktopIconNoticeForKept gives an opened Session the same completion card as
// a never-opened one. Legacy kept entries have no CompletionKey; their stored
// summary still becomes a valid completion notice instead of the generic
// open-only popup.
func desktopIconNoticeForKept(kept desktopIconKept, summaries map[string]desktopIconCompletionSummary) DesktopIconNotice {
	body := strings.TrimSpace(kept.Summary)
	status := ""
	if kept.CompletionKey != "" {
		body, status = desktopIconCompletionSummaryFor(summaries, kept.CompletionKey, body)
	}
	body, _ = completionSummaryFallback(body)
	if body == "" {
		body = "任务已完成，记录仍可在搜索中找到。"
	}
	return DesktopIconNotice{
		ID:            "retained:" + widgetRevision(kept.ItemID, kept.CompletionKey, body),
		Revision:      widgetRevision(kept.CompletionKey, body, strconv.FormatInt(kept.CompletedAt, 10)),
		Kind:          "completed",
		Priority:      2,
		Title:         "任务完成",
		Body:          body,
		CreatedAt:     kept.CompletedAt,
		TabID:         kept.SourceID,
		Options:       []WidgetOption{},
		SummaryStatus: status,
	}
}

func desktopNoticeForMessage(message WidgetMessage, kind string, priority int, at int64) DesktopIconNotice {
	return DesktopIconNotice{
		ID: message.ID, Revision: message.Revision, Kind: kind, Priority: priority,
		Title: message.StateLabel, Body: message.Message, CreatedAt: at, TabID: message.TabID,
		InteractionID: message.InteractionID, QuestionID: message.QuestionID, Questions: append([]WidgetQuestion{}, message.Questions...),
		Options: append([]WidgetOption{}, message.Options...), Retryable: message.Kind == "error",
	}
}

func noticesForConversation(conversation unread.Conversation, presentations map[string]desktopRoomNoticePresentation) []DesktopIconNotice {
	out := make([]DesktopIconNotice, 0, len(conversation.Items))
	for _, item := range conversation.Items {
		kind, priority := "message", 3
		if item.Priority == unread.PriorityHigh && item.Attention == unread.AttentionNone {
			// High-priority unread events report urgency, not a live structured
			// interaction. Only Controller.PendingInteraction may project
			// needs_input/needs_confirm; otherwise a resolved ask is rendered as
			// a second answerable question until its unread watermark advances.
			priority = 1
		} else if item.Attention != unread.AttentionNone {
			priority = 1
		}
		presentation := presentations[item.ID]
		body := strings.TrimSpace(presentation.Body)
		if body == "" {
			body = strings.TrimSpace(presentations[""].Body)
		}
		if body == "" {
			body = "收到一条新消息（摘要需在完整会话中查看）"
		}
		out = append(out, DesktopIconNotice{
			ID: item.ID, Revision: strconv.FormatUint(item.Sequence, 10), Kind: kind, Priority: priority, Attention: item.Attention,
			Title: desktopRoomNoticeTitle(conversation.Title, presentation.Author, item.Attention), Body: body, TabID: conversation.SessionID,
			CreatedAt: item.OccurredAt.UnixMilli(), Conversation: conversation.Key, ReadSequence: item.Sequence,
			Options: []WidgetOption{},
		})
	}
	return out
}

func desktopRoomNoticeTitle(room, author string, attention unread.ItemAttention) string {
	author = strings.TrimSpace(author)
	prefix := author
	if prefix == "" {
		prefix = "Room 成员"
	}
	switch attention {
	case unread.AttentionMentionMember:
		return prefix + " @ 了你"
	case unread.AttentionMentionAgent:
		return prefix + " @ 了你的 Agent"
	case unread.AttentionMentionBoth:
		return prefix + " @ 了你和你的 Agent"
	}
	if author != "" {
		return author + " · " + firstNonEmpty(strings.TrimSpace(room), "新消息")
	}
	return firstNonEmpty(strings.TrimSpace(room), "新消息")
}

func desktopTaskIndex(index map[string]int, conversation unread.Conversation) (int, bool) {
	path := strings.TrimPrefix(conversation.SessionID, "path:")
	for _, key := range []string{conversation.SessionID, path, sessionRuntimeKey(path), "path:" + sessionRuntimeKey(path)} {
		if i, ok := index[strings.TrimSpace(key)]; ok {
			return i, true
		}
	}
	return 0, false
}

func sortDesktopIconNotices(notices []DesktopIconNotice) {
	sort.SliceStable(notices, func(i, j int) bool {
		if notices[i].Priority != notices[j].Priority {
			return notices[i].Priority < notices[j].Priority
		}
		leftAction := notices[i].Kind == "needs_input" || notices[i].Kind == "needs_confirm"
		rightAction := notices[j].Kind == "needs_input" || notices[j].Kind == "needs_confirm"
		if leftAction != rightAction {
			return leftAction
		}
		if notices[i].CreatedAt != notices[j].CreatedAt {
			return notices[i].CreatedAt < notices[j].CreatedAt
		}
		return notices[i].ID < notices[j].ID
	})
}

func desktopIconStatus(kind, fallback string) string {
	switch kind {
	case "needs_input":
		return "needs_input"
	case "needs_confirm":
		return "needs_confirm"
	case "failed":
		return "failed"
	case "completed":
		return "done"
	default:
		return "unread"
	}
}

func desktopIconConversationRemovePending(receipts []desktopIconReceipt, conversation string) bool {
	for i := range receipts {
		if receipts[i].Status == "pending" && receipts[i].Action == "remove" && receipts[i].Conversation == conversation {
			return true
		}
	}
	return false
}

func desktopIconItemRevision(item DesktopIconItem) string {
	parts := []string{item.ID, item.Title, item.Status, strconv.Itoa(item.Position.Order), item.Position.Row, item.Position.Zone, strconv.FormatUint(item.ConversationSequence, 10), strconv.FormatUint(item.SourceRevision, 10)}
	if item.Runtime != nil {
		parts = append(parts, item.Runtime.Phase)
	}
	// Identity seed and workspace icon are display-only but revision-bearing:
	// a changed session identity or project icon must refresh the frontend icon.
	parts = append(parts, item.SessionID, item.AppearanceSeed, item.WorkspaceIcon, item.Icon)
	if item.SessionRef != nil {
		parts = append(parts, item.SessionRef.Scope, item.SessionRef.WorkspaceRoot, item.SessionRef.TopicID, item.SessionRef.SessionPath)
	}
	for _, notice := range item.Notifications {
		parts = append(parts, notice.ID, notice.Revision, notice.Kind, string(notice.Attention), notice.Title, notice.Body)
	}
	parts = append(parts, item.Actions...)
	return widgetRevision(parts...)
}

// desktopIconAppearanceKey binds explicit appearance changes to a durable
// session identity. Session path wins because tab IDs change when a retained
// session is reopened; older entries safely fall back to SessionID/item ID.
func desktopIconAppearanceKey(item DesktopIconItem) string {
	if item.Kind != "task" {
		return ""
	}
	if item.SessionRef != nil {
		if path := strings.TrimSpace(item.SessionRef.SessionPath); path != "" {
			return "path:" + filepath.Clean(path)
		}
		if topic := strings.TrimSpace(item.SessionRef.TopicID); topic != "" {
			return "topic:" + topic
		}
	}
	if sessionID := strings.TrimSpace(item.SessionID); sessionID != "" {
		return "session:" + sessionID
	}
	return strings.TrimSpace(item.ID)
}

func desktopIconSnapshotRevision(snapshot DesktopIconSnapshot) string {
	parts := []string{strconv.Itoa(snapshot.HoverStatusDelayMs), strconv.FormatUint(snapshot.UnreadRevision, 10), desktopIconDelegationRevision(snapshot.Delegations)}
	for _, item := range snapshot.Items {
		parts = append(parts, item.ID, item.Revision)
	}
	return widgetRevision(parts...)
}

func desktopIconDelegationRevision(items []DesktopIconDelegation) string {
	parts := make([]string, 0, len(items)*8)
	for _, item := range items {
		parts = append(parts, item.ID, item.Kind, item.Content, item.Status, item.SessionTitle, item.WorkspaceName, strconv.FormatInt(item.UpdatedAt, 10))
		if item.SessionRef != nil {
			parts = append(parts, item.SessionRef.Scope, item.SessionRef.WorkspaceRoot, item.SessionRef.TopicID, item.SessionRef.SessionPath)
		}
	}
	return widgetRevision(parts...)
}

func desktopIconZoneRank(zone string) int {
	switch zone {
	case "conversation":
		return 0
	case "running":
		return 1
	case "workspace":
		return 2
	case "fixed":
		return 3
	default:
		return 4
	}
}

func validDesktopIconPosition(item DesktopIconItem, position DesktopIconPosition) bool {
	if position.Order < 0 || position.Order > 99 {
		return false
	}
	if item.Kind == "room" || item.Kind == "person" {
		return position.Row == "top" && position.Zone == "conversation"
	}
	if position.Row != "bottom" {
		return false
	}
	switch item.Kind {
	case "task", "external":
		return position.Zone == "running"
	case "workspace":
		return position.Zone == "workspace"
	case "fixed":
		return position.Zone == "fixed"
	}
	return false
}

func cloneDesktopIconState(state desktopIconPersistedState) desktopIconPersistedState {
	clone := desktopIconPersistedState{
		Positions:              make(map[string]DesktopIconPosition, len(state.Positions)),
		Kept:                   make(map[string]desktopIconKept, len(state.Kept)),
		AppearanceSeeds:        make(map[string]string, len(state.AppearanceSeeds)),
		DismissedConversations: make(map[string]uint64, len(state.DismissedConversations)),
		DismissedExternalRuns:  make(map[string]uint64, len(state.DismissedExternalRuns)),
		Applied:                append([]desktopIconReceipt(nil), state.Applied...),
		WorkspaceSlots:         state.WorkspaceSlots,
		CompletionSummaries:    make(map[string]desktopIconCompletionSummary, len(state.CompletionSummaries)),
	}
	for id, position := range state.Positions {
		clone.Positions[id] = position
	}
	for id, kept := range state.Kept {
		clone.Kept[id] = kept
	}
	for key, seed := range state.AppearanceSeeds {
		clone.AppearanceSeeds[key] = seed
	}
	for key, sequence := range state.DismissedConversations {
		clone.DismissedConversations[key] = sequence
	}
	for id, revision := range state.DismissedExternalRuns {
		clone.DismissedExternalRuns[id] = revision
	}
	for key, summary := range state.CompletionSummaries {
		clone.CompletionSummaries[key] = summary
	}
	return clone
}

// reorderDesktopIconItems performs an insertion within one architecture zone
// and writes a dense order for every affected item. This avoids duplicate order
// values and keeps the fixed zone on the right through the zone rank.
func reorderDesktopIconItems(items []DesktopIconItem, movedID string, target DesktopIconPosition, positions map[string]DesktopIconPosition) {
	type orderedIcon struct {
		id       string
		position DesktopIconPosition
	}
	zone := make([]orderedIcon, 0, len(items)+len(positions))
	seen := map[string]struct{}{movedID: {}}
	for _, item := range items {
		if item.ID != movedID && item.Position.Row == target.Row && item.Position.Zone == target.Zone {
			zone = append(zone, orderedIcon{id: item.ID, position: item.Position})
			seen[item.ID] = struct{}{}
		}
	}
	// Capacity-capped items are absent from the snapshot but retain a durable
	// position. Keep them in the ordering so their later reappearance cannot
	// collide with or reorder the visible icons.
	for id, position := range positions {
		if _, ok := seen[id]; !ok && position.Row == target.Row && position.Zone == target.Zone {
			zone = append(zone, orderedIcon{id: id, position: position})
			seen[id] = struct{}{}
		}
	}
	sort.SliceStable(zone, func(i, j int) bool {
		if zone[i].position.Order != zone[j].position.Order {
			return zone[i].position.Order < zone[j].position.Order
		}
		return zone[i].id < zone[j].id
	})
	insert := min(max(0, target.Order), len(zone))
	ordered := make([]string, 0, len(zone)+1)
	for i, item := range zone {
		if i == insert {
			ordered = append(ordered, movedID)
		}
		ordered = append(ordered, item.id)
	}
	if insert == len(zone) {
		ordered = append(ordered, movedID)
	}
	for order, id := range ordered {
		positions[id] = DesktopIconPosition{Row: target.Row, Zone: target.Zone, Order: order}
	}
}

func desktopIconIntent(input DesktopIconActionInput) string {
	revision := input.Revision
	if strings.EqualFold(strings.TrimSpace(input.Action), "open_delegation") {
		// Pending delegation opens persist their exact target identity before
		// navigation. A retry must keep the same intent even when polling has
		// advanced or removed the running-list revision meanwhile.
		revision = ""
	}
	raw, _ := json.Marshal(struct {
		Item, Notice, Revision, Action string
		Values                         []string
		Position                       *DesktopIconPosition
		Conversation                   string
		Read                           uint64
	}{
		input.ItemID, input.NoticeID, revision, input.Action, input.Values, input.Position, input.Conversation, input.ReadSequence,
	})
	return widgetRevision(string(raw))
}

func desktopIconReplyKey(conversation string, readSequence uint64, text string) string {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	return widgetRevision(strings.TrimSpace(conversation), strconv.FormatUint(readSequence, 10), normalized)
}

func desktopIconReceiptReplyKey(receipt desktopIconReceipt) string {
	if receipt.ReplyKey != "" {
		return receipt.ReplyKey
	}
	if receipt.Action != "reply" || receipt.Conversation == "" || receipt.Text == "" {
		return ""
	}
	return desktopIconReplyKey(receipt.Conversation, receipt.ReadSequence, receipt.Text)
}

func desktopIconReplyReceiptIndex(receipts []desktopIconReceipt, replyKey string) int {
	for i := range receipts {
		if desktopIconReceiptReplyKey(receipts[i]) == replyKey {
			return i
		}
	}
	return -1
}

// ApplyDesktopIconAction is the only mutation entrance for icon mode. Every
// action is stale-checked and request-id deduplicated before it reaches the
// authoritative Controller/unread state.
func (a *App) ApplyDesktopIconAction(input DesktopIconActionInput) DesktopIconActionResult {
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	input.ItemID = strings.TrimSpace(input.ItemID)
	input.NoticeID = strings.TrimSpace(input.NoticeID)
	input.Revision = strings.TrimSpace(input.Revision)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	if input.ItemID == "" || input.RequestID == "" || input.Action == "" {
		return a.desktopIconActionErrorLocked("invalid", errors.New("itemId, requestId and action are required"))
	}
	intent := desktopIconIntent(input)
	for i := range a.iconWidgetState.Applied {
		receipt := &a.iconWidgetState.Applied[i]
		if receipt.RequestID != input.RequestID {
			continue
		}
		if receipt.Intent != intent {
			return a.desktopIconActionErrorLocked("invalid", errors.New("requestId was already used for another action"))
		}
		if receipt.Status == "pending" {
			if input.Action == "open_delegation" && receipt.SessionPath != "" {
				if err := a.advanceDesktopIconDelegation(receipt); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				receipt.Status = "applied"
				if err := a.saveDesktopIconStateLocked(); err != nil {
					receipt.Status = "pending"
					return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish delegation open: %w", err))
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "open" && receipt.Action == "open_workspace" && receipt.WorkspaceRoot != "" {
				if receipt.TabID == "" {
					if err := a.createDesktopIconWorkspaceSessionLocked(receipt); err != nil {
						return a.desktopIconActionErrorLocked("retryable_error", err)
					}
				}
				if err := a.exitDesktopIconModeLocked(receipt.TabID); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				receipt.Status = "applied"
				if err := a.saveDesktopIconStateLocked(); err != nil {
					receipt.Status = "pending"
					return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish workspace open: %w", err))
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "continue" && receipt.Text != "" {
				progress, err := a.advanceDesktopIconTaskContinue(receipt)
				if err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				applyDesktopIconReplyProgress(receipt, progress)
				if err := a.saveDesktopIconStateLocked(); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				status := "accepted"
				if progress == desktopIconReplyConfirmed {
					status = "already_applied"
				}
				return DesktopIconActionResult{Status: status, Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "reply" && receipt.Text != "" {
				progress, err := a.advanceDesktopIconPersonReply(receipt)
				if err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				applyDesktopIconReplyProgress(receipt, progress)
				if err := a.saveDesktopIconStateLocked(); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				status := "accepted"
				if progress == desktopIconReplyConfirmed {
					status = "already_applied"
				}
				return DesktopIconActionResult{Status: status, Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "open_search" && receipt.Text != "" {
				if err := a.openDesktopIconSearchItemLocked(receipt.Text); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				a.markDesktopIconReceiptApplied(input.RequestID)
				if err := a.saveDesktopIconStateLocked(); err != nil {
					a.markDesktopIconReceiptPending(input.RequestID)
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "remove" && receipt.Conversation != "" {
				if err := a.finishDesktopIconConversationRemoveLocked(receipt.Conversation, receipt.ReadSequence); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				a.markDesktopIconReceiptApplied(input.RequestID)
				if err := a.saveDesktopIconStateLocked(); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action == "rename" && receipt.SessionPath != "" && receipt.Text != "" {
				if err := a.RenameSession(receipt.SessionPath, receipt.Text); err != nil {
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				a.applyDesktopIconKeptRenameLocked(receipt.SessionPath, receipt.Text)
				receipt.Status = "applied"
				if err := a.saveDesktopIconStateLocked(); err != nil {
					receipt.Status = "pending"
					return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish session rename: %w", err))
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action != "ok" && input.Action != "dismiss" {
				return a.desktopIconActionErrorLocked("retryable_error", errors.New("action receipt is pending recovery"))
			}
			if err := a.finishDesktopIconCompletionLocked(input, nil, receipt.TabID, receipt.SessionPath, receipt.Conversation, receipt.ReadSequence); err != nil {
				return a.desktopIconActionErrorLocked("retryable_error", err)
			}
			a.markDesktopIconReceiptApplied(input.RequestID)
			if err := a.saveDesktopIconStateLocked(); err != nil {
				return a.desktopIconActionErrorLocked("retryable_error", err)
			}
			return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
		}
		return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "reply" && input.Conversation != "" && len(input.Values) == 1 {
		if result, found := a.resumeDesktopIconReplyLocked(desktopIconReplyKey(input.Conversation, input.ReadSequence, input.Values[0])); found {
			return result
		}
	}

	snapshot := a.desktopIconSnapshotLocked()
	var item *DesktopIconItem
	for i := range snapshot.Items {
		if snapshot.Items[i].ID == input.ItemID {
			item = &snapshot.Items[i]
			break
		}
	}
	if item == nil {
		return DesktopIconActionResult{Status: "stale", Error: "图标已经变化", Snapshot: snapshot}
	}
	if input.Revision != item.Revision {
		return DesktopIconActionResult{Status: "stale", Error: "图标状态已经变化", Snapshot: snapshot}
	}

	var notice *DesktopIconNotice
	if input.NoticeID != "" {
		for i := range item.Notifications {
			if item.Notifications[i].ID == input.NoticeID {
				notice = &item.Notifications[i]
				break
			}
		}
		if notice == nil {
			return DesktopIconActionResult{Status: "stale", Error: "通知已经变化", Snapshot: snapshot}
		}
	} else if len(item.Notifications) > 0 {
		notice = &item.Notifications[0]
	}

	if input.Action == "ok" {
		// OK only closes the popup — a purely local frontend action. It must
		// not acknowledge, clear attention, write a receipt, or turn the item
		// into the retained "open-only" state, so reopening the same item
		// still shows the same summary and the same three buttons. Old
		// pending "ok" receipts are still recovered by
		// recoverDesktopIconActionsLocked and the pending branch above.
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "dismiss" {
		if item.Kind != "task" || notice == nil || (notice.Kind != "completed" && notice.Kind != "failed") {
			return a.desktopIconActionErrorLocked("invalid", errors.New("completion notification is required"))
		}
		delete(a.iconWidgetState.Kept, item.ID)
		sessionPath := ""
		if item.SessionRef != nil {
			sessionPath = item.SessionRef.SessionPath
		}
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "pending", Action: input.Action, ItemID: item.ID, TabID: notice.TabID, SessionPath: sessionPath, Conversation: notice.Conversation, ReadSequence: notice.ReadSequence, AppliedAt: time.Now().UnixMilli()})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare completion action: %w", err))
		}
		if err := a.finishDesktopIconCompletionLocked(input, notice, notice.TabID, sessionPath, notice.Conversation, notice.ReadSequence); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		a.markDesktopIconReceiptApplied(input.RequestID)
		if err := a.saveDesktopIconStateLocked(); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish completion action: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "reply" && item.Kind == "person" {
		return a.applyDesktopIconPersonReplyLocked(*item, notice, input, intent)
	}
	if input.Action == "continue" {
		return a.applyDesktopIconTaskContinueLocked(*item, notice, input, intent)
	}
	if input.Action == "open_search" {
		if item.ID != "fixed:search" || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("search result is required"))
		}
		targetID := strings.TrimSpace(input.Values[0])
		before := cloneDesktopIconState(a.iconWidgetState)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "open_search", ItemID: item.ID, Text: targetID, AppliedAt: time.Now().UnixMilli()})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare search navigation: %w", err))
		}
		if err := a.openDesktopIconSearchItemLocked(targetID); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		a.markDesktopIconReceiptApplied(input.RequestID)
		if err := a.saveDesktopIconStateLocked(); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish search navigation: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: snapshot}
	}
	if input.Action == "open_delegation" {
		if item.ID != "fixed:delegate" || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("delegation target is required"))
		}
		targetID := strings.TrimSpace(input.Values[0])
		var target *DesktopIconDelegation
		for i := range snapshot.Delegations {
			if snapshot.Delegations[i].ID == targetID {
				target = &snapshot.Delegations[i]
				break
			}
		}
		if target == nil {
			return DesktopIconActionResult{Status: "stale", Error: "委托状态已经变化", Snapshot: snapshot}
		}
		if target.SessionRef == nil || strings.TrimSpace(target.SessionRef.SessionPath) == "" {
			return a.desktopIconActionErrorLocked("retryable_error", errors.New("delegation session identity is unavailable; refresh and retry"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		receipt := desktopIconReceipt{
			RequestID: input.RequestID, Intent: intent, Status: "pending", Action: input.Action, ItemID: item.ID, Text: targetID,
			TargetKind: target.Kind, TargetScope: target.SessionRef.Scope, TargetTopicID: target.SessionRef.TopicID,
			WorkspaceRoot: target.SessionRef.WorkspaceRoot, SessionPath: target.SessionRef.SessionPath, AppliedAt: time.Now().UnixMilli(),
		}
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, receipt)
		if len(a.iconWidgetState.Applied) > desktopIconActionLimit {
			a.iconWidgetState.Applied = a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-desktopIconActionLimit:]
		}
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare delegation open: %w", err))
		}
		stored := &a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-1]
		if err := a.advanceDesktopIconDelegation(stored); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		stored.Status = "applied"
		if err := a.saveDesktopIconStateLocked(); err != nil {
			stored.Status = "pending"
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish delegation open: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "remove" && item.Kind == "person" {
		conversation := strings.TrimSpace(item.SourceID)
		if conversation == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("personal conversation is required"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		if a.iconWidgetState.DismissedConversations == nil {
			a.iconWidgetState.DismissedConversations = map[string]uint64{}
		}
		a.iconWidgetState.DismissedConversations[conversation] = max(a.iconWidgetState.DismissedConversations[conversation], item.ConversationSequence)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{
			RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "remove", ItemID: item.ID,
			Conversation: conversation, ReadSequence: item.ConversationSequence, AppliedAt: time.Now().UnixMilli(),
		})
		if len(a.iconWidgetState.Applied) > desktopIconActionLimit {
			a.iconWidgetState.Applied = a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-desktopIconActionLimit:]
		}
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare personal icon removal: %w", err))
		}
		if err := a.finishDesktopIconConversationRemoveLocked(conversation, item.ConversationSequence); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		a.markDesktopIconReceiptApplied(input.RequestID)
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.markDesktopIconReceiptPending(input.RequestID)
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish personal icon removal: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "remove" && item.Kind == "external" {
		if !slices.Contains(item.Actions, "remove") {
			return a.desktopIconActionErrorLocked("invalid", errors.New("external run does not expose remove"))
		}
		runID := strings.TrimSpace(item.SourceID)
		if runID == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("external run is required"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		if a.iconWidgetState.DismissedExternalRuns == nil {
			a.iconWidgetState.DismissedExternalRuns = map[string]uint64{}
		}
		a.iconWidgetState.DismissedExternalRuns[runID] = max(a.iconWidgetState.DismissedExternalRuns[runID], item.SourceRevision)
		delete(a.iconWidgetState.Positions, item.ID)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{
			RequestID: input.RequestID, Intent: intent, Status: "applied", Action: "remove", ItemID: item.ID, AppliedAt: time.Now().UnixMilli(),
		})
		if len(a.iconWidgetState.Applied) > desktopIconActionLimit {
			a.iconWidgetState.Applied = a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-desktopIconActionLimit:]
		}
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save external icon removal: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "open" && item.Kind == "workspace" {
		if strings.TrimSpace(item.SourceID) == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("workspace icon has no target"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{
			RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "open_workspace", ItemID: item.ID,
			WorkspaceRoot: item.SourceID, AppliedAt: time.Now().UnixMilli(),
		})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare workspace open: %w", err))
		}
		receipt := &a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-1]
		if err := a.createDesktopIconWorkspaceSessionLocked(receipt); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		if err := a.exitDesktopIconModeLocked(receipt.TabID); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		receipt.Status = "applied"
		if err := a.saveDesktopIconStateLocked(); err != nil {
			receipt.Status = "pending"
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish workspace open: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "rename" {
		if item.Kind != "task" || item.SessionRef == nil || strings.TrimSpace(item.SessionRef.SessionPath) == "" || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("task session path and non-empty title are required"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		title := strings.TrimSpace(input.Values[0])
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{
			RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "rename", ItemID: item.ID,
			SessionPath: item.SessionRef.SessionPath, Text: title, AppliedAt: time.Now().UnixMilli(),
		})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare session rename: %w", err))
		}
		if err := a.RenameSession(item.SessionRef.SessionPath, title); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		a.applyDesktopIconKeptRenameLocked(item.SessionRef.SessionPath, title)
		a.markDesktopIconReceiptApplied(input.RequestID)
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.markDesktopIconReceiptPending(input.RequestID)
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish session rename: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "randomize_icon" {
		key := desktopIconAppearanceKey(*item)
		if key == "" {
			return a.desktopIconActionErrorLocked("invalid", errors.New("only task session icons can change appearance"))
		}
		before := cloneDesktopIconState(a.iconWidgetState)
		if a.iconWidgetState.AppearanceSeeds == nil {
			a.iconWidgetState.AppearanceSeeds = map[string]string{}
		}
		a.iconWidgetState.AppearanceSeeds[key] = widgetRevision("appearance", key, input.RequestID)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "applied", Action: input.Action, ItemID: item.ID, AppliedAt: time.Now().UnixMilli()})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save session icon appearance: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}
	if input.Action == "move" {
		before := cloneDesktopIconState(a.iconWidgetState)
		if input.Position == nil || !validDesktopIconPosition(*item, *input.Position) {
			return a.desktopIconActionErrorLocked("invalid", errors.New("invalid icon position"))
		}
		reorderDesktopIconItems(snapshot.Items, item.ID, *input.Position, a.iconWidgetState.Positions)
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "applied", AppliedAt: time.Now().UnixMilli()})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			a.iconWidgetState = before
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save icon positions: %w", err))
		}
		return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
	}

	err := a.applyDesktopIconActionLocked(*item, notice, input)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err)
	}
	a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "applied", AppliedAt: time.Now().UnixMilli()})
	if len(a.iconWidgetState.Applied) > desktopIconActionLimit {
		a.iconWidgetState.Applied = a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-desktopIconActionLimit:]
	}
	if err := a.saveDesktopIconStateLocked(); err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save icon action receipt: %w", err))
	}
	return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
}

// createDesktopIconWorkspaceSessionLocked performs only the creation phase of
// a recoverable workspace open. Once TabID is persisted, every retry resumes
// the window exit against that same session and never creates another one.
// Caller holds iconWidgetMu.
func (a *App) createDesktopIconWorkspaceSessionLocked(receipt *desktopIconReceipt) error {
	scope, root := "project", strings.TrimSpace(receipt.WorkspaceRoot)
	if root == widgetWorkspaceGlobal {
		scope, root = "global", ""
	}
	meta, err := a.CreateBlankSession(CreateBlankSessionInput{Scope: scope, WorkspaceRoot: root, RequestID: receipt.RequestID})
	if err != nil {
		return fmt.Errorf("create workspace session: %w", err)
	}
	receipt.TabID = meta.ID
	if err := a.saveDesktopIconStateLocked(); err != nil {
		return fmt.Errorf("record workspace session: %w", err)
	}
	return nil
}

// OpenWidgetWorkspace opens a new ordinary blank Session in the QuickStart-
// selected workspace and exits icon mode focusing it. It reuses the same
// idempotent CreateBlankSession primitive and the same workspace routing as
// StartWidgetConversation ("auto" resolves to the current/recent workspace),
// so the workspace-icon open flow is not duplicated. requestId makes a lost
// response or failed window switch a safe idempotent retry.
func (a *App) OpenWidgetWorkspace(workspace, requestID string) (TabMeta, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		workspace = widgetWorkspaceAuto
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return TabMeta{}, errors.New("requestId is required")
	}
	route, err := resolveWidgetWorkspace(workspace, "", a.widgetWorkspaceCandidates())
	if err != nil {
		return TabMeta{}, err
	}
	a.iconWidgetMu.Lock()
	defer a.iconWidgetMu.Unlock()
	a.loadDesktopIconStateLocked()
	meta, err := a.CreateBlankSession(CreateBlankSessionInput{Scope: route.Scope, WorkspaceRoot: route.Root, RequestID: requestID})
	if err != nil {
		return TabMeta{}, fmt.Errorf("create workspace session: %w", err)
	}
	if err := a.exitDesktopIconModeLocked(meta.ID); err != nil {
		return TabMeta{}, err
	}
	return meta, nil
}

func (a *App) applyDesktopIconActionLocked(item DesktopIconItem, notice *DesktopIconNotice, input DesktopIconActionInput) error {
	switch input.Action {
	case "open":
		if item.Kind == "fixed" && (item.SourceID == "workspace" || item.SourceID == "rooms") {
			// The workspace and rooms icons open management dialogs on the
			// frontend; they must never fall through to the generic fixed
			// exit action.
			return fmt.Errorf("%s icon opens the management dialog instead of exiting", item.SourceID)
		}
		if item.Kind == "task" {
			tabID, err := a.resolveDesktopIconTaskTab(item)
			if err != nil {
				return err
			}
			return a.exitDesktopIconModeLocked(tabID)
		}
		if item.Kind == "room" {
			tabID, err := a.resolveDesktopIconRoomTab(item)
			if err != nil {
				return err
			}
			return a.exitDesktopIconModeLocked(tabID)
		}
		if item.Kind == "person" {
			if notice == nil || strings.TrimSpace(notice.Conversation) == "" {
				return errors.New("personal conversation is unavailable")
			}
			resolved, err := a.ResolveUnreadSession(notice.Conversation)
			if err != nil {
				return err
			}
			meta, err := a.ActivateTopic(resolved.Scope, resolved.WorkspaceRoot, resolved.TopicID, resolved.SessionPath)
			if err != nil {
				return err
			}
			return a.exitDesktopIconModeLocked(meta.ID)
		}
		if item.Kind == "workspace" {
			return errors.New("workspace open must use the recoverable creation path")
		}
		return a.exitDesktopIconModeLocked("")
	case "stop":
		if item.Kind != "task" {
			return errors.New("only tasks can be stopped")
		}
		a.CancelTab(item.SourceID)
		return nil
	case "cancel":
		if item.Kind != "external" || !slices.Contains(item.Actions, "cancel") {
			return errors.New("external run does not expose cancel")
		}
		_, err := a.CancelExternalRun(ExternalRunCancelInput{RunID: item.SourceID, RequestID: input.RequestID})
		return err
	case "mark_read":
		if notice == nil || notice.Conversation == "" {
			return errors.New("conversation notification is required")
		}
		_, err := a.MarkUnreadRead(MarkUnreadReadInput{ConversationKey: notice.Conversation, UpToSequence: notice.ReadSequence})
		return err
	case "reply":
		if item.Kind != "room" || notice == nil || strings.TrimSpace(notice.TabID) == "" || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
			return errors.New("Room reply text and session are required")
		}
		_, err := a.PostCollaborationMessage(PostCollaborationMessageInput{RequestID: input.RequestID, SessionID: notice.TabID, Kind: "chat", Text: strings.TrimSpace(input.Values[0])})
		return err
	case "later":
		return nil
	case "answer", "approve", "deny", "retry":
		if notice == nil {
			return errors.New("task notification is required")
		}
		message := WidgetMessage{ID: notice.ID, Revision: notice.Revision, TabID: notice.TabID, Message: notice.Body, InteractionID: notice.InteractionID, QuestionID: notice.QuestionID, Options: notice.Options}
		return a.applyWidgetActionCurrent(message, WidgetActionInput{ItemID: notice.ID, Revision: notice.Revision, RequestID: input.RequestID, Action: input.Action, Values: input.Values, Answers: input.Answers})
	case "remove":
		if item.Kind != "task" || !item.Retained {
			return errors.New("only retained task icons can be removed here")
		}
		delete(a.iconWidgetState.Kept, item.ID)
		return nil
	default:
		return fmt.Errorf("unsupported icon action %q", input.Action)
	}
}

func (a *App) applyDesktopIconPersonReplyLocked(item DesktopIconItem, notice *DesktopIconNotice, input DesktopIconActionInput, intent string) DesktopIconActionResult {
	if notice == nil || strings.TrimSpace(notice.Conversation) == "" || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
		return a.desktopIconActionErrorLocked("invalid", errors.New("personal reply text and conversation are required"))
	}
	replyKey := desktopIconReplyKey(notice.Conversation, notice.ReadSequence, input.Values[0])
	if result, found := a.resumeDesktopIconReplyLocked(replyKey); found {
		return result
	}
	resolved, err := a.ResolveUnreadSession(notice.Conversation)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err)
	}
	meta, err := a.ActivateTopic(resolved.Scope, resolved.WorkspaceRoot, resolved.TopicID, resolved.SessionPath)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err)
	}
	ctrl := a.ctrlByTabID(meta.ID)
	if ctrl == nil {
		return a.desktopIconActionErrorLocked("retryable_error", errors.New("personal conversation controller is not ready"))
	}
	base := desktopIconUserMessages(ctrl.History())
	receipt := desktopIconReceipt{
		RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "reply", ItemID: item.ID,
		TabID: meta.ID, Conversation: notice.Conversation, ReadSequence: notice.ReadSequence,
		Text: strings.TrimSpace(input.Values[0]), ReplyKey: replyKey, BaseUserTurns: len(base), AppliedAt: time.Now().UnixMilli(),
	}
	a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, receipt)
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState.Applied = a.iconWidgetState.Applied[:len(a.iconWidgetState.Applied)-1]
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare personal reply: %w", err))
	}
	stored := &a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-1]
	progress, err := a.advanceDesktopIconPersonReply(stored)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err)
	}
	applyDesktopIconReplyProgress(stored, progress)
	if err := a.saveDesktopIconStateLocked(); err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save personal reply progress: %w", err))
	}
	return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
}

func (a *App) applyDesktopIconTaskContinueLocked(item DesktopIconItem, notice *DesktopIconNotice, input DesktopIconActionInput, intent string) DesktopIconActionResult {
	if item.Kind != "task" || notice == nil || (notice.Kind != "completed" && notice.Kind != "failed") || len(input.Values) != 1 || strings.TrimSpace(input.Values[0]) == "" {
		return a.desktopIconActionErrorLocked("invalid", errors.New("completed task and continuation text are required"))
	}
	tabID := firstNonEmpty(strings.TrimSpace(notice.TabID), strings.TrimSpace(item.SourceID))
	ctrl := a.ctrlByTabID(tabID)
	if ctrl == nil {
		// A completed task's tab can be closed or pruned while its icon stays
		// visible. Reopen the exact session from the snapshot ref — the same
		// identity the open action uses — so continuing never depends on a live
		// tab or on which tab happens to be active.
		resolved, err := a.resolveDesktopIconTaskTab(item)
		if err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", err)
		}
		tabID = resolved
		ctrl = a.ctrlByTabID(tabID)
	}
	if ctrl == nil {
		return a.desktopIconActionErrorLocked("retryable_error", errDesktopIconTaskControllerNotReady)
	}
	ref := item.SessionRef
	var scope, workspaceRoot, topicID, sessionPath string
	if ref != nil {
		scope, workspaceRoot, topicID, sessionPath = ref.Scope, ref.WorkspaceRoot, ref.TopicID, ref.SessionPath
	}
	receipt := desktopIconReceipt{
		RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "continue", ItemID: item.ID,
		TabID: tabID, Scope: scope, WorkspaceRoot: workspaceRoot, TopicID: topicID, SessionPath: sessionPath,
		Text: strings.TrimSpace(input.Values[0]), BaseUserTurns: len(desktopIconUserMessages(ctrl.History())), AppliedAt: time.Now().UnixMilli(),
	}
	a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, receipt)
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState.Applied = a.iconWidgetState.Applied[:len(a.iconWidgetState.Applied)-1]
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare task continuation: %w", err))
	}
	stored := &a.iconWidgetState.Applied[len(a.iconWidgetState.Applied)-1]
	progress, err := a.advanceDesktopIconTaskContinue(stored)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err)
	}
	applyDesktopIconReplyProgress(stored, progress)
	if err := a.saveDesktopIconStateLocked(); err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("save task continuation progress: %w", err))
	}
	return DesktopIconActionResult{Status: "accepted", Snapshot: a.desktopIconSnapshotLocked()}
}

func (a *App) resumeDesktopIconReplyLocked(replyKey string) (DesktopIconActionResult, bool) {
	i := desktopIconReplyReceiptIndex(a.iconWidgetState.Applied, replyKey)
	if i < 0 {
		return DesktopIconActionResult{}, false
	}
	receipt := &a.iconWidgetState.Applied[i]
	if receipt.Status == "applied" {
		return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}, true
	}
	progress, err := a.advanceDesktopIconPersonReply(receipt)
	if err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", err), true
	}
	applyDesktopIconReplyProgress(receipt, progress)
	if err := a.saveDesktopIconStateLocked(); err != nil {
		return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("finish prior personal reply: %w", err)), true
	}
	status := "accepted"
	if progress == desktopIconReplyConfirmed {
		status = "already_applied"
	}
	return DesktopIconActionResult{Status: status, Snapshot: a.desktopIconSnapshotLocked()}, true
}

type desktopIconReplyProgress string

const (
	desktopIconReplyAccepted  desktopIconReplyProgress = "accepted"
	desktopIconReplyConfirmed desktopIconReplyProgress = "confirmed"
)

func applyDesktopIconReplyProgress(receipt *desktopIconReceipt, progress desktopIconReplyProgress) {
	if progress == desktopIconReplyConfirmed {
		receipt.Status = "applied"
	}
}

type desktopIconReplyStep string

const (
	desktopIconReplyWaitStep    desktopIconReplyStep = "wait"
	desktopIconReplySubmitStep  desktopIconReplyStep = "submit"
	desktopIconReplyConfirmStep desktopIconReplyStep = "confirm"
)

// errDesktopIconTaskControllerNotReady marks a continuation recovery that is
// waiting on a session controller that has not been established yet (startup
// before the tab/controller is restored). It is a deferral, not a failure: the
// receipt stays pending and the next snapshot retries. The direct action path
// surfaces it to the frontend as a retryable error, where the user can retry.
var errDesktopIconTaskControllerNotReady = errors.New("completed task controller is not ready")

func (a *App) advanceDesktopIconPersonReply(receipt *desktopIconReceipt) (desktopIconReplyProgress, error) {
	ctrl := a.ctrlByTabID(receipt.TabID)
	if ctrl == nil && receipt.Conversation != "" {
		resolved, err := a.ResolveUnreadSession(receipt.Conversation)
		if err != nil {
			return "", err
		}
		meta, err := a.ActivateTopic(resolved.Scope, resolved.WorkspaceRoot, resolved.TopicID, resolved.SessionPath)
		if err != nil {
			return "", err
		}
		receipt.TabID = meta.ID
		ctrl = a.ctrlByTabID(meta.ID)
	}
	if ctrl == nil {
		return "", errors.New("personal conversation controller is not ready")
	}
	users := desktopIconUserMessages(ctrl.History())
	step, err := desktopIconReplyNextStep(users, receipt.BaseUserTurns, receipt.Text, receipt.Delivery, ctrl.Running())
	if err != nil {
		return "", err
	}
	switch step {
	case desktopIconReplyWaitStep:
		return desktopIconReplyAccepted, nil
	case desktopIconReplySubmitStep:
		accepted, err := a.tryDesktopIconReply(receipt.TabID, receipt.Text)
		if err != nil {
			return "", fmt.Errorf("send personal reply: %w", err)
		}
		if !accepted {
			return "", errors.New("personal conversation is busy; reply remains pending and can be retried")
		}
		receipt.Delivery = string(desktopIconReplyAccepted)
		return desktopIconReplyAccepted, nil
	case desktopIconReplyConfirmStep:
		if _, err := a.MarkUnreadRead(MarkUnreadReadInput{ConversationKey: receipt.Conversation, UpToSequence: receipt.ReadSequence}); err != nil {
			return "", fmt.Errorf("advance personal reply unread watermark: %w", err)
		}
		return desktopIconReplyConfirmed, nil
	default:
		return "", errors.New("invalid personal reply recovery step")
	}
}

func (a *App) advanceDesktopIconTaskContinue(receipt *desktopIconReceipt) (desktopIconReplyProgress, error) {
	ctrl := a.ctrlByTabID(receipt.TabID)
	if ctrl == nil && strings.TrimSpace(receipt.SessionPath) != "" {
		// The tab backing a completed task can be closed or absent after a
		// restart. Reopen the recorded session identity so recovery finishes
		// instead of surfacing "controller is not ready" on every snapshot.
		meta, err := a.OpenTopicSession(receipt.Scope, receipt.WorkspaceRoot, receipt.TopicID, receipt.SessionPath)
		if err != nil {
			return "", fmt.Errorf("reopen task session: %w", err)
		}
		receipt.TabID = meta.ID
		ctrl = a.ctrlByTabID(meta.ID)
	}
	if ctrl == nil {
		return "", errDesktopIconTaskControllerNotReady
	}
	users := desktopIconUserMessages(ctrl.History())
	step, err := desktopIconTurnNextStep(users, receipt.BaseUserTurns, receipt.Text, receipt.Delivery, ctrl.Running(), "task conversation")
	if err != nil {
		return "", err
	}
	switch step {
	case desktopIconReplyWaitStep:
		return desktopIconReplyAccepted, nil
	case desktopIconReplySubmitStep:
		accepted, err := a.tryDesktopIconReply(receipt.TabID, receipt.Text)
		if err != nil {
			return "", fmt.Errorf("continue completed task: %w", err)
		}
		if !accepted {
			return "", errors.New("task conversation is busy; continuation remains pending and can be retried")
		}
		receipt.Delivery = string(desktopIconReplyAccepted)
		return desktopIconReplyAccepted, nil
	case desktopIconReplyConfirmStep:
		return desktopIconReplyConfirmed, nil
	default:
		return "", errors.New("invalid task continuation recovery step")
	}
}

type desktopIconTurnSubmitter interface {
	TrySubmitUserTurn(input, display string) bool
}

func (a *App) tryDesktopIconReply(tabID, text string) (bool, error) {
	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab != nil && tab.ReadOnly {
		return false, readOnlyChannelErr()
	}
	if err := a.applyPendingModelForTab(tab); err != nil {
		return false, err
	}
	tab, ctrl = a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return false, workspaceNotReadyErr(tab)
	}
	if err := a.ensureTabControllerWorkspace(tab); err != nil {
		return false, err
	}
	ctrl = tab.Ctrl
	if ctrl == nil {
		return false, workspaceNotReadyErr(tab)
	}
	if err := a.takeoverFromCLI(tab); err != nil {
		return false, err
	}
	submitter, ok := ctrl.(desktopIconTurnSubmitter)
	if !ok {
		return false, errors.New("controller does not support acknowledged user turns")
	}
	if !submitter.TrySubmitUserTurn(text, text) {
		return false, nil
	}
	a.ensureTabTopicIndexedForUserTurn(tab)
	a.maybeAutoTitleTopicFromText(tab, text)
	a.emitProjectTreeChanged()
	return true, nil
}

func desktopIconReplyNextStep(users []string, base int, text, delivery string, running bool) (desktopIconReplyStep, error) {
	return desktopIconTurnNextStep(users, base, text, delivery, running, "personal conversation")
}

func desktopIconTurnNextStep(users []string, base int, text, delivery string, running bool, context string) (desktopIconReplyStep, error) {
	if len(users) > base {
		// The controller prepends transient blocks (response-language, goal,
		// plan-marker, memory-update, …) to every submitted turn, so the stored
		// message never equals the raw continuation text byte-for-byte. Compare
		// against the user-authored part instead of failing the confirmation.
		if strings.TrimSpace(agent.StripTransientUserBlocks(users[base])) == strings.TrimSpace(text) {
			return desktopIconReplyConfirmStep, nil
		}
		return "", fmt.Errorf("%s changed while recovering the user turn; retry was stopped to avoid a duplicate", context)
	}
	if len(users) < base {
		return "", fmt.Errorf("%s history moved backwards; user-turn recovery is unsafe", context)
	}
	if delivery == string(desktopIconReplyAccepted) && running {
		return desktopIconReplyWaitStep, nil
	}
	return desktopIconReplySubmitStep, nil
}

func desktopIconUserMessages(history []provider.Message) []string {
	out := make([]string, 0, len(history))
	for _, message := range history {
		if message.Role == provider.RoleUser {
			out = append(out, strings.TrimSpace(message.Content))
		}
	}
	return out
}

func (a *App) finishDesktopIconCompletionLocked(input DesktopIconActionInput, notice *DesktopIconNotice, fallbackTabID, sessionPath, conversation string, readSequence uint64) error {
	tabID := fallbackTabID
	if notice != nil {
		tabID = notice.TabID
	}
	if tabID == "" && strings.HasPrefix(input.ItemID, "task:") {
		tabID = strings.TrimPrefix(input.ItemID, "task:")
	}
	if tabID == "" {
		return errors.New("completion task is unavailable")
	}
	sessionPath = strings.TrimSpace(sessionPath)
	tab := a.tabByID(tabID)
	if tab == nil {
		// A retained task is only rendered while its tab is unloaded, so
		// dismissing its completion card must not depend on a live tab. The
		// persisted attention flag is keyed by session path, which the receipt
		// carries; clear it directly instead of failing until the tab reloads.
		if sessionPath == "" {
			return errors.New("completion task is not loaded yet")
		}
		if err := clearNeedsAttention(sessionPath); err != nil {
			return err
		}
	} else if err := clearTabAttention(tab); err != nil {
		return err
	}
	if conversation != "" && readSequence > 0 {
		if _, err := a.MarkUnreadRead(MarkUnreadReadInput{ConversationKey: conversation, UpToSequence: readSequence}); err != nil {
			return fmt.Errorf("advance completion unread watermark: %w", err)
		}
	}
	return nil
}

func (a *App) markDesktopIconReceiptApplied(requestID string) {
	for i := range a.iconWidgetState.Applied {
		if a.iconWidgetState.Applied[i].RequestID == requestID {
			a.iconWidgetState.Applied[i].Status = "applied"
			return
		}
	}
}

func (a *App) markDesktopIconReceiptPending(requestID string) {
	for i := range a.iconWidgetState.Applied {
		if a.iconWidgetState.Applied[i].RequestID == requestID {
			a.iconWidgetState.Applied[i].Status = "pending"
			return
		}
	}
}

// finishDesktopIconConversationRemoveLocked advances only the watermark that
// was visible when the remove action was accepted. MarkRead is monotonic and
// idempotent; a late message with a larger sequence remains unread and is not
// suppressed by the persisted dismissed watermark.
func (a *App) finishDesktopIconConversationRemoveLocked(conversation string, sequence uint64) error {
	if sequence == 0 {
		return nil
	}
	if _, err := a.MarkUnreadRead(MarkUnreadReadInput{ConversationKey: conversation, UpToSequence: sequence}); err != nil {
		return fmt.Errorf("advance removed personal conversation watermark: %w", err)
	}
	return nil
}

func (a *App) desktopIconActionErrorLocked(status string, err error) DesktopIconActionResult {
	return DesktopIconActionResult{Status: status, Error: err.Error(), Snapshot: a.desktopIconSnapshotLocked()}
}
