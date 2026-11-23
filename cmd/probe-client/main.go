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
	"path/filepath"
	"io"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/version"
)


// logInit creates a dual writer (stderr + log file) and replaces the default log output.
// This ensures we have a persistent record of agent activity even when the binary
// runs in silent mode with no console (e.g., from a scheduled task or service).
func logInit() {
	execDir, err := os.Executable()
	if err != nil {
		return
	}
	logDir := filepath.Join(filepath.Dir(execDir), "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return
	}
	logFile := filepath.Join(logDir, "probe-client-"+time.Now().Format("20060102-150405")+".log")
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	// MultiWriter writes to both stderr and the log file
	multi := io.MultiWriter(os.Stderr, f)
	log.SetOutput(multi)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile)
}

// appVersion is what the user sees in the banner and usage text. It is derived
// from internal/version (the single source of truth) and the very same value is
// handed to agent.Config.Version below, so the printed version and the version
// reported to the server are guaranteed to agree. Bump version.Semver (or pass
// -X github.com/falke-ai-circuit/probe/internal/version.Semver=...) once.
var appVersion = version.Display()

// clientFlags holds the parsed command line for the probe-client entrypoint.
type clientFlags struct {
	configPath   string
	// configExplicit records whether --config was actually passed. The default
	// value "probe-client.json" is a filename, not a decision: resolution must
	// be able to tell "the operator named this file" from "nobody said".
	configExplicit bool
	canary       bool
	canaryOldPID int
}

// parseClientArgs parses the argv of the probe-client entrypoint. It is the
// receiving half of the update launch contract: internal/agent.CanaryArgs emits
// ["connect", "--config", p, "--canary", "--canary-old-pid", pid] and this
// function must accept exactly that. Unknown flags are an error (never silently
// ignored), so a drift between the launcher and this parser fails loudly in the
// anti-tautology test instead of producing a canary that prints usage and exits.
func parseClientArgs(argv []string) (clientFlags, error) {
	var out clientFlags
	fs := newClientFlagSet(&out)
	// Both entrypoints accept an optional "connect" subcommand so the updater
	// can launch either binary with the same argv.
	if len(argv) > 0 && argv[0] == "connect" {
		argv = argv[1:]
	}
	if err := fs.Parse(argv); err != nil {
		return out, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "config" {
			out.configExplicit = true
		}
	})
	if fs.NArg() > 0 {
		return out, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	return out, nil
}

// newClientFlagSet registers the flags the probe-client entrypoint accepts.
// Kept separate from parseClientArgs so tests can assert the registered flag set
// against the argv the updater actually launches (see main_test.go).
func newClientFlagSet(out *clientFlags) *flag.FlagSet {
	fs := flag.NewFlagSet("probe-client", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&out.configPath, "config", "probe-client.json", "Path to JSON config file")
	fs.BoolVar(&out.canary, "canary", false, "Canary mode: connect, prove healthy, then swap into canonical path and stop the old process")
	fs.IntVar(&out.canaryOldPID, "canary-old-pid", 0, "PID of the old process to stop after a successful swap")
	return fs
}

// configB64 is injected at build time via -ldflags "-X main.configB64=...".
// When non-empty, the agent decodes this base64-encoded JSON config instead
// of reading from the --config file. This enables the agent builder to
// embed config directly into the binary, producing a zero-config agent.
// When empty (default), the agent falls back to the JSON config file.
var configB64 = ""

// ConfigFile is the JSON structure for the config file.
type ConfigFile struct {
	Server      string `json:"server"`      // e.g. "100.64.0.1:7700"
	Token       string `json:"token"`       // auth token
	Name        string `json:"name"`        // display name for this agent
	Mode        string `json:"mode"`        // "silent" or "interactive"
	Listen      string `json:"listen"`      // inbound listen address (optional)
	MaxRetries  int    `json:"maxRetries"`  // 0 = infinite
	BackoffMin  string `json:"backoffMin"`  // e.g. "1s"
	BackoffMax  string `json:"backoffMax"`  // e.g. "60s"
	TokenFile   string `json:"tokenFile"`   // path to persist rotated token
	Cert        string `json:"cert"`        // CA cert for TLS verification
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

  server       string  WebSocket server URL (e.g. "host:7700" (websocket))
  token        string  Authentication token for the server
  name         string  Display name for this agent (default: "probe-client")
  mode         string  "silent" (daemon) or "interactive" (CLI prompt) (default: "silent")
  listen       string  Address to listen on for inbound connections (e.g. ":7700")
  maxRetries   int     Max reconnect attempts; 0 = infinite (default: 0)
  backoffMin   string  Min reconnect backoff duration (e.g. "1s") (default: "1s")
  backoffMax   string  Max reconnect backoff duration (e.g. "60s") (default: "60s")
  tokenFile    string  Path to persist rotated auth token (default: ".probe-token")
  cert         string  CA certificate file (PEM) for verifying server TLS
  clientCert   string  Client certificate file (PEM) for mTLS on outbound connection
  clientKey    string  Client key file (PEM) for mTLS on outbound connection
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
    "server": "your-server:7700",
    "token": "your-auth-token",
    "name": "my-computer",
    "mode": "interactive",
    "maxRetries": 0,
    "backoffMin": "1s",
    "backoffMax": "60s"
  }

`, appVersion)
}

// main starts the PROBE client. The startup logic lives in runClient so
// that future entry-point variants (e.g. service mode) can reuse it.
func main() {
	// P8-11: `--version` is an API for the updater, not a banner. It prints
	// exactly the bare semver on one line and exits 0, so the version can be
	// read from the artifact that was just written — before it is swapped in.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-version", "version":
			fmt.Println(version.Wire())
			return
		}
	}
	// P8-04: detached-job guardian. Every client build must answer this
	// subcommand — the field binary (probe-client) is spawned by itself as
	// `guardian --job-dir <dir>` and is the only executor of on_death.
	if len(os.Args) > 1 && os.Args[1] == "guardian" {
		runGuardian(os.Args[2:])
		return
	}
	// P8-07: the SSH helper is a subcommand of this binary, not a separate
	// executable and not a runtime-compiled password helper. The credential
	// arrives on stdin.
	if len(os.Args) > 1 && os.Args[1] == "ssh" {
		os.Exit(agent.RunSSH(os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	runClient()
}

// runClient is the shared startup logic. Returns when SIGINT/SIGTERM is
// received or the agent terminates.
func runClient() {
	logInit()

	// Crash recovery FIRST: if a previous update died between
	// rename(canonical -> .old) and rename(.next -> canonical), the canonical
	// path no longer exists but <canonical>.old does. Put the backup back before
	// touching anything else so the host is never left with an unrunnable path.
	if exe, err := os.Executable(); err == nil {
		canonical := strings.TrimSuffix(exe, ".next")
		if _, recErr := agent.RecoverCanonicalBinary(canonical); recErr != nil {
			log.Printf("[recovery] canonical binary restore failed: %v", recErr)
		}
	}

	opts, err := parseClientArgs(os.Args[1:])
	if err != nil {
		printUsage()
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Resolve the config file this process ACTUALLY uses, in the fixed order:
	// explicit --config → probe-client.json beside the exe → <exe>.json beside
	// the exe → the only .json beside the exe → embedded build config. The
	// resolved path is remembered (agent.Config.ConfigPath) so the updater can
	// hand it to the canary explicitly; without it an agent-named install
	// (probe-V6049A-EAS1.exe + probe-V6049A-EAS1.json) launches a canary that
	// falls back to probe-client.json and dies.
	exePath, exeErr := os.Executable()
	if exeErr != nil {
		exePath = ""
	}
	explicitConfig := ""
	if opts.configExplicit {
		explicitConfig = opts.configPath
	}
	configPath, configSource, resolveErr := agent.ResolveConfigPath(explicitConfig, exePath, configB64 != "")
	if resolveErr != nil {
		printUsage()
		fmt.Fprintf(os.Stderr, "Error: %v\n", resolveErr)
		os.Exit(1)
	}
	log.Printf("config: %s (%s)", configPath, configSource)

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
		cfgData, err := os.ReadFile(configPath)
		if err != nil {
			printUsage()
			fmt.Fprintf(os.Stderr, "Error: could not read config file: %s\n", configPath)
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
	// A canary connects under a distinct name so it does not displace the
	// still-running old agent until the swap commits. runCanaryCommit strips
	// this suffix before re-registering under the canonical name.
	if opts.canary {
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
		Version:         appVersion,
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
		ConfigPath:      configPath,
		EmbeddedConfig:  configB64 != "",
		CanaryMode:      opts.canary,
		CanaryOldPID:    opts.canaryOldPID,
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
	fmt.Fprintf(os.Stderr, "Config: %s\n", configPath)

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