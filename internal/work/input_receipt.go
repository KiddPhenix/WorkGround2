package work

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ── Input intent receipt ──────────────────────────────────────────────────

// InputIntentReceipt records the exact outcome of an input write operation so
// replay can return the same deterministic result. It is persisted both in the
// authoritative event log (via the reducer) and as a sidecar for fast lookup.
type InputIntentReceipt struct {
	RequestID       string     `json:"requestId"`
	Operation       string     `json:"operation"`    // "RequestInput", "SaveDraft", "SubmitInput", "RejectInput"
	IntentDigest    string     `json:"intentDigest"` // hash of caller intent
	InputID         string     `json:"inputId"`
	ResultRevision  int64      `json:"resultRevision"`
	ResultDigest    string     `json:"resultDigest"`
	AffectedTaskIDs []string   `json:"affectedTaskIds,omitempty"`
	CornerstoneID   string     `json:"cornerstoneId,omitempty"`
	ResultInput     *WorkInput `json:"resultInput,omitempty"`
	Pinned          bool       `json:"pinned,omitempty"`
	Error           string     `json:"error,omitempty"`
	CreatedAt       time.Time  `json:"createdAt" ts_type:"string"`
}

// InputReceiptStore is the persistence interface for input receipts.
type InputReceiptStore interface {
	StoreInputReceipt(workID string, receipt *InputIntentReceipt) error
	LoadInputReceipt(workID, requestID string) (*InputIntentReceipt, error)
}

func cloneInputIntentReceipt(receipt *InputIntentReceipt) *InputIntentReceipt {
	if receipt == nil {
		return nil
	}
	copy := *receipt
	copy.AffectedTaskIDs = append([]string(nil), receipt.AffectedTaskIDs...)
	if receipt.ResultInput != nil {
		input := *receipt.ResultInput
		input.Value = append(json.RawMessage(nil), receipt.ResultInput.Value...)
		copy.ResultInput = &input
	}
	return &copy
}

const inputReceiptSubDir = "input_receipts"

// StoreInputReceipt persists an input intent receipt. Idempotent for same content;
// different content for same requestID returns typed conflict.
func (s *FileWorkStore) StoreInputReceipt(workID string, receipt *InputIntentReceipt) error {
	if receipt == nil || receipt.RequestID == "" {
		return fmt.Errorf("work: StoreInputReceipt requires non-empty RequestID")
	}
	wp, err := s.workPath(workID)
	if err != nil {
		return err
	}
	dir := filepath.Join(wp, inputReceiptSubDir)
	path := filepath.Join(dir, receipt.RequestID+".json")

	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return fmt.Errorf("work: marshal input receipt for %s: %w", workID, err)
	}
	data = append(data, '\n')

	existing, existErr := os.ReadFile(path)
	if existErr == nil {
		if string(existing) == string(data) {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: workID,
			Reason: fmt.Sprintf("input receipt for request %q already stored with different intent", receipt.RequestID),
			Kind:   WorkEventRequestConflict,
		}
	}
	if !os.IsNotExist(existErr) {
		return fmt.Errorf("work: read existing input receipt for %s: %w", workID, existErr)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("work: create input receipts dir for %s: %w", workID, err)
	}
	if err := writeDerivedFile(path, data, 0o644); err != nil {
		return fmt.Errorf("work: write input receipt for %s: %w", workID, err)
	}
	return nil
}

// LoadInputReceipt reconstructs the authoritative receipt from the event log.
// The receipts directory is only a backwards-compatible cache.
func (s *FileWorkStore) LoadInputReceipt(workID, requestID string) (*InputIntentReceipt, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return nil, err
	}
	replay, err := ReplayWorkEventLog(wp)
	if err != nil {
		return nil, fmt.Errorf("work: replay input receipt events for %s: %w", workID, err)
	}
	if replay.ReadOnly || replay.NeedsRepair {
		return nil, fmt.Errorf("%w: cannot reconstruct input receipt for %s from unsafe event log", ErrWorkNeedsRepair, workID)
	}

	// Replay to build the projection with V2 input receipts.
	_, proj, err := ReplayWithReducer(wp, DefaultReducer())
	if err != nil {
		return nil, fmt.Errorf("work: replay for input receipt: %w", err)
	}
	if proj.V2InputReceipts != nil {
		if r, ok := proj.V2InputReceipts[requestID]; ok {
			return &r, nil
		}
	}

	// Fallback: try the sidecar cache.
	path := filepath.Join(wp, inputReceiptSubDir, requestID+".json")
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, fmt.Errorf("work: input receipt %s for %s not found: %w", requestID, workID, ErrWorkNotFound)
		}
		return nil, fmt.Errorf("work: read input receipt %s for %s: %w", requestID, workID, readErr)
	}
	var receipt InputIntentReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("work: corrupt input receipt %s for %s: %w", requestID, workID, err)
	}
	return &receipt, nil
}

// ── Intent digest helpers ──────────────────────────────────────────────────

// HashInputIntent produces a stable intent digest for an input operation.
func HashInputIntent(workID, inputID, requestID string, value json.RawMessage, revision int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d", workID, inputID, requestID, string(value), revision)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))[:32]
}

func hashInputOperation(operation string, input any) string {
	body, err := json.Marshal(input)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(append([]byte(operation+"\x00"), body...))
	return fmt.Sprintf("sha256:%x", sum[:])[:32]
}
