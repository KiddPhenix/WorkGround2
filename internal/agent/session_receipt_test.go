package agent

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestStableSessionIDDeterministic(t *testing.T) {
	a := StableSessionID("owner-a", "req-1")
	b := StableSessionID("owner-a", "req-1")
	c := StableSessionID("owner-a", "req-2")
	d := StableSessionID("owner-b", "req-1")
	if a == "" || a != b {
		t.Fatalf("same (owner,request) must yield the same id: %q vs %q", a, b)
	}
	if a == c || a == d {
		t.Fatalf("different (owner,request) must yield different ids")
	}
}

func TestCreateStableSessionFileConcurrentAndReplay(t *testing.T) {
	dir := t.TempDir()
	path1, created1, err := CreateStableSessionFile(dir, "owner-a", "req-1")
	if err != nil || !created1 {
		t.Fatalf("first create: path=%q created=%v err=%v", path1, created1, err)
	}
	path2, created2, err := CreateStableSessionFile(dir, "owner-a", "req-1")
	if err != nil || created2 || path2 != path1 {
		t.Fatalf("replay: path=%q created=%v err=%v (want same path, created=false)", path2, created2, err)
	}
	path3, created3, err := CreateStableSessionFile(dir, "owner-a", "req-2")
	if err != nil || !created3 || path3 == path1 {
		t.Fatalf("different request: path=%q created=%v err=%v", path3, created3, err)
	}
}

func TestSessionReceiptFirstWriteWinsAndFingerprint(t *testing.T) {
	dir := t.TempDir()
	rec := SessionReceipt{SessionID: "session-a", Fingerprint: "fp-1"}
	if got, err := WriteSessionReceipt(dir, "req-1", rec); err != nil || got.SessionID != "session-a" {
		t.Fatalf("write = %+v err=%v", got, err)
	}
	// First-write-wins with the same fingerprint: returns the original, no error.
	got, err := WriteSessionReceipt(dir, "req-1", SessionReceipt{SessionID: "session-a", Fingerprint: "fp-1"})
	if err != nil || got.SessionID != "session-a" {
		t.Fatalf("replay = %+v err=%v, want session-a", got, err)
	}
	// A different fingerprint is an explicit conflict, not a silent overwrite.
	if _, err := WriteSessionReceipt(dir, "req-1", SessionReceipt{SessionID: "session-b", Fingerprint: "fp-2"}); err == nil {
		t.Fatal("different input must conflict")
	} else {
		var conflict *SessionReceiptConflictError
		if !errors.As(err, &conflict) || conflict.SessionID != "session-a" {
			t.Fatalf("conflict error = %v, want SessionReceiptConflictError carrying session-a", err)
		}
	}
	read, ok, err := ReadSessionReceipt(dir, "req-1")
	if err != nil || !ok || read.SessionID != "session-a" {
		t.Fatalf("read = %+v ok=%v err=%v", read, ok, err)
	}
	if read.Fingerprint != "fp-1" {
		t.Fatalf("fingerprint = %q, want fp-1", read.Fingerprint)
	}
	if read.RequestID != "req-1" {
		t.Fatalf("request id = %q, want req-1", read.RequestID)
	}
}

func TestStableSessionPathUsesStableID(t *testing.T) {
	dir := t.TempDir()
	path, exists := ResolveStableSessionPath(dir, "owner-a", "req-1")
	if exists {
		t.Fatal("path should not exist before create")
	}
	if filepath.Base(path) != StableSessionID("owner-a", "req-1")+".jsonl" {
		t.Fatalf("path base = %q", filepath.Base(path))
	}
}

// TestSessionReceiptFilenameHashesRequestID guards the path-traversal / illegal
// name fix: the raw request ID must never appear as a literal file name, even
// when it contains separators, drive letters or Windows-reserved names.
func TestSessionReceiptFilenameHashesRequestID(t *testing.T) {
	dir := t.TempDir()
	for _, rid := range []string{
		"../../evil", "C:\\Windows\\evil", "CON", "req:with*illegal?chars", "a/very/../../long/request/id",
	} {
		path := SessionReceiptPath(dir, rid)
		if strings.Contains(filepath.Base(path), rid) {
			t.Fatalf("request id %q leaked into file name %q", rid, filepath.Base(path))
		}
		// The hashed file name is confined to the receipt directory.
		if filepath.Dir(path) != receiptDir(dir) {
			t.Fatalf("path %q escaped receipt dir", path)
		}
		if _, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "s", Fingerprint: "fp"}); err != nil {
			t.Fatalf("write %q: %v", rid, err)
		}
		if _, ok, err := ReadSessionReceipt(dir, rid); err != nil || !ok {
			t.Fatalf("read %q: ok=%v err=%v", rid, ok, err)
		}
	}
}

// TestSessionReceiptConcurrentReserve exercises goroutine-level concurrency: many
// writers race to reserve distinct request IDs and to replay the same request ID.
func TestSessionReceiptConcurrentReserve(t *testing.T) {
	dir := t.TempDir()
	const n = 64
	var wg sync.WaitGroup
	results := make([]SessionReceipt, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rid := "req-" + string(rune('a'+i%26)) + "-" + strings.Repeat("x", i)
			rec, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "session-" + strings.Repeat("x", i), Fingerprint: "fp-" + strings.Repeat("x", i)})
			if err == nil {
				results[i] = rec
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < n; i++ {
		if results[i].SessionID == "" {
			t.Fatalf("reserve %d failed", i)
		}
	}

	// Many goroutines replay the SAME request ID; all must observe the same
	// SessionID and no conflict.
	const replays = 64
	rid := "shared-req"
	want, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "shared-session", Fingerprint: "fp-shared"})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	var replayWg sync.WaitGroup
	conflicts := make(chan error, replays)
	for i := 0; i < replays; i++ {
		replayWg.Add(1)
		go func() {
			defer replayWg.Done()
			got, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "shared-session", Fingerprint: "fp-shared"})
			if err != nil {
				conflicts <- err
				return
			}
			if got.SessionID != want.SessionID {
				conflicts <- &SessionReceiptConflictError{RequestID: rid, SessionID: got.SessionID}
			}
		}()
	}
	replayWg.Wait()
	close(conflicts)
	for err := range conflicts {
		t.Fatalf("concurrent replay diverged: %v", err)
	}
}

// TestSessionReceiptConcurrentDifferentInputConflict guards the "same request,
// different input" case under concurrency: exactly one binding wins and every
// other input surfaces a conflict.
func TestSessionReceiptConcurrentDifferentInputConflict(t *testing.T) {
	dir := t.TempDir()
	rid := "conflict-req"
	const n = 32
	var wg sync.WaitGroup
	seens := make([]SessionReceipt, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "session-" + strings.Repeat("x", i), Fingerprint: "fp-" + strings.Repeat("x", i)})
			seens[i] = rec
			errs[i] = err
		}(i)
	}
	wg.Wait()

	var winners SessionReceipt
	winnerCount := 0
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			winnerCount++
			winners = seens[i]
		} else if _, ok := errs[i].(*SessionReceiptConflictError); !ok {
			t.Fatalf("unexpected error %d: %v", i, errs[i])
		}
	}
	if winnerCount != 1 {
		t.Fatalf("want exactly one winner, got %d", winnerCount)
	}
	// The single winner is authoritative on disk.
	read, ok, err := ReadSessionReceipt(dir, rid)
	if err != nil || !ok || read.SessionID != winners.SessionID || read.Fingerprint != winners.Fingerprint {
		t.Fatalf("on-disk winner = %+v ok=%v err=%v, want %+v", read, ok, err, winners)
	}
}

// TestSessionReceiptStateMachineMonotonic exercises reserved -> ... -> committed
// and the idempotent / backward-rejection rules.
func TestSessionReceiptStateMachineMonotonic(t *testing.T) {
	dir := t.TempDir()
	rid := "state-req"
	if _, err := ReserveSessionReceipt(dir, rid, SessionReceipt{SessionID: "s", Fingerprint: "fp"}); err != nil {
		t.Fatalf("reserve: %v", err)
	}

	states := []ReceiptState{ReceiptMetaReady, ReceiptPromptRecorded, ReceiptStarted, ReceiptCommitted}
	for i, st := range states {
		got, err := AdvanceSessionReceipt(dir, rid, st)
		if err != nil {
			t.Fatalf("advance to %s: %v", st, err)
		}
		if got.State != st {
			t.Fatalf("advance %d: state = %s, want %s", i, got.State, st)
		}
	}
	// Re-advancing to the current or a later state is an idempotent no-op.
	got, err := AdvanceSessionReceipt(dir, rid, ReceiptCommitted)
	if err != nil || got.State != ReceiptCommitted {
		t.Fatalf("idempotent re-advance: %+v err=%v", got, err)
	}
	// Advancing backward is rejected (never moves the checkpoint back).
	if _, err := AdvanceSessionReceipt(dir, rid, ReceiptReserved); err == nil {
		t.Fatal("backward advance must fail")
	}
	// The committed state survives a re-read.
	read, ok, err := ReadSessionReceipt(dir, rid)
	if err != nil || !ok || read.State != ReceiptCommitted {
		t.Fatalf("read after commit = %+v ok=%v err=%v", read, ok, err)
	}
}

// TestSessionReceiptCorruptRecovery verifies that a torn / truncated receipt is
// surfaced as ErrSessionReceiptCorrupt (never as "no binding"), and that after
// the corrupt file is removed a deterministic re-reserve recovers the binding.
func TestSessionReceiptCorruptRecovery(t *testing.T) {
	dir := t.TempDir()
	rid := "corrupt-req"
	if _, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "s", Fingerprint: "fp"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	path := SessionReceiptPath(dir, rid)

	// Truncate to a half JSON — the classic crash mid-write artifact.
	if err := os.WriteFile(path, []byte(`{"session_id":"s","finger`), 0o600); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	_, ok, err := ReadSessionReceipt(dir, rid)
	if err == nil || ok {
		t.Fatalf("corrupt read must be an error and not ok: ok=%v err=%v", ok, err)
	}
	if !errors.Is(err, ErrSessionReceiptCorrupt) {
		t.Fatalf("corrupt read error = %v, want ErrSessionReceiptCorrupt", err)
	}
	// A write over a corrupt receipt must not silently succeed.
	if _, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "s2", Fingerprint: "fp2"}); err == nil {
		t.Fatal("write over corrupt receipt must not silently overwrite")
	}

	// Deterministic recovery: remove the corrupt file and re-reserve; the same
	// deterministic SessionID binding is rebuilt and readable.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove corrupt: %v", err)
	}
	got, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "s", Fingerprint: "fp"})
	if err != nil || got.SessionID != "s" {
		t.Fatalf("re-reserve = %+v err=%v", got, err)
	}
	if _, ok, err := ReadSessionReceipt(dir, rid); err != nil || !ok {
		t.Fatalf("re-read after recovery: ok=%v err=%v", ok, err)
	}
}

// TestSessionReceiptEmptyFileRecovery simulates a crash between the O_EXCL claim
// and the first write: a zero-byte file is corrupt and recoverable, not a valid
// empty binding.
func TestSessionReceiptEmptyFileRecovery(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(receiptDir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	rid := "empty-req"
	if err := os.WriteFile(SessionReceiptPath(dir, rid), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := ReadSessionReceipt(dir, rid)
	if err == nil || ok {
		t.Fatalf("empty receipt must be corrupt: ok=%v err=%v", ok, err)
	}
	if !errors.Is(err, ErrSessionReceiptCorrupt) {
		t.Fatalf("empty receipt error = %v, want ErrSessionReceiptCorrupt", err)
	}
}

// TestSessionReceiptRequestIDMismatch verifies a receipt whose embedded request
// ID does not match the file name (hash collision or tampering) is rejected.
func TestSessionReceiptRequestIDMismatch(t *testing.T) {
	dir := t.TempDir()
	rid := "mismatch-req"
	path := SessionReceiptPath(dir, rid)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"session_id":"s","fingerprint":"fp","request_id":"other"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, ok, err := ReadSessionReceipt(dir, rid)
	if err == nil || ok {
		t.Fatalf("mismatched request id must be corrupt: ok=%v err=%v", ok, err)
	}
}

// TestSessionReceiptPermissions guards 0600 file / 0700 dir. Windows only models
// the read-only bit, so the strict mode check is skipped there.
func TestSessionReceiptPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode semantics differ on Windows")
	}
	dir := t.TempDir()
	rid := "perm-req"
	if _, err := WriteSessionReceipt(dir, rid, SessionReceipt{SessionID: "s", Fingerprint: "fp"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(SessionReceiptPath(dir, rid))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("receipt file perm = %o, want 0600", perm)
	}
	dirInfo, err := os.Stat(receiptDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("receipt dir perm = %o, want 0700", perm)
	}
}

// TestSessionReceiptInputFingerprint covers the full input set: changing any of
// owner/request/workspace/purpose/parent/plan item/title/prompt changes the
// fingerprint, so a reused request ID with any differing input is a conflict.
func TestSessionReceiptInputFingerprint(t *testing.T) {
	base := SessionReceiptInput{
		Owner: "o", RequestID: "r", Workspace: "w", Purpose: "p",
		Parent: "parent", PlanItem: "plan", Title: "t", Prompt: "prompt",
	}
	fp := base.Fingerprint()
	if fp == "" {
		t.Fatal("empty fingerprint")
	}
	mutate := []func(*SessionReceiptInput){
		func(in *SessionReceiptInput) { in.Owner += "x" },
		func(in *SessionReceiptInput) { in.RequestID += "x" },
		func(in *SessionReceiptInput) { in.Workspace += "x" },
		func(in *SessionReceiptInput) { in.Purpose += "x" },
		func(in *SessionReceiptInput) { in.Parent += "x" },
		func(in *SessionReceiptInput) { in.PlanItem += "x" },
		func(in *SessionReceiptInput) { in.Title += "x" },
		func(in *SessionReceiptInput) { in.Prompt += "x" },
	}
	for i, m := range mutate {
		v := base
		m(&v)
		if v.Fingerprint() == fp {
			t.Fatalf("mutate[%d] fingerprint unchanged", i)
		}
	}
}
