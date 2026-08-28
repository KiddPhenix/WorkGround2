package assistant

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// RoutineFireState is the durable lifecycle state of a routine fire.
type RoutineFireState string

const (
	// FirePending means the fire has been materialized but not yet bound to a
	// managed Session. DueRoutineFires keeps returning pending fires across
	// crashes and restarts until they are consumed.
	FirePending RoutineFireState = "pending"
	// FireConsumed means the fire has been bound to a managed Session. A
	// consumed fire is never returned by DueRoutineFires again.
	FireConsumed RoutineFireState = "consumed"
)

// RoutineFire is a scheduled routine occurrence materialized as an idempotent
// fire intent. It deliberately carries no Run: the converged Session control
// plane turns a fire into a managed Session, so Runs/Jobs are never created or
// claimed by the scheduling path again. The fire itself is durable — unlike the
// legacy Occurrences-only ledger, a fire that has not been consumed survives a
// crash between "record" and "create Session", so it can be compensated.
type RoutineFire struct {
	FireID        string           `json:"fire_id"`
	OccurrenceKey string           `json:"occurrence_key,omitempty"`
	AssistantID   string           `json:"assistant_id"`
	RoutineID     string           `json:"routine_id"`
	Title         string           `json:"title"`
	Prompt        string           `json:"prompt"`
	ScheduledFor  time.Time        `json:"scheduled_for" ts_type:"string"`
	State         RoutineFireState `json:"state"`
	SessionID     string           `json:"session_id,omitempty"`
	Revision      int64            `json:"revision"`
	CreatedAt     time.Time        `json:"created_at" ts_type:"string"`
	UpdatedAt     time.Time        `json:"updated_at" ts_type:"string"`
}

// DueRoutineFires returns the idempotent fires due at now. For each due
// occurrence it records a pending fire in the aggregate's durable Fires ledger
// (keyed by OccurrenceKey, alongside the legacy Occurrences idempotency map)
// and advances the routine cursor in the same aggregate write, so a crash or a
// concurrent tick replays the same occurrence without producing a second fire
// and without ever creating a Run. It returns every still-pending fire —
// including ones recorded by a previous pass that crashed before their Session
// was created — so fires are compensable. CatchUpSkip routines skip merging:
// their cursor advances with no fire.
func (s *Store) DueRoutineFires(now time.Time) ([]RoutineFire, error) {
	now = utcNow(now)
	assistants, err := s.List()
	if err != nil && len(assistants) == 0 {
		return nil, err
	}
	var fires []RoutineFire
	var failures []error
	if err != nil {
		failures = append(failures, err)
	}
	for _, a := range assistants {
		if a.Lifecycle != LifecycleActive {
			continue
		}
		more, err := s.dueFiresForAssistant(a, now)
		if err != nil {
			failures = append(failures, err)
		}
		fires = append(fires, more...)
	}
	return fires, errors.Join(failures...)
}

func (s *Store) dueFiresForAssistant(a Assistant, now time.Time) ([]RoutineFire, error) {
	unlock, err := s.lockAssistant(a.ID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	agg, err := s.read(a.ID)
	if err != nil {
		return nil, err
	}
	at := storeNow(now)
	for i := range agg.Routines {
		routine := &agg.Routines[i]
		if !routine.Enabled || routine.Schedule.Kind == ScheduleManual {
			continue
		}
		cursor := routine.LastScheduledFor
		if cursor.IsZero() {
			cursor = routine.CreatedAt
		}
		latest, count, err := latestDue(routine.Schedule, cursor, at)
		if err != nil {
			return pendingFires(agg), err
		}
		if count == 0 {
			continue
		}
		if routine.CatchUp == CatchUpSkip && count > 1 {
			// Skip merging: advance the cursor past the whole missed window and
			// produce no fire, exactly like the legacy scheduler's skip path.
			routine.LastScheduledFor = latest
			routine.Revision++
			routine.UpdatedAt = at
			continue
		}
		occurrenceKey := OccurrenceKey(a.ID, routine.ID, latest)
		if agg.Occurrences[occurrenceKey] != "" || fireIndexByKey(agg, occurrenceKey) >= 0 {
			// Already materialized (a historical Run or an earlier fire). The
			// cursor must already be past it; never emit a duplicate.
			continue
		}
		fire := RoutineFire{
			FireID:        StableID("fire", occurrenceKey),
			OccurrenceKey: occurrenceKey,
			AssistantID:   a.ID,
			RoutineID:     routine.ID,
			Title:         routine.Title,
			Prompt:        routine.Prompt,
			ScheduledFor:  latest,
			State:         FirePending,
			Revision:      1,
			CreatedAt:     at,
			UpdatedAt:     at,
		}
		agg.Fires = append(agg.Fires, fire)
		agg.Occurrences[occurrenceKey] = fire.FireID
		routine.LastScheduledFor = latest
		routine.Revision++
		routine.UpdatedAt = at
	}
	pending := pendingFires(agg)
	touch(agg, at)
	if err := s.write(agg); err != nil {
		return pending, err
	}
	return pending, nil
}

// ConsumeRoutineFire durably binds a pending fire to the managed Session that
// executed it and marks it consumed. It is idempotent: replaying the same
// request ID (a lost response, crash, or restart) returns the previously
// recorded result, and consuming an already-consumed fire returns its original
// binding without re-binding.
func (s *Store) ConsumeRoutineFire(assistantID, fireID, sessionID, requestID string, now time.Time) (RoutineFire, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return RoutineFire{}, err
	}
	if err := validateID("fire", fireID); err != nil {
		return RoutineFire{}, err
	}
	if err := validateRequestID(requestID); err != nil {
		return RoutineFire{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return RoutineFire{}, errors.New("assistant: session id is required")
	}
	fp, err := inputFingerprint(struct {
		FireID    string
		SessionID string
	}{fireID, sessionID})
	if err != nil {
		return RoutineFire{}, err
	}
	unlock, err := s.lockAssistant(assistantID)
	if err != nil {
		return RoutineFire{}, err
	}
	defer unlock()
	agg, err := s.read(assistantID)
	if err != nil {
		return RoutineFire{}, err
	}
	if result, ok, receiptErr := receiptResult[RoutineFire](agg, requestID, "consume_routine_fire", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	idx := fireIndex(agg, fireID)
	if idx < 0 {
		return RoutineFire{}, ErrNotFound
	}
	fire := &agg.Fires[idx]
	if fire.State == FireConsumed {
		// First write wins: an already-consumed fire stays bound to its original
		// Session, so a retried consume can never re-bind it.
		return clone(*fire), nil
	}
	at := storeNow(now)
	fire.State = FireConsumed
	fire.SessionID = sessionID
	fire.Revision++
	fire.UpdatedAt = at
	result := clone(*fire)
	touch(agg, at)
	if err := putReceipt(agg, requestID, "consume_routine_fire", fp, result, at); err != nil {
		return RoutineFire{}, err
	}
	if err := s.write(agg); err != nil {
		return RoutineFire{}, err
	}
	return result, nil
}

func fireIndex(agg *aggregate, fireID string) int {
	for i := range agg.Fires {
		if agg.Fires[i].FireID == fireID {
			return i
		}
	}
	return -1
}

func fireIndexByKey(agg *aggregate, occurrenceKey string) int {
	for i := range agg.Fires {
		if agg.Fires[i].OccurrenceKey == occurrenceKey {
			return i
		}
	}
	return -1
}

func pendingFires(agg *aggregate) []RoutineFire {
	fires := make([]RoutineFire, 0, len(agg.Fires))
	for _, fire := range agg.Fires {
		if fire.State == FirePending {
			fires = append(fires, clone(fire))
		}
	}
	return fires
}

func validateRoutineFire(fire RoutineFire) error {
	if err := validateID("fire", fire.FireID); err != nil {
		return err
	}
	if err := validateID("assistant", fire.AssistantID); err != nil {
		return err
	}
	if err := validateID("routine", fire.RoutineID); err != nil {
		return err
	}
	switch fire.State {
	case FirePending, FireConsumed:
	default:
		return fmt.Errorf("assistant: fire %s has invalid state %q", fire.FireID, fire.State)
	}
	if fire.OccurrenceKey == "" {
		return fmt.Errorf("assistant: fire %s has no occurrence key", fire.FireID)
	}
	if fire.FireID != StableID("fire", fire.OccurrenceKey) {
		return fmt.Errorf("assistant: fire %s does not match its occurrence key", fire.FireID)
	}
	if fire.ScheduledFor.IsZero() {
		return fmt.Errorf("assistant: fire %s has no scheduled time", fire.FireID)
	}
	if fire.Revision < 1 || fire.CreatedAt.IsZero() || fire.UpdatedAt.IsZero() {
		return fmt.Errorf("assistant: fire %s has invalid revision or timestamps", fire.FireID)
	}
	if fire.State == FireConsumed && strings.TrimSpace(fire.SessionID) == "" {
		return fmt.Errorf("assistant: consumed fire %s has no session id", fire.FireID)
	}
	return nil
}
