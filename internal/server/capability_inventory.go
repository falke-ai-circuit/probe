package server

// P8-10 (P8-02 §8.2) — capability INVENTORY.
//
// F11 already records what the agent advertises and gates commands on it. This
// file turns that record into a payload an operator can read: the advertised
// set, the protocol/agent version those capabilities belong to, which
// capabilities this version is *known* to implement, which are absent, and
// when the answer was computed.
//
// It deliberately reports absence instead of omitting keys: a caller must be
// able to tell "this agent cannot do X" from "nobody asked".
//
// Nothing here invents a route. GET /api/v1/agents/{id}/capabilities gains the
// inventory block and a `refresh=true` query parameter; with no parameters the
// response keeps today's keys (agent_id, capabilities) at the same level.

import (
	"strconv"
	"strings"
	"time"
)

// capInventoryTTL is how long an inventory answer is reused. The registry
// record is already live; the cache exists because the payload is computed per
// request and the design pins a 60 s window (§8.2).
const capInventoryTTL = 60 * time.Second

// knownCapabilities is the set a v1.19 agent can advertise. Anything outside
// this list is reported verbatim under `unknown_advertised` rather than
// silently dropped — an agent build newer than this server must stay visible.
var knownCapabilities = []string{
	"exec", "filesystem", "process", "capture", "input", "clipboard",
	"tunnel", "mitm", "debug", "exec.system", "credential", "update",
}

// versionGatedCapabilities maps a capability to the agent version that first
// implements it. Used only to explain an absence; the gate itself is the
// advertised list, never this table.
var versionGatedCapabilities = map[string]string{
	"exec.system": "1.19.0",
	"credential":  "1.19.0",
}

// capabilityInventory is the additive block returned by the capabilities
// endpoint. Every field is JSON-additive: no existing key moved or renamed.
type capabilityInventory struct {
	AgentID         string          `json:"agent_id"`
	Capabilities    []string        `json:"capabilities"`
	ProtocolVersion string          `json:"protocol_version"`
	Version         string          `json:"version"`
	Inventory       inventoryDetail `json:"inventory"`
	ProbedAtUTC     string          `json:"probed_at_utc"`
	Cached          bool            `json:"cached"`
	CacheTTLSeconds int             `json:"cache_ttl_s"`
}

type inventoryDetail struct {
	// Supported is the authoritative answer: capability -> advertised.
	Supported map[string]bool `json:"supported"`
	// Absent lists known capabilities this agent does not advertise, so the
	// caller reads a reason instead of inferring one from a missing key.
	Absent []string `json:"absent"`
	// LegacyAll is true for a pre-capability agent (no advertised list). The
	// server's gating treats an empty list as "all" for backward compatibility
	// (enrollment.go HasCapability); the flag makes that permissiveness
	// visible instead of implying the agent proved it can do everything.
	LegacyAll bool `json:"legacy_all"`
	// UnknownAdvertised carries capability names this server does not know.
	UnknownAdvertised []string `json:"unknown_advertised"`
	// Platform is what the agent reported at connect.
	Platform inventoryPlatform `json:"platform"`
}

type inventoryPlatform struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Mode     string `json:"mode"`
	Status   string `json:"status"`
	UptimeS  int64  `json:"uptime_s"`
}

// buildCapabilityInventory computes the inventory from a registry record.
// advertised is taken verbatim from the record: the inventory never upgrades
// an agent's claims, it only explains them.
func buildCapabilityInventory(rec AgentRecord) *capabilityInventory {
	advertised := append([]string(nil), rec.Capabilities...)
	proto := "1"
	if len(advertised) > 0 {
		proto = "2"
	}
	// A 1.19+ agent always advertises a non-empty list; protocol "2" is the
	// post-refactor marker recorded at connect. Where the two disagree the
	// advertised list wins for gating, and protocol_version reports "1" only
	// when nothing was advertised.
	supported := make(map[string]bool, len(knownCapabilities))
	present := make(map[string]bool, len(advertised))
	for _, c := range advertised {
		present[c] = true
	}
	var absent, unknown []string
	for _, c := range knownCapabilities {
		supported[c] = present[c]
		if !present[c] {
			absent = append(absent, c)
		}
	}
	for _, c := range advertised {
		if !isKnownCapability(c) {
			unknown = append(unknown, c)
		}
	}
	return &capabilityInventory{
		AgentID:         rec.AgentID,
		Capabilities:    advertised,
		ProtocolVersion: proto,
		Version:         rec.Version,
		Inventory: inventoryDetail{
			Supported:         supported,
			Absent:            absent,
			LegacyAll:         len(advertised) == 0,
			UnknownAdvertised: unknown,
			Platform: inventoryPlatform{
				OS:      rec.OS,
				Arch:    rec.Arch,
				Mode:    rec.Mode,
				Status:  rec.Status,
				UptimeS: rec.UptimeSeconds,
			},
		},
		ProbedAtUTC:     time.Now().UTC().Format(time.RFC3339Nano),
		CacheTTLSeconds: int(capInventoryTTL.Seconds()),
	}
}

func isKnownCapability(c string) bool {
	for _, k := range knownCapabilities {
		if k == c {
			return true
		}
	}
	return false
}

// cachedCapabilityInventory returns a cached inventory unless refresh is true
// or the cached entry is older than the TTL. The returned copy is marked
// `cached` so a caller can tell a fresh probe from a reused answer.
func (s *Server) cachedCapabilityInventory(rec AgentRecord, refresh bool) *capabilityInventory {
	now := time.Now()
	s.capsMu.Lock()
	defer s.capsMu.Unlock()
	if s.capsCache == nil {
		s.capsCache = make(map[string]*capabilityInventory)
	}
	if !refresh {
		if c, ok := s.capsCache[rec.AgentID]; ok {
			if t, err := time.Parse(time.RFC3339Nano, c.ProbedAtUTC); err == nil && now.Sub(t) < capInventoryTTL {
				out := *c
				out.Cached = true
				return &out
			}
		}
	}
	inv := buildCapabilityInventory(rec)
	s.capsCache[rec.AgentID] = inv
	return inv
}

// parseRefreshFlag accepts the query spellings operators actually type.
func parseRefreshFlag(v string) bool {
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	return err == nil && b
}
