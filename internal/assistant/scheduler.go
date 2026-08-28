package assistant

import (
	"errors"
	"time"
)

type ScheduleStore interface {
	List() ([]Assistant, error)
	Routines(string) ([]Routine, error)
	// DueRoutineFires returns the idempotent scheduled fires due at now. The
	// scheduler no longer creates Runs; fires are turned into managed Sessions
	// by the host control plane.
	DueRoutineFires(now time.Time) ([]RoutineFire, error)
}

type Scheduler struct {
	store ScheduleStore
}

var _ ScheduleStore = (*Store)(nil)

func NewScheduler(store ScheduleStore) (*Scheduler, error) {
	if store == nil {
		return nil, errors.New("assistant: scheduler store is required")
	}
	return &Scheduler{store: store}, nil
}

type ScheduleFailure struct {
	AssistantID string `json:"assistant_id"`
	RoutineID   string `json:"routine_id"`
	Error       string `json:"error"`
}

type TickResult struct {
	Runs     []Run             `json:"runs"`
	Fires    []RoutineFire     `json:"fires"`
	Skipped  int               `json:"skipped"`
	Failures []ScheduleFailure `json:"failures"`
}

// Tick collects the idempotent fires due at now. The store advances each
// routine cursor in the same write that records its fire, so a crash between
// writes safely replays the same occurrence on the next Tick without creating
// duplicate work.
func (s *Scheduler) Tick(now time.Time) (TickResult, error) {
	now = utcNow(now)
	if gate, ok := s.store.(interface{ WorkControl() (WorkControl, error) }); ok {
		wc, err := gate.WorkControl()
		if err != nil {
			return TickResult{}, err
		}
		// RECOVERING re-drives unconsumed routine fires (resume_all scans them),
		// so the scheduler admits it like RUNNING; QUIESCING/PAUSED stay quiet.
		if wc.State != WorkRunning && wc.State != WorkRecovering {
			return TickResult{}, nil
		}
	}
	fires, err := s.store.DueRoutineFires(now)
	result := TickResult{Fires: fires}
	if err != nil {
		result.addFailure("", "", err)
	}
	return result, err
}

func (r *TickResult) addFailure(assistantID, routineID string, err error) {
	r.Failures = append(r.Failures, ScheduleFailure{AssistantID: assistantID, RoutineID: routineID, Error: err.Error()})
}
