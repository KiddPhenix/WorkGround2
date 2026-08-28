package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// OpReceipt is a durable first-write-wins receipt for a Session write operation
// (steer/answer/cancel/resume/retry/fork/create) or any other idempotent write
// (e.g. project constraint edits). It survives restart so a replayed request
// with the same key returns the recorded outcome instead of executing the
// external action twice. An optional Fingerprint lets a key reused with
// different input be rejected instead of silently returning the old outcome.
type OpReceipt struct {
	Status      string `json:"status"`
	SessionID   string `json:"session_id,omitempty"`
	Message     string `json:"message,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// OpReceiptConflictError reports that an op key was reused with different
// input (fingerprint mismatch).
type OpReceiptConflictError struct {
	Key         string
	Fingerprint string
}

func (e *OpReceiptConflictError) Error() string {
	return fmt.Sprintf("op receipt key %q already bound to different input", e.Key)
}

// OpReceiptPath returns the receipt path for one op key under dir's receipt
// dir. The key is hashed into the file name, never used verbatim, so a request
// ID containing separators or Windows-reserved names cannot escape the receipt
// directory.
func OpReceiptPath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, ".session-receipts", hex.EncodeToString(sum[:16])+".json")
}

// ReadOpReceipt returns the recorded op receipt, or ok=false. A corrupt receipt
// is an explicit error, never silently treated as "no binding".
func ReadOpReceipt(dir, key string) (OpReceipt, bool, error) {
	b, err := os.ReadFile(OpReceiptPath(dir, key))
	if os.IsNotExist(err) {
		return OpReceipt{}, false, nil
	}
	if err != nil {
		return OpReceipt{}, false, fmt.Errorf("read op receipt: %w", err)
	}
	var r OpReceipt
	if err := json.Unmarshal(b, &r); err != nil {
		return OpReceipt{}, false, fmt.Errorf("parse op receipt: %w", err)
	}
	if r.Status == "" {
		return OpReceipt{}, false, fmt.Errorf("%w: empty status", ErrSessionReceiptCorrupt)
	}
	return r, true, nil
}

// WriteOpReceipt durably records an op outcome with O_EXCL + fsync and 0600
// perms. It returns recorded=true when it wrote, and recorded=false plus the
// existing receipt when a prior write already won (first-write-wins).
func WriteOpReceipt(dir, key string, receipt OpReceipt) (OpReceipt, bool, error) {
	if receipt.Status == "" {
		return OpReceipt{}, false, errors.New("op receipt requires status")
	}
	path := OpReceiptPath(dir, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return OpReceipt{}, false, err
	}
	if existing, ok, err := ReadOpReceipt(dir, key); err != nil {
		return OpReceipt{}, false, err
	} else if ok {
		if existing.Fingerprint != "" && receipt.Fingerprint != "" && existing.Fingerprint != receipt.Fingerprint {
			return OpReceipt{}, false, &OpReceiptConflictError{Key: key, Fingerprint: existing.Fingerprint}
		}
		return existing, false, nil
	}
	b, err := json.Marshal(receipt)
	if err != nil {
		return OpReceipt{}, false, err
	}
	b = append(b, '\n')
	created, err := writeNewReceiptFile(path, b)
	if err != nil {
		return OpReceipt{}, false, fmt.Errorf("create op receipt: %w", err)
	}
	if !created {
		if existing, ok, err := ReadOpReceipt(dir, key); err != nil {
			return OpReceipt{}, false, err
		} else if ok {
			if existing.Fingerprint != "" && receipt.Fingerprint != "" && existing.Fingerprint != receipt.Fingerprint {
				return OpReceipt{}, false, &OpReceiptConflictError{Key: key, Fingerprint: existing.Fingerprint}
			}
			return existing, false, nil
		}
		return OpReceipt{}, false, errors.New("op receipt raced")
	}
	return receipt, true, nil
}
