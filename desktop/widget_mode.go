package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/fileutil"
	"workground2/internal/provider"
)

const (
	widgetDefaultWidth  = 590
	widgetDefaultHeight = 176
	widgetMinWidth      = 520
	widgetMinHeight     = 160
	widgetMaxWidth      = 900
	widgetMaxHeight     = 220
	widgetEdgeGap       = 16
	widgetBottomGap     = 24
	widgetActionLimit   = 64

	// legacyDefaultHeight is the old default height before the 142→176 bump.
	// Persisted state matching the old default is migrated transparently.
	legacyDefaultHeight = 142
)

// WidgetWindowState is persisted separately so compact mode never overwrites
// the user's main-window geometry.
type WidgetWindowState struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	X      int `json:"x"`
	Y      int `json:"y"`
}

// WidgetOption is one immediately recognisable reply in the current message.
type WidgetOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value"`
	Code        string `json:"code,omitempty"`
}

// WidgetMessage is the only important message shown by compact mode. Context
// is repeated on every message so users never need a list to identify it.
type WidgetMessage struct {
	ID             string         `json:"id"`
	Revision       string         `json:"revision"`
	TabID          string         `json:"tabId"`
	ProjectName    string         `json:"projectName"`
	TaskName       string         `json:"taskName"`
	TaskNameCode   string         `json:"taskNameCode,omitempty"`
	Kind           string         `json:"kind"`
	StateLabel     string         `json:"stateLabel"`
	StateCode      string         `json:"stateCode,omitempty"`
	Message        string         `json:"message"`
	MessageCode    string         `json:"messageCode,omitempty"`
	MessageCount   int            `json:"messageCount,omitempty"`
	InteractionID  string         `json:"interactionId,omitempty"`
	QuestionID     string         `json:"questionId,omitempty"`
	Options        []WidgetOption `json:"options"`
	RequiresWindow bool           `json:"requiresWindow,omitempty"`
}

// WidgetSnapshot is a projection of the existing controller/tab state. It has
// no independently mutable message queue, keeping the controller and attention
// sidecars as the single source of truth.
type WidgetSnapshot struct {
	Mode            bool           `json:"mode"`
	Current         *WidgetMessage `json:"current,omitempty"`
	RemainingCount  int            `json:"remainingCount"`
	RunningCount    int            `json:"runningCount"`
	WaitingCount    int            `json:"waitingCount"`
	CompletedCount  int            `json:"completedCount"`
	FailedCount     int            `json:"failedCount"`
	BackgroundCount int            `json:"backgroundCount"`
	IsIdle          bool           `json:"isIdle"`
	Info            WidgetInfo     `json:"info"`
	Version         string         `json:"version"`
}

// WidgetActionInput carries stale-write and retry protection for one action.
type WidgetActionInput struct {
	ItemID    string   `json:"itemId"`
	Revision  string   `json:"revision"`
	RequestID string   `json:"requestId"`
	Action    string   `json:"action"`
	Values    []string `json:"values"`
}

// WidgetActionResult exposes retryable/stale outcomes instead of swallowing
// failures. The latest snapshot lets the UI recover without guessing.
type WidgetActionResult struct {
	Status   string         `json:"status"`
	Error    string         `json:"error,omitempty"`
	Snapshot WidgetSnapshot `json:"snapshot"`
}

type widgetAppliedAction struct {
	RequestID string `json:"requestId"`
	ItemID    string `json:"itemId"`
	AppliedAt int64  `json:"appliedAt"`
}

type widgetPersistedState struct {
	Applied       []widgetAppliedAction       `json:"applied"`
	Deferred      map[string]int64            `json:"deferred,omitempty"`
	CurrentID     string                      `json:"currentId,omitempty"`
	Conversations []widgetConversationReceipt `json:"conversations,omitempty"`
}

type widgetSource struct {
	meta         TabMeta
	pending      control.PendingInteraction
	has          bool
	rank         int
	requestText  string // first user request, used for completion summaries
	resultText   string
	totalTokens  int
	tokenTracked bool
	model        string
	sessionDir   string // controller session dir; subagent meta lives under <dir>/subagents
	branchID     string // agent.BranchID(currentSessionPath); matches subagent ParentSession
}

type widgetSubagentKey struct {
	sessionDir string
	branchID   string
}

func newWidgetSubagentKey(sessionDir, branchID string) widgetSubagentKey {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir != "" {
		sessionDir = filepath.Clean(sessionDir)
	}
	return widgetSubagentKey{sessionDir: sessionDir, branchID: strings.TrimSpace(branchID)}
}

func widgetWindowStatePath() string {
	return filepath.Join(config.MemoryUserDir(), "desktop-widget-window.json")
}

func widgetActionStatePath() string {
	return filepath.Join(config.MemoryUserDir(), "desktop-widget-actions.json")
}

func loadWidgetWindowState() (WidgetWindowState, bool) {
	data, err := readFileUTF8(widgetWindowStatePath())
	if err != nil {
		return WidgetWindowState{}, false
	}
	var state WidgetWindowState
	if json.Unmarshal(data, &state) != nil {
		return WidgetWindowState{}, false
	}
	// Migrate old default 590×142 → 590×176 before validation, because the
	// legacy height is below the current minHeight.
	if state.Width == widgetDefaultWidth && state.Height == legacyDefaultHeight {
		state.Height = widgetDefaultHeight
		state.Y -= widgetDefaultHeight - legacyDefaultHeight
	}
	if state.Width < widgetMinWidth || state.Height < widgetMinHeight || state.Width > widgetMaxWidth || state.Height > widgetMaxHeight {
		return WidgetWindowState{}, false
	}
	return state, true
}

func saveWidgetWindowState(state WidgetWindowState) error {
	if state.Width < widgetMinWidth || state.Height < widgetMinHeight {
		return errors.New("widget window is smaller than its readable minimum")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(widgetWindowStatePath(), data, 0o644)
}

func (a *App) loadWidgetStateLocked() {
	if a.widgetStateLoaded {
		return
	}
	a.widgetStateLoaded = true
	data, err := readFileUTF8(widgetActionStatePath())
	if err != nil || json.Unmarshal(data, &a.widgetState) != nil {
		a.widgetState = widgetPersistedState{}
	}
	if len(a.widgetState.Applied) > widgetActionLimit {
		a.widgetState.Applied = a.widgetState.Applied[len(a.widgetState.Applied)-widgetActionLimit:]
	}
	if len(a.widgetState.Conversations) > widgetActionLimit {
		a.widgetState.Conversations = a.widgetState.Conversations[len(a.widgetState.Conversations)-widgetActionLimit:]
	}
	if a.widgetState.Deferred == nil {
		a.widgetState.Deferred = map[string]int64{}
	}
}

func (a *App) saveWidgetStateLocked() error {
	data, err := json.Marshal(a.widgetState)
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(widgetActionStatePath(), data, 0o600)
}

// IsWidgetMode reports whether the native window is currently compact.
func (a *App) IsWidgetMode() bool {
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	return a.widgetMode
}

// toggleWidgetTaskbar hides or restores the taskbar button while the native
// window switches between widget and main geometry. widgetTaskbarToggle is a
// test seam; nil uses the platform implementation, which is a no-op outside
// Windows.
func (a *App) toggleWidgetTaskbar(hide bool) error {
	toggle := a.widgetTaskbarToggle
	if toggle == nil {
		toggle = setWidgetTaskbarHidden
	}
	return toggle(hide)
}

// transitionWidgetMode serialises the complete native-window transition and
// publishes the new mode only after every transition step has finished.
func (a *App) transitionWidgetMode(target bool, apply func() error) (bool, error) {
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	if a.widgetMode == target {
		return false, nil
	}
	if err := apply(); err != nil {
		return false, err
	}
	a.widgetMode = target
	return true, nil
}

type widgetWindowTransition struct {
	widget func() error
	main   func() error
	hide   func() error
	show   func() error
}

// runWidgetWindowTransition applies geometry before taskbar visibility. When a
// later step fails it restores both pieces in the opposite order, keeping the
// primary and every rollback error. transitionWidgetMode holds widgetMu around
// this helper, so native window steps cannot interleave with another mode
// transition.
func runWidgetWindowTransition(enter bool, steps widgetWindowTransition) error {
	if enter {
		if err := steps.widget(); err != nil {
			return errors.Join(fmt.Errorf("apply widget window: %w", err), steps.main())
		}
		if err := steps.hide(); err != nil {
			return errors.Join(
				fmt.Errorf("hide taskbar for widget mode: %w", err),
				steps.main(),
				steps.show(),
			)
		}
		return nil
	}
	if err := steps.main(); err != nil {
		return errors.Join(fmt.Errorf("restore main window: %w", err), steps.widget())
	}
	if err := steps.show(); err != nil {
		return errors.Join(
			fmt.Errorf("restore taskbar for main window: %w", err),
			steps.widget(),
			steps.hide(),
		)
	}
	return nil
}

func (a *App) refreshWidgetRegion(size func() (int, int), apply func(int, int) error) error {
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	if !a.widgetMode {
		return nil
	}
	w, h := size()
	return apply(w, h)
}

// RefreshWidgetWindowRegion re-applies the native window region clipping from
// the current window size.  The frontend calls this on widget resize so the
// transparent corners stay accurate.  No-op outside widget mode and on
// non-Windows platforms.
func (a *App) RefreshWidgetWindowRegion() error {
	if a.ctx == nil {
		return nil
	}
	style, _ := a.desktopIconPreferences()
	if style == "icons" {
		return nil
	}
	return a.refreshWidgetRegion(
		func() (int, int) { return runtime.WindowGetSize(a.ctx) },
		func(w, h int) error {
			return a.applyWidgetRegion(func() error { return setWidgetWindowRegion(w, h) })
		},
	)
}

func (a *App) applyWidgetRegion(apply func() error) error {
	a.widgetRegionMu.Lock()
	defer a.widgetRegionMu.Unlock()
	return apply()
}

func (a *App) desktopWidgetPreferences() (enabled, alwaysOnTop bool, err error) {
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return false, false, err
	}
	return cfg.DesktopWidgetEnabled(), cfg.DesktopWidgetAlwaysOnTop(), nil
}

func (a *App) applyWidgetGeometry(state WidgetWindowState, alwaysOnTop bool) error {
	if a.widgetWindowOps != nil && a.widgetWindowOps.applyWidget != nil {
		return a.widgetWindowOps.applyWidget(state, alwaysOnTop, false)
	}
	runtime.WindowUnmaximise(a.ctx)
	runtime.WindowSetMinSize(a.ctx, widgetMinWidth, widgetMinHeight)
	runtime.WindowSetAlwaysOnTop(a.ctx, alwaysOnTop)
	if err := setDesktopWindowBounds(a.ctx, state.Width, state.Height, state.X, state.Y); err != nil {
		return err
	}
	return a.applyWidgetRegion(func() error {
		return setWidgetWindowRegion(state.Width, state.Height)
	})
}

func (a *App) applyDesktopIconGeometry(state WidgetWindowState, alwaysOnTop bool) error {
	if a.widgetWindowOps != nil && a.widgetWindowOps.applyWidget != nil {
		return a.widgetWindowOps.applyWidget(state, alwaysOnTop, true)
	}
	runtime.WindowUnmaximise(a.ctx)
	runtime.WindowSetMinSize(a.ctx, desktopIconMinWidth, desktopIconMinHeight)
	runtime.WindowSetAlwaysOnTop(a.ctx, alwaysOnTop)
	if err := setDesktopWindowBounds(a.ctx, state.Width, state.Height, state.X, state.Y); err != nil {
		return err
	}
	// Keep the full transparent surface available until React reports the first
	// real icon rectangles. Pre-clipping to the exit button prevents WebView2
	// from composing the later bottom-right controls outside that tiny region.
	return a.applyWidgetRegion(clearWidgetWindowRegion)
}

// widgetWindowOps is the test-only seam that replaces the native window
// geometry interaction inside widget-mode transitions and style switches.
// Every field may be nil; nil uses the Wails/Win32 implementation.
type widgetWindowOps struct {
	// read reports the current window size/position and whether it is
	// maximised.
	read func() (WidgetWindowState, bool)
	// normalize clamps a persisted widget geometry to a visible monitor.
	normalize func(state WidgetWindowState) (WidgetWindowState, error)
	// applyWidget applies widget geometry for the given style ("icons" selects
	// the transparent icon surface, any other value the pager).
	applyWidget func(state WidgetWindowState, alwaysOnTop bool, icons bool) error
	// restoreMain restores the main-window geometry after widget mode.
	restoreMain func(state DesktopWindowState, ok bool) error
}

// windowReadState returns the current native window geometry through the test
// seam when present, otherwise through the Wails runtime.
func (a *App) windowReadState() (WidgetWindowState, bool) {
	if a.widgetWindowOps != nil && a.widgetWindowOps.read != nil {
		return a.widgetWindowOps.read()
	}
	w, h := runtime.WindowGetSize(a.ctx)
	x, y := runtime.WindowGetPosition(a.ctx)
	return WidgetWindowState{Width: w, Height: h, X: x, Y: y}, runtime.WindowIsMaximised(a.ctx)
}

// normalizeWidgetState clamps a persisted widget geometry through the test
// seam when present, otherwise through the platform implementation.
func (a *App) normalizeWidgetState(state WidgetWindowState) (WidgetWindowState, error) {
	if a.widgetWindowOps != nil && a.widgetWindowOps.normalize != nil {
		return a.widgetWindowOps.normalize(state)
	}
	return normalizeWidgetWindowState(a.ctx, state)
}

// switchDesktopWidgetStyle acquires widgetMu around the style switch; see
// switchDesktopWidgetStyleLocked.
func (a *App) switchDesktopWidgetStyle(style string) (string, error) {
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	return a.switchDesktopWidgetStyleLocked(style)
}

// switchDesktopWidgetStyleLocked applies the geometry for the requested widget
// style while the compact window is active. The caller must hold widgetMu (the
// public wrapper and SetDesktopWidgetStyle both do), so the always-on-top flag
// read, the geometry switch, and the caller's persist form one atomic step that
// can never interleave with Enter/ExitWidgetMode or another style toggle. It
// returns the style that was active before the switch ("" when the call was a
// no-op or ran outside widget mode), which the caller can restore after a
// failed persist.
func (a *App) switchDesktopWidgetStyleLocked(style string) (string, error) {
	if !a.widgetMode {
		return "", nil
	}
	cfg, _, err := a.loadDesktopUserConfigForView()
	if err != nil {
		return "", err
	}
	alwaysOnTop := cfg.DesktopWidgetAlwaysOnTop()
	previous := a.widgetStyle
	if previous == style {
		return previous, nil
	}
	current, _ := a.windowReadState()
	oldState := WidgetWindowState{Width: current.Width, Height: current.Height, X: current.X, Y: current.Y}
	saveOld := saveWidgetWindowState
	if previous == "icons" {
		saveOld = saveDesktopIconWindowState
	}
	if err := saveOld(oldState); err != nil {
		return previous, fmt.Errorf("save %s widget geometry: %w", previous, err)
	}
	target, ok := loadWidgetWindowState()
	if style == "icons" {
		// Icon mode is a transparent desktop surface, so its drawing area follows
		// the current monitor on every switch. Persisted compact-window geometry
		// must not shrink the canvas after a display or DPI change.
		target, ok = a.desktopIconTargetState(), true
	}
	if !ok {
		if style == "icons" {
			target = defaultDesktopIconWindowState(a.ctx)
		} else {
			target = defaultWidgetWindowState(a.ctx)
		}
	}
	normalized, err := a.normalizeWidgetState(target)
	if err != nil {
		return previous, err
	}
	apply := a.applyWidgetGeometry
	if style == "icons" {
		apply = a.applyDesktopIconGeometry
	}
	if err := apply(normalized, alwaysOnTop); err != nil {
		rollback := a.applyWidgetGeometry
		if previous == "icons" {
			rollback = a.applyDesktopIconGeometry
		}
		return previous, errors.Join(err, rollback(oldState, alwaysOnTop))
	}
	a.widgetStyle = style
	return previous, nil
}

func (a *App) restoreMainGeometry(state DesktopWindowState, ok bool) error {
	if a.widgetWindowOps != nil && a.widgetWindowOps.restoreMain != nil {
		return a.widgetWindowOps.restoreMain(state, ok)
	}
	runtime.WindowSetAlwaysOnTop(a.ctx, false)
	regionErr := a.applyWidgetRegion(clearWidgetWindowRegion)
	runtime.WindowSetMinSize(a.ctx, 760, 480)
	if !ok {
		runtime.WindowSetSize(a.ctx, 1280, 800)
		runtime.WindowCenter(a.ctx)
		return regionErr
	}
	runtime.WindowUnmaximise(a.ctx)
	var boundsErr error
	if state.Width > 0 && state.Height > 0 {
		boundsErr = setDesktopWindowBounds(a.ctx, state.Width, state.Height, state.X, state.Y)
	}
	if state.Maximised {
		runtime.WindowMaximise(a.ctx)
	}
	return errors.Join(regionErr, boundsErr)
}

// EnterWidgetMode preserves the main geometry and switches the same Wails
// window into an always-on-top pager. Repeated calls are harmless.
func (a *App) EnterWidgetMode() (WidgetSnapshot, error) {
	if a.ctx == nil {
		return WidgetSnapshot{}, errors.New("desktop window is not ready")
	}
	changed, err := a.transitionWidgetMode(true, func() error { return a.applyEnterWidgetMode() })
	if err != nil {
		return WidgetSnapshot{}, err
	}
	if changed {
		runtime.EventsEmit(a.ctx, "widget:mode", true)
	}
	return a.GetWidgetSnapshot(), nil
}

// applyEnterWidgetMode runs inside transitionWidgetMode's widgetMu critical
// section: it reads the persisted widget preferences and applies the native
// window transition in the same atomic step, so a concurrent always-on-top or
// style toggle can never interleave between the read and the apply and leave
// the runtime window state differing from the persisted config. When widget
// mode is already active the transition short-circuits before this closure,
// keeping repeated entry idempotent.
func (a *App) applyEnterWidgetMode() error {
	widgetEnabled, widgetAlwaysOnTop, err := a.desktopWidgetPreferences()
	if err != nil {
		return fmt.Errorf("读取小组件设置: %w", err)
	}
	if !widgetEnabled {
		return errors.New("小组件已在设置中禁用，请前往 设置 > 小组件 重新启用")
	}
	current, maximised := a.windowReadState()
	mainState := DesktopWindowState{Width: current.Width, Height: current.Height, X: current.X, Y: current.Y, Maximised: maximised}
	if err := saveMainWindowState(mainState); err != nil {
		return fmt.Errorf("save main window: %w", err)
	}
	style, _ := a.desktopIconPreferences()
	state, ok := loadWidgetWindowState()
	if style == "icons" {
		// Always rebuild the icon canvas from the monitor that owns the main
		// window. The saved icon bounds remain useful for exit rollback only.
		state, ok = a.desktopIconTargetState(), true
	}
	if !ok {
		if style == "icons" {
			state = defaultDesktopIconWindowState(a.ctx)
		} else {
			state = defaultWidgetWindowState(a.ctx)
		}
	}
	normalized, normalizeErr := a.normalizeWidgetState(state)
	if normalizeErr != nil {
		return fmt.Errorf("normalize widget window: %w", normalizeErr)
	}
	state = normalized
	apply := a.applyWidgetGeometry
	if style == "icons" {
		apply = a.applyDesktopIconGeometry
	}
	if err := runWidgetWindowTransition(true, widgetWindowTransition{
		widget: func() error { return apply(state, widgetAlwaysOnTop) },
		main:   func() error { return a.restoreMainGeometry(mainState, true) },
		hide:   func() error { return a.toggleWidgetTaskbar(true) },
		show:   func() error { return a.toggleWidgetTaskbar(false) },
	}); err != nil {
		return err
	}
	a.widgetStyle = style
	return nil
}

// ExitWidgetMode saves compact geometry and restores the independent main
// geometry. Passing a tab ID also opens that task in the restored window.
func (a *App) ExitWidgetMode(tabID string) error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	// Opening a task from the widget must not change the icon's existence:
	// remember it as retained so the icon survives the task finishing and is
	// only removed by an explicit dismiss/remove. Best-effort — a failed
	// persist never blocks the window transition.
	a.rememberDesktopIconTask(tabID)
	return a.exitWidgetMode(tabID)
}

func (a *App) exitWidgetMode(tabID string) error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	_, err := a.transitionWidgetMode(false, func() error { return a.applyExitWidgetMode() })
	if err != nil {
		return err
	}
	var activateErr error
	if strings.TrimSpace(tabID) != "" {
		activateErr = a.SetActiveTab(tabID)
	}
	reconciled, reconcileErr := a.reconcileMainWindow()
	if reconcileErr != nil {
		return errors.Join(activateErr, reconcileErr)
	}
	if reconciled {
		// Publish only after the final native reconciliation. This keeps React
		// from exposing MainApp through a stale icon HRGN and also lets a repeated
		// exit repair an already-diverged logical/native window state.
		a.runtimeEvents.Emit(a.ctx, "widget:mode", false)
	}
	if activateErr != nil {
		return activateErr
	}
	if strings.TrimSpace(tabID) != "" {
		a.emitSessionActivated("widget-open")
	}
	return nil
}

// reconcileMainWindow is the idempotent commit point for every widget exit.
// It intentionally runs even when transitionWidgetMode found widgetMode
// already false: the logical bit may survive while Win32 still has widget
// bounds, WS_EX_TOOLWINDOW or an icon HRGN. Reapplying the authoritative main
// geometry and taskbar state makes the same exit call a safe recovery action.
func (a *App) reconcileMainWindow() (bool, error) {
	a.widgetMu.Lock()
	defer a.widgetMu.Unlock()
	if a.widgetMode {
		return false, nil
	}
	state, ok := loadWindowState()
	if err := errors.Join(a.restoreMainGeometry(state, ok), a.toggleWidgetTaskbar(false)); err != nil {
		return false, fmt.Errorf("reconcile main window: %w", err)
	}
	return true, nil
}

// applyExitWidgetMode runs inside transitionWidgetMode's widgetMu critical
// section: it saves the compact geometry, restores the independent main
// geometry, and switches the taskbar visibility in one atomic step.
func (a *App) applyExitWidgetMode() error {
	current, _ := a.windowReadState()
	widgetState := WidgetWindowState{Width: current.Width, Height: current.Height, X: current.X, Y: current.Y}
	style := a.widgetStyle
	save := saveWidgetWindowState
	if style == "icons" {
		save = saveDesktopIconWindowState
	}
	if err := save(widgetState); err != nil {
		return fmt.Errorf("save widget window: %w", err)
	}
	state, ok := loadWindowState()
	return runWidgetWindowTransition(false, widgetWindowTransition{
		widget: func() error { return a.reapplyWidgetGeometry(widgetState, style) },
		main:   func() error { return a.restoreMainGeometry(state, ok) },
		hide:   func() error { return a.toggleWidgetTaskbar(true) },
		show:   func() error { return a.toggleWidgetTaskbar(false) },
	})
}

// reapplyWidgetGeometry rolls a failed exit back to the saved widget geometry,
// reloading the always-on-top preference because the main restore already
// cleared it.
func (a *App) reapplyWidgetGeometry(state WidgetWindowState, style string) error {
	_, alwaysOnTop, configErr := a.desktopWidgetPreferences()
	if configErr != nil {
		fmt.Printf("widget: reload always-on-top preference during rollback: %v\n", configErr)
		alwaysOnTop = true
	}
	apply := a.applyWidgetGeometry
	if style == "icons" {
		apply = a.applyDesktopIconGeometry
	}
	return apply(state, alwaysOnTop)
}

func defaultWidgetWindowState(ctx context.Context) WidgetWindowState {
	if state, ok := nativeDefaultWidgetWindowState(ctx); ok {
		return state
	}
	screens, err := runtime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		return WidgetWindowState{Width: widgetDefaultWidth, Height: widgetDefaultHeight, X: widgetEdgeGap, Y: widgetBottomGap}
	}
	selected := screens[0]
	for _, screen := range screens {
		if screen.IsCurrent || (!selected.IsCurrent && screen.IsPrimary) {
			selected = screen
		}
		if screen.IsCurrent {
			break
		}
	}
	return defaultWidgetWindowStateForScreens(selected.Size.Width, selected.Size.Height)
}

func defaultWidgetWindowStateForScreens(width, height int) WidgetWindowState {
	widgetWidth := min(widgetDefaultWidth, max(widgetMinWidth, width-widgetEdgeGap*2))
	widgetHeight := min(widgetDefaultHeight, max(widgetMinHeight, height-widgetBottomGap*2))
	return WidgetWindowState{
		Width: widgetWidth, Height: widgetHeight,
		X: max(widgetEdgeGap, width-widgetWidth-widgetEdgeGap),
		Y: max(widgetBottomGap, height-widgetHeight-widgetBottomGap),
	}
}

func defaultDesktopIconWindowState(ctx context.Context) WidgetWindowState {
	if state, ok := nativeDefaultDesktopIconWindowState(ctx); ok {
		return state
	}
	screens, err := runtime.ScreenGetAll(ctx)
	if err != nil || len(screens) == 0 {
		return WidgetWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: widgetEdgeGap, Y: widgetBottomGap}
	}
	selected := screens[0]
	for _, screen := range screens {
		if screen.IsCurrent || (!selected.IsCurrent && screen.IsPrimary) {
			selected = screen
		}
		if screen.IsCurrent {
			break
		}
	}
	return WidgetWindowState{Width: selected.Size.Width, Height: selected.Size.Height}
}

// desktopIconTargetState keeps native production entry tied to the current
// monitor while preserving the injected geometry contract used by tests and
// non-window orchestration. A real window never reuses the persisted compact
// bounds, so stale display geometry cannot shrink the transparent canvas.
func (a *App) desktopIconTargetState() WidgetWindowState {
	if a.widgetWindowOps != nil {
		state, ok, err := loadDesktopIconWindowState()
		a.recordDesktopIconWindowError(err)
		if ok {
			return state
		}
	}
	return defaultDesktopIconWindowState(a.ctx)
}

func (a *App) widgetSources() []widgetSource {
	a.mu.RLock()
	tabs := a.runtimeTabsLocked()
	out := make([]widgetSource, 0, len(tabs))
	for rank, tab := range tabs {
		if tab == nil {
			continue
		}
		source := widgetSource{meta: a.tabMeta(tab, tab.ID == a.activeTabID), rank: rank}
		if tab.Ctrl != nil {
			source.sessionDir = tab.Ctrl.SessionDir()
		}
		source.branchID = agent.BranchID(tab.currentSessionPath())
		telemetry := tab.telemetrySnapshot().Usage
		source.totalTokens = telemetry.TotalTokens
		source.tokenTracked = telemetry.RequestCount > 0 || telemetry.TotalTokens > 0
		source.model = strings.TrimSpace(tab.Label)
		if tab.Ctrl != nil {
			source.pending, source.has = tab.Ctrl.PendingInteraction()
			source.requestText = firstWidgetUserText(tab.Ctrl.History())
			source.resultText = lastWidgetAssistantText(tab.Ctrl.History())
		}
		out = append(out, source)
	}
	a.mu.RUnlock()
	return out
}

// widgetSubagentCounts scans each unique session dir exactly once and returns
// running sub-agent counts keyed by parent branch ID. The scan performs file
// I/O and must never run while a.mu is held; widgetSources() has already
// released a.mu when this is called, and failures are returned so they surface
// in the snapshot instead of being mistaken for idle state.
func (a *App) widgetSubagentCounts(sources []widgetSource) (map[widgetSubagentKey]int, error) {
	dirs := map[string]bool{}
	for _, source := range sources {
		if dir := strings.TrimSpace(source.sessionDir); dir != "" {
			dirs[filepath.Clean(dir)] = true
		}
	}
	counts := map[widgetSubagentKey]int{}
	var errs []error
	for dir := range dirs {
		got, err := agent.RunningSubagentCounts(dir)
		for parent, n := range got {
			counts[newWidgetSubagentKey(dir, parent)] += n
		}
		if err != nil {
			// Keep the usable partial counts; the error still surfaces in the
			// snapshot instead of being mistaken for idle state.
			errs = append(errs, err)
		}
	}
	return counts, errors.Join(errs...)
}

// retryLeaseTabs retries startup after a session lease becomes available.
func (a *App) retryLeaseTabs() {
	a.retryLeaseTabsWith(agent.SessionLeaseHeldByOtherRuntime, a.RetryTabStartup)
}

// retryLeaseTabsWith keeps probing and mutation outside a.mu. The injected
// functions make filtering, retry, and failure recovery deterministic in tests.
func (a *App) retryLeaseTabsWith(leaseHeld func(string) bool, retry func(string) error) {
	type candidate struct {
		id          string
		sessionPath string
	}
	a.mu.RLock()
	var candidates []candidate
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil {
			continue
		}
		if tab.Ctrl != nil || tab.buildCancel != nil {
			continue
		}
		if !tab.StartupErrLeaseHeld || tab.StartupErr == "" {
			continue
		}
		sp := strings.TrimSpace(tab.currentSessionPath())
		if sp == "" {
			continue
		}
		candidates = append(candidates, candidate{tab.ID, sp})
	}
	a.mu.RUnlock()

	for _, c := range candidates {
		if leaseHeld(c.sessionPath) {
			continue
		}
		_ = retry(c.id)
	}
}

// GetWidgetSnapshot aggregates all runtimes while exposing one current message.
// It also attempts to auto-recover tabs that failed with a session-lease error
// when the foreign runtime has since released the lease.
func (a *App) GetWidgetSnapshot() WidgetSnapshot {
	a.widgetActionMu.Lock()
	defer a.widgetActionMu.Unlock()
	a.loadWidgetStateLocked()

	// The lease probe and retry release a.mu before doing I/O or mutation.
	a.retryLeaseTabs()

	return a.widgetSnapshotLocked()
}

func buildWidgetSnapshot(sources []widgetSource) WidgetSnapshot {
	return buildWidgetSnapshotWithDeferred(sources, nil)
}

func buildWidgetSnapshotWithDeferred(sources []widgetSource, deferred map[string]int64) WidgetSnapshot {
	return buildWidgetSnapshotState(sources, deferred, "")
}

func buildWidgetSnapshotState(sources []widgetSource, deferred map[string]int64, currentID string) WidgetSnapshot {
	type item struct {
		message  WidgetMessage
		priority int
		at       int64
		rank     int
		deferred int64
	}
	items := make([]item, 0, len(sources))
	snapshot := WidgetSnapshot{}
	for _, source := range sources {
		meta := source.meta
		if meta.RunningWork {
			snapshot.RunningCount++
		}
		if meta.BackgroundOnly {
			snapshot.BackgroundCount++
		}
		if strings.EqualFold(meta.SessionSource, "cli") {
			continue
		}
		if source.has {
			snapshot.WaitingCount++
			message := messageForPending(source)
			items = append(items, item{message: message, priority: 0, at: meta.NeedsAttentionAt, rank: source.rank, deferred: deferred[message.ID]})
			continue
		}
		if text := strings.TrimSpace(meta.StartupErr); text != "" {
			snapshot.FailedCount++
			message := baseWidgetMessage(meta, "error", "需要处理", conciseWidgetText(text, 84))
			message.StateCode = "action"
			message.ID = "error:" + meta.ID
			message.Revision = widgetMessageRevision(message)
			items = append(items, item{message: message, priority: 1, at: meta.NeedsAttentionAt, rank: source.rank, deferred: deferred[message.ID]})
			continue
		}
		if meta.NeedsAttention {
			snapshot.CompletedCount++
			text := conciseWidgetText(source.resultText, 110)
			fallback := text == ""
			if text == "" {
				text = "执行已完成，结果可以查看。"
			}
			message := baseWidgetMessage(meta, "result", "任务完成", text)
			message.StateCode = "complete"
			if fallback {
				message.MessageCode = "complete_fallback"
			}
			message.ID = fmt.Sprintf("result:%s:%d", meta.ID, meta.NeedsAttentionAt)
			message.Revision = widgetMessageRevision(message)
			items = append(items, item{message: message, priority: 2, at: meta.NeedsAttentionAt, rank: source.rank, deferred: deferred[message.ID]})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		leftDeferred := items[i].deferred != 0
		rightDeferred := items[j].deferred != 0
		if leftDeferred != rightDeferred {
			return !leftDeferred
		}
		if leftDeferred && items[i].deferred != items[j].deferred {
			return items[i].deferred < items[j].deferred
		}
		if items[i].priority != items[j].priority {
			return items[i].priority < items[j].priority
		}
		if items[i].at != items[j].at {
			if items[i].at == 0 {
				return false
			}
			if items[j].at == 0 {
				return true
			}
			return items[i].at < items[j].at
		}
		return items[i].rank < items[j].rank
	})
	// Keep the visible pager item stable while it still exists. A newly arrived
	// high-priority prompt waits behind it instead of replacing text mid-read.
	if currentID != "" {
		for i := 1; i < len(items); i++ {
			if items[i].message.ID == currentID {
				current := items[i]
				copy(items[1:i+1], items[0:i])
				items[0] = current
				break
			}
		}
	}
	if len(items) > 0 {
		current := items[0].message
		snapshot.Current = &current
		snapshot.RemainingCount = len(items) - 1
	}
	snapshot.IsIdle = snapshot.Current == nil &&
		snapshot.RunningCount == 0 &&
		snapshot.WaitingCount == 0 &&
		snapshot.CompletedCount == 0 &&
		snapshot.FailedCount == 0 &&
		snapshot.BackgroundCount == 0 &&
		snapshot.RemainingCount == 0
	snapshot.Version = widgetSnapshotVersion(snapshot)
	return snapshot
}

func lastWidgetAssistantText(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleAssistant {
			if text := strings.TrimSpace(messages[i].Content); text != "" {
				return text
			}
		}
	}
	return ""
}

// firstWidgetUserText returns the first non-empty user request of a history.
// It is the task-level ask used as completion-summary material; it never
// inspects or mutates the session itself.
func firstWidgetUserText(messages []provider.Message) string {
	for _, message := range messages {
		if message.Role == provider.RoleUser {
			if text := strings.TrimSpace(message.Content); text != "" {
				return text
			}
		}
	}
	return ""
}

func messageForPending(source widgetSource) WidgetMessage {
	meta := source.meta
	pending := source.pending
	if pending.Kind == control.PendingInteractionApproval {
		approval := pending.Approval
		text := strings.TrimSpace(approval.Subject)
		if text == "" {
			text = strings.TrimSpace(approval.Tool)
		}
		message := baseWidgetMessage(meta, "choice", "需要确认", conciseWidgetText(text, 84))
		message.StateCode = "confirm"
		message.ID = "approval:" + meta.ID + ":" + approval.ID
		message.InteractionID = approval.ID
		message.Options = []WidgetOption{
			{Label: "允许", Description: "继续执行这一步", Value: "allow", Code: "allow"},
			{Label: "拒绝", Description: "停止这一步", Value: "deny", Code: "deny"},
		}
		message.Revision = widgetMessageRevision(message)
		return message
	}

	ask := pending.Ask
	if len(ask.Questions) != 1 {
		message := baseWidgetMessage(meta, "reply", "等待回复", fmt.Sprintf("需要回答 %d 个问题，请在主窗口继续。", len(ask.Questions)))
		message.StateCode = "reply"
		message.MessageCode = "multi_question"
		message.MessageCount = len(ask.Questions)
		message.ID = "ask:" + meta.ID + ":" + ask.ID
		message.InteractionID = ask.ID
		message.RequiresWindow = true
		message.Revision = widgetMessageRevision(message)
		return message
	}
	question := ask.Questions[0]
	kind := "reply"
	if len(question.Options) > 0 && !question.Multi {
		kind = "choice"
	}
	message := baseWidgetMessage(meta, kind, "等待回复", conciseWidgetText(question.Prompt, 110))
	message.StateCode = "reply"
	message.ID = "ask:" + meta.ID + ":" + ask.ID
	message.InteractionID = ask.ID
	message.QuestionID = question.ID
	message.RequiresWindow = question.Multi || len(question.Options) > 3
	if !message.RequiresWindow {
		message.Options = make([]WidgetOption, 0, len(question.Options))
		for _, option := range question.Options {
			message.Options = append(message.Options, WidgetOption{Label: option.Label, Description: option.Description, Value: option.Label})
		}
	}
	message.Revision = widgetMessageRevision(message)
	return message
}

func baseWidgetMessage(meta TabMeta, kind, state, message string) WidgetMessage {
	project := strings.TrimSpace(meta.WorkspaceName)
	if project == "" {
		project = "WorkGround2"
	}
	task := strings.TrimSpace(meta.SessionDisplayTitle)
	if task == "" {
		task = strings.TrimSpace(meta.TopicTitle)
	}
	taskCode := ""
	if task == "" {
		task = "当前任务"
		taskCode = "current"
	}
	return WidgetMessage{
		TabID: meta.ID, ProjectName: project, TaskName: conciseWidgetText(task, 42), TaskNameCode: taskCode,
		Kind: kind, StateLabel: state, Message: message, Options: []WidgetOption{},
	}
}

func widgetMessageRevision(message WidgetMessage) string {
	return widgetRevision(
		message.ID,
		message.Message,
		message.StateCode,
		message.MessageCode,
		fmt.Sprint(message.MessageCount),
	)
}

func conciseWidgetText(text string, maxRunes int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func widgetRevision(parts ...string) string {
	h := fnv.New64a()
	for _, part := range parts {
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func widgetSnapshotVersion(snapshot WidgetSnapshot) string {
	current := ""
	if snapshot.Current != nil {
		current = snapshot.Current.ID + ":" + snapshot.Current.Revision
	}
	return widgetRevision(
		current,
		fmt.Sprint(snapshot.RemainingCount),
		fmt.Sprint(snapshot.RunningCount),
		fmt.Sprint(snapshot.WaitingCount),
		fmt.Sprint(snapshot.CompletedCount),
		fmt.Sprint(snapshot.FailedCount),
		fmt.Sprint(snapshot.BackgroundCount),
		fmt.Sprint(snapshot.IsIdle),
		fmt.Sprint(snapshot.Info.TotalTokens),
		fmt.Sprint(snapshot.Info.TokenPartial),
		fmt.Sprint(snapshot.Info.IdleSince),
		fmt.Sprint(snapshot.Info.System.Available),
		snapshot.Info.System.Network,
		fmt.Sprint(snapshot.Info.System.CPU),
		fmt.Sprint(snapshot.Info.System.Memory),
		widgetModelSignature(snapshot.Info.Models),
	)
}

// ApplyWidgetAction validates the current item, deduplicates retried requests,
// and routes the action back through the normal controller/tab entry points.
func (a *App) ApplyWidgetAction(input WidgetActionInput) WidgetActionResult {
	a.widgetActionMu.Lock()
	defer a.widgetActionMu.Unlock()

	input.ItemID = strings.TrimSpace(input.ItemID)
	input.Revision = strings.TrimSpace(input.Revision)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Action = strings.ToLower(strings.TrimSpace(input.Action))
	if input.RequestID == "" {
		a.loadWidgetStateLocked()
		return a.widgetActionErrorLocked("invalid", errors.New("requestId is required"))
	}

	a.loadWidgetStateLocked()
	for _, applied := range a.widgetState.Applied {
		if applied.RequestID == input.RequestID {
			return WidgetActionResult{Status: "already_applied", Snapshot: a.widgetSnapshotLocked()}
		}
	}

	snapshot := a.widgetSnapshotLocked()
	current := snapshot.Current
	if current == nil || current.ID != input.ItemID || current.Revision != input.Revision {
		return WidgetActionResult{Status: "stale", Error: "消息已经变化，请按最新状态操作", Snapshot: snapshot}
	}

	var err error
	if input.Action == "later" {
		a.widgetState.Deferred[current.ID] = time.Now().UnixMilli()
		a.widgetState.CurrentID = ""
		a.pruneWidgetDeferredLocked()
	} else {
		err = a.applyWidgetActionCurrent(*current, input)
	}
	if err != nil {
		return a.widgetActionErrorLocked("retryable_error", err)
	}
	if input.Action != "later" {
		delete(a.widgetState.Deferred, current.ID)
	}
	a.widgetState.Applied = append(a.widgetState.Applied, widgetAppliedAction{RequestID: input.RequestID, ItemID: input.ItemID, AppliedAt: time.Now().UnixMilli()})
	if len(a.widgetState.Applied) > widgetActionLimit {
		a.widgetState.Applied = a.widgetState.Applied[len(a.widgetState.Applied)-widgetActionLimit:]
	}
	if err := a.saveWidgetStateLocked(); err != nil {
		return a.widgetActionErrorLocked("retryable_error", fmt.Errorf("save action receipt: %w", err))
	}
	return WidgetActionResult{Status: "accepted", Snapshot: a.widgetSnapshotLocked()}
}

func (a *App) widgetSnapshotLocked() WidgetSnapshot {
	sources := a.widgetSources()
	snapshot := buildWidgetSnapshotState(sources, a.widgetState.Deferred, a.widgetState.CurrentID)
	if snapshot.Current == nil {
		a.widgetState.CurrentID = ""
	} else {
		a.widgetState.CurrentID = snapshot.Current.ID
	}
	snapshot.Mode = a.IsWidgetMode()
	a.widgetIdleSince = nextWidgetIdleSince(a.widgetIdleSince, snapshot.IsIdle, time.Now().UnixMilli())
	snapshot.Info = a.widgetInfo(sources, a.widgetIdleSince)
	snapshot.Version = widgetSnapshotVersion(snapshot)
	return snapshot
}

func nextWidgetIdleSince(current int64, idle bool, now int64) int64 {
	if !idle {
		return 0
	}
	if current > 0 {
		return current
	}
	return now
}

func (a *App) pruneWidgetDeferredLocked() {
	for len(a.widgetState.Deferred) > widgetActionLimit {
		oldestID := ""
		var oldestAt int64
		for id, at := range a.widgetState.Deferred {
			if oldestID == "" || at < oldestAt {
				oldestID, oldestAt = id, at
			}
		}
		delete(a.widgetState.Deferred, oldestID)
	}
}

func (a *App) applyWidgetActionCurrent(current WidgetMessage, input WidgetActionInput) error {
	switch input.Action {
	case "answer":
		if current.InteractionID == "" || current.QuestionID == "" || len(input.Values) == 0 {
			return errors.New("answer value is required")
		}
		ctrl := a.ctrlByTabID(current.TabID)
		if ctrl == nil {
			return errors.New("task is not ready")
		}
		pending, ok := ctrl.PendingInteraction()
		if !ok || pending.Kind != control.PendingInteractionAsk || pending.Ask.ID != current.InteractionID || len(pending.Ask.Questions) != 1 {
			return errors.New("pending question changed")
		}
		question := pending.Ask.Questions[0]
		if question.ID != current.QuestionID || (question.Multi && len(input.Values) < 1) || (!question.Multi && len(input.Values) != 1) {
			return errors.New("answer does not match the current question")
		}
		values := make([]string, 0, len(input.Values))
		for _, value := range input.Values {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			return errors.New("answer value is required")
		}
		ctrl.AnswerQuestion(pending.Ask.ID, []event.AskAnswer{{QuestionID: question.ID, Selected: values}})
		return nil
	case "approve", "deny":
		if current.InteractionID == "" {
			return errors.New("approval id is required")
		}
		return a.approvePendingIDForTab(current.TabID, current.InteractionID, input.Action == "approve")
	case "next":
		return clearTabAttention(a.tabByID(current.TabID))
	case "retry":
		return a.RetryTabStartup(current.TabID)
	case "open":
		return a.ExitWidgetMode(current.TabID)
	default:
		return fmt.Errorf("unsupported widget action %q", input.Action)
	}
}

func (a *App) widgetActionErrorLocked(status string, err error) WidgetActionResult {
	return WidgetActionResult{Status: status, Error: err.Error(), Snapshot: a.widgetSnapshotLocked()}
}
