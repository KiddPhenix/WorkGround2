package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
	EventRunChanged:          true,
	EventStageChanged:        true,
	EventTaskChanged:         true,
	EventAttemptChanged:      true,
	EventBlockUpserted:       true,
	EventBlockRemoved:        true,
	EventCornerstoneUpserted: true,
	EventCornerstoneRemoved:  true,
	EventCornerstoneRestored: true,
	EventCornerstoneGC:       true,
	EventConclusionUpserted:  true,
	EventArtifactLinked:      true,
	EventWorkArchived:        true,
	EventWorkRestored:        true,
	EventWorkDeleted:         true,
	EventBlockActionReserved: true,
	EventBlockActionChanged:  true,
	eventCompact:             true,
	// V2 event types.
	EventDefPlanningStarted:      true,
	EventDefRevisionCreated:      true,
	EventDefRevisionApplied:      true,
	EventArtifactSlotDeclared:    true,
	EventArtifactSlotUpdated:     true,
	EventInputRequested:          true,
	EventInputDraftSaved:         true,
	EventInputSubmitted:          true,
	EventInputRejected:           true,
	EventInputCornerstoneChanged: true,
	EventPatchPreviewed:          true,
	EventPatchApplied:            true,
	EventTaskInvalidated:         true,
	EventTaskReady:               true,
	EventTaskWaitingInput:        true,
	EventTaskWaitingApproval:     true,
	EventTaskRuntimeCreated:      true,
	EventTaskRuntimeUpdated:      true,
	EventTaskStaleResult:         true,
	EventNodeSkillBound:          true,
	EventNodeSkillCleared:        true,
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

// WorkRequestIndexPath returns the append-only request receipt index path.
// Keeping receipts separate prevents the event index header from growing and
// being rewritten on every live progress update.
func WorkRequestIndexPath(workDir string) string {
	if workDir == "" {
		return ""
	}
	return filepath.Join(workDir, "work.request-index.jsonl")
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
// another build) can identify the producer. Cross-process exclusion is enforced
// by the OS lease; completed history from an earlier process remains replayable.
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
	Object        ObjectContext   `json:"object,omitempty"`
	Payload       json.RawMessage `json:"payload"`
	ContentDigest string          `json:"contentDigest"`
	WriterID      string          `json:"writerId"`
	CreatedAt     time.Time       `json:"createdAt"`
}

func recordFromEvent(e WorkEvent) workEventRecord {
	return workEventRecord{
		SchemaVersion: e.SchemaVersion, ID: e.ID, RequestID: e.RequestID,
		WorkID: e.WorkID, Type: e.Type, Revision: e.Revision, BaseRevision: e.BaseRevision,
		Object: e.Object, Payload: e.Payload, ContentDigest: e.ContentDigest,
		WriterID: e.WriterID, CreatedAt: e.CreatedAt,
	}
}

func eventFromRecord(r workEventRecord) WorkEvent {
	return WorkEvent{
		SchemaVersion: r.SchemaVersion, ID: r.ID, RequestID: r.RequestID,
		WorkID: r.WorkID, Type: r.Type, Revision: r.Revision, BaseRevision: r.BaseRevision,
		Object: r.Object, Payload: r.Payload, ContentDigest: r.ContentDigest,
		WriterID: r.WriterID, CreatedAt: r.CreatedAt,
	}
}

// MarshalJSON omits object for schema==1, always includes it for schema>=2.
func (r workEventRecord) MarshalJSON() ([]byte, error) {
	type alias struct {
		SchemaVersion int             `json:"schemaVersion"`
		ID            string          `json:"id"`
		RequestID     string          `json:"requestId"`
		WorkID        string          `json:"workId"`
		Type          WorkEventType   `json:"type"`
		Revision      int64           `json:"revision"`
		BaseRevision  int64           `json:"baseRevision"`
		Object        *ObjectContext  `json:"object,omitempty"`
		Payload       json.RawMessage `json:"payload"`
		ContentDigest string          `json:"contentDigest"`
		WriterID      string          `json:"writerId"`
		CreatedAt     time.Time       `json:"createdAt"`
	}
	a := alias{
		SchemaVersion: r.SchemaVersion, ID: r.ID, RequestID: r.RequestID,
		WorkID: r.WorkID, Type: r.Type, Revision: r.Revision, BaseRevision: r.BaseRevision,
		Payload: r.Payload, ContentDigest: r.ContentDigest,
		WriterID: r.WriterID, CreatedAt: r.CreatedAt,
	}
	if r.SchemaVersion >= SchemaVersionV2 {
		a.Object = &r.Object
	}
	return json.Marshal(a)
}

// WorkRequestEntry stores semantic and event identity for a requestID. EventID
// and Type let intent-level APIs validate replay after restart or compaction;
// legacy indexes omit them and are rebuilt when source events remain.
type WorkRequestEntry struct {
	Revision int64         `json:"revision"`
	Digest   string        `json:"digest"`
	EventID  string        `json:"eventId,omitempty"`
	Type     WorkEventType `json:"type,omitempty"`
	// Event preserves the immutable retry intent across event-log compaction.
	// It is populated only for receipts whose replay requires their full intent.
	Event *WorkEvent `json:"event,omitempty"`
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
	appendRequest string
}

type workRequestIndexRecord struct {
	RequestID string           `json:"requestId"`
	Entry     WorkRequestEntry `json:"entry"`
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
	// LogFingerprint is the SHA-256 of the exact log bytes observed by replay.
	// Repair compares it after taking the lease so same-size replacements cannot
	// be mistaken for the previously validated file generation.
	LogFingerprint string
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
		Object    *ObjectContext  `json:"object,omitempty"`
	}{
		RequestID: r.RequestID, WorkID: r.WorkID, Type: r.Type, Payload: r.Payload,
	}
	if r.SchemaVersion >= SchemaVersionV2 {
		content.Object = &r.Object
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
		Object       *ObjectContext  `json:"object,omitempty"`
	}{
		ID: r.ID, RequestID: r.RequestID, WorkID: r.WorkID, Type: r.Type,
		Revision: r.Revision, BaseRevision: r.BaseRevision, Payload: r.Payload,
	}
	if r.SchemaVersion >= SchemaVersionV2 {
		content.Object = &r.Object
	}
	return hashCanonical(content)
}

// ── Errors ─────────────────────────────────────────────────────────────────

// ErrWorkEventConflict is returned when a requestID is reused with different
// content, or when the revision chain is broken.
type WorkEventConflictKind string

const (
	WorkEventRequestConflict  WorkEventConflictKind = "request_id"
	WorkEventRevisionConflict WorkEventConflictKind = "revision_chain"
)

type ErrWorkEventConflict struct {
	Reason    string                `json:"reason"`
	RequestID string                `json:"requestId,omitempty"`
	WorkID    string                `json:"workId,omitempty"`
	Kind      WorkEventConflictKind `json:"kind,omitempty"`
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
	releaseOp, err := requireLease(workDir)
	if err != nil {
		return 0, err
	}
	defer releaseOp()

	if err := validateWorkEventForAppend(event); err != nil {
		return 0, err
	}

	// Always validate the authoritative log before the public low-level append.
	// FileWorkStore uses appendWorkEventIndexed after it has independently
	// verified the projection, manifest, event index and log tail.
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
	existingWorkID := ""
	if len(replay.Events) > 0 {
		existingWorkID = replay.Events[0].WorkID
	}
	result, err := appendWorkEventValidated(
		workDir,
		event,
		sync,
		idx,
		replay.LogSize,
		existingWorkID,
	)
	return result.Revision, err
}

type workEventAppendResult struct {
	Revision int64
	Event    WorkEvent
	Index    *WorkEventIndex
	Appended bool
}

// appendWorkEventIndexed is the steady-state append seam used by
// FileWorkStore. It avoids replaying an already verified event prefix while
// still detecting the normal crash boundary where the log append succeeded
// but the index replacement did not.
func appendWorkEventIndexed(
	workDir string,
	event WorkEvent,
	sync bool,
	idx *WorkEventIndex,
	existingWorkID string,
) (workEventAppendResult, error) {
	if err := validateWorkEventForAppend(event); err != nil {
		return workEventAppendResult{}, err
	}
	if idx == nil || idx.Revision <= 0 || idx.LogSize <= 0 {
		return workEventAppendResult{}, errors.New("work: indexed append requires a non-empty verified event index")
	}
	logPath := WorkEventLogPath(workDir)
	info, err := os.Stat(logPath)
	if err != nil {
		return workEventAppendResult{}, fmt.Errorf("work: stat indexed event log: %w", err)
	}
	if info.IsDir() || info.Size() != idx.LogSize {
		return workEventAppendResult{}, fmt.Errorf(
			"work: indexed event log size mismatch: index=%d log=%d",
			idx.LogSize,
			info.Size(),
		)
	}
	last, err := readLastWorkEventRecord(logPath, info.Size())
	if err != nil {
		return workEventAppendResult{}, fmt.Errorf("work: verify indexed event log tail: %w", err)
	}
	if last.Revision != idx.Revision || last.ContentDigest != idx.ContentDigest ||
		(existingWorkID != "" && last.WorkID != existingWorkID) {
		return workEventAppendResult{}, errors.New("work: indexed event log tail does not match event index")
	}
	return appendWorkEventValidated(
		workDir,
		event,
		sync,
		idx,
		idx.LogSize,
		existingWorkID,
	)
}

func validateWorkEventForAppend(event WorkEvent) error {
	// Validate schema/type dispatch.
	// schema=1: only V1 types. schema=2: only V2 types. Other: rejected.
	switch event.SchemaVersion {
	case SchemaVersion:
		if IsV2EventType(event.Type) {
			return fmt.Errorf("work: V1 schema event cannot have V2 type %q", event.Type)
		}
		if err := CheckSchemaVersion("WorkEvent", event.SchemaVersion); err != nil {
			return err
		}
	case SchemaVersionV2:
		if !IsV2EventType(event.Type) {
			return fmt.Errorf("work: V2 schema event cannot have V1 type %q", event.Type)
		}
		if err := CheckSchemaVersionV2("WorkEvent", event.SchemaVersion); err != nil {
			return err
		}
		if err := ValidateV2WorkEvent(event); err != nil {
			return fmt.Errorf("work: V2 event validation: %w", err)
		}
	default:
		return fmt.Errorf("work: unsupported event schemaVersion %d", event.SchemaVersion)
	}

	// Validate event type.
	if !knownWorkEventTypes[event.Type] {
		return fmt.Errorf("work: unknown event type %q", event.Type)
	}

	// Reject internal compact events from callers.
	if event.Type == eventCompact {
		return fmt.Errorf("work: cannot append internal event type %q", eventCompact)
	}
	if strings.TrimSpace(event.ID) == "" || strings.TrimSpace(event.WorkID) == "" {
		return fmt.Errorf("work: event id and workID are required")
	}
	return nil
}

func appendWorkEventValidated(
	workDir string,
	event WorkEvent,
	sync bool,
	idx *WorkEventIndex,
	logSize int64,
	existingWorkID string,
) (workEventAppendResult, error) {
	logPath := WorkEventLogPath(workDir)
	if logPath == "" {
		return workEventAppendResult{}, fmt.Errorf("work: empty work event log path")
	}
	if existingWorkID != "" && existingWorkID != event.WorkID {
		return workEventAppendResult{}, fmt.Errorf(
			"work: workID mismatch: log contains %q, event has %q",
			existingWorkID,
			event.WorkID,
		)
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
				return workEventAppendResult{}, digestErr
			}
			if newDigest == entry.Digest {
				// Same requestID, same semantic content → idempotent.
				return workEventAppendResult{
					Revision: entry.Revision,
					Index:    idx,
				}, nil
			}
			return workEventAppendResult{}, &ErrWorkEventConflict{
				Reason:    fmt.Sprintf("requestID %q already used at revision %d with different content", event.RequestID, entry.Revision),
				RequestID: event.RequestID,
				WorkID:    event.WorkID,
				Kind:      WorkEventRequestConflict,
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
			return workEventAppendResult{}, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("first event must have revision=1 baseRevision=0, got revision=%d baseRevision=%d", event.Revision, event.BaseRevision),
				WorkID: event.WorkID,
				Kind:   WorkEventRevisionConflict,
			}
		}
	} else {
		if event.BaseRevision != lastRevision {
			return workEventAppendResult{}, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("revision chain broken: expected baseRevision=%d, got baseRevision=%d", lastRevision, event.BaseRevision),
				WorkID: event.WorkID,
				Kind:   WorkEventRevisionConflict,
			}
		}
		if event.Revision != lastRevision+1 {
			return workEventAppendResult{}, &ErrWorkEventConflict{
				Reason: fmt.Sprintf("revision gap: expected revision=%d, got revision=%d", lastRevision+1, event.Revision),
				WorkID: event.WorkID,
				Kind:   WorkEventRevisionConflict,
			}
		}
	}

	// Always overwrite WriterID, CreatedAt. Preserve caller SchemaVersion.
	event.WriterID = WorkWriterID()
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}

	// Compute and verify content digest.
	rec := recordFromEvent(event)
	rec.ContentDigest = "" // recompute — never trust caller-provided digest
	digest, err := workEventContentDigest(rec)
	if err != nil {
		return workEventAppendResult{}, err
	}
	rec.ContentDigest = digest
	// Preserve caller SchemaVersion; never stamp to a fixed version.

	// Serialize.
	buf, err := json.Marshal(rec)
	if err != nil {
		return workEventAppendResult{}, fmt.Errorf("work: encode event: %w", err)
	}
	buf = append(buf, '\n')

	// Ensure directory exists.
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return workEventAppendResult{}, err
	}

	// Append to log.
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return workEventAppendResult{}, fmt.Errorf("work: open event log: %w", err)
	}
	if _, err := f.Write(buf); err != nil {
		_ = f.Close()
		return workEventAppendResult{}, fmt.Errorf("work: append event: %w", err)
	}
	if sync {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return workEventAppendResult{}, err
		}
	}
	if err := f.Close(); err != nil {
		return workEventAppendResult{}, err
	}

	// Update index. If write fails, the next Append or Rebuild will self-heal.
	newIdx := buildIndexFromReplay(rec.Revision, rec.ContentDigest, logSize+int64(len(buf)), 1, nil)
	if idx != nil {
		newIdx.EventCount = idx.EventCount + 1
		if idx.RequestIndex != nil {
			// Appends are serialized by the Work operation/lease. Reuse the
			// in-memory receipt map so live updates do not copy the complete
			// history on every event.
			newIdx.RequestIndex = idx.RequestIndex
		}
	}
	if rec.RequestID != "" {
		idDigest, _ := workEventIdempotentDigest(rec)
		entry := WorkRequestEntry{
			Revision: rec.Revision,
			Digest:   idDigest,
			EventID:  rec.ID,
			Type:     rec.Type,
		}
		if rec.Type == EventTaskReady || rec.Type == EventDraftUpdated {
			value := eventFromRecord(rec)
			entry.Event = &value
		}
		newIdx.RequestIndex[rec.RequestID] = entry
		newIdx.appendRequest = rec.RequestID
	}
	if err := writeWorkEventIndexAfterAppend(workDir, newIdx); err != nil {
		return workEventAppendResult{
			Revision: rec.Revision,
			Event:    eventFromRecord(rec),
			Index:    newIdx,
			Appended: true,
		}, fmt.Errorf("work: event appended at revision %d but index update failed: %w", rec.Revision, err)
	}

	return workEventAppendResult{
		Revision: rec.Revision,
		Event:    eventFromRecord(rec),
		Index:    newIdx,
		Appended: true,
	}, nil
}

func readLastWorkEventRecord(logPath string, size int64) (workEventRecord, error) {
	const (
		tailChunk = int64(64 << 10)
		maxTail   = int64(4 << 20)
	)
	if size <= 0 {
		return workEventRecord{}, errors.New("work: event log is empty")
	}
	f, err := os.Open(logPath)
	if err != nil {
		return workEventRecord{}, err
	}
	defer f.Close()

	end := size
	var tail []byte
	for end > 0 && int64(len(tail)) < maxTail {
		readSize := tailChunk
		if end < readSize {
			readSize = end
		}
		start := end - readSize
		part := make([]byte, readSize)
		n, readErr := f.ReadAt(part, start)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return workEventRecord{}, readErr
		}
		combined := make([]byte, 0, n+len(tail))
		combined = append(combined, part[:n]...)
		tail = append(combined, tail...)

		record := bytes.TrimRight(tail, "\r\n\t ")
		if split := bytes.LastIndexByte(record, '\n'); split >= 0 {
			return decodeLastWorkEventRecord(record[split+1:])
		}
		if start == 0 {
			return decodeLastWorkEventRecord(record)
		}
		end = start
	}
	return workEventRecord{}, errors.New("work: last event exceeds indexed tail verification limit")
}

func decodeLastWorkEventRecord(data []byte) (workEventRecord, error) {
	if len(data) == 0 {
		return workEventRecord{}, errors.New("work: event log is empty")
	}
	var rec workEventRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return workEventRecord{}, err
	}
	return rec, nil
}

// ── Replay ─────────────────────────────────────────────────────────────────

// ReplayWorkEventLog decodes the event log tolerantly: decoding stops at the
// first record that fails to parse, fails chain validation, or carries an
// unsupported schema/type. The state up to that point is returned; when
// NeedsRepair is true the writer may repair the tail.
//
// Future schema, unknown event types, and a live external lease result in
// ReadOnly=true. Historical writer IDs are audit metadata: after the previous
// process releases its OS lease, a new process may verify and continue the
// digest-protected revision chain.
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
	info, err := os.Stat(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			replay := &WorkEventReplay{}
			diskIndex, _ := readWorkEventIndex(workDir)
			finalizeReplayIndex(replay, diskIndex)
			return replay, nil
		}
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("work: event log path is a directory: %s", logPath)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(data)
	replay := &WorkEventReplay{
		LogSize:        int64(len(data)),
		LogFingerprint: fmt.Sprintf("%x", fingerprint[:]),
	}

	// Load index. If corrupt, ignore it — log is the source of truth.
	// The caller (AppendWorkEvent) will try to rebuild on write.
	diskIndex, idxErr := readWorkEventIndex(workDir)
	if idxErr != nil && !os.IsNotExist(idxErr) {
		diskIndex = nil
	}
	replay.Index = diskIndex

	dec := json.NewDecoder(bytes.NewReader(data))
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
		if rec.SchemaVersion > WorkEventSchemaVersionV2 {
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

		// Schema/type cross-check.
		if rec.SchemaVersion == SchemaVersionV2 && !IsV2EventType(rec.Type) {
			replay.NeedsRepair = true
			return replay, nil
		}
		if rec.SchemaVersion == SchemaVersion && IsV2EventType(rec.Type) {
			replay.NeedsRepair = true
			return replay, nil
		}

		// V2 event payload + context validation.
		if rec.SchemaVersion == SchemaVersionV2 {
			ev := eventFromRecord(rec)
			if err := ValidateV2WorkEvent(ev); err != nil {
				replay.NeedsRepair = true
				return replay, nil
			}
		}

		// WriterID is required audit evidence. It is intentionally not required
		// to match this process: the OS lease above is the concurrency boundary,
		// while the content digest below authenticates completed history across
		// normal application restarts.
		if strings.TrimSpace(rec.WriterID) == "" {
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
		if got, ok := a.RequestIndex[requestID]; !ok || !reflect.DeepEqual(got, want) {
			return false
		}
	}
	return true
}

func decodeCompactProjection(payload json.RawMessage) (*Work, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if fields == nil {
		return nil, fmt.Errorf("compact payload must be a JSON object")
	}

	if _, modern := fields["projection"]; modern {
		var envelope struct {
			Projection   json.RawMessage             `json:"projection"`
			RequestIndex map[string]WorkRequestEntry `json:"requestIndex,omitempty"`
		}
		if err := decodeStrictJSON(payload, &envelope); err != nil {
			return nil, fmt.Errorf("invalid compact envelope: %w", err)
		}
		return decodeCompactWork(envelope.Projection, "projection")
	}

	if _, legacy := fields["id"]; !legacy {
		return nil, fmt.Errorf("compact payload has neither projection nor legacy work id")
	}
	return decodeCompactWork(payload, "legacy projection")
}

func decodeCompactWork(data json.RawMessage, kind string) (*Work, error) {
	var projection Work
	if err := decodeStrictJSON(data, &projection); err != nil {
		return nil, fmt.Errorf("invalid %s: %w", kind, err)
	}
	if strings.TrimSpace(projection.ID) == "" {
		return nil, fmt.Errorf("%s work id is required", kind)
	}
	return &projection, nil
}

func decodeStrictJSON(data []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing JSON value: %w", err)
	}
	return nil
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
			projection, err = decodeCompactProjection(e.Payload)
			if err != nil {
				return replay, nil, fmt.Errorf("work: decode compact snapshot at revision %d: %w", e.Revision, err)
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
				var decodeErr error
				p, decodeErr = decodeCompactProjection(e.Payload)
				if decodeErr != nil {
					return nil, fmt.Errorf("decode compact snapshot at revision %d: %w", e.Revision, decodeErr)
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

	// Service.Get returns a transport-safe copy whose required collections are
	// normalized from nil to empty arrays. Compare the same normalized shape on
	// both sides so that representation-only nil/empty differences do not reject
	// an otherwise identical authoritative projection.
	providedJSON, err := json.Marshal(workForView(projection))
	if err != nil {
		return fmt.Errorf("work: marshal provided projection: %w", err)
	}
	replayedJSON, err := json.Marshal(workForView(replayProj))
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
		Projection:   replayProj,
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
	if current.LogFingerprint == "" || replay.LogFingerprint == "" || current.LogFingerprint != replay.LogFingerprint {
		return fmt.Errorf("work: stale replay: event log content fingerprint changed")
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

var workEventIndexCache sync.Map // absolute work dir -> *WorkEventIndex

func readWorkEventIndexHeader(workDir string) (*WorkEventIndex, error) {
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

func readWorkEventIndex(workDir string) (*WorkEventIndex, error) {
	idx, err := readWorkEventIndexHeader(workDir)
	if err != nil || idx == nil {
		return idx, err
	}
	requests, err := readWorkRequestIndex(workDir)
	switch {
	case err == nil:
		idx.RequestIndex = requests
	case os.IsNotExist(err):
		if idx.RequestIndex == nil {
			idx.RequestIndex = map[string]WorkRequestEntry{}
		}
	default:
		return nil, err
	}
	return idx, nil
}

func readCachedWorkEventIndex(workDir string, header *WorkEventIndex) (*WorkEventIndex, error) {
	key, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	key = filepath.Clean(key)
	if cached, ok := workEventIndexCache.Load(key); ok {
		idx := cached.(*WorkEventIndex)
		if workEventIndexHeadersEqual(idx, header) {
			return idx, nil
		}
	}
	idx, err := readWorkEventIndex(workDir)
	if err != nil {
		return nil, err
	}
	if !workEventIndexHeadersEqual(idx, header) {
		return nil, errors.New("work: event index header changed while loading request receipts")
	}
	workEventIndexCache.Store(key, idx)
	return idx, nil
}

func workEventIndexHeadersEqual(a, b *WorkEventIndex) bool {
	return a != nil && b != nil &&
		a.SchemaVersion == b.SchemaVersion &&
		a.LogSize == b.LogSize &&
		a.EventCount == b.EventCount &&
		a.Revision == b.Revision &&
		a.ContentDigest == b.ContentDigest
}

func readWorkRequestIndex(workDir string) (map[string]WorkRequestEntry, error) {
	path := WorkRequestIndexPath(workDir)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	requests := map[string]WorkRequestEntry{}
	dec := json.NewDecoder(f)
	for {
		var record workRequestIndexRecord
		if err := dec.Decode(&record); err != nil {
			if errors.Is(err, io.EOF) {
				return requests, nil
			}
			return nil, fmt.Errorf("work: decode request index: %w", err)
		}
		if strings.TrimSpace(record.RequestID) == "" {
			return nil, errors.New("work: request index contains an empty requestID")
		}
		requests[record.RequestID] = record.Entry
	}
}

func writeWorkRequestIndex(workDir string, requests map[string]WorkRequestEntry) error {
	path := WorkRequestIndexPath(workDir)
	if path == "" {
		return nil
	}
	keys := make([]string, 0, len(requests))
	for requestID := range requests {
		keys = append(keys, requestID)
	}
	sort.Strings(keys)
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	for _, requestID := range keys {
		if err := enc.Encode(workRequestIndexRecord{
			RequestID: requestID,
			Entry:     requests[requestID],
		}); err != nil {
			return err
		}
	}
	return fileutil.AtomicWriteFile(path, data.Bytes(), 0o644)
}

func appendWorkRequestIndex(workDir string, idx *WorkEventIndex) error {
	path := WorkRequestIndexPath(workDir)
	if path == "" || idx == nil {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return writeWorkRequestIndex(workDir, idx.RequestIndex)
		}
		return err
	}
	if idx.appendRequest == "" {
		return nil
	}
	entry, ok := idx.RequestIndex[idx.appendRequest]
	if !ok {
		return fmt.Errorf("work: appended request %q missing from event index", idx.appendRequest)
	}
	data, err := json.Marshal(workRequestIndexRecord{
		RequestID: idx.appendRequest,
		Entry:     entry,
	})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeWorkEventIndexHeader(workDir string, idx *WorkEventIndex) error {
	indexPath := WorkEventIndexPath(workDir)
	if indexPath == "" {
		return nil
	}
	idx.SchemaVersion = WorkEventSchemaVersion
	idx.UpdatedAt = time.Now().UTC()
	if idx.WriterID == "" {
		idx.WriterID = WorkWriterID()
	}
	header := *idx
	header.RequestIndex = nil
	header.appendRequest = ""
	b, err := json.MarshalIndent(&header, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(indexPath, b, 0o644)
}

func cacheWorkEventIndex(workDir string, idx *WorkEventIndex) {
	key, err := filepath.Abs(workDir)
	if err == nil && idx != nil {
		workEventIndexCache.Store(filepath.Clean(key), idx)
	}
}

func invalidateWorkEventIndexCache(workDir string) {
	key, err := filepath.Abs(workDir)
	if err == nil {
		workEventIndexCache.Delete(filepath.Clean(key))
	}
}

func writeWorkEventIndex(workDir string, idx *WorkEventIndex) error {
	if idx == nil {
		return errors.New("work: nil event index")
	}
	invalidateWorkEventIndexCache(workDir)
	if err := writeWorkRequestIndex(workDir, idx.RequestIndex); err != nil {
		return err
	}
	if err := writeWorkEventIndexHeader(workDir, idx); err != nil {
		return err
	}
	cacheWorkEventIndex(workDir, idx)
	return nil
}

func appendWorkEventIndex(workDir string, idx *WorkEventIndex) error {
	invalidateWorkEventIndexCache(workDir)
	if err := appendWorkRequestIndex(workDir, idx); err != nil {
		return err
	}
	if err := writeWorkEventIndexHeader(workDir, idx); err != nil {
		return err
	}
	cacheWorkEventIndex(workDir, idx)
	return nil
}

// writeWorkEventIndexAfterAppend is a narrow failure-injection seam for the
// only non-atomic boundary in AppendWorkEvent: log append succeeded, index
// replacement failed. Production appends one request receipt and replaces only
// the fixed-size event index header.
var writeWorkEventIndexAfterAppend = appendWorkEventIndex

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
				entry := WorkRequestEntry{
					Revision: e.Revision,
					Digest:   idDigest,
					EventID:  e.ID,
					Type:     e.Type,
				}
				if e.Type == EventTaskReady || e.Type == EventDraftUpdated {
					value := e
					entry.Event = &value
				}
				idx.RequestIndex[e.RequestID] = entry
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
