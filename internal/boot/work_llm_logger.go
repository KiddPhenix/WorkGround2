// Package boot provides shared assembly logic for Work LLM planners.
//
// workLLMInteractionLogger records per-attempt request/response pairs for the
// Work Definition and Patch planners in JSONL format. It is optional and
// controlled by [work].llm_interaction_log in the configuration.
//
// TEMPORARY DIAGNOSTIC — this logger captures raw model prompts and responses.
// Default disabled. Do NOT enable in shared or production environments.
package boot

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workground2/internal/provider"
)

// workLLMInteractionLogger writes paired request/response JSONL lines for
// every provider attempt made by a Work planner. It is safe for concurrent use
// across goroutines and never blocks on file I/O errors (falls back to Warn).
type workLLMInteractionLogger struct {
	path string
}

var (
	workLLMLogMu  sync.Mutex
	workLLMLogSeq atomic.Uint64
)

// newWorkLLMInteractionLogger creates a lazy logger. The file is opened per
// record so rebuilt controllers do not leak descriptors and all logger
// instances can share the same append lock.
func newWorkLLMInteractionLogger(filePath string) *workLLMInteractionLogger {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil
	}
	return &workLLMInteractionLogger{path: filePath}
}

// Close is retained for test and caller symmetry. Records do not keep files
// open, so it is a nil-safe no-op.
func (l *workLLMInteractionLogger) Close() error {
	return nil
}

// interactionRecord is the base of every JSONL line.
type interactionRecord struct {
	InteractionID string `json:"interactionId"`
	Timestamp     string `json:"timestamp"`
	Kind          string `json:"kind"` // "definition" or "patch"
	WorkID        string `json:"workId"`
	Attempt       int    `json:"attempt"`
}

// requestRecord is written before a provider call.
type requestRecord struct {
	interactionRecord
	Type        string             `json:"type"` // "request"
	Provider    string             `json:"provider"`
	Messages    []provider.Message `json:"messages"`
	Temperature float64            `json:"temperature"`
	MaxTokens   int                `json:"maxTokens"`
}

// responseRecord is written after a provider call completes (success or error).
type responseRecord struct {
	interactionRecord
	Type        string `json:"type"` // "response"
	RawResponse string `json:"rawResponse"`
	Error       string `json:"error,omitempty"`
}

// logRequest writes a request record for a provider attempt. It is a no-op when
// logger is nil.
func (l *workLLMInteractionLogger) logRequest(interactionID, kind, workID, providerName string, attempt int, msgs []provider.Message, temperature float64, maxTokens int) {
	if l == nil {
		return
	}
	l.append(requestRecord{
		interactionRecord: interactionRecord{
			InteractionID: interactionID,
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			Kind:          kind,
			WorkID:        workID,
			Attempt:       attempt,
		},
		Type:        "request",
		Provider:    providerName,
		Messages:    msgs,
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
}

// logResponse writes a response record. rawResponse is the accumulated text
// (may be partial on chunk error). err is the error string (empty on success).
// It is a no-op when logger is nil.
func (l *workLLMInteractionLogger) logResponse(interactionID, kind, workID string, attempt int, rawResponse string, err error) {
	if l == nil {
		return
	}
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	l.append(responseRecord{
		interactionRecord: interactionRecord{
			InteractionID: interactionID,
			Timestamp:     time.Now().UTC().Format(time.RFC3339Nano),
			Kind:          kind,
			WorkID:        workID,
			Attempt:       attempt,
		},
		Type:        "response",
		RawResponse: rawResponse,
		Error:       errStr,
	})
}

func (l *workLLMInteractionLogger) append(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Warn("work LLM interaction log: marshal failed", "err", err)
		return
	}
	b = append(b, '\n')

	workLLMLogMu.Lock()
	defer workLLMLogMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		slog.Warn("work LLM interaction log: create directory failed",
			"path", l.path, "err", err)
		return
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		slog.Warn("work LLM interaction log: open failed",
			"path", l.path, "err", err)
		return
	}
	n, writeErr := f.Write(b)
	if writeErr == nil && n != len(b) {
		writeErr = io.ErrShortWrite
	}
	closeErr := f.Close()
	if writeErr != nil {
		slog.Warn("work LLM interaction log: write failed",
			"path", l.path, "err", writeErr)
		return
	}
	if closeErr != nil {
		slog.Warn("work LLM interaction log: close failed",
			"path", l.path, "err", closeErr)
	}
}

// interactionID generates a process-unique request/response pair ID.
func interactionID(kind, workID string, attempt int) string {
	return fmt.Sprintf("%d-%d-%s-%s-%d",
		time.Now().UTC().UnixNano(),
		workLLMLogSeq.Add(1),
		kind,
		workID,
		attempt,
	)
}
