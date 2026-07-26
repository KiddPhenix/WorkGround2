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

const validPatchPlanJSON = `[{"op":"replace","path":"blocks/v2-node-e694b6e99b86e8aebe/data","newValue":{"content":"| 门派 | 特点 |\n|---|---|"}}]`

func TestPatchPlannerValidJSONDoesNotRetry(t *testing.T) {
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{patchChunks(validPatchPlanJSON)}}
	plan, err := newBootPatchPlanner(prov, 0, 2048).PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(prov.requests) != 1 {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
}

func TestPatchPlannerRepairsNaturalLanguageOnce(t *testing.T) {
	prov := &patchPlannerProviderStub{sequences: [][]provider.Chunk{
		patchChunks("当前工作是一个武侠小说流程。用户要求增加一个门派设定表格。"),
		patchChunks(validPatchPlanJSON),
	}}
	plan, err := newBootPatchPlanner(prov, 0, 2048).PlanPatch(context.Background(), patchPlannerInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 1 || len(prov.requests) != 2 {
		t.Fatalf("plan=%+v calls=%d", plan, len(prov.requests))
	}
	repair := prov.requests[1]
	if len(repair.Messages) != 4 ||
		!strings.Contains(repair.Messages[1].Content, "要有一个门派设定表格") ||
		!strings.Contains(repair.Messages[3].Content, "JSON array") {
		t.Fatalf("repair request does not preserve intent and strict contract: %+v", repair.Messages)
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
	_, err := newBootPatchPlanner(prov, 0, 2048).PlanPatch(context.Background(), patchPlannerInput())
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
	_, err := newBootPatchPlanner(prov, 0, 2048).PlanPatch(context.Background(), patchPlannerInput())
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
	_, err := newBootPatchPlanner(prov, 0, 2048).PlanPatch(context.Background(), patchPlannerInput())
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
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
