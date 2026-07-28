package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/work"
)

func TestFindWorkArtifactRefRequiresExactIdentity(t *testing.T) {
	view := &work.WorkView{ArtifactSlots: []work.ArtifactSlot{{
		ID:            "budget",
		WorkID:        "work-1",
		DefinitionRev: 3,
		Revision:      2,
		ArtifactRefs: []work.ArtifactRef{{
			ID:     "file-1",
			Name:   "预算表.xlsx",
			Status: work.ArtifactRefStatusAvailable,
		}},
	}}}
	input := WorkArtifactFileIntent{
		WorkID:             "work-1",
		DefinitionRevision: 3,
		SlotID:             "budget",
		SlotRevision:       2,
		ArtifactRefID:      "file-1",
	}
	if _, ok := findWorkArtifactRef(view, input); !ok {
		t.Fatal("exact artifact identity was not found")
	}
	input.SlotRevision++
	if _, ok := findWorkArtifactRef(view, input); ok {
		t.Fatal("stale slot revision must not resolve")
	}
}

func TestMaterializeWorkArtifactBlobIsNamedIdempotentAndRepairable(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works")
	store, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "work-1"
	body := []byte("xlsx-bytes")
	digest, err := store.Put(workID, body)
	if err != nil {
		t.Fatal(err)
	}
	ref := work.ArtifactRef{
		ID:         "file-1",
		Name:       "预算表.xlsx",
		Type:       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Status:     work.ArtifactRefStatusAvailable,
		BlobDigest: digest,
	}
	path, err := materializeWorkArtifactBlob(workDir, workID, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if filepath.Ext(path) != ".xlsx" {
		t.Fatalf("materialized extension=%q", filepath.Ext(path))
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("materialized content=%q err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMod := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	repeated, err := materializeWorkArtifactBlob(workDir, workID, ref)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != path {
		t.Fatalf("repeated path=%q want %q", repeated, path)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMod) {
		t.Fatal("idempotent materialization rewrote an unchanged file")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeWorkArtifactBlob(workDir, workID, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("repaired content=%q err=%v", got, err)
	}
}

func TestSafeArtifactFileNameBlocksTraversal(t *testing.T) {
	got := safeArtifactFileName(`..\..\预算:表?.xlsx`, "xlsx")
	if got != "预算_表_.xlsx" {
		t.Fatalf("safe name=%q", got)
	}
	if filepath.Base(got) != got {
		t.Fatalf("safe name escaped base: %q", got)
	}
}
