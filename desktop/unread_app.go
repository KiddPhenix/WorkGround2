package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/bot"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/unread"
	"workground2/internal/work"
)

const unreadStateChannel = "unread:state"

type UnreadState struct {
	Available bool           `json:"available"`
	Error     string         `json:"error,omitempty"`
	Summary   unread.Summary `json:"summary"`
}

type MarkUnreadReadInput struct {
	ConversationKey string `json:"conversationKey"`
	UpToSequence    uint64 `json:"upToSequence"`
}

// UnreadState returns the durable data-layer projection consumed by the recent
// list and the native taskbar badge.
func (a *App) UnreadState() UnreadState {
	store, _ := a.currentUnreadStore()
	a.unreadMu.RLock()
	err := a.unreadErr
	a.unreadMu.RUnlock()
	state := UnreadState{Available: store != nil}
	if store != nil {
		state.Summary = store.Summary()
	}
	if err != nil {
		state.Error = err.Error()
	}
	return state
}

// MarkUnreadRead monotonically advances an actual visible watermark supplied
// by the presentation layer.
func (a *App) MarkUnreadRead(input MarkUnreadReadInput) (UnreadState, error) {
	store, err := a.currentUnreadStore()
	if err != nil {
		return a.UnreadState(), err
	}
	if store == nil {
		return a.UnreadState(), errors.New("unread store is unavailable")
	}
	before := store.Summary().Revision
	if _, err := store.MarkRead(input.ConversationKey, input.UpToSequence); err != nil {
		a.recordUnreadError(err)
		return a.UnreadState(), err
	}
	a.recordUnreadError(nil)
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
	return state, nil
}

func (a *App) currentUnreadStore() (*unread.Store, error) {
	if a == nil {
		return nil, errors.New("desktop application is unavailable")
	}
	a.unreadMu.RLock()
	defer a.unreadMu.RUnlock()
	if a.unreadStore == nil {
		if a.unreadErr != nil {
			return nil, a.unreadErr
		}
		return nil, errors.New("unread store is unavailable")
	}
	return a.unreadStore, nil
}

func (a *App) recordUnreadError(err error) {
	if a == nil {
		return
	}
	a.unreadMu.Lock()
	a.unreadErr = err
	a.unreadMu.Unlock()
}

func (a *App) acceptIMUnread(msg bot.InboundMessage) (bot.InboundAcceptance, error) {
	store, err := a.currentUnreadStore()
	if err != nil {
		return bot.InboundAcceptance{}, err
	}
	if store == nil {
		return bot.InboundAcceptance{}, errors.New("unread store is unavailable")
	}
	receipt, err := store.AcceptIM(unread.IMInput{
		ConversationKey: bot.BuildSessionKey(msg.Session()),
		MessageID:       msg.MessageID,
		Title:           imUnreadTitle(msg),
		AuthorID:        firstNonEmpty(strings.TrimSpace(msg.OperatorID), strings.TrimSpace(msg.UserID)),
		ReceivedAt:      msg.ReceivedAt,
		Read:            a.unreadConversationVisible("im:" + bot.BuildSessionKey(msg.Session())),
	})
	if err != nil {
		a.recordUnreadError(err)
		return bot.InboundAcceptance{}, err
	}
	a.recordUnreadError(nil)
	if !receipt.Duplicate {
		a.emitUnreadState(a.UnreadState())
	}
	return bot.InboundAcceptance{Duplicate: receipt.Duplicate}, nil
}

func imUnreadTitle(msg bot.InboundMessage) string {
	if msg.ChatType == bot.ChatDM || msg.ChatType == bot.ChatDirect {
		return firstNonEmpty(strings.TrimSpace(msg.UserName), strings.TrimSpace(msg.ChatID))
	}
	return firstNonEmpty(strings.TrimSpace(msg.ChatID), strings.TrimSpace(msg.UserName))
}

func (a *App) bindIMUnread(msg bot.InboundMessage, sessionID string) error {
	store, err := a.currentUnreadStore()
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("unread store is unavailable")
	}
	key := "im:" + bot.BuildSessionKey(msg.Session())
	before := store.Summary().Revision
	if err := store.BindSession(key, sessionID); err != nil {
		a.recordUnreadError(err)
		return err
	}
	a.recordUnreadError(nil)
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
	return nil
}

func (c *desktopCollaboration) observeUnread() {
	if c == nil || c.app == nil {
		return
	}
	store, err := c.app.currentUnreadStore()
	if err != nil || store == nil {
		return
	}
	c.mu.RLock()
	room := c.state.Room
	if room == "" {
		room = c.state.Snapshot.Room.ID
	}
	snapshot := cloneCollaborationState(CollaborationState{Snapshot: c.state.Snapshot}).Snapshot
	memberID, agentID := c.state.MemberID, c.state.AgentID
	sessionID := firstNonEmpty(strings.TrimSpace(c.ownerSessionID), strings.TrimSpace(c.state.SessionID))
	persistenceKey := collaborationPersistenceKey(c.ownerSessionID, c.ownerSessionPath)
	c.mu.RUnlock()
	if strings.TrimSpace(room) == "" || strings.TrimSpace(memberID) == "" {
		return
	}
	before := store.Summary().Revision
	created := snapshot.Room.CreatedAt.UTC().Format(time.RFC3339Nano)
	identity := strings.Join([]string{persistenceKey, room, created}, "\x00")
	_, err = store.ObserveRoom(unread.RoomInput{
		ConversationKey: stableCollaborationID("room_unread", identity),
		SessionID:       sessionID,
		Title:           firstNonEmpty(strings.TrimSpace(snapshot.Room.Name), strings.TrimSpace(room)),
		LocalMemberID:   memberID,
		LocalAgentID:    agentID,
		Snapshot:        snapshot,
		ObservedAt:      time.Now().UTC(),
		Read:            c.app.unreadTargetVisible(sessionID, ""),
	})
	if err != nil {
		c.app.recordUnreadError(fmt.Errorf("project Room unread: %w", err))
		return
	}
	c.app.recordUnreadError(nil)
	state := c.app.UnreadState()
	if state.Summary.Revision != before {
		c.app.emitUnreadState(state)
	}
}

type desktopUnreadAttention struct {
	id       string
	kind     string
	priority unread.Priority
	at       time.Time
}

func (a *App) unreadTargetVisible(sessionID, workID string) bool {
	if a == nil {
		return false
	}
	sessionID, workID = strings.TrimSpace(sessionID), strings.TrimSpace(workID)
	a.mu.RLock()
	tab := a.tabs[a.activeTabID]
	if tab == nil {
		a.mu.RUnlock()
		return false
	}
	tabID := strings.TrimSpace(tab.ID)
	tabSessionID := strings.TrimSpace(tab.SessionID)
	tabPath := strings.TrimSpace(tab.currentSessionPath())
	tabWorkID := strings.TrimSpace(tab.workID)
	a.mu.RUnlock()
	if workID != "" && workID == tabWorkID {
		return true
	}
	if sessionID == "" {
		return false
	}
	if sessionID == tabID || sessionID == tabSessionID {
		return true
	}
	path := strings.TrimSpace(strings.TrimPrefix(sessionID, "path:"))
	return strings.HasPrefix(strings.ToLower(sessionID), "path:") &&
		sessionRuntimeKey(path) != "" && sessionRuntimeKey(path) == sessionRuntimeKey(tabPath)
}

func (a *App) unreadConversationVisible(key string) bool {
	store, err := a.currentUnreadStore()
	if err != nil || store == nil {
		return false
	}
	for _, conversation := range store.Summary().Conversations {
		if conversation.Key == key {
			return a.unreadTargetVisible(conversation.SessionID, "")
		}
	}
	return false
}

// observeSessionUnread bridges the existing desktop attention boundary into
// the durable unified unread projection. CLI-owned sessions are intentionally
// excluded; a visible target still writes a read receipt so replay is harmless.
func (a *App) observeSessionUnread(tabID string, value event.Event) {
	if a == nil || (value.Kind != event.TurnDone && value.Kind != event.AskRequest && value.Kind != event.ApprovalRequest) {
		return
	}
	a.mu.RLock()
	tab := a.tabByEventSinkIDLocked(tabID)
	if tab == nil || tab.sessionKind == agent.SessionKindWork || tabSourceIsCLILocked(tab) {
		a.mu.RUnlock()
		return
	}
	path := strings.TrimSpace(tab.currentSessionPath())
	sessionID := ""
	if path != "" {
		sessionID = "path:" + path
	} else {
		sessionID = strings.TrimSpace(tab.SessionID)
	}
	key := firstNonEmpty(sessionID, strings.TrimSpace(tab.ID))
	title := firstNonEmpty(strings.TrimSpace(tab.TopicTitle), strings.TrimSpace(tab.Label), "Session")
	a.mu.RUnlock()
	if key == "" {
		return
	}
	now := time.Now().UTC()
	attention := desktopUnreadAttention{priority: unread.PriorityHigh, at: now}
	switch value.Kind {
	case event.AskRequest:
		attention.id = "ask:" + strings.TrimSpace(value.Ask.ID)
		attention.kind = "question"
	case event.ApprovalRequest:
		attention.id = "approval:" + strings.TrimSpace(value.Approval.ID)
		attention.kind = "approval"
	case event.TurnDone:
		started := tab.activeTurnStartedAt()
		if started == 0 {
			started = now.UnixNano()
		}
		attention.id = fmt.Sprintf("turn:%d", started)
		attention.kind = "completed"
		attention.priority = unread.PriorityNormal
		if value.Err != nil {
			attention.kind = "failed"
			attention.priority = unread.PriorityHigh
		}
	}
	if strings.HasSuffix(attention.id, ":") {
		return
	}
	a.recordUnreadAttention(unread.SourceSession, key, sessionID, title, a.unreadTargetVisible(sessionID, ""), []desktopUnreadAttention{attention})
}

func (a *App) bindWorkUnreadObserver(ctrl control.SessionAPI) {
	if a == nil || ctrl == nil {
		return
	}
	owner, ok := ctrl.(workController)
	if !ok || owner.WorkViews() == nil {
		return
	}
	owner.WorkViews().SetObserver(a.observeWorkUnread)
}

func (a *App) observeWorkUnread(value work.WorkViewEvent) {
	title, attention := workUnreadAttention(value)
	if len(attention) == 0 {
		return
	}
	sessionID, fallbackTitle := a.workUnreadBinding(value.WorkID)
	a.recordUnreadAttention(
		unread.SourceWork,
		value.WorkID,
		sessionID,
		firstNonEmpty(title, fallbackTitle, "Work"),
		a.unreadTargetVisible(sessionID, value.WorkID),
		attention,
	)
}

func (a *App) workUnreadBinding(workID string) (sessionID, title string) {
	workID = strings.TrimSpace(workID)
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, collection := range []map[string]*WorkspaceTab{a.tabs, a.detachedSessions} {
		for _, tab := range collection {
			if tab == nil || strings.TrimSpace(tab.workID) != workID {
				continue
			}
			sessionID = strings.TrimSpace(tab.SessionID)
			if sessionID == "" {
				if path := strings.TrimSpace(tab.currentSessionPath()); path != "" {
					sessionID = "path:" + path
				}
			}
			return sessionID, firstNonEmpty(strings.TrimSpace(tab.TopicTitle), strings.TrimSpace(tab.Label))
		}
	}
	return "", ""
}

func (a *App) recordUnreadAttention(source unread.Source, key, sessionID, title string, read bool, attention []desktopUnreadAttention) {
	store, err := a.currentUnreadStore()
	if err != nil || store == nil {
		if err != nil {
			a.recordUnreadError(err)
		}
		return
	}
	before := store.Summary().Revision
	for _, item := range attention {
		if _, err := store.AcceptAttention(unread.AttentionInput{
			Source: source, ConversationKey: key, EventID: item.id, SessionID: sessionID,
			Title: title, Kind: item.kind, Priority: item.priority, OccurredAt: item.at, Read: read,
		}); err != nil {
			a.recordUnreadError(err)
			return
		}
	}
	a.recordUnreadError(nil)
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
}

func workUnreadAttention(value work.WorkViewEvent) (string, []desktopUnreadAttention) {
	if value.Type == work.ViewAttention {
		var payload struct {
			Planning struct {
				Kind  string `json:"kind"`
				State string `json:"state"`
			} `json:"planning"`
		}
		if json.Unmarshal(value.Payload, &payload) == nil &&
			(payload.Planning.State == "waiting" || payload.Planning.Kind == "clarification") {
			return "", []desktopUnreadAttention{{id: value.EventID, kind: "question", priority: unread.PriorityHigh, at: value.CreatedAt}}
		}
		return "", nil
	}
	if value.Type != work.ViewSnapshot {
		return "", nil
	}
	var view work.WorkView
	if json.Unmarshal(value.Payload, &view) != nil || view.Work == nil {
		return "", nil
	}
	now := value.CreatedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	items := make([]desktopUnreadAttention, 0, 4)
	if view.Work.State == work.WorkCompleted {
		id := "work:" + view.Work.ID + ":completed"
		for i := len(view.Work.Runs) - 1; i >= 0; i-- {
			if view.Work.Runs[i].State == work.RunCompleted && strings.TrimSpace(view.Work.Runs[i].ID) != "" {
				id = "run:" + view.Work.Runs[i].ID + ":completed"
				break
			}
		}
		items = append(items, desktopUnreadAttention{id: id, kind: "completed", priority: unread.PriorityNormal, at: now})
	}
	for _, input := range view.Inputs {
		if input.State != work.InputRequested && input.State != work.InputRejected {
			continue
		}
		id := fmt.Sprintf("input:%s:%s:%d", input.ID, input.State, input.Revision)
		items = append(items, desktopUnreadAttention{id: id, kind: "question", priority: unread.PriorityHigh, at: input.UpdatedAt})
	}
	for _, task := range view.Tasks {
		if task.State != work.TaskWaitingInput && task.State != work.TaskWaitingApproval {
			continue
		}
		if task.State == work.TaskWaitingInput && len(task.WaitingInputIDs) > 0 {
			continue
		}
		version := task.UpdatedAt.UnixNano()
		if task.UpdatedAt.IsZero() {
			version = value.Revision
		}
		id := fmt.Sprintf("task:%s:%s:%s:%d", task.RunID, task.ID, task.State, version)
		items = append(items, desktopUnreadAttention{id: id, kind: "question", priority: unread.PriorityHigh, at: task.UpdatedAt})
	}
	if view.Work.State == work.WorkWaitingUser && len(items) == 0 {
		id := fmt.Sprintf("work:%s:waiting:%d", view.Work.ID, value.Revision)
		items = append(items, desktopUnreadAttention{id: id, kind: "question", priority: unread.PriorityHigh, at: now})
	}
	return strings.TrimSpace(view.Work.Name), items
}

func (a *App) emitUnreadState(state UnreadState) {
	if a == nil {
		return
	}
	a.scheduleUnreadBadge(state.Summary.TotalUnread)
	if a.ctx == nil {
		return
	}
	a.runtimeEvents.Emit(a.ctx, unreadStateChannel, state)
}

// ResolvedSession carries the navigation target resolved from a legacy unread
// conversation key. It intentionally omits a runtime hint so the caller stays
// on the standard open-topic path.
type ResolvedSession struct {
	Scope         string `json:"scope"`
	WorkspaceRoot string `json:"workspaceRoot"`
	TopicID       string `json:"topicId"`
	SessionPath   string `json:"sessionPath"`
	TopicTitle    string `json:"topicTitle"`
}

// ResolveLegacySessionUnread attempts to resolve a SESSION unread conversation
// key that uses an old UUID-based SessionID into a concrete session file path.
// On success it self-heals the unread store via BindSession so the record
// survives restarts. It only accepts keys with the "session:" prefix; any other
// key returns an error immediately.
func (a *App) ResolveLegacySessionUnread(conversationKey string) (ResolvedSession, error) {
	key := strings.TrimSpace(conversationKey)
	if key == "" {
		return ResolvedSession{}, errors.New("conversation key is required")
	}
	if !strings.HasPrefix(key, "session:") {
		return ResolvedSession{}, fmt.Errorf("conversation key %q is not a session unread", key)
	}
	store, err := a.currentUnreadStore()
	if err != nil {
		return ResolvedSession{}, fmt.Errorf("unread store unavailable: %w", err)
	}
	if store == nil {
		return ResolvedSession{}, errors.New("unread store unavailable")
	}
	// Find the conversation to access its SessionID and metadata.
	summary := store.Summary()
	var conv unread.Conversation
	found := false
	for _, c := range summary.Conversations {
		if c.Key == key {
			conv = c
			found = true
			break
		}
	}
	if !found {
		return ResolvedSession{}, fmt.Errorf("unread conversation %q not found", key)
	}
	// Only SESSION source unreads can be resolved this way.
	if conv.Source != unread.SourceSession {
		return ResolvedSession{}, fmt.Errorf("unread conversation %q has source %q, only session is supported", key, conv.Source)
	}
	sessionID := strings.TrimSpace(conv.SessionID)
	if sessionID == "" {
		return ResolvedSession{}, errors.New("legacy session unread has no session ID")
	}
	dirs := a.knownSessionDirs()
	resolvedPath := ""
	if strings.HasPrefix(strings.ToLower(sessionID), "path:") {
		resolvedPath = strings.TrimSpace(sessionID[len("path:"):])
	} else {
		resolvedPath = a.runtimeSessionPath(sessionID)
	}
	if resolvedPath == "" {
		resolvedPath, err = resolveSessionByID(dirs, sessionID, conv.Title, conv.LastUnreadAt)
	}
	if err != nil {
		return ResolvedSession{}, err
	}
	if !sessionPathInDirs(dirs, resolvedPath) {
		return ResolvedSession{}, fmt.Errorf("resolved session path %q is outside known session directories", resolvedPath)
	}
	// Load meta for scope/workspace/topic.
	meta, ok, err := agent.LoadBranchMeta(resolvedPath)
	if err != nil || !ok {
		return ResolvedSession{}, fmt.Errorf("cannot load session meta for %q: %w", resolvedPath, err)
	}
	scope := meta.DefaultScope()
	workspaceRoot := meta.WorkspaceRoot
	topicID := meta.TopicID
	topicTitle := firstNonEmpty(meta.TopicTitle, meta.CustomTitle, conv.Title)
	// Self-heal: bind the resolved path back to the unread store. BindSession is
	// idempotent, so a stale double-click or retry remains safe.
	newSessionID := "path:" + resolvedPath
	before := store.Summary().Revision
	if err := store.BindSession(key, newSessionID); err != nil {
		return ResolvedSession{}, fmt.Errorf("bind resolved session path: %w", err)
	}
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
	return ResolvedSession{
		Scope:         scope,
		WorkspaceRoot: workspaceRoot,
		TopicID:       topicID,
		SessionPath:   resolvedPath,
		TopicTitle:    topicTitle,
	}, nil
}

func (a *App) runtimeSessionPath(sessionID string) string {
	if a == nil || strings.TrimSpace(sessionID) == "" {
		return ""
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tabs := range []map[string]*WorkspaceTab{a.tabs, a.detachedSessions} {
		for _, tab := range tabs {
			if tab != nil && strings.TrimSpace(tab.SessionID) == sessionID {
				return strings.TrimSpace(tab.currentSessionPath())
			}
		}
	}
	return ""
}

func sessionPathInDirs(dirs []string, sessionPath string) bool {
	target := sessionRuntimeKey(filepath.Dir(strings.TrimSpace(sessionPath)))
	if target == "" {
		return false
	}
	for _, dir := range dirs {
		if sessionRuntimeKey(dir) == target {
			return true
		}
	}
	return false
}

// resolveSessionByID searches known session dirs for a .jsonl file whose stem
// matches sessionID. If no exact match is found it falls back to
// scanning .jsonl.meta files and matching by title + LastUnreadAt proximity.
func resolveSessionByID(dirs []string, sessionID, title string, lastUnreadAt time.Time) (string, error) {
	// Step 1: try a direct filename-stem match without allowing traversal.
	if filepath.Base(sessionID) == sessionID && !strings.ContainsAny(sessionID, `/\\`) {
		for _, dir := range dirs {
			candidate := filepath.Join(dir, sessionID+".jsonl")
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
		}
	}
	// Step 2: fallback scan by title + timestamp proximity.
	if title == "" {
		return "", fmt.Errorf("session %q not found and no title to fallback match", sessionID)
	}
	return resolveSessionByTitle(dirs, title, lastUnreadAt)
}

// resolveSessionByTitle scans .jsonl.meta files across known session dirs,
// matching by topic_title. If exactly one candidate matches and its
// UpdatedAt is within a reasonable window of lastUnreadAt, it is returned.
// Zero or multiple matches produce an explicit error.
func resolveSessionByTitle(dirs []string, title string, lastUnreadAt time.Time) (string, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", errors.New("title is required for fallback session resolution")
	}
	type candidate struct {
		path      string
		updatedAt time.Time
	}
	var candidates []candidate
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".jsonl.meta") {
				continue
			}
			sessionPath := filepath.Join(dir, strings.TrimSuffix(name, ".meta"))
			if _, err := os.Stat(sessionPath); err != nil {
				continue
			}
			meta, ok, err := agent.LoadBranchMeta(sessionPath)
			if err != nil || !ok {
				continue
			}
			metaTitle := firstNonEmpty(meta.TopicTitle, meta.CustomTitle)
			if metaTitle != title {
				continue
			}
			candidates = append(candidates, candidate{path: sessionPath, updatedAt: meta.UpdatedAt})
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no session found with title %q", title)
	}
	if len(candidates) > 1 {
		// Try to disambiguate by timestamp proximity when lastUnreadAt is set.
		if !lastUnreadAt.IsZero() {
			var close []candidate
			const window = 5 * time.Minute
			for _, c := range candidates {
				diff := c.updatedAt.Sub(lastUnreadAt)
				if diff < 0 {
					diff = -diff
				}
				if diff <= window {
					close = append(close, c)
				}
			}
			if len(close) == 1 {
				return close[0].path, nil
			}
		}
		return "", fmt.Errorf("multiple sessions with title %q found (%d candidates); cannot disambiguate", title, len(candidates))
	}
	return candidates[0].path, nil
}
