# p2boot.ps1 - PROBE bootstrap and swap helper (Windows, ASCII only, PowerShell 5.1).
#
# Modes:
#   -Preflight  verify the staged payload and the machine state, change nothing
#   -Launch     start the detached -Commit process and return
#   -Commit     perform the swap sequence SW-10..SW-20 (boundary and after)
#
# Exit codes (P2-01 F4):
#   0 ok
#   2 preflight refused (state is not what the manifest describes)
#   3 staging invalid (hash or identity of a staged file is wrong)
#   4 rename or commit sequence failed (machine left runnable)
#   5 old pid survived the kill
#   6 rollback completed
#   7 rollback failed
#
# Journal: <script dir>\p2boot.journal, ASCII, one line per event, append only,
# key=value, capped at 2000 lines. Every line is written before the action it
# describes. No configuration, token or secret material is ever written here.
#
# Contract: /opt/data/tmp/probe1/P2-01_BOOTSTRAP_DESIGN.md (D1-D4)
#           /opt/data/tmp/probe1/P2-02_SWAP_DESIGN.md  (S1-S5, SW-00..SW-20)

[CmdletBinding()]
param(
    [switch]$Preflight,
    [switch]$Launch,
    [switch]$Commit
)

$ErrorActionPreference = 'Stop'

$ScriptDir    = Split-Path -Parent $PSCommandPath
$BootPath     = $PSCommandPath
$ManifestPath = Join-Path $ScriptDir 'p2boot.manifest.json'
$JournalPath  = Join-Path $ScriptDir 'p2boot.journal'
$OldShaPath   = Join-Path $ScriptDir 'p2boot.oldsha'
$JournalCap   = 2000

$ExitOK           = 0
$ExitRefused      = 2
$ExitStaging      = 3
$ExitRename       = 4
$ExitOldPID       = 5
$ExitRollback     = 6
$ExitRollbackFail = 7

$script:JournalCount = -1

# ---------------------------------------------------------------------------
# journal
# ---------------------------------------------------------------------------

function Get-EpochSeconds {
    $origin = [datetime]'1970-01-01T00:00:00Z'
    $now = (Get-Date).ToUniversalTime()
    return [int](New-TimeSpan -Start $origin -End $now).TotalSeconds
}

function Write-Journal {
    param([string]$Event, [string]$Detail = '')
    if ([string]::IsNullOrEmpty($Event)) { return }
    if ($script:JournalCount -lt 0) {
        if (Test-Path -LiteralPath $JournalPath) {
            $script:JournalCount = @(Get-Content -LiteralPath $JournalPath -ErrorAction SilentlyContinue).Count
        } else {
            $script:JournalCount = 0
        }
    }
    if ($script:JournalCount -ge $JournalCap) {
        if ($Event -ne 'JOURNAL_FULL') {
            try { Add-Content -LiteralPath $JournalPath -Value 'JOURNAL_FULL cap=2000' -Encoding ASCII } catch { }
        }
        return
    }
    $line = 'ts=' + (Get-EpochSeconds).ToString() + ' ' + $Event
    if ($Detail -ne '') { $line = $line + ' ' + $Detail }
    $line = $line -replace '[\r\n]+', ' '
    try {
        Add-Content -LiteralPath $JournalPath -Value $line -Encoding ASCII
        $script:JournalCount = $script:JournalCount + 1
    } catch { }
}

function ConvertTo-DetailValue {
    param([string]$Value)
    if ($null -eq $Value) { return 'empty' }
    $v = $Value -replace '[\r\n]+', ' '
    $v = $v -replace '\s+', '_'
    if ($v -eq '') { return 'empty' }
    return $v
}

# ---------------------------------------------------------------------------
# hashing
# ---------------------------------------------------------------------------

function Get-Sha256Hex {
    param([string]$Path)
    $sha = [System.Security.Cryptography.SHA256]::Create()
    try {
        $stream = [System.IO.File]::Open($Path, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
        try {
            $bytes = $sha.ComputeHash($stream)
        } finally {
            $stream.Close()
        }
    } finally {
        $sha.Dispose()
    }
    $sb = New-Object System.Text.StringBuilder
    foreach ($b in $bytes) { [void]$sb.Append($b.ToString('x2')) }
    return $sb.ToString()
}

# ---------------------------------------------------------------------------
# manifest
# ---------------------------------------------------------------------------

function Get-OptInt {
    param($Obj, [string]$Key, [int]$Default)
    $value = $Obj.$Key
    if ($null -eq $value) { return $Default }
    try { return [int]$value } catch { return $Default }
}

function Read-Manifest {
    if (-not (Test-Path -LiteralPath $ManifestPath)) { throw 'manifest_missing' }
    $raw = Get-Content -LiteralPath $ManifestPath -Raw -ErrorAction Stop
    $m = $raw | ConvertFrom-Json
    $required = @('canonical', 'size_payload', 'sha256_payload', 'sha256_boot', 'server_host', 'server_port')
    foreach ($key in $required) {
        $value = $m.$key
        if ($null -eq $value) { throw ('manifest_key_missing_' + $key) }
        if ($value -is [string] -and $value -eq '') { throw ('manifest_key_empty_' + $key) }
    }
    return $m
}

function Test-IsRootedPath {
    param([string]$Path)
    if ([string]::IsNullOrEmpty($Path)) { return $false }
    try { return [System.IO.Path]::IsPathRooted($Path) } catch { return $false }
}

# ---------------------------------------------------------------------------
# process helpers
# ---------------------------------------------------------------------------

function Test-ProcessAlive {
    param([int]$ProcId)
    if ($ProcId -le 0) { return $false }
    try {
        $p = Get-Process -Id $ProcId -ErrorAction Stop
    } catch {
        return $false
    }
    if ($null -eq $p) { return $false }
    return $true
}

function Get-ProcessImagePath {
    param([int]$ProcId)
    if ($ProcId -le 0) { return '' }
    $p = $null
    try { $p = Get-CimInstance Win32_Process -Filter ('ProcessId=' + $ProcId) -ErrorAction Stop } catch { return '' }
    if ($null -eq $p) { return '' }
    try { return [string]$p.ExecutablePath } catch { return '' }
}

function Test-SamePath {
    param([string]$Left, [string]$Right)
    if ([string]::IsNullOrEmpty($Left) -or [string]::IsNullOrEmpty($Right)) { return $false }
    if ([string]::Compare($Left, $Right, $true) -eq 0) { return $true }
    try {
        $a = [System.IO.Path]::GetFullPath($Left)
        $b = [System.IO.Path]::GetFullPath($Right)
        return ([string]::Compare($a, $b, $true) -eq 0)
    } catch {
        return $false
    }
}

function Stop-ProcessById {
    param([int]$ProcId)
    if ($ProcId -le 0) { return }
    try {
        & taskkill.exe /F /PID $ProcId 2>&1 | Out-Null
    } catch { }
}

function Wait-ProcessGone {
    param([int]$ProcId, [int]$BudgetMs)
    $elapsed = 0
    while ($elapsed -lt $BudgetMs) {
        if (-not (Test-ProcessAlive -ProcId $ProcId)) { return $true }
        Start-Sleep -Milliseconds 100
        $elapsed = $elapsed + 100
    }
    return (-not (Test-ProcessAlive -ProcId $ProcId))
}

# ---------------------------------------------------------------------------
# session observation (proxy, never proof: P2-02 O6 / N3)
# ---------------------------------------------------------------------------

function Resolve-ServerAddresses {
    param([string]$HostName)
    $addresses = @()
    try {
        $addresses = @([System.Net.Dns]::GetHostAddresses($HostName) | ForEach-Object { $_.ToString() })
    } catch {
        $addresses = @()
    }
    if ($addresses.Count -eq 0) { $addresses = @($HostName) }
    return $addresses
}

function Get-EstablishedRemotes {
    param([int]$ProcId)
    $result = @()
    $lines = @()
    try { $lines = @(& netstat.exe -ano 2>$null) } catch { return $result }
    foreach ($line in $lines) {
        $text = ([string]$line).Trim()
        if ($text -notmatch '^TCP\s') { continue }
        if ($text -notmatch '\sESTABLISHED\s') { continue }
        $parts = $text -split '\s+'
        if ($parts.Count -lt 5) { continue }
        if ($parts[4] -ne [string]$ProcId) { continue }
        $result = $result + $parts[2]
    }
    return $result
}

function Test-EndpointMatch {
    param([string]$Remote, [string[]]$Addresses, [int]$Port)
    if ([string]::IsNullOrEmpty($Remote)) { return $false }
    $idx = $Remote.LastIndexOf(':')
    if ($idx -le 0) { return $false }
    $hostPart = $Remote.Substring(0, $idx)
    $portPart = $Remote.Substring($idx + 1)
    $remotePort = 0
    try { $remotePort = [int]$portPart } catch { return $false }
    if ($remotePort -ne $Port) { return $false }
    foreach ($address in $Addresses) {
        if ($hostPart -ieq $address) { return $true }
    }
    return $false
}

function Wait-SustainedSession {
    param([int]$ProcId, [string[]]$Addresses, [int]$Port, [int]$TimeoutS)
    $deadline = (Get-Date).AddSeconds($TimeoutS)
    $lastRemote = $null
    $lastSeenAt = $null
    $lastBeat = Get-Date
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-ProcessAlive -ProcId $ProcId)) {
            return @{ ok = $false; reason = 'process-exited' }
        }
        $remotes = Get-EstablishedRemotes -ProcId $ProcId
        $hit = $null
        foreach ($remote in $remotes) {
            if (Test-EndpointMatch -Remote $remote -Addresses $Addresses -Port $Port) { $hit = $remote; break }
        }
        if ($null -eq $hit) {
            $lastRemote = $null
            $lastSeenAt = $null
        } else {
            if ($null -ne $lastRemote -and $hit -eq $lastRemote -and $lastSeenAt -ne $null) {
                if (((Get-Date) - $lastSeenAt).TotalSeconds -ge 3) {
                    return @{ ok = $true; endpoint = $hit }
                }
            } else {
                $lastRemote = $hit
                $lastSeenAt = Get-Date
            }
        }
        if (((Get-Date) - $lastBeat).TotalSeconds -ge 1) {
            Write-Journal 'HEARTBEAT' 'phase=WAITING'
            $lastBeat = Get-Date
        }
        Start-Sleep -Milliseconds 500
    }
    return @{ ok = $false; reason = 'timeout' }
}

# ---------------------------------------------------------------------------
# rename primitives (P2-02 section 2.3)
# ---------------------------------------------------------------------------

function Invoke-RenameOnce {
    param([string]$From, [string]$To)
    try {
        [System.IO.File]::Move($From, $To)
        return 0
    } catch {
        $hr = 0
        try { $hr = $_.Exception.HResult } catch { $hr = 0 }
        $code = $hr -band 0xFFFF
        if ($code -eq 0) { $code = 1 }
        return $code
    }
}

function Invoke-RenameWithRetry {
    param([string]$From, [string]$To)
    $delays = @(0, 250, 500, 1000, 2000)
    $last = 0
    foreach ($delay in $delays) {
        if ($delay -gt 0) { Start-Sleep -Milliseconds $delay }
        $code = Invoke-RenameOnce -From $From -To $To
        if ($code -eq 0) { return 0 }
        $last = $code
        Write-Journal 'RENAME_RETRY' ('code=' + $code + ' from=' + (ConvertTo-DetailValue $From) + ' to=' + (ConvertTo-DetailValue $To))
        if ($code -eq 2 -or $code -eq 3) { return $code }
        if ($code -eq 183) { return $code }
        if ($code -ne 5 -and $code -ne 32 -and $code -ne 33) { return $code }
    }
    return $last
}

# ---------------------------------------------------------------------------
# successor spawn (P2-02 section 2.4 - argv rule)
# ---------------------------------------------------------------------------

function Start-ClientProcess {
    param([string]$Path, [string]$ConfigPath, [string]$WorkDir)
    if ($ConfigPath -ne '') {
        $argumentLine = '--config "' + $ConfigPath + '"'
        $proc = Start-Process -FilePath $Path -ArgumentList $argumentLine -WorkingDirectory $WorkDir -WindowStyle Hidden -PassThru -ErrorAction Stop
    } else {
        $proc = Start-Process -FilePath $Path -WorkingDirectory $WorkDir -WindowStyle Hidden -PassThru -ErrorAction Stop
    }
    return [int]$proc.Id
}

# ---------------------------------------------------------------------------
# -Preflight
# ---------------------------------------------------------------------------

function Invoke-Preflight {
    $checks = [ordered]@{
        journal_writable   = $false
        payload_hash       = $false
        size_match         = $false
        old_alive          = $false
        old_path_matches   = $false
        config_path_ok     = $true
        backup_state       = 'none'
    }
    $oldPid = 0
    $ok = $false
    $exitCode = $ExitStaging

    try {
        $m = Read-Manifest
        $oldPid = Get-OptInt -Obj $m -Key 'old_pid' -Default 0
        $canonical = [string]$m.canonical
        $configPath = ''
        if ($null -ne $m.config_path) { $configPath = [string]$m.config_path }

        if (-not (Test-IsRootedPath -Path $canonical)) { throw 'canonical_not_absolute' }
        if ($configPath -ne '' -and -not (Test-IsRootedPath -Path $configPath)) {
            $checks.config_path_ok = $false
            $exitCode = $ExitRefused
            throw 'config_path_relative'
        }

        # (2) journal writable: create/empty it, then continue appending.
        Set-Content -LiteralPath $JournalPath -Value '' -Encoding ASCII -ErrorAction Stop
        $script:JournalCount = 0
        $checks.journal_writable = $true

        # (1) re-hash the staged payload (V3 / H2).
        $staged = $canonical + '.next'
        if (-not (Test-Path -LiteralPath $staged)) { throw 'staged_missing' }
        $hash = Get-Sha256Hex -Path $staged
        if ($hash -ne [string]$m.sha256_payload) {
            $checks.payload_hash = $false
            $exitCode = $ExitStaging
            throw 'payload_hash_mismatch'
        }
        $checks.payload_hash = $true

        $size = -1
        try { $size = (Get-Item -LiteralPath $staged -ErrorAction Stop).Length } catch { $size = -1 }
        if ($size -ne [long]$m.size_payload) {
            $checks.size_match = $false
            $exitCode = $ExitStaging
            throw 'payload_size_mismatch'
        }
        $checks.size_match = $true

        # (4) backup state.
        $backup = $canonical + '.old'
        if (Test-Path -LiteralPath $backup) { $checks.backup_state = 'present' } else { $checks.backup_state = 'absent' }

        # (3) the discovered PID is alive and still owns the canonical image.
        if ($oldPid -le 0) {
            $exitCode = $ExitRefused
            throw 'old_pid_missing'
        }
        if (-not (Test-ProcessAlive -ProcId $oldPid)) {
            $exitCode = $ExitRefused
            throw 'old_process_not_alive'
        }
        $checks.old_alive = $true
        $imagePath = Get-ProcessImagePath -ProcId $oldPid
        if (-not (Test-SamePath -Left $imagePath -Right $canonical)) {
            $exitCode = $ExitRefused
            throw 'old_image_not_canonical'
        }
        $checks.old_path_matches = $true

        $ok = $true
        $exitCode = $ExitOK
    } catch {
        $reason = [string]$_.Exception.Message
        if ($reason -eq 'payload_hash_mismatch' -or $reason -eq 'payload_size_mismatch' -or $reason -eq 'staged_missing' -or $reason -eq 'manifest_missing') {
            $exitCode = $ExitStaging
        } elseif ($reason -eq 'config_path_relative' -or $reason -eq 'canonical_not_absolute') {
            $exitCode = $ExitRefused
        }
        Write-Journal 'PREFLIGHT_REFUSED' ('reason=' + (ConvertTo-DetailValue $reason))
    }

    Write-Journal 'PREFLIGHT' ('ok=' + $ok.ToString().ToLower() + ' old_pid=' + $oldPid)
    $payload = [ordered]@{
        phase   = 'PREFLIGHT'
        ok      = $ok
        checks  = $checks
        old_pid = $oldPid
    }
    Write-Output ($payload | ConvertTo-Json -Compress -Depth 4)
    return $exitCode
}

# ---------------------------------------------------------------------------
# -Launch
# ---------------------------------------------------------------------------

function Invoke-Launch {
    try {
        $null = Read-Manifest
        $stdoutPath = Join-Path $ScriptDir 'p2boot.new.out'
        $stderrPath = Join-Path $ScriptDir 'p2boot.new.err'
        $argumentLine = '-NoProfile -ExecutionPolicy Bypass -File "' + $BootPath + '" -Commit'
        $proc = Start-Process -FilePath 'powershell.exe' -ArgumentList $argumentLine -WorkingDirectory $ScriptDir -WindowStyle Hidden -RedirectStandardOutput $stdoutPath -RedirectStandardError $stderrPath -PassThru -ErrorAction Stop
        $commitPid = [int]$proc.Id
        Write-Journal 'LAUNCH' ('ok=true commit_pid=' + $commitPid)
        $payload = [ordered]@{ phase = 'LAUNCH'; commit_pid = $commitPid }
        Write-Output ($payload | ConvertTo-Json -Compress)
        return $ExitOK
    } catch {
        Write-Journal 'LAUNCH_FAIL' ('err=' + (ConvertTo-DetailValue ([string]$_.Exception.Message)))
        return $ExitRename
    }
}

# ---------------------------------------------------------------------------
# -Commit
# ---------------------------------------------------------------------------

function Invoke-SwapPair {
    param(
        [string]$Canonical,
        [string]$Staged,
        [string]$Backup,
        [int]$OldPid
    )
    $state = @{ ok = $false; killed = $false; code = 0; broken = $false }

    $code = Invoke-RenameWithRetry -From $Canonical -To $Backup
    if ($code -eq 0) {
        Write-Journal 'PARKED' ''
    } else {
        Write-Journal 'RENAME1_FAIL' ('code=' + $code)
        $imagePath = ''
        $alive = Test-ProcessAlive -ProcId $OldPid
        if ($alive) { $imagePath = Get-ProcessImagePath -ProcId $OldPid }
        $lockIsImage = ($alive -and (Test-SamePath -Left $imagePath -Right $Canonical))
        $lockCode = ($code -eq 5 -or $code -eq 32 -or $code -eq 33)
        if ($lockCode -and $lockIsImage) {
            Write-Journal 'SWAP_MODE' 'mode=killfirst'
            Stop-ProcessById -ProcId $OldPid
            $gone = Wait-ProcessGone -ProcId $OldPid -BudgetMs 2000
            if (-not $gone) {
                Write-Journal 'KILLFIRST_FAIL' ('pid=' + $OldPid)
                $state.code = $code
                return $state
            }
            Write-Journal 'OLD_KILLED' ('pid=' + $OldPid + ' mode=killfirst')
            $state.killed = $true
            $code = Invoke-RenameWithRetry -From $Canonical -To $Backup
            if ($code -ne 0) {
                Write-Journal 'RENAME1_FAIL' ('code=' + $code + ' mode=killfirst')
                $state.code = $code
                return $state
            }
            Write-Journal 'PARKED' 'mode=killfirst'
        } else {
            $state.code = $code
            return $state
        }
    }

    $code = Invoke-RenameWithRetry -From $Staged -To $Canonical
    if ($code -ne 0) {
        Write-Journal 'RENAME2_FAIL' ('code=' + $code)
        $restore = Invoke-RenameWithRetry -From $Backup -To $Canonical
        if ($restore -ne 0) {
            Write-Journal 'COMPENSATE_FAIL' ('code=' + $restore)
            $state.broken = $true
            $state.code = $code
            return $state
        }
        Write-Journal 'COMPENSATED' ''
        $state.code = $code
        return $state
    }
    Write-Journal 'PLACED' ''
    $state.ok = $true
    $state.code = 0
    return $state
}

function Invoke-Rollback {
    param(
        [string]$Reason,
        [string]$Canonical,
        [string]$Backup,
        [int]$NewPid,
        [string]$OldSha,
        [int]$OldPid,
        [string]$ConfigPath,
        [string[]]$Addresses,
        [int]$Port,
        [int]$N2
    )
    $late = -not (Test-ProcessAlive -ProcId $OldPid)
    Write-Journal 'ROLLBACK' ('reason=' + (ConvertTo-DetailValue $Reason) + ' late=' + $late.ToString().ToLower())

    # R-E1 / R-L1: the successor must be gone before any file is moved.
    if ($NewPid -gt 0 -and (Test-ProcessAlive -ProcId $NewPid)) {
        Stop-ProcessById -ProcId $NewPid
        $gone = Wait-ProcessGone -ProcId $NewPid -BudgetMs 1000
        if (-not $gone) {
            Write-Journal 'ROLLBACK_BLOCKED' ('pid=' + $NewPid)
            return $ExitRollbackFail
        }
    }

    # R-E3 / R-L2: quarantine the new image, never delete it.
    if (Test-Path -LiteralPath $Canonical) {
        $quarantine = $Canonical + '.next.dead.' + (Get-EpochSeconds).ToString()
        $code = Invoke-RenameWithRetry -From $Canonical -To $quarantine
        if ($code -ne 0) {
            Write-Journal 'ROLLBACK_QUARANTINE_FAIL' ('code=' + $code)
            return $ExitRollbackFail
        }
        Write-Journal 'NEW_QUARANTINED' ('to=' + (ConvertTo-DetailValue $quarantine))
    }

    # R-E4 / R-L3: restore the last-known-good image.
    $code = Invoke-RenameWithRetry -From $Backup -To $Canonical
    if ($code -ne 0) {
        Write-Journal 'ROLLBACK_RESTORE_FAIL' ('code=' + $code)
        return $ExitRollbackFail
    }

    # R-E5 / R-L4: verify the restored bytes against the journaled digest.
    $restoredHash = ''
    try { $restoredHash = Get-Sha256Hex -Path $Canonical } catch { $restoredHash = '' }
    if ($restoredHash -eq '' -or $restoredHash -ne $OldSha) {
        $short = 'none'
        if ($restoredHash.Length -ge 16) { $short = $restoredHash.Substring(0, 16) }
        Write-Journal 'ROLLBACK_HASH_BAD' ('expected=' + $OldSha.Substring(0, 16) + ' actual=' + $short)
        return $ExitRollbackFail
    }

    if ($late) {
        $restoredPid = 0
        try {
            $restoredPid = Start-ClientProcess -Path $Canonical -ConfigPath $ConfigPath -WorkDir $ScriptDir
        } catch {
            Write-Journal 'ROLLBACK_START_FAIL' ('err=' + (ConvertTo-DetailValue ([string]$_.Exception.Message)))
            return $ExitRollbackFail
        }
        $wait = Wait-SustainedSession -ProcId $restoredPid -Addresses $Addresses -Port $Port -TimeoutS $N2
        if ($wait.ok) {
            Write-Journal 'ROLLED_BACK_LATE' ('restored_pid=' + $restoredPid)
            return $ExitRollback
        }
        Write-Journal 'ROLLBACK_UNVERIFIED' ('reason=' + (ConvertTo-DetailValue ([string]$wait.reason)))
        return $ExitRollbackFail
    }

    Write-Journal 'ROLLED_BACK_EARLY' ('old_pid=' + $OldPid + ' old_alive=1')
    return $ExitRollback
}

function Invoke-Commit {
    $script:JournalCount = -1
    $canonical = ''
    $staged = ''
    $backup = ''
    $oldPid = 0
    $newPid = 0
    $oldSha = ''
    $configPath = ''
    $nRollback = 60
    $lDwell = 10
    $port = 0
    $addresses = @()

    Write-Journal 'COMMIT_START' ('pid=' + $PID + ' mode=swap')

    try {
        $m = Read-Manifest
        $canonical = [string]$m.canonical
        $staged = $canonical + '.next'
        $backup = $canonical + '.old'
        $oldPid = Get-OptInt -Obj $m -Key 'old_pid' -Default 0
        $nRollback = Get-OptInt -Obj $m -Key 'n_rollback_s' -Default 60
        $lDwell = Get-OptInt -Obj $m -Key 'l_dwell_s' -Default 10
        $port = Get-OptInt -Obj $m -Key 'server_port' -Default 0
        if ($null -ne $m.config_path) { $configPath = [string]$m.config_path }
        $addresses = Resolve-ServerAddresses -HostName ([string]$m.server_host)
    } catch {
        Write-Journal 'MANIFEST_BAD' ('err=' + (ConvertTo-DetailValue ([string]$_.Exception.Message)))
        return $ExitStaging
    }

    # SW-10: self-hash, journaled for audit; a convenience check, not a defence.
    $bootHash = ''
    try { $bootHash = Get-Sha256Hex -Path $BootPath } catch { $bootHash = '' }
    if ($bootHash -ne '') {
        Write-Journal 'BOOT_SHA256' $bootHash.Substring(0, 16)
    }
    if ($bootHash -eq '' -or $bootHash -ne [string]$m.sha256_boot) {
        Write-Journal 'BOOT_HASH_MISMATCH' ''
        return $ExitStaging
    }

    # SW-11: quarantine a stale backup; never overwrite the last-known-good.
    if (Test-Path -LiteralPath $backup) {
        $quarantine = $backup + '.' + (Get-EpochSeconds).ToString()
        $code = Invoke-RenameWithRetry -From $backup -To $quarantine
        if ($code -ne 0) {
            Write-Journal 'OLD_LOCKED' ('code=' + $code)
            return $ExitRename
        }
        Write-Journal 'OLD_QUARANTINED' ('to=' + (ConvertTo-DetailValue $quarantine))
    }

    # SW-12: digest of the image that is about to be parked (H4).
    try {
        $oldSha = Get-Sha256Hex -Path $canonical
    } catch {
        Write-Journal 'OLD_HASH_FAIL' ''
        return $ExitRename
    }
    Write-Journal 'OLD_SHA256' $oldSha.Substring(0, 16)
    try { Set-Content -LiteralPath $OldShaPath -Value $oldSha -Encoding ASCII -ErrorAction Stop } catch { }

    # SW-13 / SW-14: the rename pair.
    $pair = Invoke-SwapPair -Canonical $canonical -Staged $staged -Backup $backup -OldPid $oldPid
    if (-not $pair.ok) {
        if ($pair.broken) {
            Write-Journal 'INVARIANT_BROKEN' ''
            return $ExitRename
        }
        # A failed REN1 moved nothing; a failed REN2 was compensated. Either way
        # the canonical path is intact or restored, so no rollback is needed.
        return $ExitRename
    }

    # SW-15: the file now in place must still be the verified bytes (H5).
    $placedHash = ''
    try { $placedHash = Get-Sha256Hex -Path $canonical } catch { $placedHash = '' }
    if ($placedHash -ne [string]$m.sha256_payload) {
        $actual = 'none'
        if ($placedHash.Length -ge 16) { $actual = $placedHash.Substring(0, 16) }
        Write-Journal 'HASHMISMATCH_POSTRENAME' ('expected=' + ([string]$m.sha256_payload).Substring(0, 16) + ' actual=' + $actual)
        return (Invoke-Rollback -Reason 'post-rename-hash' -Canonical $canonical -Backup $backup -NewPid 0 -OldSha $oldSha -OldPid $oldPid -ConfigPath $configPath -Addresses $addresses -Port $port -N2 20)
    }

    # SW-16: start the successor from the canonical path, CWD = exe directory.
    try {
        $newPid = Start-ClientProcess -Path $canonical -ConfigPath $configPath -WorkDir $ScriptDir
    } catch {
        Write-Journal 'START_FAIL' ('err=' + (ConvertTo-DetailValue ([string]$_.Exception.Message)))
        return (Invoke-Rollback -Reason 'start-failed' -Canonical $canonical -Backup $backup -NewPid 0 -OldSha $oldSha -OldPid $oldPid -ConfigPath $configPath -Addresses $addresses -Port $port -N2 20)
    }
    Write-Journal 'NEW_STARTED' ('pid=' + $newPid)

    # SW-17: patience deadline for a sustained session (proxy, not proof).
    $wait = Wait-SustainedSession -ProcId $newPid -Addresses $addresses -Port $port -TimeoutS $nRollback
    if (-not $wait.ok) {
        Write-Journal 'NEW_NOT_ESTABLISHED' ('reason=' + (ConvertTo-DetailValue ([string]$wait.reason)))
        return (Invoke-Rollback -Reason 'no-sustained-session' -Canonical $canonical -Backup $backup -NewPid $newPid -OldSha $oldSha -OldPid $oldPid -ConfigPath $configPath -Addresses $addresses -Port $port -N2 20)
    }
    Write-Journal 'NEW_ESTABLISHED' ('endpoint=' + (ConvertTo-DetailValue ([string]$wait.endpoint)))

    # SW-18: kill the old process. Never /T: the commit is its descendant.
    Stop-ProcessById -ProcId $oldPid
    $gone = Wait-ProcessGone -ProcId $oldPid -BudgetMs 500
    if (-not $gone) {
        Stop-ProcessById -ProcId $oldPid
        $gone = Wait-ProcessGone -ProcId $oldPid -BudgetMs 500
    }
    if (-not $gone) {
        Write-Journal 'OLDPID_ALIVE' ('pid=' + $oldPid)
        return $ExitOldPID
    }
    Write-Journal 'OLD_KILLED' ('pid=' + $oldPid)

    # SW-19: dwell with the successor alive and the predecessor absent.
    $deadline = (Get-Date).AddSeconds($lDwell)
    $lastBeat = Get-Date
    while ((Get-Date) -lt $deadline) {
        if (-not (Test-ProcessAlive -ProcId $newPid)) {
            Write-Journal 'NEW_DIED_IN_DWELL' ('pid=' + $newPid)
            return (Invoke-Rollback -Reason 'new-died-in-dwell' -Canonical $canonical -Backup $backup -NewPid $newPid -OldSha $oldSha -OldPid $oldPid -ConfigPath $configPath -Addresses $addresses -Port $port -N2 20)
        }
        if (((Get-Date) - $lastBeat).TotalSeconds -ge 1) {
            Write-Journal 'HEARTBEAT' 'phase=DWELL'
            $lastBeat = Get-Date
        }
        Start-Sleep -Milliseconds 500
    }
    Write-Journal 'DWELL_OK' ('new_pid=' + $newPid)

    # SW-20: done.
    Write-Journal 'DONE' ('sha256=' + ([string]$m.sha256_payload).Substring(0, 16) + ' old_sha256=' + $oldSha.Substring(0, 16))
    return $ExitOK
}

# ---------------------------------------------------------------------------
# dispatch
# ---------------------------------------------------------------------------

if ($Commit) { exit (Invoke-Commit) }
if ($Launch) { exit (Invoke-Launch) }
if ($Preflight) { exit (Invoke-Preflight) }
Write-Output '{"phase":"NONE","ok":false}'
exit $ExitRefused
