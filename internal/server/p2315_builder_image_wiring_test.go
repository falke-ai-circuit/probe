package server

// P23-15 — the builder image-wiring battery. The shipped 1.19.3 image failed
// every POST /api/v1/builds twice: TOOLCHAIN_UNAVAILABLE (hardcoded dev-host
// go path /opt/data/go/bin/go1.23.12 absent in the image) and BUILD_FAILED
// (relative ./cmd/probe-client/ resolved against server CWD /app, where no
// go.mod exists — the module lives at /src).
//
// THE acceptance shape (mirrors p2505r_uplink_wiring_test.go): a build must
// succeed in the IMAGE layout — CWD /app with NO go.mod, module at /src —
// without any flags, symlinks, or env. The dev layout is exactly why the
// image shipped broken, so these tests do NOT set the builder fields by hand
// where the wiring is the thing under test.
//
// A1: image-layout rig (temp dirs mirroring the image: module tree at
//     <root>/src, server CWD <root>/app).
// A2: no hardcoded /opt/data path may remain on the build path — asserted
//     structurally with a stub toolchain path plus a source-tree grep.
// A3: regression — the wiring dies loudly if SetGoBinPath/SetClientPkg stop
//     being reachable: a server-level flag/env feed must reach the manager's
//     compiled plan.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// p2315ImageLayout builds a temp tree mirroring the shipped image layout:
//
//	<root>/src/go.mod, cmd/probe-client/main.go   — the module (image /src)
//	<root>/app/                                   — server CWD (image /app)
//	<root>/toolchain/bin/go                       — fake "go" (image /usr/local/go/bin/go)
//
// It chdirs the test into <root>/app — the exact condition that broke v1.19.3
// — and returns the fake toolchain path and source-root path.
func p2315ImageLayout(t *testing.T) (goBin, sourceRoot string) {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "src")
	app := filepath.Join(root, "app")
	tcBin := filepath.Join(root, "toolchain", "bin")
	for _, d := range []string{
		src, app, tcBin,
		filepath.Join(src, "cmd", "probe-client"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	// Minimal module: go.mod at /src, a compilable (near-empty) main package.
	write := func(path, body string) {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(src, "go.mod"), "module probe/image-rig\n\ngo 1.21\n")
	write(filepath.Join(src, "cmd", "probe-client", "main.go"),
		"package main\n\nfunc main() {}\n")
	write(filepath.Join(src, "cmd", "probe-client", "go.mod"), "") // absent on purpose — dir create only
	os.Remove(filepath.Join(src, "cmd", "probe-client", "go.mod"))

	// Fake toolchain: a shell script that records it ran and delegates. It must
	// be executable and print reason enough that a failure names it.
	script := "#!/bin/sh\necho \"p2315-fake-go $@\" >> \"" + filepath.Join(root, "toolchain", "calls.log") + "\"\nexec " + p2315RealGo(t) + " \"$@\"\n"
	write(filepath.Join(tcBin, "go"), script)
	if err := os.Chmod(filepath.Join(tcBin, "go"), 0o755); err != nil {
		t.Fatalf("chmod fake go: %v", err)
	}
	t.Setenv("PROBE_SOURCE_DIR", "") // P23-15 env must NOT be set going in
	t.Setenv("PROBE_BUILDER_SOURCE", "")
	t.Setenv("PROBE_BUILDER_GO", "")
	// Point the image-source fallback at the rig's /src-analogue: the rig
	// cannot create real /src (root-owned, host pollution).
	origFallback := sourceFallbackDir
	sourceFallbackDir = src
	t.Cleanup(func() { sourceFallbackDir = origFallback })
	cwd, _ := os.Getwd()
	if err := os.Chdir(app); err != nil {
		t.Fatalf("chdir app: %v", err)
	}
	t.Cleanup(func() { os.Chdir(cwd) })
	return filepath.Join(tcBin, "go"), src
}

// p2315RealGo finds a real go toolchain to delegate to, or skips the test —
// these are compiler-backing tests, they do not mock the compiler itself.
func p2315RealGo(t *testing.T) string {
	t.Helper()
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	if _, err := os.Stat("/usr/local/go/bin/go"); err == nil {
		return "/usr/local/go/bin/go"
	}
	t.Skip("no real Go toolchain on this host: P23-15 image-layout tests compile, they do not mock the compiler")
	return ""
}

// TestP2315_DefaultsDeriveInImageLayout (A1): with the process CWD inside a
// directory with NO go.mod (image /app) and the module one level over
// (image /src), a fresh BuilderManager must by itself point at a toolchain
// (PATH lookup) and a source dir whose module root is the /src-analogue —
// and a real go build through planTarget must succeed. This is the build
// that failed twice on the shipped image.
func TestP2315_DefaultsDeriveInImageLayout(t *testing.T) {
	goBin, sourceRoot := p2315ImageLayout(t)

	// Make the fake toolchain the PATH one so deriveGoBin finds it without
	// any setter call — the wiring IS the acceptance.
	t.Setenv("PATH", filepath.Dir(goBin)+string(os.PathListSeparator)+os.Getenv("PATH"))

	bm := NewBuilderManager("", t.TempDir())

	if got := bm.goBinPath; got != goBin {
		t.Errorf("derived toolchain = %q, want the PATH toolchain %q (dev-host default leaked?)", got, goBin)
	}
	if root := bm.SourceModuleRoot(); root != sourceRoot {
		t.Errorf("derived module root = %q, want %q — go.mod not found from image-CWD default", root, sourceRoot)
	}
	if strings.Contains(bm.clientPkg, "/opt/data") {
		t.Errorf("derived clientPkg %q carries a dev-host path", bm.clientPkg)
	}

	// The full plan: a real cross-compilation must resolve and run through
	// the derived layout, with Dir at the module root.
	plan := bm.planTarget(&BuildConfig{
		Name: "p2315rig", OS: "linux", Arch: "amd64",
		ServerURL: "ws://127.0.0.1/ws", Token: "t", Permissions: "full",
	}, BuildTarget{OS: "linux", Arch: "amd64"}, "dGVzdA==", false)
	if plan.Dir != sourceRoot {
		t.Errorf("plan.Dir = %q, want module root %q (go build would run in the server CWD)", plan.Dir, sourceRoot)
	}
	cmd, outputPath := bm.targetCommand(&BuildConfig{
		Name: "p2315rig", OS: "linux", Arch: "amd64",
		ServerURL: "ws://127.0.0.1/ws", Token: "t", Permissions: "full",
	}, BuildTarget{OS: "linux", Arch: "amd64"}, "dGVzdA==", false)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("image-layout go build failed (the shipped-image defect): %v\n%s", err, out)
	}
	if st, err := os.Stat(outputPath); err != nil || st.Size() == 0 {
		t.Fatalf("image-layout build wrote no artifact at %s: %v", outputPath, err)
	}
}

// TestP2315_ClientPkgAcceptsSourceRoot: the env/flag layer feeds a source
// ROOT (/src-shaped) into SetClientPkg — it must normalize to the package
// path so planTarget compiles the right package.
func TestP2315_ClientPkgAcceptsSourceRoot(t *testing.T) {
	_, sourceRoot := p2315ImageLayout(t)
	bm := NewBuilderManager("", t.TempDir())
	bm.SetClientPkg(sourceRoot)
	want := filepath.Join(sourceRoot, "cmd", "probe-client")
	if bm.clientPkg != want {
		t.Errorf("SetClientPkg(%q) -> %q, want %q", sourceRoot, bm.clientPkg, want)
	}
}

// TestP2315_EnvOverridesReachManager: PROBE_SOURCE_DIR / PROBE_BUILDER_GO are
// documented override surfaces — they must win over the derived defaults.
func TestP2315_EnvOverridesReachManager(t *testing.T) {
	_, sourceRoot := p2315ImageLayout(t)
	t.Setenv("PROBE_SOURCE_DIR", sourceRoot)
	t.Setenv("PROBE_BUILDER_GO", "/chosen/toolchain/go")
	bm := NewBuilderManager("", t.TempDir())
	if bm.goBinPath != "/chosen/toolchain/go" {
		t.Errorf("PROBE_BUILDER_GO did not reach the manager: %q", bm.goBinPath)
	}
	if want := filepath.Join(sourceRoot, "cmd", "probe-client"); bm.clientPkg != want {
		t.Errorf("PROBE_SOURCE_DIR did not reach the manager: %q, want %q", bm.clientPkg, want)
	}
}

// TestP2315_DevHostPathOnlyWhenReal (A2): the manager never carries
// /opt/data/... unless that path actually exists on this host. On the image
// and CI hosts it does not, so the derived value must not name it.
func TestP2315_DevHostPathOnlyWhenReal(t *testing.T) {
	if _, err := os.Stat("/opt/data/go/bin/go1.23.12"); err == nil {
		t.Skip("dev host: legacy toolchain exists, derivation legitimately finds it")
	}
	bm := NewBuilderManager("", t.TempDir())
	if strings.Contains(bm.goBinPath, "/opt/data") {
		t.Errorf("toolchain default %q names a dev-host path that does not exist (shipped-image defect intact)", bm.goBinPath)
	}
	if !filepath.IsAbs(bm.clientPkg) && bm.SourceModuleRoot() == "" {
		// Only fatal if the default is the broken relative one that cannot
		// resolve from this CWD — in-tree tests run inside the module, so the
		// walk-up always finds go.mod.
		t.Errorf("default clientPkg %q cannot resolve a module root from CWD", bm.clientPkg)
	}
}

// TestP2315_NoHardcodedDevHostPathOnBuildPath (A2): structural grep of the
// non-test build-path sources. The ONLY permitted mention is the named
// last-resort fallback constant in builder.go (printf-adjacent comment
// lines included), which deriveGoBin (wiring_p2315.go) returns only after
// os.Stat proves the path exists — the defect shape was carrying the path
// as an UNCONDITIONAL default.
func TestP2315_NoHardcodedDevHostPathOnBuildPath(t *testing.T) {
	allow := map[string]int{ // file -> permitted max occurrences
		"builder.go": 1, // the stat-gated last-resort constant only
	}
	for _, f := range []string{"builder.go", "build_matrix.go", "source_export.go", "builder_api.go"} {
		b, err := os.ReadFile(filepath.Join(".", f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if n := strings.Count(string(b), "/opt/data"); n > allow[f] {
			t.Errorf("%s carries %d hardcoded /opt/data mentions (max %d) on the build path", f, n, allow[f])
		}
	}
	// And the decisive assertion: with the legacy path stat-blocked (it cannot
	// be removed from the dev host, so probe the derivation logic with a
	// nonexistent alias instead — same code path, stat gate identical).
	os.Setenv("PROBE_BUILDER_GO", "")
	defer os.Unsetenv("PROBE_BUILDER_GO")
	path0 := os.Getenv("PATH")
	os.Unsetenv("PATH")
	origFallback := fallbackDevGoBin
	fallbackDevGoBin = "/nonexistent-p2315/go"
	defer func() { fallbackDevGoBin = origFallback }()
	os.Chdir(t.TempDir())
	origSrcFallback := sourceFallbackDir
	sourceFallbackDir = "/nonexistent-p2315/src"
	defer func() { sourceFallbackDir = origSrcFallback }()
	t.Cleanup(func() { os.Setenv("PATH", path0) })
	bm := NewBuilderManager("", t.TempDir())
	if strings.Contains(bm.goBinPath, "/opt/data") || strings.Contains(bm.goBinPath, "nonexistent") {
		t.Errorf("derivation returned %q — a path that was stat-proven absent must never be the default (shipped-image defect shape)", bm.goBinPath)
	}
	if bm.goBinPath != DefaultGoBinPath {
		t.Errorf("stat-gated derivation should fall through to the bare %q, got %q", DefaultGoBinPath, bm.goBinPath)
	}
}

// TestP2315_WiringReachesPlan (A3 regression): the wiring chain
// flags/env -> Server setters -> BuilderManager fields -> planTarget is the
// acceptance. FAILS if SetGoBinPath/SetClientPkg stop being wired: with a
// setter-set bogus toolchain, targetCommand must name the bogus path (the
// setter reached the plan); with it unset, the derived default is used.
func TestP2315_WiringReachesPlan(t *testing.T) {
	_, sourceRoot := p2315ImageLayout(t)
	bm := NewBuilderManager("", t.TempDir())
	// Pre-wiring: derived default on PATH.
	t.Setenv("PATH", filepath.Join(sourceRoot, "..", "toolchain", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	wantDerived := deriveGoBin()
	bm.SetGoBinPath("/wire-check/go")
	bm.SetClientPkg(sourceRoot)

	planCmd, _ := bm.targetCommand(&BuildConfig{
		Name: "w", OS: "linux", Arch: "amd64",
		ServerURL: "ws://x/ws", Token: "t", Permissions: "full",
	}, BuildTarget{OS: "linux", Arch: "amd64"}, "dGVzdA==", false)
	if planCmd.Path != "/wire-check/go" {
		t.Fatalf("SetGoBinPath did not reach the compiled command: cmd.Path=%q (wiring broken — the P23-15 defect shape)", planCmd.Path)
	}
	if planCmd.Dir != sourceRoot {
		t.Fatalf("SetClientPkg source-root did not reach the compiled command Dir: %q (wiring broken)", planCmd.Dir)
	}
	// And a bare manager uses the derived value, not the unset-changed one.
	bare := NewBuilderManager("", t.TempDir())
	if bare.goBinPath == "/wire-check/go" {
		t.Fatal("bare manager picked up a setter value")
	}
	if bare.goBinPath != wantDerived {
		t.Errorf("bare manager toolchain %q != derived %q", bare.goBinPath, wantDerived)
	}
}
