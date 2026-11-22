package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// TestEveryClientBuildDispatchesGuardian is the regression pin for the P8-04
// field defect: the guardian was wired into cmd/probe only, while the deployed
// agent is cmd/probe-client. A detached exec on the field binary got a usage
// banner instead of a guardian, so cleanup never existed.
//
// It fails if either client entrypoint stops dispatching `guardian` or if the
// delegating file disappears.
func TestEveryClientBuildDispatchesGuardian(t *testing.T) {
	builds := map[string]string{
		"cmd/probe":        "case \"guardian\":",
		"cmd/probe-client": "os.Args[1] == \"guardian\"",
	}
	for dir, marker := range builds {
		mainPath := filepath.Join("..", "..", dir, "main.go")
		b, err := os.ReadFile(mainPath)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		src := string(b)
		if !strings.Contains(src, marker) {
			t.Fatalf("%s/main.go no longer dispatches the guardian subcommand (missing %q)", dir, marker)
		}
		if !strings.Contains(src, "runGuardian(") {
			t.Fatalf("%s/main.go dispatches guardian but does not call runGuardian", dir)
		}
		gpath := filepath.Join("..", "..", dir, "guardian.go")
		gb, err := os.ReadFile(gpath)
		if err != nil {
			t.Fatalf("%s/guardian.go missing: %v", dir, err)
		}
		if !strings.Contains(string(gb), "RunGuardianCLI") {
			t.Fatalf("%s/guardian.go does not delegate to RunGuardianCLI (drifted copy)", dir)
		}
	}
}

// TestGuardianSelftestContract pins the selftest output the launch path trusts.
func TestGuardianSelftestContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs")
	}
	dir := t.TempDir()
	good := filepath.Join(dir, "good")
	if err := os.WriteFile(good, []byte("#!/bin/sh\necho guardian-ok\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkGuardianSupport(good); err != nil {
		t.Fatalf("good stub rejected: %v", err)
	}

	// A field binary without the subcommand prints usage and exits non-zero —
	// exactly the probe-client failure this unit had to fix.
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\necho 'Unknown subcommand: guardian' >&2\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	err := checkGuardianSupport(bad)
	if err == nil {
		t.Fatal("binary without guardian was accepted")
	}

	// Exit 0 without the marker is not proof either.
	silent := filepath.Join(dir, "silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := checkGuardianSupport(silent); err == nil {
		t.Fatal("marker-less selftest was accepted")
	}
}

// TestDetachedLaunchRefusedWithoutGuardianEntry proves the launch fails loudly
// and is not reported as a successful job: no guardian means no executor.
func TestDetachedLaunchRefusedWithoutGuardianEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub")
	}
	root := t.TempDir()
	bad := filepath.Join(root, "noguardian")
	if err := os.WriteFile(bad, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	prev := executablePath
	executablePath = func() (string, error) { return bad, nil }
	defer func() { executablePath = prev }()

	a := &Agent{cfg: Config{StateDir: filepath.Join(root, "state")}}
	params, _ := json.Marshal(protocol.ExecParams{
		Command: "sleep 5",
		Detach:  true,
		OnDeath: "true",
	})
	resp := a.handleExec(protocol.Envelope{ID: "p804", Type: protocol.TypeExec, Params: params})

	if resp.Type != protocol.TypeError || resp.Error == nil {
		t.Fatalf("launch on a guardian-less binary returned %q (result=%s), want a typed error", resp.Type, resp.Result)
	}
	if resp.Error.Code != protocol.ErrGuardianUnavailable {
		t.Fatalf("error code = %q, want %q", resp.Error.Code, protocol.ErrGuardianUnavailable)
	}
	if !strings.Contains(resp.Error.Message, ErrGuardianUnsupported.Error()) {
		t.Fatalf("error message %q does not name the missing entry point", resp.Error.Message)
	}
	if !errors.Is(ErrGuardianUnsupported, ErrGuardianUnsupported) {
		t.Fatal("unreachable")
	}
	// The refusal must not leave a spool that claims a running job.
	jobsDir := filepath.Join(root, "state", "jobs")
	if entries, err := os.ReadDir(jobsDir); err == nil {
		for _, e := range entries {
			st, rerr := readState(filepath.Join(jobsDir, e.Name()))
			if rerr == nil && st.Phase != jobPhaseStarting {
				t.Fatalf("refused launch left phase %q in %s", st.Phase, e.Name())
			}
		}
	}
}
