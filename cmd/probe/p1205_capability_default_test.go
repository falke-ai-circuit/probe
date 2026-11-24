package main

import (
	"testing"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// TestP1205ShippedDefaultConfigAdvertisesUpdate is the shipped-config half of
// the P12-04 F1 acceptance. A stock agent installed from the released binary
// takes its capability list from applyDefaults, not from
// internal/agent's defaultCapabilities (the config is filled in first), so both
// lists have to carry the marker the server gates agent_update on. Without it
// every stock install answers a capability refusal — measured in P12-04 at
// 0.089 s, HTTP 200 {"status":"update_in_progress"}, no [update] in the agent
// log and no .next on disk.
func TestP1205ShippedDefaultConfigAdvertisesUpdate(t *testing.T) {
	uc := &UnifiedConfig{Client: &ClientConfig{}}
	uc.applyDefaults()

	for _, c := range uc.Client.Capabilities {
		if c == agent.CapUpdate {
			return
		}
	}
	t.Fatalf("shipped default client capabilities %v omit %q: the server refuses agent_update for this node, so a stock agent cannot be updated",
		uc.Client.Capabilities, agent.CapUpdate)
}

// TestP1205ExplicitCapabilityPolicyIsNotRewritten pins the other side: an
// operator who names capabilities keeps exactly that list. The marker is a
// property of the shipped default, not something the config layer injects into
// a deliberate policy — the same boundary internal/agent's
// advertisedCapabilities observes.
func TestP1205ExplicitCapabilityPolicyIsNotRewritten(t *testing.T) {
	uc := &UnifiedConfig{Client: &ClientConfig{Capabilities: []string{"exec", "filesystem"}}}
	uc.applyDefaults()

	if len(uc.Client.Capabilities) != 2 {
		t.Fatalf("explicit capability list rewritten to %v; policy lists must be honoured verbatim", uc.Client.Capabilities)
	}
	for _, c := range uc.Client.Capabilities {
		if c == agent.CapUpdate {
			t.Fatalf("explicit policy gained %q; the default must not leak into a declared list", agent.CapUpdate)
		}
	}
}
