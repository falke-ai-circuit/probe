package relay

// P21-08 — the nested link record.
//
// A relay that carries another relay (relay chaining) is the only party that
// can say anything about that link: the server upstream never sees the nested
// socket. This file is the relay's record of each nested relay it carries, kept
// outside the per-connection state so a nested link that dies stays nameable —
// the chain object the server composes reads these entries through the relay's
// own status document and must still find the dead hop there.
//
// Bytes are counted where they move: in the nested read loop (payload bytes
// arriving from the nested relay) and in nestedWrite (payload bytes written to
// it), only after the write succeeded. Nothing here is derived from message
// counts.
//
// What resets: state (from the live socket), last_error (cleared by a
// re-registration of the same nested id), channels (gauge, from the live map).
// What does not: agents_carried (distinct channels ever opened), bytes_relayed
// and reconnects.

import (
	"sort"
	"time"
)

// nestedLink is one nested relay's durable record on this relay.
type nestedLink struct {
	id             string
	listenAddr     string
	state          string // "up" | "down"
	lastError      string
	channels       map[uint32]bool // our channel ids currently carried for it
	carried        map[uint32]bool // every channel ever opened for it
	bytesFromRelay int64
	bytesToRelay   int64
	reconnects     int
	connectedAt    time.Time
	lastSeen       time.Time
}

// NestedStatus is one nested link as served in the relay's status document.
type NestedStatus struct {
	ID             string   `json:"id"`
	ListenAddr     string   `json:"listen_addr"`
	State          string   `json:"state"`
	LastError      string   `json:"last_error"`
	Channels       []uint32 `json:"channels"` // our channel ids carried for it
	AgentsCarried  int      `json:"agents_carried"`
	BytesRelayed   int64    `json:"bytes_relayed"`
	BytesFromRelay int64    `json:"bytes_from_relay"`
	BytesToRelay   int64    `json:"bytes_to_relay"`
	// Reconnects counts re-registrations of the same nested relay id on this
	// relay. There is no agent registry on this side of the link, so this is
	// the link's own count; the server-side link uses AgentRecord.Reconnects.
	Reconnects  int    `json:"reconnects"`
	ConnectedAt string `json:"connected_at,omitempty"`
	LastSeen    string `json:"last_seen,omitempty"`
}

func (r *Relay) nestedFor(id string) *nestedLink {
	if r.nested == nil {
		r.nested = make(map[string]*nestedLink)
	}
	nl, ok := r.nested[id]
	if !ok {
		nl = &nestedLink{
			id:       id,
			channels: make(map[uint32]bool),
			carried:  make(map[uint32]bool),
		}
		r.nested[id] = nl
	}
	return nl
}

// nestedUp records a nested registration. A second registration under the same
// id is a reconnect and is counted as one.
func (r *Relay) nestedUp(id, listenAddr string) {
	if id == "" {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	now := time.Now().UTC()
	if nl.connectedAt.IsZero() {
		nl.connectedAt = now
	} else {
		nl.reconnects++
	}
	if listenAddr != "" {
		nl.listenAddr = listenAddr
	}
	nl.state = "up"
	nl.lastError = ""
	nl.lastSeen = now
}

// nestedDown records the failure that closed a nested link, retaining every
// counter and the channel set: the hop stays represented.
func (r *Relay) nestedDown(id string, err error) {
	if id == "" {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	nl.state = "down"
	if err != nil {
		nl.lastError = err.Error()
	} else {
		nl.lastError = "nested relay socket closed without an error"
	}
	nl.lastSeen = time.Now().UTC()
}

// nestedChannel records that this relay allocated a channel for the nested
// relay. Agents carried counts distinct channels: one channel is one client.
func (r *Relay) nestedChannel(id string, ch uint32) {
	if id == "" {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	nl.channels[ch] = true
	nl.carried[ch] = true
}

// nestedChannelGone drops a channel from the live set, retaining it in the
// carried set so agents_carried stays monotonic.
func (r *Relay) nestedChannelGone(id string, ch uint32) {
	if id == "" {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	delete(nl.channels, ch)
}

func (r *Relay) nestedBytesUp(id string, n int) {
	if id == "" || n <= 0 {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	nl.bytesFromRelay += int64(n)
	nl.lastSeen = time.Now().UTC()
}

func (r *Relay) nestedBytesDown(id string, n int) {
	if id == "" || n <= 0 {
		return
	}
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	nl := r.nestedFor(id)
	nl.bytesToRelay += int64(n)
	nl.lastSeen = time.Now().UTC()
}

// nestedSnapshot copies the nested links out, sorted by id.
func (r *Relay) nestedSnapshot() []NestedStatus {
	r.nestedMu.Lock()
	defer r.nestedMu.Unlock()
	out := make([]NestedStatus, 0, len(r.nested))
	for _, nl := range r.nested {
		chans := make([]uint32, 0, len(nl.channels))
		for ch := range nl.channels {
			chans = append(chans, ch)
		}
		sort.Slice(chans, func(i, j int) bool { return chans[i] < chans[j] })
		st := NestedStatus{
			ID:             nl.id,
			ListenAddr:     nl.listenAddr,
			State:          nl.state,
			LastError:      nl.lastError,
			Channels:       chans,
			AgentsCarried:  len(nl.carried),
			BytesRelayed:   nl.bytesFromRelay + nl.bytesToRelay,
			BytesFromRelay: nl.bytesFromRelay,
			BytesToRelay:   nl.bytesToRelay,
			Reconnects:     nl.reconnects,
		}
		if !nl.connectedAt.IsZero() {
			st.ConnectedAt = nl.connectedAt.Format(time.RFC3339Nano)
		}
		if !nl.lastSeen.IsZero() {
			st.LastSeen = nl.lastSeen.Format(time.RFC3339Nano)
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
