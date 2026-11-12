# Offline publication fixture. Only a disposable repo under .tmp is changed;
# native Go calls are mocked here, while pack-skills has separate real Go tests.
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$project = [System.IO.Path]::GetFullPath((Split-Path -Parent $PSScriptRoot))
$fixture = [System.IO.Path]::GetFullPath((Join-Path $project (".tmp\skill-publication-" + [Guid]::NewGuid().ToString("N"))))
$prefix = $project.TrimEnd('\') + '\'
if (-not $fixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw "Fixture escapes the project." }
$repo = Join-Path $fixture "repo"
$catalog = Join-Path $repo "internal\opensciskill"
$source = Join-Path $fixture "source"
$sourceSkills = Join-Path $source "backend\cli\skills"
$names = @("defaultskills", "defaultskills.upstream.manifest.json", "defaultskills.manifest.json", "capability-audit.v1.json", "defaultskills.zip")
New-Item -ItemType Directory -Path (Join-Path $repo "scripts"), $catalog, $sourceSkills | Out-Null
foreach ($script in @("sync-openscience-skills.ps1", "publish-skill-catalog.ps1")) {
    Copy-Item -LiteralPath (Join-Path $PSScriptRoot $script) -Destination (Join-Path $repo "scripts\$script")
}
. (Join-Path $PSScriptRoot "publish-skill-catalog.ps1")

function Assert-Equal($Expected, $Actual, [string]$Label) {
    if ($Expected -cne $Actual) { throw "$Label differs." }
}
function Read-PublishedState {
    return ((Get-ChildItem -LiteralPath $catalog -File -Recurse -Force |
        Where-Object { -not $_.FullName.Substring($catalog.Length + 1).StartsWith('.') } |
        ForEach-Object { "$($_.FullName.Substring($catalog.Length + 1))`t$($_.Length)`t$((Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash)" } |
        Sort-Object) -join "`n")
}
function Assert-NoStaging {
    $left = @(Get-ChildItem -LiteralPath $catalog -Force | Where-Object Name -like '.defaultskills-*')
    if ($left.Count -ne 0) { throw "Publication left temporary members: $($left.Name -join ', ')" }
}
function New-Prepared([string]$Tag) {
    $prepared = Join-Path $catalog (".defaultskills-prepared-" + [Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path (Join-Path $prepared "defaultskills") | Out-Null
    Set-Content -LiteralPath (Join-Path $prepared "defaultskills\SKILL.md") -Value $Tag -Encoding UTF8
    foreach ($name in $names | Where-Object { $_ -ne "defaultskills" }) { Set-Content -LiteralPath (Join-Path $prepared $name) -Value $Tag -Encoding UTF8 }
    return $prepared
}

# A scoped mock, removed before this fixture returns. Does not invoke a model,
# download an upstream checkout or replace the host's real Go executable.
$mock = [pscustomobject]@{ failPack = $true; checkRoot = "" }
Set-Item Function:go -Value ({
    $a = @($args)
    $command = $a[1]
    $root = $a[[Array]::IndexOf($a, '-root') + 1]
    $global:LASTEXITCODE = 0
    switch ($command) {
        './scripts/p7-skill-transform' {
            $output = $a[[Array]::IndexOf($a, '-output-manifest') + 1]
            Set-Content -LiteralPath $output -Value '{"fixture":"derived"}' -Encoding UTF8
        }
        './scripts/p7-skill-audit' {
            $output = $a[[Array]::IndexOf($a, '-output') + 1]
            Set-Content -LiteralPath $output -Value '{"fixture":"audit"}' -Encoding UTF8
        }
        './scripts/pack-skills' {
            if ($a -contains '-check') {
                $mock.checkRoot = $root
                if (-not (Test-Path -LiteralPath (Join-Path $root 'defaultskills.zip'))) { throw 'Check archive missing.' }
            } elseif ($mock.failPack) {
                $global:LASTEXITCODE = 17
            } else {
                Set-Content -LiteralPath (Join-Path $root 'defaultskills.zip') -Value 'fixture-archive' -Encoding UTF8
            }
        }
        default { throw "Unexpected native command in fixture: $command" }
    }
}.GetNewClosure())

try {
    # Exact source count accepted by production sync without real upstream data.
    for ($i = 0; $i -lt 311; $i++) {
        $dir = Join-Path $sourceSkills "skill-$i"
        New-Item -ItemType Directory -Path $dir | Out-Null
        Set-Content -LiteralPath (Join-Path $dir "SKILL.md") -Value "fixture skill $i" -Encoding UTF8
    }
    for ($i = 0; $i -lt 1311; $i++) { Set-Content -LiteralPath (Join-Path $sourceSkills "resource-$i.txt") -Value "fixture resource $i" -Encoding UTF8 }
    foreach ($name in @("LICENSE", "NOTICE")) {
        Copy-Item -LiteralPath (Join-Path $project "internal\opensciskill\defaultskills\$name") -Destination (Join-Path $source $name)
    }
    Set-Content -LiteralPath (Join-Path $source "backend\cli\package.json") -Value '{"version":"fixture"}' -Encoding UTF8
    $old = New-Prepared "original"
    Publish-SkillCatalog -CatalogRoot $catalog -PreparedRoot $old
    Remove-Item -LiteralPath $old -Recurse -Force
    $before = Read-PublishedState

    $mock.failPack = $true
    $mock.checkRoot = ""
    $failed = $false
    try { & (Join-Path $repo "scripts\sync-openscience-skills.ps1") -SourceRoot $source -Update }
    catch {
        $failed = $_.Exception.Message -like '*resource packing failed*'
        if (-not $failed) { throw }
    }
    if (-not $failed) { throw "Injected pack failure was not propagated." }
    Assert-Equal $before (Read-PublishedState) "Pack failure changed old publication"
    Assert-NoStaging

    $mock.failPack = $false
    & (Join-Path $repo "scripts\sync-openscience-skills.ps1") -SourceRoot $source -Update
    $updated = Read-PublishedState
    if ($updated -eq $before) { throw "Successful Update did not publish." }
    Assert-NoStaging
    & (Join-Path $repo "scripts\sync-openscience-skills.ps1") -SourceRoot $source -Check
    Assert-Equal $updated (Read-PublishedState) "Check mutated the publication"
    Assert-Equal $catalog $mock.checkRoot "Check did not verify the published archive"
    Assert-NoStaging

    # Fail part way through both backup and publishing. Exercise the actual
    # helper's group rollback; all tree/manifest/audit/archive bytes must match.
    foreach ($mode in @("backup", "publish")) {
        $prepared = New-Prepared "replacement"
        $script:failFrom = if ($mode -eq "backup") { Join-Path $catalog "capability-audit.v1.json" } else { Join-Path $prepared "capability-audit.v1.json" }
        $script:moveFailed = $false
        function Move-Item {
            param([string]$LiteralPath, [string]$Destination, [switch]$Force, [string]$ErrorAction)
            if (-not $script:moveFailed -and $LiteralPath -eq $script:failFrom) {
                $script:moveFailed = $true
                throw "Injected publication move failure."
            }
            Microsoft.PowerShell.Management\Move-Item -LiteralPath $LiteralPath -Destination $Destination -Force:$Force -ErrorAction Stop
        }
        $failed = $false
        try { Publish-SkillCatalog -CatalogRoot $catalog -PreparedRoot $prepared }
        catch { $failed = $_.Exception.Message -like '*Injected publication move failure*' }
        finally { Remove-Item Function:\Move-Item }
        if (-not $failed) { throw "$mode failure was not propagated." }
        Assert-Equal $updated (Read-PublishedState) "$mode rollback changed published files"
        Remove-Item -LiteralPath $prepared -Recurse -Force
        Assert-NoStaging
    }
    # A locked new directory must not swallow the old tree as a nested child.
    # Preserve the original in backup if rollback cannot remove that member.
    $originalHash = (Get-FileHash -LiteralPath (Join-Path $catalog 'defaultskills\skill-0\SKILL.md') -Algorithm SHA256).Hash
    $prepared = New-Prepared "locked replacement"
    $script:failFrom = Join-Path $prepared 'capability-audit.v1.json'
    $script:moveFailed = $false
    function Move-Item {
        param([string]$LiteralPath, [string]$Destination, [switch]$Force, [string]$ErrorAction)
        if (-not $script:moveFailed -and $LiteralPath -eq $script:failFrom) {
            $script:moveFailed = $true
            throw "Injected publication move failure."
        }
        Microsoft.PowerShell.Management\Move-Item -LiteralPath $LiteralPath -Destination $Destination -Force:$Force -ErrorAction Stop
    }
    function Remove-Item {
        param([string]$LiteralPath, [switch]$Recurse, [switch]$Force, [string]$ErrorAction)
        if ($LiteralPath -eq (Join-Path $catalog 'defaultskills')) { throw "Injected locked publication directory." }
        Microsoft.PowerShell.Management\Remove-Item -LiteralPath $LiteralPath -Recurse:$Recurse -Force:$Force -ErrorAction Stop
    }
    $failed = $false
    try { Publish-SkillCatalog -CatalogRoot $catalog -PreparedRoot $prepared }
    catch { $failed = $_.Exception.Message -like '*rollback needs attention*original retained in backup*' }
    finally {
        Microsoft.PowerShell.Management\Remove-Item Function:\Move-Item
        Microsoft.PowerShell.Management\Remove-Item Function:\Remove-Item
    }
    if (-not $failed) { throw "Blocked rollback did not report the retained original." }
    $backups = @(Get-ChildItem -LiteralPath $catalog -Directory -Force | Where-Object Name -like '.defaultskills-backup-*')
    if ($backups.Count -ne 1) { throw "Blocked rollback erased or duplicated the backup." }
    Assert-Equal $originalHash (Get-FileHash -LiteralPath (Join-Path $backups[0].FullName 'defaultskills\skill-0\SKILL.md') -Algorithm SHA256).Hash "Blocked rollback lost the original tree"
    if (Test-Path -LiteralPath (Join-Path $catalog 'defaultskills\defaultskills')) { throw "Rollback nested the old directory inside the new one." }
    Write-Host "PASS: Update pack failure leaves originals untouched; Check is read-only; successful publication and partial backup/publish rollback preserve the group; blocked rollback retains the original tree in backup without nesting. Offline disposable fixture only."
} finally {
    Remove-Item Function:\go
    if (-not $fixture.StartsWith($prefix, [StringComparison]::OrdinalIgnoreCase)) { throw "Refusing cleanup outside the project." }
    if (Test-Path -LiteralPath $fixture) { Remove-Item -LiteralPath $fixture -Recurse -Force }
}
