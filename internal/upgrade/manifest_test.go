package upgrade

import (
	"encoding/json"
	"strings"
	"testing"
)

func validManifest() Manifest {
	return Manifest{
		Canonical:     `C:\Probe\probe-client.exe`,
		SizePayload:   1024,
		SHA256Payload: strings.Repeat("a", 64),
		SHA256Boot:    strings.Repeat("b", 64),
		ServerHost:    "10.0.0.5",
		ServerPort:    443,
	}
}

func TestManifestRoundTrip(t *testing.T) {
	m := validManifest()
	m.OldPID = 1234
	data, err := m.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !IsASCII(data) {
		t.Fatal("manifest must be ASCII")
	}
	back, err := ParseManifest(data)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if back.OldPID != 1234 {
		t.Fatalf("lost old_pid: %+v", back)
	}
	// Optional timings take the settled defaults.
	if back.NRollbackS != DefaultNRollbackS || back.KDwellS != DefaultKDwellS || back.LDwellS != DefaultLDwellS {
		t.Fatalf("defaults not applied: %+v", back)
	}
}

func TestManifestRejectsBadValues(t *testing.T) {
	cases := map[string]func(*Manifest){
		"relative canonical": func(m *Manifest) { m.Canonical = `probe-client.exe` },
		"staged canonical":   func(m *Manifest) { m.Canonical = `C:\Probe\probe-client.exe.next` },
		"zero size":          func(m *Manifest) { m.SizePayload = 0 },
		"short digest":       func(m *Manifest) { m.SHA256Payload = "abc" },
		"non hex digest":     func(m *Manifest) { m.SHA256Payload = strings.Repeat("z", 64) },
		"port range":         func(m *Manifest) { m.ServerPort = 70000 },
		"empty host":         func(m *Manifest) { m.ServerHost = "" },
		"relative config":    func(m *Manifest) { m.ConfigPath = `probe-client.json` },
	}
	for name, mutate := range cases {
		m := validManifest()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected validation error", name)
		}
	}
}

func TestManifestRefusesNonASCII(t *testing.T) {
	m := validManifest()
	m.ServerHost = "srv\u00e9r"
	if _, err := m.Marshal(); err == nil {
		t.Fatal("non-ASCII manifest must be refused (PowerShell reads BOM-less files as ANSI)")
	}
}

func TestManifestOmitsEmptyOptionalKeys(t *testing.T) {
	data, err := validManifest().Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"canonical", "size_payload", "sha256_payload", "sha256_boot", "server_host", "server_port"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("required key %q missing", key)
		}
	}
	if _, ok := raw["old_pid"]; ok {
		t.Error("empty optional key old_pid should be omitted")
	}
}

func TestShortDigest(t *testing.T) {
	d := strings.Repeat("ab", 32)
	if got := ShortDigest(d); got != d[:16] {
		t.Fatalf("short digest = %q", got)
	}
	if got := ShortDigest("abcd"); got != "abcd" {
		t.Fatalf("short input = %q", got)
	}
}
