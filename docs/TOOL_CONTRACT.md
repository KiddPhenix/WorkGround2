# Tool Contract

<a href="./TOOL_CONTRACT.zh-CN.md">简体中文</a>

This document records the provider-visible contract for WorkGround2 compile-time built-in tools. It is generated from the same canonical schema path used by the runtime registry.

| Tool | Read-only | Description |
| --- | --- | --- |
| `bash` | false | Execute a command in the shell and return combined stdout/stderr. Use for builds, tests, git, package managers, etc. To search/read/list/edit/move files, prefer the dedicated tools (grep, read_file, ls, glob, edit_file, move_file) over shell grep/cat/ls/find/sed/mv/Move-Item - they behave identically on every OS. For symbol search or architecture questions, prefer LSP/read tools and targeted grep before shell commands. |
| `bash_output` | true | Read new output from a background job started with bash(run_in_background=true) or task(run_in_background=true). Returns the output produced since the last bash_output call for that job, plus its status (running/done/failed/killed). Does not block. |
| `code_index` | true | Lightweight built-in code symbol index. Prefer lsp_* for language semantics and installed code graph MCP tools for call graph, impact, and architecture relationships; use this as the local fallback for file outlines and symbol definition candidates, then verify with read_file or grep. |
| `complete_step` | true | Record the evidence-backed completion of ONE step of an approved plan. Call it as you finish each step instead of silently moving on: it signs the step off with PROOF it is done - the verification you ran (command + result), the diff/files you changed, or a manual check. A completion with no evidence is REJECTED, so don't claim a step is done until you can show why. The host advances the task list for you when you sign off - it marks this step completed and moves the next to in_progress, so you don't need a separate todo_write to mark completions. Fields: `step` (which step - its title or number, matching the task list), `result` (what is now true/changed), `evidence` (>=1 item, each with `kind` = verification\|diff\|files\|manual and a `summary`, plus optional `command`/`paths`), and optional `notes`. |
| `delete_range` | false | Delete a contiguous text range from a file using exact start/end text anchors. Each anchor must match exactly one line. Returns unified diff on success. Use for large deletions - smaller changes should use edit_file. |
| `delete_symbol` | false | Delete a named symbol (function, method, type, interface, const, var) from a Go source file using AST parsing. For non-Go files, use delete_range with manual anchors. |
| `edit_file` | false | Replace an exact string in a file with another. old_string must occur exactly once; add surrounding context to disambiguate. Use for targeted edits instead of rewriting the whole file. |
| `glob` | true | Find files matching a glob pattern (e.g. "*.go", "internal/*/*.go", "**/*.test.ts"). Supports shell metacharacters * ? [] and the recursive ** pattern. |
| `grep` | true | Search for a regular expression in a file, or recursively under a directory (skips hidden files and files matched by .gitignore). Returns matching lines as path:line:text, capped at 200 matches. |
| `kill_shell` | false | Terminate a running background job (bash or task) started with run_in_background. A no-op if the job has already finished or the id is unknown. |
| `ls` | true | List the entries of a directory. Directories are shown with a trailing slash; files show their byte size. Set recursive=true to list all nested files depth-first (skips .git/node_modules). |
| `move_file` | false | Move or rename a file from source_path to destination_path. Creates the destination parent directory as needed. Use instead of shell mv, Move-Item, or ren for file moves so workspace confinement and file-edit permissions apply. |
| `multi_edit` | false | Apply a list of edits to a single file atomically: each edit runs against the result of the previous one, all in memory; the file is rewritten only if every edit succeeds. Cheaper and safer than chaining edit_file calls - a failure in step 3 leaves the file untouched instead of half-edited. |
| `notebook_edit` | false | Edit one cell of a Jupyter notebook (.ipynb). Target a cell by 0-based cell_number (or cell_id). edit_mode: "replace" (default) swaps the cell's source; "insert" adds a new cell after cell_number (use -1 to prepend at the top), taking cell_type and new_source; "delete" removes the cell. cell_type is "code" or "markdown" (required for insert). Editing a code cell clears its outputs. Prefer this over edit_file for notebooks - it keeps the JSON valid. |
| `read_file` | true | Read a text file with optional line offset/limit. Output prefixes each line with its 1-based number so subsequent edit_file calls can target exact lines. Use `offset` and `limit` to page through large files; the tool reports total length and pagination hints in a trailer. |
| `todo_write` | true | Record and update a structured task list for the current work. Send the COMPLETE list every call - it replaces the previous one. Use it to plan multi-step work and show progress: keep exactly one item in_progress at a time, and flip an item to completed the moment it's done (don't batch completions). Skip it for trivial single-step tasks. |
| `view_image` | true | Read a local image file and attach its pixels to the conversation so you can see it directly. The path may be workspace-relative, an `@` workspace reference, or an absolute path inside the workspace. Returns a short confirmation; the image itself is delivered to the model out-of-band, not as text. Read-only. |
| `wait` | true | Block until background jobs finish, then return each job's status and final output/answer. Use to collect the result of a task(run_in_background) or bash(run_in_background) before continuing. Omit job_ids to wait for every running job. |
| `web_fetch` | true | Fetch a URL over HTTPS/HTTP and return its text content. HTML pages are reduced to readable text; JSON / plain text / markdown bodies come back verbatim. Use to read documentation pages, API responses, or source files hosted somewhere the local filesystem can't reach. |
| `write_file` | false | Write content to a file at the given path (overwriting existing content). Creates parent directories as needed. |

## Schema Snapshot

The exact canonical schemas are intentionally tested in code rather than copied by hand here. Run:

```bash
go test ./internal/tool -run TestBuiltinToolContractDocumentation
```

The test checks that every registered built-in tool has a documented name, read-only flag, description row, and canonical schema generated by `tool.BuiltinContractEntries`.

## Default Full Boot Surface

In a default full-token boot, WorkGround2 sends the built-in tools above plus the
session, memory, skill, subagent, LSP, install, and slash-command tools below:

`ask`, `browser_attach`, `browser_click`, `browser_close`, `browser_navigate`, `browser_open`, `browser_scroll`, `browser_state`, `browser_tab`, `browser_type`, `browser_upload`, `explore`, `forget`, `history`, `install_skill`, `install_source`,
`list_sessions`, `lsp_definition`, `lsp_diagnostics`, `lsp_hover`,
`lsp_references`, `memory`, `notify_me`, `parallel_tasks`, `read_only_skill`,
`read_only_task`, `read_session`, `read_skill`, `rebuild_vocabulary`, `remember`, `request_help`, `research`,
`review`, `run_skill`, `security_review`, `slash_command`, `task`.

`notify_me` creates one durable, no-reply owner notification after an explicitly
requested task reaches a terminal outcome. It is side-effecting, unavailable in
plan and token-economy modes, and configured deny/ask rules still take precedence
over its default allow rule.

The ten runtime-bound browser tools share one persistent automation browser per
user across controllers, tasks, settings rebuilds and app restarts:

| Tool | Read-only | Contract |
| --- | --- | --- |
| `browser_open` | false | Idempotently open the session browser, optionally navigating to an HTTP/HTTPS URL. Prefer `browser_*` tools over Playwright; never reload, refresh, or navigate to the same URL merely to observe, synchronize, or retry. |
| `browser_attach` | true | Return the loopback CDP endpoint of the current session for Playwright's `chromium.connectOverCDP()`. Playwright is fallback-only — use it only when `browser_*` tools are unavailable, explicitly fail, or lack a required capability, attaching to this same WG2 browser. Requires `browser_open` first; never starts a second browser. After any Playwright write, call `browser_state(refresh=true)`. |
| `browser_navigate` | false | Navigate the active tab. Requires a stable `request_id`. `allow_leave=true` accepts a `beforeunload` dialog and leaves; default stays on the page and returns `dialog_blocked`. Do not re-navigate to the current URL merely to observe state. |
| `browser_state` | true | Return page text, tabs, `revision`, and indexed interactive elements. No screenshots or form values. `refresh=true` only re-observes state and never reloads the page. |
| `browser_click` | false | Click an element from the exact supplied `revision` and index. `allow_leave=true` accepts a `beforeunload` dialog the click triggers; default stays and returns `dialog_blocked`. |
| `browser_type` | false | Type ordinary, non-sensitive text into an editable indexed element. Password inputs are allowed unless `allow_password_input=false`; file inputs are rejected. |
| `browser_scroll` | false | Scroll the viewport or an indexed element under the supplied revision. |
| `browser_tab` | false | Create, activate, or close a tab under the supplied revision. Closing a tab that blocks with `beforeunload` returns `dialog_blocked` unless `allow_leave=true`. |
| `browser_upload` | false | Set 1-20 existing local regular files on an `input[type=file]`; the selected files' contents become available to the page. Paths are recorded verbatim in the ToolCall transcript; multi-file targets require the `multiple` attribute. Denied when `allow_file_upload=false`. |
| `browser_close` | false | Idempotently detach only the current parent session's browser client; the shared Chromium and its persistent profile survive. |

Browser tools are hidden when `[tools.browser].enabled=false`, when filtered out
by `tools.enabled`, and in token economy mode. Chrome is the primary browser;
Edge, Chromium, and Chrome for Testing use the same CDP contract. V1 uses only
an isolated automation profile (distinct from any default browser profile) and
has no screenshot, drag-and-drop, directory upload, download, secret vault, daily
Chrome profile, cookie, login-state, or password-vault access. Both
`allow_password_input` and `allow_file_upload` default to true and can be
disabled independently; downloads stay denied.

`internal/boot.TestBootToolContractMatchesProviderVisibleSurface` verifies the
actual boot registry contract against the provider request, including read-only
flags and canonical schemas.

### JavaScript dialog handling

Native JavaScript dialogs (`alert`, `confirm`, `prompt`, `beforeunload`) are
resolved per target and never hang an operation until a timeout:

- `beforeunload` defaults to dismiss (stay on the page, unsaved data preserved);
  the initiating `browser_navigate` / `browser_click` / `browser_tab`
  `action=close` returns a structured `dialog_blocked` error instead of timing
  out or silently succeeding. Pass `allow_leave=true` on the same tool to
  accept the dialog and leave.
- Unexpected `alert` is accepted so the page never deadlocks; unexpected
  `confirm` / `prompt` are dismissed and reported as `dialog_blocked`. Accepting
  those dialogs requires the Playwright fallback through `browser_attach`.
- `dialog_blocked` is recoverable and carries a `dialog` context (target id,
  type, message). Navigate and tab-close report a known "stayed" outcome. A
  click reports outcome-unknown because handlers may have run before the dialog;
  call `browser_state` before deciding whether it is safe to retry. A failed CDP
  accept/dismiss returns `dialog_resolution_failed` with outcome-unknown. Policy
  is scoped per target and request and cleaned up on success, failure,
  cancellation, late events, target switches, and driver close.

## Token Economy Boot Surface

In token economy mode, WorkGround2 starts with the core coding/session/memory tools
and the connector used to enable optional sources on demand:

`ask`, `connect_tool_source`, `forget`, `history`, `list_sessions`, `memory`,
`read_session`, `rebuild_vocabulary`, `remember`, `slash_command`.

`rebuild_vocabulary` is the write tool behind the built-in inline Skill of the
same name. It deterministically rebuilds the current workspace vocabulary and
refreshes the Session-local completion snapshot.

Core built-in tools such as `bash`, `read_file`, `grep`, file writers, job tools,
and `todo_write` remain available in economy mode and are listed in the built-in
table above.
