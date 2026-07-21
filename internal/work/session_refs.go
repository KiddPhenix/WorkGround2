package work

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

// ── Session owner/ref model ─────────────────────────────────────────────────

// OwnerType classifies what kind of entity owns a session reference.
type OwnerType string

const (
	OwnerWork   OwnerType = "work"
	OwnerBranch OwnerType = "branch"
)

// OwnerState tracks whether an owner's reference is active or trashed.
type OwnerState string

const (
	OwnerActive  OwnerState = "active"
	OwnerTrashed OwnerState = "trashed"
)

// SessionOwner identifies a single owner of a session reference.
type SessionOwner struct {
	OwnerType  OwnerType  `json:"ownerType"`
	OwnerID    string     `json:"ownerId"`
	ScopeID    string     `json:"scopeId,omitempty"`
	WorkID     string     `json:"workId,omitempty"`
	State      OwnerState `json:"state"`
	TrashedAt  int64      `json:"trashedAt,omitempty"`  // unix milli; zero when active
	RestoredAt int64      `json:"restoredAt,omitempty"` // unix milli
}

// SessionRefRecord is the per-session reverse-index entry tracking all
// owners. It is cache-like: always reconstructable from Work projections.
type SessionRefRecord struct {
	SessionPath string         `json:"sessionPath"`
	Owners      []SessionOwner `json:"owners"`
	CreatedAt   int64          `json:"createdAt"` // unix milli
	UpdatedAt   int64          `json:"updatedAt"` // unix milli
}

// ActiveOwners returns only non-trashed owners.
func (r *SessionRefRecord) ActiveOwners() []SessionOwner {
	var out []SessionOwner
	for _, o := range r.Owners {
		if o.State != OwnerTrashed {
			out = append(out, o)
		}
	}
	return out
}

// HasActive reports whether any owner is still active.
func (r *SessionRefRecord) HasActive() bool {
	for _, o := range r.Owners {
		if o.State != OwnerTrashed {
			return true
		}
	}
	return false
}

// ForcePurgeImpact describes the blast radius of force-purging a session.
type ForcePurgeImpact struct {
	SessionPath       string         `json:"sessionPath"`
	AffectedOwners    []SessionOwner `json:"affectedOwners"`
	AffectedWorkIDs   []string       `json:"affectedWorkIDs"`
	AffectedBranchIDs []string       `json:"affectedBranchIDs,omitempty"`
}

// CleanupPendingRecord describes one durable delayed-cleanup marker.
type CleanupPendingRecord struct {
	SessionPath string            `json:"sessionPath"`
	Reason      string            `json:"reason"`
	RequestID   string            `json:"requestId"`
	Stage       string            `json:"stage,omitempty"`    // "starting" | "purging" | "failed"
	Error       string            `json:"error,omitempty"`    // last failure reason
	Attempts    int               `json:"attempts,omitempty"` // retry count
	Impact      *ForcePurgeImpact `json:"impact,omitempty"`   // captured at start
	CreatedAt   int64             `json:"createdAt"`          // unix milli
	UpdatedAt   int64             `json:"updatedAt,omitempty"`
}

// BranchSessionRef pairs a branch ID with the session path it references.
// Used in WorkProjectionSummary so Branch owners attach to the correct
// SessionRef record rather than being stored as standalone session paths.
type BranchSessionRef struct {
	BranchID    string `json:"branchId"`
	SessionPath string `json:"sessionPath"`
}

// WorkProjectionSummary is the minimal projection data needed to rebuild
// the session-ref reverse index.
type WorkProjectionSummary struct {
	ScopeID      string             `json:"scopeId,omitempty"`
	WorkID       string             `json:"workId"`
	ArchiveState WorkArchiveState   `json:"archiveState"`
	TrashedAt    int64              `json:"trashedAt,omitempty"`
	SessionPaths []string           `json:"sessionPaths"`
	BranchRefs   []BranchSessionRef `json:"branchRefs,omitempty"`
}

// ── Narrow port — implemented by desktop or a fake ──────────────────────────

// SessionRefStore is the narrow port that desktop implements to persist
// session-owner references and cleanup-pending state. internal/work never
// depends on control, desktop, or any UI package.
type SessionRefStore interface {
	AcquireRef(sessionPath string, owner SessionOwner, requestID string) error
	ReleaseRef(sessionPath string, owner SessionOwner, requestID string) error
	TrashRef(sessionPath string, owner SessionOwner, requestID string) error
	RestoreRef(sessionPath string, owner SessionOwner, requestID string) error
	IsReferenced(sessionPath string) (bool, error)
	IsPurgeable(sessionPath string, sessionTrashedAt int64) (bool, error)
	ForcePurgeImpact(sessionPath string) (*ForcePurgeImpact, error)
	OwnerWorkIDs(sessionPath string) ([]string, error)
	RecordCleanupPending(sessionPath string, reason string, requestID string) error
	UpdateCleanupPending(sessionPath string, requestID string, stage string, errMsg string, impact *ForcePurgeImpact) error
	ClearCleanupPending(sessionPath string, requestID string) error
	ListCleanupPending() ([]CleanupPendingRecord, error)
	GetCleanupPending(sessionPath string, requestID string) (*CleanupPendingRecord, bool, error)
	RebuildFromProjections(works []WorkProjectionSummary) error
	RebuildScope(scopeID string, works []WorkProjectionSummary) error
}

// WorkTrashLister is an optional WorkStore capability used during reverse-index
// repair. Keeping it separate avoids expanding the lifecycle write port for
// stores that do not persist trash.
type WorkTrashLister interface {
	ListTrash() ([]WorkSummary, error)
}

// ── In-memory implementation — usable as a fake in tests ────────────────────

type memorySessionRefStore struct {
	mu        sync.RWMutex
	records   map[string]*SessionRefRecord
	cleanup   map[string]*CleanupPendingRecord // keyed by requestID
	clock     func() time.Time
	retention time.Duration
}

// NewMemorySessionRefStore returns an in-memory store for testing and fake
// injection. clock controls time for retention checks; nil uses time.Now.
func NewMemorySessionRefStore(opts ...MemoryStoreOption) SessionRefStore {
	s := &memorySessionRefStore{
		records:   make(map[string]*SessionRefRecord),
		cleanup:   make(map[string]*CleanupPendingRecord),
		clock:     time.Now,
		retention: 7 * 24 * time.Hour,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// MemoryStoreOption customises NewMemorySessionRefStore.
type MemoryStoreOption func(*memorySessionRefStore)

// WithClock sets a custom clock.
func WithClock(fn func() time.Time) MemoryStoreOption {
	return func(s *memorySessionRefStore) { s.clock = fn }
}

// WithRetention sets the grace period for zero-ref session cleanup.
func WithRetention(d time.Duration) MemoryStoreOption {
	return func(s *memorySessionRefStore) { s.retention = d }
}

func normSessionPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
	}
	return p
}

// SessionRefScopeID returns a stable opaque identity for one Work store. The
// persisted owner model never needs to expose the project directory itself.
func SessionRefScopeID(workDir string) string {
	sum := sha256.Sum256([]byte(normSessionPath(workDir)))
	return fmt.Sprintf("work-scope:%x", sum[:16])
}

// SessionPurgeRequestID derives an idempotency key without putting a Session
// path into logs, error text, or the cleanup request identifier.
func SessionPurgeRequestID(sessionPath string) string {
	sum := sha256.Sum256([]byte(normSessionPath(sessionPath)))
	return fmt.Sprintf("session-purge:%x", sum[:16])
}

func requireSessionRefRequest(requestID string) error {
	if strings.TrimSpace(requestID) == "" {
		return errors.New("requestID is required")
	}
	return nil
}

func validateOwner(owner SessionOwner) error {
	if normSessionPath(owner.OwnerID) == "" {
		return fmt.Errorf("owner ID is empty")
	}
	if owner.OwnerType != OwnerWork && owner.OwnerType != OwnerBranch {
		return fmt.Errorf("unknown owner type: %s", owner.OwnerType)
	}
	return nil
}

func sameOwner(a, b SessionOwner) bool {
	return a.OwnerType == b.OwnerType && a.OwnerID == b.OwnerID && a.ScopeID == b.ScopeID
}

func (s *memorySessionRefStore) AcquireRef(sessionPath string, owner SessionOwner, requestID string) error {
	p := normSessionPath(sessionPath)
	if p == "" {
		return fmt.Errorf("session path is empty")
	}
	if err := validateOwner(owner); err != nil {
		return fmt.Errorf("AcquireRef: %w", err)
	}
	if err := requireSessionRefRequest(requestID); err != nil {
		return fmt.Errorf("AcquireRef: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMillis()
	rec, ok := s.records[p]
	if !ok {
		rec = &SessionRefRecord{SessionPath: p, CreatedAt: now}
		s.records[p] = rec
	}
	for i, o := range rec.Owners {
		if sameOwner(o, owner) {
			owner.State = OwnerActive
			owner.TrashedAt = 0
			owner.RestoredAt = now
			rec.Owners[i] = owner
			rec.UpdatedAt = now
			return nil
		}
	}
	owner.State = OwnerActive
	rec.Owners = append(rec.Owners, owner)
	rec.UpdatedAt = now
	return nil
}

func (s *memorySessionRefStore) ReleaseRef(sessionPath string, owner SessionOwner, requestID string) error {
	p := normSessionPath(sessionPath)
	if p == "" {
		return nil
	}
	if err := validateOwner(owner); err != nil {
		return fmt.Errorf("ReleaseRef: %w", err)
	}
	if err := requireSessionRefRequest(requestID); err != nil {
		return fmt.Errorf("ReleaseRef: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMillis()
	rec, ok := s.records[p]
	if !ok {
		return nil
	}
	rec.Owners = slices.DeleteFunc(rec.Owners, func(o SessionOwner) bool {
		return sameOwner(o, owner)
	})
	rec.UpdatedAt = now
	return nil
}

func (s *memorySessionRefStore) TrashRef(sessionPath string, owner SessionOwner, requestID string) error {
	p := normSessionPath(sessionPath)
	if p == "" {
		return nil
	}
	if err := validateOwner(owner); err != nil {
		return fmt.Errorf("TrashRef: %w", err)
	}
	if err := requireSessionRefRequest(requestID); err != nil {
		return fmt.Errorf("TrashRef: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMillis()
	rec, ok := s.records[p]
	if !ok {
		rec = &SessionRefRecord{SessionPath: p, CreatedAt: now}
		s.records[p] = rec
	}
	for i, o := range rec.Owners {
		if sameOwner(o, owner) {
			owner.State = OwnerTrashed
			if o.State == OwnerTrashed && o.TrashedAt > 0 {
				owner.TrashedAt = o.TrashedAt
			} else {
				owner.TrashedAt = now
			}
			rec.Owners[i] = owner
			rec.UpdatedAt = now
			return nil
		}
	}
	owner.State = OwnerTrashed
	owner.TrashedAt = now
	rec.Owners = append(rec.Owners, owner)
	rec.UpdatedAt = now
	return nil
}

func (s *memorySessionRefStore) RestoreRef(sessionPath string, owner SessionOwner, requestID string) error {
	p := normSessionPath(sessionPath)
	if p == "" {
		return nil
	}
	if err := validateOwner(owner); err != nil {
		return fmt.Errorf("RestoreRef: %w", err)
	}
	if err := requireSessionRefRequest(requestID); err != nil {
		return fmt.Errorf("RestoreRef: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMillis()
	rec, ok := s.records[p]
	if !ok {
		rec = &SessionRefRecord{SessionPath: p, CreatedAt: now}
		s.records[p] = rec
	}
	for i, o := range rec.Owners {
		if sameOwner(o, owner) {
			owner.State = OwnerActive
			owner.TrashedAt = 0
			owner.RestoredAt = now
			rec.Owners[i] = owner
			rec.UpdatedAt = now
			return nil
		}
	}
	// Owner not found — add them fresh as active (reconcile).
	owner.State = OwnerActive
	rec.Owners = append(rec.Owners, owner)
	rec.UpdatedAt = now
	return nil
}

func (s *memorySessionRefStore) IsReferenced(sessionPath string) (bool, error) {
	p := normSessionPath(sessionPath)
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[p]
	if !ok {
		return false, nil
	}
	return rec.HasActive(), nil
}

func (s *memorySessionRefStore) IsPurgeable(sessionPath string, sessionTrashedAt int64) (bool, error) {
	p := normSessionPath(sessionPath)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p == "" {
		return false, errors.New("session path is empty")
	}
	now := s.nowMillis()
	if s.retention > 0 {
		if sessionTrashedAt <= 0 || now < sessionTrashedAt+s.retention.Milliseconds() {
			return false, nil
		}
	}
	rec, ok := s.records[p]
	if !ok {
		// Unknown session is safe only after the trash grace checked above.
		return true, nil
	}
	if rec.HasActive() {
		return false, nil
	}
	// All owners are trashed. Check retention: the most recent TrashedAt
	// must be older than the retention period.
	if s.retention <= 0 {
		return true, nil
	}
	cutoff := now - s.retention.Milliseconds()
	for _, o := range rec.Owners {
		if o.State == OwnerTrashed && o.TrashedAt > cutoff {
			return false, nil
		}
	}
	return true, nil
}

func (s *memorySessionRefStore) ForcePurgeImpact(sessionPath string) (*ForcePurgeImpact, error) {
	p := normSessionPath(sessionPath)
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.records[p]
	if !ok || len(rec.Owners) == 0 {
		return nil, nil
	}
	// Include ALL owners (active and trashed) in the impact.
	impact := &ForcePurgeImpact{SessionPath: p, AffectedOwners: slices.Clone(rec.Owners)}
	seenWork := map[string]bool{}
	seenBranch := map[string]bool{}
	for _, o := range rec.Owners {
		switch o.OwnerType {
		case OwnerWork:
			if !seenWork[o.OwnerID] {
				seenWork[o.OwnerID] = true
				impact.AffectedWorkIDs = append(impact.AffectedWorkIDs, o.OwnerID)
			}
		case OwnerBranch:
			if o.WorkID != "" && !seenWork[o.WorkID] {
				seenWork[o.WorkID] = true
				impact.AffectedWorkIDs = append(impact.AffectedWorkIDs, o.WorkID)
			}
			if !seenBranch[o.OwnerID] {
				seenBranch[o.OwnerID] = true
				impact.AffectedBranchIDs = append(impact.AffectedBranchIDs, o.OwnerID)
			}
		}
	}
	sort.Strings(impact.AffectedWorkIDs)
	sort.Strings(impact.AffectedBranchIDs)
	return impact, nil
}

func (s *memorySessionRefStore) OwnerWorkIDs(sessionPath string) ([]string, error) {
	impact, err := s.ForcePurgeImpact(sessionPath)
	if err != nil || impact == nil {
		return nil, err
	}
	return impact.AffectedWorkIDs, nil
}

func (s *memorySessionRefStore) RecordCleanupPending(sessionPath string, reason string, requestID string) error {
	key := strings.TrimSpace(requestID)
	if key == "" {
		return fmt.Errorf("requestID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, exists := s.cleanup[key]; exists {
		existing.Attempts++
		existing.UpdatedAt = s.nowMillis()
		return nil
	}
	s.cleanup[key] = &CleanupPendingRecord{
		SessionPath: normSessionPath(sessionPath),
		Reason:      reason,
		RequestID:   requestID,
		Stage:       "starting",
		CreatedAt:   s.nowMillis(),
	}
	return nil
}

func (s *memorySessionRefStore) UpdateCleanupPending(sessionPath string, requestID string, stage string, errMsg string, impact *ForcePurgeImpact) error {
	key := strings.TrimSpace(requestID)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.cleanup[key]
	if !ok {
		return nil
	}
	rec.Stage = stage
	rec.Error = errMsg
	if impact != nil {
		rec.Impact = impact
	}
	rec.UpdatedAt = s.nowMillis()
	return nil
}

func (s *memorySessionRefStore) ClearCleanupPending(sessionPath string, requestID string) error {
	key := strings.TrimSpace(requestID)
	if key == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cleanup, key)
	return nil
}

func (s *memorySessionRefStore) GetCleanupPending(sessionPath string, requestID string) (*CleanupPendingRecord, bool, error) {
	key := strings.TrimSpace(requestID)
	if key == "" {
		return nil, false, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	rec, ok := s.cleanup[key]
	if !ok {
		return nil, false, nil
	}
	copy := *rec
	copy.Impact = clonePurgeImpact(rec.Impact)
	return &copy, true, nil
}

func (s *memorySessionRefStore) ListCleanupPending() ([]CleanupPendingRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CleanupPendingRecord, 0, len(s.cleanup))
	for _, rec := range s.cleanup {
		copy := *rec
		copy.Impact = clonePurgeImpact(rec.Impact)
		out = append(out, copy)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

func clonePurgeImpact(impact *ForcePurgeImpact) *ForcePurgeImpact {
	if impact == nil {
		return nil
	}
	copy := *impact
	copy.AffectedOwners = slices.Clone(impact.AffectedOwners)
	copy.AffectedWorkIDs = slices.Clone(impact.AffectedWorkIDs)
	copy.AffectedBranchIDs = slices.Clone(impact.AffectedBranchIDs)
	return &copy
}

func (s *memorySessionRefStore) RebuildFromProjections(works []WorkProjectionSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = make(map[string]*SessionRefRecord)
	s.applyProjectionSummariesLocked(works)
	return nil
}

func (s *memorySessionRefStore) RebuildScope(scopeID string, works []WorkProjectionSummary) error {
	scopeID = strings.TrimSpace(scopeID)
	if scopeID == "" {
		return errors.New("scopeID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for path, rec := range s.records {
		rec.Owners = slices.DeleteFunc(rec.Owners, func(owner SessionOwner) bool {
			return owner.ScopeID == scopeID
		})
		if len(rec.Owners) == 0 {
			delete(s.records, path)
		}
	}
	scoped := slices.Clone(works)
	for i := range scoped {
		scoped[i].ScopeID = scopeID
	}
	s.applyProjectionSummariesLocked(scoped)
	return nil
}

func (s *memorySessionRefStore) applyProjectionSummariesLocked(works []WorkProjectionSummary) {
	now := s.nowMillis()
	for _, w := range works {
		state := OwnerActive
		trashedAt := int64(0)
		if w.ArchiveState == ArchiveDeleted {
			state = OwnerTrashed
			trashedAt = w.TrashedAt
			if trashedAt <= 0 {
				trashedAt = now
			}
		}
		for _, sp := range w.SessionPaths {
			sp = normSessionPath(sp)
			if sp == "" {
				continue
			}
			s.ensureRecordLocked(sp, now)
			s.upsertOwnerLocked(sp, SessionOwner{OwnerType: OwnerWork, OwnerID: w.WorkID, WorkID: w.WorkID, ScopeID: w.ScopeID, State: state, TrashedAt: trashedAt}, now)
		}
		for _, br := range w.BranchRefs {
			sp := normSessionPath(br.SessionPath)
			bid := strings.TrimSpace(br.BranchID)
			if sp == "" || bid == "" {
				continue
			}
			s.ensureRecordLocked(sp, now)
			s.upsertOwnerLocked(sp, SessionOwner{OwnerType: OwnerBranch, OwnerID: bid, WorkID: w.WorkID, ScopeID: w.ScopeID, State: state, TrashedAt: trashedAt}, now)
		}
	}
}

func (s *memorySessionRefStore) ensureRecordLocked(sp string, now int64) {
	if _, ok := s.records[sp]; !ok {
		s.records[sp] = &SessionRefRecord{SessionPath: sp, CreatedAt: now}
	}
}

func (s *memorySessionRefStore) upsertOwnerLocked(sp string, owner SessionOwner, now int64) {
	rec := s.records[sp]
	for i, o := range rec.Owners {
		if sameOwner(o, owner) {
			rec.Owners[i] = owner
			rec.UpdatedAt = now
			return
		}
	}
	rec.Owners = append(rec.Owners, owner)
	rec.UpdatedAt = now
}

func (s *memorySessionRefStore) nowMillis() int64 { return s.clock().UnixMilli() }

// ── Coordination helpers ────────────────────────────────────────────────────

// SessionRefCoordinator provides the coordination entry points that the Work
// Service calls when creating, archiving, copying, deleting, or restoring Work
// objects. Desktop-side session cleanup (trash/restore/purge) reads the same
// store.
type SessionRefCoordinator struct {
	store SessionRefStore
}

// NewSessionRefCoordinator creates a coordinator backed by store.
func NewSessionRefCoordinator(store SessionRefStore) *SessionRefCoordinator {
	return &SessionRefCoordinator{store: store}
}

// Store returns the underlying store.
func (c *SessionRefCoordinator) Store() SessionRefStore { return c.store }

// OnWorkCreated records session references for a newly created Work.
func (c *SessionRefCoordinator) OnWorkCreated(workID string, sessionPaths []string, requestID string) error {
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: workID, State: OwnerActive}
	for _, sp := range sessionPaths {
		if err := c.store.AcquireRef(sp, ow, requestID); err != nil {
			return fmt.Errorf("acquire ref for work %s session %s: %w", workID, sp, err)
		}
	}
	return nil
}

// OnWorkDeleted marks all session references for a Work as trashed with
// a grace timestamp. The owners are not removed — retention logic keeps them
// until the grace period expires and all are trashed.
func (c *SessionRefCoordinator) OnWorkDeleted(workID string, sessionPaths []string, requestID string) error {
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: workID}
	for _, sp := range sessionPaths {
		if err := c.store.TrashRef(sp, ow, requestID); err != nil {
			return fmt.Errorf("trash ref for work %s session %s: %w", workID, sp, err)
		}
	}
	return nil
}

// OnWorkRestored marks trashed session references back to active.
func (c *SessionRefCoordinator) OnWorkRestored(workID string, sessionPaths []string, requestID string) error {
	ow := SessionOwner{OwnerType: OwnerWork, OwnerID: workID}
	for _, sp := range sessionPaths {
		if err := c.store.RestoreRef(sp, ow, requestID); err != nil {
			return fmt.Errorf("restore ref for work %s session %s: %w", workID, sp, err)
		}
	}
	return nil
}

// OnWorkArchived keeps references intact (back side still needs session history).
func (c *SessionRefCoordinator) OnWorkArchived(workID string, sessionPaths []string, requestID string) error {
	return nil
}

// OnWorkCopied records session references for a copied Work.
func (c *SessionRefCoordinator) OnWorkCopied(workID string, sessionPaths []string, requestID string) error {
	return c.OnWorkCreated(workID, sessionPaths, requestID)
}

// ReconcileWork derives every Session owner/ref from the persisted Work
// projection. It is safe after a crash because it never reads UI state and all
// mutations are idempotent.
func (c *SessionRefCoordinator) ReconcileWork(scopeID string, value *Work, requestID string) error {
	if c == nil || c.store == nil {
		return errors.New("session ref coordinator is not configured")
	}
	if value == nil || strings.TrimSpace(value.ID) == "" {
		return errors.New("Work projection is required")
	}
	if err := requireSessionRefRequest(requestID); err != nil {
		return err
	}
	summary := WorkSessionRefSummary(scopeID, value)
	state := OwnerActive
	if value.ArchiveState == ArchiveDeleted {
		state = OwnerTrashed
	}
	apply := func(path string, owner SessionOwner, suffix string) error {
		owner.State = state
		owner.ScopeID = scopeID
		if owner.WorkID == "" {
			owner.WorkID = value.ID
		}
		if state == OwnerTrashed {
			return c.store.TrashRef(path, owner, requestID+suffix)
		}
		return c.store.AcquireRef(path, owner, requestID+suffix)
	}
	for _, path := range summary.SessionPaths {
		if err := apply(path, SessionOwner{OwnerType: OwnerWork, OwnerID: value.ID}, "/work"); err != nil {
			return err
		}
	}
	for _, ref := range summary.BranchRefs {
		if err := apply(ref.SessionPath, SessionOwner{OwnerType: OwnerBranch, OwnerID: ref.BranchID}, "/branch/"+ref.BranchID); err != nil {
			return err
		}
	}
	return nil
}

// WorkSessionRefSummary extracts stable owner context from one Work projection.
func WorkSessionRefSummary(scopeID string, value *Work) WorkProjectionSummary {
	summary := WorkProjectionSummary{ScopeID: strings.TrimSpace(scopeID)}
	if value == nil {
		return summary
	}
	summary.WorkID = value.ID
	summary.ArchiveState = value.ArchiveState
	if value.ArchiveState == ArchiveDeleted {
		summary.TrashedAt = value.UpdatedAt.UnixMilli()
	}
	paths := map[string]struct{}{}
	branches := map[string]BranchSessionRef{}
	for _, run := range value.Runs {
		for _, stage := range run.Stages {
			for _, task := range stage.Tasks {
				for _, attempt := range task.Attempts {
					path := normSessionPath(attempt.SessionRef.SessionPath)
					if path == "" {
						continue
					}
					paths[path] = struct{}{}
					branchID := strings.TrimSpace(attempt.SessionRef.BranchID)
					if branchID != "" {
						branches[path+"\x00"+branchID] = BranchSessionRef{SessionPath: path, BranchID: branchID}
					}
				}
			}
		}
	}
	for path := range paths {
		summary.SessionPaths = append(summary.SessionPaths, path)
	}
	for _, ref := range branches {
		summary.BranchRefs = append(summary.BranchRefs, ref)
	}
	sort.Strings(summary.SessionPaths)
	sort.Slice(summary.BranchRefs, func(i, j int) bool {
		if summary.BranchRefs[i].SessionPath == summary.BranchRefs[j].SessionPath {
			return summary.BranchRefs[i].BranchID < summary.BranchRefs[j].BranchID
		}
		return summary.BranchRefs[i].SessionPath < summary.BranchRefs[j].SessionPath
	})
	return summary
}

// ReconcileFromWorks is the bulk idempotent path that rebuilds the entire
// index from a set of Work projections (active + trash). It drops every
// existing record and reconstructs from the projections — Work events are
// the source of truth; this index is cache-like.
func (c *SessionRefCoordinator) ReconcileFromWorks(works []WorkProjectionSummary) error {
	return c.store.RebuildFromProjections(works)
}

// ReconcileScope replaces only one Work-store scope in a shared reverse index.
func (c *SessionRefCoordinator) ReconcileScope(scopeID string, works []WorkProjectionSummary) error {
	return c.store.RebuildScope(scopeID, works)
}

// CheckForcePurge validates whether a session can be force-purged. When force
// is false it returns an error if any active references exist; always returns
// the impact so the caller can display affected Work IDs.
func (c *SessionRefCoordinator) CheckForcePurge(sessionPath string, force bool) (*ForcePurgeImpact, error) {
	impact, err := c.store.ForcePurgeImpact(sessionPath)
	if err != nil {
		return nil, fmt.Errorf("check force purge %s: %w", sessionPath, err)
	}
	if impact == nil {
		return &ForcePurgeImpact{SessionPath: sessionPath}, nil
	}
	if !force {
		referenced, refErr := c.store.IsReferenced(sessionPath)
		if refErr != nil {
			return impact, refErr
		}
		if referenced {
			return impact, fmt.Errorf("session is referenced by Work(s): %s", strings.Join(impact.AffectedWorkIDs, ", "))
		}
	}
	return impact, nil
}

// RecordCleanupPending persists a cleanup-pending marker for retry/recovery.
func (c *SessionRefCoordinator) RecordCleanupPending(sessionPath string, reason string, requestID string) error {
	return c.store.RecordCleanupPending(sessionPath, reason, requestID)
}

// UpdateCleanupPending updates the stage/error of a cleanup-pending marker.
func (c *SessionRefCoordinator) UpdateCleanupPending(sessionPath string, requestID string, stage string, errMsg string, impact *ForcePurgeImpact) error {
	return c.store.UpdateCleanupPending(sessionPath, requestID, stage, errMsg, impact)
}

// ClearCleanupPending clears a cleanup-pending marker after recovery.
func (c *SessionRefCoordinator) ClearCleanupPending(sessionPath string, requestID string) error {
	return c.store.ClearCleanupPending(sessionPath, requestID)
}

// ListCleanupPending returns all active cleanup-pending records.
func (c *SessionRefCoordinator) ListCleanupPending() ([]CleanupPendingRecord, error) {
	return c.store.ListCleanupPending()
}

// ── Serialisation helpers ───────────────────────────────────────────────────

// SessionRefIndexSnapshot is a serialisable snapshot of the entire ref index.
type SessionRefIndexSnapshot struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Records       []SessionRefRecord     `json:"records"`
	Cleanup       []CleanupPendingRecord `json:"cleanupPending,omitempty"`
	GeneratedAt   int64                  `json:"generatedAt"`
}

// EncodeSessionRefSnapshot encodes records to a JSON snapshot.
func EncodeSessionRefSnapshot(records map[string]*SessionRefRecord, nowMillis int64) ([]byte, error) {
	snap := SessionRefIndexSnapshot{SchemaVersion: 1, GeneratedAt: nowMillis}
	for _, rec := range records {
		snap.Records = append(snap.Records, *rec)
	}
	sort.Slice(snap.Records, func(i, j int) bool {
		return snap.Records[i].SessionPath < snap.Records[j].SessionPath
	})
	return json.MarshalIndent(snap, "", "  ")
}

// DecodeSessionRefSnapshot decodes a JSON snapshot back into records.
func DecodeSessionRefSnapshot(data []byte) (map[string]*SessionRefRecord, error) {
	var snap SessionRefIndexSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode session ref snapshot: %w", err)
	}
	records := make(map[string]*SessionRefRecord, len(snap.Records))
	for i := range snap.Records {
		rec := snap.Records[i]
		records[rec.SessionPath] = &rec
	}
	return records, nil
}

// FileSessionRefStore persists the cache-like reverse index and cleanup ledger
// with atomic replacement. Work projections remain authoritative and can
// rebuild this file at any time.
type FileSessionRefStore struct {
	mu   sync.Mutex
	path string
	mem  *memorySessionRefStore
}

// NewFileSessionRefStore opens a process-shared Desktop index. Mutations are
// written before returning so a caller never performs physical purge after an
// unrecorded cleanup intent.
func NewFileSessionRefStore(path string, opts ...MemoryStoreOption) (*FileSessionRefStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("session ref index path is required")
	}
	mem := NewMemorySessionRefStore(opts...).(*memorySessionRefStore)
	store := &FileSessionRefStore{path: filepath.Clean(path), mem: mem}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return store, nil
		}
		return nil, fmt.Errorf("read session ref index: %w", err)
	}
	var snap SessionRefIndexSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("decode session ref index: %w", err)
	}
	if snap.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported session ref index schema %d", snap.SchemaVersion)
	}
	for i := range snap.Records {
		rec := snap.Records[i]
		rec.SessionPath = normSessionPath(rec.SessionPath)
		if rec.SessionPath == "" {
			return nil, errors.New("session ref index contains an empty path")
		}
		for _, owner := range rec.Owners {
			if err := validateOwner(owner); err != nil {
				return nil, fmt.Errorf("session ref index owner: %w", err)
			}
		}
		copy := rec
		copy.Owners = slices.Clone(rec.Owners)
		mem.records[rec.SessionPath] = &copy
	}
	for i := range snap.Cleanup {
		rec := snap.Cleanup[i]
		if strings.TrimSpace(rec.RequestID) == "" {
			return nil, errors.New("session ref index contains cleanup without requestID")
		}
		copy := rec
		copy.Impact = clonePurgeImpact(rec.Impact)
		mem.cleanup[strings.TrimSpace(rec.RequestID)] = &copy
	}
	return store, nil
}

func (s *FileSessionRefStore) persistLocked() error {
	s.mem.mu.RLock()
	snap := SessionRefIndexSnapshot{SchemaVersion: 1, GeneratedAt: s.mem.nowMillis()}
	for _, rec := range s.mem.records {
		copy := *rec
		copy.Owners = slices.Clone(rec.Owners)
		snap.Records = append(snap.Records, copy)
	}
	for _, rec := range s.mem.cleanup {
		copy := *rec
		copy.Impact = clonePurgeImpact(rec.Impact)
		snap.Cleanup = append(snap.Cleanup, copy)
	}
	s.mem.mu.RUnlock()
	sort.Slice(snap.Records, func(i, j int) bool { return snap.Records[i].SessionPath < snap.Records[j].SessionPath })
	sort.Slice(snap.Cleanup, func(i, j int) bool { return snap.Cleanup[i].RequestID < snap.Cleanup[j].RequestID })
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(s.path, data, 0o600)
}

func (s *FileSessionRefStore) mutate(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, cleanup := cloneSessionRefState(s.mem)
	if err := fn(); err != nil {
		return err
	}
	if err := s.persistLocked(); err != nil {
		restoreSessionRefState(s.mem, records, cleanup)
		return fmt.Errorf("persist session ref index: %w", err)
	}
	return nil
}

func cloneSessionRefState(mem *memorySessionRefStore) (map[string]*SessionRefRecord, map[string]*CleanupPendingRecord) {
	mem.mu.RLock()
	defer mem.mu.RUnlock()
	records := make(map[string]*SessionRefRecord, len(mem.records))
	for key, rec := range mem.records {
		copy := *rec
		copy.Owners = slices.Clone(rec.Owners)
		records[key] = &copy
	}
	cleanup := make(map[string]*CleanupPendingRecord, len(mem.cleanup))
	for key, rec := range mem.cleanup {
		copy := *rec
		copy.Impact = clonePurgeImpact(rec.Impact)
		cleanup[key] = &copy
	}
	return records, cleanup
}

func restoreSessionRefState(mem *memorySessionRefStore, records map[string]*SessionRefRecord, cleanup map[string]*CleanupPendingRecord) {
	mem.mu.Lock()
	mem.records = records
	mem.cleanup = cleanup
	mem.mu.Unlock()
}

func (s *FileSessionRefStore) AcquireRef(path string, owner SessionOwner, requestID string) error {
	return s.mutate(func() error { return s.mem.AcquireRef(path, owner, requestID) })
}
func (s *FileSessionRefStore) ReleaseRef(path string, owner SessionOwner, requestID string) error {
	return s.mutate(func() error { return s.mem.ReleaseRef(path, owner, requestID) })
}
func (s *FileSessionRefStore) TrashRef(path string, owner SessionOwner, requestID string) error {
	return s.mutate(func() error { return s.mem.TrashRef(path, owner, requestID) })
}
func (s *FileSessionRefStore) RestoreRef(path string, owner SessionOwner, requestID string) error {
	return s.mutate(func() error { return s.mem.RestoreRef(path, owner, requestID) })
}
func (s *FileSessionRefStore) IsReferenced(path string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.IsReferenced(path)
}
func (s *FileSessionRefStore) IsPurgeable(path string, trashedAt int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.IsPurgeable(path, trashedAt)
}
func (s *FileSessionRefStore) ForcePurgeImpact(path string) (*ForcePurgeImpact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.ForcePurgeImpact(path)
}
func (s *FileSessionRefStore) OwnerWorkIDs(path string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.OwnerWorkIDs(path)
}
func (s *FileSessionRefStore) RecordCleanupPending(path, reason, requestID string) error {
	return s.mutate(func() error { return s.mem.RecordCleanupPending(path, reason, requestID) })
}
func (s *FileSessionRefStore) UpdateCleanupPending(path, requestID, stage, errMsg string, impact *ForcePurgeImpact) error {
	return s.mutate(func() error { return s.mem.UpdateCleanupPending(path, requestID, stage, errMsg, impact) })
}
func (s *FileSessionRefStore) ClearCleanupPending(path, requestID string) error {
	return s.mutate(func() error { return s.mem.ClearCleanupPending(path, requestID) })
}
func (s *FileSessionRefStore) ListCleanupPending() ([]CleanupPendingRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.ListCleanupPending()
}
func (s *FileSessionRefStore) GetCleanupPending(path, requestID string) (*CleanupPendingRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mem.GetCleanupPending(path, requestID)
}
func (s *FileSessionRefStore) RebuildFromProjections(works []WorkProjectionSummary) error {
	return s.mutate(func() error { return s.mem.RebuildFromProjections(works) })
}
func (s *FileSessionRefStore) RebuildScope(scopeID string, works []WorkProjectionSummary) error {
	return s.mutate(func() error { return s.mem.RebuildScope(scopeID, works) })
}

var _ SessionRefStore = (*FileSessionRefStore)(nil)
