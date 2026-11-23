package server

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
)

// handleFileDownload serves files from the managed artifacts directory
// (Server.FilesDir, inside the persistent /data volume) over HTTP.
// This allows the agent to download large files (like updated binaries) from
// the server without hitting command-line length limits.
//
// Canonical endpoint: GET /download/{filename}
// Docker-compatible patterns (deprecated, log warning):
//   - /api/download/{filename}
//   - /api/agent/file/{filename}
//   - /api/agent/{id}/download/{filename}
//   - POST /api/agent/{id}/file-download (body: {"filename":"..."})
func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	// Check auth: try header first, then query parameter (for agent downloads)
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		// Allow token via query param for agent-initiated downloads
		if qToken := r.URL.Query().Get("token"); qToken != "" {
			authHeader = "Bearer " + qToken
		}
	}
	if !s.checkAPIAuthWithHeader(w, r, authHeader) {
		return
	}

	// Canonical path: /download/{filename}
	canonical := strings.HasPrefix(r.URL.Path, "/download/")
	if !canonical {
		log.Printf("[server] deprecated download path: %s — use /download/{filename}", r.URL.Path)
	}

	filename := r.URL.Path
	// Support multiple path prefixes for file download
	for _, prefix := range []string{"/api/agent/file/", "/api/download/", "/download/"} {
		if strings.HasPrefix(filename, prefix) {
			filename = strings.TrimPrefix(filename, prefix)
			break
		}
	}
	// Also handle /api/agent/{id}/download/{filename}
	if strings.HasPrefix(filename, "/api/agent/") && strings.Contains(filename, "/download/") {
		parts := strings.SplitN(filename, "/download/", 2)
		if len(parts) == 2 {
			filename = parts[1]
		}
	}
	if filename == "" || strings.Contains(filename, "..") || strings.Contains(filename, "/") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	localPath, err := s.ManagedPath(filename)
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, localPath)
}

// handleFileDownloadBody serves a file from the download directory, with the
// filename specified in the JSON body. This works through Docker proxies that
// only forward 2-level paths like /api/agent/{id}/{action}.
// POST /api/agent/{id}/file-download  body: {"filename":"example.exe"}
func (s *Server) handleFileDownloadBody(w http.ResponseWriter, r *http.Request) {
	var params struct {
		Filename string `json:"filename"`
	}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if params.Filename == "" || strings.Contains(params.Filename, "..") || strings.Contains(params.Filename, "/") {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	localPath, err := s.ManagedPath(params.Filename)
	if err != nil {
		http.Error(w, "invalid filename", http.StatusBadRequest)
		return
	}
	http.ServeFile(w, r, localPath)
}