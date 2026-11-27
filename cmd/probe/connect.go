package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/version"
	_ "github.com/falke-ai-circuit/probe/internal/server"
)

// configB64 is injected at build time via -ldflags "-X main.configB64=...".
// When non-empty, the agent decodes this base64-encoded JSON config instead
// of reading from the --config file. This enables the agent builder to
// embed config directly into the binary, producing a zero-config agent.
var configB64 = ""

// ConfigFile is the JSON structure for the config file.
type ConfigFile struct {
	Server       string   `json:"server"`
	Token        string   `json:"token"`
	Name         string   `json:"name"`
	Mode         string   `json:"mode"`
	Listen       string   `json:"listen"`
	MaxRetries   int      `json:"maxRetries"`
	BackoffMin   string   `json:"backoffMin"`
	BackoffMax   string   `json:"backoffMax"`
	TokenFile    string   `json:"tokenFile"`
	Cert         string   `json:"cert"`
	ClientCert   string   `json:"clientCert"`
	ClientKey    string   `json:"clientKey"`
	CertFile     string   `json:"certFile"`
	KeyFile      string   `json:"keyFile"`
	Permissions  string   `json:"permissions"`
	SandboxDir   string   `json:"sandbox_dir"`
	Capabilities []string `json:"capabilities"`
	// P19-01: pointer so an explicit "e2e_enabled": false is honoured while
	// an absent field resolves to default-on via resolveE2EEnabled.
	E2EEnabled   *bool    `json:"e2e_enabled,omitempty"`
}

// runConnect starts the PROBE client/agent (leaf node).
// All flags are the same as the old cmd/probe-client.
func runConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	configPath := fs.String("config", "probe-client.json", "Path to JSON config file")
	showVersion := fs.Bool("version", false, "Print version and exit")
	canaryMode := fs.Bool("canary", false, "Canary mode: connect, prove healthy, then swap into canonical path and stop the old process")
	canaryOldPID := fs.Int("canary-old-pid", 0, "PID of the old process to stop after a successful swap")
	statusFile := fs.String("status-file", "", "path for the run-state record read by --service-status (default: $PROBE_STATUS_FILE, $PROBE_STATE_DIR, or the platform state dir)")
	// P23-14 (a): the agent's E2E posture must be expressible at launch, not
	// only through the config file. Default ON per P19-01 §11.4, matching the
	// server posture; an explicit --e2e=false is the opt-out.
	e2eFlag := fs.Bool("e2e", true, "End-to-end encrypt protocol payloads (default: on; --e2e=false opts out)")
	fs.Parse(args)

	// configExplicit records whether --config was passed at all: the default
	// "probe-client.json" is a filename, not an operator decision.
	configExplicit := false
	// P23-14 (a): --e2e was passed explicitly, so it overrides the config's
	// e2e_enabled rather than deferring to it.
	e2eExplicit := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			configExplicit = true
		}
		if f.Name == "e2e" {
			e2eExplicit = true
		}
	})

	if *showVersion {
		fmt.Printf("PROBE Client %s\n", appVersion)
		os.Exit(0)
	}

	// Resolve the config file this process ACTUALLY uses (explicit --config →
	// probe-client.json → <exe>.json → the only .json → embedded) and remember
	// it, so a canary launched by the updater gets an explicit --config instead
	// of a default filename an agent-named install does not have.
	exePath, exeErr := os.Executable()
	if exeErr != nil {
		exePath = ""
	}
	explicitConfig := ""
	if configExplicit {
		explicitConfig = *configPath
	}
	resolvedConfig, configSource, resolveErr := agent.ResolveConfigPath(explicitConfig, exePath, configB64 != "")
	if resolveErr != nil {
		printConnectUsage()
		fmt.Fprintf(os.Stderr, "Error: %v\n", resolveErr)
		os.Exit(1)
	}
	*configPath = resolvedConfig
	log.Printf("config: %s (%s)", resolvedConfig, configSource)

	// Read config: prefer ldflags-injected base64 config, fall back to JSON file.
	var fcfg ConfigFile
	if configB64 != "" {
		cfgData, err := base64.StdEncoding.DecodeString(configB64)
		if err != nil {
			log.Fatalf("Invalid embedded config (base64 decode): %v", err)
		}
		if err := json.Unmarshal(cfgData, &fcfg); err != nil {
			log.Fatalf("Invalid embedded config (JSON parse): %v", err)
		}
	} else {
		cfgData, err := os.ReadFile(*configPath)
		if err != nil {
			printConnectUsage()
			fmt.Fprintf(os.Stderr, "Error: could not read config file: %s\n", *configPath)
			os.Exit(1)
		}
		if err := json.Unmarshal(cfgData, &fcfg); err != nil {
			log.Fatalf("Invalid config file: %v", err)
		}
	}

	if fcfg.Server == "" && fcfg.Listen == "" {
		printConnectUsage()
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
	// A canary connects under a distinct name so it does not displace the
	// still-running old agent until the swap succeeds.
	if *canaryMode {
		name = name + "-canary"
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
		Version:         version.Display(),
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
		Permissions:    fcfg.Permissions,
		SandboxDir:     fcfg.SandboxDir,
		Capabilities:   fcfg.Capabilities,
		// P19-01 §11.4: E2E defaults ON for every agent start; the config's
		// e2e_enabled overrides explicitly (pointer, absent = default on).
		// P23-14 (a): --e2e, when passed, overrides the config value; absent
		// both, the default is on.
		E2EEnabled:      connectE2EPosture(*e2eFlag, e2eExplicit, fcfg.E2EEnabled),
		ConfigPath:     *configPath,
		EmbeddedConfig: configB64 != "",
		CanaryMode:     *canaryMode,
		CanaryOldPID:   *canaryOldPID,
		StatusPath:     *statusFile,
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

	// The banner names what this process registers as, so the name comes from
	// the agent AFTER New(): New resolves a canary's identity from the
	// installation's config file (P15-07), and the launch-time `name` variable
	// — derived from the artifact's embedded build config — can carry a
	// different (build) name that would make the canary log contradict itself
	// (P15-09). Registration is unaffected; this print must tell the same
	// truth about it.
	printConnectBanner(os.Stderr, ag.Name(), cfg, mode)

	// Handle SIGINT/SIGTERM
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start agent in background. agentDone closes after Run returns, which
	// includes the deferred cleanup that retires the run-state record — so a
	// SIGTERM shutdown must wait for it (below) instead of exiting underneath it
	// and leaving a stale "running" record behind for every status read.
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
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
	ag.Stop()
	select {
	case <-agentDone:
	case <-time.After(5 * time.Second):
		log.Println("agent did not stop within 5s; exiting (the run-state record will read stale, which the status command reports as not_running)")
	}
}

func printConnectBanner(w io.Writer, identity string, cfg agent.Config, mode string) {
	// P15-09: the banner names what this process registers as, so identity is
	// the agent's post-New() name — for a canary, the config-resolved identity
	// — never the artifact-derived launch name.
	if cfg.Mode == "inbound" {
		fmt.Fprintf(w, "Listening on %s as '%s' (mode: inbound)\n", cfg.Addr, identity)
		return
	}
	fmt.Fprintf(w, "Connecting to %s as '%s' (mode: %s)\n", cfg.URL, identity, mode)
}

func printConnectUsage() {
	fmt.Fprintf(os.Stderr, `PROBE Client %s
A remote assistant tool for the Hermes AI ecosystem

Usage:
  probe connect [--config probe-client.json]

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
  sandbox_dir  string  Restrict all filesystem operations to this directory (empty = no restriction)
  capabilities array   Capabilities to advertise (empty = all, backward compat)
  relays       array   Relay endpoints for failover: [{"url":"wss://relay:7701/ws","token":"..."}]

Flags:
  --config <path>  Path to JSON config file (default: probe-client.json)
  --version        Print version and exit

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
// connectE2EPosture resolves the agent's E2E posture for runConnect (P23-14 a).
// Precedence: an explicit --e2e flag wins over the config file's e2e_enabled;
// with the flag absent the config pointer decides (absent = default true per
// P19-01 §11.4). Returns the effective posture.
func connectE2EPosture(flagOn bool, flagExplicit bool, cfgVal *bool) bool {
	if flagExplicit {
		return flagOn
	}
	return resolveE2EEnabled(true, cfgVal)
}
