# WorkGround2 助手模式设计

> 状态：阶段 1～5 已实现并合入 `main`；阶段 6（角色化调度、反思与脑洞）已在本分支实现。
>
> 来源分支：`developping/assistant-improvement-proposals+2026-08-26`
>
> 参考视觉：用户提供的 1487×1058 Desktop 深色时间线设计图
>
> 实现范围：阶段 1～6

## 1. 功能定义

助手是长期存在的业务对象。它拥有一个长期使命或若干日常目标、自己的运行频率、显式可编辑记忆、权限边界和可审计运行历史。Agent 只负责一次 Run 内的推理与工具调用，不承担助手身份和长期状态。

一句话模型：

```text
Assistant = Mission + Plan + Routines + Memory + Policy + Runs
```

与现有概念的边界：

| 概念 | 生命周期 | 主要职责 |
|---|---|---|
| Agent | 单次执行 | 推理、工具调用、生成结果 |
| Goal | 单个 Session | 自动推进一个目标至完成、阻塞或停止 |
| Work | 一次结构化工作 | DAG、输入、成果槽和运行记录 |
| Heartbeat | 重复 Prompt | 旧版 Desktop 定时触发入口 |
| Assistant | 长期存在 | 决定何时运行、记住什么、持续优化什么 |

## 2. 用户价值与典型场景

### 2.1 代码项目助手

- 定期扫描项目近期修改、测试、构建产物和发布条件。
- 记住上次失败原因、已验证步骤和项目特有约束。
- 到达发布条件时进入待处理状态，询问是否发布。
- 用户可以随时“让它继续工作”或临时调整下一次检查时间。

### 2.2 推广助手

- 围绕长期推广使命执行多个日常 Routine。
- 记录渠道、内容、回复、效果和下一轮策略。
- 对外发帖、私信、删除、付费等动作进入持久审批。
- 阶段 4 已接通首个真实外部渠道；阶段 5 把效果复盘收敛为可审批、可审计的配置改进提案。

## 3. 设计原则

1. **长期对象、短期运行**：每次触发创建独立 Run 和 Session，禁止无限膨胀的永久会话。
2. **状态单源**：Assistant Store 是使命、Routine、记忆、运行和审批的权威来源；UI 只展示和提交意图。
3. **幂等可恢复**：调度 occurrence、立即运行、重试和审批都使用稳定请求 ID。
4. **失败显式**：失败进入 `retry_wait`、`waiting_attention` 或 `failed`，保存错误和下一步，不只写日志。
5. **权限不自增**：助手可以建议修改频率或权限，不能自行扩大权限。
6. **缓存稳定**：助手动态上下文在 Run 创建后注入 Session，不修改稳定 system-prompt 前缀。
7. **Controller-first**：Assistant Service 不依赖 Desktop；Desktop 只是首个宿主和 UI 适配器。

## 4. 核心模型

### 4.1 Assistant

```go
type Assistant struct {
    ID            string
    Name          string
    Description   string
    Mission       string
    Scope         Scope
    WorkspaceRoot string
    Lifecycle     Lifecycle
    Policy        Policy
    MemoryRev     int64
    Revision      int64
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

`Lifecycle` 只描述持久生命周期：`active`、`paused`、`archived`。运行中、待审批和故障属于派生状态，避免多个字段互相打架。

### 4.2 Routine

```go
type Routine struct {
    ID          string
    AssistantID string
    Title       string
    Prompt      string
    Schedule    Schedule
    Enabled     bool
    CatchUp     CatchUpPolicy
    Revision    int64
}
```

第一版支持：

- 手动执行。
- 固定间隔。
- 每日、每周、双周、每月、每年固定时间。
- 时区和可选运行时间窗口。
- 默认 `coalesce_latest`：离线错过多个周期时只补一次。

日历边界采用固定语义，避免依赖 `time.AddDate` 的隐式归一化：

- 所有 occurrence 以 UTC instant 持久化，Schedule 同时保留 IANA timezone。
- 夏令时跳过的本地时间移动到缺口后的第一个有效 instant；重复的本地时间只取第一次。
- 每月 29/30/31 在目标月份不存在时收敛到该月最后一天。
- 每年 2 月 29 日在非闰年收敛到 2 月最后一天。
- 跨午夜时间窗口合法，例如 `22:00-06:00`；落在窗口外的 interval 推迟到下一个窗口起点。
- interval 使用上一次计划 occurrence 推进，避免执行耗时造成持续漂移。
- timezone 无效时拒绝保存，不静默回退本机时区。

### 4.3 Run

```go
type Run struct {
    ID            string
    AssistantID   string
    RoutineID     string
    Prompt        string // 冻结的 Routine prompt 或直接用户输入原文
    RequestID     string
    OccurrenceKey string
    Trigger       TriggerKind
    State         RunState
    Attempt       int
    SessionPath   string
    LeaseOwner    string
    LeaseUntil    time.Time
    ScheduledFor  time.Time
    RetryAt       time.Time
    StartedAt     time.Time
    FinishedAt    time.Time
    Summary       string
    Error         *RunError
}
```

状态机：

```text
queued -> running -> succeeded
                 -> waiting_approval -> running
                 -> retry_wait -> queued
                 -> waiting_attention
                 -> failed / cancelled
```

同一助手默认最多一个活动 Run。新的定时 occurrence 合并为一个待执行 Run；手动点击在相同 request ID 下幂等。

直接用户输入（“对助手说”）不创建或覆盖 Routine，而是生成一条 `TriggerManual + RoutineID="" + Prompt=<原文>` 的 Run：Run 是输入、状态、Session 与结果的单一可信记录。原文可为任务、指导、批评/反馈或工作方法改进，一律按原文保存、不自动改写或美化；`RoutineID` 与直接原文同时提供会被拒绝，原文 trim 后不能为空且受 UTF-8 字节上限约束。指纹包含规范化原文，同 requestId + 同原文幂等返回同 Run，同 requestId + 不同原文显式冲突。

### 4.4 显式记忆

记忆分为：

- `charter`：使命、约束、不可自行修改的用户决定。
- `facts`：带来源和时间的事实。
- `strategy`：有效方法、无效尝试和效果结论。
- `open_loops`：开放事项、等待输入和下一步。
- `metrics`：可量化效果；阶段 3 只提供通用记录结构。

每条记忆包含稳定 ID、类型、正文、来源 Run、证据、锁定状态、版本和时间。AI 产生的是 `MemoryPatch`；Store 以 `expectedRevision` 原子提交，冲突时重新读取并安全重试。

### 4.5 Policy 与审批

权限按动作类型声明：

| 动作 | 默认策略 |
|---|---|
| 读取项目、分析历史 | 自动允许 |
| 限定 Workspace 的本地写入 | 使用 Assistant 配置 |
| 网络查询 | 使用 Assistant 配置 |
| 对外发帖、回复、私信 | 使用 Assistant 的 `publish` 三态配置；`allow` 自动执行，`approve` 逐次审批，`deny` 拒绝 |
| 删除、付费、凭据、隐私数据 | 必须逐次审批 |
| Assistant 记忆读取/写入（`memory` / `remember` / `forget`） | 自动允许 |
| 项目 Skill 安装（`install_source`） | 仅 `local_write=allow` 且 `network=allow` 时自动允许；任一拒绝则拒绝，其余逐次审批 |

内建记忆工具 `memory`（只读检索）、`remember`、`forget` 在 Assistant Session 中始终自动执行：它们写入的是 Assistant 绑定项目的受控、版本化 memory store，而不是任意文件写入，因此即使 `local_write=deny/approve` 也保持允许，不再生成 `approve_tool:remember` / `approve_tool:forget` 人工待办。Controller 只在当前 turn 的冻结 Policy 没有显式 Allow 时为普通交互 Session 补记忆 Ask 规则，不能再覆盖 Assistant 的显式 Allow；普通 Session 的默认审批保持不变。Runtime 启动及每次调度还会幂等批准并恢复旧版本遗留的记忆工具待办，响应丢失后可安全重放；发布则严格遵循冻结的 `publish` 三态。工具调用与结果事件、失败显式暴露和现有 memory queue 行为保持不变。

`bash` 是一个完整 shell，权限跟随 `local_write` 三态：`allow` 时自动执行（含只读命令与普通构建/测试命令，不做命令内容白名单），`deny` 时拒绝且不触发审批，`approve` 时保持逐次审批。浏览器工具从当前 page revision 的真实元素语义分类：普通点击/输入随 `network`，发帖、回复、评论、提交、点赞等外部可见动作同时受 `publish` 控制；删除、付费、密码/凭据和私有字段继续逐次审批或拒绝。`browser_type` 的权限类别由目标元素计算，不能由模型在参数里自报。`move_file` 与 MCP 等既有敏感边界保持审批或拒绝。

MCP 工具在阶段 2 统一视为外部边界：`network=deny` 时拒绝，其余网络策略下逐次审批。即使工具声明 `readOnly`，该声明也不能证明它不会把项目内容发送到外部服务；后续只有在引入可信 MCP 能力元数据后，才可对明确的本地只读工具放宽。

创建向导可选“先学习一下再干”，默认选中。该选择本身是对首个学习 Run 的明确授权：创建时将 `local_write` 与 `network` 显式设为 `allow` 并要求用户确认权限摘要；取消选择则保留模板原策略。学习过程仅可安装项目级 Skill，不得自行安装 MCP、插件、可执行文件、link/register 或高风险来源。

审批是持久 `AttentionItem`，关联 Assistant、Run、动作、摘要和恢复 token。应用重启后仍可批准、拒绝或取消。

### 4.6 计划与责任图

Assistant 级 Plan 是长期使命下的责任图（responsibility graph）。它随每次成功 Run 通过 `<assistant-progress>` 块推进，只依赖 store 的原子提交，不依赖 UI 或 Session 时序。

```go
type Plan struct {
    Revision         int64            // 乐观并发守卫：进度块必须携带它看到的 revision
    Responsibilities []Responsibility
}

type Responsibility struct {
    ID, AssistantID string
    Alias           string             // 稳定的模型可见别名，块协议用别名引用，不回显随机 ID
    Objective       string
    DoneCriteria    string             // 完成标准
    NextAction      string             // 下一步
    Status          ResponsibilityStatus // blocked / ready / active / done / failed
    DependsOn       []string           // 责任 ID
    BlockReason     string             // 阻塞原因（依赖未完成时派生）
    Revision        int64
    CreatedAt, UpdatedAt time.Time
}
```

`Artifact` 与 `Opportunity` 是责任推进的持久证据与提案，都绑定 Assistant + Responsibility + 来源 Run：

```go
type Artifact struct {
    ID, AssistantID, RespID, RunID string
    Title, Kind, Content, Evidence string
}
type Opportunity struct {
    ID, AssistantID, RespID, RunID string
    Reason                         string
}
```

进度块是受限、结构化的助手→计划协议。模型在成功回合的结尾发出一个或多个 `<assistant-progress>` JSON 块；Runner 解析、脱敏并原子应用：

```json
{
  "plan_revision": 3,
  "responsibility": "code-review",
  "responsibilities": [
    {"alias": "fix-tests", "objective": "…", "done_criteria": "…", "next_action": "…", "depends_on": ["scan"]}
  ],
  "complete": ["scan"],
  "active": ["fix-tests"],
  "artifacts": [{"resp": "scan", "title": "…", "kind": "report", "content": "…", "evidence": "…"}],
  "opportunities": [{"resp": "fix-tests", "reason": "…"}]
}
```

规则：

- 职责用稳定 alias 引用；重声明同一 alias 且目标一致是幂等 no-op，目标不一致是冲突。
- `depends_on` 用 alias；省略表示不变，`[]` 表示清空。同一块内允许前向引用与「下游+上游同时完成」，自依赖与环被拒绝。
- 完成与激活对顺序不敏感：同一块内同时完成下游与上游会被接受；依赖变更会双向重算 readiness（blocked ↔ ready），未完成依赖时 `complete`/`active` 被拒绝，已 done 的责任不允许新增未完成依赖。
- 进度元数据不拖垮 Run：stale `plan_revision` 以最新 Plan 有界 rebase（最多 3 次）；已存在 alias 的 objective 以当前 Plan 为权威、忽略模型重复声明的不同 objective，该声明中的合法 done/next/depends_on 仍按最新 Plan 应用。解析失败或仍无法应用的 malformed / cycle / missing alias / blocked 补丁记录可观察 diagnostic 后丢弃，Run 以同一 summary/session 成功落盘，计划不被半修改，既有 active 责任留待后续 Run 自动继续。只有连「无 progress 的 Run 成功落盘」也失败时才沿用显式失败/重试路径。

`CompleteRunWithProgress` 是唯一收敛的进度写入：以调用方 request ID/指纹幂等，做环校验、依赖重算、stale revision 拒绝，并把「Run 完成 + 计划/证据变化」连同内嵌 request receipt 一起写入单一 `aggregate.json` 的原子替换（临时文件 + rename），要么全部提交要么全部不提交，崩溃后重放返回原结果。旧快照在读取时惰性归一化为空计划，无需迁移。

### 4.7 持续改进提案

阶段 5 将“根据效果不断改进”建模为持久 `ChangeProposal`，而非让模型直接改运行配置：

```go
type ChangeProposal struct {
    ID, AssistantID, RunID string
    TargetKind             ProposalTarget // routine / channel
    TargetID               string
    BaseRevision           int64
    Routine                *RoutineProposal
    Channel                *ChannelProposal
    Summary, Reason        string
    Evidence               []string
    State                  ProposalState // pending / applied / rejected / superseded
    Resolution             string
    Revision               int64
    CreatedAt, UpdatedAt   time.Time
}

type RoutineProposal struct {
    Prompt   *string
    Schedule *Schedule
    Enabled  *bool
}

type ChannelProposal struct {
    CollectIntervalSeconds *int64
    Enabled                *bool
}
```

模型在成功 Run 的 `<assistant-progress>` 中声明 `proposals`。Store 在同一次 `CompleteRunWithProgress` 原子提交中解析目标、冻结 `BaseRevision` 与变更前值，并生成由来源 Run、序号和内容指纹决定的稳定 ID。重复提交不产生重复提案。提案只接受上下文中真实存在、属于当前 Assistant 的 Routine 或 Channel ID；单条提案只能修改一种目标，空补丁、无变化补丁和越界值直接拒绝，避免把自然语言当作隐式配置协议。

用户处理提案使用独立 request ID 和 proposal revision：

- **接受**：目标 revision 仍等于 `BaseRevision` 时，在一个聚合原子写入中应用完整补丁并把提案置为 `applied`；响应丢失后可重放同一 receipt。若 revision 只因调度进度或其它未触及字段变化，但提案涉及的字段仍等于冻结基线，则允许按字段兼容合并，保留其它新值。
- **目标已达到建议值**：即使 revision 已变化，也按幂等成功收敛为 `applied`，不重复修改目标。
- **目标发生冲突变化**：不覆盖用户的新配置，将提案置为 `superseded` 并保存显式原因；后续 Run 可基于新快照提出新提案。
- **拒绝**：只关闭提案并记录用户说明，不修改目标。

阶段 5 的提案边界只覆盖 Routine 的 Prompt / Schedule / Enabled 和 Channel 的采集间隔 / Enabled。Mission、Policy、Workspace、渠道地址和凭据均不可通过提案修改；尤其权限不能由助手提案或批准链路自行扩大。策略文字仍进入显式 `strategy` / `metrics` 记忆，避免再造一套泛化规则引擎。

## 5. 存储与恢复

实际存储是每个 Assistant 一个聚合文件，没有独立 journal：

```text
<WorkGround2 user state>/assistants/
  <assistant-id>/
    aggregate.json
```

要求：

- 每个 Assistant 的全部状态（Assistant、Routine、Memory、Run、Attention、Plan、Artifact、Opportunity、ChangeProposal）连同 request receipt 一起保存在单一 `aggregate.json` 中。
- 每次变更使用临时文件 + rename 原子替换 `aggregate.json`；request receipt 内嵌其中，崩溃后重放返回原结果，不另设 journal。
- Store 内部以 revision 做比较交换，拒绝迟到更新。
- occurrence key 使用 `assistantId/routineId/scheduledFor` 确定性生成。
- Run 领取写入租约；启动时回收过期 `running`，进入 `queued` 或 `waiting_attention`。
- 同 request ID 创建、立即运行、转换 Heartbeat 均返回同一结果。
- 创建请求可携带 `InitialPrompt`；Assistant、Routine 与首个 queued Run 在一次聚合原子写入中提交，重复请求返回同一 Run，参数变化返回幂等冲突，不会产生“助手已创建但首个任务丢失”的半完成状态。

## 6. 执行流程

1. Scheduler 计算到期 Routine，并创建或复用 occurrence。
2. Runner 原子领取 queued Run，写入 lease。
3. Desktop 宿主创建后台 Topic/Session，但不改变用户当前活动页。
4. 组装动态上下文：使命、Routine、记忆快照、当前责任图和确定性的 ready/active 责任、权限和当前时间。
5. 通过现有 Controller 提交普通用户 Turn；需要长推进时使用 Goal 能力。
6. 成功后解析并脱敏 `<assistant-progress>` 块，用 `CompleteRunWithProgress` 原子提交 Run 结果 + 计划/证据/改进提案变化；stale `plan_revision` 或 alias/objective 冲突以最新 Plan 有界 rebase 重试，仍无法应用的进度元数据记录 diagnostic 后丢弃、Run 照常成功落盘。
7. Store 原子提交结果；其它失败根据错误分类进入 retry 或 attention。
8. UI 订阅快照变化，不由网络回包直接操作 Panel。

阶段 1 的 Runner 提供接口和可恢复状态；Desktop Session 执行适配在阶段 2 接通。

### 6.1 Assistant 执行档案（profile）

每次 Assistant Run 使用独立的 `SessionKind=assistant` 会话身份，持久化在会话 sidecar 的 `SessionKind` 与 `AssistantID`，随保存/重载存活；`boot.Build` 据此使用专门的 Assistant 稳定 system prompt（长期 outcome executor）。普通 / Work / Collaboration 会话保持原有 coding-agent 行为。

- 硬性能力从冻结的 Run mission + prompt 确定性派生（`assistant.RequiredCapabilities`）。命名 URL/域名或明确要求在线检查网站的任务派生 `live_web`。
- “先学习一下再干”派生 `skill_learning`：必须同时取得实时 Web 成功证据和 `install_source` 成功计划/应用证据。Runner 要有界搜索 2–5 个候选，比较来源、时效、适配度与风险，安全时安装并验证项目 Skill，用 `remember` 记录来源、名称、路径和结果；没有合适候选时记录检索范围与判断后结束本轮，禁止无限学习。
- Assistant Session 不使用 coding Session 的三工具 Anchored Bootstrap；首轮直接暴露完整工具目录，因此浏览器配置启用且使用默认 full token profile 时，`browser_*` 工具从第一次模型请求即可见。
- 每个 Assistant Run 使用 `ToolApprovalAuto`：权限策略允许的可恢复操作直接执行，fallback 尽量自动放行；显式 Ask/Deny 规则与高风险业务边界继续请求人工确认或拒绝，不升级为 YOLO。
- `live_web` 只由成功的实时网页/浏览器工具结果满足（browser open/navigate/state 或 web fetch/search 等）；只 dispatch 或失败结果不算。
- 接受 TurnDone 为成功前，先校验必需能力证据；缺失证据通过 `Failure{code:"evidence_missing"}` 进入可重试、可观察的恢复状态，绝不记为成功 Run。网络 deny、工具不可用、模型跳过必需工具同理。
- 责任图全部 done 且无 ready/active 时，提示开启新的 2–4 项责任周期；旧 done 项保留为历史，不重开、不修改。
- 直接输入的 Run 使用“本次用户输入（原文）”语义：是任务就执行，是指导或反馈就据此调整计划/策略，不要求用户把输入改写成任务。
- 每次 Run 注入近期直接输入 Run 的有界历史（稳定倒序、限制条数与 UTF-8 总字节），包含原文、状态与结果摘要，并排除当前及其后入队的 Run；历史里已完成的任务不得仅因被引用而重复执行。Routine prompt 不会当作用户直接输入。

## 7. Desktop UI

### 7.1 参考图视觉语言

参考图采用 1487×1058 Desktop 画布，关键比例与元素如下：

- 左侧栏约 282px，深黑背景和细右边界。
- 品牌区、快速入口、项目树和底部设置沿用现有 Workbench 侧栏。
- 项目展开后增加“助手”节点；当前节点使用低饱和紫色选中底和左侧紫色指示线。
- 主区使用黑灰纸张质感和高留白，不增加卡片网格。
- 顶部显示助手名、绿色状态点和“让它继续工作”橙色动作。
- 日期使用大号衬线标题；时间轴由时间、橙色节点、细虚线和编辑式正文组成。
- 关键结论使用大号衬线文字，正文使用较低对比度无衬线文字。
- 计划节点显示可直接点击的“改成别的时间”。
- 顶部“对助手说”入口使用一条弱分割线和单行输入提示，明确显示“输入会被记录”。

实现遵循现有主题 token、侧栏、图标库和窗口结构。参考图没有独立位图内容，因此无需生成新 raster 资产。

### 7.2 信息架构

新增一级 `AssistantWorkspace`：

- 左侧项目树：展示项目绑定助手；全局助手进入全局分组。
- 时间线首页：今日已做、学到的记忆、下一次计划和“对助手说”快速入口。
- 管理抽屉：概览、责任计划、例行任务、记忆、渠道、改进建议、运行记录、权限与待处理。
- 创建/编辑向导：模板、使命、项目、Routine、频率、可选首个“先学习一下再干”任务、权限确认。
- 待处理收件箱：审批、连续失败、缺少用户输入。

高频操作不进入深层设置：

- 每个 Routine 都有“立即运行”。
- 时间线上直接修改下一次运行时间。
- 助手顶部直接暂停/唤醒。
- 运行结果可跳转到原 Session。

### 7.3 必须工作的交互

- 创建代码项目助手和通用助手。
- 编辑使命、Routine、频率和记忆。
- 暂停、恢复、立即运行和取消 Run。
- 查看运行历史并打开关联 Session。
- 时间线与运行记录对直接输入显示完整原文（安全纯文本、保留换行），结果摘要仍独立按 Markdown 渲染。
- 时间线与运行记录复用普通会话 Markdown 图片链路：远程图片可直接预览、点击放大，尺寸受正文列约束；无法访问的图片显式显示失败占位。结果中的“取证证据”“证据”“说明”“来源”等独立 Markdown 章节默认折叠，结论正文保持展开，代码围栏内的同名文本不参与折叠。
- 时间线 Run 标题表达实际工作内容：直接输入取规范化原文的有界摘要，Routine Run 取 Routine 名称，continue-mission 使用明确意图回退；运行状态只由相邻徽标表达，避免“本次运行正在工作/失败/已完成”占用标题。
- 批准、拒绝和重试 AttentionItem。
- 查看待处理与历史改进提案，比较变更前后值，接受或拒绝；待处理数量在助手顶栏和管理导航保持一致。
- 时间线自动刷新，迟到旧 revision 不覆盖新状态。
- 窄屏下侧栏可折叠，管理抽屉变为全宽层。

## 8. Heartbeat 兼容与转换

旧 `HeartbeatTask` 继续可读取，避免升级后任务消失。

阶段 3 提供：

1. 列出可转换的 HeartbeatTask。
2. 按 task ID 生成稳定转换 request ID。
3. 每个旧任务转换为一个 Assistant + 一个 Routine，保留标题、Prompt、Scope、Workspace、Schedule、启停状态和 ApprovalMode。
4. 已转换任务写入转换 receipt；重复转换返回原 Assistant。
5. 用户确认转换后禁用旧任务，失败时不禁用，允许重试。
6. 旧 Heartbeat Panel 保留兼容入口，并引导到助手工作区。

## 9. 模板

### 9.1 代码项目助手

- Mission：持续关注项目健康度和发布准备情况。
- Routine：修改扫描、测试/构建状态、发布准备检查。
- 默认外部发布为逐次审批；用户可显式改为自动允许或拒绝。

### 9.2 通用助手

- Mission 和 Routine 由用户填写。
- 默认只读，写入与网络权限显式选择。

推广助手模板在阶段 4 开放，默认创建内容规划、效果复盘和社区回复三个 Routine。模板默认逐次审批外发，用户把 `publish` 改为 `allow` 后可自动发帖/回复；读取公开效果数据按网络权限执行。

## 10. 分期与验收

### 阶段 1：核心

- `internal/assistant` 模型、校验、文件 Store、Scheduler、Run lease/retry/recovery。
- 幂等创建、更新、立即运行和 occurrence。
- 单助手单飞、错过合并、失败显式状态。
- 包级单元测试、并发/race 关键路径测试。

### 阶段 2：Desktop 与 UI

- Desktop Service 生命周期和 Wails API。
- 接通后台 Session Runner。
- 参考图时间线工作区、创建/编辑、改频、记忆、历史。
- 主要交互、TypeScript、CSS、生产构建和真实视觉验收。

### 阶段 3：兼容与收件箱

- Heartbeat 转换 receipt、兼容入口。
- 代码项目/通用模板。
- 持久 Attention Inbox 和恢复动作。
- 转换失败补偿、重复转换和重启恢复测试。

### 阶段 4：推广闭环与常驻宿主

状态：已完成（2026-08-26）。

- 提供类型化 `ChannelBinding`、外发 `ChannelAction`、效果 `ChannelMetric` 和连接器注册表；首个真实连接器为 Discourse，支持创建主题、回复和读取主题浏览/点赞/回复指标。
- Assistant 聚合只保存稳定凭据引用，API Key 写入 WorkGround2 凭据存储；缺少或失效凭据显式进入诊断/待处理，不在日志、Prompt、Run 摘要和聚合文件回显秘密。
- 发布/回复工具遵循冻结的 `publish` 三态。外发前保存 request 指纹与 `executing` receipt；同请求同意图返回原结果，不同意图冲突。网络结果不明时标记 `unknown` 并停止自动重试，人工对账后才能继续。
- 成功外发进入自动采集队列。采集器按 `next_collect_at` 轮询公开指标，原子追加快照并计算相邻增量；失败使用有界退避且可观察，重复 tick 不重复写入同一采集窗口。
- 最新渠道、动作与指标快照进入 Assistant 动态上下文。推广 Routine 必须比较效果、用 `metrics`/`strategy` 显式记忆记录结论，再提出下一轮内容；模型输出不能直接改指标权威数据。
- Desktop 与 `WorkGround2 assistant daemon` 使用同一个可续租 leader lease。只有 leader 调度/领取/采集；follower 保持可观察并周期竞争，lease 过期可接管。Run fence lease 继续作为第二道并发保护；Assistant Store 的每个聚合读改写再使用跨进程 OS 文件锁，允许 follower UI 与 leader 安全地并发保存配置。
- daemon 使用与 Desktop 相同的 Assistant Store、Controller、权限和恢复规则，可通过 `WorkGround2 assistant daemon` 作为本机后台进程或系统服务常驻；`--once` 可执行一次调度/采集/运行检查，不产生第二套状态源。

### 阶段 5：持续改进提案闭环

状态：已完成（2026-08-26）。

- `<assistant-progress>` 增加类型化 `proposals`；成功 Run、Plan 进度和提案在一次聚合写入中提交，解析失败不留下半完成配置。
- 提案只覆盖 Routine Prompt / Schedule / Enabled 与 Channel 采集间隔 / Enabled；Store 捕获基线 revision 和变更前值，禁止修改 Mission、Policy、Workspace、渠道地址或凭据。
- 提供幂等 `ResolveProposal`：接受时以 revision + 触及字段基线做 CAS/兼容合并并原子应用；目标已满足则幂等收敛；目标的同字段被用户改过则显式 `superseded`，不覆盖新配置；拒绝只关闭提案。
- 动态上下文包含现有待处理提案，防止模型重复建议；效果复盘提示要求用指标与证据解释建议，不能宣称未批准的配置已生效。
- Desktop 增加“改进建议”页、待处理计数、前后值对比、接受/拒绝与终态历史；迟到 revision 不覆盖新状态，失败保留可重试入口。
- 单元测试覆盖协议解析、无变化/越界拒绝、原子创建、幂等重放、CAS 接受、已达目标、冲突淘汰、拒绝、重启恢复和 UI 主交互；通过 Go 全量测试/vet、Frontend test/typecheck/build 与 Desktop 构建。

### 阶段 6：角色化调度、反思与脑洞

状态：已实现（2026-08-27，本分支）。

每个 Assistant 固定拥有一个 Dispatcher、最多三个并行 Runner、一个 Reflector 和一个低频 Ideator。角色是持久业务阶段，不依赖某个 Panel 或 Session 的生命周期：

1. 用户直接输入先原子创建 `Dispatch`。Dispatcher 将其分类为 `task`、`question`、`feedback`、`improvement`、`correction` 或 `control`，保存面向用户的一级回复，并创建零到多个具名 Runner Job。提交接口返回完整 Dispatch；失败保留显式可重试状态，不把未分类输入伪装成已执行。
2. Runner Job 冻结输入分类、目标、Runner 名称、权限、Workspace 和 ContextPack revision。不同 Job 可并行，单 Assistant 默认上限为 3；领取、续租、完成、失败和重试继续使用稳定 request ID 与 fence。计划、记忆和其它共享状态只经 Store 的 revision/CAS 入口提交。
3. 全部关联 Job 进入终态后，Reflector 生成一个有界 `ContextPack`：包含结论、证据、失败、可复用策略、未决事项和建议的后续 Runner 上下文。ContextPack 与来源 Dispatch/Job 绑定并原子提交；Runner 只读取适用的 ContextPack，不注入无限原始历史。
4. Ideator 在距上次脑洞至少 7 天或新增 5 个成功任务 Dispatch 后触发，也可由用户手动触发。它可以暂时放下既有策略假设来重新审视使命、目标和路径，但绝不能绕过权限、安全、Workspace、凭据和发布边界。产出保存为 `IdeaProposal`，必须由人类接受或拒绝；接受只转化为策略记忆或新的责任候选，不直接改 Mission、Policy 或执行外部动作。
5. UI 时间线展示 Dispatcher 一级回复、分类、Runner 状态、反思摘要和脑洞待确认项。迟到快照按 revision 丢弃；所有失败均保留重试入口。

验收至少覆盖：分类与幂等重放、同 request ID 冲突、零 Job 反馈输入、多 Job 三并发上限、租约恢复、反思只执行一次、ContextPack 有界与归属过滤、5 次/7 天触发、脑洞接受/拒绝 CAS、权限不可扩大、重启恢复，以及 Desktop/前端主交互。

## 11. 风险与约束

- 没有 Desktop 或本机 daemon 运行时不会执行任务；宿主恢复后按 `coalesce_latest` 补一次。
- 长时间 Run 必须持有可续租 lease，应用退出后由恢复逻辑接管。
- Assistant 动态上下文不能进入稳定 system prompt 前缀。
- Heartbeat 当前默认 `yolo`，转换时必须映射并提示用户；外部发布默认映射为逐次审批，之后可由用户显式改为自动允许或拒绝。
- UI 时间线是运行事实的投影，不能用乐观状态冒充已执行结果。
- 项目路径变化或不存在时进入 `waiting_attention`，保留修复和重新绑定入口。

## 12. 主要代码位置

```text
internal/assistant/                     核心领域、Store、Scheduler、Runner
internal/assistant/plan.go              责任图、进度块与解析/脱敏
internal/assistant/progress.go          CompleteRunWithProgress 与依赖/环校验
internal/assistant/proposal.go          改进提案校验、CAS 处理与目标应用
internal/assistant/dispatch.go          Dispatch / RunnerJob / ContextPack / IdeaProposal 模型与校验
internal/assistant/dispatch_store.go    OpenDispatch / Classify / Fail / Reflect / OpenIdea / ResolveIdea
internal/assistant/job_store.go         Job 领取/续租/完成/失败/取消/重试/恢复（三并发上限）
internal/assistant/dispatcher.go        Dispatcher 经真实模型分类编排（失败显式可重试）
internal/assistant/reflector.go         Reflector 经真实模型 once 反思 + 有界 ContextPack 归属过滤
internal/assistant/ideator.go           Ideator 经真实模型 5 次/7 天门控与手动入口
internal/assistant/role_protocol.go     RoleModel 接口、受限 JSON 协议、严格解析/校验、角色提示词与退避
desktop/assistant_app.go                Desktop 宿主与 Wails API（含 Dispatch/RetryDispatch/Ideate/ResolveIdea/Job）
desktop/assistant_runner.go             Controller/Session 执行适配与进度注入/应用
desktop/assistant_dispatch.go           Desktop 无头 Job 执行、分类/反思/脑洞推进
desktop/frontend/src/custom/features/assistant/  助手工作区、计划/提案页、抽屉、编辑器、时间线
desktop/frontend/src/App.tsx            一级入口和表面切换
desktop/heartbeat.go                    兼容与转换来源
PROJECT_FEATURE_MAP.md                  功能状态
```
