package work

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestViewFromStateNormalizesRequiredCollections(t *testing.T) {
	value := &Work{
		SchemaVersion: SchemaVersion,
		ID:            "work-wails-empty-collections",
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
	}
	view := viewFromState(value, WorkEventState{Revision: 2})
	if view.Work.Definition.Workflow.Stages == nil || view.Work.Definition.BlockSpecs == nil ||
		view.Work.Blocks == nil || view.Work.Placements == nil || view.Work.Cornerstones == nil || view.Work.Runs == nil {
		t.Fatalf("required WorkView collections must be arrays: stages=%v blockSpecs=%v blocks=%v placements=%v cornerstones=%v runs=%v",
			view.Work.Definition.Workflow.Stages, view.Work.Definition.BlockSpecs,
			view.Work.Blocks, view.Work.Placements, view.Work.Cornerstones, view.Work.Runs)
	}
	if value.Definition.Workflow.Stages != nil || value.Definition.BlockSpecs != nil ||
		value.Blocks != nil || value.Placements != nil || value.Cornerstones != nil || value.Runs != nil {
		t.Fatal("view normalization mutated the persisted Work projection")
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"workflow":{"stages":[]}`, `"blockSpecs":[]`, `"blocks":[]`, `"placements":[]`, `"cornerstones":[]`, `"runs":[]`} {
		if !bytes.Contains(raw, []byte(field)) {
			t.Fatalf("WorkView JSON %s missing %s", raw, field)
		}
	}
}

func TestViewFromStateNormalizesNestedDefinitionCollections(t *testing.T) {
	value := &Work{
		SchemaVersion: SchemaVersion,
		ID:            "work-wails-nested-definition",
		State:         WorkDraft,
		ArchiveState:  ArchiveActive,
		Definition: WorkDefinitionSnapshot{
			Workflow: WorkflowDef{Stages: []StageSpec{
				{ID: "stage-empty", Title: "Empty"},
				{ID: "stage-with-task", Title: "With task", Tasks: []TaskSpec{{ID: "task-1", Title: "Task"}}},
			}},
		},
	}

	view := viewFromState(value, WorkEventState{Revision: 3})
	stages := view.Work.Definition.Workflow.Stages
	if len(stages) != 2 || stages[0].Tasks == nil || len(stages[1].Tasks) != 1 {
		t.Fatalf("nested DefinitionSnapshot collections not normalized: %+v", stages)
	}
	if value.Definition.Workflow.Stages[0].Tasks != nil {
		t.Fatal("view normalization mutated persisted StageSpec tasks")
	}

	stages[1].Tasks[0].Title = "Changed in view"
	if value.Definition.Workflow.Stages[1].Tasks[0].Title != "Task" {
		t.Fatal("view normalization did not copy nested StageSpec tasks")
	}
}

type serviceSink struct {
	mu     sync.Mutex
	events []WorkViewEvent
	next   chan WorkViewEvent
}

func (s *serviceSink) EmitWorkView(event WorkViewEvent) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
	if s.next != nil {
		select {
		case s.next <- event:
		default:
		}
	}
}

func (s *serviceSink) Events() []WorkViewEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]WorkViewEvent(nil), s.events...)
}

type serviceFixture struct {
	root  string
	store *FileWorkStore
	sink  *serviceSink
	svc   *Service
}

func newServiceFixture(t *testing.T) *serviceFixture {
	t.Helper()
	requireFileStoreIntegration(t)
	root := filepath.Join(t.TempDir(), "works")
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	sink := &serviceSink{next: make(chan WorkViewEvent, 256)}
	return &serviceFixture{
		root:  root,
		store: store,
		sink:  sink,
		svc:   NewService(store, NewBlueprintRegistry(), sink),
	}
}

func (f *serviceFixture) restart(t *testing.T) *Service {
	t.Helper()
	store, err := NewFileWorkStore(f.root, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart NewFileWorkStore: %v", err)
	}
	f.store = store
	f.svc = NewService(store, NewBlueprintRegistry(), f.sink)
	return f.svc
}

func serviceCreateInput(requestID string) CreateWorkInput {
	return CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1},
		Name:         "Service Work",
		Inputs:       map[string]any{"repository": "local", "options": map[string]any{"strict": true}},
		RequestID:    requestID,
	}
}

func mustServiceCreate(t *testing.T, svc *Service, requestID string) *Work {
	t.Helper()
	value, err := svc.Create(context.Background(), serviceCreateInput(requestID))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return value
}

func mustServiceView(t *testing.T, svc *Service, workID string) *WorkView {
	t.Helper()
	view, err := svc.Get(context.Background(), workID)
	if err != nil {
		t.Fatalf("Get(%s): %v", workID, err)
	}
	return view
}

func TestServiceCreateRestartIdempotent(t *testing.T) {
	f := newServiceFixture(t)
	input := serviceCreateInput("service-create-restart")
	first, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if first.ID != workIDForRequest(input.RequestID) {
		t.Fatalf("ID = %q, want stable request-derived ID", first.ID)
	}
	view := mustServiceView(t, f.svc, first.ID)
	if view.Revision != 2 {
		t.Fatalf("create revision = %d, want 2", view.Revision)
	}
	if view.Work.Definition.Digest == "" {
		t.Fatal("definition digest is empty")
	}

	restarted := f.restart(t)
	second, err := restarted.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("idempotent Create after restart: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("duplicate Create IDs differ: %s != %s", second.ID, first.ID)
	}
	if got := mustServiceView(t, restarted, first.ID).Revision; got != 2 {
		t.Fatalf("duplicate Create advanced revision to %d", got)
	}

	changed := input
	changed.Name = "different intent"
	if _, err := restarted.Create(context.Background(), changed); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("reused requestID error = %v, want ErrWorkRequestIDConflict", err)
	}
	page, err := restarted.List(context.Background(), WorkFilter{Limit: 10})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if page.Total != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("List = %+v, want one Work %s", page, first.ID)
	}
}

func TestServiceCreateUsesPromptAndDerivesName(t *testing.T) {
	f := newServiceFixture(t)
	input := serviceCreateInput("service-create-prompt")
	input.Name = ""
	input.Prompt = "  整理生日派对的时间、地点和邀请名单\n并生成执行清单  "

	value, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if value.Name != "整理生日派对的时间、地点和邀请名单" {
		t.Fatalf("Name = %q, want first prompt line", value.Name)
	}
	if value.Prompt != "整理生日派对的时间、地点和邀请名单\n并生成执行清单" {
		t.Fatalf("Prompt = %q, want trimmed user prompt", value.Prompt)
	}
}

func TestServiceUpdateDraftPromptDerivesNameIdempotently(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-update-prompt-create")
	prompt := "  公司团建吃烤肉，KTV 周五晚上，预算不超过3000\n生成可执行安排  "
	input := UpdateDraftInput{
		WorkID:           value.ID,
		Prompt:           &prompt,
		ExpectedRevision: 2,
		RequestID:        "service-update-prompt-once",
	}

	first, err := f.svc.UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	want := "公司团建吃烤肉，KTV 周五晚上，预算不超过3000"
	if first.Revision != 3 || first.Work.Name != want || first.Work.Prompt != prompt {
		t.Fatalf("first prompt update = %+v, want derived name %q", first, want)
	}

	duplicate, err := f.restart(t).UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("duplicate UpdateDraft after restart: %v", err)
	}
	if duplicate.Revision != 3 || duplicate.Work.Name != want || duplicate.Work.Prompt != prompt {
		t.Fatalf("duplicate prompt update = %+v, want stable derived name %q", duplicate, want)
	}
}

func TestServiceUpdateDraftIdempotentAndConflict(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-update-create")
	name := "updated"
	input := UpdateDraftInput{
		WorkID:           value.ID,
		Name:             &name,
		ExpectedRevision: 2,
		RequestID:        "service-update-once",
	}
	first, err := f.svc.UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if first.Revision != 3 || first.Work.Name != name {
		t.Fatalf("first update = %+v", first)
	}

	restarted := f.restart(t)
	duplicate, err := restarted.UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("duplicate UpdateDraft after restart: %v", err)
	}
	if duplicate.Revision != 3 || duplicate.Work.Name != name {
		t.Fatalf("duplicate update = %+v", duplicate)
	}

	different := "different"
	input.Name = &different
	latest, err := restarted.UpdateDraft(context.Background(), input)
	var eventConflict *ErrWorkEventConflict
	if !errors.As(err, &eventConflict) {
		t.Fatalf("request content conflict = %v", err)
	}
	if latest == nil || latest.Revision != 3 || latest.Work.Name != name {
		t.Fatalf("request conflict latest = %+v", latest)
	}

	stale := "stale"
	latest, err = restarted.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           value.ID,
		Name:             &stale,
		ExpectedRevision: 2,
		RequestID:        "service-update-stale",
	})
	if !errors.As(err, &eventConflict) {
		t.Fatalf("revision conflict = %v", err)
	}
	if latest == nil || latest.Revision != 3 || latest.Work.Name != name {
		t.Fatalf("revision conflict latest = %+v", latest)
	}
}

func TestServiceUpdateDraftRetryIgnoresRefreshedRevision(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-update-refresh-create")
	prompt := "生成一份稳定的发布清单"
	input := UpdateDraftInput{
		WorkID: value.ID, Prompt: &prompt, Locale: "zh-CN",
		ExpectedRevision: 2, RequestID: "service-update-refresh",
	}
	first, err := f.svc.UpdateDraft(context.Background(), input)
	if err != nil || first.Revision != 3 {
		t.Fatalf("first UpdateDraft = %+v err=%v", first, err)
	}
	name := "后续修改"
	advanced, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, Name: &name, ExpectedRevision: 3, RequestID: "service-update-advance",
	})
	if err != nil || advanced.Revision != 4 {
		t.Fatalf("advance UpdateDraft = %+v err=%v", advanced, err)
	}

	input.ExpectedRevision = advanced.Revision
	retried, err := f.restart(t).UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("retry with refreshed revision: %v", err)
	}
	if retried.Revision != advanced.Revision || retried.Work.Name != name || retried.Work.Prompt != prompt {
		t.Fatalf("retry changed latest projection: %+v", retried)
	}
	original, err := f.store.LoadRequestEvent(value.ID, input.RequestID+"/draft")
	if err != nil {
		t.Fatalf("LoadRequestEvent: %v", err)
	}
	var payload struct {
		IntentDigest     string `json:"intentDigest"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if err := json.Unmarshal(original.Payload, &payload); err != nil {
		t.Fatalf("decode original payload: %v", err)
	}
	if payload.IntentDigest == "" || payload.ExpectedRevision != 2 {
		t.Fatalf("original payload = %+v", payload)
	}

	changed := prompt + "（修改）"
	input.Prompt = &changed
	latest, err := f.svc.UpdateDraft(context.Background(), input)
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) || conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("changed intent error = %v, want request conflict", err)
	}
	if latest == nil || latest.Revision != advanced.Revision || latest.Work.Prompt != prompt {
		t.Fatalf("changed intent latest = %+v", latest)
	}
}

func TestServiceUpdateDraftRecoversLegacyRequestIntent(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-update-legacy-create")
	prompt := "恢复旧版启动草稿"
	requestID := "service-update-legacy/draft"
	payload, err := json.Marshal(map[string]any{
		"expectedRevision": int64(2),
		"prompt":           prompt,
		"name":             workNameFromPrompt(prompt, value.Name),
		"locale":           "zh-CN",
	})
	if err != nil {
		t.Fatal(err)
	}
	event := newServiceEvent(value.ID, requestID, EventDraftUpdated, payload, time.Now().UTC())
	event.BaseRevision = 2
	event.Revision = 3
	if _, err := f.store.CommitEvent(value.ID, event); err != nil {
		t.Fatalf("commit legacy draft event: %v", err)
	}
	name := "旧事件后的标题"
	advanced, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, Name: &name, ExpectedRevision: 3, RequestID: "service-update-legacy-advance",
	})
	if err != nil || advanced.Revision != 4 {
		t.Fatalf("advance after legacy event = %+v err=%v", advanced, err)
	}

	retried, err := f.restart(t).UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, Prompt: &prompt, Locale: "zh",
		ExpectedRevision: advanced.Revision, RequestID: "service-update-legacy",
	})
	if err != nil {
		t.Fatalf("legacy retry: %v", err)
	}
	if retried.Revision != advanced.Revision || retried.Work.Name != name || retried.Work.Prompt != prompt {
		t.Fatalf("legacy retry changed latest projection: %+v", retried)
	}

	changed := prompt + "（新内容）"
	latest, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, Prompt: &changed, Locale: "zh",
		ExpectedRevision: advanced.Revision, RequestID: "service-update-legacy",
	})
	var conflict *ErrWorkEventConflict
	if !errors.As(err, &conflict) || conflict.Kind != WorkEventRequestConflict {
		t.Fatalf("changed legacy intent error = %v, want request conflict", err)
	}
	if latest == nil || latest.Revision != advanced.Revision || latest.Work.Prompt != prompt {
		t.Fatalf("changed legacy intent latest = %+v", latest)
	}
}

func TestServiceUpdateDraftConcurrentRevision(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-concurrent-create")
	start := make(chan struct{})
	type result struct {
		view *WorkView
		err  error
	}
	results := make(chan result, 2)
	for i, name := range []string{"first", "second"} {
		i, name := i, name
		go func() {
			<-start
			results <- resultFromUpdate(f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
				WorkID:           value.ID,
				Name:             &name,
				ExpectedRevision: 2,
				RequestID:        "service-concurrent-" + string(rune('a'+i)),
			}))
		}()
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		result := <-results
		if result.err == nil {
			successes++
			continue
		}
		var conflict *ErrWorkEventConflict
		if errors.As(result.err, &conflict) && result.view != nil {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent result: view=%+v err=%v", result.view, result.err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
	if got := mustServiceView(t, f.svc, value.ID).Revision; got != 3 {
		t.Fatalf("concurrent updates produced revision %d, want 3", got)
	}
}

func TestServiceUpdateDraftDoesNotStealWriterLease(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-writer-create")
	workDir, err := f.store.workPath(value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := AcquireWorkLease(workDir); err != nil {
		t.Fatalf("AcquireWorkLease: %v", err)
	}
	t.Cleanup(func() { _ = ReleaseWorkLease(workDir) })
	name := "blocked while owned"
	input := UpdateDraftInput{
		WorkID: value.ID, Name: &name, ExpectedRevision: 2, RequestID: "service-writer-update",
	}
	if _, err := f.svc.UpdateDraft(context.Background(), input); !errors.Is(err, ErrWorkLeaseHeld) {
		t.Fatalf("UpdateDraft with active writer = %v, want ErrWorkLeaseHeld", err)
	}
	if err := ReleaseWorkLease(workDir); err != nil {
		t.Fatalf("ReleaseWorkLease: %v", err)
	}
	view, err := f.svc.UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("retry UpdateDraft after writer release: %v", err)
	}
	if view.Revision != 3 || view.Work.Name != name {
		t.Fatalf("retry view = %+v", view)
	}
}

func resultFromUpdate(view *WorkView, err error) struct {
	view *WorkView
	err  error
} {
	return struct {
		view *WorkView
		err  error
	}{view: view, err: err}
}

func TestServiceArchiveRestoreImmutable(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-archive-create")
	record, err := f.svc.Archive(context.Background(), value.ID, "service-archive")
	if err != nil {
		t.Fatalf("Archive: %v", err)
	}
	if record.Snapshot.ArchiveState != ArchiveArchived || record.Snapshot.ArchivedAt == nil {
		t.Fatalf("archive snapshot state = %+v", record.Snapshot)
	}
	if record.Snapshot.State != value.State {
		t.Fatalf("Archive changed Work.State: %s -> %s", value.State, record.Snapshot.State)
	}
	if len(record.FallbackBlocks) != len(record.Snapshot.Blocks) {
		t.Fatalf("fallback count = %d, blocks = %d", len(record.FallbackBlocks), len(record.Snapshot.Blocks))
	}

	restarted := f.restart(t)
	duplicate, err := restarted.Archive(context.Background(), value.ID, "service-archive")
	if err != nil {
		t.Fatalf("duplicate Archive: %v", err)
	}
	if !reflect.DeepEqual(record, duplicate) {
		t.Fatalf("duplicate archive changed record\nfirst=%+v\nsecond=%+v", record, duplicate)
	}
	view, err := restarted.Restore(context.Background(), value.ID, "service-unarchive")
	if err != nil {
		t.Fatalf("Restore archived: %v", err)
	}
	if view.Work.ArchiveState != ArchiveActive || view.Work.State != value.State {
		t.Fatalf("restored state = archive:%s work:%s", view.Work.ArchiveState, view.Work.State)
	}
	loaded, err := f.store.LoadArchive(value.ID)
	if err != nil {
		t.Fatalf("LoadArchive after Restore: %v", err)
	}
	if !reflect.DeepEqual(record, loaded) {
		t.Fatal("Restore mutated immutable WorkRecord")
	}
}

func TestServiceDeleteRestoreRestart(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-trash-create")
	if err := f.svc.Delete(context.Background(), value.ID, "service-trash-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.store.LoadProjection(value.ID); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("active LoadProjection after Delete = %v, want ErrWorkNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(f.store.TrashDir(), value.ID, "cleanup-pending.json")); err != nil {
		t.Fatalf("trash cleanup marker: %v", err)
	}
	if err := f.svc.Delete(context.Background(), value.ID, "service-trash-delete"); err != nil {
		t.Fatalf("duplicate Delete: %v", err)
	}
	page, err := f.svc.List(context.Background(), WorkFilter{Limit: 10})
	if err != nil || page.Total != 0 {
		t.Fatalf("List after Delete = %+v err=%v", page, err)
	}

	restarted := f.restart(t)
	view, err := restarted.Restore(context.Background(), value.ID, "service-trash-restore")
	if err != nil {
		t.Fatalf("Restore from trash: %v", err)
	}
	if view.Revision != 4 || view.Work.ArchiveState != ArchiveActive || view.Work.State != value.State {
		t.Fatalf("restored view = %+v", view)
	}
	if got := mustServiceView(t, f.restart(t), value.ID); got.Revision != 4 || got.Work.ArchiveState != ArchiveActive {
		t.Fatalf("restart after Restore = %+v", got)
	}
	events := f.sink.Events()
	var removed *WorkViewEvent
	for i := range events {
		if events[i].Type == ViewRemoved {
			removed = &events[i]
		}
	}
	if removed == nil || removed.Revision != 3 || removed.BaseRevision != 2 {
		t.Fatalf("removed view event = %+v", removed)
	}
}

func TestServiceDeleteLateRetryAfterRestore(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-delete-late-create")
	const deleteRequest = "service-delete-late-old"
	if err := f.svc.Delete(context.Background(), value.ID, deleteRequest); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := f.restart(t).Restore(context.Background(), value.ID, "service-delete-late-restore"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if err := f.restart(t).Delete(context.Background(), value.ID, deleteRequest); err != nil {
		t.Fatalf("late duplicate Delete: %v", err)
	}
	view := mustServiceView(t, f.svc, value.ID)
	if view.Revision != 4 || view.Work.ArchiveState != ArchiveActive {
		t.Fatalf("late Delete changed restored Work: %+v", view)
	}
	if _, err := os.Stat(filepath.Join(f.store.TrashDir(), value.ID)); !os.IsNotExist(err) {
		t.Fatalf("late Delete recreated trash directory: %v", err)
	}
}

func TestServiceDeleteLateRetryAfterPendingCleanupRestore(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-delete-pending-late-create")
	const deleteRequest = "service-delete-pending-late-old"
	originalRename := renameWorkDir
	renameWorkDir = func(_, _ string) error { return errors.New("injected pending move") }
	t.Cleanup(func() { renameWorkDir = originalRename })
	err := f.svc.Delete(context.Background(), value.ID, deleteRequest)
	var cleanup *ErrWorkCleanupRecovery
	if !errors.As(err, &cleanup) || cleanup.Stage != "move_failed" {
		t.Fatalf("Delete error = %v, want move_failed cleanup", err)
	}
	renameWorkDir = originalRename
	if _, err := f.restart(t).Restore(context.Background(), value.ID, "service-delete-pending-late-restore"); err != nil {
		t.Fatalf("Restore over pending Delete: %v", err)
	}

	if err := f.restart(t).Delete(context.Background(), value.ID, deleteRequest); err != nil {
		t.Fatalf("late pending Delete: %v", err)
	}
	view := mustServiceView(t, f.svc, value.ID)
	if view.Revision != 4 || view.Work.ArchiveState != ArchiveActive {
		t.Fatalf("late pending Delete changed restored Work: %+v", view)
	}
	pending, err := f.store.LoadCleanupPending(value.ID)
	if err != nil || pending == nil || pending.Operation != "trash" || pending.RequestID != deleteRequest+"/move" {
		t.Fatalf("superseded cleanup state = %+v err=%v", pending, err)
	}
}

func TestServiceDeleteRetryResumesCommittedMove(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-delete-committed-move-create")
	const deleteRequest = "service-delete-committed-move"
	originalWrite := writeDerivedFile
	failed := false
	writeDerivedFile = func(path string, data []byte, mode fs.FileMode) error {
		active := filepath.Join(f.root, value.ID)
		trashed := filepath.Join(f.store.TrashDir(), value.ID)
		if !failed && filepath.Clean(path) == filepath.Join(f.root, "index.json") &&
			!f.store.isDirWithData(active) && f.store.isDirWithData(trashed) {
			failed = true
			return errors.New("injected post-move index failure")
		}
		return originalWrite(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = originalWrite })
	err := f.svc.Delete(context.Background(), value.ID, deleteRequest)
	var cleanup *ErrWorkCleanupRecovery
	if !errors.As(err, &cleanup) || !cleanup.Committed {
		t.Fatalf("Delete error = %v, want committed cleanup", err)
	}
	writeDerivedFile = originalWrite

	if err := f.restart(t).Delete(context.Background(), value.ID, deleteRequest); err != nil {
		t.Fatalf("Delete committed-move retry: %v", err)
	}
	if _, err := f.store.LoadProjection(value.ID); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("active projection after retry = %v, want ErrWorkNotFound", err)
	}
	trashDir := filepath.Join(f.store.TrashDir(), value.ID)
	replay, err := ReplayWorkEventLog(trashDir)
	if err != nil || replay.Index == nil || replay.Index.Revision != 3 {
		t.Fatalf("trash replay = %+v err=%v", replay, err)
	}
	pending, err := f.store.LoadCleanupPending(value.ID)
	if err != nil || pending == nil || pending.Stage != "done" {
		t.Fatalf("completed cleanup = %+v err=%v", pending, err)
	}
}

func TestServiceRestoreLateRetryAfterNewDelete(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-restore-late-create")
	if err := f.svc.Delete(context.Background(), value.ID, "service-restore-late-delete-one"); err != nil {
		t.Fatalf("first Delete: %v", err)
	}
	const restoreRequest = "service-restore-late-old"
	if _, err := f.restart(t).Restore(context.Background(), value.ID, restoreRequest); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if err := f.restart(t).Delete(context.Background(), value.ID, "service-restore-late-delete-two"); err != nil {
		t.Fatalf("second Delete: %v", err)
	}

	if _, err := f.restart(t).Restore(context.Background(), value.ID, restoreRequest); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("late Restore = %v, want request conflict", err)
	}
	if _, err := f.store.LoadProjection(value.ID); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("late Restore moved deleted Work active: %v", err)
	}
}

func TestServiceRestoreActiveReservesRequest(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-restore-active-create")
	first, err := f.svc.Restore(context.Background(), value.ID, "service-restore-active")
	if err != nil {
		t.Fatalf("Restore active: %v", err)
	}
	if first.Revision != 3 || first.Work.ArchiveState != ArchiveActive {
		t.Fatalf("first active Restore = %+v", first)
	}
	duplicate, err := f.restart(t).Restore(context.Background(), value.ID, "service-restore-active")
	if err != nil {
		t.Fatalf("duplicate active Restore: %v", err)
	}
	if duplicate.Revision != 3 {
		t.Fatalf("duplicate active Restore revision = %d, want 3", duplicate.Revision)
	}
	name := "edited after restore"
	updated, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, Name: &name, ExpectedRevision: 3, RequestID: "service-restore-active-edit",
	})
	if err != nil || updated.Revision != 4 {
		t.Fatalf("UpdateDraft after Restore = %+v err=%v", updated, err)
	}
	duplicate, err = f.restart(t).Restore(context.Background(), value.ID, "service-restore-active")
	if err != nil || duplicate.Revision != 4 || duplicate.Work.Name != name {
		t.Fatalf("Restore retry after non-lifecycle update = %+v err=%v", duplicate, err)
	}
	if _, err := f.svc.Archive(context.Background(), value.ID, "service-restore-active-archive"); err != nil {
		t.Fatalf("Archive after active Restore: %v", err)
	}
	if _, err := f.svc.Restore(context.Background(), value.ID, "service-restore-active"); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("late retry after Archive = %v, want request conflict", err)
	}
}

func TestServiceCreateCommittedIndexRecovery(t *testing.T) {
	f := newServiceFixture(t)
	original := writeDerivedFile
	failed := false
	writeDerivedFile = func(path string, data []byte, mode fs.FileMode) error {
		if !failed && filepath.Clean(path) == filepath.Join(f.root, "index.json") {
			failed = true
			return errors.New("injected index failure")
		}
		return original(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = original })
	input := serviceCreateInput("service-create-index-failure")
	_, err := f.svc.Create(context.Background(), input)
	var committed *ErrWorkCommittedRecovery
	if !errors.As(err, &committed) || !committed.Committed || !committed.Recoverable {
		t.Fatalf("Create error = %v, want committed recovery", err)
	}
	writeDerivedFile = original

	retried, err := f.restart(t).Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create retry: %v", err)
	}
	if retried.ID != workIDForRequest(input.RequestID) {
		t.Fatalf("retry ID = %s", retried.ID)
	}
	if got := mustServiceView(t, f.svc, retried.ID).Revision; got != 2 {
		t.Fatalf("retry revision = %d, want 2", got)
	}
}

func TestServiceProjectionFailureReplaysDuplicateRequest(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-projection-create")
	original := writeDerivedFile
	projectionWrites := 0
	writeDerivedFile = func(path string, data []byte, mode fs.FileMode) error {
		if filepath.Base(path) == "projection.json" && filepath.Dir(path) == filepath.Join(f.root, value.ID) {
			projectionWrites++
			if projectionWrites == 2 {
				return errors.New("injected projection failure")
			}
		}
		return original(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = original })
	name := "committed update"
	input := UpdateDraftInput{WorkID: value.ID, Name: &name, ExpectedRevision: 2, RequestID: "service-projection-update"}
	_, err := f.svc.UpdateDraft(context.Background(), input)
	var committed *ErrWorkCommittedRecovery
	if !errors.As(err, &committed) || committed.Revision != 3 {
		t.Fatalf("UpdateDraft error = %v, want committed revision 3", err)
	}
	writeDerivedFile = original

	retried, err := f.restart(t).UpdateDraft(context.Background(), input)
	if err != nil {
		t.Fatalf("UpdateDraft retry: %v", err)
	}
	if retried.Revision != 3 || retried.Work.Name != name {
		t.Fatalf("replayed update = %+v", retried)
	}
}

func TestServiceArchiveFileFailureRepairsFromEvent(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-archive-failure-create")
	original := writeDerivedFile
	failed := false
	writeDerivedFile = func(path string, data []byte, mode fs.FileMode) error {
		if !failed && filepath.Base(path) == "archive.json" {
			failed = true
			return errors.New("injected archive failure")
		}
		return original(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = original })
	_, err := f.svc.Archive(context.Background(), value.ID, "service-archive-failure")
	var committed *ErrWorkCommittedRecovery
	if !errors.As(err, &committed) || committed.Revision != 3 {
		t.Fatalf("Archive error = %v, want committed revision 3", err)
	}
	writeDerivedFile = original

	record, err := f.restart(t).Archive(context.Background(), value.ID, "service-archive-failure")
	if err != nil {
		t.Fatalf("Archive repair retry: %v", err)
	}
	if record.Snapshot.ArchiveState != ArchiveArchived {
		t.Fatalf("repaired record state = %s", record.Snapshot.ArchiveState)
	}
	if got := mustServiceView(t, f.svc, value.ID).Revision; got != 3 {
		t.Fatalf("Archive repair appended duplicate event, revision=%d", got)
	}
}

func TestServiceDeleteMoveFailureResumesCleanup(t *testing.T) {
	f := newServiceFixture(t)
	value := mustServiceCreate(t, f.svc, "service-delete-failure-create")
	originalRename := renameWorkDir
	renameWorkDir = func(_, _ string) error { return errors.New("injected move failure") }
	t.Cleanup(func() { renameWorkDir = originalRename })
	err := f.svc.Delete(context.Background(), value.ID, "service-delete-failure")
	var cleanup *ErrWorkCleanupRecovery
	if !errors.As(err, &cleanup) || !cleanup.Recoverable {
		t.Fatalf("Delete error = %v, want cleanup recovery", err)
	}
	renameWorkDir = originalRename
	if err := f.restart(t).Delete(context.Background(), value.ID, "service-delete-failure"); err != nil {
		t.Fatalf("Delete cleanup retry: %v", err)
	}
	if _, err := f.store.LoadProjection(value.ID); !errors.Is(err, ErrWorkNotFound) {
		t.Fatalf("LoadProjection after cleanup retry = %v", err)
	}
	view, err := f.restart(t).Restore(context.Background(), value.ID, "service-delete-failure-restore")
	if err != nil {
		t.Fatalf("Restore after cleanup: %v", err)
	}
	if view.Revision != 4 || view.Work.ArchiveState != ArchiveActive {
		t.Fatalf("restored view = %+v", view)
	}
}

func TestServiceDraftStateRules(t *testing.T) {
	for _, test := range []struct {
		from WorkState
		want WorkState
		ok   bool
	}{
		{WorkDraft, WorkDraft, true},
		{WorkReady, WorkDraft, true},
		{WorkFailed, WorkDraft, true},
		{WorkCancelled, WorkDraft, true},
		{WorkRunning, "", false},
		{WorkWaitingUser, "", false},
		{WorkPaused, "", false},
		{WorkCompleted, "", false},
	} {
		got, err := draftTargetState(test.from)
		if test.ok && (err != nil || got != test.want) {
			t.Errorf("draftTargetState(%s) = %s, %v", test.from, got, err)
		}
		if !test.ok && err == nil {
			t.Errorf("draftTargetState(%s) unexpectedly succeeded", test.from)
		}
	}

	for _, state := range []WorkState{WorkRunning, WorkWaitingUser, WorkPaused, WorkCompleted} {
		got, err := updateDraftTargetState(&Work{SchemaVersion: SchemaVersionV2, State: state})
		if err != nil || got != state {
			t.Errorf("updateDraftTargetState(V2 %s) = %s, %v", state, got, err)
		}
	}
	if _, err := updateDraftTargetState(&Work{SchemaVersion: SchemaVersion, State: WorkCompleted}); err == nil {
		t.Fatal("updateDraftTargetState(V1 completed) unexpectedly succeeded")
	}
}

func TestServiceV2UpdateDraftKeepsCompletedState(t *testing.T) {
	f := newServiceFixture(t)
	view, err := f.svc.BeginWorkPlanning(context.Background(), BeginWorkPlanningInput{
		SessionID: "session-v2-completed-edit",
		RequestID: "service-v2-completed-edit-create",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}

	run := WorkflowRun{
		ID:        "run-v2-completed-edit",
		WorkID:    view.Work.ID,
		RequestID: "service-v2-completed-edit-run",
		State:     RunPending,
	}
	states := []struct {
		eventType WorkEventType
		runState  RunState
		workState WorkState
	}{
		{EventRunStarted, RunPending, WorkRunning},
		{EventRunChanged, RunRunning, WorkRunning},
		{EventRunChanged, RunCompleted, WorkCompleted},
	}
	revision := view.Revision
	for i, state := range states {
		run.State = state.runState
		payload, marshalErr := json.Marshal(runEventPayload{Run: run, WorkState: state.workState})
		if marshalErr != nil {
			t.Fatalf("marshal run state %s: %v", state.runState, marshalErr)
		}
		event := newServiceEvent(
			view.Work.ID,
			fmt.Sprintf("service-v2-completed-edit-state-%d", i),
			state.eventType,
			payload,
			time.Now().UTC(),
		)
		event.BaseRevision = revision
		event.Revision = revision + 1
		if _, commitErr := f.store.CommitEvent(view.Work.ID, event); commitErr != nil {
			t.Fatalf("commit run state %s: %v", state.runState, commitErr)
		}
		revision++
	}

	prompt := "基于已完成结果生成新的工作结构"
	updated, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           view.Work.ID,
		Prompt:           &prompt,
		ExpectedRevision: revision,
		RequestID:        "service-v2-completed-edit-update",
	})
	if err != nil {
		t.Fatalf("UpdateDraft on completed V2 Work: %v", err)
	}
	if updated.Work.State != WorkCompleted || updated.Work.Prompt != prompt {
		t.Fatalf("updated Work = state %s prompt %q", updated.Work.State, updated.Work.Prompt)
	}
	if updated.Revision != revision+1 {
		t.Fatalf("updated revision = %d, want %d", updated.Revision, revision+1)
	}
}

func TestServiceInputAndContextErrors(t *testing.T) {
	f := newServiceFixture(t)
	if _, err := f.svc.Create(context.Background(), CreateWorkInput{}); !errors.Is(err, ErrWorkRequestIDRequired) {
		t.Fatalf("Create missing requestID = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.svc.List(ctx, WorkFilter{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("List canceled context = %v", err)
	}
	if err := (&Service{}).Delete(context.Background(), "work-deadbeefdeadbeefdeadbeef", "request"); err == nil {
		t.Fatal("nil Store Delete unexpectedly succeeded")
	}
	value := mustServiceCreate(t, f.svc, "service-empty-update-create")
	view, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: value.ID, ExpectedRevision: 2, RequestID: "service-empty-update",
	})
	if err == nil || view == nil || view.Revision != 2 {
		t.Fatalf("empty UpdateDraft = view:%+v err:%v, want unchanged view and error", view, err)
	}
}
