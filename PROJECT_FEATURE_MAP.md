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

### DecisionBroker 全局主人决策通道
- Location: `docs/DECISION_BROKER_DESIGN.zh-CN.md`, `internal/decision`, `desktop/decision_app.go`, `desktop/frontend/src/App.tsx`, `desktop/frontend/src/styles.css`, `desktop/frontend/src/components/DecisionCenter.tsx`, `desktop/frontend/src/components/SettingsPanel.tsx`, `desktop/decision_skill`
- Summary: 状态 `done`，分支 `developping/decision-entry-sidebar+2026-08-16`；应用级 Broker 汇聚跨 workspace/Agent 的长期人类决策，统一桌面与微信抢答、全局串行、静默策略、原子持久化、审计和可重试投递；已连接 Bot 可一键设为问答通道；`ask` 进入全局问答队列，`notify` 直接进入历史并复用持久 outbox，不占用问答槽；微信用户侧隐藏 Decision ID、命令协议和原始远端 ID；遮挡内容的右下角固定入口已移到侧边栏收起按钮旁，以纯图标打开主人决策。
- Keywords: DecisionBroker, Owner Inbox, Decision Center, human decision, Ask, Notify, Weixin, resolve, defer, waiting_decision, decision skill
- Source: user-requested+verified-by-search
- Updated: 2026-08-16

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

### Desktop 外部 Session 模型选择
- Location: `desktop/remote_api.go`, `desktop/tabs.go`, `internal/config/load.go`, `internal/config/config.go`, `internal/boot/boot.go`
- Summary: `/api/v1/session/new` 不接收 model；新外部 Session 以 workspace 合并配置的 `default_model` 启动，恢复既有 Session 时优先使用可解析的持久化模型，失效引用按有效默认模型和首个已配置 Provider 回退。
- Keywords: desktop new, external session, default_model, LoadForRoot, ResolveModelWithFallback, BranchMeta
- Source: verified-by-search
- Updated: 2026-08-12

### Desktop 异步派发握手
- Location: `desktop/remote_api.go`, `desktop/tabs.go`, `desktop/app.go`, `desktop/ai_collaboration_skill/scripts/dispatch.ps1`
- Summary: 外部 Session 创建后立即返回可查询的 starting SessionID；启动期任务持久排队，Controller Ready 后幂等重放；Worker 派发与 PollOnly 拆成短命令快照。
- Keywords: desktop new, starting, pendingRemoteInput, SessionID, PollOnly, dispatch.ps1
- Source: verified-by-search
- Updated: 2026-08-03

### Desktop 普通 Session 消息队列
- Location: `desktop/frontend/src/store/composerQueue.ts`, `desktop/frontend/src/components/Composer.tsx`, `desktop/frontend/src/App.tsx`, `desktop/frontend/src/lib/useController.ts`, `desktop/frontend/src/components/desktop-ui/IrisInfoComponents.tsx`
- Summary: 普通 Session 运行中消息按 session 入队，Controller 空闲且无决策门控时 FIFO 自动提交；后端拒绝时保留错误并支持重试。
- Keywords: ordinary session, composerQueue, QueueTray, sendToTabConfirmed, FIFO, retry
- Source: verified-by-search
- Updated: 2026-08-13

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

### LLM 后台任务等待
- Location: `internal/tool/builtin/bgjobs.go`, `internal/jobs/jobs.go`, `internal/agent/agent.go`
- Summary: 模型通过结构化 `wait` 工具调用等待当前 Session 的后台 job；Job 完成关闭 done channel，等待结果写回工具消息后 Agent 继续下一轮模型请求，纯文本“等待”不会自动续跑。
- Keywords: wait, waitJob, WaitForSession, background job, done channel, tool loop
- Source: verified-by-search
- Updated: 2026-08-12

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

### Room @Agent 自动触发去重
- Location: `desktop/frontend/src/collab/state.ts`, `desktop/frontend/src/collab/useCollabController.ts`, `desktop/frontend/src/__tests__/collaboration.test.tsx`
- Summary: 带问号的显式 `@成员/@Agent` 消息只由 mention 链路启动一次本机 Agent；自动回答问题扫描识别结构化目标 ID 并跳过，避免同一消息生成运行中与排队中两个任务。
- Keywords: Room, Agent, mention, autoRespondQuestions, mentionMemberIds, mentionAgentIds, dedupe
- Source: user-stated+verified-by-search
- Updated: 2026-08-04

### Room Agent 审批模式与确认详情
- Location: `desktop/collab_app.go`, `desktop/collab_agent.go`, `desktop/frontend/src/collab/CollaborationWorkspace.tsx`, `desktop/frontend/src/collab/components/CollaborationTimeline.tsx`
- Summary: “我的 Agent”可切换普通 Session 同源的 ask/auto/yolo 工具审批模式；待处理 Approval/Ask 只投影到主人本机，工具审批显示工具、对象和原因，Agent 提问显示真实问题与选项并可直接作答。
- Keywords: Room, Agent, approval mode, PendingInteraction, ApprovalModal, AskCard, local-only prompt
- Source: user-stated+verified-by-search
- Updated: 2026-08-04

### Room Agent 等待确认决策
- Location: `desktop/collab_app.go`, `desktop/collab_agent.go`, `desktop/frontend/src/collab/components/CollaborationTimeline.tsx`, `desktop/frontend/src/collab/useCollabController.ts`
- Summary: waiting_approval 卡片按所属 Session/Run ID 直接回答当前 Controller pending interaction；同意或拒绝均续接原执行且不创建新的 Agent 排队任务。
- Keywords: Room, Agent, waiting_approval, RespondCollaborationAgentRun, PendingInteraction, 同意, 拒绝
- Source: verified-by-search
- Updated: 2026-08-04

### Room Agent 运行动态浮窗
- Location: `desktop/frontend/src/collab/CollaborationWorkspace.tsx`, `desktop/frontend/src/collab/agentActivity.ts`, `desktop/frontend/src/collab/components/AgentActivityPopover.tsx`, `desktop/frontend/src/collab/collab.css`
- Summary: 成员列表的运行中状态从 Room timeline 按成员 ID 派生最近 Agent 输入、运行摘要和输出，并以可悬停/聚焦的 Portal 走马灯浮层展示。
- Keywords: Room, Agent, running, hover, activity popover, marquee, timeline
- Source: user-stated+verified-by-search
- Updated: 2026-08-04

### Room Host Session 重启可见性
- Location: `internal/agent/save.go`, `desktop/collab_app.go`, `desktop/tabs.go`, `desktop/collab_session_test.go`
- Summary: 已绑定 Room 的 collaboration Session 即使本地对话轮次仍为 0，也作为持久业务 Session 进入 Session List；普通空白 Session 继续隐藏，Host Room 在进程重启且未恢复原 tab 时仍可从原 Workspace/topic 找回。
- Keywords: Room, Host, restart, Session List, collaboration Session, empty transcript
- Source: verified-by-search
- Updated: 2026-08-04

### Room Relay-only 文件接收恢复
- Location: `desktop/collab_transport.go`, `desktop/collab_relay_host.go`, `desktop/collab_relay_file.go`, `desktop/collab_file_transfer.go`
- Summary: Relay-only Host 不再把 typed-nil HTTP 文件通道误装入 fallback；文件下载 Panic 会记录栈、隔离为可手动重试的失败，并可靠释放自动接收锁与并发槽，避免持久化传输让 Desktop 启动循环崩溃。
- Keywords: Room, Relay-only, auto receive, typed nil, fallback, panic recovery, waiting_sender
- Source: user-stated+verified-by-reproduction
- Updated: 2026-08-11

### Room 主人命令授权边界
- Location: `desktop/frontend/src/collab/useCollabController.ts`, `desktop/frontend/src/__tests__/collaboration.test.tsx`, `desktop/collab_agent.go`
- Summary: 主人命令本身直接启动；后续工具权限遵循同一 Session 的 ask/auto/yolo 策略。自动响应任务可在单次运行临时启用 scoped auto，完成后恢复原策略且不覆盖主人主动修改。
- Keywords: Room, Agent, owner command, scoped auto approval, startAgent, tool approval mode
- Source: user-stated+verified-by-search
- Updated: 2026-08-04

### Room 导出连接路由选择
- Location: `desktop/collab_app.go`, `desktop/frontend/src/collab/CollaborationWorkspace.tsx`, `desktop/frontend/src/collab/invite.ts`
- Summary: Room Host 导出连接的地址枚举、Relay/LAN 路由选择和邀请字符串编码入口。
- Keywords: Room, export connection, CollaborationInvite, Relay, LAN, inviteString
- Source: verified-by-search
- Updated: 2026-08-10

### Room 小文件自动接收与图片直显
- Location: `desktop/collab_file_transfer.go`, `desktop/collab_relay_file.go`, `desktop/collab_relay_crypto.go`, `desktop/collab_agent.go`, `desktop/frontend/src/collab/components/CollaborationTimeline.tsx`, `desktop/frontend/src/__tests__/collaboration.test.tsx`
- Summary: Room 中严格小于 1 MiB 的他人文件按 Session workspace 和可信 Room 实例自动接收到 .workground2/attachments/room，完成 SHA 校验后可供 Agent 以相对 @ 路径引用；静态图片经内容校验后在文件卡内有界懒加载直显。
- Keywords: Room, auto receive, attachments, PreviewCollaborationFile, roomAttachmentRefs, Relay HostKey, CollaborationTimeline
- Source: verified-by-search
- Updated: 2026-08-07

### Room 我的 Agent 任务队列
- Location: `desktop/collab_app.go`, `desktop/collab_agent.go`, `desktop/collab_persist.go`, `internal/collab/store.go`, `desktop/frontend/src/collab`
- Summary: Desktop 协作运行时持久化最多 20 个 Personal Agent 等待任务；统一就绪唤醒在 Controller 空闲、重连和重启恢复后幂等续跑，Room 状态从全部活跃 Run 投影，主人可关闭排队项。
- Keywords: Room, Personal Agent, 任务队列, queuedTasks, queueWaiting, activeAgentStatus, CancelCollaborationQueuedTask
- Source: verified-by-search
- Updated: 2026-08-04

### Room 我的 Agent 当前运行与停止
- Location: `desktop/collab_app.go`, `desktop/collab_agent.go`, `desktop/frontend/src/collab/CollaborationWorkspace.tsx`, `desktop/frontend/src/collab/useCollabController.ts`, `desktop/frontend/src/collab/transport.ts`, `desktop/frontend/src/collab/collab.css`
- Summary: “我的 Agent”面板常驻显示当前本地 Run 的运行、等待确认和停止中阶段，以及指令、可公开进度、开始时间和同 Session 队列数；停止按 Session/Run ID 精确取消所属 Controller，重复调用幂等，失败回滚可重试，完成后发布 cancelled 并继续队列。
- Keywords: Room, Personal Agent, currentRun, stopping, StopCollaborationAgentRun, idempotent cancel, queue continuation
- Source: user-stated+verified-by-tests
- Updated: 2026-08-05

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

### Work 信息完成后倒计时启动
- Location: `internal/work/input_service.go`, `internal/work/v2_coordinator.go`, `desktop/works.go`, `desktop/frontend/src/work/components/presentation/WorkAutoStartCountdown.tsx`, `desktop/frontend/src/work/components/presentation/WorkInformationPanel.tsx`, `desktop/frontend/src/work/components/presentation/WorkInformationPanel.css`, `design-qa.md`
- Summary: 状态 done；工作信息全部填写后在原标题行显示 20 秒自动开始倒计时，支持暂停、继续、打开信息自动暂停和立即开始。最终值先持久化为 readyForStart 草稿，全部释放后统一恢复调度，重启与重复请求均可安全恢复。
- Keywords: Work information, countdown, auto start, pause, readyForStart, deferStart, scheduling recovery
- Source: verified-by-search
- Updated: 2026-08-02

### Work 搜索硬能力自动预取
- Location: `internal/control/taskexec.go`, `internal/control/taskexec_test.go`
- Summary: TaskExecutor 对非原生且无直接搜索工具的 web_search 要求，在主模型运行前经标准 request_help 路径预取并把客观回执留在 Task Session；web_fetch 与纯 URL 不计作搜索成功。
- Keywords: RequiredCapabilities, web_search, request_help, completion gate, taskSuccessfulCapabilities
- Source: verified-by-search
- Updated: 2026-07-30

### Work 未物化任务调整恢复
- Location: `internal/work/discussion_block.go`, `internal/work/patch_service.go`, `internal/work/patch_service_test.go`
- Summary: PreviewWorkPatch 和 ApplyWorkPatch 对尚未物化 V2TaskRuntime 的 Definition 节点使用当前 Run 的稳定 Task ID 精确解析，并保持 Block 绑定、伪造身份拒绝与幂等回放。
- Keywords: PreviewWorkPatch, ApplyWorkPatch, DeriveTaskID, pending node, V2TaskRuntime
- Source: verified-by-search
- Updated: 2026-08-01

### Work 模式模型路由
- Location: `internal/boot/boot.go`, `WorkGround2.toml`, `desktop/work_chat.go`
- Summary: Work Session、结构规划、输入推断、Patch 规划和任务执行均由 Controller 启动时解析的模型 Provider 驱动；结构规划/输入推断在配置独立 planner_model 时改用 Planner，其余仍用执行模型。
- Keywords: Work mode, default_model, planner_model, workDefinitionProv, execProv, TaskExecutorAdapter
- Source: verified-by-search
- Updated: 2026-08-17

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

### 性能压力仅写日志
- Location: `desktop/frontend/src/lib/crash.ts`, `desktop/frontend/src/__tests__/crash-reporting.test.ts`
- Summary: 桌面前端继续监控长任务、事件循环延迟和 JS 堆压力，但不再显示报告弹窗；诊断按 10 分钟节流写入 Wails warning 日志，失败回退 console.warn。
- Keywords: performance.longtask, PerformancePressure, recordPerformanceLog, LogWarning
- Source: verified-by-search
- Updated: 2026-08-07

### 总体未读数据层（Room / IM）
- Location: `internal/unread`, `internal/bot/gateway.go`, `desktop/unread_app.go`, `desktop/collab_app.go`, `desktop/collab_transport.go`, `desktop/bot_runtime_app.go`
- Summary: 统一未读仓库位于 internal/unread；Room 通过 Snapshot Sequence 投影，Desktop IM 在网关处理前持久化并按 MessageID 去重，后端提供 Summary、单调已读游标和 unread:state 事件。
- Keywords: unread, Room, IM, AcceptInbound, MarkUnreadRead, UnreadState
- Source: verified-by-search
- Updated: 2026-08-08

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

### 项目说明与工程约定
- Location: `README.md`, `README.zh-CN.md`, `docs/SPEC.md`, `WorkGround2.md`
- Summary: README 说明产品定位和用法，SPEC 是工程合同，WorkGround2.md 是本项目会话常驻工程记忆。
- Keywords: WorkGround2, SPEC, WorkGround2.md, project memory
- Source: verified-by-search
- Updated: 2026-07-03
