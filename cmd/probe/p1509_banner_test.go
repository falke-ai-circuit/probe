package main

// P15-09 — the connect banner must print the identity the agent actually
// registers with, not the artifact-derived launch name.
//
// A canary launched from a build whose embedded config carries a build name
// (e.g. "P15-02-linux-canary-canary" style launch names) used to print
// "Connecting to ... as '<launch name>'" while the truthful [identity] line
// from New() named the installation. This test pins the same contract the
// canary log depends on: the banner line and the resolved identity agree.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// canarySuffix mirrors internal/agent's canaryNameSuffix ("-canary"). The
// suffix itself is unexported in the agent package; connect.go appends the
// literal, so the test does too.
const canarySuffix = "-canary"

// TestP1509_BannerNamesTheRegisteredIdentity builds a canary the way
// cmd/probe/connect.go does — artifact launch name + canary suffix — where the
// installation's config file names a DIFFERENT identity, and asserts that
// printConnectBanner emits exactly the name agent.New resolved (the registration
// name), side by side with it. It is an agreement assertion against the resolved
// identity, not a golden-file snapshot of an arbitrary string.
func TestP1509_BannerNamesTheRegisteredIdentity(t *testing.T) {
	dir := t.TempDir()

	configured := "p15-02-linux-install"
	launchName := "P15-02-linux-canary-canary" // artifact-derived launch name

	installCfg := filepath.Join(dir, "p15-02-linux.json")
	if err := os.WriteFile(installCfg, []byte(`{"server":"ws://example.invalid/ws","name":"`+configured+`"}`), 0o644); err != nil {
		t.Fatalf("write install config: %v", err)
	}

	cfg := agent.Config{
		Mode:       "outbound",
		URL:        "ws://example.invalid/ws",
		Name:       launchName + canarySuffix, // connect.go: name + "-canary"
		CanaryMode: true,
		ConfigPath: installCfg,
	}
	ag := agent.New(cfg)

	var buf bytes.Buffer
	printConnectBanner(&buf, ag.Name(), cfg, "silent")

	banner := buf.String()
	want := configured + canarySuffix

	if !strings.Contains(banner, "as '"+want+"'") {
		t.Fatalf("banner does not name the registered identity:\n  banner: %q\n  registered identity (agent.Name()): %q\n  launch name (must NOT appear): %q",
			banner, ag.Name(), launchName)
	}
	if strings.Contains(banner, "as '"+launchName+"'") {
		t.Fatalf("banner still names the artifact-derived launch name %q; it must name the identity the agent registers under (%q)",
			launchName, want)
	}
	if ag.Name() != want {
		t.Fatalf("agent.New resolved %q, want %q — the banner test's premise (agreement) must hold before the banner can",
			ag.Name(), want)
	}
}

// TestP1509_BannerInboundModeAgreesWithRegistration pins the inbound branch the
// same way: the "Listening on ... as '<name>'" line must equal agent.Name().
func TestP1509_BannerInboundModeAgreesWithRegistration(t *testing.T) {
	dir := t.TempDir()

	configured := "inbound-install"
	launchName := "artifact-build-name"

	installCfg := filepath.Join(dir, "inbound.json")
	if err := os.WriteFile(installCfg, []byte(`{"listen":":7700","name":"`+configured+`"}`), 0o644); err != nil {
		t.Fatalf("write install config: %v", err)
	}

	cfg := agent.Config{
		Mode:       "inbound",
		Addr:       ":7700",
		Name:       launchName + canarySuffix,
		CanaryMode: true,
		ConfigPath: installCfg,
	}
	ag := agent.New(cfg)

	var buf bytes.Buffer
	printConnectBanner(&buf, ag.Name(), cfg, "silent")

	banner := buf.String()
	want := configured + canarySuffix

	if !strings.Contains(banner, "as '"+want+"' (mode: inbound)") {
		t.Fatalf("inbound banner does not name the registered identity:\n  banner: %q\n  registered identity: %q", banner, ag.Name())
	}
	if strings.Contains(banner, "as '"+launchName+"'") {
		t.Fatalf("inbound banner still names the launch name %q", launchName)
	}
}
