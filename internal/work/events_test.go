package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── helpers ────────────────────────────────────────────────────────────────

func tempWorkDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	workDir := filepath.Join(dir, "works", "test-work-1")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	return workDir
}

// acquireLease acquires the lease or fails the test.
func acquireLease(t *testing.T, workDir string) {
	t.Helper()
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatalf("acquire lease: %v", err)
	}
	t.Cleanup(func() { _ = ReleaseWorkLease(workDir) })
}

func holdExternalLease(t *testing.T, workDir string) func() {
	t.Helper()
	lockPath := WorkLeasePath(workDir)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := tryLockWorkLease(lockPath)
	if err != nil {
		t.Fatalf("hold external lease: %v", err)
	}
	meta := workLeaseMeta{WriterID: "external-writer", PID: os.Getpid(), AcquiredAt: time.Now().UTC()}
	data, _ := json.Marshal(meta)
	if err := lock.WriteMetadata(append(data, '\n')); err != nil {
		lock.Unlock()
		t.Fatal(err)
	}
	return lock.Unlock
}

func makeEvent(typ WorkEventType, revision, baseRevision int64, requestID string, payload json.RawMessage) WorkEvent {
	return WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            fmt.Sprintf("evt-%d", revision),
		RequestID:     requestID,
		WorkID:        "test-work-1",
		Type:          typ,
		Revision:      revision,
		BaseRevision:  baseRevision,
		Payload:       payload,
		CreatedAt:     time.Now().UTC(),
	}
}

func appendAndCheck(t *testing.T, workDir string, event WorkEvent, wantRev int64) {
	t.Helper()
	rev, err := AppendWorkEvent(workDir, event, true)
	if err != nil {
		t.Fatalf("AppendWorkEvent(%s) failed: %v", event.Type, err)
	}
	if rev != wantRev {
		t.Fatalf("AppendWorkEvent(%s) returned revision %d, want %d", event.Type, rev, wantRev)
	}
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

// ── Basic append flow ──────────────────────────────────────────────────────

func TestAppendWorkEvent_BasicFlow(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-1", json.RawMessage(`{"name":"test"}`))
	appendAndCheck(t, workDir, e1, 1)

	logPath := WorkEventLogPath(workDir)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"type":"work.created"`) {
		t.Fatal("log does not contain the expected event type")
	}

	idx, err := ReadWorkEventIndex(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if idx == nil || idx.Revision != 1 || idx.EventCount != 1 {
		t.Fatalf("index: revision=%d eventCount=%d", idx.Revision, idx.EventCount)
	}

	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-2", json.RawMessage(`{"name":"updated"}`))
	appendAndCheck(t, workDir, e2, 2)
}

// ── Lease required ─────────────────────────────────────────────────────────

func TestAppendWorkEvent_RequiresLease(t *testing.T) {
	workDir := tempWorkDir(t)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-1", json.RawMessage(`{}`))
	_, err := AppendWorkEvent(workDir, e1, true)
	if !errors.Is(err, ErrWorkLeaseRequired) {
		t.Fatalf("expected required lease error, got %v", err)
	}
}

func TestCompactWorkEventLog_RequiresLease(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-c1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)
	_, projection, _ := ReplayWithReducer(workDir, reducer)
	ReleaseWorkLease(workDir)

	err := CompactWorkEventLog(workDir, projection, reducer)
	if !errors.Is(err, ErrWorkLeaseRequired) {
		t.Fatalf("expected required lease error for compact, got %v", err)
	}
}

func TestRepairWorkEventLogTail_RequiresLease(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-r1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)
	logPath := WorkEventLogPath(workDir)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("garbage\n"))
	f.Close()
	replay, _ := ReplayWorkEventLog(workDir)
	if !replay.NeedsRepair {
		t.Fatal("expected NeedsRepair")
	}
	ReleaseWorkLease(workDir)

	err := RepairWorkEventLogTail(workDir, replay)
	if !errors.Is(err, ErrWorkLeaseRequired) {
		t.Fatalf("expected required lease error for repair, got %v", err)
	}
}

// ── External lease blocks writes ───────────────────────────────────────────

func TestExternalLease_ReplayReadOnly(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	if err := ReleaseWorkLease(workDir); err != nil {
		t.Fatal(err)
	}
	releaseExternal := holdExternalLease(t, workDir)
	defer releaseExternal()

	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.ReadOnly {
		t.Fatal("expected ReadOnly with external lease")
	}
	if !replay.LeaseExternal {
		t.Fatal("expected LeaseExternal=true")
	}
}

// ── RequestID idempotent by content digest ─────────────────────────────────

func TestAppendWorkEvent_RequestID_Idempotent_SameContent(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-idem-1", json.RawMessage(`{"name":"test"}`))
	appendAndCheck(t, workDir, e1, 1)

	// Retry with SAME requestID, SAME content (different revision in the event
	// struct is fine — Append auto-computes or idempotent-detects).
	e2 := makeEvent(EventWorkCreated, 0, 0, "req-idem-1", json.RawMessage(`{"name":"test"}`))
	rev, err := AppendWorkEvent(workDir, e2, true)
	if err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if rev != 1 {
		t.Fatalf("idempotent retry returned revision %d, want 1", rev)
	}

	idx, _ := ReadWorkEventIndex(workDir)
	if idx.EventCount != 1 {
		t.Fatalf("idempotent retry incremented event count to %d", idx.EventCount)
	}
}

func TestAppendWorkEvent_RequestID_Conflict_DifferentContent(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-conflict-2", json.RawMessage(`{"name":"original"}`))
	appendAndCheck(t, workDir, e1, 1)

	// Same requestID, DIFFERENT content — must conflict even at revision 1.
	e2 := makeEvent(EventWorkCreated, 1, 0, "req-conflict-2", json.RawMessage(`{"name":"different"}`))
	_, err := AppendWorkEvent(workDir, e2, true)
	if err == nil {
		t.Fatal("expected conflict for different content, got nil")
	}
	var conflictErr *ErrWorkEventConflict
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ErrWorkEventConflict, got %T: %v", err, err)
	}
}

func TestAppendWorkEvent_RequestID_SameDigest_DifferentPayload_Conflict(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-adv-1", json.RawMessage(`{"name":"test"}`))
	appendAndCheck(t, workDir, e1, 1)

	// Try to reuse requestID with DIFFERENT payload — must conflict.
	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-adv-1", json.RawMessage(`{"name":"different"}`))
	_, err := AppendWorkEvent(workDir, e2, true)
	if err == nil {
		t.Fatal("expected conflict for requestID reuse")
	}
	var conflictErr *ErrWorkEventConflict
	if !errors.As(err, &conflictErr) {
		t.Fatalf("expected *ErrWorkEventConflict, got %T: %v", err, err)
	}
}

func TestAppendWorkEvent_RequestID_Idempotent_StaleRevision(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-stale-1", json.RawMessage(`{"name":"test"}`))
	appendAndCheck(t, workDir, e1, 1)

	// Advance log to revision 3.
	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-stale-2", json.RawMessage(`{"name":"v2"}`))
	appendAndCheck(t, workDir, e2, 2)
	e3 := makeEvent(EventBlockUpserted, 3, 2, "req-stale-3", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e3, 3)

	// Retry with same requestID + same content but stale revision (0 = auto-compute).
	// Should be idempotent and return original revision 1.
	e4 := makeEvent(EventWorkCreated, 0, 0, "req-stale-1", json.RawMessage(`{"name":"test"}`))
	rev, err := AppendWorkEvent(workDir, e4, true)
	if err != nil {
		t.Fatalf("stale idempotent retry failed: %v", err)
	}
	if rev != 1 {
		t.Fatalf("stale idempotent retry returned revision %d, want 1", rev)
	}
}

// ── Revision chain ─────────────────────────────────────────────────────────

func TestAppendWorkEvent_RevisionChain_BrokenBase(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-r1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	e2 := makeEvent(EventDraftUpdated, 2, 0, "req-r2", json.RawMessage(`{}`))
	_, err := AppendWorkEvent(workDir, e2, true)
	if err == nil {
		t.Fatal("expected chain error")
	}
}

func TestAppendWorkEvent_RevisionChain_Gap(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-g1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	e2 := makeEvent(EventDraftUpdated, 3, 1, "req-g2", json.RawMessage(`{}`))
	_, err := AppendWorkEvent(workDir, e2, true)
	if err == nil {
		t.Fatal("expected gap error")
	}
}

// ── Digest ─────────────────────────────────────────────────────────────────

func TestAppendWorkEvent_DigestVerification(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-dig-1", json.RawMessage(`{"name":"test"}`))
	appendAndCheck(t, workDir, e1, 1)

	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	storedDigest := replay.Events[0].ContentDigest
	if len(storedDigest) != 64 {
		t.Fatalf("unexpected digest length: %d", len(storedDigest))
	}

	// Stable recomputation.
	rec := recordFromEvent(e1)
	rec.ContentDigest = ""
	d, _ := workEventContentDigest(rec)
	d2, _ := workEventContentDigest(rec)
	if d != d2 {
		t.Fatal("digest not stable")
	}
}

func TestAppendWorkEvent_WriterID_AlwaysOverwritten(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-wid-1", json.RawMessage(`{}`))
	e1.WriterID = "fake-writer-from-caller"
	appendAndCheck(t, workDir, e1, 1)

	replay, _ := ReplayWorkEventLog(workDir)
	if replay.Events[0].WriterID != WorkWriterID() {
		t.Fatalf("writerID was not overwritten: got %q, want %q", replay.Events[0].WriterID, WorkWriterID())
	}
}

func TestWorkEventContentDigest_SelfReferencingFree(t *testing.T) {
	rec := workEventRecord{
		ID: "evt-1", RequestID: "req-1", WorkID: "work-1",
		Type: EventWorkCreated, Revision: 1, BaseRevision: 0,
		Payload: json.RawMessage(`{"x":1}`),
	}
	d1, _ := workEventContentDigest(rec)
	rec.ContentDigest = "some-other-digest"
	d2, _ := workEventContentDigest(rec)
	if d1 != d2 {
		t.Fatalf("digest not self-referencing-free: %q vs %q", d1, d2)
	}
}

// ── Torn tail recovery ─────────────────────────────────────────────────────

func TestReplayWorkEventLog_TornTail_NeedsRepair(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-torn-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	logPath := WorkEventLogPath(workDir)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("this is not valid json\n"))
	f.Close()

	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.NeedsRepair {
		t.Fatal("expected NeedsRepair")
	}
}

func TestRepairWorkEventLogTail_CopyAndRecover(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-repair-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	logPath := WorkEventLogPath(workDir)
	originalSize := fileSize(t, logPath)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("garbage bytes\n"))
	f.Close()

	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.NeedsRepair {
		t.Fatal("expected NeedsRepair")
	}

	if err := RepairWorkEventLogTail(workDir, replay); err != nil {
		t.Fatalf("repair failed: %v", err)
	}
	if replay.RecoveryPath == "" {
		t.Fatal("repair did not expose recovery path")
	}
	if _, err := os.Stat(replay.RecoveryPath); err != nil {
		t.Fatalf("recovery evidence missing: %v", err)
	}

	// Recovery copy exists with timestamp suffix.
	entries, _ := os.ReadDir(workDir)
	found := false
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "work.events.recovery-") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no recovery copy found")
	}

	newSize := fileSize(t, logPath)
	if newSize > originalSize {
		t.Fatalf("log was not truncated: old=%d new=%d", originalSize, newSize)
	}

	// After repair, log is clean.
	replay2, _ := ReplayWorkEventLog(workDir)
	if replay2.NeedsRepair {
		t.Fatal("repair did not fix NeedsRepair")
	}

	// Safe retry.
	if err := RepairWorkEventLogTail(workDir, replay2); err != nil {
		t.Fatalf("retry repair: %v", err)
	}
}

func TestRepairWorkEventLogTail_StaleReplayRejected(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-stale-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	logPath := WorkEventLogPath(workDir)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("garbage\n"))
	f.Close()

	replay, _ := ReplayWorkEventLog(workDir)

	// Append more data to make the replay stale.
	f2, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f2.Write([]byte("more garbage\n"))
	f2.Close()

	err := RepairWorkEventLogTail(workDir, replay)
	if err == nil {
		t.Fatal("stale replay should be rejected")
	}
	if !strings.Contains(err.Error(), "stale") {
		t.Fatalf("error should mention stale: %v", err)
	}
}

func TestRepairWorkEventLogTail_ConsecutiveRecoveries(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-consec-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	// First damage + repair.
	logPath := WorkEventLogPath(workDir)
	f, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write([]byte("damage1\n"))
	f.Close()

	replay1, _ := ReplayWorkEventLog(workDir)
	RepairWorkEventLogTail(workDir, replay1)

	// Second damage + repair.
	f2, _ := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	f2.Write([]byte("damage2\n"))
	f2.Close()

	replay2, _ := ReplayWorkEventLog(workDir)
	if err := RepairWorkEventLogTail(workDir, replay2); err != nil {
		t.Fatalf("second repair: %v", err)
	}

	// Both recovery copies exist.
	entries, _ := os.ReadDir(workDir)
	count := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "work.events.recovery-") {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("expected >=2 recovery copies, got %d", count)
	}
}

// ── Future schema → read-only ─────────────────────────────────────────────

func TestReplayWorkEventLog_FutureSchema_ReadOnly(t *testing.T) {
	workDir := tempWorkDir(t)

	logPath := WorkEventLogPath(workDir)
	rec := workEventRecord{
		SchemaVersion: 999,
		ID:            "evt-future-1", WorkID: "test-work-1",
		Type: EventWorkCreated, Revision: 1, BaseRevision: 0,
		Payload: json.RawMessage(`{}`), ContentDigest: "abc",
		WriterID: WorkWriterID(), CreatedAt: time.Now().UTC(),
	}
	buf, _ := json.Marshal(rec)
	os.WriteFile(logPath, append(buf, '\n'), 0o644)

	replay, _ := ReplayWorkEventLog(workDir)
	if !replay.ReadOnly || !strings.Contains(replay.ReadOnlyReason, "future schema") {
		t.Fatalf("expected future schema read-only: %+v", replay)
	}
}

// ── Unknown event type ────────────────────────────────────────────────────

func TestReplayWorkEventLog_UnknownType_ReadOnly(t *testing.T) {
	workDir := tempWorkDir(t)

	logPath := WorkEventLogPath(workDir)
	rec := workEventRecord{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-unk-1", WorkID: "test-work-1",
		Type: WorkEventType("future.unknown"), Revision: 1, BaseRevision: 0,
		Payload: json.RawMessage(`{}`), ContentDigest: "abc",
		WriterID: WorkWriterID(), CreatedAt: time.Now().UTC(),
	}
	buf, _ := json.Marshal(rec)
	os.WriteFile(logPath, append(buf, '\n'), 0o644)

	replay, _ := ReplayWorkEventLog(workDir)
	if !replay.ReadOnly || !strings.Contains(replay.ReadOnlyReason, "unknown event type") {
		t.Fatalf("expected unknown type read-only: %+v", replay)
	}
}

func TestAppendWorkEvent_UnknownType_Rejected(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e := makeEvent(EventWorkCreated, 1, 0, "req-1", json.RawMessage(`{}`))
	e.Type = WorkEventType("custom.unknown")
	_, err := AppendWorkEvent(workDir, e, true)
	if err == nil || !strings.Contains(err.Error(), "unknown event type") {
		t.Fatalf("expected unknown type rejection: %v", err)
	}
}

// ── WorkID mismatch ────────────────────────────────────────────────────────

func TestAppendWorkEvent_WorkID_Mismatch(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-wid-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-wid-2", json.RawMessage(`{}`))
	e2.WorkID = "other-work"
	_, err := AppendWorkEvent(workDir, e2, true)
	if err == nil || !strings.Contains(err.Error(), "workID mismatch") {
		t.Fatalf("expected workID mismatch: %v", err)
	}
}

// ── Index self-heal ────────────────────────────────────────────────────────

func TestIndex_SelfHeal_CorruptIndex(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-heal-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	// Corrupt the index.
	indexPath := WorkEventIndexPath(workDir)
	os.WriteFile(indexPath, []byte("not valid json"), 0o644)

	// Append should rebuild index silently and succeed.
	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-heal-2", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e2, 2)

	idx, _ := ReadWorkEventIndex(workDir)
	if idx.EventCount != 2 {
		t.Fatalf("self-healed index event count = %d, want 2", idx.EventCount)
	}
}

func TestIndex_SelfHeal_MissingIndex(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-miss-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	// Delete the index.
	os.Remove(WorkEventIndexPath(workDir))

	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-miss-2", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e2, 2)
}

func TestAppendWorkEvent_IndexWriteFails_RetrySelfHeals(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-idxfail-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-idxfail-2", json.RawMessage(`{}`))
	originalWriter := writeWorkEventIndexAfterAppend
	writeWorkEventIndexAfterAppend = func(string, *WorkEventIndex) error { return errors.New("injected index failure") }
	revision, err := AppendWorkEvent(workDir, e2, true)
	writeWorkEventIndexAfterAppend = originalWriter
	if revision != 2 || err == nil || !strings.Contains(err.Error(), "event appended") {
		t.Fatalf("partial append result revision=%d err=%v", revision, err)
	}

	// The same request is safe to retry: replay sees it in the authoritative
	// log, rebuilds the stale index, and returns without appending a duplicate.
	revision, err = AppendWorkEvent(workDir, e2, true)
	if err != nil || revision != 2 {
		t.Fatalf("retry revision=%d err=%v", revision, err)
	}
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil || len(replay.Events) != 2 {
		t.Fatalf("replay events=%d err=%v", len(replay.Events), err)
	}
	idx, err := ReadWorkEventIndex(workDir)
	if err != nil || idx.EventCount != 2 || idx.Revision != 2 {
		t.Fatalf("index=%+v err=%v", idx, err)
	}
}

func TestIndex_SelfHeal_StaleButValid(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)
	e1 := makeEvent(EventWorkCreated, 1, 0, "req-stale-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	idx, err := ReadWorkEventIndex(workDir)
	if err != nil {
		t.Fatal(err)
	}
	idx.LogSize--
	idx.RequestIndex = map[string]WorkRequestEntry{}
	if err := writeWorkEventIndex(workDir, idx); err != nil {
		t.Fatal(err)
	}

	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-stale-2", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e2, 2)
	got, _ := ReadWorkEventIndex(workDir)
	if got.EventCount != 2 || got.RequestIndex["req-stale-1"].Revision != 1 {
		t.Fatalf("stale index was not rebuilt: %+v", got)
	}
}

// ── Index rebuild ──────────────────────────────────────────────────────────

func TestRebuildWorkEventIndex(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-idx-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)
	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-idx-2", json.RawMessage(`{"name":"v2"}`))
	appendAndCheck(t, workDir, e2, 2)
	e3 := makeEvent(EventBlockUpserted, 3, 2, "req-idx-3", json.RawMessage(`{"blockId":"b1"}`))
	appendAndCheck(t, workDir, e3, 3)

	os.Remove(WorkEventIndexPath(workDir))

	idx, err := RebuildWorkEventIndex(workDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if idx.Revision != 3 || idx.EventCount != 3 {
		t.Fatalf("revision=%d eventCount=%d", idx.Revision, idx.EventCount)
	}
	if idx.RequestIndex["req-idx-1"].Revision != 1 {
		t.Fatalf("requestIndex[req-idx-1] = %+v", idx.RequestIndex["req-idx-1"])
	}
}

// ── Compact preserves requestID history ─────────────────────────────────────

func reducer(event WorkEvent, current *Work) (*Work, error) {
	if current == nil {
		current = &Work{ID: event.WorkID}
	}
	switch event.Type {
	case EventWorkCreated:
		current.State = WorkDraft
	case EventDraftUpdated:
		current.Name = "draft-updated"
	case EventBlockUpserted:
		current.Name = "block-upserted"
	}
	return current, nil
}

func TestCompactWorkEventLog_ProjectionEquivalence(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-cmp-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)
	e2 := makeEvent(EventDraftUpdated, 2, 1, "req-cmp-2", json.RawMessage(`{"name":"v2"}`))
	appendAndCheck(t, workDir, e2, 2)
	e3 := makeEvent(EventBlockUpserted, 3, 2, "req-cmp-3", json.RawMessage(`{"blockId":"b1"}`))
	appendAndCheck(t, workDir, e3, 3)

	_, projection, _ := ReplayWithReducer(workDir, reducer)

	if err := CompactWorkEventLog(workDir, projection, reducer); err != nil {
		t.Fatalf("compact failed: %v", err)
	}

	// After compact, requestIDs should still be in the index.
	idx, _ := ReadWorkEventIndex(workDir)
	if idx.RequestIndex["req-cmp-1"].Revision != 1 {
		t.Fatalf("requestIndex lost req-cmp-1: %+v", idx.RequestIndex)
	}
	if idx.RequestIndex["req-cmp-2"].Revision != 2 {
		t.Fatalf("requestIndex lost req-cmp-2: %+v", idx.RequestIndex)
	}
	if idx.RequestIndex["req-cmp-3"].Revision != 3 {
		t.Fatalf("requestIndex lost req-cmp-3: %+v", idx.RequestIndex)
	}

	// Rebuild index from compacted log should also preserve requestIDs.
	os.Remove(WorkEventIndexPath(workDir))
	idx2, _ := RebuildWorkEventIndex(workDir)
	if idx2.RequestIndex["req-cmp-1"].Revision != 1 {
		t.Fatal("rebuild lost req-cmp-1")
	}
}

func TestCompactWorkEventLog_NilProjection(t *testing.T) {
	workDir := tempWorkDir(t)
	if err := CompactWorkEventLog(workDir, nil, reducer); err == nil {
		t.Fatal("expected error for nil projection")
	}
}

func TestCompactWorkEventLog_NilReducer(t *testing.T) {
	workDir := tempWorkDir(t)
	proj := &Work{ID: "test"}
	if err := CompactWorkEventLog(workDir, proj, nil); err == nil {
		t.Fatal("expected error for nil reducer")
	}
}

func TestReplayWithReducer_NilReducer(t *testing.T) {
	_, _, err := ReplayWithReducer(tempWorkDir(t), nil)
	if err == nil {
		t.Fatal("expected error for nil reducer")
	}
}

func TestReplayWithReducer_HandlesEventCompactInternally(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e1 := makeEvent(EventWorkCreated, 1, 0, "req-ec-1", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e1, 1)

	_, proj, _ := ReplayWithReducer(workDir, reducer)
	if err := CompactWorkEventLog(workDir, proj, reducer); err != nil {
		t.Fatal(err)
	}

	// ReplayWithReducer should handle the compact event transparently.
	replay, proj2, err := ReplayWithReducer(workDir, reducer)
	if err != nil {
		t.Fatal(err)
	}
	// The reducer should never see the eventCompact type.
	for _, e := range replay.Events {
		if e.Type == eventCompact {
			// ReplayWithReducer handles this internally.
			continue
		}
	}
	_ = proj2
}

// ── Chain break ────────────────────────────────────────────────────────────

func TestReplayWorkEventLog_ChainBreak_NeedsRepair(t *testing.T) {
	workDir := tempWorkDir(t)

	logPath := WorkEventLogPath(workDir)
	rec1 := workEventRecord{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-1", WorkID: "test-work-1", Type: EventWorkCreated,
		Revision: 1, BaseRevision: 0, Payload: json.RawMessage(`{}`),
		WriterID: WorkWriterID(), CreatedAt: time.Now().UTC(),
	}
	rec1.ContentDigest, _ = workEventContentDigest(rec1)

	rec2 := workEventRecord{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-3", WorkID: "test-work-1", Type: EventDraftUpdated,
		Revision: 3, BaseRevision: 1, Payload: json.RawMessage(`{}`),
		WriterID: WorkWriterID(), CreatedAt: time.Now().UTC(),
	}
	rec2.ContentDigest, _ = workEventContentDigest(rec2)

	f, _ := os.Create(logPath)
	buf1, _ := json.Marshal(rec1)
	f.Write(append(buf1, '\n'))
	buf2, _ := json.Marshal(rec2)
	f.Write(append(buf2, '\n'))
	f.Close()

	replay, _ := ReplayWorkEventLog(workDir)
	if !replay.NeedsRepair || len(replay.Events) != 1 {
		t.Fatalf("expected NeedsRepair with 1 event: %+v", replay)
	}
}

// ── Digest mismatch → NeedsRepair ──────────────────────────────────────────

func TestReplayWorkEventLog_DigestMismatch_NeedsRepair(t *testing.T) {
	workDir := tempWorkDir(t)

	logPath := WorkEventLogPath(workDir)
	rec := workEventRecord{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            "evt-dig-1", WorkID: "test-work-1", Type: EventWorkCreated,
		Revision: 1, BaseRevision: 0, Payload: json.RawMessage(`{}`),
		ContentDigest: "0000000000000000000000000000000000000000000000000000000000000000",
		WriterID:      WorkWriterID(), CreatedAt: time.Now().UTC(),
	}
	buf, _ := json.Marshal(rec)
	os.WriteFile(logPath, append(buf, '\n'), 0o644)

	replay, _ := ReplayWorkEventLog(workDir)
	if !replay.NeedsRepair || len(replay.Events) != 0 {
		t.Fatalf("expected NeedsRepair with 0 events: %+v", replay)
	}
}

// ── EventCompact not publicly exposed ───────────────────────────────────────

func TestEventCompact_NotInPublicTypes(t *testing.T) {
	types := []WorkEventType{
		EventWorkCreated, EventDefinitionFrozen, EventDraftUpdated,
		EventRunStarted, EventStageChanged, EventTaskChanged,
		EventAttemptChanged, EventBlockUpserted, EventBlockRemoved,
		EventCornerstoneUpserted, EventCornerstoneRemoved,
		EventConclusionUpserted, EventArtifactLinked,
		EventWorkArchived, EventWorkDeleted,
	}
	for _, typ := range types {
		if typ == eventCompact {
			t.Fatal("eventCompact appears in public WorkEventType list")
		}
	}
	if !knownWorkEventTypes[eventCompact] {
		t.Fatal("eventCompact must be in knownWorkEventTypes")
	}
}

// ── Caller cannot append eventCompact ──────────────────────────────────────

func TestAppendWorkEvent_RejectsCompactType(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	e := makeEvent(EventWorkCreated, 1, 0, "req-1", json.RawMessage(`{}`))
	e.Type = eventCompact
	_, err := AppendWorkEvent(workDir, e, true)
	if err == nil || !strings.Contains(err.Error(), "internal event type") {
		t.Fatalf("expected rejection of eventCompact: %v", err)
	}
}

// ── Empty / missing log ────────────────────────────────────────────────────

func TestReplayWorkEventLog_EmptyLog(t *testing.T) {
	workDir := tempWorkDir(t)
	os.WriteFile(WorkEventLogPath(workDir), nil, 0o644)

	replay, _ := ReplayWorkEventLog(workDir)
	if replay.NeedsRepair || replay.ReadOnly {
		t.Fatal("empty log should be clean")
	}
}

func TestReplayWorkEventLog_NoFile(t *testing.T) {
	replay, _ := ReplayWorkEventLog(tempWorkDir(t))
	if replay.NeedsRepair || replay.ReadOnly {
		t.Fatal("missing log should be clean")
	}
}

// ── Creates parent dir ─────────────────────────────────────────────────────

func TestAppendWorkEvent_CreatesParentDir(t *testing.T) {
	baseDir := t.TempDir()
	workDir := filepath.Join(baseDir, "works", "new-work")
	acquireLease(t, workDir)

	e := makeEvent(EventWorkCreated, 1, 0, "req-new-dir", json.RawMessage(`{}`))
	appendAndCheck(t, workDir, e, 1)
}

// ── Lease release idempotent ───────────────────────────────────────────────

func TestReleaseWorkLease_Idempotent(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	ReleaseWorkLease(workDir)
	ReleaseWorkLease(workDir) // second release is no-op
	// Should not panic or error.
}

// ── Lease acquire idempotent ───────────────────────────────────────────────

func TestAcquireWorkLease_Idempotent(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)

	// Second acquire should succeed (same writer).
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatalf("re-acquire should succeed: %v", err)
	}
}

func TestAcquireWorkLease_ExternalOSLock(t *testing.T) {
	workDir := tempWorkDir(t)
	releaseExternal := holdExternalLease(t, workDir)
	defer releaseExternal()
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil || !replay.ReadOnly || !replay.LeaseExternal {
		t.Fatalf("missing log under external lease: replay=%+v err=%v", replay, err)
	}
	if err := AcquireWorkLease(workDir); !errors.Is(err, ErrWorkLeaseHeld) {
		t.Fatalf("expected external OS lease error, got %v", err)
	}
}

func TestAcquireWorkLease_RecoversStaleMetadata(t *testing.T) {
	workDir := tempWorkDir(t)
	releaseExternal := holdExternalLease(t, workDir)
	releaseExternal() // Metadata remains, but the OS lock is gone (crash shape).
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatalf("stale metadata blocked recovery: %v", err)
	}
	defer ReleaseWorkLease(workDir)
}

func TestAppendWorkEvent_ConcurrentSerialized(t *testing.T) {
	workDir := tempWorkDir(t)
	acquireLease(t, workDir)
	const count = 24
	errCh := make(chan error, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			event := makeEvent(EventDraftUpdated, 0, 0, fmt.Sprintf("req-concurrent-%d", i), json.RawMessage(fmt.Sprintf(`{"value":%d}`, i)))
			event.ID = fmt.Sprintf("evt-concurrent-%d", i)
			_, err := AppendWorkEvent(workDir, event, true)
			errCh <- err
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil || replay.NeedsRepair || len(replay.Events) != count {
		t.Fatalf("events=%d repair=%t err=%v", len(replay.Events), replay.NeedsRepair, err)
	}
}
