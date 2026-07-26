package work

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"
)

// ── Patch intent receipt ──────────────────────────────────────────────────

// PatchIntentReceipt records the exact outcome of a patch preview or apply
// operation so replay can return the same deterministic result. It is persisted
// both in the authoritative event log (via the reducer) and as a sidecar.
type PatchIntentReceipt struct {
	RequestID               string            `json:"requestId"`
	Operation               string            `json:"operation"` // "PreviewWorkPatch", "ApplyWorkPatch"
	IntentDigest            string            `json:"intentDigest"`
	PatchID                 string            `json:"patchId"`
	ResultRevision          int64             `json:"resultRevision"`
	ResultDigest            string            `json:"resultDigest"`
	ResultPatch             *WorkPatchPreview `json:"resultPatch,omitempty"`
	Scope                   PatchScope        `json:"scope,omitempty"`
	NewRevision             int64             `json:"newRevision,omitempty"`        // only for apply
	InvalidatedIDs          []string          `json:"invalidatedTaskIds,omitempty"` // only for apply
	AffectedBlockIDs        []string          `json:"affectedBlockIds,omitempty"`
	AffectedArtifactSlotIDs []string          `json:"affectedArtifactSlotIds,omitempty"`
	StaleArtifactSlotIDs    []string          `json:"staleArtifactSlotIds,omitempty"`
	RequiresRerun           bool              `json:"requiresRerun"`
	Error                   string            `json:"error,omitempty"`
	CreatedAt               time.Time         `json:"createdAt"`
}

// hashPatchIntent produces a stable intent digest for a patch operation.
func hashPatchIntent(operation string, input any) string {
	body, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(operation+"\x00"), body...))
	return fmt.Sprintf("sha256:%x", sum[:])
}

// hashPatchPreviewDigest computes a deterministic digest for a WorkPatchPreview.
func hashPatchPreviewDigest(p *WorkPatchPreview) string {
	if p == nil {
		return ""
	}
	// Clone and clear the digest field before hashing.
	clone := *p
	clone.Digest = ""
	clone.Operations = clonePatchOps(p.Operations)
	clone.AffectedNodeIDs = append([]string{}, p.AffectedNodeIDs...)
	clone.AffectedBlockIDs = append([]string{}, p.AffectedBlockIDs...)
	clone.AffectedArtifactSlotIDs = append([]string{}, p.AffectedArtifactSlotIDs...)
	clone.StaleArtifactSlotIDs = append([]string{}, p.StaleArtifactSlotIDs...)
	clone.InvalidatedTaskIDs = append([]string{}, p.InvalidatedTaskIDs...)
	body, err := json.Marshal(clone)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return fmt.Sprintf("sha256:%x", sum[:])
}
