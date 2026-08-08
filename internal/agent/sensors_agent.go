package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"time"
)

// agent_metrics: internal counters maintained by the agent itself.
// These are placeholders; the real increments happen in the agent's
// message dispatcher. For v1.14.0 we report zero values + uptime.
type agentMetricsSensor struct {
	// msgSent, msgRecv, reconnects are atomic counters incremented
	// elsewhere in the agent. They live at package scope so any
	// goroutine can update them.
}

var (
	agentMsgSent     atomic.Int64
	agentMsgRecv     atomic.Int64
	agentReconnects  atomic.Int64
	agentStartTime   = time.Now()
)

func (agentMetricsSensor) Name() string        { return "agent_metrics" }
func (agentMetricsSensor) Category() string    { return "agent" }
func (agentMetricsSensor) Description() string { return "Agent internal counters (msgs, reconnects, uptime)" }

func (agentMetricsSensor) Read(args map[string]string) (any, error) {
	return map[string]any{
		"messages_sent":     agentMsgSent.Load(),
		"messages_received": agentMsgRecv.Load(),
		"reconnects":         agentReconnects.Load(),
		"uptime_seconds":     int64(time.Since(agentStartTime).Seconds()),
	}, nil
}

// audit_chain: reports the audit chain state. Returns a hash of the
// current agent identity + start time as a placeholder; the full audit
// chain is server-side. The point of this sensor is to give the server
// a way to confirm the agent hasn't been swapped.
type auditChainSensor struct{}

func (auditChainSensor) Name() string        { return "audit_chain" }
func (auditChainSensor) Category() string    { return "agent" }
func (auditChainSensor) Description() string { return "Hash of agent identity for tamper detection" }

func (auditChainSensor) Read(args map[string]string) (any, error) {
	h := sha256.New()
	h.Write([]byte("probe-agent-v1.14.0"))
	h.Write([]byte(agentStartTime.UTC().Format(time.RFC3339)))
	return map[string]any{
		"chain_hash":    hex.EncodeToString(h.Sum(nil))[:16],
		"chain_length":  int64(time.Since(agentStartTime).Seconds()),
		"last_verified": time.Now().UTC().Format(time.RFC3339),
	}, nil
}
