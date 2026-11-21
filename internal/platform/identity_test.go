package platform

// identity_test.go — proves the build-derived names carry no payload brand.

import (
	"os"
	"strings"
	"testing"
)

func TestDefaultNamesAreBuildDerived(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()

	os.Args = []string{"/opt/bin/SomeHost.exe"}
	if got := DefaultAgentName(); got != "somehost" {
		t.Fatalf("DefaultAgentName = %q, want %q", got, "somehost")
	}
	if got := DefaultConfigName(); got != "somehost.json" {
		t.Fatalf("DefaultConfigName = %q, want %q", got, "somehost.json")
	}

	os.Args = []string{"probe-client"}
	if got := DefaultConfigName(); got != "probe-client.json" {
		t.Fatalf("DefaultConfigName for an unchanged binary = %q; it must follow argv[0]", got)
	}
}

func TestDefaultNamesCarryNoPayloadBrand(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"/opt/bin/landapter-agent.exe"}

	for _, got := range []string{DefaultAgentName(), DefaultConfigName()} {
		if strings.Contains(strings.ToLower(got), "probe") {
			t.Fatalf("%q still names the payload brand", got)
		}
	}
}

func TestDefaultNamesFallBackToExecutable(t *testing.T) {
	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{}

	// With no argv[0] the name comes from os.Executable() (here: the test
	// binary), never from an empty string or a hardcoded product name.
	if got := DefaultAgentName(); got == "" || strings.Contains(got, "probe") {
		t.Fatalf("DefaultAgentName with empty argv[0] = %q, want the executable base", got)
	}
	if got := DefaultConfigName(); !strings.HasSuffix(got, ".json") {
		t.Fatalf("DefaultConfigName with empty argv[0] = %q, want a .json name", got)
	}
}
