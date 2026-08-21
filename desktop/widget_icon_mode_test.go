package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/control"
	"workground2/internal/provider"
	"workground2/internal/unread"
)

func TestBuildDesktopIconSnapshotKeepsReadConversationAndTwoRows(t *testing.T) {
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 7, Conversations: []unread.Conversation{{
		Key: "room:design", Source: unread.SourceRoom, SessionID: "room-session", Title: "产品 Room",
	}}}}
	spaces := []WidgetWorkspaceOption{{Scope: "auto", Name: "自动"}, {Scope: "project", Name: "WorkGround2", Root: `D:\Work\WorkGround2`, Icon: "python"}}
	snapshot := buildDesktopIconSnapshot(nil, state, spaces, desktopIconPersistedState{}, 1200, nil, nil, nil, nil)
	room := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if room == nil || room.Position.Row != "top" || room.UnreadCount != 0 {
		t.Fatalf("read Room projection = %#v", room)
	}
	workspace := findDesktopIconItem(snapshot.Items, `workspace:D:\Work\WorkGround2`)
	if workspace == nil || workspace.Position.Row != "bottom" || workspace.Position.Zone != "workspace" || workspace.Icon != "python" {
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

func TestDesktopRoomNoticesUseExactMessageAuthorAndMentionPresentation(t *testing.T) {
	runtime := &desktopCollaboration{
		ownerSessionID: "room-session",
		state: CollaborationState{
			MemberID: "self", AgentID: "agent-self",
			Snapshot: collab.Snapshot{
				Members: []collab.Member{
					{ID: "self", Name: "Me", Agent: collab.AgentDescriptor{ID: "agent-self", Name: "My Agent"}},
					{ID: "alice", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-alice", Name: "Alice Agent"}},
				},
				Timeline: []collab.TimelineItem{
					{ID: "normal", Sequence: 1, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "normal", AuthorID: "alice", Text: "first exact message"}},
					{ID: "member", Sequence: 2, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "member", AuthorID: "alice", Text: "member exact message", MentionMemberIDs: []string{"self"}}},
					{ID: "agent", Sequence: 3, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "agent", AuthorID: "alice", Text: "agent exact message", MentionAgentIDs: []string{"agent-self"}}},
					{ID: "both", Sequence: 4, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "both", AuthorID: "alice", Text: "both exact message", MentionMemberIDs: []string{"self"}, MentionAgentIDs: []string{"agent-self"}}},
					{ID: "action", Sequence: 5, Type: collab.TimelineContribution, Contribution: &collab.Contribution{ID: "action", AuthorID: "alice", Body: "ordinary high action", ActionNeeded: true}},
				},
			},
		},
	}
	app := &App{collaborations: map[string]*desktopCollaboration{"room-session": runtime}}
	runtime.app = app
	presentations := app.desktopRoomNoticePresentations()

	at := time.Date(2026, 8, 21, 8, 0, 0, 0, time.UTC)
	conversation := unread.Conversation{
		Key: "room:exact", Source: unread.SourceRoom, SessionID: "room-session", Title: "Design Room",
		LatestSequence: 5, UnreadCount: 5, HighPriorityCount: 4,
		Items: []unread.Item{
			{ID: "normal", Sequence: 1, Kind: "chat", Priority: unread.PriorityNormal, AuthorID: "alice", OccurredAt: at},
			{ID: "member", Sequence: 2, Kind: "chat", Priority: unread.PriorityHigh, Attention: unread.AttentionMentionMember, AuthorID: "alice", OccurredAt: at.Add(time.Minute)},
			{ID: "agent", Sequence: 3, Kind: "chat", Priority: unread.PriorityHigh, Attention: unread.AttentionMentionAgent, AuthorID: "alice", OccurredAt: at.Add(2 * time.Minute)},
			{ID: "both", Sequence: 4, Kind: "chat", Priority: unread.PriorityHigh, Attention: unread.AttentionMentionBoth, AuthorID: "alice", OccurredAt: at.Add(3 * time.Minute)},
			{ID: "action", Sequence: 5, Kind: "contribution", Priority: unread.PriorityHigh, AuthorID: "alice", OccurredAt: at.Add(4 * time.Minute)},
		},
	}
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{Available: true, Summary: unread.Summary{Revision: 1, Conversations: []unread.Conversation{conversation}}}, nil, desktopIconPersistedState{}, 0, presentations, nil, nil, nil)
	room := findDesktopIconItem(snapshot.Items, "conversation:room:exact")
	if room == nil || len(room.Notifications) != 5 {
		t.Fatalf("Room notices = %#v", room)
	}
	byID := map[string]DesktopIconNotice{}
	for _, notice := range room.Notifications {
		byID[notice.ID] = notice
	}
	want := map[string]struct {
		body      string
		attention unread.ItemAttention
		title     string
		kind      string
	}{
		"normal": {body: "first exact message", title: "Alice · Design Room", kind: "message"},
		"member": {body: "member exact message", attention: unread.AttentionMentionMember, title: "Alice @ 了你", kind: "message"},
		"agent":  {body: "agent exact message", attention: unread.AttentionMentionAgent, title: "Alice @ 了你的 Agent", kind: "message"},
		"both":   {body: "both exact message", attention: unread.AttentionMentionBoth, title: "Alice @ 了你和你的 Agent", kind: "message"},
		"action": {body: "ordinary high action", title: "Alice · Design Room", kind: "needs_input"},
	}
	for id, expected := range want {
		notice := byID[id]
		if notice.Body != expected.body || notice.Attention != expected.attention || notice.Title != expected.title || notice.Kind != expected.kind {
			t.Fatalf("notice %s = %+v, want body=%q attention=%q title=%q kind=%q", id, notice, expected.body, expected.attention, expected.title, expected.kind)
		}
	}
	if byID["member"].Priority != 1 || byID["normal"].Priority != 3 {
		t.Fatalf("Room notice priorities = member %d normal %d", byID["member"].Priority, byID["normal"].Priority)
	}
}

func TestDesktopRoomMentionNoticeKeepsOpenAndReplyActions(t *testing.T) {
	sp := roomTestSession(t)
	openApp := newRoomOpenTestApp(t, sp)
	openNotice := &DesktopIconNotice{
		ID: "mention", Revision: "2", Kind: "message", Attention: unread.AttentionMentionAgent,
		TabID: "room-session", Conversation: "room:exact", ReadSequence: 2,
	}
	err := openApp.applyDesktopIconActionLocked(
		DesktopIconItem{
			ID: "conversation:room:exact", Kind: "room", SourceID: "room:exact",
			SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "room-topic", SessionPath: sp},
		},
		openNotice,
		DesktopIconActionInput{Action: "open", RequestID: "open-mention"},
	)
	if err != nil || openApp.activeTabID != "room-tab" {
		t.Fatalf("open mention Room: active=%q err=%v", openApp.activeTabID, err)
	}

	app := &App{}
	runtime := &desktopCollaboration{
		app: app, ownerSessionID: "room-session",
		state: CollaborationState{Status: "failed", Room: "room-a", MemberID: "self", SessionID: "room-session"},
	}
	app.collaborations = map[string]*desktopCollaboration{"room-session": runtime}
	notice := &DesktopIconNotice{
		ID: "mention", Revision: "2", Kind: "message", Attention: unread.AttentionMentionAgent,
		TabID: "room-session", Conversation: "room:exact", ReadSequence: 2,
	}
	err = app.applyDesktopIconActionLocked(
		DesktopIconItem{ID: "conversation:room:exact", Kind: "room", SourceID: "room:exact"},
		notice,
		DesktopIconActionInput{Action: "reply", RequestID: "reply-mention", Values: []string{"收到，我来处理"}},
	)
	if err != nil {
		t.Fatalf("reply to mention: %v", err)
	}
	if len(runtime.outbox) != 1 || runtime.outbox[0].Command.Chat == nil || runtime.outbox[0].Command.Chat.Text != "收到，我来处理" {
		t.Fatalf("queued mention reply = %+v", runtime.outbox)
	}
	if !runtime.state.Retryable || runtime.state.OutboxCount != 1 {
		t.Fatalf("retryable reply state = %+v", runtime.state)
	}
}

func TestDesktopIconPinnedRoomsKeepSevenAndAppendEighthUnread(t *testing.T) {
	pinned := make([]desktopIconRoomDescriptor, 0, desktopRoomPinLimit)
	for i := 0; i < desktopRoomPinLimit; i++ {
		pinned = append(pinned, desktopIconRoomDescriptor{
			TopicID: fmt.Sprintf("topic-%d", i), Title: fmt.Sprintf("Room %d", i), SessionID: fmt.Sprintf("session-%d", i),
			Icon: "discussion",
			Ref:  &DesktopIconTaskRef{Scope: "global", TopicID: fmt.Sprintf("topic-%d", i), SessionPath: fmt.Sprintf("room-%d.jsonl", i)},
		})
	}
	allRooms := map[string]desktopIconRoomDescriptor{"topic-8": {TopicID: "topic-8", SessionID: "session-8", Icon: "python"}}
	at := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 2, Conversations: []unread.Conversation{
		{
			Key: "room:pinned", Source: unread.SourceRoom, SessionID: "session-3", Title: "Room 3", LatestSequence: 4, UnreadCount: 1,
			Items: []unread.Item{{ID: "pinned-message", Sequence: 4, Kind: "chat", Priority: unread.PriorityNormal, OccurredAt: at}},
		},
		{
			Key: "room:eighth", Source: unread.SourceRoom, SessionID: "session-8", Title: "Room 8", LatestSequence: 8, UnreadCount: 1,
			Items: []unread.Item{{ID: "eighth-message", Sequence: 8, Kind: "chat", Priority: unread.PriorityNormal, OccurredAt: at.Add(time.Minute)}},
		},
	}}}
	snapshot := buildDesktopIconSnapshotWithPresentations(nil, state, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil, pinned, nil, allRooms)
	rooms := make([]DesktopIconItem, 0, desktopRoomPinLimit+1)
	for _, item := range snapshot.Items {
		if item.Kind == "room" {
			rooms = append(rooms, item)
		}
	}
	if len(rooms) != desktopRoomPinLimit+1 {
		t.Fatalf("Room icons = %d, want seven pinned plus unread: %+v", len(rooms), rooms)
	}
	for i := 0; i < desktopRoomPinLimit; i++ {
		if rooms[i].ID != fmt.Sprintf("room:topic-%d", i) || rooms[i].Position.Order != i {
			t.Fatalf("pinned Room %d = %+v", i, rooms[i])
		}
	}
	pinnedUnread := findDesktopIconItem(snapshot.Items, "room:topic-3")
	if pinnedUnread == nil || pinnedUnread.Icon != "discussion" || pinnedUnread.UnreadCount != 1 || len(pinnedUnread.Notifications) != 1 || pinnedUnread.Notifications[0].ID != "pinned-message" {
		t.Fatalf("merged pinned unread = %+v", pinnedUnread)
	}
	if findDesktopIconItem(snapshot.Items, "conversation:room:pinned") != nil {
		t.Fatal("pinned unread Room was projected twice")
	}
	eighth := findDesktopIconItem(snapshot.Items, "conversation:room:eighth")
	if eighth == nil || eighth.Icon != "python" || eighth.Position.Order != desktopRoomPinLimit || eighth.UnreadCount != 1 {
		t.Fatalf("eighth unread Room = %+v", eighth)
	}
}

func TestDesktopIconPinnedRoomDescriptorsSkipStaleAndKeepDurableRef(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(desktopConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := saveDesktopRoomPins(desktopRoomPinState{TopicIDs: []string{"stale-topic", "room-topic"}}); err != nil {
		t.Fatalf("save Room pins: %v", err)
	}
	sp := roomTestSession(t)
	meta, ok, err := agent.LoadBranchMeta(sp)
	if err != nil || !ok {
		t.Fatalf("load Room meta: ok=%v err=%v", ok, err)
	}
	meta.ID = "room-session"
	meta.Scope = "project"
	meta.WorkspaceRoot = t.TempDir()
	meta.TopicID = "room-topic"
	meta.CustomTitle = "Durable Room"
	meta.SessionKind = agent.SessionKindCollaboration
	if err := agent.SaveBranchMeta(sp, meta); err != nil {
		t.Fatalf("save Room meta: %v", err)
	}
	tree := []ProjectNode{{Kind: "project", Root: meta.WorkspaceRoot, Children: []ProjectNode{{
		Kind: "topic", Label: "Durable Room", Root: meta.WorkspaceRoot, TopicID: "room-topic", SessionID: "room-session", SessionPath: sp, SessionKind: string(agent.SessionKindCollaboration),
	}}}}
	descriptors := desktopIconPinnedRoomsFromDescriptors(desktopIconRoomDescriptors(tree, map[string]string{"room-topic": "discussion", "stale-topic": "python"}), []string{"stale-topic", "room-topic"})
	if len(descriptors) != 1 {
		t.Fatalf("pinned descriptors = %+v", descriptors)
	}
	descriptor := descriptors[0]
	if descriptor.TopicID != "room-topic" || descriptor.Title != "Durable Room" || descriptor.SessionID != "room-session" || descriptor.Icon != "discussion" || descriptor.Ref == nil || descriptor.Ref.SessionPath != sp || descriptor.Ref.TopicID != "room-topic" || descriptor.Ref.Scope != "project" || descriptor.Ref.WorkspaceRoot != meta.WorkspaceRoot {
		t.Fatalf("durable Room descriptor = %+v", descriptor)
	}
	persisted, err := loadDesktopRoomPins()
	if err != nil || !reflect.DeepEqual(persisted.TopicIDs, []string{"stale-topic", "room-topic"}) {
		t.Fatalf("stale pin was not preserved: %+v err=%v", persisted, err)
	}

	app := newRoomOpenTestApp(t, sp)
	item := DesktopIconItem{
		ID: "room:" + descriptor.TopicID, Kind: "room", SourceID: descriptor.TopicID, Title: descriptor.Title,
		SessionID: descriptor.SessionID, SessionRef: descriptor.Ref,
	}
	if err := app.applyDesktopIconActionLocked(item, nil, DesktopIconActionInput{Action: "open", RequestID: "open-pinned"}); err != nil || app.activeTabID != "room-tab" {
		t.Fatalf("open pinned Room: active=%q err=%v", app.activeTabID, err)
	}

	withoutMeta := desktopIconRoomDescriptors([]ProjectNode{{
		Kind: "global_topic", Label: "Recoverable Room", TopicID: "recoverable", SessionID: "recoverable-session",
		SessionPath: filepath.Join(t.TempDir(), "temporarily-missing.jsonl"), SessionKind: string(agent.SessionKindCollaboration),
	}}, map[string]string{"recoverable": "python"})["recoverable"]
	if withoutMeta.Icon != "python" || withoutMeta.SessionID != "recoverable-session" {
		t.Fatalf("Room descriptor without readable meta = %+v, want tree identity and retained icon preference", withoutMeta)
	}
}

func TestDesktopIconSnapshotReportsRoomPinReadFailureWithoutDroppingIcons(t *testing.T) {
	isolateDesktopUserDirs(t)
	if err := os.MkdirAll(desktopConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopRoomPinsPath(), []byte(`{"topicIds":["duplicate","duplicate"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{iconWidgetStateLoaded: true, iconWidgetState: newDesktopIconState()}
	snapshot := app.GetDesktopIconSnapshot()
	if !strings.Contains(snapshot.Error, "load desktop Room pins") || !strings.Contains(snapshot.Error, "duplicate") {
		t.Fatalf("snapshot pin error = %q", snapshot.Error)
	}
	if findDesktopIconItem(snapshot.Items, "fixed:new") == nil || findDesktopIconItem(snapshot.Items, "fixed:rooms") == nil {
		t.Fatalf("pin read failure dropped unrelated icons: %+v", snapshot.Items)
	}
	raw, err := os.ReadFile(desktopRoomPinsPath())
	if err != nil || string(raw) != `{"topicIds":["duplicate","duplicate"]}` {
		t.Fatalf("corrupt pin file was changed: %q err=%v", raw, err)
	}
}

func TestDesktopIconWorkspacesRespectConfiguredSlotsAndPriority(t *testing.T) {
	tree := []ProjectNode{
		{Kind: "project", Root: `D:\Work\old`, Label: "Old", Children: []ProjectNode{{LastActivityAt: 100}}},
		{Kind: "project", Root: `D:\Work\pinned-a`, Label: "Pinned A", Pinned: true, Children: []ProjectNode{{LastActivityAt: 10}}},
		{Kind: "global_folder", Label: "Global", Children: []ProjectNode{{LastActivityAt: 999}}},
		{Kind: "project", Root: `D:\Work\newest`, Label: "Newest", ProjectIcon: "typescript", Children: []ProjectNode{{LastActivityAt: 500}, {Children: []ProjectNode{{LastActivityAt: 700}}}}},
		{Kind: "project", Root: `D:\Work\pinned-b`, Label: "Pinned B", Pinned: true, Children: []ProjectNode{{LastActivityAt: 20}}},
		{Kind: "project", Root: `D:\Work\middle`, Label: "Middle", Children: []ProjectNode{{LastActivityAt: 300}}},
	}

	spaces := desktopIconWorkspaces(tree, `D:\Work\old`, 4)
	got := make([]string, 0, len(spaces))
	for _, space := range spaces {
		got = append(got, space.Name)
	}
	want := []string{"Pinned A", "Pinned B", "Old", "Newest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("desktop workspace order = %v, want %v", got, want)
	}
	if spaces[3].LastActivityAt != 700 {
		t.Fatalf("nested project activity = %d, want 700", spaces[3].LastActivityAt)
	}
	if spaces[3].Icon != "typescript" {
		t.Fatalf("workspace icon = %q, want typescript", spaces[3].Icon)
	}
	if got := desktopIconWorkspaces(tree, `D:\Work\old`, 0); len(got) != 0 {
		t.Fatalf("zero desktop workspace slots = %v, want none", got)
	}
	if got := desktopIconWorkspaces(tree, `D:\Work\old`, 2); len(got) != 2 || got[0].Name != "Pinned A" || got[1].Name != "Pinned B" {
		t.Fatalf("two desktop workspace slots = %v, want pinned projects", got)
	}
}

func TestDesktopIconWorkspacesKeepsPinnedTransientRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".WorkGround2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if !widgetIsTransientRoot(root, "shell") {
		t.Fatalf("fixture root %s is not transient", root)
	}
	project := ProjectNode{Kind: "project", Root: root, Label: "WG2ADS"}
	if spaces := desktopIconWorkspaces([]ProjectNode{project}, "", desktopWorkspacePinLimit); len(spaces) != 0 {
		t.Fatalf("unpinned transient projection = %+v, want none", spaces)
	}
	project.Pinned = true
	spaces := desktopIconWorkspaces([]ProjectNode{project}, "", desktopWorkspacePinLimit)
	if len(spaces) != 1 {
		t.Fatalf("desktopIconWorkspaces returned %d spaces, want only the pinned transient: %+v", len(spaces), spaces)
	}
	space := spaces[0]
	if space.Root != root || space.Name != "WG2ADS" || !space.Pinned || space.Scope != widgetWorkspaceProject {
		t.Fatalf("pinned transient projection = %+v, want root %s name WG2ADS pinned project", space, root)
	}
}

func TestDesktopWorkspaceSlotsPersistZeroAndRejectInvalidValues(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{}
	if got := app.GetDesktopWorkspaceSlots(); got != desktopWorkspacePinLimit {
		t.Fatalf("legacy/default desktop workspace slots = %d, want %d", got, desktopWorkspacePinLimit)
	}
	if err := app.SetDesktopWorkspaceSlots(0); err != nil {
		t.Fatalf("set zero desktop workspace slots: %v", err)
	}
	if err := app.SetDesktopWorkspaceSlots(0); err != nil {
		t.Fatalf("repeat zero desktop workspace slots: %v", err)
	}
	reloaded := &App{}
	if got := reloaded.GetDesktopWorkspaceSlots(); got != 0 {
		t.Fatalf("reloaded desktop workspace slots = %d, want 0", got)
	}
	for _, invalid := range []int{-1, desktopWorkspacePinLimit + 1} {
		if err := reloaded.SetDesktopWorkspaceSlots(invalid); err == nil {
			t.Fatalf("invalid desktop workspace slots %d succeeded", invalid)
		}
		if got := reloaded.GetDesktopWorkspaceSlots(); got != 0 {
			t.Fatalf("invalid value %d changed desktop workspace slots to %d", invalid, got)
		}
	}
	if err := os.WriteFile(desktopIconStatePath(), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write legacy desktop icon state: %v", err)
	}
	legacy := &App{}
	if got := legacy.GetDesktopWorkspaceSlots(); got != desktopWorkspacePinLimit {
		t.Fatalf("legacy state without workspaceSlots = %d, want %d", got, desktopWorkspacePinLimit)
	}
	if err := os.WriteFile(desktopIconStatePath(), []byte(`{"workspaceSlots":9}`), 0o600); err != nil {
		t.Fatalf("write invalid desktop icon state: %v", err)
	}
	repaired := &App{}
	if got := repaired.GetDesktopWorkspaceSlots(); got != desktopWorkspacePinLimit || repaired.iconWidgetStateErr == nil {
		t.Fatalf("invalid stored slots recovered as %d with error %v", got, repaired.iconWidgetStateErr)
	}
	if err := repaired.SetDesktopWorkspaceSlots(desktopWorkspacePinLimit); err != nil {
		t.Fatalf("repair invalid stored slots: %v", err)
	}
	clean := &App{}
	if got := clean.GetDesktopWorkspaceSlots(); got != desktopWorkspacePinLimit || clean.iconWidgetStateErr != nil {
		t.Fatalf("repaired stored slots loaded as %d with error %v", got, clean.iconWidgetStateErr)
	}
}

func TestBuildDesktopIconSnapshotShowsExactlyFourWorkspaceSlots(t *testing.T) {
	spaces := []WidgetWorkspaceOption{{Scope: "auto", Name: "Auto"}}
	for i := 0; i < 6; i++ {
		spaces = append(spaces, WidgetWorkspaceOption{Scope: "project", Name: fmt.Sprintf("P%d", i), Root: fmt.Sprintf("root-%d", i)})
	}
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, spaces, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
	count := 0
	for _, item := range snapshot.Items {
		if item.Kind != "workspace" {
			continue
		}
		if item.Position.Order != count {
			t.Fatalf("workspace order = %d, want %d", item.Position.Order, count)
		}
		count++
	}
	if count != desktopWorkspacePinLimit {
		t.Fatalf("workspace icon count = %d, want %d", count, desktopWorkspacePinLimit)
	}
}

func TestDesktopIconWorkspaceFixedItemContract(t *testing.T) {
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
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
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, state, nil, desktopIconPersistedState{}, 1200, nil, nil, nil, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, nil, nil)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 1 || delegate.UnreadCount != 0 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v", delegate)
	}
	if findDesktopIconItem(snapshot.Items, "task:delegated") != nil {
		t.Fatal("delegated task received an independent running icon")
	}
}

func TestBuildDesktopIconSnapshotProjectsRunningCLITaskOntoDelegate(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{ID: "cli-task", SessionSource: "cli", RunningWork: true}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, nil, nil)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 1 || delegate.Status != "running" {
		t.Fatalf("delegate = %#v, want one running CLI task", delegate)
	}
	if findDesktopIconItem(snapshot.Items, "task:cli-task") != nil {
		t.Fatal("CLI task received an independent task icon")
	}
}

func TestBuildDesktopIconSnapshotLeavesDelegateIdleForStoppedCLITask(t *testing.T) {
	sources := []widgetSource{{meta: TabMeta{ID: "cli-task", SessionSource: "cli"}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, nil, nil)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 0 || delegate.Status != "idle" {
		t.Fatalf("delegate = %#v, want stopped CLI task to stay idle", delegate)
	}
}

func TestBuildDesktopIconSnapshotCountsRealRunningSubagents(t *testing.T) {
	sources := []widgetSource{{
		meta:       TabMeta{ID: "task-1", SessionID: "session-1", WorkspaceName: "WG2", TopicTitle: "父任务", RunningWork: true, ForegroundActive: true, TurnStartedAt: time.Now().Add(-time.Second).UnixMilli()},
		sessionDir: "dir-a",
		branchID:   "branch-a",
	}}
	counts := map[widgetSubagentKey]int{newWidgetSubagentKey("dir-a", "branch-a"): 2}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, counts, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, counts, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, counts, nil)
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
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 1200, nil, nil, counts, nil)
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

func TestWidgetDelegationsAggregatesDeduplicatesAndSorts(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "parent.jsonl")
	backgroundPath := filepath.Join(dir, "background.jsonl")
	cliPath := filepath.Join(dir, "cli.jsonl")
	writeRunningDelegationMeta(t, dir, "sa_20260102_030405_000000000_aabbccddeeff", "parent", "较早委托", time.Unix(10, 0))
	writeRunningDelegationMeta(t, dir, "sa_20260102_030405_000000000_112233445566", "parent", "较新委托", time.Unix(20, 0))
	parent := widgetSource{meta: TabMeta{ID: "parent-tab", SessionID: "parent-session", Scope: "global", TopicID: "parent-topic", TopicTitle: "父 Session", SessionPath: parentPath, RunningWork: true}, sessionDir: dir, branchID: "parent"}
	background := widgetSource{meta: TabMeta{ID: "background-tab", SessionID: "background-session", Scope: "global", TopicID: "background-topic", TopicTitle: "后台 Session", SessionPath: backgroundPath, RunningWork: true, BackgroundOnly: true, TurnStartedAt: time.Unix(30, 0).UnixMilli()}, sessionDir: dir, branchID: "background", requestText: "后台兼容委托"}
	cli := widgetSource{meta: TabMeta{ID: "cli-tab", SessionID: "cli-session", SessionSource: "cli", Scope: "global", TopicID: "cli-topic", TopicTitle: "CLI Session", SessionPath: cliPath, RunningWork: true, TurnStartedAt: time.Unix(40, 0).UnixMilli()}, sessionDir: dir, branchID: "cli", requestText: "外部 CLI 委托"}
	app := &App{}
	items, counts, err := app.widgetDelegations([]widgetSource{parent, parent, background, cli})
	if err != nil {
		t.Fatalf("widgetDelegations: %v", err)
	}
	if len(items) != 4 || counts[newWidgetSubagentKey(dir, "parent")] != 2 {
		t.Fatalf("items=%+v counts=%v", items, counts)
	}
	if items[0].Kind != "cli" || items[1].Kind != "background" || items[2].Content != "较新委托" || items[3].Content != "较早委托" {
		t.Fatalf("delegation sort = %+v", items)
	}
	if items[0].SessionRef == nil || items[0].SessionRef.SessionPath != cliPath || items[2].SessionRef == nil || items[2].SessionRef.SessionPath != parentPath || items[1].SessionRef == nil || items[1].SessionRef.SessionPath != backgroundPath {
		t.Fatalf("delegation targets = %+v", items)
	}
	snapshot := buildDesktopIconSnapshot([]widgetSource{parent, parent, background, cli}, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, counts, items)
	delegate := findDesktopIconItem(snapshot.Items, "fixed:delegate")
	if delegate == nil || delegate.ActivityCount != 4 || len(snapshot.Delegations) != 4 {
		t.Fatalf("delegate=%+v projection=%+v", delegate, snapshot.Delegations)
	}
	if findDesktopIconItem(snapshot.Items, "task:background-tab") != nil {
		t.Fatal("BackgroundOnly delegation leaked into ordinary task icons")
	}
	if findDesktopIconItem(snapshot.Items, "task:cli-tab") != nil {
		t.Fatal("CLI delegation leaked into ordinary task icons")
	}
}

func TestWidgetDelegationsEmptyErrorAndCompletionRecovery(t *testing.T) {
	app := &App{}
	items, counts, err := app.widgetDelegations(nil)
	if err != nil || len(items) != 0 || len(counts) != 0 {
		t.Fatalf("empty delegations = %+v %v %v", items, counts, err)
	}
	dir := t.TempDir()
	ref := "sa_20260102_030405_000000000_aabbccddeeff"
	writeRunningDelegationMeta(t, dir, ref, "parent", "运行任务", time.Now())
	bad := filepath.Join(dir, "subagents", "sa_20260102_030405_000000000_112233445566.meta.json")
	if err := os.WriteFile(bad, []byte("{bad json"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := widgetSource{meta: TabMeta{ID: "parent", Scope: "global", TopicTitle: "父 Session", SessionPath: filepath.Join(dir, "parent.jsonl")}, sessionDir: dir, branchID: "parent"}
	items, _, err = app.widgetDelegations([]widgetSource{source})
	if err == nil || len(items) != 1 || !strings.Contains(err.Error(), "decode subagent metadata") {
		t.Fatalf("partial scan = %+v err=%v", items, err)
	}
	metaPath := filepath.Join(dir, "subagents", ref+".meta.json")
	data, readErr := os.ReadFile(metaPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var meta agent.SubagentMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatal(err)
	}
	meta.Status = agent.SubagentCompleted
	data, _ = json.Marshal(meta)
	if err := os.WriteFile(metaPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	items, _, err = app.widgetDelegations([]widgetSource{source})
	if err != nil || len(items) != 0 {
		t.Fatalf("completed delegation remained: %+v err=%v", items, err)
	}
}

func TestBuildDesktopIconSnapshotFiltersSubagentSession(t *testing.T) {
	dir := t.TempDir()
	source := widgetSource{meta: TabMeta{ID: "subagent-tab", SessionPath: filepath.Join(dir, "subagents", "sa_x.jsonl"), RunningWork: true}, sessionDir: dir}
	snapshot := buildDesktopIconSnapshot([]widgetSource{source}, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
	if findDesktopIconItem(snapshot.Items, "task:subagent-tab") != nil {
		t.Fatal("subagent session received an ordinary task icon")
	}
}

func TestBuildDesktopIconSnapshotFiltersRetainedDelegations(t *testing.T) {
	dir := t.TempDir()
	backgroundPath := filepath.Join(dir, "background.jsonl")
	subagentPath := filepath.Join(dir, "subagents", "sa_old.jsonl")
	normalPath := filepath.Join(dir, "normal.jsonl")
	state := desktopIconPersistedState{Kept: map[string]desktopIconKept{
		"task:subagent-old":   {ItemID: "task:subagent-old", SourceID: "subagent-old", Title: "旧子 agent", SessionPath: subagentPath},
		"task:background-old": {ItemID: "task:background-old", SourceID: "background-tab", SessionID: "background-session", Title: "旧后台委托", SessionPath: backgroundPath},
		"task:normal":         {ItemID: "task:normal", SourceID: "normal", Title: "普通 Session", SessionPath: normalPath},
	}, CompletionSummaries: map[string]desktopIconCompletionSummary{}}
	source := widgetSource{meta: TabMeta{ID: "background-tab", SessionID: "background-session", SessionPath: backgroundPath, BackgroundOnly: true}}
	snapshot := buildDesktopIconSnapshot([]widgetSource{source}, UnreadState{}, nil, state, 0, nil, nil, nil, nil)
	if findDesktopIconItem(snapshot.Items, "task:subagent-old") != nil || findDesktopIconItem(snapshot.Items, "task:background-old") != nil {
		t.Fatalf("retained delegations leaked into ordinary icons: %+v", snapshot.Items)
	}
	if findDesktopIconItem(snapshot.Items, "task:normal") == nil {
		t.Fatal("ordinary retained session was filtered with delegations")
	}
}

func writeRunningDelegationMeta(t *testing.T, sessionDir, ref, parent, description string, updated time.Time) {
	t.Helper()
	dir := filepath.Join(sessionDir, "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	meta := agent.SubagentMeta{Ref: ref, Status: agent.SubagentRunning, Kind: "task", Name: "task", Description: description, ParentSession: parent, UpdatedAt: updated}
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ref+".meta.json"), data, 0o600); err != nil {
		t.Fatal(err)
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
	// The controller prepends transient blocks (e.g. response-language) to every
	// submitted turn; confirmation must still match the raw continuation text.
	prefixed := agent.WithResponseLanguage("continue", "zh")
	if step, err := desktopIconTurnNextStep([]string{"before", prefixed}, 1, "continue", "accepted", false, "task conversation"); err != nil || step != desktopIconReplyConfirmStep {
		t.Fatalf("prefixed continuation = step %q, err %v", step, err)
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

func TestPinNewDesktopIconTaskOrdersPrependsNewestAndPreservesExistingOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_STATE_HOME", home)
	app := &App{iconWidgetStateLoaded: true, iconWidgetState: desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{
			"task:old-a":  {Row: "bottom", Zone: "running", Order: 0},
			"task:old-b":  {Row: "bottom", Zone: "running", Order: 8},
			"task:capped": {Row: "bottom", Zone: "running", Order: 12},
		},
		Kept: map[string]desktopIconKept{
			"task:kept": {ItemID: "task:kept", Order: 4},
		},
	}}
	snapshot := DesktopIconSnapshot{Items: []DesktopIconItem{
		{ID: "task:old-a", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 0}},
		{ID: "task:new-a", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 5}},
		{ID: "task:new-b", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 6}},
		{ID: "task:old-b", Kind: "task", Position: DesktopIconPosition{Row: "bottom", Zone: "running", Order: 8}},
		{ID: "fixed:new", Kind: "fixed", Position: DesktopIconPosition{Row: "bottom", Zone: "fixed", Order: 0}},
	}}
	if !app.pinNewDesktopIconTaskOrdersLocked(snapshot) {
		t.Fatal("new task icons were not pinned")
	}
	for id, want := range map[string]int{
		"task:new-b":  0,
		"task:new-a":  1,
		"task:old-a":  2,
		"task:old-b":  4,
		"task:capped": 5,
	} {
		got, ok := app.iconWidgetState.Positions[id]
		if !ok || got.Row != "bottom" || got.Zone != "running" || got.Order != want {
			t.Fatalf("position %s = %+v ok=%v, want running/order %d", id, got, ok, want)
		}
	}
	if kept := app.iconWidgetState.Kept["task:kept"]; kept.Order != 3 {
		t.Fatalf("retained icon order = %d, want 3", kept.Order)
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

func TestRetainedTaskAlwaysProjectsCompletionNotice(t *testing.T) {
	key := desktopIconCompletionKey("task-1", "completed", 1000)
	persisted := desktopIconPersistedState{
		Positions: map[string]DesktopIconPosition{},
		Kept: map[string]desktopIconKept{
			"task:task-1": {
				ItemID: "task:task-1", SourceID: "task-1", Title: "标题", Summary: "机械摘要",
				CompletionKey: key, CompletedAt: 1000,
			},
			"task:legacy": {ItemID: "task:legacy", SourceID: "legacy", Title: "旧任务", Summary: "旧摘要"},
		},
		CompletionSummaries: map[string]desktopIconCompletionSummary{
			key: {Status: completionSummaryReady, Text: "百字摘要"},
		},
	}
	snapshot := buildDesktopIconSnapshot(nil, UnreadState{}, nil, persisted, 0, nil, nil, nil, nil)
	for id, wantBody := range map[string]string{"task:task-1": "百字摘要", "task:legacy": "旧摘要"} {
		item := findDesktopIconItem(snapshot.Items, id)
		if item == nil || !item.Retained || len(item.Notifications) != 1 {
			t.Fatalf("retained task %s did not project a completion notice: %+v", id, item)
		}
		if item.UnreadCount != 0 {
			t.Fatalf("retained completion popup %s created an unread badge: %+v", id, item)
		}
		notice := item.Notifications[0]
		if notice.Kind != "completed" || notice.Title != "任务完成" || notice.Body != wantBody || notice.TabID != item.SourceID {
			t.Fatalf("retained notice %s = %+v", id, notice)
		}
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

func TestDesktopIconCompletionReceiptRecoversWithoutLiveTab(t *testing.T) {
	t.Setenv("WorkGround2_STATE_HOME", t.TempDir())
	_, sp := completionTestTab(t, 1000)
	// The task's tab is not loaded (empty tabs map), but the pending dismiss
	// receipt carries the session path. Recovery must clear the persisted
	// attention flag directly instead of failing until the tab reloads.
	app := &App{
		tabs:                      map[string]*WorkspaceTab{},
		activeTabID:               "",
		iconWidgetStateLoaded:     true,
		iconWidgetState:           desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}, Applied: []desktopIconReceipt{{RequestID: "req-dismiss", Intent: "intent", Status: "pending", Action: "dismiss", ItemID: "task:task-1", TabID: "task-1", SessionPath: sp, AppliedAt: time.Now().UnixMilli()}}},
		completionSummaryInFlight: map[string]*completionSummaryCall{},
	}
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("recover pending dismiss without live tab: %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("dismiss receipt did not settle: %+v", app.iconWidgetState.Applied[0])
	}
	meta, _, err := agent.LoadBranchMeta(sp)
	if err != nil || meta.NeedsAttention {
		t.Fatalf("dismiss without live tab did not clear attention: %v meta %+v", err, meta)
	}
}

func TestDesktopIconLegacyReceiptSettlesWithoutTrace(t *testing.T) {
	t.Setenv("WorkGround2_STATE_HOME", t.TempDir())
	// A pre-sessionPath receipt whose tab can never reload (tab IDs are random
	// and not restored) has nothing left to clear. Recovery settles it instead
	// of surfacing the same error on every snapshot.
	app := &App{
		tabs:                      map[string]*WorkspaceTab{},
		activeTabID:               "",
		iconWidgetStateLoaded:     true,
		iconWidgetState:           desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}, Applied: []desktopIconReceipt{{RequestID: "legacy-dismiss", Intent: "intent", Status: "pending", Action: "dismiss", ItemID: "task:gone-tab", TabID: "gone-tab", AppliedAt: time.Now().UnixMilli()}}},
		completionSummaryInFlight: map[string]*completionSummaryCall{},
	}
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("legacy receipt should settle without error: %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("legacy receipt did not settle: %+v", app.iconWidgetState.Applied[0])
	}
}

// fakeTaskContinueCtrl is a minimal control.SessionAPI test double for task
// continuation recovery: it only answers History and Running, which is all
// advanceDesktopIconTaskContinue reads on the confirm path.
type fakeTaskContinueCtrl struct {
	control.SessionAPI
	history []provider.Message
	running bool
}

func (f fakeTaskContinueCtrl) History() []provider.Message { return f.history }
func (f fakeTaskContinueCtrl) Running() bool               { return f.running }

func TestDesktopIconTaskContinueRecoveryDefersUntilControllerReady(t *testing.T) {
	app := newSummaryTestApp(t, nil, fakeCompletionSummaryGenerator{})
	app.iconWidgetState.Applied = []desktopIconReceipt{{
		RequestID: "req-continue", Intent: "intent", Status: "pending", Action: "continue",
		ItemID: "task:task-1", TabID: "task-1", Text: "继续",
		AppliedAt: time.Now().UnixMilli(),
	}}

	// First recovery: the completed task's controller has not been established
	// yet (startup, empty tabs). This is a deferral, not a failure.
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("first recovery surfaced a boot-time deferral: %v", err)
	}
	if got := app.iconWidgetState.Applied[0].Status; got != "pending" {
		t.Fatalf("not-ready continuation was marked %q, want pending", got)
	}

	// The controller becomes ready and its history already reflects the
	// submitted continuation, so recovery should confirm (not resubmit) and
	// settle the receipt.
	app.tabs["task-1"] = &WorkspaceTab{ID: "task-1", Ctrl: fakeTaskContinueCtrl{
		history: []provider.Message{{Role: provider.RoleUser, Content: "继续"}},
	}}
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("second recovery after controller ready: %v", err)
	}
	if got := app.iconWidgetState.Applied[0].Status; got != "applied" {
		t.Fatalf("continuation did not settle after controller ready: %q", got)
	}

	// An applied receipt must never resend: a third recovery is a no-op.
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("third recovery: %v", err)
	}
	if got := app.iconWidgetState.Applied[0].Status; got != "applied" {
		t.Fatalf("applied receipt was mutated: %q", got)
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

func TestDesktopIconDelegationRequiresIdentity(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	if _, err := app.resolveDesktopIconDelegation(DesktopIconDelegation{ID: "missing"}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("missing delegation identity = %v", err)
	}
}

func TestDesktopIconPendingBackgroundDelegationRetryUsesReceipt(t *testing.T) {
	isolateDesktopUserDirs(t)
	tab, sp := completionTestTab(t, 0)
	tab.ID = "background-target"
	tab.TopicID = "shared-topic"
	parent, parentPath := completionTestTab(t, 0)
	parent.ID = "parent-owner"
	parent.TopicID = "shared-topic"
	app := &App{
		tabs: map[string]*WorkspaceTab{tab.ID: tab, parent.ID: parent}, activeTabID: parent.ID, widgetMode: true, ctx: context.Background(),
		sessionDirsOverride:   []string{filepath.Dir(sp), filepath.Dir(parentPath)},
		iconWidgetStateLoaded: true,
		iconWidgetState:       desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}},
		widgetWindowOps: &widgetWindowOps{
			read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
			restoreMain: func(DesktopWindowState, bool) error { return nil }, applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
		},
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	app.runtimeEvents.emit = func(_ context.Context, _ string, _ ...interface{}) {}
	input := DesktopIconActionInput{ItemID: "fixed:delegate", Revision: "old-list", RequestID: "retry-background-open", Action: "open_delegation", Values: []string{"background:ended"}}
	app.iconWidgetState.Applied = append(app.iconWidgetState.Applied, desktopIconReceipt{
		RequestID: input.RequestID, Intent: desktopIconIntent(input), Status: "pending", Action: input.Action, ItemID: input.ItemID,
		Text: input.Values[0], TargetKind: "background", TargetScope: "global", TargetTopicID: "shared-topic", SessionPath: sp, AppliedAt: time.Now().UnixMilli(),
	})
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("automatic pending delegation recovery: %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("recovered delegation receipt = %+v", app.iconWidgetState.Applied[0])
	}
	if app.activeTabID != tab.ID {
		t.Fatalf("active tab = %q, want exact background target %q", app.activeTabID, tab.ID)
	}
	openedPath := app.tabs[app.activeTabID].currentSessionPath()
	if sessionRuntimeKey(openedPath) != sessionRuntimeKey(sp) || sessionRuntimeKey(openedPath) == sessionRuntimeKey(parentPath) {
		t.Fatalf("opened path = %q, want delegation %q instead of same-topic parent %q", openedPath, sp, parentPath)
	}
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("background delegation was retained as ordinary icon: %+v", app.iconWidgetState.Kept)
	}
	input.Revision = "new-list-after-completion"
	result := app.ApplyDesktopIconAction(input)
	if result.Status != "already_applied" {
		t.Fatalf("applied request replay = %+v", result)
	}
	app.widgetMode = true
	source := widgetSource{meta: TabMeta{ID: tab.ID, SessionID: "background-session", SessionPath: sp, RunningWork: true, BackgroundOnly: true}}
	snapshot := buildDesktopIconSnapshot([]widgetSource{source}, UnreadState{}, nil, app.iconWidgetState, 0, nil, nil, nil, nil)
	if findDesktopIconItem(snapshot.Items, "task:"+tab.ID) != nil {
		t.Fatal("opened background delegation reappeared as ordinary session icon")
	}
}

func TestRecoverPendingDelegationFailureStaysPending(t *testing.T) {
	dir := t.TempDir()
	app := &App{
		tabs: map[string]*WorkspaceTab{}, sessionDirsOverride: []string{dir}, iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{}, Applied: []desktopIconReceipt{{
			RequestID: "recover-missing-delegation", Status: "pending", Action: "open_delegation", TargetKind: "background", TargetScope: "global", SessionPath: filepath.Join(dir, "missing.jsonl"),
		}}},
	}
	err := app.recoverDesktopIconActionsLocked()
	if err == nil || !strings.Contains(err.Error(), "recover delegation open") {
		t.Fatalf("recovery error = %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "pending" {
		t.Fatalf("failed recovery lost retry intent: %+v", app.iconWidgetState.Applied[0])
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

// roomTestSession creates a real collaboration session file whose meta carries
// the topic identity and collaboration kind a Room session ref is derived from
// (the same fields collab session creation stamps).
func roomTestSession(t *testing.T) string {
	t.Helper()
	_, sp := completionTestTab(t, 0)
	meta, ok, err := agent.LoadBranchMeta(sp)
	if err != nil || !ok {
		t.Fatalf("load room branch meta: ok=%v err=%v", ok, err)
	}
	meta.TopicID = "room-topic"
	meta.SessionKind = agent.SessionKindCollaboration
	if err := agent.SaveBranchMeta(sp, meta); err != nil {
		t.Fatalf("stamp room topic: %v", err)
	}
	return sp
}

// newRoomOpenTestApp assembles an icon-widget app whose unread store holds a
// read Room conversation (no notice) bound to a collaboration runtime and a
// live Room tab, mirroring the real snapshot pipeline.
func newRoomOpenTestApp(t *testing.T, sp string) *App {
	t.Helper()
	isolateDesktopUserDirs(t)
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveRoom(unread.RoomInput{
		ConversationKey: "design", SessionID: "room-session", Title: "产品 Room", LocalMemberID: "self",
	}); err != nil {
		t.Fatal(err)
	}
	roomTab := &WorkspaceTab{
		ID: "room-tab", SessionID: "room-session", SessionPath: sp,
		sessionKind: agent.SessionKindCollaboration, disabledMCP: map[string]ServerView{},
	}
	app := &App{
		tabs:                  map[string]*WorkspaceTab{roomTab.ID: roomTab},
		activeTabID:           roomTab.ID,
		widgetMode:            true,
		unreadStore:           store,
		ctx:                   context.Background(),
		sessionDirsOverride:   []string{filepath.Dir(sp)},
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
		widgetWindowOps: &widgetWindowOps{
			read:        func() (WidgetWindowState, bool) { return WidgetWindowState{Width: 590, Height: 176}, false },
			restoreMain: func(DesktopWindowState, bool) error { return nil },
			applyWidget: func(WidgetWindowState, bool, bool) error { return nil },
		},
		widgetTaskbarToggle: func(bool) error { return nil },
	}
	app.collaborations = map[string]*desktopCollaboration{"room-session": {app: app, ownerSessionID: "room-session", ownerSessionPath: sp}}
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}
	return app
}

// TestDesktopIconRoomOpenReadRoomWithoutNotice verifies that a read Room icon
// (no notice at all) still opens its exact session through the snapshot
// session ref: the open never consults the first notice's TabID or the active
// tab, reuses the existing Room tab, and leaves the unread conversation intact.
func TestDesktopIconRoomOpenReadRoomWithoutNotice(t *testing.T) {
	sp := roomTestSession(t)
	app := newRoomOpenTestApp(t, sp)

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if item == nil {
		t.Fatalf("read Room icon missing: %+v", snapshot.Items)
	}
	if len(item.Notifications) != 0 {
		t.Fatalf("read Room must project no notice: %+v", item.Notifications)
	}
	if item.SessionRef == nil || item.SessionRef.SessionPath != sp || item.SessionRef.TopicID != "room-topic" {
		t.Fatalf("Room session ref = %#v, want topic room-topic path %q", item.SessionRef, sp)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-open-room", Action: "open",
	})
	if result.Status != "accepted" {
		t.Fatalf("open read Room status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != "room-tab" {
		t.Fatalf("open landed on %q, want %q", app.activeTabID, "room-tab")
	}
	if len(app.tabs) != 1 {
		t.Fatalf("open created duplicate tabs: %v", app.tabs)
	}
	if got := app.UnreadState().Summary; len(got.Conversations) != 1 || got.Conversations[0].Key != "room:design" {
		t.Fatalf("Room open must not consume the unread conversation: %+v", got)
	}
	if len(app.iconWidgetState.Kept) != 0 {
		t.Fatalf("Room open retained a task icon: %+v", app.iconWidgetState.Kept)
	}
}

// TestDesktopIconRoomOpenRemountsDetachedSession verifies that a Room whose
// tab was detached (not active, not in the visible tab set) is re-mounted and
// focused through its session ref — never via SetActiveTab on a stale ID.
func TestDesktopIconRoomOpenRemountsDetachedSession(t *testing.T) {
	sp := roomTestSession(t)
	app := newRoomOpenTestApp(t, sp)
	tab := app.tabs["room-tab"]
	tab.ID = "room-detached"
	app.tabs = map[string]*WorkspaceTab{}
	app.detachedSessions = map[string]*WorkspaceTab{sessionRuntimeKey(sp): tab}
	app.activeTabID = ""

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if item == nil || item.SessionRef == nil || item.SessionRef.SessionPath != sp {
		t.Fatalf("detached Room ref missing: %#v", item)
	}
	result := app.ApplyDesktopIconAction(DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "req-open-detached-room", Action: "open",
	})
	if result.Status != "accepted" {
		t.Fatalf("open detached Room status = %q error %q", result.Status, result.Error)
	}
	if app.activeTabID != "room-detached" {
		t.Fatalf("open focused %q, want remounted %q", app.activeTabID, "room-detached")
	}
	remounted := app.tabs["room-detached"]
	if remounted == nil || sessionRuntimeKey(remounted.currentSessionPath()) != sessionRuntimeKey(sp) {
		t.Fatalf("detached Room was not remounted: tabs=%v detached=%v", app.tabs, app.detachedSessions)
	}
	if len(app.detachedSessions) != 0 {
		t.Fatalf("remounted session stayed detached: %+v", app.detachedSessions)
	}
}

// TestDesktopIconRoomOpenIsIdempotent verifies repeated opens reuse the same
// session tab instead of piling up duplicate tabs.
func TestDesktopIconRoomOpenIsIdempotent(t *testing.T) {
	sp := roomTestSession(t)
	app := newRoomOpenTestApp(t, sp)
	open := func(requestID string) {
		snapshot := app.GetDesktopIconSnapshot()
		item := findDesktopIconItem(snapshot.Items, "conversation:room:design")
		if item == nil {
			t.Fatalf("Room icon missing before %s", requestID)
		}
		result := app.ApplyDesktopIconAction(DesktopIconActionInput{
			ItemID: item.ID, Revision: item.Revision, RequestID: requestID, Action: "open",
		})
		if result.Status != "accepted" {
			t.Fatalf("open %s status = %q error %q", requestID, result.Status, result.Error)
		}
	}
	open("req-room-1")
	if app.activeTabID != "room-tab" {
		t.Fatalf("first open focused %q, want %q", app.activeTabID, "room-tab")
	}
	// Re-enter widget mode and open the same Room again: the same tab must be
	// reused and no second tab created.
	app.widgetMode = true
	open("req-room-2")
	if app.activeTabID != "room-tab" {
		t.Fatalf("second open focused %q, want reused %q", app.activeTabID, "room-tab")
	}
	if len(app.tabs) != 1 {
		t.Fatalf("repeated Room open created duplicate tabs: %v", app.tabs)
	}
}

// TestDesktopIconResolveRoomRequiresSessionRef pins the unified identity
// contract for Rooms: SourceID/notice alone are never a valid open target, and
// a missing ref or session path fails explicitly instead of falling back to
// the active session.
func TestDesktopIconResolveRoomRequiresSessionRef(t *testing.T) {
	app := &App{tabs: map[string]*WorkspaceTab{}}
	if tabID, err := app.resolveDesktopIconRoomTab(DesktopIconItem{ID: "conversation:room:design", Kind: "room", SourceID: "conversation:room:design"}); err == nil || tabID != "" || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("room without ref resolved to %q, %v; want explicit identity failure", tabID, err)
	}
	if _, err := app.resolveDesktopIconRoomTab(DesktopIconItem{ID: "conversation:room:design", Kind: "room", SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "room-topic"}}); err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("room with empty session path = %v, want explicit identity error", err)
	}
	if _, err := app.resolveDesktopIconRoomTab(DesktopIconItem{ID: "conversation:room:design", Kind: "room", SessionRef: &DesktopIconTaskRef{Scope: "global", TopicID: "room-topic", SessionPath: `D:\no\such\room.jsonl`}}); err == nil {
		t.Fatal("an unvalidatable Room session path must fail through OpenTopicSession")
	}
}

// TestDesktopIconRoomOpenMissingRefKeepsActiveTab verifies a failed Room open
// returns explicitly and never falls back to the currently active tab.
func TestDesktopIconRoomOpenMissingRefKeepsActiveTab(t *testing.T) {
	app := &App{
		tabs:                  map[string]*WorkspaceTab{"stale-active": {ID: "stale-active"}},
		activeTabID:           "stale-active",
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, CompletionSummaries: map[string]desktopIconCompletionSummary{},
		},
	}
	item := DesktopIconItem{
		ID: "conversation:room:design", Kind: "room", SourceID: "conversation:room:design",
		Title: "产品 Room", Status: "unread", Position: DesktopIconPosition{Row: "top", Zone: "conversation", Order: 0},
	}
	err := app.applyDesktopIconActionLocked(item, nil, DesktopIconActionInput{Action: "open"})
	if err == nil || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Room open without ref = %v, want explicit identity error", err)
	}
	if app.activeTabID != "stale-active" {
		t.Fatalf("failed Room open changed the active tab to %q", app.activeTabID)
	}
}

// TestBuildDesktopIconSnapshotRoomCarriesSessionRef pins that Room items carry
// the backend-generated Room identity while unresolved IM/person items keep
// their own ResolveUnreadSession open route and never pick up a Room ref.
func TestBuildDesktopIconSnapshotRoomCarriesSessionRef(t *testing.T) {
	sp := `D:\Work\rooms\room.jsonl`
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 1, Conversations: []unread.Conversation{{
		Key: "room:design", Source: unread.SourceRoom, SessionID: "room-session", Title: "产品 Room",
	}}}}
	refs := map[string]*DesktopIconTaskRef{"room-session": {Scope: "global", TopicID: "room-topic", SessionPath: sp}}
	snapshot := buildDesktopIconSnapshot(nil, state, nil, desktopIconPersistedState{}, 0, nil, refs, nil, nil)
	room := findDesktopIconItem(snapshot.Items, "conversation:room:design")
	if room == nil || room.SessionRef == nil || room.SessionRef.SessionPath != sp || room.SessionRef.TopicID != "room-topic" || room.SessionRef.Scope != "global" {
		t.Fatalf("Room session ref = %#v item=%+v", room, room)
	}

	state.Summary.Conversations = []unread.Conversation{{
		Key: "im:user-1", Source: unread.SourceIM, SessionID: "room-session", Title: "用户",
	}}
	snapshot = buildDesktopIconSnapshot(nil, state, nil, desktopIconPersistedState{}, 0, nil, refs, nil, nil)
	person := findDesktopIconItem(snapshot.Items, "conversation:im:user-1")
	if person == nil || person.SessionRef != nil {
		t.Fatalf("unresolved person must not carry a Room session ref: %#v", person)
	}
}

func TestBuildDesktopIconSnapshotPersonReusesSessionAgentIdentity(t *testing.T) {
	sp := `D:\sessions\im-user.jsonl`
	sources := []widgetSource{{meta: TabMeta{
		ID: "tab-im", SessionID: "session-im", Scope: "project", WorkspaceRoot: `D:\Work\WG2`,
		TopicID: "topic-im", SessionPath: sp, ProjectIcon: "python",
	}}}
	conversation := unread.Conversation{
		Key: "im:user-1", Source: unread.SourceIM, SessionID: "path:" + sp, Title: "user@example.com",
		LatestSequence: 1, UnreadCount: 1, Items: []unread.Item{{ID: "message-1", Sequence: 1, Kind: "message", Priority: unread.PriorityNormal}},
	}
	state := UnreadState{Available: true, Summary: unread.Summary{Revision: 1, Conversations: []unread.Conversation{conversation}}}
	treePresentations := desktopIconSessionPresentations(nil, []ProjectNode{{
		Kind: "session", Root: `D:\Work\WG2`, TopicID: "topic-im", SessionID: "persisted-session-im",
		SessionPath: sp, ProjectIcon: "python",
	}})
	restarted := buildDesktopIconSnapshotWithPresentations(nil, state, nil, desktopIconPersistedState{}, 0, nil, nil, nil, treePresentations, nil, nil)
	restartedPerson := findDesktopIconItem(restarted.Items, "conversation:im:user-1")
	if restartedPerson == nil || restartedPerson.SessionID != "persisted-session-im" || restartedPerson.WorkspaceIcon != "python" || restartedPerson.SessionRef == nil || restartedPerson.SessionRef.SessionPath != sp {
		t.Fatalf("restarted person session identity = %#v", restartedPerson)
	}

	// The idle session has no task row, but the IM row still gets its exact
	// Agent Icon seed, workspace badge and durable session ref.
	snapshot := buildDesktopIconSnapshot(sources, state, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
	person := findDesktopIconItem(snapshot.Items, "conversation:im:user-1")
	if person == nil || person.SessionID != "session-im" || person.WorkspaceIcon != "python" || person.SessionRef == nil || person.SessionRef.SessionPath != sp || person.ConversationSequence != 1 {
		t.Fatalf("person session identity = %#v", person)
	}

	// Once the real task row is visible, path-level matching projects the same
	// unread onto that row and suppresses the duplicate person icon.
	sources[0].meta.RunningWork = true
	snapshot = buildDesktopIconSnapshot(sources, state, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
	if findDesktopIconItem(snapshot.Items, "conversation:im:user-1") != nil {
		t.Fatal("path-bound IM row duplicated its visible session task icon")
	}
	task := findDesktopIconItem(snapshot.Items, "task:tab-im")
	if task == nil || len(task.Notifications) != 1 || task.Notifications[0].Conversation != "im:user-1" {
		t.Fatalf("task did not receive the path-bound IM notice: %#v", task)
	}
}

func TestDesktopIconPersonRemoveIsDurableAndNewMessageReappears(t *testing.T) {
	isolateDesktopUserDirs(t)
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptIM(unread.IMInput{ConversationKey: "user-1", MessageID: "message-1", SessionID: "path:D:\\sessions\\im-user.jsonl", Title: "user@example.com", ReceivedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	app := newSummaryTestApp(t, nil, fakeCompletionSummaryGenerator{})
	app.unreadStore = store
	item := findDesktopIconItem(app.GetDesktopIconSnapshot().Items, "conversation:im:user-1")
	if item == nil || item.ConversationSequence != 1 {
		t.Fatalf("person icon before remove = %#v", item)
	}
	input := DesktopIconActionInput{ItemID: item.ID, NoticeID: item.Notifications[0].ID, Revision: item.Revision, RequestID: "remove-person-1", Action: "remove"}
	result := app.ApplyDesktopIconAction(input)
	if result.Status != "accepted" || findDesktopIconItem(result.Snapshot.Items, item.ID) != nil {
		t.Fatalf("remove result = status %q error %q items %+v", result.Status, result.Error, result.Snapshot.Items)
	}
	if app.UnreadState().Summary.TotalUnread != 0 || app.iconWidgetState.DismissedConversations["im:user-1"] != 1 {
		t.Fatalf("remove watermarks = unread %+v dismissed %+v", app.UnreadState().Summary, app.iconWidgetState.DismissedConversations)
	}
	if duplicate := app.ApplyDesktopIconAction(input); duplicate.Status != "already_applied" {
		t.Fatalf("duplicate remove = status %q error %q", duplicate.Status, duplicate.Error)
	}

	if _, err := store.AcceptIM(unread.IMInput{ConversationKey: "user-1", MessageID: "message-2", ReceivedAt: time.Now().Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	reappeared := findDesktopIconItem(app.GetDesktopIconSnapshot().Items, item.ID)
	if reappeared == nil || reappeared.ConversationSequence != 2 || reappeared.UnreadCount != 1 {
		t.Fatalf("new message did not restore removed person icon: %#v", reappeared)
	}
}

// TestDesktopIconRoomRefsRestoresPersistedRoom verifies a Room remains
// resolvable after its collaboration runtime has gone away and only the
// persisted project tree plus session sidecar remain.
func TestDesktopIconRoomRefsRestoresPersistedRoom(t *testing.T) {
	sp := roomTestSession(t)
	meta, ok, err := agent.LoadBranchMeta(sp)
	if err != nil || !ok {
		t.Fatalf("load room branch meta: ok=%v err=%v", ok, err)
	}
	app := &App{}
	refs := app.desktopIconRoomRefs([]ProjectNode{{
		Kind: "global_topic", TopicID: meta.TopicID, SessionPath: sp,
		SessionKind: string(agent.SessionKindCollaboration),
	}})
	ref := refs[meta.ID]
	if ref == nil || ref.SessionPath != sp || ref.TopicID != meta.TopicID || ref.Scope != "global" {
		t.Fatalf("persisted Room ref = %#v, want session %q topic %q", ref, sp, meta.TopicID)
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

// TestDesktopIconAgentIdentityFields verifies the display-only Agent Icon
// identity carried by task snapshots: the stable SessionID seed (live tab or
// retained kept; legacy kept entries stay empty for frontend fallback) and the
// normalized workspace icon. These fields are revision-bearing so an identity
// or icon change refreshes the icon, but they never participate in opening.
func TestDesktopIconAgentIdentityFields(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := filepath.Join(t.TempDir(), "WG2")
	if err := setProjectIcon(root, "python"); err != nil {
		t.Fatalf("setProjectIcon: %v", err)
	}
	sources := []widgetSource{{meta: TabMeta{
		ID: "task-1", SessionID: "session-1", Scope: "project", WorkspaceRoot: root,
		TopicID: "topic-1", SessionPath: "sp-1", ProjectIcon: "python", RunningWork: true,
	}}}
	snapshot := buildDesktopIconSnapshot(sources, UnreadState{}, nil, desktopIconPersistedState{}, 0, nil, nil, nil, nil)
	task := findDesktopIconItem(snapshot.Items, "task:task-1")
	if task == nil || task.SessionID != "session-1" || task.WorkspaceIcon != "python" {
		t.Fatalf("live task identity = %#v, want sessionId session-1 / workspaceIcon python", task)
	}

	kept := desktopIconKept{
		ItemID: "task:kept-1", SourceID: "kept-1", Title: "t", Summary: "s", Order: 0, Revision: "r",
		SessionID: "kept-session", Scope: "project", WorkspaceRoot: root, TopicID: "topic-1", SessionPath: "sp-1",
	}
	state := desktopIconPersistedState{Kept: map[string]desktopIconKept{kept.ItemID: kept}}
	snapshot = buildDesktopIconSnapshot(nil, UnreadState{}, nil, state, 0, nil, nil, nil, nil)
	retained := findDesktopIconItem(snapshot.Items, "task:kept-1")
	if retained == nil || retained.SessionID != "kept-session" || retained.WorkspaceIcon != "python" {
		t.Fatalf("retained task identity = %#v, want kept sessionId and project icon", retained)
	}

	// Legacy kept entries predate SessionID persistence: empty stays empty so
	// the frontend falls back to sessionRef/sessionPath instead of guessing.
	legacy := kept
	legacy.ItemID = "task:legacy-1"
	legacy.SessionID = ""
	state = desktopIconPersistedState{Kept: map[string]desktopIconKept{legacy.ItemID: legacy}}
	snapshot = buildDesktopIconSnapshot(nil, UnreadState{}, nil, state, 0, nil, nil, nil, nil)
	old := findDesktopIconItem(snapshot.Items, "task:legacy-1")
	if old == nil || old.SessionID != "" || old.WorkspaceIcon == "" || old.SessionRef == nil || old.SessionRef.SessionPath != "sp-1" {
		t.Fatalf("legacy retained task = %#v, want empty sessionId with sessionRef fallback", old)
	}

	item := DesktopIconItem{ID: "task:1", Kind: "task", Status: "idle", Position: DesktopIconPosition{Row: "bottom", Zone: "running"}}
	base := desktopIconItemRevision(item)
	item.SessionID = "session-1"
	if desktopIconItemRevision(item) == base {
		t.Fatal("a changed identity seed must change the item revision")
	}
	item.WorkspaceIcon = "python"
	if desktopIconItemRevision(item) == base {
		t.Fatal("a changed workspace icon must change the item revision")
	}
	beforeRoomIcon := desktopIconItemRevision(item)
	item.Icon = "discussion"
	if desktopIconItemRevision(item) == beforeRoomIcon {
		t.Fatal("a changed Room icon must change the item revision")
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

func TestDesktopIconOpenWorkspaceCreatesOnceAndRetriesExit(t *testing.T) {
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
		ctx:                   nil,
		widgetMode:            true,
		widgetStyle:           "icons",
		iconWidgetStateLoaded: true,
		iconWidgetState: desktopIconPersistedState{
			Positions: map[string]DesktopIconPosition{}, Kept: map[string]desktopIconKept{}, WorkspaceSlots: desktopWorkspacePinLimit,
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
	app.projectTreeChangedHook = func() {}
	app.readyHook = func() {}
	t.Cleanup(func() {
		app.mu.RLock()
		tabs := make([]*WorkspaceTab, 0, len(app.tabs))
		for _, tab := range app.tabs {
			tabs = append(tabs, tab)
		}
		app.mu.RUnlock()
		for _, tab := range tabs {
			app.closeTabRuntime(tab)
		}
	})

	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "workspace:"+rootB)
	if item == nil {
		t.Fatalf("workspace B icon missing: %+v", snapshot.Items)
	}
	input := DesktopIconActionInput{
		ItemID: item.ID, Revision: item.Revision, RequestID: "open-workspace-b", Action: "open",
	}
	failed := app.ApplyDesktopIconAction(input)
	if failed.Status != "retryable_error" || !strings.Contains(failed.Error, "desktop window is not ready") {
		t.Fatalf("first workspace open = %+v, want explicit retryable exit failure", failed)
	}
	if len(app.tabs) != 1 {
		t.Fatalf("single-surface workspace open left %d visible tabs after failed exit, want only the new tab", len(app.tabs))
	}
	createdID := app.activeTabID
	if createdID == tabA.ID || createdID == tabB.ID {
		t.Fatalf("workspace open reused old tab %q", createdID)
	}
	if created := app.tabs[createdID]; created == nil || created.createRequestID != input.RequestID {
		t.Fatalf("created workspace session request identity = %+v, want %q", created, input.RequestID)
	}
	// Simulate a crash gap: the tab snapshot was persisted, while the widget
	// receipt still has no TabID. Recovery must resolve the existing tab by the
	// same creation request instead of creating another session.
	app.iconWidgetState.Applied[len(app.iconWidgetState.Applied)-1].TabID = ""
	if err := app.saveDesktopIconStateLocked(); err != nil {
		t.Fatalf("persist crash-gap receipt: %v", err)
	}
	app.ctx = context.Background()
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("automatic workspace open recovery: %v", err)
	}
	if receipt := app.iconWidgetState.Applied[len(app.iconWidgetState.Applied)-1]; receipt.Status != "applied" || receipt.TabID != createdID {
		t.Fatalf("recovered workspace receipt = %+v, want applied tab %q", receipt, createdID)
	}
	if len(app.tabs) != 1 || app.activeTabID != createdID {
		t.Fatalf("workspace open retry created/switched session: tabs=%d active=%q want %q", len(app.tabs), app.activeTabID, createdID)
	}
	active := app.activeTab()
	if active == nil || normalizeProjectRoot(active.WorkspaceRoot) != normalizeProjectRoot(rootB) {
		got := ""
		if active != nil {
			got = active.WorkspaceRoot
		}
		t.Fatalf("active workspace = %q, want %q", got, rootB)
	}
	if active.SessionPath == "" || (active.sessionKind != "" && active.sessionKind != agent.SessionKindNormal) || active.workID != "" || active.workRequestID != "" {
		t.Fatalf("workspace open did not create an ordinary blank session: %+v", active)
	}
	duplicate := app.ApplyDesktopIconAction(input)
	if duplicate.Status != "already_applied" || len(app.tabs) != 1 || app.activeTabID != createdID {
		t.Fatalf("duplicate workspace open was not idempotent: result=%+v tabs=%d active=%q", duplicate, len(app.tabs), app.activeTabID)
	}
}

func unwritableDesktopIconStateHome(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state-file")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDesktopIconRandomizeRollsBackFailedWriteAndRetries(t *testing.T) {
	tab, _ := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	item := findDesktopIconItem(app.GetDesktopIconSnapshot().Items, "task:"+tab.ID)
	if item == nil {
		t.Fatal("task icon missing")
	}
	input := DesktopIconActionInput{ItemID: item.ID, Revision: item.Revision, RequestID: "req-randomize", Action: "randomize_icon"}
	t.Setenv("WorkGround2_STATE_HOME", unwritableDesktopIconStateHome(t))
	failed := app.ApplyDesktopIconAction(input)
	if failed.Status != "retryable_error" || len(app.iconWidgetState.AppearanceSeeds) != 0 || len(app.iconWidgetState.Applied) != 0 {
		t.Fatalf("failed randomize mutated state: result=%+v state=%+v", failed, app.iconWidgetState)
	}
	t.Setenv("WorkGround2_STATE_HOME", t.TempDir())
	retried := app.ApplyDesktopIconAction(input)
	changed := findDesktopIconItem(retried.Snapshot.Items, item.ID)
	if retried.Status != "accepted" || changed == nil || changed.AppearanceSeed == "" || changed.AppearanceSeed == item.AppearanceSeed {
		t.Fatalf("randomize retry did not commit one new appearance: result=%+v item=%+v", retried, changed)
	}
	seed := changed.AppearanceSeed
	duplicate := app.ApplyDesktopIconAction(input)
	dupItem := findDesktopIconItem(duplicate.Snapshot.Items, item.ID)
	if duplicate.Status != "already_applied" || dupItem == nil || dupItem.AppearanceSeed != seed {
		t.Fatalf("randomize duplicate was not idempotent: result=%+v item=%+v", duplicate, dupItem)
	}
}

func TestDesktopIconRenameRollsBackFailedPrepareAndRetries(t *testing.T) {
	tab, sp := completionTestTab(t, 1000)
	app := newSummaryTestApp(t, tab, fakeCompletionSummaryGenerator{})
	app.sessionDirsOverride = []string{filepath.Dir(sp)}
	item := findDesktopIconItem(app.GetDesktopIconSnapshot().Items, "task:"+tab.ID)
	if item == nil {
		t.Fatal("task icon missing")
	}
	input := DesktopIconActionInput{ItemID: item.ID, Revision: item.Revision, RequestID: "req-rename", Action: "rename", Values: []string{"新的名字"}}
	t.Setenv("WorkGround2_STATE_HOME", unwritableDesktopIconStateHome(t))
	failed := app.ApplyDesktopIconAction(input)
	if failed.Status != "retryable_error" || len(app.iconWidgetState.Applied) != 0 {
		t.Fatalf("failed rename prepare mutated receipts: result=%+v receipts=%+v", failed, app.iconWidgetState.Applied)
	}
	t.Setenv("WorkGround2_STATE_HOME", t.TempDir())
	retried := app.ApplyDesktopIconAction(input)
	if retried.Status != "accepted" {
		t.Fatalf("rename retry = %+v", retried)
	}
	if title := loadSessionTitles(filepath.Dir(sp))[filepath.Base(sp)]; title != "新的名字" {
		t.Fatalf("renamed session title = %q", title)
	}
	renamed := findDesktopIconItem(retried.Snapshot.Items, item.ID)
	if renamed == nil || renamed.Title != "新的名字" {
		t.Fatalf("rename response snapshot item = %+v", renamed)
	}
	if duplicate := app.ApplyDesktopIconAction(input); duplicate.Status != "already_applied" {
		t.Fatalf("rename duplicate = %+v", duplicate)
	}
}

// retainedRenameFixture builds an App whose only task icon is a retained entry
// whose live tab is gone, so the retained Kept projection is the visible title.
func retainedRenameFixture(t *testing.T) (*App, DesktopIconItem, string) {
	t.Helper()
	_, sp := completionTestTab(t, 0)
	app := newSummaryTestApp(t, nil, fakeCompletionSummaryGenerator{})
	app.sessionDirsOverride = []string{filepath.Dir(sp)}
	app.iconWidgetState.Kept["task:stale"] = desktopIconKept{
		ItemID: "task:stale", SourceID: "stale", Title: "原标题", Summary: "旧摘要", Order: 0,
		Scope: "global", TopicID: "topic-a", SessionPath: sp,
	}
	snapshot := app.GetDesktopIconSnapshot()
	item := findDesktopIconItem(snapshot.Items, "task:stale")
	if item == nil {
		t.Fatalf("retained icon missing: %+v", snapshot.Items)
	}
	return app, *item, sp
}

// TestDesktopIconRenameRetainedUpdatesKeptTitleAndSnapshot reproduces the bug:
// a retained icon reads its title from Kept.Title, so a successful Session
// rename must update that projection and change the item revision immediately.
func TestDesktopIconRenameRetainedUpdatesKeptTitleAndSnapshot(t *testing.T) {
	app, item, sp := retainedRenameFixture(t)

	input := DesktopIconActionInput{ItemID: item.ID, Revision: item.Revision, RequestID: "req-rename-retained", Action: "rename", Values: []string{"新的名字"}}
	result := app.ApplyDesktopIconAction(input)
	if result.Status != "accepted" {
		t.Fatalf("retained rename = %+v", result)
	}
	if kept := app.iconWidgetState.Kept["task:stale"]; kept.Title != "新的名字" {
		t.Fatalf("retained Kept title = %q, want 新的名字", kept.Title)
	}
	if title := loadSessionTitles(filepath.Dir(sp))[filepath.Base(sp)]; title != "新的名字" {
		t.Fatalf("session sidecar title = %q", title)
	}
	renamed := findDesktopIconItem(result.Snapshot.Items, item.ID)
	if renamed == nil || renamed.Title != "新的名字" {
		t.Fatalf("rename response snapshot item = %+v", renamed)
	}
	if renamed.Revision == item.Revision {
		t.Fatalf("rename did not change the item revision: %q", renamed.Revision)
	}
	if duplicate := app.ApplyDesktopIconAction(input); duplicate.Status != "already_applied" {
		t.Fatalf("retained rename duplicate = %+v", duplicate)
	}
	if kept := app.iconWidgetState.Kept["task:stale"]; kept.Title != "新的名字" {
		t.Fatalf("duplicate rename changed Kept title to %q", kept.Title)
	}
}

// TestDesktopIconRenameRetainedRecoversFromPendingReceipt covers startup
// recovery: the sidecar already carries the new title, but the retained Kept
// projection is stale. Recovery must project it without a second write failure.
func TestDesktopIconRenameRetainedRecoversFromPendingReceipt(t *testing.T) {
	app, item, sp := retainedRenameFixture(t)
	if err := setSessionTitle(filepath.Dir(sp), sp, "新的名字"); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	app.iconWidgetState.Applied = []desktopIconReceipt{{
		RequestID: "req-rename-retained", Intent: "intent", Status: "pending", Action: "rename",
		ItemID: item.ID, SessionPath: sp, Text: "新的名字", AppliedAt: time.Now().UnixMilli(),
	}}
	if err := app.recoverDesktopIconActionsLocked(); err != nil {
		t.Fatalf("recover retained rename: %v", err)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("recovered rename receipt = %+v", app.iconWidgetState.Applied[0])
	}
	if kept := app.iconWidgetState.Kept["task:stale"]; kept.Title != "新的名字" {
		t.Fatalf("recovery did not project retained title: %+v", kept)
	}
}

// TestDesktopIconRenameRetainedPendingReceiptRetriesSameRequest covers a final
// state save failure: the receipt stays pending while the sidecar already has
// the new title. Retrying the same requestId must converge the retained title
// and settle idempotently instead of silently succeeding or double-applying.
func TestDesktopIconRenameRetainedPendingReceiptRetriesSameRequest(t *testing.T) {
	app, item, sp := retainedRenameFixture(t)
	if err := setSessionTitle(filepath.Dir(sp), sp, "新的名字"); err != nil {
		t.Fatalf("write sidecar: %v", err)
	}
	input := DesktopIconActionInput{ItemID: item.ID, Revision: item.Revision, RequestID: "req-rename-retained", Action: "rename", Values: []string{"新的名字"}}
	app.iconWidgetState.Applied = []desktopIconReceipt{{
		RequestID: input.RequestID, Intent: desktopIconIntent(input), Status: "pending", Action: "rename",
		ItemID: item.ID, SessionPath: sp, Text: "新的名字", AppliedAt: time.Now().UnixMilli(),
	}}
	result := app.ApplyDesktopIconAction(input)
	if result.Status != "already_applied" {
		t.Fatalf("pending rename retry = %+v", result)
	}
	if kept := app.iconWidgetState.Kept["task:stale"]; kept.Title != "新的名字" {
		t.Fatalf("retry did not project retained title: %+v", kept)
	}
	if app.iconWidgetState.Applied[0].Status != "applied" {
		t.Fatalf("retry did not settle the receipt: %+v", app.iconWidgetState.Applied[0])
	}
}
