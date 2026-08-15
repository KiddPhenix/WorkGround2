package dshcompat

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var webReadyLine = regexp.MustCompile(`^dsh web: (http://127\.0\.0\.1:[0-9]+)(?:\s|$)`)

// WebSpec describes an isolated DSH browser surface used as the high-fidelity
// UI fallback for client plugins that cannot render inside WG2's React tree.
type WebSpec struct {
	RuntimeAnchor string
	BundlePatch   string
	BundleName    string
	Workspace     string
	DSHHome       string
	NodePath      string
	Stderr        io.Writer
}

// WebHost owns one loopback-only DSH web process.
type WebHost struct {
	url    string
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

// StartWeb launches dsh web on an OS-assigned loopback port and waits until
// both DSH's readiness line and its HTTP root are reachable.
func StartWeb(ctx context.Context, spec WebSpec) (*WebHost, error) {
	bin, err := validateWebSpec(&spec)
	if err != nil {
		return nil, err
	}
	procCtx, cancel := context.WithCancel(context.Background())
	args := []string{bin, "web"}
	if spec.BundlePatch != "" && spec.BundleName != "@deepseek-ai/dsh-base" && spec.BundleName != "@deepseek-ai/dsh-web-app" {
		args = append(args, "--patch", spec.BundlePatch)
	}
	args = append(args, "--host", "127.0.0.1", "--port", "0")
	cmd := exec.CommandContext(procCtx, spec.NodePath, args...)
	cmd.Dir = spec.Workspace
	cmd.Env = append(os.Environ(), "DSH_HOME="+spec.DSHHome, "DSH_TELEMETRY_DISABLED=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start DSH web mirror: %w", err)
	}
	host := &WebHost{cancel: cancel, done: make(chan struct{})}
	ready := make(chan string, 1)
	go scanWebOutput(stdout, ready, spec.Stderr)
	go copyWebOutput(stderr, spec.Stderr)
	go func() {
		err := cmd.Wait()
		host.mu.Lock()
		if err != nil && !errors.Is(procCtx.Err(), context.Canceled) {
			host.err = err
		}
		host.mu.Unlock()
		close(host.done)
	}()

	for {
		select {
		case address, ok := <-ready:
			if !ok {
				host.Close()
				return nil, errors.New("DSH web mirror exited before readiness")
			}
			host.url = address
			if err := awaitWebHTTP(ctx, host.url); err != nil {
				host.Close()
				return nil, err
			}
			return host, nil
		case <-host.done:
			return nil, errors.New("DSH web mirror exited before readiness")
		case <-ctx.Done():
			host.Close()
			return nil, ctx.Err()
		}
	}
}

func validateWebSpec(spec *WebSpec) (string, error) {
	for label, value := range map[string]*string{
		"runtime anchor": &spec.RuntimeAnchor,
		"workspace":      &spec.Workspace,
		"DSH home":       &spec.DSHHome,
	} {
		*value = filepath.Clean(strings.TrimSpace(*value))
		if *value == "." || *value == "" || !filepath.IsAbs(*value) {
			return "", fmt.Errorf("%s must be an absolute path", label)
		}
	}
	if spec.BundlePatch != "" {
		spec.BundlePatch = filepath.Clean(spec.BundlePatch)
		if !filepath.IsAbs(spec.BundlePatch) {
			return "", errors.New("bundle patch must be an absolute path")
		}
		if info, err := os.Stat(spec.BundlePatch); err != nil || !info.Mode().IsRegular() {
			if err == nil {
				err = errors.New("not a regular file")
			}
			return "", fmt.Errorf("bundle patch %s: %w", spec.BundlePatch, err)
		}
	}
	if spec.NodePath == "" {
		node, err := exec.LookPath("node")
		if err != nil {
			return "", errors.New("Node.js was not found on PATH")
		}
		spec.NodePath = node
	}
	b, err := os.ReadFile(spec.RuntimeAnchor)
	if err != nil {
		return "", err
	}
	var manifest struct {
		Bin any `json:"bin"`
	}
	if err := json.Unmarshal(b, &manifest); err != nil {
		return "", err
	}
	binRel := ""
	switch value := manifest.Bin.(type) {
	case string:
		binRel = value
	case map[string]any:
		binRel, _ = value["dsh"].(string)
	}
	if strings.TrimSpace(binRel) == "" {
		return "", fmt.Errorf("DSH runtime %s declares no dsh executable", spec.RuntimeAnchor)
	}
	root := filepath.Dir(spec.RuntimeAnchor)
	bin := filepath.Clean(filepath.Join(root, filepath.FromSlash(binRel)))
	rel, err := filepath.Rel(root, bin)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("DSH executable escapes its package root")
	}
	if info, err := os.Stat(bin); err != nil || !info.Mode().IsRegular() {
		if err == nil {
			err = errors.New("not a regular file")
		}
		return "", fmt.Errorf("DSH executable %s: %w", bin, err)
	}
	if err := os.MkdirAll(spec.DSHHome, 0o755); err != nil {
		return "", err
	}
	return bin, nil
}

func scanWebOutput(reader io.Reader, ready chan<- string, mirror io.Writer) {
	defer close(ready)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if mirror != nil {
			_, _ = fmt.Fprintln(mirror, line)
		}
		if match := webReadyLine.FindStringSubmatch(strings.TrimSpace(line)); len(match) == 2 {
			select {
			case ready <- match[1]:
			default:
			}
		}
	}
}

func copyWebOutput(reader io.Reader, mirror io.Writer) {
	if mirror == nil {
		mirror = io.Discard
	}
	_, _ = io.Copy(mirror, reader)
}

func awaitWebHTTP(ctx context.Context, address string) error {
	parsed, err := url.Parse(address)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" {
		return fmt.Errorf("DSH web mirror returned unsafe URL %q", address)
	}
	client := &http.Client{Timeout: time.Second}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
		response, requestErr := client.Do(req)
		if requestErr == nil {
			_ = response.Body.Close()
			if response.StatusCode >= 200 && response.StatusCode < 500 {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for DSH web mirror: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (h *WebHost) URL() string { return h.url }

func (h *WebHost) Done() <-chan struct{} { return h.done }

func (h *WebHost) Err() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.err
}

// Close is safe to call repeatedly.
func (h *WebHost) Close() {
	if h == nil {
		return
	}
	h.cancel()
	<-h.done
}
