package server

// P11-01 acceptance tests — build matrix.
//
// The three load-bearing properties, each asserted against the bytes on disk:
//   1. ONE build operation emits an artifact for every requested target.
//   2. Every reported sha256 equals a fresh hash of the file it names.
//   3. The version reported for an artifact was read OUT of the artifact and
//      equals the version constant — and an artifact with no stamp is refused
//      rather than reported with a default.
// Plus the loud-failure property: a target that produced nothing fails the
// operation; it is never dropped from the list.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/version"
)

// matrixGoBin resolves a toolchain for the real-build tests, skipping (loudly)
// when the host has none: these tests compile, they do not mock.
func matrixGoBin(t *testing.T) string {
	t.Helper()
	candidates := []string{"/opt/data/go/bin/go1.23.12"}
	if p, err := exec.LookPath("go"); err == nil {
		candidates = append(candidates, p)
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	t.Skip("no Go toolchain on this host: build-matrix tests need a real compiler")
	return ""
}

// freshSHA256 hashes a file directly in the test, independent of the server's
// own hashing code — the cross-check has to be a second opinion, not a
// restatement.
func freshSHA256(t *testing.T, path string) (string, int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), n
}

// TestBuildMatrix is the unit's acceptance test: one operation, all targets,
// every field read back from the artifact.
func TestBuildMatrix(t *testing.T) {
	dir := t.TempDir()
	bm := NewBuilderManager("", dir)
	bm.SetGoBinPath(matrixGoBin(t))

	cfg := &BuildConfig{
		ID:           "build-p1101",
		Name:         "matrix-agent",
		OS:           "linux",
		Arch:         "amd64",
		Targets:      []BuildTarget{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}, {OS: "windows", Arch: "amd64"}},
		Capabilities: []string{"exec", "filesystem"},
		ServerURL:    "ws://127.0.0.1:7700/ws",
		Token:        "matrix-test-token",
		Permissions:  "full",
	}

	artifacts, refusals, err := bm.buildAll(cfg, "dGVzdC1jb25maWc=")
	if err != nil {
		t.Fatalf("one build operation over %d targets: %v (refusals: %+v)", len(cfg.Targets), err, refusals)
	}
	if len(refusals) != 0 {
		t.Fatalf("successful matrix carries %d refusals", len(refusals))
	}
	t.Logf("one buildAll() emitted %d artifacts for %d targets", len(artifacts), len(cfg.Targets))

	t.Run("EmitsEveryTarget", func(t *testing.T) {
		if len(artifacts) != len(cfg.Targets) {
			t.Fatalf("%d artifacts for %d targets — a target was omitted", len(artifacts), len(cfg.Targets))
		}
		want := []string{"linux/amd64", "linux/arm64", "windows/amd64"}
		for i, a := range artifacts {
			if got := a.OS + "/" + a.Arch; got != want[i] {
				t.Errorf("artifact %d is %s, want %s", i, got, want[i])
			}
			info, serr := os.Stat(a.Path)
			if serr != nil {
				t.Fatalf("artifact %s missing on disk: %v", a.Path, serr)
			}
			if info.Size() == 0 {
				t.Errorf("artifact %s is empty", a.Path)
			}
			if !strings.Contains(a.Filename, a.OS+"-"+a.Arch) {
				t.Errorf("artifact filename %q does not name its platform", a.Filename)
			}
			t.Logf("artifact %s version=%s sha256=%s size=%d path=%s", a.OS+"/"+a.Arch, a.Version, a.SHA256, a.Size, a.Path)
		}
		// windows/amd64 is built here and stays UNPROVEN-ON-HOST: this test
		// asserts a PE artifact exists and carries PE build info. It makes no
		// claim about running it.
	})

	t.Run("ReportedSHA256MatchesFile", func(t *testing.T) {
		for _, a := range artifacts {
			fresh, size := freshSHA256(t, a.Path)
			if fresh != a.SHA256 {
				t.Errorf("%s: reported sha256 %s != fresh hash %s", a.OS+"/"+a.Arch, a.SHA256, fresh)
			}
			if size != a.Size {
				t.Errorf("%s: reported size %d != file size %d", a.OS+"/"+a.Arch, a.Size, size)
			}
			t.Logf("%s sha256 cross-check OK: %s (%d bytes)", a.OS+"/"+a.Arch, fresh, size)
		}
	})

	t.Run("ReportedVersionComesFromArtifact", func(t *testing.T) {
		for _, a := range artifacts {
			if a.Version != version.Wire() {
				t.Errorf("%s: version parsed from the artifact is %q, version constant is %q", a.OS+"/"+a.Arch, a.Version, version.Wire())
			}
			t.Logf("%s version from bytes = %s (constant %s)", a.OS+"/"+a.Arch, a.Version, version.Wire())
		}
	})

	t.Run("ArtifactsAreDistinctFiles", func(t *testing.T) {
		seen := map[string]bool{}
		for _, a := range artifacts {
			if seen[a.Path] {
				t.Errorf("two targets share the artifact path %s", a.Path)
			}
			seen[a.Path] = true
		}
	})
}

// TestBuildMatrixMissingTargetFailsLoud proves the negative: a target whose
// compile claims success but writes no file fails the whole operation and
// names the target. Nothing is silently omitted.
func TestBuildMatrixMissingTargetFailsLoud(t *testing.T) {
	bm := NewBuilderManager("", t.TempDir())

	orig := buildRunner
	buildRunner = func(*exec.Cmd) ([]byte, error) {
		return []byte("looks fine"), nil // exit 0, writes nothing
	}
	defer func() { buildRunner = orig }()

	cfg := &BuildConfig{
		ID:      "build-p1101-stub",
		Name:    "stub-agent",
		OS:      "linux",
		Arch:    "amd64",
		Targets: []BuildTarget{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}, {OS: "windows", Arch: "amd64"}},
	}

	artifacts, refusals, err := bm.buildAll(cfg, "dGVzdA==")
	if err == nil {
		t.Fatal("buildAll reported success although no artifact was written")
	}
	if artifacts != nil {
		t.Errorf("buildAll returned %d artifacts alongside an error — a partial matrix is not allowed", len(artifacts))
	}
	if len(refusals) == 0 {
		t.Error("failed matrix carries no typed refusal")
	}
	if !strings.Contains(err.Error(), "linux/amd64") {
		t.Errorf("error does not name the failing target: %v", err)
	}
	if !strings.Contains(err.Error(), "no artifact") {
		t.Errorf("error does not say the artifact is missing: %v", err)
	}
	t.Logf("loud failure: %v", err)
}

// TestBuildMatrixFailedTargetMarksRecordFailed checks the loud failure reaches
// the API-visible record: status failed, the error names the target, and no
// artifacts/binary_path are published.
func TestBuildMatrixFailedTargetMarksRecordFailed(t *testing.T) {
	bm := NewBuilderManager("", t.TempDir())

	orig := buildRunner
	buildRunner = func(*exec.Cmd) ([]byte, error) {
		return []byte("simulated linker failure"), errors.New("exit status 1")
	}
	defer func() { buildRunner = orig }()

	build, err := bm.CreateBuild(&BuildConfig{
		Name:         "fail-agent",
		OS:           "linux",
		Arch:         "amd64",
		Targets:      []BuildTarget{{OS: "linux", Arch: "amd64"}, {OS: "linux", Arch: "arm64"}, {OS: "windows", Arch: "amd64"}},
		Capabilities: []string{"exec"},
		ServerURL:    "ws://127.0.0.1:7700/ws",
		Token:        "tok",
	})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := bm.GetBuild(build.ID)
		if got == nil {
			t.Fatal("build record vanished")
		}
		if got.Status == BuildStatusFailed {
			if !strings.Contains(got.Error, "target linux/amd64") {
				t.Errorf("record error does not name the failing target: %q", got.Error)
			}
			if len(got.Artifacts) != 0 {
				t.Errorf("failed matrix published %d artifacts", len(got.Artifacts))
			}
			if got.BinaryPath != "" {
				t.Errorf("failed matrix published binary_path %q", got.BinaryPath)
			}
			t.Logf("record failed loudly: %s", got.Error)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("build never reached failed")
}

// TestBuildMatrixRejectsUnknownTarget: an unbuildable platform is refused at
// validation, before any compile.
func TestBuildMatrixRejectsUnknownTarget(t *testing.T) {
	bm := NewBuilderManager("", t.TempDir())
	cases := []struct {
		name   string
		target BuildTarget
		want   string
	}{
		{"unknown os", BuildTarget{OS: "plan9", Arch: "amd64"}, "target 1 (plan9/amd64): invalid os"},
		{"unknown arch", BuildTarget{OS: "linux", Arch: "mips"}, "target 1 (linux/mips): invalid arch"},
		{"empty arch", BuildTarget{OS: "linux"}, "arch is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bm.CreateBuild(&BuildConfig{
				Name:      "target-agent",
				Targets:   []BuildTarget{tc.target},
				ServerURL: "ws://h/ws",
				Token:     "tok",
			})
			if err == nil {
				t.Fatalf("target %s was accepted", tc.target)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.want)
			}
		})
	}
}

// TestReadArtifactMetadataRejectsUnstampedFile proves there is no hand-label
// fallback: a file that is not a stamped Go binary yields an error, not the
// version constant.
func TestReadArtifactMetadataRejectsUnstampedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-binary")
	if err := os.WriteFile(path, []byte("this is not a Go executable\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	art, err := readArtifactMetadata(path)
	if err == nil {
		t.Fatalf("unstamped file was accepted with version=%q", art.Version)
	}
	if art.Version != "" {
		t.Errorf("error path returned a version %q — that would be a hand-label", art.Version)
	}
	if !strings.Contains(err.Error(), "build info") {
		t.Errorf("error does not explain the missing build info: %v", err)
	}
	t.Logf("unstamped artifact refused: %v", err)
}

// TestBuildMatrixKeepsV118ClientContract pins the additive promise: a v1.18.x
// request (os/arch only) still produces the v1.18.x JSON shape and file name,
// and a v1.18.x record still decodes.
func TestBuildMatrixKeepsV118ClientContract(t *testing.T) {
	bm := NewBuilderManager("", t.TempDir())

	// 1. A v1.18.x record serialises without any of the new keys.
	raw, err := json.Marshal(BuildConfig{
		ID: "build-v118", Name: "legacy", OS: "linux", Arch: "amd64",
		ServerURL: "ws://h/ws", Token: "tok", Status: BuildStatusPending,
	})
	if err != nil {
		t.Fatalf("marshal v1 record: %v", err)
	}
	for _, key := range []string{`"targets"`, `"artifacts"`, `"matrix_version"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("v1 response carries the additive key %s: %s", key, raw)
		}
	}

	// 2. A v1.18.x record on disk (no new keys) still decodes.
	var back BuildConfig
	if err := json.Unmarshal([]byte(`{"id":"build-v118","name":"legacy","os":"linux","arch":"amd64","binary_path":"/data/files/x","status":"complete"}`), &back); err != nil {
		t.Fatalf("unmarshal v1.18.x record: %v", err)
	}
	if back.Targets != nil || back.Artifacts != nil || back.MatrixVersion != 0 {
		t.Errorf("v1.18.x record decoded with matrix fields: %+v", back)
	}

	// 3. A single-target build (no Targets) keeps the exact v1 file name.
	cmdStr := bm.BuildCommandString(&BuildConfig{ID: "build-abc123", Name: "my-agent", OS: "windows", Arch: "amd64"}, "dGVzdA==")
	if !strings.Contains(cmdStr, "build-abc123_my-agent.exe") {
		t.Errorf("single-target build name drifted from the v1.18.x shape: %s", cmdStr)
	}
	if strings.Contains(cmdStr, "windows-amd64.exe") {
		t.Errorf("single-target build gained a platform suffix: %s", cmdStr)
	}
	// 4. The plan that runs is the plan that is printed: same output path.
	plan := bm.planTarget(&BuildConfig{ID: "build-abc123", Name: "my-agent", OS: "windows", Arch: "amd64"}, BuildTarget{OS: "windows", Arch: "amd64"}, "dGVzdA==", false)
	if !strings.Contains(cmdStr, plan.Output) {
		t.Errorf("BuildCommandString omits the executed output path %s: %s", plan.Output, cmdStr)
	}
}
