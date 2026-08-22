package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/collab"
	"workground2/internal/config"
	"workground2/internal/relayproto"
	"workground2/internal/relayserver"
)

func TestRelayE2EEncryptsAndAuthenticatesHeader(t *testing.T) {
	_, identity, err := newRelayHostKey()
	if err != nil {
		t.Fatal(err)
	}
	guestPrivate, guestPublic, nonce, err := newRelayEphemeral()
	if err != nil {
		t.Fatal(err)
	}
	hello := relayE2EHello{PublicKey: guestPublic, Nonce: nonce}
	accept, hostCipher, err := relayHostAccept("tun-1", "peer-1", hello, identity)
	if err != nil {
		t.Fatal(err)
	}
	guestCipher, err := relayGuestAccept(guestPrivate, "tun-1", "peer-1", hello, accept, accept.HostPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	header := relayproto.Header{Version: 1, Type: "rpc.request", TunnelID: "tun-1", PeerID: "peer-1", Epoch: 3, Sequence: 9, Flags: []string{"encrypted"}}
	ciphertext, err := guestCipher.seal(header, []byte("private Room payload"))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := hostCipher.open(header, ciphertext)
	if err != nil || string(plaintext) != "private Room payload" {
		t.Fatalf("open = %q, %v", plaintext, err)
	}
	tampered := header
	tampered.PeerID = "peer-2"
	if _, err := hostCipher.open(tampered, ciphertext); err == nil {
		t.Fatal("tampered routing header was accepted")
	}
	wrong := base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	if _, err := relayGuestAccept(guestPrivate, "tun-1", "peer-1", hello, accept, wrong); err == nil {
		t.Fatal("wrong Host key was accepted")
	}
	_, attackerIdentity, err := newRelayHostKey()
	if err != nil {
		t.Fatal(err)
	}
	attackerAccept, _, err := relayHostAccept("tun-1", "peer-1", hello, attackerIdentity)
	if err != nil {
		t.Fatal(err)
	}
	attackerAccept.HostKeyFingerprint = accept.HostPublicKey
	if _, err := relayGuestAccept(guestPrivate, "tun-1", "peer-1", hello, attackerAccept, accept.HostPublicKey); err == nil {
		t.Fatal("self-reported Host fingerprint bypassed the invited Host key")
	}
}

func TestRelayDiscoveryURLSchemeAndPlaintextMismatch(t *testing.T) {
	plain, err := relayHTTPURL("ws://127.0.0.1:8443/relay/v1/connect")
	if err != nil || plain.Scheme != "http" {
		t.Fatalf("ws discovery URL = %v, %v", plain, err)
	}
	secure, err := relayHTTPURL("wss://relay.example.test/relay/v1/connect")
	if err != nil || secure.Scheme != "https" {
		t.Fatalf("wss discovery URL = %v, %v", secure, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	endpoint := "https" + strings.TrimPrefix(server.URL, "http") + "/relay/v1/rooms"
	var result relayproto.RoomList
	err = relayHTTPJSON(context.Background(), config.RelayConfig{ID: "local", URL: "wss://127.0.0.1:8443/relay/v1/connect"}, http.MethodGet, endpoint, &result)
	if err == nil || !strings.Contains(err.Error(), "configured as wss://") || !strings.Contains(err.Error(), "use ws://") {
		t.Fatalf("plaintext mismatch error = %v", err)
	}
}

func TestCollaborationInviteV2RoundTrip(t *testing.T) {
	value := collaborationInviteV2{Room: "联调 房间", HostKey: "host-key", RoomToken: "token", Routes: []CollaborationRouteInput{{Kind: "relay", RelayID: "sg", URL: "wss://relay.example/relay/v1/connect", TunnelID: "tun", GuestCapability: "cap"}}}
	encoded, err := buildCollaborationInviteV2(value)
	if err != nil {
		t.Fatal(err)
	}
	input := JoinCollaborationRoomInput{Invite: encoded}
	if err := applyCollaborationInvite(&input); err != nil {
		t.Fatal(err)
	}
	if input.Room != value.Room || input.HostKey != value.HostKey || input.Token != value.RoomToken || len(input.Routes) != 1 || input.Routes[0].GuestCapability != "cap" {
		t.Fatalf("parsed invite = %#v", input)
	}
}

func TestRelayDialURLRequiresExplicitPublicWSConsent(t *testing.T) {
	if err := validateRelayDialURL("ws://127.0.0.1:8443/relay/v1/connect", false); err != nil {
		t.Fatal(err)
	}
	if err := validateRelayDialURL("ws://relay.example/relay/v1/connect", false); err == nil || !strings.Contains(err.Error(), "allow_insecure") {
		t.Fatalf("error = %v", err)
	}
	if err := validateRelayDialURL("ws://relay.example/relay/v1/connect", true); err != nil {
		t.Fatal(err)
	}
}

func TestRelayConfigForRouteMatchesLocalTrustByURL(t *testing.T) {
	t.Setenv("WORKGROUND2_HOME", t.TempDir())
	trustedURL := "ws://Relay.Example:8443/relay/v1/connect"
	cfg := config.Default()
	if err := cfg.SetCollaboration(config.CollaborationConfig{
		PreferLAN: true, ConnectTimeout: 10, RouteStable: 60,
		Relays: []config.RelayConfig{{
			ID: "my-local-name", URL: trustedURL, Enabled: true, Priority: 100, AllowInsecure: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	relay, err := relayConfigForRoute(CollaborationRouteInput{
		Kind: "relay", RelayID: "someone-elses-name", URL: "ws://relay.example:8443/relay/v1/connect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if relay.ID != "my-local-name" || !relay.AllowInsecure || relay.URL != trustedURL {
		t.Fatalf("relay = %#v, want local trusted entry selected by URL", relay)
	}

	untrusted, err := relayConfigForRoute(CollaborationRouteInput{
		Kind: "relay", RelayID: "my-local-name", URL: "ws://other.example:8443/relay/v1/connect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if untrusted.AllowInsecure || untrusted.URL != "ws://other.example:8443/relay/v1/connect" {
		t.Fatalf("relay = %#v, same local ID must not trust a different URL", untrusted)
	}
}

func TestRelayConfigForRouteKeepsLegacyIDFallback(t *testing.T) {
	t.Setenv("WORKGROUND2_HOME", t.TempDir())
	cfg := config.Default()
	if err := cfg.SetCollaboration(config.CollaborationConfig{
		PreferLAN: true, ConnectTimeout: 10, RouteStable: 60,
		Relays: []config.RelayConfig{{
			ID: "legacy-relay", URL: "wss://relay.example/relay/v1/connect", Enabled: true, Priority: 100,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	relay, err := relayConfigForRoute(CollaborationRouteInput{Kind: "relay", RelayID: "legacy-relay"})
	if err != nil {
		t.Fatal(err)
	}
	if relay.ID != "legacy-relay" || relay.URL != "wss://relay.example/relay/v1/connect" {
		t.Fatalf("relay = %#v, want legacy ID lookup", relay)
	}
}

func newTestRelayFileSource(t *testing.T, data []byte) (*desktopCollaboration, collaborationSharedFile) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay-source.bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	c := &desktopCollaboration{state: CollaborationState{Room: "room", MemberID: "owner"}, shareAuthority: "authority", shares: map[string]collaborationSharedFile{}}
	share, err := c.prepareSharedFile(path)
	if err != nil {
		t.Fatal(err)
	}
	share.Room, share.ShareAuthority, share.OwnerID, share.Status = "room", "authority", "owner", "available"
	c.shares[share.FileID] = share
	return c, share
}

func TestRelayFileSourceRequiresCurrentAuthorityAndOwner(t *testing.T) {
	c, share := newTestRelayFileSource(t, []byte("relay source"))
	peer := &relayCollaborationPeer{fileSource: c, room: "room"}
	if err := peer.RegisterFileOrigin(context.Background(), share.FileID, collab.RegisterFileOriginInput{}); err != nil {
		t.Fatalf("current source registration failed: %v", err)
	}
	c.mu.Lock()
	changed := c.shares[share.FileID]
	changed.Status = "unavailable"
	c.shares[share.FileID] = changed
	c.mu.Unlock()
	if err := peer.RegisterFileOrigin(context.Background(), share.FileID, collab.RegisterFileOriginInput{}); err != nil {
		t.Fatalf("recoverable source registration failed: %v", err)
	}
	c.mu.Lock()
	c.shareAuthority = "other-authority"
	c.mu.Unlock()
	if err := peer.RegisterFileOrigin(context.Background(), share.FileID, collab.RegisterFileOriginInput{}); err == nil {
		t.Fatal("source from another Room authority was registered")
	}
	if _, err := c.serveRelayFileSource("file.manifest", relayFileRequest{Room: "room", FileID: share.FileID}); err == nil {
		t.Fatal("source from another Room authority was served")
	}
}

func TestRelayFileManifestPreservesOfferIdentity(t *testing.T) {
	c, share := newTestRelayFileSource(t, []byte("relay source"))
	share.OfferRevision = 7
	c.shares[share.FileID] = share
	value, err := c.serveRelayFileSource("file.manifest", relayFileRequest{Room: "room", FileID: share.FileID})
	if err != nil {
		t.Fatal(err)
	}
	response, ok := value.(relayFileManifestResponse)
	if !ok {
		t.Fatalf("manifest response type = %T", value)
	}
	offer := collab.FileOffer{
		ID: share.FileID, OwnerID: share.OwnerID, Size: share.Size, SHA256: share.SHA256,
		ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes), Revision: share.OfferRevision,
	}
	if !fileTicketMatchesOffer(collab.FileTransferTicket{File: response.File}, offer) {
		t.Fatalf("Relay ticket file = %+v, want offer identity %+v", response.File, offer)
	}
}

func TestRelayFileSourceRejectsOverflowAndSourceChanges(t *testing.T) {
	data := bytes.Repeat([]byte("segment-data"), 10_000)
	for _, mutation := range []string{"replace", "in_place"} {
		t.Run(mutation, func(t *testing.T) {
			c, share := newTestRelayFileSource(t, data)
			request := relayFileRequest{Room: "room", FileID: share.FileID, Index: 0, Size: 8}
			if _, err := c.serveRelayFileSource("file.segment", relayFileRequest{Room: "room", FileID: share.FileID, Index: 0, Offset: math.MaxInt64, Size: 1}); err == nil {
				t.Fatal("overflowing segment offset was accepted")
			}
			if _, err := c.serveRelayFileSource("file.segment", request); err != nil {
				t.Fatalf("initial segment: %v", err)
			}
			request.Offset = 8
			if _, err := c.serveRelayFileSource("file.segment", request); err != nil {
				t.Fatalf("cached segment: %v", err)
			}
			c.mu.RLock()
			cacheEntries := len(c.relayChunkCache)
			c.mu.RUnlock()
			if cacheEntries != 1 {
				t.Fatalf("same chunk cache entries = %d, want 1", cacheEntries)
			}
			mutated := bytes.Repeat([]byte("x"), len(data))
			if mutation == "replace" {
				if err := os.Remove(share.Path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(share.Path, mutated, 0o600); err != nil {
				t.Fatal(err)
			}
			future := time.Now().Add(2 * time.Second)
			if err := os.Chtimes(share.Path, future, future); err != nil {
				t.Fatal(err)
			}
			if value, err := c.serveRelayFileSource("file.segment", request); err == nil || value != nil {
				t.Fatalf("changed source returned data: value=%T err=%v", value, err)
			}
			c.mu.RLock()
			status := c.shares[share.FileID].Status
			c.mu.RUnlock()
			if status != "source_changed" {
				t.Fatalf("changed source status = %q", status)
			}
		})
	}
}

func TestRelayHostBridgeJoinSubmitAndSnapshot(t *testing.T) {
	t.Setenv("WORKGROUND2_HOME", t.TempDir())
	relayCfg := relayserver.DefaultConfig()
	relayCfg.RelayID = "test-relay"
	server, err := relayserver.New(relayCfg, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server.Handler())
	defer httpServer.Close()
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(httpServer.URL, "http") + "/relay/v1/connect"
	cfg := config.Default()
	if err := cfg.SetCollaboration(config.CollaborationConfig{PreferLAN: true, ConnectTimeout: 5, RouteStable: 10, Relays: []config.RelayConfig{{ID: "test-relay", URL: wsURL, Enabled: true, Priority: 100, Discovery: true}}}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(config.UserConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cfg.SaveTo(config.UserConfigPath()); err != nil {
		t.Fatal(err)
	}

	secrets := map[string]string{}
	var secretsMu sync.Mutex
	app := &App{}
	newRuntime := func() *desktopCollaboration {
		return &desktopCollaboration{
			app:       app,
			state:     CollaborationState{Status: "disconnected"},
			shares:    map[string]collaborationSharedFile{},
			setSecret: func(key, value string) error { secretsMu.Lock(); secrets[key] = value; secretsMu.Unlock(); return nil },
			getSecret: func(key string) string { secretsMu.Lock(); defer secretsMu.Unlock(); return secrets[key] },
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	roomID := newCollaborationRequestID("relay-room")
	hostRuntime := newRuntime()
	lan := false
	hostIdentity := collab.MemberDescriptor{ID: "member-host", Name: "Host", Agent: collab.AgentDescriptor{ID: "agent-host", Name: "Host Agent", Status: collab.AgentIdle}}
	hostConn, err := hostRuntime.openHostedRoom(ctx, HostCollaborationRoomInput{Room: roomID, RoomName: "Relay Room", Token: "secret", LANEnabled: &lan, RelayIDs: []string{"test-relay"}, Visibility: "public", Advertisement: &RoomAdvertisementInput{Name: "Relay Room", Tags: []string{"test"}}, SessionID: "host-session"}, hostIdentity, "")
	if err != nil {
		t.Fatal(err)
	}
	defer hostConn.close(context.Background(), false)
	if len(hostConn.routes) < 2 || hostConn.routes[1].Status != "connected" {
		t.Fatalf("host routes = %#v", hostConn.routes)
	}
	if _, ok := hostConn.filePeer.(*relayHostFilePeer); !ok {
		t.Fatalf("Relay-only Host file peer = %T, want direct Relay peer", hostConn.filePeer)
	}
	if collaborationFilePeerNeedsOrigin(hostConn.filePeer) {
		t.Fatal("Relay-only Host unexpectedly requires a local HTTP file origin")
	}
	route := hostConn.routes[1].CollaborationRouteInput
	relay := cfg.Collaboration.Relays[0]
	rooms, err := queryRelayRooms(ctx, relay, ListCollaborationRoomsInput{Query: "Relay"}, 20)
	if err != nil || len(rooms.Rooms) != 1 {
		t.Fatalf("discovery = %#v, %v", rooms, err)
	}
	if err := verifyRelayAdvertisement(rooms.Rooms[0]); err != nil {
		t.Fatal(err)
	}
	joinRef, tunnelID, err := fetchRelayJoinRef(ctx, relay, rooms.Rooms[0].PublicRoomID)
	if err != nil {
		t.Fatal(err)
	}
	route.TunnelID = tunnelID
	guestIdentity := collab.MemberDescriptor{ID: "member-guest", Name: "Guest", Agent: collab.AgentDescriptor{ID: "agent-guest", Name: "Guest Agent", Status: collab.AgentIdle}}
	guestRuntime := newRuntime()
	guestConn, err := guestRuntime.openJoinedRoom(ctx, JoinCollaborationRoomInput{
		Room: rooms.Rooms[0].PublicRoomID, Token: "secret", Routes: []CollaborationRouteInput{route},
		HostKey: rooms.Rooms[0].HostPublicKey, JoinRef: joinRef,
	}, guestIdentity, "")
	if err != nil {
		t.Fatal(err)
	}
	guest, ok := guestConn.peer.(*relayCollaborationPeer)
	if !ok {
		t.Fatalf("guest peer = %T, want Relay peer", guestConn.peer)
	}
	defer guest.Close(context.Background())
	snapshot := guestConn.initialSnapshot
	if guestConn.memberID != guestIdentity.ID || len(snapshot.Members) != 2 {
		t.Fatalf("join member = %q, snapshot members = %d", guestConn.memberID, len(snapshot.Members))
	}
	guestRef := guestConn.guestCapabilityRefs[route.RelayID]
	issuedGuestCap := guestRuntime.getSecret(guestRef)
	if issuedGuestCap == "" {
		t.Fatalf("JoinRef attach did not persist guest capability; ref = %q", guestRef)
	}
	// Verify the returned capability supports reconnect.
	guest.Close(context.Background())
	route.GuestCapability = issuedGuestCap
	reconnected, rejoined, resnapshot, _, err := joinRelayCollaborationPeer(ctx, route, rooms.Rooms[0].PublicRoomID, "secret", rooms.Rooms[0].HostPublicKey, "", guestIdentity, guestConn.connectionSession)
	if err != nil {
		t.Fatal("reconnect with issued guest capability:", err)
	}
	defer reconnected.Close(context.Background())
	if !rejoined.Rejoined || len(resnapshot.Members) != 2 {
		t.Fatalf("reconnect join = %#v, snapshot members = %d", rejoined, len(resnapshot.Members))
	}
	// Simulate a legacy persisted client that has neither Guest Capability nor
	// JoinRef. A Discovery route must acquire a fresh JoinRef and restore the
	// reusable secret automatically.
	reconnected.Close(context.Background())
	route.GuestCapability = ""
	secretsMu.Lock()
	delete(secrets, guestRef)
	secretsMu.Unlock()
	legacyRuntime := newRuntime()
	recoveredConn, err := legacyRuntime.openJoinedRoom(ctx, JoinCollaborationRoomInput{
		Room: rooms.Rooms[0].PublicRoomID, Token: "secret", Routes: []CollaborationRouteInput{route},
		HostKey: rooms.Rooms[0].HostPublicKey,
	}, guestIdentity, rejoined.ConnectionSession)
	if err != nil {
		t.Fatal("recover legacy Relay route:", err)
	}
	recovered, ok := recoveredConn.peer.(*relayCollaborationPeer)
	if !ok {
		t.Fatalf("recovered peer = %T, want Relay peer", recoveredConn.peer)
	}
	defer recovered.Close(context.Background())
	if recoveredRef := recoveredConn.guestCapabilityRefs[route.RelayID]; recoveredRef == "" || legacyRuntime.getSecret(recoveredRef) == "" {
		t.Fatalf("legacy Relay recovery did not persist guest capability; ref = %q", recoveredRef)
	}
	// Use the recovered peer for remaining tests.
	guest = recovered
	_, err = guest.Submit(ctx, collab.CommandEnvelope{RequestID: "chat-1", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "cross-network hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = fetchCollaborationSnapshot(ctx, guest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range snapshot.Timeline {
		if item.Chat != nil && item.Chat.Text == "cross-network hello" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Relay chat missing from snapshot: %#v", snapshot.Timeline)
	}
	source := bytes.Repeat([]byte("relay-file-segment-"), 9000)
	sourcePath := filepath.Join(t.TempDir(), "relay.bin")
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	share, err := hostRuntime.prepareSharedFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	_, shareAuthority := establishCollaborationRoomInstance(hostConn)
	share.Room, share.ShareAuthority, share.OwnerID, share.Status = roomID, shareAuthority, hostConn.memberID, "available"
	hostRuntime.mu.Lock()
	hostRuntime.state.Room, hostRuntime.state.MemberID = roomID, hostConn.memberID
	hostRuntime.shareAuthority = shareAuthority
	hostRuntime.shares[share.FileID] = share
	hostRuntime.mu.Unlock()
	_, err = hostConn.peer.Submit(ctx, collab.CommandEnvelope{RequestID: share.FileID + ":offer", Command: collab.Command{Type: collab.CommandOfferFile, FileOffer: &collab.OfferFileInput{FileID: share.FileID, Name: share.Name, Size: share.Size, MIME: share.MIME, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes)}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, manifest, err := guest.fetchFileManifest(ctx, share.FileID, 4096, true)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FileID != share.FileID || len(manifest.ChunkHashes) != 1 {
		t.Fatalf("manifest = %#v", manifest)
	}
	received, err := guest.fetchFileChunk(ctx, ticket, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, source) {
		t.Fatalf("Relay file mismatch: got %d, want %d", len(received), len(source))
	}
}
