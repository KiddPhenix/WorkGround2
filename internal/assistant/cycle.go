package assistant

import (
	"fmt"
	"time"
)

// CycleState is the lifecycle of one supervisor reasoning round.
type CycleState string

const (
	CycleStarted      CycleState = "started"
	CycleCheckpointed CycleState = "checkpointed"
	CycleCompleted    CycleState = "completed"
)

// CycleObservation is the bounded set of revisions the supervisor read at the
// start of a cycle. It makes a decision traceable to its inputs: a late result
// composed against an older plan/policy/work epoch cannot overwrite a newer one.
type CycleObservation struct {
	PlanRevision      int64 `json:"plan_revision"`
	AssistantRevision int64 `json:"assistant_revision"`
	MemoryRevision    int64 `json:"memory_revision"`
	WorkEpoch         int64 `json:"work_epoch"`
}

// SupervisorCycle is one durable, recoverable reasoning round. Only the current
// cycle is retained: it records the observed revisions, the persisted next step,
// and a monotonic fence so a crash or leader switch can resume from the
// checkpoint instead of re-deriving state.
type SupervisorCycle struct {
	ID          string           `json:"id"`
	AssistantID string           `json:"assistant_id"`
	Fence       int64            `json:"fence"`
	State       CycleState       `json:"state"`
	Observed    CycleObservation `json:"observed"`
	NextStep    string           `json:"next_step,omitempty"`
	Revision    int64            `json:"revision"`
	CreatedAt   time.Time        `json:"created_at" ts_type:"string"`
	UpdatedAt   time.Time        `json:"updated_at" ts_type:"string"`
}

func validCycleState(s CycleState) bool {
	switch s {
	case CycleStarted, CycleCheckpointed, CycleCompleted:
		return true
	}
	return false
}

func validateCycle(c SupervisorCycle) error {
	if c.ID == "" {
		return nil // no cycle yet
	}
	if err := validateID("cycle", c.ID); err != nil {
		return err
	}
	if err := validateID("assistant", c.AssistantID); err != nil {
		return err
	}
	if c.Fence < 1 || c.Revision < 1 || c.CreatedAt.IsZero() || c.UpdatedAt.IsZero() {
		return fmt.Errorf("cycle %s has invalid fence, revision, or timestamps", c.ID)
	}
	if !validCycleState(c.State) {
		return fmt.Errorf("cycle %s has invalid state %q", c.ID, c.State)
	}
	return nil
}

// OpenCycleInput opens the next supervisor cycle for an assistant.
type OpenCycleInput struct {
	AssistantID string
	RequestID   string
	Observed    CycleObservation
	Now         time.Time
}

// CheckpointCycleInput persists the next step of an in-flight cycle under its
// fence. It is the durable resume point.
type CheckpointCycleInput struct {
	AssistantID string
	CycleID     string
	RequestID   string
	Fence       int64
	NextStep    string
	Now         time.Time
}

// CompleteCycleInput marks a cycle completed under its fence.
type CompleteCycleInput struct {
	AssistantID string
	CycleID     string
	RequestID   string
	Fence       int64
	Now         time.Time
}

// LatestCycle returns the current supervisor cycle for an assistant, or
// ok=false when none has been opened yet.
func (s *Store) LatestCycle(assistantID string) (SupervisorCycle, bool) {
	snapshot, err := s.Get(assistantID)
	if err != nil || snapshot.Cycle.ID == "" {
		return SupervisorCycle{}, false
	}
	return snapshot.Cycle, true
}

// OpenCycle opens the next cycle, bumping the fence from the current one. It is
// idempotent by request ID; a duplicate request returns the already-opened
// cycle instead of opening a second one.
func (s *Store) OpenCycle(in OpenCycleInput) (SupervisorCycle, error) {
	if err := validateID("assistant", in.AssistantID); err != nil {
		return SupervisorCycle{}, err
	}
	if err := validateRequestID(in.RequestID); err != nil {
		return SupervisorCycle{}, err
	}
	fp, err := inputFingerprint(struct {
		AssistantID string
		Observed    CycleObservation
	}{in.AssistantID, in.Observed})
	if err != nil {
		return SupervisorCycle{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	if result, ok, receiptErr := receiptResult[SupervisorCycle](agg, in.RequestID, "open_cycle", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	now := storeNow(in.Now)
	fence := agg.Cycle.Fence + 1
	if fence < 1 {
		fence = 1
	}
	cycle := SupervisorCycle{
		ID:          StableID("cycle", fmt.Sprintf("%s/%d", in.AssistantID, fence)),
		AssistantID: in.AssistantID, Fence: fence, State: CycleStarted,
		Observed: in.Observed, Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	agg.Cycle = cycle
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "open_cycle", fp, cycle, now); err != nil {
		return SupervisorCycle{}, err
	}
	if err := s.write(agg); err != nil {
		return SupervisorCycle{}, err
	}
	return clone(cycle), nil
}

// CheckpointCycle persists the resume point of a cycle under its fence.
func (s *Store) CheckpointCycle(in CheckpointCycleInput) (SupervisorCycle, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return SupervisorCycle{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return SupervisorCycle{}, err
	}
	if err := validateID("cycle", in.CycleID); err != nil {
		return SupervisorCycle{}, err
	}
	fp, err := inputFingerprint(struct {
		CycleID  string
		Fence    int64
		NextStep string
	}{in.CycleID, in.Fence, in.NextStep})
	if err != nil {
		return SupervisorCycle{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	if result, ok, receiptErr := receiptResult[SupervisorCycle](agg, in.RequestID, "checkpoint_cycle", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	cycle := &agg.Cycle
	if cycle.ID != in.CycleID || cycle.Fence != in.Fence {
		return SupervisorCycle{}, fmt.Errorf("assistant: cycle %s fence %d is stale: %w", in.CycleID, in.Fence, ErrLeaseLost)
	}
	if cycle.State != CycleStarted && cycle.State != CycleCheckpointed {
		return SupervisorCycle{}, fmt.Errorf("%w: cannot checkpoint cycle in %s", ErrTransition, cycle.State)
	}
	now := storeNow(in.Now)
	cycle.NextStep = in.NextStep
	cycle.State = CycleCheckpointed
	cycle.Revision++
	cycle.UpdatedAt = now
	result := clone(*cycle)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "checkpoint_cycle", fp, result, now); err != nil {
		return SupervisorCycle{}, err
	}
	if err := s.write(agg); err != nil {
		return SupervisorCycle{}, err
	}
	return result, nil
}

// CompleteCycle marks a cycle completed under its fence.
func (s *Store) CompleteCycle(in CompleteCycleInput) (SupervisorCycle, error) {
	if err := validateRequestID(in.RequestID); err != nil {
		return SupervisorCycle{}, err
	}
	if err := validateID("assistant", in.AssistantID); err != nil {
		return SupervisorCycle{}, err
	}
	if err := validateID("cycle", in.CycleID); err != nil {
		return SupervisorCycle{}, err
	}
	fp, err := inputFingerprint(struct {
		CycleID string
		Fence   int64
	}{in.CycleID, in.Fence})
	if err != nil {
		return SupervisorCycle{}, err
	}
	unlock, err := s.lockAssistant(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	defer unlock()
	agg, err := s.read(in.AssistantID)
	if err != nil {
		return SupervisorCycle{}, err
	}
	if result, ok, receiptErr := receiptResult[SupervisorCycle](agg, in.RequestID, "complete_cycle", fp); ok || receiptErr != nil {
		return result, receiptErr
	}
	cycle := &agg.Cycle
	if cycle.ID != in.CycleID || cycle.Fence != in.Fence {
		return SupervisorCycle{}, fmt.Errorf("assistant: cycle %s fence %d is stale: %w", in.CycleID, in.Fence, ErrLeaseLost)
	}
	if cycle.State == CycleCompleted {
		return clone(*cycle), nil
	}
	now := storeNow(in.Now)
	cycle.State = CycleCompleted
	cycle.Revision++
	cycle.UpdatedAt = now
	result := clone(*cycle)
	touch(agg, now)
	if err := putReceipt(agg, in.RequestID, "complete_cycle", fp, result, now); err != nil {
		return SupervisorCycle{}, err
	}
	if err := s.write(agg); err != nil {
		return SupervisorCycle{}, err
	}
	return result, nil
}
