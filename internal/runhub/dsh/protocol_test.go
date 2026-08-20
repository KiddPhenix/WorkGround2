package dsh

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func loadFrame(t *testing.T, name string) Frame {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	f, err := DecodeFrame(data)
	if err != nil {
		t.Fatalf("DecodeFrame(%s): %v", name, err)
	}
	return f
}

func TestFixtureRequestsAndResponses(t *testing.T) {
	init := loadFrame(t, "initialize_request.json")
	if !init.IsRequest() || init.Method != MethodInitialize {
		t.Fatalf("initialize_request not a request: %+v", init)
	}
	var ip InitializeParams
	if err := init.DecodeParams(&ip); err != nil {
		t.Fatal(err)
	}
	if ip.CWD != "/work" || ip.Provider != "deepseek-official" || ip.Model != "deepseek-chat" || ip.MaxTokens != 4096 {
		t.Fatalf("initialize params = %+v", ip)
	}

	initResp := loadFrame(t, "initialize_response.json")
	if !initResp.IsResponse() {
		t.Fatalf("initialize_response not a response: %+v", initResp)
	}
	var ir InitializeResult
	if err := initResp.DecodeResult(&ir); err != nil {
		t.Fatal(err)
	}
	if ir.ServerInfo.Name != "deepseek-harness-sdk-runtime" || ir.ServerInfo.Version != "0.0.1" {
		t.Fatalf("initialize result = %+v", ir)
	}
}

func TestFixturePromptContentBlocks(t *testing.T) {
	prompt := loadFrame(t, "prompt_request.json")
	if prompt.Method != MethodPrompt {
		t.Fatalf("prompt method = %s", prompt.Method)
	}
	var pp PromptParams
	if err := prompt.DecodeParams(&pp); err != nil {
		t.Fatal(err)
	}
	if pp.SessionID != "sess-1" {
		t.Fatalf("prompt sessionId = %q", pp.SessionID)
	}
	// contentBlocks must be preserved verbatim as an ordered block array, never
	// flattened into a single string.
	var blocks []map[string]any
	if err := json.Unmarshal(pp.ContentBlocks, &blocks); err != nil {
		t.Fatalf("contentBlocks not a JSON array: %v", err)
	}
	if len(blocks) != 1 || blocks[0]["type"] != "text" || blocks[0]["text"] != "hello" {
		t.Fatalf("contentBlocks = %+v", blocks)
	}

	promptResp := loadFrame(t, "prompt_response.json")
	var pr PromptResult
	if err := promptResp.DecodeResult(&pr); err != nil || pr.MessageID != "msg-1" {
		t.Fatalf("prompt result = %+v err=%v", pr, err)
	}
}

func TestFixtureNotifications(t *testing.T) {
	cases := []struct {
		file   string
		method string
	}{
		{"session_event.json", MethodSessionEvent},
		{"session_status.json", MethodSessionStatus},
		{"subagent_started.json", MethodSubagentStarted},
		{"subagent_finished.json", MethodSubagentFinished},
	}
	for _, c := range cases {
		f := loadFrame(t, c.file)
		if !f.IsNotification() {
			t.Fatalf("%s not a notification: %+v", c.file, f)
		}
		if f.Method != c.method {
			t.Fatalf("%s method = %s, want %s", c.file, f.Method, c.method)
		}
	}

	started := loadFrame(t, "subagent_started.json")
	var sp SubagentStartedParams
	if err := started.DecodeParams(&sp); err != nil || sp.ParentSessionID != "sess-1" || sp.ChildSessionID != "sub-1" {
		t.Fatalf("subagent.started params = %+v err=%v", sp, err)
	}

	finished := loadFrame(t, "subagent_finished.json")
	var fp SubagentFinishedParams
	if err := finished.DecodeParams(&fp); err != nil {
		t.Fatal(err)
	}
	if fp.Provider != "codex" || fp.AgentID != "sub-1" || fp.ParentSessionID != "sess-1" ||
		fp.ChildSessionID != "sub-1" || fp.Status != SdkRunOK || fp.StopReason != SubagentCompleted {
		t.Fatalf("subagent.finished params = %+v", fp)
	}
	if len(fp.LastAssistantMessage) == 0 {
		t.Fatalf("lastAssistantMessage missing: %+v", fp)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	req, err := Request("7", MethodPrompt, PromptParams{
		SessionID:     "s",
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hi"}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := Encode(&buf, req); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsRequest() || got.Method != MethodPrompt {
		t.Fatalf("round-trip frame = %+v", got)
	}
	var pp PromptParams
	if err := got.DecodeParams(&pp); err != nil || pp.SessionID != "s" || !bytes.Contains(pp.ContentBlocks, []byte("hi")) {
		t.Fatalf("round-trip params = %+v err=%v", pp, err)
	}
}

func TestDecoderStream(t *testing.T) {
	data, err := os.ReadFile("testdata/session_stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	dec := NewDecoder(bytes.NewReader(data), 0)
	var methods []string
	for {
		f, err := dec.Decode()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Decode: %v", err)
		}
		if f.Method != "" {
			methods = append(methods, f.Method)
		}
	}
	want := []string{MethodInitialize, MethodPrompt, MethodSessionEvent, MethodSubagentStarted, MethodSubagentFinished, MethodShutdown}
	if strings.Join(methods, ",") != strings.Join(want, ",") {
		t.Fatalf("stream methods = %v, want %v", methods, want)
	}
}

func TestFrameSizeBound(t *testing.T) {
	big := strings.Repeat("x", DefaultMaxFrameSize+1)
	_, err := Marshal(Frame{JSONRPC: "2.0", Method: MethodPrompt, Params: []byte(`{"prompt":"` + big + `"}`)})
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized frame accepted: %v", err)
	}
}

func TestDecoderRejectsOversizedLine(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"session.event","params":{"event":{"blob":"` + strings.Repeat("x", 4096) + `"}}}` + "\n"
	dec := NewDecoder(strings.NewReader(line), 512)
	if _, err := dec.Decode(); err == nil {
		t.Fatalf("oversized line accepted")
	}
}

func TestDecodeFrameRejectsOversizedLine(t *testing.T) {
	line := `{"jsonrpc":"2.0","method":"session.event","params":{"event":{"blob":"` + strings.Repeat("x", 4096) + `"}}}`
	if _, err := DecodeFrameSize([]byte(line), 512); err == nil {
		t.Fatalf("oversized line accepted by DecodeFrameSize")
	}
}

func TestDecodeMalformed(t *testing.T) {
	if _, err := DecodeFrame([]byte("{not json")); err == nil {
		t.Fatalf("malformed frame accepted")
	}
	if _, err := DecodeFrame([]byte("\n")); err == nil {
		t.Fatalf("empty frame accepted")
	}
}

func TestFrameValidateStructural(t *testing.T) {
	valid := []Frame{
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Method: "session/prompt"},
		{JSONRPC: "2.0", Method: "session.event"},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Result: json.RawMessage(`{}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Error: &RPCError{Code: -32601, Message: "nope"}},
	}
	for _, f := range valid {
		if err := f.Validate(); err != nil {
			t.Fatalf("valid frame rejected: %+v err=%v", f, err)
		}
	}

	invalid := []Frame{
		{JSONRPC: "1.0", Method: "session.event"},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Result: json.RawMessage(`{}`), Error: &RPCError{Code: 1, Message: "x"}},
		{JSONRPC: "2.0", Method: "session.event", Result: json.RawMessage(`{}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Params: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`{}`), Result: json.RawMessage(`{}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`"1"`), Error: &RPCError{Code: 1, Message: strings.Repeat("x", DefaultMaxErrorMessage+1)}},
		{JSONRPC: "2.0"},
	}
	for _, f := range invalid {
		if err := f.Validate(); err == nil {
			t.Fatalf("invalid frame accepted: %+v", f)
		}
	}
}

func TestBuildersRejectInvalidFrames(t *testing.T) {
	if _, err := Request("1", "", nil); err == nil {
		t.Fatal("request with empty method accepted")
	}
	if _, err := Notification("", nil); err == nil {
		t.Fatal("notification with empty method accepted")
	}
	if _, err := Request(map[string]string{"bad": "id"}, MethodPrompt, PromptParams{}); err == nil {
		t.Fatal("object request id accepted")
	}
	if _, err := Response("1", nil); err != nil {
		t.Fatalf("null result rejected: %v", err)
	}
}

func TestMarshalValidatesFrame(t *testing.T) {
	if _, err := Marshal(Frame{JSONRPC: "2.0", ID: json.RawMessage(`"1"`)}); err == nil {
		t.Fatal("structurally invalid frame marshaled")
	}
}

func TestDecodeBoundCountsSurroundingWhitespace(t *testing.T) {
	line := append(bytes.Repeat([]byte(" "), 128), []byte(`{"jsonrpc":"2.0","method":"session.event"}`)...)
	if _, err := DecodeFrameSize(line, 64); err == nil {
		t.Fatal("oversized whitespace-prefixed frame accepted")
	}
}

func TestDecodeRejectsConflictingResultAndError(t *testing.T) {
	line := `{"jsonrpc":"2.0","id":"1","result":{},"error":{"code":1,"message":"x"}}`
	if _, err := DecodeFrame([]byte(line)); err == nil {
		t.Fatalf("conflicting result/error frame accepted")
	}
}

func TestErrorResponse(t *testing.T) {
	f, err := Error("9", -32601, "method not found")
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsResponse() || f.Error == nil || f.Error.Code != -32601 {
		t.Fatalf("error frame = %+v", f)
	}
	var buf bytes.Buffer
	if err := Encode(&buf, f); err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(buf.Bytes())
	if err != nil || got.Error == nil || got.Error.Message != "method not found" {
		t.Fatalf("error round-trip = %+v err=%v", got, err)
	}
}

func TestErrorRejectsUnmarshallableID(t *testing.T) {
	if _, err := Error(make(chan int), -32601, "nope"); err == nil {
		t.Fatalf("unmarshalable id silently dropped")
	}
}

func TestErrorRejectsOversizedMessage(t *testing.T) {
	if _, err := Error("9", -32603, strings.Repeat("x", DefaultMaxErrorMessage+1)); err == nil {
		t.Fatalf("oversized error message accepted")
	}
}
