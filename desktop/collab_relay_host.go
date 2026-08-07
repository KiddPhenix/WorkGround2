package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"workground2/internal/collab"
	"workground2/internal/config"
	"workground2/internal/relayproto"
)

type relayRPCRequest struct {
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body,omitempty"`
}

type relayRPCResponse struct {
	Body  json.RawMessage `json:"body,omitempty"`
	Error *relayRPCError  `json:"error,omitempty"`
}

type relayRPCError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

type collaborationRelayHost struct {
	runtime      *desktopCollaboration
	roomConn     *collaborationConnection
	relay        config.RelayConfig
	socket       *collaborationRelaySocket
	tunnelID     string
	identity     ed25519.PrivateKey
	publicRoomID string

	mu       sync.RWMutex
	sessions map[string]*relayHostSession
	pending  map[string]chan relayRPCResult
	done     chan struct{}
}

type relayHostSession struct {
	cipher   *relayCipher
	memberID string
}

func (c *desktopCollaboration) openRelayHost(ctx context.Context, conn *collaborationConnection, relayID string, input HostCollaborationRoomInput) (collaborationRelayBinding, CollaborationRouteState, error) {
	relay, err := collaborationRelayByID(relayID)
	route := CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "relay:" + relayID, Kind: "relay", RelayID: relayID}, Status: "connecting"}
	if err != nil {
		route.Status, route.LastError, route.Retryable = "failed", err.Error(), false
		return nil, route, err
	}
	route.URL, route.Priority = relay.URL, relay.Priority
	identity, keyRef, err := c.loadRoomAuthorityKey(conn.room)
	if err != nil {
		route.Status, route.LastError, route.Retryable = "failed", err.Error(), true
		return nil, route, err
	}
	conn.authorityKeyRef = keyRef
	conn.hostKey = base64.RawURLEncoding.EncodeToString(identity.Public().(ed25519.PublicKey))

	socket, _, err := dialCollaborationRelay(ctx, relay)
	if err != nil {
		route.Status, route.LastError, route.Retryable = "failed", err.Error(), collaborationErrorRetryable(err)
		return nil, route, err
	}
	hostRef := collaborationRelayCapabilityRef(conn.room, relay.ID, "host")
	guestRef := collaborationRelayCapabilityRef(conn.room, relay.ID, "guest")
	hostCapability := c.getSecret(hostRef)
	requestID := newCollaborationRequestID("relay-bind")
	if hostCapability == "" {
		err = socket.write(relayproto.Header{Type: relayproto.TypeTunnelCreate, RelayRequestID: requestID}, relayproto.TunnelCreate{})
	} else {
		err = socket.write(relayproto.Header{Type: relayproto.TypeHostBind, RelayRequestID: requestID}, relayproto.HostBind{Capability: hostCapability})
	}
	if err != nil {
		_ = socket.Close(context.Background())
		route.Status, route.LastError, route.Retryable = "failed", err.Error(), true
		return nil, route, err
	}
	header, payload, err := waitRelayControl(socket, requestID, relayproto.TypeHostBound)
	if err != nil {
		_ = socket.Close(context.Background())
		route.Status, route.LastError, route.Retryable = "failed", err.Error(), collaborationErrorRetryable(err)
		return nil, route, err
	}
	var bound relayproto.HostBound
	if err := relayproto.UnmarshalPayload(payload, &bound); err != nil {
		_ = socket.Close(context.Background())
		return nil, route, err
	}
	if bound.TunnelID == "" {
		bound.TunnelID = header.TunnelID
	}
	if bound.HostCapability != "" {
		if err := c.setSecret(hostRef, bound.HostCapability); err != nil {
			_ = socket.Close(context.Background())
			return nil, route, err
		}
	}
	if bound.GuestCapability != "" {
		if err := c.setSecret(guestRef, bound.GuestCapability); err != nil {
			_ = socket.Close(context.Background())
			return nil, route, err
		}
	}
	conn.hostCapabilityRefs[relay.ID] = hostRef
	conn.guestCapabilityRefs[relay.ID] = guestRef
	route.TunnelID, route.Status = bound.TunnelID, "connected"
	publicKey := identity.Public().(ed25519.PublicKey)
	publicRoomID := stableCollaborationID("room-public", base64.RawURLEncoding.EncodeToString(publicKey)+"\x00"+conn.room)
	host := &collaborationRelayHost{runtime: c, roomConn: conn, relay: relay, socket: socket, tunnelID: bound.TunnelID, identity: identity, publicRoomID: publicRoomID, sessions: map[string]*relayHostSession{}, pending: map[string]chan relayRPCResult{}, done: make(chan struct{})}
	relayFiles := &relayHostFilePeer{host: host, room: conn.room, member: conn.memberID, session: conn.connectionSession}
	if conn.filePeer == nil {
		conn.filePeer = relayFiles
	} else {
		conn.filePeer = &fallbackCollaborationFilePeer{primary: conn.filePeer, fallback: relayFiles}
	}

	visibility := normalizeRoomVisibility(input.Visibility)
	if conn.advertisement == nil {
		conn.advertisement = &CollaborationAdvertisementState{Visibility: visibility, Revision: 1}
	}
	if visibility != "private" && relay.Discovery {
		state, publishErr := host.publishAdvertisement(input)
		conn.advertisement.Relays = append(conn.advertisement.Relays, state)
		if publishErr != nil {
			route.LastError, route.Retryable = publishErr.Error(), true
		}
	} else {
		conn.advertisement.Relays = append(conn.advertisement.Relays, CollaborationAdvertisementRelayState{RelayID: relay.ID, Status: "disabled"})
	}
	go host.readLoop()
	if visibility != "private" && relay.Discovery {
		go host.advertisementLoop(input)
	}
	return host, route, nil
}

func normalizeRoomVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "public", "unlisted":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "private"
	}
}

func (c *desktopCollaboration) loadRoomAuthorityKey(room string) (ed25519.PrivateKey, string, error) {
	ref := collaborationRelayAuthorityRef(room)
	if encoded := c.getSecret(ref); encoded != "" {
		data, err := base64.RawURLEncoding.DecodeString(encoded)
		if err == nil && len(data) == ed25519.PrivateKeySize {
			return ed25519.PrivateKey(data), ref, nil
		}
	}
	_, private, err := newRelayHostKey()
	if err != nil {
		return nil, ref, err
	}
	if err := c.setSecret(ref, base64.RawURLEncoding.EncodeToString(private)); err != nil {
		return nil, ref, fmt.Errorf("save Room authority key: %w", err)
	}
	return private, ref, nil
}

func (c *desktopCollaboration) prepareRoomAuthority(conn *collaborationConnection) error {
	identity, keyRef, err := c.loadRoomAuthorityKey(conn.room)
	if err != nil {
		return err
	}
	conn.authorityKeyRef = keyRef
	conn.hostKey = base64.RawURLEncoding.EncodeToString(identity.Public().(ed25519.PublicKey))
	return nil
}

func collaborationRelayAuthorityRef(room string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(room)))
	return "WORKGROUND2_COLLAB_AUTHORITY_" + strings.ToUpper(hex.EncodeToString(sum[:12]))
}

func collaborationRelayCapabilityRef(room, relayID, role string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(room) + "\x00" + strings.ToLower(strings.TrimSpace(relayID)) + "\x00" + role))
	return "WORKGROUND2_COLLAB_RELAY_" + strings.ToUpper(hex.EncodeToString(sum[:12])) + "_" + strings.ToUpper(role)
}

func (h *collaborationRelayHost) publishAdvertisement(input HostCollaborationRoomInput) (CollaborationAdvertisementRelayState, error) {
	state := CollaborationAdvertisementRelayState{RelayID: h.relay.ID, Status: "pending"}
	ad, err := h.buildAdvertisement(input)
	if err != nil {
		state.Status, state.LastError = "failed", err.Error()
		return state, err
	}
	requestID := stableCollaborationID("relay-ad", ad.PublicRoomID+"\x00"+h.relay.ID+"\x00"+time.Now().UTC().Format("200601021504"))
	if err := h.socket.write(relayproto.Header{Type: relayproto.TypeAdvertisementUpsert, RelayRequestID: requestID, TunnelID: h.tunnelID}, relayproto.AdvertisementUpsert{Advertisement: ad}); err != nil {
		state.Status, state.LastError, state.Retryable = "failed", err.Error(), true
		return state, err
	}
	_, payload, err := waitRelayControl(h.socket, requestID, relayproto.TypeAdvertisementUpsert)
	if err != nil {
		state.Status, state.LastError, state.Retryable = "failed", err.Error(), collaborationErrorRetryable(err)
		return state, err
	}
	_ = payload
	state.Status = "published"
	return state, nil
}

func (h *collaborationRelayHost) buildAdvertisement(input HostCollaborationRoomInput) (relayproto.Advertisement, error) {
	adInput := input.Advertisement
	if adInput == nil {
		adInput = &RoomAdvertisementInput{Name: input.RoomName, Description: input.Description}
	}
	public := h.identity.Public().(ed25519.PublicKey)
	publicRoomID := h.publicRoomID
	ad := relayproto.Advertisement{
		PublicRoomID: publicRoomID, Name: strings.TrimSpace(adInput.Name), Description: strings.TrimSpace(adInput.Description), Tags: append([]string(nil), adInput.Tags...),
		Visibility: normalizeRoomVisibility(input.Visibility), RequiresToken: h.roomConn.joinToken != "", Capacity: adInput.Capacity,
		HostPublicKey: base64.RawStdEncoding.EncodeToString(public), HostKeyFingerprint: relayHostKeyFingerprint(public), AdvertisementRevision: 1, ExpiresAt: time.Now().UTC().Add(110 * time.Second),
	}
	if adInput.ShowOnlineCount {
		ad.OnlineCount = len(h.roomConn.initialSnapshot.Members)
	}
	encoded, err := relayproto.AdvertisementSigningBytes(ad)
	if err != nil {
		return relayproto.Advertisement{}, err
	}
	ad.Signature = base64.RawStdEncoding.EncodeToString(ed25519.Sign(h.identity, encoded))
	return ad, nil
}

func (h *collaborationRelayHost) advertisementLoop(input HostCollaborationRoomInput) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-ticker.C:
			ad, err := h.buildAdvertisement(input)
			if err != nil {
				continue
			}
			requestID := stableCollaborationID("relay-ad-refresh", ad.PublicRoomID+"\x00"+h.relay.ID+"\x00"+time.Now().UTC().Format(time.RFC3339))
			if err := h.socket.write(relayproto.Header{Type: relayproto.TypeAdvertisementUpsert, RelayRequestID: requestID, TunnelID: h.tunnelID}, relayproto.AdvertisementUpsert{Advertisement: ad}); err != nil {
				h.runtime.relayRouteFailed(h.roomConn, h.relay.ID, err)
				return
			}
		}
	}
}

func (h *collaborationRelayHost) readLoop() {
	defer close(h.done)
	for {
		header, payload, err := h.socket.read()
		if err != nil {
			h.runtime.relayRouteFailed(h.roomConn, h.relay.ID, err)
			return
		}
		switch header.Type {
		case relayproto.TypePeerClosed:
			h.mu.Lock()
			delete(h.sessions, header.PeerID)
			h.mu.Unlock()
		case "e2e.hello":
			go h.acceptPeer(header, payload)
		case "rpc.request":
			go h.handleRPC(header, payload)
		case "rpc.response":
			h.handlePeerRPCResponse(header, payload)
		case relayproto.TypePing:
			_ = h.socket.write(relayproto.Header{Type: relayproto.TypePong, RelayRequestID: header.RelayRequestID, TunnelID: h.tunnelID}, map[string]any{"time": time.Now().UnixMilli()})
		}
	}
}

func (h *collaborationRelayHost) acceptPeer(header relayproto.Header, payload []byte) {
	var hello relayE2EHello
	if json.Unmarshal(payload, &hello) != nil {
		return
	}
	accept, cipher, err := relayHostAccept(h.tunnelID, header.PeerID, hello, h.identity)
	if err != nil {
		return
	}
	h.mu.Lock()
	h.sessions[header.PeerID] = &relayHostSession{cipher: cipher}
	h.mu.Unlock()
	_ = h.socket.write(relayproto.Header{Type: "e2e.accept", RelayRequestID: header.RelayRequestID, TunnelID: h.tunnelID, PeerID: header.PeerID}, accept)
}

func (h *collaborationRelayHost) handleRPC(header relayproto.Header, payload []byte) {
	h.mu.RLock()
	session := h.sessions[header.PeerID]
	h.mu.RUnlock()
	if session == nil || session.cipher == nil {
		return
	}
	plaintext, err := session.cipher.open(header, payload)
	if err != nil {
		return
	}
	var request relayRPCRequest
	if err := json.Unmarshal(plaintext, &request); err != nil {
		return
	}
	body, rpcErr := h.dispatchRPC(context.Background(), header.PeerID, request)
	response := relayRPCResponse{Body: body, Error: rpcErr}
	encoded, _ := json.Marshal(response)
	responseHeader := relayproto.Header{Version: relayproto.Version, Type: "rpc.response", RelayRequestID: header.RelayRequestID, TunnelID: h.tunnelID, PeerID: header.PeerID, Epoch: header.Epoch, Flags: []string{"encrypted"}}
	responseHeader.Sequence = h.socket.seq.Add(1)
	encrypted, err := session.cipher.seal(responseHeader, encoded)
	if err == nil {
		_ = h.socket.writeBytes(responseHeader, encrypted)
	}
}

func (h *collaborationRelayHost) dispatchRPC(ctx context.Context, peerID string, request relayRPCRequest) (json.RawMessage, *relayRPCError) {
	service := h.roomConn.authority.service
	encode := func(value any, err error) (json.RawMessage, *relayRPCError) {
		if err != nil {
			return nil, toRelayRPCError(err)
		}
		data, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return nil, toRelayRPCError(marshalErr)
		}
		return data, nil
	}
	switch request.Method {
	case "collab.join":
		var input collab.JoinInput
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		result, err := service.Join(ctx, input)
		if err == nil {
			h.mu.Lock()
			if session := h.sessions[peerID]; session != nil {
				session.memberID = result.Member.ID
			}
			h.mu.Unlock()
		}
		return encode(result, err)
	case "collab.snapshot":
		var input struct{ Room, Session string }
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.Snapshot(ctx, input.Room, input.Session)
		return encode(value, err)
	case "collab.snapshot_manifest":
		var input struct{ Room, Session string }
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.SnapshotManifest(ctx, input.Room, input.Session)
		return encode(value, err)
	case "collab.snapshot_chunk":
		var input struct {
			Room, Session, SnapshotID string
			Index                     int
		}
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.SnapshotChunk(ctx, input.Room, input.Session, input.SnapshotID, input.Index)
		return encode(value, err)
	case "collab.events":
		var input struct {
			Room, Session string
			After         uint64 `json:"afterSequence"`
		}
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.Events(ctx, input.Room, input.Session, input.After)
		return encode(value, err)
	case "collab.submit":
		var input collab.CommandEnvelope
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.Submit(ctx, input)
		return encode(value, err)
	case "collab.heartbeat":
		var input collab.SessionInput
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.Heartbeat(ctx, input)
		return encode(value, err)
	case "collab.leave":
		var input collab.SessionInput
		if err := json.Unmarshal(request.Body, &input); err != nil {
			return nil, toRelayRPCError(err)
		}
		input.Room = h.authorityRoom(input.Room)
		value, err := service.Leave(ctx, input)
		return encode(value, err)
	case "file.manifest", "file.segment":
		return h.dispatchRelayFile(ctx, peerID, request)
	default:
		return nil, &relayRPCError{Code: "invalid", Message: "unsupported Relay RPC method " + request.Method}
	}
}

func (h *collaborationRelayHost) authorityRoom(room string) string {
	if room == h.publicRoomID {
		return h.roomConn.room
	}
	return room
}

func toRelayRPCError(err error) *relayRPCError {
	var protocol *collab.Error
	if errors.As(err, &protocol) {
		return &relayRPCError{Code: string(protocol.Code), Message: protocol.Message, Retryable: protocol.Retryable}
	}
	return &relayRPCError{Code: "internal", Message: err.Error(), Retryable: true}
}

func (h *collaborationRelayHost) Close(ctx context.Context) error {
	_ = h.socket.write(relayproto.Header{Type: relayproto.TypeAdvertisementRevoke, RelayRequestID: newCollaborationRequestID("relay-ad-revoke"), TunnelID: h.tunnelID}, relayproto.AdvertisementRevoke{PublicRoomID: h.publicRoomID, Revision: 1})
	err := h.socket.Close(ctx)
	select {
	case <-h.done:
	case <-ctx.Done():
	}
	return err
}

func (c *desktopCollaboration) relayRouteFailed(conn *collaborationConnection, relayID string, err error) {
	c.mu.Lock()
	if c.conn == conn {
		for i := range conn.routes {
			if conn.routes[i].RelayID == relayID {
				conn.routes[i].Status = "failed"
				conn.routes[i].LastError = err.Error()
				conn.routes[i].Retryable = true
			}
		}
		for i := range c.state.Routes {
			if c.state.Routes[i].RelayID == relayID {
				c.state.Routes[i].Status = "failed"
				c.state.Routes[i].LastError = err.Error()
				c.state.Routes[i].Retryable = true
			}
		}
		c.persistLocked()
	}
	c.mu.Unlock()
	if c.app != nil {
		c.emitState()
	}
}
