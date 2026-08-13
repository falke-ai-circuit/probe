package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)


// handleWebSocket upgrades HTTP to WebSocket, authenticates, and processes agent connections.
func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Auth check — accept primary, extra, or rotated tokens
	authHeader := r.Header.Get("Authorization")
	if !s.isValidToken(authHeader) {
		log.Printf("[server] auth rejected from %s", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	upgrader := &websocket.Upgrader{
		ReadBufferSize:  65536,
		WriteBufferSize: 65536,
		CheckOrigin:     func(r *http.Request) bool { return true },
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[server] upgrade failed: %v", err)
		return
	}

	// Read first message — detect relay vs direct agent.
	// Relay connections send BinaryMessage with framing header.
	// Direct agents send TextMessage with JSON Envelope.
	msgType, firstData, err := conn.ReadMessage()
	if err != nil {
		log.Printf("[server] failed to read first message: %v", err)
		conn.Close()
		return
	}

	if msgType == websocket.BinaryMessage {
		// Relay connection — handle in relay mode
		s.handleRelayConnection(conn, firstData)
		return
	}

	// Direct agent connection — parse first message as JSON Envelope
	var env protocol.Envelope
	if err := json.Unmarshal(firstData, &env); err != nil {
		log.Printf("[server] failed to parse agent info: %v", err)
		conn.Close()
		return
	}

	var info protocol.AgentInfo
	if env.Result != nil {
		if err := json.Unmarshal(env.Result, &info); err != nil {
			log.Printf("[server] failed to parse agent info: %v", err)
			conn.Close()
			return
		}
	}

	// Use agent's configured name as ID (unique per agent).
	// Falls back to server hostname if name is empty (backward compat).
	agentID := info.Name
	if agentID == "" {
		hostname, _ := os.Hostname()
		agentID = fmt.Sprintf("a0-%s", hostname)
	}

	// Check if agent has been revoked — reject the connection.
	if s.IsAgentRevoked(agentID) {
		log.Printf("[server] revoked agent %s attempted to connect — rejecting", agentID)
		conn.Close()
		return
	}

	// Register agent
	s.registry.Register(agentID, info.Name, info.Version, info.OS, info.Arch, info.Mode, info.Capabilities)

	// Create session
	s.sessions.CreateSession(agentID)

	// Store connection
	s.mu.Lock()
	s.conns[agentID] = conn
	s.connWriteMu[agentID] = &sync.Mutex{}
	s.mu.Unlock()

	// Check if this agent was recently updated. If so, the old process is
	// still running (with the old PID). We need to kill it via the new agent.
	s.pendingMu.Lock()
	if pending, ok := s.pendingUpdates[agentID]; ok {
		oldPID := pending.OldPID
		delete(s.pendingUpdates, agentID)
		s.pendingMu.Unlock()

		log.Printf("[update] agent %s reconnected after update — scheduling kill of old PID %d", agentID, oldPID)

		// Wait a moment for the new agent to be fully ready, then kill the old process.
		// We send proc_kill to the NEW agent (which is now connected) to kill the OLD process.
		go func() {
			time.Sleep(3 * time.Second) // give new agent time to stabilize
			killParams := protocol.TaskStopParams{PID: oldPID, Signal: 9}
			_, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeProcKill, killParams, 30*time.Second, "")
			if err != nil {
				log.Printf("[update] failed to kill old PID %d: %v (old process may have already exited)", oldPID, err)
			} else {
				log.Printf("[update] successfully killed old PID %d — update complete", oldPID)
			}
		}()
	} else {
		s.pendingMu.Unlock()
	}

	// Track token expiry so the rotation goroutine can proactively rotate it.
	if s.tokenTTL > 0 {
		s.SetTokenExpiry(agentID, time.Now().Add(s.tokenTTL))
	}

	// Determine protocol version (missing = "1" = old agent)
	protoVer := info.ProtocolVersion
	if protoVer == "" {
		protoVer = "1"
	}

	log.Printf("[server] agent connected: %s (%s/%s, mode=%s, protocol=%s)", agentID, info.OS, info.Arch, info.Mode, protoVer)

	// Handle messages
	go s.handleMessages(agentID, conn)
}


// handleMessages processes incoming WebSocket messages from an agent.
func (s *Server) handleMessages(agentID string, conn *websocket.Conn) {
	defer func() {
		conn.Close()
		s.mu.Lock()
		// Only delete the connection if it's still ours.
		// During an update, a new agent connects and replaces the old connection
		// in s.conns. The old handleMessages goroutine should NOT delete the
		// new agent's connection.
		stillOurs := s.conns[agentID] == conn
		if stillOurs {
			delete(s.conns, agentID)
			delete(s.connWriteMu, agentID)
		}
		s.mu.Unlock()
		// Only unregister if the connection was ours (not replaced by a new agent)
		if stillOurs {
			s.registry.Unregister(agentID)
			s.sessions.RemoveSession(agentID)
			s.ClearTokenExpiry(agentID)
		}
		log.Printf("[server] agent disconnected: %s", agentID)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[server] read error from %s: %v", agentID, err)
			return
		}

		var env protocol.Envelope
		if msgType == websocket.BinaryMessage && s.e2eMgr != nil && s.e2eMgr.IsActive() {
			// E2E encrypted message — decrypt first
			plaintext, err := s.e2eMgr.Decrypt(data)
			if err != nil {
				log.Printf("[server] e2e decrypt error from %s: %v", agentID, err)
				continue
			}
			if err := json.Unmarshal(plaintext, &env); err != nil {
				log.Printf("[server] json decode error from %s: %v", agentID, err)
				continue
			}
		} else {
			// Plaintext JSON message (backward compat)
			if err := json.Unmarshal(data, &env); err != nil {
				log.Printf("[server] json decode error from %s: %v", agentID, err)
				continue
			}
		}

		switch env.Type {
		case protocol.TypePing:
			pong := protocol.NewPong(env.ID)
			conn.WriteJSON(pong)
			s.registry.Heartbeat(agentID)

		case protocol.TypePong:
			s.registry.Heartbeat(agentID)

		case protocol.TypeHealthResult:
			// Agent reported health; update resource usage and refresh heartbeat.
			if env.Result != nil {
				var hr protocol.HealthResult
				if err := json.Unmarshal(env.Result, &hr); err == nil {
					s.registry.UpdateHealth(agentID, ResourceInfo{
						CPUPercent: hr.CPUPercent,
						MemoryMB:   hr.MemoryMB,
						DiskFreeMB: hr.DiskFreeMB,
					})
					if len(hr.Capabilities) > 0 {
						s.registry.UpdateCapabilities(agentID, hr.Capabilities)
					}
				}
			}

		case protocol.TypeError:
			// Error responses from agents may be replies to pending requests
			// (e.g. "not yet implemented" for stub capabilities). Check
			// pendingReqs FIRST so the HTTP handler gets the error instead
			// of timing out.
			s.pendingMu.Lock()
			if ch, ok := s.pendingReqs[env.ID]; ok {
				delete(s.pendingReqs, env.ID)
				s.pendingMu.Unlock()
				ch <- env
				continue
			}
			s.pendingMu.Unlock()
			if env.Error != nil {
				s.registry.RecordError(agentID, env.Error.Message)
			}

		case protocol.TypeAuthRefreshResult, protocol.TypeTokenRotateResult:
			// Agent confirmed it applied a rotated token. Record the event and
			// schedule the next expiry from now (relative to the new token).
			// Handles both old ("auth_refresh_result") and new ("token_rotate_result") names.
			var result protocol.TokenRotateResult
			if env.Result != nil {
				_ = json.Unmarshal(env.Result, &result)
			}
			log.Printf("[server] agent %s rotated token (rotated=%v)", agentID, result.Rotated)
			s.sessions.AddMemory(agentID, "token_rotated_at", time.Now().UTC().Format(time.RFC3339))
			if s.tokenTTL > 0 {
				s.SetTokenExpiry(agentID, time.Now().Add(s.tokenTTL))
			}

		case protocol.TypeAuthRequest, protocol.TypeTokenRefresh:
			// Agent requested a proactive token refresh (its token is nearing
			// expiry). Generate a new token and send it back.
			newToken := generateToken()
			if err := s.InitiateTokenRotation(agentID, newToken); err != nil {
				log.Printf("[server] proactive refresh failed for agent %s: %v", agentID, err)
			} else {
				log.Printf("[server] sent refreshed token to agent %s", agentID)
				if s.tokenTTL > 0 {
					s.SetTokenExpiry(agentID, time.Now().Add(s.tokenTTL))
				}
			}

		case protocol.TypeAgentUpdateResult:
			// Agent confirmed it started the new binary. Record the old PID
			// so we can kill it once the new agent connects.
			s.handleAgentUpdateResult(agentID, env)

		case protocol.TypeStreamData:
			// Agent sent a screen stream frame. Store as latest frame for polling.
			s.sessions.SetMemory(agentID, "last_stream_frame", string(env.Result))

		default:
			// Check if this is a response to a pending request (exec, fs_read, etc.)
			s.pendingMu.Lock()
			if ch, ok := s.pendingReqs[env.ID]; ok {
				delete(s.pendingReqs, env.ID)
				s.pendingMu.Unlock()
				ch <- env
				continue
			}
			s.pendingMu.Unlock()

			// Handle tunnel data from agent (target→client direction)
			if env.Type == protocol.TypeTunnelData {
				s.handleTunnelDataFromAgent(env)
				continue
			}

			// Store results in session context if relevant
			s.sessions.AddMemory(agentID, "last_msg_type", env.Type)
			s.sessions.AddMemory(agentID, fmt.Sprintf("msg_%d", time.Now().UnixMilli()), string(mustMarshalRaw(env)))
		}
	}
}

