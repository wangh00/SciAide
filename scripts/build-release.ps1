$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function Invoke-Native {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Command,
        [Parameter(ValueFromRemainingArguments = $true)]
        [string[]]$Arguments
    )

    & $Command @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Native command failed with exit code ${LASTEXITCODE}: $Command $($Arguments -join ' ')"
    }
}

$root = Split-Path -Parent $PSScriptRoot
$output = Join-Path $root "build\bin\SciAide.exe"
$previousGOOS = $env:GOOS
$previousGOARCH = $env:GOARCH
$previousCGOEnabled = $env:CGO_ENABLED

if (-not (Get-Command wails -ErrorAction SilentlyContinue)) {
    throw "Wails CLI was not found. Install the version matching go.mod first."
}

Push-Location $root
try {
    # This machine may have a 32-bit host Go toolchain. Pin both Wails and Go
    # explicitly so a release can never silently become a windows/386 binary.
    $env:GOOS = "windows"
    $env:GOARCH = "amd64"
    $env:CGO_ENABLED = "0"
    Invoke-Native wails build -platform windows/amd64 -clean
} finally {
    $env:GOOS = $previousGOOS
    $env:GOARCH = $previousGOARCH
    $env:CGO_ENABLED = $previousCGOEnabled
    Pop-Location
}

if (-not (Test-Path -LiteralPath $output -PathType Leaf)) {
    throw "Build completed without the expected artifact: $output"
}

$stream = [System.IO.File]::OpenRead($output)
$reader = [System.IO.BinaryReader]::new($stream)
try {
    if ($reader.ReadUInt16() -ne 0x5A4D) {
        throw "Artifact is not a valid Windows PE file: $output"
    }
    $stream.Position = 0x3C
    $peOffset = $reader.ReadUInt32()
    $stream.Position = $peOffset
    if ($reader.ReadUInt32() -ne 0x00004550) {
        throw "Artifact has no valid PE signature: $output"
    }
    $machine = $reader.ReadUInt16()
} finally {
    $reader.Dispose()
    $stream.Dispose()
}

if ($machine -ne 0x8664) {
    throw ("Wrong artifact architecture: PE Machine=0x{0:X4}; expected windows/amd64 (0x8664)" -f $machine)
}

$item = Get-Item -LiteralPath $output
$hash = (Get-FileHash -LiteralPath $output -Algorithm SHA256).Hash
$hashOutput = "${output}.sha256"
[System.IO.File]::WriteAllText($hashOutput, "$($hash.ToLowerInvariant())  $($item.Name)`n", [System.Text.UTF8Encoding]::new($false))
Write-Host "Release build passed."
Write-Host "Path: $($item.FullName)"
Write-Host "Bytes: $($item.Length)"
Write-Host ("PE Machine: 0x{0:X4}" -f $machine)
Write-Host "SHA256: $hash"
Write-Host "Checksum: $hashOutput"
