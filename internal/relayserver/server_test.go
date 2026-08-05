package relayserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"workground2/internal/relayproto"
)

func TestHealthAndReady(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s returned %d", path, resp.StatusCode)
		}
	}
}

func TestTunnelAttachAndOpaqueRouting(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	host := dialRelay(t, ts)
	defer host.Close()
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: "create-1"}, relayproto.TunnelCreate{})
	h, payload := readFrame(t, host)
	if h.Type != relayproto.TypeHostBound {
		t.Fatalf("unexpected create response: %s", h.Type)
	}
	var bound relayproto.HostBound
	if err := json.Unmarshal(payload, &bound); err != nil {
		t.Fatal(err)
	}
	guest := dialRelay(t, ts)
	defer guest.Close()
	writeFrame(t, guest, relayproto.Header{Type: relayproto.TypeGuestAttach, RelayRequestID: "attach-1", TunnelID: bound.TunnelID}, relayproto.GuestAttach{Capability: bound.GuestCapability})
	gh, gp := readFrame(t, guest)
	if gh.Type != relayproto.TypePeerOpened {
		t.Fatalf("unexpected guest response: %s %s", gh.Type, gp)
	}
	var opened relayproto.PeerOpened
	_ = json.Unmarshal(gp, &opened)
	hh, _ := readFrame(t, host)
	if hh.Type != relayproto.TypePeerOpened || hh.PeerID != opened.PeerID {
		t.Fatalf("host missed peer open: %#v", hh)
	}
	opaque := []byte{0, 1, 2, 3, 255}
	message, err := relayproto.Encode(relayproto.Header{Type: "rpc.request", TunnelID: bound.TunnelID, PeerID: opened.PeerID, Epoch: 1, Sequence: 1}, opaque)
	if err != nil {
		t.Fatal(err)
	}
	if err := guest.WriteMessage(websocket.BinaryMessage, message); err != nil {
		t.Fatal(err)
	}
	routedHeader, routedPayload := readFrame(t, host)
	if routedHeader.PeerID != opened.PeerID || string(routedPayload) != string(opaque) {
		t.Fatalf("opaque frame changed: %#v %v", routedHeader, routedPayload)
	}
}

func TestHostCapabilityRebindAfterRestart(t *testing.T) {
	key := []byte("01234567890123456789012345678901")
	cfg := DefaultConfig()
	cfg.IdleTimeout = time.Minute
	first, _ := New(cfg, key)
	ts1 := httptest.NewServer(first.Handler())
	host := dialRelayAt(t, ts1.URL)
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: "create"}, relayproto.TunnelCreate{})
	_, p := readFrame(t, host)
	var bound relayproto.HostBound
	_ = json.Unmarshal(p, &bound)
	host.Close()
	ts1.Close()
	second, _ := New(cfg, key)
	ts2 := httptest.NewServer(second.Handler())
	defer ts2.Close()
	rebound := dialRelayAt(t, ts2.URL)
	defer rebound.Close()
	writeFrame(t, rebound, relayproto.Header{Type: relayproto.TypeHostBind, TunnelID: bound.TunnelID, RelayRequestID: "bind"}, relayproto.HostBind{Capability: bound.HostCapability})
	h, _ := readFrame(t, rebound)
	if h.Type != relayproto.TypeHostBound || h.TunnelID != bound.TunnelID {
		t.Fatalf("rebind failed: %#v", h)
	}
}

func TestPeerStreamRequiresHostGrant(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	host := dialRelay(t, ts)
	defer host.Close()
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: "create"}, relayproto.TunnelCreate{})
	_, p := readFrame(t, host)
	var bound relayproto.HostBound
	_ = json.Unmarshal(p, &bound)
	guestA, peerA := attachTestGuest(t, ts, host, bound)
	defer guestA.Close()
	guestB, peerB := attachTestGuest(t, ts, host, bound)
	defer guestB.Close()
	writeFrame(t, guestA, relayproto.Header{Type: "stream.open", TunnelID: bound.TunnelID, PeerID: peerB, StreamID: 1}, map[string]any{"encrypted": true})
	denied, _ := readFrame(t, guestA)
	if denied.Type != relayproto.TypeError {
		t.Fatalf("ungranted stream accepted: %s", denied.Type)
	}
	grant := relayproto.StreamGrant{GrantID: "grant-1", PeerA: peerA, PeerB: peerB, ExpiresAt: time.Now().Add(time.Minute)}
	writeFrame(t, host, relayproto.Header{Type: "stream.grant", TunnelID: bound.TunnelID, RelayRequestID: "grant"}, grant)
	ack, _ := readFrame(t, host)
	if ack.Type != "stream.grant" {
		t.Fatalf("grant rejected: %s", ack.Type)
	}
	writeFrame(t, guestA, relayproto.Header{Type: "stream.open", TunnelID: bound.TunnelID, PeerID: peerB, StreamID: 1}, map[string]any{"encrypted": true})
	opened, _ := readFrame(t, guestB)
	if opened.Type != "stream.open" || opened.PeerID != peerA {
		t.Fatalf("granted stream not routed: %#v", opened)
	}
}

func TestDiscoveryAndSingleUseJoinRef(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	host := dialRelay(t, ts)
	defer host.Close()
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: "create"}, relayproto.TunnelCreate{})
	_, p := readFrame(t, host)
	var bound relayproto.HostBound
	_ = json.Unmarshal(p, &bound)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ad := relayproto.Advertisement{PublicRoomID: "room_public_abc", Name: "Profile Room", Tags: []string{"backend"}, Visibility: "public", HostPublicKey: base64.RawStdEncoding.EncodeToString(pub), HostKeyFingerprint: relayproto.HostKeyFingerprint(pub), AdvertisementRevision: 1, ExpiresAt: time.Now().Add(30 * time.Second).UTC()}
	signed, _ := relayproto.AdvertisementSigningBytes(ad)
	ad.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, signed))
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeAdvertisementUpsert, RelayRequestID: "ad-1", TunnelID: bound.TunnelID}, relayproto.AdvertisementUpsert{Advertisement: ad})
	h, _ := readFrame(t, host)
	if h.Type != relayproto.TypeAdvertisementUpsert {
		t.Fatalf("advertisement rejected: %s", h.Type)
	}
	resp, err := http.Get(ts.URL + "/relay/v1/rooms?query=profile&tag=backend")
	if err != nil {
		t.Fatal(err)
	}
	var list relayproto.RoomList
	_ = json.NewDecoder(resp.Body).Decode(&list)
	_ = resp.Body.Close()
	if len(list.Rooms) != 1 {
		t.Fatalf("unexpected discovery result: %#v", list)
	}
	resp, err = http.Post(ts.URL+"/relay/v1/rooms/room_public_abc/join-ref", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ref relayproto.JoinRefResult
	_ = json.NewDecoder(resp.Body).Decode(&ref)
	_ = resp.Body.Close()
	if ref.JoinRef == "" {
		t.Fatal("join-ref was not issued")
	}
	guest := dialRelay(t, ts)
	defer guest.Close()
	writeFrame(t, guest, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: ref.TunnelID}, relayproto.GuestAttach{JoinRef: ref.JoinRef})
	joined, _ := readFrame(t, guest)
	if joined.Type != relayproto.TypePeerOpened {
		t.Fatalf("join-ref attach failed: %s", joined.Type)
	}
	guest2 := dialRelay(t, ts)
	defer guest2.Close()
	writeFrame(t, guest2, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: ref.TunnelID}, relayproto.GuestAttach{JoinRef: ref.JoinRef})
	reused, _ := readFrame(t, guest2)
	if reused.Type != relayproto.TypeError {
		t.Fatalf("reused join-ref accepted: %s", reused.Type)
	}
}

func TestJoinRefReturnsReusableGuestCapability(t *testing.T) {
	srv := newTestServer(t)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	host := dialRelay(t, ts)
	defer host.Close()
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: "create"}, relayproto.TunnelCreate{})
	_, p := readFrame(t, host)
	var bound relayproto.HostBound
	_ = json.Unmarshal(p, &bound)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ad := relayproto.Advertisement{PublicRoomID: "room_cap", Name: "Cap Room", Tags: []string{"test"}, Visibility: "public", HostPublicKey: base64.RawStdEncoding.EncodeToString(pub), HostKeyFingerprint: relayproto.HostKeyFingerprint(pub), AdvertisementRevision: 1, ExpiresAt: time.Now().Add(30 * time.Second).UTC()}
	signed, _ := relayproto.AdvertisementSigningBytes(ad)
	ad.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(priv, signed))
	writeFrame(t, host, relayproto.Header{Type: relayproto.TypeAdvertisementUpsert, RelayRequestID: "ad", TunnelID: bound.TunnelID}, relayproto.AdvertisementUpsert{Advertisement: ad})
	readFrame(t, host)
	resp, err := http.Post(ts.URL+"/relay/v1/rooms/room_cap/join-ref", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ref relayproto.JoinRefResult
	_ = json.NewDecoder(resp.Body).Decode(&ref)
	_ = resp.Body.Close()
	if ref.JoinRef == "" {
		t.Fatal("join-ref was not issued")
	}
	// Step 1: attach with JoinRef, verify PeerOpened includes a reusable GuestCapability.
	guest := dialRelay(t, ts)
	writeFrame(t, guest, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: ref.TunnelID}, relayproto.GuestAttach{JoinRef: ref.JoinRef})
	gh, gp := readFrame(t, guest)
	if gh.Type != relayproto.TypePeerOpened {
		t.Fatalf("join-ref attach failed: %s %s", gh.Type, gp)
	}
	var opened relayproto.PeerOpened
	if err := json.Unmarshal(gp, &opened); err != nil {
		t.Fatal(err)
	}
	if opened.GuestCapability == "" {
		t.Fatal("PeerOpened returned empty GuestCapability after JoinRef attach")
	}
	if opened.PeerID == "" {
		t.Fatal("PeerOpened returned empty PeerID")
	}
	// Step 2: JoinRef is single-use — second attach with same JoinRef must fail.
	guest2 := dialRelay(t, ts)
	defer guest2.Close()
	writeFrame(t, guest2, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: ref.TunnelID}, relayproto.GuestAttach{JoinRef: ref.JoinRef})
	gh2, _ := readFrame(t, guest2)
	if gh2.Type != relayproto.TypeError {
		t.Fatalf("reused join-ref was accepted: %s", gh2.Type)
	}
	// Step 3: disconnect first guest and reconnect with the returned GuestCapability.
	guest.Close()
	reconnected := dialRelay(t, ts)
	defer reconnected.Close()
	writeFrame(t, reconnected, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: opened.TunnelID, RelayRequestID: "reconnect"}, relayproto.GuestAttach{Capability: opened.GuestCapability})
	rh, rp := readFrame(t, reconnected)
	if rh.Type != relayproto.TypePeerOpened {
		t.Fatalf("reconnect with issued guest capability failed: %s %s", rh.Type, rp)
	}
	var reopened relayproto.PeerOpened
	if err := json.Unmarshal(rp, &reopened); err != nil {
		t.Fatal(err)
	}
	if reopened.PeerID == "" {
		t.Fatal("reconnect PeerOpened returned empty PeerID")
	}
}

func TestLoadOrCreateMasterKeyStable(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateMasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || len(first) != 32 {
		t.Fatal("master key was not persisted")
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	cfg := DefaultConfig()
	cfg.IdleTimeout = time.Minute
	cfg.AdvertisementTTL = time.Minute
	srv, err := New(cfg, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	return srv
}
func dialRelay(t *testing.T, ts *httptest.Server) *websocket.Conn {
	t.Helper()
	return dialRelayAt(t, ts.URL)
}
func dialRelayAt(t *testing.T, base string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(base, "http") + "/relay/v1/connect"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}
func writeFrame(t *testing.T, conn *websocket.Conn, h relayproto.Header, payload any) {
	t.Helper()
	p, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	message, err := relayproto.Encode(h, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
		t.Fatal(err)
	}
}
func readFrame(t *testing.T, conn *websocket.Conn) (relayproto.Header, []byte) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	kind, message, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if kind != websocket.BinaryMessage {
		t.Fatalf("unexpected websocket message type %d", kind)
	}
	h, p, err := relayproto.Decode(message)
	if err != nil {
		t.Fatal(err)
	}
	return h, p
}

func attachTestGuest(t *testing.T, ts *httptest.Server, host *websocket.Conn, bound relayproto.HostBound) (*websocket.Conn, string) {
	t.Helper()
	guest := dialRelay(t, ts)
	writeFrame(t, guest, relayproto.Header{Type: relayproto.TypeGuestAttach, TunnelID: bound.TunnelID}, relayproto.GuestAttach{Capability: bound.GuestCapability})
	h, _ := readFrame(t, guest)
	if h.Type != relayproto.TypePeerOpened {
		t.Fatalf("attach failed: %s", h.Type)
	}
	hostOpened, _ := readFrame(t, host)
	if hostOpened.Type != relayproto.TypePeerOpened {
		t.Fatalf("host attach notification missing: %s", hostOpened.Type)
	}
	return guest, h.PeerID
}
