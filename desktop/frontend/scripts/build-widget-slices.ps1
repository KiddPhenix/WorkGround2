param(
    [Parameter(Mandatory = $true)]
    [string]$InputPath,

    [Parameter(Mandatory = $true)]
    [string]$OutputDir,

    [ValidatePattern('^[a-z0-9][a-z0-9-]*$')]
    [string]$BaseName = 'pager-shell',

    [int]$CropX = 16,
    [int]$CropY = 96,
    [int]$CropWidth = 2132,
    [int]$CropHeight = 512,
    [int]$Left = 576,
    [int]$Top = 112,
    [int]$Right = 800,
    [int]$Bottom = 160
)

$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Drawing

$inputFull = [System.IO.Path]::GetFullPath($InputPath)
$outputFull = [System.IO.Path]::GetFullPath($OutputDir)
if (-not (Test-Path -LiteralPath $inputFull)) {
    throw "Input image does not exist: $inputFull"
}

if ($CropWidth -le ($Left + $Right) -or $CropHeight -le ($Top + $Bottom)) {
    throw 'Crop size must be larger than the combined cap insets.'
}

New-Item -ItemType Directory -Force -Path $outputFull | Out-Null
$tileDirName = "$BaseName.9"
$tileDir = Join-Path $outputFull $tileDirName
New-Item -ItemType Directory -Force -Path $tileDir | Out-Null

$source = [System.Drawing.Bitmap]::FromFile($inputFull)
try {
    $cropRect = [System.Drawing.Rectangle]::new($CropX, $CropY, $CropWidth, $CropHeight)
    if ($cropRect.Right -gt $source.Width -or $cropRect.Bottom -gt $source.Height) {
        throw "Crop rectangle $cropRect exceeds source size $($source.Width)x$($source.Height)."
    }

    $shell = $source.Clone($cropRect, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
    try {
        $shellFile = "$BaseName.png"
        $shellPath = Join-Path $outputFull $shellFile
        $sameShell = [System.StringComparer]::OrdinalIgnoreCase.Equals($inputFull, $shellPath)
        if ($sameShell) {
            if ($CropX -ne 0 -or $CropY -ne 0 -or $CropWidth -ne $source.Width -or $CropHeight -ne $source.Height) {
                throw 'Input and output shell paths match, but the requested crop would overwrite the open source image.'
            }
        }
        else {
            $shell.Save($shellPath, [System.Drawing.Imaging.ImageFormat]::Png)
        }

        $centerWidth = $CropWidth - $Left - $Right
        $centerHeight = $CropHeight - $Top - $Bottom
        $tiles = [ordered]@{
            'top-left'     = [System.Drawing.Rectangle]::new(0, 0, $Left, $Top)
            'top'          = [System.Drawing.Rectangle]::new($Left, 0, $centerWidth, $Top)
            'top-right'    = [System.Drawing.Rectangle]::new($Left + $centerWidth, 0, $Right, $Top)
            'left'         = [System.Drawing.Rectangle]::new(0, $Top, $Left, $centerHeight)
            'center'       = [System.Drawing.Rectangle]::new($Left, $Top, $centerWidth, $centerHeight)
            'right'        = [System.Drawing.Rectangle]::new($Left + $centerWidth, $Top, $Right, $centerHeight)
            'bottom-left'  = [System.Drawing.Rectangle]::new(0, $Top + $centerHeight, $Left, $Bottom)
            'bottom'       = [System.Drawing.Rectangle]::new($Left, $Top + $centerHeight, $centerWidth, $Bottom)
            'bottom-right' = [System.Drawing.Rectangle]::new($Left + $centerWidth, $Top + $centerHeight, $Right, $Bottom)
        }

        foreach ($entry in $tiles.GetEnumerator()) {
            $tile = $shell.Clone($entry.Value, [System.Drawing.Imaging.PixelFormat]::Format32bppArgb)
            try {
                $tile.Save((Join-Path $tileDir "$($entry.Key).png"), [System.Drawing.Imaging.ImageFormat]::Png)
            }
            finally {
                $tile.Dispose()
            }
        }

        $manifest = [ordered]@{
            version = 1
            source = $shellFile
            sourceSize = [ordered]@{ width = $CropWidth; height = $CropHeight }
            capInsets = [ordered]@{ left = $Left; top = $Top; right = $Right; bottom = $Bottom }
            stretchRect = [ordered]@{
                x = $Left
                y = $Top
                width = $centerWidth
                height = $centerHeight
            }
            tiles = [ordered]@{}
        }
        foreach ($entry in $tiles.GetEnumerator()) {
            $rect = $entry.Value
            $manifest.tiles[$entry.Key] = [ordered]@{
                file = "$tileDirName/$($entry.Key).png"
                x = $rect.X
                y = $rect.Y
                width = $rect.Width
                height = $rect.Height
            }
        }
        $manifest | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath (Join-Path $outputFull "$BaseName.9.json") -Encoding utf8
    }
    finally {
        $shell.Dispose()
    }
}
finally {
    $source.Dispose()
}

Write-Output "Built widget shell and nine-slice assets in $outputFull"
