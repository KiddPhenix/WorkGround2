# WorkGround2 Desktop CLI

The CLI path is resolved by `scripts/dispatch.ps1` and injected into the runtime AGENTS.md block. Use `$wg` as the resolved executable.

## Session commands

Check Desktop reachability:

```powershell
& $wg desktop workspaces
```

Create a new session and dispatch asynchronously. The name is display-only; every `desktop new` creates a fresh SessionID. A starting controller accepts the prompt into a durable queue, so this command returns without waiting for model or controller startup:

```powershell
$dispatch = @(& $wg desktop new --workspace '<repo-root>' --session-name '<display-name>' --yolo --no-wait '<packet>' 2>&1)
$sessionID = ($dispatch | Select-String '^SessionID:\s*(.+)$' | Select-Object -First 1).Matches.Groups[1].Value.Trim()
if (-not $sessionID) { throw 'desktop new did not return SessionID' }
```

The bundled dispatch script wraps the same command and returns one immediate structured acknowledgement:

```powershell
& '<skill-root>\scripts\dispatch.ps1' -Workspace '<repo-root>' -SessionName '<display-name>' -PacketFile '<utf8-packet>' -CliPath $wg
# {"outcome":"dispatched","sessionId":"..."}
```

Read one bounded snapshot for that exact session. Repeat only when the outcome is `running`; `starting=true` means the prompt is accepted and waiting for its Controller:

```powershell
& '<skill-root>\scripts\dispatch.ps1' -Workspace '<repo-root>' -PollOnly -SessionID $sessionID -CliPath $wg
```

Continue an existing session without creating another one:

```powershell
& $wg desktop submit --session $sessionID --yolo --no-wait '<follow-up>'
```

Bring Desktop to the foreground when visual inspection is needed:

```powershell
& $wg desktop focus
```

## Structured asks

Use the interaction ID, question IDs, and option labels exactly as returned by status.

One question:

```powershell
& $wg desktop answer --session $sessionID --id '<ask-id>' --answer '<question-id>=<exact-option-label>'
```

Multiple questions use one `--answer` per question:

```powershell
& $wg desktop answer --session $sessionID --id '<ask-id>' --answer 'scope=Minimal' --answer 'tests=Run all'
```

Multi-select repeats `--answer` with the same question ID:

```powershell
& $wg desktop answer --session $sessionID --id '<ask-id>' --answer 'targets=Client' --answer 'targets=Tests'
```

After a successful answer, resume polling with the preserved SessionID:

```powershell
& '<skill-root>\scripts\dispatch.ps1' -Workspace '<repo-root>' -PollOnly -SessionID $sessionID
```

## Approvals

Allow only after the returned tool, subject, reason, and current authorization are safe:

```powershell
& $wg desktop approve --session $sessionID --id '<approval-id>' --allow
```

Deny an unsafe or out-of-scope request:

```powershell
& $wg desktop approve --session $sessionID --id '<approval-id>' --deny
```

After a successful decision, resume polling with the same command and SessionID.

## Recovery

If `answer` or `approve` reports an invalid or expired ID, immediately run `desktop status --session <id> --json` with the same SessionID. Use a newly returned interaction ID only after re-evaluating the new interaction. If no interaction remains, follow the current running and pendingPrompt fields. Never replay the stale command.

If `pendingPrompt=true` but `pendingInteraction` is absent, read status again once. If it persists, run `desktop focus`, preserve the session, and expose the missing interaction. Do not guess an ID or submit another prompt over the blocked turn.

After a caller timeout, run a fresh `PollOnly` snapshot or inspect `desktop status --session <id> --json` and focus Desktop if needed. Keep the existing SessionID; do not issue another `desktop new` while its state is ambiguous. `startup_failed` and `submit_failed` retain the same ID and queued prompt for retry.

For an empty or unchanged session, verify the returned session path and repository diff. If the session exists and the original prompt was never submitted, use `desktop submit --session ...`; otherwise preserve evidence and report failed delegation.
