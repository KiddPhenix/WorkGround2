package agent

import (
	"path/filepath"
	"testing"

	"workground2/internal/provider"
)

func TestDeriveSessionStatus(t *testing.T) {
	cases := []struct {
		name string
		meta BranchMeta
		want SessionStatus
	}{
		{"empty", BranchMeta{}, SessionStatusIdle},
		{"in flight", BranchMeta{InFlightTurn: &InFlightTurnMeta{StartMessageIndex: 1}}, SessionStatusRunning},
		{"needs attention", BranchMeta{NeedsAttention: true}, SessionStatusWaiting},
		{"persisted wins", BranchMeta{Status: SessionStatusCompleted, InFlightTurn: &InFlightTurnMeta{}}, SessionStatusCompleted},
		{"failed persisted", BranchMeta{Status: SessionStatusFailed}, SessionStatusFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveSessionStatus(c.meta); got != c.want {
				t.Fatalf("DeriveSessionStatus = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBranchMetaPurposeAndStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "managed.jsonl")
	sess := NewSession("sys")
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMeta(path, BranchMeta{
		SessionKind: SessionKindAssistant,
		AssistantID: "assistant-1",
		Purpose:     PurposeManaged,
		Status:      SessionStatusRunning,
	}); err != nil {
		t.Fatal(err)
	}
	meta, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta = %v, %v, %v", meta, ok, err)
	}
	if meta.Purpose != PurposeManaged || meta.Status != SessionStatusRunning {
		t.Fatalf("meta = %+v", meta)
	}
	if got := DeriveSessionStatus(meta); got != SessionStatusRunning {
		t.Fatalf("DeriveSessionStatus = %q, want running", got)
	}
}

func TestPreserveBranchMetaKeepsPurposeAndStatus(t *testing.T) {
	existing := BranchMeta{Purpose: PurposeSupervisor, Status: SessionStatusWaiting}
	next := BranchMeta{}
	preserveBranchMetaPersistence(&next, existing)
	if next.Purpose != PurposeSupervisor || next.Status != SessionStatusWaiting {
		t.Fatalf("preserve = %+v", next)
	}
	// Non-empty incoming values win.
	next2 := BranchMeta{Purpose: PurposeResearch, Status: SessionStatusCancelled}
	preserveBranchMetaPersistence(&next2, existing)
	if next2.Purpose != PurposeResearch || next2.Status != SessionStatusCancelled {
		t.Fatalf("incoming = %+v", next2)
	}
}

func TestFindSupervisorSessionAndIsSupervisor(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, assistantID string, purpose SessionPurpose) {
		path := filepath.Join(dir, name)
		sess := NewSession("sys")
		sess.Add(provider.Message{Role: provider.RoleUser, Content: "hi"})
		if err := sess.Save(path); err != nil {
			t.Fatal(err)
		}
		if err := SaveBranchMeta(path, BranchMeta{
			SessionKind: SessionKindAssistant, AssistantID: assistantID, Purpose: purpose,
			Turns: 1, SchemaVersion: BranchMetaCountsVersion,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("sup.jsonl", "assistant-1", PurposeSupervisor)
	mk("managed.jsonl", "assistant-1", PurposeManaged)
	mk("other-sup.jsonl", "assistant-2", PurposeSupervisor)

	sup, ok := FindSupervisorSession(dir, "assistant-1")
	if !ok || sup.Purpose != PurposeSupervisor || sup.AssistantID != "assistant-1" {
		t.Fatalf("FindSupervisorSession = %+v ok=%v", sup, ok)
	}
	if _, ok := FindSupervisorSession(dir, "assistant-3"); ok {
		t.Fatal("found a supervisor session for an unknown assistant")
	}
	if !IsSupervisorSession(BranchMeta{SessionKind: SessionKindAssistant, Purpose: PurposeSupervisor}) {
		t.Fatal("IsSupervisorSession = false for a supervisor meta")
	}
	if IsSupervisorSession(BranchMeta{SessionKind: SessionKindAssistant, Purpose: PurposeManaged}) {
		t.Fatal("IsSupervisorSession = true for a managed meta")
	}
}

func TestListSessionsByOwnerFiltersAssistant(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, assistantID string, purpose SessionPurpose) {
		path := filepath.Join(dir, name)
		sess := NewSession("sys")
		sess.Add(provider.Message{Role: provider.RoleUser, Content: "hi"})
		if err := sess.Save(path); err != nil {
			t.Fatal(err)
		}
		if err := SaveBranchMeta(path, BranchMeta{
			SessionKind: SessionKindAssistant,
			AssistantID: assistantID, Purpose: purpose,
			Status: SessionStatusCompleted, Turns: 1, SchemaVersion: BranchMetaCountsVersion,
		}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a-1.jsonl", "assistant-1", PurposeManaged)
	mk("a-2.jsonl", "assistant-1", PurposeSupervisor)
	mk("b-1.jsonl", "assistant-2", PurposeManaged)

	owned, err := ListSessionsByOwner(dir, "assistant-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(owned) != 2 {
		t.Fatalf("owned sessions = %d, want 2", len(owned))
	}
	for _, s := range owned {
		if s.AssistantID != "assistant-1" {
			t.Fatalf("unexpected owner %q", s.AssistantID)
		}
	}
}
