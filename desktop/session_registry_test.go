package main

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

func TestSessionRegistryRejectsDuplicateOwner(t *testing.T) {
	var registry sessionRegistry
	first := &WorkspaceTab{SessionID: "session-a"}
	second := &WorkspaceTab{SessionID: "session-a"}
	if err := registry.add(first); err != nil {
		t.Fatalf("add first: %v", err)
	}
	if err := registry.add(second); err == nil {
		t.Fatal("duplicate owner should fail")
	}
	if got := registry.get("session-a"); got != first {
		t.Fatalf("duplicate registration replaced owner: got %p want %p", got, first)
	}
}

func TestSessionRegistryConcurrentAddGetRemove(t *testing.T) {
	var registry sessionRegistry
	const count = 128
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("session-%d", i)
			tab := &WorkspaceTab{SessionID: id}
			if err := registry.add(tab); err != nil {
				t.Errorf("add %s: %v", id, err)
				return
			}
			if got := registry.get(id); got != tab {
				t.Errorf("get %s: got %p want %p", id, got, tab)
			}
			if !registry.remove(id, tab) {
				t.Errorf("remove %s failed", id)
			}
		}(i)
	}
	wg.Wait()
	if got := registry.count(); got != 0 {
		t.Fatalf("registry count = %d, want 0", got)
	}
}

func TestSessionRegistryReplaceRequiresCurrentOwner(t *testing.T) {
	var registry sessionRegistry
	old := &WorkspaceTab{SessionID: "session-a"}
	next := &WorkspaceTab{SessionID: "session-a"}
	stale := &WorkspaceTab{SessionID: "session-a"}
	if err := registry.add(old); err != nil {
		t.Fatal(err)
	}
	if err := registry.replace("session-a", stale, next); err == nil {
		t.Fatal("stale owner should not replace the registry entry")
	}
	if err := registry.replace("session-a", old, next); err != nil {
		t.Fatalf("replace current owner: %v", err)
	}
	if got := registry.get("session-a"); got != next {
		t.Fatalf("replace owner: got %p want %p", got, next)
	}
}

func TestSessionIdentityPersistsInDesktopTabEntry(t *testing.T) {
	original := desktopTabEntry{ID: "tab-a", SessionID: "session-a", SessionPath: "session.jsonl"}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var restored desktopTabEntry
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.SessionID != original.SessionID {
		t.Fatalf("restored SessionID = %q, want %q", restored.SessionID, original.SessionID)
	}
}

func TestDetachedRuntimeKeepsSessionIdentity(t *testing.T) {
	tab := &WorkspaceTab{ID: "tab-a", SessionID: "session-a", SessionPath: "session.jsonl"}
	detached := cloneDetachedRuntimeTab(tab, "runtime-key", tab.SessionPath)
	if detached == nil {
		t.Fatal("clone returned nil")
	}
	if detached.SessionID != tab.SessionID {
		t.Fatalf("detached SessionID = %q, want %q", detached.SessionID, tab.SessionID)
	}
	if detached.ID == tab.ID {
		t.Fatal("test requires a distinct UI tab ID")
	}
}

func TestNewSessionIDIsNonEmptyAndUnique(t *testing.T) {
	first := newSessionID()
	second := newSessionID()
	if first == "" || second == "" {
		t.Fatal("new SessionID must be non-empty")
	}
	if first == second {
		t.Fatalf("new SessionIDs must differ: %q", first)
	}
}
