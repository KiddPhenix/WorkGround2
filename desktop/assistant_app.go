package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/assistant"
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

type AssistantResumeRequest struct {
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
}

type AssistantCancelRequest struct {
	RunID     string `json:"runId"`
	RequestID string `json:"requestId"`
	Reason    string `json:"reason"`
}

type AssistantDiagnostic struct {
	At        time.Time `json:"at" ts_type:"string"`
	Operation string    `json:"operation"`
	Message   string    `json:"message"`
}

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
			At: time.Now(), Operation: "list", Message: listErr.Error(),
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
