package server

// P23-15 — builder image wiring. The shipped 1.19.3 image failed every
// POST /api/v1/builds twice: the default toolchain was a hardcoded DEV-HOST
// path (/opt/data/go/bin/go1.23.12 — absent in the image, where Go lives at
// /usr/local/go/bin/go), and the default client package was a RELATIVE path
// resolved against the server's CWD (/app), where no go.mod exists — the
// module ships at /src. These helpers DERIVE the build path from the live
// filesystem at BuilderManager construction:
//
//	toolchain:  explicit env PROBE_BUILDER_GO → exec.LookPath("go") →
//	            /usr/local/go/bin/go (image) → the dev-host path, only if it
//	            actually exists (last resort on a bare dev box).
//	source dir: env PROBE_BUILDER_SOURCE / PROBE_SOURCE_DIR → /src (image) →
//	            the repo-relative ./cmd/probe-client/ (dev checkout / tests).
//
// The ordering is environment-probed, not assumed: in the image PATH lookup
// finds /usr/local/go/bin/go and /src/go.mod exists, so the image defaults
// resolve without any flags; on a dev host LookPath may fail and ./cmd/
// probe-client resolves relative to CWD exactly as before. PROHIBITED and
// avoided: symlink or bind-mount workarounds.

import (
	"os"
	"os/exec"
	"path/filepath"
)

// imageGoBin is where the runtime image puts the Go toolchain (Dockerfile:
// COPY --from=builder /usr/local/go /usr/local/go).
const imageGoBin = "/usr/local/go/bin/go"

// sourceFallbackDir is the image's source module location (Dockerfile:
// COPY --from=builder /build /src). A var, not a const, ONLY so the
// image-layout test rig can point it at a temp-tree /src-analogue without
// being root; production code and the other tests never touch it.
var sourceFallbackDir = "/src"

// sourceRootCache memoises the derived source root. Deriving it repeatedly
// would re-stat the filesystem on every planTarget call.
var probeSourceRoot = ""

// deriveGoBin returns the best available toolchain path at construction time.
func deriveGoBin() string {
	if p := os.Getenv("PROBE_BUILDER_GO"); p != "" {
		return p
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	if _, err := os.Stat(imageGoBin); err == nil {
		return imageGoBin
	}
	// Last resort for the legacy dev-host layout — only when it exists.
	if _, err := os.Stat(fallbackDevGoBin); err == nil {
		return fallbackDevGoBin
	}
	// Nothing found; return the plain name so the toolchain refusal names a
	// diagnosable path rather than an empty string.
	return DefaultGoBinPath
}

// validSourceRoot reports whether dir holds (or plausibly holds) the probe
// client package: it must exist, and either the package dir itself or the
// module root above it carries go.mod. A missing dir is never valid.
func validSourceRoot(dir string) bool {
	st, err := os.Stat(filepath.Join(dir, "cmd", "probe-client"))
	if err != nil || !st.IsDir() {
		return false
	}
	// go.mod must be reachable from the package dir (the module root walk in
	// SourceModuleRoot starts there).
	d := dir
	for {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return true
		}
		parent := filepath.Dir(d)
		if parent == d {
			return false
		}
		d = parent
	}
}

// deriveSourceRoot returns the ABSOLUTE source directory the builder should
// compile from, starting from the caller-supplied candidate (the repo-relative
// default resolved against CWD, in dev/tests). Env overrides win; then the
// image layout (/src); then the candidate as-is.
func deriveSourceRoot(cand string) string {
	normalize := func(dir string) string {
		// An env/flag source ROOT (/src) becomes the package path, so
		// SourceModuleRoot and planTarget resolve from the same origin.
		clean := filepath.Clean(dir)
		if filepath.Base(clean) == "probe-client" ||
			filepath.Base(clean) == "cmd" && filepath.Base(filepath.Dir(clean)) != "" && true {
			return dir
		}
		return filepath.Join(clean, "cmd", "probe-client")
	}
	if env := os.Getenv("PROBE_BUILDER_SOURCE"); env != "" {
		return normalize(env)
	}
	if env := os.Getenv("PROBE_SOURCE_DIR"); env != "" {
		return normalize(env)
	}
	abs := cand
	if !filepath.IsAbs(abs) {
		if wd, err := os.Getwd(); err == nil {
			abs = filepath.Join(wd, abs)
		}
	}
	if validSourceRoot(abs) {
		return abs
	}
	if validSourceRoot(sourceFallbackDir) {
		return normalize(sourceFallbackDir)
	}
	// Nothing validated: hand the candidate back UNCHANGED. A relative
	// candidate keeps the historical resolution — SourceModuleRoot walks up
	// to the module root and planTarget runs go build there — which is what
	// makes bare dev checkouts and unit tests (CWD = package dir) work.
	return cand
}

// DerivedSourceRoot returns the memoised derived source root ("" before the
// first BuilderManager is constructed). Exposed for tests only.
func DerivedSourceRoot() string { return probeSourceRoot }
