package server

// P11-01 — build matrix.
//
// A build request names its targets; ONE call emits one artifact per target and
// reports metadata for each. Every reported field is READ BACK FROM THE
// ARTIFACT, never copied from the request:
//
//	os, arch  <- GOOS/GOARCH the Go linker recorded inside the file
//	version   <- the compiled value of internal/version.Semver in the file's
//	             data section (the same value `--version` prints), reached
//	             through the symbol table
//	sha256    <- a fresh digest of the file's bytes
//	size      <- the file's own size
//
// That discipline is the point of the unit. If the toolchain produced the
// wrong platform, an unstamped/stripped binary, or no file at all, the
// operation fails loudly with a typed refusal naming the target instead of
// reporting metadata that merely echoes the request back.
//
// The deployment matrix is the ratified contract's §11: Windows (wts), Linux
// (X11/headless X11/Wayland) and Android Termux. Android is a build target with
// an exec+fs deployment (§2, OD-7). darwin has a platform backend in the tree
// but no column in the ratified matrix, so a darwin target is refused by name
// (NOT_IN_PLATFORM_MATRIX) rather than emitted on an assumption.
//
// Backwards compatibility: `targets`, `artifacts`, `refusals` and
// `matrix_version` are additive, omitempty fields on BuildConfig. A v1.18.x
// client posts {os, arch} and gets exactly the v1 behaviour (one artifact, same
// file name, binary_path set); the added keys are absent from its response. A
// v1.18.x record loaded from builds.json simply has no matrix fields.

import (
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/pe"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/version"
)

const (
	// BuildMatrixVersion is the version of the artifact-reporting field set.
	// 1 is the v1.18.x shape (os, arch, binary_path, no target list); 2 adds
	// `targets`, `artifacts`, `refusals` and `matrix_version`. A v2 reader can
	// therefore tell a record produced by this builder from a v1.18.x one.
	BuildMatrixVersion = 2

	// versionStampKey is the linker symbol the builder writes the version
	// into. internal/version documents this -X override as supported; the
	// value is then read BACK out of the compiled data section, so the stamp
	// is verified against the bytes rather than trusted.
	versionStampKey = "github.com/falke-ai-circuit/probe/internal/version.Semver"

	// BuildTargetUnsupported is the refusal code for a target this build
	// cannot produce. The contract fixes refusal codes for primitives (§11)
	// and none for build targets, so this is the one name P11-01 introduces.
	BuildTargetUnsupported = "BUILD_TARGET_UNSUPPORTED"
)

// Closed reason enum for BuildTargetUnsupported. No free text reaches `reason`.
const (
	BuildReasonNotInMatrix = "NOT_IN_PLATFORM_MATRIX" // target absent from the ratified platform matrix
	BuildReasonToolchain   = "TOOLCHAIN_UNAVAILABLE"  // no go toolchain at the configured path
	BuildReasonBuildFailed = "BUILD_FAILED"           // the compiler ran and refused, or wrote nothing
	BuildReasonMetadata    = "METADATA_UNREADABLE"    // artifact produced, but its metadata cannot be read from it
)

// buildRunner executes one cross-compilation and returns its combined output.
// It is a variable so the failure paths a real toolchain cannot be made to
// produce on demand — an exit 0 that writes no binary, a linker failure on the
// third target — can be exercised deterministically in tests.
var buildRunner = func(cmd *exec.Cmd) ([]byte, error) {
	return cmd.CombinedOutput()
}

// BuildTarget names one platform pair inside a build request.
type BuildTarget struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// String renders the target as "os/arch" for logs and refusal messages.
func (t BuildTarget) String() string { return t.OS + "/" + t.Arch }

// BuildArtifact is the metadata for one emitted binary. Every field is derived
// from the file on disk by readArtifactMetadata: os/arch from the Go build info
// the toolchain embedded, version from the compiled version variable, sha256
// and size from the bytes.
type BuildArtifact struct {
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Filename string `json:"filename"`
	// Path is the server-side location. It is not published: download
	// selection is a separate concern (P11-02 owns the download host).
	Path string `json:"-"`
}

// BuildRefusal names one target that was not produced, and why. `Ran` says
// whether anything was executed for that target: false means nothing ran
// (refused before the compiler), true means the compiler or the metadata read
// ran and failed.
type BuildRefusal struct {
	Target  BuildTarget `json:"target"`
	Code    string      `json:"code"`
	Reason  string      `json:"reason"`
	Message string      `json:"message"`
	Ran     bool        `json:"ran"`
}

// ratifiedBuildMatrix is the deployment matrix of the ratified contract §11.
// Order is stable so a response is diffable run to run.
var ratifiedBuildMatrix = []BuildTarget{
	{OS: "linux", Arch: "amd64"},
	{OS: "windows", Arch: "amd64"},
	{OS: "android", Arch: "arm64"},
}

// RatifiedBuildMatrix returns a copy of the deployment matrix this build
// serves. Callers get their own slice and cannot mutate the table.
func RatifiedBuildMatrix() []BuildTarget {
	out := make([]BuildTarget, len(ratifiedBuildMatrix))
	copy(out, ratifiedBuildMatrix)
	return out
}

// matrixPlatforms lists, per platform the ratified matrix covers, the arches
// this build serves. The matrix (§11) is a platform matrix — Windows (wts),
// Linux (X11/headless X11/Wayland), Android Termux (§2, OD-7) — so an arch
// change does not leave it. darwin has a backend in the tree but no column in
// the ratified matrix, so it is absent here and a darwin target is refused.
var matrixPlatforms = map[string][]string{
	"linux":   {"amd64", "arm64", "386"},
	"windows": {"amd64", "arm64", "386"},
	"android": {"arm64"},
}

// isRatifiedTarget reports whether os/arch is a deployment platform+arch of the
// ratified matrix.
func isRatifiedTarget(t BuildTarget) bool {
	for _, arch := range matrixPlatforms[t.OS] {
		if arch == t.Arch {
			return true
		}
	}
	return false
}

// resolveRequestedTargets splits a request's targets into what will be built
// and what is refused by name before anything runs. Legacy requests (neither
// targets nor the matrix flag) return nil: the caller then builds the single
// os/arch pair exactly as before, so an existing caller's behaviour is intact.
//
// matrix=true with no explicit targets expands to the ratified matrix.
func resolveRequestedTargets(cfg *BuildConfig, matrix bool) (buildable []BuildTarget, refused []BuildRefusal, err error) {
	if len(cfg.Targets) == 0 && !matrix {
		return nil, nil, nil
	}
	want := cfg.Targets
	if len(want) == 0 {
		want = RatifiedBuildMatrix()
	}
	seen := make(map[string]bool, len(want))
	for _, t := range want {
		t.OS = strings.ToLower(strings.TrimSpace(t.OS))
		t.Arch = strings.ToLower(strings.TrimSpace(t.Arch))
		if t.OS == "" || t.Arch == "" {
			return nil, nil, fmt.Errorf("target %q must carry both os and arch", t.String())
		}
		if seen[t.String()] {
			continue
		}
		seen[t.String()] = true
		if isRatifiedTarget(t) {
			buildable = append(buildable, t)
			continue
		}
		refused = append(refused, BuildRefusal{
			Target: t,
			Code:   BuildTargetUnsupported,
			Reason: BuildReasonNotInMatrix,
			Message: fmt.Sprintf("target %s is not in the ratified platform matrix "+
				"(docs/contracts/P10-07_CONTRACT_V3_1.md §11: windows, linux, android); no artifact is produced for it. Nothing ran.", t.String()),
			Ran: false,
		})
	}
	if len(buildable) == 0 {
		return nil, refused, fmt.Errorf("no buildable target in request: %d refused", len(refused))
	}
	return buildable, refused, nil
}

// buildPlan is one resolved compilation: the command to run and where its
// artifact lands. It is the single source of the build command — planTarget
// builds it, targetCommand executes it and BuildCommandString renders it, so
// the three can never disagree about what was run.
type buildPlan struct {
	GoBin  string
	Args   []string
	Env    []string
	Dir    string
	Output string
}

// String renders the plan as an env-prefixed command line for logs and tests.
func (p buildPlan) String() string {
	parts := append([]string{}, p.Env...)
	parts = append(parts, p.GoBin)
	for i, a := range p.Args {
		if i > 0 && (p.Args[i-1] == "-tags" || p.Args[i-1] == "-ldflags") {
			parts = append(parts, `"`+a+`"`)
			continue
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

// resolvedTargets returns the ordered, de-duplicated target set for a request.
// An empty target list is the v1.18.x shape: the record's own OS/Arch pair.
// Targets outside validBuildOS/validBuildArch are rejected here, before
// anything is compiled.
func (bm *BuilderManager) resolvedTargets(build *BuildConfig) ([]BuildTarget, error) {
	raw := build.Targets
	if len(raw) == 0 {
		raw = []BuildTarget{{OS: build.OS, Arch: build.Arch}}
	}
	seen := make(map[string]bool, len(raw))
	out := make([]BuildTarget, 0, len(raw))
	for i, tgt := range raw {
		tgt.OS = strings.ToLower(strings.TrimSpace(tgt.OS))
		tgt.Arch = strings.ToLower(strings.TrimSpace(tgt.Arch))
		if tgt.OS == "" {
			return nil, fmt.Errorf("target %d: os is required", i+1)
		}
		if tgt.Arch == "" {
			return nil, fmt.Errorf("target %d (%s): arch is required", i+1, tgt.OS)
		}
		if !validBuildOS[tgt.OS] {
			return nil, fmt.Errorf("target %d (%s): invalid os %q: must be windows, linux, darwin, or android", i+1, tgt, tgt.OS)
		}
		if !validBuildArch[tgt.Arch] {
			return nil, fmt.Errorf("target %d (%s): invalid arch %q: must be amd64, 386, or arm64", i+1, tgt, tgt.Arch)
		}
		if seen[tgt.String()] {
			continue
		}
		seen[tgt.String()] = true
		out = append(out, tgt)
	}
	return out, nil
}

// targetSummary renders a target list for one log line.
func targetSummary(targets []BuildTarget) string {
	names := make([]string, 0, len(targets))
	for _, tgt := range targets {
		names = append(names, tgt.String())
	}
	return fmt.Sprintf("%d target(s): %s", len(targets), strings.Join(names, ", "))
}

// artifactFilename returns the output file name for one target. A single-target
// build keeps the v1.18.x naming exactly (`<id>_<name>`), because that name
// travels into download URLs and agent updates (P11-02 / P12 own that path).
// Only a multi-target build — a shape no v1.18.x client can request — suffixes
// the platform, so the artifacts are distinguishable on disk.
func artifactFilename(build *BuildConfig, tgt BuildTarget, multi bool) string {
	name := build.Name
	if name == "" {
		name = "probe-client"
	}
	if tgt.OS == "windows" && !strings.HasSuffix(name, ".exe") {
		name += ".exe"
	}
	if build.Disguise != nil && build.Disguise.Enabled && build.Disguise.Filename != "" {
		name = build.Disguise.Filename
	}
	if multi {
		ext := filepath.Ext(name)
		name = strings.TrimSuffix(name, ext) + "_" + tgt.OS + "-" + tgt.Arch + ext
	}
	return name
}

// planTarget resolves the compilation for one target: output path, the config
// and version stamp, tags, module root and the GOOS/GOARCH environment.
func (bm *BuilderManager) planTarget(build *BuildConfig, tgt BuildTarget, configB64 string, multi bool) buildPlan {
	bm.mu.RLock()
	goBin := bm.goBinPath
	clientPkg := bm.clientPkg
	outputDir := bm.outputDir
	bm.mu.RUnlock()

	_ = os.MkdirAll(outputDir, 0o755)

	filename := artifactFilename(build, tgt, multi)
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s", build.ID, filename))

	// -X main.configB64 embeds the agent config (zero-config replica); the
	// version stamp makes the artifact's own version explicit, and it is read
	// back out of the compiled data section afterwards.
	ldflags := "-X " + versionStampKey + "=" + version.Wire() + " -X main.configB64=" + configB64

	tagsStr := ""
	if len(build.Capabilities) > 0 {
		tagsStr = "caps=" + strings.Join(build.Capabilities, ",")
	}

	args := []string{"build"}
	if tagsStr != "" {
		args = append(args, "-tags", tagsStr)
	}
	args = append(args, "-ldflags", ldflags)

	// Resolve the module root (go.mod dir) and run go build from there, with
	// the client package as a path relative to the module root. Otherwise go
	// looks for go.mod from the server's CWD (/app) and fails.
	moduleRoot := bm.SourceModuleRoot()
	relPkg := clientPkg
	if moduleRoot != "" && filepath.IsAbs(clientPkg) {
		if r, err := filepath.Rel(moduleRoot, clientPkg); err == nil {
			relPkg = "." + string(filepath.Separator) + r
		}
	}
	args = append(args, "-o", outputPath, relPkg)

	return buildPlan{
		GoBin: goBin,
		Args:  args,
		Env: []string{
			"GOTOOLCHAIN=local",
			"CGO_ENABLED=0",
			"GOOS=" + tgt.OS,
			"GOARCH=" + tgt.Arch,
		},
		Dir:    moduleRoot,
		Output: outputPath,
	}
}

// targetCommand turns a plan into an executable command.
func (bm *BuilderManager) targetCommand(build *BuildConfig, tgt BuildTarget, configB64 string, multi bool) (*exec.Cmd, string) {
	p := bm.planTarget(build, tgt, configB64, multi)
	cmd := exec.Command(p.GoBin, p.Args...)
	if p.Dir != "" {
		cmd.Dir = p.Dir
	}
	cmd.Env = append(os.Environ(), p.Env...)
	return cmd, p.Output
}

// buildAll compiles every target in ONE operation and returns one artifact
// record per produced target plus a typed refusal for any target that could
// not be produced.
//
// It is all-or-nothing on failure: a target that fails to compile, exits
// successfully without writing a file, or writes a binary for a different
// platform aborts the matrix — the record publishes no artifacts and the
// refusal names the failing target. A matrix that quietly returns fewer
// artifacts than targets is the defect this unit closes.
func (bm *BuilderManager) buildAll(build *BuildConfig, configB64 string) ([]BuildArtifact, []BuildRefusal, error) {
	targets, err := bm.resolvedTargets(build)
	if err != nil {
		return nil, nil, err
	}
	multi := len(targets) > 1
	artifacts := make([]BuildArtifact, 0, len(targets))
	for i, tgt := range targets {
		path, refusal := bm.buildTarget(build, tgt, configB64, multi)
		if refusal != nil {
			return nil, []BuildRefusal{*refusal}, fmt.Errorf("target %s: %s", tgt, refusal.Message)
		}
		art, merr := readArtifactMetadata(path)
		if merr != nil {
			return nil, []BuildRefusal{{
				Target:  tgt,
				Code:    BuildTargetUnsupported,
				Reason:  BuildReasonMetadata,
				Message: fmt.Sprintf("%s was produced but its metadata cannot be read from it: %v", tgt, merr),
				Ran:     true,
			}}, fmt.Errorf("target %s: %w", tgt, merr)
		}
		if art.OS != tgt.OS || art.Arch != tgt.Arch {
			msg := fmt.Sprintf("%s: artifact reports %s/%s — the build environment did not apply", tgt, art.OS, art.Arch)
			return nil, []BuildRefusal{{
				Target: tgt, Code: BuildTargetUnsupported, Reason: BuildReasonMetadata, Message: msg, Ran: true,
			}}, errors.New(msg)
		}
		log.Printf("[builder] artifact %d/%d %s version=%s sha256=%s size=%d path=%s",
			i+1, len(targets), tgt, art.Version, art.SHA256, art.Size, art.Path)
		artifacts = append(artifacts, art)
	}
	return artifacts, nil, nil
}

// buildTarget compiles one target, applies the Windows disguise when configured
// (before metadata is read, so the reported digest describes the bytes as
// delivered), and refuses to return a path that does not exist.
func (bm *BuilderManager) buildTarget(build *BuildConfig, tgt BuildTarget, configB64 string, multi bool) (string, *BuildRefusal) {
	cmd, outputPath := bm.targetCommand(build, tgt, configB64, multi)
	log.Printf("[builder] compiling %s -> %s", tgt, outputPath)

	if _, err := os.Stat(cmd.Path); err != nil {
		return "", &BuildRefusal{
			Target:  tgt,
			Code:    BuildTargetUnsupported,
			Reason:  BuildReasonToolchain,
			Message: fmt.Sprintf("go toolchain not found at %s (%v); nothing was built for %s", cmd.Path, err, tgt),
			Ran:     false,
		}
	}

	out, err := buildRunner(cmd)
	if err != nil {
		return "", &BuildRefusal{
			Target:  tgt,
			Code:    BuildTargetUnsupported,
			Reason:  BuildReasonBuildFailed,
			Message: fmt.Sprintf("build failed: %v\n%s", err, strings.TrimSpace(string(out))),
			Ran:     true,
		}
	}
	if tgt.OS == "windows" && build.Disguise != nil && build.Disguise.Enabled {
		if derr := bm.applyDisguise(outputPath, build.Disguise); derr != nil {
			log.Printf("[builder] disguise warning for %s: %v", outputPath, derr)
			// Non-fatal — the binary is built, disguise is cosmetic.
		}
	}
	if _, serr := os.Stat(outputPath); serr != nil {
		return "", &BuildRefusal{
			Target:  tgt,
			Code:    BuildTargetUnsupported,
			Reason:  BuildReasonBuildFailed,
			Message: fmt.Sprintf("build reported success but no artifact was written at %s: %v", outputPath, serr),
			Ran:     true,
		}
	}
	return outputPath, nil
}

// primaryArtifact returns the path of the artifact matching the record's own
// OS/Arch pair — the platform the v1.18.x fields (binary_path, redeploy,
// download) name — falling back to the first artifact if the pair was not part
// of the matrix.
func primaryArtifact(artifacts []BuildArtifact, osName, arch string) string {
	for _, a := range artifacts {
		if a.OS == osName && a.Arch == arch {
			return a.Path
		}
	}
	if len(artifacts) > 0 {
		return artifacts[0].Path
	}
	return ""
}

// ---------------------------------------------------------------------------
// Metadata read back from the artifact
// ---------------------------------------------------------------------------

// versionSymbol is the compiled variable carrying the release version
// (internal/version/version.go:21). Its string header is what --version prints.
const versionSymbol = versionStampKey

var errVersionSymbolMissing = errors.New("artifact has no " + versionSymbol + " symbol (stripped?); version cannot be read from it")

// semverShape is the only shape accepted as a version read out of a binary.
var semverShape = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.]+)?$`)

// readArtifactMetadata derives every reported field from the artifact itself.
// It never consults the request: a binary that is not what the caller asked for
// cannot be reported as if it were.
func readArtifactMetadata(path string) (BuildArtifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return BuildArtifact{}, fmt.Errorf("open artifact: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return BuildArtifact{}, fmt.Errorf("hash artifact: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		return BuildArtifact{}, fmt.Errorf("stat artifact: %w", err)
	}
	if st.Size() == 0 {
		return BuildArtifact{}, fmt.Errorf("artifact %s is empty", path)
	}

	bi, err := buildinfo.ReadFile(path)
	if err != nil {
		return BuildArtifact{}, fmt.Errorf("read Go build info: %w", err)
	}
	// BuildInfo has no GOOS/GOARCH fields: the linker records them as
	// settings, which is where they are read from.
	goos := buildSetting(bi, "GOOS")
	goarch := buildSetting(bi, "GOARCH")
	if goos == "" || goarch == "" {
		return BuildArtifact{}, fmt.Errorf("artifact build info carries no GOOS/GOARCH")
	}
	ver, err := readArtifactVersion(path)
	if err != nil {
		return BuildArtifact{}, err
	}

	return BuildArtifact{
		OS:       goos,
		Arch:     goarch,
		Version:  ver,
		SHA256:   hex.EncodeToString(h.Sum(nil)),
		Size:     st.Size(),
		Filename: filepath.Base(path),
		Path:     path,
	}, nil
}

// buildSetting returns one recorded build setting, or "".
func buildSetting(bi *buildinfo.BuildInfo, key string) string {
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
}

// readArtifactVersion extracts the compiled release version from a Go binary
// by reading the string header of versionSymbol out of the data section. The
// -ldflags stamp in the build info is not trusted as the source: it records
// what the builder asked for, while this reads what the linker actually put in
// the file. A binary with no symbol table is refused, never labelled.
func readArtifactVersion(path string) (string, error) {
	var magic [4]byte
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	_, err = f.ReadAt(magic[:], 0)
	f.Close()
	if err != nil {
		return "", fmt.Errorf("read artifact header: %w", err)
	}

	switch {
	case magic == [4]byte{0x7f, 'E', 'L', 'F'}:
		return readELFVersion(path)
	case magic[0] == 'M' && magic[1] == 'Z':
		return readPEVersion(path)
	default:
		return "", fmt.Errorf("artifact %s is neither ELF nor PE (magic %x)", path, magic)
	}
}

// readELFVersion reads the string header of versionSymbol from an ELF file and
// resolves the pointer to the string bytes in a loaded segment.
func readELFVersion(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("open ELF artifact: %w", err)
	}
	defer f.Close()

	syms, err := f.Symbols()
	if err != nil {
		return "", fmt.Errorf("read ELF symbols: %w", err)
	}
	var sym *elf.Symbol
	for i := range syms {
		if syms[i].Name == versionSymbol {
			sym = &syms[i]
			break
		}
	}
	if sym == nil {
		return "", errVersionSymbolMissing
	}
	if int(sym.Section) >= len(f.Sections) {
		return "", fmt.Errorf("symbol %s sits in section %d, out of range", versionSymbol, sym.Section)
	}
	sec := f.Sections[sym.Section]
	if sym.Value < sec.Addr || sym.Value-sec.Addr+16 > sec.Size {
		return "", fmt.Errorf("symbol %s value 0x%x outside section %s", versionSymbol, sym.Value, sec.Name)
	}

	var hdr [16]byte
	if _, err := sec.ReadAt(hdr[:], int64(sym.Value-sec.Addr)); err != nil {
		return "", fmt.Errorf("read version string header: %w", err)
	}
	ptr := f.ByteOrder.Uint64(hdr[0:8])
	ln := f.ByteOrder.Uint64(hdr[8:16])
	if ln == 0 || ln > 64 {
		return "", fmt.Errorf("version string header has implausible length %d", ln)
	}
	for _, p := range f.Progs {
		if p.Type != elf.PT_LOAD {
			continue
		}
		if ptr < p.Vaddr || ptr+ln > p.Vaddr+p.Filesz {
			continue
		}
		buf := make([]byte, ln)
		if _, err := p.ReadAt(buf, int64(ptr-p.Vaddr)); err != nil {
			return "", fmt.Errorf("read version bytes: %w", err)
		}
		return acceptVersionBytes(buf)
	}
	return "", fmt.Errorf("version pointer 0x%x is not inside a loaded segment", ptr)
}

// readPEVersion reads the same string header from a PE file. A COFF symbol's
// Value is an offset inside its section; the string header holds an absolute
// virtual address (image base + RVA).
func readPEVersion(path string) (string, error) {
	f, err := pe.Open(path)
	if err != nil {
		return "", fmt.Errorf("open PE artifact: %w", err)
	}
	defer f.Close()

	syms := f.Symbols
	if len(syms) == 0 {
		return "", errVersionSymbolMissing
	}
	var sym *pe.Symbol
	for _, s := range syms {
		if s.Name == versionSymbol {
			sym = s
			break
		}
	}
	if sym == nil {
		return "", errVersionSymbolMissing
	}
	idx := int(sym.SectionNumber) - 1
	if idx < 0 || idx >= len(f.Sections) {
		return "", fmt.Errorf("symbol %s sits in section %d, out of range", versionSymbol, sym.SectionNumber)
	}
	sec := f.Sections[idx]
	if int64(sym.Value)+16 > int64(sec.Size) {
		return "", fmt.Errorf("symbol %s value 0x%x outside section %s", versionSymbol, sym.Value, sec.Name)
	}

	hdr := make([]byte, 16)
	if _, err := sec.ReadAt(hdr, int64(sym.Value)); err != nil {
		return "", fmt.Errorf("read version string header: %w", err)
	}
	ptr := binary.LittleEndian.Uint64(hdr[0:8])
	ln := binary.LittleEndian.Uint64(hdr[8:16])
	if ln == 0 || ln > 64 {
		return "", fmt.Errorf("version string header has implausible length %d", ln)
	}
	// pe.File carries no ImageBase: it is in the optional header, and the
	// width depends on PE32 vs PE32+.
	var imageBase uint64
	switch oh := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		imageBase = oh.ImageBase
	case *pe.OptionalHeader32:
		imageBase = uint64(oh.ImageBase)
	default:
		return "", fmt.Errorf("artifact has no PE optional header: image base unknown")
	}
	if ptr < imageBase {
		return "", fmt.Errorf("version pointer 0x%x is below the image base 0x%x", ptr, imageBase)
	}
	rva := ptr - imageBase
	for _, s := range f.Sections {
		if rva < uint64(s.VirtualAddress) || rva+ln > uint64(s.VirtualAddress)+uint64(s.Size) {
			continue
		}
		buf := make([]byte, ln)
		if _, err := s.ReadAt(buf, int64(rva-uint64(s.VirtualAddress))); err != nil {
			return "", fmt.Errorf("read version bytes: %w", err)
		}
		return acceptVersionBytes(buf)
	}
	return "", fmt.Errorf("version pointer 0x%x is not inside a section", ptr)
}

// acceptVersionBytes validates the bytes read out of the artifact. A value that
// is not a plain semver is an error, not a label.
func acceptVersionBytes(buf []byte) (string, error) {
	s := string(buf)
	if !semverShape.MatchString(s) {
		return "", fmt.Errorf("version bytes %q in the artifact are not a semver", s)
	}
	return s, nil
}
