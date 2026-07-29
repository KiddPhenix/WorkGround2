package work

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ── V2 intent receipt ──────────────────────────────────────────────────────

// V2IntentReceipt records the exact outcome of a V2 write operation so replay
// can return the same deterministic result. New writes persist the receipt in
// the authoritative event log; the legacy sidecar remains read-compatible.
type V2IntentReceipt struct {
	RequestID      string         `json:"requestId"`
	Operation      string         `json:"operation"`      // "BeginWorkPlanning", "CreateCandidateRevision", "ApplyDefinition"
	IntentDigest   string         `json:"intentDigest"`   // hash of the caller's intent payload
	ResultRevision int64          `json:"resultRevision"` // definition revision number created/applied
	ResultDigest   string         `json:"resultDigest"`   // body digest of the result revision
	ResultRunID    string         `json:"resultRunId,omitempty"`
	Impact         *RunImpactJSON `json:"impact,omitempty"`
	CreatedAt      time.Time      `json:"createdAt" ts_type:"string"`
}

// RunImpactJSON is the JSON-serialisable form of RunImpact for persistence.
type RunImpactJSON struct {
	KeptNodeIDs        []string `json:"keptNodeIds"`
	InvalidatedNodeIDs []string `json:"invalidatedNodeIds"`
	NewNodeIDs         []string `json:"newNodeIds"`
	RemovedNodeIDs     []string `json:"removedNodeIds"`
	RequiresRerun      bool     `json:"requiresRerun"`
}

func impactToJSON(ri *RunImpact) *RunImpactJSON {
	if ri == nil {
		return nil
	}
	return &RunImpactJSON{
		KeptNodeIDs:        append([]string{}, ri.KeptNodeIDs...),
		InvalidatedNodeIDs: append([]string{}, ri.InvalidatedNodeIDs...),
		NewNodeIDs:         append([]string{}, ri.NewNodeIDs...),
		RemovedNodeIDs:     append([]string{}, ri.RemovedNodeIDs...),
		RequiresRerun:      ri.RequiresRerun,
	}
}

func impactFromJSON(ij *RunImpactJSON) *RunImpact {
	if ij == nil {
		return nil
	}
	return &RunImpact{
		KeptNodeIDs:        append([]string{}, ij.KeptNodeIDs...),
		InvalidatedNodeIDs: append([]string{}, ij.InvalidatedNodeIDs...),
		NewNodeIDs:         append([]string{}, ij.NewNodeIDs...),
		RemovedNodeIDs:     append([]string{}, ij.RemovedNodeIDs...),
		RequiresRerun:      ij.RequiresRerun,
	}
}

// ── FileWorkStore receipt persistence ──────────────────────────────────────

const v2ReceiptSubDir = "receipts"

// StoreV2Receipt persists a V2 intent receipt. Idempotent for same content;
// different content for the same requestID returns typed conflict.
func (s *FileWorkStore) StoreV2Receipt(workID string, receipt *V2IntentReceipt) error {
	if receipt == nil || receipt.RequestID == "" {
		return fmt.Errorf("work: StoreV2Receipt requires non-empty RequestID")
	}
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	dir := filepath.Join(wp, v2ReceiptSubDir)
	path := filepath.Join(dir, receipt.RequestID+".json")

	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal receipt for %s: %w", workID, err)
	}
	data = append(data, '\n')

	existing, existErr := os.ReadFile(path)
	if existErr == nil {
		if string(existing) == string(data) {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: workID,
			Reason: fmt.Sprintf("receipt for request %q already stored with different intent", receipt.RequestID),
			Kind:   WorkEventRequestConflict,
		}
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing receipt for %s: %w", workID, existErr)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("work: create receipts dir for %s: %w", workID, err)
	}
	if err := writeDerivedFile(path, data, 0o644); err != nil {
		return fmt.Errorf("work: write receipt for %s: %w", workID, err)
	}
	return nil
}

// LoadV2Receipt reconstructs the authoritative receipt from the event log.
// The receipts directory is only a backwards-compatible cache; correctness
// never depends on a sidecar written after the domain event committed.
func (s *FileWorkStore) LoadV2Receipt(workID, requestID string) (*V2IntentReceipt, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return nil, err
	}
	replay, err := ReplayWorkEventLog(wp)
	if err != nil {
		return nil, fmt.Errorf("work: replay receipt events for %s: %w", workID, err)
	}
	if replay.ReadOnly || replay.NeedsRepair {
		return nil, fmt.Errorf("%w: cannot reconstruct receipt for %s from unsafe event log", ErrWorkNeedsRepair, workID)
	}
	if err := validateV2DefinitionReplay(wp, workID, replay); err != nil {
		return nil, err
	}

	var created *DefRevisionCreatedPayload
	var createdReceipt *V2IntentReceipt
	var applied *DefRevisionAppliedPayload
	var runReceipt *V2IntentReceipt
	var planning *DefPlanningStartedPayload
	var createdAt time.Time
	for _, event := range replay.Events {
		switch {
		case event.RequestID == requestID+"/planning-started" && event.Type == EventDefPlanningStarted:
			var payload DefPlanningStartedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: decode planning receipt for %s: %w", workID, err)
			}
			planning = &payload
			createdAt = event.CreatedAt
		case (event.RequestID == requestID+"/revision-created" || event.RequestID == requestID+"/candidate") &&
			event.Type == EventDefRevisionCreated:
			var payload DefRevisionCreatedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: decode revision receipt for %s: %w", workID, err)
			}
			created = &payload
			if payload.Receipt != nil {
				copy := *payload.Receipt
				createdReceipt = &copy
			}
			if createdAt.IsZero() {
				createdAt = event.CreatedAt
			}
		case event.RequestID == requestID+"/run" && event.Type == EventRunStarted:
			var payload runEventPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: decode run receipt for %s: %w", workID, err)
			}
			if payload.V2Receipt != nil {
				copy := *payload.V2Receipt
				runReceipt = &copy
			}
			if createdAt.IsZero() {
				createdAt = event.CreatedAt
			}
		case event.RequestID == requestID+"/apply" && event.Type == EventDefRevisionApplied:
			var payload DefRevisionAppliedPayload
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, fmt.Errorf("work: decode apply receipt for %s: %w", workID, err)
			}
			applied = &payload
			if createdAt.IsZero() {
				createdAt = event.CreatedAt
			}
		}
	}
	if runReceipt != nil && applied == nil {
		return nil, fmt.Errorf("%w: apply receipt for %s has no revision_applied event", ErrWorkNeedsRepair, workID)
	}
	if runReceipt != nil {
		return runReceipt, nil
	}
	if planning != nil && created != nil {
		intentDigest := "begin-" + planning.SessionID
		if planning.BlueprintRef.ID != "" {
			intentBytes, _ := json.Marshal(struct {
				SessionID    string       `json:"sessionId"`
				BlueprintRef BlueprintRef `json:"blueprintRef"`
			}{SessionID: planning.SessionID, BlueprintRef: planning.BlueprintRef})
			intentDigest = fmt.Sprintf("begin-%x", sha256Hash(intentBytes))
		}
		return &V2IntentReceipt{
			RequestID:      requestID,
			Operation:      "BeginWorkPlanning",
			IntentDigest:   intentDigest,
			ResultRevision: created.Revision,
			ResultDigest:   created.Digest,
			CreatedAt:      createdAt,
		}, nil
	}
	if planning == nil && created != nil {
		if createdReceipt != nil {
			if createdReceipt.RequestID != requestID ||
				createdReceipt.Operation != "CreateCandidateRevision" ||
				createdReceipt.ResultRevision != created.Revision ||
				createdReceipt.ResultDigest != created.Digest {
				return nil, fmt.Errorf("%w: candidate receipt for %s does not match revision event", ErrWorkNeedsRepair, workID)
			}
			return createdReceipt, nil
		}
		body, loadErr := s.LoadRevision(workID, created.Revision)
		if loadErr != nil {
			return nil, fmt.Errorf("work: reconstruct candidate receipt for %s: %w", workID, loadErr)
		}
		return &V2IntentReceipt{
			RequestID:      requestID,
			Operation:      "CreateCandidateRevision",
			IntentDigest:   "candidate-" + hashCandidateIntentForWork(workID, body),
			ResultRevision: created.Revision,
			ResultDigest:   created.Digest,
			CreatedAt:      createdAt,
		}, nil
	}
	if applied != nil {
		body, loadErr := s.LoadRevision(workID, applied.Revision)
		if loadErr != nil {
			return nil, fmt.Errorf("work: reconstruct apply receipt for %s: %w", workID, loadErr)
		}
		var impact *RunImpact
		if applied.PreviousRevision > 0 {
			parent, parentErr := s.LoadRevision(workID, applied.PreviousRevision)
			if parentErr != nil {
				return nil, fmt.Errorf("work: reconstruct apply impact for %s: %w", workID, parentErr)
			}
			impact = ClassifyRunImpact(parent, body)
		}
		return &V2IntentReceipt{
			RequestID:      requestID,
			Operation:      "ApplyDefinition",
			IntentDigest:   "apply-" + hashApplyIntent(ApplyDefinitionInput{WorkID: workID, Revision: applied.Revision}),
			ResultRevision: applied.Revision,
			ResultDigest:   body.Digest,
			ResultRunID:    workflowRunID(workID, requestID),
			Impact:         impactToJSON(impact),
			CreatedAt:      createdAt,
		}, nil
	}

	// Compatibility with receipts written by the pre-event-backed V2 draft.
	path := filepath.Join(wp, v2ReceiptSubDir, requestID+".json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, fmt.Errorf("work: receipt %s for %s not found: %w", requestID, workID, ErrWorkNotFound)
		}
		return nil, fmt.Errorf("work: read receipt %s for %s: %w", requestID, workID, readErr)
	}
	var receipt V2IntentReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("work: corrupt receipt %s for %s: %w", requestID, workID, err)
	}
	return &receipt, nil
}
