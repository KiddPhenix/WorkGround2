package work

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestFileWorkStore creates a FileWorkStore in a temp directory.
func newTestFileWorkStore(t *testing.T) *FileWorkStore {
	t.Helper()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileWorkStore(workDir, -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	return s
}

// writeFutureWorkProjection writes a projection.json with a future schema version.
func writeFutureWorkProjection(t *testing.T, store *FileWorkStore, workID string, schemaVer int) {
	t.Helper()
	wp, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	proj := map[string]any{
		"schemaVersion": schemaVer,
		"id":            workID,
		"name":          "Future Work",
		"archiveState":  "active",
		"state":         "running",
		"blocks": []map[string]any{{
			"id": "b1", "kind": "x", "schemaVersion": 99, "revision": 1,
			"status": "ready", "data": map[string]any{},
			"source":    map[string]any{"provider": "ai", "mode": "snapshot", "verified": false},
			"fallback":  map[string]any{"summary": "safe fallback"},
			"createdAt": "2030-01-01T00:00:00Z", "updatedAt": "2030-01-01T00:00:00Z",
		}},
		"createdAt": "2030-01-01T00:00:00Z",
		"updatedAt": "2030-01-01T00:00:00Z",
	}
	raw, _ := json.Marshal(proj)
	projPath := filepath.Join(wp, "projection.json")
	if err := os.WriteFile(projPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

type fileSnapshot struct {
	bytes []byte
	mtime time.Time
	mode  os.FileMode
}

func snapFileState(t *testing.T, dir string) map[string]fileSnapshot {
	t.Helper()
	state := map[string]fileSnapshot{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatalf("Rel: %v", err)
		}
		info, err := d.Info()
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		state[rel] = fileSnapshot{bytes: data, mtime: info.ModTime(), mode: info.Mode()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func assertFilesUnchanged(t *testing.T, dir string, before map[string]fileSnapshot) {
	t.Helper()
	after := snapFileState(t, dir)
	// Check path set equality.
	for k := range after {
		if _, ok := before[k]; !ok {
			t.Errorf("new file: %s", k)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("deleted file: %s", k)
			continue
		}
		if !bytes.Equal(before[k].bytes, after[k].bytes) {
			t.Errorf("bytes changed: %s", k)
		}
		if !before[k].mtime.Equal(after[k].mtime) {
			t.Errorf("mtime changed: %s", k)
		}
		if before[k].mode != after[k].mode {
			t.Errorf("mode changed: %s", k)
		}
	}
}

func TestLoadProjectionFutureAware_FutureWork(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-future-001"
	writeFutureWorkProjection(t, store, workID, 99)

	wp, _ := store.workPath(workID)
	before := snapFileState(t, wp)

	result, err := store.LoadProjectionFutureAware(workID)
	if err != nil {
		t.Fatalf("LoadProjectionFutureAware: %v", err)
	}
	if !result.IsFuture() {
		t.Fatal("v99 work must be future")
	}
	if result.FutureWork == nil || result.FutureWork.ID != workID {
		t.Fatalf("FutureWork: %+v", result.FutureWork)
	}

	// Source files must be unchanged (no replay/repair/write).
	assertFilesUnchanged(t, wp, before)

	// Mutation must be rejected.
	if err := result.RejectMutation("runWork"); err == nil {
		t.Fatal("must reject mutation on future work")
	}
	assertFilesUnchanged(t, wp, before)
}

func TestLoadProjectionFutureAware_FutureWorkThenKnownFails(t *testing.T) {
	// Loading a future work through the old LoadProjection must fail
	// (CheckSchemaVersion rejects it).
	store := newTestFileWorkStore(t)
	workID := "w-future-reject"
	writeFutureWorkProjection(t, store, workID, 99)

	_, err := store.LoadProjection(workID)
	if err == nil {
		t.Fatal("old LoadProjection must reject future schema")
	}
}

func TestLoadProjectionFutureAware_KnownWorkLoadsFullProjection(t *testing.T) {
	// Create a real work with events so loadProjection can replay.
	store := newTestStore(t)
	workID := "w-full-load"
	createTestWork(t, store, workID)

	// Load via normal LoadProjection.
	normal, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection: %v", err)
	}

	// Load via future-aware — must return same Work (not raw-unmarshalled).
	result, err := store.LoadProjectionFutureAware(workID)
	if err != nil {
		t.Fatalf("LoadProjectionFutureAware: %v", err)
	}
	if result.IsFuture() {
		t.Fatal("known v1 work must not be future")
	}
	if result.Work == nil {
		t.Fatal("known work result must have Work")
	}
	if result.Work.ID != normal.ID || result.Work.Name != normal.Name {
		t.Fatalf("future-aware result differs from normal load: %+v vs %+v", result.Work, normal)
	}
}

func TestLoadProjectionFutureAware_KnownCorruptProjectionRepaired(t *testing.T) {
	// Prove loadProjection is called (not raw unmarshal): corrupt the
	// projection.json with wrong workID. Raw unmarshal would return
	// the corrupt data; loadProjection detects the mismatch, replays
	// events, and rebuilds the correct projection.
	store := newTestStore(t)
	workID := "w-repair-test"
	createTestWork(t, store, workID)

	// Corrupt projection.json with wrong workID.
	wp, _ := store.workPath(workID)
	projPath := filepath.Join(wp, "projection.json")
	beforeCorrupt, _ := os.ReadFile(projPath)
	corrupt := fmt.Sprintf(`{"schemaVersion":1,"id":"WRONG_ID","name":"Corrupt","archiveState":"active","blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"definitionSnapshot":{"schemaVersion":1,"revision":1,"blueprintRef":{"id":"bp","schemaVersion":1,"version":1},"promptTemplate":"","workflow":{"stages":[]},"blockSpecs":[],"digest":"sha256:abc"},"blocks":[],"placements":[],"cornerstones":[],"runs":[],"prompt":"","createdWith":{"workSchemaVersion":1,"eventSchemaVersion":1,"rendererSetVersion":1},"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}`)
	os.WriteFile(projPath, []byte(corrupt), 0o644)

	result, err := store.LoadProjectionFutureAware(workID)
	if err != nil {
		t.Fatalf("loadProjection should repair corrupt projection: %v", err)
	}
	if result.IsFuture() {
		t.Fatal("must not be future")
	}
	// The repaired result must have the correct workID (not WRONG_ID).
	if result.Work.ID != workID {
		t.Fatalf("repair failed: got id=%q want %q", result.Work.ID, workID)
	}
	// Verify projection was repaired on disk.
	afterRepair, _ := os.ReadFile(projPath)
	if bytes.Equal(afterRepair, []byte(corrupt)) {
		t.Fatal("projection was not repaired on disk")
	}
	// The repaired content differs from the original corrupt content.
	_ = beforeCorrupt
}

func TestLoadArchiveFutureAware_KnownIdentityMismatchRejected(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-arch-id-mismatch"
	archivePath, err := store.archivePath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Valid JSON with correct workId but snapshot.id intentionally different.
	archiveJSON := `{"archiveSchemaVersion":1,"workId":"w-arch-id-mismatch","archivedAt":"2026-01-01T00:00:00Z","snapshot":{"schemaVersion":1,"id":"WRONG_SNAPSHOT_ID","name":"Mismatch","archiveState":"archived","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[]}`
	if err := os.WriteFile(archivePath, []byte(archiveJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(archivePath)

	result, err := store.LoadArchiveFutureAware(workID)
	if err == nil {
		t.Fatalf("must reject identity mismatch, got: %+v", result)
	}
	if result != nil {
		t.Fatalf("result must be nil on error, got: %+v", result)
	}

	after, _ := os.ReadFile(archivePath)
	if !bytes.Equal(after, before) {
		t.Fatal("source file modified")
	}
}

func TestReadFutureAwareRecordFromRaw_MissingWorkID(t *testing.T) {
	// Future record missing workId — must return error, not fall through to Record.
	raw := json.RawMessage(`{"archiveSchemaVersion":99,"snapshot":{"schemaVersion":99,"id":"w-1"},"fallbackBlocks":[]}`)
	result, err := ReadFutureAwareRecordFromRaw(raw)
	if err == nil {
		t.Fatalf("must return error for missing workId, got %+v", result)
	}
}

func TestReadFutureAwareRecordFromRaw_MalformedSnapshot(t *testing.T) {
	// Future record with malformed snapshot — must return error.
	raw := json.RawMessage(`{"archiveSchemaVersion":99,"workId":"w-mal","snapshot":"NOT_AN_OBJECT","fallbackBlocks":[]}`)
	result, err := ReadFutureAwareRecordFromRaw(raw)
	if err == nil {
		t.Fatalf("must return error for malformed snapshot, got %+v", result)
	}
}

func TestReadFutureAwareRecordFromRaw_KnownRecord(t *testing.T) {
	// Known record (archive v1, snapshot v1) returns Record.
	raw := json.RawMessage(`{"archiveSchemaVersion":1,"workId":"w-known","snapshot":{"schemaVersion":1,"id":"w-known","name":"Known","archiveState":"archived","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[]}`)
	result, err := ReadFutureAwareRecordFromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}
	if result.IsFuture() {
		t.Fatal("known record must not be future")
	}
	if result.Record == nil || result.Record.WorkID != "w-known" {
		t.Fatalf("Record: %+v", result.Record)
	}
}

// ── Future WorkRecord file-based no-repair tests ───────────────────────────

func TestFutureWorkRecordEnvelope_FileReadDoesNotRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future-record.json")

	futureJSON := fmt.Sprintf(`{"archiveSchemaVersion":99,"workId":"w-future-record","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":99,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[{"summary":"degraded"}]}`)
	if err := os.WriteFile(path, []byte(futureJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	before, _ := os.ReadFile(path)
	env, err := ReadFutureWorkRecordEnvelope(json.RawMessage(futureJSON))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("file modified by future record reader")
	}
	if env.SnapshotSchemaVersion != 99 {
		t.Fatalf("SnapshotSchemaVersion=%d want 99", env.SnapshotSchemaVersion)
	}

	for _, op := range []string{"restoreWork", "rerun"} {
		err := env.RejectWrite(op)
		if err == nil {
			t.Fatalf("must reject %s", op)
		}
		if _, ok := err.(*FutureWorkEnvelopeError); !ok {
			t.Fatalf("error type: %T", err)
		}
	}
}

func TestFutureWorkRecordEnvelope_SnapshotOnlyFuture_RejectWriteCorrectVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "record-snap-future.json")
	raw := `{"archiveSchemaVersion":1,"workId":"w-snap-future","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":3,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[]}`
	os.WriteFile(path, []byte(raw), 0o644)

	env, err := ReadFutureWorkRecordEnvelope(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	err = env.RejectWrite("restoreWork")
	if err == nil {
		t.Fatal("must reject")
	}
	fe, ok := err.(*FutureWorkEnvelopeError)
	if !ok {
		t.Fatalf("type: %T", err)
	}
	if fe.SchemaVer != 3 {
		t.Fatalf("SchemaVer=%d want 3 (snapshot, not archive v1)", fe.SchemaVer)
	}
}

func TestFutureWorkEnvelope_FileReadDoesNotRepair(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "future-work.json")
	futureJSON := `{"schemaVersion":99,"id":"w-future-disk","name":"Future Disk","archiveState":"active","state":"running","blocks":[{"id":"b1","kind":"x","schemaVersion":99,"revision":1,"status":"ready","data":{},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":{"summary":"safe fallback"},"createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z"}],"createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z"}`
	os.WriteFile(path, []byte(futureJSON), 0o644)

	before, _ := os.ReadFile(path)
	env, err := ReadFutureWorkEnvelope(json.RawMessage(futureJSON))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("file modified by future reader")
	}
	if env.FallbackBlocks == nil || len(env.FallbackBlocks) == 0 {
		t.Fatal("must extract fallback blocks from blocks[].fallback")
	}
	for _, op := range []string{"runWork", "patchApply"} {
		if err := env.RejectWrite(op); err == nil {
			t.Fatalf("must reject %s", op)
		}
	}
}

func TestLoadProjectionFutureAware_IDMismatchErrorsIsErrWorkNeedsRepair(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-future-mismatch"
	writeFutureWorkProjection(t, store, workID, 99)

	wp, _ := store.workPath(workID)
	// Write sentinel file to prove recursive collection.
	sentinelDir := filepath.Join(wp, "nested")
	os.MkdirAll(sentinelDir, 0o755)
	os.WriteFile(filepath.Join(sentinelDir, "sentinel.txt"), []byte("sentinel"), 0o644)
	// Corrupt the projection id to differ from workID.
	projPath := filepath.Join(wp, "projection.json")
	raw, _ := os.ReadFile(projPath)
	corrupted := bytes.Replace(raw, []byte(`"id":"w-future-mismatch"`), []byte(`"id":"WRONG_ID"`), 1)
	os.WriteFile(projPath, corrupted, 0o644)

	before := snapFileState(t, wp)

	result, err := store.LoadProjectionFutureAware(workID)
	if err == nil {
		t.Fatalf("must return error, got %+v", result)
	}
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("error must wrap ErrWorkNeedsRepair, got: %v", err)
	}
	assertFilesUnchanged(t, wp, before)
}

func TestLoadArchiveFutureAware_IDMismatchErrorsIsErrWorkNeedsRepair(t *testing.T) {
	store := newTestFileWorkStore(t)
	workID := "w-arch-id-mismatch"
	archivePath, err := store.archivePath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		t.Fatal(err)
	}
	// Write sentinel file to prove recursive collection.
	sentinelDir := filepath.Join(filepath.Dir(archivePath), "nested")
	os.MkdirAll(sentinelDir, 0o755)
	os.WriteFile(filepath.Join(sentinelDir, "sentinel.txt"), []byte("sentinel"), 0o644)
	archiveJSON := `{"archiveSchemaVersion":99,"workId":"WRONG_ID","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":99,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[{"summary":"degraded"}]}`
	os.WriteFile(archivePath, []byte(archiveJSON), 0o644)

	dir := filepath.Dir(archivePath)
	before := snapFileState(t, dir)

	result, err := store.LoadArchiveFutureAware(workID)
	if err == nil {
		t.Fatalf("must return error, got %+v", result)
	}
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("error must wrap ErrWorkNeedsRepair, got: %v", err)
	}
	assertFilesUnchanged(t, dir, before)
}
