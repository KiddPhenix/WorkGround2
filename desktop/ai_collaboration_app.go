package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	aiCollaborationStart = "<!-- WORKGROUND2_AI_COLLABORATION_BEGIN -->"
	aiCollaborationEnd   = "<!-- WORKGROUND2_AI_COLLABORATION_END -->"
)

type AICollaborationInjectResult struct {
	OK   bool   `json:"ok"`
	Path string `json:"path"`
}

// AICollaborationPrompt returns the prompt users can paste into Codex or another
// AI tool so it treats WorkGround2 Desktop as a CLI-reached implementation worker.
func (a *App) AICollaborationPrompt() string {
	return aiCollaborationPrompt(workground2CLIPath())
}

// InjectAICollaborationPrompt writes only the compact runtime rule into the
// detected Codex global AGENTS.md. Skill installation details stay in the
// one-time copy prompt so every Codex conversation does not pay for them.
func (a *App) InjectAICollaborationPrompt() (AICollaborationInjectResult, error) {
	target, err := codexAgentsPath()
	if err != nil {
		return AICollaborationInjectResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return AICollaborationInjectResult{}, err
	}
	existing, err := os.ReadFile(target)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return AICollaborationInjectResult{}, err
	}
	next := replaceAICollaborationBlock(string(existing), aiCollaborationRuntimePrompt(workground2CLIPath()))
	if err := os.WriteFile(target, []byte(next), 0o644); err != nil {
		return AICollaborationInjectResult{}, err
	}
	return AICollaborationInjectResult{OK: true, Path: target}, nil
}

func aiCollaborationPrompt(cliPath string) string {
	cli := strings.TrimSpace(cliPath)
	if cli == "" {
		cli = "workground2"
	}
	bt := "`"
	return strings.TrimSpace(`WorkGround2 CLI location: ` + cli + `

## Compact skill requirements

Keep the generated runtime skill small:

- SKILL.md: at most 80 physical lines.
- SKILL.md body: preferably 40-60 lines.
- references/cli.md: at most 120 lines.
- Do not copy installation, migration, path discovery, reconciliation, or installation validation into SKILL.md.
- Do not duplicate detailed CLI examples between SKILL.md and references/cli.md.
- Keep PowerShell implementation details inside scripts/dispatch.ps1.
- Keep trigger conditions in frontmatter description and AGENTS.md.
- Keep only runtime decisions and ownership rules in the SKILL.md body.

Write SKILL.md with the following concise content:

---
name: workground2-worker
description: Delegate bounded implementation to WorkGround2 Desktop while Codex plans, handles interactions, reviews, and verifies. Use when explicitly requested or for code, documentation, configuration, or test changes spanning 2+ files, about 100+ lines, 10+ minutes, or repeated build/test loops. Skip read-only or tiny work, GUI or multimodal tasks, secrets, security, releases, commits, staging, pushes, and under-specified tasks.
---

# WorkGround2 Worker

Use WorkGround2 only as an implementation worker. Codex owns planning, scope, interaction decisions, diff review, validation, recovery, and the final result.

## Dispatch

1. Confirm that the task meets the description trigger and has safe acceptance criteria.
2. Run ` + bt + `desktop workspaces` + bt + `; if unreachable, report it and continue locally.
3. Do only enough inspection to define outcome, scope, constraints, and acceptance; do not pre-solve work the worker can inspect locally.
4. Build a bounded UTF-8 packet, preferably under 1200 tokens. Avoid repeating AGENTS.md or repository background.
5. Require WorkGround2 to avoid unrelated changes, secrets, commits, staging, pushes, and releases.
6. Use the current repository root and a stable session name, then run ` + bt + `scripts/dispatch.ps1` + bt + `.
7. Parallelize only independent packets with unambiguous session routing. Exit code 0 means dispatched, not completed.

## Interactions

When polling returns ` + bt + `pendingInteraction` + bt + `:

1. Immediately show its ID, kind, question or subject, options, and intended decision.
2. For ` + bt + `ask` + bt + `, choose only exact returned option labels; respect multiple questions and ` + bt + `multiSelect` + bt + `.
3. For ` + bt + `approval` + bt + `, inspect tool, subject, reason, scope, and authorization before allowing or denying.
4. Ask the user only when current instructions cannot determine a safe choice.
5. Use the exact commands in ` + bt + `references/cli.md` + bt + `.
6. After answering or approving, run the dispatch script in ` + bt + `PollOnly` + bt + ` mode.
7. If the interaction command fails, re-read status and never retry a stale ID blindly.
8. Expose missing, malformed, expired, or unknown interaction states explicitly.

## Completion

Finish when ` + bt + `foregroundActive=false` + bt + ` (fall back to ` + bt + `running=false` + bt + `), ` + bt + `pendingPrompt=false` + bt + `, and no interaction remains. ` + bt + `backgroundOnly` + bt + ` does not block worker completion.

Use the returned, size-limited ` + bt + `report` + bt + `. Inspect ` + bt + `git diff --stat` + bt + ` first, then only scoped diffs; run acceptance validation once. Read full files, transcripts, or logs only when validation fails. Repair only incomplete in-scope work and treat unchanged targets or empty sessions as failed delegation.

On timeout or ambiguous failure, preserve the session, inspect status, and avoid repeating ` + bt + `desktop new` + bt + `. Load ` + bt + `references/cli.md` + bt + ` only for interaction handling, troubleshooting, or session recovery.

## dispatch.ps1 responsibilities

Keep all mechanical behavior in scripts/dispatch.ps1:

- Resolve and validate the CLI, workspace, packet, and arguments.
- Support dispatch, CheckOnly, and PollOnly modes.
- Dispatch with stable session name, ` + bt + `--yolo` + bt + `, and ` + bt + `--no-wait` + bt + `.
- Poll status internally without printing every response.
- Prefer ` + bt + `foregroundActive` + bt + ` over legacy ` + bt + `running` + bt + ` and return compact completion JSON including the final ` + bt + `report` + bt + `.
- Return the complete pendingInteraction object immediately with ` + bt + `outcome=interaction_required` + bt + `.
- Treat interaction_required as recoverable.
- Preserve questions, options, multiSelect, tool, subject, and reason.
- Detect timeout, malformed JSON, missing interactions, and unreachable CLI.
- Never retry ` + bt + `desktop new` + bt + ` automatically.
- Emit explicit nonzero failures for terminal errors.

Do not reproduce the PowerShell implementation in SKILL.md.

## references/cli.md responsibilities

Keep exact CLI syntax and rare recovery details in references/cli.md:

- ` + bt + `desktop workspaces` + bt + `
- ` + bt + `desktop new` + bt + `
- ` + bt + `desktop status --json` + bt + `
- ` + bt + `desktop answer --id ... --answer ...` + bt + `
- repeated ` + bt + `--answer` + bt + ` for multiple questions and multi-select
- ` + bt + `desktop approve --id ... --allow` + bt + `
- ` + bt + `desktop approve --id ... --deny` + bt + `
- ` + bt + `desktop submit --session ...` + bt + `
- ` + bt + `desktop focus` + bt + `
- invalid or expired interaction recovery
- pendingPrompt without pendingInteraction
- timeout and empty-session recovery

Do not repeat general ownership, eligibility, packet construction, installation, or AGENTS rules in the reference.`)
}

func aiCollaborationRuntimePrompt(cliPath string) string {
	cli := strings.TrimSpace(cliPath)
	if cli == "" {
		cli = "workground2"
	}
	return strings.TrimSpace(`## WorkGround2 Worker

CLI: ` + cli + `

Use the installed ` + "`workground2-worker`" + ` skill for eligible code, documentation, configuration, and test changes. Codex defines a compact outcome/scope/acceptance packet, WorkGround2 inspects and implements, and Codex reviews only the scoped diff plus final validation.

If the skill is unavailable, dispatch directly with ` + "`desktop new --workspace <root> --session-name <stable-name> --yolo --no-wait <packet>`" + `, then poll ` + "`desktop status --json`" + `. Exit 0 means dispatched; resolve returned interactions and wait for foreground work to end.

Keep Codex context small: do not duplicate repository background or pre-solve delegated implementation; prefer one dispatch, one compact worker report, ` + "`git diff --stat`" + `, scoped diff review, and one validation pass. Read full worker transcripts or files only after failure.

Skip delegation for tiny/read-only, multimodal/GUI, secrets/security/release/git-publish, or under-specified work. Use the current workspace root, a stable session name, YOLO, and asynchronous dispatch. Treat exit code 0 as dispatched; complete when foreground work and interactions end. Background-only jobs do not block completion.`)
}

func workground2CLIPath() string {
	if path := existingWorkground2CLIPath(workground2CLICandidates()); path != "" {
		return path
	}
	if path, err := exec.LookPath("workground2"); err == nil && strings.TrimSpace(path) != "" {
		return path
	}
	return "workground2"
}

func existingWorkground2CLIPath(candidates []string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if abs, err := filepath.Abs(candidate); err == nil {
			candidate = abs
		}
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate
		}
	}
	return ""
}

func workground2CLICandidates() []string {
	cwd, _ := os.Getwd()
	exe, _ := os.Executable()
	return workground2CLICandidatesFor(os.Getenv("WORKGROUND2_CLI"), cwd, exe)
}

func workground2CLICandidatesFor(envPath, cwd, exePath string) []string {
	var out []string
	if envPath != "" {
		out = append(out, envPath)
	}
	if exePath != "" && filepath.Base(exePath) == "WorkGround2.exe" {
		out = append(out, exePath)
	}
	if cwd != "" {
		out = append(out,
			filepath.Join(cwd, "desktop", "build", "bin", "WorkGround2.exe"),
			filepath.Join(cwd, "desktop", "build", "bin", "workground2.exe"),
			filepath.Join(cwd, "build", "bin", "WorkGround2.exe"),
			filepath.Join(cwd, "build", "bin", "workground2.exe"),
			filepath.Join(cwd, "workground2.exe"),
			filepath.Join(cwd, "bin", "workground2.exe"),
			filepath.Join(cwd, "..", "workground2.exe"),
			filepath.Join(cwd, "..", "bin", "workground2.exe"),
		)
	}
	if exePath != "" {
		dir := filepath.Dir(exePath)
		out = append(out,
			filepath.Join(dir, "WorkGround2.exe"),
			filepath.Join(dir, "workground2.exe"),
			filepath.Join(dir, "bin", "workground2.exe"),
			filepath.Join(dir, "..", "desktop", "build", "bin", "WorkGround2.exe"),
			filepath.Join(dir, "..", "desktop", "build", "bin", "workground2.exe"),
			filepath.Join(dir, "..", "build", "bin", "WorkGround2.exe"),
			filepath.Join(dir, "..", "build", "bin", "workground2.exe"),
			filepath.Join(dir, "..", "workground2.exe"),
			filepath.Join(dir, "..", "bin", "workground2.exe"),
		)
	}
	return out
}

func powershellSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func replaceAICollaborationBlock(existing, prompt string) string {
	block := aiCollaborationStart + "\n" + strings.TrimSpace(prompt) + "\n" + aiCollaborationEnd
	start := strings.Index(existing, aiCollaborationStart)
	if start >= 0 {
		endRel := strings.Index(existing[start:], aiCollaborationEnd)
		if endRel >= 0 {
			end := start + endRel + len(aiCollaborationEnd)
			before := strings.TrimRight(existing[:start], " \t\r\n")
			after := strings.TrimLeft(existing[end:], " \t\r\n")
			return joinNonEmptyBlocks(before, block, after)
		}
	}
	return joinNonEmptyBlocks(existing, block)
}

func joinNonEmptyBlocks(parts ...string) string {
	var out []string
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, "\n\n") + "\n"
}

func codexAgentsPath() (string, error) {
	if home := strings.TrimSpace(os.Getenv("CODEX_HOME")); home != "" {
		return filepath.Join(home, "AGENTS.md"), nil
	}
	for _, dir := range codexHomeCandidates() {
		if dir == "" {
			continue
		}
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return filepath.Join(dir, "AGENTS.md"), nil
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".codex", "AGENTS.md"), nil
	}
	return "", errors.New("codex home not found")
}

func codexHomeCandidates() []string {
	out := []string{
		`D:\Codex\.codex`,
		`C:\Codex\.codex`,
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		out = append(out, filepath.Join(home, ".codex"))
	}
	if appData := os.Getenv("APPDATA"); appData != "" {
		out = append(out, filepath.Join(appData, "Codex"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		out = append(out, filepath.Join(local, "OpenAI", "Codex"))
	}
	return out
}
