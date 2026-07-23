package work

import (
	"context"
	"strings"
	"testing"
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
