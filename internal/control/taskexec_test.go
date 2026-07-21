package control

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/work"
)

type taskProvider struct {
	name    string
	text    string
	err     error
	started chan struct{}
	calls   atomic.Int32
}

func (p *taskProvider) Name() string { return p.name }

func (p *taskProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	if p.started != nil {
		close(p.started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if p.err != nil {
		return nil, p.err
	}
	chunks := make(chan provider.Chunk, 2)
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: p.text}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)
	return chunks, nil
}

func taskInput() work.TaskExecuteInput {
	return work.TaskExecuteInput{
		WorkID:           "work-1",
		RunID:            "run-1",
		StageID:          "stage-1",
		TaskID:           "task-1",
		AttemptIndex:     2,
		RequestID:        "request-1",
		DefinitionDigest: "sha256:definition",
		Prompt:           "private prompt with secret-token",
	}
}

func taskFactory(t *testing.T, prov provider.Provider, modelRef string, paths chan<- string, cleaned *atomic.Bool) TaskSessionFactory {
	t.Helper()
	dir := t.TempDir()
	return func(context.Context, work.TaskExecuteInput) (*Controller, func(), error) {
		path := agent.NewSessionPath(dir, "work-task")
		if paths != nil {
			paths <- path
		}
		session := agent.NewSession("stable system prompt")
		executor := agent.New(prov, tool.NewRegistry(), session, agent.Options{}, event.Discard)
		ctrl := New(Options{
			Runner:       executor,
			Executor:     executor,
			ModelRef:     modelRef,
			SessionDir:   dir,
			SessionPath:  path,
			SystemPrompt: "stable system prompt",
		})
		return ctrl, func() {
			if cleaned != nil {
				cleaned.Store(true)
			}
		}, nil
	}
}

func TestTaskExecutorPersistsLightweightSessionRef(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "concise result\nprivate detail"}
	var cleaned atomic.Bool
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, &cleaned),
	)

	attempt, err := exec.ExecuteTask(context.Background(), taskInput())
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted || attempt.Index != 2 {
		t.Fatalf("attempt = %+v", attempt)
	}
	ref := attempt.SessionRef
	if ref.SessionPath == "" || ref.BranchID != agent.BranchID(ref.SessionPath) {
		t.Fatalf("SessionRef path/branch = %+v", ref)
	}
	if ref.ModelRef != "fake/model-v1" || ref.TurnCount != 1 || ref.Preview != "concise result" {
		t.Fatalf("SessionRef metadata = %+v", ref)
	}
	if strings.Contains(ref.Preview, "private prompt") || strings.Contains(ref.Preview, "secret-token") {
		t.Fatalf("SessionRef preview leaked prompt: %q", ref.Preview)
	}
	if _, statErr := os.Stat(ref.SessionPath); statErr != nil {
		t.Fatalf("persisted Session missing after ExecuteTask: %v", statErr)
	}
	meta, ok, metaErr := agent.LoadBranchMeta(ref.SessionPath)
	if metaErr != nil || !ok {
		t.Fatalf("LoadBranchMeta = (%+v, %v, %v)", meta, ok, metaErr)
	}
	if meta.Model != "fake/model-v1" || meta.SessionSource != "work:work-1/run:run-1/stage:stage-1/task:task-1/attempt:2" {
		t.Fatalf("branch metadata = %+v", meta)
	}
	if !cleaned.Load() {
		t.Fatal("factory cleanup was not called")
	}
}

func TestTaskExecutorReturnsSanitizedRetryableError(t *testing.T) {
	rootErr := &provider.APIError{
		Provider: "fake-provider",
		Status:   503,
		Body:     "upstream echoed secret-token and private prompt",
	}
	prov := &taskProvider{name: "fake-provider", err: rootErr}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)

	attempt, err := exec.ExecuteTask(context.Background(), taskInput())
	if attempt == nil || err == nil {
		t.Fatalf("ExecuteTask = (%+v, %v), want failed Attempt and error", attempt, err)
	}
	var taskErr *TaskRunError
	if !errors.As(err, &taskErr) || !errors.Is(err, rootErr) {
		t.Fatalf("error does not preserve typed context/cause: %T %v", err, err)
	}
	if !taskErr.Retryable || taskErr.Provider != "fake-provider" || taskErr.Model != "fake/model-v1" || taskErr.TaskID != "task-1" || taskErr.Attempt != 2 {
		t.Fatalf("TaskRunError = %+v", taskErr)
	}
	if attempt.State != work.RunFailed || attempt.Error != taskErr.Error() {
		t.Fatalf("failed Attempt = %+v", attempt)
	}
	for _, text := range []string{err.Error(), attempt.Error} {
		for _, secret := range []string{"secret-token", "private prompt", rootErr.Body} {
			if strings.Contains(text, secret) {
				t.Fatalf("sanitized error leaked %q: %q", secret, text)
			}
		}
		for _, contextValue := range []string{"fake-provider", "fake/model-v1", "task-1", "attempt=2", "retryable=true"} {
			if !strings.Contains(text, contextValue) {
				t.Fatalf("error %q missing context %q", text, contextValue)
			}
		}
	}
}

func TestTaskExecutorClassifiesNonRetryableProviderError(t *testing.T) {
	rootErr := &provider.APIError{Provider: "fake-provider", Status: 400, Body: "invalid request"}
	prov := &taskProvider{name: "fake-provider", err: rootErr}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)
	_, err := exec.ExecuteTask(context.Background(), taskInput())
	var taskErr *TaskRunError
	if !errors.As(err, &taskErr) || taskErr.Retryable {
		t.Fatalf("TaskRunError = %+v, want non-retryable", taskErr)
	}
}

func TestTaskExecutorFactoryFailureCleansUpAndKeepsCause(t *testing.T) {
	rootErr := errors.New("temporary Session factory failure with secret-token")
	var cleaned atomic.Bool
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		func(context.Context, work.TaskExecuteInput) (*Controller, func(), error) {
			return nil, func() { cleaned.Store(true) }, rootErr
		},
	)
	_, err := exec.ExecuteTask(context.Background(), taskInput())
	var taskErr *TaskRunError
	if !errors.As(err, &taskErr) || !errors.Is(err, rootErr) || !taskErr.Retryable || taskErr.Operation != "create_session" {
		t.Fatalf("factory error = %#v", err)
	}
	if strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("factory error leaked cause: %v", err)
	}
	if !cleaned.Load() {
		t.Fatal("factory cleanup was not called after partial failure")
	}
}

func TestTaskExecutorCancelIsRealAndIdempotent(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", started: make(chan struct{})}
	paths := make(chan string, 1)
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", paths, nil),
	)
	type result struct {
		attempt *work.Attempt
		err     error
	}
	done := make(chan result, 1)
	go func() {
		attempt, err := exec.ExecuteTask(context.Background(), taskInput())
		done <- result{attempt: attempt, err: err}
	}()

	path := <-paths
	<-prov.started
	ref := work.SessionRef{SessionPath: path, BranchID: agent.BranchID(path)}
	if err := exec.CancelTask(context.Background(), ref, "cancel-1"); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := exec.CancelTask(context.Background(), ref, "cancel-1"); err != nil {
		t.Fatalf("repeated CancelTask: %v", err)
	}
	select {
	case got := <-done:
		if got.attempt == nil || got.attempt.State != work.RunCancelled || !errors.Is(got.err, context.Canceled) {
			t.Fatalf("cancelled ExecuteTask = (%+v, %v)", got.attempt, got.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteTask did not stop after CancelTask")
	}
	if err := exec.CancelTask(context.Background(), ref, "cancel-2"); !errors.Is(err, ErrTaskSessionNotRunning) {
		t.Fatalf("cancel completed Session error = %v", err)
	}
	other := work.SessionRef{SessionPath: agent.NewSessionPath(t.TempDir(), "other")}
	if err := exec.CancelTask(context.Background(), other, "cancel-1"); !errors.Is(err, ErrTaskCancelConflict) {
		t.Fatalf("request conflict error = %v", err)
	}
}

func TestTaskExecutorValidationDoesNotEchoPrompt(t *testing.T) {
	input := taskInput()
	input.TaskID = ""
	exec := NewTaskExecutorAdapter(TaskExecutorProfile{}, nil)
	_, err := exec.ExecuteTask(context.Background(), input)
	var taskErr *TaskRunError
	if !errors.As(err, &taskErr) || taskErr.Operation != "validate" || taskErr.Retryable {
		t.Fatalf("validation error = %#v", err)
	}
	if strings.Contains(err.Error(), input.Prompt) || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("validation error leaked prompt: %v", err)
	}
}

func TestTaskExecutorNilCancelIsExplicit(t *testing.T) {
	var exec *TaskExecutorAdapter
	err := exec.CancelTask(context.Background(), work.SessionRef{SessionPath: "session.jsonl"}, "cancel-1")
	if err == nil || !strings.Contains(err.Error(), "Task executor") {
		t.Fatalf("nil CancelTask error = %v", err)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  一二三四\nprivate", 4); got != "一二三四" {
		t.Fatalf("firstLine exact = %q", got)
	}
	if got := firstLine("一二三四五", 4); got != "一二三…" {
		t.Fatalf("firstLine truncated = %q", got)
	}
}
