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
	resultText   string
	totalTokens  int
	tokenTracked bool
	model        string
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

// EnterWidgetMode preserves the main geometry and switches the same Wails
// window into an always-on-top pager. Repeated calls are harmless.
func (a *App) EnterWidgetMode() (WidgetSnapshot, error) {
	if a.ctx == nil {
		return WidgetSnapshot{}, errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	if a.widgetMode {
		a.widgetMu.Unlock()
		return a.GetWidgetSnapshot(), nil
	}
	w, h := runtime.WindowGetSize(a.ctx)
	x, y := runtime.WindowGetPosition(a.ctx)
	mainState := DesktopWindowState{Width: w, Height: h, X: x, Y: y, Maximised: runtime.WindowIsMaximised(a.ctx)}
	if err := saveMainWindowState(mainState); err != nil {
		a.widgetMu.Unlock()
		return WidgetSnapshot{}, fmt.Errorf("save main window: %w", err)
	}
	state, ok := loadWidgetWindowState()
	if !ok {
		state = defaultWidgetWindowState(a.ctx)
	}
	a.widgetMode = true
	a.widgetMu.Unlock()

	runtime.WindowUnmaximise(a.ctx)
	runtime.WindowSetMinSize(a.ctx, widgetMinWidth, widgetMinHeight)
	runtime.WindowSetAlwaysOnTop(a.ctx, true)
	runtime.WindowSetSize(a.ctx, state.Width, state.Height)
	runtime.WindowSetPosition(a.ctx, state.X, state.Y)
	runtime.EventsEmit(a.ctx, "widget:mode", true)
	return a.GetWidgetSnapshot(), nil
}

// ExitWidgetMode saves compact geometry and restores the independent main
// geometry. Passing a tab ID also opens that task in the restored window.
func (a *App) ExitWidgetMode(tabID string) error {
	if a.ctx == nil {
		return errors.New("desktop window is not ready")
	}
	a.widgetMu.Lock()
	if !a.widgetMode {
		a.widgetMu.Unlock()
		if strings.TrimSpace(tabID) != "" {
			if err := a.SetActiveTab(tabID); err != nil {
				return err
			}
			a.emitSessionActivated("widget-open")
		}
		return nil
	}
	w, h := runtime.WindowGetSize(a.ctx)
	x, y := runtime.WindowGetPosition(a.ctx)
	if err := saveWidgetWindowState(WidgetWindowState{Width: w, Height: h, X: x, Y: y}); err != nil {
		a.widgetMu.Unlock()
		return fmt.Errorf("save widget window: %w", err)
	}
	a.widgetMode = false
	a.widgetMu.Unlock()

	runtime.WindowSetAlwaysOnTop(a.ctx, false)
	runtime.WindowSetMinSize(a.ctx, 760, 480)
	state, ok := loadWindowState()
	if ok {
		runtime.WindowUnmaximise(a.ctx)
		if state.Width > 0 && state.Height > 0 {
			runtime.WindowSetSize(a.ctx, state.Width, state.Height)
		}
		runtime.WindowSetPosition(a.ctx, state.X, state.Y)
		if state.Maximised {
			runtime.WindowMaximise(a.ctx)
		}
	} else {
		runtime.WindowSetSize(a.ctx, 1280, 800)
		runtime.WindowCenter(a.ctx)
	}
	runtime.EventsEmit(a.ctx, "widget:mode", false)
	if strings.TrimSpace(tabID) != "" {
		if err := a.SetActiveTab(tabID); err != nil {
			return err
		}
		a.emitSessionActivated("widget-open")
	}
	return nil
}

func defaultWidgetWindowState(ctx context.Context) WidgetWindowState {
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

func (a *App) widgetSources() []widgetSource {
	a.mu.RLock()
	tabs := a.runtimeTabsLocked()
	out := make([]widgetSource, 0, len(tabs))
	for rank, tab := range tabs {
		if tab == nil {
			continue
		}
		source := widgetSource{meta: a.tabMeta(tab, tab.ID == a.activeTabID), rank: rank}
		telemetry := tab.telemetrySnapshot().Usage
		source.totalTokens = telemetry.TotalTokens
		source.tokenTracked = telemetry.RequestCount > 0 || telemetry.TotalTokens > 0
		source.model = strings.TrimSpace(tab.Label)
		if tab.Ctrl != nil {
			source.pending, source.has = tab.Ctrl.PendingInteraction()
			source.resultText = lastWidgetAssistantText(tab.Ctrl.History())
		}
		out = append(out, source)
	}
	a.mu.RUnlock()
	return out
}

// GetWidgetSnapshot aggregates all runtimes while exposing one current message.
func (a *App) GetWidgetSnapshot() WidgetSnapshot {
	a.widgetActionMu.Lock()
	defer a.widgetActionMu.Unlock()
	a.loadWidgetStateLocked()
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
