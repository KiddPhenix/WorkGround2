package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"workground2/internal/skill"
)

func TestBuildReviewTask(t *testing.T) {
	// Small diff.
	diff := "diff --git a/foo.go b/foo.go\n+added line"
	got := buildReviewTask(diff, "")
	if !strings.Contains(got, "Review the following changes.") {
		t.Error("missing review prompt prefix")
	}
	if !strings.Contains(got, diff) {
		t.Errorf("diff content missing:\n%s", got)
	}

	// With extra instructions.
	got = buildReviewTask(diff, "focus on error handling")
	if !strings.Contains(got, "focus on error handling") {
		t.Error("extra instructions missing")
	}
	if !strings.Contains(got, "The diff is:") {
		t.Error("missing diff separator")
	}

	// Truncation.
	hugeDiff := strings.Repeat("x", 20000)
	got = buildReviewTask(hugeDiff, "")
	if !strings.Contains(got, "truncated at 16000") {
		t.Error("large diff should be truncated")
	}
	if len(got) > 16500 {
		t.Errorf("truncated output too long: %d", len(got))
	}
}

func TestBuildReviewSubagentRegistryUsesReadOnlyBash(t *testing.T) {
	reg := buildReviewSubagentRegistry(skill.Skill{AllowedTools: []string{
		"bash",
		"write_file",
		"wait",
		"bash_output",
		"kill_shell",
		"task",
	}})

	for _, hidden := range []string{"write_file", "wait", "bash_output", "kill_shell", "task"} {
		if _, ok := reg.Get(hidden); ok {
			t.Fatalf("review subagent registry should hide %q; got %v", hidden, reg.Names())
		}
	}
	bash, ok := reg.Get("bash")
	if !ok {
		t.Fatalf("review subagent registry should keep bash; got %v", reg.Names())
	}
	// read-only bash schema does not advertise run_in_background.
	if strings.Contains(string(bash.Schema()), "run_in_background") {
		t.Fatalf("review subagent bash schema should not include run_in_background: %s", bash.Schema())
	}
	if !bash.ReadOnly() {
		t.Fatal("review subagent bash wrapper must report ReadOnly")
	}
	// read-only bash blocks by planmode policy, not by foreground/background check.
	// The planmode policy returns a blocked message (nil error) for unsafe commands.
	out, err := bash.Execute(context.Background(), json.RawMessage(`{"command":"rm -rf /","run_in_background":true}`))
	if err != nil {
		t.Fatalf("read-only bash should return blocked message, not error: %v", err)
	}
	if !strings.HasPrefix(out, "blocked:") {
		t.Fatalf("read-only bash should return a blocked message for unsafe commands, got %q", out)
	}
}
