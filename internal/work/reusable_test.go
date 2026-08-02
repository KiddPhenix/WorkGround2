package work

import (
	"context"
	"encoding/json"
	"testing"
)

func TestReusableFlowV1PersistsAndRunsIdempotently(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:reusable-v1", 1, testWorkflow("draft", "write"))
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","title":"主题"}},"required":["topic"]}`)
	f.registerBlueprint(t, bp)
	source, err := f.svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1},
		Name:         "长篇小说创作", Prompt: "写一部海洋小说",
		Inputs: map[string]any{"topic": "海洋"}, RequestID: "reusable-v1-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	before := mustServiceView(t, f.svc, source.ID)
	setup, err := f.svc.PrepareReusableFlow(context.Background(), PrepareReusableFlowInput{SourceWorkID: source.ID})
	if err != nil {
		t.Fatal(err)
	}
	if setup.Existing != nil || len(setup.Fields) != 2 {
		t.Fatalf("initial setup = %+v", setup)
	}
	flow, err := f.svc.SaveReusableFlow(context.Background(), SaveReusableFlowInput{
		SourceWorkID: source.ID, Name: "长篇小说创作",
		VariableKeys: []string{"prompt", "input:topic"}, RequestID: "save-reusable-v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if flow.ID == "" || flow.Digest == "" || len(flow.Fields) != 2 {
		t.Fatalf("saved flow = %+v", flow)
	}

	restarted := f.restart(t)
	setup, err = restarted.PrepareReusableFlow(context.Background(), PrepareReusableFlowInput{SourceWorkID: source.ID})
	if err != nil || setup.Existing == nil || setup.Existing.ID != flow.ID {
		t.Fatalf("persisted setup = %+v, err=%v", setup, err)
	}
	input := RunReusableFlowInput{
		FlowID: flow.ID,
		Values: map[string]json.RawMessage{
			"prompt":      json.RawMessage(`"写一部山地小说"`),
			"input:topic": json.RawMessage(`"山地"`),
		},
		RequestID: "run-reusable-v1",
	}
	first, err := restarted.RunReusableFlow(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if first.Work == nil || first.Work.ID == source.ID || first.Work.RerunOf != source.ID ||
		first.Work.ReusableFlowID != flow.ID || first.Work.Prompt != "写一部山地小说" || first.Work.Inputs["topic"] != "山地" {
		t.Fatalf("reusable run = %+v", first)
	}
	replay, err := f.restart(t).RunReusableFlow(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Work.ID != first.Work.ID || replay.Run == nil || replay.Run.ID != first.Run.ID {
		t.Fatalf("reusable replay = %+v", replay)
	}
	after := mustServiceView(t, f.svc, source.ID)
	if after.Revision != before.Revision || after.Work.Prompt != before.Work.Prompt {
		t.Fatalf("source mutated: before=%d/%q after=%d/%q", before.Revision, before.Work.Prompt, after.Revision, after.Work.Prompt)
	}
}

func TestReusableFlowV2FreezesDefinitionAndSeedsInputs(t *testing.T) {
	store, _, svc := newFS(t)
	ctx := context.Background()
	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{SessionID: "reusable-v2-session", RequestID: "reusable-v2-source"})
	if err != nil {
		t.Fatal(err)
	}
	candidate := v2def(view.Work.ID, 2)
	candidate.Goal = "制作第一卷小说"
	candidate.InputSpecs[0].Label = "主题"
	candidate.InputSpecs[0].DefaultValue = json.RawMessage(`"海洋"`)
	created, err := svc.CreateCandidateRevision(ctx, view.Work.ID, candidate, "reusable-v2-candidate", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: created.Revision,
		ExpectedRevision: state.Revision, RequestID: "reusable-v2-apply",
	}); err != nil {
		t.Fatal(err)
	}
	flow, err := svc.SaveReusableFlow(ctx, SaveReusableFlowInput{
		SourceWorkID: view.Work.ID, Name: "长篇小说创作",
		VariableKeys: []string{"goal", "input:is1"}, RequestID: "save-reusable-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunReusableFlow(ctx, RunReusableFlowInput{
		FlowID: flow.ID,
		Values: map[string]json.RawMessage{
			"goal":      json.RawMessage(`"制作第二卷小说"`),
			"input:is1": json.RawMessage(`"山地"`),
		},
		RequestID: "run-reusable-v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Work == nil || run.Work.ID == view.Work.ID || run.Work.RerunOf != view.Work.ID || run.Run == nil {
		t.Fatalf("V2 reusable run = %+v", run)
	}
	definition, err := store.LoadRevision(run.Work.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Goal != "制作第二卷小说" || string(definition.InputSpecs[0].DefaultValue) != `"山地"` {
		t.Fatalf("derived definition = %+v", definition)
	}
	target, _, err := store.LoadState(run.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if target.ReusableFlowID != flow.ID || target.V2CurrentRevision != 1 || len(target.V2Inputs) != 1 ||
		target.V2Inputs[0].State != InputSubmitted || string(target.V2Inputs[0].Value) != `"山地"` {
		t.Fatalf("seeded target = %+v", target)
	}
	source, _, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if source.ReusableFlowID != "" || source.RerunOf != "" {
		t.Fatalf("source provenance mutated = %+v", source)
	}
}

func TestReusableFlowRejectsFixedFieldOverride(t *testing.T) {
	fields := []ReusableField{{Key: "goal", Label: "目标", Kind: "text", Variable: false, Value: json.RawMessage(`"固定"`)}}
	if _, err := reusableRunValues(fields, map[string]json.RawMessage{"goal": json.RawMessage(`"篡改"`)}); err == nil {
		t.Fatal("fixed field override was accepted")
	}
}
