package server

// P21-04 — the uplink forwarder: how a server actually forwards a client
// upstream.
//
// The relay binary (internal/relay) is one way to carry a client upstream, but
// a server that enforces `relay`/`relay_all` must be able to do it itself: it
// dials its own uplink as a relay client, registers the arriving agent as a
// channel on that link, and carries frames both ways. The wire format is the
// existing relay format — relay_register on control channel 0, channel_open,
// then one framed payload per agent message — so an unmodified upstream
// server registers the forwarded agent as relay/<link id>/<agent name>.
//
// Registration is one frame and one answer (P21-03 discipline): the upstream
// answers a successful relay_register with exactly one peer_hello, and answers
// a refusal with exactly one forward_refused frame before closing. No polling.
//
// The chain is carried in the relay_register metadata and advanced per hop, so
// the receiving server can refuse a cycle without any shared state.

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// forwardRegTimeout bounds the wait for the upstream's answer to a
// registration. Short by design: a forward that cannot start must be refused
// while the client is still connected and answerable.
const forwardRegTimeout = 3 * time.Second

// ForwardMetadata is the relay_register metadata this forwarder sends. Chain
// is the P21-04 addition; the rest is the Phase 4 shape the upstream already
// decodes.
type ForwardMetadata struct {
	ListenAddr string `json:"listen_addr,omitempty"`
	MaxAgents  int    `json:"max_agents,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	Chain      Chain  `json:"chain,omitempty"`
}

// UplinkForwarder carries agents to one upstream server over one socket.
// Channels are multiplexed on that socket exactly as a relay multiplexes
// downstream agents.
type UplinkForwarder struct {
	upstreamURL string
	token       string
	linkID      string

	writeMu sync.Mutex // one writer per socket
	mu      sync.Mutex
	conn    *websocket.Conn
	magic   byte
	nextCh  uint32
	links   map[uint32]func([]byte) // upstream channel -> local deliver
	closed  bool
	onLoss  func() // mirror liveness callback
}

// NewUplinkForwarder builds a forwarder for an upstream URL. linkID is this
// hop's identity in the chain. Accepts host:port, ws(s)://host:port, or a full
// /ws URL.
func NewUplinkForwarder(upstreamURL, token, linkID string) *UplinkForwarder {
	var b [1]byte
	rand.Read(b[:])
	return &UplinkForwarder{
		upstreamURL: normalizeUpstreamURL(upstreamURL),
		token:       token,
		linkID:      linkID,
		magic:       0x02 + (b[0] % 0xFD),
		nextCh:      1,
		links:       make(map[uint32]func([]byte)),
	}
}

// LinkID is this hop's identity as the upstream will record it.
func (f *UplinkForwarder) LinkID() string { return f.linkID }

// Close tears the uplink down. Every forwarded link reports a stale upstream
// afterwards, which is the truth: there is nothing to write through.
func (f *UplinkForwarder) Close() {
	f.mu.Lock()
	c := f.conn
	f.conn = nil
	f.closed = true
	f.mu.Unlock()
	if c != nil {
		c.Close()
	}
	if f.onLoss != nil {
		f.onLoss()
	}
}

// SetLossCallback registers a callback fired when the uplink drops, so the
// server can mark mirrored records stale.
func (f *UplinkForwarder) SetLossCallback(fn func()) { f.onLoss = fn }

// normalizeUpstreamURL turns a configured address into a dialable ws URL.
func normalizeUpstreamURL(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if !strings.HasPrefix(v, "ws://") && !strings.HasPrefix(v, "wss://") {
		v = "ws://" + strings.TrimPrefix(strings.TrimPrefix(v, "http://"), "https://")
	}
	u, err := url.Parse(v)
	if err != nil {
		return v
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/ws"
	}
	return u.String()
}

// connect dials the upstream and completes registration for the given chain.
// The chain is sent in the registration, not per agent: one link, one path.
func (f *UplinkForwarder) connect(chain Chain) error {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn != nil {
		return nil
	}

	headers := map[string][]string{}
	if f.token != "" {
		headers["Authorization"] = []string{"Bearer " + f.token}
	}
	dialer := *websocket.DefaultDialer
	c, _, err := dialer.Dial(f.upstreamURL, headers)
	if err != nil {
		return fmt.Errorf("forward: dial upstream %s: %w", f.upstreamURL, err)
	}

	reg := map[string]interface{}{
		"type":     "relay_register",
		"relay_id": f.linkID,
		"token":    f.token,
		"metadata": ForwardMetadata{Upstream: f.upstreamURL, Chain: chain},
	}
	payload, err := json.Marshal(reg)
	if err != nil {
		c.Close()
		return fmt.Errorf("forward: marshal registration: %w", err)
	}
	if err := c.WriteMessage(websocket.BinaryMessage, makeRelayFrame(f.magic, 0, payload)); err != nil {
		c.Close()
		return fmt.Errorf("forward: send registration: %w", err)
	}

	// Exactly one answer: peer_hello on success, forward_refused on refusal.
	c.SetReadDeadline(time.Now().Add(forwardRegTimeout))
	_, data, err := c.ReadMessage()
	c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return fmt.Errorf("forward: no answer to registration from %s: %w", f.upstreamURL, err)
	}
	_, chanID, body, ferr := parseRelayFrame(data)
	if ferr != nil {
		c.Close()
		return fmt.Errorf("forward: registration answer unparseable: %w", ferr)
	}
	var ctrl struct {
		Type   string `json:"type"`
		Code   string `json:"code"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(body, &ctrl); err != nil || chanID != 0 {
		c.Close()
		return fmt.Errorf("forward: registration answer from %s is not a control frame", f.upstreamURL)
	}
	switch ctrl.Type {
	case "peer_hello":
		// accepted
	case "forward_refused":
		c.Close()
		return &ForwardRefusalError{Code: ctrl.Code, Reason: ctrl.Reason}
	default:
		c.Close()
		return &ForwardRefusalError{
			Code:   RefusalPolicyDenied,
			Reason: fmt.Sprintf("upstream %s answered registration with %q", f.upstreamURL, ctrl.Type),
		}
	}

	f.mu.Lock()
	f.conn = c
	f.mu.Unlock()
	go f.dispatch()
	return nil
}

// dispatch reads upstream frames and delivers each agent payload to its local
// handler. When the socket dies the mirror is marked stale — the record stays
// visible, but no write may answer from it.
func (f *UplinkForwarder) dispatch() {
	for {
		f.mu.Lock()
		c := f.conn
		f.mu.Unlock()
		if c == nil {
			return
		}
		_, data, err := c.ReadMessage()
		if err != nil {
			f.mu.Lock()
			f.conn = nil
			f.closed = true
			f.mu.Unlock()
			c.Close()
			if f.onLoss != nil {
				f.onLoss()
			}
			return
		}
		_, chanID, payload, perr := parseRelayFrame(data)
		if perr != nil {
			continue
		}
		if chanID == 0 {
			// A refusal aimed at ONE channel must reach that channel's client
			// instead of disappearing as a disconnect: the client is still
			// connected to the downstream server and can be told why.
			var ctrl struct {
				Type      string `json:"type"`
				ChannelID uint32 `json:"channel_id"`
				Code      string `json:"code"`
				Reason    string `json:"reason"`
			}
			if json.Unmarshal(payload, &ctrl) == nil && ctrl.Type == "forward_refused" {
				f.mu.Lock()
				deliver := f.links[ctrl.ChannelID]
				f.mu.Unlock()
				if deliver != nil {
					out, _ := json.Marshal(map[string]string{
						"type": "error", "code": ctrl.Code, "error": ctrl.Reason,
					})
					deliver(out)
				}
			}
			continue
		}
		f.mu.Lock()
		deliver := f.links[chanID]
		f.mu.Unlock()
		if deliver != nil {
			deliver(payload)
		}
	}
}

// ForwardRegistration is what ForwardAgent needs: the agent's id, the chain to
// extend, and where upstream frames should be delivered locally.
type ForwardRegistration struct {
	AgentID string
	Chain   Chain
	Deliver func([]byte)
}

// ForwardedLink is one agent's channel on the uplink.
type ForwardedLink struct {
	f     *UplinkForwarder
	chID  uint32
	Agent string
	Via   string
}

// Send writes one agent payload upstream.
func (l *ForwardedLink) Send(payload []byte) error {
	l.f.mu.Lock()
	c := l.f.conn
	l.f.mu.Unlock()
	if c == nil {
		return &ForwardRefusalError{
			Code:    RefusalMirrorStale,
			AgentID: l.Agent,
			Reason:  fmt.Sprintf("mirror is read-only: agent %s relayed via %s has no live upstream", l.Agent, l.Via),
		}
	}
	l.f.writeMu.Lock()
	defer l.f.writeMu.Unlock()
	return c.WriteMessage(websocket.BinaryMessage, makeRelayFrame(l.f.magic, l.chID, payload))
}

// Close drops the local channel registration. The agent is gone; the upstream
// learns it from channel_close.
func (l *ForwardedLink) Close() {
	l.f.mu.Lock()
	delete(l.f.links, l.chID)
	c := l.f.conn
	l.f.mu.Unlock()
	if c == nil {
		return
	}
	ctrl, _ := json.Marshal(map[string]interface{}{
		"type": "channel_close", "channel_id": l.chID, "relay_id": l.f.linkID,
	})
	l.f.writeMu.Lock()
	c.WriteMessage(websocket.BinaryMessage, makeRelayFrame(l.f.magic, 0, ctrl))
	l.f.writeMu.Unlock()
}

// ForwardAgent registers one agent upstream and returns the link that carries
// it. The chain is advanced by this hop before it is sent: a relay that
// forwards into a server already in its own uplink chain is the cycle the
// guard exists to refuse.
func (f *UplinkForwarder) ForwardAgent(req ForwardRegistration, first []byte) (*ForwardedLink, error) {
	next, err := req.Chain.Next(f.linkID)
	if err != nil {
		return nil, err
	}
	if err := f.connect(next); err != nil {
		return nil, err
	}

	f.mu.Lock()
	chID := f.nextCh
	f.nextCh++
	if req.Deliver != nil {
		f.links[chID] = req.Deliver
	}
	c := f.conn
	f.mu.Unlock()

	open, _ := json.Marshal(map[string]interface{}{
		"type": "channel_open", "channel_id": chID, "relay_id": f.linkID,
	})
	f.writeMu.Lock()
	if err := c.WriteMessage(websocket.BinaryMessage, makeRelayFrame(f.magic, 0, open)); err != nil {
		f.writeMu.Unlock()
		f.mu.Lock()
		delete(f.links, chID)
		f.mu.Unlock()
		return nil, fmt.Errorf("forward: channel_open: %w", err)
	}
	// The agent's first message (its AgentInfo) is the upstream's registration
	// signal on this channel — same order the relay sends it.
	if err := c.WriteMessage(websocket.BinaryMessage, makeRelayFrame(f.magic, chID, first)); err != nil {
		f.writeMu.Unlock()
		f.mu.Lock()
		delete(f.links, chID)
		f.mu.Unlock()
		return nil, fmt.Errorf("forward: first frame: %w", err)
	}
	f.writeMu.Unlock()

	return &ForwardedLink{f: f, chID: chID, Agent: req.AgentID, Via: f.linkID}, nil
}

// makeRelayFrame builds the 5-byte header + payload frame the relay wire
// format uses.
func makeRelayFrame(magic byte, chanID uint32, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = magic
	binary.BigEndian.PutUint32(frame[1:5], chanID)
	copy(frame[5:], payload)
	return frame
}
