package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/fileutil"
	"workground2/internal/provider"
)

func TestBuildWidgetSnapshotShowsOneMessageAndRemainingCount(t *testing.T) {
	snapshot := buildWidgetSnapshot([]widgetSource{
		{rank: 0, meta: TabMeta{ID: "done", WorkspaceName: "WorkGround2", SessionDisplayTitle: "更新插件文档", NeedsAttention: true, NeedsAttentionAt: 20}},
		{rank: 1, meta: TabMeta{ID: "ask", WorkspaceName: "API", SessionDisplayTitle: "确认发布"}, has: true, pending: control.PendingInteraction{
			Kind: control.PendingInteractionAsk,
			Ask:  event.Ask{ID: "ask-1", Questions: []event.AskQuestion{{ID: "q-1", Prompt: "英文版也一起更新？", Options: []event.AskOption{{Label: "一起更新"}, {Label: "仅更新中文"}}}}},
		}},
		{rank: 2, meta: TabMeta{ID: "error", WorkspaceName: "Desktop", SessionDisplayTitle: "构建桌面端", StartupErr: "provider unavailable"}},
	})

	if snapshot.Current == nil {
		t.Fatal("expected current message")
	}
	if snapshot.Current.TabID != "ask" || snapshot.Current.Kind != "choice" {
		t.Fatalf("current = %#v, want ask choice", snapshot.Current)
	}
	if snapshot.RemainingCount != 2 {
		t.Fatalf("remaining = %d, want 2", snapshot.RemainingCount)
	}
	if snapshot.CompletedCount != 1 || snapshot.FailedCount != 1 || snapshot.WaitingCount != 1 {
		t.Fatalf("counts = completed %d failed %d waiting %d", snapshot.CompletedCount, snapshot.FailedCount, snapshot.WaitingCount)
	}
}

func TestBuildWidgetSnapshotIdleKeepsAggregateStatus(t *testing.T) {
	snapshot := buildWidgetSnapshot([]widgetSource{
		{meta: TabMeta{ID: "run", RunningWork: true}},
		{meta: TabMeta{ID: "background", RunningWork: true, BackgroundOnly: true}},
	})
	if snapshot.Current != nil || snapshot.RemainingCount != 0 {
		t.Fatalf("idle snapshot unexpectedly has message: %#v", snapshot)
	}
	if snapshot.RunningCount != 2 || snapshot.BackgroundCount != 1 {
		t.Fatalf("running = %d background = %d", snapshot.RunningCount, snapshot.BackgroundCount)
	}
}

func TestMessageForPendingComplexAskRequiresMainWindow(t *testing.T) {
	message := messageForPending(widgetSource{
		meta: TabMeta{ID: "tab", WorkspaceName: "WorkGround2", TopicTitle: "发布"},
		pending: control.PendingInteraction{Kind: control.PendingInteractionAsk, Ask: event.Ask{ID: "ask", Questions: []event.AskQuestion{
			{ID: "one", Prompt: "第一题"}, {ID: "two", Prompt: "第二题"},
		}}},
		has: true,
	})
	if !message.RequiresWindow || message.Kind != "reply" {
		t.Fatalf("message = %#v", message)
	}
	if !strings.Contains(message.Message, "2") {
		t.Fatalf("message %q does not explain question count", message.Message)
	}
}

func TestConciseWidgetTextFlattensAndTruncates(t *testing.T) {
	got := conciseWidgetText("  第一行\n  第二行   第三行  ", 8)
	if got != "第一行 第二行…" {
		t.Fatalf("got %q", got)
	}
}

func TestDefaultWidgetWindowStateFitsSmallScreen(t *testing.T) {
	state := defaultWidgetWindowStateForScreens(1024, 768)
	if state.Width != widgetDefaultWidth || state.Height != widgetDefaultHeight {
		t.Fatalf("state = %#v", state)
	}
	if state.X+state.Width > 1024 || state.Y+state.Height > 768 {
		t.Fatalf("widget does not fit screen: %#v", state)
	}
}

func TestQueueNeedsAttentionIncludesActiveTabInWidgetMode(t *testing.T) {
	tab := &WorkspaceTab{ID: "active"}
	app := &App{tabs: map[string]*WorkspaceTab{"active": tab}, activeTabID: "active", widgetMode: true}
	app.queueNeedsAttention("active", 42)

	tab.saveMu.Lock()
	got := tab.pendingAttentionAt
	tab.saveMu.Unlock()
	if got != 42 {
		t.Fatalf("pending attention = %d, want 42", got)
	}
}

func TestBuildWidgetSnapshotDoesNotExposeCLIPrompts(t *testing.T) {
	snapshot := buildWidgetSnapshot([]widgetSource{{
		meta:    TabMeta{ID: "cli", SessionSource: "cli", PendingPrompt: true},
		has:     true,
		pending: control.PendingInteraction{Kind: control.PendingInteractionApproval, Approval: event.Approval{ID: "approval"}},
	}})
	if snapshot.Current != nil || snapshot.WaitingCount != 0 {
		t.Fatalf("CLI prompt leaked into widget: %#v", snapshot)
	}
}

func TestBuildWidgetSnapshotMovesDeferredMessageBehindNextItem(t *testing.T) {
	sources := []widgetSource{
		{rank: 0, meta: TabMeta{ID: "ask", WorkspaceName: "One"}, has: true, pending: control.PendingInteraction{
			Kind: control.PendingInteractionAsk,
			Ask:  event.Ask{ID: "ask-1", Questions: []event.AskQuestion{{ID: "q", Prompt: "继续？", Options: []event.AskOption{{Label: "继续"}}}}},
		}},
		{rank: 1, meta: TabMeta{ID: "done", WorkspaceName: "Two", NeedsAttention: true, NeedsAttentionAt: 10}},
	}
	first := buildWidgetSnapshot(sources)
	if first.Current == nil || first.Current.TabID != "ask" {
		t.Fatalf("first current = %#v", first.Current)
	}
	deferred := map[string]int64{first.Current.ID: 20}
	after := buildWidgetSnapshotWithDeferred(sources, deferred)
	if after.Current == nil || after.Current.TabID != "done" || after.RemainingCount != 1 {
		t.Fatalf("deferred snapshot = %#v", after)
	}
}

func TestMessageForPendingRoutesLargeChoiceToMainWindow(t *testing.T) {
	message := messageForPending(widgetSource{
		meta: TabMeta{ID: "tab"},
		has:  true,
		pending: control.PendingInteraction{Kind: control.PendingInteractionAsk, Ask: event.Ask{ID: "ask", Questions: []event.AskQuestion{{
			ID: "q", Prompt: "选择", Options: []event.AskOption{{Label: "一"}, {Label: "二"}, {Label: "三"}, {Label: "四"}},
		}}}},
	})
	if !message.RequiresWindow || len(message.Options) != 0 {
		t.Fatalf("large choice should require main window: %#v", message)
	}
}

func TestMessageForPendingInlineChoiceUpToThreeOptions(t *testing.T) {
	// 3 options should stay inline.
	message := messageForPending(widgetSource{
		meta: TabMeta{ID: "tab", WorkspaceName: "Demo"},
		has:  true,
		pending: control.PendingInteraction{Kind: control.PendingInteractionAsk, Ask: event.Ask{ID: "ask", Questions: []event.AskQuestion{{
			ID: "q", Prompt: "选择语言", Options: []event.AskOption{{Label: "中文"}, {Label: "英文"}, {Label: "日语"}},
		}}}},
	})
	if message.RequiresWindow {
		t.Fatalf("3 options should stay inline: %#v", message)
	}
	if len(message.Options) != 3 {
		t.Fatalf("expected 3 inline options, got %d", len(message.Options))
	}
	if message.Kind != "choice" {
		t.Fatalf("expected choice kind, got %s", message.Kind)
	}
}

func TestMessageForPendingMultiSelectRequiresMainWindow(t *testing.T) {
	message := messageForPending(widgetSource{
		meta: TabMeta{ID: "tab"},
		has:  true,
		pending: control.PendingInteraction{Kind: control.PendingInteractionAsk, Ask: event.Ask{ID: "ask", Questions: []event.AskQuestion{{
			ID: "q", Prompt: "多选", Multi: true, Options: []event.AskOption{{Label: "一"}, {Label: "二"}},
		}}}},
	})
	if !message.RequiresWindow {
		t.Fatalf("multi-select should require main window: %#v", message)
	}
}

func TestChooseWidgetWorkspaceTransientActivePrefersStableFamily(t *testing.T) {
	route := chooseWidgetWorkspace("整理一下", []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2-main-ci-gate`, Name: "WorkGround2-main-ci-gate", Aliases: []string{"WorkGround2-main-ci-gate", "WorkGround2"}, Active: true, Transient: true, Order: 0},
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Active: false, Transient: false, Order: 1},
		{Scope: "project", Root: `D:\Work\API`, Name: "API", Aliases: []string{"API"}, Active: false, Transient: false, Order: 2},
	})
	if route.Name != "WorkGround2" || route.Reason != "主工作区" {
		t.Fatalf("expected stable WorkGround2 with reason 主工作区, got %#v", route)
	}
}

func TestChooseWidgetWorkspaceExplicitTransientNameMatchAllowed(t *testing.T) {
	route := chooseWidgetWorkspace("在 WorkGround2-main-ci-gate 上运行", []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2-main-ci-gate`, Name: "WorkGround2-main-ci-gate", Aliases: []string{"WorkGround2-main-ci-gate"}, Active: true, Transient: true, Order: 0},
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Active: false, Transient: false, Order: 1},
	})
	if route.Name != "WorkGround2-main-ci-gate" || route.Reason != "名称匹配" {
		t.Fatalf("explicit name match should keep transient: %#v", route)
	}
}

func TestWidgetWorkspaceVariantOfUsesSiblingRootPrefix(t *testing.T) {
	if !widgetWorkspaceVariantOf(`D:\Work\WorkGround2-artifact-restart`, `D:\Work\WorkGround2`) {
		t.Fatal("expected sibling workspace to resolve to its stable family")
	}
	if widgetWorkspaceVariantOf(`D:\Other\WorkGround2-ci`, `D:\Work\WorkGround2`) {
		t.Fatal("different parent directory must not be treated as the same family")
	}
	if widgetWorkspaceVariantOf(`D:\Work\WorkGround20-ci`, `D:\Work\WorkGround2`) {
		t.Fatal("family match requires a separator boundary")
	}
}

func TestWidgetIsTransientRootDetectsSessionOnlyShell(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".WorkGround2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !widgetIsTransientRoot(root, "Feature shell") {
		t.Fatal("session-only workspace should be transient")
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
	}
	if widgetIsTransientRoot(root, "Feature shell") {
		t.Fatal("workspace with project content should remain eligible")
	}
}

func TestBuildWidgetSnapshotKeepsCurrentWhenHigherPriorityArrives(t *testing.T) {
	done := widgetSource{rank: 0, meta: TabMeta{ID: "done", NeedsAttention: true, NeedsAttentionAt: 10}}
	first := buildWidgetSnapshot([]widgetSource{done})
	if first.Current == nil {
		t.Fatal("expected initial result")
	}
	ask := widgetSource{rank: 1, meta: TabMeta{ID: "ask"}, has: true, pending: control.PendingInteraction{
		Kind: control.PendingInteractionAsk,
		Ask:  event.Ask{ID: "ask", Questions: []event.AskQuestion{{ID: "q", Prompt: "继续？"}}},
	}}
	after := buildWidgetSnapshotState([]widgetSource{done, ask}, nil, first.Current.ID)
	if after.Current == nil || after.Current.TabID != "done" || after.RemainingCount != 1 {
		t.Fatalf("current was preempted: %#v", after)
	}
}

func TestBuildWidgetSnapshotUsesAssistantResult(t *testing.T) {
	snapshot := buildWidgetSnapshot([]widgetSource{{
		meta:       TabMeta{ID: "done", WorkspaceName: "WorkGround2", NeedsAttention: true},
		resultText: "已完成构建，全部测试通过。",
	}})
	if snapshot.Current == nil || snapshot.Current.Message != "已完成构建，全部测试通过。" {
		t.Fatalf("result = %#v", snapshot.Current)
	}
}

func TestLastWidgetAssistantTextSkipsEmptyMessages(t *testing.T) {
	got := lastWidgetAssistantText([]provider.Message{
		{Role: provider.RoleAssistant, Content: "最终结果"},
		{Role: provider.RoleTool, Content: "tool output"},
		{Role: provider.RoleAssistant, Content: "   "},
	})
	if got != "最终结果" {
		t.Fatalf("got %q", got)
	}
}

func TestChooseWidgetWorkspacePrefersExplicitName(t *testing.T) {
	route := chooseWidgetWorkspace("帮我修复 WorkGround2 的登录问题", []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\Other`, Name: "Other", Aliases: []string{"Other"}, Active: true, Order: 0},
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Order: 1},
	})
	if route.Name != "WorkGround2" || route.Reason != "名称匹配" {
		t.Fatalf("route = %#v", route)
	}
}

func TestChooseWidgetWorkspaceUsesRecentTopicContext(t *testing.T) {
	route := chooseWidgetWorkspace("继续处理登录页面样式", []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\API`, Name: "API", Aliases: []string{"API"}, Active: true, Order: 0},
		{Scope: "project", Root: `D:\Work\Client`, Name: "Client", Aliases: []string{"Client"}, Topics: []string{"登录页面重构"}, Order: 1},
	})
	if route.Name != "Client" || route.Reason != "历史上下文" {
		t.Fatalf("route = %#v", route)
	}
}

func TestChooseWidgetWorkspaceFallsBackToActive(t *testing.T) {
	route := chooseWidgetWorkspace("整理一下", []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\One`, Name: "One", Active: false, Order: 0},
		{Scope: "project", Root: `D:\Work\Two`, Name: "Two", Active: true, Order: 1},
	})
	if route.Name != "Two" || route.Reason != "当前工作区" {
		t.Fatalf("route = %#v", route)
	}
}

func TestChooseWidgetWorkspaceFallsBackToGlobal(t *testing.T) {
	route := chooseWidgetWorkspace("整理一下", nil)
	if route.Scope != "global" || route.Reason != "Global 兜底" {
		t.Fatalf("route = %#v", route)
	}
}

func TestWidgetHistoryHasPromptMatchesExactUserTurn(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "修复登录页"}}
	if !widgetHistoryHasPrompt(messages, " 修复登录页 ") || widgetHistoryHasPrompt(messages, "修复注册页") {
		t.Fatal("prompt receipt matching is incorrect")
	}
}

func TestWidgetWindowStateMigrationKeepsBottomEdge(t *testing.T) {
	// Old default 590×142 must migrate up to 590×176 while Y shifts by -34.
	oldY := 500
	path := widgetWindowStatePath()
	oldState := WidgetWindowState{Width: widgetDefaultWidth, Height: legacyDefaultHeight, X: widgetEdgeGap, Y: oldY}
	data, err := json.Marshal(oldState)
	if err != nil {
		t.Fatal(err)
	}
	if err := fileutil.AtomicWriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)

	state, ok := loadWidgetWindowState()
	if !ok {
		t.Fatal("expected valid state after loading old default")
	}
	if state.Width != widgetDefaultWidth || state.Height != widgetDefaultHeight {
		t.Fatalf("migrated state = %dx%d, want %dx%d", state.Width, state.Height, widgetDefaultWidth, widgetDefaultHeight)
	}
	wantY := oldY - (widgetDefaultHeight - legacyDefaultHeight)
	if state.Y != wantY {
		t.Fatalf("migrated Y = %d, want %d (bottom edge preserved)", state.Y, wantY)
	}

	// A custom size must not be migrated.
	customState := WidgetWindowState{Width: 600, Height: 170, X: 10, Y: 200}
	data, _ = json.Marshal(customState)
	fileutil.AtomicWriteFile(path, data, 0o644)
	state2, ok2 := loadWidgetWindowState()
	if !ok2 {
		t.Fatal("custom size should be valid")
	}
	if state2.Height != 170 {
		t.Fatalf("custom size must not be migrated, got height %d", state2.Height)
	}
}

func TestBuildWidgetSnapshotIdleFlag(t *testing.T) {
	// Idle: no current, all counts zero.
	idle := buildWidgetSnapshot(nil)
	if !idle.IsIdle {
		t.Fatal("empty snapshot should be idle")
	}

	// Running work breaks idle.
	busy := buildWidgetSnapshot([]widgetSource{
		{meta: TabMeta{ID: "run", RunningWork: true}},
	})
	if busy.IsIdle {
		t.Fatal("running work should make snapshot not idle")
	}

	// Completed message breaks idle.
	done := buildWidgetSnapshot([]widgetSource{
		{rank: 0, meta: TabMeta{ID: "done", NeedsAttention: true, NeedsAttentionAt: 10}, resultText: "done"},
	})
	if done.IsIdle {
		t.Fatal("completed message should make snapshot not idle")
	}
}

func TestWidgetSnapshotVersionTracksBackgroundCount(t *testing.T) {
	left := WidgetSnapshot{BackgroundCount: 1}
	right := WidgetSnapshot{BackgroundCount: 2}
	if widgetSnapshotVersion(left) == widgetSnapshotVersion(right) {
		t.Fatal("background count changes must refresh the widget status")
	}
}

func TestResolveWidgetWorkspaceAutoDelegatesToSmartRouting(t *testing.T) {
	candidates := []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Order: 0},
	}
	route, err := resolveWidgetWorkspace("auto", "帮我整理 WorkGround2", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if route.Name != "WorkGround2" || route.Reason != "名称匹配" {
		t.Fatalf("auto routing = %#v", route)
	}
}

func TestResolveWidgetWorkspaceGlobal(t *testing.T) {
	route, err := resolveWidgetWorkspace("global", "随便说点什么", nil)
	if err != nil {
		t.Fatal(err)
	}
	if route.Scope != "global" || route.Reason != "手动选择" {
		t.Fatalf("global routing = %#v", route)
	}
}

func TestResolveWidgetWorkspaceProjectExplicit(t *testing.T) {
	candidates := []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Order: 0},
		{Scope: "project", Root: `D:\Work\CICDBOT`, Name: "CICDBOT", Aliases: []string{"CICDBOT"}, Order: 1},
	}
	route, err := resolveWidgetWorkspace("project:D:\\Work\\CICDBOT", "deploy", candidates)
	if err != nil {
		t.Fatal(err)
	}
	if route.Name != "CICDBOT" || route.Reason != "手动选择" || route.Root != `D:\Work\CICDBOT` {
		t.Fatalf("project routing = %#v", route)
	}
}

func TestResolveWidgetWorkspaceRejectsTransient(t *testing.T) {
	candidates := []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2-ci-gate`, Name: "WorkGround2-ci-gate", Aliases: []string{"WorkGround2-ci-gate"}, Transient: true, Order: 0},
	}
	_, err := resolveWidgetWorkspace("project:D:\\Work\\WorkGround2-ci-gate", "test", candidates)
	if err == nil {
		t.Fatal("expected transient workspace to be rejected for manual selection")
	}
}

func TestResolveWidgetWorkspaceRejectsExpired(t *testing.T) {
	candidates := []widgetWorkspaceCandidate{
		{Scope: "project", Root: `D:\Work\WorkGround2`, Name: "WorkGround2", Aliases: []string{"WorkGround2"}, Order: 0},
	}
	_, err := resolveWidgetWorkspace("project:D:\\Work\\Deleted", "test", candidates)
	if err == nil {
		t.Fatal("expected expired root to be rejected")
	}
}

func TestExitWidgetModeEmitsSessionActivated(t *testing.T) {
	events := make(chan sessionActivatedEvent, 1)
	tab := &WorkspaceTab{ID: "result-tab"}
	app := &App{
		ctx:         context.Background(),
		tabs:        map[string]*WorkspaceTab{"result-tab": tab},
		activeTabID: "result-tab",
		widgetMode:  false, // not in widget mode → window ops skipped, goes straight to SetActiveTab
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != "session:activated" {
			return
		}
		if len(payload) == 1 {
			if evt, ok := payload[0].(sessionActivatedEvent); ok {
				events <- evt
			}
		}
	}

	if err := app.ExitWidgetMode("result-tab"); err != nil {
		t.Fatalf("ExitWidgetMode: %v", err)
	}

	select {
	case evt := <-events:
		if evt.Reason != "widget-open" {
			t.Fatalf("session:activated reason = %q, want widget-open", evt.Reason)
		}
		if evt.TabID != "result-tab" {
			t.Fatalf("session:activated tabId = %q, want result-tab", evt.TabID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("session:activated event was not emitted")
	}
}

func TestExitWidgetModeNoTabIDDoesNotEmitSessionActivated(t *testing.T) {
	emitted := false
	tab := &WorkspaceTab{ID: "idle-tab"}
	app := &App{
		ctx:         context.Background(),
		tabs:        map[string]*WorkspaceTab{"idle-tab": tab},
		activeTabID: "idle-tab",
		widgetMode:  false,
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {
		if name == "session:activated" {
			emitted = true
		}
	}

	if err := app.ExitWidgetMode(""); err != nil {
		t.Fatalf("ExitWidgetMode: %v", err)
	}
	if emitted {
		t.Fatal("session:activated should not be emitted when no tabID is passed")
	}
}

func TestExitWidgetModeSetActiveTabFailureDoesNotEmitSessionActivated(t *testing.T) {
	emitted := false
	app := &App{
		ctx:         context.Background(),
		tabs:        map[string]*WorkspaceTab{},
		activeTabID: "missing",
		widgetMode:  false,
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {
		if name == "session:activated" {
			emitted = true
		}
	}

	err := app.ExitWidgetMode("nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent tab")
	}
	if emitted {
		t.Fatal("session:activated must not be emitted when SetActiveTab fails")
	}
}
