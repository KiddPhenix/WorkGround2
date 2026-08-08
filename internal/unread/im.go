package unread

import (
	"errors"
	"strings"
	"time"
)

type IMInput struct {
	ConversationKey string
	MessageID       string
	SessionID       string
	Title           string
	AuthorID        string
	ReceivedAt      time.Time
	Read            bool
}

type IMReceipt struct {
	ConversationKey string
	Sequence        uint64
	Duplicate       bool
}

// AcceptIM durably deduplicates and records one authorized, non-self inbound
// message before any queueing or Agent work begins.
func (s *Store) AcceptIM(input IMInput) (IMReceipt, error) {
	if s == nil {
		return IMReceipt{}, errors.New("unread store is unavailable")
	}
	key, err := prefixedKey(SourceIM, input.ConversationKey)
	if err != nil {
		return IMReceipt{}, err
	}
	messageID := strings.TrimSpace(input.MessageID)
	if messageID == "" {
		return IMReceipt{}, errors.New("IM message ID is required for durable deduplication")
	}
	at := input.ReceivedAt.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Conversations[key]
	if current != nil {
		if sequence, ok := current.Seen[messageID]; ok {
			return IMReceipt{ConversationKey: key, Sequence: sequence, Duplicate: true}, nil
		}
	}
	next := cloneState(s.state)
	conversation := next.Conversations[key]
	if conversation == nil {
		conversation = &conversationState{Key: key, Source: SourceIM, Seen: map[string]uint64{}}
		next.Conversations[key] = conversation
	}
	conversation.LatestSequence++
	conversation.SessionID = firstNonEmpty(strings.TrimSpace(input.SessionID), conversation.SessionID)
	conversation.Title = firstNonEmpty(strings.TrimSpace(input.Title), conversation.Title)
	conversation.Seen[messageID] = conversation.LatestSequence
	if input.Read {
		conversation.ReadSequence = conversation.LatestSequence
		conversation.Pending = nil
	} else {
		conversation.Pending = append(conversation.Pending, Item{
			ID:         messageID,
			Sequence:   conversation.LatestSequence,
			Kind:       "message",
			Priority:   PriorityNormal,
			AuthorID:   strings.TrimSpace(input.AuthorID),
			OccurredAt: at,
		})
	}
	next.Revision++
	if err := s.persist(next); err != nil {
		return IMReceipt{}, err
	}
	s.state = next
	return IMReceipt{ConversationKey: key, Sequence: conversation.LatestSequence}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
