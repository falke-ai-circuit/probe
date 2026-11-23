package agent

import (
	"time"
)

// PrincipalInfo is the identity the agent process actually runs under. It is
// read from the operating system's own token/credentials at call time — it is
// evidence, never a value copied from the request.
type PrincipalInfo struct {
	// User is the OS-native account name (Windows: "<DOMAIN>\<user>" or the
	// SID when the name cannot be resolved; Unix: the account name).
	User string
	// IsSystem is true only when the process token is the system principal
	// (Windows LocalSystem, SID S-1-5-18). A Unix root process is NOT SYSTEM:
	// the contract's SYSTEM means the Windows service account.
	IsSystem bool
	// Elevated reports whether the token is elevated (Windows UAC); always
	// true for a SYSTEM token. On Unix it reports uid 0.
	Elevated bool
	// Integrity is the declared integrity label: "system" | "high" | "medium"
	// | "root" | "user" | "unknown".
	Integrity string
	// TokenFiltered reports a UAC-filtered (non-elevated) token. It is the
	// incident condition: a filtered admin token is still denied SYSTEM-only
	// operations.
	TokenFiltered bool
}

// principalFn is a package variable so tests can present a known principal
// without needing to run as SYSTEM. Production code only calls principalFn().
var principalFn = detectPrincipal

// currentPrincipal returns the process principal. Never guesses: when the OS
// call fails the zero-value fields say "unknown" rather than inventing an
// identity.
func currentPrincipal() PrincipalInfo {
	p := principalFn()
	if p.Integrity == "" {
		p.Integrity = "unknown"
	}
	return p
}

// agentTZ declares the host time zone for the response envelope. The rule is
// "declared, never inferred by the caller": TZ wins when set, otherwise the
// Go runtime's own local zone label is reported verbatim. A label of "Local"
// is honest (the zone could not be named) and tz_offset_min carries the
// numeric truth for that instant.
func agentTZ() (name string, offsetMin int) {
	now := time.Now()
	_, off := now.Zone()
	name = now.Location().String()
	if env := tzEnv(); env != "" {
		name = env
	}
	if name == "" {
		name = "unknown"
	}
	return name, off / 60
}

// tzEnv is split out so the Windows and Unix builds can differ if needed.
func tzEnv() string { return getenv("TZ") }
