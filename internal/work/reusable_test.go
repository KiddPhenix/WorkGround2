package work

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
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

func TestReusableFlowV2DispatchReturnsBeforeDAGAndConverges(t *testing.T) {
	store := newTestFileWorkStore(t)
	svc := NewService(store, nil, nil)
	ctx := context.Background()

	view, err := svc.BeginWorkPlanning(ctx, BeginWorkPlanningInput{
		SessionID: "reusable-async-session", RequestID: "reusable-async-source",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := v2def(view.Work.ID, 2)
	candidate.Goal = "制作第一卷小说"
	candidate.InputSpecs[0].Label = "主题"
	candidate.InputSpecs[0].DefaultValue = json.RawMessage(`"海洋"`)
	// No artifact slots: completion depends only on node runtimes.
	candidate.ArtifactSlots = nil
	created, err := svc.CreateCandidateRevision(ctx, view.Work.ID, candidate, "reusable-async-candidate", view.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: created.Revision,
		ExpectedRevision: state.Revision, RequestID: "reusable-async-apply",
	}); err != nil {
		t.Fatal(err)
	}
	flow, err := svc.SaveReusableFlow(ctx, SaveReusableFlowInput{
		SourceWorkID: view.Work.ID, Name: "异步流程",
		VariableKeys: []string{"goal", "input:is1"}, RequestID: "save-reusable-async",
	})
	if err != nil {
		t.Fatal(err)
	}

	requestID := "run-reusable-async"
	workID := workIDForRequest(requestID + "/work")
	runID := workflowRunID(workID, requestID+"/apply")
	executor := &coordinatorExecutor{
		blockRun: runID,
		started:  make(chan TaskExecuteInput, 1),
		release:  make(chan struct{}),
	}
	svc.SetTaskExecutor(executor)
	input := RunReusableFlowInput{
		FlowID: flow.ID,
		Values: map[string]json.RawMessage{
			"goal":      json.RawMessage(`"制作第二卷小说"`),
			"input:is1": json.RawMessage(`"山地"`),
		},
		RequestID: requestID,
	}

	startedAt := time.Now()
	run, err := svc.RunReusableFlow(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	// The DAG executor is blocked, so a prompt return proves the create/apply
	// returned before foreground node execution could block.
	if run.Work == nil || run.Work.ID != workID || run.Run == nil {
		t.Fatalf("reusable async run = %+v", run)
	}
	select {
	case <-executor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("reusable DAG wake was never dispatched")
	}
	if elapsed := time.Since(startedAt); elapsed > 3*time.Second {
		t.Fatalf("RunReusableFlow blocked on the DAG for %v", elapsed)
	}
	// Release the DAG and await durable completion.
	close(executor.release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, _, loadErr := store.LoadState(workID, "")
		if loadErr == nil && current.State == WorkCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("reusable run did not complete: err=%v state=%v", loadErr, currentStateName(store, workID, loadErr))
		}
		time.Sleep(25 * time.Millisecond)
	}

	// A restart with the same request converges to the same Work (and the
	// Session binding derives from the same request ID), never a new Work.
	restarted := NewService(store, nil, nil)
	restarted.SetTaskExecutor(&fakeRunnerExecutor{})
	replay, err := restarted.RunReusableFlow(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Duplicate || replay.Work == nil || replay.Work.ID != workID {
		t.Fatalf("reusable replay = %+v", replay)
	}
}

func currentStateName(store *FileWorkStore, workID string, loadErr error) WorkState {
	if loadErr == nil {
		if current, _, err := store.LoadState(workID, ""); err == nil {
			return current.State
		}
	}
	return ""
}

func TestReusableFlowRejectsFixedFieldOverride(t *testing.T) {
	fields := []ReusableField{{Key: "goal", Label: "目标", Kind: "text", Variable: false, Value: json.RawMessage(`"固定"`)}}
	if _, err := reusableRunValues(fields, map[string]json.RawMessage{"goal": json.RawMessage(`"篡改"`)}); err == nil {
		t.Fatal("fixed field override was accepted")
	}
}

func TestReusableFlowV1PreservesCurrentStructure(t *testing.T) {
	f := newRunnerFixture(t)
	bp := testBlueprint("blueprint:reusable-preserve", 1, testWorkflow("draft", "write"))
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","title":"主题"}},"required":["topic"]}`)
	// 标记 block 为可编辑，以便模拟用户创建后编辑
	bp.BlockSpecs[0].Editable = true
	f.registerBlueprint(t, bp)

	source, err := f.svc.Create(context.Background(), CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1},
		Name:         "保存测试", Prompt: "原始内容",
		Inputs: map[string]any{"topic": "原始"}, RequestID: "reusable-preserve-source",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 模拟用户创建后编辑 block
	modifiedData := json.RawMessage(`{"content":"用户编辑后的笔记内容"}`)
	if _, err := f.svc.UpsertBlock(context.Background(), BlockUpsertInput{
		WorkID: source.ID, BlockID: "notes", Kind: "markdown", SchemaVersion: 1,
		Revision: 2, Status: BlockReady,
		Data:             modifiedData,
		Source:           BlockSource{Provider: "user", Mode: "snapshot"},
		ExpectedRevision: 2, RequestID: "reusable-preserve-upsert",
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟用户 Pin 一个 snapshot Cornerstone
	f.svc.SetCornerstoneManager(NewCornerstoneManager(f.store, f.store, RealClock{}))
	cornerstoneContent := strings.Repeat("story-rule ", CornerstoneInlineThreshold)
	csResult, err := f.svc.PinCornerstone(context.Background(), source.ID, PinCornerstoneInput{
		Type: CornerstoneDecision, Title: "测试决策",
		Content:          cornerstoneContent,
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         true,
		Tags:             []string{"格式"},
		ExpectedRevision: 3, RequestID: "reusable-preserve-cs",
	})
	if err != nil {
		t.Fatal(err)
	}
	sourceCsID := csResult.Cornerstone.ID
	if sourceCsID == "" || csResult.Cornerstone.WorkID != source.ID {
		t.Fatalf("source cornerstone identity: id=%q workID=%q", sourceCsID, csResult.Cornerstone.WorkID)
	}
	if csResult.Cornerstone.Ref.BlobDigest == "" {
		t.Fatal("source cornerstone was not stored as a blob")
	}

	// 保存为常用流程
	flow, err := f.svc.SaveReusableFlow(context.Background(), SaveReusableFlowInput{
		SourceWorkID: source.ID, Name: "保存测试流程",
		VariableKeys: []string{"prompt", "input:topic"}, RequestID: "save-preserve",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 加载已保存的 flow 记录，验证 template 中的 blocks 和 cornerstones
	record, err := f.store.LoadReusableFlow(flow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatal("flow record not found")
	}
	if len(record.Template.Blocks) != 1 {
		t.Fatalf("template blocks = %d, want 1", len(record.Template.Blocks))
	}
	if !bytes.Equal(record.Template.Blocks[0].Data, modifiedData) {
		t.Fatalf("template block data = %s, want %s", record.Template.Blocks[0].Data, modifiedData)
	}
	if len(record.Template.Cornerstones) != 1 {
		t.Fatalf("template cornerstones = %d, want 1", len(record.Template.Cornerstones))
	}
	templateCs := record.Template.Cornerstones[0]
	if templateCs.Content != csResult.Cornerstone.Content || templateCs.Type != csResult.Cornerstone.Type {
		t.Fatalf("template cornerstone content/type = %q/%s, want %q/%s",
			templateCs.Content, templateCs.Type, csResult.Cornerstone.Content, csResult.Cornerstone.Type)
	}

	// 运行流程，验证新 Work
	restarted := f.restart(t)
	restarted.SetCornerstoneManager(NewCornerstoneManager(f.store, f.store, RealClock{}))
	run, err := restarted.RunReusableFlow(context.Background(), RunReusableFlowInput{
		FlowID: flow.ID,
		Values: map[string]json.RawMessage{
			"prompt":      json.RawMessage(`"新内容"`),
			"input:topic": json.RawMessage(`"新主题"`),
		},
		RequestID: "run-preserve",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Work == nil {
		t.Fatal("run.Work is nil")
	}
	if len(run.Work.Blocks) != 1 {
		t.Fatalf("new work blocks = %d, want 1", len(run.Work.Blocks))
	}
	// 比较反序列化后的内容，避免 JSON 格式化差异
	var gotContent, wantContent struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(run.Work.Blocks[0].Data, &gotContent); err != nil {
		t.Fatalf("unmarshal block data: %v", err)
	}
	if err := json.Unmarshal(modifiedData, &wantContent); err != nil {
		t.Fatalf("unmarshal modified data: %v", err)
	}
	if gotContent.Content != wantContent.Content {
		t.Fatalf("new work block content = %q, want %q", gotContent.Content, wantContent.Content)
	}
	// 新 Work 必须有独立身份
	if run.Work.ID == source.ID {
		t.Fatal("new work has same ID as source")
	}
	// 源 Work 未被修改
	if run.Work.CopiedFrom != "" {
		t.Fatalf("new work CopiedFrom = %q, want empty (reusable flow uses RerunOf)", run.Work.CopiedFrom)
	}
	// Cornerstone 已重绑定到新 Work
	if len(run.Work.Cornerstones) != 1 {
		t.Fatalf("new work cornerstones = %d, want 1", len(run.Work.Cornerstones))
	}
	newCs := run.Work.Cornerstones[0]
	if newCs.ID == sourceCsID {
		t.Fatalf("new work cornerstone ID = %q, still matches source ID", newCs.ID)
	}
	wantCsID, err := computeStableCornerstoneID(run.Work.ID, PinCornerstoneInput{
		Type: newCs.Type, Content: cornerstoneContent, Ref: newCs.Ref,
	})
	if err != nil {
		t.Fatal(err)
	}
	if newCs.ID != wantCsID {
		t.Fatalf("new work cornerstone ID = %q, want %q", newCs.ID, wantCsID)
	}
	if newCs.WorkID != run.Work.ID {
		t.Fatalf("new work cornerstone WorkID = %q, want %q", newCs.WorkID, run.Work.ID)
	}
	if newCs.Content != csResult.Cornerstone.Content || newCs.Type != csResult.Cornerstone.Type ||
		newCs.Mode != csResult.Cornerstone.Mode || newCs.Required != csResult.Cornerstone.Required {
		t.Fatalf("new cornerstone content/type/mode/required mismatched: %+v vs %+v", newCs, csResult.Cornerstone)
	}
	if len(newCs.Tags) != 1 || newCs.Tags[0] != "格式" {
		t.Fatalf("new cornerstone tags = %v, want [格式]", newCs.Tags)
	}
	gotBlob, err := f.store.Get(run.Work.ID, newCs.Ref.BlobDigest)
	if err != nil {
		t.Fatalf("get copied cornerstone blob: %v", err)
	}
	if string(gotBlob) != cornerstoneContent {
		t.Fatal("copied cornerstone blob content mismatched")
	}
	after := mustServiceView(t, f.svc, source.ID)
	if len(after.Work.Runs) != 0 {
		t.Fatal("source Work runs was mutated")
	}
	// 源 Cornerstone 未被修改
	if len(after.Work.Cornerstones) != 1 || after.Work.Cornerstones[0].ID != sourceCsID {
		t.Fatal("source cornerstone was mutated")
	}
}

// TestReusableRunCommitted guards the host rollback probe: before a
// RunReusableFlow commits, ReusableRunCommitted is false; after a committed
// run (or replay), it is true; an unknown requestID is false, not an error.
func TestReusableRunCommitted(t *testing.T) {
	f := newRunnerFixture(t)
	ctx := context.Background()
	const requestID = "run-committed-probe"
	if committed, err := f.svc.ReusableRunCommitted(ctx, requestID); err != nil || committed {
		t.Fatalf("pre-commit probe = (%v, %v), want (false, nil)", committed, err)
	}

	bp := testBlueprint("blueprint:reusable-committed", 1, testWorkflow("draft", "write"))
	bp.InputSchema = json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","title":"主题"}},"required":["topic"]}`)
	f.registerBlueprint(t, bp)
	source, err := f.svc.Create(ctx, CreateWorkInput{
		BlueprintRef: BlueprintRef{ID: bp.ID, SchemaVersion: SchemaVersion, Version: 1},
		Name:         "提交探测", Prompt: "写一份报告", RequestID: "run-committed-source",
		Inputs: map[string]any{"topic": "报告主题"},
	})
	if err != nil {
		t.Fatal(err)
	}
	flow, err := f.svc.SaveReusableFlow(ctx, SaveReusableFlowInput{
		SourceWorkID: source.ID, Name: "提交探测", VariableKeys: []string{"prompt"}, RequestID: "save-committed-probe",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RunReusableFlow(ctx, RunReusableFlowInput{
		FlowID: flow.ID, Values: map[string]json.RawMessage{"prompt": json.RawMessage(`"报告"`)}, RequestID: requestID,
	}); err != nil {
		t.Fatal(err)
	}
	if committed, err := f.svc.ReusableRunCommitted(ctx, requestID); err != nil || !committed {
		t.Fatalf("post-commit probe = (%v, %v), want (true, nil)", committed, err)
	}
	if committed, err := f.svc.ReusableRunCommitted(ctx, "run-never-started"); err != nil || committed {
		t.Fatalf("unknown request probe = (%v, %v), want (false, nil)", committed, err)
	}
}
