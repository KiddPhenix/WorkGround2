package assistant

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

const aggregateVersion = 1

var storeGates sync.Map

// Snapshot is the externally visible, immutable-by-convention projection of an
// assistant aggregate. Callers receive deep copies, so mutating a result never
// changes Store state without a subsequent CAS operation.
type Snapshot struct {
	Revision      int64            `json:"revision"`
	Assistant     Assistant        `json:"assistant"`
	Routines      []Routine        `json:"routines"`
	Memory        Memory           `json:"memory"`
	Runs          []Run            `json:"runs"`
	Attention     []AttentionItem  `json:"attention"`
	Plan          Plan             `json:"plan"`
	Artifacts     []Artifact       `json:"artifacts"`
	Opportunities []Opportunity    `json:"opportunities"`
	Receipts      []RequestReceipt `json:"receipts"`
	UpdatedAt     time.Time        `json:"updated_at" ts_type:"string"`
}

type RequestReceipt struct {
	RequestID   string    `json:"request_id"`
	Operation   string    `json:"operation"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}

type requestReceipt struct {
	Operation   string          `json:"operation"`
	Fingerprint string          `json:"fingerprint"`
	Result      json.RawMessage `json:"result"`
	CreatedAt   time.Time       `json:"created_at"`
}

type aggregate struct {
	Version       int                       `json:"version"`
	Revision      int64                     `json:"revision"`
	Assistant     Assistant                 `json:"assistant"`
	Routines      []Routine                 `json:"routines"`
	Memory        Memory                    `json:"memory"`
	Runs          []Run                     `json:"runs"`
	Attention     []AttentionItem           `json:"attention"`
	Plan          Plan                      `json:"plan"`
	Artifacts     []Artifact                `json:"artifacts"`
	Opportunities []Opportunity             `json:"opportunities"`
	Requests      map[string]requestReceipt `json:"requests"`
	Occurrences   map[string]string         `json:"occurrences"`
	UpdatedAt     time.Time                 `json:"updated_at"`
}

type storeGate struct {
	root       sync.Mutex
	assistants sync.Map
}

type Store struct {
	root string
	gate *storeGate
}

// NewStore opens a root dedicated to assistant state. Empty paths, relative
// paths and volume roots are rejected so a configuration mistake cannot turn
// the filesystem root (or the process working directory) into the store.
func NewStore(root string) (*Store, error) {
	raw := strings.TrimSpace(root)
	if raw == "" {
		return nil, errors.New("assistant: store root is required")
	}
	if !filepath.IsAbs(raw) {
		return nil, fmt.Errorf("assistant: store root must be absolute: %q", root)
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return nil, fmt.Errorf("assistant: resolve store root: %w", err)
	}
	abs = filepath.Clean(abs)
	if isVolumeRoot(abs) {
		return nil, fmt.Errorf("assistant: refusing dangerous store root %q", abs)
	}
	if info, statErr := os.Lstat(abs); statErr == nil {
		if !info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("assistant: store root %q is not a directory", abs)
		}
	} else if !os.IsNotExist(statErr) {
		return nil, fmt.Errorf("assistant: inspect store root: %w", statErr)
	}
	abs, err = canonicalStoreRoot(abs)
	if err != nil {
		return nil, err
	}
	if isVolumeRoot(abs) {
		return nil, fmt.Errorf("assistant: refusing dangerous store root %q", abs)
	}
	gateValue, _ := storeGates.LoadOrStore(abs, &storeGate{})
	return &Store{root: abs, gate: gateValue.(*storeGate)}, nil
}

func canonicalStoreRoot(path string) (string, error) {
	current := path
	missing := make([]string, 0)
	for {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("assistant: inspect store root ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		return "", fmt.Errorf("assistant: resolve store root: %w", err)
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func isVolumeRoot(path string) bool {
	volume := filepath.VolumeName(path)
	rest := strings.TrimPrefix(path, volume)
	rest = strings.Trim(rest, `\/`)
	return rest == ""
}

func (s *Store) lockAssistant(id string) func() {
	value, _ := s.gate.assistants.LoadOrStore(id, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Store) Create(in CreateInput) (Snapshot, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Snapshot{}, err
	}
	if err := validateID("assistant", in.Assistant.ID); err != nil {
		return Snapshot{}, err
	}
	now := storeNow(in.Now)
	fingerprint, err := inputFingerprint(struct {
		Assistant Assistant
		Routines  []Routine
	}{assistantIntent(in.Assistant), routineIntents(in.Routines)})
	if err != nil {
		return Snapshot{}, err
	}

	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	unlock := s.lockAssistant(in.Assistant.ID)
	defer unlock()

	current, err := s.read(in.Assistant.ID)
	if err == nil {
		if result, ok, receiptErr := receiptResult[Snapshot](current, in.RequestID, "create", fingerprint); ok || receiptErr != nil {
			return result, receiptErr
		}
		return Snapshot{}, fmt.Errorf("assistant: %s already exists: %w", in.Assistant.ID, ErrConflict)
	}
	if !errors.Is(err, ErrNotFound) {
		return Snapshot{}, err
	}

	a := in.Assistant
	a.Revision = 1
	a.MemoryRev = 1
	a.CreatedAt, a.UpdatedAt = now, now
	if err := validateAssistant(a); err != nil {
		return Snapshot{}, err
	}
	routines := make([]Routine, len(in.Routines))
	seen := make(map[string]struct{}, len(in.Routines))
	for i := range in.Routines {
		r := in.Routines[i]
		if r.AssistantID == "" {
			r.AssistantID = a.ID
		}
		if r.AssistantID != a.ID {
			return Snapshot{}, fmt.Errorf("assistant: routine %s belongs to %s", r.ID, r.AssistantID)
		}
		if _, exists := seen[r.ID]; exists {
			return Snapshot{}, fmt.Errorf("assistant: duplicate routine %s", r.ID)
		}
		seen[r.ID] = struct{}{}
		r.Revision = 1
		r.CreatedAt, r.UpdatedAt = now, now
		if err := validateRoutine(r); err != nil {
			return Snapshot{}, err
		}
		routines[i] = r
	}
	agg := &aggregate{
		Version: aggregateVersion, Revision: 1, Assistant: a, Routines: routines,
		Memory: Memory{Revision: 1, Items: []MemoryItem{}}, Runs: []Run{}, Attention: []AttentionItem{},
		Plan: emptyPlan(), Artifacts: []Artifact{}, Opportunities: []Opportunity{},
		Requests: map[string]requestReceipt{}, Occurrences: map[string]string{}, UpdatedAt: now,
	}
	result := snapshotOf(agg)
	if err := putReceipt(agg, in.RequestID, "create", fingerprint, result, now); err != nil {
		return Snapshot{}, err
	}
	if err := s.write(agg); err != nil {
		return Snapshot{}, err
	}
	return clone(result), nil
}

func (s *Store) Get(assistantID string) (Snapshot, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return Snapshot{}, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return Snapshot{}, err
	}
	return clone(snapshotOf(agg)), nil
}

func (s *Store) List() ([]Assistant, error) {
	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return []Assistant{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("assistant: list store: %w", err)
	}
	result := make([]Assistant, 0, len(entries))
	var issues []error
	for _, entry := range entries {
		if !entry.IsDir() || validateID("assistant", entry.Name()) != nil {
			continue
		}
		unlock := s.lockAssistant(entry.Name())
		agg, readErr := s.read(entry.Name())
		unlock()
		if readErr != nil {
			issues = append(issues, readErr)
			continue
		}
		result = append(result, clone(agg.Assistant))
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	if len(issues) > 0 {
		return result, fmt.Errorf("%w: %w", ErrCorrupt, errors.Join(issues...))
	}
	return result, nil
}

func (s *Store) Routines(assistantID string) ([]Routine, error) {
	snapshot, err := s.Get(assistantID)
	if err != nil {
		return nil, err
	}
	return clone(snapshot.Routines), nil
}

func (s *Store) UpdateAssistant(requestID string, desired Assistant, expectedRevision int64, now time.Time) (Assistant, error) {
	if err := validateRequestID(requestID); err != nil {
		return Assistant{}, err
	}
	if err := validateID("assistant", desired.ID); err != nil {
		return Assistant{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		Assistant Assistant
		Expected  int64
	}{assistantIntent(desired), expectedRevision})
	if err != nil {
		return Assistant{}, err
	}
	unlock := s.lockAssistant(desired.ID)
	defer unlock()
	agg, err := s.read(desired.ID)
	if err != nil {
		return Assistant{}, err
	}
	if result, ok, receiptErr := receiptResult[Assistant](agg, requestID, "update_assistant", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	if agg.Assistant.Revision != expectedRevision {
		return Assistant{}, conflict("assistant", desired.ID, expectedRevision, agg.Assistant.Revision)
	}
	current := agg.Assistant
	desired.Revision = current.Revision + 1
	desired.MemoryRev = current.MemoryRev
	desired.CreatedAt = current.CreatedAt
	desired.UpdatedAt = storeNow(now)
	if err := validateAssistant(desired); err != nil {
		return Assistant{}, err
	}
	agg.Assistant = desired
	touch(agg, desired.UpdatedAt)
	if err := putReceipt(agg, requestID, "update_assistant", fingerprint, desired, desired.UpdatedAt); err != nil {
		return Assistant{}, err
	}
	if err := s.write(agg); err != nil {
		return Assistant{}, err
	}
	return clone(desired), nil
}

func (s *Store) PutRoutine(in RoutineInput) (Routine, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Routine{}, err
	}
	if err := validateID("assistant", in.Routine.AssistantID); err != nil {
		return Routine{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		Routine  Routine
		Expected int64
	}{routineIntent(in.Routine), in.ExpectedRevision})
	if err != nil {
		return Routine{}, err
	}
	unlock := s.lockAssistant(in.Routine.AssistantID)
	defer unlock()
	agg, err := s.read(in.Routine.AssistantID)
	if err != nil {
		return Routine{}, err
	}
	if result, ok, receiptErr := receiptResult[Routine](agg, in.RequestID, "put_routine", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	index := routineIndex(agg, in.Routine.ID)
	if index < 0 && in.ExpectedRevision != 0 {
		return Routine{}, conflict("routine", in.Routine.ID, in.ExpectedRevision, 0)
	}
	if index >= 0 && agg.Routines[index].Revision != in.ExpectedRevision {
		return Routine{}, conflict("routine", in.Routine.ID, in.ExpectedRevision, agg.Routines[index].Revision)
	}
	now := storeNow(in.Now)
	r := in.Routine
	if index < 0 {
		r.Revision, r.CreatedAt = 1, now
	} else {
		// Scheduling progress is Store-owned. UI edits cannot erase or rewind it.
		r.LastScheduledFor = agg.Routines[index].LastScheduledFor
		r.Revision, r.CreatedAt = agg.Routines[index].Revision+1, agg.Routines[index].CreatedAt
	}
	r.UpdatedAt = now
	if err := validateRoutine(r); err != nil {
		return Routine{}, err
	}
	if index < 0 {
		agg.Routines = append(agg.Routines, r)
	} else {
		agg.Routines[index] = r
	}
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "put_routine", fingerprint, r, now); err != nil {
		return Routine{}, err
	}
	if err := s.write(agg); err != nil {
		return Routine{}, err
	}
	return clone(r), nil
}

func (s *Store) AdvanceRoutine(assistantID, routineID, requestID string, expectedRevision int64, scheduledFor, now time.Time) (*Routine, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return nil, err
	}
	if err := validateID("routine", routineID); err != nil {
		return nil, err
	}
	if err := validateRequestID(requestID); err != nil {
		return nil, err
	}
	scheduledFor = scheduledFor.UTC()
	if scheduledFor.IsZero() {
		return nil, errors.New("assistant: routine cursor time is required")
	}
	fingerprint, err := inputFingerprint(struct {
		RoutineID   string
		Expected    int64
		ScheduledAt time.Time
	}{routineID, expectedRevision, scheduledFor})
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[Routine](agg, requestID, "advance_routine", fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := routineIndex(agg, routineID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	routine := &agg.Routines[idx]
	if routine.Revision != expectedRevision {
		return nil, conflict("routine", routineID, expectedRevision, routine.Revision)
	}
	if routine.LastScheduledFor.After(scheduledFor) {
		return nil, fmt.Errorf("assistant: routine cursor cannot move backwards: %w", ErrConflict)
	}
	at := storeNow(now)
	routine.LastScheduledFor = scheduledFor
	routine.Revision++
	routine.UpdatedAt = at
	result := clone(*routine)
	touch(agg, at)
	if err := putReceipt(agg, requestID, "advance_routine", fingerprint, result, at); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) ApplyMemory(assistantID, requestID string, expectedRevision int64, patch MemoryPatch, now time.Time) (Memory, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return Memory{}, err
	}
	if err := validateRequestID(requestID); err != nil {
		return Memory{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		Expected int64
		Patch    MemoryPatch
	}{expectedRevision, memoryIntent(patch)})
	if err != nil {
		return Memory{}, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return Memory{}, err
	}
	if result, ok, receiptErr := receiptResult[Memory](agg, requestID, "apply_memory", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	if agg.Memory.Revision != expectedRevision {
		return Memory{}, conflict("memory", assistantID, expectedRevision, agg.Memory.Revision)
	}
	if err := applyMemoryPatch(&agg.Memory, patch, storeNow(now)); err != nil {
		return Memory{}, err
	}
	agg.Assistant.MemoryRev = agg.Memory.Revision
	touch(agg, storeNow(now))
	result := clone(agg.Memory)
	if err := putReceipt(agg, requestID, "apply_memory", fingerprint, result, storeNow(now)); err != nil {
		return Memory{}, err
	}
	if err := s.write(agg); err != nil {
		return Memory{}, err
	}
	return result, nil
}

func (s *Store) Trigger(in TriggerInput) (Run, error) {
	if in.Trigger == "" {
		in.Trigger = TriggerManual
	}
	if in.Trigger != TriggerManual {
		return Run{}, fmt.Errorf("assistant: public trigger only accepts %q: %w", TriggerManual, ErrTransition)
	}
	return s.trigger(in, false)
}

func (s *Store) CreateOccurrence(in TriggerInput) (*Run, error) {
	in.Trigger = TriggerScheduled
	run, err := s.trigger(in, true)
	if err != nil {
		return nil, err
	}
	return &run, nil
}

func (s *Store) trigger(in TriggerInput, occurrence bool) (Run, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Run{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Run{}, err
	}
	if in.MaxAttempts < 1 {
		in.MaxAttempts = 3
	}
	operation := "trigger"
	if occurrence {
		operation = "create_occurrence"
		if in.RoutineID == "" || in.ScheduledFor.IsZero() {
			return Run{}, errors.New("assistant: occurrence requires routine and scheduled time")
		}
	}
	fingerprint, err := inputFingerprint(struct {
		AssistantID, RoutineID string
		Trigger                TriggerKind
		ScheduledFor           time.Time
		MaxAttempts            int
	}{in.AssistantID, in.RoutineID, in.Trigger, in.ScheduledFor.UTC(), in.MaxAttempts})
	if err != nil {
		return Run{}, err
	}
	unlock := s.lockAssistant(in.AssistantID)
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return Run{}, err
	}
	if result, ok, receiptErr := receiptResult[Run](agg, in.RequestID, operation, fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	if agg.Assistant.Lifecycle != LifecycleActive {
		return Run{}, fmt.Errorf("assistant: %s is %s: %w", in.AssistantID, agg.Assistant.Lifecycle, ErrTransition)
	}
	frozenRoutine := Routine{}
	if in.RoutineID != "" {
		idx := routineIndex(agg, in.RoutineID)
		if idx < 0 {
			return Run{}, fmt.Errorf("assistant: routine %s: %w", in.RoutineID, ErrNotFound)
		}
		if occurrence && !agg.Routines[idx].Enabled {
			return Run{}, fmt.Errorf("assistant: routine %s is disabled: %w", in.RoutineID, ErrTransition)
		}
		frozenRoutine = agg.Routines[idx]
	}
	now := storeNow(in.Now)
	occurrenceKey := ""
	if occurrence {
		occurrenceKey = OccurrenceKey(in.AssistantID, in.RoutineID, in.ScheduledFor)
		if runID := agg.Occurrences[occurrenceKey]; runID != "" {
			run, ok := findRun(agg, runID)
			if !ok {
				return Run{}, fmt.Errorf("assistant: occurrence %s references missing run %s", occurrenceKey, runID)
			}
			if err := putReceipt(agg, in.RequestID, operation, fingerprint, run, now); err != nil {
				return Run{}, err
			}
			touch(agg, now)
			if err := s.write(agg); err != nil {
				return Run{}, err
			}
			return clone(run), nil
		}
		if idx := coalescibleRunIndex(agg, in.RoutineID); idx >= 0 {
			run := &agg.Runs[idx]
			run.Occurrences = append(run.Occurrences, occurrenceKey)
			agg.Occurrences[occurrenceKey] = run.ID
			if run.ScheduledFor.Before(in.ScheduledFor) {
				run.ScheduledFor = in.ScheduledFor.UTC()
				run.OccurrenceKey = occurrenceKey
			}
			run.Revision++
			run.UpdatedAt = now
			result := clone(*run)
			touch(agg, now)
			if err := putReceipt(agg, in.RequestID, operation, fingerprint, result, now); err != nil {
				return Run{}, err
			}
			if err := s.write(agg); err != nil {
				return Run{}, err
			}
			return result, nil
		}
	}
	run := Run{
		ID: StableID("run", in.AssistantID+"/"+in.RequestID), AssistantID: in.AssistantID,
		RoutineID: in.RoutineID, RequestID: in.RequestID, OccurrenceKey: occurrenceKey,
		Trigger: in.Trigger, AssistantRevision: agg.Assistant.Revision,
		Scope: agg.Assistant.Scope, WorkspaceRoot: agg.Assistant.WorkspaceRoot,
		RoutineRevision: frozenRoutine.Revision, Prompt: frozenRoutine.Prompt,
		Mission: agg.Assistant.Mission, Policy: agg.Assistant.Policy, State: RunQueued, MaxAttempts: in.MaxAttempts,
		ScheduledFor: in.ScheduledFor.UTC(), Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if occurrence {
		run.Occurrences = []string{occurrenceKey}
		agg.Occurrences[occurrenceKey] = run.ID
	}
	if err := validateRun(run); err != nil {
		return Run{}, err
	}
	agg.Runs = append(agg.Runs, run)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, operation, fingerprint, run, now); err != nil {
		return Run{}, err
	}
	if err := s.write(agg); err != nil {
		return Run{}, err
	}
	return clone(run), nil
}

func (s *Store) Claim(owner string, now time.Time, lease time.Duration) (*Run, bool, error) {
	if strings.TrimSpace(owner) == "" || lease <= 0 {
		return nil, false, errors.New("assistant: claim requires owner and positive lease")
	}
	now = storeNow(now)
	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	entries, err := os.ReadDir(s.root)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("assistant: list claim candidates: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var issues []error
	for _, entry := range entries {
		assistantID := entry.Name()
		if !entry.IsDir() || validateID("assistant", assistantID) != nil {
			continue
		}
		unlock := s.lockAssistant(assistantID)
		agg, readErr := s.read(assistantID)
		if readErr != nil {
			unlock()
			issues = append(issues, readErr)
			continue
		}
		dirty := false
		for i := range agg.Runs {
			run := &agg.Runs[i]
			if run.State != RunRunning || run.LeaseUntil.After(now) {
				continue
			}
			if err := moveRun(run, RunWaitingAttention); err != nil {
				unlock()
				return nil, false, err
			}
			run.Error = &RunError{Code: "outcome_unknown", Message: "execution lease expired; external outcome is unknown", OutcomeKnown: false, At: now}
			clearLease(run)
			run.Revision++
			run.UpdatedAt = now
			ensureAttention(agg, run, now)
			dirty = true
		}
		busy := false
		for i := range agg.Runs {
			switch agg.Runs[i].State {
			case RunRunning, RunWaitingApproval, RunWaitingAttention:
				busy = true
			}
		}
		idx := nextQueuedIndex(agg, now)
		if busy || idx < 0 || agg.Assistant.Lifecycle != LifecycleActive {
			if dirty {
				touch(agg, now)
				if writeErr := s.write(agg); writeErr != nil {
					unlock()
					return nil, false, writeErr
				}
			}
			unlock()
			continue
		}
		run := &agg.Runs[idx]
		if err := moveRun(run, RunRunning); err != nil {
			unlock()
			return nil, false, err
		}
		run.Attempt++
		run.LeaseOwner = owner
		run.LeaseFence++
		run.LeaseUntil = now.Add(lease)
		run.StartedAt = now
		run.UpdatedAt = now
		run.Revision++
		run.Error, run.RetryAt, run.FinishedAt = nil, time.Time{}, time.Time{}
		touch(agg, now)
		if writeErr := s.write(agg); writeErr != nil {
			unlock()
			return nil, false, writeErr
		}
		result := clone(*run)
		unlock()
		return &result, true, nil
	}
	if len(issues) > 0 {
		return nil, false, fmt.Errorf("%w: %w", ErrCorrupt, errors.Join(issues...))
	}
	return nil, false, nil
}

func (s *Store) Renew(runID, owner string, fence int64, now time.Time, lease time.Duration) (*Run, error) {
	if lease <= 0 {
		return nil, errors.New("assistant: renew requires positive lease")
	}
	return s.withRunLease(runID, owner, fence, storeNow(now), func(run *Run, at time.Time) error {
		run.LeaseUntil = at.Add(lease)
		return nil
	})
}

// BindSession durably records the execution session before the host submits
// work to the controller. A crash after this commit leaves an auditable session
// reference on the run recovered into attention.
func (s *Store) BindSession(in BindSessionInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.SessionPath = strings.TrimSpace(in.SessionPath)
	if in.SessionPath == "" {
		return nil, errors.New("assistant: session path is required")
	}
	fp, err := inputFingerprint(struct {
		RunID, Owner, SessionPath string
		Fence                     int64
	}{in.RunID, in.LeaseOwner, in.SessionPath, in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withRunLeaseRequest(in.RunID, in.LeaseOwner, in.LeaseFence, in.RequestID, "bind_session", fp, storeNow(in.Now), func(run *Run, _ time.Time) error {
		run.SessionPath = in.SessionPath
		return nil
	}, nil)
}

func (s *Store) Finish(in FinishInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	fp, err := inputFingerprint(struct {
		RunID, Owner, Summary, SessionPath string
		Fence                              int64
	}{in.RunID, in.LeaseOwner, strings.TrimSpace(in.Summary), strings.TrimSpace(in.SessionPath), in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withRunLeaseRequest(in.RunID, in.LeaseOwner, in.LeaseFence, in.RequestID, "finish", fp, storeNow(in.Now), func(run *Run, at time.Time) error {
		if err := moveRun(run, RunSucceeded); err != nil {
			return err
		}
		run.Summary = strings.TrimSpace(in.Summary)
		run.SessionPath = strings.TrimSpace(in.SessionPath)
		run.FinishedAt = at
		clearLease(run)
		return nil
	}, nil)
}

func (s *Store) Fail(in FailInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	failure := in.Failure
	now := storeNow(failure.Now)
	failureIntent := failure
	failureIntent.Now = time.Time{}
	fp, err := inputFingerprint(struct {
		RunID, Owner string
		Fence        int64
		Failure      Failure
	}{in.RunID, in.LeaseOwner, in.LeaseFence, failureIntent})
	if err != nil {
		return nil, err
	}
	return s.withRunLeaseRequest(in.RunID, in.LeaseOwner, in.LeaseFence, in.RequestID, "fail", fp, now, func(run *Run, at time.Time) error {
		failure.Code, failure.Message, failure.Provider = strings.TrimSpace(failure.Code), strings.TrimSpace(failure.Message), strings.TrimSpace(failure.Provider)
		if failure.Code == "" || failure.Message == "" {
			return errors.New("assistant: failure code and message are required")
		}
		run.Error = &RunError{
			Code: failure.Code, Message: failure.Message, Provider: failure.Provider,
			Retryable: failure.Retryable, OutcomeKnown: failure.OutcomeKnown, At: at,
		}
		clearLease(run)
		if !failure.OutcomeKnown {
			if err := moveRun(run, RunWaitingAttention); err != nil {
				return err
			}
			run.Error.Retryable = false
		} else if failure.Retryable && run.Attempt < run.MaxAttempts {
			if err := moveRun(run, RunRetryWait); err != nil {
				return err
			}
			if failure.RetryAfter < 0 {
				failure.RetryAfter = 0
			}
			run.RetryAt = at.Add(failure.RetryAfter)
		} else {
			if err := moveRun(run, RunFailed); err != nil {
				return err
			}
			run.FinishedAt = at
		}
		return nil
	}, nil)
}

func (s *Store) withRunLeaseRequest(runID, owner string, fence int64, requestID, operation, fingerprint string, now time.Time, mutate func(*Run, time.Time) error, after func(*aggregate, Run, time.Time)) (*Run, error) {
	if err := validateID("run", runID); err != nil {
		return nil, err
	}
	assistantID, err := s.runOwner(runID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[Run](agg, requestID, operation, fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := runIndex(agg, runID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	run := &agg.Runs[idx]
	if run.State != RunRunning || run.LeaseOwner != owner || run.LeaseFence != fence || !now.Before(run.LeaseUntil) {
		return nil, fmt.Errorf("assistant: run %s fence %d is stale: %w", runID, fence, ErrLeaseLost)
	}
	if err := mutate(run, now); err != nil {
		return nil, err
	}
	run.Revision++
	run.UpdatedAt = now
	if after != nil {
		after(agg, *run, now)
	}
	if run.State == RunWaitingAttention && !hasOpenAttention(agg, run.ID) {
		ensureAttention(agg, run, now)
	}
	result := clone(*run)
	touch(agg, now)
	if err := putReceipt(agg, requestID, operation, fingerprint, result, now); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) RequestApproval(in ApprovalInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.Action, in.Summary = strings.TrimSpace(in.Action), strings.TrimSpace(in.Summary)
	in.Tool, in.Subject = strings.TrimSpace(in.Tool), strings.TrimSpace(in.Subject)
	in.SessionPath, in.ResumeToken = strings.TrimSpace(in.SessionPath), strings.TrimSpace(in.ResumeToken)
	if in.Action == "" || in.Summary == "" || in.ResumeToken == "" {
		return nil, errors.New("assistant: approval action, summary, and resume token are required")
	}
	if strings.HasPrefix(in.Action, "approve_tool") {
		if in.Tool == "" {
			return nil, errors.New("assistant: tool approval requires a tool")
		}
	} else {
		in.Tool, in.Subject = "", ""
	}
	preSubmit := in.Action == AttentionActionRebindWorkspace || in.Action == AttentionActionCancelRecreate
	if in.SessionPath == "" && !preSubmit {
		return nil, errors.New("assistant: approval session path is required after submission")
	}
	fp, err := inputFingerprint(struct {
		RunID, Owner, Action, Summary, Tool, Subject, SessionPath, ResumeToken string
		Fence                                                                  int64
	}{in.RunID, in.LeaseOwner, in.Action, in.Summary, in.Tool, in.Subject, in.SessionPath, in.ResumeToken, in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withRunLeaseRequest(in.RunID, in.LeaseOwner, in.LeaseFence, in.RequestID, "request_approval", fp, storeNow(in.Now), func(run *Run, _ time.Time) error {
		if err := moveRun(run, RunWaitingApproval); err != nil {
			return err
		}
		run.Summary = in.Summary
		run.SessionPath = in.SessionPath
		run.ResumeToken = in.ResumeToken
		clearLease(run)
		return nil
	}, func(agg *aggregate, run Run, now time.Time) {
		id := StableID("att", in.RequestID)
		for i := range agg.Attention {
			if agg.Attention[i].ID == id {
				return
			}
		}
		agg.Attention = append(agg.Attention, AttentionItem{
			ID: id, AssistantID: run.AssistantID, RunID: run.ID, RequestID: in.RequestID,
			Action: in.Action, Summary: in.Summary, Tool: in.Tool, Subject: in.Subject, ResumeToken: in.ResumeToken,
			State: AttentionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
	})
}

// RequireAttention records a known, pre-side-effect configuration blocker and
// releases the active lease. It is distinct from approval and unknown-outcome
// recovery: the run cannot execute again until its attention is resolved.
func (s *Store) RequireAttention(in RequireAttentionInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.Action, in.Summary = strings.TrimSpace(in.Action), strings.TrimSpace(in.Summary)
	in.SessionPath, in.ResumeToken = strings.TrimSpace(in.SessionPath), strings.TrimSpace(in.ResumeToken)
	if in.Action == "" || in.Summary == "" || in.ResumeToken == "" {
		return nil, errors.New("assistant: attention action, summary, and resume token are required")
	}
	fp, err := inputFingerprint(struct {
		RunID, Owner, Action, Summary, SessionPath, ResumeToken string
		Fence                                                   int64
	}{in.RunID, in.LeaseOwner, in.Action, in.Summary, in.SessionPath, in.ResumeToken, in.LeaseFence})
	if err != nil {
		return nil, err
	}
	return s.withRunLeaseRequest(in.RunID, in.LeaseOwner, in.LeaseFence, in.RequestID, "require_attention", fp, storeNow(in.Now), func(run *Run, now time.Time) error {
		if err := moveRun(run, RunWaitingAttention); err != nil {
			return err
		}
		run.Summary = in.Summary
		run.SessionPath = in.SessionPath
		run.ResumeToken = in.ResumeToken
		run.Error = &RunError{
			Code: "config_attention", Message: in.Summary,
			Retryable: false, OutcomeKnown: true, At: now,
		}
		clearLease(run)
		return nil
	}, func(agg *aggregate, run Run, now time.Time) {
		id := StableID("att", in.RequestID)
		for i := range agg.Attention {
			if agg.Attention[i].ID == id {
				return
			}
		}
		agg.Attention = append(agg.Attention, AttentionItem{
			ID: id, AssistantID: run.AssistantID, RunID: run.ID, RequestID: in.RequestID,
			Action: in.Action, Summary: in.Summary, ResumeToken: in.ResumeToken,
			State: AttentionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
	})
}

func (s *Store) Cancel(in CancelInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	in.Reason = strings.TrimSpace(in.Reason)
	fp, err := inputFingerprint(struct{ RunID, Reason string }{in.RunID, in.Reason})
	if err != nil {
		return nil, err
	}
	return s.mutateRun(in.RunID, in.RequestID, "cancel", fp, storeNow(in.Now), func(agg *aggregate, run *Run, now time.Time) error {
		if err := moveRun(run, RunCancelled); err != nil {
			return err
		}
		clearLease(run)
		run.Summary = in.Reason
		run.FinishedAt = now
		for i := range agg.Attention {
			if agg.Attention[i].RunID == run.ID && agg.Attention[i].State == AttentionOpen {
				agg.Attention[i].State = AttentionCancelled
				agg.Attention[i].Resolution = in.Reason
				agg.Attention[i].Revision++
				agg.Attention[i].UpdatedAt = now
			}
		}
		return nil
	})
}

func (s *Store) ResolveAttention(in ResolveAttentionInput) (*AttentionItem, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return nil, err
	}
	if err := validateID("attention", in.AttentionID); err != nil {
		return nil, err
	}
	switch in.State {
	case AttentionApproved, AttentionRejected, AttentionCancelled:
	default:
		return nil, errors.New("assistant: attention resolution must be approved, rejected, or cancelled")
	}
	in.Resolution = strings.TrimSpace(in.Resolution)
	fp, err := inputFingerprint(struct {
		AttentionID string
		Expected    int64
		State       AttentionState
		Resolution  string
	}{in.AttentionID, in.ExpectedRevision, in.State, in.Resolution})
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(in.AssistantID)
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[AttentionItem](agg, in.RequestID, "resolve_attention", fp); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := attentionIndex(agg, in.AttentionID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	item := &agg.Attention[idx]
	if item.Revision != in.ExpectedRevision {
		return nil, conflict("attention", item.ID, in.ExpectedRevision, item.Revision)
	}
	if item.State != AttentionOpen {
		return nil, fmt.Errorf("%w: attention %s is %s", ErrTransition, item.ID, item.State)
	}
	now := storeNow(in.Now)
	runIdx := runIndex(agg, item.RunID)
	if runIdx < 0 {
		return nil, fmt.Errorf("assistant: attention %s run is missing: %w", item.ID, ErrCorrupt)
	}
	run := &agg.Runs[runIdx]
	if item.Action == "verify_run_outcome" && in.State == AttentionApproved {
		switch in.Resolution {
		case "retry_acknowledged":
		case "mark_succeeded":
			if err := moveRun(run, RunSucceeded); err != nil {
				return nil, err
			}
			run.FinishedAt = now
			run.Error = nil
		case "mark_failed":
			if err := moveRun(run, RunFailed); err != nil {
				return nil, err
			}
			run.FinishedAt = now
			provider := ""
			if run.Error != nil {
				provider = run.Error.Provider
			}
			run.Error = &RunError{
				Code: "outcome_failed_confirmed", Message: "external outcome manually confirmed as failed",
				Provider: provider, Retryable: false, OutcomeKnown: true, At: now,
			}
		default:
			return nil, errors.New("assistant: unknown outcome requires retry_acknowledged, mark_succeeded, or mark_failed")
		}
	} else if in.State == AttentionRejected || in.State == AttentionCancelled {
		if err := moveRun(run, RunCancelled); err != nil {
			return nil, err
		}
		run.FinishedAt = now
	}
	if run.State == RunSucceeded || run.State == RunFailed || run.State == RunCancelled {
		clearLease(run)
		run.UpdatedAt, run.Revision = now, run.Revision+1
	}
	item.State, item.Resolution, item.UpdatedAt, item.Revision = in.State, in.Resolution, now, item.Revision+1
	result := clone(*item)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "resolve_attention", fp, result, item.UpdatedAt); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) Resume(in ResumeInput) (*Run, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	fp, err := inputFingerprint(struct{ RunID string }{in.RunID})
	if err != nil {
		return nil, err
	}
	return s.mutateRun(in.RunID, in.RequestID, "resume", fp, storeNow(in.Now), func(agg *aggregate, run *Run, _ time.Time) error {
		if run.State != RunWaitingApproval && run.State != RunWaitingAttention {
			return fmt.Errorf("%w: cannot resume run in %s", ErrTransition, run.State)
		}
		var current *AttentionItem
		for i := range agg.Attention {
			item := &agg.Attention[i]
			if item.RunID != run.ID {
				continue
			}
			if item.ResumeToken != run.ResumeToken {
				if item.State == AttentionOpen {
					return fmt.Errorf("%w: historical attention %s is still open", ErrTransition, item.ID)
				}
				continue
			}
			if current != nil {
				return fmt.Errorf("%w: multiple current attention items match resume token", ErrTransition)
			}
			current = item
		}
		if current == nil {
			return errors.New("assistant: waiting run has no current attention item")
		}
		if current.State != AttentionApproved {
			return fmt.Errorf("%w: attention %s is %s", ErrTransition, current.ID, current.State)
		}
		if current.Action == "verify_run_outcome" && current.Resolution != "retry_acknowledged" {
			return fmt.Errorf("%w: outcome attention %s was resolved as %s", ErrTransition, current.ID, current.Resolution)
		}
		if current.Action != "verify_run_outcome" && !attentionActionSupportsResume(current.Action) {
			return fmt.Errorf("%w: attention action %s requires cancellation or recreation", ErrTransition, current.Action)
		}
		if current.Action != "verify_run_outcome" && (current.ResumeToken == "" || run.SessionPath == "") {
			return fmt.Errorf("%w: approval %s has no matching persisted resume context", ErrTransition, current.ID)
		}
		if err := moveRun(run, RunQueued); err != nil {
			return err
		}
		if run.Error != nil && run.Error.Code == "config_attention" {
			run.Error = nil
		}
		run.RetryAt = time.Time{}
		return nil
	})
}

func attentionActionSupportsResume(action string) bool {
	switch action {
	case AttentionActionRebindWorkspace, AttentionActionCancelRecreate, "inspect_run_failure":
		return false
	default:
		return true
	}
}

func (s *Store) mutateRun(runID, requestID, operation, fingerprint string, now time.Time, mutate func(*aggregate, *Run, time.Time) error) (*Run, error) {
	if err := validateID("run", runID); err != nil {
		return nil, err
	}
	assistantID, err := s.runOwner(runID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[Run](agg, requestID, operation, fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := runIndex(agg, runID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	run := &agg.Runs[idx]
	if err := mutate(agg, run, now); err != nil {
		return nil, err
	}
	run.UpdatedAt, run.Revision = now, run.Revision+1
	result := clone(*run)
	touch(agg, now)
	if err := putReceipt(agg, requestID, operation, fingerprint, result, now); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	return &result, nil
}

func (s *Store) withRunLease(runID, owner string, fence int64, now time.Time, mutate func(*Run, time.Time) error) (*Run, error) {
	if err := validateID("run", runID); err != nil {
		return nil, err
	}
	assistantID, err := s.runOwner(runID)
	if err != nil {
		return nil, err
	}
	unlock := s.lockAssistant(assistantID)
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return nil, err
	}
	idx := runIndex(agg, runID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	run := &agg.Runs[idx]
	if run.State != RunRunning || run.LeaseOwner != owner || run.LeaseFence != fence || !now.Before(run.LeaseUntil) {
		return nil, fmt.Errorf("assistant: run %s fence %d is stale: %w", runID, fence, ErrLeaseLost)
	}
	if err := mutate(run, now); err != nil {
		return nil, err
	}
	run.Revision++
	run.UpdatedAt = now
	if run.State == RunWaitingAttention {
		ensureAttention(agg, run, now)
	}
	touch(agg, now)
	if err := s.write(agg); err != nil {
		return nil, err
	}
	result := clone(*run)
	return &result, nil
}

func (s *Store) runOwner(runID string) (string, error) {
	assistants, listErr := s.List()
	for _, assistant := range assistants {
		unlock := s.lockAssistant(assistant.ID)
		agg, readErr := s.read(assistant.ID)
		unlock()
		if readErr != nil {
			return "", readErr
		}
		if runIndex(agg, runID) >= 0 {
			return assistant.ID, nil
		}
	}
	if listErr != nil {
		return "", errors.Join(ErrNotFound, listErr)
	}
	return "", ErrNotFound
}

// Recover converts expired running leases to waiting_attention. The process
// cannot know whether an external side effect completed before the crash, so
// automatic replay would be unsafe.
func (s *Store) Recover(now time.Time) ([]Run, error) {
	now = storeNow(now)
	return s.scanRuns(now, func(run *Run, at time.Time) (bool, error) {
		if run.State != RunRunning || run.LeaseUntil.After(at) {
			return false, nil
		}
		if err := moveRun(run, RunWaitingAttention); err != nil {
			return false, err
		}
		run.Error = &RunError{Code: "outcome_unknown", Message: "execution lease expired; external outcome is unknown", Retryable: false, OutcomeKnown: false, At: at}
		run.FinishedAt = time.Time{}
		clearLease(run)
		return true, nil
	})
}

func (s *Store) RetryDue(now time.Time) ([]Run, error) {
	now = storeNow(now)
	return s.scanRuns(now, func(run *Run, at time.Time) (bool, error) {
		if run.State != RunRetryWait || run.RetryAt.After(at) {
			return false, nil
		}
		if err := moveRun(run, RunQueued); err != nil {
			return false, err
		}
		run.RetryAt = time.Time{}
		return true, nil
	})
}

func (s *Store) scanRuns(now time.Time, mutate func(*Run, time.Time) (bool, error)) ([]Run, error) {
	assistants, listErr := s.List()
	changed := make([]Run, 0)
	for _, a := range assistants {
		unlock := s.lockAssistant(a.ID)
		agg, readErr := s.read(a.ID)
		if readErr != nil {
			unlock()
			return nil, readErr
		}
		dirty := false
		for i := range agg.Runs {
			changedRun, mutateErr := mutate(&agg.Runs[i], now)
			if mutateErr != nil {
				unlock()
				return nil, mutateErr
			}
			if changedRun {
				agg.Runs[i].Revision++
				agg.Runs[i].UpdatedAt = now
				if agg.Runs[i].State == RunWaitingAttention {
					ensureAttention(agg, &agg.Runs[i], now)
				}
				changed = append(changed, clone(agg.Runs[i]))
				dirty = true
			}
		}
		if dirty {
			touch(agg, now)
			if writeErr := s.write(agg); writeErr != nil {
				unlock()
				return nil, writeErr
			}
		}
		unlock()
	}
	return changed, listErr
}

func applyMemoryPatch(memory *Memory, patch MemoryPatch, now time.Time) error {
	byID := make(map[string]int, len(memory.Items))
	for i := range memory.Items {
		byID[memory.Items[i].ID] = i
	}
	deleteSet := make(map[string]struct{}, len(patch.Delete))
	for _, id := range patch.Delete {
		if err := validateID("memory", id); err != nil {
			return err
		}
		if _, duplicate := deleteSet[id]; duplicate {
			continue
		}
		deleteSet[id] = struct{}{}
		if idx, exists := byID[id]; exists && memory.Items[idx].Locked {
			return fmt.Errorf("assistant: memory %s is locked: %w", id, ErrConflict)
		}
	}
	seenUpsert := make(map[string]struct{}, len(patch.Upsert))
	for _, raw := range patch.Upsert {
		if _, duplicate := seenUpsert[raw.ID]; duplicate {
			return fmt.Errorf("assistant: duplicate memory upsert %s", raw.ID)
		}
		seenUpsert[raw.ID] = struct{}{}
		if _, deleting := deleteSet[raw.ID]; deleting {
			return fmt.Errorf("assistant: memory %s cannot be deleted and upserted", raw.ID)
		}
		if idx, exists := byID[raw.ID]; exists && memory.Items[idx].Locked {
			return fmt.Errorf("assistant: memory %s is locked: %w", raw.ID, ErrConflict)
		}
	}
	items := make([]MemoryItem, 0, len(memory.Items)+len(patch.Upsert))
	for _, item := range memory.Items {
		if _, deleting := deleteSet[item.ID]; !deleting {
			items = append(items, item)
		}
	}
	byID = make(map[string]int, len(items))
	for i := range items {
		byID[items[i].ID] = i
	}
	for _, item := range patch.Upsert {
		if idx, exists := byID[item.ID]; exists {
			item.Revision = items[idx].Revision + 1
			item.CreatedAt = items[idx].CreatedAt
			item.UpdatedAt = now
			if err := validateMemoryItem(item); err != nil {
				return err
			}
			items[idx] = item
		} else {
			item.Revision, item.CreatedAt, item.UpdatedAt = 1, now, now
			if err := validateMemoryItem(item); err != nil {
				return err
			}
			byID[item.ID] = len(items)
			items = append(items, item)
		}
	}
	memory.Items = items
	memory.Revision++
	return nil
}

func (s *Store) read(assistantID string) (*aggregate, error) {
	path, err := s.aggregatePath(assistantID, false)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("assistant: %s: %w", assistantID, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("assistant: read %s: %w", assistantID, err)
	}
	var agg aggregate
	if err := json.Unmarshal(data, &agg); err != nil {
		return nil, fmt.Errorf("assistant: parse %s aggregate: %w", assistantID, err)
	}
	if agg.Version != aggregateVersion || agg.Assistant.ID != assistantID {
		return nil, fmt.Errorf("assistant: invalid %s aggregate identity or version", assistantID)
	}
	if agg.Requests == nil {
		agg.Requests = map[string]requestReceipt{}
	}
	if agg.Occurrences == nil {
		agg.Occurrences = map[string]string{}
	}
	// Old aggregates predate the plan. Lazily normalize them to an empty plan so
	// they remain readable and writable without a migration.
	if agg.Plan.Revision == 0 {
		agg.Plan = emptyPlan()
	}
	if agg.Plan.Responsibilities == nil {
		agg.Plan.Responsibilities = []Responsibility{}
	}
	if agg.Artifacts == nil {
		agg.Artifacts = []Artifact{}
	}
	if agg.Opportunities == nil {
		agg.Opportunities = []Opportunity{}
	}
	if err := validateAggregate(&agg); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrCorrupt, assistantID, err)
	}
	return &agg, nil
}

func (s *Store) write(agg *aggregate) error {
	path, err := s.aggregatePath(agg.Assistant.ID, true)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(agg, "", "  ")
	if err != nil {
		return fmt.Errorf("assistant: marshal %s aggregate: %w", agg.Assistant.ID, err)
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("assistant: commit %s aggregate: %w", agg.Assistant.ID, err)
	}
	return nil
}

func (s *Store) aggregatePath(assistantID string, create bool) (string, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, assistantID)
	rel, err := filepath.Rel(s.root, dir)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return "", fmt.Errorf("assistant: unsafe aggregate path for %q", assistantID)
	}
	if info, statErr := os.Lstat(dir); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("assistant: unsafe aggregate directory for %q", assistantID)
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("assistant: inspect aggregate directory: %w", statErr)
	} else if !create {
		return filepath.Join(dir, "aggregate.json"), nil
	}
	path := filepath.Join(dir, "aggregate.json")
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("assistant: unsafe aggregate file for %q", assistantID)
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("assistant: inspect aggregate file: %w", statErr)
	}
	return path, nil
}

func receiptResult[T any](agg *aggregate, requestID, operation, fingerprint string) (T, bool, error) {
	var zero T
	receipt, exists := agg.Requests[requestID]
	if !exists {
		return zero, false, nil
	}
	if receipt.Operation != operation || receipt.Fingerprint != fingerprint {
		return zero, true, &IdempotencyError{RequestID: requestID, Operation: operation}
	}
	var result T
	if err := json.Unmarshal(receipt.Result, &result); err != nil {
		return zero, true, fmt.Errorf("assistant: decode request %q receipt: %w", requestID, err)
	}
	return result, true, nil
}

func putReceipt(agg *aggregate, requestID, operation, fingerprint string, result any, now time.Time) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("assistant: encode %s receipt: %w", operation, err)
	}
	agg.Requests[requestID] = requestReceipt{Operation: operation, Fingerprint: fingerprint, Result: data, CreatedAt: now}
	return nil
}

func inputFingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("assistant: fingerprint input: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func assistantIntent(a Assistant) Assistant {
	a.MemoryRev, a.Revision = 0, 0
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	return a
}

func routineIntent(r Routine) Routine {
	r.Revision = 0
	r.LastScheduledFor = time.Time{}
	r.CreatedAt, r.UpdatedAt = time.Time{}, time.Time{}
	return r
}

func routineIntents(in []Routine) []Routine {
	out := make([]Routine, len(in))
	for i := range in {
		out[i] = routineIntent(in[i])
	}
	return out
}

func memoryIntent(patch MemoryPatch) MemoryPatch {
	patch = clone(patch)
	for i := range patch.Upsert {
		patch.Upsert[i].Revision = 0
		patch.Upsert[i].CreatedAt, patch.Upsert[i].UpdatedAt = time.Time{}, time.Time{}
	}
	return patch
}

func snapshotOf(agg *aggregate) Snapshot {
	receipts := make([]RequestReceipt, 0, len(agg.Requests))
	for id, receipt := range agg.Requests {
		receipts = append(receipts, RequestReceipt{RequestID: id, Operation: receipt.Operation, Fingerprint: receipt.Fingerprint, CreatedAt: receipt.CreatedAt})
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].RequestID < receipts[j].RequestID })
	return Snapshot{
		Revision: agg.Revision, Assistant: agg.Assistant, Routines: agg.Routines,
		Memory: agg.Memory, Runs: agg.Runs, Attention: agg.Attention,
		Plan: clonePlan(agg.Plan), Artifacts: clone(agg.Artifacts), Opportunities: clone(agg.Opportunities),
		Receipts: receipts, UpdatedAt: agg.UpdatedAt,
	}
}

// clonePlan deep-copies a plan so its responsibility dependency slices are never
// shared with the store.
func clonePlan(p Plan) Plan {
	p.Responsibilities = cloneRespSlice(p.Responsibilities)
	return p
}

func cloneRespSlice(in []Responsibility) []Responsibility {
	out := make([]Responsibility, len(in))
	for i := range in {
		out[i] = copyResp(in[i])
	}
	return out
}

func copyResp(in Responsibility) Responsibility {
	in.DependsOn = clone(in.DependsOn)
	return in
}

func touch(agg *aggregate, now time.Time) {
	agg.Revision++
	agg.UpdatedAt = now
}

func storeNow(now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	return now.UTC()
}

func conflict(entity, id string, expected, actual int64) error {
	return &ConflictError{Entity: entity, ID: id, Expected: expected, Actual: actual}
}

func routineIndex(agg *aggregate, id string) int {
	for i := range agg.Routines {
		if agg.Routines[i].ID == id {
			return i
		}
	}
	return -1
}

func runIndex(agg *aggregate, id string) int {
	for i := range agg.Runs {
		if agg.Runs[i].ID == id {
			return i
		}
	}
	return -1
}

func attentionIndex(agg *aggregate, id string) int {
	for i := range agg.Attention {
		if agg.Attention[i].ID == id {
			return i
		}
	}
	return -1
}

func findRun(agg *aggregate, id string) (Run, bool) {
	idx := runIndex(agg, id)
	if idx < 0 {
		return Run{}, false
	}
	return agg.Runs[idx], true
}

func coalescibleRunIndex(agg *aggregate, routineID string) int {
	for i := range agg.Runs {
		state := agg.Runs[i].State
		if agg.Runs[i].RoutineID == routineID && agg.Runs[i].Trigger == TriggerScheduled && (state == RunQueued || state == RunRetryWait) {
			return i
		}
	}
	return -1
}

func nextQueuedIndex(agg *aggregate, now time.Time) int {
	best := -1
	for i := range agg.Runs {
		if agg.Runs[i].State != RunQueued {
			continue
		}
		if !agg.Runs[i].ScheduledFor.IsZero() && agg.Runs[i].ScheduledFor.After(now) {
			continue
		}
		if best < 0 || runBefore(agg.Runs[i], agg.Runs[best]) {
			best = i
		}
	}
	return best
}

func runBefore(a, b Run) bool {
	aTime, bTime := a.ScheduledFor, b.ScheduledFor
	if aTime.IsZero() {
		aTime = a.CreatedAt
	}
	if bTime.IsZero() {
		bTime = b.CreatedAt
	}
	if aTime.Equal(bTime) {
		return a.ID < b.ID
	}
	return aTime.Before(bTime)
}

func clearLease(run *Run) {
	run.LeaseOwner = ""
	run.LeaseUntil = time.Time{}
}

func moveRun(run *Run, next RunState) error {
	allowed := map[RunState]map[RunState]bool{
		RunQueued:           {RunRunning: true, RunCancelled: true},
		RunRunning:          {RunSucceeded: true, RunWaitingApproval: true, RunRetryWait: true, RunWaitingAttention: true, RunFailed: true, RunCancelled: true},
		RunWaitingApproval:  {RunQueued: true, RunRunning: true, RunWaitingAttention: true, RunCancelled: true},
		RunRetryWait:        {RunQueued: true, RunWaitingAttention: true, RunCancelled: true},
		RunWaitingAttention: {RunQueued: true, RunSucceeded: true, RunFailed: true, RunCancelled: true},
		RunFailed:           {RunQueued: true},
	}
	if !allowed[run.State][next] {
		return fmt.Errorf("%w: %s -> %s", ErrTransition, run.State, next)
	}
	run.State = next
	return nil
}

func ensureAttention(agg *aggregate, run *Run, now time.Time) {
	requestID := fmt.Sprintf("run-attention:%s:%d", run.ID, run.LeaseFence)
	id := StableID("att", requestID)
	resumeToken := StableID("resume", requestID)
	for i := range agg.Attention {
		if agg.Attention[i].ID == id {
			if agg.Attention[i].ResumeToken == "" {
				agg.Attention[i].ResumeToken = resumeToken
				agg.Attention[i].UpdatedAt = now
				agg.Attention[i].Revision++
			}
			run.ResumeToken = agg.Attention[i].ResumeToken
			return
		}
	}
	run.ResumeToken = resumeToken
	action := "inspect_run_failure"
	if run.Error != nil && !run.Error.OutcomeKnown {
		action = "verify_run_outcome"
	}
	summary := "run requires attention"
	if run.Error != nil && run.Error.Message != "" {
		summary = run.Error.Message
	}
	agg.Attention = append(agg.Attention, AttentionItem{
		ID: id, AssistantID: run.AssistantID, RunID: run.ID, RequestID: requestID,
		Action: action, Summary: summary, ResumeToken: resumeToken,
		State: AttentionOpen, Revision: 1, CreatedAt: now, UpdatedAt: now,
	})
}

func hasOpenAttention(agg *aggregate, runID string) bool {
	for i := range agg.Attention {
		if agg.Attention[i].RunID == runID && agg.Attention[i].State == AttentionOpen {
			return true
		}
	}
	return false
}

func clone[T any](in T) T {
	data, _ := json.Marshal(in)
	var out T
	_ = json.Unmarshal(data, &out)
	return out
}

func validateAggregate(agg *aggregate) error {
	if agg.Revision < 1 || agg.UpdatedAt.IsZero() {
		return errors.New("aggregate revision and timestamp are required")
	}
	if err := validateAssistant(agg.Assistant); err != nil {
		return err
	}
	if agg.Memory.Revision != agg.Assistant.MemoryRev {
		return fmt.Errorf("memory revision %d does not match assistant %d", agg.Memory.Revision, agg.Assistant.MemoryRev)
	}
	memoryIDs := make(map[string]bool, len(agg.Memory.Items))
	for _, item := range agg.Memory.Items {
		if err := validateMemoryItem(item); err != nil {
			return err
		}
		if memoryIDs[item.ID] {
			return fmt.Errorf("duplicate memory %s", item.ID)
		}
		memoryIDs[item.ID] = true
		if item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("memory %s has invalid revision or timestamps", item.ID)
		}
	}
	routineIDs := make(map[string]bool, len(agg.Routines))
	for _, routine := range agg.Routines {
		if err := validateRoutine(routine); err != nil {
			return err
		}
		if routine.AssistantID != agg.Assistant.ID {
			return fmt.Errorf("routine %s belongs to %s", routine.ID, routine.AssistantID)
		}
		if routineIDs[routine.ID] {
			return fmt.Errorf("duplicate routine %s", routine.ID)
		}
		routineIDs[routine.ID] = true
	}
	runIDs := make(map[string]Run, len(agg.Runs))
	for _, run := range agg.Runs {
		if err := validateRun(run); err != nil {
			return err
		}
		if run.AssistantID != agg.Assistant.ID {
			return fmt.Errorf("run %s belongs to %s", run.ID, run.AssistantID)
		}
		if run.RoutineID != "" && !routineIDs[run.RoutineID] {
			return fmt.Errorf("run %s references missing routine %s", run.ID, run.RoutineID)
		}
		if _, exists := runIDs[run.ID]; exists {
			return fmt.Errorf("duplicate run %s", run.ID)
		}
		if run.State != RunRunning && (run.LeaseOwner != "" || !run.LeaseUntil.IsZero()) {
			return fmt.Errorf("non-running run %s retains a lease", run.ID)
		}
		if run.State == RunRetryWait && run.RetryAt.IsZero() {
			return fmt.Errorf("retry run %s has no retry time", run.ID)
		}
		runIDs[run.ID] = run
	}
	for key, runID := range agg.Occurrences {
		run, ok := runIDs[runID]
		if !ok || !hasOccurrenceKey(run, key) {
			return fmt.Errorf("occurrence %s references inconsistent run %s", key, runID)
		}
	}
	attentionIDs := make(map[string]bool, len(agg.Attention))
	for _, item := range agg.Attention {
		if err := validateID("attention", item.ID); err != nil {
			return err
		}
		if item.AssistantID != agg.Assistant.ID || item.Revision < 1 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("attention %s has invalid ownership, revision, or timestamps", item.ID)
		}
		if item.RunID != "" {
			if _, ok := runIDs[item.RunID]; !ok {
				return fmt.Errorf("attention %s references missing run %s", item.ID, item.RunID)
			}
		}
		switch item.State {
		case AttentionOpen, AttentionApproved, AttentionRejected, AttentionCancelled:
		default:
			return fmt.Errorf("attention %s has invalid state %s", item.ID, item.State)
		}
		if attentionIDs[item.ID] {
			return fmt.Errorf("duplicate attention %s", item.ID)
		}
		attentionIDs[item.ID] = true
	}
	if err := validatePlan(agg); err != nil {
		return err
	}
	for requestID, receipt := range agg.Requests {
		if err := validateRequestID(requestID); err != nil {
			return err
		}
		if receipt.Operation == "" || receipt.Fingerprint == "" || receipt.CreatedAt.IsZero() || !json.Valid(receipt.Result) {
			return fmt.Errorf("request %s has invalid receipt", requestID)
		}
	}
	return nil
}

func hasOccurrenceKey(run Run, key string) bool {
	if run.OccurrenceKey == key {
		return true
	}
	for _, occurrence := range run.Occurrences {
		if occurrence == key {
			return true
		}
	}
	return false
}
