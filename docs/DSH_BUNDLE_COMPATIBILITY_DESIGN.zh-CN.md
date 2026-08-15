# DSH Bundle 兼容设计

## 目标

让 WorkGround2 能识别、安装、诊断并运行 DSH Bundle，在不破坏 WG2 单一会话与权限入口的前提下尽量复用 DSH 的 Host Tool、Skill、MCP 和 Client UI 能力。兼容结果必须可观察：已支持、降级、缺失依赖和拒绝原因都进入安装计划与 Doctor，禁止静默忽略 Bundle 行。

## 用户价值

- 已有 DSH Bundle 可以从本地目录、压缩包或包目录进入 WG2 插件管理流程。
- 兼容工具加入当前 WG2 Session 的 Tool Registry，沿用 WG2 权限、超时、取消和结果记录。
- 兼容 UI 进入 WG2 AddOn Workbench、设置、对话节点或输入区映像位置；未知 UI Slot 有明确的兼容工作台回退。
- 插件加载失败不影响其他插件，修复后可安全重试、禁用或重新诊断。

## 入口与主流程

1. `plugin install` / 设置页预检识别 `package.json.dsh.bundle.patch`。
2. 安装器读取 Bundle patch，形成稳定的 `DshBundle` 摘要和兼容报告；复制安装继续使用临时目录验证后切换。
3. 配置装配把已启用的 DSH Bundle 转成每 Session 的兼容运行时声明。
4. DSH Host sidecar 使用目标 DSH 安装中的 Node/Cordis Loader 挂载 Bundle；WG2 通过协议桥接 Tool/Prompt/Resource 与生命周期。
5. Desktop 加载 DSH Client Bundle，通过 Client Context 映像把 Slot、Session Projection、Remote、Locale 和 Theme 接到 WG2 Surface。
6. 未知服务、事件、Slot 或运行时依赖在 Doctor 和运行状态中显式显示；兼容运行时保持可停止、可重启。

## 核心模型

- `DshBundle`：Bundle 包名、版本、patch 路径、解析出的 Cordis 行和 Client 包目录。
- `DshCompatReport`：每项能力的 `native`、`bridged`、`fallback`、`unsupported` 状态与原因。
- `DshRuntimeSpec`：Node、DSH 安装根、Bundle 根、工作目录、超时和启动策略。
- `DshHost`：每 WG2 Controller/Session 一份 sidecar 所有者，负责启动、握手、健康状态、重启和关闭。
- `DshSurface`：Client Plugin 对 WG2 映像位置的稳定声明；未知 Slot 落入 AddOn Workbench。

## UI 映像

常见 Slot 显式映射：

| DSH Slot | WG2 Surface |
|---|---|
| `conversation.input.dock` | Composer 扩展区 |
| `conversation.chat.node` | 对话节点扩展区 |
| Tool call/result slots | WG2 Tool Card 扩展区 |
| Settings slots | 插件设置页 |
| Sidebar / root page | AddOn Workbench |
| 未知 Slot | Compatibility Workbench，显示原 Slot 名和诊断 |

WG2 使用 React 19，DSH Client Plugin 以 React 18 为 peer。兼容组件运行在独立 Renderer Island；首选独立 React 18 Root + ShadowRoot，依赖全局 DOM、Portal 或无法隔离的插件回退 sandbox iframe。两套 React 不共享组件、Hook 或可变 Store 对象。

## 状态与数据流

- WG2 原生会话状态由 WG2 保持单一可信源，适配器只生成 DSH Session Projection 快照。
- DSH 插件私有状态由 DSH sidecar 持有，WG2 保存运行状态和可恢复引用，不双写业务数据。
- UI 写操作回到明确的状态拥有者；请求携带 Session、Plugin、Run 和 Request ID，迟到响应按身份丢弃。
- Sidecar 启动、停止、重复握手和重启必须幂等；崩溃后保留最后错误并允许用户重试。

## 安全与安装

- WG2 不在普通插件安装阶段隐式执行第三方 `prepare`/`install` 脚本。
- 已构建 Bundle 可以直接使用；缺少构建产物时 Doctor 给出构建命令和缺失文件。
- Node sidecar 继承最小环境，敏感值通过现有凭据/权限入口提供，日志不得包含明文。
- Client Bundle 默认隔离，所有 Host 调用经过白名单 Remote/Bridge。

## 兼容等级

| 等级 | 能力 |
|---|---|
| L1 | DSH Bundle 清单、Skill 和静态资源 |
| L2 | MCP、Prompt、Resource 和无状态 Tool |
| L3 | Cordis Host Service、事件、每 Session 生命周期 |
| L4 | 常用 Client Slot、Projection、Remote 和 Locale/Theme |
| L5 | 未知 Slot 回退、动态 Host/Client Package 与完整诊断 |

每个 Bundle 按实际证据报告等级，不能用一个成功工具调用宣称完整兼容。

## 首批兼容测试

- `@deepseek-ai/dsh-client-ui-goal`：验证 `dsh.client` 发现、`conversation.input.dock`、Projection 和 Remote。
- `@deepseek-ai/dsh-tool-todo`：验证 `ctx.tools` 注册、结构化结果和 Session 所有权。
- `@deepseek-ai/dsh-skill-badge` / `dsh-skill-filesystem`：验证 Skill 发现与资源路径。
- 一个纯函数 Tool fixture：验证最小 Cordis Tool 插件可稳定桥接并重复调用。
- 一个带未知 Slot 的 fixture：验证回退面板和显式诊断。

## 验证

- Go：`pluginpkg`、`installsource`、`config`、`plugin`、`boot`、`desktop` 专项测试与 `go vet`。
- Frontend：兼容 Loader、Slot 映像、崩溃隔离、Session 切换、卸载和未知 Slot 回退测试；TypeScript、CSS 门禁与生产构建。
- Integration：真实 DSH checkout 的多个 Bundle/Plugin 预检、启动、调用、UI 加载、禁用和重启。
