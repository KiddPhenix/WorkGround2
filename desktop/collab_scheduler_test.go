package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/collab"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func snapshotWithTimeline(items ...collab.TimelineItem) collab.Snapshot {
	var seq uint64
	for i := range items {
		seq++
		items[i].Sequence = seq
	}
	return collab.Snapshot{
		Room: collab.Room{ID: "room-a", Name: "Room A", LatestSequence: seq},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Name: "Alice Agent", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-b", Name: "Bob Agent", Status: collab.AgentIdle}},
		},
		Timeline:       items,
		LatestSequence: seq,
	}
}

func chatItem(id, authorID, text string, mentionMembers, mentionAgents []string) collab.TimelineItem {
	return collab.TimelineItem{
		ID:   id,
		Type: collab.TimelineChat,
		Chat: &collab.ChatMessage{ID: id, AuthorID: authorID, Text: text, MentionMemberIDs: mentionMembers, MentionAgentIDs: mentionAgents},
	}
}

func agentRequestItem(id, authorID, targetMemberID, instruction string, status collab.AgentRequestStatus) collab.TimelineItem {
	return collab.TimelineItem{
		ID:           id,
		Type:         collab.TimelineAgentRequest,
		AgentRequest: &collab.AgentRequest{ID: id, AuthorID: authorID, TargetMemberID: targetMemberID, Instruction: instruction, Status: status},
	}
}

func agentResultItem(id, ownerID string, handoffs []collab.AgentHandoff) collab.TimelineItem {
	return collab.TimelineItem{
		ID:          id,
		Type:        collab.TimelineAgentResult,
		AgentResult: &collab.AgentResult{ID: id, OwnerID: ownerID, Handoffs: handoffs},
	}
}

func agentRunItem(id, ownerID, commandID string) collab.TimelineItem {
	return collab.TimelineItem{
		ID:       id,
		Type:     collab.TimelineAgentRun,
		AgentRun: &collab.AgentRun{OwnerID: ownerID, CommandID: commandID},
	}
}

// newSchedulerTestRuntime creates a minimal desktopCollaboration for scheduler tests.
func newSchedulerTestRuntime(t *testing.T, snapshot collab.Snapshot) *desktopCollaboration {
	t.Helper()
	c := &desktopCollaboration{
		app:            &App{},
		ownerSessionID: "session-a",
		state: CollaborationState{
			Status: "connected", Mode: "host",
			Room: snapshot.Room.ID, MemberID: "member-a", AgentID: "agent-a",
			SessionID: "session-a", Snapshot: snapshot,
			AgentConfig: defaultCollaborationAgentConfig(),
		},
		starts:         map[string]collaborationStartRecord{},
		runs:           map[string]*collaborationAgentRun{},
		outboxFailures: map[string]string{},
	}
	c.validateAgent = func(string) error { return nil }
	c.agentReady = func(string) (bool, error) { return true, nil }
	c.prepareAgentInput = func(_ string, _ []string, input string) (string, error) { return input, nil }
	c.submitAgent = func(_, _, _ string) error { return nil }
	c.prepareAutoAgent = func(string) (string, error) { return "ask", nil }
	c.restoreAutoAgent = func(string, string) {}
	c.scheduler = newCollaborationScheduler()
	c.conn = &collaborationConnection{
		mode: "host", room: snapshot.Room.ID,
		memberID: "member-a", agentID: "agent-a", sessionID: "session-a",
		connectionSession: "cs1.test",
	}
	return c
}

// ---------------------------------------------------------------------------
// Mention detection tests
// ---------------------------------------------------------------------------

func TestSchedulerMentionTriggersAgent(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "hello @Alice Agent please help", []string{"member-a"}, []string{"agent-a"}),
	)
	s := newCollaborationScheduler()
	input := s.nextMention(snap, "member-a", "agent-a", "session-a", nil)
	if input == nil {
		t.Fatal("expected mention to trigger agent start")
	}
	if !input.Automatic {
		t.Error("expected automatic=true")
	}
}

func TestSchedulerMentionSkipsWhenAlreadyHandled(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent go", []string{"member-a"}, []string{"agent-a"}),
	)
	s := newCollaborationScheduler()
	input := s.nextMention(snap, "member-a", "agent-a", "session-a", map[string]bool{"chat-1": true})
	if input != nil {
		t.Fatal("expected mention to be skipped when already handled")
	}
}

func TestSchedulerMentionSkipsWhenAlreadyStarted(t *testing.T) {
	requestID := schedulerMentionRequestID("chat-1", "agent-a")
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent go", []string{"member-a"}, []string{"agent-a"}),
		agentRunItem("cmd-1", "member-a", requestID),
	)
	s := newCollaborationScheduler()
	input := s.nextMention(snap, "member-a", "agent-a", "session-a", nil)
	if input != nil {
		t.Fatal("expected mention to be skipped when agent command already exists")
	}
}

// ---------------------------------------------------------------------------
// Request detection tests
// ---------------------------------------------------------------------------

func TestSchedulerRequestTriggersAgent(t *testing.T) {
	snap := snapshotWithTimeline(
		agentRequestItem("req-1", "member-b", "member-a", "Please review my PR", collab.RequestPending),
	)
	s := newCollaborationScheduler()
	input := s.nextRequest(snap, "member-a", nil)
	if input == nil {
		t.Fatal("expected pending request to trigger agent start")
	}
}

func TestSchedulerRequestSkipsWhenAlreadyAccepted(t *testing.T) {
	snap := snapshotWithTimeline(
		agentRequestItem("req-1", "member-b", "member-a", "Review PR", collab.RequestAccepted),
	)
	s := newCollaborationScheduler()
	input := s.nextRequest(snap, "member-a", nil)
	if input != nil {
		t.Fatal("expected accepted request to be skipped")
	}
}

// ---------------------------------------------------------------------------
// Question detection tests
// ---------------------------------------------------------------------------

func TestSchedulerQuestionTriggersAgent(t *testing.T) {
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{}}, // no agent
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "\u8fd9\u4e2abug\u600e\u4e48\u4fee\uff1f", nil, nil),
		},
	}
	s := newCollaborationScheduler()
	input := s.nextQuestion(snap, "member-a", "agent-a", "session-a", nil)
	if input == nil {
		t.Fatal("expected question to trigger agent start")
	}
}

func TestSchedulerQuestionSkipsAgentAuthored(t *testing.T) {
	// member-b has agent and is online → skip.
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-b", Status: collab.AgentIdle}},
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "how to fix this?", nil, nil),
		},
	}
	s := newCollaborationScheduler()
	input := s.nextQuestion(snap, "member-a", "agent-a", "session-a", nil)
	if input != nil {
		t.Fatal("expected agent-authored question to be skipped")
	}
}

func TestSchedulerQuestionSkipsOfflineAgentAuthored(t *testing.T) {
	// member-b has agent but is OFFLINE — question is still agent-authored.
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOffline, Agent: collab.AgentDescriptor{ID: "agent-b", Status: collab.AgentOffline}},
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "how to fix this?", nil, nil),
		},
	}
	s := newCollaborationScheduler()
	input := s.nextQuestion(snap, "member-a", "agent-a", "session-a", nil)
	if input != nil {
		t.Fatal("offline member with agent is still agent-authored; expected skip")
	}
}

// ---------------------------------------------------------------------------
// Handoff detection tests
// ---------------------------------------------------------------------------

func TestSchedulerHandoffTriggersAgent(t *testing.T) {
	snap := snapshotWithTimeline(
		agentResultItem("result-1", "member-b", []collab.AgentHandoff{
			{TargetAgentID: "agent-a", Instruction: "Review the generated code", RequiresResponse: true},
		}),
	)
	config := CollaborationAgentConfig{AutoRespondAgents: true, AgentResponseIntervalSeconds: 0, AgentClockTurns: 12, AgentClockUnlimited: true}
	s := newCollaborationScheduler()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input == nil {
		t.Fatal("expected handoff to trigger agent start")
	}
}

func TestSchedulerHandoffSkipsWhenDisabled(t *testing.T) {
	snap := snapshotWithTimeline(
		agentResultItem("result-1", "member-b", []collab.AgentHandoff{
			{TargetAgentID: "agent-a", Instruction: "Review", RequiresResponse: true},
		}),
	)
	config := CollaborationAgentConfig{AutoRespondAgents: false, AgentClockTurns: 12, AgentClockUnlimited: true}
	s := newCollaborationScheduler()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input != nil {
		t.Fatal("expected handoff to be skipped when disabled")
	}
}

func TestSchedulerHandoffRespectsClockLimit(t *testing.T) {
	items := []collab.TimelineItem{
		agentResultItem("result-1", "member-b", []collab.AgentHandoff{
			{TargetAgentID: "agent-a", Instruction: "Review", RequiresResponse: true},
		}),
	}
	for i := 0; i < 12; i++ {
		id := string(rune('a' + i))
		items = append(items, agentRunItem("collab-cmd-"+id, "member-a", agentCollaborationPrefix+id))
	}
	snap := snapshotWithTimeline(items...)
	config := CollaborationAgentConfig{AutoRespondAgents: true, AgentResponseIntervalSeconds: 0, AgentClockTurns: 12, AgentClockUnlimited: false}
	s := newCollaborationScheduler()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input != nil {
		t.Fatal("expected handoff to be skipped when clock turns exhausted")
	}
}

func TestSchedulerHandoffRespectsIntervalGate(t *testing.T) {
	snap := snapshotWithTimeline(
		agentResultItem("result-1", "member-b", []collab.AgentHandoff{
			{TargetAgentID: "agent-a", Instruction: "Review", RequiresResponse: true},
		}),
	)
	config := CollaborationAgentConfig{AutoRespondAgents: true, AgentResponseIntervalSeconds: 60, AgentClockTurns: 12, AgentClockUnlimited: true}
	s := newCollaborationScheduler()
	s.lastCollabAttemptAt = time.Now()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input != nil {
		t.Fatal("expected handoff to be skipped when interval gate not elapsed")
	}
}

func TestSchedulerHandoffRestoresIntervalFromTimeline(t *testing.T) {
	now := time.Now().UTC()
	previous := agentRunItem("run-previous", "member-a", agentCollaborationPrefix+"previous")
	previous.AgentRun.UpdatedAt = now.Add(-5 * time.Second)
	result := agentResultItem("result-new", "member-b", []collab.AgentHandoff{{
		TargetAgentID: "agent-a", Instruction: "continue", RequiresResponse: true,
	}})
	result.AgentResult.CreatedAt = now
	snap := snapshotWithTimeline(previous, result)
	s := newCollaborationScheduler()
	config := defaultCollaborationAgentConfig()
	config.AutoRespondAgents = true
	config.AgentResponseIntervalSeconds = 30
	if input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil); input != nil {
		t.Fatal("restart must preserve the handoff interval from the persisted Agent run")
	}
}

// ---------------------------------------------------------------------------
// Clock recovery: woundAt with CreatedAt timestamps
// ---------------------------------------------------------------------------

func TestSchedulerClockWoundAtWithCreatedAt(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-2 * time.Hour)
	future := now.Add(2 * time.Hour)

	// AgentResult with CreatedAt after woundAt → should pass.
	resultAfter := collab.TimelineItem{
		ID: "result-after", Sequence: 1, Type: collab.TimelineAgentResult,
		AgentResult: &collab.AgentResult{ID: "result-after", OwnerID: "member-b",
			CreatedAt: future,
			Handoffs:  []collab.AgentHandoff{{TargetAgentID: "agent-a", Instruction: "after", RequiresResponse: true}},
		},
	}
	snap := snapshotWithTimeline(resultAfter)
	config := CollaborationAgentConfig{
		AutoRespondAgents: true, AgentResponseIntervalSeconds: 0,
		AgentClockTurns: 12, AgentClockUnlimited: true,
		AgentClockWoundAt: now.Format(time.RFC3339Nano),
	}
	s := newCollaborationScheduler()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input == nil {
		t.Fatal("result with CreatedAt after woundAt should trigger")
	}

	// Same result but woundAt is in the future → skip.
	config2 := CollaborationAgentConfig{
		AutoRespondAgents: true, AgentResponseIntervalSeconds: 0,
		AgentClockTurns: 12, AgentClockUnlimited: true,
		AgentClockWoundAt: future.Add(time.Hour).Format(time.RFC3339Nano),
	}
	input = s.nextHandoff(snap, "member-a", "agent-a", "session-a", config2, nil)
	if input != nil {
		t.Fatal("result with CreatedAt before woundAt should be skipped")
	}

	// Result with CreatedAt before woundAt → skip.
	resultBefore := collab.TimelineItem{
		ID: "result-before", Sequence: 2, Type: collab.TimelineAgentResult,
		AgentResult: &collab.AgentResult{ID: "result-before", OwnerID: "member-b",
			CreatedAt: past,
			Handoffs:  []collab.AgentHandoff{{TargetAgentID: "agent-a", Instruction: "before", RequiresResponse: true}},
		},
	}
	snap2 := snapshotWithTimeline(resultBefore)
	config3 := CollaborationAgentConfig{
		AutoRespondAgents: true, AgentResponseIntervalSeconds: 0,
		AgentClockTurns: 12, AgentClockUnlimited: true,
		AgentClockWoundAt: now.Format(time.RFC3339Nano),
	}
	input = s.nextHandoff(snap2, "member-a", "agent-a", "session-a", config3, nil)
	if input != nil {
		t.Fatal("result with CreatedAt before woundAt should be skipped")
	}
}

func TestSchedulerClockWoundAtZeroTimeIsConservative(t *testing.T) {
	// Item with zero CreatedAt + non-zero woundAt → conservative skip.
	result := collab.TimelineItem{
		ID: "result-zero", Sequence: 1, Type: collab.TimelineAgentResult,
		AgentResult: &collab.AgentResult{ID: "result-zero", OwnerID: "member-b",
			CreatedAt: time.Time{}, // zero
			Handoffs:  []collab.AgentHandoff{{TargetAgentID: "agent-a", Instruction: "zero", RequiresResponse: true}},
		},
	}
	snap := snapshotWithTimeline(result)
	config := CollaborationAgentConfig{
		AutoRespondAgents: true, AgentResponseIntervalSeconds: 0,
		AgentClockTurns: 12, AgentClockUnlimited: true,
		AgentClockWoundAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano),
	}
	s := newCollaborationScheduler()
	input := s.nextHandoff(snap, "member-a", "agent-a", "session-a", config, nil)
	if input != nil {
		t.Fatal("zero-CreatedAt item with woundAt should be conservatively skipped")
	}
}

// ---------------------------------------------------------------------------
// Build handled refs test
// ---------------------------------------------------------------------------

func TestBuildSchedulerHandledRefs(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "hello", nil, nil),
		collab.TimelineItem{ID: "cmd-1", Type: collab.TimelineAgentRun, AgentRun: &collab.AgentRun{
			OwnerID: "member-a", ReferenceIDs: []string{"chat-1", "req-1"},
		}},
		collab.TimelineItem{ID: "cmd-2", Type: collab.TimelineAgentRun, AgentRun: &collab.AgentRun{
			OwnerID: "member-b", ReferenceIDs: []string{"chat-2"},
		}},
	)
	refs := buildSchedulerHandledRefs(snap, "member-a")
	if !refs["chat-1"] || !refs["req-1"] || refs["chat-2"] {
		t.Errorf("handled refs: %v", refs)
	}
}

// ---------------------------------------------------------------------------
// Recognition mode semantics
// ---------------------------------------------------------------------------

func TestSchedulerRecognitionModeMessageScansOnSignal(t *testing.T) {
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{}}, // no agent
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "how to fix?", nil, nil),
		},
	}
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "message"
	c.state.AgentConfig.AutoRespondQuestions = true

	var captured []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		captured = append(captured, input)
	}

	s := newCollaborationScheduler()

	// Signal wake → should scan and find question.
	s.scheduleOnce(context.Background(), c, wakeSignal)
	if len(captured) != 1 {
		t.Fatalf("message mode on signal: expected 1 start, got %d", len(captured))
	}

	// Ticker wake → should NOT scan questions in message mode.
	captured = nil
	s.scheduleOnce(context.Background(), c, wakeTicker)
	if len(captured) != 0 {
		t.Fatalf("message mode on ticker: expected 0 starts, got %d", len(captured))
	}
}

func TestSchedulerRecognitionModeIntervalScansOnlyOnTicker(t *testing.T) {
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{}},
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "how to fix?", nil, nil),
		},
	}
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "interval"
	c.state.AgentConfig.AutoRespondQuestions = true

	var captured []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		captured = append(captured, input)
	}

	s := newCollaborationScheduler()

	// Signal wake → should NOT scan in interval mode.
	s.scheduleOnce(context.Background(), c, wakeSignal)
	if len(captured) != 0 {
		t.Fatalf("interval mode on signal: expected 0 starts, got %d", len(captured))
	}

	// Ticker wake → should scan.
	s.scheduleOnce(context.Background(), c, wakeTicker)
	if len(captured) != 1 {
		t.Fatalf("interval mode on ticker: expected 1 start, got %d", len(captured))
	}
}

func TestSchedulerRecognitionModeOffNeverScans(t *testing.T) {
	snap := collab.Snapshot{
		Room: collab.Room{ID: "room-a"},
		Members: []collab.Member{
			{ID: "member-a", Name: "Alice", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent-a", Status: collab.AgentIdle}},
			{ID: "member-b", Name: "Bob", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{}},
		},
		Timeline: []collab.TimelineItem{
			chatItem("chat-1", "member-b", "how to fix?", nil, nil),
		},
	}
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "off"
	c.state.AgentConfig.AutoRespondQuestions = true

	var captured []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		captured = append(captured, input)
	}

	s := newCollaborationScheduler()
	s.scheduleOnce(context.Background(), c, wakeSignal)
	if len(captured) != 0 {
		t.Fatalf("off mode on signal: expected 0 starts, got %d", len(captured))
	}
	s.scheduleOnce(context.Background(), c, wakeTicker)
	if len(captured) != 0 {
		t.Fatalf("off mode on ticker: expected 0 starts, got %d", len(captured))
	}
}

func TestSchedulerMentionsAlwaysFireOnSignal(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "off" // recognition off, but mentions still fire

	var captured []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		captured = append(captured, input)
	}

	s := newCollaborationScheduler()
	s.scheduleOnce(context.Background(), c, wakeSignal)
	if len(captured) != 1 {
		t.Fatalf("mentions should fire even with recognition off, got %d", len(captured))
	}
	if !strings.HasPrefix(captured[0].RequestID, mentionPrefix) {
		t.Errorf("expected mention prefix, got %q", captured[0].RequestID)
	}
}

func TestSchedulerMentionsDoNotFireOnTicker(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "message"

	var captured []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		captured = append(captured, input)
	}

	s := newCollaborationScheduler()
	s.scheduleOnce(context.Background(), c, wakeTicker)
	if len(captured) != 0 {
		t.Fatalf("mentions should not fire on ticker, got %d", len(captured))
	}
}

// ---------------------------------------------------------------------------
// Integration proof: no frontend, event → exactly one submission, replay → idempotent
// ---------------------------------------------------------------------------

func TestSchedulerIntegrationNoFrontendMounted(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "message"

	var mu sync.Mutex
	var submissions []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		mu.Lock()
		submissions = append(submissions, input)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start the scheduler loop (no frontend mounted).
	s := newCollaborationScheduler()
	go s.run(ctx, c)

	// Deliver a snapshot signal.
	s.signal(wakeSignal)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(submissions)
	var first StartCollaborationAgentInput
	if count > 0 {
		first = submissions[0]
	}
	mu.Unlock()
	if count != 1 {
		t.Fatalf("first signal: expected exactly 1 submission, got %d", count)
	}

	// Add the agent command to the timeline so the next scheduler run sees it as handled.
	c.state.Snapshot.Timeline = append(c.state.Snapshot.Timeline,
		agentRunItem("integration-cmd", "member-a", first.RequestID))

	// Replay the same snapshot: should NOT produce a second submission.
	s.signal(wakeSignal)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count2 := len(submissions)
	mu.Unlock()
	if count2 != 1 {
		t.Fatalf("replay signal: expected still 1 submission, got %d", count2)
	}

	// The submission must be automatic with a mention prefix.
	mu.Lock()
	sub := submissions[0]
	mu.Unlock()
	if !sub.Automatic {
		t.Error("submission should be automatic")
	}
	if !strings.HasPrefix(sub.RequestID, mentionPrefix) {
		t.Errorf("expected mention prefix, got %q", sub.RequestID)
	}
}

// ---------------------------------------------------------------------------
// Startup residency: inactive tab restores runtime + scheduler
// ---------------------------------------------------------------------------

func TestSchedulerStartupResidencyRestoresRuntime(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "message"

	// Simulate: runtime exists (like after restoreCollaborationRuntimes),
	// scheduler is running, but no frontend tab is mounted.
	var mu sync.Mutex
	var submissions []StartCollaborationAgentInput
	c.startAgentHook = func(_ context.Context, input StartCollaborationAgentInput) {
		mu.Lock()
		submissions = append(submissions, input)
		mu.Unlock()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go c.scheduler.run(ctx, c)
	c.scheduler.signal(wakeSignal)
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	count := len(submissions)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("inactive tab with restored runtime: expected 1 submission, got %d", count)
	}
}

func TestSchedulerOffTabTransportResidencyDoesNotQueueAgentRun(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.startAgentHook = nil
	c.agentReady = func(string) (bool, error) { return false, nil }

	c.scheduler.scheduleOnce(context.Background(), c, wakeSignal)
	c.mu.RLock()
	starts := len(c.starts)
	queued := len(c.queuedRuns)
	c.mu.RUnlock()
	if starts != 0 || queued != 0 {
		t.Fatalf("off-tab scheduler queued Agent work: starts=%d queued=%d", starts, queued)
	}
}

func TestRestoreOneCollaborationUsesOnlyRegisteredSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	root := t.TempDir()
	if err := addProject(root, ""); err != nil {
		t.Fatal(err)
	}
	app := NewApp()
	defer app.closeCollaborations()
	sessionDir := desktopSessionDir(root)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "inactive-room.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(sessionPath, agent.BranchMeta{
		ID: string(agent.BranchID(sessionPath)), Scope: "project", WorkspaceRoot: root,
		TopicID: "topic-registered-room", TopicTitle: "Registered Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	if err := ensureTopicIndexed("project", root, "topic-registered-room", "Registered Room", topicTitleSourceAuto); err != nil {
		t.Fatal(err)
	}
	sessionID := "session_scheduler_restore_registered"
	tab := &WorkspaceTab{ID: "inactive-room", SessionID: sessionID, SessionPath: sessionPath}
	app.trackSession(tab)
	app.mu.Lock()
	app.tabs[tab.ID] = tab
	app.mu.Unlock()

	writeState := func(name, id string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name)
		data, err := json.Marshal(collaborationPersistedState{
			Mode: "client", Host: "127.0.0.1", Room: "room-stale-check",
			SessionID: id, SessionPath: sessionPath,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	noopStart := func(*desktopCollaboration, string) {}
	app.restoreOneCollaborationWithRegistry(writeState("registered.json", sessionID), noopStart, loadProjectsFile())
	app.collaborationMu.Lock()
	registered := app.collaborations[sessionID]
	app.collaborationMu.Unlock()
	if registered == nil {
		t.Fatal("inactive registered Room Session was not restored")
	}

	stalePath := filepath.Join(sessionDir, "stale-room.jsonl")
	if err := os.WriteFile(stalePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(stalePath, agent.BranchMeta{
		ID: string(agent.BranchID(stalePath)), Scope: "project", WorkspaceRoot: root,
		TopicID: "topic-removed-room", TopicTitle: "Removed Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	staleID := "session_scheduler_restore_stale"
	staleState := filepath.Join(t.TempDir(), "stale.json")
	data, err := json.Marshal(collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Room: "room-stale-check",
		SessionID: staleID, SessionPath: stalePath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staleState, data, 0o600); err != nil {
		t.Fatal(err)
	}
	app.restoreOneCollaborationWithRegistry(staleState, noopStart, loadProjectsFile())
	app.collaborationMu.Lock()
	stale := app.collaborations[staleID]
	app.collaborationMu.Unlock()
	if stale != nil {
		t.Fatal("closed Room cache must not create a ghost Agent runtime")
	}

	trashDir := filepath.Join(sessionDir, sessionTrashDir, "trashed-room.jsonl")
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		t.Fatal(err)
	}
	trashPath := filepath.Join(trashDir, "trashed-room.jsonl")
	if err := os.WriteFile(trashPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(trashPath, agent.BranchMeta{
		ID: string(agent.BranchID(trashPath)), Scope: "project", WorkspaceRoot: root,
		TopicID: "topic-registered-room", TopicTitle: "Registered Room", SessionKind: agent.SessionKindCollaboration,
	}); err != nil {
		t.Fatal(err)
	}
	trashID := "session_scheduler_restore_trash"
	trashState := filepath.Join(t.TempDir(), "trash.json")
	data, err = json.Marshal(collaborationPersistedState{
		Mode: "client", Host: "127.0.0.1", Room: "room-trash-check",
		SessionID: trashID, SessionPath: trashPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(trashState, data, 0o600); err != nil {
		t.Fatal(err)
	}
	app.restoreOneCollaborationWithRegistry(trashState, noopStart, loadProjectsFile())
	app.collaborationMu.Lock()
	trashed := app.collaborations[trashID]
	app.collaborationMu.Unlock()
	if trashed != nil {
		t.Fatal("trashed Room Session was restored")
	}
}

// ---------------------------------------------------------------------------
// Error handling: startAgent failures are surfaced as LastError
// ---------------------------------------------------------------------------

func TestSchedulerStartErrorIsObservable(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)
	c.state.AgentConfig.RecognitionMode = "message"

	// Inject a fake peer that fails non-retryably so startAgent returns an error.
	peer := &fakeCollaborationPeer{snapshot: snap, failNonRetry: 1}
	c.conn.peer = peer

	s := newCollaborationScheduler()
	s.scheduleOnce(context.Background(), c, wakeSignal)

	// After the failed call, LastError should be set and retryable.
	st := c.snapshot()
	if st.LastError == "" {
		t.Error("expected LastError to be set after failed start")
	}
	if !st.Retryable {
		t.Error("expected Retryable=true after failed start")
	}
	if !strings.Contains(st.LastError, "auto-start") {
		t.Errorf("expected 'auto-start' in error, got %q", st.LastError)
	}
}

// ---------------------------------------------------------------------------
// Concurrent safety
// ---------------------------------------------------------------------------

func TestSchedulerConcurrentSafety(t *testing.T) {
	snap := snapshotWithTimeline(
		chatItem("chat-1", "member-b", "@Alice Agent help", []string{"member-a"}, []string{"agent-a"}),
	)
	c := newSchedulerTestRuntime(t, snap)

	var mu sync.Mutex
	var count int
	c.startAgentHook = func(_ context.Context, _ StartCollaborationAgentInput) {
		mu.Lock()
		count++
		mu.Unlock()
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := newCollaborationScheduler()
			s.scheduleOnce(context.Background(), c, wakeSignal)
		}()
	}
	wg.Wait()
	if count < 1 {
		t.Error("expected at least 1 start from concurrent calls")
	}
}
