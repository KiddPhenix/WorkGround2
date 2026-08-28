package assistantdaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/assistantchannel"
	"workground2/internal/config"
	"workground2/internal/netclient"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/tool/sessiontool"
)

const leaseTTL = 2 * time.Minute

type Options struct {
	StoreRoot, Model string
	Interval         time.Duration
	Stderr           io.Writer
}

type Runtime struct {
	store          *assistant.Store
	scheduler      *assistant.Scheduler
	dispatcher     *assistant.Dispatcher
	reflector      *assistant.Reflector
	ideator        *assistant.Ideator
	autoAnswer     *assistant.AutoAnswer
	leader         *assistant.LeaderElector
	channels       *assistantchannel.Service
	sessionControl *daemonSessionControl
	executor       *assistant.SupervisorExecutor
	opts           Options
	leaseMu        sync.Mutex
	lease          assistant.LeaderLease
}

func New(opts Options) (*Runtime, error) {
	if opts.StoreRoot == "" {
		opts.StoreRoot = filepath.Join(config.MemoryUserDir(), "assistants")
	}
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	store, err := assistant.NewStore(opts.StoreRoot)
	if err != nil {
		return nil, err
	}
	scheduler, err := assistant.NewScheduler(store)
	if err != nil {
		return nil, err
	}
	owner := fmt.Sprintf("daemon:%d:%d", os.Getpid(), time.Now().UnixNano())
	roleModel := assistant.RoleModelFunc(func(ctx context.Context, prompt string) (string, error) {
		return runRoleCompletion(ctx, opts.Model, opts.Stderr, prompt)
	})
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
	autoAnswer, err := assistant.NewAutoAnswer(roleModel)
	if err != nil {
		return nil, err
	}
	leader, err := assistant.NewLeaderElector(opts.StoreRoot, owner, 90*time.Second)
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	client, err := netclient.NewHTTPClient(cfg.NetworkProxySpec(), netclient.TransportOptions{})
	if err != nil {
		return nil, err
	}
	channels, err := assistantchannel.New(store, func(key string) string { return config.ResolveCredential(key).Value }, assistantchannel.NewDiscourse(client))
	if err != nil {
		return nil, err
	}
	r := &Runtime{
		store: store, scheduler: scheduler, dispatcher: dispatcher, reflector: reflector,
		ideator: ideator, autoAnswer: autoAnswer, leader: leader, channels: channels, opts: opts,
	}
	r.sessionControl = newDaemonSessionControl(opts.Model, opts.Stderr, store, func(assistantID, executionID string) []tool.Tool {
		return assistantchannel.Tools(r.channels, assistantID, executionID)
	})
	// The shared supervisor executor drives the same core the desktop uses:
	// atomic supervisor Session, durable event queue, real Controller turns.
	// Hooks resolve sessionControl/autoAnswer/trialStatus lazily.
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		return nil, err
	}
	executor, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:  store,
		Events: events,
		Host:   &daemonSupervisorHost{r: r},
		Control: func() assistant.SessionControl {
			return &daemonSupervisorSessionControl{inner: r.sessionControl}
		},
		Ideator:    ideator,
		AutoAnswer: func() *assistant.AutoAnswer { return r.autoAnswer },
		TrialStatus: func() assistant.TrialStatusResolver {
			return r.daemonTrialSessionStatus
		},
		Diagnostic: func(operation string, err error) {
			fmt.Fprintln(opts.Stderr, "assistant daemon:", operation+":", err)
		},
		Constraints: func(assistantID string) (string, int64) {
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
	return r, nil
}

func (r *Runtime) Run(ctx context.Context) error {
	defer r.Close()
	ticker := time.NewTicker(r.opts.Interval)
	defer ticker.Stop()
	for {
		if err := r.RunOnce(ctx); err != nil {
			fmt.Fprintln(r.opts.Stderr, "assistant daemon:", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunOnce executes one converged pass: it first resolves the restart semantics
// (a pending safe-restart intent re-enters RECOVERING exactly once), then the
// scheduler produces idempotent routine fires, classified task Dispatches and
// fires become managed Sessions through the headless SessionControl, and the
// phase-6 roles advance. While RECOVERING the same pass re-drives recovery
// work (unconsumed fires, dispatches, supervisor subscriptions) and then
// completes the resume back to RUNNING. No Run or RunnerJob is ever claimed or
// written here.
func (r *Runtime) RunOnce(ctx context.Context) error {
	wc, err := r.store.WorkControl()
	if err != nil {
		return err
	}
	// Restart semantics: a safe-restart intent survives into the next process
	// and must recover exactly once (PAUSED -> RECOVERING -> RUNNING). An
	// explicit PAUSED without intent stays paused; a plain RUNNING restart just
	// keeps running — nothing here fakes a safe recovery.
	if wc.RestartIntent == assistant.RestartIntentRestart && (wc.State == assistant.WorkPaused || wc.State == assistant.WorkQuiescing) {
		if _, err := r.store.BeginRestartRecovery("restart:"+StableRequestID(), time.Now()); err != nil {
			return err
		}
	}
	wc, err = r.store.WorkControl()
	if err != nil {
		return err
	}
	if wc.State != assistant.WorkRunning && wc.State != assistant.WorkRecovering {
		// Global pause/restart fence: release any leader lease and do no work.
		r.setLease(assistant.LeaderLease{})
		return nil
	}
	recovering := wc.State == assistant.WorkRecovering
	lease, leader, err := r.leader.Acquire(time.Now())
	if err != nil {
		return err
	}
	if !leader {
		r.setLease(assistant.LeaderLease{})
		return nil
	}
	r.setLease(lease)
	workCtx, stopRenew := r.keepLeader(ctx)
	result, scheduleErr := r.scheduler.Tick(time.Now())
	_, collectErr := r.channels.CollectDue(workCtx)
	issues := []error{scheduleErr, collectErr}
	for _, fire := range result.Fires {
		requestID := assistant.StableID("request", "routine-fire/"+fire.FireID)
		sessionID, err := r.sessionControl.Create(sessiontool.SessionCreateRequest{
			Title: fire.Title, Prompt: fire.Prompt, OwnerID: fire.AssistantID,
			Purpose:   agent.PurposeManaged,
			RequestID: requestID,
		})
		if err != nil {
			issues = append(issues, err)
			continue
		}
		if _, err := r.store.ConsumeRoutineFire(fire.AssistantID, fire.FireID, sessionID, requestID, time.Now()); err != nil {
			issues = append(issues, err)
		}
	}
	if err := r.processDispatches(workCtx); err != nil {
		issues = append(issues, err)
	}
	if err := r.advanceClassifiedDispatches(time.Now()); err != nil {
		issues = append(issues, err)
	}
	issues = append(issues, r.writebackManagedSessions(time.Now())...)
	// Supervisor loop: guarantee the unique supervisor Session, collect durable
	// events (routine fires, session lifecycle, retries, heartbeat) into the
	// mergeable queue, and run real reasoning turns through each supervisor
	// Session's Controller — the same core the desktop drives. While RECOVERING
	// only the subscription/scan half runs (EnsureSupervisorSessions +
	// CollectSupervisorEvents): model turns are still fenced until the recovery
	// pass completes back to RUNNING, so recovery never invents fresh decisions.
	if r.executor != nil {
		if assistants, err := r.store.List(); err == nil {
			r.executor.EnsureSupervisorSessions(assistants)
		}
		if err := r.executor.EnqueueRoutineFires(result.Fires, time.Now()); err != nil {
			issues = append(issues, err)
		}
		r.executor.CollectSupervisorEvents(time.Now())
		if !recovering {
			r.executor.RunTurns(workCtx, time.Now())
			r.executor.ResolveExperimentTrials()
		}
	}
	issues = append(issues, stopRenew())
	// A recovery pass ends by re-opening the gate: RECOVERING -> RUNNING exactly
	// once per resume, idempotent under replay (already-RUNNING is a no-op).
	if recovering {
		if _, err := r.store.CompleteResume("resume:"+StableRequestID(), time.Now()); err != nil {
			issues = append(issues, err)
		}
	}
	return errors.Join(compact(issues)...)
}

// writebackManagedSessions converges completed headless Sessions into the
// Assistant plan before lifecycle events wake the supervisor. It also restores
// nonterminal managed Sessions after a daemon restart, so the same persisted
// Session either keeps running or settles and is recorded exactly once.
func (r *Runtime) writebackManagedSessions(now time.Time) []error {
	if r == nil || r.store == nil || r.sessionControl == nil {
		return nil
	}
	assistants, err := r.store.List()
	if err != nil {
		return []error{err}
	}
	var issues []error
	for _, a := range assistants {
		seen := map[string]struct{}{}
		for _, dir := range daemonSupervisorDirs(r) {
			sessions, listErr := agent.ListSessionsByOwnerByMeta(dir, a.ID)
			if listErr != nil {
				issues = append(issues, listErr)
				continue
			}
			for _, s := range sessions {
				if s.Purpose != agent.PurposeManaged {
					continue
				}
				id := agent.BranchID(s.Path)
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				meta, ok, metaErr := agent.LoadBranchMeta(s.Path)
				if metaErr != nil || !ok {
					if metaErr != nil {
						issues = append(issues, metaErr)
					}
					continue
				}
				status := agent.DeriveSessionStatus(meta)
				if status == agent.SessionStatusCompleted || status == agent.SessionStatusFailed || status == agent.SessionStatusCancelled {
					continue
				}
				ctrl, ctrlErr := r.sessionControl.requireCtrl(id)
				if ctrlErr != nil {
					issues = append(issues, ctrlErr)
					continue
				}
				if ctrl.Running() {
					continue
				}
				if _, pending := ctrl.PendingInteraction(); pending {
					continue
				}
				sess, loadErr := agent.LoadSession(s.Path)
				if loadErr != nil {
					issues = append(issues, loadErr)
					continue
				}
				transcript := daemonAssistantText(sess.Snapshot())
				if strings.TrimSpace(transcript) == "" {
					continue
				}
				if recordErr := r.store.RecordSessionTranscript(assistant.RecordSessionTranscriptInput{
					RequestID: "record-progress:" + id, AssistantID: a.ID, SessionID: id,
					Transcript: transcript, Now: now,
				}); recordErr != nil {
					issues = append(issues, recordErr)
					continue
				}
				meta.Status = agent.SessionStatusCompleted
				meta.UpdatedAt = now
				if saveErr := agent.SaveBranchMetaPreserveUpdated(s.Path, meta); saveErr != nil {
					issues = append(issues, saveErr)
					continue
				}
				if dispatchErr := r.markDispatchExecuted(a.ID, id, now); dispatchErr != nil {
					issues = append(issues, dispatchErr)
				}
			}
		}
	}
	return issues
}

func daemonAssistantText(messages []provider.Message) string {
	var parts []string
	for _, message := range messages {
		if message.Role == provider.RoleAssistant && strings.TrimSpace(message.Content) != "" {
			parts = append(parts, message.Content)
		}
	}
	return strings.Join(parts, "\n")
}

func (r *Runtime) markDispatchExecuted(assistantID, sessionID string, now time.Time) error {
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return err
	}
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.SessionID != sessionID || dispatch.State != assistant.DispatchClassified {
			continue
		}
		if _, err := r.store.MarkDispatchExecuted(assistant.MarkDispatchExecutedInput{
			RequestID:   assistant.StableID("request", "dispatch-executed/"+dispatch.ID),
			AssistantID: assistantID, DispatchID: dispatch.ID, Now: now,
		}); err != nil {
			return err
		}
	}
	return nil
}

// StableRequestID returns a fresh, collision-safe request ID for daemon-owned
// work-control transitions.
func StableRequestID() string {
	return assistant.StableID("request", fmt.Sprintf("daemon/%d/%d", os.Getpid(), time.Now().UnixNano()))
}

// Close releases leadership held by this runtime and shuts down the headless
// session controllers. It is safe to call more than once and is required by
// one-shot embedders that do not call Run.
func (r *Runtime) Close() error {
	var issues []error
	if r.sessionControl != nil {
		issues = append(issues, r.sessionControl.Close())
	}
	issues = append(issues, r.release())
	return errors.Join(compact(issues)...)
}

func (r *Runtime) keepLeader(ctx context.Context) (context.Context, func() error) {
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	finished := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(finished)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-workCtx.Done():
				return
			case now := <-ticker.C:
				lease := r.currentLease()
				renewed, err := r.leader.Renew(lease, now)
				if err != nil {
					errCh <- fmt.Errorf("assistant daemon: renew leader lease: %w", err)
					cancel()
					return
				}
				r.setLease(renewed)
			}
		}
	}()
	var once sync.Once
	stop := func() error {
		once.Do(func() {
			close(done)
			cancel()
		})
		<-finished
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
	return workCtx, stop
}

func (r *Runtime) setLease(lease assistant.LeaderLease) {
	r.leaseMu.Lock()
	r.lease = lease
	r.leaseMu.Unlock()
}

func (r *Runtime) currentLease() assistant.LeaderLease {
	r.leaseMu.Lock()
	defer r.leaseMu.Unlock()
	return r.lease
}

func (r *Runtime) release() error {
	lease := r.currentLease()
	if lease.Fence == "" {
		return nil
	}
	err := r.leader.Release(lease)
	if err == nil || errors.Is(err, assistant.ErrLeaderLost) {
		r.setLease(assistant.LeaderLease{})
	}
	return err
}

func compact(in []error) []error {
	out := []error{}
	for _, err := range in {
		if err != nil {
			out = append(out, err)
		}
	}
	return out
}
