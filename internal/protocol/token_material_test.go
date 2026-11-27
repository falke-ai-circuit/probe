package protocol

import (
	"testing"
)

// TestP19R26_IsPlausibleTokenClosesTheMaterialGate — the boundary the P19R1
// F2 probe exploited (\x00\x01truncated-fragment adopted as a live token)
// is closed at the material level.
func TestP19R26_IsPlausibleTokenClosesTheMaterialGate(t *testing.T) {
	implausible := []string{
		"",                        // empty
		"ab",                      // below MinTokenLength
		"\x00\x01truncated",       // the reviewer's F2 garbage
		"tok\x00en-fragment-1234", // NUL mid-string
		"tok\ten-fragment-1234",   // control char (tab)
		"tok\nten-fragment-1234",  // control char (newline)
		"tok\x7Fen-fragment-1234", // DEL
		"tok\u00e9n-12345678",     // non-ASCII
	}
	for _, s := range implausible {
		if IsPlausibleToken(s) {
			t.Errorf("implausible material accepted: %q", s)
		}
	}
	plausible := []string{
		"tok-A",
		"tok-OLD",
		"rotated-token-NEW",
		"dead-token",
		generateTokenShaped(), // 48-char hex: the real generator's shape
	}
	for _, s := range plausible {
		if !IsPlausibleToken(s) {
			t.Errorf("plausible token refused: %q", s)
		}
	}
}

// generateTokenShaped returns a 48-char lowercase-hex string, the exact shape
// server-side generateToken emits (24 random bytes hex-encoded).
func generateTokenShaped() string {
	const hex48 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	return hex48
}
