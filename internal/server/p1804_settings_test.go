package server

// P18-04 — settings-surface tests.
//
// Acceptance mapping:
//   - A1/A3: TestP1804_SettingsGroupsEffectiveValues (plain default server)
//     and TestP1804_TLSDowngradeVisibility (wss-configured, missing CA →
//     loud tls_without_client_ca state + ca_configured=false).
//   - A2: TestP1804_SettingsBodiesNeverCarrySecretMaterial — every response
//     from the surface across three server configurations captured and
//     grepped for PEM headers / BEGIN / token material ≡ 0 occurrences
//     (P17-03 no-get-echo, applied to this surface).
//
// All rigs reuse the established package helpers (newCredTestServer / doJSON)
// so the settings route is exercised through the same mux the fleet mounts.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTestingDirP1804 is a t.TempDir alias so the fixture block reads angible.
func newTestingDirP1804(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// p1804get performs the GET and decodes the APIResponse {ok,data} envelope.
func p1804get(t *testing.T, srv *Server) map[string]interface{} {
	t.Helper()
	rec := doJSON(srv, "GET", "/api/v1/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/settings = %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool                   `json:"ok"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("settings body not JSON: %v", err)
	}
	if !env.OK {
		t.Fatalf("settings body ok=false: %s", rec.Body.String())
	}
	return env.Data
}

// TestP1804_SettingsGroupsEffectiveValues — A1 on the default rig: E2E ON
// (P19-01 default, same SetE2E the entrypoints call), no TLS (plain HTTP),
// token rotation enabled, relay downstream tokens required.
func TestP1804_SettingsGroupsEffectiveValues(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	srv.SetTokenTTL(24 * time.Hour)

	d := p1804get(t, srv)

	e2e, _ := d["e2e"].(map[string]interface{})
	if e2e == nil || e2e["enabled"] != true || e2e["state"] != "on_aes256gcm" {
		t.Fatalf("e2e group wrong: %+v", e2e)
	}

	tls, _ := d["tls"].(map[string]interface{})
	if tls == nil {
		t.Fatal("missing tls group")
	}
	if tls["enabled"] != false || tls["state"] != "ok_plain_http" || tls["ca_configured"] != false {
		t.Fatalf("tls group wrong: %+v", tls)
	}
	if tls["cert_path"] != "" || tls["key_path"] != "" || tls["client_ca_path"] != "" {
		t.Fatalf("tls paths should be empty on plain server: %+v", tls)
	}

	tr, _ := d["token_rotation"].(map[string]interface{})
	if tr == nil || tr["active"] != true || tr["ttl_seconds"] != float64(24*3600) {
		t.Fatalf("token_rotation group wrong: %+v", tr)
	}

	rd, _ := d["relay_downstream"].(map[string]interface{})
	if rd == nil {
		t.Fatal("missing relay_downstream group")
	}
	// Default rig installs no allowlist spec → allow-all is the effective
	// policy and the P19-01 opt-out state reads true.
	if rd["policy"] != RelayAllowlistAllowAll || rd["allow_any_downstream"] != true || rd["tokens_required"] != false {
		t.Fatalf("relay_downstream group wrong: %+v", rd)
	}
}

// TestP1804_TLSDowngradeVisibility — A3: a TLS-enabled server WITHOUT a
// client CA renders the loud state (the P19-01 wss-no-CA dial refusal is
// closed server-side; this surface makes its precondition visible pre-flight).
func TestP1804_TLSDowngradeVisibility(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	srv.certFile = "/etc/probe/tls/cert.pem"
	srv.keyFile = "/etc/probe/tls/key.pem"
	srv.clientCAFile = "" // the P19-01 refusal condition

	d := p1804get(t, srv)
	tls, _ := d["tls"].(map[string]interface{})
	if tls == nil {
		t.Fatal("missing tls group")
	}
	if tls["enabled"] != true {
		t.Fatalf("tls.enabled should be true with cert+key: %+v", tls)
	}
	if tls["ca_configured"] != false {
		t.Fatalf("tls.ca_configured should be false (loud field): %+v", tls)
	}
	if tls["state"] != "tls_without_client_ca" {
		t.Fatalf("loud state missing: %+v", tls)
	}
	if tls["cert_path"] != "/etc/probe/tls/cert.pem" || tls["key_path"] != "/etc/probe/tls/key.pem" {
		t.Fatalf("material paths not rendered: %+v", tls)
	}

	// And the configured-CA case completes the ladder.
	srv.clientCAFile = "/etc/probe/tls/client-ca.pem"
	d = p1804get(t, srv)
	tls = d["tls"].(map[string]interface{})
	if tls["ca_configured"] != true || tls["state"] != "ok_tls_mtls" || tls["mtls_active"] != true {
		t.Fatalf("mTLS state wrong: %+v", tls)
	}
}

// TestP1804_RelayDownstreamStates — the allowlist policy the server actually
// installed is what the surface reports (effective, not the flags file).
func TestP1804_RelayDownstreamStates(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	if err := srv.InstallRelayAllowlist("alpha:deadbeef, beta", true); err != nil {
		t.Fatalf("allowlist install: %v", err)
	}
	d := p1804get(t, srv)
	rd, _ := d["relay_downstream"].(map[string]interface{})
	if rd == nil {
		t.Fatal("missing relay_downstream group")
	}
	if rd["policy"] != RelayAllowlistListed || rd["tokens_required"] != true || rd["allow_any_downstream"] != false {
		t.Fatalf("listed policy not surfaced: %+v", rd)
	}
	entries, _ := rd["allowlist"].([]interface{})
	if len(entries) != 2 {
		t.Fatalf("allowlist entries wrong: %+v", entries)
	}
	// The allowlist echoes only fingerprints/ids — the fp8 in the spec is
	// already a fingerprint by P21-07 construction, and no token appears.
	body := ""
	rec := doJSON(srv, "GET", "/api/v1/settings", nil)
	body = rec.Body.String()
	if strings.Contains(body, "test-token") {
		t.Fatal("settings body carried the server token")
	}
}

// P18-04 PEM fixtures. Filled with structurally valid but throwaway content:
// the grep bar (A2) fires if any of this shows up in a response body.
const (
	p1804FakeCert = `-----BEGIN CERTIFICATE-----` + `
MIIB0zCCAX2gAwIBAgIUK18eP1804FixtureOnly00000000MAoGCCqGSM49BAMC
P1804FakeCertBodyNotRealMaterialzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz
-----END CERTIFICATE-----`
	p1804FakeKey = `-----BEGIN PRIVATE KEY-----` + `
MIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQueP1804FixtureOnly
P1804FakeKeyBodyNotRealMaterialzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz
-----END PRIVATE KEY-----`
	p1804FakeCA = `-----BEGIN CERTIFICATE-----` + `
MIIB0zCCAX2gAwIBAgIUK18eP1804FixtureOnlyCA00000000MAoGCCqGSM49BAMC
P1804FakeCABodyNotRealMaterialzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz
-----END CERTIFICATE-----`
)

// writePEMFixtureP1804 writes one fixture file; content only — the server is
// never started in TLS mode here, the fields go straight to the surface.
func writePEMFixtureP1804(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("fixture write %s: %v", path, err)
	}
}

// TestP1804_SettingsBodiesNeverCarrySecretMaterial — A2: across three server
// configurations (plain, TLS-no-CA, TLS+mTLS), every response body from the
// surface is captured and grepped for PEM headers, BEGIN markers, and the
// server's token material. Zero occurrences is the pass bar (P17-03).
func TestP1804_SettingsBodiesNeverCarrySecretMaterial(t *testing.T) {
	// TLS fixtures are written as real PEM-looking files so any accidental
	// material read-and-echo would be caught by the grep.
	dir := newTestingDirP1804(t)
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	caPath := filepath.Join(dir, "ca.pem")
	writePEMFixtureP1804(t, certPath, p1804FakeCert)
	writePEMFixtureP1804(t, keyPath, p1804FakeKey)
	writePEMFixtureP1804(t, caPath, p1804FakeCA)

	for _, tc := range []struct {
		name                       string
		cert, key, ca              string
		wantState                  string
	}{
		{"plain", "", "", "", "ok_plain_http"},
		{"tls-no-ca", certPath, keyPath, "", "tls_without_client_ca"},
		{"tls-mtls", certPath, keyPath, caPath, "ok_tls_mtls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := newCredTestServer(t)
			srv.certFile = tc.cert
			srv.keyFile = tc.key
			srv.clientCAFile = tc.ca

			// Capture the body of every variant of the surface.
			var bodies []string
			rec := doJSON(srv, "GET", "/api/v1/settings", nil)
			bodies = append(bodies, rec.Body.String())

			all := strings.Join(bodies, "\n")
			for _, needle := range []string{"BEGIN CERTIFICATE", "BEGIN PRIVATE KEY", "BEGIN RSA", "BEGIN EC", "-----BEGIN", "test-token", p1804FakeCert, p1804FakeKey, p1804FakeCA} {
				if strings.Contains(all, needle) {
					t.Fatalf("PEM/secret material (%q) in settings response: %s", needle, all)
				}
			}

			var env struct {
				Data map[string]interface{} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("bad JSON: %v", err)
			}
			tls := env.Data["tls"].(map[string]interface{})
			if tls["state"] != tc.wantState {
				t.Fatalf("state = %v want %v", tls["state"], tc.wantState)
			}
		})
	}
}

// TestP1804_SettingsEffective_notFlagsFile — rule 1: the surface reads the
// live parsed fields, not any flag file. Mutating the server fields directly
// (exactly what NewServerWithTLS does) is reflected verbatim.
func TestP1804_SettingsEffective_notFlagsFile(t *testing.T) {
	srv, _, _ := newCredTestServer(t)
	// Boot-time guard state is echoed, not re-evaluated: TTL set on the live
	// server object shows up as-is.
	srv.SetTokenTTL(0)
	d := p1804get(t, srv)
	tr := d["token_rotation"].(map[string]interface{})
	if tr["active"] != false || tr["ttl_seconds"] != float64(0) {
		t.Fatalf("disabled rotation not effective: %+v", tr)
	}
}
