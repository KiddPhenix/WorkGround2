package runhub

import (
	"strconv"
	"time"
)

// Reduce applies evt to run and returns the next run with Revision incremented.
// It never mutates run. It rejects with *TransitionError when the event cannot
// be applied: a terminal run is frozen, an optimistic ExpectRevision does not
// match, or the state transition is not allowed.
//
// Reduce is the single place where run state changes; the Hub calls it after
// idempotency checks and before persisting the result.
func Reduce(run AgentRun, evt RunEvent) (AgentRun, error) {
	if !run.State.Valid() {
		return run, &TransitionError{Code: TransitionInvalid, Msg: "invalid run state " + string(run.State)}
	}
	if !evt.Type.Valid() {
		return run, &TransitionError{Code: TransitionInvalid, Msg: "unknown event type " + string(evt.Type)}
	}
	if evt.Source != "" && !evt.Source.Valid() {
		return run, &TransitionError{Code: TransitionInvalid, Msg: "invalid source " + string(evt.Source)}
	}
	if run.Source != "" && evt.Source != "" && evt.Source != run.Source {
		return run, &TransitionError{
			Code: TransitionInvalid,
			Msg:  "event source " + string(evt.Source) + " conflicts with run source " + string(run.Source),
		}
	}
	if evt.Payload.Activity != "" && !evt.Payload.Activity.Valid() {
		return run, &TransitionError{Code: TransitionInvalid, Msg: "invalid activity " + string(evt.Payload.Activity)}
	}

	if run.State.IsTerminal() {
		return run, &TransitionError{Code: TransitionStale, Msg: "terminal state " + string(run.State) + " is final"}
	}
	if evt.ExpectRevision != 0 && evt.ExpectRevision != run.Revision {
		return run, &TransitionError{
			Code: TransitionStale,
			Msg:  "expected revision " + strconv.FormatUint(evt.ExpectRevision, 10) + " but run is at " + strconv.FormatUint(run.Revision, 10),
		}
	}

	next := run
	if target, ok := targetState(evt.Type); ok {
		if !canTransition(run.State, target) {
			return run, &TransitionError{
				Code: TransitionInvalid,
				Msg:  "transition " + string(run.State) + " -> " + string(target) + " is not allowed",
			}
		}
		next.State = target
		if target.IsTerminal() {
			next.Activity = ActivityIdle
			next.ActivityLabel = ""
		}
	} else {
		applyMetadata(&next, evt)
	}

	if next.Source == "" && evt.Source != "" {
		next.Source = evt.Source
	}

	ts := evt.OccurredAt.Round(0)
	if ts.IsZero() {
		ts = time.Now().Round(0)
	}
	if ts.After(next.LastSeenAt) {
		next.LastSeenAt = ts
	}
	next.UpdatedAt = time.Now().Round(0)
	next.Revision++
	return next, nil
}

// targetState maps an event type to the state it forces, or ok=false for
// metadata-only events that refine a run without changing its phase.
func targetState(t EventType) (RunState, bool) {
	switch t {
	case EventQueued:
		return StateQueued, true
	case EventStarting:
		return StateStarting, true
	case EventRunning:
		return StateRunning, true
	case EventWaitingUser:
		return StateWaitingUser, true
	case EventSucceeded:
		return StateSucceeded, true
	case EventFailed:
		return StateFailed, true
	case EventCancelled:
		return StateCancelled, true
	case EventInterrupted:
		return StateInterrupted, true
	case EventStale:
		return StateStale, true
	}
	return "", false
}

// canTransition reports whether from may move to to. Terminal targets are
// always reachable from any non-terminal state; terminal sources are handled
// before this is consulted. Within non-terminal states it forbids regression
// (starting -> queued, running -> starting) while allowing waiting_user -> running.
func canTransition(from, to RunState) bool {
	if to.IsTerminal() {
		return true
	}
	switch from {
	case StateQueued:
		return to == StateQueued || to == StateStarting || to == StateRunning || to == StateWaitingUser
	case StateStarting:
		return to == StateStarting || to == StateRunning || to == StateWaitingUser
	case StateRunning:
		return to == StateRunning || to == StateWaitingUser
	case StateWaitingUser:
		return to == StateWaitingUser || to == StateRunning
	}
	return false
}

// applyMetadata folds a metadata-only event into the run.
func applyMetadata(run *AgentRun, evt RunEvent) {
	switch evt.Type {
	case EventActivity:
		if evt.Payload.Activity != "" {
			run.Activity = evt.Payload.Activity
		}
		if evt.Payload.Label != "" {
			run.ActivityLabel = evt.Payload.Label
		}
	case EventSummary:
		if evt.Payload.Summary != "" {
			run.Summary = evt.Payload.Summary
		} else if evt.Payload.Detail != "" {
			run.Summary = evt.Payload.Detail
		}
	case EventTitle:
		if evt.Payload.Title != "" {
			run.Title = evt.Payload.Title
		} else if evt.Payload.Label != "" {
			run.Title = evt.Payload.Label
		}
	}
}
