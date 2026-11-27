package main

import (
	"encoding/json"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// TestCanaryLaunchArgvIsParseableByEntrypoint is the anti-tautology guard for the
// update launch contract. It takes the argv the updater ACTUALLY launches
// (agent.CanaryArgs — the single source of truth used by StartCanary) and feeds
// it to the probe-client entrypoint parser. If the launcher and the entrypoint
// drift apart again — the v1.18.6 defect where the client only defined --config,
// so the launched argv printed usage and exited — this test fails.
func TestCanaryLaunchArgvIsParseableByEntrypoint(t *testing.T) {
	const cfg = "/opt/probe/probe-client.json"
	const oldPID = 4242

	argv := agent.CanaryArgs(cfg, oldPID)

	// Every flag the launcher emits must be registered on the entrypoint.
	var out clientFlags
	fs := newClientFlagSet(&out)
	for _, name := range launchedFlagNames(t, argv) {
		if fs.Lookup(name) == nil {
			t.Fatalf("updater launches --%s but the probe-client entrypoint does not define it; argv=%v", name, argv)
		}
	}

	opts, err := parseClientArgs(argv)
	if err != nil {
		t.Fatalf("probe-client cannot parse the argv the updater launches: %v; argv=%v", err, argv)
	}
	if opts.configPath != cfg {
		t.Fatalf("configPath = %q, want %q", opts.configPath, cfg)
	}
	if !opts.canary {
		t.Fatalf("canary = false, want true; argv=%v", argv)
	}
	if opts.canaryOldPID != oldPID {
		t.Fatalf("canaryOldPID = %d, want %d", opts.canaryOldPID, oldPID)
	}
}

// launchedFlagNames extracts the "--name" tokens from an argv, stopping value
// tokens (the ones following a flag that is not boolean) from being mistaken for
// flags.
func launchedFlagNames(t *testing.T, argv []string) []string {
	t.Helper()
	var names []string
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		if len(a) > 2 && a[0] == '-' && a[1] == '-' {
			names = append(names, a[2:])
			switch a[2:] {
			case "config", "canary-old-pid":
				i++ // skip the value token
			}
		}
	}
	return names
}

// TestEntrypointRejectsUnknownFlags proves the guard above cannot pass by
// accident: an argv shaped like the old defect (an undefined canary flag) must
// be an error, never silently ignored.
func TestEntrypointRejectsUnknownFlags(t *testing.T) {
	if _, err := parseClientArgs([]string{"connect", "--config", "x.json", "--canary-pid", "7"}); err == nil {
		t.Fatal("parseClientArgs accepted an undefined flag; it must reject unknown flags")
	}
	if _, err := parseClientArgs([]string{"--config"}); err == nil {
		t.Fatal("parseClientArgs accepted a flag with a missing value; it must error")
	}
}

// TestEntrypointAcceptsPlainInvocation keeps the no-flag path intact: a normal
// launch must still parse with the default config path and canary disabled.
func TestEntrypointAcceptsPlainInvocation(t *testing.T) {
	opts, err := parseClientArgs(nil)
	if err != nil {
		t.Fatalf("plain invocation failed: %v", err)
	}
	if opts.configPath != "probe-client.json" || opts.canary || opts.canaryOldPID != 0 {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
}

// TestConnectE2EPostureConfigResolution covers the P23-16 posture matrix:
// absent e2e_enabled resolves default-on (P19-01 §11.4), true stays true,
// explicit false opts out verbatim.
func TestConnectE2EPostureConfigResolution(t *testing.T) {
	f := false
	tr := true
	cases := []struct {
		name string
		p    *bool
		want bool
	}{
		{"absent_defaults_on", nil, true},
		{"explicit_true", &tr, true},
		{"explicit_false", &f, false},
	}
	for _, tc := range cases {
		if got := connectE2EPosture(false, false, tc.p); got != tc.want {
			t.Fatalf("%s: connectE2EPosture = %v, want %v", tc.name, got, tc.want)
		}
	}
	// P23-14 (a) parity: an explicit --e2e flag overrides the config value.
	if got := connectE2EPosture(true, true, &f); !got {
		t.Fatal("explicit --e2e must override e2e_enabled:false in the config")
	}
	if got := connectE2EPosture(false, true, &tr); got {
		t.Fatal("explicit --e2e=false must override e2e_enabled:true in the config")
	}
}

// TestConfigFileE2EKeyWiring proves the JSON key actually reaches the
// constructed agent.Config — not just that the struct tag exists. The P23-14
// defect (agent.Config.E2EEnabled config-deaf) is exactly this wiring, so the
// test decodes real config JSON and walks the same path runClient uses
// (resolve -> connectE2EPosture -> agent.Config literal).
func TestConfigFileE2EKeyWiring(t *testing.T) {
	cases := []struct {
		name string
		json string
		want bool
	}{
		{"no_key_defaults_on", `{"server": "h:7700", "token": "t"}`, true},
		{"true_stays_true", `{"server": "h:7700", "token": "t", "e2e_enabled": true}`, true},
		{"explicit_false", `{"server": "h:7700", "token": "t", "e2e_enabled": false}`, false},
	}
	for _, tc := range cases {
		var fcfg ConfigFile
		if err := json.Unmarshal([]byte(tc.json), &fcfg); err != nil {
			t.Fatalf("%s: config decode: %v", tc.name, err)
		}
		cfg := agent.Config{
			Mode:       "outbound",
			E2EEnabled: connectE2EPosture(false, false, fcfg.E2EEnabled),
		}
		if cfg.E2EEnabled != tc.want {
			t.Fatalf("%s: agent.Config.E2EEnabled = %v, want %v", tc.name, cfg.E2EEnabled, tc.want)
		}
	}
}

// TestE2EFlagParsesAndOverrides keeps the --e2e flag honest on both axes: it
// is registered and parseable, and its explicitness reaches the posture
// resolver the way the wire precedence expects.
func TestE2EFlagParsesAndOverrides(t *testing.T) {
	var out clientFlags
	fs := newClientFlagSet(&out)
	if fs.Lookup("e2e") == nil {
		t.Fatal("newClientFlagSet does not define --e2e")
	}
	opts, err := parseClientArgs([]string{"connect", "--config", "x.json", "--e2e"})
	if err != nil {
		t.Fatalf("--e2e not parseable: %v", err)
	}
	if !opts.e2eExplicit || !opts.e2eFlag {
		t.Fatalf("--e2e must be explicit+on, got explicit=%v flag=%v", opts.e2eExplicit, opts.e2eFlag)
	}
	// Absent flag: not explicit, config decides.
	opts, err = parseClientArgs([]string{"connect", "--config", "x.json"})
	if err != nil {
		t.Fatalf("plain invocation failed: %v", err)
	}
	if opts.e2eExplicit {
		t.Fatal("absent --e2e must not be explicit")
	}
}
