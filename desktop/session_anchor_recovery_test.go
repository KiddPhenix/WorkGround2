package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/provider"
	"workground2/internal/store"
)

// writeRecoverableSessionFixture creates the exact anomaly shape this fix
// targets: a session whose primary .jsonl is 0 bytes (as if pre-created by
// createEmptySessionFile) while the native event log and meta sidecar hold the
// real transcript. Returns the session path.
func writeRecoverableSessionFixture(t *testing.T, withEvents, withMeta bool) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("create empty anchor: %v", err)
	}
	if withEvents {
		s := agent.NewSession("system prompt")
		s.Add(provider.Message{Role: provider.RoleUser, Content: "first turn"})
		s.Add(provider.Message{Role: provider.RoleAssistant, Content: "first answer"})
		if err := s.SaveSnapshot(path); err != nil {
			t.Fatalf("save snapshot: %v", err)
		}
		// Restore the anomaly: the anchor must be empty again, exactly like the
		// sessions produced before the anchor guarantee existed.
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("truncate anchor: %v", err)
		}
	}
	if withMeta {
		meta := agent.BranchMeta{ID: agent.BranchID(path), Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion}
		if err := agent.SaveBranchMeta(path, meta); err != nil {
			t.Fatalf("write meta: %v", err)
		}
	}
	return path
}

// TestLoadResumableSessionRecoversEmptyAnchor exercises the real Desktop open
// path (loadResumableSession → agent.LoadSession): a 0-byte .jsonl with a valid
// event log must show history and heal the anchor, so direct readers see a
// usable file afterwards.
func TestLoadResumableSessionRecoversEmptyAnchor(t *testing.T) {
	path := writeRecoverableSessionFixture(t, true, true)
	loaded, err := loadResumableSession(path)
	if err != nil {
		t.Fatalf("loadResumableSession: %v", err)
	}
	if n := len(loaded.Snapshot()); n != 3 {
		t.Fatalf("recovered %d messages, want 3", n)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Fatal("anchor was not healed by the load path")
	}
}

// TestPreviewSessionPageRecoversEmptyAnchor covers the history-drawer read
// path, which is the other place a Desktop user sees a session without resuming
// it.
func TestPreviewSessionPageRecoversEmptyAnchor(t *testing.T) {
	path := writeRecoverableSessionFixture(t, true, true)
	page, err := previewSessionPage(filepath.Dir(path), path, 0, 60)
	if err != nil {
		t.Fatalf("previewSessionPage: %v", err)
	}
	if len(page.Messages) != 3 {
		t.Fatalf("preview returned %d messages, want 3", len(page.Messages))
	}
	if page.TotalTurns != 1 {
		t.Fatalf("preview totalTurns = %d, want 1", page.TotalTurns)
	}
}

// TestLoadResumableSessionFailsExplicitlyWhenUnrecoverable: a session whose
// meta promises content but whose .jsonl is empty and no event log remains must
// fail explicitly instead of silently opening a blank conversation.
func TestLoadResumableSessionFailsExplicitlyWhenUnrecoverable(t *testing.T) {
	path := writeRecoverableSessionFixture(t, false, true)
	_, err := loadResumableSession(path)
	if err == nil {
		t.Fatal("loadResumableSession succeeded for an unrecoverable session; want explicit error")
	}
	if !strings.Contains(err.Error(), "cannot be recovered") && !strings.Contains(err.Error(), "no replayable event log") {
		t.Fatalf("error does not explain the recovery failure: %v", err)
	}
}

// TestLoadResumableSessionAllowsGenuinelyEmptySession: a truly new session (no
// meta promise, no events) still opens as an empty conversation — the strict
// gate must not break fresh-session flows.
func TestLoadResumableSessionAllowsGenuinelyEmptySession(t *testing.T) {
	path := writeRecoverableSessionFixture(t, false, false)
	loaded, err := loadResumableSession(path)
	if err != nil {
		t.Fatalf("genuinely empty session must open without error: %v", err)
	}
	if n := len(loaded.Snapshot()); n != 0 {
		t.Fatalf("empty session reported %d messages, want 0", n)
	}
}

// TestLoadResumableSessionRejectsDamagedRecoveryLog ensures Desktop surfaces a
// torn recovery source instead of showing its last clean prefix as complete.
func TestLoadResumableSessionRejectsDamagedRecoveryLog(t *testing.T) {
	path := writeRecoverableSessionFixture(t, true, true)
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
	if _, err := loadResumableSession(path); !errors.Is(err, agent.ErrSessionAnchorDamaged) {
		t.Fatalf("damaged event log: err = %v, want ErrSessionAnchorDamaged", err)
	}
}
