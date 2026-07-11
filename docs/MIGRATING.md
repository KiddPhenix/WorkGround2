# Migrating to WorkGround2 1.0 (the Go rewrite)

WorkGround2 1.0 is a **ground-up rewrite in Go**. It is a new codebase, not an
incremental upgrade of the `0.x` TypeScript releases. This guide explains what
changed and how to move over.

## TL;DR

| | Legacy (v1) | WorkGround2 1.0+ (v2) |
|---|---|---|
| Language | TypeScript / Node | Go |
| Branch | [`v1`](https://github.com/KiddPhenix/WorkGround2/tree/v1) (maintenance only) | `main` (default, active) |
| Versions | `0.x` (up to v0.54.x) | `1.0.0`+ |
| Install | `npm i -g WorkGround2` (the `latest` tag, stays on `0.x`) | `npm i -g WorkGround2@next` — `latest` deliberately stays on `0.x`; or a release archive / `go build` |
| Code intelligence | embedding semantic search + tree-sitter symbols | LSP-assisted code reading plus grep/read_file/glob; semantic index is not yet ported |

"v1" and "v2" are **codebase generations**, not semver: the v1 line never reached
1.0, so the Go rewrite takes the `1.x` major.

## Installing 1.0

`npm` stays the primary channel — the package wraps the prebuilt Go binary (the
same way esbuild/biome ship native binaries via npm). The binary itself is a
standalone Go executable; npm is only the installer, not a runtime dependency.

**`npm i -g WorkGround2` deliberately still installs `0.x`.** A bare install — and
`npx WorkGround2`, and 0.53's own `update` — follows npm's `latest` tag, which we
keep pinned to the `0.x` line so existing users aren't pulled into the rewrite
without asking. v1.x (Go) ships under the `next` tag; opt in explicitly:

```sh
npm i -g WorkGround2@next     # or pin a version: WorkGround2@1.1.0
WorkGround2
```

`latest` will stay on `0.x` for the foreseeable future, so installing or
updating v2 always means `@next` (or a pinned `1.x`).

Prebuilt archives (`WorkGround2-<os>-<arch>.tar.gz` / `.zip`) and the desktop
installer are attached to each GitHub release. These are a **separate channel**
from npm: the installer drops a standalone desktop/binary build and does not
touch a CLI you installed with `npm i -g`, so the two coexist — an npm `0.53` in
your shell alongside a `1.x` desktop app is expected, not a conflict. Or build
from source:

```sh
git clone https://github.com/KiddPhenix/WorkGround2   # default: main (Go)
cd WorkGround2 && make build                        # -> bin/WorkGround2(.exe)
```

## Configuration

| Legacy | WorkGround2 1.0 |
|---|---|
| TS config files | `WorkGround2.toml` (project) / `config.toml` in WorkGround2 home (`~/.WorkGround2/` on macOS/Linux; `%AppData%\WorkGround2\` on Windows) from v1.8.1 — see `WorkGround2.example.toml` and [Configuration paths](./CONFIG_PATHS.md) |
| env / API keys | Provider config keeps `api_key_env`; saved key values live in WorkGround2 home `.env` (`DEEPSEEK_API_KEY`, `MIMO_API_KEY`, …) |
| project memory | `WorkGround2.md` (+ auto-memory), Claude-Code-compatible |
| MCP servers | `[[plugins]]` in `WorkGround2.toml`, or a Claude-Code `.mcp.json` (read as-is) |

On first launch, v1.8.1+ runs a one-time, **non-destructive** import: it reads
legacy config from `~/Library/Application Support/WorkGround2/config.toml`,
`~/.config/WorkGround2/config.toml`, `~/.WorkGround2/WorkGround2.toml`, or v0.x
`~/.WorkGround2/config.json` (API key, base URL, language, MCP servers), migrates
legacy credentials into `<WorkGround2 home>/.env` when a key is missing there, and
imports past sessions from legacy session directories. Old files are left
untouched, and WorkGround2 prints a boot notice when it imports data. Each session lands in the
workspace it belonged to (read from its v0.x sidecar meta, summary carried over
as the title), so the desktop sidebar lists it under the right project; sessions
whose workspace no longer exists land in the global session dir. Imported
sessions resume with `--resume` or the history panel. The config import only
runs when no v1.8.1+ config exists yet — if v1.8.1+ wrote its config before your
legacy data was in place nothing is overwritten, so copy any missing values
across by hand.

If the automatic pass missed data because you opened a v1.8.1+ CLI/desktop build
before the old paths were available, run `/migrate` from an interactive session.
The command is available only in Go-based WorkGround2 builds that include it; if you
see `unknown command`, upgrade first. It prints progress while it checks legacy
config and credentials, scans legacy memory and session directories, imports
memory files and sessions that were not previously imported, and summarizes the
result. `/migrate` keeps the same safety rules as startup migration: it does not
overwrite an existing `config.toml` or memory file, it respects session import
markers, and it is not available in the legacy 0.x TypeScript line. If the old
v0.x sessions are in a custom Windows install/data directory, use
`/migrate --from "D:\OldWorkGround2"` to import sessions from that explicit source.
See
[Configuration paths](./CONFIG_PATHS.md) for the full path list and limitations.

## What's the same

The agent core carries over: the loop, tools (read/write/edit/glob/grep/bash/…),
subagents (`task`, explore/research/review), skills, hooks, plan mode, MCP client,
and DeepSeek prefix-cache–oriented design.

## What's different

- **Code intelligence**: the Go rewrite uses LSP-assisted code reading plus
  `grep` / `read_file` / `glob` for local understanding. The legacy v1 semantic
  search + tree-sitter symbol index is not bundled in v2 yet, and CodeGraph is no
  longer shipped as an internal MCP server.
- **Plan mode** + `complete_step` (evidence-backed step sign-off).
- **Plan-mode tool overrides are narrower, and plan mode is fail-closed for
  external tools**: `[agent].plan_mode_allowed_tools` now only declares extra
  read-only custom/external tools. It no longer unlocks known blocked plan-mode
  tools such as `bash`, `task`, writers, installers, or memory mutation tools, and
  unsafe bash commands still remain blocked. An MCP/plugin tool whose read-only
  status comes from the server's untrusted `readOnlyHint` is confirmed the first
  time an interactive plan-mode run needs it; choose the persistent option to
  write the plugin-level `trusted_read_only_tools` raw-name list. Auto/YOLO tool
  approval does not answer this trust prompt, although a session or persistent
  trust choice prevents repeat prompts for the same MCP tool. Non-interactive
  runs still fail closed, so pre-seed `trusted_read_only_tools` or declare a
  concrete `mcp__<server>__<tool>` when no user can approve. In the desktop MCP
  panel, expand a server and use **Pre-trust read-only** for currently listed
  `readOnlyHint` tools, per-tool **Pre-trust** for audited readers, or
  **Untrust** to remove a tool; those actions write the same
  `trusted_read_only_tools` list. First-party `ReadOnlyToolNames` overrides and
  built-ins stay trusted.
- **Read-only subagent research**: use `read_only_task` for generic isolated
  research in plan mode, or `read_only_skill` when the work should follow an
  existing skill. Both expose only read-only tools and safe foreground bash, do
  not write resumable transcripts, and keep writer-capable `task` / `run_skill`
  blocked until after plan approval.
- **No web dashboard** — the v2 line is terminal + desktop (Wails), by design.
- Some granular v1 tools are intentionally consolidated (e.g. file-management ops
  go through `bash`); a few v1 tools are not yet ported (tracked on Discussions).

## File encoding

WorkGround2 1.0 supports reading and editing files in UTF-8, UTF-8 BOM, UTF-16
LE/BE, and GB18030 (a superset of GBK). This matches v1's behavior.

- `read_file` decodes any supported encoding to UTF-8 for the model.
- `edit_file` and `multi_edit` preserve the file's original encoding — if you
  edit a GB18030 file, it stays GB18030 on disk.
- `write_file` always writes UTF-8 (the model's output encoding).
- `grep` decodes before matching, so regex patterns work on non-UTF-8 files.

## Reporting issues

Issues and PRs are labelled by line: **`v1`** (legacy TypeScript) and **`v2`**
(Go). File new reports against the line you're using. The legacy `v1` line is in
maintenance mode — bug fixes only, no new features.

Questions? Open a [Discussion](https://github.com/KiddPhenix/WorkGround2/discussions).
