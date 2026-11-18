package upgrade

import (
	"errors"
	"strings"
	"testing"
)

func statExists(size int64) StatFunc {
	return func(string) (StatResult, error) {
		return StatResult{Exists: true, Size: size}, nil
	}
}

func TestStripStagedSuffix(t *testing.T) {
	cases := []struct{ in, want string }{
		{`C:\Probe\probe-client.exe.next`, `C:\Probe\probe-client.exe`},
		{`C:\Probe\probe-client.exe.old`, `C:\Probe\probe-client.exe`},
		{`C:\Probe\probe-client.exe.next.old`, `C:\Probe\probe-client.exe.next`},
		{`C:\Probe\probe-client.exe.NEXT`, `C:\Probe\probe-client.exe`},
		{`C:\Probe\probe-client.exe`, `C:\Probe\probe-client.exe`},
	}
	for _, c := range cases {
		if got := StripStagedSuffix(c.in); got != c.want {
			t.Errorf("StripStagedSuffix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestResolveCanonicalPrefersManifest(t *testing.T) {
	res, err := ResolveCanonical(`C:\Probe\probe-client.exe.next`, `C:\Probe\probe-client.exe`, statExists(10))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Canonical != `C:\Probe\probe-client.exe` || res.Source != SourceManifest {
		t.Fatalf("resolution = %+v", res)
	}
}

func TestResolveCanonicalUsesDiscovered(t *testing.T) {
	res, err := ResolveCanonical(`C:\Probe\probe-client.exe`, "", statExists(10))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Canonical != `C:\Probe\probe-client.exe` || res.Source != SourceDiscovered {
		t.Fatalf("resolution = %+v", res)
	}
}

func TestResolveCanonicalRefusesDisagreement(t *testing.T) {
	_, err := ResolveCanonical(`C:\Probe\probe-client.exe`, `C:\Other\probe-client.exe`, statExists(10))
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeCanonicalUnresolved {
		t.Fatalf("expected CANONICAL_UNRESOLVED, got %v", err)
	}
	if !strings.Contains(err.Error(), "exe=") || !strings.Contains(err.Error(), "manifest=") {
		t.Fatalf("error must name both sources: %v", err)
	}
}

func TestResolveCanonicalRefusesRelativeAndMissing(t *testing.T) {
	if _, err := ResolveCanonical("", `probe-client.exe`, statExists(10)); err == nil {
		t.Fatal("relative supplied path must be refused")
	}
	if _, err := ResolveCanonical(`C:\Probe\probe-client.exe`, "", func(string) (StatResult, error) {
		return StatResult{Exists: false}, nil
	}); err == nil {
		t.Fatal("missing canonical must be refused")
	}
	if _, err := ResolveCanonical(`C:\Probe\probe-client.exe`, "", func(string) (StatResult, error) {
		return StatResult{Exists: true, IsDir: true}, nil
	}); err == nil {
		t.Fatal("directory canonical must be refused")
	}
}

func TestSandboxGate(t *testing.T) {
	if err := SandboxGate("", `C:\Probe`); err != nil {
		t.Fatalf("empty sandbox must allow: %v", err)
	}
	if err := SandboxGate(`C:\`, `C:\Probe`); err != nil {
		t.Fatalf("containing sandbox must allow: %v", err)
	}
	err := SandboxGate(`C:\Probe\sandbox`, `C:\Probe`)
	var se *StepError
	if !errors.As(err, &se) || se.Code != CodeSandboxRefused {
		t.Fatalf("expected SANDBOX_REFUSED, got %v", err)
	}
}
