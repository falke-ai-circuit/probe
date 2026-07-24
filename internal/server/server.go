package server

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// RateLimitConfig holds the rate-limiter settings supplied from the CLI /
// environment. A zero value falls back to the proxy defaults (10/s, burst 20,
// max 5 concurrent).
type RateLimitConfig struct {
	RatePerSec    float64 // tokens added per second; <=0 → default
	Burst         int     // bucket capacity; <=0 → default
	MaxConcurrent int     // global in-flight cap; <=0 → default
}

// Server is the multi-session WebSocket server with agent registry, LLM proxy, and session management.
type Server struct {
	addr      string
	token     string   // primary auth token
	tokens    []string // all accepted tokens (for multi-token rollover support)
	registry  *Registry
	sessions  *SessionManager
	proxy     *LLMProxy
	rateLimit *RateLimiter
	srv       *http.Server
	mux       *http.ServeMux

	// TLS configuration for the server (optional).
	certFile     string
	keyFile      string
	clientCAFile string // optional CA for TLS mutual authentication (mTLS)

	mu        sync.RWMutex
	conns     map[string]Conn   // agentID -> conn (direct *websocket.Conn or relayed *virtualConn)
	connWriteMu map[string]*sync.Mutex    // agentID -> write mutex (prevents parallel WS write corruption)

	// pendingRequests maps request IDs to response channels for request-response
	// over WebSocket. When handleAgentExec sends a command to an agent, it creates
	// a channel and waits on it. handleMessages delivers the response.
	pendingMu    sync.Mutex
	pendingReqs  map[string]chan protocol.Envelope // requestID -> response channel

	// pendingUpdates tracks agents that received an agent_update command.
	// When the new agent connects, we use this to kill the old process.
	pendingUpdates map[string]*pendingUpdate

	// Tunnels: server-side TCP listeners that relay through WebSocket to the agent.
	tunnelMu    sync.Mutex
	tunnels     map[string]*Tunnel // tunnelID -> Tunnel
	tunnelCount int               // for generating unique tunnel IDs

	// requireAPIAuth enforces bearer-token auth on HTTP API endpoints
	// (/api/agents, /api/agent/*, /download/*). When false (default), requests
	// without an Authorization header are allowed through with a warning log.
	requireAPIAuth bool

	tokenTTL    time.Duration                 // configured token TTL (0 = rotation disabled)
	tokenExpiry map[string]time.Time          // agentID -> token expiry time
	tokenMu       sync.Mutex                   // guards tokenExpiry + rotatedTokens
	rotatedTokens map[string]string            // agentID -> last rotated token
	tokenStop   chan struct{}                 // closes to stop rotation goroutine
	tokenWG     sync.WaitGroup                // waits for rotation goroutine on shutdown

	// Configurable reverse proxies (path prefix → target URL)
	proxyMu sync.RWMutex
	proxies map[string]*ProxyEntry

	// RBAC: operator management (nil or empty → fall back to legacy token auth).
	operators *OperatorManager

	// Audit logging: persists every forwarded command as a JSONL entry.
	audit *AuditLogger

	// Enrollment: pre-shared token → per-agent cert → mTLS.
	enrollment *EnrollmentManager
	caManager  *CAManager

	// Revoked agents: agent IDs that are rejected on WS connect.
	revokedAgents *RevokedAgents

	// Agent builder: cross-compiles agent binaries with embedded config.
	builder *BuilderManager

	// Build profiles: reusable build configuration templates.
	profiles *ProfileManager

	// VirusTotal scanner: optional, for auto-scan after build and manual scan API.
	vtScanner *VirusTotalScanner

	// Task scheduler: delayed, recurring, and offline-queued tasks.
	tasks *TaskManager

	// File transfer manager: resumable chunked file transfers.
	transferMgr *TransferManager

	// uiWrapper wraps the mux with an embedded-frontend handler for serving
	// the React WebUI. When nil (frontend not built), Start/StartTLS uses
	// the mux directly.
	uiWrapper http.Handler

	// IP filtering: allowed CIDR for WebUI/API routes. /ws is always open.
	// When nil or "0.0.0.0/0", filtering is disabled (allow all).
	allowedCIDR    *net.IPNet
	allowedCIDRs   []*net.IPNet // additional always-allowed ranges (localhost, docker)
	ipFilterActive bool

	// Phase 4: relay registry — tracks connected relays for topology view
	relayMu sync.RWMutex
	relays  map[string]*relaySession // relayID → session
}

// startTime records when the server process began, used by /health for uptime.
var startTime = time.Now()

// NewServer creates a new server.
func NewServer(addr string, token string, registryPath string) *Server {
	reg := NewRegistry(registryPath)
	reg.StartStaleDetector()
	return &Server{
		addr:          addr,
		token:         token,
		registry:      reg,
		sessions:      NewSessionManager(),
		proxy:         NewLLMProxy(),
		conns:         make(map[string]Conn),
		connWriteMu:   make(map[string]*sync.Mutex),
		pendingReqs:   make(map[string]chan protocol.Envelope),
		pendingUpdates: make(map[string]*pendingUpdate),
		relays:        make(map[string]*relaySession),
		tunnels:       make(map[string]*Tunnel),
		tokenExpiry:   make(map[string]time.Time),
		rotatedTokens: make(map[string]string),
		tokenStop:     make(chan struct{}),
		proxies:       make(map[string]*ProxyEntry),
		operators:     NewOperatorManager(""),
		audit:         NewAuditLogger(""),
		enrollment:    NewEnrollmentManager(""),
		caManager:     NewCAManager(""),
		revokedAgents: NewRevokedAgents(),
		builder:       NewBuilderManager("", ""),
		profiles:      NewProfileManager(""),
		tasks:         NewTaskManager("", nil),
		transferMgr:   NewTransferManager(""),
	}
}

// NewServerWithRateLimit creates a new server with an explicit rate-limit
// configuration applied to the LLM proxy. Pass a RateLimitConfig whose zero
// values fall back to the proxy defaults (10 req/s, burst 20, max 5
// concurrent).
func NewServerWithRateLimit(addr string, token string, registryPath string, rlCfg RateLimitConfig) *Server {
	srv := NewServer(addr, token, registryPath)
	srv.rateLimit = NewRateLimiter(rlCfg.RatePerSec, rlCfg.Burst, rlCfg.MaxConcurrent)
	srv.proxy.SetRateLimiter(srv.rateLimit)
	return srv
}

// NewServerWithTLS creates a new server configured for TLS. certFile/keyFile
// are the server's TLS certificate and key. clientCAFile, when non-empty,
// enables TLS mutual authentication: clients must present a certificate
// signed by that CA or the TLS handshake is rejected. The returned server is
// started with StartTLS.
func NewServerWithTLS(addr string, token string, registryPath string, certFile string, keyFile string, clientCAFile string) *Server {
	srv := NewServer(addr, token, registryPath)
	srv.certFile = certFile
	srv.keyFile = keyFile
	srv.clientCAFile = clientCAFile
	return srv
}

// NewServerWithTLSRateLimit creates a new TLS server with an explicit
// rate-limit configuration. It is the TLS + rate-limiting combination of
// NewServerWithRateLimit and NewServerWithTLS.
func NewServerWithTLSRateLimit(addr string, token string, registryPath string, certFile string, keyFile string, clientCAFile string, rlCfg RateLimitConfig) *Server {
	srv := NewServerWithTLS(addr, token, registryPath, certFile, keyFile, clientCAFile)
	srv.rateLimit = NewRateLimiter(rlCfg.RatePerSec, rlCfg.Burst, rlCfg.MaxConcurrent)
	srv.proxy.SetRateLimiter(srv.rateLimit)
	return srv
}

// SetTokenTTL configures the server-side token rotation interval. When ttl > 0
// the server runs a background goroutine that proactively rotates each
// connected agent's token ttl before it expires. A ttl of 0 disables
// rotation. Must be called before Start/StartTLS.
func (s *Server) SetTokenTTL(ttl time.Duration) {
	s.tokenTTL = ttl
}

// SetAllowedCIDR configures IP filtering for WebUI/API HTTP routes. Only
// requests from the given CIDR (plus localhost and Docker ranges) are
// allowed. The /ws WebSocket endpoint is always open from any IP. When cidr
// is empty or "0.0.0.0/0", filtering is disabled (allow all). Must be called
// before Start/StartTLS.
func (s *Server) SetAllowedCIDR(cidr string) {
	if cidr == "" || cidr == "0.0.0.0/0" {
		s.ipFilterActive = false
		return
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		log.Printf("[server] WARNING: invalid --allowed-cidr %q: %v — IP filtering disabled", cidr, err)
		s.ipFilterActive = false
		return
	}
	s.allowedCIDR = ipNet
	s.ipFilterActive = true

	// Always allow localhost. Do NOT allow 172.16.0.0/12 because Docker NAT
	// makes external requests appear from that range, bypassing the filter.
	for _, localCIDR := range []string{"127.0.0.0/8", "::1/128"} {
		_, ln, err := net.ParseCIDR(localCIDR)
		if err == nil {
			s.allowedCIDRs = append(s.allowedCIDRs, ln)
		}
	}
	log.Printf("[server] IP filtering enabled: allowed CIDR %s (+ localhost, Docker)", cidr)
}

// clientIP extracts the client IP from a request, preferring X-Forwarded-For
// (for proxy setups) over r.RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Use the first IP in the list (closest to the client).
		for _, ip := range strings.Split(xff, ",") {
			ip = strings.TrimSpace(ip)
			if ip != "" {
				return ip
			}
		}
	}
	// Strip port from RemoteAddr.
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isIPAllowed checks whether the given IP is within the allowed CIDR or any
// of the always-allowed ranges (localhost, Docker).
func (s *Server) isIPAllowed(ipStr string) bool {
	if !s.ipFilterActive {
		return true
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	// Check primary CIDR.
	if s.allowedCIDR != nil && s.allowedCIDR.Contains(ip) {
		return true
	}
	// Check always-allowed ranges.
	for _, cidr := range s.allowedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ipFilterMiddleware wraps an http.Handler with IP filtering. The /ws path
// is always allowed (probe clients connect from the internet). All other
// paths are filtered by the configured allowed CIDR.
func (s *Server) ipFilterMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// WebSocket endpoint is always open.
		if r.URL.Path == "/ws" || strings.HasPrefix(r.URL.Path, "/ws/") {
			next.ServeHTTP(w, r)
			return
		}
		if !s.ipFilterActive {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)
		if !s.isIPAllowed(ip) {
			log.Printf("[server] IP filter: 403 %s for %s", ip, r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// registerRoutes sets up all HTTP routes on the server's mux. Called once
// by both Start and StartTLS to avoid route registration duplication.
func (s *Server) registerRoutes() {
	// WebSocket endpoint
	s.mux.HandleFunc("/ws", s.handleWebSocket)
	// Health endpoint
	s.mux.HandleFunc("/health", s.handleHealth)
	// HTTP API endpoints (legacy — kept for backward compatibility)
	s.mux.HandleFunc("/api/agents", s.handleListAgents)
	s.mux.HandleFunc("/api/agent/", s.handleAgentRoute)
	// File download endpoint (serves files from /tmp/probe-files/)
	s.mux.HandleFunc("/download/", s.handleFileDownload)
	// Also serve downloads under /api/download/ for Docker proxy compatibility
	s.mux.HandleFunc("/api/download/", s.handleFileDownload)
	s.mux.HandleFunc("/logreport/", s.handleLogReportProxy)

	// REST API v1 — versioned, consistent response format, Go 1.22 patterns.
	s.registerV1Routes()

	// OpenAPI 3.0 specification endpoint.
	s.mux.HandleFunc("GET /openapi.json", s.handleOpenAPI)

	// Embedded React WebUI — SPA fallback for non-API routes.
	s.registerUI()
}

// Start begins listening for WebSocket and HTTP connections.
func (s *Server) Start() error {
	s.mux = http.NewServeMux()
	s.registerRoutes()

	// Wrap mux with UI handler if the frontend was embedded, then apply
	// IP filtering middleware on top.
	handler := http.Handler(s.mux)
	if s.uiWrapper != nil {
		handler = s.uiWrapper
	}
	handler = s.ipFilterMiddleware(handler)

	s.srv = &http.Server{
		Addr:    s.addr,
		Handler: handler,
	}

	// Start proactive token rotation if a TTL was configured.
	s.StartTokenRotation()

	// Start the task scheduler background goroutine.
	s.tasks.Start()

	log.Printf("[server] starting on %s", s.addr)
	return s.srv.ListenAndServe()
}

// StartTLS begins listening with TLS. When the server was constructed with a
// clientCAFile (via NewServerWithTLS), the TLS config requires and verifies
// client certificates (mTLS). certFile/keyFile override the server's stored
// cert/key paths when non-empty.
func (s *Server) StartTLS(certFile, keyFile string) error {
	if certFile != "" {
		s.certFile = certFile
	}
	if keyFile != "" {
		s.keyFile = keyFile
	}

	s.mux = http.NewServeMux()

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
	// mTLS: require and verify client certificates when a client CA is configured.
	if s.clientCAFile != "" {
		caCert, err := os.ReadFile(s.clientCAFile)
		if err != nil {
			return fmt.Errorf("read client CA: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return fmt.Errorf("failed to parse client CA")
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = caCertPool
	}

	s.registerRoutes()

	// Wrap mux with UI handler if the frontend was embedded, then apply
	// IP filtering middleware on top.
	handler := http.Handler(s.mux)
	if s.uiWrapper != nil {
		handler = s.uiWrapper
	}
	handler = s.ipFilterMiddleware(handler)

	s.srv = &http.Server{
		Addr:      s.addr,
		Handler:   handler,
		TLSConfig: tlsConfig,
	}

	// Start proactive token rotation if a TTL is configured.
	s.StartTokenRotation()

	// Start the task scheduler background goroutine.
	s.tasks.Start()

	mode := "TLS"
	if s.clientCAFile != "" {
		mode = "TLS+mTLS"
	}
	log.Printf("[server] starting %s on %s", mode, s.addr)
	return s.srv.ListenAndServeTLS(s.certFile, s.keyFile)
}

// Close shuts down the server, stops the stale-detector goroutine, and stops
// the token rotation goroutine (if running). Closes all tunnels.
func (s *Server) Close() error {
	// Close all tunnels
	s.tunnelMu.Lock()
	for _, t := range s.tunnels {
		t.Close()
	}
	s.tunnels = make(map[string]*Tunnel)
	s.tunnelMu.Unlock()

	s.registry.Stop()
	// Stop the token rotation goroutine and wait for it to drain.
	close(s.tokenStop)
	s.tokenWG.Wait()
	// Stop the task scheduler background goroutine.
	s.tasks.Stop()
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

// SetOperatorPath configures persistent operator storage. When set,
// operators are loaded from / persisted to the given file path. Must be
// called before Start/StartTLS.
func (s *Server) SetOperatorPath(path string) {
	s.operators = NewOperatorManager(path)
}

// Operators returns the server's OperatorManager, allowing callers to create
// default operators, check IsEmpty, etc.
func (s *Server) Operators() *OperatorManager {
	return s.operators
}

// SetAuditPath configures persistent audit logging. When set, every
// forwarded command is appended to the given JSONL file. Must be called
// before Start/StartTLS.
func (s *Server) SetAuditPath(path string) {
	s.audit = NewAuditLogger(path)
}

// SetEnrollmentPath configures persistent enrollment token storage.
// When set, enrollment tokens are loaded from / persisted to the given
// file path. Must be called before Start/StartTLS.
func (s *Server) SetEnrollmentPath(path string) {
	s.enrollment = NewEnrollmentManager(path)
}

// SetCADir configures the directory where the server CA (self-signed cert
// + key) is stored. The CA is generated on first run and used to sign
// per-agent certificates during enrollment. Must be called before
// Start/StartTLS.
func (s *Server) SetCADir(dir string) {
	s.caManager = NewCAManager(dir)
}

// SetBuilderPath configures persistent build storage. When set, build
// records are loaded from / persisted to the given file path. Must be
// called before Start/StartTLS.
func (s *Server) SetBuilderPath(path, outputDir string) {
	s.builder = NewBuilderManager(path, outputDir)
}

// SetProfilesPath configures persistent build profile storage. When set,
// profiles are loaded from / persisted to the given file path. Must be
// called before Start/StartTLS.
func (s *Server) SetProfilesPath(path string) {
	s.profiles = NewProfileManager(path)
}

// SetVTAPIKey configures the VirusTotal scanner with the given API key.
// When set, builds are automatically scanned after completion and the
// manual VT scan API endpoints are available. Must be called before
// Start/StartTLS.
func (s *Server) SetVTAPIKey(apiKey string) {
	if apiKey == "" {
		return
	}
	s.vtScanner = NewVirusTotalScanner(apiKey)
	s.builder.SetVTScanner(s.vtScanner)
}

// SetTasksPath configures persistent task storage. When set, scheduled tasks
// are loaded from / persisted to the given file path and the background
// scheduler goroutine forwards pending tasks to connected agents. Must be
// called before Start/StartTLS.
func (s *Server) SetTasksPath(path string) {
	s.tasks = NewTaskManager(path, s)
}

// SetTransferPath configures persistent file transfer state. When set,
// transfer records are loaded from / persisted to the given file path so
// transfers can survive server restart. Must be called before Start/StartTLS.
func (s *Server) SetTransferPath(path string) {
	s.transferMgr = NewTransferManager(path)
}

// operatorContextKey is the context key used to store the authenticated
// operator in the request context.
type operatorContextKey struct{}

// operatorFromContext extracts the operator from the request context, or
// returns nil if no operator was set.
func operatorFromContext(r *http.Request) *Operator {
	if v := r.Context().Value(operatorContextKey{}); v != nil {
		if op, ok := v.(*Operator); ok {
			return op
		}
	}
	return nil
}

// operatorIDFromRequest is a convenience wrapper that returns the operator ID
// from the request context, or "" if no operator was set.
func operatorIDFromRequest(r *http.Request) string {
	if op := operatorFromContext(r); op != nil {
		return op.ID
	}
	return ""
}

// checkOperatorAuth extracts the bearer token from the Authorization header,
// looks up the operator by token, and checks whether the operator's role
// permits the requested action. When no operators are configured (IsEmpty),
// it falls back to the legacy token-based auth (checkAPIAuth). Returns the
// operator and true on success, or nil and false on failure.
func (s *Server) checkOperatorAuth(r *http.Request, action string) (*Operator, bool) {
	// Fall back to legacy token auth when no operators are configured.
	if s.operators == nil || s.operators.IsEmpty() {
		if s.isValidToken(r.Header.Get("Authorization")) {
			return nil, true
		}
		if !s.requireAPIAuth {
			return nil, true // auth optional, allow through
		}
		return nil, false
	}

	// RBAC mode: extract bearer token.
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader || token == "" {
		if !s.requireAPIAuth {
			return nil, true // auth optional
		}
		log.Printf("[server] missing bearer token from %s: %s", r.RemoteAddr, r.URL.Path)
		return nil, false
	}

	op := s.operators.GetByToken(token)
	if op == nil {
		if !s.requireAPIAuth {
			return nil, true // auth optional
		}
		log.Printf("[server] unknown operator token from %s: %s", r.RemoteAddr, r.URL.Path)
		return nil, false
	}

	if !op.CanPerform(action) {
		log.Printf("[server] operator %s (role %s) denied action %q on %s",
			op.ID, op.Role, action, r.URL.Path)
		return op, false
	}

	// Update last-seen timestamp.
	s.operators.UpdateLastSeen(op.ID, time.Now().UTC())
	return op, true
}

