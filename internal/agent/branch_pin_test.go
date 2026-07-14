package agent

import (
	"path/filepath"
	"testing"
	"time"
)

func TestSetBranchPinnedPreservesActivity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pinned.jsonl")
	updated := time.Now().UTC().Add(-time.Hour).Round(0)
	if err := SaveBranchMetaPreserveUpdated(path, BranchMeta{ID: "pinned", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated}); err != nil {
		t.Fatalf("save meta: %v", err)
	}
	if err := SetBranchPinned(path, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	meta, ok, err := LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("load meta: ok=%v err=%v", ok, err)
	}
	if !meta.Pinned || !meta.UpdatedAt.Equal(updated) {
		t.Fatalf("meta after pin = %+v, want pinned with UpdatedAt %s", meta, updated)
	}
}
