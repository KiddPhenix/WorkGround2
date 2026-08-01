package boot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"workground2/internal/provider"
	"workground2/internal/work"
)

func TestBootInputInferrerReturnsTypedDraftAndIncludesDetailedContext(t *testing.T) {
	prov := &definitionPlannerProviderStub{chunks: []provider.Chunk{
		{Type: provider.ChunkText, Text: `{"items":[{"inputId":"audience","value":"面向第一次接触该主题的普通读者","reason":"依据原始请求的科普目标"}],"skipped":[]}`},
		{Type: provider.ChunkDone},
	}}
	planner := newBootInputInferrer(prov, 0.1, 1024, nil)
	result, err := planner.InferInputs(context.Background(), work.InputInferencePlanInput{
		Work:       &work.Work{ID: "work-1", Locale: work.LocaleChinese, Prompt: "写一篇给大众看的详细科普文章"},
		Definition: &work.WorkDefinitionRevision{Goal: "完成科普文章", Nodes: []work.NodeDef{{ID: "write", Title: "撰写文章"}}},
		Targets: []work.InputInferenceTarget{{InputID: "audience", Spec: work.InputSpec{
			ID: "audience", Label: "目标读者是谁？", Kind: work.InputText, Required: true,
		}}},
	})
	if err != nil {
		t.Fatalf("InferInputs: %v", err)
	}
	if len(result.Items) != 1 || string(result.Items[0].Value) != `"面向第一次接触该主题的普通读者"` {
		t.Fatalf("result = %#v", result)
	}
	joined := ""
	for _, message := range prov.last.Messages {
		joined += message.Content
	}
	for _, want := range []string{"写一篇给大众看的详细科普文章", "目标读者是谁？", "Cover every target exactly once", "Use Simplified Chinese"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("prompt missing %q:\n%s", want, joined)
		}
	}
}

func TestParseInputInferenceRejectsSchemaInvalidValue(t *testing.T) {
	schema, _ := json.Marshal(map[string]any{"options": []map[string]string{{"value": "a", "label": "A"}}})
	_, err := parseInputInference(
		`{"items":[{"inputId":"choice","value":"missing","reason":"guess"}],"skipped":[]}`,
		[]work.InputInferenceTarget{{InputID: "choice", Spec: work.InputSpec{ID: "choice", Kind: work.InputChoice, Required: true, ValueSchema: schema}}},
	)
	if err == nil {
		t.Fatal("expected schema validation error")
	}
}

func TestDefinitionPlannerPromptRequiresDetailedWorkInformation(t *testing.T) {
	for _, want := range []string{
		"Enumerate every distinct indispensable user-owned decision or constraint",
		"Do not collapse audience, scope, format, deadline, budget, tone",
		"Prefer several precise typed inputs over one vague",
		"Description must be one or two concrete sentences",
	} {
		if !strings.Contains(definitionPlannerPrompt, want) {
			t.Fatalf("definition planner prompt missing %q", want)
		}
	}
}
