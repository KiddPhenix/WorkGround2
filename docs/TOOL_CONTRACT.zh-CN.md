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

`ask`, `browser_click`, `browser_close`, `browser_navigate`, `browser_open`, `browser_scroll`, `browser_state`, `browser_tab`, `browser_type`, `explore`, `forget`, `history`, `install_skill`, `install_source`,
`list_sessions`, `lsp_definition`, `lsp_diagnostics`, `lsp_hover`,
`lsp_references`, `memory`, `parallel_tasks`, `read_only_skill`,
`read_only_task`, `read_session`, `read_skill`, `remember`, `request_help`, `research`,
`review`, `run_skill`, `security_review`, `slash_command`, `task`.

八个运行时绑定的浏览器工具按 parent session 独占浏览器进程：

| 工具 | Read-only | 合约 |
| --- | --- | --- |
| `browser_open` | false | 幂等打开当前会话浏览器，可选导航到 HTTP/HTTPS URL。 |
| `browser_navigate` | false | 导航当前标签页，要求稳定的 `request_id`。 |
| `browser_state` | true | 返回页面文本、标签页、`revision` 和带编号交互元素；不返回截图或表单值。 |
| `browser_click` | false | 按指定 `revision` 和元素编号点击，陈旧 revision 显式失败。 |
| `browser_type` | false | 向可编辑元素输入普通非敏感文本；拒绝 password/file 输入。 |
| `browser_scroll` | false | 按 revision 滚动视口或指定元素。 |
| `browser_tab` | false | 按 revision 新建、激活或关闭标签页。 |
| `browser_close` | false | 幂等关闭且只关闭当前 parent session 的浏览器。 |

`[tools.browser].enabled=false`、`tools.enabled` 未选中或 token economy
模式都会隐藏浏览器工具。首选 Google Chrome，同一 CDP 合约也支持 Edge、
Chromium 和 Chrome for Testing。V1 只使用隔离的临时 Profile，不提供截图、
上传下载、敏感信息输入、日常 Chrome Profile/Cookie/登录态或密码库访问。

`internal/boot.TestBootToolContractMatchesProviderVisibleSurface` 会校验真实 boot registry 合约和 provider request 一致，包括 read-only 标记和 canonical schema。

## Token Economy Boot Surface

token economy 模式启动时保留核心编码、session、memory 工具，以及按需启用可选来源的 connector：

`ask`, `connect_tool_source`, `forget`, `history`, `list_sessions`, `memory`,
`read_session`, `remember`, `slash_command`.

`bash`、`read_file`、`grep`、文件写工具、后台 job 工具和 `todo_write` 等核心内置工具在 economy 模式下仍可用，见上方内置工具表。
