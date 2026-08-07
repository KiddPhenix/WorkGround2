package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"workground2/internal/collab"
	"workground2/internal/config"
	"workground2/internal/relayproto"
)

type relayRPCResult struct {
	response relayRPCResponse
	err      error
}

type relayCollaborationPeer struct {
	socket   *collaborationRelaySocket
	tunnelID string
	peerID   string
	cipher   *relayCipher
	epoch    uint64
	room     string
	member   string
	session  string

	mu         sync.Mutex
	closeOnce  sync.Once
	pending    map[string]chan relayRPCResult
	closed     chan struct{}
	done       chan struct{}
	fileSource *desktopCollaboration
}

func openRelayCollaborationPeer(ctx context.Context, route CollaborationRouteInput, expectedHostKey, joinRef string) (*relayCollaborationPeer, string, error) {
	relay, err := relayConfigForRoute(route)
	if err != nil {
		return nil, "", err
	}
	socket, _, err := dialCollaborationRelay(ctx, relay)
	if err != nil {
		return nil, "", err
	}
	requestID := newCollaborationRequestID("relay-attach")
	if err := socket.write(relayproto.Header{Type: relayproto.TypeGuestAttach, RelayRequestID: requestID, TunnelID: route.TunnelID}, relayproto.GuestAttach{Capability: route.GuestCapability, JoinRef: joinRef}); err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	header, payload, err := waitRelayControl(socket, requestID, relayproto.TypePeerOpened)
	if err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	var opened relayproto.PeerOpened
	if err := relayproto.UnmarshalPayload(payload, &opened); err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	if opened.PeerID == "" {
		opened.PeerID = header.PeerID
	}
	if opened.TunnelID == "" {
		opened.TunnelID = route.TunnelID
	}
	private, public, nonce, err := newRelayEphemeral()
	if err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	hello := relayE2EHello{PublicKey: public, Nonce: nonce}
	handshakeID := newCollaborationRequestID("relay-e2e")
	if err := socket.write(relayproto.Header{Type: "e2e.hello", RelayRequestID: handshakeID, TunnelID: opened.TunnelID, PeerID: opened.PeerID}, hello); err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	_, acceptPayload, err := waitRelayControl(socket, handshakeID, "e2e.accept")
	if err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	var accept relayE2EAccept
	if err := json.Unmarshal(acceptPayload, &accept); err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	cipher, err := relayGuestAccept(private, opened.TunnelID, opened.PeerID, hello, accept, expectedHostKey)
	if err != nil {
		_ = socket.Close(context.Background())
		return nil, "", err
	}
	peer := &relayCollaborationPeer{socket: socket, tunnelID: opened.TunnelID, peerID: opened.PeerID, cipher: cipher, epoch: uint64(time.Now().UnixNano()), pending: map[string]chan relayRPCResult{}, closed: make(chan struct{}), done: make(chan struct{})}
	go peer.readLoop()
	return peer, opened.GuestCapability, nil
}

func relayConfigForRoute(route CollaborationRouteInput) (config.RelayConfig, error) {
	if strings.TrimSpace(route.URL) != "" {
		relay, err := collaborationRelayByURL(route.URL)
		if err == nil {
			return relay, nil
		}
		// An invitation may carry an unconfigured WSS route. Plaintext routes
		// still require an explicit trusted Settings entry for the same URL.
		return config.RelayConfig{ID: route.RelayID, Name: route.RelayID, URL: route.URL, Enabled: true}, nil
	}
	if strings.TrimSpace(route.RelayID) != "" {
		// Legacy persisted routes may omit the URL. ID lookup remains only as a
		// compatibility path; IDs are local labels and are not cross-client trust keys.
		return collaborationRelayByID(route.RelayID)
	}
	return config.RelayConfig{}, fmt.Errorf("Relay route URL is required")
}

func (p *relayCollaborationPeer) readLoop() {
	defer close(p.done)
	for {
		header, payload, err := p.socket.read()
		if err != nil {
			p.failPending(err)
			return
		}
		switch header.Type {
		case "rpc.response":
			plaintext, openErr := p.cipher.open(header, payload)
			if openErr != nil {
				continue
			}
			var response relayRPCResponse
			if err := json.Unmarshal(plaintext, &response); err != nil {
				continue
			}
			p.mu.Lock()
			waiter := p.pending[header.RelayRequestID]
			delete(p.pending, header.RelayRequestID)
			p.mu.Unlock()
			if waiter != nil {
				waiter <- relayRPCResult{response: response}
			}
		case "rpc.request":
			go p.handleHostRPC(header, payload)
		case relayproto.TypePing:
			_ = p.socket.write(relayproto.Header{Type: relayproto.TypePong, RelayRequestID: header.RelayRequestID, TunnelID: p.tunnelID}, map[string]any{"time": time.Now().UnixMilli()})
		case relayproto.TypePeerClosed:
			p.failPending(&collaborationTransportError{message: "Relay Host route closed", retryable: true})
			return
		}
	}
}

func (p *relayCollaborationPeer) failPending(err error) {
	p.mu.Lock()
	pending := p.pending
	p.pending = map[string]chan relayRPCResult{}
	p.mu.Unlock()
	for _, waiter := range pending {
		waiter <- relayRPCResult{err: err}
	}
}

func (p *relayCollaborationPeer) call(ctx context.Context, method string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := json.Marshal(relayRPCRequest{Method: method, Body: body})
	if err != nil {
		return err
	}
	requestID := newCollaborationRequestID("relay-rpc")
	header := relayproto.Header{Version: relayproto.Version, Type: "rpc.request", RelayRequestID: requestID, TunnelID: p.tunnelID, PeerID: p.peerID, Epoch: p.epoch, Flags: []string{"encrypted"}}
	header.Sequence = p.socket.seq.Add(1)
	encrypted, err := p.cipher.seal(header, request)
	if err != nil {
		return err
	}
	waiter := make(chan relayRPCResult, 1)
	p.mu.Lock()
	p.pending[requestID] = waiter
	p.mu.Unlock()
	if err := p.socket.writeBytes(header, encrypted); err != nil {
		p.mu.Lock()
		delete(p.pending, requestID)
		p.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		p.mu.Lock()
		delete(p.pending, requestID)
		p.mu.Unlock()
		return ctx.Err()
	case result := <-waiter:
		if result.err != nil {
			return result.err
		}
		if result.response.Error != nil {
			return &collab.Error{Code: collab.ErrorCode(result.response.Error.Code), Message: result.response.Error.Message, Retryable: result.response.Error.Retryable}
		}
		if output == nil {
			return nil
		}
		if err := json.Unmarshal(result.response.Body, output); err != nil {
			return &collaborationTransportError{message: "decode Relay RPC response: " + err.Error(), retryable: true}
		}
		return nil
	}
}

func (p *relayCollaborationPeer) Snapshot(ctx context.Context) (collab.Snapshot, error) {
	var value collab.Snapshot
	err := p.call(ctx, "collab.snapshot", struct{ Room, Session string }{p.room, p.session}, &value)
	return value, err
}

func (p *relayCollaborationPeer) Events(ctx context.Context, after uint64) ([]collab.RoomEvent, error) {
	var value []collab.RoomEvent
	err := p.call(ctx, "collab.events", struct {
		Room, Session string
		After         uint64 `json:"afterSequence"`
	}{p.room, p.session, after}, &value)
	return value, err
}

func (p *relayCollaborationPeer) Stream(ctx context.Context, after uint64, handle func(collab.RoomEvent) error) error {
	last := after
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	for {
		events, err := p.Events(ctx, last)
		if err != nil {
			return err
		}
		for _, event := range events {
			if handle != nil {
				if err := handle(event); err != nil {
					return err
				}
			}
			last = event.Sequence
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *relayCollaborationPeer) Submit(ctx context.Context, env collab.CommandEnvelope) (collab.CommandReceipt, error) {
	env.Room, env.MemberID, env.Session = p.room, p.member, p.session
	var value collab.CommandReceipt
	err := p.call(ctx, "collab.submit", env, &value)
	return value, err
}

func (p *relayCollaborationPeer) Heartbeat(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	return p.call(ctx, "collab.heartbeat", collab.SessionInput{RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session}, &value)
}

func (p *relayCollaborationPeer) Leave(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	return p.call(ctx, "collab.leave", collab.SessionInput{RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session}, &value)
}

func (p *relayCollaborationPeer) Close(ctx context.Context) error {
	p.closeOnce.Do(func() { close(p.closed) })
	err := p.socket.Close(ctx)
	select {
	case <-p.done:
	case <-ctx.Done():
	}
	return err
}

func joinRelayCollaborationPeer(ctx context.Context, route CollaborationRouteInput, room, token, hostKey, joinRef string, identity collab.MemberDescriptor, resume string) (*relayCollaborationPeer, collab.JoinResult, collab.Snapshot, string, error) {
	peer, issuedGuestCap, err := openRelayCollaborationPeer(ctx, route, hostKey, joinRef)
	if err != nil {
		return nil, collab.JoinResult{}, collab.Snapshot{}, "", fmt.Errorf("attach Relay route: %w", err)
	}
	var joined collab.JoinResult
	err = peer.call(ctx, "collab.join", collab.JoinInput{RequestID: newCollaborationRequestID("join"), Room: room, Token: token, Member: identity, ResumeSession: strings.TrimSpace(resume)}, &joined)
	if err != nil {
		_ = peer.Close(context.Background())
		return nil, collab.JoinResult{}, collab.Snapshot{}, "", fmt.Errorf("join Room through Relay: %w", err)
	}
	peer.room, peer.member, peer.session = room, joined.Member.ID, joined.ConnectionSession
	snapshot, err := peer.Snapshot(ctx)
	if err != nil {
		_ = peer.Close(context.Background())
		return nil, collab.JoinResult{}, collab.Snapshot{}, "", fmt.Errorf("load Room snapshot through Relay: %w", err)
	}
	return peer, joined, snapshot, issuedGuestCap, nil
}

var _ collaborationPeer = (*relayCollaborationPeer)(nil)
var _ collaborationRelayBinding = (*relayCollaborationPeer)(nil)
