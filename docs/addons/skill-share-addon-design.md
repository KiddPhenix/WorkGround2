# Skill 共享 AddOn 设计

本文档设计 WorkGround2 的外部 AddOn 示例：内部 skill 共享插件。它面向后续实现，范围只覆盖通过一个受管理 Git 来源同步共享 skills，并让同步后的技能通过现有插件包与 skill 发现链路生效。绘图、飞书、Jira 等后续插件只作为扩展方向，不进入本版实现。

## 现有基础

当前仓库已经具备这些可复用能力：

- `internal/pluginpkg` 负责解析和管理已安装插件包。插件包状态存放在 `<WorkGround2 home>/plugin-packages.json`，插件内容默认落在 `<WorkGround2 home>/plugins/<name>/`，manifest 支持 `WorkGround2-plugin.json` 与 `.codex-plugin/plugin.json`。
- `internal/config/plugin_packages.go` 会在配置加载时把启用的插件包合并进内存配置：`skills` 映射为 `[skills].paths`，`mcpServers` 映射为运行时 MCP 配置，过程不会回写用户 `config.toml`。
- `internal/installsource` 提供两阶段安装：`apply=false` 只规划并返回 `planId`、风险与动作；`apply=true` 执行写入并返回 `done`、`partial`、`failed`、`blocked` 等显式状态。
- `internal/skill` 的 `Store` 会扫描项目、custom、全局和内置 skill roots；同名优先级为 project > custom > global > builtin。目录 skill 使用 `<name>/SKILL.md`。
- `desktop/plugin_packages_app.go` 暴露插件包管理 API：`Plugins`、`PlanPluginInstall`、`InstallPlugin`、`RemovePlugin`、`SetPluginEnabled`、`UpdatePlugin`、`PluginDoctor`。安装、更新、移除、启停后会 invalidate skill roots cache 并 rebuild。
- 桌面端已有 skills 刷新能力：`RefreshSkills` 会重建 controller，让 skill index、slash menu 和 skill tools 重新读取磁盘。

## 目标

1. 提供一个外部 AddOn 包 `skill-share`，引导用户配置内部 Git 地址与登录凭据。
2. 用户登录或桌面启动后，自动检查并更新共享 skill 仓库。
3. 同步后的 skills 通过插件包 manifest 进入现有 skill discovery，不额外改写用户 `[skills].paths`。
4. 提供独立管理页面，支持查看状态、更新、删除、强制刷新、重新配置凭据。
5. 文件更新后通过重启或刷新立即生效，刷新路径复用现有 controller rebuild。
6. 全链路幂等、可重试、可恢复，失败状态清楚暴露给 UI 和日志。
7. 密码、token、personal access token 等敏感值只进入 secret 存储，配置和文档示例只保存引用。

## 非目标

- 不设计通用 AddOn 市场、评分、签名分发和在线目录。
- 不实现绘图、飞书、Jira 插件的业务能力。
- 不要求 Git 服务必须是 GitHub；MVP 只保证 HTTPS Git 源。
- 不在 manifest、`plugin-packages.json`、配置示例或日志中保存密码明文。
- 不要求运行中热替换已经注入当前 turn 的 skill body；刷新或新 session 生效即可。

## 用户流程

### 首次启用

1. 用户安装 `skill-share` AddOn 包后，打开 **Settings -> Plugins** 中对应的 AddOn package 块。
2. 页面发现 `skill-share` 没有 source/profile，显示初始化向导。
3. 用户填写：
   - Git 仓库 URL，例如 `https://git.example.com/team/workground2-skills.git`。
   - 分支，默认 `main`。
   - 仓库内插件目录，默认仓库根目录。
   - 用户名。
   - 密码或 token 输入框。
4. 用户点击 **连接并预检**。
5. 后端保存或更新 secret，拉取仓库到临时 staging 目录，解析 manifest，列出将导入的 skills 数量、插件名称、版本、commit。
6. 用户点击 **启用同步**。
7. 后端把 staging 原子切换为 active checkout，写入 source/profile 状态，注册/更新插件包状态，然后 rebuild controller。
8. 页面展示 `Ready`，列出最近 commit、同步时间、skill 数量、下次检查时间。

### 已登录自动更新

1. 桌面启动或用户登录成功后，后台读取启用的 `skill-share` sources/profiles。
2. 如果已有同步任务在跑，新的触发只记录 `pendingRefresh=true` 并返回当前任务 ID。
3. 后台用 secret 引用获取凭据，执行 `git fetch` 或浅克隆检查远端 commit。
4. 远端 commit 未变化时更新 `lastCheckedAt`，状态保持 `Ready`。
5. 远端 commit 变化时拉取到 staging，校验 manifest 和 skill 可读性。
6. 校验通过后切换 active checkout，更新 `currentRevision`，调用插件包 upsert，触发 rebuild 或标记 `needsRefresh`。
7. 校验失败时保留旧 active checkout，状态进入 `UpdateFailed`，页面展示错误和重试入口。

### 管理页面操作

- **更新**：按正常节流策略检查远端并同步。
- **强制刷新**：跳过本地 revision 判断，重新拉取、校验、切换，并强制 rebuild。
- **删除**：禁用 source/profile，移除对应插件包状态，删除受管理 checkout；secret 默认保留并提示用户可单独清除。
- **清除凭据**：删除 secret 引用或让 secret backend 作废，source/profile 进入 `NeedsAuth`。
- **重新配置**：修改 Git URL、分支、路径、用户名或插件名后产生新的 source generation；下一次同步从空 staging 开始。

## AddOn 定位与目录

MVP 中 `skill-share` 作为外部 AddOn 包声明，管理一个或多个共享 skill source/profile。同步结果表现为普通插件包，继续使用 `pluginpkg` 参与配置合并。主项目只按已安装 AddOn package 渲染通用管理块，不再默认显示固定 Skill Share 设置块。

术语约定：UI 和用户文案统一称为 **sources / 来源**；后端 API、JSON 字段和目录为了兼容继续使用 `profile` 命名。一个 Skill Share AddOn 可以配置多个 source/profile。每个 source/profile 独立保存 Git URL、branch、path、credential/secretRef、pluginName 和 state；同步、失败、删除、凭据更新和插件包 upsert 都按单个 source/profile 收敛，避免一个来源的迟到任务或删除操作影响另一个来源。

建议落盘结构：

```text
<WorkGround2 home>/
  addons/
    skill-share/
      profiles.json
      profiles/
        <profile-id>/
          active/              # 当前生效 checkout
          staging-<task-id>/    # 当前同步临时目录
          previous/             # 可选，保留上一个成功版本用于回滚
          sync-log.jsonl        # 短状态日志，不写 secret
  plugin-packages.json
  plugins/
    skill-share-<profile-id>/   # 可选：如果复用 pluginpkg copy 安装
```

两种落地方式：

- MVP 推荐：active checkout 直接作为插件包 root，`pluginpkg.InstalledPlugin.Root` 指向 active 目录。这样避免二次复制，更新时只切换目录。
- 复制安装路径：调用 `installsource` 的 `kind=plugin`、`replace=true` 复制到 `<WorkGround2 home>/plugins/<name>/`。实现更贴近现有 CLI，磁盘写入更多。

MVP 优先 active checkout 方案。它依然写入 `plugin-packages.json`，让现有 config merge、hooks、MCP 与桌面插件列表全部复用。

## Manifest 设计

共享 skill 仓库必须提供 `WorkGround2-plugin.json`，最小形式：

```json
{
  "name": "team-skills",
  "version": "2026.07.04",
  "description": "Team shared WorkGround2 skills",
  "repository": "https://git.example.com/team/workground2-skills",
  "skills": "skills"
}
```

约束：

- `name` 需要满足现有 `pluginpkg.IsValidName`。
- `skills` 必须是相对路径，并留在插件根目录内。
- secret、用户名、密码、token、cookie 不能写入 manifest。
- `hooks` 和 `mcpServers` 在 MVP 中允许解析但默认标记高风险；管理页面需要展示能力数量和风险说明。
- 如果仓库提供 `.codex-plugin/plugin.json`，可沿用现有兼容逻辑。内部共享仓库推荐使用原生 manifest，字段语义更清楚。

后续可扩展 `WorkGround2-plugin.json`：

```json
{
  "name": "team-skills",
  "version": "2026.07.04",
  "description": "Team shared WorkGround2 skills",
  "repository": "https://git.example.com/team/workground2-skills",
  "skills": [
    "skills",
    "playbooks"
  ],
  "workground2": {
    "addonKind": "skill-share",
    "minAppVersion": "1.8.1"
  }
}
```

`workground2` 扩展字段只用于声明元数据。MVP 可以忽略未知字段，避免 manifest 解析提前泛化。

## Profile 配置设计

`profiles.json` 保存 AddOn 自身状态，建议结构：

```json
{
  "version": 1,
  "profiles": [
    {
      "id": "team-skills",
      "enabled": true,
      "displayName": "Team Skills",
      "git": {
        "url": "https://git.example.com/team/workground2-skills.git",
        "branch": "main",
        "path": ".",
        "auth": {
          "type": "basic",
          "username": "alice",
          "secretRef": "secret://workground2/addons/skill-share/team-skills/password"
        }
      },
      "pluginName": "team-skills",
      "update": {
        "auto": true,
        "checkOnLogin": true,
        "intervalSeconds": 3600
      },
      "state": {
        "status": "ready",
        "currentRevision": "abc1234",
        "lastCheckedAt": "2026-07-04T10:00:00Z",
        "lastUpdatedAt": "2026-07-04T10:00:02Z",
        "lastError": ""
      }
    }
  ]
}
```

配置规则：

- `id` 是本地 source/profile ID，稳定且可作为锁名、目录名、request ID 的一部分。
- `pluginName` 来自 manifest，也可由用户覆盖；与已有插件同名时需要明确确认 replace。
- `secretRef` 是唯一可持久化的凭据引用。
- `state` 可由同步任务更新；用户意图字段和运行状态字段分区，避免失败恢复时误改配置。
- 写入使用临时文件 + rename，保留旧文件作为恢复来源。

## 凭据与 secret 处理

凭据处理原则：

- 密码、token、PAT 只写入 secret backend。UI 输入框提交后立即交给 secret 写入接口，后端返回 `secretRef`。
- `profiles.json`、`plugin-packages.json`、manifest、同步日志、错误详情只保存 `secretRef`、用户名、认证类型和脱敏 host。
- Git 命令调用时通过临时 credential helper、环境变量或 stdin 注入凭据，执行后清理临时文件和环境。
- 错误清洗需要覆盖 URL、命令行、stderr、Git remote 输出，避免带凭据的远端地址泄漏。
- secret 读取失败时 profile 进入 `NeedsAuth`，页面提示重新登录或解锁 secret store。

认证类型：

- `basic`：用户名 + 密码或 token。MVP 支持。
- `token`：用户名可为空，token 作为 secret。MVP 可映射到 basic helper。
- `ssh`：后续支持，只保存 key 引用和 known_hosts 策略。

示例中禁止出现真实密码。文档和 UI 示例统一使用 `secretRef`。

## 更新策略

### 触发源

- 登录后自动触发：`checkOnLogin=true`。
- 定时检查：桌面后台按 `intervalSeconds` 触发。
- 手动更新：管理页面点击 **更新**。
- 强制刷新：管理页面点击 **强制刷新**，忽略本地 revision 和缓存。
- 应用启动恢复：发现上次任务停在 `Syncing` 或残留 staging 时执行恢复流程。

### 同步阶段

1. `AcquireLock`：按 profile ID 获取本地锁。同一 profile 同时只允许一个写任务。
2. `ReadConfig`：读取 profile 与 secret 元数据，校验必要字段。
3. `ResolveSecret`：按目的读取 secret，失败进入 `NeedsAuth`。
4. `FetchRemote`：查询远端 revision。网络失败进入 `UpdateFailed`，保留旧版本。
5. `PrepareStaging`：克隆或 fetch 到 `staging-<task-id>`。
6. `ValidateManifest`：调用 `pluginpkg.ParseDir`，校验 skill roots、manifest name、路径边界。
7. `VerifySkills`：用 `skill.Store` 在 staging 根上验证 skill 可发现，至少列出数量和名称。
8. `SwitchActive`：把当前 active 移到 previous，把 staging rename 为 active。
9. `UpsertPlugin`：写入 `pluginpkg.Upsert`，`Root` 指向 active，`Source` 记录 Git URL 或 profile source。
10. `RefreshRuntime`：桌面端调用 `invalidateSkillRootsCache` 和 `rebuild`，CLI 或后台只标记 `needsRestart`。
11. `Cleanup`：删除旧 staging，按策略保留 previous。

### 原子性与恢复

- staging 校验通过前不触碰 active。
- active 切换失败时保留 staging 并记录 `RecoverableSwitchFailed`。
- `pluginpkg.Upsert` 失败时 active 已切换但插件状态未更新，恢复任务可重新执行 Upsert。
- rebuild 失败时磁盘状态已更新，页面展示 `NeedsRefresh`，用户可再次点击刷新。
- 删除操作先禁用 profile 和插件状态，再删除磁盘目录。目录删除失败时状态进入 `RemovePending`，下次启动继续清理。

## 状态模型

Profile 状态建议使用显式枚举：

| 状态 | 含义 | 用户可操作 |
| --- | --- | --- |
| `unconfigured` | 没有 Git 来源或凭据 | 初始化 |
| `needs_auth` | secret 缺失、过期或不可读取 | 重新登录、清除凭据 |
| `checking` | 正在检查远端 revision | 查看任务 |
| `syncing` | 正在拉取和校验 staging | 查看任务、取消后重试 |
| `ready` | active checkout 可用，插件已启用 | 更新、强制刷新、禁用、删除 |
| `update_available` | 检测到远端变化，等待应用 | 更新 |
| `update_failed` | 拉取或校验失败，旧版本继续可用 | 重试、强制刷新、查看错误 |
| `needs_refresh` | 磁盘已更新，controller rebuild 失败或尚未执行 | 刷新、重启 |
| `disabled` | profile 保留但不参与同步和 discovery | 启用、删除 |
| `remove_pending` | 状态已移除，磁盘清理未完成 | 继续清理、查看错误 |

任务状态单独建模：

```json
{
  "taskId": "team-skills-20260704T100002Z",
  "profileId": "team-skills",
  "trigger": "login",
  "phase": "validate_manifest",
  "status": "running",
  "startedAt": "2026-07-04T10:00:02Z",
  "finishedAt": "",
  "currentRevision": "abc1234",
  "targetRevision": "def5678",
  "error": "",
  "retryable": true
}
```

任务状态用于 UI 展示和恢复，profile 状态用于长期事实。两者分离后，重复触发、进程中断、迟到回调都容易处理。

## 管理页面能力

页面入口：**Settings -> AddOns -> Skill Share**。后续 AddOn 增多后可变成 AddOns 总览页，此插件先提供独立页面。

页面区域：

- **连接状态**：source 名称、启用状态、Git host、分支、路径、当前 commit、最近检查时间、最近更新时间、错误摘要。
- **凭据**：用户名、secret 状态、重新登录、清除凭据。密码输入框只用于提交，不回显。
- **同步控制**：更新、强制刷新、取消当前任务、刷新运行时、打开日志。
- **能力预览**：manifest kind、插件名、版本、skills 数量、hooks 数量、MCP servers 数量、解析警告。
- **风险提示**：hooks 或 MCP 存在时展示额外确认；skill-only 仓库走低风险路径。
- **恢复提示**：发现 staging、previous、remove_pending、needs_refresh 时展示继续恢复按钮。

管理 API 草案：

```go
type SkillShareProfileView struct {
    ID              string   `json:"id"`
    DisplayName     string   `json:"displayName"`
    Enabled         bool     `json:"enabled"`
    GitURL          string   `json:"gitUrl"`
    Branch          string   `json:"branch"`
    Path            string   `json:"path"`
    Username        string   `json:"username,omitempty"`
    AuthStatus      string   `json:"authStatus"`
    PluginName      string   `json:"pluginName,omitempty"`
    ManifestKind    string   `json:"manifestKind,omitempty"`
    Version         string   `json:"version,omitempty"`
    CurrentRevision string   `json:"currentRevision,omitempty"`
    LastCheckedAt   string   `json:"lastCheckedAt,omitempty"`
    LastUpdatedAt   string   `json:"lastUpdatedAt,omitempty"`
    Status          string   `json:"status"`
    Error           string   `json:"error,omitempty"`
    Warnings        []string `json:"warnings,omitempty"`
    Skills          int      `json:"skills"`
    Hooks           int      `json:"hooks"`
    MCPServers      int      `json:"mcpServers"`
    NeedsRefresh    bool     `json:"needsRefresh"`
}
```

```go
PlanSkillShare(profileInput) (SkillSharePlan, error)
SaveSkillShareProfile(profileInput, secretInput) (SkillShareProfileView, error)
SyncSkillShare(profileID string, force bool) (SkillShareTaskView, error)
DeleteSkillShare(profileID string, removeSecret bool) (SkillShareProfileView, error)
RefreshSkillShare(profileID string) error
SkillShareProfiles() []SkillShareProfileView
SkillShareTask(profileID string) SkillShareTaskView
```

这些 API 可以先放在 desktop app 层，内部调用 service。后续 CLI 可复用同一 service。

## 核心 API 依赖

MVP 应复用现有 API：

- `pluginpkg.ParseDir(root)`：校验共享仓库 manifest。
- `pluginpkg.Upsert(home, InstalledPlugin)`：把 active checkout 注册为插件包。
- `pluginpkg.Remove(home, name)`：删除时移除插件包状态。
- `pluginpkg.SetEnabled(home, name, enabled)`：启用和禁用。
- `pluginpkg.LoadState(home)` 与 `pluginpkg.LoadInstalled(home)`：管理页读取状态和诊断。
- `installsource.NewTool`：可用于预检、兼容安装路径或未来 CLI 对齐。
- `skill.New(...).List()` 与 `Read(name)`：验证 staging 的 skills 可发现。
- `App.RefreshSkills()` 或 `a.rebuild()`：文件更新后刷新运行时。
- `config.WorkGround2HomeDir()`：定位全局状态目录。

建议新增 service 边界：

```go
type SkillShareService interface {
    Profiles() ([]SkillShareProfileView, error)
    Plan(ctx context.Context, in ProfileInput) (SkillSharePlan, error)
    Save(ctx context.Context, in ProfileInput, secret SecretInput) (SkillShareProfileView, error)
    Sync(ctx context.Context, profileID string, force bool) (SkillShareTaskView, error)
    Delete(ctx context.Context, profileID string, removeSecret bool) (SkillShareProfileView, error)
    Recover(ctx context.Context) ([]SkillShareTaskView, error)
}
```

service 内部负责文件锁、secret、Git、staging、pluginpkg、恢复。desktop 只负责调用和展示。

## 失败、重试与恢复

### 幂等规则

- `SaveProfile` 对相同 `id` 做 upsert，重复提交只更新 generation 和状态。
- `Sync(profileID, force=false)` 遇到相同 remote revision 返回 `ready`，不重复切换 active。
- `Sync(profileID, force=true)` 可重复执行；每次使用新的 staging task ID。
- `Delete(profileID)` 可重复执行；profile 不存在、插件状态已移除、目录已删除都视为删除完成。
- `RefreshRuntime` 可重复执行；失败只影响运行时加载，不回滚磁盘版本。

### 错误显式暴露

每个失败需要至少包含：

- `phase`：失败阶段。
- `retryable`：是否建议直接重试。
- `safeMessage`：脱敏后展示给用户。
- `detailRef`：可选，指向本地短日志或 capture，仍需脱敏。
- `nextAction`：例如 `retry`、`reauth`、`fix_manifest`、`restart`。

### 典型失败处理

- 网络失败：状态 `update_failed`，`retryable=true`，保留旧 active。
- 凭据失败：状态 `needs_auth`，`retryable=false`，要求重新登录。
- manifest 缺失：状态 `update_failed`，`nextAction=fix_manifest`，保留旧 active。
- skill 校验失败：保留 staging 供诊断，旧 active 继续生效。
- active 切换中断：启动时根据 marker 判断，优先恢复到已有 active；若 staging 已完整且 marker 指向 pending switch，可继续切换。
- plugin state 写入失败：active 已更新时可重复执行 `UpsertPlugin`。
- rebuild 失败：状态 `needs_refresh`，用户可点刷新或重启。
- 删除目录失败：状态 `remove_pending`，后台下次继续清理。

### 任务 marker

同步任务开始时写入：

```text
profiles/<id>/sync-<task-id>.json
```

任务完成后写入 `finishedAt` 和最终状态。启动恢复扫描 running marker：

1. staging 不存在：标记 failed，可重试。
2. staging 存在且 active 未切换：删除 staging 或继续校验。
3. active 已切换且 plugin state 未更新：重放 Upsert。
4. remove_pending：继续删除受管理目录。

## 最小 MVP 切片

### 切片 1：状态与 service

- 定义 `profiles.json`、profile view、task view。
- 实现文件锁、原子写入、脱敏错误。
- 实现 secretRef 字段，但 secret backend 可先接入现有凭据保存机制或本地 mock。

验收：

- 重复保存同一 profile 不产生重复项。
- 缺少 secret 时返回 `needs_auth`。
- profiles 文件损坏时能报告错误，保留原文件供手动修复。

### 切片 2：Git 预检与 staging 校验

- 支持 HTTPS Git clone/fetch。
- 支持分支和仓库内路径。
- 调用 `pluginpkg.ParseDir` 与 `skill.Store` 校验。

验收：

- manifest 缺失、路径越界、skill 无法发现都有明确错误。
- 凭据不会出现在错误文本。
- 网络失败后旧 active 不受影响。

### 切片 3：注册插件包并刷新

- active checkout 注册到 `plugin-packages.json`。
- 同步成功后调用 rebuild。
- 管理页面能看到插件和 skills 数量。

验收：

- 重启后共享 skills 仍可发现。
- 点击刷新后新文件可出现在 slash menu。
- rebuild 失败时状态进入 `needs_refresh`，再次刷新可恢复。

### 切片 4：管理页面

- 初始化向导。
- source 列表和详情。
- 更新、强制刷新、删除、重新登录。
- 状态、错误、警告展示。

验收：

- 删除可重复点击，最终状态一致。
- 强制刷新可重复执行。
- hooks/MCP 能力出现时 UI 展示风险提示。

### 切片 5：启动恢复与自动更新

- 登录后触发后台检查。
- 启动时扫描 running marker 和 remove_pending。
- 同步任务去重。

验收：

- 上次进程中断后能恢复或显式失败。
- 连续点击更新只产生一个活跃写任务。
- 远端无变化时只更新 `lastCheckedAt`。

## 后续扩展

- 多 source/profile：支持不同团队、不同分支、不同权限来源；这是 Skill Share 的一等配置模型。
- SSH 认证：secretRef 指向私钥或 agent 配置，增加 known_hosts 策略。
- 签名和校验：manifest 声明签名、commit allowlist 或发布 tag 策略。
- AddOn 总线：把 `skill-share` service 抽象为通用 AddOn lifecycle，后续绘图、飞书、Jira 插件复用状态、secret、任务和管理页模式。
- 回滚 UI：用户选择 previous revision 回退，仍走 manifest 校验和 Upsert。
- 差异预览：展示新增、删除、修改的 skill 名称和描述。
- CLI 支持：`WorkGround2 addon skill-share sync`、`status`、`login`、`delete`。
- 团队策略：管理员提供默认 Git URL，用户只填凭据。

## 实现注意事项

- AddOn service 需要作为业务入口收敛 Git、secret、profile state、pluginpkg、刷新运行时，避免 UI 直接改多个状态源。
- 共享仓库内容进入 staging 后再校验，校验通过前不能影响 active。
- 运行时刷新失败只改变可观测状态，不能删除已同步文件。
- `plugin-packages.json` 是插件能力的 source of truth；`profiles.json` 是同步配置和任务状态的 source of truth。
- 日志、错误、计划输出默认脱敏，尤其是 Git URL、remote stderr、credential helper 路径。
- 后台任务必须有锁和 task ID，UI 回包只按 task ID 更新，避免迟到回调覆盖新状态。
