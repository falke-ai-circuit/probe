//go:build windows

package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

func getenv(k string) string { return os.Getenv(k) }

// detectPrincipal reads the current process token. IsSystem requires the
// LocalSystem SID (S-1-5-18): an administrator account, however privileged,
// is not SYSTEM and must be reported as itself.
func detectPrincipal() PrincipalInfo {
	p := PrincipalInfo{Integrity: "unknown", User: "unknown"}
	tok := windows.GetCurrentProcessToken()
	elevated := tok.IsElevated()

	sid := ""
	if tu, err := tok.GetTokenUser(); err == nil && tu != nil {
		sid = tu.User.Sid.String()
	}
	if sid != "" {
		p.User = sid
	}

	p.Elevated = elevated
	p.IsSystem = sid == "S-1-5-18"
	p.TokenFiltered = !elevated
	switch {
	case p.IsSystem:
		p.Integrity = "system"
		p.Elevated = true
		p.TokenFiltered = false
	case elevated:
		p.Integrity = "high"
	default:
		p.Integrity = "medium"
	}
	return p
}
