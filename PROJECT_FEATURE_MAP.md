# Project Feature Map

WorkGround2 功能到代码位置的快速索引。用于先定位入口，再按需深入代码。

## Rules

- 只记录稳定功能及其核心位置，不记录项目计划、分支、进度、验收结果或变更历史。
- 每个功能保留最有用的入口路径；同一功能的细节合并到一个条目。
- Summary 只说明职责边界，Keywords 只服务于搜索。
- 位置必须来自用户确认或代码验证；发现过期信息时直接修正原条目。

## Entries

### Agent 多选人格提示词
- Location: `internal/agentstyle`, `internal/boot/boot.go`, `desktop/settings_app.go`, `desktop/frontend/src/components/SettingsPanel.tsx`
- Summary: 设置页按病名展示并多选 Agent 风格，稳定 ID 立即持久化到 `agent.prompt_styles`；运行中的 Controller 保持不变，安全后延迟重建。能力描述只保留原分号前的风格句，system prompt 仅含单一 `风格: ` 前缀，不含兜底句、病名或风格名。
- Keywords: Agent 风格, prompt_styles, SetAgentPromptStyles, system prompt
- Source: verified-by-search
- Updated: 2026-09-01

### 浏览器原生优先策略（Browser Native-First Policy）
- Status: `done`
- Location: `internal/config/config.go`（BrowserPolicy 常量）, `internal/boot/boot.go`（system prompt 追加）, `internal/tool/browser/tools.go`（browser_open/state/attach 描述与 schema）, `internal/tool/builtin/bash.go`（bash 描述与 schema）, `docs/BROWSER_CONTROL_DESIGN.zh-CN.md`, `docs/TOOL_CONTRACT.md`
- Summary: 模型系统提示与工具描述统一要求浏览器操作优先使用内置 `browser_*` 工具，Playwright 仅作 fallback 且必须 attach 同一 WG2 浏览器；`browser_state(refresh=true)` 只重观察快照、绝不重载页面，禁止仅为观察/同步/重试而 reload/navigate 同 URL。
- Keywords: BrowserPolicy, browser_*, Playwright fallback, browser_attach, browser_state, no reload, 页面不重载
- Source: verified-by-search
- Updated: 2026-08-25
- 验收：`go test ./internal/config/ ./internal/boot/ ./internal/tool/browser/ ./internal/tool/builtin/ -count=1` 全绿；`go vet` 与 `git diff --check` 通过；改动文件见 Location 与 `internal/boot/boot_test.go`、`internal/config/system_prompt_test.go`、`internal/tool/browser/priority_test.go`、`internal/tool/builtin/bash_schema_test.go`、`docs/TOOL_CONTRACT.zh-CN.md`。

### 日常 / Daily Routine
- Location: `desktop/daily_routine.go`, `desktop/daily_routine_test.go`, `desktop/app.go`, `desktop/widget_icon_mode.go`, `desktop/frontend/src/components/widget/DesktopIconMode.tsx`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/__tests__/desktop-icon-mode.test.ts`
- Summary: Workspace 级日常模板支持从 Session 对话/工具记录严格提炼、本地原子持久化与损坏恢复；Workspace 图标可列出、执行、改名和幂等删除，执行通过可确认的普通 user-turn 链路创建并恢复 Session。前后端 requestId、迟到结果、busy、renderer 重启和 workspace 切换均有隔离与安全重试。
- Keywords: 日常, Daily Routine, I will do it again, DesktopIconMode, CreateBlankSession, TrySubmitUserTurn, daily-routines-v1.json
- Source: verified-by-search
- Updated: 2026-08-21

### AddOn 框架与插件包
- Location: `internal/pluginpkg`, `internal/installsource`, `internal/config/plugin_packages.go`, `desktop/plugin_packages_app.go`, `docs/addons`, `pkg/drawaddon`
- Summary: AddOn 的安装来源、包清单、启用状态、运行时能力合并、桌面管理和宿主接口集中在这些位置。
- Keywords: AddOn, plugin package, manifest, install archive, runtime, host interfaces
- Source: verified-by-search
- Updated: 2026-08-20

### Agent 执行循环
- Location: `internal/agent/agent.go`, `internal/agent/session.go`, `internal/agent/save.go`, `internal/agent/compact.go`
- Summary: Agent 负责模型流式请求、工具调用、权限门控、上下文维护、压缩和会话保存。
- Keywords: Agent, Run, tool_calls, plan mode, context, compaction
- Source: verified-by-search
- Updated: 2026-08-20

### AutoResearch 与 Memory Compiler
- Location: `internal/autoresearch`, `internal/memorycompiler/runtime.go`, `docs/superpowers`
- Summary: AutoResearch 管理研究任务状态，Memory Compiler 负责 Memory v5 的执行 IR、trace 和规则运行时。
- Keywords: autoresearch, Memory v5, PlannerIR, ExecutionTrace, memorycompiler
- Source: verified-by-search
- Updated: 2026-08-20

### Bot 与 IM 网关
- Location: `internal/bot/gateway.go`, `internal/bot/session.go`, `internal/botruntime/runtime.go`, `internal/bot/feishu`, `internal/bot/qq`, `internal/bot/weixin`, `desktop/bot_runtime_app.go`
- Summary: Bot 网关统一管理 QQ、飞书和微信的连接、路由、会话、配对、访问控制、媒体和 Controller 生命周期。
- Keywords: BotGateway, botruntime, QQ, Feishu, Lark, Weixin, pairing, media
- Source: verified-by-search
- Updated: 2026-08-20

### CLI 入口和命令路由
- Location: `cmd/workground2/main.go`, `internal/cli/cli.go`, `internal/cli/desktop.go`, `docs/CLI_DESKTOP.md`
- Summary: CLI 入口和 run、chat、serve、setup、config、mcp、plugin、bot、desktop 等子命令路由位于这些位置。
- Keywords: cli.Run, workground2, desktop status, serve, setup, bot, mcp, plugin
- Source: verified-by-search
- Updated: 2026-08-20

### DecisionBroker 主人决策通道
- Location: `internal/decision`, `desktop/decision_app.go`, `desktop/frontend/src/components/DecisionCenter.tsx`, `desktop/decision_skill`, `docs/DECISION_BROKER_DESIGN.zh-CN.md`
- Summary: 应用级决策通道汇聚跨 Workspace 和 Agent 的 ask/notify，处理桌面与微信抢答、持久化、审计和重试投递。
- Keywords: DecisionBroker, Owner Inbox, Ask, Notify, Weixin, resolve, defer
- Source: verified-by-search
- Updated: 2026-08-20

### Desktop AI 协作与外部派发
- Location: `desktop/ai_collaboration_app.go`, `desktop/ai_collaboration_skill`, `desktop/remote_api.go`, `desktop/tabs.go`, `docs/CLI_DESKTOP.md`
- Summary: Desktop 对外提供异步 Session 派发、状态轮询、交互回答和内嵌 workground2-worker 协作包。
- Keywords: AI Collaboration, workground2-worker, desktop new, SessionID, pendingInteraction, PollOnly
- Source: verified-by-search
- Updated: 2026-08-20

### Desktop Session 管理
- Location: `desktop/tabs.go`, `desktop/remote_api.go`, `desktop/frontend/src/components/ProjectTree.tsx`, `desktop/frontend/src/store/composerQueue.ts`, `desktop/frontend/src/lib/useController.ts`
- Summary: Desktop Session 的创建、恢复、来源标识、切换保活、普通消息队列和默认模型回退集中在这些位置。
- Keywords: WorkspaceTab, sessionSource, detachedSessions, composerQueue, default_model, session switch
- Source: verified-by-search
- Updated: 2026-08-20

### Draw AddOn 画图工具
- Location: `pkg/drawaddon`, `desktop/draw_addon_app.go`, `internal/boot/boot.go`, `docs/addons/draw-addon-design.md`, `D:\Work\wg2addons\draw-tool`
- Summary: 画图配置和宿主接口位于主仓库，模型侧 draw_image 由外部 draw-tool AddOn 的 MCP runtime 提供。
- Keywords: DrawAddon, draw-tool, draw_image, image generation, MCP runtime
- Source: verified-by-search
- Updated: 2026-08-20

### HTTP Serve 前端
- Location: `internal/serve/serve.go`, `internal/serve/index.html`
- Summary: Serve 包把 Controller 暴露为 HTTP/SSE 接口，提供事件流和提交、取消、审批、回滚、Session 等端点。
- Keywords: serve, SSE, /events, /submit, /approve, browser UI
- Source: verified-by-search
- Updated: 2026-08-20

### MCP 插件系统
- Location: `internal/plugin/plugin.go`, `internal/plugin`, `cmd/workground2-plugin-example/main.go`, `docs/PLUGIN_PACKAGES.md`
- Summary: MCP 客户端支持 stdio、HTTP 和 SSE 传输，并把外部工具适配进 Tool Registry。
- Keywords: MCP, JSON-RPC, stdio, streamable-http, mcp__, tools/list
- Source: verified-by-search
- Updated: 2026-08-20

### Memory 与 Skill 系统
- Location: `internal/memory/memory.go`, `internal/skill`, `internal/agent/protected.go`, `internal/cli/skill_picker_view.go`, `internal/skillshare/remote.go`
- Summary: Memory 发现项目记忆，Skill 负责发现和运行 playbook；protected 路径处理受保护内容的读取、保存和泄漏拦截。
- Keywords: Memory, WorkGround2.md, AGENTS.md, Skill, SKILL.md, protected skill, FlowSkillShare
- Source: verified-by-search
- Updated: 2026-08-20

### Pin Memory Sidebar
- Location: `internal/control/pinned_memory.go`, `internal/store/session.go`, `desktop/frontend/src/components/Message.tsx`, `desktop/frontend/src/components/WorkspacePanel.tsx`
- Summary: 会话级钉选记忆通过 sidecar 持久化，并从消息和助手结论进入右侧 Workspace Sidebar。
- Keywords: pin memory, pinned memory, sidebar, compaction, pinned-memo sidecar
- Source: verified-by-search
- Updated: 2026-08-20

### Provider 模型与接入
- Location: `internal/provider`, `internal/config/config.go`, `internal/config/load.go`, `desktop/onboarding_cli.go`, `desktop/settings_app.go`, `desktop/frontend/src/components/OnboardingOverlay.tsx`
- Summary: Provider 接口、OpenAI/Anthropic/CLI 后端、模型解析、首次接入和本地 CLI 探测集中在这些位置。
- Keywords: Provider, ResolveModel, OpenAI, Anthropic, local CLI, onboarding, provider_access
- Source: verified-by-search
- Updated: 2026-08-20

### Room Agent 执行
- Location: `desktop/collab_agent.go`, `desktop/collab_app.go`, `desktop/frontend/src/collab/useCollabController.ts`, `desktop/frontend/src/collab/CollaborationWorkspace.tsx`
- Summary: Room 内 Personal Agent 的触发去重、审批、等待回答、持久队列、当前运行和幂等停止位于这些位置。
- Keywords: Room, Personal Agent, mention, approval mode, queuedTasks, currentRun, stop
- Source: verified-by-search
- Updated: 2026-08-20

### Room 文件传输
- Location: `desktop/collab_file_transfer.go`, `desktop/collab_relay_file.go`, `desktop/collab_relay_crypto.go`, `desktop/collab_transport.go`, `desktop/frontend/src/collab/components/CollaborationTimeline.tsx`
- Summary: Room 文件发送、Relay 接收、自动落盘、哈希校验、失败恢复和图片预览集中在这些位置。
- Keywords: Room, file transfer, Relay, auto receive, attachments, SHA, image preview
- Source: verified-by-search
- Updated: 2026-08-20

### Room 协作运行时
- Location: `desktop/collab_app.go`, `desktop/collab_persist.go`, `internal/collab/store.go`, `desktop/frontend/src/collab`, `desktop/tabs.go`
- Summary: Room 的 Topic、成员、Timeline、持久化、Session 绑定、重启恢复和前端工作区集中在这些位置。
- Keywords: Room, collaboration, Topic, timeline, member, persistence, Session
- Source: verified-by-search
- Updated: 2026-08-20

### Room 邀请与连接路由
- Location: `desktop/collab_app.go`, `desktop/collab_relay_host.go`, `desktop/frontend/src/collab/invite.ts`, `desktop/frontend/src/collab/transport.ts`
- Summary: Room Host 的地址枚举、LAN/Relay 路由选择、邀请编码和连接传输入口位于这些位置。
- Keywords: Room, invite, CollaborationInvite, Relay, LAN, Host
- Source: verified-by-search
- Updated: 2026-08-20

### Session 持久化、后台任务与回滚
- Location: `internal/store/session.go`, `internal/agent/save.go`, `internal/agent/session_lease.go`, `internal/checkpoint`, `internal/jobs`, `internal/tool/builtin/bgjobs.go`, `internal/control/checkpoint.go`
- Summary: Session sidecar、租约和冲突恢复、Checkpoint 回滚以及跨 Turn 后台任务集中在这些位置。
- Keywords: session jsonl, sidecar, lease, recovery, checkpoint, rewind, background job, wait
- Source: verified-by-search
- Updated: 2026-08-20

### Tool 注册表与内置工具
- Location: `internal/tool/tool.go`, `internal/tool/builtin`, `docs/TOOL_CONTRACT.md`
- Summary: Tool 接口、能力扩展、运行期 Registry 以及文件、Shell、搜索等内置工具位于这些位置。
- Keywords: Tool, Registry, ReadOnly, Previewer, builtin, bash, read_file, edit_file
- Source: verified-by-search
- Updated: 2026-08-20

### Wails 桌面端
- Location: `desktop/main.go`, `desktop/app.go`, `desktop/wails.json`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/App.tsx`
- Summary: Desktop 是独立 Go Module 的 Wails Shell，Go App 暴露绑定，React 通过 bridge 调用并订阅事件。
- Keywords: Wails, Desktop, App, WorkspaceTab, bridge.ts, agent:event, React
- Source: verified-by-search
- Updated: 2026-08-20

### Web 站点与 Cloudflare Workers
- Location: `site/src/pages`, `site/package.json`, `workers/accounts`, `workers/registry`, `workers/forum`, `workers/crash-report`
- Summary: Site 是 Astro 官网和社区前端，Workers 提供账号、注册表、论坛和崩溃报告服务。
- Keywords: Astro, Cloudflare Workers, Hono, D1, accounts, registry, forum, crash-report
- Source: verified-by-search
- Updated: 2026-08-20

### Work Block 与修订
- Location: `internal/work/block_schema.go`, `internal/work/service.go`, `internal/work/collaboration_recovery.go`, `desktop/frontend/src/components/work/WorkCardFront.tsx`, `desktop/frontend/src/work/controller.ts`
- Summary: Work Block 的 schema、迁移、布局、提交、权威 revision 和并发修订恢复集中在这些位置。
- Keywords: Work Block, BlockSchemaRegistry, Placement, SubmitV2Input, revision, conflict
- Source: verified-by-search
- Updated: 2026-08-20

### Work 能力路由与预执行
- Location: `internal/control/taskexec.go`, `internal/work/ports.go`, `internal/work/scheduler_v2.go`, `internal/agent/request_help.go`
- Summary: Work Task 的必需能力检查、网页搜索预取和结构化产物 preflight 通过 request_help 路径执行。
- Keywords: RequiredCapabilities, web_search, SlotPreflight, CapabilityProducer, request_help
- Source: verified-by-search
- Updated: 2026-08-20

### Work 输入与调度
- Location: `internal/work/input_service.go`, `internal/work/v2_coordinator.go`, `internal/work/patch_service.go`, `internal/work/discussion_block.go`, `desktop/frontend/src/work/components/presentation/WorkInformationPanel.tsx`
- Summary: Work 的类型化输入、自动启动、DAG 调度、未物化任务调整和补丁恢复集中在这些位置。
- Keywords: Work information, readyForStart, scheduler, PreviewWorkPatch, ApplyWorkPatch, V2TaskRuntime
- Source: verified-by-search
- Updated: 2026-08-20

### Work 系统
- Location: `internal/work`, `internal/control/work.go`, `desktop/works.go`, `desktop/work_chat.go`, `desktop/frontend/src/components/work`, `docs/WORK_COLLABORATION_WORKBENCH_V2.zh-CN.md`
- Summary: Work 的规划、Definition、DAG Run、Task、Artifact、讨论补丁、模型路由和桌面交互入口位于这些位置。
- Keywords: Work, Work V2, Definition, Run, Task, Artifact, planner_model, TaskExecutor
- Source: verified-by-search
- Updated: 2026-08-20

### 会话控制器
- Location: `internal/control/controller.go`, `internal/control`, `desktop/frontend/src/lib/useController.ts`
- Summary: Controller 是各前端共享的会话驱动，统一 Send、Cancel、Approve、Plan、Compact、Checkpoint、Goal 和运行状态事件。
- Keywords: Controller, Send, Cancel, Approve, PlanMode, RuntimeStatus, typed event stream
- Source: verified-by-search
- Updated: 2026-08-20

### 共享启动装配
- Location: `internal/boot/boot.go`
- Summary: boot.Build 把配置解析为共享 Controller，并装配 Provider、Tool、Plugin、Permission、Memory、Skill 和 Job。
- Keywords: boot.Build, Controller, provider, tool registry, plugin, memory, skills, jobs
- Source: verified-by-search
- Updated: 2026-08-20

### 能力求助路由
- Location: `internal/config/assist.go`, `internal/config/capability.go`, `internal/agent/request_help.go`, `internal/agent/assist_artifact.go`, `desktop/frontend/src/components/RequestHelpCard.tsx`
- Summary: 主模型缺少搜索、画图或视觉能力时，request_help 负责候选模型路由、能力探测、执行和产物校验。
- Keywords: capability assist, request_help, web_search, image_generation, vision, assist_models
- Source: verified-by-search
- Updated: 2026-08-20

### 配置加载与模型解析
- Location: `internal/config/config.go`, `internal/config/load.go`, `WorkGround2.example.toml`, `docs/CONFIG_PATHS.md`, `docs/GUIDE.md`
- Summary: Config 处理 CLI、项目、用户和默认配置优先级，以及 Provider、Model、Desktop、Tool、Permission、Sandbox、Plugin 和 Skill 配置。
- Keywords: Config, WorkGround2.toml, ResolveModel, ProviderEntry, DesktopConfig, ToolsConfig
- Source: verified-by-search
- Updated: 2026-08-20

### 权限与沙盒
- Location: `internal/permission/permission.go`, `internal/sandbox/sandbox.go`, `internal/tool/builtin`
- Summary: Permission 负责 allow/ask/deny 决策，Sandbox 负责 Shell 和文件工具的读取、写入及网络边界。
- Keywords: Policy, Gate, allow, ask, deny, sandbox, bash, write roots
- Source: verified-by-search
- Updated: 2026-08-20

### 图标小组件
- Location: `desktop/widget_icon_mode.go`, `desktop/widget_room_pins.go`, `desktop/frontend/src/components/widget/DesktopIconMode.tsx`, `desktop/frontend/src/components/widget/roomsManager.ts`, `desktop/frontend/src/components/widget/roomNotifications.ts`, `internal/unread`, `desktop/frontend/src/components/widget/desktop-icon-mode.css`
- Summary: 图标小组件集中管理任务、Workspace 和 Room 图标及弹层；Room 子链路负责独立 Pin、自定义图标、常驻/未读强显、数字或逐条提醒与 @ 分类。
- Keywords: DesktopIconMode, task icon, Workspace, Room, RoomsManager, roomNotifications, room pins, unread
- Source: verified-by-search
- Updated: 2026-08-21

### 图标小组件快速新建
- Location: `desktop/widget_conversation.go`, `desktop/widget_icon_mode.go`, `desktop/frontend/src/components/widget/widgetQuickStartJobs.ts`, `desktop/frontend/src/components/widget/quickStartPreferences.ts`, `desktop/frontend/src/components/widget/DesktopIconMode.tsx`
- Summary: QuickStart 负责带 Prompt 的后台投递；独立图标入口可随时按所选 Workspace 幂等创建普通空白 Session、退出小组件并聚焦主窗口。
- Keywords: QuickStart, StartWidgetConversation, OpenWidgetWorkspace, requestId, optimistic ledger, workspace open
- Source: verified-by-search
- Updated: 2026-08-23

### 未读数据层（Room / IM）
- Location: `internal/unread`, `internal/bot/gateway.go`, `desktop/unread_app.go`, `desktop/collab_app.go`, `desktop/bot_runtime_app.go`
- Summary: 统一未读仓库负责 Room 和 IM 的幂等入站、摘要、单调已读游标和 unread:state 事件。
- Keywords: unread, Room, IM, AcceptInbound, MarkUnreadRead, UnreadState
- Source: verified-by-search
- Updated: 2026-08-20

### 性能诊断
- Location: `desktop/frontend/src/lib/crash.ts`, `desktop/frontend/src/__tests__/crash-reporting.test.ts`
- Summary: Desktop 前端监控长任务、事件循环延迟和堆压力，并节流写入 Wails warning 日志。
- Keywords: performance.longtask, PerformancePressure, recordPerformanceLog, LogWarning
- Source: verified-by-search
- Updated: 2026-08-20

### 构建、桌面打包与 npm 分发
- Location: `Makefile`, `desktop/wails.json`, `desktop/README.md`, `npm/WorkGround2/package.json`, `npm/build.mjs`, `scripts/desktop-build.sh`
- Summary: 根构建生成 CLI，Wails 配置生成桌面应用，npm 脚本生成多平台预编译 CLI 包。
- Keywords: make build, go build, wails build, npm, release, WorkGround2.exe
- Source: verified-by-search
- Updated: 2026-08-20

### 桌面内置图标资源
- Location: `desktop/frontend/src/lib/projectIcons.ts`, `desktop/frontend/src/components/widget/WorkspaceMatteIcon.tsx`, `desktop/frontend/src/assets/workspace-icons-matte-v1`, `desktop/frontend/src/assets/agent-icon`, `desktop/frontend/src/lib/agentIcon/assets.ts`
- Summary: Workspace/Room 使用 94 枚哑光 PNG 图标（原有 34 枚，新增 60 枚）并保留 5 个 Lucide 兼容键；Agent 图标由 manifest 驱动的边框、头饰、眼睛状态和任务工具图层组合，`workspace-icons-v1` 与 `workspace-icons-final` 当前无代码引用。
- Keywords: projectIcons, WorkspaceMatteIcon, workspace-icons-matte-v1, agent-icon, agentManifest, 内置图标
- Source: verified-by-search
- Updated: 2026-08-26

### 桌面显示缩放
- Location: `desktop/zoom_factor.go`, `desktop/zoom_runtime_windows.go`, `desktop/main.go`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/dpiScale.ts`, `desktop/frontend/src/components/widget`
- Summary: Windows 显示缩放经 WebView2 Controller 即时应用并原子持久化，失败回滚；设置页以后端有效值为可信源，Widget/Icon 模式按缩放事件补偿逻辑坐标。
- Keywords: 显示缩放, DesktopZoomFactor, WebView2, ZoomFactor, dpiScale
- Source: verified-by-search
- Updated: 2026-09-03

### 桌面设置
- Location: `desktop/settings_app.go`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/locales`, `internal/config/edit.go`, `internal/config/render.go`
- Summary: Desktop 设置的 Go 绑定、React 面板、多语言文案和配置读写入口位于这些位置。
- Keywords: SettingsPanel, settings, provider, workbench, localization, config edit
- Source: verified-by-search
- Updated: 2026-08-20

### 桌面小组件模式
- Location: `desktop/widget_mode.go`, `desktop/widget_window_windows.go`, `desktop/widget_conversation.go`, `desktop/frontend/src/components/widget`, `desktop/frontend/src/assets/widget-mode`, `docs/desktop/widget-mode-design.md`
- Summary: Desktop 主窗口与小组件形态切换、窗口几何、任务路由、皮肤和 Windows 任务栏行为集中在这些位置。
- Keywords: widget mode, window transition, widget skin, geometry, taskbar, pager
- Source: verified-by-search
- Updated: 2026-08-20

### 桌面小组件委托分类
- Location: `desktop/widget_mode.go`, `desktop/widget_icon_mode.go`, `internal/control/controller.go`, `desktop/widget_icon_mode_test.go`
- Summary: 委托仅聚合真实 RunningSubagents 与 CLI/外部派发；BackgroundOnly 只表示普通会话仍有后台 Job，并保留为自己的运行任务图标。
- Keywords: 委托, widgetDelegations, BackgroundOnly, RunningSubagents, fixed:delegate
- Source: verified-by-search
- Updated: 2026-08-23

### 项目说明与工程约定
- Location: `README.md`, `README.zh-CN.md`, `docs/SPEC.md`, `AGENTS.md`
- Summary: README 说明产品定位和用法，SPEC 定义工程合同，AGENTS.md 保存项目级协作约定。
- Keywords: WorkGround2, README, SPEC, AGENTS.md, project memory
- Source: verified-by-search
- Updated: 2026-08-20

### 助手模式
- Location: `internal/assistant`, `internal/assistantdaemon`, `internal/tool/sessiontool`, `internal/tool/assistanttool`, `desktop/assistant_app.go`, `desktop/assistant_runner.go`, `desktop/assistant_session_control.go`, `desktop/frontend/src/custom/features/assistant`, `docs/ASSISTANT_MODE_DESIGN.zh-CN.md`
- Summary: 长期 Assistant 以 Plan、Routine、Memory、Policy 和唯一主管 Session 持续推进使命；所有新执行统一成为带所有者、用途、父级和责任引用的普通 Session。主管循环合并用户输入、Routine、重试和 Session 生命周期事件，通过持久 checkpoint/receipt 串行决策，支持自动代答、隔离实验、研究扩展、完成回写与全局暂停恢复。Desktop 与 daemon 共用控制面；UI 只展示和提交权威状态，旧 Run/RunnerJob 仅作历史兼容。
- Keywords: AssistantWorkspace, AssistantRuntime, SupervisorSession, ManagedSession, SessionControl, SessionPurpose, SessionStatus, SupervisorCycle, SupervisorEventQueue, RecordSessionTranscript, Plan, Routine, Memory, Policy, Experiment, Research, AutoAnswer, WorkControl, Viewport, session_list, session_status, session_read
- Source: verified-by-search
- Updated: 2026-08-28
