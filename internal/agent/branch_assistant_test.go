package agent

import (
	"path/filepath"
	"testing"

	"workground2/internal/provider"
)

func TestBranchMetaAssistantIdentityRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "assistant.jsonl")

	sess := NewSession("sys")
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMeta(path, BranchMeta{
		SessionKind: SessionKindAssistant,
		AssistantID: "assistant-1",
	}); err != nil {
		t.Fatal(err)
	}

	meta, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta = %v, %v, %v", meta, ok, err)
	}
	if meta.SessionKind != SessionKindAssistant {
		t.Fatalf("SessionKind = %q, want %q", meta.SessionKind, SessionKindAssistant)
	}
	if meta.AssistantID != "assistant-1" {
		t.Fatalf("AssistantID = %q, want assistant-1", meta.AssistantID)
	}
}

func TestRecoveryBranchKeepsAssistantIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assistant.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "old"})
	if err := parent.Save(path); err != nil {
		t.Fatal(err)
	}
	meta, err := EnsureBranchMeta(path)
	if err != nil {
		t.Fatal(err)
	}
	meta.SessionKind = SessionKindAssistant
	meta.AssistantID = "assistant-recovery"
	if err := SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}

	recovered := NewSession("sys")
	recovered.Add(provider.Message{Role: provider.RoleUser, Content: "new"})
	info, err := recovered.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if info.Meta.SessionKind != SessionKindAssistant || info.Meta.AssistantID != "assistant-recovery" {
		t.Fatalf("recovery identity = kind %q assistant %q", info.Meta.SessionKind, info.Meta.AssistantID)
	}
}

func TestPreserveBranchMetaKeepsAssistantIdentity(t *testing.T) {
	existing := BranchMeta{SessionKind: SessionKindAssistant, AssistantID: "assistant-9"}
	next := BranchMeta{SessionKind: SessionKindAssistant}
	preserveBranchMetaPersistence(&next, existing)
	if next.AssistantID != "assistant-9" {
		t.Fatalf("AssistantID = %q, want preserved assistant-9", next.AssistantID)
	}
	// A non-empty AssistantID on the incoming meta must win.
	next2 := BranchMeta{SessionKind: SessionKindAssistant, AssistantID: "assistant-10"}
	preserveBranchMetaPersistence(&next2, existing)
	if next2.AssistantID != "assistant-10" {
		t.Fatalf("AssistantID = %q, want incoming assistant-10", next2.AssistantID)
	}
}
