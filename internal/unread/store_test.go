package unread

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestAttentionPersistsDeduplicatesAndKeepsVisibleEventsRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unread.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 8, 8, 7, 0, 0, 0, time.UTC)
	first, err := store.AcceptAttention(AttentionInput{
		Source: SourceSession, ConversationKey: "session-1", EventID: "ask-1",
		SessionID: "session-1", Title: "Session", Kind: "question", Priority: PriorityHigh, OccurredAt: at,
	})
	if err != nil || first.Duplicate || first.Sequence != 1 || first.ConversationKey != "session:session-1" {
		t.Fatalf("first attention = %+v, err = %v", first, err)
	}
	duplicate, err := store.AcceptAttention(AttentionInput{
		Source: SourceSession, ConversationKey: "session-1", EventID: "ask-1", Kind: "question",
	})
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != 1 {
		t.Fatalf("duplicate attention = %+v, err = %v", duplicate, err)
	}
	visible, err := store.AcceptAttention(AttentionInput{
		Source: SourceSession, ConversationKey: "session-1", EventID: "done-1",
		Kind: "completed", Read: true, OccurredAt: at.Add(time.Minute),
	})
	if err != nil || visible.Duplicate || visible.Sequence != 2 {
		t.Fatalf("visible attention = %+v, err = %v", visible, err)
	}
	if got := store.Summary(); got.TotalUnread != 0 || got.HighPriorityCount != 0 || got.Conversations[0].ReadSequence != 2 {
		t.Fatalf("visible attention did not consume prior pending state: %+v", got)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := reopened.AcceptAttention(AttentionInput{
		Source: SourceSession, ConversationKey: "session-1", EventID: "done-1", Kind: "completed",
	})
	if err != nil || !replayed.Duplicate || reopened.Summary().TotalUnread != 0 {
		t.Fatalf("visible replay resurrected unread: receipt=%+v summary=%+v err=%v", replayed, reopened.Summary(), err)
	}
}

func TestAttentionRejectsUnsupportedSourceAndRetriesPersistence(t *testing.T) {
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
	if _, err := store.AcceptAttention(AttentionInput{Source: SourceIM, ConversationKey: "x", EventID: "e", Kind: "completed"}); err == nil {
		t.Fatal("AcceptAttention accepted an IM source")
	}
	input := AttentionInput{Source: SourceWork, ConversationKey: "work-1", EventID: "run-1", Kind: "completed"}
	if _, err := store.AcceptAttention(input); err == nil {
		t.Fatal("AcceptAttention succeeded while persistence failed")
	}
	if got := store.Summary(); got.TotalUnread != 0 || len(got.Conversations) != 0 {
		t.Fatalf("failed attention leaked into memory: %+v", got)
	}
	fail = false
	receipt, err := store.AcceptAttention(input)
	if err != nil || receipt.Duplicate || receipt.Sequence != 1 {
		t.Fatalf("attention retry = %+v, err = %v", receipt, err)
	}
}

func TestVisibleIMAndRoomEventsAdvanceReadWatermarks(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptIM(IMInput{ConversationKey: "remote", MessageID: "one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptIM(IMInput{ConversationKey: "remote", MessageID: "two", Read: true}); err != nil {
		t.Fatal(err)
	}
	if got := store.Summary(); got.TotalUnread != 0 || got.Conversations[0].ReadSequence != 2 {
		t.Fatalf("visible IM state = %+v", got)
	}

	base := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 1}, LatestSequence: 1}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "room", LocalMemberID: "self", Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	next := base
	next.LatestSequence, next.Room.LatestSequence = 2, 2
	next.Timeline = []collab.TimelineItem{{ID: "chat", Sequence: 2, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "chat", AuthorID: "other"}}}
	conversation, err := store.ObserveRoom(RoomInput{ConversationKey: "room", LocalMemberID: "self", Snapshot: next, Read: true})
	if err != nil || conversation.UnreadCount != 0 || conversation.ReadSequence != 2 {
		t.Fatalf("visible Room state = %+v, err = %v", conversation, err)
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

func TestRoomMentionAttentionPersistsAcrossOutOfOrderAndDuplicateSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unread.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 10}, LatestSequence: 10}
	input := RoomInput{ConversationKey: "mentions", LocalMemberID: "self", LocalAgentID: "agent-self", Snapshot: base}
	if _, err := store.ObserveRoom(input); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 8, 21, 7, 0, 0, 0, time.UTC)
	next := base
	next.LatestSequence, next.Room.LatestSequence = 14, 14
	// Deliberately reverse the authoritative timeline order. ObserveRoom must
	// sort by sequence and persist each exact mention target once.
	next.Timeline = []collab.TimelineItem{
		{ID: "both", Sequence: 14, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "both", AuthorID: "other", MentionMemberIDs: []string{"self"}, MentionAgentIDs: []string{"agent-self"}, CreatedAt: at.Add(4 * time.Minute)}},
		{ID: "high", Sequence: 13, Type: collab.TimelineContribution, Contribution: &collab.Contribution{ID: "high", AuthorID: "other", ActionNeeded: true, CreatedAt: at.Add(3 * time.Minute)}},
		{ID: "agent", Sequence: 12, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "agent", AuthorID: "other", MentionAgentIDs: []string{"agent-self"}, CreatedAt: at.Add(2 * time.Minute)}},
		{ID: "member", Sequence: 11, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "member", AuthorID: "other", MentionMemberIDs: []string{"self"}, CreatedAt: at.Add(time.Minute)}},
	}
	input.Snapshot = next
	conversation, err := store.ObserveRoom(input)
	if err != nil {
		t.Fatal(err)
	}
	if conversation.UnreadCount != 4 || conversation.HighPriorityCount != 4 {
		t.Fatalf("mention projection = %+v", conversation)
	}
	want := []ItemAttention{AttentionMentionMember, AttentionMentionAgent, AttentionNone, AttentionMentionBoth}
	for i, attention := range want {
		if conversation.Items[i].Sequence != uint64(11+i) || conversation.Items[i].Attention != attention {
			t.Fatalf("item %d = %+v, want sequence %d attention %q", i, conversation.Items[i], 11+i, attention)
		}
	}
	beforeRevision := store.Summary().Revision
	duplicate, err := store.ObserveRoom(input)
	if err != nil || duplicate.UnreadCount != 4 || store.Summary().Revision != beforeRevision {
		t.Fatalf("duplicate observation = %+v revision=%d err=%v", duplicate, store.Summary().Revision, err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	persisted := reopened.Summary().Conversations[0]
	for i, attention := range want {
		if persisted.Items[i].Attention != attention {
			t.Fatalf("persisted item %d = %+v, want attention %q", i, persisted.Items[i], attention)
		}
	}
	staleInput := input
	staleInput.Snapshot = base
	stale, err := reopened.ObserveRoom(staleInput)
	if err != nil || stale.UnreadCount != 4 || stale.LatestSequence != 14 {
		t.Fatalf("stale observation = %+v err=%v", stale, err)
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

func TestRoomLegacyIdentityMigrationKeepsOldWaterlineAndRemovesDuplicate(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 2}, LatestSequence: 2}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "legacy-missing-created", LocalMemberID: "self", Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	next := base
	next.LatestSequence, next.Room.LatestSequence = 3, 3
	next.Timeline = []collab.TimelineItem{{
		ID: "new-after-startup", Sequence: 3, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "new-after-startup", AuthorID: "other", Text: "new", CreatedAt: time.Now().UTC()},
	}}
	// This duplicate models the old key changing when optional CreatedAt became
	// available. Its first observation incorrectly established sequence 3 as a
	// read baseline.
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "legacy-with-created", LocalMemberID: "self", Snapshot: next}); err != nil {
		t.Fatal(err)
	}
	conversation, err := store.ObserveRoom(RoomInput{
		ConversationKey: "canonical", LegacyConversationKeys: []string{"legacy-missing-created", "legacy-with-created"},
		LocalMemberID: "self", Snapshot: next,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conversation.Key != "room:canonical" || conversation.ReadSequence != 2 || conversation.LatestSequence != 3 || conversation.UnreadCount != 1 || conversation.Items[0].ID != "new-after-startup" {
		t.Fatalf("migrated Room conversation = %+v", conversation)
	}
	summary := store.Summary()
	if len(summary.Conversations) != 1 || summary.TotalUnread != 1 || summary.Conversations[0].Key != "room:canonical" {
		t.Fatalf("legacy Room identities were not folded atomically: %+v", summary)
	}
}

func TestRoomLegacyIdentityMigrationWriteFailureKeepsPublishedState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	base := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 2}, LatestSequence: 2}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "legacy-missing-created", LocalMemberID: "self", Snapshot: base}); err != nil {
		t.Fatal(err)
	}
	next := base
	next.LatestSequence, next.Room.LatestSequence = 3, 3
	next.Timeline = []collab.TimelineItem{{
		ID: "new-after-startup", Sequence: 3, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "new-after-startup", AuthorID: "other", Text: "new", CreatedAt: time.Now().UTC()},
	}}
	if _, err := store.ObserveRoom(RoomInput{ConversationKey: "legacy-with-created", LocalMemberID: "self", Snapshot: next}); err != nil {
		t.Fatal(err)
	}
	published := store.Summary()
	store.writeFile = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	conversation, err := store.ObserveRoom(RoomInput{
		ConversationKey: "canonical", LegacyConversationKeys: []string{"legacy-missing-created", "legacy-with-created"},
		LocalMemberID: "self", Snapshot: next,
	})
	if err == nil {
		t.Fatal("legacy Room migration unexpectedly persisted")
	}
	if conversation.Key != "room:legacy-missing-created" || conversation.ReadSequence != 2 || conversation.LatestSequence != 2 || conversation.UnreadCount != 0 {
		t.Fatalf("migration failure exposed uncommitted conversation: %+v", conversation)
	}
	if after := store.Summary(); !reflect.DeepEqual(after, published) {
		t.Fatalf("migration failure changed published summary:\nafter=%+v\nbefore=%+v", after, published)
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
