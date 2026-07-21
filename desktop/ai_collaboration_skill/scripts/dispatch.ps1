[CmdletBinding(DefaultParameterSetName = 'Dispatch')]
param(
    [Parameter(Mandatory)]
    [string]$Workspace,

    [Parameter(Mandatory, ParameterSetName = 'Dispatch')]
    [string]$SessionName,

    [Parameter(Mandatory, ParameterSetName = 'Dispatch')]
    [string]$PacketFile,

    [Parameter(Mandatory, ParameterSetName = 'CheckOnly')]
    [switch]$CheckOnly,

    [Parameter(Mandatory, ParameterSetName = 'PollOnly')]
    [switch]$PollOnly,

    [string]$CliPath,
    [string]$SessionID,

    [ValidateRange(1, 86400)]
    [int]$TimeoutSeconds = 900,

    [ValidateRange(1, 60)]
    [int]$PollSeconds = 3,

    [ValidateRange(1024, 30000)]
    [int]$MaxPacketBytes = 24576
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'
$utf8 = [System.Text.UTF8Encoding]::new($false)
[Console]::InputEncoding = $utf8
[Console]::OutputEncoding = $utf8
$OutputEncoding = $utf8

function Write-Outcome {
    param(
        [Parameter(Mandatory)][string]$Outcome,
        [Parameter(Mandatory)][int]$ExitCode,
        [hashtable]$Fields = @{}
    )

    $result = [ordered]@{ outcome = $Outcome }
    foreach ($key in $Fields.Keys) {
        $result[$key] = $Fields[$key]
    }
    [Console]::Out.WriteLine(($result | ConvertTo-Json -Depth 20 -Compress))
    exit $ExitCode
}

function Short-Text {
    param([AllowNull()][object]$Value, [int]$Limit = 800)
    $text = if ($null -eq $Value) { '' } else { [string]$Value }
    $text = $text.Trim()
    if ($text.Length -le $Limit) { return $text }
    return $text.Substring(0, $Limit) + '...'
}

function Resolve-Directory {
    param([string]$Path, [string]$Label)
    try {
        $resolved = (Resolve-Path -LiteralPath $Path -ErrorAction Stop).Path
    }
    catch {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = $Label
            message = "$Label does not exist: $Path"
        }
    }
    if (-not (Test-Path -LiteralPath $resolved -PathType Container)) {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = $Label
            message = "$Label is not a directory: $resolved"
        }
    }
    return $resolved
}

function Resolve-Cli {
    $running = @(Get-Process -Name 'WorkGround2' -ErrorAction SilentlyContinue |
        ForEach-Object { try { $_.Path } catch { $null } })
    $candidates = @(
        $CliPath,
        $env:WORKGROUND2_CLI,
        $(if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'Programs\WorkGround2\WorkGround2.exe' }),
        $(if ($env:LOCALAPPDATA) { Join-Path $env:LOCALAPPDATA 'WorkGround2\WorkGround2.exe' })
    ) + $running
    foreach ($candidate in $candidates) {
        if ([string]::IsNullOrWhiteSpace($candidate)) { continue }
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return (Resolve-Path -LiteralPath $candidate).Path
        }
    }
    $command = Get-Command workground2 -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($null -ne $command -and -not [string]::IsNullOrWhiteSpace($command.Source)) {
        return $command.Source
    }
    Write-Outcome -Outcome 'unreachable_cli' -ExitCode 1 -Fields @{
        message = 'WorkGround2 CLI was not found.'
    }
}

function Invoke-WorkGround2 {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $previousErrorPreference = $ErrorActionPreference
    try {
        $ErrorActionPreference = 'Continue'
        $lines = @(& $script:ResolvedCli @Arguments 2>&1 | ForEach-Object { $_.ToString() })
        $code = $LASTEXITCODE
    }
    finally {
        $ErrorActionPreference = $previousErrorPreference
    }
    if ($null -eq $code) { $code = 0 }
    return [pscustomobject]@{
        ExitCode = [int]$code
        Text = ($lines -join [Environment]::NewLine).Trim()
    }
}

function Has-Property {
    param([AllowNull()][object]$Object, [string]$Name)
    return $null -ne $Object -and $null -ne $Object.PSObject.Properties[$Name]
}

function Read-Status {
    param([Parameter(Mandatory)][string]$TargetSessionID)

    $call = Invoke-WorkGround2 -Arguments @('desktop', 'status', '--session', $TargetSessionID, '--json')
    if ($call.ExitCode -ne 0) {
        Write-Outcome -Outcome 'status_failed' -ExitCode 1 -Fields @{
            sessionId = $TargetSessionID
            message = 'desktop status --session <id> --json failed.'
            detail = Short-Text $call.Text
        }
    }
    try {
        $status = $call.Text | ConvertFrom-Json -ErrorAction Stop
    }
    catch {
        Write-Outcome -Outcome 'malformed_status' -ExitCode 3 -Fields @{
            sessionId = $TargetSessionID
            message = 'desktop status --session <id> --json returned malformed JSON.'
            detail = Short-Text $call.Text
        }
    }
    if ($null -eq $status -or $status -is [array] -or $status -is [string]) {
        Write-Outcome -Outcome 'malformed_status' -ExitCode 3 -Fields @{
            sessionId = $TargetSessionID
            message = 'desktop status --session <id> --json did not return an object.'
        }
    }
    return $status
}

function Validate-Interaction {
    param([object]$Interaction)
    if (-not (Has-Property $Interaction 'id') -or
        [string]::IsNullOrWhiteSpace([string]$Interaction.id) -or
        -not (Has-Property $Interaction 'kind')) {
        return 'pendingInteraction is missing id or kind.'
    }
    $kind = [string]$Interaction.kind
    if ($kind -eq 'ask') {
        if (-not (Has-Property $Interaction 'questions') -or $null -eq $Interaction.questions) {
            return 'Ask interaction is missing questions.'
        }
        $questions = @($Interaction.questions)
        if ($questions.Count -eq 0) { return 'Ask interaction has no questions.' }
        foreach ($question in $questions) {
            if (-not (Has-Property $question 'id') -or
                [string]::IsNullOrWhiteSpace([string]$question.id) -or
                -not (Has-Property $question 'question') -or
                [string]::IsNullOrWhiteSpace([string]$question.question) -or
                -not (Has-Property $question 'options') -or
                $null -eq $question.options -or
                -not (Has-Property $question 'multiSelect') -or
                $question.multiSelect -isnot [bool]) {
                return 'Ask interaction contains a malformed question.'
            }
            $options = @($question.options)
            if ($options.Count -eq 0) { return 'Ask interaction contains a question with no options.' }
            foreach ($option in $options) {
                if (-not (Has-Property $option 'label') -or
                    [string]::IsNullOrWhiteSpace([string]$option.label) -or
                    -not (Has-Property $option 'description')) {
                    return 'Ask interaction contains a malformed option.'
                }
            }
        }
        return $null
    }
    if ($kind -eq 'approval') {
        foreach ($field in @('tool', 'subject', 'reason')) {
            if (-not (Has-Property $Interaction $field)) {
                return "Approval interaction is missing $field."
            }
        }
        return $null
    }
    return "Unknown interaction kind: $kind"
}

try {
$resolvedWorkspace = Resolve-Directory -Path $Workspace -Label 'Workspace'
$script:ResolvedCli = Resolve-Cli
$reachability = Invoke-WorkGround2 -Arguments @('desktop', 'workspaces')
if ($reachability.ExitCode -ne 0) {
    Write-Outcome -Outcome 'unreachable_cli' -ExitCode 1 -Fields @{
        cli = $script:ResolvedCli
        message = 'desktop workspaces failed.'
        detail = Short-Text $reachability.Text
    }
}

if ($CheckOnly) {
    Write-Outcome -Outcome 'reachable' -ExitCode 0 -Fields @{
        cli = $script:ResolvedCli
        workspace = $resolvedWorkspace
    }
}

if ($PSCmdlet.ParameterSetName -eq 'Dispatch') {
    $session = $SessionName.Trim()
    if ([string]::IsNullOrWhiteSpace($session) -or $session.Length -gt 120 -or $session -match '[\r\n\t]') {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'SessionName'
            message = 'SessionName must be non-empty, at most 120 characters, and contain no control whitespace.'
        }
    }
    try {
        $resolvedPacket = (Resolve-Path -LiteralPath $PacketFile -ErrorAction Stop).Path
    }
    catch {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'PacketFile'
            message = "PacketFile does not exist: $PacketFile"
        }
    }
    if (-not (Test-Path -LiteralPath $resolvedPacket -PathType Leaf)) {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'PacketFile'
            message = "PacketFile is not a file: $resolvedPacket"
        }
    }
    $packetBytes = [System.IO.File]::ReadAllBytes($resolvedPacket)
    if ($packetBytes.Length -eq 0 -or $packetBytes.Length -gt $MaxPacketBytes) {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'PacketFile'
            message = "PacketFile must contain 1-$MaxPacketBytes UTF-8 bytes."
            bytes = $packetBytes.Length
        }
    }
    try {
        $strictUtf8 = [System.Text.UTF8Encoding]::new($false, $true)
        $packet = $strictUtf8.GetString($packetBytes).TrimStart([char]0xFEFF).Trim()
    }
    catch {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'PacketFile'
            message = 'PacketFile is not valid UTF-8.'
        }
    }
    if ([string]::IsNullOrWhiteSpace($packet)) {
        Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
            argument = 'PacketFile'
            message = 'PacketFile contains no packet text.'
        }
    }

    $dispatch = Invoke-WorkGround2 -Arguments @(
        'desktop', 'new',
        '--workspace', $resolvedWorkspace,
        '--session-name', $session,
        '--yolo', '--no-wait',
        $packet
    )
    if ($dispatch.ExitCode -ne 0) {
        Write-Outcome -Outcome 'dispatch_failed' -ExitCode 1 -Fields @{
            message = 'desktop new failed; it was not retried.'
            detail = Short-Text $dispatch.Text
        }
    }
    $sessionMatches = [regex]::Matches($dispatch.Text, '(?m)^SessionID:\s*(.+?)\s*$')
    if ($sessionMatches.Count -eq 0) {
        Write-Outcome -Outcome 'dispatch_failed' -ExitCode 1 -Fields @{
            message = 'desktop new succeeded but did not return a SessionID; the session was preserved and dispatch was not retried.'
            detail = Short-Text $dispatch.Text
        }
    }
    $sessionIDs = @($sessionMatches | ForEach-Object { $_.Groups[1].Value.Trim() } | Select-Object -Unique)
    if ($sessionIDs.Count -ne 1 -or [string]::IsNullOrWhiteSpace($sessionIDs[0])) {
        Write-Outcome -Outcome 'dispatch_failed' -ExitCode 1 -Fields @{
            message = 'desktop new returned ambiguous SessionIDs; the sessions were preserved and dispatch was not retried.'
            detail = Short-Text $dispatch.Text
        }
    }
    $SessionID = $sessionIDs[0]
}
elseif ([string]::IsNullOrWhiteSpace($SessionID)) {
    Write-Outcome -Outcome 'invalid_argument' -ExitCode 2 -Fields @{
        argument = 'SessionID'
        message = 'PollOnly requires the SessionID returned by desktop new.'
    }
}

$deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
$lastStatus = $null
do {
    $status = Read-Status -TargetSessionID $SessionID
    $lastStatus = $status

    $hasForegroundActive = Has-Property $status 'foregroundActive'
    $hasRunning = Has-Property $status 'running'
    if (($hasForegroundActive -and $status.foregroundActive -isnot [bool]) -or
        (-not $hasForegroundActive -and (-not $hasRunning -or $status.running -isnot [bool])) -or
        ($hasRunning -and $status.running -isnot [bool]) -or
        -not (Has-Property $status 'pendingPrompt') -or $status.pendingPrompt -isnot [bool]) {
        Write-Outcome -Outcome 'empty_session' -ExitCode 3 -Fields @{
            message = 'Status requires boolean foregroundActive (or legacy running) and pendingPrompt fields.'
            path = if (Has-Property $status 'path') { [string]$status.path } else { '' }
        }
    }

    $foregroundActive = if ($hasForegroundActive) { [bool]$status.foregroundActive } else { [bool]$status.running }
    $running = if ($hasRunning) { [bool]$status.running } else { $foregroundActive }
    $backgroundOnly = if ((Has-Property $status 'backgroundOnly') -and $status.backgroundOnly -is [bool]) {
        [bool]$status.backgroundOnly
    }
    else {
        $false
    }

    $path = if (Has-Property $status 'path') { [string]$status.path } else { '' }
    $hasInteraction = Has-Property $status 'pendingInteraction'
    if ($hasInteraction -and $null -ne $status.pendingInteraction) {
        $interactionError = Validate-Interaction -Interaction $status.pendingInteraction
        if ($null -ne $interactionError) {
            Write-Outcome -Outcome 'malformed_interaction' -ExitCode 3 -Fields @{
                message = $interactionError
                path = $path
                pendingInteraction = $status.pendingInteraction
            }
        }
        Write-Outcome -Outcome 'interaction_required' -ExitCode 0 -Fields @{
            sessionId = $SessionID
            path = $path
            foregroundActive = $foregroundActive
            running = $running
            backgroundOnly = $backgroundOnly
            pendingPrompt = $status.pendingPrompt
            pendingInteraction = $status.pendingInteraction
        }
    }

    if ($status.pendingPrompt) {
        Write-Outcome -Outcome 'interaction_missing' -ExitCode 3 -Fields @{
            sessionId = $SessionID
            message = 'pendingPrompt is true but pendingInteraction is absent.'
            path = $path
            foregroundActive = $foregroundActive
            running = $running
            backgroundOnly = $backgroundOnly
            pendingPrompt = $status.pendingPrompt
        }
    }

    if (-not $foregroundActive -and -not $status.pendingPrompt) {
        if ([string]::IsNullOrWhiteSpace($path)) {
            Write-Outcome -Outcome 'empty_session' -ExitCode 3 -Fields @{
                message = 'The active session completed without a session path.'
            }
        }
        Write-Outcome -Outcome 'completed' -ExitCode 0 -Fields @{
            sessionId = $SessionID
            path = $path
            foregroundActive = $foregroundActive
            running = $running
            backgroundOnly = $backgroundOnly
            pendingPrompt = $status.pendingPrompt
            mode = if (Has-Property $status 'mode') { [string]$status.mode } else { '' }
            report = if (Has-Property $status 'report') { $status.report } else { $null }
        }
    }

    if ([DateTime]::UtcNow -lt $deadline) {
        Start-Sleep -Seconds $PollSeconds
    }
} while ([DateTime]::UtcNow -lt $deadline)

$timeoutPath = if ($null -ne $lastStatus -and (Has-Property $lastStatus 'path')) { [string]$lastStatus.path } else { '' }
$timeoutHasForeground = $null -ne $lastStatus -and (Has-Property $lastStatus 'foregroundActive') -and $lastStatus.foregroundActive -is [bool]
$timeoutHasRunning = $null -ne $lastStatus -and (Has-Property $lastStatus 'running') -and $lastStatus.running -is [bool]
$timeoutForeground = if ($timeoutHasForeground) { [bool]$lastStatus.foregroundActive } elseif ($timeoutHasRunning) { [bool]$lastStatus.running } else { $null }
Write-Outcome -Outcome 'timeout' -ExitCode 4 -Fields @{
    sessionId = $SessionID
    message = 'Polling timed out; the session was preserved and desktop new was not retried.'
    path = $timeoutPath
    foregroundActive = $timeoutForeground
    running = if ($timeoutHasRunning) { [bool]$lastStatus.running } else { $timeoutForeground }
    backgroundOnly = if ($null -ne $lastStatus -and (Has-Property $lastStatus 'backgroundOnly') -and $lastStatus.backgroundOnly -is [bool]) { [bool]$lastStatus.backgroundOnly } else { $false }
    pendingPrompt = if ($null -ne $lastStatus -and (Has-Property $lastStatus 'pendingPrompt')) { $lastStatus.pendingPrompt } else { $null }
}
}
catch {
    Write-Outcome -Outcome 'terminal_error' -ExitCode 1 -Fields @{
        message = 'dispatch.ps1 failed unexpectedly.'
        detail = Short-Text $_.Exception.Message
    }
}
