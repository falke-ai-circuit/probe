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

		// Forward to downstream agent
		ch := r.channels.Get(chanID)
		if ch != nil && ch.Conn != nil {
			ch.Conn.WriteMessage(websocket.TextMessage, payload)
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

// handleDownstream accepts WebSocket connections from agents.
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

	// Allocate channel
	ch := r.channels.Alloc(conn, func() {
		r.ipMu.Lock()
		r.ipCounts[ip]--
		if r.ipCounts[ip] <= 0 {
			delete(r.ipCounts, ip)
		}
		r.ipMu.Unlock()
	})

	log.Printf("[relay] agent connected on channel %d from %s", ch.ID, ip)

	// Send channel_open to server
	r.sendChannelOpen(ch)

	// Pipe: agent → relay → server
	go r.pipeAgentToServer(conn, ch, ip)
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