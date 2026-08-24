package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/fileutil"
	"workground2/internal/memory"
	"workground2/internal/skill"
	"workground2/internal/unread"
)

type semanticIntentSession struct {
	control.SessionAPI
	intent agent.SemanticIntent
	err    error
	calls  int
}

type collaborationPromptSession struct {
	control.SessionAPI
	pending         control.PendingInteraction
	hasPending      bool
	approvalID      string
	approvalAllow   bool
	approvalSession bool
	approvalPersist bool
	answerID        string
	answers         []event.AskAnswer
	cancels         int
}

func (s *collaborationPromptSession) PendingInteraction() (control.PendingInteraction, bool) {
	return s.pending, s.hasPending
}

func (s *collaborationPromptSession) Approve(id string, allow, session, persist bool) {
	s.approvalID = id
	s.approvalAllow = allow
	s.approvalSession = session
	s.approvalPersist = persist
	s.hasPending = false
}

func (s *collaborationPromptSession) AnswerQuestion(id string, answers []event.AskAnswer) {
	s.answerID = id
	s.answers = append([]event.AskAnswer(nil), answers...)
	s.hasPending = false
}

func (s *collaborationPromptSession) Cancel() { s.cancels++ }

func (s *semanticIntentSession) ClassifySemanticIntent(context.Context, string) (agent.SemanticIntent, error) {
	s.calls++
	return s.intent, s.err
}

type fakeCollaborationPeer struct {
	mu           sync.Mutex
	snapshot     collab.Snapshot
	events       []collab.RoomEvent
	eventCalls   int
	submitted    []collab.CommandEnvelope
	failSubmit   int
	failNonRetry int
	leaveErr     error
	leaveCount   int
	heartbeats   int
}

type scriptedStreamPeer struct {
	fakeCollaborationPeer
	streamMu     sync.Mutex
	streamErrors []error
	streamCalls  int
	streamCalled chan int
}

func (p *scriptedStreamPeer) Stream(ctx context.Context, _ uint64, _ func(collab.RoomEvent) error) error {
	p.streamMu.Lock()
	p.streamCalls++
	call := p.streamCalls
	var err error
	if len(p.streamErrors) > 0 {
		err = p.streamErrors[0]
		p.streamErrors = p.streamErrors[1:]
	}
	p.streamMu.Unlock()
	if p.streamCalled != nil {
		select {
		case p.streamCalled <- call:
		default:
		}
	}
	if err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (p *fakeCollaborationPeer) Snapshot(context.Context) (collab.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return cloneCollaborationState(CollaborationState{Snapshot: p.snapshot}).Snapshot, nil
}

func (p *fakeCollaborationPeer) Events(_ context.Context, after uint64) ([]collab.RoomEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.eventCalls++
	out := make([]collab.RoomEvent, 0, len(p.events))
	for _, value := range p.events {
		if value.Sequence > after {
			out = append(out, value)
		}
	}
	return out, nil
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

// lockedSecretMap guards the test collaboration's secret store against the
// background auto-receive scanner goroutine (signalAutoReceiveFiles), which can
// read secrets after the test's main goroutine has written them.
type lockedSecretMap struct {
	mu  sync.Mutex
	val map[string]string
}

func newLockedSecretMap() *lockedSecretMap {
	return &lockedSecretMap{val: map[string]string{}}
}

func (s *lockedSecretMap) put(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.val[key] = value
}

func (s *lockedSecretMap) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.val[key]
}

func (s *lockedSecretMap) del(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.val, key)
}

func newTestDesktopCollaboration(t *testing.T) (*App, *desktopCollaboration, *lockedSecretMap) {
	t.Helper()
	app := &App{}
	secrets := newLockedSecretMap()
	c := &desktopCollaboration{
		app: app, ownerSessionID: "session-a", state: CollaborationState{Status: "disconnected", SessionID: "session-a"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
		persistPath:    filepath.Join(t.TempDir(), "collaboration.json"), writeState: fileutil.AtomicWriteFile,
		validateAgent: func(string) error { return nil },
	}
	c.setSecret = func(key, value string) error { secrets.put(key, value); return nil }
	c.getSecret = func(key string) string { return secrets.get(key) }
	c.removeSecret = func(key string) error { secrets.del(key); return nil }
	app.collaborations = map[string]*desktopCollaboration{"session-a": c}
	return app, c, secrets
}

func TestCollaborationAgentConfigRenamesAndPersists(t *testing.T) {
	app, runtime, _ := newTestDesktopCollaboration(t)
	ctrl := control.New(control.Options{Skills: []skill.Skill{{Name: "review", Body: "review body", Path: "(builtin)", Scope: skill.ScopeBuiltin}}})
	defer ctrl.Close()
	tab := &WorkspaceTab{ID: "session-a", SessionID: "session-a", Ctrl: ctrl, Ready: true}
	app.tabs = map[string]*WorkspaceTab{"session-a": tab}
	app.trackSession(tab)
	peer := &fakeCollaborationPeer{snapshot: collab.Snapshot{
		Room: collab.Room{ID: "room-a", Name: "Room A", LatestSequence: 1}, LatestSequence: 1,
		Members: []collab.Member{{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Old", Status: collab.AgentIdle}}},
	}}
	conn := &collaborationConnection{peer: peer, room: "room-a", memberID: "member-a", agentID: "agent-a", agentName: "Old", sessionID: "session-a", connectionSession: "cs1.test"}
	runtime.conn = conn
	runtime.state = CollaborationState{Status: "connected", Room: "room-a", MemberID: "member-a", AgentID: "agent-a", SessionID: "session-a", Snapshot: peer.snapshot, AgentConfig: defaultCollaborationAgentConfig()}

	want := CollaborationAgentConfig{Alias: "Kite", AutoRespondQuestions: true, AutoRespondRequests: true, AutoRespondAgents: true, AgentResponseIntervalSeconds: 15, AgentClockTurns: 8, AgentClockUnlimited: true, AgentClockWoundAt: "2026-08-05T03:04:05Z", RecognitionMode: "message", ContextRefs: []string{"skill:review"}}
	state, err := runtime.updateAgentConfig(context.Background(), UpdateCollaborationAgentConfigInput{SessionID: "session-a", RequestID: "agent-config-1", Config: want})
	if err != nil {
		t.Fatal(err)
	}
	if !equalCollaborationAgentConfig(state.AgentConfig, want) || state.Snapshot.Members[0].Agent.Name != "Kite" || conn.agentName != "Kite" {
		t.Fatalf("Agent config projection mismatch: %+v", state)
	}
	if len(peer.submitted) != 1 || peer.submitted[0].Command.Type != collab.CommandUpdateAgent || peer.submitted[0].Command.AgentUpdate.Name != "Kite" {
		t.Fatalf("Agent alias was not submitted once: %+v", peer.submitted)
	}
	if _, err := runtime.updateAgentConfig(context.Background(), UpdateCollaborationAgentConfigInput{SessionID: "session-a", RequestID: "agent-config-1", Config: want}); err != nil || len(peer.submitted) != 1 {
		t.Fatalf("repeated Agent config was not idempotent: submits=%d err=%v", len(peer.submitted), err)
	}

	data, err := os.ReadFile(runtime.persistPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted collaborationPersistedState
	if err := json.Unmarshal(data, &persisted); err != nil || !equalCollaborationAgentConfig(persisted.AgentConfig, want) || persisted.AgentName != "Kite" {
		t.Fatalf("Agent config was not persisted: %+v, %v", persisted, err)
	}
	legacy := normalizeCollaborationAgentConfig(CollaborationAgentConfig{Alias: "Legacy"}, "")
	if legacy.RecognitionMode != "interval" || legacy.AgentResponseIntervalSeconds != 30 || legacy.AgentClockTurns != 12 {
		t.Fatalf("legacy Agent collaboration defaults = recognition %s, interval %d, clock %d", legacy.RecognitionMode, legacy.AgentResponseIntervalSeconds, legacy.AgentClockTurns)
	}
	bounded := normalizeCollaborationAgentConfig(CollaborationAgentConfig{Alias: "Bounded", AgentResponseIntervalSeconds: 1, AgentClockTurns: 101}, "")
	if bounded.AgentResponseIntervalSeconds != 5 || bounded.AgentClockTurns != 100 {
		t.Fatalf("bounded Agent collaboration config = interval %d, clock %d", bounded.AgentResponseIntervalSeconds, bounded.AgentClockTurns)
	}
	if _, err := runtime.updateAgentConfig(context.Background(), UpdateCollaborationAgentConfigInput{SessionID: "session-a", RequestID: "agent-config-invalid-clock", Config: CollaborationAgentConfig{Alias: "Kite", AgentClockWoundAt: "not-a-time"}}); err == nil {
		t.Fatal("invalid Agent clock timestamp should fail explicitly")
	}
}

func TestCollaborationProfileUpdatesBothIdentitiesAndPersists(t *testing.T) {
	_, runtime, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{snapshot: collab.Snapshot{Room: collab.Room{ID: "room-a"}, Members: []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Old"}}}}}
	conn := &collaborationConnection{peer: peer, room: "room-a", memberID: "member-a", memberName: "Alice", agentID: "agent-a", agentName: "Old", sessionID: "session-a", connectionSession: "cs1.test"}
	runtime.conn = conn
	runtime.state = CollaborationState{Status: "connected", Room: "room-a", MemberID: "member-a", AgentID: "agent-a", SessionID: "session-a", Snapshot: peer.snapshot, AgentConfig: CollaborationAgentConfig{Alias: "Old", RecognitionMode: "off"}}

	memberAvatar := "data:image/png;base64,iVBORw0KGgo="
	agentAvatar := "data:image/webp;base64,UklGRg=="
	input := UpdateCollaborationProfileInput{SessionID: "session-a", RequestID: "profile-1", MemberName: "Alicia", MemberAvatar: memberAvatar, AgentName: "Kite", AgentAvatar: agentAvatar}
	state, err := runtime.updateProfile(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	member := state.Snapshot.Members[0]
	if member.Name != "Alicia" || member.Avatar != memberAvatar || member.Agent.Name != "Kite" || member.Agent.Avatar != agentAvatar || state.AgentConfig.Alias != "Kite" {
		t.Fatalf("profile projection mismatch: %+v", state)
	}
	if conn.memberName != "Alicia" || conn.memberAvatar != memberAvatar || conn.agentName != "Kite" || conn.agentAvatar != agentAvatar || len(peer.submitted) != 1 {
		t.Fatalf("connection profile mismatch: %+v submits=%d", conn, len(peer.submitted))
	}
	if _, err := runtime.updateProfile(context.Background(), input); err != nil || len(peer.submitted) != 1 {
		t.Fatalf("repeated profile update was not idempotent: submits=%d err=%v", len(peer.submitted), err)
	}
}

func TestCollaborationAgentExplicitSourcesUseLoadedMemoryAndControllerSkills(t *testing.T) {
	root := t.TempDir()
	agentsPath := filepath.Join(root, "AGENTS.md")
	skillPath := filepath.Join(root, ".workground2", "skills", "review", "SKILL.md")
	ctrl := control.New(control.Options{
		Memory: &memory.Set{Docs: []memory.Source{{Path: agentsPath, Scope: memory.ScopeProject, Body: "project rules"}}},
		Skills: []skill.Skill{{Name: "review", Description: "Review changes", Body: "REVIEW PLAYBOOK", Path: skillPath, Scope: skill.ScopeProject, RunAs: skill.RunInline}},
	})
	defer ctrl.Close()
	tab := &WorkspaceTab{ID: "session-a", SessionID: "session-a", Ctrl: ctrl, Ready: true}
	app := &App{tabs: map[string]*WorkspaceTab{"session-a": tab}}
	app.trackSession(tab)

	sources := app.collaborationAgentSources("session-a")
	if len(sources.Agents) != 1 || sources.Agents[0].Path != agentsPath || len(sources.Skills) != 1 || sources.Skills[0].Path != skillPath {
		t.Fatalf("explicit sources = %+v", sources)
	}
	refs := []string{sources.Agents[0].ID, sources.Skills[0].ID}
	prepared, err := app.prepareCollaborationAgentInput("session-a", refs, "handle the Room task")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<explicit-agents-md>", agentsPath, "REVIEW PLAYBOOK", "handle the Room task"} {
		if !strings.Contains(prepared, want) {
			t.Fatalf("prepared input missing %q:\n%s", want, prepared)
		}
	}
	if err := app.validateCollaborationAgentRefs("session-a", []string{"skill:missing"}); err == nil {
		t.Fatal("missing explicit source should fail visibly")
	}
	stale := app.collaborationAgentSourcesWithRefs("session-a", []string{"skill:missing"})
	if len(stale.Skills) != 2 || stale.Skills[1].ID != "skill:missing" || stale.Skills[1].Available {
		t.Fatalf("stale explicit source was not recoverable: %+v", stale.Skills)
	}
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

func TestCollaborationAgentConfirmationTargetsOwningSessionPrompt(t *testing.T) {
	ctrl := &collaborationPromptSession{hasPending: true, pending: control.PendingInteraction{
		Kind:     control.PendingInteractionApproval,
		Approval: event.Approval{ID: "approval-1"},
	}}
	app := &App{}
	app.tabs = map[string]*WorkspaceTab{"tab-a": {ID: "tab-a", SessionID: "session-a", Ctrl: ctrl, Ready: true}}
	app.tabOrder = []string{"tab-a"}
	app.trackSession(app.tabs["tab-a"])
	cancelled, err := app.respondCollaborationAgent("session-a", RespondCollaborationAgentRunInput{Allow: true, Session: true, Persist: true})
	if err != nil || cancelled || ctrl.approvalID != "approval-1" || !ctrl.approvalAllow || !ctrl.approvalSession || !ctrl.approvalPersist || ctrl.cancels != 0 {
		t.Fatalf("approval routed incorrectly: cancelled=%v id=%q allow=%v cancels=%d err=%v", cancelled, ctrl.approvalID, ctrl.approvalAllow, ctrl.cancels, err)
	}

	ctrl.pending = control.PendingInteraction{Kind: control.PendingInteractionAsk, Ask: event.Ask{ID: "ask-1", Questions: []event.AskQuestion{{ID: "q1", Prompt: "选择环境", Options: []event.AskOption{{Label: "测试"}, {Label: "生产"}}}}}}
	ctrl.hasPending = true
	if _, err := app.respondCollaborationAgent("session-a", RespondCollaborationAgentRunInput{Allow: true}); err == nil || ctrl.cancels != 0 {
		t.Fatalf("structured Ask was incorrectly accepted without answers: cancels=%d err=%v", ctrl.cancels, err)
	}
	ctrl.hasPending = true
	cancelled, err = app.respondCollaborationAgent("session-a", RespondCollaborationAgentRunInput{Answering: true, Answers: []QuestionAnswer{{QuestionID: "q1", Selected: []string{"测试"}}}})
	if err != nil || cancelled || ctrl.answerID != "ask-1" || len(ctrl.answers) != 1 || ctrl.answers[0].Selected[0] != "测试" {
		t.Fatalf("Ask answer routed incorrectly: cancelled=%v id=%q answers=%+v err=%v", cancelled, ctrl.answerID, ctrl.answers, err)
	}
	ctrl.hasPending = true
	cancelled, err = app.respondCollaborationAgent("session-a", RespondCollaborationAgentRunInput{Allow: false})
	if err != nil || !cancelled || ctrl.cancels != 1 {
		t.Fatalf("Ask rejection did not cancel the owning run: cancelled=%v cancels=%d err=%v", cancelled, ctrl.cancels, err)
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

func TestCollaborationRuntimeReplacesStaleOwnerForSameSessionPath(t *testing.T) {
	isolateDesktopUserDirs(t)
	sessionPath := filepath.Join(t.TempDir(), "room.jsonl")
	app := &App{}
	oldTab := &WorkspaceTab{ID: "old-tab", SessionID: "old-runtime-id", SessionPath: sessionPath}
	newTab := &WorkspaceTab{ID: "new-tab", SessionID: "new-runtime-id", SessionPath: sessionPath}
	app.trackSession(oldTab)
	app.trackSession(newTab)

	oldRuntime, err := app.collaborationRuntime(oldTab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	newRuntime, err := app.collaborationRuntime(newTab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer app.closeCollaborations()

	if newRuntime == oldRuntime {
		t.Fatal("SessionID rotation reused a runtime bound to the stale Agent identity")
	}
	if newRuntime.ownerSessionID != newTab.SessionID || newRuntime.ownerSessionPath != sessionRuntimeKey(sessionPath) {
		t.Fatalf("replacement owner = %q / %q", newRuntime.ownerSessionID, newRuntime.ownerSessionPath)
	}
	app.collaborationMu.Lock()
	defer app.collaborationMu.Unlock()
	if app.collaborations[oldTab.SessionID] != nil || app.collaborations[newTab.SessionID] != newRuntime || len(app.collaborations) != 1 {
		t.Fatalf("runtime registry did not converge: %+v", app.collaborations)
	}
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

func TestCollaborationJoinScopesIdentityBeforeFirstJoin(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	input := JoinCollaborationRoomInput{
		Host: "10.0.0.8", Port: 39170, Room: "room-a", SessionID: "session-b",
		MemberID: "shared-member", MemberName: "Alice", AgentID: "shared-agent", AgentName: "Alice Agent",
	}
	calls := 0
	c.openJoin = func(_ context.Context, _ JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		calls++
		if resume != "" {
			t.Fatalf("unexpected resume credential %q", resume)
		}
		if identity.ID == input.MemberID || identity.Agent.ID == input.AgentID {
			t.Fatalf("first join identity was not scoped: %+v", identity)
		}
		conn := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		conn.hostName, conn.memberID, conn.agentID = input.Host, identity.ID, identity.Agent.ID
		conn.memberName, conn.agentName = identity.Name, identity.Agent.Name
		conn.initialSnapshot.Members = []collab.Member{{ID: identity.ID, Name: identity.Name, Agent: identity.Agent}}
		return conn, nil
	}
	state, err := c.join(context.Background(), input)
	if err != nil || calls != 1 || state.Status != "connected" || state.MemberID == input.MemberID || state.AgentID == input.AgentID {
		t.Fatalf("scoped join state=%+v calls=%d err=%v", state, calls, err)
	}
	c.close()
}

func TestCollaborationJoinRecoversAggregatedLANResumeCollision(t *testing.T) {
	store, err := collab.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := collab.NewService(store)
	if _, err := service.CreateRoom(context.Background(), collab.CreateRoomInput{RequestID: "create", ID: "room-a", Name: "Room A"}); err != nil {
		t.Fatal(err)
	}
	identity := collab.MemberDescriptor{ID: "shared-member", Name: "Alice", Agent: collab.AgentDescriptor{ID: "shared-agent", Name: "Alice Agent"}}
	if _, err := service.Join(context.Background(), collab.JoinInput{RequestID: "occupy", Room: "room-a", Member: identity}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(collab.NewHandler(service))
	defer server.Close()
	address := server.Listener.Addr().(*net.TCPAddr)

	_, c, _ := newTestDesktopCollaboration(t)
	c.openJoin = c.openJoinedRoom
	state, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: address.IP.String(), Port: address.Port, Room: "room-a", SessionID: "session-b",
		MemberID: identity.ID, MemberName: identity.Name, AgentID: identity.Agent.ID, AgentName: identity.Agent.Name,
	})
	if err != nil || state.Status != "connected" || state.MemberID == identity.ID || state.AgentID == identity.Agent.ID {
		t.Fatalf("aggregated LAN collision was not recovered: state=%+v err=%v", state, err)
	}
	c.close()
}

func TestCollaborationJoinDoesNotRotateIdentityForOtherUnauthorizedErrors(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	calls := 0
	c.openJoin = func(_ context.Context, _ JoinCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		calls++
		return nil, &collab.Error{Code: collab.CodeUnauthorized, Message: "room token is invalid"}
	}
	_, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: "10.0.0.8", Port: 39170, Room: "room-a", SessionID: "session-b",
		MemberID: "shared-member", MemberName: "Alice", AgentID: "shared-agent", AgentName: "Alice Agent",
	})
	if err == nil || calls != 1 {
		t.Fatalf("unauthorized join calls=%d err=%v", calls, err)
	}
}

func TestCollaborationRecognizesLegacyResumeRequirement(t *testing.T) {
	err := &collab.Error{Code: collab.CodeUnauthorized, Message: collab.ResumeRequiredMessage}
	if !collaborationMemberResumeRequired(err) {
		t.Fatalf("legacy resume error was not recognized: %v", err)
	}
}

func TestCollaborationRecognizesAggregatedResumeRequirement(t *testing.T) {
	err := &collaborationTransportError{
		message:   "all collaboration routes failed: lan: " + collab.ResumeRequiredMessage,
		retryable: true,
		causes:    []error{&collab.Error{Code: collab.CodeResumeNeeded, Message: collab.ResumeRequiredMessage}},
	}
	if !collaborationMemberResumeRequired(err) {
		t.Fatalf("aggregated resume error was not recognized: %v", err)
	}
	if !collaborationErrorRetryable(err) {
		t.Fatalf("aggregated route failure lost its retryable state: %v", err)
	}
}

func TestCollaborationJoinDoesNotResumeMemberFromAnotherWorkspace(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	current := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	current.hostName = "10.0.0.8"
	c.conn = current
	c.state = CollaborationState{
		Status: "connected", Mode: "client", Host: current.hostName, Port: current.port, Room: current.room,
		MemberID: current.memberID, AgentID: current.agentID, SessionID: current.sessionID, Snapshot: current.initialSnapshot,
	}
	gotResume := ""
	var gotIdentity collab.MemberDescriptor
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		gotResume = resume
		gotIdentity = identity
		next := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		next.hostName = current.hostName
		next.memberID, next.agentID = identity.ID, identity.Agent.ID
		return next, nil
	}
	state, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: current.hostName, Port: current.port, Room: current.room, SessionID: "session-b",
		MemberID: current.memberID, MemberName: current.memberName, AgentID: current.agentID, AgentName: current.agentName,
	})
	if err != nil || state.Status != "connected" || state.SessionID != "session-b" || gotResume != "" || gotIdentity.ID == current.memberID || gotIdentity.Agent.ID == current.agentID {
		t.Fatalf("workspace-isolated join state=%+v identity=%+v resume=%q err=%v", state, gotIdentity, gotResume, err)
	}
	c.close()
}

func TestCollaborationJoinDoesNotLoadResumeCredentialAcrossWorkspaceRuntimes(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	const resume = "cs1.saved-for-member"
	secrets.put(collaborationSecretRef("10.0.0.8", 39170, "room-a", "member-a"), resume)
	gotResume := ""
	var gotIdentity collab.MemberDescriptor
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, value string) (*collaborationConnection, error) {
		gotResume = value
		gotIdentity = identity
		conn := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		conn.hostName, conn.memberID, conn.agentID = input.Host, identity.ID, identity.Agent.ID
		return conn, nil
	}
	state, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: "10.0.0.8", Port: 39170, Room: "room-a", SessionID: "different-workspace",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent",
	})
	if err != nil || state.Status != "connected" || state.SessionID != "different-workspace" || gotResume != "" || gotIdentity.ID == "member-a" || gotIdentity.Agent.ID == "agent-a" {
		t.Fatalf("cross-workspace credential isolation state=%+v identity=%+v resume=%q err=%v", state, gotIdentity, gotResume, err)
	}
	c.close()
}

func TestCollaborationJoinKeepsLegacyResumeForSameSession(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	current := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	current.hostName = "10.0.0.8"
	c.conn = current
	c.state = CollaborationState{
		Status: "connected", Mode: "client", Host: current.hostName, Port: current.port, Room: current.room,
		MemberID: current.memberID, AgentID: current.agentID, SessionID: current.sessionID, Snapshot: current.initialSnapshot,
	}
	var gotIdentity collab.MemberDescriptor
	gotResume := ""
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		gotIdentity, gotResume = identity, resume
		next := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		next.hostName, next.rejoined = input.Host, true
		return next, nil
	}
	state, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: current.hostName, Port: current.port, Room: current.room, SessionID: current.sessionID,
		MemberID: current.memberID, MemberName: current.memberName, AgentID: current.agentID, AgentName: current.agentName,
	})
	if err != nil || state.Status != "syncing" || gotResume != current.connectionSession || gotIdentity.ID != current.memberID || gotIdentity.Agent.ID != current.agentID {
		t.Fatalf("same-session legacy resume state=%+v identity=%+v resume=%q err=%v", state, gotIdentity, gotResume, err)
	}
	c.close()
}

func TestCollaborationJoinFallsBackFromRejectedLegacyResume(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	current := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	current.hostName = "10.0.0.8"
	c.conn = current
	c.state = CollaborationState{
		Status: "connected", Mode: "client", Host: current.hostName, Port: current.port, Room: current.room,
		MemberID: current.memberID, AgentID: current.agentID, SessionID: current.sessionID, Snapshot: current.initialSnapshot,
	}
	calls := 0
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		calls++
		if calls == 1 {
			if identity.ID != current.memberID || resume != current.connectionSession {
				t.Fatalf("legacy attempt identity=%+v resume=%q", identity, resume)
			}
			return nil, &collab.Error{Code: collab.CodeResumeNeeded, Message: collab.ResumeRequiredMessage}
		}
		if identity.ID == current.memberID || identity.Agent.ID == current.agentID || resume != "" {
			t.Fatalf("fallback attempt identity=%+v resume=%q", identity, resume)
		}
		next := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		next.hostName, next.memberID, next.agentID = input.Host, identity.ID, identity.Agent.ID
		return next, nil
	}
	state, err := c.join(context.Background(), JoinCollaborationRoomInput{
		Host: current.hostName, Port: current.port, Room: current.room, SessionID: current.sessionID,
		MemberID: current.memberID, MemberName: current.memberName, AgentID: current.agentID, AgentName: current.agentName,
	})
	if err != nil || calls != 2 || state.Status != "connected" || state.MemberID == current.memberID {
		t.Fatalf("legacy fallback state=%+v calls=%d err=%v", state, calls, err)
	}
	c.close()
}

func TestCollaborationRevokedSessionCannotOverwriteNewWorkspaceCredential(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "client", "old-workspace")
	ref := collaborationSecretRef(conn.hostName, conn.port, conn.room, conn.memberID)
	secrets.put(ref, "cs1.new-workspace-session")
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Mode: "client", Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	c.markReconnect(conn, &collab.Error{Code: collab.CodeUnauthorized, Message: "connection session is invalid"})
	state := c.snapshot()
	if state.Status != "failed" || !state.Retryable || c.conn != nil || secrets.get(ref) != "cs1.new-workspace-session" {
		t.Fatalf("revoked session state=%+v conn=%p credential=%q", state, c.conn, secrets.get(ref))
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

func TestCollaborationHostRecoversStalePersistedSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	app, c, secrets := newTestDesktopCollaboration(t)
	defer c.close()
	ctx := context.Background()
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := app.closeCollaborationLAN(shutdown); err != nil {
			t.Errorf("close shared LAN host: %v", err)
		}
	}()
	identity := collab.MemberDescriptor{ID: "member-host", Name: "Host", Agent: collab.AgentDescriptor{ID: "agent-host", Name: "Host Agent", Status: collab.AgentIdle}}
	input := HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", Room: "host-room", RoomName: "Host Room", SessionID: "session-a", ProtocolVersion: collaborationProtocolV2,
	}
	first, err := c.openHostedRoom(ctx, input, identity, "")
	if err != nil {
		t.Fatal(err)
	}
	staleSession := first.connectionSession
	newer, err := first.authority.service.RecoverHostMember(ctx, collab.JoinInput{
		RequestID: "newer-host-session", Room: input.Room, Member: identity,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.close(ctx, false); err != nil {
		t.Fatal(err)
	}

	recovered, err := c.openHostedRoom(ctx, input, identity, staleSession)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.close(context.Background(), false)
	if recovered.connectionSession == staleSession || recovered.connectionSession == newer.ConnectionSession {
		t.Fatal("Host session was not rotated")
	}
	if !app.sharedCollaborationLAN().roomActive(input.Room) {
		t.Fatal("recovered Host Room was released from the shared Listener")
	}
	if _, err := recovered.peer.Snapshot(ctx); err != nil {
		t.Fatalf("recovered Host peer is unusable: %v", err)
	}
	state, err := c.installConnection(recovered)
	if err != nil {
		t.Fatal(err)
	}
	secretRef := collaborationSecretRef(state.Host, state.Port, state.Room, state.MemberID)
	if got := secrets.get(secretRef); got != recovered.connectionSession {
		t.Fatal("recovered Host session was not persisted")
	}
	_, err = first.authority.service.Join(ctx, collab.JoinInput{
		RequestID: "remote-stale-session", Room: input.Room, Member: identity, ResumeSession: staleSession,
	})
	var protocolErr *collab.Error
	if !errors.As(err, &protocolErr) || protocolErr.Code != collab.CodeResumeNeeded {
		t.Fatalf("ordinary Join with stale session = %v, want resume_required", err)
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
	if routedSession != "session-a" || strings.Index(routedInput, "第一条") > strings.Index(routedInput, "第二条") || !strings.Contains(routedInput, "author=Alice revision=2") || !strings.Contains(routedInput, "<room-collaboration") || !strings.Contains(routedInput, `"agentId":"agent-a"`) {
		t.Fatalf("explicit route/context mismatch: session=%q input=%q", routedSession, routedInput)
	}
	busy, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "start-2", SessionID: "session-a", Instruction: "另一个任务"})
	if err != nil || !busy.Queued || busy.RunID == "" || len(c.snapshot().QueuedTasks) != 1 || submits != 1 {
		t.Fatalf("queued result=%+v queue=%+v submits=%d err=%v", busy, c.snapshot().QueuedTasks, submits, err)
	}
	changed := input
	changed.Instruction = "不同指令"
	if _, err := c.startAgent(context.Background(), changed); err == nil {
		t.Fatal("same requestId with a different fingerprint was accepted")
	}
}

func TestCollaborationAgentOutputExtractsDirectedHandoffs(t *testing.T) {
	snapshot := collab.Snapshot{
		Members: []collab.Member{
			{ID: "member-a", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a"}},
			{ID: "member-b", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-b"}},
		},
		Timeline: []collab.TimelineItem{{ID: "message-1", Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "message-1", Text: "check this"}}},
	}
	output := `已完成当前分析。
<room-handoffs>[{"targetAgentId":"agent-b","instruction":"复核边界条件","referenceIds":["message-1","missing"],"reason":"需要独立验证","expectedOutcome":"给出测试结论","requiresResponse":true},{"targetAgentId":"agent-a","instruction":"self","requiresResponse":true}]</room-handoffs>`
	public, handoffs := collaborationAgentOutput(output, snapshot, "agent-a", nil)
	if public != "已完成当前分析。" || len(handoffs) != 1 {
		t.Fatalf("parsed output=%q handoffs=%+v", public, handoffs)
	}
	if handoffs[0].TargetAgentID != "agent-b" || handoffs[0].Instruction != "复核边界条件" || !handoffs[0].RequiresResponse || len(handoffs[0].ReferenceIDs) != 1 || handoffs[0].ReferenceIDs[0] != "message-1" {
		t.Fatalf("directed handoff=%+v", handoffs[0])
	}
	malformed := `保留原文<room-handoffs>[bad]</room-handoffs>`
	if text, parsed := collaborationAgentOutput(malformed, snapshot, "agent-a", nil); text != malformed || len(parsed) != 0 {
		t.Fatalf("malformed marker was hidden: text=%q parsed=%+v", text, parsed)
	}
}

func TestCollaborationAgentQueueCapsCancelsAndRunsFIFO(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	submitted := make(chan string, maxCollaborationAgentQueue+2)
	c.submitAgent = func(_, instruction, _ string) error {
		submitted <- instruction
		return nil
	}
	first, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "active", SessionID: "session-a", Instruction: "active task"})
	if err != nil || first.RunID == "" {
		t.Fatalf("start active: %+v, %v", first, err)
	}
	if got := <-submitted; got != "active task" {
		t.Fatalf("active instruction = %q", got)
	}
	c.mu.Lock()
	c.state.AgentConfig.ContextRefs = []string{"skill:review"}
	c.mu.Unlock()
	var cancelID string
	for i := 0; i < maxCollaborationAgentQueue; i++ {
		input := StartCollaborationAgentInput{RequestID: fmt.Sprintf("queued-%02d", i), SessionID: "session-a", Instruction: fmt.Sprintf("task %02d", i)}
		result, queueErr := c.startAgent(context.Background(), input)
		if queueErr != nil || !result.Queued || result.RunID == "" {
			t.Fatalf("enqueue %d: %+v, %v", i, result, queueErr)
		}
		if i == 10 {
			cancelID = result.RunID
		}
	}
	state := c.snapshot()
	if len(state.QueuedTasks) != maxCollaborationAgentQueue || state.QueuedTasks[0].Instruction != "task 00" || state.QueuedTasks[19].Instruction != "task 19" {
		t.Fatalf("queue projection = %+v", state.QueuedTasks)
	}
	overflow, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "overflow", SessionID: "session-a", Instruction: "overflow task"})
	if err != nil || overflow.Code != "agent_queue_full" || overflow.Retryable || len(c.snapshot().QueuedTasks) != maxCollaborationAgentQueue {
		t.Fatalf("overflow result=%+v err=%v", overflow, err)
	}
	request := &collab.AgentRequest{ID: "request-full", AuthorID: "member-b", TargetMemberID: conn.memberID, Instruction: "must stay pending", Status: collab.RequestPending}
	requestItem := collab.TimelineItem{ID: request.ID, Sequence: 2, Type: collab.TimelineAgentRequest, AgentRequest: request}
	c.mu.Lock()
	c.state.Snapshot.Timeline = append(c.state.Snapshot.Timeline, requestItem)
	c.mu.Unlock()
	peer.mu.Lock()
	peer.snapshot.Timeline = append(peer.snapshot.Timeline, requestItem)
	peer.mu.Unlock()
	fullRequest, err := c.respond(context.Background(), RespondCollaborationRequestInput{RequestID: "accept-full", AgentRequestID: request.ID, Action: "accept", SessionID: "session-a"})
	if err != nil || fullRequest.Code != "agent_queue_full" {
		t.Fatalf("full request result=%+v err=%v", fullRequest, err)
	}
	peer.mu.Lock()
	for _, env := range peer.submitted {
		if env.Command.Type == collab.CommandDecideAgentRequest && env.Command.RequestDecision.AgentRequestID == request.ID {
			peer.mu.Unlock()
			t.Fatal("full queue accepted a request that could not be scheduled")
		}
	}
	peer.mu.Unlock()
	cancelled, err := c.cancelQueuedTask(context.Background(), CancelCollaborationQueuedTaskInput{SessionID: "session-a", TaskID: cancelID})
	if err != nil || cancelled.RunID != cancelID || len(c.snapshot().QueuedTasks) != maxCollaborationAgentQueue-1 {
		t.Fatalf("cancel result=%+v queue=%d err=%v", cancelled, len(c.snapshot().QueuedTasks), err)
	}
	duplicate, err := c.cancelQueuedTask(context.Background(), CancelCollaborationQueuedTaskInput{SessionID: "session-a", TaskID: cancelID})
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("repeat cancel=%+v err=%v", duplicate, err)
	}
	restored := &desktopCollaboration{
		ownerSessionID: "session-a", state: CollaborationState{Status: "disconnected", SessionID: "session-a"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{}, persistPath: c.persistPath, getSecret: func(string) string { return "" },
	}
	restored.loadPersisted()
	if got := restored.snapshot().QueuedTasks; len(got) != maxCollaborationAgentQueue-1 || got[0].Instruction != "task 00" || got[0].QueuedAt == "" || restored.queuedRuns[0].PublishIndex != 1 || !equalStrings(restored.queuedRuns[0].ContextRefs, []string{"skill:review"}) {
		t.Fatalf("restored queue = %+v", got)
	}
	c.observeAgentEvent("session-a", event.Event{Kind: event.TurnDone})
	select {
	case got := <-submitted:
		if got != "task 00" {
			t.Fatalf("next instruction = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("next queued task did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(c.snapshot().QueuedTasks) != maxCollaborationAgentQueue-2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(c.snapshot().QueuedTasks); got != maxCollaborationAgentQueue-2 {
		t.Fatalf("queue size after FIFO start = %d", got)
	}
}

func TestRecoveredCollaborationAgentQueueStartsOnceWhenWorkspaceIsIdle(t *testing.T) {
	app, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.queuedRuns = []*collaborationAgentRun{{
		Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, RunID: "recovered-run", CommandID: "recovered-command",
		SessionID: conn.sessionID, Instruction: "恢复后继续执行", QueuedAt: time.Now().UTC().Format(time.RFC3339Nano), Updates: make(chan collaborationRunUpdate, 32),
	}}
	c.persistLocked()

	restored := &desktopCollaboration{
		app: app, ownerSessionID: "session-a", state: CollaborationState{Status: "disconnected", SessionID: "session-a"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, outboxFailures: map[string]string{},
		persistPath: c.persistPath, writeState: fileutil.AtomicWriteFile, getSecret: func(string) string { return "" },
	}
	restored.loadPersisted()
	restored.agentReady = func(string) (bool, error) { return true, nil }
	submitted := make(chan string, 2)
	restored.submitAgent = func(_, instruction, _ string) error {
		submitted <- instruction
		return nil
	}

	var starts sync.WaitGroup
	starts.Add(2)
	for range 2 {
		go func() {
			defer starts.Done()
			restored.startNextQueuedAgent("session-a")
		}()
	}
	starts.Wait()
	select {
	case got := <-submitted:
		if got != "恢复后继续执行" {
			t.Fatalf("recovered instruction = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered queued task did not start")
	}
	select {
	case got := <-submitted:
		t.Fatalf("recovered queued task started twice: %q", got)
	case <-time.After(100 * time.Millisecond):
	}
	if got := restored.snapshot().QueuedTasks; len(got) != 0 {
		t.Fatalf("recovered queue was not drained: %+v", got)
	}
}

func TestCollaborationAgentConfirmationResumesCurrentRunWithoutQueueing(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	run := &collaborationAgentRun{
		Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, RunID: "waiting-run", CommandID: "waiting-command",
		SessionID: conn.sessionID, Instruction: "continue current task", PromptOpen: true, Updates: make(chan collaborationRunUpdate, 1),
	}
	c.runs[conn.sessionID] = run
	var calls int
	var allowed bool
	c.respondAgent = func(sessionID string, input RespondCollaborationAgentRunInput) (bool, error) {
		calls++
		allowed = input.Allow
		if sessionID != conn.sessionID {
			t.Fatalf("confirmation routed to %q", sessionID)
		}
		return false, nil
	}

	result, err := c.respondAgentRun(context.Background(), RespondCollaborationAgentRunInput{SessionID: conn.sessionID, RunID: run.RunID, Allow: true})
	if err != nil || result.RunID != run.RunID || calls != 1 || !allowed {
		t.Fatalf("confirmation result=%+v calls=%d allowed=%v err=%v", result, calls, allowed, err)
	}
	if len(c.snapshot().QueuedTasks) != 0 || c.runs[conn.sessionID] != run {
		t.Fatalf("confirmation changed queue/current run: queue=%+v current=%p", c.snapshot().QueuedTasks, c.runs[conn.sessionID])
	}
	duplicate, err := c.respondAgentRun(context.Background(), RespondCollaborationAgentRunInput{SessionID: conn.sessionID, RunID: run.RunID, Allow: true})
	if err != nil || !duplicate.Duplicate || calls != 1 {
		t.Fatalf("repeat confirmation=%+v calls=%d err=%v", duplicate, calls, err)
	}
	c.observeAgentEvent(conn.sessionID, event.Event{Kind: event.ApprovalRequest, Approval: event.Approval{ID: "approval-2", Tool: "shell_command", Subject: "go test ./desktop", Reason: "执行本地测试"}})
	prompt := c.snapshot().AgentPrompt
	if prompt == nil || prompt.RunID != run.RunID || prompt.Kind != control.PendingInteractionApproval || prompt.Tool != "shell_command" || prompt.Subject != "go test ./desktop" || prompt.Reason != "执行本地测试" {
		t.Fatalf("approval details were not projected locally: %+v", prompt)
	}
	rejected, err := c.respondAgentRun(context.Background(), RespondCollaborationAgentRunInput{SessionID: conn.sessionID, RunID: run.RunID, Allow: false})
	if err != nil || rejected.RunID != run.RunID || calls != 2 || allowed {
		t.Fatalf("rejection result=%+v calls=%d allowed=%v err=%v", rejected, calls, allowed, err)
	}
	if len(c.snapshot().QueuedTasks) != 0 || c.runs[conn.sessionID] != run {
		t.Fatalf("rejection created a queue entry or replaced the run: queue=%+v current=%p", c.snapshot().QueuedTasks, c.runs[conn.sessionID])
	}
	if prompt := c.snapshot().AgentPrompt; prompt != nil {
		t.Fatalf("resolved approval prompt remained visible: %+v", prompt)
	}
	peer.mu.Lock()
	defer peer.mu.Unlock()
	foundRunning := false
	for _, env := range peer.submitted {
		if env.Command.AgentRun != nil && env.Command.AgentRun.RunID == run.RunID && env.Command.AgentRun.Status == collab.RunRunning {
			foundRunning = true
		}
	}
	if !foundRunning {
		t.Fatalf("confirmation did not resume current Run: %+v", peer.submitted)
	}
}

func TestCollaborationCurrentRunCanStopIdempotently(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.state = CollaborationState{Status: "connected", SessionID: "session-a"}
	run := &collaborationAgentRun{
		RunID: "run-a", SessionID: "session-a", Instruction: "检查当前修改",
		StartedAt: time.Unix(1_700_000_000, 0), PromptOpen: true,
		Prompt:  &CollaborationAgentPrompt{RunID: "run-a", Kind: control.PendingInteractionApproval, ID: "approval-a"},
		Updates: make(chan collaborationRunUpdate, 1),
	}
	run.Text.WriteString("正在运行测试")
	c.runs[run.SessionID] = run
	c.queuedRuns = []*collaborationAgentRun{{RunID: "queued-a", SessionID: "session-a"}}
	stops := 0
	c.stopAgent = func(sessionID string) error {
		stops++
		if sessionID != "session-a" {
			t.Fatalf("stop routed to %q", sessionID)
		}
		return nil
	}

	before := c.snapshot().CurrentRun
	if before == nil || before.Phase != "waiting_approval" || before.Progress != "正在运行测试" || before.QueueCount != 1 || before.StartedAt == 0 {
		t.Fatalf("current run projection = %+v", before)
	}
	if err := c.stopCurrentAgentRun("session-a", "newer-run"); err == nil || stops != 0 {
		t.Fatalf("mismatched run stop err=%v calls=%d", err, stops)
	}
	if err := c.stopCurrentAgentRun("session-a", run.RunID); err != nil {
		t.Fatal(err)
	}
	after := c.snapshot()
	if stops != 1 || after.CurrentRun == nil || after.CurrentRun.Phase != "stopping" || after.AgentPrompt != nil {
		t.Fatalf("stopping projection=%+v prompt=%+v calls=%d", after.CurrentRun, after.AgentPrompt, stops)
	}
	if err := c.stopCurrentAgentRun("session-a", run.RunID); err != nil || stops != 1 {
		t.Fatalf("repeated stop err=%v calls=%d", err, stops)
	}

	c.observeAgentEvent("session-a", event.Event{Kind: event.TurnDone, Err: context.Canceled})
	select {
	case update := <-run.Updates:
		if !update.Final || update.Status != collab.RunCancelled || update.Error != "cancelled by the Agent owner" {
			t.Fatalf("cancel update = %+v", update)
		}
	default:
		t.Fatal("cancelled run did not publish a final update")
	}
}

func TestCollaborationCurrentRunStopFailureRestoresPrompt(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.state = CollaborationState{Status: "connected", SessionID: "session-a"}
	run := &collaborationAgentRun{
		RunID: "run-a", SessionID: "session-a", Instruction: "等待确认", PromptOpen: true,
		Prompt:  &CollaborationAgentPrompt{RunID: "run-a", Kind: control.PendingInteractionApproval, ID: "approval-a"},
		Updates: make(chan collaborationRunUpdate, 1),
	}
	c.runs[run.SessionID] = run
	c.stopAgent = func(string) error { return errors.New("workspace unavailable") }

	if err := c.stopCurrentAgentRun("session-a", run.RunID); err == nil {
		t.Fatal("stop unexpectedly succeeded")
	}
	state := c.snapshot()
	if run.StopRequested || state.CurrentRun == nil || state.CurrentRun.Phase != "waiting_approval" || state.AgentPrompt == nil || state.AgentPrompt.ID != "approval-a" {
		t.Fatalf("failed stop was not rolled back: run=%+v current=%+v prompt=%+v", run, state.CurrentRun, state.AgentPrompt)
	}
	delete(c.runs, run.SessionID)
	if err := c.stopCurrentAgentRun("session-a", run.RunID); err != nil {
		t.Fatalf("stop with no current run should be idempotent: %v", err)
	}
}

func TestAutomaticCollaborationAgentUsesScopedAutoApproval(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}

	prepared := make(chan string, 1)
	restored := make(chan string, 1)
	submitted := make(chan string, 2)
	c.prepareAutoAgent = func(sessionID string) (string, error) {
		prepared <- sessionID
		return control.ToolApprovalAsk, nil
	}
	c.restoreAutoAgent = func(sessionID, previous string) {
		restored <- sessionID + ":" + previous
	}
	c.submitAgent = func(_, instruction, _ string) error {
		submitted <- instruction
		return nil
	}

	_, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "auto-run", SessionID: "session-a", Instruction: "自动回答", Automatic: true})
	if err != nil {
		t.Fatalf("start automatic Agent: %v", err)
	}
	if got := <-prepared; got != "session-a" {
		t.Fatalf("prepared session = %q", got)
	}
	if got := <-submitted; got != "自动回答" {
		t.Fatalf("submitted instruction = %q", got)
	}
	c.observeAgentEvent("session-a", event.Event{Kind: event.TurnDone})
	select {
	case got := <-restored:
		if got != "session-a:"+control.ToolApprovalAsk {
			t.Fatalf("restored approval = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("automatic Agent approval mode was not restored")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		active := c.runs["session-a"] != nil
		c.mu.RUnlock()
		if !active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	_, err = c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "manual-run", SessionID: "session-a", Instruction: "手动回答"})
	if err != nil {
		t.Fatalf("start manual Agent: %v", err)
	}
	if got := <-submitted; got != "手动回答" {
		t.Fatalf("manual instruction = %q", got)
	}
	select {
	case got := <-prepared:
		t.Fatalf("manual Agent unexpectedly changed approval mode for %q", got)
	default:
	}
}

func TestReadOnlyCollaborationAgentEnforcesTextOnlyBoundary(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	peer.snapshot = conn.initialSnapshot
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}

	prepared := make(chan string, 1)
	restored := make(chan string, 1)
	submitted := make(chan string, 2)
	c.prepareAgentReadOnly = func(sessionID string) (bool, error) {
		prepared <- sessionID
		return false, nil
	}
	c.restoreAgentReadOnly = func(sessionID string, previous bool) {
		restored <- sessionID + ":" + strconv.FormatBool(previous)
	}
	c.submitAgent = func(_, instruction, input string) error {
		submitted <- instruction + "\x00" + input
		return nil
	}

	// A read-only automatic run (question-only mode) must flip the executor
	// read-only gate before launch and restore it when the run completes.
	_, err := c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "readonly-run", SessionID: "session-a", Instruction: "回答一下", Automatic: true, ReadOnly: true})
	if err != nil {
		t.Fatalf("start read-only Agent: %v", err)
	}
	if got := <-prepared; got != "session-a" {
		t.Fatalf("prepared read-only session = %q", got)
	}
	full := <-submitted
	if !strings.HasPrefix(full, "回答一下\x00") {
		t.Fatalf("submitted read-only input = %q", full)
	}
	if !strings.Contains(full, collaborationReadOnlyAnswerMarker) {
		t.Error("read-only run input must carry the text-only answer directive")
	}
	c.mu.RLock()
	readOnlyRun := c.runs["session-a"]
	c.mu.RUnlock()
	c.observeAgentEvent("session-a", event.Event{Kind: event.TurnDone})
	select {
	case got := <-restored:
		if got != "session-a:false" {
			t.Fatalf("restored read-only gate = %q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read-only executor gate was not restored after the run")
	}
	// Completion publishing also calls restoration; it must remain a no-op and
	// must not overwrite a later runtime policy change.
	c.restoreAgentReadOnlyForRun(readOnlyRun)
	select {
	case got := <-restored:
		t.Fatalf("read-only gate restored more than once: %q", got)
	default:
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		active := c.runs["session-a"] != nil
		c.mu.RUnlock()
		if !active {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A non-read-only automatic run must not touch the read-only gate.
	_, err = c.startAgent(context.Background(), StartCollaborationAgentInput{RequestID: "full-run", SessionID: "session-a", Instruction: "改一下", Automatic: true})
	if err != nil {
		t.Fatalf("start full-capability Agent: %v", err)
	}
	select {
	case got := <-prepared:
		t.Fatalf("full-capability run unexpectedly enabled read-only gate for %q", got)
	default:
	}
	if got := <-submitted; !strings.HasPrefix(got, "改一下\x00") || strings.Contains(got, collaborationReadOnlyAnswerMarker) {
		t.Fatalf("full-capability run input = %q", got)
	}
	select {
	case got := <-restored:
		t.Fatalf("full-capability run unexpectedly restored read-only gate for %q", got)
	default:
	}
}

func TestCollaborationToolApprovalModeUsesOwningSessionPolicy(t *testing.T) {
	app, runtime, _ := newTestDesktopCollaboration(t)
	tab := testTab("approval-tab", t.TempDir())
	tab.SessionID = "session-a"
	defer tab.Ctrl.Close()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.trackSession(tab)
	runtime.state.SessionID = tab.SessionID

	state, err := app.UpdateCollaborationToolApprovalMode(UpdateCollaborationToolApprovalModeInput{SessionID: tab.SessionID, Mode: control.ToolApprovalYolo})
	if err != nil || state.ToolApprovalMode != control.ToolApprovalYolo || tab.toolApprovalMode != control.ToolApprovalYolo || tab.Ctrl.ToolApprovalMode() != control.ToolApprovalYolo {
		t.Fatalf("Room approval policy was not applied to owning Session: state=%q stored=%q controller=%q err=%v", state.ToolApprovalMode, tab.toolApprovalMode, tab.Ctrl.ToolApprovalMode(), err)
	}
	if _, err := app.UpdateCollaborationToolApprovalMode(UpdateCollaborationToolApprovalModeInput{SessionID: tab.SessionID, Mode: "invalid"}); err == nil {
		t.Fatal("invalid Room approval policy was accepted")
	}
}

func TestCollaborationAutoApprovalRestorePreservesOwnerChange(t *testing.T) {
	app := NewApp()
	tab := testTab("auto-tab", t.TempDir())
	tab.SessionID = "auto-session"
	defer tab.Ctrl.Close()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.trackSession(tab)
	tab.Ctrl.SetToolApprovalMode(control.ToolApprovalAsk)

	previous, err := app.prepareCollaborationAutoAgent(tab.SessionID)
	if err != nil || previous != control.ToolApprovalAsk || tab.Ctrl.ToolApprovalMode() != control.ToolApprovalAuto {
		t.Fatalf("prepare previous=%q current=%q err=%v", previous, tab.Ctrl.ToolApprovalMode(), err)
	}
	app.restoreCollaborationAutoAgent(tab.SessionID, previous)
	if got := tab.Ctrl.ToolApprovalMode(); got != control.ToolApprovalAsk {
		t.Fatalf("restored approval mode = %q", got)
	}

	previous, err = app.prepareCollaborationAutoAgent(tab.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	tab.Ctrl.SetToolApprovalMode(control.ToolApprovalYolo)
	app.restoreCollaborationAutoAgent(tab.SessionID, previous)
	if got := tab.Ctrl.ToolApprovalMode(); got != control.ToolApprovalYolo {
		t.Fatalf("owner approval change was overwritten with %q", got)
	}
}

func TestCollaborationAutoApprovalDoesNotLeakIntoPersistentState(t *testing.T) {
	app := NewApp()
	tab := testTab("auto-leak-tab", t.TempDir())
	tab.SessionID = "auto-leak-session"
	tab.toolApprovalMode = control.ToolApprovalYolo
	tab.mode = "yolo"
	defer tab.Ctrl.Close()
	app.tabs = map[string]*WorkspaceTab{tab.ID: tab}
	app.tabOrder = []string{tab.ID}
	app.activeTabID = tab.ID
	app.trackSession(tab)
	tab.Ctrl.SetToolApprovalMode(control.ToolApprovalYolo)

	// User persistent choice is yolo; verify it's reflected.
	if got := currentTabToolApprovalMode(tab); got != control.ToolApprovalYolo {
		t.Fatalf("initial currentTabToolApprovalMode = %q, want yolo", got)
	}
	if got := currentTabMode(tab); got != "yolo" {
		t.Fatalf("initial currentTabMode = %q, want yolo", got)
	}

	// An explicit YOLO choice is already more permissive than auto, so an
	// automatic delegation must inherit it instead of downgrading execution.
	previous, err := app.prepareCollaborationAutoAgent(tab.SessionID)
	if err != nil || previous != control.ToolApprovalYolo {
		t.Fatalf("prepare previous=%q err=%v", previous, err)
	}
	if tab.Ctrl.ToolApprovalMode() != control.ToolApprovalYolo {
		t.Fatalf("automatic delegation downgraded yolo to %q", tab.Ctrl.ToolApprovalMode())
	}
	if tab.autoAgentActive.Load() {
		t.Fatal("yolo automatic delegation unexpectedly installed a temporary auto override")
	}

	// Persistent paths must still report the user's persistent choice (yolo).
	if got := currentTabToolApprovalMode(tab); got != control.ToolApprovalYolo {
		t.Fatalf("currentTabToolApprovalMode leaked auto: got %q, want yolo", got)
	}
	if got := currentTabMode(tab); got != "yolo" {
		t.Fatalf("currentTabMode leaked auto-derived mode: got %q, want yolo", got)
	}
	recoveryMeta := app.tabSessionRecoveryMeta(tab)(control.SessionRecoveryRequest{})
	if recoveryMeta.ToolApprovalMode != control.ToolApprovalYolo || recoveryMeta.Mode != "yolo" {
		t.Fatalf("recovery metadata leaked temporary auto: mode=%q approval=%q", recoveryMeta.Mode, recoveryMeta.ToolApprovalMode)
	}
	tabMeta := app.tabMeta(tab, true)
	if tabMeta.ToolApprovalMode != control.ToolApprovalYolo || tabMeta.Mode != "yolo" {
		t.Fatalf("TabMeta leaked temporary auto: mode=%q approval=%q", tabMeta.Mode, tabMeta.ToolApprovalMode)
	}

	// Completion is a no-op for inherited YOLO.
	app.restoreCollaborationAutoAgent(tab.SessionID, previous)
	if tab.Ctrl.ToolApprovalMode() != control.ToolApprovalYolo {
		t.Fatalf("controller not restored: got %q, want yolo", tab.Ctrl.ToolApprovalMode())
	}
	if tab.autoAgentActive.Load() {
		t.Fatal("autoAgentActive not cleared after restore")
	}

	// Ask is elevated only for this automatic run; every observable and
	// persistent projection must continue to expose the owner's Ask choice.
	app.SetToolApprovalModeForTab(tab.ID, control.ToolApprovalAsk)
	previous, err = app.prepareCollaborationAutoAgent(tab.SessionID)
	if err != nil || previous != control.ToolApprovalAsk || tab.Ctrl.ToolApprovalMode() != control.ToolApprovalAuto {
		t.Fatalf("ask elevation previous=%q controller=%q err=%v", previous, tab.Ctrl.ToolApprovalMode(), err)
	}
	if got := currentTabToolApprovalMode(tab); got != control.ToolApprovalAsk {
		t.Fatalf("temporary auto leaked through Tab projection: got %q", got)
	}
	recoveryMeta = app.tabSessionRecoveryMeta(tab)(control.SessionRecoveryRequest{})
	// Ask/normal are intentionally omitted in BranchMeta as its persisted
	// defaults; temporary auto must not make either field non-empty.
	if recoveryMeta.ToolApprovalMode != "" || recoveryMeta.Mode != "" {
		t.Fatalf("recovery metadata leaked ask elevation: mode=%q approval=%q", recoveryMeta.Mode, recoveryMeta.ToolApprovalMode)
	}
	if !tab.autoAgentActive.Load() {
		t.Fatal("ask automatic delegation did not install its scoped auto override")
	}

	// Selecting auto while the temporary mode is also auto must still count as
	// an explicit owner change; equality with the temporary Controller value
	// cannot be used to infer that restoration is still allowed.
	app.SetToolApprovalModeForTab(tab.ID, control.ToolApprovalAuto)
	if tab.autoAgentActive.Load() {
		t.Fatal("explicit owner Auto did not clear the scoped override")
	}
	app.restoreCollaborationAutoAgent(tab.SessionID, previous)
	if tab.Ctrl.ToolApprovalMode() != control.ToolApprovalAuto || tab.toolApprovalMode != control.ToolApprovalAuto {
		t.Fatalf("restore overwrote explicit owner auto: controller=%q stored=%q", tab.Ctrl.ToolApprovalMode(), tab.toolApprovalMode)
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
	var ready atomic.Bool
	c.agentReady = func(string) (bool, error) { return ready.Load(), nil }
	workspaceReady := make(chan struct{})
	c.waitAgentReady = func(ctx context.Context, sessionID string) error {
		if sessionID != "session-a" {
			t.Fatalf("wait routed to %q", sessionID)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-workspaceReady:
			ready.Store(true)
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
	if queued := c.snapshot().QueuedTasks; len(queued) != 1 || queued[0].Instruction != input.Instruction {
		t.Fatalf("starting workspace task is not visible in queue: %+v", queued)
	}
	select {
	case value := <-submitted:
		t.Fatalf("Agent started before workspace was ready: %q", value)
	default:
	}
	close(workspaceReady)
	select {
	case value := <-submitted:
		if !strings.Contains(value, input.Instruction) || !strings.Contains(value, "<room-collaboration") {
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
	autoPrepares := 0
	c.prepareAutoAgent = func(sessionID string) (string, error) {
		if sessionID != "session-a" {
			t.Fatalf("prepared %q", sessionID)
		}
		autoPrepares++
		return control.ToolApprovalAsk, nil
	}
	c.submitAgent = func(sessionID, _, _ string) error {
		if sessionID != "session-a" {
			t.Fatalf("routed to %q", sessionID)
		}
		localSubmits++
		return nil
	}
	for _, requestID := range []string{"decision-1", "decision-2"} {
		if _, err := c.respond(context.Background(), RespondCollaborationRequestInput{RequestID: requestID, AgentRequestID: request.ID, Action: "accept", SessionID: "session-a", Automatic: true}); err != nil {
			t.Fatalf("respond %s: %v", requestID, err)
		}
	}
	if localSubmits != 1 || autoPrepares != 1 || c.runs["session-a"] == nil || !c.runs["session-a"].Automatic {
		t.Fatalf("local Agent submits=%d auto prepares=%d run=%+v", localSubmits, autoPrepares, c.runs["session-a"])
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
	c2.getSecret = func(key string) string { return secrets.get(key) }
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

func TestCollaborationV2SessionIDRepairPreservesCachedRoom(t *testing.T) {
	// A SessionPath-owned v2 persist file with a mismatched
	// internal SessionID must be repaired rather than rejected, so the
	// cached Room/Snapshot can render locally even when the network is down.
	_, c, _ := newTestDesktopCollaboration(t)
	persisted := collaborationPersistedState{
		Mode: "client", Host: "10.0.0.1", Port: 39171, Room: "shared-room",
		MemberID: "member-a", AgentID: "agent-a",
		SessionID: "stale-session-id",
		Snapshot: collab.Snapshot{
			Room:    collab.Room{ID: "shared-room", Name: "Shared Room", LatestSequence: 7},
			Members: []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Bot"}}},
			Timeline: []collab.TimelineItem{
				{ID: "msg-1", Sequence: 1, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "msg-1", AuthorID: "member-a", Text: "offline message", Revision: 1}},
			},
			LatestSequence: 7,
		},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	// c has ownerSessionID = "session-a" but the file has SessionID = "stale-session-id".
	c.loadPersisted()
	state := c.snapshot()
	if state.SessionID != "session-a" {
		t.Fatalf("v2 SessionID was not repaired: got %q", state.SessionID)
	}
	if state.Room != "shared-room" || state.Host != "10.0.0.1" || state.Port != 39171 || state.Mode != "client" {
		t.Fatalf("v2 cached Room not preserved: %+v", state)
	}
	if state.Snapshot.LatestSequence != 7 || len(state.Snapshot.Members) != 1 || len(state.Snapshot.Timeline) != 1 {
		t.Fatalf("v2 cached Snapshot not preserved: %+v", state.Snapshot)
	}
	if state.Status != "failed" || !state.Retryable {
		t.Fatalf("v2 repaired state must be failed+retryable: status=%s retryable=%v lastError=%q", state.Status, state.Retryable, state.LastError)
	}
}

func TestCollaborationRetryRepairsLateStaleSessionID(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	defer c.close()
	persisted := collaborationPersistedState{
		Mode: "host", Host: "127.0.0.1", Port: 39171, Room: "room-a", RoomName: "Room A",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent",
		SessionID: "stale-runtime-id",
		Runs:      []collaborationPersistedRun{{RunID: "run-a", SessionID: "stale-runtime-id"}},
		Queue:     []collaborationPersistedRun{{RunID: "queued-a", SessionID: "stale-runtime-id"}},
		Snapshot: collab.Snapshot{
			Room:    collab.Room{ID: "room-a", Name: "Room A"},
			Members: []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent"}}},
		},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.validateAgent = func(sessionID string) error {
		if sessionID != c.ownerSessionID {
			return fmt.Errorf("session %q does not own this collaboration runtime", sessionID)
		}
		return nil
	}
	var opened HostCollaborationRoomInput
	c.openHost = func(_ context.Context, input HostCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		opened = input
		return testConnection(&fakeCollaborationPeer{}, "host", input.SessionID), nil
	}

	state, err := c.retry(context.Background())
	if err != nil {
		t.Fatalf("retry rejected a stale persisted SessionID: %v", err)
	}
	if opened.SessionID != c.ownerSessionID || state.SessionID != c.ownerSessionID || state.Status != "connected" {
		t.Fatalf("retry identity did not converge: input=%q state=%+v", opened.SessionID, state)
	}
	repaired := c.repairPersisted(persisted)
	if repaired.SessionID != c.ownerSessionID || repaired.Runs[0].SessionID != c.ownerSessionID || repaired.Queue[0].SessionID != c.ownerSessionID {
		t.Fatalf("persisted Agent identities did not converge: %+v", repaired)
	}
}

func TestCollaborationRecoveryBranchUsesParentRoomPersistenceKey(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "room-session.jsonl")
	recovery := filepath.Join(dir, "room-session-recovery-1234.jsonl")
	for _, path := range []string{parent, recovery} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := agent.SaveBranchMeta(recovery, agent.BranchMeta{
		ID:            string(agent.BranchID(recovery)),
		ParentID:      string(agent.BranchID(parent)),
		Recovered:     true,
		RecoveryDepth: 1,
	}); err != nil {
		t.Fatal(err)
	}

	owner := collaborationOwnerSessionPath(recovery)
	if owner != sessionRuntimeKey(parent) {
		t.Fatalf("recovery Room owner path = %q, want parent %q", owner, sessionRuntimeKey(parent))
	}
	parentKey := collaborationPersistenceKey("parent-runtime", parent)
	recoveryKey := collaborationPersistenceKey("recovery-runtime", owner)
	if recoveryKey != parentKey {
		t.Fatalf("recovery Room persistence key = %q, want %q", recoveryKey, parentKey)
	}
}

func TestRestoreCollaborationRuntimesDeduplicatesRecoveryCaches(t *testing.T) {
	isolateDesktopUserDirs(t)
	stateDir := filepath.Join(os.Getenv("WorkGround2_STATE_HOME"), "desktop-collaboration-v2")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionDir := t.TempDir()
	parent := filepath.Join(sessionDir, "room-session.jsonl")
	recovery := filepath.Join(sessionDir, "room-session-recovery-1234.jsonl")
	for _, path := range []string{parent, recovery} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := agent.SaveBranchMeta(parent, agent.BranchMeta{
		ID: string(agent.BranchID(parent)), Scope: "global", TopicID: "topic-recovery-room",
		TopicTitle: "Recovery Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(recovery, agent.BranchMeta{
		ID:            string(agent.BranchID(recovery)),
		ParentID:      string(agent.BranchID(parent)),
		Recovered:     true,
		RecoveryDepth: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTopicIndexed("global", "", "topic-recovery-room", "Recovery Room", topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}

	sessionID := "session-recovery-room"
	baseCache := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(sessionID, parent)))
	recoveryCache := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(sessionID, recovery)))
	writeState := func(path string, value collaborationPersistedState) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeState(baseCache, collaborationPersistedState{
		Mode: "host", Host: "0.0.0.0", Port: 49853, Room: "stable-room",
		SessionID: sessionID, SessionPath: parent,
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "stable-room", Name: "Stable Room"}},
	})
	// This is the incomplete duplicate left by a pre-canonicalisation build.
	// The startup scan must not let it schedule a second reconnect.
	writeState(recoveryCache, collaborationPersistedState{
		SessionID: sessionID, SessionPath: recovery,
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "stable-room", Name: "Stable Room"}},
	})

	app := NewApp()
	tab := &WorkspaceTab{ID: "recovery-room", SessionID: sessionID, SessionPath: recovery}
	app.trackSession(tab)
	app.mu.Lock()
	app.tabs[tab.ID] = tab
	app.mu.Unlock()

	starts := 0
	var startedRuntime *desktopCollaboration
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	app.restoreCollaborationRuntimesWithRegistry(entries, stateDir, func(runtime *desktopCollaboration, restoredSessionID string) {
		starts++
		startedRuntime = runtime
		if restoredSessionID != sessionID {
			t.Fatalf("restored SessionID = %q, want %q", restoredSessionID, sessionID)
		}
	}, loadProjectsFile())
	if starts != 1 {
		t.Fatalf("startup restore starts = %d, want exactly 1 for duplicate owner caches", starts)
	}
	app.collaborationMu.Lock()
	runtime := app.collaborations[sessionID]
	app.collaborationMu.Unlock()
	if runtime == nil || runtime != startedRuntime {
		t.Fatalf("restored runtime = %p, callback runtime = %p", runtime, startedRuntime)
	}
	if runtime.ownerSessionPath != sessionRuntimeKey(parent) {
		t.Fatalf("runtime owner path = %q, want %q", runtime.ownerSessionPath, sessionRuntimeKey(parent))
	}
	state := runtime.snapshot()
	if state.Mode != "host" || state.Host != "0.0.0.0" || state.Port != 49853 || state.Room != "stable-room" {
		t.Fatalf("restored Host authority = %+v", state)
	}
}

func TestRestoreCollaborationRuntimesKeepsCompleteOffTabRoomResidentForUnread(t *testing.T) {
	supportDir := isolateDesktopUserDirs(t)
	stateDir := filepath.Join(os.Getenv("WorkGround2_STATE_HOME"), "desktop-collaboration-v2")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(supportDir, "projects", "room-project", "sessions", "off-tab-room.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	workspaceRoot := filepath.Dir(filepath.Dir(sessionPath))
	if err := agent.SaveBranchMeta(sessionPath, agent.BranchMeta{
		ID: "off-tab-room", Scope: "project", WorkspaceRoot: workspaceRoot,
		TopicID: "topic-off-tab", TopicTitle: "Off-tab Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTopicIndexed("project", workspaceRoot, "topic-off-tab", "Off-tab Room", topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	sessionID := "session-off-tab-room"
	createdAt := time.Date(2026, 8, 21, 9, 0, 0, 0, time.UTC)
	persisted := collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Port: 49853, Room: "room-off-tab", RoomName: "Off-tab Room",
		MemberID: "self", AgentID: "self-agent", SessionID: sessionID, SessionPath: sessionPath,
		Snapshot: collab.Snapshot{
			Room:           collab.Room{ID: "room-off-tab", Name: "Off-tab Room", CreatedAt: createdAt, LatestSequence: 1},
			LatestSequence: 1,
			Timeline: []collab.TimelineItem{{
				ID: "baseline", Sequence: 1, Type: collab.TimelineChat,
				Chat: &collab.ChatMessage{ID: "baseline", AuthorID: "other", Text: "cached baseline", CreatedAt: createdAt},
			}},
		},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	persistPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(sessionID, sessionPath)))
	if err := os.WriteFile(persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	stalePath := filepath.Join(filepath.Dir(sessionPath), "left-room.jsonl")
	if err := os.WriteFile(stalePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(stalePath, agent.BranchMeta{
		ID: "left-room", Scope: "project", WorkspaceRoot: filepath.Dir(filepath.Dir(stalePath)),
		TopicID: "topic-left", TopicTitle: "Left Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	stale := persisted
	stale.SessionID = "session-left-room"
	stale.SessionPath = stalePath
	stale.Room = "room-left"
	staleData, err := json.Marshal(stale)
	if err != nil {
		t.Fatal(err)
	}
	stalePersistPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(stale.SessionID, stalePath)))
	if err := os.WriteFile(stalePersistPath, staleData, 0o600); err != nil {
		t.Fatal(err)
	}

	store, err := unread.Open(filepath.Join(supportDir, "unread-test.json"))
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	app.unreadStore = store
	starts := 0
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	app.restoreCollaborationRuntimesWithRegistry(entries, stateDir, func(runtime *desktopCollaboration, restoredSessionID string) {
		starts++
		if restoredSessionID != sessionID {
			t.Fatalf("restored SessionID = %q, want %q", restoredSessionID, sessionID)
		}
	}, loadProjectsFile())
	defer app.closeCollaborations()

	app.collaborationMu.Lock()
	runtime := app.collaborations[sessionID]
	staleRuntime := app.collaborations[stale.SessionID]
	app.collaborationMu.Unlock()
	if starts != 1 || runtime == nil || staleRuntime != nil {
		t.Fatalf("off-tab Room restore starts=%d live=%p stale=%p", starts, runtime, staleRuntime)
	}
	if runtime.ownerSessionPath != sessionRuntimeKey(sessionPath) || runtime.persistPath != persistPath {
		t.Fatalf("off-tab Room identity path=%q persist=%q, want %q and %q", runtime.ownerSessionPath, runtime.persistPath, sessionRuntimeKey(sessionPath), persistPath)
	}
	if got := app.UnreadState().Summary; got.TotalUnread != 0 || len(got.Conversations) != 1 || got.Conversations[0].LatestSequence != 1 {
		t.Fatalf("cached Room baseline = %+v", got)
	}

	runtime.mu.Lock()
	runtime.state.Snapshot.LatestSequence = 2
	runtime.state.Snapshot.Room.LatestSequence = 2
	runtime.state.Snapshot.Timeline = append(runtime.state.Snapshot.Timeline, collab.TimelineItem{
		ID: "new-off-tab", Sequence: 2, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "new-off-tab", AuthorID: "other", Text: "new while tab closed", CreatedAt: createdAt.Add(time.Minute)},
	})
	runtime.mu.Unlock()
	runtime.observeUnread()
	got := app.UnreadState().Summary
	if got.TotalUnread != 1 || len(got.Conversations) != 1 || got.Conversations[0].SessionID != sessionID || got.Conversations[0].Items[0].ID != "new-off-tab" {
		t.Fatalf("off-tab Room unread = %+v", got)
	}
}

func seedRestorableHostRoom(t *testing.T, root string) string {
	t.Helper()
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	sessionDir := desktopSessionDir(root)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := agent.NewSessionPath(sessionDir, "late-host-room")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(sessionPath, agent.BranchMeta{
		ID: string(agent.BranchID(sessionPath)), Scope: "project", WorkspaceRoot: root,
		TopicID: "topic-late-host", TopicTitle: "Late Host", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTopicIndexed("project", root, "topic-late-host", "Late Host", topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(os.Getenv("WorkGround2_STATE_HOME"), "desktop-collaboration-v2")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionID := "session-late-host"
	persisted := collaborationPersistedState{
		Mode: "host", Host: "127.0.0.1", Room: "room-late-host", RoomName: "Late Host",
		MemberID: "host-member", MemberName: "Host", AgentID: "host-agent", AgentName: "Host Agent",
		SessionID: sessionID, SessionPath: sessionPath, LANEnabled: true, ReachabilityVersion: 1, ProtocolVersion: collaborationProtocolV2,
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-late-host", Name: "Late Host"}},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	persistPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey(sessionID, sessionPath)))
	if err := os.WriteFile(persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return sessionID
}

func TestCollaborationRuntimeRestoreActivatesHostWithoutProjectTreeProjection(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	sessionID := seedRestorableHostRoom(t, root)

	app := NewApp()
	app.sessionDirsOverride = []string{filepath.Join(root, "project-tree-cache-not-ready")}
	defer app.closeCollaborations()
	// The project-tree directory is deliberately unavailable. Durable Session
	// metadata plus the Topic registry must be sufficient to activate the Host.
	app.restoreCollaborationRuntimesWith(app.startCollaborationRestore)

	var runtime *desktopCollaboration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		app.collaborationMu.Lock()
		runtime = app.collaborations[sessionID]
		app.collaborationMu.Unlock()
		if runtime != nil && runtime.snapshot().Status == "connected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runtime == nil || runtime.snapshot().Mode != "host" || runtime.snapshot().Status != "connected" {
		t.Fatalf("late Host Room was not activated: runtime=%p state=%+v", runtime, func() CollaborationState {
			if runtime == nil {
				return CollaborationState{}
			}
			return runtime.snapshot()
		}())
	}
	probe, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", runtime.snapshot().Port), time.Second)
	if err != nil {
		t.Fatalf("restored Host listener is unavailable: %v", err)
	}
	_ = probe.Close()
	first := runtime
	app.collaborationReconcileMu.Lock()
	app.collaborationReconcileEnabled = true
	app.collaborationReconcileMu.Unlock()
	app.scheduleCollaborationRuntimeReconcile()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		app.collaborationReconcileMu.Lock()
		running := app.collaborationReconcileRunning
		app.collaborationReconcileMu.Unlock()
		if !running {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	app.collaborationMu.Lock()
	runtime = app.collaborations[sessionID]
	app.collaborationMu.Unlock()
	if runtime != first {
		t.Fatalf("idempotent reconcile replaced Host runtime: first=%p next=%p", first, runtime)
	}
}

func TestRestoreOrBuildTabsRestoresCollaborationHostWithoutPersistedTabs(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	sessionID := seedRestorableHostRoom(t, root)
	if tabs := loadTabsFile(); len(tabs.Tabs) != 0 {
		t.Fatalf("persisted tabs = %d, want empty startup state", len(tabs.Tabs))
	}

	app := NewApp()
	app.sessionDirsOverride = []string{filepath.Join(root, "project-tree-cache-not-ready")}
	defer app.closeCollaborations()

	app.restoreOrBuildTabs()

	var runtime *desktopCollaboration
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		app.collaborationMu.Lock()
		runtime = app.collaborations[sessionID]
		app.collaborationMu.Unlock()
		if runtime != nil && runtime.snapshot().Status == "connected" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if runtime == nil || runtime.snapshot().Status != "connected" {
		t.Fatalf("empty-tab startup did not activate Host Room: runtime=%p state=%+v", runtime, func() CollaborationState {
			if runtime == nil {
				return CollaborationState{}
			}
			return runtime.snapshot()
		}())
	}
	probe, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", runtime.snapshot().Port), time.Second)
	if err != nil {
		t.Fatalf("empty-tab startup Host listener is unavailable: %v", err)
	}
	_ = probe.Close()
}

func TestCollaborationMigratesRecoveryPathCacheToOwnerPath(t *testing.T) {
	stateDir := t.TempDir()
	sessionDir := t.TempDir()
	parent := filepath.Join(sessionDir, "room-session.jsonl")
	recovery := filepath.Join(sessionDir, "room-session-recovery-1234.jsonl")
	for _, path := range []string{parent, recovery} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := agent.SaveBranchMeta(recovery, agent.BranchMeta{
		ID:            string(agent.BranchID(recovery)),
		ParentID:      string(agent.BranchID(parent)),
		Recovered:     true,
		RecoveryDepth: 1,
	}); err != nil {
		t.Fatal(err)
	}

	targetPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey("current", parent)))
	oldPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey("current", recovery)))
	value := collaborationPersistedState{
		Mode: "host", Host: "0.0.0.0", Port: 49853, Room: "stable-room",
		SessionID: "current", SessionPath: recovery,
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "stable-room", Name: "Stable Room"}},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	c := &desktopCollaboration{
		ownerSessionID: "current", ownerSessionPath: sessionRuntimeKey(parent), ownerSessionTitle: "Stable Room",
		state:  CollaborationState{Status: "disconnected", SessionID: "current"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, shares: map[string]collaborationSharedFile{},
		transfers: map[string]*CollaborationFileTransfer{}, outboxFailures: map[string]string{}, persistPath: targetPath, writeState: fileutil.AtomicWriteFile,
	}
	c.loadPersisted()
	state := c.snapshot()
	if state.Mode != "host" || state.Room != "stable-room" || state.SessionID != "current" {
		t.Fatalf("migrated Host state = %+v", state)
	}
	if sessionRuntimeKey(c.readPersisted().SessionPath) != sessionRuntimeKey(parent) {
		t.Fatalf("migrated cache path = %q, want %q", c.readPersisted().SessionPath, parent)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("recovery-path cache still exists: %v", err)
	}
}

func TestCollaborationOldV2CacheMigratesToStableSessionPath(t *testing.T) {
	stateDir := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "sessions", "room-session.jsonl")
	oldPath := filepath.Join(stateDir, collaborationSessionStateName("old-runtime-session"))
	targetPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey("current-runtime-session", sessionPath)))
	value := collaborationPersistedState{
		Mode: "client", Host: "192.168.6.85", Port: 60268, Room: "Chat_room_t2",
		RoomName: "瞎搞区无审核", MemberID: "member-a", AgentID: "agent-a", SessionID: "old-runtime-session",
		Snapshot: collab.Snapshot{
			Room:           collab.Room{ID: "Chat_room_t2", Name: "瞎搞区无审核", LatestSequence: 5},
			Members:        []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Bot"}}},
			Timeline:       []collab.TimelineItem{{ID: "msg-1", Sequence: 1, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "msg-1", AuthorID: "member-a", Text: "cached", Revision: 1}}},
			LatestSequence: 5,
		},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	newRuntime := func(sessionID string) *desktopCollaboration {
		return &desktopCollaboration{
			ownerSessionID: sessionID, ownerSessionPath: sessionRuntimeKey(sessionPath), ownerSessionTitle: "瞎搞区无审核",
			state:  CollaborationState{Status: "disconnected", SessionID: sessionID},
			starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, shares: map[string]collaborationSharedFile{},
			transfers: map[string]*CollaborationFileTransfer{}, outboxFailures: map[string]string{}, persistPath: targetPath, writeState: fileutil.AtomicWriteFile,
		}
	}
	c := newRuntime("current-runtime-session")
	c.loadPersisted()
	state := c.snapshot()
	if state.Room != "Chat_room_t2" || state.SessionID != "current-runtime-session" || len(state.Snapshot.Timeline) != 1 {
		t.Fatalf("migrated state = %+v", state)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("old SessionID-keyed cache still exists: %v", err)
	}
	persisted := c.readPersisted()
	if sessionRuntimeKey(persisted.SessionPath) != sessionRuntimeKey(sessionPath) || persisted.SessionID != "current-runtime-session" {
		t.Fatalf("stable cache identity = path %q session %q", persisted.SessionPath, persisted.SessionID)
	}

	// Reopening the same saved Session gets another runtime ID but resolves the
	// same stable cache and repairs only the runtime routing identity.
	c2 := newRuntime("next-runtime-session")
	c2.loadPersisted()
	state = c2.snapshot()
	if state.Room != "Chat_room_t2" || state.SessionID != "next-runtime-session" || len(state.Snapshot.Members) != 1 {
		t.Fatalf("reopened state = %+v", state)
	}
	if got := c2.readPersisted().SessionID; got != "next-runtime-session" {
		t.Fatalf("reopened runtime identity was not persisted: %q", got)
	}
}

func TestCollaborationOldV2MigrationRejectsAmbiguousTitle(t *testing.T) {
	stateDir := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "sessions", "room-session.jsonl")
	for _, oldSessionID := range []string{"old-a", "old-b"} {
		value := collaborationPersistedState{
			Mode: "client", Room: "room-" + oldSessionID, RoomName: "Same Room", SessionID: oldSessionID,
			Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-" + oldSessionID, Name: "Same Room"}},
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stateDir, collaborationSessionStateName(oldSessionID)), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targetPath := filepath.Join(stateDir, collaborationSessionStateName(collaborationPersistenceKey("current", sessionPath)))
	c := &desktopCollaboration{
		ownerSessionID: "current", ownerSessionPath: sessionRuntimeKey(sessionPath), ownerSessionTitle: "Same Room",
		state:  CollaborationState{Status: "disconnected", SessionID: "current"},
		starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}, shares: map[string]collaborationSharedFile{},
		transfers: map[string]*CollaborationFileTransfer{}, outboxFailures: map[string]string{}, persistPath: targetPath, writeState: fileutil.AtomicWriteFile,
	}
	c.loadPersisted()
	state := c.snapshot()
	if state.Room != "" || !state.Retryable || !strings.Contains(state.LastError, "multiple old Room caches") {
		t.Fatalf("ambiguous migration state = %+v", state)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("ambiguous migration unexpectedly created target: %v", err)
	}
}

func TestCollaborationLegacyMigrationRejectsMismatchedSessionID(t *testing.T) {
	// Legacy global file (desktop-collaboration-v1.json) with a
	// non-empty SessionID that does not match the current runtime
	// must NOT be migrated — it belongs to a different session.
	_, c, _ := newTestDesktopCollaboration(t)
	c.legacyPersistPath = c.persistPath + ".legacy"
	c.persistPath = c.persistPath + ".v2"
	// The v2 file does not exist → fallback to legacy.
	legacy := collaborationPersistedState{
		Mode: "host", Host: "127.0.0.1", Port: 39170, Room: "other-room",
		MemberID: "other-member", AgentID: "other-agent",
		SessionID: "other-session",
		Snapshot: collab.Snapshot{
			Room:    collab.Room{ID: "other-room", Name: "Other Room"},
			Members: []collab.Member{{ID: "other-member", Name: "Bob", Agent: collab.AgentDescriptor{ID: "other-agent", Name: "Bot"}}},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.legacyPersistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadPersisted()
	state := c.snapshot()
	// Must NOT adopt the legacy Room — cross-session guard fires.
	if state.Room != "" || state.Host != "" || state.Mode != "" {
		t.Fatalf("legacy cross-session data leaked: %+v", state)
	}
	// SessionID must stay as ownerSessionID (not the legacy value).
	if state.SessionID != "session-a" {
		t.Fatalf("legacy rejection must preserve owner SessionID: got %q", state.SessionID)
	}
	// The v2 file must NOT have been written (migration skipped).
	if _, err := os.Stat(c.persistPath); err == nil {
		t.Fatal("v2 file was written despite legacy SessionID mismatch")
	}
}

func TestCollaborationRetryRepairsLegacyHostIdentityFromSnapshot(t *testing.T) {
	_, c, secrets := newTestDesktopCollaboration(t)
	connectionRef := collaborationSecretRef("127.0.0.1", 39170, "room-a", "member-a")
	secrets.put(connectionRef, "cs1.recover-me")
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
	post := PostCollaborationMessageInput{RequestID: "offline-chat", SessionID: "session-a", Kind: "chat", Text: "local update", MentionAgentIDs: []string{"agent-a"}, MentionMemberIDs: []string{"member-b"}}
	first, err := c.post(context.Background(), post)
	if err != nil || !first.Queued || first.Duplicate || first.Item == nil || first.Item.ID != "outbox:offline-chat" || first.Item.Chat == nil || first.Item.Chat.Text != "local update" || !equalStrings(first.Item.Chat.MentionAgentIDs, []string{"agent-a"}) || !equalStrings(first.Item.Chat.MentionMemberIDs, []string{"member-b"}) {
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
	c2.setSecret = func(key, value string) error { secrets.put(key, value); return nil }
	c2.getSecret = func(key string) string { return secrets.get(key) }
	c2.removeSecret = func(key string) error { secrets.del(key); return nil }
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

func TestCollaborationPersistRecoversAfterTransientEmptyFile(t *testing.T) {
	// Simulate a concurrent writer that briefly left the file empty.
	// The reader must retry and recover once the writer completes.
	oldInterval, oldMax := persistReadRetryInterval, persistReadMaxRetries
	persistReadRetryInterval, persistReadMaxRetries = time.Millisecond, 5
	t.Cleanup(func() { persistReadRetryInterval, persistReadMaxRetries = oldInterval, oldMax })

	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	conn.initialSnapshot.Timeline = []collab.TimelineItem{{
		ID: "cached-msg", Sequence: 1, Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: "cached-msg", AuthorID: "member-a", Text: "offline", Revision: 1},
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.mu.Lock()
	c.persistLocked()
	c.mu.Unlock()
	c.close()

	validData, err := os.ReadFile(c.persistPath)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate to empty — simulate the old copyOnto WriteFile truncation.
	if err := os.WriteFile(c.persistPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	// After a short delay the "writer" completes and writes valid data.
	go func() {
		time.Sleep(2 * time.Millisecond)
		os.WriteFile(c.persistPath, validData, 0o600)
	}()

	c2 := &desktopCollaboration{
		state:          CollaborationState{Status: "disconnected"},
		starts:         map[string]collaborationStartRecord{},
		runs:           map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
		transfers:      map[string]*CollaborationFileTransfer{},
		shares:         map[string]collaborationSharedFile{},
		persistPath:    c.persistPath,
		ownerSessionID: "session-a",
	}
	c2.loadPersisted()
	state := c2.snapshot()
	if state.Room != conn.room || state.SessionID != conn.sessionID ||
		state.Snapshot.LatestSequence < 1 || len(state.Snapshot.Timeline) < 1 {
		t.Fatalf("recovery failed after transient empty: state=%+v lastError=%q", state, state.LastError)
	}
}

func TestCollaborationPersistRecoversAfterUnexpectedEOFWithoutPartialState(t *testing.T) {
	oldInterval, oldMax := persistReadRetryInterval, persistReadMaxRetries
	persistReadRetryInterval, persistReadMaxRetries = time.Millisecond, 5
	t.Cleanup(func() { persistReadRetryInterval, persistReadMaxRetries = oldInterval, oldMax })

	path := filepath.Join(t.TempDir(), "room.json")
	if err := os.WriteFile(path, []byte(`{"RoomName":"stale","Room":`), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(2 * time.Millisecond)
		_ = os.WriteFile(path, []byte(`{"Room":"room-a"}`), 0o600)
	}()

	var got collaborationPersistedState
	if err := readPersistFile(path, &got); err != nil {
		t.Fatal(err)
	}
	if got.Room != "room-a" || got.RoomName != "" {
		t.Fatalf("recovered state retained partial decode: %+v", got)
	}
}

func TestCollaborationPersistFailsAfterPersistentEmptyFile(t *testing.T) {
	// A persistently empty file must eventually report a retryable error.
	oldInterval, oldMax := persistReadRetryInterval, persistReadMaxRetries
	persistReadRetryInterval, persistReadMaxRetries = time.Millisecond, 2
	t.Cleanup(func() { persistReadRetryInterval, persistReadMaxRetries = oldInterval, oldMax })

	_, c, _ := newTestDesktopCollaboration(t)
	if err := os.WriteFile(c.persistPath, []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	c2 := &desktopCollaboration{
		state:          CollaborationState{Status: "disconnected"},
		starts:         map[string]collaborationStartRecord{},
		runs:           map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
		transfers:      map[string]*CollaborationFileTransfer{},
		shares:         map[string]collaborationSharedFile{},
		persistPath:    c.persistPath,
	}
	c2.loadPersisted()
	state := c2.snapshot()
	if !state.Retryable {
		t.Fatal("persistent empty file must be retryable")
	}
	if !strings.Contains(state.LastError, "empty") {
		t.Fatalf("error must mention empty file: %q", state.LastError)
	}
}

func TestCollaborationPersistStableErrorReturnsImmediately(t *testing.T) {
	// A stable parse error (not truncation) must return immediately
	// without wasting time on retries.
	oldInterval := persistReadRetryInterval
	persistReadRetryInterval = time.Second // very slow if retried
	t.Cleanup(func() { persistReadRetryInterval = oldInterval })

	_, c, _ := newTestDesktopCollaboration(t)
	// Valid JSON followed by garbage — not a truncation, just corruption.
	if err := os.WriteFile(c.persistPath, []byte(`{"Mode":"client"}-trailing-`), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	c2 := &desktopCollaboration{
		state:          CollaborationState{Status: "disconnected"},
		starts:         map[string]collaborationStartRecord{},
		runs:           map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
		transfers:      map[string]*CollaborationFileTransfer{},
		shares:         map[string]collaborationSharedFile{},
		persistPath:    c.persistPath,
	}
	c2.loadPersisted()
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("stable syntax error must not retry; took %v", elapsed)
	}
	state := c2.snapshot()
	if !state.Retryable || !strings.Contains(state.LastError, "load collaboration state") {
		t.Fatalf("state = %+v", state)
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

func TestCollaborationRoomUpdateDelayIsLowFrequencyAndJittered(t *testing.T) {
	for _, sessionID := range []string{"session-a", "session-b", "session-c"} {
		delay := collaborationRoomUpdateDelay(sessionID)
		if delay < collaborationUpdateInterval-collaborationUpdateJitter || delay > collaborationUpdateInterval+collaborationUpdateJitter {
			t.Fatalf("update delay for %q = %v, want bounded around %v", sessionID, delay, collaborationUpdateInterval)
		}
	}
	if collaborationRoomUpdateDelay("session-a") == collaborationRoomUpdateDelay("session-b") {
		t.Fatal("Room update delay has no per-Session jitter")
	}
	for _, sessionID := range []string{"session-a", "session-b", "session-c"} {
		delay := collaborationInitialUpdateDelay(sessionID)
		if delay < collaborationInitialUpdateMin || delay >= collaborationInitialUpdateMin+collaborationInitialUpdateSpread {
			t.Fatalf("initial update delay for %q = %v, want short bounded spread", sessionID, delay)
		}
	}
	if collaborationInitialUpdateDelay("session-a") == collaborationInitialUpdateDelay("session-b") {
		t.Fatal("initial Room update delay has no per-Session jitter")
	}
}

func TestCollaborationAutomaticUpdateRestoresPersistedClient(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	persisted := collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Port: 39170, Room: "room-a",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent", SessionID: "session-a",
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-a", Name: "Room A"}},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadPersisted()

	calls := 0
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		calls++
		if calls == 1 {
			return nil, &collaborationTransportError{message: "Room host is temporarily unavailable", retryable: true}
		}
		return testConnection(&fakeCollaborationPeer{}, "client", input.SessionID), nil
	}
	if err := c.updateConnection(context.Background()); err == nil {
		t.Fatal("first automatic update succeeded unexpectedly")
	}
	failed := c.snapshot()
	if failed.Status != "failed" || !failed.Retryable || !strings.Contains(failed.LastError, "temporarily unavailable") {
		t.Fatalf("failed automatic update state = %+v", failed)
	}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatalf("second automatic update: %v", err)
	}
	if state := c.snapshot(); calls != 2 || state.Status != "connected" || c.conn == nil {
		t.Fatalf("restored calls=%d state=%+v conn=%p", calls, state, c.conn)
	}
	c.close()
}

func TestCollaborationAutomaticUpdateRejoinsAfterHostInvalidatesSession(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	first := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	if _, err := c.installConnection(first); err != nil {
		t.Fatal(err)
	}

	c.markReconnect(first, &collab.Error{Code: collab.CodeUnauthorized, Message: "connection session expired after Host restart", Retryable: true})
	if state := c.snapshot(); c.conn != nil || state.Status != "failed" || !state.Retryable {
		t.Fatalf("invalidated connection was not released for recovery: conn=%p state=%+v", c.conn, state)
	}

	joins := 0
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		joins++
		if input.Room != first.room || input.SessionID != first.sessionID || resume != first.connectionSession {
			t.Fatalf("Host restart recovery input=%+v resume=%q", input, resume)
		}
		return testConnection(&fakeCollaborationPeer{}, "client", input.SessionID), nil
	}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatalf("recover after Host restart: %v", err)
	}
	if state := c.snapshot(); joins != 1 || c.conn == nil || state.Status != "connected" {
		t.Fatalf("Host restart recovery joins=%d conn=%p state=%+v", joins, c.conn, state)
	}
	c.close()
}

func TestCollaborationAutomaticUpdateHealthyConnectionHasNoRemoteActivation(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	conn.done = make(chan struct{})
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	var joins, hosts atomic.Int32
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		joins.Add(1)
		return nil, errors.New("healthy update must not join")
	}
	c.openHost = func(context.Context, HostCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		hosts.Add(1)
		return nil, errors.New("healthy update must not host")
	}
	for range 5 {
		if err := c.updateConnection(context.Background()); err != nil {
			t.Fatalf("healthy update: %v", err)
		}
	}
	if joins.Load() != 0 || hosts.Load() != 0 {
		t.Fatalf("healthy updates activated Room: joins=%d hosts=%d", joins.Load(), hosts.Load())
	}
	peer := conn.peer.(*fakeCollaborationPeer)
	peer.mu.Lock()
	eventCalls := peer.eventCalls
	peer.mu.Unlock()
	if eventCalls != 5 {
		t.Fatalf("healthy updates pulled events %d times, want one bounded reconcile per tick", eventCalls)
	}
	close(conn.done)
}

func TestCollaborationAutomaticUpdateRestartsStoppedLoopInPlaceOnce(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	stopped := make(chan struct{})
	close(stopped)
	conn.done = stopped
	c.conn = conn
	c.state = CollaborationState{
		Status: "reconnecting", Retryable: true, Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	var joins, hosts atomic.Int32
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		joins.Add(1)
		return nil, errors.New("stopped loop repair must not join")
	}
	c.openHost = func(context.Context, HostCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		hosts.Add(1)
		return nil, errors.New("stopped loop repair must not host")
	}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatalf("restart stopped loop: %v", err)
	}
	restarted := conn.done
	if restarted == stopped || !collaborationConnectionLoopRunning(conn) {
		t.Fatalf("stopped loop was not restarted in place: old=%p new=%p", stopped, restarted)
	}
	for range 4 {
		if err := c.updateConnection(context.Background()); err != nil {
			t.Fatalf("idempotent stopped-loop update: %v", err)
		}
	}
	if conn.done != restarted || joins.Load() != 0 || hosts.Load() != 0 {
		t.Fatalf("repeated updates replaced connection: sameLoop=%v joins=%d hosts=%d", conn.done == restarted, joins.Load(), hosts.Load())
	}
	conn.cancel()
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("restarted loop did not stop")
	}
}

func TestCollaborationAutomaticUpdateRestoresMissingConnectionOnce(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.state = CollaborationState{
		Status: "failed", Retryable: true, Mode: "client", Host: "127.0.0.1", Port: 39170, Room: "room-a",
		MemberID: "member-a", AgentID: "agent-a", SessionID: "session-a",
	}
	persisted := collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Port: 39170, Room: "room-a",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent", SessionID: "session-a",
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-a", Name: "Room A"}},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var joins, hosts atomic.Int32
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		joins.Add(1)
		return testConnection(&fakeCollaborationPeer{}, "client", input.SessionID), nil
	}
	c.openHost = func(context.Context, HostCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		hosts.Add(1)
		return nil, errors.New("client recovery must not host")
	}
	for range 5 {
		if err := c.updateConnection(context.Background()); err != nil {
			t.Fatalf("recover missing connection: %v", err)
		}
	}
	if joins.Load() != 1 || hosts.Load() != 0 {
		t.Fatalf("missing connection recovery calls: joins=%d hosts=%d", joins.Load(), hosts.Load())
	}
	c.close()
}

func TestCollaborationStreamRetriesDoNotJoinForTransientEnd(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &scriptedStreamPeer{
		streamErrors: []error{
			&collaborationTransportError{message: "short stream reset 1", retryable: true},
			&collaborationTransportError{message: "short stream reset 2", retryable: true},
		},
		streamCalled: make(chan int, 8),
	}
	conn := testConnection(peer, "client", "session-a")
	conn.routes = []CollaborationRouteState{{
		CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: "127.0.0.1", Port: 39170}, Status: "connected", Active: true,
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "client", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.streamRetryDelay = func(int, uint64) time.Duration { return time.Millisecond }
	var joins atomic.Int32
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		joins.Add(1)
		return nil, errors.New("single-route stream retry must not join")
	}
	c.opMu.Lock()
	c.ensureConnectionLoop(conn)
	c.opMu.Unlock()
	deadline := time.After(time.Second)
	for calls := 0; calls < 3; {
		select {
		case calls = <-peer.streamCalled:
		case <-deadline:
			t.Fatal("timed out waiting for in-place stream retries")
		}
	}
	if joins.Load() != 0 {
		t.Fatalf("transient stream retries joined Room %d times", joins.Load())
	}
	conn.cancel()
	<-conn.done
}

func TestCollaborationStreamFailoverJoinsDifferentRouteOnce(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &scriptedStreamPeer{
		streamErrors: []error{
			&collaborationTransportError{message: "active route down 1", retryable: true},
			&collaborationTransportError{message: "active route down 2", retryable: true},
			&collaborationTransportError{message: "active route down 3", retryable: true},
		},
		streamCalled: make(chan int, 8),
	}
	conn := testConnection(peer, "client", "session-a")
	conn.routes = []CollaborationRouteState{
		{CollaborationRouteInput: CollaborationRouteInput{ID: "lan-primary", Kind: "lan", Host: "127.0.0.1", Port: 39170}, Status: "connected", Active: true},
		{CollaborationRouteInput: CollaborationRouteInput{ID: "lan-backup", Kind: "lan", Host: "127.0.0.2", Port: 39171}, Status: "disabled"},
	}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "client", Host: conn.hostName, Port: conn.port, Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.streamRetryDelay = func(int, uint64) time.Duration { return time.Millisecond }
	joined := make(chan JoinCollaborationRoomInput, 2)
	var joins atomic.Int32
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		joins.Add(1)
		joined <- input
		replacement := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		replacement.hostName, replacement.port = input.Routes[0].Host, input.Routes[0].Port
		replacement.routes = []CollaborationRouteState{{CollaborationRouteInput: input.Routes[0], Status: "connected", Active: true}}
		return replacement, nil
	}
	c.opMu.Lock()
	c.ensureConnectionLoop(conn)
	c.opMu.Unlock()
	var input JoinCollaborationRoomInput
	select {
	case input = <-joined:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for alternative route failover")
	}
	if len(input.Routes) != 1 || input.Routes[0].Host != "127.0.0.2" {
		t.Fatalf("failover routes = %+v, want backup only", input.Routes)
	}
	time.Sleep(20 * time.Millisecond)
	if joins.Load() != 1 {
		t.Fatalf("route failover joined %d times, want once", joins.Load())
	}
	c.close()
}

func TestCollaborationStreamFailureRedialsOnlyActiveRoute(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &scriptedStreamPeer{
		streamErrors: []error{
			&collaborationTransportError{message: "write Relay frame: broken pipe 1", retryable: true},
			&collaborationTransportError{message: "write Relay frame: broken pipe 2", retryable: true},
			&collaborationTransportError{message: "write Relay frame: broken pipe 3", retryable: true},
		},
		streamCalled: make(chan int, 8),
	}
	conn := testConnection(peer, "client", "session-a")
	conn.routes = []CollaborationRouteState{{
		CollaborationRouteInput: CollaborationRouteInput{ID: "relay", Kind: "relay", RelayID: "public", URL: "wss://relay.example.test", TunnelID: "tunnel-a"},
		Status:                  "connected",
		Active:                  true,
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "client", Room: conn.room, MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot}
	c.streamRetryDelay = func(int, uint64) time.Duration { return time.Millisecond }
	joined := make(chan JoinCollaborationRoomInput, 1)
	var joins atomic.Int32
	c.openJoin = func(_ context.Context, input JoinCollaborationRoomInput, _ collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
		joins.Add(1)
		if resume != conn.connectionSession {
			t.Fatalf("resume session = %q, want %q", resume, conn.connectionSession)
		}
		joined <- input
		replacement := testConnection(&fakeCollaborationPeer{}, "client", input.SessionID)
		replacement.routes = []CollaborationRouteState{{CollaborationRouteInput: input.Routes[0], Status: "connected", Active: true}}
		return replacement, nil
	}
	c.opMu.Lock()
	c.ensureConnectionLoop(conn)
	c.opMu.Unlock()
	var input JoinCollaborationRoomInput
	select {
	case input = <-joined:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for active Relay redial")
	}
	if len(input.Routes) != 1 || input.Routes[0].URL != "wss://relay.example.test" {
		t.Fatalf("redial routes = %+v, want active Relay", input.Routes)
	}
	time.Sleep(20 * time.Millisecond)
	if joins.Load() != 1 {
		t.Fatalf("active Relay redial joined %d times, want once", joins.Load())
	}
	c.close()
}

func TestCollaborationAutomaticUpdatePreservesManualOutboxFailures(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	env := collab.CommandEnvelope{
		RequestID: "manual-retry-only", Room: conn.room, MemberID: conn.memberID,
		Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "retry only after confirmation"}},
	}
	c.conn = conn
	c.outbox = []collab.CommandEnvelope{env}
	c.outboxFailures[env.RequestID] = "denied"
	c.state = CollaborationState{
		Status: "connected", Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}

	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatalf("automatic update: %v", err)
	}
	peer.mu.Lock()
	automaticSubmits := len(peer.submitted)
	peer.mu.Unlock()
	if automaticSubmits != 0 || c.outboxFailures[env.RequestID] == "" || len(c.outbox) != 1 {
		t.Fatalf("automatic update released manual failure: submits=%d failures=%v outbox=%d", automaticSubmits, c.outboxFailures, len(c.outbox))
	}

	if _, err := c.retry(context.Background()); err != nil {
		t.Fatalf("manual retry: %v", err)
	}
	peer.mu.Lock()
	manualSubmits := len(peer.submitted)
	peer.mu.Unlock()
	if manualSubmits != 1 || len(c.outboxFailures) != 0 || len(c.outbox) != 0 {
		t.Fatalf("manual retry did not release failure: submits=%d failures=%v outbox=%d", manualSubmits, c.outboxFailures, len(c.outbox))
	}
	c.close()
}

func TestCollaborationAutomaticUpdateRequiresRecoverableState(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	calls := 0
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		calls++
		return nil, errors.New("must not be called")
	}

	c.state = CollaborationState{Status: "failed", Mode: "client", Host: "127.0.0.1", Room: "room-a", SessionID: "session-a", Retryable: false}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.state = CollaborationState{Status: "failed", Mode: "client", Host: "127.0.0.1", SessionID: "session-a", Retryable: true}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.state = CollaborationState{Status: "disconnected", SessionID: "session-a", Retryable: true}
	if err := c.updateConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("incomplete or non-retryable Room attempted %d updates", calls)
	}
}

func TestCollaborationUpdateLoopStopsWithRuntime(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	persisted := collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Port: 39170, Room: "room-a",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent", SessionID: "session-a",
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-a", Name: "Room A"}},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadPersisted()

	var calls atomic.Int32
	called := make(chan struct{}, 8)
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		calls.Add(1)
		select {
		case called <- struct{}{}:
		default:
		}
		return nil, &collaborationTransportError{message: "offline", retryable: true}
	}
	c.updateDelay = func() time.Duration { return time.Millisecond }
	c.initialUpdateDelay = func() time.Duration { return 0 }
	c.startUpdateLoop(context.Background())
	for range 2 {
		select {
		case <-called:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for Room update")
		}
	}
	c.close()
	afterClose := calls.Load()
	time.Sleep(10 * time.Millisecond)
	if calls.Load() != afterClose {
		t.Fatalf("Room update continued after close: before=%d after=%d", afterClose, calls.Load())
	}
}

func TestCollaborationUpdateLoopReconcilesBeforeFirstLowFrequencyTick(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	persisted := collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Port: 39170, Room: "room-ready-race",
		MemberID: "member-a", MemberName: "Alice", AgentID: "agent-a", AgentName: "Alice Agent", SessionID: "session-ready-race",
		Snapshot: collab.Snapshot{Room: collab.Room{ID: "room-ready-race", Name: "Ready Race"}},
	}
	data, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.persistPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c.loadPersisted()

	called := make(chan struct{}, 1)
	c.openJoin = func(context.Context, JoinCollaborationRoomInput, collab.MemberDescriptor, string) (*collaborationConnection, error) {
		called <- struct{}{}
		return nil, &collaborationTransportError{message: "offline", retryable: true}
	}
	c.updateDelay = func() time.Duration { return time.Hour }
	c.initialUpdateDelay = func() time.Duration { return 0 }
	c.startUpdateLoop(context.Background())
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("resident Room waited for the first low-frequency tick before reconciliation")
	}
	c.close()
}

func TestCollaborationUpdateRestartsStoppedConnectionLoop(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "client", "session-a")
	oldDone := make(chan struct{})
	close(oldDone)
	var oldCanceled atomic.Bool
	conn.done = oldDone
	conn.cancel = func() { oldCanceled.Store(true) }
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Mode: conn.mode, Host: conn.hostName, Port: conn.port, Room: conn.room,
		MemberID: conn.memberID, AgentID: conn.agentID, SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}

	c.opMu.Lock()
	started := c.ensureConnectionLoop(conn)
	c.opMu.Unlock()
	if !started || !oldCanceled.Load() || conn.done == oldDone || !collaborationConnectionLoopRunning(conn) {
		t.Fatalf("stopped loop restart: started=%v oldCanceled=%v sameDone=%v running=%v", started, oldCanceled.Load(), conn.done == oldDone, collaborationConnectionLoopRunning(conn))
	}
	conn.cancel()
	select {
	case <-conn.done:
	case <-time.After(time.Second):
		t.Fatal("restarted connection loop did not stop")
	}
	c.close()
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
	conn.hostKey = "sha256:host-key"
	conn.routes = []CollaborationRouteState{{
		CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: conn.hostName, Port: conn.port, Priority: 1000, ProtocolVersion: collaborationProtocolV2},
		Status:                  "connected",
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "host", Host: conn.hostName, Port: conn.port, Room: conn.room, SessionID: conn.sessionID}
	invite, err := c.invite()
	if err != nil || invite.Room != conn.room || invite.Port != conn.port || invite.Token != conn.joinToken {
		t.Fatalf("invite=%+v err=%v", invite, err)
	}
	for _, host := range invite.Hosts {
		if host == "0.0.0.0" || host == "::" {
			t.Fatalf("invite exposed an unspecified address: %+v", invite.Hosts)
		}
	}
	if size := len(invite.Hosts); size < 2 || invite.Hosts[size-2] != "127.0.0.1" || invite.Hosts[size-1] != "::1" {
		t.Fatalf("invite loopback addresses are not last: %+v", invite.Hosts)
	}
	for _, route := range invite.Routes {
		if route.Kind == "lan" && route.ProtocolVersion != collaborationProtocolV2 {
			t.Fatalf("invite lost LAN protocol version: %+v", route)
		}
	}
	c.state.Mode = "client"
	if _, err := c.invite(); err == nil {
		t.Fatal("client exported a Host invite using its own IP")
	}
}

func TestCollaborationPureRelayHostInvite(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	conn := testConnection(&fakeCollaborationPeer{}, "host", "session-relay")
	conn.hostName = "0.0.0.0"
	conn.port = 0
	conn.joinToken = "relay-secret"
	conn.hostKey = "sha256:host-key"
	conn.routes = []CollaborationRouteState{{
		CollaborationRouteInput: CollaborationRouteInput{Kind: "relay", RelayID: "sg", URL: "wss://relay.example/relay/v1/connect", TunnelID: "tun-1", GuestCapability: "cap-1", Priority: 100},
		Status:                  "connected",
	}}
	c.conn = conn
	c.state = CollaborationState{Status: "connected", Mode: "host", Host: conn.hostName, Port: conn.port, Room: conn.room, SessionID: conn.sessionID}
	invite, err := c.invite()
	if err != nil || invite.Version != 2 || invite.HostKey != conn.hostKey || invite.Invite == "" || len(invite.Routes) == 0 {
		t.Fatalf("pure Relay Host should export V2 invite: invite=%+v err=%v", invite, err)
	}

	// No hostKey → error
	conn.hostKey = ""
	conn.routes = []CollaborationRouteState{{
		CollaborationRouteInput: CollaborationRouteInput{Kind: "relay", RelayID: "sg", URL: "wss://relay.example/relay/v1/connect", TunnelID: "tun-1", GuestCapability: "cap-1", Priority: 100},
		Status:                  "connected",
	}}
	c.conn = conn
	c.state.Port = 0
	if _, err := c.invite(); err == nil {
		t.Fatal("pure Relay Host without hostKey should fail to export invite")
	}

	// No connected routes → error
	conn.hostKey = "sha256:host-key"
	conn.routes = nil
	c.conn = conn
	c.state.Port = 0
	if _, err := c.invite(); err == nil {
		t.Fatal("pure Relay Host without connected routes should fail to export invite")
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

func TestCollaborationOutboxFilteredByRoom(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Host: conn.hostName, Port: conn.port,
		Room: "room-a", MemberID: "member-a", AgentID: conn.agentID,
		SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	c.outbox = []collab.CommandEnvelope{
		{RequestID: "room-a-chat", Room: "room-a", MemberID: "member-a", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "current room"}}},
		{RequestID: "room-b-chat", Room: "room-b", MemberID: "member-b", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "other room"}}},
		{RequestID: "diff-member", Room: "room-a", MemberID: "member-b", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "different member"}}},
	}
	state := c.snapshot()
	if len(state.Outbox) != 1 || state.Outbox[0].RequestID != "room-a-chat" {
		t.Fatalf("outbox should only contain items matching current room and member: %+v", state.Outbox)
	}

	// Verify drainOutbox also respects room/member filter
	c.conn = conn
	c.outboxFailures = map[string]string{}
	ctx := context.Background()
	if !c.drainOutbox(ctx, conn) {
		t.Fatal("drainOutbox should succeed")
	}
	peer.mu.Lock()
	submitted := len(peer.submitted)
	peer.mu.Unlock()
	if submitted != 1 || peer.submitted[0].RequestID != "room-a-chat" {
		t.Fatalf("drainOutbox submitted wrong items: submitted=%d ids=%v", submitted, peer.submitted)
	}
}

func TestCollaborationSetConnectingFencesOldConnection(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	peer := &fakeCollaborationPeer{}
	conn := testConnection(peer, "client", "session-a")
	c.conn = conn
	c.state = CollaborationState{
		Status: "connected", Host: conn.hostName, Port: conn.port,
		Room: "room-a", MemberID: conn.memberID, AgentID: conn.agentID,
		SessionID: conn.sessionID, Snapshot: conn.initialSnapshot,
	}
	identity := collab.MemberDescriptor{ID: "member-b", Name: "Bob", Agent: collab.AgentDescriptor{ID: "agent-b", Name: "Bob Agent"}}
	c.setConnecting("client", "10.0.0.9", 9999, "room-b", identity, "session-a")

	// Verify c.conn is nil (fenced)
	c.mu.RLock()
	connAfter := c.conn
	c.mu.RUnlock()
	if connAfter != nil {
		t.Fatalf("c.conn should be nil after setConnecting, got %v", connAfter)
	}

	// Old connection's markConnected must not overwrite new state
	oldSnapshot := collab.Snapshot{Room: collab.Room{ID: "room-a", Name: "Room A", LatestSequence: 10}, LatestSequence: 10}
	c.markConnected(conn, &oldSnapshot, nil)
	state := c.snapshot()
	if state.Room != "room-b" || state.Snapshot.LatestSequence != 0 {
		t.Fatalf("old connection markConnected should not change new room state: %+v", state)
	}

	// Old connection's markReconnect must not overwrite new state or emit
	c.markReconnect(conn, &collaborationTransportError{message: "stale heartbeat failure", retryable: true})
	state = c.snapshot()
	if state.Status != "connecting" || state.LastError != "" {
		t.Fatalf("old connection markReconnect changed state: %+v", state)
	}
}

func TestCollaborationHostFencesBeforeNewConnection(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	defer c.close()
	firstPeer := &fakeCollaborationPeer{}
	firstConn := testConnection(firstPeer, "client", "session-a")
	firstConn.cancel = func() {} // no-op cancel for fencing test
	c.conn = firstConn
	c.state = CollaborationState{
		Status: "connected", Host: "10.0.0.8", Port: 39170,
		Room: "room-a", MemberID: "member-a", AgentID: "agent-a",
		SessionID: "session-a", Snapshot: firstConn.initialSnapshot,
	}
	c.outbox = []collab.CommandEnvelope{
		{RequestID: "old-room-chat", Room: "room-a", MemberID: "member-a", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "old room"}}},
	}

	// Host a new room to trigger fenceCurrentConnection
	secondPeer := &fakeCollaborationPeer{}
	snapshotB := collab.Snapshot{
		Room: collab.Room{ID: "room-b", Name: "Room B", LatestSequence: 1}, LatestSequence: 1,
		Members: []collab.Member{{ID: "member-a", Name: "Alice", Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent"}}},
	}
	secondPeer.snapshot = snapshotB
	c.openHost = func(_ context.Context, input HostCollaborationRoomInput, _ collab.MemberDescriptor, _ string) (*collaborationConnection, error) {
		if input.Room != "room-b" {
			t.Fatalf("expected room-b, got %q", input.Room)
		}
		return &collaborationConnection{
			peer: secondPeer, mode: "host", hostName: "127.0.0.1", port: 39171, room: "room-b",
			memberID: "member-a", agentID: "agent-a", sessionID: "session-a",
			memberName: "Alice", agentName: "Alice Agent",
			connectionSession: "cs-room-b", initialSnapshot: snapshotB,
		}, nil
	}
	state, err := c.host(context.Background(), HostCollaborationRoomInput{
		ListenHost: "127.0.0.1", Port: 39171, Room: "room-b", RoomName: "Room B",
		MemberName: "Alice", SessionID: "session-a",
	})
	if err != nil || (state.Status != "connected" && state.Status != "syncing") || state.Room != "room-b" {
		t.Fatalf("host new room failed: state=%+v err=%v", state, err)
	}
	if firstPeer.leaveCount != 1 {
		t.Fatalf("old connection leave count = %d; want 1", firstPeer.leaveCount)
	}

	// Old room's outbox item must not appear in the new room's snapshot
	finalState := c.snapshot()
	for _, item := range finalState.Outbox {
		if item.RequestID == "old-room-chat" {
			t.Fatalf("old room outbox leaked into new room: %+v", finalState.Outbox)
		}
	}
}

func TestCollaborationMultiSessionIdentityStablePerRoomAndSession(t *testing.T) {
	// Two different Sessions joining the same Room must receive different,
	// stable Member/Agent IDs. The same Session retrying must produce
	// identical IDs (idempotent), and the state/events of one Session
	// must not leak into the other.
	isolateDesktopUserDirs(t)
	app := &App{}
	app.collaborations = map[string]*desktopCollaboration{}

	// Create two runtimes for two different sessions in the same workspace.
	first, err := app.collaborationRuntime("session-first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.collaborationRuntime("session-second")
	if err != nil {
		t.Fatal(err)
	}
	// Override validateAgent so localIdentity does not require real tabs.
	first.validateAgent = func(string) error { return nil }
	second.validateAgent = func(string) error { return nil }
	const sharedRoom = "shared-room"

	// The profile IDs are intentionally identical, matching the Desktop form.
	// Room membership still scopes them by Session before the first join.
	base1, err := first.localIdentity("shared-member", "Alice", "", "", "shared-agent", "Alice Agent", "", "", "session-first", sharedRoom)
	if err != nil {
		t.Fatal(err)
	}
	base2, err := second.localIdentity("shared-member", "Alice", "", "", "shared-agent", "Alice Agent", "", "", "session-second", sharedRoom)
	if err != nil {
		t.Fatal(err)
	}
	id1 := scopedCollaborationIdentity(base1, sharedRoom, "session-first")
	id2 := scopedCollaborationIdentity(base2, sharedRoom, "session-second")
	if id1.ID == "" || id2.ID == "" || id1.Agent.ID == "" || id2.Agent.ID == "" {
		t.Fatalf("empty identities: first=%+v second=%+v", id1, id2)
	}
	if id1.ID == id2.ID || id1.Agent.ID == id2.Agent.ID {
		t.Fatalf("different sessions must receive different stable IDs: first=%+v second=%+v", id1, id2)
	}

	// Same Session calling localIdentity again yields identical IDs.
	id1Again := scopedCollaborationIdentity(base1, sharedRoom, "session-first")
	if id1Again.ID != id1.ID || id1Again.Agent.ID != id1.Agent.ID {
		t.Fatalf("repeated localIdentity was not idempotent: first=%+v again=%+v err=%v", id1, id1Again, err)
	}

	// The second runtime must report its own SessionID, not the first one.
	first.mu.Lock()
	first.state.Room = sharedRoom
	first.state.MemberID = id1.ID
	first.state.AgentID = id1.Agent.ID
	first.state.SessionID = "session-first"
	first.state.Snapshot.Timeline = []collab.TimelineItem{{ID: "first-msg", Sequence: 1, Type: collab.TimelineChat}}
	first.mu.Unlock()

	second.mu.Lock()
	second.state.Room = sharedRoom
	second.state.MemberID = id2.ID
	second.state.AgentID = id2.Agent.ID
	second.state.SessionID = "session-second"
	second.mu.Unlock()

	firstState := first.snapshot()
	secondState := second.snapshot()
	if firstState.SessionID != "session-first" || secondState.SessionID != "session-second" {
		t.Fatalf("session isolation broken: first=%q second=%q", firstState.SessionID, secondState.SessionID)
	}
	if len(firstState.Snapshot.Timeline) != 1 || len(secondState.Snapshot.Timeline) != 0 {
		t.Fatalf("cross-session timeline leakage: first=%d items second=%d items", len(firstState.Snapshot.Timeline), len(secondState.Snapshot.Timeline))
	}
	if firstState.MemberID != id1.ID || secondState.MemberID != id2.ID {
		t.Fatalf("member IDs drifted across sessions: first=%q second=%q", firstState.MemberID, secondState.MemberID)
	}

	app.closeCollaborations()
}
