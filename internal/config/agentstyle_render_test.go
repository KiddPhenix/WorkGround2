package config

import (
	"strings"
	"testing"
)

func TestRenderProjectDeltaIncludesPromptStyles(t *testing.T) {
	cfg := Default()
	cfg.Agent.PromptStyles = []string{"paranoid", "obsessive_compulsive"}
	got := RenderTOMLProjectDelta(cfg)
	if !strings.Contains(got, `prompt_styles = ["paranoid", "obsessive_compulsive"]`) {
		t.Fatalf("project delta missing prompt_styles:\n%s", got)
	}
}
