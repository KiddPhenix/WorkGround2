package work

import (
	"context"
	"testing"
)

func TestServiceUpdateDraftKeepsManualNameAcrossPromptEdits(t *testing.T) {
	f := newServiceFixture(t)
	input := serviceCreateInput("service-manual-name-create")
	input.Name = ""
	input.Prompt = "自动推定名称\n初始任务说明"

	value, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	manualName := "用户自定义名称"
	renamed, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           value.ID,
		Name:             &manualName,
		ExpectedRevision: 2,
		RequestID:        "service-manual-name-rename",
	})
	if err != nil {
		t.Fatalf("rename UpdateDraft: %v", err)
	}

	nextPrompt := "新的任务说明\n需要更新工作结构"
	updated, err := f.restart(t).UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           value.ID,
		Prompt:           &nextPrompt,
		ExpectedRevision: renamed.Revision,
		RequestID:        "service-manual-name-prompt",
	})
	if err != nil {
		t.Fatalf("prompt UpdateDraft: %v", err)
	}
	if updated.Work.Name != manualName {
		t.Fatalf("Name = %q, want manual name %q", updated.Work.Name, manualName)
	}
	if updated.Work.Prompt != nextPrompt {
		t.Fatalf("Prompt = %q, want %q", updated.Work.Prompt, nextPrompt)
	}
}

func TestServiceUpdateDraftContinuesUpdatingAutomaticName(t *testing.T) {
	f := newServiceFixture(t)
	input := serviceCreateInput("service-auto-name-create")
	input.Name = ""
	input.Prompt = "旧的自动名称\n初始说明"

	value, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	nextPrompt := "新的自动名称\n更新说明"
	updated, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           value.ID,
		Prompt:           &nextPrompt,
		ExpectedRevision: 2,
		RequestID:        "service-auto-name-prompt",
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.Work.Name != "新的自动名称" {
		t.Fatalf("Name = %q, want updated automatic name", updated.Work.Name)
	}
}

func TestServiceUpdateDraftSavesPromptAndManualNameTogether(t *testing.T) {
	f := newServiceFixture(t)
	input := serviceCreateInput("service-combined-name-create")
	input.Name = ""
	input.Prompt = ""

	value, err := f.svc.Create(context.Background(), input)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	name := "用户指定的报告名称"
	prompt := "生成一份月度经营报告"
	updated, err := f.svc.UpdateDraft(context.Background(), UpdateDraftInput{
		WorkID:           value.ID,
		Name:             &name,
		Prompt:           &prompt,
		ExpectedRevision: 2,
		RequestID:        "service-combined-name-save",
	})
	if err != nil {
		t.Fatalf("UpdateDraft: %v", err)
	}
	if updated.Work.Name != name {
		t.Fatalf("Name = %q, want explicit name %q", updated.Work.Name, name)
	}
	if updated.Work.Prompt != prompt {
		t.Fatalf("Prompt = %q, want %q", updated.Work.Prompt, prompt)
	}
}
