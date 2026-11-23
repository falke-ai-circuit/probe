package server

// P9-11 — artifact hygiene.
//
// Every artifact the server produces or accepts — build output copies,
// /download delivery, transfer staging and update payloads — resolves through
// ONE directory: Server.FilesDir(), inside the persistent /data volume,
// configurable with --files-dir / PROBE_FILES_DIR.
//
// Measured defect this file closes: the old download directory was hardcoded
// in seven non-test places. A container recreate destroys it, so every
// deliverable vanished and the documented update procedure degraded into
// hand-copying files onto the host — the exact thing the owner forbids.

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
	"sort"
	"strings"
	"time"
)

const (
	// DefaultFilesDir is the managed artifacts directory. It sits inside the
	// persistent /data volume the container maps, so a recreate keeps the
	// artifacts the API serves.
	DefaultFilesDir = "/data/files"

	// DefaultFilesKeep is how many artifacts are retained per (agent|name).
	DefaultFilesKeep = 5

	// artifactsLedgerName is the retention ledger kept beside the artifacts.
	artifactsLedgerName = ".artifacts.json"

	// uploadMaxBytes bounds one streamed upload. Artifacts are executables;
	// 512 MiB leaves room for a large agent plus resources while stopping a
	// runaway body from filling the volume. The body is streamed, never
	// buffered whole and never base64'd (an 8 MB exe as base64 already took
	// the server down once).
	uploadMaxBytes int64 = 512 << 20

	// inflightArtifactTTL stops a crashed update from pinning an artifact
	// forever: a delivery that never reports completion stops protecting its
	// file after 30 minutes.
	inflightArtifactTTL = 30 * time.Minute
)

// artifactRecord is one managed file the server knows how to serve. Bucket is
// the logical artifact name a retention bucket is keyed by; Name is the stored
// file's name, which may carry a version suffix so history is kept.
type artifactRecord struct {
	Name      string    `json:"name"`
	Bucket    string    `json:"bucket,omitempty"`
	Path      string    `json:"path"`
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	Agent     string    `json:"agent,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// artifactsLedger is the on-disk retention ledger. Items is keyed
// "agent|name" so retention buckets never mix two agents' binaries.
type artifactsLedger struct {
	Items map[string][]artifactRecord `json:"items"`
}

// ---------------------------------------------------------------------------
// Directory resolution
// ---------------------------------------------------------------------------

// SetFilesDir resolves and creates the managed artifacts directory (0755) and
// logs the resolved path exactly once per server process. An empty dir selects
// DefaultFilesDir. Startup is the intended caller: it fails loud so the
// operator learns immediately that the volume is not writable, rather than at
// the first build.
func (s *Server) SetFilesDir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultFilesDir
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolve artifacts dir %q: %w", dir, err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("create artifacts dir %s: %w", abs, err)
	}
	s.filesMu.Lock()
	s.filesDir = abs
	first := !s.filesDirLogged
	s.filesDirLogged = true
	keep := s.filesKeep
	s.filesMu.Unlock()
	if first {
		if keep <= 0 {
			keep = DefaultFilesKeep
		}
		log.Printf("[server] artifacts dir: %s (mode 0755, keep latest %d per agent|name)", abs, keep)
	}
	return nil
}

// FilesDir returns the resolved managed artifacts directory. Before SetFilesDir
// runs (tests, embedded use) it reports the configured default without creating
// it; ensureFilesDir is the creating path.
func (s *Server) FilesDir() string {
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	if s.filesDir != "" {
		return s.filesDir
	}
	return DefaultFilesDir
}

// SetFilesKeep sets the per-(agent|name) retention count. Values below 1 fall
// back to DefaultFilesKeep; the newest artifact is therefore never pruned.
func (s *Server) SetFilesKeep(n int) {
	if n < 1 {
		n = DefaultFilesKeep
	}
	s.filesMu.Lock()
	s.filesKeep = n
	s.filesMu.Unlock()
}

func (s *Server) filesKeepN() int {
	s.filesMu.RLock()
	defer s.filesMu.RUnlock()
	if s.filesKeep > 0 {
		return s.filesKeep
	}
	return DefaultFilesKeep
}

// ensureFilesDir creates the managed directory (0755) if it is missing. Used by
// write paths so a server started without SetFilesDir still has a home for its
// artifacts rather than serving from a non-existent path.
func (s *Server) ensureFilesDir() error {
	dir := s.FilesDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create artifacts dir %s: %w", dir, err)
	}
	return nil
}

// ManagedPath resolves a bare artifact file name inside the managed directory
// and refuses anything that could escape it. Names are flat on purpose: the
// /download route rejects separators, and a single directory is what makes
// builds, downloads, transfers and updates agree by construction.
func (s *Server) ManagedPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("artifact name is empty")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("invalid artifact name %q: expected a bare file name", name)
	}
	return filepath.Join(s.FilesDir(), name), nil
}

// ResolveManagedFilePath accepts either an absolute path already inside the
// managed artifacts directory or a bare file name, and refuses every other
// path. This is the gate that keeps binary_path from pointing at /tmp, /root or
// any other caller-chosen location: the only persistent, API-visible place for
// a deliverable is FilesDir.
func (s *Server) ResolveManagedFilePath(p string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(p))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("binary_path is empty")
	}
	dir := filepath.Clean(s.FilesDir())
	if filepath.IsAbs(clean) {
		rel, err := filepath.Rel(dir, clean)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("binary_path %q is outside the managed artifacts directory %s — upload it with POST /api/v1/files or use build_id", p, dir)
		}
		return clean, nil
	}
	return s.ManagedPath(clean)
}

// ---------------------------------------------------------------------------
// Retention ledger
// ---------------------------------------------------------------------------

func artifactKey(agent, name string) string {
	if agent == "" {
		agent = "_server"
	}
	return agent + "|" + name
}

// loadLedgerLocked reads the retention ledger from disk. Caller holds artifactMu.
// A missing or corrupt ledger is an empty one: retention must never fail a
// delivery, and the data lost is only pruning history.
func (s *Server) loadLedgerLocked() {
	if s.artifactLedger != nil {
		return
	}
	s.artifactLedger = make(map[string][]artifactRecord)
	raw, err := os.ReadFile(filepath.Join(s.FilesDir(), artifactsLedgerName))
	if err != nil {
		return
	}
	var ledger artifactsLedger
	if err := json.Unmarshal(raw, &ledger); err != nil {
		log.Printf("[files] ignoring unreadable retention ledger %s: %v", artifactsLedgerName, err)
		return
	}
	for k, v := range ledger.Items {
		s.artifactLedger[k] = v
	}
}

// saveLedgerLocked persists the ledger atomically (temp + rename) with 0600.
// Caller holds artifactMu.
func (s *Server) saveLedgerLocked() {
	raw, err := json.MarshalIndent(artifactsLedger{Items: s.artifactLedger}, "", "  ")
	if err != nil {
		log.Printf("[files] encode retention ledger: %v", err)
		return
	}
	dir := s.FilesDir()
	tmp, err := os.CreateTemp(dir, ".artifacts-*.tmp")
	if err != nil {
		log.Printf("[files] write retention ledger: %v", err)
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		log.Printf("[files] write retention ledger: %v", err)
		return
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		log.Printf("[files] write retention ledger: %v", err)
		return
	}
	_ = os.Chmod(tmpName, 0o600)
	if err := os.Rename(tmpName, filepath.Join(dir, artifactsLedgerName)); err != nil {
		os.Remove(tmpName)
		log.Printf("[files] commit retention ledger: %v", err)
	}
}

// registerArtifact records a managed file under (agent|bucket), replacing any
// earlier record for the same path, persists the ledger and prunes the bucket.
// bucket is the logical artifact name; name is the stored file name.
// Registration is best-effort: a ledger failure must not fail the delivery.
func (s *Server) registerArtifact(agent, bucket, name, path, sha string, size int64) {
	if bucket == "" || path == "" {
		return
	}
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.loadLedgerLocked()
	key := artifactKey(agent, bucket)
	rec := artifactRecord{
		Name:      name,
		Bucket:    bucket,
		Path:      path,
		SHA256:    sha,
		Size:      size,
		Agent:     agent,
		CreatedAt: time.Now().UTC(),
	}
	kept := make([]artifactRecord, 0, len(s.artifactLedger[key])+1)
	for _, old := range s.artifactLedger[key] {
		if old.Path != path {
			kept = append(kept, old)
		}
	}
	s.artifactLedger[key] = append(kept, rec)
	s.saveLedgerLocked()
	s.pruneLocked(key)
}

// pruneLocked deletes the oldest artifacts in one bucket until at most keep
// remain, never touching the newest records and never touching a record an
// in-flight update references. Caller holds artifactMu. Returns the number of
// files removed.
//
// Prune candidates are strictly the prefix older than the newest `keep`
// records: the newest artifact can never be a candidate, even when in-flight
// pins on older files would otherwise push the quota down onto it.
func (s *Server) pruneLocked(key string) int {
	recs := s.artifactLedger[key]
	keep := s.filesKeepN()
	if keep < 1 {
		keep = DefaultFilesKeep
	}
	if len(recs) <= keep {
		return 0
	}
	ordered := append([]artifactRecord(nil), recs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].CreatedAt.Before(ordered[j].CreatedAt)
	})

	cut := ordered[:len(ordered)-keep]
	newest := ordered[len(ordered)-keep:]

	kept := make([]artifactRecord, 0, len(ordered))
	pruned := 0
	for _, rec := range cut {
		if s.artifactInFlight(rec.Path) {
			// The agent may still be downloading this exact file: it counts
			// toward retention but is not deletable.
			kept = append(kept, rec)
			continue
		}
		if err := os.Remove(rec.Path); err != nil && !os.IsNotExist(err) {
			kept = append(kept, rec)
			continue
		}
		pruned++
		log.Printf("[files] pruned artifact %s (key=%s keep=%d sha=%s)", rec.Path, key, keep, shortSHA(rec.SHA256))
	}
	kept = append(kept, newest...)
	s.artifactLedger[key] = kept
	s.saveLedgerLocked()
	return pruned
}

// PruneArtifacts applies retention to one (agent|name) bucket. Exported for
// tests and for an operator-triggered prune; normal writes prune inline.
func (s *Server) PruneArtifacts(agent, name string) int {
	s.artifactMu.Lock()
	defer s.artifactMu.Unlock()
	s.loadLedgerLocked()
	return s.pruneLocked(artifactKey(agent, name))
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// ---------------------------------------------------------------------------
// In-flight artifacts
// ---------------------------------------------------------------------------

// markArtifactInFlight pins a path so retention cannot delete it while an
// update is delivering it.
func (s *Server) markArtifactInFlight(path string) {
	if path == "" {
		return
	}
	s.inflightMu.Lock()
	if s.inflight == nil {
		s.inflight = make(map[string]time.Time)
	}
	s.inflight[filepath.Clean(path)] = time.Now()
	s.inflightMu.Unlock()
}

// clearArtifactInFlight releases the pin after the update completed.
func (s *Server) clearArtifactInFlight(path string) {
	if path == "" {
		return
	}
	s.inflightMu.Lock()
	delete(s.inflight, filepath.Clean(path))
	s.inflightMu.Unlock()
}

// artifactInFlight reports whether a path is currently being delivered. Pins
// expire after inflightArtifactTTL so a crashed update cannot protect a file
// forever.
func (s *Server) artifactInFlight(path string) bool {
	clean := filepath.Clean(path)
	s.inflightMu.Lock()
	defer s.inflightMu.Unlock()
	since, ok := s.inflight[clean]
	if !ok {
		return false
	}
	if time.Since(since) > inflightArtifactTTL {
		delete(s.inflight, clean)
		return false
	}
	return true
}

// ---------------------------------------------------------------------------
// P9-11 — POST /api/v1/files : streamed multipart upload into the managed dir
// ---------------------------------------------------------------------------

// handleV1FileUpload accepts a streamed multipart/form-data upload and writes it
// into the managed artifacts directory. It is the API route that makes a whole
// update performable with API calls alone: upload -> POST .../update -> observe.
// No shell, no docker cp, no scp.
//
//	POST /api/v1/files
//	Content-Type: multipart/form-data
//	  file  = the artifact bytes (required, streamed)
//	  name  = destination file name (optional; defaults to the part filename)
//	  agent = retention bucket (optional; defaults to _server)
//
// Body is never buffered whole and never base64'd. Response:
//
//	{"ok":true,"data":{"name":"probe.exe","sha256":"…","size":8123456,
//	                   "path":"/data/files/probe.exe"}}
func (s *Server) handleV1FileUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.v1CheckAuth(w, r, "fs-write"); !ok {
		return
	}
	if err := s.ensureFilesDir(); err != nil {
		writeError(w, http.StatusInternalServerError, "FILES_DIR_UNAVAILABLE", err.Error())
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST",
			"multipart/form-data required (streamed upload, no base64): "+err.Error())
		return
	}

	var agent, explicitName string
	written := false
	var (
		name   string
		bucket string // logical artifact name: the retention bucket
		sha    string
		size   int64
		dest   string
	)

	for {
		part, perr := mr.NextPart()
		if perr == io.EOF {
			break
		}
		if perr != nil {
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "read multipart body: "+perr.Error())
			return
		}
		formName := part.FormName()
		partFileName := part.FileName()

		// Value fields.
		if partFileName == "" && (formName == "agent" || formName == "name") {
			val, rerr := io.ReadAll(io.LimitReader(part, 4096))
			part.Close()
			if rerr != nil {
				writeError(w, http.StatusBadRequest, "BAD_REQUEST", "read form field "+formName+": "+rerr.Error())
				return
			}
			if formName == "agent" {
				agent = strings.TrimSpace(string(val))
			} else {
				explicitName = strings.TrimSpace(string(val))
			}
			continue
		}

		// Exactly one file part.
		if written {
			part.Close()
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", "exactly one file part is accepted per request")
			return
		}
		candidate := explicitName
		if candidate == "" {
			candidate = filepath.Base(partFileName)
		}
		if candidate == "" || candidate == "." || candidate == ".." || strings.ContainsAny(candidate, `/\`) {
			part.Close()
			writeError(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("invalid artifact name %q: expected a bare file name", candidate))
			return
		}

		tmp, terr := os.CreateTemp(s.FilesDir(), ".upload-*.part")
		if terr != nil {
			part.Close()
			writeError(w, http.StatusInternalServerError, "FILES_DIR_UNAVAILABLE", "create staging file: "+terr.Error())
			return
		}
		tmpName := tmp.Name()

		h := sha256.New()
		n, cerr := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(part, uploadMaxBytes+1))
		part.Close()
		closeErr := tmp.Close()
		if cerr != nil || closeErr != nil {
			os.Remove(tmpName)
			msg := "stream upload"
			if cerr != nil {
				msg = cerr.Error()
			} else {
				msg = closeErr.Error()
			}
			writeError(w, http.StatusInternalServerError, "UPLOAD_FAILED", msg)
			return
		}
		if n > uploadMaxBytes {
			os.Remove(tmpName)
			writeError(w, http.StatusRequestEntityTooLarge, "TOO_LARGE",
				fmt.Sprintf("artifact exceeds %d bytes", uploadMaxBytes))
			return
		}

		sum := hex.EncodeToString(h.Sum(nil))
		finalPath, ferr := s.commitUploadedArtifact(tmpName, candidate, sum)
		if ferr != nil {
			os.Remove(tmpName)
			writeError(w, http.StatusInternalServerError, "UPLOAD_FAILED", ferr.Error())
			return
		}
		written = true
		bucket = candidate
		name = filepath.Base(finalPath)
		sha = sum
		size = n
		dest = finalPath
	}

	if !written {
		writeError(w, http.StatusBadRequest, "BAD_REQUEST", "multipart body carried no file part")
		return
	}

	s.registerArtifact(agent, bucket, name, dest, sha, size)
	log.Printf("[files] uploaded %s (%d bytes, sha=%s) to %s [agent=%s]",
		name, size, shortSHA(sha), dest, artifactKey(agent, bucket))

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"name":   name,
		"sha256": sha,
		"size":   size,
		"path":   dest,
	})
}

// stageBuildArtifact copies a completed build's binary into the managed
// artifacts directory and returns the managed path. Idempotent: a build whose
// binary already sits in the directory is not copied again.
func (s *Server) stageBuildArtifact(build *BuildConfig) (string, error) {
	name := filepath.Base(build.BinaryPath)
	dest, err := s.ManagedPath(name)
	if err != nil {
		return "", err
	}
	if err := s.ensureFilesDir(); err != nil {
		return "", err
	}
	if filepath.Clean(build.BinaryPath) == filepath.Clean(dest) {
		return dest, nil
	}
	if err := copyFile(build.BinaryPath, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// setUpdateArtifact remembers which managed artifact an agent's in-flight
// update references, so completion can release the retention pin.
func (s *Server) setUpdateArtifact(agentID, path string) {
	if agentID == "" || path == "" {
		return
	}
	s.pendingMu.Lock()
	if s.updateArtifacts == nil {
		s.updateArtifacts = make(map[string]string)
	}
	s.updateArtifacts[agentID] = filepath.Clean(path)
	s.pendingMu.Unlock()
}

// takeUpdateArtifact returns and forgets the artifact an agent's update
// referenced. Used on completion (success or failure) to release the pin.
func (s *Server) takeUpdateArtifact(agentID string) string {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.updateArtifacts[agentID]
	delete(s.updateArtifacts, agentID)
	return p
}

// commitUploadedArtifact moves a staged upload into place. A name collision
// keeps history (the existing file is never destroyed): an identical digest
// reuses the existing name, a different digest gets a timestamped sibling so
// the retention bucket has more than one version to prune.
func (s *Server) commitUploadedArtifact(tmpName, name, sha string) (string, error) {
	target, err := s.ManagedPath(name)
	if err != nil {
		return "", err
	}
	if _, statErr := os.Stat(target); statErr == nil {
		if existing, herr := hashFileServer(target); herr == nil && existing == sha {
			os.Remove(tmpName)
			return target, nil
		}
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		target = filepath.Join(s.FilesDir(), fmt.Sprintf("%s.%d%s", stem, time.Now().UnixNano(), ext))
	}
	if err := os.Rename(tmpName, target); err != nil {
		return "", fmt.Errorf("commit upload: %w", err)
	}
	_ = os.Chmod(target, 0o644)
	return target, nil
}
