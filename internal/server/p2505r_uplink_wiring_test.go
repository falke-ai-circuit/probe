package server

// P25-05R — the uplink wiring battery. Remediation of P25-05 FAIL:
// forwardURL was declared but never written at runtime. The wiring under
// test: a default_peer flagged uplink:true, once ADOPTED (§4 connected),
// wires the server's forward target from its own record, so the forward
// policy becomes reachable live.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// p2505rTargetOf reads the uplink forward target without reaching into
// unsynchronised state: the forwarder is created (or not) exactly as the
// live decision path does, via uplinkForward().
func p2505rTargetOf(t *testing.T, srv *Server) string {
	t.Helper()
	f := srv.uplinkForward()
	if f == nil {
		return ""
	}
	return f.upstreamURL
}

// p1702PeersServerFlag is p1702PeersServer with the P25-05R uplink flag.
func p1702PeersServerFlag(t *testing.T, up *p1702Upstream, id string, uplink bool) (*Server, func()) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	t.Setenv("PROBE_SERVER_NAME", "dialer")
	srv, cleanup := newV1TestServer(t)
	srv.addr = "localhost:7700"
	srv.SetVersion("v1.20.0-p1702")

	body := fmt.Sprintf(`{"id":%q,"upstream":%q,"token":"test-token","enabled":true,"uplink":%t,"max_agents":8}`,
		id, up.url, uplink)
	req := httptest.NewRequest("POST", "/api/v1/peers", strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/peers: status %d body %s", rec.Code, rec.Body.String())
	}
	return srv, cleanup
}

// TestP2505R_AdoptionWiresUplink (A1): POST /api/v1/peers with uplink:true
// → adopted peer → forwardURL populated. FAILS if SetUplinkForwardTarget
// stops being called (the wiring IS the acceptance).
func TestP2505R_AdoptionWiresUplink(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServerFlag(t, up, "uplink-peer-1", true)
	defer cleanup()

	view := p1702AwaitState(t, srv, "uplink-peer-1", peerStateConnected, 15*time.Second)
	if st, _ := view["link_state"].(string); st != peerStateConnected {
		t.Fatalf("precondition: peer did not adopt, last view %v", view)
	}
	if upl, _ := view["uplink"].(bool); !upl {
		t.Fatalf("POST body uplink:true did not persist: view %v", view)
	}
	deadline := time.Now().Add(5 * time.Second)
	want := "ws" + strings.TrimPrefix(up.srv.URL, "http")
	for time.Now().Before(deadline) {
		if got := p2505rTargetOf(t, srv); got != "" {
			if !strings.HasPrefix(got, want) {
				t.Fatalf("forward target %q does not point at the adopted peer %q", got, want)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("peer adopted with uplink:true but forwardURL was never wired (SetUplinkForwardTarget not called)")
}

// TestP2505R_NoUplinkFlagLeavesChainRoot (A3): a peer WITHOUT the uplink
// flag adopts normally and leaves the chain root untouched — forwardURL
// stays empty, and the chain-root deferral text is byte-for-byte unchanged.
func TestP2505R_NoUplinkFlagLeavesChainRoot(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServerFlag(t, up, "plain-peer-1", false)
	defer cleanup()

	view := p1702AwaitState(t, srv, "plain-peer-1", peerStateConnected, 15*time.Second)
	if st, _ := view["link_state"].(string); st != peerStateConnected {
		t.Fatalf("precondition: peer did not adopt, last view %v", view)
	}
	if got := p2505rTargetOf(t, srv); got != "" {
		t.Fatalf("uplink:false peer wired the forward target: %q (chain-root back-compat broken)", got)
	}
	// A3: the deferral error text is UNCHANGED.
	_, err := srv.ToggleRelayPolicy("some-agent", PolicyRelay)
	if err == nil || !strings.Contains(err.Error(), "no uplink configured") {
		t.Fatalf("chain-root deferral text changed: %v", err)
	}
}

// TestP2505R_RelPolicyAppliesThroughAdoptedPeer (A2): with the uplink wired
// by adoption, POST forward-policy relay on a connected client no longer
// defers on chain root — the agent re-registers upstream and the note
// reports the live apply.
func TestP2505R_RelPolicyAppliesThroughAdoptedPeer(t *testing.T) {
	up := p1702StartUpstream(t)
	srv, cleanup := p1702PeersServerFlag(t, up, "uplink-peer-2", true)
	// In-process both servers derive the same chain id from the hostname,
	// so the upstream would read the dialer's derived fwd id as its own
	// (FORWARD_LOOP on a loopback-only collision). An explicit forwarder id
	// is also what a production two-servers-one-host operator would set.
	srv.SetForwarderID("p2505r-ds")
	defer cleanup()

	view := p1702AwaitState(t, srv, "uplink-peer-2", peerStateConnected, 15*time.Second)
	if st, _ := view["link_state"].(string); st != peerStateConnected {
		t.Fatalf("precondition: peer did not adopt, last view %v", view)
	}
	p2505rAwaitForwardTarget(t, srv, up.srv.URL, "adoption wiring")

	// A live agent on the dialing server, so the toggle can re-register it.
	ts := httptest.NewServer(srv.mux)
	defer ts.CloseClientConnections()
	defer ts.Close()
	conn := p2104DialAgent(t, ts, "agent-p2505r")
	defer conn.Close()
	p2104Wait(t, "agent-p2505r registration", func() bool { return p2104RegistryHas(srv, "agent-p2505r") })

	// The policy toggles through the production mux: POST forward-policy.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/agents/agent-p2505r/forward-policy",
		strings.NewReader(`{"action":"relay"}`))
	req.Header.Set("Authorization", "Bearer test-token")
	srv.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST forward-policy: %d %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK      bool   `json:"ok"`
		Applied bool   `json:"applied"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode forward-policy response: %v", err)
	}
	if strings.Contains(resp.Note, "chain root") {
		t.Fatalf("A2 failed: toggle still defers on chain root with a live adopted uplink: %s", resp.Note)
	}
	if strings.Contains(resp.Note, "takes effect at next arrival") {
		t.Fatalf("A2 failed: live apply deferred: %s", resp.Note)
	}
	if !resp.Applied {
		t.Fatalf("A2: policy not applied live: note=%q", resp.Note)
	}
}

// p2505rAwaitForwardTarget polls until the forward target is wired to a
// URL containing wantHost.
func p2505rAwaitForwardTarget(t *testing.T, srv *Server, wantURL, what string) {
	t.Helper()
	wantHost := hostOf(wantURL)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f := srv.uplinkForward()
		if f != nil && f.upstreamURL != "" &&
			strings.Contains(f.upstreamURL, wantHost) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (forward target never set)", what)
}

func hostOf(raw string) string {
	raw = strings.TrimPrefix(raw, "http://")
	raw = strings.TrimPrefix(raw, "https://")
	raw = strings.TrimPrefix(raw, "ws://")
	raw = strings.TrimPrefix(raw, "wss://")
	if i := strings.Index(raw, "/"); i >= 0 {
		raw = raw[:i]
	}
	return raw
}
