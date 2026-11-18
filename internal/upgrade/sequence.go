package upgrade

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Exec command strings from P2-02 section 2.4. Each carries no path and no
// quote: paths travel as typed JSON fields, which is the only way to be immune
// to the exec quoting defect (A1).
const (
	ExecDiscover  = `powershell -NoProfile -ExecutionPolicy Bypass -File .\p2discover.ps1`
	ExecPreflight = `powershell -NoProfile -ExecutionPolicy Bypass -File .\p2boot.ps1 -Preflight`
	ExecLaunch    = `powershell -NoProfile -ExecutionPolicy Bypass -File .\p2boot.ps1 -Launch`
)

// Runner drives the caller side of the sequence through a Transport.
type Runner struct {
	T Transport
	// Logf is optional instrumentation; it is never used for control flow.
	Logf func(format string, args ...any)
}

// NewRunner returns a Runner over t.
func NewRunner(t Transport) *Runner { return &Runner{T: t} }

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

func (r *Runner) requireTransport() error {
	if r.T == nil {
		return stepErr("SW-00", "NO_TRANSPORT", fmt.Errorf("runner has no transport"))
	}
	return nil
}

// Discovered is the single JSON line p2discover.ps1 prints (A4).
type Discovered struct {
	PID     int    `json:"pid"`
	Exe     string `json:"exe"`
	Dir     string `json:"dir"`
	Name    string `json:"name"`
	Cmdline string `json:"cmdline"`
}

// Discover runs A4: the ancestor walk. dir is only a working directory for
// relative script resolution; exec's workdir is not sandbox-checked.
func (r *Runner) Discover(ctx context.Context, dir string) (Discovered, error) {
	var d Discovered
	if err := r.requireTransport(); err != nil {
		return d, err
	}
	res, err := r.T.Exec(ctx, ExecRequest{Command: ExecDiscover, Timeout: DiscoverTimeoutS, WorkDir: dir})
	if err != nil {
		return d, stepErr("A4", "DISCOVER_FAILED", err)
	}
	d, perr := decodeJSONLine[Discovered](res.Stdout)
	if perr != nil {
		return d, stepErr("A4", "DISCOVER_FAILED",
			fmt.Errorf("exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 240)))
	}
	if res.ExitCode != 0 || d.PID <= 0 {
		return d, stepErr("A4", "DISCOVER_FAILED",
			fmt.Errorf("exit=%d pid=%d", res.ExitCode, d.PID))
	}
	return d, nil
}

// ResolveCanonical performs SW-00 against the agent.
func (r *Runner) ResolveCanonical(ctx context.Context, discoveredExe, supplied string) (CanonicalResolution, error) {
	if err := r.requireTransport(); err != nil {
		return CanonicalResolution{}, err
	}
	return ResolveCanonical(discoveredExe, supplied, func(path string) (StatResult, error) {
		return r.T.FsStat(ctx, path)
	})
}

// StageRequest is the input to StagePayload (SW-01..SW-06).
type StageRequest struct {
	// Dir is the exe directory the artefacts are written beside.
	Dir string
	// Manifest carries canonical plus server endpoint. Digests may be left
	// empty: they are derived from Payload and from the embedded script bytes,
	// so the caller and the manifest cannot disagree about what was staged.
	Manifest Manifest
	// Payload is the exact new-client binary.
	Payload []byte
	// SandboxDir is the agent's sandbox_dir ("" = unrestricted). Every fs-*
	// call on the staged path is checked against it (G-SANDBOX).
	SandboxDir string
}

// StageResult is what StagePayload leaves on the host.
type StageResult struct {
	Canonical      string
	StagedPath     string
	BootPath       string
	ManifestPath   string
	PayloadSHA256  string
	BootSHA256     string
	ManifestSHA256 string
	Chunks         int
}

// StagePayload runs SW-01..SW-06: the payload is written chunked, verified by
// digest and size, then armed with the commit script and the manifest.
func (r *Runner) StagePayload(ctx context.Context, req StageRequest) (StageResult, error) {
	var out StageResult
	if err := r.requireTransport(); err != nil {
		return out, err
	}
	if len(req.Payload) == 0 {
		return out, stepErr("SW-01", CodeManifestInvalid, fmt.Errorf("empty payload"))
	}

	manifest := req.Manifest
	canonical := CleanWinPath(manifest.Canonical)
	if canonical == "" {
		return out, stepErr("SW-00", CodeCanonicalUnresolved, fmt.Errorf("canonical is empty"))
	}
	if !IsAbsWinPath(canonical) {
		return out, stepErr("SW-00", CodeCanonicalUnresolved, fmt.Errorf("canonical %q is not absolute", canonical))
	}
	dir := CleanWinPath(req.Dir)
	if dir == "" {
		dir = DirWinPath(canonical)
	}

	// G-SANDBOX is asserted before the first fs-* op, not after a refusal.
	if err := SandboxGate(req.SandboxDir, dir); err != nil {
		return out, err
	}

	// SW-01: the authority digest is computed over the bytes the caller holds.
	payloadDigest := SHA256Hex(req.Payload)
	if manifest.SHA256Payload != "" && !strings.EqualFold(manifest.SHA256Payload, payloadDigest) {
		return out, stepErr("SW-01", CodeDigestMismatch,
			fmt.Errorf("manifest %s does not match the payload bytes", ShortDigest(manifest.SHA256Payload)))
	}
	manifest.SHA256Payload = payloadDigest
	if manifest.SizePayload != 0 && manifest.SizePayload != int64(len(req.Payload)) {
		return out, stepErr("SW-01", CodeSizeMismatch,
			fmt.Errorf("manifest size %d does not match %d payload bytes", manifest.SizePayload, len(req.Payload)))
	}
	manifest.SizePayload = int64(len(req.Payload))
	bootDigest := BootScriptSHA256()
	if manifest.SHA256Boot != "" && !strings.EqualFold(manifest.SHA256Boot, bootDigest) {
		return out, stepErr("SW-05", CodeDigestMismatch,
			fmt.Errorf("manifest sha256_boot does not match the embedded script"))
	}
	manifest.SHA256Boot = bootDigest
	manifest.Canonical = canonical
	manifest.ApplyDefaults()
	if err := manifest.Validate(); err != nil {
		return out, err
	}

	staged := canonical + StagedSuffix

	// SW-02: sequential chunked write. Ordering is part of correctness - the
	// offset>0 branch seeks an existing file.
	chunks, err := r.writePayload(ctx, staged, req.Payload)
	if err != nil {
		if IsPermissionDenied(err) {
			return out, stepErr("SW-02", CodeSandboxRefused, err)
		}
		// No delete primitive exists: a replay rewrites from offset 0, which
		// truncates. One replay only (F3).
		r.logf("SW-02: write failed (%v), replaying from offset 0", err)
		chunks, err = r.writePayload(ctx, staged, req.Payload)
		if err != nil {
			return out, stepErr("SW-02", CodeWriteFailed, err)
		}
	}

	// SW-03: the assembled file must hash to the caller's digest.
	hash, err := r.T.FsHash(ctx, staged)
	if err != nil {
		return out, stepErr("SW-03", CodeWriteFailed, err)
	}
	if !strings.EqualFold(hash.SHA256, payloadDigest) {
		r.logf("SW-03: digest mismatch (%s), replaying from offset 0", ShortDigest(hash.SHA256))
		if _, rerr := r.writePayload(ctx, staged, req.Payload); rerr != nil {
			return out, stepErr("SW-03", CodeWriteFailed, rerr)
		}
		hash, err = r.T.FsHash(ctx, staged)
		if err != nil {
			return out, stepErr("SW-03", CodeWriteFailed, err)
		}
		if !strings.EqualFold(hash.SHA256, payloadDigest) {
			return out, stepErr("SW-03", CodeDigestMismatch,
				fmt.Errorf("expected %s actual %s", ShortDigest(payloadDigest), ShortDigest(hash.SHA256)))
		}
	}

	// SW-04: size is a cross-check, never a proof (A4).
	st, err := r.T.FsStat(ctx, staged)
	if err != nil {
		return out, stepErr("SW-04", CodeWriteFailed, err)
	}
	if !st.Exists || st.IsDir {
		return out, stepErr("SW-04", CodeSizeMismatch, fmt.Errorf("staged payload missing"))
	}
	if st.Size != int64(len(req.Payload)) {
		return out, stepErr("SW-04", CodeSizeMismatch,
			fmt.Errorf("expected %d bytes, stat reports %d", len(req.Payload), st.Size))
	}

	// SW-05: arm the commit script and the manifest beside the payload.
	bootPath := JoinWinPath(dir, BootScriptFile)
	manifestPath := JoinWinPath(dir, ManifestFile)
	if _, err := r.T.FsWrite(ctx, WriteRequest{Path: bootPath, Data: BootScript(), Mode: "0644"}); err != nil {
		return out, stepErr("SW-05", CodeWriteFailed, err)
	}
	manifestBytes, err := manifest.Marshal()
	if err != nil {
		return out, err
	}
	if _, err := r.T.FsWrite(ctx, WriteRequest{Path: manifestPath, Data: manifestBytes, Mode: "0644"}); err != nil {
		return out, stepErr("SW-05", CodeWriteFailed, err)
	}

	// SW-06: V1 for the script, V4 for the manifest (the manifest cannot carry
	// its own digest).
	bootHash, err := r.T.FsHash(ctx, bootPath)
	if err != nil {
		return out, stepErr("SW-06", CodeWriteFailed, err)
	}
	if !strings.EqualFold(bootHash.SHA256, bootDigest) {
		return out, stepErr("SW-06", CodeDigestMismatch,
			fmt.Errorf("sha256_boot expected %s actual %s", ShortDigest(bootDigest), ShortDigest(bootHash.SHA256)))
	}
	manifestHash, err := r.T.FsHash(ctx, manifestPath)
	if err != nil {
		return out, stepErr("SW-06", CodeWriteFailed, err)
	}

	out = StageResult{
		Canonical:      canonical,
		StagedPath:     staged,
		BootPath:       bootPath,
		ManifestPath:   manifestPath,
		PayloadSHA256:  payloadDigest,
		BootSHA256:     bootDigest,
		ManifestSHA256: manifestHash.SHA256,
		Chunks:         chunks,
	}
	return out, nil
}

func (r *Runner) writePayload(ctx context.Context, path string, payload []byte) (int, error) {
	chunks := 0
	for offset := 0; offset < len(payload); offset += ChunkSize {
		end := offset + ChunkSize
		if end > len(payload) {
			end = len(payload)
		}
		mode := "0644"
		if offset == 0 {
			mode = "create"
		}
		res, err := r.T.FsWrite(ctx, WriteRequest{
			Path:   path,
			Data:   payload[offset:end],
			Offset: offset,
			Mode:   mode,
		})
		if err != nil {
			return chunks, err
		}
		if res.Written != end-offset {
			return chunks, fmt.Errorf("chunk at offset %d: wrote %d of %d bytes", offset, res.Written, end-offset)
		}
		chunks++
	}
	return chunks, nil
}

// PreflightReport is the JSON line p2boot.ps1 -Preflight prints.
type PreflightReport struct {
	Phase  string         `json:"phase"`
	OK     bool           `json:"ok"`
	Checks map[string]any `json:"checks"`
	OldPID int            `json:"old_pid"`
}

// Preflight runs SW-07. On failure the parsed report is returned alongside the
// error so the caller can journal the checks that refused.
func (r *Runner) Preflight(ctx context.Context, dir string) (PreflightReport, error) {
	var report PreflightReport
	if err := r.requireTransport(); err != nil {
		return report, err
	}
	res, err := r.T.Exec(ctx, ExecRequest{Command: ExecPreflight, Timeout: PreflightTimeoutS, WorkDir: dir})
	if err != nil {
		return report, stepErr("SW-07", CodePreflightRefused, err)
	}
	report, perr := decodeJSONLine[PreflightReport](res.Stdout)
	if perr != nil {
		return report, stepErr("SW-07", CodePreflightRefused,
			fmt.Errorf("exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 240)))
	}
	if res.ExitCode != 0 || !report.OK {
		return report, stepErr("SW-07", CodePreflightRefused,
			fmt.Errorf("exit=%d phase=%s checks=%v", res.ExitCode, report.Phase, report.Checks))
	}
	return report, nil
}

// LaunchReport is the JSON line p2boot.ps1 -Launch prints.
type LaunchReport struct {
	Phase     string `json:"phase"`
	CommitPID int    `json:"commit_pid"`
}

// Launch runs SW-08: the boundary. A non-zero exit or a timeout means nothing
// has moved and the unit is re-runnable.
func (r *Runner) Launch(ctx context.Context, dir string) (LaunchReport, error) {
	var report LaunchReport
	if err := r.requireTransport(); err != nil {
		return report, err
	}
	res, err := r.T.Exec(ctx, ExecRequest{Command: ExecLaunch, Timeout: LaunchTimeoutS, WorkDir: dir})
	if err != nil {
		return report, stepErr("SW-08", CodeLaunchFailed, err)
	}
	if res.TimedOut {
		return report, stepErr("SW-08", CodeLaunchFailed, fmt.Errorf("exec timed out"))
	}
	report, perr := decodeJSONLine[LaunchReport](res.Stdout)
	if perr != nil {
		return report, stepErr("SW-08", CodeLaunchFailed,
			fmt.Errorf("exit=%d stdout=%q", res.ExitCode, truncate(res.Stdout, 240)))
	}
	if res.ExitCode != 0 || report.CommitPID <= 0 {
		return report, stepErr("SW-08", CodeLaunchFailed,
			fmt.Errorf("exit=%d commit_pid=%d", res.ExitCode, report.CommitPID))
	}
	return report, nil
}

// ReadJournal reads <dir>\p2boot.journal through the agent (SV-02: the API is
// not an observation channel after the boundary, but the new agent can read
// the file once it is alive).
func (r *Runner) ReadJournal(ctx context.Context, dir string) ([]Event, error) {
	if err := r.requireTransport(); err != nil {
		return nil, err
	}
	res, err := r.T.FsRead(ctx, ReadRequest{Path: JoinWinPath(dir, JournalFile)})
	if err != nil {
		return nil, stepErr("SV-02", CodeJournalUnreadable, err)
	}
	return ParseJournal(res.Data), nil
}

// VerifyCanonical is H6: an independent re-derivation of the in-place binary's
// digest through the new agent's Go implementation.
func (r *Runner) VerifyCanonical(ctx context.Context, canonical, expected string) error {
	if err := r.requireTransport(); err != nil {
		return err
	}
	hash, err := r.T.FsHash(ctx, canonical)
	if err != nil {
		return stepErr("H6", CodeWriteFailed, err)
	}
	if !strings.EqualFold(hash.SHA256, expected) {
		return stepErr("H6", CodeDigestMismatch,
			fmt.Errorf("expected %s actual %s", ShortDigest(expected), ShortDigest(hash.SHA256)))
	}
	return nil
}

// decodeJSONLine returns the last JSON object printed on stdout. The scripts
// print exactly one line, but a noisy shell banner must not break the parse.
func decodeJSONLine[T any](stdout string) (T, error) {
	var out T
	text := strings.ReplaceAll(stdout, "\r\n", "\n")
	lines := strings.Split(text, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "{") {
			continue
		}
		if err := json.Unmarshal([]byte(line), &out); err == nil {
			return out, nil
		}
	}
	return out, fmt.Errorf("no JSON object in output")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
