package assistant

import (
	"errors"
	"strings"
	"time"
)

// InteractionDecisionRecord is the durable, auditable receipt of one
// auto-response routing decision for a pending interaction. It carries the
// decision source, the model's confidence/candidates/rationale, the concrete
// outcome (Result) and the rollback point (the isolated fork session for
// experiments), plus the decision deadline (DueAt) so a deferred decision can
// resume across ticks and restarts. Trials records the isolated experiment
// candidates (each a fork Session ID + its submitted answer + running/done
// status) so the winner-selection sweep can recover across ticks and restarts.
type InteractionDecisionRecord struct {
	ID            string         `json:"id"`
	AssistantID   string         `json:"assistant_id"`
	SessionID     string         `json:"session_id"`
	InteractionID string         `json:"interaction_id"`
	Source        DecisionSource `json:"source"`
	HardGate      HardGateReason `json:"hard_gate,omitempty"`
	Confidence    float64        `json:"confidence,omitempty"`
	Candidates    []string       `json:"candidates,omitempty"`
	Rationale     string         `json:"rationale,omitempty"`
	Result        string         `json:"result,omitempty"`
	Rollback      string         `json:"rollback,omitempty"`
	Trials        []TrialState   `json:"trials,omitempty"`
	// Winner is the winning trial session ID after an experiment settles
	// (empty for a fallback answer); Evidence/Cost/SideEffects record the
	// bounded comparison that picked it.
	Winner      string    `json:"winner,omitempty"`
	Evidence    string    `json:"evidence,omitempty"`
	Cost        string    `json:"cost,omitempty"`
	SideEffects string    `json:"side_effects,omitempty"`
	DueAt       time.Time `json:"due_at,omitempty" ts_type:"string"`
	CreatedAt   time.Time `json:"created_at" ts_type:"string"`
}

// RecordInteractionDecision persists one auto-response decision idempotently.
// The record ID is the receipt key: replaying the same ID returns the already
// stored record instead of appending a duplicate, so a crash between deciding
// and answering cannot double-record the same decision.
func (s *Store) RecordInteractionDecision(rec InteractionDecisionRecord) (InteractionDecisionRecord, error) {
	if err := validateID("assistant", rec.AssistantID); err != nil {
		return InteractionDecisionRecord{}, err
	}
	if err := validateID("decision", rec.ID); err != nil {
		return InteractionDecisionRecord{}, err
	}
	if strings.TrimSpace(rec.SessionID) == "" || strings.TrimSpace(rec.InteractionID) == "" {
		return InteractionDecisionRecord{}, errors.New("assistant: decision session and interaction are required")
	}
	unlock, err := s.lockAssistant(rec.AssistantID)
	if err != nil {
		return InteractionDecisionRecord{}, err
	}
	defer unlock()
	agg, err := s.read(rec.AssistantID)
	if err != nil {
		return InteractionDecisionRecord{}, err
	}
	for i := range agg.Decisions {
		if agg.Decisions[i].ID == rec.ID {
			return clone(agg.Decisions[i]), nil
		}
	}
	now := storeNow(rec.CreatedAt)
	rec.CreatedAt = now
	agg.Decisions = append(agg.Decisions, rec)
	touch(agg, now)
	if err := s.write(agg); err != nil {
		return InteractionDecisionRecord{}, err
	}
	return clone(rec), nil
}

// LatestDecision returns the most recently recorded decision for one
// interaction, or ok=false when none has been recorded yet.
func (s *Store) LatestDecision(assistantID, sessionID, interactionID string) (InteractionDecisionRecord, bool, error) {
	if err := validateID("assistant", assistantID); err != nil {
		return InteractionDecisionRecord{}, false, err
	}
	snapshot, err := s.Get(assistantID)
	if err != nil {
		return InteractionDecisionRecord{}, false, err
	}
	var latest InteractionDecisionRecord
	found := false
	for _, rec := range snapshot.Decisions {
		if rec.SessionID != sessionID || rec.InteractionID != interactionID {
			continue
		}
		if !found || rec.CreatedAt.After(latest.CreatedAt) {
			latest, found = rec, true
		}
	}
	return latest, found, nil
}
