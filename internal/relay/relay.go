package relay

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Config holds relay configuration.
type Config struct {
	UpstreamURL  string // wss://server:port/ws
	ListenAddr   string // :7701
	Token        string // relay's own token for upstream auth
	AgentTokens  string // comma-separated tokens agents must present to relay
	CertFile     string // TLS cert for downstream listener (optional)
	KeyFile      string // TLS key for downstream listener (optional)
	MaxAgents    int    // max concurrent downstream agents (default 100)
	MaxPerIP     int    // max connections per IP (default 10)
	RelayID      string // relay identifier (auto-generated if empty)
}

// Relay is the bridge between downstream agents and upstream server.
type Relay struct {
	cfg         Config
	magic       byte
	channels    *ChannelMap
	upstream    *websocket.Conn
	upstreamMu  sync.Mutex // protects upstream connection writes
	upstreamOK  atomic.Bool
	httpSrv     *http.Server
	// IP tracking for rate limiting
	ipMu        sync.Mutex
	ipCounts    map[string]int
}

var upgrader = &websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// New creates a new Relay with the given configuration.
func New(cfg Config) *Relay {
	if cfg.MaxAgents <= 0 {
		cfg.MaxAgents = 100
	}
	if cfg.MaxPerIP <= 0 {
		cfg.MaxPerIP = 10
	}
	// Generate random magic byte (0x02-0xFF, avoid 0x00 and 0x01)
	var b [1]byte
	rand.Read(b[:])
	cfg.RelayID = cfg.RelayID // keep as-is if set
	return &Relay{
		cfg:      cfg,
		magic:    0x02 + (b[0] % 0xFE), // 0x02..0xFF
		channels: NewChannelMap(),
		ipCounts: make(map[string]int),
	}
}

// Magic returns the relay's framing magic byte.
func (r *Relay) Magic() byte { return r.magic }

// Stop gracefully shuts down the relay: stops the HTTP server and closes the upstream connection.
func (r *Relay) Stop() {
	r.upstreamMu.Lock()
	if r.upstream != nil {
		r.upstream.Close()
		r.upstream = nil
	}
	r.upstreamMu.Unlock()
	if r.httpSrv != nil {
		r.httpSrv.Close()
	}
}

// Run starts the relay: connects upstream, then listens for downstream agents.
func (r *Relay) Run() error {
	// 1. Connect to upstream server
	if err := r.connectUpstream(); err != nil {
		return fmt.Errorf("upstream connect: %w", err)
	}

	// 2. Start upstream reader goroutine
	go r.dispatchFromServer()

	// 3. Start upstream reconnection watcher
	go r.upstreamWatcher()

	// 4. Listen for downstream agents
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", r.handleDownstream)
	mux.HandleFunc("/health", r.handleHealth)

	r.httpSrv = &http.Server{
		Addr:    r.cfg.ListenAddr,
		Handler: mux,
	}

	log.Printf("[relay] listening on %s, upstream=%s, magic=0x%02X, max-agents=%d",
		r.cfg.ListenAddr, r.cfg.UpstreamURL, r.magic, r.cfg.MaxAgents)

	if r.cfg.CertFile != "" && r.cfg.KeyFile != "" {
		return r.httpSrv.ListenAndServeTLS(r.cfg.CertFile, r.cfg.KeyFile)
	}
	return r.httpSrv.ListenAndServe()
}

// connectUpstream dials the upstream server and sends relay registration.
func (r *Relay) connectUpstream() error {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+r.cfg.Token)

	conn, _, err := websocket.DefaultDialer.Dial(r.cfg.UpstreamURL, headers)
	if err != nil {
		return fmt.Errorf("dial upstream: %w", err)
	}

	r.upstreamMu.Lock()
	r.upstream = conn
	r.upstreamOK.Store(true)
	r.upstreamMu.Unlock()

	// Generate relay ID if not set
	relayID := r.cfg.RelayID
	if relayID == "" {
		var idBytes [8]byte
		rand.Read(idBytes[:])
		relayID = fmt.Sprintf("relay-%x", idBytes[:4])
		r.cfg.RelayID = relayID
	}

	// Send relay registration as first message (binary, channelID=0)
	// with metadata for topology awareness (Phase 4).
	reg := ControlMessage{
		Type:    "relay_register",
		RelayID: relayID,
		Token:   r.cfg.Token,
		Metadata: &RelayMetadata{
			ListenAddr: r.cfg.ListenAddr,
			MaxAgents:  r.cfg.MaxAgents,
			Upstream:   r.cfg.UpstreamURL,
		},
	}
	frame, err := MakeControlFrame(r.magic, reg)
	if err != nil {
		conn.Close()
		return fmt.Errorf("build registration frame: %w", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		conn.Close()
		return fmt.Errorf("send registration: %w", err)
	}

	log.Printf("[relay] connected to upstream %s (id=%s)", r.cfg.UpstreamURL, relayID)
	return nil
}

// dispatchFromServer reads framed messages from the upstream server and
// forwards payloads to the correct downstream agent.
func (r *Relay) dispatchFromServer() {
	for {
		r.upstreamMu.Lock()
		conn := r.upstream
		r.upstreamMu.Unlock()

		if conn == nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}

		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[relay] upstream read error: %v", err)
			r.upstreamOK.Store(false)
			// Wait for upstreamWatcher to reconnect
			return
		}

		_ = msgType

		magic, chanID, payload, err := ParseFrame(data)
		if err != nil {
			log.Printf("[relay] frame parse error: %v", err)
			continue
		}

		// Verify magic byte (optional — server should use our magic)
		_ = magic

		if chanID == 0 {
			// Control message from server (heartbeat ack, etc.)
			var ctrl ControlMessage
			if err := json.Unmarshal(payload, &ctrl); err == nil {
				// Handle server-side control messages if needed
			}
			continue
		}

		// Forward to downstream agent or nested relay
		ch := r.channels.Get(chanID)
		if ch != nil {
			if ch.Conn != nil {
				// Direct agent connection
				ch.Conn.WriteMessage(websocket.TextMessage, payload)
			} else if ch.NestedWrite != nil {
				// Virtual channel from a nested relay — re-frame and send
				// back through the nested relay's connection
				ch.NestedWrite(chanID, payload)
			}
		}
	}
}

// upstreamWatcher monitors the upstream connection and reconnects on failure.
func (r *Relay) upstreamWatcher() {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		time.Sleep(5 * time.Second)
		if r.upstreamOK.Load() {
			backoff = 1 * time.Second
			continue
		}

		log.Printf("[relay] upstream disconnected — attempting reconnect (backoff=%v)", backoff)
		time.Sleep(backoff)

		if err := r.connectUpstream(); err != nil {
			log.Printf("[relay] reconnect failed: %v", err)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		// Reconnected — re-register all active channels
		r.reregisterChannels()
		r.upstreamOK.Store(true)
		backoff = 1 * time.Second
		go r.dispatchFromServer()
	}
}

// reregisterChannels sends channel_open for all active channels after reconnect.
func (r *Relay) reregisterChannels() {
	channels := r.channels.All()
	for _, ch := range channels {
		// We don't have the original AgentInfo stored — send a minimal channel_open
		// The server will request agent info from the agent itself
		ctrl := ControlMessage{
			Type:      "channel_open",
			ChannelID: ch.ID,
			RelayID:   r.cfg.RelayID,
		}
		frame, _ := MakeControlFrame(r.magic, ctrl)
		r.upstreamMu.Lock()
		if r.upstream != nil {
			r.upstream.WriteMessage(websocket.BinaryMessage, frame)
		}
		r.upstreamMu.Unlock()
	}
	log.Printf("[relay] re-registered %d channels after reconnect", len(channels))
}

// handleDownstream accepts WebSocket connections from agents OR nested relays.
// Detection: read the first message. If it's BinaryMessage with a relay_register
// control frame (channel 0, type "relay_register"), it's a nested relay —
// multiplex its channels through our upstream. Otherwise, it's a regular agent.
func (r *Relay) handleDownstream(w http.ResponseWriter, req *http.Request) {
	// Check if upstream is available
	if !r.upstreamOK.Load() {
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}

	// Rate limit: max agents
	if r.channels.Count() >= r.cfg.MaxAgents {
		http.Error(w, "too many agents", http.StatusTooManyRequests)
		return
	}

	// Rate limit: per-IP
	ip := clientIP(req)
	if !r.allowIP(ip) {
		http.Error(w, "too many connections from this IP", http.StatusTooManyRequests)
		return
	}

	// Token validation (if agent tokens are configured)
	if r.cfg.AgentTokens != "" {
		authHeader := req.Header.Get("Authorization")
		if !r.isValidAgentToken(authHeader) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("[relay] upgrade failed: %v", err)
		return
	}

	// Read the first message to detect: agent (text/JSON) vs nested relay (binary + relay_register)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	msgType, firstData, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{}) // reset deadline
	if err != nil {
		log.Printf("[relay] first message read error from %s: %v", ip, err)
		conn.Close()
		r.releaseIP(ip)
		return
	}

	// Check if this is a nested relay (binary message with relay_register on channel 0)
	if msgType == websocket.BinaryMessage && len(firstData) >= 5 {
		_, chanID, payload, parseErr := ParseFrame(firstData)
		if parseErr == nil && chanID == 0 {
			var ctrl ControlMessage
			if json.Unmarshal(payload, &ctrl) == nil && ctrl.Type == "relay_register" {
				// This is a nested relay — handle relay chaining (Step 12)
				r.handleNestedRelay(conn, &ctrl, ip, firstData)
				return
			}
		}
	}

	// Regular agent connection — allocate channel
	ch := r.channels.Alloc(conn, func() {
		r.releaseIP(ip)
	})

	// If we already read the first message, we need to forward it
	// to the upstream as the first data frame on this channel.
	log.Printf("[relay] agent connected on channel %d from %s", ch.ID, ip)

	// Send channel_open to server
	r.sendChannelOpen(ch)

	// Forward the already-read first message
	firstFrame := MakeFrame(r.magic, ch.ID, firstData)
	r.upstreamMu.Lock()
	if r.upstream != nil && r.upstreamOK.Load() {
		r.upstream.WriteMessage(websocket.BinaryMessage, firstFrame)
	}
	r.upstreamMu.Unlock()

	// Pipe: agent → relay → server
	go r.pipeAgentToServer(conn, ch, ip)
}

// handleNestedRelay handles a downstream connection from another relay (relay chaining).
// The nested relay's channels are sub-multiplexed through this relay's upstream.
// Each channel from the nested relay gets its own channel on our upstream.
//
// Topology: Client Z → Relay A → Relay B → Server Y
// Relay A connects to Relay B as a downstream agent. Relay B sees Relay A's
// relay_register and creates a nested relay session. Relay B forwards Relay A's
// channels as its own channels to Server Y. Server Y sees: relay/B/relay/A/Z
func (r *Relay) handleNestedRelay(conn *websocket.Conn, ctrl *ControlMessage, ip string, firstData []byte) {
	nestedRelayID := ctrl.RelayID
	if nestedRelayID == "" {
		nestedRelayID = fmt.Sprintf("nested-%d", time.Now().UnixMilli())
	}

	log.Printf("[relay] nested relay connected: id=%s from %s", nestedRelayID, ip)

	// We treat the nested relay as a sub-multiplexer. Messages from the nested
	// relay are framed with the nested relay's magic + channel IDs. We need to
	// re-frame them with OUR magic and allocate OUR channel IDs for each of
	// the nested relay's channels.
	//
	// Map: nestedRelayChannelID → ourChannelID
	nestedChanMap := make(map[uint32]uint32)
	// Reverse map: ourChannelID → nestedRelayChannelID (for downstream writes)
	reverseChanMap := make(map[uint32]uint32)
	var nestedMu sync.Mutex

	// Extract nested relay's magic from the first frame
	nestedMagic := byte(0x02)
	if _, _, _, parseErr := ParseFrame(firstData); parseErr == nil {
		nestedMagic = firstData[0]
	}

	// nestedWrite writes a payload back to the nested relay's connection,
	// re-framed with the nested relay's magic and original channel ID.
	nestedWrite := func(ourChanID uint32, payload []byte) error {
		nestedMu.Lock()
		nestedChID, ok := reverseChanMap[ourChanID]
		nestedMu.Unlock()
		if !ok {
			return fmt.Errorf("no nested channel for our channel %d", ourChanID)
		}
		frame := MakeFrame(nestedMagic, nestedChID, payload)
		return conn.WriteMessage(websocket.BinaryMessage, frame)
	}

	// Register the nested relay's own registration with our upstream
	// so the server knows about the relay chain.
	// Forward the relay_register with path prefix: relay/{ourID}/{nestedID}
	r.upstreamMu.Lock()
	if r.upstream != nil && r.upstreamOK.Load() {
		// Build a relay_register for the nested relay, prefixed with our relay ID
		nestedReg := ControlMessage{
			Type:    "relay_register",
			RelayID: fmt.Sprintf("%s/relay/%s", r.cfg.RelayID, nestedRelayID),
			Token:   ctrl.Token,
			Metadata: ctrl.Metadata,
		}
		frame, _ := MakeControlFrame(r.magic, nestedReg)
		r.upstream.WriteMessage(websocket.BinaryMessage, frame)
	}
	r.upstreamMu.Unlock()

	// Main loop: read framed messages from nested relay, re-frame and forward upstream
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[relay] nested relay %s disconnected: %v", nestedRelayID, err)
			break
		}
		_ = msgType

		// Parse the nested relay's frame
		nestedMagic, nestedChanID, payload, err := ParseFrame(data)
		if err != nil {
			log.Printf("[relay] nested relay %s frame parse error: %v", nestedRelayID, err)
			continue
		}
		_ = nestedMagic

		if nestedChanID == 0 {
			// Control message from nested relay
			var nestedCtrl ControlMessage
			if json.Unmarshal(payload, &nestedCtrl) != nil {
				continue
			}

			switch nestedCtrl.Type {
			case "channel_open":
				// Allocate a channel on our upstream for this nested channel
				ourCh := r.channels.AllocVirtual(func() {})
				ourChanID := ourCh.ID
				nestedMu.Lock()
				nestedChanMap[nestedCtrl.ChannelID] = ourChanID
				reverseChanMap[ourChanID] = nestedCtrl.ChannelID
				nestedMu.Unlock()

				// Set the NestedWrite callback so dispatchFromServer can
				// forward server→agent traffic back through the nested relay
				ourCh.NestedWrite = nestedWrite

				// Forward channel_open to our upstream with our channel ID
				fwdCtrl := ControlMessage{
					Type:      "channel_open",
					ChannelID: ourChanID,
					RelayID:   r.cfg.RelayID,
				}
				frame, _ := MakeControlFrame(r.magic, fwdCtrl)
				r.upstreamMu.Lock()
				if r.upstream != nil {
					r.upstream.WriteMessage(websocket.BinaryMessage, frame)
				}
				r.upstreamMu.Unlock()

				log.Printf("[relay] nested %s: channel %d → our channel %d", nestedRelayID, nestedCtrl.ChannelID, ourChanID)

			case "channel_close":
				// Close our channel for this nested channel
				nestedMu.Lock()
				ourChanID, ok := nestedChanMap[nestedCtrl.ChannelID]
				if ok {
					delete(nestedChanMap, nestedCtrl.ChannelID)
				}
				nestedMu.Unlock()
				if ok {
					r.channels.Close(ourChanID)
					// Forward channel_close
					fwdCtrl := ControlMessage{
						Type:      "channel_close",
						ChannelID: ourChanID,
						RelayID:   r.cfg.RelayID,
					}
					frame, _ := MakeControlFrame(r.magic, fwdCtrl)
					r.upstreamMu.Lock()
					if r.upstream != nil {
						r.upstream.WriteMessage(websocket.BinaryMessage, frame)
					}
					r.upstreamMu.Unlock()
				}
			}
			continue
		}

		// Data frame from nested relay — re-frame with our magic + our channel ID
		nestedMu.Lock()
		ourChanID, ok := nestedChanMap[nestedChanID]
		nestedMu.Unlock()
		if !ok {
			// Unknown nested channel — allocate on the fly
			ourCh := r.channels.AllocVirtual(func() {})
			ourChanID = ourCh.ID
			nestedMu.Lock()
			nestedChanMap[nestedChanID] = ourChanID
			reverseChanMap[ourChanID] = nestedChanID
			nestedMu.Unlock()
			ourCh.NestedWrite = nestedWrite

				// Send channel_open
				fwdCtrl := ControlMessage{
					Type:      "channel_open",
					ChannelID: ourChanID,
					RelayID:   r.cfg.RelayID,
				}
			frame, _ := MakeControlFrame(r.magic, fwdCtrl)
			r.upstreamMu.Lock()
			if r.upstream != nil {
				r.upstream.WriteMessage(websocket.BinaryMessage, frame)
			}
			r.upstreamMu.Unlock()
		}

		// Forward the data with our framing
		fwdFrame := MakeFrame(r.magic, ourChanID, payload)
		r.upstreamMu.Lock()
		if r.upstream != nil && r.upstreamOK.Load() {
			r.upstream.WriteMessage(websocket.BinaryMessage, fwdFrame)
		}
		r.upstreamMu.Unlock()
	}

	// Cleanup: close all nested channels
	nestedMu.Lock()
	for nestedChID, ourChID := range nestedChanMap {
		r.channels.Close(ourChID)
		fwdCtrl := ControlMessage{
			Type:      "channel_close",
			ChannelID: ourChID,
			RelayID:   r.cfg.RelayID,
		}
		frame, _ := MakeControlFrame(r.magic, fwdCtrl)
		r.upstreamMu.Lock()
		if r.upstream != nil {
			r.upstream.WriteMessage(websocket.BinaryMessage, frame)
		}
		r.upstreamMu.Unlock()
		delete(nestedChanMap, nestedChID)
	}
	nestedMu.Unlock()

	r.releaseIP(ip)
	conn.Close()
	log.Printf("[relay] nested relay %s cleaned up", nestedRelayID)
}

// releaseIP decrements the IP connection count.
func (r *Relay) releaseIP(ip string) {
	r.ipMu.Lock()
	r.ipCounts[ip]--
	if r.ipCounts[ip] <= 0 {
		delete(r.ipCounts, ip)
	}
	r.ipMu.Unlock()
}

// pipeAgentToServer reads messages from the downstream agent and forwards
// them as framed messages on the upstream WebSocket.
func (r *Relay) pipeAgentToServer(conn *websocket.Conn, ch *Channel, ip string) {
	defer func() {
		r.channels.Close(ch.ID)
		r.sendChannelClose(ch)
		conn.Close()
		log.Printf("[relay] channel %d closed (agent from %s)", ch.ID, ip)
	}()

	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		_ = msgType

		// Forward as framed message on upstream
		frame := MakeFrame(r.magic, ch.ID, data)
		r.upstreamMu.Lock()
		if r.upstream == nil || !r.upstreamOK.Load() {
			r.upstreamMu.Unlock()
			// Upstream down — buffer or drop
			if !ch.QueueMessage(frame) {
				return // buffer full — close channel
			}
			continue
		}
		err = r.upstream.WriteMessage(websocket.BinaryMessage, frame)
		r.upstreamMu.Unlock()
		if err != nil {
			log.Printf("[relay] upstream write error on channel %d: %v", ch.ID, err)
			r.upstreamOK.Store(false)
			return
		}
	}
}

// sendChannelOpen sends a channel_open control message to the server.
func (r *Relay) sendChannelOpen(ch *Channel) {
	ctrl := ControlMessage{
		Type:      "channel_open",
		ChannelID: ch.ID,
		RelayID:   r.cfg.RelayID,
	}
	frame, _ := MakeControlFrame(r.magic, ctrl)
	r.upstreamMu.Lock()
	defer r.upstreamMu.Unlock()
	if r.upstream != nil {
		r.upstream.WriteMessage(websocket.BinaryMessage, frame)
	}
}

// sendChannelClose sends a channel_close control message to the server.
func (r *Relay) sendChannelClose(ch *Channel) {
	ctrl := ControlMessage{
		Type:      "channel_close",
		ChannelID: ch.ID,
		RelayID:   r.cfg.RelayID,
	}
	frame, _ := MakeControlFrame(r.magic, ctrl)
	r.upstreamMu.Lock()
	defer r.upstreamMu.Unlock()
	if r.upstream != nil {
		r.upstream.WriteMessage(websocket.BinaryMessage, frame)
	}
}

// handleHealth is a simple health endpoint for the relay.
func (r *Relay) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":     "ok",
		"upstream":   r.upstreamOK.Load(),
		"channels":   r.channels.Count(),
		"max_agents": r.cfg.MaxAgents,
	})
}

// allowIP checks if the IP is within the per-IP connection limit.
func (r *Relay) allowIP(ip string) bool {
	r.ipMu.Lock()
	defer r.ipMu.Unlock()
	if r.ipCounts[ip] >= r.cfg.MaxPerIP {
		return false
	}
	r.ipCounts[ip]++
	return true
}

// isValidAgentToken checks if the Authorization header matches any configured agent token.
func (r *Relay) isValidAgentToken(header string) bool {
	if header == "" {
		return false
	}
	// Strip "Bearer " prefix
	token := header
	if len(header) > 7 && header[:7] == "Bearer " {
		token = header[7:]
	}
	for _, allowed := range splitComma(r.cfg.AgentTokens) {
		if token == allowed {
			return true
		}
	}
	return false
}

func (r *Relay) upstreamWriteLoop() {
	// Reserved for future use — currently writes are done inline in pipeAgentToServer
	// with upstreamMu protection.
}

// clientIP extracts the client IP from a request, handling X-Forwarded-For.
func clientIP(req *http.Request) string {
	if xff := req.Header.Get("X-Forwarded-For"); xff != "" {
		return xff
	}
	host := req.RemoteAddr
	for i := len(host) - 1; i >= 0; i-- {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

func splitComma(s string) []string {
	var result []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			tok := s[start:i]
			// trim spaces
			for len(tok) > 0 && (tok[0] == ' ' || tok[0] == '	') {
				tok = tok[1:]
			}
			for len(tok) > 0 && (tok[len(tok)-1] == ' ' || tok[len(tok)-1] == '	') {
				tok = tok[:len(tok)-1]
			}
			if tok != "" {
				result = append(result, tok)
			}
			start = i + 1
		}
	}
	return result
}