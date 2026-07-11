package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/store"
)

func TestPinnedMemoryPersistsAndComposes(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	c := New(Options{})
	c.SetSessionPath(sessionPath)

	id, isNew, err := c.PinMemory("ai", "  keep this conclusion  ", 3)
	if err != nil {
		t.Fatal(err)
	}
	if id == "" || !isNew {
		t.Fatalf("PinMemory id=%q isNew=%v, want new id", id, isNew)
	}

	id2, isNew, err := c.PinMemory("assistant", "keep this conclusion", 3)
	if err != nil {
		t.Fatal(err)
	}
	if id2 != id || isNew {
		t.Fatalf("PinMemory duplicate id=%q isNew=%v, want id=%q isNew=false", id2, isNew, id)
	}

	got := c.Compose("next")
	if !strings.Contains(got, "<pinned-memory>") || !strings.Contains(got, "keep this conclusion") {
		t.Fatalf("Compose should include pinned memory, got %q", got)
	}
	if stripped := StripComposePrefixes(got); stripped != "next" {
		t.Fatalf("StripComposePrefixes = %q, want next", stripped)
	}
	if _, err := os.Stat(store.SessionPinnedMemo(sessionPath)); err != nil {
		t.Fatalf("pinned-memory sidecar not written: %v", err)
	}

	resumed := New(Options{SessionPath: sessionPath})
	items := resumed.PinnedMemories()
	if len(items) != 1 || items[0].ID != id || !items[0].Pinned {
		t.Fatalf("resumed pinned memories = %+v, want one active item %q", items, id)
	}

	changed, err := resumed.SetPinnedMemoryPinned(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("SetPinnedMemoryPinned(false) changed=false, want true")
	}
	if got := resumed.Compose("next"); strings.Contains(got, "keep this conclusion") {
		t.Fatalf("unpinned memory should not compose, got %q", got)
	}

	changed, err = resumed.SetPinnedMemoryPinned(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("SetPinnedMemoryPinned(true) changed=false, want true")
	}
	if got := resumed.Compose("next"); !strings.Contains(got, "keep this conclusion") {
		t.Fatalf("re-pinned memory should compose, got %q", got)
	}
}

func TestPinMemoryRejectsEmptyContent(t *testing.T) {
	c := New(Options{})
	if _, _, err := c.PinMemory("user", "   ", -1); err == nil {
		t.Fatal("PinMemory accepted empty content")
	}
}
