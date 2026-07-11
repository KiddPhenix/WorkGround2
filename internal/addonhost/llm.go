package addonhost

import (
	"context"
	"encoding/json"
	"fmt"
)

// ── LLM API ─────────────────────────────────────────────────────────────────

// LLMClient is the interface the host uses to call the LLM.
// It mirrors provider.Provider's Stream method.
type LLMClient interface {
	Stream(ctx context.Context, req LLMRequest) (<-chan LLMChunk, error)
}

// LLMCapability is an optional Host extension for LLM access.
type LLMCapability struct {
	Client LLMClient
	// Scoped is the list of allowed models. Empty means any model.
	Scoped []string
}

// LLMRequest is a single completion request from an AddOn.
type LLMRequest struct {
	Model       string       `json:"model,omitempty"`
	Messages    []LLMMessage `json:"messages"`
	Tools       []LLMTool    `json:"tools,omitempty"`
	Temperature float64      `json:"temperature,omitempty"`
	MaxTokens   int          `json:"maxTokens,omitempty"`
}

// LLMMessage is a single conversation message.
type LLMMessage struct {
	Role    string `json:"role"` // "system" | "user" | "assistant"
	Content string `json:"content"`
}

// LLMTool is a tool definition exposed to the model.
type LLMTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// LLMChunk is a single streamed LLM event.
type LLMChunk struct {
	Type     string       `json:"type"` // "text" | "reasoning" | "tool_call" | "usage" | "error"
	Text     string       `json:"text,omitempty"`
	ToolCall *LLMToolCall `json:"toolCall,omitempty"`
	Err      string       `json:"error,omitempty"`
}

// LLMToolCall is a tool invocation requested by the model.
type LLMToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// LLMCompleteResponse is the aggregated result of a non-streaming completion.
type LLMCompleteResponse struct {
	Content string `json:"content"`
}

// LLMComplete sends a completion request and collects all text chunks into
// a single response. It is a convenience wrapper around Stream for simple
// one-shot queries.
func (h *Host) LLMComplete(ctx context.Context, req LLMRequest) (LLMCompleteResponse, error) {
	if h.llm == nil || h.llm.Client == nil {
		return LLMCompleteResponse{}, fmt.Errorf("%w: LLM capability not configured", ErrBadRequest)
	}
	if !h.llmAllowedModel(req.Model) {
		return LLMCompleteResponse{}, fmt.Errorf("%w: model %q not allowed for this AddOn", ErrBadRequest, req.Model)
	}

	ch, err := h.llm.Client.Stream(ctx, req)
	if err != nil {
		return LLMCompleteResponse{}, err
	}
	var content string
	for chunk := range ch {
		if chunk.Err != "" {
			return LLMCompleteResponse{}, fmt.Errorf("llm stream error: %s", chunk.Err)
		}
		if chunk.Type == "text" {
			content += chunk.Text
		}
	}
	return LLMCompleteResponse{Content: content}, nil
}

// LLMStream starts a streaming completion. The caller must drain the channel.
func (h *Host) LLMStream(ctx context.Context, req LLMRequest) (<-chan LLMChunk, error) {
	if h.llm == nil || h.llm.Client == nil {
		return nil, fmt.Errorf("%w: LLM capability not configured", ErrBadRequest)
	}
	if !h.llmAllowedModel(req.Model) {
		return nil, fmt.Errorf("%w: model %q not allowed for this AddOn", ErrBadRequest, req.Model)
	}
	return h.llm.Client.Stream(ctx, req)
}

func (h *Host) llmAllowedModel(model string) bool {
	if h.llm == nil || len(h.llm.Scoped) == 0 {
		return true // no restriction
	}
	if model == "" {
		return true
	}
	for _, m := range h.llm.Scoped {
		if m == model {
			return true
		}
	}
	return false
}

// llm is set by SetLLMCapability.
var _ = json.Marshal // keep json import
