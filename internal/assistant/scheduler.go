package assistant

import (
	"errors"
	"fmt"
	"time"
)

type ScheduleStore interface {
	List() ([]Assistant, error)
	Routines(string) ([]Routine, error)
	CreateOccurrence(TriggerInput) (*Run, error)
	AdvanceRoutine(assistantID, routineID, requestID string, expected int64, scheduledFor, now time.Time) (*Routine, error)
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
	Skipped  int               `json:"skipped"`
	Failures []ScheduleFailure `json:"failures"`
}

// Tick persists each run before advancing its routine cursor. A crash between
// those writes safely replays the same occurrence request on the next Tick.
func (s *Scheduler) Tick(now time.Time) (TickResult, error) {
	now = utcNow(now)
	assistants, err := s.store.List()
	if err != nil && len(assistants) == 0 {
		return TickResult{}, fmt.Errorf("assistant: list scheduled assistants: %w", err)
	}
	var result TickResult
	var failures []error
	if err != nil {
		result.addFailure("", "", err)
		failures = append(failures, err)
	}
	for _, a := range assistants {
		if a.Lifecycle != LifecycleActive {
			continue
		}
		routines, err := s.store.Routines(a.ID)
		if err != nil {
			result.addFailure(a.ID, "", err)
			failures = append(failures, err)
			continue
		}
		for _, routine := range routines {
			if !routine.Enabled || routine.Schedule.Kind == ScheduleManual {
				continue
			}
			cursor := routine.LastScheduledFor
			if cursor.IsZero() {
				cursor = routine.CreatedAt
			}
			latest, count, err := latestDue(routine.Schedule, cursor, now)
			if err != nil {
				result.addFailure(a.ID, routine.ID, err)
				failures = append(failures, err)
				continue
			}
			if count == 0 {
				continue
			}
			requestID := "occurrence:" + OccurrenceKey(a.ID, routine.ID, latest)
			if routine.CatchUp == CatchUpSkip && count > 1 {
				if err := s.advance(routine, requestID, latest, now); err != nil {
					result.addFailure(a.ID, routine.ID, err)
					failures = append(failures, err)
					continue
				}
				result.Skipped += count
				continue
			}
			run, err := s.store.CreateOccurrence(TriggerInput{
				AssistantID: a.ID, RoutineID: routine.ID, RequestID: requestID,
				Trigger: TriggerScheduled, ScheduledFor: latest, Now: now,
			})
			if err != nil {
				result.addFailure(a.ID, routine.ID, err)
				failures = append(failures, err)
				continue
			}
			if err := s.advance(routine, requestID, latest, now); err != nil {
				result.addFailure(a.ID, routine.ID, err)
				failures = append(failures, err)
				continue
			}
			result.Runs = append(result.Runs, *run)
		}
	}
	return result, errors.Join(failures...)
}

func (s *Scheduler) advance(routine Routine, occurrenceRequest string, scheduledFor, now time.Time) error {
	requestID := "cursor:" + occurrenceRequest
	_, err := s.store.AdvanceRoutine(routine.AssistantID, routine.ID, requestID, routine.Revision, scheduledFor, now)
	if !errors.Is(err, ErrConflict) {
		return err
	}
	// Concurrent ticks may both create the same occurrence. A cursor already at
	// or past our occurrence proves the other tick completed the same commit.
	current, readErr := s.store.Routines(routine.AssistantID)
	if readErr != nil {
		return errors.Join(err, readErr)
	}
	for _, value := range current {
		if value.ID == routine.ID && !value.LastScheduledFor.Before(scheduledFor) {
			return nil
		}
	}
	return err
}

func (r *TickResult) addFailure(assistantID, routineID string, err error) {
	r.Failures = append(r.Failures, ScheduleFailure{AssistantID: assistantID, RoutineID: routineID, Error: err.Error()})
}
