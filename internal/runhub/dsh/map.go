package dsh

import (
	"encoding/json"
	"fmt"
)

// DefaultMaxSummary bounds the final assistant summary persisted into a run.
// It is a cleaned, truncated tail of the final assistant text — never the
// prompt, reasoning, tool arguments or tool results.
const DefaultMaxSummary = 512

// DefaultMaxLabel bounds an activity label (a tool name or short phase note).
const DefaultMaxLabel = 128

// Session event types the runner maps. Unknown types are ignored: the runtime
// may emit a superset of this vocabulary, and we must not persist anything we
// do not understand.
const (
	evTurnStart        = "turn/start"
	evTurnEnd          = "turn/end"
	evAssistantChunk   = "assistant/chunk"
	evAssistantMessage = "assistant/message"
	evToolCall         = "tool/call"
	evToolResult       = "tool/result"
	evInboxSpliced     = "agent/inbox/spliced"
)

// sessionEvent is the minimal wire envelope read from a session.event payload.
// Only type, seq and data are decoded; the full event is never retained.
type sessionEvent struct {
	Type string          `json:"type"`
	Seq  int64           `json:"seq"`
	Data json.RawMessage `json:"data"`
}

// decodeSessionEvent parses the nested event envelope. A malformed envelope is
// a protocol error, not silently dropped state.
func decodeSessionEvent(raw json.RawMessage) (sessionEvent, error) {
	var ev sessionEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return ev, fmt.Errorf("dsh: decode session event: %w", err)
	}
	if ev.Type == "" {
		return ev, fmt.Errorf("dsh: session event has no type")
	}
	return ev, nil
}

// isInboxReceipt reports whether ev is the durable enqueue receipt for messageID
// (the `agent/inbox/spliced` event whose inserted messages include messageID).
func isInboxReceipt(ev sessionEvent, messageID string) bool {
	if ev.Type != evInboxSpliced {
		return false
	}
	var data struct {
		Inserted []struct {
			ID string `json:"id"`
		} `json:"inserted"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return false
	}
	for _, m := range data.Inserted {
		if m.ID == messageID {
			return true
		}
	}
	return false
}

// toolCallName extracts the tool name from a tool/call event. It never reads the
// raw arguments, which are present on the wire but must not be persisted.
func toolCallName(ev sessionEvent) string {
	if ev.Type != evToolCall {
		return ""
	}
	var data struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return ""
	}
	return truncateRunes(sanitizeDiagnostic(data.Name), DefaultMaxLabel)
}

// turnEndKind extracts the `kind` of a turn/end reason (e.g. completed, error,
// aborted, max-tokens, blocked, interrupted). Unknown future kinds are preserved
// so settlement can still distinguish failure from success.
func turnEndKind(ev sessionEvent) string {
	if ev.Type != evTurnEnd {
		return ""
	}
	var data struct {
		Reason struct {
			Kind string `json:"kind"`
		} `json:"reason"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return ""
	}
	return data.Reason.Kind
}

// assistantText concatenates the text blocks of an assistant/message event. It
// is the only model output the runner ever reads, and only into the final
// summary; reasoning and raw chunks are never consulted. max bounds the result
// (<= 0 selects DefaultMaxSummary).
func assistantText(ev sessionEvent, max int) string {
	if ev.Type != evAssistantMessage {
		return ""
	}
	if max <= 0 {
		max = DefaultMaxSummary
	}
	var data struct {
		Message struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(ev.Data, &data); err != nil {
		return ""
	}
	var out string
	for _, b := range data.Message.Content {
		if b.Type == "text" {
			out += b.Text
		}
	}
	return truncateRunes(sanitizeDiagnostic(out), max)
}

// truncateRunes shortens s to at most max runes, appended with an ellipsis when
// truncated. max <= 0 returns s unchanged.
func truncateRunes(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}
