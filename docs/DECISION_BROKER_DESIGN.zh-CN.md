# DecisionBroker 全局主人决策通道设计

## 1. 目标

DecisionBroker 是 WorkGround2 进程级的人类决策中心。来自任意 workspace、Session、Work 或外部 Agent 的长期问题先登记为持久化 `Decision`，再由桌面端、微信等端点展示。所有回答进入同一个解析入口，第一个有效回答生效，其余端点同步显示“已经回答”。

它解决五个问题：

- 问题不再绑定某个 workspace 的前台生命周期。
- 人类在桌面或微信任一端回答，结果只应用一次。
- 多个任务同时提问时，全应用一次只展示一个问题。
- 问题可等待数天，应用重启后仍可发现、回答和恢复。
- Codex 等外部 Agent 可通过稳定 API 和 Skill 使用同一能力。

第一阶段只承载业务选择型 `Ask`。工具审批保留独立权限语义，数据模型预留 `approval` 类型但不自动接入。

## 2. 核心原则

### 2.1 单一可信源

应用只运行一个 `DecisionBroker`。UI 创建和管理的是 `DecisionChannel` 投递端点，不能创建多个相互竞争的 Broker。

### 2.2 一条 Decision，多端展示

桌面弹窗、Decision Center 和外部 IM 渲染同一份 `DecisionPresentation`。端点不能各自改写业务含义。

### 2.3 先回答者生效

所有端点调用统一的 `Resolve`。Broker 以 `pending/presented` 条件原子冻结第一个答案。重复、乱序和迟到回答返回当前结果，不产生第二次副作用。

### 2.4 人类可读

每条问题必须假设收件人完全没看过任务前文。移动端应在数秒内回答以下问题：

1. Agent 正在做什么？
2. 为什么现在需要人类决定？
3. 每个选项会产生什么结果？
4. Agent 推荐什么，理由是什么？
5. 人类没有回答时任务会怎样？

依赖图片、文件或网页才能判断时，必须携带引用或安全预览。通道无法展示必要引用时，应显式提示回桌面查看，不能发送一个无法判断的纯文本问题。

### 2.5 长期等待不占用运行资源

Decision 默认没有时间型硬过期。来源任务进入 `waiting_decision` 后应保存恢复信息并释放模型、工具和活跃执行资源。回答到达时用幂等恢复命令继续。

## 3. 组件

```text
Controller / Work / 外部 Agent
             |
             v
      DecisionBroker
       |           |
       v           v
  Durable Store   Ordered Outbox
       |           |
       v           v
 Decision Center  Desktop / Weixin / future transports
```

建议包边界：

- `internal/decision`：模型、状态机、持久化、全局队列、端点投递记录和恢复命令。
- `internal/control`：通过窄 `DecisionPort` 登记问题或接收已解析答案，不感知微信/UI。
- `internal/bot`：实现 DecisionChannel 发送与微信回复解析。
- `desktop`：持有应用级 Broker、Controller origin registry、Wails API 和恢复协调。
- `desktop/frontend`：Decision Center、当前决策卡片、历史和通道设置。

## 4. 数据模型

### 4.1 Decision

```go
type Decision struct {
    ID             string
    IdempotencyKey string
    Kind           string
    Origin         OriginRef
    Presentation   Presentation
    Status         Status
    Answer         *Answer
    Responder      *Responder
    Revision       int64
    QueueSeq       int64
    CreatedAt      time.Time
    PresentedAt    *time.Time
    DecidedAt      *time.Time
    AppliedAt      *time.Time
    BusinessDueAt  *time.Time
    StaleAfter     *time.Time
}
```

`OriginRef` 至少包含来源类型、workspace、Session path/ID、Controller generation、Agent/thread 标识和恢复 payload。`IdempotencyKey` 由来源生成，重复创建必须返回同一条 Decision。

### 4.2 Presentation

```go
type Presentation struct {
    Title          string
    TaskSummary    string
    WhyNow         string
    Questions      []Question
    Recommendation *Recommendation
    NoAnswerPolicy string
    References     []Reference
}
```

每个选项必须包含简短 label 和 impact。推荐项存在时必须包含理由。来源项目、Session 和 Agent 名称由宿主补齐，避免模型伪造路由信息。

### 4.3 Delivery

```go
type Delivery struct {
    DecisionID     string
    EndpointID     string
    Sequence       int64
    Event          string
    Status         string
    RemoteMessage  string
    Attempts       int
    NextRetryAt    time.Time
    LastError      string
}
```

同一端点严格按 `Sequence` 投递。上一题的 `resolved` 必须先于下一题的 `presented` 到达。

## 5. 状态机

```text
queued -> presented -> decided -> applied
             |            |
             v            v
          deferred     apply_failed --retry--> applied
             |
             +--------> queued

queued/presented/deferred -> cancelled | orphaned
```

- `queued`：已持久化，等待全局注意力槽。
- `presented`：全应用唯一可回答问题。
- `decided`：答案已冻结，后续回答只能观察结果。
- `applied`：来源已确认接收答案。
- `deferred`：人类明确选择稍后，仍长期有效。
- `orphaned`：来源无法恢复，保留记录并显式暴露。

Broker 同时最多有一个 `presented`。当前题进入 `decided`、`deferred` 或终态后才晋升下一题。默认 FIFO；已展示的问题不被高优先级抢占。安全紧急问题只能调整尚未展示的队列顺序。

## 6. 双端抢答与一致性

`Resolve` 在一个持久化临界区内完成：

1. 校验 Decision 仍可回答。
2. 校验选项和值。
3. 把答案、responder 和时间写入 Decision，状态转为 `decided`。
4. 写入向来源应用答案的 outbox。
5. 为所有已展示端点写入 `resolved` 投递。
6. 晋升下一条 Decision，并在各端点的 `resolved` 后写入新的 `presented` 投递。

来源应用失败不会解冻答案。恢复器安全重试直到 `applied` 或确认 `orphaned`。来源接口必须返回明确结果，不能只靠日志判断。

## 7. 超长时限与恢复

`BusinessDueAt` 可空；空表示不会因为时间自动取消。`StaleAfter` 只控制提醒和“等待很久”标签。Decision 的真正终止来自明确回答、取消、来源取消、业务截止或来源不可恢复。

回答可能在数天后到达。恢复前必须检查 Controller generation、Session revision 和依赖摘要：

- 一致：应用答案并恢复任务。
- 已变化：进入 `needs_revalidation`，生成带新上下文的后续 Decision。
- 来源不存在：标记 `orphaned`。

外部 Agent 采用异步协议。`decision_ask` 创建后立即返回 ID；Agent 保存 ID 并结束或挂起当前工作。`decision_wait` 只做有界等待，长期恢复依赖事件/后续调用。

## 8. 临时静默

静默只影响外部投递，不能停止 Broker、桌面展示或已登记 Decision。模式：

- `smart`：桌面最近活跃时先本地展示，宽限期后仍未回答再向外投递。
- `always`：立即向所有启用端点投递。
- `local_only_until`：指定时间前只在本地展示，到期自动恢复。
- `off`：关闭新的外部投递。

静默前已经发出的 Decision 仍可从远端回答，并继续接收 `resolved` 通知。临时状态和截止时间必须持久化。

## 9. 产品 UI

桌面新增“主人决策”入口：

- 当前问题：全局唯一决策卡片，显示来源、背景、影响、推荐和引用。
- 队列：只显示后续数量和简短标题，避免同时争夺注意力。
- 历史：已回答、已取消、等待过久和 orphaned。
- 通道：创建/编辑微信等 DecisionChannel、选择目标对话、测试发送和诊断。
- 静默：暂停 30 分钟、1 小时、到今天下班或手动恢复。

回答后所有表面立即禁用原选项，展示“由谁、在哪个端点、何时回答”。

## 10. 外部 Agent API 与 Skill

稳定接口：

- `decision_ask`
- `decision_get`
- `decision_list`
- `decision_wait`
- `decision_cancel`

外部调用方只能查看和取消自己创建的 Decision。回答接口只开放给经过认证的 Owner/Approver 端点。

Skill 是调用规范层，要求 Agent 提供完整人类可读上下文、幂等 key 和可恢复 origin。可靠传输由 MCP/HTTP/CLI 工具提供，Skill 本身不充当消息队列。

## 11. 安全与隐私

- Presentation 不能包含 secret、token、原始凭据或未经授权的本地文件内容。
- 本地绝对路径默认转为项目内相对描述；远端预览使用受限引用。
- 只有 Owner/Approver 可以回答；普通 Bot 用户不能处理跨项目 Decision。
- 每次创建、投递、回答、应用、失败和重试都写审计记录。

## 12. 验收标准

- 两个 workspace 同时提问时只展示一个，另一个稳定排队。
- 桌面和微信并发回答只有一个胜出，另一端看到“已经回答”。
- 重复创建、重复回答、乱序投递和进程重启均不产生重复副作用。
- 暂停外部投递不影响桌面回答；到期自动恢复。
- 问题包含任务背景、原因、选项影响、推荐理由和无回答策略。
- 默认长期有效；重启后恢复当前问题和队列顺序。
- 外部 Agent 可通过 API 创建、查询、等待和取消，并可使用安装的 Skill。
- 发送/应用失败可观察、可重试、可恢复。
