package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/collab"
)

func TestCollaborationSharedFileOriginServesVerifiedChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.bin")
	if err := os.WriteFile(path, []byte("shared-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &desktopCollaboration{app: &App{}, shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{}, transferCancel: map[string]context.CancelFunc{}, outboxFailures: map[string]string{}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{}}
	share, err := c.prepareSharedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	share.Room, share.OwnerID, share.Status = "room", "owner", "available"
	c.shares[share.FileID] = share

	store, err := collab.OpenFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := collab.NewService(store)
	if _, err := service.CreateRoom(context.Background(), collab.CreateRoomInput{RequestID: "create", ID: "room", Name: "Room"}); err != nil {
		t.Fatal(err)
	}
	owner, err := service.Join(context.Background(), collab.JoinInput{RequestID: "join-owner", Room: "room", Member: collab.MemberDescriptor{ID: "owner", Name: "Owner", Agent: collab.AgentDescriptor{ID: "owner-agent", Name: "Agent"}}})
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := service.Join(context.Background(), collab.JoinInput{RequestID: "join-receiver", Room: "room", Member: collab.MemberDescriptor{ID: "receiver", Name: "Receiver", Agent: collab.AgentDescriptor{ID: "receiver-agent", Name: "Agent"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Submit(context.Background(), collab.CommandEnvelope{RequestID: share.FileID + ":offer", Room: "room", MemberID: "owner", Session: owner.ConnectionSession, Command: collab.Command{Type: collab.CommandOfferFile, FileOffer: &collab.OfferFileInput{FileID: share.FileID, Name: share.Name, Size: share.Size, MIME: share.MIME, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes)}}}); err != nil {
		t.Fatal(err)
	}
	host := httptest.NewServer(collab.NewHandler(service))
	defer host.Close()
	peer := func(member, session string) *httpCollaborationPeer {
		return &httpCollaborationPeer{baseURL: host.URL, client: &http.Client{Timeout: time.Second}, streamClient: &http.Client{}, room: "room", member: member, session: session}
	}
	ownerPeer := peer("owner", owner.ConnectionSession)
	c.conn = &collaborationConnection{filePeer: ownerPeer, room: "room", memberID: "owner"}
	if err := c.ensureFileOrigin(c.conn); err != nil {
		t.Fatal(err)
	}
	defer c.closeFileTransfers()
	if err := c.registerFileOrigin(context.Background(), share.FileID); err != nil {
		t.Fatal(err)
	}
	receiverPeer := peer("receiver", receiver.ConnectionSession)
	ticket, manifest, err := receiverPeer.fetchFileManifest(context.Background(), share.FileID)
	if err != nil || manifest.FileID != share.FileID || len(manifest.ChunkHashes) != 1 {
		t.Fatalf("manifest = %+v, ticket = %+v, err = %v", manifest, ticket, err)
	}
	data, err := receiverPeer.fetchFileChunk(context.Background(), ticket, 0)
	if err != nil || string(data) != "shared-content" {
		t.Fatalf("chunk = %q, %v", data, err)
	}
	snapshot, err := service.Snapshot(context.Background(), "room", receiver.ConnectionSession)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "received.bin")
	receiverRuntime := &desktopCollaboration{
		app: &App{}, state: CollaborationState{Status: "connected", Room: "room", MemberID: "receiver", SessionID: "receiver-session", Snapshot: snapshot},
		conn:   &collaborationConnection{filePeer: receiverPeer, room: "room", memberID: "receiver"},
		shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{}, transferCancel: map[string]context.CancelFunc{}, outboxFailures: map[string]string{}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
	}
	if _, err := receiverRuntime.receiveFile(context.Background(), share.FileID, destination); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		receiverRuntime.mu.RLock()
		status := receiverRuntime.transfers[share.FileID].Status
		receiverRuntime.mu.RUnlock()
		if status == "completed" {
			break
		}
		if status == "failed" || status == "waiting_sender" || time.Now().After(deadline) {
			t.Fatalf("receive status = %s", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	received, err := os.ReadFile(destination)
	if err != nil || string(received) != "shared-content" {
		t.Fatalf("received = %q, %v", received, err)
	}
	receiverRuntime.closeFileTransfers()

	changedAt := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, changedAt, changedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := receiverPeer.fetchFileChunk(context.Background(), ticket, 0); err == nil {
		t.Fatal("changed source remained downloadable")
	}
}

func TestPrepareSharedFileRejectsDirectory(t *testing.T) {
	c := &desktopCollaboration{}
	if _, err := c.prepareSharedFile(t.TempDir()); err == nil {
		t.Fatal("directory was accepted")
	}
}

func TestCompletedFileBytesHandlesLastShortChunk(t *testing.T) {
	if got := completedFileBytes([]bool{true, false, true}, 10, 4); got != 6 {
		t.Fatalf("completed bytes = %d", got)
	}
}

func TestCollaborationFileTransferRestoresPausedWithBitmap(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "collaboration.json")
	first := &desktopCollaboration{
		ownerSessionID: "session", persistPath: statePath,
		state:  CollaborationState{Status: "connected", Room: "room", MemberID: "receiver", SessionID: "session"},
		shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{
			"file": {ID: "transfer", FileID: "file", Direction: "receive", Name: "file.bin", Status: "downloading", Transferred: 4, Total: 10, Destination: filepath.Join(t.TempDir(), "file.bin"), Completed: []bool{true, false, false}},
		},
		transferCancel: map[string]context.CancelFunc{}, outboxFailures: map[string]string{}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
	}
	first.mu.Lock()
	first.persistLocked()
	first.mu.Unlock()
	second := &desktopCollaboration{
		ownerSessionID: "session", persistPath: statePath,
		state:  CollaborationState{Status: "disconnected", SessionID: "session"},
		shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{}, transferCancel: map[string]context.CancelFunc{}, outboxFailures: map[string]string{}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
	}
	second.loadPersisted()
	transfer := second.transfers["file"]
	if transfer == nil || transfer.Status != "paused" || len(transfer.Completed) != 3 || !transfer.Completed[0] || transfer.PartPath != transfer.Destination+".wg2part" {
		t.Fatalf("restored transfer = %+v", transfer)
	}
}
