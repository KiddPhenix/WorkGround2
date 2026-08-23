package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/collab"
	"workground2/internal/config"
)

const (
	collaborationHeartbeatInterval = 95 * time.Second
	collaborationMemberStaleAfter  = 4 * collaborationHeartbeatInterval
	// A stream can end briefly without invalidating its connection session.
	// Give the active peer several in-place stream retries before considering a
	// different configured route; a route switch performs a remote Join.
	collaborationRouteFailoverAttempts = 3
)

func (c *desktopCollaboration) openHostedRoom(ctx context.Context, input HostCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
	listenHost := strings.TrimSpace(input.ListenHost)
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	if input.Port < 0 || input.Port > 65535 {
		return nil, fmt.Errorf("port must be between 0 and 65535")
	}
	room := strings.TrimSpace(input.Room)
	roomName := strings.TrimSpace(input.RoomName)
	if roomName == "" {
		roomName = room
	}
	protocolVersion := input.ProtocolVersion
	if protocolVersion == 0 {
		protocolVersion = collaborationProtocolV1
	}
	lanEnabled := input.LANEnabled == nil || *input.LANEnabled
	var listener net.Listener
	var server *http.Server
	var releaseLAN func()
	var err error
	actualPort := 0
	routes := make([]CollaborationRouteState, 0, 1+len(input.RelayIDs))
	if c.app == nil {
		return nil, fmt.Errorf("collaboration authority owner is unavailable")
	}
	authority, err := c.app.openCollaborationAuthority(ctx, input)
	if err != nil {
		return nil, err
	}
	if lanEnabled && protocolVersion == collaborationProtocolV2 {
		owner := collaborationPersistenceKey(c.ownerSessionID, c.ownerSessionPath)
		actualPort, releaseLAN, err = c.app.sharedCollaborationLAN().register(input, authority, owner)
		if err != nil {
			routes = append(routes, CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: listenHost, Port: input.Port, ProtocolVersion: protocolVersion}, Status: "failed", LastError: err.Error(), Retryable: true})
		} else {
			routes = append(routes, CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: listenHost, Port: actualPort, Priority: 1000, ProtocolVersion: protocolVersion}, Status: "connected"})
		}
	} else if lanEnabled {
		listener, err = net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(input.Port)))
		if err != nil {
			routes = append(routes, CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: listenHost, Port: input.Port, ProtocolVersion: collaborationProtocolV1}, Status: "failed", LastError: err.Error(), Retryable: true})
		} else {
			actualPort = listener.Addr().(*net.TCPAddr).Port
			server = &http.Server{Handler: collab.NewHandler(authority.service, authority.hub), ReadHeaderTimeout: 10 * time.Second}
			go func() {
				if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
					c.failState("failed", fmt.Errorf("collaboration host stopped: %w", serveErr), true)
				}
			}()
			routes = append(routes, CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: listenHost, Port: actualPort, Priority: 1000, ProtocolVersion: collaborationProtocolV1}, Status: "connected"})
		}
	} else {
		routes = append(routes, CollaborationRouteState{CollaborationRouteInput: CollaborationRouteInput{ID: "lan", Kind: "lan", Host: listenHost, Port: input.Port, ProtocolVersion: protocolVersion}, Status: "disabled"})
	}
	service, hub := authority.service, authority.hub
	joinInput := collab.JoinInput{
		RequestID: newCollaborationRequestID("join"), Room: room, Token: strings.TrimSpace(input.Token), Member: identity, ResumeSession: strings.TrimSpace(resume),
	}
	joined, err := service.Join(ctx, joinInput)
	if err != nil && collaborationMemberResumeRequired(err) {
		joined, err = service.RecoverHostMember(ctx, joinInput)
		if err == nil {
			slog.Warn("desktop: recovered stale collaboration Host session", "room", room, "member", identity.ID)
		}
	}
	if err != nil {
		if server != nil {
			_ = server.Shutdown(context.Background())
		}
		if releaseLAN != nil {
			releaseLAN()
		}
		return nil, err
	}
	peer := &serviceCollaborationPeer{service: service, hub: hub, room: room, member: joined.Member.ID, session: joined.ConnectionSession}
	httpHost := listenHost
	if ip := net.ParseIP(strings.Split(strings.Trim(httpHost, "[]"), "%")[0]); ip != nil && ip.IsUnspecified() {
		httpHost = "127.0.0.1"
	}
	var filePeer collaborationFilePeer
	if actualPort > 0 {
		filePeer = &httpCollaborationPeer{baseURL: "http://" + net.JoinHostPort(httpHost, strconv.Itoa(actualPort)), client: &http.Client{Timeout: 15 * time.Second}, streamClient: &http.Client{}, room: room, member: joined.Member.ID, session: joined.ConnectionSession, protocolVersion: protocolVersion}
	}
	snapshot, err := fetchCollaborationSnapshot(ctx, peer)
	if err != nil {
		if server != nil {
			_ = server.Shutdown(context.Background())
		}
		if releaseLAN != nil {
			releaseLAN()
		}
		return nil, err
	}
	conn := &collaborationConnection{
		peer: peer, filePeer: filePeer, host: server, listener: listener, mode: "host", roomName: roomName, description: strings.TrimSpace(input.Description), hostName: listenHost,
		port: actualPort, room: room, memberID: joined.Member.ID, agentID: joined.Member.Agent.ID,
		memberName: identity.Name, memberAvatar: identity.Avatar, memberRole: identity.Role, agentName: identity.Agent.Name, agentAvatar: identity.Agent.Avatar, agentRole: identity.Agent.Role,
		sessionID: strings.TrimSpace(input.SessionID), connectionSession: joined.ConnectionSession,
		initialSnapshot: snapshot, joinToken: strings.TrimSpace(input.Token), rejoined: joined.Rejoined,
		authority: authority, routes: routes, lanEnabled: lanEnabled, relayIDs: append([]string(nil), input.RelayIDs...), protocolVersion: protocolVersion, releaseLAN: releaseLAN,
		preferLAN:          input.PreferLAN == nil || *input.PreferLAN,
		hostCapabilityRefs: map[string]string{}, guestCapabilityRefs: map[string]string{},
		sweep: func(sweepCtx context.Context) error {
			_, sweepErr := service.SweepStale(sweepCtx, collab.SweepInput{
				RequestID: newCollaborationRequestID("sweep"), Room: room, Before: time.Now().UTC().Add(-collaborationMemberStaleAfter),
			})
			return sweepErr
		},
	}
	if protocolVersion == collaborationProtocolV2 {
		if err := c.prepareRoomAuthority(conn); err != nil {
			_ = conn.close(context.Background(), false)
			return nil, err
		}
	}
	if err := c.startRelayBindings(ctx, conn, input); err != nil && !lanEnabled {
		// Authority and local Host stay available; individual Relay failures are
		// projected through route state and remain retryable.
		conn.routeError = err.Error()
	}
	return conn, nil
}

func (c *desktopCollaboration) openJoinedRoom(ctx context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
	host := strings.TrimSpace(input.Host)
	room := strings.TrimSpace(input.Room)
	candidates := append([]CollaborationRouteInput(nil), input.Routes...)
	if len(candidates) == 0 && host != "" && input.Port > 0 && input.Port <= 65535 {
		candidates = append([]CollaborationRouteInput{{ID: "lan", Kind: "lan", Host: host, Port: input.Port, Priority: 1000}}, candidates...)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("a LAN endpoint or Relay route is required")
	}
	preferLAN := true
	if cfg, loadErr := config.Load(); loadErr == nil {
		preferLAN = cfg.Collaboration.PreferLAN
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		score := func(route CollaborationRouteInput) int {
			if route.Priority < 0 {
				return -1000
			}
			if strings.EqualFold(route.Kind, "lan") {
				if preferLAN {
					return 10000
				}
				return 0
			}
			return route.Priority
		}
		return score(candidates[i]) > score(candidates[j])
	})
	var routeStates []CollaborationRouteState
	var failures []string
	var causes []error
	for index, route := range candidates {
		if route.ID == "" {
			route.ID = route.Kind + ":" + route.RelayID
		}
		state := CollaborationRouteState{CollaborationRouteInput: route, Status: "connecting"}
		if strings.EqualFold(route.Kind, "lan") {
			protocolVersion := route.ProtocolVersion
			if protocolVersion == 0 {
				protocolVersion = collaborationProtocolV1
			}
			peer, joined, snapshot, err := joinCollaborationPeer(ctx, strings.TrimSpace(route.Host), route.Port, room, strings.TrimSpace(input.Token), identity, resume, protocolVersion)
			if err != nil {
				state.Status, state.LastError, state.Retryable = "failed", err.Error(), collaborationErrorRetryable(err)
				routeStates = append(routeStates, state)
				failures = append(failures, route.ID+": "+err.Error())
				causes = append(causes, err)
				continue
			}
			state.Status, state.Active = "connected", true
			routeStates = append(routeStates, state)
			return &collaborationConnection{
				peer: peer, filePeer: peer, mode: "client", hostName: route.Host, port: route.Port, room: room,
				memberID: joined.Member.ID, agentID: joined.Member.Agent.ID,
				memberName: identity.Name, memberAvatar: identity.Avatar, memberRole: identity.Role, agentName: identity.Agent.Name, agentAvatar: identity.Agent.Avatar, agentRole: identity.Agent.Role,
				sessionID: strings.TrimSpace(input.SessionID), connectionSession: joined.ConnectionSession,
				initialSnapshot: snapshot, joinToken: strings.TrimSpace(input.Token), rejoined: joined.Rejoined, routes: appendRemainingRouteStates(routeStates, candidates[index+1:]), lanEnabled: true, protocolVersion: protocolVersion, hostKey: strings.TrimSpace(input.HostKey),
			}, nil
		}
		if !strings.EqualFold(route.Kind, "relay") {
			state.Status, state.LastError = "failed", "unsupported collaboration route kind"
			routeStates = append(routeStates, state)
			continue
		}
		joinRef := strings.TrimSpace(input.JoinRef)
		if joinRef == "" && strings.TrimSpace(route.GuestCapability) == "" {
			if relay, relayErr := relayConfigForRoute(route); relayErr == nil && relay.Discovery {
				var tunnelID string
				joinRef, tunnelID, relayErr = fetchRelayJoinRef(ctx, relay, room)
				if relayErr != nil {
					state.Status, state.LastError, state.Retryable = "failed", relayErr.Error(), collaborationErrorRetryable(relayErr)
					routeStates = append(routeStates, state)
					failures = append(failures, route.ID+": "+relayErr.Error())
					causes = append(causes, relayErr)
					continue
				}
				if tunnelID != "" {
					route.TunnelID, state.TunnelID = tunnelID, tunnelID
				}
			}
		}
		peer, joined, snapshot, issuedGuestCap, err := joinRelayCollaborationPeer(ctx, route, room, strings.TrimSpace(input.Token), strings.TrimSpace(input.HostKey), joinRef, identity, resume)
		if err != nil {
			state.Status, state.LastError, state.Retryable = "failed", err.Error(), collaborationErrorRetryable(err)
			routeStates = append(routeStates, state)
			failures = append(failures, route.ID+": "+err.Error())
			causes = append(causes, err)
			continue
		}
		state.Status, state.Active = "connected", true
		peer.fileSource = c
		routeStates = append(routeStates, state)
		guestRefs := map[string]string{}
		guestCap := issuedGuestCap
		if guestCap == "" {
			guestCap = route.GuestCapability
		}
		if guestCap != "" && route.RelayID != "" {
			ref := collaborationRelayCapabilityRef(room, route.RelayID, "guest")
			if err := c.setSecret(ref, guestCap); err != nil {
				_ = peer.Close(context.Background())
				return nil, fmt.Errorf("save Relay guest capability: %w", err)
			}
			guestRefs[route.RelayID] = ref
		}
		return &collaborationConnection{
			peer: peer, filePeer: peer, mode: "client", hostName: route.URL, room: room,
			memberID: joined.Member.ID, agentID: joined.Member.Agent.ID,
			memberName: identity.Name, memberAvatar: identity.Avatar, memberRole: identity.Role, agentName: identity.Agent.Name, agentAvatar: identity.Agent.Avatar, agentRole: identity.Agent.Role,
			sessionID: strings.TrimSpace(input.SessionID), connectionSession: joined.ConnectionSession,
			initialSnapshot: snapshot, joinToken: strings.TrimSpace(input.Token), rejoined: joined.Rejoined, routes: appendRemainingRouteStates(routeStates, candidates[index+1:]), relayBindings: []collaborationRelayBinding{peer}, relayIDs: []string{route.RelayID}, hostKey: input.HostKey, guestCapabilityRefs: guestRefs,
		}, nil
	}
	return nil, &collaborationTransportError{message: "all collaboration routes failed: " + strings.Join(failures, "; "), retryable: true, causes: causes}
}

func appendRemainingRouteStates(states []CollaborationRouteState, routes []CollaborationRouteInput) []CollaborationRouteState {
	result := append([]CollaborationRouteState(nil), states...)
	for _, route := range routes {
		result = append(result, CollaborationRouteState{CollaborationRouteInput: route, Status: "disabled"})
	}
	return result
}

func publicCollaborationRoutes(routes []CollaborationRouteState) []CollaborationRouteState {
	result := append([]CollaborationRouteState(nil), routes...)
	for i := range result {
		result[i].GuestCapability = ""
	}
	return result
}

func (conn *collaborationConnection) close(ctx context.Context, sendLeave bool) error {
	var result error
	conn.closeOnce.Do(func() {
		if conn.cancel != nil {
			conn.cancel()
		}
		if sendLeave && conn.peer != nil {
			result = conn.peer.Leave(ctx, newCollaborationRequestID("leave"))
		}
		if conn.host != nil {
			if err := conn.host.Shutdown(ctx); err != nil && result == nil {
				result = err
			}
		}
		if conn.releaseLAN != nil {
			conn.releaseLAN()
		}
		for _, binding := range conn.relayBindings {
			if binding == nil {
				continue
			}
			if err := binding.Close(ctx); err != nil && result == nil {
				result = err
			}
		}
		if conn.done != nil {
			select {
			case <-conn.done:
			case <-ctx.Done():
			}
		}
	})
	return result
}

type httpCollaborationPeer struct {
	baseURL         string
	client          *http.Client
	streamClient    *http.Client
	room            string
	member          string
	session         string
	protocolVersion int
}

// serviceCollaborationPeer keeps the Host's own session on the authoritative
// in-process service. Remote members still use HTTP/SSE, while a transient
// loopback transport failure can no longer make a healthy local Host appear
// offline or divert its messages into the retry queue.
type serviceCollaborationPeer struct {
	service *collab.Service
	hub     *collab.Hub
	room    string
	member  string
	session string
}

func (p *serviceCollaborationPeer) Snapshot(ctx context.Context) (collab.Snapshot, error) {
	return p.service.Snapshot(ctx, p.room, p.session)
}

func (p *serviceCollaborationPeer) Events(ctx context.Context, after uint64) ([]collab.RoomEvent, error) {
	return p.service.Events(ctx, p.room, p.session, after)
}

func (p *serviceCollaborationPeer) Stream(ctx context.Context, after uint64, handle func(collab.RoomEvent) error) error {
	wake, unsubscribe, err := p.hub.TrySubscribe(p.room)
	if err != nil {
		return err
	}
	defer unsubscribe()
	last := after
	drain := func() error {
		events, eventsErr := p.Events(ctx, last)
		if eventsErr != nil {
			return eventsErr
		}
		for _, value := range events {
			if handle != nil {
				if handleErr := handle(value); handleErr != nil {
					return handleErr
				}
			}
			last = value.Sequence
		}
		return nil
	}
	if err := drain(); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-wake:
			if !ok {
				return &collaborationTransportError{message: "collaboration host stream closed", retryable: true}
			}
			if err := drain(); err != nil {
				return err
			}
		}
	}
}

func (p *serviceCollaborationPeer) Submit(ctx context.Context, env collab.CommandEnvelope) (collab.CommandReceipt, error) {
	env.Room, env.MemberID, env.Session = p.room, p.member, p.session
	return p.service.Submit(ctx, env)
}

func (p *serviceCollaborationPeer) Heartbeat(ctx context.Context, requestID string) error {
	_, err := p.service.Heartbeat(ctx, collab.SessionInput{RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session})
	return err
}

func (p *serviceCollaborationPeer) Leave(ctx context.Context, requestID string) error {
	_, err := p.service.Leave(ctx, collab.SessionInput{RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session})
	return err
}

func joinCollaborationPeer(ctx context.Context, host string, port int, room, token string, identity collab.MemberDescriptor, resume string, protocolVersion int) (*httpCollaborationPeer, collab.JoinResult, collab.Snapshot, error) {
	baseURL := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	peer := &httpCollaborationPeer{baseURL: baseURL, client: &http.Client{Timeout: 15 * time.Second}, streamClient: &http.Client{}, room: room, member: identity.ID, protocolVersion: protocolVersion}
	var joined collab.JoinResult
	joinPath := "/collab/v1/join"
	if protocolVersion >= collaborationProtocolV2 {
		joinPath = peer.roomPath("join")
	}
	err := peer.doJSON(ctx, http.MethodPost, joinPath, collab.JoinInput{
		RequestID:     newCollaborationRequestID("join"),
		Room:          room,
		Token:         token,
		Member:        identity,
		ResumeSession: strings.TrimSpace(resume),
	}, &joined, false)
	if err != nil {
		return nil, collab.JoinResult{}, collab.Snapshot{}, err
	}
	peer.session = joined.ConnectionSession
	snapshot, err := fetchCollaborationSnapshot(ctx, peer)
	if err != nil {
		return nil, collab.JoinResult{}, collab.Snapshot{}, err
	}
	return peer, joined, snapshot, nil
}

func (p *httpCollaborationPeer) Snapshot(ctx context.Context) (collab.Snapshot, error) {
	var value collab.Snapshot
	path := p.roomPath("snapshot")
	err := p.doJSON(ctx, http.MethodGet, path, nil, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) SnapshotManifest(ctx context.Context) (collab.SnapshotManifest, error) {
	var value collab.SnapshotManifest
	err := p.doJSON(ctx, http.MethodGet, p.roomPath("snapshot/manifest"), nil, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) SnapshotChunk(ctx context.Context, snapshotID string, index int) (collab.SnapshotChunk, error) {
	path := p.roomPath("snapshot/chunks/"+strconv.Itoa(index)) + "?snapshotId=" + url.QueryEscape(snapshotID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return collab.SnapshotChunk{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.session)
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := p.client.Do(req)
	if err != nil {
		return collab.SnapshotChunk{}, &collaborationTransportError{message: err.Error(), retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var value collab.Error
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&value); err == nil && value.Message != "" {
			return collab.SnapshotChunk{}, &value
		}
		return collab.SnapshotChunk{}, &collaborationTransportError{message: "collaboration host returned " + resp.Status, retryable: resp.StatusCode >= 500}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, collab.MaxSnapshotChunkBytes+1))
	if err != nil {
		return collab.SnapshotChunk{}, &collaborationTransportError{message: "read collaboration snapshot chunk: " + err.Error(), retryable: true}
	}
	if len(data) > collab.MaxSnapshotChunkBytes {
		return collab.SnapshotChunk{}, &collaborationTransportError{message: "collaboration snapshot chunk exceeds size limit", retryable: false}
	}
	returnedID := resp.Header.Get("X-Collab-Snapshot-ID")
	if returnedID == "" {
		returnedID = snapshotID
	}
	returnedIndex := index
	if raw := resp.Header.Get("X-Collab-Chunk-Index"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil {
			return collab.SnapshotChunk{}, &collaborationTransportError{message: "invalid collaboration snapshot chunk index header", retryable: true}
		}
		returnedIndex = parsed
	}
	return collab.SnapshotChunk{SnapshotID: returnedID, Index: returnedIndex, SHA256: resp.Header.Get("X-Collab-Chunk-SHA256"), Data: data}, nil
}

func (p *httpCollaborationPeer) Events(ctx context.Context, after uint64) ([]collab.RoomEvent, error) {
	var value []collab.RoomEvent
	path := p.roomPath("events") + "?afterSequence=" + strconv.FormatUint(after, 10)
	err := p.doJSON(ctx, http.MethodGet, path, nil, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) Stream(ctx context.Context, after uint64, handle func(collab.RoomEvent) error) error {
	path := p.roomPath("stream") + "?afterSequence=" + strconv.FormatUint(after, 10)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+p.session)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := p.streamClient.Do(req)
	if err != nil {
		return &collaborationTransportError{message: err.Error(), retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var value collab.Error
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&value) == nil && value.Message != "" {
			return &value
		}
		return &collaborationTransportError{message: "collaboration stream returned " + resp.Status, retryable: resp.StatusCode >= 500}
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var value collab.RoomEvent
		if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &value); err != nil {
			return &collaborationTransportError{message: "decode collaboration event: " + err.Error(), retryable: true}
		}
		if handle != nil {
			if err := handle(value); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return &collaborationTransportError{message: err.Error(), retryable: true}
	}
	return &collaborationTransportError{message: "collaboration stream closed", retryable: true}
}

func (p *httpCollaborationPeer) Submit(ctx context.Context, env collab.CommandEnvelope) (collab.CommandReceipt, error) {
	env.Room, env.MemberID, env.Session = p.room, p.member, p.session
	var value collab.CommandReceipt
	path := p.roomPath("commands")
	err := p.doJSON(ctx, http.MethodPost, path, env, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) Heartbeat(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	path := "/collab/v1/heartbeat"
	if p.protocolVersion >= collaborationProtocolV2 {
		path = p.roomPath("heartbeat")
	}
	return p.doJSON(ctx, http.MethodPost, path, collab.SessionInput{
		RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session,
	}, &value, true)
}

func (p *httpCollaborationPeer) Leave(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	path := "/collab/v1/leave"
	if p.protocolVersion >= collaborationProtocolV2 {
		path = p.roomPath("leave")
	}
	return p.doJSON(ctx, http.MethodPost, path, collab.SessionInput{
		RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session,
	}, &value, true)
}

func (p *httpCollaborationPeer) roomPath(action string) string {
	prefix := "/collab/v1/rooms/"
	if p.protocolVersion >= collaborationProtocolV2 {
		prefix = "/collab/v2/rooms/"
	}
	return prefix + url.PathEscape(p.room) + "/" + strings.TrimPrefix(action, "/")
}

func (p *httpCollaborationPeer) doJSON(ctx context.Context, method, path string, input, output any, authorize bool) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.baseURL+path, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if authorize {
		req.Header.Set("Authorization", "Bearer "+p.session)
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return &collaborationTransportError{message: err.Error(), retryable: true}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var value collab.Error
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&value); err == nil && value.Message != "" {
			return &value
		}
		return &collaborationTransportError{message: "collaboration host returned " + resp.Status, retryable: resp.StatusCode >= 500}
	}
	if output == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output); err != nil {
		return &collaborationTransportError{message: "decode collaboration response: " + err.Error(), retryable: true}
	}
	return nil
}

type collaborationTransportError struct {
	message   string
	retryable bool
	causes    []error
}

func (e *collaborationTransportError) Error() string { return e.message }

func (e *collaborationTransportError) Unwrap() []error { return e.causes }

func collaborationErrorRetryable(err error) bool {
	var transport *collaborationTransportError
	if errors.As(err, &transport) {
		return transport.retryable
	}
	var protocol *collab.Error
	if errors.As(err, &protocol) {
		return protocol.Retryable
	}
	return false
}

func newCollaborationRequestID(prefix string) string {
	return stableCollaborationID(prefix, fmt.Sprintf("%d\x00%s", time.Now().UnixNano(), newSessionID()))
}

func collaborationPostCommand(input PostCollaborationMessageInput) (collab.Command, error) {
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	text := strings.TrimSpace(input.Text)
	switch kind {
	case "chat", "message", "":
		if text == "" {
			return collab.Command{}, fmt.Errorf("chat text is required")
		}
		return collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{
			Text: text, MentionMemberIDs: append([]string(nil), input.MentionMemberIDs...), MentionAgentIDs: append([]string(nil), input.MentionAgentIDs...), ReferenceIDs: append([]string(nil), input.ReferenceIDs...),
		}}, nil
	case "contribution":
		if text == "" || input.ContributionKind == "" {
			return collab.Command{}, fmt.Errorf("contribution kind and text are required")
		}
		return collab.Command{Type: collab.CommandPublishContribution, Contribution: &collab.PublishContributionInput{
			Kind: input.ContributionKind, Title: strings.TrimSpace(input.Title), Body: text,
			Scope: append([]string(nil), input.Scope...), TargetIDs: append([]string(nil), input.TargetIDs...),
			RelatedItem: strings.TrimSpace(input.RelatedItem), Dependencies: append([]string(nil), input.Dependencies...),
			ActionNeeded: input.ActionNeeded,
		}}, nil
	case "agent_request", "request":
		if text == "" || strings.TrimSpace(input.TargetMemberID) == "" {
			return collab.Command{}, fmt.Errorf("targetMemberId and instruction are required")
		}
		return collab.Command{Type: collab.CommandCreateAgentRequest, AgentRequest: &collab.CreateAgentRequestInput{
			TargetMemberID: strings.TrimSpace(input.TargetMemberID), Instruction: text,
			ReferenceIDs: append([]string(nil), input.ReferenceIDs...),
		}}, nil
	case "reaction":
		if strings.TrimSpace(input.TargetItemID) == "" || strings.TrimSpace(input.ReactionKind) == "" {
			return collab.Command{}, fmt.Errorf("reaction target and kind are required")
		}
		return collab.Command{Type: collab.CommandAddReaction, Reaction: &collab.AddReactionInput{
			TargetID: strings.TrimSpace(input.TargetItemID), Kind: strings.TrimSpace(input.ReactionKind),
		}}, nil
	default:
		return collab.Command{}, fmt.Errorf("unsupported collaboration message kind %q", input.Kind)
	}
}

func (c *desktopCollaboration) submit(ctx context.Context, requestID string, command collab.Command) (CollaborationActionResult, error) {
	c.mu.RLock()
	conn := c.conn
	state := c.state
	c.mu.RUnlock()
	if strings.TrimSpace(state.Room) == "" || strings.TrimSpace(state.MemberID) == "" {
		return CollaborationActionResult{}, fmt.Errorf("collaboration Room has no cached identity; join it once before working offline")
	}
	env := collab.CommandEnvelope{
		RequestID: strings.TrimSpace(requestID), Room: state.Room, MemberID: state.MemberID,
		QueuedAt: time.Now().UTC().Format(time.RFC3339Nano), Command: command,
	}
	if conn == nil || state.Status == "disconnected" || state.Status == "failed" {
		c.mu.Lock()
		duplicate := outboxContains(c.outbox, env.RequestID)
		if !duplicate {
			c.outbox = append(c.outbox, env)
		} else if existing, ok := outboxEnvelope(c.outbox, env.RequestID); ok {
			env = existing
		}
		if c.conn == nil {
			c.state.Status = "failed"
		} else {
			c.state.Status = "reconnecting"
		}
		c.state.LastError = "offline: collaboration update is queued for retry"
		c.state.Retryable = true
		c.state.OutboxCount = len(c.outbox)
		item := collaborationQueuedItem(env, c.state.Snapshot.LatestSequence+uint64(len(c.outbox)))
		c.persistLocked()
		c.mu.Unlock()
		c.emitState()
		return CollaborationActionResult{RequestID: requestID, Duplicate: duplicate, Queued: true, Retryable: true, Error: "waiting for collaboration Room reconnect", Item: item}, nil
	}
	env.Session = conn.connectionSession
	receipt, err := conn.peer.Submit(ctx, env)
	if err == nil {
		result := CollaborationActionResult{RequestID: requestID, Receipt: receipt, Duplicate: receipt.Duplicate}
		events, eventsErr := conn.peer.Events(ctx, state.Snapshot.LatestSequence)
		if eventsErr == nil {
			if !c.markConnected(conn, nil, events) {
				snapshot, snapshotErr := fetchCollaborationSnapshot(ctx, conn.peer)
				if snapshotErr == nil {
					c.markConnected(conn, &snapshot, events)
				}
			}
			result.Item = collaborationTimelineAt(c.snapshot().Snapshot, receipt.LatestSequence)
		}
		return result, nil
	}
	if !collaborationErrorRetryable(err) {
		return CollaborationActionResult{}, err
	}
	c.mu.Lock()
	var item *collab.TimelineItem
	if c.conn == conn {
		env.Session = ""
		if !outboxContains(c.outbox, env.RequestID) {
			c.outbox = append(c.outbox, env)
		} else if existing, ok := outboxEnvelope(c.outbox, env.RequestID); ok {
			env = existing
		}
		item = collaborationQueuedItem(env, c.state.Snapshot.LatestSequence+uint64(len(c.outbox)))
		c.state.Status = "reconnecting"
		c.state.LastError = err.Error()
		c.state.Retryable = true
		c.state.OutboxCount = len(c.outbox)
		c.persistLocked()
	}
	c.mu.Unlock()
	c.emitState()
	return CollaborationActionResult{RequestID: requestID, Queued: true, Retryable: true, Error: err.Error(), Item: item}, nil
}

func (c *desktopCollaboration) submitRunCommand(ctx context.Context, run *collaborationAgentRun, requestID string, command collab.Command) (CollaborationActionResult, error) {
	c.mu.RLock()
	conn := c.conn
	matching := conn != nil && c.state.Room == run.Room && c.state.MemberID == run.MemberID && c.state.AgentID == run.AgentID
	c.mu.RUnlock()
	if matching {
		return c.submit(ctx, requestID, command)
	}
	env := collab.CommandEnvelope{RequestID: requestID, Room: run.Room, MemberID: run.MemberID, QueuedAt: time.Now().UTC().Format(time.RFC3339Nano), Command: command}
	c.mu.Lock()
	if !outboxContains(c.outbox, requestID) {
		c.outbox = append(c.outbox, env)
	}
	c.state.LastError = "Agent result is waiting for its original collaboration Room"
	c.state.Retryable = true
	c.persistLocked()
	c.mu.Unlock()
	c.emitState()
	return CollaborationActionResult{RequestID: requestID, Queued: true, Retryable: true, Error: "waiting for original collaboration Room"}, nil
}

func outboxContains(values []collab.CommandEnvelope, requestID string) bool {
	for _, value := range values {
		if value.RequestID == requestID {
			return true
		}
	}
	return false
}

func outboxEnvelope(values []collab.CommandEnvelope, requestID string) (collab.CommandEnvelope, bool) {
	for _, value := range values {
		if value.RequestID == requestID {
			return value, true
		}
	}
	return collab.CommandEnvelope{}, false
}

func (c *desktopCollaboration) connectionLoop(ctx context.Context, conn *collaborationConnection) {
	defer close(conn.done)
	go c.connectionHeartbeatLoop(ctx, conn)
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		c.syncConnection(ctx, conn)
		c.mu.RLock()
		after := c.state.Snapshot.LatestSequence
		current := c.conn == conn
		c.mu.RUnlock()
		if !current {
			return
		}
		streamed := false
		started := time.Now()
		err := conn.peer.Stream(ctx, after, func(value collab.RoomEvent) error {
			streamed = true
			return c.consumeStreamEvent(ctx, conn, value)
		})
		if ctx.Err() != nil {
			return
		}
		c.markReconnect(conn, err)
		if streamed || time.Since(started) >= 5*time.Second {
			attempt = 0
		}
		if conn.mode == "client" && err != nil && attempt+1 >= collaborationRouteFailoverAttempts {
			c.startRouteFailover(conn)
		}
		delay := collaborationReconnectDelay(attempt, uint64(time.Now().UnixNano()))
		if c.streamRetryDelay != nil {
			delay = c.streamRetryDelay(attempt, uint64(time.Now().UnixNano()))
		}
		if attempt < 16 {
			attempt++
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (c *desktopCollaboration) startRouteFailover(failed *collaborationConnection) bool {
	routes := collaborationAlternativeRoutes(failed)
	if len(routes) == 0 {
		return false
	}
	failed.failoverMu.Lock()
	if failed.failoverActive {
		failed.failoverMu.Unlock()
		return false
	}
	failed.failoverActive = true
	failed.failoverMu.Unlock()
	go c.failoverConnection(failed, routes)
	return true
}

func collaborationAlternativeRoutes(failed *collaborationConnection) []CollaborationRouteInput {
	if failed == nil {
		return nil
	}
	routes := make([]CollaborationRouteInput, 0, len(failed.routes))
	seen := map[string]struct{}{}
	for _, state := range failed.routes {
		if state.Active {
			seen[collaborationRouteIdentity(state.CollaborationRouteInput)] = struct{}{}
		}
	}
	for _, state := range failed.routes {
		if state.Active {
			continue
		}
		route := state.CollaborationRouteInput
		key := collaborationRouteIdentity(route)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		routes = append(routes, route)
	}
	return routes
}

func collaborationRouteIdentity(route CollaborationRouteInput) string {
	return strings.ToLower(strings.TrimSpace(route.Kind)) + "\x00" + strings.ToLower(strings.TrimSpace(route.Host)) + "\x00" + strconv.Itoa(route.Port) + "\x00" + strings.TrimSpace(route.RelayID) + "\x00" + strings.TrimSpace(route.URL) + "\x00" + strings.TrimSpace(route.TunnelID)
}

func (c *desktopCollaboration) failoverConnection(failed *collaborationConnection, routes []CollaborationRouteInput) {
	defer func() {
		failed.failoverMu.Lock()
		failed.failoverActive = false
		failed.failoverMu.Unlock()
	}()
	c.opMu.Lock()
	defer c.opMu.Unlock()
	c.mu.RLock()
	current := c.conn == failed
	c.mu.RUnlock()
	if !current {
		return
	}
	for i := range routes {
		route := &routes[i]
		if route.Kind == "relay" && route.GuestCapability == "" {
			if ref := failed.guestCapabilityRefs[route.RelayID]; ref != "" {
				route.GuestCapability = c.getSecret(ref)
			}
		}
	}
	identity := collab.MemberDescriptor{
		ID: failed.memberID, Name: failed.memberName, Avatar: failed.memberAvatar, Role: failed.memberRole,
		Agent: collab.AgentDescriptor{ID: failed.agentID, Name: failed.agentName, Avatar: failed.agentAvatar, Role: failed.agentRole},
	}
	openJoin := c.openJoin
	if openJoin == nil {
		openJoin = c.openJoinedRoom
	}
	conn, err := openJoin(c.app.bootContext(), JoinCollaborationRoomInput{
		Room: failed.room, Token: failed.joinToken, MemberID: failed.memberID, MemberName: failed.memberName,
		MemberAvatar: failed.memberAvatar, MemberRole: failed.memberRole, AgentID: failed.agentID, AgentName: failed.agentName,
		AgentAvatar: failed.agentAvatar, AgentRole: failed.agentRole, SessionID: failed.sessionID, Routes: routes, HostKey: failed.hostKey,
	}, identity, failed.connectionSession)
	if err != nil {
		// Keep the current peer/session alive. Its stream loop continues retrying
		// while a later bounded failover attempt may recover another route.
		c.markReconnect(failed, err)
		return
	}
	if _, err := c.installConnection(conn); err != nil {
		c.markReconnect(failed, err)
	}
}

func collaborationReconnectDelay(attempt int, entropy uint64) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 6 {
		attempt = 6
	}
	base := 500 * time.Millisecond * time.Duration(1<<attempt)
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	// Stable bounded jitter prevents a Room full of Clients from reconnecting
	// on the same cadence after a Host restart.
	span := base / 5
	if span == 0 {
		return base
	}
	offset := time.Duration(entropy%uint64(span*2+1)) - span
	delay := base + offset
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (c *desktopCollaboration) connectionHeartbeatLoop(ctx context.Context, conn *collaborationConnection) {
	ticker := time.NewTicker(collaborationHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := conn.peer.Heartbeat(ctx, newCollaborationRequestID("heartbeat")); err != nil {
				c.markReconnect(conn, err)
			}
			if conn.sweep != nil {
				if err := conn.sweep(ctx); err != nil {
					c.markReconnect(conn, err)
				}
			}
		}
	}
}

func (c *desktopCollaboration) consumeStreamEvent(ctx context.Context, conn *collaborationConnection, value collab.RoomEvent) error {
	c.mu.RLock()
	if c.conn != conn {
		c.mu.RUnlock()
		return context.Canceled
	}
	after := c.state.Snapshot.LatestSequence
	c.mu.RUnlock()
	if value.Sequence <= after {
		return nil
	}
	if value.Sequence != after+1 {
		events, err := conn.peer.Events(ctx, after)
		if err != nil {
			return err
		}
		if len(events) > 0 && c.markConnected(conn, nil, events) {
			return nil
		}
		snapshot, err := fetchCollaborationSnapshot(ctx, conn.peer)
		if err != nil {
			return err
		}
		if snapshot.LatestSequence < value.Sequence {
			return &collaborationTransportError{message: fmt.Sprintf("collaboration snapshot stopped at %d while recovering event %d", snapshot.LatestSequence, value.Sequence), retryable: true}
		}
		c.markConnected(conn, &snapshot, events)
		return nil
	}
	if c.markConnected(conn, nil, []collab.RoomEvent{value}) {
		return nil
	}
	snapshot, err := fetchCollaborationSnapshot(ctx, conn.peer)
	if err != nil {
		return err
	}
	if snapshot.LatestSequence < value.Sequence {
		return &collaborationTransportError{message: fmt.Sprintf("collaboration snapshot stopped at %d while projecting event %d", snapshot.LatestSequence, value.Sequence), retryable: true}
	}
	c.markConnected(conn, &snapshot, []collab.RoomEvent{value})
	return nil
}

func (c *desktopCollaboration) syncConnection(ctx context.Context, conn *collaborationConnection) {
	conn.syncMu.Lock()
	defer conn.syncMu.Unlock()
	if !c.drainOutbox(ctx, conn) {
		return
	}
	c.mu.RLock()
	if c.conn != conn {
		c.mu.RUnlock()
		return
	}
	after := c.state.Snapshot.LatestSequence
	c.mu.RUnlock()
	events, err := conn.peer.Events(ctx, after)
	if err != nil {
		c.markReconnect(conn, err)
		return
	}
	if len(events) == 0 {
		c.markConnected(conn, nil, nil)
		return
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	if events[0].Sequence != after+1 {
		snapshot, snapErr := fetchCollaborationSnapshot(ctx, conn.peer)
		if snapErr != nil {
			c.markReconnect(conn, snapErr)
			return
		}
		c.markConnected(conn, &snapshot, nil)
		return
	}
	if c.markConnected(conn, nil, events) {
		return
	}
	snapshot, err := fetchCollaborationSnapshot(ctx, conn.peer)
	if err != nil {
		c.markReconnect(conn, err)
		return
	}
	c.markConnected(conn, &snapshot, events)
}

func (c *desktopCollaboration) drainOutbox(ctx context.Context, conn *collaborationConnection) bool {
	for {
		c.mu.RLock()
		if c.conn != conn {
			c.mu.RUnlock()
			return false
		}
		index := -1
		var env collab.CommandEnvelope
		for i, candidate := range c.outbox {
			if candidate.Room == conn.room && candidate.MemberID == conn.memberID && c.outboxFailures[candidate.RequestID] == "" {
				index, env = i, candidate
				break
			}
		}
		c.mu.RUnlock()
		if index < 0 {
			return true
		}
		env.Session = conn.connectionSession
		if _, err := conn.peer.Submit(ctx, env); err != nil {
			if collaborationSessionInvalid(err) {
				c.markReconnect(conn, err)
				return false
			}
			if collaborationErrorRetryable(err) {
				c.markReconnect(conn, err)
				return false
			}
			c.mu.Lock()
			if c.conn == conn {
				if c.outboxFailures == nil {
					c.outboxFailures = map[string]string{}
				}
				c.outboxFailures[env.RequestID] = err.Error()
				c.state.LastError = "collaboration command " + env.RequestID + " failed: " + err.Error()
				c.state.Retryable = true
				c.state.OutboxCount = len(c.outbox)
				c.persistLocked()
			}
			c.mu.Unlock()
			c.emitState()
			continue
		}
		c.mu.Lock()
		if c.conn == conn {
			for i, candidate := range c.outbox {
				if candidate.RequestID == env.RequestID {
					c.outbox = append(c.outbox[:i], c.outbox[i+1:]...)
					delete(c.outboxFailures, env.RequestID)
					break
				}
			}
			c.state.OutboxCount = len(c.outbox)
			c.persistLocked()
		}
		c.mu.Unlock()
	}
}

func (c *desktopCollaboration) markReconnect(conn *collaborationConnection, err error) {
	if err == nil {
		return
	}
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	invalidSession := collaborationSessionInvalid(err)
	if invalidSession {
		c.conn = nil
		c.state.Status = "failed"
	} else {
		c.state.Status = "reconnecting"
	}
	c.state.LastError = err.Error()
	c.state.Retryable = true
	c.persistLocked()
	c.mu.Unlock()
	if invalidSession && conn.cancel != nil {
		conn.cancel()
	}
	c.emitState()
}

func collaborationSessionInvalid(err error) bool {
	var protocol *collab.Error
	return errors.As(err, &protocol) && protocol.Code == collab.CodeUnauthorized
}

func (c *desktopCollaboration) markConnected(conn *collaborationConnection, snapshot *collab.Snapshot, events []collab.RoomEvent) bool {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return false
	}
	installed := snapshot
	if installed == nil && len(events) > 0 {
		projected, err := projectCollaborationEvents(c.state.Snapshot, events)
		if err != nil {
			c.mu.Unlock()
			return false
		}
		installed = &projected
	}
	resumeTransfers := c.state.Status == "reconnecting"
	c.state.Status = "connected"
	if c.leaveError != "" {
		c.state.Status = "failed"
		c.state.LastError = c.leaveError
		c.state.Retryable = true
	} else if len(c.outboxFailures) > 0 {
		c.state.LastError = fmt.Sprintf("%d collaboration command(s) require manual retry", len(c.outboxFailures))
		c.state.Retryable = true
	} else if !strings.HasPrefix(c.state.LastError, collaborationAutoReceiveNotice) {
		c.state.LastError = ""
		c.state.Retryable = false
	}
	if installed != nil {
		c.state.Snapshot = *installed
		c.rebuildFileOffersLocked(*installed)
	}
	c.state.OutboxCount = len(c.outbox)
	c.persistLocked()
	c.mu.Unlock()
	c.observeUnread()
	if c.app != nil && c.app.ctx != nil {
		for _, value := range events {
			c.app.runtimeEvents.Emit(c.app.ctx, collaborationEventChannel, collaborationEventView(c.ownerSessionID, value))
		}
	}
	c.emitState()
	go c.startNextQueuedAgent(conn.sessionID)
	if c.scheduler != nil {
		c.scheduler.signal(wakeSignal)
	}
	if c.hasPendingFileOrigins(conn) {
		go c.restoreFileOrigins(conn)
	}
	if installed != nil && collaborationEventsAffectFiles(events) {
		c.signalAutoReceiveFiles()
	}
	if resumeTransfers {
		go c.resumeWaitingFileTransfers()
	}
	return true
}

func collaborationEventsAffectFiles(events []collab.RoomEvent) bool {
	// A Snapshot installed without its incremental events is a full reconcile
	// point (initial join, gap recovery, or reconnect).
	if len(events) == 0 {
		return true
	}
	for _, value := range events {
		eventType := strings.ToLower(strings.TrimSpace(value.Type))
		// File offers obviously change auto-receive eligibility. Member presence
		// changes matter too: a transfer stuck in waiting_sender because the file
		// owner was offline must re-evaluate the moment the owner comes online
		// (member.online / member.joined / member.rejoined) or leaves.
		if strings.HasPrefix(eventType, "file.") || strings.HasPrefix(eventType, "member") {
			return true
		}
	}
	return false
}

func collaborationEventView(sessionID string, value collab.RoomEvent) CollaborationEventView {
	view := CollaborationEventView{SessionID: strings.TrimSpace(sessionID), Event: value}
	var item collab.TimelineItem
	if len(value.Payload) > 0 && json.Unmarshal(value.Payload, &item) == nil && item.ID != "" {
		view.Item = &item
	}
	return view
}

func collaborationTimelineAt(snapshot collab.Snapshot, sequence uint64) *collab.TimelineItem {
	for i := range snapshot.Timeline {
		if snapshot.Timeline[i].Sequence == sequence {
			value := snapshot.Timeline[i]
			return &value
		}
	}
	return nil
}
