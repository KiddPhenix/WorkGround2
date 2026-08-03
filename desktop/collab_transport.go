package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
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
	storeRoot := strings.TrimSpace(config.MemoryUserDir())
	if storeRoot == "" {
		return nil, fmt.Errorf("collaboration data directory is unavailable")
	}
	store, err := collab.OpenFileStore(filepath.Join(storeRoot, "collaboration-host-v1"))
	if err != nil {
		return nil, err
	}
	hub := collab.NewHub()
	service := collab.NewService(store, hub)
	_, err = service.CreateRoom(ctx, collab.CreateRoomInput{
		RequestID:   stableCollaborationID("create", room),
		ID:          room,
		Name:        roomName,
		Description: strings.TrimSpace(input.Description),
		Token:       strings.TrimSpace(input.Token),
	})
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(listenHost, strconv.Itoa(input.Port)))
	if err != nil {
		return nil, fmt.Errorf("listen collaboration host: %w", err)
	}
	actualPort := listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: collab.NewHandler(service, hub), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if serveErr := server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			c.failState("failed", fmt.Errorf("collaboration host stopped: %w", serveErr), true)
		}
	}()
	joined, err := service.Join(ctx, collab.JoinInput{
		RequestID: newCollaborationRequestID("join"), Room: room, Token: strings.TrimSpace(input.Token), Member: identity, ResumeSession: strings.TrimSpace(resume),
	})
	if err != nil {
		_ = server.Shutdown(context.Background())
		return nil, err
	}
	peer := &serviceCollaborationPeer{service: service, hub: hub, room: room, member: joined.Member.ID, session: joined.ConnectionSession}
	snapshot, err := peer.Snapshot(ctx)
	if err != nil {
		_ = server.Shutdown(context.Background())
		return nil, err
	}
	return &collaborationConnection{
		peer: peer, host: server, listener: listener, mode: "host", roomName: roomName, description: strings.TrimSpace(input.Description), hostName: listenHost,
		port: actualPort, room: room, memberID: joined.Member.ID, agentID: joined.Member.Agent.ID,
		memberName: identity.Name, memberRole: identity.Role, agentName: identity.Agent.Name, agentRole: identity.Agent.Role,
		sessionID: strings.TrimSpace(input.SessionID), connectionSession: joined.ConnectionSession,
		initialSnapshot: snapshot, joinToken: strings.TrimSpace(input.Token), rejoined: joined.Rejoined,
		sweep: func(sweepCtx context.Context) error {
			_, sweepErr := service.SweepStale(sweepCtx, collab.SweepInput{
				RequestID: newCollaborationRequestID("sweep"), Room: room, Before: time.Now().UTC().Add(-collaborationMemberStaleAfter),
			})
			return sweepErr
		},
	}, nil
}

func (c *desktopCollaboration) openJoinedRoom(ctx context.Context, input JoinCollaborationRoomInput, identity collab.MemberDescriptor, resume string) (*collaborationConnection, error) {
	host := strings.TrimSpace(input.Host)
	if host == "" || input.Port <= 0 || input.Port > 65535 {
		return nil, fmt.Errorf("host and a valid port are required")
	}
	room := strings.TrimSpace(input.Room)
	peer, joined, snapshot, err := joinCollaborationPeer(ctx, host, input.Port, room, strings.TrimSpace(input.Token), identity, resume)
	if err != nil {
		return nil, err
	}
	return &collaborationConnection{
		peer: peer, mode: "client", hostName: host, port: input.Port, room: room,
		memberID: joined.Member.ID, agentID: joined.Member.Agent.ID,
		memberName: identity.Name, memberRole: identity.Role, agentName: identity.Agent.Name, agentRole: identity.Agent.Role,
		sessionID: strings.TrimSpace(input.SessionID), connectionSession: joined.ConnectionSession,
		initialSnapshot: snapshot, joinToken: strings.TrimSpace(input.Token), rejoined: joined.Rejoined,
	}, nil
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
	baseURL      string
	client       *http.Client
	streamClient *http.Client
	room         string
	member       string
	session      string
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

func joinCollaborationPeer(ctx context.Context, host string, port int, room, token string, identity collab.MemberDescriptor, resume string) (*httpCollaborationPeer, collab.JoinResult, collab.Snapshot, error) {
	baseURL := "http://" + net.JoinHostPort(host, strconv.Itoa(port))
	peer := &httpCollaborationPeer{baseURL: baseURL, client: &http.Client{Timeout: 15 * time.Second}, streamClient: &http.Client{}, room: room, member: identity.ID}
	var joined collab.JoinResult
	err := peer.doJSON(ctx, http.MethodPost, "/collab/v1/join", collab.JoinInput{
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
	snapshot, err := peer.Snapshot(ctx)
	if err != nil {
		return nil, collab.JoinResult{}, collab.Snapshot{}, err
	}
	return peer, joined, snapshot, nil
}

func (p *httpCollaborationPeer) Snapshot(ctx context.Context) (collab.Snapshot, error) {
	var value collab.Snapshot
	path := "/collab/v1/rooms/" + url.PathEscape(p.room) + "/snapshot"
	err := p.doJSON(ctx, http.MethodGet, path, nil, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) Events(ctx context.Context, after uint64) ([]collab.RoomEvent, error) {
	var value []collab.RoomEvent
	path := "/collab/v1/rooms/" + url.PathEscape(p.room) + "/events?afterSequence=" + strconv.FormatUint(after, 10)
	err := p.doJSON(ctx, http.MethodGet, path, nil, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) Stream(ctx context.Context, after uint64, handle func(collab.RoomEvent) error) error {
	path := "/collab/v1/rooms/" + url.PathEscape(p.room) + "/stream?afterSequence=" + strconv.FormatUint(after, 10)
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
	path := "/collab/v1/rooms/" + url.PathEscape(p.room) + "/commands"
	err := p.doJSON(ctx, http.MethodPost, path, env, &value, true)
	return value, err
}

func (p *httpCollaborationPeer) Heartbeat(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	return p.doJSON(ctx, http.MethodPost, "/collab/v1/heartbeat", collab.SessionInput{
		RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session,
	}, &value, true)
}

func (p *httpCollaborationPeer) Leave(ctx context.Context, requestID string) error {
	var value collab.CommandReceipt
	return p.doJSON(ctx, http.MethodPost, "/collab/v1/leave", collab.SessionInput{
		RequestID: requestID, Room: p.room, MemberID: p.member, Session: p.session,
	}, &value, true)
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
}

func (e *collaborationTransportError) Error() string { return e.message }

func collaborationErrorRetryable(err error) bool {
	var protocol *collab.Error
	if errors.As(err, &protocol) {
		return protocol.Retryable
	}
	var transport *collaborationTransportError
	return errors.As(err, &transport) && transport.retryable
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
		return collab.Command{Type: collab.CommandPostChat, Chat: &collab.PostChatInput{Text: text}}, nil
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
		snapshot, snapshotErr := conn.peer.Snapshot(ctx)
		if eventsErr == nil && snapshotErr == nil {
			c.markConnected(conn, &snapshot, events)
			result.Item = collaborationTimelineAt(snapshot, receipt.LatestSequence)
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
		delay := collaborationReconnectDelay(attempt, uint64(time.Now().UnixNano()))
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
		return &collaborationTransportError{message: fmt.Sprintf("collaboration event gap after %d: got %d", after, value.Sequence), retryable: true}
	}
	snapshot, err := conn.peer.Snapshot(ctx)
	if err != nil {
		return err
	}
	c.markConnected(conn, &snapshot, []collab.RoomEvent{value})
	return nil
}

func (c *desktopCollaboration) syncConnection(ctx context.Context, conn *collaborationConnection) {
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
		snapshot, snapErr := conn.peer.Snapshot(ctx)
		if snapErr != nil {
			c.markReconnect(conn, snapErr)
			return
		}
		c.markConnected(conn, &snapshot, nil)
		return
	}
	snapshot, err := conn.peer.Snapshot(ctx)
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
	if c.conn == conn {
		c.state.Status = "reconnecting"
		c.state.LastError = err.Error()
		c.state.Retryable = true
		c.persistLocked()
	}
	c.mu.Unlock()
	c.emitState()
}

func (c *desktopCollaboration) markConnected(conn *collaborationConnection, snapshot *collab.Snapshot, events []collab.RoomEvent) {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	c.state.Status = "connected"
	if c.leaveError != "" {
		c.state.Status = "failed"
		c.state.LastError = c.leaveError
		c.state.Retryable = true
	} else if len(c.outboxFailures) == 0 {
		c.state.LastError = ""
		c.state.Retryable = false
	} else {
		c.state.LastError = fmt.Sprintf("%d collaboration command(s) require manual retry", len(c.outboxFailures))
		c.state.Retryable = true
	}
	if snapshot != nil {
		c.state.Snapshot = *snapshot
	}
	c.state.OutboxCount = len(c.outbox)
	c.persistLocked()
	c.mu.Unlock()
	if c.app != nil && c.app.ctx != nil {
		for _, value := range events {
			c.app.runtimeEvents.Emit(c.app.ctx, collaborationEventChannel, collaborationEventView(c.ownerSessionID, value))
		}
	}
	c.emitState()
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
