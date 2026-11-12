param([string]$OutputDirectory = ".tmp/native-lifecycle-$([DateTime]::Now.ToString('yyyyMMdd-HHmmss'))")
$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

# Exercise the one release EXE with an empty, isolated application home. Never
# change a user task, configure a provider, or terminate an unrelated process.
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
$isolatedHome = Join-Path $output "home"
$marker = Join-Path $isolatedHome "logs/desktop-session.json"
$desktopLog = Join-Path $isolatedHome "logs/desktop.jsonl"
$applicationLog = Join-Path $isolatedHome "logs/sciaide.jsonl"
$previousHome = $env:SCIAIDE_HOME
$script:hostProcess = $null

# Process.CloseMainWindow ignores hidden windows. Hidden test launches still
# have the ordinary Wails window; send WM_CLOSE only after PID, path and class
# identification rather than treating a missing visible window as an app hang.
Add-Type -TypeDefinition @'
using System;
using System.Text;
using System.Runtime.InteropServices;
public static class SciAideLifecycleWindow {
    public delegate bool EnumProc(IntPtr hwnd, IntPtr data);
    [DllImport("user32.dll")] private static extern bool EnumWindows(EnumProc callback, IntPtr data);
    [DllImport("user32.dll")] private static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
    [DllImport("user32.dll", CharSet=CharSet.Unicode)] private static extern int GetClassName(IntPtr hwnd, StringBuilder name, int length);
    [DllImport("user32.dll", SetLastError=true)] private static extern bool PostMessage(IntPtr hwnd, uint message, IntPtr wparam, IntPtr lparam);
    public static bool RequestClose(uint processId) {
        bool sent = false;
        EnumWindows((hwnd, data) => {
            uint pid;
            GetWindowThreadProcessId(hwnd, out pid);
            if (pid != processId) return true;
            var name = new StringBuilder(256);
            GetClassName(hwnd, name, name.Capacity);
            if (name.ToString() != "SciAideMainWindowV1") return true;
            sent = PostMessage(hwnd, 0x0010, IntPtr.Zero, IntPtr.Zero);
            return false;
        }, IntPtr.Zero);
        return sent;
    }
}
'@

function Assert-TestHost {
    $check = Get-CimInstance Win32_Process -Filter "ProcessId=$($script:hostProcess.Id)"
    if (-not $check -or $check.ExecutablePath -ine $exe) { throw "Isolated host identity changed." }
}

function Start-TestHost {
    $env:SCIAIDE_HOME = $isolatedHome
    try {
        # Deliberately no stdout/stderr redirection: a GUI installation must
        # persist diagnostics without depending on a valid console handle.
        $script:hostProcess = Start-Process -FilePath $exe -WorkingDirectory $root -WindowStyle Hidden -PassThru
    } finally { $env:SCIAIDE_HOME = $previousHome }
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    do {
        Start-Sleep -Milliseconds 250
        if ($script:hostProcess.HasExited) { throw "Native host exited during startup." }
        $state = if (Test-Path -LiteralPath $marker) { Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json } else { $null }
        if ($state -and $state.pid -eq $script:hostProcess.Id -and $state.phase -eq "running") { break }
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $state -or $state.pid -ne $script:hostProcess.Id -or $state.phase -ne "running") {
        throw "Native GUI startup did not reach running; inspect isolated diagnostics."
    }
    Assert-TestHost
    if (-not (Test-Path -LiteralPath $applicationLog) -or -not (Select-String -LiteralPath $applicationLog -SimpleMatch 'SciAide started' -Quiet)) {
        throw "Application file log is missing without console redirection."
    }
}

function Close-TestHost {
    Assert-TestHost
    $deadline = [DateTime]::UtcNow.AddSeconds(10)
    $requested = $false
    do {
        Assert-TestHost
        $script:hostProcess.Refresh()
        $requested = [SciAideLifecycleWindow]::RequestClose([uint32]$script:hostProcess.Id)
        if ($requested) { break }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    if (-not $requested -or -not $script:hostProcess.WaitForExit(15000)) {
        throw "Native host did not close normally."
    }
    $state = Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
    if ($state.pid -ne $script:hostProcess.Id -or $state.phase -ne "clean_exit") { throw "Normal close lost its clean lifecycle marker." }
}

try {
    Start-TestHost
    $firstPID = $script:hostProcess.Id
    Close-TestHost
    $firstExit = Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
    Start-TestHost
    $killedPID = $script:hostProcess.Id
    $warnings = @(Get-Content -LiteralPath $desktopLog | ForEach-Object { $_ | ConvertFrom-Json } | Where-Object msg -eq 'previous desktop session did not record a clean exit')
    if ($warnings.Count) { throw "Clean close was incorrectly reported as an unfinished exit." }
    Assert-TestHost
    Stop-Process -Id $killedPID -Force
    if (-not $script:hostProcess.WaitForExit(10000)) { throw "Controlled host termination did not finish." }
    $unfinished = Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json
    if ($unfinished.pid -ne $killedPID -or $unfinished.phase -ne "running") { throw "External termination lost the last observed lifecycle marker." }
    Start-TestHost
    $restartPID = $script:hostProcess.Id
    $warnings = @(Get-Content -LiteralPath $desktopLog | ForEach-Object { $_ | ConvertFrom-Json } | Where-Object msg -eq 'previous desktop session did not record a clean exit')
    if ($warnings.Count -ne 1 -or $warnings[0].previousPid -ne $killedPID -or $warnings[0].lastPhase -ne "running") {
        throw "Restart did not identify the interrupted host session."
    }
    Close-TestHost
    $logs = @(Get-Content -LiteralPath $applicationLog | ForEach-Object { $_ | ConvertFrom-Json })
    if (@($logs | Where-Object msg -eq 'SciAide started').Count -ne 3 -or @($logs | Where-Object msg -eq 'application shutdown completed').Count -ne 2) {
        throw "Durable application startup/shutdown logs are incomplete."
    }
    $result = [ordered]@{
        source = "isolated release EXE lifecycle, controlled host termination, no provider configured or real-model task"
        exeSha256 = (Get-FileHash -LiteralPath $exe -Algorithm SHA256).Hash
        consoleRedirected = $false
        cleanPID = $firstPID
        cleanPhase = $firstExit.phase
        controlledTerminationPID = $killedPID
        lastObservedPhase = $unfinished.phase
        restartPID = $restartPID
        unfinishedWarnings = $warnings.Count
        finalPhase = (Get-Content -LiteralPath $marker -Raw | ConvertFrom-Json).phase
        fileLogStarts = 3
        fileLogShutdowns = 2
    }
    $result | ConvertTo-Json | Set-Content (Join-Path $output "result.json")
    Write-Output "PASS: GUI file logs without redirected handles; clean exit distinguished from controlled termination and restart. Evidence: $output"
} finally {
    $env:SCIAIDE_HOME = $previousHome
    if ($script:hostProcess -and -not $script:hostProcess.HasExited) {
        $check = Get-CimInstance Win32_Process -Filter "ProcessId=$($script:hostProcess.Id)"
        if ($check -and $check.ExecutablePath -ieq $exe) {
            $null = [SciAideLifecycleWindow]::RequestClose([uint32]$script:hostProcess.Id)
            if (-not $script:hostProcess.WaitForExit(5000)) {
                Assert-TestHost
                Stop-Process -Id $script:hostProcess.Id -Force
            }
        }
    }
}
