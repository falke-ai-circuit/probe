package server

import (
	"net/http"

	cryptomanager "github.com/falke-ai-circuit/probe/internal/crypto"
)

// P18-04 (operative spec §11.4) — /SETTINGS surface for network/security
// toggles. READ ONLY: one GET, effective values only.
//
// Two rules copied verbatim from the closed units this surface renders:
//
//   - P17-03 no-get-echo doctrine: this is a GET surface, so by construction
//     it carries paths and boolean/configured states only. No cert or key
//     MATERIAL, no token material, ever — the response type has no field a
//     secret could ride in. Tested: every field path captured, grepped for
//     PEM headers / BEGIN / token material ≡ 0 hits (TestP1804*).
//
//   - P19-01 refusal discipline, inverted: the wss-no-CA refusal behaviour on
//     the relay dial path is CLOSED and tested — this surface does not change
//     it and adds no new server-side refusal. What it adds is VISIBILITY: the
//     pre-flight state an operator would otherwise only learn from a refused
//     dial is rendered here as a loud, stable state string the UI can show.
//
// EFFECTIVE only: every value is read from the same live fields the server's
// own read/admission paths read (e2eMgr, certFile/keyFile/clientCAFile,
// tokenTTL, the installed relay allowlist) — never the compose file, never
// the flags file alone.
//
// Response shape (each group carries a loud `state` for the UI):
//
//	e2e:              { enabled, state }
//	tls:              { enabled, cert_path, key_path, client_ca_path,
//	                    ca_configured, mtls_active, state }
//	token_rotation:   { ttl_seconds, ttl, active, guard }
//	relay_downstream: { policy, tokens_required, allow_any_downstream,
//	                    allowlist }
//
// TLS `state` values (stable strings, UI-renderable):
//
//	"ok_plain_http"         — no TLS configured
//	"tls_without_client_ca" — TLS up but no client CA: the P19-01 wss dial
//	                          refusal pre-flight is VISIBLE here
//	"ok_tls_mtls"           — TLS + client CA configured
//
// Token-rotation `ttl_seconds` = 0 means rotation is DISABLED — that state is
// only reachable through the --allow-disabled-token-ttl opt-out, which
// ValidateTokenTTL enforced at boot (P19-01 guard kept intact, not re-run
// here: it is a boot-time CLI guard, not an API-read concern).

// tlsStateFor derives the loud TLS/CA state string from the effective paths.
func tlsStateFor(certFile, keyFile, clientCAFile string) string {
	switch {
	case certFile == "" || keyFile == "":
		return "ok_plain_http"
	case clientCAFile == "":
		return "tls_without_client_ca"
	default:
		return "ok_tls_mtls"
	}
}

// e2eStateLive derives the loud E2E state from the manager the WS read path
// itself consults (server_ws.go: s.e2eMgr.IsActive()).
func e2eStateLive(mgr *cryptomanager.Manager) string {
	switch {
	case mgr == nil:
		return "off_no_manager"
	case mgr.IsActive():
		return "on_aes256gcm"
	default:
		return "off_inactive"
	}
}

// settingsPayload is the typed response for GET /api/v1/settings. By
// construction it cannot carry secret material: every field is a path
// string, a boolean, a duration, or a policy enum. P17-03 holds.
type settingsPayload struct {
	E2E struct {
		Enabled bool   `json:"enabled"`
		State   string `json:"state"`
	} `json:"e2e"`
	TLS struct {
		Enabled       bool   `json:"enabled"`
		CertPath      string `json:"cert_path"`
		KeyPath       string `json:"key_path"`
		ClientCAPath  string `json:"client_ca_path"`
		CAConfigured  bool   `json:"ca_configured"`
		MTLSActive    bool   `json:"mtls_active"`
		State         string `json:"state"`
	} `json:"tls"`
	TokenRotation struct {
		TTLSeconds int64  `json:"ttl_seconds"`
		TTL        string `json:"ttl"`
		Active     bool   `json:"active"`
		Guard      string `json:"guard"`
	} `json:"token_rotation"`
	RelayDownstream struct {
		Policy             string   `json:"policy"`
		TokensRequired     bool     `json:"tokens_required"`
		AllowAnyDownstream bool     `json:"allow_any_downstream"`
		Allowlist          []string `json:"allowlist"`
	} `json:"relay_downstream"`
}

// handleV1Settings serves GET /api/v1/settings: the effective network and
// security toggle values. Auth via the same v1CheckAuth gate as every other
// read surface; the payload itself is secrets-free by type construction.
func (s *Server) handleV1Settings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}

	policy, entries := s.relayAllowlistPolicy()
	var p settingsPayload
	p.E2E.Enabled = s.e2eMgr != nil && s.e2eMgr.IsActive()
	p.E2E.State = e2eStateLive(s.e2eMgr)
	p.TLS.Enabled = s.certFile != "" && s.keyFile != ""
	p.TLS.CertPath = s.certFile
	p.TLS.KeyPath = s.keyFile
	p.TLS.ClientCAPath = s.clientCAFile
	p.TLS.CAConfigured = s.clientCAFile != ""
	p.TLS.MTLSActive = p.TLS.CAConfigured && p.TLS.Enabled
	p.TLS.State = tlsStateFor(s.certFile, s.keyFile, s.clientCAFile)
	p.TokenRotation.TTLSeconds = int64(s.tokenTTL.Seconds())
	p.TokenRotation.TTL = s.tokenTTL.String()
	p.TokenRotation.Active = s.tokenTTL > 0
	p.TokenRotation.Guard = "explicit --token-ttl=0 refused without --allow-disabled-token-ttl (P19-01)"
	p.RelayDownstream.Policy = policy
	p.RelayDownstream.TokensRequired = policy != RelayAllowlistAllowAll
	p.RelayDownstream.AllowAnyDownstream = policy == RelayAllowlistAllowAll
	p.RelayDownstream.Allowlist = entries

	writeJSON(w, http.StatusOK, p)
}

// registerSettingsRoutes wires the P18-04 surface onto the v1 mux. GET only —
// this surface is a viewport onto boot-time state, never a control. Called
// from registerV1Routes so the surface exists on every server that mounts
// the v1 API.
func (s *Server) registerSettingsRoutes() {
	s.mux.HandleFunc("GET /api/v1/settings", s.handleV1Settings)
}
