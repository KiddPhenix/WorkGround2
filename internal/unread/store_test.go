package unread

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/collab"
	"workground2/internal/fileutil"
)

func TestIMPersistsDeduplicatesAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unread.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 1, 2, 3, 0, time.UTC)
	first, err := store.AcceptIM(IMInput{ConversationKey: "remote-a", MessageID: "msg-1", AuthorID: "user", ReceivedAt: at})
	if err != nil || first.Duplicate || first.Sequence != 1 || first.ConversationKey != "im:remote-a" {
		t.Fatalf("first receipt = %+v, err = %v", first, err)
	}
	duplicate, err := store.AcceptIM(IMInput{ConversationKey: "remote-a", MessageID: "msg-1", AuthorID: "user", ReceivedAt: at.Add(time.Minute)})
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != 1 {
		t.Fatalf("duplicate receipt = %+v, err = %v", duplicate, err)
	}
	if revision := store.Summary().Revision; revision != 1 {
		t.Fatalf("revision after duplicate = %d, want 1", revision)
	}
	if err := store.BindSession("im:remote-a", "session-a"); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	summary := reopened.Summary()
	if summary.Revision != 2 || summary.TotalUnread != 1 || len(summary.Conversations) != 1 {
		t.Fatalf("summary = %+v", summary)
	}
	conversation := summary.Conversations[0]
	if conversation.SessionID != "session-a" || conversation.LatestSequence != 1 || conversation.ReadSequence != 0 {
		t.Fatalf("conversation = %+v", conversation)
	}
	if _, err := reopened.MarkRead(conversation.Key, 99); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Summary(); got.Revision != 3 || got.TotalUnread != 0 || got.Conversations[0].ReadSequence != 1 {
		t.Fatalf("read summary = %+v", got)
	}
	afterRead, err := reopened.AcceptIM(IMInput{ConversationKey: "remote-a", MessageID: "msg-1"})
	if err != nil || !afterRead.Duplicate || afterRead.Sequence != 1 {
		t.Fatalf("dedupe after read = %+v, err = %v", afterRead, err)
	}
	if revision := reopened.Summary().Revision; revision != 3 {
		t.Fatalf("revision after read duplicate = %d, want 3", revision)
	}
}

func TestIMFailedPersistenceDoesNotAdvanceMemoryAndCanRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unread.json")
	fail := true
	store, err := open(path, func(path string, data []byte, mode os.FileMode) error {
		if fail {
			return errors.New("disk unavailable")
		}
		return fileutil.AtomicWriteFile(path, data, mode)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptIM(IMInput{ConversationKey: "remote", MessageID: "msg"}); err == nil {
		t.Fatal("AcceptIM succeeded while persistence failed")
	}
	if got := store.Summary(); got.TotalUnread != 0 || len(got.Conversations) != 0 {
		t.Fatalf("failed mutation leaked into memory: %+v", got)
	}
	fail = false
	receipt, err := store.AcceptIM(IMInput{ConversationKey: "remote", MessageID: "msg"})
	if err != nil || receipt.Sequence != 1 || receipt.Duplicate {
		t.Fatalf("retry receipt = %+v, err = %v", receipt, err)
	}
}

func TestRoomUsesBaselineAndProjectsOnlyRemoteReadableItems(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	baseAt := time.Date(2026, 8, 8, 2, 0, 0, 0, time.UTC)
	base := collab.Snapshot{
		Room:           collab.Room{ID: "room", Name: "Room", LatestSequence: 3},
		LatestSequence: 3,
		Timeline: []collab.TimelineItem{{
			ID: "old", Sequence: 3, Type: collab.TimelineChat,
			Chat: &collab.ChatMessage{ID: "old", AuthorID: "other", Text: "old", CreatedAt: baseAt},
		}},
	}
	conversation, err := store.ObserveRoom(RoomInput{ConversationKey: "instance", SessionID: "session", LocalMemberID: "self", LocalAgentID: "agent-self", Snapshot: base})
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UnreadCount != 0 || conversation.ReadSequence != 3 || conversation.LatestSequence != 3 {
		t.Fatalf("baseline = %+v", conversation)
	}

	next := base
	next.LatestSequence = 9
	next.Room.LatestSequence = 9
	next.Timeline = append(next.Timeline,
		collab.TimelineItem{ID: "own", Sequence: 4, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "own", AuthorID: "self", CreatedAt: baseAt.Add(time.Minute)}},
		collab.TimelineItem{ID: "system", Sequence: 5, Type: collab.TimelineSystem, System: &collab.SystemEvent{Kind: "member.online"}},
		collab.TimelineItem{ID: "chat", Sequence: 6, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "chat", AuthorID: "other", MentionMemberIDs: []string{"self"}, CreatedAt: baseAt.Add(2 * time.Minute)}},
		collab.TimelineItem{ID: "run", Sequence: 7, Type: collab.TimelineAgentRun, AgentRun: &collab.AgentRun{ID: "run", OwnerID: "other"}},
		collab.TimelineItem{ID: "file", Sequence: 8, Type: collab.TimelineFile, File: &collab.FileOffer{ID: "file", OwnerID: "other", CreatedAt: baseAt.Add(3 * time.Minute)}},
		collab.TimelineItem{ID: "handoff", Sequence: 9, Type: collab.TimelineAgentResult, AgentResult: &collab.AgentResult{ID: "handoff", OwnerID: "other", CreatedAt: baseAt.Add(4 * time.Minute), Handoffs: []collab.AgentHandoff{{TargetAgentID: "agent-self", RequiresResponse: true}}}},
	)
	conversation, err = store.ObserveRoom(RoomInput{ConversationKey: "instance", SessionID: "session", LocalMemberID: "self", LocalAgentID: "agent-self", Snapshot: next})
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UnreadCount != 3 || conversation.HighPriorityCount != 2 || conversation.LatestSequence != 9 {
		t.Fatalf("projected = %+v", conversation)
	}
	if conversation.Items[0].ID != "chat" || conversation.Items[1].ID != "file" || conversation.Items[2].ID != "handoff" {
		t.Fatalf("items = %+v", conversation.Items)
	}

	conversation, err = store.MarkRead(conversation.Key, 6)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UnreadCount != 2 || conversation.ReadSequence != 6 || conversation.Items[0].Sequence != 8 {
		t.Fatalf("partially read = %+v", conversation)
	}
	stale, err := store.ObserveRoom(RoomInput{ConversationKey: "instance", LocalMemberID: "self", Snapshot: base})
	if err != nil || stale.LatestSequence != 9 || stale.UnreadCount != 2 {
		t.Fatalf("stale observation = %+v, err = %v", stale, err)
	}
}

func TestRoomRevisionUpdatesOnePendingItem(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 1}, LatestSequence: 1}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "room", LocalMemberID: "self", Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	first := base
	first.LatestSequence, first.Room.LatestSequence = 2, 2
	first.Timeline = []collab.TimelineItem{{ID: "result", Sequence: 2, Type: collab.TimelineAgentResult, AgentResult: &collab.AgentResult{ID: "result", OwnerID: "other", Revision: 1}}}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "room", LocalMemberID: "self", Snapshot: first}); err != nil {
		t.Fatal(err)
	}
	revised := first
	revised.LatestSequence, revised.Room.LatestSequence = 3, 3
	revised.Timeline[0].Sequence = 3
	revised.Timeline[0].AgentResult.Revision = 2
	conversation, err := store.ObserveRoom(RoomInput{ConversationKey: "room", LocalMemberID: "self", Snapshot: revised})
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UnreadCount != 1 || conversation.Items[0].Sequence != 3 {
		t.Fatalf("revision created duplicate unread: %+v", conversation)
	}
}

func TestOpenRejectsCorruptState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unread.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted corrupt unread state")
	}
}
