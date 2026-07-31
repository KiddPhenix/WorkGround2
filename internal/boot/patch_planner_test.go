package boot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"workground2/internal/provider"
	"workground2/internal/work"
)

type patchPlannerProviderStub struct {
	sequences   [][]provider.Chunk
	streamError map[int]error
	requests    []provider.Request
}

func (p *patchPlannerProviderStub) Name() string { return "patch-planner-stub" }

func (p *patchPlannerProviderStub) Stream(
	_ context.Context,
	request provider.Request,
) (<-chan provider.Chunk, error) {
	call := len(p.requests)
	p.requests = append(p.requests, request)
	if err := p.streamError[call]; err != nil {
		return nil, err
	}
	chunks := []provider.Chunk(nil)
	if call < len(p.sequences) {
		chunks = p.sequences[call]
	}
	out := make(chan provider.Chunk, len(chunks))
	for _, chunk := range chunks {
		out <- chunk
	}
	close(out)
	return out, nil
}

func patchPlannerInput() work.PatchPlanInput {
	return work.PatchPlanInput{
		Instruction: "要有一个门派设定表格",
		Scope:       work.PatchBlock,
		Block: &work.BlockInstance{
			ID:       "v2-node-e694b6e99b86e8aebe",
			Title:    "收集设定",
			Revision: 1,
			Data:     json.RawMessage(`{"content":"当前设定"}`),
		},
	}
}

func patchChunks(text string) []provider.Chunk {
	return []provider.Chunk{
		{Type: provider.ChunkText, Text: text},
		{Type: provider.ChunkDone},
	}
}

const validPatchPlanJSON = `{"operations":[{"op":"replace","path":"blocks/v2-node-e694b6e99b86e8aebe/data","newValue":{"content":"| 门派 | 特点 |\n|---|---|"}}],"actions":[{"action":"rerun","nodeId":"v2-node-e694b6e99b86e8aebe","reason":"block content changed"}]}`

func TestPatchPlannerValidJSONDoesNotRetry(t *testing.T) {
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{patchChunks(validPatchPlanJSON)}}
	plan, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(plan.Actions) != 1 || len(prov.requests) != 1 {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
}

func TestPatchPlannerRepairsLegacyOperationArray(t *testing.T) {
	legacy := `[{"op":"replace","path":"blocks/v2-node-e694b6e99b86e8aebe/data","newValue":{"content":"updated"}}]`
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks(legacy),
		patchChunks(validPatchPlanJSON),
	}}
	plan, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 2 || len(plan.Actions) != 1 {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
}

func TestPatchPlannerRepairsNaturalLanguageOnce(t *testing.T) {
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks("当前工作是一个武侠小说流程。用户要求增加一个门派设定表格。"),
		patchChunks(validPatchPlanJSON),
	}}
	plan, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(prov.requests) != 2 {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
	repair := prov.requests[1]
	if len(repair.Messages) != 4 ||
		!strings.Contains(repair.Messages[1].Content, "要有一个门派设定表格") ||
		!strings.Contains(repair.Messages[3].Content, "operations and actions") {
		t.Fatalf("repair request does not preserve intent and strict contract: %+v", repair.Messages)
	}
}

func TestPatchPlannerParsesSemanticReformatAction(t *testing.T) {
	raw := `{"operations":[` +
		`{"op":"replace","path":"artifactSlots/route/kind","newValue":"docx"}` +
		`],"actions":[{"action":"reformat","artifactSlotId":"route","reason":"content remains valid"}]}`
	plan, err := parsePatchPlanResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(plan.Actions) != 1 ||
		plan.Actions[0].Action != work.PatchActionReformat ||
		plan.Actions[0].ArtifactSlotID != "route" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPatchPlannerRepairsWorkflowRuntimeBlockPathOnce(t *testing.T) {
	input := patchPlannerInput()
	input.Scope = work.PatchWorkflow
	input.TargetNodeID = "plan-theme"
	input.Definition = &work.WorkDefinitionRevision{
		WorkID:   "joke-series",
		Revision: 2,
		Nodes: []work.NodeDef{{
			ID:          "plan-theme",
			Title:       "Plan theme",
			Description: "Choose a coherent theme.",
		}},
	}
	const repaired = `{"operations":[{"op":"replace","path":"nodes/plan-theme/description","newValue":"Choose a coherent animal theme, focusing on chickens and ducks."}],"actions":[{"action":"rerun","nodeId":"plan-theme","reason":"semantic guidance changed"}]}`
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks(validPatchPlanJSON),
		patchChunks(repaired),
	}}

	plan, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 2 || len(plan.Operations) != 1 ||
		plan.Operations[0].Path != "nodes/plan-theme/description" {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
	repair := prov.requests[1]
	if len(repair.Messages) != 4 ||
		!strings.Contains(repair.Messages[3].Content, "runtime Block forbidden by workflow scope") {
		t.Fatalf("repair request does not explain the scope violation: %+v", repair.Messages)
	}
}

func TestPatchPlannerParsesJSONWithNaturalLanguageWrapper(t *testing.T) {
	raw := "好的，补丁如下：\n```json\n" + validPatchPlanJSON + "\n```\n请确认。"
	plan, err := parsePatchPlanResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPatchPlannerUsesLastStructuredPlanAfterReasoningExample(t *testing.T) {
	raw := `先参考示例：{"operations":[{"op":"replace","path":"artifactSlots/route_guide/title","newValue":"Route guide.docx"}],"actions":[{"action":"reformat","artifactSlotId":"route_guide"}]}` +
		"\n最终答案：" +
		`{"operations":[{"op":"replace","path":"artifactSlots/plan_doc/title","newValue":"团建方案.docx"}],"actions":[{"action":"reformat","artifactSlotId":"plan_doc"}]}`
	plan, err := parsePatchPlanResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || plan.Operations[0].Path != "artifactSlots/plan_doc/title" ||
		len(plan.Actions) != 1 || plan.Actions[0].ArtifactSlotID != "plan_doc" {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestPatchPlannerRepairsUnknownWorkflowTargetOnce(t *testing.T) {
	input := patchPlannerInput()
	input.Scope = work.PatchWorkflow
	input.TargetNodeID = "design_plan"
	input.Definition = &work.WorkDefinitionRevision{
		WorkID:   "team-building",
		Revision: 2,
		Nodes:    []work.NodeDef{{ID: "design_plan", ProducesSlotIDs: []string{"plan_doc"}}},
		ArtifactSlots: []work.ArtifactSlotDef{{
			ID: "plan_doc", Title: "团建方案", Kind: "document",
		}},
	}
	unknown := `{"operations":[{"op":"replace","path":"artifactSlots/route_guide/title","newValue":"团建方案.docx"}],"actions":[{"action":"reformat","artifactSlotId":"route_guide"}]}`
	repaired := `{"operations":[{"op":"replace","path":"artifactSlots/plan_doc/title","newValue":"团建方案.docx"}],"actions":[{"action":"reformat","artifactSlotId":"plan_doc"}]}`
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks(unknown),
		patchChunks(repaired),
	}}

	plan, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if len(prov.requests) != 2 || plan.Operations[0].Path != "artifactSlots/plan_doc/title" {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
	if !strings.Contains(prov.requests[1].Messages[3].Content, `unknown artifact slot ID "route_guide"`) {
		t.Fatalf("repair request missing authoritative target error: %+v", prov.requests[1].Messages)
	}
}

func TestPatchPlannerValidatesWorkflowRootWithoutObjectID(t *testing.T) {
	input := patchPlannerInput()
	input.Scope = work.PatchWorkflow
	input.Definition = &work.WorkDefinitionRevision{
		Nodes: []work.NodeDef{{ID: "design_plan"}},
	}
	plan := &work.PatchPlan{
		Operations: []work.PatchOp{{
			Op: "replace", Path: "goal", NewValue: json.RawMessage(`"更新后的目标"`),
		}},
		Actions: []work.PatchAction{{
			Action: work.PatchActionRerun, NodeID: "design_plan",
		}},
	}
	if err := validatePatchPlanScope(input, plan); err != nil {
		t.Fatal(err)
	}
}

func TestPatchPlannerRejectsUnrelatedJSONObject(t *testing.T) {
	if _, err := parsePatchPlanResponse(`说明：{"goal":"只是一段摘要"}`); err == nil {
		t.Fatal("unrelated JSON object must not become an empty PatchPlan")
	}
}

func TestPatchPlannerRepairFailureIsExplicitAndSafe(t *testing.T) {
	const secret = "凤凰计划-绝密"
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks("首次回答：" + secret),
		patchChunks("修复回答仍然不是 JSON：" + secret),
	}}
	_, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err == nil || !strings.Contains(err.Error(), "repair response invalid") {
		t.Fatalf("err=%v", err)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("calls=%d, want 2", len(prov.requests))
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaks raw model response: %v", err)
	}
}

func TestPatchPlannerRepairChunkErrorIsExplicit(t *testing.T) {
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks("自然语言回答"),
		{{Type: provider.ChunkError, Err: errors.New("model overloaded")}},
	}}
	_, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err == nil ||
		!strings.Contains(err.Error(), "repair") ||
		!strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err=%v", err)
	}
	if len(prov.requests) != 2 {
		t.Fatalf("calls=%d, want 2", len(prov.requests))
	}
}

func TestPatchPlannerRepairStreamErrorIsExplicit(t *testing.T) {
	prov := &patchPlannerProviderStub{
		sequences:   [][]provider.Chunk{patchChunks("自然语言回答")},
		streamError: map[int]error{1: errors.New("network unavailable")},
	}
	_, err := newBootPatchPlanner(prov, 0, 2048, nil).PlanPatch(context.Background(), patchPlannerInput())
	if err == nil ||
		!strings.Contains(err.Error(), "repair") ||
		!strings.Contains(err.Error(), "network unavailable") {
		t.Fatalf("err=%v", err)
	}
}

func TestPatchPlannerPromptIncludesBlockDataAndExactExample(t *testing.T) {
	prompt := buildPatchPlannerSystemPrompt(patchPlannerInput())
	for _, want := range []string{
		`data={"content":"当前设定"}`,
		`"path":"blocks/<blockID>/data"`,
		"Do not summarize or restate",
		"For block content or table requests",
		`blocks/<blockID>/data newValue must be a raw JSON object`,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestPatchPlannerWorkflowPromptAnchorsGuidanceToTargetNode(t *testing.T) {
	input := patchPlannerInput()
	input.Scope = work.PatchWorkflow
	input.TargetNodeID = "plan-theme"
	input.Definition = &work.WorkDefinitionRevision{
		WorkID:   "joke-series",
		Revision: 2,
		Goal:     "创作系列笑话集",
		Nodes: []work.NodeDef{
			{ID: "plan-theme", Title: "Plan theme"},
			{ID: "create-jokes", Title: "Create jokes", DependsOn: []string{"plan-theme"}},
		},
	}
	prompt := buildPatchPlannerSystemPrompt(input)
	for _, want := range []string{
		"workflow revision",
		"nodes/<targetNodeID>/description",
		"Target Node: id=plan-theme",
		"downstream nodes execute with the new guidance",
		"infer exactly one existing producer",
		"Never ask the user to choose",
		"keeps its existing slot ID",
		"never model an edit as remove plus add",
		"Changing only the title or MIME is not a format change",
		"For a format-only result change, do not modify any node description",
		`"action":"reformat"`,
		`description=""`,
		`"path":"nodes/plan-theme/description"`,
		"Runtime Block paths are forbidden",
		"Reference output Block (read-only)",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("workflow prompt missing %q:\n%s", want, prompt)
		}
	}
	for _, forbidden := range []string{
		"Allowed paths for this Block patch",
		`"path":"blocks/<blockID>/data"`,
		"For block content or table requests",
	} {
		if strings.Contains(prompt, forbidden) {
			t.Fatalf("workflow prompt contains block-only guidance %q:\n%s", forbidden, prompt)
		}
	}
}

// ── Locale language constraint tests ──────────────────────────────────────

func TestPatchPlanner_LocalePrompt_SimplifiedChinese(t *testing.T) {
	input := patchPlannerInput()
	input.Work = &work.Work{ID: "w1", Locale: "zh"}
	prompt := buildPatchPlannerSystemPrompt(input)
	if !strings.Contains(prompt, "Simplified Chinese") {
		t.Fatal("patch planner prompt missing Simplified Chinese language constraint")
	}
}

func TestPatchPlanner_LocalePrompt_English(t *testing.T) {
	input := patchPlannerInput()
	input.Work = &work.Work{ID: "w1", Locale: "en"}
	prompt := buildPatchPlannerSystemPrompt(input)
	if !strings.Contains(prompt, "English") {
		t.Fatal("patch planner prompt missing English language constraint\n" + prompt)
	}
}

func TestPatchPlanner_LocalePrompt_TraditionalChinese(t *testing.T) {
	input := patchPlannerInput()
	input.Work = &work.Work{ID: "w1", Locale: "zh-TW"}
	prompt := buildPatchPlannerSystemPrompt(input)
	if !strings.Contains(prompt, "Traditional Chinese") {
		t.Fatal("patch planner prompt missing Traditional Chinese language constraint")
	}
}

func TestPatchPlanner_LocalePrompt_NoWorkSkipsDirective(t *testing.T) {
	input := patchPlannerInput()
	// input.Work is nil
	prompt := buildPatchPlannerSystemPrompt(input)
	if strings.Contains(prompt, "Simplified Chinese") || strings.Contains(prompt, "English") {
		t.Fatal("patch planner prompt should not contain language directive when Work is nil")
	}
}
