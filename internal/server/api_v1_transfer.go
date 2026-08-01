package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// v1 API — File transfer endpoints
// ---------------------------------------------------------------------------

// handleV1CreateTransfer initiates a new resumable file transfer.
// POST /api/v1/agents/{id}/transfer
// Body: {"direction": "upload|download", "remote_path": "...", "local_path": "...", "chunk_size": 65536}
func (s *Server) handleV1CreateTransfer(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}

	var params struct {
		Direction  string `json:"direction"`
		RemotePath string `json:"remote_path"`
		LocalPath  string `json:"local_path"`
		ChunkSize  int    `json:"chunk_size"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}

	transfer, err := s.transferMgr.Create(agentID, params.Direction, params.RemotePath, params.LocalPath, params.ChunkSize)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}

	// For upload, start the transfer in background
	if params.Direction == "upload" && params.LocalPath != "" {
		go func() {
			if err := s.transferMgr.ExecuteUpload(transfer, params.LocalPath, s.forwardToAgent); err != nil {
				// Error already recorded in transfer state
				return
			}
		}()
	}

	// For download, start in background — the caller provides local_path
	// as where to save the downloaded file on the server.
	if params.Direction == "download" && params.LocalPath != "" {
		go func() {
			if err := s.transferMgr.ExecuteDownload(transfer, params.LocalPath, s.forwardToAgent); err != nil {
				return
			}
		}()
	}

	writeJSON(w, http.StatusCreated, transfer)
}

// handleV1ListTransfers returns all file transfers.
// GET /api/v1/transfers
func (s *Server) handleV1ListTransfers(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	transfers := s.transferMgr.List()
	writeJSON(w, http.StatusOK, transfers)
}

// handleV1GetTransfer returns a single transfer by ID.
// GET /api/v1/transfers/{id}
func (s *Server) handleV1GetTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "list"); !ok {
		return
	}
	transfer, ok := s.transferMgr.Get(transferID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "transfer not found")
		return
	}
	// Compute percentage
	type transferWithPercent struct {
		*FileTransfer
		Percent float64 `json:"percent"`
	}
	pct := 0.0
	if transfer.TotalSize > 0 {
		pct = float64(transfer.Offset) / float64(transfer.TotalSize) * 100
	}
	writeJSON(w, http.StatusOK, transferWithPercent{FileTransfer: transfer, Percent: pct})
}

// handleV1PauseTransfer pauses a transfer.
// POST /api/v1/transfers/{id}/pause
func (s *Server) handleV1PauseTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}
	if err := s.transferMgr.Pause(transferID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"paused": transferID})
}

// handleV1ResumeTransfer resumes a paused transfer.
// POST /api/v1/transfers/{id}/resume
func (s *Server) handleV1ResumeTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}
	transfer, ok := s.transferMgr.Get(transferID)
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "transfer not found")
		return
	}
	if err := s.transferMgr.Resume(transferID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}

	// Restart the transfer execution in background
	// For upload we need the local path — the caller must provide it in the body
	var params struct {
		LocalPath string `json:"local_path"`
	}
	_ = json.NewDecoder(r.Body).Decode(&params)

	if transfer.Direction == "upload" && params.LocalPath != "" {
		go func() {
			_ = s.transferMgr.ExecuteUpload(transfer, params.LocalPath, s.forwardToAgent)
		}()
	}
	// For download, we need the local save path
	if transfer.Direction == "download" && params.LocalPath != "" {
		go func() {
			_ = s.transferMgr.ExecuteDownload(transfer, params.LocalPath, s.forwardToAgent)
		}()
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{"resumed": transferID})
}

// handleV1CancelTransfer cancels a transfer.
// POST /api/v1/transfers/{id}/cancel
func (s *Server) handleV1CancelTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}
	if err := s.transferMgr.Cancel(transferID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"cancelled": transferID})
}

// handleV1VerifyTransfer verifies the SHA256 of a transferred file.
// POST /api/v1/transfers/{id}/verify
// Body: {"verify_path": "/path/to/file"} — path to the file to verify
func (s *Server) handleV1VerifyTransfer(w http.ResponseWriter, r *http.Request) {
	transferID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-read"); !ok {
		return
	}
	var params struct {
		VerifyPath string `json:"verify_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if params.VerifyPath == "" {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "verify_path is required")
		return
	}
	match, actualHash, err := s.transferMgr.Verify(transferID, params.VerifyPath)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "VERIFY_FAILED", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"verified":     match,
		"expected":     s.transferMgr.transfers[transferID].SHA256,
		"actual":       actualHash,
		"transfer_id":  transferID,
	})
}

// ---------------------------------------------------------------------------
// v1 API — File download endpoints
// ---------------------------------------------------------------------------

// handleV1FileDownload serves files from the download directory via v1 API.
// GET /api/v1/downloads/{filename}
func (s *Server) handleV1FileDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "fs-read"); !ok {
		return
	}
	filename := r.PathValue("filename")
	if filename == "" || strings.Contains(filename, "..") || strings.Contains(filename, "/") || strings.Contains(filename, "\\") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid filename")
		return
	}
	// Use the existing handleFileDownload logic by serving from /tmp/probe-files/
	downloadPath := "/tmp/probe-files/" + filename
	http.ServeFile(w, r, downloadPath)
}

// handleV1AgentFileDownload serves a file from the download directory,
// with the filename specified in the request body. This allows downloading
// through agents that may have restricted path forwarding.
// POST /api/v1/agents/{id}/file-download
// Body: {"filename": "example.exe"}
func (s *Server) handleV1AgentFileDownload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "fs-read"); !ok {
		return
	}
	var params struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if params.Filename == "" || strings.Contains(params.Filename, "..") || strings.Contains(params.Filename, "/") || strings.Contains(params.Filename, "\\") {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid filename")
		return
	}
	downloadPath := "/tmp/probe-files/" + params.Filename
	http.ServeFile(w, r, downloadPath)
}

// ---------------------------------------------------------------------------
// v1 API — Screen streaming endpoints
// ---------------------------------------------------------------------------

// handleV1StreamStart starts screen streaming on an agent.
// POST /api/v1/agents/{id}/stream-start
// Body: {"display": 0, "fps": 10, "quality": 80}
func (s *Server) handleV1StreamStart(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "capture"); !ok {
		return
	}
	var params protocol.ScreenStreamParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	if params.FPS <= 0 {
		params.FPS = 10
	}
	if params.Quality <= 0 {
		params.Quality = 80
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeStreamBegin, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	// Check for agent-side error
	if m, ok := resp.(map[string]interface{}); ok {
		if errMsg, hasErr := m["error"]; hasErr {
			writeError(w, http.StatusServiceUnavailable, "AGENT_ERROR", errMsg.(string))
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleV1StreamStop stops screen streaming on an agent.
// POST /api/v1/agents/{id}/stream-stop
// Body: {"stream_id": "..."}
func (s *Server) handleV1StreamStop(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "capture"); !ok {
		return
	}
	var params protocol.ScreenStreamStopParams
	// Try to parse body, but allow empty body for convenience
	body := make([]byte, 0)
	if r.Body != nil {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		body = buf[:n]
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &params)
	}

	resp, err := s.forwardToAgent(agentID, protocol.TypeStreamEnd, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	if m, ok := resp.(map[string]interface{}); ok {
		if errMsg, hasErr := m["error"]; hasErr {
			writeError(w, http.StatusServiceUnavailable, "AGENT_ERROR", errMsg.(string))
			return
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleV1StreamFrame returns the latest stream frame for an agent.
// GET /api/v1/agents/{id}/stream-frame
// Lightweight polling endpoint — agent pushes frames via WebSocket, server stores
// the latest one. Frontend polls this at the desired FPS.
func (s *Server) handleV1StreamFrame(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "capture"); !ok {
		return
	}
	frameJSON := s.sessions.GetMemory(agentID, "last_stream_frame")
	if frameJSON == "" {
		writeError(w, http.StatusNotFound, "NO_FRAME", "no stream frame available")
		return
	}
	// Parse the stored frame JSON and re-wrap in API envelope
	var frameObj interface{}
	if err := json.Unmarshal([]byte(frameJSON), &frameObj); err != nil {
		writeError(w, http.StatusInternalServerError, "PARSE_ERROR", "failed to parse frame")
		return
	}
	writeJSON(w, http.StatusOK, frameObj)
}

// handleV1PointerClick sends a mouse click to an agent.
// POST /api/v1/agents/{id}/pointer-click
// Body: {"x": 100, "y": 200, "button": "left"}
func (s *Server) handleV1PointerClick(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "input"); !ok {
		return
	}
	var params protocol.InputParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	resp, err := s.forwardToAgent(agentID, protocol.TypePointerClick, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleV1KeyPress sends a single key press to an agent.
// POST /api/v1/agents/{id}/key-press
// Body: {"key": "Enter"}
func (s *Server) handleV1KeyPress(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "input"); !ok {
		return
	}
	var params protocol.InputParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	resp, err := s.forwardToAgent(agentID, protocol.TypeKeyPress, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleV1KeyCombo sends a key combination to an agent.
// POST /api/v1/agents/{id}/key-combo
// Body: {"keys": ["Ctrl", "C"]}
func (s *Server) handleV1KeyCombo(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "input"); !ok {
		return
	}
	var params protocol.InputParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	resp, err := s.forwardToAgent(agentID, protocol.TypeKeyCombo, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleV1TextInput sends text input to an agent.
// POST /api/v1/agents/{id}/text-input
// Body: {"text": "Hello World"}
func (s *Server) handleV1TextInput(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "input"); !ok {
		return
	}
	var params protocol.InputParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "invalid JSON: "+err.Error())
		return
	}
	resp, err := s.forwardToAgent(agentID, protocol.TypeTextInput, params)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			writeError(w, http.StatusGatewayTimeout, "TIMEOUT", err.Error())
		} else {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNREACHABLE", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, resp)
}