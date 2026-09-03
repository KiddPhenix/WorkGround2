package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"workground2/internal/agent"
)

const (
	sidebarBoltSchema    = 3
	sidebarBoltBatchSize = 256
	maxSidebarIssues     = 200
)

var (
	sidebarBoltMeta    = []byte("meta")
	sidebarBoltFiles   = []byte("files")
	sidebarBoltRows    = []byte("rows")
	sidebarBoltOrder   = []byte("order")
	sidebarBoltGroups  = []byte("groups")
	sidebarBoltQueries = []byte("queries")
	sidebarBoltIssues  = []byte("issues")
)

type sidebarBoltManifest struct {
	Signature       string `json:"signature"`
	TranscriptStamp string `json:"transcriptStamp,omitempty"`
	MetaStamp       string `json:"metaStamp,omitempty"`
	OrderKey        string `json:"orderKey"`
}

// sidebarIssueRecord is the persisted issue payload. Ownership records which
// sidebar mode the sidecar belonged to before it became unreadable, so a broken
// ROOM/Assistant sidecar only warns in its own view instead of leaking into
// Projects (or vice versa).
type sidebarIssueRecord struct {
	Code       string `json:"code"`
	Retryable  bool   `json:"retryable"`
	ObservedAt int64  `json:"observedAt"`
	Ownership  string `json:"ownership"` // "projects" | "rooms" | "assistants"
}

func (r sidebarIssueRecord) public() SidebarIssue {
	return SidebarIssue{Code: r.Code, Retryable: r.Retryable, ObservedAt: r.ObservedAt}
}

type sidebarBoltQuery struct {
	mu         sync.Mutex
	revision   uint64
	bucket     string
	component  string
	kind       string
	signature  string
	direct     bool
	generation string
	total      int
	plan       sidebarGroupPlan
	titles     map[string]string
	runtimes   map[string]sidebarRuntimeRow
}

type sidebarBoltAppState struct {
	queries       map[string]*sidebarBoltQuery
	queryVersions map[string][]*sidebarBoltQuery
	order         []string
	byRev         map[uint64]*sidebarBoltQuery
	nextRev       uint64
	dbPath        string
	db            *bolt.DB
	dbOpenMu      sync.Mutex
}

// sidebarBoltIndex is a disposable, persistent projection of BranchMeta
// sidecars. Session files remain authoritative; a corrupt or old schema is
// dropped and rebuilt without touching them.
type sidebarBoltIndex struct {
	mu                 sync.Mutex
	lifecycleMu        sync.Mutex
	lifecycles         map[string]*sync.Mutex
	locksMu            sync.Mutex
	syncLocks          map[string]*sync.Mutex
	dirtyMu            sync.Mutex
	dirty              map[string]uint64
	audited            map[string]time.Time
	states             map[*App]*sidebarBoltAppState
	path               func(*App) string
	source             sidebarPlanSource
	auditEvery         time.Duration
	now                func() time.Time
	view               func(*bolt.DB, func(*bolt.Tx) error) error
	update             func(*bolt.DB, func(*bolt.Tx) error) error
	routes             func() map[string]channelSessionRoute
	buildFault         func() error
	publishFault       func() error
	scanBatchFault     func() error
	loadBranchMeta     func(string) (agent.BranchMeta, bool, error)
	branchMetaBackoffs []time.Duration
	resetHook          func()
	maxBytes           int64
}

type sidebarPlanSource interface {
	plans(*App) ([]sidebarGroupPlan, error)
	stamp(*App, sidebarGroupPlan) string
}

var desktopSidebarBolt = newSidebarBoltIndex(func(*App) string {
	return filepath.Join(desktopConfigDir(), "sidebar-index-v1.db")
})

func newSidebarBoltIndex(path func(*App) string) *sidebarBoltIndex {
	return &sidebarBoltIndex{
		lifecycles: map[string]*sync.Mutex{}, syncLocks: map[string]*sync.Mutex{}, dirty: map[string]uint64{}, audited: map[string]time.Time{},
		states: map[*App]*sidebarBoltAppState{}, path: path, source: sidebarDiskIndexSource{}, auditEvery: 5 * time.Second, now: time.Now,
		view: sidebarBoltView, update: sidebarBoltUpdate, routes: autoBotChannelSessionRoutes, maxBytes: 512 << 20,
		loadBranchMeta: agent.LoadBranchMeta, branchMetaBackoffs: []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond},
	}
}

func sidebarBoltView(db *bolt.DB, fn func(*bolt.Tx) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if corruption := sidebarBoltPagePanic(recovered); corruption != nil {
				err = corruption
				return
			}
			panic(recovered)
		}
	}()
	return db.View(fn)
}

func sidebarBoltUpdate(db *bolt.DB, fn func(*bolt.Tx) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			if corruption := sidebarBoltPagePanic(recovered); corruption != nil {
				err = corruption
				return
			}
			panic(recovered)
		}
	}()
	return db.Update(fn)
}

func sidebarBoltPagePanic(recovered any) error {
	message := strings.ToLower(fmt.Sprint(recovered))
	if strings.Contains(message, "page") && (strings.Contains(message, "invalid") || strings.Contains(message, "checksum") || strings.Contains(message, "freed") || strings.Contains(message, "out of bounds")) {
		return fmt.Errorf("%w: bbolt page failure: %v", bolt.ErrChecksum, recovered)
	}
	return nil
}

func (index *sidebarBoltIndex) state(app *App) *sidebarBoltAppState {
	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.states[app]
	if state == nil {
		state = &sidebarBoltAppState{queries: map[string]*sidebarBoltQuery{}, queryVersions: map[string][]*sidebarBoltQuery{}, byRev: map[uint64]*sidebarBoltQuery{}, dbPath: index.path(app)}
		index.states[app] = state
	}
	return state
}

func (index *sidebarBoltIndex) open(app *App) (*sidebarBoltAppState, error) {
	path := filepath.Clean(index.path(app))
	lifecycle := index.lifecycle(path)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	return index.openLocked(app)
}

func (index *sidebarBoltIndex) openLocked(app *App) (*sidebarBoltAppState, error) {
	state := index.state(app)
	state.dbOpenMu.Lock()
	defer state.dbOpenMu.Unlock()
	if state.db != nil {
		return state, nil
	}
	if err := os.MkdirAll(filepath.Dir(state.dbPath), 0o755); err != nil {
		return nil, err
	}
	if index.maxBytes > 0 {
		if info, statErr := os.Stat(state.dbPath); statErr == nil && info.Size() > index.maxBytes {
			if removeErr := os.Remove(state.dbPath); removeErr != nil {
				return nil, fmt.Errorf("reset oversized sidebar index: %w", removeErr)
			}
		}
	}
	db, err := bolt.Open(state.dbPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if sidebarBoltCorrupt(err) {
		if removeErr := os.Remove(state.dbPath); removeErr != nil {
			return nil, fmt.Errorf("remove corrupt sidebar index: %w", removeErr)
		}
		db, err = bolt.Open(state.dbPath, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	}
	if err != nil {
		return nil, fmt.Errorf("open sidebar index: %w", err)
	}
	if err := index.update(db, func(tx *bolt.Tx) error { return initSidebarBolt(tx) }); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize sidebar index: %w", err)
	}
	state.db = db
	return state, nil
}

func (index *sidebarBoltIndex) lifecycle(path string) *sync.Mutex {
	index.lifecycleMu.Lock()
	defer index.lifecycleMu.Unlock()
	lock := index.lifecycles[path]
	if lock == nil {
		lock = &sync.Mutex{}
		index.lifecycles[path] = lock
	}
	return lock
}

func sidebarBoltCorrupt(err error) bool {
	return errors.Is(err, bolt.ErrInvalid) || errors.Is(err, bolt.ErrVersionMismatch) || errors.Is(err, bolt.ErrChecksum)
}

// errSidebarDerivedIndexCorrupt marks a semantically corrupt derived value (e.g.
// an issue record that no longer decodes). It triggers the same disposable-index
// reset as bolt page corruption.
var errSidebarDerivedIndexCorrupt = errors.New("sidebar derived index is corrupt")

func (index *sidebarBoltIndex) recoverError(app *App, err error) error {
	if !sidebarBoltCorrupt(err) && !errors.Is(err, errSidebarDerivedIndexCorrupt) {
		return err
	}
	path := filepath.Clean(index.path(app))
	lifecycle := index.lifecycle(path)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	index.mu.Lock()
	state := index.states[app]
	index.mu.Unlock()
	if state == nil {
		return fmt.Errorf("sidebar index is corrupt; retry: %w", err)
	}
	path = state.dbPath
	closeErr := index.closeLocked(app)
	if index.resetHook != nil {
		index.resetHook()
	}
	removeErr := os.Remove(path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if closeErr != nil || removeErr != nil {
		return fmt.Errorf("sidebar index is corrupt and reset failed: %w", errors.Join(err, closeErr, removeErr))
	}
	return fmt.Errorf("sidebar index was corrupt and has been reset; retry: %w", err)
}

func (index *sidebarBoltIndex) enforceCapacity(app *App) error {
	if index.maxBytes <= 0 {
		return nil
	}
	path := filepath.Clean(index.path(app))
	lifecycle := index.lifecycle(path)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	index.mu.Lock()
	state := index.states[app]
	index.mu.Unlock()
	if state == nil {
		return nil
	}
	info, err := os.Stat(state.dbPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect sidebar index size: %w", err)
	}
	if info.Size() <= index.maxBytes {
		return nil
	}
	size, path := info.Size(), state.dbPath
	closeErr := index.closeLocked(app)
	if index.resetHook != nil {
		index.resetHook()
	}
	removeErr := os.Remove(path)
	if os.IsNotExist(removeErr) {
		removeErr = nil
	}
	if closeErr != nil || removeErr != nil {
		return fmt.Errorf("sidebar index exceeded %d bytes and reset failed: %w", index.maxBytes, errors.Join(closeErr, removeErr))
	}
	return fmt.Errorf("sidebar index exceeded %d bytes (actual %d); derived index was reset; retry", index.maxBytes, size)
}

func initSidebarBolt(tx *bolt.Tx) error {
	meta, err := tx.CreateBucketIfNotExists(sidebarBoltMeta)
	if err != nil {
		return err
	}
	want := []byte(fmt.Sprint(sidebarBoltSchema))
	if current := meta.Get([]byte("schema")); current != nil && !bytes.Equal(current, want) {
		for _, name := range [][]byte{sidebarBoltMeta, sidebarBoltFiles, sidebarBoltRows, sidebarBoltOrder, sidebarBoltGroups, sidebarBoltQueries, sidebarBoltIssues} {
			_ = tx.DeleteBucket(name)
		}
		meta, err = tx.CreateBucket(sidebarBoltMeta)
		if err != nil {
			return err
		}
	}
	// Query cursors are process-local. Clearing their disposable buckets on
	// reopen also recovers any failed best-effort eviction from a prior run.
	_ = tx.DeleteBucket(sidebarBoltQueries)
	for _, name := range [][]byte{sidebarBoltFiles, sidebarBoltRows, sidebarBoltOrder, sidebarBoltGroups, sidebarBoltQueries, sidebarBoltIssues} {
		if _, err := tx.CreateBucketIfNotExists(name); err != nil {
			return err
		}
	}
	active := map[string]bool{}
	activeCursor := meta.Cursor()
	for key, value := activeCursor.Seek([]byte("active:")); key != nil && bytes.HasPrefix(key, []byte("active:")); key, value = activeCursor.Next() {
		active[string(value)] = true
	}
	groups := tx.Bucket(sidebarBoltGroups)
	groupCursor := groups.Cursor()
	stale := [][]byte{}
	for key, value := groupCursor.First(); key != nil; key, value = groupCursor.Next() {
		if value == nil && !active[string(key)] {
			stale = append(stale, append([]byte(nil), key...))
		}
	}
	for _, key := range stale {
		_ = groups.DeleteBucket(key)
	}
	return meta.Put([]byte("schema"), want)
}

func (index *sidebarBoltIndex) close(app *App) error {
	path := filepath.Clean(index.path(app))
	lifecycle := index.lifecycle(path)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	return index.closeLocked(app)
}

func (index *sidebarBoltIndex) closeLocked(app *App) error {
	index.mu.Lock()
	state := index.states[app]
	delete(index.states, app)
	index.mu.Unlock()
	if state == nil {
		return nil
	}
	state.dbOpenMu.Lock()
	defer state.dbOpenMu.Unlock()
	defer func() {
		prefix := fmt.Sprintf("%p\x00", app)
		index.locksMu.Lock()
		for key := range index.syncLocks {
			if strings.HasPrefix(key, prefix) {
				delete(index.syncLocks, key)
			}
		}
		index.locksMu.Unlock()
		index.dirtyMu.Lock()
		for key := range index.dirty {
			if strings.HasPrefix(key, prefix) {
				delete(index.dirty, key)
			}
		}
		for key := range index.audited {
			if strings.HasPrefix(key, prefix) {
				delete(index.audited, key)
			}
		}
		index.dirtyMu.Unlock()
	}()
	if state.db == nil {
		return nil
	}
	err := state.db.Close()
	state.db = nil
	return err
}

func (index *sidebarBoltIndex) listGroups(app *App, mode SidebarMode) (groups []SidebarGroup, err error) {
	defer func() { err = index.recoverError(app, err) }()
	defer func() {
		if err == nil {
			err = index.enforceCapacity(app)
		}
	}()
	plans, err := index.source.plans(app)
	if err != nil {
		return nil, err
	}
	if mode == SidebarProjects {
		groups := make([]SidebarGroup, len(plans))
		for i := range plans {
			groups[i] = plans[i].group
		}
		return groups, nil
	}
	if err := index.syncPlans(app, plans); err != nil {
		return nil, err
	}
	unlock := index.lockPlans(app, plans)
	defer unlock()
	stats, err := index.groupStats(app, plans, mode)
	if err != nil {
		return nil, err
	}
	groups = []SidebarGroup{}
	for _, plan := range plans {
		stat := stats[plan.group.ID]
		if stat.count == 0 {
			continue
		}
		group := plan.group
		group.SessionCount, group.LastActivityAt = stat.count, stat.last
		groups = append(groups, group)
	}
	return groups, nil
}

func (index *sidebarBoltIndex) listSessions(app *App, query SidebarSessionQuery) (page SidebarSessionPage, err error) {
	defer func() { err = index.recoverError(app, err) }()
	defer func() {
		if err == nil {
			err = index.enforceCapacity(app)
		}
	}()
	plans, err := index.source.plans(app)
	if err != nil {
		return SidebarSessionPage{}, err
	}
	selected := plans
	groupID := strings.TrimSpace(query.GroupID)
	if groupID != "" {
		selected = nil
		for _, plan := range plans {
			if plan.group.ID == groupID {
				selected = []sidebarGroupPlan{plan}
				break
			}
		}
	}
	syncSelected := selected
	componentPlans := selected
	if len(selected) == 1 && selected[0].crew {
		syncSelected = plans
		componentPlans = plans
	}
	if err := index.syncPlans(app, syncSelected); err != nil {
		return SidebarSessionPage{}, err
	}
	unlock := index.lockPlans(app, componentPlans)
	defer unlock()
	signature := sidebarQuerySignature("sessions", string(query.Mode), groupID)
	if sidebarDirectProjectQuery(selected, query.Mode, groupID) {
		q, cursor, err := index.resolveDirectQuery(app, query.Cursor, signature, selected[0])
		if err != nil {
			return SidebarSessionPage{}, err
		}
		return index.readDirectSessionPage(app, q, cursor, normalizeSidebarLimit(query.Limit))
	}
	q, cursor, err := index.resolveQuery(app, query.Cursor, "sessions", signature, plans, selected, componentPlans, query.Mode, "", "")
	if err != nil {
		return SidebarSessionPage{}, err
	}
	items, next, total, err := index.readSessionPage(app, q, cursor, normalizeSidebarLimit(query.Limit))
	if err != nil {
		return SidebarSessionPage{}, err
	}
	result := SidebarSessionPage{Items: items, Total: &total, Snapshot: sidebarSnapshotToken(app, q.revision)}
	if next != nil {
		result.NextCursor, err = encodeSidebarCursor(*next)
	}
	return result, err
}

func sidebarDirectProjectQuery(selected []sidebarGroupPlan, mode SidebarMode, groupID string) bool {
	return mode == SidebarProjects && groupID != "" && len(selected) == 1 && !selected[0].crew
}

func (index *sidebarBoltIndex) search(app *App, request SidebarSearchRequest) (page SidebarSearchPage, err error) {
	defer func() { err = index.recoverError(app, err) }()
	defer func() {
		if err == nil {
			err = index.enforceCapacity(app)
		}
	}()
	filter := normalizeSidebarFilter(request.Filter)
	if filter == "" {
		return SidebarSearchPage{}, fmt.Errorf("%w: %q", errInvalidSidebarFilter, request.Filter)
	}
	plans, err := index.source.plans(app)
	if err != nil {
		return SidebarSearchPage{}, err
	}
	if filter != "projects" {
		if err := index.syncPlans(app, plans); err != nil {
			return SidebarSearchPage{}, err
		}
	}
	unlock := index.lockPlans(app, plans)
	defer unlock()
	query := strings.TrimSpace(request.Query)
	signature := sidebarQuerySignature("search", filter, strings.ToLower(query))
	q, cursor, err := index.resolveQuery(app, request.Cursor, "search", signature, plans, plans, plans, SidebarProjects, query, filter)
	if err != nil {
		return SidebarSearchPage{}, err
	}
	items, next, total, err := index.readSearchPage(app, q, cursor, normalizeSidebarLimit(request.Limit))
	if err != nil {
		return SidebarSearchPage{}, err
	}
	result := SidebarSearchPage{Items: items, Total: &total, Snapshot: sidebarSnapshotToken(app, q.revision)}
	if next != nil {
		result.NextCursor, err = encodeSidebarCursor(*next)
	}
	return result, err
}

// listIssues returns the isolated sidecars visible in the given mode as a pure
// read of the derived index. Scanning is the responsibility of listGroups,
// listSessions and search; the frontend retries those to trigger a real re-scan.
func (index *sidebarBoltIndex) listIssues(app *App, mode SidebarMode) (issues []SidebarIssue, err error) {
	defer func() { err = index.recoverError(app, err) }()
	plans, err := index.source.plans(app)
	if err != nil {
		return nil, err
	}
	validGroups := make(map[string]bool, len(plans))
	for _, plan := range plans {
		validGroups[plan.group.ID] = true
	}
	state, err := index.open(app)
	if err != nil {
		return nil, err
	}
	err = index.view(state.db, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sidebarBoltIssues)
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if value == nil {
				continue
			}
			groupID := string(key)
			if sep := bytes.IndexByte(key, 0); sep >= 0 {
				groupID = string(key[:sep])
			}
			if !validGroups[groupID] {
				continue
			}
			var record sidebarIssueRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("%w: decode sidebar issue: %v", errSidebarDerivedIndexCorrupt, err)
			}
			if !sidebarIssueMatchesMode(record.Ownership, mode) {
				continue
			}
			issues = append(issues, record.public())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].ObservedAt > issues[j].ObservedAt })
	if len(issues) > maxSidebarIssues {
		issues = issues[:maxSidebarIssues]
	}
	return issues, nil
}

// sidebarIssueMatchesMode reports whether an issue owned by a given mode belongs
// to the requested view. Projects shows every issue (including unknown-new ones);
// ROOM and Assistant only show issues that were owned by those kinds before the
// sidecar became unreadable.
func sidebarIssueMatchesMode(ownership string, mode SidebarMode) bool {
	if mode == SidebarProjects {
		return true
	}
	want := string(SidebarRooms)
	if mode == SidebarAssistants {
		want = string(SidebarAssistants)
	}
	return ownership == want
}

// refreshIssues re-scans only the plans that currently own an issue in the given
// mode, then returns that mode's issues. It is the targeted counterpart of the
// pure-read listIssues: a stable bad signature keeps its issue, while a repaired
// or deleted sidecar (signature change) clears it without a full-library sync.
func (index *sidebarBoltIndex) refreshIssues(app *App, mode SidebarMode) (issues []SidebarIssue, err error) {
	defer func() { err = index.recoverError(app, err) }()
	plans, err := index.source.plans(app)
	if err != nil {
		return nil, err
	}
	planByID := make(map[string]sidebarGroupPlan, len(plans))
	for _, plan := range plans {
		planByID[plan.group.ID] = plan
	}
	state, err := index.open(app)
	if err != nil {
		return nil, err
	}
	affected := map[string]bool{}
	err = index.view(state.db, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sidebarBoltIssues)
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			if value == nil {
				continue
			}
			groupID := string(key)
			if sep := bytes.IndexByte(key, 0); sep >= 0 {
				groupID = string(key[:sep])
			}
			if _, ok := planByID[groupID]; !ok {
				continue
			}
			var record sidebarIssueRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return fmt.Errorf("%w: decode sidebar issue: %v", errSidebarDerivedIndexCorrupt, err)
			}
			if sidebarIssueMatchesMode(record.Ownership, mode) {
				affected[groupID] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	targeted := make([]sidebarGroupPlan, 0, len(affected))
	for _, plan := range plans {
		if affected[plan.group.ID] {
			targeted = append(targeted, plan)
		}
	}
	if len(targeted) > 0 {
		if err := index.syncPlans(app, targeted); err != nil {
			return nil, err
		}
	}
	return index.listIssues(app, mode)
}

func (index *sidebarBoltIndex) syncPlans(app *App, plans []sidebarGroupPlan) error {
	jobs := make(chan sidebarGroupPlan)
	errs := make(chan error, len(plans))
	var wait sync.WaitGroup
	workers := min(len(plans), maxSidebarLoadConcurrency)
	for range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for plan := range jobs {
				if plan.crew {
					continue
				}
				errs <- index.syncPlan(app, plan)
			}
		}()
	}
	for _, plan := range plans {
		jobs <- plan
	}
	close(jobs)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			return err
		}
	}
	return index.pruneOrphanIssues(app)
}

// pruneOrphanIssues deletes derived issues whose group is no longer in the
// current plan set, keeping the issue store bounded after projects are removed.
// It always recomputes the full plan set (not the syncPlans subset), so a
// single-group or targeted refresh never prunes other valid groups' issues.
func (index *sidebarBoltIndex) pruneOrphanIssues(app *App) error {
	plans, err := index.source.plans(app)
	if err != nil {
		return err
	}
	valid := make(map[string]bool, len(plans))
	for _, plan := range plans {
		valid[plan.group.ID] = true
	}
	state, err := index.open(app)
	if err != nil {
		return err
	}
	return index.update(state.db, func(tx *bolt.Tx) error {
		issues := tx.Bucket(sidebarBoltIssues)
		cursor := issues.Cursor()
		stale := [][]byte{}
		for key, _ := cursor.First(); key != nil; key, _ = cursor.Next() {
			groupID := string(key)
			if sep := bytes.IndexByte(key, 0); sep >= 0 {
				groupID = string(key[:sep])
			}
			if !valid[groupID] {
				stale = append(stale, append([]byte(nil), key...))
			}
		}
		for _, key := range stale {
			if err := issues.Delete(key); err != nil {
				return err
			}
		}
		return nil
	})
}

func (index *sidebarBoltIndex) planLock(app *App, groupID string) *sync.Mutex {
	key := fmt.Sprintf("%p\x00%s", app, groupID)
	index.locksMu.Lock()
	defer index.locksMu.Unlock()
	lock := index.syncLocks[key]
	if lock == nil {
		lock = &sync.Mutex{}
		index.syncLocks[key] = lock
	}
	return lock
}

func (index *sidebarBoltIndex) lockPlans(app *App, plans []sidebarGroupPlan) func() {
	ids := make([]string, 0, len(plans))
	seen := map[string]bool{}
	for _, plan := range plans {
		if plan.crew || seen[plan.group.ID] {
			continue
		}
		seen[plan.group.ID] = true
		ids = append(ids, plan.group.ID)
	}
	sort.Strings(ids)
	locks := make([]*sync.Mutex, 0, len(ids))
	for _, id := range ids {
		lock := index.planLock(app, id)
		lock.Lock()
		locks = append(locks, lock)
	}
	return func() {
		for i := len(locks) - 1; i >= 0; i-- {
			locks[i].Unlock()
		}
	}
}

func (index *sidebarBoltIndex) dirKey(app *App, dir string) string {
	return fmt.Sprintf("%p\x00%s", app, sessionRuntimeKey(dir))
}

func (index *sidebarBoltIndex) markDirty(app *App, path string) {
	dir := path
	if filepath.Ext(filepath.Base(path)) != "" {
		dir = filepath.Dir(path)
	}
	key := index.dirKey(app, dir)
	index.dirtyMu.Lock()
	index.dirty[key]++
	index.dirtyMu.Unlock()
}

func (index *sidebarBoltIndex) dirSignal(app *App, dir string) (uint64, bool) {
	key := index.dirKey(app, dir)
	index.dirtyMu.Lock()
	defer index.dirtyMu.Unlock()
	now := index.now()
	due := index.auditEvery <= 0 || index.audited[key].IsZero() || now.Sub(index.audited[key]) >= index.auditEvery
	return index.dirty[key], due
}

func (index *sidebarBoltIndex) markAudited(app *App, dir string) {
	key := index.dirKey(app, dir)
	index.dirtyMu.Lock()
	index.audited[key] = index.now()
	index.dirtyMu.Unlock()
}

func (index *sidebarBoltIndex) syncPlan(app *App, plan sidebarGroupPlan) error {
	// Crew is a virtual routing view over sessions indexed by their owning
	// global/project plans. Scanning its union of directories as another owner
	// would let an invisible Crew row delete the canonical project row.
	if plan.crew {
		return nil
	}
	lock := index.planLock(app, plan.group.ID)
	lock.Lock()
	defer lock.Unlock()
	state, err := index.open(app)
	if err != nil {
		return err
	}
	for _, dir := range plan.dirs {
		if err := index.syncDir(app, state.db, plan, dir); err != nil {
			return err
		}
	}
	return nil
}

type sidebarScannedFile struct {
	path            string
	signature       string
	transcriptStamp string
	metaStamp       string
	row             *SidebarSession
	issue           *sidebarIssueRecord
	changed         bool
}

func (index *sidebarBoltIndex) syncDir(app *App, db *bolt.DB, plan sidebarGroupPlan, dir string) error {
	dirty, audit := index.dirSignal(app, dir)
	quick := fmt.Sprintf("%s:%d", sidebarPathStamp(dir), dirty)
	stampKey := []byte("dir:" + plan.group.ID + "\x00" + dir)
	classKey := []byte("class:" + plan.group.ID)
	classStamp := index.source.stamp(app, plan)
	var priorQuick string
	var priorClass string
	var pending bool
	if err := index.view(db, func(tx *bolt.Tx) error {
		meta := tx.Bucket(sidebarBoltMeta)
		priorQuick = string(meta.Get(stampKey))
		priorClass = string(meta.Get(classKey))
		pending = len(meta.Get([]byte("pending:"+plan.group.ID))) > 0
		return nil
	}); err != nil {
		return err
	}
	if priorQuick == quick && priorClass == classStamp && !audit && !pending {
		return nil
	}
	for attempt := 0; attempt < 3; attempt++ {
		scanned, err := index.scanSidebarDir(plan, dir, db)
		if err != nil {
			return err
		}
		endDirty, _ := index.dirSignal(app, dir)
		if end := fmt.Sprintf("%s:%d", sidebarPathStamp(dir), endDirty); end != quick {
			quick = end
			continue
		}
		changed, err := index.applySidebarScan(db, plan.group.ID, dir, quick, classStamp, priorClass != classStamp, scanned)
		if err != nil {
			return fmt.Errorf("update sidebar index: %w", err)
		}
		if changed {
			if err := index.pruneGroupGenerations(app, db, plan.group.ID); err != nil {
				return fmt.Errorf("prune sidebar index generations: %w", err)
			}
		}
		index.markAudited(app, dir)
		return nil
	}
	return errors.New("session directory changed while indexing; retry")
}

func (index *sidebarBoltIndex) scanSidebarDir(plan sidebarGroupPlan, dir string, db *bolt.DB) ([]sidebarScannedFile, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return []sidebarScannedFile{}, nil
	}
	if err != nil {
		return nil, err
	}
	prior := map[string]sidebarBoltManifest{}
	prefix := plan.group.ID + "\x00" + dir + "\x00"
	if err := index.view(db, func(tx *bolt.Tx) error {
		cursor := tx.Bucket(sidebarBoltFiles).Cursor()
		for key, value := cursor.Seek([]byte(prefix)); key != nil && bytes.HasPrefix(key, []byte(prefix)); key, value = cursor.Next() {
			var manifest sidebarBoltManifest
			if json.Unmarshal(value, &manifest) == nil {
				prior[strings.TrimPrefix(string(key), prefix)] = manifest
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	result := make([]sidebarScannedFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		transcriptStamp := sidebarPathStamp(path)
		metaStamp := sidebarPathStamp(agent.BranchMetaPath(path))
		signature := transcriptStamp + ":" + metaStamp
		item := sidebarScannedFile{path: path, signature: signature, transcriptStamp: transcriptStamp, metaStamp: metaStamp}
		if old, ok := prior[path]; !ok || old.Signature != signature {
			item.changed = true
			transcriptOnlyChange := ok && old.TranscriptStamp != "" && old.TranscriptStamp != transcriptStamp && old.MetaStamp == metaStamp
			row, visible, err := index.sidebarRowFromSidecar(plan, path, transcriptOnlyChange)
			if err != nil {
				var decodeErr *agent.BranchMetaDecodeError
				if !errors.As(err, &decodeErr) {
					// Permission, stat, or ordinary I/O errors are not a damaged
					// sidecar: fail the scan so nothing is published.
					return nil, err
				}
				// Isolate this malformed sidecar by signature. The old row
				// projection (if any) is dropped and the scan continues over
				// healthy entries so one bad meta cannot reject the group.
				item.issue = index.sidebarIssueForMetaError(index.sidebarIssueOwnership(db, path))
			} else if visible {
				item.row = &row
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func applySidebarScan(db *bolt.DB, groupID, dir, quick string, scanned []sidebarScannedFile) (bool, error) {
	return applySidebarScanWithFaults(db, groupID, dir, quick, "", false, scanned, nil, nil, nil, autoBotChannelSessionRoutes())
}

func (index *sidebarBoltIndex) applySidebarScan(db *bolt.DB, groupID, dir, quick, classStamp string, force bool, scanned []sidebarScannedFile) (bool, error) {
	return applySidebarScanWithFaults(db, groupID, dir, quick, classStamp, force, scanned, index.buildFault, index.publishFault, index.scanBatchFault, index.routes())
}

func applySidebarScanWithFaults(db *bolt.DB, groupID, dir, quick, classStamp string, force bool, scanned []sidebarScannedFile, buildFault, publishFault, batchFault func() error, crewRoutes map[string]channelSessionRoute) (bool, error) {
	prefix := groupID + "\x00" + dir + "\x00"
	pendingKey := []byte("pending:" + groupID)
	seen := make(map[string]bool, len(scanned))
	for _, item := range scanned {
		seen[item.path] = true
	}
	deleted := []string{}
	pending := false
	if err := sidebarBoltView(db, func(tx *bolt.Tx) error {
		pending = len(tx.Bucket(sidebarBoltMeta).Get(pendingKey)) > 0
		cursor := tx.Bucket(sidebarBoltFiles).Cursor()
		for key, _ := cursor.Seek([]byte(prefix)); key != nil && bytes.HasPrefix(key, []byte(prefix)); key, _ = cursor.Next() {
			path := strings.TrimPrefix(string(key), prefix)
			if !seen[path] {
				deleted = append(deleted, path)
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	// Also clean up issues whose sidecar disappeared without ever persisting a
	// manifest (e.g. a scan crashed after a prior batch). Deleting a missing key
	// is a no-op, so deleted entries are safe to process uniformly below.
	if err := sidebarBoltView(db, func(tx *bolt.Tx) error {
		cursor := tx.Bucket(sidebarBoltIssues).Cursor()
		for key, _ := cursor.Seek([]byte(prefix)); key != nil && bytes.HasPrefix(key, []byte(prefix)); key, _ = cursor.Next() {
			path := strings.TrimPrefix(string(key), prefix)
			if !seen[path] {
				deleted = append(deleted, path)
			}
		}
		return nil
	}); err != nil {
		return false, err
	}
	updates := make([]sidebarScannedFile, 0, len(scanned))
	for _, item := range scanned {
		if item.changed || item.row != nil {
			updates = append(updates, item)
		}
	}
	changed := len(deleted) > 0 || len(updates) > 0
	if changed || force {
		// Persist intent before the first batched mutation. A crash or a failed
		// generation publish can then rebuild from rows even when manifests
		// already match on the retry.
		if err := sidebarBoltUpdate(db, func(tx *bolt.Tx) error {
			return tx.Bucket(sidebarBoltMeta).Put(pendingKey, []byte{1})
		}); err != nil {
			return false, err
		}
	}
	runBatch := func(deleted []string, scanned []sidebarScannedFile) error {
		if batchFault != nil {
			if err := batchFault(); err != nil {
				return err
			}
		}
		return applySidebarScanBatch(db, prefix, deleted, scanned)
	}
	for start := 0; start < len(deleted); start += sidebarBoltBatchSize {
		end := min(start+sidebarBoltBatchSize, len(deleted))
		if err := runBatch(deleted[start:end], nil); err != nil {
			return false, err
		}
	}
	for start := 0; start < len(updates); start += sidebarBoltBatchSize {
		batch := updates[start:min(start+sidebarBoltBatchSize, len(updates))]
		if err := runBatch(nil, batch); err != nil {
			return false, err
		}
	}
	count := 0
	generation := uint64(0)
	generationBucket := ""
	publish := changed || pending || force
	if publish {
		if err := sidebarBoltView(db, func(tx *bolt.Tx) error {
			generation = bytesToUint64(tx.Bucket(sidebarBoltMeta).Get([]byte("generation:"+groupID))) + 1
			return nil
		}); err != nil {
			return false, err
		}
		generationBucket = sidebarGroupGenerationBucket(groupID, generation)
		if buildFault != nil {
			if err := buildFault(); err != nil {
				return false, err
			}
		}
		var err error
		count, err = buildSidebarGroupGeneration(db, groupID, generationBucket, crewRoutes)
		if err != nil {
			return false, err
		}
	}
	err := sidebarBoltUpdate(db, func(tx *bolt.Tx) error {
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("dir:"+groupID+"\x00"+dir), []byte(quick)); err != nil {
			return err
		}
		if !publish {
			return nil
		}
		if publishFault != nil {
			if err := publishFault(); err != nil {
				return err
			}
		}
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("count:"+groupID), uint64Bytes(uint64(count))); err != nil {
			return err
		}
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("active:"+groupID), []byte(generationBucket)); err != nil {
			return err
		}
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("generation:"+groupID), uint64Bytes(generation)); err != nil {
			return err
		}
		if err := tx.Bucket(sidebarBoltMeta).Put([]byte("class:"+groupID), []byte(classStamp)); err != nil {
			return err
		}
		return tx.Bucket(sidebarBoltMeta).Delete(pendingKey)
	})
	return publish, err
}

func sidebarGroupGenerationBucket(groupID string, generation uint64) string {
	return fmt.Sprintf("%s%d", sidebarGroupGenerationPrefix(groupID), generation)
}

func sidebarGroupGenerationPrefix(groupID string) string {
	sum := sha256.Sum256([]byte(groupID))
	return fmt.Sprintf("g-%s-", hex.EncodeToString(sum[:8]))
}

func (index *sidebarBoltIndex) pruneGroupGenerations(app *App, db *bolt.DB, groupID string) error {
	keep := map[string]bool{}
	index.mu.Lock()
	if state := index.states[app]; state != nil {
		for _, query := range state.byRev {
			if query.direct && query.plan.group.ID == groupID && query.generation != "" {
				keep[query.generation] = true
			}
		}
	}
	index.mu.Unlock()
	return index.update(db, func(tx *bolt.Tx) error {
		meta, groups := tx.Bucket(sidebarBoltMeta), tx.Bucket(sidebarBoltGroups)
		keep[string(meta.Get([]byte("active:"+groupID)))] = true
		prefix := []byte(sidebarGroupGenerationPrefix(groupID))
		stale := [][]byte{}
		cursor := groups.Cursor()
		for key, value := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
			if value == nil && !keep[string(key)] {
				stale = append(stale, append([]byte(nil), key...))
			}
		}
		for _, key := range stale {
			if err := groups.DeleteBucket(key); err != nil {
				return err
			}
		}
		return nil
	})
}

func buildSidebarGroupGeneration(db *bolt.DB, groupID, bucketName string, crewRoutes map[string]channelSessionRoute) (int, error) {
	if err := sidebarBoltUpdate(db, func(tx *bolt.Tx) error {
		groups := tx.Bucket(sidebarBoltGroups)
		_ = groups.DeleteBucket([]byte(bucketName))
		_, err := groups.CreateBucket([]byte(bucketName))
		return err
	}); err != nil {
		return 0, err
	}
	prefix := []byte(groupID + "\x00")
	var after []byte
	total := 0
	for {
		type pair struct{ key, value []byte }
		batch := make([]pair, 0, sidebarBoltBatchSize)
		var lastScanned []byte
		exhausted := false
		if err := sidebarBoltView(db, func(tx *bolt.Tx) error {
			order, rows := tx.Bucket(sidebarBoltOrder), tx.Bucket(sidebarBoltRows)
			cursor := order.Cursor()
			key, path := cursor.Seek(prefix)
			if after != nil {
				key, path = cursor.Seek(after)
				if bytes.Equal(key, after) {
					key, path = cursor.Next()
				}
			}
			scanned := 0
			for ; key != nil && bytes.HasPrefix(key, prefix) && scanned < sidebarBoltBatchSize; key, path = cursor.Next() {
				scanned++
				lastScanned = append(lastScanned[:0], key...)
				encoded := rows.Get(path)
				if encoded == nil {
					continue
				}
				var row SidebarSession
				if json.Unmarshal(encoded, &row) != nil || sidebarRowBelongsToCrew(row, crewRoutes) {
					continue
				}
				batch = append(batch, pair{append([]byte(nil), key...), append([]byte(nil), encoded...)})
			}
			exhausted = key == nil || !bytes.HasPrefix(key, prefix)
			return nil
		}); err != nil {
			return 0, err
		}
		if len(batch) > 0 {
			if err := sidebarBoltUpdate(db, func(tx *bolt.Tx) error {
				bucket := tx.Bucket(sidebarBoltGroups).Bucket([]byte(bucketName))
				for _, item := range batch {
					if err := bucket.Put(item.key, item.value); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				return 0, err
			}
			total += len(batch)
		}
		if exhausted {
			return total, nil
		}
		after = append(after[:0], lastScanned...)
	}
}

// applySidebarScanBatch commits manifest, row, order and issue changes in a
// single Bolt transaction, so a batch either fully applies or fully rolls back.
// This guarantees a sidecar's manifest can never be updated without its issue
// (and vice versa) — a partial batch is retried from scratch on reopen.
func applySidebarScanBatch(db *bolt.DB, prefix string, deleted []string, scanned []sidebarScannedFile) error {
	return sidebarBoltUpdate(db, func(tx *bolt.Tx) error {
		files, rows, order := tx.Bucket(sidebarBoltFiles), tx.Bucket(sidebarBoltRows), tx.Bucket(sidebarBoltOrder)
		issues := tx.Bucket(sidebarBoltIssues)
		for _, path := range deleted {
			key := []byte(prefix + path)
			value := files.Get(key)
			var old sidebarBoltManifest
			_ = json.Unmarshal(value, &old)
			if orderKey, err := base64.RawURLEncoding.DecodeString(old.OrderKey); err == nil {
				_ = order.Delete(orderKey)
			}
			_ = rows.Delete([]byte(path))
			_ = files.Delete(key)
			_ = issues.Delete(key)
		}
		for _, item := range scanned {
			manifestKey := []byte(prefix + item.path)
			issueKey := manifestKey
			var old sidebarBoltManifest
			if value := files.Get(manifestKey); value != nil {
				_ = json.Unmarshal(value, &old)
			}
			if old.Signature == item.signature && item.issue == nil {
				continue
			}
			if old.OrderKey != "" {
				if orderKey, err := base64.RawURLEncoding.DecodeString(old.OrderKey); err == nil {
					_ = order.Delete(orderKey)
				}
			}
			_ = rows.Delete([]byte(item.path))
			manifest := sidebarBoltManifest{Signature: item.signature, TranscriptStamp: item.transcriptStamp, MetaStamp: item.metaStamp}
			if item.row != nil {
				encoded, err := json.Marshal(item.row)
				if err != nil {
					return err
				}
				orderKey := sidebarBoltOrderKey(item.row.GroupID, item.row.LastActivityAt, item.row.ID, item.path)
				manifest.OrderKey = base64.RawURLEncoding.EncodeToString(orderKey)
				if err := rows.Put([]byte(item.path), encoded); err != nil {
					return err
				}
				if err := order.Put(orderKey, []byte(item.path)); err != nil {
					return err
				}
			}
			encodedManifest, _ := json.Marshal(manifest)
			if err := files.Put(manifestKey, encodedManifest); err != nil {
				return err
			}
			if item.issue != nil {
				encodedIssue, err := json.Marshal(item.issue)
				if err != nil {
					return err
				}
				if err := issues.Put(issueKey, encodedIssue); err != nil {
					return err
				}
			} else {
				_ = issues.Delete(issueKey)
			}
		}
		return nil
	})
}

// loadSidebarBranchMeta retries only malformed-JSON decode corruption with a
// bounded short backoff. Permission, stat, and ordinary I/O errors are returned
// immediately so the scan fails rather than publishing a half-built generation.
func (index *sidebarBoltIndex) loadSidebarBranchMeta(path string) (agent.BranchMeta, bool, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		meta, ok, err := index.loadBranchMeta(path)
		if err == nil {
			return meta, ok, nil
		}
		lastErr = err
		var decodeErr *agent.BranchMetaDecodeError
		if !errors.As(err, &decodeErr) {
			return agent.BranchMeta{}, false, err
		}
		if attempt >= len(index.branchMetaBackoffs) {
			return agent.BranchMeta{}, false, lastErr
		}
		time.Sleep(index.branchMetaBackoffs[attempt])
	}
}

func (index *sidebarBoltIndex) sidebarIssueForMetaError(ownership string) *sidebarIssueRecord {
	if ownership == "" {
		ownership = "projects"
	}
	return &sidebarIssueRecord{Code: "meta_decode", Retryable: true, ObservedAt: index.now().UTC().UnixMilli(), Ownership: ownership}
}

// sidebarIssueOwnership derives which mode a broken sidecar belonged to from its
// last indexed row. A previously collaboration/assistant session keeps warning
// in ROOM/Assistant; a normal or never-indexed sidecar falls back to Projects.
func (index *sidebarBoltIndex) sidebarIssueOwnership(db *bolt.DB, path string) string {
	ownership := "projects"
	_ = index.view(db, func(tx *bolt.Tx) error {
		encoded := tx.Bucket(sidebarBoltRows).Get([]byte(path))
		if encoded == nil {
			return nil
		}
		var row SidebarSession
		if json.Unmarshal(encoded, &row) != nil {
			return nil
		}
		switch sidebarExplicitSessionKind(row.SessionKind, row.SessionSource) {
		case "collaboration":
			ownership = "rooms"
		case "assistant":
			ownership = "assistants"
		}
		return nil
	})
	return ownership
}

func (index *sidebarBoltIndex) sidebarRowFromSidecar(plan sidebarGroupPlan, path string, refreshTranscript bool) (SidebarSession, bool, error) {
	meta, ok, err := index.loadSidebarBranchMeta(path)
	if err != nil {
		return SidebarSession{}, false, err
	}
	if !ok {
		when := time.Now().UTC()
		if info, statErr := os.Stat(path); statErr == nil {
			when = info.ModTime().UTC()
		} else if !os.IsNotExist(statErr) {
			return SidebarSession{}, false, statErr
		}
		meta = agent.BranchMeta{ID: agent.BranchID(path), Scope: plan.scope, WorkspaceRoot: plan.root, CreatedAt: when, UpdatedAt: when}
	}
	if !sidebarMetaMatchesPlan(meta, plan) || isWorkTaskSessionSource(meta.SessionSource) {
		return SidebarSession{}, false, nil
	}
	if meta.SchemaVersion < agent.BranchMetaCountsVersion || refreshTranscript {
		preview, turns := agent.SessionPreview(path)
		meta.Preview, meta.Turns = preview, turns
	}
	kind := sidebarExplicitSessionKind(string(meta.SessionKind), meta.SessionSource)
	if kind == "" {
		kind = "normal"
	}
	if meta.Turns == 0 && kind != "collaboration" && kind != "assistant" && kind != "work" {
		return SidebarSession{}, false, nil
	}
	createdAt := unixMilliOrZero(meta.CreatedAt)
	lastActivityAt := unixMilliOrZero(meta.UpdatedAt)
	if info, statErr := os.Stat(path); statErr == nil {
		lastActivityAt = max(lastActivityAt, info.ModTime().UnixMilli())
	}
	if lastActivityAt == 0 {
		lastActivityAt = createdAt
	}
	id := strings.TrimSpace(meta.ID)
	if id == "" {
		id = string(agent.BranchID(path))
	}
	title := firstNonEmpty(strings.TrimSpace(meta.CustomTitle), strings.TrimSpace(meta.TopicTitle), topicTitleFromText(meta.Preview), defaultTopicTitle)
	return SidebarSession{
		ID: id, GroupID: plan.group.ID, Scope: plan.scope, WorkspaceRoot: plan.root, Title: title,
		SessionPath: path, TopicID: meta.TopicID, SessionSource: meta.SessionSource, Channel: meta.Channel, ChannelLabel: meta.ChannelLabel,
		SessionKind: kind, Turns: meta.Turns, Pinned: meta.Pinned, CreatedAt: createdAt, LastActivityAt: lastActivityAt,
	}, true, nil
}

func sidebarMetaMatchesPlan(meta agent.BranchMeta, plan sidebarGroupPlan) bool {
	scope := strings.TrimSpace(meta.Scope)
	if scope == "" {
		scope = "global"
	}
	if plan.scope == "global" {
		return scope == "global"
	}
	return scope == "project" && normalizeProjectRoot(meta.WorkspaceRoot) == plan.root
}

func (index *sidebarBoltIndex) resolveDirectQuery(app *App, encoded, signature string, plan sidebarGroupPlan) (*sidebarBoltQuery, *sidebarCursor, error) {
	state, err := index.open(app)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(encoded) != "" {
		cursor, err := decodeSidebarCursor(encoded)
		if err != nil || cursor.Kind != "sessions" || cursor.Signature != signature || !validSidebarLastKey(cursor.LastKey) {
			return nil, nil, errInvalidSidebarCursor
		}
		index.mu.Lock()
		q := state.byRev[cursor.Version]
		index.mu.Unlock()
		if q == nil || !q.direct || q.kind != cursor.Kind || q.signature != cursor.Signature {
			return nil, nil, errInvalidSidebarCursor
		}
		return q, &cursor, nil
	}
	component, err := index.queryComponent(app, state.db, []sidebarGroupPlan{plan})
	if err != nil {
		return nil, nil, err
	}
	key := "sessions\x00" + signature
	index.mu.Lock()
	if cached := state.queries[key]; cached != nil && cached.direct && cached.component == component {
		state.order = touchSidebarQuery(state.order, key)
		index.mu.Unlock()
		return cached, nil, nil
	}
	state.nextRev++
	revision := state.nextRev
	index.mu.Unlock()
	generation := ""
	total := 0
	if err := index.view(state.db, func(tx *bolt.Tx) error {
		meta := tx.Bucket(sidebarBoltMeta)
		generation = string(meta.Get([]byte("active:" + plan.group.ID)))
		total = int(bytesToUint64(meta.Get([]byte("count:" + plan.group.ID))))
		return nil
	}); err != nil {
		return nil, nil, err
	}
	titles := map[string]string{}
	for _, dir := range plan.dirs {
		for name, title := range loadSessionTitles(dir) {
			titles[sessionRuntimeKey(filepath.Join(dir, name))] = title
		}
	}
	q := &sidebarBoltQuery{
		revision: revision, component: component, kind: "sessions", signature: signature, direct: true, generation: generation, total: total, plan: plan,
		titles: titles, runtimes: sidebarRuntimeByTopic(app, plan),
	}
	if err := index.rememberQuery(state, key, q); err != nil {
		return nil, nil, err
	}
	return q, nil, nil
}

func (index *sidebarBoltIndex) readDirectSessionPage(app *App, q *sidebarBoltQuery, cursor *sidebarCursor, limit int) (SidebarSessionPage, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	prefix := []byte(q.plan.group.ID + "\x00")
	items := make([]SidebarSession, 0, limit+1)
	keys := make([][]byte, 0, limit+1)
	state, err := index.open(app)
	if err != nil {
		return SidebarSessionPage{}, err
	}
	if q.generation != "" {
		err = index.view(state.db, func(tx *bolt.Tx) error {
			bucket := tx.Bucket(sidebarBoltGroups).Bucket([]byte(q.generation))
			if bucket == nil {
				return errInvalidSidebarCursor
			}
			c := bucket.Cursor()
			var key, encoded []byte
			if cursor != nil {
				last, decodeErr := base64.RawURLEncoding.DecodeString(cursor.LastKey)
				if decodeErr != nil || !bytes.HasPrefix(last, prefix) {
					return errInvalidSidebarCursor
				}
				key, encoded = c.Seek(last)
				if !bytes.Equal(key, last) {
					return errInvalidSidebarCursor
				}
				key, encoded = c.Next()
			} else {
				key, encoded = c.Seek(prefix)
			}
			for ; key != nil && bytes.HasPrefix(key, prefix) && len(items) <= limit; key, encoded = c.Next() {
				var row SidebarSession
				if json.Unmarshal(encoded, &row) != nil {
					continue
				}
				decorateSidebarBoltRow(&row, q.plan, q.titles, q.runtimes)
				row.Revision = int64(q.revision)
				items = append(items, row)
				keys = append(keys, append([]byte(nil), key...))
			}
			return nil
		})
		if err != nil {
			return SidebarSessionPage{}, err
		}
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
		keys = keys[:limit]
	}
	total := q.total
	result := SidebarSessionPage{Items: items, Total: &total, Snapshot: sidebarSnapshotToken(app, q.revision)}
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		next := sidebarCursor{Version: q.revision, Kind: q.kind, Signature: q.signature, LastActivityAt: last.LastActivityAt, LastID: last.ID, LastKey: base64.RawURLEncoding.EncodeToString(keys[len(keys)-1])}
		encoded, err := encodeSidebarCursor(next)
		if err != nil {
			return SidebarSessionPage{}, err
		}
		result.NextCursor = encoded
	}
	return result, nil
}

func (index *sidebarBoltIndex) resolveQuery(app *App, encoded, kind, signature string, allPlans, selected, componentPlans []sidebarGroupPlan, mode SidebarMode, search, filter string) (*sidebarBoltQuery, *sidebarCursor, error) {
	state, err := index.open(app)
	if err != nil {
		return nil, nil, err
	}
	key := kind + "\x00" + signature
	if strings.TrimSpace(encoded) != "" {
		cursor, err := decodeSidebarCursor(encoded)
		if err != nil || cursor.Kind != kind || cursor.Signature != signature || !validSidebarLastKey(cursor.LastKey) {
			return nil, nil, errInvalidSidebarCursor
		}
		index.mu.Lock()
		q := state.byRev[cursor.Version]
		index.mu.Unlock()
		if q == nil || q.kind != cursor.Kind || q.signature != cursor.Signature {
			return nil, nil, errInvalidSidebarCursor
		}
		return q, &cursor, nil
	}
	queryLock := index.planLock(app, "query:"+key)
	queryLock.Lock()
	defer queryLock.Unlock()
	component, err := index.queryComponent(app, state.db, componentPlans)
	if err != nil {
		return nil, nil, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		index.mu.Lock()
		if cached := state.queries[key]; cached != nil && cached.component == component {
			state.order = touchSidebarQuery(state.order, key)
			index.mu.Unlock()
			return cached, nil, nil
		}
		state.nextRev++
		revision := state.nextRev
		index.mu.Unlock()
		q := &sidebarBoltQuery{revision: revision, bucket: fmt.Sprintf("q-%d", revision), component: component, kind: kind, signature: signature}
		if err := index.buildQueryBucket(app, state.db, q, allPlans, selected, mode, search, filter); err != nil {
			return nil, nil, err
		}
		after, err := index.queryComponent(app, state.db, componentPlans)
		if err != nil {
			return nil, nil, err
		}
		if after != component {
			_ = index.update(state.db, func(tx *bolt.Tx) error { return tx.Bucket(sidebarBoltQueries).DeleteBucket([]byte(q.bucket)) })
			component = after
			continue
		}
		if err := index.rememberQuery(state, key, q); err != nil {
			return nil, nil, err
		}
		return q, nil, nil
	}
	return nil, nil, errors.New("sidebar index changed while building query; retry")
}

func validSidebarLastKey(encoded string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(encoded))
	return err == nil && len(decoded) > 0
}

func (index *sidebarBoltIndex) queryComponent(app *App, db *bolt.DB, plans []sidebarGroupPlan) (string, error) {
	hash := sha256.New()
	err := index.view(db, func(tx *bolt.Tx) error {
		meta := tx.Bucket(sidebarBoltMeta)
		for _, plan := range plans {
			fmt.Fprintf(hash, "%s:%d:%s\x00", plan.group.ID, bytesToUint64(meta.Get([]byte("generation:"+plan.group.ID))), index.source.stamp(app, plan))
		}
		return nil
	})
	return hex.EncodeToString(hash.Sum(nil)[:12]), err
}

func (index *sidebarBoltIndex) rememberQuery(state *sidebarBoltAppState, key string, q *sidebarBoltQuery) error {
	index.mu.Lock()
	deleteBuckets := []string{}
	deleteGenerations := map[string]string{}
	state.queries[key], state.byRev[q.revision] = q, q
	versions := append(state.queryVersions[key], q)
	for len(versions) > maxSidebarQueryRevisions {
		old := versions[0]
		versions = versions[1:]
		delete(state.byRev, old.revision)
		if old.bucket != "" {
			deleteBuckets = append(deleteBuckets, old.bucket)
		}
		if old.generation != "" {
			deleteGenerations[old.generation] = old.plan.group.ID
		}
	}
	state.queryVersions[key] = versions
	state.order = touchSidebarQuery(state.order, key)
	for len(state.order) > maxSidebarQueryCache {
		evicted := state.order[0]
		state.order = state.order[1:]
		delete(state.queries, evicted)
		for _, old := range state.queryVersions[evicted] {
			delete(state.byRev, old.revision)
			if old.bucket != "" {
				deleteBuckets = append(deleteBuckets, old.bucket)
			}
			if old.generation != "" {
				deleteGenerations[old.generation] = old.plan.group.ID
			}
		}
		delete(state.queryVersions, evicted)
	}
	keptGenerations := map[string]bool{}
	for _, current := range state.byRev {
		if current.generation != "" {
			keptGenerations[current.generation] = true
		}
	}
	index.mu.Unlock()
	if len(deleteBuckets) == 0 && len(deleteGenerations) == 0 {
		return nil
	}
	return index.update(state.db, func(tx *bolt.Tx) error {
		queries := tx.Bucket(sidebarBoltQueries)
		for _, bucket := range deleteBuckets {
			if err := queries.DeleteBucket([]byte(bucket)); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
				return err
			}
		}
		groups, meta := tx.Bucket(sidebarBoltGroups), tx.Bucket(sidebarBoltMeta)
		for bucket, groupID := range deleteGenerations {
			if keptGenerations[bucket] || string(meta.Get([]byte("active:"+groupID))) == bucket {
				continue
			}
			if err := groups.DeleteBucket([]byte(bucket)); err != nil && !errors.Is(err, bolt.ErrBucketNotFound) {
				return err
			}
		}
		return nil
	})
}

func touchSidebarQuery(order []string, key string) []string {
	out := make([]string, 0, len(order)+1)
	for _, current := range order {
		if current != key {
			out = append(out, current)
		}
	}
	return append(out, key)
}

func (index *sidebarBoltIndex) buildQueryBucket(app *App, db *bolt.DB, q *sidebarBoltQuery, allPlans, selected []sidebarGroupPlan, mode SidebarMode, search, filter string) error {
	groups := make(map[string]sidebarGroupPlan, len(allPlans))
	crewRoutes := index.routes()
	selectedIDs := map[string]bool{}
	sessionTitles := map[string]string{}
	runtimes := map[string]map[string]sidebarRuntimeRow{}
	for _, plan := range allPlans {
		groups[plan.group.ID] = plan
		if !plan.crew {
			runtimes[plan.group.ID] = sidebarRuntimeByTopic(app, plan)
		}
		for _, dir := range plan.dirs {
			for name, title := range loadSessionTitles(dir) {
				sessionTitles[sessionRuntimeKey(filepath.Join(dir, name))] = title
			}
		}
	}
	for _, plan := range selected {
		selectedIDs[plan.group.ID] = true
	}
	needle := strings.ToLower(strings.TrimSpace(search))
	if err := index.update(db, func(tx *bolt.Tx) error {
		queries := tx.Bucket(sidebarBoltQueries)
		_ = queries.DeleteBucket([]byte(q.bucket))
		_, err := queries.CreateBucket([]byte(q.bucket))
		return err
	}); err != nil {
		return err
	}

	type result struct {
		key, value []byte
	}
	total := 0
	put := func(batch []result) error {
		if len(batch) == 0 {
			return nil
		}
		return index.update(db, func(tx *bolt.Tx) error {
			bucket := tx.Bucket(sidebarBoltQueries).Bucket([]byte(q.bucket))
			if bucket == nil {
				return errInvalidSidebarCursor
			}
			for _, item := range batch {
				if err := bucket.Put(item.key, item.value); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if q.kind == "search" && needle != "" && filter != "sessions" {
		batch := make([]result, 0, sidebarBoltBatchSize)
		for _, plan := range allPlans {
			group := plan.group
			if !sidebarContains(needle, group.Label, group.Root) {
				continue
			}
			item := SidebarSearchItem{Kind: "project", ID: "project:" + group.ID, Group: &group, LastActivityAt: group.LastActivityAt}
			encoded, err := json.Marshal(item)
			if err != nil {
				return err
			}
			batch = append(batch, result{sidebarBoltResultKey(item.LastActivityAt, item.ID, item.ID), encoded})
			total++
			if len(batch) == sidebarBoltBatchSize {
				if err := put(batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		if err := put(batch); err != nil {
			return err
		}
	}
	if q.kind != "search" || filter != "projects" {
		var after []byte
		for {
			batch := make([]result, 0, sidebarBoltBatchSize)
			var lastKey []byte
			exhausted := false
			err := index.view(db, func(tx *bolt.Tx) error {
				rows, order := tx.Bucket(sidebarBoltRows), tx.Bucket(sidebarBoltOrder)
				cursor := order.Cursor()
				key, path := cursor.First()
				if after != nil {
					key, path = cursor.Seek(after)
					if bytes.Equal(key, after) {
						key, path = cursor.Next()
					}
				}
				scanned := 0
				for ; key != nil && scanned < sidebarBoltBatchSize; key, path = cursor.Next() {
					scanned++
					lastKey = append(lastKey[:0], key...)
					encodedRow := rows.Get(path)
					if encodedRow == nil {
						continue
					}
					var row SidebarSession
					if json.Unmarshal(encodedRow, &row) != nil {
						continue
					}
					plan := sidebarEffectivePlan(row, groups, crewRoutes)
					if plan.group.ID == "" || !selectedIDs[plan.group.ID] {
						continue
					}
					decorateSidebarBoltRow(&row, plan, sessionTitles, runtimes[plan.group.ID])
					if !sidebarModeMatches(row, mode) || (needle != "" && !sidebarContains(needle, row.Title, row.SessionPath, plan.group.Label, plan.group.Root)) {
						continue
					}
					resultKey := sidebarBoltResultKey(row.LastActivityAt, row.ID, row.SessionPath)
					if q.kind == "search" {
						group := plan.group
						item := SidebarSearchItem{Kind: "session", ID: "session:" + row.ID, Group: &group, Session: &row, LastActivityAt: row.LastActivityAt}
						encoded, err := json.Marshal(item)
						if err != nil {
							return err
						}
						batch = append(batch, result{resultKey, encoded})
					} else {
						encoded, err := json.Marshal(row)
						if err != nil {
							return err
						}
						batch = append(batch, result{resultKey, encoded})
					}
				}
				exhausted = key == nil
				return nil
			})
			if err != nil {
				return err
			}
			if err := put(batch); err != nil {
				return err
			}
			total += len(batch)
			if exhausted {
				break
			}
			after = append(after[:0], lastKey...)
		}
	}
	return index.update(db, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sidebarBoltQueries).Bucket([]byte(q.bucket))
		if bucket == nil {
			return errInvalidSidebarCursor
		}
		return bucket.Put([]byte("\xfftotal"), uint64Bytes(uint64(total)))
	})
}

func decorateSidebarBoltRow(row *SidebarSession, plan sidebarGroupPlan, sessionTitles map[string]string, runtimes map[string]sidebarRuntimeRow) {
	row.GroupID = plan.group.ID
	row.Scope = plan.scope
	row.WorkspaceRoot = plan.root
	if title := strings.TrimSpace(plan.titles[row.TopicID]); title != "" {
		row.Title = title
	} else if title := strings.TrimSpace(sessionTitles[sessionRuntimeKey(row.SessionPath)]); title != "" {
		row.Title = title
	}
	if plan.pinned[row.TopicID] {
		row.Pinned = true
	}
	row.TitleSource = plan.titleSource[row.TopicID]
	if runtime, ok := runtimes[row.TopicID]; ok {
		if runtime.ID != "" {
			row.ID, row.SessionID = runtime.ID, runtime.ID
		}
		row.SessionPath = firstNonEmpty(runtime.SessionPath, row.SessionPath)
		row.Status, row.Open, row.Running, row.TurnStartedAt = runtime.Status, runtime.Open, runtime.Running, runtime.TurnStartedAt
		if runtime.SessionSource != "" {
			row.SessionSource = runtime.SessionSource
		}
		if kind := sidebarExplicitSessionKind(runtime.SessionKind, runtime.SessionSource); kind != "" {
			row.SessionKind = kind
		}
	}
}

func sidebarEffectivePlan(row SidebarSession, groups map[string]sidebarGroupPlan, crewRoutes map[string]channelSessionRoute) sidebarGroupPlan {
	if crew, ok := groups["crew_folder"]; ok {
		if sidebarRowBelongsToCrew(row, crewRoutes) {
			return crew
		}
	}
	return groups[row.GroupID]
}

func sidebarRowBelongsToCrew(row SidebarSession, crewRoutes map[string]channelSessionRoute) bool {
	_, configured := crewRoutes[sessionRuntimeKey(row.SessionPath)]
	return configured || sidebarRowHasCrewSource(row)
}

func sidebarRowHasCrewSource(row SidebarSession) bool {
	source := strings.ToLower(strings.TrimSpace(row.SessionSource))
	if strings.TrimSpace(row.Channel) == "" || source == "auto" || source == "assist" || source == "collaboration" || strings.HasPrefix(source, "work:") {
		return false
	}
	return true
}

func sidebarModeMatches(row SidebarSession, mode SidebarMode) bool {
	if mode == SidebarProjects {
		return true
	}
	want := "collaboration"
	if mode == SidebarAssistants {
		want = "assistant"
	}
	return sidebarExplicitSessionKind(row.SessionKind, row.SessionSource) == want
}

func (index *sidebarBoltIndex) readSessionPage(app *App, q *sidebarBoltQuery, cursor *sidebarCursor, limit int) ([]SidebarSession, *sidebarCursor, int, error) {
	state, err := index.open(app)
	if err != nil {
		return nil, nil, 0, err
	}
	items := []SidebarSession{}
	var next *sidebarCursor
	total := 0
	err = index.view(state.db, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sidebarBoltQueries).Bucket([]byte(q.bucket))
		if bucket == nil {
			return errInvalidSidebarCursor
		}
		total = int(bytesToUint64(bucket.Get([]byte("\xfftotal"))))
		keys, values, hasMore := sidebarBoltPage(bucket, cursor, limit)
		for _, value := range values {
			var item SidebarSession
			if err := json.Unmarshal(value, &item); err != nil {
				return err
			}
			item.Revision = int64(q.revision)
			items = append(items, item)
		}
		if len(items) > 0 && hasMore {
			last := items[len(items)-1]
			next = &sidebarCursor{Version: q.revision, Kind: q.kind, Signature: q.signature, LastActivityAt: last.LastActivityAt, LastID: last.ID, LastKey: base64.RawURLEncoding.EncodeToString(keys[len(keys)-1])}
		}
		return nil
	})
	return items, next, total, err
}

func (index *sidebarBoltIndex) readSearchPage(app *App, q *sidebarBoltQuery, cursor *sidebarCursor, limit int) ([]SidebarSearchItem, *sidebarCursor, int, error) {
	state, err := index.open(app)
	if err != nil {
		return nil, nil, 0, err
	}
	items := []SidebarSearchItem{}
	var next *sidebarCursor
	total := 0
	err = index.view(state.db, func(tx *bolt.Tx) error {
		bucket := tx.Bucket(sidebarBoltQueries).Bucket([]byte(q.bucket))
		if bucket == nil {
			return errInvalidSidebarCursor
		}
		total = int(bytesToUint64(bucket.Get([]byte("\xfftotal"))))
		keys, values, hasMore := sidebarBoltPage(bucket, cursor, limit)
		for _, value := range values {
			var item SidebarSearchItem
			if err := json.Unmarshal(value, &item); err != nil {
				return err
			}
			if item.Session != nil {
				item.Session.Revision = int64(q.revision)
			}
			items = append(items, item)
		}
		if len(items) > 0 && hasMore {
			last := items[len(items)-1]
			next = &sidebarCursor{Version: q.revision, Kind: q.kind, Signature: q.signature, LastActivityAt: last.LastActivityAt, LastID: last.ID, LastKey: base64.RawURLEncoding.EncodeToString(keys[len(keys)-1])}
		}
		return nil
	})
	return items, next, total, err
}

func sidebarBoltPage(bucket *bolt.Bucket, cursor *sidebarCursor, limit int) ([][]byte, [][]byte, bool) {
	keys, values := make([][]byte, 0, limit), make([][]byte, 0, limit)
	c := bucket.Cursor()
	var key, value []byte
	if cursor != nil && cursor.LastKey != "" {
		last, err := base64.RawURLEncoding.DecodeString(cursor.LastKey)
		if err != nil {
			return keys, values, false
		}
		key, value = c.Seek(last)
		if bytes.Equal(key, last) {
			key, value = c.Next()
		}
	} else {
		key, value = c.First()
	}
	for ; key != nil && len(keys) < limit; key, value = c.Next() {
		if bytes.Equal(key, []byte("\xfftotal")) {
			break
		}
		keys = append(keys, append([]byte(nil), key...))
		values = append(values, append([]byte(nil), value...))
	}
	hasMore := key != nil && !bytes.Equal(key, []byte("\xfftotal"))
	return keys, values, hasMore
}

type sidebarGroupStat struct {
	count int
	last  int64
}

func (index *sidebarBoltIndex) groupStats(app *App, plans []sidebarGroupPlan, mode SidebarMode) (map[string]sidebarGroupStat, error) {
	state, err := index.open(app)
	if err != nil {
		return nil, err
	}
	stats := map[string]sidebarGroupStat{}
	crewRoutes := index.routes()
	err = index.view(state.db, func(tx *bolt.Tx) error {
		rows, order := tx.Bucket(sidebarBoltRows), tx.Bucket(sidebarBoltOrder)
		groups := make(map[string]sidebarGroupPlan, len(plans))
		for _, plan := range plans {
			groups[plan.group.ID] = plan
		}
		cursor := order.Cursor()
		for _, path := cursor.First(); path != nil; _, path = cursor.Next() {
			var row SidebarSession
			if json.Unmarshal(rows.Get(path), &row) != nil || !sidebarModeMatches(row, mode) {
				continue
			}
			groupID := sidebarEffectivePlan(row, groups, crewRoutes).group.ID
			if groupID == "" {
				continue
			}
			stat := stats[groupID]
			stat.count++
			stat.last = max(stat.last, row.LastActivityAt)
			stats[groupID] = stat
		}
		return nil
	})
	return stats, err
}

func sidebarBoltOrderKey(groupID string, activity int64, id, path string) []byte {
	return append([]byte(groupID+"\x00"), sidebarBoltResultKey(activity, id, path)...)
}

func sidebarBoltResultKey(activity int64, id, path string) []byte {
	key := make([]byte, 8, 8+len(id)+18)
	binary.BigEndian.PutUint64(key, uint64(math.MaxInt64-max(activity, 0)))
	key = append(key, 0)
	for i := range len(id) {
		key = append(key, ^id[i])
	}
	key = append(key, 0xff)
	sum := sha256.Sum256([]byte(path))
	return append(key, sum[:8]...)
}

func sidebarPathStamp(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "missing"
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}

func uint64Bytes(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func bytesToUint64(value []byte) uint64 {
	if len(value) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(value)
}

func sidebarBoltPlanMetaStamp(plan sidebarGroupPlan) string {
	hash := sha256.New()
	fmt.Fprintf(hash, "%s:%s:%s:%s:%t\x00", plan.group.Label, plan.group.Color, plan.group.Icon, plan.group.Root, plan.group.Pinned)
	keys := make([]string, 0, len(plan.titles)+len(plan.pinned))
	for key := range plan.titles {
		keys = append(keys, "t:"+key)
	}
	for key, pinned := range plan.pinned {
		if pinned {
			keys = append(keys, "p:"+key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.HasPrefix(key, "t:") {
			fmt.Fprintf(hash, "%s=%s\x00", key, plan.titles[strings.TrimPrefix(key, "t:")])
		} else {
			fmt.Fprintf(hash, "%s\x00", key)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)[:12])
}
