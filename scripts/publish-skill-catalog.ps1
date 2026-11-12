# Publish source, inventories, audit and packed bytes as one recoverable group.
# Preparation/packing happens first; a failed move restores every old member.
function Publish-SkillCatalog {
    param([string]$CatalogRoot, [string]$PreparedRoot)
    $catalog = [System.IO.Path]::GetFullPath($CatalogRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
    $prepared = [System.IO.Path]::GetFullPath($PreparedRoot).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
    if ([System.IO.Path]::GetDirectoryName($prepared) -ne $catalog) {
        throw "Prepared Skill catalog must be a direct child of the destination catalog."
    }
    $names = @("defaultskills", "defaultskills.upstream.manifest.json", "defaultskills.manifest.json", "capability-audit.v1.json", "defaultskills.zip")
    foreach ($name in $names) {
        $source = Join-Path $prepared $name
        $kind = if ($name -eq "defaultskills") { "Container" } else { "Leaf" }
        if (-not (Test-Path -LiteralPath $source -PathType $kind)) { throw "Prepared Skill catalog is incomplete: $source" }
    }
    # No deletion follows links, including old catalogs preserved for rollback.
    foreach ($root in @($catalog, $prepared)) {
        if (((Get-Item -LiteralPath $root -Force).Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0) {
            throw "Reparse points are not accepted in the Skill publication paths: $root"
        }
    }
    foreach ($name in $names) {
        foreach ($item in @((Join-Path $catalog $name), (Join-Path $prepared $name))) {
            if (Test-Path -LiteralPath $item) {
                $entries = @((Get-Item -LiteralPath $item -Force))
                if (Test-Path -LiteralPath $item -PathType Container) { $entries += @(Get-ChildItem -LiteralPath $item -Recurse -Force) }
                if (@($entries | Where-Object { ($_.Attributes -band [System.IO.FileAttributes]::ReparsePoint) -ne 0 }).Count -gt 0) {
                    throw "Reparse points are not accepted in the Skill publication tree: $item"
                }
            }
        }
    }
    $backup = [System.IO.Path]::GetFullPath((Join-Path $catalog (".defaultskills-backup-" + [Guid]::NewGuid().ToString("N"))))
    if ([System.IO.Path]::GetDirectoryName($backup) -ne $catalog) { throw "Skill backup path escapes the catalog." }
    $backedUp = @()
    $published = @()
    $canRemoveBackup = $false
    New-Item -ItemType Directory -Path $backup | Out-Null
    try {
        foreach ($name in $names) {
            $target = Join-Path $catalog $name
            if (Test-Path -LiteralPath $target) {
                Move-Item -LiteralPath $target -Destination (Join-Path $backup $name) -ErrorAction Stop
                $backedUp += $name
            }
        }
        foreach ($name in $names) {
            Move-Item -LiteralPath (Join-Path $prepared $name) -Destination (Join-Path $catalog $name) -ErrorAction Stop
            $published += $name
        }
        $canRemoveBackup = $true
    } catch {
        $originalError = $_
        $rollbackErrors = @()
        foreach ($name in $published) {
            try { Remove-Item -LiteralPath (Join-Path $catalog $name) -Recurse -Force -ErrorAction Stop }
            catch { $rollbackErrors += $_.Exception.Message }
        }
        foreach ($name in $backedUp) {
            try {
                $target = Join-Path $catalog $name
                # Move-Item would nest an old directory inside a still-existing
                # new directory. If removing a locked member failed, preserve
                # its original at the backup path instead of moving it.
                if (Test-Path -LiteralPath $target) { throw "Rollback destination still exists: $target; original retained in backup." }
                Move-Item -LiteralPath (Join-Path $backup $name) -Destination $target -ErrorAction Stop
            }
            catch { $rollbackErrors += $_.Exception.Message }
        }
        if ($rollbackErrors.Count -gt 0) {
            # Never erase the only surviving originals when rollback is blocked.
            throw "Skill publication failed: $($originalError.Exception.Message); rollback needs attention. Preserved originals: $backup. $($rollbackErrors -join '; ')"
        }
        $canRemoveBackup = $true
        throw $originalError
    } finally {
        if ($canRemoveBackup -and (Test-Path -LiteralPath $backup)) {
            Remove-Item -LiteralPath $backup -Recurse -Force
        }
    }
}
