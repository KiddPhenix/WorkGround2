package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"workground2/internal/fileutil"
)

// SessionFileHasContent reports whether a session transcript file has any
// content, so a caller can make prompt submission idempotent across
// create/submit/replay. It is a fast presence check, not an idempotency
// guarantee: callers that must not submit a prompt twice under concurrency or a
// crash use the Session receipt state machine (ReserveSessionReceipt +
// AdvanceSessionReceipt) instead of relying on this probe alone.
func SessionFileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// StableSessionID derives a deterministic Session identity from the owner
// assistant and the caller's request ID. It is the idempotency key of the
// Session subsystem: the same (owner, requestID) always resolves to the same
// Session ID before any Session is created, so concurrent creates, a crash
// before commit, or a replay can never produce a second Session.
func StableSessionID(ownerAssistantID, requestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(ownerAssistantID) + "/" + strings.TrimSpace(requestID)))
	return "session-" + hex.EncodeToString(sum[:12])
}

// ForkStableID derives the deterministic identity of a fork branch created for
// (parentSessionID, requestID). It is the fork's idempotency key: replaying
// the same fork request (same parent + requestID) resolves to the same branch
// ID before any fork executes, so a crash between receipt, fork and metadata
// inheritance never produces a second branch.
func ForkStableID(parentSessionID, requestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(parentSessionID) + "/fork/" + strings.TrimSpace(requestID)))
	return "fork-" + hex.EncodeToString(sum[:12])
}

// ReceiptState is the durable, monotonically advancing lifecycle of a Session
// creation. The states exist so a crash or a concurrent host can tell how far a
// create/submit got before it was interrupted, and resume from the checkpoint
// instead of re-running an already-durable step.
//
//	reserved       request ID claimed; SessionID + input fingerprint bound.
//	meta_ready     branch meta (owner/purpose/workspace/parent/plan item) written.
//	prompt_recorded the user turn was durably persisted as a Session checkpoint.
//	started        the model turn was started.
//	committed      the create/submit is fully durable and observable.
//
// The states are advisory recovery hints, not a second execution truth: the
// authoritative idempotency is the (owner, requestID) -> deterministic SessionID
// binding plus the input fingerprint. A transition may skip forward, but is
// never allowed to move backward.
type ReceiptState string

const (
	ReceiptReserved       ReceiptState = "reserved"
	ReceiptMetaReady      ReceiptState = "meta_ready"
	ReceiptPromptRecorded ReceiptState = "prompt_recorded"
	ReceiptStarted        ReceiptState = "started"
	ReceiptCommitted      ReceiptState = "committed"
)

// receiptStateRank gives the monotonic order of receipt states.
var receiptStateRank = map[ReceiptState]int{
	ReceiptReserved:       1,
	ReceiptMetaReady:      2,
	ReceiptPromptRecorded: 3,
	ReceiptStarted:        4,
	ReceiptCommitted:      5,
}

func validReceiptState(s ReceiptState) bool {
	_, ok := receiptStateRank[s]
	return ok
}

// AtLeast reports whether s is at least as far as o in the monotonic order. An
// empty/unknown state is treated as the earliest (reserved), so a host can ask
// "has this create already started submitting?" without a dedicated sentinel.
func (s ReceiptState) AtLeast(o ReceiptState) bool {
	rank := receiptStateRank[s]
	if rank == 0 {
		rank = receiptStateRank[ReceiptReserved]
	}
	return rank >= receiptStateRank[o]
}

// ErrSessionReceiptCorrupt marks a receipt file that exists but cannot be
// parsed or whose embedded request ID does not match its hash-derived file
// name. The binding is unrecoverable from the file's own bytes; the caller
// recovers deterministically from the stable SessionID and re-reserves.
var ErrSessionReceiptCorrupt = errors.New("session receipt is corrupt")

// SessionReceiptConflictError reports that a request ID was reused with
// different input. The existing binding is authoritative and returned so a
// caller can resolve it without guessing.
type SessionReceiptConflictError struct {
	RequestID   string
	SessionID   string
	Fingerprint string
}

func (e *SessionReceiptConflictError) Error() string {
	return fmt.Sprintf("session receipt request %q already bound to session %s with a different input", e.RequestID, e.SessionID)
}

// SessionReceipt is the durable (requestID -> SessionID + input fingerprint)
// binding stored beside the Session files. It lets a replayed request be
// resolved without scanning, and rejects a request ID reused with different
// input (fingerprint mismatch). RequestID is stored so a read can verify the
// file name hash matches its content; State carries the recovery checkpoint.
type SessionReceipt struct {
	SessionID   string       `json:"session_id"`
	Fingerprint string       `json:"fingerprint"`
	RequestID   string       `json:"request_id,omitempty"`
	State       ReceiptState `json:"state,omitempty"`
}

// SessionReceiptInput is the full set of caller inputs that participate in the
// idempotency fingerprint. Reusing a request ID with any of these differing
// (not just owner/request) is a conflict, so a collision on one field can never
// silently reuse the wrong Session.
type SessionReceiptInput struct {
	Owner     string
	RequestID string
	Workspace string
	Purpose   string
	Parent    string
	PlanItem  string
	Title     string
	Prompt    string
}

// Fingerprint returns the content hash of the full input set.
func (in SessionReceiptInput) Fingerprint() string {
	return SessionReceiptFingerprint(
		in.Owner, in.RequestID, in.Workspace, in.Purpose,
		in.Parent, in.PlanItem, in.Title, in.Prompt,
	)
}

// receiptDir returns the durable receipt directory inside dir.
func receiptDir(dir string) string {
	return filepath.Join(dir, ".session-receipts")
}

// receiptFileName maps a raw request ID to a safe file name. The request ID is
// never used as a literal file name: hashing removes path separators, drive
// letters, reserved Windows names and any other bytes that could escape the
// receipt directory. The raw request ID is stored inside the receipt and
// verified on read, so a hash collision cannot alias two different requests.
func receiptFileName(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return hex.EncodeToString(sum[:16]) + ".json"
}

// SessionReceiptPath returns the receipt path for one request ID. The file name
// is a hash, never the request ID itself.
func SessionReceiptPath(dir, requestID string) string {
	return filepath.Join(receiptDir(dir), receiptFileName(requestID))
}

// SessionReceiptFingerprint hashes the request inputs so a request ID reused
// with different parameters is detected instead of silently reused.
func SessionReceiptFingerprint(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

// syncDir best-effort fsyncs a directory so a just-created entry survives a
// power loss, not only a process crash. It is a no-op on platforms (Windows)
// that do not support syncing a directory handle; the file fsync still guards
// the content.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// writeNewReceiptFile claims path with O_EXCL and writes the full payload with
// fsync before returning. On any failure the torn claim is removed so the next
// attempt can re-reserve cleanly; a successful return leaves a complete,
// fsynced receipt. created=false means another writer already claimed the path.
func writeNewReceiptFile(path string, data []byte) (created bool, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	cleanup := func() {
		_ = f.Close()
		_ = os.Remove(path)
	}
	if _, err := f.Write(data); err != nil {
		cleanup()
		return false, err
	}
	if err := f.Sync(); err != nil {
		cleanup()
		return false, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return false, err
	}
	syncDir(filepath.Dir(path))
	return true, nil
}

// readReceiptFile reads and validates one receipt file. A missing file is
// ok=false; a present-but-unparseable file, or one whose embedded request ID
// does not match the expected value, is ErrSessionReceiptCorrupt.
func readReceiptFile(path, requestID string) (SessionReceipt, bool, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return SessionReceipt{}, false, nil
	}
	if err != nil {
		return SessionReceipt{}, false, fmt.Errorf("read session receipt: %w", err)
	}
	var r SessionReceipt
	if err := json.Unmarshal(b, &r); err != nil {
		return SessionReceipt{}, false, fmt.Errorf("%w: %v", ErrSessionReceiptCorrupt, err)
	}
	if r.SessionID == "" {
		return SessionReceipt{}, false, fmt.Errorf("%w: empty session id", ErrSessionReceiptCorrupt)
	}
	if r.RequestID != "" && r.RequestID != requestID {
		return SessionReceipt{}, false, fmt.Errorf("%w: request id mismatch (%q vs %q)", ErrSessionReceiptCorrupt, r.RequestID, requestID)
	}
	return r, true, nil
}

// ReadSessionReceipt returns the recorded receipt for a request ID, or ok=false.
// A corrupt receipt is an error (ErrSessionReceiptCorrupt), never silently
// treated as "no binding".
func ReadSessionReceipt(dir, requestID string) (SessionReceipt, bool, error) {
	return readReceiptFile(SessionReceiptPath(dir, requestID), requestID)
}

// receiptLocks serializes receipt writes within this process. Cross-process
// safety for the reserve step comes from O_EXCL; for the monotonic advance it
// comes from the leader election that guarantees only one host advances a given
// Assistant's receipts at a time.
var receiptLocks sync.Map // dir+requestID -> *sync.Mutex

func lockReceipt(dir, requestID string) func() {
	key := dir + "\x00" + requestID
	v, _ := receiptLocks.LoadOrStore(key, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// WriteSessionReceipt durably records a request ID -> Session ID binding. It is
// first-write-wins: an existing receipt with the same fingerprint is returned
// unchanged, a different fingerprint is a conflict, and a corrupt receipt is an
// explicit error. The file is written with O_EXCL + fsync, and the receipt
// directory and file use 0700/0600 so the binding is private and durable.
func WriteSessionReceipt(dir, requestID string, receipt SessionReceipt) (SessionReceipt, error) {
	if strings.TrimSpace(requestID) == "" {
		return SessionReceipt{}, errors.New("session receipt requires a request id")
	}
	if receipt.SessionID == "" {
		return SessionReceipt{}, errors.New("session receipt requires session id")
	}
	if receipt.Fingerprint == "" {
		return SessionReceipt{}, errors.New("session receipt requires fingerprint")
	}
	if err := os.MkdirAll(receiptDir(dir), 0o700); err != nil {
		return SessionReceipt{}, fmt.Errorf("create session receipt dir: %w", err)
	}
	unlock := lockReceipt(dir, requestID)
	defer unlock()

	path := SessionReceiptPath(dir, requestID)
	if existing, ok, err := readReceiptFile(path, requestID); err != nil {
		return SessionReceipt{}, err
	} else if ok {
		if existing.Fingerprint != receipt.Fingerprint {
			return SessionReceipt{}, &SessionReceiptConflictError{
				RequestID: requestID, SessionID: existing.SessionID, Fingerprint: existing.Fingerprint,
			}
		}
		return existing, nil
	}

	receipt.RequestID = requestID
	if !validReceiptState(receipt.State) {
		receipt.State = ReceiptReserved
	}
	b, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return SessionReceipt{}, err
	}
	b = append(b, '\n')
	created, err := writeNewReceiptFile(path, b)
	if err != nil {
		return SessionReceipt{}, err
	}
	if !created {
		// Lost a cross-process race: re-read the winner.
		existing, ok, err := readReceiptFile(path, requestID)
		if err != nil {
			return SessionReceipt{}, err
		}
		if !ok {
			return SessionReceipt{}, fmt.Errorf("session receipt raced: %w", ErrSessionReceiptCorrupt)
		}
		if existing.Fingerprint != receipt.Fingerprint {
			return SessionReceipt{}, &SessionReceiptConflictError{
				RequestID: requestID, SessionID: existing.SessionID, Fingerprint: existing.Fingerprint,
			}
		}
		return existing, nil
	}
	return receipt, nil
}

// ReserveSessionReceipt is WriteSessionReceipt with an explicit initial state:
// it claims the request ID at ReceiptReserved and returns the durable binding.
func ReserveSessionReceipt(dir, requestID string, receipt SessionReceipt) (SessionReceipt, error) {
	receipt.State = ReceiptReserved
	return WriteSessionReceipt(dir, requestID, receipt)
}

// AdvanceSessionReceipt moves a receipt to a later state atomically. Advancing
// to the current state (or one already passed) is an idempotent no-op;
// advancing backward is rejected. The write is an atomic replace + fsync.
func AdvanceSessionReceipt(dir, requestID string, to ReceiptState) (SessionReceipt, error) {
	if !validReceiptState(to) {
		return SessionReceipt{}, fmt.Errorf("session receipt: invalid state %q", to)
	}
	unlock := lockReceipt(dir, requestID)
	defer unlock()

	path := SessionReceiptPath(dir, requestID)
	current, ok, err := readReceiptFile(path, requestID)
	if err != nil {
		return SessionReceipt{}, err
	}
	if !ok {
		return SessionReceipt{}, errors.New("session receipt: cannot advance a receipt that does not exist")
	}
	if current.State == "" {
		current.State = ReceiptReserved
	}
	if receiptStateRank[to] == receiptStateRank[current.State] {
		return current, nil // already at this state; idempotent
	}
	if receiptStateRank[to] < receiptStateRank[current.State] {
		return SessionReceipt{}, fmt.Errorf("session receipt: cannot advance %s from %s to %s", requestID, current.State, to)
	}
	current.State = to
	b, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return SessionReceipt{}, err
	}
	b = append(b, '\n')
	if err := fileutil.AtomicWriteFile(path, b, 0o600); err != nil {
		return SessionReceipt{}, fmt.Errorf("session receipt: advance: %w", err)
	}
	return current, nil
}

// ResolveStableSessionPath returns the deterministic session file path for an
// owner+requestID pair, and whether it already exists. It does not create it.
func ResolveStableSessionPath(dir, ownerAssistantID, requestID string) (path string, exists bool) {
	id := StableSessionID(ownerAssistantID, requestID)
	path = filepath.Join(dir, id+".jsonl")
	_, err := os.Lstat(path)
	return path, err == nil
}

// CreateStableSessionFile atomically creates the deterministic session file for
// an owner+requestID pair. It returns the path and whether it created it; a
// concurrent or replayed create resolves to the same path (created=false). The
// placeholder file is 0600 and fsynced before returning.
func CreateStableSessionFile(dir, ownerAssistantID, requestID string) (string, bool, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", false, fmt.Errorf("create session dir: %w", err)
	}
	path, _ := ResolveStableSessionPath(dir, ownerAssistantID, requestID)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return path, false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("create stable session file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return "", false, err
	}
	if err := f.Close(); err != nil {
		return "", false, err
	}
	syncDir(dir)
	return path, true, nil
}
