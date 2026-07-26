package work

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func v1ToV2Transform(src []byte) ([]byte, error) {
	var w struct {
		SchemaVersion int    `json:"schemaVersion"`
		ID            string `json:"id"`
		Name          string `json:"name"`
	}
	if err := json.Unmarshal(src, &w); err != nil {
		return nil, err
	}
	w.SchemaVersion = SchemaVersionV2
	return json.Marshal(w)
}

func TestMigrateV1FileToV2_Success(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1-work.json")
	dstPath := filepath.Join(dir, "v2-work.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	if err := os.WriteFile(srcPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	before, _ := os.ReadFile(srcPath)

	err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-001", v1ToV2Transform)
	if err != nil {
		t.Fatalf("MigrateV1FileToV2: %v", err)
	}

	// Source unchanged.
	after, _ := os.ReadFile(srcPath)
	if !bytes.Equal(after, before) {
		t.Fatal("source file was modified")
	}

	// Destination exists and is V2.
	dst, err := os.ReadFile(dstPath)
	if err != nil {
		t.Fatal("destination not created")
	}
	var header struct {
		SchemaVersion int    `json:"schemaVersion"`
		ID            string `json:"id"`
	}
	if err := json.Unmarshal(dst, &header); err != nil {
		t.Fatal(err)
	}
	if header.SchemaVersion != SchemaVersionV2 {
		t.Fatalf("dst schemaVersion=%d want %d", header.SchemaVersion, SchemaVersionV2)
	}
	if header.ID != "w1" {
		t.Fatalf("dst id=%s want w1", header.ID)
	}
}

func TestMigrateV1FileToV2_RetrySameRequestID(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1-work.json")
	dstPath := filepath.Join(dir, "v2-work.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	os.WriteFile(srcPath, []byte(src), 0o644)

	// First call succeeds.
	if err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-002", v1ToV2Transform); err != nil {
		t.Fatalf("first: %v", err)
	}

	// Second call with same requestID: idempotent.
	if err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-002", v1ToV2Transform); err != nil {
		t.Fatalf("retry: %v", err)
	}

	// Destination content unchanged after retry.
	dst, _ := os.ReadFile(dstPath)
	var header struct {
		SchemaVersion int `json:"schemaVersion"`
	}
	json.Unmarshal(dst, &header)
	if header.SchemaVersion != SchemaVersionV2 {
		t.Fatal("dst corrupted after retry")
	}
}

func TestMigrateV1FileToV2_FutureRejectZeroWrites(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "future-work.json")
	dstPath := filepath.Join(dir, "should-not-exist.json")
	src := `{"schemaVersion":99,"id":"w99","name":"future"}`
	os.WriteFile(srcPath, []byte(src), 0o644)

	err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-003", v1ToV2Transform)
	if err == nil {
		t.Fatal("must reject future schema")
	}

	// Destination must not exist.
	if _, e := os.Stat(dstPath); e == nil {
		t.Fatal("dst file created for future schema")
	}
	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestMigrateV1FileToV2_ConflictReject(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1-work.json")
	dstPath := filepath.Join(dir, "v2-work.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	os.WriteFile(srcPath, []byte(src), 0o644)

	// Pre-create dst with different content.
	os.WriteFile(dstPath, []byte(`{"schemaVersion":2,"id":"other","name":"conflict"}`), 0o644)

	err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-004", v1ToV2Transform)
	if err == nil {
		t.Fatal("must reject conflict")
	}
}

func TestMigrateV1FileToV2_RejectsEmptyRequestID(t *testing.T) {
	err := MigrateV1FileToV2("/tmp/a", "/tmp/b", "", v1ToV2Transform)
	if err == nil {
		t.Fatal("must reject empty requestID")
	}
}

func TestMigrateV1FileToV2_SourceBytesUnchangedAfterTransformFailure(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1-work.json")
	dstPath := filepath.Join(dir, "v2-work.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	os.WriteFile(srcPath, []byte(src), 0o644)
	before, _ := os.ReadFile(srcPath)

	// Transform that fails.
	failTransform := func([]byte) ([]byte, error) {
		return nil, os.ErrInvalid
	}
	err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-005", failTransform)
	if err == nil {
		t.Fatal("must fail on transform error")
	}

	// Source unchanged.
	after, _ := os.ReadFile(srcPath)
	if !bytes.Equal(after, before) {
		t.Fatal("source modified after transform failure")
	}

	// Retry with correct transform succeeds.
	if err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-005", v1ToV2Transform); err != nil {
		t.Fatalf("retry after failure: %v", err)
	}
	dst, _ := os.ReadFile(dstPath)
	var header struct{ SchemaVersion int }
	json.Unmarshal(dst, &header)
	if header.SchemaVersion != SchemaVersionV2 {
		t.Fatal("retry produced wrong schema version")
	}
}

func TestMigrateV1FileToV2_TransformOutputWrongSchema(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "v1-work.json")
	dstPath := filepath.Join(dir, "v2-work.json")
	src := `{"schemaVersion":1,"id":"w1","name":"test"}`
	os.WriteFile(srcPath, []byte(src), 0o644)

	// Transform that outputs V1 still.
	badTransform := func(src []byte) ([]byte, error) {
		return src, nil
	}
	err := MigrateV1FileToV2(srcPath, dstPath, "req-mig-006", badTransform)
	if err == nil {
		t.Fatal("must reject transform output with wrong schema version")
	}
}
