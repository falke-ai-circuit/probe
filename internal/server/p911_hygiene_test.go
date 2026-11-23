package server

// P9-11 — artifact hygiene. Five contracts:
//
//  1. startup creates the configured artifacts directory (0755) and logs the
//     resolved path exactly once;
//  2. a binary_path outside the managed directory is refused with 400 and
//     never copied;
//  3. build_id resolves to an artifact INSIDE the managed directory;
//  4. a streamed multipart upload lands in the managed directory and reports
//     the sha256 of the exact bytes written;
//  5. retention prunes the oldest artifact per (agent|name) and never the
//     newest nor one an in-flight update references.
//
// Before this unit "/tmp/probe-files/" was hardcoded in seven non-test places:
// a container recreate destroyed every deliverable and the documented update
// procedure degraded into hand-copying onto the host.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// p911Server builds a server whose managed artifacts directory is a temp dir,
// with one mock agent connected so the update handler completes instead of
// waiting on the 30s forward timeout.
func p911Server(t *testing.T, agentID string) (*Server, *updateMockAgent, string) {
	t.Helper()
	dir := t.TempDir()
	files := filepath.Join(dir, "artifacts")
	srv := NewServer("", "test-token", filepath.Join(dir, "registry.json"))
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	if err := srv.SetFilesDir(files); err != nil {
		t.Fatalf("SetFilesDir(%s): %v", files, err)
	}
	agent := &updateMockAgent{srv: srv}
	srv.mu.Lock()
	srv.conns[agentID] = agent
	srv.connWriteMu[agentID] = &sync.Mutex{}
	srv.mu.Unlock()
	srv.registry.Register(agentID, agentID, "1.18.6", "windows", "amd64", "silent",
		[]string{"exec", "filesystem", "update"})
	t.Cleanup(func() { srv.registry.Stop() })
	return srv, agent, files
}

// TestP911StartupCreatesAndLogsFilesDir proves the configured directory is
// created at startup with 0755 and that the resolved path is logged exactly
// once, however many times the setter runs.
func TestP911StartupCreatesAndLogsFilesDir(t *testing.T) {
	var logBuf bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldWriter)

	dir := filepath.Join(t.TempDir(), "nested", "artifacts")
	srv := NewServer("", "test-token", "")
	t.Cleanup(func() { srv.registry.Stop() })

	if err := srv.SetFilesDir(dir); err != nil {
		t.Fatalf("SetFilesDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("configured artifacts dir was not created: %v", err)
	}
	if !fi.IsDir() {
		t.Fatalf("%s exists but is not a directory", dir)
	}
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("artifacts dir mode = %v, want 0755", fi.Mode().Perm())
	}
	if got := srv.FilesDir(); got != dir {
		t.Fatalf("FilesDir() = %q, want %q", got, dir)
	}

	want := "artifacts dir: " + dir
	if n := strings.Count(logBuf.String(), want); n != 1 {
		t.Fatalf("resolved path logged %d times, want exactly 1 (log=%q)", n, logBuf.String())
	}

	// A second resolution (config reload, repeated call) must not log again.
	if err := srv.SetFilesDir(dir); err != nil {
		t.Fatalf("second SetFilesDir: %v", err)
	}
	if n := strings.Count(logBuf.String(), want); n != 1 {
		t.Fatalf("resolved path logged %d times after second call, want 1", n)
	}
}

// TestP911UpdateRejectsBinaryPathOutsideManagedDir proves the managed-path
// gate: a path outside the artifacts directory is a 400 naming the
// remediation, and nothing is staged.
func TestP911UpdateRejectsBinaryPathOutsideManagedDir(t *testing.T) {
	const agentID = "TEST-P911-OUTSIDE"
	srv, agent, files := p911Server(t, agentID)

	outside := filepath.Join(t.TempDir(), "probe.exe")
	if err := os.WriteFile(outside, []byte("outside binary"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		outside,                          // arbitrary host path
		"/tmp/probe-files/probe.exe",     // the literal this unit removes
		"/data/files/../escape/probe.exe", // traversal out of the managed dir
	} {
		rec := postUpdate(srv, agentID, map[string]interface{}{
			"binary_path": path,
			"version":     "1.19.1",
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("binary_path %q: status = %d, want 400 (body=%s)", path, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, "outside the managed artifacts directory") {
			t.Fatalf("binary_path %q: body = %q, want the outside-directory refusal", path, body)
		}
		if !strings.Contains(body, "POST /api/v1/files") {
			t.Fatalf("binary_path %q: refusal does not name the upload route: %s", path, body)
		}
	}

	if _, err := os.Stat(filepath.Join(files, "probe.exe")); !os.IsNotExist(err) {
		t.Fatalf("a refused path still staged a file in the managed dir (err=%v)", err)
	}
	if _, ok := agent.last(); ok {
		t.Fatal("a refused path still sent an agent_update to the agent")
	}
}

// TestP911UpdateBuildIDResolvesInsideManagedDir proves build_id stages the
// build's artifact into the managed directory and delivers that digest.
func TestP911UpdateBuildIDResolvesInsideManagedDir(t *testing.T) {
	const agentID = "TEST-P911-BUILD"
	srv, agent, files := p911Server(t, agentID)

	payload := []byte("built agent binary bytes")
	src := filepath.Join(t.TempDir(), "build-p911_probe.exe")
	if err := os.WriteFile(src, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed a completed build record (CreateBuild would start a real compiler).
	srv.builder.mu.Lock()
	srv.builder.builds["build-p911"] = &BuildConfig{
		ID:         "build-p911",
		Name:       "p911",
		OS:         "windows",
		Arch:       "amd64",
		Status:     BuildStatusComplete,
		BinaryPath: src,
	}
	srv.builder.mu.Unlock()

	rec := postUpdate(srv, agentID, map[string]interface{}{
		"build_id": "build-p911",
		"version":  "1.19.1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}

	staged := filepath.Join(files, "build-p911_probe.exe")
	onDisk, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("build artifact not staged into the managed dir: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("staged build bytes = %q, want %q", onDisk, payload)
	}
	if !strings.HasPrefix(staged, files+string(os.PathSeparator)) {
		t.Fatalf("staged path %q is not inside the managed dir %q", staged, files)
	}

	delivered, ok := agent.last()
	if !ok {
		t.Fatal("agent received no agent_update message")
	}
	if delivered.Filename != "build-p911_probe.exe" {
		t.Fatalf("delivered filename = %q, want build-p911_probe.exe", delivered.Filename)
	}
	if delivered.SHA256 != shaOf(payload) {
		t.Fatalf("delivered sha256 = %q, want %q", delivered.SHA256, shaOf(payload))
	}
}

// TestP911UploadWritesManagedDir proves POST /api/v1/files streams raw bytes
// into the managed directory and reports the digest of exactly those bytes.
func TestP911UploadWritesManagedDir(t *testing.T) {
	srv, _, files := p911Server(t, "TEST-P911-UPLOAD")

	payload := bytes.Repeat([]byte("MZ\x90\x00"), 2048) // 8 KiB of binary
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("agent", "TEST-P911-UPLOAD"); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("name", "probe-upload.exe"); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "ignored-name.bin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	srv.handleV1FileUpload(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body=%s)", rec.Code, rec.Body.String())
	}
	var resp APIResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp.OK || resp.Error != nil {
		t.Fatalf("response not ok: %s", rec.Body.String())
	}
	data, ok := resp.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("response data = %T, want object", resp.Data)
	}
	if got := data["name"]; got != "probe-upload.exe" {
		t.Fatalf("name = %v, want probe-upload.exe", got)
	}
	gotSHA, _ := data["sha256"].(string)
	if gotSHA != shaOf(payload) {
		t.Fatalf("sha256 = %q, want %q", gotSHA, shaOf(payload))
	}
	gotSize, _ := data["size"].(float64)
	if int64(gotSize) != int64(len(payload)) {
		t.Fatalf("size = %v, want %d", data["size"], len(payload))
	}
	path, _ := data["path"].(string)
	if got := filepath.Dir(path); got != files {
		t.Fatalf("path dir = %q, want managed dir %q", got, files)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("uploaded file not readable at %s: %v", path, err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatal("on-disk bytes differ from uploaded bytes (base64 or corruption in the path)")
	}
	if _, err := os.Stat(filepath.Join(files, "probe-upload.exe")); err != nil {
		t.Fatalf("explicit name not honoured inside the managed dir: %v", err)
	}
}

// TestP911RetentionPrunesOldestOnly proves retention keeps the newest N per
// (agent|name) and never deletes an artifact an in-flight update references.
func TestP911RetentionPrunesOldestOnly(t *testing.T) {
	const agentID = "TEST-P911-RET"
	srv, _, files := p911Server(t, agentID)

	// Seed three versions of one logical name with a wide keep so registration
	// itself does not prune, then tighten the budget and prune explicitly.
	srv.SetFilesKeep(10)
	names := []string{"a.exe", "b.exe", "c.exe"}
	paths := make(map[string]string, len(names))
	for i, n := range names {
		p := filepath.Join(files, n)
		content := []byte(fmt.Sprintf("version-%d", i))
		if err := os.WriteFile(p, content, 0o644); err != nil {
			t.Fatal(err)
		}
		paths[n] = p
		srv.registerArtifact(agentID, "probe.exe", n, p, shaOf(content), int64(len(content)))
		time.Sleep(2 * time.Millisecond) // deterministic CreatedAt ordering
	}

	// b.exe is mid-delivery: prune must not touch it.
	srv.markArtifactInFlight(paths["b.exe"])
	srv.SetFilesKeep(1)

	if pruned := srv.PruneArtifacts(agentID, "probe.exe"); pruned != 1 {
		t.Fatalf("pruned %d artifacts, want 1 (only the oldest outside the newest keep)", pruned)
	}
	if _, err := os.Stat(paths["a.exe"]); !os.IsNotExist(err) {
		t.Fatalf("oldest artifact was not pruned (err=%v)", err)
	}
	for _, keep := range []string{"b.exe", "c.exe"} {
		if _, err := os.Stat(paths[keep]); err != nil {
			t.Fatalf("artifact %s was deleted but must survive (in-flight=%v): %v", keep, keep == "b.exe", err)
		}
	}

	// The newest is never a candidate, and the in-flight file stays pinned even
	// when that pin consumes the whole retention budget: add a fourth version
	// and let registration prune inline. c.exe is now neither the newest nor
	// in-flight, so it is the correct casualty.
	d := filepath.Join(files, "d.exe")
	content := []byte("version-3")
	if err := os.WriteFile(d, content, 0o644); err != nil {
		t.Fatal(err)
	}
	srv.registerArtifact(agentID, "probe.exe", "d.exe", d, shaOf(content), int64(len(content)))
	if _, err := os.Stat(d); err != nil {
		t.Fatalf("newest artifact was pruned: %v", err)
	}
	if _, err := os.Stat(paths["b.exe"]); err != nil {
		t.Fatalf("in-flight artifact was pruned by a later registration: %v", err)
	}
	if _, err := os.Stat(paths["c.exe"]); !os.IsNotExist(err) {
		t.Fatalf("c.exe is neither newest nor in-flight and should have been pruned (err=%v)", err)
	}
}
