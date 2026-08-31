package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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

// TestFindSupervisorSessionByMetaFollowsLegacyRecoveryHead proves discovery
// treats a legacy recovery branch with an empty Purpose as the current physical
// supervisor Session when its ParentID continues a supervisor lineage.
func TestFindSupervisorSessionByMetaFollowsLegacyRecoveryHead(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "supervisor-root.jsonl")
	recovery1 := filepath.Join(dir, "supervisor-recovery-1.jsonl")
	recovery2 := filepath.Join(dir, "supervisor-recovery-2.jsonl")
	for _, path := range []string{root, recovery1, recovery2} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := SaveBranchMetaPreserveUpdated(root, BranchMeta{
		SessionKind: SessionKindAssistant, AssistantID: "helper-recovery", Purpose: PurposeSupervisor,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMetaPreserveUpdated(recovery1, BranchMeta{
		SessionKind: SessionKindAssistant, AssistantID: "helper-recovery",
		Recovered: true, ParentID: string(BranchID(root)),
		CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMetaPreserveUpdated(recovery2, BranchMeta{
		SessionKind: SessionKindAssistant, AssistantID: "helper-recovery",
		Recovered: true, ParentID: string(BranchID(recovery1)),
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	found, ok := FindSupervisorSessionByMeta(dir, "helper-recovery")
	if !ok || found.Path != recovery2 {
		t.Fatalf("FindSupervisorSessionByMeta = %+v ok=%v, want recovery head %s", found, ok, recovery2)
	}
	if err := EnsureSupervisorSessionMeta(found.Path); err != nil {
		t.Fatalf("EnsureSupervisorSessionMeta: %v", err)
	}
	meta, ok, err := LoadBranchMeta(found.Path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta: ok=%v err=%v", ok, err)
	}
	if meta.Purpose != PurposeSupervisor {
		t.Fatalf("repaired Purpose = %q, want %q", meta.Purpose, PurposeSupervisor)
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
