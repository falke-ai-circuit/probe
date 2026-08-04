package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/relay"
)

// UnifiedConfig is the Phase 4 config format with optional sections for
// server, client, and relay. It supports backward compatibility with the
// flat config format ({"server":"...","token":"...","name":"..."}).
type UnifiedConfig struct {
	Name  string `json:"name"`
	Token string `json:"token"`

	Server *ServerConfig `json:"server,omitempty"`
	Client *ClientConfig `json:"client,omitempty"`
	Relay  *RelayConfig  `json:"relay,omitempty"`

	// Management API listen address (default :9700)
	MgmtAddr string `json:"mgmt_addr,omitempty"`
}

// ServerConfig holds settings for serve mode.
type ServerConfig struct {
	Addr            string  `json:"addr"`
	AdminPassword   string  `json:"admin_password"`
	AllowedCIDR     string  `json:"allowed_cidr"`
	RateLimit       float64 `json:"rate_limit"`
	RateBurst       int     `json:"rate_burst"`
	MaxConcurrent   int     `json:"max_concurrent"`
	RegistryPath    string  `json:"registry_path"`
	EnrollmentPath  string  `json:"enrollment_path"`
	OperatorPath    string  `json:"operator_path"`
	CertFile        string  `json:"cert_file"`
	KeyFile         string  `json:"key_file"`
	VTAPIKey        string  `json:"vt_api_key"`
	Proxy           string  `json:"proxy"`
	E2EEnabled      bool    `json:"e2e_enabled"` // Step 13: end-to-end encryption
}

// RelayEntryConfig is a single relay endpoint in the unified config.
type RelayEntryConfig struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// ClientConfig holds settings for connect mode.
type ClientConfig struct {
	Server      string   `json:"server"`
	Token       string   `json:"token"`
	Name        string   `json:"name"`
	Mode        string   `json:"mode"`
	Permissions string   `json:"permissions"`
	Listen      string   `json:"listen"`
	MaxRetries  int      `json:"maxRetries"`
	BackoffMin  string   `json:"backoffMin"`
	BackoffMax  string   `json:"backoffMax"`
	TokenFile   string   `json:"tokenFile"`
	Cert        string   `json:"cert"`
	ClientCert  string   `json:"clientCert"`
	ClientKey   string   `json:"clientKey"`
	CertFile    string   `json:"certFile"`
	KeyFile     string   `json:"keyFile"`
	SandboxDir  string   `json:"sandbox_dir"`
	Capabilities []string `json:"capabilities"`
	// Relays is an ordered list of relay endpoints for failover.
	// When the direct server connection fails, the agent tries each relay in order.
	Relays []RelayEntryConfig `json:"relays,omitempty"`
	E2EEnabled bool `json:"e2e_enabled,omitempty"` // Step 13: end-to-end encryption
}

// RelayConfig holds settings for relay mode.
type RelayConfig struct {
	Listen      string `json:"listen"`
	Upstream    string `json:"upstream"`
	Token       string `json:"token"`
	AgentTokens string `json:"agent_tokens"`
	CertFile    string `json:"cert_file"`
	KeyFile     string `json:"key_file"`
}

// LoadConfig reads a JSON config file and returns a UnifiedConfig.
// It supports both the new structured format (with server/client/relay
// sections) and the legacy flat format ({"server":"...","token":"..."}).
func LoadConfig(path string) (*UnifiedConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return ParseConfig(data)
}

// ParseConfig parses JSON config bytes into a UnifiedConfig.
// Handles both structured and legacy flat formats.
func ParseConfig(data []byte) (*UnifiedConfig, error) {
	// First, unmarshal into a raw map to detect the format
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Detect format: if "server" field is a string, it's legacy flat format
	// If "server" is an object (or absent), it's the new structured format
	if serverRaw, hasServer := raw["server"]; hasServer {
		// Check if "server" is a string (legacy) or object (new)
		var serverStr string
		if err := json.Unmarshal(serverRaw, &serverStr); err == nil {
			// "server" is a string → legacy flat format
			var flat ConfigFile
			if err := json.Unmarshal(data, &flat); err != nil {
				return nil, fmt.Errorf("parse config (legacy): %w", err)
			}
			uc := flatToUnified(flat)
			uc.applyDefaults()
			return uc, nil
		}
	}

	// Structured format (or no server field at all)
	var uc UnifiedConfig
	if err := json.Unmarshal(data, &uc); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// If no sections are populated but we have raw fields, try legacy
	if uc.Server == nil && uc.Client == nil && uc.Relay == nil {
		// Check for legacy fields: token, name, mode at top level
		var flat ConfigFile
		if err := json.Unmarshal(data, &flat); err == nil {
			if flat.Token != "" || flat.Name != "" || flat.Mode != "" {
				uc = *flatToUnified(flat)
			}
		}
	}

	uc.applyDefaults()
	return &uc, nil
}

// flatToUnified converts a legacy ConfigFile to a UnifiedConfig.
func flatToUnified(flat ConfigFile) *UnifiedConfig {
	uc := &UnifiedConfig{
		Name:  flat.Name,
		Token: flat.Token,
	}

	if flat.Server != "" || flat.Listen != "" {
		uc.Client = &ClientConfig{
			Server:      flat.Server,
			Token:       flat.Token,
			Name:        flat.Name,
			Mode:        flat.Mode,
			Permissions: flat.Permissions,
			Listen:      flat.Listen,
			MaxRetries:  flat.MaxRetries,
			BackoffMin:  flat.BackoffMin,
			BackoffMax:  flat.BackoffMax,
			TokenFile:   flat.TokenFile,
			Cert:        flat.Cert,
			ClientCert:  flat.ClientCert,
			ClientKey:   flat.ClientKey,
			CertFile:    flat.CertFile,
			KeyFile:     flat.KeyFile,
			SandboxDir:  flat.SandboxDir,
			Capabilities: flat.Capabilities,
			Relays:      flat.Relays,
		}
	}

	return uc
}

// applyDefaults fills in default values for missing fields.
func (uc *UnifiedConfig) applyDefaults() {
	if uc.MgmtAddr == "" {
		uc.MgmtAddr = ":9700"
	}

	if uc.Client != nil {
		if uc.Client.Mode == "" {
			uc.Client.Mode = "silent"
		}
		if uc.Client.Name == "" {
			uc.Client.Name = uc.Name
		}
		if uc.Client.Name == "" {
			uc.Client.Name = "probe-client"
		}
		if uc.Client.Permissions == "" {
			uc.Client.Permissions = "full"
		}
		// Ensure WebSocket URL includes /ws path
		if uc.Client.Server != "" && !strings.Contains(uc.Client.Server, "/ws") {
			uc.Client.Server = strings.TrimRight(uc.Client.Server, "/") + "/ws"
		}
	}

	if uc.Server != nil {
		if uc.Server.Addr == "" {
			uc.Server.Addr = "localhost:7700"
		}
		if uc.Server.RateLimit == 0 {
			uc.Server.RateLimit = 10.0
		}
		if uc.Server.RateBurst == 0 {
			uc.Server.RateBurst = 20
		}
		if uc.Server.MaxConcurrent == 0 {
			uc.Server.MaxConcurrent = 5
		}
		if uc.Server.RegistryPath == "" {
			uc.Server.RegistryPath = "/tmp/probe-registry.json"
		}
		if uc.Server.AllowedCIDR == "" {
			uc.Server.AllowedCIDR = "100.64.0.0/10,10.10.10.0/24,172.16.0.0/12"
		}
		if uc.Server.EnrollmentPath == "" {
			uc.Server.EnrollmentPath = "/tmp/probe-enrollment.json"
		}
		if uc.Server.OperatorPath == "" {
			uc.Server.OperatorPath = "/tmp/probe-operators.json"
		}
	}

	if uc.Relay != nil {
		if uc.Relay.Listen == "" {
			uc.Relay.Listen = ":7701"
		}
		// If no upstream specified, use client's server URL
		if uc.Relay.Upstream == "" && uc.Client != nil {
			uc.Relay.Upstream = uc.Client.Server
		}
		// If no token specified, use top-level or client token
		if uc.Relay.Token == "" {
			uc.Relay.Token = uc.Token
		}
		if uc.Relay.Token == "" && uc.Client != nil {
			uc.Relay.Token = uc.Client.Token
		}
	}
}

// AutoDetect determines which modes to start based on config.
// Returns: (startServe, startConnect, startRelay)
func (uc *UnifiedConfig) AutoDetect() (bool, bool, bool) {
	startConnect := uc.Client != nil && uc.Client.Server != ""
	startServe := !startConnect && (uc.Server != nil || true) // default to server
	startRelay := uc.Relay != nil

	// If server section explicitly present and no client, start server
	if uc.Server != nil && !startConnect {
		startServe = true
	}

	// If no config sections at all, start server with defaults
	if uc.Server == nil && uc.Client == nil && uc.Relay == nil {
		startServe = true
	}

	return startServe, startConnect, startRelay
}

// ToAgentConfig converts ClientConfig to agent.Config.
func (uc *UnifiedConfig) ToAgentConfig() agent.Config {
	if uc.Client == nil {
		return agent.Config{}
	}

	c := uc.Client
	backoffMin, err := time.ParseDuration(c.BackoffMin)
	if err != nil || backoffMin <= 0 {
		backoffMin = 1 * time.Second
	}
	backoffMax, err := time.ParseDuration(c.BackoffMax)
	if err != nil || backoffMax <= 0 {
		backoffMax = 60 * time.Second
	}

	tokenFile := c.TokenFile
	if tokenFile == "" {
		tokenFile = ".probe-token"
	}

	tokenVal := strings.Trim(c.Token, "\"'")

	mode := "outbound"
	if c.Listen != "" {
		mode = "inbound"
	}

	cfg := agent.Config{
		Mode:           mode,
		URL:            c.Server,
		Addr:           c.Listen,
		Token:          tokenVal,
		CertPath:       c.Cert,
		ClientCertFile: c.ClientCert,
		ClientKeyFile:  c.ClientKey,
		CertFile:       c.CertFile,
		KeyFile:        c.KeyFile,
		Name:           c.Name,
		MaxRetries:     c.MaxRetries,
		BackoffMin:     backoffMin,
		BackoffMax:     backoffMax,
		TokenFile:      tokenFile,
		Permissions:    c.Permissions,
		SandboxDir:     c.SandboxDir,
		Capabilities:   c.Capabilities,
		E2EEnabled:      c.E2EEnabled,
	}

	// Convert relay endpoints from config format to agent format
	for _, r := range c.Relays {
		relayURL := r.URL
		if relayURL != "" && !strings.Contains(relayURL, "/ws") {
			relayURL = strings.TrimRight(relayURL, "/") + "/ws"
		}
		cfg.Relays = append(cfg.Relays, agent.RelayEndpoint{
			URL:   relayURL,
			Token: strings.Trim(r.Token, "\"'"),
		})
	}

	return cfg
}

// ToRelayConfig converts RelayConfig to relay.Config.
func (uc *UnifiedConfig) ToRelayConfig() relay.Config {
	if uc.Relay == nil {
		return relay.Config{}
	}
	r := uc.Relay
	return relay.Config{
		UpstreamURL:  r.Upstream,
		ListenAddr:   r.Listen,
		Token:        strings.Trim(r.Token, "\"'"),
		AgentTokens:  r.AgentTokens,
		CertFile:     r.CertFile,
		KeyFile:      r.KeyFile,
		MaxAgents:    100,
		MaxPerIP:     10,
	}
}

// LoadConfigFromB64 decodes a base64-encoded JSON config (used by ldflags injection).
func LoadConfigFromB64(b64 string) (*UnifiedConfig, error) {
	data, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, fmt.Errorf("base64 decode: %w", err)
	}
	return ParseConfig(data)
}

// printConfigUsage prints help for the unified config format.
func printConfigUsage() {
	fmt.Fprintf(os.Stderr, `PROBE Unified Config

The config file supports two formats:

1. Structured (Phase 4):
  {
    "name": "my-agent",
    "mgmt_addr": ":9700",
    "client": {
      "server": "ws://server:7700/ws",
      "token": "...",
      "name": "my-agent",
      "mode": "silent",
      "permissions": "full"
    },
    "relay": {
      "listen": ":7701",
      "upstream": "ws://server:7700/ws",
      "token": "relay-token",
      "agent_tokens": "token1,token2"
    },
    "server": {
      "addr": ":7700",
      "admin_password": "admin"
    }
  }

2. Legacy flat (backward compatible):
  {
    "server": "ws://server:7700",
    "token": "...",
    "name": "my-agent",
    "mode": "silent"
  }

Auto-detection (no subcommand):
  - client.server present → supervisor + connect
  - no client.server      → supervisor + serve (default)
  - relay section present → also start relay
`)
}