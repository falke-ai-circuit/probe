package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/falke-ai-circuit/probe/internal/relay"
)

// runRelay starts the PROBE relay bridge (downstream listener + upstream client).
func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	upstream := fs.String("upstream", "", "upstream server URL (e.g. wss://server:7700/ws)")
	token := fs.String("token", "", "relay token for upstream authentication")
	listen := fs.String("listen", ":7701", "downstream listen address")
	agentTokens := fs.String("agent-tokens", "", "comma-separated tokens agents must present (empty = accept all)")
	certFile := fs.String("cert-file", "", "TLS cert for downstream listener (optional)")
	keyFile := fs.String("key-file", "", "TLS key for downstream listener (optional)")
	maxAgents := fs.Int("max-agents", 100, "max concurrent downstream agents")
	maxPerIP := fs.Int("max-per-ip", 10, "max connections per IP")
	relayID := fs.String("relay-id", "", "relay identifier (auto-generated if empty)")
	showVersion := fs.Bool("version", false, "Print version and exit")
	fs.Parse(args)

	if *showVersion {
		fmt.Printf("PROBE Relay %s\n", appVersion)
		os.Exit(0)
	}

	if *upstream == "" {
		fmt.Fprintf(os.Stderr, "Error: --upstream is required\n\n")
		fs.Usage()
		os.Exit(1)
	}
	if *token == "" {
		fmt.Fprintf(os.Stderr, "Error: --token is required\n\n")
		fs.Usage()
		os.Exit(1)
	}

	cfg := relay.Config{
		UpstreamURL: *upstream,
		ListenAddr:  *listen,
		Token:       *token,
		AgentTokens: *agentTokens,
		CertFile:    *certFile,
		KeyFile:     *keyFile,
		MaxAgents:   *maxAgents,
		MaxPerIP:    *maxPerIP,
		RelayID:     *relayID,
	}

	r := relay.New(cfg)
	if err := r.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Relay failed: %v\n", err)
		os.Exit(1)
	}
}