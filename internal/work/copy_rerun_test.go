package work

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestServiceListBlueprintsAndCopyWork(t *testing.T) {
	f := newServiceFixture(t)
	blueprints, err := f.svc.ListBlueprints(context.Background())
	if err != nil {
		t.Fatalf("ListBlueprints: %v", err)
	}
	if len(blueprints) != 4 {
		t.Fatalf("blueprints = %d, want 4", len(blueprints))
	}

	source := mustServiceCreate(t, f.svc, "copy-source")
	prompt := "review this repository"
	view, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID: source.ID, Prompt: &prompt, ExpectedRevision: 2, RequestID: "copy-source-prompt",
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	copied, err := f.svc.CopyWork(context.Background(), CopyWorkInput{
		SourceWorkID: source.ID, RequestID: "copy-destination",
	})
	if err != nil {
		t.Fatalf("CopyWork: %v", err)
	}
	if copied.ID == source.ID || copied.CopiedFrom != source.ID || copied.State != WorkDraft {
		t.Fatalf("copied identity/state = %+v", copied)
	}
	if copied.Prompt != prompt || copied.Name != source.Name+" - 副本" {
		t.Fatalf("copied editable data = name %q prompt %q", copied.Name, copied.Prompt)
	}
	if len(copied.Runs) != 0 || copied.ArchiveState != ArchiveActive {
		t.Fatalf("copied runtime was not reset: %+v", copied)
	}

	duplicate, err := f.restart(t).CopyWork(context.Background(), CopyWorkInput{
		SourceWorkID: source.ID, RequestID: "copy-destination",
	})
	if err != nil {
		t.Fatalf("CopyWork retry: %v", err)
	}
	if duplicate.ID != copied.ID {
		t.Fatalf("copy retry ID = %s, want %s", duplicate.ID, copied.ID)
	}
	if got := mustServiceView(t, f.svc, source.ID).Revision; got != view.Revision {
		t.Fatalf("copy mutated source revision: got %d want %d", got, view.Revision)
	}
}

func TestServiceRerunLatestMigratesBlockSchema(t *testing.T) {
	root := t.TempDir()
	store, err := NewFileWorkStore(root, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	registry := NewBlueprintRegistry()
	v1 := &WorkBlueprint{
		SchemaVersion:  SchemaVersion,
		ID:             "blueprint:file-migration",
		Version:        1,
		Name:           "File migration",
		Source:         BlueprintUser,
		PromptTemplate: "List files",
		Workflow: WorkflowDef{Stages: []StageSpec{{
			ID: "stage", Title: "Stage", Tasks: []TaskSpec{{ID: "task", Title: "Task"}},
		}}},
		BlockSpecs: []BlockSpec{{
			ID: "files", Kind: "file_list", SchemaVersion: 1, Label: "Files",
			Placement: BlockPlacement{Slot: "primary", Order: 0}, Editable: true,
		}},
		CreatedAt: time.Now().UTC(),
	}
	if err := registry.Register(v1); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, registry, ViewSinkDiscard)
	source, err := svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: v1.ID, SchemaVersion: SchemaVersion, Version: 1},
		Name:         "Source", Inputs: map[string]any{}, RequestID: "create-file-migration",
	})
	if err != nil {
		t.Fatal(err)
	}
	view, err := svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: source.ID, BlockID: "files", Kind: "file_list", SchemaVersion: 1,
		Revision: 2, Status: BlockReady,
		Data:             json.RawMessage(`{"files":[{"path":"a.txt","status":"modified","desc":"legacy"}]}`),
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2, RequestID: "update-file-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Archive(context.Background(), source.ID, "archive-file-v1"); err != nil {
		t.Fatal(err)
	}

	v2 := *v1
	v2.Version = 2
	v2.CreatedAt = v1.CreatedAt.Add(time.Minute)
	v2.BlockSpecs = deepCopyBlockSpecs(v1.BlockSpecs)
	v2.BlockSpecs[0].SchemaVersion = 2
	v2.BlockSpecs[0].Placement = BlockPlacement{Slot: "result", Order: 1, Span: 8}
	if err := registry.Register(&v2); err != nil {
		t.Fatal(err)
	}

	plan, err := svc.PrepareRerun(context.Background(), PrepareRerunInput{
		RecordID: source.ID, Mode: RerunLatestDefinition,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocking || len(plan.BlockIssues) != 0 || plan.TargetDefinition.Version != 2 {
		t.Fatalf("unexpected migration plan: %+v", plan)
	}
	rerun, err := svc.ExecuteRerun(context.Background(), plan.PlanToken, "rerun-file-v2")
	if err != nil {
		t.Fatal(err)
	}
	if !rerun.RerunUpgraded || rerun.RerunOf != source.ID ||
		len(rerun.MigrationPath) != 2 || rerun.MigrationPath[1] != 2 {
		t.Fatalf("rerun provenance = %+v", rerun)
	}
	var migratedData struct {
		Files []struct {
			Desc        string `json:"desc"`
			Description string `json:"description"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rerun.Blocks[0].Data, &migratedData); err != nil {
		t.Fatal(err)
	}
	if len(rerun.Blocks) != 1 || rerun.Blocks[0].SchemaVersion != 2 ||
		len(migratedData.Files) != 1 || migratedData.Files[0].Desc != "" ||
		migratedData.Files[0].Description != "legacy" {
		t.Fatalf("rerun blocks = %+v; source revision=%d", rerun.Blocks, view.Revision)
	}
	if len(rerun.Placements) != 1 || rerun.Placements[0].Slot != "result" ||
		rerun.Placements[0].Span != 8 {
		t.Fatalf("rerun placements = %+v", rerun.Placements)
	}
}

func TestServiceRerunOriginalAndDeletedList(t *testing.T) {
	f := newServiceFixture(t)
	source := mustServiceCreate(t, f.svc, "rerun-source")
	if _, err := f.svc.Archive(context.Background(), source.ID, "rerun-archive"); err != nil {
		t.Fatalf("Archive: %v", err)
	}
	plan, err := f.svc.PrepareRerun(context.Background(), PrepareRerunInput{
		RecordID: source.ID, Mode: RerunOriginalDefinition,
	})
	if err != nil {
		t.Fatalf("PrepareRerun: %v", err)
	}
	if plan.Blocking || plan.PlanToken == "" || !plan.ExpiresAt.After(source.CreatedAt) {
		t.Fatalf("unexpected rerun plan: %+v", plan)
	}
	rerun, err := f.svc.ExecuteRerun(context.Background(), plan.PlanToken, "rerun-execute")
	if err != nil {
		t.Fatalf("ExecuteRerun: %v", err)
	}
	if rerun.RerunOf != source.ID || rerun.State != WorkDraft || rerun.ArchiveState != ArchiveActive {
		t.Fatalf("rerun metadata/state = %+v", rerun)
	}
	if rerun.Definition.Digest != source.Definition.Digest {
		t.Fatalf("rerun definition changed: %s != %s", rerun.Definition.Digest, source.Definition.Digest)
	}

	if err := f.svc.Delete(context.Background(), rerun.ID, "rerun-delete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	deleted := ArchiveDeleted
	page, err := f.svc.List(context.Background(), WorkFilter{ArchiveState: &deleted, Limit: 10})
	if err != nil {
		t.Fatalf("List deleted: %v", err)
	}
	if page.Total != 1 || page.Items[0].ID != rerun.ID {
		t.Fatalf("deleted page = %+v", page)
	}
}

func TestRunWorkRejectsEmptyPromptBeforePersistingRun(t *testing.T) {
	f := newRunnerFixture(t)
	work := f.createWork(t, BlueprintRef{ID: "blueprint:blank", SchemaVersion: SchemaVersion, Version: 1}, "empty-prompt-create")
	before := mustServiceView(t, f.svc, work.ID)
	if _, err := f.svc.RunWork(context.Background(), work.ID, "empty-prompt-run"); err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("RunWork error = %v, want prompt required", err)
	}
	after := mustServiceView(t, f.svc, work.ID)
	if after.Revision != before.Revision || len(after.Work.Runs) != 0 {
		t.Fatalf("empty prompt persisted partial run: before=%d after=%d runs=%d", before.Revision, after.Revision, len(after.Work.Runs))
	}
}
