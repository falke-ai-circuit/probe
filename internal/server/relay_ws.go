package server

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// relaySession represents a connected relay and its virtual channels.
type relaySession struct {
	relayID    string
	magic      byte
	conn       *websocket.Conn
	writeMu    sync.Mutex // shared across ALL virtual channels on this relay
	channels   map[uint32]*virtualConn
	channelsMu sync.RWMutex
}

// virtualConn represents a single agent behind a relay. It implements
// the same write semantics as a direct agent connection, but writes
// framed messages on the relay's shared WebSocket.
type virtualConn struct {
	channelID uint32
	session   *relaySession
	agentID   string
}

// WriteJSON writes a JSON value as a framed message on the relay's
// upstream WebSocket. It acquires the relay session's shared write mutex
// to prevent concurrent write corruption.
func (vc *virtualConn) WriteJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}

	frame := make([]byte, 5+len(data))
	frame[0] = vc.session.magic
	binary.BigEndian.PutUint32(frame[1:5], vc.channelID)
	copy(frame[5:], data)

	vc.session.writeMu.Lock()
	defer vc.session.writeMu.Unlock()
	return vc.session.conn.WriteMessage(websocket.BinaryMessage, frame)
}

// WriteMessage writes a raw message as a framed payload on the relay's
// upstream WebSocket.
func (vc *virtualConn) WriteMessage(msgType int, data []byte) error {
	frame := make([]byte, 5+len(data))
	frame[0] = vc.session.magic
	binary.BigEndian.PutUint32(frame[1:5], vc.channelID)
	copy(frame[5:], data)

	vc.session.writeMu.Lock()
	defer vc.session.writeMu.Unlock()
	return vc.session.conn.WriteMessage(websocket.BinaryMessage, frame)
}

// Close closes the virtual connection and removes it from the session.
func (vc *virtualConn) Close() error {
	vc.session.channelsMu.Lock()
	delete(vc.session.channels, vc.channelID)
	vc.session.channelsMu.Unlock()
	return nil
}

// handleRelayConnection processes a WebSocket connection that has been
// identified as a relay (first message was BinaryMessage).
func (s *Server) handleRelayConnection(conn *websocket.Conn, firstData []byte) {
	// Parse the first frame (relay registration)
	magic, chanID, payload, err := parseRelayFrame(firstData)
	if err != nil {
		log.Printf("[server] relay frame parse error: %v", err)
		conn.Close()
		return
	}

	if chanID != 0 {
		log.Printf("[server] expected relay registration on channel 0, got channel %d", chanID)
		conn.Close()
		return
	}

	var reg struct {
		Type    string `json:"type"`
		RelayID string `json:"relay_id"`
		Token   string `json:"token"`
	}
	if err := json.Unmarshal(payload, &reg); err != nil {
		log.Printf("[server] relay registration parse error: %v", err)
		conn.Close()
		return
	}

	if reg.Type != "relay_register" {
		log.Printf("[server] expected relay_register, got %s", reg.Type)
		conn.Close()
		return
	}

	session := &relaySession{
		relayID:  reg.RelayID,
		magic:    magic,
		conn:     conn,
		channels: make(map[uint32]*virtualConn),
	}

	log.Printf("[server] relay connected: id=%s, magic=0x%02X", reg.RelayID, magic)

	// Main loop: read framed messages from relay and dispatch
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[server] relay %s disconnected: %v", reg.RelayID, err)
			s.cleanupRelaySession(session)
			return
		}
		_ = msgType

		_, chanID, payload, err := parseRelayFrame(data)
		if err != nil {
			log.Printf("[server] relay %s frame parse error: %v", reg.RelayID, err)
			continue
		}

		if chanID == 0 {
			// Control message
			s.handleRelayControl(session, payload)
			continue
		}

		// Agent data — find the virtual connection
		session.channelsMu.RLock()
		vc, ok := session.channels[chanID]
		session.channelsMu.RUnlock()

		if !ok {
			log.Printf("[server] relay %s: unknown channel %d", reg.RelayID, chanID)
			continue
		}

		// Parse payload as protocol.Envelope
		var env protocol.Envelope
		if err := json.Unmarshal(payload, &env); err != nil {
			log.Printf("[server] relay %s channel %d: envelope parse error: %v", reg.RelayID, chanID, err)
			continue
		}

		// If this is a new channel with no agentID yet, the first message
		// should be the agent's registration (AgentInfo in Result).
		if vc.agentID == "" {
			var info protocol.AgentInfo
			if env.Result != nil {
				if err := json.Unmarshal(env.Result, &info); err == nil {
					agentID := info.Name
					if agentID == "" {
						agentID = fmt.Sprintf("relay-%s-ch%d", session.relayID, chanID)
					}
					vc.agentID = agentID
					s.registry.Register(agentID, info.Name, info.Version, info.OS, info.Arch, info.Mode, info.Capabilities)
					s.sessions.CreateSession(agentID)

					// Register the virtual connection in the server's conn map
					s.mu.Lock()
					s.conns[agentID] = vc
					s.connWriteMu[agentID] = &session.writeMu
					s.mu.Unlock()

					if s.tokenTTL > 0 {
						s.SetTokenExpiry(agentID, time.Now().Add(s.tokenTTL))
					}

					log.Printf("[server] relayed agent registered: %s (relay=%s, channel=%d)", agentID, session.relayID, chanID)
					continue
				}
			}
			// Not an agent info message — skip
			log.Printf("[server] relay %s channel %d: expected agent info, got type=%s", reg.RelayID, chanID, env.Type)
			continue
		}

		s.processAgentMessage(vc.agentID, env)
	}
}

// handleRelayControl processes control messages (channel_open, channel_close).
func (s *Server) handleRelayControl(session *relaySession, payload []byte) {
	var ctrl struct {
		Type      string          `json:"type"`
		ChannelID uint32          `json:"channel_id"`
		RelayID   string          `json:"relay_id"`
		AgentInfo json.RawMessage `json:"agent_info"`
	}
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		log.Printf("[server] relay control parse error: %v", err)
		return
	}

	switch ctrl.Type {
	case "channel_open":
		// A new agent connected behind the relay.
		// The agent's first message (AgentInfo) will come as the first
		// data frame on this channel. For now, create a placeholder
		// and wait for the agent's registration message.
		// Actually — the relay sends channel_open BEFORE the agent sends
		// its AgentInfo. The agent's first message will arrive as a
		// framed payload on this channel. We need to intercept it to
		// get the agent ID.
		// For now, create a pending virtual conn and handle the first
		// message specially in the main loop.
		vc := &virtualConn{
			channelID: ctrl.ChannelID,
			session:   session,
			agentID:   "", // will be set when we get the agent's first message
		}
		session.channelsMu.Lock()
		session.channels[ctrl.ChannelID] = vc
		session.channelsMu.Unlock()
		log.Printf("[server] relay %s: channel %d opened", session.relayID, ctrl.ChannelID)

	case "channel_close":
		session.channelsMu.Lock()
		vc, ok := session.channels[ctrl.ChannelID]
		if ok {
			delete(session.channels, ctrl.ChannelID)
		}
		session.channelsMu.Unlock()
		if vc != nil && vc.agentID != "" {
			s.mu.Lock()
			delete(s.conns, vc.agentID)
			delete(s.connWriteMu, vc.agentID)
			s.mu.Unlock()
			s.registry.Unregister(vc.agentID)
			s.sessions.RemoveSession(vc.agentID)
			s.ClearTokenExpiry(vc.agentID)
			log.Printf("[server] relay %s: channel %d closed (agent %s)", session.relayID, ctrl.ChannelID, vc.agentID)
		} else {
			log.Printf("[server] relay %s: channel %d closed (no agent registered)", session.relayID, ctrl.ChannelID)
		}

	case "heartbeat":
		// Relay keepalive — nothing to do
	}
}

// cleanupRelaySession removes all agents from a disconnected relay.
func (s *Server) cleanupRelaySession(session *relaySession) {
	session.channelsMu.Lock()
	defer session.channelsMu.Unlock()
	for chanID, vc := range session.channels {
		if vc.agentID != "" {
			s.mu.Lock()
			delete(s.conns, vc.agentID)
			delete(s.connWriteMu, vc.agentID)
			s.mu.Unlock()
			s.registry.Unregister(vc.agentID)
			s.sessions.RemoveSession(vc.agentID)
			s.ClearTokenExpiry(vc.agentID)
		}
		delete(session.channels, chanID)
	}
	log.Printf("[server] relay %s: cleaned up %d channels", session.relayID, len(session.channels))
}

// parseRelayFrame parses a relay framing header.
func parseRelayFrame(data []byte) (byte, uint32, []byte, error) {
	if len(data) < 5 {
		return 0, 0, nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}
	magic := data[0]
	chanID := binary.BigEndian.Uint32(data[1:5])
	payload := data[5:]
	return magic, chanID, payload, nil
}

// processAgentMessage handles a protocol.Envelope from a relayed agent.
// This is the same logic as handleMessages but works for relayed agents.
func (s *Server) processAgentMessage(agentID string, env protocol.Envelope) {
	// If this is the first message from a relayed agent (AgentInfo),
	// register it like a direct connection.
	if env.Type == "agent_info" || (env.Result != nil && agentID == "") {
		var info protocol.AgentInfo
		if err := json.Unmarshal(env.Result, &info); err == nil {
			agentID = info.Name
			if agentID == "" {
				agentID = fmt.Sprintf("relay-agent-%d", time.Now().UnixMilli())
			}
			s.registry.Register(agentID, info.Name, info.Version, info.OS, info.Arch, info.Mode, info.Capabilities)
			s.sessions.CreateSession(agentID)
			log.Printf("[server] relayed agent registered: %s", agentID)
			return
		}
	}

	if agentID == "" {
		log.Printf("[server] relayed message with no agent ID, type=%s", env.Type)
		return
	}

	// Process the message the same way as handleMessages
	switch env.Type {
	case protocol.TypePing:
		// Need to send pong back through the relay — find the virtual conn
		// This is handled by the relay's dispatchFromServer
		pong := protocol.NewPong(env.ID)
		// Find the relayed agent's virtual connection
		s.mu.RLock()
		conn := s.conns[agentID]
		s.mu.RUnlock()
		if vc, ok := conn.(*virtualConn); ok {
			vc.WriteJSON(pong)
		}
		s.registry.Heartbeat(agentID)

	case protocol.TypePong:
		s.registry.Heartbeat(agentID)

	default:
		// Check if this is a response to a pending request
		s.pendingMu.Lock()
		if ch, ok := s.pendingReqs[env.ID]; ok {
			delete(s.pendingReqs, env.ID)
			s.pendingMu.Unlock()
			ch <- env
			return
		}
		s.pendingMu.Unlock()

		// Store in session context
		s.sessions.AddMemory(agentID, "last_msg_type", env.Type)
		s.sessions.AddMemory(agentID, fmt.Sprintf("msg_%d", time.Now().UnixMilli()), string(mustMarshalRaw(env)))
	}
}