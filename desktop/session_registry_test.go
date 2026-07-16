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

func TestSessionRegistryEnsureRepairsDuplicateAtomically(t *testing.T) {
	var registry sessionRegistry
	first := &WorkspaceTab{SessionID: "session-duplicate"}
	second := &WorkspaceTab{SessionID: "session-duplicate"}
	if id := registry.ensure(first); id != "session-duplicate" {
		t.Fatalf("first identity = %q", id)
	}
	secondID := registry.ensure(second)
	if secondID == "" || secondID == first.SessionID {
		t.Fatalf("duplicate identity was not repaired: %q", secondID)
	}
	if registry.get(first.SessionID) != first || registry.get(secondID) != second {
		t.Fatal("ensure did not retain both runtimes")
	}
}

func TestSessionRegistryRekeyIsAtomic(t *testing.T) {
	var registry sessionRegistry
	tab := &WorkspaceTab{SessionID: "session-old"}
	if err := registry.add(tab); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		<-start
		done <- registry.rekey("session-old", "session-new", tab)
	}()
	close(start)
	for {
		old := registry.get("session-old")
		next := registry.get("session-new")
		if old == nil && next == nil {
			t.Fatal("registry exposed a missing identity during rekey")
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			if registry.get("session-old") != nil || registry.get("session-new") != tab {
				t.Fatal("rekey did not publish exactly the new identity")
			}
			return
		default:
		}
	}
}

func TestSessionRegistryAdoptTransfersIdentityAndDropsPlaceholder(t *testing.T) {
	var registry sessionRegistry
	source := &WorkspaceTab{SessionID: "session-source"}
	target := &WorkspaceTab{SessionID: "session-placeholder"}
	if err := registry.add(source); err != nil {
		t.Fatal(err)
	}
	if err := registry.add(target); err != nil {
		t.Fatal(err)
	}
	if err := registry.adopt(target, source); err != nil {
		t.Fatal(err)
	}
	if target.SessionID != "session-source" || registry.get("session-source") != target {
		t.Fatal("source identity was not transferred to the target runtime")
	}
	if registry.get("session-placeholder") != nil || registry.count() != 1 {
		t.Fatal("target placeholder identity remained registered")
	}
}

func TestSessionRegistrySerializesUIRotateAndExternalCreate(t *testing.T) {
	var registry sessionRegistry
	ui := &WorkspaceTab{SessionID: "session-ui-old"}
	external := &WorkspaceTab{SessionID: "session-external"}
	if err := registry.add(ui); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		errs <- registry.rekey("session-ui-old", "session-ui-new", ui)
	}()
	go func() {
		<-start
		errs <- registry.add(external)
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if registry.count() != 2 || registry.get("session-ui-new") != ui || registry.get("session-external") != external {
		t.Fatal("concurrent UI/external create lost or replaced a session identity")
	}
	if registry.get("session-ui-old") != nil {
		t.Fatal("rotated UI identity remained queryable")
	}
}

func TestTabLookupAcceptsSessionIdentity(t *testing.T) {
	tab := &WorkspaceTab{ID: "tab-a", SessionID: "session-a"}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}
	if got := app.tabByID("session-a"); got != tab {
		t.Fatalf("session lookup = %p, want %p", got, tab)
	}
}

func TestMarkTabRemovedMakesSessionUnqueryable(t *testing.T) {
	tab := &WorkspaceTab{ID: "tab-a", SessionID: "session-a"}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}
	app.trackSession(tab)
	app.mu.Lock()
	app.markTabRemovedLocked(tab)
	app.mu.Unlock()
	if got := app.sessionByID(tab.SessionID); got != nil {
		t.Fatalf("removed session remains queryable: %p", got)
	}
}

func TestExplicitSessionActionsRejectEmptyIdentity(t *testing.T) {
	app := NewApp()
	if err := app.CompactForSession(""); err == nil {
		t.Fatal("CompactForSession accepted an empty identity")
	}
	if err := app.RewindForSession("", 0, "both"); err == nil {
		t.Fatal("RewindForSession accepted an empty identity")
	}
	if _, err := app.ForkForSession("", 0); err == nil {
		t.Fatal("ForkForSession accepted an empty identity")
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
