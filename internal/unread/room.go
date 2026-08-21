package unread

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"workground2/internal/collab"
)

type RoomInput struct {
	ConversationKey string
	SessionID       string
	Title           string
	LocalMemberID   string
	LocalAgentID    string
	Snapshot        collab.Snapshot
	ObservedAt      time.Time
	Read            bool
}

// ObserveRoom projects a complete authoritative Room snapshot. The first
// observation establishes an already-read baseline; later snapshots add only
// eligible remote timeline items above the stored waterline.
func (s *Store) ObserveRoom(input RoomInput) (Conversation, error) {
	if s == nil {
		return Conversation{}, errors.New("unread store is unavailable")
	}
	key, err := prefixedKey(SourceRoom, input.ConversationKey)
	if err != nil {
		return Conversation{}, err
	}
	localMemberID := strings.TrimSpace(input.LocalMemberID)
	if localMemberID == "" {
		return Conversation{}, errors.New("Room local member ID is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Conversations[key]
	if current == nil {
		next := cloneState(s.state)
		conversation := &conversationState{
			Key:            key,
			Source:         SourceRoom,
			SessionID:      strings.TrimSpace(input.SessionID),
			Title:          strings.TrimSpace(input.Title),
			LatestSequence: input.Snapshot.LatestSequence,
			ReadSequence:   input.Snapshot.LatestSequence,
			Seen:           map[string]uint64{},
		}
		next.Conversations[key] = conversation
		next.Revision++
		if err := s.persist(next); err != nil {
			return Conversation{}, err
		}
		s.state = next
		return projectConversation(conversation), nil
	}

	next := cloneState(s.state)
	conversation := next.Conversations[key]
	metadataChanged := false
	if value := strings.TrimSpace(input.SessionID); value != "" && value != conversation.SessionID {
		conversation.SessionID = value
		metadataChanged = true
	}
	if value := strings.TrimSpace(input.Title); value != "" && value != conversation.Title {
		conversation.Title = value
		metadataChanged = true
	}
	if input.Snapshot.LatestSequence <= conversation.LatestSequence {
		if !metadataChanged {
			return projectConversation(current), nil
		}
		next.Revision++
		if err := s.persist(next); err != nil {
			return projectConversation(current), err
		}
		s.state = next
		return projectConversation(conversation), nil
	}

	timeline := append([]collab.TimelineItem(nil), input.Snapshot.Timeline...)
	sort.Slice(timeline, func(i, j int) bool { return timeline[i].Sequence < timeline[j].Sequence })
	for _, value := range timeline {
		if value.Sequence <= conversation.LatestSequence || value.Sequence > input.Snapshot.LatestSequence {
			continue
		}
		item, eligible := roomUnreadItem(value, localMemberID, strings.TrimSpace(input.LocalAgentID), input.ObservedAt)
		if !eligible || item.Sequence <= conversation.ReadSequence {
			continue
		}
		conversation.Pending = upsertPending(conversation.Pending, item)
	}
	conversation.LatestSequence = input.Snapshot.LatestSequence
	if input.Read {
		conversation.ReadSequence = conversation.LatestSequence
		conversation.Pending = nil
	}
	next.Revision++
	if err := s.persist(next); err != nil {
		return projectConversation(current), err
	}
	s.state = next
	return projectConversation(conversation), nil
}

func upsertPending(items []Item, item Item) []Item {
	for i := range items {
		if items[i].ID == item.ID && items[i].Kind == item.Kind {
			items[i] = item
			sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
			return items
		}
	}
	items = append(items, item)
	sort.Slice(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	return items
}

func roomUnreadItem(value collab.TimelineItem, localMemberID, localAgentID string, fallback time.Time) (Item, bool) {
	item := Item{ID: value.ID, Sequence: value.Sequence, Kind: string(value.Type), Priority: PriorityNormal, OccurredAt: fallback.UTC()}
	if item.ID == "" {
		item.ID = string(value.Type) + ":" + strconv.FormatUint(value.Sequence, 10)
	}
	switch value.Type {
	case collab.TimelineChat:
		if value.Chat == nil || value.Chat.AuthorID == localMemberID {
			return Item{}, false
		}
		item.AuthorID, item.OccurredAt = value.Chat.AuthorID, value.Chat.CreatedAt
		memberMentioned := contains(value.Chat.MentionMemberIDs, localMemberID)
		agentMentioned := contains(value.Chat.MentionAgentIDs, localAgentID)
		switch {
		case memberMentioned && agentMentioned:
			item.Attention = AttentionMentionBoth
		case memberMentioned:
			item.Attention = AttentionMentionMember
		case agentMentioned:
			item.Attention = AttentionMentionAgent
		}
		if item.Attention != AttentionNone {
			item.Priority = PriorityHigh
		}
	case collab.TimelineContribution:
		if value.Contribution == nil || value.Contribution.AuthorID == localMemberID {
			return Item{}, false
		}
		item.AuthorID, item.OccurredAt = value.Contribution.AuthorID, value.Contribution.CreatedAt
		if value.Contribution.ActionNeeded || contains(value.Contribution.TargetIDs, localMemberID) || contains(value.Contribution.TargetIDs, localAgentID) {
			item.Priority = PriorityHigh
		}
	case collab.TimelineAgentRequest:
		if value.AgentRequest == nil || value.AgentRequest.AuthorID == localMemberID {
			return Item{}, false
		}
		item.AuthorID, item.OccurredAt = value.AgentRequest.AuthorID, value.AgentRequest.CreatedAt
		if value.AgentRequest.TargetMemberID == localMemberID {
			item.Priority = PriorityHigh
		}
	case collab.TimelineAgentResult:
		if value.AgentResult == nil || value.AgentResult.OwnerID == localMemberID {
			return Item{}, false
		}
		item.AuthorID, item.OccurredAt = value.AgentResult.OwnerID, value.AgentResult.CreatedAt
		for _, handoff := range value.AgentResult.Handoffs {
			if handoff.TargetAgentID == localAgentID && handoff.RequiresResponse {
				item.Priority = PriorityHigh
				break
			}
		}
	case collab.TimelineFile:
		if value.File == nil || value.File.OwnerID == localMemberID {
			return Item{}, false
		}
		item.AuthorID, item.OccurredAt = value.File.OwnerID, value.File.CreatedAt
	default:
		return Item{}, false
	}
	if item.OccurredAt.IsZero() {
		item.OccurredAt = fallback.UTC()
	}
	return item, true
}

func contains(values []string, target string) bool {
	if target == "" {
		return false
	}
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
