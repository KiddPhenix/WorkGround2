// Package cli implements a local command-line provider.
//
// The command receives one UTF-8 JSON request on stdin and returns either plain
// text, one JSON object, or newline-delimited JSON chunks on stdout.
package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"workground2/internal/proc"
	"workground2/internal/provider"
)

const (
	defaultTimeout = 120 * time.Second
	maxOutputBytes = 10 << 20
	maxStderrBytes = 64 << 10
)

func init() {
	provider.Register("cli", New)
}

type client struct {
	name     string
	model    string
	command  string
	args     []string
	protocol string
	timeout  time.Duration
	vision   bool
	codex    bool
}

type request struct {
	Model       string                `json:"model"`
	Messages    []provider.Message    `json:"messages"`
	Tools       []provider.ToolSchema `json:"tools,omitempty"`
	Temperature float64               `json:"temperature,omitempty"`
	MaxTokens   int                   `json:"max_tokens,omitempty"`
	Vision      bool                  `json:"vision,omitempty"`
}

type output struct {
	Text      string          `json:"text,omitempty"`
	Type      string          `json:"type,omitempty"`
	ThreadID  string          `json:"thread_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	Delta     string          `json:"delta,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Done      bool            `json:"done,omitempty"`
	Error     string          `json:"error,omitempty"`
	Message   *messageOutput  `json:"message,omitempty"`
	Item      *itemOutput     `json:"item,omitempty"`
	Choices   []choiceOutput  `json:"choices,omitempty"`
	Usage     *provider.Usage `json:"usage,omitempty"`
}

type messageOutput struct {
	Content string `json:"content,omitempty"`
	Text    string `json:"text,omitempty"`
}

func (m *messageOutput) UnmarshalJSON(data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		m.Content = s
		return nil
	}
	type alias messageOutput
	var out alias
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	*m = messageOutput(out)
	return nil
}

func (m *messageOutput) text() string {
	if m == nil {
		return ""
	}
	if strings.TrimSpace(m.Content) != "" {
		return m.Content
	}
	return m.Text
}

type itemOutput struct {
	ID        string          `json:"id,omitempty"`
	Type      string          `json:"type,omitempty"`
	Text      string          `json:"text,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Delta     string          `json:"delta,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	Message   *messageOutput  `json:"message,omitempty"`
}

type choiceOutput struct {
	Text         string         `json:"text,omitempty"`
	FinishReason string         `json:"finish_reason,omitempty"`
	Message      *messageOutput `json:"message,omitempty"`
	Delta        *deltaOutput   `json:"delta,omitempty"`
}

type deltaOutput struct {
	Content          string `json:"content,omitempty"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
}

type jsonlState struct {
	itemText      map[string]string
	itemReasoning map[string]string
	threadID      string
}

// New builds a provider backed by a local CLI process.
func New(cfg provider.Config) (provider.Provider, error) {
	command, _ := cfg.Extra["command"].(string)
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, fmt.Errorf("cli: command is required for provider %q", cfg.Name)
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("cli: model is required for provider %q", cfg.Name)
	}
	name := strings.TrimSpace(cfg.Name)
	if name == "" {
		name = "cli"
	}
	args := stringSlice(cfg.Extra["args"])
	protocol := stringValue(cfg.Extra["protocol"])
	args, protocol = NormalizeInvocation(command, args, protocol)
	return &client{
		name:     name,
		model:    model,
		command:  command,
		args:     args,
		protocol: protocol,
		timeout:  timeoutValue(cfg.Extra["timeout_seconds"]),
		vision:   boolValue(cfg.Extra["vision"]),
		codex:    isCodexCommand(command),
	}, nil
}

func (c *client) Name() string { return c.name }

func (c *client) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	out := make(chan provider.Chunk)
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	go func() {
		defer close(out)
		defer cancel()
		if err := c.run(ctx, req, out); err != nil {
			send(ctx, out, provider.Chunk{Type: provider.ChunkError, Err: err})
		}
	}()
	return out, nil
}

func (c *client) run(ctx context.Context, req provider.Request, out chan<- provider.Chunk) error {
	body, err := json.Marshal(request{
		Model:       c.model,
		Messages:    provider.SanitizeToolPairing(req.Messages),
		Tools:       req.Tools,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Vision:      c.vision,
	})
	if err != nil {
		return fmt.Errorf("%s: encode cli request: %w", c.name, err)
	}
	cmd := exec.CommandContext(ctx, c.command, c.args...)
	proc.HideWindow(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("%s: open stdin: %w", c.name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("%s: open stdout: %w", c.name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("%s: open stderr: %w", c.name, err)
	}
	var stderrBuf limitedBuffer
	stderrDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(&stderrBuf, stderr)
		close(stderrDone)
	}()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: start %q: %w", c.name, c.command, err)
	}
	go func() {
		_, _ = stdin.Write(body)
		_ = stdin.Close()
	}()
	threadID, parseErr := c.readStdout(ctx, stdout, out)
	waitErr := cmd.Wait()
	<-stderrDone
	// Report side-effect image artifacts even when the process exits with an
	// error: Codex may have already written PNGs before failing late.
	if c.codex && threadID != "" {
		reportCodexArtifacts(ctx, threadID)
	}
	if waitErr != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s: local cli timed out or was cancelled: %w", c.name, ctx.Err())
		}
		return fmt.Errorf("%s: local cli failed: %w%s", c.name, waitErr, stderrSuffix(stderrBuf.String()))
	}
	if parseErr != nil {
		return parseErr
	}
	return nil
}

func (c *client) readStdout(ctx context.Context, r io.Reader, out chan<- provider.Chunk) (string, error) {
	switch c.protocol {
	case "jsonl":
		state := jsonlState{
			itemText:      map[string]string{},
			itemReasoning: map[string]string{},
		}
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64<<10), maxOutputBytes)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			var ev output
			if err := json.Unmarshal([]byte(line), &ev); err != nil {
				return "", fmt.Errorf("%s: decode jsonl output: %w", c.name, err)
			}
			if ev.Type == "thread.started" && state.threadID == "" {
				state.threadID = strings.TrimSpace(ev.ThreadID)
			}
			if err := emitOutput(ctx, out, ev, &state); err != nil {
				return "", fmt.Errorf("%s: cli output: %w", c.name, err)
			}
		}
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("%s: read jsonl output: %w", c.name, err)
		}
		return state.threadID, nil
	case "json":
		data, err := readLimited(r, maxOutputBytes)
		if err != nil {
			return "", fmt.Errorf("%s: read json output: %w", c.name, err)
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return "", fmt.Errorf("%s: local cli returned empty stdout", c.name)
		}
		var ev output
		if err := json.Unmarshal(data, &ev); err != nil {
			return "", fmt.Errorf("%s: decode json output: %w", c.name, err)
		}
		return "", emitOutput(ctx, out, ev, nil)
	default:
		return "", c.readTextStdout(ctx, r, out)
	}
}

// readTextStdout streams text-protocol output line by line. Each non-empty
// line is sent as an individual ChunkText as soon as it arrives, so consumers
// never have to wait for the whole output before seeing incremental results.
func (c *client) readTextStdout(ctx context.Context, r io.Reader, out chan<- provider.Chunk) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64<<10), maxOutputBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		if err := emitText(ctx, out, line+"\n"); err != nil {
			return fmt.Errorf("%s: cli output: %w", c.name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("%s: read text output: %w", c.name, err)
	}
	return nil
}

func emitOutput(ctx context.Context, out chan<- provider.Chunk, ev output, state *jsonlState) error {
	if strings.TrimSpace(ev.Error) != "" || ev.Type == "error" {
		return errors.New(firstNonEmpty(ev.Error, ev.Message.text(), ev.Text, ev.Content, "local cli returned an error event"))
	}
	if ev.Usage != nil && !send(ctx, out, provider.Chunk{Type: provider.ChunkUsage, Usage: ev.Usage}) {
		return ctx.Err()
	}
	if ev.Item != nil {
		if err := emitItemOutput(ctx, out, ev.Item, state); err != nil {
			return err
		}
	}
	if strings.TrimSpace(ev.Reasoning) != "" && !send(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, Text: ev.Reasoning}) {
		return ctx.Err()
	}
	if text := ev.Message.text(); strings.TrimSpace(text) != "" {
		return emitText(ctx, out, text)
	}
	for _, choice := range ev.Choices {
		if choice.Delta != nil {
			if strings.TrimSpace(choice.Delta.ReasoningContent) != "" && !send(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, Text: choice.Delta.ReasoningContent}) {
				return ctx.Err()
			}
			if strings.TrimSpace(choice.Delta.Content) != "" {
				if err := emitText(ctx, out, choice.Delta.Content); err != nil {
					return err
				}
			}
		}
		if choice.Message != nil && strings.TrimSpace(choice.Message.Content) != "" {
			if err := emitText(ctx, out, choice.Message.Content); err != nil {
				return err
			}
		}
		if strings.TrimSpace(choice.Text) != "" {
			if err := emitText(ctx, out, choice.Text); err != nil {
				return err
			}
		}
	}
	if strings.TrimSpace(ev.Delta) != "" {
		return emitText(ctx, out, ev.Delta)
	}
	if strings.TrimSpace(ev.Content) != "" {
		return emitText(ctx, out, ev.Content)
	}
	if strings.TrimSpace(ev.Text) != "" {
		return emitText(ctx, out, ev.Text)
	}
	if ev.Done {
		return nil
	}
	return nil
}

func emitItemOutput(ctx context.Context, out chan<- provider.Chunk, item *itemOutput, state *jsonlState) error {
	var seenText map[string]string
	var seenReasoning map[string]string
	if state != nil {
		seenText = state.itemText
		seenReasoning = state.itemReasoning
	}
	text := firstNonEmpty(item.Delta, item.Text, item.Message.text(), rawContentText(item.Content))
	switch item.Type {
	case "agent_message", "assistant_message", "message":
		delta := itemDelta(item.ID, text, seenText)
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		return emitText(ctx, out, delta)
	case "reasoning":
		delta := itemDelta(item.ID, firstNonEmpty(item.Reasoning, text), seenReasoning)
		if strings.TrimSpace(delta) == "" {
			return nil
		}
		if !send(ctx, out, provider.Chunk{Type: provider.ChunkReasoning, Text: delta}) {
			return ctx.Err()
		}
		return nil
	default:
		return nil
	}
}

func itemDelta(id string, text string, seen map[string]string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	if id == "" || seen == nil {
		return text
	}
	prev := seen[id]
	seen[id] = text
	if strings.HasPrefix(text, prev) {
		return strings.TrimPrefix(text, prev)
	}
	return text
}

func rawContentText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var msg messageOutput
	if err := json.Unmarshal(raw, &msg); err == nil {
		if text := msg.text(); strings.TrimSpace(text) != "" {
			return text
		}
	}
	var parts []messageOutput
	if err := json.Unmarshal(raw, &parts); err == nil {
		var b strings.Builder
		for _, part := range parts {
			b.WriteString(part.text())
		}
		return b.String()
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func emitText(ctx context.Context, out chan<- provider.Chunk, text string) error {
	if !send(ctx, out, provider.Chunk{Type: provider.ChunkText, Text: text}) {
		return ctx.Err()
	}
	return nil
}

func send(ctx context.Context, out chan<- provider.Chunk, chunk provider.Chunk) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- chunk:
		return true
	}
}

func normalizeProtocol(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "json", "jsonl", "text":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return "text"
	}
}

// NormalizeInvocation repairs known local CLI invocation shapes without changing
// provider semantics. Codex exec defaults to final-only stdout; --json is the
// stdout event stream that WorkGround2 can parse incrementally.
func NormalizeInvocation(command string, args []string, protocol string) ([]string, string) {
	protocol = normalizeProtocol(protocol)
	if !isCodexCommand(command) || !isCodexExecArgs(args) {
		return args, protocol
	}
	nextArgs := append([]string(nil), args...)
	if !hasArg(nextArgs, "--json") && !hasArg(nextArgs, "--experimental-json") {
		nextArgs = append(nextArgs[:1], append([]string{"--json"}, nextArgs[1:]...)...)
	}
	return nextArgs, "jsonl"
}

func isCodexCommand(command string) bool {
	base := strings.TrimSpace(command)
	if idx := strings.LastIndexAny(base, `/\`); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.ToLower(base)
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".cmd")
	return base == "codex"
}

func isCodexExecArgs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(args[0]), "exec")
}

func hasArg(args []string, needle string) bool {
	for _, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), needle) {
			return true
		}
	}
	return false
}

func timeoutValue(raw any) time.Duration {
	seconds := 0
	switch v := raw.(type) {
	case int:
		seconds = v
	case int64:
		seconds = int(v)
	case float64:
		seconds = int(v)
	}
	if seconds <= 0 {
		return defaultTimeout
	}
	return time.Duration(seconds) * time.Second
}

func stringValue(raw any) string {
	if v, ok := raw.(string); ok {
		return v
	}
	return ""
}

func stringSlice(raw any) []string {
	switch v := raw.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	var buf bytes.Buffer
	n, err := io.Copy(&buf, io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if n > limit {
		return nil, fmt.Errorf("output exceeds %d bytes", limit)
	}
	return buf.Bytes(), nil
}

func boolValue(raw any) bool {
	v, _ := raw.(bool)
	return v
}

type limitedBuffer struct {
	buf bytes.Buffer
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remain := maxStderrBytes - b.buf.Len()
	if remain > 0 {
		if len(p) > remain {
			_, _ = b.buf.Write(p[:remain])
		} else {
			_, _ = b.buf.Write(p)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return strings.TrimSpace(b.buf.String())
}

func stderrSuffix(stderr string) string {
	if strings.TrimSpace(stderr) == "" {
		return ""
	}
	return ": " + strings.TrimSpace(stderr)
}

// reportCodexArtifacts scans $CODEX_HOME/generated_images/<threadID>/ for real
// image files produced by Codex CLI as a side effect and reports their absolute
// paths through the request-scoped ArtifactCollector attached to ctx (if any).
//
// Security: threadID is treated as untrusted input. The resolved directory must
// stay inside the generated_images root — a threadID containing path separators
// or ".." segments is rejected. Only regular, non-empty files are reported.
func reportCodexArtifacts(ctx context.Context, threadID string) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return
	}
	collector, ok := provider.ArtifactCollectorFrom(ctx)
	if !ok || collector == nil {
		return
	}
	root := provider.CodexGeneratedImagesRoot()
	if root == "" {
		return
	}
	// Reject thread IDs that attempt path traversal — the directory must be a
	// direct child of the generated_images root.
	if strings.ContainsAny(threadID, `/\`) || threadID == "." || threadID == ".." || strings.Contains(threadID, "..") {
		return
	}
	dir := filepath.Join(root, threadID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			continue
		}
		abs := filepath.Join(dir, entry.Name())
		// Final boundary check: the cleaned absolute path must be inside dir.
		if !pathWithinRoot(abs, dir) {
			continue
		}
		collector.AddArtifact(abs)
	}
}

// pathWithinRoot reports whether path is inside root after cleaning both.
func pathWithinRoot(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
