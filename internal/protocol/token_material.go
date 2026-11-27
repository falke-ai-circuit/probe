package protocol

// P19-06: material checks for token-shaped strings shared by the server
// (rotatedTokens persistence) and the agent (pickupRotatedToken adoption
// gate). Lives here because both packages already import protocol; neither
// imports the other.
//
// generateToken (internal/server/server_token.go:20) mints 24 random bytes
// hex-encoded = 48 printable lowercase hex characters; the legacy fallback
// ("tok-<unixnano>") is also printable ASCII. Every check below is therefore
// an invariant of REAL token material, not a policy roundtrip.

// MinTokenLength is the floor a plausible bearer token must clear. It is
// deliberately far below the real generator's 48 so operator hand-seeded
// test/short tokens pass, while truncated fragments and 1-2 char junk do not.
const MinTokenLength = 8

// IsPlausibleToken reports whether s has the material shape of a token this
// system issues: non-empty, no NUL bytes, only printable ASCII, and at least
// MinTokenLength characters. A pure material check — no lookup, no I/O, no
// policy. It can never validate a token's correctness, only that presenting
// it is plausibly meaningful (P19-05R F2: pickup adopted "\x00\x01truncated"
// as a live token).
func IsPlausibleToken(s string) bool {
	if len(s) < MinTokenLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == 0x00 {
			return false // NUL byte: classic truncation artifact
		}
		// Reject control chars and anything non-ASCII; both generator
		// variants emit printable ASCII only. DEL (0x7F) is not printable.
		if c < 0x20 || c > 0x7E {
			return false
		}
	}
	return true
}
