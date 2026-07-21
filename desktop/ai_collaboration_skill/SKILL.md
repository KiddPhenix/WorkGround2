---
name: workground2-worker
description: Delegate bounded implementation to WorkGround2 Desktop while Codex plans, handles interactions, reviews, and verifies. Use when explicitly requested or for code, documentation, configuration, or test changes spanning 2+ files, about 100+ lines, 10+ minutes, or repeated build/test loops. Skip read-only or tiny work, GUI or multimodal tasks, secrets, security, releases, commits, staging, pushes, and under-specified tasks.
---

# WorkGround2 Worker

Use WorkGround2 only as an implementation worker. Codex owns planning, scope, interaction decisions, diff review, validation, recovery, and the final result.

## Dispatch

1. Confirm that the task meets the description trigger and has safe acceptance criteria.
2. Run `desktop workspaces`; if unreachable, report it and continue locally.
3. Do only enough inspection to define outcome, scope, constraints, and acceptance; do not pre-solve work the worker can inspect locally.
4. Build a bounded UTF-8 packet, preferably under 1200 tokens. Avoid repeating AGENTS.md or repository background.
5. Require WorkGround2 to avoid unrelated changes, secrets, commits, staging, pushes, and releases.
6. Use the current repository root and a display-only session name, pass the runtime AGENTS `CLI` value as `-CliPath`, then run `scripts/dispatch.ps1` and preserve its returned SessionID.
7. Parallelize only independent packets with unambiguous session routing. Exit code 0 means dispatched, not completed.

## Interactions

When polling returns `pendingInteraction`:

1. Immediately show its ID, kind, question or subject, options, and intended decision.
2. For `ask`, choose only exact returned option labels; respect multiple questions and `multiSelect`.
3. For `approval`, inspect tool, subject, reason, and authorization before allowing or denying.
4. Ask the user only when current instructions cannot determine a safe choice.
5. Use the exact commands in `references/cli.md`.
6. After answering or approving, run the dispatch script in `PollOnly` mode with the same SessionID.
7. If the interaction command fails, re-read status and never retry a stale ID blindly.
8. Expose missing, malformed, expired, or unknown interaction states explicitly.

## Completion

Finish when `foregroundActive=false` (fall back to `running=false`), `pendingPrompt=false`, and no interaction remains. `backgroundOnly` does not block worker completion.

Use the returned, size-limited `report`. Inspect `git diff --stat` first, then only scoped diffs; run acceptance validation once. Read full files, transcripts, or logs only when validation fails. Repair only incomplete in-scope work and treat unchanged targets or empty sessions as failed delegation.

On timeout or ambiguous failure, preserve the session, inspect status, and avoid repeating `desktop new`. Load `references/cli.md` only for interaction handling, troubleshooting, or session recovery.
