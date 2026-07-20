package work

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestNormalizeDefinitionSnapshot_Idempotent(t *testing.T) {
	orig := &WorkDefinitionSnapshot{
		SchemaVersion: 1,
		Revision:      1,
		BlueprintRef: BlueprintRef{
			ID:            "blueprint:test",
			SchemaVersion: 1,
			Version:       2,
		},
		PromptTemplate: "hello",
		Workflow:       WorkflowDef{Stages: []StageSpec{}},
		BlockSpecs:     []BlockSpec{},
	}

	n1, err := NormalizeDefinitionSnapshot(orig)
	if err != nil {
		t.Fatalf("first normalise: %v", err)
	}
	if n1.Digest == "" {
		t.Fatal("expected non-empty digest after normalise")
	}
	if n1.Digest == orig.Digest {
		t.Fatal("normalised digest should differ from original (which was empty)")
	}

	// Second normalise of the same input must produce the same digest.
	n2, err := NormalizeDefinitionSnapshot(orig)
	if err != nil {
		t.Fatalf("second normalise: %v", err)
	}
	if n2.Digest != n1.Digest {
		t.Fatalf("digest changed across calls: %s vs %s", n1.Digest, n2.Digest)
	}

	// Normalise the already-normalised snapshot again — must be stable.
	n3, err := NormalizeDefinitionSnapshot(n1)
	if err != nil {
		t.Fatalf("third normalise: %v", err)
	}
	if n3.Digest != n1.Digest {
		t.Fatalf("re-normalising changed digest: %s vs %s", n1.Digest, n3.Digest)
	}

	// Original must be untouched.
	if orig.Digest != "" {
		t.Fatal("NormalizeDefinitionSnapshot mutated the original")
	}
}

func TestNormalizeDefinitionSnapshot_DigestNotSelfReferential(t *testing.T) {
	// If Digest were included in the hash it would change the hash, making
	// it impossible to store. Verify that different Digest values on input
	// produce the same output digest.
	s := &WorkDefinitionSnapshot{
		SchemaVersion:  1,
		Revision:       1,
		BlueprintRef:   BlueprintRef{ID: "bp:x", SchemaVersion: 1, Version: 1},
		PromptTemplate: "test",
		Workflow:       WorkflowDef{},
		BlockSpecs:     []BlockSpec{},
		Digest:         "sha256:0000000000000000000000000000000000000000000000000000000000000000",
	}
	d1, err := ComputeDigest(s)
	if err != nil {
		t.Fatalf("ComputeDigest with fake digest: %v", err)
	}
	s.Digest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	d2, err := ComputeDigest(s)
	if err != nil {
		t.Fatalf("ComputeDigest with different fake digest: %v", err)
	}
	if d1 != d2 {
		t.Fatalf("digest changed when only the Digest field changed: %s vs %s", d1, d2)
	}
}

func TestNormalizeDefinitionSnapshot_StableAcrossKeyOrder(t *testing.T) {
	// JSON objects with the same logical content but different key order
	// must produce the same digest.
	jsA := `{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp:x","schemaVersion":1,"version":1},"promptTemplate":"x","workflow":{"stages":[]},"blockSpecs":[]}`
	jsB := `{"revision":1,"schemaVersion":1,"promptTemplate":"x","blueprintRef":{"version":1,"schemaVersion":1,"id":"bp:x"},"blockSpecs":[],"workflow":{"stages":[]}}`

	var a, b WorkDefinitionSnapshot
	if err := json.Unmarshal([]byte(jsA), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(jsB), &b); err != nil {
		t.Fatal(err)
	}

	na, _ := NormalizeDefinitionSnapshot(&a)
	nb, _ := NormalizeDefinitionSnapshot(&b)
	if na.Digest != nb.Digest {
		t.Fatalf("key-order change altered digest: %s vs %s", na.Digest, nb.Digest)
	}
}

func TestNormalizeDefinitionSnapshot_PreservesLargeJSONInteger(t *testing.T) {
	s := &WorkDefinitionSnapshot{
		SchemaVersion: 1,
		Revision:      1,
		BlueprintRef:  BlueprintRef{ID: "bp:x", SchemaVersion: 1, Version: 1},
		InputSchema:   json.RawMessage(`{"minimum":9007199254740993}`),
		Workflow:      WorkflowDef{},
		BlockSpecs:    []BlockSpec{},
	}

	normalized, err := NormalizeDefinitionSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(normalized.InputSchema), `{"minimum":9007199254740993}`; got != want {
		t.Fatalf("large JSON integer changed: got %s, want %s", got, want)
	}
}

func TestNormalizeDefinitionSnapshot_Nil(t *testing.T) {
	_, err := NormalizeDefinitionSnapshot(nil)
	if err == nil {
		t.Fatal("expected error for nil input")
	}
	_, err = ComputeDigest(nil)
	if err == nil {
		t.Fatal("expected error for nil input")
	}
}

func TestDefinitionSnapshot_FutureSchemaIsReadOnly(t *testing.T) {
	s := &WorkDefinitionSnapshot{SchemaVersion: SchemaVersion + 1}
	if _, err := NormalizeDefinitionSnapshot(s); err == nil {
		t.Fatal("NormalizeDefinitionSnapshot accepted a future schema")
	}
	if _, err := ComputeDigest(s); err == nil {
		t.Fatal("ComputeDigest accepted a future schema")
	}
}

func TestComputeDigest_DifferentContentDifferentDigest(t *testing.T) {
	a := &WorkDefinitionSnapshot{
		SchemaVersion: 1, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "bp:x", SchemaVersion: 1, Version: 1},
		PromptTemplate: "hello",
		Workflow:       WorkflowDef{},
		BlockSpecs:     []BlockSpec{},
	}
	b := &WorkDefinitionSnapshot{
		SchemaVersion: 1, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "bp:x", SchemaVersion: 1, Version: 1},
		PromptTemplate: "world",
		Workflow:       WorkflowDef{},
		BlockSpecs:     []BlockSpec{},
	}
	da, _ := ComputeDigest(a)
	db, _ := ComputeDigest(b)
	if da == db {
		t.Fatal("different content produced identical digest")
	}
}

func TestNormalizeDefinitionSnapshot_DigestPrefix(t *testing.T) {
	s := &WorkDefinitionSnapshot{
		SchemaVersion: 1, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "bp:x", SchemaVersion: 1, Version: 1},
		PromptTemplate: "test",
		Workflow:       WorkflowDef{},
		BlockSpecs:     []BlockSpec{},
	}
	n, err := NormalizeDefinitionSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(n.Digest, digestPrefix) {
		t.Fatalf("digest missing prefix: got %q, want prefix %q", n.Digest, digestPrefix)
	}
	hexPart := strings.TrimPrefix(n.Digest, digestPrefix)
	if len(hexPart) != 64 {
		t.Fatalf("expected 64 hex chars after prefix, got %d", len(hexPart))
	}
}

// ── Golden fixture ─────────────────────────────────────────────────────────

func TestGoldenWorkV1_RoundTrip(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden-work-v1.json")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var w Work
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal golden Work: %v", err)
	}

	// Required fields that must survive round-trip.
	if w.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", w.SchemaVersion)
	}
	if w.ID != "work-golden-001" {
		t.Errorf("ID = %q, want work-golden-001", w.ID)
	}
	if w.State != WorkCompleted {
		t.Errorf("State = %q, want completed", w.State)
	}
	if w.ArchiveState != ArchiveActive {
		t.Errorf("ArchiveState = %q, want active", w.ArchiveState)
	}
	if len(w.Blocks) != 2 {
		t.Errorf("len(Blocks) = %d, want 2", len(w.Blocks))
	}
	if len(w.Runs) != 1 {
		t.Errorf("len(Runs) = %d, want 1", len(w.Runs))
	}
	if len(w.Conclusions) != 1 {
		t.Errorf("len(Conclusions) = %d, want 1", len(w.Conclusions))
	}

	want, err := canonicalJSON(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("canonicalize golden fixture: %v", err)
	}
	got, err := canonicalJSON(w)
	if err != nil {
		t.Fatalf("canonicalize decoded Work: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("golden fixture loses or changes fields during Work round-trip")
	}
}

func TestGoldenWorkV1_DefSnapshotDigest(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden-work-v1.json")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var w Work
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	ds := w.Definition
	// Compute the digest for this definition snapshot.
	d, err := ComputeDigest(&ds)
	if err != nil {
		t.Fatal(err)
	}
	if d != ds.Digest {
		t.Fatalf("golden digest mismatch: computed %s, fixture %s", d, ds.Digest)
	}
}

func TestGoldenWorkV1_NoMutation(t *testing.T) {
	raw, err := os.ReadFile("testdata/golden-work-v1.json")
	if err != nil {
		t.Fatalf("read golden fixture: %v", err)
	}
	var w Work
	if err := json.Unmarshal(raw, &w); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	before, err := json.Marshal(w.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeDefinitionSnapshot(&w.Definition); err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(w.Definition)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("NormalizeDefinitionSnapshot mutated the golden definition")
	}
}
