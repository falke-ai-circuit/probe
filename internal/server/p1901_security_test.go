package server

// P19-01 hardening-lite — pins for the seams that live in this package:
//
//   - ValidateTokenTTL: explicit --token-ttl=0 is refused unless
//     --allow-disabled-token-ttl; the unset/24h default path is unchanged.
//     Both server entrypoints import this rule so the two mains cannot drift.

import (
	"testing"
	"time"
)

// TestP1901_ValidateTTLGuardExplicitZeroRefused is the seam-3 acceptance:
// an explicit 0 without the opt-out is a loud refusal.
func TestP1901_ValidateTTLGuardExplicitZeroRefused(t *testing.T) {
	err := ValidateTokenTTL(true, 0, false)
	if err == nil {
		t.Fatalf("explicit token-ttl=0 without opt-out: expected refusal, got nil")
	}
	t.Logf("refusal: %v", err)
}

// TestP1901_ValidateTTLGuardOptOutHonoured: the explicit opt-out admits 0.
func TestP1901_ValidateTTLGuardOptOutHonoured(t *testing.T) {
	if err := ValidateTokenTTL(true, 0, true); err != nil {
		t.Fatalf("explicit ttl=0 WITH opt-out: expected allowed, got %v", err)
	}
}

// TestP1901_ValidateTTLGuardDefaultUnchanged: the unset flag and the 24h
// default path are exactly as before.
func TestP1901_ValidateTTLGuardDefaultUnchanged(t *testing.T) {
	if err := ValidateTokenTTL(false, 0, false); err != nil {
		t.Errorf("unset flag: unexpected refusal %v", err)
	}
	if err := ValidateTokenTTL(true, 24*time.Hour, false); err != nil {
		t.Errorf("default 24h path changed: %v", err)
	}
}
