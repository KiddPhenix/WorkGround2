package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/assistantchannel"
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
)

// processDispatches advances the phase-6 roles: classify pending/failed
// Dispatches, reflect terminal Dispatches, and open cadence ideas when due. All
// mutations are idempotent and Store-CAS-guarded.
func (r *AssistantRuntime) processDispatches(ctx context.Context) error {
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
	return errors.Join(issues...)
}

func (r *AssistantRuntime) classifyPending(ctx context.Context, snapshot assistant.Snapshot) error {
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

func (r *AssistantRuntime) reflectReady(ctx context.Context, snapshot assistant.Snapshot) error {
	for _, dispatch := range assistant.DispatchesReadyForReflection(snapshot, time.Now()) {
		if _, err := r.reflector.Reflect(ctx, dispatch.AssistantID, dispatch.ID, "reflect:"+dispatch.ID, time.Now()); err != nil {
			return fmt.Errorf("reflect dispatch %s: %w", dispatch.ID, err)
		}
	}
	return nil
}

func (r *AssistantRuntime) reflectReadyOf(ctx context.Context, assistantID string) error {
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return err
	}
	return r.reflectReady(ctx, snapshot)
}

func (r *AssistantRuntime) ideateIfDue(ctx context.Context, snapshot assistant.Snapshot) error {
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

type jobCapture struct {
	summary, text string
	attention     bool
	evidence      assistant.Evidence
}

func (c *jobCapture) sink(value event.Event) {
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
	case event.AskRequest:
		c.attention = true
	}
}

// executeJob runs one claimed Job through a headless controller and then
// finishes or fails the Job. It keeps the user's active tab untouched.
func (r *AssistantRuntime) executeJob(ctx context.Context, job assistant.RunnerJob) {
	defer r.Wake()
	a := r.app
	workspace := ""
	if job.Scope == assistant.ScopeWorkspace {
		workspace = job.WorkspaceRoot
		info, err := os.Stat(workspace)
		if err != nil || !info.IsDir() {
			cause := fmt.Errorf("frozen workspace unavailable: %s", workspace)
			r.failJob(job, "workspace_unavailable", cause, false)
			return
		}
	}
	snapshot, err := r.store.Get(job.AssistantID)
	if err != nil {
		r.failJob(job, "snapshot", err, true)
		return
	}
	capture := &jobCapture{}
	sessionDir := config.SessionDir()
	if workspace != "" {
		sessionDir = config.ProjectSessionDir(workspace)
	}
	ctrl, err := boot.Build(ctx, boot.Options{Model: "", RequireKey: true, Sink: event.FuncSink(capture.sink), Stderr: os.Stderr, WorkspaceRoot: workspace, SessionDir: sessionDir, SessionKind: agent.SessionKindAssistant, ExtraTools: assistantchannel.Tools(r.channels, job.AssistantID, job.ID), ApprovalTimeout: 2 * time.Second, SessionRefs: a.sessionRefs, SessionRefsErr: a.sessionRefsErr})
	if err != nil {
		r.failJob(job, "controller_start", err, true)
		return
	}
	defer ctrl.Close()
	ctrl.SetSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	meta, _ := agent.EnsureBranchMeta(ctrl.SessionPath())
	meta.SessionKind, meta.AssistantID = agent.SessionKindAssistant, job.AssistantID
	_ = agent.SaveBranchMetaPreserveUpdated(ctrl.SessionPath(), meta)
	if err := ctx.Err(); err != nil {
		r.failJob(job, "runtime_stopped", err, true)
		return
	}
	prompt := r.jobPrompt(snapshot, job)
	runErr := ctrl.RunWithPolicy(ctx, prompt, assistant.PermissionPolicy(job.Policy), control.ToolApprovalAuto)
	if capture.attention {
		r.failJob(job, "attention", errors.New("job requires user attention"), false)
		return
	}
	if runErr != nil {
		r.failJob(job, "turn_failed", runErr, true)
		return
	}
	if missing := capture.evidence.Missing(assistant.RequiredCapabilities(job.Prompt, job.Prompt)); len(missing) > 0 {
		failure := assistant.EvidenceFailure(missing)
		failure.Now = time.Now()
		failure.RetryAfter = time.Minute
		_, failErr := r.jobRunner.Fail(job, failure)
		if failErr != nil {
			r.recordDiagnostic("job_evidence", failErr)
		}
		return
	}
	summary := strings.TrimSpace(assistant.StripProgressBlocks(capture.summary))
	if summary == "" {
		summary = strings.TrimSpace(capture.text)
	}
	if _, err := r.jobRunner.Finish(job, summary, time.Now()); err != nil {
		r.recordDiagnostic("job_finish", err)
	}
}

func (r *AssistantRuntime) failJob(job assistant.RunnerJob, code string, cause error, retryable bool) {
	if cause == nil {
		return
	}
	r.recordDiagnostic(code, cause)
	_, err := r.jobRunner.Fail(job, assistant.Failure{Code: code, Message: cause.Error(), Retryable: retryable, OutcomeKnown: true, RetryAfter: time.Minute, Now: time.Now()})
	if err != nil {
		r.recordDiagnostic(code, err)
	}
}

func (r *AssistantRuntime) jobPrompt(snapshot assistant.Snapshot, job assistant.RunnerJob) string {
	var b strings.Builder
	b.WriteString("你正在执行一个长期助手的 Runner Job。\n\n")
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

// runRoleCompletion runs one bounded, tool-free model turn for a role call
// (Dispatcher/Reflector/Ideator). The controller uses the stable Assistant
// system prompt; the role instruction and dynamic context live entirely in the
// prompt (the user turn). A frozen deny policy prevents any tool side effect.
func (a *App) runRoleCompletion(ctx context.Context, prompt string) (string, error) {
	capture := &roleCapture{}
	ctrl, err := boot.Build(ctx, boot.Options{
		Model: "", RequireKey: true, Sink: event.FuncSink(capture.sink),
		Stderr: os.Stderr, SessionDir: config.SessionDir(), SessionKind: agent.SessionKindAssistant,
		ApprovalTimeout: 2 * time.Second, SessionRefs: a.sessionRefs, SessionRefsErr: a.sessionRefsErr,
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
