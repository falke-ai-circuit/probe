package server

// P27-02 — backend acceptance battery (spec P27-01 §1/§2/§3/§5.2).
//
// Each test names the item it proves so a reviewer can map test -> brief
// bullet without guessing:
//
//	TestP2702UplinkTypeStoredShape      A4 (UI-added == API-added stored shape)
//	TestP2702UplinkTypeSurvivesRestart  A6 (survives restart + legacy default)
//	TestP2702ValidationMap              §3.1 typed refusals
//	TestP2702ManualNeverAutoDials       A5 (manual never auto-dials)
//	TestP2702ScheduledWindowSemantics   A5 (window, incl. midnight crossing)
//	TestP2702StateDerivation            §1.2 derivation + §1.1 ordering rule
//	TestP2702ActionDialGates            §3.3 action semantics
//	TestP2702ServersViewKeys            A1 (every row carries the new keys)
//	TestP2702HealthTruth                §2.1 posture + §5.3 roll-up
//	TestP2702AgentSealedStamp           §2.2 wire-proved e2e stamp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// p2702Do issues one request against the test mux.
func p2702Do(srv *Server, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	srv.mux.ServeHTTP(rec, req)
	return rec
}

// TestP2702UplinkTypeStoredShape proves A4 on the stored side: the record
// persisted from the API body carries the three keys, and the read-back view
// echoes them — one shape for the UI post and the API post (D3).
func TestP2702UplinkTypeStoredShape(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body := fmt.Sprintf(`{"id":"ui-manual","upstream":%q,"token":"test-token","enabled":true,`+
		`"uplink":false,"uplink_type":"scheduled","schedule_start":"08:00","schedule_end":"18:00"}`, up.url)
	rec := p2702Do(srv, "POST", "/api/v1/peers", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	for _, key := range []string{"uplink_type", "schedule_start", "schedule_end"} {
		if !strings.Contains(rec.Body.String(), `"`+key+`"`) {
			t.Errorf("201 read-back missing %q — the form's read-back contract (§3.2): %s", key, rec.Body.String())
		}
	}

	// The stored document is the contract: read it back from disk.
	raw, err := os.ReadFile(filepath.Join(dir, "servers.json"))
	if err != nil {
		t.Fatalf("read doc: %v", err)
	}
	var doc struct {
		Peers map[string]map[string]any `json:"default_peers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("doc decode: %v", err)
	}
	rec2 := doc.Peers["ui-manual"]
	if rec2 == nil {
		// Doc shape is {peers:{...}} under doc v2; fall back to the store.
		st := srv.inventory()
		st.load()
		st.mu.Lock()
		r := st.doc.DefaultPeers["ui-manual"]
		st.mu.Unlock()
		if r.UplinkType != uplinkTypeScheduled || r.ScheduleStart != "08:00" || r.ScheduleEnd != "18:00" {
			t.Fatalf("stored record = %+v, want scheduled 08:00-18:00", r)
		}
	} else {
		if rec2["uplink_type"] != uplinkTypeScheduled || rec2["schedule_start"] != "08:00" || rec2["schedule_end"] != "18:00" {
			t.Fatalf("stored doc record = %v, want scheduled 08:00-18:00", rec2)
		}
	}
}

// TestP2702UplinkTypeSurvivesRestart proves A6 twice: a persisted scheduled
// peer re-reads as scheduled through a fresh store, and a legacy record with
// no uplink_type key reads as always-on (C1 backward compatibility).
func TestP2702UplinkTypeSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "servers.json")
	t.Setenv("PROBE_SERVERS_DB", doc)
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))

	// Write a v2 doc by hand: one scheduled peer, one legacy peer (no key).
	body := `{"version":2,"default_peers":{` +
		`"sched-1":{"id":"sched-1","upstream":"ws://127.0.0.1:9/ws","token_fp":"deadbeef",` +
		`"enabled":false,"uplink_type":"scheduled","schedule_start":"01:00","schedule_end":"02:00"},` +
		`"legacy-1":{"id":"legacy-1","upstream":"ws://127.0.0.1:8/ws","token_fp":"cafebabe","enabled":false}}}`
	if err := os.WriteFile(doc, []byte(body), 0o600); err != nil {
		t.Fatalf("seed doc: %v", err)
	}

	srv, cleanup := newV1TestServer(t)
	defer cleanup()
	st := srv.inventory()
	st.load()
	st.mu.Lock()
	sched, legacy := st.doc.DefaultPeers["sched-1"], st.doc.DefaultPeers["legacy-1"]
	st.mu.Unlock()

	if sched.UplinkType != uplinkTypeScheduled || sched.ScheduleStart != "01:00" || sched.ScheduleEnd != "02:00" {
		t.Fatalf("scheduled record did not survive reload: %+v", sched)
	}
	if got := sched.effectiveUplinkType(); got != uplinkTypeScheduled {
		t.Fatalf("effectiveUplinkType(scheduled) = %q", got)
	}
	if legacy.UplinkType != "" {
		t.Fatalf("legacy record grew a value: %q", legacy.UplinkType)
	}
	if got := legacy.effectiveUplinkType(); got != uplinkTypeAlwaysOn {
		t.Fatalf("legacy record reads %q, want always-on (C1)", got)
	}
}

// TestP2702ValidationMap pins every typed refusal §3.1 names.
func TestP2702ValidationMap(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	cases := []struct{ name, body, code string }{
		{"unknown-type",
			fmt.Sprintf(`{"id":"v1","upstream":%q,"token":"test-token","uplink_type":"sometimes"}`, up.url),
			"PEER_UPLINK_TYPE_INVALID"},
		{"inbound-with-discipline",
			fmt.Sprintf(`{"id":"v2","upstream":%q,"token":"test-token","listen":":7702","uplink_type":"manual"}`, up.url),
			"PEER_UPLINK_TYPE_UNSUPPORTED"},
		{"scheduled-without-window",
			fmt.Sprintf(`{"id":"v3","upstream":%q,"token":"test-token","uplink_type":"scheduled"}`, up.url),
			"PEER_SCHEDULE_INVALID"},
		{"scheduled-bad-clock",
			fmt.Sprintf(`{"id":"v4","upstream":%q,"token":"test-token","uplink_type":"scheduled","schedule_start":"25:99","schedule_end":"08:00"}`, up.url),
			"PEER_SCHEDULE_INVALID"},
	}
	for _, c := range cases {
		rec := p2702Do(srv, "POST", "/api/v1/peers", c.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s: %d %s, want 400 %s", c.name, rec.Code, rec.Body.String(), c.code)
		}
	}
	// Midnight-crossing window is legal (start > end wraps).
	rec := p2702Do(srv, "POST", "/api/v1/peers",
		fmt.Sprintf(`{"id":"wrap","upstream":%q,"token":"test-token","enabled":false,"uplink_type":"scheduled","schedule_start":"22:00","schedule_end":"06:00"}`, up.url))
	if rec.Code != http.StatusCreated {
		t.Errorf("midnight-crossing window refused: %d %s", rec.Code, rec.Body.String())
	}
}

// TestP2702ManualNeverAutoDials proves A5's manual half without waiting on a
// network: the lifecycle returns immediately with disabled/manual-uplink-not-
// dialed and the upstream never sees a relay_register.
func TestP2702ManualNeverAutoDials(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	body := fmt.Sprintf(`{"id":"manual-1","upstream":%q,"token":"test-token","enabled":true,"uplink_type":"manual"}`, up.url)
	if rec := p2702Do(srv, "POST", "/api/v1/peers", body); rec.Code != http.StatusCreated {
		t.Fatalf("create manual: %d %s", rec.Code, rec.Body.String())
	}
	view := p1702AwaitState(t, srv, "manual-1", peerStateDisabled, 3*time.Second)
	if got, _ := view["link_state"].(string); got != peerStateDisabled {
		t.Fatalf("manual link_state = %v, want disabled (§3.3)", got)
	}
	detail, _ := view["state_detail"].(map[string]any)
	if reason, _ := detail["reason"].(string); reason != detailManualUplinkNotDialed {
		t.Fatalf("state_detail.reason = %v, want %q", detail["reason"], detailManualUplinkNotDialed)
	}
	// The negative proof: nothing dialed. Give any errant dial a beat.
	time.Sleep(300 * time.Millisecond)
	up.s.relayMu.RLock()
	n := len(up.s.relays)
	up.s.relayMu.RUnlock()
	if n != 0 {
		t.Fatalf("manual peer auto-dialed: upstream holds %d relay session(s)", n)
	}
}

// TestP2702ScheduledWindowSemantics pins inWindowNow, including the wrap.
func TestP2702ScheduledWindowSemantics(t *testing.T) {
	now := time.Now()
	hm := func(d time.Duration) string { return now.Add(d).Format("15:04") }

	in := defaultPeerRecord{UplinkType: uplinkTypeScheduled, ScheduleStart: hm(-30 * time.Minute), ScheduleEnd: hm(30 * time.Minute)}
	if !in.inWindowNow() {
		t.Errorf("window %s-%s does not contain now (%s)", in.ScheduleStart, in.ScheduleEnd, now.Format("15:04"))
	}
	out := defaultPeerRecord{UplinkType: uplinkTypeScheduled, ScheduleStart: hm(2 * time.Hour), ScheduleEnd: hm(3 * time.Hour)}
	if out.inWindowNow() {
		t.Errorf("window %s-%s claims to contain now (%s)", out.ScheduleStart, out.ScheduleEnd, now.Format("15:04"))
	}
	// Midnight crossing: a window that spans 00:00 must be evaluated as a
	// wrap, not as an empty set. Construct it from the clock so the test is
	// stable at any hour: start = now+2h, end = now-2h (wraps through 00:00).
	wrap := defaultPeerRecord{UplinkType: uplinkTypeScheduled, ScheduleStart: hm(2 * time.Hour), ScheduleEnd: hm(1 * time.Hour)}
	if !wrap.inWindowNow() {
		t.Errorf("midnight-crossing window %s-%s excluded now — wrap semantics broken", wrap.ScheduleStart, wrap.ScheduleEnd)
	}
}

// TestP2702StateDerivation pins §1.2 and the §1.1 ordering rule.
func TestP2702StateDerivation(t *testing.T) {
	if got := uplinkStateOf(peerConfigView{Enabled: false, LinkState: peerStateConnected}); got != peerStateDisabled {
		t.Errorf("enabled=false with a connected link = %q, want disabled (ordering rule)", got)
	}
	if got := uplinkStateOf(peerConfigView{Enabled: true, LinkState: ""}); got != peerStateDisabled {
		t.Errorf("no runtime state = %q, want disabled", got)
	}
	for _, st := range []string{peerStateConnected, peerStateRetrying, peerStateAuthRejected, peerStateVersionMismatch, peerStateUnreachable} {
		if got := uplinkStateOf(peerConfigView{Enabled: true, LinkState: st}); got != st {
			t.Errorf("state %q mangled to %q", st, got)
		}
	}

	row := serverEntry{State: "up"}
	normalizeUplinkRow(&row)
	if row.State != peerStateConnected || !row.InWindow || !row.Enabled || row.Encrypted.Payload != "none" {
		t.Errorf("bootstrap normalization = %+v", row)
	}
	row = serverEntry{State: "down"}
	normalizeUplinkRow(&row)
	if row.State != peerStateUnreachable {
		t.Errorf("down mapped to %q, want unreachable", row.State)
	}
	if got := linkTransport("wss://example:443/ws"); got != "wss" {
		t.Errorf("transport(wss) = %q", got)
	}
	if got := linkTransport("ws://example:80/ws"); got != "ws" {
		t.Errorf("transport(ws) = %q", got)
	}
}

// TestP2702ActionDialGates proves §3.3's refusals: always-on needs no kick,
// scheduled outside its window is closed.
func TestP2702ActionDialGates(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// always-on: dial is the loop's job.
	if rec := p2702Do(srv, "POST", "/api/v1/peers",
		fmt.Sprintf(`{"id":"ao-1","upstream":%q,"token":"test-token","enabled":false}`, up.url)); rec.Code != http.StatusCreated {
		t.Fatalf("create always-on: %d %s", rec.Code, rec.Body.String())
	}
	rec := p2702Do(srv, "POST", "/api/v1/peers/ao-1/action", `{"action":"dial"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "PEER_NOT_MANUAL") {
		t.Errorf("dial on always-on: %d %s, want 409 PEER_NOT_MANUAL", rec.Code, rec.Body.String())
	}

	// scheduled, outside window (window is 2-3h from now, so never now).
	now := time.Now()
	body := fmt.Sprintf(`{"id":"sc-1","upstream":%q,"token":"test-token","enabled":false,"uplink_type":"scheduled","schedule_start":%q,"schedule_end":%q}`,
		up.url, now.Add(2*time.Hour).Format("15:04"), now.Add(3*time.Hour).Format("15:04"))
	if rec := p2702Do(srv, "POST", "/api/v1/peers", body); rec.Code != http.StatusCreated {
		t.Fatalf("create scheduled: %d %s", rec.Code, rec.Body.String())
	}
	rec = p2702Do(srv, "POST", "/api/v1/peers/sc-1/action", `{"action":"dial"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "SCHEDULE_WINDOW_CLOSED") {
		t.Errorf("dial outside window: %d %s, want 409 SCHEDULE_WINDOW_CLOSED", rec.Code, rec.Body.String())
	}
}

// TestP2702ServersViewKeys proves A1's key contract on every row, live row
// included, and that a configured-but-idle peer still gets a row (source
// peers) — the join that makes "wired, carrying nothing" visible.
func TestP2702ServersViewKeys(t *testing.T) {
	up := p1702StartUpstream(t)
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	if rec := p2702Do(srv, "POST", "/api/v1/peers",
		fmt.Sprintf(`{"id":"view-peer","upstream":%q,"token":"test-token","enabled":true,"uplink_type":"manual"}`, up.url)); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec := p2702Do(srv, "GET", "/api/v1/servers", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/servers: %d %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data struct {
			Servers []map[string]json.RawMessage `json:"servers"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Servers) < 2 {
		t.Fatalf("servers = %d rows, want local + the configured peer", len(env.Data.Servers))
	}
	keys := []string{"id", "address", "direction", "state", "uplink_type",
		"schedule_window", "in_window", "enabled", "last_seen", "peer_version",
		"required_min", "encrypted", "carrying", "token_fp", "source"}
	for i, row := range env.Data.Servers {
		for _, k := range keys {
			if _, ok := row[k]; !ok {
				t.Errorf("row %d missing key %q (R3: every key on every row)", i, k)
			}
		}
	}
	// The configured peer must be present with its discipline, not omitted.
	found := false
	for _, row := range env.Data.Servers {
		var id string
		_ = json.Unmarshal(row["id"], &id)
		if id != "view-peer" {
			continue
		}
		found = true
		var ty string
		_ = json.Unmarshal(row["uplink_type"], &ty)
		var src string
		_ = json.Unmarshal(row["source"], &src)
		if ty != uplinkTypeManual || src != "peers" {
			t.Errorf("view-peer row: uplink_type=%q source=%q", ty, src)
		}
		var st string
		_ = json.Unmarshal(row["state"], &st)
		if st != peerStateDisabled {
			t.Errorf("idle manual peer state = %q, want disabled (never green while dead)", st)
		}
	}
	if !found {
		t.Fatal("configured peer absent from /api/v1/servers — the join did not run")
	}
}

// TestP2702HealthTruth proves §2.1 (posture object from the manager) and the
// §5.3 roll-up keys exist with honest counts.
func TestP2702HealthTruth(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PROBE_SERVERS_DB", filepath.Join(dir, "servers.json"))
	t.Setenv("PROBE_PEER_SECRET_STORE", filepath.Join(dir, "peer-secrets.json"))
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	rec := p2702Do(srv, "GET", "/api/v1/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/health: %d", rec.Code)
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	h := env.Data
	e2e, ok := h["e2e"].(map[string]any)
	if !ok {
		t.Fatalf("health has no e2e object: %v", h)
	}
	if _, ok := e2e["active"].(bool); !ok {
		t.Errorf("e2e.active is not a bool: %v", e2e["active"])
	}
	if _, ok := e2e["cipher_error"].(string); !ok {
		t.Errorf("e2e.cipher_error is not a string: %v", e2e["cipher_error"])
	}
	for _, k := range []string{"uplinks_total", "uplinks_connected", "uplinks_carrying"} {
		if _, ok := h[k]; !ok {
			t.Errorf("health missing %q (§5.3 rule 6)", k)
		}
	}
}

// TestP2702AgentSealedStamp proves §2.2's registry projection: the stamp is
// wire-proved truth with an observation time, and it serializes as e2e.
func TestP2702AgentSealedStamp(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "registry.json"))
	reg.Register("agent-1", "agent-1", "v1", "linux", "amd64", "full", nil)
	reg.MarkE2E("agent-1", true)

	var rec *AgentRecord
	for _, a := range reg.ListAgents() {
		if a.AgentID == "agent-1" {
			cp := a
			rec = &cp
		}
	}
	if rec == nil || rec.E2E == nil {
		t.Fatal("e2e stamp missing after MarkE2E")
	}
	if !rec.E2E.Sealed || rec.E2E.ObservedAt == "" {
		t.Errorf("stamp = %+v, want sealed with observed_at", rec.E2E)
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"e2e":{"sealed":true`) {
		t.Errorf("agent row does not serialize e2e: %s", raw)
	}

	// A never-classified agent renders null, not a false claim.
	reg.Register("agent-2", "agent-2", "v1", "linux", "amd64", "full", nil)
	for _, a := range reg.ListAgents() {
		if a.AgentID == "agent-2" && a.E2E != nil {
			t.Errorf("unobserved agent carries a claim: %+v", a.E2E)
		}
	}
}
