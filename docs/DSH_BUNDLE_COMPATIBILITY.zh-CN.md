# DSH Bundle 兼容使用说明

## 安装

本地开发目录推荐链接安装，能保留 pnpm workspace 的依赖链接：

```powershell
WorkGround2 plugin install D:\Work\dsh\packages\bundle\base --link --yes
```

复制安装也受支持。若复制过程没有带入 workspace `node_modules` 链接，WG2 会记录源目录，并在源目录仍有效时自动回退到源 Bundle。zip、GitHub manifest 和 patch 也会先做静态预检，安装阶段不会执行 `!!js` 或第三方安装脚本。

## Host 与工具

启用的 DSH Bundle 在每个 WG2 Controller 中拥有一个独立 Node/Cordis sidecar 和 Agent：

- 工具名为 `dsh__<bundle>__<tool>`，避免与 WG2 原生工具冲突。
- 调用、取消和 Controller 关闭会传播到 DSH sidecar。
- 单个 Bundle 启动失败只产生警告，不阻塞其他 Bundle 或 WG2 会话。
- 外部 DSH 工具默认按可写、不允许 Plan Mode 处理，权限判断采取保守策略。

WG2 会从 Bundle 或仍有效的本地源向上寻找能解析 `@deepseek-ai/dsh-app-boot` 的 DSH CLI。自动发现失败时可设置：

```powershell
$env:DSH_RUNTIME_ANCHOR='D:\Work\dsh\apps\cli\package.json'
```

## UI 映像

进入“设置 → 插件”，展开 DSH Bundle：

1. 点击“启动 UI 映像”。
2. WG2 启动一个只监听 `127.0.0.1`、由系统分配端口的 DSH Web Host。
3. DSH 原生 React 18/Cordis Client 在 sandbox iframe 中展示；也可点击“在浏览器打开”。
4. 点击“停止映像”可释放进程；退出 WG2 时所有映像都会关闭。

UI 映像保留 DSH 的 Slot、Remote、Projection、Locale、Theme 和动态 Client 插件，兼容面较大。它使用独立 DSH Session，与 WG2 当前聊天不共享历史或进行中状态。这个边界是有意设计的，避免两套状态树出现看似同步、实际不可恢复的半完成状态。

## 诊断与恢复

- 安装计划显示 Cordis 行数、Client 行数、动态 `!!js` 数量、缺失包和静态兼容级别。
- Plugin Doctor 显示 Node 路径、运行时锚点和未解析依赖。
- Sidecar 或 UI 映像失败会显式返回错误；修复 Node、依赖或环境变量后可重复启动。
- 本地源码移动后，重新链接/安装 Bundle，或设置新的 `DSH_RUNTIME_ANCHOR`。

## 已验证插件

| DSH 插件 | 验证内容 | 结果 |
|---|---|---|
| `@deepseek-ai/dsh-tool-todo` | 真实 Agent 状态写入与结构化结果 | 通过 |
| `@deepseek-ai/dsh-tool-fs` | 从 WG2 Workspace 读取文件 | 通过 |
| `@deepseek-ai/dsh-tool-pwsh` | 长命令取消传播与有界返回 | 通过 |
| `@deepseek-ai/dsh-tool-skill` + `dsh-skill-filesystem` | 发现并读取 `.agents/skills/*/SKILL.md` | 通过 |
| `@deepseek-ai/dsh-client-ui-goal` | Web bootstrap 注册及 Client JS 可取回 | 通过 |
| `@deepseek-ai/dsh-client-ui-skill` | Web bootstrap 注册及 Client JS 可取回 | 通过 |

真实集成测试默认跳过，显式指定 DSH checkout 后运行：

```powershell
$env:DSH_COMPAT_TEST_ROOT='D:\Work\dsh'
go test ./internal/dshcompat/ -count=1
go test ./internal/boot/ -run TestBuildBridgesRealDSHTodoTool -count=1
```
