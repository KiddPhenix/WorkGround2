package control

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"workground2/internal/agent"
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
	Provider string
	Model    string
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
	artifactTexts map[string]string
}

// NewTaskExecutorAdapter returns a Task executor for one provider/model profile.
func NewTaskExecutorAdapter(profile TaskExecutorProfile, factory TaskSessionFactory) *TaskExecutorAdapter {
	return &TaskExecutorAdapter{
		profile:       profile,
		factory:       factory,
		active:        make(map[string]*activeTask),
		finished:      make(map[string]bool),
		cancels:       make(map[string]taskCancelResult),
		artifactTexts: make(map[string]string),
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

	runErr := ctrl.RunTurn(taskCtx, input.Prompt)
	snapshotErr := ctrl.Snapshot()
	metaErr := agent.SetBranchSource(sessionPath, taskSessionSource(input))
	cause := errors.Join(runErr, snapshotErr, metaErr)

	finishedAt := time.Now().UTC()
	history := ctrl.History()
	if cause == nil && len(input.ProducesSlotIDs) > 0 {
		content := taskSessionArtifactContent(history)
		if content != "" {
			a.mu.Lock()
			a.artifactTexts[targetKey] = content
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
	attempt := &work.Attempt{
		ID:              input.AttemptID,
		Index:           input.AttemptIndex,
		State:           work.RunCompleted,
		SessionRef:      ref,
		StartedAt:       startedAt,
		FinishedAt:      &finishedAt,
		SideEffectClass: input.SideEffectClass,
	}
	if cause == nil {
		return attempt, nil
	}

	operation := "run"
	if snapshotErr != nil || metaErr != nil {
		operation = "persist_session"
	}
	retryable := taskErrorRetryable(runErr)
	if snapshotErr != nil || metaErr != nil {
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
// response is preserved in the Work blob store and referenced from each
// declared textual/document slot. Binary/image slots require a dedicated
// producer and fail explicitly instead of being reported as completed.
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
	content := a.artifactTexts[key]
	a.mu.Unlock()
	if strings.TrimSpace(content) == "" {
		return nil, errors.New("work task completed without a materializable final response")
	}
	defer func() {
		a.mu.Lock()
		delete(a.artifactTexts, key)
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
	digest, err := a.blobs.Put(input.WorkID, []byte(content))
	if err != nil {
		return nil, fmt.Errorf("persist task artifact content: %w", err)
	}
	now := time.Now().UTC()
	outputs := make([]work.TaskArtifactOutput, 0, len(input.ProducesSlotIDs))
	for _, slotID := range input.ProducesSlotIDs {
		slot, ok := slotByID[slotID]
		if !ok {
			return nil, fmt.Errorf("artifact slot %q is unavailable in the active Work projection", slotID)
		}
		name, mediaType, supported := taskTextArtifactName(slot)
		if !supported {
			return nil, fmt.Errorf(
				"artifact slot %q requires %q output; the task returned text only",
				slot.ID,
				slot.Kind,
			)
		}
		refID := strings.TrimPrefix(work.ContentDigest([]byte(input.AttemptID+"\x00"+slot.ID)), "sha256:")
		outputs = append(outputs, work.TaskArtifactOutput{
			SlotID: slot.ID,
			Refs: []work.ArtifactRef{{
				ID:             "task-" + refID[:24],
				Name:           name,
				Type:           mediaType,
				Status:         work.ArtifactRefStatusAvailable,
				BlobDigest:     digest,
				SourceRunID:    input.RunID,
				LastVerifiedAt: &now,
			}},
			Summary: firstLine(content, 120),
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

func taskTextArtifactName(slot work.ArtifactSlot) (name, mediaType string, supported bool) {
	kind := strings.ToLower(strings.TrimSpace(slot.Kind))
	switch kind {
	case "text", "markdown", "md", "document", "txt", "plain_text", "text/plain", "text/markdown":
	default:
		return "", "", false
	}
	base := strings.TrimSpace(slot.Title)
	if base == "" {
		base = strings.TrimSpace(slot.ID)
	}
	base = strings.Map(func(r rune) rune {
		switch r {
		case '<', '>', ':', '"', '/', '\\', '|', '?', '*':
			return '-'
		default:
			if r < 32 {
				return -1
			}
			return r
		}
	}, base)
	base = strings.Trim(strings.TrimSpace(base), ".")
	if base == "" {
		base = "artifact"
	}
	if kind == "txt" || kind == "text/plain" || strings.Contains(strings.ToLower(slot.ID), "txt") {
		return base + ".txt", "text/plain", true
	}
	return base + ".md", "text/markdown", true
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
