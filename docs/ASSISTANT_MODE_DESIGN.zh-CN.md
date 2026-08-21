# WorkGround2 助理模式设计

> 状态：阶段 1～3 已实现，阶段 4 暂不实施
>
> 分支：`developping/assistant-mode+2026-08-17`
>
> 参考视觉：用户提供的 1487×1058 Desktop 深色时间线设计图
>
> 实现范围：阶段 1、2、3；阶段 4 暂不实施

## 1. 功能定义

助理是长期存在的业务对象。它拥有一个长期使命或若干日常目标、自己的运行频率、显式可编辑记忆、权限边界和可审计运行历史。Agent 只负责一次 Run 内的推理与工具调用，不承担助理身份和长期状态。

一句话模型：

```text
Assistant = Mission + Routines + Memory + Policy + Runs
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

### 2.1 代码项目助理

- 定期扫描项目近期修改、测试、构建产物和发布条件。
- 记住上次失败原因、已验证步骤和项目特有约束。
- 到达发布条件时进入待处理状态，询问是否发布。
- 用户可以随时“让它继续工作”或临时调整下一次检查时间。

### 2.2 推广助理

- 围绕长期推广使命执行多个日常 Routine。
- 记录渠道、内容、回复、效果和下一轮策略。
- 对外发帖、私信、删除、付费等动作进入持久审批。
- 阶段 3 只完成模型、收件箱和扩展接口；真正外部渠道接入属于阶段 4，不在本次范围。

## 3. 设计原则

1. **长期对象、短期运行**：每次触发创建独立 Run 和 Session，禁止无限膨胀的永久会话。
2. **状态单源**：Assistant Store 是使命、Routine、记忆、运行和审批的权威来源；UI 只展示和提交意图。
3. **幂等可恢复**：调度 occurrence、立即运行、重试和审批都使用稳定请求 ID。
4. **失败显式**：失败进入 `retry_wait`、`waiting_attention` 或 `failed`，保存错误和下一步，不只写日志。
5. **权限不自增**：助理可以建议修改频率或权限，不能自行扩大权限。
6. **缓存稳定**：助理动态上下文在 Run 创建后注入 Session，不修改稳定 system-prompt 前缀。
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

同一助理默认最多一个活动 Run。新的定时 occurrence 合并为一个待执行 Run；手动点击在相同 request ID 下幂等。

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
| 对外发帖、回复、私信 | 必须审批 |
| 删除、付费、凭据、隐私数据 | 必须逐次审批 |

MCP 工具在阶段 2 统一视为外部边界：`network=deny` 时拒绝，其余网络策略下逐次审批。即使工具声明 `readOnly`，该声明也不能证明它不会把项目内容发送到外部服务；后续只有在引入可信 MCP 能力元数据后，才可对明确的本地只读工具放宽。

审批是持久 `AttentionItem`，关联 Assistant、Run、动作、摘要和恢复 token。应用重启后仍可批准、拒绝或取消。

## 5. 存储与恢复

建议目录：

```text
<WorkGround2 user state>/assistants/
  index.json
  <assistant-id>/
    assistant.json
    routines.json
    memory.json
    runs/
      <run-id>.json
    attention.json
    events.jsonl
```

要求：

- JSON 快照使用临时文件 + rename 原子替换。
- `events.jsonl` 仅作审计，不作为主要查询状态。
- Store 内部以 revision 做比较交换，拒绝迟到更新。
- occurrence key 使用 `assistantId/routineId/scheduledFor` 确定性生成。
- Run 领取写入租约；启动时回收过期 `running`，进入 `queued` 或 `waiting_attention`。
- 同 request ID 创建、立即运行、转换 Heartbeat 均返回同一结果。

## 6. 执行流程

1. Scheduler 计算到期 Routine，并创建或复用 occurrence。
2. Runner 原子领取 queued Run，写入 lease。
3. Desktop 宿主创建后台 Topic/Session，但不改变用户当前活动页。
4. 组装动态上下文：使命、Routine、记忆快照、最近结果、权限和当前时间。
5. 通过现有 Controller 提交普通用户 Turn；需要长推进时使用 Goal 能力。
6. Run 完成后记录摘要、错误、Session 引用和 MemoryPatch。
7. Store 原子提交结果；失败根据错误分类进入 retry 或 attention。
8. UI 订阅快照变化，不由网络回包直接操作 Panel。

阶段 1 的 Runner 提供接口和可恢复状态；Desktop Session 执行适配在阶段 2 接通。

## 7. Desktop UI

### 7.1 参考图视觉语言

参考图采用 1487×1058 Desktop 画布，关键比例与元素如下：

- 左侧栏约 282px，深黑背景和细右边界。
- 品牌区、快速入口、项目树和底部设置沿用现有 Workbench 侧栏。
- 项目展开后增加“助理”节点；当前节点使用低饱和紫色选中底和左侧紫色指示线。
- 主区使用黑灰纸张质感和高留白，不增加卡片网格。
- 顶部显示助理名、绿色状态点和“让它继续工作”橙色动作。
- 日期使用大号衬线标题；时间轴由时间、橙色节点、细虚线和编辑式正文组成。
- 关键结论使用大号衬线文字，正文使用较低对比度无衬线文字。
- 计划节点显示可直接点击的“改成别的时间”。
- 底部交办入口使用一条弱分割线和单行输入提示。

实现遵循现有主题 token、侧栏、图标库和窗口结构。参考图没有独立位图内容，因此无需生成新 raster 资产。

### 7.2 信息架构

新增一级 `AssistantWorkspace`：

- 左侧项目树：展示项目绑定助理；全局助理进入全局分组。
- 时间线首页：今日已做、学到的记忆、下一次计划和快速交办。
- 管理抽屉：概览、例行任务、记忆、运行记录、权限。
- 创建/编辑向导：模板、使命、项目、Routine、频率、权限确认。
- 待处理收件箱：审批、连续失败、缺少用户输入。

高频操作不进入深层设置：

- 每个 Routine 都有“立即运行”。
- 时间线上直接修改下一次运行时间。
- 助理顶部直接暂停/唤醒。
- 运行结果可跳转到原 Session。

### 7.3 必须工作的交互

- 创建代码项目助理和通用助理。
- 编辑使命、Routine、频率和记忆。
- 暂停、恢复、立即运行和取消 Run。
- 查看运行历史并打开关联 Session。
- 批准、拒绝和重试 AttentionItem。
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
6. 旧 Heartbeat Panel 保留兼容入口，并引导到助理工作区。

## 9. 模板

### 9.1 代码项目助理

- Mission：持续关注项目健康度和发布准备情况。
- Routine：修改扫描、测试/构建状态、发布准备检查。
- 默认外部发布必须审批。

### 9.2 通用助理

- Mission 和 Routine 由用户填写。
- 默认只读，写入与网络权限显式选择。

推广助理模板可以在阶段 3 展示，但外部渠道连接和自动效果采集明确标记为阶段 4 能力。

## 10. 分期与验收

### 阶段 1：核心

- `internal/assistant` 模型、校验、文件 Store、Scheduler、Run lease/retry/recovery。
- 幂等创建、更新、立即运行和 occurrence。
- 单助理单飞、错过合并、失败显式状态。
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

### 阶段 4：本次不做

- 外部论坛/社区连接器。
- 推广效果自动采集与优化闭环。
- 无 UI 常驻 daemon、系统服务和多进程 leader 选举。
- 云端 24×7 执行。

## 11. 风险与约束

- Desktop 关闭时阶段 1～3 不执行任务；重启后按 `coalesce_latest` 补一次。
- 长时间 Run 必须持有可续租 lease，应用退出后由恢复逻辑接管。
- Assistant 动态上下文不能进入稳定 system prompt 前缀。
- Heartbeat 当前默认 `yolo`，转换时必须映射并提示用户；外部发布仍升级为强制审批。
- UI 时间线是运行事实的投影，不能用乐观状态冒充已执行结果。
- 项目路径变化或不存在时进入 `waiting_attention`，保留修复和重新绑定入口。

## 12. 主要代码位置

```text
internal/assistant/                     核心领域、Store、Scheduler、Runner
desktop/assistant_app.go                Desktop 宿主与 Wails API
desktop/assistant_runner.go             Controller/Session 执行适配
desktop/frontend/src/assistant/         助理工作区、抽屉、编辑器、Store
desktop/frontend/src/App.tsx            一级入口和表面切换
desktop/heartbeat.go                    兼容与转换来源
Codex/KnowledgeBase/FeatureMap.md        功能状态
```
