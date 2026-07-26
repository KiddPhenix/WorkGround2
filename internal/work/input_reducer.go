package work

import (
	"encoding/json"
	"fmt"
	"time"
)

// ── V2 input reducer helpers ────────────────────────────────────────────────

// findInputIndex returns the index of a WorkInput in the projection by its ID,
// or -1 if not found.
func findInputIndex(current *Work, inputID string) int {
	for i := range current.V2Inputs {
		if current.V2Inputs[i].ID == inputID {
			return i
		}
	}
	return -1
}

// reduceInputRequested creates a new WorkInput in the requested state.
func reduceInputRequested(current *Work, p InputRequestedPayload, now time.Time) error {
	if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" ||
		p.BlockID == "" || p.SpecID == "" {
		return fmt.Errorf("work: reduce input.requested requires full input identity")
	}
	if idx := findInputIndex(current, p.InputID); idx >= 0 {
		if err := requireInputIdentity(&current.V2Inputs[idx], p.WorkID, p.RunID, p.TaskID, p.BlockID, p.SpecID); err != nil {
			return err
		}
		return nil
	}
	current.V2Inputs = append(current.V2Inputs, WorkInput{
		ID:        p.InputID,
		WorkID:    p.WorkID,
		RunID:     p.RunID,
		TaskID:    p.TaskID,
		BlockID:   p.BlockID,
		SpecID:    p.SpecID,
		State:     InputRequested,
		Revision:  0,
		UpdatedAt: now,
	})
	recordInputReceipt(current, p.Receipt)
	return nil
}

// reduceInputDraftSaved updates an existing input with a draft value.
func reduceInputDraftSaved(current *Work, p InputDraftSavedPayload, now time.Time, requestID string) error {
	idx := findInputIndex(current, p.InputID)
	if idx < 0 {
		return fmt.Errorf("work: reduce input.draft_saved: input %q not found", p.InputID)
	}
	existing := &current.V2Inputs[idx]
	if err := requireInputIdentity(existing, p.WorkID, p.RunID, p.TaskID, p.BlockID, p.SpecID); err != nil {
		return err
	}

	// Revision check: only apply if this is a newer revision or idempotent replay.
	if err := checkInputRevision(existing, p.Revision, p.ExpectedRevision); err != nil {
		return err
	}
	if err := ValidateInputTransition(existing.State, InputDraft); err != nil {
		return err
	}

	// Idempotency: same revision + same value → no-op.
	if existing.Revision == p.Revision && existing.State == InputDraft {
		// Same payload digest means idempotent replay.
		return nil
	}

	existing.Value = p.Value
	existing.State = InputDraft
	existing.Source = p.Source
	existing.UpdatedBy = p.UpdatedBy
	existing.Revision = p.Revision
	existing.Error = ""
	existing.UpdatedAt = now
	recordInputReceipt(current, p.Receipt)
	return nil
}

// reduceInputSubmitted marks an input as submitted with value.
func reduceInputSubmitted(current *Work, p InputSubmittedPayload, now time.Time, requestID string) error {
	idx := findInputIndex(current, p.InputID)
	if idx < 0 {
		return fmt.Errorf("work: reduce input.submitted: input %q not found", p.InputID)
	}
	existing := &current.V2Inputs[idx]
	if err := requireInputIdentity(existing, p.WorkID, p.RunID, p.TaskID, p.BlockID, p.SpecID); err != nil {
		return err
	}

	if err := checkInputRevision(existing, p.Revision, p.ExpectedRevision); err != nil {
		return err
	}
	if err := ValidateInputTransition(existing.State, InputSubmitted); err != nil {
		return err
	}

	if existing.Revision == p.Revision && existing.State == InputSubmitted {
		return nil
	}

	existing.Value = p.Value
	existing.State = InputSubmitted
	existing.Source = p.Source
	existing.UpdatedBy = p.UpdatedBy
	existing.Revision = p.Revision
	existing.Error = ""
	existing.UpdatedAt = now
	recordInputReceipt(current, p.Receipt)
	return nil
}

// reduceInputRejected marks an input as rejected with an optional reason.
func reduceInputRejected(current *Work, p InputRejectedPayload, now time.Time, requestID string) error {
	idx := findInputIndex(current, p.InputID)
	if idx < 0 {
		return fmt.Errorf("work: reduce input.rejected: input %q not found", p.InputID)
	}
	existing := &current.V2Inputs[idx]
	if err := requireInputIdentity(existing, p.WorkID, p.RunID, p.TaskID, p.BlockID, p.SpecID); err != nil {
		return err
	}
	if err := checkInputRevision(existing, p.Revision, p.ExpectedRevision); err != nil {
		return err
	}
	if err := ValidateInputTransition(existing.State, InputRejected); err != nil {
		return err
	}
	existing.Value = append(json.RawMessage(nil), p.Value...)
	existing.State = InputRejected
	existing.Error = p.Reason
	existing.Source = p.Source
	existing.UpdatedBy = p.UpdatedBy
	existing.Revision = p.Revision
	existing.UpdatedAt = now
	recordInputReceipt(current, p.Receipt)
	return nil
}

// reduceInputCornerstoneChanged updates the cornerstone linkage for an input.
func reduceInputCornerstoneChanged(current *Work, p InputCornerstoneChangedPayload, now time.Time) error {
	idx := findInputIndex(current, p.InputID)
	if idx < 0 {
		return fmt.Errorf("work: reduce input.cornerstone_changed: input %q not found", p.InputID)
	}
	existing := &current.V2Inputs[idx]
	if err := requireInputIdentity(existing, p.WorkID, p.RunID, p.TaskID, p.BlockID, p.SpecID); err != nil {
		return err
	}
	if err := checkInputRevision(existing, p.Revision, p.ExpectedRevision); err != nil {
		return err
	}
	if existing.State != InputSubmitted && existing.State != InputAccepted {
		return fmt.Errorf("work: input %q cornerstone change requires submitted or accepted state, got %s", existing.ID, existing.State)
	}
	if p.Pinned {
		existing.CornerstoneID = p.CornerstoneID
	} else {
		existing.CornerstoneID = ""
	}
	existing.Revision = p.Revision
	existing.UpdatedAt = now
	recordInputReceipt(current, p.Receipt)
	return nil
}

func recordInputReceipt(current *Work, receipt *InputIntentReceipt) {
	if receipt == nil || receipt.RequestID == "" {
		return
	}
	if current.V2InputReceipts == nil {
		current.V2InputReceipts = make(map[string]InputIntentReceipt)
	}
	cp := *receipt
	if receipt.ResultInput != nil {
		inputCopy := *receipt.ResultInput
		inputCopy.Value = append(json.RawMessage(nil), receipt.ResultInput.Value...)
		cp.ResultInput = &inputCopy
	}
	cp.AffectedTaskIDs = append([]string(nil), receipt.AffectedTaskIDs...)
	current.V2InputReceipts[receipt.RequestID] = cp
}

// ── Revision guard ─────────────────────────────────────────────────────────

func checkInputRevision(existing *WorkInput, newRevision, expectedRevision int64) error {
	// First write: revision must be 1, expected must be 0.
	if existing.Revision == 0 {
		if newRevision != 1 {
			return fmt.Errorf("work: input %q first write expects revision 1, got %d", existing.ID, newRevision)
		}
		if expectedRevision != 0 {
			return fmt.Errorf("work: input %q first write expects expectedRevision 0, got %d", existing.ID, expectedRevision)
		}
		return nil
	}

	// Expected revision mismatch: the caller's view is stale.
	if expectedRevision != existing.Revision {
		return fmt.Errorf("work: input %q revision conflict: expected %d, current %d", existing.ID, expectedRevision, existing.Revision)
	}
	if newRevision != existing.Revision+1 {
		return fmt.Errorf("work: input %q revision must advance from %d to %d, got %d", existing.ID, existing.Revision, existing.Revision+1, newRevision)
	}

	return nil
}

func requireInputIdentity(
	existing *WorkInput,
	workID, runID, taskID, blockID, specID string,
) error {
	if existing == nil || workID == "" || runID == "" || taskID == "" ||
		blockID == "" || specID == "" {
		return fmt.Errorf("work: input identity requires workId/runId/taskId/blockId/specId")
	}
	if existing.WorkID != workID || existing.RunID != runID ||
		existing.TaskID != taskID || existing.BlockID != blockID ||
		existing.SpecID != specID {
		return fmt.Errorf(
			"work: input %q identity conflict: have %s/%s/%s/%s/%s, got %s/%s/%s/%s/%s",
			existing.ID,
			existing.WorkID,
			existing.RunID,
			existing.TaskID,
			existing.BlockID,
			existing.SpecID,
			workID,
			runID,
			taskID,
			blockID,
			specID,
		)
	}
	return nil
}

// ── WorkInput JSON helpers ───────────────────────────────────────────────────

// inputFromJSON unmarshals a WorkInput from raw JSON, handling the value field
// as json.RawMessage to preserve structure.
func inputFromJSON(raw json.RawMessage) (*WorkInput, error) {
	var wi WorkInput
	if err := json.Unmarshal(raw, &wi); err != nil {
		return nil, err
	}
	return &wi, nil
}
