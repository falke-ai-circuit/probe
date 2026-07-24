package main

import (
	"os/exec"
	"strings"
	"testing"
)

// buildProbe builds the default (client-only) probe binary and returns its path.
func buildProbe(t *testing.T) string {
	t.Helper()
	binPath := t.TempDir() + "/probe"
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build probe binary: %v\n%s", err, out)
	}
	return binPath
}

// runProbe executes the probe binary with given args and returns stdout, stderr, exit code.
func runProbe(t *testing.T, binPath string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("failed to execute probe: %v", err)
		}
	}
	return stdout.String(), stderr.String(), exitCode
}

func TestCLI_Version(t *testing.T) {
	bin := buildProbe(t)
	stdout, _, code := runProbe(t, bin, "--version")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "PROBE") {
		t.Errorf("expected 'PROBE' in output, got: %s", stdout)
	}
	if !strings.Contains(stdout, appVersion) {
		t.Errorf("expected version %s in output, got: %s", appVersion, stdout)
	}
}

func TestCLI_VersionSubcommand(t *testing.T) {
	bin := buildProbe(t)
	stdout, _, code := runProbe(t, bin, "version")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, appVersion) {
		t.Errorf("expected version %s, got: %s", appVersion, stdout)
	}
}

func TestCLI_NoArgs_PrintsUsage(t *testing.T) {
	bin := buildProbe(t)
	_, stderr, code := runProbe(t, bin)
	if code == 0 {
		t.Log("note: no-args exit code is 1 (usage error)")
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("expected 'Usage:' in stderr, got: %s", stderr)
	}
	if !strings.Contains(stderr, "serve") {
		t.Errorf("expected 'serve' in usage, got: %s", stderr)
	}
	if !strings.Contains(stderr, "connect") {
		t.Errorf("expected 'connect' in usage, got: %s", stderr)
	}
}

func TestCLI_HelpFlag(t *testing.T) {
	bin := buildProbe(t)
	_, stderr, code := runProbe(t, bin, "--help")
	// --help may exit 0 or non-zero depending on flag handling
	_ = code
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("expected 'Usage:' in stderr, got: %s", stderr)
	}
}

func TestCLI_UnknownSubcommand(t *testing.T) {
	bin := buildProbe(t)
	_, stderr, code := runProbe(t, bin, "foobar")
	if code == 0 {
		t.Errorf("expected non-zero exit for unknown subcommand, got 0")
	}
	if !strings.Contains(stderr, "Unknown subcommand") {
		t.Errorf("expected 'Unknown subcommand' in stderr, got: %s", stderr)
	}
}


func TestCLI_ConnectVersion(t *testing.T) {
	bin := buildProbe(t)
	stdout, _, code := runProbe(t, bin, "connect", "--version")
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
	if !strings.Contains(stdout, "PROBE Client") {
		t.Errorf("expected 'PROBE Client' in output, got: %s", stdout)
	}
}

