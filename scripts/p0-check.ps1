$ErrorActionPreference = "Stop"

$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOTOOLCHAIN = "local"
$env:GOPROXY = "off"

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

if (-not (Test-Path -LiteralPath "go.sum")) {
    throw "缺少 go.sum。请按 docs/development.md 执行 go mod tidy。"
}
if (-not (Test-Path -LiteralPath "frontend/package-lock.json")) {
    throw "缺少 frontend/package-lock.json。请在 frontend 执行 npm install。"
}
if (-not (Test-Path -LiteralPath "frontend/node_modules")) {
    throw "缺少 frontend/node_modules。请在 frontend 执行 npm ci。"
}

Write-Host "== Go format =="
$goFiles = Get-ChildItem -Path . -Recurse -Filter "*.go" -File |
    Where-Object { $_.FullName -notmatch "[\\/](artifacts|frontend[\\/]node_modules)[\\/]" }
$unformatted = $goFiles | ForEach-Object {
    $output = & gofmt -l $_.FullName
    if ($LASTEXITCODE -ne 0) {
        throw "gofmt failed for $($_.FullName)"
    }
    $output
}
if ($unformatted) {
    $unformatted | ForEach-Object { Write-Host $_ }
    throw "Go files are not formatted."
}

Write-Host "== Frontend type check =="
Push-Location frontend
try {
    Invoke-Native npm run typecheck
    Invoke-Native npm run build
} finally {
    Pop-Location
}

# main.go embeds frontend/dist, so frontend build must precede Go package checks.
Write-Host "== Go vet =="
Invoke-Native go vet ./...

Write-Host "== Go tests =="
Invoke-Native go test ./... -count=1

Write-Host "== Contract JSON syntax =="
Get-ChildItem -Path contracts -Filter "*.json" -File | ForEach-Object {
    $null = Get-Content -LiteralPath $_.FullName -Raw | ConvertFrom-Json
}

Write-Host "P0/P1 checks passed."
