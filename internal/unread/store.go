package unread

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

const stateVersion = 1

type Source string

const (
	SourceRoom    Source = "room"
	SourceIM      Source = "im"
	SourceSession Source = "session"
	SourceWork    Source = "work"
)

type Priority string

const (
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
)

// Item is the minimum durable identity needed to count and navigate an unread
// event. Full payloads intentionally remain in their authoritative source.
type Item struct {
	ID         string    `json:"id"`
	Sequence   uint64    `json:"sequence"`
	Kind       string    `json:"kind"`
	Priority   Priority  `json:"priority"`
	AuthorID   string    `json:"authorId,omitempty"`
	OccurredAt time.Time `json:"occurredAt"`
}

// Conversation is a read-only projection returned to callers.
type Conversation struct {
	Key               string    `json:"key"`
	Source            Source    `json:"source"`
	SessionID         string    `json:"sessionId,omitempty"`
	Title             string    `json:"title,omitempty"`
	LatestSequence    uint64    `json:"latestSequence"`
	ReadSequence      uint64    `json:"readSequence"`
	UnreadCount       int       `json:"unreadCount"`
	HighPriorityCount int       `json:"highPriorityCount"`
	LastUnreadAt      time.Time `json:"lastUnreadAt,omitempty"`
	Items             []Item    `json:"items,omitempty"`
}

// Summary is the source of truth later presentation layers can render.
type Summary struct {
	Revision          uint64         `json:"revision"`
	TotalUnread       int            `json:"totalUnread"`
	HighPriorityCount int            `json:"highPriorityCount"`
	Conversations     []Conversation `json:"conversations"`
}

type conversationState struct {
	Key            string            `json:"key"`
	Source         Source            `json:"source"`
	SessionID      string            `json:"sessionId,omitempty"`
	Title          string            `json:"title,omitempty"`
	LatestSequence uint64            `json:"latestSequence"`
	ReadSequence   uint64            `json:"readSequence"`
	Pending        []Item            `json:"pending,omitempty"`
	Seen           map[string]uint64 `json:"seen,omitempty"`
}

type diskState struct {
	Version       int                           `json:"version"`
	Revision      uint64                        `json:"revision"`
	Conversations map[string]*conversationState `json:"conversations"`
}

type writeFileFunc func(string, []byte, os.FileMode) error

// Store serializes every mutation and only publishes it in memory after the
// matching snapshot has been atomically persisted.
type Store struct {
	mu        sync.RWMutex
	path      string
	state     diskState
	writeFile writeFileFunc
}

// Open loads or creates a durable unread store at path.
func Open(path string) (*Store, error) {
	return open(path, fileutil.AtomicWriteFile)
}

func open(path string, writeFile writeFileFunc) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("unread store path is required")
	}
	if writeFile == nil {
		return nil, errors.New("unread store writer is required")
	}
	state := newDiskState()
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if len(strings.TrimSpace(string(raw))) == 0 {
			return nil, errors.New("unread store is empty")
		}
		if err := json.Unmarshal(raw, &state); err != nil {
			return nil, fmt.Errorf("decode unread store: %w", err)
		}
		if err := validateState(&state); err != nil {
			return nil, err
		}
	case os.IsNotExist(err):
	case err != nil:
		return nil, fmt.Errorf("read unread store: %w", err)
	}
	return &Store{path: path, state: state, writeFile: writeFile}, nil
}

func newDiskState() diskState {
	return diskState{Version: stateVersion, Conversations: map[string]*conversationState{}}
}

func validateState(state *diskState) error {
	if state.Version != stateVersion {
		return fmt.Errorf("unsupported unread store version %d", state.Version)
	}
	if state.Conversations == nil {
		state.Conversations = map[string]*conversationState{}
	}
	for key, conversation := range state.Conversations {
		if conversation == nil || strings.TrimSpace(key) == "" || conversation.Key != key {
			return fmt.Errorf("invalid unread conversation %q", key)
		}
		if !conversation.Source.valid() {
			return fmt.Errorf("invalid unread source %q", conversation.Source)
		}
		if conversation.ReadSequence > conversation.LatestSequence {
			return fmt.Errorf("unread conversation %q has read sequence beyond latest", key)
		}
		if conversation.Seen == nil {
			conversation.Seen = map[string]uint64{}
		}
		for id, sequence := range conversation.Seen {
			if strings.TrimSpace(id) == "" || sequence == 0 || sequence > conversation.LatestSequence {
				return fmt.Errorf("unread conversation %q has invalid event receipt", key)
			}
		}
		var previous uint64
		for i, item := range conversation.Pending {
			if strings.TrimSpace(item.ID) == "" || strings.TrimSpace(item.Kind) == "" || (item.Priority != PriorityNormal && item.Priority != PriorityHigh) || item.Sequence == 0 || item.Sequence > conversation.LatestSequence || (i > 0 && item.Sequence <= previous) {
				return fmt.Errorf("unread conversation %q has invalid pending sequence", key)
			}
			if item.Sequence <= conversation.ReadSequence {
				return fmt.Errorf("unread conversation %q retains an already-read item", key)
			}
			previous = item.Sequence
		}
	}
	return nil
}

func (s Source) valid() bool {
	switch s {
	case SourceRoom, SourceIM, SourceSession, SourceWork:
		return true
	default:
		return false
	}
}

// Summary returns a detached, stable snapshot sorted by newest unread activity.
func (s *Store) Summary() Summary {
	if s == nil {
		return Summary{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := Summary{Revision: s.state.Revision, Conversations: make([]Conversation, 0, len(s.state.Conversations))}
	for _, state := range s.state.Conversations {
		conversation := projectConversation(state)
		out.TotalUnread += conversation.UnreadCount
		out.HighPriorityCount += conversation.HighPriorityCount
		out.Conversations = append(out.Conversations, conversation)
	}
	sort.Slice(out.Conversations, func(i, j int) bool {
		left, right := out.Conversations[i], out.Conversations[j]
		if !left.LastUnreadAt.Equal(right.LastUnreadAt) {
			return left.LastUnreadAt.After(right.LastUnreadAt)
		}
		return left.Key < right.Key
	})
	return out
}

func projectConversation(state *conversationState) Conversation {
	out := Conversation{
		Key:            state.Key,
		Source:         state.Source,
		SessionID:      state.SessionID,
		Title:          state.Title,
		LatestSequence: state.LatestSequence,
		ReadSequence:   state.ReadSequence,
		Items:          append([]Item(nil), state.Pending...),
	}
	out.UnreadCount = len(out.Items)
	for _, item := range out.Items {
		if item.Priority == PriorityHigh {
			out.HighPriorityCount++
		}
		if item.OccurredAt.After(out.LastUnreadAt) {
			out.LastUnreadAt = item.OccurredAt
		}
	}
	return out
}

// MarkRead monotonically advances one conversation's read cursor. upTo is
// clamped to the latest observed sequence, making duplicate and late calls safe.
func (s *Store) MarkRead(key string, upTo uint64) (Conversation, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Conversation{}, errors.New("unread conversation key is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Conversations[key]
	if current == nil {
		return Conversation{}, fmt.Errorf("unread conversation %q does not exist", key)
	}
	if upTo > current.LatestSequence {
		upTo = current.LatestSequence
	}
	if upTo <= current.ReadSequence {
		return projectConversation(current), nil
	}
	next := cloneState(s.state)
	conversation := next.Conversations[key]
	conversation.ReadSequence = upTo
	kept := conversation.Pending[:0]
	for _, item := range conversation.Pending {
		if item.Sequence > upTo {
			kept = append(kept, item)
		}
	}
	conversation.Pending = kept
	next.Revision++
	if err := s.persist(next); err != nil {
		return projectConversation(current), err
	}
	s.state = next
	return projectConversation(conversation), nil
}

// BindSession associates a durable conversation with its local Session.
func (s *Store) BindSession(key, sessionID string) error {
	key, sessionID = strings.TrimSpace(key), strings.TrimSpace(sessionID)
	if key == "" || sessionID == "" {
		return errors.New("unread conversation key and session ID are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.state.Conversations[key]
	if current == nil {
		return fmt.Errorf("unread conversation %q does not exist", key)
	}
	if current.SessionID == sessionID {
		return nil
	}
	next := cloneState(s.state)
	next.Conversations[key].SessionID = sessionID
	next.Revision++
	if err := s.persist(next); err != nil {
		return err
	}
	s.state = next
	return nil
}

func (s *Store) persist(state diskState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode unread store: %w", err)
	}
	if err := s.writeFile(s.path, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("persist unread store: %w", err)
	}
	return nil
}

func cloneState(state diskState) diskState {
	out := newDiskState()
	out.Revision = state.Revision
	for key, value := range state.Conversations {
		clone := *value
		clone.Pending = append([]Item(nil), value.Pending...)
		clone.Seen = make(map[string]uint64, len(value.Seen))
		for id, sequence := range value.Seen {
			clone.Seen[id] = sequence
		}
		out.Conversations[key] = &clone
	}
	return out
}

func prefixedKey(source Source, key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("unread conversation key is required")
	}
	prefix := string(source) + ":"
	if strings.HasPrefix(key, prefix) {
		return key, nil
	}
	return prefix + key, nil
}
