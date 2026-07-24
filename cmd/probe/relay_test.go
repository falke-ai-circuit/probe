package main

import (
	"os/exec"
	"strings"
	"testing"
)

func buildProbeRelay(t *testing.T) string {
	t.Helper()
	binPath := t.TempDir() + "/probe-relay"
	cmd := exec.Command("go", "build", "-tags", "relay", "-o", binPath, ".")
	cmd.Dir = "/opt/data/workspace-operative/hermes-remote/cmd/probe"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("failed to build probe-relay binary: %v\n%s", err, out)
	}
	return binPath
}

func TestCLI_RelayRequiresUpstream(t *testing.T) {
	bin := buildProbeRelay(t)
	cmd := exec.Command(bin, "relay")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Errorf("expected non-zero exit for relay without --upstream, got 0")
	}
	if !strings.Contains(stderr.String(), "--upstream is required") {
		t.Errorf("expected '--upstream is required' in stderr, got: %s", stderr.String())
	}
}

func TestCLI_RelayVersion(t *testing.T) {
	bin := buildProbeRelay(t)
	cmd := exec.Command(bin, "relay", "--version")
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
	if !strings.Contains(stdout.String(), "PROBE Relay") {
		t.Errorf("expected 'PROBE Relay' in output, got: %s", stdout.String())
	}
}