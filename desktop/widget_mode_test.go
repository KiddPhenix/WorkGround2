package main

import (
	"strings"
	"testing"

	"workground2/internal/control"
	"workground2/internal/event"
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
			ID: "q", Prompt: "选择", Options: []event.AskOption{{Label: "一"}, {Label: "二"}, {Label: "三"}},
		}}}},
	})
	if !message.RequiresWindow || len(message.Options) != 0 {
		t.Fatalf("large choice should require main window: %#v", message)
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
