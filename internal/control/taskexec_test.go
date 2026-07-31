package control

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

func TestTaskArtifactReformatMarkdownToDOCXDoesNotStartModelTurn(t *testing.T) {
	blobs := newTaskBlobStore()
	sourceBody := []byte("# 路线指引\n\n从公司集合后乘车前往场地。")
	sourceDigest, err := blobs.Put("work-reformat", sourceBody)
	if err != nil {
		t.Fatal(err)
	}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "unused", Model: "unused"},
		func(context.Context, work.TaskExecuteInput) (*Controller, func(), error) {
			t.Fatal("reformat must not create a model session")
			return nil, nil, nil
		},
	)
	exec.SetArtifactStore(blobs)
	refs, err := exec.ReformatTaskArtifacts(context.Background(), work.ArtifactReformatInput{
		WorkID:    "work-reformat",
		RequestID: "patch/reformat/route",
		SourceRefs: []work.ArtifactRef{{
			ID: "route-md", Name: "路线指引.md", Type: "text/markdown",
			Status: work.ArtifactRefStatusAvailable, BlobDigest: sourceDigest,
		}},
		Target: work.ArtifactSlot{
			ID: "route", Title: "路线指引.docx", Kind: "docx",
			ExpectedCount: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refs) != 1 || refs[0].Name != "路线指引.docx" || refs[0].Type != docxMediaType {
		t.Fatalf("refs=%+v", refs)
	}
	body, err := blobs.Get("work-reformat", refs[0].BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("reformatted artifact is not a DOCX zip: %v", err)
	}
	foundDocument := false
	for _, file := range reader.File {
		if file.Name == "word/document.xml" {
			foundDocument = true
			break
		}
	}
	if !foundDocument {
		t.Fatalf("DOCX is missing word/document.xml: %+v", reader.File)
	}
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

func TestTaskExecutorPublishesHiddenSessionBeforeTurn(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "done"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)
	input := taskInput()
	var refs []work.SessionRef
	input.Live = func(update work.TaskLiveUpdate) error {
		if update.SessionRef == nil {
			return nil
		}
		refs = append(refs, *update.SessionRef)
		meta, ok, err := agent.LoadBranchMeta(update.SessionRef.SessionPath)
		if err != nil || !ok || !strings.HasPrefix(meta.SessionSource, "work:work-1/") {
			t.Fatalf("hidden Session source was not durable before publish: (%+v, %v, %v)", meta, ok, err)
		}
		return nil
	}

	if _, err := exec.ExecuteTask(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 2 {
		t.Fatalf("SessionRef updates = %d, want created + final", len(refs))
	}
	if refs[0].SessionPath == "" || refs[0].TurnCount != 0 || refs[1].TurnCount != 1 ||
		refs[1].Preview != "done" {
		t.Fatalf("SessionRef lifecycle = %+v", refs)
	}
}

func TestPublishTaskSessionIsEarlyAndIdempotent(t *testing.T) {
	startedAt := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	path := agent.NewSessionPath(t.TempDir(), "work-task")
	input := taskInput()
	input.StartedAt = startedAt
	var refs []work.SessionRef
	input.Live = func(update work.TaskLiveUpdate) error {
		if update.SessionRef != nil {
			refs = append(refs, *update.SessionRef)
		}
		return nil
	}

	first, err := PublishTaskSession(input, path, "fake/model-v1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PublishTaskSession(input, path, "fake/model-v1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(refs) != 2 || refs[0] != refs[1] {
		t.Fatalf("idempotent Session announcement = (%+v, %+v, %+v)", first, second, refs)
	}
	if first.StartedAt != startedAt || first.SessionPath != path || first.BranchID != agent.BranchID(path) {
		t.Fatalf("SessionRef = %+v", first)
	}
	meta, ok, err := agent.LoadBranchMeta(path)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta = (%+v, %v, %v)", meta, ok, err)
	}
	if !strings.HasPrefix(meta.SessionSource, "work:work-1/") {
		t.Fatalf("Session source = %q", meta.SessionSource)
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

func TestTaskExecutorDocumentUsesTextBeforeDocxCandidate(t *testing.T) {
	root := t.TempDir()
	docxPath := filepath.Join(root, "路线指引.docx")
	docxBody := []byte("structured docx bytes")
	if err := os.WriteFile(docxPath, docxBody, 0o644); err != nil {
		t.Fatal(err)
	}

	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	plan := work.ArtifactSlot{
		ID: "plan_doc", WorkID: "work-text-priority", DefinitionRev: 1,
		Title: "团建方案", Kind: "document", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	route := work.ArtifactSlot{
		ID: "route_docx", WorkID: plan.WorkID, DefinitionRev: 1,
		Title: "路线指引.docx", Kind: "docx", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work: &work.Work{
			ID: plan.WorkID, SchemaVersion: work.SchemaVersionV2,
			V2ArtifactSlots: []work.ArtifactSlot{plan, route},
		},
		ArtifactSlots: []work.ArtifactSlot{plan, route},
	}})
	input := taskInput()
	input.WorkID = plan.WorkID
	input.ProducesSlotIDs = []string{plan.ID, route.ID}
	key := taskAttemptKey(input.WorkID, input.RunID, input.StageID, input.TaskID, input.AttemptID)
	exec.taskArtifacts[key] = taskArtifactData{
		workspaceRoot: root,
		text:          "完整团建方案正文",
		artifacts: []artifact.Discovered{
			{Name: "路线指引.docx", Path: docxPath, Kind: "docx"},
		},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 2 || len(outputs[0].Refs) != 1 || len(outputs[1].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	planBody, err := blobs.Get(input.WorkID, outputs[0].Refs[0].BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if string(planBody) != "完整团建方案正文" {
		t.Fatalf("plan body = %q", planBody)
	}
	routeBody, err := blobs.Get(input.WorkID, outputs[1].Refs[0].BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(routeBody, docxBody) || outputs[1].Refs[0].Name != "路线指引.docx" {
		t.Fatalf("route ref = %+v, body = %q", outputs[1].Refs[0], routeBody)
	}
}

func TestTaskExecutorMaterializesCodeFinalResponseAsArtifact(t *testing.T) {
	const code = "```python\nprint(\"Hello, World!\")\n```"
	prov := &taskProvider{name: "fake-provider", text: code}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: "fake-provider", Model: "fake-model"},
		taskFactory(t, prov, "fake-model", nil, nil),
	)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "hello-world-code", WorkID: "work-code", DefinitionRev: 1,
		Title: "Hello World Code", Kind: "code", ExpectedCount: 1, Required: true,
		State: work.SlotReserved, Revision: 1,
	}
	exec.SetWorkService(&taskCornerstoneWork{view: &work.WorkView{
		Work:          &work.Work{ID: slot.WorkID, SchemaVersion: work.SchemaVersionV2, V2ArtifactSlots: []work.ArtifactSlot{slot}},
		ArtifactSlots: []work.ArtifactSlot{slot},
	}})
	input := taskInput()
	input.WorkID = slot.WorkID
	input.ProducesSlotIDs = []string{slot.ID}

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
	if ref.Name != "Hello World Code.txt" || ref.Type != "text/plain" {
		t.Fatalf("artifact ref = %+v", ref)
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != code {
		t.Fatalf("blob = %q, want %q", body, code)
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

func TestTaskArtifactRelativePathKeepsWorkspaceArtifactUsable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "outputs", "report.pdf")
	got := taskArtifactRelativePath(&artifact.Discovered{Path: path}, root)
	if got != "outputs/report.pdf" {
		t.Fatalf("relative artifact path = %q", got)
	}
	if got := taskArtifactRelativePath(&artifact.Discovered{Path: filepath.Join("outputs", "report.pdf")}, root); got != "outputs/report.pdf" {
		t.Fatalf("workspace-relative artifact path = %q", got)
	}
	outside := filepath.Join(filepath.Dir(root), "outside.pdf")
	if got := taskArtifactRelativePath(&artifact.Discovered{Path: outside}, root); got != "" {
		t.Fatalf("outside artifact path leaked as %q", got)
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

func TestTaskExecutorArtifactCandidateFallback_FirstMissingSecondValid(t *testing.T) {
	// The first matching docx candidate path does not exist on disk, the
	// second does. TaskArtifacts must skip the first and materialize the
	// second instead of failing outright.
	root := t.TempDir()
	validPath := filepath.Join(root, "路线指引.docx")
	if err := os.MkdirAll(filepath.Dir(validPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("valid docx content")
	if err := os.WriteFile(validPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "report", WorkID: "work-fallback", DefinitionRev: 1,
		Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true,
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
		artifacts: []artifact.Discovered{
			{Name: "missing.docx", Path: filepath.Join(root, "missing.docx"), Kind: "docx"},
			{Name: "路线指引.docx", Path: validPath, Kind: "docx"},
		},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "路线指引.docx" {
		t.Errorf("Name = %q, want 路线指引.docx", ref.Name)
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("blob = %q, want %q", body, want)
	}
}

func TestTaskExecutorArtifactCandidateFallback_AllCandidatesFailDiagnosticError(t *testing.T) {
	// When all matching candidates fail to materialize, the error must
	// contain the slot ID, expected count, tried count, and error summary.
	root := t.TempDir()
	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "report", WorkID: "work-allfail", DefinitionRev: 1,
		Title: "Report", Kind: "docx", ExpectedCount: 1, Required: true,
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
		artifacts: []artifact.Discovered{
			{Name: "missing1.docx", Path: filepath.Join(root, "missing1.docx"), Kind: "docx"},
			{Name: "missing2.docx", Path: filepath.Join(root, "missing2.docx"), Kind: "docx"},
		},
	}

	_, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, `"report"`) {
		t.Errorf("error missing slot ID: %s", errStr)
	}
	if !strings.Contains(errStr, "requires 1") {
		t.Errorf("error missing required count: %s", errStr)
	}
	if !strings.Contains(errStr, "tried 2 candidate") {
		t.Errorf("error missing tried count: %s", errStr)
	}
	if !strings.Contains(errStr, "missing1.docx") || !strings.Contains(errStr, "missing2.docx") {
		t.Errorf("error missing candidate names: %s", errStr)
	}
}

func TestTaskExecutorArtifactCandidateFallback_ExpectedCountTwoNotPartialSuccess(t *testing.T) {
	// When ExpectedCount=2 and only 1 candidate succeeds, the slot must
	// not produce a partial success — it must fail with a diagnostic error.
	root := t.TempDir()
	validPath := filepath.Join(root, "valid.docx")
	if err := os.WriteFile(validPath, []byte("valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "reports", WorkID: "work-partial", DefinitionRev: 1,
		Title: "Reports", Kind: "docx", ExpectedCount: 2, Required: true,
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
		artifacts: []artifact.Discovered{
			{Name: "valid.docx", Path: validPath, Kind: "docx"},
			{Name: "missing.docx", Path: filepath.Join(root, "missing.docx"), Kind: "docx"},
		},
	}

	_, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err == nil {
		t.Fatal("expected error for partial success, got nil")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "requires 2") {
		t.Errorf("error missing required count: %s", errStr)
	}
	if !strings.Contains(errStr, "tried 2 candidate") {
		t.Errorf("error missing tried count: %s", errStr)
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

func TestTaskExecutorMaterializesExternalFileArtifact(t *testing.T) {
	root := t.TempDir()
	// External file in a different temp dir.
	externalDir := t.TempDir()
	externalPath := filepath.Join(externalDir, "output", "result.bin")
	if err := os.MkdirAll(filepath.Dir(externalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("external file content")
	if err := os.WriteFile(externalPath, want, 0o644); err != nil {
		t.Fatal(err)
	}

	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "external_file", WorkID: "work-ext", DefinitionRev: 1,
		Title: "External File", Kind: "file", ExpectedCount: 1, Required: true,
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
		artifacts:     []artifact.Discovered{{Name: "result.bin", Path: externalPath, Kind: "file"}},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "result.bin" {
		t.Errorf("Name = %q, want result.bin", ref.Name)
	}
	if ref.Status != work.ArtifactRefStatusAvailable {
		t.Errorf("Status = %q, want available", ref.Status)
	}
	if ref.RelativePath != "" {
		t.Errorf("RelativePath = %q, want empty for external artifact", ref.RelativePath)
	}
	if ref.BlobDigest == "" {
		t.Fatal("BlobDigest is empty")
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("blob = %q, want %q", body, want)
	}
}

func TestTaskExecutorMaterializesExternalFileArtifactViaDotDot(t *testing.T) {
	root := t.TempDir()
	// External file outside workspace, referenced via .. relative path.
	externalDir := t.TempDir()
	externalPath := filepath.Join(externalDir, "outside", "result.bin")
	if err := os.MkdirAll(filepath.Dir(externalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("external dotdot content")
	if err := os.WriteFile(externalPath, want, 0o644); err != nil {
		t.Fatal(err)
	}
	// Compute a relative path with .. from workspace root.
	relPath, err := filepath.Rel(root, externalPath)
	if err != nil {
		t.Fatal(err)
	}

	exec := NewTaskExecutorAdapter(TaskExecutorProfile{Provider: "fake", Model: "fake"}, nil)
	blobs := newTaskBlobStore()
	exec.SetArtifactStore(blobs)
	slot := work.ArtifactSlot{
		ID: "ext_dotdot", WorkID: "work-ext-dotdot", DefinitionRev: 1,
		Title: "External via DotDot", Kind: "file", ExpectedCount: 1, Required: true,
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
		artifacts:     []artifact.Discovered{{Name: "result.bin", Path: relPath, Kind: "file"}},
	}

	outputs, err := exec.TaskArtifacts(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != 1 || len(outputs[0].Refs) != 1 {
		t.Fatalf("outputs = %+v", outputs)
	}
	ref := outputs[0].Refs[0]
	if ref.Name != "result.bin" {
		t.Errorf("Name = %q, want result.bin", ref.Name)
	}
	if ref.Status != work.ArtifactRefStatusAvailable {
		t.Errorf("Status = %q, want available", ref.Status)
	}
	if ref.RelativePath != "" {
		t.Errorf("RelativePath = %q, want empty for external artifact via ..", ref.RelativePath)
	}
	if ref.BlobDigest == "" {
		t.Fatal("BlobDigest is empty")
	}
	body, err := blobs.Get(input.WorkID, ref.BlobDigest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, want) {
		t.Fatalf("blob = %q, want %q", body, want)
	}
}

// ── Preflight tests ────────────────────────────────────────────────────────

// fakeRequestHelpTool is a deterministic request_help stub for preflight tests.
type fakeRequestHelpTool struct {
	calls         []fakeRequestHelpCall
	err           error
	artifactPath  string
	parentSession string
	completed     atomic.Bool
	mu            sync.Mutex
}

type fakeRequestHelpCall struct {
	Capability string
	Prompt     string
}

func (t *fakeRequestHelpTool) Name() string        { return "request_help" }
func (t *fakeRequestHelpTool) Description() string { return "fake request_help for testing" }
func (t *fakeRequestHelpTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"capability":{"type":"string"},"prompt":{"type":"string"}},"required":["capability","prompt"]}`)
}
func (t *fakeRequestHelpTool) ReadOnly() bool { return false }

func (t *fakeRequestHelpTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Capability string `json:"capability"`
		Prompt     string `json:"prompt"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	t.mu.Lock()
	t.calls = append(t.calls, fakeRequestHelpCall{Capability: p.Capability, Prompt: p.Prompt})
	t.parentSession = agent.ParentSession(ctx)
	err := t.err
	t.mu.Unlock()
	t.completed.Store(true)
	if err != nil {
		return "", err
	}
	// Return a structured result that ImageProducer can parse.
	artifactJSON, err := json.Marshal(map[string]any{
		"task_id": "fake", "path": t.artifactPath, "mime": "image/png",
		"size": 1, "width": 1, "height": 1,
	})
	if err != nil {
		return "", err
	}
	return "Capability assist succeeded\nrequest_id: fake\ncapability: image_generation\nfrom_model: fake\nmodel: fake\nattempt: 1/1\nartifact: " + string(artifactJSON) + "\n\nFake image generated", nil
}

// eventRecorder captures events in order for assertion.
type eventRecorder struct {
	mu     sync.Mutex
	events []event.Event
}

func (r *eventRecorder) Emit(e event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// providerWithGuard tracks whether Stream was called and provides a guard
// channel to block/fail if called before preflight completes.
type providerWithGuard struct {
	name                    string
	streamedAt              chan struct{} // closed when Stream is called
	blockCh                 chan struct{} // blocks Stream until closed
	text                    string        // response text to return
	preflightDone           *atomic.Bool
	streamedBeforePreflight atomic.Bool
}

func (p *providerWithGuard) Name() string { return p.name }

func (p *providerWithGuard) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	if p.preflightDone != nil && !p.preflightDone.Load() {
		p.streamedBeforePreflight.Store(true)
	}
	close(p.streamedAt)
	if p.blockCh != nil {
		select {
		case <-p.blockCh:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	chunks := make(chan provider.Chunk, 2)
	chunks <- provider.Chunk{Type: provider.ChunkText, Text: p.text}
	chunks <- provider.Chunk{Type: provider.ChunkDone}
	close(chunks)
	return chunks, nil
}

func TestPreflightExecutesBeforeProviderStream(t *testing.T) {
	prov := &providerWithGuard{name: "fake-provider", streamedAt: make(chan struct{}), blockCh: make(chan struct{}), text: "result"}
	codexHome := t.TempDir()
	generatedDir := filepath.Join(codexHome, "generated_images")
	if err := os.MkdirAll(generatedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	artifactPath := filepath.Join(generatedDir, "output.png")
	if err := os.WriteFile(artifactPath, mustBase64(t, tinyPNG), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := artifact.ValidateImageFile(artifactPath); err != nil {
		t.Fatalf("validate test image: %v", err)
	}
	fakeTool := &fakeRequestHelpTool{artifactPath: artifactPath}
	prov.preflightDone = &fakeTool.completed
	recorder := &eventRecorder{}

	dir := t.TempDir()
	var created *Controller
	factory := func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
		path := agent.NewSessionPath(dir, "work-task")
		reg := tool.NewRegistry()
		reg.Add(fakeTool)
		session := agent.NewSession("stable system prompt")
		executor := agent.New(prov, reg, session, agent.Options{}, recorder)
		ctrl := New(Options{
			Runner:       executor,
			Executor:     executor,
			ModelRef:     "fake/model",
			SessionDir:   dir,
			SessionPath:  path,
			SystemPrompt: "stable system prompt",
		})
		created = ctrl
		return ctrl, func() {}, nil
	}

	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model"},
		factory,
	)

	input := taskInput()
	input.SlotPreflights = []work.SlotPreflight{
		{SlotID: "img", SlotIndex: 0, Capability: "image_generation", Prompt: "generate a hero image"},
	}

	// Run in a goroutine — the provider blocks, so we check state after.
	errCh := make(chan error, 1)
	go func() {
		_, err := exec.ExecuteTask(context.Background(), input)
		errCh <- err
	}()

	// Wait for the provider to be called (preflight must have finished by then).
	select {
	case <-prov.streamedAt:
		// Good: preflight completed, provider called next.
	case <-time.After(5 * time.Second):
		t.Fatal("provider Stream was never called")
	}

	// Unblock the provider so the turn can finish.
	close(prov.blockCh)
	if err := <-errCh; err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if prov.streamedBeforePreflight.Load() {
		t.Fatal("provider Stream started before request_help preflight completed")
	}

	// Verify preflight events: ToolDispatch then ToolResult for request_help.
	recorder.mu.Lock()
	events := recorder.events
	recorder.mu.Unlock()

	var dispatchSeen, resultSeen bool
	for _, e := range events {
		switch e.Kind {
		case event.ToolDispatch:
			if e.Tool.Name == "request_help" {
				dispatchSeen = true
			}
		case event.ToolResult:
			if e.Tool.Name == "request_help" && dispatchSeen {
				resultSeen = true
			}
		}
	}
	if !dispatchSeen || !resultSeen {
		t.Fatalf("expected ToolDispatch+ToolResult for request_help; dispatch=%v result=%v, events=%d", dispatchSeen, resultSeen, len(events))
	}

	// Verify the fake tool was called with correct args.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 1 {
		t.Fatalf("expected 1 request_help call, got %d", len(fakeTool.calls))
	}
	call := fakeTool.calls[0]
	if call.Capability != "image_generation" || call.Prompt != "generate a hero image" {
		t.Fatalf("request_help call = %+v", call)
	}
	if fakeTool.parentSession == "" {
		t.Fatal("request_help preflight missing parent Session context")
	}
	fakeTool.mu.Unlock()

	history := created.History()
	var toolResultIndex, taskPromptIndex = -1, -1
	for i, msg := range history {
		if msg.Role == provider.RoleTool && msg.Name == "request_help" {
			toolResultIndex = i
		}
		if msg.Role == provider.RoleUser && strings.Contains(msg.Content, "Host capability preflight terminal states:") {
			taskPromptIndex = i
		}
	}
	if toolResultIndex < 0 || taskPromptIndex <= toolResultIndex {
		t.Fatalf("history order toolResult=%d taskPrompt=%d", toolResultIndex, taskPromptIndex)
	}
	if got := artifact.Collect(history, artifact.DefaultProducers()); len(got) != 1 {
		t.Fatalf("preflight artifacts in shared history = %d, want 1; history=%+v", len(got), history)
	}
}

func TestPreflightFailureStillAllowsProviderRun(t *testing.T) {
	prov := &providerWithGuard{name: "fake-provider", streamedAt: make(chan struct{}), blockCh: make(chan struct{}), text: "result"}
	fakeTool := &fakeRequestHelpTool{err: errors.New("no usable provider")}
	prov.preflightDone = &fakeTool.completed
	recorder := &eventRecorder{}

	dir := t.TempDir()
	var created *Controller
	factory := func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
		path := agent.NewSessionPath(dir, "work-task")
		reg := tool.NewRegistry()
		reg.Add(fakeTool)
		session := agent.NewSession("stable system prompt")
		executor := agent.New(prov, reg, session, agent.Options{}, recorder)
		ctrl := New(Options{
			Runner:       executor,
			Executor:     executor,
			ModelRef:     "fake/model",
			SessionDir:   dir,
			SessionPath:  path,
			SystemPrompt: "stable system prompt",
		})
		created = ctrl
		return ctrl, func() {}, nil
	}

	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model"},
		factory,
	)

	input := taskInput()
	input.SlotPreflights = []work.SlotPreflight{
		{SlotID: "img", SlotIndex: 0, Capability: "image_generation", Prompt: "generate a hero image"},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := exec.ExecuteTask(context.Background(), input)
		errCh <- err
	}()

	// Provider must be called even if preflight fails.
	select {
	case <-prov.streamedAt:
		// Provider was called — preflight completed (success or failure) first.
	case <-time.After(5 * time.Second):
		t.Fatal("provider Stream was never called after preflight")
	}

	close(prov.blockCh)
	if err := <-errCh; err != nil {
		t.Fatalf("ExecuteTask should not fail on preflight error: %v", err)
	}
	if prov.streamedBeforePreflight.Load() {
		t.Fatal("provider Stream started before failed preflight reached a terminal result")
	}

	history := created.History()
	var failedResult, fallbackUnlocked bool
	for _, msg := range history {
		if msg.Role == provider.RoleTool && msg.Name == "request_help" &&
			strings.Contains(msg.Content, "no usable provider") {
			failedResult = true
		}
		if msg.Role == provider.RoleUser && strings.Contains(msg.Content, "fallback is now unlocked") {
			fallbackUnlocked = true
		}
	}
	if !failedResult || !fallbackUnlocked {
		t.Fatalf("failed preflight not visible before fallback: result=%v unlocked=%v", failedResult, fallbackUnlocked)
	}
}

func TestNoPreflightPreservesOldPath(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	var cleaned atomic.Bool
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, &cleaned),
	)

	input := taskInput()
	// No SlotPreflights — should work exactly as before.
	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("expected RunCompleted, got %s", attempt.State)
	}
}

func TestTaskExecutorRunsMandatoryQualityPass(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "complete delivery"}
	exec := NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: "fake/model-v1"},
		taskFactory(t, prov, "fake/model-v1", nil, nil),
	)
	input := taskInput()
	input.AcceptanceCriteria = []string{"include the full report", "preserve source URLs"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.SessionRef.TurnCount != 2 || prov.calls.Load() != 2 {
		t.Fatalf("quality pass turns=%d provider_calls=%d, want 2", attempt.SessionRef.TurnCount, prov.calls.Load())
	}
	request := prov.lastRequest()
	if len(request.Messages) == 0 {
		t.Fatal("quality pass request is empty")
	}
	last := request.Messages[len(request.Messages)-1]
	if last.Role != provider.RoleUser ||
		!strings.Contains(last.Content, "complete replacement delivery") ||
		!strings.Contains(last.Content, "preserve source URLs") {
		t.Fatalf("quality pass prompt = %+v", last)
	}
}

func TestTaskSuccessfulCapabilitiesRequirePairedSuccessfulToolResult(t *testing.T) {
	messages := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "search-ok", Name: "request_help", Arguments: `{"capability":"web_search"}`},
			{ID: "image-failed", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
			{ID: "orphan", Name: "web_search", Arguments: `{}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "search-ok", Name: "request_help", Content: "Capability assist succeeded"},
		{Role: provider.RoleTool, ToolCallID: "image-failed", Name: "request_help", Content: "error: no provider"},
	}
	got := taskSuccessfulCapabilities(messages, nil)
	if !reflect.DeepEqual(got, []string{"web_search"}) {
		t.Fatalf("successful capabilities = %v, want [web_search]", got)
	}
}

func TestTaskSuccessfulCapabilitiesAcceptNativeSearchOnlyWithCitations(t *testing.T) {
	withURL := []provider.Message{{
		Role: provider.RoleAssistant, Content: "Current result: https://example.com/source",
	}}
	if got := taskSuccessfulCapabilities(withURL, []string{"web_search"}); !reflect.DeepEqual(got, []string{"web_search"}) {
		t.Fatalf("native search evidence = %v", got)
	}
	withoutURL := []provider.Message{{
		Role: provider.RoleAssistant, Content: "I searched and found a result.",
	}}
	if got := taskSuccessfulCapabilities(withoutURL, []string{"web_search"}); len(got) != 0 {
		t.Fatalf("uncited native search should not pass: %v", got)
	}
}

func TestValidateTaskInputRejectsInvalidPreflights(t *testing.T) {
	valid := work.SlotPreflight{
		SlotID: "image", SlotIndex: 0, Capability: "image_generation", Prompt: "draw",
	}
	tests := []struct {
		name       string
		preflights []work.SlotPreflight
		want       string
	}{
		{name: "missing slot", preflights: []work.SlotPreflight{{SlotIndex: 0, Capability: "image_generation", Prompt: "draw"}}, want: "slotId is required"},
		{name: "negative index", preflights: []work.SlotPreflight{{SlotID: "image", SlotIndex: -1, Capability: "image_generation", Prompt: "draw"}}, want: "slotIndex must be non-negative"},
		{name: "missing capability", preflights: []work.SlotPreflight{{SlotID: "image", SlotIndex: 0, Prompt: "draw"}}, want: "capability is required"},
		{name: "duplicate", preflights: []work.SlotPreflight{valid, valid}, want: "duplicates slot"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := taskInput()
			input.SlotPreflights = tt.preflights
			if err := validateTaskInput(input); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// ── Capability preflight tests ───────────────────────────────────────────

// taskExecutorWithRequestHelp builds a TaskExecutorAdapter whose Controller
// registry includes the given fake request_help tool.
func taskExecutorWithRequestHelp(t *testing.T, prov provider.Provider, modelRef string, fakeTool *fakeRequestHelpTool, nativeCaps []string) *TaskExecutorAdapter {
	t.Helper()
	dir := t.TempDir()
	factory := func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
		path := agent.NewSessionPath(dir, "work-task")
		reg := tool.NewRegistry()
		reg.Add(fakeTool)
		session := agent.NewSession("stable system prompt")
		executor := agent.New(prov, reg, session, agent.Options{}, event.Discard)
		ctrl := New(Options{
			Runner:       executor,
			Executor:     executor,
			Registry:     reg,
			ModelRef:     modelRef,
			SessionDir:   dir,
			SessionPath:  path,
			SystemPrompt: "stable system prompt",
		})
		return ctrl, func() {}, nil
	}
	return NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: modelRef, NativeCapabilities: nativeCaps},
		factory,
	)
}

func TestCapabilityPreflightWebSearchSuccess(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	fakeTool := &fakeRequestHelpTool{}
	exec := taskExecutorWithRequestHelp(t, prov, "fake/model", fakeTool, nil)

	input := taskInput()
	input.RequiredCapabilities = []string{"web_search"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("state = %s, want RunCompleted", attempt.State)
	}

	// Preflight was called once with web_search.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 1 || fakeTool.calls[0].Capability != "web_search" {
		t.Fatalf("request_help calls = %+v, want 1 web_search call", fakeTool.calls)
	}
	fakeTool.mu.Unlock()

	// web_search appears in SuccessfulCapabilities.
	var found bool
	for _, c := range attempt.SuccessfulCapabilities {
		if c == "web_search" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SuccessfulCapabilities = %v, want web_search", attempt.SuccessfulCapabilities)
	}
}

func TestCapabilityPreflightWebSearchFailure(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	fakeTool := &fakeRequestHelpTool{err: errors.New("no usable web_search provider")}
	exec := taskExecutorWithRequestHelp(t, prov, "fake/model", fakeTool, nil)

	input := taskInput()
	input.RequiredCapabilities = []string{"web_search"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask should not fail on preflight error: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("state = %s, want RunCompleted (preflight failure is non-blocking)", attempt.State)
	}

	// Preflight was called once.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 1 {
		t.Fatalf("request_help calls = %d, want 1", len(fakeTool.calls))
	}
	fakeTool.mu.Unlock()

	// web_search must NOT appear in SuccessfulCapabilities after failure.
	for _, c := range attempt.SuccessfulCapabilities {
		if c == "web_search" {
			t.Fatalf("SuccessfulCapabilities should not include web_search after preflight failure")
		}
	}
}

func TestCapabilityPreflightNativeSkip(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	fakeTool := &fakeRequestHelpTool{}
	// Model has native web_search capability.
	exec := taskExecutorWithRequestHelp(t, prov, "fake/model", fakeTool, []string{"web_search"})

	input := taskInput()
	input.RequiredCapabilities = []string{"web_search"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("state = %s", attempt.State)
	}

	// No preflight should have been triggered.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 0 {
		t.Fatalf("request_help was called %d times despite native web_search", len(fakeTool.calls))
	}
	fakeTool.mu.Unlock()
}

func TestCapabilityPreflightUnsupportedSkip(t *testing.T) {
	for _, capability := range []string{"image_generation", "future_capability"} {
		t.Run(capability, func(t *testing.T) {
			prov := &taskProvider{name: "fake-provider", text: "result"}
			fakeTool := &fakeRequestHelpTool{}
			exec := taskExecutorWithRequestHelp(t, prov, "fake/model", fakeTool, nil)

			input := taskInput()
			input.RequiredCapabilities = []string{capability}

			attempt, err := exec.ExecuteTask(context.Background(), input)
			if err != nil {
				t.Fatalf("ExecuteTask: %v", err)
			}
			if attempt.State != work.RunCompleted {
				t.Fatalf("state = %s", attempt.State)
			}

			fakeTool.mu.Lock()
			defer fakeTool.mu.Unlock()
			if len(fakeTool.calls) != 0 {
				t.Fatalf("request_help was called %d times for unsupported capability %q", len(fakeTool.calls), capability)
			}
		})
	}
}

// fakeWebSearchTool is a minimal stub that the model could call directly.
type fakeWebSearchTool struct{}

func (t *fakeWebSearchTool) Name() string        { return "web_search" }
func (t *fakeWebSearchTool) Description() string { return "fake web_search" }
func (t *fakeWebSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
}
func (t *fakeWebSearchTool) ReadOnly() bool { return true }
func (t *fakeWebSearchTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "search result: https://example.com", nil
}

// taskExecutorWithWebSearch builds a TaskExecutorAdapter whose Controller
// registry includes both a fake request_help and a fake web_search tool.
func taskExecutorWithWebSearch(t *testing.T, prov provider.Provider, modelRef string, fakeTool *fakeRequestHelpTool) *TaskExecutorAdapter {
	t.Helper()
	dir := t.TempDir()
	factory := func(ctx context.Context, input work.TaskExecuteInput) (*Controller, func(), error) {
		path := agent.NewSessionPath(dir, "work-task")
		reg := tool.NewRegistry()
		reg.Add(fakeTool)
		reg.Add(&fakeWebSearchTool{})
		session := agent.NewSession("stable system prompt")
		executor := agent.New(prov, reg, session, agent.Options{}, event.Discard)
		ctrl := New(Options{
			Runner:       executor,
			Executor:     executor,
			Registry:     reg,
			ModelRef:     modelRef,
			SessionDir:   dir,
			SessionPath:  path,
			SystemPrompt: "stable system prompt",
		})
		return ctrl, func() {}, nil
	}
	return NewTaskExecutorAdapter(
		TaskExecutorProfile{Provider: prov.Name(), Model: modelRef},
		factory,
	)
}

func TestCapabilityPreflightDirectToolSkip(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	fakeTool := &fakeRequestHelpTool{}
	exec := taskExecutorWithWebSearch(t, prov, "fake/model", fakeTool)

	input := taskInput()
	input.RequiredCapabilities = []string{"web_search"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("state = %s", attempt.State)
	}

	// No preflight because a direct web_search tool exists.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 0 {
		t.Fatalf("request_help was called %d times despite direct web_search tool", len(fakeTool.calls))
	}
	fakeTool.mu.Unlock()
}

func TestCapabilityPreflightDedup(t *testing.T) {
	prov := &taskProvider{name: "fake-provider", text: "result"}
	fakeTool := &fakeRequestHelpTool{}
	exec := taskExecutorWithRequestHelp(t, prov, "fake/model", fakeTool, nil)

	input := taskInput()
	// Duplicate RequiredCapabilities.
	input.RequiredCapabilities = []string{"web_search", "web_search", "web_search"}

	attempt, err := exec.ExecuteTask(context.Background(), input)
	if err != nil {
		t.Fatalf("ExecuteTask: %v", err)
	}
	if attempt.State != work.RunCompleted {
		t.Fatalf("state = %s", attempt.State)
	}

	// Only one preflight despite three duplicates.
	fakeTool.mu.Lock()
	if len(fakeTool.calls) != 1 {
		t.Fatalf("request_help calls = %d, want 1 (duplicates deduped)", len(fakeTool.calls))
	}
	fakeTool.mu.Unlock()
}

func TestWebFetchNotWebSearchEvidence(t *testing.T) {
	// A model that calls web_fetch and outputs a URL must NOT be counted as
	// having web_search capability.
	messages := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "fetch-1", Name: "web_fetch", Arguments: `{"url":"https://example.com"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "fetch-1", Name: "web_fetch", Content: "page content"},
		{Role: provider.RoleAssistant, Content: "I fetched https://example.com and found the answer."},
	}
	caps := taskSuccessfulCapabilities(messages, nil)
	for _, c := range caps {
		if c == "web_search" {
			t.Fatalf("web_fetch + URL should not produce web_search capability; got %v", caps)
		}
	}
}
