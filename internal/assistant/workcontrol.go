package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"workground2/internal/fileutil"
	"workground2/internal/workgate"
)

// WorkControlState is the persistent global work gate. It is deliberately not
// per-assistant: pause_all, resume_all and pause_for_restart stop or restore
// every Assistant, scheduler tick, Runner claim, Job claim and retry through a
// single authoritative fence. Session and Run state are never copied here.
// It is an alias of workgate.State so the Assistant writer and the
// dependency-free workgate reader agree on the wire representation.
type WorkControlState = workgate.State

const (
	WorkRunning    WorkControlState = workgate.Running
	WorkQuiescing  WorkControlState = workgate.Quiescing
	WorkPaused     WorkControlState = workgate.Paused
	WorkRecovering WorkControlState = workgate.Recovering
)

// RestartIntent records a one-shot recovery intent used by pause_for_restart.
// The empty value means no intent; after an ordinary restart from RUNNING the
// store still recovers automatically, while an explicit PAUSED stays paused.
type RestartIntent string

const (
	RestartIntentNone    RestartIntent = ""
	RestartIntentRestart RestartIntent = "restart"
)

// WorkControl is the durable global work gate. Epoch is a monotonic generation
// that increments on every interruption (pause and resume) and on a safe
// restart, so a late result produced under an older epoch can never overwrite
// state written after the fence moved. Revision is a monotonic write counter
// bumped on every persisted transition, so hosts can detect any change.
type WorkControl struct {
	State         WorkControlState `json:"state"`
	Epoch         int64            `json:"epoch"`
	Fence         string           `json:"fence"`
	Revision      int64            `json:"revision,omitempty"`
	RestartIntent RestartIntent    `json:"restart_intent,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	RequestID     string           `json:"request_id,omitempty"`
	UpdatedAt     time.Time        `json:"updated_at" ts_type:"string"`
	CreatedAt     time.Time        `json:"created_at" ts_type:"string"`
}

var (
	// ErrWorkPaused reports that a write that would start new work was refused
	// because WorkControl is not RUNNING.
	ErrWorkPaused = errors.New("assistant: work is paused")
	// ErrStaleFence reports that a completion was refused because the global
	// work epoch moved between claim and completion.
	ErrStaleFence = errors.New("assistant: work epoch changed; late result rejected")
)

func validateWorkControl(wc WorkControl) error {
	switch wc.State {
	case WorkRunning, WorkQuiescing, WorkPaused, WorkRecovering:
	default:
		return fmt.Errorf("assistant: invalid work control state %q", wc.State)
	}
	if wc.Epoch < 1 {
		return errors.New("assistant: work control epoch must be positive")
	}
	if wc.Fence == "" {
		return errors.New("assistant: work control fence is required")
	}
	switch wc.RestartIntent {
	case RestartIntentNone, RestartIntentRestart:
	default:
		return fmt.Errorf("assistant: invalid restart intent %q", wc.RestartIntent)
	}
	return nil
}

func defaultWorkControl(now time.Time) WorkControl {
	now = utcNow(now)
	return WorkControl{
		State: WorkRunning, Epoch: 1, Revision: 1,
		Fence:     StableID("work", "running/1"),
		CreatedAt: now, UpdatedAt: now,
	}
}

func (s *Store) workControlPath() string {
	return filepath.Join(s.root, "workcontrol.json")
}

// readWorkControlLocked reads the global gate. It assumes gate.root is held.
// A missing file means the default RUNNING generation, which is never written
// until the first explicit transition.
func (s *Store) readWorkControlLocked() (WorkControl, error) {
	data, err := os.ReadFile(s.workControlPath())
	if os.IsNotExist(err) {
		return defaultWorkControl(time.Now()), nil
	}
	if err != nil {
		return WorkControl{}, fmt.Errorf("assistant: read work control: %w", err)
	}
	var wc WorkControl
	if err := json.Unmarshal(data, &wc); err != nil {
		return WorkControl{}, fmt.Errorf("assistant: parse work control: %w", err)
	}
	if err := validateWorkControl(wc); err != nil {
		return WorkControl{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return wc, nil
}

func (s *Store) writeWorkControlLocked(wc WorkControl) error {
	if err := validateWorkControl(wc); err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("assistant: create store root: %w", err)
	}
	data, err := json.MarshalIndent(wc, "", "  ")
	if err != nil {
		return fmt.Errorf("assistant: marshal work control: %w", err)
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(s.workControlPath(), data, 0o600); err != nil {
		return fmt.Errorf("assistant: commit work control: %w", err)
	}
	return nil
}

// WorkControl reads the current global gate.
func (s *Store) WorkControl() (WorkControl, error) {
	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	return s.readWorkControlLocked()
}

// WorkGate returns a read-only workgate.Gate over the same durable
// workcontrol.json this Store writes. Ordinary (non-Assistant) sessions mount it
// so their model turns and tool execution are fenced by the same persistent
// gate that pauses and resumes the Assistant runtime.
func (s *Store) WorkGate() workgate.Gate {
	return workgate.OpenFile(s.workControlPath())
}

// applyWorkControl runs change against the current gate under the root lock and
// persists the result only when it actually differs. Replaying the same request
// therefore returns the already-applied state instead of bumping the epoch.
// The read-modify-write is additionally serialized across processes with the
// store's cross-process file lock, so concurrent pause/resume from multiple
// hosts (Desktop + daemon) never loses an update.
func (s *Store) applyWorkControl(requestID string, now time.Time, change func(WorkControl) (WorkControl, error)) (WorkControl, error) {
	if err := validateRequestID(requestID); err != nil {
		return WorkControl{}, err
	}
	now = storeNow(now)
	s.gate.root.Lock()
	defer s.gate.root.Unlock()
	unlockFile, err := s.lockWorkControl()
	if err != nil {
		return WorkControl{}, err
	}
	defer unlockFile()
	current, err := s.readWorkControlLocked()
	if err != nil {
		return WorkControl{}, err
	}
	next, err := change(current)
	if err != nil {
		return WorkControl{}, err
	}
	if next == current {
		return current, nil
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = current.CreatedAt
		if next.CreatedAt.IsZero() {
			next.CreatedAt = now
		}
	}
	if next.Revision < current.Revision {
		next.Revision = current.Revision
	}
	next.Revision++
	next.UpdatedAt = now
	next.RequestID = requestID
	if err := s.writeWorkControlLocked(next); err != nil {
		return WorkControl{}, err
	}
	return next, nil
}

// lockWorkControl takes the cross-process file lock guarding workcontrol.json
// read-modify-write cycles. Callers must already hold s.gate.root.
func (s *Store) lockWorkControl() (func(), error) {
	lockRoot := filepath.Join(s.root, ".locks")
	if err := os.MkdirAll(lockRoot, 0o700); err != nil {
		return nil, fmt.Errorf("assistant: create work control lock root: %w", err)
	}
	unlock, err := lockStoreFile(filepath.Join(lockRoot, "workcontrol.lock"), 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("assistant: lock work control: %w", err)
	}
	return unlock, nil
}

func bumpWorkControl(wc WorkControl, state WorkControlState) WorkControl {
	wc.State = state
	wc.Epoch++
	wc.Fence = StableID("work", fmt.Sprintf("%s/%d", state, wc.Epoch))
	return wc
}

// PauseAll atomically moves RUNNING (or RECOVERING) to QUIESCING and bumps the
// global epoch so no new work is claimed and late results are refused. Replaying
// while already QUIESCING or PAUSED is a no-op.
func (s *Store) PauseAll(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		switch wc.State {
		case WorkQuiescing, WorkPaused:
			// An explicit pause overrides any pending safe-restart intent.
			if wc.RestartIntent == RestartIntentRestart {
				next := wc
				next.RestartIntent = RestartIntentNone
				return next, nil
			}
			return wc, nil
		case WorkRunning, WorkRecovering:
			next := bumpWorkControl(wc, WorkQuiescing)
			next.RestartIntent = RestartIntentNone
			next.Reason = ""
			return next, nil
		default:
			return wc, fmt.Errorf("%w: cannot pause from %s", ErrTransition, wc.State)
		}
	})
}

// CompletePause finalizes a quiesce once all in-flight work has checkpointed.
func (s *Store) CompletePause(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		switch wc.State {
		case WorkPaused:
			return wc, nil
		case WorkQuiescing:
			next := wc
			next.State = WorkPaused
			return next, nil
		default:
			return wc, fmt.Errorf("%w: cannot complete pause from %s", ErrTransition, wc.State)
		}
	})
}

// ResumeAll moves PAUSED to RECOVERING and bumps the epoch again, so results
// produced before the pause remain stale across the full pause/resume cycle.
func (s *Store) ResumeAll(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		switch wc.State {
		case WorkRunning:
			return wc, nil
		case WorkPaused:
			next := bumpWorkControl(wc, WorkRecovering)
			next.RestartIntent = RestartIntentNone
			next.Reason = ""
			return next, nil
		default:
			return wc, fmt.Errorf("%w: cannot resume from %s", ErrTransition, wc.State)
		}
	})
}

// CompleteResume finishes recovery and re-opens the work gate.
func (s *Store) CompleteResume(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		switch wc.State {
		case WorkRunning:
			return wc, nil
		case WorkRecovering:
			next := wc
			next.State = WorkRunning
			next.RestartIntent = RestartIntentNone
			return next, nil
		default:
			return wc, fmt.Errorf("%w: cannot complete resume from %s", ErrTransition, wc.State)
		}
	})
}

// PauseForRestart quiesces and records a one-shot restart intent. The host
// persists recovery intent, restarts, then observes the intent and auto-enters
// RECOVERING before calling CompleteResume.
func (s *Store) PauseForRestart(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		switch wc.State {
		case WorkQuiescing, WorkPaused:
			next := wc
			next.RestartIntent = RestartIntentRestart
			return next, nil
		case WorkRunning:
			next := bumpWorkControl(wc, WorkQuiescing)
			next.RestartIntent = RestartIntentRestart
			next.Reason = ""
			return next, nil
		default:
			return wc, fmt.Errorf("%w: cannot restart from %s", ErrTransition, wc.State)
		}
	})
}

// BeginRestartRecovery consumes a pending restart intent by moving PAUSED (or
// QUIESCING) into RECOVERING. It is a no-op when no restart intent is set, so an
// explicit user pause never auto-resumes on a later start.
func (s *Store) BeginRestartRecovery(requestID string, now time.Time) (WorkControl, error) {
	return s.applyWorkControl(requestID, now, func(wc WorkControl) (WorkControl, error) {
		if wc.RestartIntent != RestartIntentRestart {
			return wc, nil
		}
		if wc.State != WorkPaused && wc.State != WorkQuiescing {
			return wc, nil
		}
		next := bumpWorkControl(wc, WorkRecovering)
		next.RestartIntent = RestartIntentNone
		return next, nil
	})
}

// requireRunning returns ErrWorkPaused when new work must not start. It reads
// the gate without holding any assistant lock, so it is safe to call from
// trigger paths that later take a per-assistant lock.
func (s *Store) requireRunning() error {
	wc, err := s.WorkControl()
	if err != nil {
		return err
	}
	if wc.State != WorkRunning {
		return fmt.Errorf("%w: state %s", ErrWorkPaused, wc.State)
	}
	return nil
}

// requireResumeRunning reports ErrWorkPaused while the gate is QUIESCING or
// PAUSED. It admits RECOVERING: resume_all re-drives interrupted work (Plan
// write-back, dispatch receipts, external-action confirmations) during
// recovery, which must not be refused like brand-new work. Reads the gate
// without holding any assistant lock.
func (s *Store) requireResumeRunning() error {
	wc, err := s.WorkControl()
	if err != nil {
		return err
	}
	if wc.State != WorkRunning && wc.State != WorkRecovering {
		return fmt.Errorf("%w: state %s", ErrWorkPaused, wc.State)
	}
	return nil
}

// checkWorkEpoch rejects a completion when the entity was claimed under a
// different work generation. Legacy entities with a zero epoch predate the
// fence and are not checked, so old aggregates remain recoverable.
func checkWorkEpoch(claimed, current int64) error {
	if claimed != 0 && claimed != current {
		return ErrStaleFence
	}
	return nil
}
