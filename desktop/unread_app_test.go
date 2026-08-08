package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/bot"
	"workground2/internal/collab"
	"workground2/internal/event"
	"workground2/internal/unread"
	"workground2/internal/work"
)

func TestDesktopIMUnreadAcceptsBindsAndSurvivesRecordedError(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store}
	app.recordUnreadError(errors.New("previous transient failure"))
	msg := bot.InboundMessage{
		Platform: bot.PlatformFeishu, ConnectionID: "lark", Domain: "lark",
		ChatType: bot.ChatDM, ChatID: "chat", UserID: "user", UserName: "Alice",
		MessageID: "message-1", ReceivedAt: time.Date(2026, 8, 8, 5, 0, 0, 0, time.UTC),
	}
	receipt, err := app.acceptIMUnread(msg)
	if err != nil || receipt.Duplicate {
		t.Fatalf("accept = %+v, err = %v", receipt, err)
	}
	if err := app.bindIMUnread(msg, "session-1"); err != nil {
		t.Fatal(err)
	}
	state := app.UnreadState()
	if !state.Available || state.Error != "" || state.Summary.TotalUnread != 1 || len(state.Summary.Conversations) != 1 {
		t.Fatalf("state = %+v", state)
	}
	if got := state.Summary.Conversations[0]; got.SessionID != "session-1" || got.Source != unread.SourceIM {
		t.Fatalf("conversation = %+v", got)
	}
}

func TestDesktopSessionAttentionRecordsOnlyBackgroundNonCLIEvents(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	active := &WorkspaceTab{ID: "active", SessionID: "session-active"}
	background := &WorkspaceTab{ID: "background", SessionID: "session-background", SessionPath: filepath.Join(t.TempDir(), "background.jsonl"), TopicTitle: "Background"}
	app := &App{
		tabs: map[string]*WorkspaceTab{"active": active, "background": background}, activeTabID: active.ID,
		unreadStore: store,
	}
	app.observeSessionUnread(background.ID, event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: "ask-1"}})
	app.observeSessionUnread(background.ID, event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: "ask-1"}})
	if got := app.UnreadState().Summary; got.TotalUnread != 1 || got.HighPriorityCount != 1 {
		t.Fatalf("background question state = %+v", got)
	}

	app.activeTabID = background.ID
	app.observeSessionUnread(background.ID, event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: "approval-1"}})
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || got.Conversations[0].ReadSequence != 2 {
		t.Fatalf("visible approval did not keep Session read: %+v", got)
	}

	app.activeTabID = active.ID
	background.recordTurnStarted(1234)
	app.observeSessionUnread(background.ID, event.Event{Kind: event.TurnDone})
	completed := app.UnreadState()
	if got := completed.Summary; got.TotalUnread != 1 || got.Conversations[0].LatestSequence != 3 || got.Conversations[0].Items[0].Kind != "completed" {
		t.Fatalf("background completion state = %+v", got)
	}
	if _, err := store.MarkRead(completed.Summary.Conversations[0].Key, 3); err != nil {
		t.Fatal(err)
	}

	background.runtimeSourcePath = background.currentSessionPath()
	background.runtimeSource = "cli"
	app.observeSessionUnread(background.ID, event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: "cli-ask"}})
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || got.Conversations[0].LatestSequence != 3 {
		t.Fatalf("CLI attention changed unread state: %+v", got)
	}
}

func TestDesktopWorkAttentionUsesStableRunAndInputIdentity(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "work-tab", SessionID: "work-session", TopicTitle: "Build", workID: "work-1", sessionKind: "work"}
	other := &WorkspaceTab{ID: "other"}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab, other.ID: other}, activeTabID: other.ID, unreadStore: store}
	at := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	completed := work.WorkView{
		SchemaVersion: 1,
		Work:          &work.Work{ID: "work-1", Name: "Build", State: work.WorkCompleted, Runs: []work.WorkflowRun{{ID: "run-1", State: work.RunCompleted}}},
		Revision:      10,
	}
	payload, _ := json.Marshal(completed)
	app.observeWorkUnread(work.WorkViewEvent{Type: work.ViewSnapshot, WorkID: "work-1", EventID: "snapshot-10", Revision: 10, Payload: payload, CreatedAt: at})
	app.observeWorkUnread(work.WorkViewEvent{Type: work.ViewSnapshot, WorkID: "work-1", EventID: "resync-10", Revision: 10, Payload: payload, CreatedAt: at.Add(time.Minute)})
	if got := app.UnreadState().Summary; got.TotalUnread != 1 || got.Conversations[0].Source != unread.SourceWork || got.Conversations[0].SessionID != "work-session" {
		t.Fatalf("completed Work state = %+v", got)
	}

	app.activeTabID = tab.ID
	waiting := work.WorkView{
		SchemaVersion: 2,
		Work:          &work.Work{ID: "work-1", Name: "Build", State: work.WorkWaitingUser},
		Revision:      11,
		Inputs: []work.WorkInput{{
			ID: "input-1", WorkID: "work-1", RunID: "run-2", TaskID: "task-1",
			State: work.InputRequested, Revision: 1, UpdatedAt: at.Add(2 * time.Minute),
		}},
	}
	payload, _ = json.Marshal(waiting)
	app.observeWorkUnread(work.WorkViewEvent{Type: work.ViewSnapshot, WorkID: "work-1", EventID: "snapshot-11", Revision: 11, Payload: payload, CreatedAt: at.Add(2 * time.Minute)})
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || got.Conversations[0].LatestSequence != 2 || got.Conversations[0].ReadSequence != 2 {
		t.Fatalf("visible Work question state = %+v", got)
	}
	app.activeTabID = other.ID
	app.observeWorkUnread(work.WorkViewEvent{Type: work.ViewSnapshot, WorkID: "work-1", EventID: "resync-11", Revision: 11, Payload: payload, CreatedAt: at.Add(3 * time.Minute)})
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || got.Conversations[0].LatestSequence != 2 {
		t.Fatalf("visible Work question replay resurrected unread: %+v", got)
	}
	app.observeWorkUnread(work.WorkViewEvent{
		Type: work.ViewAttention, WorkID: "work-1", EventID: "clarification-1", Revision: 11,
		Payload: json.RawMessage(`{"planning":{"kind":"clarification","state":"waiting"}}`), CreatedAt: at.Add(4 * time.Minute),
	})
	if got := app.UnreadState().Summary; got.TotalUnread != 1 || got.Conversations[0].LatestSequence != 3 || got.Conversations[0].Items[0].Kind != "question" {
		t.Fatalf("background Work clarification state = %+v", got)
	}
}

func TestWorkSessionTurnDoneDoesNotDuplicateAggregateWorkUnread(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{ID: "work-tab", SessionID: "work-session", workID: "work-1", sessionKind: "work"}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}, unreadStore: store}
	app.observeSessionUnread(tab.ID, event.Event{Kind: event.TurnDone})
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || len(got.Conversations) != 0 {
		t.Fatalf("Work Session created duplicate Session unread: %+v", got)
	}
}

func TestDesktopRoomUnreadUsesStableSessionAndRoomIdentity(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store}
	created := time.Date(2026, 8, 8, 6, 0, 0, 0, time.UTC)
	c := &desktopCollaboration{
		app: app, ownerSessionID: "owner-session", ownerSessionPath: `D:\sessions\owner.jsonl`,
		state: CollaborationState{
			Room: "room", MemberID: "self", AgentID: "self-agent", SessionID: "room-session",
			Snapshot: collab.Snapshot{Room: collab.Room{ID: "room", Name: "Team", CreatedAt: created, LatestSequence: 2}, LatestSequence: 2},
		},
	}
	c.observeUnread()
	c.state.Snapshot.LatestSequence = 3
	c.state.Snapshot.Room.LatestSequence = 3
	c.state.Snapshot.Timeline = []collab.TimelineItem{{
		ID: "chat", Sequence: 3, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "chat", AuthorID: "other", MentionAgentIDs: []string{"self-agent"}, CreatedAt: created.Add(time.Minute)},
	}}
	c.observeUnread()
	state := app.UnreadState()
	if state.Summary.TotalUnread != 1 || state.Summary.HighPriorityCount != 1 || len(state.Summary.Conversations) != 1 {
		t.Fatalf("state = %+v", state)
	}
	conversation := state.Summary.Conversations[0]
	if conversation.Source != unread.SourceRoom || conversation.SessionID != "owner-session" || conversation.Title != "Team" {
		t.Fatalf("conversation = %+v", conversation)
	}
	read, err := app.MarkUnreadRead(MarkUnreadReadInput{ConversationKey: conversation.Key, UpToSequence: 3})
	if err != nil || read.Summary.TotalUnread != 0 || read.Summary.Conversations[0].ReadSequence != 3 {
		t.Fatalf("read state = %+v, err = %v", read, err)
	}
}
