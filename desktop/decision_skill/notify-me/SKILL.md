---
name: notify-me
description: Notify the human owner once through WorkGround2 after the current task reaches a terminal outcome. Use whenever the user explicitly asks to be notified, alerted, pinged, or messaged when work finishes, including successful completion, failure, or cancellation.
---

# Notify Me

Use WorkGround2's durable owner channel for one standalone, no-reply message after the requested task finishes. This skill is for explicit completion notifications, not routine progress updates or questions.

## Workflow

1. Record that the user requested a completion notification.
2. Finish the task and its required validation first. Do not notify while implementation, testing, or a required decision remains pending.
3. Build a human-readable message for someone who cannot see the current conversation:
   - `Title`: the terminal result in a few words.
   - `TaskSummary`: what task ended and whether it succeeded, failed, or was cancelled.
   - `WhyNow`: the useful next step, such as what can now be reviewed or retried.
4. Run `scripts/notify.ps1` exactly once with a stable idempotency key. Reuse the same key when retrying the same logical notification.
5. If delivery creation fails, report the failure explicitly in the final answer. Never claim the owner was notified unless the script returns a decision ID.

## Send

```powershell
& "<skill-dir>\scripts\notify.ps1" `
  -ThreadId "<current-task-id>" `
  -IdempotencyKey "codex:<current-task-id>:complete" `
  -WorkspaceRoot "<absolute-workspace-path>" `
  -Title "WorkGround2 修改已完成" `
  -TaskSummary "notify-me 已实现并通过相关测试。" `
  -WhyNow "可以开始验收。"
```

The script returns the durable notification as JSON. WorkGround2 handles configured Desktop/WeChat routing and retries. The message requires no answer and does not occupy the decision queue.

## Message rules

- Assume the owner is in a meeting, commuting, or seeing only the notification.
- State the concrete outcome. Avoid vague text such as “好了” or “请看一下”.
- Include failures and cancellations; terminal does not imply success.
- Keep secrets, raw stack traces, local tokens, and internal protocol IDs out of the human-facing fields.
- Send at most one notification for one terminal task outcome.
