package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/provider"
	"workground2/internal/store"
)

// newRecoverableFixture writes a session via the normal save path (event log +
// .jsonl anchor), then truncates the anchor to 0 bytes — reproducing the
// real-world anomaly where a session starts from a pre-created empty .jsonl
// (Desktop's createEmptySessionFile) and the append-only save path never
// refreshes the anchor while the event log carries the full transcript.
func newRecoverableFixture(t *testing.T) (path string, want []string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session.jsonl")
	s := NewSession("system prompt")
	msgs := []provider.Message{
		{Role: provider.RoleUser, Content: "first turn"},
		{Role: provider.RoleAssistant, Content: "first answer"},
		{Role: provider.RoleUser, Content: "second turn"},
		{Role: provider.RoleAssistant, Content: "second answer"},
	}
	for _, m := range msgs {
		s.Add(m)
	}
	// SaveSnapshot (not Save) reproduces the production shape: the first
	// snapshot bootstraps the native event log; the append-only path then owns
	// the transcript while the .jsonl stays a compatibility anchor.
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("truncate anchor: %v", err)
	}
	want = make([]string, 0, len(msgs)+1)
	want = append(want, "system prompt")
	for _, m := range msgs {
		want = append(want, m.Content)
	}
	return path, want
}

func decodeAnchorMessages(t *testing.T, path string) []string {
	t.Helper()
	msgs, err := loadSessionMessagesFromJSONL(path)
	if err != nil {
		t.Fatalf("decode anchor: %v", err)
	}
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

// TestRebuildSessionAnchorFromEmptyJSONL: an empty main transcript with a valid
// event log rebuilds a usable, content-equivalent anchor.
func TestRebuildSessionAnchorFromEmptyJSONL(t *testing.T) {
	path, want := newRecoverableFixture(t)
	if err := RebuildSessionAnchor(path); err != nil {
		t.Fatalf("RebuildSessionAnchor: %v", err)
	}
	got := decodeAnchorMessages(t, path)
	if len(got) != len(want) {
		t.Fatalf("rebuilt anchor has %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestLoadSessionSelfHealsEmptyAnchor: the load path itself recovers the
// conversation and heals the empty anchor, so opening the session shows history
// and direct readers see a valid file afterwards.
func TestLoadSessionSelfHealsEmptyAnchor(t *testing.T) {
	path, want := newRecoverableFixture(t)
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(loaded.Messages) != len(want) {
		t.Fatalf("LoadSession recovered %d messages, want %d", len(loaded.Messages), len(want))
	}
	got := decodeAnchorMessages(t, path)
	if len(got) != len(want) {
		t.Fatalf("anchor not healed by load: %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRebuildSessionAnchorPreservesNonEmpty: a valid non-empty anchor is never
// overwritten — the rebuild is a strict no-op.
func TestRebuildSessionAnchorPreservesNonEmpty(t *testing.T) {
	path, _ := newRecoverableFixture(t)
	if err := RebuildSessionAnchor(path); err != nil {
		t.Fatalf("RebuildSessionAnchor: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Write something deliberately different into the anchor: recovery must
	// leave it byte-identical (a newer or intentionally rewritten transcript).
	foreign := []byte(`{"role":"user","content":"newer transcript"}` + "\n")
	if err := os.WriteFile(path, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RebuildSessionAnchor(path); err != nil {
		t.Fatalf("RebuildSessionAnchor on non-empty anchor: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(foreign) {
		t.Fatalf("non-empty anchor was overwritten:\nbefore=%q\nafter =%q", foreign, after)
	}
	_ = before
}

// TestRebuildSessionAnchorIdempotent: repeated recovery yields the same result,
// never duplicating messages or corrupting the file.
func TestRebuildSessionAnchorIdempotent(t *testing.T) {
	path, want := newRecoverableFixture(t)
	for i := 0; i < 3; i++ {
		if err := RebuildSessionAnchor(path); err != nil {
			t.Fatalf("rebuild pass %d: %v", i, err)
		}
		if got := decodeAnchorMessages(t, path); len(got) != len(want) {
			t.Fatalf("pass %d: anchor has %d messages, want %d", i, len(got), len(want))
		}
		// Interleave loads like a real open/retry cycle.
		if _, err := LoadSession(path); err != nil {
			t.Fatalf("load pass %d: %v", i, err)
		}
	}
}

// TestRebuildSessionAnchorUnrecoverable: an empty anchor with no replayable
// recovery source fails explicitly instead of silently leaving a blank file.
func TestRebuildSessionAnchorUnrecoverable(t *testing.T) {
	dir := t.TempDir()

	// No event log at all.
	plain := filepath.Join(dir, "plain.jsonl")
	if err := os.WriteFile(plain, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RebuildSessionAnchor(plain); !errors.Is(err, ErrSessionAnchorUnrecoverable) {
		t.Fatalf("missing event log: err = %v, want ErrSessionAnchorUnrecoverable", err)
	}

	// A foreign file squatting the event-log path is read-ignored, so recovery
	// must also refuse rather than invent content.
	foreignLog := filepath.Join(dir, "foreign.jsonl")
	if err := os.WriteFile(foreignLog, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.SessionEventLog(foreignLog), []byte(`{"kind":"user.message","text":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RebuildSessionAnchor(foreignLog); !errors.Is(err, ErrSessionAnchorUnrecoverable) {
		t.Fatalf("foreign event log: err = %v, want ErrSessionAnchorUnrecoverable", err)
	}
}

// TestRebuildSessionAnchorRejectsDamagedLog proves recovery never promotes a
// clean prefix from a torn event log into an apparently complete anchor.
func TestRebuildSessionAnchorRejectsDamagedLog(t *testing.T) {
	path, _ := newRecoverableFixture(t)
	f, err := os.OpenFile(store.SessionEventLog(path), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schema_version":1,"type":"append"`); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RebuildSessionAnchor(path); !errors.Is(err, ErrSessionAnchorDamaged) {
		t.Fatalf("damaged event log: err = %v, want ErrSessionAnchorDamaged", err)
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Size() != 0 {
		t.Fatalf("damaged event log was promoted to a %d-byte anchor", info.Size())
	}
}

// TestSaveSnapshotRefreshesEmptyAnchor: the save path's anchor guarantee — a
// session that starts from a pre-created empty .jsonl (Desktop's
// createEmptySessionFile) must converge to a valid anchor on its first real
// snapshot instead of keeping the "content only in the sidecar" half-state.
func TestSaveSnapshotRefreshesEmptyAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	// Simulate createEmptySessionFile: the discovery anchor exists but is empty.
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSession("system prompt")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "first turn"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "first answer"})
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("SaveSnapshot: %v", err)
	}
	got := decodeAnchorMessages(t, path)
	want := []string{"system prompt", "first turn", "first answer"}
	if len(got) != len(want) {
		t.Fatalf("anchor after first snapshot has %d messages, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("message %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The event log must also be intact: loading from disk yields the same
	// conversation, proving the anchor refresh did not disturb the authority.
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if len(loaded.Messages) != len(want) {
		t.Fatalf("LoadSession recovered %d messages, want %d", len(loaded.Messages), len(want))
	}
}

// TestSaveSnapshotRefreshesTruncatedUpToDateAnchor covers the same-content
// retry path: losing only the anchor after a successful save must not be
// stranded by the up-to-date fast path.
func TestSaveSnapshotRefreshesTruncatedUpToDateAnchor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	s := NewSession("system prompt")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "first turn"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "first answer"})
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("first SaveSnapshot: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("same-content SaveSnapshot: %v", err)
	}
	if got := decodeAnchorMessages(t, path); len(got) != 3 {
		t.Fatalf("repaired anchor has %d messages, want 3", len(got))
	}
}
