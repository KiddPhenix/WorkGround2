# Project Feature Map

Concise, incremental index of confirmed feature locations in this repository.

## Rules

- Only record user-stated or verified findings.
- Keep entries short and path-focused.
- Update existing entries instead of duplicating them.

## Entries

### 工作信息单项建议
- Status: done
- Branch: `developping/work-input-field-inference+2026-08-01`
- Location: `desktop/frontend/src/work/components/presentation/WorkInformationPanel.tsx`, `desktop/frontend/src/work/components/presentation/WorkDefinitionOverview.tsx`, 对应样式与测试
- Summary: 移除面板级批量推断入口，为定义字段和自定义字段统一增加无文字的小星光图标；单次只生成目标字段的候选草稿，保存前不改原值，并支持独立忙碌、失败与重试。后端定向测试、前端组件 33 项、TypeScript、CSS、Wails 正式构建及正式 EXE 五字段视觉验收通过。
- Keywords: Work information, per-field inference, suggestion, retry
- Source: user-reported+screenshot
- Updated: 2026-08-01

### Session 工作入口文案柔化
- Status: done
- Branch: `developping/work-entry-copy+2026-08-01`
- Location: `desktop/frontend/src/components/desktop-ui/RuntimeConfigBar.tsx`, `desktop/frontend/src/locales/zh.ts`, `desktop/frontend/src/App.tsx`
- Summary: 将偏命令式的“发送为工作”调整为“作为工作开始”，选中后显示“将作为工作发送”，让入口更自然并保持状态明确；Work 合约 37 项、Iris 合约 17 项、TypeScript、Wails 正式构建及正式 EXE 双状态验收通过。
- Keywords: Work entry, copywriting, send as Work, selected state
- Source: user-reported
- Updated: 2026-08-01

### Session 输入区左右留白恢复
- Status: done
- Branch: `developping/composer-horizontal-spacing+2026-08-01`
- Location: `desktop/frontend/src/styles.css`, `desktop/frontend/src/__tests__/app-work-integration.test.tsx`
- Summary: 修复页脚 `display: contents` 插槽导致直接子元素选择器失效的问题，恢复输入框和运行配置栏左右各 48px 留白；Work 合约 37 项、CSS、TypeScript、前端生产构建、Wails 正式构建及正式 EXE 视觉验收通过。
- Keywords: composer, footer dock, horizontal spacing, display contents
- Source: user-reported+screenshot
- Updated: 2026-08-01

### 发送为工作选中态强化
- Status: done
- Branch: `developping/work-send-selected-state+2026-08-01`
- Location: `desktop/frontend/src/components/desktop-ui/RuntimeConfigBar.tsx`, `desktop/frontend/src/styles.css`
- Summary: 将低辨识度的细描边选中态改为实心强调色，并增加勾选图标和“工作模式已选”文案；Work 合约 36 项、TypeScript、CSS、前端生产构建、Wails 正式构建及真实点击视觉验收通过。
- Keywords: send as Work, selected state, visual feedback, accessibility
- Source: user-reported+screenshot
- Updated: 2026-08-01

### Session 发送为工作入口稳定显示
- Status: done
- Branch: `developping/work-send-entry-stability+2026-08-01`
- Location: `desktop/frontend/src/App.tsx`, `desktop/frontend/src/__tests__/app-work-integration.test.tsx`
- Summary: 入口可见性只由普通可写 Session 的真实用户首条消息、后端 blank 和 Work 过渡状态决定；内部 Item、历史恢复、WorkEnabled/WorkCapable 异步结果不再撤销入口。后端探测只传 Tab ID，缺失时用空 ID 活动路由；配置和能力在点击时显式校验。Work 合约 35 项、TypeScript、Wails 正式构建及 27 秒延时实机验收通过。
- Keywords: send as Work, stable visibility, WorkEnabled, first message
- Source: user-reported+verified-by-code-tests-and-running-window
- Updated: 2026-08-01

### Session 发送为工作入口运行时修复
- Status: done
- Branch: `developping/work-send-entry-runtime+2026-08-01`
- Location: `desktop/frontend/src/App.tsx`, `desktop/frontend/src/components/SessionSurface.tsx`, `desktop/frontend/src/components/desktop-ui/RuntimeConfigBar.tsx`, `desktop/tabs.go`
- Summary: 工作台会隐藏 Composer 自带元数据栏，因此入口改由实际可见的 RuntimeConfigBar 承载并复用同一 Work 发送状态；仅在空白普通 Session 的第一句话前显示。恢复链路可用 Session ID 或后端活动 Tab 路由，空白检测读取 Controller 当前路径，CreateWorkSession 保留最终原子校验。Work 合约 34 项、TypeScript、CSS、Go 定向测试、Wails 正式构建和运行窗口可访问性/选中态验收通过。
- Keywords: send as Work, RuntimeConfigBar, first message, restored Session, currentSessionPath, authoritative backend
- Source: user-reported+verified-by-code-tests-and-running-window
- Updated: 2026-08-01

### Session 发送为工作入口常驻
- Status: done
- Branch: `developping/work-send-entry-visibility+2026-08-01`
- Location: `desktop/frontend/src/App.tsx`, `desktop/frontend/src/__tests__/app-work-integration.test.tsx`
- Summary: 空白普通 Session 始终显示“发送为工作”；入口由后端 blank 状态决定，Work 能力在用户选择时按需确认，失败显式提示且不会提前转换 Session。Work 集成契约 31 项、TypeScript、CSS、前端生产构建和 Wails 正式构建通过。
- Keywords: send as Work, blank Session, WorkCapable, first message, visibility
- Source: user-reported+verified-by-code-and-tests
- Updated: 2026-08-01

### Work 后新建 Session 回归修复
- Status: done
- Branch: `developping/session-new-session-regression+2026-08-01`
- Location: `desktop/tabs.go`, `desktop/app_session_dedup_test.go`
- Summary: Work Session 不再参与空白普通 Session 的幂等复用；从 Work 点击新建会话会创建独立的普通 Session，Work 元数据也不再标记为 blank。相关会话去重测试、Go vet、Diff 检查和 Wails 正式构建通过。
- Keywords: EnsureBlankTab, Work Session, new session, blank reuse
- Source: user-reported+verified-by-code-and-tests
- Updated: 2026-08-01

### Work 独立填写信息面板
- Status: done
- Branch: `developping/work-info-entry-panel+2026-07-30`
- Location: `desktop/frontend/src/components/work/WorkCardFront.tsx`, `desktop/frontend/src/work/components/presentation/WorkDefinitionOverview.tsx`, `desktop/frontend/src/work/components/presentation/WorkInformationPanel.tsx`, `desktop/frontend/src/work/controller.ts`, `desktop/frontend/src/work/wailsAdapter.ts`, `internal/work`, `desktop/works.go`
- Summary: 保留两列“工作信息”常驻列表和上层填写面板；已填写项可重新打开修改。用户可新增名称、可选解释、文本或文件内容的自定义工作信息；新增项以带内联规格的 WorkInput 原子持久化，并作为 Work 级上下文供后续任务读取。数字范围继续只给软 warning。
- Keywords: WorkInformationPanel, WorkInputHost, stacked cards, advanced context, file drop, number warning
- Source: user-requested+verified-by-tests-and-production-build
- Updated: 2026-07-31

### Work 任务说明底板中性化
- Status: done
- Branch: `main`（共享脏工作树，未提交）
- Location: `desktop/frontend/src/styles.css`, `desktop/frontend/src/__tests__/work-card.test.tsx`
- Summary: 任务说明卡片、输入框运行态及规划覆盖层改用中性深灰底色，强调色仅用于边框、状态点与轻量光晕。
- Keywords: Work, task prompt, planning overlay, neutral surface, accent
- Source: user-requested+verified-by-tests
- Updated: 2026-07-29

### Work 失败任务成果状态同步
- Status: done
- Branch: `developping/work-failed-artifact-status+2026-07-30`
- Location: `desktop/frontend/src/work/components/v2/ResultShelf.tsx`, `desktop/frontend/src/components/work/WorkCardFront.tsx`, `desktop/frontend/src/components/work/WorkCard.tsx`, `desktop/frontend/src/work/components/v2/ResultShelf.test.tsx`
- Summary: 成果架使用活动 Definition 的生产关系与当前 Run Task 状态修正迟到或缺失的 ArtifactSlot 结算；生产任务失败时，仍处于生成态的成果即时显示失败并保留可重试入口，任务重新运行后恢复生成态。
- Keywords: ResultShelf, ArtifactSlot, failed_retryable, TaskV2View, producesSlotIds
- Source: user-requested+verified-by-tests
- Updated: 2026-07-30

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
- Location: `desktop/ai_collaboration_app.go`, `desktop/ai_collaboration_app_test.go`, `desktop/ai_collaboration_skill/`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/lib/bridge.ts`, `desktop/frontend/src/lib/types.ts`
- Summary: Desktop 内嵌版本化 `workground2-worker` Skill Bundle；复制提示词导出逐字节一致的 `SKILL.md`、`references/cli.md`、完整 `scripts/dispatch.ps1`、manifest 与 SHA-256，自动安装复用同一份内容并只向全局 `AGENTS.md` 写入精简运行规则。更新过程原子落盘、manifest 最后写入，未知或用户修改内容先保存 `.bak.N`，重复执行安全。安装/创建/更新 Skill 和生成设计文件必须由 Codex 直接完成，导出提示词和 Skill 均明确禁止调度 WorkGround2。
- Keywords: AI Collaboration, deterministic skill bundle, workground2-worker, SKILL.md, dispatch.ps1, manifest.json, SHA-256, AICollaborationPrompt, InjectAICollaborationPrompt, SessionID, pendingInteraction, foregroundActive, backgroundOnly
- Source: verified-by-search
- Updated: 2026-07-21

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

### Session 首条消息发送为工作
- Location: `desktop/works.go`, `desktop/frontend/src/App.tsx`, `desktop/frontend/src/components/Composer.tsx`, `desktop/frontend/src/components/work/WorkSessionTransition.tsx`
- Summary: 空白普通 Session 的首条消息可原地转换为 Work；结构规划期间保留 Session 视觉，Definition 应用后以独立动画显现 WorkCard。
- Keywords: send as work, Session, WorkSessionTransition, CreateWorkSession, tabId
- Source: verified-by-search
- Updated: 2026-08-01

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

### Work Block schema 与四槽 Placement
- Location: `internal/work/block_schema.go`, `internal/work/copy_rerun.go`, `desktop/frontend/src/components/work/WorkCardFront.tsx`, `desktop/frontend/src/styles.css`, `docs/WORK_SYSTEM_DESIGN.zh-CN.md`
- Summary: BlockSchemaRegistry 按 kind/version 校验并执行相邻迁移，file_list 支持 v1/v2 与 latest rerun 升级；Desktop 按 attention、primary、secondary、result 四槽和 12 栏 span 响应式布局。
- Keywords: BlockSchemaRegistry, BlockMigration, file_list, Placement, attention, primary, secondary, result, span, WorkCardFront
- Source: verified-by-search
- Updated: 2026-07-27

### Work Block 单次提交与修订链恢复
- Location: `internal/work/service.go`, `internal/work/collaboration_recovery.go`, `desktop/frontend/src/work/controller.ts`, `desktop/frontend/src/work/components/v2/ExpandedBlock.tsx`
- Summary: 输入 Block 串行提交消费最终权威 Work revision，结构改进在活动 Definition 未变化时对并发 revision 进行有界重基。
- Keywords: Work Block, SubmitV2Input, CreateCandidateRevision, revision conflict, single submit
- Source: verified-by-search
- Updated: 2026-07-29

### Work V1/V2 覆盖与兼容
- Location: `docs/WORK_COLLABORATION_WORKBENCH_V2.zh-CN.md`, `internal/work`, `internal/control/work.go`, `desktop/works.go`, `desktop/frontend/src/components/work`, `desktop/frontend/src/work`
- Summary: V2 默认覆盖新建规划、定义激活、DAG 调度、类型化输入、节点/成果重试和讨论补丁；V1 创建/手动运行入口保留为 feature flag 回退，V1 Work/Run/Block/事件/归档仍被 V2 复用。
- Keywords: Work V1, Work V2, collaboration_workbench_v2, BeginWorkPlanning, CreateWork, ApplyDefinition, RunWork, RetryWorkNode
- Source: verified-by-search
- Updated: 2026-07-27

### Work 搜索硬能力自动预取
- Location: `internal/control/taskexec.go`, `internal/control/taskexec_test.go`
- Summary: TaskExecutor 对非原生且无直接搜索工具的 web_search 要求，在主模型运行前经标准 request_help 路径预取并把客观回执留在 Task Session；web_fetch 与纯 URL 不计作搜索成功。
- Keywords: RequiredCapabilities, web_search, request_help, completion gate, taskSuccessfulCapabilities
- Source: verified-by-search
- Updated: 2026-07-30

### Work 结构化产物能力预执行
- Location: `internal/work/ports.go`, `internal/work/scheduler_v2.go`, `internal/agent/agent.go`, `internal/control/controller.go`, `internal/control/taskexec.go`
- Summary: 带能力要求（如 image_generation）的 ArtifactSlot 在 TaskExecutor 主模型运行前，通过标准 Agent 工具路径串行执行 request_help preflight；SlotPreflight 由 ArtifactSlotDef + CapabilityProducer 自动生成，失败结果可观察并允许主模型 fallback；无能力槽保持旧路径。
- Keywords: SlotPreflight, BuildSlotPreflights, CapabilityProducer, preflight, request_help, ExecuteSyntheticToolCall, executeToolCall
- Source: implementation+tests
- Updated: 2026-07-28

### 上游可靠性加固
- Location: `internal/provider/openai`, `internal/agent`, `internal/control`, `internal/skill`, `internal/tool`, `internal/fileutil/encoding`, `internal/config`, `internal/plugin`, `internal/acp`, `internal/boot`, `desktop`
- Summary: 状态 done；分支 `developping/upstream-hardening+2026-07-10`；按行为重实现 DeepSeek reasoning 回放、planner 失败降级/no-op/宿主审批与用户决策、review/子代理只读边界、tab-scoped 工作区、Windows 文本解码、MCP get、ACP 文件/终端协作、plan/location/mode。全仓测试编译、Go vet、受影响核心包实跑通过；Windows 历史 `printf`/长等待测试单列风险。
- Keywords: upstream hardening, reasoning_content, planner fallback, host approval, subagent boundary, read-only review, tab scoped, encoding, mcp get, ACP, file overlay, terminal, plan, mode
- Source: verified-by-search
- Updated: 2026-07-10

### 会话切换保持后台运行
- Location: `desktop/tabs.go`, `desktop/tabs_order_test.go`, `internal/config/cli_capability.go`, `internal/config/cli_capability_windows_test.go`
- Summary: single-surface 会话切换通过 detachedSessions 原地重新挂载同一 Controller 和 SessionID；新 session 的 Codex capability probe 使用 CREATE_NO_WINDOW 并短期缓存失败，避免后台任务失联及 cmd 窗口闪现。
- Keywords: session switch, detachedSessions, openTopicTabWithActivation, keepOnlyVisibleTab, Controller reuse, ProbeCLICapabilities, CREATE_NO_WINDOW
- Source: verified-by-search
- Updated: 2026-07-19

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
- Location: `docs/desktop/widget-mode-design.md`, `desktop/widget_mode.go`, `desktop/widget_window_windows.go`, `desktop/window_state.go`, `desktop/app.go`, `desktop/widget_conversation.go`, `desktop/widget_info.go`, `desktop/widget_mode_test.go`, `desktop/widget_window_windows_test.go`, `desktop/widget_info_test.go`, `desktop/widget_conversation_test.go`, `desktop/frontend/src/assets/widget-mode`, `desktop/frontend/src/assets/widget-mode/skins`, `desktop/frontend/scripts/validate-widget-skins.py`, `desktop/frontend/src/components/widget`, `desktop/frontend/src/__tests__/widget-conversation-retry.test.ts`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/frontend/src/locales`, `desktop/settings_app.go`, `internal/config/config.go`, `internal/config/edit.go`, `internal/config/render.go`, `internal/config/edit_test.go`
- Summary: 状态 done；桌面主窗口可缩为单消息小组件，保留稳定任务、路由、重试、几何恢复和三语交互。视觉层新增 BP机、拍立得、电子宠物、录音机四套完整设备外壳并保留经典皮肤；所有皮肤共用现有窗口画布与交互，通过注册表和九宫格资源切换。设置即时生效并持久化，未知值回退 classic；四套资源已通过尺寸、透明角和九宫格重建校验，前端生产构建、专项测试及 Go vet 通过。
- Keywords: widget mode, widget skin, window transition, pager, instant camera, virtual pet, cassette recorder, nine-slice, widget_skin, info carousel, routeReasonCode, 多语言, BP机, 拍立得, 电子宠物, 录音机, geometry, retry, settings widget tab
- Source: user-requested+design-approved+verified-by-search
- Updated: 2026-07-18

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

### Work 系统 V1 — Desktop 产品闭环
- Status: done
- Location: `internal/config/config.go`, `internal/boot/boot.go`, `internal/control/work.go`, `internal/work/service.go`, `internal/work/copy_rerun.go`, `desktop/works.go`, `desktop/frontend/src/components/work/WorkAvailabilitySurface.tsx`, `desktop/frontend/src/components/work/WorkPage.tsx`, `desktop/frontend/src/components/work/WorkCard.tsx`, `desktop/frontend/src/components/work/WorkCardBack.tsx`, `desktop/frontend/src/components/work/WorkRunEntry.tsx`, `desktop/frontend/src/work/wailsAdapter.ts`, `desktop/frontend/src/work/controller.ts`, `desktop/frontend/src/work/store.ts`, `desktop/frontend/src/lib/bridge.ts`
- Summary: `work.enabled` 缺省开启；生产 WorkPage 从 Registry 读取 Blueprint 并填写 inputs。空 Prompt 在 UI 与 Service 提交事件前双重阻断；Draft 与可编辑 Block 使用 request ID 和 Work revision 持久化。列表覆盖进行中、历史、回收站，支持归档、恢复、删除、复制及原定义重执行；最新定义需要迁移且迁移器缺失时 RerunPlan 显式阻断。事件重放允许历史 writer，当前并发写仍由 OS lease 串行。
- Keywords: WorkPage, Blueprint, UpdateDraft, UpsertBlock, Archive, Restore, Trash, CopyWork, PrepareRerun, ExecuteRerun, request ID, writer lease
- Source: implementation+focused tests+frontend Work suite
- Updated: 2026-07-23

### 项目说明与工程约定
- Location: `README.md`, `README.zh-CN.md`, `docs/SPEC.md`, `WorkGround2.md`
- Summary: README 说明产品定位和用法，SPEC 是工程合同，WorkGround2.md 是本项目会话常驻工程记忆。
- Keywords: WorkGround2, SPEC, WorkGround2.md, project memory
- Source: verified-by-search
- Updated: 2026-07-03
