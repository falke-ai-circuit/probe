package agent

import (
	"fmt"
	"log"

	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/gorilla/websocket"
)

// dialWithFailover attempts a direct connection to the primary server URL.
// If that fails and relays are configured, it tries each relay in order.
// Returns the first successful connection, or the last error if all fail.
//
// This is used by runOutbound to add relay failover without rewriting the
// existing reconnect loop. The caller still owns backoff/retry logic —
// this function is a single "best attempt to connect right now" call.
func (a *Agent) dialWithFailover() (*websocket.Conn, error) {
	// Try direct connection first
	conn, err := protocol.Dial(a.cfg.URL, a.cfg.CertPath, a.cfg.ClientCertFile, a.cfg.ClientKeyFile, a.cfg.Token)
	if err == nil {
		return conn, nil
	}

	// Direct failed — no relays configured, return the original error
	if len(a.cfg.Relays) == 0 {
		return nil, err
	}

	// Try each relay in order
	directErr := err
	for i, relay := range a.cfg.Relays {
		if relay.URL == "" {
			continue
		}
		log.Printf("Direct connection failed (%v), trying relay %d/%d: %s", directErr, i+1, len(a.cfg.Relays), relay.URL)
		conn, relayErr := protocol.Dial(relay.URL, a.cfg.CertPath, a.cfg.ClientCertFile, a.cfg.ClientKeyFile, relay.Token)
		if relayErr == nil {
			log.Printf("Connected via relay %s", relay.URL)
			return conn, nil
		}
		log.Printf("Relay %s failed: %v", relay.URL, relayErr)
	}

	// All relays failed — return a combined error
	return nil, fmt.Errorf("direct connection failed: %w (also tried %d relay(s), all failed)", directErr, len(a.cfg.Relays))
}