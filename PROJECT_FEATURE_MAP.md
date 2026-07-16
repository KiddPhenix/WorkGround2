# Project Feature Map

Concise, incremental index of confirmed feature locations in this repository.

## Rules

- Only record user-stated or verified findings.
- Keep entries short and path-focused.
- Update existing entries instead of duplicating them.

## Entries

### AddOn 框架与插件包
- Location: `internal/pluginpkg`, `internal/installsource`, `internal/config/plugin_packages.go`, `desktop/plugin_packages_app.go`, `desktop/frontend/src/components/CapabilitiesPanel.tsx`, `cmd/workground2-addon-pack`, `scripts/build-addons.ps1`, `docs/addons`, `D:\Work\wg2addons`, `D:\Work\WG2AddOnsExample`
- Summary: plugin package 是运行时 AddOn 的落点，负责安装来源、manifest、启用状态、skill/hook/MCP/AddOn metadata 合并和桌面管理入口；外部 AddOn 包已迁移到 `D:\Work\wg2addons`，`docs/HOST_INTERFACES.zh-CN.md` 记录主项目提供给 AddOn 的 manifest、安装/打包、MCP newline JSON-RPC、panel/query/action、runtime env、skills/protected frontmatter、hooks 和公开 `pkg/drawaddon` 接口；`D:\Work\WG2AddOnsExample` 是可推送的示例仓库。
- Keywords: AddOn, plugin package, external package, wg2addons, WG2AddOnsExample, HOST_INTERFACES, addon.runtime, MCP runtime, zip package, install archive, skill sharing, manifest, update, management UI, credential ref
- Source: user-requested+verified-by-search
- Updated: 2026-07-06

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

### Desktop AI 协作导出
- Location: `desktop/ai_collaboration_app.go`, `desktop/ai_collaboration_app_test.go`, `desktop/frontend/src/components/SettingsPanel.tsx`
- Summary: Settings 的 AI 协作页导出一次性 skill 安装契约，自动注入仅写入不超过 12 行的运行规则；worker 使用独立命名会话，status 暴露前台/后台状态与限长完成报告，Codex 按 stat、scope diff、单次验证收口以减少重复上下文。
- Keywords: AI Collaboration, workground2-worker, compact skill, dispatch.ps1, references/cli.md, AICollaborationPrompt
- Source: verified-by-search
- Updated: 2026-07-10

### Desktop 会话来源标识
- Location: `desktop/remote_api.go`, `desktop/tabs.go`, `desktop/frontend/src/components/ProjectTree.tsx`, `desktop/session_source_test.go`
- Summary: desktop new 新建会话写入 sessionSource=cli；复用既有会话不重分类，Desktop 接管会清除 CLI 来源；侧栏按 sessionSource/channel/titleSource 渲染来源标签。
- Keywords: sessionSource, CLI badge, setActiveSessionSource, takeoverFromCLI, ProjectTree
- Source: verified-by-search
- Updated: 2026-07-14

### Draw AddOn 画图工具
- Location: `pkg/drawaddon`, `internal/boot/boot.go`, `desktop/draw_addon_app.go`, `desktop/frontend/src/lib/types.ts`, `desktop/frontend/src/lib/bridge.ts`, `D:\Work\wg2addons\draw-tool`, `docs/addons/draw-addon-design.md`
- Summary: draw-tool AddOn 管理多 provider 画图配置、secret 引用、CLI/API 生成任务；主项目不再默认注册 `draw_image` 或固定渲染 Draw AddOn 设置块，安装外部 draw-tool zip 包后由独立编译的 MCP runtime `workground2-draw-addon` 承接模型侧 `draw_image`，插件页按已安装 AddOn package 渲染通用管理块。
- Keywords: DrawAddon, draw-tool, draw_image, GenerateImageWithDrawAddon, apiKeyRef, cliCommand, external AddOn package, wg2addons, zip package, MCP runtime
- Source: verified-by-search
- Updated: 2026-07-06

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

### Provider 引导与本地 CLI 接入
- Location: `desktop/app.go`, `desktop/onboarding_cli.go`, `desktop/frontend/src/components/OnboardingOverlay.tsx`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/lib/types.ts`
- Summary: first-run gate 由 `NeedsOnboarding` 判断是否已有 key、provider_access 或可配置 provider；overlay 支持 DeepSeek key 和本地 CLI 两条路径，本地 CLI 扫描常见命令后保存为 `kind=cli` provider。Settings 的“添加供应商”把本地 CLI 提升为与官方、自定义同级的第三个入口，进入后自动扫描并只展示已安装项，可重新扫描或一键添加并使用；已有可用 API provider 时不会覆盖当前 API 默认模型，无可用 API 时 CLI 作为兜底默认。高级 provider 编辑器仍可修改 command/args/protocol/timeout/model。Codex 预设使用 `exec --json` 和 `jsonl` 协议接收 stdout 事件流，并支持 Windows Codex Desktop 安装目录探测。
- Keywords: onboarding, first-run, settings, ConnectKey, SkipOnboarding, ScanLocalCLIProviders, ConnectLocalCLIProvider, local CLI, codex --json
- Source: verified-by-search
- Updated: 2026-07-12

### Provider 模型后端
- Location: `internal/provider/provider.go`, `internal/provider/openai`, `internal/provider/anthropic`, `internal/provider/cli`
- Summary: provider 包定义模型后端接口和 kind->factory 注册表，OpenAI-compatible、Anthropic、本地 CLI 子包自注册并负责把模型请求适配为对应后端调用；CLI provider 在 Windows 隐藏子进程控制台，并按 stdout text/jsonl 分块转发。
- Keywords: Provider, Stream, ToolCall, openai, anthropic, cli, local CLI, NormalizeMessages, HideWindow, JSONL
- Source: verified-by-search
- Updated: 2026-07-03

### Provider 访问与首次启动
- Location: `desktop/app.go`, `desktop/settings_app.go`, `desktop/frontend/src/components/OnboardingOverlay.tsx`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/providerModels.ts`
- Summary: desktop 首次启动通过 NeedsOnboarding/SkipOnboarding 处理 provider setup；Settings 的 provider_access 记录显式访问项，ProviderEntry 支持 key-backed、无 key、本地/私有 endpoint 和本地 CLI provider。
- Keywords: onboarding, SkipOnboarding, NeedsOnboarding, provider_access, api_key_env, no-key provider, local provider, local CLI
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
- Location: `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/lib/activity.ts`, `desktop/frontend/src/components/Composer.tsx`, `desktop/frontend/src/components/TodoPanel.tsx`, `desktop/frontend/src/lib/todoVisibility.ts`
- Summary: 状态 done；桌面端运行提示由 useController 的 per-tab runtime state 驱动，Composer 渲染右下运行状态，activity 负责阶段趣味文案，TodoPanel 从最新 todo_write 快照渲染待办进度。
- Note: 运行状态胶囊移除 `·` 前的通用阶段前缀，只保留具体任务文案。
- Keywords: running, runstatus, todo_write, TodoPanel, tab switch, openProjectTab, detached runtime
- Source: verified-by-search
- Updated: 2026-07-15

### 桌面传呼机小组件模式
- Location: `docs/desktop/widget-mode-design.md`, `desktop/widget_mode.go`, `desktop/widget_conversation.go`, `desktop/widget_info.go`, `desktop/widget_mode_test.go`, `desktop/widget_info_test.go`, `desktop/frontend/src/assets/widget-mode`, `desktop/frontend/src/components/widget`, `desktop/frontend/src/locales`
- Summary: 状态 done；桌面主窗口可缩为单消息传呼机小组件，聚合任务状态并支持幂等操作、稳定当前项、工作区路由、完成态 session 激活和独立几何恢复。左侧六页点阵信息终端统一使用英文；其余交互支持简中、繁中、英文，并为每种语言提供 40 条短运行文案，后端通过稳定语义码传递可翻译状态。
- Keywords: widget mode, pager, info carousel, widgetSuffixes, routeReasonCode, 多语言, 随机文案, token meter, system telemetry, model logo, idle timer, W2 companion, session:activated, widget-open
- Source: user-requested+design-approved+verified-by-search
- Updated: 2026-07-16

### 桌面通用设置精简
- Location: `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/locales/zh.ts`, `desktop/frontend/src/locales/en.ts`, `desktop/frontend/src/locales/zh-TW.ts`
- Summary: 通用设置页隐藏桌面风格、会话展示、底部信息栏配置，保留工作台默认及兼容配置，并细化新会话审批选项说明。
- Keywords: SettingsPanel, GeneralSection, defaultToolApprovalMode, workbench
- Source: verified-by-search
- Updated: 2026-07-12

### 能力求助路由
- Location: `internal/config/assist.go`, `internal/config/capability.go`, `internal/config/cli_capability.go`, `internal/provider/artifact.go`, `internal/provider/cli/cli.go`, `internal/agent/request_help.go`, `internal/agent/assist_artifact.go`, `internal/boot/boot.go`, `desktop/app.go`, `desktop/onboarding_cli.go`, `desktop/settings_app.go`, `desktop/frontend/src/components/RequestHelpCard.tsx`, `desktop/frontend/src/components/ToolCard.tsx`, `desktop/frontend/src/lib/requestHelp.ts`, `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/components/SettingsPanel.tsx`, `docs/SPEC.md`
- Summary: 状态 done；主模型缺少网页搜索或图片生成能力时，request_help 按显式路由或 provider 顺序选择候选，并在对话流展示接管状态。Codex CLI 运行时探测搜索和画图能力，按 JSONL thread_id 收集请求作用域图片并严格校验；Google/Gemini 按模型识别搜索、识图和画图能力；显式 capabilities 含空数组始终优先。
- Keywords: capability assist, request_help, web_search, image_generation, vision, assist_models, request_id, artifact validation, progress status, history replay, Codex CLI probe, Gemini, thread_id, subagent transcript
- Source: user-requested+verified-by-tests
- Updated: 2026-07-15

### 配置加载与模型解析
- Location: `internal/config/config.go`, `WorkGround2.example.toml`, `docs/CONFIG_PATHS.md`, `docs/GUIDE.md`
- Summary: config 包加载 TOML 配置，处理 flag/project/user/default 优先级、provider/model 解析、desktop/ui/tools/permissions/sandbox/plugin/skill 配置。
- Keywords: Config, WorkGround2.toml, ResolveModel, ProviderEntry, DesktopConfig, ToolsConfig
- Source: verified-by-search
- Updated: 2026-07-03

### 项目说明与工程约定
- Location: `README.md`, `README.zh-CN.md`, `docs/SPEC.md`, `WorkGround2.md`
- Summary: README 说明产品定位和用法，SPEC 是工程合同，WorkGround2.md 是本项目会话常驻工程记忆。
- Keywords: WorkGround2, SPEC, WorkGround2.md, project memory
- Source: verified-by-search
- Updated: 2026-07-03
