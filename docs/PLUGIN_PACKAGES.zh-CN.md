# WorkGround2 插件包

WorkGround2 插件包把 skills、hooks 和 MCP servers 组织成一个可安装单元。

## CLI 模式

在终端里使用 `WorkGround2 plugin` 安装和管理插件包。插件包当前按全局范围安装，
写入 WorkGround2 home 目录。

### 通过 CLI 安装

`install` 接收一个来源：

- GitHub 仓库，例如 `git:github.com/obra/superpowers` 或
  `https://github.com/obra/superpowers`。
- GitHub 分支或子目录 URL，例如
  `https://github.com/owner/repo/tree/main/path/to/plugin`。
- 本地目录，目录内需要包含 `WorkGround2-plugin.json` 或
  `.codex-plugin/plugin.json`。
- 本地 `.zip` 包，包内可以在根目录直接包含插件 manifest，也可以包含一个顶层插件目录。

只预览安装计划，不写文件：

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --dry-run
```

确认计划后安装：

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --yes
```

指定安装名称，或覆盖已安装的同名插件：

```bash
WorkGround2 plugin install git:github.com/obra/superpowers --name superpowers --replace --yes
```

以开发模式使用本地目录：

```bash
WorkGround2 plugin install /path/to/plugin --link --replace --yes
```

安装已打包的 AddOn：

```bash
WorkGround2 plugin install D:\Work\wg2addons\dist\draw-tool-0.1.0-windows-amd64.zip --yes
```

CLI 安装参数：

- `--dry-run` 只规划和校验安装，不写文件。
- `--yes` 用于确认执行会写文件的安装。
- `--replace` 允许当前来源替换已安装的同名插件。
- `--name <name>` 或 `--name=<name>` 覆盖插件 manifest 里的名称，
  作为本次安装名称。
- `--link` 链接本地插件目录，而不是复制到 WorkGround2 的插件存储目录。
  移动或删除该目录会导致这个链接插件失效。

如果运行 `WorkGround2 plugin install <source>` 时既没有 `--dry-run`，
也没有 `--yes`，CLI 会拒绝写文件，并提示使用其中一个参数重新运行。
安装和移除命令会输出结构化 JSON，来源于桌面端同一套 install-source 后端。

插件状态和内容写入：

```text
~/.WorkGround2/plugin-packages.json
~/.WorkGround2/plugins/<name>/
```

### 通过 CLI 管理

列出已安装插件：

```bash
WorkGround2 plugin list
```

查看某个插件的元数据、根目录、来源以及导出的能力数量：

```bash
WorkGround2 plugin show superpowers
```

检查 manifest 和 skill roots 是否可读：

```bash
WorkGround2 plugin doctor superpowers
```

在不卸载的情况下启用或禁用插件：

```bash
WorkGround2 plugin disable superpowers
WorkGround2 plugin enable superpowers
```

移除插件：

```bash
WorkGround2 plugin remove superpowers --yes
```

`remove` 也可以写成 `uninstall`。它需要 `--yes`，
因为会写入状态并删除复制安装的插件内容。如果是链接模式安装的本地插件，
外部源目录会保留。

## 桌面端设置

打开 **设置 -> 插件**，可以不用 CLI 直接安装和管理插件包。

### 安装插件

安装区有两种模式：

- **本地 zip/目录**：点击 **选择插件 zip** 从磁盘选择已打包的插件包；需要调试源码时，
  点击 **选择插件目录** 选择插件目录。选中路径会显示在按钮右侧。
- **Git 仓库**：填写 Git 来源，例如 `git:github.com/obra/superpowers`。
  **安装名称（可选）** 可覆盖插件 manifest 声明的名称，用于本次安装或覆盖。

选择来源和选项后，再使用操作按钮：

- **预检** 校验来源并展示计划安装动作，不写入文件。
- **安装插件** 按当前来源和选项执行安装。
- **刷新插件** 从磁盘和配置重新读取已安装插件列表。

安装选项：

- **覆盖同名插件** 允许当前来源替换已安装的同名插件。关闭时，同名安装会失败，
  而不是覆盖已有内容。
- **开发模式：链接所选目录** 只对目录来源可用。它不会复制插件，
  而是直接链接所选目录；适合开发或调试插件。移动或删除该目录会导致这个链接插件失效。

对新的 Git 来源、本地 zip 或本地插件目录，建议先点 **预检**。

### 管理已安装插件

已安装插件列表会展示每个插件包以及它导出的 skills、hooks 和 MCP servers。
通过应用外编辑插件文件或配置后，可点 **刷新插件** 重新读取。

展开插件行后可以：

- 启用或禁用插件。
- 使用 **更新** 拉取或刷新具备更新来源的插件。
- 使用 **诊断** 检查插件 manifest，并查看警告或诊断信息。
- 使用 **移除插件**，确认后卸载该插件包。

## 原生 Manifest

WorkGround2 原生插件在根目录声明 `WorkGround2-plugin.json`：

```json
{
  "name": "example",
  "version": "1.0.0",
  "description": "Example plugin",
  "skills": "skills",
  "hooks": {
    "SessionStart": [
      {
        "command": "hooks/session-start",
        "description": "Load startup context"
      }
    ]
  },
  "mcpServers": {
    "helper": {
      "command": "bin/helper"
    }
  }
}
```

相对路径都按插件根目录解析。WorkGround2 安装插件时不会执行第三方安装脚本。

## AddOn 包

AddOn 包复用同一套插件包安装器，再在 manifest 里增加 `addon` 块。为了让 AddOn
单独编译，实际可执行能力应通过 `mcpServers` 暴露，并让 `addon.runtime` 指向其中一个
server：

```json
{
  "name": "draw-tool",
  "version": "0.1.0",
  "addon": {
    "kind": "draw-tool",
    "displayName": "Draw AddOn",
    "runtime": { "type": "mcp", "mcpServer": "draw-addon" },
    "panels": [{ "id": "providers", "title": "Providers", "entry": "panels/providers.schema.json" }],
    "configSchema": "panels/providers.schema.json",
    "storage": { "namespace": "draw-tool" }
  },
  "mcpServers": {
    "draw-addon": { "command": "bin/workground2-draw-addon", "auto_start": true }
  }
}
```

宿主会给包内 MCP server 注入 `WORKGROUND2_HOME`、`WorkGround2_HOME`、
`WORKGROUND2_PLUGIN_ROOT`、`WORKGROUND2_PLUGIN_NAME`、`WORKGROUND2_ADDON_KIND`、
`WORKGROUND2_ADDON_STORAGE_NAMESPACE`、`WORKGROUND2_ADDON_HOME`、
`WORKGROUND2_ADDON_CONFIG_DIR`、`WORKGROUND2_ADDON_DATA_DIR` 和
`WORKGROUND2_ADDON_STATE_DIR`。secret 声明只作为 metadata，明文不会注入环境变量。

构建参考 Draw AddOn 包：

```powershell
D:\Work\wg2addons\scripts\build-addons.ps1
```

主项目里的 `.\scripts\build-addons.ps1` 是指向外部 AddOn 根的 wrapper。有 `make`
的环境也可以执行 `make build-addons`。构建会把运行时 binary 写到
`D:\Work\wg2addons\draw-tool\bin\`，并在 `D:\Work\wg2addons\dist\` 下生成可分发包，例如：

```text
D:\Work\wg2addons\dist\draw-tool-0.1.0-windows-amd64.zip
```

这个包可以用 `WorkGround2 plugin install <zip> --yes` 安装。会话启动时，WorkGround2
读取已安装包的 metadata，给包内 MCP server 注入 AddOn 环境变量，并把 server 暴露的
tools 注册进当前 Tool Registry；AddOn runtime 不需要编译进主项目主 binary。

## Codex 兼容

WorkGround2 也会读取 `.codex-plugin/plugin.json`。对于 Superpowers 这类插件，
WorkGround2 会映射：

- `skills` 到 WorkGround2 skill root。
- 如果存在 `hooks/session-start-codex`，映射为 WorkGround2 `SessionStart` hook。

插件 hook 会收到这些环境变量：

- `WorkGround2_PLUGIN_ROOT`
- `WorkGround2_PLUGIN_NAME`
- `WorkGround2_PLUGIN_VERSION`
- `WorkGround2_HOME`
- `WorkGround2_WORKSPACE_ROOT`

## 桌面端后端方法

Desktop 通过 Wails 方法暴露插件包操作：

- `Plugins`
- `PlanPluginInstall`
- `InstallPlugin`
- `RemovePlugin`
- `SetPluginEnabled`
- `UpdatePlugin`
- `PluginDoctor`
