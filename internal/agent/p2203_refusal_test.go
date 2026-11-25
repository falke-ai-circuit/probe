package agent

// P22-03 (F3) — the agent must be able to tell "refused by policy" from
// "the handshake broke". This test feeds the agent the exact bytes the relay
// emits (built by the same constructor, marshalled and decoded through the
// protocol envelope the read loop uses) and pins the classification.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

func TestP2203_AgentNamesPolicyRefusal(t *testing.T) {
	raw, err := json.Marshal(protocol.AdmissionRefusal(
		protocol.CodeForwardMaxAgents,
		"max_agents limit 1 reached on link relay-1a2b3c4d (current 1)",
		1, 1,
	))
	if err != nil {
		t.Fatalf("marshal refusal: %v", err)
	}

	// Exactly what the read loop does with an incoming frame.
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("refusal frame does not decode as an envelope: %v (%s)", err, raw)
	}

	line, ok := policyRefusalLine(env)
	if !ok {
		t.Fatalf("the agent did not classify %s as a policy refusal", raw)
	}
	t.Logf("agent log line: %s", line)

	for _, want := range []string{
		protocol.CodeForwardMaxAgents,
		"limit=1",
		"current=1",
		"not a handshake/auth/TLS failure",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("agent log line %q is missing %q", line, want)
		}
	}
}

func TestP2203_AuthFailureIsNotAPolicyRefusal(t *testing.T) {
	// The distinction only helps if it excludes the case it must differ from:
	// an unauthorized answer and ordinary traffic are not policy refusals.
	for _, frame := range []string{
		`{"type":"error","error":{"code":"UNAUTHORIZED","message":"unauthorized"}}`,
		`{"id":"ping-1","type":"ping"}`,
		`{"type":"error"}`,
	} {
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(frame), &env); err != nil {
			t.Fatalf("decode %s: %v", frame, err)
		}
		if line, ok := policyRefusalLine(env); ok {
			t.Errorf("frame %s was classified as a policy refusal: %q", frame, line)
		}
	}
}
