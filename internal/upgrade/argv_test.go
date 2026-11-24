package upgrade

import (
	"errors"
	"testing"
)

func TestNewClientArgsEmbeddedBuild(t *testing.T) {
	args, cfg, err := NewClientArgs(`C:\Probe\probe-client.exe`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 0 || cfg != "" {
		t.Fatalf("embedded build must get zero arguments, got %v / %q", args, cfg)
	}
}

func TestNewClientArgsConfigBuild(t *testing.T) {
	args, cfg, err := NewClientArgs(`C:\Probe\probe-client.exe --config "C:\Probe\probe-client.json"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != `C:\Probe\probe-client.json` {
		t.Fatalf("config = %q", cfg)
	}
	if len(args) != 2 || args[0] != "--config" || args[1] != cfg {
		t.Fatalf("args = %v", args)
	}
	if err := ValidateClientArgs(args); err != nil {
		t.Fatalf("args must be parseable: %v", err)
	}
}

func TestNewClientArgsStripsBrokenPrefix(t *testing.T) {
	// The K9 command line: connect terminates flag parsing, so the successor
	// must receive only the --config pair, never the old command line.
	args, _, err := NewClientArgs(`C:\Probe\probe-client.exe connect --config C:\Probe\probe-client.json --canary --canary-old-pid 99`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(args) != 2 || args[0] != "--config" {
		t.Fatalf("args = %v", args)
	}
	if err := ValidateClientArgs(args); err != nil {
		t.Fatalf("derived args must be valid: %v", err)
	}
}

func TestNewClientArgsRefusesRelativeConfig(t *testing.T) {
	_, _, err := NewClientArgs(`C:\Probe\probe-client.exe --config probe-client.json`)
	var se *StepError
	if !errors.As(err, &se) || se.Code != "ARGV_INVALID" {
		t.Fatalf("expected ARGV_INVALID, got %v", err)
	}
}

func TestNewClientArgsRefusesUnterminatedQuote(t *testing.T) {
	if _, _, err := NewClientArgs(`C:\Probe\probe-client.exe --config "C:\Probe\x.json`); err == nil {
		t.Fatal("unterminated quote must be refused")
	}
}

// TestNewClientArgsDropsSyntheticConfig is the P12-01 writer-side half: an old
// client that synthesised <dir>\probe-client.json on an agent-named install
// hands over a path that does not exist. Pinning it into the successor argv
// would make the new process resolve a dead end; dropping the pair lets it
// resolve the real config beside its own executable. The dropped path is
// returned so the caller can journal exactly what was discarded.
func TestNewClientArgsDropsSyntheticConfig(t *testing.T) {
	const synthetic = `C:\Probe\probe-client.json`
	oracle := func(path string) bool { return path != synthetic }

	args, cfg, dropped, err := NewClientArgsChecked(
		`C:\Probe\probe-V6049A-EAS1.exe connect --config C:\Probe\probe-client.json`, oracle)
	if err != nil {
		t.Fatalf("NewClientArgsChecked: %v", err)
	}
	if len(args) != 0 || cfg != "" {
		t.Fatalf("args=%v cfg=%q, want zero arguments so the successor resolves its own config", args, cfg)
	}
	if dropped != synthetic {
		t.Fatalf("dropped=%q, want the synthetic path %q (journal this)", dropped, synthetic)
	}
	if err := ValidateClientArgs(args); err != nil {
		t.Fatalf("zero args must stay parseable: %v", err)
	}
}

// TestNewClientArgsKeepsExistingConfig proves the existence check does not
// regress a genuine --config: when the file is there, the pair is still passed
// through and nothing is reported as dropped.
func TestNewClientArgsKeepsExistingConfig(t *testing.T) {
	const real = `C:\Probe\probe-V6049A-EAS1.json`
	oracle := func(path string) bool { return EqualWinPath(path, real) }

	args, cfg, dropped, err := NewClientArgsChecked(
		`C:\Probe\probe-V6049A-EAS1.exe --config C:\Probe\probe-V6049A-EAS1.json`, oracle)
	if err != nil {
		t.Fatalf("NewClientArgsChecked: %v", err)
	}
	if cfg != real || dropped != "" {
		t.Fatalf("cfg=%q dropped=%q, want %q and nothing dropped", cfg, dropped, real)
	}
	if len(args) != 2 || args[0] != "--config" || args[1] != real {
		t.Fatalf("args=%v, want the --config pair unchanged", args)
	}
}

func TestValidateClientArgs(t *testing.T) {
	bad := [][]string{
		{"connect"},
		{"--canary"},
		{"--config", "C:\\Probe\\x.json", "--canary-old-pid", "1"},
		{"C:\\Probe\\probe-client.exe"},
	}
	for _, args := range bad {
		if err := ValidateClientArgs(args); err == nil {
			t.Errorf("%v should be rejected", args)
		}
	}
	if err := ValidateClientArgs(nil); err != nil {
		t.Errorf("zero args are valid: %v", err)
	}
}
