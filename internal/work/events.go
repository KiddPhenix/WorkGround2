package work

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

// ── Internal event type (not public business event) ────────────────────────

// eventCompact is an internal event type used only for log compaction. It
// carries a snapshot of the full Work projection as its payload so repeated
// replay produces the same result as the original event sequence. It is never
// exposed as a business event type and never appears in WorkViewEvent streams.
const eventCompact WorkEventType = "work.compact"

// knownWorkEventTypes is the set of all event types that this build may write
// or replay. Unknown types trigger a read-only hard error.
var knownWorkEventTypes = map[WorkEventType]bool{
	EventWorkCreated:         true,
	EventDefinitionFrozen:    true,
	EventDraftUpdated:        true,
	EventRunStarted:          true,
	EventStageChanged:        true,
	EventTaskChanged:         true,
	EventAttemptChanged:      true,
	EventBlockUpserted:       true,
	EventBlockRemoved:        true,
	EventCornerstoneUpserted: true,
	EventCornerstoneRemoved:  true,
	EventConclusionUpserted:  true,
	EventArtifactLinked:      true,
	EventWorkArchived:        true,
	EventWorkDeleted:         true,
	eventCompact:             true,
}

// ── Path helpers ───────────────────────────────────────────────────────────

// WorkEventLogPath returns the append-only event log path for a work directory.
func WorkEventLogPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, "work.events.jsonl")
}

// WorkEventIndexPath returns the index sidecar path for a work directory.
func WorkEventIndexPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, "work.event-index.json")
}

// WorkRecoveryPath returns the recovery-copy path used before repairing a torn
// tail, so the original damaged log is preserved for diagnosis.
func WorkRecoveryPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, "work.events.recovery.jsonl")
}

// WorkLeasePath returns the path to the writer lease lock file.
func WorkLeasePath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, "work.lock")
}

// ── Writer identity ────────────────────────────────────────────────────────

var workWriterID string

func init() {
	workWriterID = newWorkWriterID()
}

// WorkWriterID returns the stable writer identity for this process. It is
// embedded in every persisted record and index so that another process (or
// another build) can detect foreign ownership and refuse writes.
func WorkWriterID() string {
	return workWriterID
}

func newWorkWriterID() string {
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		host = "unknown-host"
	}
	host = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, host)
	return fmt.Sprintf("%s-%d-%d", host, os.Getpid(), time.Now().UnixNano())
}

// ── Persisted types ────────────────────────────────────────────────────────

// workEventRecord is the on-disk representation of a Work event. It mirrors
// WorkEvent but uses the same JSON structure (camelCase keys per existing DTO
// convention).
type workEventRecord struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	RequestID     string          `json:"requestId"`
	WorkID        string          `json:"workId"`
	Type          WorkEventType   `json:"type"`
	Revision      int64           `json:"revision"`
	BaseRevision  int64           `json:"baseRevision"`
	Payload       json.RawMessage `json:"payload"`
	ContentDigest string          `json:"contentDigest"`
	WriterID      string          `json:"writerId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func recordFromEvent(e WorkEvent) workEventRecord {
	return workEventRecord{
		SchemaVersion: e.SchemaVersion,
		ID:            e.ID,
		RequestID:     e.RequestID,
		WorkID:        e.WorkID,
		Type:          e.Type,
		Revision:      e.Revision,
		BaseRevision:  e.BaseRevision,
		Payload:       e.Payload,
		ContentDigest: e.ContentDigest,
		WriterID:      e.WriterID,
		CreatedAt:     e.CreatedAt,
	}
}

func eventFromRecord(r workEventRecord) WorkEvent {
	return WorkEvent{
		SchemaVersion: r.SchemaVersion,
		ID:            r.ID,
		RequestID:     r.RequestID,
		WorkID:        r.WorkID,
		Type:          r.Type,
		Revision:      r.Revision,
		BaseRevision:  r.BaseRevision,
		Payload:       r.Payload,
		ContentDigest: r.ContentDigest,
		WriterID:      r.WriterID,
		CreatedAt:     r.CreatedAt,
	}
}

// WorkRequestEntry stores the revision and digest for a requestID so
// idempotency checks can compare semantic content, not just revision numbers.
type WorkRequestEntry struct {
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

// WorkEventIndex is the persisted sidecar that summarises the event log for
// fast startup checks and writer-ownership detection.
type WorkEventIndex struct {
	SchemaVersion int                         `json:"schemaVersion"`
	LogSize       int64                       `json:"logSize"`
	EventCount    int                         `json:"eventCount"`
	Revision      int64                       `json:"revision"`
	ContentDigest string                      `json:"contentDigest"`
	WriterID      string                      `json:"writerId"`
	UpdatedAt     time.Time                   `json:"updatedAt"`
	RequestIndex  map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
}

// ── Lease ──────────────────────────────────────────────────────────────────

// workLeaseMeta is the content of the work.lock file.
type workLeaseMeta struct {
	WriterID   string    `json:"writerId"`
	PID        int       `json:"pid"`
	AcquiredAt time.Time `json:"acquiredAt"`
}

var (
	ErrWorkLeaseHeld     = errors.New("work lease held by another writer")
	ErrWorkLeaseRequired = errors.New("work writer lease required")
	workLeaseMu          sync.Mutex
	workLeases           = map[string]*heldWorkLease{}
)

type heldWorkLease struct {
	lock    *workLeaseLock
	opMu    sync.Mutex
	closing bool
}

func workLeaseKey(workDir string) (string, error) {
	if strings.TrimSpace(workDir) == "" {
		return "", fmt.Errorf("work: empty work dir for lease")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

// AcquireWorkLease takes a process-backed, non-blocking OS lock on work.lock.
// A crashed process releases the OS lock automatically; stale metadata never
// prevents recovery. Repeated acquisition by this process is idempotent.
func AcquireWorkLease(workDir string) error {
	key, err := workLeaseKey(workDir)
	if err != nil {
		return err
	}
	lockPath := WorkLeasePath(key)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	workLeaseMu.Lock()
	defer workLeaseMu.Unlock()
	if held, ok := workLeases[key]; ok {
		if held.closing {
			return fmt.Errorf("%w: local lease is releasing", ErrWorkLeaseHeld)
		}
		return nil
	}
	lock, err := tryLockWorkLease(lockPath)
	if err != nil {
		meta, _ := readWorkLease(lockPath)
		if meta != nil && meta.WriterID != "" {
			return fmt.Errorf("%w: writer %q pid %d", ErrWorkLeaseHeld, meta.WriterID, meta.PID)
		}
		return ErrWorkLeaseHeld
	}
	meta := workLeaseMeta{
		WriterID:   WorkWriterID(),
		PID:        os.Getpid(),
		AcquiredAt: time.Now().UTC(),
	}
	data, err := json.Marshal(meta)
	if err != nil {
		lock.Unlock()
		return err
	}
	data = append(data, '\n')
	if err := lock.WriteMetadata(data); err != nil {
		lock.Unlock()
		return fmt.Errorf("work: write lease metadata: %w", err)
	}
	workLeases[key] = &heldWorkLease{lock: lock}
	return nil
}

// ReleaseWorkLease releases the writer lease. Idempotent: returns nil if no
// lease is held or the lease is held by another writer (stale cleanup).
func ReleaseWorkLease(workDir string) error {
	if strings.TrimSpace(workDir) == "" {
		return nil
	}
	key, err := workLeaseKey(workDir)
	if err != nil {
		return err
	}
	workLeaseMu.Lock()
	held := workLeases[key]
	if held == nil {
		workLeaseMu.Unlock()
		return nil
	}
	if held.closing {
		workLeaseMu.Unlock()
		return nil
	}
	held.closing = true
	workLeaseMu.Unlock()
	held.opMu.Lock()
	workLeaseMu.Lock()
	if workLeases[key] == held {
		delete(workLeases, key)
	}
	workLeaseMu.Unlock()
	err = held.lock.ClearMetadata()
	held.lock.Unlock()
	held.opMu.Unlock()
	return err
}

// requireLease serializes one mutation under a lease already held locally.
func requireLease(workDir string) (func(), error) {
	key, err := workLeaseKey(workDir)
	if err != nil {
		return nil, err
	}
	workLeaseMu.Lock()
	held := workLeases[key]
	workLeaseMu.Unlock()
	if held == nil || held.closing {
		return nil, ErrWorkLeaseRequired
	}
	held.opMu.Lock()
	workLeaseMu.Lock()
	current := workLeases[key]
	workLeaseMu.Unlock()
	if current != held || held.closing {
		held.opMu.Unlock()
		return nil, ErrWorkLeaseRequired
	}
	return held.opMu.Unlock, nil
}

func readWorkLease(lockPath string) (*workLeaseMeta, error) {
	data, err := os.ReadFile(lockPath)
	if err != nil {
		return nil, err
	}
	var meta workLeaseMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("work: corrupt lease file: %w", err)
	}
	return &meta, nil
}

// probeWorkLease checks whether the workDir has an active external lease.
// Returns (leaseExists, isExternal, writerID).
func probeWorkLease(workDir string) (exists bool, external bool, writerID string) {
	key, err := workLeaseKey(workDir)
	if err != nil {
		return false, false, ""
	}
	workLeaseMu.Lock()
	_, local := workLeases[key]
	workLeaseMu.Unlock()
	if local {
		return true, false, WorkWriterID()
	}
	lockPath := WorkLeasePath(key)
	lock, err := tryLockWorkLease(lockPath)
	if err == nil {
		lock.Unlock()
		return false, false, ""
	}
	meta, _ := readWorkLease(lockPath)
	if meta != nil {
		return true, true, meta.WriterID
	}
	return true, true, "unknown"
}

// ── Replay result ──────────────────────────────────────────────────────────

// WorkEventReplay is the result of a tolerant event-log replay. It contains
// all cleanly decoded events, plus enough bookkeeping for writers to detect
// damage, foreign ownership, or future-schema conditions.
type WorkEventReplay struct {
	Events []WorkEvent
	// LastGoodEnd is the byte offset just past the last cleanly applied record.
	LastGoodEnd int64
	// LogSize is the total log size that was replayed.
	LogSize int64
	// NeedsRepair is set when replay stopped early on a torn/corrupt record
	// or a broken append chain.
	NeedsRepair bool
	// ReadOnly is set when the log is owned by a different writer, carries a
	// future schema, contains unknown event types, or a live external lease
	// exists. The caller MUST NOT write, compact, or truncate the log.
	ReadOnly bool
	// ReadOnlyReason explains why the log is read-only.
	ReadOnlyReason string
	// Index is the replayed or rebuilt index.
	Index *WorkEventIndex
	// IndexNeedsRebuild reports that the persisted secondary index was
	// missing or did not exactly describe the validated log.
	IndexNeedsRebuild bool
	// LeaseExternal is set when a live external lease blocks writes.
	LeaseExternal bool
	// RecoveryPath is populated after a successful damaged-tail repair.
	RecoveryPath string
}

// WorkEventReducer is a callback that applies a single event to an in-flight
// projection. It is called in revision order during replay.
//
// The reducer receives the current projection (nil for the first event) and
// must return the updated projection. EventCompact events are handled
// transparently by ReplayWithReducer — the reducer never sees them.
type WorkEventReducer func(event WorkEvent, current *Work) (*Work, error)

// ── Digest ─────────────────────────────────────────────────────────────────

// workEventIdempotentDigest computes a digest over only the caller-controlled
// semantic fields: RequestID, WorkID, Type, Payload. Revision, BaseRevision,
// and ID are excluded so idempotency checks match on content, not on timing.
func workEventIdempotentDigest(r workEventRecord) (string, error) {
	content := struct {
		RequestID string          `json:"requestId"`
		WorkID    string          `json:"workId"`
		Type      WorkEventType   `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}{
		RequestID: r.RequestID,
		WorkID:    r.WorkID,
		Type:      r.Type,
		Payload:   r.Payload,
	}
	return hashCanonical(content)
}

// hashCanonical marshals v to canonical JSON and returns its hex SHA-256.
func hashCanonical(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("work: marshal for hash: %w", err)
	}
	var generic any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return "", fmt.Errorf("work: canonicalise for hash: %w", err)
	}
	canon, err := json.Marshal(generic)
	if err != nil {
		return "", fmt.Errorf("work: re-marshal for hash: %w", err)
	}
	h := sha256.Sum256(canon)
	return fmt.Sprintf("%x", h[:]), nil
}

// workEventContentDigest computes a stable SHA-256 digest over the fields that
// determine an event's semantic meaning, excluding ContentDigest (self-reference
// would break stability), WriterID (set by the system), SchemaVersion, and
// CreatedAt. The digest is computed on canonical JSON of the content fields.
func workEventContentDigest(r workEventRecord) (string, error) {
	content := struct {
		ID           string          `json:"id"`
		RequestID    string          `json:"requestId"`
		WorkID       string          `json:"workId"`
		Type         WorkEventType   `json:"type"`
		Revision     int64           `json:"revision"`
		BaseRevision int64           `json:"baseRevision"`
		Payload      json.RawMessage `json:"payload"`
	}{
		ID:           r.ID,
		RequestID:    r.RequestID,
		WorkID:       r.WorkID,
		Type:         r.Type,
		Revision:     r.Revision,
		BaseRevision: r.BaseRevision,
		Payload:      r.Payload,
	}
	return hashCanonical(content)
}

// ── Errors ─────────────────────────────────────────────────────────────────

// ErrWorkEventConflict is returned when a requestID is reused with different
// content, or when the revision chain is broken.
type ErrWorkEventConflict struct {
	Reason    string `json:"reason"`
	RequestID string `json:"requestId,omitempty"`
	WorkID    string `json:"workId,omitempty"`
}

func (e *ErrWorkEventConflict) Error() string {
	if e.RequestID != "" {
		return fmt.Sprintf("work event conflict for request %s on work %s: %s", e.RequestID, e.WorkID, e.Reason)
	}
	return fmt.Sprintf("work event conflict: %s", e.Reason)
}

// ── Append ─────────────────────────────────────────────────────────────────

// AppendWorkEvent validates and appends a single WorkEvent to the event log.
// It returns the assigned revision on success.
//
// The caller must hold the writer lease (via AcquireWorkLease). If the index is
// missing or corrupt it is rebuilt from the log. WriterID is always overwritten
// with WorkWriterID(); caller-provided values are ignored.
//
// When sync is true the write is fsynced before returning.
func AppendWorkEvent(workDir string, event WorkEvent, sync bool) (int64, error) {
	logPath := WorkEventLogPath(workDir)
	if logPath == "" {
		return 0, fmt.Errorf("work: empty work event log path")
	}

	releaseOp, err := requireLease(workDir)
	if err != nil {
		return 0, err
	}
	defer releaseOp()

	// Validate schema.
	if err := CheckSchemaVersion("WorkEvent", event.SchemaVersion); err != nil {
		return 0, err
	}

	// Validate event type.
	if !knownWorkEventTypes[event.Type] {
		return 0, fmt.Errorf("work: unknown event type %q", event.Type)
	}

	// Reject internal compact events from callers.
	if event.Type == eventCompact {
		return 0, fmt.Errorf("work: cannot append internal event type %q", eventCompact)
	}
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.WorkID) == "" {
		return 0, fmt.Errorf("work: event id and workID are required")
	}

	// Always validate the authoritative log before writing. The index is only
	// a cache; trusting a stale-but-decodable index can duplicate a request
	// after an append succeeded but its index update failed.
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return 0, err
	}
	if replay.ReadOnly {
		return 0, fmt.Errorf("work: event log is read-only: %s", replay.ReadOnlyReason)
	}
	if replay.NeedsRepair {
		return 0, fmt.Errorf("work: cannot append to damaged event log; repair first")
	}
	idx := replay.Index
	if replay.IndexNeedsRebuild && replay.LogSize > 0 {
		if err := writeWorkEventIndex(workDir, idx); err != nil {
			return 0, fmt.Errorf("work: rebuild event index before append: %w", err)
		}
	}

	// WorkID consistency: if the log has existing records, the workID must match.
	if len(replay.Events) > 0 && replay.Events[0].WorkID != event.WorkID {
		return 0, fmt.Errorf("work: workID mismatch: log contains %q, event has %q", replay.Events[0].WorkID, event.WorkID)
	}

	// Determine last revision and next revision.
	lastRevision := int64(0)
	if idx != nil {
		lastRevision = idx.Revision
	}

	// requestID idempotency / conflict — must precede chain validation.
	if event.RequestID != "" && idx != nil && idx.RequestIndex != nil {
		if entry, ok := idx.RequestIndex[event.RequestID]; ok {
			// Compare semantic content (idempotent digest), not just revision.
			newRec := recordFromEvent(event)
			newDigest, digestErr := workEventIdempotentDigest(newRec)
			if digestErr != nil {
				return 0, digestErr
			}
			if newDigest == entry.Digest {
				// Same requestID, same semantic content → idempotent.
				return entry.Revision, nil
			}
			return 0, &ErrWorkEventConflict{
				Reason:    fmt.Sprintf("requestID %q already used at revision %d with different content", event.RequestID, entry.Revision),
				RequestID: event.RequestID,
				WorkID:    event.WorkID,
			}
		}
	}

	// Revision chain: first event must have revision 1 and baseRevision 0.
	// Auto-compute revision only when caller didn't set it (revision==0).
	// This must happen before the chain check.
	if event.Revision == 0 {
		event.Revision = lastRevision + 1
		event.BaseRevision = lastRevision
	}

	if lastRevision == 0 {
		if event.Revision != 1 || event.BaseRevision != 0 {
			return 0, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("first event must have revision=1 baseRevision=0, got revision=%d baseRevision=%d", event.Revision, event.BaseRevision),
				WorkID: event.WorkID,
			}
		}
	} else {
		if event.BaseRevision != lastRevision {
			return 0, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("revision chain broken: expected baseRevision=%d, got baseRevision=%d", lastRevision, event.BaseRevision),
				WorkID: event.WorkID,
			}
		}
		if event.Revision != lastRevision+1 {
			return 0, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("revision gap: expected revision=%d, got revision=%d", lastRevision+1, event.Revision),
				WorkID: event.WorkID,
			}
		}
	}

	// Always overwrite SchemaVersion, WriterID, CreatedAt.
	event.SchemaVersion = WorkEventSchemaVersion
	event.WriterID = WorkWriterID()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	// Compute and verify content digest.
	rec := recordFromEvent(event)
	rec.ContentDigest = "" // recompute — never trust caller-provided digest
	digest, err := workEventContentDigest(rec)
	if err != nil {
		return 0, err
	}
	rec.ContentDigest = digest
	rec.SchemaVersion = WorkEventSchemaVersion

	// Serialize.
	buf, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("work: encode event: %w", err)
	}
	buf = append(buf, '\n')

	// Ensure directory exists.
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return 0, err
	}

	// Append to log.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("work: open event log: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return 0, fmt.Errorf("work: append event: %w", err)
	}
	if sync {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return 0, err
		}
	}
	if err := f.Close(); err != nil {
		return 0, err
	}

	// Update index. If write fails, the next Append or Rebuild will self-heal.
	newIdx := buildIndexFromReplay(rec.Revision, rec.ContentDigest, replay.LogSize+int64(len(buf)), 1, nil)
	if idx != nil {
		newIdx.EventCount = idx.EventCount + 1
		if idx.RequestIndex != nil {
			for k, v := range idx.RequestIndex {
				newIdx.RequestIndex[k] = v
			}
		}
	}
	if rec.RequestID != "" {
		idDigest, _ := workEventIdempotentDigest(rec)
		newIdx.RequestIndex[rec.RequestID] = WorkRequestEntry{Revision: rec.Revision, Digest: idDigest}
	}
	if err := writeWorkEventIndexAfterAppend(workDir, newIdx); err != nil {
		return rec.Revision, fmt.Errorf("work: event appended at revision %d but index update failed: %w", rec.Revision, err)
	}

	return rec.Revision, nil
}

// ── Replay ─────────────────────────────────────────────────────────────────

// ReplayWorkEventLog decodes the event log tolerantly: decoding stops at the
// first record that fails to parse, fails chain validation, or carries an
// unsupported schema/type. The state up to that point is returned; when
// NeedsRepair is true the writer may repair the tail.
//
// Future schema, unknown event types, and a live external lease result in
// ReadOnly=true. Historical (non-live) external writerIDs do NOT cause
// ReadOnly — a new process can acquire the lease and write.
//
// If the index is missing or corrupt, it is silently rebuilt from the log.
func ReplayWorkEventLog(workDir string) (*WorkEventReplay, error) {
	logPath := WorkEventLogPath(workDir)
	if logPath == "" {
		return nil, fmt.Errorf("work: empty event log path")
	}
	// Lease ownership is checked even when the log has not been created yet;
	// an external writer may be between acquiring the lease and first append.
	leaseExists, leaseExternal, leaseWriter := probeWorkLease(workDir)
	if leaseExists && leaseExternal {
		return &WorkEventReplay{
			ReadOnly:       true,
			ReadOnlyReason: fmt.Sprintf("live external lease held by writer %q", leaseWriter),
			LeaseExternal:  true,
		}, nil
	}
	f, err := os.Open(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			replay := &WorkEventReplay{}
			diskIndex, _ := readWorkEventIndex(workDir)
			finalizeReplayIndex(replay, diskIndex)
			return replay, nil
		}
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("work: event log path is a directory: %s", logPath)
	}
	replay := &WorkEventReplay{LogSize: info.Size()}

	// Load index. If corrupt, ignore it — log is the source of truth.
	// The caller (AppendWorkEvent) will try to rebuild on write.
	diskIndex, idxErr := readWorkEventIndex(workDir)
	if idxErr != nil && !os.IsNotExist(idxErr) {
		diskIndex = nil
	}
	replay.Index = diskIndex

	dec := json.NewDecoder(f)
	var lastRevision int64
	for {
		var rec workEventRecord
		if err := dec.Decode(&rec); err != nil {
			if errors.Is(err, io.EOF) {
				finalizeReplayIndex(replay, diskIndex)
				return replay, nil
			}
			replay.NeedsRepair = true
			return replay, nil
		}

		// Schema check.
		if rec.SchemaVersion > WorkEventSchemaVersion {
			replay.ReadOnly = true
			replay.ReadOnlyReason = fmt.Sprintf("future schema version %d at offset %d", rec.SchemaVersion, dec.InputOffset())
			return replay, nil
		}
		if rec.SchemaVersion <= 0 {
			replay.NeedsRepair = true
			return replay, nil
		}

		// Type check.
		if !knownWorkEventTypes[rec.Type] {
			replay.ReadOnly = true
			replay.ReadOnlyReason = fmt.Sprintf("unknown event type %q at offset %d", rec.Type, dec.InputOffset())
			return replay, nil
		}

		// writerID must be non-empty.
		if rec.WriterID == "" {
			replay.NeedsRepair = true
			return replay, nil
		}
		if strings.TrimSpace(rec.ID) == "" || strings.TrimSpace(rec.WorkID) == "" {
			replay.NeedsRepair = true
			return replay, nil
		}

		// WorkID consistency.
		if len(replay.Events) > 0 && rec.WorkID != replay.Events[0].WorkID {
			replay.NeedsRepair = true
			return replay, nil
		}

		// A compacted log may begin above revision 1, but its base must still
		// be exactly revision-1. Every later record follows the normal chain.
		if lastRevision == 0 && rec.Type == eventCompact {
			if rec.Revision <= 0 || rec.BaseRevision != rec.Revision-1 {
				replay.NeedsRepair = true
				return replay, nil
			}
		} else if lastRevision == 0 {
			if rec.Revision != 1 || rec.BaseRevision != 0 {
				replay.NeedsRepair = true
				return replay, nil
			}
		} else if rec.BaseRevision != lastRevision || rec.Revision != lastRevision+1 {
			replay.NeedsRepair = true
			return replay, nil
		}

		// Digest verification.
		expectedDigest, err := workEventContentDigest(rec)
		if err != nil {
			replay.NeedsRepair = true
			return replay, nil
		}
		if rec.ContentDigest != expectedDigest {
			replay.NeedsRepair = true
			return replay, nil
		}

		replay.Events = append(replay.Events, eventFromRecord(rec))
		lastRevision = rec.Revision
		replay.LastGoodEnd = dec.InputOffset()
	}
}

func finalizeReplayIndex(replay *WorkEventReplay, disk *WorkEventIndex) {
	authoritative := buildIndexFromReplay(0, "", replay.LogSize, len(replay.Events), replay.Events)
	replay.Index = authoritative
	replay.IndexNeedsRebuild = !workEventIndexesEqual(disk, authoritative)
}

func workEventIndexesEqual(a, b *WorkEventIndex) bool {
	if a == nil || b == nil ||
		a.SchemaVersion != WorkEventSchemaVersion ||
		a.LogSize != b.LogSize ||
		a.EventCount != b.EventCount ||
		a.Revision != b.Revision ||
		a.ContentDigest != b.ContentDigest ||
		len(a.RequestIndex) != len(b.RequestIndex) {
		return false
	}
	for requestID, want := range b.RequestIndex {
		if got, ok := a.RequestIndex[requestID]; !ok || got != want {
			return false
		}
	}
	return true
}

// ReplayWithReducer replays the event log and applies a reducer to build a
// projection. It transparently handles internal EventCompact events internally
// — the reducer never sees them.
//
// Returns an error if reducer is nil.
func ReplayWithReducer(workDir string, reducer WorkEventReducer) (*WorkEventReplay, *Work, error) {
	if reducer == nil {
		return nil, nil, fmt.Errorf("work: reducer must not be nil")
	}
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return nil, nil, err
	}
	var projection *Work
	for _, e := range replay.Events {
		if e.Type == eventCompact {
			// Internal compact event: extract projection from wrapper payload.
			var cp struct {
				Projection   Work                        `json:"projection"`
				RequestIndex map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
			}
			if err := json.Unmarshal(e.Payload, &cp); err != nil {
				// Fall back: legacy compact events had the Work directly.
				var snap Work
				if err2 := json.Unmarshal(e.Payload, &snap); err2 != nil {
					return replay, nil, fmt.Errorf("work: decode compact snapshot at revision %d: %w", e.Revision, err)
				}
				projection = &snap
			} else {
				projection = &cp.Projection
			}
			continue
		}
		projection, err = reducer(e, projection)
		if err != nil {
			return replay, nil, fmt.Errorf("work: reducer failed at revision %d: %w", e.Revision, err)
		}
	}
	return replay, projection, nil
}

// ── Compact ────────────────────────────────────────────────────────────────

// CompactWorkEventLog rewrites the event log as a single compact event via an
// atomic tmp+fsync+rename. The compact event records the full projection as its
// payload so that replay produces an equivalent result.
//
// The caller must hold the writer lease. The provided projection must match the
// result of replaying the current log through the reducer. Nil projection or
// reducer returns an error.
//
// The compact event preserves all historical requestID→{revision,digest}
// mappings from the original log so idempotency checks continue to work.
func CompactWorkEventLog(workDir string, projection *Work, reducer WorkEventReducer) error {
	if projection == nil {
		return fmt.Errorf("work: projection must not be nil for compact")
	}
	if strings.TrimSpace(projection.ID) == "" {
		return fmt.Errorf("work: projection work ID must not be empty for compact")
	}
	if reducer == nil {
		return fmt.Errorf("work: reducer must not be nil for compact")
	}

	logPath := WorkEventLogPath(workDir)
	if logPath == "" {
		return fmt.Errorf("work: empty event log path")
	}

	releaseOp, err := requireLease(workDir)
	if err != nil {
		return err
	}
	defer releaseOp()

	// Replay to verify we own the log and get the current state.
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return fmt.Errorf("work: replay before compact: %w", err)
	}
	if replay.ReadOnly {
		return fmt.Errorf("work: cannot compact read-only log: %s", replay.ReadOnlyReason)
	}
	if replay.NeedsRepair {
		return fmt.Errorf("work: cannot compact damaged log — repair first")
	}
	if len(replay.Events) > 0 && replay.Events[0].WorkID != projection.ID {
		return fmt.Errorf("work: compact projection workID %q does not match log workID %q", projection.ID, replay.Events[0].WorkID)
	}

	lastRevision := int64(0)
	if len(replay.Events) > 0 {
		lastRevision = replay.Events[len(replay.Events)-1].Revision
	}

	// Verify projection equivalence by replaying through the reducer.
	replayProj, err := func() (*Work, error) {
		var p *Work
		for _, e := range replay.Events {
			if e.Type == eventCompact {
				var cp struct {
					Projection   Work                        `json:"projection"`
					RequestIndex map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
				}
				if err := json.Unmarshal(e.Payload, &cp); err != nil {
					// Legacy fallback.
					var snap Work
					if err2 := json.Unmarshal(e.Payload, &snap); err2 != nil {
						return nil, err
					}
					p = &snap
				} else {
					p = &cp.Projection
				}
				continue
			}
			var rerr error
			p, rerr = reducer(e, p)
			if rerr != nil {
				return nil, rerr
			}
		}
		return p, nil
	}()
	if err != nil {
		return fmt.Errorf("work: reducer replay before compact: %w", err)
	}

	providedJSON, err := json.Marshal(projection)
	if err != nil {
		return fmt.Errorf("work: marshal provided projection: %w", err)
	}
	replayedJSON, err := json.Marshal(replayProj)
	if err != nil {
		return fmt.Errorf("work: marshal replayed projection: %w", err)
	}
	if string(providedJSON) != string(replayedJSON) {
		return fmt.Errorf("work: compact rejected: provided projection differs from replayed projection")
	}

	// Build the compact event payload with embedded requestIndex so
	// RebuildWorkEventIndex can restore idempotency state.
	compactPayload := struct {
		Projection   *Work                       `json:"projection"`
		RequestIndex map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
	}{
		Projection:   projection,
		RequestIndex: map[string]WorkRequestEntry{},
	}
	// Collect requestIDs from the original replay.
	if replay.Index != nil && replay.Index.RequestIndex != nil {
		for k, v := range replay.Index.RequestIndex {
			compactPayload.RequestIndex[k] = v
		}
	}
	payload, err := json.Marshal(compactPayload)
	if err != nil {
		return fmt.Errorf("work: marshal compact payload: %w", err)
	}

	newRevision := lastRevision + 1
	event := WorkEvent{
		SchemaVersion: WorkEventSchemaVersion,
		ID:            fmt.Sprintf("compact-%d", time.Now().UnixNano()),
		WorkID:        projection.ID,
		Type:          eventCompact,
		Revision:      newRevision,
		BaseRevision:  lastRevision,
		Payload:       json.RawMessage(payload),
		WriterID:      WorkWriterID(),
		CreatedAt:     time.Now().UTC(),
	}
	rec := recordFromEvent(event)
	rec.ContentDigest = ""
	digest, err := workEventContentDigest(rec)
	if err != nil {
		return err
	}
	rec.ContentDigest = digest

	buf, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("work: encode compact event: %w", err)
	}
	buf = append(buf, '\n')

	// Atomic write.
	if err := fileutil.AtomicWriteFile(logPath, buf, 0o644); err != nil {
		return fmt.Errorf("work: atomic write compact log: %w", err)
	}

	compactEvent := eventFromRecord(rec)
	index := buildIndexFromReplay(0, "", int64(len(buf)), 1, []WorkEvent{compactEvent})
	if err := writeWorkEventIndex(workDir, index); err != nil {
		return fmt.Errorf("work: compact log replaced at revision %d but index update failed: %w", rec.Revision, err)
	}
	return nil
}

// ── Repair ─────────────────────────────────────────────────────────────────

// RepairWorkEventLogTail truncates undecodable or chain-broken bytes left by a
// crash or disk-full append. Before truncating, it copies the original damaged
// log to a recovery sidecar file so the damage can be diagnosed.
//
// The caller must hold the writer lease. The provided replay must be a prior
// ReplayWorkEventLog result that shows NeedsRepair=true. The replay's LogSize
// and LastGoodEnd are re-validated against the current file; a stale replay
// is rejected.
//
// Future schema, unknown event types, and live external leases result in
// read-only — no repair is attempted. Repair is safe to retry.
func RepairWorkEventLogTail(workDir string, replay *WorkEventReplay) error {
	logPath := WorkEventLogPath(workDir)
	if logPath == "" {
		return nil
	}
	if replay == nil {
		return fmt.Errorf("work: repair requires a replay result")
	}
	releaseOp, err := requireLease(workDir)
	if err != nil {
		return err
	}
	defer releaseOp()

	// Re-replay while the lease serializes all writers. Matching size alone
	// cannot detect same-size replacement or a changed validation boundary.
	current, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return err
	}
	if current.ReadOnly {
		return fmt.Errorf("work: cannot repair read-only log: %s", current.ReadOnlyReason)
	}
	if current.LogSize != replay.LogSize || current.LastGoodEnd != replay.LastGoodEnd || current.NeedsRepair != replay.NeedsRepair {
		return fmt.Errorf("work: stale replay: current size/end/repair=%d/%d/%t, replay=%d/%d/%t",
			current.LogSize, current.LastGoodEnd, current.NeedsRepair,
			replay.LogSize, replay.LastGoodEnd, replay.NeedsRepair)
	}
	if !current.NeedsRepair {
		if current.IndexNeedsRebuild {
			if err := writeWorkEventIndex(workDir, current.Index); err != nil {
				return fmt.Errorf("work: rebuild index: %w", err)
			}
		}
		return nil
	}

	// Generate a non-overwriting recovery copy.
	recoveryPath := WorkRecoveryPath(workDir)
	// Add timestamp suffix to avoid overwriting prior recovery copies.
	ext := filepath.Ext(recoveryPath)
	base := recoveryPath[:len(recoveryPath)-len(ext)]
	tsRecoveryPath := fmt.Sprintf("%s-%d%s", base, time.Now().UnixNano(), ext)

	if err := os.MkdirAll(filepath.Dir(tsRecoveryPath), 0o755); err != nil {
		return fmt.Errorf("work: mkdir for recovery: %w", err)
	}
	if err := copyFile(logPath, tsRecoveryPath); err != nil {
		return fmt.Errorf("work: save recovery copy: %w", err)
	}

	// Write back only the valid prefix from the recovery copy.
	if err := writeLogPrefix(tsRecoveryPath, logPath, current.LastGoodEnd); err != nil {
		return fmt.Errorf("work: write repaired log: %w", err)
	}
	repaired, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return fmt.Errorf("work: replay repaired log: %w", err)
	}
	if repaired.ReadOnly || repaired.NeedsRepair {
		return fmt.Errorf("work: repaired log did not validate")
	}
	if err := writeWorkEventIndex(workDir, repaired.Index); err != nil {
		return fmt.Errorf("work: rebuild index after repair: %w", err)
	}
	replay.RecoveryPath = tsRecoveryPath
	slog.Warn("repaired Work event log tail",
		"logPath", logPath,
		"recoveryPath", tsRecoveryPath,
		"lastGoodRevision", repaired.Index.Revision,
		"writerID", WorkWriterID())
	return nil
}

// writeLogPrefix copies the first size bytes from src to dst, ensuring the
// output ends with a newline and is fsynced.
func writeLogPrefix(src, dst string, size int64) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if int64(len(data)) < size {
		return fmt.Errorf("work: source file shorter than expected prefix: %d < %d", len(data), size)
	}
	prefix := data[:size]
	if len(prefix) > 0 && prefix[len(prefix)-1] != '\n' {
		prefix = append(prefix, '\n')
	}
	return fileutil.AtomicWriteFile(dst, prefix, 0o644)
}

func copyFile(src, dst string) error {
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 30 * time.Millisecond)
		}
		data, err := os.ReadFile(src)
		if err != nil {
			lastErr = err
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := fileutil.AtomicWriteFile(dst, data, 0o644); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return fmt.Errorf("copyFile %s: %w after 10 retries", src, lastErr)
}

// ── Index ──────────────────────────────────────────────────────────────────

func readWorkEventIndex(workDir string) (*WorkEventIndex, error) {
	indexPath := WorkEventIndexPath(workDir)
	if indexPath == "" {
		return nil, nil
	}
	b, err := os.ReadFile(indexPath)
	if err != nil {
		return nil, err
	}
	var idx WorkEventIndex
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	if idx.SchemaVersion > WorkEventSchemaVersion {
		return nil, fmt.Errorf("work: unsupported event index schema %d", idx.SchemaVersion)
	}
	return &idx, nil
}

func writeWorkEventIndex(workDir string, idx *WorkEventIndex) error {
	indexPath := WorkEventIndexPath(workDir)
	if indexPath == "" {
		return nil
	}
	idx.SchemaVersion = WorkEventSchemaVersion
	idx.UpdatedAt = time.Now().UTC()
	if idx.WriterID == "" {
		idx.WriterID = WorkWriterID()
	}
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(indexPath, b, 0o644)
}

// writeWorkEventIndexAfterAppend is a narrow failure-injection seam for the
// only non-atomic boundary in AppendWorkEvent: log append succeeded, index
// replacement failed. Production always uses writeWorkEventIndex.
var writeWorkEventIndexAfterAppend = writeWorkEventIndex

// ReadWorkEventIndex loads the event index from disk. Returns nil when no
// index exists yet.
func ReadWorkEventIndex(workDir string) (*WorkEventIndex, error) {
	idx, err := readWorkEventIndex(workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return idx, nil
}

func buildIndexFromReplay(revision int64, digest string, logSize int64, eventCount int, events []WorkEvent) *WorkEventIndex {
	idx := &WorkEventIndex{
		SchemaVersion: WorkEventSchemaVersion,
		LogSize:       logSize,
		EventCount:    eventCount,
		Revision:      revision,
		ContentDigest: digest,
		WriterID:      WorkWriterID(),
		UpdatedAt:     time.Now().UTC(),
		RequestIndex:  map[string]WorkRequestEntry{},
	}
	if len(events) > 0 {
		last := events[len(events)-1]
		idx.Revision = last.Revision
		idx.ContentDigest = last.ContentDigest
		for _, e := range events {
			if e.RequestID != "" {
				// Use the idempotent digest for the requestIndex entry.
				rec := recordFromEvent(e)
				idDigest, _ := workEventIdempotentDigest(rec)
				idx.RequestIndex[e.RequestID] = WorkRequestEntry{Revision: e.Revision, Digest: idDigest}
			}
			if e.Type == eventCompact {
				// Extract embedded requestIndex from compact payload.
				var cp struct {
					Projection   json.RawMessage             `json:"projection"`
					RequestIndex map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
				}
				if err := json.Unmarshal(e.Payload, &cp); err == nil && cp.RequestIndex != nil {
					for k, v := range cp.RequestIndex {
						if _, exists := idx.RequestIndex[k]; !exists {
							idx.RequestIndex[k] = v
						}
					}
				}
			}
		}
	}
	return idx
}

// RebuildWorkEventIndex replays the event log and rebuilds the index from
// scratch. This is the authoritative recovery path when the index drifts from
// the log (e.g. after a crash during append).
func RebuildWorkEventIndex(workDir string) (*WorkEventIndex, error) {
	releaseOp, err := requireLease(workDir)
	if err != nil {
		return nil, err
	}
	defer releaseOp()
	replay, err := ReplayWorkEventLog(workDir)
	if err != nil {
		return nil, fmt.Errorf("work: replay for index rebuild: %w", err)
	}
	if replay.ReadOnly {
		return nil, fmt.Errorf("work: cannot rebuild index for read-only log: %s", replay.ReadOnlyReason)
	}
	if replay.NeedsRepair {
		return nil, fmt.Errorf("work: cannot rebuild index for damaged log — repair first")
	}

	idx := replay.Index
	if err := writeWorkEventIndex(workDir, idx); err != nil {
		return nil, err
	}
	return idx, nil
}
