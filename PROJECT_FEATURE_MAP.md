# Project Feature Map

Concise, incremental index of confirmed feature locations in this repository.

## Rules

- Only record user-stated or verified findings.
- Keep entries short and path-focused.
- Update existing entries instead of duplicating them.

## Entries

### Agent 执行循环
- Location: `internal/agent/agent.go`, `internal/agent/session.go`
- Summary: Agent 持有 Provider、Tool Registry 和 Session，执行流式模型请求、工具调用、权限/plan-mode gating、上下文维护和事件输出。
- Keywords: Agent, Provider, Tool Registry, Run, tool_calls, planMode, context compaction
- Source: verified-by-search
- Updated: 2026-07-03

### AutoResearch 与 Memory Compiler
- Location: `internal/autoresearch/store.go`, `internal/autoresearch`, `internal/memorycompiler/runtime.go`, `docs/superpowers`
- Summary: autoresearch 在 .WorkGround2/autoresearch 下管理研究任务状态，memorycompiler 是本地 rule-driven Memory v5 runtime 和执行 IR/trace 学习层。
- Keywords: autoresearch, Memory v5, PlannerIR, ExecutionTrace, .WorkGround2/autoresearch, memorycompiler
- Source: verified-by-search
- Updated: 2026-07-03

### Bot 与 IM 网关
- Location: `internal/bot/gateway.go`, `internal/bot/session.go`, `internal/bot/pairing.go`, `internal/bot/control_server.go`, `internal/bot/media.go`, `internal/bot/project_index.go`, `internal/botruntime/runtime.go`, `internal/cli/bot.go`, `internal/bot/feishu`, `internal/bot/qq`, `internal/bot/weixin`, `desktop/bot_runtime_app.go`, `desktop/bot_connection_app.go`, `docs/BOT_GUIDE.md`
- Summary: bot gateway 为 QQ/Feishu-Lark/Weixin 管理远端会话、队列策略、角色访问、配对审批、项目/会话命令、媒体附件、control server 和 Controller 生命周期；botruntime 与 desktop 从配置装配连接、route、access 和 pairing 状态，AddOn 框架保持独立。
- Keywords: BotGateway, botruntime, QQ, Feishu, Lark, Weixin, queue, pairing, access, approver, admin, media, control server, route, desktop bot connection, AddOn
- Source: verified-by-search
- Updated: 2026-07-09

### CLI 入口和命令路由
- Location: `cmd/WorkGround2/main.go`, `internal/cli/cli.go`, `internal/cli/desktop.go`, `desktop/desktop_cli.go`, `desktop/remote_api.go`, `docs/CLI_DESKTOP.md`
- Summary: cmd 只做 blank import 和 cli.Run 转发，internal/cli 负责 run/chat/serve/setup/config/mcp/plugin/bot 等子命令路由；Desktop CLI/remote API 支持 workspace/session 派发、结构化运行状态，以及按 interaction ID 回答 ask 或审批，供 Codex 异步轮询执行。
- Keywords: cli.Run, WorkGround2 run, desktop status, pendingInteraction, desktop answer, desktop approve, serve, setup, bot, mcp, plugin
- Source: verified-by-search
- Updated: 2026-07-10

### HTTP Serve 前端
- Location: `internal/serve/serve.go`, `internal/serve/index.html`
- Summary: serve 包把 Controller 暴露成 HTTP/SSE 前端，提供 /events 流和 submit/cancel/approve/plan/rewind/session 等 JSON 端点。
- Keywords: serve, SSE, /events, /submit, /approve, browser UI
- Source: verified-by-search
- Updated: 2026-07-03

### MCP 插件系统
- Location: `internal/plugin/plugin.go`, `internal/plugin`, `cmd/WorkGround2-plugin-example/main.go`, `docs/PLUGIN_PACKAGES.md`
- Summary: plugin 包是 MCP JSON-RPC 客户端，支持 stdio/http 传输，把外部 tools/list 工具适配进 Tool Registry，命名为 mcp__server__tool。
- Keywords: MCP, plugin, JSON-RPC, stdio, streamable-http, mcp__, tools/list
- Source: verified-by-search
- Updated: 2026-07-03

### AddOn 框架与插件包
- Location: `internal/pluginpkg`, `internal/installsource`, `internal/config/plugin_packages.go`, `desktop/plugin_packages_app.go`, `desktop/frontend/src/components/CapabilitiesPanel.tsx`, `cmd/workground2-addon-pack`, `scripts/build-addons.ps1`, `docs/addons`, external `D:\Work\wg2addons`, external `D:\Work\WG2AddOnsExample`
- Summary: plugin package 是运行时 AddOn 的落点，负责安装来源、manifest、启用状态、skill/hook/MCP/AddOn metadata 合并和桌面管理入口；外部 AddOn 包已迁移到 `D:\Work\wg2addons`，`docs/HOST_INTERFACES.zh-CN.md` 记录主项目提供给 AddOn 的 manifest、安装/打包、MCP newline JSON-RPC、panel/query/action、runtime env、skills/protected frontmatter、hooks 和公开 `pkg/drawaddon` 接口；`D:\Work\WG2AddOnsExample` 是可推送的示例仓库。
- Keywords: AddOn, plugin package, external package, wg2addons, WG2AddOnsExample, HOST_INTERFACES, addon.runtime, MCP runtime, zip package, install archive, skill sharing, manifest, update, management UI, credential ref
- Source: user-requested+verified-by-search
- Updated: 2026-07-06

### Draw AddOn 画图工具
- Location: `pkg/drawaddon`, `internal/boot/boot.go`, `desktop/draw_addon_app.go`, `desktop/frontend/src/lib/types.ts`, `desktop/frontend/src/lib/bridge.ts`, external `D:\Work\wg2addons\draw-tool`, `docs/addons/draw-addon-design.md`
- Summary: draw-tool AddOn 管理多 provider 画图配置、secret 引用、CLI/API 生成任务；主项目不再默认注册 `draw_image` 或固定渲染 Draw AddOn 设置块，安装外部 draw-tool zip 包后由独立编译的 MCP runtime `workground2-draw-addon` 承接模型侧 `draw_image`，插件页按已安装 AddOn package 渲染通用管理块。
- Keywords: DrawAddon, draw-tool, draw_image, GenerateImageWithDrawAddon, apiKeyRef, cliCommand, external AddOn package, wg2addons, zip package, MCP runtime
- Source: verified-by-search
- Updated: 2026-07-06

### Memory 与 Skill 系统
- Location: `internal/memory/memory.go`, `internal/skill/skill.go`, `internal/skill/protected.go`, `internal/skill/tools.go`, `internal/agent/protected.go`, `internal/agent/agent.go`, `internal/agent/save.go`, `internal/agent/compact.go`, `internal/cli/skill_picker_view.go`, `internal/skillshare/remote.go`, `desktop/app.go`, `WorkGround2.md`
- Summary: memory 发现 WorkGround2/AGENTS 层级文档和自动记忆索引并折进系统提示，skill 发现项目/自定义/全局 playbook 并按需运行；protected skill 由 host 侧负责原文保护，FlowSkillShare 远端 skill 默认 protected/antiLeak，read_skill 原文读取被阻断，TUI/desktop 历史和 tool result 展示脱敏，Session.Save/compaction archive 不落 protected body，最终答复和工具参数会做 protected body/凭据指纹拦截。
- Keywords: Memory, WorkGround2.md, AGENTS.md, remember, Skill, SKILL.md, run_skill, protected skill, antiLeak, FlowSkillShare, fingerprint guard, read_skill
- Source: verified-by-search
- Updated: 2026-07-06

### Pin Memory Sidebar
- Location: `internal/control/pinned_memory.go`, `internal/control/input.go`, `internal/store/session.go`, `desktop/app.go`, `desktop/frontend/src/components/Message.tsx`, `desktop/frontend/src/components/Transcript.tsx`, `desktop/frontend/src/components/WorkspacePanel.tsx`, `desktop/frontend/src/lib/useController.ts`
- Summary: 分支 `developping/pin-memory-sidebar+2026-07-10`；会话级 pinned memory 使用 `<session>.pinned-memo.json` sidecar 持久化；用户话和助手结论可从 transcript 钉选，右侧 workspace sidebar 的“钉选”页沿改动列表样式展示并支持 unpin/re-pin；Compose 把 active pins 注入 `<pinned-memory>` transient block，压缩/展示清理路径会识别该 block。
- Keywords: pin memory, pinned memory, sidebar, compaction keep, conversation memory, pinned-memo sidecar
- Source: user-requested+verified-by-tests
- Updated: 2026-07-10

### Provider 模型后端
- Location: `internal/provider/provider.go`, `internal/provider/openai`, `internal/provider/anthropic`, `internal/provider/cli`
- Summary: provider 包定义模型后端接口和 kind->factory 注册表，OpenAI-compatible、Anthropic、本地 CLI 子包自注册并负责把模型请求适配为对应后端调用；CLI provider 在 Windows 隐藏子进程控制台，并按 stdout text/jsonl 分块转发。
- Keywords: Provider, Stream, ToolCall, openai, anthropic, cli, local CLI, NormalizeMessages, HideWindow, JSONL
- Source: verified-by-search
- Updated: 2026-07-03

### Provider 引导与本地 CLI 接入
- Location: `desktop/app.go`, `desktop/onboarding_cli.go`, `desktop/frontend/src/components/OnboardingOverlay.tsx`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/lib/types.ts`
- Summary: first-run gate 由 `NeedsOnboarding` 判断是否已有 key、provider_access 或可配置 provider；overlay 支持 DeepSeek key 和本地 CLI 两条路径，本地 CLI 扫描常见命令后保存为 `kind=cli` provider 并切换默认模型。Settings provider 编辑器也能扫描本地 CLI 并把 command/args/protocol/timeout/model 填入表单；Codex 预设使用 `exec --json` 和 `jsonl` 协议接收 stdout 事件流。
- Keywords: onboarding, first-run, settings, ConnectKey, SkipOnboarding, ScanLocalCLIProviders, ConnectLocalCLIProvider, local CLI, codex --json
- Source: verified-by-search
- Updated: 2026-07-03

### Session 持久化、后台任务与回滚
- Location: `internal/store/session.go`, `internal/agent/save.go`, `internal/agent/session.go`, `internal/agent/session_lease.go`, `internal/agent/session_removal.go`, `internal/agent/recovery_gc.go`, `internal/checkpoint/checkpoint.go`, `internal/jobs/jobs.go`, `internal/control/controller.go`, `internal/control/checkpoint.go`, `internal/control/session_lease_keeper.go`, `internal/boot/boot.go`, `internal/cli/session_lease.go`, `internal/acp/service.go`, `internal/serve/serve.go`, `desktop/tabs.go`, `desktop/app.go`, `desktop/settings_app.go`, `desktop/sessions.go`, `desktop/recovery_gc.go`
- Summary: store 集中 session sidecar 路径，checkpoint 记录编辑前快照支持 rewind/fork，jobs 管理跨 turn 的后台 bash/task 任务与 artifact；`codex/session-recovery-port-2026-07-09` 移植 Reasonix 的 session lease、CAS 保存冲突恢复、recovery branch、removal guard、desktop/CLI/serve/ACP 恢复回调与 lease 跟随，并把 intentional rewrite 路径切到 `SnapshotRewrite()`；CLI rename 使用 `CustomTitle`。AddOn 框架保持在 WorkGround2 现有入口上适配。
- Keywords: session jsonl, sidecar, checkpoint, rewind, jobs, background, cleanup-pending, session lease, recovery branch, recovery GC, SaveSnapshot, SnapshotRewrite, CustomTitle, AddOn
- Source: verified-by-search
- Updated: 2026-07-09

### Tool 注册表与内置工具
- Location: `internal/tool/tool.go`, `internal/tool/builtin`, `docs/TOOL_CONTRACT.md`
- Summary: tool 包定义 Tool 接口、ReadOnly/Preview/SnipHint 等能力和每次运行的 Registry，builtin 子包用 init 注册读写文件、bash、grep 等内置工具。
- Keywords: Tool, Registry, ReadOnly, Previewer, builtin, bash, read_file, edit_file
- Source: verified-by-search
- Updated: 2026-07-03

### Wails 桌面端
- Location: `desktop/main.go`, `desktop/app.go`, `desktop/wails.json`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/App.tsx`
- Summary: desktop 是独立 Go module 的 Wails shell，App 暴露 Go 绑定，React bridge 调用绑定并订阅 agent:event，当前产品输出名是 WorkGround2。
- Keywords: Wails, WorkGround2, App, WorkspaceTab, bridge.ts, agent:event, React
- Source: verified-by-search
- Updated: 2026-07-03

### Desktop Obsidian Iris 信息架构翻新
- Status: done
- Branch: `codex/desktop-ui-obsidian-iris`
- Owner: Codex + WorkGround2 Desktop
- Location: `docs/DESKTOP_UI_DESIGN_SPEC.zh-CN.md`, `docs/design-qa.md`, `docs/assets/workground2-desktop-obsidian-iris-reference.png`, `desktop/frontend/src/App.tsx`, `desktop/frontend/src/components/desktop-ui`, `desktop/frontend/src/store`, `desktop/frontend/src/styles.css`
- Summary: 已按 1488×1024 黄金夹具完成 Workspace/Session 两级导航、任务记忆、Run 完成态 40px 折叠与运行态 160px 受限窗、固定 Artifact Shelf、可排序 Queue、运行配置条和多实例 AddOn 浮层；同视口视觉 QA、窄屏稳定性、核心交互和构建均通过。
- Keywords: Obsidian Iris, WorkspaceSidebar, TaskMemoryBar, RunBlock, ArtifactShelf, RuntimeConfigBar, AddOnSurfaceRegistry, design QA
- Source: user-approved-design + implementation-spec
- Updated: 2026-07-10

### 桌面会话运行态恢复与回看
- Status: done
- Branch: `developping/session-runtime-resume+2026-07-11`
- Owner: Codex + WorkGround2 Desktop
- Location: `desktop/frontend/src/components/Composer.tsx`, `desktop/frontend/src/components/Transcript.tsx`, `desktop/frontend/src/components/desktop-ui`, `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/lib/useScrollManager.ts`, `desktop/frontend/src/store`
- Summary: Run 停止操作改为明确的 CircleStop；处理结果阶段只显示风格化短句；Workbench 统一由 conversation viewport 持有滚动并在会话切换后落到底部；TaskMemory 从 TabMeta/Meta/ListTabs 即时恢复，运行态显示真实任务与状态，完成态补充来自真实历史的最近摘要。类型检查、定向回归和前端生产构建通过。
- Keywords: session resume, runtime status, TaskMemoryBar, RunBlock, auto scroll, stop action
- Source: user-reported-runtime-ux
- Updated: 2026-07-11

### 桌面侧栏会话入口与滚动反馈
- Status: done
- Branch: `developping/sidebar-session-list-polish+2026-07-11`
- Owner: Codex + WorkGround2 Desktop
- Location: `desktop/frontend/src/App.tsx`, `desktop/frontend/src/styles.css`, `desktop/frontend/src/__tests__/workbench-layout.test.ts`
- Summary: 侧栏“新建会话”已改为无重描边的轻量工具入口；会话列表使用默认透明、真实滚动时显示并在 700ms 后隐藏的细圆角滚动条，原生箭头常驻隐藏，兼容 Chromium/Wails 与 Firefox。类型检查、74 项 Workbench 布局契约和前端生产构建通过。
- Keywords: workspace sidebar, new session, session list, scrollbar, scroll feedback
- Source: user-reported-sidebar-ux
- Updated: 2026-07-11

### 桌面会话产物投影与历史恢复
- Status: done
- Branch: `developping/session-artifact-projection+2026-07-11`
- Owner: Codex
- Location: `desktop/artifacts.go`, `desktop/artifacts_test.go`, `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/store/artifacts.ts`, `desktop/frontend/src/components/desktop-ui/ArtifactShelf.tsx`, `desktop/frontend/src/__tests__`
- Summary: 后端从完整会话历史中的成功产物工具和 `complete_step` host-verified 文件证据识别脚本、可执行文件、安装包、压缩包、图片、音视频与 PDF；路径必须位于当前工作区并按磁盘状态验证。前端在会话恢复、产物工具完成和回合结束时幂等刷新 Artifact Shelf；类型检查、专项回归、Go vet、前端生产构建和历史时序测试通过。
- Restart recovery: 桌面启动时控制器异步恢复，前端可能在控制器就绪前调用 `ArtifactsForTab`。当 `Ctrl==nil` 但 tab 有合法的 `SessionPath` 时，`ArtifactsForTab` 调用 `agent.LoadSession` 从磁盘 `.jsonl` / `.events.jsonl` 恢复历史消息并投影产物；失败路径安全返回空列表并记录 `slog.Warn`。路径经 `validateSessionPath` 校验，禁止加载会话目录之外的文件。`TestArtifactsForTab_RestartRecoveryFromEventLog` 会清空兼容 `.jsonl` 锚点，验证产物确实由权威 `.events.jsonl` 重放恢复。
- Keywords: Artifact Shelf, complete_step, files evidence, write_file, history hydration, artifact projection, restart recovery, agent.LoadSession
- Source: user-reported-artifact-missing
- Updated: 2026-07-12

### 桌面产物架扩展浏览
- Status: done
- Branch: `developping/artifact-shelf-scale+2026-07-11`
- Owner: Codex + WorkGround2 Desktop
- Location: `desktop/frontend/src/components/desktop-ui/ArtifactShelf.tsx`, `desktop/frontend/src/components/desktop-ui/IrisInfoComponents.tsx`, `desktop/frontend/src/styles.css`, `desktop/frontend/src/__tests__`
- Summary: Artifact Shelf 保持 64px 单行高度，左侧固定总数与“全部”入口，最多展示 6 个最近的可用/生成中产物；完整列表在入口上方浮层按当前/历史分组，超过 12 项提供名称/路径搜索和实际类型筛选，保留打开、重新校验、重新生成操作。入口在 AddOn 浮层开启时仍可访问；专项交互、组件、布局、主题、Store 回归与前端生产构建通过，并完成真实浏览器渲染检查。
- Keywords: Artifact Shelf, recent artifacts, all artifacts, search, type filter, history states, scalable UI
- Source: user-requested-artifact-shelf-scale
- Updated: 2026-07-11

### Web 站点与 Cloudflare Workers
- Location: `site/package.json`, `site/src/pages`, `workers/accounts`, `workers/registry`, `workers/forum`, `workers/crash-report`
- Summary: site 是 Astro 官网/社区前端；workers 下是 Cloudflare Worker 服务，包含 accounts、registry、forum、crash-report，使用 Hono/Zod/Wrangler/D1。
- Keywords: Astro, Cloudflare Workers, wrangler, Hono, D1, accounts, registry, forum, crash-report
- Source: verified-by-search
- Updated: 2026-07-03

### 上游可靠性加固
- Location: `internal/provider/openai`, `internal/agent`, `internal/control`, `internal/skill`, `internal/tool`, `internal/fileutil/encoding`, `internal/config`, `internal/plugin`, `internal/acp`, `internal/boot`, `desktop`
- Summary: 状态 done；分支 `developping/upstream-hardening+2026-07-10`；按行为重实现 DeepSeek reasoning 回放、planner 失败降级/no-op/宿主审批与用户决策、review/子代理只读边界、tab-scoped 工作区、Windows 文本解码、MCP get、ACP 文件/终端协作、plan/location/mode。全仓测试编译、Go vet、受影响核心包实跑通过；Windows 历史 `printf`/长等待测试单列风险。
- Keywords: upstream hardening, reasoning_content, planner fallback, host approval, subagent boundary, read-only review, tab scoped, encoding, mcp get, ACP, file overlay, terminal, plan, mode
- Source: verified-by-search
- Updated: 2026-07-10

### 会话控制器
- Location: `internal/control/controller.go`, `internal/control`, `desktop/tabs.go`, `desktop/frontend/src/lib/useController.ts`
- Summary: control.Controller 是 transport-agnostic 会话驱动，统一 Send/Cancel/Approve/Plan/Compact/Checkpoint/Goal/MCP 等生命周期；RuntimeStatus 用少量 mode 区分 idle/foreground/waiting_user/background_only/cancelling，并向桌面端导出 foreground/background 派生事实，避免 UI 只靠 running 推断。
- Keywords: Controller, Send, Approve, PlanMode, Goal, MCP, typed event stream, RuntimeStatus, RuntimeMode, foregroundActive, backgroundOnly
- Source: verified-by-search
- Updated: 2026-07-07

### 共享启动装配
- Location: `internal/boot/boot.go`
- Summary: boot.Build 是配置到运行时 Controller 的唯一装配点，解析模型、工具、插件、权限、memory、skills、jobs 并供 CLI/serve/desktop 共用。
- Keywords: boot.Build, Controller, provider, tool registry, memory, skills, jobs
- Source: verified-by-search
- Updated: 2026-07-03

### 权限与沙盒
- Location: `internal/permission/permission.go`, `internal/sandbox/sandbox.go`, `internal/tool/builtin`
- Summary: permission 做每个工具调用的 allow/ask/deny 规则判断，sandbox 对 bash 做 OS 级写入/读取/网络约束，文件写入工具另有 in-process 限制。
- Keywords: Policy, Gate, allow, ask, deny, sandbox, bash, write roots
- Source: verified-by-search
- Updated: 2026-07-03

### 构建、桌面打包与 npm 分发
- Location: `Makefile`, `desktop/wails.json`, `desktop/README.md`, `npm/WorkGround2/package.json`, `npm/build.mjs`, `scripts/desktop-build.sh`
- Summary: Makefile 构建静态 CLI 和插件示例，desktop/wails.json 驱动 Wails 桌面构建，npm/build.mjs 生成多平台 @WorkGround2/cli-* 预编译 npm 包。
- Keywords: make build, CGO_ENABLED=0, wails build, pnpm build, npm, release, WorkGround2.exe
- Source: verified-by-search
- Updated: 2026-07-03

### 桌面运行状态与待办提示
- Location: `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/components/Composer.tsx`, `desktop/frontend/src/components/TodoPanel.tsx`, `desktop/frontend/src/lib/todoVisibility.ts`
- Summary: 桌面端运行提示由 useController 的 per-tab runtime state 驱动，Composer 渲染右下运行状态，TodoPanel 从最新 todo_write 快照渲染待办进度。
- Keywords: running, runstatus, todo_write, TodoPanel, tab switch, openProjectTab, detached runtime
- Source: verified-by-search
- Updated: 2026-07-07

### Desktop AI 协作导出
- Location: `desktop/ai_collaboration_app.go`, `desktop/ai_collaboration_app_test.go`, `desktop/frontend/src/components/SettingsPanel.tsx`
- Summary: Settings 的 AI 协作页导出一次性 skill 安装契约，自动注入仅写入不超过 12 行的运行规则；worker 使用独立命名会话，status 暴露前台/后台状态与限长完成报告，Codex 按 stat、scope diff、单次验证收口以减少重复上下文。
- Keywords: AI Collaboration, workground2-worker, compact skill, dispatch.ps1, references/cli.md, AICollaborationPrompt
- Source: verified-by-search
- Updated: 2026-07-10

### 桌面通用设置精简
- Location: `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/locales/zh.ts`, `desktop/frontend/src/locales/en.ts`, `desktop/frontend/src/locales/zh-TW.ts`
- Summary: 通用设置页隐藏桌面风格、会话展示、底部信息栏配置，保留工作台默认及兼容配置，并细化新会话审批选项说明。
- Keywords: SettingsPanel, GeneralSection, defaultToolApprovalMode, workbench
- Source: verified-by-search
- Updated: 2026-07-12

### 配置加载与模型解析
- Location: `internal/config/config.go`, `WorkGround2.example.toml`, `docs/CONFIG_PATHS.md`, `docs/GUIDE.md`
- Summary: config 包加载 TOML 配置，处理 flag/project/user/default 优先级、provider/model 解析、desktop/ui/tools/permissions/sandbox/plugin/skill 配置。
- Keywords: Config, WorkGround2.toml, ResolveModel, ProviderEntry, DesktopConfig, ToolsConfig
- Source: verified-by-search
- Updated: 2026-07-03

### Provider 访问与首次启动
- Location: `desktop/app.go`, `desktop/settings_app.go`, `desktop/frontend/src/components/OnboardingOverlay.tsx`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/providerModels.ts`
- Summary: desktop 首次启动通过 NeedsOnboarding/SkipOnboarding 处理 provider setup；Settings 的 provider_access 记录显式访问项，ProviderEntry 支持 key-backed、无 key、本地/私有 endpoint 和本地 CLI provider。
- Keywords: onboarding, SkipOnboarding, NeedsOnboarding, provider_access, api_key_env, no-key provider, local provider, local CLI
- Source: verified-by-search
- Updated: 2026-07-03

### 项目说明与工程约定
- Location: `README.md`, `README.zh-CN.md`, `docs/SPEC.md`, `WorkGround2.md`
- Summary: README 说明产品定位和用法，SPEC 是工程合同，WorkGround2.md 是本项目会话常驻工程记忆。
- Keywords: WorkGround2, WorkGround2, SPEC, WorkGround2.md, project memory
- Source: verified-by-search
- Updated: 2026-07-03

### 模型设置简化与接入引导
- Status: done
- Branch: `developping/model-settings-simplification+2026-07-12`
- Owner: Codex
- Location: `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/styles.css`, `desktop/frontend/src/locales`, `internal/config/fetch.go`, `docs/MODEL_CONFIGURATION_UX_DESIGN.zh-CN.md`
- Summary: 模型设置已收敛为连接状态、默认模型和显式“添加模型服务”主任务；高级运行参数按需展开，官方供应商接入隐藏内部字段并在保存时验证连接，失败显式提示且保留草稿供重试。专项契约、既有设置契约、生产构建、desktop 全量 Go 测试与视觉 QA 通过。
- Keywords: model settings, provider access, connection check, default model, progressive disclosure
- Source: user-approved-design
- Updated: 2026-07-12

### 桌面全局设置持久化
- Status: done
- Branch: `developping/settings-persistence-race+2026-07-12`
- Owner: Codex
- Location: `desktop/app.go`, `desktop/settings_app.go`, `desktop/settings_app_test.go`
- Summary: Desktop 设置通过 `updateUserConfig` 串行执行用户 config 的读取、修改和原子写回，避免后台 provider 刷新或相邻设置操作用旧快照覆盖 `composer_submit_key`、`default_tool_approval_mode` 等字段。
- Keywords: configWriteMu, updateUserConfig, composer_submit_key, default_tool_approval_mode, lost update, debug-restart
- Source: user-reported+verified-by-tests
- Updated: 2026-07-12
