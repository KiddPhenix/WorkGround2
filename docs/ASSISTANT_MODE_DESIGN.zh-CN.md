# WorkGround2 Assistant 改进设计与实施计划

> 状态：规划中
>
> 类型：调整现有 Assistant 功能
>
> 本文是 Assistant 目标行为和后续实施的唯一设计入口。已经完成的历史实现从 Git 历史查询，不在本文维护另一套旧设计。

## 1. 目标

Assistant 是长期存在、持续推进用户目标的高自治执行助手。它拥有自己的主管 Session、工作计划、记忆、定时任务和权限策略，并通过工具直接创建和管理其他普通 Session。

用户只需描述意图，例如：

- “继续推进这个项目，发现问题自己修。”
- “每天检查构建，失败后分析并重试。”
- “推广完成后继续找新渠道、优化文案。”
- “继续找更多符合条件的职位和公司并投递。”
- “暂停全部工作，更新 WG2 后恢复。”

Assistant 负责把自然语言转成可审计的操作：

- 新建、读取、指导、回答、取消、恢复和分叉 Session。
- 查询和更新长期记忆。
- 增加、修改、暂停、恢复、删除和立即执行定时任务。
- 查询项目状态，修改项目约束。
- 维护工作计划，完成当前工作后主动发现下一批机会。
- 根据实际结果复盘，并通过网页、GitHub、官方文档和公开经验继续学习。

最终体验应满足：持续响应、高权限、允许可恢复的试错，同时不能因为普通选择或失败停在那里等待用户。

## 2. 调整范围

这次调整直接改进现有 Assistant，不新增平行的 Supervisor 产品，也不新增第二套 Assistant 存储或调度系统。“Supervisor”只描述 Assistant 持续观察、计划和管理 Session 的职责。

现有基础已经具备：

- Assistant、Plan、Routine、Memory、Policy 和持久存储。
- Desktop Assistant Runtime 的定时 tick、主动 Wake、leader lease 和执行 lease。
- Controller 的 `Steer`、`Cancel`、`PendingInteraction`、`AnswerQuestion` 和 Session 恢复。
- 待回答问题的持久化与重启恢复。
- 有 revision 的责任图、Artifact、Opportunity 和结构化进度提交。
- Dispatcher、RunnerJob、Reflector 和 Ideator。

当前主要问题：

- Assistant 的执行状态分散在 Run、RunnerJob、后台 Controller 和 Session 中。
- RunnerJob 创建的后台 Controller 没有进入统一 Session 控制面，难以实时查看、指导和停止。
- 当前计划完成后，Assistant 可能进入空闲，横向发现能力偏低频且依赖人工采纳。
- Session 的普通问题会等待用户，Assistant 无法统一代答。
- 定时任务缺少完整的自然语言增删改查闭环。
- 缺少覆盖全部执行的持久化暂停、静默、恢复和安全重启能力。
- Assistant 看到的运行 Session、近期 Session、失败 Session 和用户当前视窗任务缺少统一的有界上下文。

## 3. 状态唯一可信源

目标实现必须收敛状态所有权，禁止为方便 UI 或兼容旧流程继续复制执行状态。

### 3.1 Assistant Store

Assistant Store 只拥有长期意图、知识和计划证据状态：

- Assistant 身份、使命、工作模式和生命周期。
- Policy。
- Work Plan。
- Routine。
- Memory（包含 Learning）、Artifact 和 Opportunity。
- 请求幂等凭据和计划修改 revision。

Assistant Store 不保存 Session 的运行状态、当前步骤、待回答问题或取消状态。

### 3.2 Session 子系统

Session 子系统是所有执行状态的唯一可信源：

- 对话和工具调用记录。
- queued、running、waiting、completed、failed、cancelled 等运行状态。
- 当前活动、错误、待回答问题和待审批事项。
- `Steer`、`Answer`、`Cancel`、`Resume` 和恢复检查点。
- Session 所属 Assistant、父 Session、用途和 Workspace。

Assistant 管理的任务都是普通 Session。Session 元数据直接记录 `OwnerAssistantID` 和可选的 `ParentSessionID`；查询受管 Session 时直接查询 Session 子系统，不在 Assistant Store 再维护一份任务列表。

每个 Assistant 有且只有一个 `Purpose=supervisor` 的主管 Session。唯一性由 Session 子系统保证，Assistant Runtime 直接恢复或创建它。

### 3.3 Routine

Routine 是用户定时任务定义的唯一可信源。调度器只计算到期时间并创建一次触发记录；触发成功后，由对应 Session 表达实际执行状态。

触发记录只承担：

- 防止同一个计划时间重复启动。
- 记录本次触发创建的 Session ID。
- 记录触发是否已被消费或需要补偿。

它不复制 Session 的 running、failed 或 completed 状态。

### 3.4 项目约束

项目约束继续由项目自己的约束文件或约束存储持有。Assistant 通过工具读取和修改权威内容，不把副本写入 Assistant Memory。Memory 可以保存“为什么这样约束”的经验，但不能覆盖项目约束本身。

### 3.5 全局工作控制

所有工作是否允许继续由一个持久化 `WorkControl` 状态决定。Runtime、Scheduler、Session 创建和重试都读取同一个状态，不在各模块维护独立暂停开关。

### 3.6 UI 视窗

用户当前看到的任务属于短期 UI 观察，不是业务状态。前端只发布带时间戳的可见 Session ID、选中 ID 和窗口信息；后端根据这些 ID 读取权威 Session 状态。

## 4. 核心模型

### 4.1 Assistant

```go
type Assistant struct {
    ID            string
    Name          string
    Mission       string
    Mode          AssistantMode
    Scope         Scope
    WorkspaceRoot string
    Lifecycle     Lifecycle
    Policy        Policy
    Revision      int64
    CreatedAt     time.Time
    UpdatedAt     time.Time
}
```

`Mode`：

- `finite`：满足完成标准后结束或进入维护。
- `continuous`：完成当前批次后主动发现下一批工作，长期保持运行。

`Lifecycle` 只描述 `active`、`paused`、`archived`。执行状态从主管 Session 和受管 Session 推导。

Assistant 的 `paused` 只停止该 Assistant；`WorkControl=PAUSED` 停止 WG2 的全部工作。两者作用域不同，都不表达 Session 执行状态。

### 4.2 Work Plan

Assistant 必须拥有可编辑、可观察、可恢复的工作计划表：

```go
type WorkPlan struct {
    Revision         int64
    Responsibilities []Responsibility
    Opportunities    []Opportunity
    Experiments      []Experiment
}
```

`Responsibility` 表达已经决定要做的工作：

```go
type Responsibility struct {
    ID           string
    Objective    string
    DoneCriteria string
    NextAction   string
    Disposition  ResponsibilityDisposition
    DependsOn    []string
    BlockReason  string
    ParentID     string
    Strategy     string
    Priority     int
    ExpectedGain string
    Cost         string
    Risk         string
    Revision     int64
}
```

持久状态只表达计划决策：`planned`、`waiting`、`review`、`done`、`dropped`。`ready` 根据依赖和 Policy 推导；`active`、`failed` 和 `completed` 根据关联 Session 推导，不写回 Responsibility。

`Opportunity` 是尚未采纳的候选工作。`Experiment` 记录假设、隔离方案、衡量指标和结论。Artifact 通过 Responsibility ID 记录完成证据；Session 通过计划项 ID 表达正在执行哪项工作，计划不反向保存 Session 列表。

Learning 使用现有 Memory 的受控类型保存，记录可复用经验、来源、观察时间、置信度、适用范围和失效条件，不再建立独立知识副本。

计划状态只通过少数计划工具修改，并使用 `expected_revision` 防止迟到推理覆盖新计划。

### 4.3 Routine

```go
type Routine struct {
    ID          string
    AssistantID string
    Title       string
    Prompt      string
    Schedule    Schedule
    Timezone    string
    Enabled     bool
    CatchUp     CatchUpPolicy
    Revision    int64
}
```

内部失败重试计时器不写入 Routine。用户定时任务和运行恢复是两类不同状态，分别管理。

### 4.4 Policy

Policy 至少包含：

- Workspace 与项目范围。
- 本地读写权限。
- 网络查询权限。
- 创建 Session 和并发数量限制。
- 项目约束修改权限。
- 外部发言权限。
- 删除、付费、凭据和隐私边界。
- 自动回答问题和隔离试错策略。

对外公开发布或代表用户发言由用户直接配置：

```go
ExternalVoiceEnabled bool
```

- `true`：在允许的渠道、身份、范围和频率内自主发布或回复，不逐次询问。
- `false`：发布工具明确拒绝；Assistant 继续研究、生成草稿和优化计划，不反复请求用户。

## 5. 持续监督循环

Assistant Runtime 运行一个逻辑无限的控制循环。控制循环持续存在，单次模型推理仍然有边界并可恢复。

```text
watching
  -> thinking
  -> acting
  -> awaiting_session
  -> evaluating
  -> expanding / retry_wait / blocked / done
  -> watching
```

### 5.1 唤醒来源

- 用户向 Assistant 输入新要求。
- 受管 Session 开始、推进、提问、失败或完成。
- Routine 到期。
- 重试时间到期。
- 项目状态或约束变化。
- 用户恢复全部工作。
- 空闲 heartbeat 到期。

事件立即唤醒。heartbeat 只负责发现遗漏事件和在长期空闲时重新评估目标。控制循环可以高频检查轻量状态；只有观察 revision 变化、存在可执行工作或 heartbeat 到期时才调用模型。

### 5.2 单次循环边界

每次循环拥有：

- 唯一 cycle ID 和 fence。
- 观察到的 Plan、Policy、WorkControl 和 Session revision。
- 模型轮数、工具次数、并发 Session 和运行时间预算。
- 动作后的持久化检查点。

达到单次预算时保存下一步并回到 `watching`，随后继续新循环。长期目标没有总轮数上限。

同一个 Assistant 同时只运行一个主管推理回合，避免两个模型同时改计划。受管 Session 可以在 Policy 限制内并行运行。

### 5.3 下一步选择

每轮按以下顺序选择工作：

1. 处理需要立即响应的安全或失败事件。
2. 代答受管 Session 的普通问题。
3. 处理已到期 Routine。
4. 观察执行中的 Session，并推进最高优先级可执行责任。
5. 复核 `review` 责任的完成证据。
6. 对可重试失败执行恢复。
7. 没有可执行责任时启动计划扩展。
8. 仅剩硬门槛时记录阻塞，同时继续其他不依赖该门槛的工作。

完成由 `DoneCriteria + Artifact` 证据判定，不能只依赖模型声称完成。

## 6. Assistant 隐式上下文

每次主管推理默认注入一个有界 `AssistantContext`：

- Mission、Mode、Policy 摘要。
- 当前可执行、等待和待验证责任，以及从 Session 推导出的执行中责任。
- 正在运行的受管 Session：ID、标题、状态、当前活动、更新时间。
- 最近五个相关 Session 的短摘要。
- 最近失败的 Session 和错误分类。
- 待回答问题、待审批事项和重试时间。
- 当前 Workspace 和项目约束摘要。
- 用户当前视窗可见的 Session ID、顺序和选中项。
- 当前时间与调度状态。

默认上下文不注入完整历史、完整工具输出或大量失败日志。Assistant 使用只读工具渐进加载：

1. 默认摘要。
2. 指定 Session 的最近回合或失败详情。
3. 明确需要时读取更深历史、项目文件或外部来源。

### 6.1 视窗上下文

前端发布：

```text
window_id
workspace_id
visible_session_ids
selected_session_id
observed_at
ui_revision
```

视窗快照有短 TTL。滚动只更新快照，不单独启动模型；下一次用户输入或运行事件发生时，Assistant 可以理解“我正在看的这几个任务”等指代。

多窗口时使用最近获得焦点且仍有效的快照。快照过期后必须显式视为未知。

### 6.2 可观察性

隐式上下文默认不占据对话正文，但 UI 提供诊断入口，展示本轮读取了哪些 Session、Plan 项和项目状态。任何决策都可以追溯到输入 revision 和工具结果。

从其他 Session、网页和 GitHub 读取的文本一律视为不可信数据，不能借其中的指令提升权限或绕过 Policy。

## 7. Session 控制工具

Assistant 通过明确工具管理 Session：

### 7.1 查询

- `session_list`：按 running、recent、failed、owned、workspace 查询。
- `session_status`：读取一个或多个 Session 当前状态。
- `session_read`：有界读取指定 Session 对话和工具摘要。
- `interaction_list`：查询待回答问题和待审批事项。

### 7.2 操作

- `session_create`：创建普通受管 Session。
- `session_steer`：向正在运行的 Session 插入指导。
- `interaction_answer`：回答指定 Session 的指定问题。
- `session_cancel`：停止执行并保存可恢复状态。
- `session_resume`：恢复已中断 Session。
- `session_retry`：基于失败上下文安全重试。
- `session_fork`：从检查点创建隔离分支，用于尝试互斥方案。

所有目标都必须显式传入稳定 Session ID，不允许空 ID 代表“当前 Session”。

所有写工具必须携带 `request_id`。可能覆盖并发修改的操作同时携带 `expected_revision`。统一返回：

- `accepted`
- `already_applied`
- `stale`
- `retryable_error`
- `invalid`
- `blocked_by_policy`

工具返回当前状态、revision 和下一步提示。响应丢失后使用相同 `request_id` 重放，不能重复创建或重复执行外部动作。

## 8. 受管 Session 行为

Assistant 创建的任务直接成为普通 Session，并在 Session 元数据中记录所有者、父 Session、工作计划项和用途。Desktop、daemon 和其他宿主使用同一个 Session 控制面。

受管 Session 的事件会唤醒主管 Session：

- `session_started`
- `session_progressed`
- `interaction_required`
- `session_failed`
- `session_completed`
- `session_cancelled`

主管 Session 可以读取最新状态，然后选择指导、回答、取消、恢复、分叉或更新计划。

Session 完成后，主管 Session 根据真实产物和验证结果更新责任状态。Session 失败只改变 Session 自身状态；计划项根据失败分类进入 retry、重新设计、等待依赖或 dropped。

## 9. 自动回答与隔离试错

受管 Session 发生 `interaction_required` 时，Assistant 应尽可能自行处理。

决策顺序：

1. 根据 Mission、项目约束、计划、记忆和当前证据推断最佳选项。
2. 选项可逆且置信度不足时，在隔离 Session、worktree 或 sandbox 中尝试多个方案。
3. 用测试、产物、成本和副作用比较结果。
4. 选择证据最好的方案并回答原 Session，或让胜出的 Session 继续执行。
5. 无法隔离时选择最容易回滚且能继续推进的选项。

每个普通问题都有 `decision_due_at`。到期后 Assistant 必须自行决定，不能无限等待用户。

只有以下硬门槛允许等待用户：

- 缺少无法推断或获取的凭据。
- 不可恢复的大范围删除或数据覆盖。
- 资金、法律和身份操作。
- Policy 明确要求用户决定。
- 用户明确要求此类动作必须确认。

等待硬门槛时，其他可执行责任继续运行。Assistant 记录选择来源 `inferred`、`experiment` 或 `user`，以及置信度、候选项、理由和结果。

## 10. 工作计划与横向扩展

工作计划表在 UI 中按以下队列展示：

```text
机会池 -> 待执行 -> 执行中 -> 等待 -> 验证 -> 完成
```

### 10.1 扩展触发

- 当前没有可执行责任，并且没有执行中的关联 Session。
- 工作长期没有进展。
- 同类失败重复出现。
- 关键指标下降。
- Session、用户或外部研究提供了新证据。
- heartbeat 发现 Mission 仍有未覆盖空间。

### 10.2 扩展循环

```text
Evaluate -> Discover -> Research -> Rank -> Adopt -> Execute -> Learn
```

`Evaluate`：检查结果、失败、瓶颈、指标和未覆盖面。

`Discover`：从相邻渠道、相邻受众、相邻目标、质量优化、流程自动化和已有成果复用等方向生成 Opportunity。

`Research`：按需搜索网页、GitHub、官方文档和可信社区。研究可以交给独立受管 Session，来源必须保留链接、观察时间和关键证据。

`Rank`：按目标相关性、预期价值、证据强度、成本、风险和重复度排序。

`Adopt`：高自治模式自动把最有价值且可执行的 Opportunity 提升为 Responsibility 或 Experiment。

`Execute`：创建 Session 执行，结果回写 Plan。

`Learn`：总结有效做法、失败模式、适用范围和下一轮调整。

持续目标在计划为空时必须进入扩展循环。有限目标根据配置进入 `done`、`maintenance` 或继续扩展。

### 10.3 示例

推广助手完成当前渠道后：

- 搜索新的社区、产品目录、媒体、合作伙伴和内容形式。
- 研究同类产品公开的增长经验。
- 根据现有效果拆分受众并优化文案。
- 建立可度量的文案或渠道实验。

求职助手完成当前职位投递后：

- 扩展相邻职位、行业和公司名单。
- 搜索新的招聘渠道和关键词。
- 根据回复率与拒绝原因调整简历版本和投递策略。
- 继续形成下一批投递任务。

扩展始终受 Mission、Scope、Policy、成本和并发限制。候选项必须去重；没有价值证据时允许降低频率，不能为了维持循环制造重复工作。

## 11. 反思与学习

以下事件触发反思：

- Session 完成。
- Session 失败。
- 重试仍失败。
- 一轮计划完成。
- 指标显著变化。
- 新研究与现有认知冲突。

反思输出必须回答：

- 做了什么，结果如何。
- 哪些方法有效，证据是什么。
- 哪些方法无效，失败边界是什么。
- 是否应重试、调整、停止或扩大。
- 哪些结论值得写入 Memory 的 Learning、strategy 或 fact 类型。
- 下一步计划如何改变。

Memory 中的 Learning 记录来源、观察时间、置信度、适用范围和失效条件。来自网络的经验先作为候选知识，经过本地验证或多来源交叉验证后再提升置信度。

GitHub 研究优先读取项目文档、代码、Issue 和 Discussion；引入代码或配置前必须在隔离环境验证，不能因为仓库热度直接认为适用。

## 12. 定时任务管理

Assistant 必须能理解自然语言并直接管理 Routine：

- `schedule_list`
- `schedule_get`
- `schedule_create`
- `schedule_update`
- `schedule_pause`
- `schedule_resume`
- `schedule_delete`
- `schedule_run_now`

语义：

- 修改从下一次触发生效，不改变已经运行的 Session。
- 暂停和删除只阻止未来触发，不隐式取消当前 Session。
- 用户要求同时停止当前执行时，Assistant 额外调用 `session_cancel`。
- 删除默认可恢复，重复删除返回 `already_applied`。
- `run_now` 创建一次独立 fire，并通过 `request_id` 防止重复启动。
- 每次计划触发生成稳定 `fire_id`；重复 tick、leader 切换和重启只能创建一个 Session。
- 内部重试计时器不出现在用户 Routine 列表中。

Routine UI 展示规则、时区、启停状态、下次运行、上次触发 Session、连续失败和当前重试状态；失败和重试信息从关联 Session 推导，不写回 Routine。

## 13. 项目、记忆和 Policy 工具

### 13.1 项目

- `project_status`
- `project_constraints_get`
- `project_constraints_patch`

修改项目约束直接作用于项目权威状态，使用 `request_id` 和 `expected_revision`。Assistant Memory 不保存可覆盖项目约束的副本。

### 13.2 记忆

- `memory_search`
- `memory_remember`
- `memory_forget`

事实、策略、失败经验和用户偏好都要带来源。过时信息可失效或替换，不能与新事实并列后让模型自行猜测。

### 13.3 Policy

- `assistant_policy_get`
- `assistant_policy_update`

Assistant 可以根据用户自然语言调整 Policy，但不能自行扩大自己的权限。用户可以直接开关外部发言权限、修改允许的 Workspace、渠道、并发和危险动作边界。

## 14. 暂停全部、恢复与重启

`WorkControl` 状态：

```text
RUNNING -> QUIESCING -> PAUSED
   ^                      |
   |------ RECOVERING <---|
```

### 14.1 暂停全部

`pause_all` 必须作用于所有模型回合、工具执行、Assistant 循环、Routine 触发、重试和受管 Session：

1. 原子进入 `QUIESCING`，增加全局 work epoch/fence。
2. 停止领取新任务和创建新 Session。
3. 通知活动 Session 在安全点保存检查点。
4. 超时未结束的模型或工具调用执行取消。
5. 保存每个中断 Session 的恢复意图。
6. 全部静默后进入 `PAUSED`。

控制命令、状态查询和恢复命令在暂停期间仍可使用。旧 epoch 的迟到完成不得覆盖暂停后的状态。

### 14.2 恢复全部

`resume_all`：

1. 进入 `RECOVERING`。
2. 扫描中断 Session、待回答问题、到期 Routine fire、失效 lease 和未完成检查点。
3. 根据幂等凭据恢复或补偿，未知外部结果不得自动重放。
4. 恢复主管 Session 和受管 Session 的事件订阅。
5. 进入 `RUNNING` 并立即唤醒 Assistant。

### 14.3 重启语义

- `RUNNING` 状态下异常退出或普通重启：启动后自动恢复。
- 显式 `PAUSED`：重启后继续保持暂停，防止失控任务复活。
- `pause_for_restart`：安全静默并写入一次性恢复意图，应用重启后自动进入 `RECOVERING`。

UI 提供“暂停全部”“恢复全部”“安全重启”三个入口，并显示静默进度、未完成检查点和恢复结果。

## 15. 失败、重试与自愈

每次失败先分类：

- `retryable_known`：结果明确，可安全重试。
- `failed_known`：结果明确，需要换方案或调整计划。
- `outcome_unknown`：外部结果未知，禁止自动重放。
- `blocked_policy`：Policy 阻止，继续其他工作。
- `blocked_dependency`：依赖不可用，等待事件或退避。

处理流程：

1. 保存错误、当前 Session、工具调用和观察 revision。
2. 生成短反思，避免把完整失败日志反复塞入上下文。
3. 选择重试原动作、指导原 Session、分叉替代方案、等待依赖或放弃。
4. 可重试错误使用有上限的指数退避。
5. 重复同类失败后必须改变策略，不能机械重放。
6. 失败与恢复结果写入 Plan、Memory 中的 Learning 和诊断事件。

Assistant 可以犯错，但错误必须显式、可定位、可恢复、可复盘。

## 16. Desktop UI

继续使用现有 Assistant 页面，增加以下区域：

### 16.1 工作计划

- 机会池、待执行、执行中、等待、验证和完成列。
- 展示目标、下一步、完成条件和优先级；关联 Session、证据和失败从 Session 与 Artifact 查询。
- 用户可以直接新增、修改优先级、暂停、丢弃或要求扩展。

### 16.2 受管 Session

- 展示 running、waiting、failed、retrying 和 completed。
- 可以打开 Session、插入指导、回答、停止、恢复和分叉。
- 明确显示 Session 当前活动和最后更新时间。

### 16.3 定时任务

- 增加、编辑、暂停、恢复、删除和立即运行。
- 展示下次触发、最近 Session、失败次数和重试状态。

### 16.4 控制与权限

- 暂停全部、恢复全部、安全重启。
- 外部发言开关。
- Workspace、网络、本地写入、项目约束、并发和危险操作设置。

### 16.5 观察与学习

- 最近反思、Learning、来源和置信度。
- 本轮隐式上下文诊断。
- Assistant 自主选择和隔离实验的理由、结果与回滚点。

UI 只提交意图和展示权威状态，不直接推断或修改业务状态。

## 17. 实施计划

以下工作按依赖顺序推进。每项完成后运行最小相关测试，并在跨模块控制面稳定后执行完整验证。

### 17.1 收敛 Session 状态所有权

目标：所有 Assistant 执行统一成为 Session。

- 将活动 Session 目录下沉为 Desktop、daemon 和后台 Controller 共用的控制能力。
- Session 元数据增加 Assistant 所有者、父 Session、用途和计划项引用。
- 后台 Controller 创建后立即注册，结束后保留持久 Session 身份并释放运行实例。
- Assistant 查询、取消和指导都按 Session ID 操作。
- 停止为新任务创建独立 RunnerJob 执行状态。
- 已有 Run/Job 只保留历史读取，不继续双写，不为其维护实时投影。

验收：任意 Assistant 创建的后台任务都能出现在统一 Session 列表中，并能实时 `status`、`steer`、`answer`、`cancel` 和恢复。

### 17.2 建立主管 Session 与常驻循环

目标：现有 Assistant 直接拥有持续运行的主管 Session。

- 创建或恢复唯一主管 Session。
- 用户输入、Session 事件、Routine、重试和 heartbeat 进入同一事件队列。
- 同一 Assistant 的主管推理串行执行，事件可合并但不能丢失。
- 每轮保存 observation revision、动作 receipt 和下一步。
- 计划为空时进入扩展循环。

验收：应用持续运行时 Assistant 能自动推进；退出和重启后从同一计划与 Session 状态继续。

### 17.3 补齐 Session、项目、记忆和 Policy 工具

目标：自然语言意图都通过明确工具落地。

- 实现 Session 查询与控制工具。
- 实现项目状态和项目约束工具。
- 复用并收敛 Memory 工具。
- 实现 Policy 查询与更新。
- 为所有写操作统一 request receipt 和 revision 冲突语义。

验收：重复工具调用不产生重复 Session、重复修改或重复外部动作；目标 ID 缺失时显式拒绝。

### 17.4 升级工作计划和扩展引擎

目标：Assistant 有自己的工作组合表，完成后自动横向扩展。

- 在现有 Plan 中补齐 Opportunity、Experiment，并让 Learning 直接使用 Memory 的受控类型。
- 将执行中、失败和完成等运行状态改为从 Session 推导，Plan 只保存计划决策。
- 增加 plan_empty、stalled、repeated_failure、metric_regression 和 new_evidence 触发。
- 将 Ideator 的候选生成能力纳入主管循环。
- 高自治模式自动采纳有证据的高价值机会。
- 增加网页和 GitHub Research Session 模板、来源记录与验证流程。

验收：推广和求职场景完成当前批次后，能自动生成、筛选并执行下一批计划；重复机会不会反复加入。

### 17.5 自动代答和多方案试错

目标：普通问题不再等待用户。

- 将 PendingInteraction 事件送入主管 Session。
- 实现 `interaction_answer` 和 decision deadline。
- 可逆选项通过隔离 Session/worktree 并行尝试。
- 保存选择依据、置信度、结果和回滚点。
- 只把硬门槛留给用户。

验收：受管 Session 的普通选项能自动得到回答；低置信选项可以隔离验证；等待硬门槛时其他计划继续推进。

### 17.6 补齐 Routine 管理

目标：Assistant 能自然语言增删改查定时任务。

- 补齐 schedule 工具。
- 使用稳定 fire ID 防止重复触发。
- 明确修改、暂停、删除与当前运行 Session 的边界。
- 在重启和 leader 切换后安全补偿。

验收：重复 tick、重复请求和重启不会重复创建 Session；定时任务变更从下一次触发生效。

### 17.7 全局暂停和恢复

目标：用户可以可靠停止 WG2 的全部工作并恢复。

- 实现唯一 WorkControl 状态和全局 fence。
- 所有任务领取、Session 创建、模型回合、工具执行和重试接入同一闸门。
- 实现 `pause_all`、`resume_all` 和 `pause_for_restart`。
- 恢复 pending ask、lease、Session 检查点和未消费 fire。

验收：暂停后没有新工作启动，迟到结果不会污染状态；普通重启、安全重启和显式暂停分别符合定义语义。

### 17.8 更新 Assistant UI 与诊断

目标：用户能看到和控制 Assistant 的真实工作状态。

- 增加计划表、受管 Session、定时任务、学习记录和控制区。
- 前端发布当前视窗快照。
- 增加隐式上下文和自主决策诊断。
- 所有操作失败显式提示并可安全重试。

验收：UI 中显示的数据都能追溯到 Assistant Store、Session、Routine、Project 或 WorkControl 的权威状态，没有前端私有业务状态。

### 17.9 清理重复执行路径

目标：完成状态收敛后移除旧路径。

- 删除新流程不再使用的 RunnerJob 领取与状态推进。
- Dispatcher 保留用户意图入口和流式回复能力，停止把任务冻结成独立 Job。
- 删除 Run/Job 与 Session 的实时双写、修复和对账代码。
- 更新 daemon、Desktop、CLI 和测试，使其共享相同控制面。

验收：一个任务只有一个 Session 执行状态；取消、恢复、问题和结果不需要跨多套状态同步。

## 18. 影响模块

- `internal/assistant`：Assistant、Policy、Plan、Routine、Memory、扩展循环和事件调度。
- `internal/control`：共享 Session 控制、PendingInteraction、恢复和全局暂停接入。
- `internal/tool/sessiontool`：Session 查询与控制工具。
- `internal/assistantdaemon`：常驻宿主和重启恢复。
- `desktop/assistant_app.go`：Assistant API。
- `desktop/assistant_runner.go`：持续监督循环、事件唤醒和全局闸门。
- `desktop/assistant_dispatch.go`：输入进入主管 Session，移除独立 Job 执行依赖。
- `desktop/session_registry.go`：收敛到共享 Session 控制面。
- `desktop/frontend/src/custom/features/assistant`：计划、Session、Routine、学习、权限和暂停恢复 UI。
- 项目约束与 Memory 现有实现：提供权威读写工具，不复制状态。

## 19. 验证清单

### 状态单源

- 一个受管任务只有一个 Session 状态。
- Assistant Store 不保存 Session 运行状态副本。
- Routine fire 不复制 Session 结果。
- UI 不维护可覆盖后端的业务状态。

### 幂等与乱序

- 相同 request ID 重放返回同一结果。
- 重复 tick 只创建一个 Session。
- 旧 fence 的完成和失败都被拒绝。
- Session 事件重复、迟到和乱序不会倒退计划。

### 无限循环与扩展

- 有事件立即响应。
- 长期空闲仍会 heartbeat。
- 单轮结束后可以继续下一轮。
- 计划为空时 continuous Assistant 自动发现下一批工作。

### 自动代答

- 普通问题在 deadline 前由 Assistant 回答。
- 多方案在隔离环境执行。
- 硬门槛等待期间其他责任继续推进。

### 暂停与恢复

- 暂停后没有新模型、工具、Session、Routine 或重试启动。
- 活动工作保存恢复意图。
- 重启恢复不重复外部动作。
- 显式暂停不会因为重启自动解除。
- 安全重启能够自动恢复。

### 权限

- 外部发言关闭时无法发布，但草稿与研究继续。
- 外部发言开启时在允许范围内无需逐次确认。
- Assistant 无法自行扩大 Policy。
- 外部内容不能通过提示注入绕过工具权限。

## 20. 完成标准

- 用户可以只通过自然语言让现有 Assistant 管理 Session、记忆、Routine、项目状态和项目约束。
- Assistant 持续运行，能观察正在运行、近期、失败和视窗中的 Session。
- Assistant 能指导、回答、取消、恢复和分叉受管 Session。
- Assistant 有自己的工作计划，并在当前计划完成后主动研究和扩展。
- 普通选择由 Assistant 推断或隔离试错，不长期等待用户。
- 用户可以配置是否允许 Assistant 对外公开发言。
- 用户可以暂停全部工作、安全重启并可靠恢复。
- 执行状态收敛到 Session，计划状态收敛到 Assistant Plan，没有长期双写或投影层。
