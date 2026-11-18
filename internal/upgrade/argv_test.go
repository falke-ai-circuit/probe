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
