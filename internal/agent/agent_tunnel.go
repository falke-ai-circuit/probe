package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// agentTunnel manages the agent-side of a TCP tunnel: a connection to the
// target host:port and a WebSocket connection back to the server.
type agentTunnel struct {
	connID   string
	target   net.Conn
	wsConn   *websocket.Conn
	closed   bool
	mu       sync.Mutex
}

// tunnelManager manages all active tunnels on the agent side.
type tunnelManager struct {
	mu      sync.Mutex
	tunnels map[string]*agentTunnel // connID → tunnel
}

func newTunnelManager() *tunnelManager {
	return &tunnelManager{
		tunnels: make(map[string]*agentTunnel),
	}
}

// handleTunnelOpen is called when the server sends a tunnel_open message.
// The agent connects to the target and relays data.
func (a *Agent) handleTunnelOpen(env protocol.Envelope) protocol.Envelope {
	// Tunnel open from server means: server accepted a local connection,
	// now agent should connect to the target. The connID is in params.
	var params protocol.TunnelDataParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	// Actually, tunnel_open carries the target info and connID.
	// We need to re-parse — the params might be TunnelParams + connID
	var openParams struct {
		TunnelID   string `json:"tunnel_id"`   // connID for this connection
		TargetHost string `json:"target_host"`
		TargetPort int    `json:"target_port"`
	}
	if err := json.Unmarshal(env.Params, &openParams); err != nil {
		// Fall back to trying TunnelDataParams
		openParams.TargetHost = ""
		openParams.TunnelID = params.TunnelID
	}

	// If no target info, we need to use the one from the initial tunnel setup.
	// For simplicity, the server sends the target in every tunnel_open.
	if openParams.TargetHost == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "missing target_host")
	}

	targetAddr := fmt.Sprintf("%s:%d", openParams.TargetHost, openParams.TargetPort)
	conn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("connect to %s failed: %v", targetAddr, err))
	}

	// Disable Nagle's algorithm for lower latency on tunnel connections
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		tcpConn.SetNoDelay(true)
	}

	tun := &agentTunnel{
		connID: openParams.TunnelID,
		target: conn,
		wsConn: a.conn,
	}

	a.tunnelMgr.mu.Lock()
	a.tunnelMgr.tunnels[openParams.TunnelID] = tun
	a.tunnelMgr.mu.Unlock()

	// Start reading from target, sending to server as tunnel_data
	go a.relayTargetToServer(openParams.TunnelID, conn)

	return protocol.NewResult(env.ID, protocol.TypeTunnelOpened, map[string]interface{}{
		"tunnel_id": openParams.TunnelID,
		"target":    targetAddr,
	})
}

// relayTargetToServer reads data from the target connection and sends it
// to the server as tunnel_data frames (direction: target→client).
func (a *Agent) relayTargetToServer(connID string, target net.Conn) {
	buf := make([]byte, 64*1024)
	for {
		n, err := target.Read(buf)
		if n > 0 {
			data := base64.StdEncoding.EncodeToString(buf[:n])
			env := protocol.Envelope{
				ID:   fmt.Sprintf("tun-reply-%d", time.Now().UnixMilli()),
				Type: protocol.TypeTunnelData,
				Params: mustMarshal(protocol.TunnelDataParams{
					TunnelID:  connID,
					Direction: "target→client",
					Data:      data,
				}),
			}
			if a.conn != nil {
				if err := a.writeMessage(a.conn, env); err != nil {
					log.Printf("[tunnel] send to server failed: %v", err)
					return
				}
			}
		}
		if err != nil {
			break
		}
	}

	// Connection closed — clean up
	a.tunnelMgr.mu.Lock()
	if tun, ok := a.tunnelMgr.tunnels[connID]; ok {
		tun.mu.Lock()
		tun.closed = true
		tun.mu.Unlock()
		delete(a.tunnelMgr.tunnels, connID)
	}
	a.tunnelMgr.mu.Unlock()
}

// handleTunnelData is called when the server sends tunnel_data (client→target).
// The agent writes the data to the target connection.
func (a *Agent) handleTunnelData(env protocol.Envelope) protocol.Envelope {
	var params protocol.TunnelDataParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.tunnelMgr.mu.Lock()
	tun, ok := a.tunnelMgr.tunnels[params.TunnelID]
	a.tunnelMgr.mu.Unlock()

	if !ok {
		// Tunnel might not be open yet — this is data for a new connection.
		// The server sends tunnel_data with direction "client→target" containing
		// the initial data. We need to open the connection first.
		// Actually, the server should send tunnel_open first. If we get data
		// without an open tunnel, it's an error.
		return protocol.NewError(env.ID, protocol.ErrNotFound, fmt.Sprintf("tunnel %s not found", params.TunnelID))
	}

	data, err := base64.StdEncoding.DecodeString(params.Data)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "invalid base64 data")
	}

	tun.mu.Lock()
	defer tun.mu.Unlock()
	if tun.closed {
		return protocol.NewError(env.ID, protocol.ErrInternal, "tunnel closed")
	}

	_, err = tun.target.Write(data)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("write to target failed: %v", err))
	}

	// Acknowledge — no explicit response needed, but send one for flow control
	return protocol.NewResult(env.ID, protocol.TypeTunnelData, map[string]interface{}{
		"tunnel_id": params.TunnelID,
		"written":   len(data),
	})
}

// handleTunnelClose closes a tunnel connection on the agent side.
func (a *Agent) handleTunnelClose(env protocol.Envelope) protocol.Envelope {
	var params protocol.TunnelCloseParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.tunnelMgr.mu.Lock()
	tun, ok := a.tunnelMgr.tunnels[params.TunnelID]
	if ok {
		delete(a.tunnelMgr.tunnels, params.TunnelID)
	}
	a.tunnelMgr.mu.Unlock()

	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "tunnel not found")
	}

	tun.mu.Lock()
	tun.closed = true
	tun.target.Close()
	tun.mu.Unlock()

	return protocol.NewResult(env.ID, protocol.TypeTunnelClosed, map[string]interface{}{
		"tunnel_id": params.TunnelID,
		"closed":    true,
	})
}

// closeAllTunnels closes all active tunnel connections (called on disconnect).
func (a *Agent) closeAllTunnels() {
	if a.tunnelMgr == nil {
		return
	}
	a.tunnelMgr.mu.Lock()
	defer a.tunnelMgr.mu.Unlock()
	for _, tun := range a.tunnelMgr.tunnels {
		tun.mu.Lock()
		tun.closed = true
		tun.target.Close()
		tun.mu.Unlock()
	}
	a.tunnelMgr.tunnels = make(map[string]*agentTunnel)
}