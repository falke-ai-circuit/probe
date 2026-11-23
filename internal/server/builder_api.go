package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// v1 handlers — agent builder
// ---------------------------------------------------------------------------

// handleV1CreateBuild handles POST /api/v1/builds — creates a new agent build
// (admin only). The build runs in a background goroutine and the build ID is
// returned immediately.
func (s *Server) handleV1CreateBuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	var cfg BuildConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}

	build, err := s.builder.CreateBuild(&cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, build)
}

// handleV1ListBuilds handles GET /api/v1/builds — lists all build records.
func (s *Server) handleV1ListBuilds(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	builds := s.builder.ListBuilds()
	writeJSON(w, http.StatusOK, builds)
}

// handleV1GetBuild handles GET /api/v1/builds/{id} — returns a single build's
// status.
func (s *Server) handleV1GetBuild(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	build := s.builder.GetBuild(buildID)
	if build == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "build not found")
		return
	}
	writeJSON(w, http.StatusOK, build)
}

// handleV1DownloadBuild handles GET /api/v1/builds/{id}/download — serves the
// built binary for download.
func (s *Server) handleV1DownloadBuild(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	build := s.builder.GetBuild(buildID)
	if build == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "build not found")
		return
	}
	if build.Status != BuildStatusComplete {
		writeError(w, http.StatusConflict, "BUILD_NOT_READY", "build status: "+build.Status)
		return
	}
	if build.BinaryPath == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "binary path is empty")
		return
	}
	// Check the file exists.
	if _, err := os.Stat(build.BinaryPath); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "binary file not found on disk")
		return
	}
	filename := filepath.Base(build.BinaryPath)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, build.BinaryPath)
}

// handleV1DeleteBuild handles DELETE /api/v1/builds/{id} — deletes a build
// and its binary.
func (s *Server) handleV1DeleteBuild(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	if !s.builder.DeleteBuild(buildID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "build not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": buildID})
}

// ---------------------------------------------------------------------------
// v1 handlers — build profiles
// ---------------------------------------------------------------------------

// handleV1ListProfiles handles GET /api/v1/profiles — lists all build profiles.
func (s *Server) handleV1ListProfiles(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	profiles := s.profiles.List()
	writeJSON(w, http.StatusOK, profiles)
}

// handleV1CreateProfile handles POST /api/v1/profiles — creates a new build
// profile (admin only).
func (s *Server) handleV1CreateProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	var p Profile
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}

	created, err := s.profiles.Create(&p)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, created)
}

// handleV1GetProfile handles GET /api/v1/profiles/{id} — returns a single
// build profile.
func (s *Server) handleV1GetProfile(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	profile := s.profiles.Get(profileID)
	if profile == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "profile not found")
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleV1DeleteProfile handles DELETE /api/v1/profiles/{id} — deletes a
// build profile (admin only).
func (s *Server) handleV1DeleteProfile(w http.ResponseWriter, r *http.Request) {
	profileID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}
	if !s.profiles.Delete(profileID) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "profile not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"deleted": profileID})
}

// ---------------------------------------------------------------------------
// v1 handlers — agent capabilities + redeploy
// ---------------------------------------------------------------------------

// handleV1GetAgentCapabilities handles GET /api/v1/agents/{id}/capabilities —
// returns the agent's current capabilities from the registry.
//
// P8-10 (P8-02 §8.2): the response gains an `inventory` block and the route
// accepts ?refresh=true. The two original keys keep their names and position;
// the added keys sit beside them, so an existing caller is unaffected.
func (s *Server) handleV1GetAgentCapabilities(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	refresh := parseRefreshFlag(r.URL.Query().Get("refresh"))
	inv := s.cachedCapabilityInventory(rec, refresh)
	writeJSON(w, http.StatusOK, inv)
}

// handleV1RedeployAgent handles POST /api/v1/agents/{id}/redeploy — rebuilds
// the agent with new capabilities and pushes the update through the existing
// agent connection. Returns immediately with build_id and status "building".
func (s *Server) handleV1RedeployAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	var params struct {
		Capabilities []string `json:"capabilities"`
		ServerURL    string   `json:"server_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}

	if len(params.Capabilities) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "capabilities is required")
		return
	}

	// Look up agent in registry for existing config.
	rec, err := s.registry.GetHealth(agentID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	// Determine server URL: use the one from the request, or fall back to the
	// agent's existing server URL (from the registry record), or the server's
	// own listen address.
	serverURL := params.ServerURL
	if serverURL == "" {
		serverURL = "ws://" + s.addr + "/ws"
	}

	// Create BuildConfig with new capabilities + agent's existing config.
	cfg := &BuildConfig{
		Name:         rec.Name,
		OS:           rec.OS,
		Arch:         rec.Arch,
		Capabilities: params.Capabilities,
		ServerURL:    serverURL,
		Token:        s.token, // use the server's primary token for the rebuilt agent
		Permissions:  "full",
	}

	build, err := s.builder.CreateBuild(cfg)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}

	// Start a goroutine to poll build status and trigger agent update when done.
	go s.pollAndDeployRedeploy(agentID, build.ID, rec.Name)

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"build_id":  build.ID,
		"status":    "building",
		"agent_id":  agentID,
		"agent_name": rec.Name,
	})
}

// pollAndDeployRedeploy polls the build status every 2s until complete or
// failed. On success, copies the binary to the download directory, computes
// SHA256, and sends an agent_update message to the agent.
func (s *Server) pollAndDeployRedeploy(agentID, buildID, agentName string) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	timeout := time.After(10 * time.Minute)
	for {
		select {
		case <-ticker.C:
			build := s.builder.GetBuild(buildID)
			if build == nil {
				log.Printf("[redeploy] build %s not found for agent %s", buildID, agentID)
				return
			}
			if build.Status == BuildStatusComplete {
				s.deployRedeployUpdate(agentID, buildID, agentName, build.BinaryPath)
				return
			}
			if build.Status == BuildStatusFailed {
				log.Printf("[redeploy] build %s failed for agent %s: %s", buildID, agentID, build.Error)
				return
			}
		case <-timeout:
			log.Printf("[redeploy] build %s timed out for agent %s", buildID, agentID)
			return
		}
	}
}

// deployRedeployUpdate copies the built binary to the download directory,
// computes its SHA256, and sends an agent_update message to the agent.
func (s *Server) deployRedeployUpdate(agentID, buildID, agentName, binaryPath string) {
	if binaryPath == "" {
		log.Printf("[redeploy] build %s has no binary path", buildID)
		return
	}

	// Compute SHA256 of the binary.
	hash, err := hashFileServer(binaryPath)
	if err != nil {
		log.Printf("[redeploy] hash binary failed for build %s: %v", buildID, err)
		return
	}

	// Copy binary into the managed artifacts directory (P9-11).
	filename := filepath.Base(binaryPath)
	downloadDir := s.FilesDir()
	downloadPath := filepath.Join(downloadDir, filename)
	if binaryPath != downloadPath {
		if err := copyFile(binaryPath, downloadPath); err != nil {
			log.Printf("[redeploy] copy to download dir failed for build %s: %v", buildID, err)
			return
		}
	}

	// Build the download URL using the server's address.
	host := s.addr
	if host != "" && host[0] == ':' {
		host = "localhost" + host
	}
	downloadURL := fmt.Sprintf("http://%s/download/%s", host, filename)

	version := fmt.Sprintf("redeploy-%s-%d", buildID, time.Now().Unix())

	updateParams := protocol.AgentUpdateParams{
		DownloadURL: downloadURL,
		Filename:    filename,
		SHA256:      hash,
		Version:     version,
	}

	log.Printf("[redeploy] sending agent_update to %s: version=%s, file=%s, hash=%s",
		agentID, version, filename, hash[:16])

	// Send agent_update. Timeout is expected — the agent restarts and the old
	// connection drops. Use 30s timeout.
	_, updateErr := s.forwardToAgentWithTimeout(agentID, protocol.TypeAgentUpdate, updateParams, 30*time.Second, "")
	if updateErr != nil {
		// Timeout is expected during agent restart — log but don't treat as error.
		log.Printf("[redeploy] agent_update sent to %s (timeout expected: %v)", agentID, updateErr)
	} else {
		log.Printf("[redeploy] agent_update acknowledged by %s", agentID)
	}
}

// ---------------------------------------------------------------------------
// v1 handlers — VirusTotal scan
// ---------------------------------------------------------------------------

// handleV1VTScan handles POST /api/v1/builds/{id}/vt-scan — triggers a VT
// scan on a completed build's binary. Returns 400 if no VT API key is set.
func (s *Server) handleV1VTScan(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "operator-manage"); !ok {
		return
	}

	if s.vtScanner == nil {
		writeError(w, http.StatusBadRequest, "VT_NOT_CONFIGURED", "VT API key not configured")
		return
	}

	build := s.builder.GetBuild(buildID)
	if build == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "build not found")
		return
	}
	if build.Status != BuildStatusComplete {
		writeError(w, http.StatusConflict, "BUILD_NOT_READY", "build status: "+build.Status)
		return
	}
	if build.BinaryPath == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "binary path is empty")
		return
	}
	if _, err := os.Stat(build.BinaryPath); err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "binary file not found on disk")
		return
	}

	// Start VT scan in goroutine, update build record on completion.
	s.builder.UpdateVTStatus(buildID, VTStatusScanning, 0, "")
	go func() {
		report, err := s.vtScanner.ScanFile(build.BinaryPath)
		if err != nil {
			log.Printf("[vt] scan failed for build %s: %v", buildID, err)
			s.builder.UpdateVTStatus(buildID, "failed", 0, "")
			return
		}
		status := VTStatusClean
		if report.Detections > 0 {
			status = VTStatusDirty
		}
		s.builder.UpdateVTStatus(buildID, status, report.Detections, report.ReportURL)
		log.Printf("[vt] scan complete for build %s: %d/%d detections", buildID, report.Detections, report.Total)
	}()

	writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"build_id":  buildID,
		"vt_status": VTStatusScanning,
		"message":   "VT scan started",
	})
}

// handleV1GetVTScan handles GET /api/v1/builds/{id}/vt-scan — returns the
// current VT scan status for a build. Returns 400 if no VT API key is set.
func (s *Server) handleV1GetVTScan(w http.ResponseWriter, r *http.Request) {
	buildID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}

	if s.vtScanner == nil {
		writeError(w, http.StatusBadRequest, "VT_NOT_CONFIGURED", "VT API key not configured")
		return
	}

	build := s.builder.GetBuild(buildID)
	if build == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "build not found")
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"build_id":      buildID,
		"vt_status":     build.VTStatus,
		"vt_detections": build.VTDetections,
		"vt_report_url": build.VTReportURL,
	})
}