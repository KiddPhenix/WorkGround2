package assistantdaemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
	"workground2/internal/tool/sessiontool"
)

// processDispatches advances the phase-6 roles for every assistant: it
// classifies pending/failed Dispatches with the deterministic classifier,
// reflects Dispatches whose sessions are terminal, and opens cadence ideas when
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

// advanceClassifiedDispatches creates and submits a managed Session for each
// classified task Dispatch that has no Session yet, then binds it. The
// Dispatcher only classifies; the daemon loop creates the Session through the
// headless SessionControl.
func (r *Runtime) advanceClassifiedDispatches(now time.Time) error {
	assistants, err := r.store.List()
	if err != nil {
		return err
	}
	var issues []error
	for _, a := range assistants {
		snapshot, err := r.store.Get(a.ID)
		if err != nil {
			issues = append(issues, err)
			continue
		}
		for _, d := range snapshot.Dispatches {
			if d.State != assistant.DispatchClassified || d.Kind != assistant.DispatchTask || d.SessionID != "" {
				continue
			}
			requestID := assistant.StableID("request", "dispatch-session/"+d.ID)
			sessionID, err := r.sessionControl.Create(sessiontool.SessionCreateRequest{
				Title: d.Input, Prompt: assistant.ManagedSessionPrompt(snapshot, d.Input), IntentPrompt: d.Input, OwnerID: a.ID, Purpose: agent.PurposeManaged,
				Workspace: snapshot.Assistant.WorkspaceRoot, RequestID: requestID,
			})
			if err != nil {
				issues = append(issues, err)
				continue
			}
			if _, err := r.store.BindDispatchSession(assistant.BindDispatchSessionInput{
				RequestID: requestID, AssistantID: a.ID, DispatchID: d.ID, SessionID: sessionID, Now: now,
			}); err != nil {
				issues = append(issues, err)
			}
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

// runRoleCompletion runs one bounded, tool-free model turn for a role call
// (Dispatcher/Reflector/Ideator). The controller uses the stable Assistant
// system prompt; the role instruction and dynamic context live entirely in the
// prompt (the user turn). A frozen deny policy prevents any tool side effect.
func runRoleCompletion(ctx context.Context, model string, stderr io.Writer, prompt string) (string, error) {
	role, _ := assistant.RoleContextFrom(ctx)
	workspace := ""
	sessionDir := config.SessionDir()
	if role.Scope == assistant.ScopeWorkspace {
		workspace = strings.TrimSpace(role.WorkspaceRoot)
		if workspace == "" {
			return "", errors.New("assistant role workspace is required")
		}
		sessionDir = config.ProjectSessionDir(workspace)
		if strings.TrimSpace(sessionDir) == "" {
			return "", errors.New("assistant role session dir is unavailable")
		}
	}
	capture := &roleCapture{}
	ctrl, err := boot.Build(ctx, boot.Options{
		Model: model, RequireKey: true, Sink: event.FuncSink(capture.sink),
		Stderr: stderr, WorkspaceRoot: workspace, SessionDir: sessionDir, SessionKind: agent.SessionKindAssistant,
		ApprovalTimeout: 2 * time.Second,
	})
	if err != nil {
		return "", err
	}
	defer ctrl.Close()
	ctrl.SetSessionPath(agent.NewSessionPath(ctrl.SessionDir(), ctrl.Label()))
	meta, err := agent.EnsureBranchMeta(ctrl.SessionPath())
	if err != nil {
		return "", err
	}
	meta.SessionKind = agent.SessionKindAssistant
	meta.SessionSource = agent.SessionSourceAssist
	meta.AssistantID = strings.TrimSpace(role.AssistantID)
	meta.ToolApprovalMode = control.ToolApprovalAuto
	if role.Scope == assistant.ScopeWorkspace {
		meta.Scope = "project"
		meta.WorkspaceRoot = workspace
	} else {
		meta.Scope = "global"
		meta.WorkspaceRoot = ""
	}
	if err := agent.SaveBranchMetaPreserveUpdated(ctrl.SessionPath(), meta); err != nil {
		return "", err
	}
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
