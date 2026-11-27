package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/relay"
)

// runRelay starts the PROBE relay bridge (downstream listener + upstream client).
func runRelay(args []string) {
	fs := flag.NewFlagSet("relay", flag.ExitOnError)
	upstream := fs.String("upstream", "", "upstream server URL (e.g. wss://server:7700/ws)")
	upstreams := fs.String("upstreams", "", "ordered upstream candidates, comma-separated (overrides --upstream)")
	token := fs.String("token", "", "relay token for upstream authentication")
	listen := fs.String("listen", ":7701", "downstream listen address")
	agentTokens := fs.String("agent-tokens", "", "comma-separated tokens agents must present (required; empty refuses to start without --allow-any-downstream)")
	allowAny := fs.Bool("allow-any-downstream", false, "explicit opt-out: run with NO downstream agent token check (accept-all)")
	upstreamCA := fs.String("upstream-ca", "", "CA certificate PEM to verify the upstream server on wss:// candidates (P19-01: wss without CA fails unless PROBE_TLS_SKIP_VERIFY=true)")
	upstreamClientCert := fs.String("upstream-client-cert", "", "client certificate PEM for mTLS on the upstream hop")
	upstreamClientKey := fs.String("upstream-client-key", "", "client key PEM for mTLS on the upstream hop")
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

	candidates := []string{}
	for _, u := range strings.Split(*upstreams, ",") {
		if u = strings.TrimSpace(u); u != "" {
			candidates = append(candidates, u)
		}
	}
	if len(candidates) == 0 && *upstream != "" {
		candidates = []string{*upstream}
	}
	if len(candidates) == 0 {
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
		UpstreamURL:  candidates[0],
		UpstreamURLs: candidates,
		ListenAddr:   *listen,
		Token:        *token,
		AgentTokens:  *agentTokens,
		CertFile:     *certFile,
		KeyFile:      *keyFile,
		MaxAgents:    *maxAgents,
		MaxPerIP:     *maxPerIP,
		RelayID:      *relayID,

		AllowAnyDownstream: *allowAny,
		UpstreamCAFile:     *upstreamCA,
		UpstreamClientCert: *upstreamClientCert,
		UpstreamClientKey:  *upstreamClientKey,
	}

	// P19-01: refuse-to-start when the downstream listener would admit
	// everything (empty -agent-tokens without the explicit opt-out). The
	// failure is loud, on stderr, before any socket is bound.
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		fs.Usage()
		os.Exit(1)
	}

	r := relay.New(cfg)
	if err := r.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Relay failed: %v\n", err)
		os.Exit(1)
	}
}