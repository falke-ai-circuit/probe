package modes

import (
	"log"
	"sync"

	"github.com/falke-ai-circuit/probe/internal/relay"
)

// RelayMode wraps the PROBE relay as a startable/stoppable mode.
type RelayMode struct {
	mu      sync.Mutex
	running bool
	r       *relay.Relay
	cfg     relay.Config
}

// NewRelayMode creates a new relay mode with the given config.
func NewRelayMode(cfg relay.Config) *RelayMode {
	return &RelayMode{
		cfg: cfg,
	}
}

func (r *RelayMode) Name() string { return "relay" }

func (r *RelayMode) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return nil
	}
	r.r = relay.New(r.cfg)
	r.running = true

	go func() {
		if err := r.r.Run(); err != nil {
			log.Printf("[relay] error: %v", err)
		}
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	return nil
}

func (r *RelayMode) Stop() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.running {
		return nil
	}
	r.running = false
	if r.r != nil {
		r.r.Stop()
	}
	log.Printf("[relay] stopped")
	return nil
}

func (r *RelayMode) IsRunning() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.running
}