# WorkGround2

Local-first AI engineering workbench — Go agent kernel shared by CLI/TUI, HTTP/SSE, Wails desktop, and IM bots.

## Project

- **Stack:** Go 1.25+ (toolchain 1.26.4), Wails + React desktop, Astro site, Cloudflare Workers.
- **Entry point:** `cmd/workground2/main.go` — blank imports wire providers/tools, then delegates to `internal/cli`.
- **Module:** `workground2` (go.mod root). Desktop is a separate Go module under `desktop/`.
- **Config:** CLI flags > `./WorkGround2.toml` > user `%AppData%\WorkGround2\config.toml`. Secrets in OS credential store or global `.env`.

## Commands

```sh
make build       # CGO_ENABLED=0 → bin/workground2(.exe) + plugin example
make cross       # dist/ for darwin/linux/windows × amd64/arm64
make test        # go test ./...
make vet         # go vet ./...
make fmt         # gofmt -w .
make hooks       # core.hooksPath → .githooks (pre-push runs go vet)

cd desktop && wails dev    # desktop app dev
cd desktop && wails build  # desktop app build
```

No `make` on Windows — use direct Go commands: `go build -o bin/workground2.exe ./cmd/workground2`, `go test ./...`, `go vet ./...`, `gofmt -w .`.

## Test Strategy

- **Fast feedback first:** during implementation, run the smallest relevant test or package. Do not run the full suite after every edit.
  - Single test: `go test ./internal/boot/ -run RequestHelp -count=1`
  - Related packages: `go test ./internal/tool/builtin/ ./internal/boot/`
- **Full validation at gates:** run `go test ./...` and `go vet ./...` only when the feature is ready for handoff, before commit/push, or when shared infrastructure changes require broad regression coverage.
- **External model tests are integration tests:** real DeepSeek, Codex CLI, Gemini, network, credential, or image-generation calls must be opt-in through a build tag or explicit environment variable. Default unit tests should use fakes and verify capability detection, routing, `request_help` arguments, retries, and failure states without external calls.
- **Failures stay visible and retryable:** skipped integration tests must state how to enable them; failed external calls must report the provider/model and error instead of silently passing or hanging.

## Architecture

All frontends share one transport-agnostic `control.Controller`; add behavior to the controller, never a single frontend.

| Package | Role |
|---|---|
| `internal/boot` | Assembles Controller from config: providers, tools, permissions, executor |
| `internal/control` | Session lifecycle, Send/Cancel/Approve/Compact/Rewind, typed event stream |
| `internal/agent` | Model requests, tool-call dispatch, context maintenance, provider adapters |
| `internal/tool` + `builtin` | Tool interface + Registry; built-ins self-register via `init()` |
| `internal/plugin` | MCP JSON-RPC client (stdio/http/SSE), adapts external tools into Registry |
| `internal/config` / `provider` | Config loading (TOML) and model backends (OpenAI, Anthropic, CLI) |
| `internal/cli` | Subcommand routing: run, chat, serve, setup, config, mcp, plugin, bot |
| `internal/serve` | HTTP/SSE frontend (`/events` stream, `/submit`, `/approve`, etc.) |
| `internal/bot` / `botruntime` | IM gateway: Feishu/Lark, QQ, WeChat adapters |
| `internal/memory` / `memorycompiler` | Project memory docs, auto-memory indexes, Memory v5 compiler |
| `internal/permission` / `guardian` / `sandbox` | Permission policy, approval flows, sandboxing |
| `desktop/` | Wails + React desktop app (separate module) |

## Conventions

- **Package comments** on every package — match the surrounding style and density.
- **Controller-first:** all frontends (TUI, HTTP, desktop, bots) drive the same `control.Controller`. Don't add turn logic to a frontend.
- **Cache-stable prefix:** the system-prompt prefix (base + tools + memory) must stay byte-identical across turns so DeepSeek's prefix cache stays warm. Never mutate it mid-session.
- **Tool pattern:** implement `tool.Tool` (Name, Description, Schema, Execute, ReadOnly); optionally `Previewer`, `SnipHinter`, `PlanModeClassifier`. Register via `init()`.
- **MCP tool naming:** `mcp__<server>__<tool>` — use `tool.SplitMCPName` to decompose.
- **Formatting:** `gofmt` (no `goimports` grouping required). Linter: errcheck, govet, ineffassign, staticcheck, unused.
- **Import cycle guard:** before importing a package from non-test code, verify its test files don't import back. Run `go test ./path/to/target/` — `[setup failed]` means a cycle.
- **Pre-push:** `gofmt -w .`, `go vet ./...`, `go test ./internal/tool/builtin/ ./internal/boot/`.
- **PR hygiene:** one force-push per review round; minimal diff; amend (don't add commits) for feedback.
- **Cache-impact PRs:** when touching `internal/boot/`, `internal/tool/`, `internal/provider/` etc., add `Cache-impact:` and `Cache-guard:` lines to the PR body.
- **Tests:** colocate `*_test.go` files; use standard `testing` package.

## Notes
