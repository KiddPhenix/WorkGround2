package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
	"workground2/internal/artifact"
	"workground2/internal/provider"
	"workground2/internal/work"
)

var (
	// ErrTaskSessionNotRunning reports a cancellation request for a Session that
	// is no longer owned by this executor.
	ErrTaskSessionNotRunning = work.ErrTaskNotRunning
	// ErrTaskCancelConflict reports reuse of one cancellation request ID for a
	// different Session.
	ErrTaskCancelConflict = errors.New("work task cancellation request conflict")
)

// TaskSessionFactory creates one independently persisted Controller Session.
// Cleanup releases factory-owned runtime resources; it must not delete the
// persisted Session referenced by Controller.SessionPath().
type TaskSessionFactory func(context.Context, work.TaskExecuteInput) (*Controller, func(), error)

// TaskExecutorProfile identifies the provider and model used by Task Sessions.
// It is copied into structured errors without exposing prompts or credentials.
type TaskExecutorProfile struct {
	Provider           string
	Model              string
	NativeCapabilities []string
}

// TaskRunError reports a sanitized Task execution failure while preserving its
// original cause for errors.Is/errors.As checks.
type TaskRunError struct {
	Provider  string
	Model     string
	WorkID    string
	RunID     string
	StageID   string
	TaskID    string
	Attempt   int
	Operation string
	Retryable bool
	cause     error
}

func (e *TaskRunError) Error() string {
	if e == nil {
		return "work task execution failed"
	}
	return fmt.Sprintf(
		"work task %s failed: provider=%q model=%q work=%q run=%q stage=%q task=%q attempt=%d retryable=%t",
		e.Operation, e.Provider, e.Model, e.WorkID, e.RunID, e.StageID, e.TaskID, e.Attempt, e.Retryable,
	)
}

func (e *TaskRunError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type taskCancelResult struct {
	targetKey string
}

type activeTask struct {
	ctrl            *Controller
	cancel          context.CancelFunc
	begun           bool
	cancelRequested bool
}

// TaskExecutorAdapter maps the narrow work.TaskExecutor port to isolated
// Controller Sessions. It tracks active Sessions so cancellation is real and
// repeated cancellation request IDs are safe.
type TaskExecutorAdapter struct {
	profile TaskExecutorProfile
	factory TaskSessionFactory
	workSvc WorkService
	blobs   work.BlobStore

	mu            sync.Mutex
	active        map[string]*activeTask
	finished      map[string]bool
	cancels       map[string]taskCancelResult
	taskArtifacts map[string]taskArtifactData
}

type taskArtifactData struct {
	text          string
	workspaceRoot string
	artifacts     []artifact.Discovered
}

// NewTaskExecutorAdapter returns a Task executor for one provider/model profile.
func NewTaskExecutorAdapter(profile TaskExecutorProfile, factory TaskSessionFactory) *TaskExecutorAdapter {
	return &TaskExecutorAdapter{
		profile:       profile,
		factory:       factory,
		active:        make(map[string]*activeTask),
		finished:      make(map[string]bool),
		cancels:       make(map[string]taskCancelResult),
		taskArtifacts: make(map[string]taskArtifactData),
	}
}

// SetWorkService attaches the optional Work service for Cornerstone context
// injection during task execution. Nil is safe and disables injection.
func (a *TaskExecutorAdapter) SetWorkService(svc WorkService) {
	if a == nil {
		return
	}
	a.workSvc = svc
}

// SetArtifactStore attaches the content-addressed store used to materialize a
// V2 task's final response as a durable textual ArtifactRef.
func (a *TaskExecutorAdapter) SetArtifactStore(store work.BlobStore) {
	if a == nil {
		return
	}
	a.blobs = store
}

// ExecuteTask runs one synchronous Controller turn and persists its Session
// before returning the lightweight reference to Work.
func (a *TaskExecutorAdapter) ExecuteTask(ctx context.Context, input work.TaskExecuteInput) (*work.Attempt, error) {
	if err := validateTaskInput(input); err != nil {
		return nil, a.taskError(input, "validate", false, err)
	}
	if a == nil || a.factory == nil {
		return nil, a.taskError(input, "create_session", false, errors.New("Task Session factory is not configured"))
	}
	if strings.TrimSpace(a.profile.Provider) == "" || strings.TrimSpace(a.profile.Model) == "" {
		return nil, a.taskError(input, "create_session", false, errors.New("Task provider and model are required"))
	}
	if err := ctx.Err(); err != nil {
		return nil, a.taskError(input, "create_session", false, err)
	}
	targetKey := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	cancelled, err := a.beginTask(targetKey)
	if err != nil {
		return nil, a.taskError(input, "register_attempt", false, err)
	}
	defer a.finishTask(targetKey)
	startedAt := time.Now().UTC()
	input.StartedAt = startedAt
	if cancelled {
		return cancelledAttempt(input, startedAt), a.taskError(input, "cancel", false, context.Canceled)
	}
	taskCtx, taskCancel := context.WithCancel(ctx)
	defer taskCancel()
	if a.attachCancel(targetKey, taskCancel) {
		return cancelledAttempt(input, startedAt), a.taskError(input, "cancel", false, context.Canceled)
	}

	ctrl, cleanup, err := a.factory(taskCtx, input)
	if err != nil {
		if ctrl != nil {
			ctrl.Close()
		}
		if cleanup != nil {
			cleanup()
		}
		return nil, a.taskError(input, "create_session", taskErrorRetryable(err), err)
	}
	if ctrl == nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, a.taskError(input, "create_session", false, errors.New("Task Session factory returned a nil Controller"))
	}
	defer func() {
		ctrl.Close()
		if cleanup != nil {
			cleanup()
		}
	}()

	sessionPath := ctrl.SessionPath()
	sessionKey := agent.CanonicalSessionPath(sessionPath)
	if sessionKey == "" {
		return nil, a.taskError(input, "create_session", false, errors.New("Task Session path is empty"))
	}
	if err := agent.SetBranchSource(sessionPath, taskSessionSource(input)); err != nil {
		return nil, a.taskError(input, "persist_session", true, err)
	}
	liveRef := work.SessionRef{
		SessionPath: sessionPath,
		BranchID:    agent.BranchID(sessionPath),
		ModelRef:    controllerModelRef(ctrl, a.profile.Model),
		StartedAt:   startedAt,
	}
	if input.Live != nil {
		if err := input.Live(work.TaskLiveUpdate{SessionRef: &liveRef}); err != nil {
			return nil, a.taskError(input, "publish_session", true, err)
		}
	}
	if a.attachController(targetKey, ctrl) {
		return cancelledAttempt(input, startedAt), a.taskError(input, "cancel", false, context.Canceled)
	}

	// Inject Work Cornerstone context as a transient block before the turn.
	// Fail-closed: any Get/BuildCornerstoneContext failure blocks execution
	// before RunTurn to prevent running with missing or broken context.
	if a.workSvc != nil && input.WorkID != "" {
		finishContext, contextErr := a.injectCornerstoneBlock(taskCtx, ctrl, input)
		if contextErr != nil {
			finishedAt := time.Now().UTC()
			taskErr := a.taskError(input, "cornerstone_context", true, contextErr)
			attempt := &work.Attempt{
				ID:              input.AttemptID,
				Index:           input.AttemptIndex,
				State:           work.RunFailed,
				SessionRef:      work.SessionRef{},
				StartedAt:       startedAt,
				FinishedAt:      &finishedAt,
				SideEffectClass: input.SideEffectClass,
				Error:           taskErr.Error(),
			}
			return attempt, taskErr
		}
		defer finishContext()
	}

	// Execute capability preflights before the first provider call.
	// Each preflight calls request_help through the standard tool path so
	// the result (success or failure) is visible in session history.
	// Failures are non-blocking: the main model sees the error and can
	// fall back to Shell/file-based artifact discovery.
	runPrompt := input.Prompt
	if len(input.SlotPreflights) > 0 {
		var terminal []string
		for _, pf := range input.SlotPreflights {
			if err := taskCtx.Err(); err != nil {
				return cancelledAttempt(input, startedAt), a.taskError(input, "preflight", false, err)
			}
			callID := preflightCallID(input, pf.SlotID, pf.SlotIndex)
			args, err := buildRequestHelpArgs(pf.Capability, pf.Prompt)
			if err != nil {
				slog.Warn("work: preflight args build failed",
					"work_id", input.WorkID,
					"task_id", input.TaskID,
					"slot_id", pf.SlotID,
					"capability", pf.Capability,
					"error", err,
				)
				continue
			}
			_, execErr := ctrl.executeToolCall(taskCtx, callID, pf.Prompt, "request_help", args)
			if execErr != nil {
				terminal = append(terminal, fmt.Sprintf(
					"- slot %q item %d capability %q failed above; fallback is now unlocked. Do not retry the same preflight unless its tool result explicitly asks for a retry.",
					pf.SlotID, pf.SlotIndex+1, pf.Capability,
				))
				slog.Warn("work: preflight failed",
					"work_id", input.WorkID,
					"task_id", input.TaskID,
					"slot_id", pf.SlotID,
					"capability", pf.Capability,
					"error", execErr,
				)
				continue
			}
			terminal = append(terminal, fmt.Sprintf(
				"- slot %q item %d capability %q succeeded above; consume that tool result and do not generate a replacement or call the same capability again.",
				pf.SlotID, pf.SlotIndex+1, pf.Capability,
			))
		}
		if len(terminal) > 0 {
			runPrompt += "\n\nHost capability preflight terminal states:\n" + strings.Join(terminal, "\n")
		}
	}

	// Execute capability preflights for RequiredCapabilities that the model
	// lacks natively and for which no direct tool is available. Each preflight
	// calls request_help through the standard tool path so the result (success
	// or failure) is visible in session history. Failures are non-blocking:
	// the main model sees the error and can attempt direct search or report
	// the failure.
	if len(input.RequiredCapabilities) > 0 {
		directToolNames := directToolNamesFromController(ctrl)
		var capTerminal []string
		seen := make(map[string]bool, len(input.RequiredCapabilities))
		for _, cap := range input.RequiredCapabilities {
			cap = strings.ToLower(strings.TrimSpace(cap))
			if cap == "" || seen[cap] {
				continue
			}
			seen[cap] = true

			// Only web_search uses this generic preflight. image_generation is
			// handled per output by SlotPreflights; future capabilities must opt
			// in explicitly instead of being sent to request_help by accident.
			if cap != "web_search" {
				continue
			}

			// Model has native support — no preflight needed.
			if hasNativeCapability(a.profile.NativeCapabilities, cap) {
				continue
			}

			// A direct tool is available (e.g. web_search or mcp__*__web_search)
			// that the model can call itself.
			if hasDirectCapabilityTool(directToolNames, cap) {
				continue
			}

			if err := taskCtx.Err(); err != nil {
				return cancelledAttempt(input, startedAt), a.taskError(input, "preflight", false, err)
			}

			callID := capabilityPreflightCallID(input, cap)
			preflightPrompt := buildCapabilityPreflightPrompt(cap, input.Prompt)
			args, err := buildRequestHelpArgs(cap, preflightPrompt)
			if err != nil {
				slog.Warn("work: capability preflight args build failed",
					"work_id", input.WorkID,
					"task_id", input.TaskID,
					"capability", cap,
					"error", err,
				)
				continue
			}
			_, execErr := ctrl.executeToolCall(taskCtx, callID, preflightPrompt, "request_help", args)
			if execErr != nil {
				capTerminal = append(capTerminal, fmt.Sprintf(
					"- capability %q preflight failed above; the model should attempt direct search or report the failure. Do not retry the same preflight unless its tool result explicitly asks for a retry.",
					cap,
				))
				slog.Warn("work: capability preflight failed",
					"work_id", input.WorkID,
					"task_id", input.TaskID,
					"capability", cap,
					"error", execErr,
				)
				continue
			}
			capTerminal = append(capTerminal, fmt.Sprintf(
				"- capability %q preflight succeeded above; consume that tool result and do not generate a replacement or call the same capability again.",
				cap,
			))
		}
		if len(capTerminal) > 0 {
			runPrompt += "\n\nHost capability preflight terminal states (required capabilities):\n" + strings.Join(capTerminal, "\n")
		}
	}

	runErr := ctrl.RunTurn(taskCtx, runPrompt)
	if runErr == nil && len(input.AcceptanceCriteria) > 0 {
		runErr = ctrl.RunTurn(taskCtx, taskQualityReviewPrompt(input.AcceptanceCriteria))
	}
	snapshotErr := ctrl.Snapshot()
	cause := errors.Join(runErr, snapshotErr)

	finishedAt := time.Now().UTC()
	history := ctrl.History()
	if cause == nil && len(input.ProducesSlotIDs) > 0 {
		content := taskSessionArtifactContent(history)
		discovered := artifact.Collect(history, artifact.DefaultProducers())
		if content != "" || len(discovered) > 0 {
			a.mu.Lock()
			a.taskArtifacts[targetKey] = taskArtifactData{
				text:          content,
				workspaceRoot: ctrl.WorkspaceRoot(),
				artifacts:     discovered,
			}
			a.mu.Unlock()
		}
	}
	ref := work.SessionRef{
		SessionPath: sessionPath,
		BranchID:    agent.BranchID(sessionPath),
		ModelRef:    controllerModelRef(ctrl, a.profile.Model),
		TurnCount:   countUserTurns(history),
		Preview:     taskSessionPreview(history),
		StartedAt:   startedAt,
	}
	var liveErr error
	if input.Live != nil {
		liveErr = input.Live(work.TaskLiveUpdate{SessionRef: &ref})
		cause = errors.Join(cause, liveErr)
	}
	attempt := &work.Attempt{
		ID:                     input.AttemptID,
		Index:                  input.AttemptIndex,
		State:                  work.RunCompleted,
		SessionRef:             ref,
		StartedAt:              startedAt,
		FinishedAt:             &finishedAt,
		SideEffectClass:        input.SideEffectClass,
		LastAssistantText:      taskLastAssistantText(history),
		SuccessfulCapabilities: taskSuccessfulCapabilities(history, a.profile.NativeCapabilities),
	}
	if cause == nil {
		return attempt, nil
	}

	operation := "run"
	if snapshotErr != nil {
		operation = "persist_session"
	} else if liveErr != nil {
		operation = "publish_session"
	}
	retryable := taskErrorRetryable(runErr)
	if snapshotErr != nil || liveErr != nil {
		retryable = true
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		attempt.State = work.RunCancelled
		retryable = false
	} else {
		attempt.State = work.RunFailed
	}
	taskErr := a.taskError(input, operation, retryable, cause)
	attempt.Error = taskErr.Error()
	return attempt, taskErr
}

// TaskArtifacts implements work.TaskArtifactReporter. The final assistant
// response is materialized according to each declared slot's format before it
// is preserved in the Work blob store.
func (a *TaskExecutorAdapter) TaskArtifacts(
	ctx context.Context,
	input work.TaskExecuteInput,
	_ *work.Attempt,
) ([]work.TaskArtifactOutput, error) {
	if len(input.ProducesSlotIDs) == 0 {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if a == nil || a.blobs == nil {
		return nil, errors.New("work task artifact store is not configured")
	}
	if a.workSvc == nil {
		return nil, errors.New("work task artifact projection is not configured")
	}
	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	a.mu.Lock()
	data := a.taskArtifacts[key]
	a.mu.Unlock()
	if strings.TrimSpace(data.text) == "" && len(data.artifacts) == 0 {
		return nil, errors.New("work task completed without a materializable final response")
	}
	defer func() {
		a.mu.Lock()
		delete(a.taskArtifacts, key)
		a.mu.Unlock()
	}()
	view, err := a.workSvc.Get(ctx, input.WorkID)
	if err != nil {
		return nil, fmt.Errorf("load artifact slots: %w", err)
	}
	if view == nil || view.Work == nil {
		return nil, fmt.Errorf("load artifact slots: Work %q is unavailable", input.WorkID)
	}
	slots := view.ArtifactSlots
	if len(slots) == 0 {
		slots = view.Work.V2ArtifactSlots
	}
	slotByID := make(map[string]work.ArtifactSlot, len(slots))
	for _, slot := range slots {
		if current, ok := slotByID[slot.ID]; !ok || slot.DefinitionRev > current.DefinitionRev {
			slotByID[slot.ID] = slot
		}
	}
	now := time.Now().UTC()
	outputs := make([]work.TaskArtifactOutput, 0, len(input.ProducesSlotIDs))
	usedArtifacts := make(map[int]bool, len(data.artifacts))
	for _, slotID := range input.ProducesSlotIDs {
		slot, ok := slotByID[slotID]
		if !ok {
			return nil, fmt.Errorf("artifact slot %q is unavailable in the active Work projection", slotID)
		}
		expectedCount := slot.ExpectedCount
		if expectedCount <= 0 {
			expectedCount = 1
		}
		indexes := takeArtifacts(data.artifacts, usedArtifacts, slot.Kind, expectedCount)
		useTextFallback := len(indexes) == 0 && textArtifactKind(slot.Kind) && strings.TrimSpace(data.text) != ""
		if !useTextFallback && len(indexes) != expectedCount {
			return nil, fmt.Errorf(
				"artifact slot %q requires %d %q artifact(s); the task returned %d unconsumed match(es)",
				slot.ID, expectedCount, slot.Kind, len(indexes),
			)
		}
		if useTextFallback && expectedCount != 1 {
			return nil, fmt.Errorf(
				"artifact slot %q requires %d %q artifact(s); one textual final response cannot satisfy the count",
				slot.ID, expectedCount, slot.Kind,
			)
		}
		if useTextFallback {
			indexes = []int{-1}
		}
		refs := make([]work.ArtifactRef, 0, len(indexes))
		names := make([]string, 0, len(indexes))
		for refIndex, artifactIndex := range indexes {
			var discovered *artifact.Discovered
			if artifactIndex >= 0 {
				discovered = &data.artifacts[artifactIndex]
			}
			body, name, mediaType, supported, err := materializeTaskArtifact(
				slot, data.text, discovered, data.workspaceRoot,
			)
			if err != nil {
				return nil, fmt.Errorf("materialize artifact slot %q as %q: %w", slot.ID, slot.Kind, err)
			}
			if !supported {
				return nil, fmt.Errorf(
					"artifact slot %q requires %q output; the task returned no matching artifact",
					slot.ID, slot.Kind,
				)
			}
			digest, err := a.blobs.Put(input.WorkID, body)
			if err != nil {
				return nil, fmt.Errorf("persist artifact slot %q content: %w", slot.ID, err)
			}
			refKey := fmt.Sprintf("%s\x00%s\x00%d\x00%s", input.AttemptID, slot.ID, refIndex, digest)
			refID := strings.TrimPrefix(work.ContentDigest([]byte(refKey)), "sha256:")
			refs = append(refs, work.ArtifactRef{
				ID:             "task-" + refID[:24],
				Name:           name,
				Type:           mediaType,
				Status:         work.ArtifactRefStatusAvailable,
				RelativePath:   taskArtifactRelativePath(discovered, data.workspaceRoot),
				BlobDigest:     digest,
				SourceRunID:    input.RunID,
				LastVerifiedAt: &now,
			})
			names = append(names, name)
			if artifactIndex >= 0 {
				usedArtifacts[artifactIndex] = true
			}
		}
		summary := firstLine(data.text, 120)
		if summary == "" {
			summary = strings.Join(names, ", ")
		}
		outputs = append(outputs, work.TaskArtifactOutput{
			SlotID:  slot.ID,
			Refs:    refs,
			Summary: summary,
		})
	}
	return outputs, nil
}

// CancelTask records cancellation by stable Attempt ownership before consulting
// Session state. A cancellation that arrives before Session creation is held
// and consumed by ExecuteTask, so the empty-SessionRef window is safe.
func (a *TaskExecutorAdapter) CancelTask(ctx context.Context, input work.TaskCancelInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil {
		return errors.New("work task cancellation requires a Task executor")
	}
	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		return errors.New("work task cancellation requires requestID")
	}
	targetKey := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	if err := validateTaskTarget(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID); err != nil {
		return fmt.Errorf("work task cancellation: %w", err)
	}

	a.mu.Lock()
	if previous, ok := a.cancels[requestID]; ok {
		a.mu.Unlock()
		if previous.targetKey != targetKey {
			return fmt.Errorf("%w: requestID=%q", ErrTaskCancelConflict, requestID)
		}
		return nil
	}
	if a.finished[targetKey] {
		a.mu.Unlock()
		return fmt.Errorf("%w: attempt=%q", ErrTaskSessionNotRunning, input.AttemptID)
	}
	active := a.active[targetKey]
	if active == nil {
		active = &activeTask{}
		a.active[targetKey] = active
	}
	active.cancelRequested = true
	ctrl := active.ctrl
	cancel := active.cancel
	a.cancels[requestID] = taskCancelResult{targetKey: targetKey}
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ctrl != nil {
		ctrl.Cancel()
	}
	return nil
}

func (a *TaskExecutorAdapter) attachCancel(key string, cancel context.CancelFunc) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	active := a.active[key]
	if active == nil {
		return true
	}
	active.cancel = cancel
	return active.cancelRequested
}

func (a *TaskExecutorAdapter) beginTask(key string) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	active := a.active[key]
	if active == nil {
		active = &activeTask{}
		a.active[key] = active
	}
	if active.begun {
		return false, fmt.Errorf("attempt %q is already executing", key)
	}
	active.begun = true
	delete(a.finished, key)
	return active.cancelRequested, nil
}

func (a *TaskExecutorAdapter) attachController(key string, ctrl *Controller) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	active := a.active[key]
	if active == nil {
		return true
	}
	active.ctrl = ctrl
	return active.cancelRequested
}

func (a *TaskExecutorAdapter) finishTask(key string) {
	a.mu.Lock()
	delete(a.active, key)
	a.finished[key] = true
	a.mu.Unlock()
}

func cancelledAttempt(input work.TaskExecuteInput, startedAt time.Time) *work.Attempt {
	finishedAt := time.Now().UTC()
	return &work.Attempt{
		ID: input.AttemptID, Index: input.AttemptIndex, State: work.RunCancelled,
		StartedAt: startedAt, FinishedAt: &finishedAt, SideEffectClass: input.SideEffectClass,
	}
}

func taskAttemptKey(workID, runID, stageID, taskID, attemptID string) string {
	return strings.Join([]string{workID, runID, stageID, taskID, attemptID}, "\x00")
}

func validateTaskTarget(workID, runID, stageID, taskID, attemptID string) error {
	for _, field := range []struct{ name, value string }{
		{"workID", workID}, {"runID", runID}, {"stageID", stageID},
		{"taskID", taskID}, {"attemptID", attemptID},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	return nil
}

func (a *TaskExecutorAdapter) taskError(input work.TaskExecuteInput, operation string, retryable bool, cause error) *TaskRunError {
	profile := TaskExecutorProfile{}
	if a != nil {
		profile = a.profile
	}
	return &TaskRunError{
		Provider:  strings.TrimSpace(profile.Provider),
		Model:     strings.TrimSpace(profile.Model),
		WorkID:    input.WorkID,
		RunID:     input.RunID,
		StageID:   input.StageID,
		TaskID:    input.TaskID,
		Attempt:   input.AttemptIndex,
		Operation: operation,
		Retryable: retryable,
		cause:     cause,
	}
}

func validateTaskInput(input work.TaskExecuteInput) error {
	required := []struct {
		name  string
		value string
	}{
		{"workID", input.WorkID},
		{"runID", input.RunID},
		{"stageID", input.StageID},
		{"taskID", input.TaskID},
		{"attemptID", input.AttemptID},
		{"requestID", input.RequestID},
		{"definitionDigest", input.DefinitionDigest},
		{"prompt", input.Prompt},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if input.AttemptIndex < 0 {
		return errors.New("attemptIndex must be non-negative")
	}
	seenPreflights := make(map[string]struct{}, len(input.SlotPreflights))
	for i, pf := range input.SlotPreflights {
		if strings.TrimSpace(pf.SlotID) == "" {
			return fmt.Errorf("slotPreflights[%d].slotId is required", i)
		}
		if pf.SlotIndex < 0 {
			return fmt.Errorf("slotPreflights[%d].slotIndex must be non-negative", i)
		}
		if strings.TrimSpace(pf.Capability) == "" {
			return fmt.Errorf("slotPreflights[%d].capability is required", i)
		}
		if strings.TrimSpace(pf.Prompt) == "" {
			return fmt.Errorf("slotPreflights[%d].prompt is required", i)
		}
		key := fmt.Sprintf("%s\x00%d", pf.SlotID, pf.SlotIndex)
		if _, duplicate := seenPreflights[key]; duplicate {
			return fmt.Errorf("slotPreflights[%d] duplicates slot %q item %d", i, pf.SlotID, pf.SlotIndex)
		}
		seenPreflights[key] = struct{}{}
	}
	for i, criterion := range input.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("acceptanceCriteria[%d] is required", i)
		}
	}
	for i, capability := range input.RequiredCapabilities {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("requiredCapabilities[%d] is required", i)
		}
	}
	return nil
}

func taskErrorRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var authErr *provider.AuthError
	if errors.As(err, &authErr) {
		return false
	}
	var apiErr *provider.APIError
	if errors.As(err, &apiErr) {
		return provider.RetryableStatus(apiErr.Status)
	}
	return true
}

func controllerModelRef(ctrl *Controller, fallback string) string {
	ctrl.mu.Lock()
	modelRef := strings.TrimSpace(ctrl.modelRef)
	ctrl.mu.Unlock()
	if modelRef == "" {
		return strings.TrimSpace(fallback)
	}
	return modelRef
}

func countUserTurns(messages []provider.Message) int {
	turns := 0
	for _, message := range messages {
		if message.Role == provider.RoleUser {
			turns++
		}
	}
	return turns
}

func taskSessionPreview(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role != provider.RoleAssistant {
			continue
		}
		if text := strings.TrimSpace(message.Content); text != "" {
			return firstLine(text, 120)
		}
	}
	return ""
}

func taskSessionArtifactContent(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != provider.RoleAssistant {
			continue
		}
		if content := strings.TrimSpace(messages[i].Content); content != "" {
			return strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
		}
	}
	return ""
}

// taskLastAssistantText returns the full content of the last assistant message.
func taskLastAssistantText(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleAssistant {
			return messages[i].Content
		}
	}
	return ""
}

func taskQualityReviewPrompt(criteria []string) string {
	var b strings.Builder
	b.WriteString("Perform the mandatory final quality pass now. Re-read the original task, the upstream inputs, and your previous delivery. Return a corrected, complete replacement delivery; do not return a review, checklist, summary, or a claim that the work passes.\n\nAcceptance criteria:\n")
	for i, criterion := range criteria {
		fmt.Fprintf(&b, "%d. %s\n", i+1, strings.TrimSpace(criterion))
	}
	b.WriteString("\nPreserve valid citations and generated artifacts. If a criterion cannot be satisfied, fail explicitly with the blocking reason instead of claiming completion.")
	return b.String()
}

// taskSuccessfulCapabilities derives objective capability evidence from paired,
// successful tool calls. Text that merely claims a search happened is ignored.
func taskSuccessfulCapabilities(messages []provider.Message, nativeCapabilities []string) []string {
	successful := make(map[string]bool)
	for _, message := range messages {
		if message.Role != provider.RoleTool || strings.TrimSpace(message.ToolCallID) == "" {
			continue
		}
		if !taskToolResultFailed(message.Content) {
			successful[message.ToolCallID] = true
		}
	}
	capabilities := make(map[string]bool)
	finalText := taskLastAssistantText(messages)
	for _, capability := range nativeCapabilities {
		switch strings.ToLower(strings.TrimSpace(capability)) {
		case "web_search":
			if strings.Contains(finalText, "https://") || strings.Contains(finalText, "http://") {
				capabilities["web_search"] = true
			}
		}
	}
	for _, message := range messages {
		if message.Role != provider.RoleAssistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if !successful[call.ID] {
				continue
			}
			switch taskToolCapability(call) {
			case "web_search":
				capabilities["web_search"] = true
			case "image_generation":
				capabilities["image_generation"] = true
			}
		}
	}
	out := make([]string, 0, len(capabilities))
	for capability := range capabilities {
		out = append(out, capability)
	}
	sort.Strings(out)
	return out
}

func taskToolCapability(call provider.ToolCall) string {
	name := strings.ToLower(strings.TrimSpace(call.Name))
	switch {
	case name == "web_search", strings.HasSuffix(name, "__web_search"):
		return "web_search"
	case name == "image_generation", name == "draw_image", strings.HasSuffix(name, "__image_generation"):
		return "image_generation"
	case name != "request_help":
		return ""
	}
	var args struct {
		Capability string `json:"capability"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(args.Capability))
}

func taskToolResultFailed(content string) bool {
	content = strings.TrimSpace(content)
	return strings.HasPrefix(content, "error:") ||
		strings.HasPrefix(content, "blocked:") ||
		strings.HasPrefix(content, "Error:") ||
		strings.HasPrefix(content, "[error")
}

func firstLine(text string, maxRunes int) string {
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		text = text[:index]
	}
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	if maxRunes <= 1 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-1]) + "…"
}

func taskSessionSource(input work.TaskExecuteInput) string {
	return fmt.Sprintf(
		"work:%s/run:%s/stage:%s/task:%s/attempt:%d/request:%s",
		input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptIndex, input.RequestID,
	)
}

// PublishTaskSession makes a freshly allocated hidden Session observable before
// slower Controller setup or the first provider token. Repeating the call with
// the same input is safe: branch ownership and live runtime updates are both
// idempotent.
func PublishTaskSession(input work.TaskExecuteInput, sessionPath, modelRef string) (work.SessionRef, error) {
	sessionPath = strings.TrimSpace(sessionPath)
	if sessionPath == "" {
		return work.SessionRef{}, errors.New("Task Session path is empty")
	}
	if err := agent.SetBranchSource(sessionPath, taskSessionSource(input)); err != nil {
		return work.SessionRef{}, err
	}
	startedAt := input.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	ref := work.SessionRef{
		SessionPath: sessionPath,
		BranchID:    agent.BranchID(sessionPath),
		ModelRef:    strings.TrimSpace(modelRef),
		StartedAt:   startedAt,
	}
	if input.Live != nil {
		if err := input.Live(work.TaskLiveUpdate{SessionRef: &ref}); err != nil {
			return work.SessionRef{}, err
		}
	}
	return ref, nil
}

// preflightCallID generates a stable, deterministic tool-call ID for one
// preflight invocation. The ID is safe to replay: repeated calls with the same
// arguments produce the same ID.
func preflightCallID(input work.TaskExecuteInput, slotID string, index int) string {
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d",
		input.WorkID, input.RunID, input.TaskID, input.AttemptID, slotID, input.RequestID, index)
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("preflight-%x", sum[:8])
}

// buildRequestHelpArgs marshals a capability+prompt pair into the JSON that
// the request_help tool expects.
func buildRequestHelpArgs(capability, prompt string) (json.RawMessage, error) {
	args := struct {
		Capability string `json:"capability"`
		Prompt     string `json:"prompt"`
	}{
		Capability: capability,
		Prompt:     prompt,
	}
	data, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal request_help args: %w", err)
	}
	return json.RawMessage(data), nil
}

// hasNativeCapability reports whether cap is among the model's native
// capabilities (case-insensitive).
func hasNativeCapability(native []string, cap string) bool {
	cap = strings.ToLower(strings.TrimSpace(cap))
	for _, n := range native {
		if strings.ToLower(strings.TrimSpace(n)) == cap {
			return true
		}
	}
	return false
}

// hasDirectCapabilityTool reports whether any tool named `cap` or
// `mcp__*__cap` is among the provided tool names, making a preflight
// unnecessary because the model can call it directly.
func hasDirectCapabilityTool(toolNames []string, cap string) bool {
	cap = strings.ToLower(cap)
	for _, name := range toolNames {
		lower := strings.ToLower(strings.TrimSpace(name))
		if lower == cap || strings.HasSuffix(lower, "__"+cap) {
			return true
		}
	}
	return false
}

// directToolNamesFromController extracts the current tool names from a
// Controller's combined (builtin + MCP) registry.
func directToolNamesFromController(ctrl *Controller) []string {
	if ctrl == nil {
		return nil
	}
	entries := ctrl.ToolContractEntries()
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.Name
	}
	return names
}

// capabilityPreflightCallID generates a stable, deterministic tool-call ID
// for one RequiredCapabilities preflight invocation. The ID is safe to replay:
// repeated calls with the same arguments produce the same ID.
func capabilityPreflightCallID(input work.TaskExecuteInput, capability string) string {
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		input.WorkID, input.RunID, input.TaskID, input.AttemptID, capability, input.RequestID)
	sum := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("cap-pf-%x", sum[:8])
}

// buildCapabilityPreflightPrompt constructs a self-contained prompt for a
// capability preflight using the task's semantic context.
func buildCapabilityPreflightPrompt(capability, taskPrompt string) string {
	return fmt.Sprintf("Search with context from the following task. Return URLs and key findings.\n\n--- Task ---\n%s", taskPrompt)
}

var _ work.TaskExecutor = (*TaskExecutorAdapter)(nil)
var _ work.TaskArtifactReporter = (*TaskExecutorAdapter)(nil)

// injectCornerstoneBlock fetches the Work and builds the cornerstone context
// block, sets it on the Controller for Compose to inject. Returns an error
// when cornerstone fetch or context building fails — callers must block
// execution before RunTurn to avoid running with missing context.
func (a *TaskExecutorAdapter) injectCornerstoneBlock(ctx context.Context, ctrl *Controller, input work.TaskExecuteInput) (func(), error) {
	view, err := a.workSvc.Get(ctx, input.WorkID)
	if err != nil {
		return nil, fmt.Errorf("cornerstone context: get work %q: %w", input.WorkID, err)
	}
	if view == nil || view.Work == nil {
		return nil, fmt.Errorf("cornerstone context: work %q not found", input.WorkID)
	}

	config := work.CornerstoneContextConfig{
		MaxTokens:  work.DefaultCornerstoneContextMaxTokens,
		MaxPerItem: work.DefaultCornerstoneContextMaxPerItem,
	}
	var block work.CornerstoneContextBlock
	if builder, ok := a.workSvc.(interface {
		BuildCornerstoneContext(context.Context, string, work.CornerstoneContextConfig) (work.CornerstoneContextBlock, error)
	}); ok {
		block, err = builder.BuildCornerstoneContext(ctx, input.WorkID, config)
	} else {
		block, err = work.BuildCornerstoneContext(view.Work.Cornerstones, config)
	}
	if err != nil {
		return nil, fmt.Errorf("cornerstone context: build: %w", err)
	}
	// Blocking should never happen here because the Service already checked
	// before creating this task. If it does, fail closed.
	if block.Blocking {
		return nil, fmt.Errorf("cornerstone context: required cornerstones not ready (Service pre-flight bypassed)")
	}
	if block.Degraded {
		slog.Warn("work: cornerstone context degraded",
			"work_id", input.WorkID,
			"skipped_ids", block.SkippedIDs,
			"issue_count", len(block.Assessment.Issues),
		)
	}
	token, err := ctrl.beginCornerstoneTurn(input.WorkID, block.XML, block.ActiveCount)
	if err != nil {
		return nil, err
	}
	return func() { ctrl.finishCornerstoneTurn(token) }, nil
}
