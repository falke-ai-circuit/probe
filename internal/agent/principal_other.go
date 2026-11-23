//go:build !windows

package agent

import (
	"os"
	"os/user"
	"strconv"
)

func getenv(k string) string { return os.Getenv(k) }

// detectPrincipal reports the Unix account. SYSTEM does not exist here, so
// IsSystem is always false and exec{as:"SYSTEM"} always refuses — the
// contract is a Windows service principal, and claiming root is SYSTEM would
// be a false success on a host that has no such account.
func detectPrincipal() PrincipalInfo {
	uid := os.Getuid()
	p := PrincipalInfo{Integrity: "user", User: strconv.Itoa(uid), TokenFiltered: true}
	if u, err := user.Current(); err == nil && u != nil {
		if u.Username != "" {
			p.User = u.Username
		}
	}
	if uid == 0 {
		p.Elevated = true
		p.Integrity = "root"
		p.TokenFiltered = false
	}
	return p
}
