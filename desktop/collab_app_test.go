package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/fileutil"
)

type semanticIntentSession struct {
	control.SessionAPI
	intent agent.SemanticIntent
	err    error
	calls  int
}

func (s *semanticIntentSession) ClassifySemanticIntent(context.Context, string) (agent.SemanticIntent, error) {
	s.calls++
	return s.intent, s.err
}

type fakeCollaborationPeer struct {
	mu           sync.Mutex
	snapshot     collab.Snapshot
	submitted    []collab.CommandEnvelope
	failSubmit   int
	failNonRetry int
	leaveErr     error
	leaveCount   int
	heartbeats   int
}

func (p *fakeCollaborationPeer) Snapshot(context.Context) (collab.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneCollaborationState(CollaborationState{Snapshot: p.snapshot}).Snapshot, nil
}

func (p *fakeCollaborationPeer) Events(context.Context, uint64) ([]collab.RoomEvent, error) {
	return []collab.RoomEvent{}, nil
}

func (p *fakeCollaborationPeer) Stream(ctx context.Context, _ uint64, _ func(collab.RoomEvent) error) error {
	<-ctx.Done()
	return ctx.Err()
}

func (p *fakeCollaborationPeer) Submit(_ context.Context, env collab.CommandEnvelope) (collab.CommandReceipt, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.submitted = append(p.submitted, env)
	if p.failSubmit > 0 {
		p.failSubmit--
		return collab.CommandReceipt{}, &collaborationTransportError{message: "temporary", retryable: true}
	}
	if p.failNonRetry > 0 {
		p.failNonRetry--
		return collab.CommandReceipt{}, &collab.Error{Code: collab.CodeForbidden, Message: "denied", Retryable: false}
	}
	return collab.CommandReceipt{RequestID: env.RequestID, LatestSequence: p.snapshot.LatestSequence + 1}, nil
}

func (p *fakeCollaborationPeer) Heartbeat(context.Context, string) error {
	p.mu.Lock()
	p.heartbeats++
	p.mu.Unlock()
	return nil
}

func (p *fakeCollaborationPeer) Leave(context.Context, string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.leaveCount++
	return p.leaveErr
}

func newTestDesktopCollaboration(t *testing.T) (*App, *desktopCollaboration, map[string]string) {
	t.Helper()
	app := &App{}
	secrets := map[string]string{}
	c := &desktopCollaboration{
		app: app, ownerSessionID: "session-a", state: CollaborationState{Status: "disconnected", SessionID: "session-a"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
		persistPath:    filepath.Join(t.TempDir(), "collaboration.json"), writeState: fileutil.AtomicWriteFile,
		validateAgent: func(string) error { return nil },
	}
	c.setSecret = func(key, value string) error { secrets[key] = value; return nil }
	c.getSecret = func(key string) string { return secrets[key] }
	c.removeSecret = func(key string) error { delete(secrets, key); return nil }
	app.collaborations = map[string]*desktopCollaboration{"session-a": c}
	return app, c, secrets
}

func TestClassifyCollaborationIntentUsesOwningSessionModel(t *testing.T) {
	ctrl := &semanticIntentSession{intent: agent.SemanticIntentUncertain}
	app := &App{}
	app.trackSession(&WorkspaceTab{SessionID: "session-a", Ctrl: ctrl, Ready: true})

	got := app.ClassifyCollaborationIntent(ClassifyCollaborationIntentInput{
		SessionID: "session-a",
		Text:      "现在多人协作 room，在 session 里会有一个外部标签",
	})
	if got.Intent != "uncertain" || got.Source != "llm" || got.Error != "" || ctrl.calls != 1 {
		t.Fatalf("classification = %+v, calls = %d", got, ctrl.calls)
	}

	ctrl.err = context.DeadlineExceeded
	failed := app.ClassifyCollaborationIntent(ClassifyCollaborationIntentInput{SessionID: "session-a", Text: "uncovered"})
	if failed.Intent != "chat" || failed.Source != "fallback" || failed.Error == "" || !failed.Retryable {
		t.Fatalf("failed classification should be visible and retryable: %+v", failed)
	}
}

func TestCollaborationRuntimesAreIsolatedPerSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := &App{}
	first, err := app.collaborationRuntime("session-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.collaborationRuntime("session-b")
	if err != nil {
		t.Fatal(err)
	}
	firstAgain, err := app.collaborationRuntime("session-a")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || firstAgain != first || first.persistPath == second.persistPath {
		t.Fatalf("runtimes are not isolated: first=%p second=%p paths=%q/%q", first, second, first.persistPath, second.persistPath)
	}
	first.mu.Lock()
	first.state.Room = "room-a"
	first.state.Snapshot.Timeline = []collab.TimelineItem{{ID: "a-1", Sequence: 1, Type: collab.TimelineChat}}
	first.mu.Unlock()
	if state := second.snapshot(); state.SessionID != "session-b" || state.Room != "" || len(state.Snapshot.Timeline) != 0 {
		t.Fatalf("second runtime observed first runtime state: %+v", state)
	}
	if _, err := app.collaborationRuntime(" "); err == nil {
		t.Fatal("empty SessionID created a runtime")
	}
	app.closeCollaborations()
}

func testConnection(peer collaborationPeer, mode, sessionID string) *collaborationConnection {
	snapshot := collab.Snapshot{
		Room: collab.Room{ID: "room-a", Name: "Room A", LatestSequence: 2}, LatestSequence: 2,
		Members: []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent"}}},
	}
	if fake, ok := peer.(*fakeCollaborationPeer); ok {
		fake.snapshot = snapshot
	}
	return &collaborationConnection{
		peer: peer, mode: mode, hostName: "127.0.0.1", port: 39170, room: "room-a",
		memberID: "member-a", agentID: "agent-a", sessionID: sessionID,
		memberName: "Alice", agentName: "Alice Agent",
		connectionSession: "cs1.test-session-secret", initialSnapshot: snapshot,
	}
}

func TestCollaborationHostJoinLeaveLifecycle(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	var hosted, joined bool
	c.openHost = func(_ context.Context, input HostCollaborationRoomInput, member collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		hosted = input.Room == "room-a" && member.Agent.ID != "" && input.SessionID == "session-a"
		return testConnection(peer, "host", input.SessionID), nil
	}
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, member collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		joined = input.Host == "10.0.0.8" && input.Port == 39170 && member.Name == "Alice"
		return testConnection(peer, "client", input.SessionID), nil
	}
	state, err := c.host(context.Background(), HostCollaborationRoomInput{ListenHost: "127.0.0.1", Port: 39170, Room: "room-a", MemberName: "Alice", SessionID: "session-a"})
	if err != nil || !hosted || state.Status != "connected" || state.Mode != "host" {
		t.Fatalf("host state=%+v hosted=%v err=%v", state, hosted, err)
	}
	state, err = c.join(context.Background(), JoinCollaborationRoomInput{Host: "10.0.0.8", Port: 39170, Room: "room-a", MemberName: "Alice", SessionID: "session-a"})
	if err != nil || !joined || state.Mode != "client" {
		t.Fatalf("join state=%+v joined=%v err=%v", state, joined, err)
	}
	if err := c.leave(context.Background()); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if got := c.snapshot().Status; got != "disconnected" {
		t.Fatalf("status after leave = %q", got)
	}
}

func TestCollaborationHostUsesInProcessAuthoritativePeer(t *testing.T) {
	isolateDesktopUserDirs(t)
	_, c, _ := newTestDesktopCollaboration(t)
	identity := collab.MemberDescriptor{ID: "member-host", Name: "Host", Agent: collab.AgentDescriptor{ID: "agent-host", Name: "Host Agent", Status: collab.AgentIdle}}
	conn, err := c.openHostedRoom(context.Background(), HostCollaborationRoomInput{ListenHost: "127.0.0.1", Port: 0, Room: "host-room", RoomName: "Host Room", SessionID: "session-a"}, identity, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := conn.peer.(*serviceCollaborationPeer); !ok {
		t.Fatalf("host peer = %T, want in-process service peer", conn.peer)
	}
	if err := conn.host.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn.host = nil
	receipt, err := conn.peer.Submit(context.Background(), collab.CommandEnvelope{
		RequestID: "host-chat", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "still available"}},
	})
	if err != nil || receipt.LatestSequence == 0 {
		t.Fatalf("local Host submit after HTTP shutdown = %+v, %v", receipt, err)
	}
	snapshot, err := conn.peer.Snapshot(context.Background())
	if err != nil || len(snapshot.Timeline) == 0 || snapshot.Timeline[len(snapshot.Timeline)-1].Chat == nil || snapshot.Timeline[len(snapshot.Timeline)-1].Chat.Text != "still available" {
		t.Fatalf("local Host snapshot after HTTP shutdown = %+v, %v", snapshot, err)
	}
}

func TestCollaborationExplicitSessionRoutingAndStartIdempotency(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	conn.initialSnapshot.Timeline = []collab.TimelineItem{
		{ID: "m2", Sequence: 2, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "m2", AuthorID: "member-b", Text: "第二条", Revision: 4}},
		{ID: "m1", Sequence: 1, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "m1", AuthorID: "member-a", Text: "第一条", Revision: 2}},
	}
	conn.initialSnapshot.Members = append(conn.initialSnapshot.Members, collab.Member{ID: "member-b", Name: "Bob"})
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	var routedSession, routedInput string
	var submits int
	c.submitAgent = func(sessionID, _, input string) error {
		submits++
		routedSession, routedInput = sessionID, input
		return nil
	}
	if _, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "start-1", SessionID: "session-b", Instruction: "验证", ReferenceIDs: []string{"m2", "m1"}}); err == nil {
		t.Fatal("accepted a different SessionID")
	}
	input := StartCollaborationAgentInput{RequestID: "start-1", SessionID: "session-a", Instruction: "验证", ReferenceIDs: []string{"m2", "m1"}}
	first, err := c.startAgent(context.Background(), input)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	second, err := c.startAgent(context.Background(), input)
	if err != nil || !second.Duplicate || second.RunID != first.RunID || submits != 1 {
		t.Fatalf("duplicate=%+v submits=%d err=%v", second, submits, err)
	}
	if routedSession != "session-a" || strings.Index(routedInput, "第一条") > strings.Index(routedInput, "第二条") || !strings.Contains(routedInput, "author=Alice revision=2") {
		t.Fatalf("explicit route/context mismatch: session=%q input=%q", routedSession, routedInput)
	}
	busy, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "start-2", SessionID: "session-a", Instruction: "另一个任务"})
	if err != nil || busy.Code != "agent_busy" || !busy.Retryable || busy.Error == "" || submits != 1 {
		t.Fatalf("busy result=%+v submits=%d err=%v", busy, submits, err)
	}
	changed := input
	changed.Instruction = "不同指令"
	if _, err := c.startAgent(context.Background(), changed); err == nil {
		t.Fatal("same requestId with a different fingerprint was accepted")
	}
}

func TestCollaborationAgentWaitsForStartingWorkspaceAndRunsOnce(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "host", "session-a")
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID,
		SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	c.agentReady = func(string) (bool, error) { return false, nil }
	workspaceReady := make(chan struct{})
	c.waitAgentReady = func(ctx context.Context, sessionID string) error {
		if sessionID != "session-a" {
			t.Fatalf("wait routed to %q", sessionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-workspaceReady:
			return nil
		}
	}
	submitted := make(chan string, 2)
	c.submitAgent = func(sessionID, _, input string) error {
		if sessionID != "session-a" {
			t.Fatalf("Agent routed to %q", sessionID)
		}
		submitted <- input
		return nil
	}

	input := StartCollaborationAgentInput{RequestID: "startup-agent", SessionID: "session-a", Instruction: "提交现有修改"}
	result, err := c.startAgent(context.Background(), input)
	if err != nil || !result.Queued || result.RunID == "" {
		t.Fatalf("starting workspace result=%+v err=%v", result, err)
	}
	select {
	case value := <-submitted:
		t.Fatalf("Agent started before workspace was ready: %q", value)
	default:
	}
	close(workspaceReady)
	select {
	case value := <-submitted:
		if value != input.Instruction {
			t.Fatalf("submitted input=%q", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("queued Agent did not start when workspace became ready")
	}
	duplicate, err := c.startAgent(context.Background(), input)
	if err != nil || !duplicate.Duplicate || duplicate.RunID != result.RunID {
		t.Fatalf("duplicate result=%+v err=%v", duplicate, err)
	}
	select {
	case value := <-submitted:
		t.Fatalf("duplicate Agent start submitted again: %q", value)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCollaborationRequestAcceptControlsOnlyLocalAgentAndIsIdempotent(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	request := &collab.AgentRequest{ID: "request-b", AuthorID: "member-b", TargetMemberID: "member-a", Instruction: "修复接口", Status: collab.RequestPending}
	conn.initialSnapshot.Timeline = []collab.TimelineItem{{ID: request.ID, Sequence: 3, Type: collab.TimelineAgentRequest, AgentRequest: request}}
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	localSubmits := 0
	c.submitAgent = func(sessionID, _, _ string) error {
		if sessionID != "session-a" {
			t.Fatalf("routed to %q", sessionID)
		}
		localSubmits++
		return nil
	}
	for _, requestID := range []string{"decision-1", "decision-2"} {
		if _, err := c.respond(context.Background(), RespondCollaborationRequestInput{RequestID: requestID, AgentRequestID: request.ID, Action: "accept", SessionID: "session-a"}); err != nil {
			t.Fatalf("respond %s: %v", requestID, err)
		}
	}
	if localSubmits != 1 {
		t.Fatalf("local Agent submits = %d, want 1", localSubmits)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	for _, env := range peer.submitted {
		if env.Command.Type == collab.CommandCreateAgentRequest {
			t.Fatal("accept unexpectedly created a remote Agent command")
		}
	}
}

func TestCollaborationOutboxRetryStripsBearer(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{failSubmit: 1}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	result, err := c.post(context.Background(), PostCollaborationMessageInput{RequestID: "chat-1", Kind: "chat", Text: "hello"})
	if err != nil || !result.Queued || len(c.outbox) != 1 || c.outbox[0].Session != "" {
		t.Fatalf("queued=%+v outbox=%+v err=%v", result, c.outbox, err)
	}
	data, err := os.ReadFile(c.persistPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), conn.connectionSession) {
		t.Fatal("persisted state leaked connectionSession")
	}
	if !c.drainOutbox(context.Background(), conn) || len(c.outbox) != 0 {
		t.Fatalf("outbox was not retried: %+v", c.outbox)
	}
}

func TestCollaborationRestartRecoveryKeepsSecretReferenceAndCursor(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	conn.joinToken = "room-token-secret"
	conn.initialSnapshot.Timeline = []collab.TimelineItem{{
		ID: "cached-chat", Sequence: 2, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "cached-chat", AuthorID: "member-a", Text: "cached background", Revision: 1},
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.mu.Lock()
	c.persistLocked()
	c.mu.Unlock()
	c.close()
	data, err := os.ReadFile(c.persistPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), conn.connectionSession) || strings.Contains(string(data), conn.joinToken) || !strings.Contains(string(data), "connectionSecretRef") || !strings.Contains(string(data), "joinTokenSecretRef") {
		t.Fatalf("unsafe or missing persisted secret reference: %s", data)
	}
	c2 := &desktopCollaboration{state: CollaborationState{Status: "disconnected"}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, persistPath: c.persistPath}
	c2.getSecret = func(key string) string { return secrets[key] }
	c2.loadPersisted()
	state := c2.snapshot()
	if state.Host != conn.hostName || state.Room != conn.room || state.SessionID != conn.sessionID || state.Snapshot.LatestSequence != conn.initialSnapshot.LatestSequence ||
		len(state.Snapshot.Members) != 1 || len(state.Snapshot.Timeline) != 1 || state.Snapshot.Timeline[0].Chat.Text != "cached background" {
		t.Fatalf("recovered state = %+v", state)
	}
	if got := c2.resumeSession(conn.hostName, conn.port, conn.room, conn.memberID, conn.sessionID); got != conn.connectionSession {
		t.Fatalf("resume session = %q", got)
	}
}

func TestCollaborationRetryRepairsLegacyHostIdentityFromSnapshot(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	connectionRef := collaborationSecretRef("127.0.0.1", 39170, "room-a", "member-a")
	secrets[connectionRef] = "cs1.recover-me"
	persisted := collaborationPersistedState{
		Mode: "host", Host: "127.0.0.1", Port: 39170, Room: "room-a", MemberID: "member-a", AgentID: "agent-a",
		Snapshot: collab.Snapshot{
			Room:    collab.Room{ID: "room-a", Name: "Room A", Description: "cached"},
			Members: []collab.Member{{ID: "member-a", Name: "Alice", Role: "Backend", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent", Role: "Coder"}}},
		},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadPersisted()
	var got HostCollaborationRoomInput
	var gotResume string
	c.openHost = func(_ context.Context, input HostCollaborationRoomInput, _ collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		got, gotResume = input, resume
		return testConnection(&fakeCollaborationPeer{}, "host", input.SessionID), nil
	}
	state, err := c.retry(context.Background())
	if err != nil || state.Status != "connected" {
		t.Fatalf("retry repaired Host state=%+v err=%v", state, err)
	}
	defer c.close()
	if got.MemberName != "Alice" || got.MemberRole != "Backend" || got.AgentName != "Alice Agent" || got.AgentRole != "Coder" || got.SessionID != "session-a" || gotResume != "cs1.recover-me" {
		t.Fatalf("repaired Host input=%+v resume=%q", got, gotResume)
	}
}

func TestCollaborationOfflinePersistKeepsRecoveryIdentity(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "host", "session-a")
	conn.memberRole, conn.agentRole = "Backend", "Coder"
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "host", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.mu.Lock()
	c.persistLocked()
	c.conn = nil
	c.state.Status = "failed"
	c.mu.Unlock()
	if _, err := c.post(context.Background(), PostCollaborationMessageInput{RequestID: "offline-after-restart", SessionID: "session-a", Kind: "chat", Text: "commit changes"}); err != nil {
		t.Fatal(err)
	}
	persisted := c.readPersisted()
	if persisted.MemberName != conn.memberName || persisted.MemberRole != conn.memberRole || persisted.AgentName != conn.agentName || persisted.AgentRole != conn.agentRole || persisted.SessionID != conn.sessionID || persisted.ConnectionSecretRef == "" {
		t.Fatalf("offline persist erased recovery identity: %+v", persisted)
	}
}

func TestCollaborationOfflineCacheSupportsAgentAndIdempotentOutbox(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.state = CollaborationState{
		Status: "failed", Host: "10.0.0.8", Port: 39170, Room: "room-a",
		MemberID: "member-a", AgentID: "agent-a", SessionID: "session-a",
		Snapshot: collab.Snapshot{
			Room: collab.Room{ID: "room-a", Name: "Cached Room", LatestSequence: 7}, LatestSequence: 7,
			Members: []collab.Member{{ID: "member-a", Name: "Alice"}, {ID: "member-b", Name: "Bob"}},
			Timeline: []collab.TimelineItem{{
				ID: "remote-7", Sequence: 7, Type: collab.TimelineChat,
				Chat: &collab.ChatMessage{ID: "remote-7", AuthorID: "member-b", Text: "please inspect cached API notes", Revision: 1},
			}},
		},
	}
	var submittedInput string
	c.submitAgent = func(sessionID, _, input string) error {
		if sessionID != "session-a" {
			t.Fatalf("Agent routed to %q", sessionID)
		}
		submittedInput = input
		return nil
	}
	post := PostCollaborationMessageInput{RequestID: "offline-chat", SessionID: "session-a", Kind: "chat", Text: "local update"}
	first, err := c.post(context.Background(), post)
	if err != nil || !first.Queued || first.Duplicate || first.Item == nil || first.Item.ID != "outbox:offline-chat" || first.Item.Chat == nil || first.Item.Chat.Text != "local update" {
		t.Fatalf("first offline post=%+v err=%v", first, err)
	}
	state := c.snapshot()
	if len(state.Outbox) != 1 || state.Outbox[0].Item == nil || state.Outbox[0].Item.ID != first.Item.ID || state.Outbox[0].Status != "pending" {
		t.Fatalf("offline post is not projected from persisted Outbox: %+v", state.Outbox)
	}
	second, err := c.post(context.Background(), post)
	if err != nil || !second.Queued || !second.Duplicate || len(c.outbox) != 1 {
		t.Fatalf("duplicate offline post=%+v outbox=%+v err=%v", second, c.outbox, err)
	}
	result, err := c.startAgent(context.Background(), StartCollaborationAgentInput{
		RequestID: "offline-agent", SessionID: "session-a", Instruction: "检查并给出修复建议", ReferenceIDs: []string{"remote-7"},
	})
	if err != nil || !result.Queued || result.RunID == "" || !strings.Contains(submittedInput, "please inspect cached API notes") {
		t.Fatalf("offline Agent result=%+v input=%q err=%v", result, submittedInput, err)
	}
	if len(c.outbox) != 3 {
		t.Fatalf("offline chat and Agent state were not queued independently: %+v", c.outbox)
	}
	c.observeAgentEvent("session-a", event.Event{Kind: event.Message, Text: "cached analysis complete"})
	c.observeAgentEvent("session-a", event.Event{Kind: event.TurnDone})
	deadline := time.Now().Add(2 * time.Second)
	for {
		c.mu.RLock()
		runDone := c.runs["session-a"] == nil
		outboxCount := len(c.outbox)
		c.mu.RUnlock()
		if runDone && outboxCount >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("offline Agent completion was not queued: runDone=%v outbox=%d", runDone, outboxCount)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCollaborationPersistenceAlwaysStripsOutboxSession(t *testing.T) {
	_, runtime, _ := newTestDesktopCollaboration(t)
	runtime.mu.Lock()
	runtime.state = CollaborationState{Status: "reconnecting", Mode: "client", Host: "127.0.0.1", Port: 9911, Room: "room-a", MemberID: "member-a", AgentID: "agent-a", SessionID: "tab-a"}
	runtime.outbox = []collab.CommandEnvelope{{
		RequestID: "post-1", Room: "room-a", MemberID: "member-a", Session: "must-not-be-persisted",
		Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "hello"}},
	}}
	runtime.persistLocked()
	runtime.mu.Unlock()

	data, err := os.ReadFile(runtime.persistPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("must-not-be-persisted")) {
		t.Fatalf("persisted state leaked the outbox connection credential: %s", data)
	}
}

func TestCollaborationRetryRestoresPersistedClient(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	conn.joinToken = "room-token-secret"
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.mu.Lock()
	c.persistLocked()
	c.mu.Unlock()
	c.close()

	app := &App{}
	c2 := &desktopCollaboration{
		app: app, state: CollaborationState{Status: "disconnected"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, outboxFailures: map[string]string{},
		persistPath: c.persistPath, writeState: fileutil.AtomicWriteFile,
		validateAgent: func(sessionID string) error {
			if sessionID != "session-a" {
				t.Fatalf("validated %q", sessionID)
			}
			return nil
		},
	}
	c2.setSecret = func(key, value string) error { secrets[key] = value; return nil }
	c2.getSecret = func(key string) string { return secrets[key] }
	c2.removeSecret = func(key string) error { delete(secrets, key); return nil }
	c2.loadPersisted()
	var gotToken, gotResume string
	c2.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		gotToken, gotResume = input.Token, resume
		restored := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		restored.joinToken = input.Token
		return restored, nil
	}
	state, err := c2.retry(context.Background())
	if err != nil || state.Status != "connected" || gotToken != conn.joinToken || gotResume != conn.connectionSession {
		t.Fatalf("restored state=%+v token=%q resume=%q err=%v", state, gotToken, gotResume, err)
	}
	c2.close()
}

func TestCollaborationPersistenceFailuresAreObservable(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.writeState = func(string, []byte, os.FileMode) error { return errors.New("disk full") }
	c.mu.Lock()
	c.persistLocked()
	c.mu.Unlock()
	if state := c.snapshot(); !state.Retryable || !strings.Contains(state.LastError, "disk full") {
		t.Fatalf("state = %+v", state)
	}
	if err := os.WriteFile(c.persistPath, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	c2 := &desktopCollaboration{state: CollaborationState{Status: "disconnected"}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, persistPath: c.persistPath}
	c2.loadPersisted()
	if state := c2.snapshot(); !state.Retryable || !strings.Contains(state.LastError, "load collaboration state") {
		t.Fatalf("corrupt recovery state = %+v", state)
	}
}

func TestCollaborationReconnectBackoffIsBoundedAndJittered(t *testing.T) {
	first := collaborationReconnectDelay(0, 1)
	later := collaborationReconnectDelay(5, 1)
	capped := collaborationReconnectDelay(100, 1)
	if first < 400*time.Millisecond || first > 600*time.Millisecond || later <= first || capped > 36*time.Second {
		t.Fatalf("delays first=%v later=%v capped=%v", first, later, capped)
	}
	if collaborationReconnectDelay(3, 1) == collaborationReconnectDelay(3, 2) {
		t.Fatal("reconnect delay has no jitter")
	}
}

func TestCollaborationHeartbeatIsLowFrequencyAndToleratesMisses(t *testing.T) {
	if collaborationHeartbeatInterval < 90*time.Second {
		t.Fatalf("heartbeat interval = %v; want at least 90s", collaborationHeartbeatInterval)
	}
	if collaborationMemberStaleAfter < 3*collaborationHeartbeatInterval {
		t.Fatalf("stale threshold = %v; want at least three heartbeat intervals", collaborationMemberStaleAfter)
	}
}

func TestCollaborationHostInviteExportsLocalAddressesOnDemand(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "host", "session-a")
	conn.hostName = "0.0.0.0"
	conn.joinToken = "room-secret"
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "host", Host: conn.hostName, Port: conn.port, Room: conn.room, SessionID: conn.sessionID}
	invite, err := c.invite()
	if err != nil || invite.Room != conn.room || invite.Port != conn.port || invite.Token != conn.joinToken {
		t.Fatalf("invite=%+v err=%v", invite, err)
	}
	foundLoopback := false
	for _, host := range invite.Hosts {
		if host == "127.0.0.1" {
			foundLoopback = true
		}
		if host == "0.0.0.0" || host == "::" {
			t.Fatalf("invite exposed an unspecified address: %+v", invite.Hosts)
		}
	}
	if !foundLoopback {
		t.Fatalf("invite has no local double-open address: %+v", invite.Hosts)
	}
	c.state.Mode = "client"
	if _, err := c.invite(); err == nil {
		t.Fatal("client exported a Host invite using its own IP")
	}
}

func TestCollaborationRejoinAutomaticallyRetriesCachedOutbox(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	conn.rejoined = true
	c.outbox = []collab.CommandEnvelope{{
		RequestID: "cached-chat", Room: conn.room, MemberID: conn.memberID,
		Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "cached"}},
	}}
	state, err := c.installConnection(conn)
	if err != nil || state.Status != "syncing" {
		t.Fatalf("install state=%+v err=%v", state, err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		peer.mu.Lock()
		submitted := len(peer.submitted)
		peer.mu.Unlock()
		if submitted == 1 && c.snapshot().OutboxCount == 0 && c.snapshot().Status == "connected" {
			c.close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	c.close()
	t.Fatalf("cached outbox was not retried automatically: state=%+v outbox=%+v", c.snapshot(), c.outbox)
}

func TestNormalizeCollaborationHostIPv6Brackets(t *testing.T) {
	for input, want := range map[string]string{"[::1]": "::1", "::1": "::1", "127.0.0.1": "127.0.0.1", "devbox.local": "devbox.local"} {
		got, err := normalizeCollaborationHost(input)
		if err != nil || got != want {
			t.Fatalf("normalize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := normalizeCollaborationHost("[devbox]"); err == nil {
		t.Fatal("accepted brackets around a hostname")
	}
}

func TestCollaborationEventWrapperAndSequenceGap(t *testing.T) {
	item := collab.TimelineItem{ID: "chat-3", Sequence: 3, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "chat-3", Text: "hello", Revision: 1}}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	view := collaborationEventView("session-a", collab.RoomEvent{EventID: "event-3", Sequence: 3, Type: "chat.posted", Payload: payload})
	if view.SessionID != "session-a" {
		t.Fatalf("event session = %q", view.SessionID)
	}
	if view.Item == nil || view.Item.Type != collab.TimelineChat || view.Item.Chat.Text != "hello" {
		t.Fatalf("event view = %+v", view)
	}
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	if err := c.consumeStreamEvent(context.Background(), conn, collab.RoomEvent{Sequence: 4}); err == nil || c.snapshot().Snapshot.LatestSequence != 2 {
		t.Fatalf("gap was not rejected: err=%v state=%+v", err, c.snapshot())
	}
	peer.snapshot.LatestSequence = 3
	peer.snapshot.Room.LatestSequence = 3
	if err := c.consumeStreamEvent(context.Background(), conn, collab.RoomEvent{Sequence: 3, Payload: payload}); err != nil || c.snapshot().Snapshot.LatestSequence != 3 {
		t.Fatalf("contiguous event failed: err=%v state=%+v", err, c.snapshot())
	}
}

func TestCollaborationRecoveredRunNeverCrossesRoom(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	c.state = CollaborationState{Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID}
	c.recoveredRuns = []collaborationPersistedRun{
		{Room: "other-room", MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, RunID: "other-run", CommandID: "other-command"},
		{Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, RunID: "same-run", CommandID: "same-command"},
	}
	c.mu.Lock()
	c.recoverInterruptedRunsLocked(conn)
	c.mu.Unlock()
	if len(c.outbox) != 1 || c.outbox[0].Room != conn.room || c.outbox[0].Command.AgentRun.Status != collab.RunInterrupted {
		t.Fatalf("interrupted outbox = %+v", c.outbox)
	}
	if len(c.recoveredRuns) != 1 || c.recoveredRuns[0].Room != "other-room" {
		t.Fatalf("cross-room recovery was lost/published: %+v", c.recoveredRuns)
	}
}

func TestCollaborationLeaveFailureKeepsRecoverableIdentity(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{leaveErr: &collaborationTransportError{message: "host unavailable", retryable: true}}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.mu.Lock()
	c.persistLocked()
	c.mu.Unlock()
	if err := c.leave(context.Background()); err == nil {
		t.Fatal("leave failure was hidden")
	}
	state := c.snapshot()
	if c.conn != conn || state.Room != conn.room || state.Status != "failed" || !state.Retryable {
		t.Fatalf("leave failure destroyed recovery state: %+v", state)
	}
	data, err := os.ReadFile(c.persistPath)
	if err != nil || !strings.Contains(string(data), "connectionSecretRef") {
		t.Fatalf("recovery reference missing after leave failure: %s err=%v", data, err)
	}
	peer.mu.Lock()
	peer.leaveErr = nil
	peer.mu.Unlock()
	state, err = c.retry(context.Background())
	if err != nil || state.Status != "disconnected" || c.conn != nil {
		t.Fatalf("retry leave state=%+v conn=%v err=%v", state, c.conn, err)
	}
}

func TestCollaborationNonRetryOutboxRemainsUntilManualRetry(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{failNonRetry: 1}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.outbox = []collab.CommandEnvelope{{RequestID: "failed-chat", Room: conn.room, MemberID: conn.memberID, Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "keep me"}}}}
	if !c.drainOutbox(context.Background(), conn) || len(c.outbox) != 1 {
		t.Fatalf("failed outbox was discarded: %+v", c.outbox)
	}
	state := c.snapshot()
	if len(state.Outbox) != 1 || state.Outbox[0].Status != "failed" || !state.Retryable {
		t.Fatalf("failed outbox is not observable/retryable: %+v", state)
	}
	state, err := c.retry(context.Background())
	if err != nil || state.OutboxCount != 0 || len(c.outbox) != 0 {
		t.Fatalf("manual retry failed: state=%+v outbox=%+v err=%v", state, c.outbox, err)
	}
}

func TestHTTPCollaborationPeerStreamDecodesRoomEvent(t *testing.T) {
	payload, _ := json.Marshal(collab.TimelineItem{ID: "chat-1", Sequence: 1, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "chat-1", Text: "hello"}})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cs1.test" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "id: 1\nevent: room\ndata: %s\n\n", mustJSON(t, collab.RoomEvent{EventID: "event-1", Sequence: 1, Room: "room-a", Type: "chat.posted", Payload: payload}))
	}))
	defer server.Close()
	peer := &httpCollaborationPeer{baseURL: server.URL, streamClient: server.Client(), room: "room-a", session: "cs1.test"}
	var got collab.RoomEvent
	err := peer.Stream(context.Background(), 0, func(value collab.RoomEvent) error { got = value; return nil })
	if got.EventID != "event-1" || got.Sequence != 1 || err == nil {
		t.Fatalf("event=%+v err=%v", got, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCollaborationAgentEventFiltersReasoningToolsAndSecrets(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	run := &collaborationAgentRun{RunID: "run-1", CommandID: "command-1", SessionID: "session-a", Updates: make(chan collaborationRunUpdate, 2)}
	c.runs[run.SessionID] = run
	c.observeAgentEvent(run.SessionID, event.Event{Kind: event.Reasoning, Text: "private chain of thought"})
	c.observeAgentEvent(run.SessionID, event.Event{Kind: event.ToolResult, Tool: event.Tool{Args: `{"token":"tool-secret"}`, Output: "sensitive tool output"}})
	if run.Text.Len() != 0 {
		t.Fatalf("private event entered public summary: %q", run.Text.String())
	}
	c.observeAgentEvent(run.SessionID, event.Event{Kind: event.Message, Text: "done token=super-secret"})
	c.observeAgentEvent(run.SessionID, event.Event{Kind: event.TurnDone})
	update := <-run.Updates
	if strings.Contains(update.Summary, "super-secret") || !strings.Contains(update.Summary, "[REDACTED]") {
		t.Fatalf("summary was not redacted: %q", update.Summary)
	}
}

func TestCollaborationSummaryTruncationKeepsUTF8Valid(t *testing.T) {
	value := strings.Repeat("测", maxCollaborationSummaryBytes)
	got := sanitizeCollaborationText(value)
	if !strings.Contains(got, "…") || strings.ContainsRune(got, '\uFFFD') {
		t.Fatalf("invalid UTF-8 truncation suffix=%q", got[len(got)-8:])
	}
}
