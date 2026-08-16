param(
    [Parameter(Mandatory = $true)]
    [ValidateSet('ask', 'notify', 'get', 'list', 'wait', 'cancel')]
    [string]$Action,
    [string]$Id,
    [string]$Json,
	[string]$AgentId = 'codex',
	[string]$ThreadId,
    [long]$AfterRevision = 0,
    [ValidateRange(1, 25)]
    [int]$TimeoutSec = 25
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

function Invoke-DecisionApi {
    param([string]$Method, [string]$Path, [string]$Body = '')
    $portPath = Join-Path (Get-WorkGround2StateDir) 'desktop-port'
    if (-not (Test-Path -LiteralPath $portPath)) {
        throw 'WorkGround2 Desktop is not running (desktop-port was not found).'
    }
    $port = (Get-Content -LiteralPath $portPath -Raw).Trim()
    if ($port -notmatch '^\d+$') {
        throw 'WorkGround2 Desktop wrote an invalid port file.'
    }
    $uri = "http://127.0.0.1:$port$Path"
    $args = @{ Method = $Method; Uri = $uri; TimeoutSec = 35 }
    if (-not [string]::IsNullOrWhiteSpace($Body)) {
        $args.ContentType = 'application/json; charset=utf-8'
        $args.Body = [Text.Encoding]::UTF8.GetBytes($Body)
    }
    return Invoke-RestMethod @args
}

function Set-DecisionKind {
    param([string]$Body, [string]$Kind)
    $request = $Body | ConvertFrom-Json
    $request | Add-Member -NotePropertyName 'kind' -NotePropertyValue $Kind -Force
    return $request | ConvertTo-Json -Depth 20 -Compress
}

switch ($Action) {
    'ask' {
        if ([string]::IsNullOrWhiteSpace($Json)) { throw '-Json is required for ask.' }
		$body = Set-DecisionKind -Body $Json -Kind 'ask'
		$result = Invoke-DecisionApi -Method Post -Path '/api/v1/decisions/create' -Body $body
	}
	'notify' {
		if ([string]::IsNullOrWhiteSpace($Json)) { throw '-Json is required for notify.' }
		$body = Set-DecisionKind -Body $Json -Kind 'notify'
		$result = Invoke-DecisionApi -Method Post -Path '/api/v1/decisions/create' -Body $body
    }
    'get' {
		if ([string]::IsNullOrWhiteSpace($Id) -or [string]::IsNullOrWhiteSpace($ThreadId)) { throw '-Id and -ThreadId are required for get.' }
        $encoded = [Uri]::EscapeDataString($Id)
		$agent = [Uri]::EscapeDataString($AgentId)
		$thread = [Uri]::EscapeDataString($ThreadId)
		$result = Invoke-DecisionApi -Method Get -Path "/api/v1/decisions/get?id=$encoded&agentId=$agent&threadId=$thread"
    }
    'list' {
		if ([string]::IsNullOrWhiteSpace($ThreadId)) { throw '-ThreadId is required for list.' }
		$agent = [Uri]::EscapeDataString($AgentId)
		$thread = [Uri]::EscapeDataString($ThreadId)
		$result = Invoke-DecisionApi -Method Get -Path "/api/v1/decisions/list?agentId=$agent&threadId=$thread"
    }
    'wait' {
		if ([string]::IsNullOrWhiteSpace($Id) -or [string]::IsNullOrWhiteSpace($ThreadId)) { throw '-Id and -ThreadId are required for wait.' }
        $encoded = [Uri]::EscapeDataString($Id)
		$agent = [Uri]::EscapeDataString($AgentId)
		$thread = [Uri]::EscapeDataString($ThreadId)
		$waitPath = "/api/v1/decisions/wait?id=$encoded&agentId=$agent&threadId=$thread&after=$AfterRevision&timeout=$TimeoutSec"
		$null = Invoke-DecisionApi -Method Get -Path $waitPath
		$result = Invoke-DecisionApi -Method Get -Path "/api/v1/decisions/get?id=$encoded&agentId=$agent&threadId=$thread"
    }
    'cancel' {
		if ([string]::IsNullOrWhiteSpace($Id) -or [string]::IsNullOrWhiteSpace($ThreadId)) { throw '-Id and -ThreadId are required for cancel.' }
		$body = @{ id = $Id; agentId = $AgentId; threadId = $ThreadId } | ConvertTo-Json -Compress
        $result = Invoke-DecisionApi -Method Post -Path '/api/v1/decisions/cancel' -Body $body
    }
}

$result | ConvertTo-Json -Depth 20 -Compress
