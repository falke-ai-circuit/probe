# p2discover.ps1 - resolve the running probe client from its own launch chain.
# ASCII only, Windows PowerShell 5.1, no arguments.
# Emits exactly one JSON line on stdout:
#   {"pid":N,"exe":"...","dir":"...","name":"...","cmdline":"..."}
# Contract: P2-01 section 3 phase A (A4). Read-only: touches no file, starts nothing.
$ErrorActionPreference = 'Stop'

function Test-HelperName {
    param([string]$Name)
    if ($Name -ieq 'cmd.exe') { return $true }
    if ($Name -ieq 'powershell.exe') { return $true }
    if ($Name -ieq 'pwsh.exe') { return $true }
    if ($Name -ieq 'conhost.exe') { return $true }
    return $false
}

$current = $PID
$found = $null
for ($depth = 0; $depth -lt 5; $depth++) {
    $proc = $null
    try {
        $proc = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $current) -ErrorAction Stop
    } catch {
        $proc = $null
    }
    if ($null -eq $proc) { break }
    if ($depth -gt 0 -and -not (Test-HelperName -Name ([string]$proc.Name))) {
        $found = $proc
        break
    }
    $parent = 0
    try { $parent = [int]$proc.ParentProcessId } catch { $parent = 0 }
    if ($parent -le 0) { break }
    $current = $parent
}

if ($null -eq $found) {
    try {
        $found = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $PID) -ErrorAction Stop
    } catch {
        $found = $null
    }
}
if ($null -eq $found) {
    Write-Output '{"pid":0,"exe":"","dir":"","name":"","cmdline":""}'
    exit 2
}

$exe = [string]$found.ExecutablePath
$name = [System.IO.Path]::GetFileNameWithoutExtension([string]$found.Name)
$dir = ''
if ($exe -ne '') {
    try { $dir = [System.IO.Path]::GetDirectoryName($exe) } catch { $dir = '' }
}
$cmdline = ''
try { $cmdline = [string]$found.CommandLine } catch { $cmdline = '' }

$payload = [ordered]@{
    pid     = [int]$found.ProcessId
    exe     = $exe
    dir     = $dir
    name    = $name
    cmdline = $cmdline
}
Write-Output ($payload | ConvertTo-Json -Compress -Depth 3)
exit 0
