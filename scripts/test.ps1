[CmdletBinding()]
param(
    [ValidateSet('Fast', 'Root', 'Fresh', 'Desktop', 'All', 'Live')]
    [string]$Mode = 'Fast'
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$root = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path

function Invoke-GoTest {
    param(
        [Parameter(Mandatory)][string]$Directory,
        [Parameter(Mandatory)][string[]]$Arguments
    )

    Push-Location $Directory
    try {
        & go @Arguments | Out-Host
        $code = $LASTEXITCODE
        return $code
    }
    finally {
        Pop-Location
    }
}

function Run-Root {
    param([string[]]$Arguments)

    return Invoke-GoTest -Directory $root -Arguments (@('test') + $Arguments + './...')
}

function Run-Desktop {
    return Invoke-GoTest -Directory (Join-Path $root 'desktop') -Arguments @('test', './...')
}

$code = switch ($Mode) {
    'Fast' { Run-Root @('-short') }
    'Root' { Run-Root @() }
    'Fresh' { Run-Root @('-count=1') }
    'Desktop' { Run-Desktop }
    'All' {
        $rootCode = Run-Root @()
        if ($rootCode -ne 0) { $rootCode } else { Run-Desktop }
    }
    'Live' {
        if ($env:WORKGROUND2_LIVE_TEST -ne '1') {
            [Console]::Error.WriteLine('Set WORKGROUND2_LIVE_TEST=1 before running live model tests.')
            2
        }
        else {
            Invoke-GoTest -Directory $root -Arguments @(
                'test',
                '-tags=live',
                '-count=1',
                './internal/acp',
                './internal/provider/openai'
            )
        }
    }
}

exit $code
