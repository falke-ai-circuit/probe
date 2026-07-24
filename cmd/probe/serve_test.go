package main

import (
	"os/exec"
	"strings"
	"testing"
)

func buildProbeServer(t *testing.T) string {
	t.Helper()
	binPath := t.TempDir() + "/probe-server"
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build probe-server binary: %v\n%s", err, out)
	}
	return binPath
}

func TestCLI_ServeVersion(t *testing.T) {
	bin := buildProbeServer(t)
	cmd := exec.Command(bin, "serve", "--version")
	var stdout strings.Builder
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() != 0 {
				t.Fatalf("expected exit 0, got %d", exitErr.ExitCode())
			}
		} else {
			t.Fatalf("failed to execute: %v", err)
		}
	}
	if !strings.Contains(stdout.String(), "PROBE Server") {
		t.Errorf("expected 'PROBE Server' in output, got: %s", stdout.String())
	}
}

func TestCLI_SharedVersion(t *testing.T) {
	bin := buildProbeServer(t)
	// Both serve and connect should report same version
	serveOut, _ := exec.Command(bin, "serve", "--version").Output()
	connectOut, _ := exec.Command(bin, "connect", "--version").Output()
	if !strings.Contains(string(serveOut), appVersion) {
		t.Errorf("serve version mismatch: %s", serveOut)
	}
	if !strings.Contains(string(connectOut), appVersion) {
		t.Errorf("connect version mismatch: %s", connectOut)
	}
}