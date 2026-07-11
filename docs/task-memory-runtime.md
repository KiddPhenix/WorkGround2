# TaskMemory 运行时链路

TaskMemory 是 Controller 拥有的、按 Session 隔离的事实型任务简报，用于用户切回会话时快速恢复上下文。它不读取或猜测 UI 文案。

## 数据模型

```text
TaskMemory
├─ goal / goalSource
├─ current / currentSource
├─ nextStep / nextStepSource
├─ sessionKey
├─ revision
└─ updatedAt
```

每次有效变化都递增 `revision`；相同内容重复写入不递增。`sessionKey` 为 revision 划分命名空间：同一 Tab 切换 Session/Branch 时，即使新 Session 的 revision 更小也会被接受。前端保存每个 Session 命名空间的最高 revision，迟到、重复和旧事件不会覆盖新状态。空内容以新 revision 形成 tombstone，旧事件不能使已隐藏的简报重新出现。

## 可信来源与显示条件

| 字段 | 更新条件 | Source | 显示文案来源 |
|---|---|---|---|
| goal | 用户设置 Goal | `explicit_goal` | Goal 原文 |
| goal | Session 尚无 Goal/任务时提交首个正常任务 | `user_prompt` | 用户原文，压平空白并最多保留 240 字符 |
| current | Turn 启动或用户回答后继续 | `runtime` | 类型化状态“执行中” |
| current | Ask 阻塞 | `ask` | 类型化状态“等待回答” |
| current | Approval 阻塞 | `approval` | 类型化状态“等待批准” |
| current | Turn 失败 | `turn_error` | 类型化状态“执行失败” |
| nextStep | Ask 阻塞 | `ask` | 第一条真实问题 Prompt |
| nextStep | Approval 阻塞 | `approval` | Approval Subject |
| nextStep | AutoResearch 提供明确下一动作 | `autoresearch` | `NextRequiredAction` |
| nextStep | 存在未完成 Todo | `todo` | 第一条未完成 Todo 原文 |

成功 Turn 结束后 `current` 清空；没有明确下一动作时 `nextStep` 清空。三个字段全空时 UI 不渲染。字段部分存在时，只渲染存在的段。`user_prompt` 来源在 UI 中标记为“任务”，只有 `explicit_goal` 标记为“目标”。

## 持久化和恢复

- Sidecar：`<session>.task-memory.json`。
- 写入：`fileutil.AtomicWriteFile`，同目录临时文件后原子替换。
- 恢复：Controller 创建、Resume、SetSessionPath、SwitchBranch、磁盘 Session 接管时加载。
- 新建/清空 Session：写入新的空 revision，防止旧状态泄漏。
- Fork/Branch/冲突恢复分支：继承当前简报并写入新 Session sidecar。
- Sidecar 缺失：按空状态启动。
- Sidecar 损坏：记录 warning，按空状态恢复，不阻塞会话。

## 传输和 UI

Controller 通过独立 `TaskMemoryStatus` 读端口暴露快照。Desktop 将快照放入 Tab Meta，并附在正常事件包上，不插入事件流次序；显式 Goal 变更会发送 `task_memory_updated`。前端 `useController` 按 `tabId` 写入 Zustand Memory Store，并以 revision/tombstone 处理重复、乱序和迟到数据。

TaskMemory 不修改 system prompt、tool schema 或 cache-stable prefix。
