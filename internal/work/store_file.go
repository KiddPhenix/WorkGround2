package work

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

// ── Errors ─────────────────────────────────────────────────────────────────

var (
	ErrWorkNotFound            = errors.New("work: not found")
	ErrWorkArchiveExists       = errors.New("work: archive already exists — archives are immutable")
	ErrWorkTrashExists         = errors.New("work: already in trash")
	ErrWorkNotInTrash          = errors.New("work: not in trash")
	ErrWorkDigestMismatch      = errors.New("work: content digest mismatch — data may be corrupt")
	ErrWorkPathTraversal       = errors.New("work: work ID contains illegal path segments")
	ErrWorkDefinitionImmutable = errors.New("work: definition snapshot is immutable — cannot overwrite with different content")
	ErrWorkInvalidDigest       = errors.New("work: invalid digest format — must be sha256: plus 64 lowercase hex characters")
	ErrWorkNilInput            = errors.New("work: nil input")
	ErrWorkEmptyWorkDir        = errors.New("work: empty work directory")
	ErrWorkRequestIDRequired   = errors.New("work: requestID is required for idempotent operations")
	ErrWorkRequestIDConflict   = errors.New("work: requestID conflict — same requestID with different operation or object")
	ErrWorkNeedsRepair         = errors.New("work: persisted data needs repair")
	ErrWorkAlreadyExists       = errors.New("work: already exists")
)

// ErrWorkCommittedRecovery is returned by Append when the event was successfully
// appended to the event log but the derived projection could not be persisted.
// The caller can retry LoadProjection which will rebuild from the event log.
type ErrWorkCommittedRecovery struct {
	Operation   string `json:"operation"`
	WorkID      string `json:"workId"`
	RequestID   string `json:"requestId,omitempty"`
	Revision    int64  `json:"revision,omitempty"`
	Committed   bool   `json:"committed"`
	Cause       string `json:"cause"`
	Recoverable bool   `json:"recoverable"`
	cause       error
}

func (e *ErrWorkCommittedRecovery) Error() string {
	return fmt.Sprintf("work: %s committed for %s at revision %d but derived state failed: %s (recoverable)",
		e.Operation, e.WorkID, e.Revision, e.Cause)
}

// Unwrap preserves the underlying I/O error for errors.Is/errors.As callers.
func (e *ErrWorkCommittedRecovery) Unwrap() error { return e.cause }

func committedRecovery(operation, workID, requestID string, revision int64, cause error) error {
	return &ErrWorkCommittedRecovery{
		Operation:   operation,
		WorkID:      workID,
		RequestID:   requestID,
		Revision:    revision,
		Committed:   true,
		Cause:       cause.Error(),
		Recoverable: true,
		cause:       cause,
	}
}

// ErrWorkCleanupRecovery reports a failed lifecycle operation together with
// the durable cleanup state needed to retry it safely.
type ErrWorkCleanupRecovery struct {
	Operation       string `json:"operation"`
	WorkID          string `json:"workId"`
	RequestID       string `json:"requestId"`
	Stage           string `json:"stage"`
	CleanupPath     string `json:"cleanupPath,omitempty"`
	Committed       bool   `json:"committed"`
	MarkerPersisted bool   `json:"markerPersisted"`
	Recoverable     bool   `json:"recoverable"`
	Cause           string `json:"cause"`
	cause           error
}

func (e *ErrWorkCleanupRecovery) Error() string {
	state := "pending"
	if e.Committed {
		state = "committed"
	}
	return fmt.Sprintf("work: %s %s for %s request %q at stage %q failed: %s (recoverable)",
		e.Operation, state, e.WorkID, e.RequestID, e.Stage, e.Cause)
}

// Unwrap preserves every primary, marker and cleanup failure joined below.
func (e *ErrWorkCleanupRecovery) Unwrap() error { return e.cause }

func cleanupRecovery(cp *cleanupPending, path string, committed, persisted bool, cause error) error {
	return &ErrWorkCleanupRecovery{
		Operation:       cp.Operation,
		WorkID:          cp.WorkID,
		RequestID:       cp.RequestID,
		Stage:           cp.Stage,
		CleanupPath:     path,
		Committed:       committed,
		MarkerPersisted: persisted,
		Recoverable:     true,
		Cause:           cause.Error(),
		cause:           cause,
	}
}

// ── Digest validation ──────────────────────────────────────────────────────

var digestRegexp = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

var (
	writeDerivedFile  = fileutil.AtomicWriteFile
	removeWorkDir     = os.RemoveAll
	renameWorkDir     = os.Rename
	releaseStoreLease = ReleaseWorkLease
	storeLocks        sync.Map // absolute works root -> *sync.RWMutex
	workOpLocks       sync.Map // absolute work path -> *sync.Mutex
)

func validateDigest(d string) error {
	if !digestRegexp.MatchString(d) {
		return fmt.Errorf("%w: %q", ErrWorkInvalidDigest, d)
	}
	return nil
}

// ── FileWorkStore ──────────────────────────────────────────────────────────

// FileWorkStore implements WorkStore on the local filesystem. It stores every
// Work under a per-project data directory (typically config.ProjectWorkDir(root))
// and never writes inside the git workspace.
type FileWorkStore struct {
	workDir        string        // <project-data-dir>/works (absolute, non-empty)
	trashDir       string        // <project-data-dir>/.trash/works
	trashRetention time.Duration // 0 means GC is disabled
	indexMu        *sync.RWMutex // shared by every store instance for this root
}

// NewFileWorkStore creates a FileWorkStore rooted at workDir. workDir is
// typically the result of config.ProjectWorkDir(root).
//
// trashRetention semantics:
//
//	0  → GC is disabled entirely.
//	<0 → use the default 30-day retention.
//	>0 → use the given duration.
//
// Returns an error if workDir is empty, or cannot be resolved to an absolute path.
func NewFileWorkStore(workDir string, trashRetention time.Duration) (*FileWorkStore, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil, fmt.Errorf("%w: workDir must not be empty", ErrWorkEmptyWorkDir)
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil, fmt.Errorf("work: cannot resolve workDir %q: %w", workDir, err)
	}
	// Prevent degenerate paths like "." or the current working directory itself
	// being used as the works root.
	if abs == "." || filepath.Clean(abs) == filepath.Clean(mustGetwd()) {
		return nil, fmt.Errorf("%w: workDir must be an explicit project data path, not the current working directory", ErrWorkEmptyWorkDir)
	}
	if filepath.Dir(abs) == abs {
		return nil, fmt.Errorf("%w: workDir must not be a filesystem root", ErrWorkEmptyWorkDir)
	}
	if trashRetention < 0 {
		trashRetention = 30 * 24 * time.Hour
	}
	return &FileWorkStore{
		workDir:        abs,
		trashDir:       filepath.Join(abs, "..", ".trash", "works"),
		trashRetention: trashRetention,
		indexMu:        sharedStoreLock(abs),
	}, nil
}

func sharedStoreLock(root string) *sync.RWMutex {
	lock, _ := storeLocks.LoadOrStore(filepath.Clean(root), &sync.RWMutex{})
	return lock.(*sync.RWMutex)
}

func (s *FileWorkStore) lockWorkOp(workID string) func() {
	key := filepath.Join(s.workDir, workID)
	lock, _ := workOpLocks.LoadOrStore(key, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *FileWorkStore) withWorkOp(workID string, fn func() error) error {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return err
	}
	err = fn()
	doneErr := done()
	return errors.Join(err, doneErr)
}

func (s *FileWorkStore) withCleanupOp(workID, requestID, operation string, fn func() error) error {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return err
	}
	opErr := fn()
	doneErr := done()
	if doneErr == nil {
		return opErr
	}
	if opErr != nil {
		return errors.Join(opErr, doneErr)
	}
	cp, stateErr := s.loadCleanupPendingAny(workID)
	if stateErr == nil && cp != nil && cp.RequestID == requestID && cp.Operation == operation {
		return cleanupRecovery(cp, cp.CleanupPath, true, true, doneErr)
	}
	cp = &cleanupPending{
		RequestID: requestID,
		Operation: operation,
		WorkID:    workID,
		Stage:     "lease_release_failed",
		Error:     doneErr.Error(),
	}
	return cleanupRecovery(cp, "", true, false, errors.Join(doneErr, stateErr))
}

func (s *FileWorkStore) beginWorkOp(workID string) (func() error, error) {
	if err := validateWorkID(workID); err != nil {
		return nil, err
	}
	unlock := s.lockWorkOp(workID)
	lockDir := filepath.Join(s.workDir, ".locks", workID)
	if err := AcquireWorkLease(lockDir); err != nil {
		unlock()
		return nil, fmt.Errorf("work: acquire lifecycle lock for %s: %w", workID, err)
	}
	releaseOp, err := requireLease(lockDir)
	if err != nil {
		err = errors.Join(err, releaseStoreLease(lockDir))
		unlock()
		return nil, err
	}
	return func() error {
		releaseOp()
		err := releaseStoreLease(lockDir)
		unlock()
		if err != nil {
			return fmt.Errorf("work: release lifecycle lease for %s: %w", workID, err)
		}
		return nil
	}, nil
}

func (s *FileWorkStore) beginIndexOp() (func() error, error) {
	s.indexMu.Lock()
	lockDir := filepath.Join(s.workDir, ".locks", "index")
	if err := AcquireWorkLease(lockDir); err != nil {
		s.indexMu.Unlock()
		return nil, fmt.Errorf("work: acquire global index lock: %w", err)
	}
	releaseOp, err := requireLease(lockDir)
	if err != nil {
		err = errors.Join(err, releaseStoreLease(lockDir))
		s.indexMu.Unlock()
		return nil, err
	}
	return func() error {
		releaseOp()
		err := releaseStoreLease(lockDir)
		s.indexMu.Unlock()
		if err != nil {
			return fmt.Errorf("work: release global index lease: %w", err)
		}
		return nil
	}, nil
}

func mustGetwd() string {
	d, _ := os.Getwd()
	return d
}

// WorkDir returns the configured works directory (always absolute).
func (s *FileWorkStore) WorkDir() string { return s.workDir }

// TrashDir returns the configured trash directory.
func (s *FileWorkStore) TrashDir() string { return s.trashDir }

// ── Path helpers ───────────────────────────────────────────────────────────

func (s *FileWorkStore) workPath(workID string) (string, error) {
	if err := validateWorkID(workID); err != nil {
		return "", err
	}
	return filepath.Join(s.workDir, workID), nil
}

func (s *FileWorkStore) trashPath(workID string) (string, error) {
	if err := validateWorkID(workID); err != nil {
		return "", err
	}
	return filepath.Join(s.trashDir, workID), nil
}

func (s *FileWorkStore) manifestPath(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "manifest.json"), nil
}

func (s *FileWorkStore) definitionDir(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "definitions"), nil
}

func (s *FileWorkStore) definitionPath(workID, digest string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	dir, err := s.definitionDir(workID)
	if err != nil {
		return "", err
	}
	hash := digest[len(digestPrefix):]
	return filepath.Join(dir, hash+".json"), nil
}

func (s *FileWorkStore) projectionPath(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "projection.json"), nil
}

func (s *FileWorkStore) archivePath(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "archive.json"), nil
}

func (s *FileWorkStore) blobDir(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "blobs"), nil
}

func (s *FileWorkStore) blobPath(workID, digest string) (string, error) {
	if err := validateDigest(digest); err != nil {
		return "", err
	}
	dir, err := s.blobDir(workID)
	if err != nil {
		return "", err
	}
	hash := digest[len(digestPrefix):]
	return filepath.Join(dir, hash), nil
}

func (s *FileWorkStore) cleanupPendingPath(workID string) (string, error) {
	p, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(p, "cleanup-pending.json"), nil
}

func (s *FileWorkStore) indexPath() string {
	return filepath.Join(s.workDir, "index.json")
}

// ── ID validation ──────────────────────────────────────────────────────────

func validateWorkID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return fmt.Errorf("%w: empty work ID", ErrWorkPathTraversal)
	}
	if trimmed != id || id == "." || strings.HasPrefix(id, ".") || strings.HasSuffix(id, ".") || strings.EqualFold(id, "blueprints") {
		return fmt.Errorf("%w: %q is not a canonical work ID", ErrWorkPathTraversal, id)
	}
	if strings.Contains(id, string(os.PathSeparator)) ||
		strings.Contains(id, "/") ||
		strings.Contains(id, "\\") ||
		strings.Contains(id, "..") ||
		strings.ContainsAny(id, `<>:"|?*`) ||
		strings.IndexFunc(id, func(r rune) bool { return r < 0x20 }) >= 0 {
		return fmt.Errorf("%w: %q", ErrWorkPathTraversal, id)
	}
	if filepath.IsAbs(id) {
		return fmt.Errorf("%w: %q looks like an absolute path", ErrWorkPathTraversal, id)
	}
	if isWindowsReservedName(id) {
		return fmt.Errorf("%w: %q is a reserved name", ErrWorkPathTraversal, id)
	}
	return nil
}

func isWindowsReservedName(name string) bool {
	upper := strings.ToUpper(name)
	reserved := map[string]bool{
		"CON": true, "PRN": true, "AUX": true, "NUL": true,
		"COM1": true, "COM2": true, "COM3": true, "COM4": true,
		"COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
		"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true,
		"LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
	}
	base := upper
	if dot := strings.IndexByte(upper, '.'); dot >= 0 {
		base = upper[:dot]
	}
	return reserved[base]
}

// ── WorkStore: LoadProjection ──────────────────────────────────────────────

func (s *FileWorkStore) LoadProjection(workID string) (*Work, error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return nil, err
	}
	wp, err := s.workPath(workID)
	if err != nil {
		return nil, errors.Join(err, done())
	}
	projection, loadErr := s.loadProjection(wp, workID)
	return projection, errors.Join(loadErr, done())
}

// LoadState returns a projection and its authoritative event-log state while
// holding the per-Work lifecycle lock. Service uses it to make requestID
// idempotency and expectedRevision checks survive restarts and concurrent
// Service instances.
func (s *FileWorkStore) LoadState(workID, requestID string) (value *Work, state WorkEventState, retErr error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return nil, state, err
	}
	defer func() { retErr = errors.Join(retErr, done()) }()

	wp, err := s.workPath(workID)
	if err != nil {
		return nil, state, err
	}
	value, err = s.loadProjection(wp, workID)
	if err != nil {
		return value, state, err
	}
	replay, err := ReplayWorkEventLog(wp)
	if err != nil {
		return value, state, err
	}
	state, err = eventStateFromReplay(workID, requestID, replay)
	return value, state, err
}

// LoadTrashState reads authoritative lifecycle state from Trash without moving
// or repairing the directory. Service uses it to reject late requests before
// they repeat an already-superseded filesystem side effect.
func (s *FileWorkStore) LoadTrashState(workID, requestID string) (value *Work, state WorkEventState, retErr error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return nil, state, err
	}
	defer func() { retErr = errors.Join(retErr, done()) }()

	tp, err := s.trashPath(workID)
	if err != nil {
		return nil, state, err
	}
	replay, value, err := ReplayWithReducer(tp, DefaultReducer())
	if err != nil {
		return value, state, err
	}
	if value == nil {
		return nil, state, fmt.Errorf("%w: no trashed events for work %s", ErrWorkNotInTrash, workID)
	}
	state, err = eventStateFromReplay(workID, requestID, replay)
	return value, state, err
}

func eventStateFromReplay(workID, requestID string, replay *WorkEventReplay) (WorkEventState, error) {
	var state WorkEventState
	if replay == nil {
		return state, fmt.Errorf("%w: event replay for %s is unavailable", ErrWorkNeedsRepair, workID)
	}
	if replay.ReadOnly {
		return state, fmt.Errorf("work: event log for %s is read-only: %s", workID, replay.ReadOnlyReason)
	}
	if replay.NeedsRepair {
		return state, fmt.Errorf("%w: event log for %s requires repair", ErrWorkNeedsRepair, workID)
	}
	if replay.Index == nil {
		return state, fmt.Errorf("%w: event index for %s is unavailable", ErrWorkNeedsRepair, workID)
	}
	state.Revision = replay.Index.Revision
	for _, event := range replay.Events {
		switch event.Type {
		case EventWorkArchived, EventWorkRestored, EventWorkDeleted:
			state.LifecycleRevision = event.Revision
		}
		if requestID != "" && event.RequestID == requestID {
			state.RequestType = event.Type
		}
	}
	if requestID != "" {
		entry, ok := replay.Index.RequestIndex[requestID]
		state.RequestFound = ok
		if ok {
			state.RequestRevision = entry.Revision
		}
	}
	return state, nil
}

func (s *FileWorkStore) loadProjection(workDir, workID string) (*Work, error) {
	projPath := filepath.Join(workDir, "projection.json")

	data, err := os.ReadFile(projPath)
	if err == nil {
		var w Work
		if err := json.Unmarshal(data, &w); err != nil {
			// Corrupt projection → rebuild from events.
			return s.rebuildProjection(workDir, workID, fmt.Sprintf("corrupt projection: %v", err))
		}
		if w.ID != workID {
			return s.rebuildProjection(workDir, workID, fmt.Sprintf("projection workID mismatch: %s contains %q", workID, w.ID))
		}
		if err := CheckSchemaVersion("Work", w.SchemaVersion); err != nil {
			return nil, err
		}
		replay, authoritative, replayErr := ReplayWithReducer(workDir, DefaultReducer())
		if replayErr != nil {
			return authoritative, fmt.Errorf("%w: validate projection for %s against event log: %v", ErrWorkNeedsRepair, workID, replayErr)
		}
		if replay.ReadOnly || replay.NeedsRepair {
			return authoritative, fmt.Errorf("%w: event log for %s is not safely replayable", ErrWorkNeedsRepair, workID)
		}
		if authoritative == nil {
			return nil, fmt.Errorf("%w: no events for work %s", ErrWorkNotFound, workID)
		}
		projectionMatches, compareErr := projectionsEqual(&w, authoritative)
		if compareErr != nil {
			return authoritative, fmt.Errorf("%w: compare projection for %s with event replay: %v", ErrWorkNeedsRepair, workID, compareErr)
		}
		if err := s.repairEventIndex(workDir, replay); err != nil {
			return authoritative, fmt.Errorf("%w: projection replayed for %s but event index repair failed: %v", ErrWorkNeedsRepair, workID, err)
		}
		manifest, manifestErr := loadManifestAt(filepath.Join(workDir, "manifest.json"))
		manifestStale := manifestErr != nil || manifest.ID != workID || manifest.Revision != replay.Index.Revision
		if !manifestStale {
			manifestStale = CheckSchemaVersion("WorkManifest", manifest.SchemaVersion) != nil
		}
		// Projection is written before manifest. A revision mismatch therefore
		// proves that a prior derived-state update stopped part-way through.
		if manifestStale || !projectionMatches {
			if err := s.persistProjection(workDir, workID, authoritative, replay.Index.Revision); err != nil {
				return authoritative, fmt.Errorf("%w: stale projection rebuilt for %s but derived files could not be repaired: %v", ErrWorkNeedsRepair, workID, err)
			}
			return authoritative, nil
		}
		if err := s.persistProjection(workDir, workID, &w, replay.Index.Revision); err != nil {
			return &w, fmt.Errorf("%w: projection loaded for %s but derived state repair failed: %v", ErrWorkNeedsRepair, workID, err)
		}
		return &w, nil
	}

	if !os.IsNotExist(err) {
		// Read error → don't hide it, but still try to rebuild.
		proj, rebuildErr := s.rebuildProjection(workDir, workID, fmt.Sprintf("read projection: %v", err))
		if rebuildErr != nil {
			return nil, fmt.Errorf("work: read projection for %s: %w", workID, err)
		}
		return proj, nil
	}

	return s.rebuildProjection(workDir, workID, "no projection snapshot")
}

func projectionsEqual(a, b *Work) (bool, error) {
	left, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	right, err := json.Marshal(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(left, right), nil
}

func (s *FileWorkStore) repairEventIndex(workDir string, replay *WorkEventReplay) error {
	if replay == nil || !replay.IndexNeedsRebuild {
		return nil
	}
	held, external, writer := probeWorkLease(workDir)
	if external {
		return fmt.Errorf("%w: event index repair blocked by writer %q", ErrWorkLeaseHeld, writer)
	}
	acquired := false
	if !held {
		if err := AcquireWorkLease(workDir); err != nil {
			return err
		}
		acquired = true
	}
	_, rebuildErr := RebuildWorkEventIndex(workDir)
	if acquired {
		rebuildErr = errors.Join(rebuildErr, releaseStoreLease(workDir))
	}
	return rebuildErr
}

func (s *FileWorkStore) rebuildProjection(workDir, workID, reason string) (*Work, error) {
	replay, proj, err := ReplayWithReducer(workDir, DefaultReducer())
	if err != nil {
		return nil, fmt.Errorf("work: rebuild projection for %s (%s): %w", workID, reason, err)
	}
	if proj == nil {
		return nil, fmt.Errorf("%w: no events for work %s", ErrWorkNotFound, workID)
	}
	if replay.ReadOnly || replay.NeedsRepair {
		return proj, fmt.Errorf("%w: cannot rebuild projection for %s from an unsafe event log", ErrWorkNeedsRepair, workID)
	}
	if err := s.repairEventIndex(workDir, replay); err != nil {
		return proj, fmt.Errorf("%w: event index repair failed for %s: %v", ErrWorkNeedsRepair, workID, err)
	}
	revision := int64(0)
	if replay != nil && replay.Index != nil {
		revision = replay.Index.Revision
	}
	if err := s.persistProjection(workDir, workID, proj, revision); err != nil {
		return proj, fmt.Errorf("%w: projection rebuilt from events for %s (%s) but derived files could not be repaired: %v", ErrWorkNeedsRepair, workID, reason, err)
	}
	return proj, nil
}

// ── WorkStore: LoadArchive ────────────────────────────────────────────────

func (s *FileWorkStore) LoadArchive(workID string) (*WorkRecord, error) {
	archivePath, err := s.archivePath(workID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: archive for %s", ErrWorkNotFound, workID)
		}
		return nil, fmt.Errorf("work: read archive for %s: %w", workID, err)
	}
	var record WorkRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("%w: corrupt archive for %s: %v", ErrWorkNeedsRepair, workID, err)
	}
	if err := CheckSchemaVersion("WorkRecord", record.ArchiveSchemaVersion); err != nil {
		return nil, fmt.Errorf("work: %w", err)
	}
	if record.WorkID != workID || record.Snapshot.ID != workID {
		return nil, fmt.Errorf("%w: archive identity mismatch for %s (record=%q snapshot=%q)", ErrWorkNeedsRepair, workID, record.WorkID, record.Snapshot.ID)
	}
	if err := CheckSchemaVersion("Work", record.Snapshot.SchemaVersion); err != nil {
		return nil, err
	}
	return &record, nil
}

// ── WorkStore: Append ──────────────────────────────────────────────────────

func (s *FileWorkStore) Append(workID string, event WorkEvent) (revision int64, retErr error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := done(); closeErr != nil {
			if retErr == nil && revision > 0 {
				retErr = committedRecovery("append", workID, event.RequestID, revision, closeErr)
			} else {
				retErr = errors.Join(retErr, closeErr)
			}
		}
	}()

	wp, err := s.workPath(workID)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		return 0, fmt.Errorf("work: create work dir %s: %w", workID, err)
	}
	return s.appendLocked(workID, wp, event)
}

// CommitEvent serializes a Service event under both the lifecycle lock and the
// event-log writer lease. Append intentionally retains its lower-level contract
// that callers already hold the writer lease.
func (s *FileWorkStore) CommitEvent(workID string, event WorkEvent) (revision int64, retErr error) {
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return 0, err
	}
	defer func() {
		if closeErr := done(); closeErr != nil {
			if retErr == nil && revision > 0 {
				retErr = committedRecovery("commit-event", workID, event.RequestID, revision, closeErr)
			} else {
				retErr = errors.Join(retErr, closeErr)
			}
		}
	}()

	wp, err := s.workPath(workID)
	if err != nil {
		return 0, err
	}
	if !s.isDirWithData(wp) {
		return 0, fmt.Errorf("%w: %s", ErrWorkNotFound, workID)
	}
	if held, _, writer := probeWorkLease(wp); held {
		return 0, fmt.Errorf("%w: cannot commit %s while writer %q is active", ErrWorkLeaseHeld, workID, writer)
	}
	if err := AcquireWorkLease(wp); err != nil {
		return 0, fmt.Errorf("work: acquire event writer lease for %s: %w", workID, err)
	}
	defer func() {
		if releaseErr := releaseStoreLease(wp); releaseErr != nil {
			if retErr == nil && revision > 0 {
				retErr = committedRecovery("commit-event", workID, event.RequestID, revision, releaseErr)
			} else {
				retErr = errors.Join(retErr, releaseErr)
			}
		}
	}()
	return s.appendLocked(workID, wp, event)
}

func (s *FileWorkStore) appendLocked(workID, wp string, event WorkEvent) (int64, error) {
	rev, err := AppendWorkEvent(wp, event, true)
	if err != nil {
		if rev > 0 {
			return rev, committedRecovery("append", workID, event.RequestID, rev, err)
		}
		return 0, err
	}

	// Event is committed. Now rebuild the derived projection.
	// If this fails, return a CommittedRecovery error so the caller knows
	// the event IS on disk but the projection needs rebuilding.
	_, proj, replayErr := ReplayWithReducer(wp, DefaultReducer())
	if replayErr != nil {
		return rev, committedRecovery("append", workID, event.RequestID, rev, fmt.Errorf("replay after append: %w", replayErr))
	}
	if proj == nil {
		return rev, committedRecovery("append", workID, event.RequestID, rev, errors.New("replay produced nil projection"))
	}
	if err := s.persistProjection(wp, workID, proj, rev); err != nil {
		return rev, committedRecovery("append", workID, event.RequestID, rev, err)
	}

	return rev, nil
}

func (s *FileWorkStore) persistProjection(workDir, workID string, value *Work, revision int64) error {
	if value == nil {
		return ErrWorkNilInput
	}
	if value.ID != workID {
		return fmt.Errorf("projection workID mismatch: expected %q, got %q", workID, value.ID)
	}
	if err := CheckSchemaVersion("Work", value.SchemaVersion); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projection: %w", err)
	}
	data = append(data, '\n')
	if err := writeDerivedFile(filepath.Join(workDir, "projection.json"), data, 0o644); err != nil {
		return fmt.Errorf("write projection: %w", err)
	}
	manifest := manifestFromWork(value, revision)
	// Preserve metadata that cannot be reconstructed from the projection.
	// A missing/corrupt manifest is derived-state damage and is repaired below;
	// a healthy manifest keeps the original create idempotency key.
	if currentData, readErr := os.ReadFile(filepath.Join(workDir, "manifest.json")); readErr == nil {
		var current workManifest
		if json.Unmarshal(currentData, &current) == nil && current.ID == workID {
			manifest.CreateRequestID = current.CreateRequestID
			manifest.CreateDigest = current.CreateDigest
			manifest.DeletedAt = current.DeletedAt
		}
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}
	manifestData = append(manifestData, '\n')
	if err := writeDerivedFile(filepath.Join(workDir, "manifest.json"), manifestData, 0o644); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}
	active, pathErr := s.workPath(workID)
	if pathErr == nil && filepath.Clean(active) == filepath.Clean(workDir) {
		done, lockErr := s.beginIndexOp()
		if lockErr != nil {
			return lockErr
		}
		err = s.upsertIndexLocked(workID)
		err = errors.Join(err, done())
		if err != nil {
			return fmt.Errorf("write global index: %w", err)
		}
	}
	return nil
}

// ── WorkStore: WriteProjection ─────────────────────────────────────────────

func (s *FileWorkStore) WriteProjection(workID string, work *Work, revision int64) error {
	return s.withWorkOp(workID, func() error {
		return s.writeProjectionLocked(workID, work, revision)
	})
}

func (s *FileWorkStore) writeProjectionLocked(workID string, work *Work, revision int64) error {
	if work == nil {
		return fmt.Errorf("%w: projection for %s", ErrWorkNilInput, workID)
	}
	if err := CheckSchemaVersion("Work", work.SchemaVersion); err != nil {
		return fmt.Errorf("work: %w", err)
	}
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		return fmt.Errorf("work: create work dir %s: %w", workID, err)
	}
	if work.ID != workID {
		return fmt.Errorf("work: projection workID mismatch: expected %q, got %q", workID, work.ID)
	}

	return s.persistProjection(wp, workID, work, revision)
}

// ── WorkStore: WriteArchive ────────────────────────────────────────────────

func (s *FileWorkStore) WriteArchive(workID string, record *WorkRecord) error {
	return s.withWorkOp(workID, func() error {
		return s.writeArchiveLocked(workID, record)
	})
}

func (s *FileWorkStore) writeArchiveLocked(workID string, record *WorkRecord) error {
	if record == nil {
		return fmt.Errorf("%w: archive for %s", ErrWorkNilInput, workID)
	}
	if err := CheckSchemaVersion("WorkRecord", record.ArchiveSchemaVersion); err != nil {
		return fmt.Errorf("work: %w", err)
	}
	if record.WorkID != workID {
		return fmt.Errorf("work: archive workID mismatch: expected %q, got %q", workID, record.WorkID)
	}
	if record.Snapshot.ID != workID {
		return fmt.Errorf("work: archive snapshot ID mismatch: expected %q, got %q", workID, record.Snapshot.ID)
	}
	if err := CheckSchemaVersion("Work", record.Snapshot.SchemaVersion); err != nil {
		return err
	}
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(wp, 0o755); err != nil {
		return fmt.Errorf("work: create work dir %s: %w", workID, err)
	}

	archivePath := filepath.Join(wp, "archive.json")

	existing, existErr := os.ReadFile(archivePath)
	if existErr == nil {
		// Archive exists — compare normalized content.
		newData, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			return fmt.Errorf("work: marshal archive for comparison: %w", marshalErr)
		}
		var existingRec WorkRecord
		if json.Unmarshal(existing, &existingRec) == nil {
			existingData, _ := json.Marshal(existingRec)
			if sha256.Sum256(existingData) == sha256.Sum256(newData) {
				return nil // idempotent — identical content
			}
		} else {
			return fmt.Errorf("%w: %w: corrupt archive for %s", ErrWorkArchiveExists, ErrWorkNeedsRepair, workID)
		}
		return fmt.Errorf("%w: %s", ErrWorkArchiveExists, workID)
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing archive for %s: %w", workID, existErr)
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal archive for %s: %w", workID, err)
	}
	data = append(data, '\n')
	return writeDerivedFile(archivePath, data, 0o644)
}

// ── WorkStore: List ────────────────────────────────────────────────────────

func (s *FileWorkStore) List(filter WorkFilter) ([]WorkSummary, error) {
	done, lockErr := s.beginIndexOp()
	if lockErr != nil {
		return nil, lockErr
	}
	idx, err := s.loadIndexLocked()
	err = errors.Join(err, done())
	if err != nil {
		return nil, err
	}

	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	var results []WorkSummary
	for _, entry := range idx.Works {
		if filter.Matches(&entry) {
			results = append(results, entry)
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if filter.Cursor != "" {
		start := -1
		for i, r := range results {
			if r.ID == filter.Cursor {
				start = i + 1
				break
			}
		}
		if start < 0 {
			return nil, nil
		}
		results = results[start:]
	}

	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (f WorkFilter) Matches(s *WorkSummary) bool {
	if f.State != nil && s.State != *f.State {
		return false
	}
	if f.ArchiveState != nil && s.ArchiveState != *f.ArchiveState {
		return false
	}
	if f.Blueprint != "" && s.BlueprintRef.ID != f.Blueprint {
		return false
	}
	if f.Search != "" {
		search := strings.ToLower(f.Search)
		if !strings.Contains(strings.ToLower(s.Name), search) &&
			!strings.Contains(strings.ToLower(s.ID), search) {
			return false
		}
	}
	return true
}

// ── WorkStore: MoveToTrash ─────────────────────────────────────────────────

func (s *FileWorkStore) MoveToTrash(workID, requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("%w: MoveToTrash", ErrWorkRequestIDRequired)
	}
	return s.withCleanupOp(workID, requestID, "trash", func() error {
		return s.moveToTrashLocked(workID, requestID)
	})
}

func (s *FileWorkStore) moveToTrashLocked(workID, requestID string) error {
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	tp, err := s.trashPath(workID)
	if err != nil {
		return err
	}
	sourceExists := s.isDirWithData(wp)
	targetExists := s.isDirWithData(tp)
	if sourceExists {
		if held, _, writer := probeWorkLease(wp); held {
			return fmt.Errorf("%w: cannot trash %s while writer %q is active", ErrWorkLeaseHeld, workID, writer)
		}
	}
	cp, err := s.loadCleanupPendingAny(workID)
	if err != nil {
		return err
	}
	if cp != nil && cp.Operation == "restore" && cp.Stage == "done" && sourceExists && !targetExists {
		if err := s.clearCleanupPending(workID); err != nil {
			return err
		}
		cp = nil
	}
	if cp != nil && cp.Operation == "trash" && cp.Stage == "done" && targetExists && !sourceExists {
		return nil
	}
	if cp != nil && (cp.RequestID != requestID || cp.Operation != "trash") {
		return fmt.Errorf("%w: %s has %s request %q", ErrWorkRequestIDConflict, workID, cp.Operation, cp.RequestID)
	}
	if !sourceExists && !targetExists {
		return fmt.Errorf("%w: %s", ErrWorkNotFound, workID)
	}
	if cp != nil {
		markerDir := wp
		if !sourceExists {
			markerDir = tp
		}
		if err := s.resumeMoveCleanup(markerDir, cp, targetExists); err != nil {
			return err
		}
	}
	if cp == nil {
		now := time.Now().UTC()
		cp = &cleanupPending{RequestID: requestID, Operation: "trash", WorkID: workID, Stage: "moving", TrashedAt: &now, StartedAt: now}
		if !sourceExists {
			return fmt.Errorf("%w: trash destination exists without a recoverable intent for %s", ErrWorkNeedsRepair, workID)
		}
		if err := s.writeCleanupState(wp, cp, false); err != nil {
			return err
		}
	} else {
		cp.Retries++
	}
	if !targetExists {
		if err := os.MkdirAll(s.trashDir, 0o755); err != nil {
			return fmt.Errorf("work: create trash dir: %w", err)
		}
		cp.Stage = "moving"
		cp.Error = ""
		if err := s.writeCleanupState(wp, cp, false); err != nil {
			return err
		}
		if err := s.atomicMoveWorkDir(wp, tp, cp); err != nil {
			if cp.CleanupPath == "" {
				cp.Stage = "move_failed"
			}
			return s.failCleanup(wp, cp, false, fmt.Errorf("work: move %s to trash: %w", workID, err))
		}
		targetExists = true
		sourceExists = s.isDirWithData(wp)
	}
	if sourceExists {
		cp.Stage = "removing_source"
		cp.CleanupPath = wp
		if err := s.writeCleanupState(tp, cp, true); err != nil {
			return err
		}
		if err := removeWorkDir(wp); err != nil {
			return s.failCleanup(tp, cp, true, fmt.Errorf("work: remove source after trash copy: %w", err))
		}
		if _, err := os.Stat(wp); !os.IsNotExist(err) {
			return s.failCleanup(tp, cp, true, fmt.Errorf("work: source still exists after trash move: %s", wp))
		}
	}
	cp.CleanupPath = ""
	if cp.TrashedAt == nil {
		now := time.Now().UTC()
		cp.TrashedAt = &now
	}
	cp.Stage = "updating_manifest"
	if err := s.writeCleanupState(tp, cp, true); err != nil {
		return err
	}
	if err := s.updateManifestDeletedAt(tp, workID, *cp.TrashedAt); err != nil {
		return s.failCleanup(tp, cp, true, fmt.Errorf("work: mark %s deleted: %w", workID, err))
	}
	cp.Stage = "updating_index"
	if err := s.writeCleanupState(tp, cp, true); err != nil {
		return err
	}
	done, lockErr := s.beginIndexOp()
	if lockErr != nil {
		return s.failCleanup(tp, cp, true, fmt.Errorf("work: lock index while trashing %s: %w", workID, lockErr))
	}
	err = s.removeFromIndexLocked(workID)
	err = errors.Join(err, done())
	if err != nil {
		return s.failCleanup(tp, cp, true, fmt.Errorf("work: remove %s from index: %w", workID, err))
	}
	cp.Stage = "done"
	cp.Error = ""
	cp.CompletedAt = ptrTime(time.Now().UTC())
	return s.writeCleanupState(tp, cp, true)
}

// ── WorkStore: RestoreFromTrash ────────────────────────────────────────────

func (s *FileWorkStore) RestoreFromTrash(workID, requestID string) error {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return fmt.Errorf("%w: RestoreFromTrash", ErrWorkRequestIDRequired)
	}

	return s.withCleanupOp(workID, requestID, "restore", func() error {
		return s.restoreFromTrashLocked(workID, requestID)
	})
}

func (s *FileWorkStore) restoreFromTrashLocked(workID, requestID string) error {
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	tp, err := s.trashPath(workID)
	if err != nil {
		return err
	}
	targetExists := s.isDirWithData(wp)
	sourceExists := s.isDirWithData(tp)
	cp, err := s.loadCleanupPendingAny(workID)
	if err != nil {
		return err
	}
	if cp != nil && cp.Operation == "restore" && cp.Stage == "done" && targetExists && !sourceExists {
		return nil
	}
	if cp != nil && cp.Operation == "trash" && cp.Stage == "done" && sourceExists && !targetExists {
		if err := s.clearCleanupPending(workID); err != nil {
			return err
		}
		cp = nil
	}
	if cp != nil && (cp.RequestID != requestID || cp.Operation != "restore") {
		return fmt.Errorf("%w: %s has %s request %q", ErrWorkRequestIDConflict, workID, cp.Operation, cp.RequestID)
	}
	if targetExists && !sourceExists {
		return nil
	}
	if !targetExists && !sourceExists {
		return fmt.Errorf("%w: %s", ErrWorkNotInTrash, workID)
	}
	if cp != nil {
		markerDir := tp
		if !sourceExists {
			markerDir = wp
		}
		if err := s.resumeMoveCleanup(markerDir, cp, targetExists); err != nil {
			return err
		}
	}
	if cp == nil {
		now := time.Now().UTC()
		cp = &cleanupPending{RequestID: requestID, Operation: "restore", WorkID: workID, Stage: "moving", StartedAt: now}
		if !sourceExists {
			return fmt.Errorf("%w: active and trash copies exist without restore intent for %s", ErrWorkNeedsRepair, workID)
		}
		if err := s.writeCleanupState(tp, cp, false); err != nil {
			return err
		}
	} else {
		cp.Retries++
	}
	if !targetExists {
		if err := os.MkdirAll(s.workDir, 0o755); err != nil {
			return err
		}
		cp.Stage = "moving"
		cp.Error = ""
		if err := s.writeCleanupState(tp, cp, false); err != nil {
			return err
		}
		if err := s.atomicMoveWorkDir(tp, wp, cp); err != nil {
			if cp.CleanupPath == "" {
				cp.Stage = "move_failed"
			}
			return s.failCleanup(tp, cp, false, fmt.Errorf("work: restore %s: %w", workID, err))
		}
		targetExists = true
		sourceExists = s.isDirWithData(tp)
	}
	if sourceExists {
		cp.Stage = "removing_source"
		cp.CleanupPath = tp
		if err := s.writeCleanupState(wp, cp, true); err != nil {
			return err
		}
		if err := removeWorkDir(tp); err != nil {
			return s.failCleanup(wp, cp, true, fmt.Errorf("work: remove trash source after restore: %w", err))
		}
		if _, err := os.Stat(tp); !os.IsNotExist(err) {
			return s.failCleanup(wp, cp, true, fmt.Errorf("work: trash source still exists after restore: %s", tp))
		}
	}
	cp.CleanupPath = ""
	cp.Stage = "updating_manifest"
	if err := s.writeCleanupState(wp, cp, true); err != nil {
		return err
	}
	if err := s.resetManifestDeletedAt(wp, workID); err != nil {
		return s.failCleanup(wp, cp, true, fmt.Errorf("work: reset deleted state for %s: %w", workID, err))
	}
	cp.Stage = "updating_index"
	if err := s.writeCleanupState(wp, cp, true); err != nil {
		return err
	}
	done, lockErr := s.beginIndexOp()
	if lockErr != nil {
		return s.failCleanup(wp, cp, true, fmt.Errorf("work: lock index while restoring %s: %w", workID, lockErr))
	}
	err = s.upsertIndexLocked(workID)
	err = errors.Join(err, done())
	if err != nil {
		return s.failCleanup(wp, cp, true, fmt.Errorf("work: restore %s index: %w", workID, err))
	}
	cp.Stage = "done"
	cp.Error = ""
	cp.CompletedAt = ptrTime(time.Now().UTC())
	return s.writeCleanupState(wp, cp, true)
}

// ── Definition storage ─────────────────────────────────────────────────────

func (s *FileWorkStore) WriteDefinition(workID string, def *WorkDefinitionSnapshot) error {
	return s.withWorkOp(workID, func() error {
		return s.writeDefinitionLocked(workID, def)
	})
}

func (s *FileWorkStore) writeDefinitionLocked(workID string, def *WorkDefinitionSnapshot) error {
	if def == nil {
		return fmt.Errorf("%w: definition for %s", ErrWorkNilInput, workID)
	}
	// Always normalize regardless of whether Digest is set — ensures the
	// stored content is canonical and the digest is authoritative.
	normalized, err := NormalizeDefinitionSnapshot(def)
	if err != nil {
		return fmt.Errorf("work: normalize definition for %s: %w", workID, err)
	}
	if normalized.Digest == "" {
		return fmt.Errorf("work: definition for %s has empty digest after normalization", workID)
	}

	path, err := s.definitionPath(workID, normalized.Digest)
	if err != nil {
		return err
	}

	existing, existErr := os.ReadFile(path)
	if existErr == nil {
		// File exists — compare normalized content, not just the Digest field.
		var existingDef WorkDefinitionSnapshot
		if json.Unmarshal(existing, &existingDef) == nil {
			existingNormalized, normErr := NormalizeDefinitionSnapshot(&existingDef)
			if normErr == nil && existingNormalized.Digest == normalized.Digest {
				return nil // idempotent
			}
		}
		return fmt.Errorf("%w: %w: definition at %s failed integrity validation", ErrWorkDefinitionImmutable, ErrWorkNeedsRepair, path)
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing definition: %w", existErr)
	}

	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal definition for %s: %w", workID, err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("work: create definitions dir for %s: %w", workID, err)
	}
	return writeDerivedFile(path, data, 0o644)
}

func (s *FileWorkStore) LoadDefinition(workID, digest string) (*WorkDefinitionSnapshot, error) {
	path, err := s.definitionPath(workID, digest)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: definition %s for %s", ErrWorkNotFound, digest, workID)
		}
		return nil, fmt.Errorf("work: read definition %s for %s: %w", digest, workID, err)
	}

	var def WorkDefinitionSnapshot
	if err := json.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("%w: corrupt definition %s for %s: %v", ErrWorkNeedsRepair, digest, workID, err)
	}

	if def.Digest == "" {
		return nil, fmt.Errorf("%w: %w: definition for %s has empty stored digest", ErrWorkNeedsRepair, ErrWorkDigestMismatch, workID)
	}
	if def.Digest != digest {
		return nil, fmt.Errorf("%w: %w: definition for %s: stored digest %q does not match requested %q", ErrWorkNeedsRepair, ErrWorkDigestMismatch, workID, def.Digest, digest)
	}

	computedDigest, err := ComputeDigest(&def)
	if err != nil {
		return nil, fmt.Errorf("work: compute digest for loaded definition %s: %w", workID, err)
	}
	if computedDigest != digest {
		return nil, fmt.Errorf("%w: %w: definition for %s: content integrity check failed (stored %q, computed %q)", ErrWorkNeedsRepair, ErrWorkDigestMismatch, workID, digest, computedDigest)
	}

	return &def, nil
}

// ── Blob storage ───────────────────────────────────────────────────────────

func (s *FileWorkStore) WriteBlob(workID, digest string, data []byte) error {
	return s.withWorkOp(workID, func() error {
		return s.writeBlobLocked(workID, digest, data)
	})
}

func (s *FileWorkStore) writeBlobLocked(workID, digest string, data []byte) error {
	if err := validateDigest(digest); err != nil {
		return err
	}
	if data == nil {
		return fmt.Errorf("%w: blob for %s", ErrWorkNilInput, workID)
	}

	path, err := s.blobPath(workID, digest)
	if err != nil {
		return err
	}

	hash := sha256.Sum256(data)
	expectedHash := digest[len(digestPrefix):]
	actualHash := fmt.Sprintf("%x", hash[:])
	if actualHash != expectedHash {
		return fmt.Errorf("%w: blob for %s has digest %q but content hashes to sha256:%s", ErrWorkDigestMismatch, workID, digest, actualHash)
	}

	existing, existErr := os.ReadFile(path)
	if existErr == nil {
		existingHash := sha256.Sum256(existing)
		if existingHash == hash {
			return nil // idempotent
		}
		return fmt.Errorf("%w: %w: blob at %s has different content than digest %q", ErrWorkNeedsRepair, ErrWorkDigestMismatch, path, digest)
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing blob: %w", existErr)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("work: create blobs dir for %s: %w", workID, err)
	}
	return writeDerivedFile(path, data, 0o644)
}

func (s *FileWorkStore) ReadBlob(workID, digest string) ([]byte, error) {
	path, err := s.blobPath(workID, digest)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: blob %s for %s", ErrWorkNotFound, digest, workID)
		}
		return nil, fmt.Errorf("work: read blob %s for %s: %w", digest, workID, err)
	}

	hash := sha256.Sum256(data)
	expectedHash := digest[len(digestPrefix):]
	actualHash := fmt.Sprintf("%x", hash[:])
	if actualHash != expectedHash {
		return nil, fmt.Errorf("%w: %w: blob for %s: stored content integrity check failed (expected sha256:%s, got sha256:%s)", ErrWorkNeedsRepair, ErrWorkDigestMismatch, workID, expectedHash, actualHash)
	}

	return data, nil
}

// ── Manifest ───────────────────────────────────────────────────────────────

type workManifest struct {
	SchemaVersion   int              `json:"schemaVersion"`
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	State           WorkState        `json:"state"`
	ArchiveState    WorkArchiveState `json:"archiveState"`
	BlueprintRef    BlueprintRef     `json:"blueprintRef"`
	CreatedAt       time.Time        `json:"createdAt"`
	UpdatedAt       time.Time        `json:"updatedAt"`
	ArchivedAt      *time.Time       `json:"archivedAt,omitempty"`
	DeletedAt       *time.Time       `json:"deletedAt,omitempty"`
	Revision        int64            `json:"revision"`
	CreateRequestID string           `json:"createRequestId,omitempty"`
	CreateDigest    string           `json:"createDigest,omitempty"`
}

func manifestFromWork(value *Work, revision int64) *workManifest {
	return &workManifest{
		SchemaVersion: value.SchemaVersion,
		ID:            value.ID,
		Name:          value.Name,
		State:         value.State,
		ArchiveState:  value.ArchiveState,
		BlueprintRef:  value.BlueprintRef,
		CreatedAt:     value.CreatedAt,
		UpdatedAt:     value.UpdatedAt,
		ArchivedAt:    value.ArchivedAt,
		Revision:      revision,
	}
}

func (s *FileWorkStore) WriteManifest(workID string, m *workManifest) error {
	return s.withWorkOp(workID, func() error {
		return s.writeManifestLocked(workID, m)
	})
}

func (s *FileWorkStore) writeManifestLocked(workID string, m *workManifest) error {
	if m == nil {
		return fmt.Errorf("%w: manifest for %s", ErrWorkNilInput, workID)
	}
	if m.ID != workID {
		return fmt.Errorf("work: manifest workID mismatch: expected %q, got %q", workID, m.ID)
	}
	if err := CheckSchemaVersion("WorkManifest", m.SchemaVersion); err != nil {
		return err
	}
	path, err := s.manifestPath(workID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("work: create work dir for manifest %s: %w", workID, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal manifest for %s: %w", workID, err)
	}
	data = append(data, '\n')
	return writeDerivedFile(path, data, 0o644)
}

func (s *FileWorkStore) LoadManifest(workID string) (*workManifest, error) {
	path, err := s.manifestPath(workID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: manifest for %s", ErrWorkNotFound, workID)
		}
		return nil, fmt.Errorf("work: read manifest for %s: %w", workID, err)
	}
	var m workManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%w: corrupt manifest for %s: %v", ErrWorkNeedsRepair, workID, err)
	}
	if m.ID != workID {
		return nil, fmt.Errorf("%w: manifest workID mismatch: %s contains %q", ErrWorkNeedsRepair, workID, m.ID)
	}
	if err := CheckSchemaVersion("WorkManifest", m.SchemaVersion); err != nil {
		return nil, err
	}
	return &m, nil
}

func summaryFromManifest(m *workManifest) WorkSummary {
	return WorkSummary{
		ID:           m.ID,
		Name:         m.Name,
		State:        m.State,
		ArchiveState: m.ArchiveState,
		BlueprintRef: m.BlueprintRef,
		CreatedAt:    m.CreatedAt,
		UpdatedAt:    m.UpdatedAt,
	}
}

func (s *FileWorkStore) updateManifestDeletedAt(dir, workID string, t time.Time) error {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m workManifest
	if json.Unmarshal(data, &m) != nil {
		return fmt.Errorf("%w: corrupt manifest for %s", ErrWorkNeedsRepair, workID)
	}
	if m.ID != workID {
		return fmt.Errorf("%w: manifest identity mismatch for %s", ErrWorkNeedsRepair, workID)
	}
	m.DeletedAt = &t
	m.ArchiveState = ArchiveDeleted
	m.UpdatedAt = t
	newData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	newData = append(newData, '\n')
	return writeDerivedFile(path, newData, 0o644)
}

func (s *FileWorkStore) resetManifestDeletedAt(dir, workID string) error {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var m workManifest
	if json.Unmarshal(data, &m) != nil {
		return fmt.Errorf("%w: corrupt manifest for %s", ErrWorkNeedsRepair, workID)
	}
	if m.ID != workID {
		return fmt.Errorf("%w: manifest identity mismatch for %s", ErrWorkNeedsRepair, workID)
	}
	m.DeletedAt = nil
	projectionData, projectionErr := os.ReadFile(filepath.Join(dir, "projection.json"))
	if projectionErr != nil {
		return fmt.Errorf("%w: read projection while restoring %s: %v", ErrWorkNeedsRepair, workID, projectionErr)
	}
	var projection Work
	if err := json.Unmarshal(projectionData, &projection); err != nil || projection.ID != workID {
		return fmt.Errorf("%w: invalid projection while restoring %s", ErrWorkNeedsRepair, workID)
	}
	m.ArchiveState = projection.ArchiveState
	m.UpdatedAt = time.Now().UTC()
	newData, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	newData = append(newData, '\n')
	return writeDerivedFile(path, newData, 0o644)
}

// ── Index ──────────────────────────────────────────────────────────────────

type workIndex struct {
	SchemaVersion int           `json:"schemaVersion"`
	Works         []WorkSummary `json:"works"`
	UpdatedAt     time.Time     `json:"updatedAt"`
}

func (s *FileWorkStore) loadIndexLocked() (*workIndex, error) {
	diskWorks, scanErr := s.scanIndexEntries()
	if scanErr != nil {
		return nil, scanErr
	}
	data, err := os.ReadFile(s.indexPath())
	if err == nil {
		var idx workIndex
		if jsonErr := json.Unmarshal(data, &idx); jsonErr == nil {
			if schemaErr := CheckSchemaVersion("WorkIndex", idx.SchemaVersion); schemaErr != nil {
				return nil, schemaErr
			}
			sort.Slice(idx.Works, func(i, j int) bool { return idx.Works[i].ID < idx.Works[j].ID })
			sort.Slice(diskWorks, func(i, j int) bool { return diskWorks[i].ID < diskWorks[j].ID })
			left, _ := json.Marshal(idx.Works)
			right, _ := json.Marshal(diskWorks)
			if string(left) == string(right) {
				return &idx, nil
			}
		}
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("work: read index: %w", err)
	}
	return s.rebuildIndexFromLocked(diskWorks)
}

func (s *FileWorkStore) rebuildIndexLocked() (*workIndex, error) {
	works, err := s.scanIndexEntries()
	if err != nil {
		return nil, err
	}
	return s.rebuildIndexFromLocked(works)
}

func (s *FileWorkStore) rebuildIndexFromLocked(works []WorkSummary) (*workIndex, error) {
	idx := &workIndex{
		SchemaVersion: SchemaVersion,
		Works:         works,
		UpdatedAt:     time.Now().UTC(),
	}
	sort.Slice(idx.Works, func(i, j int) bool { return idx.Works[i].UpdatedAt.After(idx.Works[j].UpdatedAt) })
	if err := s.writeIndexLocked(idx); err != nil {
		return nil, err
	}
	return idx, nil
}

func (s *FileWorkStore) scanIndexEntries() ([]WorkSummary, error) {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("work: read works dir: %w", err)
	}

	works := make([]WorkSummary, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") || strings.EqualFold(entry.Name(), "blueprints") {
			continue
		}
		workID := entry.Name()
		if err := validateWorkID(workID); err != nil {
			return nil, fmt.Errorf("%w: invalid directory %q under works root: %v", ErrWorkNeedsRepair, workID, err)
		}
		m, mErr := loadManifestAt(filepath.Join(s.workDir, workID, "manifest.json"))
		if mErr != nil {
			return nil, fmt.Errorf("%w: load manifest for %s while rebuilding index: %v", ErrWorkNeedsRepair, workID, mErr)
		}
		if m.ID != workID {
			return nil, fmt.Errorf("%w: manifest identity mismatch for %s", ErrWorkNeedsRepair, workID)
		}
		if err := CheckSchemaVersion("WorkManifest", m.SchemaVersion); err != nil {
			return nil, err
		}
		works = append(works, summaryFromManifest(m))
	}
	return works, nil
}

func loadManifestAt(path string) (*workManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m workManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt manifest: %w", err)
	}
	return &m, nil
}

func (s *FileWorkStore) writeIndexLocked(idx *workIndex) error {
	idx.SchemaVersion = SchemaVersion
	idx.UpdatedAt = time.Now().UTC()

	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal index: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(s.workDir, 0o755); err != nil {
		return fmt.Errorf("work: mkdir works dir: %w", err)
	}
	return writeDerivedFile(s.indexPath(), data, 0o644)
}

func (s *FileWorkStore) upsertIndexLocked(workID string) error {
	m, err := s.LoadManifest(workID)
	if err != nil {
		return err
	}
	summary := summaryFromManifest(m)

	idx, err := s.loadIndexLocked()
	if err != nil {
		return err
	}

	found := false
	for i := range idx.Works {
		if idx.Works[i].ID == workID {
			idx.Works[i] = summary
			found = true
			break
		}
	}
	if !found {
		idx.Works = append(idx.Works, summary)
	}

	return s.writeIndexLocked(idx)
}

func (s *FileWorkStore) removeFromIndexLocked(workID string) error {
	idx, err := s.loadIndexLocked()
	if err != nil {
		return err
	}
	for i, w := range idx.Works {
		if w.ID == workID {
			idx.Works = append(idx.Works[:i], idx.Works[i+1:]...)
			break
		}
	}
	return s.writeIndexLocked(idx)
}

// ── Cleanup-pending ────────────────────────────────────────────────────────

type cleanupPending struct {
	RequestID   string     `json:"requestId"`
	Operation   string     `json:"operation"` // "trash" | "restore" | "gc" | "create"
	WorkID      string     `json:"workId"`
	Stage       string     `json:"stage"`
	CleanupPath string     `json:"cleanupPath,omitempty"`
	TrashedAt   *time.Time `json:"trashedAt,omitempty"`
	StartedAt   time.Time  `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
	Retries     int        `json:"retries"`
}

func (s *FileWorkStore) writeCleanupPendingTo(dir string, cp *cleanupPending) error {
	path := filepath.Join(dir, "cleanup-pending.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("work: mkdir for cleanup-pending: %w", err)
	}
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal cleanup-pending: %w", err)
	}
	data = append(data, '\n')
	return writeDerivedFile(path, data, 0o644)
}

func (s *FileWorkStore) writeCleanupState(dir string, cp *cleanupPending, committed bool) error {
	if err := s.writeCleanupPendingTo(dir, cp); err != nil {
		cause := fmt.Errorf("work: persist cleanup-pending in %s: %w", dir, err)
		return cleanupRecovery(cp, cp.CleanupPath, committed, false, cause)
	}
	return nil
}

func (s *FileWorkStore) failCleanup(dir string, cp *cleanupPending, committed bool, cause error) error {
	cp.Error = cause.Error()
	markerErr := s.writeCleanupPendingTo(dir, cp)
	persisted := markerErr == nil
	if markerErr != nil {
		markerErr = fmt.Errorf("work: persist cleanup-pending in %s: %w", dir, markerErr)
	}
	return cleanupRecovery(cp, cp.CleanupPath, committed, persisted, errors.Join(cause, markerErr))
}

func (s *FileWorkStore) resumeMoveCleanup(dir string, cp *cleanupPending, committed bool) error {
	if cp.Stage == "removing_source" {
		if err := s.validateSourceCleanup(cp); err != nil {
			return s.failCleanup(dir, cp, committed, err)
		}
		return nil
	}
	paths, err := s.moveCleanupPaths(cp)
	if err != nil {
		return s.failCleanup(dir, cp, committed, err)
	}
	if len(paths) == 0 {
		return nil
	}
	for _, path := range paths {
		cp.CleanupPath = path
		if err := removeWorkDir(path); err != nil {
			cp.Stage = "cleanup_failed"
			cause := fmt.Errorf("work: remove pending move temp %s: %w", path, err)
			return s.failCleanup(dir, cp, committed, cause)
		}
		if _, err := os.Stat(path); err != nil && !os.IsNotExist(err) {
			cp.Stage = "cleanup_failed"
			cause := fmt.Errorf("work: verify pending move temp removal %s: %w", path, err)
			return s.failCleanup(dir, cp, committed, cause)
		} else if err == nil {
			cp.Stage = "cleanup_failed"
			cause := fmt.Errorf("work: pending move temp still exists after removal: %s", path)
			return s.failCleanup(dir, cp, committed, cause)
		}
	}
	cp.CleanupPath = ""
	cp.Stage = "moving"
	cp.Error = ""
	return s.writeCleanupState(dir, cp, committed)
}

func (s *FileWorkStore) validateSourceCleanup(cp *cleanupPending) error {
	if cp.CleanupPath == "" {
		return nil
	}
	var expected string
	var err error
	switch cp.Operation {
	case "trash":
		expected, err = s.workPath(cp.WorkID)
	case "restore":
		expected, err = s.trashPath(cp.WorkID)
	default:
		return fmt.Errorf("%w: operation %q cannot remove a move source", ErrWorkNeedsRepair, cp.Operation)
	}
	if err != nil {
		return err
	}
	if !samePath(cp.CleanupPath, expected) {
		return fmt.Errorf("%w: invalid pending source cleanup path %q", ErrWorkNeedsRepair, cp.CleanupPath)
	}
	return nil
}

func (s *FileWorkStore) moveCleanupPaths(cp *cleanupPending) ([]string, error) {
	prefix := ".move-" + cp.WorkID + "-"
	paths := map[string]bool{}
	if cp.CleanupPath != "" {
		path := filepath.Clean(cp.CleanupPath)
		parent := filepath.Clean(filepath.Dir(path))
		if (!samePath(parent, s.workDir) && !samePath(parent, s.trashDir)) || !strings.HasPrefix(filepath.Base(path), prefix) {
			return nil, fmt.Errorf("%w: invalid pending move cleanup path %q", ErrWorkNeedsRepair, cp.CleanupPath)
		}
		paths[path] = true
	}
	for _, root := range []string{s.workDir, s.trashDir} {
		entries, err := os.ReadDir(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("work: scan move temp dirs in %s: %w", root, err)
		}
		for _, entry := range entries {
			if entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
				paths[filepath.Join(root, entry.Name())] = true
			}
		}
	}
	result := make([]string, 0, len(paths))
	for path := range paths {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func cleanupStageRank(stage string) int {
	switch stage {
	case "moving", "copying":
		return 1
	case "move_failed", "copy_failed":
		return 2
	case "removing_source":
		return 3
	case "updating_manifest":
		return 4
	case "updating_index", "index_failed":
		return 5
	case "cleanup_failed":
		return 6
	case "done":
		return 7
	default:
		return 0
	}
}

func (s *FileWorkStore) loadCleanupPendingAny(workID string) (*cleanupPending, error) {
	// Try work dir first, then trash.
	var found *cleanupPending
	for _, getPath := range []func() (string, error){
		func() (string, error) { return s.cleanupPendingPath(workID) },
		func() (string, error) {
			tp, err := s.trashPath(workID)
			if err != nil {
				return "", err
			}
			return filepath.Join(tp, "cleanup-pending.json"), nil
		},
	} {
		path, err := getPath()
		if err != nil {
			return nil, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("work: read cleanup-pending for %s: %w", workID, err)
		}
		var cp cleanupPending
		if err := json.Unmarshal(data, &cp); err != nil {
			return nil, fmt.Errorf("%w: corrupt cleanup-pending for %s: %v", ErrWorkNeedsRepair, workID, err)
		}
		if cp.WorkID != workID || cp.RequestID == "" || cp.Operation == "" {
			return nil, fmt.Errorf("%w: invalid cleanup-pending identity for %s", ErrWorkNeedsRepair, workID)
		}
		if found != nil {
			if found.RequestID != cp.RequestID || found.Operation != cp.Operation {
				return nil, fmt.Errorf("%w: divergent cleanup-pending copies for %s", ErrWorkNeedsRepair, workID)
			}
			// A cross-volume move can leave two copies when source cleanup
			// fails. The later durable stage is the resumable source of truth.
			if cleanupStageRank(found.Stage) > cleanupStageRank(cp.Stage) {
				continue
			}
		}
		copy := cp
		found = &copy
	}
	return found, nil
}

// LoadCleanupPending reads the cleanup-pending state for a workID.
func (s *FileWorkStore) LoadCleanupPending(workID string) (*cleanupPending, error) {
	return s.loadCleanupPendingAny(workID)
}

func (s *FileWorkStore) clearCleanupPending(workID string) error {
	var errs []error
	for _, getPath := range []func() (string, error){
		func() (string, error) { return s.cleanupPendingPath(workID) },
		func() (string, error) {
			tp, err := s.trashPath(workID)
			if err != nil {
				return "", err
			}
			return filepath.Join(tp, "cleanup-pending.json"), nil
		},
	} {
		path, err := getPath()
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ── GC ─────────────────────────────────────────────────────────────────────

type gcProgress struct {
	RequestID   string            `json:"requestId"`
	Cutoff      time.Time         `json:"cutoff"`
	Targets     []string          `json:"targets"`
	Deleted     []string          `json:"deleted"`
	Errors      map[string]string `json:"errors,omitempty"`
	StartedAt   time.Time         `json:"startedAt"`
	CompletedAt *time.Time        `json:"completedAt,omitempty"`
}

func (s *FileWorkStore) GCTrash(requestID string) ([]string, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, fmt.Errorf("%w: GCTrash", ErrWorkRequestIDRequired)
	}
	if s.trashRetention == 0 {
		return nil, nil // GC disabled
	}
	digest := sha256.Sum256([]byte(requestID))
	done, err := s.beginWorkOp("gc-" + fmt.Sprintf("%x", digest[:]))
	if err != nil {
		return nil, err
	}
	deleted, gcErr := s.gcTrashLocked(requestID)
	return deleted, errors.Join(gcErr, done())
}

func (s *FileWorkStore) gcTrashLocked(requestID string) ([]string, error) {
	progress, path, err := s.loadOrCreateGC(requestID)
	if err != nil {
		return nil, err
	}
	if progress.CompletedAt != nil {
		return append([]string(nil), progress.Deleted...), nil
	}
	deletedSet := make(map[string]bool, len(progress.Deleted))
	for _, workID := range progress.Deleted {
		deletedSet[workID] = true
	}
	var failures []error
	for _, workID := range progress.Targets {
		if deletedSet[workID] {
			continue
		}
		err := s.withWorkOp(workID, func() error {
			tp, pathErr := s.trashPath(workID)
			if pathErr != nil {
				return pathErr
			}
			if _, statErr := os.Stat(tp); os.IsNotExist(statErr) {
				return nil // a prior attempt removed it before progress persisted
			} else if statErr != nil {
				return statErr
			}
			cp := &cleanupPending{RequestID: requestID, Operation: "gc", WorkID: workID, Stage: "removing", StartedAt: progress.StartedAt}
			if writeErr := s.writeCleanupPendingTo(tp, cp); writeErr != nil {
				return writeErr
			}
			if removeErr := removeWorkDir(tp); removeErr != nil {
				return removeErr
			}
			if _, statErr := os.Stat(tp); !os.IsNotExist(statErr) {
				return fmt.Errorf("trash directory still exists after removal")
			}
			return nil
		})
		if err != nil {
			if progress.Errors == nil {
				progress.Errors = map[string]string{}
			}
			progress.Errors[workID] = err.Error()
			failures = append(failures, fmt.Errorf("gc %s: %w", workID, err))
			continue
		}
		delete(progress.Errors, workID)
		progress.Deleted = append(progress.Deleted, workID)
		deletedSet[workID] = true
		if err := s.writeGCProgress(path, progress); err != nil {
			return append([]string(nil), progress.Deleted...), committedRecovery("gc", workID, requestID, 0, err)
		}
	}
	if len(failures) > 0 {
		if err := s.writeGCProgress(path, progress); err != nil {
			failures = append(failures, err)
		}
		return append([]string(nil), progress.Deleted...), errors.Join(failures...)
	}
	now := time.Now().UTC()
	progress.CompletedAt = &now
	sort.Strings(progress.Deleted)
	if err := s.writeGCProgress(path, progress); err != nil {
		return append([]string(nil), progress.Deleted...), committedRecovery("gc", "", requestID, 0, err)
	}
	return append([]string(nil), progress.Deleted...), nil
}

func (s *FileWorkStore) loadOrCreateGC(requestID string) (*gcProgress, string, error) {
	digest := sha256.Sum256([]byte(requestID))
	path := filepath.Join(s.trashDir, ".gc", fmt.Sprintf("%x.json", digest[:]))
	data, err := os.ReadFile(path)
	if err == nil {
		var progress gcProgress
		if err := json.Unmarshal(data, &progress); err != nil {
			return nil, path, fmt.Errorf("%w: corrupt GC progress: %v", ErrWorkNeedsRepair, err)
		}
		if progress.RequestID != requestID {
			return nil, path, ErrWorkRequestIDConflict
		}
		return &progress, path, nil
	}
	if !os.IsNotExist(err) {
		return nil, path, err
	}
	now := time.Now().UTC()
	progress := &gcProgress{RequestID: requestID, Cutoff: now.Add(-s.trashRetention), StartedAt: now}
	entries, err := os.ReadDir(s.trashDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, path, err
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		workID := entry.Name()
		if err := validateWorkID(workID); err != nil {
			return nil, path, fmt.Errorf("%w: invalid trash entry %q", ErrWorkNeedsRepair, workID)
		}
		tp, _ := s.trashPath(workID)
		deletedAt, err := s.loadDeletedAt(tp, workID)
		if err != nil {
			return nil, path, err
		}
		if !deletedAt.After(progress.Cutoff) {
			progress.Targets = append(progress.Targets, workID)
		}
	}
	sort.Strings(progress.Targets)
	if err := s.writeGCProgress(path, progress); err != nil {
		return nil, path, err
	}
	return progress, path, nil
}

func (s *FileWorkStore) writeGCProgress(path string, progress *gcProgress) error {
	data, err := json.MarshalIndent(progress, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeDerivedFile(path, data, 0o644)
}

func (s *FileWorkStore) loadDeletedAt(dir, workID string) (time.Time, error) {
	path := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, fmt.Errorf("%w: read trash manifest for %s: %v", ErrWorkNeedsRepair, workID, err)
	}
	var m workManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return time.Time{}, fmt.Errorf("%w: corrupt trash manifest for %s: %v", ErrWorkNeedsRepair, workID, err)
	}
	if m.ID != workID || m.DeletedAt == nil {
		return time.Time{}, fmt.Errorf("%w: trash manifest for %s has no valid deletedAt", ErrWorkNeedsRepair, workID)
	}
	return *m.DeletedAt, nil
}

// TrashRetention returns the configured trash retention period.
func (s *FileWorkStore) TrashRetention() time.Duration { return s.trashRetention }

// ── Atomic directory creation ──────────────────────────────────────────────

// CreateWorkDirInput carries everything needed to atomically create a Work directory.
type CreateWorkDirInput struct {
	RequestID  string                  `json:"requestId"`
	Work       *Work                   `json:"work"`
	Definition *WorkDefinitionSnapshot `json:"definition,omitempty"`
	Events     []WorkEvent             `json:"events,omitempty"`
	Blobs      map[string][]byte       `json:"blobs,omitempty"` // digest → data
}

// CreateWorkDir atomically creates a complete Work directory. It writes all
// assets into a sibling temp directory, validates everything, then renames
// the temp directory into place. On failure the temp directory is removed.
func (s *FileWorkStore) CreateWorkDir(input CreateWorkDirInput) error {
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		return fmt.Errorf("%w: CreateWorkDir", ErrWorkRequestIDRequired)
	}
	if input.Work == nil {
		return fmt.Errorf("%w: CreateWorkDir work", ErrWorkNilInput)
	}
	workID := input.Work.ID
	if err := validateWorkID(workID); err != nil {
		return err
	}
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return err
	}
	createErr := s.createWorkDirLocked(input)
	doneErr := done()
	if doneErr == nil {
		return createErr
	}
	if createErr != nil {
		return errors.Join(createErr, doneErr)
	}
	wp, pathErr := s.workPath(workID)
	if pathErr != nil {
		return errors.Join(doneErr, pathErr)
	}
	manifest, manifestErr := loadManifestAt(filepath.Join(wp, "manifest.json"))
	if manifestErr == nil && manifest.CreateRequestID == input.RequestID {
		return committedRecovery("create", workID, input.RequestID, manifest.Revision, doneErr)
	}
	return errors.Join(doneErr, manifestErr, fmt.Errorf("work: create request %q lease release failed before committed state could be verified", input.RequestID))
}

func (s *FileWorkStore) createWorkDirLocked(input CreateWorkDirInput) (retErr error) {
	workID := input.Work.ID
	wp, _ := s.workPath(workID)
	tp, _ := s.trashPath(workID)
	intentDigest, err := workCreateDigest(input.Work)
	if err != nil {
		return fmt.Errorf("work: digest create intent for %s: %w", workID, err)
	}
	if s.isDirWithData(wp) {
		manifest, err := s.LoadManifest(workID)
		if err != nil {
			return err
		}
		if manifest.CreateRequestID != input.RequestID {
			return fmt.Errorf("%w: %s was created by request %q", ErrWorkRequestIDConflict, workID, manifest.CreateRequestID)
		}
		if manifest.CreateDigest != "" && manifest.CreateDigest != intentDigest {
			return fmt.Errorf("%w: create request %q was reused with different content", ErrWorkRequestIDConflict, input.RequestID)
		}
		if manifest.CreateDigest == "" {
			manifest.CreateDigest = intentDigest
			if err := s.writeManifestLocked(workID, manifest); err != nil {
				return fmt.Errorf("work: persist create digest for %s: %w", workID, err)
			}
		}
		if err := s.resumeCreateCleanup(workID, input.RequestID, true); err != nil {
			return err
		}
		if _, err := s.loadProjection(wp, workID); err != nil {
			return err
		}
		done, lockErr := s.beginIndexOp()
		if lockErr != nil {
			return lockErr
		}
		err = s.upsertIndexLocked(workID)
		return errors.Join(err, done())
	}
	if s.isDirWithData(tp) {
		return fmt.Errorf("%w: %s", ErrWorkTrashExists, workID)
	}
	if _, err := os.Stat(wp); err == nil {
		return fmt.Errorf("%w: incomplete final directory already exists for %s", ErrWorkNeedsRepair, workID)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := CheckSchemaVersion("Work", input.Work.SchemaVersion); err != nil {
		return err
	}
	if err := os.MkdirAll(s.workDir, 0o755); err != nil {
		return err
	}
	if err := s.resumeCreateCleanup(workID, input.RequestID, false); err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp(s.workDir, ".new-"+workID+"-*")
	if err != nil {
		return fmt.Errorf("work: create temp dir for %s: %w", workID, err)
	}
	exposed := false
	defer func() {
		if exposed {
			return
		}
		if cleanupErr := removeWorkDir(tmpDir); cleanupErr != nil {
			cause := fmt.Errorf("work: clean failed create temp %s: %w", tmpDir, cleanupErr)
			cp := &cleanupPending{RequestID: input.RequestID, Operation: "create", WorkID: workID, Stage: "cleanup_failed", CleanupPath: tmpDir, StartedAt: time.Now().UTC(), Error: cause.Error()}
			if info, statErr := os.Stat(tmpDir); statErr == nil && info.IsDir() {
				retErr = errors.Join(retErr, s.failCleanup(tmpDir, cp, false, cause))
			} else {
				if statErr != nil && !os.IsNotExist(statErr) {
					cause = errors.Join(cause, fmt.Errorf("work: inspect failed create temp %s: %w", tmpDir, statErr))
				}
				retErr = errors.Join(retErr, cleanupRecovery(cp, "", false, false, cause))
			}
		}
	}()

	events := append([]WorkEvent(nil), input.Events...)
	if len(events) == 0 {
		payload, err := json.Marshal(input.Work)
		if err != nil {
			return err
		}
		events = []WorkEvent{{SchemaVersion: WorkEventSchemaVersion, ID: "create-" + input.RequestID, RequestID: input.RequestID, WorkID: workID, Type: EventWorkCreated, Payload: payload, CreatedAt: input.Work.CreatedAt}}
	}
	if events[0].Type != EventWorkCreated {
		return fmt.Errorf("work: first create event must be %s", EventWorkCreated)
	}
	if events[0].RequestID == "" {
		events[0].RequestID = input.RequestID
	}
	if err := AcquireWorkLease(tmpDir); err != nil {
		return err
	}
	for i := range events {
		if events[i].WorkID != workID {
			cause := fmt.Errorf("work: create event %d workID mismatch", i)
			return errors.Join(cause, releaseCreateLease(tmpDir, workID, input.RequestID))
		}
		events[i].Revision = 0
		events[i].BaseRevision = 0
		if _, err := AppendWorkEvent(tmpDir, events[i], true); err != nil {
			cause := fmt.Errorf("work: append create event %d: %w", i, err)
			return errors.Join(cause, releaseCreateLease(tmpDir, workID, input.RequestID))
		}
	}
	if err := releaseCreateLease(tmpDir, workID, input.RequestID); err != nil {
		return err
	}
	replay, projection, err := ReplayWithReducer(tmpDir, DefaultReducer())
	if err != nil {
		return fmt.Errorf("work: replay created work: %w", err)
	}
	if projection == nil {
		return fmt.Errorf("%w: replay created no projection", ErrWorkNeedsRepair)
	}
	if replay.ReadOnly || replay.NeedsRepair || replay.IndexNeedsRebuild {
		return fmt.Errorf("%w: created event log did not validate", ErrWorkNeedsRepair)
	}
	revision := replay.Index.Revision
	projectionData, err := json.MarshalIndent(projection, "", "  ")
	if err != nil {
		return err
	}
	projectionData = append(projectionData, '\n')
	if err := writeDerivedFile(filepath.Join(tmpDir, "projection.json"), projectionData, 0o644); err != nil {
		return err
	}
	manifest := manifestFromWork(projection, revision)
	manifest.CreateRequestID = input.RequestID
	manifest.CreateDigest = intentDigest
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestData = append(manifestData, '\n')
	if err := writeDerivedFile(filepath.Join(tmpDir, "manifest.json"), manifestData, 0o644); err != nil {
		return err
	}
	if input.Definition != nil {
		normalized, err := NormalizeDefinitionSnapshot(input.Definition)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(normalized, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		if err := writeDerivedFile(filepath.Join(tmpDir, "definitions", strings.TrimPrefix(normalized.Digest, digestPrefix)+".json"), data, 0o644); err != nil {
			return err
		}
	}
	for digest, data := range input.Blobs {
		if err := validateDigest(digest); err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		if fmt.Sprintf("%x", hash[:]) != strings.TrimPrefix(digest, digestPrefix) {
			return ErrWorkDigestMismatch
		}
		if err := writeDerivedFile(filepath.Join(tmpDir, "blobs", strings.TrimPrefix(digest, digestPrefix)), data, 0o644); err != nil {
			return err
		}
	}
	if _, err := treeDigests(tmpDir); err != nil {
		return err
	}
	if err := renameWorkDir(tmpDir, wp); err != nil {
		return fmt.Errorf("work: expose created directory: %w", err)
	}
	exposed = true
	done, lockErr := s.beginIndexOp()
	if lockErr != nil {
		return committedRecovery("create", workID, input.RequestID, revision, lockErr)
	}
	err = s.upsertIndexLocked(workID)
	err = errors.Join(err, done())
	if err != nil {
		return committedRecovery("create", workID, input.RequestID, revision, err)
	}
	return nil
}

func workCreateDigest(value *Work) (string, error) {
	if value == nil {
		return "", ErrWorkNilInput
	}
	copyValue := *value
	copyValue.CreatedAt = time.Time{}
	copyValue.UpdatedAt = time.Time{}
	copyValue.ArchivedAt = nil
	copyValue.Blocks = append([]BlockInstance(nil), value.Blocks...)
	for i := range copyValue.Blocks {
		copyValue.Blocks[i].CreatedAt = time.Time{}
		copyValue.Blocks[i].UpdatedAt = time.Time{}
	}
	return hashCanonical(copyValue)
}

func (s *FileWorkStore) resumeCreateCleanup(workID, requestID string, committed bool) error {
	entries, err := os.ReadDir(s.workDir)
	if err != nil {
		return fmt.Errorf("work: scan create temp dirs for %s: %w", workID, err)
	}
	prefix := ".new-" + workID + "-"
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		dir := filepath.Join(s.workDir, entry.Name())
		data, readErr := os.ReadFile(filepath.Join(dir, "cleanup-pending.json"))
		var cp cleanupPending
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				return fmt.Errorf("%w: unreadable create temp marker %s: %v", ErrWorkNeedsRepair, dir, readErr)
			}
			cp = cleanupPending{
				RequestID:   requestID,
				Operation:   "create",
				WorkID:      workID,
				Stage:       "cleanup_failed",
				CleanupPath: dir,
				StartedAt:   time.Now().UTC(),
			}
			if !committed {
				manifest, manifestErr := loadManifestAt(filepath.Join(dir, "manifest.json"))
				if manifestErr != nil {
					cause := fmt.Errorf("%w: markerless create temp %s cannot verify request %q: %v", ErrWorkNeedsRepair, dir, requestID, manifestErr)
					return cleanupRecovery(&cp, dir, false, false, cause)
				}
				if manifest.ID != workID {
					cause := fmt.Errorf("%w: markerless create temp %s has manifest workID %q", ErrWorkNeedsRepair, dir, manifest.ID)
					return cleanupRecovery(&cp, dir, false, false, cause)
				}
				if manifest.CreateRequestID != requestID {
					return fmt.Errorf("%w: create temp %s belongs to request %q", ErrWorkRequestIDConflict, dir, manifest.CreateRequestID)
				}
			}
		} else {
			if err := json.Unmarshal(data, &cp); err != nil {
				return fmt.Errorf("%w: corrupt create cleanup marker %s: %v", ErrWorkNeedsRepair, dir, err)
			}
			if cp.Operation != "create" || cp.WorkID != workID {
				return fmt.Errorf("%w: invalid create cleanup marker identity in %s", ErrWorkNeedsRepair, dir)
			}
			if cp.RequestID != requestID && !committed {
				return fmt.Errorf("%w: create temp %s belongs to request %q", ErrWorkRequestIDConflict, dir, cp.RequestID)
			}
			if committed {
				cp.RequestID = requestID
			}
		}
		cp.Stage = "cleanup_failed"
		cp.CleanupPath = dir
		if err := removeWorkDir(dir); err != nil {
			return s.failCleanup(dir, &cp, committed, fmt.Errorf("work: retry create temp cleanup %s: %w", dir, err))
		}
		if _, err := os.Stat(dir); err == nil {
			return s.failCleanup(dir, &cp, committed, fmt.Errorf("work: create temp still exists after cleanup: %s", dir))
		} else if !os.IsNotExist(err) {
			return s.failCleanup(dir, &cp, committed, fmt.Errorf("work: verify create temp cleanup %s: %w", dir, err))
		}
	}
	return nil
}

func releaseCreateLease(dir, workID, requestID string) error {
	if err := releaseStoreLease(dir); err != nil {
		cause := fmt.Errorf("work: release create lease for %s request %q: %w", workID, requestID, err)
		cp := &cleanupPending{
			RequestID:   requestID,
			Operation:   "create",
			WorkID:      workID,
			Stage:       "lease_release_failed",
			CleanupPath: dir,
			StartedAt:   time.Now().UTC(),
			Error:       cause.Error(),
		}
		return cleanupRecovery(cp, dir, false, false, cause)
	}
	return nil
}

// ── Atomic move ────────────────────────────────────────────────────────────

// atomicMoveWorkDir moves a directory from src to dst atomically. It first
// tries os.Rename. If that fails (cross-device, or destination exists as a
// stale remnant), it copies to a sibling temp directory inside the destination
// parent, validates, then renames into place. The final directory only appears
// after full validation.
func (s *FileWorkStore) atomicMoveWorkDir(src, dst string, cp *cleanupPending) (retErr error) {
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("%w: move destination %s", ErrWorkAlreadyExists, dst)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("work: stat move destination: %w", err)
	}
	renameErr := renameWorkDir(src, dst)
	if renameErr == nil {
		return nil
	}
	directErr := fmt.Errorf("work: direct move %s to %s: %w", src, dst, renameErr)

	parent := filepath.Dir(dst)
	if parent == "." || parent == "" {
		return errors.Join(directErr, fmt.Errorf("work: cannot determine parent of %s", dst))
	}

	tmpDst, err := os.MkdirTemp(parent, ".move-"+cp.WorkID+"-*")
	if err != nil {
		return errors.Join(directErr, fmt.Errorf("work: create temp move dir: %w", err))
	}
	success := false
	defer func() {
		if success {
			return
		}
		if cleanupErr := removeWorkDir(tmpDst); cleanupErr != nil {
			cause := fmt.Errorf("work: remove failed move temp %s: %w", tmpDst, cleanupErr)
			statInfo, statErr := os.Stat(tmpDst)
			persisted := false
			cleanupPath := ""
			if statErr == nil && statInfo.IsDir() {
				cleanupPath = tmpDst
				cp.CleanupPath = tmpDst
				cp.Stage = "cleanup_failed"
				cp.Error = errors.Join(retErr, cause).Error()
				markerErr := s.writeCleanupPendingTo(tmpDst, cp)
				persisted = markerErr == nil
				if markerErr != nil {
					cause = errors.Join(cause, fmt.Errorf("work: persist cleanup-pending in move temp %s: %w", tmpDst, markerErr))
				}
			} else if statErr != nil && !os.IsNotExist(statErr) {
				cause = errors.Join(cause, fmt.Errorf("work: inspect failed move temp %s: %w", tmpDst, statErr))
			}
			retErr = errors.Join(retErr, cleanupRecovery(cp, cleanupPath, false, persisted, cause))
		}
	}()

	if err := copyDirFull(src, tmpDst); err != nil {
		return errors.Join(directErr, fmt.Errorf("work: copy to temp: %w", err))
	}

	if err := validateCopiedTree(src, tmpDst); err != nil {
		return errors.Join(directErr, err)
	}

	// Rename temp to final.
	if err := renameWorkDir(tmpDst, dst); err != nil {
		return errors.Join(directErr, fmt.Errorf("work: rename temp %s → %s: %w", tmpDst, dst, err))
	}
	success = true
	return nil
}

func validateCopiedTree(src, dst string) error {
	want, err := treeDigests(src)
	if err != nil {
		return fmt.Errorf("work: digest source tree: %w", err)
	}
	got, err := treeDigests(dst)
	if err != nil {
		return fmt.Errorf("work: digest copied tree: %w", err)
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(got)
	if string(wantJSON) != string(gotJSON) {
		return fmt.Errorf("%w: copied directory tree differs from source", ErrWorkNeedsRepair)
	}
	if !dirHasData(dst) {
		return fmt.Errorf("%w: copied directory has no manifest or event log", ErrWorkNeedsRepair)
	}
	return nil
}

func treeDigests(root string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || strings.HasPrefix(entry.Name(), "work.lock") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		files[filepath.ToSlash(rel)] = fmt.Sprintf("%x", digest[:])
		return nil
	})
	return files, err
}

func dirHasData(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "manifest.json")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(dir, "work.events.jsonl")); err == nil {
		return true
	}
	return false
}

// ── Utility ────────────────────────────────────────────────────────────────

func (s *FileWorkStore) isDirWithData(dir string) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	return dirHasData(dir)
}

func copyDirFull(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if strings.HasPrefix(d.Name(), "work.lock") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		return writeDerivedFile(target, data, 0o644)
	})
}

func ptrTime(t time.Time) *time.Time { return &t }

// ── DefaultReducer ─────────────────────────────────────────────────────────

func DefaultReducer() WorkEventReducer {
	return func(event WorkEvent, current *Work) (*Work, error) {
		if current == nil {
			if event.Type != EventWorkCreated {
				return nil, fmt.Errorf("work: first event must be work.created, got %s", event.Type)
			}
			var w Work
			if err := json.Unmarshal(event.Payload, &w); err != nil {
				return nil, fmt.Errorf("work: unmarshal created payload: %w", err)
			}
			w.ID = event.WorkID
			w.SchemaVersion = SchemaVersion
			w.CreatedAt = event.CreatedAt
			w.UpdatedAt = event.CreatedAt
			if w.ArchiveState == "" {
				w.ArchiveState = ArchiveActive
			}
			if w.State == "" {
				w.State = WorkDraft
			}
			return &w, nil
		}

		switch event.Type {
		case EventDraftUpdated:
			var update struct {
				Name       *string          `json:"name,omitempty"`
				Prompt     *string          `json:"prompt,omitempty"`
				Inputs     map[string]any   `json:"inputs,omitempty"`
				State      WorkState        `json:"state,omitempty"`
				Placements []BlockPlacement `json:"placements,omitempty"`
			}
			if err := json.Unmarshal(event.Payload, &update); err != nil {
				return nil, fmt.Errorf("work: unmarshal draft update: %w", err)
			}
			var placements []BlockPlacement
			if update.Placements != nil {
				var placementErr error
				placements, placementErr = validateBlockPlacements(current, update.Placements)
				if placementErr != nil {
					return nil, fmt.Errorf("work: validate draft placements: %w", placementErr)
				}
			}
			if update.Name != nil {
				current.Name = *update.Name
			}
			if update.Prompt != nil {
				current.Prompt = *update.Prompt
			}
			if update.Inputs != nil {
				if current.Inputs == nil {
					current.Inputs = make(map[string]any)
				}
				for k, v := range update.Inputs {
					current.Inputs[k] = v
				}
			}
			if update.State != "" {
				if err := ValidateWorkTransition(current.State, update.State); err != nil {
					return nil, err
				}
				current.State = update.State
			}
			if update.Placements != nil {
				current.Placements = placements
			}

		case EventDefinitionFrozen:
			var def WorkDefinitionSnapshot
			if err := json.Unmarshal(event.Payload, &def); err != nil {
				return nil, fmt.Errorf("work: unmarshal definition: %w", err)
			}
			current.Definition = def

		case EventBlockUpserted:
			var block BlockInstance
			if err := json.Unmarshal(event.Payload, &block); err != nil {
				return nil, fmt.Errorf("work: unmarshal block upsert: %w", err)
			}
			if block.ID == "" {
				return nil, fmt.Errorf("work: block upsert: block id is required")
			}
			if block.Revision <= 0 {
				return nil, fmt.Errorf("work: block upsert: revision must be positive")
			}
			digest, err := blockContentDigest(&block)
			if err != nil {
				return nil, err
			}
			found := false
			for i, b := range current.Blocks {
				if b.ID == block.ID {
					result, merged, mergeErr := mergeBlock(&b, &block, digest)
					if mergeErr != nil {
						return nil, mergeErr
					}
					switch result {
					case blockMergeConflict:
						return nil, newBlockConflict(current.ID, block.ID, "same block revision has different content",
							block.Revision, b.Revision, 0, event.BaseRevision, true, nil)
					case blockMergeApplied:
						block = *merged
						block.CreatedAt = b.CreatedAt
						block.UpdatedAt = event.CreatedAt
						current.Blocks[i] = block
					}
					found = true
					break
				}
			}
			if !found {
				block.CreatedAt = event.CreatedAt
				block.UpdatedAt = event.CreatedAt
				current.Blocks = append(current.Blocks, block)
			}
			// Keep placements sorted after block changes.
			if len(current.Placements) > 1 {
				current.Placements = sortPlacements(current.Placements)
			}

		case EventBlockRemoved:
			var payload blockRemovedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: unmarshal block remove: %w", err)
			}
			if payload.BlockID == "" {
				return nil, fmt.Errorf("work: block remove: blockId is required")
			}
			found := false
			for i, b := range current.Blocks {
				if b.ID == payload.BlockID {
					found = true
					tombstone := b
					tombstone.Tombstone = true
					if payload.Revision == 0 {
						// Events written before removal revisions were introduced
						// remain readable and keep the last known block revision.
						tombstone.UpdatedAt = event.CreatedAt
						current.Blocks[i] = tombstone
						break
					}
					tombstone.Revision = payload.Revision
					digest, digestErr := blockContentDigest(&tombstone)
					if digestErr != nil {
						return nil, digestErr
					}
					result, merged, mergeErr := mergeBlock(&b, &tombstone, digest)
					if mergeErr != nil {
						return nil, mergeErr
					}
					switch result {
					case blockMergeConflict:
						return nil, newBlockConflict(current.ID, payload.BlockID, "same block revision has different content",
							payload.Revision, b.Revision, 0, event.BaseRevision, true, nil)
					case blockMergeApplied:
						tombstone = *merged
						tombstone.CreatedAt = b.CreatedAt
						tombstone.UpdatedAt = event.CreatedAt
						current.Blocks[i] = tombstone
					}
					break
				}
			}
			if !found && payload.Revision > 0 {
				// Keep an out-of-order removal as a tombstone marker so a
				// subsequently replayed older upsert cannot revive the block.
				current.Blocks = append(current.Blocks, BlockInstance{
					ID:        payload.BlockID,
					Revision:  payload.Revision,
					Tombstone: true,
					CreatedAt: event.CreatedAt,
					UpdatedAt: event.CreatedAt,
				})
			}

		case EventRunStarted:
			var run WorkflowRun
			if err := json.Unmarshal(event.Payload, &run); err != nil {
				return nil, fmt.Errorf("work: unmarshal run started: %w", err)
			}
			found := false
			for i, r := range current.Runs {
				if r.ID == run.ID {
					current.Runs[i] = run
					found = true
					break
				}
			}
			if !found {
				current.Runs = append(current.Runs, run)
			}

		case EventStageChanged:
			var stage Stage
			if err := json.Unmarshal(event.Payload, &stage); err != nil {
				return nil, fmt.Errorf("work: unmarshal stage changed: %w", err)
			}
			for i := range current.Runs {
				for j := range current.Runs[i].Stages {
					if current.Runs[i].Stages[j].Name == stage.Name {
						current.Runs[i].Stages[j] = stage
						break
					}
				}
			}

		case EventTaskChanged:
			var task Task
			if err := json.Unmarshal(event.Payload, &task); err != nil {
				return nil, fmt.Errorf("work: unmarshal task changed: %w", err)
			}
			for i := range current.Runs {
				for j := range current.Runs[i].Stages {
					for k := range current.Runs[i].Stages[j].Tasks {
						if current.Runs[i].Stages[j].Tasks[k].Name == task.Name {
							current.Runs[i].Stages[j].Tasks[k] = task
						}
					}
				}
			}

		case EventAttemptChanged:
			var attempt Attempt
			if err := json.Unmarshal(event.Payload, &attempt); err != nil {
				return nil, fmt.Errorf("work: unmarshal attempt changed: %w", err)
			}
			for i := range current.Runs {
				for j := range current.Runs[i].Stages {
					for k := range current.Runs[i].Stages[j].Tasks {
						for l := range current.Runs[i].Stages[j].Tasks[k].Attempts {
							if current.Runs[i].Stages[j].Tasks[k].Attempts[l].Index == attempt.Index {
								current.Runs[i].Stages[j].Tasks[k].Attempts[l] = attempt
							}
						}
					}
				}
			}

		case EventCornerstoneUpserted:
			var cs Cornerstone
			if err := json.Unmarshal(event.Payload, &cs); err != nil {
				return nil, fmt.Errorf("work: unmarshal cornerstone upsert: %w", err)
			}
			found := false
			for i, c := range current.Cornerstones {
				if c.ID == cs.ID {
					current.Cornerstones[i] = cs
					found = true
					break
				}
			}
			if !found {
				current.Cornerstones = append(current.Cornerstones, cs)
			}

		case EventCornerstoneRemoved:
			var payload struct {
				CornerstoneID string `json:"cornerstoneId"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: unmarshal cornerstone remove: %w", err)
			}
			for i, c := range current.Cornerstones {
				if c.ID == payload.CornerstoneID {
					current.Cornerstones = append(current.Cornerstones[:i], current.Cornerstones[i+1:]...)
					break
				}
			}

		case EventConclusionUpserted:
			var c Conclusion
			if err := json.Unmarshal(event.Payload, &c); err != nil {
				return nil, fmt.Errorf("work: unmarshal conclusion upsert: %w", err)
			}
			found := false
			for i, existing := range current.Conclusions {
				if existing.ID == c.ID {
					current.Conclusions[i] = c
					found = true
					break
				}
			}
			if !found {
				current.Conclusions = append(current.Conclusions, c)
			}

		case EventArtifactLinked:
			// M0: artifact refs have no model destination in the Work projection.
			// Unmarshal and validate the payload but do not store — this is an
			// explicit no-op, not an oversight. Full artifact tracking is M1+.
			var art ArtifactRef
			if err := json.Unmarshal(event.Payload, &art); err != nil {
				return nil, fmt.Errorf("work: unmarshal artifact linked: %w", err)
			}
			// No state change — intentionally.

		case EventWorkArchived:
			if err := ValidateArchiveTransition(current.ArchiveState, ArchiveArchived); err != nil {
				return nil, err
			}
			current.ArchiveState = ArchiveArchived
			now := event.CreatedAt
			current.ArchivedAt = &now

		case EventWorkRestored:
			if err := ValidateArchiveTransition(current.ArchiveState, ArchiveActive); err != nil {
				return nil, err
			}
			current.ArchiveState = ArchiveActive
			current.ArchivedAt = nil

		case EventWorkDeleted:
			if err := ValidateArchiveTransition(current.ArchiveState, ArchiveDeleted); err != nil {
				return nil, err
			}
			current.ArchiveState = ArchiveDeleted
		}

		current.UpdatedAt = event.CreatedAt
		return current, nil
	}
}
