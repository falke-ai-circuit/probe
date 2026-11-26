package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// Tunnel represents a server-side TCP listener that relays connections
// through the WebSocket to the agent, which connects to the target.
type Tunnel struct {
	ID         string
	AgentID    string
	TargetHost string
	TargetPort int
	Listener   net.Listener
	Server     *Server

	// connMap maps local connection IDs to the agent-side connection.
	connMu  sync.Mutex
	conns   map[string]net.Conn // connID → local TCP conn
	nextSeq int

	// openEvents carries the outcome of each accepted connection's agent-side
	// open. A create that has just proved its listener accepts waits on this
	// for the connection it dialled, so `verified` is not published until the
	// route the caller will use actually exists. Sends are non-blocking: a
	// long-lived tunnel with no waiter must not stall its relay goroutines.
	openEvents chan tunnelOpenEvent
}

// tunnelOpenEvent is the per-connection outcome of the agent-side tunnel_open.
// seq is the accept sequence of the local connection, so a waiter only accepts
// the event for the connection it opened (a stale event cannot satisfy it).
type tunnelOpenEvent struct {
	seq int
	err error
}

// newTunnel creates a tunnel, starts the TCP listener, and tells the agent
// to be ready for tunnel data.
func (s *Server) newTunnel(agentID string, targetHost string, targetPort int, listenPort int) (*Tunnel, error) {
	s.tunnelMu.Lock()
	s.tunnelCount++
	id := fmt.Sprintf("tun-%d", s.tunnelCount)
	s.tunnelMu.Unlock()

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", listenPort))
	if err != nil {
		return nil, fmt.Errorf("listen failed: %w", err)
	}

	t := &Tunnel{
		ID:         id,
		AgentID:    agentID,
		TargetHost: targetHost,
		TargetPort: targetPort,
		Listener:   ln,
		Server:     s,
		conns:      make(map[string]net.Conn),
		openEvents: make(chan tunnelOpenEvent, 64),
	}

	s.tunnelMu.Lock()
	s.tunnels[id] = t
	s.tunnelMu.Unlock()

	go t.acceptLoop()
	return t, nil
}

// acceptLoop accepts TCP connections on the server side. For each connection,
// it sends a tunnel_open to the agent (which connects to the target), then
// relays data bidirectionally.
func (t *Tunnel) acceptLoop() {
	for {
		conn, err := t.Listener.Accept()
		if err != nil {
			// Listener closed
			return
		}
		t.connMu.Lock()
		t.nextSeq++
		seq := t.nextSeq
		connID := fmt.Sprintf("%s-c%d", t.ID, t.nextSeq)
		t.conns[connID] = conn
		t.connMu.Unlock()

		go t.handleConn(connID, seq, conn)
	}
}

// acceptedSeq reports the accept sequence of the most recent connection. A
// create snapshots it before dialling its own loopback connection, so a stale
// event from an earlier connection cannot stand in for the one it opened.
func (t *Tunnel) acceptedSeq() int {
	t.connMu.Lock()
	defer t.connMu.Unlock()
	return t.nextSeq
}

// reportOpen publishes one connection's agent-side open outcome to any create
// waiting for the data path. It never blocks: with nobody waiting the buffered
// channel would otherwise fill and stop the relay goroutine that reports.
func (t *Tunnel) reportOpen(seq int, err error) {
	select {
	case t.openEvents <- tunnelOpenEvent{seq: seq, err: err}:
	default:
	}
}

// handleConn relays data between a local TCP connection and the agent's
// connection to the target via WebSocket frames.
func (t *Tunnel) handleConn(connID string, seq int, localConn net.Conn) {
	// Disable Nagle's algorithm for lower latency
	if tcpConn, ok := localConn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
	}
	defer func() {
		localConn.Close()
		t.connMu.Lock()
		delete(t.conns, connID)
		t.connMu.Unlock()
		// Tell agent to close its side
		t.sendToAgent(protocol.Envelope{
			ID:   fmt.Sprintf("%s-close-%d", t.ID, time.Now().UnixMilli()),
			Type: protocol.TypeTunnelClose,
			Params: mustMarshalRawParams(protocol.TunnelCloseParams{
				TunnelID: connID,
			}),
		})
	}()

	// Send tunnel_open to agent and WAIT for response — the agent must
	// connect to the target before we start relaying data.
	openParams := map[string]interface{}{
		"tunnel_id":    connID,
		"target_host":  t.TargetHost,
		"target_port":  t.TargetPort,
	}

	// Use forwardToAgentWithTimeout to wait for agent to confirm tunnel opened
	resp, err := t.Server.forwardToAgentWithTimeout(t.AgentID, protocol.TypeTunnelOpen, openParams, 10*time.Second, "")
	if err != nil {
		log.Printf("[tunnel] tunnel_open failed for %s: %v", connID, err)
		// A create waiting on this connection must learn the route never
		// existed, so it refuses instead of publishing the port.
		t.reportOpen(seq, err)
		return
	}
	// Check if agent returned an error
	if m, ok := resp.(map[string]interface{}); ok && m["error"] != nil {
		log.Printf("[tunnel] agent rejected tunnel_open for %s: %v", connID, m["error"])
		t.reportOpen(seq, fmt.Errorf("%v", m["error"]))
		return
	}
	log.Printf("[tunnel] agent confirmed tunnel opened for %s", connID)
	// The route the caller's bytes will take is now established end to end:
	// the agent holds a live connection to the target for this local conn.
	t.reportOpen(seq, nil)

	// Read from local connection, send to agent as tunnel_data
	buf := make([]byte, 64*1024)
	for {
		n, err := localConn.Read(buf)
		if n > 0 {
			data := base64.StdEncoding.EncodeToString(buf[:n])
			t.sendToAgent(protocol.Envelope{
				ID:   fmt.Sprintf("%s-data-%d", t.ID, time.Now().UnixMilli()),
				Type: protocol.TypeTunnelData,
				Params: mustMarshalRawParams(protocol.TunnelDataParams{
					TunnelID:  connID,
					Direction: "client→target",
					Data:      data,
				}),
			})
		}
		if err != nil {
			return
		}
	}
}

// WriteToConn writes data to a local TCP connection (data coming from the
// agent/target side). Called when the server receives tunnel_data from the
// agent with direction "target→client".
func (t *Tunnel) WriteToConn(connID string, data []byte) error {
	t.connMu.Lock()
	conn, ok := t.conns[connID]
	t.connMu.Unlock()
	if !ok {
		return fmt.Errorf("connection %s not found", connID)
	}
	_, err := conn.Write(data)
	return err
}

// Close stops the tunnel listener and closes all connections.
func (t *Tunnel) Close() {
	t.Listener.Close()
	t.connMu.Lock()
	for _, conn := range t.conns {
		conn.Close()
	}
	t.conns = make(map[string]net.Conn)
	t.connMu.Unlock()
}

// sendToAgent sends a WebSocket message to the tunnel's agent. Uses the single
// resolver (P21-05): a relayed agent is reachable by its own id, and the write
// lock discipline comes with the transport.
func (t *Tunnel) sendToAgent(env protocol.Envelope) {
	s := t.Server
	tr, err := s.resolveAgentTransport(t.AgentID)
	if err != nil {
		log.Printf("[tunnel] send to agent %s refused: %v", t.AgentID, err)
		return
	}
	if tr.NeedsWriteLock {
		tr.WriteMu.Lock()
		defer tr.WriteMu.Unlock()
	}
	if err := tr.Conn.WriteJSON(env); err != nil {
		log.Printf("[tunnel] send to agent failed: %v", err)
	}
}

func mustMarshalRawParams(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// --- HTTP Handlers for tunnel and sniff ---

// handleAgentTunnel opens a TCP tunnel: server listens on a local port,
// and relays connections through the agent to the target.
// POST /api/agent/{id}/tunnel
// {"target_host": "127.0.0.1", "target_port": 1234, "listen_port": 0}
func (s *Server) handleAgentTunnel(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.TunnelParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if params.TargetHost == "" || params.TargetPort == 0 {
		http.Error(w, "target_host and target_port required", http.StatusBadRequest)
		return
	}

	// Verify the agent can be dispatched. P21-01: the answer comes from the
	// one predicate, and the refusal states its reason instead of a bare
	// "not connected". A dispatchable agent is never refused here for absence.
	if dispatchable, _, reason := s.AgentDispatchability(agentID); !dispatchable {
		http.Error(w, reason, http.StatusServiceUnavailable)
		return
	}

	// P21-06: a listen port is a promise, so the create call does not return
	// until two proofs are in hand. (1) The agent dials the target and reports
	// readiness — the agent's own error is the answer when that fails. (2) The
	// server dials the port it is about to publish. Either failure tears the
	// listener down and answers with an error envelope that has no listen_port
	// key at all: absence is the contract, because a caller that sees the key
	// will dial a port that does not work.
	t, err := s.newTunnel(agentID, params.TargetHost, params.TargetPort, params.ListenPort)
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel creation failed: %v", err), http.StatusInternalServerError)
		return
	}

	actualPort := t.Listener.Addr().(*net.TCPAddr).Port

	if fail := s.confirmTunnelTarget(agentID, t); fail != nil {
		s.abandonTunnel(t)
		s.writeTunnelFailure(w, agentID, fail)
		return
	}

	// The create dials its own loopback connection; the sequence snapshot
	// before the dial is what identifies that connection's open event.
	loopbackSeq := t.acceptedSeq()

	ok, latency, dialErr := tunnelLoopbackProbe(actualPort)
	if !ok {
		s.abandonTunnel(t)
		s.writeTunnelFailure(w, agentID, &tunnelFailure{
			Status:  http.StatusBadGateway,
			Code:    "TUNNEL_LOOPBACK_FAILED",
			Message: fmt.Sprintf("listen port %d did not accept a connection from the server: %s", actualPort, dialErr),
			Detail:  map[string]interface{}{"agent_id": agentID, "tunnel_id": t.ID, "port_not_dialable": true},
		})
		return
	}

	// A dialable listener is not a working tunnel: accept() succeeds before
	// the agent has opened its side, so a port published here can accept a
	// caller's connection and then drop it. Wait for the agent's tunnel_open
	// answer on THAT connection — the same route the caller's bytes take —
	// before anything is published.
	if pathErr := s.confirmTunnelDataPath(t, loopbackSeq, tunnelDataPathTimeout); pathErr != nil {
		s.abandonTunnel(t)
		s.writeTunnelFailure(w, agentID, &tunnelFailure{
			Status:  http.StatusBadGateway,
			Code:    "TUNNEL_DATA_PATH_FAILED",
			Message: fmt.Sprintf("listen port %d accepted the server's loopback connection but the agent opened no route through it: %v; no listen port was published", actualPort, pathErr),
			Detail:  map[string]interface{}{"agent_id": agentID, "tunnel_id": t.ID, "port_not_usable": true},
		})
		return
	}

	result := protocol.TunnelOpenResult{
		ListenPort: actualPort,
		TunnelID:   t.ID,
		Verified:   true,
		Loopback:   &protocol.TunnelLoopbackResult{OK: true, LatencyMS: latency},
	}
	log.Printf("[tunnel] opened %s → agent %s → %s:%d (listen :%d, loopback %dms)",
		t.ID, agentID, params.TargetHost, params.TargetPort, actualPort, latency)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// Timeouts for the two create-time proofs (P21-06). They are variables because
// a test that exercises the timeout path must not spend the production budget;
// production uses these values.
var (
	// tunnelReadyTimeout bounds the wait for the agent's bind confirmation.
	// The agent's own dial is bounded at 3s (agent.tunnelProbeDialTimeout),
	// so an unreachable or blackholed target still produces the agent's own
	// answer inside this window instead of a bare server timeout.
	tunnelReadyTimeout = 5 * time.Second
	// tunnelLoopbackTimeout bounds the server's dial of the port it opened.
	tunnelLoopbackTimeout = 2 * time.Second
	// tunnelDataPathTimeout bounds the wait for the agent's tunnel_open answer
	// on the loopback connection. It is the create's data-path proof: the
	// agent must hold a live target connection for the connection that was
	// accepted before verified:true is published.
	tunnelDataPathTimeout = 5 * time.Second
)

// tunnelLoopbackProbe is the server-side proof. It is a variable so a test can
// force the failure branch deterministically; production dials for real.
var tunnelLoopbackProbe = dialTunnelLoopback

// dialTunnelLoopback dials the freshly-opened listen port from this process and
// reports the handshake time. ok=false means the port the caller was about to
// be given is not usable, which is a failure response, not a warning.
func dialTunnelLoopback(port int) (bool, int64, string) {
	start := time.Now()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), tunnelLoopbackTimeout)
	if err != nil {
		return false, 0, err.Error()
	}
	latency := time.Since(start).Milliseconds()
	conn.Close()
	return true, latency, ""
}

// confirmTunnelDataPath waits for the agent's tunnel_open answer on the
// connection accepted after afterSeq — the loopback connection the create just
// dialled. nil means the agent holds a live connection to the target for that
// local conn, so a caller's bytes have a route at the moment verified:true is
// published. Any other answer means the port accepts and carries nothing, which
// is a refusal, not a success with a caveat.
func (s *Server) confirmTunnelDataPath(t *Tunnel, afterSeq int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev := <-t.openEvents:
			if ev.seq <= afterSeq {
				// An event for an earlier connection cannot prove this one.
				continue
			}
			return ev.err
		case <-deadline.C:
			return fmt.Errorf("the agent did not confirm the tunnel connection within %v", timeout)
		}
	}
}

// tunnelFailure is a refused create: typed code, the sentence the caller sees,
// and the HTTP status. It carries no port in any field.
type tunnelFailure struct {
	Status  int
	Code    string
	Message string
	Detail  map[string]interface{}
}

// confirmTunnelTarget asks the agent to prove the target is reachable. nil
// means the agent confirmed. A refusal that never reached the wire keeps the
// dispatch predicate's status (503 absence / 502 agent-side error); a sent
// command that never answered is the timeout case and has its own code.
func (s *Server) confirmTunnelTarget(agentID string, t *Tunnel) *tunnelFailure {
	params := protocol.TunnelProbeParams{
		TunnelID:   t.ID,
		TargetHost: t.TargetHost,
		TargetPort: t.TargetPort,
	}
	resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeTunnelProbe, params, tunnelReadyTimeout, "")
	if err != nil {
		var refusal *ForwardRefusal
		if errors.As(err, &refusal) {
			// The probe never left the server: this unit must not turn the
			// predicate's own answer into a timeout lie.
			d := s.dispatchRefusal(agentID, "tunnel", err)
			return &tunnelFailure{Status: d.Status, Code: d.Code, Message: d.Message, Detail: d.Detail}
		}
		return &tunnelFailure{
			Status: http.StatusBadGateway,
			Code:   "TUNNEL_READY_TIMEOUT",
			Message: fmt.Sprintf("agent %s did not confirm the tunnel target within %v; no listen port was published",
				agentID, tunnelReadyTimeout),
			Detail: map[string]interface{}{
				"agent_id":   agentID,
				"tunnel_id":  t.ID,
				"timeout_ms": tunnelReadyTimeout.Milliseconds(),
				"ran":        false,
			},
		}
	}
	if m, ok := resp.(map[string]interface{}); ok && m["error"] != nil {
		detail := map[string]interface{}{"agent_id": agentID, "tunnel_id": t.ID, "ran": false}
		if code, has := m["code"]; has {
			detail["agent_code"] = code
		}
		return &tunnelFailure{
			Status:  http.StatusBadGateway,
			Code:    "TUNNEL_BIND_FAILED",
			Message: fmt.Sprint(m["error"]),
			Detail:  detail,
		}
	}
	return nil
}

// abandonTunnel undoes a create that failed verification: the tunnel leaves the
// map and the listener stops accepting. Called on every refusal path so a
// timed-out or refused call cannot leak a listening socket. The proof is
// external: the refused request's port answers nothing (tests dial it).
func (s *Server) abandonTunnel(t *Tunnel) {
	s.tunnelMu.Lock()
	delete(s.tunnels, t.ID)
	s.tunnelMu.Unlock()
	t.Close()
}

// writeTunnelFailure renders a refused create. No listen_port key is written,
// not even as null or zero.
func (s *Server) writeTunnelFailure(w http.ResponseWriter, agentID string, f *tunnelFailure) {
	log.Printf("[tunnel] create refused for agent %s: %s: %s", agentID, f.Code, f.Message)
	writeErrorDetail(w, f.Status, f.Code, f.Message, f.Detail)
}

// handleAgentTunnelClose closes a tunnel.
// POST /api/agent/{id}/tunnel-close  {"tunnel_id": "tun-1"}
func (s *Server) handleAgentTunnelClose(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.TunnelCloseParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	s.tunnelMu.Lock()
	t, ok := s.tunnels[params.TunnelID]
	delete(s.tunnels, params.TunnelID)
	s.tunnelMu.Unlock()

	if !ok {
		http.Error(w, "tunnel not found", http.StatusNotFound)
		return
	}

	t.Close()
	log.Printf("[tunnel] closed %s", params.TunnelID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"closed": true, "tunnel_id": params.TunnelID})
}

// handleAgentTunnelList returns all active tunnels for an agent.
// GET /api/agent/{id}/tunnels
func (s *Server) handleAgentTunnelList(w http.ResponseWriter, r *http.Request, agentID string) {
	type tunnelInfo struct {
		ID         string `json:"tunnel_id"`
		ListenPort int    `json:"listen_port"`
		TargetHost string `json:"target_host"`
		TargetPort int    `json:"target_port"`
		AgentID    string `json:"agent_id"`
		Conns      int    `json:"connections"`
	}

	s.tunnelMu.RLock()
	defer s.tunnelMu.RUnlock()

	var list []tunnelInfo
	for _, t := range s.tunnels {
		if t.AgentID != agentID {
			continue
		}
		port := 0
		if addr, ok := t.Listener.Addr().(*net.TCPAddr); ok {
			port = addr.Port
		}
		t.connMu.Lock()
		connCount := len(t.conns)
		t.connMu.Unlock()
		list = append(list, tunnelInfo{
			ID:         t.ID,
			ListenPort: port,
			TargetHost: t.TargetHost,
			TargetPort: t.TargetPort,
			AgentID:    t.AgentID,
			Conns:      connCount,
		})
	}
	if list == nil {
		list = []tunnelInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "data": map[string]interface{}{"tunnels": list}})
}

// handleAgentSniff starts a traffic sniffer: connects to target through
// the agent, relays traffic, and captures all data for inspection.
// POST /api/agent/{id}/sniff
// {"target_host": "127.0.0.1", "target_port": 1234, "duration": 0}
//
// The sniff endpoint creates a tunnel with capture mode. Data frames are
// stored and can be retrieved via the sniffData callback.
func (s *Server) handleAgentSniff(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params protocol.SniffParams
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	if params.TargetHost == "" || params.TargetPort == 0 {
		http.Error(w, "target_host and target_port required", http.StatusBadRequest)
		return
	}

	// A sniff is a tunnel with capture logging. We create a tunnel and
	// mark it as a sniff session. The handleTunnelData method logs all data.
	t, err := s.newTunnel(agentID, params.TargetHost, params.TargetPort, 0)
	if err != nil {
		http.Error(w, fmt.Sprintf("sniff creation failed: %v", err), http.StatusInternalServerError)
		return
	}

	actualPort := t.Listener.Addr().(*net.TCPAddr).Port

	// If duration is set, auto-close after that time
	if params.Duration > 0 {
		go func() {
			time.Sleep(time.Duration(params.Duration) * time.Second)
			s.tunnelMu.Lock()
			if tun, ok := s.tunnels[t.ID]; ok {
				delete(s.tunnels, t.ID)
				tun.Close()
			}
			s.tunnelMu.Unlock()
		}()
	}

	result := protocol.SniffStartResult{
		SniffID:  t.ID,
		Captures: 0,
	}
	log.Printf("[sniff] started %s → agent %s → %s:%d (listen :%d, duration=%ds)",
		t.ID, agentID, params.TargetHost, params.TargetPort, actualPort, params.Duration)

	// Return the listen port and ID — the caller connects to the listen port
	// to trigger traffic, and the data is captured by the tunnel relay.
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"sniff_id":    result.SniffID,
		"listen_port": actualPort,
		"captures":    0,
	})
}

// handleAgentSniffStop stops a sniff session.
// POST /api/agent/{id}/sniff-stop  {"sniff_id": "tun-1"}
func (s *Server) handleAgentSniffStop(w http.ResponseWriter, r *http.Request, agentID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var params struct {
		SniffID string `json:"sniff_id"`
	}
	if err := json.Unmarshal(body, &params); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON: %v", err), http.StatusBadRequest)
		return
	}

	s.tunnelMu.Lock()
	t, ok := s.tunnels[params.SniffID]
	delete(s.tunnels, params.SniffID)
	s.tunnelMu.Unlock()

	if !ok {
		http.Error(w, "sniff not found", http.StatusNotFound)
		return
	}

	t.Close()
	log.Printf("[sniff] stopped %s", params.SniffID)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"stopped": true, "sniff_id": params.SniffID})
}

// handleTunnelDataFromAgent processes tunnel_data messages from the agent
// (target→client direction). It writes the data to the local TCP connection.
func (s *Server) handleTunnelDataFromAgent(env protocol.Envelope) {
	var params protocol.TunnelDataParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		log.Printf("[tunnel] failed to parse tunnel_data: %v", err)
		return
	}

	// Extract tunnel ID from connID (format: "tun-N-cM")
	// The connID includes the tunnel prefix, but we stored it as the full connID
	// Try to find the tunnel by matching the prefix
	s.tunnelMu.Lock()
	var t *Tunnel
	for _, tun := range s.tunnels {
		if len(params.TunnelID) >= len(tun.ID) && params.TunnelID[:len(tun.ID)] == tun.ID {
			t = tun
			break
		}
	}
	s.tunnelMu.Unlock()

	if t == nil {
		log.Printf("[tunnel] tunnel not found for conn %s", params.TunnelID)
		return
	}

	data, err := base64.StdEncoding.DecodeString(params.Data)
	if err != nil {
		log.Printf("[tunnel] failed to decode tunnel_data: %v", err)
		return
	}

	if err := t.WriteToConn(params.TunnelID, data); err != nil {
		log.Printf("[tunnel] write to conn failed: %v", err)
	}
}
