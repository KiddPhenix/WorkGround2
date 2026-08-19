package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/unread"
)

func TestBuildDesktopIconSnapshotKeepsReadConversationAndTwoRows(t *testing.T) {
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 7, Conversations: []unread.Conversation{{
		Key: "room:design", Source: unread.SourceRoom, SessionID: "room-session", Title: "产品 Room",
	}}}}
	spaces := []WidgetWorkspaceOption{{Scope: "auto", Name: "自动"}, {Scope: "project", Name: "WorkGround2", Root: `D:\Work\WorkGround2`}}
	snapshot := buildDesktopIconSnapshot(nil, state, spaces, desktopIconPersistedState{}, 1200, nil, nil)
	room := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if room == nil || room.Position.Row != "top" || room.UnreadCount != 0 {
		t.Fatalf("read Room projection = %#v", room)
	}
	workspace := findDesktopIconItem(snapshot.Items, `workspace:D:\Work\WorkGround2`)
	if workspace == nil || workspace.Position.Row != "bottom" || workspace.Position.Zone != "workspace" {
		t.Fatalf("workspace projection = %#v", workspace)
	}
	for _, id := range []string{"fixed:new", "fixed:workspace", "fixed:rooms", "fixed:delegate", "fixed:search"} {
		item := findDesktopIconItem(snapshot.Items, id)
		if item == nil || item.Position.Row != "bottom" || item.Position.Zone != "fixed" {
			t.Fatalf("fixed item %s = %#v", id, item)
		}
	}
	// The fixed bottom bar order is a Go contract: 新建 → 工作区 → Rooms → 委托 → 搜索.
	// Order comes from the declared slice index, never map iteration.
	wantOrder := []string{"new", "workspace", "rooms", "delegate", "search"}
	for order, sourceID := range wantOrder {
		item := findDesktopIconItem(snapshot.Items, "fixed:"+sourceID)
		if item == nil || item.SourceID != sourceID || item.Position.Order != order {
			t.Fatalf("fixed order %d = %#v, want sourceId %q order %d", order, item, sourceID, order)
		}
	}
	if findDesktopIconItem(snapshot.Items, "fixed:knowledge") != nil {
		t.Fatal("knowledge entry should stay hidden until the feature is ready")
	}
}

func TestDesktopIconWorkspaceFixedItemContract(t *testing.T) {
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil)
	workspace := findDesktopIconItem(snapshot.Items, "fixed:workspace")
	if workspace == nil {
		t.Fatal("fixed workspace icon is missing")
	}
	if workspace.Kind != "fixed" || workspace.SourceID != "workspace" || workspace.Icon != "workspace" {
		t.Fatalf("workspace fixed item = %#v, want kind fixed sourceId workspace icon workspace", workspace)
	}
	if workspace.Title != "工作区" {
		t.Fatalf("workspace fixed title = %q, want 工作区", workspace.Title)
	}
	if workspace.ActivityCount != 0 || workspace.UnreadCount != 0 {
		t.Fatalf("workspace fixed item must stay idle without badges: %#v", workspace)
	}
}

func TestDesktopIconWorkspaceOpenIsRejected(t *testing.T) {
	app := &App{}
	err := app.applyDesktopIconActionLocked(DesktopIconItem{
		ID: "fixed:workspace", Kind: "fixed", SourceID: "workspace",
	}, nil, DesktopIconActionInput{Action: "open"})
	if err == nil || !strings.Contains(err.Error(), "management dialog") {
		t.Fatalf("open on fixed workspace = %v, want explicit management-dialog guard", err)
	}
}

func TestDesktopIconRoomsFixedItemContract(t *testing.T) {
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil)
	rooms := findDesktopIconItem(snapshot.Items, "fixed:rooms")
	if rooms == nil {
		t.Fatal("fixed rooms icon is missing")
	}
	if rooms.Kind != "fixed" || rooms.SourceID != "rooms" || rooms.Icon != "rooms" {
		t.Fatalf("rooms fixed item = %#v, want kind fixed sourceId rooms icon rooms", rooms)
	}
	if rooms.Title != "Rooms" {
		t.Fatalf("rooms fixed title = %q, want Rooms", rooms.Title)
	}
	if rooms.ActivityCount != 0 || rooms.UnreadCount != 0 {
		t.Fatalf("rooms fixed item must stay idle without badges: %#v", rooms)
	}
	// The rooms icon sits between 工作区 and 委托 in the fixed bar.
	workspace := findDesktopIconItem(snapshot.Items, "fixed:workspace")
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if workspace == nil || delegate == nil || workspace.Position.Order+1 != rooms.Position.Order || rooms.Position.Order+1 != delegate.Position.Order {
		t.Fatalf("fixed bar order around rooms = workspace %#v rooms %#v delegate %#v", workspace, rooms, delegate)
	}
}

func TestDesktopIconRoomsOpenIsRejected(t *testing.T) {
	app := &App{}
	err := app.applyDesktopIconActionLocked(DesktopIconItem{
		ID: "fixed:rooms", Kind: "fixed", SourceID: "rooms",
	}, nil, DesktopIconActionInput{Action: "open"})
	if err == nil || !strings.Contains(err.Error(), "management dialog") {
		t.Fatalf("open on fixed rooms = %v, want explicit management-dialog guard", err)
	}
}

func TestBuildDesktopIconSnapshotSeparatesRuntimeFromUnread(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{ID: "task-1", SessionID: "session-1", WorkspaceName: "WG2", TopicTitle: "实现图标模式", RunningWork: true, ForegroundActive: true, ActivityStatus: topicStatusThinking, ActivityText: "正在核对真实运行状态", TurnStartedAt: time.Now().Add(-time.Second).UnixMilli()}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil)
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil || task.Runtime == nil || task.UnreadCount != 0 {
		t.Fatalf("running task = %#v", task)
	}
	if task.Status != "thinking" {
		t.Fatalf("status = %q, want thinking", task.Status)
	}
	if task.Runtime.Summary != "正在核对真实运行状态" {
		t.Fatalf("summary = %q, want live reasoning text", task.Runtime.Summary)
	}
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.UnreadCount != 0 || delegate.ActivityCount != 0 {
		t.Fatalf("delegate = %#v", delegate)
	}
}

func TestBuildDesktopIconSnapshotDistinguishesToolRunning(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{
		ID: "task-1", TopicTitle: "实现图标模式", RunningWork: true, ForegroundActive: true,
		RuntimeMode: string(control.RuntimeModeForeground), ActivityStatus: topicStatusRunning, ActivityText: "read_file 执行中",
	}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil)
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil || task.Runtime == nil || task.Status != "running" || task.Runtime.Phase != "Running" {
		t.Fatalf("tool-running task = %#v", task)
	}
	if task.Runtime.Summary != "read_file 执行中" {
		t.Fatalf("summary = %q, want real tool stage", task.Runtime.Summary)
	}
}

func TestDesktopIconRunningRevisionIgnoresElapsedProjection(t *testing.T) {
	item := DesktopIconItem{
		ID: "task:task-1", Kind: "task", Status: "running",
		Position: DesktopIconPosition{Row: "bottom", Zone: "running"},
		Runtime:  &DesktopIconRuntime{Phase: "Running", ElapsedMs: 1000, UpdatedAt: 10},
	}
	first := desktopIconItemRevision(item)
	item.Runtime.ElapsedMs = 9000
	item.Runtime.UpdatedAt = 99
	if second := desktopIconItemRevision(item); second != first {
		t.Fatalf("running revision changed with display-only time: %q != %q", second, first)
	}
}

func TestBuildDesktopIconSnapshotDoesNotDoubleCountTaskUnread(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "task-1", SessionID: "session-1", TopicTitle: "实现图标模式", NeedsAttention: true, NeedsAttentionAt: 12},
		resultText: "已经完成",
	}}
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 4, Conversations: []unread.Conversation{{
		Key: "session:session-1", Source: unread.SourceSession, SessionID: "session-1", LatestSequence: 7, UnreadCount: 1,
		Items: []unread.Item{{ID: "turn:1", Sequence: 7, Kind: "completed", OccurredAt: time.UnixMilli(12)}},
	}}}}
	snapshot := buildDesktopIconSnapshot(sources, state, nil, desktopIconPersistedState{}, 1200, nil, nil)
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil || task.UnreadCount != 1 || len(task.Notifications) != 1 {
		t.Fatalf("task unread projection = %#v", task)
	}
	if task.Notifications[0].Conversation != "session:session-1" || task.Notifications[0].ReadSequence != 7 {
		t.Fatalf("task watermark = %#v", task.Notifications[0])
	}
	if findDesktopIconItem(snapshot.Items, "conversation:session:session-1") != nil {
		t.Fatal("task unread was also projected as a separate conversation icon")
	}
}

func TestBuildDesktopIconSnapshotAggregatesDelegatedActivity(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{ID: "delegated", RunningWork: true, BackgroundOnly: true}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 1 || delegate.UnreadCount != 0 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v", delegate)
	}
	if findDesktopIconItem(snapshot.Items, "task:delegated") != nil {
		t.Fatal("delegated task received an independent running icon")
	}
}

func TestBuildDesktopIconSnapshotCountsRealRunningSubagents(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "task-1", SessionID: "session-1", WorkspaceName: "WG2", TopicTitle: "父任务", RunningWork: true, ForegroundActive: true, TurnStartedAt: time.Now().Add(-time.Second).UnixMilli()},
		sessionDir: "dir-a",
		branchID:   "branch-a",
	}}
	counts := map[widgetSubagentKey]int{newWidgetSubagentKey("dir-a", "branch-a"): 2}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, counts)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 2 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v, want activity 2 running", delegate)
	}
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil {
		t.Fatal("foreground parent lost its own task icon")
	}
}

func TestBuildDesktopIconSnapshotRealSubagentsDoNotDoubleCountBackgroundCompat(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "background-1", RunningWork: true, BackgroundOnly: true},
		sessionDir: "dir-b",
		branchID:   "branch-b",
	}}
	counts := map[widgetSubagentKey]int{newWidgetSubagentKey("dir-b", "branch-b"): 2}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, counts)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 2 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v, want activity 2 (not 3) running", delegate)
	}
}

func TestBuildDesktopIconSnapshotRealSubagentsSurviveBackgroundTurnEnd(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "background-1", BackgroundOnly: true}, // RunningWork=false: own turn ended
		sessionDir: "dir-b",
		branchID:   "branch-b",
	}}
	counts := map[widgetSubagentKey]int{newWidgetSubagentKey("dir-b", "branch-b"): 2}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, counts)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 2 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v, want real sub-agents counted after background turn ended", delegate)
	}
}

func TestBuildDesktopIconSnapshotIgnoresSubagentsOfInactiveSessions(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "idle-1", SessionID: "session-1", TopicTitle: "空闲任务"},
		sessionDir: "dir-idle",
		branchID:   "branch-idle",
	}}
	counts := map[widgetSubagentKey]int{newWidgetSubagentKey("dir-idle", "branch-other"): 3}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, counts)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 0 || delegate.Status != "idle" {
		t.Fatalf("delegate = %#v, want idle without activity", delegate)
	}
}

func TestWidgetSubagentScanSurfacesCorruptMeta(t *testing.T) {
	dir := t.TempDir()
	subagentDir := filepath.Join(dir, "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ref := "sa_20260102_030405_000000000_aabbccddeeff"
	if err := os.WriteFile(filepath.Join(subagentDir, ref+".meta.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	sources := []widgetSource{{sessionDir: dir}}
	counts, err := app.widgetSubagentCounts(sources)
	if err == nil || !strings.Contains(err.Error(), "decode subagent metadata") {
		t.Fatalf("widgetSubagentCounts error = %v, counts = %v; want decode error surfaced", err, counts)
	}
	if counts[newWidgetSubagentKey(dir, ref)] != 0 {
		t.Fatalf("corrupt meta must not count as running: %v", counts)
	}
}

func TestWidgetSubagentScanKeepsCountsDespiteCorruptSibling(t *testing.T) {
	dir := t.TempDir()
	subagentDir := filepath.Join(dir, "subagents")
	if err := os.MkdirAll(subagentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	good := "sa_20260102_030405_000000000_aabbccddeeff"
	writeRunningSubagentMeta(t, filepath.Join(subagentDir, good+".meta.json"), "branch-a")
	bad := "sa_20260102_030405_000000000_112233445566"
	if err := os.WriteFile(filepath.Join(subagentDir, bad+".meta.json"), []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	sources := []widgetSource{{sessionDir: dir}}
	counts, err := app.widgetSubagentCounts(sources)
	if err == nil || !strings.Contains(err.Error(), "decode subagent metadata") {
		t.Fatalf("widgetSubagentCounts error = %v, counts = %v; want decode error surfaced", err, counts)
	}
	if counts[newWidgetSubagentKey(dir, "branch-a")] != 1 {
		t.Fatalf("counts = %v, want usable counts kept despite corrupt sibling", counts)
	}
}

func TestWidgetSubagentScanScansEachUniqueDirOnce(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	for dir, parent := range map[string]string{dirA: "branch-a", dirB: "branch-b"} {
		subagentDir := filepath.Join(dir, "subagents")
		if err := os.MkdirAll(subagentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRunningSubagentMeta(t, filepath.Join(subagentDir, "sa_20260102_030405_000000000_aabbccddeeff.meta.json"), parent)
	}
	app := &App{}
	sources := []widgetSource{{sessionDir: dirA, branchID: "branch-a"}, {sessionDir: dirA, branchID: "branch-dup"}, {sessionDir: dirB, branchID: "branch-b"}}
	counts, err := app.widgetSubagentCounts(sources)
	if err != nil {
		t.Fatalf("widgetSubagentCounts: %v", err)
	}
	if counts[newWidgetSubagentKey(dirA, "branch-a")] != 1 || counts[newWidgetSubagentKey(dirB, "branch-b")] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %v, want branch-a:1 branch-b:1", counts)
	}
}

func TestWidgetSubagentCountsKeepSessionDirsIsolated(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	for _, dir := range []string{dirA, dirB} {
		subagentDir := filepath.Join(dir, "subagents")
		if err := os.MkdirAll(subagentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRunningSubagentMeta(t, filepath.Join(subagentDir, "sa_20260102_030405_000000000_aabbccddeeff.meta.json"), "same-branch")
	}
	app := &App{}
	counts, err := app.widgetSubagentCounts([]widgetSource{{sessionDir: dirA}, {sessionDir: dirB}})
	if err != nil {
		t.Fatalf("widgetSubagentCounts: %v", err)
	}
	if counts[newWidgetSubagentKey(dirA, "same-branch")] != 1 || counts[newWidgetSubagentKey(dirB, "same-branch")] != 1 || len(counts) != 2 {
		t.Fatalf("counts = %v, want one isolated count per session dir", counts)
	}
}

func writeRunningSubagentMeta(t *testing.T, metaPath, parentSession string) {
	t.Helper()
	data := []byte(`{"ref":"sa_x","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","status":"running","kind":"task","name":"n","workspaceRoot":"","parentSession":"` + parentSession + `","systemPromptHash":"","toolScope":[],"toolSchemaHash":"","model":"","effort":""}` + "\n")
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDesktopIconNoticesPriorityAndFIFO(t *testing.T) {
	notices := []DesktopIconNotice{{ID: "message", Priority: 3, CreatedAt: 1}, {ID: "newer-input", Priority: 1, CreatedAt: 3}, {ID: "older-input", Priority: 1, CreatedAt: 2}, {ID: "confirm", Priority: 2, CreatedAt: 0}}
	sortDesktopIconNotices(notices)
	want := []string{"older-input", "newer-input", "confirm", "message"}
	for i := range want {
		if notices[i].ID != want[i] {
			t.Fatalf("notice[%d] = %q, want %q", i, notices[i].ID, want[i])
		}
	}
}

func TestDesktopIconPositionKeepsArchitectureZones(t *testing.T) {
	task := DesktopIconItem{Kind: "task"}
	if !validDesktopIconPosition(task, DesktopIconPosition{Row: "bottom", Zone: "running", Order: 3}) {
		t.Fatal("valid task position rejected")
	}
	if validDesktopIconPosition(task, DesktopIconPosition{Row: "top", Zone: "conversation", Order: 0}) {
		t.Fatal("task escaped into conversation row")
	}
	room := DesktopIconItem{Kind: "room"}
	if validDesktopIconPosition(room, DesktopIconPosition{Row: "bottom", Zone: "running", Order: 0}) {
		t.Fatal("Room escaped into bottom row")
	}
}

func TestNormalizeDesktopIconRectsClampsAndDropsInvalid(t *testing.T) {
	got := normalizeDesktopIconRects([]DesktopIconRect{{X: -5, Y: 10, Width: 20, Height: 20}, {X: 90, Y: 90, Width: 30, Height: 30}, {Width: 0, Height: 4}}, 100, 100)
	want := []DesktopIconRect{{X: 0, Y: 10, Width: 15, Height: 20}, {X: 90, Y: 90, Width: 10, Height: 10}}
	if len(got) != len(want) {
		t.Fatalf("rects = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rect[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestDesktopIconCompletionReceiptCarriesRecoveryIdentity(t *testing.T) {
	receipt := desktopIconReceipt{RequestID: "request-1", Intent: "intent", Status: "pending", Action: "dismiss", ItemID: "task:tab-1", TabID: "tab-1"}
	if receipt.Status != "pending" || receipt.Action != "dismiss" || receipt.ItemID == "" || receipt.TabID == "" {
		t.Fatalf("receipt cannot recover: %#v", receipt)
	}
}

func TestDesktopIconSearchIncludesCappedAndDismissedHistory(t *testing.T) {
	infos := make([]agent.SessionInfo, 0, 10)
	for i := 0; i < 9; i++ {
		infos = append(infos, agent.SessionInfo{Path: filepath.Join(`D:\sessions`, string(rune('a'+i))+".jsonl"), TopicTitle: "recent task", LastActivityAt: time.Unix(int64(100-i), 0)})
	}
	infos = append(infos, agent.SessionInfo{Path: `D:\sessions\dismissed.jsonl`, TopicTitle: "dismissed lunar task", Preview: "saved after Dismiss", SessionKind: agent.SessionKindWork, LastActivityAt: time.Unix(1, 0)})
	items := buildDesktopIconSearchItems("lunar", infos, []WidgetWorkspaceOption{{Scope: "project", Name: "Lunar Workspace", Root: `D:\Work\Lunar`}})
	if len(items) != 2 {
		t.Fatalf("search items = %+v", items)
	}
	if items[0].Kind != "workspace" && items[1].Kind != "workspace" {
		t.Fatalf("workspace was not indexed: %+v", items)
	}
	if items[0].Kind != "task" && items[1].Kind != "task" {
		t.Fatalf("dismissed/capped task was not indexed: %+v", items)
	}
}

func TestDesktopIconReorderUsesDenseStableInsertion(t *testing.T) {
	items := []DesktopIconItem{
		{ID: "fixed:a", Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 0}},
		{ID: "fixed:b", Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 0}},
		{ID: "fixed:c", Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 7}},
		{ID: "workspace:x", Position: DesktopIconPosition{Row: "bottom", Zone: "workspace", Order: 0}},
	}
	positions := map[string]DesktopIconPosition{}
	reorderDesktopIconItems(items, "fixed:c", DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 1}, positions)
	if positions["fixed:a"].Order != 0 || positions["fixed:c"].Order != 1 || positions["fixed:b"].Order != 2 {
		t.Fatalf("fixed reorder = %+v", positions)
	}
	if _, ok := positions["workspace:x"]; ok {
		t.Fatalf("reorder escaped its zone: %+v", positions)
	}
}

func TestDesktopIconReorderKeepsHiddenPersistedItems(t *testing.T) {
	items := []DesktopIconItem{
		{ID: "task:a", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 0}},
		{ID: "task:c", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 2}},
	}
	positions := map[string]DesktopIconPosition{
		"task:hidden": {Row: "bottom", Zone: "running", Order: 1},
		"task:stale":  {Row: "bottom", Zone: "running", Order: 8},
	}
	reorderDesktopIconItems(items, "task:c", DesktopIconPosition{Row: "bottom", Zone: "running", Order: 1}, positions)
	if positions["task:a"].Order != 0 || positions["task:c"].Order != 1 || positions["task:hidden"].Order != 2 || positions["task:stale"].Order != 3 {
		t.Fatalf("hidden reorder = %+v", positions)
	}
	reappeared := []DesktopIconItem{
		{ID: "task:a", Position: positions["task:a"]},
		{ID: "task:c", Position: positions["task:c"]},
		{ID: "task:hidden", Position: positions["task:hidden"]},
	}
	sort.Slice(reappeared, func(i, j int) bool { return reappeared[i].Position.Order < reappeared[j].Position.Order })
	if reappeared[0].ID != "task:a" || reappeared[1].ID != "task:c" || reappeared[2].ID != "task:hidden" {
		t.Fatalf("reappeared order = %+v", reappeared)
	}
}

func TestDesktopIconReplyRecoveryStateMachine(t *testing.T) {
	if step, err := desktopIconReplyNextStep([]string{"before"}, 1, "hello", "accepted", true); err != nil || step != desktopIconReplyWaitStep {
		t.Fatalf("accepted before history = step %q, err %v", step, err)
	}
	receipt := desktopIconReceipt{Status: "pending", Delivery: "accepted"}
	applyDesktopIconReplyProgress(&receipt, desktopIconReplyAccepted)
	if receipt.Status != "pending" {
		t.Fatalf("accepted reply became %q before history confirmation", receipt.Status)
	}
	applyDesktopIconReplyProgress(&receipt, desktopIconReplyConfirmed)
	if receipt.Status != "applied" {
		t.Fatalf("confirmed reply stayed %q", receipt.Status)
	}
	if step, err := desktopIconReplyNextStep([]string{"before", "hello"}, 1, "hello", "accepted", false); err != nil || step != desktopIconReplyConfirmStep {
		t.Fatalf("history confirmation = step %q, err %v", step, err)
	}
	if step, err := desktopIconReplyNextStep([]string{"before"}, 1, "hello", "accepted", false); err != nil || step != desktopIconReplySubmitStep {
		t.Fatalf("restart without history = step %q, err %v", step, err)
	}
	if _, err := desktopIconReplyNextStep([]string{"before", "other"}, 1, "hello", "accepted", false); err == nil {
		t.Fatal("conflicting history should stop a duplicate reply")
	}
}

func TestDesktopIconTaskContinueRecoveryStateMachine(t *testing.T) {
	if step, err := desktopIconTurnNextStep([]string{"before"}, 1, "continue", "accepted", true, "task conversation"); err != nil || step != desktopIconReplyWaitStep {
		t.Fatalf("running continuation = step %q, err %v", step, err)
	}
	if step, err := desktopIconTurnNextStep([]string{"before", "continue"}, 1, "continue", "accepted", false, "task conversation"); err != nil || step != desktopIconReplyConfirmStep {
		t.Fatalf("confirmed continuation = step %q, err %v", step, err)
	}
	if step, err := desktopIconTurnNextStep([]string{"before"}, 1, "continue", "accepted", false, "task conversation"); err != nil || step != desktopIconReplySubmitStep {
		t.Fatalf("restart continuation = step %q, err %v", step, err)
	}
	if _, err := desktopIconTurnNextStep([]string{"before", "other"}, 1, "continue", "accepted", false, "task conversation"); err == nil || !strings.Contains(err.Error(), "task conversation") {
		t.Fatalf("conflicting continuation history = %v", err)
	}
}

func TestDesktopIconReplyBusinessKeyReusesOldRequest(t *testing.T) {
	key := desktopIconReplyKey("im:alice", 9, " hello   world ")
	receipts := []desktopIconReceipt{{RequestID: "old-request", Status: "pending", Action: "reply", Conversation: "im:alice", ReadSequence: 9, Text: "hello world"}}
	if index := desktopIconReplyReceiptIndex(receipts, key); index != 0 {
		t.Fatalf("reply receipt index = %d, want old pending receipt", index)
	}
	if other := desktopIconReplyKey("im:alice", 10, "hello world"); desktopIconReplyReceiptIndex(receipts, other) != -1 {
		t.Fatal("a different unread sequence reused the old receipt")
	}
}

func TestDesktopIconReplyNewRequestReturnsAlreadyApplied(t *testing.T) {
	t.Setenv("WorkGround2_HOME", t.TempDir())
	key := desktopIconReplyKey("im:alice", 9, "hello")
	app := &App{iconWidgetStateLoaded: true, iconWidgetState: desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{},
		Applied: []desktopIconReceipt{{RequestID: "old-request", Status: "applied", Action: "reply", Conversation: "im:alice", ReadSequence: 9, Text: "hello", ReplyKey: key}},
	}}
	result, found := app.resumeDesktopIconReplyLocked(key)
	if !found || result.Status != "already_applied" || len(app.iconWidgetState.Applied) != 1 {
		t.Fatalf("new request continuation = found %v, result %+v, receipts %+v", found, result, app.iconWidgetState.Applied)
	}
}

func TestDesktopIconCorruptStateIsVisibleInSnapshot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	path := desktopIconStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	snapshot := app.GetDesktopIconSnapshot()
	if !strings.Contains(snapshot.Error, "load desktop icon state") {
		t.Fatalf("snapshot error = %q", snapshot.Error)
	}
}

func TestPinNewDesktopIconTaskOrdersAppendsToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	app := &App{iconWidgetStateLoaded: true, iconWidgetState: desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{
			"task:old": {Row: "bottom", Zone: "running", Order: 0},
		},
	}}
	snapshot := DesktopIconSnapshot{Items: []DesktopIconItem{
		{ID: "task:old", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 0}},
		{ID: "task:new", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 5}},
		{ID: "fixed:new", Kind: "fixed", Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 0}},
	}}
	if !app.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		t.Fatal("new task icon was not pinned")
	}
	got, ok := app.iconWidgetState.Positions["task:new"]
	if !ok || got.Row != "bottom" || got.Zone != "running" || got.Order != 1 {
		t.Fatalf("pinned position = %+v ok=%v, want running/order 1", got, ok)
	}
	if old := app.iconWidgetState.Positions["task:old"]; old.Order != 0 {
		t.Fatalf("existing pinned icon changed = %+v", old)
	}
	// Idempotent: a second call with no new unpinned tasks must be a no-op.
	if app.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		t.Fatal("second pin call must be a no-op")
	}
}

func TestPinNewDesktopIconTaskOrdersSkipsRetained(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	app := &App{iconWidgetStateLoaded: true, iconWidgetState: desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{},
	}}
	snapshot := DesktopIconSnapshot{Items: []DesktopIconItem{
		{ID: "task:kept", Kind: "task", Retained: true, Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 3}},
	}}
	if app.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		t.Fatal("retained icons must not be pinned")
	}
	if _, ok := app.iconWidgetState.Positions["task:kept"]; ok {
		t.Fatalf("retained icon was pinned: %+v", app.iconWidgetState.Positions)
	}
}

func TestGetDesktopIconSnapshotPinsNewTaskOrderStably(t *testing.T) {
	tab, _ := completionTestTab(t, 1000) // NeedsAttention makes the task visible in the snapshot
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	// A fresh task (no prior drag) must be pinned into Positions on first
	// snapshot so its order never re-derives from the map iteration again.
	first := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(first.Items, "task:task-1")
	if item == nil {
		t.Fatalf("task icon missing from snapshot: %+v", first.Items)
	}
	pinned, ok := app.iconWidgetState.Positions["task:task-1"]
	if !ok || pinned.Row != "bottom" || pinned.Zone != "running" {
		t.Fatalf("task icon was not pinned: %+v ok=%v", pinned, ok)
	}
	second := app.GetDesktopIconSnapshot()
	again := findDesktopIconItem(second.Items, "task:task-1")
	if again == nil || again.Position != item.Position {
		t.Fatalf("task order changed across snapshots: first %+v second %+v", item.Position, again.Position)
	}
	if again.Position != pinned {
		t.Fatalf("rendered position %+v differs from pinned %+v", again.Position, pinned)
	}
}

func TestDesktopIconWindowStateRejectsOldShortGeometryWithError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	path := desktopIconWindowStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"width":900,"height":360,"x":0,"y":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadDesktopIconWindowState(); ok || err == nil || !strings.Contains(err.Error(), "below") {
		t.Fatalf("old icon geometry = ok %v, err %v", ok, err)
	}
	if err := os.WriteFile(path, []byte(`{bad json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := loadDesktopIconWindowState(); ok || err == nil || !strings.Contains(err.Error(), "load desktop icon window state") {
		t.Fatalf("corrupt icon geometry = ok %v, err %v", ok, err)
	}
}

func TestDesktopIconWindowStateMigratesLegacyDefaultGeometry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	path := desktopIconWindowStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"width":900,"height":600,"x":120,"y":80}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := loadDesktopIconWindowState(); ok || err != nil || got != (WidgetWindowState{}) {
		t.Fatalf("legacy icon geometry = %+v ok %v err %v, want default recompute", got, ok, err)
	}
}

func TestSaveCurrentWindowStateRoutesIconsToIconWindowState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	state := DesktopWindowState{Width: desktopIconWidth, Height: desktopIconHeight, X: 120, Y: 80}
	if err := saveCurrentWindowStateTo(true, "icons", state); err != nil {
		t.Fatalf("icons geometry save: %v", err)
	}
	got, ok, err := loadDesktopIconWindowState()
	if !ok || err != nil {
		t.Fatalf("icons geometry did not reach icon window state: ok %v err %v", ok, err)
	}
	if got.Width != desktopIconWidth || got.Height != desktopIconHeight || got.X != 120 || got.Y != 80 {
		t.Fatalf("icons state = %+v", got)
	}
	if _, ok := loadWidgetWindowState(); ok {
		t.Fatal("icons geometry leaked into pager window state")
	}
	if _, ok := loadWindowState(); ok {
		t.Fatal("icons geometry leaked into main window state")
	}
}

func TestSaveCurrentWindowStateRoutesPagerAndMain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)

	pager := DesktopWindowState{Width: 590, Height: 176, X: 10, Y: 20}
	if err := saveCurrentWindowStateTo(true, "pager", pager); err != nil {
		t.Fatalf("pager geometry save: %v", err)
	}
	if got, ok := loadWidgetWindowState(); !ok || got.Width != 590 || got.Height != 176 || got.X != 10 || got.Y != 20 {
		t.Fatalf("pager state = %+v ok %v", got, ok)
	}
	if _, ok, _ := loadDesktopIconWindowState(); ok {
		t.Fatal("pager geometry leaked into icon window state")
	}

	main := DesktopWindowState{Width: 1280, Height: 800, X: 30, Y: 40}
	if err := saveCurrentWindowStateTo(false, "", main); err != nil {
		t.Fatalf("main geometry save: %v", err)
	}
	if got, ok := loadWindowState(); !ok || got.Width != 1280 || got.Height != 800 || got.X != 30 || got.Y != 40 {
		t.Fatalf("main state = %+v ok %v", got, ok)
	}
	if got, ok := loadWidgetWindowState(); !ok || got.Width != 590 {
		t.Fatalf("main geometry overwrote pager window state: %+v ok %v", got, ok)
	}
	if _, ok, _ := loadDesktopIconWindowState(); ok {
		t.Fatal("main geometry leaked into icon window state")
	}
}

func findDesktopIconItem(items []DesktopIconItem, id string) *DesktopIconItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}

func TestCloneDesktopIconStateKeepsSummaryCacheIndependent(t *testing.T) {
	state := desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{},
		Kept:      map[string]desktopIconKept{},
		CompletionSummaries: map[string]desktopIconCompletionSummary{
			"summary": {Status: completionSummaryReady, Text: "新闻体摘要"},
		},
	}
	clone := cloneDesktopIconState(state)
	delete(clone.CompletionSummaries, "summary")
	if state.CompletionSummaries["summary"].Text != "新闻体摘要" {
		t.Fatal("clone shares the summary cache map with authoritative state")
	}
}

// keptFixture returns a completed task whose item is also retained in the
// persisted Kept map, mirroring the pre-fix state that "OK" used to create.
func keptCompletionFixture(t *testing.T, attentionAt int64) (*App, *WorkspaceTab, string) {
	t.Helper()
	tab, sp := completionTestTab(t, attentionAt)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.iconWidgetState.Kept["task:task-1"] = desktopIconKept{
		ItemID: "task:task-1", SourceID: "task-1", Title: "标题", Summary: "旧摘要", Order: 0, Revision: "r",
	}
	return app, tab, sp
}

func completedIconItem(t *testing.T, app *App) (DesktopIconItem, DesktopIconNotice) {
	t.Helper()
	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:task-1")
	if item == nil || len(item.Notifications) != 1 || item.Notifications[0].Kind != "completed" {
		t.Fatalf("completed task projection = %+v", snapshot.Items)
	}
	return *item, item.Notifications[0]
}

func TestDesktopIconOkActionIsLocalCloseOnly(t *testing.T) {
	app, _, sp := keptCompletionFixture(t, 1000)
	item, notice := completedIconItem(t, app)

	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, NoticeID: notice.ID, Revision: item.Revision,
		RequestID: "req-ok", Action: "ok",
	})
	if result.Status != "accepted" {
		t.Fatalf("ok status = %q error %q", result.Status, result.Error)
	}
	if len(app.iconWidgetState.Applied) != 0 {
		t.Fatalf("ok created a backend receipt: %+v", app.iconWidgetState.Applied)
	}
	if kept := app.iconWidgetState.Kept["task:task-1"]; kept.Title != "标题" {
		t.Fatalf("ok changed the kept item: %+v", kept)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil || !meta.NeedsAttention {
		t.Fatalf("ok cleared completion attention: %v meta %+v", err, meta)
	}
	// Reopening the same item still shows the same summary notice and buttons.
	again, noticeAgain := completedIconItem(t, app)
	if again.Revision != item.Revision || noticeAgain.ID != notice.ID || noticeAgain.Body != notice.Body {
		t.Fatalf("reopen changed the notice: %+v vs %+v", noticeAgain, notice)
	}
}

func TestDesktopIconDismissClearsAndStaysRecoverable(t *testing.T) {
	app, _, sp := keptCompletionFixture(t, 1000)
	item, notice := completedIconItem(t, app)

	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, NoticeID: notice.ID, Revision: item.Revision,
		RequestID: "req-dismiss", Action: "dismiss",
	})
	if result.Status != "accepted" {
		t.Fatalf("dismiss status = %q error %q", result.Status, result.Error)
	}
	if _, kept := app.iconWidgetState.Kept["task:task-1"]; kept {
		t.Fatal("dismiss kept the completion item")
	}
	// The receipt settles to "applied" on success; "pending" only exists in
	// the crash window between the receipt save and the attention clear.
	if len(app.iconWidgetState.Applied) != 1 || app.iconWidgetState.Applied[0].Action != "dismiss" || app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("dismiss receipt = %+v", app.iconWidgetState.Applied)
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil || meta.NeedsAttention {
		t.Fatalf("dismiss did not clear attention: %v meta %+v", err, meta)
	}

	// Restart recovery: the pending dismiss receipt is applied idempotently
	// without error, so a crash between receipt and attention-clear is safe.
	restarted := &App{
		tabs:                      app.tabs,
		activeTabID:               app.activeTabID,
		iconWidgetStateLoaded:     true,
		iconWidgetState:           desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}, Applied: append([]desktopIconReceipt(nil), app.iconWidgetState.Applied...)},
		completionSummaryInFlight: map[string]*completionSummaryCall{},
	}
	if err := restarted.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("recover pending dismiss: %v", err)
	}
	if restarted.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("dismiss receipt did not settle: %+v", restarted.iconWidgetState.Applied[0])
	}
}

func TestDesktopIconPendingOkReceiptRecoveryCompatible(t *testing.T) {
	tab, sp := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	// A legacy frontend (pre-fix) persisted a pending "ok" receipt that was
	// never settled. Startup recovery must still finish it exactly like a
	// dismiss: clear attention and mark it applied.
	app.iconWidgetState.Applied = []desktopIconReceipt{{
		RequestID: "legacy-ok", Intent: "intent", Status: "pending", Action: "ok",
		ItemID: "task:task-1", TabID: "task-1", AppliedAt: time.Now().UnixMilli(),
	}}
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("recover legacy pending ok: %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("legacy ok receipt did not settle: %+v", app.iconWidgetState.Applied[0])
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil || meta.NeedsAttention {
		t.Fatalf("legacy ok receipt did not clear attention: %v meta %+v", err, meta)
	}
}
func TestRememberDesktopIconTaskRetainsOpenedTask(t *testing.T) {
	tab, _ := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = true

	app.rememberDesktopIconTask(tab.ID)

	kept, ok := app.iconWidgetState.Kept["task:"+tab.ID]
	if !ok {
		t.Fatalf("opened task was not retained: %+v", app.iconWidgetState.Kept)
	}
	if kept.SourceID != tab.ID {
		t.Fatalf("kept sourceID = %q, want %q", kept.SourceID, tab.ID)
	}
	if kept.Title == "" {
		t.Fatal("kept title is empty")
	}
	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:"+tab.ID)
	if item == nil {
		t.Fatalf("retained icon missing from snapshot: %+v", snapshot.Items)
	}
	if item.Kind != "task" || !item.Retained || item.Status != "done" {
		t.Fatalf("retained icon = kind %q retained %v status %q, want task/done/true", item.Kind, item.Retained, item.Status)
	}
}

func TestRememberDesktopIconTaskIdempotentKeepsOriginalSummary(t *testing.T) {
	tab, _ := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = true

	app.rememberDesktopIconTask(tab.ID)
	app.iconWidgetState.Kept["task:"+tab.ID] = desktopIconKept{
		ItemID: "task:" + tab.ID, SourceID: tab.ID, Title: "原标题", Summary: "原摘要", Order: 3,
	}
	app.rememberDesktopIconTask(tab.ID)

	kept := app.iconWidgetState.Kept["task:"+tab.ID]
	if kept.Summary != "原摘要" || kept.Title != "原标题" || kept.Order != 3 {
		t.Fatalf("re-retain overwrote the kept entry: %+v", kept)
	}
}

func TestRememberDesktopIconTaskSkipsCliAndCollaboration(t *testing.T) {
	cliTab, cliSP := completionTestTab(t, 0)
	cliTab.ID = "cli-tab"
	cliMeta, _, err := agent.LoadBranchMeta(cliSP)
	if err != nil {
		t.Fatalf("load cli branch meta: %v", err)
	}
	cliMeta.SessionSource = "cli"
	if err := agent.SaveBranchMeta(cliSP, cliMeta); err != nil {
		t.Fatalf("stamp cli source: %v", err)
	}
	collabTab, _ := completionTestTab(t, 0)
	collabTab.ID = "collab-tab"
	collabTab.sessionKind = agent.SessionKindCollaboration
	app := newSummaryTestApp(t, cliTab, fakeCompletionSummaryGenerator{})
	app.tabs[collabTab.ID] = collabTab
	app.widgetMode = true

	app.rememberDesktopIconTask(cliTab.ID)
	app.rememberDesktopIconTask(collabTab.ID)

	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("cli/collaboration tabs were retained: %+v", app.iconWidgetState.Kept)
	}
}

func TestRememberDesktopIconTaskRequiresWidgetModeAndKnownTab(t *testing.T) {
	tab, _ := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = false

	app.rememberDesktopIconTask(tab.ID)
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("retained while not in widget mode: %+v", app.iconWidgetState.Kept)
	}
	app.rememberDesktopIconTask("")
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("retained an empty tab id: %+v", app.iconWidgetState.Kept)
	}
	app.widgetMode = true
	app.rememberDesktopIconTask("missing-tab")
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("retained an unknown tab: %+v", app.iconWidgetState.Kept)
	}
}

func TestDesktopIconRemoveDeletesRetainedIcon(t *testing.T) {
	tab, _ := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = true
	app.rememberDesktopIconTask(tab.ID)
	item := findDesktopIconItem(app.GetDesktopIconSnapshot().Items, "task:"+tab.ID)
	if item == nil {
		t.Fatal("retained icon missing before remove")
	}

	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-remove", Action: "remove",
	})
	if result.Status != "accepted" {
		t.Fatalf("remove status = %q error %q", result.Status, result.Error)
	}
	if _, kept := app.iconWidgetState.Kept["task:"+tab.ID]; kept {
		t.Fatal("remove kept the retained icon")
	}
	if findDesktopIconItem(result.Snapshot.Items, "task:"+tab.ID) != nil {
		t.Fatal("removed icon still in snapshot")
	}
}

// TestDesktopIconOpenActivatesRespectiveTab reproduces the icon→session
// binding contract: opening two different task icons must activate their own
// tabs, and the second open must never fall back to the first icon's session.
func TestDesktopIconOpenActivatesRespectiveTab(t *testing.T) {
	tabA, spA := completionTestTab(t, 1000)
	tabB, spB := completionTestTab(t, 1000)
	tabA.ID = "task-a"
	tabB.ID = "task-b"
	app := newSummaryTestApp(t, tabA, fakeCompletionSummaryGenerator{})
	app.tabs[tabB.ID] = tabB
	app.sessionDirsOverride = []string{filepath.Dir(spA), filepath.Dir(spB)}
	app.activeTabID = tabA.ID
	app.widgetMode = true
	app.ctx = context.Background()
	app.widgetWindowOps = &widgetWindowOps{
		read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
		restoreMain: func(DesktopWindowState, bool) error { return nil },
		applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
	}
	app.widgetTaskbarToggle = func(bool) error { return nil }
	var activated []string
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {
		if name == "session:activated" {
			activated = append(activated, app.activeTabID)
		}
	}

	snapshot := app.GetDesktopIconSnapshot()
	itemA := findDesktopIconItem(snapshot.Items, "task:task-a")
	itemB := findDesktopIconItem(snapshot.Items, "task:task-b")
	if itemA == nil || itemB == nil {
		t.Fatalf("both task icons must be visible: %+v", snapshot.Items)
	}
	if itemA.SourceID != "task-a" || itemB.SourceID != "task-b" {
		t.Fatalf("icon source ids = %q %q, want task-a/task-b", itemA.SourceID, itemB.SourceID)
	}
	// Live task icons carry the backend-generated session ref that routes the
	// open, not the tab SourceID.
	if itemA.SessionRef == nil || itemA.SessionRef.SessionPath != spA || itemB.SessionRef == nil || itemB.SessionRef.SessionPath != spB {
		t.Fatalf("live task session refs = %#v / %#v, want paths %q / %q", itemA.SessionRef, itemB.SessionRef, spA, spB)
	}

	// Open icon A (already the active tab) then icon B: B must become active.
	// Re-read the snapshot first so the action submits the current revision.
	snapshot = app.GetDesktopIconSnapshot()
	itemB = findDesktopIconItem(snapshot.Items, "task:task-b")
	if itemB == nil {
		t.Fatalf("task B icon missing before open: %+v", snapshot.Items)
	}
	if result := app.ApplyDesktopIconAction(DesktopIconActionInput{ItemID: itemB.ID, Revision: itemB.Revision, RequestID: "req-open-b", Action: "open"}); result.Status != "accepted" {
		t.Fatalf("open B status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != "task-b" {
		t.Fatalf("active tab after opening B = %q, want task-b", app.activeTabID)
	}
	if sessionRuntimeKey(app.tabs[app.activeTabID].currentSessionPath()) != sessionRuntimeKey(spB) {
		t.Fatalf("active session after opening B = %q, want %q", app.tabs[app.activeTabID].currentSessionPath(), spB)
	}
	// Re-enter widget mode and re-read the fresh snapshot (async completion
	// summaries can advance item revisions), then open icon A: must switch
	// back to A's session.
	app.widgetMode = true
	app.activeTabID = "task-b"
	snapshot = app.GetDesktopIconSnapshot()
	itemA = findDesktopIconItem(snapshot.Items, "task:task-a")
	if itemA == nil {
		t.Fatalf("task A icon missing after reopen: %+v", snapshot.Items)
	}
	if result := app.ApplyDesktopIconAction(DesktopIconActionInput{ItemID: itemA.ID, Revision: itemA.Revision, RequestID: "req-open-a", Action: "open"}); result.Status != "accepted" {
		t.Fatalf("open A status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != "task-a" {
		t.Fatalf("active tab after opening A = %q, want task-a", app.activeTabID)
	}
	if sessionRuntimeKey(app.tabs[app.activeTabID].currentSessionPath()) != sessionRuntimeKey(spA) {
		t.Fatalf("active session after opening A = %q, want %q", app.tabs[app.activeTabID].currentSessionPath(), spA)
	}
	if len(activated) != 2 || activated[0] != "task-b" || activated[1] != "task-a" {
		t.Fatalf("session:activated payloads = %+v, want [task-b task-a]", activated)
	}
}

// TestDesktopIconOpenRetainedIconWithMissingTab verifies the durable-identity
// path: a retained task icon whose tab was closed reopens the same session
// through its recorded identity — reusing an existing tab for that session —
// instead of falling back to the previously active session.
func TestDesktopIconOpenRetainedIconWithMissingTab(t *testing.T) {
	tab, sp := completionTestTab(t, 0)
	app := &App{
		// The kept icon points at "task-a" whose tab is gone; a live tab
		// "task-1" still owns the same session path.
		tabs:                  map[string]*WorkspaceTab{tab.ID: tab},
		activeTabID:           "stale-active",
		widgetMode:            true,
		ctx:                   context.Background(),
		sessionDirsOverride:   []string{filepath.Dir(sp)},
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{},
			Kept: map[string]desktopIconKept{
				"task:task-a": {
					ItemID: "task:task-a", SourceID: "task-a", Title: "任务A", Order: 0,
					Scope: "global", TopicID: "topic-a", SessionPath: sp,
				},
			},
			CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
		widgetWindowOps: &widgetWindowOps{
			read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
			restoreMain: func(DesktopWindowState, bool) error { return nil },
			applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
		},
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:task-a")
	if item == nil {
		t.Fatalf("retained icon missing: %+v", snapshot.Items)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-open-kept", Action: "open",
	})
	if result.Status != "accepted" {
		t.Fatalf("open retained icon status = %q error %q", result.Status, result.Error)
	}
	active := app.tabs[app.activeTabID]
	if active == nil {
		t.Fatalf("no tab behind active id %q", app.activeTabID)
	}
	if sessionRuntimeKey(active.currentSessionPath()) != sessionRuntimeKey(sp) {
		t.Fatalf("opened session path %q, want %q", active.currentSessionPath(), sp)
	}
}

// TestDesktopIconOpenRetainedIconWithoutIdentityFailsExplicitly pins the
// failure mode for kept entries recorded before session identity existed:
// the open must surface an explicit error instead of silently showing the
// previously active session.
func TestDesktopIconOpenRetainedIconWithoutIdentityFailsExplicitly(t *testing.T) {
	app := &App{
		tabs:                  map[string]*WorkspaceTab{},
		activeTabID:           "stale-active",
		widgetMode:            true,
		ctx:                   context.Background(),
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{},
			Kept: map[string]desktopIconKept{
				"task:task-a": {ItemID: "task:task-a", SourceID: "task-a", Title: "任务A", Order: 0},
			},
			CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
		widgetWindowOps: &widgetWindowOps{
			read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
			restoreMain: func(DesktopWindowState, bool) error { return nil },
			applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
		},
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:task-a")
	if item == nil {
		t.Fatalf("retained icon missing: %+v", snapshot.Items)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-open-kept", Action: "open",
	})
	if result.Status == "accepted" {
		t.Fatalf("open without identity unexpectedly accepted (active=%q)", app.activeTabID)
	}
	if !strings.Contains(result.Error, "identity") {
		t.Fatalf("open without identity error = %q, want explicit identity error", result.Error)
	}
	if app.activeTabID != "stale-active" {
		t.Fatalf("failed open changed active tab to %q", app.activeTabID)
	}
}

// TestDesktopIconResolveTaskRequiresSessionRef pins the unified identity
// contract at the resolver level: SourceID alone is never a valid open target,
// and a missing ref or session path fails explicitly instead of returning a
// tab or falling back to the active session.
func TestDesktopIconResolveTaskRequiresSessionRef(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	if tabID, err := app.resolveDesktopIconTaskTab(DesktopIconItem{ID: "task:x", Kind: "task", SourceID: "tab-1"}); err == nil || tabID != "" {
		t.Fatalf("live icon without ref resolved to %q, %v; want explicit identity failure", tabID, err)
	}
	if _, err := app.resolveDesktopIconTaskTab(DesktopIconItem{ID: "task:x", Kind: "task", SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "topic-a"}}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing session path = %v, want explicit identity error", err)
	}
	if _, err := app.resolveDesktopIconTaskTab(DesktopIconItem{ID: "task:x", Kind: "task", SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "topic-a", SessionPath: `D:\no\such\session.jsonl`}}); err == nil {
		t.Fatal("an unvalidatable session path must fail through OpenTopicSession")
	}
}

// TestDesktopIconOpenStaleSourceIDUsesSessionRef verifies that a retained icon
// whose SourceID names a live-but-wrong tab still opens the session recorded in
// its snapshot ref. The resolver must not consult SourceID/tabID at all.
func TestDesktopIconOpenStaleSourceIDUsesSessionRef(t *testing.T) {
	isolateDesktopUserDirs(t)
	tabWrong, spWrong := completionTestTab(t, 0) // idle tab, invisible as a task icon
	tabRight, sp := completionTestTab(t, 0)      // idle tab owning the kept session
	tabWrong.ID = "task-wrong"
	tabRight.ID = "task-right"
	app := &App{
		tabs:                  map[string]*WorkspaceTab{tabWrong.ID: tabWrong, tabRight.ID: tabRight},
		activeTabID:           tabRight.ID,
		widgetMode:            true,
		ctx:                   context.Background(),
		sessionDirsOverride:   []string{filepath.Dir(spWrong), filepath.Dir(sp)},
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{},
			Kept: map[string]desktopIconKept{
				"task:stale": {
					ItemID: "task:stale", SourceID: "task-wrong", Title: "任务A", Order: 0,
					Scope: "global", TopicID: "topic-a", SessionPath: sp,
				},
			},
			CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
		widgetWindowOps: &widgetWindowOps{
			read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
			restoreMain: func(DesktopWindowState, bool) error { return nil },
			applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
		},
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:stale")
	if item == nil {
		t.Fatalf("retained icon missing: %+v", snapshot.Items)
	}
	if item.SessionRef == nil || item.SessionRef.SessionPath != sp {
		t.Fatalf("retained item session ref = %#v, want path %q", item.SessionRef, sp)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-open-stale", Action: "open",
	})
	if result.Status != "accepted" {
		t.Fatalf("open stale-source icon status = %q error %q", result.Status, result.Error)
	}
	active := app.tabs[app.activeTabID]
	if active == nil || active.ID != tabRight.ID {
		t.Fatalf("open landed on %q, want %q (SourceID must not win)", app.activeTabID, tabRight.ID)
	}
	if sessionRuntimeKey(active.currentSessionPath()) != sessionRuntimeKey(sp) {
		t.Fatalf("opened session path %q, want %q", active.currentSessionPath(), sp)
	}
}

// TestDesktopIconOpenSameTopicDifferentSessionPathReusesCorrectSession verifies
// that two live sessions under one topic open their own session: the ref's
// sessionPath selects the tab, never the shared topic.
func TestDesktopIconOpenSameTopicDifferentSessionPathReusesCorrectSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	tabA, spA := completionTestTab(t, 1000)
	tabB, spB := completionTestTab(t, 1000)
	tabA.ID = "task-a"
	tabB.ID = "task-b"
	tabA.TopicID = "topic-x"
	tabB.TopicID = "topic-x"
	app := newSummaryTestApp(t, tabA, fakeCompletionSummaryGenerator{})
	app.tabs[tabB.ID] = tabB
	app.sessionDirsOverride = []string{filepath.Dir(spA), filepath.Dir(spB)}
	app.activeTabID = tabA.ID
	app.widgetMode = true
	app.ctx = context.Background()
	app.widgetWindowOps = &widgetWindowOps{
		read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
		restoreMain: func(DesktopWindowState, bool) error { return nil },
		applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
	}
	app.widgetTaskbarToggle = func(bool) error { return nil }
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}

	snapshot := app.GetDesktopIconSnapshot()
	itemB := findDesktopIconItem(snapshot.Items, "task:task-b")
	if itemB == nil || itemB.SessionRef == nil || itemB.SessionRef.TopicID != "topic-x" || itemB.SessionRef.SessionPath != spB {
		t.Fatalf("task B ref = %#v item=%+v, want topic-x path %q", itemB.SessionRef, itemB, spB)
	}
	if result := app.ApplyDesktopIconAction(DesktopIconActionInput{ItemID: itemB.ID, Revision: itemB.Revision, RequestID: "req-open-b", Action: "open"}); result.Status != "accepted" {
		t.Fatalf("open B status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != tabB.ID {
		t.Fatalf("open B activated %q, want %q", app.activeTabID, tabB.ID)
	}
	if sessionRuntimeKey(app.tabs[app.activeTabID].currentSessionPath()) != sessionRuntimeKey(spB) {
		t.Fatalf("open B session = %q, want %q", app.tabs[app.activeTabID].currentSessionPath(), spB)
	}
	// Re-enter widget mode, then open session A: the same topic must not
	// redirect the open onto B's tab.
	app.widgetMode = true
	app.activeTabID = tabB.ID
	snapshot = app.GetDesktopIconSnapshot()
	itemA := findDesktopIconItem(snapshot.Items, "task:task-a")
	if itemA == nil || itemA.SessionRef == nil {
		t.Fatalf("task A ref missing: %+v", snapshot.Items)
	}
	if result := app.ApplyDesktopIconAction(DesktopIconActionInput{ItemID: itemA.ID, Revision: itemA.Revision, RequestID: "req-open-a", Action: "open"}); result.Status != "accepted" {
		t.Fatalf("open A status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != tabA.ID {
		t.Fatalf("open A activated %q, want %q", app.activeTabID, tabA.ID)
	}
	if sessionRuntimeKey(app.tabs[app.activeTabID].currentSessionPath()) != sessionRuntimeKey(spA) {
		t.Fatalf("open A session = %q, want %q", app.tabs[app.activeTabID].currentSessionPath(), spA)
	}
}

// TestDesktopIconTaskRevisionIncludesSessionIdentity pins requirement that the
// session identity participates in the item revision: an identity change must
// invalidate the revision (so the frontend stale check fires), while identical
// identity keeps the revision stable.
func TestDesktopIconTaskRevisionIncludesSessionIdentity(t *testing.T) {
	base := DesktopIconItem{
		ID: "task:1", Kind: "task", Status: "idle",
		Position:   DesktopIconPosition{Row: "bottom", Zone: "running", Order: 0},
		SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "topic-a", SessionPath: "sp-a"},
	}
	clone := func(ref *DesktopIconTaskRef) DesktopIconItem {
		item := base
		if ref != nil {
			copy := *ref
			item.SessionRef = &copy
		} else {
			item.SessionRef = nil
		}
		return item
	}
	if desktopIconItemRevision(clone(&DesktopIconTaskRef{Scope: "global", TopicID: "topic-a", SessionPath: "sp-b"})) == desktopIconItemRevision(base) {
		t.Fatal("a session path change must change the item revision")
	}
	if desktopIconItemRevision(clone(nil)) == desktopIconItemRevision(base) {
		t.Fatal("losing the session identity must change the item revision")
	}
	if desktopIconItemRevision(clone(&DesktopIconTaskRef{Scope: "global", TopicID: "topic-a", SessionPath: "sp-a"})) != desktopIconItemRevision(base) {
		t.Fatal("identical session identity must keep a stable revision")
	}
}

// TestRememberDesktopIconTaskSameSessionRefreshesNotDuplicates verifies the
// same-session dedupe: reopening a closed task (new tab ID, same session path)
// refreshes the existing kept entry instead of adding a second icon.
func TestRememberDesktopIconTaskSameSessionRefreshesNotDuplicates(t *testing.T) {
	tab, sp := completionTestTab(t, 0)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.widgetMode = true
	app.rememberDesktopIconTask(tab.ID)
	if len(app.iconWidgetState.Kept) != 1 {
		t.Fatalf("first retain kept %d entries: %+v", len(app.iconWidgetState.Kept), app.iconWidgetState.Kept)
	}
	// The tab is recreated with a new ID but the same session path.
	recreated := &WorkspaceTab{ID: "task-1-reborn", SessionPath: sp, disabledMCP: map[string]ServerView{}}
	app.tabs[recreated.ID] = recreated
	app.rememberDesktopIconTask(recreated.ID)
	if len(app.iconWidgetState.Kept) != 1 {
		t.Fatalf("reopen accumulated %d kept entries: %+v", len(app.iconWidgetState.Kept), app.iconWidgetState.Kept)
	}
	kept, ok := app.iconWidgetState.Kept["task:task-1"]
	if !ok || kept.SourceID != "task-1-reborn" {
		t.Fatalf("kept entry not refreshed to live tab: %+v", app.iconWidgetState.Kept)
	}
}

func TestDesktopIconOpenWorkspaceActivatesSelectedWorkspace(t *testing.T) {
	isolateDesktopUserDirs(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
	for _, root := range []string{rootA, rootB} {
		if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("workspace"), 0o600); err != nil {
			t.Fatalf("seed workspace %s: %v", root, err)
		}
	}
	for _, root := range []string{rootA, rootB} {
		if err := addProject(root, ""); err != nil {
			t.Fatalf("register workspace %s: %v", root, err)
		}
	}
	tabA := &WorkspaceTab{ID: "tab-a", Scope: "project", WorkspaceRoot: rootA, TopicID: "topic-a"}
	tabB := &WorkspaceTab{ID: "tab-b", Scope: "project", WorkspaceRoot: rootB, TopicID: "topic-b"}
	app := &App{
		tabs:                  map[string]*WorkspaceTab{tabA.ID: tabA, tabB.ID: tabB},
		tabOrder:              []string{tabA.ID, tabB.ID},
		activeTabID:           tabA.ID,
		ctx:                   context.Background(),
		widgetMode:            true,
		widgetStyle:           "icons",
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{},
			CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
	}
	app.widgetWindowOps = &widgetWindowOps{
		read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 1080, Height: 720}, false },
		restoreMain: func(DesktopWindowState, bool) error { return nil },
		applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
	}
	app.widgetTaskbarToggle = func(bool) error { return nil }
	app.runtimeEvents.emit = func(context.Context, string, ...interface{}) {}

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "workspace:"+rootB)
	if item == nil {
		t.Fatalf("workspace B icon missing: %+v", snapshot.Items)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "open-workspace-b", Action: "open",
	})
	if result.Status != "accepted" {
		t.Fatalf("open workspace B status = %q error %q", result.Status, result.Error)
	}
	active := app.activeTab()
	if active == nil || normalizeProjectRoot(active.WorkspaceRoot) != normalizeProjectRoot(rootB) {
		got := ""
		if active != nil {
			got = active.WorkspaceRoot
		}
		t.Fatalf("active workspace = %q, want %q", got, rootB)
	}
}
