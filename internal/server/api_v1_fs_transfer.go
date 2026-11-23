package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// P8-06 — raw-body streaming upload / ranged download (P8-02 §5)
//
// These two routes are the operator-facing transport. Both speak raw bytes on
// the operator↔server leg (no base64 in the path the client sees) and reuse the
// existing agent primitives: fs-write with offset for upload, fs-read with
// offset/limit for download. No new agent command, no new dependency.
//
// Upload keeps its partial state in "<path>.part" on the node; the final name
// only appears after the whole-file digest matches (§5.1.3, D-09).
// ---------------------------------------------------------------------------

// rawUploadMaxChunk bounds one request body. It is a guard against a single
// request buffering an unbounded payload in the server, not a framing limit:
// the operator is expected to send ~256 KiB per chunk (§5.1.2).
const rawUploadMaxChunk = 32 << 20

// fsReadMaxBytes returns the server-side fs-read cap (P8-02 §4.3.2). Default
// 8 MiB; PROBE_FS_READ_MAX_BYTES=0 restores unlimited.
func fsReadMaxBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("PROBE_FS_READ_MAX_BYTES"))
	if raw == "" {
		return 8 << 20
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 8 << 20
	}
	if n < 0 {
		return 0
	}
	return n
}

// remoteStat returns the size / existence / mtime of a path on the agent.
func (s *Server) remoteStat(agentID, path string) (size int64, exists bool, modTime int64, err error) {
	resp, ferr := s.forwardToAgent(agentID, protocol.TypeFSStat, protocol.FSParams{Path: path})
	if ferr != nil {
		return 0, false, 0, ferr
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return 0, false, 0, fmt.Errorf("unexpected fs-stat response type")
	}
	if e, has := m["error"]; has {
		return 0, false, 0, fmt.Errorf("%v", e)
	}
	if v, ok := m["size"].(float64); ok {
		size = int64(v)
	}
	if v, ok := m["exists"].(bool); ok {
		exists = v
	}
	if v, ok := m["mod_time"].(float64); ok {
		modTime = int64(v)
	}
	return size, exists, modTime, nil
}

// remoteHash returns the whole-file sha256 of a path on the agent.
func (s *Server) remoteHash(agentID, path string) (string, int64, error) {
	resp, err := s.forwardToAgent(agentID, protocol.TypeFSHash, protocol.FSParams{Path: path})
	if err != nil {
		return "", 0, err
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return "", 0, fmt.Errorf("unexpected fs-hash response type")
	}
	if e, has := m["error"]; has {
		return "", 0, fmt.Errorf("%v", e)
	}
	sum, _ := m["sha256"].(string)
	var size int64
	if v, ok := m["size"].(float64); ok {
		size = int64(v)
	}
	return sum, size, nil
}

// remoteWriteChunk writes raw bytes at an offset on the agent. offset==0 with
// create=true truncates first (first chunk); every other chunk seeks and writes
// (agent.writeFileChunk, already offset-addressed — F2).
func (s *Server) remoteWriteChunk(agentID, path string, offset int64, data []byte, create bool) (int, error) {
	mode := ""
	if create {
		mode = "create"
	}
	params := protocol.FSParams{
		Path:   path,
		Offset: int(offset),
		Data:   base64.StdEncoding.EncodeToString(data),
		Mode:   mode,
	}
	resp, err := s.forwardToAgent(agentID, protocol.TypeFileSave, params)
	if err != nil {
		return 0, err
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("unexpected fs-write response type")
	}
	if e, has := m["error"]; has {
		return 0, fmt.Errorf("%v", e)
	}
	written := len(data)
	if v, ok := m["written"].(float64); ok && int(v) != written {
		return int(v), fmt.Errorf("short write: agent reported %d of %d bytes", int(v), written)
	}
	return written, nil
}

// remoteReadWindow reads [offset, offset+limit) from the agent and returns the
// decoded bytes plus the agent's own next_offset declaration.
func (s *Server) remoteReadWindow(agentID, path string, offset, limit int64) ([]byte, int64, error) {
	params := protocol.FSParams{Path: path, Offset: int(offset), Limit: int(limit)}
	resp, err := s.forwardToAgent(agentID, protocol.TypeFSRead, params)
	if err != nil {
		return nil, 0, err
	}
	m, ok := resp.(map[string]interface{})
	if !ok {
		return nil, 0, fmt.Errorf("unexpected fs-read response type")
	}
	if e, has := m["error"]; has {
		return nil, 0, fmt.Errorf("%v", e)
	}
	enc, _ := m["data"].(string)
	raw, derr := base64.StdEncoding.DecodeString(enc)
	if derr != nil {
		return nil, 0, fmt.Errorf("decode fs-read window: %w", derr)
	}
	next := offset + int64(len(raw))
	if v, ok := m["next_offset"].(float64); ok {
		next = int64(v)
	}
	return raw, next, nil
}

// handleV1FSRawUpload takes one raw chunk of an upload.
// POST /api/v1/agents/{id}/fs/upload?path=<remote path>
// Headers: X-Probe-Offset (bytes already on the node), X-Probe-Total-Size,
// X-Probe-Sha256 (whole file, optional), Content-Range (alternative to the two
// X-Probe headers).
// Body: the chunk's raw bytes.
//
// Idempotence: a chunk re-sent at an offset already written is accepted and
// rewrites the same region (same offset, same bytes ⇒ same file). An offset
// beyond the part file's size is a gap and is refused, never guessed.
func (s *Server) handleV1FSRawUpload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "path query parameter is required")
		return
	}

	offset, total, crErr := uploadOffsets(r)
	if crErr != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", crErr.Error())
		return
	}

	body, err := readLimited(r.Body, rawUploadMaxChunk)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "INVALID_PARAMS",
			fmt.Sprintf("chunk exceeds %d bytes or could not be read: %v", rawUploadMaxChunk, err))
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "empty chunk body")
		return
	}

	partPath := path + ".part"
	partSize, exists, _, err := s.remoteStat(agentID, partPath)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
		return
	}
	if !exists {
		partSize = 0
	}
	if offset > partSize {
		writeErrorDetail(w, http.StatusConflict, "INTEGRITY_MISMATCH",
			fmt.Sprintf("upload offset %d is past the part file size %d; resume from the declared offset", offset, partSize),
			map[string]interface{}{"expected_offset": partSize, "requested_offset": offset, "part_path": partPath})
		return
	}

	if _, err := s.remoteWriteChunk(agentID, partPath, offset, body, offset == 0); err != nil {
		writeError(w, http.StatusConflict, "INTEGRITY_MISMATCH", "chunk write failed: "+err.Error())
		return
	}

	newSize, _, _, err := s.remoteStat(agentID, partPath)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
		return
	}
	next := offset + int64(len(body))
	if newSize < next {
		writeErrorDetail(w, http.StatusConflict, "INTEGRITY_MISMATCH",
			fmt.Sprintf("part file holds %d bytes after writing up to %d", newSize, next),
			map[string]interface{}{"expected_offset": next, "part_size": newSize, "part_path": partPath})
		return
	}

	sum := sha256.Sum256(body)
	resp := map[string]interface{}{
		"path":         path,
		"part_path":    partPath,
		"offset":       offset,
		"written":      len(body),
		"next_offset":  next,
		"size":         newSize,
		"sha256":       hex.EncodeToString(sum[:]),
		"sha256_scope": "window",
		"state":        "uploading",
	}
	if total > 0 {
		resp["total"] = total
	}
	resp["upload_id"] = s.transferMgr.TrackRawUpload(agentID, path, partPath, total, next).ID

	if total > 0 && next >= total {
		whole, size, err := s.remoteHash(agentID, partPath)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
			return
		}
		if want := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Probe-Sha256"))); want != "" && want != whole {
			rejected := path + ".rejected"
			_, _ = s.forwardToAgent(agentID, protocol.TypeFSMove, protocol.FSParams{From: partPath, To: rejected})
			writeErrorDetail(w, http.StatusConflict, "INTEGRITY_MISMATCH",
				"whole-file sha256 does not match the declared digest; the part file was renamed to .rejected and the target is untouched",
				map[string]interface{}{"expected": want, "actual": whole, "rejected_path": rejected})
			return
		}
		if size != total {
			writeErrorDetail(w, http.StatusConflict, "INTEGRITY_MISMATCH",
				fmt.Sprintf("part file is %d bytes, declared total is %d", size, total),
				map[string]interface{}{"expected": total, "actual": size, "part_path": partPath})
			return
		}
		if _, err := s.forwardToAgent(agentID, protocol.TypeFSMove, protocol.FSParams{From: partPath, To: path}); err != nil {
			writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", "finalize rename failed: "+err.Error())
			return
		}
		resp["state"] = "completed"
		resp["file_sha256"] = whole
		resp["size"] = size
	}
	writeJSON(w, http.StatusAccepted, resp)
}

// uploadOffsets reads the resume offset and total from the X-Probe headers or a
// Content-Range header ("bytes start-end/total").
func uploadOffsets(r *http.Request) (offset, total int64, err error) {
	if raw := strings.TrimSpace(r.Header.Get("X-Probe-Offset")); raw != "" {
		offset, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("invalid X-Probe-Offset %q", raw)
		}
	}
	if raw := strings.TrimSpace(r.Header.Get("X-Probe-Total-Size")); raw != "" {
		total, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || total < 0 {
			return 0, 0, fmt.Errorf("invalid X-Probe-Total-Size %q", raw)
		}
	}
	if cr := strings.TrimSpace(r.Header.Get("Content-Range")); cr != "" {
		start, _, size, ok := parseContentRange(cr)
		if !ok {
			return 0, 0, fmt.Errorf("invalid Content-Range %q (want bytes start-end/total)", cr)
		}
		if r.Header.Get("X-Probe-Offset") == "" {
			offset = start
		}
		if r.Header.Get("X-Probe-Total-Size") == "" {
			total = size
		}
	}
	return offset, total, nil
}

func parseContentRange(h string) (start, end, total int64, ok bool) {
	h = strings.TrimSpace(h)
	if !strings.HasPrefix(strings.ToLower(h), "bytes ") {
		return 0, 0, 0, false
	}
	h = strings.TrimSpace(h[len("bytes "):])
	slash := strings.Index(h, "/")
	if slash < 0 {
		return 0, 0, 0, false
	}
	rangePart, totalPart := h[:slash], h[slash+1:]
	dash := strings.Index(rangePart, "-")
	if dash < 0 {
		return 0, 0, 0, false
	}
	s, err1 := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	e, err2 := strconv.ParseInt(strings.TrimSpace(rangePart[dash+1:]), 10, 64)
	total = 0
	var err3 error
	if strings.TrimSpace(totalPart) != "*" {
		total, err3 = strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
	}
	if err1 != nil || err2 != nil || err3 != nil || s < 0 || e < s {
		return 0, 0, 0, false
	}
	return s, e, total, true
}

// handleV1FSDownload streams a file from the agent, honouring Range.
// GET /api/v1/agents/{id}/fs/download?path=<remote path>
func (s *Server) handleV1FSDownload(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("id")
	if _, ok := s.v1CheckAuth(w, r, "fs-read"); !ok {
		return
	}
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		writeError(w, http.StatusBadRequest, "INVALID_PARAMS", "path query parameter is required")
		return
	}
	size, exists, modTime, err := s.remoteStat(agentID, path)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such file on the agent: "+path)
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", fmt.Sprintf("W/\"%d-%d\"", size, modTime))

	cap := fsReadMaxBytes()
	var start, end int64
	status := http.StatusOK
	rangeHeader := strings.TrimSpace(r.Header.Get("Range"))

	if size == 0 {
		w.Header().Set("Content-Length", "0")
		w.Header().Set("X-Probe-Sha256", hex.EncodeToString([]byte{}))
		w.Header().Set("X-Probe-Sha256-Scope", "window")
		w.WriteHeader(status)
		return
	}

	if rangeHeader == "" {
		start, end = 0, size-1
		if cap > 0 && size > cap {
			end = cap - 1
			status = http.StatusPartialContent
		}
	} else {
		var ok, unsatisfiable bool
		start, end, ok, unsatisfiable = parseByteRange(rangeHeader, size)
		if unsatisfiable || !ok {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			writeErrorDetail(w, http.StatusRequestedRangeNotSatisfiable, "INVALID_PARAMS",
				fmt.Sprintf("range %q is not satisfiable for a %d-byte file", rangeHeader, size),
				map[string]interface{}{"size": size, "range": rangeHeader})
			return
		}
		if cap > 0 && end-start+1 > cap {
			end = start + cap - 1
		}
		status = http.StatusPartialContent
	}

	body, next, err := s.readRemoteRange(agentID, path, start, end)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "AGENT_UNAVAILABLE", err.Error())
		return
	}

	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+int64(len(body))-1, size))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	sum := sha256.Sum256(body)
	w.Header().Set("X-Probe-Sha256", hex.EncodeToString(sum[:]))
	w.Header().Set("X-Probe-Sha256-Scope", "window")
	if next < size {
		w.Header().Set("X-Probe-Next-Offset", strconv.FormatInt(next, 10))
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// readRemoteRange assembles [start,end] from the agent in bounded windows.
func (s *Server) readRemoteRange(agentID, path string, start, end int64) ([]byte, int64, error) {
	const window = 1 << 20
	out := make([]byte, 0, end-start+1)
	offset := start
	for offset <= end {
		limit := end - offset + 1
		if limit > window {
			limit = window
		}
		chunk, next, err := s.remoteReadWindow(agentID, path, offset, limit)
		if err != nil {
			return nil, offset, err
		}
		if len(chunk) == 0 {
			break
		}
		out = append(out, chunk...)
		offset = next
	}
	return out, offset, nil
}

// parseByteRange parses a single-range header. ok=false with unsatisfiable=true
// means the range is syntactically valid but cannot be served (RFC 7233 → 416).
func parseByteRange(h string, size int64) (start, end int64, ok, unsatisfiable bool) {
	if !strings.HasPrefix(strings.ToLower(h), "bytes=") {
		return 0, 0, false, false
	}
	spec := strings.TrimSpace(h[len("bytes="):])
	if strings.Contains(spec, ",") {
		// Multi-range is legal but not needed here and guessing would be worse
		// than refusing: a client that asks for several ranges gets one.
		spec = strings.TrimSpace(strings.Split(spec, ",")[0])
	}
	dash := strings.Index(spec, "-")
	if dash < 0 {
		return 0, 0, false, false
	}
	left, right := strings.TrimSpace(spec[:dash]), strings.TrimSpace(spec[dash+1:])
	if left == "" {
		// suffix form: last N bytes
		n, err := strconv.ParseInt(right, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		if n > size {
			n = size
		}
		return size - n, size - 1, true, false
	}
	s, err := strconv.ParseInt(left, 10, 64)
	if err != nil || s < 0 {
		return 0, 0, false, false
	}
	if s >= size {
		return 0, 0, false, true
	}
	e := size - 1
	if right != "" {
		parsed, err := strconv.ParseInt(right, 10, 64)
		if err != nil || parsed < s {
			return 0, 0, false, false
		}
		if parsed < e {
			e = parsed
		}
	}
	return s, e, true, false
}

// readLimited reads at most max bytes and reports an error when the body is
// larger, so an oversized chunk is refused rather than silently truncated.
func readLimited(r interface{ Read([]byte) (int, error) }, max int64) ([]byte, error) {
	buf := make([]byte, 0, 64<<10)
	tmp := make([]byte, 64<<10)
	var total int64
	for {
		n, err := r.Read(tmp)
		if n > 0 {
			total += int64(n)
			if total > max {
				return nil, fmt.Errorf("body larger than %d bytes", max)
			}
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return buf, nil
			}
			return buf, nil
		}
		if n == 0 {
			return buf, nil
		}
	}
}
