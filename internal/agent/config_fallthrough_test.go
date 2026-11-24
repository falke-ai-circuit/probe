package agent

import (
	"path/filepath"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/upgrade"
)

// TestSyntheticConfigFallsThroughToRealConfig is the P12-01 acceptance for the
// config leg. It exercises BOTH halves of the link that broke on the live
// Windows node, in the order they happen:
//
//	writer half (new):   the old client synthesises <dir>\probe-client.json; the
//	                     successor argv derivation must drop a --config that
//	                     names no file instead of pinning a dead end into it.
//	reader half (P9-20): the successor resolves its config beside its own
//	                     executable when no usable --config was passed.
//
// The assertion is exact: the path the process ends up reading is the
// agent-named file that actually exists, and the synthetic default is reported
// as dropped rather than silently honoured.
func TestSyntheticConfigFallsThroughToRealConfig(t *testing.T) {
	// The field layout from V6049A-EAS1: agent-named exe + agent-named json,
	// and no probe-client.json (the old client's synthesised default).
	dir := t.TempDir()
	exe := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.exe"), "current-binary")
	named := writeCfg(t, filepath.Join(dir, "probe-V6049A-EAS1.json"),
		`{"server":"ws://139.99.148.90:80/ws","mode":"silent","token":"V6049A-EAS1"}`)
	synthetic := filepath.Join(dir, "probe-client.json") // deliberately never written

	// Reader half: an explicit --config that names no file falls through to the
	// files beside the executable instead of aborting the canary.
	fallthroughPath, fallthroughSource, err := ResolveConfigPath(synthetic, exe, false)
	if err != nil {
		t.Fatalf("ResolveConfigPath fell through to an error: %v", err)
	}
	if fallthroughPath != named || fallthroughSource != ConfigSourceExeNamed {
		t.Fatalf("explicit-missing resolution = %q (%s), want %q (%s)",
			fallthroughPath, fallthroughSource, named, ConfigSourceExeNamed)
	}

	// Writer half: the same synthetic value on an old client's command line is
	// dropped, so the successor is not handed a path that cannot be read. The
	// oracle models the Windows host that runs the swap: the synthetic file is
	// absent, the agent-named file is present.
	oracle := func(path string) bool {
		return upgrade.EqualWinPath(path, `C:\Probe\probe-V6049A-EAS1.json`)
	}
	args, cfg, dropped, err := upgrade.NewClientArgsChecked(
		`C:\Probe\probe-V6049A-EAS1.exe connect --config C:\Probe\probe-client.json`, oracle)
	if err != nil {
		t.Fatalf("NewClientArgsChecked: %v", err)
	}
	if len(args) != 0 || cfg != "" {
		t.Fatalf("successor argv = %v (cfg %q), want zero arguments: a dead --config must not be propagated", args, cfg)
	}
	if dropped != `C:\Probe\probe-client.json` {
		t.Fatalf("dropped = %q, want the synthesised default so the caller can journal it", dropped)
	}
	if err := upgrade.ValidateClientArgs(args); err != nil {
		t.Fatalf("derived argv is not parseable by the successor: %v", err)
	}

	// What the successor then does with those zero arguments is the reader half
	// again — and it lands on the real config, not the synthetic name.
	path, source, err := ResolveConfigPath("", exe, false)
	if err != nil {
		t.Fatalf("ResolveConfigPath with the derived argv: %v", err)
	}
	if path != named || source != ConfigSourceExeNamed {
		t.Fatalf("resolved %q (%s), want the real agent-named config %q (%s)",
			path, source, named, ConfigSourceExeNamed)
	}
	t.Logf("synthetic %q dropped; successor reads %q (%s)", dropped, path, source)
}
