package upgrade

import (
	"context"
	"fmt"
	"strings"
)

// P8-11 gap 2 — the version comes from the ARTIFACT, never from the build
// label. The updater runs the newly-staged binary with --version and compares
// that answer with the manifest before anything is parked or swapped. A build
// label can be stale, copied, or forged; only the bytes that will run know.

// ArtifactVersionCommand returns the exec command that asks an artifact for its
// own version. The command carries the path inline because --version takes no
// typed parameter; the path is the only variable and it is quoted.
func ArtifactVersionCommand(path string) string {
	if IsAbsWinPath(path) {
		return fmt.Sprintf(`powershell -NoProfile -ExecutionPolicy Bypass -Command "& { & '%s' --version | Out-Host }"`, path)
	}
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "' --version"
}

// ParseArtifactVersion takes the first non-empty stdout line and returns the
// bare semver. `probe-client --version` prints exactly one line ("1.19.0") and
// exits 0; anything else is not a version and the caller refuses.
func ParseArtifactVersion(stdout string) (string, error) {
	text := strings.ReplaceAll(stdout, "\r\n", "\n")
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		v := strings.TrimPrefix(line, "v")
		if !looksLikeSemver(v) {
			return "", fmt.Errorf("artifact printed %q, not a bare semver", line)
		}
		return v, nil
	}
	return "", fmt.Errorf("artifact printed no version")
}

// looksLikeSemver accepts MAJOR.MINOR.PATCH with an optional suffix; it is a
// shape check so a banner ("PROBE Client v1.18.6") is never mistaken for the
// version contract.
func looksLikeSemver(v string) bool {
	parts := strings.SplitN(v, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	for _, r := range parts[0] + parts[1] {
		if r < '0' || r > '9' {
			return false
		}
	}
	if parts[2] == "" || parts[2][0] < '0' || parts[2][0] > '9' {
		return false
	}
	return true
}

// VerifyArtifactVersion executes the staged artifact and refuses the sequence
// when its self-reported version is not the manifest's. It runs after the
// payload's digest and size are proven (SW-03/SW-04) and before any script or
// manifest is armed, so a mismatch leaves nothing to roll back.
//
// When want is empty the read is informational: older callers that predate the
// version field keep working, and the observed version is returned so the
// caller can record it.
func (r *Runner) VerifyArtifactVersion(ctx context.Context, artifactPath, want string) (string, error) {
	if err := r.requireTransport(); err != nil {
		return "", err
	}
	res, err := r.T.Exec(ctx, ExecRequest{
		Command: ArtifactVersionCommand(artifactPath),
		Timeout: ArtifactVersionTimeoutS,
	})
	if err != nil {
		if want == "" {
			return "", nil
		}
		return "", stepErr("SW-04B", CodeVersionMismatch, err)
	}
	got, perr := ParseArtifactVersion(res.Stdout)
	if perr != nil || res.ExitCode != 0 {
		if want == "" {
			return "", nil
		}
		return "", stepErr("SW-04B", CodeVersionMismatch,
			fmt.Errorf("exit=%d stdout=%q: %v", res.ExitCode, truncate(res.Stdout, 160), perr))
	}
	if want != "" && !strings.EqualFold(got, strings.TrimPrefix(want, "v")) {
		return got, stepErr("SW-04B", CodeVersionMismatch,
			fmt.Errorf("artifact reports %s, manifest says %s", got, want))
	}
	return got, nil
}

// ArtifactVersionTimeoutS bounds the --version probe: it is a read, not a job.
const ArtifactVersionTimeoutS = 15
