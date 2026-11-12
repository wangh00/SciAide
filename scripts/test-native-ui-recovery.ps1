param([string]$OutputDirectory = ".tmp/native-ui-recovery-$([DateTime]::Now.ToString('yyyyMMdd-HHmmss'))")
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# Only a newly created, isolated home and this repository's release EXE.
$root = Split-Path -Parent $PSScriptRoot
$exe = (Resolve-Path (Join-Path $root "build/bin/SciAide.exe")).Path
$output = [IO.Path]::GetFullPath((Join-Path $root $OutputDirectory))
$testRoot = [IO.Path]::GetFullPath((Join-Path $root ".tmp")) + [IO.Path]::DirectorySeparatorChar
if (-not $output.StartsWith($testRoot, [StringComparison]::OrdinalIgnoreCase) -or (Test-Path -LiteralPath $output)) {
    throw "Native test requires a new directory beneath this project's .tmp."
}
if (@(Get-CimInstance Win32_Process -Filter "Name='SciAide.exe'" | Where-Object { $_.ExecutablePath -ieq $exe }).Count) {
    throw "Close this project's SciAide before the isolated native test."
}
New-Item -ItemType Directory -Path $output | Out-Null
$previousHome = $env:SCIAIDE_HOME
$hostProcess = $null
try {
    $env:SCIAIDE_HOME = Join-Path $output "home"
    # Capture stderr explicitly: GUI processes launched without standard handles
    # may stall in WebView initialization on this machine.
    $hostProcess = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -RedirectStandardError (Join-Path $output "stderr.txt") -RedirectStandardOutput (Join-Path $output "stdout.txt") -PassThru
    $env:SCIAIDE_HOME = $previousHome
    $log = Join-Path $output "home/logs/sciaide.jsonl"
    $deadline = [DateTime]::UtcNow.AddSeconds(30)
    do {
        Start-Sleep -Milliseconds 500
        if ($hostProcess.HasExited) { throw "Isolated native host exited before startup." }
        $started = (Test-Path -LiteralPath $log) -and (Select-String -LiteralPath $log -SimpleMatch 'SciAide started' -Quiet)
    } until ($started -or [DateTime]::UtcNow -gt $deadline)
    if (-not $started) { throw "Native startup did not complete; no renderer terminated." }
    Start-Sleep -Seconds 8 # allow readonly page heartbeat and process tracking
    $hostID = $hostProcess.Id
    $verifiedHost = Get-CimInstance Win32_Process -Filter "ProcessId=$hostID"
    if ($verifiedHost.ExecutablePath -ine $exe) { throw "Test host identity changed." }
    $all = Get-CimInstance Win32_Process
    $ids = [Collections.Generic.HashSet[int]]::new()
    $null = $ids.Add($hostID)
    do {
        $added = 0
        foreach ($p in $all) {
            if ($ids.Contains([int]$p.ParentProcessId) -and $ids.Add([int]$p.ProcessId)) { $added++ }
        }
    } while ($added -gt 0)
    $renderers = @($all | Where-Object { $ids.Contains([int]$_.ProcessId) -and $_.Name -eq 'msedgewebview2.exe' -and $_.CommandLine -match '(?:^|\s)--type=renderer(?:\s|$)' -and $_.CommandLine.Contains((Join-Path $output "home/cache/webview2")) })
    if ($renderers.Count -ne 1) { throw "Expected exactly one isolated host renderer." }
    $renderers | Select-Object ProcessId, ParentProcessId, ExecutablePath, CommandLine | ConvertTo-Json | Set-Content (Join-Path $output "renderer-before.json")
    # Recheck identity immediately before the only deliberate process mutation.
    $renderer = Get-CimInstance Win32_Process -Filter "ProcessId=$($renderers[0].ProcessId)"
    if ($renderer.CommandLine -cne $renderers[0].CommandLine -or $renderer.ParentProcessId -ne $renderers[0].ParentProcessId) { throw "Renderer identity changed." }
    $injected = [DateTimeOffset]::Now
    Stop-Process -Id $renderer.ProcessId -Force
    $deadline = [DateTime]::UtcNow.AddSeconds(55)
    do {
        Start-Sleep -Seconds 1
        if ($hostProcess.HasExited) { throw "Native host exited after renderer failure." }
        $acknowledged = Select-String -LiteralPath $log -SimpleMatch 'UI page responded after native recovery' -Quiet
    } until ($acknowledged -or [DateTime]::UtcNow -gt $deadline)
    if (-not $acknowledged) { throw "Recovery requested but page heartbeat did not resume." }
    Start-Sleep -Seconds 6
    $events = @(Get-Content -LiteralPath $log | ForEach-Object { $_ | ConvertFrom-Json })
    $requests = @($events | Where-Object msg -eq 'native renderer exited; restoring UI only')
    $acks = @($events | Where-Object msg -eq 'UI page responded after native recovery')
    if ($requests.Count -ne 1 -or $acks.Count -ne 1) { throw "Unexpected recovery loop or acknowledgement count." }
    $result = [ordered]@{ source = "isolated release EXE controlled renderer termination, not actual OOM or real-model research"; exeSha256 = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash; hostPid = $hostID; rendererPid = $renderer.ProcessId; recoveryRequestedAt = $requests[0].time; pageAcknowledgedAt = $acks[0].time; secondsToPageResponse = ([DateTimeOffset]$acks[0].time - $injected).TotalSeconds; hostStayedAlive = -not $hostProcess.HasExited; reloadRequests = $requests.Count; pageAcks = $acks.Count }
    $result | ConvertTo-Json | Set-Content (Join-Path $output "result.json")
    Write-Output "PASS: renderer failed; same host navigated natively; page heartbeat resumed once. Evidence: $output"
} finally {
    $env:SCIAIDE_HOME = $previousHome
    if ($hostProcess -and -not $hostProcess.HasExited) {
        $check = Get-CimInstance Win32_Process -Filter "ProcessId=$($hostProcess.Id)"
        if ($check -and $check.ExecutablePath -ieq $exe) {
            $null = $hostProcess.CloseMainWindow()
            if (-not $hostProcess.WaitForExit(5000)) {
                $check = Get-CimInstance Win32_Process -Filter "ProcessId=$($hostProcess.Id)"
                if ($check -and $check.ExecutablePath -ieq $exe) { Stop-Process -Id $hostProcess.Id -Force }
            }
        }
    }
}
