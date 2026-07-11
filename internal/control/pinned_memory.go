package control

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"workground2/internal/fileutil"
	"workground2/internal/store"
)

// PinnedMemory is one pinned transcript entry, stored per-session and
// injected into future turns as a transient <pinned-memory> block so context
// compaction cannot lose it.
type PinnedMemory struct {
	ID      string `json:"id"`
	Role    string `json:"role"` // "user" or "assistant"
	Content string `json:"content"`
	Turn    int    `json:"turn"`
	Pinned  bool   `json:"pinned"`
}

// PinMemoryID derives a stable idempotent ID from role, turn, and content
// so pinning the same text twice produces the same entry.
func PinMemoryID(role, content string, turn int) string {
	role = normalizePinnedMemoryRole(role)
	content = strings.TrimSpace(content)
	h := sha256.Sum224([]byte(fmt.Sprintf("%s:%d:%s", role, turn, content)))
	return fmt.Sprintf("pm-%x", h[:16])
}

func normalizePinnedMemoryRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "assistant", "ai", "model":
		return "assistant"
	default:
		return "user"
	}
}

func validatePinnedMemory(role, content string, turn int) (string, string, int, error) {
	role = normalizePinnedMemoryRole(role)
	content = strings.TrimSpace(content)
	if content == "" {
		return role, "", turn, errors.New("pinned memory content is empty")
	}
	if turn < 0 {
		turn = 0
	}
	return role, content, turn, nil
}

// pinnedMemoStore is the session-scoped pinned-memory list. It lives on the
// Controller and is persisted to the *.pinned-memo.json sidecar.
type pinnedMemoStore struct {
	mu    sync.RWMutex
	items []PinnedMemory
}

func (s *pinnedMemoStore) load(sessionPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = nil
	if sessionPath == "" {
		return nil
	}
	sidecar := store.SessionPinnedMemo(sessionPath)
	data, err := os.ReadFile(sidecar)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, &s.items); err != nil {
		s.items = nil
		return fmt.Errorf("pinned memos: corrupt sidecar %s: %w", sidecar, err)
	}
	return nil
}

func (s *pinnedMemoStore) save(sessionPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionPath == "" {
		return nil
	}
	sidecar := store.SessionPinnedMemo(sessionPath)
	if len(s.items) == 0 {
		// Remove empty sidecar.
		_ = os.Remove(sidecar)
		return nil
	}
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return err
	}
	if err := fileutil.AtomicWriteFile(sidecar, data, 0o644); err != nil {
		return err
	}
	return nil
}

// all returns a copy of the current list.
func (s *pinnedMemoStore) all() []PinnedMemory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]PinnedMemory, len(s.items))
	copy(out, s.items)
	return out
}

// active returns pinned items only.
func (s *pinnedMemoStore) active() []PinnedMemory {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []PinnedMemory
	for _, it := range s.items {
		if it.Pinned {
			out = append(out, it)
		}
	}
	return out
}

// pin adds or updates an entry. Returns the ID and whether it's new.
func (s *pinnedMemoStore) pin(role, content string, turn int) (string, bool) {
	role, content, turn, _ = validatePinnedMemory(role, content, turn)
	id := PinMemoryID(role, content, turn)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.ID == id {
			if !it.Pinned {
				s.items[i].Pinned = true
			}
			return id, false
		}
	}
	s.items = append(s.items, PinnedMemory{
		ID:      id,
		Role:    role,
		Content: content,
		Turn:    turn,
		Pinned:  true,
	})
	return id, true
}

// setPinned toggles the pinned flag for an entry by ID.
func (s *pinnedMemoStore) setPinned(id string, pinned bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, it := range s.items {
		if it.ID == id {
			if it.Pinned == pinned {
				return false // no change
			}
			s.items[i].Pinned = pinned
			return true
		}
	}
	return false
}
