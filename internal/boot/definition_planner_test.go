package boot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"workground2/internal/provider"
	"workground2/internal/work"
)

type definitionPlannerProviderStub struct {
	chunks []provider.Chunk
	err    error
	calls  int
	last   provider.Request
}

func (p *definitionPlannerProviderStub) Name() string { return "definition-planner-stub" }

func (p *definitionPlannerProviderStub) Stream(_ context.Context, request provider.Request) (<-chan provider.Chunk, error) {
	p.calls++
	p.last = request
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan provider.Chunk, len(p.chunks))
	for _, chunk := range p.chunks {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

// validPlanJSON returns a minimal valid DefinitionPlan JSON string.
func validPlanJSON() string {
	return `{"goal":"deliver","nodes":[{"id":"collect","title":"Collect","inputSpecIds":["topic"],"producesSlotIds":["source"]},{"id":"review","title":"Review","dependsOn":["collect"],"consumesSlotIds":["source"],"producesSlotIds":["report"]}],"artifactSlots":[{"id":"source","title":"Source","kind":"text","expectedCount":1,"required":true},{"id":"report","title":"Report","kind":"document","expectedCount":1,"required":true}],"inputSpecs":[{"id":"topic","label":"Topic","kind":"text","required":true}]}`
}

func parsePlan(raw string) (*work.DefinitionPlan, error) {
	return parseDefinitionPlanResponse(raw)
}

func mustParsePlan(t *testing.T, raw string) *work.DefinitionPlan {
	t.Helper()
	plan, err := parsePlan(raw)
	if err != nil {
		t.Fatalf("unexpected parse error: %v\nraw: %s", err, raw)
	}
	if plan == nil {
		t.Fatal("nil plan without error")
	}
	return plan
}

func mustFailParse(t *testing.T, raw, want string) {
	t.Helper()
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatalf("expected error containing %q, got nil\nraw: %s", want, raw)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q\nraw: %s", err.Error(), want, raw)
	}
}

// ── parseDefinitionPlanResponse unit tests ─────────────────────────────────

func TestParseDefinitionPlanResponse_PureJSON(t *testing.T) {
	plan := mustParsePlan(t, validPlanJSON())
	if plan.Goal != "deliver" || len(plan.Nodes) != 2 || len(plan.ArtifactSlots) != 2 || len(plan.InputSpecs) != 1 {
		t.Fatalf("plan fields incorrect: %+v", plan)
	}
}

func TestParseDefinitionPlanResponse_Empty(t *testing.T) {
	mustFailParse(t, "", "empty model response")
}

func TestParseDefinitionPlanResponse_ChinesePrefaceThenJSON(t *testing.T) {
	// Reproduces the original bug: model outputs Chinese explanation before JSON.
	raw := "好的，以下是结构规划方案：\n" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ChinesePrefaceWithNonASCIIPunctuation(t *testing.T) {
	raw := "以下是基于您的需求生成的结构定义：" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if len(plan.Nodes) != 2 {
		t.Fatalf("nodes=%d, want 2", len(plan.Nodes))
	}
}

func TestParseDefinitionPlanResponse_MarkdownFenceJSON(t *testing.T) {
	raw := "```json\n" + validPlanJSON() + "\n```"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_MarkdownFenceNoLanguageTag(t *testing.T) {
	raw := "```\n" + validPlanJSON() + "\n```"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ChinesePrefaceWithFence(t *testing.T) {
	raw := "好的，以下是规划结果：\n\n```json\n" + validPlanJSON() + "\n```\n\n希望对您有帮助！"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_UTF8BOM(t *testing.T) {
	raw := "\xEF\xBB\xBF" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_BOMWithChinesePreface(t *testing.T) {
	raw := "\xEF\xBB\xBF好的，" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_TrailingTextAfterJSON(t *testing.T) {
	raw := validPlanJSON() + "\n\n希望对您有帮助！如有需要请告诉我。"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("trailing text broke parsing: goal=%q", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_StringContainsBraces(t *testing.T) {
	// Braces inside JSON string values must not confuse the scanner.
	raw := `{"goal":"use {braces} inside","nodes":[{"id":"n1","title":"Node {with} braces","producesSlotIds":["s1"]}],"artifactSlots":[{"id":"s1","title":"Slot","kind":"text","expectedCount":1,"required":false}],"inputSpecs":[]}`
	plan := mustParsePlan(t, raw)
	if plan.Goal != "use {braces} inside" {
		t.Fatalf("goal=%q", plan.Goal)
	}
	if plan.Nodes[0].Title != "Node {with} braces" {
		t.Fatalf("node title=%q", plan.Nodes[0].Title)
	}
}

func TestParseDefinitionPlanResponse_StringContainsEscapedQuotes(t *testing.T) {
	raw := `{"goal":"say \"hello\" world","nodes":[{"id":"n1","title":"Quote \"test\"","producesSlotIds":["s1"]}],"artifactSlots":[{"id":"s1","title":"Slot","kind":"text","expectedCount":1,"required":false}],"inputSpecs":[]}`
	plan := mustParsePlan(t, raw)
	if plan.Goal != `say "hello" world` {
		t.Fatalf("goal=%q", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_StringContainsEscapedBackslash(t *testing.T) {
	raw := `{"goal":"path\\\\to\\\\file","nodes":[{"id":"n1","title":"Node","producesSlotIds":["s1"]}],"artifactSlots":[{"id":"s1","title":"Slot","kind":"text","expectedCount":1,"required":false}],"inputSpecs":[]}`
	plan := mustParsePlan(t, raw)
	if plan.Goal != `path\\to\\file` {
		t.Fatalf("goal=%q", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ChineseInStringValues(t *testing.T) {
	raw := `{"goal":"交付产品","nodes":[{"id":"n1","title":"收集需求","inputSpecIds":["topic"],"producesSlotIds":["source"]}],"artifactSlots":[{"id":"source","title":"需求文档","kind":"text","expectedCount":1,"required":true}],"inputSpecs":[{"id":"topic","label":"主题","kind":"text","required":true}]}`
	plan := mustParsePlan(t, raw)
	if plan.Goal != "交付产品" {
		t.Fatalf("goal=%q", plan.Goal)
	}
	if plan.Nodes[0].Title != "收集需求" {
		t.Fatalf("node title=%q", plan.Nodes[0].Title)
	}
}

func TestParseDefinitionPlanResponse_StringContainsMarkdownFenceChars(t *testing.T) {
	// Triple backticks inside a JSON string must not be treated as fence markers.
	raw := "```json\n{\"goal\":\"deliver\",\"nodes\":[{\"id\":\"n1\",\"title\":\"code ``` fence\",\"producesSlotIds\":[\"s1\"]}],\"artifactSlots\":[{\"id\":\"s1\",\"title\":\"Slot\",\"kind\":\"text\",\"expectedCount\":1,\"required\":false}],\"inputSpecs\":[]}\n```"
	plan := mustParsePlan(t, raw)
	if plan.Nodes[0].Title != "code ``` fence" {
		t.Fatalf("node title=%q", plan.Nodes[0].Title)
	}
}

// ── Rejection cases ────────────────────────────────────────────────────────

func TestParseDefinitionPlanResponse_TwoObjectsUsesLastValid(t *testing.T) {
	first := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"first"`, 1)
	last := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"last"`, 1)
	plan := mustParsePlan(t, first+" "+last)
	if plan.Goal != "last" {
		t.Fatalf("goal=%q, want last", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ObjectThenAnotherObjectWithTextUsesLastValid(t *testing.T) {
	first := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"first"`, 1)
	last := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"last"`, 1)
	plan := mustParsePlan(t, first+"\n这里有一些说明\n"+last)
	if plan.Goal != "last" {
		t.Fatalf("goal=%q, want last", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_UsesEarlierObjectWhenLastIsInvalid(t *testing.T) {
	invalid := `{"goal":"invalid","nodes":[],"artifactSlots":[],"inputSpecs":[],"extra":true}`
	plan := mustParsePlan(t, validPlanJSON()+"\n"+invalid)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_AnalysisArrayThenValidObject(t *testing.T) {
	raw := "分析：先比较两个候选。\n" +
		`[{"candidate":"A"},{"candidate":"B"}]` +
		"\n最终答案：\n" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ArrayInsteadOfObject(t *testing.T) {
	raw := `[{"goal":"test"}]`
	mustFailParse(t, raw, "expected JSON object, got array")
}

func TestParseDefinitionPlanResponse_ArrayWithPreface(t *testing.T) {
	raw := "结果是：\n[1, 2, 3]"
	mustFailParse(t, raw, "expected JSON object, got array")
}

func TestParseDefinitionPlanResponse_TruncatedJSON(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[`
	mustFailParse(t, raw, "unterminated JSON object")
}

func TestParseDefinitionPlanResponse_TruncatedInString(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[{"id":"n1","title":"unclosed string`
	mustFailParse(t, raw, "unterminated JSON object")
}

func TestParseDefinitionPlanResponse_InvalidUTF8(t *testing.T) {
	raw := "好的\xfe\xfe" + validPlanJSON()
	mustFailParse(t, raw, "invalid UTF-8")
}

func TestParseDefinitionPlanResponse_NoJSON(t *testing.T) {
	raw := "这是一段纯中文说明，没有任何JSON对象。"
	mustFailParse(t, raw, "no JSON object found")
}

func TestParseDefinitionPlanResponse_UnknownField(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":[],"inputSpecs":[],"extraField":"should be rejected"}`
	mustFailParse(t, raw, "unknown field")
}

func TestParseDefinitionPlanResponse_UnknownFieldInNode(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[{"id":"n1","title":"Node","producesSlotIds":["s1"],"unknownNodeField":true}],"artifactSlots":[{"id":"s1","title":"Slot","kind":"text","expectedCount":1,"required":false}],"inputSpecs":[]}`
	mustFailParse(t, raw, "unknown field")
}

func TestParseDefinitionPlanResponse_WrongType_GoalIsNumber(t *testing.T) {
	raw := `{"goal":123,"nodes":[],"artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "type error")
}

func TestParseDefinitionPlanResponse_WrongType_NodesIsString(t *testing.T) {
	raw := `{"goal":"deliver","nodes":"not-an-array","artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "must be a JSON array")
}

func TestParseDefinitionPlanResponse_WrongType_InputSpecsIsNumber(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":[],"inputSpecs":42}`
	mustFailParse(t, raw, "must be a JSON array")
}

// ── Safety: error messages must not leak raw response content ──────────────

func TestParseDefinitionPlanResponse_ErrorDoesNotLeakRawChinese(t *testing.T) {
	raw := "这是一段中文前言，包含敏感信息：密钥=abc123\n" + `{"goal":"test","nodes":[],"artifactSlots":[],"inputSpecs":[]}`
	plan, err := parsePlan(raw)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "这是一段中文前言") {
			t.Fatalf("error message leaks raw Chinese preface: %q", errStr)
		}
		if strings.Contains(errStr, "密钥") {
			t.Fatalf("error message leaks sensitive content: %q", errStr)
		}
	}
	if plan == nil {
		t.Fatal("expected plan to be parsed successfully")
	}
}

func TestParseDefinitionPlanResponse_JSONErrorDoesNotLeakRawContent(t *testing.T) {
	// When JSON is valid but has wrong types, error must not include the full raw text.
	raw := `{"goal":"secret-project-name","nodes":"bad-type","artifactSlots":[],"inputSpecs":[]}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("expected error")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "secret-project-name") {
		t.Fatalf("error message leaks JSON value content: %q", errStr)
	}
}

// ── Stream behavior tests ──────────────────────────────────────────────────

func TestBootDefinitionPlannerUsesProviderForFullStructure(t *testing.T) {
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkDone},
	}}
	planner := newBootDefinitionPlanner(prov, 0.2, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "split collection and review",
		Base: &work.WorkDefinitionRevision{
			WorkID: "work-1", Revision: 1, Goal: "base",
			Nodes: []work.NodeDef{{ID: "base", Title: "Base"}},
		},
	})
	if err != nil || plan == nil || len(plan.Nodes) != 2 || len(plan.ArtifactSlots) != 2 ||
		len(plan.InputSpecs) != 1 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	if prov.calls != 1 || len(prov.last.Messages) != 2 ||
		!strings.Contains(prov.last.Messages[1].Content, "split collection and review") ||
		!strings.Contains(prov.last.Messages[1].Content, `"workId":"work-1"`) {
		t.Fatalf("provider request=%+v calls=%d", prov.last, prov.calls)
	}
}

func TestBootDefinitionPlannerProviderFailureIsExplicit(t *testing.T) {
	prov := &definitionPlannerProviderStub{err: errors.New("provider unavailable")}
	_, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err == nil || !strings.Contains(err.Error(), "provider unavailable") {
		t.Fatalf("err=%v", err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls=%d, want 1 (Stream error must not trigger repair)", prov.calls)
	}
}

func TestBootDefinitionPlanner_ChunkError(t *testing.T) {
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkError, Err: fmt.Errorf("model overloaded")},
	}}
	_, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err=%v, want model overloaded", err)
	}
	if prov.calls != 1 {
		t.Fatalf("provider calls=%d, want 1 (ChunkError must not trigger repair)", prov.calls)
	}
}

func TestBootDefinitionPlanner_NoChunkDone(t *testing.T) {
	// Stream closes without ChunkDone — must still parse accumulated text.
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
	}}
	plan, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err != nil || plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestBootDefinitionPlanner_DoubleChunkDone(t *testing.T) {
	// Two ChunkDone signals — first one triggers parse, second never reached.
	// The stream consumer stops after the first Done return.
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkDone},
		{Type: provider.ChunkDone},
	}}
	plan, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err != nil || plan == nil || len(plan.Nodes) != 2 {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestBootDefinitionPlanner_ChinesePrefaceWithChunks(t *testing.T) {
	// Chunked delivery of Chinese preface + JSON.
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "好的，以下是结构规划方案：\n"},
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkText, Text: "\n希望对您有帮助！"},
		{Type: provider.ChunkDone},
	}}
	plan, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err != nil || plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
}

func TestBootDefinitionPlanner_ErrorDoesNotLeakRawChineseInStream(t *testing.T) {
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: "包含用户敏感数据的说明：项目代号凤凰\n"},
		{Type: provider.ChunkText, Text: `{"goal":"test"`},
		{Type: provider.ChunkDone},
	}}
	_, err := newBootDefinitionPlanner(prov, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if err == nil {
		t.Fatal("expected error for truncated JSON")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "凤凰") {
		t.Fatalf("error message leaks raw model output: %q", errStr)
	}
	if strings.Contains(errStr, "项目代号") {
		t.Fatalf("error message leaks sensitive content: %q", errStr)
	}
}

// ── Nil planner guard ──────────────────────────────────────────────────────

func TestBootDefinitionPlanner_NilProvider(t *testing.T) {
	_, err := newBootDefinitionPlanner(nil, 0, 0, nil).PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if !errors.Is(err, work.ErrDefinitionPlannerUnavailable) {
		t.Fatalf("err=%v, want ErrDefinitionPlannerUnavailable", err)
	}
}

func TestBootDefinitionPlanner_NilBootPlanner(t *testing.T) {
	var p *bootDefinitionPlanner
	_, err := p.PlanDefinition(
		context.Background(),
		work.DefinitionPlanInput{Intent: "change", Base: &work.WorkDefinitionRevision{}},
	)
	if !errors.Is(err, work.ErrDefinitionPlannerUnavailable) {
		t.Fatalf("err=%v, want ErrDefinitionPlannerUnavailable", err)
	}
}

// ── Empty but valid collections (no null) ───────────────────────────────────

func TestParseDefinitionPlanResponse_EmptyNodesAndSlots(t *testing.T) {
	raw := `{"goal":"empty-plan","nodes":[],"artifactSlots":[],"inputSpecs":[]}`
	plan := mustParsePlan(t, raw)
	if plan.Goal != "empty-plan" || len(plan.Nodes) != 0 || len(plan.ArtifactSlots) != 0 || len(plan.InputSpecs) != 0 {
		t.Fatalf("plan=%+v", plan)
	}
}

func TestParseDefinitionPlanResponse_DisallowUnknownFieldsPreventsSilentDrift(t *testing.T) {
	// Extra key at top level that looks plausible but is not in the contract.
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":[],"inputSpecs":[],"version":2}`
	mustFailParse(t, raw, "unknown field")
}

// ── Strict wire: null / missing / non-array rejections ─────────────────────

func TestParseDefinitionPlanResponse_NodesNull(t *testing.T) {
	raw := `{"goal":"deliver","nodes":null,"artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "nodes must not be null")
}

func TestParseDefinitionPlanResponse_ArtifactSlotsNull(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":null,"inputSpecs":[]}`
	mustFailParse(t, raw, "artifactSlots must not be null")
}

func TestParseDefinitionPlanResponse_InputSpecsNull(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":[],"inputSpecs":null}`
	mustFailParse(t, raw, "inputSpecs must not be null")
}

func TestParseDefinitionPlanResponse_GoalNull(t *testing.T) {
	raw := `{"goal":null,"nodes":[],"artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "goal must not be null")
}

func TestParseDefinitionPlanResponse_MissingGoal(t *testing.T) {
	raw := `{"nodes":[],"artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "missing required field: goal")
}

func TestParseDefinitionPlanResponse_MissingNodes(t *testing.T) {
	raw := `{"goal":"test","artifactSlots":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "missing required field: nodes")
}

func TestParseDefinitionPlanResponse_MissingArtifactSlots(t *testing.T) {
	raw := `{"goal":"test","nodes":[],"inputSpecs":[]}`
	mustFailParse(t, raw, "missing required field: artifactSlots")
}

func TestParseDefinitionPlanResponse_MissingInputSpecs(t *testing.T) {
	raw := `{"goal":"test","nodes":[],"artifactSlots":[]}`
	mustFailParse(t, raw, "missing required field: inputSpecs")
}

// ── Natural-language bracket tolerance ──────────────────────────────────────

func TestParseDefinitionPlanResponse_BracketInPrefaceAccepted(t *testing.T) {
	// [draft] in natural language must not be mistaken for a JSON array start.
	raw := "状态 [草稿]：\n" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_MultipleBracketsInPreface(t *testing.T) {
	raw := "[规划中] [待审核]：\n" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_BracketInTrailingTextOk(t *testing.T) {
	raw := validPlanJSON() + "\n\n备注 [仅供参考]"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_PureArrayStillRejected(t *testing.T) {
	raw := `[{"goal":"nested"}]`
	mustFailParse(t, raw, "expected JSON object, got array")
}

func TestParseDefinitionPlanResponse_PureArrayWithPrefaceStillRejected(t *testing.T) {
	raw := "结果是：\n[1, 2, 3]"
	mustFailParse(t, raw, "expected JSON object, got array")
}

func TestParseDefinitionPlanResponse_ObjectThenArrayUsesValidObject(t *testing.T) {
	raw := validPlanJSON() + "\n[1,2]"
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ArrayThenObjectUsesValidObject(t *testing.T) {
	raw := "[1,2]\n" + validPlanJSON()
	plan := mustParsePlan(t, raw)
	if plan.Goal != "deliver" {
		t.Fatalf("goal=%q, want deliver", plan.Goal)
	}
}

func TestParseDefinitionPlanResponse_ArrayWrappingObjectRejected(t *testing.T) {
	// [{...}] — an array containing an object — must be rejected, not
	// mistaken for a bare object.
	raw := `[` + validPlanJSON() + `]`
	mustFailParse(t, raw, "expected JSON object, got array")
}

// ── Error safety: unknown field names / sensitive content in errors ────────

func TestParseDefinitionPlanResponse_UnknownFieldNameIsSuppressed(t *testing.T) {
	raw := `{"goal":"deliver","nodes":[],"artifactSlots":[],"inputSpecs":[],"secretProject":"凤凰计划-绝密"}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	errStr := err.Error()
	// Must not leak the unknown field name.
	if strings.Contains(errStr, "secretProject") {
		t.Fatalf("error leaks unknown field name: %q", errStr)
	}
	// Must not leak the field value.
	if strings.Contains(errStr, "凤凰") || strings.Contains(errStr, "绝密") {
		t.Fatalf("error leaks field value content: %q", errStr)
	}
	// Must contain a safe category.
	if !strings.Contains(errStr, "unknown field") {
		t.Fatalf("error missing safe category: %q", errStr)
	}
}

func TestParseDefinitionPlanResponse_SyntaxErrorDoesNotLeakSensitiveValue(t *testing.T) {
	raw := `{"goal":"凤凰计划-绝密",}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("expected syntax error")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "凤凰") || strings.Contains(errStr, "绝密") {
		t.Fatalf("syntax error leaks sensitive content: %q", errStr)
	}
	if !strings.Contains(errStr, "syntax error") {
		t.Fatalf("error missing safe category: %q", errStr)
	}
}

func TestParseDefinitionPlanResponse_TypeErrorDoesNotLeakValue(t *testing.T) {
	raw := `{"goal":"凤凰计划-绝密","nodes":"凤凰计划-绝密","artifactSlots":[],"inputSpecs":[]}`
	_, err := parsePlan(raw)
	if err == nil {
		t.Fatal("expected type error")
	}
	errStr := err.Error()
	if strings.Contains(errStr, "凤凰") || strings.Contains(errStr, "绝密") {
		t.Fatalf("type error leaks sensitive value: %q", errStr)
	}
}

// ── Real-chain integration: FileWorkStore + Service + bootDefinitionPlanner ─

// variableProvider produces configurable output chunks; it can be changed
// between calls so the same planner instance can simulate parse failure then
// success.
type variableProvider struct {
	chunks atomic.Pointer[[]provider.Chunk]
	calls  atomic.Int32
}

func (p *variableProvider) Name() string { return "variable-planner-provider" }

func (p *variableProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	ptr := p.chunks.Load()
	if ptr == nil {
		ch := make(chan provider.Chunk)
		close(ch)
		return ch, nil
	}
	ch := make(chan provider.Chunk, len(*ptr))
	for _, chunk := range *ptr {
		ch <- chunk
	}
	close(ch)
	return ch, nil
}

func (p *variableProvider) set(chunks []provider.Chunk) {
	p.chunks.Store(&chunks)
}

func TestBootIntegration_ParseFailureNoWriteThenRetrySuccessThenRestartReplay_FileWorkStore(t *testing.T) {
	// 1. Set up a real FileWorkStore + Service with an active definition.
	dir := t.TempDir()
	store, err := work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, nil, nil)

	view, err := svc.BeginWorkPlanning(context.Background(), work.BeginWorkPlanningInput{
		SessionID: "session-" + t.Name(),
		RequestID: "begin-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	baseDef := &work.WorkDefinitionRevision{
		Goal:          "base",
		Nodes:         []work.NodeDef{{ID: "n1", Title: "Base"}},
		ArtifactSlots: []work.ArtifactSlotDef{{ID: "slot", Title: "Slot", Kind: "text", ExpectedCount: 1, Required: true}},
		CreatedBy:     "test",
	}
	candidate, err := svc.CreateCandidateRevision(
		context.Background(), workID, baseDef, "candidate-"+t.Name(), view.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID:           workID,
		Revision:         candidate.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        "apply-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = applied

	// 2. Configure a planner with a variable provider.
	prov := &variableProvider{}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	svc.SetV2DefinitionPlanner(planner)

	// ── Snapshot before first attempt ──────────────────────────────────────
	beforeWork, beforeState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := beforeState.Revision
	beforeCurrentRev := beforeWork.V2CurrentRevision
	beforeLatestRev := beforeWork.V2LatestRevision
	beforeDefs := listDefinitionFiles(t, store, workID)

	requestID := "integration-" + t.Name()
	input := work.CreateCandidateRevisionInput{
		WorkID:                 workID,
		Intent:                 "split the base node into two nodes",
		BaseDefinitionRevision: candidate.Revision,
		ExpectedRevision:       beforeState.Revision,
		RequestID:              requestID,
	}

	// 3. First attempt: provider returns parse-failing output.
	prov.set([]provider.Chunk{
		{Type: provider.ChunkText, Text: "好的，以下是结构规划方案：\n好的，以下"},
		{Type: provider.ChunkDone},
	})
	result1, err1 := svc.CreateCandidateRevisionWithResult(context.Background(), input)
	if err1 == nil {
		t.Fatal("first attempt with parse-failing output must return error")
	}
	if result1 == nil || result1.Committed {
		t.Fatalf("parse failure must not commit: result=%+v", result1)
	}
	if !result1.Recoverable {
		t.Fatalf("parse failure must be recoverable: result=%+v", result1)
	}
	if result1.TransportError == nil || result1.TransportError.Code != "planner_failed" {
		t.Fatalf("parse failure TransportError code: got=%v want=planner_failed", result1.TransportError)
	}

	// ── Assert NO state mutation after parse failure ───────────────────────
	afterFailWork, afterFailState, loadErr := store.LoadState(workID, "")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if afterFailState.Revision != beforeRevision {
		t.Fatalf("parse failure changed state.Revision: before=%d after=%d", beforeRevision, afterFailState.Revision)
	}
	if afterFailWork.V2CurrentRevision != beforeCurrentRev {
		t.Fatalf("parse failure changed V2CurrentRevision: before=%d after=%d", beforeCurrentRev, afterFailWork.V2CurrentRevision)
	}
	if afterFailWork.V2LatestRevision != beforeLatestRev {
		t.Fatalf("parse failure changed V2LatestRevision: before=%d after=%d", beforeLatestRev, afterFailWork.V2LatestRevision)
	}
	_, candidateState, _ := store.LoadState(workID, requestID+"/candidate")
	if candidateState.RequestFound {
		t.Fatalf("parse failure left candidate event: state=%+v", candidateState)
	}
	receipt, _ := store.LoadV2Receipt(workID, requestID)
	if receipt != nil {
		t.Fatalf("parse failure wrote receipt: %+v", receipt)
	}
	afterFailDefs := listDefinitionFiles(t, store, workID)
	if !defFilesEqual(beforeDefs, afterFailDefs) {
		t.Fatalf("parse failure changed definitions: before=%v after=%v", beforeDefs, afterFailDefs)
	}
	// Next revision body must not exist.
	nextRev := beforeLatestRev + 1
	if _, revErr := store.LoadRevision(workID, nextRev); revErr == nil {
		t.Fatalf("parse failure wrote revision body at %d", nextRev)
	}

	// Verify model was called exactly 3 times (all repair attempts exhausted).
	if calls := prov.calls.Load(); calls != 3 {
		t.Fatalf("model calls after first attempt=%d, want 3", calls)
	}

	// 4. Second attempt with same requestId: provider returns valid JSON.
	prov.set([]provider.Chunk{
		{Type: provider.ChunkText, Text: validPlanJSON()},
		{Type: provider.ChunkDone},
	})
	beforeRetryWork, beforeRetryState, _ := store.LoadState(workID, "")
	_ = beforeRetryWork
	beforeRetryDefs := listDefinitionFiles(t, store, workID)

	result2, err2 := svc.CreateCandidateRevisionWithResult(context.Background(), input)
	if err2 != nil {
		t.Fatalf("retry with valid output failed: %v", err2)
	}
	if result2 == nil || !result2.Committed || result2.Candidate == nil {
		t.Fatalf("retry must commit one candidate: result=%+v", result2)
	}
	if result2.Duplicate {
		t.Fatalf("retry must be a fresh candidate, not duplicate")
	}
	if calls := prov.calls.Load(); calls != 4 {
		t.Fatalf("model calls after retry=%d, want 4", calls)
	}

	// ── Assert exactly one candidate was committed ─────────────────────────
	candidateRev := result2.Candidate.Revision
	if candidateRev != beforeLatestRev+1 {
		t.Fatalf("candidate revision=%d, want latest+1=%d", candidateRev, beforeLatestRev+1)
	}
	afterRetryWork, afterRetryState, _ := store.LoadState(workID, "")
	if afterRetryState.Revision != beforeRetryState.Revision+1 {
		t.Fatalf("event revision must advance by exactly 1 (single commit): before=%d after=%d", beforeRetryState.Revision, afterRetryState.Revision)
	}
	// V2LatestRevision must now point to the new candidate.
	if afterRetryWork.V2LatestRevision != candidateRev {
		t.Fatalf("V2LatestRevision=%d, want candidate=%d", afterRetryWork.V2LatestRevision, candidateRev)
	}
	// V2CurrentRevision must NOT have changed (candidate, not applied).
	if afterRetryWork.V2CurrentRevision != beforeCurrentRev {
		t.Fatalf("candidate changed V2CurrentRevision: before=%d after=%d", beforeCurrentRev, afterRetryWork.V2CurrentRevision)
	}
	// Definitions directory: exactly one new body file.
	afterRetryDefs := listDefinitionFiles(t, store, workID)
	if len(afterRetryDefs) != len(beforeRetryDefs)+1 {
		t.Fatalf("definitions count: before=%d after=%d, want exactly 1 new body", len(beforeRetryDefs), len(afterRetryDefs))
	}
	added := setDifference(afterRetryDefs, beforeRetryDefs)
	if len(added) != 1 {
		t.Fatalf("definitions added=%v, want exactly 1 new body", added)
	}
	if !isSubset(beforeRetryDefs, afterRetryDefs) {
		t.Fatalf("original definitions not preserved: before=%v after=%v", beforeRetryDefs, afterRetryDefs)
	}
	body, bodyErr := store.LoadRevision(workID, candidateRev)
	if bodyErr != nil || body == nil {
		t.Fatalf("committed candidate body not loadable at %d: err=%v", candidateRev, bodyErr)
	}
	// Receipt must exist with no error.
	receipt2, receiptErr := store.LoadV2Receipt(workID, requestID)
	if receiptErr != nil {
		t.Fatalf("LoadV2Receipt error after commit: %v", receiptErr)
	}
	if receipt2 == nil || receipt2.ResultRevision != candidateRev {
		t.Fatalf("receipt missing or wrong revision: receipt=%+v", receipt2)
	}

	// 5. Reopen FileWorkStore + Service, record snapshot, replay same requestId.
	reopenedStore, err := work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSvc := work.NewService(reopenedStore, nil, nil)
	// Snapshot before replay.
	_, beforeReplayState, _ := reopenedStore.LoadState(workID, "")
	beforeReplayDefs := listDefinitionFiles(t, reopenedStore, workID)

	// Deliberately do NOT configure a planner — replay must not need one.
	replayResult, replayErr := reopenedSvc.CreateCandidateRevisionWithResult(context.Background(), input)
	if replayErr != nil {
		t.Fatalf("replay without planner failed: %v", replayErr)
	}
	if replayResult == nil || !replayResult.Committed || !replayResult.Duplicate {
		t.Fatalf("replay must be duplicate and committed: result=%+v", replayResult)
	}
	if replayResult.Candidate == nil || replayResult.Candidate.Revision != candidateRev {
		t.Fatalf("replay candidate revision mismatch: got=%d want=%d",
			replayResult.Candidate.Revision, candidateRev)
	}

	// ── Assert replay is pure replay: zero new state/definitions ───────────
	_, afterReplayState, _ := reopenedStore.LoadState(workID, "")
	if afterReplayState.Revision != beforeReplayState.Revision {
		t.Fatalf("replay changed event revision: before=%d after=%d", beforeReplayState.Revision, afterReplayState.Revision)
	}
	afterReplayDefs := listDefinitionFiles(t, reopenedStore, workID)
	if !defFilesEqual(beforeReplayDefs, afterReplayDefs) {
		t.Fatalf("replay changed definitions: before=%v after=%v", beforeReplayDefs, afterReplayDefs)
	}
	// Model must NOT have been called again.
	if calls := prov.calls.Load(); calls != 4 {
		t.Fatalf("replay called model: calls=%d, want still 4", calls)
	}
	// Latest definition must still be the one candidate (no second body).
	latest, latErr := reopenedStore.LoadLatestRevision(workID)
	if latErr != nil || latest == nil || latest.Revision != candidateRev {
		t.Fatalf("latest revision after replay: rev=%v err=%v, want candidate=%d", latest, latErr, candidateRev)
	}
}

func TestBootIntegration_RepairSuccessWithinOnePlanDefinition_SingleWrite_FileWorkStore(t *testing.T) {
	// Verifies: repair within one PlanDefinition call produces exactly one
	// committed candidate (one event, one definition body, one receipt).  After
	// restart, replaying the same requestId returns Duplicate without any model
	// calls.

	// 1. Set up FileWorkStore + Service with active definition.
	dir := t.TempDir()
	store, err := work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, nil, nil)

	view, err := svc.BeginWorkPlanning(context.Background(), work.BeginWorkPlanningInput{
		SessionID: "session-" + t.Name(),
		RequestID: "begin-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := view.Work.ID
	baseDef := &work.WorkDefinitionRevision{
		Goal:          "base",
		Nodes:         []work.NodeDef{{ID: "n1", Title: "Base"}},
		ArtifactSlots: []work.ArtifactSlotDef{{ID: "slot", Title: "Slot", Kind: "text", ExpectedCount: 1, Required: true}},
		CreatedBy:     "test",
	}
	candidate, err := svc.CreateCandidateRevision(
		context.Background(), workID, baseDef, "candidate-"+t.Name(), view.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID:           workID,
		Revision:         candidate.Revision,
		ExpectedRevision: state.Revision,
		RequestID:        "apply-" + t.Name(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = applied

	// 2. Configure planner with sequenceProvider: call 1 fails (array), call 2 succeeds.
	bad := `[{"goal":"test","nodes":[],"artifactSlots":[],"inputSpecs":[]}]`
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	svc.SetV2DefinitionPlanner(planner)

	// ── Snapshot before call ───────────────────────────────────────────────
	beforeWork, beforeState, err := store.LoadState(workID, "")
	if err != nil {
		t.Fatal(err)
	}
	beforeRevision := beforeState.Revision
	beforeCurrentRev := beforeWork.V2CurrentRevision
	beforeLatestRev := beforeWork.V2LatestRevision
	beforeDefs := listDefinitionFiles(t, store, workID)

	requestID := "repair-integration-" + t.Name()
	input := work.CreateCandidateRevisionInput{
		WorkID:                 workID,
		Intent:                 "split the base node into two nodes",
		BaseDefinitionRevision: candidate.Revision,
		ExpectedRevision:       beforeState.Revision,
		RequestID:              requestID,
	}

	// 3. Single call: planner repairs internally (2 provider calls) → success.
	result, err := svc.CreateCandidateRevisionWithResult(context.Background(), input)
	if err != nil {
		t.Fatalf("repair within one PlanDefinition must succeed: %v", err)
	}
	if result == nil || !result.Committed || result.Candidate == nil {
		t.Fatalf("repair must commit one candidate: result=%+v", result)
	}
	if result.Duplicate {
		t.Fatalf("repair must be a fresh candidate, not duplicate")
	}

	// ── Provider calls: exactly 2 (1 fail + 1 repair success) ──────────────
	if calls := prov.calls.Load(); calls != 2 {
		t.Fatalf("provider calls=%d, want 2", calls)
	}

	// ── Assert exactly one event committed ─────────────────────────────────
	candidateRev := result.Candidate.Revision
	if candidateRev != beforeLatestRev+1 {
		t.Fatalf("candidate revision=%d, want latest+1=%d", candidateRev, beforeLatestRev+1)
	}
	afterWork, afterState, _ := store.LoadState(workID, "")
	if afterState.Revision != beforeRevision+1 {
		t.Fatalf("event revision must advance by exactly 1: before=%d after=%d", beforeRevision, afterState.Revision)
	}
	// V2LatestRevision must point to the new candidate.
	if afterWork.V2LatestRevision != candidateRev {
		t.Fatalf("V2LatestRevision=%d, want candidate=%d", afterWork.V2LatestRevision, candidateRev)
	}
	// V2CurrentRevision must NOT have changed.
	if afterWork.V2CurrentRevision != beforeCurrentRev {
		t.Fatalf("candidate changed V2CurrentRevision: before=%d after=%d", beforeCurrentRev, afterWork.V2CurrentRevision)
	}

	// ── Assert exactly one new definition body ─────────────────────────────
	afterDefs := listDefinitionFiles(t, store, workID)
	if len(afterDefs) != len(beforeDefs)+1 {
		t.Fatalf("definitions count: before=%d after=%d, want exactly 1 new body", len(beforeDefs), len(afterDefs))
	}
	body, bodyErr := store.LoadRevision(workID, candidateRev)
	if bodyErr != nil || body == nil {
		t.Fatalf("committed candidate body not loadable at %d: err=%v", candidateRev, bodyErr)
	}
	// Receipt must exist.
	receipt, receiptErr := store.LoadV2Receipt(workID, requestID)
	if receiptErr != nil {
		t.Fatalf("LoadV2Receipt error after commit: %v", receiptErr)
	}
	if receipt == nil || receipt.ResultRevision != candidateRev {
		t.Fatalf("receipt missing or wrong revision: receipt=%+v", receipt)
	}

	// 4. Reopen FileWorkStore + Service, replay same requestId.
	reopenedStore, err := work.NewFileWorkStore(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	reopenedSvc := work.NewService(reopenedStore, nil, nil)
	_, beforeReplayState, _ := reopenedStore.LoadState(workID, "")
	beforeReplayDefs := listDefinitionFiles(t, reopenedStore, workID)

	// Deliberately do NOT configure a planner — replay must not need one.
	replayResult, replayErr := reopenedSvc.CreateCandidateRevisionWithResult(context.Background(), input)
	if replayErr != nil {
		t.Fatalf("replay without planner failed: %v", replayErr)
	}
	if replayResult == nil || !replayResult.Committed || !replayResult.Duplicate {
		t.Fatalf("replay must be duplicate and committed: result=%+v", replayResult)
	}
	if replayResult.Candidate == nil || replayResult.Candidate.Revision != candidateRev {
		t.Fatalf("replay candidate revision mismatch: got=%d want=%d",
			replayResult.Candidate.Revision, candidateRev)
	}

	// ── Assert replay is pure: zero new state/definitions ──────────────────
	_, afterReplayState, _ := reopenedStore.LoadState(workID, "")
	if afterReplayState.Revision != beforeReplayState.Revision {
		t.Fatalf("replay changed event revision: before=%d after=%d", beforeReplayState.Revision, afterReplayState.Revision)
	}
	afterReplayDefs := listDefinitionFiles(t, reopenedStore, workID)
	if !defFilesEqual(beforeReplayDefs, afterReplayDefs) {
		t.Fatalf("replay changed definitions: before=%v after=%v", beforeReplayDefs, afterReplayDefs)
	}
	// Model must NOT have been called again.
	if calls := prov.calls.Load(); calls != 2 {
		t.Fatalf("replay called model: calls=%d, want still 2", calls)
	}
	// Latest definition must still be the one candidate.
	latest, latErr := reopenedStore.LoadLatestRevision(workID)
	if latErr != nil || latest == nil || latest.Revision != candidateRev {
		t.Fatalf("latest revision after replay: rev=%v err=%v, want candidate=%d", latest, latErr, candidateRev)
	}
}

// listDefinitionFiles returns the sorted list of revision JSON filenames in the
// definitions directory for a work ID.
func listDefinitionFiles(t *testing.T, store *work.FileWorkStore, workID string) []string {
	t.Helper()
	wp := filepath.Join(store.WorkDir(), workID, "definitions")
	entries, err := os.ReadDir(wp)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("ReadDir definitions: %v", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

func defFilesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func setDifference(a, b []string) []string {
	bm := make(map[string]bool, len(b))
	for _, s := range b {
		bm[s] = true
	}
	var diff []string
	for _, s := range a {
		if !bm[s] {
			diff = append(diff, s)
		}
	}
	return diff
}

func isSubset(sub, super []string) bool {
	sm := make(map[string]bool, len(super))
	for _, s := range super {
		sm[s] = true
	}
	for _, s := range sub {
		if !sm[s] {
			return false
		}
	}
	return true
}

// ── Repair tests ────────────────────────────────────────────────────────────

// sequenceProvider returns a different chunk sequence for each call.
// When calls exceed len(sequences), it returns an empty (immediately closed)
// channel so the planner sees an empty response and fails safely.
type sequenceProvider struct {
	sequences [][]provider.Chunk
	calls     atomic.Int32
	last      atomic.Pointer[provider.Request]
}

func (p *sequenceProvider) Name() string { return "sequence-planner-provider" }

func (p *sequenceProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	idx := int(p.calls.Add(1)) - 1
	p.last.Store(&req)
	if idx >= len(p.sequences) {
		ch := make(chan provider.Chunk)
		close(ch)
		return ch, nil
	}
	chunks := p.sequences[idx]
	ch := make(chan provider.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

func (p *sequenceProvider) lastRequest() provider.Request {
	ptr := p.last.Load()
	if ptr == nil {
		return provider.Request{}
	}
	return *ptr
}

func chunkT(s string) provider.Chunk { return provider.Chunk{Type: provider.ChunkText, Text: s} }

var chunkD = provider.Chunk{Type: provider.ChunkDone}

func chunkErr(e error) provider.Chunk { return provider.Chunk{Type: provider.ChunkError, Err: e} }

func TestRepair_ArrayThenSuccess(t *testing.T) {
	// Call 1: model returns an array — parse fails. Call 2: model returns valid JSON — success.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(`[{"goal":"test","nodes":[],"artifactSlots":[],"inputSpecs":[]}]`), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "add review node",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("repair should succeed: %v", err)
	}
	if plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v", plan)
	}
	if calls := prov.calls.Load(); calls != 2 {
		t.Fatalf("calls=%d, want 2 (1 fail + 1 repair success)", calls)
	}
}

func TestRepair_UnknownFieldThenSuccess(t *testing.T) {
	// Call 1: valid JSON but with unknown field "artifactSlots[].extra" causes parse failure.
	// Call 2: clean valid JSON.
	bad := `{"goal":"deliver","nodes":[{"id":"n1","title":"N1","producesSlotIds":["s1"]}],"artifactSlots":[{"id":"s1","title":"Slot","kind":"text","expectedCount":1,"required":true,"extraField":"bad"}],"inputSpecs":[]}`
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("repair should succeed: %v", err)
	}
	if plan == nil || len(plan.Nodes) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	if calls := prov.calls.Load(); calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestRepair_TwoFailuresThenSuccess(t *testing.T) {
	// Calls 1 & 2 fail, call 3 succeeds. Exactly 3 provider calls, 1 plan.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(`[{"goal":"test"}]`), chunkD},                                                    // array → fail
			{chunkT(`{"goal":"x","nodes":[],"artifactSlots":[],"inputSpecs":[],"extra":1}`), chunkD}, // unknown field → fail
			{chunkT(validPlanJSON()), chunkD},                                                        // success
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("repair should succeed on third attempt: %v", err)
	}
	if plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v", plan)
	}
	if calls := prov.calls.Load(); calls != 3 {
		t.Fatalf("calls=%d, want 3", calls)
	}
}

func TestRepair_ThreeFailuresExhausted(t *testing.T) {
	// All 3 calls fail. Exactly 3 provider calls, safe error with no raw/field leak.
	secret := "凤凰计划-绝密-abcdef123456"
	bad := `{"goal":"` + secret + `","nodes":` + secret + `,"artifactSlots":[],"inputSpecs":[]}`
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(bad), chunkD},
			{chunkT(bad), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err == nil {
		t.Fatal("expected error after 3 failures")
	}
	if calls := prov.calls.Load(); calls != 3 {
		t.Fatalf("calls=%d, want exactly 3 (max attempts)", calls)
	}
	// Error must be safe: no raw response content, no unknown field names.
	errStr := err.Error()
	if strings.Contains(errStr, secret) {
		t.Fatalf("error leaks raw model output: %q", errStr)
	}
	if strings.Contains(errStr, "凤凰") || strings.Contains(errStr, "绝密") {
		t.Fatalf("error leaks sensitive content: %q", errStr)
	}
}

func TestRepair_ChunkErrorNoRepair(t *testing.T) {
	// ChunkError is a provider/stream error, not a format error — zero repair.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT("好的，"), chunkErr(fmt.Errorf("model overloaded"))},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err == nil || !strings.Contains(err.Error(), "model overloaded") {
		t.Fatalf("err=%v, want model overloaded", err)
	}
	if calls := prov.calls.Load(); calls != 1 {
		t.Fatalf("calls=%d, want 1 (ChunkError must not trigger repair)", calls)
	}
}

func TestRepair_ContextCancelNoRepair(t *testing.T) {
	// Context cancellation must stop immediately — zero repair calls.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT("好的，"), chunkT("以下是"), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before any call
	_, err := planner.PlanDefinition(ctx, work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if calls := prov.calls.Load(); calls != 0 {
		t.Fatalf("calls=%d, want 0 (context cancelled before any call)", calls)
	}
}

func TestRepair_ValidChineseWrappedJSON_SingleCall(t *testing.T) {
	// Valid JSON surrounded by Chinese commentary must succeed in 1 call.
	raw := "好的，以下是结构规划方案：\n" + validPlanJSON() + "\n希望对您有帮助！"
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(raw), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("valid JSON with Chinese wrapper must succeed: %v", err)
	}
	if plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v", plan)
	}
	if calls := prov.calls.Load(); calls != 1 {
		t.Fatalf("calls=%d, want 1 (valid parse must not trigger repair)", calls)
	}
}

func TestRepair_FirstPromptContainsFullSchema(t *testing.T) {
	// The first-turn system prompt must contain the full nested schema
	// for DefinitionPlan, NodeDef, ArtifactSlotDef, InputSpec, and InputKind.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := prov.lastRequest()
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(req.Messages))
	}
	sysContent := req.Messages[0].Content
	// Must contain top-level schema markers.
	for _, want := range []string{
		"## DefinitionPlan JSON Schema",
		"### Top-level object",
		"### NodeDef",
		"### ArtifactSlotDef",
		"### InputSpec",
		"Allowed InputKind values",
		"Complete example",
		`"inputSpecIds"`,
		`"producesSlotIds"`,
		`"consumesSlotIds"`,
		`"pinEligible"`,
		`"text", "number", "date", "choice", "multi_choice", "file", "roster", "form", "approval"`,
		`"valueSchema"`,
		`"multiline": true`,
	} {
		if !strings.Contains(sysContent, want) {
			t.Fatalf("first prompt missing %q in system message:\n%s", want, sysContent)
		}
	}
}

func TestRepair_RepairPromptContainsSafeErrorAndSingleObjectInstruction(t *testing.T) {
	secret := "凤凰绝密项目代号xyz"
	bad := "RAW_ANALYSIS_MUST_BE_DROPPED\n" +
		`{"goal":"` + secret + `","nodes":[],"artifactSlots":[],"inputSpecs":[],"extra":true}`

	prov := &repairCaptureProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(prov.requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(prov.requests))
	}
	repairReq := prov.requests[1]

	if len(repairReq.Messages) != 2 {
		t.Fatalf("repair request: expected exactly 2 messages, got %d", len(repairReq.Messages))
	}
	sysContent := repairReq.Messages[0].Content
	for _, want := range []string{
		"## DefinitionPlan JSON Schema",
		"### Top-level object",
		"### NodeDef",
		"### ArtifactSlotDef",
		"### InputSpec",
		"Allowed InputKind values",
		"Complete example",
		`"inputSpecIds"`,
		`"producesSlotIds"`,
		`"consumesSlotIds"`,
		`"pinEligible"`,
		`"text", "number", "date", "choice", "multi_choice", "file", "roster", "form", "approval"`,
		`"valueSchema"`,
		`"multiline": true`,
	} {
		if !strings.Contains(sysContent, want) {
			t.Fatalf("repair system message missing %q:\n%s", want, sysContent)
		}
	}

	lastUser := repairReq.Messages[len(repairReq.Messages)-1].Content
	if !strings.Contains(lastUser, "Parse error category: unknown field") {
		t.Fatalf("repair prompt missing safe error category: %q", lastUser)
	}
	if !strings.Contains(lastUser, "Last JSON object candidate to repair:") {
		t.Fatalf("repair prompt missing object candidate: %q", lastUser)
	}
	if !strings.Contains(lastUser, secret) {
		t.Fatalf("repair prompt lost candidate semantics: %q", lastUser)
	}
	if strings.Contains(lastUser, "RAW_ANALYSIS_MUST_BE_DROPPED") {
		t.Fatalf("repair prompt retained raw analysis: %q", lastUser)
	}
	for _, message := range repairReq.Messages {
		if message.Role == provider.RoleAssistant {
			t.Fatalf("repair request must not replay raw response as assistant history")
		}
	}
	for _, want := range []string{"do NOT re-plan", "begin with { and end with }", "Do not emit analysis"} {
		if !strings.Contains(lastUser, want) {
			t.Fatalf("repair prompt missing %q: %q", want, lastUser)
		}
	}
}

func TestRepair_DropsLongRawAnalysisAndKeepsLastObjectCandidateUTF8(t *testing.T) {
	chinesePadding := strings.Repeat("模型分析内容不应进入修复上下文", 200)
	uniqueGoal := "凤凰计划候选语义"
	bad := chinesePadding + "\n" +
		`{"goal":"` + uniqueGoal + `","nodes":[],"artifactSlots":[],"inputSpecs":[],"extra":true}`

	if len(bad) <= 4096 {
		t.Fatalf("test setup: bad response must be >4096 bytes, got %d", len(bad))
	}

	prov := &repairCaptureProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(prov.requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(prov.requests))
	}
	repairReq := prov.requests[1]
	if len(repairReq.Messages) != 2 {
		t.Fatalf("repair request: expected exactly 2 messages, got %d", len(repairReq.Messages))
	}
	repair := repairReq.Messages[1].Content
	if !strings.Contains(repair, uniqueGoal) {
		t.Fatalf("repair prompt lost last object candidate: %q", repair)
	}
	if strings.Contains(repair, "模型分析内容不应进入修复上下文") {
		t.Fatalf("repair prompt retained raw analysis")
	}
	if !utf8.ValidString(repair) {
		t.Fatalf("repair prompt contains invalid UTF-8")
	}
}

func TestRepair_FirstPromptContainsSingleObjectAndNoMultiCandidate(t *testing.T) {
	// The first-turn system prompt must explicitly forbid multiple objects,
	// candidate A/B, markdown fences, commentary, and explanations.
	prov := &sequenceProvider{
		sequences: [][]provider.Chunk{
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := prov.lastRequest()
	if len(req.Messages) < 2 {
		t.Fatalf("expected at least 2 messages, got %d", len(req.Messages))
	}
	sysContent := req.Messages[0].Content

	// Internal validation instruction.
	if !strings.Contains(sysContent, "Internally validate") {
		t.Fatalf("first prompt missing internal validation instruction:\n%s", sysContent)
	}
	// Single object constraint.
	if !strings.Contains(sysContent, "exactly ONE top-level JSON object") {
		t.Fatalf("first prompt missing single-object constraint:\n%s", sysContent)
	}
	// No candidate A/B.
	if !strings.Contains(sysContent, "never give candidate A/B") {
		t.Fatalf("first prompt missing no-candidate-A/B constraint:\n%s", sysContent)
	}
	// No second object.
	if !strings.Contains(sysContent, "never give a second object") {
		t.Fatalf("first prompt missing no-second-object constraint:\n%s", sysContent)
	}
	// No markdown / no commentary / no explanations.
	if !strings.Contains(sysContent, "no markdown, no code fences, no commentary") {
		t.Fatalf("first prompt missing no-markdown/commentary constraint:\n%s", sysContent)
	}
	for _, want := range []string{
		"Think silently",
		"first non-whitespace character of your response must be {",
		"last must be }",
		"Do not quote, restate, or discuss the input JSON",
	} {
		if !strings.Contains(sysContent, want) {
			t.Fatalf("first prompt missing %q:\n%s", want, sysContent)
		}
	}
	userContent := req.Messages[len(req.Messages)-1].Content
	if !strings.HasSuffix(userContent, definitionPlanOutputReminder) {
		t.Fatalf("first user message must end with the output reminder:\n%s", userContent)
	}
	for _, want := range []string{"Validate silently", "Do not emit analysis", "a second JSON value"} {
		if !strings.Contains(userContent, want) {
			t.Fatalf("first user message missing %q:\n%s", want, userContent)
		}
	}
	// Full schema still present.
	for _, want := range []string{
		"## DefinitionPlan JSON Schema",
		"### Top-level object",
		"### NodeDef",
		"### ArtifactSlotDef",
		"### InputSpec",
		"Allowed InputKind values",
		"Complete example",
		`"inputSpecIds"`,
	} {
		if !strings.Contains(sysContent, want) {
			t.Fatalf("first prompt missing schema marker %q", want)
		}
	}
}

func TestPlanDefinition_MultipleValidObjectsUsesLastWithoutRepair(t *testing.T) {
	first := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"first"`, 1)
	last := strings.Replace(validPlanJSON(), `"goal":"deliver"`, `"goal":"last"`, 1)
	raw1 := first + "\n" + last

	prov := &repairCaptureProvider{
		sequences: [][]provider.Chunk{
			{chunkT(raw1), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("parse should succeed: %v", err)
	}
	if plan == nil || plan.Goal != "last" {
		t.Fatalf("plan=%+v", plan)
	}
	if len(prov.requests) != 1 {
		t.Fatalf("expected one request without repair, got %d", len(prov.requests))
	}
}

func TestRepair_ConsecutiveRepairsUseMostRecentDraft(t *testing.T) {
	// Call 1 fails with unknown field → repair includes its last object.
	// Call 2 fails with array error → repair reports that no object exists.
	// Call 3 succeeds.
	bad1 := `{"goal":"x","nodes":[],"artifactSlots":[],"inputSpecs":[],"secretField":"bad"}`
	bad2 := `[{"goal":"x","nodes":[],"artifactSlots":[],"inputSpecs":[]}]`

	prov := &repairCaptureProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad1), chunkD},
			{chunkT(bad2), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	plan, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatalf("repair should succeed on third attempt: %v", err)
	}
	if plan == nil || plan.Goal != "deliver" {
		t.Fatalf("plan=%+v", plan)
	}

	if len(prov.requests) != 3 {
		t.Fatalf("expected 3 requests, got %d", len(prov.requests))
	}

	repair1 := prov.requests[1]
	if len(repair1.Messages) != 2 {
		t.Fatalf("repair1: expected 2 messages, got %d", len(repair1.Messages))
	}
	lastUser1 := repair1.Messages[1].Content
	if !strings.Contains(lastUser1, "secretField") {
		t.Fatalf("repair1 missing bad1 candidate: %q", lastUser1)
	}
	if !strings.Contains(lastUser1, "unknown field") {
		t.Fatalf("repair1 error category wrong: %q", lastUser1)
	}

	repair2 := prov.requests[2]
	if len(repair2.Messages) != 2 {
		t.Fatalf("repair2: expected 2 messages, got %d", len(repair2.Messages))
	}
	lastUser2 := repair2.Messages[1].Content
	if !strings.Contains(lastUser2, "No complete JSON object candidate was found") {
		t.Fatalf("repair2 missing no-object notice: %q", lastUser2)
	}
	if strings.Contains(lastUser2, "secretField") || strings.Contains(lastUser2, `[{"goal":"x"`) {
		t.Fatalf("repair2 retained prior raw output: %q", lastUser2)
	}
	if !strings.Contains(lastUser2, "expected JSON object, got array") {
		t.Fatalf("repair2 error category wrong: %q", lastUser2)
	}
	for i, req := range prov.requests[1:] {
		for _, message := range req.Messages {
			if message.Role == provider.RoleAssistant {
				t.Fatalf("repair request %d contains assistant history", i+1)
			}
		}
	}
}

func TestRepair_LongRawSendsOnlyLastObjectCandidate(t *testing.T) {
	chineseHead := strings.Repeat("凤凰计划原始分析不应回灌", 180)
	uniqueTail := `UNIQUE_TAIL_SURVIVES_9876543210`
	bad := chineseHead + "\n" +
		`{"goal":"` + uniqueTail + `","nodes":[],"artifactSlots":[],"inputSpecs":[],"extra":true}`

	if len(bad) <= 4096 {
		t.Fatalf("test setup: bad response must be >4096 bytes, got %d", len(bad))
	}

	prov := &repairCaptureProvider{
		sequences: [][]provider.Chunk{
			{chunkT(bad), chunkD},
			{chunkT(validPlanJSON()), chunkD},
		},
	}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(context.Background(), work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(prov.requests) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(prov.requests))
	}
	repairReq := prov.requests[1]
	if len(repairReq.Messages) != 2 {
		t.Fatalf("repair request: expected 2 messages, got %d", len(repairReq.Messages))
	}
	repair := repairReq.Messages[1].Content
	if strings.Contains(repair, "凤凰计划原始分析不应回灌") {
		t.Fatalf("repair prompt retained long raw analysis")
	}
	if !strings.Contains(repair, uniqueTail) {
		t.Fatalf("repair prompt missing last object candidate: %q", repair)
	}
	if strings.Contains(repair, "... [middle truncated] ...") {
		t.Fatalf("repair prompt should not contain raw-response truncation marker")
	}
	if !utf8.ValidString(repair) {
		t.Fatalf("repair prompt contains invalid UTF-8")
	}
}

func TestTruncateRawResponseHonorsSmallBudget(t *testing.T) {
	raw := strings.Repeat("凤凰", 100)
	for _, maxBytes := range []int{-1, 0, 1, 8, 32, 128, 300} {
		got := truncateRawResponse(raw, maxBytes)
		if maxBytes <= 0 {
			if got != "" {
				t.Fatalf("maxBytes=%d: got %q, want empty", maxBytes, got)
			}
			continue
		}
		if len(got) > maxBytes {
			t.Fatalf("maxBytes=%d: length=%d", maxBytes, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("maxBytes=%d: invalid UTF-8", maxBytes)
		}
	}
}

func TestRepair_ContextCancelAfterFirstParseFailureStopsImmediately(t *testing.T) {
	// After first parse failure and context is cancelled, the loop must NOT
	// make a second provider call.
	ctx, cancel := context.WithCancel(context.Background())
	prov := &cancelAfterFirstProvider{cancel: cancel}
	planner := newBootDefinitionPlanner(prov, 0, 2048, nil)
	_, err := planner.PlanDefinition(ctx, work.DefinitionPlanInput{
		Intent: "restructure",
		Base:   &work.WorkDefinitionRevision{WorkID: "w1", Revision: 1, Goal: "base"},
	})
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if calls := prov.calls.Load(); calls != 1 {
		t.Fatalf("provider calls=%d, want exactly 1 (cancel must prevent second call)", calls)
	}
}

// cancelAfterFirstProvider cancels the context after the first Stream call
// completes, so the repair loop detects cancellation before the second call.
type cancelAfterFirstProvider struct {
	cancel context.CancelFunc
	called atomic.Bool
	calls  atomic.Int32
}

func (p *cancelAfterFirstProvider) Name() string { return "cancel-after-first" }

func (p *cancelAfterFirstProvider) Stream(_ context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	p.calls.Add(1)
	wasCalled := p.called.Swap(true)
	ch := make(chan provider.Chunk, 2)
	ch <- chunkT(`[{"goal":"test"}]`) // bad: array
	ch <- chunkD
	close(ch)
	// Cancel synchronously on the first call so the loop sees
	// cancellation before attempting a second provider call.
	if !wasCalled && p.cancel != nil {
		p.cancel()
	}
	return ch, nil
}

// repairCaptureProvider captures every request while returning sequences.
type repairCaptureProvider struct {
	sequences [][]provider.Chunk
	calls     atomic.Int32
	requests  []provider.Request
	mu        sync.Mutex
}

func (p *repairCaptureProvider) Name() string { return "repair-capture-provider" }

func (p *repairCaptureProvider) Stream(_ context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	idx := int(p.calls.Add(1)) - 1
	p.mu.Lock()
	p.requests = append(p.requests, req)
	p.mu.Unlock()
	if idx >= len(p.sequences) {
		ch := make(chan provider.Chunk)
		close(ch)
		return ch, nil
	}
	chunks := p.sequences[idx]
	ch := make(chan provider.Chunk, len(chunks))
	for _, c := range chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}
