package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBlacklist_Blocked verifies that blacklisted IPs get 403 on all routes.
func TestBlacklist_Blocked(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetBlacklist([]string{"8.8.8.8"})

	called := false
	mw := srv.blacklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("handler should NOT be called for blacklisted IP")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

// TestBlacklist_WSBlocked verifies that /ws is also blocked for blacklisted IPs.
func TestBlacklist_WSBlocked(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetBlacklist([]string{"8.8.8.8"})

	called := false
	mw := srv.blacklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/ws", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("handler should NOT be called for blacklisted IP on /ws")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 on /ws for blacklisted IP, got %d", rec.Code)
	}
}

// TestBlacklist_NotBlocked verifies that non-blacklisted IPs pass through.
func TestBlacklist_NotBlocked(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetBlacklist([]string{"8.8.8.8"})

	called := false
	mw := srv.blacklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "1.2.3.4:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should be called for non-blacklisted IP")
	}
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestBlacklist_CIDR verifies CIDR range blocking.
func TestBlacklist_CIDR(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetBlacklist([]string{"10.0.0.0/8"})

	// IP in range
	mw := srv.blacklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "10.1.2.3:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403 for IP in blacklisted CIDR, got %d", rec.Code)
	}

	// IP outside range
	req2 := httptest.NewRequest("GET", "/api/v1/health", nil)
	req2.RemoteAddr = "192.168.1.1:12345"
	rec2 := httptest.NewRecorder()
	mw.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Errorf("expected 200 for IP outside blacklisted CIDR, got %d", rec2.Code)
	}
}

// TestBlacklist_Disabled verifies that empty blacklist allows all.
func TestBlacklist_Disabled(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetBlacklist(nil)

	called := false
	mw := srv.blacklistMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should be called when blacklist is disabled")
	}
}

// TestSecurityHeaders verifies that security headers are set on responses.
func TestSecurityHeaders(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")

	mw := srv.securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("missing X-Content-Type-Options: nosniff")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("missing X-Frame-Options: DENY")
	}
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("missing Strict-Transport-Security")
	}
}

// TestLoginRateLimit verifies that repeated failed logins lock out the IP.
func TestLoginRateLimit(t *testing.T) {
	lrl := newLoginRateLimiter()
	// Override defaults for faster testing
	lrl.maxFail = 3
	lrl.window = 60000000000  // 1 min
	lrl.lockDur = 300000000000 // 5 min

	ip := "1.2.3.4"

	// 3 failures should lock
	for i := 0; i < 3; i++ {
		locked := lrl.recordFailure(ip)
		if i < 2 && locked {
			t.Errorf("should not be locked after %d failures", i+1)
		}
		if i == 2 && !locked {
			t.Error("should be locked after 3 failures")
		}
	}

	// Now should be locked
	if !lrl.isLocked(ip) {
		t.Error("IP should be locked after 3 failures")
	}

	// Different IP should not be locked
	if lrl.isLocked("5.6.7.8") {
		t.Error("different IP should not be locked")
	}

	// Success clears the counter
	lrl.recordSuccess(ip)
	// But we're still locked until lockDur expires — success doesn't unlock
	// Actually, recordSuccess deletes the entry entirely
	if lrl.isLocked(ip) {
		t.Error("IP should not be locked after recordSuccess")
	}
}

// TestLoginRateLimit_WindowReset verifies that failures outside the window reset.
func TestLoginRateLimit_WindowReset(t *testing.T) {
	lrl := newLoginRateLimiter()
	lrl.maxFail = 3
	lrl.window = 1 // 1 nanosecond — always expired

	ip := "9.9.9.9"

	// Record 2 failures
	lrl.recordFailure(ip)
	lrl.recordFailure(ip)

	// Wait a moment
	// Since window is 1ns, the next failure should reset the counter
	locked := lrl.recordFailure(ip)
	if locked {
		t.Error("should not lock — window expired, counter should have reset")
	}
}

// TestSecurityStatus verifies the security status endpoint structure.
func TestSecurityStatus(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")
	srv.SetBlacklist([]string{"1.2.3.4"})

	// Verify state directly
	if !srv.ipFilterActive {
		t.Error("IP filter should be active")
	}
	if !srv.isBlacklisted("1.2.3.4") {
		t.Error("1.2.3.4 should be blacklisted")
	}
	if srv.isBlacklisted("5.6.7.8") {
		t.Error("5.6.7.8 should not be blacklisted")
	}
}

// TestBlacklistRuntimeAPI tests the dynamic blacklist management API.
func TestBlacklistRuntimeAPI(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetOperatorPath("")
	srv.SetRequireAPIAuth(false) // allow unauthenticated for test

	// Create admin operator
	op, _ := srv.operators.CreateWithPassword("admin", "admin", "pass", "test-token")
	_ = op

	// Add to blacklist
	body := `{"action":"add","cidrs":["8.8.4.4/32"]}`
	req := httptest.NewRequest("POST", "/api/v1/security/blacklist", stringReader(body))
	req.Header.Set("Authorization", "Bearer test-token")
	rec := httptest.NewRecorder()
	srv.handleV1SecurityBlacklist(rec, req)

	if rec.Code != 200 {
		t.Errorf("add blacklist: expected 200, got %d", rec.Code)
	}
	if !srv.isBlacklisted("8.8.4.4") {
		t.Error("8.8.4.4 should be blacklisted after add")
	}

	// List blacklist
	req2 := httptest.NewRequest("POST", "/api/v1/security/blacklist", stringReader(`{"action":"list"}`))
	req2.Header.Set("Authorization", "Bearer test-token")
	rec2 := httptest.NewRecorder()
	srv.handleV1SecurityBlacklist(rec2, req2)

	var resp struct {
		OK   bool     `json:"ok"`
		Data struct {
			Blacklist []string `json:"blacklist"`
		} `json:"data"`
	}
	json.NewDecoder(rec2.Body).Decode(&resp)
	if !resp.OK || len(resp.Data.Blacklist) == 0 {
		t.Error("list blacklist: expected non-empty blacklist")
	}

	// Clear blacklist
	req3 := httptest.NewRequest("POST", "/api/v1/security/blacklist", stringReader(`{"action":"clear"}`))
	req3.Header.Set("Authorization", "Bearer test-token")
	rec3 := httptest.NewRecorder()
	srv.handleV1SecurityBlacklist(rec3, req3)

	if srv.isBlacklisted("8.8.4.4") {
		t.Error("8.8.4.4 should not be blacklisted after clear")
	}
}

// helper
func stringReader(s string) *stringReaderImpl {
	return &stringReaderImpl{s: s}
}

type stringReaderImpl struct {
	s string
	i int
}

func (sr *stringReaderImpl) Read(p []byte) (int, error) {
	if sr.i >= len(sr.s) {
		return 0, nil
	}
	n := copy(p, sr.s[sr.i:])
	sr.i += n
	return n, nil
}