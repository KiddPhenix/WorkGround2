package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAICollaborationPromptDescribesCodexSideWorker(t *testing.T) {
	prompt := aiCollaborationPrompt(`D:\Work\WorkGround2\workground2.exe`)

	// Present: dynamic CLI path and compact skill contract content.
	for _, want := range []string{
		`WorkGround2 CLI location: D:\Work\WorkGround2\workground2.exe`,
		"## Compact skill requirements",
		"- SKILL.md: at most 80 physical lines.",
		"- SKILL.md body: preferably 40-60 lines.",
		"- references/cli.md: at most 120 lines.",
		"Write SKILL.md with the following concise content:",
		"scripts/dispatch.ps1",
		"workground2-worker",
		"Delegate bounded implementation",
		"Codex owns planning",
		"desktop workspaces",
		"pendingInteraction",
		"outcome=interaction_required",
		"interaction_required",
		"running=false",
		"pendingPrompt=false",
		"foregroundActive=false",
		"backgroundOnly",
		"git diff --stat",
		"preferably under 1200 tokens",
		"desktop status --session <id> --json",
		"desktop answer --session <id> --id",
		"desktop approve --session <id> --id",
		"desktop submit --session",
		"desktop focus",
		"PollOnly",
		"--yolo",
		"--no-wait",
		"## dispatch.ps1 responsibilities",
		"## references/cli.md responsibilities",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}

	// Absent: old direct AGENTS workflow, PowerShell CLI Pattern, and execution-default rules.
	for _, unwanted := range []string{
		"# WorkGround2 AI Collaboration",
		"Install into Codex's default AGENTS.md",
		"remote executor",
		"actually run the CLI",
		"do not only show this snippet",
		"required workflow",
		"Execution default",
		"Codex should mainly plan and verify",
		"Before Codex edits files itself",
		"ordinary code/doc/config/test/file changes",
		"Codex must dispatch WorkGround2 first",
		"Planning rule",
		"Session rule",
		"Parallel rule",
		"Async rule",
		"Workspace rule",
		"Packet:",
		"## CLI Pattern",
		"```powershell",
		"$wg =",
		"ConvertFrom-Json",
		"Start-Sleep",
	} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("prompt should not contain %q:\n%s", unwanted, prompt)
		}
	}

	if strings.Contains(prompt, "runAs: subagent") {
		t.Fatalf("prompt should not configure WorkGround2 as a runAs subagent:\n%s", prompt)
	}

	skillStart := strings.Index(prompt, "---\nname: workground2-worker")
	skillEnd := strings.Index(prompt, "\n## dispatch.ps1 responsibilities")
	if skillStart < 0 || skillEnd <= skillStart {
		t.Fatalf("prompt should contain a bounded SKILL.md template:\n%s", prompt)
	}
	skillText := strings.TrimSpace(prompt[skillStart:skillEnd])
	if lines := strings.Count(skillText, "\n") + 1; lines > 80 {
		t.Fatalf("embedded SKILL.md template has %d physical lines, want at most 80", lines)
	}
	bodyStart := strings.Index(skillText, "\n---\n\n# WorkGround2 Worker")
	if bodyStart < 0 {
		t.Fatalf("embedded SKILL.md template is missing its body boundary:\n%s", skillText)
	}
	body := strings.TrimSpace(skillText[bodyStart+len("\n---\n\n"):])
	if lines := strings.Count(body, "\n") + 1; lines > 60 {
		t.Fatalf("embedded SKILL.md body has %d physical lines, want preferably at most 60", lines)
	}

	referenceStart := strings.Index(prompt, "## references/cli.md responsibilities")
	if referenceStart < 0 {
		t.Fatalf("prompt should contain references/cli.md responsibilities:\n%s", prompt)
	}
	referenceText := strings.TrimSpace(prompt[referenceStart:])
	if lines := strings.Count(referenceText, "\n") + 1; lines > 120 {
		t.Fatalf("embedded references/cli.md contract has %d physical lines, want at most 120", lines)
	}
}

func TestAICollaborationRuntimePromptStaysCompact(t *testing.T) {
	prompt := aiCollaborationRuntimePrompt(`D:\Work\WorkGround2\desktop\build\bin\WorkGround2.exe`)
	for _, want := range []string{
		"## WorkGround2 Worker",
		`CLI: D:\Work\WorkGround2\desktop\build\bin\WorkGround2.exe`,
		"installed `workground2-worker` skill",
		"If the skill is unavailable",
		"desktop new --workspace <root>",
		"desktop status --session <id> --json",
		"git diff --stat",
		"Background-only jobs do not block completion",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("runtime prompt missing %q:\n%s", want, prompt)
		}
	}
	if lines := strings.Count(prompt, "\n") + 1; lines > 12 {
		t.Fatalf("runtime prompt has %d lines, want at most 12:\n%s", lines, prompt)
	}
	if strings.Contains(prompt, "## Compact skill requirements") || strings.Contains(prompt, "dispatch.ps1 responsibilities") {
		t.Fatalf("runtime prompt contains installation details:\n%s", prompt)
	}
}

func TestInjectAICollaborationUsesCompactRuntimePrompt(t *testing.T) {
	existing := "# Existing\n"
	next := replaceAICollaborationBlock(existing, aiCollaborationRuntimePrompt(`D:\\wg.exe`))
	if !strings.Contains(next, "## WorkGround2 Worker") || !strings.Contains(next, "# Existing") {
		t.Fatalf("compact runtime block was not injected:\n%s", next)
	}
	if strings.Contains(next, "## Compact skill requirements") {
		t.Fatalf("installation prompt leaked into runtime AGENTS.md:\n%s", next)
	}
}

func TestReplaceAICollaborationBlockIsIdempotent(t *testing.T) {
	first := replaceAICollaborationBlock("# Existing\n", "one")
	second := replaceAICollaborationBlock(first, "two")
	if strings.Count(second, aiCollaborationStart) != 1 || strings.Count(second, aiCollaborationEnd) != 1 {
		t.Fatalf("markers should appear once:\n%s", second)
	}
	if strings.Contains(second, "one") || !strings.Contains(second, "two") {
		t.Fatalf("block should be replaced:\n%s", second)
	}
	if !strings.Contains(second, "# Existing") {
		t.Fatalf("existing content should be preserved:\n%s", second)
	}
}

func TestWorkground2CLIPathPrefersDesktopBuildOverRepoRootStub(t *testing.T) {
	root := t.TempDir()
	desktopExe := filepath.Join(root, "desktop", "build", "bin", "WorkGround2.exe")
	rootExe := filepath.Join(root, "workground2.exe")
	if err := os.MkdirAll(filepath.Dir(desktopExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopExe, []byte("desktop"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rootExe, []byte("root"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := existingWorkground2CLIPath(workground2CLICandidatesFor("", root, rootExe))
	if got != desktopExe {
		t.Fatalf("expected desktop build exe before repo root stub\nwant: %s\n got: %s", desktopExe, got)
	}
}

func TestWorkground2CLIPathUsesEnvOverrideFirst(t *testing.T) {
	root := t.TempDir()
	envExe := filepath.Join(root, "custom.exe")
	desktopExe := filepath.Join(root, "desktop", "build", "bin", "WorkGround2.exe")
	if err := os.MkdirAll(filepath.Dir(desktopExe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(envExe, []byte("env"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desktopExe, []byte("desktop"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := existingWorkground2CLIPath(workground2CLICandidatesFor(envExe, root, ""))
	if got != envExe {
		t.Fatalf("expected WORKGROUND2_CLI override first\nwant: %s\n got: %s", envExe, got)
	}
}
