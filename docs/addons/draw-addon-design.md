# 画图工具 AddOn 设计

本文档记录 WorkGround2 `draw-tool` AddOn 的 MVP 设计。它把多来源画图 provider、secret 引用、CLI/API 两种执行模式、可恢复任务状态和模型侧 `draw_image` tool 串到同一个外部 AddOn 包能力上。主项目只提供 AddOn package 安装、更新、启停、卸载和通用管理块；`draw-tool` 源码、runtime 和 zip 打包链路位于 `D:\Work\wg2addons\draw-tool`。

## 目标

- 用户可以单独配置画图 provider，不污染普通聊天模型 provider。
- provider 支持 `api` 和 `cli` 两种模式，并允许同一台机器保存多个来源。
- API key 只保存为 `apiKeyRef`，真实值进入现有 credential store。
- 生成任务返回独立 `TaskView`，失败有 `phase`、`error`、`retryable`，便于 UI 展示和安全重试。
- CLI 模式可以在本机真实执行，prompt 默认走 stdin，也支持 args 模板。
- API 模式 MVP 先返回 dry-run plan 状态，保留 OpenAI image-compatible 请求骨架需要的配置字段。
- 配置写入、删除和任务状态更新可重复执行；中断后能通过 provider state 发现并恢复到可重试状态。

## 用户流程

1. 用户安装 `D:\Work\wg2addons\dist\draw-tool-<version>-<os>-<arch>.zip` 后，Settings -> Plugins 显示 Draw AddOn 的通用 AddOn package 块。
2. provider 配置由 AddOn 自带 panel schema 或 `<WorkGround2 home>/addons/draw-tool/config.json` 写入。配置模式包括：
   - `api`：填写 `baseURL`、`model`、可选 API key。
   - `cli`：填写本地命令、参数模板、可选输出目录和 API key 引用。
3. 如果用户输入 key，AddOn 配置流程先保存 provider 配置，再通过宿主 credential 能力写入 credential store；失败时回滚或暴露可重试状态。
4. 用户发起生成时，外部 MCP runtime 通过 `draw_image` tool 读取配置并执行。
5. runtime 写入 running 状态，执行 CLI 或 API dry-run，然后写回最终状态。
6. UI 只读取 AddOn package 状态、`TaskView` 和 provider `state`，不接触 secret 明文。

## 配置模型

配置落在：

```text
<WorkGround2 home>/addons/draw-tool/config.json
```

结构：

```json
{
  "version": 1,
  "providers": [
    {
      "id": "deepseek-image",
      "enabled": true,
      "displayName": "DeepSeek Image",
      "mode": "api",
      "baseUrl": "https://example.com/v1/images/generations",
      "model": "image-model",
      "apiKeyRef": "DRAWADDON_DEEPSEEK_IMAGE_API_KEY",
      "state": {
        "status": "ready",
        "lastTaskId": "deepseek-image-...",
        "lastStartedAt": "2026-07-04T10:00:00Z",
        "lastFinishedAt": "2026-07-04T10:00:01Z",
        "lastOutputPath": "",
        "lastError": ""
      }
    }
  ]
}
```

核心字段：

- `id`：本地 provider ID，稳定用于任务 ID、默认 secret key 和状态归属。
- `enabled`：禁用后生成入口显式失败。
- `mode`：`api` 或 `cli`。
- `baseURL`、`model`：API 请求骨架字段；CLI 也可通过模板或环境变量使用。
- `apiKeyRef`：credential store key 或 `env:<KEY>` 引用。
- `cliCommand`、`cliArgs`：本地 CLI 可执行入口。
- `outputDir`：CLI 输出目录；相对路径会落到 AddOn 自己的 outputs 目录下。
- `state`：最后一次任务状态，作为 UI 展示和中断恢复依据。

## Secret 处理

- 桌面保存方法 `SaveDrawAddonProvider(input, secretValue?)` 接收一次性明文。
- 明文只交给 `config.SetCredential`。
- `config.json` 只保存 `apiKeyRef`。
- 默认 key 形如 `DRAWADDON_<PROVIDER_ID>_API_KEY`。
- `ResolveCredential(home, ref)` 支持普通 key 和 `env:` 前缀，优先读取 WorkGround2 全局 credential store，再回退环境。
- 错误文本会脱敏 URL userinfo、query secret、Authorization/API key/token 片段和已解析出的 secret 值。

## CLI 模式

CLI provider 示例：

```json
{
  "id": "local-comfy",
  "enabled": true,
  "mode": "cli",
  "cliCommand": "comfy-draw",
  "cliArgs": ["--prompt", "{{prompt}}", "--out", "{{output}}"],
  "outputDir": "comfy"
}
```

执行规则：

- `cliArgs` 中出现 `{{prompt}}` 或 `{prompt}` 时，prompt 进入参数。
- 没有 prompt 模板时，prompt 通过 stdin 传入，适合长文本和中文。
- `{{output}}` / `{{outputPath}}` 会替换为后端生成的目标路径。
- 还支持 `{{model}}`、`{{baseURL}}`、`{{providerId}}`。
- 后端会设置 `DRAW_ADDON_OUTPUT`、`DRAW_ADDON_MODEL`、`DRAW_ADDON_BASE_URL`。
- 如果有 `apiKeyRef`，后端设置 `DRAW_ADDON_API_KEY`，并在 key 名合法时同时设置原 key 环境变量。
- CLI 成功后优先从 stdout 读取输出路径；stdout 可直接打印路径，也可打印 `{"outputPath":"..."}`。
- stdout 没给路径且配置了 `outputDir` 时，返回后端生成的目标路径。

## API 模式

API provider 示例：

```json
{
  "id": "openai-compatible-image",
  "enabled": true,
  "mode": "api",
  "baseUrl": "https://gateway.example.com/v1/images/generations",
  "model": "image-model",
  "apiKeyRef": "DRAWADDON_OPENAI_COMPATIBLE_IMAGE_API_KEY"
}
```

MVP 行为：

- 校验 provider 已启用、`baseURL` 和 `model` 已配置。
- 如果有 `apiKeyRef`，先解析 credential，缺失时任务失败到 `needs_auth`。
- 不发真实网络请求，返回 `phase=api_dry_run` 的成功任务。
- 后续实现真实请求时可复用同一配置模型和 `TaskView`，失败继续走显式 phase、retryable 和脱敏错误。

## Tool / Skill 暴露方式

MVP 的模型可调用 tool：

- `draw_image`
- 输入：`prompt` 必填，`providerId` 可选。
- `providerId` 为空时，后端选择第一个启用的 provider。
- 输出：JSON 格式的 `TaskView`，包含 `taskId`、`providerId`、`phase`、`status`、`outputPath`、`error` 和 `retryable`。
- 注册规则：运行 `D:\Work\wg2addons\scripts\build-addons.ps1` 生成 `D:\Work\wg2addons\dist\draw-tool-<version>-<os>-<arch>.zip`，再通过 `WorkGround2 plugin install <zip> --yes` 安装；manifest 中 `addon.runtime` 指向包内 `draw-addon` MCP server，由独立编译的 `workground2-draw-addon` 进程通过 MCP 暴露 `draw_image`。未安装该 AddOn 包时，主项目不会默认注册 `draw_image`。

后续可以继续补：

- skill：`draw-addon`，把 provider 配置、prompt 改写和输出文件引用组织成可复用流程。
- 泛型 panel renderer：把 `panels/providers.schema.json` 渲染为外部 AddOn 的配置 UI。

## MVP 范围

已覆盖：

- 多 provider 保存、读取、删除。
- `config.json` 原子写入。
- secret ref 保存和解析 helper。
- CLI 本地执行。
- API dry-run。
- 模型侧 `draw_image` tool。
- 外部 AddOn 包 `D:\Work\wg2addons\draw-tool`、可单独编译的 MCP runtime `D:\Work\wg2addons\draw-tool\runtime`，以及可安装的 zip 包构建链路 `D:\Work\wg2addons\scripts\build-addons.ps1` / `D:\Work\wg2addons\scripts\pack-addon.ps1`。
- Settings -> Plugins 中的已安装 Draw AddOn package 通用管理块。
- 任务状态写入和返回。
- 进程中断后，下次读取 provider 会把遗留 `running` 标记成可重试失败。
- 单元测试覆盖多 provider、secret 不泄漏、fake CLI 成功、重复删除、错误脱敏。

暂不覆盖：

- 真实 API 网络请求。
- 后台异步任务队列。
- 图片预览 UI。
- provider marketplace。
- secret vault 多后端策略。

## 风险与处理

- CLI 命令长时间挂住：MVP 使用默认超时，失败返回 retryable。
- CLI 不输出路径：要求 stdout 输出路径，或配置 `outputDir` 让后端生成目标路径。
- CLI stderr 泄漏 key：错误进入 UI 前统一脱敏。
- 用户配置相对输出目录：统一放到 AddOn 自己的 outputs 下，减少误写项目文件。
- API key 写入失败：桌面绑定尝试回滚 provider 配置；如果回滚也失败，provider 会因 credential 缺失进入可恢复状态。
- 真实 API 供应商差异大：MVP 只固定 OpenAI image-compatible 字段骨架，真实请求在后续按 provider 能力扩展。
