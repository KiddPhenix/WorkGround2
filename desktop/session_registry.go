package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"workground2/internal/control"
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

// ensure registers restored or legacy runtime state. Missing and persisted
// duplicate identities are repaired while holding the same lock as insertion.
func (r *sessionRegistry) ensure(tab *WorkspaceTab) string {
	if tab == nil {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]*WorkspaceTab)
	}
	id := strings.TrimSpace(tab.SessionID)
	if owner := r.items[id]; id == "" || (owner != nil && owner != tab) {
		for {
			id = newSessionID()
			if r.items[id] == nil {
				break
			}
		}
		tab.SessionID = id
	}
	r.items[id] = tab
	return id
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

// rekey atomically removes one identity and adds its replacement for the same
// runtime. Readers can observe either complete state, never a half update.
func (r *sessionRegistry) rekey(oldID, newID string, tab *WorkspaceTab) error {
	oldID = strings.TrimSpace(oldID)
	newID = strings.TrimSpace(newID)
	if oldID == "" || newID == "" || tab == nil {
		return fmt.Errorf("old ID, new ID, and runtime are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil || r.items[oldID] != tab {
		return fmt.Errorf("session %q owner changed", oldID)
	}
	if owner := r.items[newID]; owner != nil {
		return fmt.Errorf("session %q already exists", newID)
	}
	delete(r.items, oldID)
	tab.SessionID = newID
	r.items[newID] = tab
	return nil
}

// adopt transfers source's identity to target and drops target's previous
// identity in one registry critical section.
func (r *sessionRegistry) adopt(target, source *WorkspaceTab) error {
	if target == nil || source == nil {
		return fmt.Errorf("target and source runtimes are required")
	}
	sourceID := strings.TrimSpace(source.SessionID)
	targetID := strings.TrimSpace(target.SessionID)
	if sourceID == "" {
		return fmt.Errorf("source session ID is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil || r.items[sourceID] != source {
		return fmt.Errorf("session %q owner changed", sourceID)
	}
	if targetID != "" && targetID != sourceID {
		if owner := r.items[targetID]; owner != nil && owner != target {
			return fmt.Errorf("target session %q owner changed", targetID)
		}
		delete(r.items, targetID)
	}
	target.SessionID = sourceID
	r.items[sourceID] = target
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
	a.sessions.ensure(tab)
}

// sessionByID resolves only explicit identities. It deliberately gives an
// empty ID no "current UI session" meaning.
func (a *App) sessionByID(id string) *WorkspaceTab {
	return a.sessions.get(id)
}

func (a *App) sessionAndCtrl(id string) (*WorkspaceTab, control.SessionAPI) {
	tab := a.sessionByID(id)
	if tab == nil {
		return nil, nil
	}
	a.mu.RLock()
	ctrl := tab.Ctrl
	a.mu.RUnlock()
	return tab, ctrl
}

func (a *App) activeSessionID() string {
	a.mu.RLock()
	tab := a.activeTabLocked()
	a.mu.RUnlock()
	if tab == nil {
		return ""
	}
	if id := strings.TrimSpace(tab.SessionID); id != "" && a.sessionByID(id) == tab {
		return id
	}
	// Legacy tests and restored pre-SessionID state may reach an old active
	// facade before the normal startup registration pass.
	a.trackSession(tab)
	return tab.SessionID
}

func (a *App) untrackSession(tab *WorkspaceTab) bool {
	if tab == nil {
		return false
	}
	return a.sessions.remove(tab.SessionID, tab)
}

func (a *App) rotateSessionID(tab *WorkspaceTab) error {
	if tab == nil {
		return fmt.Errorf("session runtime is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.sessions.rekey(tab.SessionID, newSessionID(), tab); err != nil {
		return err
	}
	a.saveTabsLocked()
	return nil
}
