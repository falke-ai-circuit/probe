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

const appVersion = "v1.8.5"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
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
	fs.Parse(args)

	mgr := modes.NewManager(*mgmtAddr)

	// Register a dynamic mode factory that creates modes on demand
	mgr.RegisterFactory("serve", func(cfg json.RawMessage) (modes.Mode, error) {
		var opts modes.ServerOptions
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &opts); err != nil {
				return nil, fmt.Errorf("invalid serve config: %w", err)
			}
		}
		// Apply defaults
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
		return modes.NewServerMode(opts), nil
	})

	mgr.RegisterFactory("connect", func(cfg json.RawMessage) (modes.Mode, error) {
		var c struct {
			Server      string `json:"server"`
			Token       string `json:"token"`
			Name        string `json:"name"`
			Mode        string `json:"mode"`
			Permissions string `json:"permissions"`
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
	fmt.Fprintf(os.Stderr, "\nWaiting for commands...\n")

	<-sigCh
	fmt.Fprintf(os.Stderr, "\nShutting down...\n")
	mgr.StopAll()
	mgr.StopManagementAPI()
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `PROBE %s — Platform for Remote Operations & Bridge Environment

Usage:
  probe <subcommand> [flags]

Subcommands:
  serve       Start as server (top of tree, listens for agents + operators)
  connect     Start as client/agent (connects to server or relay)
  relay       Start as relay bridge (listens downstream, connects upstream)
  supervisor  Start mode manager with management API for dynamic mode switching
  version     Print version
  help        Show this usage

Quick start:
  probe serve --addr :7700 --admin-password admin
  probe connect --config probe-client.json
  probe relay --upstream wss://server:7700/ws --token secret
  probe supervisor --mgmt-addr :9700

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