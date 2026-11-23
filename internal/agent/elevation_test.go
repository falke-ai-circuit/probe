package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/platform"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-03: exec{as:"SYSTEM"} is a declared-and-verified principal, never an
// escalation primitive. These tests are written so they FAIL on the two
// defects the unit exists to close: a silent unelevated success, and an exec
// response without the declared envelope.

func stubPrincipal(t *testing.T, p PrincipalInfo) {
	t.Helper()
	prev := principalFn
	principalFn = func() PrincipalInfo { return p }
	t.Cleanup(func() { principalFn = prev })
}

func execEnvelope(t *testing.T, fields map[string]interface{}) protocol.Envelope {
	t.Helper()
	raw, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return protocol.Envelope{ID: "p803", Type: protocol.TypeExec, Params: raw}
}

// (a) A non-SYSTEM agent REFUSES as:"SYSTEM" with a typed error naming the
// prerequisite, and the command is never spawned — proven by the marker file
// the command would have created.
func TestExecSystemRefusedOnNonSystemPrincipal(t *testing.T) {
	stubPrincipal(t, PrincipalInfo{User: "flk", IsSystem: false, Integrity: "medium", TokenFiltered: true})
	a := &Agent{plat: platform.New("p803-test")}
	marker := filepath.Join(t.TempDir(), "must-not-exist")

	resp := a.handleExec(execEnvelope(t, map[string]interface{}{
		"command": "touch " + marker,
		"as":      "SYSTEM",
	}))

	if resp.Type != protocol.TypeError || resp.Error == nil {
		t.Fatalf("as:SYSTEM on a non-SYSTEM agent returned %q (result=%s), want a typed error",
			resp.Type, resp.Result)
	}
	if resp.Error.Code != protocol.ErrElevationUnavailable {
		t.Fatalf("error code = %q, want %q", resp.Error.Code, protocol.ErrElevationUnavailable)
	}
	if resp.Error.Detail["principal"] != "flk" {
		t.Fatalf("detail.principal = %v, want the observed principal \"flk\"", resp.Error.Detail["principal"])
	}
	if _, ok := resp.Error.Detail["remediation"]; !ok {
		t.Fatal("refusal does not name the remediation; the operator is left guessing")
	}
	if !strings.Contains(resp.Error.Message, "service") {
		t.Fatalf("refusal message %q does not name the service prerequisite", resp.Error.Message)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("refused exec still spawned the command (marker file exists) — the refusal must precede the spawn")
	}
}

// The other half of (a): a SYSTEM principal runs the command, and the response
// is the SAME envelope shape as a normal exec, with principal as evidence.
func TestExecSystemRunsOnSystemPrincipal(t *testing.T) {
	stubPrincipal(t, PrincipalInfo{User: "S-1-5-18", IsSystem: true, Elevated: true, Integrity: "system"})
	a := &Agent{plat: platform.New("p803-test")}

	resp := a.handleExec(execEnvelope(t, map[string]interface{}{
		"command": "echo elevated-ok",
		"as":      "SYSTEM",
	}))

	if resp.Error != nil {
		t.Fatalf("unexpected error: %s (%s)", resp.Error.Code, resp.Error.Message)
	}
	var res protocol.ExecResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "elevated-ok") {
		t.Fatalf("exit=%d stdout=%q, want 0 and the command output", res.ExitCode, res.Stdout)
	}
	if res.Principal != "S-1-5-18" {
		t.Fatalf("principal = %q, want the SYSTEM token identity", res.Principal)
	}
	assertEnvelopeFields(t, res)
}

// (c) Plain exec is unchanged: no `as`, no refusal, same result body — plus
// only additive fields.
//
//	ASSERTION: resp.Error == nil && res.ExitCode == 0 && strings.Contains(res.Stdout, "plain-ok")
//
// i.e. a v1.18.6-shaped request produces the same success it produced before.
func TestPlainExecUnchangedWithoutAsField(t *testing.T) {
	stubPrincipal(t, PrincipalInfo{User: "flk", IsSystem: false, Integrity: "medium", TokenFiltered: true})
	a := &Agent{plat: platform.New("p803-test")}

	resp := a.handleExec(execEnvelope(t, map[string]interface{}{
		"command": "echo plain-ok",
		"timeout": 10,
	}))
	if resp.Error != nil {
		t.Fatalf("plain exec regressed into an error: %s (%s)", resp.Error.Code, resp.Error.Message)
	}
	var res protocol.ExecResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "plain-ok") {
		t.Fatalf("plain exec changed: exit=%d stdout=%q", res.ExitCode, res.Stdout)
	}
	// The added fields must be populated, not zero-valued.
	assertEnvelopeFields(t, res)
	if res.Principal != "flk" {
		t.Fatalf("principal = %q, want the agent's own principal", res.Principal)
	}
}

// A bogus `as` value is refused rather than silently treated as "current".
func TestExecUnknownAsRefused(t *testing.T) {
	stubPrincipal(t, PrincipalInfo{User: "flk", Integrity: "medium"})
	a := &Agent{plat: platform.New("p803-test")}
	resp := a.handleExec(execEnvelope(t, map[string]interface{}{"command": "echo x", "as": "root"}))
	if resp.Error == nil || resp.Error.Code != protocol.ErrInvalidParams {
		t.Fatalf("as:\"root\" response = %+v, want INVALID_PARAMS", resp.Error)
	}
}

func assertEnvelopeFields(t *testing.T, res protocol.ExecResult) {
	t.Helper()
	if res.StartedAtUTC == "" || res.StartedAtLocal == "" {
		t.Fatalf("envelope timestamps missing: utc=%q local=%q", res.StartedAtUTC, res.StartedAtLocal)
	}
	if _, err := time.Parse(time.RFC3339Nano, res.StartedAtUTC); err != nil {
		t.Fatalf("started_at_utc %q is not RFC3339: %v", res.StartedAtUTC, err)
	}
	if _, err := time.Parse(time.RFC3339, res.StartedAtLocal); err != nil {
		t.Fatalf("started_at_local %q is not RFC3339 with offset: %v", res.StartedAtLocal, err)
	}
	if res.TZ == "" {
		t.Fatal("tz is empty: the response does not declare the host zone")
	}
}

// The askpass workaround must not reappear. This is the retirement check: the
// runtime-compiled askpass helper is gone from the agent and client trees, and
// its replacement (as:"SYSTEM" + a LocalSystem service) is the only path.
func TestNoAskpassPathInAgentOrClientSource(t *testing.T) {
	roots := []string{filepath.Join(".."), filepath.Join("..", "..", "cmd")}
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".ps1") {
				return nil
			}
			// This test names the thing it forbids; it is not a path.
			if strings.HasSuffix(path, "elevation_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			if strings.Contains(strings.ToLower(string(b)), "askpass") {
				t.Fatalf("askpass path is back in %s; the ratified contract replaced it with exec{as:\"SYSTEM\"}", path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}
