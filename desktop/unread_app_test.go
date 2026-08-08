package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/bot"
	"workground2/internal/collab"
	"workground2/internal/unread"
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
