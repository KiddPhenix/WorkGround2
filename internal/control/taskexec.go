package control

import (
	"context"
	"errors"
	"fmt"
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
	ErrTaskSessionNotRunning = errors.New("work task session is not running")
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
	sessionKey string
	err        error
}

// TaskExecutorAdapter maps the narrow work.TaskExecutor port to isolated
// Controller Sessions. It tracks active Sessions so cancellation is real and
// repeated cancellation request IDs are safe.
type TaskExecutorAdapter struct {
	profile TaskExecutorProfile
	factory TaskSessionFactory

	mu      sync.Mutex
	active  map[string]*Controller
	cancels map[string]taskCancelResult
}

// NewTaskExecutorAdapter returns a Task executor for one provider/model profile.
func NewTaskExecutorAdapter(profile TaskExecutorProfile, factory TaskSessionFactory) *TaskExecutorAdapter {
	return &TaskExecutorAdapter{
		profile: profile,
		factory: factory,
		active:  make(map[string]*Controller),
		cancels: make(map[string]taskCancelResult),
	}
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

	startedAt := time.Now().UTC()
	ctrl, cleanup, err := a.factory(ctx, input)
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
	a.setActive(sessionKey, ctrl)
	defer a.clearActive(sessionKey, ctrl)

	runErr := ctrl.RunTurn(ctx, input.Prompt)
	snapshotErr := ctrl.Snapshot()
	metaErr := agent.SetBranchSource(sessionPath, taskSessionSource(input))
	cause := errors.Join(runErr, snapshotErr, metaErr)

	finishedAt := time.Now().UTC()
	history := ctrl.History()
	ref := work.SessionRef{
		SessionPath: sessionPath,
		BranchID:    agent.BranchID(sessionPath),
		ModelRef:    controllerModelRef(ctrl, a.profile.Model),
		TurnCount:   countUserTurns(history),
		Preview:     taskSessionPreview(history),
		StartedAt:   startedAt,
	}
	attempt := &work.Attempt{
		Index:      input.AttemptIndex,
		State:      work.RunCompleted,
		SessionRef: ref,
		StartedAt:  startedAt,
		FinishedAt: &finishedAt,
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

// CancelTask cancels an active Controller turn. Repeating the same request ID
// for the same Session returns the original result without another side effect.
func (a *TaskExecutorAdapter) CancelTask(ctx context.Context, session work.SessionRef, requestID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil {
		return errors.New("work task cancellation requires a Task executor")
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("work task cancellation requires requestID")
	}
	sessionKey := agent.CanonicalSessionPath(session.SessionPath)
	if sessionKey == "" {
		return errors.New("work task cancellation requires sessionPath")
	}

	a.mu.Lock()
	if previous, ok := a.cancels[requestID]; ok {
		a.mu.Unlock()
		if previous.sessionKey != sessionKey {
			return fmt.Errorf("%w: requestID=%q", ErrTaskCancelConflict, requestID)
		}
		return previous.err
	}
	ctrl := a.active[sessionKey]
	var result error
	if ctrl == nil {
		result = fmt.Errorf("%w: session=%q", ErrTaskSessionNotRunning, session.BranchID)
	}
	a.cancels[requestID] = taskCancelResult{sessionKey: sessionKey, err: result}
	a.mu.Unlock()
	if result != nil {
		return result
	}
	ctrl.Cancel()
	return nil
}

func (a *TaskExecutorAdapter) setActive(key string, ctrl *Controller) {
	a.mu.Lock()
	a.active[key] = ctrl
	a.mu.Unlock()
}

func (a *TaskExecutorAdapter) clearActive(key string, ctrl *Controller) {
	a.mu.Lock()
	if a.active[key] == ctrl {
		delete(a.active, key)
	}
	a.mu.Unlock()
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
