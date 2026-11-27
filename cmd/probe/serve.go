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
	tokenTTL := fs.Duration("token-ttl", 24*time.Hour, "token rotation interval; the server proactively rotates each agent's token before it expires. 0 disables rotation (P19-01: requires --allow-disabled-token-ttl).")
	allowZeroTTL := fs.Bool("allow-disabled-token-ttl", false, "explicit opt-out for --token-ttl=0 (disables token rotation)")
	e2e := fs.Bool("e2e", true, "E2E AES-GCM encryption of agent↔server traffic (P19-01: default ON; --e2e=false to disable)")
	certFile := fs.String("cert-file", "", "TLS certificate file (PEM) for the server; enables TLS when provided with --key-file")
	keyFile := fs.String("key-file", "", "TLS key file (PEM) for the server; enables TLS when provided with --cert-file")
	clientCA := fs.String("client-ca", "", "Client CA certificate file (PEM) for TLS mutual authentication; requires --cert-file/--key-file")
	extraTokens := fs.String("extra-tokens", "", "Comma-separated additional auth tokens (for safe rollover: new server accepts old agent's token)")
	requireAPIAuth := fs.Bool("require-api-auth", false, "Require bearer-token auth on HTTP API endpoints (/api/agents, /api/agent/*, /download/*). When false (default), missing auth is logged as a warning but allowed through.")
	proxyFlags := fs.String("proxy", "", "Reverse proxy: path=target (repeatable via comma). Example: /logreport=http://localhost:8642. When empty, defaults to /logreport=http://localhost:8642 for backward compatibility.")
	enrollmentPath := fs.String("enrollment-db", "", "enrollment tokens file path (default: PROBE_ENROLLMENT_DB env or /tmp/probe-enrollment.json)")
	rotatedTokensPath := fs.String("rotated-tokens-db", "", "rotated agent tokens file path (P19-06; default: PROBE_ROTATED_TOKENS_DB env or /tmp/probe-rotated-tokens.json)")
	caDir := fs.String("ca-dir", "", "directory for CA cert/key storage (default: PROBE_CA_DIR env or /tmp/probe-ca)")
	builderPath := fs.String("builder-db", "", "agent builder records file path (default: PROBE_BUILDER_DB env or /tmp/probe-builds.json)")
	builderOutputDir := fs.String("builder-output-dir", "", "directory for built agent binaries (default: PROBE_BUILDER_OUTPUT_DIR env or /tmp/probe-builds)")
	builderGoPath := fs.String("builder-go", "", "path to the go binary for agent cross-compilation (default: /opt/data/go/bin/go1.23.12)")
	builderSource := fs.String("builder-source", "", "path to the probe-client package for cross-compilation (default: ./cmd/probe-client/)")
	profilesPath := fs.String("profiles-db", "", "build profiles file path (default: PROBE_PROFILES_DB env or /tmp/probe-profiles.json)")
	allowedCIDR := fs.String("allowed-cidr", "100.64.0.0/10,10.10.10.0/24,172.16.0.0/12", "CIDR ranges allowed for WebUI/API HTTP routes (default: Tailscale 100.64.0.0/10 + subnet-routed 10.10.10.0/24 + Docker bridges 172.16.0.0/12). Comma-separated for multiple. /ws is always open from any IP. Set to 0.0.0.0/0 to disable.")
	blacklistCIDR := fs.String("blacklist-cidr", "", "Comma-separated CIDR ranges to block from ALL routes including /ws (e.g., \"1.2.3.0/24,5.6.7.8/32\"). Blocks known-bad IPs from connecting.")
	auditLogPath := fs.String("audit-log", "", "Path to audit log file (JSONL format). Logs every command, login, and access decision. Default: /tmp/probe-audit.jsonl")
	flowsPath := fs.String("flows-path", "", "Path to flow definitions JSON file (persistent). Default: /data/runtime/flows.json. Created if missing.")
	adminPassword := fs.String("admin-password", "", "password for the default admin operator created on startup if no operators exist")
	operatorPath := fs.String("operator-db", "", "operator database file path (default: PROBE_OPERATOR_DB env or /tmp/probe-operators.json)")
	relayStatus := fs.String("relay-status", "", "loopback address (host:port) of the relay serving this server's uplink; enables the uplink edge in /api/v1/topology (default: PROBE_RELAY_STATUS env). Empty = no uplink configured.")
	advertisedAddr := fs.String("advertised-addr", "", "address peers should dial for this server, pushed in peer_hello (default: PROBE_ADVERTISED_ADDR env, else host name + listen port)")
	// P21-07: the installed relay allowlist. Omitted = allow all; given but
	// empty = deny all; given with entries = admit exactly those ids/fps.
	relayAllowlist := fs.String("relay-allowlist", "", "comma-separated relay identities admitted to forward downstream clients: id, id:token_fp8, or *:token_fp8. Omitted = allow all; given empty = deny all.")
	relayIdentityPath := fs.String("relay-identity-db", "", "relay identity records file (fingerprints only; default: PROBE_RELAY_IDENTITY_DB env or /tmp/probe-relay-identities.json)")
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
	// P19-06: rotated-token persistence follows the sibling flag/env/tmpdefault
	// pattern exactly (enrollment-db is the model).
	if *rotatedTokensPath == "" {
		*rotatedTokensPath = os.Getenv("PROBE_ROTATED_TOKENS_DB")
	}
	if *rotatedTokensPath == "" {
		*rotatedTokensPath = "/tmp/probe-rotated-tokens.json"
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
	if *relayStatus == "" {
		*relayStatus = os.Getenv("PROBE_RELAY_STATUS")
	}
	if *advertisedAddr == "" {
		*advertisedAddr = os.Getenv("PROBE_ADVERTISED_ADDR")
	}
	if *auditLogPath == "" {
		*auditLogPath = os.Getenv("PROBE_AUDIT_LOG")
	}
	if *auditLogPath == "" {
		*auditLogPath = "/tmp/probe-audit.jsonl"
	}
	if *blacklistCIDR == "" {
		*blacklistCIDR = os.Getenv("PROBE_BLACKLIST_CIDR")
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
	// P19-01: an explicit --token-ttl=0 disables agent token rotation
	// entirely; refused unless the operator passes --allow-disabled-token-ttl.
	// The default (24h) path is unchanged. Rule lives in internal/server so
	// both server entrypoints enforce the same line.
	ttlWasSet := false
	fs.Visit(func(f *flag.Flag) { if f.Name == "token-ttl" { ttlWasSet = true } })
	if err := server.ValidateTokenTTL(ttlWasSet, *tokenTTL, *allowZeroTTL); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n\n", err)
		fs.Usage()
		os.Exit(1)
	}
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
	// P19-01 §11.4: E2E defaults ON — an old plain-text agent is unaffected
	// (its frames are still text JSON); a new agent sends binary encrypted
	// frames and this server is the matching peer. SetE2E with an empty token
	// stays inactive.
	srv.SetE2E(*token, *e2e)
	srv.SetVersion(appVersion)
	srv.SetUplinkRelayStatus(*relayStatus)
	srv.SetAdvertisedAddr(*advertisedAddr)
	// P21-07: install the allowlist before Start. A malformed entry is a
	// deployment error and must fail loud, not fall back to a silent pass.
	allowlistProvided := false
	fs.Visit(func(f *flag.Flag) { if f.Name == "relay-allowlist" { allowlistProvided = true } })
	if *relayAllowlist == "" {
		if env := os.Getenv("PROBE_RELAY_ALLOWLIST"); env != "" {
			*relayAllowlist = env
			allowlistProvided = true
		}
	}
	if err := srv.InstallRelayAllowlist(*relayAllowlist, allowlistProvided); err != nil {
		log.Fatalf("relay allowlist: %v", err)
	}
	if *relayIdentityPath == "" {
		*relayIdentityPath = os.Getenv("PROBE_RELAY_IDENTITY_DB")
	}
	if err := srv.SetRelayIdentityPath(*relayIdentityPath); err != nil {
		log.Fatalf("relay identity db: %v", err)
	}
	srv.SetEnrollmentPath(*enrollmentPath)
	// P19-06: load persisted rotated tokens before accepting connections.
	srv.SetRotatedTokensPath(*rotatedTokensPath)
	srv.SetCADir(*caDir)
	srv.SetBuilderPath(*builderPath, *builderOutputDir)
	if *builderGoPath != "" {
		srv.SetBuilderGoPath(*builderGoPath)
	}
	if *builderSource != "" {
		srv.SetBuilderClientPkg(*builderSource)
	}
	srv.SetProfilesPath(*profilesPath)
	srv.SetOperatorPath(*operatorPath)
	srv.SetAllowedCIDR(*allowedCIDR)

	// Configure audit logging (every command, login, and access decision).
	srv.SetAuditPath(*auditLogPath)
	log.Printf("Audit log: %s", *auditLogPath)

	// Configure flow runtime (server-side workflow orchestrator). The flow
	// manager is persistent on disk; the event store is also persistent.
	// Both default to /tmp/* for dev; production sets --flows-path.
	flowsPathValue := *flowsPath
	if flowsPathValue == "" {
		flowsPathValue = "/data/runtime/flows.json"
	}
	srv.SetFlowsPath(flowsPathValue)
	log.Printf("Flows: %s", flowsPathValue)

	// Configure IP blacklist (blocks specific IPs from ALL routes).
	if *blacklistCIDR != "" {
		cidrs := strings.Split(*blacklistCIDR, ",")
		for i, c := range cidrs {
			cidrs[i] = strings.TrimSpace(c)
		}
		srv.SetBlacklist(cidrs)
	}

	// When operators are configured, auto-enable requireAPIAuth for security.
	// This prevents unauthenticated API access when the server is internet-exposed.
	// The admin can still explicitly disable it with --require-api-auth=false if needed.
	if *adminPassword != "" || !srv.Operators().IsEmpty() {
		*requireAPIAuth = true
		log.Printf("[security] requireAPIAuth auto-enabled (operators configured)")
	}
	srv.SetRequireAPIAuth(*requireAPIAuth)

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
