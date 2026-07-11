# CLI Session Management

Manage WorkGround2 conversation sessions directly from the command line —
list, inspect, rename, delete, and recover saved transcripts. These commands
operate on the same session directory shared by the Desktop app and `workground2
serve`, so changes made from the CLI are visible everywhere.

Status: **Phase 1 implemented** — list, show, rename, delete (soft-trash),
trash listing, restore, and permanent purge.

## Quick Reference

```
workground2 session list                     # 列出所有保存的会话
workground2 session show  <path>             # 显示指定会话的完整对话内容
workground2 session rename <path> <title>    # 设置/修改会话的自定义标题
workground2 session delete <path>            # 将会话移入回收站（软删除）
workground2 session trash                    # 列出回收站中的会话
workground2 session restore <path>           # 从回收站恢复会话
workground2 session purge <path> [--force]   # 永久删除回收站中的会话
```

## Session Directory

By default, commands use the same auto-detected session directory as the
interactive REPL: the project session dir when run from inside a project
workspace, otherwise the global session dir (`~/.WorkGround2/sessions/`).

Override with `--dir`:

```sh
workground2 session list --dir ~/.WorkGround2/sessions
```

## Commands

### `list` — browse saved sessions

```sh
workground2 session list
# PATH                     TURNS  LAST ACTIVITY         TITLE
# 20260709-xxxx.jsonl         12  2026-07-09T08:18:04Z  Rename the desktop project…
```

Output includes:
- **PATH**: the session file basename on disk.
- **TURNS**: number of user messages (conversation turns).
- **LAST ACTIVITY**: RFC 3339 timestamp of the most recent change.
- **TITLE**: the custom title (from `.titles.json`) when set; otherwise the
  first user message as a preview.

### `show` — read a session transcript

```sh
workground2 session show 20260709-075257.307768100-deepseek-deepseek-v4-pro.jsonl
# [1] USER
# 帮我看一下这个项目的架构
#
# [2] ASSISTANT
# …
```

Each message is printed with its sequence number and role. Content longer than
2000 characters per message is truncated for readability.

### `rename` — give a session a custom title

```sh
workground2 session rename 20260709-xxxx.jsonl "Architecture review notes"
# Renamed 20260709-xxxx.jsonl → "Architecture review notes"
```

Custom titles are stored in the `.titles.json` sidecar next to the `.jsonl`
transcripts — the same file used by the Desktop app's history panel. An empty
title clears the custom name (falls back to the preview).

### `delete` — soft-delete to trash

```sh
workground2 session delete 20260709-xxxx.jsonl
# Moved 20260709-xxxx.jsonl to trash
```

Moves the `.jsonl` transcript and all its sidecar artifacts (`.meta`,
`.events.jsonl`, telemetry, checkpoints, jobs) to `.trash/<basename>/`. The
titles entry is preserved so the trash listing still shows the custom name.

This is a **non-destructive** soft delete — equivalent to the Desktop app's
"move to trash". Use `restore` to bring it back.

### `trash` — list trashed sessions

```sh
workground2 session trash
# PATH                     DELETED AT              TITLE
# 20260709-xxxx.jsonl      2026-07-09T10:15:00Z    Architecture review notes
```

### `restore` — recover from trash

```sh
workground2 session restore 20260709-xxxx.jsonl
# Restored 20260709-xxxx.jsonl from trash
```

Moves the session and all its sidecars back to the live session directory.
Fails if a session with the same name already exists.

### `purge` — permanently delete

```sh
workground2 session purge 20260709-xxxx.jsonl
# Permanently delete 20260709-xxxx.jsonl? [y/N] y
# Permanently deleted 20260709-xxxx.jsonl
```

Permanently removes the trashed session directory. Prompts for confirmation
unless `--force` is passed.

## Shared State with Desktop

All `session` commands operate on the same filesystem state as the Desktop app:

| File / Directory | Purpose | Shared? |
|---|---|---|
| `*.jsonl` | Conversation transcript | CLI + Desktop |
| `*.jsonl.meta` | Branch metadata (turns, timestamps) | CLI + Desktop |
| `.titles.json` | User-chosen session titles | CLI + Desktop |
| `.trash/` | Soft-deleted sessions | CLI + Desktop |
| `.display.json` | Message display overrides | Desktop only |
| `.planner-display.json` | Plan-mode display turns | Desktop only |

The CLI writes only `.titles.json` (via `rename`) and `.trash/` entries, and
changes are immediately visible in the Desktop after the next refresh.

## Implementation

The shared logic lives in `internal/session/`:

- `session.TitlesPath`, `LoadTitles`, `SaveTitles`, `SetTitle` — title sidecar.
- `session.TrashPath`, `TrashSession`, `ListTrashed`, `RestoreTrashedSession`,
  `PurgeTrashedSession` — soft-delete lifecycle.
- `session.ValidatePath`, `ValidateTrashedPath` — path sanitisation.
- `session.MovePathIfExists` — cross-device-aware file move.

The CLI commands are in `internal/cli/session.go`. The Desktop wraps the same
shared primitives with runtime safety (session guards, subagent cleanup, read
timeouts) in `desktop/sessions.go`.
