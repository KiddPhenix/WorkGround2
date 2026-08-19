package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/fileutil"
	"workground2/internal/provider"
	"workground2/internal/unread"
)

const (
	desktopIconActionLimit = 128
	desktopIconMaxRooms    = 6
	desktopIconMaxTasks    = 8
	desktopIconMaxSpaces   = 5
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
	ID            string         `json:"id"`
	Revision      string         `json:"revision"`
	Kind          string         `json:"kind"`
	Priority      int            `json:"priority"`
	Title         string         `json:"title"`
	Body          string         `json:"body"`
	CreatedAt     int64          `json:"createdAt"`
	TabID         string         `json:"tabId,omitempty"`
	Conversation  string         `json:"conversation,omitempty"`
	ReadSequence  uint64         `json:"readSequence,omitempty"`
	InteractionID string         `json:"interactionId,omitempty"`
	QuestionID    string         `json:"questionId,omitempty"`
	Options       []WidgetOption `json:"options"`
	Retryable     bool           `json:"retryable,omitempty"`
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
func (a *App) SetDesktopIconHitRegions(rects []DesktopIconRect) error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	if !a.widgetMode || a.widgetStyle != "icons" {
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
	return setDesktopIconHitRegions(rects)
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
	ID            string              `json:"id"`
	Kind          string              `json:"kind"`
	SourceID      string              `json:"sourceId"`
	Title         string              `json:"title"`
	Subtitle      string              `json:"subtitle,omitempty"`
	Icon          string              `json:"icon,omitempty"`
	Status        string              `json:"status"`
	UnreadCount   int                 `json:"unreadCount"`
	ActivityCount int                 `json:"activityCount,omitempty"`
	Notifications []DesktopIconNotice `json:"notifications"`
	Runtime       *DesktopIconRuntime `json:"runtimeStatus,omitempty"`
	Position      DesktopIconPosition `json:"position"`
	Revision      string              `json:"revision"`
	Retained      bool                `json:"retained,omitempty"`
	SessionRef    *DesktopIconTaskRef `json:"sessionRef,omitempty"`
}

type DesktopIconSnapshot struct {
	Items              []DesktopIconItem `json:"items"`
	Revision           string            `json:"revision"`
	HoverStatusDelayMs int               `json:"hoverStatusDelayMs"`
	Style              string            `json:"style"`
	UnreadRevision     uint64            `json:"unreadRevision"`
	Error              string            `json:"error,omitempty"`
}

type DesktopIconActionInput struct {
	ItemID       string               `json:"itemId"`
	NoticeID     string               `json:"noticeId,omitempty"`
	Revision     string               `json:"revision"`
	RequestID    string               `json:"requestId"`
	Action       string               `json:"action"`
	Values       []string             `json:"values,omitempty"`
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
	Order     int    `json:"order"`
	Revision  string `json:"revision"`
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
	Applied   []desktopIconReceipt           `json:"applied,omitempty"`
	// CompletionSummaries caches LLM news-style summaries keyed by
	// desktopIconCompletionKey; entries survive restarts so already-generated
	// summaries are never regenerated and failed ones retry on backoff.
	CompletionSummaries map[string]desktopIconCompletionSummary `json:"completionSummaries,omitempty"`
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
	a.iconWidgetState = desktopIconPersistedState{
		Positions:           map[string]DesktopIconPosition{},
		Kept:                map[string]desktopIconKept{},
		CompletionSummaries: map[string]desktopIconCompletionSummary{},
	}
	raw, err := readFileUTF8(desktopIconStatePath())
	if err == nil {
		if err := json.Unmarshal(raw, &a.iconWidgetState); err != nil {
			a.iconWidgetStateErr = fmt.Errorf("load desktop icon state: %w", err)
			a.iconWidgetState = desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		a.iconWidgetStateErr = fmt.Errorf("load desktop icon state: %w", err)
	}
	if a.iconWidgetState.Positions == nil {
		a.iconWidgetState.Positions = map[string]DesktopIconPosition{}
	}
	if a.iconWidgetState.Kept == nil {
		a.iconWidgetState.Kept = map[string]desktopIconKept{}
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
	summary := ""
	if tab.Ctrl != nil {
		summary = conciseWidgetText(lastWidgetAssistantText(tab.Ctrl.History()), 120)
	}
	entry := desktopIconKept{
		ItemID:        id,
		SourceID:      meta.ID,
		SessionID:     strings.TrimSpace(meta.SessionID),
		Title:         firstNonEmpty(strings.TrimSpace(meta.SessionDisplayTitle), strings.TrimSpace(meta.TopicTitle), "当前任务"),
		Summary:       summary,
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

func (a *App) desktopIconSnapshotLocked() DesktopIconSnapshot {
	recoveryErr := a.recoverDesktopIconActionsLocked()
	sources := a.widgetSources()
	// Completion summaries are generated asynchronously: only the request
	// collection and goroutine start happen under iconWidgetMu, and the
	// network calls run inside the goroutines, never under an App/widget lock.
	a.maybeGenerateCompletionSummariesLocked(a.completionSummaryRequestsLocked(sources))
	// Subagent metadata scanning happens after widgetSources released a.mu,
	// so file I/O never runs under the App's main lock.
	subagentCounts, subagentErr := a.widgetSubagentCounts(sources)
	unreadState := a.UnreadState()
	spaces := a.ListWidgetWorkspaces()
	style, hover := a.desktopIconPreferences()
	roomSummaries := a.desktopRoomSummaries()
	snapshot := buildDesktopIconSnapshot(sources, unreadState, spaces, a.iconWidgetState, hover, roomSummaries, subagentCounts)
	if a.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		// The snapshot just pinned brand-new task icons, so rebuild once: the
		// current response must already reflect the pinned (stable) orders,
		// otherwise the very first render would still use the ephemeral ones.
		snapshot = buildDesktopIconSnapshot(sources, unreadState, spaces, a.iconWidgetState, hover, roomSummaries, subagentCounts)
	}
	snapshot.Style = style
	if recoveryErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, recoveryErr.Error())
	}
	if subagentErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, subagentErr.Error())
	}
	if a.iconWidgetStateErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, a.iconWidgetStateErr.Error())
	}
	if a.iconWidgetWindowErr != nil {
		snapshot.Error = firstNonEmpty(snapshot.Error, a.iconWidgetWindowErr.Error())
	}
	return snapshot
}

// pinNewDesktopIconTaskOrdersLocked durably pins the running-zone order of
// every live task icon that has never been dragged. Without a persisted
// position a task icon's order is re-derived from the (map-iteration) order of
// widgetSources on every snapshot, so a running icon visibly jumps between
// refreshes. New tasks are appended after the current running zone — never
// inserted at the front — so existing icons (including running ones) never
// move. Retained icons already carry a stable kept.Order and are skipped. It
// is idempotent: already-pinned icons are skipped, and a failed save rolls
// back so the next snapshot retries.
func (a *App) pinNewDesktopIconTaskOrdersLocked(snapshot DesktopIconSnapshot) bool {
	next := 0
	// The tail is the max order of already-durable icons: persisted positions
	// (user-dragged) and retained kept icons. Ephemeral snapshot orders for
	// unpinned tasks are map-iteration noise and must not move the tail.
	for _, item := range snapshot.Items {
		if item.Position.Row != "bottom" || item.Position.Zone != "running" {
			continue
		}
		durable := item.Retained
		if _, ok := a.iconWidgetState.Positions[item.ID]; ok {
			durable = true
		}
		if durable && item.Position.Order >= next {
			next = item.Position.Order + 1
		}
	}
	// Orders persisted for capacity-capped icons stay reserved so a
	// reappearing icon never collides with a freshly pinned one.
	for _, position := range a.iconWidgetState.Positions {
		if position.Row == "bottom" && position.Zone == "running" && position.Order >= next {
			next = position.Order + 1
		}
	}
	before := cloneDesktopIconState(a.iconWidgetState)
	changed := false
	for _, item := range snapshot.Items {
		if item.Kind != "task" || item.Retained {
			continue
		}
		if _, ok := a.iconWidgetState.Positions[item.ID]; ok {
			continue
		}
		a.iconWidgetState.Positions[item.ID] = DesktopIconPosition{Row: "bottom", Zone: "running", Order: next}
		next++
		changed = true
	}
	if !changed {
		return false
	}
	if err := a.saveDesktopIconStateLocked(); err != nil {
		a.iconWidgetState = before
		a.iconWidgetStateErr = fmt.Errorf("pin desktop icon order: %w", err)
		return false
	}
	return true
}

func (a *App) desktopRoomSummaries() map[string]string {
	out := map[string]string{}
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
			if strings.TrimSpace(key) != "" {
				out[key] = text
			}
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
		for i := len(runtime.state.Snapshot.Timeline) - 1; i >= 0; i-- {
			chat := runtime.state.Snapshot.Timeline[i].Chat
			if chat != nil && chat.AuthorID != runtime.state.MemberID && chat.AuthorID != runtime.state.AgentID && strings.TrimSpace(chat.Text) != "" {
				out[sessionID] = conciseWidgetText(chat.Text, 100)
				break
			}
		}
		runtime.mu.RUnlock()
	}
	return out
}

func (a *App) recoverDesktopIconActionsLocked() error {
	var recoveryErr error
	changed := false
	for i := range a.iconWidgetState.Applied {
		receipt := &a.iconWidgetState.Applied[i]
		if receipt.Status != "pending" {
			continue
		}
		if receipt.Action == "continue" && receipt.Text != "" {
			beforeDelivery := receipt.Delivery
			progress, err := a.advanceDesktopIconTaskContinue(receipt)
			if err != nil {
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
		if receipt.Action != "ok" && receipt.Action != "dismiss" {
			continue
		}
		if receipt.TabID == "" {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon action %s: task id is missing", receipt.RequestID))
			continue
		}
		if err := a.finishDesktopIconCompletionLocked(DesktopIconActionInput{ItemID: receipt.ItemID}, nil, receipt.TabID, receipt.Conversation, receipt.ReadSequence); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("recover icon action %s: %w", receipt.RequestID, err))
			continue
		}
		receipt.Status = "applied"
		changed = true
	}
	if changed {
		if err := a.saveDesktopIconStateLocked(); err != nil {
			recoveryErr = errors.Join(recoveryErr, fmt.Errorf("save recovered icon actions: %w", err))
		}
	}
	return recoveryErr
}

func (a *App) desktopIconPreferences() (string, int) {
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return "icons", 1200
	}
	return cfg.DesktopWidgetStyle(), cfg.DesktopHoverStatusDelayMs()
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

func buildDesktopIconSnapshot(sources []widgetSource, unreadState UnreadState, spaces []WidgetWorkspaceOption, persisted desktopIconPersistedState, hover int, roomSummaries map[string]string, subagentCounts map[widgetSubagentKey]int) DesktopIconSnapshot {
	snapshot := DesktopIconSnapshot{HoverStatusDelayMs: hover, UnreadRevision: unreadState.Summary.Revision}
	items := make([]DesktopIconItem, 0, len(sources)+len(spaces)+8)
	taskBySource := map[string]int{}
	delegatedRunning := 0
	spaceCount := 0
	for _, space := range spaces {
		if space.Scope != "auto" && spaceCount < desktopIconMaxSpaces {
			spaceCount++
		}
	}
	taskLimit := min(desktopIconMaxTasks, max(1, (desktopIconWidth-36)/68-4-spaceCount))
	taskCount := 0

	for _, source := range sources {
		meta := source.meta
		if strings.EqualFold(meta.SessionSource, "cli") || meta.SessionKind == "collaboration" {
			continue
		}
		// Real running sub-agents owned by this session are the authoritative
		// delegation signal. Foreground parents keep their own task icon below
		// and still contribute their running sub-agents to the fixed entry.
		realRunning := subagentCounts[newWidgetSubagentKey(source.sessionDir, source.branchID)]
		if meta.BackgroundOnly {
			// The legacy compatibility path counts the background tab itself
			// as the delegated work. When the same source owns real running
			// sub-agents, those are authoritative and count instead, so a
			// source is never double counted by both signals — even when the
			// tab's own turn has already ended (RunningWork=false).
			delegatedRunning += realRunning
			if realRunning == 0 && meta.RunningWork {
				delegatedRunning++
			}
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
		for _, key := range []string{meta.ID, meta.SessionID, meta.WorkID} {
			if key = strings.TrimSpace(key); key != "" {
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
		if _, live := taskBySource[kept.SourceID]; live {
			continue
		}
		items = append(items, DesktopIconItem{
			ID: kept.ItemID, Kind: "task", SourceID: kept.SourceID, Title: kept.Title,
			Subtitle: kept.Summary, Status: "done", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: kept.Order},
			Revision: kept.Revision, Retained: true, Notifications: []DesktopIconNotice{},
			SessionRef: desktopIconTaskRef(kept.Scope, kept.WorkspaceRoot, kept.TopicID, kept.SessionPath),
		})
		taskCount++
	}

	roomCount := 0
	for _, conversation := range unreadState.Summary.Conversations {
		if index, ok := desktopTaskIndex(taskBySource, conversation); ok && conversation.UnreadCount > 0 {
			// Task attention is projected from Controller/BranchMeta. The unread
			// store supplies only its durable watermark so the same completion or
			// prompt is not rendered and counted twice.
			if len(items[index].Notifications) > 0 {
				items[index].Notifications[0].Conversation = conversation.Key
				items[index].Notifications[0].ReadSequence = conversation.LatestSequence
			}
			continue
		}
		if conversation.Source != unread.SourceRoom && conversation.Source != unread.SourceIM {
			continue
		}
		if roomCount >= desktopIconMaxRooms {
			continue
		}
		kind := "room"
		if conversation.Source == unread.SourceIM {
			kind = "person"
		}
		item := DesktopIconItem{
			ID: "conversation:" + conversation.Key, Kind: kind, SourceID: conversation.Key,
			Title: firstNonEmpty(strings.TrimSpace(conversation.Title), "消息"), Status: "unread",
			Notifications: noticesForConversation(conversation, roomSummaries[conversation.SessionID]),
			Position:      DesktopIconPosition{Row: "top", Zone: "conversation", Order: roomCount},
		}
		items = append(items, item)
		roomCount++
	}

	for i, space := range spaces {
		if i >= desktopIconMaxSpaces || space.Scope == "auto" {
			continue
		}
		id := "workspace:" + firstNonEmpty(space.Root, space.Scope)
		items = append(items, DesktopIconItem{
			ID: id, Kind: "workspace", SourceID: firstNonEmpty(space.Root, space.Scope), Title: space.Name,
			Status: "idle", Notifications: []DesktopIconNotice{},
			Position: DesktopIconPosition{Row: "bottom", Zone: "workspace", Order: i},
		})
	}

	// The fixed bottom bar is the declared order of the stable source ids:
	// 新建 → 工作区 → Rooms → 委托 → 搜索. Position.Order is derived from
	// this slice index, never from map iteration, so the bar order is a Go
	// contract.
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
		sortDesktopIconNotices(item.Notifications)
		item.UnreadCount = len(item.Notifications)
		if item.UnreadCount > 0 {
			item.Status = desktopIconStatus(item.Notifications[0].Kind, item.Status)
		}
		item.Revision = desktopIconItemRevision(*item)
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

func desktopTaskItem(source widgetSource, order int, summaries map[string]desktopIconCompletionSummary) DesktopIconItem {
	meta := source.meta
	title := firstNonEmpty(strings.TrimSpace(meta.SessionDisplayTitle), strings.TrimSpace(meta.TopicTitle), "当前任务")
	item := DesktopIconItem{
		ID: "task:" + meta.ID, Kind: "task", SourceID: meta.ID, Title: title,
		Subtitle: firstNonEmpty(strings.TrimSpace(meta.WorkspaceName), "WorkGround2"), Status: "idle",
		Notifications: []DesktopIconNotice{}, Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: order},
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
		body := conciseWidgetText(source.resultText, 140)
		if body == "" {
			body = "任务已完成，记录仍可在搜索中找到。"
		}
		body, summaryStatus := desktopIconCompletionSummaryFor(summaries, desktopIconCompletionKey(meta.ID, "completed", meta.NeedsAttentionAt), body)
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

func desktopNoticeForMessage(message WidgetMessage, kind string, priority int, at int64) DesktopIconNotice {
	return DesktopIconNotice{
		ID: message.ID, Revision: message.Revision, Kind: kind, Priority: priority,
		Title: message.StateLabel, Body: message.Message, CreatedAt: at, TabID: message.TabID,
		InteractionID: message.InteractionID, QuestionID: message.QuestionID,
		Options: append([]WidgetOption(nil), message.Options...), Retryable: message.Kind == "error",
	}
}

func noticesForConversation(conversation unread.Conversation, summary string) []DesktopIconNotice {
	out := make([]DesktopIconNotice, 0, len(conversation.Items))
	for _, item := range conversation.Items {
		kind, priority := "message", 3
		if item.Priority == unread.PriorityHigh {
			kind, priority = "needs_input", 1
		}
		body := strings.TrimSpace(summary)
		if body == "" {
			body = "收到一条新消息（摘要需在完整会话中查看）"
		}
		out = append(out, DesktopIconNotice{
			ID: item.ID, Revision: strconv.FormatUint(item.Sequence, 10), Kind: kind, Priority: priority,
			Title: firstNonEmpty(conversation.Title, "新消息"), Body: body, TabID: conversation.SessionID,
			CreatedAt: item.OccurredAt.UnixMilli(), Conversation: conversation.Key, ReadSequence: item.Sequence,
			Options: []WidgetOption{},
		})
	}
	return out
}

func desktopTaskIndex(index map[string]int, conversation unread.Conversation) (int, bool) {
	for _, key := range []string{conversation.SessionID, strings.TrimPrefix(conversation.SessionID, "path:")} {
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

func desktopIconItemRevision(item DesktopIconItem) string {
	parts := []string{item.ID, item.Status, strconv.Itoa(item.Position.Order), item.Position.Row, item.Position.Zone}
	if item.Runtime != nil {
		parts = append(parts, item.Runtime.Phase)
	}
	if item.SessionRef != nil {
		parts = append(parts, item.SessionRef.Scope, item.SessionRef.WorkspaceRoot, item.SessionRef.TopicID, item.SessionRef.SessionPath)
	}
	for _, notice := range item.Notifications {
		parts = append(parts, notice.ID, notice.Revision)
	}
	return widgetRevision(parts...)
}

func desktopIconSnapshotRevision(snapshot DesktopIconSnapshot) string {
	parts := []string{strconv.Itoa(snapshot.HoverStatusDelayMs), strconv.FormatUint(snapshot.UnreadRevision, 10)}
	for _, item := range snapshot.Items {
		parts = append(parts, item.ID, item.Revision)
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
	case "task":
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
		Positions:           make(map[string]DesktopIconPosition, len(state.Positions)),
		Kept:                make(map[string]desktopIconKept, len(state.Kept)),
		Applied:             append([]desktopIconReceipt(nil), state.Applied...),
		CompletionSummaries: make(map[string]desktopIconCompletionSummary, len(state.CompletionSummaries)),
	}
	for id, position := range state.Positions {
		clone.Positions[id] = position
	}
	for id, kept := range state.Kept {
		clone.Kept[id] = kept
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
	raw, _ := json.Marshal(struct {
		Item, Notice, Revision, Action string
		Values                         []string
		Position                       *DesktopIconPosition
		Conversation                   string
		Read                           uint64
	}{
		input.ItemID, input.NoticeID, input.Revision, input.Action, input.Values, input.Position, input.Conversation, input.ReadSequence,
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
					return a.desktopIconActionErrorLocked("retryable_error", err)
				}
				return DesktopIconActionResult{Status: "already_applied", Snapshot: a.desktopIconSnapshotLocked()}
			}
			if input.Action != "ok" && input.Action != "dismiss" {
				return a.desktopIconActionErrorLocked("retryable_error", errors.New("action receipt is pending recovery"))
			}
			if err := a.finishDesktopIconCompletionLocked(input, nil, receipt.TabID, receipt.Conversation, receipt.ReadSequence); err != nil {
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
		a.iconWidgetState.Applied = append(a.iconWidgetState.Applied, desktopIconReceipt{RequestID: input.RequestID, Intent: intent, Status: "pending", Action: input.Action, ItemID: item.ID, TabID: notice.TabID, Conversation: notice.Conversation, ReadSequence: notice.ReadSequence, AppliedAt: time.Now().UnixMilli()})
		if err := a.saveDesktopIconStateLocked(); err != nil {
			return a.desktopIconActionErrorLocked("retryable_error", fmt.Errorf("prepare completion action: %w", err))
		}
		if err := a.finishDesktopIconCompletionLocked(input, notice, notice.TabID, notice.Conversation, notice.ReadSequence); err != nil {
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
			tabID := a.desktopIconTabID(noticeTabID(notice))
			if tabID == "" {
				return errors.New("Room session is not loaded yet")
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
			tabID, err := a.activateDesktopIconWorkspace(item.SourceID)
			if err != nil {
				return err
			}
			return a.exitDesktopIconModeLocked(tabID)
		}
		return a.exitDesktopIconModeLocked("")
	case "stop":
		if item.Kind != "task" {
			return errors.New("only tasks can be stopped")
		}
		a.CancelTab(item.SourceID)
		return nil
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
		return a.applyWidgetActionCurrent(message, WidgetActionInput{ItemID: notice.ID, Revision: notice.Revision, RequestID: input.RequestID, Action: input.Action, Values: input.Values})
	case "remove":
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
		return a.desktopIconActionErrorLocked("retryable_error", errors.New("completed task controller is not ready"))
	}
	receipt := desktopIconReceipt{
		RequestID: input.RequestID, Intent: intent, Status: "pending", Action: "continue", ItemID: item.ID,
		TabID: tabID, Text: strings.TrimSpace(input.Values[0]), BaseUserTurns: len(desktopIconUserMessages(ctrl.History())), AppliedAt: time.Now().UnixMilli(),
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
	if ctrl == nil {
		return "", errors.New("completed task controller is not ready")
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
		if strings.TrimSpace(users[base]) == strings.TrimSpace(text) {
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

func (a *App) desktopIconTabID(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil {
			continue
		}
		path := strings.TrimSpace(tab.currentSessionPath())
		if tab.ID == sessionID || tab.SessionID == sessionID || path == sessionID || "path:"+path == sessionID {
			return tab.ID
		}
	}
	return ""
}

func (a *App) finishDesktopIconCompletionLocked(input DesktopIconActionInput, notice *DesktopIconNotice, fallbackTabID, conversation string, readSequence uint64) error {
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
	tab := a.tabByID(tabID)
	if tab == nil {
		return errors.New("completion task is not loaded yet")
	}
	if err := clearTabAttention(tab); err != nil {
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

func noticeTabID(notice *DesktopIconNotice) string {
	if notice == nil {
		return ""
	}
	return notice.TabID
}

func (a *App) desktopIconActionErrorLocked(status string, err error) DesktopIconActionResult {
	return DesktopIconActionResult{Status: status, Error: err.Error(), Snapshot: a.desktopIconSnapshotLocked()}
}
