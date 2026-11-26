package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/version"
)

// Config holds relay configuration.
type Config struct {
	// UpstreamURL is the legacy single upstream target. It is still honoured:
	// when UpstreamURLs is empty it becomes a one-element candidate list, so an
	// operator upgrading the binary does not rewrite their config.
	UpstreamURL string // wss://server:port/ws
	// UpstreamURLs is the ordered upstream candidate list (P21-09). The first
	// candidate that completes the upgrade wins; after a drop the whole list is
	// walked again from the top. Order is the operator's, not sorted.
	UpstreamURLs []string
	// DialTimeout bounds one candidate's upgrade attempt (default 5s). A
	// candidate that accepts TCP and never upgrades must not stall the walk.
	DialTimeout time.Duration
	ListenAddr  string // :7701
	Token       string // relay's own token for upstream auth
	AgentTokens string // comma-separated tokens agents must present to relay
	CertFile    string // TLS cert for downstream listener (optional)
	KeyFile     string // TLS key for downstream listener (optional)
	MaxAgents   int    // max concurrent downstream agents (default 100)
	MaxPerIP    int    // max connections per IP (default 10)
	RelayID     string // relay identifier (auto-generated if empty)
}

// Relay is the bridge between downstream agents and upstream server.
type Relay struct {
	cfg        Config
	magic      byte
	channels   *ChannelMap
	upstream   *websocket.Conn
	upstreamMu sync.Mutex // protects upstream connection writes
	upstreamOK atomic.Bool
	httpSrv    *http.Server
	// IP tracking for rate limiting
	ipMu     sync.Mutex
	ipCounts map[string]int

	// P21-03: the relay's own record of its uplink — the peer identity the
	// upstream pushed down (peer_hello) and the last link failure. Guarded
	// by statusMu; rendered by Status()/handleStatus in status.go.
	statusMu sync.RWMutex
	uplink   uplinkState

	// P21-09: the ordered candidate list resolved once in New, and the
	// per-candidate attempt record that says why each one was skipped.
	// candidates is immutable after New; candState is guarded by statusMu.
	candidates []candidate
	candState  []candidateState

	// P21-07: the credential in force. It starts as cfg.Token and is
	// replaced in place when the server rotates this relay's identity, so a
	// reconnect after rotation presents the new value without a restart.
	// tokenFP is the fingerprint of token — the only form ever logged.
	credMu  sync.RWMutex
	token   string
	tokenFP string

	// P21-08: the nested relays this relay carries (relay chaining), keyed by
	// nested relay id and retained after a nested socket dies. Guarded by
	// nestedMu; served in the status document as downstream.nested.
	nestedMu sync.Mutex
	nested   map[string]*nestedLink

	// P17-02: peer-backend signals (uplink_only.go). stopCh releases
	// RunUplinkOnly; linkDownCb is the per-run exit hook fired once when
	// the dispatch loop ends. panicCB is FM-1.1's audit hook. All lazily
	// initialised so Relay literals in existing tests stay valid.
	signalMu     sync.Mutex
	stopCh       chan struct{}
	linkDownCb   func()
	linkDownFired bool
	stopOnce     sync.Once
	panicMu      sync.RWMutex
	panicCB      func(msg string)

	// P17-02: uplink-up hook (fire-once per Relay), peer backend's
	// `connected` transition (§4). uplinkSignalsInit lazily initialises
	// stopCh/uplinkDown so Relay literals in existing tests stay valid.
	uplinkUpMu  sync.RWMutex
	uplinkUpCB  func()
	uplinkUpOnce sync.Once
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
	// cfg.RelayID is preserved from input if already set
	cands := resolveCandidates(cfg)
	return &Relay{
		cfg:      cfg,
		candidates: cands,
		candState:  make([]candidateState, len(cands)),
		magic:    0x02 + (b[0] % 0xFE), // 0x02..0xFF
		channels: NewChannelMap(),
		ipCounts: make(map[string]int),
		token:    cfg.Token,
		tokenFP:  TokenFingerprint(cfg.Token),
		stopCh:   make(chan struct{}),
	}
}

// Magic returns the relay's framing magic byte.
func (r *Relay) Magic() byte { return r.magic }

// TokenFingerprint is the token_fp convention (sha256 hex, first 8 chars).
// Exported so the relay binary and the server agree on one definition.
func TokenFingerprint(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])[:8]
}

// Token returns the credential currently in force for the uplink.
func (r *Relay) Token() string {
	r.credMu.RLock()
	defer r.credMu.RUnlock()
	return r.token
}

// TokenFP returns the fingerprint of the credential in force.
func (r *Relay) TokenFP() string {
	r.credMu.RLock()
	defer r.credMu.RUnlock()
	return r.tokenFP
}

// setToken installs a rotated credential. Called from the uplink reader when
// the server pushes credential_rotate; the next reconnect presents it.
func (r *Relay) setToken(secret string) {
	r.credMu.Lock()
	r.token = secret
	r.tokenFP = TokenFingerprint(secret)
	r.credMu.Unlock()
}

// Stop is declared in uplink_only.go (P17-02 dial-only surface), which folds
// in the original body: releases RunUplinkOnly via stopCh, closes the
// upstream connection, stops the downstream listener.

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
	// P21-03: the uplink's state is served by the relay itself, on the
	// relay's own listen port. This is the relay binary, not the server.
	mux.HandleFunc("GET /api/v1/relay/status", r.handleStatus)

	r.httpSrv = &http.Server{
		Addr:    r.cfg.ListenAddr,
		Handler: mux,
	}

	log.Printf("[relay] listening on %s, upstreams=[%s], magic=0x%02X, max-agents=%d",
		r.cfg.ListenAddr, strings.Join(r.candidateURLs(), ", "), r.magic, r.cfg.MaxAgents)

	if r.cfg.CertFile != "" && r.cfg.KeyFile != "" {
		return r.httpSrv.ListenAndServeTLS(r.cfg.CertFile, r.cfg.KeyFile)
	}
	return r.httpSrv.ListenAndServe()
}

// connectUpstream walks the ordered candidate list and adopts the first that
// completes the upgrade. It is the only dial path: the initial connect and every
// reconnect after a drop go through the same walk, so the list is always
// re-entered from the top. Each failed candidate keeps its error (P21-09).
func (r *Relay) connectUpstream() error {
	if len(r.candidates) == 0 {
		err := fmt.Errorf("no upstream candidate configured")
		r.recordUpstreamError(err)
		return err
	}

	var failures []string
	for i, c := range r.candidates {
		r.noteAttempt(i)
		conn, err := r.dial(c)
	if err != nil {
			r.recordCandidateFailure(i, err)
			failures = append(failures, fmt.Sprintf("%s: %v", c.url, err))
			log.Printf("[relay] candidate %d/%d %s skipped: %v", i+1, len(r.candidates), c.url, err)
			continue
		}
		if err := r.adoptUpstream(i, conn); err != nil {
			conn.Close()
			// P22-05 (F5): a policy refusal is the upstream's answer, not a
			// candidate's failure. It ends the walk here and it ends the
			// reconnect loop: retrying cannot make an allowlist admit the
			// relay. Transport failures keep their old path.
			if ref, ok := err.(*ForwardRefusal); ok {
				r.handleRefusal(ref)
				return err
			}
			r.recordCandidateFailure(i, err)
			failures = append(failures, fmt.Sprintf("%s: %v", c.url, err))
			log.Printf("[relay] candidate %d/%d %s skipped: %v", i+1, len(r.candidates), c.url, err)
			continue
		}
		return nil
	}

	err := fmt.Errorf("no upstream candidate accepted the connection (tried %d: %s)",
		len(r.candidates), strings.Join(failures, "; "))
	r.recordUpstreamError(err)
	return err
}

// adoptUpstream installs a connection that completed the upgrade and sends the
// relay registration over it. Registration is part of the upgrade: a socket
// that opens but cannot register has not won, and the walk moves on.
func (r *Relay) adoptUpstream(idx int, conn *websocket.Conn) error {
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
		Token:   r.Token(),
		Metadata: &RelayMetadata{
			ListenAddr: r.cfg.ListenAddr,
			MaxAgents:  r.cfg.MaxAgents,
			Upstream:   r.candidates[idx].url,
			Version:    version.Wire(),
		},
	}
	frame, err := MakeControlFrame(r.magic, reg)
	if err != nil {
		return fmt.Errorf("build registration frame: %w", err)
	}
	if err := conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return fmt.Errorf("send registration: %w", err)
	}

	// P22-05 (F5): the registration is answered, not assumed. peer_hello
	// means the upstream accepted the link; forward_refused means it did
	// not, and the refusal is the answer — the walk ends and the code
	// travels the hop as the typed error the refused relay reports.
	if refusal := r.readRegistrationAnswer(conn); refusal != nil {
		return refusal
	}

	r.upstreamMu.Lock()
	r.upstream = conn
	r.upstreamOK.Store(true)
	r.upstreamMu.Unlock()
	r.clearUpstreamError()
	r.markCandidateUp(idx)

	// One line at connection time naming the transport that won. The transport
	// is the port actually dialled, which is what the operator points a rule at.
	log.Printf("[relay] connected to upstream %s (id=%s, transport=%s, candidate %d/%d)",
		r.candidates[idx].url, relayID, transportName(r.candidates[idx].port), idx+1, len(r.candidates))
	return nil
}

// dispatchFromServer reads framed messages from the upstream server and
// forwards payloads to the correct downstream agent.
// P17-02 FM-1.1: the loop runs under a recover — a relay panic converts to
// link-down (retrying at the peer backend) and never crosses into the server
// process. A link loss fires the per-run hook the dial-only caller blocked on.
func (r *Relay) dispatchFromServer() {
	defer r.recoverUplink()
	defer r.signalUplinkDown()
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
			r.recordUpstreamError(err)
			// P21-09: the transport that was carrying the link is named as the
			// one that went down; the watcher then re-walks the list from the top.
			r.recordCandidateLinkLoss(err)
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
			// Control message from server (heartbeat ack, peer_hello, ...)
			var ctrl ControlMessage
			if err := json.Unmarshal(payload, &ctrl); err == nil {
				// P21-03: the upstream server pushes its identity down this
				// same socket on registration success. Store it; retain it on
				// link loss so a dead uplink stays nameable.
				if ctrl.Type == "peer_hello" && ctrl.Peer != nil {
					r.recordPeerHello(ctrl.Peer)
					log.Printf("[relay] peer_hello from upstream: id=%s name=%s version=%s", ctrl.Peer.ID, ctrl.Peer.Name, ctrl.Peer.Version)
				}
				// P21-07: the server rotated this relay's credential. Hold
				// the new value in memory, log the fingerprint only, and let
				// the reconnect that follows present it. The socket is
				// closed by the server immediately after this frame.
				if ctrl.Type == "credential_rotate" && ctrl.Token != "" {
					r.setToken(ctrl.Token)
					log.Printf("[relay] credential rotated by upstream (token_fp=%s)", r.TokenFP())
				}
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

// refusalAnswerTimeout bounds the wait for the upstream's answer to the
// relay's registration. Same one-frame-one-answer discipline as the server's
// own forwarder (forward_uplink.go): peer_hello on success, forward_refused
// on refusal.
const refusalAnswerTimeout = 5 * time.Second

// readRegistrationAnswer reads the single control frame the upstream answers
// a relay_register with. nil means accepted (peer_hello — or, tolerantly, a
// frame the relay does not recognise, which a healthy server does not send on
// channel 0 at this moment). A ForwardRefusal is returned as-is: it is the
// typed answer that must survive the hop.
func (r *Relay) readRegistrationAnswer(conn *websocket.Conn) error {
	conn.SetReadDeadline(time.Now().Add(refusalAnswerTimeout))
	_, data, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		// No answer at all is a transport fact about this candidate, not a
		// refusal: report it and let the walk (or the watcher) retry.
		return fmt.Errorf("no answer to registration: %w", err)
	}
	_, chanID, payload, err := ParseFrame(data)
	if err != nil || chanID != 0 {
		return fmt.Errorf("registration answer unparseable: %w", err)
	}
	var ctrl ControlMessage
	if err := json.Unmarshal(payload, &ctrl); err != nil {
		return fmt.Errorf("registration answer is not a control frame: %w", err)
	}
	if ctrl.Type == "forward_refused" {
		return &ForwardRefusal{Code: ctrl.RefusalCode, Reason: ctrl.RefusalReason}
	}
	if ctrl.Type == "peer_hello" && ctrl.Peer != nil {
		// The hello arrived during registration instead of in
		// dispatchFromServer's read loop; record it exactly as the loop
		// would, or the uplink's own status never learns its peer.
		r.recordPeerHello(ctrl.Peer)
	}
	// Everything else is treated as acceptance so an unrelated control frame
	// cannot wedge every registration.
	return nil
}

// handleRefusal records a policy refusal the way transport failures are
// recorded, so the operator's log names the code the peer saw, and reports
// whether the failure is terminal for the reconnect loop.
func (r *Relay) handleRefusal(ref *ForwardRefusal) {
	err := error(ref)
	log.Printf("[relay] upstream refused registration: %v", err)
	r.recordUpstreamError(err)
	r.setRefusal(ref)
}

// Redial drops the current upstream socket (P17-02 dial-now §2): the
// blocked dispatch read unblocks, fires the per-run link-down hook, and the
// peer manager's loop respawns with backoff reset. No-op when no link is up.
func (r *Relay) Redial() {
	r.upstreamMu.Lock()
	c := r.upstream
	r.upstreamMu.Unlock()
	if c != nil {
		c.Close()
	}
}

func (r *Relay) upstreamWatcher() {
	backoff := 1 * time.Second
	maxBackoff := 60 * time.Second

	for {
		time.Sleep(5 * time.Second)
		if r.upstreamOK.Load() {
			backoff = 1 * time.Second
			continue
		}

		// P22-05 (F5): a policy refusal is terminal. The refused relay does
		// not retry: an allowlist, loop or hop-budget decision does not
		// change by reconnecting, and an endless retry loop against a
		// refusal that can never succeed is the defect. A transport drop
		// (dispatchFromServer lost the socket) stays on the retry path.
		if ref := r.refusalSnapshot(); ref != nil && !ref.Retryable() {
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
			// P21-07: replay the cached first frame so the server re-admits
			// the agent. Without it the channel exists but carries no
			// identity, and a link that "reconnected" answers nothing.
			if len(ch.FirstFrame) > 0 {
				r.upstream.WriteMessage(websocket.BinaryMessage, MakeFrame(r.magic, ch.ID, ch.FirstFrame))
			}
		}
		r.upstreamMu.Unlock()
	}
	log.Printf("[relay] re-registered %d channels after reconnect", len(channels))
}

// refusalDrain bounds how long a refused upgrade is held open so the client can
// read the frame. Closing a socket that still has unread inbound bytes resets
// the connection, and a reset can discard the refusal we just wrote.
const refusalDrain = 2 * time.Second

// refuseMaxAgents answers an over-limit agent with the typed refusal instead of
// a bare 429. A pre-upgrade 429 is invisible to a WebSocket client: the
// handshake never completes, so the agent logs "websocket: bad handshake" and an
// operator cannot tell a full relay from a broken one. The upgrade therefore
// completes, one typed envelope naming the code, the configured limit and the
// live count goes over the socket, and only then is the connection closed.
func (r *Relay) refuseMaxAgents(w http.ResponseWriter, req *http.Request) {
	limit := r.cfg.MaxAgents
	current := r.channels.Count()
	reason := fmt.Sprintf("max_agents limit %d reached on link %s (current %d)", limit, r.relayID(), current)

	if !websocket.IsWebSocketUpgrade(req) {
		// A plain HTTP caller (curl, a probe) gets the same code as status
		// text: still refused, still named, still no silent cap.
		http.Error(w, protocol.CodeForwardMaxAgents+": "+reason, http.StatusTooManyRequests)
		return
	}

	conn, err := upgrader.Upgrade(w, req, nil)
	if err != nil {
		log.Printf("[relay] refusal upgrade failed from %s: %v", clientIP(req), err)
		return
	}
	frame, _ := json.Marshal(protocol.AdmissionRefusal(protocol.CodeForwardMaxAgents, reason, limit, current))
	if err := conn.WriteMessage(websocket.TextMessage, frame); err != nil {
		log.Printf("[relay] refusal frame to %s failed: %v", clientIP(req), err)
		conn.Close()
		return
	}
	log.Printf("[relay] refused agent from %s: %s limit=%d current=%d (typed frame sent)",
		clientIP(req), protocol.CodeForwardMaxAgents, limit, current)

	conn.SetReadDeadline(time.Now().Add(refusalDrain))
	_, _, _ = conn.ReadMessage()
	conn.Close()
}

// relayID is this relay's identity as the upstream records it. Registration
// assigns it before the downstream listener starts; the fallback keeps a
// refusal reason from ending in "link " with nothing after it.
func (r *Relay) relayID() string {
	if id := r.cfg.RelayID; id != "" {
		return id
	}
	return "relay"
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

	// Rate limit: max agents. The refusal leaves as a typed frame (P22-03):
	// a pre-upgrade 429 reaches a WebSocket client as "bad handshake" and
	// carries no reason at all.
	if r.channels.Count() >= r.cfg.MaxAgents {
		r.refuseMaxAgents(w, req)
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
	// P21-07: keep the client's first message. A re-registration after an
	// uplink reconnect replays it, so the server re-admits the agent on the
	// same downstream socket (the agent never reconnects).
	ch.FirstFrame = append([]byte(nil), firstData...)

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
//
// P21-08: Relay B is also the only party that can report the B↔A link. This
// function keeps a durable record of it (nested.go) and counts bytes at the
// copy points, so the server's chain object can render the hop and keep
// rendering it after it dies.
func (r *Relay) handleNestedRelay(conn *websocket.Conn, ctrl *ControlMessage, ip string, firstData []byte) {
	nestedRelayID := ctrl.RelayID
	if nestedRelayID == "" {
		nestedRelayID = fmt.Sprintf("nested-%d", time.Now().UnixMilli())
	}

	log.Printf("[relay] nested relay connected: id=%s from %s", nestedRelayID, ip)

	// P21-08: the nested link is up as of this registration. A second
	// registration of the same id is counted as a reconnect.
	listenAddr := ""
	if ctrl.Metadata != nil {
		listenAddr = ctrl.Metadata.ListenAddr
	}
	r.nestedUp(nestedRelayID, listenAddr)

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
		err := conn.WriteMessage(websocket.BinaryMessage, frame)
		// P21-08: bytes counted where the payload copy into the nested
		// frame happened, and only when the write succeeded.
		if err == nil {
			r.nestedBytesDown(nestedRelayID, len(payload))
		}
		return err
	}

	// Register the nested relay's own registration with our upstream
	// so the server knows about the relay chain.
	// Forward the relay_register with path prefix: relay/{ourID}/{nestedID}
	r.upstreamMu.Lock()
	if r.upstream != nil && r.upstreamOK.Load() {
		// Build a relay_register for the nested relay, prefixed with our relay ID
		nestedReg := ControlMessage{
			Type:     "relay_register",
			RelayID:  fmt.Sprintf("%s/relay/%s", r.cfg.RelayID, nestedRelayID),
			Token:    ctrl.Token,
			Metadata: ctrl.Metadata,
		}
		frame, _ := MakeControlFrame(r.magic, nestedReg)
		r.upstream.WriteMessage(websocket.BinaryMessage, frame)
	}
	r.upstreamMu.Unlock()

	// Main loop: read framed messages from nested relay, re-frame and forward upstream
	var closeErr error
	for {
		msgType, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[relay] nested relay %s disconnected: %v", nestedRelayID, err)
			closeErr = err
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

		if nestedChanID != 0 {
			// P21-08: payload bytes arriving from the nested relay, counted
			// on the same read that hands them on.
			r.nestedBytesUp(nestedRelayID, len(payload))
		}

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
				// P21-08: one channel is one client on this nested link.
				r.nestedChannel(nestedRelayID, ourChanID)

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
					r.nestedChannelGone(nestedRelayID, ourChanID)
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

	// P21-08: the nested link is down, and stays on record. Its channel ids
	// are retained: they are what places an agent behind this hop in the
	// server's chain object after the hop is gone.
	r.nestedDown(nestedRelayID, closeErr)

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
			r.recordUpstreamError(err)
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
