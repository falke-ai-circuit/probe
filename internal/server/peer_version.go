package server

// P17-02 (spec P17-01 §4) — READY-LEVEL version negotiation.
//
// The server never refuses a relay merely OLDER than itself (P23-00 §2
// degraded vocabulary: an older peer still connects and gets null/absent
// fields). version-mismatch asserts ONLY when a frame exchange the link
// needs cannot happen — that is, when one side's build predates the minimum
// that carries the frame types the other side sends.
//
// The bound is READY-LEVEL, not comparison-to-server: it is a stamp the
// protocol bumps when a new frame exchange lands (P21-04 typed refusals was
// the last such bump). An old peer connects fine; it just cannot carry the
// frames its build predates, and THAT is the mismatch this file owns.

import (
	"strings"
)

// peerReadyLevelMin is the minimum relay version that carries the frame
// types a v1.20-era server exchanges on a peer link (typed forward_refused,
// relay metadata.version). Bumped by spec when a new frame land changes the
// minimum.
const peerReadyLevelMin = "1.20.0"

// versionAtLeast compares dotted numeric version strings the way runtime
// builds spell them ("1.19.2"). A malformed string is NOT a match: an
// unparseable version cannot be proven sufficient, and honesty forbids
// defaulting it (§3 honest-absence rule).
func versionAtLeast(v, min string) bool {
	v = normalizeSemver(v)
	min = normalizeSemver(min)
	if v == "" {
		return false
	}
	vParts := splitSemver(v)
	mParts := splitSemver(min)
	for i := 0; i < 3; i++ {
		vp, mp := 0, 0
		if i < len(vParts) {
			vp = vParts[i]
		}
		if i < len(mParts) {
			mp = mParts[i]
		}
		if vp != mp {
			return vp > mp
		}
	}
	return true
}

// normalizeSemver strips a leading v and whitespace, then drops any
// prerelease/build suffix ("1.20.0-p1702", "1.20.0+build" ≥ 1.20.0 by
// semver ordering). Empty stays empty.
func normalizeSemver(s string) string {
	s = trimSpaceASCII(s)
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	return s
}

func splitSemver(s string) []int {
	parts := strings.Split(s, ".")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		n := 0
		ok := len(p) > 0
		for _, c := range p {
			if c < '0' || c > '9' {
				ok = false
				break
			}
			n = n*10 + int(c-'0')
		}
		if !ok {
			// Malformed component: fail closed (caller treats as mismatch).
			return []int{-1, 0, 0}
		}
		out = append(out, n)
	}
	return out
}

func trimSpaceASCII(s string) string {
	start := 0
	for start < len(s) && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	end := len(s)
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}

// peerVersionMismatch reports whether a connected relay's build predates the
// ready level this server requires for the frame exchanges it initiates.
// Empty (pre-extension relay) is honest absence, never a mismatch: the link
// runs degraded (§4), which the peer row renders as null version — the
// mismatch state is reserved for builds that CANNOT carry required frames.
func peerVersionMismatch(relayVersion string) bool {
	if relayVersion == "" {
		return false
	}
	return !versionAtLeast(relayVersion, peerReadyLevelMin)
}
