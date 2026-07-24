package modes

import (
	"log"
	"sync"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// ConnectMode wraps the PROBE agent/client as a startable/stoppable mode.
type ConnectMode struct {
	mu      sync.Mutex
	running bool
	ag      *agent.Agent
	cfg     agent.Config
	doneCh  chan struct{}
}

// NewConnectMode creates a new connect mode with the given agent config.
func NewConnectMode(cfg agent.Config) *ConnectMode {
	return &ConnectMode{
		cfg:    cfg,
		doneCh: make(chan struct{}),
	}
}

func (c *ConnectMode) Name() string { return "connect" }

func (c *ConnectMode) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return nil
	}
	c.ag = agent.New(c.cfg)
	c.running = true

	go func() {
		if err := c.ag.Run(); err != nil {
			log.Printf("[connect] agent error: %v", err)
		}
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		close(c.doneCh)
	}()

	return nil
}

func (c *ConnectMode) Stop() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.running {
		return nil
	}
	c.running = false
	if c.ag != nil {
		c.ag.Stop()
	}
	log.Printf("[connect] agent stopped")
	return nil
}

func (c *ConnectMode) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}