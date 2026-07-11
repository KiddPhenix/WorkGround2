<p align="center">
  <img src="docs/logo.svg" alt="WorkGround2" width="640"/>
</p>

<p align="center">
  <strong>English</strong>
  &nbsp;·&nbsp;
  <a href="./README.zh-CN.md">简体中文</a>
  &nbsp;·&nbsp;
  <a href="./docs/GUIDE.md">Guide</a>
  &nbsp;·&nbsp;
  <a href="./docs/SPEC.md">Spec</a>
</p>

# WorkGround2

WorkGround2 is a local-first AI engineering workbench. It connects the CLI,
desktop app, HTTP/SSE frontend, and IM bots to the same Go agent kernel, so
models, tools, permissions, sandboxing, memory, and session state follow one
control path.

It is built for day-to-day engineering work in real repositories: reading code,
editing files, running commands, managing MCP tools, keeping project context,
rewinding file changes, using local or hosted models, and moving between desktop
and terminal workflows without changing the underlying runtime.

## Core Capabilities

- **One controller.** CLI/TUI, `serve`, the Wails desktop app, and the bot
  gateway share `control.Controller`, reusing the same models, tools,
  permissions, and typed event stream.
- **Multiple providers.** DeepSeek, OpenAI-compatible endpoints, Anthropic, and
  local CLI providers are configured through provider entries instead of hardcoded
  model logic.
- **MCP and plugins.** External tools can be attached over stdio, Streamable HTTP,
  or SSE. Tool names, read-only hints, permission checks, and output compaction
  all flow through one registry.
- **Project memory.** `WorkGround2.md`, `AGENTS.md`, user-global memory, and
  auto-memory indexes are loaded into the stable session prefix.
- **Safer execution.** Permission policy, sandboxing, approval flows, YOLO mode,
  checkpoints, and `/rewind` make agent edits recoverable in real worktrees.
- **Desktop workbench.** The Wails + React desktop app provides sessions,
  settings, provider setup, approvals, update checks, diagnostics, and visual task
  flow.
- **Remote entry points.** Feishu, Lark, WeChat, and QQ bots can route IM messages
  into local WorkGround2 sessions while keeping the same permission rules.
- **AutoResearch and Memory v5.** Local research state, the memory compiler, and
  content-free quality metrics support longer-running and cross-session work.

## Quick Start

Download a release build from the [Releases page](https://github.com/KiddPhenix/WorkGround2/releases).

Run one-shot tasks from a shell:

```sh
WorkGround2 run "explain this repository's entry points and module layout"
WorkGround2 run --model deepseek-pro "review the latest changes"
echo "summarize the README" | WorkGround2 run
```

Minimal configuration:

```toml
default_model = "deepseek-flash"

[[providers]]
name        = "deepseek-flash"
kind        = "openai"
base_url    = "https://api.deepseek.com"
model       = "deepseek-v4-flash"
api_key_env = "DEEPSEEK_API_KEY"
```

Configuration resolution is **CLI flags > `./WorkGround2.toml` > user config >
built-in defaults**. User-level config lives at:

- macOS/Linux: `~/.WorkGround2/config.toml`
- Windows: `%AppData%\WorkGround2\config.toml`

Secrets are stored in the WorkGround2 global `.env` or OS credential store.
Project `.env` files are used only for workspace-scoped MCP/plugin `${VAR}`
expansion.

## Desktop App

The desktop app lives in `desktop/` as a separate Go module. It uses Wails for
the native window, while the React frontend calls the same controller through Go
bindings and receives events through `agent:event`.

Develop the desktop app:

```sh
cd desktop
wails dev
```

Develop only the frontend:

```sh
cd desktop/frontend
pnpm install
pnpm dev
```

Build the desktop app:

```sh
cd desktop
wails build
```

## Build From Source

```sh
make build      # -> bin/workground2(.exe)
make cross      # -> dist/ multi-platform artifacts
go test ./...
```

The main repository is Go:

- `cmd/workground2`: CLI entry point.
- `internal/boot`: runtime assembly from config.
- `internal/control`: session lifecycle, send, cancel, approve, compact, rewind.
- `internal/agent`: model requests, tool calls, and context maintenance.
- `internal/tool` / `internal/plugin`: built-in tools and MCP tool integration.
- `internal/config` / `internal/provider`: config loading and model backends.
- `desktop/`: Wails desktop app.
- `site/` and `workers/`: website and Cloudflare Workers services.

## Documentation

- [Guide](./docs/GUIDE.md): config, permissions, sandboxing, plugins, slash
  commands, and `@` references.
- [CLI session management](./docs/CLI_SESSION.md): list, show, rename, delete,
  and recover conversation sessions from the command line.
- [CLI desktop remote control](./docs/CLI_DESKTOP.md): create sessions, submit
  prompts, handle approvals, and read responses in Desktop from the CLI.
- [Bot guide](./docs/BOT_GUIDE.md): IM bot connections, approvals, YOLO, and
  remote commands.
- [Configuration paths](./docs/CONFIG_PATHS.md): user config, project config, and
  secret locations.
- [Tool contract](./docs/TOOL_CONTRACT.md): built-in tool schemas and regression
  guards.
- [Checkpoints and rewind](./docs/CHECKPOINTS.md): edit snapshots and recovery.
- [Spec](./docs/SPEC.md): architecture, data types, registries, and roadmap.

## License

MIT, see [LICENSE](./LICENSE).

## Origin

WorkGround2 originated from [Reasonix](https://github.com/esengine/deepseek-reasonix).
