package work

import (
	"fmt"
	"time"
)

// ── Patch reducer helpers ──────────────────────────────────────────────────

// reducePatchPreviewed records a WorkPatchPreview in the projection.
// Idempotent: same patchID + same digest is a no-op. Different content for
// same patchID returns a conflict.
func reducePatchPreviewed(current *Work, p PatchPreviewedPayload, now time.Time, requestID string) error {
	if p.PatchID == "" || p.WorkID == "" {
		return fmt.Errorf("work: reduce patch.previewed requires patchId/workId")
	}
	if current.V2PatchPreviews == nil {
		current.V2PatchPreviews = make(map[string]WorkPatchPreview)
	}
	existing, exists := current.V2PatchPreviews[p.PatchID]
	if exists {
		if existing.Digest != "" && existing.Digest == p.Digest {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: p.WorkID, Kind: WorkEventRevisionConflict,
			Reason: fmt.Sprintf("patch %q was previewed with different content", p.PatchID),
		}
	}

	// Rebuild the full WorkPatchPreview from payload for storage.
	preview := WorkPatchPreview{
		ID:                      p.PatchID,
		WorkID:                  p.WorkID,
		RunID:                   p.RunID,
		TaskID:                  p.TaskID,
		BlockID:                 p.BlockID,
		SessionID:               p.SessionID,
		BaseDefinitionRev:       p.BaseDefinitionRev,
		BaseBlockRev:            p.BaseBlockRev,
		Scope:                   p.Scope,
		Operations:              clonePatchOps(p.Operations),
		AffectedNodeIDs:         clonePatchStrings(p.AffectedNodeIDs),
		AffectedBlockIDs:        clonePatchStrings(p.AffectedBlockIDs),
		AffectedArtifactSlotIDs: clonePatchStrings(p.AffectedArtifactSlotIDs),
		StaleArtifactSlotIDs:    clonePatchStrings(p.StaleArtifactSlotIDs),
		InvalidatedTaskIDs:      clonePatchStrings(p.InvalidatedTasks),
		RequiresRerun:           p.RequiresRerun,
		Digest:                  p.Digest,
	}
	if p.ExpiresAt != nil {
		preview.ExpiresAt = *p.ExpiresAt
	}

	current.V2PatchPreviews[p.PatchID] = preview
	return nil
}

// reducePatchApplied records the result of applying a patch. For block scope
// this marks the patch as applied; for workflow scope it records the new
// definition revision.
func reducePatchApplied(current *Work, p PatchAppliedPayload, now time.Time, requestID string) error {
	if p.PatchID == "" || p.WorkID == "" {
		return fmt.Errorf("work: reduce patch.applied requires patchId/workId")
	}

	if current.V2PatchReceipts == nil {
		current.V2PatchReceipts = make(map[string]PatchIntentReceipt)
	}

	existing, exists := current.V2PatchReceipts[requestID]
	if exists {
		// Idempotent: same result → no-op.
		if existing.NewRevision == p.NewRevision && existing.Scope == p.Scope {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: p.WorkID, Kind: WorkEventRevisionConflict,
			Reason: fmt.Sprintf("patch apply requestID %q already recorded with different result", requestID),
		}
	}

	receipt := PatchIntentReceipt{
		RequestID:      requestID,
		Operation:      "ApplyWorkPatch",
		PatchID:        p.PatchID,
		ResultRevision: p.NewRevision,
		Scope:          p.Scope,
		NewRevision:    p.NewRevision,
		InvalidatedIDs: append([]string(nil), p.InvalidatedTaskIDs...),
		CreatedAt:      now,
	}
	if p.Receipt != nil {
		receipt.IntentDigest = p.Receipt.IntentDigest
		receipt.ResultDigest = p.Receipt.ResultDigest
		receipt.AffectedBlockIDs = append([]string(nil), p.Receipt.AffectedBlockIDs...)
		receipt.AffectedArtifactSlotIDs = append([]string(nil), p.Receipt.AffectedArtifactSlotIDs...)
		receipt.StaleArtifactSlotIDs = append([]string(nil), p.Receipt.StaleArtifactSlotIDs...)
		receipt.RequiresRerun = p.Receipt.RequiresRerun
	}
	current.V2PatchReceipts[requestID] = receipt

	// For workflow scope, the definition revision change is handled by
	// EventDefRevisionApplied separately. Here we just record the receipt.

	return nil
}

// recordPatchReceipt stores a PatchIntentReceipt in the Work projection.
func recordPatchReceipt(current *Work, receipt *PatchIntentReceipt) {
	if receipt == nil || receipt.RequestID == "" {
		return
	}
	if current.V2PatchReceipts == nil {
		current.V2PatchReceipts = make(map[string]PatchIntentReceipt)
	}
	cp := *receipt
	if receipt.ResultPatch != nil {
		patchCopy := *receipt.ResultPatch
		patchCopy.Operations = clonePatchOps(receipt.ResultPatch.Operations)
		patchCopy.AffectedNodeIDs = clonePatchStrings(receipt.ResultPatch.AffectedNodeIDs)
		patchCopy.AffectedBlockIDs = clonePatchStrings(receipt.ResultPatch.AffectedBlockIDs)
		patchCopy.AffectedArtifactSlotIDs = clonePatchStrings(receipt.ResultPatch.AffectedArtifactSlotIDs)
		patchCopy.StaleArtifactSlotIDs = clonePatchStrings(receipt.ResultPatch.StaleArtifactSlotIDs)
		patchCopy.InvalidatedTaskIDs = clonePatchStrings(receipt.ResultPatch.InvalidatedTaskIDs)
		cp.ResultPatch = &patchCopy
	}
	cp.InvalidatedIDs = append([]string(nil), receipt.InvalidatedIDs...)
	cp.AffectedBlockIDs = append([]string(nil), receipt.AffectedBlockIDs...)
	cp.AffectedArtifactSlotIDs = append([]string(nil), receipt.AffectedArtifactSlotIDs...)
	cp.StaleArtifactSlotIDs = append([]string(nil), receipt.StaleArtifactSlotIDs...)
	current.V2PatchReceipts[receipt.RequestID] = cp
}
