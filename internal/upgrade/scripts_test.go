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
