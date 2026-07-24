package relay

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// FrameVersion is the protocol version byte for relay framing.
// Using a variable (not const) so it can be randomized per relay instance
// to prevent Suricata signature matching on a fixed byte at offset 0.
var frameMagic byte = 0x7B // default, overridden by Relay.Run()

// ControlMessage is sent on channelID=0 for relay lifecycle events.
type ControlMessage struct {
	Type      string          `json:"type"`       // "relay_register", "channel_open", "channel_close", "heartbeat"
	ChannelID uint32          `json:"channel_id,omitempty"`
	RelayID   string          `json:"relay_id,omitempty"`
	Token     string          `json:"token,omitempty"`
	AgentInfo json.RawMessage `json:"agent_info,omitempty"` // for channel_open: the agent's AgentInfo envelope
	Metadata  *RelayMetadata  `json:"metadata,omitempty"`   // Phase 4: relay topology metadata
}

// RelayMetadata carries relay topology information to the server.
type RelayMetadata struct {
	ListenAddr string `json:"listen_addr,omitempty"`
	MaxAgents  int    `json:"max_agents,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	AgentCount int    `json:"agent_count,omitempty"` // updated on channel_open/close
}

// Frame parses a relay framing header from a byte slice.
// Returns: magic byte, channelID, payload, error.
func ParseFrame(data []byte) (byte, uint32, []byte, error) {
	if len(data) < 5 {
		return 0, 0, nil, fmt.Errorf("frame too short: %d bytes", len(data))
	}
	magic := data[0]
	chanID := binary.BigEndian.Uint32(data[1:5])
	payload := data[5:]
	return magic, chanID, payload, nil
}

// MakeFrame constructs a framed message for sending over the upstream WebSocket.
func MakeFrame(magic byte, chanID uint32, payload []byte) []byte {
	frame := make([]byte, 5+len(payload))
	frame[0] = magic
	binary.BigEndian.PutUint32(frame[1:5], chanID)
	copy(frame[5:], payload)
	return frame
}

// MakeControlFrame constructs a control message frame (channelID=0).
func MakeControlFrame(magic byte, msg ControlMessage) ([]byte, error) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal control message: %w", err)
	}
	return MakeFrame(magic, 0, payload), nil
}

// ChannelMap manages the mapping of channelID → downstream agent connection.
// It is safe for concurrent use.
type ChannelMap struct {
	mu       sync.RWMutex
	channels map[uint32]*Channel
	nextID   atomic.Uint32
}

// Channel represents a single downstream agent connection through the relay.
type Channel struct {
	ID       uint32
	Conn     *websocket.Conn // downstream agent WebSocket connection
	OnClose  func()
	sendBuf  chan []byte // buffered messages waiting for upstream
	closed   bool
	closeMu  sync.Mutex
}

const (
	defaultBufferSize   = 256
	defaultChannelStart = 1
)

// NewChannelMap creates a new ChannelMap.
func NewChannelMap() *ChannelMap {
	cm := &ChannelMap{
		channels: make(map[uint32]*Channel),
	}
	cm.nextID.Store(defaultChannelStart)
	return cm
}

// Alloc allocates a new channel ID and registers the channel.
func (cm *ChannelMap) Alloc(conn *websocket.Conn, onClose func()) *Channel {
	id := cm.nextID.Add(1)
	if id == 0 { // wrapped around — skip 0 (reserved for control)
		id = cm.nextID.Add(1)
	}
	ch := &Channel{
		ID:      id,
		Conn:    conn,
		OnClose: onClose,
		sendBuf: make(chan []byte, defaultBufferSize),
	}
	cm.mu.Lock()
	cm.channels[id] = ch
	cm.mu.Unlock()
	return ch
}

// Get returns the channel for the given ID, or nil if not found.
func (cm *ChannelMap) Get(id uint32) *Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.channels[id]
}

// Close marks a channel as closed and removes it from the map.
func (cm *ChannelMap) Close(id uint32) {
	cm.mu.Lock()
	ch, ok := cm.channels[id]
	if ok {
		delete(cm.channels, id)
	}
	cm.mu.Unlock()
	if ch != nil {
		ch.close()
	}
}

// All returns a snapshot of all active channels.
func (cm *ChannelMap) All() []*Channel {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	result := make([]*Channel, 0, len(cm.channels))
	for _, ch := range cm.channels {
		result = append(result, ch)
	}
	return result
}

// Count returns the number of active channels.
func (cm *ChannelMap) Count() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.channels)
}

func (ch *Channel) close() {
	ch.closeMu.Lock()
	defer ch.closeMu.Unlock()
	if ch.closed {
		return
	}
	ch.closed = true
	close(ch.sendBuf)
	if ch.OnClose != nil {
		ch.OnClose()
	}
}

// IsClosed returns true if the channel has been closed.
func (ch *Channel) IsClosed() bool {
	ch.closeMu.Lock()
	defer ch.closeMu.Unlock()
	return ch.closed
}

// QueueMessage attempts to buffer a message for upstream delivery.
// Returns false if the buffer is full (backpressure — caller should close channel).
func (ch *Channel) QueueMessage(data []byte) bool {
	select {
	case ch.sendBuf <- data:
		return true
	default:
		return false // buffer full
	}
}

// Drain returns the channel's buffered message queue for consumption.
func (ch *Channel) Drain() <-chan []byte {
	return ch.sendBuf
}