# WorkGround2 外部 Agent RunHub 设计

## 1. 文档状态

- 状态：设计确认，首阶段实现中
- 分支：`developping/external-agent-runhub+2026-08-20`
- 首个适配目标：DeepSeek Harness（DSH）
- 后续目标：Codex、Claude Code
- DSH 验证基线：`dsh-v0.1.0-rc.8`，commit `141eb6fef83422698aef7a981029e843e8161534`

## 2. 背景与目标

WorkGround2 图标小组件目前主要展示 WorkGround2 自身 Session、Work、Room 和委托状态。用户同时使用 DSH、Codex、Claude Code 等独立 AI 工具时，任务分散在多个界面中，缺少统一的运行状态、待处理提醒和启动入口。

本设计将 WorkGround2 扩展为本地优先的跨 Agent 任务中心：

1. 外部工具主动上报任务生命周期，图标小组件消费统一状态投影。
2. WorkGround2 可以发起外部 Agent 任务，并按适配器能力提供取消、打开、回答或恢复动作。
3. 外部工具独立启动的会话也能以观察模式进入 WorkGround2。
4. 重复启动、重复事件、乱序、迟到数据、Desktop 重启和子进程崩溃均有明确恢复语义。
5. 新增 Codex、Claude Code 适配器时不修改 RunHub 核心状态模型。

## 3. 非目标

首阶段不包含：

- 将不同产品的完整 Transcript 同步进 WorkGround2。
- 保存 reasoning、工具参数、工具结果或模型原始流。
- 模拟适配器尚未支持的审批、恢复、继续对话能力。
- 依赖 DSH 私有 Web RPC 或内部未发布 API。
- 同时完成 DSH、Codex、Claude Code 三个 Managed Runner。
- 让外部 Agent 直接修改图标小组件 UI 状态。

## 4. 核心原则

### 4.1 RunHub 是单一可信源

外部工具只提交启动意图或规范化事件。RunHub 负责持久化、去重、状态归约、能力投影和订阅通知。Desktop、HTTP、CLI、Bot 均消费同一份 Run 投影。

### 4.2 Managed 与 Observed 分离

- `managed`：WorkGround2 创建并拥有进程或协议连接，可提供适配器真实支持的控制动作。
- `observed`：任务由外部工具创建，WorkGround2 只观察；默认仅提供打开来源或查看诊断。

### 4.3 确定性事件负责生命周期

Hook、Observer、SDK 通知和进程退出码负责确定性状态。Skill、`AGENTS.md`、`CLAUDE.md` 等默认行为只补充语义阶段和摘要，不能成为唯一生命周期来源。

### 4.4 终态单调

`succeeded`、`failed`、`cancelled` 等终态不可被重复、乱序或迟到事件改回运行状态。显式 Retry 创建新 attempt 和新 Run，不复活旧终态。

### 4.5 能力驱动操作

UI 只显示适配器声明的动作。缺少协议支持时不显示伪造的 Approve、Resume、Send 或 Cancel。

## 5. 总体架构

```text
Desktop / Widget / HTTP / CLI
              │
              ▼
         RunHub Controller
      ┌────────┼────────┐
      │        │        │
  DSH Runner  Codex   Claude
      │       Runner   Runner
      │
 DSH SDK JSON-RPC
 one process / Run

外部启动的 DSH / Codex / Claude 会话
      │
 Hook / Native Observer
      │
 durable inbox
      └────────────────► RunHub
```

现有 `internal/dshcompat` 继续负责 DSH Bundle Tool 和 DSH Web UI 兼容。RunHub 的外部任务管理放在独立包中，两种 Session 生命周期保持隔离。

## 6. 领域模型

### 6.1 AgentRun

```go
type AgentRun struct {
    ID              RunID
    Source          Source
    NativeSessionID string
    Ownership       Ownership
    Workspace       string
    Title           string
    State           RunState
    Activity        Activity
    ActivityLabel   string
    Summary         string
    Capabilities    Capabilities
    Revision        uint64
    LastSeenAt      time.Time
    CreatedAt       time.Time
    UpdatedAt       time.Time
}
```

### 6.2 RunEvent

```go
type RunEvent struct {
    EventID   EventID
    RunID     RunID
    Source    Source
    NativeSeq string
    OccurredAt time.Time
    Type      EventType
    Payload   EventPayload
}
```

### 6.3 LaunchIntent

```go
type LaunchIntent struct {
    RequestID         string
    RunnerProfileID   string
    Workspace         string
    Prompt            string
    PermissionProfile string
}
```

### 6.4 RunnerBinding

```go
type RunnerBinding struct {
    RunID           RunID
    NativeSessionID string
    ProtocolVersion string
    ProcessRef      string
    Attempt         uint32
}
```

### 6.5 状态枚举

```text
queued → starting → running → waiting_user
                          ├→ succeeded
                          ├→ failed
                          ├→ cancelled
                          └→ interrupted / stale
```

`Activity` 描述运行中的细阶段：`thinking`、`tool`、`responding`、`background`。`ActivityLabel` 可携带经过清洗的工具名或简短阶段说明。

## 7. 公共接口

### 7.1 Runner

```go
type Runner interface {
    Probe(context.Context, Profile) (Capabilities, error)
    Start(context.Context, LaunchRequest, EventSink) (RunnerBinding, error)
    Cancel(context.Context, RunnerBinding) error
    Open(context.Context, RunnerBinding) error
    Recover(context.Context, RunnerBinding) (Observation, error)
}
```

### 7.2 RunHub

- `Launch(request)`：按 `requestId` 幂等创建 Run 和启动意图。
- `Report(event)`：按 `eventId` 去重并归约状态。
- `Action(runId, action)`：根据 Capability 路由到对应 Runner。
- `List(filter)`：返回规范化 RunProjection。
- `Subscribe()`：提供 transport-agnostic 变更流。

写操作返回明确回执：

- `accepted`
- `already_applied`
- `stale`
- `retryable_error`
- `invalid`

## 8. 持久化与恢复

首阶段不新增 SQLite 依赖，使用单写者 RunHub 和原子文件：

```text
%AppData%/WorkGround2/runhub/
  runs/<runId>/meta.json
  runs/<runId>/events.jsonl
  launches/<requestId>.json
  inbox/<eventId>.json
```

约束：

1. 写入使用同目录临时文件和原子替换。
2. 外部 Reporter 先写 durable inbox，Desktop 未运行时不丢事件。
3. RunHub 启动时和运行中持续消费 inbox。
4. `eventId` 去重；相同 `requestId` 始终返回同一个 Run。
5. 每次归约递增 `Revision`，旧 revision 的状态更新返回 `stale`。
6. 终态不可回退。
7. WorkGround2 重启后，无法恢复协议连接的运行中绑定标记为 `interrupted` 或 `stale`。
8. 未知结果不会自动重启，避免重复执行有副作用的任务。
9. 用户 Retry 创建新 RunID，并记录 parent Run 与 attempt。

## 9. DSH rc.8 能力复核

### 9.1 接入方式比较

| 接入方式 | 状态/事件 | 控制能力 | 限制 | 定位 |
|---|---|---|---|---|
| Headless | 最终文本、退出码 | 进程终止 | 单任务，无实时状态 | 诊断与降级 |
| SDK JSON-RPC | 完整 `session.event`、`session.status`、子 Agent 通知 | Prompt、Shutdown | 无单会话 Cancel/Resume/Close | Managed MVP |
| ACP | 已提交回复、Prompt 结算、权限请求 | 新会话、Prompt、Cancel | 无实时工具进度，无 Load/List/Resume | 后续控制面候选 |
| Cordis Observer | 完整 DSH 生命周期 | 观察 | 需要安装原生插件 | Observed 会话 |

### 9.2 rc.8 的变化

- SDK `initialize` 会等待 Loader 插件树就绪，首个 Prompt 可看到已经完成发现的 MCP 工具。
- SDK 公共协议仍为 `initialize`、`session/prompt`、`shutdown` 和四类通知。
- SDK 仍缺少 per-session cancel、close、resume 和协议版本协商。
- ACP 加强 Prompt admission、按 Session 取消、停稳结算和图片输入输出。
- ACP 仍只支持新会话，且只输出 committed message。
- DSH 已提供可直接安装的 Codex 和 Claude Code 子 Agent Provider。
- Codex Provider 固定 `@openai/codex@0.147.0`；Claude Provider 固定 Agent SDK `0.3.220`。
- 产品子 Agent 采用一次 Run 一个进程，支持后台 Job、取消、命名实例、非交互权限模式和安全诊断。
- SDK `subagent.finished` 只报告 in-process child。Codex、Claude Code 等 out-of-process child 需要父任务 Job/Tool 事件或原生 Observer 结算。
- DSH 内部持久化和 cold resume 已增强，公共 SDK/ACP 尚未暴露恢复入口。

## 10. DSH Managed Runner

### 10.1 进程所有权

首版采用一个 WorkGround2 Run 对应一个 DSH SDK 进程：

- 单任务故障隔离。
- 协议缺少 per-session cancel 时，可通过关闭运行时实现精确取消。
- shutdown 先走 JSON-RPC，超时后依次关闭 stdin、终止进程树并强制结束。
- 启动命令使用 argv 数组，不拼接 shell 字符串。
- 记录受限的 stderr tail，用于安全诊断，不写入凭据和完整 Prompt。

### 10.2 启动流程

1. 校验并持久化 LaunchIntent。
2. 创建 Run，状态为 `queued`。
3. Runner `Probe` 检查 Node、入口文件、配置文件和 DSH 版本。
4. 启动独立 DSH SDK 进程，状态进入 `starting`。
5. 发送 `initialize`，校验 `serverInfo.name` 与已验证能力基线。
6. 发送 `session/prompt`，保存返回的 `messageId`。
7. 等待相同 `messageId` 的 `agent/inbox/spliced` 事件，状态进入 `running`。
8. 归约后续事件，直到已归属区间进入 `idle` 或进程退出。
9. 持久化终态，再通知 Desktop 投影刷新。

### 10.3 取消流程

1. 幂等写入 cancel requested 事件。
2. 发送 `shutdown` 并等待有界时间。
3. 运行时未退出时关闭 stdin。
4. 仍未退出时终止进程树。
5. 用户取消拥有结算优先级，最终状态为 `cancelled`。
6. 清理失败单独记录诊断，不覆盖任务原始终态。

### 10.4 状态映射

| DSH 信号 | WorkGround2 状态 |
|---|---|
| LaunchIntent 已持久化 | `queued` |
| 进程启动、initialize | `starting` |
| `session/prompt` 返回 messageId | `queued`，保存绑定 |
| 匹配的 `agent/inbox/spliced` | `running` |
| `turn/start` | `running / thinking` |
| `assistant/chunk` | `running / responding` |
| `tool/call` | `running / tool:<name>` |
| `tool/result` | `running / thinking` |
| 权限请求 | `waiting_user` |
| `turn/end` 后进入 idle | 根据原因结算终态 |
| 意外进程退出 | `failed` |
| 主动关闭进程 | `cancelled` |
| Desktop 重启后发现未完成绑定 | `interrupted` 或 `stale` |

首版 SDK 没有权限回答接口。权限请求可以显示待处理状态，但不能提供 Approve 动作。

## 11. DSH Observed 会话

Observed 路径通过原生 Cordis 插件订阅：

- `session/created`
- `session/event`
- `session/flush`
- `session/disposed`
- `agent/status`
- `agent/error`
- `subagent/start`
- `subagent/end`

Observer 将完整 DSH 事件在适配器边界归约，只上报 WorkGround2 所需字段。插件提供显式安装、卸载、版本检查和 Doctor，不隐式执行 `pnpm install` 或构建。

DSH 的 `hooks-codex` 和 `hooks-claude-code` 用于让 DSH 执行用户现有 Hook 配置，其方向与 DSH 会话上报不同，不用于 Observed Reporter。

## 12. Desktop 与图标小组件

Desktop 将现有原生 widget sources 与 RunHub 的 `RunProjection` 合并：

- 显示来源标识：DSH、Codex、Claude。
- 显示主状态和简短 ActivityLabel。
- 聚合子 Agent 数量，首版不为每个子 Agent生成独立图标。
- `waiting_user` 使用明确的待处理视觉状态。
- 失败状态显示安全摘要和可重试入口。
- Open、Cancel、Retry、Resume、Approve、Send 均由 Capability 控制。
- RunHub 状态不放入 `TabMeta`，避免外部任务生命周期依赖本地 Tab。

## 13. 隐私与安全

默认只保存：

- 来源、Workspace、状态、时间和 Revision。
- 工具名称，不保存参数和结果。
- 截断、清洗后的错误诊断。
- 可配置长度的最终摘要。

默认不保存：

- 完整 Transcript。
- reasoning 和 assistant chunk。
- 工具参数、工具结果和文件内容。
- 环境变量、Token、Cookie、Authorization Header。

所有本地服务使用 stdio 或 loopback。Reporter 文件权限遵循当前用户范围，日志和错误在写入前脱敏。

## 14. 包结构

```text
internal/runhub/
  model.go
  reduce.go
  store.go
  hub.go
  runner.go
  *_test.go

internal/runhub/dsh/
  probe.go
  process.go
  protocol.go
  runner.go
  map.go
  *_test.go
```

`runhub` 父包不导入 `runhub/dsh`。Boot/Desktop 负责注册具体 Runner，避免包循环和供应商逻辑进入核心模型。

## 15. 实施阶段

### P0：能力探针与无密钥协议夹具

- 锁定 rc.8 验证基线。
- Probe Node、DSH 入口、配置、serverInfo 和能力。
- 建立 keyless JSON-RPC fixture，覆盖 initialize、prompt、event、status、shutdown。
- 不支持的版本明确拒绝启动。

验收：默认测试不访问外部模型；失败明确指出 provider、版本和缺失能力。

### P1：RunHub 核心

- 实现模型、Reducer、Store、Inbox 和幂等回执。
- 覆盖重复、乱序、迟到、终态单调、崩溃重载和损坏输入。
- 保持 transport-agnostic，不依赖 Desktop 或 DSH 包。

验收：相同 requestId 只产生一个 Run；重复 eventId 不增加 Revision 或重复通知。

### P2：DSH Managed Runner

- 实现进程所有权和 JSON-RPC 客户端。
- 完成 messageId 区间关联和事件映射。
- 实现 Cancel、意外退出、绑定持久化和重启恢复语义。

验收：成功、模型失败、进程崩溃、取消和 Desktop 重启均得到明确状态，且不会重复启动。

### P3：Desktop 投影与快速启动

- 图标小组件合并外部 RunProjection。
- 支持当前 Workspace 快速启动和显式 Workspace 覆盖。
- 动作按 Capability 显示。

验收：DSH 状态变化可见；缺少协议能力的操作不会出现。

### P4：DSH Observer

- 实现 Cordis Observer、durable reporter、安装/卸载和 Doctor。
- 将外部启动的 DSH Session 作为 `observed` Run 上报。

验收：WorkGround2 未运行时事件不丢失；启动后可去重回放。

### P5：Codex

- Observed：Codex Hooks Reporter。
- Managed：Codex App Server Runner。
- 支持 thread/turn、流式通知、中断和恢复能力协商。

### P6：Claude Code

- Observed：Claude Code Hooks Reporter。
- Managed：Claude Agent SDK streaming mode。
- 支持 Session ID、Resume/Fork、权限请求和用户输入能力协商。

## 16. 测试策略

### 16.1 默认测试

- 不调用真实 DeepSeek、Codex 或 Claude。
- 使用协议 fixture、伪进程和临时状态目录。
- 精确验证 eventId/requestId 去重、乱序恢复和终态单调。
- Windows 进程树清理使用可控子进程 fixture。

### 16.2 Opt-in 集成测试

- 显式环境变量或 build tag 才调用真实 DSH。
- 跳过时输出启用方式。
- 外部调用失败显示 provider、model、协议阶段和安全错误。

### 16.3 交付门禁

进入 Codex 适配前，DSH 必须满足：

1. 相同 requestId 只产生一个 Run 和一个进程。
2. 重复、乱序事件不产生重复图标或终态回退。
3. 成功、失败、取消、崩溃均显式结算。
4. Desktop 重启不自动重复执行未知结果任务。
5. 默认存储无 Transcript、reasoning 和工具输入输出泄漏。
6. UI 动作严格服从 Capability。
7. 新 Adapter 不修改 RunHub 核心状态机。

## 17. 未决问题

- DSH rc.8 仍无公共 Resume，后续是否推动上游协议扩展。
- ACP 是否作为独立 Runner，或只承担需要权限回调的自动化场景。
- Observed 会话的 Open 动作如何稳定定位到 DSH Web/TUI 会话。
- 子 Agent 首版只聚合计数，何时提升为独立 Run。
- RunHub 文件存储达到规模阈值后是否迁移到 SQLite。
- Codex/Claude 的 Hook 安装是否默认启用，或保持显式 opt-in。

## 18. 粗略排期

- DSH Managed MVP：约两周。
- DSH Observer 和加固：约一周。
- Codex：约一至一点五周。
- Claude Code：约一至一点五周。
- 单人完整路线：约五至六周，具体取决于 Desktop UI、搜索和交互闭环范围。
