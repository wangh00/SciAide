# Focused, reproducible source-scope handoff regression.
# Uses only local scripted model/source HTTP and temporary SQLite roots.
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
$root = Split-Path -Parent $PSScriptRoot
$previous = @{
    GOOS=$env:GOOS; GOARCH=$env:GOARCH; CGO_ENABLED=$env:CGO_ENABLED
    GOMAXPROCS=$env:GOMAXPROCS; GOFLAGS=$env:GOFLAGS; GOPROXY=$env:GOPROXY; GOTOOLCHAIN=$env:GOTOOLCHAIN
}
Push-Location $root
try {
    $env:GOOS='windows'; $env:GOARCH='amd64'; $env:CGO_ENABLED='0'
    $env:GOMAXPROCS='2'; $env:GOFLAGS='-p=1'; $env:GOPROXY='off'; $env:GOTOOLCHAIN='local'
    & go test ./internal/bootstrap -run '^TestResearchSourceScopeHandoffFromNaturalLanguage$' -count=3 -timeout 3m
    if ($LASTEXITCODE -ne 0) { throw 'Source-scope handoff regression failed.' }
    Write-Host 'Source-scope handoff regression passed. Real-model semantic acceptance remains separate.'
} finally {
    $env:GOOS=$previous.GOOS; $env:GOARCH=$previous.GOARCH; $env:CGO_ENABLED=$previous.CGO_ENABLED
    $env:GOMAXPROCS=$previous.GOMAXPROCS; $env:GOFLAGS=$previous.GOFLAGS; $env:GOPROXY=$previous.GOPROXY; $env:GOTOOLCHAIN=$previous.GOTOOLCHAIN
    Pop-Location
}
