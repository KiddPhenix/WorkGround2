package work

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		input := cloneWorkInput(*receipt.ResultInput)
		copy.ResultInput = &input
	}
	return &copy
}

const inputReceiptSubDir = "input_receipts"

// receiptCacheFileName maps a requestID to a filesystem-safe cache file name.
// RequestIDs are caller-chosen and may contain characters that are illegal in
// file names on Windows (":", "/", "\\", "*", "?", '"', "<", ">", "|", control
// chars) or may collide with reserved device names. The receipt sidecar is only
// a cache (authoritative data lives in the event log), but store and load must
// agree deterministically and two distinct request IDs must never map to the
// same file name. A reversible percent-encoding (URL-style, "%XX") guarantees
// that; safe request IDs keep their exact name so sidecars written by older
// builds remain readable.
func receiptCacheFileName(requestID string) string {
	var b strings.Builder
	b.Grow(len(requestID) + 8)
	for _, r := range requestID {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`<>:"/\|?*%`, r) {
			fmt.Fprintf(&b, "%%%02X", r)
		} else {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if receiptNameIsReservedDevice(name) {
		// CON, PRN, AUX, NUL, COM1..9, LPT1..9 (also with an extension) cannot
		// be created on Windows; prefix so the cache write never fails.
		name = "_" + name
	}
	return name + ".json"
}

func receiptNameIsReservedDevice(name string) bool {
	base := name
	if dot := strings.IndexByte(base, '.'); dot >= 0 {
		base = base[:dot]
	}
	switch strings.ToUpper(base) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

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
	path := filepath.Join(dir, receiptCacheFileName(receipt.RequestID))

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
func (s *FileWorkStore) LoadInputReceipt(workID, requestID string) (receipt *InputIntentReceipt, retErr error) {
	// Read under the per-Work lifecycle lock so a concurrent commit cannot
	// expose a mid-batch event log to the replay below (same reasoning as
	// LoadV2Receipt).
	done, err := s.beginWorkOp(workID)
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, done()) }()
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
	path := filepath.Join(wp, inputReceiptSubDir, receiptCacheFileName(requestID))
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, fmt.Errorf("work: input receipt %s for %s not found: %w", requestID, workID, ErrWorkNotFound)
		}
		return nil, fmt.Errorf("work: read input receipt %s for %s: %w", requestID, workID, readErr)
	}
	var cached InputIntentReceipt
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, fmt.Errorf("work: corrupt input receipt %s for %s: %w", requestID, workID, err)
	}
	return &cached, nil
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
