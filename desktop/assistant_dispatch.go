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
	"workground2/internal/boot"
	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/event"
)

// processDispatches advances the phase-6 roles: classify pending/failed
// Dispatches, reflect terminal Dispatches, and open cadence ideas when due. All
// mutations are idempotent and Store-CAS-guarded. It never executes Jobs — the
// converged supervisor loop turns classified task Dispatches into managed
// Sessions.
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
		result, err := r.dispatcher.RetryDispatch(ctx, dispatch.AssistantID, dispatch.ID, time.Now())
		if err != nil {
			r.emitDispatchTerminal(assistant.Dispatch{
				ID: dispatch.ID, AssistantID: dispatch.AssistantID, RequestID: dispatch.RequestID,
				State: assistant.DispatchClassificationFailed,
				Error: &assistant.RunError{Code: "classification_unavailable", Message: err.Error()},
			})
			return fmt.Errorf("classify dispatch %s: %w", dispatch.ID, err)
		}
		r.emitDispatchTerminal(result)
	}
	return nil
}

const assistantDispatchStreamChannel = "assistant:dispatch-stream"

const (
	assistantDispatchPhaseAccepted  = "accepted"
	assistantDispatchPhaseStreaming = "streaming"
	assistantDispatchPhaseCommitted = "committed"
	assistantDispatchPhaseFailed    = "failed"
)

// assistantDispatchStreamEvent is the typed Wails event payload for the inline
// live reply. Reply carries the cumulative decoded reply ("" for accepted and
// for failed previews); it is provisional until phase committed.
type assistantDispatchStreamEvent struct {
	AssistantID string `json:"assistantId"`
	DispatchID  string `json:"dispatchId"`
	RequestID   string `json:"requestId"`
	Sequence    int64  `json:"sequence"`
	Phase       string `json:"phase"`
	Reply       string `json:"reply,omitempty"`
	JobCount    int    `json:"jobCount,omitempty"`
	Revision    int64  `json:"revision,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (r *AssistantRuntime) emitDispatchStream(event assistantDispatchStreamEvent) {
	if r == nil || r.app == nil || r.app.ctx == nil {
		return
	}
	event.Sequence = r.dispatchSeq.Add(1)
	r.app.runtimeEvents.Emit(r.app.ctx, assistantDispatchStreamChannel, event)
}

func (r *AssistantRuntime) emitDispatchAccepted(dispatch assistant.Dispatch) {
	r.emitDispatchStream(assistantDispatchStreamEvent{
		AssistantID: dispatch.AssistantID, DispatchID: dispatch.ID, RequestID: dispatch.RequestID,
		Phase: assistantDispatchPhaseAccepted, Revision: dispatch.Revision,
	})
}

// emitDispatchOpened projects the durable state returned by OpenDispatch. A
// fresh/replayed pending Dispatch is accepted; an idempotent replay of an
// already-classified or failed Dispatch must keep its terminal truth instead
// of briefly regressing the UI to "understanding".
func (r *AssistantRuntime) emitDispatchOpened(dispatch assistant.Dispatch) {
	if dispatch.State == assistant.DispatchPendingClassification {
		r.emitDispatchAccepted(dispatch)
		return
	}
	r.emitDispatchTerminal(dispatch)
}

func (r *AssistantRuntime) emitDispatchPreview(preview assistant.ReplyPreview) {
	r.emitDispatchStream(assistantDispatchStreamEvent{
		AssistantID: preview.AssistantID, DispatchID: preview.DispatchID, RequestID: preview.RequestID,
		Phase: assistantDispatchPhaseStreaming, Reply: preview.Reply,
	})
}

// emitDispatchTerminal closes a classification with the validated, durable
// Dispatch: committed carries the authoritative reply and job count, failed
// carries an explicit retryable error and no reply.
func (r *AssistantRuntime) emitDispatchTerminal(dispatch assistant.Dispatch) {
	event := assistantDispatchStreamEvent{
		AssistantID: dispatch.AssistantID, DispatchID: dispatch.ID, RequestID: dispatch.RequestID,
		Phase: assistantDispatchPhaseCommitted, Reply: dispatch.Reply, Revision: dispatch.Revision,
	}
	if dispatch.State == assistant.DispatchClassificationFailed || dispatch.Error != nil {
		event.Phase = assistantDispatchPhaseFailed
		event.Reply = ""
		if dispatch.Error != nil {
			event.Error = dispatch.Error.Message
		}
	} else {
		event.JobCount = r.dispatchJobCount(dispatch.AssistantID, dispatch.ID)
	}
	r.emitDispatchStream(event)
}

func (r *AssistantRuntime) dispatchJobCount(assistantID, dispatchID string) int {
	if r == nil || r.store == nil {
		return 0
	}
	snapshot, err := r.store.Get(assistantID)
	if err != nil {
		return 0
	}
	count := 0
	for _, job := range snapshot.Jobs {
		if job.DispatchID == dispatchID {
			count++
		}
	}
	return count
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

// assistantRoleModel adapts the App's headless role controller to the
// assistant.RoleModel contract. It is also a StreamRoleModel so the Dispatcher
// can stream best-effort reply previews while the role turn runs.
type assistantRoleModel struct {
	app *App
}

func (m assistantRoleModel) Complete(ctx context.Context, prompt string) (string, error) {
	return m.app.runRoleCompletion(ctx, prompt)
}

func (m assistantRoleModel) CompleteStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	return m.app.runRoleCompletionStream(ctx, prompt, onDelta)
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

// runRoleCompletionStream is runRoleCompletion with answer-text deltas forwarded
// to onDelta for streaming previews. The final Message remains authoritative.
func (a *App) runRoleCompletionStream(ctx context.Context, prompt string, onDelta func(string)) (string, error) {
	capture := &streamRoleCapture{onDelta: onDelta}
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

type streamRoleCapture struct {
	text     string
	overflow bool
	onDelta  func(string)
}

func (c *streamRoleCapture) sink(value event.Event) {
	switch value.Kind {
	case event.Text:
		if c.onDelta != nil && value.Text != "" {
			c.onDelta(value.Text)
		}
	case event.Message:
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
