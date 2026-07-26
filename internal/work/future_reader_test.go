package work

import (
	"fmt"
	"strings"
	"testing"
)

// ── FutureWorkEnvelope tests ───────────────────────────────────────────────

func TestReadFutureWorkEnvelope_BlocksFallback(t *testing.T) {
	// Real Work JSON shape: blocks[].fallback, not top-level fallbackBlocks.
	raw := []byte(`{"schemaVersion":3,"id":"w-future","name":"Future Work","archiveState":"active","createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z","blocks":[{"id":"b1","kind":"unknown_v5","schemaVersion":5,"revision":1,"status":"ready","data":{},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":{"summary":"block 1 fallback"},"createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z"},{"id":"b2","kind":"unknown_v6","schemaVersion":6,"revision":1,"status":"ready","data":{},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":{"summary":"block 2 fallback"},"createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z"}]}`)
	env, err := ReadFutureWorkEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != 3 || env.ID != "w-future" {
		t.Fatalf("metadata: %+v", env)
	}
	if len(env.FallbackBlocks) != 2 {
		t.Fatalf("expected 2 fallback blocks from blocks[].fallback, got %d: %+v", len(env.FallbackBlocks), env.FallbackBlocks)
	}
	if env.FallbackBlocks[0].Summary != "block 1 fallback" {
		t.Fatalf("fallback[0]: %s", env.FallbackBlocks[0].Summary)
	}
	if env.FallbackBlocks[1].Summary != "block 2 fallback" {
		t.Fatalf("fallback[1]: %s", env.FallbackBlocks[1].Summary)
	}
}

func TestReadFutureWorkEnvelope_BrokenBlockFallbackSkipped(t *testing.T) {
	// One block has a broken fallback (not an object), one is valid.
	raw := []byte(`{"schemaVersion":3,"id":"w-broken","name":"Broken","archiveState":"active","createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z","blocks":[{"id":"b1","kind":"x","schemaVersion":9,"revision":1,"status":"ready","data":{},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":"NOT_AN_OBJECT","createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z"},{"id":"b2","kind":"y","schemaVersion":9,"revision":1,"status":"ready","data":{},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":{"summary":"valid fallback"},"createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z"}]}`)
	env, err := ReadFutureWorkEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.FallbackBlocks) != 1 {
		t.Fatalf("broken fallback should be skipped, keeping 1 valid, got %d", len(env.FallbackBlocks))
	}
	if env.FallbackBlocks[0].Summary != "valid fallback" {
		t.Fatalf("fallback: %s", env.FallbackBlocks[0].Summary)
	}
}

func TestReadFutureWorkEnvelope_RejectsCurrentOrPast(t *testing.T) {
	for _, ver := range []int{1, 2} {
		raw := []byte(fmt.Sprintf(`{"schemaVersion":%d,"id":"w%d","name":"W","archiveState":"active","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","blocks":[]}`, ver, ver))
		_, err := ReadFutureWorkEnvelope(raw)
		if err == nil {
			t.Fatalf("ReadFutureWorkEnvelope must reject v%d (within binary capability)", ver)
		}
	}
}

func TestReadFutureWorkEnvelope_RejectsMissingID(t *testing.T) {
	raw := []byte(`{"schemaVersion":3,"name":"No ID","archiveState":"active","createdAt":"2027-01-01T00:00:00Z","updatedAt":"2027-01-01T00:00:00Z","blocks":[]}`)
	_, err := ReadFutureWorkEnvelope(raw)
	if err == nil {
		t.Fatal("must reject future work with missing id")
	}
}

func TestReadFutureWorkEnvelope_RejectsInvalidJSON(t *testing.T) {
	_, err := ReadFutureWorkEnvelope([]byte(`{bad`))
	if err == nil {
		t.Fatal("must reject invalid JSON")
	}
}

func TestFutureWorkEnvelope_RawIsPreserved(t *testing.T) {
	raw := []byte(`{"schemaVersion":5,"id":"w-export","name":"Export","archiveState":"active","extra":"custom","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]}`)
	env, err := ReadFutureWorkEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if string(env.Raw) != string(raw) {
		t.Fatal("Raw must be byte-identical to input")
	}
}

func TestFutureWorkEnvelope_RejectWrite(t *testing.T) {
	raw := []byte(`{"schemaVersion":99,"id":"w-future","name":"F","archiveState":"active","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]}`)
	env, err := ReadFutureWorkEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"updateDraft", "runWork", "patchApply", "archiveWork"} {
		err := env.RejectWrite(op)
		if err == nil {
			t.Fatalf("RejectWrite(%q) must return error", op)
		}
		e, ok := err.(*FutureWorkEnvelopeError)
		if !ok {
			t.Fatalf("RejectWrite error type: %T", err)
		}
		if e.SchemaVer != 99 || e.BinaryMaxVer != SchemaVersionV2 || e.WorkID != "w-future" {
			t.Fatalf("FutureWorkEnvelopeError fields: %+v", e)
		}
		if !strings.Contains(e.Error(), "read-only") {
			t.Fatalf("error message missing read-only: %s", e.Error())
		}
	}
}

func TestFutureWorkEnvelope_MetadataOnly(t *testing.T) {
	raw := []byte(`{"schemaVersion":7,"id":"w-meta","name":"Meta Work","archiveState":"archived","state":"running","blocks":[{"id":"b1","kind":"unknown_v9","schemaVersion":9,"revision":1,"status":"ready","data":{"secret":"should not be parsed"},"source":{"provider":"ai","mode":"snapshot","verified":false},"fallback":{"summary":"degraded view"},"createdAt":"2030-06-01T00:00:00Z","updatedAt":"2030-06-02T00:00:00Z"}],"createdAt":"2030-06-01T00:00:00Z","updatedAt":"2030-06-02T00:00:00Z"}`)
	env, err := ReadFutureWorkEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "w-meta" || env.Name != "Meta Work" || env.ArchiveState != ArchiveArchived {
		t.Fatalf("metadata: %+v", env)
	}
	if len(env.FallbackBlocks) != 1 || env.FallbackBlocks[0].Summary != "degraded view" {
		t.Fatalf("fallback blocks: %+v", env.FallbackBlocks)
	}
	if string(env.Raw) != string(raw) {
		t.Fatal("Raw must be preserved for export")
	}
}

// ── FutureWorkRecordEnvelope tests ─────────────────────────────────────────

func TestReadFutureWorkRecord_FutureArchive(t *testing.T) {
	raw := []byte(`{"archiveSchemaVersion":5,"workId":"w-future-arch","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":7,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[{"summary":"degraded"}]}`)
	env, err := ReadFutureWorkRecordEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.ArchiveSchemaVersion != 5 || env.SnapshotSchemaVersion != 7 || env.WorkID != "w-future-arch" {
		t.Fatalf("metadata: %+v", env)
	}
	if len(env.FallbackBlocks) != 1 || env.FallbackBlocks[0].Summary != "degraded" {
		t.Fatalf("fallback: %+v", env.FallbackBlocks)
	}
	if string(env.Raw) != string(raw) {
		t.Fatal("Raw must be byte-identical")
	}
}

func TestReadFutureWorkRecord_SnapshotOnlyFuture(t *testing.T) {
	// archiveSchemaVersion is V1 but snapshot is V3 — still future.
	raw := []byte(`{"archiveSchemaVersion":1,"workId":"w-snap-future","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":3,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[]}`)
	env, err := ReadFutureWorkRecordEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.WorkID != "w-snap-future" {
		t.Fatalf("workId: %s", env.WorkID)
	}
	if env.SnapshotSchemaVersion != 3 {
		t.Fatalf("SnapshotSchemaVersion = %d, want 3", env.SnapshotSchemaVersion)
	}

	// RejectWrite must report v3 (the snapshot's version), NOT archive v1.
	err = env.RejectWrite("restoreWork")
	if err == nil {
		t.Fatal("RejectWrite must return error")
	}
	fe, ok := err.(*FutureWorkEnvelopeError)
	if !ok {
		t.Fatalf("error type: %T", err)
	}
	if fe.SchemaVer != 3 {
		t.Fatalf("RejectWrite SchemaVer = %d, want 3 (snapshot version, not archive v1)", fe.SchemaVer)
	}
}

func TestReadFutureWorkRecord_RejectsCurrent(t *testing.T) {
	raw := []byte(`{"archiveSchemaVersion":1,"workId":"w-current","archivedAt":"2026-01-01T00:00:00Z","snapshot":{"schemaVersion":1,"id":"w-1","name":"C","archiveState":"archived","createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":[]}`)
	_, err := ReadFutureWorkRecordEnvelope(raw)
	if err == nil {
		t.Fatal("must reject current-schema WorkRecord")
	}
}

func TestReadFutureWorkRecord_BrokenFallbackDegrades(t *testing.T) {
	raw := []byte(`{"archiveSchemaVersion":5,"workId":"w-broken-fb","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":7,"id":"w-1","name":"F","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]},"fallbackBlocks":"not-an-array"}`)
	env, err := ReadFutureWorkRecordEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.FallbackBlocks != nil {
		t.Fatalf("broken fallback must degrade to nil, got: %+v", env.FallbackBlocks)
	}
	if string(env.Raw) != string(raw) {
		t.Fatal("Raw must be preserved despite fallback parse failure")
	}
}

func TestReadFutureWorkRecord_RejectWrite(t *testing.T) {
	raw := []byte(`{"archiveSchemaVersion":9,"workId":"w-rw","archivedAt":"2030-01-01T00:00:00Z","snapshot":{"schemaVersion":9,"id":"w-1","name":"R","archiveState":"archived","createdAt":"2030-01-01T00:00:00Z","updatedAt":"2030-01-01T00:00:00Z","blocks":[]}}`)
	env, err := ReadFutureWorkRecordEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"restoreWork", "deleteWork", "rerun"} {
		if err := env.RejectWrite(op); err == nil {
			t.Fatalf("RejectWrite(%q) must return error", op)
		}
	}
}

func TestReadFutureWorkRecord_MissingSnapshot(t *testing.T) {
	raw := []byte(`{"archiveSchemaVersion":99,"workId":"w-no-snap","archivedAt":"2030-01-01T00:00:00Z"}`)
	env, err := ReadFutureWorkRecordEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.WorkID != "w-no-snap" {
		t.Fatalf("workId: %s", env.WorkID)
	}
}
