package unread

import (
	"errors"
	"strings"
	"time"
)

// AttentionInput records one durable Session or Work condition that merits
// the user's attention. EventID must be stable across replay and resync.
type AttentionInput struct {
	Source          Source
	ConversationKey string
	EventID         string
	SessionID       string
	Title           string
	Kind            string
	Priority        Priority
	OccurredAt      time.Time
	Read            bool
}

type AttentionReceipt struct {
	ConversationKey string
	Sequence        uint64
	Duplicate       bool
}

// AcceptAttention durably deduplicates one Session/Work attention event. Read
// events still leave a receipt, preventing a later replay from resurrecting an
// unread that was first observed while its target was visible.
func (s *Store) AcceptAttention(input AttentionInput) (AttentionReceipt, error) {
	if s == nil {
		return AttentionReceipt{}, errors.New("unread store is unavailable")
	}
	if input.Source != SourceSession && input.Source != SourceWork {
		return AttentionReceipt{}, errors.New("attention source must be session or work")
	}
	key, err := prefixedKey(input.Source, input.ConversationKey)
	if err != nil {
		return AttentionReceipt{}, err
	}
	eventID := strings.TrimSpace(input.EventID)
	if eventID == "" {
		return AttentionReceipt{}, errors.New("attention event ID is required for durable deduplication")
	}
	kind := strings.TrimSpace(input.Kind)
	if kind == "" {
		return AttentionReceipt{}, errors.New("attention kind is required")
	}
	priority := input.Priority
	if priority == "" {
		priority = PriorityNormal
	}
	if priority != PriorityNormal && priority != PriorityHigh {
		return AttentionReceipt{}, errors.New("attention priority is invalid")
	}
	at := input.OccurredAt.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Conversations[key]
	if current != nil {
		if sequence, ok := current.Seen[eventID]; ok {
			return AttentionReceipt{ConversationKey: key, Sequence: sequence, Duplicate: true}, nil
		}
	}
	next := cloneState(s.state)
	conversation := next.Conversations[key]
	if conversation == nil {
		conversation = &conversationState{Key: key, Source: input.Source, Seen: map[string]uint64{}}
		next.Conversations[key] = conversation
	}
	conversation.LatestSequence++
	conversation.SessionID = firstNonEmpty(strings.TrimSpace(input.SessionID), conversation.SessionID)
	conversation.Title = firstNonEmpty(strings.TrimSpace(input.Title), conversation.Title)
	conversation.Seen[eventID] = conversation.LatestSequence
	if input.Read {
		conversation.ReadSequence = conversation.LatestSequence
		conversation.Pending = nil
	} else {
		conversation.Pending = append(conversation.Pending, Item{
			ID:         eventID,
			Sequence:   conversation.LatestSequence,
			Kind:       kind,
			Priority:   priority,
			OccurredAt: at,
		})
	}
	next.Revision++
	if err := s.persist(next); err != nil {
		return AttentionReceipt{}, err
	}
	s.state = next
	return AttentionReceipt{ConversationKey: key, Sequence: conversation.LatestSequence}, nil
}
