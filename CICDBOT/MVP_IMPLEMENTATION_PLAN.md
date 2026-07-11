# CICDBOT MVP 实施方案

> **文档状态：** 初稿 / 待评审  
> **目标读者：** 架构师、后端/前端开发工程师、实施团队、项目经理  
> **产品设计依据：** [CICDBOT 产品设计文档](PRODUCT_DESIGN.md)
> **前置验证：** [CICDBOT 两天 Demo 计划](DEMO_PLAN_2D.md)

---

## 目录

1. [MVP 目标与范围](#1-mvp-目标与范围)
2. [MVP 技术架构与 WorkGround2 复用矩阵](#2-mvp-技术架构与-workground2-复用矩阵)
3. [推荐目录和包结构](#3-推荐目录和包结构)
4. [PostgreSQL 数据模型](#4-postgresql-数据模型)
5. [API 清单](#5-api-清单)
6. [实时事件设计](#6-实时事件设计)
7. [执行器详细设计](#7-执行器详细设计)
8. [权限和审批矩阵](#8-权限和审批矩阵)
9. [首版连接器](#9-首版连接器)
10. [LLM 接入](#10-llm-接入)
11. [前端设计](#11-前端设计)
12. [里程碑规划](#12-里程碑规划)
13. [测试策略](#13-测试策略)
14. [部署方案](#14-部署方案)
15. [可观测性](#15-可观测性)
16. [上线门禁与风险](#16-上线门禁与风险)
17. [团队配置建议](#17-团队配置建议)

---

## 1. MVP 目标与范围

### MVP 目标

在 WorkGround2 的 Agent Runtime 基础上，构建一个面向运维团队的共享工作台 MVP，覆盖"调查 → 计划 → 审批 → 执行"核心闭环，支持 SSH/Linux 连接器。

### MVP 假设

| 假设 | 验证方式 |
|---|---|
| 运维团队需要共享工作区而非单人工具 | MVP 发布后与 1-2 个试点团队验证 |
| Agent 生成的计划能减少运维决策时间 | 对比 Agent 辅助 vs 纯手工的调查完成时间 |
| 审批流是可接受的变更安全阀 | 统计审批通过率、拒绝原因和用户反馈 |
| 运维人员愿意在浏览器中而非终端中执行命令 | 试点团队使用率和留存率 |

### MVP 范围

| 包含 | 不包含 |
|---|---|
| 单组织、基础多工作区隔离 | 多组织和跨组织共享 |
| SSH/Linux 连接器（完整） | Kubernetes 连接器和其他写操作连接器（P1+） |
| Agent 消息会话 + 工具调用 | Runbook（P2）、定时任务（P2） |
| 高风险操作审批流 | 双人审批（P1） |
| 共享任务（创建、认领、转交、关注、评论、完成） | 步骤级评论线程（P2） |
| 事件流 + SSE（断线补读） | WebSocket（P2） |
| 基本 RBAC（观察者 + 操作员 + 审批人 + 管理员） | 独立审计员角色（P1） |
| 审计日志（事件驱动写入） | 审计查询和导出界面（P2） |
| Shell 命令执行与输出流式展示 | 文件在线编辑、上传下载 |
| OpenAI-compatible LLM 基座，支持主/备 Provider | 按成本与质量的复杂路由策略（P1） |
| 计划生成、查看、审批与执行 | 计划协同编辑和版本比较（P1） |
| 一个只读观测连接器（Loki、Elasticsearch 或 Prometheus 三选一） | 多观测平台聚合（P1） |

### MVP 端到端验收场景

**场景 A：日志调查**

1. 操作员登录 CICDBOT，进入工作区
2. 创建共享任务："调查 Web 服务器高负载原因"
3. Agent 自动 SSH 登录目标主机，执行 `top`、`vmstat`、`journalctl -u nginx` 等命令
4. 命令输出流式显示在工作台中央消息流中
5. Agent 汇总分析结果，输出调查结论
6. 操作员追加追问，Agent 继续执行补充命令
7. 操作员确认结论，将任务标记完成

**场景 B：变更执行**

1. 操作员创建任务："Nginx 配置优化"
2. Agent 调查当前配置，生成变更计划（3 个步骤）
3. 计划显示在工作台计划查看器中
4. 操作员审查计划，确认无误
5. 计划包含 1 个高风险步骤（需要审批）
6. 审批请求通知在线审批人
7. 审批人批准后，执行器依次执行步骤
8. 每步输出实时显示
9. 全部步骤执行完毕，Agent 验证并汇总结果
10. 操作员完成确认

**场景 C：多人协作查看**

1. 操作员 A 正在执行 SSH 命令
2. 操作员 B 和观察者打开同一任务页面
3. B 看到 A 的操作全流程（实时流式展示）
4. B 看到 A 的每一步工具调用和输出
5. B 看到任务当前认领状态

---

## 2. MVP 技术架构与 WorkGround2 复用矩阵

![CICDBOT MVP 架构示意](docs/images/cicdbot-architecture-wireframe.png)

*↑ 用于说明团队用户、协作控制面、Agent 运行时、工具网关、模型路由和数据层之间的边界。*

### 复用策略 

| WorkGround2 组件 | 复用方式 | 修改级别 |
|---|---|---|
| `internal/control` — Controller | 作为 Agent Runtime 核心，新增 SessionAPI 端口 | 扩展接口 |
| `internal/agent` — Agent / Runner | 直接复用模型请求、工具调度、上下文管理 | 少量扩展 |
| `internal/provider` — Provider 适配器 | 直接复用 OpenAI/Anthropic/CLI Provider | 无修改 |
| `internal/tool` + `Registry` | 直接复用工具注册、执行、调度 | 无修改 |
| `internal/plugin` — MCP Client | 复用 MCP 协议，扩展运维连接器 | 新增连接器 MCP Server |
| `internal/permission` — Policy/Gate | 保持工具调用策略与审批闸门职责，不承载用户 RBAC | 小幅扩展操作上下文 |
| `internal/serve` — HTTP/SSE Server | 复用 SSE 事件推送、API 路由 | 扩展 API 端点 |
| `internal/memory` — 记忆系统 | 复用文档/记忆，扩展运维知识库 | 轻度适配 |
| `internal/boot` — 组装 | 复用组件装配过程 | 新增 CICDBOT 初始化流程 |

### 新增组件

| 新增组件 | 职责 | 基础包 |
|---|---|---|
| `cicdbot/controlplane` | 多用户管理、会话、工作区选择 | 新增 |
| `cicdbot/task` | 共享任务 CRUD、认领、转交、状态管理 | 新增 |
| `cicdbot/plan` | 计划生成、存储、版本 | 新增 |
| `cicdbot/approval` | 审批请求创建、路由、通知 | 新增 |
| `cicdbot/execution` | 操作队列、Lease、心跳、超时、重试 | 新增 |
| `cicdbot/gateway` | Tool Gateway，凭据注入、操作执行 | 新增 |
| `cicdbot/connector/ssh` | SSH/Linux 连接器（MCP Server） | 独立进程 |
| `cicdbot/rbac` | RBAC 权限模型与策略引擎 | 新增 |
| `cicdbot/audit` | 审计日志写入与查询 | 新增 |
| `cicdbot/store` | 事件存储、业务快照读写 | 新增 |
| `cicdbot/events` | 事件类型定义、事件总线 | 新增 |

---

## 3. 推荐目录和包结构

```
cicdbot/
├── cmd/
│   ├── cicdbot/                  # 主入口
│   │   └── main.go                # 服务启动、组件组装
│   └── connectors/               # 连接器入口
│       ├── ssh-server/            # SSH MCP Server 独立进程
│       │   └── main.go
│       └── k8s-server/            # K8s MCP Server 独立进程 (P1)
│           └── main.go
├── internal/
│   ├── controlplane/             # 多用户控制面
│   │   ├── user.go               # 用户管理
│   │   ├── session.go            # 用户会话管理
│   │   └── workspace.go          # 工作区管理
│   ├── task/                     # 共享任务
│   │   ├── model.go              # 任务领域模型
│   │   ├── service.go            # 任务 CRUD + 认领/转交
│   │   ├── store.go              # 任务持久化
│   │   └── handler.go            # HTTP handler
│   ├── plan/                     # 计划
│   │   ├── model.go              # 计划领域模型
│   │   ├── service.go            # 计划生成/存储
│   │   └── handler.go
│   ├── approval/                 # 审批
│   │   ├── model.go
│   │   ├── service.go            # 审批流程管理
│   │   └── handler.go
│   ├── execution/                # 执行引擎
│   │   ├── engine.go             # 执行引擎核心
│   │   ├── queue.go              # 操作队列
│   │   ├── lease.go              # 资源租赁
│   │   ├── step.go               # 执行步骤管理
│   │   ├── retry.go              # 重试与退避
│   │   └── recovery.go           # 服务重启恢复
│   ├── gateway/                  # Tool Gateway
│   │   ├── gateway.go            # Gateway 核心
│   │   ├── credential.go         # 凭据管理
│   │   └── executor.go           # 操作执行器
│   ├── rbac/                     # RBAC
│   │   ├── model.go              # 角色/权限模型
│   │   ├── policy.go             # 策略引擎
│   │   └── handler.go
│   ├── audit/                    # 审计
│   │   ├── audit.go              # 审计写入
│   │   └── query.go              # 审计查询
│   ├── events/                   # 领域事件
│   │   ├── types.go              # 事件类型定义
│   │   ├── bus.go                # 事件总线
│   │   └── store.go              # 事件持久化
│   ├── store/                    # 数据存储层
│   │   ├── db.go                 # PostgreSQL 连接
│   │   ├── event_store.go        # 事件追加存储
│   │   ├── snapshot.go           # 业务快照
│   │   └── migration/            # 数据库迁移
│   │       ├── 001_init.sql
│   │       └── ...
│   ├── api/                      # HTTP API
│   │   ├── router.go             # 路由注册
│   │   ├── middleware.go         # 认证/权限/审计中间件
│   │   └── response.go           # 统一响应格式
│   └── sse/                      # SSE 事件推送 (扩展 WorkGround2)
│       ├── hub.go                # 连接管理
│       └── catchup.go            # 断线补读
├── connectors/                   # 连接器 MCP Server 实现
│   ├── ssh/
│   │   ├── main.go               # MCP Server 入口
│   │   ├── session.go            # SSH 会话管理
│   │   ├── exec.go               # 命令执行
│   │   └── tools.go              # MCP 工具定义
│   └── k8s/                      # P1
├── web/                          # 前端 SPA (React/Vue)
│   ├── src/
│   │   ├── pages/
│   │   ├── components/
│   │   ├── stores/               # 状态管理，从事件推导
│   │   ├── api/                  # API 客户端
│   │   └── sse/                  # SSE 客户端
│   └── package.json
├── deploy/                       # 部署配置
│   ├── docker-compose.yml
│   ├── Dockerfile
│   └── k8s/                      # P1
└── config/
    ├── cicdbot.toml              # 配置文件模板
    └── connectors.toml           # 连接器配置
```

### 架构原则对照

| 原则 | 实现 |
|---|---|
| **Controller-first** | 所有前端（Web、CLI、未来 bot）驱动同一个 Controller |
| **Manager 偏协调** | WorkspaceManager、TaskManager 等负责统一入口和路由 |
| **领域状态入口收敛** | 状态只由领域 Service 方法修改，并在同一事务写入追加事件 |
| **状态单源** | PostgreSQL 关系表保存当前权威状态，事件表保存不可变历史与分发游标 |
| **数据层和 UI 层边界清楚** | Web 先加载服务端快照，再按游标应用 SSE 事件；写请求回包只确认接收或返回当前结果，不直接操作 Panel |

---

## 4. PostgreSQL 数据模型

### 表清单与设计说明

所有表使用 UUID v7 主键（按时间排序，适合 B-tree），`created_at` / `updated_at` 时间戳，`version` 乐观锁版本号。

#### organizations

```sql
CREATE TABLE organizations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name          VARCHAR(128) NOT NULL,
    slug          VARCHAR(64) UNIQUE NOT NULL,
    settings      JSONB DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### workspaces

```sql
CREATE TABLE workspaces (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id),
    name            VARCHAR(128) NOT NULL,
    slug            VARCHAR(64) NOT NULL,
    description     TEXT,
    settings        JSONB DEFAULT '{}',
    status          VARCHAR(32) NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, slug)
);
```

#### environments

```sql
CREATE TABLE environments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id),
    name            VARCHAR(64) NOT NULL,
    type            VARCHAR(32) NOT NULL DEFAULT 'testing', -- production/staging/testing
    risk_level      VARCHAR(16) NOT NULL DEFAULT 'low',
    settings        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(workspace_id, name)
);
```

#### resources

```sql
CREATE TABLE resources (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id),
    environment_id  UUID REFERENCES environments(id),
    name            VARCHAR(128) NOT NULL,
    resource_type   VARCHAR(64) NOT NULL,  -- host, k8s_cluster, db_instance
    address         VARCHAR(256),           -- hostname/IP
    metadata        JSONB DEFAULT '{}',     -- 额外元数据
    status          VARCHAR(32) NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_resources_ws_env ON resources(workspace_id, environment_id);
```

#### users

```sql
CREATE TABLE users (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username        VARCHAR(64) UNIQUE NOT NULL,
    display_name    VARCHAR(128) NOT NULL,
    email           VARCHAR(256),
    password_hash   VARCHAR(256),          -- 本地认证
    auth_provider   VARCHAR(32) DEFAULT 'local',
    auth_subject    VARCHAR(256),          -- OAuth/LDAP subject
    status          VARCHAR(32) NOT NULL DEFAULT 'active',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### role_assignments

```sql
CREATE TABLE role_assignments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         UUID NOT NULL REFERENCES users(id),
    workspace_id    UUID REFERENCES workspaces(id),  -- NULL = 全局角色
    role            VARCHAR(32) NOT NULL,  -- observer/operator/approver/admin/auditor
    created_by      UUID REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(user_id, workspace_id, role)
);
```

#### shared_tasks

核心表，映射一条持久化 Agent 会话。

```sql
CREATE TABLE shared_tasks (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    UUID NOT NULL REFERENCES workspaces(id),
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    status          VARCHAR(32) NOT NULL DEFAULT 'idle',
                    -- idle/running/investigating/planning/
                    -- awaiting_approval/executing/verifying/
                    -- completed/failed/cancelled
    owner_id        UUID NOT NULL REFERENCES users(id),
    claimant_id     UUID REFERENCES users(id),    -- 当前执行人
    priority        VARCHAR(16) DEFAULT 'normal',
    tags             TEXT[],                        -- 标签
    context         JSONB DEFAULT '{}',             -- 任务上下文（环境、资源）
    version         INT NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_tasks_ws_status ON shared_tasks(workspace_id, status);
CREATE INDEX idx_tasks_claimant ON shared_tasks(claimant_id) WHERE claimant_id IS NOT NULL;
```

#### task_watchers

```sql
CREATE TABLE task_watchers (
    task_id         UUID NOT NULL REFERENCES shared_tasks(id),
    user_id         UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, user_id)
);
```

#### messages

Agent 会话消息，追加写入。

```sql
CREATE TABLE messages (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL REFERENCES shared_tasks(id),
    sequence        BIGINT NOT NULL,              -- 任务内序号
    role            VARCHAR(32) NOT NULL,          -- user/assistant/tool/system
    content         TEXT,
    tool_calls      JSONB,                         -- 工具调用
    tool_results    JSONB,                         -- 工具结果
    actor_id        UUID,
    actor_type      VARCHAR(32) DEFAULT 'user',    -- user/agent/system
    metadata        JSONB DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(task_id, sequence)
);
CREATE INDEX idx_messages_task_seq ON messages(task_id, sequence);
```

#### plans

```sql
CREATE TABLE plans (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    task_id         UUID NOT NULL REFERENCES shared_tasks(id),
    version         INT NOT NULL DEFAULT 1,
    title           VARCHAR(255) NOT NULL,
    summary         TEXT,
    status          VARCHAR(32) NOT NULL DEFAULT 'draft',
                    -- draft/reviewing/approved/rejected/executing/
                    -- completed/partially_completed/failed
    created_by      VARCHAR(32) NOT NULL,           -- user/agent
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

#### plan_steps

```sql
CREATE TABLE plan_steps (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    plan_id         UUID NOT NULL REFERENCES plans(id),
    sequence        INT NOT NULL,
    title           VARCHAR(255) NOT NULL,
    description     TEXT,
    action          VARCHAR(64) NOT NULL,           -- run_command / api_call / runbook
    params          JSONB NOT NULL,
    expected_result TEXT,
    risk_level      VARCHAR(16) NOT NULL DEFAULT 'low',
    status          VARCHAR(32) NOT NULL DEFAULT 'pending',
                    -- pending/running/succeeded/failed/skipped/cancelled
    result          JSONB,
    error           TEXT,
    retry_count     INT DEFAULT 0,
    max_retries     INT DEFAULT 2,
    rollback_action JSONB,                          -- 回滚操作
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    UNIQUE(plan_id, sequence)
);
```

#### approvals

```sql
CREATE TABLE approvals (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id        UUID NOT NULL,                 -- 幂等键
    workspace_id      UUID NOT NULL REFERENCES workspaces(id),
    task_id           UUID NOT NULL REFERENCES shared_tasks(id),
    plan_id           UUID REFERENCES plans(id),
    operation_id      UUID,
    resource_id       UUID REFERENCES resources(id),
    risk_level        VARCHAR(16) NOT NULL,
    status            VARCHAR(32) NOT NULL DEFAULT 'pending',
                      -- pending/approved/rejected/expired/cancelled
    requested_by      UUID NOT NULL,
    required_count    INT NOT NULL DEFAULT 1,        -- 需要审批人数
    context           JSONB,                         -- 审批上下文
    expires_at        TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(request_id)
);
CREATE INDEX idx_approvals_status ON approvals(status) WHERE status = 'pending';
```

#### approval_decisions

每位审批人的决定独立保存，避免并发审批时更新同一数组，也为双人审批和审计保留清晰证据。

```sql
CREATE TABLE approval_decisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    approval_id     UUID NOT NULL REFERENCES approvals(id),
    approver_id     UUID NOT NULL REFERENCES users(id),
    request_id      UUID NOT NULL UNIQUE,
    decision        VARCHAR(16) NOT NULL, -- approved/rejected
    reason          TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(approval_id, approver_id)
);
```

#### operations

```sql
CREATE TABLE operations (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    idempotency_key     VARCHAR(255) UNIQUE NOT NULL,
    request_id          UUID NOT NULL,
    actor_id            UUID NOT NULL,
    workspace_id        UUID NOT NULL REFERENCES workspaces(id),
    task_id             UUID REFERENCES shared_tasks(id),
    resource_id         UUID REFERENCES resources(id),
    execution_step_id   UUID REFERENCES plan_steps(id),
    connector_type      VARCHAR(64) NOT NULL,
    action              VARCHAR(128) NOT NULL,
    params              JSONB NOT NULL,
    risk_level          VARCHAR(16) NOT NULL DEFAULT 'low',
    status              VARCHAR(32) NOT NULL DEFAULT 'queued',
                        -- queued/running/cancelling/succeeded/failed/
                        -- cancelled/timed_out/manual_required
    lease_id            UUID,
    lease_expires_at    TIMESTAMPTZ,
    result              JSONB,
    error               TEXT,
    retry_count         INT DEFAULT 0,
    max_retries         INT DEFAULT 2,
    timeout_seconds     INT DEFAULT 30,
    started_at          TIMESTAMPTZ,
    completed_at        TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    version             INT NOT NULL DEFAULT 1
);
CREATE INDEX idx_ops_idempotency ON operations(idempotency_key);
CREATE INDEX idx_ops_status ON operations(status) WHERE status IN ('queued', 'running');
```

#### events

追加事件存储。

```sql
CREATE TABLE events (
    event_id        UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_type      VARCHAR(64) NOT NULL,
    event_version   INT DEFAULT 1,
    aggregate_type  VARCHAR(64) NOT NULL,
    aggregate_id    UUID NOT NULL,
    sequence        BIGINT NOT NULL,
    request_id      UUID,
    actor_id        UUID,
    workspace_id    UUID,
    task_id         UUID,
    resource_id     UUID,
    payload         JSONB NOT NULL,
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at    TIMESTAMPTZ,
    publish_attempts INT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(aggregate_type, aggregate_id, sequence)
);
CREATE INDEX idx_events_type ON events(event_type);
CREATE INDEX idx_events_ws_time ON events(workspace_id, timestamp);
CREATE INDEX idx_events_aggregate ON events(aggregate_type, aggregate_id, sequence);
CREATE INDEX idx_events_unpublished ON events(created_at) WHERE published_at IS NULL;
```

#### audit_logs

```sql
CREATE TABLE audit_logs (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id      UUID NOT NULL,
    actor_id        UUID NOT NULL,
    actor_type      VARCHAR(32) NOT NULL,   -- user/agent/system
    workspace_id    UUID,
    task_id         UUID,
    action          VARCHAR(128) NOT NULL,  -- task.create, operation.execute, ...
    resource_type   VARCHAR(64),
    resource_id     UUID,
    risk_level      VARCHAR(16),
    status          VARCHAR(32),            -- allowed/denied/succeeded/failed
    detail          JSONB,
    ip_address      INET,
    user_agent      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_audit_actor ON audit_logs(actor_id, created_at);
CREATE INDEX idx_audit_action ON audit_logs(action, created_at);
CREATE INDEX idx_audit_request ON audit_logs(request_id);
```

#### leases

```sql
CREATE TABLE leases (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    resource_id     UUID NOT NULL REFERENCES resources(id),
    holder_id       UUID NOT NULL,           -- 持有者（用户或 Agent）
    holder_type     VARCHAR(32) NOT NULL,
    task_id         UUID REFERENCES shared_tasks(id),
    lease_type      VARCHAR(32) DEFAULT 'exclusive',
    acquired_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    heartbeat_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    released_at     TIMESTAMPTZ,
    version         INT NOT NULL DEFAULT 1
);
CREATE UNIQUE INDEX idx_leases_active_resource
    ON leases(resource_id, lease_type)
    WHERE released_at IS NULL;
```

#### secrets (SecretRef)

```sql
CREATE TABLE secrets (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            VARCHAR(128) NOT NULL,
    secret_type     VARCHAR(32) NOT NULL,      -- ssh_key/password/token/cert
    vault_ref       VARCHAR(512) NOT NULL,     -- 外部密钥管理引用
    connector_type  VARCHAR(64) NOT NULL,
    resource_id     UUID REFERENCES resources(id),
    rotation_policy JSONB,
    created_by      UUID NOT NULL REFERENCES users(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

### 当前状态与追加事件关系

```
请求 → 领域 Service → PostgreSQL 事务
                        ├─ 更新当前状态表（shared_tasks / plans / operations）
                        ├─ 追加 events（时间线 + Outbox）
                        └─ 追加 audit_logs（需要审计的动作）

Outbox Publisher → 读取未发布 events → SSE Hub / 内部消费者 → 标记 published_at
```

- **当前状态表**是业务判断的权威来源，状态迁移通过少数领域 Service 收敛。
- **events** 是不可变历史、共享时间线和可靠分发 Outbox，不要求 MVP 通过全量回放重建业务表。
- 状态更新与事件追加在同一事务提交；事务失败时两者一起回滚。
- Publisher 使用 `event_id` 去重并允许重试；消费者按游标和事件版本幂等处理。
- 服务重启后直接读取当前状态，再扫描未发布事件和未完成操作进行补发、恢复和对账。

---

## 5. API 清单

### 认证（Auth）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/login` | 用户登录，返回 Token |
| POST | `/api/auth/logout` | 登出，失效 Token |
| POST | `/api/auth/refresh` | 刷新 Token |
| GET | `/api/auth/me` | 当前用户信息 |

### 工作区（Workspace）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/workspaces` | 列当前用户所属工作区 |
| POST | `/api/workspaces` | 创建工作区 |
| GET | `/api/workspaces/:id` | 工作区详情 |
| PUT | `/api/workspaces/:id` | 更新工作区 |
| DELETE | `/api/workspaces/:id` | 删除工作区 |

### 资源（Resource）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/workspaces/:id/resources` | 资源列表 |
| POST | `/api/workspaces/:id/resources` | 注册资源 |
| GET | `/api/resources/:id` | 资源详情 |
| PUT | `/api/resources/:id` | 更新资源 |
| DELETE | `/api/resources/:id` | 注销资源 |

### 任务（Task）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/workspaces/:id/tasks` | 任务列表 |
| POST | `/api/workspaces/:id/tasks` | 创建任务 |
| GET | `/api/tasks/:id` | 任务详情 |
| PUT | `/api/tasks/:id` | 更新任务 |
| POST | `/api/tasks/:id/claim` | 认领任务（Body: `{ "claimant_id" }`） |
| POST | `/api/tasks/:id/transfer` | 转交任务（Body: `{ "to_user_id" }`） |
| POST | `/api/tasks/:id/watch` | 关注/取消关注 |
| POST | `/api/tasks/:id/complete` | 完成任务 |
| POST | `/api/tasks/:id/cancel` | 取消任务 |

### 消息与评论（Message / Comment）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/tasks/:id/messages` | 消息历史（cursor 分页） |
| POST | `/api/tasks/:id/messages` | 发送消息 |
| GET | `/api/messages/:id` | 消息详情 |
| POST | `/api/tasks/:id/comments` | 添加评论 |

### 计划（Plan）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/tasks/:id/plans` | 生成/创建计划 |
| GET | `/api/tasks/:id/plans` | 计划列表 |
| GET | `/api/plans/:id` | 计划详情（含步骤） |
| PUT | `/api/plans/:id` | 更新计划 |
| POST | `/api/plans/:id/submit` | 提交审批 |

### 审批（Approval）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/approvals` | 待审批列表 |
| GET | `/api/approvals/:id` | 审批详情 |
| POST | `/api/approvals/:id/approve` | 批准 |
| POST | `/api/approvals/:id/reject` | 拒绝（Body: `{ "reason" }`） |

### 执行（Execution）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/plans/:id/execute` | 开始执行计划 |
| GET | `/api/executions/:id` | 执行进度 |
| POST | `/api/executions/:id/cancel` | 取消执行 |
| POST | `/api/steps/:id/retry` | 重试步骤 |
| POST | `/api/steps/:id/skip` | 跳过步骤 |

### 事件（Events）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/events` | 历史事件查询（支持 cursor、type、task_id 过滤） |
| GET | `/api/events/stream` | SSE 事件流 |

### 审计（Audit）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/audit-logs` | 审计日志查询（actor、action、时间范围） |

### Provider 管理

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/admin/providers` | LLM Provider 列表 |
| POST | `/api/admin/providers` | 添加 Provider |
| PUT | `/api/admin/providers/:id` | 更新 Provider |
| POST | `/api/admin/providers/:id/test` | 测试连接 |

---

## 6. 实时事件设计

### 事件信封（Envelope）

所有事件统一信封格式：

```json
{
  "event_id": "uuid-v7",
  "event_type": "message.sent",
  "event_version": 1,
  "sequence": 1042,
  "workspace_id": "uuid",
  "task_id": "uuid",
  "resource_id": "uuid-or-null",
  "request_id": "uuid-v7",
  "actor_id": "uuid",
  "actor_type": "user|agent|system",
  "timestamp": "2026-07-10T12:00:00.000Z",
  "payload": {},
  "trace_id": "uuid"
}
```

### 事件类型（≥ 12 个）

| 事件类型 | 触发条件 | Payload 关键字段 |
|---|---|---|
| `task.created` | 创建共享任务 | title, description, owner_id |
| `task.claimed` | 用户认领任务 | claimant_id |
| `task.transferred` | 任务转交 | from_user_id, to_user_id |
| `task.completed` | 完成任务 | result_summary |
| `task.cancelled` | 取消任务 | reason |
| `task.watcher.added` | 添加关注者 | user_id |
| `task.watcher.removed` | 取消关注 | user_id |
| `message.sent` | 发送消息 | message_id, role, content, tool_calls |
| `comment.added` | 成员添加评论 | comment_id, parent_id, mentioned_user_ids |
| `presence.changed` | 成员在线状态变化 | user_id, status, expires_at |
| `tool.dispatched` | 工具调用 | tool_name, params, risk_level, connector_type |
| `tool.result` | 工具返回结果 | tool_name, result, error, latency_ms |
| `plan.generated` | Agent 生成计划 | plan_id, step_count, summary |
| `plan.submitted` | 计划提交审批 | plan_id, risk_level |
| `approval.requested` | 发起审批请求 | approval_id, risk_level, operation_detail |
| `approval.approved` | 审批通过 | approval_id, approver_id |
| `approval.rejected` | 审批拒绝 | approval_id, approver_id, reason |
| `approval.expired` | 审批超时 | approval_id |
| `execution.started` | 开始执行计划 | plan_id, step_count |
| `execution.step.started` | 步骤开始执行 | step_id, action, params |
| `execution.step.completed` | 步骤完成 | step_id, result |
| `execution.step.failed` | 步骤失败 | step_id, error, retry_count |
| `execution.completed` | 计划全部完成 | plan_id, summary |
| `execution.cancelled` | 执行取消 | plan_id, reason |
| `lease.acquired` | 获取资源租赁 | resource_id, holder_id, expires_at |
| `lease.released` | 释放资源租赁 | resource_id, holder_id |
| `lease.expired` | 租赁超时 | resource_id, holder_id |
| `connection.status_changed` | 连接器状态变化 | connector_type, status, error |
| `system.announcement` | 系统公告 | message, severity |

---

## 7. 执行器详细设计

### 设计目标

- 可靠执行运维操作，支持幂等、超时、重试、退避、取消、补偿
- 服务重启后恢复未完成的操作
- 所有操作进入事件流和审计日志

### 架构

```
执行引擎（Execution Engine）
├─ 队列（Queue）— 操作排队，按 FIFO + 优先级
├─ 工作线程池（Worker Pool）— 并发执行操作
├─ Lease 管理器 — 资源租赁
├─ 心跳（Heartbeat）— 操作执行中定期心跳
├─ 超时（Timeout）— 单步超时控制
├─ 重试/退避（Retry/Backoff）— 失败重试策略
├─ 取消（Cancellation）— 操作取消分发
├─ 补偿（Compensation）— 失败回滚
├─ 验证（Verification）— 执行结果验证
└─ 恢复（Recovery）— 服务重启恢复
```

### 幂等键

- 每个操作由 `idempotency_key` 唯一标识
- 格式：`{connector_type}/{action}/{request_id}/{sequence}`
- operations 表中 `idempotency_key` 为 UNIQUE
- 重复请求直接返回已存在的操作结果
- 只对已分类为可重试且可验证幂等的动作自动重试；网络超时等结果未知状态先执行只读核对

### 队列

| 特性 | 说明 |
|---|---|
| 存储 | PostgreSQL（operations 表，status = 'queued'） |
| 排序 | 显式业务优先级 + created_at；风险等级只控制权限和审批，不用于插队 |
| 轮询 | 后台 Worker 定时轮询（每 1s 查询一次） |
| 并发控制 | 每个连接器类型维护独立的并发槽位（SSH 默认 3 并发） |

### Lease

| 特性 | 说明 |
|---|---|
| 获取条件 | 操作目标资源未被其他 Lease 持有，或持有者超时 |
| 独占模式 | 同一资源同时只能有一个写操作 |
| 超时释放 | 默认 15 分钟，Lease 到期自动释放 |
| 心跳续约 | 执行中每 30s 发送心跳，维持 Lease |
| 恢复检查 | 服务重启后检查到期 Lease，标记为释放 |

### 心跳

- Worker 每 30s 更新 operations 表的 lease_expires_at
- 心跳失败 3 次（90s 无心跳）视为 Worker 崩溃，操作标记为 failed

### 超时

| 配置 | 默认值 | 说明 |
|---|---|---|
| 连接超时 | 10s | SSH 连接建立 |
| 命令超时 | 30s | 单条命令执行（可重写） |
| 步骤超时 | 120s | 单个计划步骤（含重试总时间） |
| 计划执行超时 | 1800s | 整个计划执行 |

### 重试与退避

```mermaid
graph TD
    StepFailed --> IsRetryable{可重试?}
    IsRetryable -->|是| HasRetries{剩余重试次数 > 0?}
    HasRetries -->|是| WaitBackoff[退避等待]
    WaitBackoff --> Retry[重新执行]
    Retry --> StepOk{成功?}
    StepOk -->|否| IsRetryable
    HasRetries -->|否| MarkFailed[标记为最终失败]
    IsRetryable -->|否| MarkFailed
```

| 重试次数 | 退避时间 |
|---|---|
| 1 | 1s |
| 2 | 5s |
| 3 | 15s（默认最大重试=2，第三次为手动重试） |

### 取消

- 取消请求先把 operations 状态设置为 `cancelling`，重复取消返回当前状态
- 正在执行的操作通过 Context 的 cancel 函数通知
- SSH 连接器收到取消通知后关闭连接
- 连接器确认停止后进入 `cancelled`；无法确认远端状态时进入 `manual_required`
- 计划是否继续由步骤策略决定，默认停止后续写操作

### 补偿

- Plan 步骤定义 `rollback_action`（可选）
- 步骤失败或操作员触发回滚时，按逆序执行已成功步骤的回滚操作
- 回滚操作同样经过 Tool Gateway 执行，风险级别与正向操作相同
- 回滚失败需要手动介入

### 验证

- 步骤定义 `expected_result`（可选预期输出或状态）
- 执行完成后，Agent 或验证器检查结果是否符合预期
- 验证失败触发重试或标记为失败

### 服务重启恢复

```
服务启动 → 启动恢复流程
1. 查询 status IN ('running', 'queued', 'cancelling') 的操作
2. 对 `queued` 操作用 `FOR UPDATE SKIP LOCKED` 重新认领，重复认领仍由幂等键去重
3. 对 `running/cancelling` 操作核对 Worker 实例租约；实例仍存活时不抢占
4. 原 Worker 已失联时，连接器使用 operation_id/idempotency_key 查询或验证远端实际结果
5. 已确认成功则补记成功；已确认未执行且动作可安全重试则重新排队
6. 结果未知或动作不可安全重试时进入 `manual_required`，显式提示人工核对，禁止盲目重放
7. 扫描未发布 events 并补发，记录 recovery.completed 及每个操作的恢复决策
```

### 迟到结果处理

- 执行器可能因为网络延迟收到已超时操作的执行结果
- 策略：检查操作当前状态
  - 状态已为 `succeeded` → 以幂等方式确认，不重复产生副作用事件
  - 状态为 `failed` / `cancelled` / `timed_out` → 记录迟到结果并进入核对流程；若远端可能已产生副作用，标记 `manual_required`
  - 状态仍为 `running` (因 Lease 续约存活) → 接受结果，更新状态

---

## 8. 权限和审批矩阵

### 权限矩阵

| 操作 | 观察者 | 操作员 | 审批人 | 管理员 | 审计员 |
|---|---|---|---|---|---|
| 查看工作区 | ✅ | ✅ | ✅ | ✅ | ✅ (审计视图) |
| 查看任务 | ✅ | ✅ | ✅ | ✅ | — |
| 创建任务 | — | ✅ | ✅ | ✅ | — |
| 认领任务 | — | ✅ | ✅ | ✅ | — |
| 转交任务 | — | ✅ | ✅ | ✅ | — |
| 发送消息 | — | ✅ | ✅ | ✅ | — |
| 查看执行输出 | ✅ | ✅ | ✅ | ✅ | — |
| 执行低风险操作 | — | ✅ (自动) | ✅ (自动) | ✅ | — |
| 执行中风险操作 | — | ✅ (需审批) | ✅ (自动) | ✅ | — |
| 执行高风险操作 | — | 需审批 | 需审批 | ✅ | — |
| 审批请求 | — | — | ✅ | ✅ | — |
| 管理用户 | — | — | — | ✅ | — |
| 管理资源 | — | — | — | ✅ | — |
| 管理连接器 | — | — | — | ✅ | — |
| 查看审计日志 | — | — | — | ✅ | ✅ |
| 查询审计日志 | — | — | — | ✅ | ✅ |

### 审批矩阵

| 场景 | 环境 | 风险级别 | 审批要求 | 审批人 |
|---|---|---|---|---|
| 只读命令（查看日志、检查状态） | 所有 | low | 无审批 | — |
| 修改命令（重启服务、修改配置） | 测试 | medium | 单人审批 | 审批人 |
| 修改命令（重启服务、修改配置） | 生产 | high | 单人审批 | 审批人 |
| 破坏性命令（停止服务、删除文件） | 测试 | high | 单人审批 | 审批人 |
| 破坏性命令（停止服务、删除文件） | 生产 | critical | 双人审批 | 2 位审批人 |
| 批量变更 | 生产 | critical | 双人审批 | 2 位审批人 |

### 审批幂等保护

- 审批请求和每次审批决定分别使用独立 `request_id`
- `(approval_id, approver_id)` 唯一，同一审批人重复点击直接返回原决定
- 审批聚合状态与 `approval_decisions` 在同一事务更新；达到 `required_count` 后只产生一次 `approval.approved`
- 审批通过后变更操作由 `idempotency_key` 保证不重复执行
- 审批被拒绝后，修改计划可重新提交（新 `request_id`）

---

## 9. 首版连接器

### SSH/Linux 连接器（MVP 完整闭环）

**实现方式：** 独立的 MCP Server 进程（Go 实现），通过 stdio 与 CICDBOT 主进程通信。

**工具清单：**

| MCP 工具名称 | 说明 | 风险级别 |
|---|---|---|
| `ssh_run_command` | 执行远程命令 | low / high（由命令决定） |
| `ssh_read_file` | 读取远程文件内容 | low |
| `ssh_write_file` | 写入远程文件 | high |
| `ssh_check_service` | 检查服务状态 | low |
| `ssh_list_processes` | 列进程列表 | low |
| `ssh_disk_usage` | 磁盘使用情况 | low |
| `ssh_memory_usage` | 内存使用情况 | low |
| `ssh_tail_log` | 查看日志 | low |
| `ssh_grep_log` | 搜索日志 | low |
| `ssh_stat` | 文件/目录状态 | low |

**风险判断逻辑：**

- 命令白名单决定风险级别
- 写入类命令（sed、echo >、rm、dd、mkfs、chmod、chown）：high
- 只读类命令（cat、grep、tail、top、ps、df、free、journalctl）：low
- 可执行命令（systemctl restart、service start、kill）：medium/high

**凭据管理：**

```
SSH MCP Server 只接收 SecretRef 和短期授权上下文
├─ Tool Gateway 根据任务、用户和资源校验权限
├─ 执行时向 Vault/凭据代理换取短期凭据
├─ 凭据仅存在于连接器内存，不写日志、不进入 LLM 上下文
└─ 支持 ProxyJump、端口和主机指纹约束
```

### Kubernetes 连接器（P1 只读 + P2 受控动作）

**候选工具清单：**

| MCP 工具名称 | 说明 | 风险级别 |
|---|---|---|
| `k8s_get_pods` | 查看 Pod 列表 | low |
| `k8s_get_services` | 查看 Service 列表 | low |
| `k8s_get_deployments` | 查看 Deployment 列表 | low |
| `k8s_get_nodes` | 查看 Node 状态 | low |
| `k8s_get_events` | 查看 K8s 事件 | low |
| `k8s_describe` | 查看资源详情 | low |
| `k8s_logs` | 查看 Pod 日志 | low |
| `k8s_rollout_status` | 查看滚动更新状态 | low |
| `k8s_restart_deployment` | 重启 Deployment（受控） | high（需审批） |

### 观测连接器（MVP 至少一个）

**日志查询连接器（样例）：**

| MCP 工具名称 | 说明 | 风险级别 |
|---|---|---|
| `log_query` | 查询日志 | low |
| `log_tail` | 实时日志流 | low |
| `log_stats` | 日志统计 | low |

对接目标：Elasticsearch / Loki / 任意 HTTP 日志 API。

---

## 10. LLM 接入

### Provider 配置

```toml
[provider]
  # 主 Provider：企业 OpenAI-compatible LLM 基座
  [provider.primary]
    kind = "openai"
    base_url = "${PRIMARY_LLM_BASE_URL}"
    api_key_env = "PRIMARY_LLM_API_KEY"
    model = "${PRIMARY_LLM_MODEL}"
    max_tokens = 16384
    temperature = 0.3  # 降低创造力，提高确定性

  # 备用 Provider（可选，主 Provider 不可用时自动切换）
  [provider.fallback]
    kind = "openai"
    base_url = "${FALLBACK_LLM_BASE_URL}"
    api_key_env = "FALLBACK_LLM_API_KEY"
    model = "${FALLBACK_LLM_MODEL}"
    max_tokens = 16384

  # 路由策略
  [provider.routing]
    strategy = "priority"       # priority / round_robin / latency_based
    health_check_interval = "60s"
    fallback_on_timeout = true
    fallback_on_error = true
```

### 模型路由

| 任务类型 | 推荐模型 | 说明 |
|---|---|---|
| 调查类（信息搜集、日志分析） | `large_context` 能力组 | 需要长上下文和稳定工具调用 |
| 计划生成与风险分析 | `reasoning` 能力组 | 需要结构化输出和高可靠推理 |
| 工具调用 | `tool_use` 能力组 | 必须通过工具 schema 契约测试 |
| 摘要与普通回复 | `economy` 能力组 | 使用低成本模型降低总消耗 |

### 健康检查

- 每 60s 向 Provider 发送轻量请求（`/v1/models` 或 `/health`）
- 连续 3 次失败标记 Provider 不可用
- 不可用 Provider 自动从路由表中移除
- Provider 恢复后重新加入路由表

### Token / 成本限制

| 限制 | 默认值 | 说明 |
|---|---|---|
| 单次请求最大 Token | 按模型上下文和任务策略配置 | 超出时先压缩证据，不直接截断关键操作上下文 |
| 工作区日 Token 上限 | 管理员配置 | 超限后停止新长任务或显式降级 |
| 工作区月成本预算 | 管理员配置 | 达到分级阈值时告警、限流或阻止新任务 |
| 单任务工具/轮次上限 | 按风险策略配置 | 防止 Agent 失控循环 |

---

## 11. 前端设计

### 技术选型

| 选项 | 决策 | 依据 |
|---|---|---|
| 框架 | React 18+ / Vue 3 | 待确认（D1） |
| 状态管理 | Zustand / Vue Pinia | 轻量、事件驱动友好 |
| SSE 客户端 | EventSource | 浏览器原生支持 |
| UI 组件库 | Ant Design / Element Plus | 企业级、表格/表单丰富 |
| 构建工具 | Vite | 快速 HMR |

### 状态管理原则

- **服务端状态单源：** PostgreSQL 关系状态是权威来源；前端不自行推断审批或执行成功。
- **快照 + 增量事件：** 页面打开时读取 API 快照和 `event_cursor`，再按序消费 SSE；发现游标缺口时重新拉取快照。
- **网络回包不直接操作 Panel：** 写请求回包只确认接收或返回当前幂等结果，领域事件驱动后续 UI 状态变化。
- **受控乐观更新：** 消息发送可显示本地 `pending` 项，使用 `request_id` 与服务端事件合并；变更、审批和执行禁止乐观成功。

### 状态存储结构

```typescript
// 应用状态（Zustand / Pinia store）
interface AppStore {
  // 当前用户
  currentUser: User;
  
  // 工作区
  workspaces: Workspace[];
  currentWorkspaceId: string;
  
  // 资源
  resources: Map<string, Resource>;
  resourcesByEnv: Map<string, Resource[]>;
  
  // 任务列表
  tasks: Task[];
  currentTaskId: string;
  
  // 当前任务详情
  currentTask: {
    task: Task;
    messages: Message[];
    onlineMembers: Member[];
    claims: ClaimInfo;
    activePlan: Plan | null;
    executionStatus: ExecutionStatus | null;
    pendingApprovals: Approval[];
  };
  
  // 通知
  notifications: Notification[];
}
```

### 关键页面组件

| 页面 | 组件 | 数据源 |
|---|---|---|
| 工作台 | WorkspaceLayout | 初始 API 加载 + SSE 实时更新 |
| 任务列表 | TaskSidebar | GET /api/workspaces/.../tasks |
| 消息流 | MessageList | GET /api/tasks/.../messages + SSE 追加 |
| 消息输入 | MessageInput | POST /api/tasks/.../messages |
| 计划查看器 | PlanViewer | GET /api/plans/... |
| 执行进度 | ExecutionProgress | SSE `execution.step.*` 事件 |
| 审批面板 | ApprovalPanel | SSE `approval.*` 事件 |
| 成员列表 | MemberList | SSE `task.*` + 实时通知 |

---

## 12. 里程碑规划

### 10 周里程碑总览

```
第 1-2 周：基础架构搭建（Epic 1）
第 3-4 周：领域模型与 API（Epic 2）
第 5-6 周：Agent 集成与 SSH 连接器（Epic 3）
第 7-8 周：审批执行与前端（Epic 4）
第 9 周：集成测试与修复（Epic 5）
第 10 周：部署上线与验收（Epic 6）
```

### Epic 1：基础架构搭建（第 1-2 周）

**依赖：** WorkGround2 代码库就绪

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| 项目初始化，Go Module 配置 | 1d | cicdbot 项目骨架 | 编译通过 |
| 目录结构和包组织 | 1d | 初步目录结构 | 代码规范检查通过 |
| PostgreSQL 数据库层 | 2d | 连接池、迁移框架、基础表 | 迁移脚本创建表成功 |
| 事件存储和事件总线 | 2d | Event Store 接口 + PostgreSQL 实现 | 单元测试通过 |
| 认证基础（用户注册/登录） | 2d | 用户管理、JWT Token | API 测试通过 |
| 配置加载 | 1d | cicdbot.toml 配置系统 | 配置系统可用 |

**退出条件：** 服务启动、数据库迁移完成、用户注册/登录可用、事件写入和读取通过测试

### Epic 2：领域模型与 API（第 3-4 周）

**依赖：** Epic 1 完成

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| 工作区 CRUD | 1d | 工作区管理 API | 创建/查看/删除工作区可用 |
| 资源和环境管理 | 2d | 资源注册 API、环境管理 | 注册/查看/删除资源可用 |
| 共享任务（CRUD、认领、转交、关注） | 2d | 任务管理 API | 团队交接和关注可用 |
| 消息、评论和在线状态 | 2d | 消息/评论 API、Presence TTL | 多人上下文和在线状态可见 |
| RBAC 权限引擎 | 2d | 角色分配、权限检查 | 权限矩阵测试通过 |
| 审计日志写入 | 1d | 审计日志写入中间件 | 写操作全部记录审计 |

**退出条件：** 所有领域 API 可用、RBAC 权限检查生效、审计日志写入

### Epic 3：Agent 集成与 SSH 连接器（第 5-6 周）

**依赖：** Epic 2 完成

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| WorkGround2 Controller 集成 | 2d | Controller 嵌入 cicdbot | 消息通过 Controller 路由到 Agent |
| 运维 Agent 上下文构建 | 1d | 工作区上下文注入 | Agent 能感知环境和资源信息 |
| SSH MCP Server 开发 | 3d | SSH 连接器（10+ 工具） | 命令执行、文件读写、日志查看可用 |
| Tool Gateway 实现 | 2d | 凭据注入、风险分级 | 凭据不进入 Agent 上下文 |
| 只读观测连接器 | 1d | Loki、Elasticsearch 或 Prometheus 三选一 | 查询结果可进入任务证据流 |
| Agent 调查流程端到端 | 2d | Agent 自主动调查完成 | E2E 场景 A 通过 |

**退出条件：** Agent 通过 SSH 连接器执行命令、输出流式显示、凭据隔离验证通过

### Epic 4：审批执行与前端（第 7-8 周）

**依赖：** Epic 3 完成

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| 计划生成与查看 | 2d | Agent 生成结构化计划、计划查看 API | 计划生成并显示 |
| 审批服务 | 2d | 审批请求/批准/拒绝 | 审批流程通过测试 |
| 执行引擎 | 3d | 队列、Lease、心跳、超时、重试 | 单步执行、失败重试可用 |
| 实时事件 + SSE | 2d | SSE Hub、事件推送 | 事件实时推送到浏览器 |
| 前端 SPA 工作台 | 3d | 消息流、计划视图、执行进度 | 页面对接所有 API |
| 断线补读 | 1d | Catch-up 事件 | 断线后重连补读事件 |

**退出条件：** 调查 → 计划 → 审批 → 执行全程可用、多人实时查看

### Epic 5：集成测试与修复（第 9 周）

**依赖：** Epic 4 完成

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| 集成测试编写 | 2d | 全流程集成测试 | 3 个 E2E 场景通过 |
| 故障注入测试 | 1d | 超时/网络断开/服务重启 | 恢复逻辑验证通过 |
| 安全测试 | 1d | 权限绕过/注入测试 | 安全测试通过 |
| 性能测试 | 1d | 并发测试 | 满足 SLO |
| Bug 修复 | 2d | 修复清单 | 所有 P0/P1 Bug 修复 |

**退出条件：** 全部测试通过、安全测试通过、SLO 达标

### Epic 6：部署上线与验收（第 10 周）

**依赖：** Epic 5 完成

| Story | 工时 | 交付物 | 退出条件 |
|---|---|---|---|
| Docker Compose 部署配置 | 1d | docker-compose.yml + Dockerfile | 一键启动成功 |
| 配置模板和文档 | 1d | 部署文档、配置说明 | 新团队成员按文档部署成功 |
| 试点团队验收 | 2d | 验收测试 | 3 个验收场景全部通过 |
| 监控告警配置 | 1d | Prometheus + Grafana 面板 | 指标可用 |
| 安全加固 | 1d | 安全审查 + 日志审计 | 安全审查通过 |

**退出条件：** Docker Compose 部署成功、试点团队验收通过、监控告警配置完成

---

## 13. 测试策略

### 测试金字塔

```
          ╱  E2E (Playwright)  ╲         ← 5-10% 人力
         ╱   集成测试 (Go test)   ╲       ← 15-20% 人力
        ╱    故障注入测试 (chaos)    ╲     ← 10% 人力
       ╱      单元测试 (Go test)        ╲   ← 60-70% 人力
```

### 单元测试

| 范围 | 要求 | 工具 |
|---|---|---|
| 领域模型 | 所有领域逻辑覆盖 | Go test |
| 服务层 | 核心 Service 方法覆盖 | Go test + mock |
| 权限引擎 | 角色-权限矩阵覆盖 | Go test |
| 执行引擎 | 队列、Lease、重试逻辑 | Go test + mock |

### 契约测试

| 范围 | 要求 | 工具 |
|---|---|---|
| API 端点 | 请求/响应格式验证 | Go httptest |
| MCP Server | 工具调用协议测试 | 自定义测试客户端 |
| SSE 事件 | 事件格式验证 | Go httptest + SSE client |

### 集成测试

| 范围 | 要求 | 工具 |
|---|---|---|
| API + 数据库 | 数据库操作正确性 | Go test + testcontainers-postgres |
| API + 事件 | 事件写入和查询 | Go test + testcontainers |
| Agent + SSH | Agent 工具调用、结果返回 | Docker SSH 容器 |

### 故障注入测试

| 场景 | 方法 |
|---|---|
| SSH 连接超时 | 模拟 SSH 服务端延迟 |
| Provider API 不可用 | mock Provider 返回 503 |
| 数据库连接断开 | 重启 PostgreSQL |
| 服务重启 | 重启进程，检查恢复 |
| 操作超时 | 设置短超时，验证超时处理 |

### E2E 测试

- 使用 Playwright 自动化浏览器
- 覆盖 3 个验收场景
- 验证流式渲染、状态更新、实时事件

### 安全测试

| 类型 | 覆盖 |
|---|---|
| 权限绕过测试 | 每个端点测试越权访问 |
| SQL 注入测试 | 所有参数化查询 |
| XSS 测试 | 消息内容渲染 |
| Prompt Injection 测试 | Agent 输入注入检查 |
| 密钥泄露检查 | 检查日志中是否包含密钥 |

### 恢复演练

| 场景 | 验证 |
|---|---|
| 执行中服务重启 | 恢复后操作状态正确 |
| 审批中服务重启 | 审批状态不丢失 |
| SSE 断线重连 | 补读断线期间事件 |
| 操作超时 + 重试 | 重试成功或恰当标记失败 |

---

## 14. 部署方案

### 本地开发

```yaml
# docker-compose.yml (开发)
version: "3.8"
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: cicdbot_dev
      POSTGRES_USER: cicdbot
      POSTGRES_PASSWORD: cicdbot_dev
    ports:
      - "5432:5432"
    volumes:
      - pgdata:/var/lib/postgresql/data
  
  cicdbot:
    build:
      context: .
      dockerfile: Dockerfile.dev
    ports:
      - "8080:8080"
    environment:
      CICDBOT_DB_URL: postgres://cicdbot:cicdbot_dev@postgres:5432/cicdbot_dev?sslmode=disable
      CICDBOT_LOG_LEVEL: debug
      CICDBOT_VAULT_ADDR: http://dev-vault:8200
    volumes:
      - ./config:/app/config:ro
    depends_on:
      - postgres

volumes:
  pgdata:
```

### Docker Compose 试点（MVP）

```yaml
# docker-compose.yml (试点)
version: "3.8"
services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: cicdbot
      POSTGRES_USER: cicdbot
      POSTGRES_PASSWORD_FILE: /run/secrets/db_password
    volumes:
      - pgdata:/var/lib/postgresql/data
      - ./backup:/backup
    restart: always
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U cicdbot"]
      interval: 10s
  
  cicdbot:
    image: cicdbot:${VERSION:-latest}
    ports:
      - "8080:8080"
    environment:
      CICDBOT_DB_URL: postgres://cicdbot@postgres:5432/cicdbot?sslmode=disable
      CICDBOT_LOG_LEVEL: info
      PRIMARY_LLM_API_KEY_FILE: /run/secrets/primary_llm_key
      VAULT_TOKEN_FILE: /run/secrets/vault_token
    volumes:
      - ./config:/app/config:ro
    secrets:
      - primary_llm_key
      - vault_token
    depends_on:
      postgres:
        condition: service_healthy
    restart: always
    logging:
      driver: "json-file"
      options:
        max-size: "10m"
        max-file: "3"

  cicdbot-ssh-connector:
    image: cicdbot-ssh-connector:${VERSION:-latest}
    environment:
      CICDBOT_GATEWAY_URL: http://cicdbot:8080
      LOG_LEVEL: info
    depends_on:
      - cicdbot
    restart: always

  prometheus:
    image: prom/prometheus
    volumes:
      - ./deploy/prometheus.yml:/etc/prometheus/prometheus.yml
      - promdata:/prometheus
    ports:
      - "9090:9090"

volumes:
  pgdata:
  promdata:

secrets:
  primary_llm_key:
    file: ./deploy/secrets/primary_llm_key
  vault_token:
    file: ./deploy/secrets/vault_token
```

### Kubernetes 部署（P1）

- 使用 Helm Chart
- 各组件独立 Deployment
- ConfigMap 管理配置
- Secret 管理凭据
- Horizontal Pod Autoscaler
- PersistentVolume 保存数据

### 数据库迁移

- 使用 golang-migrate 或 goose
- 迁移脚本在 `internal/store/migration/`
- 应用启动时自动执行迁移
- 迁移失败阻止应用启动
- 回滚脚本随版本发布

### 备份恢复

| 数据 | 备份频率 | 保留 | 恢复方式 |
|---|---|---|---|
| PostgreSQL | 每日全量 + WAL 连续 | 30 天 | pg_restore + WAL 回放 |
| 配置文件 | Git 版本化 | 无限 | git checkout |
| SecretRef 元数据 | 随 PostgreSQL 备份 | 30 天 | 数据库恢复；真实凭据由 Vault 自身策略备份 |

---

## 15. 可观测性

### 指标（Prometheus）

| 指标 | 类型 | 标签 |
|---|---|---|
| `cicdbot_http_requests_total` | Counter | method, path, status |
| `cicdbot_http_request_duration_ms` | Histogram | method, path |
| `cicdbot_events_total` | Counter | event_type |
| `cicdbot_tasks_total` | Gauge | status |
| `cicdbot_operations_total` | Counter | connector, status |
| `cicdbot_operation_duration_ms` | Histogram | connector |
| `cicdbot_approval_duration_ms` | Histogram | risk_level |
| `cicdbot_llm_requests_total` | Counter | provider, model |
| `cicdbot_llm_tokens_total` | Counter | provider, model, type (input/output) |
| `cicdbot_llm_latency_ms` | Histogram | provider |
| `cicdbot_connector_up` | Gauge | connector_type |
| `cicdbot_leases_active` | Gauge | resource_type |
| `cicdbot_sse_connections` | Gauge | |

### 日志

- 所有组件输出结构化 JSON 日志
- 日志级别：debug / info / warn / error
- 每个日志行包含：timestamp、level、component、request_id、actor_id
- 日志收集：Loki / ELK

### Trace（OpenTelemetry）

| Span | 父 Span | 说明 |
|---|---|---|
| HTTP Request | — | API 请求全链路 |
| Agent Turn | HTTP Request | Agent 一次完整轮次 |
| Tool Call | Agent Turn | 工具调用 |
| Connector Execute | Tool Call | 连接器执行命令 |

### 审计

- 所有操作写入 `audit_logs` 表
- 审计日志不可删除、不可修改
- 审计查询 API 支持按 actor、action、时间范围检索

### 告警

| 告警规则 | 条件 | 级别 |
|---|---|---|
| 操作失败率过高 | `cicdbot_operations_total{status="failed"} > 5%` 5 分钟 | critical |
| LLM 错误率过高 | `cicdbot_llm_requests_total{status="error"} > 10%` 5 分钟 | warning |
| 审批超时 | 审批超过 30 分钟未处理 | warning |
| 数据库连接失败 | PostgreSQL 不可用 | critical |
| SSH 连接器无响应 | `cicdbot_connector_up{type="ssh"} == 0` | critical |
| SSE 断连数异常 | `cicdbot_sse_connections` 突降 | warning |

### SLO

| 指标 | SLO | 测量窗口 |
|---|---|---|
| API 可用性 | ≥ 99.9% | 30 天 |
| API 延迟 P99 | ≤ 500ms | 7 天 |
| SSE 事件延迟 P99 | ≤ 200ms | 7 天 |
| 操作成功率 | ≥ 95% | 7 天 |
| Agent 调查成功率 | ≥ 80% | 30 天 |

---

## 16. 上线门禁与风险

### 上线门禁

| # | 门禁 | 通过标准 |
|---|---|---|
| 1 | 全部测试通过 | `go test ./...` 全部通过 |
| 2 | E2E 场景通过 | 3 个验收场景全部通过 Playwright 测试 |
| 3 | 安全测试通过 | 权限绕过、注入、密钥泄露测试通过 |
| 4 | 数据迁移验证 | 迁移脚本从空库到最新版本，回滚脚本验证 |
| 5 | Docker Compose 部署 | 一键部署成功，功能可用 |
| 6 | 指标和日志验证 | Prometheus 指标正常、日志输出正常 |
| 7 | 备份恢复验证 | PostgreSQL 备份和恢复验证通过 |
| 8 | 安全审查 | 代码安全审查通过 |
| 9 | 文档就绪 | 部署文档、配置说明、操作手册 |

### 验收清单

| # | 验收项 | 状态 |
|---|---|---|
| 1 | 用户登录/登出 | □ |
| 2 | 工作区查看 | □ |
| 3 | 创建共享任务 | □ |
| 4 | Agent SSH 执行命令 | □ |
| 5 | 命令输出流式展示 | □ |
| 6 | 高风险操作审批提示 | □ |
| 7 | 审批人批准/拒绝 | □ |
| 8 | 审批后执行变更操作 | □ |
| 9 | 多人同时查看同一任务 | □ |
| 10 | SSE 事件实时推送 | □ |
| 11 | 浏览器断线重连补读 | □ |
| 12 | 审计日志记录 | □ |
| 13 | RBAC 角色权限验证 | □ |

### 回滚方案

| 回滚场景 | 回滚步骤 | 预估耗时 |
|---|---|---|
| 数据库迁移错误 | 执行回滚迁移脚本 → 恢复应用 | 15 分钟 |
| 应用 Crash | 回退到上一个稳定版本 → 重启 | 10 分钟 |
| 数据损坏 | 从备份恢复 PostgreSQL → 重新部署 | 30 分钟 |
| 配置错误 | 恢复上一个配置版本 → 重启 | 5 分钟 |

### MVP 已知风险

| 风险 | 概率 | 影响 | 缓解 |
|---|---|---|---|
| SSH 批量执行导致生产故障 | 中 | 高 | 高风险操作强制审批 + 命令白名单 |
| LLM Token 消耗超出预算 | 中 | 低 | Token 上限 + 成本告警 |
| 多人操作同一资源冲突 | 低 | 中 | Lease 机制串行操作 |
| PostgreSQL 性能瓶颈 | 低 | 中 | 合理索引 + 查询优化 + 连接池 |
| Agent 生成的计划不合理 | 中 | 中 | 审批审核计划 |
| 审批流程阻塞变更 | 中 | 低 | 审批超时机制 + 多审批人 |

### MVP 后路线图

| 阶段 | 内容 | 时间 |
|---|---|---|
| MVP 后第 1-2 月 | 双人审批、任务转交/关注、WebSocket、K8s 连接器 | |
| MVP 后第 3-4 月 | Runbook 模板、定时任务、多 Provider 路由、故障切换 | |
| MVP 后第 5-6 月 | CI/CD 对接、监控日志连接器、外部通知、观察者角色 | |
| MVP 后第 7-8 月 | 多工作区、多组织、高可用部署 Helm Chart | |
| 长期 | 自托管 LLM、SSO/LDAP 集成、RBAC 精细化管理 | |

---

## 17. 团队配置建议

### MVP 阶段（第 1-10 周）

| 角色 | 人数 | 职责 | 阶段 |
|---|---|---|---|
| 架构师 / Tech Lead | 1 | 总体设计、技术决策、代码审查 | 全期 |
| 后端开发（Go） | 2-3 | 领域模型、API、执行引擎、SSH 连接器 | 全期 |
| 前端开发 | 1 | SPA 工作台、SSE 客户端、状态管理 | 第 4 周起 |
| DevOps / 测试 | 1 | 部署配置、测试、CI/CD | 第 5 周起 |
| 产品经理 / 需求 | 1 | 需求确认、验收、试点团队对接 | 第 1-2 周 + 第 9-10 周 |

### 阶段性职责分配

| 阶段 | 架构师 | 后端 Dev1 | 后端 Dev2 | 后端 Dev3 | 前端 Dev | DevOps/测试 | PM |
|---|---|---|---|---|---|---|---|
| 第 1-2 周 | 架构设计 | 项目骨架 + DB | 事件存储 | 认证 | — | — | 需求细化 |
| 第 3-4 周 | 代码审查 | 工作区+任务 | 资源+消息 | RBAC+审计 | 初始化项目 | — | — |
| 第 5-6 周 | 架构审查 | Controller集成 | SSH 连接器 | Tool Gateway | — | 测试框架 | — |
| 第 7-8 周 | 性能审查 | 执行引擎 | 审批服务 | SSE/事件 | 工作台页面 | 集成测试 | — |
| 第 9 周 | Bug 修复 | Bug 修复 | 安全加固 | 性能优化 | Bug 修复 | 全部测试 | 验收准备 |
| 第 10 周 | 上线审查 | 部署+文档 | 部署+监控 | 运维支持 | 最终调整 | 安全审计 | 试点验收 |

### 推荐总人力

- **MVP 核心团队：** 5-7 人（含 PM）
- **试点阶段：** 以上团队 + 1-2 名试点运维团队代表
- **MVP 后维护：** 3-4 人（2 后端 + 1 前端 + 1 DevOps）
