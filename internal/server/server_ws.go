package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)


// writeToAgentLocked writes a control reply on an agent connection under the
// same per-connection mutex forwardToAgent uses. gorilla's Conn is not safe for
// concurrent writers; one unlocked writer is enough to panic the process
// ("concurrent write to websocket connection") and take every agent with it.
// Relayed connections own their locking internally, so they are not double-locked.
func (s *Server) writeToAgentLocked(agentID string, conn Conn, v interface{}) error {
	if _, isVirtual := conn.(*virtualConn); isVirtual {
		return conn.WriteJSON(v)
	}
	s.mu.RLock()
	mu := s.connWriteMu[agentID]
	s.mu.RUnlock()
	if mu == nil {
		return conn.WriteJSON(v)
	}
	mu.Lock()
	defer mu.Unlock()
	return conn.WriteJSON(v)
}

// updatePromotionKillDelay is how long the server waits for the promoted agent
// to stabilise before ordering the kill of its predecessor. A variable so the
// completion path can be tested without a three-second sleep per run.
var updatePromotionKillDelay = 3 * time.Second

// completeUpdatePromotion is the completion half of an update: after the
// promoted agent has reconnected, it waits for it to stabilise, orders the
// predecessor process killed through the new connection, and clears the dead
// `<agentID>-canary` registry row. It runs in its own goroutine from
// handleWebSocket. Split out as a method so tests can exercise the completion
// path directly (P9-14 / P9-12 A2: no test reached this call site).
func (s *Server) completeUpdatePromotion(agentID string, oldPID int) {
	time.Sleep(updatePromotionKillDelay) // give new agent time to stabilise
	killParams := protocol.TaskStopParams{PID: oldPID, Signal: 9}
	if _, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeProcKill, killParams, 30*time.Second, ""); err != nil {
		log.Printf("[update] failed to kill old PID %d: %v (old process may have already exited)", oldPID, err)
	} else {
		log.Printf("[update] successfully killed old PID %d — update complete", oldPID)
	}
	// The purge runs after the kill attempt on BOTH paths. A failed kill means
	// the predecessor exited on its own — the handover still happened — and the
	// suffixed row is dead weight that reads as a second live agent either way.
	// P9-14: leaving it inside the success branch was defect B surviving its
	// own fix (P9-12 A2, server_ws.go:140-148).
	s.purgeCanaryRegistryRow(agentID)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Auth check — accept primary, extra, or rotated tokens
	authHeader := r.Header.Get("Authorization")
	if !s.isValidToken(authHeader) {
		// P21-07: a relay rotated to a per-node credential presents that
		// credential on reconnect, not the shared token. Accepting it here is
		// what lets rotation finish without a server restart; the credential
		// is bound to one relay id by bindRelayIdentity at registration.
		ok := false
		if strings.HasPrefix(authHeader, "Bearer ") {
			ok = s.isValidRelayCredential(strings.TrimPrefix(authHeader, "Bearer "))
		}
		if !ok {
			log.Printf("[server] auth rejected from %s", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
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
		// P23-12 (input: P23-07 CHECK 6): an E2E-active agent's first frame is
		// BinaryMessage — AES-GCM ciphertext of its agent-info envelope (agent.go
		// writeMessage encrypts before sending; the agent hello is sent first) —
		// which the old code misread as relay framing: relay_ws.go's channel-0
		// guard parsed ciphertext bytes [1:5] as the channel id "got channel
		// 2993237412" and closed 21/22 live dials with 1006. Classification is
		// exact by GCM auth: same-key ciphertext decrypts (it IS an agent hello),
		// a genuine plaintext relay frame never authenticates (relay framing is
		// not E2E-sealed), so the relay path is unchanged in shape.
		if s.e2eMgr != nil && s.e2eMgr.IsActive() {
			plaintext, derr := s.e2eMgr.Decrypt(firstData)
			if derr != nil {
				// Relay connection — handle in relay mode
				s.handleRelayConnection(conn, firstData)
				return
			}
			// E2E-sealed agent hello: continue with the plaintext envelope.
			firstData = plaintext
		} else {
			// E2E inactive: a binary first frame can only be relay framing.
			s.handleRelayConnection(conn, firstData)
			return
		}
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
		log.Printf("[server] server: revoked agent %s attempted to connect — rejecting", agentID)
		conn.Close()
		return
	}

	// P21-04: the policy decides where this client registers. `local` (and any
	// server without an uplink, i.e. a chain root) registers here; `relay` and
	// `relay_all` register upstream and leave a read-only mirror behind. A
	// refused forward is answered with its named code and the socket closes —
	// never a silent local registration the policy did not ask for.
	link, ferr := s.forwardArrival(agentID, Chain{}, func(p []byte) {
		_ = conn.WriteMessage(websocket.TextMessage, p)
	}, firstData)
	if ferr != nil {
		code, _ := isForwardRefusal(ferr)
		log.Printf("[server] agent %s refused at arrival: %v", agentID, ferr)
		writeForwardRefusal(conn, code, ferr)
		conn.Close()
		return
	}
	if link != nil {
		log.Printf("[server] agent %s forwarded upstream via %s (policy enforced, mirrored; serving locally too)", agentID, link.Via)
		// P22-06: dual registration — the client stays a live local row AND
		// appears upstream as relay/<link>/<name>. The mirror hook in
		// handleMessages carries its frames upstream from now on, and the
		// link map records the channel so a policy toggle can pull it back
		// down live without touching the client's socket.
		s.registry.Register(agentID, info.Name, info.Version, info.OS, info.Arch, info.Mode, info.Capabilities)
		s.sessions.CreateSession(agentID)
		s.mu.Lock()
		s.conns[agentID] = conn
		s.connWriteMu[agentID] = &sync.Mutex{}
		s.mu.Unlock()
		s.noteForwardLink(agentID, link)
		if s.tokenTTL > 0 {
			s.SetTokenExpiry(agentID, time.Now().Add(s.tokenTTL))
		}
		go s.handleMessages(agentID, conn)
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

		// P9-11: the promoted agent is live, so the artifact is no longer
		// being delivered — release its retention pin.
		if artifact := s.takeUpdateArtifact(agentID); artifact != "" {
			s.clearArtifactInFlight(artifact)
		}

		log.Printf("[update] agent %s reconnected after update — scheduling kill of old PID %d", agentID, oldPID)

		// Wait a moment for the new agent to be fully ready, then kill the old process.
		// We send proc_kill to the NEW agent (which is now connected) to kill the OLD process.
		go s.completeUpdatePromotion(agentID, oldPID)
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
	// P8-04: on reconnect, ask the agent for the spool state of jobs this
	// server still holds open, so a guardian report resolves the record.
	go s.reconcileAgentJobs(agentID)
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
			// P22-06: a forwarded agent that disconnects takes its upstream
			// channel down with it — the upstream registration must not
			// outlive the client that owns it.
			if link, fwd := s.forwardLinkedAgent(agentID); fwd {
				link.Close()
				s.forgetForwardLink(agentID)
				s.dropMirrored(agentID)
			}
			s.registry.Unregister(agentID)
			s.sessions.RemoveSession(agentID)
			s.ClearTokenExpiry(agentID)
			// P8-04: an agent dropped with a detached job open cannot be the
			// executor of its cleanup. Arm the 120s recovery escalation; the
			// guardian on the host is still the one that runs on_death.
			s.agentJobsDisconnected(agentID)
		}
		log.Printf("[server] agent disconnected: %s", agentID)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[server] read error from %s: %v", agentID, err)
			return
		}

		// P22-06: a forwarded agent's frames mirror upstream, raw as
		// received — the uplink carries exactly what the agent sent (the
		// same payload shape pipeForwardedAgent carried). A mirror failure
		// never stops local serving: the local row is the serving truth.
		if link, fwd := s.forwardLinkedAgent(agentID); fwd {
			if serr := link.Send(data); serr != nil {
				log.Printf("[forward] mirror to upstream for %s failed: %v", agentID, serr)
			}
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

		// P21-04: the direct path goes through the same admission as a
		// relayed one. Parity is asserted here and in the relay loop, not
		// inherited from the transport.
		// P22-07: two exemptions, both because the frame is not
		// agent-initiated traffic:
		//   1. tunnel-plane DATA frames — an exec-style admission token per
		//      echoed byte throttled the tunnel itself, and under repetition
		//      the read loop silently dropped tunnel_data frames once the
		//      bucket ran dry (live 9/20, all timeouts on verified ports).
		//      The tunnel's admission happens once, at create.
		//   2. replies to a request the SERVER is waiting on (tunnel_opened,
		//      exec results, pongs) — refusing a reply strands the server's
		//      own caller with a timeout, which is the same
		//      published-but-dead shape as the data-plane drop.
		if env.Type != protocol.TypeTunnelData && !s.isPendingReply(env.ID) && !s.admitInbound(agentID, false) {
			log.Printf("[server] agent %s: rate/concurrency limit refused", agentID)
			continue
		}

		switch env.Type {
		case protocol.TypePing:
			pong := protocol.NewPong(env.ID)
			// P8-06 (P8-16 crash evidence): this was the only server-side
			// WebSocket write that did not take the per-connection write mutex.
			// A pong landing while forwardToAgent was writing an fs-write
			// response is exactly the "concurrent write to websocket
			// connection" panic at gorilla flushFrame. Every writer on a
			// connection serializes through connWriteMu, so the race cannot
			// form — for any payload size, which is why chunk size was never
			// the real threshold.
			s.writeToAgentLocked(agentID, conn, pong)
			s.registry.Heartbeat(agentID)

		case protocol.TypePong:
			// P12-03: a pong may be the answer to the server's liveness
			// probe — hand it to the waiting probe before recording it.
			s.pongToPending(env)
			s.registry.RecordPong(agentID)

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

