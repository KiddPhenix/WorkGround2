package agent

import (
	"path/filepath"
	"testing"
	"time"
)

// TestBranchMetaAssistantIdentityFieldsPersist verifies the durable Assistant
// ownership chain of a Session: OwnerAssistantID (AssistantID), ParentSessionID
// (ParentID), Purpose, WorkspaceRoot, ResponsibilityID and CreateRequestID all
// round-trip through the meta sidecar and survive the preserve-merge path used
// by desktop/daemon/fork/create.
func TestBranchMetaAssistantIdentityFieldsPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sess-managed.jsonl")
	if err := writeEmptySession(path); err != nil {
		t.Fatal(err)
	}
	meta := BranchMeta{
		ID:               BranchID(path),
		AssistantID:      "assistant-1",
		ParentID:         "sess-parent",
		Purpose:          PurposeManaged,
		WorkspaceRoot:    "C:\\ws\\project",
		ResponsibilityID: "resp-1",
		CreateRequestID:  "advance/assistant-1/resp-1",
		SessionKind:      SessionKindAssistant,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	if err := SaveBranchMetaPreserveUpdated(path, meta); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta ok=%v err=%v", ok, err)
	}
	if loaded.AssistantID != "assistant-1" || loaded.ParentID != "sess-parent" ||
		loaded.Purpose != PurposeManaged || loaded.WorkspaceRoot != "C:\\ws\\project" ||
		loaded.ResponsibilityID != "resp-1" || loaded.CreateRequestID != "advance/assistant-1/resp-1" {
		t.Fatalf("meta identity fields not persisted: %+v", loaded)
	}

	// The preserve-merge path keeps the identity when a partial meta is saved
	// (the shape hosts use to stamp one field at a time).
	partial := BranchMeta{ID: loaded.ID, Purpose: PurposeResearch}
	partial = preserveBranchMetaPersistenceCopy(partial, loaded)
	if partial.AssistantID != "assistant-1" || partial.ResponsibilityID != "resp-1" ||
		partial.CreateRequestID != "advance/assistant-1/resp-1" || partial.Purpose != PurposeResearch {
		t.Fatalf("preserve-merge dropped identity: %+v", partial)
	}
}

// preserveBranchMetaPersistenceCopy mirrors preserveBranchMetaPersistence so the
// test can assert on the merge without writing.
func preserveBranchMetaPersistenceCopy(next BranchMeta, existing BranchMeta) BranchMeta {
	preserveBranchMetaPersistence(&next, existing)
	return next
}

func writeEmptySession(path string) error {
	return (&Session{}).Save(path)
}
