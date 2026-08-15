# DSH Bundle 兼容设计

## 目标

让 WorkGround2 能识别、安装、诊断并运行 DSH Bundle，在不破坏 WG2 单一会话与权限入口的前提下尽量复用 DSH 的 Host Tool（含 Skill/MCP 暴露的 Tool）和 Client UI 能力。兼容结果必须可观察：已支持、降级、缺失依赖和拒绝原因都进入安装计划与 Doctor，禁止静默忽略 Bundle 行。

## 用户价值

- 已有 DSH Bundle 可以从本地目录、压缩包或包目录进入 WG2 插件管理流程。
- 兼容工具加入当前 WG2 Session 的 Tool Registry，沿用 WG2 权限、超时、取消和结果记录。
- 兼容 UI 进入 WG2 插件设置中的隔离 DSH Workbench；DSH Slot、Remote、Projection 和主题仍由原生 DSH Web Host 提供。
- 插件加载失败不影响其他插件，修复后可安全重试、禁用或重新诊断。

## 入口与主流程

1. `plugin install` / 设置页预检识别 `package.json.dsh.bundle.patch`。
2. 安装器读取 Bundle patch，形成稳定的 `DshBundle` 摘要和兼容报告；复制安装继续使用临时目录验证后切换。
3. 配置装配把已启用的 DSH Bundle 转成每 Session 的兼容运行时声明。
4. DSH Host sidecar 使用目标 DSH 安装中的 Node/Cordis Loader 挂载 Bundle；WG2 通过协议桥接 Tool、取消与生命周期。
5. Desktop 按需启动 loopback-only 的 DSH Web profile，把目标 Bundle patch 叠加到原生 Client Host，并在 sandbox iframe 中映像。
6. 未知服务、事件、Slot 或运行时依赖在 Doctor 和运行状态中显式显示；兼容运行时保持可停止、可重启。

## 核心模型

- `DshBundle`：Bundle 包名、版本、patch 路径、解析出的 Cordis 行和 Client 包目录。
- `DshCompatReport`：每项能力的 `native`、`bridged`、`fallback`、`unsupported` 状态与原因。
- `DshRuntimeSpec`：Node、DSH 安装根、Bundle 根、工作目录、超时和启动策略。
- `DshHost`：每 WG2 Controller/Session 一份 sidecar 所有者，负责启动、握手、健康状态、重启和关闭。
- `DshSurface`：独立 DSH Web Host 的 URL、进程状态、错误和启停入口。

## UI 映像

WG2 使用 React 19，DSH Client Plugin 以 React 18 为 peer。当前选择完整映像优先：DSH Client Bundle 在独立 Node/Cordis/Web/React 18 Host 中运行，WG2 只持有进程状态和 loopback URL，并通过 sandbox iframe 展示。两套 React 不共享组件、Hook、路由或可变 Store 对象。

该方案可保留 DSH 的已知及未知 Slot、Remote、Locale、Theme 和动态 Client Package，避免逐项复制 UI 协议。代价是映像拥有独立 DSH Session，不伪装成 WG2 当前会话。未来如需把少量高价值 Slot 原生嵌入 Composer 或 Tool Card，应新增显式 Projection/Remote 适配器并逐项验收，不能直接共享 React Context。

## 状态与数据流

- WG2 原生会话状态由 WG2 保持单一可信源；当前 UI 映像拥有独立 DSH Session，不双写或镜像 WG2 会话。
- DSH 插件私有状态由 DSH sidecar 持有，WG2 保存运行状态和可恢复引用，不双写业务数据。
- UI 写操作回到明确的状态拥有者；请求携带 Session、Plugin、Run 和 Request ID，迟到响应按身份丢弃。
- Sidecar 启动、停止、重复握手和重启必须幂等；崩溃后保留最后错误并允许用户重试。

## 安全与安装

- WG2 不在普通插件安装阶段隐式执行第三方 `prepare`/`install` 脚本。
- 已构建 Bundle 可以直接使用；缺少构建产物时 Doctor 给出构建命令和缺失文件。
- Node sidecar 继承最小环境，敏感值通过现有凭据/权限入口提供，日志不得包含明文。
- Client Bundle 默认隔离，其 Host 调用留在 loopback-only 的原生 DSH Web transport 内。

## 兼容等级

| 等级 | 能力 |
|---|---|
| L1 | DSH Bundle 清单、Skill 和静态资源 |
| L2 | MCP、Prompt、Resource 和无状态 Tool |
| L3 | Cordis Host Service、事件、每 Session 生命周期 |
| L4 | 经显式适配的常用 Client Slot、Projection、Remote 和 Locale/Theme（当前未宣称） |
| L5 | 独立 DSH Workbench 中的动态 Host/Client Package、任意 Slot 回退与完整诊断 |

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
