// Package version is the single source of truth for the PROBE build version.
//
// Every surface that displays or reports a version reads it from here, so the
// value printed on the client banner and the value the agent reports to the
// server can never diverge. The canonical form (Semver) carries no "v" prefix;
// Display() adds it for human-facing banners and Wire() is the form sent over
// the protocol, which consumers already expect as bare "1.18.6".
//
// Release builds may override Semver at link time with a single -X flag:
//
//	go build -ldflags "-X github.com/falke-ai-circuit/probe/internal/version.Semver=1.18.7" ./cmd/probe-client/
//
// When no -X is supplied the literal below is used, so a plain `go build` still
// produces a self-consistent binary on both surfaces.
package version

import "strings"

// Semver is the release version in canonical form (no "v" prefix). Overridable
// with -X at build time; see the package doc.
var Semver = "1.20.0"

// Normalize converts any accepted spelling ("v1.18.6", " 1.18.6 ") into the
// canonical wire form ("1.18.6"). It is the only place the prefix is stripped.
func Normalize(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), "v")
}

// Wire is the version string sent to the server and stored in the registry.
func Wire() string { return Normalize(Semver) }

// Display is the version string shown in banners and usage text.
func Display() string { return "v" + Wire() }
