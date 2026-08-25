package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"workground2/internal/assistant"
)

// AssistantCreateRequest is the typed Wails input for creating one durable
// assistant aggregate. RequestID is the caller-owned idempotency key.
type AssistantCreateRequest struct {
	RequestID string              `json:"requestId"`
	Assistant assistant.Assistant `json:"assistant"`
	Routines  []assistant.Routine `json:"routines"`
}

type AssistantUpdateRequest struct {
	RequestID        string              `json:"requestId"`
	ExpectedRevision int64               `json:"expectedRevision"`
	Assistant        assistant.Assistant `json:"assistant"`
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
	return service.store.Get(assistantID)
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
	return service.store.Create(assistant.CreateInput{
		RequestID: req.RequestID,
		Assistant: req.Assistant,
		Routines:  req.Routines,
		Now:       time.Now(),
	})
}

func (a *App) AssistantUpdate(req AssistantUpdateRequest) (assistant.Assistant, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return assistant.Assistant{}, err
	}
	return service.store.UpdateAssistant(req.RequestID, req.Assistant, req.ExpectedRevision, time.Now())
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
