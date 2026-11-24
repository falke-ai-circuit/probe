package agent

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// writeCfg writes a file for a resolution fixture.
func writeCfg(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// captureLog runs fn with the standard logger redirected into a buffer and
// returns everything it printed, so a test can assert the operator-visible
// warning text verbatim.
func captureLog(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	}()
	fn()
	return buf.String()
}

// TestResolveConfigPathOrder pins the five-step resolution order that the
// v1.19.1 field failure exposed: the running process must know which file it
// actually read, so the updater can hand that same path to the canary. Each
// case is a directory where a HIGHER-priority candidate also exists, so a
// reordering of the rules fails here.
func TestResolveConfigPathOrder(t *testing.T) {
	t.Run("explicit --config wins over every file beside the exe", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "probe-V6049A-EAS1.exe")
		writeCfg(t, filepath.Join(dir, "probe-client.json"), "{}")
		writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"), "{}")
		explicit := writeCfg(t, filepath.Join(dir, "operator.json"), "{}")

		path, source, err := ResolveConfigPath(explicit, exe, false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != explicit || source != ConfigSourceFlag {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, explicit, ConfigSourceFlag)
		}
	})

	t.Run("probe-client.json beside the exe beats the agent-named file", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "probe-V6049A-EAS1.exe")
		beside := writeCfg(t, filepath.Join(dir, "probe-client.json"), "{}")
		writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"), "{}")

		path, source, err := ResolveConfigPath("", exe, false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != beside || source != ConfigSourceProbeClientJSON {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, beside, ConfigSourceProbeClientJSON)
		}
	})

	t.Run("agent-named <exe>.json covers installs with no probe-client.json", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "probe-V6049A-EAS1.exe")
		named := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"), `{"server":"ws://h/ws"}`)

		path, source, err := ResolveConfigPath("", exe, false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != named || source != ConfigSourceExeNamed {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, named, ConfigSourceExeNamed)
		}
	})

	t.Run("the only .json beside the exe is used", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "agent.exe")
		only := writeCfg(t, filepath.Join(dir, "site-config.json"), "{}")

		path, source, err := ResolveConfigPath("", exe, false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != only || source != ConfigSourceSingleJSON {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, only, ConfigSourceSingleJSON)
		}
	})

	t.Run("embedded build config when nothing is on disk", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "agent.exe")

		path, source, err := ResolveConfigPath("", exe, true)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != "" || source != ConfigSourceEmbedded {
			t.Fatalf("path=%q source=%q, want empty path / %q", path, source, ConfigSourceEmbedded)
		}
	})

	t.Run("nothing resolvable is an error naming the reason", func(t *testing.T) {
		dir := t.TempDir()
		exe := filepath.Join(dir, "agent.exe")
		// Two JSON files is ambiguous, and there is no embedded config.
		writeCfg(t, filepath.Join(dir, "a.json"), "{}")
		writeCfg(t, filepath.Join(dir, "b.json"), "{}")

		path, _, err := ResolveConfigPath("", exe, false)
		if err == nil {
			t.Fatalf("ResolveConfigPath returned path %q, want error", path)
		}
		if !strings.Contains(err.Error(), "config not found") {
			t.Fatalf("error %q does not say config not found", err)
		}
	})
}

// TestResolveConfigPathFallsThroughWhenExplicitMissing reproduces the real
// V6049A-EAS1 desktop directory - probe-V6049A-EAS1.exe, probe-V6049A-EAS1.json,
// a stale .next and a .old - and pins the fall-through rule those files exposed:
// the process that performs an update is the OLD client, which has no resolver
// and synthesises <dir>\probe-client.json for the canary. That path never exists
// on an agent-named install, so a missing explicit --config must warn loudly and
// let rules 2-5 find the file that is actually there.
func TestResolveConfigPathFallsThroughWhenExplicitMissing(t *testing.T) {
	// fieldDir builds her install layout and returns the synthesised default
	// the old updater hands over (a probe-client.json that is NOT there).
	fieldDir := func(t *testing.T) (exe, named, synthesised string) {
		t.Helper()
		dir := t.TempDir()
		exe = writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.exe"), "current-binary")
		writeCfg(t, exe+".next", "staged-binary")
		writeCfg(t, exe+".old", "previous-binary")
		named = writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"),
			`{"server":"ws://139.99.148.90:80/ws","mode":"silent","token":"V6049A-EAS1"}`)
		return exe, named, filepath.Join(dir, "probe-client.json")
	}

	t.Run("missing explicit --config falls through to the agent-named file", func(t *testing.T) {
		exe, named, synthesised := fieldDir(t)
		var path, source string
		var err error
		warning := captureLog(t, func() {
			path, source, err = ResolveConfigPath(synthesised, exe, false)
		})
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != named || source != ConfigSourceExeNamed {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, named, ConfigSourceExeNamed)
		}
		for _, want := range []string{"WARNING", "explicit --config", synthesised, "falling through"} {
			if !strings.Contains(warning, want) {
				t.Fatalf("warning %q missing %q", warning, want)
			}
		}
	})

	t.Run("missing explicit --config falls through to probe-client.json", func(t *testing.T) {
		dir := t.TempDir()
		exe := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.exe"), "current-binary")
		beside := writeCfg(t, filepath.Join(dir, "probe-client.json"), `{"server":"ws://h/ws"}`)
		synthesised := filepath.Join(dir, "operator-typo.json")

		path, source, err := ResolveConfigPath(synthesised, exe, false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != beside || source != ConfigSourceProbeClientJSON {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, beside, ConfigSourceProbeClientJSON)
		}
	})

	t.Run("missing explicit --config with nothing beside it still fails and names every rule", func(t *testing.T) {
		dir := t.TempDir()
		exe := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.exe"), "current-binary")
		synthesised := filepath.Join(dir, "probe-client.json")

		path, _, err := ResolveConfigPath(synthesised, exe, false)
		if err == nil {
			t.Fatalf("ResolveConfigPath returned path %q, want error", path)
		}
		for _, want := range []string{
			"config not found",
			"--config " + synthesised + " does not exist",
			"no probe-client.json beside " + dir,
			"no probe-V6049A-EAS1.json beside it",
			"refusing to start a canary",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error %q missing %q", err.Error(), want)
			}
		}
	})

	t.Run("existing explicit --config still wins with no fall-through", func(t *testing.T) {
		exe, named, _ := fieldDir(t)
		explicit := writeCfg(t, filepath.Join(filepath.Dir(exe), "operator.json"), `{"server":"ws://explicit/ws"}`)

		var path, source string
		var err error
		logged := captureLog(t, func() {
			path, source, err = ResolveConfigPath(explicit, exe, false)
		})
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != explicit || source != ConfigSourceFlag {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, explicit, ConfigSourceFlag)
		}
		if path == named {
			t.Fatalf("existing explicit --config lost to the agent-named file")
		}
		if logged != "" {
			t.Fatalf("a resolvable explicit --config logged %q, want no warning", logged)
		}
	})

	t.Run("stale .next load-time path still resolves beside the canonical exe", func(t *testing.T) {
		exe, named, synthesised := fieldDir(t)
		// Windows reports the load-time path, which is the .next name while the
		// canary runs; the config lives beside the canonical binary.
		path, source, err := ResolveConfigPath(synthesised, exe+".next", false)
		if err != nil {
			t.Fatalf("ResolveConfigPath: %v", err)
		}
		if path != named || source != ConfigSourceExeNamed {
			t.Fatalf("path=%q source=%q, want %q / %q", path, source, named, ConfigSourceExeNamed)
		}
	})
}

// TestCanaryArgvCarriesResolvedConfigPath is the launch-contract guard: the
// argv handed to the staged binary must contain the path the running agent
// resolved, as an explicit --config pair. The v1.19.1 defect launched the
// canary with no --config at all, so it fell back to the default filename.
func TestCanaryArgvCarriesResolvedConfigPath(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-V6049A-EAS1.exe")
	cfgPath := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"), "{}")

	resolved, source, err := ResolveConfigPath("", exe, false)
	if err != nil || source != ConfigSourceExeNamed {
		t.Fatalf("resolve: path=%q source=%q err=%v", resolved, source, err)
	}
	if resolved != cfgPath {
		t.Fatalf("resolved %q, want %q", resolved, cfgPath)
	}

	argv := CanaryArgs(resolved, 7072)
	joined := strings.Join(argv, " ")
	if !strings.Contains(joined, "--config "+resolved) {
		t.Fatalf("canary argv %v does not carry the resolved config path %q", argv, resolved)
	}
	if !strings.HasPrefix(argv[0], "connect") {
		t.Fatalf("argv must start with the connect subcommand, got %v", argv)
	}

	// Embedded builds carry their settings in the binary: no --config at all,
	// rather than a path that cannot exist.
	embeddedArgv := strings.Join(CanaryArgs("", 7072), " ")
	if strings.Contains(embeddedArgv, "--config") {
		t.Fatalf("embedded build argv must not name a config file: %v", embeddedArgv)
	}
}

// TestUpdateAbortsBeforeStagingWhenConfigUnresolvable pins the "never start a
// canary that cannot run, never leave a half-swap on disk" rule: with no
// resolvable config the update returns a loud error and writes NOTHING — no
// .next staging file, and the pre-staged binary is left where it was.
func TestUpdateAbortsBeforeStagingWhenConfigUnresolvable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "probe-V6049A-EAS1.exe")
	writeCfg(t, exe, "current-binary")
	staged := writeCfg(t, filepath.Join(dir, "incoming.exe"), "new-binary")

	prev := executablePath
	executablePath = func() (string, error) { return exe, nil }
	defer func() { executablePath = prev }()

	a := New(Config{Permissions: "full"})
	env := protocol.Envelope{
		ID:     "upd-1",
		Type:   protocol.TypeAgentUpdate,
		Params: mustMarshal(protocol.AgentUpdateParams{Filename: "incoming.exe", WSPath: staged, SHA256: mustHash(t, staged)}),
	}

	resp := a.handleAgentUpdate(env)
	if resp.Error == nil {
		t.Fatalf("update returned no error; resp=%+v", resp)
	}
	if !strings.Contains(resp.Error.Message, "config not found") {
		t.Fatalf("error %q does not say config not found", resp.Error.Message)
	}
	if _, err := os.Stat(exe + ".next"); !os.IsNotExist(err) {
		t.Fatalf(".next was staged despite unresolvable config (stat err=%v)", err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("pre-staged binary was moved/removed before the abort: %v", err)
	}
}

// TestCanaryExitFailsUpdateWithLogTail pins the loud failure: a canary that
// exits (here: no config file to read) must return an error carrying the log
// tail, so the server records "failed: could not read config file" instead of
// an indefinite "waiting for new agent to connect".
func TestCanaryExitFailsUpdateWithLogTail(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh to simulate the staged binary")
	}
	dir := t.TempDir()
	canonical := filepath.Join(dir, "probe-V6049A-EAS1.exe")
	next := canonical + ".next"
	script := "#!/bin/sh\necho 'Error: could not read config file: probe-client.json' >&2\nexit 1\n"
	if err := os.WriteFile(next, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}

	prevWindow := CanaryExitWindow
	CanaryExitWindow = 5 * time.Second
	defer func() { CanaryExitWindow = prevWindow }()

	pid, logPath, err := StartCanary(next, canonical, filepath.Join(dir, "missing.json"), 7072)
	if err == nil {
		t.Fatalf("StartCanary returned pid=%d, want failure for an exiting canary", pid)
	}
	if pid != 0 {
		t.Fatalf("pid=%d, want 0 on a failed canary", pid)
	}
	for _, want := range []string{"canary exited before connecting", "could not read config file", logPath} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
}
