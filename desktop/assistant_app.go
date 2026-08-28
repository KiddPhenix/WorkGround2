package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/workgate"
)

// AssistantCreateRequest is the typed Wails input for creating one durable
// assistant aggregate. RequestID is the caller-owned idempotency key.
type AssistantCreateRequest struct {
	RequestID     string              `json:"requestId"`
	Assistant     assistant.Assistant `json:"assistant"`
	Routines      []assistant.Routine `json:"routines"`
	InitialPrompt string              `json:"initialPrompt,omitempty"`
}

type AssistantUpdateRequest struct {
	RequestID        string              `json:"requestId"`
	ExpectedRevision int64               `json:"expectedRevision"`
	Assistant        assistant.Assistant `json:"assistant"`
}

type AssistantDeleteRequest struct {
	AssistantID      string `json:"assistantId"`
	RequestID        string `json:"requestId"`
	ExpectedRevision int64  `json:"expectedRevision"`
}

type AssistantPutRoutineRequest struct {
	RequestID        string            `json:"requestId"`
	ExpectedRevision int64             `json:"expectedRevision"`
	Routine          assistant.Routine `json:"routine"`
}

type AssistantApplyMemoryRequest struct {
	AssistantID      string                `json:"assistantId"`
	RequestID        string                `json:"requestId"`
	ExpectedRevision int64                 `json:"expectedRevision"`
	Patch            assistant.MemoryPatch `json:"patch"`
}

type AssistantPutChannelRequest struct {
	RequestID        string                   `json:"requestId"`
	ExpectedRevision int64                    `json:"expectedRevision"`
	Channel          assistant.ChannelBinding `json:"channel"`
	APIKey           string                   `json:"apiKey,omitempty"`
}

type AssistantRunNowRequest struct {
	AssistantID string `json:"assistantId"`
	RoutineID   string `json:"routineId,omitempty"`
	RequestID   string `json:"requestId"`
	MaxAttempts int    `json:"maxAttempts,omitempty"`
}

// AssistantSubmitInputRequest records a direct user message to the assistant
// ("对助手说") as a manual, non-routine Run. The normalized original text is
// stored without being rewritten into a task.
type AssistantSubmitInputRequest struct {
	AssistantID string `json:"assistantId"`
	RequestID   string `json:"requestId"`
	Input       string `json:"input"`
	MaxAttempts int    `json:"maxAttempts,omitempty"`
}

type AssistantResolveAttentionRequest struct {
	AssistantID      string                   `json:"assistantId"`
	AttentionID      string                   `json:"attentionId"`
	RequestID        string                   `json:"requestId"`
	ExpectedRevision int64                    `json:"expectedRevision"`
	State            assistant.AttentionState `json:"state"`
	Resolution       string                   `json:"resolution"`
}

type AssistantResolveProposalRequest struct {
	AssistantID      string                     `json:"assistantId"`
	ProposalID       string                     `json:"proposalId"`
	RequestID        string                     `json:"requestId"`
	ExpectedRevision int64                      `json:"expectedRevision"`
	Decision         assistant.ProposalDecision `json:"decision"`
	Resolution       string                     `json:"resolution,omitempty"`
}

type AssistantResumeRequest struct {
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
}

type AssistantCancelRequest struct {
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

// AssistantSubmitRequest routes a direct user input through the Dispatcher:
// it persists a Dispatch and classifies it. Execution happens via the converged
// supervisor loop, which turns classified task Dispatches into managed Sessions.
type AssistantSubmitRequest struct {
	AssistantID string `json:"assistantId"`
	RequestID   string `json:"requestId"`
	Input       string `json:"input"`
}

// AssistantIdeateRequest triggers a manual ideation.
type AssistantIdeateRequest struct {
	AssistantID string `json:"assistantId"`
	RequestID   string `json:"requestId"`
}

// AssistantResolveIdeaRequest accepts or rejects a pending idea proposal.
type AssistantResolveIdeaRequest struct {
	AssistantID      string                 `json:"assistantId"`
	IdeaID           string                 `json:"ideaId"`
	RequestID        string                 `json:"requestId"`
	ExpectedRevision int64                  `json:"expectedRevision"`
	Decision         assistant.IdeaDecision `json:"decision"`
	Resolution       string                 `json:"resolution,omitempty"`
}

// AssistantRetryJobRequest re-queues a failed/cancelled/waiting Job.
type AssistantRetryJobRequest struct {
	JobID     string `json:"jobId"`
	RequestID string `json:"requestId"`
}

// AssistantCancelJobRequest cancels a Job.
type AssistantCancelJobRequest struct {
	JobID     string `json:"jobId"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

// AssistantWorkControlRequest carries the idempotency key for a global
// pause/resume/restart intent.
type AssistantWorkControlRequest struct {
	RequestID string `json:"requestId"`
}

// AssistantPublishViewportRequest carries the user's current window observation.
type AssistantPublishViewportRequest struct {
	WindowID          string   `json:"windowId"`
	WorkspaceID       string   `json:"workspaceId,omitempty"`
	VisibleSessionIDs []string `json:"visibleSessionIds,omitempty"`
	SelectedSessionID string   `json:"selectedSessionId,omitempty"`
	UIRevision        int64    `json:"uiRevision,omitempty"`
}

type AssistantDiagnostic struct {
	At        time.Time `json:"at" ts_type:"string"`
	Category  string    `json:"category"`
	Operation string    `json:"operation"`
	Message   string    `json:"message"`
}

const (
	assistantDiagnosticData    = "data"
	assistantDiagnosticRuntime = "runtime"
)

type AssistantListResult struct {
	Items       []assistant.Assistant `json:"items"`
	Diagnostics []AssistantDiagnostic `json:"diagnostics"`
}

func (a *App) assistantRuntime() (*AssistantRuntime, error) {
	if a.assistant != nil {
		return a.assistant, nil
	}
	if a.assistantErr != nil {
		return nil, fmt.Errorf("assistant runtime unavailable: %w", a.assistantErr)
	}
	return nil, errors.New("assistant runtime is not started")
}

func (a *App) assistantContext() context.Context {
	if a != nil && a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// workGate returns the shared assistant work-control gate so ordinary (non-
// Assistant) tabs are fenced by the same persistent gate that pauses/resumes the
// assistant runtime. Nil when the assistant runtime or its store is unavailable.
func (a *App) workGate() workgate.Gate {
	if a == nil || a.assistant == nil || a.assistant.store == nil {
		return nil
	}
	return a.assistant.store.WorkGate()
}

func (a *App) AssistantList() (AssistantListResult, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantListResult{}, err
	}
	items, listErr := service.store.List()
	result := AssistantListResult{Items: items, Diagnostics: service.Diagnostics()}
	if listErr == nil {
		return result, nil
	}
	if errors.Is(listErr, assistant.ErrCorrupt) {
		result.Diagnostics = append(result.Diagnostics, AssistantDiagnostic{
			At: time.Now(), Category: assistantDiagnosticData, Operation: "list", Message: listErr.Error(),
		})
		return result, nil
	}
	return AssistantListResult{}, listErr
}

func (a *App) AssistantGet(assistantID string) (assistant.Snapshot, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Snapshot{}, err
	}
	snapshot, err := service.store.Get(assistantID)
	if err != nil {
		return assistant.Snapshot{}, err
	}
	a.reconcileAssistantSessionTitles(snapshot)
	return snapshot, nil
}

func (a *App) AssistantCreate(req AssistantCreateRequest) (assistant.Snapshot, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Snapshot{}, err
	}
	req.Assistant.ID = strings.TrimSpace(req.Assistant.ID)
	if req.Assistant.ID == "" {
		req.Assistant.ID = assistant.StableID("assistant", req.RequestID)
	}
	if req.Assistant.Lifecycle == "" {
		req.Assistant.Lifecycle = assistant.LifecycleActive
	}
	if req.Assistant.Scope == "" {
		if strings.TrimSpace(req.Assistant.WorkspaceRoot) == "" {
			req.Assistant.Scope = assistant.ScopeGlobal
		} else {
			req.Assistant.Scope = assistant.ScopeWorkspace
		}
	}
	if req.Assistant.Policy == (assistant.Policy{}) {
		req.Assistant.Policy = assistant.DefaultPolicy()
	}
	for i := range req.Routines {
		routine := &req.Routines[i]
		routine.ID = strings.TrimSpace(routine.ID)
		if routine.ID == "" {
			routine.ID = assistant.StableID("routine", fmt.Sprintf("%s/%s/%d", req.Assistant.ID, req.RequestID, i))
		}
		if routine.AssistantID == "" {
			routine.AssistantID = req.Assistant.ID
		}
		if routine.CatchUp == "" {
			routine.CatchUp = assistant.CatchUpCoalesceLatest
		}
		if routine.Schedule.Kind == "" {
			routine.Schedule.Kind = assistant.ScheduleManual
		}
	}
	snapshot, err := service.store.Create(assistant.CreateInput{
		RequestID:     req.RequestID,
		Assistant:     req.Assistant,
		Routines:      req.Routines,
		InitialPrompt: req.InitialPrompt,
		Now:           time.Now(),
	})
	if err != nil {
		return assistant.Snapshot{}, err
	}
	if strings.TrimSpace(req.InitialPrompt) != "" {
		service.Wake()
	}
	return snapshot, nil
}

func (a *App) AssistantUpdate(req AssistantUpdateRequest) (assistant.Assistant, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Assistant{}, err
	}
	return service.store.UpdateAssistant(req.RequestID, req.Assistant, req.ExpectedRevision, time.Now())
}

func (a *App) AssistantDelete(req AssistantDeleteRequest) error {
	service, err := a.assistantRuntime()
	if err != nil {
		return err
	}
	runIDs, err := service.store.Delete(req.RequestID, req.AssistantID, req.ExpectedRevision)
	if err != nil {
		return err
	}
	for _, runID := range runIDs {
		service.CancelRun(runID)
	}
	service.Wake()
	return nil
}

func (a *App) AssistantPutRoutine(req AssistantPutRoutineRequest) (assistant.Routine, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Routine{}, err
	}
	if req.Routine.CatchUp == "" {
		req.Routine.CatchUp = assistant.CatchUpCoalesceLatest
	}
	if req.Routine.Schedule.Kind == "" {
		req.Routine.Schedule.Kind = assistant.ScheduleManual
	}
	return service.store.PutRoutine(assistant.RoutineInput{
		RequestID: req.RequestID, Routine: req.Routine,
		ExpectedRevision: req.ExpectedRevision, Now: time.Now(),
	})
}

func (a *App) AssistantApplyMemory(req AssistantApplyMemoryRequest) (assistant.Memory, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Memory{}, err
	}
	return service.store.ApplyMemory(req.AssistantID, req.RequestID, req.ExpectedRevision, req.Patch, time.Now())
}

func (a *App) AssistantPutChannel(req AssistantPutChannelRequest) (assistant.ChannelBinding, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.ChannelBinding{}, err
	}
	req.Channel.ID = strings.TrimSpace(req.Channel.ID)
	req.Channel.AssistantID = strings.TrimSpace(req.Channel.AssistantID)
	if req.Channel.ID == "" {
		req.Channel.ID = assistant.StableID("channel", req.Channel.AssistantID+"/"+req.RequestID)
	}
	if req.Channel.Kind == "" {
		req.Channel.Kind = assistant.ChannelDiscourse
	}
	if req.Channel.CollectIntervalSeconds == 0 {
		req.Channel.CollectIntervalSeconds = 3600
	}
	// Credential references are host-owned. Never let the frontend select an
	// arbitrary environment key; edits retain the existing reference and new
	// channels receive a deterministic private key.
	req.Channel.CredentialKey = ""
	snapshot, err := service.store.Get(req.Channel.AssistantID)
	if err != nil {
		return assistant.ChannelBinding{}, err
	}
	for _, existing := range snapshot.Channels {
		if existing.ID == req.Channel.ID {
			req.Channel.CredentialKey = existing.CredentialKey
			break
		}
	}
	if req.Channel.CredentialKey == "" {
		req.Channel.CredentialKey = assistantChannelCredentialKey(req.Channel.AssistantID, req.Channel.ID)
	}
	key := req.Channel.CredentialKey
	apiKey := strings.TrimSpace(req.APIKey)
	previous := config.ResolveCredential(key)
	if apiKey == "" && strings.TrimSpace(previous.Value) == "" {
		return assistant.ChannelBinding{}, errors.New("assistant: Discourse API key is required")
	}
	if apiKey != "" {
		if _, err := config.SetCredential(key, apiKey); err != nil {
			return assistant.ChannelBinding{}, err
		}
	}
	result, err := service.store.PutChannel(assistant.PutChannelInput{RequestID: req.RequestID, ExpectedRevision: req.ExpectedRevision, Channel: req.Channel, Now: time.Now()})
	if err != nil && apiKey != "" {
		if previous.Value != "" {
			_, _ = config.SetCredential(key, previous.Value)
		} else {
			_ = config.RemoveCredential(key)
		}
	}
	return result, err
}

func assistantChannelCredentialKey(assistantID, channelID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(assistantID) + "/" + strings.TrimSpace(channelID)))
	return "ASSISTANT_CHANNEL_" + strings.ToUpper(hex.EncodeToString(sum[:12])) + "_API_KEY"
}

func (a *App) AssistantRunNow(req AssistantRunNowRequest) (assistant.Run, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Run{}, err
	}
	run, err := service.store.Trigger(assistant.TriggerInput{
		AssistantID: req.AssistantID, RoutineID: req.RoutineID, RequestID: req.RequestID,
		Trigger: assistant.TriggerManual, MaxAttempts: req.MaxAttempts, Now: time.Now(),
	})
	if err != nil {
		return assistant.Run{}, err
	}
	service.Wake()
	return run, nil
}

func (a *App) AssistantSubmitInput(req AssistantSubmitInputRequest) (assistant.Run, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Run{}, err
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return assistant.Run{}, errors.New("assistant: direct input must not be empty")
	}
	run, err := service.store.Trigger(assistant.TriggerInput{
		AssistantID: req.AssistantID, Prompt: input, RequestID: req.RequestID,
		Trigger: assistant.TriggerManual, MaxAttempts: req.MaxAttempts, Now: time.Now(),
	})
	if err != nil {
		return assistant.Run{}, err
	}
	service.Wake()
	return run, nil
}

func (a *App) AssistantResolveAttention(req AssistantResolveAttentionRequest) (assistant.AttentionItem, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.AttentionItem{}, err
	}
	item, err := service.store.ResolveAttention(assistant.ResolveAttentionInput{
		AssistantID: req.AssistantID, AttentionID: req.AttentionID, RequestID: req.RequestID,
		ExpectedRevision: req.ExpectedRevision, State: req.State, Resolution: req.Resolution, Now: time.Now(),
	})
	if err != nil {
		return assistant.AttentionItem{}, err
	}
	return *item, nil
}

func (a *App) AssistantResolveProposal(req AssistantResolveProposalRequest) (assistant.ChangeProposal, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.ChangeProposal{}, err
	}
	return service.store.ResolveProposal(assistant.ResolveProposalInput{
		AssistantID: req.AssistantID, ProposalID: req.ProposalID, RequestID: req.RequestID,
		ExpectedRevision: req.ExpectedRevision, Decision: req.Decision, Resolution: req.Resolution, Now: time.Now(),
	})
}

func (a *App) AssistantResume(req AssistantResumeRequest) (assistant.Run, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Run{}, err
	}
	run, err := service.store.Resume(assistant.ResumeInput{RunID: req.RunID, RequestID: req.RequestID, Now: time.Now()})
	if err != nil {
		return assistant.Run{}, err
	}
	service.Wake()
	return *run, nil
}

func (a *App) AssistantCancel(req AssistantCancelRequest) (assistant.Run, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Run{}, err
	}
	run, err := service.store.Cancel(assistant.CancelInput{
		RunID: req.RunID, RequestID: req.RequestID, Reason: req.Reason, Now: time.Now(),
	})
	if err != nil {
		return assistant.Run{}, err
	}
	service.CancelRun(req.RunID)
	return *run, nil
}

// AssistantSubmit durably opens a Dispatch for a direct input and returns the
// persisted pending Dispatch immediately, without waiting for the model reply.
// Background classification (woken below) then streams the reply and applies the
// validated result exactly once. The returned Dispatch is pending_classification
// on a fresh input; a replayed request ID returns the already-persisted Dispatch.
func (a *App) AssistantSubmit(req AssistantSubmitRequest) (assistant.Dispatch, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Dispatch{}, err
	}
	input := strings.TrimSpace(req.Input)
	if input == "" {
		return assistant.Dispatch{}, errors.New("assistant: direct input must not be empty")
	}
	dispatch, err := service.store.OpenDispatch(assistant.OpenDispatchInput{
		AssistantID: req.AssistantID, RequestID: req.RequestID, Input: input, Now: time.Now(),
	})
	if err != nil {
		return assistant.Dispatch{}, err
	}
	service.emitDispatchOpened(dispatch)
	// User input enters the durable supervisor event queue (mergeable, non-lossy)
	// so the supervisor wakes even when the input does not create a managed
	// Session (questions, feedback, control intents).
	service.enqueueSupervisorUserInput(req.AssistantID, req.RequestID, input)
	service.Wake()
	return dispatch, nil
}

// AssistantRetryDispatch re-runs classification for a Dispatch stuck in
// pending_classification or classification_failed. It returns the classified
// Dispatch, or an explicit error so the UI can surface a retryable failure.
func (a *App) AssistantRetryDispatch(assistantID, dispatchID, requestID string) (assistant.Dispatch, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Dispatch{}, err
	}
	dispatch, err := service.dispatcher.RetryDispatch(a.assistantContext(), assistantID, dispatchID, time.Now())
	if err != nil {
		service.emitDispatchTerminal(assistant.Dispatch{
			ID: dispatchID, AssistantID: assistantID,
			State: assistant.DispatchClassificationFailed,
			Error: &assistant.RunError{Code: "classification_unavailable", Message: err.Error()},
		})
		return assistant.Dispatch{}, err
	}
	service.emitDispatchTerminal(dispatch)
	service.Wake()
	return dispatch, nil
}

// AssistantIdeate triggers a manual ideation and returns the pending proposal.
func (a *App) AssistantIdeate(req AssistantIdeateRequest) (assistant.IdeaProposal, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.IdeaProposal{}, err
	}
	idea, err := service.ideator.Ideate(a.assistantContext(), assistant.OpenIdeaInput{
		AssistantID: req.AssistantID, RequestID: req.RequestID,
		Trigger: assistant.IdeaTriggerManual, Now: time.Now(),
	})
	if err != nil {
		return assistant.IdeaProposal{}, err
	}
	service.Wake()
	return idea, nil
}

// AssistantResolveIdea accepts or rejects a pending idea proposal.
func (a *App) AssistantResolveIdea(req AssistantResolveIdeaRequest) (assistant.IdeaProposal, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.IdeaProposal{}, err
	}
	return service.store.ResolveIdea(assistant.ResolveIdeaInput{
		AssistantID: req.AssistantID, IdeaID: req.IdeaID, RequestID: req.RequestID,
		ExpectedRevision: req.ExpectedRevision, Decision: req.Decision, Resolution: req.Resolution, Now: time.Now(),
	})
}

// AssistantRetryJob is disabled: the legacy RunnerJob execution path is
// decommissioned and Jobs are historical/read-only.
func (a *App) AssistantRetryJob(req AssistantRetryJobRequest) (assistant.RunnerJob, error) {
	return assistant.RunnerJob{}, errors.New("assistant: 旧 Job 路径已停用，仅历史只读")
}

// AssistantCancelJob is disabled: the legacy RunnerJob execution path is
// decommissioned and Jobs are historical/read-only.
func (a *App) AssistantCancelJob(req AssistantCancelJobRequest) (assistant.RunnerJob, error) {
	return assistant.RunnerJob{}, errors.New("assistant: 旧 Job 路径已停用，仅历史只读")
}

// AssistantWorkControl returns the current global work gate (state/epoch/
// revision + host-observed active work + next hint).
func (a *App) AssistantWorkControl() (AssistantWorkControlView, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	return service.WorkControl()
}

// AssistantPauseAll pauses all Assistant work and cancels in-flight sessions.
func (a *App) AssistantPauseAll(req AssistantWorkControlRequest) (AssistantWorkControlView, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	return service.PauseAll(req.RequestID)
}

// AssistantResumeAll resumes all Assistant work and wakes the runtime loop.
func (a *App) AssistantResumeAll(req AssistantWorkControlRequest) (AssistantWorkControlView, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	return service.ResumeAll(req.RequestID)
}

// AssistantPauseForRestart quiesces work and records a one-shot restart intent.
func (a *App) AssistantPauseForRestart(req AssistantWorkControlRequest) (AssistantWorkControlView, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantWorkControlView{}, err
	}
	return service.PauseForRestart(req.RequestID)
}

// AssistantPublishViewport records the user's current window observation as a
// short-lived snapshot. It only submits intent; the backend treats it as
// unknown once its TTL expires.
func (a *App) AssistantPublishViewport(req AssistantPublishViewportRequest) {
	service, err := a.assistantRuntime()
	if err != nil {
		return
	}
	service.PublishViewport(assistant.ViewportSnapshot{
		WindowID:          req.WindowID,
		WorkspaceID:       req.WorkspaceID,
		VisibleSessionIDs: req.VisibleSessionIDs,
		SelectedSessionID: req.SelectedSessionID,
		ObservedAt:        time.Now().UTC(),
		UIRevision:        req.UIRevision,
	})
}

// AssistantViewport returns the most recently focused still-valid viewport
// snapshot, or ok=false when it is expired or unknown.
func (a *App) AssistantViewport() (assistant.ViewportSnapshot, bool) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.ViewportSnapshot{}, false
	}
	return service.CurrentViewport(time.Now())
}

// PickAssistantWorkspace opens a native directory chooser for the create dialog
// and returns the picked path ("" with no error when cancelled). It only returns
// a path — it never registers a workspace, switches tabs, or creates an
// assistant. defaultDir seeds the chooser from the field's current input.
func (a *App) PickAssistantWorkspace(defaultDir string) (string, error) {
	if a.ctx == nil {
		return "", nil
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title:            "Choose assistant workspace",
		DefaultDirectory: dialogDefaultDirectory(defaultDir),
	})
	if err != nil || dir == "" {
		return "", err
	}
	return filepath.Clean(dir), nil
}

// CreateAssistantWorkspace creates a single directory under parentDir and
// returns its clean absolute path. It rejects empty values, absolute names,
// "." / "..", and path separators so the name cannot escape the parent.
// Creating an already-existing directory is an idempotent success; an existing
// file at the target fails explicitly.
func (a *App) CreateAssistantWorkspace(parentDir, name string) (string, error) {
	parentDir = strings.TrimSpace(parentDir)
	name = strings.TrimSpace(name)
	if parentDir == "" {
		return "", errors.New("assistant: workspace parent directory must not be empty")
	}
	if name == "" {
		return "", errors.New("assistant: workspace name must not be empty")
	}
	if name == "." || name == ".." {
		return "", errors.New(`assistant: workspace name must not be "." or ".."`)
	}
	if filepath.IsAbs(name) {
		return "", errors.New("assistant: workspace name must not be an absolute path")
	}
	if strings.ContainsAny(name, `/\`) {
		return "", errors.New("assistant: workspace name must not contain path separators")
	}
	parent, err := filepath.Abs(parentDir)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, name)
	if filepath.Dir(target) != parent {
		return "", errors.New("assistant: workspace path escapes parent directory")
	}
	info, statErr := os.Stat(target)
	if statErr == nil {
		if info.IsDir() {
			return filepath.Clean(target), nil
		}
		return "", fmt.Errorf("assistant: workspace path already exists as a file: %s", target)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return "", statErr
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	return filepath.Clean(target), nil
}
