# Workspace 词汇库

Workspace 词汇库用于记录项目专有名词、动词和固定短语，并在 Desktop Session 输入框中提供 IDE 风格的灰色后缀补全。输入中文至少 2 个字符、其他文字至少 3 个字符后，命中时按 `Tab` 接受，按 `Esc` 暂时忽略；`Shift+Tab` 仍保留为模式切换。

## 词汇来源与优先级

同一词条按 `Agent > Skill > Workspace > 自动学习` 合并。高优先级来源提供名称、类型和说明；使用次数、证据次数及所有来源会保留，用于稳定排序。

### Workspace 词汇

在项目中创建 `.WorkGround2/vocabulary.toml`：

```toml
version = 1

[[terms]]
text = "多模态生视频V5"
kind = "noun" # noun / verb / phrase
description = "项目中的多模态视频生成节点"
aliases = ["多模V5"]
preferred = true
```

该文件适合纳入版本控制，作为团队共享词汇表。别名命中后会提示并补全为规范词。

### Skill 自带词汇

简单词条可写入 `SKILL.md` frontmatter：

```yaml
---
name: video-workflow
description: 视频工作流
vocabulary: 多模态生视频V5, 角色设定Pro, 批量跑图 — 批量执行生成任务
---
```

需要类型、别名或首选项时，在 `SKILL.md` 同目录增加 `VOCABULARY.toml`，格式与 Workspace 文件一致。禁用或受保护的 Skill 不会向当前 Session 注入词汇。

Skill 支持动态激活：在输入框中精确输入 `/gpt`（无需回车），或从 `/` 菜单选中 GPT Skill 时，该 Skill 的 frontmatter 与同目录 `VOCABULARY.toml` 会立即进入当前 Session。重复激活会重新读取文件并安全替换旧快照；不会写入项目词表，也不会影响其他 Session。

### Agent 自带词汇

`AGENTS.md` 支持 frontmatter，也支持正文词汇表：

```markdown
---
vocabulary: 多模态生视频V5, 角色设定Pro
---

# 词汇表

- 批量跑图 — 批量执行生成任务
- 运镜 — 调整镜头运动
```

也可在 `AGENTS.md` 同目录放置 `VOCABULARY.toml`。词汇跟随实际加载的 Agent 指令范围进入 Controller，前端不自行猜测作用域。

## 自动学习

每个成功对话轮次会观察原始用户输入和最终 Assistant 文本。提取器只接受显式声明、引号包围或具备版本号/中英混排等明显特征的短词，普通句子不会写入；疑似 API Key、Token、路径和命令触发词会被拒绝。

学习数据写入用户私有的项目状态目录，不修改仓库文件。写入采用原子替换、跨 Session 锁、落盘前重读合并和事件去重；进程崩溃留下的锁会自动恢复。损坏或不可读的可选来源会产生可见警告，其余有效词汇仍可使用。

## 重建项目词表

提交 `/rebuild_vocabulary` 会扫描当前 Workspace 的常见代码、配置和文档文件，提取显式声明、引号词条及具有版本号或中英混排特征的项目术语，然后更新 `.WorkGround2/vocabulary.toml`。命令完成后，当前 Session 会立即重载新词表。

扫描生成内容位于 `WORKGROUND2:BEGIN/END GENERATED VOCABULARY` 标记之间。每次重建只替换该区段，标记外的手工词条和注释保持原样；写入采用原子替换，相同输入重复执行不会改写文件。扫描跳过 Git、依赖、构建产物、缓存、附件和 Session 数据目录，并限制单文件大小、文件数和生成词条数。TOML 损坏时命令显式失败且保留原文件。

## 对话上下文

词汇快照不会追加到会话的稳定 system prompt。仅当用户本次输入完整提到带说明的规范词时，Controller 才把最多 5 条定义作为临时上下文加入该次请求，避免破坏模型前缀缓存。

## 输入框行为

- 仅在光标位于文本末尾、没有选区且不处于中文输入法组合阶段时请求补全。
- `/`、`@`、`!` 菜单优先于词汇补全；菜单关闭后才显示灰色后缀。
- 查询带 90ms 防抖和 Tab ID，迟到的旧 Workspace 响应会被丢弃。
- 接受记录携带唯一凭据，可安全重试且不会重复增加排序权重。
- 补全服务不可用时输入框保持正常编辑，不阻塞发送；持久化失败会显示警告。
