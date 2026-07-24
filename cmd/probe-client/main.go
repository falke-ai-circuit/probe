package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	_ "github.com/falke-ai-circuit/probe/internal/server"
)

const appVersion = "v1.5.0"

// configPath is the JSON config file path (flag: --config).
// All connection parameters are read from this file instead of
// individual CLI flags, which keeps the command line clean and
// unremarkable.
var configPath = flag.String("config", "probe-client.json", "Path to JSON config file")

// configB64 is injected at build time via -ldflags "-X main.configB64=...".
// When non-empty, the agent decodes this base64-encoded JSON config instead
// of reading from the --config file. This enables the agent builder to
// embed config directly into the binary, producing a zero-config agent.
// When empty (default), the agent falls back to the JSON config file.
var configB64 = ""

// ConfigFile is the JSON structure for the config file.
type ConfigFile struct {
	Server      string `json:"server"`      // e.g. "ws://100.64.0.1:7700"
	Token       string `json:"token"`       // auth token
	Name        string `json:"name"`        // display name for this agent
	Mode        string `json:"mode"`        // "silent" or "interactive"
	Listen      string `json:"listen"`      // inbound listen address (optional)
	MaxRetries  int    `json:"maxRetries"`  // 0 = infinite
	BackoffMin  string `json:"backoffMin"`  // e.g. "1s"
	BackoffMax  string `json:"backoffMax"`  // e.g. "60s"
	TokenFile   string `json:"tokenFile"`   // path to persist rotated token
	Cert        string `json:"cert"`        // CA cert for wss:// verification
	ClientCert  string `json:"clientCert"`  // client cert for mTLS
	ClientKey   string `json:"clientKey"`   // client key for mTLS
	CertFile    string `json:"certFile"`    // TLS cert for inbound server
	KeyFile     string `json:"keyFile"`     // TLS key for inbound server
	Permissions string `json:"permissions"` // "read-only", "standard", "full" (default: "full")
	SandboxDir  string `json:"sandbox_dir"`  // restrict fs ops to this directory (empty = no restriction)
	Capabilities []string `json:"capabilities"` // capabilities to advertise to server (empty = all, backward compat)
}

// printUsage writes a clean help/usage banner to stderr.
func printUsage() {
	fmt.Fprintf(os.Stderr, `PROBE Client %s
A remote assistant tool for the Hermes AI ecosystem

Usage:
  ProbeClient.exe [--config probe-client.json]

Configuration:
  All settings are read from a JSON config file (default: probe-client.json).
  Use --config <path> to specify an alternate file.

Config file fields:

  server       string  WebSocket server URL (e.g. "ws://host:7700" or "wss://host:7700")
  token        string  Authentication token for the server
  name         string  Display name for this agent (default: "probe-client")
  mode         string  "silent" (daemon) or "interactive" (CLI prompt) (default: "silent")
  listen       string  Address to listen on for inbound connections (e.g. ":7700")
  maxRetries   int     Max reconnect attempts; 0 = infinite (default: 0)
  backoffMin   string  Min reconnect backoff duration (e.g. "1s") (default: "1s")
  backoffMax   string  Max reconnect backoff duration (e.g. "60s") (default: "60s")
  tokenFile    string  Path to persist rotated auth token (default: ".probe-token")
  cert         string  CA certificate file (PEM) for verifying server TLS on wss://
  clientCert   string  Client certificate file (PEM) for mTLS on outbound wss://
  clientKey    string  Client key file (PEM) for mTLS on outbound wss://
  certFile     string  TLS certificate file (PEM) for inbound server mode
  keyFile      string  TLS key file (PEM) for inbound server mode
  permissions  string  Permission tier: "sandboxed", "standard", "read-only", or "full" (default: "full")
                         sandboxed: fs ops restricted to startup dir, exec (non-destructive),
                                    proc_start + proc_kill (only agent-started PIDs), no file delete,
                                    no capture/input/tunnel/debug/agent-update
                         standard: like sandboxed but sandbox_dir from config (if set),
                                   no auto-sandbox to startup dir
                         read-only: read files + safe exec only (no writes, no proc control)
                         full: no restrictions
  sandbox_dir  string  Restrict all filesystem operations to this directory (empty = no restriction)
                         In "sandboxed" mode, if empty, auto-uses the agent's startup directory

Example config (probe-client.json):

  {
    "server": "ws://your-server:7700",
    "token": "your-auth-token",
    "name": "my-computer",
    "mode": "interactive",
    "maxRetries": 0,
    "backoffMin": "1s",
    "backoffMax": "60s"
  }

`, appVersion)
}

func main() {
	flag.Usage = printUsage
	flag.Parse()

	// Read config: prefer ldflags-injected base64 config, fall back to JSON file.
	var fcfg ConfigFile
	if configB64 != "" {
		// Config injected at build time via -ldflags. Decode and use directly.
		cfgData, err := base64.StdEncoding.DecodeString(configB64)
		if err != nil {
			log.Fatalf("Invalid embedded config (base64 decode): %v", err)
		}
		if err := json.Unmarshal(cfgData, &fcfg); err != nil {
			log.Fatalf("Invalid embedded config (JSON parse): %v", err)
		}
	} else {
		// Fall back to JSON config file (backward compat).
		cfgData, err := os.ReadFile(*configPath)
		if err != nil {
			printUsage()
			fmt.Fprintf(os.Stderr, "Error: could not read config file: %s\n", *configPath)
			os.Exit(1)
		}
		if err := json.Unmarshal(cfgData, &fcfg); err != nil {
			log.Fatalf("Invalid config file: %v", err)
		}
	}

	if fcfg.Server == "" && fcfg.Listen == "" {
		printUsage()
		fmt.Fprintf(os.Stderr, "Error: config file must contain a 'server' or 'listen' field.\n")
		os.Exit(1)
	}

	mode := fcfg.Mode
	if mode == "" {
		mode = "silent"
	}
	if mode != "silent" && mode != "interactive" {
		log.Fatalf("Invalid mode in config: %s (must be 'silent' or 'interactive')", mode)
	}

	// Strip surrounding quotes from token (shell expansion can embed them)
	tokenVal := strings.Trim(fcfg.Token, "\"'")

	name := fcfg.Name
	if name == "" {
		name = "probe-client"
	}

	// Parse backoff durations
	backoffMin, err := time.ParseDuration(fcfg.BackoffMin)
	if err != nil || backoffMin <= 0 {
		backoffMin = 1 * time.Second
	}
	backoffMax, err := time.ParseDuration(fcfg.BackoffMax)
	if err != nil || backoffMax <= 0 {
		backoffMax = 60 * time.Second
	}

	tokenFile := fcfg.TokenFile
	if tokenFile == "" {
		tokenFile = ".probe-token"
	}

	// If no token in config, try to resume with a previously persisted token
	if tokenVal == "" && tokenFile != "" {
		if persisted, err := agent.LoadPersistedToken(tokenFile); err != nil {
			log.Printf("Warning: could not read token file %s: %v", tokenFile, err)
		} else if persisted != "" {
			tokenVal = persisted
			log.Printf("Resumed session from %s", tokenFile)
		}
	}

	cfg := agent.Config{
		Mode:           "outbound",
		URL:            fcfg.Server,
		Addr:           fcfg.Listen,
		Token:          tokenVal,
		CertPath:       fcfg.Cert,
		ClientCertFile: fcfg.ClientCert,
		ClientKeyFile:  fcfg.ClientKey,
		CertFile:       fcfg.CertFile,
		KeyFile:        fcfg.KeyFile,
		Name:           name,
		MaxRetries:     fcfg.MaxRetries,
		BackoffMin:     backoffMin,
		BackoffMax:     backoffMax,
		TokenFile:      tokenFile,
		Permissions:     fcfg.Permissions,
		SandboxDir:      fcfg.SandboxDir,
		Capabilities:    fcfg.Capabilities,
	}

	// Ensure the WebSocket URL includes the /ws path the server expects
	if cfg.URL != "" && !strings.Contains(cfg.URL, "/ws") {
		cfg.URL = strings.TrimRight(cfg.URL, "/") + "/ws"
	}

	if fcfg.Listen != "" {
		cfg.Mode = "inbound"
	}

	// Startup logging
	fmt.Fprintf(os.Stderr, "PROBE Client %s\n", appVersion)
	fmt.Fprintf(os.Stderr, "Config: %s\n", *configPath)

	ag := agent.New(cfg)

	if cfg.Mode == "inbound" {
		fmt.Fprintf(os.Stderr, "Listening on %s as '%s' (mode: inbound)\n", cfg.Addr, name)
	} else {
		fmt.Fprintf(os.Stderr, "Connecting to %s as '%s' (mode: %s)\n", cfg.URL, name, mode)
	}

	// Handle SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start agent in background
	go func() {
		if err := ag.Run(); err != nil {
			log.Fatalf("Connection error: %v", err)
		}
	}()

	// Interactive mode: read stdin and send prompts to server
	if mode == "interactive" {
		go runInteractive(ag)
	}

	<-sigCh
	log.Println("PROBE Client shutting down...")
}

func runInteractive(ag *agent.Agent) {
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Fprintf(os.Stderr, "Interactive mode. Type commands (or Ctrl+D to quit):\n")
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		ag.SendPrompt(line)
	}
	if err := scanner.Err(); err != nil {
		log.Printf("stdin error: %v", err)
	}
}