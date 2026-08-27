package assistantdaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
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
)

// processDispatches advances the phase-6 roles for every assistant: it
// classifies pending/failed Dispatches with the deterministic classifier,
// reflects Dispatches whose jobs are terminal, and opens cadence ideas when
// due. Every mutation is idempotent and CAS-guarded by the Store, so repeated
// ticks are safe.
func (r *Runtime) processDispatches(ctx context.Context) error {
	assistants, err := r.store.List()
	if err != nil {
		return err
	}
	var issues []error
	for _, record := range assistants {
		snapshot, err := r.store.Get(record.ID)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		if err := r.classifyPending(ctx, snapshot); err != nil {
			issues = append(issues, err)
		}
		if err := r.reflectReady(ctx, snapshot); err != nil {
			issues = append(issues, err)
		}
		if err := r.ideateIfDue(ctx, snapshot); err != nil {
			issues = append(issues, err)
		}
	}
	return errors.Join(compact(issues)...)
}

func (r *Runtime) classifyPending(ctx context.Context, snapshot assistant.Snapshot) error {
	now := time.Now()
	for _, dispatch := range snapshot.Dispatches {
		if dispatch.State != assistant.DispatchPendingClassification && dispatch.State != assistant.DispatchClassificationFailed {
			continue
		}
		if dispatch.State == assistant.DispatchClassificationFailed && dispatch.RetryAt.After(now) {
			continue
		}
		if _, err := r.dispatcher.RetryDispatch(ctx, dispatch.AssistantID, dispatch.ID, time.Now()); err != nil {
			return fmt.Errorf("classify dispatch %s: %w", dispatch.ID, err)
		}
	}
	return nil
}

func (r *Runtime) reflectReady(ctx context.Context, snapshot assistant.Snapshot) error {
	for _, dispatch := range assistant.DispatchesReadyForReflection(snapshot, time.Now()) {
		if _, err := r.reflector.Reflect(ctx, dispatch.AssistantID, dispatch.ID, "reflect:"+dispatch.ID, time.Now()); err != nil {
			return fmt.Errorf("reflect dispatch %s: %w", dispatch.ID, err)
		}
	}
	return nil
}

func (r *Runtime) ideateIfDue(ctx context.Context, snapshot assistant.Snapshot) error {
	due, _, _, err := r.store.ShouldIdeate(snapshot.Assistant.ID, time.Now())
	if err != nil || !due {
		return err
	}
	requestID := "ideate:" + snapshot.Assistant.ID + ":" + strconv.Itoa(len(snapshot.Ideas))
	_, err = r.ideator.Ideate(ctx, assistant.OpenIdeaInput{
		AssistantID: snapshot.Assistant.ID, RequestID: requestID,
		Trigger: assistant.IdeaTriggerCadence, Now: time.Now(),
	})
	return err
}

// executeJob runs one claimed Job through a headless controller, then finishes
// or fails it. It mirrors execute for Runs but drives the Job lifecycle.
func (r *Runtime) executeJob(ctx context.Context, job assistant.RunnerJob) error {
	workspace := ""
	if job.Scope == assistant.ScopeWorkspace {
		workspace = job.WorkspaceRoot
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			cause := fmt.Errorf("frozen workspace is not a directory: %s", workspace)
			if err != nil {
				cause = fmt.Errorf("frozen workspace unavailable: %w", err)
			}
			_, failErr := r.jobRunner.Fail(job, assistant.Failure{Code: "workspace_unavailable", Message: cause.Error(), Retryable: false, OutcomeKnown: true, Now: time.Now()})
			return errorsJoin(cause, failErr)
		}
	}
	snapshot, err := r.store.Get(job.AssistantID)
	if err != nil {
		return r.failJob(job, "snapshot", err, true)
	}
	capture := &turnCapture{}
	sessionDir := config.SessionDir()
	if workspace != "" {
		sessionDir = config.ProjectSessionDir(workspace)
	}
	ctrl, err := boot.Build(ctx, boot.Options{Model: r.opts.Model, RequireKey: true, Sink: event.FuncSink(capture.sink), Stderr: r.opts.Stderr, WorkspaceRoot: workspace, SessionDir: sessionDir, SessionKind: agent.SessionKindAssistant, ExtraTools: assistantchannel.Tools(r.channels, job.AssistantID, job.ID), ApprovalTimeout: 2 * time.Second})
	if err != nil {
		return r.failJob(job, "controller_start", err, true)
	}
	defer ctrl.Close()
	ctrl.SetSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	meta, _ := agent.EnsureBranchMeta(ctrl.SessionPath())
	meta.SessionKind, meta.AssistantID = agent.SessionKindAssistant, job.AssistantID
	meta.SessionSource = agent.SessionSourceAssist
	_ = agent.SaveBranchMetaPreserveUpdated(ctrl.SessionPath(), meta)
	bound, err := r.jobRunner.BindSession(job, fmt.Sprintf("daemon-bind-session:%s:%d", job.ID, job.LeaseFence), ctrl.SessionPath(), time.Now())
	if err != nil {
		return r.failJob(job, "session_bind", err, true)
	}
	job = *bound
	prompt := daemonJobPrompt(snapshot, job)
	runErr := ctrl.RunWithPolicy(ctx, prompt, daemonPermission(job.Policy), control.ToolApprovalAuto)
	if capture.attention {
		_, failErr := r.jobRunner.Fail(job, assistant.Failure{Code: "attention", Message: "job requires user attention", Retryable: false, OutcomeKnown: false, Now: time.Now()})
		return failErr
	}
	if runErr != nil {
		return r.failJob(job, "turn_failed", runErr, true)
	}
	if missing := capture.evidence.Missing(assistant.RequiredCapabilities(jobKindMission(job), job.Prompt)); len(missing) > 0 {
		failure := assistant.EvidenceFailure(missing)
		_, err := r.jobRunner.Fail(job, failure)
		return err
	}
	summary := strings.TrimSpace(assistant.StripProgressBlocks(capture.summary))
	if summary == "" {
		summary = strings.TrimSpace(capture.text)
	}
	_, err = r.jobRunner.Finish(job, summary, time.Now())
	return err
}

func (r *Runtime) failJob(job assistant.RunnerJob, code string, cause error, retryable bool) error {
	_, err := r.jobRunner.Fail(job, assistant.Failure{Code: code, Message: cause.Error(), Retryable: retryable, OutcomeKnown: true, RetryAfter: time.Minute, Now: time.Now()})
	return errorsJoin(cause, err)
}

// jobKindMission returns the frozen mission for capability detection: jobs carry
// no mission field, so derive from the assistant snapshot is unnecessary; the
// job prompt is authoritative for live_web detection.
func jobKindMission(job assistant.RunnerJob) string {
	return job.Prompt
}

func daemonJobPrompt(snapshot assistant.Snapshot, job assistant.RunnerJob) string {
	var b strings.Builder
	b.WriteString("你正在执行一个长期助手的 Runner Job。\n\n")
	if job.Kind == assistant.DispatchTask {
		b.WriteString("本次是用户直接输入派生出的任务 Job。\n\n")
	} else {
		fmt.Fprintf(&b, "本次是分类为 %s 的 Job。\n\n", job.Kind)
	}
	fmt.Fprintf(&b, "Job 名称：%s\n", job.Name)
	fmt.Fprintf(&b, "助手使命：\n%s\n\n本次任务：\n%s\n\n", snapshot.Assistant.Mission, job.Prompt)
	packs := assistant.ApplicableContextPacks(snapshot.ContextPacks, job.AssistantID, job.DispatchID, job.ContextPackRevision, 4, 8000)
	if len(packs) > 0 {
		b.WriteString("近期反思结论（只作背景，不得跨任务重复执行已完成事项）：\n")
		for _, pack := range packs {
			fmt.Fprintf(&b, "- %s\n", strings.TrimSpace(pack.Conclusion))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "权限：local_write=%s, network=%s, publish=%s\n", job.Policy.LocalWrite, job.Policy.Network, job.Policy.Publish)
	b.WriteString("\n完成后给出结论。共享计划与记忆只能通过现有 <assistant-progress> 协议提交。")
	return b.String()
}

func (r *Runtime) reflectReadyOf(ctx context.Context, assistantID string) error {
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return err
	}
	return r.reflectReady(ctx, snapshot)
}

// keepJobLease renews a Job lease until the returned stop function is called.
// It mirrors keepRunLease for the Job lifecycle.
func (r *Runtime) keepJobLease(ctx context.Context, job assistant.RunnerJob) (context.Context, func() error) {
	jobCtx, cancel := context.WithCancel(ctx)
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
			case <-jobCtx.Done():
				return
			case now := <-ticker.C:
				if _, err := r.jobRunner.Renew(job, now); err != nil {
					errCh <- fmt.Errorf("assistant daemon: renew job %s: %w", job.ID, err)
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
	return jobCtx, stop
}

func errorsJoin(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w: %v", primary, secondary)
}

// runRoleCompletion runs one bounded, tool-free model turn for a role call
// (Dispatcher/Reflector/Ideator). The controller uses the stable Assistant
// system prompt; the role instruction and dynamic context live entirely in the
// prompt (the user turn). A frozen deny policy prevents any tool side effect.
func runRoleCompletion(ctx context.Context, model string, stderr io.Writer, prompt string) (string, error) {
	capture := &roleCapture{}
	ctrl, err := boot.Build(ctx, boot.Options{
		Model: model, RequireKey: true, Sink: event.FuncSink(capture.sink),
		Stderr: stderr, SessionDir: config.SessionDir(), SessionKind: agent.SessionKindAssistant,
		ApprovalTimeout: 2 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer ctrl.Close()
	ctrl.SetSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	if err := ctrl.RunWithPolicy(ctx, prompt, assistant.RolePermissionPolicy(), control.ToolApprovalAuto); err != nil {
		return "", err
	}
	if capture.overflow {
		return "", errors.New("assistant: role model output exceeded byte limit")
	}
	if strings.TrimSpace(capture.text) == "" {
		return "", errors.New("assistant: role model returned no output")
	}
	return capture.text, nil
}

const assistantRoleMaxOutputBytes = 256 * 1024

type roleCapture struct {
	text     string
	overflow bool
}

func (c *roleCapture) sink(value event.Event) {
	if value.Kind == event.Message {
		if text := strings.TrimSpace(value.Text); text != "" {
			if len(text) > assistantRoleMaxOutputBytes {
				c.text = ""
				c.overflow = true
				return
			}
			c.text = text
		}
	}
}
