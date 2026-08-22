package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"workground2/internal/collab"
)

func TestDesktopOfflineOutboxesConvergeAfterHostRecovery(t *testing.T) {
	store, err := collab.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := collab.NewService(store)
	if _, err := service.CreateRoom(context.Background(), collab.CreateRoomInput{RequestID: "create", ID: "room", Name: "Room"}); err != nil {
		t.Fatal(err)
	}
	join := func(member, agent string) collab.JoinResult {
		joined, err := service.Join(context.Background(), collab.JoinInput{RequestID: "join-" + member, Room: "room", Member: collab.MemberDescriptor{ID: member, Name: member, Agent: collab.AgentDescriptor{ID: agent, Name: agent}}})
		if err != nil {
			t.Fatal(err)
		}
		return joined
	}
	a, b := join("a", "agent-a"), join("b", "agent-b")
	base, err := service.Snapshot(context.Background(), "room", a.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	_, runtimeA, _ := newTestDesktopCollaboration(t)
	_, runtimeB, _ := newTestDesktopCollaboration(t)
	prepare := func(runtime *desktopCollaboration, member, agent, sessionID string) {
		runtime.state = CollaborationState{Status: "failed", Room: "room", MemberID: member, AgentID: agent, SessionID: sessionID, Snapshot: base}
	}
	prepare(runtimeA, "a", "agent-a", "session-a")
	prepare(runtimeB, "b", "agent-b", "session-b")
	queue := func(runtime *desktopCollaboration, prefix string) {
		for index := 1; index <= 2; index++ {
			text := fmt.Sprintf("%s%d", prefix, index)
			result, err := runtime.submit(context.Background(), "offline-"+text, collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: text}})
			if err != nil || !result.Queued {
				t.Fatalf("queue %s: result=%+v err=%v", text, result, err)
			}
		}
	}
	queue(runtimeA, "A")
	queue(runtimeB, "B")
	connect := func(runtime *desktopCollaboration, joined collab.JoinResult, member, agent, sessionID string) *collaborationConnection {
		peer := &serviceCollaborationPeer{service: service, hub: service.Hub(), room: "room", member: member, session: joined.ConnectionSession}
		conn := &collaborationConnection{peer: peer, mode: "client", room: "room", memberID: member, agentID: agent, sessionID: sessionID, connectionSession: joined.ConnectionSession, initialSnapshot: base}
		runtime.conn = conn
		runtime.state.Status = "reconnecting"
		return conn
	}
	connA := connect(runtimeA, a, "a", "agent-a", "session-a")
	connB := connect(runtimeB, b, "b", "agent-b", "session-b")
	var wg sync.WaitGroup
	for _, value := range []struct {
		runtime *desktopCollaboration
		conn    *collaborationConnection
	}{{runtimeA, connA}, {runtimeB, connB}} {
		value := value
		wg.Add(1)
		go func() {
			defer wg.Done()
			value.runtime.syncConnection(context.Background(), value.conn)
		}()
	}
	wg.Wait()
	// One Client can finish its first catch-up while the other is committing.
	// A second ordinary catch-up must converge without replaying either Outbox.
	runtimeA.syncConnection(context.Background(), connA)
	runtimeB.syncConnection(context.Background(), connB)
	assertConverged := func(runtime *desktopCollaboration) map[string]uint64 {
		state := runtime.snapshot()
		if state.OutboxCount != 0 {
			t.Fatalf("outbox did not drain: %+v", state.Outbox)
		}
		positions := map[string]uint64{}
		for _, item := range state.Snapshot.Timeline {
			if item.Chat != nil {
				positions[item.Chat.Text] = item.Sequence
			}
		}
		if len(positions) != 4 || positions["A1"] >= positions["A2"] || positions["B1"] >= positions["B2"] {
			t.Fatalf("offline replay did not preserve per-client FIFO: %v", positions)
		}
		return positions
	}
	positionsA, positionsB := assertConverged(runtimeA), assertConverged(runtimeB)
	for text, sequence := range positionsA {
		if positionsB[text] != sequence {
			t.Fatalf("clients diverged for %s: A=%d B=%d", text, sequence, positionsB[text])
		}
	}
}

func TestAssembleCollaborationSnapshotRetriesAndValidatesChunks(t *testing.T) {
	snapshot := collab.Snapshot{
		Room: collab.Room{ID: "room", Name: "Room", LatestSequence: 7}, LatestSequence: 7,
		Timeline: []collab.TimelineItem{{ID: "message-7", Sequence: 7, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "message-7", Text: "chunked", Revision: 1}}},
	}
	manifest, chunks := testCollaborationSnapshotChunks(t, snapshot, 37)
	attempts := map[int]int{}
	var mu sync.Mutex
	got, err := assembleCollaborationSnapshot(context.Background(), manifest, func(_ context.Context, snapshotID string, index int) (collab.SnapshotChunk, error) {
		mu.Lock()
		attempts[index]++
		attempt := attempts[index]
		mu.Unlock()
		if index == 1 && attempt == 1 {
			return collab.SnapshotChunk{}, &collaborationTransportError{message: "temporary chunk failure", retryable: true}
		}
		return collab.SnapshotChunk{SnapshotID: snapshotID, Index: index, SHA256: manifest.Chunks[index].SHA256, Data: chunks[index]}, nil
	})
	if err != nil || got.LatestSequence != snapshot.LatestSequence || len(got.Timeline) != 1 || attempts[1] != 2 {
		t.Fatalf("assembled snapshot=%+v attempts=%v err=%v", got, attempts, err)
	}
}

func TestAssembleCollaborationSnapshotRejectsCorruption(t *testing.T) {
	snapshot := collab.Snapshot{Room: collab.Room{ID: "room", LatestSequence: 3}, LatestSequence: 3}
	manifest, chunks := testCollaborationSnapshotChunks(t, snapshot, 32)
	_, err := assembleCollaborationSnapshot(context.Background(), manifest, func(_ context.Context, snapshotID string, index int) (collab.SnapshotChunk, error) {
		data := append([]byte(nil), chunks[index]...)
		data[0] ^= 0xff
		return collab.SnapshotChunk{SnapshotID: snapshotID, Index: index, SHA256: manifest.Chunks[index].SHA256, Data: data}, nil
	})
	if err == nil {
		t.Fatal("corrupt snapshot chunks were accepted")
	}
}

func TestProjectCollaborationEventsAppliesTimelineWithoutSnapshot(t *testing.T) {
	base := collab.Snapshot{
		Room: collab.Room{ID: "room", LatestSequence: 2}, LatestSequence: 2,
		Members: []collab.Member{{ID: "member", Status: collab.MemberOnline, Agent: collab.AgentDescriptor{ID: "agent", Status: collab.AgentIdle}}},
	}
	item := collab.TimelineItem{ID: "message-3", Sequence: 3, Type: collab.TimelineChat, Chat: &collab.ChatMessage{ID: "message-3", AuthorID: "member", Text: "project me", Revision: 1, CreatedAt: time.Now().UTC()}}
	payload, _ := json.Marshal(item)
	projected, err := projectCollaborationEvents(base, []collab.RoomEvent{{EventID: "event-3", Room: "room", Sequence: 3, Type: "chat.posted", Payload: payload}})
	if err != nil || projected.LatestSequence != 3 || len(projected.Timeline) != 1 || projected.Timeline[0].Chat.Text != "project me" {
		t.Fatalf("projected=%+v err=%v", projected, err)
	}
	if len(base.Timeline) != 0 || base.LatestSequence != 2 {
		t.Fatalf("projection mutated the installed base snapshot: %+v", base)
	}
	if _, err := projectCollaborationEvents(projected, []collab.RoomEvent{{Room: "room", Sequence: 4, Type: "agent.updated", Payload: payload}}); err == nil {
		t.Fatal("profile event without a member projection did not request Snapshot fallback")
	}
}

func testCollaborationSnapshotChunks(t *testing.T, snapshot collab.Snapshot, chunkBytes int) (collab.SnapshotManifest, [][]byte) {
	t.Helper()
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	manifest := collab.SnapshotManifest{
		Version: collab.SnapshotFormatVersion, SnapshotID: "snapshot-test", Room: snapshot.Room.ID,
		BaseSequence: snapshot.LatestSequence, Encoding: "json", TotalBytes: int64(len(encoded)), RootSHA256: hashCollaborationSnapshot(encoded), ChunkSizeBytes: chunkBytes,
	}
	var chunks [][]byte
	for offset, index := 0, 0; offset < len(encoded); offset, index = offset+chunkBytes, index+1 {
		end := offset + chunkBytes
		if end > len(encoded) {
			end = len(encoded)
		}
		data := append([]byte(nil), encoded[offset:end]...)
		chunks = append(chunks, data)
		manifest.Chunks = append(manifest.Chunks, collab.SnapshotChunkMeta{Index: index, Offset: int64(offset), Size: len(data), SHA256: hashCollaborationSnapshot(data)})
	}
	if len(chunks) == 0 {
		t.Fatal(fmt.Errorf("test snapshot encoded to no chunks"))
	}
	return manifest, chunks
}
