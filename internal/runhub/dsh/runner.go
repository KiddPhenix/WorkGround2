package dsh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"workground2/internal/runhub"
)

// serverIdentity is the wire-stable server name the SDK runtime must return at
// initialize. A different identity means the peer is not the DSH SDK runtime.
const serverIdentity = "deepseek-harness-sdk-runtime"

// requiredDSHVersion is the baseline the managed runner enforces. Anything else
// is refused before a process is spawned.
const requiredDSHVersion = "0.1.0-rc.8"

// ErrAlreadySettled marks a Start for a run that is already terminal. A runner
// must never spawn a second process for such a run.
var ErrAlreadySettled = errors.New("dsh: run already settled")

// Stable terminal detail codes. Only these (plus a fixed turn-end reason) may
// reach the durable EventPayload.Detail: raw stderr, JSON-RPC error messages,
// the prompt and model output never belong there.
const (
	detailCancelled        = "cancelled"
	detailRuntimeExited    = "runtime-exited"
	detailProtocolError    = "protocol-error"
	detailIdleWithoutEnd   = "idle-without-turn-end"
	detailProbeFailed      = "probe-failed"
	detailLaunchFailed     = "launch-failed"
	detailInitializeFailed = "initialize-failed"
	detailPromptFailed     = "prompt-failed"
	detailBindFailed       = "binding-failed"
	detailReportFailed     = "report-failed"
)

// Timeouts bound the synchronous protocol phases and the cancel quiesce ladder.
// Zero values select defaults.
type Timeouts struct {
	Initialize time.Duration
	Prompt     time.Duration
	Shutdown   time.Duration
	KillGrace  time.Duration
}

func (t Timeouts) withDefaults() Timeouts {
	if t.Initialize <= 0 {
		t.Initialize = 30 * time.Second
	}
	if t.Prompt <= 0 {
		t.Prompt = 30 * time.Second
	}
	if t.Shutdown <= 0 {
		t.Shutdown = 3 * time.Second
	}
	if t.KillGrace <= 0 {
		t.KillGrace = 2 * time.Second
	}
	return t
}

// RunnerConfig is the explicit construction input for the DSH managed runner.
type RunnerConfig struct {
	Probe      Config // filesystem/version/capability probe input
	Provider   string // initialize provider route
	Model      string // initialize model name
	MaxTokens  int64  // optional output-token cap (0 = omit)
	MaxSummary int    // final-summary length cap (0 = DefaultMaxSummary)
	Timeouts   Timeouts
	Launcher   Launcher // process factory; nil selects LaunchProcess
	// Env is the complete child environment for the DSH runtime (exec.Cmd.Env
	// semantics: nil inherits the parent process environment). A managed runner
	// should pass a deliberate, minimal environment (e.g. DEEPSEEK_API_KEY,
	// DEEPSEEK_BASE_URL, DSH_SESSION_ROOT) rather than leaking every parent
	// secret into the child. Keys are passed through to the runtime verbatim.
	Env []string
}

// Runner is the DSH managed runner: one run owns one DSH SDK runtime process.
// It implements runhub.Runner and persists RunnerBindings through the shared
// runhub.Store so a restart can settle orphaned runs without restarting them.
type Runner struct {
	cfg      RunnerConfig
	store    *runhub.Store
	launcher Launcher

	mu       sync.Mutex
	sessions map[runhub.RunID]*runSession
	starting map[runhub.RunID]*startGate
}

type startGate struct {
	done    chan struct{}
	binding runhub.RunnerBinding
	err     error
}

// NewRunner builds a DSH managed runner over store. It forces the rc.8 version
// baseline and its required capability surface unless the caller supplied
// explicit overrides, so a managed run can never silently target an untested DSH.
func NewRunner(cfg RunnerConfig, store *runhub.Store) *Runner {
	cfg.Timeouts = cfg.Timeouts.withDefaults()
	if cfg.MaxSummary <= 0 {
		cfg.MaxSummary = DefaultMaxSummary
	}
	// P2 is evidence-locked to rc.8. Callers may describe what is installed via
	// KnownCapabilities, but cannot weaken the managed runner's required wire.
	cfg.Probe.RequiredVersion = requiredDSHVersion
	cfg.Probe.RequiredCapabilities = Rc8Capabilities()
	if cfg.Launcher == nil {
		cfg.Launcher = LaunchProcess
	}
	return &Runner{
		cfg:      cfg,
		store:    store,
		launcher: cfg.Launcher,
		sessions: make(map[runhub.RunID]*runSession),
		starting: make(map[runhub.RunID]*startGate),
	}
}

// Probe checks the DSH filesystem, version and capability surface and returns
// the control capabilities this runner exposes. The DSH SDK runtime has no
// open/resume/approve/send surface, so only Cancel is declared.
func (r *Runner) Probe(ctx context.Context, _ runhub.Profile) (runhub.Capabilities, error) {
	res, err := Probe(r.cfg.Probe)
	if err != nil {
		return runhub.Capabilities{}, err
	}
	if !res.Ready() {
		return runhub.Capabilities{}, fmt.Errorf("dsh: probe not ready: %s", issueText(res.Missing))
	}
	return runhub.Capabilities{Cancel: true}, nil
}

// Start launches one run: probe → process → initialize → session/prompt, then
// persists a binding and hands notification-driven settlement to a background
// loop. The session id is derived from the run id, so a retry of the same
// request reuses it; the message id belongs only to this prompt. Concurrent or
// duplicate Start calls for the same run collapse to one process, and a terminal
// run is never started again.
func (r *Runner) Start(ctx context.Context, req runhub.LaunchRequest, sink runhub.EventSink) (runhub.RunnerBinding, error) {
	if sink == nil {
		return runhub.RunnerBinding{}, errors.New("dsh: nil event sink")
	}
	runID := runhub.DeriveRunID(req.RequestID)

	r.mu.Lock()
	if s, ok := r.sessions[runID]; ok && !s.settled() {
		b := s.binding
		r.mu.Unlock()
		return b, nil
	}
	if g, ok := r.starting[runID]; ok {
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return runhub.RunnerBinding{}, ctx.Err()
		case <-g.done:
			return g.binding, g.err
		}
	}
	g := &startGate{done: make(chan struct{})}
	r.starting[runID] = g
	r.mu.Unlock()

	binding, err := r.start(ctx, req, sink, runID)

	r.mu.Lock()
	g.binding, g.err = binding, err
	close(g.done)
	delete(r.starting, runID)
	r.mu.Unlock()
	return binding, err
}

func (r *Runner) start(ctx context.Context, req runhub.LaunchRequest, sink runhub.EventSink, runID runhub.RunID) (runhub.RunnerBinding, error) {
	if r.store == nil {
		return runhub.RunnerBinding{}, errors.New("dsh: nil store")
	}
	if strings.TrimSpace(r.cfg.Provider) == "" {
		return runhub.RunnerBinding{}, errors.New("dsh: provider is empty")
	}
	if strings.TrimSpace(r.cfg.Model) == "" {
		return runhub.RunnerBinding{}, errors.New("dsh: model is empty")
	}
	source := req.Source
	if source == "" {
		source = runhub.SourceDSH
	}
	if source != runhub.SourceDSH {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: unsupported source %q", source)
	}

	// The run must already exist as a managed, queued DSH run. Unknown runs,
	// wrong source, wrong ownership and terminal/non-queued runs are refused
	// before any process is spawned, and without reporting events for runs we
	// do not own.
	run, ok, err := r.store.LoadRun(runID)
	if err != nil {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: load run: %w", err)
	}
	if !ok {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: unknown run %s", runID)
	}
	if run.Ownership != runhub.OwnershipManaged {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: run %s is %s, not managed", runID, run.Ownership)
	}
	if run.Source != runhub.SourceDSH {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: run %s source is %s, want dsh", runID, run.Source)
	}
	if run.State.IsTerminal() {
		return runhub.RunnerBinding{}, ErrAlreadySettled
	}
	if run.State != runhub.StateQueued {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: run %s is %s, not queued", runID, run.State)
	}
	if _, ok, err := r.store.LoadBinding(runID); err != nil {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: load binding: %w", err)
	} else if ok {
		return runhub.RunnerBinding{}, fmt.Errorf("dsh: run %s has an unfinished binding; settle it via RecoverBindings before starting", runID)
	}

	fail := func(code string, err error) (runhub.RunnerBinding, error) {
		_ = reportEvent(sink, runhub.RunEvent{
			EventID:    settleEventID(runID),
			RunID:      runID,
			Source:     source,
			OccurredAt: time.Now(),
			Type:       runhub.EventFailed,
			Payload:    runhub.EventPayload{Detail: code},
		})
		return runhub.RunnerBinding{}, err
	}

	res, err := Probe(r.cfg.Probe)
	if err != nil {
		return fail(detailProbeFailed, fmt.Errorf("dsh: probe: %w", err))
	}
	if !res.Ready() {
		return fail(detailProbeFailed, fmt.Errorf("dsh: probe not ready: %s", issueText(res.Missing)))
	}

	spec := ProcessSpec{
		NodePath:   res.NodePath,
		EntryPath:  res.EntryPath,
		ConfigPath: res.ConfigPath,
		Dir:        req.Workspace,
		Env:        r.cfg.Env,
	}
	procHandle, err := r.launcher(spec)
	if err != nil {
		return fail(detailLaunchFailed, fmt.Errorf("dsh: launch: %w", err))
	}
	cleanup := true
	defer func() {
		if cleanup {
			procHandle.Kill()
		}
	}()

	if err := reportEvent(sink, runhub.RunEvent{
		EventID:    runhub.EventID(string(runID) + ":starting"),
		RunID:      runID,
		Source:     source,
		OccurredAt: time.Now(),
		Type:       runhub.EventStarting,
	}); err != nil {
		return fail(detailReportFailed, fmt.Errorf("dsh: report starting: %w", err))
	}

	sessionID := sessionIDFor(runID)
	sess := &runSession{
		runner:    r,
		runID:     runID,
		source:    source,
		sessionID: sessionID,
		sink:      sink,
		proc:      procHandle,
		settledCh: make(chan struct{}),
	}
	sess.client = NewClient(procHandle.Stdin(), procHandle.Stdout(), DefaultMaxFrameSize)
	sess.client.SetTransportErrorHandler(sess.handleTransportError)

	initCtx, initCancel := context.WithTimeout(ctx, r.cfg.Timeouts.Initialize)
	var initRes InitializeResult
	initErr := sess.client.Call(initCtx, MethodInitialize, InitializeParams{
		CWD:       req.Workspace,
		Provider:  r.cfg.Provider,
		Model:     r.cfg.Model,
		MaxTokens: r.cfg.MaxTokens,
	}, &initRes)
	initCancel()
	if initErr != nil {
		return fail(detailInitializeFailed, fmt.Errorf("dsh: initialize: %w", initErr))
	}
	if initRes.ServerInfo.Name != serverIdentity {
		return fail(detailInitializeFailed, fmt.Errorf("dsh: initialize server identity %q, want %q", initRes.ServerInfo.Name, serverIdentity))
	}

	blocks, err := json.Marshal([]struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{{Type: "text", Text: req.Prompt}})
	if err != nil {
		return fail(detailPromptFailed, fmt.Errorf("dsh: marshal prompt: %w", err))
	}
	promptCtx, promptCancel := context.WithTimeout(ctx, r.cfg.Timeouts.Prompt)
	var promptRes PromptResult
	promptErr := sess.client.Call(promptCtx, MethodPrompt, PromptParams{
		SessionID:     sessionID,
		ContentBlocks: json.RawMessage(blocks),
	}, &promptRes)
	promptCancel()
	if promptErr != nil {
		return fail(detailPromptFailed, fmt.Errorf("dsh: session/prompt: %w", promptErr))
	}
	if promptRes.MessageID == "" {
		return fail(detailPromptFailed, errors.New("dsh: session/prompt returned an empty messageId"))
	}
	sess.messageID = promptRes.MessageID

	binding := runhub.RunnerBinding{
		RunID:           runID,
		NativeSessionID: sessionID,
		ProtocolVersion: "2.0",
		ProcessRef:      procHandle.Ref(),
		Attempt:         1,
	}
	sess.binding = binding

	// Persist the binding before the run is observable as live, so a restart can
	// always classify this run (the run snapshot may lag behind by one materialize).
	if err := r.store.SaveBinding(runhub.BindingRecord{RunID: runID, Binding: binding, State: runhub.StateStarting, SavedAt: time.Now()}); err != nil {
		return fail(detailBindFailed, fmt.Errorf("dsh: persist binding: %w", err))
	}

	r.mu.Lock()
	r.sessions[runID] = sess
	r.mu.Unlock()
	sess.client.SetHandler(sess.handleNotify)
	cleanup = false

	go sess.watch()
	return binding, nil
}

// Cancel idempotently stops a live run. The first caller marks the cancellation
// intent, walks the bounded shutdown → stdin-EOF → process-tree-kill quiesce,
// confirms the process has exited, and only then settles the run as cancelled.
// Concurrent callers wait on the same quiesce and respect their own context.
func (r *Runner) Cancel(ctx context.Context, b runhub.RunnerBinding) error {
	if r.store == nil {
		return errors.New("dsh: nil store")
	}
	r.mu.Lock()
	s := r.sessions[b.RunID]
	r.mu.Unlock()
	if s != nil {
		return s.cancel(ctx)
	}
	// No live session: Cancel is idempotent for an already-terminal run, and an
	// explicit error for anything else we do not own in this process.
	if run, ok, err := r.store.LoadRun(b.RunID); err != nil {
		return err
	} else if ok && run.State.IsTerminal() {
		return nil
	}
	return fmt.Errorf("dsh: run %s is not live in this process", b.RunID)
}

// Open is unsupported: the DSH SDK runtime exposes no reveal-in-UI surface.
func (r *Runner) Open(ctx context.Context, b runhub.RunnerBinding) error {
	return runhub.ErrUnsupported
}

// Recover returns the best-known durable observation of a previously bound run.
func (r *Runner) Recover(ctx context.Context, b runhub.RunnerBinding) (runhub.Observation, error) {
	if r.store == nil {
		return runhub.Observation{}, errors.New("dsh: nil store")
	}
	run, ok, err := r.store.LoadRun(b.RunID)
	if err != nil {
		return runhub.Observation{}, err
	}
	if !ok {
		return runhub.Observation{}, fmt.Errorf("dsh: unknown run %s", b.RunID)
	}
	binding := b
	if rec, ok, err := r.store.LoadBinding(b.RunID); err != nil {
		return runhub.Observation{}, err
	} else if ok {
		binding = rec.Binding
	}
	return runhub.Observation{
		Binding:  binding,
		State:    run.State,
		Activity: run.Activity,
		Summary:  run.Summary,
	}, nil
}

func (r *Runner) removeSession(s *runSession) {
	r.mu.Lock()
	if cur, ok := r.sessions[s.runID]; ok && cur == s {
		delete(r.sessions, s.runID)
	}
	r.mu.Unlock()
}

func sessionIDFor(runID runhub.RunID) string {
	return "wg2-" + string(runID)
}

func settleEventID(runID runhub.RunID) runhub.EventID {
	return runhub.EventID(string(runID) + ":settle")
}

func issueText(issues []Issue) string {
	parts := make([]string, 0, len(issues))
	for _, iss := range issues {
		parts = append(parts, string(iss.Kind)+": "+iss.Detail)
	}
	return strings.Join(parts, "; ")
}

// reportEvent submits one normalized event and honors its receipt: accepted and
// already_applied succeed; retryable is retried with the same EventID to finish
// materialization; invalid and stale fail explicitly.
func reportEvent(sink runhub.EventSink, evt runhub.RunEvent) error {
	const maxAttempts = 5
	var last runhub.Receipt
	for i := 0; i < maxAttempts; i++ {
		rec, _ := sink.Report(evt)
		last = rec
		switch rec.Status {
		case runhub.ReceiptAccepted, runhub.ReceiptAlreadyApplied:
			return nil
		case runhub.ReceiptRetryable:
			time.Sleep(time.Duration(i) * 2 * time.Millisecond)
			continue
		case runhub.ReceiptInvalid, runhub.ReceiptStale:
			return fmt.Errorf("runhub: report %s: %s", evt.Type, rec.Message)
		}
	}
	return fmt.Errorf("runhub: report %s not materialized after %d attempts: %s", evt.Type, maxAttempts, last.Message)
}

// turnEndDetail maps a turn/end reason to a stable detail code. Unknown future
// reasons collapse to a generic "other" so arbitrary runtime text never reaches
// the durable detail.
func turnEndDetail(kind string) string {
	switch kind {
	case "completed":
		return "turn-ended:completed"
	case "error":
		return "turn-ended:error"
	case "aborted":
		return "turn-ended:aborted"
	case "max-tokens":
		return "turn-ended:max-tokens"
	case "blocked":
		return "turn-ended:blocked"
	case "interrupted":
		return "turn-ended:interrupted"
	default:
		return "turn-ended:other"
	}
}

// settleKind identifies which settlement path fired.
type settleKind int

const (
	settleIdle settleKind = iota
	settleFailed
	settleCancelled
)

// runSession is the live runtime state of one started run. Notification state is
// mutated from the client reader goroutine and read by Cancel/watch, all guarded
// by mu; terminal settlement is exactly-once via settledOnce, teardown is
// single-flight via teardownOnce, and cancel is single-flight via cancelDone.
type runSession struct {
	runner    *Runner
	runID     runhub.RunID
	source    runhub.Source
	sessionID string
	messageID string
	sink      runhub.EventSink
	proc      Proc
	client    *Client
	binding   runhub.RunnerBinding

	mu            sync.Mutex
	receivedInbox bool
	turnEnd       string
	summary       string
	lastAct       runhub.Activity
	lastLabel     string
	cancelReq     bool
	failCode      string
	cancelDone    chan struct{}

	settledOnce sync.Once
	settledCh   chan struct{}
	settleErr   error

	teardownOnce sync.Once
}

func (s *runSession) settled() bool {
	select {
	case <-s.settledCh:
		return true
	default:
		return false
	}
}

func (s *runSession) handleNotify(f Frame) {
	if s.settled() {
		return
	}
	switch f.Method {
	case MethodSessionStatus:
		var p SessionStatusParams
		if err := f.DecodeParams(&p); err != nil {
			s.failTransport(detailProtocolError)
			return
		}
		if p.SessionID != s.sessionID {
			return
		}
		s.handleStatus(p)
	case MethodSessionEvent:
		var p SessionEventParams
		if err := f.DecodeParams(&p); err != nil {
			s.failTransport(detailProtocolError)
			return
		}
		if p.SessionID != s.sessionID {
			return
		}
		s.handleEvent(p)
	}
}

func (s *runSession) handleStatus(p SessionStatusParams) {
	if p.Status != SessionIdle {
		return
	}
	s.mu.Lock()
	received := s.receivedInbox
	s.mu.Unlock()
	if !received {
		// An idle before our message was spliced belongs to an earlier activity
		// interval and is ignored, never a failure.
		return
	}
	s.settleAndTeardown(settleIdle)
}

func (s *runSession) handleEvent(p SessionEventParams) {
	ev, err := decodeSessionEvent(p.Event)
	if err != nil {
		s.failTransport(detailProtocolError)
		return
	}
	if ev.Type == evInboxSpliced {
		if isInboxReceipt(ev, s.messageID) {
			s.markRunning()
		}
		return
	}

	// Before our message id is owned (the inbox spliced receipt), every other
	// turn/activity/summary/status event is ignored: it must neither advance nor
	// fail the run.
	s.mu.Lock()
	received := s.receivedInbox
	s.mu.Unlock()
	if !received {
		return
	}

	switch ev.Type {
	case evTurnStart:
		s.setActivity(runhub.ActivityThinking, "", ev.Seq)
	case evAssistantChunk:
		s.setActivity(runhub.ActivityResponding, "", ev.Seq)
	case evToolCall:
		s.setActivity(runhub.ActivityTool, toolCallName(ev), ev.Seq)
	case evToolResult:
		s.setActivity(runhub.ActivityThinking, "", ev.Seq)
	case evAssistantMessage:
		if text := assistantText(ev, s.runner.cfg.MaxSummary); text != "" {
			s.mu.Lock()
			s.summary = text
			s.mu.Unlock()
		}
	case evTurnEnd:
		s.mu.Lock()
		s.turnEnd = turnEndKind(ev)
		s.mu.Unlock()
	}
}

func (s *runSession) markRunning() {
	s.mu.Lock()
	if s.receivedInbox {
		s.mu.Unlock()
		return
	}
	s.receivedInbox = true
	s.mu.Unlock()
	if err := reportEvent(s.sink, runhub.RunEvent{
		EventID:    runhub.EventID(string(s.runID) + ":running"),
		RunID:      s.runID,
		Source:     s.source,
		OccurredAt: time.Now(),
		Type:       runhub.EventRunning,
	}); err != nil {
		s.failTransport(detailReportFailed)
	}
}

func (s *runSession) setActivity(act runhub.Activity, label string, seq int64) {
	s.mu.Lock()
	if act == s.lastAct && label == s.lastLabel {
		s.mu.Unlock()
		return
	}
	s.lastAct, s.lastLabel = act, label
	s.mu.Unlock()
	if err := reportEvent(s.sink, runhub.RunEvent{
		EventID:    runhub.EventID(string(s.runID) + ":act:" + strconv.FormatInt(seq, 10)),
		RunID:      s.runID,
		Source:     s.source,
		OccurredAt: time.Now(),
		Type:       runhub.EventActivity,
		Payload:    runhub.EventPayload{Activity: act, Label: label},
	}); err != nil {
		s.failTransport(detailReportFailed)
	}
}

func (s *runSession) failTransport(code string) {
	s.mu.Lock()
	if s.failCode == "" {
		s.failCode = code
	}
	s.mu.Unlock()
	s.settleAndTeardown(settleFailed)
}

func (s *runSession) handleTransportError(err error) {
	s.failTransport(detailProtocolError)
}

func (s *runSession) settle(kind settleKind) {
	s.settledOnce.Do(func() {
		s.settleErr = s.reportTerminal(kind)
		close(s.settledCh)
	})
}

func (s *runSession) settleAndTeardown(kind settleKind) {
	s.settle(kind)
	go s.teardown(context.Background())
}

func (s *runSession) reportTerminal(kind settleKind) error {
	s.mu.Lock()
	summary := s.summary
	turnEnd := s.turnEnd
	cancelReq := s.cancelReq
	failCode := s.failCode
	s.mu.Unlock()

	evtType := runhub.EventFailed
	detail := ""
	switch {
	case kind == settleCancelled || cancelReq:
		evtType = runhub.EventCancelled
		detail = detailCancelled
	case kind == settleFailed:
		evtType = runhub.EventFailed
		detail = failCode
		if detail == "" {
			detail = detailRuntimeExited
		}
	case kind == settleIdle:
		switch {
		case turnEnd == "completed":
			evtType = runhub.EventSucceeded
		case turnEnd == "":
			evtType = runhub.EventFailed
			detail = detailIdleWithoutEnd
		default:
			evtType = runhub.EventFailed
			detail = turnEndDetail(turnEnd)
		}
	}

	var firstErr error
	if summary != "" {
		firstErr = reportEvent(s.sink, runhub.RunEvent{
			EventID:    runhub.EventID(string(s.runID) + ":summary"),
			RunID:      s.runID,
			Source:     s.source,
			OccurredAt: time.Now(),
			Type:       runhub.EventSummary,
			Payload:    runhub.EventPayload{Summary: summary},
		})
	}
	if err := reportEvent(s.sink, runhub.RunEvent{
		EventID:    settleEventID(s.runID),
		RunID:      s.runID,
		Source:     s.source,
		OccurredAt: time.Now(),
		Type:       evtType,
		Payload:    runhub.EventPayload{Detail: detail},
	}); err != nil {
		return err
	}
	return firstErr
}

func (s *runSession) watch() {
	<-s.proc.Wait()
	s.mu.Lock()
	cancelReq := s.cancelReq
	s.mu.Unlock()
	if cancelReq {
		s.settleAndTeardown(settleCancelled)
		return
	}
	// The raw exit error and stderr tail are kept only as in-process diagnostics
	// (Proc.StderrTail); the durable detail stays a stable code.
	s.mu.Lock()
	if s.failCode == "" {
		s.failCode = detailRuntimeExited
	}
	s.mu.Unlock()
	s.settleAndTeardown(settleFailed)
}

// teardown walks the bounded quiesce ladder exactly once: protocol shutdown,
// stdin EOF, then process-tree kill, confirming exit before releasing resources
// and removing the session from the runner's registry.
func (s *runSession) teardown(ctx context.Context) {
	s.teardownOnce.Do(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, s.runner.cfg.Timeouts.Shutdown)
		_ = s.client.Call(shutdownCtx, MethodShutdown, ShutdownParams{}, nil)
		cancel()

		_ = s.proc.CloseStdin()
		select {
		case <-s.proc.Exited():
		case <-time.After(s.runner.cfg.Timeouts.KillGrace):
			s.proc.Kill()
			select {
			case <-s.proc.Exited():
			case <-time.After(s.runner.cfg.Timeouts.KillGrace):
				s.proc.Kill()
			}
		}
		s.proc.Cleanup()
		s.runner.removeSession(s)
	})
}

// cancel marks the intent and runs the quiesce, then settles cancelled only
// after the process has exited. It is single-flight: concurrent callers wait on
// the same completion and honor their own context.
func (s *runSession) cancel(ctx context.Context) error {
	s.mu.Lock()
	s.cancelReq = true
	if s.cancelDone == nil {
		s.cancelDone = make(chan struct{})
		done := s.cancelDone
		go func() {
			// Cleanup ownership is independent of any one caller. Every caller,
			// including the first, may stop waiting through its own context.
			s.teardown(context.Background())
			s.settle(settleCancelled)
			close(done)
		}()
	}
	done := s.cancelDone
	s.mu.Unlock()

	select {
	case <-done:
		return s.settleErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
