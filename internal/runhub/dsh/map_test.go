package dsh

import (
	"encoding/json"
	"strings"
	"testing"
)

func ev(t *testing.T, typ string, data string) sessionEvent {
	t.Helper()
	var raw struct {
		Type string          `json:"type"`
		Seq  int64           `json:"seq"`
		Data json.RawMessage `json:"data"`
	}
	raw.Type = typ
	raw.Seq = 1
	raw.Data = json.RawMessage(data)
	out, err := decodeSessionEvent(mustRaw(t, raw))
	if err != nil {
		t.Fatalf("decodeSessionEvent(%s): %v", typ, err)
	}
	return out
}

func mustRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestInboxReceiptMatchesOnlyMessageID(t *testing.T) {
	e := ev(t, evInboxSpliced, `{"inserted":[{"id":"msg-1"},{"id":"msg-2"}]}`)
	if !isInboxReceipt(e, "msg-1") {
		t.Fatalf("inbox receipt should match msg-1")
	}
	if isInboxReceipt(e, "msg-other") {
		t.Fatalf("inbox receipt should not match msg-other")
	}
	if isInboxReceipt(ev(t, evTurnStart, `{"turn":1}`), "msg-1") {
		t.Fatalf("turn/start is not an inbox receipt")
	}
}

func TestToolCallNameNeverReadsArguments(t *testing.T) {
	e := ev(t, evToolCall, `{"turn":1,"step":1,"callId":"c1","name":"read","arguments":"{\"path\":\"/secret\"}"}`)
	if got := toolCallName(e); got != "read" {
		t.Fatalf("toolCallName = %q, want read", got)
	}
	if strings.Contains(toolCallName(e), "secret") {
		t.Fatalf("toolCallName leaked arguments")
	}
}

func TestAssistantTextConcatenatesOnlyText(t *testing.T) {
	e := ev(t, evAssistantMessage, `{"turn":1,"step":1,"message":{"content":[{"type":"text","text":"hello "},{"type":"reasoning","text":"hidden"},{"type":"text","text":"world"}]}}`)
	if got := assistantText(e, 0); got != "hello world" {
		t.Fatalf("assistantText = %q, want %q", got, "hello world")
	}
}

func TestAssistantTextTruncates(t *testing.T) {
	e := ev(t, evAssistantMessage, `{"turn":1,"step":1,"message":{"content":[{"type":"text","text":"abcdefgh"}]}}`)
	if got := assistantText(e, 4); got != "abcd…" {
		t.Fatalf("assistantText truncated = %q, want %q", got, "abcd…")
	}
}

func TestTurnEndKind(t *testing.T) {
	e := ev(t, evTurnEnd, `{"turn":1,"reason":{"kind":"completed"}}`)
	if got := turnEndKind(e); got != "completed" {
		t.Fatalf("turnEndKind = %q, want completed", got)
	}
	if got := turnEndKind(ev(t, evTurnStart, `{"turn":1}`)); got != "" {
		t.Fatalf("turnEndKind on turn/start = %q, want empty", got)
	}
}

func TestDecodeSessionEventRejectsMalformed(t *testing.T) {
	if _, err := decodeSessionEvent(json.RawMessage(`{"type":`)); err == nil {
		t.Fatalf("malformed session event accepted")
	}
	if _, err := decodeSessionEvent(json.RawMessage(`{"seq":1}`)); err == nil {
		t.Fatalf("session event without type accepted")
	}
}
