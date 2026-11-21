package platform

// identity.go — build-derived naming for the agent entry point.
//
// Nothing here names a product. The display name an agent registers with and
// the config file it reads by default are both derived from the running
// executable, so a binary that has been renamed (or woven into another
// product) presents its own name on the wire and on disk instead of the
// original build's brand.
//
// Caller contract:
//   - DefaultAgentName() is used wherever a config leaves "name" empty; it
//     must be stable for the process lifetime.
//   - DefaultConfigName() is the --config default; the operator either drops
//     that file next to the executable or passes --config explicitly.

import (
	"os"
	"path/filepath"
	"strings"
)

// executableBase returns the file name of the running binary without its
// directory or extension. It prefers the invoked path (os.Args[0]) because
// that is what an operator typed or a service registered, and falls back to
// os.Executable() when Args[0] carries no base name (e.g. a bare argv[0]).
func executableBase() string {
	base := ""
	if len(os.Args) > 0 {
		base = strings.TrimSpace(os.Args[0])
	}
	if base != "" {
		base = filepath.Base(filepath.FromSlash(base))
	}
	if base == "" || base == "." || base == string(filepath.Separator) {
		if exe, err := os.Executable(); err == nil {
			base = filepath.Base(exe)
		}
	}
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".EXE")
	base = strings.TrimSpace(base)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "agent"
	}
	return strings.ToLower(base)
}

// DefaultAgentName is the display name an agent advertises when the config
// does not set one.
func DefaultAgentName() string {
	return executableBase()
}

// DefaultConfigName is the JSON config file name used when --config is not
// supplied: "<executable base>.json" in the working directory.
func DefaultConfigName() string {
	return executableBase() + ".json"
}
