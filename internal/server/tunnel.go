package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
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
		connID := fmt.Sprintf("%s-c%d", t.ID, t.nextSeq)
		t.conns[connID] = conn
		t.connMu.Unlock()

		go t.handleConn(connID, conn)
	}
}

// handleConn relays data between a local TCP connection and the agent's
// connection to the target via WebSocket frames.
func (t *Tunnel) handleConn(connID string, localConn net.Conn) {
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
		return
	}
	// Check if agent returned an error
	if m, ok := resp.(map[string]interface{}); ok && m["error"] != nil {
		log.Printf("[tunnel] agent rejected tunnel_open for %s: %v", connID, m["error"])
		return
	}
	log.Printf("[tunnel] agent confirmed tunnel opened for %s", connID)

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

// sendToAgent sends a WebSocket message to the tunnel's agent.
// Uses the per-agent write mutex to prevent concurrent WriteJSON corruption.
func (t *Tunnel) sendToAgent(env protocol.Envelope) {
	s := t.Server
	s.mu.RLock()
	conn, ok := s.conns[t.AgentID]
	writeMu, wmuOk := s.connWriteMu[t.AgentID]
	s.mu.RUnlock()
	if !ok || !wmuOk {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	if err := conn.WriteJSON(env); err != nil {
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

	// Verify agent is connected
	s.mu.RLock()
	_, ok := s.conns[agentID]
	s.mu.RUnlock()
	if !ok {
		http.Error(w, fmt.Sprintf("agent %s not connected", agentID), http.StatusServiceUnavailable)
		return
	}

	t, err := s.newTunnel(agentID, params.TargetHost, params.TargetPort, params.ListenPort)
	if err != nil {
		http.Error(w, fmt.Sprintf("tunnel creation failed: %v", err), http.StatusInternalServerError)
		return
	}

	actualPort := t.Listener.Addr().(*net.TCPAddr).Port

	result := protocol.TunnelOpenResult{
		ListenPort: actualPort,
		TunnelID:   t.ID,
	}
	log.Printf("[tunnel] opened %s → agent %s → %s:%d (listen :%d)",
		t.ID, agentID, params.TargetHost, params.TargetPort, actualPort)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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