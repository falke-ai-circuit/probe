package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// IP Blacklist
// ---------------------------------------------------------------------------

// BlacklistConfig holds IP blacklist configuration. When active, requests from
// blacklisted IPs are rejected with 403 on ALL routes (including /ws).
type BlacklistConfig struct {
	CIDRs []string // CIDR ranges to block (e.g., "1.2.3.0/24", "5.6.7.8/32")
}

// blacklistedCIDRs holds parsed CIDR ranges for the blacklist.
type blacklistedCIDRs struct {
	mu    sync.RWMutex
	cidrs []*net.IPNet
}

// SetBlacklist configures IP blacklist for all routes. When set, requests from
// the given CIDR ranges are rejected with 403 on every endpoint, including /ws.
// Must be called before Start/StartTLS.
func (s *Server) SetBlacklist(cidrs []string) {
	if len(cidrs) == 0 {
		s.blacklistMu.Lock()
		s.blacklistedCIDRs = nil
		s.blacklistMu.Unlock()
		return
	}

	var parsed []*net.IPNet
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		// Allow single IP addresses (convert to /32 or /128)
		if !strings.Contains(c, "/") {
			ip := net.ParseIP(c)
			if ip == nil {
				log.Printf("[server] WARNING: invalid blacklist IP %q: not a valid IP", c)
				continue
			}
			if ip.To4() != nil {
				c += "/32"
			} else {
				c += "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(c)
		if err != nil {
			log.Printf("[server] WARNING: invalid blacklist CIDR %q: %v", c, err)
			continue
		}
		parsed = append(parsed, ipNet)
	}

	s.blacklistMu.Lock()
	s.blacklistedCIDRs = parsed
	s.blacklistMu.Unlock()

	if len(parsed) > 0 {
		var descs []string
		for _, c := range parsed {
			descs = append(descs, c.String())
		}
		log.Printf("[server] IP blacklist active: %d range(s): %s", len(parsed), strings.Join(descs, ", "))
	}
}

// isBlacklisted checks whether the given IP is in the blacklist.
func (s *Server) isBlacklisted(ipStr string) bool {
	s.blacklistMu.RLock()
	defer s.blacklistMu.RUnlock()
	if len(s.blacklistedCIDRs) == 0 {
		return false
	}
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}
	for _, cidr := range s.blacklistedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// blacklistMiddleware wraps an http.Handler with IP blacklist filtering.
// Applied to ALL routes (including /ws). Must run BEFORE the whitelist filter
// and before any route-specific handler.
func (s *Server) blacklistMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if s.isBlacklisted(ip) {
			log.Printf("[server] IP blacklist: 403 %s for %s", ip, r.URL.Path)
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Security Headers
// ---------------------------------------------------------------------------

// securityHeadersMiddleware adds standard security headers to all HTTP responses.
// These prevent content-type sniffing, clickjacking, and enforce HTTPS.
func (s *Server) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-XSS-Protection", "0") // Modern browsers use X-Content-Type-Options instead
		// HSTS only makes sense over HTTPS; set it unconditionally and let
		// browsers ignore it on plain HTTP.
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Login Rate Limiting
// ---------------------------------------------------------------------------

// loginAttempt tracks failed login attempts per IP for brute-force protection.
type loginAttempt struct {
	failures  int
	firstFail time.Time
	lastFail  time.Time
	lockedUntil time.Time
}

// loginRateLimiter provides per-IP rate limiting for login attempts.
// After maxFailures failed attempts within the window, the IP is locked
// for lockDuration. Successful logins reset the counter.
type loginRateLimiter struct {
	mu         sync.Mutex
	attempts   map[string]*loginAttempt // IP → attempt state
	maxFail    int                      // max failed attempts before lock
	window     time.Duration            // counting window
	lockDur    time.Duration            // how long to lock after threshold
}

// newLoginRateLimiter creates a login rate limiter with sensible defaults:
// 5 failed attempts in 5 minutes → 15-minute lockout.
func newLoginRateLimiter() *loginRateLimiter {
	return &loginRateLimiter{
		attempts: make(map[string]*loginAttempt),
		maxFail:  5,
		window:   5 * time.Minute,
		lockDur:  15 * time.Minute,
	}
}

// isLocked returns true if the IP is currently locked out.
func (lrl *loginRateLimiter) isLocked(ip string) bool {
	lrl.mu.Lock()
	defer lrl.mu.Unlock()

 att, ok := lrl.attempts[ip]
	if !ok {
		return false
	}
	if !att.lockedUntil.IsZero() && time.Now().Before(att.lockedUntil) {
		return true
	}
	// Lock expired → reset
	if !att.lockedUntil.IsZero() && time.Now().After(att.lockedUntil) {
		delete(lrl.attempts, ip)
		return false
	}
	return false
}

// recordFailure records a failed login attempt and returns true if the IP
// is now locked out.
func (lrl *loginRateLimiter) recordFailure(ip string) bool {
	lrl.mu.Lock()
	defer lrl.mu.Unlock()

	now := time.Now()
	att, ok := lrl.attempts[ip]
	if !ok {
		att = &loginAttempt{firstFail: now}
		lrl.attempts[ip] = att
	}

	// Reset if outside the window
	if now.Sub(att.firstFail) > lrl.window {
		att.failures = 0
		att.firstFail = now
	}

	att.failures++
	att.lastFail = now

	if att.failures >= lrl.maxFail {
		att.lockedUntil = now.Add(lrl.lockDur)
		log.Printf("[security] login rate limit: IP %s locked out for %v after %d failed attempts",
			ip, lrl.lockDur, att.failures)
		return true
	}
	return false
}

// recordSuccess clears the failure counter for the IP.
func (lrl *loginRateLimiter) recordSuccess(ip string) {
	lrl.mu.Lock()
	defer lrl.mu.Unlock()
	delete(lrl.attempts, ip)
}

// loginAttemptsStatus returns a snapshot of login rate-limiter state for diagnostics.
func (lrl *loginRateLimiter) status() map[string]interface{} {
	lrl.mu.Lock()
	defer lrl.mu.Unlock()
	locked := 0
	tracked := 0
	for _, att := range lrl.attempts {
		tracked++
		if !att.lockedUntil.IsZero() && time.Now().Before(att.lockedUntil) {
			locked++
		}
	}
	return map[string]interface{}{
		"tracked_ips":     tracked,
		"locked_ips":      locked,
		"max_failures":    lrl.maxFail,
		"window_seconds":  int(lrl.window.Seconds()),
		"lock_seconds":    int(lrl.lockDur.Seconds()),
	}
}

// ---------------------------------------------------------------------------
// Login Audit Logging
// ---------------------------------------------------------------------------

// LoginAuditEntry records a login attempt (success or failure) with the
// source IP, operator name, and outcome. Written to the same JSONL audit log
// as command audit entries, but with action "login" or "login_failed".
type LoginAuditEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Action    string    `json:"action"`     // "login_success" or "login_failed"
	Operator  string    `json:"operator"`   // attempted username
	SourceIP  string    `json:"source_ip"`
	UserAgent string    `json:"user_agent,omitempty"`
	LockedOut bool      `json:"locked_out,omitempty"`
}

// logLoginAttempt writes a login audit entry to the audit log.
func (s *Server) logLoginAttempt(action, operator, sourceIP, userAgent string, locked bool) {
	if s.audit == nil {
		return
	}
	s.audit.Log(AuditEntry{
		Action:     action,
		OperatorID: operator,
		Result:     action,
		Params: LoginAuditEntry{
			Timestamp: time.Now().UTC(),
			Action:    action,
			Operator:  operator,
			SourceIP:  sourceIP,
			UserAgent: userAgent,
			LockedOut: locked,
		},
	})
}

// ---------------------------------------------------------------------------
// Security Status API
// ---------------------------------------------------------------------------

// SecurityStatus represents the current security configuration and state,
// returned by the /api/v1/security/status endpoint.
type SecurityStatus struct {
	IPFilterActive   bool     `json:"ip_filter_active"`
	AllowedCIDR      string   `json:"allowed_cidr,omitempty"`
	BlacklistActive  bool     `json:"blacklist_active"`
	BlacklistCount   int      `json:"blacklist_count"`
	RequireAPIAuth   bool     `json:"require_api_auth"`
	OperatorsCount   int      `json:"operators_count"`
	AuditLogActive   bool     `json:"audit_log_active"`
	TLS              bool     `json:"tls"`
	MTLS             bool     `json:"mtls"`
	TokenTTL         string   `json:"token_ttl,omitempty"`
	LoginRateLimit   map[string]interface{} `json:"login_rate_limit"`
}

// handleV1SecurityStatus returns the current security configuration.
// Requires admin role.
func (s *Server) handleV1SecurityStatus(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	blacklistCount := 0
	s.blacklistMu.RLock()
	blacklistCount = len(s.blacklistedCIDRs)
	s.blacklistMu.RUnlock()

	status := SecurityStatus{
		IPFilterActive:  s.ipFilterActive,
		RequireAPIAuth:  s.requireAPIAuth,
		BlacklistActive: blacklistCount > 0,
		BlacklistCount:  blacklistCount,
		TLS:             s.certFile != "",
		MTLS:            s.clientCAFile != "",
		AuditLogActive:  s.audit != nil && s.audit.IsActive(),
		OperatorsCount:  len(s.operators.List()),
		LoginRateLimit:  s.loginLimiter.status(),
	}
	if s.allowedCIDR != nil {
		status.AllowedCIDR = s.allowedCIDR.String()
	}
	if s.tokenTTL > 0 {
		status.TokenTTL = s.tokenTTL.String()
	}

	writeJSON(w, http.StatusOK, status)
}

// handleV1SecurityBlacklist is an admin endpoint to dynamically add/remove
// blacklist CIDRs at runtime without restarting the server.
//
// POST /api/v1/security/blacklist
// {"action": "add", "cidrs": ["1.2.3.0/24"]}
// {"action": "remove", "cidrs": ["1.2.3.0/24"]}
// {"action": "clear"}
// {"action": "list"}
func (s *Server) handleV1SecurityBlacklist(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	var params struct {
		Action string   `json:"action"`
		CIDRs  []string `json:"cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}

	switch params.Action {
	case "list":
		s.blacklistMu.RLock()
		cidrs := make([]string, 0, len(s.blacklistedCIDRs))
		for _, c := range s.blacklistedCIDRs {
			cidrs = append(cidrs, c.String())
		}
		s.blacklistMu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"blacklist": cidrs})

	case "add":
		s.blacklistMu.RLock()
		existing := make([]*net.IPNet, len(s.blacklistedCIDRs))
		copy(existing, s.blacklistedCIDRs)
		s.blacklistMu.RUnlock()

		for _, c := range params.CIDRs {
			c = strings.TrimSpace(c)
			if c == "" {
				continue
			}
			if !strings.Contains(c, "/") {
				ip := net.ParseIP(c)
				if ip == nil {
					writeError(w, http.StatusBadRequest, "INVALID_PARAMS", fmt.Sprintf("invalid IP: %s", c))
					return
				}
				if ip.To4() != nil {
					c += "/32"
				} else {
					c += "/128"
				}
			}
			_, ipNet, err := net.ParseCIDR(c)
			if err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_PARAMS", fmt.Sprintf("invalid CIDR: %s: %v", c, err))
				return
			}
			existing = append(existing, ipNet)
		}
		s.blacklistMu.Lock()
		s.blacklistedCIDRs = existing
		s.blacklistMu.Unlock()
		log.Printf("[server] blacklist updated: %d range(s) (added %d)", len(existing), len(params.CIDRs))
		writeJSON(w, http.StatusOK, map[string]interface{}{"blacklist_count": len(existing)})

	case "remove":
		s.blacklistMu.Lock()
		toRemove := make(map[string]bool)
		for _, c := range params.CIDRs {
			toRemove[strings.TrimSpace(c)] = true
		}
		filtered := s.blacklistedCIDRs[:0:0]
		for _, c := range s.blacklistedCIDRs {
			if !toRemove[c.String()] {
				filtered = append(filtered, c)
			}
		}
		s.blacklistedCIDRs = filtered
		count := len(filtered)
		s.blacklistMu.Unlock()
		writeJSON(w, http.StatusOK, map[string]interface{}{"blacklist_count": count})

	case "clear":
		s.blacklistMu.Lock()
		s.blacklistedCIDRs = nil
		s.blacklistMu.Unlock()
		log.Printf("[server] blacklist cleared")
		writeJSON(w, http.StatusOK, map[string]interface{}{"blacklist_count": 0})

	default:
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "action must be: list, add, remove, or clear")
	}
}

// handleV1SecurityLoginAttempts returns the current login rate-limiter state
// (locked IPs, tracked IPs, configuration). Admin-only.
func (s *Server) handleV1SecurityLoginAttempts(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.loginLimiter.status())
}

// registerSecurityRoutes registers the security management API routes on the
// v1 mux. Called by registerV1Routes.
func (s *Server) registerSecurityRoutes() {
	s.mux.HandleFunc("GET /api/v1/security/status", s.handleV1SecurityStatus)
	s.mux.HandleFunc("POST /api/v1/security/blacklist", s.handleV1SecurityBlacklist)
	s.mux.HandleFunc("GET /api/v1/security/login-attempts", s.handleV1SecurityLoginAttempts)
}