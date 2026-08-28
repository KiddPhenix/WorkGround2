package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestFindSupervisorSessionByMetaFindsEmptySession proves the supervisor loop's
// discovery works for a freshly-created supervisor Session whose transcript is
// still empty: ListSessions skips zero-turn transcripts, so the meta scan is
// the lookup that keeps uniqueness across ticks.
func TestFindSupervisorSessionByMetaFindsEmptySession(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "supervisor-session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = SessionKindAssistant
	meta.AssistantID = "helper-meta"
	meta.Purpose = PurposeSupervisor
	if err := SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	// The listing-based lookup skips the empty transcript.
	if _, ok := FindSupervisorSession(dir, "helper-meta"); ok {
		t.Fatal("listing-based FindSupervisorSession found an empty supervisor session (would cause duplicate creation)")
	}
	// The meta scan is the durable identity lookup the loop uses.
	found, ok := FindSupervisorSessionByMeta(dir, "helper-meta")
	if !ok || found.Path != path {
		t.Fatalf("FindSupervisorSessionByMeta = %+v ok=%v, want %s", found, ok, path)
	}

	// A managed session is never returned as the supervisor session.
	managed := filepath.Join(dir, "managed-session.jsonl")
	if err := os.WriteFile(managed, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	mmeta, err := EnsureBranchMeta(managed)
	if err != nil {
		t.Fatal(err)
	}
	mmeta.SessionKind = SessionKindAssistant
	mmeta.AssistantID = "helper-meta"
	mmeta.Purpose = PurposeManaged
	if err := SaveBranchMetaPreserveUpdated(managed, mmeta); err != nil {
		t.Fatal(err)
	}
	if _, ok := FindSupervisorSessionByMeta(dir, "helper-meta"); !ok {
		t.Fatal("supervisor session no longer found after adding a managed session")
	}
}

// TestListSessionsByOwnerByMetaIncludesEmptySessions proves the meta-based
// owner listing observes sessions from creation, before their first turn.
func TestListSessionsByOwnerByMetaIncludesEmptySessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty-session.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = SessionKindAssistant
	meta.AssistantID = "helper-owner"
	meta.Purpose = PurposeManaged
	if err := SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	owned, err := ListSessionsByOwnerByMeta(dir, "helper-owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 1 || owned[0].Path != path {
		t.Fatalf("meta owner listing = %+v, want the empty managed session", owned)
	}
}
