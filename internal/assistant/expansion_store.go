package assistant

import (
	"fmt"
	"strings"
	"time"
)

// RecordExpansionInput records one expansion pass outcome. A success clears the
// backoff; a failure (or a fruitless pass with no adoptable candidate) bumps the
// attempt and sets a bounded exponential BackoffUntil so the loop cannot spin.
type RecordExpansionInput struct {
	RequestID   string
	AssistantID string
	Trigger     ExpansionTrigger
	Err         string
	Now         time.Time
}

// RecordExpansion persists the observable expansion-loop state, idempotently by
// request ID. The host calls it once per expansion pass with the outcome.
func (s *Store) RecordExpansion(in RecordExpansionInput) (ExpansionState, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return ExpansionState{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return ExpansionState{}, err
	}
	in.Err = strings.TrimSpace(in.Err)
	fp, err := inputFingerprint(struct {
		Trigger ExpansionTrigger
		Err     string
	}{in.Trigger, in.Err})
	if err != nil {
		return ExpansionState{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return ExpansionState{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return ExpansionState{}, err
	}
	if result, ok, receiptErr := receiptResult[ExpansionState](agg, in.RequestID, "record_expansion", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	now := storeNow(in.Now)
	st := agg.Expansion
	st.LastTriggerAt = now
	st.LastTrigger = in.Trigger
	if in.Err == "" {
		// A successful pass produced adoptable work (or at least ran cleanly):
		// reset the backoff so the next trigger is honored immediately.
		st.Attempt = 0
		st.BackoffUntil = time.Time{}
		st.Error = ""
	} else {
		st.Attempt++
		st.Error = in.Err
		st.BackoffUntil = now.Add(expansionBackoff(st.Attempt))
	}
	agg.Expansion = st
	result := clone(st)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "record_expansion", fp, result, now); err != nil {
		return ExpansionState{}, err
	}
	if err := validateAggregate(agg); err != nil {
		return ExpansionState{}, fmt.Errorf("%w: record expansion: %v", ErrCorrupt, err)
	}
	if err := s.write(agg); err != nil {
		return ExpansionState{}, err
	}
	return result, nil
}

// ExpansionDue reports whether the expansion loop may run now for an assistant,
// honoring the bounded backoff from the last fruitless/failed pass.
func (s *Store) ExpansionDue(assistantID string, now time.Time) (bool, ExpansionState, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return false, ExpansionState{}, err
	}
	agg, err := s.read(assistantID)
	if err != nil {
		return false, ExpansionState{}, err
	}
	return agg.Expansion.expansionDue(storeNow(now)), clone(agg.Expansion), nil
}
