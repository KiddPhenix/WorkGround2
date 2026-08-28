package assistanttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"workground2/internal/assistant"
)

type memoryResult struct {
	Status   string                 `json:"status"`
	Memory   *assistant.Memory      `json:"memory,omitempty"`
	Revision int64                  `json:"revision,omitempty"`
	Message  string                 `json:"message,omitempty"`
	Items    []assistant.MemoryItem `json:"items,omitempty"`
}

func (r memoryResult) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"status":"retryable_error","message":%q}`, err.Error())
	}
	return string(b)
}

func memoryError(status string, err error) string {
	return memoryResult{Status: status, Message: err.Error()}.String()
}

type memoryTool struct {
	store       *assistant.Store
	assistantID string
}

func newMemoryTool(store *assistant.Store, assistantID string) *memoryTool {
	return &memoryTool{store: store, assistantID: assistantID}
}

// ---- memory_search ----------------------------------------------------------

type memorySearchTool struct{ *memoryTool }

// NewMemorySearchTool searches the Assistant's controlled Memory.
func NewMemorySearchTool(store *assistant.Store, assistantID string) *memorySearchTool {
	return &memorySearchTool{memoryTool: newMemoryTool(store, assistantID)}
}

func (t *memorySearchTool) Name() string   { return "memory_search" }
func (t *memorySearchTool) ReadOnly() bool { return true }

func (t *memorySearchTool) Description() string {
	return "Search the Assistant's long-term memory (facts, strategy, metrics, charter, open loops) by case-insensitive substring over kind and body. Use memory_remember to record reusable experience and memory_forget to retire stale items."
}

func (t *memorySearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"query":{"type":"string","description":"Substring to match against memory kind and body."},"kind":{"type":"string","description":"Optional kind filter: facts, strategy, metrics, charter, open_loops."}},"required":["query"]}`)
}

func (t *memorySearchTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID string `json:"assistant_id"`
		Query       string `json:"query"`
		Kind        string `json:"kind"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return memoryError("invalid", err), nil
	}
	id := t.assistantID
	if strings.TrimSpace(in.AssistantID) != "" {
		id = strings.TrimSpace(in.AssistantID)
	}
	snapshot, err := t.store.Get(id)
	if err != nil {
		return memoryError("retryable_error", err), nil
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	kind := strings.TrimSpace(in.Kind)
	var items []assistant.MemoryItem
	for _, item := range snapshot.Memory.Items {
		if kind != "" && string(item.Kind) != kind {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(string(item.Kind)+" "+item.Body), query) {
			continue
		}
		items = append(items, item)
	}
	return memoryResult{Status: "accepted", Items: items, Revision: snapshot.Memory.Revision}.String(), nil
}

// ---- memory_remember --------------------------------------------------------

type memoryRememberTool struct{ *memoryTool }

// NewMemoryRememberTool upserts one Memory item.
func NewMemoryRememberTool(store *assistant.Store, assistantID string) *memoryRememberTool {
	return &memoryRememberTool{memoryTool: newMemoryTool(store, assistantID)}
}

func (t *memoryRememberTool) Name() string   { return "memory_remember" }
func (t *memoryRememberTool) ReadOnly() bool { return false }

func (t *memoryRememberTool) Description() string {
	return "Record or replace a long-term memory item. Kind is facts, strategy, metrics, charter, or open_loops; always record a source so the fact can be traced. Pass expected_revision to reject a stale write; request_id makes the write replay-safe."
}

func (t *memoryRememberTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"memory_id":{"type":"string","description":"Stable memory ID; defaults to a deterministic ID derived from kind+body."},"kind":{"type":"string"},"body":{"type":"string"},"source":{"type":"string","description":"Where this fact came from (run, url, session)."},"evidence":{"type":"string"},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["kind","body","request_id"]}`)
}

func (t *memoryRememberTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID      string `json:"assistant_id"`
		MemoryID         string `json:"memory_id"`
		Kind             string `json:"kind"`
		Body             string `json:"body"`
		Source           string `json:"source"`
		Evidence         string `json:"evidence"`
		ExpectedRevision int64  `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return memoryError("invalid", err), nil
	}
	id := t.assistantID
	if strings.TrimSpace(in.AssistantID) != "" {
		id = strings.TrimSpace(in.AssistantID)
	}
	if in.RequestID == "" || strings.TrimSpace(in.Body) == "" {
		return memoryError("invalid", fmt.Errorf("body and request_id are required")), nil
	}
	memoryID := strings.TrimSpace(in.MemoryID)
	if memoryID == "" {
		memoryID = assistant.StableID("mem", in.Kind+"/"+in.Body)
	}
	memory, err := t.store.ApplyMemory(id, in.RequestID, in.ExpectedRevision, assistant.MemoryPatch{
		Upsert: []assistant.MemoryItem{{
			ID: memoryID, Kind: assistant.MemoryKind(in.Kind), Body: in.Body,
			SourceRun: in.Source, Evidence: in.Evidence,
		}},
	}, time.Now().UTC())
	if err != nil {
		return memoryError(mapStoreError(err), err), nil
	}
	return memoryResult{Status: "accepted", Memory: &memory, Revision: memory.Revision}.String(), nil
}

// ---- memory_forget ----------------------------------------------------------

type memoryForgetTool struct{ *memoryTool }

// NewMemoryForgetTool deletes one Memory item.
func NewMemoryForgetTool(store *assistant.Store, assistantID string) *memoryForgetTool {
	return &memoryForgetTool{memoryTool: newMemoryTool(store, assistantID)}
}

func (t *memoryForgetTool) Name() string   { return "memory_forget" }
func (t *memoryForgetTool) ReadOnly() bool { return false }

func (t *memoryForgetTool) Description() string {
	return "Retire a stale memory item by ID. Pass expected_revision to reject a stale write; request_id makes the delete replay-safe."
}

func (t *memoryForgetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"memory_id":{"type":"string"},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["memory_id","request_id"]}`)
}

func (t *memoryForgetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID      string `json:"assistant_id"`
		MemoryID         string `json:"memory_id"`
		ExpectedRevision int64  `json:"expected_revision"`
		RequestID        string `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return memoryError("invalid", err), nil
	}
	id := t.assistantID
	if strings.TrimSpace(in.AssistantID) != "" {
		id = strings.TrimSpace(in.AssistantID)
	}
	if in.RequestID == "" || in.MemoryID == "" {
		return memoryError("invalid", fmt.Errorf("memory_id and request_id are required")), nil
	}
	memory, err := t.store.ApplyMemory(id, in.RequestID, in.ExpectedRevision, assistant.MemoryPatch{
		Delete: []string{in.MemoryID},
	}, time.Now().UTC())
	if err != nil {
		return memoryError(mapStoreError(err), err), nil
	}
	return memoryResult{Status: "accepted", Memory: &memory, Revision: memory.Revision}.String(), nil
}
