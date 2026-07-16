package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"
)

// sessionRegistry owns the process-local index from stable session identity to
// its current runtime holder. The zero value is ready for use so tests and
// lightweight App values do not need special initialization.
type sessionRegistry struct {
	mu    sync.RWMutex
	items map[string]*WorkspaceTab
}

func (r *sessionRegistry) add(tab *WorkspaceTab) error {
	if tab == nil {
		return fmt.Errorf("session runtime is required")
	}
	id := strings.TrimSpace(tab.SessionID)
	if id == "" {
		return fmt.Errorf("session ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]*WorkspaceTab)
	}
	if current := r.items[id]; current != nil && current != tab {
		return fmt.Errorf("session %q is already registered", id)
	}
	r.items[id] = tab
	return nil
}

func (r *sessionRegistry) get(id string) *WorkspaceTab {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.items[id]
}

// replace moves one registered identity to a new runtime holder without
// exposing an unregistered window. It rejects stale owners explicitly.
func (r *sessionRegistry) replace(id string, old, next *WorkspaceTab) error {
	id = strings.TrimSpace(id)
	if id == "" || next == nil || strings.TrimSpace(next.SessionID) != id {
		return fmt.Errorf("matching session ID and runtime are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil || r.items[id] != old {
		return fmt.Errorf("session %q owner changed", id)
	}
	r.items[id] = next
	return nil
}

// remove deletes id only when expected still owns it. This keeps a delayed
// cleanup from removing a newer runtime registered under the same identity.
func (r *sessionRegistry) remove(id string, expected *WorkspaceTab) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil || r.items[id] != expected {
		return false
	}
	delete(r.items, id)
	return true
}

func (r *sessionRegistry) count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.items)
}

func newSessionID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return "session_" + hex.EncodeToString(value[:])
	}
	now := time.Now().UTC()
	return "session_" + now.Format("20060102150405") + "_" + fmt.Sprintf("%09d", now.Nanosecond())
}

// trackSession assigns an identity when restoring legacy state and registers
// the holder. A persisted duplicate is repaired with a fresh ID; add itself
// still rejects overwrites so callers can never silently replace an owner.
func (a *App) trackSession(tab *WorkspaceTab) {
	if tab == nil {
		return
	}
	for {
		if strings.TrimSpace(tab.SessionID) == "" {
			tab.SessionID = newSessionID()
		}
		if err := a.sessions.add(tab); err == nil {
			return
		}
		tab.SessionID = newSessionID()
	}
}

// sessionByID resolves only explicit identities. It deliberately gives an
// empty ID no "current UI session" meaning.
func (a *App) sessionByID(id string) *WorkspaceTab {
	return a.sessions.get(id)
}
