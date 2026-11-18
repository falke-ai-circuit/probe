package upgrade

import (
	"bytes"
	"strings"
	"testing"
)

func TestScriptsAreASCIIWithoutBOM(t *testing.T) {
	for name, script := range map[string][]byte{
		BootScriptFile:     BootScript(),
		DiscoverScriptFile: DiscoverScript(),
	} {
		if len(script) == 0 {
			t.Fatalf("%s is empty", name)
		}
		if !IsASCII(script) {
			t.Fatalf("%s contains a non-ASCII byte (PowerShell 5.1 would read it as ANSI)", name)
		}
		if bytes.HasPrefix(script, []byte{0xEF, 0xBB, 0xBF}) {
			t.Fatalf("%s starts with a UTF-8 BOM", name)
		}
		if bytes.Contains(script, []byte("\r\n")) {
			t.Fatalf("%s contains CRLF; the embed must stay byte-stable", name)
		}
	}
}

func TestBootScriptContract(t *testing.T) {
	script := string(BootScript())
	must := []string{
		"-Preflight", "-Launch", "-Commit",
		"p2boot.journal", "p2boot.manifest.json", "p2boot.oldsha",
		"COMMIT_START", "BOOT_SHA256", "OLD_SHA256", "OLD_QUARANTINED",
		"PARKED", "PLACED", "HASHMISMATCH_POSTRENAME", "NEW_STARTED",
		"NEW_ESTABLISHED", "OLD_KILLED", "OLDPID_ALIVE", "DONE",
		"ROLLED_BACK_EARLY", "ROLLED_BACK_LATE", "ROLLBACK_BLOCKED",
		"taskkill.exe /F /PID",
		"-RedirectStandardOutput", "-WindowStyle Hidden",
		"JOURNAL_FULL",
	}
	for _, needle := range must {
		if !strings.Contains(script, needle) {
			t.Errorf("p2boot.ps1 is missing %q", needle)
		}
	}
	// Forbidden: /T kills the commit itself, which is a descendant of the old
	// agent (P2-02 section 2.5).
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "taskkill") && strings.Contains(line, "/T") {
			t.Errorf("taskkill must never use /T: %q", line)
		}
	}
	// Forbidden client flags: the successor must not receive the broken K9 argv.
	for _, bad := range []string{"--canary", "--canary-old-pid"} {
		if strings.Contains(script, bad) {
			t.Errorf("p2boot.ps1 mentions the forbidden client flag %q", bad)
		}
	}
}

// scriptRegion returns the text from the first line containing from up to (not
// including) the first following line containing to. It exists so the gate
// tests below assert on a whole branch body, not on a bare event string.
func scriptRegion(t *testing.T, script, from, to string) string {
	t.Helper()
	start := strings.Index(script, from)
	if start < 0 {
		t.Fatalf("p2boot.ps1 does not contain the anchor %q", from)
	}
	rest := script[start:]
	if to == "" {
		return rest
	}
	end := strings.Index(rest, to)
	if end < 0 {
		t.Fatalf("p2boot.ps1 has %q but not the closing anchor %q", from, to)
	}
	return rest[:end]
}

func requireAll(t *testing.T, region, label string, musts []string) {
	t.Helper()
	for _, needle := range musts {
		if !strings.Contains(region, needle) {
			t.Errorf("%s is missing %q (the branch was narrowed or removed)", label, needle)
		}
	}
}

// F2: SW-15 must never start a successor on a hash mismatch. A test that only
// compares string offsets passes with the gate dead, so this asserts the guard
// expression, the journal event and the rollback call inside the SW-15 body.
func TestBootScriptSW15GateBites(t *testing.T) {
	script := string(BootScript())
	block := scriptRegion(t, script, "# SW-15:", "# SW-16:")
	requireAll(t, block, "SW-15 block", []string{
		"$placedHash = Get-Sha256Hex -Path $canonical",
		"if ($placedHash -ne [string]$m.sha256_payload) {",
		"Write-Journal 'HASHMISMATCH_POSTRENAME'",
		"return (Invoke-Rollback -Reason 'post-rename-hash'",
	})
	// The successor start must live after the gate, never inside it.
	if strings.Contains(block, "Start-ClientProcess") {
		t.Error("SW-15 block must not start a process; a mismatch rolls back instead of starting")
	}
}

// F3: the rollback restore must be verified against the digest SW-12 journaled.
// Removing the branch previously left the suite green.
func TestBootScriptRollbackHashGateBites(t *testing.T) {
	script := string(BootScript())
	block := scriptRegion(t, script, "# R-E5 / R-L4:", "if ($late) {")
	requireAll(t, block, "rollback hash block", []string{
		"$restoredHash = Get-Sha256Hex -Path $Canonical",
		"if ($restoredHash -eq '' -or $restoredHash -ne $OldSha) {",
		"Write-Journal 'ROLLBACK_HASH_BAD'",
		"return $ExitRollbackFail",
	})
}

// F1: SW-13 code 183 must journal OLD_RACE, re-quarantine <C>.old and retry,
// and both REN1 sites must go through that branch.
func TestBootScriptOldRaceBranchBites(t *testing.T) {
	script := string(BootScript())
	block := scriptRegion(t, script, "function Invoke-ParkCanonical", "function Start-ClientProcess")
	requireAll(t, block, "OLD_RACE branch", []string{
		"if ($code -ne 183) { return $code }",
		"Write-Journal 'OLD_RACE'",
		"Invoke-RenameWithRetry -From $Backup -To $quarantine",
		"Invoke-RenameWithRetry -From $Canonical -To $Backup",
	})
	if n := strings.Count(script, "Invoke-ParkCanonical -Canonical $Canonical -Backup $Backup"); n < 2 {
		t.Errorf("both REN1 sites must route through Invoke-ParkCanonical, found %d", n)
	}
}

// F4: SW-07 must hash the canonical image before the boundary.
func TestPreflightHashesCanonical(t *testing.T) {
	script := string(BootScript())
	block := scriptRegion(t, script, "function Invoke-Preflight", "function Invoke-Launch")
	requireAll(t, block, "preflight block", []string{
		"old_hash           = $false",
		"Test-Path -LiteralPath $canonical",
		"$oldHash = Get-Sha256Hex -Path $canonical",
		"$checks.old_hash = $true",
		"Write-Journal 'OLD_SHA256'",
	})
	for _, reason := range []string{"canonical_missing", "canonical_unreadable"} {
		if !strings.Contains(block, reason) {
			t.Errorf("preflight must refuse on %s", reason)
		}
	}
}

func TestBootScriptRollbackOrder(t *testing.T) {
	script := string(BootScript())
	// Every rollback line is written before the action it describes.
	idxRollback := strings.Index(script, "Write-Journal 'ROLLBACK'")
	idxRestore := strings.Index(script, "R-E4 / R-L3")
	if idxRollback < 0 || idxRestore < 0 || idxRollback > idxRestore {
		t.Fatalf("ROLLBACK must be journaled before the restore (rollback=%d restore=%d)", idxRollback, idxRestore)
	}
	// The placed binary is hashed (H5) before the successor is started.
	idxHash := strings.Index(script, "HASHMISMATCH_POSTRENAME")
	idxStart := strings.Index(script, "NEW_STARTED")
	if idxHash < 0 || idxStart < 0 || idxHash > idxStart {
		t.Fatalf("H5 must run before SW-16 (hash=%d start=%d)", idxHash, idxStart)
	}
}

func TestDiscoverScriptContract(t *testing.T) {
	script := string(DiscoverScript())
	for _, needle := range []string{"Win32_Process", "ExecutablePath", "CommandLine", "ParentProcessId", "ConvertTo-Json"} {
		if !strings.Contains(script, needle) {
			t.Errorf("p2discover.ps1 is missing %q", needle)
		}
	}
	if strings.Contains(script, "taskkill") {
		t.Error("discovery must not touch processes")
	}
}

func TestScriptDigestsAreStable(t *testing.T) {
	if BootScriptSHA256() != SHA256Hex(BootScript()) {
		t.Fatal("BootScriptSHA256 must be the digest of the embedded bytes")
	}
	if DiscoverScriptSHA256() != SHA256Hex(DiscoverScript()) {
		t.Fatal("DiscoverScriptSHA256 must be the digest of the embedded bytes")
	}
	if BootScriptSHA256() == DiscoverScriptSHA256() {
		t.Fatal("scripts must not be identical")
	}
}
