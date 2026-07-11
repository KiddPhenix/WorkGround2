# AddOn Runtime Architecture

## 目标

WorkGround2 的 AddOn 体系把可安装功能包接到同一个核心运行时上。核心负责模型调用、工具注册、skill 发现、配置存储、状态持久化、权限和桌面管理；AddOn 通过 manifest 声明能力，并在启用后把能力合并进运行时。

典型 AddOn：

- Skill 共享插件：从团队 Git 仓库安装和更新 skill，并在管理页显示版本、更新、刷新、删除状态。
- 绘图插件：保存自己的 API key 或 CLI 配置，把图片生成能力暴露成 tool 或 skill。
- 飞书连接器：引导用户授权，把飞书消息、文档、审批等能力暴露给 core。
- Jira 连接器：引导连接公司 Jira，扫描待办，形成独立视图，并能从条目创建会话或打开原页面。

## 现有基础

当前仓库已经有运行时插件包基础：

- `internal/pluginpkg`：解析插件包 manifest，维护安装状态，发现 skill/hook/MCP 能力。
- `internal/installsource`：从本地目录、zip 包、链接或 GitHub 来源安装插件包。
- `internal/config/plugin_packages.go`：把已启用插件包的 skill roots、hooks、MCP server 合并进运行配置，并给包内运行时注入 `WORKGROUND2_HOME`、包根目录和 AddOn storage/config/data/state 目录。
- `desktop/plugin_packages_app.go`：提供桌面端插件安装、更新、启用、删除、doctor API。
- `desktop/frontend/src/components/CapabilitiesPanel.tsx`：已有插件包管理页面。

AddOn 框架先复用这个落点，扩展 manifest、状态、更新和 UI 数据模型。

## 核心模型

### Core

Core 是 AddOn 的宿主，负责：

- LLM provider 调用和会话生命周期。
- tools、skills、hooks、MCP servers 的注册和执行入口。
- AddOn 安装、启用、禁用、更新、删除和诊断。
- AddOn 私有配置、状态、缓存和 secret 引用。
- 权限检查、失败暴露、日志和恢复。

Core 不理解每个业务 AddOn 的内部流程，只识别通用生命周期和能力声明。

### AddOn Package

AddOn Package 是磁盘上的插件包目录，至少包含 manifest。它可以携带：

- `skills/`：skill 目录。
- `hooks/`：hook 脚本。
- MCP server 声明，用作外部 AddOn 的独立进程运行时。
- 桌面管理页 panel 声明。
- AddOn 私有默认配置和配置 schema。
- 更新策略和来源元数据。
- secret 需求声明。

### Installed AddOn

Installed AddOn 是 core 持久化的运行时实例，记录：

- `name`、`source`、`root`、`version`、`enabled`。
- 安装、检查、更新、启用、失败时间。
- 当前 manifest 摘要和能力数量。
- 更新状态，如 `updateAvailable`、`remoteVersion`。
- 用户配置和 secret 引用。
- 最近一次可观察错误。

状态文件更新必须幂等。重复安装同一来源应稳定覆盖同名实例，重复启用/禁用不产生额外副作用。

## Manifest 草案

兼容当前 `WorkGround2-plugin.json` 和 `.codex-plugin/plugin.json`。新增字段尽量可选，旧插件包可继续运行。

```json
{
  "name": "team-skill-share",
  "version": "1.2.0",
  "description": "Shared skills for the team",
  "homepage": "https://example.internal/skills",
  "repository": "https://git.example.internal/ai/skills.git",
  "skills": ["skills"],
  "hooks": {},
  "mcpServers": {
    "skill-share-runtime": {
      "command": "bin/skill-share-runtime",
      "auto_start": true
    }
  },
  "addon": {
    "kind": "skill-share",
    "displayName": "Team Skill Share",
    "capabilities": ["skills", "update", "settings"],
    "runtime": {
      "type": "mcp",
      "mcpServer": "skill-share-runtime"
    },
    "panels": [
      {
        "id": "skill-share",
        "title": "Skill Share",
        "entry": "panels/skill-share"
      }
    ],
    "configSchema": "config.schema.json",
    "secrets": [
      {
        "id": "git-credential",
        "label": "Git credential",
        "purpose": "Read shared skill repository"
      }
    ],
    "update": {
      "type": "git",
      "strategy": "replace",
      "check": "manual-or-startup",
      "credential": "git-credential"
    },
    "storage": {
      "namespace": "team-skill-share"
    }
  }
}
```

当前已落地的外部运行时边界：

- AddOn 包通过 `addon.runtime.type = "mcp"` 声明运行时，并用 `mcpServer` 指向同包 `mcpServers` 中的 server。
- 主项目只验证 manifest、安装/启用包、把 MCP server 接进 `boot.Build` 的 Tool Registry，不把外部 AddOn 二进制编译进主项目。
- 包内进程通过环境变量拿到宿主接口：`WORKGROUND2_HOME`、`WorkGround2_HOME`、`WORKGROUND2_PLUGIN_ROOT`、`WORKGROUND2_PLUGIN_NAME`、`WORKGROUND2_ADDON_KIND`、`WORKGROUND2_ADDON_STORAGE_NAMESPACE`、`WORKGROUND2_ADDON_HOME`、`WORKGROUND2_ADDON_CONFIG_DIR`、`WORKGROUND2_ADDON_DATA_DIR`、`WORKGROUND2_ADDON_STATE_DIR`。
- `D:\Work\wg2addons\draw-tool` 是当前外部参考包；`D:\Work\wg2addons\draw-tool\runtime` 可以独立编译为包内 `bin/workground2-draw-addon(.exe)`，`D:\Work\wg2addons\scripts\build-addons.ps1` 会把 `draw-tool` 和 `skill-share` 打成 `D:\Work\wg2addons\dist\<name>-<version>-<os>-<arch>.zip`，主项目安装 zip 后通过 MCP runtime 或 host runtime metadata 加载 AddOn。

## 生命周期

### Plan

Plan 阶段只读取来源并解析 manifest，输出将要安装的 AddOn 名称、版本、能力和风险。它不修改安装状态。

### Install

Install 阶段把来源复制或解包到临时目录，解析 manifest，验证目录结构，再切换为目标目录。安装成功后才写入 `plugin-packages.json`。失败时保留旧版本，错误写入可观察状态。zip 包安装会先做路径逃逸、文件数量和总大小校验，再识别根目录或单一顶层插件目录。

### Enable

Enable 只修改实例启用状态。下一次 core build 会把该 AddOn 的 skills、hooks、MCP servers 合并进配置。重复 enable 返回当前状态。

### Activate

Activate 是 core build 的一部分，读取已启用 AddOn，合并能力，并给 MCP server 注入只读环境变量：

- `WorkGround2_PLUGIN_ROOT`
- `WorkGround2_PLUGIN_NAME`
- `WorkGround2_PLUGIN_VERSION`

后续扩展可以加 `WorkGround2_ADDON_STORAGE` 和 `WorkGround2_ADDON_CONFIG`，但不直接注入 secret 明文。

### Check Update

Check Update 查询远端版本或提交摘要，写入 `lastCheckedAt`、`remoteVersion`、`updateAvailable`、`lastError`。网络失败只影响检查结果，不改变当前启用能力。

### Update

Update 复用 Install 的临时目录和验证流程。只有新包验证成功才替换当前版本，并写入 `lastUpdatedAt`。替换失败时继续使用旧版本。

### Disable

Disable 修改启用状态，并在下一次运行时构建时移除能力。禁用不删除配置、状态和 secret 引用。

### Remove

Remove 先禁用，再删除安装目录和安装状态。默认保留用户配置和 secret 引用的删除确认权，避免误删连接凭据。

### Doctor

Doctor 输出 manifest、目录、能力数量、配置、secret 引用、更新来源和最近错误。它只读运行，适合在失败后反复执行。

## 存储和 Secret

AddOn 有三类持久数据：

- 安装状态：core 管理，记录版本、来源、路径和运行时状态。
- 用户配置：AddOn 私有 namespace，保存非敏感配置，如仓库 URL、扫描间隔、默认项目。
- Secret 引用：只保存 secret id 或 credential ref，不保存密码、token、API key 明文。

敏感数据由统一 secret store 或系统凭据管理器处理。AddOn 运行时通过受控 API 请求 secret，core 可以记录用途、权限和失败原因。

## 管理 UI

管理页面分为两层：

- AddOn 总览：安装、更新、启用、禁用、删除、doctor、状态徽标。
- AddOn 详情：配置项、secret 连接状态、更新历史、能力列表、最近错误。

Jira、飞书这类连接器可以声明独立 view。该 view 读取 AddOn 的状态 API 展示待办、同步状态和可执行动作。点击条目时可创建会话并把 issue/link/context 注入首条消息，也可打开外部页面。

## Core API 草案

框架侧应保持少量公共入口：

```go
type AddOnManager interface {
    List(ctx context.Context) ([]InstalledAddOn, error)
    Plan(ctx context.Context, source InstallSource) (*AddOnPlan, error)
    Install(ctx context.Context, source InstallSource, opts InstallOptions) (*InstalledAddOn, error)
    SetEnabled(ctx context.Context, name string, enabled bool) (*InstalledAddOn, error)
    CheckUpdate(ctx context.Context, name string) (*UpdateStatus, error)
    Update(ctx context.Context, name string) (*InstalledAddOn, error)
    Remove(ctx context.Context, name string, opts RemoveOptions) error
    Doctor(ctx context.Context, name string) (*DoctorReport, error)
}
```

能力读取保持从安装状态推导：

```go
type CapabilityProvider interface {
    SkillRoots() []string
    HookEntries() map[string][]HookSpec
    MCPServers() map[string]MCPServerSpec
    Panels() []PanelSpec
}
```

## 失败和恢复

- 来源不可达：保留当前版本，记录 `lastError`，允许手动重试。
- manifest 无效：安装和更新失败，旧版本继续可用。
- 更新中断：临时目录下次 doctor 或安装时清理，安装状态不切到半完成版本。
- secret 失效：连接器显示未连接或认证失败，能力仍可禁用、重配、重试。
- MCP server 启动失败：core 暴露具体 AddOn、server 名称和启动命令，允许用户禁用该 AddOn。
- skill 目录损坏：skill 发现跳过坏目录并记录 AddOn 名称，其他 AddOn 继续工作。

## MVP 阶段

1. 文档和 feature map：固定 AddOn 框架、Skill 共享插件设计和现有代码承载点。
2. Manifest 扩展：在 `internal/pluginpkg` 增加可选 `addon` 元数据、panel、secret、update、storage 字段。
3. 状态扩展：记录 `installedAt`、`lastCheckedAt`、`lastUpdatedAt`、`lastError`、能力摘要和可选更新状态。
4. Desktop API 扩展：列表、doctor、更新返回 AddOn 元数据，管理页能显示能力、版本、最近错误。
5. Skill 共享插件：以 Git 来源安装和刷新 skill 包，支持手动更新、强制刷新、删除和 doctor。凭据先存为 credential ref，明文输入只进入受控 secret 流程。

## 后续扩展

- 绘图 AddOn：把 API key 或 CLI 配置转成受控 tool，输出图片 artifact。
- 飞书 AddOn：OAuth 或 app credential 连接，暴露消息、文档、任务能力。
- Jira AddOn：同步 assigned issues，提供独立 view 和从 issue 创建会话的入口。
- AddOn 市场：签名、兼容版本、更新提醒、排序、组织内推荐。
- AddOn 事件：安装、启用、更新、连接失败、待办变化等事件进入统一观察流。
