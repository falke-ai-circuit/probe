package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/server"
)

// runServe starts the PROBE server (top of tree).
// All flags are the same as the old cmd/probe-server.
func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "", "listen address (default: PROBE_ADDR env or localhost:7700)")
	token := fs.String("token", "", "auth token (default: PROBE_TOKEN env)")
	registryPath := fs.String("registry", "", "registry file path (default: PROBE_REGISTRY env or /tmp/probe-registry.json)")
	rateLimit := fs.Float64("rate-limit", 10.0, "LLM proxy rate limit: requests per second per agent (default 10)")
	rateBurst := fs.Int("rate-burst", 20, "LLM proxy burst size: max tokens accumulated per agent (default 20)")
	maxConcurrent := fs.Int("max-concurrent", 5, "LLM proxy max concurrent in-flight requests across all agents (default 5)")
	tokenTTL := fs.Duration("token-ttl", 24*time.Hour, "token rotation interval; the server proactively rotates each agent's token before it expires. 0 disables rotation.")
	certFile := fs.String("cert-file", "", "TLS certificate file (PEM) for the server; enables TLS when provided with --key-file")
	keyFile := fs.String("key-file", "", "TLS key file (PEM) for the server; enables TLS when provided with --cert-file")
	clientCA := fs.String("client-ca", "", "Client CA certificate file (PEM) for TLS mutual authentication; requires --cert-file/--key-file")
	extraTokens := fs.String("extra-tokens", "", "Comma-separated additional auth tokens (for safe rollover: new server accepts old agent's token)")
	requireAPIAuth := fs.Bool("require-api-auth", false, "Require bearer-token auth on HTTP API endpoints (/api/agents, /api/agent/*, /download/*). When false (default), missing auth is logged as a warning but allowed through.")
	proxyFlags := fs.String("proxy", "", "Reverse proxy: path=target (repeatable via comma). Example: /logreport=http://localhost:8642. When empty, defaults to /logreport=http://localhost:8642 for backward compatibility.")
	enrollmentPath := fs.String("enrollment-db", "", "enrollment tokens file path (default: PROBE_ENROLLMENT_DB env or /tmp/probe-enrollment.json)")
	caDir := fs.String("ca-dir", "", "directory for CA cert/key storage (default: PROBE_CA_DIR env or /tmp/probe-ca)")
	builderPath := fs.String("builder-db", "", "agent builder records file path (default: PROBE_BUILDER_DB env or /tmp/probe-builds.json)")
	builderOutputDir := fs.String("builder-output-dir", "", "directory for built agent binaries (default: PROBE_BUILDER_OUTPUT_DIR env or /tmp/probe-builds)")
	profilesPath := fs.String("profiles-db", "", "build profiles file path (default: PROBE_PROFILES_DB env or /tmp/probe-profiles.json)")
	allowedCIDR := fs.String("allowed-cidr", "100.64.0.0/10", "CIDR range allowed for WebUI/API HTTP routes (default: Tailscale 100.64.0.0/10). /ws is always open from any IP. Set to 0.0.0.0/0 to disable.")
	adminPassword := fs.String("admin-password", "", "password for the default admin operator created on startup if no operators exist")
	operatorPath := fs.String("operator-db", "", "operator database file path (default: PROBE_OPERATOR_DB env or /tmp/probe-operators.json)")
	vtAPIKey := fs.String("vt-api-key", "", "VirusTotal API key for auto-scan after build and manual scan API (default: PROBE_VT_API_KEY env)")
	showVersion := fs.Bool("version", false, "Print version and exit")
	fs.Parse(args)

	if *showVersion {
		fmt.Printf("PROBE Server %s\n", appVersion)
		os.Exit(0)
	}

	// Env vars as fallback
	if *addr == "" {
		*addr = os.Getenv("PROBE_ADDR")
	}
	if *addr == "" {
		*addr = "localhost:7700"
	}
	if *token == "" {
		*token = os.Getenv("PROBE_TOKEN")
	}
	if *registryPath == "" {
		*registryPath = os.Getenv("PROBE_REGISTRY")
	}
	if *registryPath == "" {
		*registryPath = "/tmp/probe-registry.json"
	}
	if *extraTokens == "" {
		*extraTokens = os.Getenv("PROBE_EXTRA_TOKENS")
	}
	if *enrollmentPath == "" {
		*enrollmentPath = os.Getenv("PROBE_ENROLLMENT_DB")
	}
	if *enrollmentPath == "" {
		*enrollmentPath = "/tmp/probe-enrollment.json"
	}
	if *caDir == "" {
		*caDir = os.Getenv("PROBE_CA_DIR")
	}
	if *caDir == "" {
		*caDir = "/tmp/probe-ca"
	}
	if *builderPath == "" {
		*builderPath = os.Getenv("PROBE_BUILDER_DB")
	}
	if *builderPath == "" {
		*builderPath = "/tmp/probe-builds.json"
	}
	if *builderOutputDir == "" {
		*builderOutputDir = os.Getenv("PROBE_BUILDER_OUTPUT_DIR")
	}
	if *builderOutputDir == "" {
		*builderOutputDir = "/tmp/probe-builds"
	}
	if *profilesPath == "" {
		*profilesPath = os.Getenv("PROBE_PROFILES_DB")
	}
	if *profilesPath == "" {
		*profilesPath = "/tmp/probe-profiles.json"
	}
	if *operatorPath == "" {
		*operatorPath = os.Getenv("PROBE_OPERATOR_DB")
	}
	if *operatorPath == "" {
		*operatorPath = "/tmp/probe-operators.json"
	}
	if *vtAPIKey == "" {
		*vtAPIKey = os.Getenv("PROBE_VT_API_KEY")
	}

	rlCfg := server.RateLimitConfig{
		RatePerSec:    *rateLimit,
		Burst:         *rateBurst,
		MaxConcurrent: *maxConcurrent,
	}

	// TLS mode: when a cert and key are provided, start with TLS. When a
	// client CA is also provided, enable TLS mutual authentication (mTLS).
	useTLS := *certFile != "" && *keyFile != ""
	if useTLS && *clientCA != "" {
		log.Printf("Starting PROBE server with TLS+mTLS on %s (rate-limit=%.1f req/s, burst=%d, max-concurrent=%d, token-ttl=%v, client-ca=%s)", *addr, *rateLimit, *rateBurst, *maxConcurrent, *tokenTTL, *clientCA)
	} else if useTLS {
		log.Printf("Starting PROBE server with TLS on %s (rate-limit=%.1f req/s, burst=%d, max-concurrent=%d, token-ttl=%v)", *addr, *rateLimit, *rateBurst, *maxConcurrent, *tokenTTL)
	} else {
		log.Printf("Starting PROBE server %s on %s (rate-limit=%.1f req/s, burst=%d, max-concurrent=%d, token-ttl=%v)", appVersion, *addr, *rateLimit, *rateBurst, *maxConcurrent, *tokenTTL)
	}

	var srv *server.Server
	if useTLS {
		srv = server.NewServerWithTLSRateLimit(*addr, *token, *registryPath, *certFile, *keyFile, *clientCA, rlCfg)
	} else {
		srv = server.NewServerWithRateLimit(*addr, *token, *registryPath, rlCfg)
	}
	srv.SetTokenTTL(*tokenTTL)
	srv.SetRequireAPIAuth(*requireAPIAuth)
	srv.SetEnrollmentPath(*enrollmentPath)
	srv.SetCADir(*caDir)
	srv.SetBuilderPath(*builderPath, *builderOutputDir)
	srv.SetProfilesPath(*profilesPath)
	srv.SetOperatorPath(*operatorPath)
	srv.SetAllowedCIDR(*allowedCIDR)

	// Configure VirusTotal scanner if API key is provided.
	if *vtAPIKey != "" {
		srv.SetVTAPIKey(*vtAPIKey)
		log.Printf("VirusTotal auto-scan enabled")
	}

	// Create default admin operator if no operators exist and --admin-password is set.
	if *adminPassword != "" && srv.Operators().IsEmpty() {
		op, err := srv.Operators().CreateWithPassword("admin", "admin", *adminPassword, "")
		if err != nil {
			log.Printf("WARNING: failed to create default admin operator: %v", err)
		} else {
			log.Printf("Created default admin operator (id=%s, name=%s) — log in with username 'admin'", op.ID, op.Name)
		}
	}

	// Configure reverse proxies. Default: LOGReport proxy for backward compat.
	var proxies []server.ProxyEntry
	if *proxyFlags != "" {
		for _, p := range strings.Split(*proxyFlags, ",") {
			parts := strings.SplitN(strings.TrimSpace(p), "=", 2)
			if len(parts) == 2 {
				proxies = append(proxies, server.ProxyEntry{
					PathPrefix: strings.TrimSpace(parts[0]),
					TargetURL:  strings.TrimSpace(parts[1]),
				})
			}
		}
	} else {
		// Default: LOGReport proxy
		proxies = []server.ProxyEntry{
			{PathPrefix: "/logreport", TargetURL: "http://localhost:8642"},
		}
	}
	srv.SetProxies(proxies)
	log.Printf("Configured %d reverse proxy route(s)", len(proxies))

	// Configure extra tokens for safe deployment rollover
	if *extraTokens != "" {
		extra := strings.Split(*extraTokens, ",")
		// Trim whitespace from each token
		for i, t := range extra {
			extra[i] = strings.TrimSpace(t)
		}
		srv.SetExtraTokens(extra)
		log.Printf("Accepting %d extra token(s) for rollover", len(extra))
	}

	if useTLS {
		if err := srv.StartTLS("", ""); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	} else {
		if err := srv.Start(); err != nil {
			log.Fatalf("Server failed: %v", err)
		}
	}
}