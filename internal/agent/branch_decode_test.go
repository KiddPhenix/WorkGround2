package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBranchMetaDecodeErrorIsTypedAndOmitsPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metaPath := BranchMetaPath(path)
	if err := os.WriteFile(metaPath, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err := LoadBranchMeta(path)
	if err == nil {
		t.Fatal("expected a decode error for malformed meta")
	}
	var decodeErr *BranchMetaDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("malformed meta error %T is not BranchMetaDecodeError: %v", err, err)
	}
	if decodeErr.Err == nil {
		t.Fatal("BranchMetaDecodeError does not carry the underlying JSON error")
	}
	if strings.Contains(err.Error(), metaPath) {
		t.Fatalf("decode error leaks the sidecar path: %v", err)
	}
	if decodeErr.MetaPath != metaPath {
		t.Fatalf("BranchMetaDecodeError.MetaPath=%q, want %q (kept for logs only)", decodeErr.MetaPath, metaPath)
	}
}

func TestSaveBranchMetaRepairsCorruptMeta(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BranchMetaPath(path), []byte("{ corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A decode-only corruption may be overwritten so the sidecar is repaired.
	if err := SaveBranchMeta(path, BranchMeta{Name: "repaired"}); err != nil {
		t.Fatal(err)
	}
	meta, ok, err := LoadBranchMeta(path)
	if err != nil || !ok || meta.Name != "repaired" {
		t.Fatalf("repaired meta=%+v ok=%v err=%v", meta, ok, err)
	}
}

func TestSaveBranchMetaReturnsOnNonDecodeIOError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Make the meta path a directory so the read is an ordinary I/O error, not a
	// decode corruption; SaveBranchMeta must not overwrite (and lose preserve
	// fields) in that case.
	if err := os.MkdirAll(BranchMetaPath(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := SaveBranchMeta(path, BranchMeta{Name: "nope"}); err == nil {
		t.Fatal("expected SaveBranchMeta to fail on a non-decode I/O error")
	}
}

func TestSaveBranchMetaWritesValidJSONAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := SaveBranchMeta(path, BranchMeta{Name: "first"}); err != nil {
		t.Fatal(err)
	}
	first, ok, err := LoadBranchMeta(path)
	if err != nil || !ok || first.Name != "first" {
		t.Fatalf("first meta=%+v ok=%v err=%v", first, ok, err)
	}

	// Overwrite the existing sidecar through the same atomic path.
	if err := SaveBranchMeta(path, BranchMeta{Name: "second", SessionKind: SessionKindCollaboration}); err != nil {
		t.Fatal(err)
	}
	second, ok, err := LoadBranchMeta(path)
	if err != nil || !ok || second.Name != "second" || second.SessionKind != SessionKindCollaboration {
		t.Fatalf("second meta=%+v ok=%v err=%v", second, ok, err)
	}

	raw, err := os.ReadFile(BranchMetaPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		t.Fatal("saved meta is empty")
	}
	var parsed BranchMeta
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("saved meta is not valid JSON: %v", err)
	}
}
