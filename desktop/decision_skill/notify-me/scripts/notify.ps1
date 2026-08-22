param(
    [Parameter(Mandatory = $true)]
    [string]$ThreadId,
    [Parameter(Mandatory = $true)]
    [string]$IdempotencyKey,
    [Parameter(Mandatory = $true)]
    [string]$Title,
    [Parameter(Mandatory = $true)]
    [string]$TaskSummary,
    [string]$WhyNow = '',
    [string]$WorkspaceRoot = '',
    [string]$AgentId = 'codex'
)

$ErrorActionPreference = 'Stop'

function Get-WorkGround2StateDir {
    if (-not [string]::IsNullOrWhiteSpace($env:WorkGround2_STATE_HOME)) {
        return $env:WorkGround2_STATE_HOME
    }
    if (-not [string]::IsNullOrWhiteSpace($env:WorkGround2_HOME)) {
        return $env:WorkGround2_HOME
    }
    if (-not [string]::IsNullOrWhiteSpace($env:APPDATA)) {
        return (Join-Path $env:APPDATA 'WorkGround2')
    }
    throw 'Cannot resolve the WorkGround2 state directory.'
}

$portPath = Join-Path (Get-WorkGround2StateDir) 'desktop-port'
if (-not (Test-Path -LiteralPath $portPath)) {
    throw 'WorkGround2 Desktop is not running (desktop-port was not found).'
}
$port = (Get-Content -LiteralPath $portPath -Raw).Trim()
if ($port -notmatch '^\d+$') {
    throw 'WorkGround2 Desktop wrote an invalid port file.'
}

$request = @{
    idempotencyKey = $IdempotencyKey
    kind = 'notify'
    agentId = $AgentId
    threadId = $ThreadId
    workspaceRoot = $WorkspaceRoot
    title = $Title
    taskSummary = $TaskSummary
    whyNow = $WhyNow
} | ConvertTo-Json -Depth 10 -Compress

$uri = "http://127.0.0.1:$port/api/v1/decisions/create"
$result = Invoke-RestMethod -Method Post -Uri $uri -ContentType 'application/json; charset=utf-8' -Body ([Text.Encoding]::UTF8.GetBytes($request)) -TimeoutSec 35
$result | ConvertTo-Json -Depth 20 -Compress
