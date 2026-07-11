[CmdletBinding()]
param(
    [string]$AddonsRoot = (Join-Path (Resolve-Path (Join-Path $PSScriptRoot '..\..')).Path 'wg2addons')
)

$ErrorActionPreference = 'Stop'

$repo = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
$AddonsRoot = (Resolve-Path $AddonsRoot).Path
$script = Join-Path $AddonsRoot 'scripts\build-addons.ps1'
if (-not (Test-Path -LiteralPath $script -PathType Leaf)) {
    throw "AddOn build script not found: $script"
}

& $script -WorkGround2Root $repo
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}
