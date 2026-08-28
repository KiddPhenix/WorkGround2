package assistant

import (
	"time"
)

// DeleteRoutineInput removes a routine by stable ID under revision CAS.
type DeleteRoutineInput struct {
	RequestID        string
	AssistantID      string
	RoutineID        string
	ExpectedRevision int64
	Now              time.Time
}

// RunNowInput runs a routine immediately as an independent manual fire. The
// RequestID is the stable fire idempotency key: replaying the same request
// returns the same Run instead of creating a second one.
type RunNowInput struct {
	AssistantID string
	RoutineID   string
	RequestID   string
	MaxAttempts int
	Now         time.Time
}

// DeleteRoutine removes a routine durably and idempotently by request ID. A
// stale expected_revision is rejected so a late delete cannot remove a routine
// the user just edited; a repeat with the same request ID replays the original
// result. Deleting a routine does not cancel any already-running Session.
func (s *Store) DeleteRoutine(in DeleteRoutineInput) (*Routine, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return nil, err
	}
	if err := validateID("routine", in.RoutineID); err != nil {
		return nil, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return nil, err
	}
	fingerprint, err := inputFingerprint(struct {
		RoutineID string
		Expected  int64
	}{in.RoutineID, in.ExpectedRevision})
	if err != nil {
		return nil, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return nil, err
	}
	if result, ok, receiptErr := receiptResult[Routine](agg, in.RequestID, "delete_routine", fingerprint); ok || receiptErr != nil {
		return &result, receiptErr
	}
	idx := routineIndex(agg, in.RoutineID)
	if idx < 0 {
		return nil, ErrNotFound
	}
	routine := agg.Routines[idx]
	if in.ExpectedRevision != 0 && routine.Revision != in.ExpectedRevision {
		return nil, conflict("routine", routine.ID, in.ExpectedRevision, routine.Revision)
	}
	agg.Routines = append(agg.Routines[:idx], agg.Routines[idx+1:]...)
	now := storeNow(in.Now)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "delete_routine", fingerprint, routine, now); err != nil {
		return nil, err
	}
	if err := s.write(agg); err != nil {
		return nil, err
	}
	result := clone(routine)
	return &result, nil
}

// RunNow fires one routine immediately. It is a manual trigger that reuses the
// store's request receipt so a duplicated request (double-click, retry after a
// lost response, leader switch, or restart replay) returns the same Run instead
// of spawning a second Session.
func (s *Store) RunNow(in RunNowInput) (Run, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return Run{}, err
	}
	if err := validateID("routine", in.RoutineID); err != nil {
		return Run{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return Run{}, err
	}
	if in.MaxAttempts < 1 {
		in.MaxAttempts = 3
	}
	run, err := s.Trigger(TriggerInput{
		AssistantID: in.AssistantID, RoutineID: in.RoutineID,
		RequestID: in.RequestID, Trigger: TriggerManual,
		MaxAttempts: in.MaxAttempts, Now: in.Now,
	})
	if err != nil {
		return Run{}, err
	}
	return run, nil
}
