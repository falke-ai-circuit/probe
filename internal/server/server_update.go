package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// canaryRegistrySuffix is the registry-key suffix a canary process carries
// while it runs beside the still-live old agent (agent.canaryNameSuffix).
// When the update commits the canary strips the suffix and registers as the
// canonical agent, so the suffixed row must not outlive the update.
const canaryRegistrySuffix = "-canary"

// pendingUpdates tracks agents that have been told to update.
// When a new agent connects and the old one reports its PID, we kill the old.
type pendingUpdate struct {
	OldAgentID string
	OldPID     int
	NewVersion string
	NotifiedAt time.Time
}

// purgeCanaryRegistryRow deletes the "<agentID>-canary" registry row if one
// exists and reports whether a row was removed. The canonical agent row (key
// == agentID) is never touched. Both call sites are idempotent: a missing row
// is a silent no-op, so a repeated update start or a retried completion cannot
// remove anything twice or log noise.
//
// Defect B (P9-08): the canary row stayed in the registry forever after a
// successful promotion, so a completed update left TEST-OLD-1186 and
// TEST-OLD-1186-canary both at 1.19.0 — indistinguishable from a live second
// agent.
func (s *Server) purgeCanaryRegistryRow(agentID string) bool {
	if agentID == "" {
		return false
	}
	canaryID := agentID + canaryRegistrySuffix
	if canaryID == agentID { // unreachable, kept so the canonical row can never be the target
		return false
	}
	if !s.registry.Remove(canaryID) {
		return false
	}
	log.Printf("[update] removed stale canary registry row %s (canonical %s untouched)", canaryID, agentID)
	return true
}

// handleAgentUpdate is the HTTP API endpoint that triggers a remote agent update.
// POST /api/agent/{id}/update  body: {"binary_path":"/tmp/probe-files/PROBE_v9.exe","version":"v9"}
//
// The server:
//  1. Computes SHA256 of the binary file
//  2. Sends agent_update message to the agent with download URL + hash
//  3. Agent downloads, verifies, starts new process, reports old PID
//  4. When new agent connects (detected by version change), server kills old PID
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request, agentID string) {
	var params struct {
		BinaryPath   string `json:"binary_path"`   // local path on server to the binary
		Version      string `json:"version"`       // version label
		DownloadHost string `json:"download_host"` // override for download URL host (e.g. "187.124.31.229:80")
		WSPath       string `json:"ws_path"`       // if set, server pre-staged binary via WS at this path on the agent
		WSRemotePath string `json:"ws_remote_path"` // remote path on agent where the WS-staged binary lives (for swap to find)
		SHA256       string `json:"sha256"`        // optional with binary_path (must match), required with ws_remote_path
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if params.BinaryPath == "" && params.WSRemotePath == "" {
		http.Error(w, "either binary_path or ws_remote_path is required", http.StatusBadRequest)
		return
	}

	// Compute SHA256 of the binary. In ws_remote_path mode there is no local
	// file to hash — the binary was staged on the agent over the WS tunnel — so
	// the caller supplies the digest it staged, and the agent verifies it
	// against the file it finds at ws_remote_path. Defect A (P9-08): hashing
	// params.BinaryPath unconditionally made the documented ws_remote_path mode
	// return "hash binary failed: open : no such file or directory".
	var hash string
	if params.BinaryPath == "" {
		supplied := strings.ToLower(strings.TrimSpace(params.SHA256))
		if supplied == "" {
			http.Error(w, "sha256 is required with ws_remote_path", http.StatusBadRequest)
			return
		}
		hash = supplied
	} else {
		computed, err := hashFileServer(params.BinaryPath)
		if err != nil {
			http.Error(w, fmt.Sprintf("hash binary failed: %v", err), http.StatusInternalServerError)
			return
		}
		hash = computed
		// Optional server-side confirmation: when the caller also states the
		// digest it believes it is sending, a disagreement is a bad request —
		// never a silent install of a different binary.
		if supplied := strings.ToLower(strings.TrimSpace(params.SHA256)); supplied != "" && supplied != computed {
			http.Error(w, fmt.Sprintf("sha256 mismatch: supplied %s, computed %s", supplied, computed), http.StatusBadRequest)
			return
		}
	}

	// Get the filename for the download URL (ws mode: the staged file's name).
	filename := filepath.Base(params.BinaryPath)
	if params.BinaryPath == "" {
		filename = filepath.Base(params.WSRemotePath)
	}

	// Copy binary to the download directory if not already there. Only a
	// server-local binary has anything to copy.
	downloadDir := "/tmp/probe-files/"
	downloadPath := downloadDir + filename
	if params.BinaryPath != "" && params.BinaryPath != downloadPath {
		if err := copyFile(params.BinaryPath, downloadPath); err != nil {
			http.Error(w, fmt.Sprintf("copy to download dir failed: %v", err), http.StatusInternalServerError)
			return
		}
	}

	// Build the download URL. Use download_host from request if provided,
	// otherwise use r.Host (the host the API caller used to reach us),
	// otherwise fall back to s.addr.
	host := params.DownloadHost
	if host == "" {
		host = r.Host
	}
	if host == "" {
		host = s.addr
		if host != "" && host[0] == ':' {
			host = "localhost" + host
		}
	}
	downloadURL := fmt.Sprintf("http://%s/download/%s?token=%s", host, filename, s.token)

	// Build the AgentUpdateParams message. If WSRemotePath is set, the agent
	// uses the locally-pre-staged file at that path (already delivered via
	// WS file_save) instead of HTTP-downloading.
	updateParams := protocol.AgentUpdateParams{
		DownloadURL: downloadURL,
		Filename:    filename,
		SHA256:      hash,
		Version:     params.Version,
	}
	if params.WSRemotePath != "" {
		updateParams.WSPath = params.WSRemotePath
		updateParams.DownloadURL = "" // empty tells agent not to HTTP-fetch
	}

	// An update start supersedes any canary row left behind by an earlier
	// attempt: from here the only rows that should exist are the old agent and
	// (once it connects) the promoted agent under its canonical name.
	s.purgeCanaryRegistryRow(agentID)

	shortHash := hash
	if len(shortHash) > 16 {
		shortHash = shortHash[:16]
	}
	log.Printf("[update] sending agent_update to %s: version=%s, file=%s, hash=%s, ws_path=%q",
		agentID, params.Version, filename, shortHash, updateParams.WSPath)

	// Send agent_update to the agent. We use forwardToAgentWithTimeout which
	// will send the WebSocket message and wait for a response. The agent will
	// download the binary, verify it, start the new process, and respond.
	// However, the response may not arrive because the agent's WebSocket
	// connection drops when the new process takes over. So we use a short
	// timeout and treat timeout as "update in progress" — the server's
	// handleWebSocket will detect the new agent and auto-kill the old PID.
	resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeAgentUpdate, updateParams, 30*time.Second, operatorIDFromRequest(r))
	if err != nil {
		// Timeout is expected — the agent starts the new process and the old
		// connection drops. The update result arrives via the agent_update_result
		// message handler in handleMessages, and the new agent's connection
		// triggers the auto-kill. Report "update in progress" to the caller.
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "update_in_progress",
			"message": "agent_update command sent. The agent will download and restart. Check /api/agents for the new version.",
			"version": params.Version,
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleAgentUpdateResult processes the agent_update_result message from an agent.
// The agent reports its old PID and the new process PID. The server stores this
// so it can kill the old process once the new agent connects.
func (s *Server) handleAgentUpdateResult(agentID string, env protocol.Envelope) {
	var result protocol.AgentUpdateResult
	if env.Result != nil {
		_ = json.Unmarshal(env.Result, &result)
	}

	log.Printf("[update] agent %s update result: success=%v, oldPID=%d, newPID=%d, msg=%s",
		agentID, result.Success, result.OldPID, result.NewPID, result.Message)

	if !result.Success {
		log.Printf("[update] agent %s update failed: %s", agentID, result.Message)
		return
	}

	// Store the pending update info. When a new agent connects with a different
	// version, we'll check if we need to kill the old PID.
	s.pendingMu.Lock()
	s.pendingUpdates[agentID] = &pendingUpdate{
		OldAgentID: agentID,
		OldPID:     result.OldPID,
		NewVersion: "",
		NotifiedAt: time.Now(),
	}
	s.pendingMu.Unlock()

	log.Printf("[update] agent %s: old PID=%d recorded, waiting for new agent to connect", agentID, result.OldPID)
}

// hashFileServer computes SHA256 hex digest of a file (server-side).
func hashFileServer(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}