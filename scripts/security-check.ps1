$ErrorActionPreference = "Stop"
$env:GOARCH = "amd64"
$env:CGO_ENABLED = "0"
$env:GOTOOLCHAIN = "local"

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

Write-Host "== Go module integrity =="
Invoke-Native go mod verify

Write-Host "== Frontend production dependency audit =="
Push-Location frontend
try {
    # Some npm mirrors do not implement the audit API. This command only
    # overrides the registry for this invocation and does not change user config.
    Invoke-Native npm audit --omit=dev --registry=https://registry.npmjs.org/
} finally {
    Pop-Location
}

Write-Host "Security dependency checks passed."
