package addonhost

import (
	"context"
	"encoding/json"
	"fmt"
)

// ── Tools API ───────────────────────────────────────────────────────────────

// ToolRegistry is the interface a host tool registry must satisfy.
type ToolRegistry interface {
	// Get returns the tool by name, or nil if not found.
	Get(name string) ToolLike
	// Names returns all registered tool names visible to this AddOn.
	Names() []string
}

// ToolLike is a minimal tool interface. It mirrors tool.Tool.
type ToolLike interface {
	Name() string
	Description() string
	Schema() json.RawMessage
	Execute(ctx context.Context, args json.RawMessage) (string, error)
	ReadOnly() bool
}

// ToolInfo is the public metadata for a host tool.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	ReadOnly    bool            `json:"readOnly"`
}

// ToolCallRequest is the input for calling a host tool.
type ToolCallRequest struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Reason    string          `json:"reason"`
}

// ToolCallResponse is the output of a host tool call.
type ToolCallResponse struct {
	Output string `json:"output"`
	Error  string `json:"error,omitempty"`
}

// ToolsList returns the registered host tools visible to this AddOn.
func (h *Host) ToolsList() ([]ToolInfo, error) {
	if h.tools == nil {
		return []ToolInfo{}, nil
	}
	names := h.tools.Names()
	out := make([]ToolInfo, 0, len(names))
	for _, name := range names {
		t := h.tools.Get(name)
		if t == nil {
			continue
		}
		out = append(out, ToolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      t.Schema(),
			ReadOnly:    t.ReadOnly(),
		})
	}
	return out, nil
}

// ToolsCall executes a host tool on behalf of the AddOn.
func (h *Host) ToolsCall(ctx context.Context, req ToolCallRequest) (ToolCallResponse, error) {
	if h.tools == nil {
		return ToolCallResponse{Error: "tool registry not configured"}, fmt.Errorf("%w: tool registry not configured", ErrBadRequest)
	}
	t := h.tools.Get(req.Name)
	if t == nil {
		return ToolCallResponse{Error: fmt.Sprintf("tool %q not found", req.Name)}, nil
	}
	output, err := t.Execute(ctx, req.Arguments)
	if err != nil {
		return ToolCallResponse{Output: output, Error: err.Error()}, nil
	}
	return ToolCallResponse{Output: output}, nil
}

// Ensure json is used.
var _ = json.Marshal
