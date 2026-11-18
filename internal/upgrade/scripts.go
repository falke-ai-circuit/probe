package upgrade

import _ "embed"

// The bootstrap artefacts are embedded so the caller ships exactly the bytes
// whose digests it pins (V1 / H3): the scripts are not read from the server's
// filesystem at dispatch time.
//
//go:embed scripts/p2boot.ps1
var bootScript []byte

//go:embed scripts/p2discover.ps1
var discoverScript []byte

// BootScript returns a copy of the embedded p2boot.ps1 bytes.
func BootScript() []byte { return append([]byte(nil), bootScript...) }

// DiscoverScript returns a copy of the embedded p2discover.ps1 bytes.
func DiscoverScript() []byte { return append([]byte(nil), discoverScript...) }

// BootScriptSHA256 is the V1 authority value for p2boot.ps1.
func BootScriptSHA256() string { return SHA256Hex(bootScript) }

// DiscoverScriptSHA256 is the V1 authority value for p2discover.ps1.
func DiscoverScriptSHA256() string { return SHA256Hex(discoverScript) }
