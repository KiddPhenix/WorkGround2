# CLI Desktop Remote Control

Control a running WorkGround2 Desktop instance from the command line:
create sessions, submit prompts, switch workspaces, handle approvals, and
read responses — all through a local HTTP API.

Status: **implemented** — new, submit, submit-to-session, open, workspaces,
focus, approval handling, reply polling.

## Quick Reference

```
workground2 desktop workspaces                        # 列出 Desktop 所有工作区
workground2 desktop new [prompt]                      # 新建会话，可选发送首条消息
workground2 desktop submit <prompt>                   # 向当前活跃会话发送消息
workground2 desktop status [--json]                   # 查看当前活跃会话运行状态
workground2 desktop open <path>                       # 打开指定会话文件
workground2 desktop focus                             # 聚焦 Desktop 窗口

Options:
  --workspace <dir>     指定目标工作区目录
  --session <path>      指定目标会话文件（submit 专用）
  --session-name NAME   复用或创建命名会话（new 专用）
  --no-wait             发送后不等待回复（fire-and-forget）
  --yolo                使用 yolo 工具审批模式
  --tool-approval MODE  ask、auto 或 yolo
```

## How It Works

Desktop 启动时绑定 `127.0.0.1` 随机端口，端口号写入
`~/.WorkGround2/desktop-port`。CLI 读取该文件发现端口，通过 HTTP 发送命令。

```
┌─────────────────┐         HTTP (127.0.0.1:PORT)         ┌─────────────────┐
│  workground2    │ ────────────────────────────────────── │  Desktop App    │
│  desktop new    │  POST /api/v1/session/new              │  (Wails)        │
│  desktop submit │  POST /api/v1/session/submit           │                 │
│  desktop status │  GET  /api/v1/session/status           │                 │
│  desktop focus  │  POST /api/v1/window/focus             │                 │
│                 │                                        │                 │
│  ← 轮询 .jsonl  │ ← 审批检测 →  ← GET /api/v1/session/   │                 │
│     ← 回复打印  │              ← POST /api/v1/session/   │                 │
└─────────────────┘                                        └─────────────────┘
```

## Commands

### `workspaces` — list Desktop workspace tabs

```sh
workground2 desktop workspaces
```

```
  *  WorkGround2                   D:\Work\WorkGround2
     my-project                    D:\Projects\my-project
```

`*` 标记当前活跃工作区。输出列：当前标记、名称、目录路径。

对应 API：`GET /api/v1/workspaces`。

### `new` — create a new session

```sh
# 只创建空白会话
workground2 desktop new

# 创建会话并发送首条消息，等待回复
workground2 desktop new "介绍一下这个项目的架构"

# 复用或创建一个命名会话；同名会话存在时会打开最新的同名会话
workground2 desktop new --session-name codex-worker "继续执行这条任务"

# 在指定工作区创建
workground2 desktop new --workspace D:\myproject "帮我修复 README"

# 只发送不等待
workground2 desktop new --no-wait "hi"

# 委托实现类任务：不等待，并允许范围内工具调用直接执行
workground2 desktop new --workspace D:\myproject --session-name codex-worker --yolo --no-wait "帮我修复 README"
```

流程：
1. CLI → Desktop 创建新会话；如果传了 `--session-name`，则先按会话标题复用最新同名会话，找不到才创建并命名
2. Desktop 返回 `path`、`running`、`pendingPrompt`、`pendingInteraction`、`mode`、`toolApprovalMode`
3. 如果有 prompt，CLI 发送 submit 请求
4. 默认等待回复：轮询 `.jsonl` 文件，新消息实时打印
5. 遇到审批自动暂停，提示用户 `y/n/c`

对应 API：
- `POST /api/v1/session/new`
- `POST /api/v1/workspace/switch`（如果有 `--workspace`）

### `submit` — submit a prompt to the active session

```sh
# 向当前活跃标签页发送
workground2 desktop submit "解释这段代码"

# 指定目标会话（Desktop 会自动打开该会话）
workground2 desktop submit --session 20260708-xxxx.jsonl "继续上次的讨论"

# 指定工作区 + 不等待
workground2 desktop submit --workspace D:\myproject --no-wait "改一下标题"

# 指定工作区 + yolo + 不等待
workground2 desktop submit --workspace D:\myproject --yolo --no-wait "改一下标题"
```

`--session` 指定后 Desktop 会先调用 `ResumeSession` 打开目标会话，再发送消息。

对应 API：`POST /api/v1/session/submit`（可选 `session` 字段）。

### `status` — inspect the active session

```sh
workground2 desktop status
workground2 desktop status --json
```

`--no-wait` 的 `new` / `submit` 返回 0 表示 Desktop 已接收任务，不表示任务完成。
调用方应定时查询 `status --json`。优先使用 `foregroundActive` 判断当前 turn，
`backgroundOnly=true` 不阻塞 worker 完成；`running` 保留为兼容字段，可能包含后台工作。
当 `pendingPrompt=true` 时应立即读取
`pendingInteraction`，选择并提交答案后继续轮询，不能继续空等。最终直到
`foregroundActive=false` 且 `pendingPrompt=false`。完成状态会附带最多 2000 字符的
`report`，调用方应先看报告和 `git diff --stat`，再只检查 scope 内 diff。

对应 API：`GET /api/v1/session/status`。

### `answer` — answer a structured ask

`status --json` 返回 `pendingInteraction.kind=ask` 时，按 interaction ID、question ID
和返回的 option label 回答：

```powershell
workground2 desktop answer --id 7 --answer 'q1=Use existing path'

# multiSelect 问题重复同一个 question ID
workground2 desktop answer --id 7 --answer 'q1=Option A' --answer 'q1=Option B'
```

服务端会校验 interaction ID、question ID 和选项标签；状态已变化或答案拼错时返回非零。

### `approve` — resolve a protected approval

`status --json` 返回 `pendingInteraction.kind=approval` 时，检查 `tool`、`subject`、
`reason` 后按 ID 允许或拒绝：

```powershell
workground2 desktop approve --id 8 --allow
workground2 desktop approve --id 8 --deny
```

### `open` — open a session file

```sh
workground2 desktop open 20260708-083655.473844000-deepseek-deepseek-v4-pro.jsonl
```

不发送消息，仅在 Desktop 中打开该会话。

对应 API：`POST /api/v1/session/open`。

### `focus` — bring Desktop window to front

```sh
workground2 desktop focus
```

对应 API：`POST /api/v1/window/focus`。

## Approval Handling

当 agent 执行过程中遇到需要审批的工具调用（如 `write_file`、`bash`）或
`ask` 结构化询问时，Desktop 暂停等待选择。`status --json` 会提供：

- 审批：`pendingInteraction={kind,id,tool,subject,reason}`
- 询问：`pendingInteraction={kind,id,questions:[{id,header,question,options,multiSelect}]}`

同步 CLI 轮询检测到普通审批时会：

1. 显示 `⏸ Desktop 等待审批…`
2. 列出待审批的工具调用（如 `🔧 write_file("README.md")`）
3. 等待用户输入：

```
批准? [y=允许 / n=拒绝 / c=取消]
```

- **y / yes / allow / 批准** → 批准此工具调用，agent 继续
- **n / 拒绝** → 拒绝此调用
- **c / cancel** → 取消

审批通过后 CLI 继续轮询，直到 agent 完成回复或再次需要审批。结构化询问通过
`desktop answer` 回答；异步 Codex 调用应先读取选项并自行判断，只有任务包无法确定
安全选择时才交给用户。

`--yolo` 等价于 `--tool-approval yolo`，会在目标 Desktop 会话里启用工具自动审批。
它适合 Codex 委托的有明确 scope 的实现任务；普通探索或不确定任务仍建议用默认审批。

对应 API：
- `GET /api/v1/session/status` — 检测 `pendingPrompt`
- `POST /api/v1/session/approve` `{"id":"8","allow":true}` — 审批
- `POST /api/v1/session/answer` `{"id":"7","answers":[{"questionId":"q1","selected":["Use existing path"]}]}` — 回答询问

## Reply Polling

`new` 和 `submit` 默认等待 agent 回复（除非 `--no-wait`）：

| 行为 | 说明 |
|---|---|
| 轮询间隔 | 500ms |
| 新消息到达 | 即时打印 `[USER]` / `[ASSISTANT]` / `[TOOL]` |
| 工具调用 | 显示为 `[TOOL] write_file(…)` |
| 安静判定 | 连续 3 秒无新消息视为完成 |
| 等待选择 | 自动检测 `PendingPrompt`；状态提供完整 `pendingInteraction` |
| 超时 | 5 分钟无活动强制退出 |

## API Reference

所有端点在 `127.0.0.1:<port>` 上。端口从 `~/.WorkGround2/desktop-port` 读取。

| Method | Endpoint | Request Body | Response |
|---|---|---|---|
| `POST` | `/api/v1/session/new` | `{"toolApprovalMode":"yolo"}` 可选 | `{"status":"ok","path":"…","running":bool,"pendingPrompt":bool,"mode":"…","toolApprovalMode":"…"}` |
| `POST` | `/api/v1/session/open` | `{"path":"…"}` | 同 session 状态响应 |
| `POST` | `/api/v1/session/submit` | `{"prompt":"…"[, "session":"…", "toolApprovalMode":"yolo"]}` | 同 session 状态响应 |
| `GET` | `/api/v1/session/status` | — | 同 session 状态响应 |
| `POST` | `/api/v1/session/approve` | `{"id":"…","allow":bool}` | `{"status":"ok"}` |
| `POST` | `/api/v1/session/answer` | `{"id":"…","answers":[{"questionId":"q1","selected":["label"]}]}` | `{"status":"ok"}` |
| `GET` | `/api/v1/workspaces` | — | `[{"path":"…","name":"…","current":bool}]` |
| `POST` | `/api/v1/workspace/switch` | `{"dir":"…"}` | `{"status":"ok"}` |
| `POST` | `/api/v1/window/focus` | — | `{"status":"ok"}` |
| `GET` | `/api/v1/status` | — | `{"status":"running","port":…}` |

## Implementation

| 文件 | 角色 |
|---|---|
| `desktop/remote_api.go` | Desktop HTTP API：路由 + 处理器 |
| `desktop/app.go` | Remote API 内部的 pending interaction 查询、校验和解决入口 |
| `internal/control/controller.go` | 暴露当前 `PendingInteraction` 并按 ID 解决审批/询问 |
| `internal/control/port.go` | `PendingInteraction` 接口 |
| `internal/cli/desktop.go` | CLI 命令：端口发现、轮询、`answer` / `approve` |
