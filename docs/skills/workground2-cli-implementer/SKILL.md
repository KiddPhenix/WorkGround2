---
name: workground2-cli-implementer
description: Delegate scoped implementation work from Codex to a running WorkGround2 Desktop session through `workground2 desktop`. Use when Codex should plan, decompose, and verify while WorkGround2 Desktop performs straightforward file edits, code changes, tests, formatting, or docs updates that do not need deep ambiguous reasoning or visual understanding.
allowed-tools: bash
---

# WorkGround2 CLI Implementer

Use WorkGround2 Desktop as Codex's external implementation worker. Codex owns
the thinking: decide scope, write the implementation packet, review the diff,
and verify the result. WorkGround2 receives a concrete task through
`workground2 desktop` and executes it.

This is not a WorkGround2 `runAs: subagent` skill. WorkGround2 is only the
remote executor reached from Codex through the CLI.

## When To Delegate

Default to delegation whenever WorkGround2 Desktop is reachable and the task is
implementation work. Codex should mainly plan, decompose, review, and verify;
WorkGround2 should execute file/code/doc/config/test changes. This is a required
workflow gate, not a hint: before Codex edits files itself, decide whether the
task truly requires Codex-only execution. If it does not, dispatch WorkGround2
first, poll it, inspect the diff, and only then fix anything the worker missed.
If Codex skips delegation, it must state the concrete blocker.

Delegate implementation packets such as:

- known fix in named files or packages
- focused tests for a described behavior
- mechanical refactor in a bounded area
- docs/config/small UI text updates
- formatting or named validation command
- small bug fixes where the cause and target files are already identified
- larger implementation split into bounded non-overlapping packets

Do not delegate root-cause discovery, broad architecture design, product
decisions, image/Figma/OCR/audio/video/browser-GUI inspection,
security-sensitive operations, secrets, release actions, commits, staging, or
work with vague acceptance criteria.

## Required Packet

Send WorkGround2 a precise packet. Include:

- goal: one concrete outcome
- workspace: absolute repo path
- scope: allowed files/directories/packages
- constraints: no unrelated refactors, no commits/staging unless explicitly told
- steps: exact implementation plan
- validation: exact commands, or say validation is skipped
- report: changed files, commands/results, skipped checks, blockers

Tell WorkGround2 to stop and report if the repo contradicts the packet, the
scope is insufficient, or the task needs a design decision.

## Parallel Dispatch

If work can be safely split, dispatch multiple WorkGround2 sessions in parallel.
Only do this for independent, non-overlapping scopes. Give each session a
separate goal and allowed scope, use `--yolo --no-wait` for each worker, then
poll each worker before reconciling diffs and running final verification in
Codex.

## Async Dispatch And Polling

For `--no-wait`, exit code 0 means the task was dispatched to Desktop, not that
the implementation is done. Capture the SessionID returned by `desktop new`,
then poll `desktop status --session <id> --json` every few seconds. If
`pendingPrompt=true`, inspect `pendingInteraction` immediately. For a clear
structured ask, choose from the returned options and run `desktop answer
--session <sessionId> --id <interactionId> --answer '<questionId>=<option label>'`;
for a reviewed approval, run `desktop approve --session <sessionId> --id
<interactionId> --allow` or `--deny`. Then resume polling. Ask the
user only when the packet does not determine a safe choice. Completion requires
`running=false` and `pendingPrompt=false`; only then inspect the worker report,
target files, and repo diff.

If dispatch does not return a SessionID, treat dispatch as failed. Never query
status without an ID or substitute the UI's current session.

## CLI Pattern

Resolve the CLI first:

```powershell
$wg = @(
  $env:WORKGROUND2_CLI
  ".\desktop\build\bin\WorkGround2.exe"
  ".\desktop\build\bin\workground2.exe"
  ".\build\bin\WorkGround2.exe"
  ".\build\bin\workground2.exe"
  ".\bin\workground2.exe"
  ".\workground2.exe"
  (Get-Command workground2 -ErrorAction SilentlyContinue).Source
) | Where-Object { $_ -and (Test-Path -LiteralPath $_) } | Select-Object -First 1
if (-not $wg) { throw "workground2 CLI not found" }
```

Check Desktop reachability:

```powershell
& $wg desktop workspaces
```

Dispatch new implementation work asynchronously by default:

```powershell
$prompt = @'
You are the implementation worker. Codex already did the planning.

Goal:
<one concrete outcome>

Workspace:
<absolute path>

Allowed scope:
- <file/dir/package>

Constraints:
- Do not make unrelated refactors.
- Do not commit, push, or stage files.
- Stop and report if instructions conflict with the repo.

Implementation steps:
1. <specific edit>
2. <specific edit>

Validation:
- Run: <exact command>

Completion report:
- Changed files.
- Commands run and results.
- Blockers or skipped validation.
'@
$sessionName = "<display name for this worker session>"
$dispatch = @(& $wg desktop new --workspace "<absolute repo path>" --session-name $sessionName --yolo --no-wait $prompt 2>&1)
$dispatch | ForEach-Object { $_ }
$sessionID = ($dispatch | Select-String '^SessionID:\s*(.+)$' | Select-Object -First 1).Matches.Groups[1].Value.Trim()
if (-not $sessionID) { throw "desktop new did not return SessionID" }
& $wg desktop focus
for ($i = 0; $i -lt 60; $i++) {
  $status = (& $wg desktop status --session $sessionID --json | ConvertFrom-Json)
  if ($status.pendingPrompt) {
    $status.pendingInteraction | ConvertTo-Json -Depth 8
    break
  }
  if (-not $status.running) { break }
  Start-Sleep -Seconds 5
}
```

Every `desktop new` call creates a new SessionID; `--session-name` is display
metadata and may repeat. Preserve the returned ID and use `desktop submit
--session <id> --yolo --no-wait` only when Codex intentionally continues that
specific Session. Omit `--no-wait` only for small read-only tasks where Codex
needs the reply immediately.

## Approval Boundary

Delegated implementation uses `--yolo` so bounded file edits can proceed without
ordinary tool approval prompts. It does not answer structured asks or protected
approvals. Resolve only the exact `pendingInteraction` ID after inspecting its
question/options or tool/reason; never pipe blanket approval. Reject or stop if
the worker asks for broader scope than the packet allowed.

## Return Format

Return only the handoff needed by Codex:

```text
delegated: yes|no
workspace: <path>
session: <path if known>
mode: async|sync
status: dispatched|completed|pending-approval|failed
result: <short summary>
next: <parent verification / approve in Desktop / retry after Desktop restart>
```
