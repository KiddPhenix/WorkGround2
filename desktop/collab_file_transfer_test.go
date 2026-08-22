package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"workground2/internal/collab"
)

func TestRegisterFileOriginAllowsRelayWithoutLocalHTTPOrigin(t *testing.T) {
	c := &desktopCollaboration{
		state:          CollaborationState{Room: "room", MemberID: "owner"},
		shareAuthority: "share-authority",
		shares: map[string]collaborationSharedFile{
			"file": {FileID: "file", Room: "room", OwnerID: "owner", ShareAuthority: "share-authority", Status: "available"},
		},
	}
	c.conn = &collaborationConnection{
		filePeer:          &relayCollaborationPeer{fileSource: c, room: "room"},
		room:              "room",
		memberID:          "owner",
		shareAuthorityKey: "share-authority",
	}

	if err := c.registerFileOrigin(context.Background(), "file"); err != nil {
		t.Fatalf("register Relay file origin: %v", err)
	}
}

func TestFallbackFilePeerSkipsTypedNilPrimary(t *testing.T) {
	offer, fallback := testFileOffer("relay-only", "payload.json", "other", []byte(`{"ok":true}`), collab.MinFileChunkSize)
	var primary *httpCollaborationPeer
	peer := &fallbackCollaborationFilePeer{primary: primary, fallback: fallback}

	if collaborationFilePeerAvailable(primary) {
		t.Fatal("typed nil HTTP peer reported available")
	}
	if collaborationFilePeerNeedsOrigin(peer) {
		t.Fatal("typed nil HTTP peer incorrectly requires a local file origin")
	}
	ticket, manifest, err := peer.fetchFileManifest(context.Background(), offer.ID, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.File.ID != offer.ID || manifest.FileID != offer.ID {
		t.Fatalf("fallback manifest = %+v, ticket = %+v", manifest, ticket)
	}
}

func TestRegisterFileOriginRequiresLocalHTTPOrigin(t *testing.T) {
	c := &desktopCollaboration{
		shareAuthority: "share-authority",
		shares: map[string]collaborationSharedFile{
			"file": {FileID: "file", ShareAuthority: "share-authority"},
		},
	}
	c.conn = &collaborationConnection{
		filePeer:          &httpCollaborationPeer{},
		shareAuthorityKey: "share-authority",
	}

	if err := c.registerFileOrigin(context.Background(), "file"); err == nil || err.Error() != "file origin is unavailable" {
		t.Fatalf("register HTTP file origin error = %v", err)
	}
}

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
	share.Room, share.ShareAuthority, share.OwnerID, share.Status = "room", "share-authority", "owner", "available"
	c.shares[share.FileID] = share
	c.roomInstance, c.shareAuthority = "room-instance", "share-authority"

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
	c.conn = &collaborationConnection{filePeer: ownerPeer, room: "room", memberID: "owner", roomInstanceKey: "room-instance", shareAuthorityKey: "share-authority"}
	if err := c.ensureFileOrigin(c.conn); err != nil {
		t.Fatal(err)
	}
	defer c.closeFileTransfers()
	if address := c.fileOrigin.listener.Addr().String(); !strings.HasPrefix(address, "127.0.0.1:") {
		t.Fatalf("test file origin address = %q, want loopback", address)
	}
	if err := c.registerFileOrigin(context.Background(), share.FileID); err != nil {
		t.Fatal(err)
	}
	receiverPeer := peer("receiver", receiver.ConnectionSession)
	ticket, manifest, err := receiverPeer.fetchFileManifest(context.Background(), share.FileID, 4096, true)
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
		app: &App{}, roomInstance: "room-instance", shareAuthority: "receiver-authority", state: CollaborationState{Status: "connected", Room: "room", MemberID: "receiver", SessionID: "receiver-session", Snapshot: snapshot},
		conn:   &collaborationConnection{filePeer: receiverPeer, room: "room", memberID: "receiver", roomInstanceKey: "room-instance", shareAuthorityKey: "receiver-authority"},
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

func TestAutomaticFileTransferRestoresForReconnect(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "collaboration.json")
	first := &desktopCollaboration{
		ownerSessionID: "session", persistPath: statePath,
		state:  CollaborationState{Status: "connected", Room: "room", MemberID: "receiver", SessionID: "session"},
		shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{
			"file": {ID: "transfer", FileID: "file", Room: "room", SHA256: strings.Repeat("a", 64), Direction: "receive", Name: "file.bin", Status: "downloading", Total: 10, Automatic: true},
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
	if transfer == nil || transfer.Status != "waiting_sender" || !transfer.Automatic || transfer.PausedByUser {
		t.Fatalf("restored automatic transfer = %+v", transfer)
	}
}

func TestCompletedReceivedFilePathRequiresCompletedRegularFile(t *testing.T) {
	data := []byte("received")
	path := filepath.Join(t.TempDir(), "received.txt")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	offer, _ := testFileOffer("file", "received.txt", "other", data, 4)
	c := &desktopCollaboration{
		roomInstance: "room-instance",
		state:        CollaborationState{Room: "room", Snapshot: collab.Snapshot{Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}},
		transfers:    map[string]*CollaborationFileTransfer{"file": transferForTestOffer(offer, "room", path, "")},
	}
	got, err := c.completedReceivedFilePath("file")
	if err != nil || got != path {
		t.Fatalf("completed path = %q, %v", got, err)
	}
	c.transfers["file"].Status = "failed"
	if _, err := c.completedReceivedFilePath("file"); err == nil {
		t.Fatal("incomplete received file was accepted")
	}
	c.transfers["file"].Status = "completed"
	c.transfers["file"].Direction = "share"
	if _, err := c.completedReceivedFilePath("file"); err == nil {
		t.Fatal("shared source file was accepted as a received file")
	}
	c.transfers["file"].Direction = "receive"
	c.transfers["file"].Destination = t.TempDir()
	if _, err := c.completedReceivedFilePath("file"); err == nil {
		t.Fatal("received directory was accepted as a file")
	}
}

func TestCollaborationFileActionsUseAuthoritativeDestination(t *testing.T) {
	data := []byte("received")
	path := filepath.Join(t.TempDir(), "received.txt")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	offer, _ := testFileOffer("file", "received.txt", "other", data, 4)
	runtime := &desktopCollaboration{
		roomInstance: "room-instance",
		state:        CollaborationState{Room: "room", Snapshot: collab.Snapshot{Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}},
		transfers:    map[string]*CollaborationFileTransfer{"file": transferForTestOffer(offer, "room", path, "")},
	}
	app := &App{collaborations: map[string]*desktopCollaboration{"session": runtime}}
	oldOpen, oldReveal := openCollaborationFilePath, revealCollaborationFilePath
	t.Cleanup(func() { openCollaborationFilePath, revealCollaborationFilePath = oldOpen, oldReveal })
	var opened, revealed string
	openCollaborationFilePath = func(value string) error { opened = value; return nil }
	revealCollaborationFilePath = func(value string) error { revealed = value; return nil }
	input := CollaborationFileActionInput{SessionID: "session", FileID: "file"}
	if err := app.OpenCollaborationFile(input); err != nil {
		t.Fatal(err)
	}
	if err := app.RevealCollaborationFile(input); err != nil {
		t.Fatal(err)
	}
	if opened != path || revealed != path {
		t.Fatalf("opened = %q, revealed = %q", opened, revealed)
	}
}

func TestSanitizeRoomAttachmentName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"simple", "hello.png", "hello.png", false},
		{"spaces", "  my file.jpg  ", "my_file.jpg", false},
		{"path_separator", "a/b.png", "", true},
		{"backslash", "a\\b.png", "", true},
		{"null_byte", "a\x00b.png", "", true},
		{"empty", "", "", true},
		{"control_chars", "ab\x01c.png", "ab_c.png", false},
		{"reserved_punctuation", "con:<.txt", "con__.txt", false},
		{"agent_token_punctuation", "README!", "README_", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeRoomAttachmentName(tt.input)
			if tt.wantErr != (err != nil) {
				t.Fatalf("sanitize error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("sanitized = %q, want %q", got, tt.want)
			}
		})
	}
	for _, value := range []string{
		strings.Repeat("a", 300) + ".png",
		"a." + strings.Repeat("x", 300),
		strings.Repeat("图", 100) + ".png",
	} {
		got, err := sanitizeRoomAttachmentName(value)
		if err != nil || len(got) > maxRoomAttachmentNameBytes || !utf8.ValidString(got) {
			t.Fatalf("long name sanitize = %q, %v", got, err)
		}
	}
}

func TestRoomAttachmentDestinationIsDeterministicAndUnique(t *testing.T) {
	root := t.TempDir()
	sha := hex.EncodeToString(sha256.New().Sum(nil))
	dest1, rel1, err := roomAttachmentDestination(root, "room", "file-a", "my_file.png", sha)
	if err != nil {
		t.Fatal(err)
	}
	dest2, rel2, err := roomAttachmentDestination(root, "room", "file-a", "my_file.png", sha)
	if err != nil {
		t.Fatal(err)
	}
	dest3, rel3, err := roomAttachmentDestination(root, "room", "file-b", "my_file.png", sha)
	if err != nil {
		t.Fatal(err)
	}
	if dest1 != dest2 || rel1 != rel2 {
		t.Fatalf("destination is not deterministic: %q/%q vs %q/%q", dest1, rel1, dest2, rel2)
	}
	if dest1 == dest3 || rel1 == rel3 {
		t.Fatal("different file IDs shared one destination")
	}
	if !strings.HasPrefix(filepath.Clean(dest1), filepath.Join(root, filepath.FromSlash(roomAttachmentsRelPath))+string(filepath.Separator)) {
		t.Fatalf("destination escaped attachments directory: %q", dest1)
	}
	if strings.Contains(rel1, " ") {
		t.Fatalf("workspace reference contains whitespace: %q", rel1)
	}
}

func TestRoomAttachmentDestinationRejectsSymlinkParent(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(root, ".workground2")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, _, err := roomAttachmentDestination(root, "room", "file", "file.png", strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("symlinked attachment parent was accepted")
	}
}

func TestNewDesktopCollaborationSeparatesWorkspaceAndSessionPath(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspace := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	app := NewApp()
	tab := app.createTabEntryWithID("project", workspace, "", "room-tab")
	tab.SessionID = "room-session"
	tab.SessionPath = sessionPath
	app.tabs[tab.ID] = tab
	app.trackSession(tab)
	runtime := newDesktopCollaboration(app, tab.SessionID)
	if runtime.ownerWorkspaceRoot != normalizeProjectRoot(workspace) || runtime.ownerSessionPath != sessionRuntimeKey(sessionPath) {
		t.Fatalf("runtime roots = workspace %q, session %q", runtime.ownerWorkspaceRoot, runtime.ownerSessionPath)
	}
	prompt := &collaborationPromptSession{}
	tab.Ctrl = prompt
	if err := runtime.stopAgent(tab.SessionID); err != nil || prompt.cancels != 1 {
		t.Fatalf("SessionID Agent routing failed: cancels=%d err=%v", prompt.cancels, err)
	}
}

func TestAutoReceiveUsesWorkspaceNotSessionPath(t *testing.T) {
	workspace := t.TempDir()
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sessionPath, []byte("session"), 0o600); err != nil {
		t.Fatal(err)
	}
	offer, peer := testFileOffer("small", "hello world.txt", "other", []byte("hello"), 4)
	c := testAutoReceiveRuntime(workspace, sessionPath, offer, peer)
	c.maybeAutoReceiveFiles()
	transfer := waitForTransferStatus(t, c, offer.ID, "completed")
	if !strings.HasPrefix(filepath.Clean(transfer.Destination), filepath.Clean(workspace)+string(filepath.Separator)) {
		t.Fatalf("received outside workspace: %q", transfer.Destination)
	}
	if info, err := os.Stat(sessionPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("session path was damaged: %v, %v", info, err)
	}
	if got, err := os.ReadFile(transfer.Destination); err != nil || string(got) != "hello" {
		t.Fatalf("received = %q, %v", got, err)
	}
	refs := c.roomAttachmentRefs([]string{offer.ID})
	if !strings.HasPrefix(refs[offer.ID], "@.workground2/attachments/room/") || strings.Contains(refs[offer.ID], " ") {
		t.Fatalf("agent reference = %q", refs[offer.ID])
	}
}

func TestAutoReceiveThresholdAndFilters(t *testing.T) {
	workspace := t.TempDir()
	eligible, peer := testDeclaredFileOffer("eligible", "eligible.bin", "other", collaborationAutoReceiveLimit-1)
	atLimit, _ := testDeclaredFileOffer("limit", "limit.bin", "other", collaborationAutoReceiveLimit)
	own, _ := testDeclaredFileOffer("own", "own.bin", "self", 1)
	revoked, _ := testDeclaredFileOffer("revoked", "revoked.bin", "other", 1)
	revoked.RevokedAt = timePtr(time.Now())
	c := testAutoReceiveRuntime(workspace, "", eligible, peer)
	c.state.Snapshot.Timeline = append(c.state.Snapshot.Timeline,
		collab.TimelineItem{ID: atLimit.ID, Type: collab.TimelineFile, File: &atLimit},
		collab.TimelineItem{ID: own.ID, Type: collab.TimelineFile, File: &own},
		collab.TimelineItem{ID: revoked.ID, Type: collab.TimelineFile, File: &revoked},
	)
	c.maybeAutoReceiveFiles()
	waitForTransferStatus(t, c, eligible.ID, "completed")
	c.mu.RLock()
	if c.transfers[eligible.ID] == nil {
		c.mu.RUnlock()
		t.Fatal("file below 1 MiB was not reserved")
	}
	for _, id := range []string{atLimit.ID, own.ID, revoked.ID} {
		if c.transfers[id] != nil {
			c.mu.RUnlock()
			t.Fatalf("ineligible file %q was auto-received", id)
		}
	}
	c.mu.RUnlock()
}

func TestAutoReceiveConcurrentReconcileIsIdempotent(t *testing.T) {
	offer, peer := testFileOffer("same", "same.bin", "other", []byte("idempotent"), 4)
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	var wait sync.WaitGroup
	for range 20 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			c.maybeAutoReceiveFiles()
		}()
	}
	wait.Wait()
	waitForTransferStatus(t, c, offer.ID, "completed")
	for range 5 {
		c.maybeAutoReceiveFiles()
	}
	peer.mu.Lock()
	manifestCalls := peer.manifestCalls
	peer.mu.Unlock()
	if manifestCalls != 1 {
		t.Fatalf("manifest fetched %d times, want 1", manifestCalls)
	}
	c.mu.RLock()
	if len(c.transfers) != 1 {
		t.Fatalf("transfer count = %d", len(c.transfers))
	}
	c.mu.RUnlock()
}

func TestAutoReceiveRetriesWaitingSender(t *testing.T) {
	offer, peer := testFileOffer("retry", "retry.bin", "other", []byte("retry-ok"), 4)
	peer.manifestFailures = 1
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.autoRetryDelay = func(int) time.Duration { return time.Millisecond }
	c.maybeAutoReceiveFiles()
	waitForTransferStatus(t, c, offer.ID, "completed")
	peer.mu.Lock()
	calls := peer.manifestCalls
	peer.mu.Unlock()
	if calls != 2 {
		t.Fatalf("manifest calls = %d, want 2", calls)
	}
}

func TestAutoReceiveRespectsUserPause(t *testing.T) {
	offer, peer := testFileOffer("paused", "paused.bin", "other", []byte("paused"), 4)
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	safe, _ := sanitizeRoomAttachmentName(offer.Name)
	dest, rel, err := roomAttachmentDestination(c.ownerWorkspaceRoot, c.roomInstance, offer.ID, safe, offer.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	paused := transferForTestOffer(offer, c.state.Room, dest, rel)
	paused.Status, paused.Transferred, paused.PausedByUser, paused.Retryable = "paused", 0, true, true
	for index := range paused.Completed {
		paused.Completed[index] = false
	}
	c.transfers[offer.ID] = paused
	c.maybeAutoReceiveFiles()
	time.Sleep(20 * time.Millisecond)
	peer.mu.Lock()
	calls := peer.manifestCalls
	peer.mu.Unlock()
	if calls != 0 || c.transfers[offer.ID].Status != "paused" {
		t.Fatalf("paused transfer resumed: calls=%d transfer=%+v", calls, c.transfers[offer.ID])
	}
}

func TestDownloadRejectsChunkLongerThanOffer(t *testing.T) {
	data := []byte("evil")
	offer, peer := testFileOffer("malicious", "malicious.bin", "other", data, 4)
	offer.Size = 1
	peer.manifest.Size = 1
	manifestData, _ := json.Marshal(peer.manifest)
	manifestHash := sha256.Sum256(manifestData)
	offer.ManifestHash = hex.EncodeToString(manifestHash[:])
	peer.ticket.File = offer
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.maybeAutoReceiveFiles()
	transfer := waitForTransferStatus(t, c, offer.ID, "failed")
	if !transfer.Retryable || !transfer.AutoBlocked || !strings.Contains(transfer.Error, "长度") {
		t.Fatalf("oversized chunk failure = %+v", transfer)
	}
	time.Sleep(20 * time.Millisecond)
	peer.mu.Lock()
	before := peer.manifestCalls
	peer.mu.Unlock()
	c.maybeAutoReceiveFiles()
	time.Sleep(20 * time.Millisecond)
	peer.mu.Lock()
	after := peer.manifestCalls
	peer.mu.Unlock()
	if after != before {
		t.Fatalf("blocked malicious offer retried automatically: before=%d after=%d", before, after)
	}
	if _, err := os.Stat(transfer.Destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized chunk was published: %v", err)
	}
}

func TestDownloadRejectsSymlinkPart(t *testing.T) {
	offer, peer := testFileOffer("symlink", "symlink.bin", "other", []byte("safe"), 4)
	dir := t.TempDir()
	target := filepath.Join(dir, "target.bin")
	part := filepath.Join(dir, "received.bin.wg2part")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, part); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	transfer := transferForTestOffer(offer, "room", filepath.Join(dir, "received.bin"), "")
	transfer.Status, transfer.Transferred, transfer.PartPath, transfer.Automatic = "negotiating", 0, part, false
	for index := range transfer.Completed {
		transfer.Completed[index] = false
	}
	c.transfers[offer.ID] = transfer
	c.startFileDownload(offer.ID)
	waitForTransferStatus(t, c, offer.ID, "completed")
	if got, _ := os.ReadFile(target); string(got) != "untouched" {
		t.Fatalf("symlink target was modified: %q", got)
	}
}

func TestPreviewCollaborationFileValidatesContentAndSupportsOwnShare(t *testing.T) {
	root := t.TempDir()
	pngData := testPNG(t, 2, 2)
	path := filepath.Join(root, "image.bin")
	if err := os.WriteFile(path, pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	offer, peer := testFileOffer("image", "image.bin", "self", pngData, 4)
	c := &desktopCollaboration{
		roomInstance:   "room-instance",
		shareAuthority: "share-authority",
		state:          CollaborationState{Room: "room", MemberID: "self", Snapshot: collab.Snapshot{Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}},
		shares: map[string]collaborationSharedFile{offer.ID: {
			FileID: offer.ID, Room: "room", ShareAuthority: "share-authority", OwnerID: "self", Path: path, Name: offer.Name, Size: int64(len(pngData)), SHA256: offer.SHA256,
			ManifestHash: offer.ManifestHash, ChunkSize: offer.ChunkSize, ChunkHashes: append([]string(nil), peer.manifest.ChunkHashes...),
			OfferRevision: offer.Revision, ModTimeUnix: info.ModTime().UnixNano(), Status: "available",
		}},
		transfers: map[string]*CollaborationFileTransfer{},
	}
	preview, err := c.previewFile(offer.ID)
	if err != nil || preview.MIME != "image/png" || !strings.HasPrefix(preview.DataURL, "data:image/png;base64,") {
		t.Fatalf("own preview = %+v, %v", preview, err)
	}
	offer.OwnerID = "other"
	c.state.Snapshot.Timeline[0].File = &offer
	delete(c.shares, offer.ID)
	safe, _ := sanitizeRoomAttachmentName(offer.Name)
	receivedPath, rel, err := roomAttachmentDestination(root, c.roomInstance, offer.ID, safe, offer.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(receivedPath, pngData, 0o600); err != nil {
		t.Fatal(err)
	}
	c.ownerWorkspaceRoot = root
	c.transfers[offer.ID] = transferForTestOffer(offer, "room", receivedPath, rel)
	preview, err = c.previewFile(offer.ID)
	if err != nil || preview.MIME != "image/png" {
		t.Fatalf("received preview = %+v, %v", preview, err)
	}
}

func TestPreviewCollaborationFileRejectsCorruptAndOversizedDimensions(t *testing.T) {
	root := t.TempDir()
	corrupt := filepath.Join(root, "corrupt.png")
	corruptData := append([]byte("\x89PNG\r\n\x1a\n"), []byte("broken")...)
	if err := os.WriteFile(corrupt, corruptData, 0o600); err != nil {
		t.Fatal(err)
	}
	corruptSum := sha256.Sum256(corruptData)
	if _, _, err := readCollaborationImage(corrupt, int64(len(corruptData)), hex.EncodeToString(corruptSum[:])); err == nil || errors.Is(err, errCollaborationFileNotImage) {
		t.Fatalf("corrupt PNG error = %v", err)
	}
	bomb := testPNG(t, 1, 1)
	binary.BigEndian.PutUint32(bomb[16:20], 10000)
	binary.BigEndian.PutUint32(bomb[20:24], 10000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	bombPath := filepath.Join(root, "bomb.png")
	if err := os.WriteFile(bombPath, bomb, 0o600); err != nil {
		t.Fatal(err)
	}
	bombSum := sha256.Sum256(bomb)
	if _, _, err := readCollaborationImage(bombPath, int64(len(bomb)), hex.EncodeToString(bombSum[:])); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("pixel bomb error = %v", err)
	}
}

func TestRoomAttachmentRefsRejectsStaleRoom(t *testing.T) {
	data := []byte("agent-readable")
	offer, _ := testFileOffer("file", "README!", "other", data, 4)
	root := t.TempDir()
	safe, _ := sanitizeRoomAttachmentName(offer.Name)
	dest, rel, err := roomAttachmentDestination(root, "room-instance", offer.ID, safe, offer.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &desktopCollaboration{
		ownerWorkspaceRoot: root,
		roomInstance:       "room-instance",
		state:              CollaborationState{Room: "room", Snapshot: collab.Snapshot{Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}},
		transfers:          map[string]*CollaborationFileTransfer{offer.ID: transferForTestOffer(offer, "room", dest, rel)},
	}
	if ref := c.roomAttachmentRefs([]string{offer.ID})[offer.ID]; ref == "" || strings.Contains(ref, " ") {
		t.Fatalf("room attachment ref = %q", ref)
	}
	contextText, err := collaborationContext(c.state.Snapshot, []string{offer.ID}, c.roomAttachmentRefs([]string{offer.ID}))
	if err != nil || !strings.Contains(contextText, c.roomAttachmentRefs([]string{offer.ID})[offer.ID]) {
		t.Fatalf("Agent context does not contain the received path: %q, %v", contextText, err)
	}
	c.transfers[offer.ID].Room = "old-room"
	if ref := c.roomAttachmentRefs([]string{offer.ID})[offer.ID]; ref != "" {
		t.Fatalf("stale Room attachment ref = %q", ref)
	}
}

func TestPreviewCollaborationFileRejectsAnimatedGIF(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.Pix[0] = 1
	var encoded bytes.Buffer
	if err := gif.EncodeAll(&encoded, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{1, 1}}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "animated.gif")
	raw := encoded.Bytes()
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if _, _, err := readCollaborationImage(path, int64(len(raw)), hex.EncodeToString(sum[:])); err == nil || !strings.Contains(err.Error(), "animated") {
		t.Fatalf("animated GIF error = %v", err)
	}
}

func TestAutoReceiveRejectsInvalidSnapshotBeforeAllocation(t *testing.T) {
	offer, peer := testFileOffer("invalid", "invalid.bin", "other", []byte("x"), 4)
	offer.ChunkCount = -1
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.maybeAutoReceiveFiles()
	c.mu.RLock()
	transfer := c.transfers[offer.ID]
	notice := c.state.LastError
	c.mu.RUnlock()
	if transfer != nil || !strings.Contains(notice, "不安全") {
		t.Fatalf("invalid offer was not rejected: transfer=%+v notice=%q", transfer, notice)
	}
}

func TestAutoReceiveQuotaDoesNotResetWhenOffersDisappear(t *testing.T) {
	offer, peer := testFileOffer("new", "new.bin", "other", []byte("x"), 4)
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.transferArchive = map[string]*CollaborationFileTransfer{}
	for index := 0; index < collaborationAutoReceiveMaxFiles; index++ {
		fileID := fmt.Sprintf("old-%d", index)
		transfer := &CollaborationFileTransfer{FileID: fileID, Room: "old-room", RoomInstance: fmt.Sprintf("old-instance-%d", index), SHA256: strings.Repeat("a", 64), Total: 1, Automatic: true, Status: "revoked"}
		c.transferArchive[collaborationTransferArchiveKey(transfer.RoomInstance, fileID)] = transfer
	}
	c.maybeAutoReceiveFiles()
	c.mu.RLock()
	transfer := c.transfers[offer.ID]
	notice := c.state.LastError
	c.mu.RUnlock()
	if transfer != nil || !strings.Contains(notice, "安全配额") {
		t.Fatalf("persistent quota was bypassed: transfer=%+v notice=%q", transfer, notice)
	}
}

func TestAutoReceiveGlobalSessionReportsMissingWorkspace(t *testing.T) {
	offer, peer := testFileOffer("global", "global.bin", "other", []byte("x"), 4)
	c := testAutoReceiveRuntime("", "", offer, peer)
	c.maybeAutoReceiveFiles()
	c.mu.RLock()
	notice := c.state.LastError
	c.mu.RUnlock()
	if !strings.Contains(notice, "没有 workspace") {
		t.Fatalf("missing workspace notice = %q", notice)
	}
}

func TestAutoReceiveCompletedFileMutationBecomesVisible(t *testing.T) {
	offer, peer := testFileOffer("mutated", "mutated.bin", "other", []byte("good"), 4)
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.maybeAutoReceiveFiles()
	transfer := waitForTransferStatus(t, c, offer.ID, "completed")
	if err := os.WriteFile(transfer.Destination, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(transfer.Destination, future, future); err != nil {
		t.Fatal(err)
	}
	c.maybeAutoReceiveFiles()
	failed := waitForTransferStatus(t, c, offer.ID, "failed")
	if !failed.AutoBlocked || !strings.Contains(failed.Error, "内容不同") {
		t.Fatalf("mutated completed file state = %+v", failed)
	}
}

func TestFileTransfersAreArchivedByRoomInstance(t *testing.T) {
	old := &CollaborationFileTransfer{FileID: "same", RoomInstance: "old", Status: "paused"}
	current := &desktopCollaboration{transfers: map[string]*CollaborationFileTransfer{"same": old}, transferCancel: map[string]context.CancelFunc{}}
	current.switchFileTransfersLocked("new")
	if current.transfers["same"] != nil {
		t.Fatal("old Room transfer remained active")
	}
	current.transfers["same"] = &CollaborationFileTransfer{FileID: "same", RoomInstance: "new", Status: "completed"}
	current.switchFileTransfersLocked("old")
	if current.transfers["same"] != old || current.transfers["same"].Status != "paused" {
		t.Fatalf("old Room transfer was not restored: %+v", current.transfers["same"])
	}
	if archived := current.transferArchive[collaborationTransferArchiveKey("new", "same")]; archived == nil || archived.Status != "completed" {
		t.Fatalf("new Room transfer was not archived: %+v", archived)
	}
}

func TestFileTransferRouteSwitchLeavesManualDownloadRecoverable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transfer := &CollaborationFileTransfer{FileID: "manual", RoomInstance: "same-room", Status: "downloading", Direction: "receive"}
	c := &desktopCollaboration{
		transfers:      map[string]*CollaborationFileTransfer{"manual": transfer},
		transferCancel: map[string]context.CancelFunc{"manual": cancel},
		transferRun:    map[string]uint64{"manual": 1},
	}
	c.switchFileTransfersLocked("same-room")
	select {
	case <-ctx.Done():
	default:
		t.Fatal("old route worker was not cancelled")
	}
	restored := c.transfers["manual"]
	if restored == nil || restored.Status != "waiting_sender" || !restored.Retryable {
		t.Fatalf("manual route-switch state = %+v", restored)
	}
}

func TestUnauthenticatedLANRoomIdentityIsConnectionScoped(t *testing.T) {
	first := &collaborationConnection{mode: "client", room: "same-room", hostKey: "claimed-host-key", protocolVersion: collaborationProtocolV2}
	second := &collaborationConnection{mode: "client", room: "same-room", hostKey: "claimed-host-key", protocolVersion: collaborationProtocolV2}
	firstRoom, firstShare := establishCollaborationRoomInstance(first)
	secondRoom, secondShare := establishCollaborationRoomInstance(second)
	if firstRoom == secondRoom || firstShare == secondShare {
		t.Fatalf("unauthenticated LAN identity was reused: room=%q share=%q", firstRoom, firstShare)
	}
	trustedA := &collaborationConnection{mode: "client", room: "same-room", hostKey: "verified-host-key", relayBindings: []collaborationRelayBinding{(*relayCollaborationPeer)(nil)}}
	trustedB := &collaborationConnection{mode: "client", room: "same-room", hostKey: "verified-host-key", relayBindings: []collaborationRelayBinding{(*relayCollaborationPeer)(nil)}}
	trustedRoomA, trustedShareA := establishCollaborationRoomInstance(trustedA)
	trustedRoomB, trustedShareB := establishCollaborationRoomInstance(trustedB)
	if trustedRoomA != trustedRoomB || trustedShareA != trustedShareB {
		t.Fatalf("authenticated Relay identity was not stable: %q/%q vs %q/%q", trustedRoomA, trustedShareA, trustedRoomB, trustedShareB)
	}
}

func TestAutoReceiveRetryCycleYieldsAndRecovers(t *testing.T) {
	offer, peer := testFileOffer("retry-cycle", "retry.bin", "other", []byte("recovered"), 4)
	peer.manifestFailures = collaborationAutoReceiveAttempts
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, peer)
	c.autoRetryDelay = func(int) time.Duration { return time.Millisecond }
	c.maybeAutoReceiveFiles()
	waitForTransferStatus(t, c, offer.ID, "completed")
	peer.mu.Lock()
	calls := peer.manifestCalls
	peer.mu.Unlock()
	if calls != collaborationAutoReceiveAttempts+1 {
		t.Fatalf("manifest calls = %d, want %d", calls, collaborationAutoReceiveAttempts+1)
	}
}

func TestAutomaticFileFetchUsesAuthenticatedRoomProxyOnly(t *testing.T) {
	offer, scripted := testFileOffer("proxy-only", "proxy.bin", "other", []byte("proxy-data"), 4)
	var directHits atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directHits.Add(1)
		_, _ = w.Write([]byte("unexpected direct response"))
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/collab/v2/rooms/room/files/proxy-only/ticket":
			ticket := scripted.ticket
			ticket.DirectURLs = []string{direct.URL}
			_ = json.NewEncoder(w).Encode(ticket)
		case "/collab/v2/rooms/room/files/proxy-only/manifest":
			_ = json.NewEncoder(w).Encode(scripted.manifest)
		case "/collab/v2/rooms/room/files/proxy-only/chunks/0":
			_, _ = w.Write(scripted.chunks[0])
		default:
			http.NotFound(w, r)
		}
	}))
	defer proxy.Close()
	peer := &httpCollaborationPeer{baseURL: proxy.URL, client: &http.Client{}, streamClient: &http.Client{}, room: "room", member: "receiver", session: "session", protocolVersion: collaborationProtocolV2}
	ticket, manifest, err := peer.fetchFileManifest(context.Background(), offer.ID, 4096, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket.DirectURLs) != 0 || manifest.FileID != offer.ID {
		t.Fatalf("automatic ticket retained direct URLs: %+v", ticket)
	}
	chunk, err := peer.fetchFileChunk(context.Background(), ticket, 0)
	if err != nil || !bytes.Equal(chunk, scripted.chunks[0]) {
		t.Fatalf("proxy chunk = %q, %v", chunk, err)
	}
	if directHits.Load() != 0 {
		t.Fatalf("automatic fetch contacted direct origin %d times", directHits.Load())
	}
}

func TestAutomaticFileDownloadPanicIsContainedAndRetryable(t *testing.T) {
	offer, healthy := testFileOffer("panic-recovery", "payload.json", "other", []byte(`{"ok":true}`), collab.MinFileChunkSize)
	panicking := &panicFilePeer{}
	c := testAutoReceiveRuntime(t.TempDir(), "", offer, panicking)
	defer c.closeFileTransfers()

	c.maybeAutoReceiveFiles()
	failed := waitForTransferStatus(t, c, offer.ID, "failed")
	if !failed.Retryable || !failed.AutoBlocked || !strings.Contains(failed.Error, "内部异常") {
		t.Fatalf("contained panic state = %+v", failed)
	}
	time.Sleep(20 * time.Millisecond)
	if calls := panicking.manifestCalls.Load(); calls != 1 {
		t.Fatalf("contained panic automatically retried %d times, want 1", calls)
	}

	c.mu.Lock()
	c.conn.filePeer = healthy
	c.mu.Unlock()
	if _, err := c.resumeFile(offer.ID); err != nil {
		t.Fatal(err)
	}
	want := waitForTransferStatus(t, c, offer.ID, "completed")
	if data, err := os.ReadFile(want.Destination); err != nil || string(data) != `{"ok":true}` {
		t.Fatalf("retried file = %q, %v", data, err)
	}
}

func TestInstallConnectionReconcilesInitialSnapshotAndReopensAfterClose(t *testing.T) {
	_, c, _ := newTestDesktopCollaboration(t)
	c.ownerWorkspaceRoot = t.TempDir()
	offer, filePeer := testFileOffer("initial", "initial.bin", "other", []byte("initial-data"), 4)
	snapshot := collab.Snapshot{Room: collab.Room{ID: "room"}, Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}
	newConnection := func() *collaborationConnection {
		peer := &fakeCollaborationPeer{snapshot: snapshot}
		return &collaborationConnection{peer: peer, filePeer: filePeer, mode: "host", room: "room", memberID: "self", sessionID: "session-a", hostKey: "owned-host-key", initialSnapshot: snapshot}
	}
	first := newConnection()
	if _, err := c.installConnection(first); err != nil {
		t.Fatal(err)
	}
	completed := waitForTransferStatus(t, c, offer.ID, "completed")
	c.fenceCurrentConnection()
	c.closeFileTransfers()
	if err := os.Remove(completed.Destination); err != nil {
		t.Fatal(err)
	}
	second := newConnection()
	if _, err := c.installConnection(second); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = second.close(context.Background(), false)
		c.closeFileTransfers()
	}()
	deadline := time.Now().Add(5 * time.Second)
	calls := 0
	for time.Now().Before(deadline) {
		filePeer.mu.Lock()
		calls = filePeer.manifestCalls
		filePeer.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitForTransferStatus(t, c, offer.ID, "completed")
	if calls != 2 {
		t.Fatalf("manifest calls across reinstall = %d, want 2", calls)
	}
}

type scriptedFilePeer struct {
	mu               sync.Mutex
	ticket           collab.FileTransferTicket
	manifest         collaborationFileManifest
	chunks           map[int][]byte
	manifestFailures int
	manifestCalls    int
}

type panicFilePeer struct {
	manifestCalls atomic.Int32
}

func (p *panicFilePeer) RegisterFileOrigin(context.Context, string, collab.RegisterFileOriginInput) error {
	return nil
}

func (p *panicFilePeer) fileTicket(context.Context, string) (collab.FileTransferTicket, error) {
	return collab.FileTransferTicket{}, nil
}

func (p *panicFilePeer) fetchFileManifest(context.Context, string, int64, bool) (collab.FileTransferTicket, collaborationFileManifest, error) {
	p.manifestCalls.Add(1)
	panic("file peer exploded")
}

func (p *panicFilePeer) fetchFileChunk(context.Context, collab.FileTransferTicket, int) ([]byte, error) {
	return nil, nil
}

func (p *scriptedFilePeer) RegisterFileOrigin(context.Context, string, collab.RegisterFileOriginInput) error {
	return nil
}

func (p *scriptedFilePeer) fileTicket(context.Context, string) (collab.FileTransferTicket, error) {
	return p.ticket, nil
}

func (p *scriptedFilePeer) fetchFileManifest(context.Context, string, int64, bool) (collab.FileTransferTicket, collaborationFileManifest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.manifestCalls++
	if p.manifestFailures > 0 {
		p.manifestFailures--
		return collab.FileTransferTicket{}, collaborationFileManifest{}, fmt.Errorf("sender is not ready")
	}
	return p.ticket, p.manifest, nil
}

func (p *scriptedFilePeer) fetchFileChunk(_ context.Context, _ collab.FileTransferTicket, index int) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	data, ok := p.chunks[index]
	if !ok {
		return nil, fmt.Errorf("chunk %d is unavailable", index)
	}
	return append([]byte(nil), data...), nil
}

func testFileOffer(id, name, owner string, data []byte, chunkSize int64) (collab.FileOffer, *scriptedFilePeer) {
	if chunkSize < collab.MinFileChunkSize {
		chunkSize = collab.MinFileChunkSize
	}
	chunks := map[int][]byte{}
	hashes := make([]string, 0)
	for offset := int64(0); offset < int64(len(data)); offset += chunkSize {
		end := min(offset+chunkSize, int64(len(data)))
		chunk := append([]byte(nil), data[offset:end]...)
		index := len(hashes)
		chunks[index] = chunk
		sum := sha256.Sum256(chunk)
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	manifest := collaborationFileManifest{FileID: id, Size: int64(len(data)), ChunkSize: chunkSize, ChunkHashes: hashes}
	manifestData, _ := json.Marshal(manifest)
	manifestSum := sha256.Sum256(manifestData)
	whole := sha256.Sum256(data)
	offer := collab.FileOffer{ID: id, OwnerID: owner, Name: name, Size: int64(len(data)), SHA256: hex.EncodeToString(whole[:]), ManifestHash: hex.EncodeToString(manifestSum[:]), ChunkSize: chunkSize, ChunkCount: len(hashes), Revision: 1}
	peer := &scriptedFilePeer{ticket: collab.FileTransferTicket{File: offer, ExpiresAt: time.Now().Add(time.Hour)}, manifest: manifest, chunks: chunks}
	return offer, peer
}

func testDeclaredFileOffer(id, name, owner string, size int64) (collab.FileOffer, *scriptedFilePeer) {
	return testFileOffer(id, name, owner, make([]byte, int(size)), collab.MinFileChunkSize)
}

func testAutoReceiveRuntime(workspace, sessionPath string, offer collab.FileOffer, peer collaborationFilePeer) *desktopCollaboration {
	return &desktopCollaboration{
		app: &App{}, ownerSessionPath: sessionPath, ownerWorkspaceRoot: workspace, roomInstance: "room-instance",
		state:  CollaborationState{Status: "connected", Room: "room", MemberID: "self", Snapshot: collab.Snapshot{Timeline: []collab.TimelineItem{{ID: offer.ID, Type: collab.TimelineFile, File: &offer}}}},
		conn:   &collaborationConnection{room: "room", memberID: "self", filePeer: peer, roomInstanceKey: "room-instance"},
		shares: map[string]collaborationSharedFile{}, transfers: map[string]*CollaborationFileTransfer{}, transferCancel: map[string]context.CancelFunc{}, transferRun: map[string]uint64{}, transferLocks: map[string]*sync.Mutex{}, autoReceiveSem: make(chan struct{}, 2), outboxFailures: map[string]string{}, starts: map[string]collaborationStartRecord{}, runs: map[string]*collaborationAgentRun{},
	}
}

func transferForTestOffer(offer collab.FileOffer, room, destination, workspacePath string) *CollaborationFileTransfer {
	completed := make([]bool, offer.ChunkCount)
	for index := range completed {
		completed[index] = true
	}
	return &CollaborationFileTransfer{
		FileID: offer.ID, Room: room, RoomInstance: "room-instance", OwnerID: offer.OwnerID,
		SHA256: offer.SHA256, ManifestHash: offer.ManifestHash, ChunkSize: offer.ChunkSize,
		ChunkCount: offer.ChunkCount, OfferRevision: offer.Revision, Direction: "receive",
		Name: offer.Name, Status: "completed", Transferred: offer.Size, Total: offer.Size,
		Destination: destination, WorkspacePath: workspacePath, Completed: completed, Automatic: workspacePath != "",
	}
}

func waitForTransferStatus(t *testing.T, c *desktopCollaboration, fileID, want string) CollaborationFileTransfer {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		transfer := cloneCollaborationTransferPtr(c.transfers[fileID])
		c.mu.RUnlock()
		if transfer != nil && transfer.Status == want {
			return *transfer
		}
		time.Sleep(5 * time.Millisecond)
	}
	c.mu.RLock()
	transfer := cloneCollaborationTransferPtr(c.transfers[fileID])
	c.mu.RUnlock()
	t.Fatalf("transfer %q did not reach %q: %+v", fileID, want, transfer)
	return CollaborationFileTransfer{}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	var out bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func timePtr(t time.Time) *time.Time { return &t }
