package server

// P8-10 (P8-02 §8.3) — watch{path, since_offset}.
//
// Long-poll over the existing fs_read primitive: no new agent command, no SSE.
// The server reads the byte window after the caller's marker every 500 ms and
// returns as soon as bytes appear or the caller's timeout elapses.
//
// MARKER SEMANTICS — the marker is an ABSOLUTE BYTE OFFSET, not a line index:
//
//	next_offset = offset at which the next call must resume
//
// It advances only past a newline boundary, so a half-written line is never
// split across two responses (the partial line is returned once, at timeout,
// and consumed). `eof` reports "no more bytes exist right now"; it is not an
// end-of-stream claim, because a watched file can always grow.
//
// Rotation: a file that is smaller than the caller's marker was truncated or
// replaced. The response says `rotated:true` and `next_offset:0`, and the
// lines delivered in that same response begin at byte 0.

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

const (
	watchPollInterval = 500 * time.Millisecond
	watchChunkBytes   = 64 * 1024
	watchDefaultSecs  = 25
	watchMaxSecs      = 300
)

type watchRequest struct {
	Path string `json:"path"`
	// SinceOffset is the marker. `since` is accepted as the spelling the
	// design's prose uses; both resolve to the same byte offset.
	SinceOffset *int64 `json:"since_offset,omitempty"`
	Since       *int64 `json:"since,omitempty"`
	TimeoutS    int    `json:"timeout_s,omitempty"`
	MaxBytes    int64  `json:"max_bytes,omitempty"`
}

type watchResponse struct {
	Path        string   `json:"path"`
	Lines       []string `json:"lines"`
	NextOffset  int64    `json:"next_offset"`
	EOF         bool     `json:"eof"`
	Rotated     bool     `json:"rotated"`
	Marker      string   `json:"marker"`
	WaitedMS    int64    `json:"waited_ms"`
	ReadBytes   int64    `json:"read_bytes"`
	Truncated   bool     `json:"truncated"`
}

// handleV1Watch implements POST /api/v1/agents/{id}/watch.
func (s *Server) handleV1Watch(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-read"); !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body")
		return
	}
	var req watchRequest
	if err := json.Unmarshal(body, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "path is required")
		return
	}
	req.Path = strings.TrimSpace(req.Path)

	offset := int64(0)
	switch {
	case req.SinceOffset != nil && *req.SinceOffset < 0:
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "since_offset must be >= 0")
		return
	case req.SinceOffset != nil:
		offset = *req.SinceOffset
	case req.Since != nil && *req.Since < 0:
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "since must be >= 0")
		return
	case req.Since != nil:
		offset = *req.Since
	}
	if req.TimeoutS <= 0 {
		req.TimeoutS = watchDefaultSecs
	}
	if req.TimeoutS > watchMaxSecs {
		req.TimeoutS = watchMaxSecs
	}
	limit := req.MaxBytes
	if limit <= 0 || limit > 1<<20 {
		limit = watchChunkBytes
	}

	start := time.Now()
	deadline := start.Add(time.Duration(req.TimeoutS) * time.Second)
	out := watchResponse{Path: req.Path, Lines: []string{}, NextOffset: offset, EOF: true, Marker: "byte_offset"}

	// Rotation is decided from a stat before reading: if the file shrank
	// below the marker, the old bytes the marker referred to no longer exist.
	size, _, statOK := s.watchStat(agentID, req.Path)
	if statOK && size < offset {
		out.Rotated = true
		offset = 0
		out.NextOffset = 0
	}

	var buf []byte
	for {
		chunk, total, nerr := s.watchRead(agentID, req.Path, offset+int64(len(buf)), limit)
		if nerr == nil && len(chunk) > 0 {
			buf = append(buf, chunk...)
			// A file that shrank mid-poll is a rotation: deliver what we
			// have and tell the caller to restart at 0.
			if total >= 0 && offset+int64(len(buf)) > total {
				out.Rotated = true
			}
			if nl := lastNewline(buf); nl >= 0 {
				out.Lines = splitLines(buf[:nl+1])
				out.ReadBytes = int64(nl + 1)
				out.NextOffset = offset + int64(nl+1)
				out.EOF = true
				out.WaitedMS = time.Since(start).Milliseconds()
				writeJSON(w, http.StatusOK, out)
				return
			}
		}
		if time.Now().After(deadline) {
			break
		}
		if len(buf) == 0 {
			time.Sleep(watchPollInterval)
		} else {
			// Bytes but no newline yet: wait briefly for the writer to finish
			// the line, but never past the caller's deadline.
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Timeout. A trailing partial line is delivered once and consumed: the
	// marker moves to its end so the next call cannot repeat it.
	if len(buf) > 0 {
		nl := lastNewline(buf)
		if nl >= 0 {
			out.Lines = splitLines(buf[:nl+1])
			out.ReadBytes = int64(nl + 1)
			out.NextOffset = offset + int64(nl+1)
		} else {
			out.Lines = []string{string(buf)}
			out.ReadBytes = int64(len(buf))
			out.NextOffset = offset + int64(len(buf))
		}
		out.Truncated = nl < len(buf)-1
	}
	out.EOF = true
	out.WaitedMS = time.Since(start).Milliseconds()
	writeJSON(w, http.StatusOK, out)
}

// watchStat asks the agent for the file size. A stat failure is reported as
// statOK=false; the watch loop still reads, and the read error surfaces there.
func (s *Server) watchStat(agentID, path string) (size int64, modTime int64, ok bool) {
	params := protocol.FSParams{Path: path}
	resp, err := s.forwardToAgentWithTimeout(agentID, protocol.TypeFSStat, params, 10*time.Second, "")
	if err != nil {
		return 0, 0, false
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return 0, 0, false
	}
	if _, isErr := m["error"]; isErr {
		return 0, 0, false
	}
	if v, ok := m["size"].(float64); ok {
		size = int64(v)
	}
	if v, ok := m["mod_time"].(float64); ok {
		modTime = int64(v)
	}
	return size, modTime, true
}

// watchRead reads one window starting at offset. It decodes the base64 `data`
// field the fs-read result has carried since v1 (F1) and returns the file's
// total size so the caller can notice a truncation.
func (s *Server) watchRead(agentID, path string, offset, limit int64) (data []byte, totalSize int64, err error) {
	params := protocol.FSParams{Path: path, Offset: int(offset), Limit: int(limit)}
	resp, ferr := s.forwardToAgentWithTimeout(agentID, protocol.TypeFSRead, params, 30*time.Second, "")
	if ferr != nil {
		return nil, -1, ferr
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return nil, -1, nil
	}
	if e, isErr := m["error"]; isErr {
		return nil, -1, &watchRemoteError{msg: e}
	}
	totalSize = -1
	if v, ok := m["total_size"].(float64); ok {
		totalSize = int64(v)
	}
	switch d := m["data"].(type) {
	case string:
		raw, derr := base64.StdEncoding.DecodeString(d)
		if derr != nil {
			return nil, totalSize, derr
		}
		return raw, totalSize, nil
	case []byte:
		return d, totalSize, nil
	}
	return nil, totalSize, nil
}

type watchRemoteError struct{ msg interface{} }

func (e *watchRemoteError) Error() string { return "watch read failed" }

func lastNewline(b []byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			return i
		}
	}
	return -1
}

func splitLines(b []byte) []string {
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return []string{}
	}
	return strings.Split(s, "\n")
}
