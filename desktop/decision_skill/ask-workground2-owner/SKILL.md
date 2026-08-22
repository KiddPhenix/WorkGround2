---
name: ask-workground2-owner
description: Ask the human owner a durable, standalone decision or send a durable notification through WorkGround2. Use for consequential choices that need a Desktop or WeChat answer, or when the user explicitly requests a completion or attention notification.
---

# Ask WorkGround2 Owner

Use WorkGround2's process-wide DecisionBroker for human decisions and durable notifications that must survive agent, workspace, or application restarts. The first valid decision answer from Desktop or a configured IM channel wins; notifications require no reply and never occupy the decision queue.

## Requirements

- WorkGround2 Desktop must be running.
- Run `scripts/decision.ps1` from this skill directory.
- Do not use this channel for routine progress updates unless the user explicitly requested a notification.
- Do not ask questions the agent can safely resolve itself.

## Workflow

1. Write a standalone, human-readable question. Assume the owner sees it without the current chat.
2. Include the task context, why the choice is needed now, concrete options, and the impact of every option.
3. Create it with a stable idempotency key. Reuse that key when retrying the same logical question.
4. Keep the returned decision ID. The decision has no technical expiry by default.
5. Poll in bounded long-poll calls (25 seconds or less). It is safe to repeat after timeouts, restarts, or network failures.
6. Continue only when status is `decided` or `applied`. If status is `cancelled`, `orphaned`, or `apply_failed`, report it explicitly.
7. Before applying a late answer, revalidate the current workspace state. Ask again if the choice became stale.

## Create

Build JSON and pass it without embedding shell-sensitive quoting in the command:

```powershell
$request = @{
  idempotencyKey = "codex:<thread-or-task-id>:hero-image-strategy"
  agentId = "codex"
  threadId = "<current task id>"
  workspaceRoot = "<absolute workspace path>"
  title = "确定主角图策略"
  taskSummary = "正在为夏季活动页生成一组保持同一主角的视觉素材。"
  whyNow = "下一步会批量生成其余画面；这个选择决定角色一致性和返工成本。"
  questions = @(
    @{
      id = "hero-image"
      header = "主角图"
      prompt = "这批素材要生成新的主角图，还是复用已经确认的主角图？"
      multiSelect = $false
      options = @(
        @{ label = "复用主角图"; impact = "角色一致性最高，构图变化空间较小。" }
        @{ label = "生成新图"; impact = "创意空间更大，需要再次确认角色一致性。" }
      )
    }
  )
  noAnswerPolicy = "任务保持暂停，不会自动替你选择。"
} | ConvertTo-Json -Depth 10 -Compress

& "<skill-dir>\scripts\decision.ps1" -Action ask -Json $request
```

The output is JSON. Persist the returned `id` in the task state.

## Notify without asking

Use `notify` for a standalone completion or attention message. It is durably retried through the configured channel, requires no options or answer, and does not block later questions:

```powershell
$request = @{
  idempotencyKey = "codex:<thread-or-task-id>:build-complete"
  agentId = "codex"
  threadId = "<current task id>"
  workspaceRoot = "<absolute workspace path>"
  title = "WorkGround2 修改已完成"
  taskSummary = "微信决策消息已隐藏内部协议、修复选项换行，并合并了回答确认。"
  whyNow = "新构建已通过检查，可以开始验收。"
} | ConvertTo-Json -Depth 10 -Compress

& "<skill-dir>\scripts\decision.ps1" -Action notify -Json $request
```

The script forces `kind = notify`; callers do not need to add empty questions or a no-answer policy.

## Observe and wait

Use a bounded wait so the agent remains responsive:

```powershell
& "<skill-dir>\scripts\decision.ps1" -Action wait -Id "D-..." -AgentId "codex" -ThreadId "<current task id>" -TimeoutSec 25
```

Repeat as needed. A timeout returns the current durable decision; it does not cancel it. Use `get` for an immediate snapshot and `list` to recover an unknown ID.

## Cancel

Cancel only when the originating task no longer needs the answer:

```powershell
& "<skill-dir>\scripts\decision.ps1" -Action cancel -Id "D-..." -AgentId "codex" -ThreadId "<current task id>"
```

Cancellation is explicit and releases the global queue for the next question.

## Human readability rules

- Never send only a raw prompt such as “生成新的还是复用主角图？”
- `taskSummary` must make sense to someone in a meeting, commuting, or seeing only the WeChat message.
- `whyNow` explains what is blocked and what happens after the answer.
- Every option needs a concrete impact. Avoid generic text such as “按这个方向继续”.
- State the no-answer behavior. Default to pausing rather than silently choosing.
- Keep one decision focused. If several choices are coupled, include them as ordered questions in the same decision.
- Notifications must state what completed or needs attention and the useful next step. They must not look like a question or request a reply.
