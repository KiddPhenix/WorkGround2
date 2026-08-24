package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/unread"
)

// offscreenRoomUnreadFixture assembles the acceptance scenario: widget mode on,
// Room tab NOT mounted, Room runtime resident in the background consuming its
// stream offscreen. The Room's topic is deliberately never registered in the
// project-tree projection (no topic index, no mounted tab), so the tree has no
// Room node carrying a session identity — mirroring a cold/partial tree while
// the runtime is already live. The startup baseline observation is read.
type offscreenRoomUnreadFixture struct {
	app       *App
	runtime   *desktopCollaboration
	conn      *collaborationConnection
	message   collab.TimelineItem
	createdAt time.Time
}

func newOffscreenRoomUnreadFixture(t *testing.T) *offscreenRoomUnreadFixture {
	t.Helper()
	sp := roomTestSession(t)
	meta, ok, err := agent.LoadBranchMeta(sp)
	if err != nil || !ok {
		t.Fatalf("load Room branch meta: ok=%v err=%v", ok, err)
	}
	meta.Scope = "global"
	meta.TopicTitle = "产品 Room"
	meta.ID = "room-session"
	if err := agent.SaveBranchMeta(sp, meta); err != nil {
		t.Fatalf("save Room branch meta: %v", err)
	}
	if err := os.WriteFile(sp, []byte(`{"role":"user","content":"Room"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write Room session: %v", err)
	}

	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		unreadStore:           store,
		widgetMode:            true,
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
	app.runtimeEvents.emit = func(_ context.Context, name string, _ ...interface{}) {}

	createdAt := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	baseline := collab.Snapshot{
		Room:           collab.Room{ID: "room-a", Name: "产品 Room", CreatedAt: createdAt, LatestSequence: 2},
		LatestSequence: 2,
		Members: []collab.Member{
			{ID: "self", Name: "Me", Agent: collab.AgentDescriptor{ID: "self-agent", Name: "My Agent"}},
			{ID: "alice", Name: "Alice", Agent: collab.AgentDescriptor{ID: "alice-agent", Name: "Alice Agent"}},
		},
	}
	message := collab.TimelineItem{
		ID: "missed-offscreen", Sequence: 3, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{
			ID: "missed-offscreen", AuthorID: "alice", Text: "离屏期间收到的新消息",
			MentionAgentIDs: []string{"self-agent"}, CreatedAt: createdAt.Add(time.Minute),
		},
	}
	conn := &collaborationConnection{
		peer: &fakeCollaborationPeer{}, mode: "client", hostName: "127.0.0.1", port: 39170, room: "room-a",
		memberID: "self", agentID: "self-agent", sessionID: "room-session",
		connectionSession: "existing-session", initialSnapshot: baseline, done: make(chan struct{}),
	}
	runtime := &desktopCollaboration{
		app: app, ownerSessionID: "room-session", ownerSessionPath: sp, conn: conn,
		state: CollaborationState{
			Status: "connected", Mode: "client", Host: conn.hostName, Port: conn.port, Room: conn.room,
			MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: baseline,
		},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
	}
	app.collaborations = map[string]*desktopCollaboration{"room-session": runtime}

	runtime.observeUnread()
	if got := app.UnreadState().Summary; got.TotalUnread != 0 {
		t.Fatalf("baseline must be read, got %+v", got)
	}
	if rooms := desktopIconRoomDescriptors(app.ListProjectTree(), nil); len(rooms) != 0 {
		t.Fatalf("precondition: Room must be absent from the tree projection, got %+v", rooms)
	}
	return &offscreenRoomUnreadFixture{app: app, runtime: runtime, conn: conn, message: message, createdAt: createdAt}
}

func (fx *offscreenRoomUnreadFixture) event(t *testing.T) collab.RoomEvent {
	t.Helper()
	payload, err := json.Marshal(fx.message)
	if err != nil {
		t.Fatal(err)
	}
	return collab.RoomEvent{EventID: "event-3", Room: fx.conn.room, Sequence: fx.message.Sequence, Type: "chat.posted", Payload: payload}
}

func (fx *offscreenRoomUnreadFixture) assertSnapshotUnread(t *testing.T) {
	t.Helper()
	if got := fx.app.UnreadState().Summary; got.TotalUnread != 1 {
		t.Fatalf("unread store after offscreen sync = %+v, want TotalUnread 1", got)
	}
	snapshot := fx.app.GetDesktopIconSnapshot()
	var room *DesktopIconItem
	for i := range snapshot.Items {
		item := &snapshot.Items[i]
		if item.Kind == "room" && (item.UnreadCount > 0 || len(item.Notifications) > 0) {
			room = item
			break
		}
	}
	if room == nil || room.UnreadCount != 1 || room.Status != "unread" {
		t.Fatalf("widget snapshot Room unread = %#v, all items=%+v", room, snapshot.Items)
	}
	if len(room.Notifications) != 1 || room.Notifications[0].ID != fx.message.ID {
		t.Fatalf("widget snapshot Room notice = %+v, want message %q", room.Notifications, fx.message.ID)
	}
}

// TestDesktopIconResidentRoomOffscreenUnreadLowFrequencyRefresh pins acceptance
// #1 for the low-frequency entry: after startup established a read baseline,
// the resident Room's low-frequency pull consumes a missed remote message and
// the next icon snapshot carries the unread number — without opening the Room,
// even though the project-tree projection has no Room node.
func TestDesktopIconResidentRoomOffscreenUnreadLowFrequencyRefresh(t *testing.T) {
	fx := newOffscreenRoomUnreadFixture(t)
	payload, err := json.Marshal(fx.message)
	if err != nil {
		t.Fatal(err)
	}
	fx.conn.peer = &fakeCollaborationPeer{events: []collab.RoomEvent{{
		EventID: "event-3", Room: fx.conn.room, Sequence: fx.message.Sequence, Type: "chat.posted", Payload: payload,
	}}}
	var joins int
	fx.runtime.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		joins++
		return nil, errors.New("resident Room update must not Join")
	}
	if err := fx.runtime.updateConnection(context.Background()); err != nil {
		t.Fatalf("resident Room update: %v", err)
	}
	if joins != 0 {
		t.Fatalf("resident Room update joined %d times", joins)
	}
	fx.assertSnapshotUnread(t)
	close(fx.conn.done)
}

// TestDesktopIconResidentRoomOffscreenUnreadStreamRefresh pins the same
// acceptance scenario through the actual stream entry (consumeStreamEvent):
// the resident stream loop delivers the remote event offscreen and the next
// icon snapshot shows the unread number immediately.
func TestDesktopIconResidentRoomOffscreenUnreadStreamRefresh(t *testing.T) {
	fx := newOffscreenRoomUnreadFixture(t)
	if err := fx.runtime.consumeStreamEvent(context.Background(), fx.conn, fx.event(t)); err != nil {
		t.Fatalf("consumeStreamEvent: %v", err)
	}
	fx.assertSnapshotUnread(t)
	close(fx.conn.done)
}

// TestDesktopIconResidentRoomLeftDoesNotResurrectUnread pins the live-prune
// guard: once the resident runtime's Room identity is cleared (leave/close),
// the merge must no longer treat it as live, so the durable unread history of
// a room that is absent from the project tree stays hidden instead of being
// resurrected as a desktop icon.
func TestDesktopIconResidentRoomLeftDoesNotResurrectUnread(t *testing.T) {
	fx := newOffscreenRoomUnreadFixture(t)
	// Deliver one unread message first so the store really holds history.
	if err := fx.runtime.consumeStreamEvent(context.Background(), fx.conn, fx.event(t)); err != nil {
		t.Fatalf("consumeStreamEvent: %v", err)
	}
	if got := fx.app.UnreadState().Summary; got.TotalUnread != 1 {
		t.Fatalf("unread store = %+v, want 1 pending message", got)
	}
	// leaveCurrent resets the runtime state: Room and Snapshot identity gone.
	fx.runtime.mu.Lock()
	fx.runtime.state = CollaborationState{Status: "disconnected", SessionID: fx.runtime.ownerSessionID}
	fx.runtime.mu.Unlock()

	snapshot := fx.app.GetDesktopIconSnapshot()
	for i := range snapshot.Items {
		if snapshot.Items[i].Kind == "room" && snapshot.Items[i].UnreadCount > 0 {
			t.Fatalf("left Room resurrected unread icon: %+v", snapshot.Items[i])
		}
	}
	close(fx.conn.done)
}
