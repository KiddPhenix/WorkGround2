package control

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/artifact"
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
	mu      sync.Mutex
	reqs    []provider.Request
}

type taskExecutorWork struct {
	WorkService
	executor work.TaskExecutor
}

type taskCornerstoneWork struct {
	WorkService
	view *work.WorkView
	err  error
}

type taskBlobStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newTaskBlobStore() *taskBlobStore {
	return &taskBlobStore{data: make(map[string][]byte)}
}

func (s *taskBlobStore) Put(_ string, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	digest := work.ContentDigest(data)
	s.data[digest] = append([]byte(nil), data...)
	return digest, nil
}

func (s *taskBlobStore) Get(_ string, digest string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.data[digest]...), nil
}

func (s *taskBlobStore) Exists(_ string, digest string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[digest]
	return ok, nil
}

func (s *taskBlobStore) Delete(_ string, digest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, digest)
	return nil
}

func (s *taskBlobStore) ListDigests(_ string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]string, 0, len(s.data))
	for digest := range s.data {
		result = append(result, digest)
	}
	return result, nil
}

func (w *taskCornerstoneWork) Get(context.Context, string) (*work.WorkView, error) {
	return w.view, w.err
}

func (w *taskExecutorWork) SetTaskExecutor(executor work.TaskExecutor) {
	w.executor = executor
}

func (p *taskProvider) Name() string { return p.name }

func (p *taskProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	p.mu.Unlock()
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

func (p *taskProvider) lastRequest() provider.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reqs) == 0 {
		return provider.Request{}
	}
	return p.reqs[len(p.reqs)-1]
}

func taskInput() work.TaskExecuteInput {
	return work.TaskExecuteInput{
		WorkID:           "work-1",
		RunID:            "run-1",
		StageID:          "stage-1",
		TaskID:           "task-1",
		AttemptID:        "attempt-2",
		AttemptIndex:     2,
		RequestID:        "request-1",
		DefinitionDigest: "sha256:definition",
		Prompt:           "private prompt with secret-token",
	}
}

func taskCancel(ref work.SessionRef, requestID string) work.TaskCancelInput {
	input := taskInput()
	return work.TaskCancelInput{
		WorkID: input.WorkID, RunID: input.RunID, StageID: input.StageID,
		TaskID: input.TaskID, AttemptID: input.AttemptID, Session: ref, RequestID: requestID,
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
	if meta.Model != "fake/model-v1" || meta.SessionSource != "work:work-1/run:run-1/stage:stage-1/task:task-1/attempt:2/request:request-1" {
		t.Fatalf("branch metadata = %+v", meta)
	}
	if !cleaned.Load() {
		t.Fatal("factory cleanup was not called")
	}
}

func TestTaskExecutorMaterializesFinalResponseAsArtifact(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "完整的武侠小说正文"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		taskFactory(t, prov, "fake-model", nil, nil),
	)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work: &work.Work{
			ID:            "work-1",
			SchemaVersion: work.SchemaVersionV2,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "txt_file", WorkID: "work-1", DefinitionRev: 2,
				Title: "TXT 文件", Kind: "document", ExpectedCount: 1, Required: true,
				State: work.SlotReserved, Revision: 1,
			}},
		},
		ArtifactSlots: []work.ArtifactSlot{{
			ID: "txt_file", WorkID: "work-1", DefinitionRev: 2,
			Title: "TXT 文件", Kind: "document", ExpectedCount: 1, Required: true,
			State: work.SlotReserved, Revision: 1,
		}},
	}})
	input := taskInput()
	input.ProducesSlotIDs = []string{"txt_file"}
	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := exec.TaskArtifacts(context.Background(), input, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].SlotID != "txt_file" || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "TXT 文件.txt" || ref.Type != "text/plain" || ref.BlobDigest == "" {
		t.Fatalf("artifact ref = %+v", ref)
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil || string(body) != "完整的武侠小说正文" {
		t.Fatalf("blob = %q, err=%v", body, err)
	}
	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	if _, ok := exec.taskArtifacts[key]; ok {
		t.Fatal("materialized artifact text was not released")
	}
}

func TestTaskExecutorMaterializesMarkdownTableAsXLSX(t *testing.T) {
	prov := &taskProvider{
		name: "fake-provider",
		text: "| 项目 | 金额 |\n|---|---:|\n| 场地 | 1500 |\n| 餐饮 | 3000 |",
	}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		taskFactory(t, prov, "fake-model", nil, nil),
	)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "budget", WorkID: "work-1", DefinitionRev: 2,
		Title: "预算表.md", Kind: "xlsx", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work:          &work.Work{ID: "work-1", SchemaVersion: work.SchemaVersionV2, V2ArtifactSlots: []work.ArtifactSlot{slot}},
		ArtifactSlots: []work.ArtifactSlot{slot},
	}})
	input := taskInput()
	input.ProducesSlotIDs = []string{"budget"}
	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := exec.TaskArtifacts(context.Background(), input, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "预算表.xlsx" || ref.Type != xlsxMediaType {
		t.Fatalf("artifact ref = %+v", ref)
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	workbook, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("open xlsx: %v", err)
	}
	files := make(map[string]*zip.File, len(workbook.File))
	for _, file := range workbook.File {
		files[file.Name] = file
	}
	for _, name := range []string{"[Content_Types].xml", "xl/workbook.xml", "xl/worksheets/sheet1.xml"} {
		if files[name] == nil {
			t.Fatalf("xlsx missing %s; files=%v", name, files)
		}
	}
	sheet, err := files["xl/worksheets/sheet1.xml"].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer sheet.Close()
	var sheetBody bytes.Buffer
	if _, err := sheetBody.ReadFrom(sheet); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"项目", "金额", "场地", "1500"} {
		if !strings.Contains(sheetBody.String(), want) {
			t.Fatalf("sheet missing %q: %s", want, sheetBody.String())
		}
	}
}

func TestV2SchedulerPassesCompleteContextToTaskExecutorAdapter(t *testing.T) {
	store, err := work.NewFileWorkStore(t.TempDir(), 0)
	if err != nil {
		t.Fatal(err)
	}
	workID, runID := "work-v2-adapter", "run-v2-adapter"
	now := time.Date(2026, 7, 24, 3, 0, 0, 0, time.UTC)
	if err := store.CreateWorkDir(work.CreateWorkDirInput{
		RequestID: workID + "/create",
		Work: &work.Work{
			SchemaVersion:     work.SchemaVersionV2,
			ID:                workID,
			Name:              "V2 adapter",
			State:             work.WorkDraft,
			ArchiveState:      work.ArchiveActive,
			BlueprintRef:      work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			V2CurrentRevision: 1,
			V2LatestRevision:  1,
			V2RevisionStates:  map[int64]work.DefinitionStatus{1: work.DefActive},
			CreatedAt:         now,
			UpdatedAt:         now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	authority, err := work.NewFileV2RuntimeAuthority(store, workID)
	if err != nil {
		t.Fatal(err)
	}
	prov := &taskProvider{name: "fake-provider", text: "done"}
	baseFactory := taskFactory(t, prov, "fake/model-v1", nil, nil)
	captured := make(chan work.TaskExecuteInput, 1)
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
			captured <- input
			return baseFactory(ctx, input)
		},
	)
	node := work.NodeDef{
		ID:          "compile-report",
		Title:       "Compile report",
		Description: "Generate the reviewed final report.",
	}
	if _, err := work.NewV2Scheduler(exec).Schedule(
		context.Background(),
		workID,
		runID,
		[]work.NodeDef{node},
		nil,
		1,
		nil,
		nil,
		nil,
		authority,
	); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-captured:
		if input.StageID != "v2-dag" {
			t.Fatalf("V2 scheduler StageID = %q", input.StageID)
		}
		if input.Prompt != "Compile report\n\nGenerate the reviewed final report." {
			t.Fatalf("V2 scheduler Prompt = %q", input.Prompt)
		}
		if input.WorkID != workID || input.RunID != runID ||
			input.TaskID == "" || input.AttemptID == "" || input.RequestID == "" {
			t.Fatalf("V2 scheduler adapter context incomplete: %+v", input)
		}
	default:
		t.Fatal("TaskExecutorAdapter factory did not receive V2 scheduler input")
	}
}

func TestTaskExecutorCornerstoneContextKeepsSystemPromptGolden(t *testing.T) {
	const systemPromptGolden = "stable system prompt"
	prov := &taskProvider{name: "fake-provider", text: "ok"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{Work: &work.Work{
		ID: "work-1",
		Cornerstones: []work.Cornerstone{{
			ID:       "cs-1",
			WorkID:   "work-1",
			Type:     work.CornerstoneInstruction,
			Title:    "Pinned rule",
			Content:  "keep these exact instructions",
			Mode:     work.CornerstoneSnapshot,
			Status:   work.CornerstoneActive,
			PinnedAt: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC),
		}},
	}}})

	if _, err := exec.ExecuteTask(context.Background(), taskInput()); err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	req := prov.lastRequest()
	if len(req.Messages) < 2 {
		t.Fatalf("provider messages = %d, want system and user", len(req.Messages))
	}
	if got := req.Messages[0]; got.Role != provider.RoleSystem || got.Content != systemPromptGolden {
		t.Fatalf("system prompt bytes changed: role=%q content=%q", got.Role, got.Content)
	}
	user := req.Messages[len(req.Messages)-1].Content
	for _, want := range []string{"<cornerstone-context>", `id="cs-1"`, "keep these exact instructions", taskInput().Prompt} {
		if !strings.Contains(user, want) {
			t.Fatalf("composed user turn missing %q: %q", want, user)
		}
	}
}

func TestTaskExecutorCornerstoneFailureDoesNotRunTurn(t *testing.T) {
	tests := []struct {
		name string
		work *taskCornerstoneWork
	}{
		{name: "get failure", work: &taskCornerstoneWork{err: errors.New("store unavailable")}},
		{name: "required missing", work: &taskCornerstoneWork{view: &work.WorkView{Work: &work.Work{
			ID: "work-1",
			Cornerstones: []work.Cornerstone{{
				ID: "cs-required", WorkID: "work-1", Type: work.CornerstonePolicy,
				Required: true, Status: work.CornerstoneMissing,
			}},
		}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prov := &taskProvider{name: "fake-provider", text: "must not run"}
			exec := NewTaskExecutorAdapter(
				TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
				taskFactory(t, prov, "fake/model-v1", nil, nil),
			)
			exec.SetWorkService(tt.work)
			attempt, err := exec.ExecuteTask(context.Background(), taskInput())
			if err == nil || attempt == nil || attempt.State != work.RunFailed {
				t.Fatalf("ExecuteTask = (%+v, %v), want failed before RunTurn", attempt, err)
			}
			if got := prov.calls.Load(); got != 0 {
				t.Fatalf("provider called %d times after cornerstone failure", got)
			}
		})
	}
}

func TestCornerstoneTurnRejectsOverlapAndTokenizesCleanup(t *testing.T) {
	c := New(Options{})
	first, err := c.beginCornerstoneTurn("work-a", "<cornerstone-context/>\n", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.beginCornerstoneTurn("work-b", "<cornerstone-context/>\n", 2); err == nil {
		t.Fatal("overlapping Work context should fail closed")
	}
	c.finishCornerstoneTurn(first + 1)
	if _, err := c.beginCornerstoneTurn("work-b", "<cornerstone-context/>\n", 2); err == nil {
		t.Fatal("late cleanup token cleared the active Work context")
	}
	c.finishCornerstoneTurn(first)
	second, err := c.beginCornerstoneTurn("work-b", "<cornerstone-context/>\n", 2)
	if err != nil {
		t.Fatalf("new Work context after cleanup: %v", err)
	}
	c.finishCornerstoneTurn(second)
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
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-1")); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-1")); err != nil {
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
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-2")); !errors.Is(err, ErrTaskSessionNotRunning) {
		t.Fatalf("cancel completed Session error = %v", err)
	}
	other := work.SessionRef{SessionPath: agent.NewSessionPath(t.TempDir(), "other")}
	otherInput := taskCancel(other, "cancel-1")
	otherInput.AttemptID = "other-attempt"
	if err := exec.CancelTask(context.Background(), otherInput); !errors.Is(err, ErrTaskCancelConflict) {
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
	err := exec.CancelTask(context.Background(), taskCancel(work.SessionRef{SessionPath: "session.jsonl"}, "cancel-1"))
	if err == nil || !strings.Contains(err.Error(), "Task executor") {
		t.Fatalf("nil CancelTask error = %v", err)
	}
}

func TestControllerBindsTaskExecutorToWork(t *testing.T) {
	target := &taskExecutorWork{}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		nil,
	)
	ctrl := New(Options{Work: target, TaskExecutor: exec})
	defer ctrl.Close()
	if target.executor != exec || ctrl.TaskExecutor() != exec {
		t.Fatalf("TaskExecutor binding = (%T, %T), want %T", target.executor, ctrl.TaskExecutor(), exec)
	}
}

func TestControllerNormalizesTypedNilTaskExecutor(t *testing.T) {
	target := &taskExecutorWork{}
	var exec *TaskExecutorAdapter
	ctrl := New(Options{Work: target, TaskExecutor: exec})
	defer ctrl.Close()
	if target.executor != nil || ctrl.TaskExecutor() != nil {
		t.Fatalf("typed-nil TaskExecutor binding = (%T, %T), want nil", target.executor, ctrl.TaskExecutor())
	}
}

func TestTaskExecutorRequestIDInProvenance(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "ok"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)
	input := taskInput()
	input.RequestID = "req-provenance-42"
	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	meta, ok, metaErr := agent.LoadBranchMeta(attempt.SessionRef.SessionPath)
	if metaErr != nil || !ok {
		t.Fatalf("LoadBranchMeta = (%+v, %v, %v)", meta, ok, metaErr)
	}
	if !strings.Contains(meta.SessionSource, "/request:req-provenance-42") {
		t.Fatalf("SessionSource %q missing RequestID", meta.SessionSource)
	}
}

func TestTaskExecutorCancelRaceWithCompletion(t *testing.T) {
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

	// Cancel while running — must succeed.
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-race-1")); err != nil {
		t.Fatalf("CancelTask during run: %v", err)
	}
	// Wait for ExecuteTask to finish (cancelled).
	select {
	case got := <-done:
		if got.attempt == nil || got.attempt.State != work.RunCancelled {
			t.Fatalf("expected RunCancelled after cancel: %+v", got.attempt)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ExecuteTask did not finish after CancelTask")
	}

	// Cancel after completion: session no longer active, same requestID is idempotent.
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-race-1")); err != nil {
		t.Fatalf("idempotent CancelTask after completion: %v", err)
	}
	// New requestID after completion: must report session not running.
	if err := exec.CancelTask(context.Background(), taskCancel(ref, "cancel-race-2")); !errors.Is(err, ErrTaskSessionNotRunning) {
		t.Fatalf("new CancelTask after completion error = %v", err)
	}
}

func TestTaskExecutorFactoryReturnsControllerAndError(t *testing.T) {
	rootErr := errors.New("partial factory failure with secret-detail")
	var cleaned atomic.Bool
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
			dir := t.TempDir()
			path := agent.NewSessionPath(dir, "work-partial")
			session := agent.NewSession("stable system prompt")
			prov := &taskProvider{name: "fake-provider", text: "ok"}
			agent := agent.New(prov, tool.NewRegistry(), session, agent.Options{}, event.Discard)
			ctrl := New(Options{
				Runner:       agent,
				Executor:     agent,
				ModelRef:     "fake/model-v1",
				SessionDir:   dir,
				SessionPath:  path,
				SystemPrompt: "stable system prompt",
			})
			return ctrl, func() { cleaned.Store(true) }, rootErr
		},
	)
	_, err := exec.ExecuteTask(context.Background(), taskInput())
	var taskErr *TaskRunError
	if !errors.As(err, &taskErr) || !errors.Is(err, rootErr) || taskErr.Operation != "create_session" {
		t.Fatalf("partial factory error = %#v", err)
	}
	if strings.Contains(err.Error(), "secret-detail") {
		t.Fatalf("partial factory error leaked cause: %v", err)
	}
	if !cleaned.Load() {
		t.Fatal("cleanup was not called after factory returned error")
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

func TestTaskExecutorPendingCancelBeforeSession(t *testing.T) {
	var factoryCalls atomic.Int32
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake/model-v1"},
		func(context.Context, work.TaskExecuteInput) (*Controller, func(), error) {
			factoryCalls.Add(1)
			return nil, nil, errors.New("factory must not run after pending cancel")
		},
	)
	input := taskInput()
	cancel := taskCancel(work.SessionRef{}, "cancel-before-session")
	if err := exec.CancelTask(context.Background(), cancel); err != nil {
		t.Fatalf("CancelTask before ExecuteTask: %v", err)
	}
	attempt, err := exec.ExecuteTask(context.Background(), input)
	if attempt == nil || attempt.State != work.RunCancelled || !errors.Is(err, context.Canceled) {
		t.Fatalf("pending-cancel ExecuteTask = (%+v, %v)", attempt, err)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("factory ran %d times after pending cancel", got)
	}
	if err := exec.CancelTask(context.Background(), cancel); err != nil {
		t.Fatalf("repeated pending cancel: %v", err)
	}
	newRequest := cancel
	newRequest.RequestID = "cancel-after-finished"
	if err := exec.CancelTask(context.Background(), newRequest); !errors.Is(err, ErrTaskSessionNotRunning) {
		t.Fatalf("new cancel after finished error = %v", err)
	}
}

// ── Image artifact regression tests ────────────────────────────────────────

func TestTaskExecutorMaterializesImageSlotFromDiscovered(t *testing.T) {
	blobs := newTaskBlobStore()
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		nil,
	)
	exec.SetArtifactStore(blobs)
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work: &work.Work{
			ID:            "work-img",
			SchemaVersion: work.SchemaVersionV2,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "cover_image", WorkID: "work-img", DefinitionRev: 2,
				Title: "封面图", Kind: "image", ExpectedCount: 1, Required: true,
				State: work.SlotReserved, Revision: 1,
			}},
		},
		ArtifactSlots: []work.ArtifactSlot{{
			ID: "cover_image", WorkID: "work-img", DefinitionRev: 2,
			Title: "封面图", Kind: "image", ExpectedCount: 1, Required: true,
			State: work.SlotReserved, Revision: 1,
		}},
	}})

	input := taskInput()
	input.WorkID = "work-img"
	input.ProducesSlotIDs = []string{"cover_image"}

	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	imageData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A} // PNG signature
	exec.taskArtifacts[key] = taskArtifactData{
		text: "封面已生成",
		artifacts: []artifact.Discovered{{
			Name:        "generated_cover.png",
			Type:        "image/png",
			Path:        "/fake/path/generated_cover.png",
			Data:        imageData,
			SourceRunID: "tc-img-1",
		}},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].SlotID != "cover_image" || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "generated_cover.png" {
		t.Errorf("Name = %q, want generated_cover.png", ref.Name)
	}
	if ref.Type != "image/png" {
		t.Errorf("Type = %q, want image/png", ref.Type)
	}
	if ref.Status != work.ArtifactRefStatusAvailable {
		t.Errorf("Status = %q, want available", ref.Status)
	}
	if ref.BlobDigest == "" {
		t.Fatal("BlobDigest is empty")
	}

	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, imageData) {
		t.Fatalf("blob = %x, want %x", body, imageData)
	}
	if outputs[0].Summary != "封面已生成" {
		t.Errorf("Summary = %q, want 封面已生成", outputs[0].Summary)
	}

	// Verify cleanup.
	if _, ok := exec.taskArtifacts[key]; ok {
		t.Fatal("task artifact data was not released")
	}
}

func TestTaskExecutorImageSlotRejectsMissingArtifact(t *testing.T) {
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		nil,
	)
	exec.SetArtifactStore(newTaskBlobStore())
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work: &work.Work{
			ID:            "work-img2",
			SchemaVersion: work.SchemaVersionV2,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "missing_image", WorkID: "work-img2", DefinitionRev: 2,
				Title: "Missing", Kind: "image", ExpectedCount: 1, Required: true,
				State: work.SlotReserved, Revision: 1,
			}},
		},
		ArtifactSlots: []work.ArtifactSlot{{
			ID: "missing_image", WorkID: "work-img2", DefinitionRev: 2,
			Title: "Missing", Kind: "image", ExpectedCount: 1, Required: true,
			State: work.SlotReserved, Revision: 1,
		}},
	}})

	input := taskInput()
	input.WorkID = "work-img2"
	input.ProducesSlotIDs = []string{"missing_image"}

	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	// Only text, no image artifact.
	exec.taskArtifacts[key] = taskArtifactData{
		text: "no image produced",
	}

	_, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error: no matching artifact for image slot")
	}
	if !strings.Contains(err.Error(), `requires 1 "image" artifact`) {
		t.Errorf("error = %v, want explicit image artifact count", err)
	}
}

// ── Generic artifact test: matching by kind, not by tool name ──────────────

func TestTaskExecutorMaterializesSlotByKindNotByToolName(t *testing.T) {
	blobs := newTaskBlobStore()
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		nil,
	)
	exec.SetArtifactStore(blobs)
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work: &work.Work{
			ID:            "work-gen",
			SchemaVersion: work.SchemaVersionV2,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "arbitrary_image", WorkID: "work-gen", DefinitionRev: 2,
				Title: "任意图片", Kind: "image", ExpectedCount: 1, Required: true,
				State: work.SlotReserved, Revision: 1,
			}},
		},
		ArtifactSlots: []work.ArtifactSlot{{
			ID: "arbitrary_image", WorkID: "work-gen", DefinitionRev: 2,
			Title: "任意图片", Kind: "image", ExpectedCount: 1, Required: true,
			State: work.SlotReserved, Revision: 1,
		}},
	}})

	input := taskInput()
	input.WorkID = "work-gen"
	input.ProducesSlotIDs = []string{"arbitrary_image"}

	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	// Use a Discovered artifact whose SourceRunID is NOT request_help —
	// proving the match is by SlotKind, not by tool name.
	genericImageData := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 'G', 'E', 'N', 'E', 'R', 'I', 'C'}
	exec.taskArtifacts[key] = taskArtifactData{
		text: "",
		artifacts: []artifact.Discovered{{
			Name:        "generic_output.png",
			Type:        "image/png",
			Path:        "/some/other/tool/output.png",
			Data:        genericImageData,
			SourceRunID: "tc-custom-tool-99", // not request_help
		}},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].SlotID != "arbitrary_image" {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "generic_output.png" || ref.Type != "image/png" {
		t.Errorf("ref = %+v", ref)
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, genericImageData) {
		t.Fatalf("blob mismatch")
	}
	// The SourceRunID should be "tc-custom-tool-99", proving no dependency on
	// request_help or image_generation string.
	if ref.SourceRunID != input.RunID {
		t.Errorf("SourceRunID = %q, want %q (run-level, not tool-level)", ref.SourceRunID, input.RunID)
	}

	// Verify cleanup.
	if _, ok := exec.taskArtifacts[key]; ok {
		t.Fatal("task artifact data was not released")
	}
}

func TestTaskExecutorSlotTextKindsStillWork(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "| ColA | ColB |\n|---|---:|\n| a | 1 |"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		taskFactory(t, prov, "fake-model", nil, nil),
	)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "table", WorkID: "work-table", DefinitionRev: 2,
		Title: "table", Kind: "xlsx", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work:          &work.Work{ID: "work-table", SchemaVersion: work.SchemaVersionV2, V2ArtifactSlots: []work.ArtifactSlot{slot}},
		ArtifactSlots: []work.ArtifactSlot{slot},
	}})
	input := taskInput()
	input.WorkID = "work-table"
	input.ProducesSlotIDs = []string{"table"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := exec.TaskArtifacts(context.Background(), input, attempt)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || outputs[0].Refs[0].Type != xlsxMediaType {
		t.Fatalf("text→xlsx regression: outputs = %+v", outputs)
	}
}

func TestTaskExecutorMaterializesExpectedArtifactCount(t *testing.T) {
	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "gallery", WorkID: "work-gallery", DefinitionRev: 1,
		Title: "Gallery", Kind: "image", ExpectedCount: 2, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work:          &work.Work{ID: slot.WorkID, SchemaVersion: work.SchemaVersionV2, V2ArtifactSlots: []work.ArtifactSlot{slot}},
		ArtifactSlots: []work.ArtifactSlot{slot},
	}})
	input := taskInput()
	input.WorkID = slot.WorkID
	input.ProducesSlotIDs = []string{slot.ID}
	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	exec.taskArtifacts[key] = taskArtifactData{artifacts: []artifact.Discovered{
		{Name: "one.png", Type: "image/png", Data: []byte("one")},
		{Name: "two.png", Type: "image/png", Data: []byte("two")},
	}}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || len(outputs[0].Refs) != 2 {
		t.Fatalf("outputs = %+v", outputs)
	}
	if outputs[0].Refs[0].ID == outputs[0].Refs[1].ID ||
		outputs[0].Refs[0].Name != "one.png" ||
		outputs[0].Refs[1].Name != "two.png" {
		t.Fatalf("refs were not assigned deterministically: %+v", outputs[0].Refs)
	}
}

func TestTaskExecutorMaterializesWorkspaceFileArtifact(t *testing.T) {
	root := t.TempDir()
	relative := filepath.Join("dist", "bundle.zip")
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("zip payload")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "bundle", WorkID: "work-file", DefinitionRev: 1,
		Title: "Bundle", Kind: "archive", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work:          &work.Work{ID: slot.WorkID, SchemaVersion: work.SchemaVersionV2, V2ArtifactSlots: []work.ArtifactSlot{slot}},
		ArtifactSlots: []work.ArtifactSlot{slot},
	}})
	input := taskInput()
	input.WorkID = slot.WorkID
	input.ProducesSlotIDs = []string{slot.ID}
	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	exec.taskArtifacts[key] = taskArtifactData{
		workspaceRoot: root,
		artifacts:     []artifact.Discovered{{Name: "bundle.zip", Path: relative, Kind: "archive"}},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := blobs.Get(input.WorkID, outputs[0].Refs[0].BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("blob = %q, want %q", body, want)
	}
}

func TestMaterializeTaskArtifactRejectsInvalidWorkspaceImage(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "fake.png")
	if err := os.WriteFile(path, []byte("not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	slot := work.ArtifactSlot{ID: "image", Title: "Image", Kind: "image"}
	discovered := artifact.Discovered{Name: "fake.png", Path: path}
	if _, _, _, _, err := materializeTaskArtifact(slot, "", &discovered, root); err == nil ||
		!strings.Contains(err.Error(), "validate image artifact") {
		t.Fatalf("error = %v, want invalid workspace image rejection", err)
	}
}
