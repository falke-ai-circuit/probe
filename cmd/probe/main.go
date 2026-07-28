package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/modes"
	"github.com/falke-ai-circuit/probe/internal/relay"
)

const appVersion = "v1.9.4"

func main() {
	// No arguments → default supervisor mode with auto-detection from config
	if len(os.Args) < 2 {
		runDefaultSupervisor()
		return
	}

	sub := os.Args[1]
	args := os.Args[2:]

	// Backward compatibility: if the first arg is a flag (starts with -),
	// treat it as a legacy connect invocation (old update code passes
	// "-config path" without the "connect" subcommand).
	if strings.HasPrefix(sub, "-") && sub != "--version" && sub != "-version" && sub != "--help" && sub != "-h" {
		sub = "connect"
		args = os.Args[1:]
	}

	switch sub {
	case "serve":
		runServe(args)
	case "connect":
		runConnect(args)
	case "relay":
		runRelay(args)
	case "supervisor":
		runSupervisor(args)
	case "--version", "-version", "version":
		fmt.Printf("PROBE %s\n", appVersion)
	case "--help", "-h", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown subcommand: %s\n\n", sub)
		printUsage()
		os.Exit(1)
	}
}

// runSupervisor starts the mode manager with the management API.
// All modes are available but not started. Use the management API
// to dynamically start/stop modes at runtime.
//
// The management API accepts mode-specific configuration in the
// POST /api/mgmt/start body, so modes can be started with different
// configs without restarting the binary.
func runSupervisor(args []string) {
	fs := flag.NewFlagSet("supervisor", flag.ExitOnError)
	mgmtAddr := fs.String("mgmt-addr", ":9700", "management API listen address")
	configPath := fs.String("config", "", "config file for auto-start modes (optional)")
	fs.Parse(args)

	mgr := modes.NewManager(*mgmtAddr)
	registerFactories(mgr)

	// If config provided, auto-start modes from it
	if *configPath != "" {
		uc, err := LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", *configPath, err)
			os.Exit(1)
		}
		startServe, startConnect, startRelay := uc.AutoDetect()

		if startServe && uc.Server != nil {
			cfgJSON, _ := json.Marshal(*uc.Server)
			if err := mgr.StartMode("serve", cfgJSON); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start serve: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "  ✓ serve mode started on %s\n", uc.Server.Addr)
			}
		} else if startServe {
			cfgJSON, _ := json.Marshal(map[string]interface{}{
				"addr":          "localhost:7700",
				"rate_limit":    10.0,
				"rate_burst":    20,
				"max_concurrent": 5,
				"registry_path": "/tmp/probe-registry.json",
				"allowed_cidr":  "0.0.0.0/0",
			})
			if err := mgr.StartMode("serve", cfgJSON); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start serve: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "  ✓ serve mode started on localhost:7700\n")
			}
		}

		if startConnect && uc.Client != nil {
			cfgJSON, _ := json.Marshal(struct {
				Server      string             `json:"server"`
				Token       string             `json:"token"`
				Name        string             `json:"name"`
				Mode        string             `json:"mode"`
				Permissions string             `json:"permissions"`
				ConfigPath  string             `json:"config_path"`
				Relays      []RelayEntryConfig `json:"relays,omitempty"`
			}{
				Server:      uc.Client.Server,
				Token:       uc.Client.Token,
				Name:        uc.Client.Name,
				Mode:        uc.Client.Mode,
				Permissions: uc.Client.Permissions,
				ConfigPath:  *configPath,
				Relays:      uc.Client.Relays,
			})
			if err := mgr.StartMode("connect", cfgJSON); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start connect: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "  ✓ connect mode started → %s as '%s'\n", uc.Client.Server, uc.Client.Name)
			}
		}

		if startRelay && uc.Relay != nil {
			relayCfg := uc.ToRelayConfig()
			cfgJSON, _ := json.Marshal(struct {
				Upstream    string `json:"upstream"`
				Token       string `json:"token"`
				Listen      string `json:"listen"`
				AgentTokens string `json:"agent_tokens"`
			}{
				Upstream:    relayCfg.UpstreamURL,
				Token:       relayCfg.Token,
				Listen:      relayCfg.ListenAddr,
				AgentTokens: relayCfg.AgentTokens,
			})
			if err := mgr.StartMode("relay", cfgJSON); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to start relay: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "  ✓ relay mode started on %s → %s\n", relayCfg.ListenAddr, relayCfg.UpstreamURL)
			}
		}
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := mgr.StartManagementAPI(); err != nil {
			fmt.Fprintf(os.Stderr, "Management API error: %v\n", err)
			os.Exit(1)
		}
	}()

	fmt.Fprintf(os.Stderr, "PROBE Supervisor %s\n", appVersion)
	fmt.Fprintf(os.Stderr, "Management API on %s\n", *mgmtAddr)
	fmt.Fprintf(os.Stderr, "Endpoints:\n")
	fmt.Fprintf(os.Stderr, "  GET  /api/mgmt/status  — list mode statuses\n")
	fmt.Fprintf(os.Stderr, "  POST /api/mgmt/start    — {\"mode\":\"serve\", \"config\":{...}}\n")
	fmt.Fprintf(os.Stderr, "  POST /api/mgmt/stop     — {\"mode\":\"serve\"}\n")
	fmt.Fprintf(os.Stderr, "\nWaiting for commands... (or Ctrl+C to stop)\n")

	<-sigCh
	fmt.Fprintf(os.Stderr, "\nShutting down...\n")
	mgr.StopAll()
	mgr.StopManagementAPI()
}

// runDefaultSupervisor starts PROBE in supervisor mode with auto-detection.
// It reads a config file (default: probe.json, or PROBE_CONFIG env var) and
// auto-starts modes based on the config contents:
//   - client.server present → supervisor + connect
//   - no client.server      → supervisor + serve (default)
//   - relay section present → also start relay
func runDefaultSupervisor() {
	configPath := os.Getenv("PROBE_CONFIG")
	if configPath == "" {
		// Try common config file names
		for _, name := range []string{"probe.json", "probe-client.json"} {
			if _, err := os.Stat(name); err == nil {
				configPath = name
				break
			}
		}
	}

	// No config file found → start as server with defaults
	if configPath == "" {
		fmt.Fprintf(os.Stderr, "PROBE %s — no config file found, starting as server (default)\n", appVersion)
		runSupervisorWithAutoStart(nil, "")
		return
	}

	uc, err := LoadConfig(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config %s: %v\n", configPath, err)
		os.Exit(1)
	}

	fmt.Fprintf(os.Stderr, "PROBE %s — config: %s\n", appVersion, configPath)
	runSupervisorWithAutoStart(uc, configPath)
}

// runSupervisorWithAutoStart starts the mode manager, auto-starts modes based
// on the unified config, then waits for signals.
func runSupervisorWithAutoStart(uc *UnifiedConfig, configPath string) {
	mgmtAddr := ":9700"
	if uc != nil && uc.MgmtAddr != "" {
		mgmtAddr = uc.MgmtAddr
	}

	mgr := modes.NewManager(mgmtAddr)
	registerFactories(mgr)

	// Auto-detect which modes to start
	startServe, startConnect, startRelay := false, false, false
	if uc != nil {
		startServe, startConnect, startRelay = uc.AutoDetect()
	}

	// Start serve mode
	if startServe && uc != nil && uc.Server != nil {
		srvCfg := *uc.Server
		cfgJSON, _ := json.Marshal(srvCfg)
		if err := mgr.StartMode("serve", cfgJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start serve: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ serve mode started on %s\n", srvCfg.Addr)
		}
	} else if startServe {
		// Default server config
		cfgJSON, _ := json.Marshal(map[string]interface{}{
			"addr":          "localhost:7700",
			"rate_limit":    10.0,
			"rate_burst":    20,
			"max_concurrent": 5,
			"registry_path": "/tmp/probe-registry.json",
			"allowed_cidr":  "0.0.0.0/0",
		})
		if err := mgr.StartMode("serve", cfgJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start serve: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ serve mode started on localhost:7700\n")
		}
	}

	// Start connect mode
	if startConnect && uc != nil && uc.Client != nil {
		agentCfg := uc.ToAgentConfig()
		// Wrap agent.Config in JSON for the factory
		cfgJSON, _ := json.Marshal(struct {
			Server      string             `json:"server"`
			Token       string             `json:"token"`
			Name        string             `json:"name"`
			Mode        string             `json:"mode"`
			Permissions string             `json:"permissions"`
			ConfigPath  string             `json:"config_path"`
			Relays      []RelayEntryConfig `json:"relays,omitempty"`
		}{
			Server:      uc.Client.Server,
			Token:       uc.Client.Token,
			Name:        uc.Client.Name,
			Mode:        uc.Client.Mode,
			Permissions: uc.Client.Permissions,
			ConfigPath:  configPath,
			Relays:      uc.Client.Relays,
		})
		if err := mgr.StartMode("connect", cfgJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start connect: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ connect mode started → %s as '%s'\n", agentCfg.URL, agentCfg.Name)
		}
	}

	// Start relay mode
	if startRelay && uc != nil && uc.Relay != nil {
		relayCfg := uc.ToRelayConfig()
		cfgJSON, _ := json.Marshal(struct {
			Upstream    string `json:"upstream"`
			Token       string `json:"token"`
			Listen      string `json:"listen"`
			AgentTokens string `json:"agent_tokens"`
		}{
			Upstream:    relayCfg.UpstreamURL,
			Token:       relayCfg.Token,
			Listen:      relayCfg.ListenAddr,
			AgentTokens: relayCfg.AgentTokens,
		})
		if err := mgr.StartMode("relay", cfgJSON); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to start relay: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  ✓ relay mode started on %s → %s\n", relayCfg.ListenAddr, relayCfg.UpstreamURL)
		}
	}

	// Handle signals for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := mgr.StartManagementAPI(); err != nil {
			fmt.Fprintf(os.Stderr, "Management API error: %v\n", err)
			os.Exit(1)
		}
	}()

	fmt.Fprintf(os.Stderr, "PROBE Supervisor %s\n", appVersion)
	fmt.Fprintf(os.Stderr, "Management API on %s\n", mgmtAddr)
	fmt.Fprintf(os.Stderr, "Endpoints:\n")
	fmt.Fprintf(os.Stderr, "  GET  /api/mgmt/status  — list mode statuses\n")
	fmt.Fprintf(os.Stderr, "  POST /api/mgmt/start    — {\"mode\":\"serve\", \"config\":{...}}\n")
	fmt.Fprintf(os.Stderr, "  POST /api/mgmt/stop     — {\"mode\":\"serve\"}\n")
	fmt.Fprintf(os.Stderr, "\nRunning. Press Ctrl+C to stop.\n")

	<-sigCh
	fmt.Fprintf(os.Stderr, "\nShutting down...\n")
	mgr.StopAll()
	mgr.StopManagementAPI()
}

// registerFactories registers all mode factories with the mode manager.
func registerFactories(mgr *modes.Manager) {
	mgr.RegisterFactory("serve", func(cfg json.RawMessage) (modes.Mode, error) {
		var opts modes.ServerOptions
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &opts); err != nil {
				return nil, fmt.Errorf("invalid serve config: %w", err)
			}
		}
		if opts.Addr == "" {
			opts.Addr = "localhost:7700"
		}
		if opts.RateLimit == 0 {
			opts.RateLimit = 10.0
		}
		if opts.RateBurst == 0 {
			opts.RateBurst = 20
		}
		if opts.MaxConcurrent == 0 {
			opts.MaxConcurrent = 5
		}
		if opts.RegistryPath == "" {
			opts.RegistryPath = "/tmp/probe-registry.json"
		}
		if opts.AllowedCIDR == "" {
			opts.AllowedCIDR = "0.0.0.0/0"
		}
		opts.Version = appVersion
		return modes.NewServerMode(opts), nil
	})

	mgr.RegisterFactory("connect", func(cfg json.RawMessage) (modes.Mode, error) {
		var c struct {
			Server      string             `json:"server"`
			Token       string             `json:"token"`
			Name        string             `json:"name"`
			Mode        string             `json:"mode"`
			Permissions string             `json:"permissions"`
			ConfigPath  string             `json:"config_path"`
			Relays      []RelayEntryConfig `json:"relays,omitempty"`
		}
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, fmt.Errorf("invalid connect config: %w", err)
			}
		}
		mode := c.Mode
		if mode == "" {
			mode = "silent"
		}
		name := c.Name
		if name == "" {
			name = "probe-client"
		}
		perms := c.Permissions
		if perms == "" {
			perms = "full"
		}
		url := c.Server
		if url != "" && !strings.Contains(url, "/ws") {
			url = strings.TrimRight(url, "/") + "/ws"
		}
		ac := agent.Config{
			Mode:        "outbound",
			URL:         url,
			Token:       strings.Trim(c.Token, "\"'"),
			Name:        name,
			Permissions: perms,
			ConfigPath:  c.ConfigPath,
		}
		// Convert relay endpoints
		for _, r := range c.Relays {
			relayURL := r.URL
			if relayURL != "" && !strings.Contains(relayURL, "/ws") {
				relayURL = strings.TrimRight(relayURL, "/") + "/ws"
			}
			ac.Relays = append(ac.Relays, agent.RelayEndpoint{
				URL:   relayURL,
				Token: strings.Trim(r.Token, "\"'"),
			})
		}
		return modes.NewConnectMode(ac), nil
	})

	mgr.RegisterFactory("relay", func(cfg json.RawMessage) (modes.Mode, error) {
		var c struct {
			Upstream    string `json:"upstream"`
			Token       string `json:"token"`
			Listen      string `json:"listen"`
			AgentTokens string `json:"agent_tokens"`
		}
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &c); err != nil {
				return nil, fmt.Errorf("invalid relay config: %w", err)
			}
		}
		if c.Upstream == "" {
			return nil, fmt.Errorf("upstream is required")
		}
		if c.Token == "" {
			return nil, fmt.Errorf("token is required")
		}
		listen := c.Listen
		if listen == "" {
			listen = ":7701"
		}
		rc := relay.Config{
			UpstreamURL: c.Upstream,
			ListenAddr:  listen,
			Token:       c.Token,
			AgentTokens: c.AgentTokens,
			MaxAgents:   100,
			MaxPerIP:    10,
		}
		return modes.NewRelayMode(rc), nil
	})
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `PROBE %s — Platform for Remote Operations & Bridge Environment

Usage:
  probe [subcommand] [flags]

Subcommands:
  (none)      Auto-detect from config: supervisor + serve or connect (+ relay if configured)
  serve       Start as server (top of tree, listens for agents + operators)
  connect     Start as client/agent (connects to server or relay)
  relay       Start as relay bridge (listens downstream, connects upstream)
  supervisor  Start mode manager with management API for dynamic mode switching
  version     Print version
  help        Show this usage

Quick start:
  probe                          # auto-detect from probe.json or probe-client.json
  probe serve --addr :7700 --admin-password admin
  probe connect --config probe-client.json
  probe relay --upstream wss://server:7700/ws --token secret
  probe supervisor --mgmt-addr :9700 --config probe.json

Config file (probe.json):
  {
    "client": { "server": "ws://host:7700/ws", "token": "...", "name": "my-pc" },
    "relay":  { "listen": ":7701", "upstream": "ws://host:7700/ws" }
  }

Management API (supervisor mode):
  GET  /api/mgmt/status  — list mode statuses
  POST /api/mgmt/start   — start a mode {"mode":"serve", "config":{...}}
  POST /api/mgmt/stop    — stop a mode {"mode":"serve"}

`, appVersion)
}

// newFlagSet creates a flag set that prints to stderr and exits on error.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	fs.SetOutput(os.Stderr)
	return fs
}