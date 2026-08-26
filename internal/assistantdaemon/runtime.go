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
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/netclient"
	"workground2/internal/permission"
)

const leaseTTL = 2 * time.Minute

type Options struct {
	StoreRoot, Model string
	Interval         time.Duration
	Stderr           io.Writer
}
type Runtime struct {
	store      *assistant.Store
	scheduler  *assistant.Scheduler
	runner     *assistant.Runner
	jobRunner  *assistant.JobRunner
	dispatcher *assistant.Dispatcher
	reflector  *assistant.Reflector
	ideator    *assistant.Ideator
	leader     *assistant.LeaderElector
	channels   *assistantchannel.Service
	opts       Options
	leaseMu    sync.Mutex
	lease      assistant.LeaderLease
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
	runner, err := assistant.NewRunner(store, owner, leaseTTL)
	if err != nil {
		return nil, err
	}
	jobRunner, err := assistant.NewJobRunner(store, owner, leaseTTL)
	if err != nil {
		return nil, err
	}
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
	return &Runtime{store: store, scheduler: scheduler, runner: runner, jobRunner: jobRunner, dispatcher: dispatcher, reflector: reflector, ideator: ideator, leader: leader, channels: channels, opts: opts}, nil
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

func (r *Runtime) RunOnce(ctx context.Context) error {
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
	_, scheduleErr := r.scheduler.Tick(time.Now())
	_, collectErr := r.channels.CollectDue(workCtx)
	issues := []error{scheduleErr, collectErr}
	if err := r.processDispatches(workCtx); err != nil {
		issues = append(issues, err)
	}
	for {
		acquired, err := r.runner.Acquire(time.Now())
		if err != nil {
			issues = append(issues, err)
			break
		}
		if acquired.Run == nil {
			break
		}
		runCtx, stopRunLease := r.keepRunLease(workCtx, *acquired.Run)
		if err := r.execute(runCtx, *acquired.Run); err != nil {
			issues = append(issues, err)
		}
		issues = append(issues, stopRunLease())
	}
	for {
		acquired, err := r.jobRunner.Acquire(time.Now())
		if err != nil {
			issues = append(issues, err)
			break
		}
		if acquired.Job == nil {
			break
		}
		jobCtx, stopJobLease := r.keepJobLease(workCtx, *acquired.Job)
		if err := r.executeJob(jobCtx, *acquired.Job); err != nil {
			issues = append(issues, err)
		}
		issues = append(issues, stopJobLease())
		if err := r.reflectReadyOf(jobCtx, acquired.Job.AssistantID); err != nil {
			issues = append(issues, err)
		}
	}
	issues = append(issues, stopRenew())
	return errors.Join(compact(issues)...)
}

// Close releases leadership held by this runtime. It is safe to call more than
// once and is required by one-shot embedders that do not call Run.
func (r *Runtime) Close() error { return r.release() }

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

type turnCapture struct {
	summary, text                string
	attention                    bool
	action, tool, subject, token string
	evidence                     assistant.Evidence
}

func (c *turnCapture) sink(value event.Event) {
	switch value.Kind {
	case event.Text:
		if len(c.text) < 1<<20 {
			c.text += value.Text
		}
	case event.Message:
		if strings.TrimSpace(value.Text) != "" {
			c.summary = strings.TrimSpace(value.Text)
		}
	case event.ToolResult:
		c.evidence.RecordToolResult(value.Tool.Name, value.Tool.Err == "")
	case event.ApprovalRequest:
		c.attention = true
		c.action = "approve_tool"
		c.tool = strings.TrimSpace(value.Approval.Tool)
		if c.tool != "" {
			c.action += ":" + c.tool
		}
		c.subject = strings.TrimSpace(value.Approval.Subject)
		c.summary = strings.TrimSpace(value.Approval.Summary)
		if c.subject != "" {
			c.summary = c.subject + " — " + c.summary
		}
		c.token = strings.TrimSpace(value.Approval.ID)
	case event.AskRequest:
		c.attention = true
		c.action = "answer_required"
		c.token = strings.TrimSpace(value.Ask.ID)
		if len(value.Ask.Questions) > 0 {
			c.summary = value.Ask.Questions[0].Prompt
		}
	}
}

func (r *Runtime) execute(ctx context.Context, run assistant.Run) error {
	workspace := ""
	if run.Scope == assistant.ScopeWorkspace {
		workspace = run.WorkspaceRoot
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			cause := fmt.Errorf("frozen workspace is not a directory: %s", workspace)
			if err != nil {
				cause = fmt.Errorf("frozen workspace unavailable: %w", err)
			}
			_, persistErr := r.store.RequireAttention(assistant.RequireAttentionInput{RequestID: fmt.Sprintf("daemon-workspace:%s:%d", run.ID, run.LeaseFence), RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence, Action: assistant.AttentionActionCancelRecreate, Summary: cause.Error(), ResumeToken: assistant.StableID("resume", run.ID), Now: time.Now()})
			return errors.Join(cause, persistErr)
		}
	}
	snapshot, err := r.store.Get(run.AssistantID)
	if err != nil {
		return r.fail(run, "snapshot", err, true)
	}
	capture := &turnCapture{}
	sessionDir := config.SessionDir()
	if workspace != "" {
		sessionDir = config.ProjectSessionDir(workspace)
	}
	ctrl, err := boot.Build(ctx, boot.Options{Model: r.opts.Model, RequireKey: true, Sink: event.FuncSink(capture.sink), Stderr: r.opts.Stderr, WorkspaceRoot: workspace, SessionDir: sessionDir, SessionKind: agent.SessionKindAssistant, ExtraTools: assistantchannel.Tools(r.channels, run.AssistantID, run.ID), ApprovalTimeout: 2 * time.Second})
	if err != nil {
		return r.fail(run, "controller_start", err, true)
	}
	defer ctrl.Close()
	if run.SessionPath != "" {
		loaded, loadErr := agent.LoadSession(run.SessionPath)
		if loadErr != nil {
			return r.fail(run, "session_load", loadErr, true)
		}
		ctrl.Resume(loaded, run.SessionPath)
	} else {
		ctrl.SetSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	}
	meta, _ := agent.EnsureBranchMeta(ctrl.SessionPath())
	meta.SessionKind, meta.AssistantID = agent.SessionKindAssistant, run.AssistantID
	_ = agent.SaveBranchMetaPreserveUpdated(ctrl.SessionPath(), meta)
	bound, err := r.runner.BindSession(run, fmt.Sprintf("daemon-bind:%s:%d", run.ID, run.LeaseFence), ctrl.SessionPath(), time.Now())
	if err != nil {
		return err
	}
	run = *bound
	prompt, grants := daemonPrompt(snapshot, run)
	runErr := ctrl.RunWithPolicy(ctx, prompt, daemonPermission(run.Policy), control.ToolApprovalAuto, grants...)
	if capture.attention {
		if capture.summary == "" {
			capture.summary = "助手执行需要用户处理"
		}
		if capture.token == "" {
			capture.token = assistant.StableID("resume", fmt.Sprintf("%s/%d", run.ID, run.LeaseFence))
		}
		_, err := r.store.RequestApproval(assistant.ApprovalInput{RequestID: fmt.Sprintf("daemon-attention:%s:%d", run.ID, run.LeaseFence), RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence, Action: capture.action, Summary: capture.summary, Tool: capture.tool, Subject: capture.subject, SessionPath: ctrl.SessionPath(), ResumeToken: capture.token, Now: time.Now()})
		return err
	}
	if runErr != nil {
		return r.fail(run, "turn_failed", runErr, true)
	}
	if missing := capture.evidence.Missing(assistant.RequiredCapabilities(run.Mission, run.Prompt)); len(missing) > 0 {
		failure := assistant.EvidenceFailure(missing)
		_, err := r.runner.Fail(run, failure)
		return err
	}
	return r.complete(snapshot, run, capture, ctrl.SessionPath())
}

func (r *Runtime) keepRunLease(ctx context.Context, run assistant.Run) (context.Context, func() error) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	finished := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		defer close(finished)
		ticker := time.NewTicker(leaseTTL / 2)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-runCtx.Done():
				return
			case now := <-ticker.C:
				if _, err := r.runner.Renew(run, now); err != nil {
					errCh <- fmt.Errorf("assistant daemon: renew run %s: %w", run.ID, err)
					cancel()
					return
				}
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
	return runCtx, stop
}

func (r *Runtime) fail(run assistant.Run, code string, cause error, retryable bool) error {
	_, err := r.runner.Fail(run, assistant.Failure{Code: code, Message: cause.Error(), Retryable: retryable, OutcomeKnown: true, RetryAfter: time.Minute, Now: time.Now()})
	return errors.Join(cause, err)
}

func (r *Runtime) complete(snapshot assistant.Snapshot, run assistant.Run, capture *turnCapture, sessionPath string) error {
	base := assistant.CompleteRunInput{RunID: run.ID, LeaseOwner: run.LeaseOwner, LeaseFence: run.LeaseFence, Summary: strings.TrimSpace(assistant.StripProgressBlocks(capture.summary)), SessionPath: sessionPath, Now: time.Now()}
	blocks, parseErrs := assistant.ParseProgressBlocks(capture.text)
	if len(parseErrs) == 0 && len(blocks) > 0 {
		base.Progress = assistant.MergeProgressBlocks(blocks)
		base.Progress.PlanRevision = snapshot.Plan.Revision
		for attempt := 0; attempt < 3; attempt++ {
			base.RequestID = fmt.Sprintf("daemon-progress:%s:%d:%d", run.ID, run.LeaseFence, attempt)
			if _, err := r.store.CompleteRunWithProgress(base); err == nil {
				return nil
			} else if !errors.Is(err, assistant.ErrConflict) {
				break
			}
			latest, err := r.store.Get(run.AssistantID)
			if err != nil {
				break
			}
			assistant.RebaseProgress(latest.Plan, &base.Progress)
		}
	}
	base.Progress = assistant.ProgressBlock{}
	base.RequestID = fmt.Sprintf("daemon-complete:%s:%d", run.ID, run.LeaseFence)
	_, err := r.store.CompleteRunWithProgress(base)
	return err
}

func daemonPrompt(snapshot assistant.Snapshot, run assistant.Run) (string, []control.ToolGrant) {
	var b strings.Builder
	fmt.Fprintf(&b, "你正在由本机无 UI daemon 执行长期助手 Run。\n\n助手使命：\n%s\n\n本次任务：\n%s\n\n", run.Mission, run.Prompt)
	if len(snapshot.Memory.Items) > 0 {
		b.WriteString("显式记忆：\n")
		for _, item := range snapshot.Memory.Items {
			fmt.Fprintf(&b, "- [%s] %s\n", item.Kind, item.Body)
		}
	}
	if len(snapshot.Channels) > 0 {
		b.WriteString("\n渠道：\n")
		for _, channel := range snapshot.Channels {
			fmt.Fprintf(&b, "- %s id=%s kind=%s enabled=%t\n", channel.Name, channel.ID, channel.Kind, channel.Enabled)
		}
	}
	for _, metric := range snapshot.ChannelMetrics {
		fmt.Fprintf(&b, "- metric channel=%s topic=%d views=%d(+%d) likes=%d(+%d) replies=%d(+%d)\n", metric.ChannelID, metric.TopicID, metric.Views, metric.ViewsDelta, metric.Likes, metric.LikesDelta, metric.Replies, metric.ReplyDelta)
	}
	grants := []control.ToolGrant{}
	for _, item := range snapshot.Attention {
		if item.RunID == run.ID && item.State == assistant.AttentionApproved && item.ResumeToken == run.ResumeToken {
			if strings.HasPrefix(item.Action, "approve_tool:") && item.Tool != "" {
				grants = append(grants, control.ToolGrant{Tool: item.Tool, Subject: item.Subject})
			} else if item.Action == "answer_required" {
				fmt.Fprintf(&b, "\n用户明确回答：%s\n", item.Resolution)
			}
		}
	}
	b.WriteString("\n外发必须使用 assistant_channel_publish/reply；冻结的 publish 权限决定自动执行、逐次审批或拒绝。读取已采集指标用 assistant_channel_metrics。完成后给出结论，并可在末尾输出合法 <assistant-progress> JSON 块。")
	return b.String(), grants
}

func daemonPermission(p assistant.Policy) permission.Policy {
	return assistant.PermissionPolicy(p)
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
