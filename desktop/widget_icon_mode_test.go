package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/unread"
)

func TestBuildDesktopIconSnapshotKeepsReadConversationAndTwoRows(t *testing.T) {
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 7, Conversations: []unread.Conversation{{
		Key: "room:design", Source: unread.SourceRoom, SessionID: "room-session", Title: "产品 Room",
	}}}}
	spaces := []WidgetWorkspaceOption{{Scope: "auto", Name: "自动"}, {Scope: "project", Name: "WorkGround2", Root: `D:\Work\WorkGround2`}}
	snapshot := buildDesktopIconSnapshot(nil, state, spaces, desktopIconPersistedState{}, 1200, nil)
	room := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if room == nil || room.Position.Row != "top" || room.UnreadCount != 0 {
		t.Fatalf("read Room projection = %#v", room)
	}
	workspace := findDesktopIconItem(snapshot.Items, `workspace:D:\Work\WorkGround2`)
	if workspace == nil || workspace.Position.Row != "bottom" || workspace.Position.Zone != "workspace" {
		t.Fatalf("workspace projection = %#v", workspace)
	}
	for _, id := range []string{"fixed:new", "fixed:delegate", "fixed:knowledge", "fixed:search"} {
		item := findDesktopIconItem(snapshot.Items, id)
		if item == nil || item.Position.Row != "bottom" || item.Position.Zone != "fixed" {
			t.Fatalf("fixed item %s = %#v", id, item)
		}
	}
}

func TestBuildDesktopIconSnapshotSeparatesRuntimeFromUnread(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{ID: "task-1", SessionID: "session-1", WorkspaceName: "WG2", TopicTitle: "实现图标模式", RunningWork: true, ForegroundActive: true, TurnStartedAt: time.Now().Add(-time.Second).UnixMilli()}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil)
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil || task.Runtime == nil || task.UnreadCount != 0 {
		t.Fatalf("running task = %#v", task)
	}
	if task.Status != "thinking" {
		t.Fatalf("status = %q, want thinking", task.Status)
	}
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.UnreadCount != 0 || delegate.ActivityCount != 0 {
		t.Fatalf("delegate = %#v", delegate)
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
	snapshot := buildDesktopIconSnapshot(sources, state, nil, desktopIconPersistedState{}, 1200, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 1 || delegate.UnreadCount != 0 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v", delegate)
	}
	if findDesktopIconItem(snapshot.Items, "task:delegated") != nil {
		t.Fatal("delegated task received an independent running icon")
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
	t.Setenv("WorkGround2_HOME", home)
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

func TestDesktopIconWindowStateRejectsOldShortGeometryWithError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
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

func findDesktopIconItem(items []DesktopIconItem, id string) *DesktopIconItem {
	for i := range items {
		if items[i].ID == id {
			return &items[i]
		}
	}
	return nil
}
