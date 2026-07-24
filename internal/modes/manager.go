package modes

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// Mode represents a runnable PROBE mode (serve, connect, relay).
type Mode interface {
	Name() string
	Start() error
	Stop() error
	IsRunning() bool
}

// ModeFactory creates a mode instance from raw JSON config.
type ModeFactory func(cfg json.RawMessage) (Mode, error)

// Manager controls the lifecycle of all modes in a single binary.
// It allows dynamic start/stop of serve, connect, and relay modes
// at runtime via a local management API.
type Manager struct {
	mu        sync.Mutex
	modes     map[string]Mode
	factories map[string]ModeFactory

	mgmtAddr string
	mgmtSrv  *http.Server
}

// NewManager creates a mode manager with the given management API address.
func NewManager(mgmtAddr string) *Manager {
	if mgmtAddr == "" {
		mgmtAddr = ":9700"
	}
	return &Manager{
		modes:     make(map[string]Mode),
		factories: make(map[string]ModeFactory),
		mgmtAddr:  mgmtAddr,
	}
}

// RegisterFactory registers a mode factory that can create modes on demand.
func (m *Manager) RegisterFactory(name string, factory ModeFactory) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.factories[name] = factory
}

// Register adds a pre-built mode to the manager.
func (m *Manager) Register(mode Mode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.modes[mode.Name()] = mode
}

// StartMode starts a registered mode by name, optionally with config.
func (m *Manager) StartMode(name string, cfg json.RawMessage) error {
	m.mu.Lock()
	// Check if already running
	if mode, ok := m.modes[name]; ok && mode.IsRunning() {
		m.mu.Unlock()
		return fmt.Errorf("mode %s already running", name)
	}

	// Use factory to create mode if available
	factory, hasFactory := m.factories[name]
	m.mu.Unlock()

	if hasFactory {
		mode, err := factory(cfg)
		if err != nil {
			return fmt.Errorf("create mode %s: %w", name, err)
		}

		// Phase 4: wire mode manager to connect mode for remote control
		if cm, ok := mode.(*ConnectMode); ok {
			cm.SetModeManager(m.StartMode, m.StopMode, m.Status)
		}

		m.mu.Lock()
		m.modes[name] = mode
		m.mu.Unlock()
		log.Printf("[modes] starting %s", name)
		return mode.Start()
	}

	// Fall back to pre-registered mode
	m.mu.Lock()
	mode, ok := m.modes[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown mode: %s", name)
	}
	if mode.IsRunning() {
		return fmt.Errorf("mode %s already running", name)
	}
	log.Printf("[modes] starting %s", name)
	return mode.Start()
}

// GetMode returns a mode by name, or nil if not found.
func (m *Manager) GetMode(name string) Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modes[name]
}

// StopMode stops a running mode by name.
func (m *Manager) StopMode(name string) error {
	m.mu.Lock()
	mode, ok := m.modes[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("unknown mode: %s", name)
	}
	if !mode.IsRunning() {
		return fmt.Errorf("mode %s not running", name)
	}
	log.Printf("[modes] stopping %s", name)
	return mode.Stop()
}

// Status returns the status of all registered modes.
func (m *Manager) Status() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := make(map[string]bool)
	for name, mode := range m.modes {
		status[name] = mode.IsRunning()
	}
	return status
}

// StartManagementAPI starts the local HTTP management API.
func (m *Manager) StartManagementAPI() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/mgmt/status", m.handleStatus)
	mux.HandleFunc("/api/mgmt/start", m.handleStart)
	mux.HandleFunc("/api/mgmt/stop", m.handleStop)

	m.mgmtSrv = &http.Server{
		Addr:    m.mgmtAddr,
		Handler: mux,
	}

	log.Printf("[modes] management API on %s", m.mgmtAddr)
	return m.mgmtSrv.ListenAndServe()
}

// StopManagementAPI stops the management API.
func (m *Manager) StopManagementAPI() error {
	if m.mgmtSrv != nil {
		return m.mgmtSrv.Close()
	}
	return nil
}

// StopAll stops all running modes.
func (m *Manager) StopAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, mode := range m.modes {
		if mode.IsRunning() {
			log.Printf("[modes] stopping %s (shutdown)", name)
			mode.Stop()
		}
	}
}

func (m *Manager) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"data": m.Status(),
	})
}

func (m *Manager) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Mode   string          `json:"mode"`
		Config json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Mode == "" {
		http.Error(w, "mode is required", http.StatusBadRequest)
		return
	}
	if err := m.StartMode(req.Mode, req.Config); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"data": m.Status(),
	})
}

func (m *Manager) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if req.Mode == "" {
		http.Error(w, "mode is required", http.StatusBadRequest)
		return
	}
	if err := m.StopMode(req.Mode); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":   true,
		"data": m.Status(),
	})
}