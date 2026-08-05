package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	newRuntime := func() *desktopCollaboration {
		return &desktopCollaboration{
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
	guest, joined, snapshot, err := joinRelayCollaborationPeer(ctx, route, rooms.Rooms[0].PublicRoomID, "secret", rooms.Rooms[0].HostPublicKey, joinRef, guestIdentity, "")
	if err != nil {
		t.Fatal(err)
	}
	defer guest.Close(context.Background())
	if joined.Member.ID != guestIdentity.ID || len(snapshot.Members) != 2 {
		t.Fatalf("join = %#v, snapshot members = %d", joined, len(snapshot.Members))
	}
	_, err = guest.Submit(ctx, collab.CommandEnvelope{RequestID: "chat-1", Command: collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: "cross-network hello"}}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = guest.Snapshot(ctx)
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
	share.Room, share.OwnerID, share.Status = roomID, hostConn.memberID, "available"
	hostRuntime.mu.Lock()
	hostRuntime.shares[share.FileID] = share
	hostRuntime.mu.Unlock()
	_, err = hostConn.peer.Submit(ctx, collab.CommandEnvelope{RequestID: share.FileID + ":offer", Command: collab.Command{Type: collab.CommandOfferFile, FileOffer: &collab.OfferFileInput{FileID: share.FileID, Name: share.Name, Size: share.Size, MIME: share.MIME, SHA256: share.SHA256, ManifestHash: share.ManifestHash, ChunkSize: share.ChunkSize, ChunkCount: len(share.ChunkHashes)}}})
	if err != nil {
		t.Fatal(err)
	}
	ticket, manifest, err := guest.fetchFileManifest(ctx, share.FileID)
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
