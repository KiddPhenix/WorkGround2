package relayserver

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"workground2/internal/relayproto"
)

type Server struct {
	cfg       Config
	signer    *relayproto.Signer
	registry  *registry
	discovery *discovery
	limiter   *rateLimiter
	upgrader  websocket.Upgrader
	ready     atomic.Bool
	clientsMu sync.Mutex
	clients   map[*client]struct{}
}

func New(cfg Config, masterKey []byte) (*Server, error) {
	cfg.normalize()
	if cfg.AccessMode != "public" && cfg.AccessMode != "token" {
		return nil, errors.New("relay access mode must be public or token")
	}
	if cfg.AccessMode == "token" && cfg.AccessToken == "" {
		return nil, errors.New("relay access token is required in token mode")
	}
	signer, err := relayproto.NewSigner(cfg.RelayID, masterKey)
	if err != nil {
		return nil, err
	}
	registry := newRegistry(cfg)
	s := &Server{cfg: cfg, signer: signer, registry: registry, limiter: newRateLimiter(cfg.RequestsPerMinute), clients: make(map[*client]struct{})}
	s.discovery = newDiscovery(cfg.AdvertisementTTL, cfg.JoinRefTTL, signer, registry)
	s.upgrader = websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 4096, CheckOrigin: func(*http.Request) bool { return true }}
	s.ready.Store(true)
	return s, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /relay/v1/connect", s.connect)
	mux.HandleFunc("GET /relay/v1/rooms", s.rooms)
	mux.HandleFunc("GET /relay/v1/rooms/{id}", s.room)
	mux.HandleFunc("POST /relay/v1/rooms/{id}/join-ref", s.joinRef)
	return mux
}

// MetricsHandler exposes only the Prometheus endpoint for a separate,
// typically loopback-only listener.
func (s *Server) MetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", s.metrics)
	return mux
}

// Close marks the Relay unready and terminates active connections.
func (s *Server) Close() {
	s.ready.Store(false)
	s.clientsMu.Lock()
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.clientsMu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}
func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"status": "not_ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ready"})
}
func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	tunnels, peers := s.registry.counts()
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	_, _ = fmt.Fprintf(w, "workground2_relay_tunnels %d\nworkground2_relay_peers %d\n", tunnels, peers)
}

func (s *Server) rooms(w http.ResponseWriter, r *http.Request) {
	if !s.discoveryAllowed(w) || !s.allowRequest(w, r) {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			limit = n
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	result := s.discovery.list(r.URL.Query().Get("query"), r.URL.Query().Get("tag"), r.URL.Query().Get("cursor"), limit)
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) room(w http.ResponseWriter, r *http.Request) {
	if !s.discoveryAllowed(w) || !s.allowRequest(w, r) {
		return
	}
	record, ok := s.discovery.get(r.PathValue("id"))
	if !ok {
		writeRelayError(w, http.StatusNotFound, relayproto.RelayError{Code: relayproto.ErrTunnelNotFound, Message: "room is not active", Retryable: true})
		return
	}
	writeJSON(w, http.StatusOK, record.ad)
}

func (s *Server) joinRef(w http.ResponseWriter, r *http.Request) {
	if !s.discoveryAllowed(w) || !s.allowRequest(w, r) {
		return
	}
	result, err := s.discovery.issueJoin(r.PathValue("id"))
	if err != nil {
		writeRelayError(w, http.StatusNotFound, relayproto.RelayError{Code: relayproto.ErrHostOffline, Message: err.Error(), Retryable: true})
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (s *Server) discoveryAllowed(w http.ResponseWriter) bool {
	if s.cfg.AllowDiscovery {
		return true
	}
	writeRelayError(w, http.StatusNotFound, relayproto.RelayError{Code: "discovery_disabled", Message: "room discovery is disabled"})
	return false
}
func (s *Server) allowRequest(w http.ResponseWriter, r *http.Request) bool {
	if s.limiter.allow(remoteIP(r)) {
		return true
	}
	w.Header().Set("Retry-After", "60")
	writeRelayError(w, http.StatusTooManyRequests, relayproto.RelayError{Code: relayproto.ErrRateLimited, Message: "request rate exceeded", Retryable: true, RetryAfter: 60})
	return false
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(w, r) {
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := newClient(s, conn)
	s.clientsMu.Lock()
	s.clients[c] = struct{}{}
	s.clientsMu.Unlock()
	go c.writeLoop()
	c.readLoop()
}

func (s *Server) authorized(token string) bool {
	return s.cfg.AccessMode == "public" || hmac.Equal([]byte(token), []byte(s.cfg.AccessToken))
}

type outbound struct {
	message []byte
	control bool
}

type client struct {
	server     *Server
	conn       *websocket.Conn
	control    chan []byte
	data       chan []byte
	done       chan struct{}
	closeOnce  sync.Once
	tunnelID   string
	peerID     string
	isHost     bool
	authorized bool
	results    map[string][]byte
	streams    map[string]struct{}
	rateStart  time.Time
	frameCount int
	byteCount  int64
}

func newClient(s *Server, conn *websocket.Conn) *client {
	return &client{server: s, conn: conn, control: make(chan []byte, s.cfg.ControlQueue), data: make(chan []byte, s.cfg.DataQueue), done: make(chan struct{}), results: make(map[string][]byte), streams: make(map[string]struct{}), rateStart: time.Now()}
}

func (c *client) readLoop() {
	defer c.close()
	c.conn.SetReadLimit(c.server.cfg.MaxFrameBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(c.server.cfg.IdleTimeout))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(c.server.cfg.IdleTimeout))
		if c.isHost {
			c.server.registry.touch(c.tunnelID, c)
		}
		return nil
	})
	for {
		messageType, message, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(c.server.cfg.IdleTimeout))
		if messageType != websocket.BinaryMessage {
			c.sendError("invalid_frame", "binary WebSocket message required", false, "")
			continue
		}
		if !c.allowFrame(len(message)) {
			c.sendError(relayproto.ErrRateLimited, "connection frame or bandwidth rate exceeded", true, "")
			continue
		}
		h, payload, err := relayproto.Decode(message)
		if err != nil {
			c.sendError("invalid_frame", err.Error(), false, "")
			continue
		}
		if cached := c.results[h.RelayRequestID]; h.RelayRequestID != "" && cached != nil {
			c.enqueue(cached, true)
			continue
		}
		c.handle(h, payload, message)
	}
}

func (c *client) handle(h relayproto.Header, payload, original []byte) {
	switch h.Type {
	case relayproto.TypeHello:
		var req relayproto.Hello
		if relayproto.UnmarshalPayload(payload, &req) != nil {
			c.sendError("invalid_request", "invalid hello", false, h.RelayRequestID)
			return
		}
		if req.MinVersion > relayproto.Version || req.MaxVersion < relayproto.Version {
			c.sendError(relayproto.ErrProtocolMismatch, "no common relay protocol version", false, h.RelayRequestID)
			return
		}
		c.authorized = c.server.authorized(req.AccessToken)
		if !c.authorized {
			c.sendError(relayproto.ErrAuthFailed, "relay access token rejected", false, h.RelayRequestID)
			return
		}
		c.sendPayload(relayproto.Header{Type: relayproto.TypeHello, RelayRequestID: h.RelayRequestID}, relayproto.HelloResult{Version: relayproto.Version, RelayID: c.server.cfg.RelayID, Capabilities: c.capabilities()}, true, true)
	case relayproto.TypeTunnelCreate:
		var req relayproto.TunnelCreate
		_ = json.Unmarshal(payload, &req)
		if !c.authorized && !c.server.authorized(req.AccessToken) {
			c.sendError(relayproto.ErrAuthFailed, "relay access token rejected", false, h.RelayRequestID)
			return
		}
		c.createTunnel(h)
	case relayproto.TypeHostBind:
		var req relayproto.HostBind
		if relayproto.UnmarshalPayload(payload, &req) != nil || (!c.authorized && !c.server.authorized(req.AccessToken)) {
			c.sendError(relayproto.ErrAuthFailed, "host bind rejected", false, h.RelayRequestID)
			return
		}
		claims, err := c.server.signer.Verify(req.Capability, relayproto.RoleHost, h.TunnelID)
		if err != nil {
			c.capabilityError(err, h.RelayRequestID)
			return
		}
		if err := c.server.registry.bind(claims.TunnelID, c); err != nil {
			c.registryError(err, h.RelayRequestID)
			return
		}
		c.isHost, c.tunnelID = true, claims.TunnelID
		c.sendPayload(relayproto.Header{Type: relayproto.TypeHostBound, RelayRequestID: h.RelayRequestID, TunnelID: claims.TunnelID}, relayproto.HostBound{TunnelID: claims.TunnelID, ExpiresAt: claims.ExpiresAt}, true, true)
	case relayproto.TypeGuestAttach:
		c.attachGuest(h, payload)
	case relayproto.TypePing:
		if c.isHost {
			c.server.registry.touch(c.tunnelID, c)
		}
		c.sendPayload(relayproto.Header{Type: relayproto.TypePong, RelayRequestID: h.RelayRequestID, TunnelID: c.tunnelID, PeerID: c.peerID}, map[string]any{"time": time.Now().UnixMilli()}, true, false)
	case relayproto.TypeAdvertisementUpsert:
		c.upsertAdvertisement(h, payload)
	case relayproto.TypeAdvertisementRevoke:
		c.revokeAdvertisement(h, payload)
	case "stream.grant":
		c.grantStream(h, payload)
	default:
		if relayproto.IsRoutable(h.Type) {
			c.route(h, original)
			return
		}
		c.sendError("unsupported_frame", "unsupported relay frame type", false, h.RelayRequestID)
	}
}

func (c *client) createTunnel(h relayproto.Header) {
	if c.tunnelID != "" {
		c.sendError("invalid_state", "connection already attached", false, h.RelayRequestID)
		return
	}
	id, err := randomID("tun_", 18)
	if err != nil {
		c.sendError("internal_error", err.Error(), true, h.RelayRequestID)
		return
	}
	if err := c.server.registry.create(id, c); err != nil {
		c.registryError(err, h.RelayRequestID)
		return
	}
	hostCap, claims, err := c.server.signer.Issue(id, relayproto.RoleHost, c.server.cfg.CapabilityTTL, relayproto.CapabilityLimits{MaxStreams: 32})
	if err != nil {
		c.sendError("internal_error", err.Error(), true, h.RelayRequestID)
		return
	}
	guestCap, _, err := c.server.signer.Issue(id, relayproto.RoleGuest, c.server.cfg.CapabilityTTL, relayproto.CapabilityLimits{MaxStreams: 32})
	if err != nil {
		c.sendError("internal_error", err.Error(), true, h.RelayRequestID)
		return
	}
	c.isHost, c.tunnelID, c.authorized = true, id, true
	c.sendPayload(relayproto.Header{Type: relayproto.TypeHostBound, RelayRequestID: h.RelayRequestID, TunnelID: id}, relayproto.HostBound{TunnelID: id, HostCapability: hostCap, GuestCapability: guestCap, ExpiresAt: claims.ExpiresAt}, true, true)
}

func (c *client) attachGuest(h relayproto.Header, payload []byte) {
	if c.tunnelID != "" {
		c.sendError("invalid_state", "connection already attached", false, h.RelayRequestID)
		return
	}
	var req relayproto.GuestAttach
	if relayproto.UnmarshalPayload(payload, &req) != nil {
		c.sendError("invalid_request", "invalid guest attach", false, h.RelayRequestID)
		return
	}
	var tunnelID string
	var issuedGuestCap string
	if req.JoinRef != "" {
		claims, err := c.server.signer.Verify(req.JoinRef, relayproto.RoleJoin, h.TunnelID)
		if err != nil {
			c.capabilityError(err, h.RelayRequestID)
			return
		}
		tunnelID = claims.TunnelID
		issuedGuestCap, _, err = c.server.signer.Issue(tunnelID, relayproto.RoleGuest, c.server.cfg.CapabilityTTL, relayproto.CapabilityLimits{MaxStreams: c.server.cfg.MaxStreamsPerPeer})
		if err != nil {
			c.sendError("internal_error", "issue guest capability: "+err.Error(), true, h.RelayRequestID)
			return
		}
		if err := c.server.discovery.consumeJoin(req.JoinRef, tunnelID); err != nil {
			c.sendError(relayproto.ErrAuthFailed, err.Error(), false, h.RelayRequestID)
			return
		}
	} else {
		claims, err := c.server.signer.Verify(req.Capability, relayproto.RoleGuest, h.TunnelID)
		if err != nil {
			c.capabilityError(err, h.RelayRequestID)
			return
		}
		tunnelID = claims.TunnelID
	}
	peerID, err := randomID("peer_", 12)
	if err != nil {
		c.sendError("internal_error", err.Error(), true, h.RelayRequestID)
		return
	}
	host, err := c.server.registry.attach(tunnelID, peerID, c)
	if err != nil {
		c.registryError(err, h.RelayRequestID)
		return
	}
	c.tunnelID, c.peerID = tunnelID, peerID
	opened := relayproto.PeerOpened{TunnelID: tunnelID, PeerID: peerID, GuestCapability: issuedGuestCap}
	c.sendPayload(relayproto.Header{Type: relayproto.TypePeerOpened, RelayRequestID: h.RelayRequestID, TunnelID: tunnelID, PeerID: peerID}, opened, true, true)
	host.sendPayload(relayproto.Header{Type: relayproto.TypePeerOpened, TunnelID: tunnelID, PeerID: peerID}, relayproto.PeerOpened{TunnelID: tunnelID, PeerID: peerID}, true, false)
}

func (c *client) route(h relayproto.Header, original []byte) {
	if c.tunnelID == "" || (h.TunnelID != "" && h.TunnelID != c.tunnelID) {
		c.sendError(relayproto.ErrTunnelNotFound, "connection is not attached to tunnel", true, h.RelayRequestID)
		return
	}
	h.TunnelID = c.tunnelID
	streamPeer := h.PeerID
	if !c.isHost {
		streamPeer = c.peerID
	}
	streamKey := streamPeer + ":" + strconv.FormatUint(uint64(h.StreamID), 10)
	if h.Type == "stream.open" {
		if _, exists := c.streams[streamKey]; !exists {
			if len(c.streams) >= c.server.cfg.MaxStreamsPerPeer {
				c.sendError(relayproto.ErrRateLimited, "concurrent stream limit reached", true, h.RelayRequestID)
				return
			}
			c.streams[streamKey] = struct{}{}
		}
	}
	var target *client
	if c.isHost {
		if h.PeerID == "" {
			c.sendError("invalid_route", "peer id is required", false, h.RelayRequestID)
			return
		}
		target = c.server.registry.peer(c.tunnelID, h.PeerID)
	} else {
		targetPeer := h.PeerID
		if strings.HasPrefix(h.Type, "stream.") && targetPeer != "" && targetPeer != c.peerID {
			if !c.server.registry.streamAllowed(c.tunnelID, c.peerID, targetPeer) {
				c.sendError("stream_not_granted", "peer stream is not granted", false, h.RelayRequestID)
				return
			}
			target = c.server.registry.peer(c.tunnelID, targetPeer)
		} else {
			target = c.server.registry.host(c.tunnelID)
		}
		h.PeerID = c.peerID
	}
	if target == nil {
		c.sendError(relayproto.ErrHostOffline, "route target is offline", true, h.RelayRequestID)
		return
	}
	_, payload, _ := relayproto.Decode(original)
	message, err := relayproto.Encode(h, payload)
	if err != nil {
		c.sendError("invalid_frame", err.Error(), false, h.RelayRequestID)
		return
	}
	if !target.enqueue(message, !relayproto.IsDataFrame(h.Type)) {
		c.sendError(relayproto.ErrBackpressure, "target send queue is full", true, h.RelayRequestID)
	}
	if h.Type == "stream.end" || h.Type == "stream.reset" {
		delete(c.streams, streamKey)
	}
}

func (c *client) grantStream(h relayproto.Header, payload []byte) {
	if !c.isHost {
		c.sendError("stream_not_granted", "host binding required", false, h.RelayRequestID)
		return
	}
	var grant relayproto.StreamGrant
	if relayproto.UnmarshalPayload(payload, &grant) != nil {
		c.sendError("invalid_request", "invalid stream grant", false, h.RelayRequestID)
		return
	}
	if err := c.server.registry.grant(c.tunnelID, grant.PeerA, grant.PeerB, grant.ExpiresAt); err != nil {
		c.sendError("stream_not_granted", err.Error(), false, h.RelayRequestID)
		return
	}
	c.sendPayload(relayproto.Header{Type: "stream.grant", RelayRequestID: h.RelayRequestID, TunnelID: c.tunnelID}, map[string]any{"accepted": true, "grantId": grant.GrantID}, true, true)
}

func (c *client) allowFrame(bytes int) bool {
	now := time.Now()
	if now.Sub(c.rateStart) >= time.Second {
		c.rateStart, c.frameCount, c.byteCount = now, 0, 0
	}
	c.frameCount++
	c.byteCount += int64(bytes)
	return c.frameCount <= c.server.cfg.FramesPerSecond && c.byteCount <= c.server.cfg.BytesPerSecond
}

func (c *client) upsertAdvertisement(h relayproto.Header, payload []byte) {
	if !c.isHost || !c.server.cfg.AllowDiscovery {
		c.sendError(relayproto.ErrAdvertisementRejected, "advertisement is not allowed", false, h.RelayRequestID)
		return
	}
	var req relayproto.AdvertisementUpsert
	if relayproto.UnmarshalPayload(payload, &req) != nil {
		c.sendError(relayproto.ErrAdvertisementRejected, "invalid advertisement", false, h.RelayRequestID)
		return
	}
	if err := c.server.discovery.upsert(c.tunnelID, req.Advertisement); err != nil {
		c.sendError(relayproto.ErrAdvertisementRejected, err.Error(), true, h.RelayRequestID)
		return
	}
	c.sendPayload(relayproto.Header{Type: relayproto.TypeAdvertisementUpsert, RelayRequestID: h.RelayRequestID, TunnelID: c.tunnelID}, map[string]any{"accepted": true, "advertisementRevision": req.Advertisement.AdvertisementRevision}, true, true)
}

func (c *client) revokeAdvertisement(h relayproto.Header, payload []byte) {
	if !c.isHost {
		c.sendError(relayproto.ErrAdvertisementRejected, "host binding required", false, h.RelayRequestID)
		return
	}
	var req relayproto.AdvertisementRevoke
	if relayproto.UnmarshalPayload(payload, &req) != nil {
		c.sendError(relayproto.ErrAdvertisementRejected, "invalid revoke", false, h.RelayRequestID)
		return
	}
	c.server.discovery.revoke(c.tunnelID, req.PublicRoomID, req.Revision)
	c.sendPayload(relayproto.Header{Type: relayproto.TypeAdvertisementRevoke, RelayRequestID: h.RelayRequestID, TunnelID: c.tunnelID}, map[string]any{"revoked": true}, true, true)
}

func (c *client) capabilities() []string {
	caps := []string{"rpc", "event", "file_stream"}
	if c.server.cfg.AllowDiscovery {
		caps = append(caps, "discovery_v1")
	}
	return caps
}

func (c *client) writeLoop() {
	ticker := time.NewTicker(c.server.cfg.HostHeartbeat)
	defer ticker.Stop()
	defer c.close()
	for {
		var message []byte
		select {
		case message = <-c.control:
		default:
			select {
			case message = <-c.control:
			case message = <-c.data:
			case <-ticker.C:
				_ = c.conn.SetWriteDeadline(time.Now().Add(c.server.cfg.WriteTimeout))
				if c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.server.cfg.WriteTimeout)) != nil {
					return
				}
				continue
			case <-c.done:
				return
			}
		}
		_ = c.conn.SetWriteDeadline(time.Now().Add(c.server.cfg.WriteTimeout))
		if c.conn.WriteMessage(websocket.BinaryMessage, message) != nil {
			return
		}
	}
}

func (c *client) enqueue(message []byte, control bool) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	if control {
		select {
		case c.control <- message:
			return true
		default:
			return false
		}
	}
	select {
	case c.data <- message:
		return true
	default:
		return false
	}
}

func (c *client) sendPayload(h relayproto.Header, payload any, control, cache bool) {
	b, err := relayproto.MarshalPayload(payload)
	if err != nil {
		return
	}
	message, err := relayproto.Encode(h, b)
	if err != nil {
		return
	}
	if cache && h.RelayRequestID != "" {
		c.results[h.RelayRequestID] = message
	}
	_ = c.enqueue(message, control)
}
func (c *client) sendError(code, message string, retryable bool, requestID string) {
	c.sendPayload(relayproto.Header{Type: relayproto.TypeError, RelayRequestID: requestID, TunnelID: c.tunnelID, PeerID: c.peerID}, relayproto.RelayError{Code: code, Message: message, Retryable: retryable}, true, false)
}
func (c *client) capabilityError(err error, requestID string) {
	code := relayproto.ErrAuthFailed
	retry := false
	if strings.Contains(err.Error(), "expired") {
		code, retry = relayproto.ErrCapabilityExpired, true
	}
	c.sendError(code, err.Error(), retry, requestID)
}
func (c *client) registryError(err error, requestID string) {
	code, retry := relayproto.ErrTunnelNotFound, true
	if errors.Is(err, errHostConflict) {
		code, retry = relayproto.ErrHostConflict, false
	}
	if errors.Is(err, errHostOffline) {
		code = relayproto.ErrHostOffline
	}
	if errors.Is(err, errTunnelLimit) || errors.Is(err, errPeerLimit) {
		code = relayproto.ErrRateLimited
	}
	c.sendError(code, err.Error(), retry, requestID)
}

func (c *client) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.conn.Close()
		c.server.clientsMu.Lock()
		delete(c.server.clients, c)
		c.server.clientsMu.Unlock()
		host, peers := c.server.registry.remove(c)
		if host != nil {
			host.sendPayload(relayproto.Header{Type: relayproto.TypePeerClosed, TunnelID: c.tunnelID, PeerID: c.peerID}, map[string]any{"peerId": c.peerID}, true, false)
		}
		for _, peer := range peers {
			peer.sendError(relayproto.ErrHostOffline, "host disconnected", true, "")
			peer.close()
		}
	})
}

func randomID(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeRelayError(w http.ResponseWriter, status int, err relayproto.RelayError) {
	writeJSON(w, status, err)
}
