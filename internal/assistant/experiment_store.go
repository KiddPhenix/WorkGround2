package assistant

import (
	"fmt"
	"time"
)

// RecordExperimentInput upserts one Experiment under revision CAS.
type RecordExperimentInput struct {
	RequestID        string
	Experiment       Experiment
	ExpectedRevision int64
	Now              time.Time
}

// RecordExperiment creates or updates one isolated-trial record idempotently by
// request ID. A stale expected_revision is rejected so a late conclusion cannot
// overwrite a newer edit. The trial's Session carries execution state; the
// Experiment only records the decision evidence.
func (s *Store) RecordExperiment(in RecordExperimentInput) (Experiment, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return Experiment{}, err
	}
	if err := validateID("assistant", in.Experiment.AssistantID); err != nil {
		return Experiment{}, err
	}
	fingerprint, err := inputFingerprint(struct {
		Experiment Experiment
		Expected   int64
	}{experimentIntent(in.Experiment), in.ExpectedRevision})
	if err != nil {
		return Experiment{}, err
	}
	unlock, err := s.lockAssistant(in.Experiment.AssistantID)
	if err != nil {
		return Experiment{}, err
	}
	defer unlock()
	agg, err := s.read(in.Experiment.AssistantID)
	if err != nil {
		return Experiment{}, err
	}
	if result, ok, receiptErr := receiptResult[Experiment](agg, in.RequestID, "record_experiment", fingerprint); ok || receiptErr != nil {
		return result, receiptErr
	}
	now := storeNow(in.Now)
	idx := experimentIndex(agg, in.Experiment.ID)
	if idx < 0 && in.ExpectedRevision != 0 {
		return Experiment{}, conflict("experiment", in.Experiment.ID, in.ExpectedRevision, 0)
	}
	if idx >= 0 && agg.Experiments[idx].Revision != in.ExpectedRevision {
		return Experiment{}, conflict("experiment", in.Experiment.ID, in.ExpectedRevision, agg.Experiments[idx].Revision)
	}
	e := in.Experiment
	if idx < 0 {
		e.Revision, e.CreatedAt = 1, now
	} else {
		e.Revision, e.CreatedAt = agg.Experiments[idx].Revision+1, agg.Experiments[idx].CreatedAt
	}
	e.UpdatedAt = now
	if err := validateExperimentRecord(e, agg); err != nil {
		return Experiment{}, err
	}
	if idx < 0 {
		agg.Experiments = append(agg.Experiments, e)
	} else {
		agg.Experiments[idx] = e
	}
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "record_experiment", fingerprint, e, now); err != nil {
		return Experiment{}, err
	}
	if err := s.write(agg); err != nil {
		return Experiment{}, err
	}
	return clone(e), nil
}

func experimentIndex(agg *aggregate, id string) int {
	for i := range agg.Experiments {
		if agg.Experiments[i].ID == id {
			return i
		}
	}
	return -1
}

func experimentIntent(e Experiment) Experiment {
	e.Revision = 0
	e.CreatedAt, e.UpdatedAt = time.Time{}, time.Time{}
	return e
}

// validateExperimentRecord checks a single experiment against the aggregate's
// responsibility IDs before it is committed.
func validateExperimentRecord(e Experiment, agg *aggregate) error {
	if err := validateID("experiment", e.ID); err != nil {
		return err
	}
	if e.AssistantID != agg.Assistant.ID {
		return fmt.Errorf("experiment %s belongs to %s", e.ID, e.AssistantID)
	}
	if e.RespID != "" {
		ids := make(map[string]bool, len(agg.Plan.Responsibilities))
		for _, r := range agg.Plan.Responsibilities {
			ids[r.ID] = true
		}
		if !ids[e.RespID] {
			return fmt.Errorf("experiment %s references missing responsibility %s", e.ID, e.RespID)
		}
	}
	return nil
}
