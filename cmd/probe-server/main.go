package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/server"
	"github.com/falke-ai-circuit/probe/internal/version"
)

var (
	// appVersion is derived from internal/version, the single source of truth.
	appVersion = version.Display()
	appName    = "PROBE Server"
)

func main() {
	addr := flag.String("addr", "", "listen address (default: PROBE_ADDR env or localhost:7700)")
	token := flag.String("token", "", "auth token (default: PROBE_TOKEN env)")
	registryPath := flag.String("registry", "", "registry file path (default: PROBE_REGISTRY env or /tmp/probe-registry.json)")
	rateLimit := flag.Float64("rate-limit", 10.0, "LLM proxy rate limit: requests per second per agent (default 10)")
	rateBurst := flag.Int("rate-burst", 20, "LLM proxy burst size: max tokens accumulated per agent (default 20)")
	maxConcurrent := flag.Int("max-concurrent", 5, "LLM proxy max concurrent in-flight requests across all agents (default 5)")
	tokenTTL := flag.Duration("token-ttl", 24*time.Hour, "token rotation interval; the server proactively rotates each agent's token before it expires. 0 disables rotation.")
	certFile := flag.String("cert-file", "", "TLS certificate file (PEM) for the server; enables TLS when provided with --key-file")
	keyFile := flag.String("key-file", "", "TLS key file (PEM) for the server; enables TLS when provided with --cert-file")
	clientCA := flag.String("client-ca", "", "Client CA certificate file (PEM) for TLS mutual authentication; requires --cert-file/--key-file")
	extraTokens := flag.String("extra-tokens", "", "Comma-separated additional auth tokens (for safe rollover: new server accepts old agent's token)")
	requireAPIAuth := flag.Bool("require-api-auth", false, "Require bearer-token auth on HTTP API endpoints (/api/agents, /api/agent/*, /download/*). When false (default), missing auth is logged as a warning but allowed through.")
	proxyFlags := flag.String("proxy", "", "Reverse proxy: path=target (repeatable via comma). Example: /logreport=http://localhost:8642. When empty, defaults to /logreport=http://localhost:8642 for backward compatibility.")
	enrollmentPath := flag.String("enrollment-db", "", "enrollment tokens file path (default: PROBE_ENROLLMENT_DB env or /tmp/probe-enrollment.json)")
	caDir := flag.String("ca-dir", "", "directory for CA cert/key storage (default: PROBE_CA_DIR env or /tmp/probe-ca)")
	builderPath := flag.String("builder-db", "", "agent builder records file path (default: PROBE_BUILDER_DB env or /tmp/probe-builds.json)")
	builderOutputDir := flag.String("builder-output-dir", "", "directory for built agent binaries (default: PROBE_BUILDER_OUTPUT_DIR env or /tmp/probe-builds)")
	filesDir := flag.String("files-dir", "", "managed artifacts directory for builds, downloads, transfers and updates — must live on a persistent volume (default: PROBE_FILES_DIR env or /data/files)")
	filesKeep := flag.Int("files-keep", 0, "artifacts retained per (agent|name) (default: PROBE_FILES_KEEP env or 5)")
	profilesPath := flag.String("profiles-db", "", "build profiles file path (default: PROBE_PROFILES_DB env or /tmp/probe-profiles.json)")
	allowedCIDR := flag.String("allowed-cidr", "100.64.0.0/10,10.10.10.0/24,172.16.0.0/12", "CIDR ranges allowed for WebUI/API HTTP routes (default: Tailscale 100.64.0.0/10 + subnet-routed 10.10.10.0/24 + Docker bridges 172.16.0.0/12). Comma-separated for multiple. /ws is always open from any IP. Set to 0.0.0.0/0 to disable.")
	adminPassword := flag.String("admin-password", "", "password for the default admin operator created on startup if no operators exist")
	operatorPath := flag.String("operator-db", "", "operator database file path (default: PROBE_OPERATOR_DB env or /tmp/probe-operators.json)")
	relayStatus := flag.String("relay-status", "", "loopback address (host:port) of the relay serving this server's uplink; enables the uplink edge in /api/v1/topology (default: PROBE_RELAY_STATUS env). Empty = no uplink configured.")
	advertisedAddr := flag.String("advertised-addr", "", "address peers should dial for this server, pushed in peer_hello (default: PROBE_ADVERTISED_ADDR env, else host name + listen port)")
	// P21-07: installed relay allowlist + fingerprint-only identity records.
	relayAllowlist := flag.String("relay-allowlist", "", "comma-separated relay identities admitted to forward downstream clients: id, id:token_fp8, or *:token_fp8. Omitted = allow all; given empty = deny all.")
	relayIdentityPath := flag.String("relay-identity-db", "", "relay identity records file (fingerprints only; default: PROBE_RELAY_IDENTITY_DB env or /tmp/probe-relay-identities.json)")
	vtAPIKey := flag.String("vt-api-key", "", "VirusTotal API key for auto-scan after build and manual scan API (default: PROBE_VT_API_KEY env)")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("%s %s\n", appName, appVersion)
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
	if *relayStatus == "" {
		*relayStatus = os.Getenv("PROBE_RELAY_STATUS")
	}
	if *advertisedAddr == "" {
		*advertisedAddr = os.Getenv("PROBE_ADVERTISED_ADDR")
	}
	if *vtAPIKey == "" {
		*vtAPIKey = os.Getenv("PROBE_VT_API_KEY")
	}
	if *filesDir == "" {
		*filesDir = os.Getenv("PROBE_FILES_DIR")
	}
	if *filesDir == "" {
		*filesDir = server.DefaultFilesDir
	}
	if *filesKeep <= 0 {
		if raw := os.Getenv("PROBE_FILES_KEEP"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				*filesKeep = n
			}
		}
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
	// F1: the version string was parsed for the banner but never handed to
	// the server, so /api/v1/health served "server_version":"" on this
	// binary while `probe serve` reported correctly. One line, same source.
	srv.SetVersion(appVersion)
	srv.SetUplinkRelayStatus(*relayStatus)
	srv.SetAdvertisedAddr(*advertisedAddr)
	// P21-07: fail loud on a malformed allowlist entry — never a silent pass.
	allowlistProvided := false
	flag.Visit(func(f *flag.Flag) { if f.Name == "relay-allowlist" { allowlistProvided = true } })
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
	srv.SetRequireAPIAuth(*requireAPIAuth)
	srv.SetEnrollmentPath(*enrollmentPath)
	srv.SetCADir(*caDir)
	srv.SetBuilderPath(*builderPath, *builderOutputDir)
	// P9-11: one resolved, persistent artifacts directory. Fail loud at boot
	// rather than at the first build: an unwritable volume is a deployment
	// error, not a per-request surprise.
	if err := srv.SetFilesDir(*filesDir); err != nil {
		log.Fatalf("artifacts dir: %v", err)
	}
	srv.SetFilesKeep(*filesKeep)
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
