package dsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/runhub"
)

// lineBuffer is a buffered, blocking line queue used as the fake peer's stdin,
// so the runner's writes never block even when the test is not currently
// reading (mirroring an OS pipe's buffer).
type lineBuffer struct {
	mu     sync.Mutex
	cond   *sync.Cond
	buf    []byte
	closed bool
}

func newLineBuffer() *lineBuffer {
	lb := &lineBuffer{}
	lb.cond = sync.NewCond(&lb.mu)
	return lb
}

func (lb *lineBuffer) Write(p []byte) (int, error) {
	lb.mu.Lock()
	lb.buf = append(lb.buf, p...)
	lb.mu.Unlock()
	lb.cond.Broadcast()
	return len(p), nil
}

func (lb *lineBuffer) Close() error {
	lb.mu.Lock()
	lb.closed = true
	lb.mu.Unlock()
	lb.cond.Broadcast()
	return nil
}

func (lb *lineBuffer) ReadLine() ([]byte, error) {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	for {
		if i := bytes.IndexByte(lb.buf, '\n'); i >= 0 {
			line := lb.buf[:i+1]
			lb.buf = lb.buf[i+1:]
			return line, nil
		}
		if lb.closed {
			if len(lb.buf) > 0 {
				line := lb.buf
				lb.buf = nil
				return line, nil
			}
			return nil, io.EOF
		}
		lb.cond.Wait()
	}
}

// fakePeer is an interactive in-memory JSON-RPC peer. The runner writes to
// Stdin() and reads Stdout(); the test drives the other direction by reading
// requests and writing notifications/responses, so ordering and timing are fully
// deterministic.
type fakePeer struct {
	stdin   *lineBuffer
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter

	stderrMu sync.Mutex
	stderr   strings.Builder

	mu          sync.Mutex
	killed      bool
	cleaned     bool
	stdinClosed bool

	waitCh   chan error
	exitedCh chan struct{}
	waitOnce sync.Once
}

func newFakePeer() *fakePeer {
	stdoutR, stdoutW := io.Pipe()
	return &fakePeer{
		stdin:    newLineBuffer(),
		stdoutR:  stdoutR,
		stdoutW:  stdoutW,
		waitCh:   make(chan error, 1),
		exitedCh: make(chan struct{}),
	}
}

func (p *fakePeer) Stdin() io.WriteCloser   { return p.stdin }
func (p *fakePeer) Stdout() io.Reader       { return p.stdoutR }
func (p *fakePeer) Ref() string             { return "fake" }
func (p *fakePeer) Wait() <-chan error      { return p.waitCh }
func (p *fakePeer) Exited() <-chan struct{} { return p.exitedCh }

func (p *fakePeer) StderrTail(maxBytes int) string {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	s := p.stderr.String()
	if maxBytes > 0 && len(s) > maxBytes {
		s = s[len(s)-maxBytes:]
	}
	return sanitizeDiagnostic(s)
}

func (p *fakePeer) CloseStdin() error {
	p.mu.Lock()
	p.stdinClosed = true
	p.mu.Unlock()
	return p.stdin.Close()
}

func (p *fakePeer) Kill() {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	p.signalExit(errors.New("dsh: killed"))
}

func (p *fakePeer) Cleanup() {
	p.mu.Lock()
	p.cleaned = true
	p.mu.Unlock()
}

func (p *fakePeer) signalExit(err error) {
	p.waitOnce.Do(func() {
		_ = p.stdoutW.Close()
		close(p.exitedCh)
		p.waitCh <- err
		close(p.waitCh)
	})
}

func (p *fakePeer) killedFlag() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

func (p *fakePeer) cleanedFlag() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cleaned
}

func (p *fakePeer) stdinClosedFlag() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stdinClosed
}

func (p *fakePeer) writeStderr(s string) {
	p.stderrMu.Lock()
	defer p.stderrMu.Unlock()
	p.stderr.WriteString(s)
}

// readRequest reads the next request the runner sent to the peer's stdin.
func (p *fakePeer) readRequest(t *testing.T) Frame {
	t.Helper()
	line, err := p.stdin.ReadLine()
	if err != nil {
		t.Fatalf("readRequest: %v", err)
	}
	f, err := DecodeFrame(line)
	if err != nil {
		t.Fatalf("readRequest: decode: %v", err)
	}
	return f
}

// send writes one raw JSON line the runner will read as a frame.
func (p *fakePeer) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(p.stdoutW, line+"\n"); err != nil {
		t.Fatalf("send: %v", err)
	}
}

// respond writes a success response for request f with the given result JSON.
func (p *fakePeer) respond(t *testing.T, f Frame, result string) {
	t.Helper()
	p.send(t, `{"jsonrpc":"2.0","id":`+string(f.ID)+`,"result":`+result+`}`)
}

func sessEventLine(sessionID, typ string, seq int64, data string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":%q,"event":{"type":%q,"seq":%d,"data":%s}}}`, sessionID, typ, seq, data)
}

func statusLine(sessionID, status string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","method":"session.status","params":{"sessionId":%q,"status":%q}}`, sessionID, status)
}

const serverInfoJSON = `{"serverInfo":{"name":"deepseek-harness-sdk-runtime","version":"0.0.1"}}`

func writeTestFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// newTestRunner builds a hub/store/runner over a fresh directory with a fake
// launcher, probes against real temp files, and launches one run.
func newTestRunner(t *testing.T, launch Launcher) (*Runner, *runhub.Hub, *runhub.Store, runhub.RunID) {
	t.Helper()
	dir := t.TempDir()
	store, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	probeDir := t.TempDir()
	cfg := RunnerConfig{
		Probe: Config{
			NodePath:             writeTestFile(t, probeDir, "node", ""),
			EntryPath:            writeTestFile(t, probeDir, "entry.js", ""),
			ConfigPath:           writeTestFile(t, probeDir, "dsh.json", "{}"),
			VersionPath:          writeTestFile(t, probeDir, "package.json", `{"version":"0.1.0-rc.8"}`),
			RequiredVersion:      "0.1.0-rc.8",
			RequiredCapabilities: []Capability{CapInitialize, CapPrompt, CapShutdown},
		},
		Provider: "deepseek-official",
		Model:    "deepseek-chat",
		Timeouts: Timeouts{Initialize: time.Second, Prompt: time.Second, Shutdown: 100 * time.Millisecond, KillGrace: 100 * time.Millisecond},
		Launcher: launch,
	}
	r := NewRunner(cfg, store)

	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	if rec, run := hub.Launch(intent); rec.Status != runhub.ReceiptAccepted {
		t.Fatalf("launch status = %s", rec.Status)
	} else {
		return r, hub, store, run.ID
	}
	return nil, nil, nil, ""
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

func waitForState(t *testing.T, hub *runhub.Hub, id runhub.RunID, state runhub.RunState) {
	t.Helper()
	waitFor(t, 2*time.Second, func() bool {
		r, ok := hub.Get(id)
		return ok && r.State == state
	})
}

// startPeer starts a run against the peer, completing initialize + prompt, and
// returns the Start result and the resolved session id.
type startResult struct {
	binding   runhub.RunnerBinding
	err       error
	sessionID string
}

func startPeer(t *testing.T, r *Runner, sink runhub.EventSink, peer *fakePeer) startResult {
	t.Helper()
	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	resCh := make(chan startResult, 1)
	go func() {
		b, err := r.Start(context.Background(), runhub.LaunchRequest{LaunchIntent: intent}, sink)
		resCh <- startResult{binding: b, err: err}
	}()

	initReq := peer.readRequest(t)
	if initReq.Method != MethodInitialize {
		t.Fatalf("first request method = %s, want initialize", initReq.Method)
	}
	peer.respond(t, initReq, serverInfoJSON)

	promptReq := peer.readRequest(t)
	if promptReq.Method != MethodPrompt {
		t.Fatalf("second request method = %s, want session/prompt", promptReq.Method)
	}
	peer.respond(t, promptReq, `{"messageId":"msg-1"}`)

	res := <-resCh
	if res.err != nil {
		t.Fatalf("Start: %v", res.err)
	}
	res.sessionID = res.binding.NativeSessionID
	return res
}

func TestRunnerSuccess(t *testing.T) {
	peer := newFakePeer()
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 1, `{"inserted":[{"id":"msg-1"}]}`))
	waitForState(t, hub, runID, runhub.StateRunning)
	peer.send(t, sessEventLine(res.sessionID, evTurnStart, 2, `{"turn":1}`))
	waitFor(t, 2*time.Second, func() bool {
		got, _ := hub.Get(runID)
		return got.Activity == runhub.ActivityThinking
	})
	peer.send(t, sessEventLine(res.sessionID, evToolCall, 3, `{"turn":1,"step":1,"callId":"c1","name":"read","arguments":"secret"}`))
	waitFor(t, 2*time.Second, func() bool {
		got, _ := hub.Get(runID)
		return got.ActivityLabel == "read"
	})
	peer.send(t, sessEventLine(res.sessionID, evToolResult, 4, `{"turn":1,"step":1,"message":{"content":[{"type":"text","text":"secret result"}]}}`))
	peer.send(t, sessEventLine(res.sessionID, evAssistantMessage, 5, `{"turn":1,"step":1,"message":{"content":[{"type":"text","text":"the answer"}]}}`))
	peer.send(t, sessEventLine(res.sessionID, evTurnEnd, 6, `{"turn":1,"reason":{"kind":"completed"}}`))
	peer.send(t, statusLine(res.sessionID, "idle"))

	waitForState(t, hub, runID, runhub.StateSucceeded)
	got, _ := hub.Get(runID)
	if got.Summary != "the answer" {
		t.Fatalf("summary = %q, want %q", got.Summary, "the answer")
	}
	if got.ActivityLabel != "" {
		t.Fatalf("terminal activity label = %q, want empty", got.ActivityLabel)
	}

	// The binding must be durable and the process must have been reaped.
	rec, ok, err := store.LoadBinding(runID)
	if err != nil || !ok {
		t.Fatalf("LoadBinding: ok=%v err=%v", ok, err)
	}
	if rec.Binding.NativeSessionID != res.sessionID || rec.Binding.RunID != runID {
		t.Fatalf("binding = %+v", rec.Binding)
	}
	waitFor(t, 2*time.Second, func() bool { return peer.killedFlag() })

	// No transcript/tool-argument leakage into persisted events.
	logged, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%+v", logged)
	for _, leak := range []string{"secret", "arguments"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("persisted events leak %q: %+v", leak, logged)
		}
	}
}

func TestRunnerReplaysNotificationsBeforePromptResponse(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })

	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	resCh := make(chan startResult, 1)
	go func() {
		b, err := r.Start(context.Background(), runhub.LaunchRequest{LaunchIntent: intent}, hub)
		resCh <- startResult{binding: b, err: err}
	}()

	initReq := peer.readRequest(t)
	peer.respond(t, initReq, serverInfoJSON)
	promptReq := peer.readRequest(t)
	sessionID := sessionIDFor(runID)

	// DSH may emit lifecycle notifications before writing the prompt response.
	// No later frame is sent after the response, so SetHandler itself must wake
	// and replay the complete buffered interval.
	peer.send(t, sessEventLine(sessionID, evInboxSpliced, 1, `{"inserted":[{"id":"msg-1"}]}`))
	peer.send(t, sessEventLine(sessionID, evTurnEnd, 2, `{"turn":1,"reason":{"kind":"completed"}}`))
	peer.send(t, statusLine(sessionID, "idle"))
	peer.respond(t, promptReq, `{"messageId":"msg-1"}`)

	if res := <-resCh; res.err != nil {
		t.Fatalf("Start: %v", res.err)
	}
	waitForState(t, hub, runID, runhub.StateSucceeded)
}

func TestRunnerErrorResponseSettlesFailed(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })

	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	resCh := make(chan error, 1)
	go func() {
		_, err := r.Start(context.Background(), runhub.LaunchRequest{LaunchIntent: intent}, hub)
		resCh <- err
	}()

	initReq := peer.readRequest(t)
	peer.send(t, `{"jsonrpc":"2.0","id":`+string(initReq.ID)+`,"error":{"code":-32601,"message":"no such method"}}`)

	if err := <-resCh; err == nil {
		t.Fatalf("Start succeeded, want error response failure")
	}
	waitForState(t, hub, runID, runhub.StateFailed)
}

func TestRunnerBadFrameSettlesFailed(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	startPeer(t, r, hub, peer)

	peer.send(t, `{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"x","event":{not-json}`)
	waitForState(t, hub, runID, runhub.StateFailed)
	waitFor(t, 2*time.Second, func() bool { return peer.killedFlag() })
}

func TestRunnerCrashSettlesFailed(t *testing.T) {
	peer := newFakePeer()
	peer.writeStderr("stderr noise\n")
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	startPeer(t, r, hub, peer)

	peer.signalExit(errors.New("boom"))
	waitForState(t, hub, runID, runhub.StateFailed)
	got, _ := hub.Get(runID)
	if got.Summary != "" {
		t.Fatalf("crash summary = %q, want empty", got.Summary)
	}
}

func TestRunnerCancelEscalatesAndSettlesCancelled(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	// Cancel quiesces synchronously: run it in a goroutine and drive the ladder.
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- r.Cancel(context.Background(), res.binding) }()

	// The teardown ladder first sends shutdown, then closes stdin, then kills the
	// tree; the peer never exits on its own, so the kill path is exercised.
	shutdownReq := peer.readRequest(t)
	if shutdownReq.Method != MethodShutdown {
		t.Fatalf("teardown request method = %s, want shutdown", shutdownReq.Method)
	}
	peer.respond(t, shutdownReq, `{}`)
	waitFor(t, 2*time.Second, func() bool { return peer.stdinClosedFlag() })
	waitFor(t, 2*time.Second, func() bool { return peer.killedFlag() })

	if err := <-cancelDone; err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	waitForState(t, hub, runID, runhub.StateCancelled)
}

func TestRunnerCancelIdempotent(t *testing.T) {
	peer := newFakePeer()
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	if err := r.Cancel(context.Background(), res.binding); err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
	if err := r.Cancel(context.Background(), res.binding); err != nil {
		t.Fatalf("second Cancel: %v", err)
	}
	waitForState(t, hub, runID, runhub.StateCancelled)

	// Exactly one terminal event must be durable, despite two cancels.
	events, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	terminals := 0
	for _, ev := range events {
		if ev.Type == runhub.EventCancelled {
			terminals++
		}
	}
	if terminals != 1 {
		t.Fatalf("terminal cancelled events = %d, want 1", terminals)
	}
}

func TestRunnerIgnoresWrongSessionAndMessage(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	peer.send(t, statusLine("other-session", "idle"))
	peer.send(t, sessEventLine("other-session", evTurnStart, 1, `{"turn":1}`))
	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 2, `{"inserted":[{"id":"msg-OTHER"}]}`))

	if got, _ := hub.Get(runID); got.State == runhub.StateRunning {
		t.Fatalf("wrong session/message marked run running")
	}

	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 3, `{"inserted":[{"id":"msg-1"}]}`))
	waitForState(t, hub, runID, runhub.StateRunning)
}

func TestRunnerDuplicateStartNoSecondProcess(t *testing.T) {
	var launches int32
	peer := newFakePeer()
	r, hub, _, _ := newTestRunner(t, func(ProcessSpec) (Proc, error) {
		atomic.AddInt32(&launches, 1)
		return peer, nil
	})
	res := startPeer(t, r, hub, peer)

	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	b2, err := r.Start(context.Background(), runhub.LaunchRequest{LaunchIntent: intent}, hub)
	if err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if b2.NativeSessionID != res.sessionID {
		t.Fatalf("second binding session = %q, want %q", b2.NativeSessionID, res.sessionID)
	}
	if n := atomic.LoadInt32(&launches); n != 1 {
		t.Fatalf("launched %d processes, want 1", n)
	}
}

func TestRunnerRestartRecoveryRefusesStart(t *testing.T) {
	dir := t.TempDir()

	// A previous owner left a running run plus a persisted binding, then died.
	s1, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h1, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, run := h1.Launch(runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH})
	h1.Report(runhub.RunEvent{EventID: "evt-run", RunID: run.ID, Type: runhub.EventRunning})
	if err := s1.SaveBinding(runhub.BindingRecord{
		RunID:   run.ID,
		Binding: runhub.RunnerBinding{RunID: run.ID, NativeSessionID: "wg2-" + string(run.ID), ProtocolVersion: "2.0", ProcessRef: "fake", Attempt: 1},
		State:   runhub.StateRunning,
		SavedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	// A new owner over the same directory settles the orphan as interrupted.
	h2, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := h2.RecoverBindings(); err != nil || n != 1 {
		t.Fatalf("RecoverBindings = (%d, %v), want (1, nil)", n, err)
	}
	if got, _ := h2.Get(run.ID); got.State != runhub.StateInterrupted {
		t.Fatalf("orphan state = %s, want interrupted", got.State)
	}

	// A fresh runner must not silently restart the interrupted run.
	s2, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	probeDir := t.TempDir()
	r2 := NewRunner(RunnerConfig{
		Probe: Config{
			NodePath:    writeTestFile(t, probeDir, "node", ""),
			EntryPath:   writeTestFile(t, probeDir, "entry.js", ""),
			ConfigPath:  writeTestFile(t, probeDir, "dsh.json", "{}"),
			VersionPath: writeTestFile(t, probeDir, "package.json", `{"version":"0.1.0-rc.8"}`),
		},
		Provider: "deepseek-official",
		Model:    "deepseek-chat",
		Timeouts: Timeouts{Initialize: time.Second, Prompt: time.Second, Shutdown: 100 * time.Millisecond, KillGrace: 100 * time.Millisecond},
	}, s2)

	_, err = r2.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"},
	}, h2)
	if !errors.Is(err, ErrAlreadySettled) {
		t.Fatalf("Start on interrupted run = %v, want ErrAlreadySettled", err)
	}
}

func TestProbeDeclaresCancelOnly(t *testing.T) {
	probeDir := t.TempDir()
	r := NewRunner(RunnerConfig{
		Probe: Config{
			NodePath:    writeTestFile(t, probeDir, "node", ""),
			EntryPath:   writeTestFile(t, probeDir, "entry.js", ""),
			ConfigPath:  writeTestFile(t, probeDir, "dsh.json", "{}"),
			VersionPath: writeTestFile(t, probeDir, "package.json", `{"version":"0.1.0-rc.8"}`),
		},
	}, nil)
	caps, err := r.Probe(context.Background(), runhub.Profile{})
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !caps.Cancel || caps.Open || caps.Resume || caps.Approve || caps.Send || caps.Retry {
		t.Fatalf("capabilities = %+v, want only cancel", caps)
	}
	if err := r.Open(context.Background(), runhub.RunnerBinding{}); !errors.Is(err, runhub.ErrUnsupported) {
		t.Fatalf("Open = %v, want ErrUnsupported", err)
	}
}

func newStoreAndHub(t *testing.T) (*runhub.Store, *runhub.Hub) {
	t.Helper()
	dir := t.TempDir()
	store, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store, hub
}

func quickTimeouts() Timeouts {
	return Timeouts{Initialize: time.Second, Prompt: time.Second, Shutdown: 50 * time.Millisecond, KillGrace: 50 * time.Millisecond}
}

// probeConfig builds a passing probe fixture. RequiredVersion and required
// capabilities are left empty so NewRunner's forced rc.8 defaults are exercised.
func probeConfig(t *testing.T) Config {
	t.Helper()
	probeDir := t.TempDir()
	return Config{
		NodePath:    writeTestFile(t, probeDir, "node", ""),
		EntryPath:   writeTestFile(t, probeDir, "entry.js", ""),
		ConfigPath:  writeTestFile(t, probeDir, "dsh.json", "{}"),
		VersionPath: writeTestFile(t, probeDir, "package.json", `{"version":"0.1.0-rc.8"}`),
	}
}

func saveRun(t *testing.T, store *runhub.Store, runID runhub.RunID, mutate func(*runhub.AgentRun)) {
	t.Helper()
	now := time.Now()
	run := runhub.AgentRun{
		ID: runID, Source: runhub.SourceDSH, Ownership: runhub.OwnershipManaged,
		State: runhub.StateQueued, Activity: runhub.ActivityIdle, Revision: 1,
		LastSeenAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if mutate != nil {
		mutate(&run)
	}
	if err := store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
}

// flakySink injects one retryable receipt per configured EventID, then delegates
// to the inner sink, so reportEvent's retry path is exercised.
type flakySink struct {
	inner runhub.EventSink
	mu    sync.Mutex
	fail  map[runhub.EventID]int
}

func newFlakySink(inner runhub.EventSink, ids ...runhub.EventID) *flakySink {
	m := make(map[runhub.EventID]int, len(ids))
	for _, id := range ids {
		m[id] = 1
	}
	return &flakySink{inner: inner, fail: m}
}

func (f *flakySink) Report(evt runhub.RunEvent) (runhub.Receipt, runhub.AgentRun) {
	f.mu.Lock()
	if n := f.fail[evt.EventID]; n > 0 {
		f.fail[evt.EventID] = n - 1
		f.mu.Unlock()
		return runhub.Receipt{Status: runhub.ReceiptRetryable, EventID: evt.EventID, Message: "injected retryable"}, runhub.AgentRun{}
	}
	f.mu.Unlock()
	return f.inner.Report(evt)
}

func terminalEvent(t *testing.T, store *runhub.Store, runID runhub.RunID) runhub.RunEvent {
	t.Helper()
	events, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case runhub.EventSucceeded, runhub.EventFailed, runhub.EventCancelled, runhub.EventInterrupted, runhub.EventStale:
			return events[i]
		}
	}
	t.Fatalf("no terminal event for run %s", runID)
	return runhub.RunEvent{}
}

func TestRunnerIgnoresEarlyNotificationsAndIdle(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	peer.send(t, sessEventLine(res.sessionID, evTurnStart, 2, `{"turn":1}`))
	peer.send(t, sessEventLine(res.sessionID, evToolCall, 3, `{"turn":1,"step":1,"callId":"c1","name":"read","arguments":"{}"}`))
	peer.send(t, sessEventLine(res.sessionID, evAssistantMessage, 4, `{"turn":1,"step":1,"message":{"content":[{"type":"text","text":"early"}]}}`))
	peer.send(t, sessEventLine(res.sessionID, evTurnEnd, 5, `{"turn":1,"reason":{"kind":"completed"}}`))
	peer.send(t, statusLine(res.sessionID, "idle"))

	if got, _ := hub.Get(runID); got.State != runhub.StateStarting {
		t.Fatalf("early notifications advanced state to %s, want starting", got.State)
	}

	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 6, `{"inserted":[{"id":"msg-1"}]}`))
	waitForState(t, hub, runID, runhub.StateRunning)
}

func TestRunnerIdleWithoutTurnEndFails(t *testing.T) {
	peer := newFakePeer()
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 1, `{"inserted":[{"id":"msg-1"}]}`))
	waitForState(t, hub, runID, runhub.StateRunning)
	peer.send(t, statusLine(res.sessionID, "idle"))

	waitForState(t, hub, runID, runhub.StateFailed)
	if got := terminalEvent(t, store, runID); got.Payload.Detail != detailIdleWithoutEnd {
		t.Fatalf("detail = %q, want %q", got.Payload.Detail, detailIdleWithoutEnd)
	}
}

func TestRunnerTerminalAttribution(t *testing.T) {
	cases := []struct {
		name       string
		reason     string
		wantState  runhub.RunState
		wantDetail string
	}{
		{"completed", `{"kind":"completed"}`, runhub.StateSucceeded, ""},
		{"error", `{"kind":"error"}`, runhub.StateFailed, "turn-ended:error"},
		{"max-tokens", `{"kind":"max-tokens"}`, runhub.StateFailed, "turn-ended:max-tokens"},
		{"empty", `{"kind":""}`, runhub.StateFailed, detailIdleWithoutEnd},
		{"unknown", `{"kind":"future-xyz"}`, runhub.StateFailed, "turn-ended:other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			peer := newFakePeer()
			r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
			res := startPeer(t, r, hub, peer)

			peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 1, `{"inserted":[{"id":"msg-1"}]}`))
			waitForState(t, hub, runID, runhub.StateRunning)
			peer.send(t, sessEventLine(res.sessionID, evTurnEnd, 2, `{"turn":1,"reason":`+tc.reason+`}`))
			peer.send(t, statusLine(res.sessionID, "idle"))

			waitForState(t, hub, runID, tc.wantState)
			if got := terminalEvent(t, store, runID); got.Payload.Detail != tc.wantDetail {
				t.Fatalf("detail = %q, want %q", got.Payload.Detail, tc.wantDetail)
			}
		})
	}
}

func TestRunnerUnknownRunNoProcess(t *testing.T) {
	var launches int32
	store, hub := newStoreAndHub(t)
	r := NewRunner(RunnerConfig{Probe: probeConfig(t), Provider: "deepseek-official", Model: "deepseek-chat", Timeouts: quickTimeouts(),
		Launcher: func(ProcessSpec) (Proc, error) { atomic.AddInt32(&launches, 1); return newFakePeer(), nil }}, store)

	_, err := r.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "req-x", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hi"},
	}, hub)
	if err == nil || !strings.Contains(err.Error(), "unknown run") {
		t.Fatalf("Start = %v, want unknown run error", err)
	}
	if atomic.LoadInt32(&launches) != 0 {
		t.Fatalf("launched %d processes, want 0", launches)
	}
}

func TestRunnerWrongSourceNoProcess(t *testing.T) {
	var launches int32
	store, hub := newStoreAndHub(t)
	r := NewRunner(RunnerConfig{Probe: probeConfig(t), Provider: "deepseek-official", Model: "deepseek-chat", Timeouts: quickTimeouts(),
		Launcher: func(ProcessSpec) (Proc, error) { atomic.AddInt32(&launches, 1); return newFakePeer(), nil }}, store)

	_, err := r.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "req-x", Source: runhub.SourceCodex, Workspace: "/work", Prompt: "hi"},
	}, hub)
	if err == nil || !strings.Contains(err.Error(), "unsupported source") {
		t.Fatalf("Start = %v, want unsupported source error", err)
	}
	if atomic.LoadInt32(&launches) != 0 {
		t.Fatalf("launched %d processes, want 0", launches)
	}
}

func TestRunnerWrongOwnershipNoProcess(t *testing.T) {
	var launches int32
	store, hub := newStoreAndHub(t)
	runID := runhub.DeriveRunID("req-x")
	saveRun(t, store, runID, func(r *runhub.AgentRun) { r.Ownership = runhub.OwnershipObserved })
	r := NewRunner(RunnerConfig{Probe: probeConfig(t), Provider: "deepseek-official", Model: "deepseek-chat", Timeouts: quickTimeouts(),
		Launcher: func(ProcessSpec) (Proc, error) { atomic.AddInt32(&launches, 1); return newFakePeer(), nil }}, store)

	_, err := r.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "req-x", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hi"},
	}, hub)
	if err == nil || !strings.Contains(err.Error(), "not managed") {
		t.Fatalf("Start = %v, want not managed error", err)
	}
	if atomic.LoadInt32(&launches) != 0 {
		t.Fatalf("launched %d processes, want 0", launches)
	}
}

func TestRunnerRequiresQueuedRunNoProcess(t *testing.T) {
	var launches int32
	store, hub := newStoreAndHub(t)
	runID := runhub.DeriveRunID("req-x")
	saveRun(t, store, runID, func(r *runhub.AgentRun) { r.State = runhub.StateRunning })
	r := NewRunner(RunnerConfig{Probe: probeConfig(t), Provider: "deepseek-official", Model: "deepseek-chat", Timeouts: quickTimeouts(),
		Launcher: func(ProcessSpec) (Proc, error) { atomic.AddInt32(&launches, 1); return newFakePeer(), nil }}, store)

	_, err := r.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "req-x", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hi"},
	}, hub)
	if err == nil || !strings.Contains(err.Error(), "not queued") {
		t.Fatalf("Start = %v, want not queued error", err)
	}
	if atomic.LoadInt32(&launches) != 0 {
		t.Fatalf("launched %d processes, want 0", launches)
	}
}

func TestNewRunnerForcesRc8Defaults(t *testing.T) {
	store, _ := newStoreAndHub(t)
	r := NewRunner(RunnerConfig{Probe: Config{
		RequiredVersion:      "0.1.0-rc.7",
		RequiredCapabilities: []Capability{CapInitialize},
	}, Provider: "p", Model: "m"}, store)
	if r.cfg.Probe.RequiredVersion != "0.1.0-rc.8" {
		t.Fatalf("RequiredVersion = %q, want 0.1.0-rc.8", r.cfg.Probe.RequiredVersion)
	}
	if len(r.cfg.Probe.RequiredCapabilities) != len(Rc8Capabilities()) {
		t.Fatalf("RequiredCapabilities = %v, want rc8 baseline", r.cfg.Probe.RequiredCapabilities)
	}
}

func TestRunnerPassesEnvToLauncher(t *testing.T) {
	var got ProcessSpec
	peer := newFakePeer()
	store, hub := newStoreAndHub(t)
	r := NewRunner(RunnerConfig{
		Probe:    probeConfig(t),
		Provider: "deepseek-official",
		Model:    "deepseek-chat",
		Env:      []string{"DSH_CWD=/work", "DEEPSEEK_BASE_URL=http://127.0.0.1:9"},
		Launcher: func(spec ProcessSpec) (Proc, error) {
			got = spec
			return peer, nil
		},
	}, store)
	if rec, _ := hub.Launch(runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}); rec.Status != runhub.ReceiptAccepted {
		t.Fatalf("launch status = %s: %s", rec.Status, rec.Message)
	}

	startPeer(t, r, hub, peer)

	if len(got.Env) != 2 || got.Env[0] != "DSH_CWD=/work" || got.Env[1] != "DEEPSEEK_BASE_URL=http://127.0.0.1:9" {
		t.Fatalf("launcher spec.Env = %q, want the configured child environment", got.Env)
	}
}

func TestRunnerFirstCancelRespectsContext(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Cancel(ctx, res.binding); !errors.Is(err, context.Canceled) {
		t.Fatalf("Cancel = %v, want context.Canceled", err)
	}

	// Cleanup continues independently after the caller stops waiting.
	shutdownReq := peer.readRequest(t)
	peer.respond(t, shutdownReq, `{}`)
	waitForState(t, hub, runID, runhub.StateCancelled)
}

func TestRunnerProviderModelRequired(t *testing.T) {
	var launches int32
	store, hub := newStoreAndHub(t)
	runID := runhub.DeriveRunID("req-x")
	saveRun(t, store, runID, nil)
	for name, cfg := range map[string]RunnerConfig{
		"empty-provider": {Probe: probeConfig(t), Model: "m"},
		"empty-model":    {Probe: probeConfig(t), Provider: "p"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Timeouts = quickTimeouts()
			cfg.Launcher = func(ProcessSpec) (Proc, error) { atomic.AddInt32(&launches, 1); return newFakePeer(), nil }
			r := NewRunner(cfg, store)
			_, err := r.Start(context.Background(), runhub.LaunchRequest{
				LaunchIntent: runhub.LaunchIntent{RequestID: "req-x", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hi"},
			}, hub)
			if err == nil {
				t.Fatalf("Start succeeded, want error")
			}
		})
	}
	if atomic.LoadInt32(&launches) != 0 {
		t.Fatalf("launched %d processes, want 0", launches)
	}
}

func TestRunnerReportRetryableThenSuccess(t *testing.T) {
	peer := newFakePeer()
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	sink := newFlakySink(hub, runhub.EventID(string(runID)+":running"), runhub.EventID(string(runID)+":settle"))
	res := startPeer(t, r, sink, peer)

	peer.send(t, sessEventLine(res.sessionID, evInboxSpliced, 1, `{"inserted":[{"id":"msg-1"}]}`))
	waitForState(t, hub, runID, runhub.StateRunning)
	peer.send(t, sessEventLine(res.sessionID, evTurnEnd, 2, `{"turn":1,"reason":{"kind":"completed"}}`))
	peer.send(t, statusLine(res.sessionID, "idle"))
	waitForState(t, hub, runID, runhub.StateSucceeded)

	// The retried running + terminal events must be materialized durably.
	events, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	var sawRunning, sawTerminal bool
	for _, ev := range events {
		if ev.Type == runhub.EventRunning {
			sawRunning = true
		}
		switch ev.Type {
		case runhub.EventSucceeded, runhub.EventFailed, runhub.EventCancelled, runhub.EventInterrupted, runhub.EventStale:
			sawTerminal = true
		}
	}
	if !sawRunning || !sawTerminal {
		t.Fatalf("running=%v terminal=%v, want both materialized", sawRunning, sawTerminal)
	}
}

func TestRunnerCrashDetailHasNoSecrets(t *testing.T) {
	peer := newFakePeer()
	peer.writeStderr("sk-APIKEY1234 prompt-leak-secret\n")
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	startPeer(t, r, hub, peer)

	peer.signalExit(errors.New("boom prompt-leak-secret"))
	waitForState(t, hub, runID, runhub.StateFailed)

	events, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%+v", events)
	for _, leak := range []string{"APIKEY", "prompt-leak-secret", "boom"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("durable events leak %q: %s", leak, blob)
		}
	}
	if got := terminalEvent(t, store, runID); got.Payload.Detail != detailRuntimeExited {
		t.Fatalf("detail = %q, want %q", got.Payload.Detail, detailRuntimeExited)
	}
}

func TestRunnerRPCErrorDetailHasNoMessage(t *testing.T) {
	peer := newFakePeer()
	r, hub, store, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })

	intent := runhub.LaunchIntent{RequestID: "req-1", Source: runhub.SourceDSH, Workspace: "/work", Prompt: "hello"}
	resCh := make(chan error, 1)
	go func() {
		_, err := r.Start(context.Background(), runhub.LaunchRequest{LaunchIntent: intent}, hub)
		resCh <- err
	}()
	initReq := peer.readRequest(t)
	peer.send(t, `{"jsonrpc":"2.0","id":`+string(initReq.ID)+`,"error":{"code":-32601,"message":"auth sk-APIKEY prompt-leak-secret"}}`)
	if err := <-resCh; err == nil {
		t.Fatalf("Start succeeded, want error response failure")
	}
	waitForState(t, hub, runID, runhub.StateFailed)

	events, err := store.ListEvents(runID)
	if err != nil {
		t.Fatal(err)
	}
	blob := fmt.Sprintf("%+v", events)
	for _, leak := range []string{"APIKEY", "prompt-leak-secret", "auth"} {
		if strings.Contains(blob, leak) {
			t.Fatalf("durable events leak %q: %s", leak, blob)
		}
	}
	if got := terminalEvent(t, store, runID); got.Payload.Detail != detailInitializeFailed {
		t.Fatalf("detail = %q, want %q", got.Payload.Detail, detailInitializeFailed)
	}
}

func TestRunnerCleanExitCallsCleanupNotKill(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, runID := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	startPeer(t, r, hub, peer)

	peer.signalExit(nil) // clean exit, no error
	waitForState(t, hub, runID, runhub.StateFailed)
	waitFor(t, 2*time.Second, func() bool { return peer.cleanedFlag() })
	if peer.killedFlag() {
		t.Fatalf("clean exit should not kill the process")
	}
}

func TestRunnerCancelRespectsContext(t *testing.T) {
	peer := newFakePeer()
	r, hub, _, _ := newTestRunner(t, func(ProcessSpec) (Proc, error) { return peer, nil })
	res := startPeer(t, r, hub, peer)

	firstDone := make(chan error, 1)
	go func() { firstDone <- r.Cancel(context.Background(), res.binding) }()

	// Wait until the first cancel is inside its quiesce (shutdown request sent).
	shutdownReq := peer.readRequest(t)

	cancelledCtx, cancelFn := context.WithCancel(context.Background())
	cancelFn()
	if err := r.Cancel(cancelledCtx, res.binding); !errors.Is(err, context.Canceled) {
		t.Fatalf("second Cancel = %v, want context.Canceled", err)
	}

	peer.respond(t, shutdownReq, `{}`)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Cancel: %v", err)
	}
}
