package dsh

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// DefaultMaxFrameSize bounds one newline-delimited JSON-RPC frame. The DSH SDK
// speaks newline-delimited JSON over stdio; this codec never assumes a richer
// transport and never persists transcripts.
const DefaultMaxFrameSize = 1 << 20

// DefaultMaxErrorMessage bounds the human-readable JSON-RPC error message so a
// runaway peer cannot force an unbounded allocation in the client.
const DefaultMaxErrorMessage = 4096

// JSON-RPC 2.0 method names used by the DSH SDK baseline.
const (
	MethodInitialize       = "initialize"
	MethodPrompt           = "session/prompt"
	MethodShutdown         = "shutdown"
	MethodSessionEvent     = "session.event"
	MethodSessionStatus    = "session.status"
	MethodSubagentStarted  = "subagent.started"
	MethodSubagentFinished = "subagent.finished"
)

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Error renders the code and message so *RPCError satisfies error. Data is
// intentionally omitted: it may carry provider or tool payloads that must not
// enter diagnostic strings or persisted state.
func (e *RPCError) Error() string {
	if e == nil {
		return "dsh: <nil rpc error>"
	}
	return fmt.Sprintf("dsh: json-rpc error %d: %s", e.Code, e.Message)
}

// Frame is one JSON-RPC 2.0 message. Payloads are kept as json.RawMessage or
// typed safe fields (see the typed structs below), never as opaque strings.
type Frame struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// IsRequest reports whether f is a request (method plus id).
func (f Frame) IsRequest() bool { return f.Method != "" && len(f.ID) > 0 }

// IsNotification reports whether f is a notification (method without id).
func (f Frame) IsNotification() bool { return f.Method != "" && len(f.ID) == 0 }

// IsResponse reports whether f is a response or error (id, no method).
func (f Frame) IsResponse() bool {
	return f.Method == "" && len(f.ID) > 0 && (f.Result != nil || f.Error != nil)
}

// Validate enforces the JSON-RPC 2.0 version and the structural rules for a
// single frame: a request or notification (method present) must not also carry
// a result or error, and a response (id present, no method) must carry exactly
// one of result or error.
func (f Frame) Validate() error {
	if f.JSONRPC != "2.0" {
		return fmt.Errorf("dsh: unsupported jsonrpc version %q", f.JSONRPC)
	}
	if len(f.ID) > 0 && !validID(f.ID) {
		return errors.New("dsh: jsonrpc id must be a string, number, or null")
	}
	if f.Error != nil && len(f.Error.Message) > DefaultMaxErrorMessage {
		return fmt.Errorf("dsh: error message length %d exceeds limit %d", len(f.Error.Message), DefaultMaxErrorMessage)
	}
	switch {
	case f.Method != "":
		if strings.TrimSpace(f.Method) == "" {
			return errors.New("dsh: method is empty")
		}
		if f.Result != nil || f.Error != nil {
			return errors.New("dsh: request/notification frame must not carry result or error")
		}
		return nil
	case len(f.ID) > 0:
		if f.Params != nil {
			return errors.New("dsh: response frame must not carry params")
		}
		if f.Result != nil && f.Error != nil {
			return errors.New("dsh: response frame must not carry both result and error")
		}
		if f.Result == nil && f.Error == nil {
			return errors.New("dsh: response frame must carry exactly one of result or error")
		}
		return nil
	default:
		return errors.New("dsh: frame must be a request, notification, or response")
	}
}

// Request builds a request frame. id may be a string or number; params may be
// nil (params is omitted).
func Request(id any, method string, params any) (Frame, error) {
	f := Frame{JSONRPC: "2.0", Method: method}
	raw, err := marshalRaw(id)
	if err != nil {
		return Frame{}, err
	}
	f.ID = raw
	if params != nil {
		if f.Params, err = marshalRaw(params); err != nil {
			return Frame{}, err
		}
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// Notification builds a notification frame (no id).
func Notification(method string, params any) (Frame, error) {
	f := Frame{JSONRPC: "2.0", Method: method}
	if params == nil {
		return f, f.Validate()
	}
	raw, err := marshalRaw(params)
	if err != nil {
		return Frame{}, err
	}
	f.Params = raw
	return f, f.Validate()
}

// Response builds a success response frame.
func Response(id any, result any) (Frame, error) {
	f := Frame{JSONRPC: "2.0"}
	raw, err := marshalRaw(id)
	if err != nil {
		return Frame{}, err
	}
	f.ID = raw
	if f.Result, err = marshalRaw(result); err != nil {
		return Frame{}, err
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// Error builds an error response frame. It fails instead of silently dropping
// an unmarshalable id, and rejects an error message over the bounded limit.
func Error(id any, code int, message string) (Frame, error) {
	if len(message) > DefaultMaxErrorMessage {
		return Frame{}, fmt.Errorf("dsh: error message length %d exceeds limit %d", len(message), DefaultMaxErrorMessage)
	}
	f := Frame{JSONRPC: "2.0", Error: &RPCError{Code: code, Message: message}}
	raw, err := marshalRaw(id)
	if err != nil {
		return Frame{}, err
	}
	f.ID = raw
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// DecodeParams unmarshals the frame's params into dst.
func (f Frame) DecodeParams(dst any) error {
	if len(f.Params) == 0 {
		return errors.New("dsh: frame has no params")
	}
	if err := json.Unmarshal(f.Params, dst); err != nil {
		return fmt.Errorf("dsh: decode params: %w", err)
	}
	return nil
}

// DecodeResult unmarshals the frame's result into dst.
func (f Frame) DecodeResult(dst any) error {
	if len(f.Result) == 0 {
		return errors.New("dsh: frame has no result")
	}
	if err := json.Unmarshal(f.Result, dst); err != nil {
		return fmt.Errorf("dsh: decode result: %w", err)
	}
	return nil
}

// Encode writes f as one newline-terminated JSON line to w.
func Encode(w io.Writer, f Frame) error {
	data, err := Marshal(f)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("dsh: write frame: %w", err)
	}
	if _, err := io.WriteString(w, "\n"); err != nil {
		return fmt.Errorf("dsh: write frame terminator: %w", err)
	}
	return nil
}

// Marshal serializes f to JSON and enforces the frame size bound.
func Marshal(f Frame) ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(f)
	if err != nil {
		return nil, fmt.Errorf("dsh: marshal frame: %w", err)
	}
	if len(data) > DefaultMaxFrameSize {
		return nil, fmt.Errorf("dsh: frame size %d exceeds limit %d", len(data), DefaultMaxFrameSize)
	}
	return data, nil
}

// DecodeFrame parses one frame from line (a trailing newline is tolerated) and
// enforces the default frame size bound.
func DecodeFrame(line []byte) (Frame, error) {
	return DecodeFrameSize(line, DefaultMaxFrameSize)
}

// DecodeFrameSize parses one frame from line, enforcing maxSize (<= 0 selects
// DefaultMaxFrameSize). The size bound is applied before JSON parsing so an
// oversized line is rejected rather than partially decoded, and the parsed
// frame is structurally validated.
func DecodeFrameSize(line []byte, maxSize int) (Frame, error) {
	if maxSize <= 0 {
		maxSize = DefaultMaxFrameSize
	}
	if len(line) > maxSize {
		return Frame{}, fmt.Errorf("dsh: frame size %d exceeds limit %d", len(line), maxSize)
	}
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return Frame{}, errors.New("dsh: empty frame")
	}
	var f Frame
	if err := json.Unmarshal(line, &f); err != nil {
		return Frame{}, fmt.Errorf("dsh: decode frame: %w", err)
	}
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}

// Decoder streams newline-delimited frames with a size bound. A frame larger
// than maxSize fails with an error rather than being truncated.
type Decoder struct {
	sc *bufio.Scanner
}

// NewDecoder returns a Decoder over r. maxSize <= 0 selects DefaultMaxFrameSize.
func NewDecoder(r io.Reader, maxSize int) *Decoder {
	if maxSize <= 0 {
		maxSize = DefaultMaxFrameSize
	}
	if maxSize < 64 {
		maxSize = 64
	}
	sc := bufio.NewScanner(r)
	// The scanner's effective token limit is max(initial buffer capacity,
	// maxSize), so the starting buffer must not exceed maxSize or the bound is
	// silently widened.
	start := 64 * 1024
	if start > maxSize {
		start = maxSize
	}
	sc.Buffer(make([]byte, 0, start), maxSize)
	return &Decoder{sc: sc}
}

// Decode returns the next frame, or io.EOF at end of stream.
func (d *Decoder) Decode() (Frame, error) {
	if d == nil || d.sc == nil {
		return Frame{}, errors.New("dsh: nil decoder")
	}
	if !d.sc.Scan() {
		if err := d.sc.Err(); err != nil {
			return Frame{}, fmt.Errorf("dsh: read frame: %w", err)
		}
		return Frame{}, io.EOF
	}
	return DecodeFrame(d.sc.Bytes())
}

func marshalRaw(v any) (json.RawMessage, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("dsh: marshal value: %w", err)
	}
	return json.RawMessage(data), nil
}

func validID(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if !json.Valid(trimmed) {
		return false
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] == '"' {
		var value string
		return json.Unmarshal(trimmed, &value) == nil
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return false
	}
	var trailing any
	return number.String() != "" && errors.Is(decoder.Decode(&trailing), io.EOF)
}

// InitializeParams is the process-wide handshake request payload (rc.8). cwd,
// provider and model are required; maxTokens is an optional positive output-token
// cap (omitted when zero/absent).
type InitializeParams struct {
	CWD       string `json:"cwd"`
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	MaxTokens int64  `json:"maxTokens,omitempty"`
}

// ServerInfo is the wire-stable server identity returned by initialization.
// name stays the wire-stable `deepseek-harness-sdk-runtime`.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult is the initialize response payload (rc.8): serverInfo only.
type InitializeResult struct {
	ServerInfo ServerInfo `json:"serverInfo"`
}

// PromptParams is the session/prompt request payload (rc.8). sessionId is
// required and contentBlocks is the ordered prompt content, preserved verbatim
// as a bounded JSON array rather than flattened into a single string.
type PromptParams struct {
	SessionID     string          `json:"sessionId"`
	ContentBlocks json.RawMessage `json:"contentBlocks"`
}

// PromptResult is the session/prompt response payload (rc.8).
type PromptResult struct {
	MessageID string `json:"messageId"`
}

// ShutdownParams is the shutdown request payload (currently empty).
type ShutdownParams struct{}

// ShutdownResult is the shutdown response payload (currently empty).
type ShutdownResult struct{}

// SessionEventParams is the session.event notification payload. The full
// session-log event envelope is preserved as a bounded raw message.
type SessionEventParams struct {
	SessionID string          `json:"sessionId"`
	Event     json.RawMessage `json:"event"`
}

// SessionStatusParams is the session.status notification payload.
type SessionStatusParams struct {
	SessionID string        `json:"sessionId"`
	Status    SessionStatus `json:"status"`
}

// SessionStatus is the complete rc.8 whole-Agent status vocabulary.
type SessionStatus string

const (
	SessionRunning SessionStatus = "running"
	SessionIdle    SessionStatus = "idle"
)

// SubagentStartedParams is the subagent.started notification payload (rc.8).
type SubagentStartedParams struct {
	ParentSessionID string `json:"parentSessionId"`
	ChildSessionID  string `json:"childSessionId"`
}

// SubagentFinishedParams is the subagent.finished notification payload (rc.8).
// lastAssistantMessage is the child's selected assistant output, preserved as a
// bounded raw message and absent when the child produced none.
type SubagentFinishedParams struct {
	Provider             string             `json:"provider"`
	AgentID              string             `json:"agentId"`
	ParentSessionID      string             `json:"parentSessionId"`
	ChildSessionID       string             `json:"childSessionId"`
	Status               SdkRunStatus       `json:"status"`
	StopReason           SubagentStopReason `json:"stopReason"`
	LastAssistantMessage json.RawMessage    `json:"lastAssistantMessage,omitempty"`
}

// SdkRunStatus is the complete rc.8 deployment-mapped result vocabulary.
type SdkRunStatus string

const (
	SdkRunOK    SdkRunStatus = "ok"
	SdkRunError SdkRunStatus = "error"
)

// SubagentStopReason is the known rc.8 stop vocabulary. DSH declares the map
// merge-extensible, so callers must preserve and surface unknown future values.
type SubagentStopReason string

const (
	SubagentCompleted SubagentStopReason = "completed"
	SubagentAborted   SubagentStopReason = "aborted"
	SubagentError     SubagentStopReason = "error"
	SubagentMaxTokens SubagentStopReason = "max-tokens"
	SubagentRefusal   SubagentStopReason = "refusal"
)
