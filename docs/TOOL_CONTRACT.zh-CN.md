# 工具合约

<a href="./TOOL_CONTRACT.md">English</a>

本文记录 WorkGround2 编译期内置工具的 provider-visible 合约。运行时 registry 使用同一条 canonical schema 路径；测试会校验这里列出的工具名、read-only 标记和 schema 快照不会漂移。

| 工具 | Read-only | 说明 |
| --- | --- | --- |
| `bash` | false | 执行 shell 命令并返回 stdout/stderr。构建、测试、git、包管理器等使用它；读写查找文件优先使用专用工具。 |
| `bash_output` | true | 读取后台 `bash` 或 `task` job 自上次读取后的新增输出和状态。 |
| `code_index` | true | 轻量内置代码符号索引；优先使用 `lsp_*` 或代码图 MCP，缺失时用它兜底。 |
| `complete_step` | true | 用证据记录已批准计划中一个步骤的完成情况。 |
| `delete_range` | false | 用精确 start/end 文本锚点删除文件中的连续范围。 |
| `delete_symbol` | false | 用 Go AST 删除 Go 源文件中的命名符号。 |
| `edit_file` | false | 将文件中的唯一精确字符串替换为另一个字符串。 |
| `glob` | true | 查找匹配 glob pattern 的文件。 |
| `grep` | true | 在文件或目录下按正则搜索文本。 |
| `kill_shell` | false | 终止后台 `bash` 或 `task` job。 |
| `ls` | true | 列出目录条目，可递归。 |
| `move_file` | false | 移动或重命名文件。 |
| `multi_edit` | false | 对单个文件原子应用多个编辑。 |
| `notebook_edit` | false | 编辑 Jupyter notebook 的单个 cell。 |
| `read_file` | true | 按可分页的行号格式读取文本文件。 |
| `todo_write` | true | 记录并替换当前工作的结构化任务列表。 |
| `wait` | true | 等待后台 job 完成并返回最终输出。 |
| `web_fetch` | true | 通过 HTTP/HTTPS 获取 URL 文本内容。 |
| `write_file` | false | 写入文件内容，必要时创建父目录。 |

## Schema 快照

完整 canonical schema 不在文档中手写，避免文档和代码手工漂移。运行：

```bash
go test ./internal/tool -run TestBuiltinToolContractDocumentation
```

该测试会用 `tool.BuiltinContractEntries` 校验每个内置工具都有文档行、read-only 标记、非空 description 和 canonical JSON schema。

## 默认 Full Boot Surface

默认 full-token boot 会发送上面的内置工具，并额外发送 session、memory、skill、subagent、LSP、install 和 slash-command 工具：

`ask`, `browser_attach`, `browser_click`, `browser_close`, `browser_navigate`, `browser_open`, `browser_scroll`, `browser_state`, `browser_tab`, `browser_type`, `browser_upload`, `explore`, `forget`, `history`, `install_skill`, `install_source`,
`list_sessions`, `lsp_definition`, `lsp_diagnostics`, `lsp_hover`,
`lsp_references`, `memory`, `notify_me`, `parallel_tasks`, `read_only_skill`,
`read_only_task`, `read_session`, `read_skill`, `rebuild_vocabulary`, `remember`, `request_help`, `research`,
`review`, `run_skill`, `security_review`, `slash_command`, `task`.

`notify_me` 会在用户明确要求的任务进入终态后，创建一条持久且无需回复的主人通知。
它有副作用，在计划模式和 Token Economy 模式下不可用；用户配置的 deny/ask 规则仍优先于默认允许规则。

十个运行时绑定的浏览器工具按用户共享一个持久化自动化浏览器（跨 Controller、Task、设置重建和应用重启复用）：

| 工具 | Read-only | 合约 |
| --- | --- | --- |
| `browser_open` | false | 幂等打开当前会话浏览器，可选导航到 HTTP/HTTPS URL。 |
| `browser_attach` | true | 返回当前会话的回环 CDP endpoint，供 Playwright `chromium.connectOverCDP()` 使用。必须先 `browser_open`；绝不启动第二个浏览器。任何 Playwright 写操作后必须调用 `browser_state(refresh=true)`。 |
| `browser_navigate` | false | 导航当前标签页，要求稳定的 `request_id`。`allow_leave=true` 时接受 `beforeunload` 对话框并离开页面；默认留在页面并返回 `dialog_blocked`。 |
| `browser_state` | true | 返回页面文本、标签页、`revision` 和带编号交互元素；不返回截图或表单值。 |
| `browser_click` | false | 按指定 `revision` 和元素编号点击，陈旧 revision 显式失败。`allow_leave=true` 时接受点击触发的 `beforeunload` 对话框；默认留在页面并返回 `dialog_blocked`。 |
| `browser_type` | false | 向可编辑元素输入普通文本；password 输入在 `allow_password_input=false` 时拒绝，file 输入始终拒绝。 |
| `browser_scroll` | false | 按 revision 滚动视口或指定元素。 |
| `browser_tab` | false | 按 revision 新建、激活或关闭标签页。关闭被 `beforeunload` 阻止的标签页时返回 `dialog_blocked`，除非 `allow_leave=true`。 |
| `browser_upload` | false | 向 `input[type=file]` 设置 1-20 个存在的本地普通文件，所选文件内容会交给页面。路径会原样进入 ToolCall transcript；多文件目标要求 `multiple` 属性；`allow_file_upload=false` 时拒绝。 |
| `browser_close` | false | 幂等仅分离当前 parent session 的浏览器客户端；共享 Chromium 及其持久化 profile 继续存活。 |

`[tools.browser].enabled=false`、`tools.enabled` 未选中或 token economy
模式都会隐藏浏览器工具。首选 Google Chrome，同一 CDP 合约也支持 Edge、
Chromium 和 Chrome for Testing。V1 只使用独立的自动化 Profile（区别于任何
默认浏览器 Profile），不提供截图、拖放、目录上传、下载、敏感信息输入、
日常 Chrome Profile/Cookie/登录态或密码库访问。`allow_password_input` 与
`allow_file_upload` 默认均开启且可独立关闭；下载保持拒绝。

`internal/boot.TestBootToolContractMatchesProviderVisibleSurface` 会校验真实 boot registry 合约和 provider request 一致，包括 read-only 标记和 canonical schema。

### JavaScript 对话框处理

原生 JavaScript 对话框（`alert`、`confirm`、`prompt`、`beforeunload`）按 target 处理，绝不让操作挂到超时：

- `beforeunload` 默认 dismiss（留在页面，未保存数据保留）；发起动作的 `browser_navigate` / `browser_click` / `browser_tab` `action=close` 返回结构化 `dialog_blocked` 错误，而不是超时或静默成功。同一工具传 `allow_leave=true` 可接受对话框并离开页面。
- 意外的 `alert` 会被接受以免页面死锁；意外的 `confirm` / `prompt` 默认 dismiss 并返回 `dialog_blocked`。需要接受时，通过 `browser_attach` 交给 Playwright 处理。
- `dialog_blocked` 可恢复并携带 `dialog` 上下文（target id、类型、消息）。导航和关闭标签页的结果明确为“已停留”；点击可能已执行部分页面 handler，因此返回 outcome-unknown，调用方应先用 `browser_state` 对账再决定是否重试。CDP 接受/取消失败返回 outcome-unknown 的 `dialog_resolution_failed`。策略按 target 和请求限定，并在成功、失败、取消、迟到事件、target 切换与 driver 关闭时清理。

## Token Economy Boot Surface

token economy 模式启动时保留核心编码、session、memory 工具，以及按需启用可选来源的 connector：

`ask`, `connect_tool_source`, `forget`, `history`, `list_sessions`, `memory`,
`read_session`, `rebuild_vocabulary`, `remember`, `slash_command`.

`rebuild_vocabulary` 是同名内置 inline Skill 背后的写工具；它会确定性重建
当前 Workspace 的词表，并刷新当前 Session 的补全快照。

`bash`、`read_file`、`grep`、文件写工具、后台 job 工具和 `todo_write` 等核心内置工具在 economy 模式下仍可用，见上方内置工具表。
