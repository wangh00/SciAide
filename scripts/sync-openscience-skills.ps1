[CmdletBinding()]
param(
    [string]$SourceRoot = "",
    [switch]$Check,
    [switch]$Update
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
if ($Check -and $Update) { throw "-Check and -Update are mutually exclusive." }

$repoRoot = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
if ([string]::IsNullOrWhiteSpace($SourceRoot)) { $SourceRoot = Join-Path $repoRoot "..\openscience-main" }
$sourceRootPath = [System.IO.Path]::GetFullPath($SourceRoot)
$sourceSkills = Join-Path $sourceRootPath "backend\cli\skills"
$catalogRoot = Join-Path $repoRoot "internal\opensciskill"
$targetRoot = Join-Path $catalogRoot "defaultskills"
$derivedManifestPath = Join-Path $catalogRoot "defaultskills.manifest.json"
$upstreamManifestPath = Join-Path $catalogRoot "defaultskills.upstream.manifest.json"
$auditPath = Join-Path $catalogRoot "capability-audit.v1.json"
$expectedLicenseHash = "d8ac5e917b2099e5cbe2999f297b56e2cc946e545f39aebc1e1aa91dd5cb0e9f"
$expectedNoticeHash = "14a9f611d28bccc13723c15a251fa358baaef95995beaae2571f8bb7dbea3069"

function Assert-PathWithinRepo([string]$Path) {
    $full = [System.IO.Path]::GetFullPath($Path)
    $prefix = $repoRoot.TrimEnd([System.IO.Path]::DirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    if (-not $full.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) {
        throw "Refusing to modify a path outside the SciBuddy repository: $full"
    }
}

function Assert-SafeTree([string]$Root) {
    if (-not (Test-Path -LiteralPath $Root -PathType Container)) { throw "Directory does not exist: $Root" }
    $unsafe = Get-ChildItem -LiteralPath $Root -Recurse -Force | Where-Object {
        ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0
    } | Select-Object -First 1
    if ($unsafe) { throw "Reparse points are not accepted in the Skill source tree: $($unsafe.FullName)" }
}

function Get-Inventory([string]$Root) {
    $rootPath = [System.IO.Path]::GetFullPath($Root)
    $prefix = $rootPath.TrimEnd([System.IO.Path]::DirectorySeparatorChar, [System.IO.Path]::AltDirectorySeparatorChar) + [System.IO.Path]::DirectorySeparatorChar
    return @(Get-ChildItem -LiteralPath $rootPath -Recurse -Force -File | ForEach-Object {
        $full = [System.IO.Path]::GetFullPath($_.FullName)
        if (-not $full.StartsWith($prefix, [System.StringComparison]::OrdinalIgnoreCase)) { throw "Inventory path escapes root: $full" }
        [pscustomobject][ordered]@{
            path = $full.Substring($prefix.Length).Replace('\', '/')
            size = [int64]$_.Length
            sha256 = (Get-FileHash -LiteralPath $full -Algorithm SHA256).Hash.ToLowerInvariant()
        }
    } | Sort-Object -Property @{ Expression = "path"; Ascending = $true })
}

function Get-UpstreamInventory {
    $values = @(Get-Inventory $sourceSkills)
    foreach ($name in @("LICENSE", "NOTICE")) {
        $item = Get-Item -LiteralPath (Join-Path $sourceRootPath $name)
        $values += [pscustomobject][ordered]@{ path = $name; size = [int64]$item.Length; sha256 = (Get-FileHash -LiteralPath $item.FullName -Algorithm SHA256).Hash.ToLowerInvariant() }
    }
    return @($values | Sort-Object -Property @{ Expression = "path"; Ascending = $true })
}

function Assert-InventoryShape([object[]]$Inventory) {
    $skills = @($Inventory | Where-Object { $_.path -eq "SKILL.md" -or $_.path.EndsWith("/SKILL.md", [System.StringComparison]::Ordinal) }).Count
    if ($skills -ne 311 -or $Inventory.Count -ne 1624) { throw "Unexpected Skill inventory: skills=$skills files=$($Inventory.Count); expected 311 and 1624." }
}

function Compare-Inventories([object[]]$Expected, [object[]]$Actual, [string]$Label) {
    $left = @($Expected | ForEach-Object { "$($_.path)`t$($_.size)`t$($_.sha256)" })
    $right = @($Actual | ForEach-Object { "$($_.path)`t$($_.size)`t$($_.sha256)" })
    $difference = @(Compare-Object -ReferenceObject $left -DifferenceObject $right)
    if ($difference.Count -gt 0) {
        $preview = ($difference | Select-Object -First 20 | ForEach-Object { "$($_.SideIndicator) $($_.InputObject)" }) -join [Environment]::NewLine
        throw "$Label drift detected ($($difference.Count) differences).`n$preview"
    }
}

function Write-UpstreamManifest([object[]]$Inventory, [string]$Path) {
    $package = Get-Content -LiteralPath (Join-Path $sourceRootPath "backend\cli\package.json") -Raw -Encoding UTF8 | ConvertFrom-Json
    $value = [pscustomobject][ordered]@{
        schemaVersion = 1
        sourceRepository = "https://github.com/synthetic-sciences/openscience"
        sourceVersion = [string]$package.version
        sourceRevision = "source archive without git metadata"
        skillCount = 311
        fileCount = $Inventory.Count
        files = $Inventory
    }
    $json = ($value | ConvertTo-Json -Depth 6).Replace("`r`n", "`n").TrimEnd("`r", "`n") + "`n"
    [System.IO.File]::WriteAllText($Path, $json, [System.Text.UTF8Encoding]::new($false))
}

function Assert-SameFile([string]$Expected, [string]$Actual, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Actual -PathType Leaf)) { throw "$Label is missing: $Actual" }
    $left = (Get-FileHash -LiteralPath $Expected -Algorithm SHA256).Hash
    $right = (Get-FileHash -LiteralPath $Actual -Algorithm SHA256).Hash
    if ($left -ne $right) { throw "$Label drift detected." }
}

foreach ($required in @($sourceSkills, (Join-Path $sourceRootPath "LICENSE"), (Join-Path $sourceRootPath "NOTICE"), (Join-Path $sourceRootPath "backend\cli\package.json"))) {
    if (-not (Test-Path -LiteralPath $required)) { throw "OpenScience source is incomplete: $required" }
}
Assert-SafeTree $sourceSkills
$sourceInventory = @(Get-UpstreamInventory)
Assert-InventoryShape $sourceInventory
$license = $sourceInventory | Where-Object path -eq "LICENSE"
$notice = $sourceInventory | Where-Object path -eq "NOTICE"
if ($license.sha256 -ne $expectedLicenseHash -or $notice.sha256 -ne $expectedNoticeHash) { throw "OpenScience LICENSE or NOTICE changed; review licensing before syncing." }

$token = [Guid]::NewGuid().ToString("N")
$staging = Join-Path $catalogRoot ".defaultskills-derived-$token"
$generatedUpstream = Join-Path $catalogRoot ".defaultskills-upstream-$token.json"
$generatedDerived = Join-Path $catalogRoot ".defaultskills-derived-$token.json"
$generatedAudit = Join-Path $catalogRoot ".capability-audit-$token.json"
$backup = Join-Path $catalogRoot ".defaultskills-backup-$token"
foreach ($path in @($staging, $generatedUpstream, $generatedDerived, $generatedAudit, $backup)) { Assert-PathWithinRepo $path }

try {
    New-Item -ItemType Directory -Path $staging | Out-Null
    Get-ChildItem -LiteralPath $sourceSkills -Force | ForEach-Object { Copy-Item -LiteralPath $_.FullName -Destination $staging -Recurse -Force }
    Copy-Item -LiteralPath (Join-Path $sourceRootPath "LICENSE") -Destination (Join-Path $staging "LICENSE")
    Copy-Item -LiteralPath (Join-Path $sourceRootPath "NOTICE") -Destination (Join-Path $staging "NOTICE")
    Compare-Inventories $sourceInventory @(Get-Inventory $staging) "Raw staging tree"
    Write-UpstreamManifest $sourceInventory $generatedUpstream

    & go run ./scripts/p7-skill-transform -root $staging -upstream-manifest $generatedUpstream -output-manifest $generatedDerived
    if ($LASTEXITCODE -ne 0) { throw "SciAide Skill transform failed." }
    & go run ./scripts/p7-skill-audit -root $staging -output $generatedAudit
    if ($LASTEXITCODE -ne 0) { throw "SciAide Skill capability audit generation failed." }
    Assert-InventoryShape @(Get-Inventory $staging)

    if (-not $Update) {
        Assert-SafeTree $targetRoot
        Compare-Inventories @(Get-Inventory $staging) @(Get-Inventory $targetRoot) "SciAide derived Skill tree"
        Assert-SameFile $generatedUpstream $upstreamManifestPath "Upstream provenance manifest"
        Assert-SameFile $generatedDerived $derivedManifestPath "SciAide derived manifest"
        Assert-SameFile $generatedAudit $auditPath "SciAide capability audit"
    } else {
        foreach ($path in @($targetRoot, $derivedManifestPath, $upstreamManifestPath, $auditPath)) { Assert-PathWithinRepo $path }
        if (Test-Path -LiteralPath $targetRoot) { Move-Item -LiteralPath $targetRoot -Destination $backup }
        Move-Item -LiteralPath $staging -Destination $targetRoot
        Move-Item -LiteralPath $generatedUpstream -Destination $upstreamManifestPath -Force
        Move-Item -LiteralPath $generatedDerived -Destination $derivedManifestPath -Force
        Move-Item -LiteralPath $generatedAudit -Destination $auditPath -Force
        if (Test-Path -LiteralPath $backup) { Remove-Item -LiteralPath $backup -Recurse -Force }
    }
} catch {
    if ($Update -and (Test-Path -LiteralPath $backup)) {
        if (Test-Path -LiteralPath $targetRoot) { Remove-Item -LiteralPath $targetRoot -Recurse -Force }
        Move-Item -LiteralPath $backup -Destination $targetRoot
    }
    throw
} finally {
    foreach ($path in @($staging, $backup)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Recurse -Force } }
    foreach ($path in @($generatedUpstream, $generatedDerived, $generatedAudit)) { if (Test-Path -LiteralPath $path) { Remove-Item -LiteralPath $path -Force } }
}

$mode = if ($Update) { "updated" } else { "verified" }
Write-Host "SciAide derived Skill tree ${mode}: 311 Skills, 1624 files, transform p7.3-v2, capability audit p7.3-v2."
