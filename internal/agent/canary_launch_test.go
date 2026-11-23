package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRecoverCanonicalBinaryRestoresOld is the startup-recovery guard: when the
// canonical path is missing (crash between rename(canonical -> .old) and
// rename(.next -> canonical)) and <canonical>.old exists, the backup must be put
// back so the host still has a runnable agent binary.
func TestRecoverCanonicalBinaryRestoresOld(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	backup := canonical + ".old"
	if err := os.WriteFile(backup, []byte("old-binary"), 0755); err != nil {
		t.Fatal(err)
	}

	restored, err := RecoverCanonicalBinary(canonical)
	if err != nil {
		t.Fatalf("RecoverCanonicalBinary: %v", err)
	}
	if !restored {
		t.Fatal("restored = false, want true (canonical missing + .old present)")
	}
	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("canonical not restored: %v", err)
	}
	if string(got) != "old-binary" {
		t.Fatalf("canonical content = %q, want %q", got, "old-binary")
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf(".old still exists after restore (err=%v)", err)
	}
}

// TestRecoverCanonicalBinaryNoopWhenCanonicalExists proves recovery never
// clobbers a healthy canonical binary with a stale backup.
func TestRecoverCanonicalBinaryNoopWhenCanonicalExists(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	if err := os.WriteFile(canonical, []byte("current"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(canonical+".old", []byte("stale"), 0755); err != nil {
		t.Fatal(err)
	}

	restored, err := RecoverCanonicalBinary(canonical)
	if err != nil {
		t.Fatalf("RecoverCanonicalBinary: %v", err)
	}
	if restored {
		t.Fatal("restored = true, want false when canonical exists")
	}
	got, _ := os.ReadFile(canonical)
	if string(got) != "current" {
		t.Fatalf("canonical was overwritten: %q", got)
	}
}

// TestRecoverCanonicalBinaryNoBackupIsNoop: nothing to restore, no error, and no
// binary invented.
func TestRecoverCanonicalBinaryNoBackupIsNoop(t *testing.T) {
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	restored, err := RecoverCanonicalBinary(canonical)
	if err != nil || restored {
		t.Fatalf("restored=%v err=%v, want false/nil", restored, err)
	}
	if _, err := os.Stat(canonical); !os.IsNotExist(err) {
		t.Fatalf("canonical should still be missing, err=%v", err)
	}
}

// TestStartCanaryCapturesOutput proves defect 2 is fixed: the canary's stdout and
// stderr land in <canonical>.canary.log, truncated at the start of each update.
// The staged "binary" is a shell script that prints to both streams and exits.
func TestStartCanaryCapturesOutput(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to simulate the staged binary")
	}
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-client")
	next := canonical + ".next"
	script := "#!/bin/sh\necho \"canary-argv:$1|$2|$3|$4|$5|$6\"\necho \"canary-stderr\" >&2\nexit 0\n"
	if err := os.WriteFile(next, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	// Seed a previous run's content to prove truncation.
	logPath := CanaryLogPath(canonical)
	if err := os.WriteFile(logPath, []byte("STALE-PREVIOUS-UPDATE-OUTPUT\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// A canary that exits is a failed update (P9-19): StartCanary must report an
	// error, so the server records a failure instead of "waiting for new agent
	// to connect" forever. The capture assertions below run on the same log.
	if _, gotLog, err := StartCanary(next, canonical, "/opt/probe/cfg.json", 1234); err == nil {
		t.Fatal("StartCanary returned nil error for a canary that exited")
	} else if gotLog != logPath {
		t.Fatalf("log path = %q, want %q", gotLog, logPath)
	}

	deadline := time.Now().Add(5 * time.Second)
	var content string
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(logPath)
		if err == nil {
			content = string(b)
			if strings.Contains(content, "canary-stderr") && strings.Contains(content, "canary-argv:") {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if strings.Contains(content, "STALE-PREVIOUS-UPDATE-OUTPUT") {
		t.Fatalf("canary log was not truncated; content=%q", content)
	}
	// The logged argv is the launched contract itself: connect + config + canary flags.
	wantArgv := "canary-argv:connect|--config|/opt/probe/cfg.json|--canary|--canary-old-pid|1234"
	if !strings.Contains(content, wantArgv) {
		t.Fatalf("launched argv not captured; want %q in content=%q", wantArgv, content)
	}
	if !strings.Contains(content, "canary-stderr") {
		t.Fatalf("stderr not captured; content=%q", content)
	}
}
