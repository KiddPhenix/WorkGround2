package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// ErrRunBlockedByCornerstones is returned by RunWork when required Cornerstones
// are missing, denied, stale, or invalid. The Assessment contains the full
// detail so callers can surface every blocking item without parsing.
// This error is retryable: after the user fixes the cornerstones, RunWork
// with a new requestID will proceed.
type ErrRunBlockedByCornerstones struct {
	WorkID     string
	Assessment CornerstoneAssessment
}

func (e *ErrRunBlockedByCornerstones) Error() string {
	if e == nil {
		return "work: run blocked by cornerstones"
	}
	return fmt.Sprintf("work: run blocked by cornerstones for %s (state=%s, blocking=%v, issues=%d)",
		e.WorkID, e.Assessment.State, e.Assessment.Blocking, len(e.Assessment.Issues))
}

// CheckRunCornerstones validates that required Cornerstones are ready for a
// run. It uses production budget defaults. Returns nil when the Work can
// proceed. When required cornerstones are blocking it returns
// ErrRunBlockedByCornerstones with the full assessment.
// Optional failures do not block; callers should inspect Degraded.
func CheckRunCornerstones(w *Work) *ErrRunBlockedByCornerstones {
	return checkRunCornerstones(w, productionCornerstoneContextConfig())
}

func (s *Service) checkRunCornerstones(w *Work) *ErrRunBlockedByCornerstones {
	config := productionCornerstoneContextConfig()
	if s != nil {
		if blobs, ok := s.store.(BlobStore); ok {
			config.BlobStore = blobs
		}
	}
	return checkRunCornerstones(w, config)
}

func checkRunCornerstones(w *Work, config CornerstoneContextConfig) *ErrRunBlockedByCornerstones {
	if w == nil {
		return nil
	}
	block, err := BuildCornerstoneContext(w.Cornerstones, config)
	if err != nil {
		// BuildCornerstoneContext errors are internal (e.g. budget edge case for
		// required items). Treat as blocking so we don't run with broken context.
		return &ErrRunBlockedByCornerstones{
			WorkID: w.ID,
			Assessment: CornerstoneAssessment{
				State:    CornerstoneUseBlocked,
				Blocking: true,
				Issues:   []CornerstoneIssue{{Problem: err.Error(), Blocking: true}},
			},
		}
	}
	if block.Blocking {
		return &ErrRunBlockedByCornerstones{
			WorkID:     w.ID,
			Assessment: block.Assessment,
		}
	}
	return nil
}

type cornerstoneBlockPhase uint8

const (
	cornerstoneBlockInitialRun cornerstoneBlockPhase = iota + 1
	cornerstoneBlockResume
)

// emitCornerstoneBlockedRun persists a run.changed event that transitions the
// run to waiting and the Work to waiting_user, so the user sees a blocked
// state instead of a silent failure.
//
// It reads the current Work state from the store to compute valid
// BaseRevision/Revision. Same requestID replays idempotently (CommitEvent
// deduplicates). Concurrent commits retry via the standard event conflict
// path.
func (s *Service) emitCornerstoneBlockedRun(workID, runID, requestID string, assessment CornerstoneAssessment, phase cornerstoneBlockPhase) error {
	eventRequestID := requestID + "/blocked"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return fmt.Errorf("work: cornerstone blocked run: load state: %w", err)
	}

	// Idempotent replay: same requestID already committed.
	if state.RequestFound {
		return nil
	}
	run := findWorkflowRun(current, runID)
	if run == nil {
		return fmt.Errorf("work: cornerstone blocked run: run %q not found", runID)
	}
	next := *run
	next.State = RunWaiting
	switch phase {
	case cornerstoneBlockInitialRun:
		// Initial preflight failure must not materialize Task/RunTurn records.
		// ResumeRun reconstructs the deterministic shape after preflight passes.
		next.Stages = []Stage{}
	case cornerstoneBlockResume:
		// A resumed Run may already own completed attempts, SessionRefs and
		// receipts. Preserve the complete shape; only its waiting state changes.
	default:
		return fmt.Errorf("work: cornerstone blocked run: invalid phase %d", phase)
	}

	now := time.Now().UTC()
	payload, err := json.Marshal(runEventPayload{
		Run:       next,
		WorkState: WorkWaitingUser,
	})
	if err != nil {
		return fmt.Errorf("work: encode cornerstone blocked run: %w", err)
	}
	event := newServiceEvent(workID, eventRequestID, EventRunChanged, payload, now)
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1
	if _, commitErr := s.store.CommitEvent(workID, event); commitErr != nil {
		return fmt.Errorf("work: commit cornerstone blocked run: %w", commitErr)
	}
	return nil
}
