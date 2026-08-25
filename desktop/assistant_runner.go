package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/permission"
)

const (
	assistantTickInterval = 30 * time.Second
	assistantLeaseTTL     = 2 * time.Minute
	assistantReadyTimeout = 20 * time.Second
)

type assistantSession struct {
	TabID       string
	SessionPath string
}

type assistantSessionHost interface {
	PrepareSession(assistant.Run) (assistantSession, error)
	WaitReady(context.Context, string, time.Duration) (assistantController, error)
	TrySubmit(string, string, assistant.Policy, []control.ToolGrant, func() bool, func()) (bool, error)
	Cancel(string)
}

type assistantController interface {
	SetToolApprovalMode(string)
}

type appAssistantSessionHost struct{ app *App }

func (h appAssistantSessionHost) PrepareSession(run assistant.Run) (assistantSession, error) {
	var tab *WorkspaceTab
	var err error
	if strings.TrimSpace(run.SessionPath) != "" {
		// Restored run: persist the Assistant identity before the tab loads so
		// boot.Build deterministically selects the Assistant system prompt.
		if err := ensureAssistantSessionMeta(run.SessionPath, run.AssistantID); err != nil {
			return assistantSession{}, err
		}
		tab, err = h.app.ensureTabForSessionPath(run.SessionPath)
	} else {
		tab, err = h.app.ensureAssistantBackgroundTab(run)
	}
	if err != nil {
		return assistantSession{}, err
	}
	if tab == nil {
		return assistantSession{}, errors.New("assistant session tab was not created")
	}
	if err := h.app.ensureAssistantTabProfile(tab, run.AssistantID); err != nil {
		return assistantSession{}, err
	}
	path := strings.TrimSpace(tab.currentSessionPath())
	if path == "" {
		return assistantSession{}, errors.New("assistant session path is unavailable")
	}
	return assistantSession{TabID: tab.ID, SessionPath: path}, nil
}

// ensureAssistantTabProfile upgrades an already-loaded legacy Assistant tab in
// place. Old Assistant sessions were ordinary coding tabs; merely stamping the
// sidecar would leave their live Controller on the old system prompt until an
// app restart. Rebuilding an idle controller carries history forward with the
// fresh Assistant prompt. Repeated calls inspect the live prompt and are no-ops
// once the profile is active.
func (a *App) ensureAssistantTabProfile(tab *WorkspaceTab, assistantID string) error {
	if tab == nil {
		return errors.New("assistant session tab is unavailable")
	}
	a.mu.Lock()
	ctrl := tab.Ctrl
	if ctrl != nil && controllerHasActiveRuntimeWork(ctrl) {
		a.mu.Unlock()
		return rebuildControllerActiveWorkError("assistant profile")
	}
	tab.sessionKind = agent.SessionKindAssistant
	tab.assistantID = strings.TrimSpace(assistantID)
	a.mu.Unlock()
	if ctrl == nil || strings.Contains(systemPromptFrom(ctrl.History()), "long-running outcome executor") {
		return nil
	}

	tab.modelMu.Lock()
	defer tab.modelMu.Unlock()
	return a.applyModelForTabLocked(tab, tab.model)
}

// ensureAssistantSessionMeta stamps the durable Assistant identity onto a session
// sidecar. It is idempotent, so repeated calls on a restored session are safe.
func ensureAssistantSessionMeta(sessionPath, assistantID string) error {
	meta, err := agent.EnsureBranchMeta(sessionPath)
	if err != nil {
		return err
	}
	meta.SessionKind = agent.SessionKindAssistant
	if id := strings.TrimSpace(assistantID); id != "" {
		meta.AssistantID = id
	}
	return agent.SaveBranchMetaPreserveUpdated(sessionPath, meta)
}

// ensureAssistantBackgroundTab creates a fresh blank background session for an
// Assistant run and stamps the Assistant identity into its BranchMeta BEFORE the
// tab's controller builds, so boot.Build selects the Assistant system prompt on
// the first turn rather than only after a restore.
func (a *App) ensureAssistantBackgroundTab(run assistant.Run) (*WorkspaceTab, error) {
	scope := "global"
	workspaceRoot := ""
	if run.Scope == assistant.ScopeWorkspace {
		scope, workspaceRoot = "project", run.WorkspaceRoot
	}
	scope = strings.TrimSpace(scope)
	actualRoot := globalWorkspaceRoot()
	if scope == "project" {
		workspaceRoot = normalizeProjectRoot(workspaceRoot)
		if workspaceRoot == "" {
			return nil, fmt.Errorf("workspaceRoot is required")
		}
		actualRoot = workspaceRoot
		_ = addProject(workspaceRoot, "")
	} else {
		scope = "global"
		workspaceRoot = ""
	}
	topicID := newTopicID()
	if err := setTopicTitleWithSource(workspaceRoot, topicID, defaultTopicTitle, topicTitleSourceAuto); err != nil {
		return nil, err
	}
	_ = prependTopicInProjectsFile(workspaceRoot, topicID, false)

	sessionPath, err := createEmptySessionFile(desktopSessionDir(actualRoot), "")
	if err != nil {
		return nil, err
	}
	if err := ensureAssistantSessionMeta(sessionPath, run.AssistantID); err != nil {
		return nil, err
	}

	if _, err := a.openTopicTabWithActivation(scope, workspaceRoot, topicID, sessionPath, a.noActiveTab()); err != nil {
		return nil, err
	}
	tab := a.findTabBySessionRuntimeKey(sessionRuntimeKey(sessionPath))
	if tab == nil {
		return nil, fmt.Errorf("background assistant tab was not created")
	}
	return tab, nil
}

func (h appAssistantSessionHost) WaitReady(ctx context.Context, tabID string, timeout time.Duration) (assistantController, error) {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		tab, ctrl := h.app.tabAndCtrlByID(tabID)
		if ctrl != nil {
			return ctrl, nil
		}
		if tab != nil && tab.Ready && strings.TrimSpace(tab.StartupErr) != "" {
			return nil, fmt.Errorf("assistant session startup: %s", tab.StartupErr)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("assistant session startup timed out")
		case <-ticker.C:
		}
	}
}

func (h appAssistantSessionHost) TrySubmit(tabID, prompt string, policy assistant.Policy, grants []control.ToolGrant, claim func() bool, release func()) (bool, error) {
	tab, ctrl := h.app.tabAndCtrlByID(tabID)
	if tab == nil || ctrl == nil {
		return false, workspaceNotReadyErr(tab)
	}
	if err := h.app.applyPendingModelForTab(tab); err != nil {
		return false, err
	}
	tab, ctrl = h.app.tabAndCtrlByID(tabID)
	if tab == nil || ctrl == nil {
		return false, workspaceNotReadyErr(tab)
	}
	if err := h.app.ensureTabControllerWorkspace(tab); err != nil {
		return false, err
	}
	tab.reconcileMu.Lock()
	defer tab.reconcileMu.Unlock()
	tab, ctrl = h.app.tabAndCtrlByID(tabID)
	if ctrl == nil || tab.sink == nil {
		return false, workspaceNotReadyErr(tab)
	}
	tab.sink.assistantMu.Lock()
	defer tab.sink.assistantMu.Unlock()
	if !claim() {
		return false, nil
	}
	if !ctrl.TrySubmitUserTurnWithPolicy(prompt, prompt, buildAssistantPermissionPolicy(policy), control.ToolApprovalAuto, grants...) {
		release()
		return false, nil
	}
	h.app.ensureTabTopicIndexedForUserTurn(tab)
	h.app.maybeAutoTitleTopicFromText(tab, prompt)
	h.app.emitProjectTreeChanged()
	return true, nil
}

func (h appAssistantSessionHost) Cancel(tabID string) { h.app.CancelTab(tabID) }

type assistantTurnResult struct {
	Err          error
	Summary      string
	ProgressText string
	Attention    bool
	Action       string
	Tool         string
	Subject      string
	ResumeToken  string
}

type assistantInFlight struct {
	runID    string
	tabID    string
	done     chan assistantTurnResult
	once     sync.Once
	summary  string
	turnText string
	// required holds the deterministic capabilities this run must evidence
	// before it can be recorded as successful.
	required []assistant.Capability
	// evidence accumulates successful live tool results observed on the typed
	// event stream. A dispatch alone or a failed result never records evidence.
	evidence assistant.Evidence
}

func (f *assistantInFlight) complete(result assistantTurnResult) {
	f.once.Do(func() { f.done <- result })
}

// AssistantRuntime owns one process-local scheduler owner and executes claimed
// runs in inactive Desktop sessions. The Store lease remains the authority;
// this map only correlates Controller events while this process is alive.
type AssistantRuntime struct {
	store     *assistant.Store
	scheduler *assistant.Scheduler
	runner    *assistant.Runner
	host      assistantSessionHost

	mu       sync.Mutex
	inflight map[string]*assistantInFlight // tab ID -> run event correlation
	byRun    map[string]*assistantInFlight
	cancel   context.CancelFunc
	done     chan struct{}
	wake     chan struct{}
	wg       sync.WaitGroup
	running  atomic.Bool
	tick     time.Duration

	diagnosticMu sync.RWMutex
	diagnostics  []AssistantDiagnostic
}

func NewAssistantRuntime(app *App, root string) (*AssistantRuntime, error) {
	if app == nil {
		return nil, errors.New("assistant runtime requires an app")
	}
	store, err := assistant.NewStore(root)
	if err != nil {
		return nil, err
	}
	scheduler, err := assistant.NewScheduler(store)
	if err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("desktop:%d:%d", os.Getpid(), time.Now().UnixNano())
	runner, err := assistant.NewRunner(store, owner, assistantLeaseTTL)
	if err != nil {
		return nil, err
	}
	return &AssistantRuntime{
		store: store, scheduler: scheduler, runner: runner,
		host: appAssistantSessionHost{app: app}, inflight: map[string]*assistantInFlight{},
		byRun: map[string]*assistantInFlight{}, tick: assistantTickInterval,
		wake: make(chan struct{}, 1),
	}, nil
}

func (r *AssistantRuntime) Start() {
	if r == nil || !r.running.CompareAndSwap(false, true) {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	if r.wake == nil {
		r.wake = make(chan struct{}, 1)
	}
	r.cancel = cancel
	r.done = make(chan struct{})
	done := r.done
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer close(done)
		r.loop(ctx)
	}()
}

func (r *AssistantRuntime) Stop() {
	if r == nil || !r.running.CompareAndSwap(true, false) {
		return
	}
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	active := make([]*assistantInFlight, 0, len(r.byRun))
	for _, in := range r.byRun {
		active = append(active, in)
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, in := range active {
		r.host.Cancel(in.tabID)
	}
	if done != nil {
		<-done
	}
	r.wg.Wait()
}

func (r *AssistantRuntime) loop(ctx context.Context) {
	r.tickOnce(ctx)
	ticker := time.NewTicker(r.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
			r.tickOnce(ctx)
		case <-ticker.C:
			r.tickOnce(ctx)
		}
	}
}

// Wake requests an immediate scheduler/runner pass. Capacity one coalesces
// bursts from repeated idempotent UI calls without blocking the Wails thread.
func (r *AssistantRuntime) Wake() {
	if r == nil {
		return
	}
	r.mu.Lock()
	wake := r.wake
	r.mu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *AssistantRuntime) tickOnce(ctx context.Context) {
	now := time.Now()
	if result, err := r.scheduler.Tick(now); err != nil {
		r.recordDiagnostic("schedule", err)
		slog.Error("desktop: assistant schedule tick failed", "err", err, "failures", len(result.Failures))
	}
	for {
		acquired, err := r.runner.Acquire(time.Now())
		if err != nil {
			r.recordDiagnostic("acquire", err)
			slog.Error("desktop: assistant acquire failed", "err", err)
			return
		}
		for _, diagnostic := range acquired.Diagnostics {
			r.recordDiagnostic("recovery", errors.New(diagnostic))
			slog.Warn("desktop: assistant recovery diagnostic", "detail", diagnostic)
		}
		if acquired.Run == nil {
			return
		}
		run := *acquired.Run
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			r.execute(ctx, run)
		}()
	}
}

func (r *AssistantRuntime) recordDiagnostic(operation string, err error) {
	if r == nil || err == nil {
		return
	}
	r.diagnosticMu.Lock()
	r.diagnostics = append(r.diagnostics, AssistantDiagnostic{
		At: time.Now(), Operation: operation, Message: err.Error(),
	})
	if len(r.diagnostics) > 50 {
		r.diagnostics = append([]AssistantDiagnostic(nil), r.diagnostics[len(r.diagnostics)-50:]...)
	}
	r.diagnosticMu.Unlock()
}

func (r *AssistantRuntime) Diagnostics() []AssistantDiagnostic {
	if r == nil {
		return []AssistantDiagnostic{}
	}
	r.diagnosticMu.RLock()
	defer r.diagnosticMu.RUnlock()
	return append([]AssistantDiagnostic(nil), r.diagnostics...)
}

func (r *AssistantRuntime) execute(ctx context.Context, run assistant.Run) {
	defer r.Wake()
	if err := validateAssistantWorkspace(run); err != nil {
		if attentionErr := r.requestWorkspaceAttention(run, err); attentionErr != nil {
			r.recordDiagnostic("workspace_attention", errors.Join(err, attentionErr))
			slog.Error("desktop: persist assistant workspace attention failed", "run", run.ID, "err", attentionErr)
		}
		return
	}
	session, err := r.host.PrepareSession(run)
	if err != nil {
		r.failKnown(run, "session_prepare", err, true)
		return
	}
	_, err = r.host.WaitReady(ctx, session.TabID, assistantReadyTimeout)
	if err != nil {
		r.failKnown(run, "session_startup", err, true)
		return
	}
	if err := ctx.Err(); err != nil {
		r.failKnown(run, "runtime_stopped", err, true)
		return
	}
	bindRequest := fmt.Sprintf("bind-session:%s:%d", run.ID, run.LeaseFence)
	bound, err := r.runner.BindSession(run, bindRequest, session.SessionPath, time.Now())
	if err != nil {
		r.failKnown(run, "session_bind", err, true)
		return
	}
	run = *bound

	prompt, grants, selected, planRevision, err := r.promptFor(run)
	if err != nil {
		r.failKnown(run, "prompt_build", err, true)
		return
	}
	if err := ctx.Err(); err != nil {
		r.failKnown(run, "runtime_stopped", err, true)
		return
	}
	required := assistant.RequiredCapabilities(run.Mission, run.Prompt)
	in := &assistantInFlight{
		runID:    run.ID,
		tabID:    session.TabID,
		done:     make(chan assistantTurnResult, 1),
		required: required,
	}
	accepted, err := r.host.TrySubmit(session.TabID, prompt, run.Policy, grants, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.inflight[session.TabID] != nil || r.byRun[run.ID] != nil {
			return false
		}
		r.inflight[session.TabID], r.byRun[run.ID] = in, in
		return true
	}, func() {
		r.removeInFlight(in)
	})
	if err != nil {
		r.failKnown(run, "submit", err, true)
		return
	}
	if !accepted {
		r.failKnown(run, "session_busy", errors.New("assistant session already has an active turn"), true)
		return
	}
	defer r.removeInFlight(in)

	renew := time.NewTicker(assistantLeaseTTL / 2)
	defer renew.Stop()
	for {
		select {
		case <-ctx.Done():
			// Process shutdown intentionally leaves the lease to expire. Recovery
			// will surface the unknown outcome instead of guessing completion.
			return
		case result := <-in.done:
			if errors.Is(result.Err, context.Canceled) {
				return
			}
			if result.Attention {
				if strings.TrimSpace(result.ResumeToken) == "" {
					result.ResumeToken = fmt.Sprintf("resume:%s:%d", run.ID, run.LeaseFence)
				}
				_, requestErr := r.store.RequestApproval(assistant.ApprovalInput{
					RequestID: "attention:" + run.ID + ":" + fmt.Sprint(run.LeaseFence),
					RunID:     run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
					Action: result.Action, Summary: result.Summary, Tool: result.Tool, Subject: result.Subject,
					SessionPath: session.SessionPath,
					ResumeToken: result.ResumeToken, Now: time.Now(),
				})
				if requestErr != nil {
					r.recordDiagnostic("attention", requestErr)
					slog.Error("desktop: persist assistant attention failed", "run", run.ID, "err", requestErr)
					_, failErr := r.runner.Fail(run, assistant.Failure{
						Code: "attention_persist_failed", Message: requestErr.Error(), Retryable: false,
						OutcomeKnown: false, Now: time.Now(),
					})
					if failErr != nil {
						slog.Error("desktop: persist assistant unknown outcome failed", "run", run.ID, "err", failErr)
					}
				}
				r.host.Cancel(session.TabID)
				return
			}
			if result.Err != nil {
				_, failErr := r.runner.Fail(run, assistant.Failure{
					Code: "turn_failed", Message: result.Err.Error(), Retryable: false,
					OutcomeKnown: false, Now: time.Now(),
				})
				if failErr != nil {
					r.recordDiagnostic("turn_failure", failErr)
					slog.Error("desktop: persist assistant failure failed", "run", run.ID, "err", failErr)
				}
				return
			}
			// Validate required-capability evidence before accepting success. A
			// missing successful live tool result means the model skipped, was
			// denied, or the tool failed — never a successful Run.
			if missing := in.evidence.Missing(in.required); len(missing) > 0 {
				r.failEvidence(run, missing)
				r.host.Cancel(session.TabID)
				return
			}
			r.completeRun(run, selected, planRevision, result, session.SessionPath)
			return
		case <-renew.C:
			renewed, renewErr := r.runner.Renew(run, time.Now())
			if renewErr != nil {
				r.recordDiagnostic("renew", renewErr)
				slog.Error("desktop: renew assistant lease failed", "run", run.ID, "err", renewErr)
				r.host.Cancel(session.TabID)
				return
			}
			run = *renewed
		}
	}
}

func validateAssistantWorkspace(run assistant.Run) error {
	if run.Scope != assistant.ScopeWorkspace {
		return nil
	}
	root := strings.TrimSpace(run.WorkspaceRoot)
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("frozen workspace path is invalid: %q", run.WorkspaceRoot)
	}
	info, err := os.Stat(root)
	if err != nil {
		return fmt.Errorf("frozen workspace is unavailable: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("frozen workspace is not a directory: %s", root)
	}
	return nil
}

func (r *AssistantRuntime) requestWorkspaceAttention(run assistant.Run, cause error) error {
	_, err := r.store.RequireAttention(assistant.RequireAttentionInput{
		RequestID: fmt.Sprintf("workspace-attention:%s:%d", run.ID, run.LeaseFence),
		RunID:     run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence,
		Action:      "cancel_recreate",
		Summary:     fmt.Sprintf("工作区不可用：%v。旧 Run 的冻结工作区不可修改；请取消它，重新绑定 Assistant 后新建 Run。", cause),
		ResumeToken: fmt.Sprintf("cancel-recreate:%s:%d", run.ID, run.LeaseFence), Now: time.Now(),
	})
	return err
}

func (r *AssistantRuntime) failKnown(run assistant.Run, code string, err error, retryable bool) {
	if err == nil {
		return
	}
	r.recordDiagnostic(code, err)
	_, persistErr := r.runner.Fail(run, assistant.Failure{
		Code: code, Message: err.Error(), Retryable: retryable,
		OutcomeKnown: true, RetryAfter: time.Minute, Now: time.Now(),
	})
	if persistErr != nil {
		slog.Error("desktop: persist assistant setup failure failed", "run", run.ID, "cause", err, "err", persistErr)
	}
}

// completeRun records a successful turn together with any progress patch the
// model emitted. Run completion and plan/artifact changes commit atomically; a
// malformed progress block rejects the whole result and moves the run to an
// observable, retryable failure instead of silently dropping it.
func (r *AssistantRuntime) completeRun(run assistant.Run, selected *assistant.Responsibility, planRevision int64, result assistantTurnResult, sessionPath string) {
	summary := strings.TrimSpace(assistant.StripProgressBlocks(result.Summary))
	blocks, parseErrs := assistant.ParseProgressBlocks(result.ProgressText)
	if len(parseErrs) > 0 {
		for _, perr := range parseErrs {
			r.recordDiagnostic("progress_parse", perr)
			slog.Warn("desktop: assistant progress block parse failed", "run", run.ID, "err", perr)
		}
		r.failProgress(run, "progress_parse_failed", errors.Join(parseErrs...), true)
		return
	}
	selectedID := ""
	if selected != nil {
		selectedID = selected.ID
	}
	input := assistant.CompleteRunInput{
		RequestID:        fmt.Sprintf("progress:%s:%d", run.ID, run.LeaseFence),
		RunID:            run.ID,
		LeaseOwner:       run.LeaseOwner,
		LeaseFence:       run.LeaseFence,
		Summary:          summary,
		SessionPath:      sessionPath,
		ResponsibilityID: selectedID,
		Now:              time.Now(),
	}
	if len(blocks) > 0 {
		input.Progress = assistant.MergeProgressBlocks(blocks)
		input.Progress.PlanRevision = planRevision
	}
	if _, err := r.store.CompleteRunWithProgress(input); err != nil {
		r.recordDiagnostic("progress_apply", err)
		slog.Error("desktop: apply assistant progress failed", "run", run.ID, "err", err)
		// The next attempt is a fresh model turn and can repair a stale revision,
		// bad alias or invalid dependency patch. Runner retry limits keep this
		// bounded while preserving an explicit recovery path.
		r.failProgress(run, "progress_apply_failed", err, true)
	}
}

// failProgress persists a progress parse/apply failure with the run's lease
// released. Retryable failures return to retry_wait with a bounded delay, so a
// transient conflict or a malformed block can be recovered on the next attempt
// without leaving a half-applied plan.
func (r *AssistantRuntime) failProgress(run assistant.Run, code string, err error, retryable bool) {
	_, failErr := r.runner.Fail(run, assistant.Failure{
		Code: code, Message: err.Error(), Retryable: retryable,
		OutcomeKnown: true, RetryAfter: time.Minute, Now: time.Now(),
	})
	if failErr != nil {
		r.recordDiagnostic(code, failErr)
		slog.Error("desktop: persist assistant progress failure failed", "run", run.ID, "err", failErr)
	}
}

// failEvidence persists a missing-capability failure with a bounded retry. It
// keeps the run recoverable and observable: the next attempt re-runs the turn so
// the model can obtain the required successful tool result. No success or
// progress completion is applied.
func (r *AssistantRuntime) failEvidence(run assistant.Run, missing []assistant.Capability) {
	failure := assistant.EvidenceFailure(missing)
	failure.Now = time.Now()
	failure.RetryAfter = time.Minute
	r.recordDiagnostic(failure.Code, errors.New(failure.Message))
	_, failErr := r.runner.Fail(run, failure)
	if failErr != nil {
		r.recordDiagnostic(failure.Code, failErr)
		slog.Error("desktop: persist assistant evidence failure failed", "run", run.ID, "err", failErr)
	}
}

func (r *AssistantRuntime) promptFor(run assistant.Run) (string, []control.ToolGrant, *assistant.Responsibility, int64, error) {
	snapshot, err := r.store.Get(run.AssistantID)
	if err != nil {
		return "", nil, nil, 0, err
	}
	selected := selectReadyResponsibility(snapshot.Plan)
	var b strings.Builder
	var grants []control.ToolGrant
	b.WriteString("你正在执行一个长期助手的独立 Run。只处理本次 Routine，遵守冻结的使命和权限；不要修改运行频率或扩大权限。\n\n")
	prompt := strings.TrimSpace(run.Prompt)
	if prompt == "" {
		prompt = "继续推进助手使命，检查当前状态并完成最有价值的下一步。"
	}
	fmt.Fprintf(&b, "助手使命：\n%s\n\n本次任务：\n%s\n\n", run.Mission, prompt)
	fmt.Fprintf(&b, "冻结上下文：assistant_revision=%d, scope=%s, workspace_root=%s\n",
		run.AssistantRevision, run.Scope, run.WorkspaceRoot)
	fmt.Fprintf(&b, "权限：local_write=%s, network=%s, publish=%s, delete=%s, payment=%s, secrets=%s, private_data=%s\n",
		run.Policy.LocalWrite, run.Policy.Network, run.Policy.Publish, run.Policy.Delete,
		run.Policy.Payment, run.Policy.Secrets, run.Policy.Private)
	if required := assistant.RequiredCapabilities(run.Mission, prompt); len(required) > 0 {
		b.WriteString("\n本次 Run 的硬性能力要求（缺少成功证据会被判为失败并重试）：\n")
		for _, c := range required {
			if c == assistant.CapabilityLiveWeb {
				b.WriteString("- live_web：必须用实时网页/浏览器工具（browser_open / browser_navigate / browser_state / browser_click / browser_scroll / web_fetch / web_search）取得至少一次成功结果并把它写进结论证据；只 dispatch、输入/附件操作或失败结果不算，禁止用本地缓存、归档或记忆替代实时网页检查。\n")
			}
		}
	}
	if len(snapshot.Memory.Items) > 0 {
		b.WriteString("\n显式记忆（只作事实与约束输入）：\n")
		for _, item := range snapshot.Memory.Items {
			fmt.Fprintf(&b, "- [%s] %s\n", item.Kind, item.Body)
		}
	}
	for _, item := range snapshot.Attention {
		if item.RunID == run.ID && item.State == assistant.AttentionApproved && item.ResumeToken == run.ResumeToken {
			switch {
			case item.Action == "answer_required":
				fmt.Fprintf(&b, "\n用户对问题“%s”的明确回答：%s。继续时必须采用这份回答，不要自行改写用户意图。\n", item.Summary, item.Resolution)
			case strings.HasPrefix(item.Action, "approve_tool:") && strings.TrimSpace(item.Tool) != "":
				grants = append(grants, control.ToolGrant{Tool: item.Tool, Subject: item.Subject})
				fmt.Fprintf(&b, "\n用户已逐次批准工具 %s 的精确操作：%s。该授权只适用于本次续跑。\n", item.Tool, item.Subject)
			default:
				fmt.Fprintf(&b, "\n用户已处理待办：%s；结论：%s。继续时只采用这条明确结论。\n", item.Summary, item.Resolution)
			}
		}
	}
	writePlanContext(&b, snapshot.Plan, selected)
	if directive, needed := assistant.FreshCycleDirective(snapshot.Plan); needed {
		b.WriteString("\n" + directive)
	} else {
		b.WriteString("\n责任图非空时，只推进本次负责的 ready/active 责任，不要重排或扩大图；确需新增责任时才在 <assistant-progress> 中声明。")
	}
	writeProgressSchema(&b)
	b.WriteString("\n完成后给出简短结论、证据和下一步。对权限内可恢复的操作直接执行，不要请求确认；只有命中显式审批边界或确实需要用户拥有的决定时才提出，不要猜测授权。")
	return b.String(), grants, selected, snapshot.Plan.Revision, nil
}

// writeProgressSchema appends a bounded, concrete example of the
// <assistant-progress> protocol so the model emits well-formed patches.
func writeProgressSchema(b *strings.Builder) {
	b.WriteString(`

<assistant-progress> 块是单个 JSON 对象，字段如下（除 alias/objective 外都可省略）：

{
  "plan_revision": 3,
  "responsibility": "code-review",
  "responsibilities": [
    {"alias": "fix-tests", "objective": "修复失败用例", "done_criteria": "全部通过", "next_action": "运行 go test", "depends_on": ["scan"]}
  ],
  "complete": ["scan"],
  "active": ["fix-tests"],
  "artifacts": [{"resp": "scan", "title": "扫描报告", "kind": "report", "content": "…", "evidence": "…"}],
  "opportunities": [{"resp": "fix-tests", "reason": "下游已就绪"}]
}

depends_on 用 alias 引用，省略表示不变，[] 表示清空；同一块内可前向引用，禁止自依赖与环。responsibility 填本次实际推进的 alias，complete/active 填 alias 列表。`)
}

// selectReadyResponsibility deterministically picks the one responsibility a
// run works on: an already-active one wins, otherwise the first ready one in
// creation order.
func selectReadyResponsibility(plan assistant.Plan) *assistant.Responsibility {
	for i := range plan.Responsibilities {
		if plan.Responsibilities[i].Status == assistant.RespActive {
			r := plan.Responsibilities[i]
			return &r
		}
	}
	for i := range plan.Responsibilities {
		if plan.Responsibilities[i].Status == assistant.RespReady {
			r := plan.Responsibilities[i]
			return &r
		}
	}
	return nil
}

func writePlanContext(b *strings.Builder, plan assistant.Plan, selected *assistant.Responsibility) {
	if len(plan.Responsibilities) == 0 {
		return
	}
	b.WriteString("\n当前责任图（按别名引用）：\n")
	aliasOf := map[string]string{}
	for _, r := range plan.Responsibilities {
		if r.Alias != "" {
			aliasOf[r.ID] = r.Alias
		}
	}
	for _, r := range plan.Responsibilities {
		label := r.Alias
		if label == "" {
			label = r.ID
		}
		status := string(r.Status)
		fmt.Fprintf(b, "- %s [%s] %s", label, status, r.Objective)
		if strings.TrimSpace(r.DoneCriteria) != "" {
			fmt.Fprintf(b, "（完成标准：%s）", strings.TrimSpace(r.DoneCriteria))
		}
		if strings.TrimSpace(r.NextAction) != "" {
			fmt.Fprintf(b, "（下一步：%s）", strings.TrimSpace(r.NextAction))
		}
		if len(r.DependsOn) > 0 {
			deps := make([]string, 0, len(r.DependsOn))
			for _, dep := range r.DependsOn {
				if a, ok := aliasOf[dep]; ok {
					deps = append(deps, a)
				} else {
					deps = append(deps, dep)
				}
			}
			fmt.Fprintf(b, "（依赖：%s）", strings.Join(deps, ", "))
		}
		if strings.TrimSpace(r.BlockReason) != "" {
			fmt.Fprintf(b, "（阻塞原因：%s）", strings.TrimSpace(r.BlockReason))
		}
		b.WriteString("\n")
	}
	if selected != nil {
		label := selected.Alias
		if label == "" {
			label = selected.ID
		}
		fmt.Fprintf(b, "\n本次负责：%s（%s）。只推进这一项，完成后在 <assistant-progress> 中声明 complete。\n", label, selected.Objective)
	}
}

func buildAssistantPermissionPolicy(policy assistant.Policy) permission.Policy {
	safeLocalWrites := []string{"write_file", "edit_file", "multi_edit", "notebook_edit"}
	localAll := []string{
		"write_file", "edit_file", "multi_edit", "notebook_edit",
		"move_file", "delete_range", "delete_symbol", "bash",
	}
	networkTools := []string{
		"web_fetch", "web_search", "browser_open", "browser_navigate", "browser_state", "browser_scroll",
		"browser_tab", "browser_close", "browser_click", "browser_type", "browser_upload", "browser_attach",
	}
	networkAllow := []string{
		"web_fetch", "web_search", "browser_open", "browser_navigate", "browser_state", "browser_scroll", "browser_tab", "browser_close",
	}
	allow := make([]string, 0, len(safeLocalWrites)+len(networkAllow))
	deny := make([]string, 0, len(localAll)+len(networkTools))
	if policy.LocalWrite == assistant.AccessAllow {
		allow = append(allow, safeLocalWrites...)
	} else if policy.LocalWrite == assistant.AccessDeny {
		deny = append(deny, localAll...)
	}
	if policy.Network == assistant.AccessAllow {
		allow = append(allow, networkAllow...)
	} else if policy.Network == assistant.AccessDeny {
		deny = append(deny, networkTools...)
		deny = append(deny, "mcp__*")
	}
	// Ask has precedence over Allow. These cover destructive local operations,
	// shell escape hatches and browser actions that can publish or disclose data.
	ask := []string{
		"bash", "delete_range", "delete_symbol",
		"browser_click", "browser_type", "browser_upload", "browser_attach",
		"mcp__*",
	}
	if policy.Network == assistant.AccessApprove {
		ask = append(ask, networkTools...)
	}
	// Publish/Delete/Payment/Secrets/Private are deliberately never translated
	// into Allow or Deny rules here: every concrete writer remains Ask even when
	// those Assistant fields are configured as allow.
	return permission.New("ask", allow, ask, deny)
}

func (r *AssistantRuntime) removeInFlight(in *assistantInFlight) {
	r.mu.Lock()
	if r.inflight[in.tabID] == in {
		delete(r.inflight, in.tabID)
	}
	if r.byRun[in.runID] == in {
		delete(r.byRun, in.runID)
	}
	r.mu.Unlock()
}

func (r *AssistantRuntime) ObserveEvent(tabID string, value event.Event) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	in := r.inflight[tabID]
	if in == nil {
		r.mu.Unlock()
		return false
	}
	switch value.Kind {
	case event.Text:
		// Raw answer deltas carry the <assistant-progress> protocol block. The
		// final Message is stripped for display, so the runner reconstructs the
		// raw protocol from these deltas instead.
		if len(in.turnText) < 1<<20 {
			in.turnText += value.Text
		}
		r.mu.Unlock()
		return false
	case event.Message:
		if strings.TrimSpace(value.Text) != "" {
			in.summary = strings.TrimSpace(value.Text)
		}
		r.mu.Unlock()
		return false
	case event.ToolResult:
		// Only a successful result counts as capability evidence. A dispatch
		// alone (ToolDispatch) or a failed/denied result records nothing.
		in.evidence.RecordToolResult(value.Tool.Name, value.Tool.Err == "")
		r.mu.Unlock()
		return false
	case event.ApprovalRequest:
		tool := strings.TrimSpace(value.Approval.Tool)
		action := "approve_tool"
		if tool != "" {
			action += ":" + tool
		}
		summary := strings.TrimSpace(value.Approval.Subject)
		if summary == "" {
			summary = strings.TrimSpace(value.Approval.Summary)
		} else if detail := strings.TrimSpace(value.Approval.Summary); detail != "" && detail != summary {
			summary += " — " + detail
		}
		if summary == "" {
			summary = "助手执行需要用户审批"
		}
		token := strings.TrimSpace(value.Approval.ID)
		r.mu.Unlock()
		in.complete(assistantTurnResult{
			Attention: true, Action: action, Summary: summary,
			Tool: tool, Subject: strings.TrimSpace(value.Approval.Subject), ResumeToken: token,
		})
		return true
	case event.AskRequest:
		summary := "助手执行需要用户输入"
		if len(value.Ask.Questions) > 0 && strings.TrimSpace(value.Ask.Questions[0].Prompt) != "" {
			summary = strings.TrimSpace(value.Ask.Questions[0].Prompt)
		}
		token := strings.TrimSpace(value.Ask.ID)
		r.mu.Unlock()
		in.complete(assistantTurnResult{Attention: true, Action: "answer_required", Summary: summary, ResumeToken: token})
		return true
	case event.TurnDone:
		summary := in.summary
		if summary == "" && value.Err == nil {
			summary = "助手已完成本次运行"
		}
		progressText := in.turnText
		r.mu.Unlock()
		in.complete(assistantTurnResult{Err: value.Err, Summary: summary, ProgressText: progressText})
		return false
	default:
		r.mu.Unlock()
		return false
	}
}

func (r *AssistantRuntime) CancelRun(runID string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	in := r.byRun[runID]
	if in != nil {
		delete(r.byRun, runID)
		if r.inflight[in.tabID] == in {
			delete(r.inflight, in.tabID)
		}
	}
	r.mu.Unlock()
	if in != nil {
		in.complete(assistantTurnResult{Err: context.Canceled})
		r.host.Cancel(in.tabID)
	}
}
