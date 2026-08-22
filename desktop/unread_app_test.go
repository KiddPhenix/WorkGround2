package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
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
	if got := app.UnreadState().Summary; got.TotalUnread != 1 || got.HighPriorityCount != 1 || got.Conversations[0].SessionID != "path:"+background.SessionPath {
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

// TestUnreadTargetVisibleWidgetModeBlocksBackgroundAutoRead pins the visibility
// gate: while the main window is in widget mode the background active tab is
// hidden behind the widget, so session/work targets must never count as
// visible; normal window mode keeps the existing visible-equals-read semantics.
func TestUnreadTargetVisibleWidgetModeBlocksBackgroundAutoRead(t *testing.T) {
	app := &App{
		widgetMode: true,
		tabs: map[string]*WorkspaceTab{
			"tab-1": {ID: "tab-1", SessionID: "session-1", workID: "work-1"},
		},
		activeTabID: "tab-1",
	}
	for _, probe := range []struct{ sessionID, workID string }{
		{"session-1", ""},
		{"", "work-1"},
		{`path:D:\sessions\session-1.jsonl`, ""},
	} {
		if app.unreadTargetVisible(probe.sessionID, probe.workID) {
			t.Fatalf("widget mode treated background target %q/%q as visible", probe.sessionID, probe.workID)
		}
	}
	app.widgetMode = false
	for _, probe := range []struct{ sessionID, workID string }{
		{"session-1", ""},
		{"", "work-1"},
	} {
		if !app.unreadTargetVisible(probe.sessionID, probe.workID) {
			t.Fatalf("normal window mode did not treat %q/%q as visible", probe.sessionID, probe.workID)
		}
	}
}

// TestDesktopRoomUnreadRespectsWidgetModeVisibility verifies the Room unread
// projection honors the widget-mode visibility gate: with widgetMode=true even
// a matching active Room keeps a new remote message unread, while
// widgetMode=false keeps the existing auto-read behavior for the visible Room.
func TestDesktopRoomUnreadRespectsWidgetModeVisibility(t *testing.T) {
	observe := func(widgetMode, matchActive bool) unread.Summary {
		store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
		if err != nil {
			t.Fatal(err)
		}
		tabs := map[string]*WorkspaceTab{}
		active := ""
		if matchActive {
			tabs["tab-1"] = &WorkspaceTab{ID: "tab-1", SessionID: "owner-session"}
			active = "tab-1"
		}
		app := &App{unreadStore: store, widgetMode: widgetMode, tabs: tabs, activeTabID: active}
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
		return app.UnreadState().Summary
	}
	if got := observe(true, true); got.TotalUnread != 1 {
		t.Fatalf("widget mode with matching active Room = %+v, want 1 pending remote message", got)
	}
	if got := observe(false, true); got.TotalUnread != 0 {
		t.Fatalf("normal window with matching active Room = %+v, want auto-read", got)
	}
	if got := observe(false, false); got.TotalUnread != 1 {
		t.Fatalf("normal window without active Room = %+v, want 1 pending", got)
	}
}

func TestResolveLegacySessionUnreadByExactBranchID(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session_f719de92b7ada7e462b8afd646331866.jsonl")
	if err := os.WriteFile(sessionPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.EnsureBranchMeta(sessionPath); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(dir, "unread.json")
	store, err := unread.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	key := "session:session_f719de92b7ada7e462b8afd646331866"
	_, err = store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key,
		EventID: "turn:1", SessionID: "session_f719de92b7ada7e462b8afd646331866",
		Title: "查看一下调用codex cli的方式", Kind: "completed", Priority: unread.PriorityNormal,
		OccurredAt: time.Date(2026, 8, 9, 3, 21, 6, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveLegacySessionUnread(key)
	if err != nil {
		t.Fatalf("ResolveLegacySessionUnread: %v", err)
	}
	if resolved.SessionPath != sessionPath {
		t.Fatalf("SessionPath = %q, want %q", resolved.SessionPath, sessionPath)
	}

	// Self-healing: the unread store should now have path: prefix.
	summary := store.Summary()
	if len(summary.Conversations) != 1 {
		t.Fatalf("expected 1 conversation, got %d", len(summary.Conversations))
	}
	if got := summary.Conversations[0].SessionID; got != "path:"+sessionPath {
		t.Fatalf("SessionID after bind = %q, want path:...", got)
	}

	// Idempotent: a stale retry after self-healing returns the same target.
	again, err := app.ResolveLegacySessionUnread(key)
	if err != nil || again.SessionPath != sessionPath {
		t.Fatalf("idempotent ResolveLegacySessionUnread = %+v, %v", again, err)
	}
}

func TestResolveLegacySessionUnreadByRuntimeUUID(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "runtime-session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.EnsureBranchMeta(sessionPath); err != nil {
		t.Fatal(err)
	}
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := "session:runtime-uuid"
	if _, err := store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key, EventID: "turn:runtime",
		SessionID: "runtime-uuid", Title: "Runtime", Kind: "completed",
		Priority: unread.PriorityNormal, OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{
		unreadStore:         store,
		sessionDirsOverride: []string{dir},
		tabs: map[string]*WorkspaceTab{
			"tab-runtime": {ID: "tab-runtime", SessionID: "runtime-uuid", SessionPath: sessionPath},
		},
	}
	resolved, err := app.ResolveLegacySessionUnread(key)
	if err != nil || resolved.SessionPath != sessionPath {
		t.Fatalf("runtime UUID resolution = %+v, %v", resolved, err)
	}
}

func TestResolveLegacySessionUnreadByTitleFallback(t *testing.T) {
	dir := t.TempDir()
	title := "查看一下调用codex cli的方式"
	sessionPath := filepath.Join(dir, "other-uuid.jsonl")
	if err := os.WriteFile(sessionPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.EnsureBranchMeta(sessionPath); err != nil {
		t.Fatal(err)
	}
	// Overwrite meta with matching title.
	meta, _, _ := agent.LoadBranchMeta(sessionPath)
	meta.TopicTitle = title
	meta.UpdatedAt = time.Date(2026, 8, 9, 3, 21, 6, 0, time.UTC)
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(agent.BranchMetaPath(sessionPath), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	storePath := filepath.Join(dir, "unread.json")
	store, err := unread.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	key := "session:session_f719de92b7ada7e462b8afd646331866"
	_, err = store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key,
		EventID: "turn:1", SessionID: "session_f719de92b7ada7e462b8afd646331866",
		Title: title, Kind: "completed", Priority: unread.PriorityNormal,
		OccurredAt: time.Date(2026, 8, 9, 3, 21, 6, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveLegacySessionUnread(key)
	if err != nil {
		t.Fatalf("ResolveLegacySessionUnread by title: %v", err)
	}
	if resolved.SessionPath != sessionPath {
		t.Fatalf("SessionPath = %q, want %q", resolved.SessionPath, sessionPath)
	}
}

func TestResolveLegacySessionUnreadAmbiguousTitle(t *testing.T) {
	dir := t.TempDir()
	title := "Same Title"
	for i, name := range []string{"a.jsonl", "b.jsonl"} {
		sp := filepath.Join(dir, name)
		if err := os.WriteFile(sp, []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := agent.EnsureBranchMeta(sp); err != nil {
			t.Fatal(err)
		}
		meta, _, _ := agent.LoadBranchMeta(sp)
		meta.TopicTitle = title
		meta.UpdatedAt = time.Date(2026, 8, 9, 3, 21, 0, 0, time.UTC).Add(time.Duration(i) * time.Minute)
		raw, _ := json.Marshal(meta)
		if err := os.WriteFile(agent.BranchMetaPath(sp), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	storePath := filepath.Join(dir, "unread.json")
	store, err := unread.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	key := "session:missing-uuid"
	_, err = store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key,
		EventID: "turn:1", SessionID: "missing-uuid",
		Title: title, Kind: "completed", Priority: unread.PriorityNormal,
		OccurredAt: time.Date(2026, 8, 9, 3, 21, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	_, err = app.ResolveLegacySessionUnread(key)
	if err == nil {
		t.Fatal("expected ambiguous-title error, got nil")
	}
}

func TestResolveLegacySessionUnreadNonSessionSource(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store}
	// Key does not start with session:, so it must fail.
	_, err = app.ResolveLegacySessionUnread("im:chat-1")
	if err == nil {
		t.Fatal("expected error for non-session key prefix, got nil")
	}
	// Empty key must also fail.
	_, err = app.ResolveLegacySessionUnread("")
	if err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
}

func TestResolveLegacySessionUnreadMissingConversation(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store}
	_, err = app.ResolveLegacySessionUnread("session:nonexistent")
	if err == nil {
		t.Fatal("expected error for missing conversation, got nil")
	}
}

func TestResolveLegacySessionUnreadDisambiguatesByTimestamp(t *testing.T) {
	dir := t.TempDir()
	title := "Timestamp Test"
	lastUnreadAt := time.Date(2026, 8, 9, 3, 21, 6, 0, time.UTC)

	// Create two sessions with same title, one close in time, one far.
	closePath := filepath.Join(dir, "close.jsonl")
	farPath := filepath.Join(dir, "far.jsonl")
	for _, sp := range []string{closePath, farPath} {
		if err := os.WriteFile(sp, []byte("[]"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := agent.EnsureBranchMeta(sp); err != nil {
			t.Fatal(err)
		}
	}
	// Close match: within 5-minute window.
	cm, _, _ := agent.LoadBranchMeta(closePath)
	cm.TopicTitle = title
	cm.UpdatedAt = lastUnreadAt.Add(2 * time.Minute) // within window
	raw, _ := json.Marshal(cm)
	os.WriteFile(agent.BranchMetaPath(closePath), raw, 0o600)

	// Far match: outside 5-minute window.
	fm, _, _ := agent.LoadBranchMeta(farPath)
	fm.TopicTitle = title
	fm.UpdatedAt = lastUnreadAt.Add(10 * time.Minute) // outside window
	raw, _ = json.Marshal(fm)
	os.WriteFile(agent.BranchMetaPath(farPath), raw, 0o600)

	storePath := filepath.Join(dir, "unread.json")
	store, err := unread.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	key := "session:disambig-uuid"
	_, err = store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key,
		EventID: "turn:1", SessionID: "disambig-uuid",
		Title: title, Kind: "completed", Priority: unread.PriorityNormal,
		OccurredAt: lastUnreadAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveLegacySessionUnread(key)
	if err != nil {
		t.Fatalf("ResolveLegacySessionUnread with timestamp disambiguation: %v", err)
	}
	if resolved.SessionPath != closePath {
		t.Fatalf("SessionPath = %q, want close path %q", resolved.SessionPath, closePath)
	}
}

// seedIMSession writes a .jsonl session (with optional meta title/topic) under
// dir and returns its absolute path.
func seedIMSession(t *testing.T, dir, name, title, topicID string) string {
	t.Helper()
	sessionPath := filepath.Join(dir, name)
	if err := os.WriteFile(sessionPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.EnsureBranchMeta(sessionPath); err != nil {
		t.Fatal(err)
	}
	if title != "" || topicID != "" {
		meta, _, _ := agent.LoadBranchMeta(sessionPath)
		meta.TopicTitle = title
		meta.TopicID = topicID
		raw, _ := json.Marshal(meta)
		if err := os.WriteFile(agent.BranchMetaPath(sessionPath), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sessionPath
}

// acceptIMUnreadForTest records one IM unread bound to sessionID and returns
// the durable "im:" conversation key.
func acceptIMUnreadForTest(t *testing.T, store *unread.Store, key, sessionID, title string) string {
	t.Helper()
	if _, err := store.AcceptIM(unread.IMInput{
		ConversationKey: key,
		MessageID:       "msg-" + key,
		SessionID:       sessionID,
		Title:           title,
		ReceivedAt:      time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	return "im:" + key
}

func TestResolveUnreadSessionIMByLivePath(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	sessionPath := seedIMSession(t, dir, "im-live.jsonl", "IM Live", "topic-im-live")
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := acceptIMUnreadForTest(t, store, "chat-live", "path:"+sessionPath, "IM Live")
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveUnreadSession(key)
	if err != nil {
		t.Fatalf("ResolveUnreadSession(im live): %v", err)
	}
	if resolved.SessionPath != sessionPath {
		t.Fatalf("SessionPath = %q, want %q", resolved.SessionPath, sessionPath)
	}
	if resolved.TopicTitle != "IM Live" {
		t.Fatalf("TopicTitle = %q, want IM Live", resolved.TopicTitle)
	}
	// Resolve alone must not clear the unread; only MarkUnreadRead does.
	if got := app.UnreadState().Summary.TotalUnread; got != 1 {
		t.Fatalf("unread should remain intact after resolve, got %d", got)
	}
	// Idempotent retry after success.
	again, err := app.ResolveUnreadSession(key)
	if err != nil || again.SessionPath != sessionPath {
		t.Fatalf("idempotent IM resolve = %+v, %v", again, err)
	}
}

func TestResolveUnreadSessionIMRestoresFromTrash(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	sessionPath := seedIMSession(t, dir, "im-trash.jsonl", "IM Trash", "topic-im-trash")
	// Simulate the external session GC: move the session into the local trash.
	if err := deleteSessionFile(dir, sessionPath); err != nil {
		t.Fatalf("trash IM session: %v", err)
	}
	if _, err := os.Stat(sessionPath); !os.IsNotExist(err) {
		t.Fatalf("live session should be gone after trash, stat err = %v", err)
	}
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := acceptIMUnreadForTest(t, store, "chat-trash", "path:"+sessionPath, "IM Trash")
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveUnreadSession(key)
	if err != nil {
		t.Fatalf("ResolveUnreadSession(im trash restore): %v", err)
	}
	if resolved.SessionPath != sessionPath {
		t.Fatalf("SessionPath = %q, want %q", resolved.SessionPath, sessionPath)
	}
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should be restored live: %v", err)
	}
	// The restore consumed the trash copy.
	trashPath := filepath.Join(sessionTrashPath(dir), filepath.Base(sessionPath), filepath.Base(sessionPath))
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Fatalf("trash copy should be consumed by restore, stat err = %v", err)
	}
	if got := app.UnreadState().Summary.TotalUnread; got != 1 {
		t.Fatalf("unread should remain intact after restore resolve, got %d", got)
	}
	// Repeated restore after completion stays idempotent.
	again, err := app.ResolveUnreadSession(key)
	if err != nil || again.SessionPath != sessionPath {
		t.Fatalf("idempotent restored IM resolve = %+v, %v", again, err)
	}
	// A caller that already entered the restore path before another caller won
	// must also observe the completed live state after it acquires the lock.
	if err := app.restoreTrashedIMSession(dir, sessionPath); err != nil {
		t.Fatalf("late idempotent restore: %v", err)
	}
}

func TestResolveUnreadSessionIMMissingTargetKeepsUnread(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "im-gone.jsonl") // never created, no trash copy
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := acceptIMUnreadForTest(t, store, "chat-gone", "path:"+sessionPath, "IM Gone")
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	if _, err := app.ResolveUnreadSession(key); err == nil {
		t.Fatal("expected explicit error for missing live+trash target, got nil")
	}
	if got := app.UnreadState().Summary.TotalUnread; got != 1 {
		t.Fatalf("unread must be preserved for retry, got %d", got)
	}
}

func TestResolveUnreadSessionIMOutsideKnownDirs(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.jsonl")
	if err := os.WriteFile(outside, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := acceptIMUnreadForTest(t, store, "chat-outside", "path:"+outside, "IM Outside")
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	if _, err := app.ResolveUnreadSession(key); err == nil {
		t.Fatal("expected explicit error for out-of-scope IM session path, got nil")
	}
	if got := app.UnreadState().Summary.TotalUnread; got != 1 {
		t.Fatalf("unread must be preserved for retry, got %d", got)
	}
}

func TestResolveUnreadSessionLegacySessionRegression(t *testing.T) {
	dir := t.TempDir()
	sessionPath := filepath.Join(dir, "session_f719de92b7ada7e462b8afd646331866.jsonl")
	if err := os.WriteFile(sessionPath, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.EnsureBranchMeta(sessionPath); err != nil {
		t.Fatal(err)
	}
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := "session:session_f719de92b7ada7e462b8afd646331866"
	if _, err := store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceSession, ConversationKey: key,
		EventID: "turn:1", SessionID: "session_f719de92b7ada7e462b8afd646331866",
		Title: "查看一下调用codex cli的方式", Kind: "completed", Priority: unread.PriorityNormal,
		OccurredAt: time.Date(2026, 8, 9, 3, 21, 6, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	resolved, err := app.ResolveUnreadSession(key)
	if err != nil || resolved.SessionPath != sessionPath {
		t.Fatalf("ResolveUnreadSession legacy session = %+v, %v", resolved, err)
	}
	// The legacy bound method keeps working unchanged.
	legacy, err := app.ResolveLegacySessionUnread(key)
	if err != nil || legacy.SessionPath != sessionPath {
		t.Fatalf("ResolveLegacySessionUnread regression = %+v, %v", legacy, err)
	}
}

func TestResolveUnreadSessionUnsupportedSource(t *testing.T) {
	store, err := unread.Open(filepath.Join(t.TempDir(), "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := "work:job-1"
	if _, err := store.AcceptAttention(unread.AttentionInput{
		Source: unread.SourceWork, ConversationKey: key,
		EventID: "input:1:waiting:1", SessionID: "work-session",
		Title: "Work", Kind: "question", Priority: unread.PriorityHigh,
		OccurredAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{unreadStore: store}
	if _, err := app.ResolveUnreadSession(key); err == nil {
		t.Fatal("expected error for unsupported work source, got nil")
	}
	if _, err := app.ResolveUnreadSession(""); err == nil {
		t.Fatal("expected error for empty key, got nil")
	}
	if _, err := app.ResolveUnreadSession("session:missing"); err == nil {
		t.Fatal("expected error for missing conversation, got nil")
	}
}

func TestResolveUnreadSessionIMConcurrentRestore(t *testing.T) {
	isolateDesktopUserDirs(t)
	dir := t.TempDir()
	sessionPath := seedIMSession(t, dir, "im-race.jsonl", "IM Race", "topic-im-race")
	if err := deleteSessionFile(dir, sessionPath); err != nil {
		t.Fatalf("trash IM session: %v", err)
	}
	store, err := unread.Open(filepath.Join(dir, "unread.json"))
	if err != nil {
		t.Fatal(err)
	}
	key := acceptIMUnreadForTest(t, store, "chat-race", "path:"+sessionPath, "IM Race")
	app := &App{unreadStore: store, sessionDirsOverride: []string{dir}}

	var wg sync.WaitGroup
	results := make([]error, 2)
	targets := make([]ResolvedSession, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			targets[i], results[i] = app.ResolveUnreadSession(key)
		}(i)
	}
	wg.Wait()
	for i := 0; i < 2; i++ {
		if results[i] != nil {
			t.Fatalf("concurrent resolve %d failed: %v", i, results[i])
		}
		if targets[i].SessionPath != sessionPath {
			t.Fatalf("concurrent resolve %d target = %q, want %q", i, targets[i].SessionPath, sessionPath)
		}
	}
	// Exactly one live session copy, no duplicates from the racing restores.
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("session should be restored live: %v", err)
	}
	trashPath := filepath.Join(sessionTrashPath(dir), filepath.Base(sessionPath), filepath.Base(sessionPath))
	if _, err := os.Stat(trashPath); !os.IsNotExist(err) {
		t.Fatalf("trash copy should be consumed exactly once, stat err = %v", err)
	}
	if got := app.UnreadState().Summary.TotalUnread; got != 1 {
		t.Fatalf("unread should remain intact after concurrent resolves, got %d", got)
	}
}
