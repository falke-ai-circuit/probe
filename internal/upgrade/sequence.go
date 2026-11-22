package upgrade

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	// ChunkSize bounds one fs-write body. Zero means DefaultChunkSize; the
	// value is clamped to MaxChunkSize, so a caller cannot reintroduce the
	// single 8 MB body that crashed the server.
	ChunkSize int
	// NoResume disables resuming at the last chunk boundary after a dropped
	// call. Resume is the default: a restart from zero re-uploads the whole
	// artifact for one failed chunk.
	NoResume bool
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
	// offset>0 branch seeks an existing file. A dropped call resumes at the
	// last whole-chunk boundary; a proven-bad chunk is a refusal.
	chunks, err := r.writePayload(ctx, staged, req.Payload, true)
	if err != nil {
		if IsPermissionDenied(err) {
			return out, stepErr("SW-02", CodeSandboxRefused, err)
		}
		// One replay from offset 0: a dropped call is repaired by resuming,
		// but a chunk whose read-back lied is only trusted after a full
		// rewrite proves the carrier can carry it.
		r.logf("SW-02: write failed (%v), replaying from offset 0", err)
		chunks, err = r.writePayload(ctx, staged, req.Payload, false)
		if err != nil {
			if errors.Is(err, ErrChunkDigestMismatch) {
				return out, stepErr("SW-02", CodeDigestMismatch, err)
			}
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
		if _, rerr := r.writePayload(ctx, staged, req.Payload, false); rerr != nil {
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

	// SW-04B: the version comes from the artifact, never from the build label.
	// The staged binary is already digest-proven and executable, so this read
	// answers for exactly the bytes that would be swapped in. A mismatch stops
	// the sequence before any script or manifest is armed.
	if manifest.Version != "" {
		// Only probed when the manifest states the expected version. Callers
		// that predate the version field keep their exact primitive budget;
		// a caller that pins a version gets the refusal.
		if _, verr := r.VerifyArtifactVersion(ctx, staged, manifest.Version); verr != nil {
			return out, verr
		}
	}

	// SW-05: arm the commit script and the manifest beside the payload.
	//
	// Integrity map for every write this sequence performs (A4 — fs-write
	// returns Written = len(data) it was handed, never a digest, so a write's
	// own report is not evidence):
	//   payload chunks   -> SW-03: fs-hash vs the caller's digest, replay once
	//   boot script      -> writeVerified here, then SW-06 re-hashes it against
	//                       SHA256Boot (V1), the authority the commit script
	//                       will also check at SW-10
	//   manifest         -> writeVerified here; the proven digest is recorded as V4
	//   post-rename file -> SW-15 inside p2boot.ps1: the only check of the bytes
	//                       actually in place. Not repeated here.
	// SW-07 executes the script only after SW-06 has read it back from the host,
	// so nothing is executed that was not verified after landing.
	bootPath := JoinWinPath(dir, BootScriptFile)
	manifestPath := JoinWinPath(dir, ManifestFile)
	if _, err := r.writeVerified(ctx, "SW-05", bootPath, BootScript(), "0644"); err != nil {
		return out, err
	}
	manifestBytes, err := manifest.Marshal()
	if err != nil {
		return out, err
	}
	manifestDigest, err := r.writeVerified(ctx, "SW-05", manifestPath, manifestBytes, "0644")
	if err != nil {
		return out, err
	}

	// SW-06: V1 for the script, V4 for the manifest (the manifest cannot carry
	// its own digest). The boot script is re-hashed under the same step because
	// SHA256Boot is the value SW-10 compares against on the host.
	bootHash, err := r.T.FsHash(ctx, bootPath)
	if err != nil {
		return out, stepErr("SW-06", CodeWriteFailed, err)
	}
	if !strings.EqualFold(bootHash.SHA256, bootDigest) {
		return out, stepErr("SW-06", CodeDigestMismatch,
			fmt.Errorf("sha256_boot expected %s actual %s", ShortDigest(bootDigest), ShortDigest(bootHash.SHA256)))
	}

	out = StageResult{
		Canonical:      canonical,
		StagedPath:     staged,
		BootPath:       bootPath,
		ManifestPath:   manifestPath,
		PayloadSHA256:  payloadDigest,
		BootSHA256:     bootDigest,
		ManifestSHA256: manifestDigest,
		Chunks:         chunks,
	}
	return out, nil
}

// writeVerified performs one fs-write and proves the bytes that landed: the
// file is hashed back and compared to the digest of the bytes passed in. A4
// makes the write's own result insufficient — Written is len(data) it was
// handed, not what reached the disk — so every artefact this sequence arms goes
// through here. The post-rename image is the one write this sequence does not
// verify itself: SW-15 covers it inside the commit script.
func (r *Runner) writeVerified(ctx context.Context, step, path string, data []byte, mode string) (string, error) {
	want := SHA256Hex(data)
	res, err := r.T.FsWrite(ctx, WriteRequest{Path: path, Data: data, Mode: mode})
	if err != nil {
		return "", stepErr(step, CodeWriteFailed, err)
	}
	if res.Written != len(data) {
		return "", stepErr(step, CodeWriteFailed,
			fmt.Errorf("%s: wrote %d of %d bytes", path, res.Written, len(data)))
	}
	got, err := r.T.FsHash(ctx, path)
	if err != nil {
		return "", stepErr(step, CodeWriteFailed, err)
	}
	if !strings.EqualFold(got.SHA256, want) {
		return "", stepErr(step, CodeDigestMismatch,
			fmt.Errorf("%s: expected %s actual %s", path, ShortDigest(want), ShortDigest(got.SHA256)))
	}
	return want, nil
}

func (r *Runner) writePayload(ctx context.Context, path string, payload []byte, resume bool) (int, error) {
	cs := r.chunkSize()
	offset := 0
	if resume && !r.NoResume {
		// Resume rule: the file's size, floored to a chunk boundary, is the
		// last offset whose write was fully acknowledged. A fragment past
		// the boundary is discarded by rewriting that chunk.
		if st, err := r.T.FsStat(ctx, path); err == nil && st.Exists && !st.IsDir &&
			st.Size > 0 && st.Size < int64(len(payload)) {
			offset = int(st.Size/int64(cs)) * cs
			if offset > 0 {
				r.logf("SW-02: resuming at offset %d (%d bytes already on disk)", offset, st.Size)
			}
		}
	}
	chunks := 0
	stalls := 0
	for offset < len(payload) {
		end := offset + cs
		if end > len(payload) {
			end = len(payload)
		}
		mode := "0644"
		if offset == 0 {
			// The staged artifact must be runnable for the SW-04B
			// self-reported version read; on Windows the mode is ignored.
			mode = "0755"
		}
		res, err := r.T.FsWrite(ctx, WriteRequest{
			Path:   path,
			Data:   payload[offset:end],
			Offset: offset,
			Mode:   mode,
		})
		if err != nil || res.Written != end-offset {
			if err == nil {
				err = fmt.Errorf("chunk at offset %d: wrote %d of %d bytes", offset, res.Written, end-offset)
			}
			// One resume attempt per stall: continue at the last whole-chunk
			// boundary rather than restarting the artifact from zero.
			stalls++
			if r.NoResume || stalls > 2 {
				return chunks, err
			}
			st, serr := r.T.FsStat(ctx, path)
			if serr != nil || !st.Exists {
				return chunks, err
			}
			next := int(st.Size/int64(cs)) * cs
			if next > len(payload) {
				next = len(payload)
			}
			r.logf("SW-02: write failed (%v), resuming at offset %d", err, next)
			offset = next
			continue
		}
		stalls = 0
		// Every chunk is verified by offset: read the range back and compare
		// its digest with the bytes that were handed to the agent.
		back, rerr := r.T.FsRead(ctx, ReadRequest{Path: path, Offset: offset, Limit: end - offset})
		if rerr != nil {
			return chunks, rerr
		}
		if !strings.EqualFold(SHA256Hex(back.Data), SHA256Hex(payload[offset:end])) {
			return chunks, fmt.Errorf("%w: chunk at offset %d failed read-back verification (%d bytes)",
				ErrChunkDigestMismatch, offset, len(back.Data))
		}
		chunks++
		offset = end
	}
	return chunks, nil
}

// ErrChunkDigestMismatch marks a chunk whose read-back bytes do not match what
// was sent. It is a refusal (the carrier lied), not a dropped call, so the
// sequence must not quietly resume past it.
var ErrChunkDigestMismatch = errors.New("chunk digest mismatch")

// chunkSize resolves the bounded carrier chunk.
func (r *Runner) chunkSize() int {
	cs := r.ChunkSize
	if cs <= 0 {
		cs = DefaultChunkSize
	}
	if cs > MaxChunkSize {
		cs = MaxChunkSize
	}
	return cs
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
