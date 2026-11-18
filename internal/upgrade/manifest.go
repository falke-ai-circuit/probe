package upgrade

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// File names and suffixes are fixed by D5.5 so this path and any future
// in-band path cannot disagree about them.
const (
	BootScriptFile     = "p2boot.ps1"
	DiscoverScriptFile = "p2discover.ps1"
	ManifestFile       = "p2boot.manifest.json"
	JournalFile        = "p2boot.journal"
	OldSHAFile         = "p2boot.oldsha"
	StagedSuffix       = ".next"
	BackupSuffix       = ".old"
)

// Timing and transport constants settled by P2-02 (O7, D5.6) and P2-01 (O1).
const (
	// DefaultNRollbackS is the commit's patience deadline for a sustained
	// session: spawn + five dial attempts at maximum jitter + handshake.
	DefaultNRollbackS = 60
	// DefaultKDwellS is the post-registration dwell before the old process is
	// killed.
	DefaultKDwellS = 10
	// DefaultLDwellS is the dwell after the kill, with the successor alive.
	DefaultLDwellS = 10
	// ChunkSize is the raw payload bytes per fs-write call (O1; unmeasured
	// server body limit, chosen six times under the only frame-size datum).
	ChunkSize = 262144
	// PreflightTimeoutS / LaunchTimeoutS are the exec timeouts from P2-02 2.4.
	PreflightTimeoutS = 60
	LaunchTimeoutS    = 15
	DiscoverTimeoutS  = 30
)

// Manifest is p2boot.manifest.json. The first six fields are required by
// P2-02 SW-05; the rest are optional and carry the defaults above when absent.
// No configuration, token or secret material belongs here.
type Manifest struct {
	Canonical     string `json:"canonical"`
	SizePayload   int64  `json:"size_payload"`
	SHA256Payload string `json:"sha256_payload"`
	SHA256Boot    string `json:"sha256_boot"`
	ServerHost    string `json:"server_host"`
	ServerPort    int    `json:"server_port"`

	// OldPID is the discovered agent PID (A4). Preflight refuses without it.
	OldPID int `json:"old_pid,omitempty"`
	// Name is the agent name the successor must register under.
	Name string `json:"name,omitempty"`
	// ConfigPath is the absolute --config path when the old process was a
	// config-file build. Empty means the embedded-config build (zero argv).
	ConfigPath string `json:"config_path,omitempty"`
	// Timings default to 60/10/10 when zero.
	NRollbackS int `json:"n_rollback_s,omitempty"`
	KDwellS    int `json:"k_dwell_s,omitempty"`
	LDwellS    int `json:"l_dwell_s,omitempty"`
}

// ApplyDefaults fills the optional timing fields.
func (m *Manifest) ApplyDefaults() {
	if m.NRollbackS <= 0 {
		m.NRollbackS = DefaultNRollbackS
	}
	if m.KDwellS <= 0 {
		m.KDwellS = DefaultKDwellS
	}
	if m.LDwellS <= 0 {
		m.LDwellS = DefaultLDwellS
	}
}

// Validate enforces the manifest contract: required keys present, digests
// well formed, path absolute, port in range, ASCII only.
func (m Manifest) Validate() error {
	missing := func(k string) error {
		return stepErr("SW-05", CodeManifestInvalid, fmt.Errorf("required key %q is empty", k))
	}
	if m.Canonical == "" {
		return missing("canonical")
	}
	if !IsAbsWinPath(m.Canonical) {
		return stepErr("SW-05", CodeManifestInvalid,
			fmt.Errorf("canonical %q is not absolute", m.Canonical))
	}
	if EqualWinPath(m.Canonical, StripStagedSuffix(m.Canonical)) == false {
		return stepErr("SW-05", CodeManifestInvalid,
			fmt.Errorf("canonical %q still carries a staging suffix", m.Canonical))
	}
	if m.SizePayload <= 0 {
		return stepErr("SW-05", CodeManifestInvalid, fmt.Errorf("size_payload must be positive"))
	}
	if err := validateDigest("sha256_payload", m.SHA256Payload); err != nil {
		return err
	}
	if err := validateDigest("sha256_boot", m.SHA256Boot); err != nil {
		return err
	}
	if m.ServerHost == "" {
		return missing("server_host")
	}
	if m.ServerPort < 1 || m.ServerPort > 65535 {
		return stepErr("SW-05", CodeManifestInvalid,
			fmt.Errorf("server_port %d out of range", m.ServerPort))
	}
	if m.ConfigPath != "" && !IsAbsWinPath(m.ConfigPath) {
		return stepErr("SW-05", CodeManifestInvalid,
			fmt.Errorf("config_path %q is not absolute (O10)", m.ConfigPath))
	}
	for _, v := range []string{m.Canonical, m.ServerHost, m.Name, m.ConfigPath} {
		if strings.ContainsAny(v, "\x00\n\r") {
			return stepErr("SW-05", CodeManifestInvalid, fmt.Errorf("control character in manifest value"))
		}
	}
	return nil
}

func validateDigest(key, value string) error {
	if len(value) != 64 {
		return stepErr("SW-05", CodeManifestInvalid,
			fmt.Errorf("%s must be 64 hex characters, got %d", key, len(value)))
	}
	if _, err := hex.DecodeString(value); err != nil {
		return stepErr("SW-05", CodeManifestInvalid, fmt.Errorf("%s is not hex: %w", key, err))
	}
	return nil
}

// Marshal renders the manifest as one ASCII line. encoding/json emits raw
// UTF-8 for non-ASCII values, so the ASCII rule is checked here rather than
// assumed: PowerShell 5.1 reads a BOM-less file as ANSI.
func (m Manifest) Marshal() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return nil, stepErr("SW-05", CodeManifestInvalid, err)
	}
	for _, b := range data {
		if b > 0x7F {
			return nil, stepErr("SW-05", CodeManifestInvalid,
				fmt.Errorf("manifest contains a non-ASCII byte"))
		}
	}
	return append(data, '\n'), nil
}

// ParseManifest parses a manifest and validates it.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return m, stepErr("SW-05", CodeManifestInvalid, err)
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	m.ApplyDefaults()
	return m, nil
}

// SHA256Hex returns the lowercase hex sha256 of b.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ShortDigest is the 16-character prefix the journal records.
func ShortDigest(digest string) string {
	if len(digest) < 16 {
		return digest
	}
	return digest[:16]
}

// IsASCII reports whether every byte of b is in the ASCII range.
func IsASCII(b []byte) bool {
	for _, c := range b {
		if c > 0x7F {
			return false
		}
	}
	return true
}
