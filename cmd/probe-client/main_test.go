package main

import (
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
