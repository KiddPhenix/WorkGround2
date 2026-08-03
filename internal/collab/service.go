package collab

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

const (
	MaxMembers           = 256
	MaxTimelineItems     = 100000
	MaxTextBytes         = 256 * 1024
	MaxReferences        = 256
	MaxMetadata          = 128
	MaxIDBytes           = 256
	MaxShortText         = 4096
	MaxTransientRequests = 8192
)

var roomIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
var fallbackID atomic.Uint64

// Service is the single mutation entrance for all hosted rooms.
type Service struct {
	store *FileStore
	hub   *Hub
	now   func() time.Time
}

func NewService(store *FileStore, hubs ...*Hub) *Service {
	hub := NewHub()
	if len(hubs) > 0 && hubs[0] != nil {
		hub = hubs[0]
	}
	return &Service{store: store, hub: hub, now: time.Now}
}

func (s *Service) Hub() *Hub { return s.hub }

func (s *Service) CreateRoom(ctx context.Context, input CreateRoomInput) (Room, error) {
	if err := ctx.Err(); err != nil {
		return Room{}, err
	}
	input.RequestID, input.ID, input.Name = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.ID), strings.TrimSpace(input.Name)
	input.Token = strings.TrimSpace(input.Token)
	if !validID(input.RequestID) || !roomIDPattern.MatchString(input.ID) || input.Name == "" {
		return Room{}, fail(CodeInvalid, "requestId, valid room id, and name are required")
	}
	if len(input.Name) > 256 || len(input.Description) > MaxTextBytes || len(input.Token) > 4096 {
		return Room{}, fail(CodeInvalid, "room field exceeds size limit")
	}
	requestHash := fingerprint(struct {
		RequestID, ID, Name, Description, TokenHash string
	}{input.RequestID, input.ID, input.Name, input.Description, hashSecret(input.Token)})
	now := s.now().UTC()
	s.store.mu.Lock()
	if state, ok := s.store.room(input.ID); ok {
		if _, duplicate, requestErr := replayRequest(state, input.RequestID, requestHash); requestErr != nil {
			s.store.mu.Unlock()
			return Room{}, requestErr
		} else if duplicate {
			room := state.Room
			s.store.mu.Unlock()
			return room, nil
		}
		s.store.mu.Unlock()
		return Room{}, fail(CodeConflict, "room already exists")
	}
	room := Room{ID: input.ID, Name: input.Name, Description: input.Description, TokenRequired: input.Token != "", CreatedAt: now, LatestSequence: 1}
	payload, _ := json.Marshal(room)
	event := RoomEvent{EventID: newID("event"), Sequence: 1, Room: input.ID, Type: "room.created", RequestID: input.RequestID, CreatedAt: now, Payload: payload}
	receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{event.EventID}, LatestSequence: 1}
	record := journalRecord{Event: event, Receipt: receipt, RequestHash: requestHash, Room: &room, TokenHash: hashSecret(input.Token)}
	err := s.store.append(record)
	s.store.mu.Unlock()
	if err != nil {
		return Room{}, err
	}
	s.hub.Publish(input.ID)
	return room, nil
}

func (s *Service) Join(ctx context.Context, input JoinInput) (JoinResult, error) {
	if err := ctx.Err(); err != nil {
		return JoinResult{}, err
	}
	input.RequestID, input.Room, input.Token = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.Room), strings.TrimSpace(input.Token)
	input.Member.ID, input.Member.Name = strings.TrimSpace(input.Member.ID), strings.TrimSpace(input.Member.Name)
	input.Member.Agent.ID, input.Member.Agent.Name = strings.TrimSpace(input.Member.Agent.ID), strings.TrimSpace(input.Member.Agent.Name)
	if !validID(input.RequestID) || !roomIDPattern.MatchString(input.Room) || !validID(input.Member.ID) || input.Member.Name == "" || !validID(input.Member.Agent.ID) {
		return JoinResult{}, fail(CodeInvalid, "requestId, room, member id/name, and agent id are required")
	}
	if len(input.Member.Name) > 256 || len(input.Member.Role) > 128 || len(input.Member.Agent.Name) > 256 || !validStringList(input.Member.Agent.Capabilities, 128, MaxShortText) {
		return JoinResult{}, fail(CodeInvalid, "member descriptor exceeds size limit")
	}
	requestHash := fingerprint(struct {
		RequestID, Room, TokenHash string
		Member                     MemberDescriptor
	}{input.RequestID, input.Room, hashSecret(input.Token), input.Member})
	now := s.now().UTC()
	s.store.mu.Lock()
	state, ok := s.store.room(input.Room)
	if !ok {
		s.store.mu.Unlock()
		return JoinResult{}, fail(CodeNotFound, "room does not exist")
	}
	if receipt, duplicate, requestErr := replayRequest(state, input.RequestID, requestHash); requestErr != nil {
		s.store.mu.Unlock()
		return JoinResult{}, requestErr
	} else if duplicate {
		if len(receipt.EventIDs) == 0 {
			s.store.mu.Unlock()
			return JoinResult{}, fail(CodeConflict, "requestId was already used by a transient operation")
		}
		member, exists := state.Members[input.Member.ID]
		if !exists {
			s.store.mu.Unlock()
			return JoinResult{}, fail(CodeConflict, "requestId belongs to another operation")
		}
		session := s.deriveSession(input.Room, input.Member.ID, input.RequestID)
		result := JoinResult{Member: cloneMember(member), ConnectionSession: session, LatestSequence: receipt.LatestSequence, Rejoined: eventType(state, receipt) == "member.rejoined"}
		s.store.mu.Unlock()
		return result, nil
	}
	if !secretEqual(state.TokenHash, input.Token) {
		s.store.mu.Unlock()
		return JoinResult{}, fail(CodeUnauthorized, "room token is invalid")
	}
	old, exists := state.Members[input.Member.ID]
	if !exists && len(state.Members) >= MaxMembers {
		s.store.mu.Unlock()
		return JoinResult{}, fail(CodeConflict, "room member limit reached")
	}
	if exists && !s.sessionMatchesLocked(state, input.Member.ID, input.ResumeSession) {
		s.store.mu.Unlock()
		return JoinResult{}, fail(CodeUnauthorized, "resume session is required for this member")
	}
	for id, member := range state.Members {
		if id != input.Member.ID && member.Agent.ID == input.Member.Agent.ID {
			s.store.mu.Unlock()
			return JoinResult{}, fail(CodeConflict, "agent id already belongs to another member")
		}
	}
	joinedAt := now
	if exists {
		joinedAt = old.JoinedAt
	}
	member := Member{ID: input.Member.ID, Name: input.Member.Name, Role: input.Member.Role, Agent: input.Member.Agent, Status: MemberOnline, JoinedAt: joinedAt, LastSeenAt: now}
	member.Agent.Capabilities = cloneStrings(input.Member.Agent.Capabilities)
	if member.Agent.Name == "" {
		member.Agent.Name = member.Name + " Agent"
	}
	if member.Agent.Status == "" || member.Agent.Status == AgentOffline {
		member.Agent.Status = AgentIdle
	}
	sequence := state.Room.LatestSequence + 1
	eventTypeName := "member.joined"
	if exists {
		eventTypeName = "member.rejoined"
	}
	session := s.deriveSession(input.Room, input.Member.ID, input.RequestID)
	sessionHash := hashSecret(session)
	public := TimelineItem{ID: newID("timeline"), Sequence: sequence, Type: TimelineSystem, System: &SystemEvent{Kind: eventTypeName, MemberID: member.ID}}
	payload, _ := json.Marshal(public)
	event := RoomEvent{EventID: newID("event"), Sequence: sequence, Room: input.Room, Type: eventTypeName, ActorID: member.ID, RequestID: input.RequestID, CreatedAt: now, Payload: payload}
	receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{event.EventID}, LatestSequence: sequence}
	record := journalRecord{Event: event, Receipt: receipt, RequestHash: requestHash, Member: &member, SessionHash: sessionHash, Timeline: &public, RevokeSessions: exists}
	if err := s.store.append(record); err != nil {
		s.store.mu.Unlock()
		return JoinResult{}, err
	}
	result := JoinResult{Member: cloneMember(member), ConnectionSession: session, LatestSequence: sequence, Rejoined: exists}
	s.store.mu.Unlock()
	s.hub.Publish(input.Room)
	return result, nil
}

func (s *Service) Leave(ctx context.Context, input SessionInput) (CommandReceipt, error) {
	return s.memberSignal(ctx, input, true)
}

func (s *Service) Heartbeat(ctx context.Context, input SessionInput) (CommandReceipt, error) {
	return s.memberSignal(ctx, input, false)
}

// SweepStale persists offline state for members that have not heartbeated
// since Before. Hosts should call it periodically with a new stable requestId.
func (s *Service) SweepStale(ctx context.Context, input SweepInput) (CommandReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CommandReceipt{}, err
	}
	input.RequestID, input.Room = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.Room)
	if !validID(input.RequestID) || !roomIDPattern.MatchString(input.Room) || input.Before.IsZero() {
		return CommandReceipt{}, fail(CodeInvalid, "requestId, room, and before are required")
	}
	input.Before = input.Before.UTC()
	requestHash := fingerprint(input)
	now := s.now().UTC()
	s.store.mu.Lock()
	state, ok := s.store.room(input.Room)
	if !ok {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeNotFound, "room does not exist")
	}
	if receipt, duplicate, requestErr := replayRequest(state, input.RequestID, requestHash); requestErr != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, requestErr
	} else if duplicate {
		s.store.mu.Unlock()
		return receipt, nil
	}
	stale := make([]Member, 0)
	ids := make([]string, 0)
	for _, member := range state.Members {
		if member.Status != MemberOnline || !member.LastSeenAt.Before(input.Before) {
			continue
		}
		member.Status, member.Agent.Status = MemberOffline, AgentOffline
		stale = append(stale, member)
		ids = append(ids, member.ID)
	}
	if len(stale) == 0 {
		receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{}, LatestSequence: state.Room.LatestSequence}
		rememberTransient(state, input.RequestID, requestHash, receipt)
		s.store.mu.Unlock()
		return receipt, nil
	}
	sequence := state.Room.LatestSequence + 1
	payload, _ := json.Marshal(struct {
		MemberIDs []string  `json:"memberIds"`
		Before    time.Time `json:"before"`
	}{ids, input.Before})
	event := RoomEvent{EventID: newID("event"), Sequence: sequence, Room: input.Room, Type: "members.stale", RequestID: input.RequestID, CreatedAt: now, Payload: payload}
	receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{event.EventID}, LatestSequence: sequence}
	if err := s.store.append(journalRecord{Event: event, Receipt: receipt, RequestHash: requestHash, Members: stale}); err != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, err
	}
	s.store.mu.Unlock()
	s.hub.Publish(input.Room)
	return receipt, nil
}

func (s *Service) memberSignal(ctx context.Context, input SessionInput, leave bool) (CommandReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CommandReceipt{}, err
	}
	input.RequestID, input.Room, input.MemberID = strings.TrimSpace(input.RequestID), strings.TrimSpace(input.Room), strings.TrimSpace(input.MemberID)
	if !validID(input.RequestID) || !roomIDPattern.MatchString(input.Room) || !validID(input.MemberID) || input.Session == "" {
		return CommandReceipt{}, fail(CodeInvalid, "requestId, room, memberId, and connectionSession are required")
	}
	now := s.now().UTC()
	requestHash := fingerprint(struct {
		RequestID, Room, MemberID, SessionHash string
		Leave                                  bool
	}{input.RequestID, input.Room, input.MemberID, hashSecret(input.Session), leave})
	s.store.mu.Lock()
	state, ok := s.store.room(input.Room)
	if !ok {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeNotFound, "room does not exist")
	}
	if receipt, duplicate, requestErr := replayRequest(state, input.RequestID, requestHash); requestErr != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, requestErr
	} else if duplicate {
		s.store.mu.Unlock()
		return receipt, nil
	}
	if !s.sessionMatchesLocked(state, input.MemberID, input.Session) {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeUnauthorized, "connection session is invalid")
	}
	member := state.Members[input.MemberID]
	member.LastSeenAt = now
	if leave {
		if member.Status != MemberOnline {
			s.store.mu.Unlock()
			return CommandReceipt{}, fail(CodeConflict, "member is already offline")
		}
		member.Status = MemberOffline
		member.Agent.Status = AgentOffline
	} else if member.Status == MemberOnline {
		state.Members[member.ID] = member
		receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{}, LatestSequence: state.Room.LatestSequence}
		rememberTransient(state, input.RequestID, requestHash, receipt)
		s.store.mu.Unlock()
		return receipt, nil
	} else {
		member.Status = MemberOnline
		if member.Agent.Status == AgentOffline {
			member.Agent.Status = AgentIdle
		}
	}
	typeName := "member.online"
	if leave {
		typeName = "member.left"
	}
	sequence := state.Room.LatestSequence + 1
	payload, _ := json.Marshal(member)
	event := RoomEvent{EventID: newID("event"), Sequence: sequence, Room: input.Room, Type: typeName, ActorID: input.MemberID, RequestID: input.RequestID, CreatedAt: now, Payload: payload}
	receipt := CommandReceipt{RequestID: input.RequestID, EventIDs: []string{event.EventID}, LatestSequence: sequence}
	record := journalRecord{Event: event, Receipt: receipt, RequestHash: requestHash, Member: &member, SessionHash: hashSecret(input.Session), Leave: leave, Heartbeat: !leave}
	if err := s.store.append(record); err != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, err
	}
	s.store.mu.Unlock()
	s.hub.Publish(input.Room)
	return receipt, nil
}

func (s *Service) Snapshot(ctx context.Context, room, session string) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	state, ok := s.store.room(strings.TrimSpace(room))
	if !ok {
		return Snapshot{}, fail(CodeNotFound, "room does not exist")
	}
	if !s.anySessionLocked(state, session) {
		return Snapshot{}, fail(CodeUnauthorized, "connection session is invalid")
	}
	return snapshotOf(state), nil
}

func (s *Service) Events(ctx context.Context, room, session string, afterSequence uint64) ([]RoomEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.store.mu.RLock()
	defer s.store.mu.RUnlock()
	state, ok := s.store.room(strings.TrimSpace(room))
	if !ok {
		return nil, fail(CodeNotFound, "room does not exist")
	}
	if !s.anySessionLocked(state, session) {
		return nil, fail(CodeUnauthorized, "connection session is invalid")
	}
	return eventsAfter(state, afterSequence), nil
}

func (s *Service) Submit(ctx context.Context, env CommandEnvelope) (CommandReceipt, error) {
	if err := ctx.Err(); err != nil {
		return CommandReceipt{}, err
	}
	env.RequestID, env.Room, env.MemberID = strings.TrimSpace(env.RequestID), strings.TrimSpace(env.Room), strings.TrimSpace(env.MemberID)
	if !validID(env.RequestID) || !roomIDPattern.MatchString(env.Room) || !validID(env.MemberID) || env.Session == "" {
		return CommandReceipt{}, fail(CodeInvalid, "requestId, room, memberId, and connectionSession are required")
	}
	now := s.now().UTC()
	requestHash := fingerprint(struct {
		RequestID, Room, MemberID string
		Command                   Command
	}{env.RequestID, env.Room, env.MemberID, env.Command})
	s.store.mu.Lock()
	state, ok := s.store.room(env.Room)
	if !ok {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeNotFound, "room does not exist")
	}
	if !s.authLocked(state, env.MemberID, env.Session) {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeUnauthorized, "connection session is invalid")
	}
	if receipt, duplicate, requestErr := replayRequest(state, env.RequestID, requestHash); requestErr != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, requestErr
	} else if duplicate {
		s.store.mu.Unlock()
		return receipt, nil
	}
	if !validCommandUnion(env.Command) {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeInvalid, "command must contain exactly one payload matching its type")
	}
	if len(state.Timeline) >= MaxTimelineItems {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeConflict, "room timeline limit reached")
	}
	sequence := state.Room.LatestSequence + 1
	item, eventTypeName, causation, err := s.buildTimeline(state, env.MemberID, sequence, now, env.Command)
	if err != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, err
	}
	payload, err := json.Marshal(item)
	if err != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, fmt.Errorf("collab: encode timeline item: %w", err)
	}
	if len(payload) > MaxTextBytes*2 {
		s.store.mu.Unlock()
		return CommandReceipt{}, fail(CodeInvalid, "command payload exceeds size limit")
	}
	event := RoomEvent{EventID: newID("event"), Sequence: sequence, Room: env.Room, Type: eventTypeName, ActorID: env.MemberID, RequestID: env.RequestID, CausationID: causation, CreatedAt: now, Payload: payload}
	receipt := CommandReceipt{RequestID: env.RequestID, EventIDs: []string{event.EventID}, LatestSequence: sequence}
	if err := s.store.append(journalRecord{Event: event, Receipt: receipt, RequestHash: requestHash, Timeline: &item}); err != nil {
		s.store.mu.Unlock()
		return CommandReceipt{}, err
	}
	s.store.mu.Unlock()
	s.hub.Publish(env.Room)
	return receipt, nil
}

func (s *Service) buildTimeline(state *roomState, actor string, sequence uint64, now time.Time, cmd Command) (TimelineItem, string, string, error) {
	switch cmd.Type {
	case CommandPostChat:
		if cmd.Chat == nil || blankOrLarge(cmd.Chat.Text) {
			return TimelineItem{}, "", "", fail(CodeInvalid, "non-empty chat text is required")
		}
		id := newID("message")
		return TimelineItem{ID: id, Sequence: sequence, Type: TimelineChat, Chat: &ChatMessage{ID: id, AuthorID: actor, Text: cmd.Chat.Text, Revision: 1, CreatedAt: now}}, "chat.posted", "", nil
	case CommandPublishContribution:
		v := cmd.Contribution
		if v == nil || blankOrLarge(v.Body) || len(v.Title) > MaxShortText || !validContribution(v.Kind) || !validStringList(v.Scope, MaxReferences, MaxShortText) || !validStringList(v.TargetIDs, MaxMembers, MaxIDBytes) || !validStringList(v.Dependencies, MaxReferences, MaxIDBytes) || !validMetadata(v.Metadata) {
			return TimelineItem{}, "", "", fail(CodeInvalid, "invalid contribution")
		}
		for _, target := range v.TargetIDs {
			if _, ok := state.Members[target]; !ok {
				return TimelineItem{}, "", "", fail(CodeNotFound, "contribution target member does not exist")
			}
		}
		if v.RelatedItem != "" && (!validID(v.RelatedItem) || !timelineExists(state, v.RelatedItem)) {
			return TimelineItem{}, "", "", fail(CodeNotFound, "contribution related item does not exist")
		}
		if !referencesExist(state, v.Dependencies) {
			return TimelineItem{}, "", "", fail(CodeNotFound, "one or more contribution dependencies do not exist")
		}
		id, revision := newID("contribution"), v.Revision
		if revision == 0 {
			revision = 1
		}
		value := Contribution{ID: id, AuthorID: actor, Kind: v.Kind, Title: v.Title, Body: v.Body, Scope: cloneStrings(v.Scope), TargetIDs: cloneStrings(v.TargetIDs), RelatedItem: v.RelatedItem, Dependencies: cloneStrings(v.Dependencies), ActionNeeded: v.ActionNeeded, Revision: revision, Metadata: cloneMap(v.Metadata), CreatedAt: now}
		return TimelineItem{ID: id, Sequence: sequence, Type: TimelineContribution, Contribution: &value}, "contribution.published", "", nil
	case CommandAddReaction:
		v := cmd.Reaction
		if v == nil || !validID(v.TargetID) || strings.TrimSpace(v.Kind) == "" || len(v.Kind) > 64 || !timelineExists(state, v.TargetID) {
			return TimelineItem{}, "", "", fail(CodeInvalid, "valid reaction target and kind are required")
		}
		id := newID("reaction")
		value := Reaction{ID: id, AuthorID: actor, TargetID: v.TargetID, Kind: v.Kind, CreatedAt: now}
		return TimelineItem{ID: id, Sequence: sequence, Type: TimelineReaction, Reaction: &value}, "reaction.added", v.TargetID, nil
	case CommandCreateAgentRequest:
		v := cmd.AgentRequest
		if v == nil || blankOrLarge(v.Instruction) || !validID(v.TargetMemberID) || v.TargetMemberID == actor || !validStringList(v.ReferenceIDs, MaxReferences, MaxIDBytes) {
			return TimelineItem{}, "", "", fail(CodeInvalid, "request must target another member and include an instruction")
		}
		if _, ok := state.Members[v.TargetMemberID]; !ok {
			return TimelineItem{}, "", "", fail(CodeNotFound, "target member does not exist")
		}
		if !referencesExist(state, v.ReferenceIDs) {
			return TimelineItem{}, "", "", fail(CodeNotFound, "one or more references do not exist")
		}
		id := newID("agent-request")
		value := AgentRequest{ID: id, AuthorID: actor, TargetMemberID: v.TargetMemberID, Instruction: v.Instruction, ReferenceIDs: cloneStrings(v.ReferenceIDs), Status: RequestPending, CreatedAt: now, UpdatedAt: now}
		return TimelineItem{ID: id, Sequence: sequence, Type: TimelineAgentRequest, AgentRequest: &value}, "agent_request.created", "", nil
	case CommandDecideAgentRequest:
		v := cmd.RequestDecision
		if v == nil || !validID(v.AgentRequestID) || len(v.Note) > MaxTextBytes || (v.Decision != RequestAccepted && v.Decision != RequestRejected) {
			return TimelineItem{}, "", "", fail(CodeInvalid, "decision must be accepted or rejected")
		}
		request, ok := state.Requests[v.AgentRequestID]
		if !ok {
			return TimelineItem{}, "", "", fail(CodeNotFound, "agent request does not exist")
		}
		if request.TargetMemberID != actor {
			return TimelineItem{}, "", "", fail(CodeForbidden, "only the target member can decide an agent request")
		}
		if request.Status != RequestPending && request.Status != v.Decision {
			return TimelineItem{}, "", "", fail(CodeConflict, "agent request already has a different decision")
		}
		request.Status, request.DecisionBy, request.DecisionNote, request.UpdatedAt = v.Decision, actor, v.Note, now
		return TimelineItem{ID: request.ID, Sequence: sequence, Type: TimelineAgentRequest, AgentRequest: &request}, "agent_request.decided", request.ID, nil
	case CommandPublishAgentRun:
		v := cmd.AgentRun
		member := state.Members[actor]
		if v == nil || !validID(v.RunID) || !validID(v.CommandID) || !validID(v.AgentID) || v.AgentID != member.Agent.ID || !validRunStatus(v.Status) || !validStringList(v.ReferenceIDs, MaxReferences, MaxIDBytes) || len(v.Instruction) > MaxTextBytes || len(v.Summary) > MaxTextBytes || len(v.Error) > MaxTextBytes {
			return TimelineItem{}, "", "", fail(CodeForbidden, "agent run must belong to the current member's agent")
		}
		if !referencesExist(state, v.ReferenceIDs) {
			return TimelineItem{}, "", "", fail(CodeNotFound, "one or more agent run references do not exist")
		}
		if v.RequestRef != "" {
			request, ok := state.Requests[v.RequestRef]
			if !ok || request.TargetMemberID != actor || request.Status != RequestAccepted {
				return TimelineItem{}, "", "", fail(CodeForbidden, "agent request is not accepted by this member")
			}
		}
		if old, exists := state.Runs[v.RunID]; exists {
			if old.OwnerID != actor || old.AgentID != v.AgentID {
				return TimelineItem{}, "", "", fail(CodeForbidden, "agent run belongs to another member")
			}
			if !canTransition(old.Status, v.Status) {
				return TimelineItem{}, "", "", fail(CodeConflict, "invalid agent run status transition")
			}
			if old.CommandID != v.CommandID || old.RequestRef != v.RequestRef || old.Instruction != v.Instruction || !slices.Equal(old.ReferenceIDs, v.ReferenceIDs) {
				return TimelineItem{}, "", "", fail(CodeConflict, "agent run identity fields are immutable")
			}
		}
		value := AgentRun{ID: v.RunID, OwnerID: actor, AgentID: v.AgentID, CommandID: v.CommandID, RequestRef: v.RequestRef, Instruction: v.Instruction, ReferenceIDs: cloneStrings(v.ReferenceIDs), Status: v.Status, Summary: v.Summary, Error: v.Error, UpdatedAt: now}
		return TimelineItem{ID: v.RunID, Sequence: sequence, Type: TimelineAgentRun, AgentRun: &value}, "agent_run.published", v.RequestRef, nil
	case CommandPublishAgentResult:
		v := cmd.AgentResult
		member := state.Members[actor]
		if v == nil || !validID(v.ResultID) || !validID(v.RunID) || !validID(v.AgentID) || v.AgentID != member.Agent.ID || blankOrLarge(v.Summary) || !validStringList(v.ReferenceIDs, MaxReferences, MaxIDBytes) {
			return TimelineItem{}, "", "", fail(CodeForbidden, "agent result must belong to the current member's agent")
		}
		if !referencesExist(state, v.ReferenceIDs) {
			return TimelineItem{}, "", "", fail(CodeNotFound, "one or more agent result references do not exist")
		}
		run, ok := state.Runs[v.RunID]
		if !ok || run.OwnerID != actor || run.AgentID != v.AgentID {
			return TimelineItem{}, "", "", fail(CodeForbidden, "agent result run belongs to another member")
		}
		revision := v.Revision
		if revision == 0 {
			revision = 1
		}
		if old, exists := state.Results[v.ResultID]; exists {
			if old.OwnerID != actor || old.AgentID != v.AgentID || old.RunID != v.RunID {
				return TimelineItem{}, "", "", fail(CodeForbidden, "agent result belongs to another member or run")
			}
			if revision <= old.Revision {
				return TimelineItem{}, "", "", fail(CodeConflict, "agent result revision cannot go backward")
			}
		}
		if _, exists := state.ResultKeys[resultKey(v.RunID, revision)]; exists {
			return TimelineItem{}, "", "", fail(CodeConflict, "agent result revision already exists")
		}
		value := AgentResult{ID: v.ResultID, OwnerID: actor, AgentID: v.AgentID, RunID: v.RunID, Revision: revision, Summary: v.Summary, ReferenceIDs: cloneStrings(v.ReferenceIDs), CreatedAt: now}
		return TimelineItem{ID: v.ResultID, Sequence: sequence, Type: TimelineAgentResult, AgentResult: &value}, "agent_result.published", v.RunID, nil
	default:
		return TimelineItem{}, "", "", fail(CodeInvalid, "unsupported command type")
	}
}

func (s *Service) deriveSession(room, member, request string) string {
	mac := hmac.New(sha256.New, s.store.key)
	_, _ = mac.Write([]byte(room + "\x00" + member + "\x00" + request))
	return "cs1." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) authLocked(state *roomState, memberID, session string) bool {
	member, ok := state.Members[memberID]
	if !ok || member.Status != MemberOnline {
		return false
	}
	return s.sessionMatchesLocked(state, memberID, session)
}

func (s *Service) sessionMatchesLocked(state *roomState, memberID, session string) bool {
	want := hashSecret(session)
	for sessionHash, owner := range state.Sessions {
		if owner == memberID && subtle.ConstantTimeCompare([]byte(sessionHash), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

func (s *Service) anySessionLocked(state *roomState, session string) bool {
	want := hashSecret(session)
	for sessionHash, memberID := range state.Sessions {
		member := state.Members[memberID]
		if member.Status == MemberOnline && subtle.ConstantTimeCompare([]byte(sessionHash), []byte(want)) == 1 {
			return true
		}
	}
	return false
}

func hashSecret(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}
func secretEqual(storedHash, value string) bool {
	actual := hashSecret(value)
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(actual)) == 1
}

func fingerprint(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic("collab: fingerprint JSON: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func fingerprintEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func replayRequest(state *roomState, requestID, requestHash string) (CommandReceipt, bool, error) {
	if receipt, ok := state.Receipts[requestID]; ok {
		if !fingerprintEqual(state.Fingerprints[requestID], requestHash) {
			return CommandReceipt{}, false, fail(CodeConflict, "requestId was already used with different input")
		}
		receipt = cloneReceipt(receipt)
		receipt.Duplicate = true
		return receipt, true, nil
	}
	if value, ok := state.Transient[requestID]; ok {
		if !fingerprintEqual(value.Fingerprint, requestHash) {
			return CommandReceipt{}, false, fail(CodeConflict, "requestId was already used with different input")
		}
		receipt := cloneReceipt(value.Receipt)
		receipt.Duplicate = true
		return receipt, true, nil
	}
	return CommandReceipt{}, false, nil
}

func rememberTransient(state *roomState, requestID, requestHash string, receipt CommandReceipt) {
	if _, exists := state.Transient[requestID]; exists {
		return
	}
	if len(state.TransientIDs) >= MaxTransientRequests {
		oldest := state.TransientIDs[0]
		state.TransientIDs = state.TransientIDs[1:]
		delete(state.Transient, oldest)
	}
	state.Transient[requestID] = transientRequest{Fingerprint: requestHash, Receipt: cloneReceipt(receipt)}
	state.TransientIDs = append(state.TransientIDs, requestID)
}

func newID(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// Event IDs are identifiers, not credentials. The host key/session path
		// still fails closed if cryptographic randomness is unavailable.
		return fmt.Sprintf("%s_fallback_%d_%d", prefix, time.Now().UnixNano(), fallbackID.Add(1))
	}
	return prefix + "_" + hex.EncodeToString(b)
}
func blankOrLarge(value string) bool {
	return strings.TrimSpace(value) == "" || len(value) > MaxTextBytes
}

func validID(value string) bool { return strings.TrimSpace(value) != "" && len(value) <= MaxIDBytes }

func validStringList(values []string, maxItems, maxItemBytes int) bool {
	if len(values) > maxItems {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == "" || len(value) > maxItemBytes {
			return false
		}
	}
	return true
}

func validMetadata(values map[string]string) bool {
	if len(values) > MaxMetadata {
		return false
	}
	total := 0
	for key, value := range values {
		if strings.TrimSpace(key) == "" || len(key) > MaxShortText || len(value) > MaxTextBytes {
			return false
		}
		total += len(key) + len(value)
		if total > MaxTextBytes {
			return false
		}
	}
	return true
}
func cloneStrings(v []string) []string { return append([]string(nil), v...) }
func cloneMap(v map[string]string) map[string]string {
	if v == nil {
		return nil
	}
	out := make(map[string]string, len(v))
	for k, value := range v {
		out[k] = value
	}
	return out
}
func validContribution(v ContributionKind) bool {
	switch v {
	case ContributionProposal, ContributionDecision, ContributionDeliverable, ContributionIssue, ContributionFixReady, ContributionVerified, ContributionQuestion:
		return true
	}
	return false
}
func validRunStatus(v AgentRunStatus) bool {
	switch v {
	case RunQueued, RunRunning, RunWaitingApproval, RunCompleted, RunFailed, RunCancelled, RunInterrupted:
		return true
	}
	return false
}

func validCommandUnion(cmd Command) bool {
	count := 0
	for _, set := range []bool{cmd.Chat != nil, cmd.Contribution != nil, cmd.Reaction != nil, cmd.AgentRequest != nil, cmd.RequestDecision != nil, cmd.AgentRun != nil, cmd.AgentResult != nil} {
		if set {
			count++
		}
	}
	if count != 1 {
		return false
	}
	switch cmd.Type {
	case CommandPostChat:
		return cmd.Chat != nil
	case CommandPublishContribution:
		return cmd.Contribution != nil
	case CommandAddReaction:
		return cmd.Reaction != nil
	case CommandCreateAgentRequest:
		return cmd.AgentRequest != nil
	case CommandDecideAgentRequest:
		return cmd.RequestDecision != nil
	case CommandPublishAgentRun:
		return cmd.AgentRun != nil
	case CommandPublishAgentResult:
		return cmd.AgentResult != nil
	default:
		return false
	}
}
func terminal(v AgentRunStatus) bool {
	return v == RunCompleted || v == RunFailed || v == RunCancelled || v == RunInterrupted
}
func canTransition(from, to AgentRunStatus) bool {
	if from == to {
		return true
	}
	if terminal(from) {
		return false
	}
	switch from {
	case RunQueued:
		return to == RunRunning || terminal(to)
	case RunRunning:
		return to == RunWaitingApproval || terminal(to)
	case RunWaitingApproval:
		return to == RunRunning || terminal(to)
	}
	return false
}
func timelineExists(state *roomState, id string) bool {
	for _, item := range state.Timeline {
		if item.ID == id {
			return true
		}
	}
	return false
}
func referencesExist(state *roomState, ids []string) bool {
	for _, id := range ids {
		if !timelineExists(state, id) {
			return false
		}
	}
	return true
}
func eventType(state *roomState, receipt CommandReceipt) string {
	if len(receipt.EventIDs) == 0 {
		return ""
	}
	for _, event := range state.Events {
		if event.EventID == receipt.EventIDs[0] {
			return event.Type
		}
	}
	return ""
}

func resultKey(runID string, revision uint64) string { return fmt.Sprintf("%s/%d", runID, revision) }
