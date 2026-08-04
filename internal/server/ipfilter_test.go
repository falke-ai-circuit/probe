package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestIPFilter_Disabled verifies that when filtering is disabled, all IPs are allowed.
func TestIPFilter_Disabled(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("") // disabled

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should be called when filter is disabled")
	}
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestIPFilter_AlwaysAllowed verifies that /ws bypasses filtering.
func TestIPFilter_WSBypass(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/ws", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("/ws should bypass IP filter")
	}
}

// TestIPFilter_Blocked verifies that non-allowed IPs get 403.
func TestIPFilter_Blocked(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "8.8.8.8:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("handler should NOT be called for blocked IP")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

// TestIPFilter_LocalhostAllowed verifies that localhost is always allowed.
func TestIPFilter_LocalhostAllowed(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should be called for localhost")
	}
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestIPFilter_TailscaleAllowed verifies that Tailscale IPs are allowed.
func TestIPFilter_TailscaleAllowed(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "100.64.1.2:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !called {
		t.Error("handler should be called for Tailscale IP")
	}
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

// TestIPFilter_XForwardedFor verifies that X-Forwarded-For is parsed.
func TestIPFilter_XForwardedFor(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10")

	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	// Request from 127.0.0.1 but XFF says 8.8.8.8 → should be blocked.
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-For", "8.8.8.8")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if called {
		t.Error("handler should NOT be called when XFF IP is blocked")
	}
	if rec.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rec.Code)
	}
}

// TestIPFilter_MultiCIDR verifies that comma-separated CIDRs are all allowed.
func TestIPFilter_MultiCIDR(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10,10.10.10.0/24")

	// Tailscale IP should be allowed.
	{
		called := false
		mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		}))
		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		req.RemoteAddr = "100.116.46.18:12345"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if !called {
			t.Error("Tailscale IP 100.116.46.18 should be allowed")
		}
		if rec.Code != 200 {
			t.Errorf("expected 200 for Tailscale IP, got %d", rec.Code)
		}
	}

	// Subnet-routed IP should be allowed.
	{
		called := false
		mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			w.WriteHeader(200)
		}))
		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		req.RemoteAddr = "10.10.10.50:12345"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if !called {
			t.Error("subnet IP 10.10.10.50 should be allowed")
		}
		if rec.Code != 200 {
			t.Errorf("expected 200 for subnet IP, got %d", rec.Code)
		}
	}

	// Random IP should be blocked.
	{
		called := false
		mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
		}))
		req := httptest.NewRequest("GET", "/api/v1/health", nil)
		req.RemoteAddr = "8.8.8.8:12345"
		rec := httptest.NewRecorder()
		mw.ServeHTTP(rec, req)
		if called {
			t.Error("random IP 8.8.8.8 should be blocked")
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("expected 403 for random IP, got %d", rec.Code)
		}
	}
}

// TestIPFilter_MultiCIDR_InvalidSkipped verifies that invalid CIDRs in a
// comma-separated list are skipped while valid ones still work.
func TestIPFilter_MultiCIDR_InvalidSkipped(t *testing.T) {
	srv := NewServer("127.0.0.1:0", "tok", "")
	srv.SetAllowedCIDR("100.64.0.0/10,not-a-cidr,10.10.10.0/24")

	// Valid CIDR should still work.
	called := false
	mw := srv.ipFilterMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	req.RemoteAddr = "10.10.10.50:12345"
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	if !called {
		t.Error("valid CIDR should still work when an invalid CIDR is in the list")
	}
	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}