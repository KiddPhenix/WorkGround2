package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/assistantchannel"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/netclient"
	"workground2/internal/permission"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/tool/assistanttool"
	"workground2/internal/tool/sessiontool"
)

const (
	assistantTickInterval = 30 * time.Second
)

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
	meta.SessionSource = agent.SessionSourceAssist
	meta.ToolApprovalMode = control.ToolApprovalAuto
	if id := strings.TrimSpace(assistantID); id != "" {
		meta.AssistantID = id
	}
	return agent.SaveBranchMetaPreserveUpdated(sessionPath, meta)
}

func assistantRunTopicTitle(run assistant.Run) string {
	if title := topicTitleFromUserText(run.Prompt); title != "" {
		return title
	}
	if title := topicTitleFromUserText(run.Mission); title != "" {
		return title
	}
	return defaultTopicTitle
}

func isLegacyAssistantTopicTitle(title string) bool {
	title = strings.TrimSpace(title)
	if title == "" || title == defaultTopicTitle {
		return true
	}
	lower := strings.ToLower(title)
	return strings.HasPrefix(title, "你正在执行一个") || strings.HasPrefix(lower, "you are executing a")
}

// reconcileAssistantSessionTitles repairs titles produced from the Assistant's
// internal execution envelope. Manual/custom titles are authoritative and are
// never replaced. The repair is safe to repeat on every AssistantGet.
func (a *App) reconcileAssistantSessionTitles(snapshot assistant.Snapshot) {
	changed := false
	for _, run := range snapshot.Runs {
		updated, err := a.reconcileAssistantSessionTitle(run)
		if err != nil {
			slog.Warn("assistant session title reconciliation failed", "assistant_id", run.AssistantID, "run_id", run.ID, "error", err)
			continue
		}
		changed = changed || updated
	}
	if changed {
		a.invalidatePromptHistoryCache()
		a.emitProjectTreeChanged()
	}
}

func (a *App) reconcileAssistantSessionTitle(run assistant.Run) (bool, error) {
	sessionPath := strings.TrimSpace(run.SessionPath)
	desired := assistantRunTopicTitle(run)
	if sessionPath == "" || desired == defaultTopicTitle {
		return false, nil
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		return false, err
	}
	if strings.TrimSpace(meta.TopicID) == "" || strings.TrimSpace(meta.CustomTitle) != "" {
		return false, nil
	}
	if owner := strings.TrimSpace(meta.AssistantID); owner != "" && owner != strings.TrimSpace(run.AssistantID) {
		return false, nil
	}
	if meta.SessionKind != agent.SessionKindAssistant && strings.TrimSpace(meta.AssistantID) == "" {
		return false, nil
	}

	titleRoot := ""
	if meta.Scope == "project" || (strings.TrimSpace(meta.Scope) == "" && run.Scope == assistant.ScopeWorkspace) {
		titleRoot = normalizeProjectRoot(meta.WorkspaceRoot)
		if titleRoot == "" {
			titleRoot = normalizeProjectRoot(run.WorkspaceRoot)
		}
		if titleRoot == "" {
			return false, nil
		}
	}
	if loadTopicTitleSource(titleRoot, meta.TopicID) == topicTitleSourceManual {
		return false, nil
	}
	current := strings.TrimSpace(loadTopicTitle(titleRoot, meta.TopicID))
	if current == "" {
		current = strings.TrimSpace(meta.TopicTitle)
	}
	if !isLegacyAssistantTopicTitle(current) || current == desired {
		return false, nil
	}
	if err := setTopicTitleWithSource(titleRoot, meta.TopicID, desired, topicTitleSourceAuto); err != nil {
		return false, err
	}
	meta.TopicTitle = desired
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, meta); err != nil {
		return false, err
	}
	a.updateOpenTopicTitle(meta.TopicID, desired)
	return true, nil
}

// ensureAssistantBackgroundTab creates a fresh blank background session for an
// Assistant run and stamps the Assistant identity into its BranchMeta BEFORE the
// tab's controller builds, so boot.Build selects the Assistant system prompt on
// the first turn rather than only after a restore.
func (a *App) ensureAssistantBackgroundTab(run assistant.Run, sessionPath string) (*WorkspaceTab, error) {
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
	if err := setTopicTitleWithSource(workspaceRoot, topicID, assistantRunTopicTitle(run), topicTitleSourceAuto); err != nil {
		return nil, err
	}
	_ = prependTopicInProjectsFile(workspaceRoot, topicID, false)

	if strings.TrimSpace(sessionPath) == "" {
		var err error
		sessionPath, err = createEmptySessionFile(desktopSessionDir(actualRoot), "")
		if err != nil {
			return nil, err
		}
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
	a.SetToolApprovalModeForTab(tab.ID, control.ToolApprovalAuto)
	return tab, nil
}

// AssistantRuntime owns one process-local supervisor loop. Execution state lives
// exclusively in the shared Session subsystem: the loop creates managed Sessions
// (for routine fires, task Dispatches and supervisor advance decisions) and
// never claims or writes Runs/Jobs.
type AssistantRuntime struct {
	app         *App
	store       *assistant.Store
	scheduler   *assistant.Scheduler
	dispatcher  *assistant.Dispatcher
	reflector   *assistant.Reflector
	ideator     *assistant.Ideator
	channels    *assistantchannel.Service
	leader      *assistant.LeaderElector
	leaderLease assistant.LeaderLease
	viewport    *assistant.Viewport
	autoAnswer  *assistant.AutoAnswer
	// executor is the shared supervisor Session executor (atomic supervisor
	// Session creation, durable event queue, real Controller turns). Desktop
	// and daemon drive the same core.
	executor *assistant.SupervisorExecutor

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	wake    chan struct{}
	wg      sync.WaitGroup
	running atomic.Bool
	tick    time.Duration

	// sessionControl is the adapter used to create/steer managed sessions. It
	// defaults to the desktop app adapter; tests inject a recording fake.
	sessionControl sessiontool.SessionControl

	// trialStatus resolves an experiment trial fork session's derived status for
	// the winner sweep. It defaults to the Session subsystem (agent.ListSessions
	// + DeriveSessionStatus); tests inject a fake.
	trialStatus assistant.TrialStatusResolver

	// dispatchSeq gives every assistant:dispatch-stream event a monotonic,
	// runtime-global sequence so the frontend can drop stale/out-of-order deltas.
	dispatchSeq atomic.Int64

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
	migrateAssistantRoleSessions(app, store)
	scheduler, err := assistant.NewScheduler(store)
	if err != nil {
		return nil, err
	}
	roleModel := assistantRoleModel{app: app}
	autoAnswer, err := assistant.NewAutoAnswer(roleModel)
	if err != nil {
		return nil, err
	}
	dispatcher, err := assistant.NewDispatcher(store, roleModel)
	if err != nil {
		return nil, err
	}
	reflector, err := assistant.NewReflector(store, roleModel)
	if err != nil {
		return nil, err
	}
	ideator, err := assistant.NewIdeator(store, roleModel)
	if err != nil {
		return nil, err
	}
	leader, err := assistant.NewLeaderElector(root, assistantOwner("desktop"), 90*time.Second)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	httpClient, err := netclient.NewHTTPClient(cfg.NetworkProxySpec(), netclient.TransportOptions{})
	if err != nil {
		return nil, err
	}
	channels, err := assistantchannel.New(store, func(key string) string { return config.ResolveCredential(key).Value }, assistantchannel.NewDiscourse(httpClient))
	if err != nil {
		return nil, err
	}
	r := &AssistantRuntime{
		app: app, store: store, scheduler: scheduler,
		dispatcher: dispatcher, reflector: reflector, ideator: ideator,
		channels: channels, leader: leader, tick: assistantTickInterval,
		wake:       make(chan struct{}, 1),
		viewport:   assistant.NewViewport(),
		autoAnswer: autoAnswer,
	}
	// Build the shared supervisor executor AFTER r exists: its hooks close over
	// the runtime so sessionControl/autoAnswer/trialStatus resolve lazily (test
	// fakes can be injected later).
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		return nil, err
	}
	executor, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:  store,
		Events: events,
		Host:   &desktopSupervisorHost{r: r},
		Control: func() assistant.SessionControl {
			return supervisorSessionControl{inner: r.sessionCreator()}
		},
		Ideator:    ideator,
		AutoAnswer: func() *assistant.AutoAnswer { return r.autoAnswer },
		TrialStatus: func() assistant.TrialStatusResolver {
			if r.trialStatus != nil {
				return r.trialStatus
			}
			return r.trialSessionStatus
		},
		Viewport:   r.CurrentViewport,
		Diagnostic: r.recordDiagnostic,
		Wake:       r.Wake,
		Constraints: func(assistantID string) (string, int64) {
			if store == nil {
				return "", 0
			}
			snap, err := store.Get(assistantID)
			if err != nil {
				return "", 0
			}
			return assistant.LoadProjectConstraintsSummary(snap.Assistant.WorkspaceRoot)
		},
	})
	if err != nil {
		return nil, err
	}
	r.executor = executor
	dispatcher.SetReplyObserver(r.emitDispatchPreview)
	return r, nil
}

// assistantOwner derives a process-local scheduler owner ID. Runs/Jobs are no
// longer claimed, but the leader elector still needs a stable owner string.
func assistantOwner(kind string) string {
	return fmt.Sprintf("%s:%d:%d", kind, os.Getpid(), time.Now().UnixNano())
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
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	r.wg.Wait()
	if r.leader != nil && r.leaderLease.Fence != "" {
		if err := r.leader.Release(r.leaderLease); err != nil && !errors.Is(err, assistant.ErrLeaderLost) {
			r.recordDiagnostic("leader_release", err)
		}
		r.leaderLease = assistant.LeaderLease{}
	}
}

// sessionCreator returns the SessionControl adapter for managed-session writes.
// Production uses the app adapter; tests may inject a recording fake.
func (r *AssistantRuntime) sessionCreator() sessiontool.SessionControl {
	if r == nil {
		return nil
	}
	if r.sessionControl != nil {
		return r.sessionControl
	}
	if r.app != nil {
		return &appAssistantSessionControl{app: r.app, store: r.store}
	}
	return nil
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

// Wake requests an immediate supervisor pass. Capacity one coalesces
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

// WorkControl returns the current global work gate with host-observed active
// work and a next-step hint.
func (r *AssistantRuntime) WorkControl() (AssistantWorkControlView, error) {
	store, err := r.requireStore()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	wc, err := store.WorkControl()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	var active []AssistantActiveWork
	if r.app != nil {
		active = r.app.activeHostWork()
	}
	hint := ""
	switch wc.State {
	case assistant.WorkQuiescing:
		hint = "waiting for active sessions to quiesce"
	case assistant.WorkPaused:
		hint = "all work paused; resume to continue"
	case assistant.WorkRecovering:
		hint = "recovering interrupted sessions"
	case assistant.WorkRunning:
		hint = "running"
	}
	return workControlView(wc, active, hint), nil
}

// PauseAll quiesces every active Session (ordinary tabs, managed and supervisor
// Assistant sessions): it first raises the persistent epoch/fence, requests
// cancellation of all running controllers so they checkpoint at a safe point,
// and only confirms PAUSED once the host observes global silence. On timeout it
// keeps QUIESCING and returns the still-active objects plus an explicit error —
// it never claims PAUSED while work is still running. Idempotent by request ID.
func (r *AssistantRuntime) PauseAll(requestID string) (AssistantWorkControlView, error) {
	store, err := r.requireStore()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	wc, err := store.PauseAll(requestID, time.Now())
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	// Notify and cancel every active live controller so they checkpoint at a
	// safe point; then wait (bounded) for global silence before entering PAUSED.
	if r.app != nil {
		r.app.quiesceHostWork()
		if !r.app.hostWorkQuiet(5 * time.Second) {
			active := r.app.activeHostWork()
			return workControlView(wc, active, "still waiting for active sessions to quiesce"),
				fmt.Errorf("pause_all: %d session(s) still active after timeout; state %s", len(active), wc.State)
		}
	}
	r.Wake()
	done, err := store.CompletePause("complete:"+requestID, time.Now())
	if err != nil {
		return workControlView(wc, nil, ""), err
	}
	return workControlView(done, nil, "all work paused"), nil
}

// ResumeAll moves PAUSED/QUIESCING to RECOVERING, scans interrupted Sessions
// and safely retryable work, then completes back to RUNNING. It is idempotent:
// replaying while already RUNNING is a no-op that returns the current gate.
func (r *AssistantRuntime) ResumeAll(requestID string) (AssistantWorkControlView, error) {
	store, err := r.requireStore()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	// Replay while already RUNNING is a no-op: return the current gate.
	if wc, err := store.WorkControl(); err == nil && wc.State == assistant.WorkRunning {
		return workControlView(wc, nil, "already running"), nil
	}
	wc, err := store.ResumeAll(requestID, time.Now())
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	// Recovery scan: re-drive interrupted sessions from their checkpoint and
	// restore supervisor subscriptions. The loop wake re-runs the supervisor
	// pass (fires, dispatches, events) once the gate is RUNNING again.
	recovered := []AssistantActiveWork{}
	if r.app != nil {
		recovered = r.app.resumeHostWork()
	}
	r.Wake()
	done, err := store.CompleteResume("complete:"+requestID, time.Now())
	if err != nil {
		return workControlView(wc, recovered, "recovery incomplete"), err
	}
	return workControlView(done, recovered, "work resumed"), nil
}

// PauseForRestart quiesces work and records a one-shot restart intent, then
// enters PAUSED. The next process consumes the intent and auto-recovers.
func (r *AssistantRuntime) PauseForRestart(requestID string) (AssistantWorkControlView, error) {
	store, err := r.requireStore()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	if r.app != nil {
		r.app.quiesceHostWork()
		if !r.app.hostWorkQuiet(5 * time.Second) {
			wc, _ := store.WorkControl()
			active := r.app.activeHostWork()
			return workControlView(wc, active, "still waiting for active sessions to quiesce"),
				fmt.Errorf("pause_for_restart: %d session(s) still active after timeout; state %s", len(active), wc.State)
		}
	}
	if _, err := store.PauseForRestart(requestID, time.Now()); err != nil {
		return AssistantWorkControlView{}, err
	}
	done, err := store.CompletePause("complete:"+requestID, time.Now())
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	return workControlView(done, nil, "safe restart armed"), nil
}

// recordSupervisorCycles persists one bounded supervisor cycle per active
// Assistant per tick: the observed Plan/Assistant/Memory/WorkControl revisions
// plus a derived next step. It is the durable checkpoint the supervisor loop
// resumes from after a crash or leader switch, and the fence is monotonic per
// Assistant so a stale cycle can never overwrite a newer one.
func (r *AssistantRuntime) recordSupervisorCycles(now time.Time) {
	if r == nil {
		return
	}
	assistants, err := r.store.List()
	if err != nil {
		r.recordDiagnostic("cycle_list", err)
		return
	}
	wc, err := r.store.WorkControl()
	if err != nil {
		r.recordDiagnostic("cycle_workcontrol", err)
		return
	}
	for _, a := range assistants {
		if a.Lifecycle != assistant.LifecycleActive {
			continue
		}
		snapshot, err := r.store.Get(a.ID)
		if err != nil {
			r.recordDiagnostic("cycle_snapshot", err)
			continue
		}
		cycle, err := r.store.OpenCycle(assistant.OpenCycleInput{
			AssistantID: a.ID,
			RequestID:   assistant.StableID("request", "cycle/"+a.ID+"/"+fmt.Sprint(now.UnixNano())),
			Observed: assistant.CycleObservation{
				PlanRevision:      snapshot.Plan.Revision,
				AssistantRevision: snapshot.Assistant.Revision,
				MemoryRevision:    snapshot.Memory.Revision,
				WorkEpoch:         wc.Epoch,
			},
			Now: now,
		})
		if err != nil {
			r.recordDiagnostic("cycle_open", err)
			continue
		}
		if _, err := r.store.CheckpointCycle(assistant.CheckpointCycleInput{
			AssistantID: a.ID, CycleID: cycle.ID,
			RequestID: assistant.StableID("request", "cycle-checkpoint/"+cycle.ID+"/"+fmt.Sprint(cycle.Fence)),
			Fence:     cycle.Fence,
			NextStep:  r.supervisorNextStep(snapshot, now),
			Now:       now,
		}); err != nil {
			r.recordDiagnostic("cycle_checkpoint", err)
		}
		r.writebackManagedSessions(a.ID)
	}
}

// writebackManagedSessions polls completed managed Sessions and applies their
// <assistant-progress> to the plan via RecordSessionTranscript, then marks them
// completed. It is poll-based (runs on the tick), idempotent, and leaves the
// event hot path untouched; a still-running Session is skipped until its next
// idle observation.
func (r *AssistantRuntime) writebackManagedSessions(assistantID string) {
	if r.app == nil || r.executor == nil || r.executor.Host() == nil {
		return
	}
	for _, s := range r.executor.Host().ManagedSessions(assistantID) {
		meta, ok, err := agent.LoadBranchMeta(s.Path)
		if err != nil || !ok {
			continue
		}
		if meta.Status == agent.SessionStatusCompleted || meta.Status == agent.SessionStatusFailed {
			continue
		}
		_, ctrl := r.app.sessionCtrlByID(agent.BranchID(s.Path))
		if ctrl == nil || ctrl.Running() {
			continue // not loaded yet, or still executing
		}
		ses, err := agent.LoadSession(s.Path)
		if err != nil {
			continue
		}
		transcript := rawAssistantText(ses.Snapshot())
		if strings.TrimSpace(transcript) == "" {
			continue
		}
		if err := r.store.RecordSessionTranscript(assistant.RecordSessionTranscriptInput{
			RequestID: "record-progress:" + agent.BranchID(s.Path), AssistantID: assistantID, SessionID: agent.BranchID(s.Path),
			Transcript: transcript, Now: time.Now(),
		}); err != nil {
			r.recordDiagnostic("supervisor_writeback", err)
			continue
		}
		meta.Status = agent.SessionStatusCompleted
		meta.UpdatedAt = time.Now()
		_ = agent.SaveBranchMetaPreserveUpdated(s.Path, meta)
		r.markDispatchExecuted(assistantID, agent.BranchID(s.Path))
	}
}

// markDispatchExecuted marks the Dispatch bound to a completed managed Session
// as executed, making it reflection-ready under the converged Session-triggered
// precondition.
func (r *AssistantRuntime) markDispatchExecuted(assistantID, sessionID string) {
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return
	}
	for _, d := range snapshot.Dispatches {
		if d.SessionID != sessionID || d.State != assistant.DispatchClassified {
			continue
		}
		if _, err := r.store.MarkDispatchExecuted(assistant.MarkDispatchExecutedInput{
			RequestID:   assistant.StableID("request", "dispatch-executed/"+d.ID),
			AssistantID: assistantID, DispatchID: d.ID, Now: time.Now(),
		}); err != nil {
			r.recordDiagnostic("dispatch_executed", err)
		}
	}
}

// advanceClassifiedDispatches creates and submits a managed Session for each
// classified task Dispatch that has no Session yet, then binds it. It is the
// converged direct-input execution path: the Dispatcher only classifies, and the
// supervisor loop creates the Session.
func (r *AssistantRuntime) advanceClassifiedDispatches(now time.Time) {
	if r.sessionCreator() == nil {
		return
	}
	assistants, err := r.store.List()
	if err != nil {
		return
	}
	for _, a := range assistants {
		snapshot, err := r.store.Get(a.ID)
		if err != nil {
			continue
		}
		for _, d := range snapshot.Dispatches {
			if d.State != assistant.DispatchClassified || d.Kind != assistant.DispatchTask || d.SessionID != "" {
				continue
			}
			adapter := r.sessionCreator()
			if adapter == nil {
				return
			}
			sessionID, err := adapter.Create(sessiontool.SessionCreateRequest{
				Title: d.Input, Prompt: assistant.ManagedSessionPrompt(snapshot, d.Input), IntentPrompt: d.Input, OwnerID: a.ID, Purpose: agent.PurposeManaged,
				Workspace: snapshot.Assistant.WorkspaceRoot, RequestID: assistant.StableID("request", "dispatch-session/"+d.ID),
			})
			if err != nil {
				r.recordDiagnostic("dispatch_session", err)
				continue
			}
			if _, err := r.store.BindDispatchSession(assistant.BindDispatchSessionInput{
				RequestID:   assistant.StableID("request", "dispatch-session/"+d.ID),
				AssistantID: a.ID, DispatchID: d.ID, SessionID: sessionID, Now: now,
			}); err != nil {
				r.recordDiagnostic("dispatch_bind", err)
			}
		}
	}
}

// fireRoutineSessions turns each due routine fire into a managed Session through
// the shared Session subsystem, then binds the fire to the Session. The Store's
// durable fire ledger makes the fire idempotent across crashes, and the
// fire-derived RequestID makes the Session creation idempotent, so a duplicated
// tick or a crash between "create" and "bind" never creates a second Session;
// an unconsumed fire is simply retried on the next tick.
func (r *AssistantRuntime) fireRoutineSessions(fires []assistant.RoutineFire) {
	if r == nil || len(fires) == 0 {
		return
	}
	adapter := r.sessionCreator()
	if adapter == nil {
		return
	}
	for _, fire := range fires {
		title := strings.TrimSpace(fire.Title)
		if title == "" {
			title = strings.TrimSpace(fire.Prompt)
		}
		requestID := assistant.StableID("request", "routine-fire/"+fire.FireID)
		snapshot, err := r.store.Get(fire.AssistantID)
		if err != nil {
			r.recordDiagnostic("routine_fire_context", err)
			continue
		}
		sessionID, err := adapter.Create(sessiontool.SessionCreateRequest{
			Title: title, Prompt: assistant.ManagedSessionPrompt(snapshot, fire.Prompt), IntentPrompt: fire.Prompt, OwnerID: fire.AssistantID, Purpose: agent.PurposeManaged,
			Workspace: snapshot.Assistant.WorkspaceRoot, RequestID: requestID,
		})
		if err != nil {
			r.recordDiagnostic("routine_fire_session", err)
			continue
		}
		if _, err := r.store.ConsumeRoutineFire(fire.AssistantID, fire.FireID, sessionID, requestID, time.Now()); err != nil {
			r.recordDiagnostic("routine_fire_bind", err)
		}
	}
}

func rawAssistantText(msgs []provider.Message) string {
	var b strings.Builder
	for _, m := range msgs {
		if m.Role == provider.RoleAssistant && m.Content != "" {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// runSupervisorTurns runs one real supervisor reasoning turn per assistant with
// a decision-worthy signal (pending durable events, expansion trigger, or
// pending attention). Reasoning executes through each assistant's durable
// Purpose=supervisor Session Controller (model history, tool calls, pending
// interaction, checkpoint and restart recovery all live in that Session), never
// through an out-of-session role model. It is a no-op when no executor is
// wired (tests) and is not run on every idle tick: the model is only called
// when there is work to decide.
func (r *AssistantRuntime) runSupervisorTurns(now time.Time) {
	if r == nil || r.executor == nil {
		return
	}
	r.executor.RunTurns(context.Background(), now)
}

// enqueueSupervisorUserInput durably records the user input event and wakes the
// loop, so the supervisor observes direct input even when it does not create a
// managed Session (questions, feedback, control intents). A persistence failure
// is recorded, never silently swallowed.
func (r *AssistantRuntime) enqueueSupervisorUserInput(assistantID, requestID, input string) {
	if r == nil || r.executor == nil {
		return
	}
	if err := r.executor.EnqueueUserInput(assistantID, requestID, input); err != nil {
		r.recordDiagnostic("supervisor_event", err)
	}
}

// advanceResponsibility is the acting-phase delegate: the supervisor executor
// owns the advance logic (shared with the daemon); the runtime forwards so
// tests and callers keep the same entry point.
func (r *AssistantRuntime) advanceResponsibility(a assistant.Assistant, alias string) {
	if r.executor != nil {
		r.executor.AdvanceResponsibility(a, alias)
	}
}

// autoAnswerPending forwards the answer action to the shared supervisor
// executor (auto-answer loop, hard-gate classification and experiment forks).
func (r *AssistantRuntime) autoAnswerPending(a assistant.Assistant, sessionID string) {
	if r.executor != nil {
		r.executor.AutoAnswerPending(a, sessionID)
	}
}

// autoAnswerInteraction forwards one interaction batch to the shared executor.
func (r *AssistantRuntime) autoAnswerInteraction(a assistant.Assistant, sessionID string, item sessiontool.SessionInteraction, now time.Time) {
	if r.executor != nil {
		r.executor.AutoAnswerInteraction(a, sessionID, assistant.SessionInteraction{
			Kind: item.Kind, ID: item.ID, Questions: item.Questions, DueAt: item.DueAt,
		}, now)
	}
}

// resolveExperimentTrials forwards the experiment winner sweep to the shared
// executor; the trial status resolver is resolved lazily there.
func (r *AssistantRuntime) resolveExperimentTrials() {
	if r.executor != nil {
		r.executor.ResolveExperimentTrials()
	}
}

// trialSessionStatus derives a trial fork session's experiment status from the
// Session subsystem: completed -> done, failed/cancelled -> failed (terminal,
// can never win), any other located state -> running.
func (r *AssistantRuntime) trialSessionStatus(sessionID string) (string, bool) {
	dirs := []string{config.SessionDir()}
	for _, p := range loadProjectsFile().Projects {
		dirs = append(dirs, config.ProjectSessionDir(p.Root))
	}
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		sessions, err := agent.ListSessions(dir)
		if err != nil {
			continue
		}
		for _, s := range sessions {
			if agent.BranchID(s.Path) != sessionID {
				continue
			}
			meta, ok, err := agent.LoadBranchMeta(s.Path)
			if err != nil || !ok {
				return "", false
			}
			switch agent.DeriveSessionStatus(meta) {
			case agent.SessionStatusCompleted:
				return assistant.TrialStatusDone, true
			case agent.SessionStatusFailed, agent.SessionStatusCancelled:
				return assistant.TrialStatusFailed, true
			default:
				return assistant.TrialStatusRunning, true
			}
		}
	}
	return "", false
}

// recordDecision persists a decision through the store (idempotent receipt) and
// mirrors it to the diagnostic log so the source/confidence/candidates/rationale/
// result/rollback point are auditable even without opening the aggregate.
func (r *AssistantRuntime) recordDecision(rec assistant.InteractionDecisionRecord) {
	if r.store != nil {
		if recorded, err := r.store.RecordInteractionDecision(rec); err == nil {
			rec = recorded
		} else {
			r.recordDiagnostic("autoanswer_decision", err)
		}
	}
	slog.Info("desktop: assistant auto-answer decision",
		"assistant_id", rec.AssistantID,
		"session_id", rec.SessionID,
		"interaction_id", rec.InteractionID,
		"source", string(rec.Source),
		"hard_gate", string(rec.HardGate),
		"confidence", rec.Confidence,
		"candidates", rec.Candidates,
		"rationale", rec.Rationale,
		"result", rec.Result,
		"rollback", rec.Rollback,
		"trials", len(rec.Trials),
		"due_at", rec.DueAt,
	)
}

// interactionPrompt joins a batch's question prompts into one bounded string for
// hard-gate classification and routing.
func interactionPrompt(questions []event.AskQuestion) string {
	prompts := make([]string, 0, len(questions))
	for _, q := range questions {
		if p := strings.TrimSpace(q.Prompt); p != "" {
			prompts = append(prompts, p)
		}
	}
	return strings.Join(prompts, " ")
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// supervisorNextStep derives the durable next-step hint from the plan plus the
// managed-Session execution state. It deliberately ignores snapshot.Runs: the
// Session subsystem is the single source of execution truth. The running/failed
// view comes from the shared supervisor executor's host (multi-dir meta scan),
// the same view the supervisor turns use.
func (r *AssistantRuntime) supervisorNextStep(snapshot assistant.Snapshot, now time.Time) string {
	running, failed := 0, 0
	if r.executor != nil {
		rs, fs := r.executor.SessionSummaries(snapshot.Assistant.ID)
		running, failed = len(rs), len(fs)
	}
	if triggers := assistant.EvaluateExpansion(snapshot, now, assistant.ExpansionLive{
		Running: running, Failed: failed, ObservedAt: snapshot.Expansion.EvidenceObservedAt,
	}); len(triggers) > 0 {
		return "expand:" + string(triggers[0])
	}
	executable := 0
	for _, resp := range snapshot.Plan.Responsibilities {
		if resp.Status == assistant.RespReady || resp.Status == assistant.RespActive {
			executable++
		}
	}
	return fmt.Sprintf("advance %d executable responsibilities (%d running, %d failed sessions)", executable, running, failed)
}

// PublishViewport records the user's current window observation. It is
// short-lived UI context, not business state: the frontend only submits intent
// (visible/selected session IDs), and the backend reads authoritative Session
// state by those IDs when composing the Assistant's implicit context.
func (r *AssistantRuntime) PublishViewport(snapshot assistant.ViewportSnapshot) {
	if r == nil || r.viewport == nil {
		return
	}
	r.viewport.Publish(snapshot)
}

// CurrentViewport returns the most recently focused still-valid viewport
// snapshot, or ok=false when it is expired or unknown.
func (r *AssistantRuntime) CurrentViewport(now time.Time) (assistant.ViewportSnapshot, bool) {
	if r == nil || r.viewport == nil {
		return assistant.ViewportSnapshot{}, false
	}
	return r.viewport.Current(now)
}

func (r *AssistantRuntime) tickOnce(ctx context.Context) {
	now := time.Now()
	wc, err := r.store.WorkControl()
	if err != nil {
		r.recordDiagnostic("workcontrol", err)
		return
	}
	// Restart semantics: a safe-restart intent survives into the next process
	// and recovers exactly once (PAUSED -> RECOVERING -> RUNNING). An explicit
	// PAUSED without intent stays paused; a plain RUNNING restart keeps running.
	if wc.RestartIntent == assistant.RestartIntentRestart && (wc.State == assistant.WorkPaused || wc.State == assistant.WorkQuiescing) {
		if _, err := r.store.BeginRestartRecovery("restart:"+stableRequestID(), time.Now()); err != nil {
			r.recordDiagnostic("workcontrol_restart", err)
			return
		}
		wc, err = r.store.WorkControl()
		if err != nil {
			r.recordDiagnostic("workcontrol", err)
			return
		}
	}
	if wc.State != assistant.WorkRunning && wc.State != assistant.WorkRecovering {
		// Global pause/restart fence: no scheduling, dispatch or session creation
		// happens while work is quiescing or paused.
		return
	}
	recovering := wc.State == assistant.WorkRecovering
	// Recovery pass: re-drive interrupted sessions from their checkpoints
	// before anything else, then complete the resume back to RUNNING.
	if recovering && r.app != nil {
		r.app.resumeHostWork()
	}
	r.ensureSupervisorSessions()
	if r.leader != nil {
		lease, leader, err := r.leader.Acquire(now)
		if err != nil {
			r.recordDiagnostic("leader", err)
			return
		}
		if !leader {
			r.leaderLease = assistant.LeaderLease{}
			return
		}
		r.leaderLease = lease
	}
	// Session creation first, so the supervisor observes fresh execution state
	// and its durable event queue sees the new sessions in the same tick.
	result, err := r.scheduler.Tick(now)
	if err != nil {
		r.recordDiagnostic("schedule", err)
		slog.Error("desktop: assistant schedule tick failed", "err", err, "failures", len(result.Failures))
	}
	r.fireRoutineSessions(result.Fires)
	if r.channels != nil {
		if _, err := r.channels.CollectDue(ctx); err != nil {
			r.recordDiagnostic("channel_collect", err)
		}
	}
	if err := r.processDispatches(ctx); err != nil {
		r.recordDiagnostic("dispatch", err)
	}
	r.advanceClassifiedDispatches(now)
	// Durable, mergeable event collection: routine fires, session lifecycle
	// transitions, deferred retries and the idle heartbeat wake the supervisor.
	if r.executor != nil {
		if err := r.executor.EnqueueRoutineFires(result.Fires, now); err != nil {
			r.recordDiagnostic("supervisor_event", err)
		}
		r.executor.CollectSupervisorEvents(now)
	}
	r.recordSupervisorCycles(now)
	r.runSupervisorTurns(now)
	r.resolveExperimentTrials()
	// A recovery pass ends by re-opening the gate: RECOVERING -> RUNNING exactly
	// once per resume, idempotent under replay (already-RUNNING is a no-op).
	if recovering {
		if _, err := r.store.CompleteResume("resume:"+stableRequestID(), time.Now()); err != nil {
			r.recordDiagnostic("workcontrol_resume", err)
		}
	}
}

// stableRequestID returns a fresh, collision-safe request ID for desktop-owned
// work-control transitions.
func stableRequestID() string {
	return assistant.StableID("request", fmt.Sprintf("desktop/%d/%d", os.Getpid(), time.Now().UnixNano()))
}

// ensureSupervisorSessions gives every active Assistant exactly one durable
// Purpose=supervisor session through the shared supervisor executor. Uniqueness
// lives in the Session subsystem (atomic deterministic creation by the host):
// a supervisor session already present is recovered, never duplicated, and a
// missing one is created once.
func (r *AssistantRuntime) ensureSupervisorSessions() {
	if r == nil || r.executor == nil || r.app == nil {
		return
	}
	assistants, err := r.store.List()
	if err != nil {
		r.recordDiagnostic("supervisor_list", err)
		return
	}
	r.executor.EnsureSupervisorSessions(assistants)
}

func (r *AssistantRuntime) Tools(assistantID, executionID string) []tool.Tool {
	if r == nil {
		return nil
	}
	tools := assistantchannel.Tools(r.channels, assistantID, executionID)
	tools = append(tools, r.sessionTools(assistantID)...)
	tools = append(tools, assistantStoreTools(r.store, assistantID)...)
	return tools
}

// assistantStoreTools returns the schedule/memory/policy tools bound to one
// Assistant, so a supervisor turn can manage its own Routines, Memory and Policy
// through the authoritative assistant.Store.
func assistantStoreTools(store *assistant.Store, assistantID string) []tool.Tool {
	if store == nil || assistantID == "" {
		return nil
	}
	return []tool.Tool{
		assistanttool.NewScheduleListTool(store, assistantID),
		assistanttool.NewScheduleGetTool(store, assistantID),
		assistanttool.NewScheduleCreateTool(store, assistantID),
		assistanttool.NewScheduleUpdateTool(store, assistantID),
		assistanttool.NewSchedulePauseTool(store, assistantID),
		assistanttool.NewScheduleResumeTool(store, assistantID),
		assistanttool.NewScheduleDeleteTool(store, assistantID),
		assistanttool.NewScheduleRunNowTool(store, assistantID),
		assistanttool.NewMemorySearchTool(store, assistantID),
		assistanttool.NewMemoryRememberTool(store, assistantID),
		assistanttool.NewMemoryForgetTool(store, assistantID),
		assistanttool.NewPolicyGetTool(store, assistantID),
		assistanttool.NewPolicyUpdateTool(store, assistantID),
		assistanttool.NewProjectStatusTool(store, assistantID),
		assistanttool.NewProjectConstraintsGetTool(store, assistantID),
		assistanttool.NewProjectConstraintsPatchTool(store, assistantID),
	}
}

func (a *App) assistantToolsForTab(tab *WorkspaceTab) []tool.Tool {
	if a == nil || tab == nil || tab.sessionKind != agent.SessionKindAssistant || a.assistant == nil {
		return nil
	}
	// The Controller is assembled before a Session runs. Its durable session
	// identity still gives outbound intents a stable retry scope without
	// deduplicating identical content across unrelated Assistant sessions.
	tools := a.assistant.Tools(tab.assistantID, tab.SessionID)
	// The supervisor Session reasons with a bounded, read-only observation
	// surface: it can list/read sessions, schedules, memory, policy and project
	// state, but never acts directly — the loop routes its bounded decision and
	// applies fences before any side effect. Write session/schedule/memory/
	// policy tools stay on managed Sessions.
	if supervisorTab(tab) {
		tools = filterReadOnlyTools(tools)
	}
	return tools
}

func (r *AssistantRuntime) recordDiagnostic(operation string, err error) {
	if r == nil || err == nil {
		return
	}
	r.diagnosticMu.Lock()
	r.diagnostics = append(r.diagnostics, AssistantDiagnostic{
		At: time.Now(), Category: assistantDiagnosticRuntime, Operation: operation, Message: err.Error(),
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

// selectReadyResponsibility deterministically picks the one responsibility a
// session works on: an already-active one wins, otherwise the first ready one in
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

func buildAssistantPermissionPolicy(policy assistant.Policy) permission.Policy {
	return assistant.PermissionPolicy(policy)
}

// ObserveEvent is retained as the tab event-sink seam. With the Run execution
// path removed there are no in-flight runs to correlate, so it always reports
// "not consumed" and the normal sink pipeline handles the event.
func (r *AssistantRuntime) ObserveEvent(string, event.Event) bool {
	return false
}

// CancelRun is a no-op compatibility seam: Runs are historical/read-only and
// never have a live in-flight turn to cancel.
func (r *AssistantRuntime) CancelRun(string) {}
