package boot

import (
	"strings"
	"testing"

	"workground2/internal/agentstyle"
)

func TestComposeAgentPromptStylesAppendsOnce(t *testing.T) {
	base := "base prompt"
	got, err := composeAgentPromptStyles(base, []string{"paranoid"})
	if err != nil {
		t.Fatalf("composeAgentPromptStyles: %v", err)
	}
	if !strings.HasPrefix(got, "base prompt\n\n风格: 保持高度警觉") {
		t.Fatalf("block not appended after base:\n%s", got)
	}
	if strings.Count(got, "风格: ") != 1 {
		t.Fatalf("style prefix must appear exactly once:\n%s", got)
	}
	if !strings.Contains(got, "保持高度警觉，寻找隐藏假设、利益冲突、欺诈风险和安全漏洞") {
		t.Fatalf("capability text missing:\n%s", got)
	}
	if strings.Contains(got, "所有怀疑必须给出证据") {
		t.Fatalf("fallback clause must not enter the prompt:\n%s", got)
	}
	for _, hidden := range []string{"Agent 风格", "偏执型", "风险审查者", "#"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("prompt leaked Settings-only label %q:\n%s", hidden, got)
		}
	}
}

func TestComposeAgentPromptStylesEmptySelectionIsNoop(t *testing.T) {
	got, err := composeAgentPromptStyles("base", nil)
	if err != nil {
		t.Fatalf("composeAgentPromptStyles: %v", err)
	}
	if got != "base" {
		t.Fatalf("empty selection mutated the base: %q", got)
	}
}

func TestComposeAgentPromptStylesSurfacesUnknownIDs(t *testing.T) {
	if _, err := composeAgentPromptStyles("base", []string{"bogus"}); err == nil {
		t.Fatal("expected error for unknown style ID")
	}
}

func TestAgentPromptStyleResolutionKeepsValidSubset(t *testing.T) {
	valid, unknown := agentstyle.ResolveIDs([]string{"paranoid", "bogus"})
	if len(valid) != 1 || valid[0] != "paranoid" || len(unknown) != 1 || unknown[0] != "bogus" {
		t.Fatalf("ResolveIDs = valid %v unknown %v", valid, unknown)
	}
	got, err := composeAgentPromptStyles("base", valid)
	if err != nil || !strings.Contains(got, "风格: 保持高度警觉") {
		t.Fatalf("valid subset was not compiled: got %q err %v", got, err)
	}
}
