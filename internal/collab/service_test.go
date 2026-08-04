package collab

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newTestService(t *testing.T, token string) (*Service, string, JoinResult) {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if _, err := service.CreateRoom(context.Background(), CreateRoomInput{RequestID: "create-1", ID: "room", Name: "Room", Token: token}); err != nil {
		t.Fatal(err)
	}
	joined, err := service.Join(context.Background(), JoinInput{RequestID: "join-a", Room: "room", Token: token, Member: memberDesc("a", "agent-a")})
	if err != nil {
		t.Fatal(err)
	}
	return service, dir, joined
}

func memberDesc(member, agent string) MemberDescriptor {
	return MemberDescriptor{ID: member, Name: strings.ToUpper(member), Role: "developer", Agent: AgentDescriptor{ID: agent, Name: agent}}
}

func env(requestID, member, session string, command Command) CommandEnvelope {
	return CommandEnvelope{RequestID: requestID, Room: "room", MemberID: member, Session: session, Command: command}
}

func requireCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	var value *Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func TestPersistenceIdempotencyAndSecrets(t *testing.T) {
	service, dir, joined := newTestService(t, "team-secret-value")
	first, err := service.Submit(context.Background(), env("chat-1", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "hello"}}))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.Submit(context.Background(), env("chat-1", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "hello"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate || duplicate.LatestSequence != first.LatestSequence || duplicate.EventIDs[0] != first.EventIDs[0] {
		t.Fatalf("duplicate receipt = %#v, first = %#v", duplicate, first)
	}
	_, err = service.Submit(context.Background(), env("chat-1", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "changed"}}))
	requireCode(t, err, CodeConflict)

	journal, err := os.ReadFile(filepath.Join(dir, "room", journalName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(journal, []byte("team-secret-value")) || bytes.Contains(journal, []byte(joined.ConnectionSession)) {
		t.Fatal("journal contains a plaintext token or connection session")
	}

	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	service = NewService(reopened)
	rejoined, err := service.Join(context.Background(), JoinInput{RequestID: "join-a", Room: "room", Token: "team-secret-value", Member: memberDesc("a", "agent-a")})
	if err != nil {
		t.Fatal(err)
	}
	if rejoined.ConnectionSession != joined.ConnectionSession {
		t.Fatalf("replayed session changed: %q != %q", rejoined.ConnectionSession, joined.ConnectionSession)
	}
	duplicate, err = service.Submit(context.Background(), env("chat-1", "a", rejoined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "hello"}}))
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("restart duplicate = %#v, %v", duplicate, err)
	}
	snapshot, err := service.Snapshot(context.Background(), "room", rejoined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Timeline) != 2 {
		t.Fatalf("timeline length = %d, want join + chat", len(snapshot.Timeline))
	}
}

func TestPublicValuesCannotMutateStoredState(t *testing.T) {
	service, _, joined := newTestService(t, "")
	receipt, err := service.Submit(context.Background(), env("chat-isolated", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "original"}}))
	if err != nil {
		t.Fatal(err)
	}
	receipt.EventIDs[0] = "tampered"
	snapshot, err := service.Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Timeline[len(snapshot.Timeline)-1].Chat.Text = "tampered"
	snapshot, err = service.Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Timeline[len(snapshot.Timeline)-1].Chat.Text; got != "original" {
		t.Fatalf("stored chat mutated to %q", got)
	}
	duplicate, err := service.Submit(context.Background(), env("chat-isolated", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "original"}}))
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.EventIDs[0] == "tampered" {
		t.Fatal("receipt slice mutated stored receipt")
	}
}

func TestRequestIDConflictAcrossOperations(t *testing.T) {
	service, _, joined := newTestService(t, "")
	_, err := service.Heartbeat(context.Background(), SessionInput{RequestID: "same", Room: "room", MemberID: "a", Session: joined.ConnectionSession})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Leave(context.Background(), SessionInput{RequestID: "same", Room: "room", MemberID: "a", Session: joined.ConnectionSession})
	requireCode(t, err, CodeConflict)
	_, err = service.Heartbeat(context.Background(), SessionInput{RequestID: "same", Room: "room", MemberID: "a", Session: "wrong-session"})
	requireCode(t, err, CodeConflict)

	_, err = service.Join(context.Background(), JoinInput{RequestID: "join-a", Room: "room", Token: "different", Member: memberDesc("other", "agent-other")})
	requireCode(t, err, CodeConflict)
}

func TestJoinExposesTypedResumeRequirement(t *testing.T) {
	service, _, _ := newTestService(t, "")
	_, err := service.Join(context.Background(), JoinInput{RequestID: "join-a-again", Room: "room", Member: memberDesc("a", "agent-a")})
	requireCode(t, err, CodeResumeNeeded)
}

func TestMemberCanRenameOwnAgentIdempotently(t *testing.T) {
	service, dir, joined := newTestService(t, "")
	command := Command{Type: CommandUpdateAgent, AgentUpdate: &UpdateAgentInput{Name: "Kite"}}
	first, err := service.Submit(context.Background(), env("rename-agent-1", "a", joined.ConnectionSession, command))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.Submit(context.Background(), env("rename-agent-1", "a", joined.ConnectionSession, command))
	if err != nil || !duplicate.Duplicate || duplicate.LatestSequence != first.LatestSequence {
		t.Fatalf("duplicate rename = %#v, %v", duplicate, err)
	}
	snapshot, err := service.Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Members) != 1 || snapshot.Members[0].Agent.Name != "Kite" {
		t.Fatalf("renamed Agent missing from snapshot: %+v", snapshot.Members)
	}

	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = NewService(reopened).Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil || snapshot.Members[0].Agent.Name != "Kite" {
		t.Fatalf("renamed Agent was not recovered: %+v, %v", snapshot.Members, err)
	}
	_, err = service.Submit(context.Background(), env("rename-agent-empty", "a", joined.ConnectionSession, Command{Type: CommandUpdateAgent, AgentUpdate: &UpdateAgentInput{}}))
	requireCode(t, err, CodeInvalid)
}

func TestAgentOwnershipRequestDecisionAndRunTransitions(t *testing.T) {
	service, dir, a := newTestService(t, "")
	b, err := service.Join(context.Background(), JoinInput{RequestID: "join-b", Room: "room", Member: memberDesc("b", "agent-b")})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Submit(context.Background(), env("request-1", "a", a.ConnectionSession, Command{Type: CommandCreateAgentRequest, AgentRequest: &CreateAgentRequestInput{TargetMemberID: "b", Instruction: "inspect the API"}}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(context.Background(), "room", a.ConnectionSession, created.LatestSequence-1)
	if err != nil {
		t.Fatal(err)
	}
	var item TimelineItem
	if err := json.Unmarshal(events[0].Payload, &item); err != nil {
		t.Fatal(err)
	}
	requestID := item.AgentRequest.ID
	_, err = service.Submit(context.Background(), env("decision-a", "a", a.ConnectionSession, Command{Type: CommandDecideAgentRequest, RequestDecision: &DecideAgentRequestInput{AgentRequestID: requestID, Decision: RequestAccepted}}))
	requireCode(t, err, CodeForbidden)
	if _, err := service.Submit(context.Background(), env("decision-b", "b", b.ConnectionSession, Command{Type: CommandDecideAgentRequest, RequestDecision: &DecideAgentRequestInput{AgentRequestID: requestID, Decision: RequestAccepted}})); err != nil {
		t.Fatal(err)
	}

	wrongRun := Command{Type: CommandPublishAgentRun, AgentRun: &PublishAgentRunInput{RunID: "run-1", AgentID: "agent-b", CommandID: "local-command", RequestRef: requestID, Status: RunRunning}}
	_, err = service.Submit(context.Background(), env("wrong-run", "a", a.ConnectionSession, wrongRun))
	requireCode(t, err, CodeForbidden)
	run := Command{Type: CommandPublishAgentRun, AgentRun: &PublishAgentRunInput{RunID: "run-1", AgentID: "agent-b", CommandID: "local-command", RequestRef: requestID, Status: RunRunning}}
	if _, err := service.Submit(context.Background(), env("run-start", "b", b.ConnectionSession, run)); err != nil {
		t.Fatal(err)
	}
	mutatedRun := *run.AgentRun
	mutatedRun.CommandID = "different-command"
	_, err = service.Submit(context.Background(), env("run-mutated", "b", b.ConnectionSession, Command{Type: CommandPublishAgentRun, AgentRun: &mutatedRun}))
	requireCode(t, err, CodeConflict)
	runningSnapshot, err := service.Snapshot(context.Background(), "room", b.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	for _, member := range runningSnapshot.Members {
		if member.ID == "b" && member.Agent.Status != AgentRunning {
			t.Fatalf("running member agent status = %q", member.Agent.Status)
		}
	}
	run.AgentRun.Status = RunCompleted
	if _, err := service.Submit(context.Background(), env("run-done", "b", b.ConnectionSession, run)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(context.Background(), env("run-done-again", "b", b.ConnectionSession, run)); err != nil {
		t.Fatalf("same terminal state must be safe: %v", err)
	}
	run.AgentRun.Status = RunRunning
	_, err = service.Submit(context.Background(), env("run-backward", "b", b.ConnectionSession, run))
	requireCode(t, err, CodeConflict)
	result := Command{Type: CommandPublishAgentResult, AgentResult: &PublishAgentResultInput{ResultID: "result-1", AgentID: "agent-b", RunID: "run-1", Summary: "done"}}
	_, err = service.Submit(context.Background(), env("wrong-result", "a", a.ConnectionSession, result))
	requireCode(t, err, CodeForbidden)
	if _, err := service.Submit(context.Background(), env("result", "b", b.ConnectionSession, result)); err != nil {
		t.Fatal(err)
	}
	result.AgentResult.Summary = "rewrite"
	_, err = service.Submit(context.Background(), env("result-rewrite", "b", b.ConnectionSession, result))
	requireCode(t, err, CodeConflict)
	result.AgentResult.ResultID = "result-2"
	_, err = service.Submit(context.Background(), env("result-same-revision", "b", b.ConnectionSession, result))
	requireCode(t, err, CodeConflict)

	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewService(reopened).Snapshot(context.Background(), "room", b.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	var foundB, foundRequest, foundRun bool
	for _, member := range snapshot.Members {
		if member.ID == "b" {
			foundB = member.Agent.Status == AgentIdle
		}
	}
	for _, value := range snapshot.Timeline {
		if value.AgentRequest != nil && value.AgentRequest.ID == requestID {
			foundRequest = value.AgentRequest.Status == RequestAccepted
		}
		if value.AgentRun != nil && value.AgentRun.ID == "run-1" {
			foundRun = value.AgentRun.Status == RunCompleted
		}
	}
	if !foundB || !foundRequest || !foundRun {
		t.Fatalf("replayed state missing: member=%v request=%v run=%v", foundB, foundRequest, foundRun)
	}
}

func TestContributionReferencesMustExist(t *testing.T) {
	service, _, joined := newTestService(t, "")
	command := Command{Type: CommandPublishContribution, Contribution: &PublishContributionInput{Kind: ContributionIssue, Body: "broken", Dependencies: []string{"missing"}}}
	_, err := service.Submit(context.Background(), env("dangling-dependency", "a", joined.ConnectionSession, command))
	requireCode(t, err, CodeNotFound)
	command.Contribution.Dependencies = nil
	command.Contribution.RelatedItem = "missing"
	_, err = service.Submit(context.Background(), env("dangling-related", "a", joined.ConnectionSession, command))
	requireCode(t, err, CodeNotFound)
}

func TestSweepStalePersistsOfflineAndDoesNotWriteNoop(t *testing.T) {
	service, dir, joined := newTestService(t, "")
	before, err := service.Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	noEffect, err := service.SweepStale(context.Background(), SweepInput{RequestID: "sweep-none", Room: "room", Before: time.Now().Add(-time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(noEffect.EventIDs) != 0 || noEffect.LatestSequence != before.LatestSequence {
		t.Fatalf("no-op sweep = %#v", noEffect)
	}
	receipt, err := service.SweepStale(context.Background(), SweepInput{RequestID: "sweep-stale", Room: "room", Before: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.EventIDs) != 1 {
		t.Fatalf("stale sweep = %#v", receipt)
	}

	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := reopened.room("room")
	if !ok || state.Members["a"].Status != MemberOffline || state.Members["a"].Agent.Status != AgentOffline {
		t.Fatalf("stale state was not replayed: %#v", state)
	}
	service = NewService(reopened)
	rejoined, err := service.Join(context.Background(), JoinInput{RequestID: "rejoin-a", Room: "room", Member: memberDesc("a", "agent-a"), ResumeSession: joined.ConnectionSession})
	if err != nil || !rejoined.Rejoined {
		t.Fatalf("rejoin after stale = %#v, %v", rejoined, err)
	}
}

func TestHeartbeatDoesNotGrowJournalAndOfflineRecoveryPersists(t *testing.T) {
	service, dir, joined := newTestService(t, "")
	base := joined.Member.LastSeenAt
	tick := 0
	service.now = func() time.Time { tick++; return base.Add(time.Duration(tick) * time.Second) }
	path := filepath.Join(dir, "room", journalName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	initialSequence := joined.LatestSequence
	for i := 0; i < 50; i++ {
		receipt, err := service.Heartbeat(context.Background(), SessionInput{RequestID: fmt.Sprintf("heartbeat-%d", i), Room: "room", MemberID: "a", Session: joined.ConnectionSession})
		if err != nil {
			t.Fatal(err)
		}
		if len(receipt.EventIDs) != 0 || receipt.LatestSequence != initialSequence {
			t.Fatalf("heartbeat %d persisted: %#v", i, receipt)
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("journal grew from %d to %d for ordinary heartbeats", before.Size(), after.Size())
	}
	events, err := service.Events(context.Background(), "room", joined.ConnectionSession, initialSequence)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("ordinary heartbeats emitted %d events", len(events))
	}

	sweep, err := service.SweepStale(context.Background(), SweepInput{RequestID: "sweep-for-heartbeat", Room: "room", Before: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Heartbeat(context.Background(), SessionInput{RequestID: "heartbeat-recover", Room: "room", MemberID: "a", Session: joined.ConnectionSession})
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered.EventIDs) != 1 || recovered.LatestSequence != sweep.LatestSequence+1 {
		t.Fatalf("offline recovery receipt = %#v", recovered)
	}
	recoveryEvents := eventsFrom(t, service, joined.ConnectionSession, sweep.LatestSequence)
	if len(recoveryEvents) != 1 || recoveryEvents[0].Type != "member.online" {
		t.Fatalf("recovery events = %#v", recoveryEvents)
	}
	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	state, ok := reopened.room("room")
	if !ok || state.Members["a"].Status != MemberOnline {
		t.Fatalf("online recovery was not replayed: %#v", state)
	}
}

func eventsFrom(t *testing.T, service *Service, session string, after uint64) []RoomEvent {
	t.Helper()
	values, err := service.Events(context.Background(), "room", session, after)
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestLeaveCanRejoinButCannotMutateWhileOffline(t *testing.T) {
	service, _, joined := newTestService(t, "")
	if _, err := service.Leave(context.Background(), SessionInput{RequestID: "leave", Room: "room", MemberID: "a", Session: joined.ConnectionSession}); err != nil {
		t.Fatal(err)
	}
	_, err := service.Submit(context.Background(), env("offline-chat", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "no"}}))
	requireCode(t, err, CodeUnauthorized)
	rejoined, err := service.Join(context.Background(), JoinInput{RequestID: "rejoin", Room: "room", Member: memberDesc("a", "agent-a"), ResumeSession: joined.ConnectionSession})
	if err != nil || !rejoined.Rejoined {
		t.Fatalf("rejoin = %#v, %v", rejoined, err)
	}
}

func TestJournalTailRepairSurvivesAnotherRestart(t *testing.T) {
	service, dir, joined := newTestService(t, "")
	path := filepath.Join(dir, "room", journalName)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":{"partial":`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	service = NewService(reopened)
	if _, err := service.Submit(context.Background(), env("after-repair", "a", joined.ConnectionSession, Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "survives"}})); err != nil {
		t.Fatal(err)
	}
	reopened, err = OpenFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewService(reopened).Snapshot(context.Background(), "room", joined.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.Timeline[len(snapshot.Timeline)-1].Chat.Text; got != "survives" {
		t.Fatalf("last chat = %q", got)
	}
}

func TestHTTPJoinSnapshotCommandsAndSSE(t *testing.T) {
	store, err := OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(store)
	if _, err := service.CreateRoom(context.Background(), CreateRoomInput{RequestID: "create", ID: "room", Name: "Room"}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(service))
	defer server.Close()
	joinBody, _ := json.Marshal(JoinInput{RequestID: "join", Room: "room", Member: memberDesc("a", "agent-a")})
	response, err := http.Post(server.URL+"/collab/v1/join", "application/json", bytes.NewReader(joinBody))
	if err != nil {
		t.Fatal(err)
	}
	var joined JoinResult
	if err := json.NewDecoder(response.Body).Decode(&joined); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	streamRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/collab/v1/rooms/room/stream?afterSequence="+strconv.FormatUint(joined.LatestSequence, 10), nil)
	streamRequest.Header.Set("Authorization", "Bearer "+joined.ConnectionSession)
	stream, err := http.DefaultClient.Do(streamRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()

	commandBody, _ := json.Marshal(env("chat", "a", "", Command{Type: CommandPostChat, Chat: &PostChatInput{Text: "over HTTP"}}))
	commandRequest, _ := http.NewRequest(http.MethodPost, server.URL+"/collab/v1/rooms/room/commands", bytes.NewReader(commandBody))
	commandRequest.Header.Set("Authorization", "Bearer "+joined.ConnectionSession)
	commandRequest.Header.Set("Content-Type", "application/json")
	commandResponse, err := http.DefaultClient.Do(commandRequest)
	if err != nil {
		t.Fatal(err)
	}
	if commandResponse.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(commandResponse.Body)
		t.Fatalf("command status %d: %s", commandResponse.StatusCode, body)
	}
	_ = commandResponse.Body.Close()

	lines := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				lines <- scanner.Text()
				return
			}
		}
	}()
	select {
	case line := <-lines:
		if !strings.Contains(line, `"type":"chat.posted"`) {
			t.Fatalf("SSE = %s", line)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for SSE event")
	}

	snapshotRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/collab/v1/rooms/room/snapshot", nil)
	snapshotRequest.Header.Set("X-Collab-Session", joined.ConnectionSession)
	snapshotResponse, err := http.DefaultClient.Do(snapshotRequest)
	if err != nil {
		t.Fatal(err)
	}
	if snapshotResponse.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status = %d", snapshotResponse.StatusCode)
	}
	_ = snapshotResponse.Body.Close()
}

func TestHTTPBodyLimitAndEmptyToken(t *testing.T) {
	service, _, _ := newTestService(t, "")
	server := httptest.NewServer(NewHandler(service))
	defer server.Close()
	response, err := http.Post(server.URL+"/collab/v1/join", "application/json", strings.NewReader(strings.Repeat("x", maxHTTPBody+1)))
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d", response.StatusCode)
	}
	_ = response.Body.Close()
	joined, err := service.Join(context.Background(), JoinInput{RequestID: "join-b", Room: "room", Token: "   ", Member: memberDesc("b", "agent-b")})
	if err != nil || joined.ConnectionSession == "" {
		t.Fatalf("empty token join = %#v, %v", joined, err)
	}
}

func TestHubCoalescesWithoutBlocking(t *testing.T) {
	hub := NewHub()
	wake, cancel := hub.Subscribe("room")
	defer cancel()
	for i := 0; i < 100000; i++ {
		hub.Publish("room")
	}
	select {
	case <-wake:
	case <-time.After(time.Second):
		t.Fatal("missing coalesced wakeup")
	}
}

func TestHubSubscriberLimit(t *testing.T) {
	hub := NewHub()
	cancels := make([]func(), 0, MaxSubscribersPerRoom)
	for i := 0; i < MaxSubscribersPerRoom; i++ {
		_, cancel, err := hub.TrySubscribe("room")
		if err != nil {
			t.Fatalf("subscriber %d: %v", i, err)
		}
		cancels = append(cancels, cancel)
	}
	if _, _, err := hub.TrySubscribe("room"); err == nil {
		t.Fatal("subscriber limit was not enforced")
	}
	for _, cancel := range cancels {
		cancel()
	}
}

func TestFileOfferPersistsAndOwnerCanRevoke(t *testing.T) {
	service, _, owner := newTestService(t, "")
	const hash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	offer := CommandEnvelope{RequestID: "file-offer-1", Room: "room", MemberID: owner.Member.ID, Session: owner.ConnectionSession, Command: Command{Type: CommandOfferFile, FileOffer: &OfferFileInput{
		FileID: "file-1", Name: "report.txt", Size: 3, MIME: "text/plain", SHA256: hash, ManifestHash: hash, ChunkSize: MinFileChunkSize, ChunkCount: 1,
	}}}
	receipt, err := service.Submit(context.Background(), offer)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := service.Submit(context.Background(), offer)
	if err != nil || !duplicate.Duplicate || duplicate.LatestSequence != receipt.LatestSequence {
		t.Fatalf("duplicate = %+v, %v", duplicate, err)
	}
	file, err := service.File(context.Background(), "room", "file-1")
	if err != nil || file.OwnerID != owner.Member.ID || file.Name != "report.txt" || file.Revision != 1 {
		t.Fatalf("file = %+v, %v", file, err)
	}
	revoke := CommandEnvelope{RequestID: "file-revoke-1", Room: "room", MemberID: owner.Member.ID, Session: owner.ConnectionSession, Command: Command{Type: CommandRevokeFile, FileRevoke: &RevokeFileInput{FileID: "file-1"}}}
	if _, err := service.Submit(context.Background(), revoke); err != nil {
		t.Fatal(err)
	}
	if _, err := service.File(context.Background(), "room", "file-1"); err == nil {
		t.Fatal("revoked file remained transferable")
	}
	snapshot, err := service.Snapshot(context.Background(), "room", owner.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	var found *FileOffer
	for i := range snapshot.Timeline {
		if snapshot.Timeline[i].ID == "file-1" {
			found = snapshot.Timeline[i].File
		}
	}
	if found == nil || found.RevokedAt == nil || found.Revision != 2 {
		t.Fatalf("revoked timeline = %+v", found)
	}
}

func TestFileOfferRejectsUnsafeMetadataAndForeignRevoke(t *testing.T) {
	service, _, owner := newTestService(t, "token")
	other, err := service.Join(context.Background(), JoinInput{RequestID: "join-b", Room: "room", Token: "token", Member: memberDesc("b", "agent-b")})
	if err != nil {
		t.Fatal(err)
	}
	const hash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	bad := CommandEnvelope{RequestID: "bad-file", Room: "room", MemberID: owner.Member.ID, Session: owner.ConnectionSession, Command: Command{Type: CommandOfferFile, FileOffer: &OfferFileInput{FileID: "bad", Name: "../secret", SHA256: hash, ManifestHash: hash, ChunkSize: MinFileChunkSize}}}
	if _, err := service.Submit(context.Background(), bad); err == nil {
		t.Fatal("unsafe filename was accepted")
	}
	good := CommandEnvelope{RequestID: "good-file", Room: "room", MemberID: owner.Member.ID, Session: owner.ConnectionSession, Command: Command{Type: CommandOfferFile, FileOffer: &OfferFileInput{FileID: "owned", Name: "safe.bin", Size: 1, SHA256: hash, ManifestHash: hash, ChunkSize: MinFileChunkSize, ChunkCount: 1}}}
	if _, err := service.Submit(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	foreign := CommandEnvelope{RequestID: "foreign-revoke", Room: "room", MemberID: other.Member.ID, Session: other.ConnectionSession, Command: Command{Type: CommandRevokeFile, FileRevoke: &RevokeFileInput{FileID: "owned"}}}
	if _, err := service.Submit(context.Background(), foreign); err == nil {
		t.Fatal("foreign revoke was accepted")
	}
}
