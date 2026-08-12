$ErrorActionPreference = "Stop"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOTOOLCHAIN = "local"

$wails = Get-Command wails -ErrorAction SilentlyContinue
if (-not $wails) {
    $candidate = Join-Path (go env GOPATH) "bin\wails.exe"
    if (Test-Path -LiteralPath $candidate) {
        $wails = Get-Item -LiteralPath $candidate
    } else {
        throw "Wails CLI 未安装。请先按 docs/development.md 安装依赖。"
    }
}

& $wails.Source dev
if ($LASTEXITCODE -ne 0) {
    throw "Wails dev failed with exit code $LASTEXITCODE."
}
