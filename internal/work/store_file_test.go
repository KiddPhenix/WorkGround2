package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/fileutil"
)

// ── Helpers ────────────────────────────────────────────────────────────────

func newTestStore(t *testing.T) *FileWorkStore {
	t.Helper()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileWorkStore(workDir, -1) // -1 = 30d default
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	return s
}

func newTestStoreWithRetention(t *testing.T, retention time.Duration) *FileWorkStore {
	t.Helper()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	s, err := NewFileWorkStore(workDir, retention)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	return s
}

func acquireStoreLease(t *testing.T, store *FileWorkStore, workID string) {
	t.Helper()
	wp, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(wp); err != nil {
		t.Fatalf("acquire lease for %s: %v", workID, err)
	}
	t.Cleanup(func() { _ = ReleaseWorkLease(wp) })
}

func createTestWork(t *testing.T, store *FileWorkStore, workID string) *Work {
	t.Helper()
	wp, err := store.workPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		t.Fatal(err)
	}

	w := &Work{
		SchemaVersion: SchemaVersion,
		ID:            workID,
		Name:          "Test Work",
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		BlueprintRef:  BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		CreatedWith:   RuntimeFingerprint{WorkSchemaVersion: SchemaVersion},
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}

	payload, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}

	acquireStoreLease(t, store, workID)
	evt := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-1",
		RequestID:     "req-create-" + workID,
		WorkID:        workID,
		Type:          EventWorkCreated,
		Payload:       json.RawMessage(payload),
	}

	rev, err := store.Append(workID, evt)
	if err != nil {
		t.Fatalf("create work: %v", err)
	}
	if rev != 1 {
		t.Fatalf("create work revision = %d, want 1", rev)
	}

	m := &workManifest{
		SchemaVersion: SchemaVersion,
		ID:            workID,
		Name:          "Test Work",
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		BlueprintRef:  w.BlueprintRef,
		CreatedAt:     w.CreatedAt,
		UpdatedAt:     w.UpdatedAt,
		Revision:      rev,
	}
	if err := store.WriteManifest(workID, m); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	return w
}

// ── NewFileWorkStore validation ────────────────────────────────────────────

func TestNewFileWorkStore_RejectsEmpty(t *testing.T) {
	_, err := NewFileWorkStore("", 0)
	if !errors.Is(err, ErrWorkEmptyWorkDir) {
		t.Fatalf("expected ErrWorkEmptyWorkDir, got: %v", err)
	}
}

func TestNewFileWorkStore_RejectsCWD(t *testing.T) {
	cwd, _ := os.Getwd()
	_, err := NewFileWorkStore(cwd, 0)
	if !errors.Is(err, ErrWorkEmptyWorkDir) {
		t.Fatalf("expected ErrWorkEmptyWorkDir for CWD, got: %v", err)
	}
	_, err = NewFileWorkStore(".", 0)
	if !errors.Is(err, ErrWorkEmptyWorkDir) {
		t.Fatalf("expected ErrWorkEmptyWorkDir for '.', got: %v", err)
	}
}

func TestNewFileWorkStore_RejectsFilesystemRoot(t *testing.T) {
	root := string(os.PathSeparator)
	if volume := filepath.VolumeName(t.TempDir()); volume != "" {
		root = volume + string(os.PathSeparator)
	}
	_, err := NewFileWorkStore(root, 0)
	if !errors.Is(err, ErrWorkEmptyWorkDir) {
		t.Fatalf("expected ErrWorkEmptyWorkDir for filesystem root %q, got: %v", root, err)
	}
}

func TestNewFileWorkStore_GCZeroDisabled(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	os.MkdirAll(workDir, 0o755)
	s, err := NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if s.TrashRetention() != 0 {
		t.Fatalf("retention = %v, want 0 (disabled)", s.TrashRetention())
	}
}

func TestNewFileWorkStore_NegativeUsesDefault(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	os.MkdirAll(workDir, 0o755)
	s, err := NewFileWorkStore(workDir, -1)
	if err != nil {
		t.Fatal(err)
	}
	if s.TrashRetention() != 30*24*time.Hour {
		t.Fatalf("retention = %v, want 30d", s.TrashRetention())
	}
}

// ── Digest validation ──────────────────────────────────────────────────────

func TestValidateDigest(t *testing.T) {
	tests := []struct {
		d       string
		wantErr bool
	}{
		{"sha256:" + strings.Repeat("a", 64), false},
		{"sha256:" + strings.Repeat("0", 64), false},
		{"sha256:" + strings.Repeat("f", 64), false},
		{"", true},
		{"sha256:abc", true},
		{"sha256:" + strings.Repeat("A", 64), true}, // uppercase
		{"sha256:" + strings.Repeat("g", 64), true}, // invalid hex
		{"sha256:" + strings.Repeat("a", 63), true}, // too short
		{"sha256:" + strings.Repeat("a", 65), true}, // too long
		{"md5:" + strings.Repeat("a", 32), true},    // wrong prefix
		{"../definitions/../../../", true},          // traversal attempt
		{"sha256:../../../../etc/passwd", true},     // traversal via digest
	}

	for _, tc := range tests {
		t.Run(tc.d[:min(20, len(tc.d))], func(t *testing.T) {
			err := validateDigest(tc.d)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateDigest(%q) error = %v, wantErr = %v", tc.d, err, tc.wantErr)
			}
		})
	}
}

func TestDigestPathTraversal(t *testing.T) {
	store := newTestStore(t)
	_, err := store.blobPath("work-1", "sha256:../../etc/passwd../../../../../../../../etc/passwd../../abcdef1234")
	if !errors.Is(err, ErrWorkInvalidDigest) {
		t.Fatalf("expected ErrWorkInvalidDigest for traversal digest, got: %v", err)
	}
	_, err = store.definitionPath("work-1", "sha256:00000000000000000000000000000000../../etc/passwd")
	if !errors.Is(err, ErrWorkInvalidDigest) {
		t.Fatalf("expected ErrWorkInvalidDigest, got: %v", err)
	}
}

// ── Work ID validation ─────────────────────────────────────────────────────

func TestValidateWorkID(t *testing.T) {
	tests := []struct {
		id      string
		wantErr bool
	}{
		{"", true},
		{"work-abc123", false},
		{"work_123", false},
		{".", true},
		{".locks", true},
		{"blueprints", true},
		{" work", true},
		{"work ", true},
		{"work.", true},
		{"work:name", true},
		{"work*name", true},
		{"work\x00name", true},
		{"../etc/passwd", true},
		{"..\\windows\\system32", true},
		{"foo/bar", true},
		{"foo\\bar", true},
		{"C:\\absolute\\path", true},
		{"CON", true},
		{"PRN", true},
		{"NUL", true},
		{"COM1", true},
		{"LPT1", true},
		{"con.txt", true},
		{"my-con", false},
		{"   ", true},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			err := validateWorkID(tc.id)
			if (err != nil) != tc.wantErr {
				t.Errorf("validateWorkID(%q) error = %v, wantErr = %v", tc.id, err, tc.wantErr)
			}
		})
	}
}

func TestFileWorkStore_PathTraversal(t *testing.T) {
	store := newTestStore(t)
	_, err := store.workPath("../../etc/passwd")
	if err == nil {
		t.Fatal("expected error for path traversal work ID")
	}
	_, err = store.LoadProjection("..\\bad")
	if err == nil {
		t.Fatal("expected error for path traversal on LoadProjection")
	}
}

// ── Create/Load/Projection ─────────────────────────────────────────────────

func TestFileWorkStore_CreateAndLoad(t *testing.T) {
	store := newTestStore(t)
	workID := "work-test-1"
	w := createTestWork(t, store, workID)

	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("loaded ID = %q, want %q", loaded.ID, workID)
	}
	if loaded.Name != w.Name {
		t.Fatalf("loaded Name = %q, want %q", loaded.Name, w.Name)
	}
}

func TestFileWorkStore_LoadNonExistent(t *testing.T) {
	store := newTestStore(t)
	_, err := store.LoadProjection("no-such-work")
	if !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("expected ErrWorkNotFound, got: %v", err)
	}
}

func TestFileWorkStore_AppendThenLoad(t *testing.T) {
	store := newTestStore(t)
	workID := "work-append-1"
	createTestWork(t, store, workID)

	acquireStoreLease(t, store, workID)
	updatePayload := json.RawMessage(`{"name":"Updated Name"}`)
	evt := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-2",
		RequestID:     "req-update-" + workID,
		WorkID:        workID,
		Type:          EventDraftUpdated,
		Payload:       updatePayload,
	}

	rev, err := store.Append(workID, evt)
	if err != nil {
		t.Fatalf("Append draft update: %v", err)
	}
	if rev != 2 {
		t.Fatalf("append revision = %d, want 2", rev)
	}

	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection after append: %v", err)
	}
	if loaded.Name != "Updated Name" {
		t.Fatalf("loaded Name = %q, want %q", loaded.Name, "Updated Name")
	}
}

func TestFileWorkStore_AppendUsesIndexedSteadyState(t *testing.T) {
	store := newTestStore(t)
	workID := "work-indexed-steady-state"
	createTestWork(t, store, workID)

	// The legacy test fixture rewrites the manifest without the projection
	// digest. One load performs the bounded migration and establishes trust.
	if _, err := store.LoadProjection(workID); err != nil {
		t.Fatalf("establish trusted projection: %v", err)
	}

	originalAppend := appendIndexedWorkEvent
	t.Cleanup(func() { appendIndexedWorkEvent = originalAppend })
	indexedAppends := 0
	appendIndexedWorkEvent = func(
		workDir string,
		event WorkEvent,
		sync bool,
		idx *WorkEventIndex,
		existingWorkID string,
	) (workEventAppendResult, error) {
		indexedAppends++
		return originalAppend(workDir, event, sync, idx, existingWorkID)
	}

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-indexed-2",
		RequestID:     "req-indexed-2",
		WorkID:        workID,
		Type:          EventDraftUpdated,
		Payload:       json.RawMessage(`{"name":"Indexed Name"}`),
	}
	revision, err := store.Append(workID, event)
	if err != nil || revision != 2 {
		t.Fatalf("indexed append = (%d, %v), want (2, nil)", revision, err)
	}
	revision, err = store.Append(workID, event)
	if err != nil || revision != 2 {
		t.Fatalf("indexed idempotent retry = (%d, %v), want (2, nil)", revision, err)
	}
	if indexedAppends != 2 {
		t.Fatalf("indexed append calls = %d, want 2", indexedAppends)
	}

	wp, _ := store.workPath(workID)
	replay, err := ReplayWorkEventLog(wp)
	if err != nil || len(replay.Events) != 2 {
		t.Fatalf("steady-state replay = %+v, err = %v", replay, err)
	}
	header, err := os.ReadFile(WorkEventIndexPath(wp))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(header, []byte(`"requestIndex"`)) || len(header) > 2048 {
		t.Fatalf("event index header rewrote request history: bytes=%d", len(header))
	}
	receipts, err := os.ReadFile(WorkRequestIndexPath(wp))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(receipts, []byte(event.RequestID)) {
		t.Fatalf("append-only request index lost %q", event.RequestID)
	}
}

func TestFileWorkStore_AppendMigratesEmbeddedRequestIndex(t *testing.T) {
	store := newTestStore(t)
	workID := "work-index-migration"
	createTestWork(t, store, workID)
	if _, err := store.LoadProjection(workID); err != nil {
		t.Fatalf("establish trusted projection: %v", err)
	}
	wp, _ := store.workPath(workID)
	legacy, err := ReadWorkEventIndex(wp)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(WorkRequestIndexPath(wp)); err != nil {
		t.Fatal(err)
	}
	legacyData, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fileutil.AtomicWriteFile(
		WorkEventIndexPath(wp),
		append(legacyData, '\n'),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	cacheKey, _ := filepath.Abs(wp)
	workEventIndexCache.Delete(filepath.Clean(cacheKey))

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-migrated-2",
		RequestID:     "req-migrated-2",
		WorkID:        workID,
		Type:          EventDraftUpdated,
		Payload:       json.RawMessage(`{"name":"Migrated"}`),
	}
	if revision, err := store.Append(workID, event); err != nil || revision != 2 {
		t.Fatalf("append after legacy index = (%d, %v), want (2, nil)", revision, err)
	}

	header, err := os.ReadFile(WorkEventIndexPath(wp))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(header, []byte(`"requestIndex"`)) {
		t.Fatal("legacy embedded request index was not migrated")
	}
	migrated, err := ReadWorkEventIndex(wp)
	if err != nil {
		t.Fatal(err)
	}
	if len(migrated.RequestIndex) != 2 {
		t.Fatalf("migrated request receipts = %d, want 2", len(migrated.RequestIndex))
	}
}

// ── Append recovery error ──────────────────────────────────────────────────

func TestFileWorkStore_AppendCommittedRecoveryError(t *testing.T) {
	store := newTestStore(t)
	workID := "work-recovery-1"
	createTestWork(t, store, workID)

	acquireStoreLease(t, store, workID)

	// Simulate: after appending event, corruption in projection write.
	// We inject a failure by making the projection path unwritable.
	// Since we can't easily do that on all platforms, we verify the error
	// type is detectable via errors.As.
	t.Run("errors.As", func(t *testing.T) {
		recErr := &ErrWorkCommittedRecovery{
			WorkID:      "w1",
			Revision:    1,
			Committed:   true,
			Cause:       "disk full",
			Recoverable: true,
		}
		var target *ErrWorkCommittedRecovery
		if !errors.As(recErr, &target) {
			t.Fatal("ErrWorkCommittedRecovery must be errors.As compatible")
		}
		if target.WorkID != "w1" || target.Revision != 1 {
			t.Fatalf("errors.As extracted wrong values: %+v", target)
		}
	})

	// After a normal append, verify the projection is loadable even
	// if we delete it — LoadProjection rebuilds from events.
	t.Run("delete projection then rebuild", func(t *testing.T) {
		updatePayload := json.RawMessage(`{"name":"Recovery Name"}`)
		evt := WorkEvent{
			SchemaVersion: WorkEventSchemaVersion,
			ID:            "evt-rec-1",
			RequestID:     "req-rec-" + workID,
			WorkID:        workID,
			Type:          EventDraftUpdated,
			Payload:       updatePayload,
		}
		rev, err := store.Append(workID, evt)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}

		projPath, _ := store.projectionPath(workID)
		if err := os.Remove(projPath); err != nil {
			t.Fatal(err)
		}

		loaded, loadErr := store.LoadProjection(workID)
		if loadErr != nil {
			t.Fatalf("LoadProjection after deletion: %v", loadErr)
		}
		if loaded.Name != "Recovery Name" {
			t.Fatalf("rebuilt name = %q, want %q", loaded.Name, "Recovery Name")
		}
		_ = rev
	})
}

func TestFileWorkStore_AppendProjectionFailureIsRecoverable(t *testing.T) {
	store := newTestStore(t)
	workID := "work-projection-failure"
	createTestWork(t, store, workID)

	originalWrite := writeDerivedFile
	t.Cleanup(func() { writeDerivedFile = originalWrite })
	injected := errors.New("injected projection write failure")
	failed := false
	writeDerivedFile = func(path string, data []byte, perm os.FileMode) error {
		if !failed && filepath.Base(path) == "projection.json" {
			failed = true
			return injected
		}
		return originalWrite(path, data, perm)
	}

	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-projection-failure",
		RequestID:     "req-projection-failure",
		WorkID:        workID,
		Type:          EventDraftUpdated,
		Payload:       json.RawMessage(`{"name":"Recovered Name"}`),
	}
	revision, err := store.Append(workID, event)
	if revision != 2 {
		t.Fatalf("Append revision = %d, want 2", revision)
	}
	var committed *ErrWorkCommittedRecovery
	if !errors.As(err, &committed) || !committed.Committed || !committed.Recoverable {
		t.Fatalf("expected recoverable committed error, got: %v", err)
	}
	if committed.RequestID != event.RequestID || !errors.Is(err, injected) {
		t.Fatalf("committed error lost context: %+v", committed)
	}

	writeDerivedFile = originalWrite
	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection recovery: %v", err)
	}
	if loaded.Name != "Recovered Name" {
		t.Fatalf("recovered projection name = %q", loaded.Name)
	}
	manifest, err := store.LoadManifest(workID)
	if err != nil || manifest.Revision != 2 {
		t.Fatalf("recovered manifest = %+v, err = %v", manifest, err)
	}
}

func TestFileWorkStore_AppendIndexFailureIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	workID := "work-index-failure"
	createTestWork(t, store, workID)

	originalWrite := writeWorkEventIndexAfterAppend
	t.Cleanup(func() { writeWorkEventIndexAfterAppend = originalWrite })
	injected := errors.New("injected event index failure")
	writeWorkEventIndexAfterAppend = func(string, *WorkEventIndex) error { return injected }
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-index-failure",
		RequestID:     "req-index-failure",
		WorkID:        workID,
		Type:          EventDraftUpdated,
		Payload:       json.RawMessage(`{"name":"Index Recovered"}`),
	}
	revision, err := store.Append(workID, event)
	var committed *ErrWorkCommittedRecovery
	if revision != 2 || !errors.As(err, &committed) || !errors.Is(err, injected) {
		t.Fatalf("Append = (%d, %v), want revision 2 committed recovery", revision, err)
	}

	writeWorkEventIndexAfterAppend = originalWrite
	revision, err = store.Append(workID, event)
	if err != nil || revision != 2 {
		t.Fatalf("idempotent retry = (%d, %v), want (2, nil)", revision, err)
	}
	wp, _ := store.workPath(workID)
	replay, err := ReplayWorkEventLog(wp)
	if err != nil || len(replay.Events) != 2 || replay.IndexNeedsRebuild {
		t.Fatalf("replay after retry = %+v, err = %v", replay, err)
	}
}

// ── Projection rebuild from events ─────────────────────────────────────────

func TestFileWorkStore_RebuildProjectionFromEvents(t *testing.T) {
	store := newTestStore(t)
	workID := "work-rebuild-1"
	createTestWork(t, store, workID)

	projPath, err := store.projectionPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(projPath); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection after projection deletion: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("rebuilt ID = %q, want %q", loaded.ID, workID)
	}
	if _, err := os.Stat(projPath); err != nil {
		t.Fatalf("projection not re-persisted after rebuild: %v", err)
	}
}

func TestFileWorkStore_LoadProjectionRepairsTamperSameRevision(t *testing.T) {
	store := newTestStore(t)
	workID := "work-tampered-projection"
	createTestWork(t, store, workID)

	projectionPath, err := store.projectionPath(workID)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var tampered Work
	if err := json.Unmarshal(data, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Name = "tampered"
	data, err = json.MarshalIndent(&tampered, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := fileutil.AtomicWriteFile(projectionPath, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	manifestBefore, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection tamper repair: %v", err)
	}
	if loaded.Name != "Test Work" {
		t.Fatalf("LoadProjection trusted same-revision tamper %q", loaded.Name)
	}

	repairedData, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var repaired Work
	if err := json.Unmarshal(repairedData, &repaired); err != nil {
		t.Fatal(err)
	}
	if repaired.Name != "Test Work" {
		t.Fatalf("persisted projection remained tampered: %q", repaired.Name)
	}
	manifestAfter, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatal(err)
	}
	if manifestAfter.Revision != manifestBefore.Revision {
		t.Fatalf("tamper repair changed revision: before=%d after=%d", manifestBefore.Revision, manifestAfter.Revision)
	}
}

func TestFileWorkStore_DamagedLogDoesNotTrustTamperedProjection(t *testing.T) {
	store := newTestStore(t)
	workID := "work-tampered-damaged-log"
	createTestWork(t, store, workID)
	projectionPath, _ := store.projectionPath(workID)
	data, err := os.ReadFile(projectionPath)
	if err != nil {
		t.Fatal(err)
	}
	var tampered Work
	if err := json.Unmarshal(data, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.Name = "tampered"
	data, err = json.MarshalIndent(&tampered, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(projectionPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	workDir, _ := store.workPath(workID)
	log, err := os.OpenFile(WorkEventLogPath(workDir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.WriteString("{torn-tail"); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.LoadProjection(workID)
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("LoadProjection = %v, want ErrWorkNeedsRepair", err)
	}
	if loaded == nil || loaded.Name != "Test Work" {
		t.Fatalf("damaged log returned untrusted snapshot: %+v", loaded)
	}
	after, readErr := os.ReadFile(projectionPath)
	if readErr != nil || string(after) != string(data) {
		t.Fatalf("damaged log must stay read-only: data=%q err=%v", after, readErr)
	}
}

func TestFileWorkStore_DoesNotProjectDamagedEventTail(t *testing.T) {
	store := newTestStore(t)
	workID := "work-damaged-event-tail"
	createTestWork(t, store, workID)
	projectionPath, _ := store.projectionPath(workID)
	if err := fileutil.AtomicWriteFile(projectionPath, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	workDir, _ := store.workPath(workID)
	log, err := os.OpenFile(WorkEventLogPath(workDir), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.WriteString("{torn-tail"); err != nil {
		_ = log.Close()
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = store.LoadProjection(workID)
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("LoadProjection = %v, want ErrWorkNeedsRepair", err)
	}
	data, readErr := os.ReadFile(projectionPath)
	if readErr != nil || string(data) != "not-json" {
		t.Fatalf("damaged log must not overwrite projection: data=%q err=%v", data, readErr)
	}
}

// ── Archive ────────────────────────────────────────────────────────────────

func TestFileWorkStore_WriteArchive(t *testing.T) {
	store := newTestStore(t)
	workID := "work-archive-1"
	createTestWork(t, store, workID)

	w := &Work{SchemaVersion: SchemaVersion, ID: workID, Name: "Archived Work", State: WorkCompleted, ArchiveState: ArchiveArchived}
	record := &WorkRecord{ArchiveSchemaVersion: SchemaVersion, WorkID: workID, Snapshot: *w, ArchivedAt: time.Now().UTC()}

	if err := store.WriteArchive(workID, record); err != nil {
		t.Fatalf("WriteArchive: %v", err)
	}
	loaded, err := store.LoadArchive(workID)
	if err != nil {
		t.Fatalf("LoadArchive: %v", err)
	}
	if loaded.WorkID != workID {
		t.Fatalf("archive WorkID = %q, want %q", loaded.WorkID, workID)
	}
}

func TestFileWorkStore_WriteArchiveImmutable(t *testing.T) {
	store := newTestStore(t)
	workID := "work-archive-immutable"
	createTestWork(t, store, workID)

	w := &Work{SchemaVersion: SchemaVersion, ID: workID, Name: "First"}
	record := &WorkRecord{ArchiveSchemaVersion: SchemaVersion, WorkID: workID, Snapshot: *w, ArchivedAt: time.Now().UTC()}
	if err := store.WriteArchive(workID, record); err != nil {
		t.Fatalf("first WriteArchive: %v", err)
	}

	w2 := &Work{SchemaVersion: SchemaVersion, ID: workID, Name: "Second"}
	record2 := &WorkRecord{ArchiveSchemaVersion: SchemaVersion, WorkID: workID, Snapshot: *w2, ArchivedAt: time.Now().UTC()}
	err := store.WriteArchive(workID, record2)
	if !errors.Is(err, ErrWorkArchiveExists) {
		t.Fatalf("expected ErrWorkArchiveExists, got: %v", err)
	}

	if err := store.WriteArchive(workID, record); err != nil {
		t.Fatalf("idempotent WriteArchive: %v", err)
	}
}

func TestFileWorkStore_WriteArchiveConcurrentImmutable(t *testing.T) {
	store := newTestStore(t)
	workID := "work-archive-concurrent"
	createTestWork(t, store, workID)
	records := []*WorkRecord{
		{ArchiveSchemaVersion: SchemaVersion, WorkID: workID, Snapshot: Work{SchemaVersion: SchemaVersion, ID: workID, Name: "First"}, ArchivedAt: time.Now().UTC()},
		{ArchiveSchemaVersion: SchemaVersion, WorkID: workID, Snapshot: Work{SchemaVersion: SchemaVersion, ID: workID, Name: "Second"}, ArchivedAt: time.Now().UTC()},
	}
	results := make(chan error, len(records))
	var wg sync.WaitGroup
	for _, record := range records {
		wg.Add(1)
		go func(value *WorkRecord) {
			defer wg.Done()
			results <- store.WriteArchive(workID, value)
		}(record)
	}
	wg.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrWorkArchiveExists):
			rejected++
		default:
			t.Fatalf("unexpected concurrent archive error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent archive results: succeeded=%d rejected=%d", succeeded, rejected)
	}
}

func TestFileWorkStore_WriteArchiveNil(t *testing.T) {
	store := newTestStore(t)
	err := store.WriteArchive("w1", nil)
	if !errors.Is(err, ErrWorkNilInput) {
		t.Fatalf("expected ErrWorkNilInput, got: %v", err)
	}
}

func TestFileWorkStore_LoadArchiveCorrupt(t *testing.T) {
	store := newTestStore(t)
	workID := "work-archive-corr"
	createTestWork(t, store, workID)

	ap, _ := store.archivePath(workID)
	os.MkdirAll(filepath.Dir(ap), 0o755)
	fileutil.AtomicWriteFile(ap, []byte("not json"), 0o644)

	_, err := store.LoadArchive(workID)
	if !errors.Is(err, ErrWorkNeedsRepair) || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("expected corrupt error, got: %v", err)
	}
}

// ── Definitions ────────────────────────────────────────────────────────────

func TestFileWorkStore_WriteLoadDefinition(t *testing.T) {
	store := newTestStore(t)
	workID := "work-def-1"
	createTestWork(t, store, workID)

	def := &WorkDefinitionSnapshot{
		SchemaVersion:  SchemaVersion,
		Revision:       1,
		BlueprintRef:   BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		PromptTemplate: "Hello",
		Workflow:       WorkflowDef{},
	}
	normalized, err := NormalizeDefinitionSnapshot(def)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.WriteDefinition(workID, normalized); err != nil {
		t.Fatalf("WriteDefinition: %v", err)
	}

	loaded, err := store.LoadDefinition(workID, normalized.Digest)
	if err != nil {
		t.Fatalf("LoadDefinition: %v", err)
	}
	if loaded.Digest != normalized.Digest {
		t.Fatalf("loaded digest = %q, want %q", loaded.Digest, normalized.Digest)
	}
}

func TestFileWorkStore_WriteDefinitionAlwaysNormalizes(t *testing.T) {
	store := newTestStore(t)
	workID := "work-def-norm"
	createTestWork(t, store, workID)

	// Write a definition with empty digest — it should be normalized.
	def := &WorkDefinitionSnapshot{
		SchemaVersion:  SchemaVersion,
		Revision:       1,
		BlueprintRef:   BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		PromptTemplate: "Auto",
		Workflow:       WorkflowDef{},
		Digest:         "", // empty — will be computed
	}
	if err := store.WriteDefinition(workID, def); err != nil {
		t.Fatalf("WriteDefinition with empty digest: %v", err)
	}
	// Should have been normalized and gotten a digest.
	normalized, _ := NormalizeDefinitionSnapshot(def)
	loaded, err := store.LoadDefinition(workID, normalized.Digest)
	if err != nil {
		t.Fatalf("LoadDefinition after auto-normalize: %v", err)
	}
	if loaded.Digest == "" {
		t.Fatal("definition was stored without digest")
	}
}

func TestFileWorkStore_LoadDefinitionDigestMismatch(t *testing.T) {
	store := newTestStore(t)
	workID := "work-def-corrupt"
	createTestWork(t, store, workID)

	def := &WorkDefinitionSnapshot{
		SchemaVersion: SchemaVersion, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		PromptTemplate: "Test", Workflow: WorkflowDef{},
	}
	normalized, _ := NormalizeDefinitionSnapshot(def)
	store.WriteDefinition(workID, normalized)

	defPath, _ := store.definitionPath(workID, normalized.Digest)
	tampered := &WorkDefinitionSnapshot{
		SchemaVersion: SchemaVersion, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		PromptTemplate: "Tampered", Workflow: WorkflowDef{},
	}
	tampered.Digest = normalized.Digest
	tamperedData, _ := json.MarshalIndent(tampered, "", "  ")
	tamperedData = append(tamperedData, '\n')
	fileutil.AtomicWriteFile(defPath, tamperedData, 0o644)

	_, err := store.LoadDefinition(workID, normalized.Digest)
	if !errors.Is(err, ErrWorkDigestMismatch) || !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("expected ErrWorkDigestMismatch, got: %v", err)
	}
}

func TestFileWorkStore_LoadDefinitionNil(t *testing.T) {
	store := newTestStore(t)
	workID := "work-def-nil"
	createTestWork(t, store, workID)
	err := store.WriteDefinition(workID, nil)
	if !errors.Is(err, ErrWorkNilInput) {
		t.Fatalf("expected ErrWorkNilInput, got: %v", err)
	}
}

// ── Blobs ──────────────────────────────────────────────────────────────────

func TestFileWorkStore_WriteReadBlob(t *testing.T) {
	store := newTestStore(t)
	workID := "work-blob-1"
	createTestWork(t, store, workID)

	data := []byte("hello blob world")
	hash := sha256.Sum256(data)
	digest := digestPrefix + fmt.Sprintf("%x", hash[:])

	if err := store.WriteBlob(workID, digest, data); err != nil {
		t.Fatalf("WriteBlob: %v", err)
	}
	read, err := store.ReadBlob(workID, digest)
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}
	if string(read) != string(data) {
		t.Fatalf("blob content = %q, want %q", string(read), string(data))
	}
}

func TestFileWorkStore_WriteBlobDigestMismatch(t *testing.T) {
	store := newTestStore(t)
	workID := "work-blob-mismatch"
	createTestWork(t, store, workID)

	digest := digestPrefix + strings.Repeat("0", 64)
	err := store.WriteBlob(workID, digest, []byte("hello"))
	if !errors.Is(err, ErrWorkDigestMismatch) {
		t.Fatalf("expected ErrWorkDigestMismatch, got: %v", err)
	}
}

func TestFileWorkStore_ReadBlobCorruption(t *testing.T) {
	store := newTestStore(t)
	workID := "work-blob-corrupt"
	createTestWork(t, store, workID)

	data := []byte("original content")
	hash := sha256.Sum256(data)
	digest := digestPrefix + fmt.Sprintf("%x", hash[:])
	store.WriteBlob(workID, digest, data)

	blobPath, _ := store.blobPath(workID, digest)
	os.WriteFile(blobPath, []byte("corrupted!!"), 0o644)

	_, err := store.ReadBlob(workID, digest)
	if !errors.Is(err, ErrWorkDigestMismatch) || !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("expected ErrWorkDigestMismatch, got: %v", err)
	}
}

func TestFileWorkStore_WriteBlobNil(t *testing.T) {
	store := newTestStore(t)
	digest := digestPrefix + strings.Repeat("a", 64)
	err := store.WriteBlob("w", digest, nil)
	if !errors.Is(err, ErrWorkNilInput) {
		t.Fatalf("expected ErrWorkNilInput, got: %v", err)
	}
}

// ── Manifest ───────────────────────────────────────────────────────────────

func TestFileWorkStore_WriteLoadManifest(t *testing.T) {
	store := newTestStore(t)
	workID := "work-manifest-1"
	createTestWork(t, store, workID)

	m, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if m.ID != workID {
		t.Fatalf("manifest ID = %q, want %q", m.ID, workID)
	}
}

func TestFileWorkStore_LoadManifestCorruptNeedsRepair(t *testing.T) {
	store := newTestStore(t)
	workID := "work-manifest-corrupt"
	createTestWork(t, store, workID)
	path, _ := store.manifestPath(workID)
	if err := fileutil.AtomicWriteFile(path, []byte("not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := store.LoadManifest(workID)
	if !errors.Is(err, ErrWorkNeedsRepair) {
		t.Fatalf("LoadManifest = %v, want ErrWorkNeedsRepair", err)
	}
}

// ── List / Index ───────────────────────────────────────────────────────────

func TestFileWorkStore_List(t *testing.T) {
	store := newTestStore(t)
	createTestWork(t, store, "work-list-1")
	createTestWork(t, store, "work-list-2")
	createTestWork(t, store, "work-list-3")

	results, err := store.List(WorkFilter{Limit: 100})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(results) < 3 {
		t.Fatalf("List returned %d results, want at least 3", len(results))
	}

	draftResults, err := store.List(WorkFilter{State: ptrState(WorkDraft), Limit: 100})
	if err != nil {
		t.Fatalf("List by state: %v", err)
	}
	for _, r := range draftResults {
		if r.State != WorkDraft {
			t.Fatalf("expected all draft, got %q", r.State)
		}
	}
}

func TestFileWorkStore_IndexRebuild(t *testing.T) {
	store := newTestStore(t)
	createTestWork(t, store, "work-idx-1")
	createTestWork(t, store, "work-idx-2")

	indexPath := store.indexPath()
	if _, err := os.Stat(indexPath); err == nil {
		os.Remove(indexPath)
	}

	results, err := store.List(WorkFilter{Limit: 100})
	if err != nil {
		t.Fatalf("List after index rebuild: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("rebuilt index has %d results, want at least 2", len(results))
	}
}

func TestFileWorkStore_ListIgnoresBlueprintStore(t *testing.T) {
	store := newTestStore(t)
	createTestWork(t, store, "work-with-blueprints")
	if err := os.MkdirAll(filepath.Join(store.WorkDir(), "blueprints", "example"), 0o755); err != nil {
		t.Fatal(err)
	}
	listed, err := store.List(WorkFilter{})
	if err != nil || len(listed) != 1 || listed[0].ID != "work-with-blueprints" {
		t.Fatalf("List with blueprint store = (%v, %v)", listed, err)
	}
}

// ── Trash / Restore ────────────────────────────────────────────────────────

func TestFileWorkStore_MoveToTrashAndRestore(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-1"
	createTestWork(t, store, workID)

	// Release the lease so MoveToTrash can clean up.
	wp, _ := store.workPath(workID)
	_ = ReleaseWorkLease(wp)

	// Use different requestIDs for trash and restore.
	if err := store.MoveToTrash(workID, "req-trash-1a"); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}

	_, err := store.LoadProjection(workID)
	if !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("expected ErrWorkNotFound for trashed work, got: %v", err)
	}

	if err := store.RestoreFromTrash(workID, "req-restore-1a"); err != nil {
		t.Fatalf("RestoreFromTrash: %v", err)
	}

	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection after restore: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("restored ID = %q, want %q", loaded.ID, workID)
	}
	manifest, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatalf("LoadManifest after restore: %v", err)
	}
	if manifest.DeletedAt != nil || manifest.ArchiveState != ArchiveActive {
		t.Fatalf("restored manifest state = %+v, want active and not deleted", manifest)
	}
}

func TestFileWorkStore_MoveToTrashBlocksWriter(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-writer"
	createTestWork(t, store, workID)

	err := store.MoveToTrash(workID, "req-trash-writer")
	if !errors.Is(err, ErrWorkLeaseHeld) {
		t.Fatalf("expected ErrWorkLeaseHeld, got: %v", err)
	}
	wp, _ := store.workPath(workID)
	if err := ReleaseWorkLease(wp); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveToTrash(workID, "req-trash-writer"); err != nil {
		t.Fatalf("MoveToTrash after writer release: %v", err)
	}
}

func TestFileWorkStore_MoveToTrashRetriesSourceCleanup(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-source-cleanup"
	createTestWork(t, store, workID)
	wp, _ := store.workPath(workID)
	tp, _ := store.trashPath(workID)
	_ = ReleaseWorkLease(wp)

	originalRename := renameWorkDir
	originalRemove := removeWorkDir
	t.Cleanup(func() {
		renameWorkDir = originalRename
		removeWorkDir = originalRemove
	})
	renameWorkDir = func(src, dst string) error {
		if filepath.Clean(src) == filepath.Clean(wp) && filepath.Clean(dst) == filepath.Clean(tp) {
			return errors.New("injected cross-volume rename failure")
		}
		return originalRename(src, dst)
	}
	removeFailed := false
	removeWorkDir = func(path string) error {
		if !removeFailed && filepath.Clean(path) == filepath.Clean(wp) {
			removeFailed = true
			return errors.New("injected source cleanup failure")
		}
		return originalRemove(path)
	}

	if err := store.MoveToTrash(workID, "req-source-cleanup"); err == nil {
		t.Fatal("expected first MoveToTrash to report source cleanup failure")
	}
	if !store.isDirWithData(wp) || !store.isDirWithData(tp) {
		t.Fatal("partial cross-volume move must keep both recoverable copies")
	}
	removeWorkDir = originalRemove
	if err := store.MoveToTrash(workID, "req-source-cleanup"); err != nil {
		t.Fatalf("same-request cleanup retry: %v", err)
	}
	if store.isDirWithData(wp) || !store.isDirWithData(tp) {
		t.Fatal("retry must converge to one trash copy")
	}
}

func TestFileWorkStore_MoveToTrashReportsCleanupMarkerFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-marker-failure"
	createTestWork(t, store, workID)
	wp, _ := store.workPath(workID)
	tp, _ := store.trashPath(workID)
	if err := ReleaseWorkLease(wp); err != nil {
		t.Fatal(err)
	}

	originalRename := renameWorkDir
	originalRemove := removeWorkDir
	originalWrite := writeDerivedFile
	t.Cleanup(func() {
		renameWorkDir = originalRename
		removeWorkDir = originalRemove
		writeDerivedFile = originalWrite
	})
	renameWorkDir = func(src, dst string) error {
		if samePath(src, wp) && samePath(dst, tp) {
			return errors.New("injected cross-volume move")
		}
		return originalRename(src, dst)
	}
	removeErr := errors.New("injected source removal failure")
	removeWorkDir = func(path string) error {
		if samePath(path, wp) {
			return removeErr
		}
		return originalRemove(path)
	}
	markerErr := errors.New("injected cleanup marker failure")
	writeDerivedFile = func(path string, data []byte, perm os.FileMode) error {
		if samePath(filepath.Dir(path), tp) && filepath.Base(path) == "cleanup-pending.json" && strings.Contains(string(data), removeErr.Error()) {
			return markerErr
		}
		return originalWrite(path, data, perm)
	}

	err := store.MoveToTrash(workID, "req-marker-failure")
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, removeErr) || !errors.Is(err, markerErr) {
		t.Fatalf("MoveToTrash lost joined recovery errors: %v", err)
	}
	if recovery.Operation != "trash" || recovery.WorkID != workID || recovery.RequestID != "req-marker-failure" ||
		recovery.Stage != "removing_source" || !samePath(recovery.CleanupPath, wp) ||
		!recovery.Committed || recovery.MarkerPersisted {
		t.Fatalf("cleanup recovery evidence = %+v", recovery)
	}

	removeWorkDir = originalRemove
	writeDerivedFile = originalWrite
	if err := store.MoveToTrash(workID, "req-marker-failure"); err != nil {
		t.Fatalf("same-request retry after marker failure: %v", err)
	}
	if store.isDirWithData(wp) || !store.isDirWithData(tp) {
		t.Fatal("marker failure retry did not converge to trash")
	}
}

func TestFileWorkStore_RestoreReportsCleanupMarkerFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-restore-marker-failure"
	createTestWork(t, store, workID)
	wp, _ := store.workPath(workID)
	tp, _ := store.trashPath(workID)
	if err := ReleaseWorkLease(wp); err != nil {
		t.Fatal(err)
	}
	if err := store.MoveToTrash(workID, "req-prepare-restore-marker"); err != nil {
		t.Fatalf("prepare trash: %v", err)
	}

	originalRename := renameWorkDir
	originalRemove := removeWorkDir
	originalWrite := writeDerivedFile
	t.Cleanup(func() {
		renameWorkDir = originalRename
		removeWorkDir = originalRemove
		writeDerivedFile = originalWrite
	})
	renameWorkDir = func(src, dst string) error {
		if samePath(src, tp) && samePath(dst, wp) {
			return errors.New("injected cross-volume restore")
		}
		return originalRename(src, dst)
	}
	removeErr := errors.New("injected trash source removal failure")
	removeWorkDir = func(path string) error {
		if samePath(path, tp) {
			return removeErr
		}
		return originalRemove(path)
	}
	markerErr := errors.New("injected restore cleanup marker failure")
	writeDerivedFile = func(path string, data []byte, perm os.FileMode) error {
		if samePath(filepath.Dir(path), wp) && filepath.Base(path) == "cleanup-pending.json" && strings.Contains(string(data), removeErr.Error()) {
			return markerErr
		}
		return originalWrite(path, data, perm)
	}

	err := store.RestoreFromTrash(workID, "req-restore-marker-failure")
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, removeErr) || !errors.Is(err, markerErr) {
		t.Fatalf("RestoreFromTrash lost joined recovery errors: %v", err)
	}
	if recovery.Operation != "restore" || recovery.WorkID != workID || recovery.RequestID != "req-restore-marker-failure" ||
		recovery.Stage != "removing_source" || !samePath(recovery.CleanupPath, tp) ||
		!recovery.Committed || recovery.MarkerPersisted {
		t.Fatalf("restore cleanup recovery evidence = %+v", recovery)
	}

	removeWorkDir = originalRemove
	writeDerivedFile = originalWrite
	if err := store.RestoreFromTrash(workID, "req-restore-marker-failure"); err != nil {
		t.Fatalf("same-request restore after marker failure: %v", err)
	}
	if !store.isDirWithData(wp) || store.isDirWithData(tp) {
		t.Fatal("restore marker failure retry did not converge to active work")
	}
}

func TestFileWorkStore_MoveToTrashReportsLeaseReleaseFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-release-failure"
	createTestWork(t, store, workID)
	wp, _ := store.workPath(workID)
	if err := ReleaseWorkLease(wp); err != nil {
		t.Fatal(err)
	}

	originalRelease := releaseStoreLease
	t.Cleanup(func() { releaseStoreLease = originalRelease })
	releaseErr := errors.New("injected lifecycle lease release failure")
	lifecycleLock := filepath.Join(store.WorkDir(), ".locks", workID)
	failed := false
	releaseStoreLease = func(path string) error {
		err := originalRelease(path)
		if !failed && samePath(path, lifecycleLock) {
			failed = true
			return errors.Join(err, releaseErr)
		}
		return err
	}

	err := store.MoveToTrash(workID, "req-release-failure")
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, releaseErr) {
		t.Fatalf("MoveToTrash lease release error = %v", err)
	}
	if !recovery.Committed || !recovery.MarkerPersisted || recovery.RequestID != "req-release-failure" || recovery.Stage != "done" {
		t.Fatalf("lease release recovery evidence = %+v", recovery)
	}

	releaseStoreLease = originalRelease
	if err := store.MoveToTrash(workID, "req-release-failure"); err != nil {
		t.Fatalf("same-request retry after release failure: %v", err)
	}
}

func TestFileWorkStore_AtomicMoveReportsTempCleanupFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-move-temp-cleanup"
	createTestWork(t, store, workID)
	wp, _ := store.workPath(workID)
	tp, _ := store.trashPath(workID)
	if err := ReleaseWorkLease(wp); err != nil {
		t.Fatal(err)
	}

	originalRename := renameWorkDir
	originalRemove := removeWorkDir
	originalWrite := writeDerivedFile
	t.Cleanup(func() {
		renameWorkDir = originalRename
		removeWorkDir = originalRemove
		writeDerivedFile = originalWrite
	})
	renameWorkDir = func(src, dst string) error {
		if samePath(src, wp) && samePath(dst, tp) {
			return errors.New("injected cross-volume move")
		}
		return originalRename(src, dst)
	}
	copyErr := errors.New("injected temp copy failure")
	copyFailed := false
	writeDerivedFile = func(path string, data []byte, perm os.FileMode) error {
		if !copyFailed && strings.HasPrefix(filepath.Base(filepath.Dir(path)), ".move-") && filepath.Base(path) != "cleanup-pending.json" {
			copyFailed = true
			return copyErr
		}
		return originalWrite(path, data, perm)
	}
	removeErr := errors.New("injected temp RemoveAll failure")
	removeFailed := false
	removeWorkDir = func(path string) error {
		if !removeFailed && strings.HasPrefix(filepath.Base(filepath.Clean(path)), ".move-") {
			removeFailed = true
			return removeErr
		}
		return originalRemove(path)
	}

	err := store.MoveToTrash(workID, "req-temp-cleanup")
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, copyErr) || !errors.Is(err, removeErr) {
		t.Fatalf("atomic move lost temp cleanup failures: %v", err)
	}
	if recovery.CleanupPath == "" || !recovery.MarkerPersisted || recovery.RequestID != "req-temp-cleanup" {
		t.Fatalf("temp cleanup recovery evidence = %+v", recovery)
	}
	markerPath := filepath.Join(recovery.CleanupPath, "cleanup-pending.json")
	if _, err := os.Stat(markerPath); err != nil {
		t.Fatalf("failed move temp marker missing: %v", err)
	}

	writeDerivedFile = originalWrite
	removeWorkDir = originalRemove
	if err := store.MoveToTrash(workID, "req-temp-cleanup"); err != nil {
		t.Fatalf("same-request temp cleanup retry: %v", err)
	}
	if _, err := os.Stat(recovery.CleanupPath); !os.IsNotExist(err) {
		t.Fatalf("retry left failed move temp %s: %v", recovery.CleanupPath, err)
	}
	if store.isDirWithData(wp) || !store.isDirWithData(tp) {
		t.Fatal("temp cleanup retry did not converge to trash")
	}
}

func TestFileWorkStore_MoveToTrashIdempotent(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-idemp"
	createTestWork(t, store, workID)

	wp, _ := store.workPath(workID)
	_ = ReleaseWorkLease(wp)

	if err := store.MoveToTrash(workID, "req-trash-2a"); err != nil {
		t.Fatalf("first MoveToTrash: %v", err)
	}
	if err := store.MoveToTrash(workID, "req-trash-2a"); err != nil {
		t.Fatalf("second MoveToTrash (idempotent): %v", err)
	}
	if err := store.RestoreFromTrash(workID, "req-restore-2a"); err != nil {
		t.Fatalf("RestoreFromTrash: %v", err)
	}
	if err := store.RestoreFromTrash(workID, "req-restore-2a"); err != nil {
		t.Fatalf("idempotent RestoreFromTrash: %v", err)
	}
	if err := store.MoveToTrash(workID, "req-trash-2b"); err != nil {
		t.Fatalf("MoveToTrash after completed restore: %v", err)
	}
}

func TestFileWorkStore_MoveToTrashRequestIDRequired(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-rid"
	createTestWork(t, store, workID)

	err := store.MoveToTrash(workID, "")
	if !errors.Is(err, ErrWorkRequestIDRequired) {
		t.Fatalf("expected ErrWorkRequestIDRequired, got: %v", err)
	}
}

func TestFileWorkStore_MoveToTrashConflictDifferentRequestID(t *testing.T) {
	store := newTestStore(t)
	workID := "work-trash-conflict"
	createTestWork(t, store, workID)

	// First trash: partial failure (simulated by writing cleanup-pending).
	cp := &cleanupPending{
		RequestID: "other-request",
		Operation: "trash",
		WorkID:    workID,
		Stage:     "copying",
	}
	wp, _ := store.workPath(workID)
	_ = ReleaseWorkLease(wp)
	store.writeCleanupPendingTo(wp, cp)

	// Different requestID must fail.
	err := store.MoveToTrash(workID, "my-request")
	if !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("expected ErrWorkRequestIDConflict, got: %v", err)
	}

	// Clean up and retry with correct requestID — must resolve.
	store.writeCleanupPendingTo(wp, &cleanupPending{
		RequestID: "my-request",
		Operation: "trash",
		WorkID:    workID,
		Stage:     "done",
	})
	if err := store.MoveToTrash(workID, "my-request"); err != nil {
		t.Fatalf("retry MoveToTrash: %v", err)
	}
}

func TestFileWorkStore_RestoreNonExistent(t *testing.T) {
	store := newTestStore(t)
	err := store.RestoreFromTrash("no-such-trash", "req-1")
	if !errors.Is(err, ErrWorkNotInTrash) {
		t.Fatalf("expected ErrWorkNotInTrash, got: %v", err)
	}
}

func TestFileWorkStore_TrashNonExistent(t *testing.T) {
	store := newTestStore(t)
	err := store.MoveToTrash("no-such-work", "req-1")
	if !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("expected ErrWorkNotFound, got: %v", err)
	}
}

// ── GC ─────────────────────────────────────────────────────────────────────

func TestFileWorkStore_GCTrashDisabled(t *testing.T) {
	store := newTestStoreWithRetention(t, 0)
	deleted, err := store.GCTrash("req-gc-1")
	if err != nil {
		t.Fatalf("GCTrash with 0 retention: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("GC with 0 retention deleted %d works", len(deleted))
	}
}

func TestFileWorkStore_GCTrash(t *testing.T) {
	store := newTestStoreWithRetention(t, 1*time.Millisecond)
	workID := "work-gc-1"
	createTestWork(t, store, workID)

	wp, _ := store.workPath(workID)
	_ = ReleaseWorkLease(wp)

	if err := store.MoveToTrash(workID, "req-gc-trash"); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}
	time.Sleep(10 * time.Millisecond)

	deleted, err := store.GCTrash("req-gc-2")
	if err != nil {
		t.Fatalf("GCTrash: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != workID {
		t.Fatalf("GC deleted %v, want [%s]", deleted, workID)
	}

	tp, _ := store.trashPath(workID)
	if _, err := os.Stat(tp); !os.IsNotExist(err) {
		t.Fatal("trash dir should be gone after GC")
	}
}

func TestFileWorkStore_GCTrashUsesDeletedAt(t *testing.T) {
	// Verify GC uses DeletedAt (from manifest), not UpdatedAt.
	// A work just deleted should NOT be immediately GC'd even if UpdatedAt is old.
	store := newTestStoreWithRetention(t, 200*time.Millisecond)
	workID := "work-gc-delat"
	createTestWork(t, store, workID)

	// Set manifest UpdatedAt to 1 hour ago, but DeletedAt to now.
	oldTime := time.Now().UTC().Add(-1 * time.Hour)
	m, _ := store.LoadManifest(workID)
	m.UpdatedAt = oldTime
	m.CreatedAt = oldTime
	_ = store.WriteManifest(workID, m)

	wp, _ := store.workPath(workID)
	_ = ReleaseWorkLease(wp)

	if err := store.MoveToTrash(workID, "req-gc-delat"); err != nil {
		t.Fatalf("MoveToTrash: %v", err)
	}

	// GC immediately — the DeletedAt was just set, so it should NOT be deleted.
	deleted, err := store.GCTrash("req-gc-3")
	if err != nil {
		t.Fatalf("GCTrash: %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("GC deleted %v immediately after trash — DeletedAt should prevent this", deleted)
	}

	// Wait for retention.
	time.Sleep(250 * time.Millisecond)

	deleted2, err := store.GCTrash("req-gc-4")
	if err != nil {
		t.Fatalf("GCTrash: %v", err)
	}
	if len(deleted2) != 1 || deleted2[0] != workID {
		t.Fatalf("GC after retention should delete, got %v", deleted2)
	}
}

func TestFileWorkStore_GCTrashRetriesPartialFailure(t *testing.T) {
	store := newTestStoreWithRetention(t, time.Nanosecond)
	workIDs := []string{"work-gc-partial-a", "work-gc-partial-b"}
	for _, workID := range workIDs {
		createTestWork(t, store, workID)
		wp, _ := store.workPath(workID)
		_ = ReleaseWorkLease(wp)
		if err := store.MoveToTrash(workID, "trash-"+workID); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(time.Millisecond)

	originalRemove := removeWorkDir
	t.Cleanup(func() { removeWorkDir = originalRemove })
	failed := false
	failedPath, _ := store.trashPath(workIDs[0])
	removeWorkDir = func(path string) error {
		if !failed && filepath.Clean(path) == filepath.Clean(failedPath) {
			failed = true
			return errors.New("injected GC remove failure")
		}
		return originalRemove(path)
	}

	first, err := store.GCTrash("req-gc-partial")
	if err == nil || len(first) != 1 {
		t.Fatalf("first GC = (%v, %v), want one deletion plus retryable error", first, err)
	}
	removeWorkDir = originalRemove
	second, err := store.GCTrash("req-gc-partial")
	if err != nil || len(second) != 2 {
		t.Fatalf("GC retry = (%v, %v), want two stable deletions", second, err)
	}
	third, err := store.GCTrash("req-gc-partial")
	if err != nil || strings.Join(second, ",") != strings.Join(third, ",") {
		t.Fatalf("completed GC retry changed result: second=%v third=%v err=%v", second, third, err)
	}
}

// ── Concurrent writer protection ───────────────────────────────────────────

func TestFileWorkStore_LeaseProtection(t *testing.T) {
	store := newTestStore(t)
	workID := "work-lease-1"
	wp, _ := store.workPath(workID)
	os.MkdirAll(wp, 0o755)

	evt := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-1", RequestID: "req-no-lease", WorkID: workID,
		Type:    EventWorkCreated,
		Payload: json.RawMessage(`{"id":"work-lease-1","name":"test"}`),
	}
	_, err := store.Append(workID, evt)
	if !errors.Is(err, ErrWorkLeaseRequired) {
		t.Fatalf("expected ErrWorkLeaseRequired, got: %v", err)
	}

	acquireStoreLease(t, store, workID)
	evt2 := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-lease-1", RequestID: "req-with-lease", WorkID: workID,
		Type:    EventWorkCreated,
		Payload: json.RawMessage(`{"id":"work-lease-1","name":"test"}`),
	}
	rev, err := store.Append(workID, evt2)
	if err != nil {
		t.Fatalf("Append with lease: %v", err)
	}
	if rev != 1 {
		t.Fatalf("revision = %d, want 1", rev)
	}
}

// ── WriteProjection ────────────────────────────────────────────────────────

func TestFileWorkStore_WriteProjection(t *testing.T) {
	store := newTestStore(t)
	workID := "work-proj-1"
	createTestWork(t, store, workID)

	w := &Work{SchemaVersion: SchemaVersion, ID: workID, Name: "Written Projection", State: WorkDraft, ArchiveState: ArchiveActive}
	if err := store.WriteProjection(workID, w, 1); err != nil {
		t.Fatalf("WriteProjection: %v", err)
	}
	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection: %v", err)
	}
	if loaded.Name != "Test Work" {
		t.Fatalf("loaded Name = %q, want event-log truth %q", loaded.Name, "Test Work")
	}
}

func TestFileWorkStore_WriteProjectionMismatchedID(t *testing.T) {
	store := newTestStore(t)
	workID := "work-proj-mismatch"
	createTestWork(t, store, workID)

	w := &Work{SchemaVersion: SchemaVersion, ID: "different-id", Name: "Wrong"}
	err := store.WriteProjection(workID, w, 1)
	if err == nil {
		t.Fatal("expected error for mismatched work ID")
	}
}

func TestFileWorkStore_WriteProjectionNil(t *testing.T) {
	store := newTestStore(t)
	err := store.WriteProjection("w", nil, 1)
	if !errors.Is(err, ErrWorkNilInput) {
		t.Fatalf("expected ErrWorkNilInput, got: %v", err)
	}
}

// ── Cleanup-pending ────────────────────────────────────────────────────────

func TestFileWorkStore_CleanupPending(t *testing.T) {
	store := newTestStore(t)
	workID := "work-cp-1"
	wp, _ := store.workPath(workID)
	os.MkdirAll(wp, 0o755)

	cp := &cleanupPending{
		RequestID: "req-cp-1", Operation: "trash", WorkID: workID,
		Stage: "copying", StartedAt: time.Now().UTC(),
	}
	if err := store.writeCleanupPendingTo(wp, cp); err != nil {
		t.Fatalf("writeCleanupPendingTo: %v", err)
	}

	loaded, err := store.LoadCleanupPending(workID)
	if err != nil {
		t.Fatalf("LoadCleanupPending: %v", err)
	}
	if loaded == nil || loaded.RequestID != "req-cp-1" {
		t.Fatalf("Loaded cleanup pending = %+v", loaded)
	}
}

// ── Default work dir does not pollute repo ─────────────────────────────────

func TestFileWorkStore_DefaultsNotInRepo(t *testing.T) {
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works")
	os.MkdirAll(workDir, 0o755)
	store, err := NewFileWorkStore(workDir, -1)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(filepath.ToSlash(store.WorkDir()), filepath.ToSlash(dir)) {
		t.Fatalf("WorkDir %q must be under temp dir %q", store.WorkDir(), dir)
	}
	if !strings.Contains(store.TrashDir(), ".trash") {
		t.Fatalf("TrashDir %q should contain .trash", store.TrashDir())
	}
}

func TestFileWorkStore_EmptyDirRejected(t *testing.T) {
	_, err := NewFileWorkStore("", 0)
	if err == nil {
		t.Fatal("expected error for empty dir")
	}
	_, err = NewFileWorkStore("   ", 0)
	if err == nil {
		t.Fatal("expected error for whitespace dir")
	}
}

// ── Unicode / long paths ───────────────────────────────────────────────────

func TestFileWorkStore_UnicodeWorkID(t *testing.T) {
	store := newTestStore(t)
	workID := "work-测试-日本語"
	createTestWork(t, store, workID)
	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection unicode: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("unicode ID = %q, want %q", loaded.ID, workID)
	}
}

func TestFileWorkStore_LongWorkID(t *testing.T) {
	store := newTestStore(t)
	workID := "work-" + strings.Repeat("a", 195)
	createTestWork(t, store, workID)
	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection long ID: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("long ID mismatch")
	}
}

// ── CreateWorkDir (atomic directory creation) ──────────────────────────────

func TestFileWorkStore_CreateWorkDir(t *testing.T) {
	store := newTestStore(t)
	workID := "work-atomic-1"

	w := &Work{
		SchemaVersion: SchemaVersion, ID: workID, Name: "Atomic Work",
		State: WorkDraft, ArchiveState: ArchiveActive,
		BlueprintRef: BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		CreatedWith:  RuntimeFingerprint{WorkSchemaVersion: SchemaVersion},
		CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	payload, _ := json.Marshal(w)

	evt := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-1", RequestID: "req-atomic-1", WorkID: workID,
		Type: EventWorkCreated, Payload: json.RawMessage(payload),
		CreatedAt: time.Now().UTC(),
	}

	def := &WorkDefinitionSnapshot{
		SchemaVersion: SchemaVersion, Revision: 1,
		BlueprintRef:   BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
		PromptTemplate: "test", Workflow: WorkflowDef{},
	}

	input := CreateWorkDirInput{
		RequestID:  "req-create-atomic-1",
		Work:       w,
		Definition: def,
		Events:     []WorkEvent{evt},
	}

	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("CreateWorkDir: %v", err)
	}

	loaded, err := store.LoadProjection(workID)
	if err != nil {
		t.Fatalf("LoadProjection after atomic create: %v", err)
	}
	if loaded.ID != workID {
		t.Fatalf("loaded ID = %q, want %q", loaded.ID, workID)
	}
	manifest, err := store.LoadManifest(workID)
	if err != nil {
		t.Fatalf("LoadManifest after atomic create: %v", err)
	}
	if manifest.ProjectionDigest == "" {
		t.Fatal("atomic create did not establish trusted projection digest")
	}

	// Idempotent.
	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("idempotent CreateWorkDir: %v", err)
	}
	conflict := input
	conflict.RequestID = "req-create-atomic-other"
	if err := store.CreateWorkDir(conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("different-request CreateWorkDir = %v, want ErrWorkRequestIDConflict", err)
	}
}

func TestFileWorkStore_CreateWorkDirCleansFailedTemp(t *testing.T) {
	store := newTestStore(t)
	workID := "work-create-cleanup"
	input := CreateWorkDirInput{
		RequestID: "req-create-cleanup",
		Work: &Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          "Cleanup",
			State:         WorkDraft,
			ArchiveState:  ArchiveActive,
			BlueprintRef:  BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		Blobs: map[string][]byte{digestPrefix + strings.Repeat("0", 64): []byte("wrong digest")},
	}
	if err := store.CreateWorkDir(input); !errors.Is(err, ErrWorkDigestMismatch) {
		t.Fatalf("CreateWorkDir = %v, want digest error", err)
	}
	entries, err := os.ReadDir(store.WorkDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".new-"+workID+"-") {
			t.Fatalf("failed create exposed temp directory %q", entry.Name())
		}
	}
	wp, _ := store.workPath(workID)
	if _, err := os.Stat(wp); !os.IsNotExist(err) {
		t.Fatalf("failed create exposed final directory: %v", err)
	}
}

func TestFileWorkStore_CreateWorkDirReportsTempCleanupFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-create-cleanup-failure"
	input := CreateWorkDirInput{
		RequestID: "req-create-cleanup-failure",
		Work: &Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          "Cleanup failure",
			State:         WorkDraft,
			ArchiveState:  ArchiveActive,
			BlueprintRef:  BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		Blobs: map[string][]byte{digestPrefix + strings.Repeat("0", 64): []byte("wrong digest")},
	}

	originalRemove := removeWorkDir
	t.Cleanup(func() { removeWorkDir = originalRemove })
	removeErr := errors.New("injected create temp RemoveAll failure")
	failed := false
	removeWorkDir = func(path string) error {
		if !failed && strings.HasPrefix(filepath.Base(filepath.Clean(path)), ".new-"+workID+"-") {
			failed = true
			return removeErr
		}
		return originalRemove(path)
	}

	err := store.CreateWorkDir(input)
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, ErrWorkDigestMismatch) || !errors.Is(err, removeErr) {
		t.Fatalf("CreateWorkDir cleanup failure = %v", err)
	}
	if recovery.CleanupPath == "" || !recovery.MarkerPersisted || recovery.RequestID != input.RequestID || recovery.Committed {
		t.Fatalf("create cleanup recovery evidence = %+v", recovery)
	}
	if _, err := os.Stat(filepath.Join(recovery.CleanupPath, "cleanup-pending.json")); err != nil {
		t.Fatalf("create cleanup marker missing: %v", err)
	}

	removeWorkDir = originalRemove
	input.Blobs = nil
	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("same-request create cleanup retry: %v", err)
	}
	if _, err := os.Stat(recovery.CleanupPath); !os.IsNotExist(err) {
		t.Fatalf("create retry left stale temp %s: %v", recovery.CleanupPath, err)
	}
	wp, _ := store.workPath(workID)
	if !store.isDirWithData(wp) {
		t.Fatal("create retry did not expose final work")
	}
}

func TestFileWorkStore_CreateWorkDirRetriesMarkerlessTemp(t *testing.T) {
	store := newTestStore(t)
	workID := "work-create-markerless-cleanup"
	input := CreateWorkDirInput{
		RequestID: "req-create-markerless-cleanup",
		Work: &Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          "Markerless cleanup",
			State:         WorkDraft,
			ArchiveState:  ArchiveActive,
			BlueprintRef:  BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
		Blobs: map[string][]byte{digestPrefix + strings.Repeat("0", 64): []byte("wrong digest")},
	}

	originalRemove := removeWorkDir
	originalWrite := writeDerivedFile
	t.Cleanup(func() {
		removeWorkDir = originalRemove
		writeDerivedFile = originalWrite
	})
	removeErr := errors.New("injected markerless create RemoveAll failure")
	removeWorkDir = func(path string) error {
		if strings.HasPrefix(filepath.Base(filepath.Clean(path)), ".new-"+workID+"-") {
			return removeErr
		}
		return originalRemove(path)
	}
	markerErr := errors.New("injected markerless create marker failure")
	writeDerivedFile = func(path string, data []byte, perm os.FileMode) error {
		if filepath.Base(path) == "cleanup-pending.json" && strings.HasPrefix(filepath.Base(filepath.Dir(path)), ".new-"+workID+"-") {
			return markerErr
		}
		return originalWrite(path, data, perm)
	}

	err := store.CreateWorkDir(input)
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, ErrWorkDigestMismatch) || !errors.Is(err, removeErr) || !errors.Is(err, markerErr) {
		t.Fatalf("CreateWorkDir markerless cleanup errors = %v", err)
	}
	if recovery.Operation != "create" || recovery.WorkID != workID || recovery.RequestID != input.RequestID ||
		recovery.Stage != "cleanup_failed" || recovery.CleanupPath == "" || recovery.Committed || recovery.MarkerPersisted {
		t.Fatalf("markerless cleanup recovery evidence = %+v", recovery)
	}
	if _, err := os.Stat(filepath.Join(recovery.CleanupPath, "cleanup-pending.json")); !os.IsNotExist(err) {
		t.Fatalf("cleanup marker unexpectedly persisted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recovery.CleanupPath, "manifest.json")); err != nil {
		t.Fatalf("markerless temp lost request manifest evidence: %v", err)
	}

	removeWorkDir = originalRemove
	writeDerivedFile = originalWrite
	input.Blobs = nil
	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("same-request markerless cleanup retry: %v", err)
	}
	if _, err := os.Stat(recovery.CleanupPath); !os.IsNotExist(err) {
		t.Fatalf("create retry left markerless temp %s: %v", recovery.CleanupPath, err)
	}
	wp, _ := store.workPath(workID)
	if !store.isDirWithData(wp) {
		t.Fatal("markerless cleanup retry did not expose final work")
	}
}

func TestFileWorkStore_CreateWorkDirReportsLeaseReleaseFailure(t *testing.T) {
	store := newTestStore(t)
	workID := "work-create-release-failure"
	input := CreateWorkDirInput{
		RequestID: "req-create-release-failure",
		Work: &Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          "Release failure",
			State:         WorkDraft,
			ArchiveState:  ArchiveActive,
			BlueprintRef:  BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
			CreatedAt:     time.Now().UTC(),
			UpdatedAt:     time.Now().UTC(),
		},
	}

	originalRelease := releaseStoreLease
	t.Cleanup(func() { releaseStoreLease = originalRelease })
	releaseErr := errors.New("injected create lease release failure")
	failed := false
	releaseStoreLease = func(path string) error {
		err := originalRelease(path)
		if !failed && strings.HasPrefix(filepath.Base(filepath.Clean(path)), ".new-"+workID+"-") {
			failed = true
			return errors.Join(err, releaseErr)
		}
		return err
	}

	err := store.CreateWorkDir(input)
	var recovery *ErrWorkCleanupRecovery
	if !errors.As(err, &recovery) || !errors.Is(err, releaseErr) || !strings.Contains(err.Error(), input.RequestID) {
		t.Fatalf("CreateWorkDir lease release error lost context: %v", err)
	}
	if recovery.Operation != "create" || recovery.WorkID != workID || recovery.RequestID != input.RequestID ||
		recovery.Stage != "lease_release_failed" || recovery.CleanupPath == "" || recovery.Committed || recovery.MarkerPersisted {
		t.Fatalf("create lease recovery evidence = %+v", recovery)
	}
	if !strings.HasPrefix(filepath.Base(filepath.Clean(recovery.CleanupPath)), ".new-"+workID+"-") ||
		!samePath(filepath.Dir(recovery.CleanupPath), store.WorkDir()) {
		t.Fatalf("create lease cleanup path = %q", recovery.CleanupPath)
	}
	wp, _ := store.workPath(workID)
	if store.isDirWithData(wp) {
		t.Fatal("create with temp lease release failure exposed final work")
	}

	releaseStoreLease = originalRelease
	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("same-request create retry after lease release failure: %v", err)
	}
}

func TestFileWorkStore_ConcurrentCreateKeepsIndex(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works")
	stores := make([]*FileWorkStore, 2)
	for i := range stores {
		var err error
		stores[i], err = NewFileWorkStore(workDir, -1)
		if err != nil {
			t.Fatal(err)
		}
	}
	ids := []string{"work-concurrent-a", "work-concurrent-b"}
	var wg sync.WaitGroup
	errs := make(chan error, len(ids))
	for i, workID := range ids {
		wg.Add(1)
		go func(store *FileWorkStore, id string) {
			defer wg.Done()
			now := time.Now().UTC()
			errs <- store.CreateWorkDir(CreateWorkDirInput{
				RequestID: "req-" + id,
				Work: &Work{
					SchemaVersion: SchemaVersion, ID: id, Name: id,
					State: WorkDraft, ArchiveState: ArchiveActive,
					BlueprintRef: BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
					CreatedAt:    now, UpdatedAt: now,
				},
			})
		}(stores[i], workID)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent CreateWorkDir: %v", err)
		}
	}
	listed, err := stores[0].List(WorkFilter{})
	if err != nil || len(listed) != len(ids) {
		t.Fatalf("List after concurrent create = (%v, %v)", listed, err)
	}
}

func TestFileWorkStore_CreateWorkDirWithBlobs(t *testing.T) {
	store := newTestStore(t)
	workID := "work-atomic-blob"

	data := []byte("blob content")
	hash := sha256.Sum256(data)
	digest := digestPrefix + fmt.Sprintf("%x", hash[:])

	input := CreateWorkDirInput{
		RequestID: "req-create-blob",
		Work: &Work{
			SchemaVersion: SchemaVersion, ID: workID, Name: "Blob Work",
			State: WorkDraft, ArchiveState: ArchiveActive,
			BlueprintRef: BlueprintRef{ID: "blueprint:test", SchemaVersion: 1, Version: 1},
			CreatedAt:    time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		},
		Blobs: map[string][]byte{digest: data},
	}

	if err := store.CreateWorkDir(input); err != nil {
		t.Fatalf("CreateWorkDir with blobs: %v", err)
	}

	read, err := store.ReadBlob(workID, digest)
	if err != nil {
		t.Fatalf("ReadBlob after atomic create: %v", err)
	}
	if string(read) != string(data) {
		t.Fatalf("blob = %q, want %q", string(read), string(data))
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func ptrState(s WorkState) *WorkState { return &s }
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
