// Package dshcompat runs DSH Bundle plugins in a session-owned Node/Cordis
// sidecar and adapts their Tool registry to WorkGround2 tools.
package dshcompat

import (
	"bufio"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"workground2/internal/tool"
)

const (
	protocolVersion       = 1
	maxBridgeMessageBytes = 16 << 20
	closeTimeout          = 5 * time.Second
)

//go:embed bridge.mjs
var bridgeScript []byte

// Spec identifies one DSH Bundle runtime owned by a WG2 Controller.
type Spec struct {
	Name              string
	BundlePackageJSON string
	RuntimeAnchor     string
	Workspace         string
	DSHHome           string
	RuntimeDir        string
	NodePath          string
	Stderr            io.Writer
}

// HostInfo is returned by the DSH bridge after Cordis and the per-session
// Agent have settled.
type HostInfo struct {
	Protocol      int          `json:"protocol"`
	Layers        []Layer      `json:"layers"`
	Workspace     string       `json:"workspace"`
	RuntimeAnchor string       `json:"runtimeAnchor"`
	Tools         []ToolSchema `json:"tools"`
}

type Layer struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type ToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type bridgeRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type bridgeResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *bridgeError    `json:"error,omitempty"`
}

type bridgeError struct {
	Message string        `json:"message"`
	Name    string        `json:"name,omitempty"`
	Stack   string        `json:"stack,omitempty"`
	Cause   *bridgeError  `json:"cause,omitempty"`
	Errors  []bridgeError `json:"errors,omitempty"`
}

func (e *bridgeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Cause != nil && e.Cause.Message != "" && e.Cause.Message != e.Message {
		return e.Message + ": " + e.Cause.Message
	}
	return e.Message
}

type pendingResponse struct {
	result json.RawMessage
	err    error
}

// Client owns one Node sidecar and its in-flight request table.
type Client struct {
	name   string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	writeMu sync.Mutex
	mu      sync.Mutex
	pending map[int64]chan pendingResponse
	nextID  atomic.Int64
	done    chan struct{}
	waitErr error
	closed  bool
	info    HostInfo
}

// Start materializes the versioned bridge, starts Node, boots the Bundle, and
// returns only after DSH reports its settled Tool schemas.
func Start(ctx context.Context, spec Spec) (*Client, error) {
	if err := validateSpec(&spec); err != nil {
		return nil, err
	}
	script, err := materializeBridge(spec.RuntimeDir)
	if err != nil {
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, spec.NodePath, script)
	cmd.Dir = spec.Workspace
	if spec.Stderr == nil {
		spec.Stderr = os.Stderr
	}
	cmd.Stderr = spec.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start DSH Node sidecar: %w", err)
	}
	c := &Client{
		name:    spec.Name,
		cmd:     cmd,
		stdin:   stdin,
		cancel:  cancel,
		pending: make(map[int64]chan pendingResponse),
		done:    make(chan struct{}),
	}
	go c.readLoop(stdout)
	go c.waitLoop()

	var info HostInfo
	err = c.call(ctx, "initialize", map[string]any{
		"workspace":         spec.Workspace,
		"runtimeAnchor":     spec.RuntimeAnchor,
		"bundlePackageJSON": spec.BundlePackageJSON,
		"dshHome":           spec.DSHHome,
	}, &info)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("initialize DSH bundle %s: %w", spec.Name, err)
	}
	if info.Protocol != protocolVersion {
		_ = c.Close()
		return nil, fmt.Errorf("DSH bridge protocol %d is unsupported (want %d)", info.Protocol, protocolVersion)
	}
	c.info = info
	return c, nil
}

func validateSpec(spec *Spec) error {
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.Name == "" {
		return errors.New("DSH bundle name is required")
	}
	for label, value := range map[string]*string{
		"bundle package.json": &spec.BundlePackageJSON,
		"runtime anchor":      &spec.RuntimeAnchor,
		"workspace":           &spec.Workspace,
		"DSH home":            &spec.DSHHome,
		"runtime directory":   &spec.RuntimeDir,
	} {
		*value = filepath.Clean(strings.TrimSpace(*value))
		if *value == "." || *value == "" || !filepath.IsAbs(*value) {
			return fmt.Errorf("%s must be an absolute path", label)
		}
	}
	if spec.NodePath == "" {
		node, err := exec.LookPath("node")
		if err != nil {
			return errors.New("Node.js was not found on PATH")
		}
		spec.NodePath = node
	}
	for label, path := range map[string]string{
		"bundle package.json": spec.BundlePackageJSON,
		"runtime anchor":      spec.RuntimeAnchor,
	} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = errors.New("not a regular file")
			}
			return fmt.Errorf("%s %s: %w", label, path, err)
		}
	}
	if info, err := os.Stat(spec.Workspace); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("not a directory")
		}
		return fmt.Errorf("workspace %s: %w", spec.Workspace, err)
	}
	return nil
}

func materializeBridge(runtimeDir string) (string, error) {
	sum := sha256.Sum256(bridgeScript)
	name := "bridge-" + hex.EncodeToString(sum[:8]) + ".mjs"
	target := filepath.Join(runtimeDir, name)
	if existing, err := os.ReadFile(target); err == nil {
		if string(existing) != string(bridgeScript) {
			return "", fmt.Errorf("DSH bridge checksum collision at %s", target)
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(runtimeDir, ".bridge-*.tmp")
	if err != nil {
		return "", err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		_ = tmp.Close()
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(bridgeScript); err != nil {
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, target); err != nil {
		if existing, readErr := os.ReadFile(target); readErr == nil && string(existing) == string(bridgeScript) {
			return target, nil
		}
		return "", err
	}
	removeTemp = false
	return target, nil
}

func (c *Client) readLoop(stdout io.Reader) {
	reader := bufio.NewReader(stdout)
	for {
		line, err := readBoundedLine(reader, maxBridgeMessageBytes)
		if len(line) > 0 {
			var response bridgeResponse
			if decodeErr := json.Unmarshal(line, &response); decodeErr != nil {
				c.fail(fmt.Errorf("decode DSH bridge response: %w", decodeErr))
				return
			}
			c.deliver(response)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				c.fail(fmt.Errorf("read DSH bridge: %w", err))
			} else {
				c.fail(errors.New("DSH bridge closed stdout"))
			}
			return
		}
	}
}

func readBoundedLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(line)+len(part) > limit {
			return nil, fmt.Errorf("DSH bridge message exceeds %d bytes", limit)
		}
		line = append(line, part...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}

func (c *Client) waitLoop() {
	err := c.cmd.Wait()
	if err == nil {
		err = errors.New("DSH bridge exited")
	} else {
		err = fmt.Errorf("DSH bridge exited: %w", err)
	}
	c.mu.Lock()
	if c.waitErr == nil {
		c.waitErr = err
	}
	c.mu.Unlock()
	c.fail(err)
}

func (c *Client) deliver(response bridgeResponse) {
	c.mu.Lock()
	ch := c.pending[response.ID]
	delete(c.pending, response.ID)
	c.mu.Unlock()
	if ch == nil {
		return
	}
	if response.Error != nil {
		ch <- pendingResponse{err: response.Error}
	} else {
		ch <- pendingResponse{result: response.Result}
	}
}

func (c *Client) fail(err error) {
	c.mu.Lock()
	if c.waitErr == nil {
		c.waitErr = err
	}
	pending := c.pending
	c.pending = make(map[int64]chan pendingResponse)
	select {
	case <-c.done:
	default:
		close(c.done)
	}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- pendingResponse{err: err}
	}
}

func (c *Client) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan pendingResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("DSH bridge is closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(bridgeRequest{ID: id, Method: method, Params: params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case response := <-ch:
		if response.err != nil {
			return response.err
		}
		if out == nil || len(response.result) == 0 || string(response.result) == "null" {
			return nil
		}
		return json.Unmarshal(response.result, out)
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = c.write(bridgeRequest{ID: c.nextID.Add(1), Method: "cancel", Params: map[string]any{"id": id}})
		return ctx.Err()
	case <-c.done:
		c.mu.Lock()
		err := c.waitErr
		c.mu.Unlock()
		if err == nil {
			err = errors.New("DSH bridge stopped")
		}
		return err
	}
}

func (c *Client) write(request bridgeRequest) error {
	b, err := json.Marshal(request)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(b)
	if err != nil {
		return fmt.Errorf("write DSH bridge request: %w", err)
	}
	return nil
}

// Info returns a detached startup snapshot.
func (c *Client) Info() HostInfo {
	info := c.info
	info.Layers = append([]Layer(nil), info.Layers...)
	info.Tools = append([]ToolSchema(nil), info.Tools...)
	return info
}

// Tools adapts every DSH schema under a collision-safe package namespace.
func (c *Client) Tools() []tool.Tool {
	tag := modelNamePart(c.name)
	out := make([]tool.Tool, 0, len(c.info.Tools))
	for _, schema := range c.info.Tools {
		if strings.TrimSpace(schema.Name) == "" {
			continue
		}
		if len(schema.Parameters) == 0 {
			schema.Parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, &bridgedTool{
			client: c,
			name:   "dsh__" + tag + "__" + modelNamePart(schema.Name),
			raw:    schema.Name,
			desc:   schema.Description,
			schema: append(json.RawMessage(nil), schema.Parameters...),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Close is idempotent and waits briefly for Cordis effect disposal before
// force-cancelling the process context.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	var response map[string]any
	// Close marked the client closed to reject new calls; issue shutdown through
	// the wire directly and then wait for process exit.
	id := c.nextID.Add(1)
	ch := make(chan pendingResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	requestErr := c.write(bridgeRequest{ID: id, Method: "shutdown"})
	if requestErr == nil {
		select {
		case settled := <-ch:
			requestErr = settled.err
			if requestErr == nil && len(settled.result) > 0 {
				_ = json.Unmarshal(settled.result, &response)
			}
		case <-ctx.Done():
			requestErr = ctx.Err()
		case <-c.done:
		}
	}
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-ctx.Done():
		c.cancel()
		<-c.done
	}
	c.cancel()
	return requestErr
}

type bridgedTool struct {
	client *Client
	name   string
	raw    string
	desc   string
	schema json.RawMessage
}

func (t *bridgedTool) Name() string            { return t.name }
func (t *bridgedTool) Description() string     { return "DSH: " + t.desc }
func (t *bridgedTool) Schema() json.RawMessage { return t.schema }
func (t *bridgedTool) ReadOnly() bool          { return false }
func (t *bridgedTool) PlanModeSafe() bool      { return false }

func (t *bridgedTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var arguments any = map[string]any{}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &arguments); err != nil {
			return "", err
		}
	}
	var result struct {
		IsError bool `json:"isError"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error,omitempty"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text,omitempty"`
		} `json:"content"`
		Value any `json:"value,omitempty"`
		Meta  any `json:"meta,omitempty"`
	}
	err := t.client.call(ctx, "tools/call", map[string]any{
		"callId":    fmt.Sprintf("wg2-%d", time.Now().UTC().UnixNano()),
		"name":      t.raw,
		"arguments": arguments,
	}, &result)
	if err != nil {
		return "", err
	}
	var texts []string
	allText := true
	for _, block := range result.Content {
		if block.Type != "text" {
			allText = false
			break
		}
		texts = append(texts, block.Text)
	}
	output := strings.Join(texts, "\n")
	if !allText || output == "" {
		value := result.Value
		if value == nil {
			value = result.Content
		}
		b, marshalErr := json.Marshal(value)
		if marshalErr != nil {
			return "", marshalErr
		}
		output = string(b)
	}
	if result.IsError {
		if result.Error != nil && result.Error.Message != "" {
			return "", errors.New(result.Error.Message)
		}
		return "", errors.New(output)
	}
	return output, nil
}

func modelNamePart(value string) string {
	value = strings.TrimSpace(value)
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		valid := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
		if valid && r <= unicode.MaxASCII {
			b.WriteRune(r)
			lastUnderscore = r == '_'
		} else if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "plugin"
	}
	return out
}
