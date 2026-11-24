package server

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// P11-02: both the update path and the builder redeploy path must derive the
// download host the same way and must carry ?token=; GET /download/{file}
// answers 401 without it.

func newDownloadURLTestServer(t *testing.T, addr, token, filesDir string) *Server {
	t.Helper()
	s := NewServer(addr, token, filepath.Join(t.TempDir(), "registry.json"))
	s.requireAPIAuth = true
	s.SetFilesDir(filesDir)
	return s
}

func TestDownloadURL_HostResolutionAndToken(t *testing.T) {
	s := newDownloadURLTestServer(t, ":18080", "secret-token", t.TempDir())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a1/redeploy", nil)
	req.Host = "10.20.30.40:18080"

	cases := []struct {
		name     string
		req      *http.Request
		override string
		want     string
	}{
		{
			name: "runtime host from request",
			req:  req,
			want: "http://10.20.30.40:18080/download/PROBE_v1.19.0_windows_amd64.exe?token=secret-token",
		},
		{
			name:     "explicit download_host wins",
			req:      req,
			override: "probe.example.net:443",
			want:     "http://probe.example.net:443/download/PROBE_v1.19.0_windows_amd64.exe?token=secret-token",
		},
		{
			// Builder path: the redeploy goroutine has no request, so the
			// handler passes the host it captured as the override.
			name:     "builder path, captured caller host",
			req:      nil,
			override: "10.20.30.40:18080",
			want:     "http://10.20.30.40:18080/download/PROBE_v1.19.0_windows_amd64.exe?token=secret-token",
		},
		{
			name: "listen-address fallback",
			req:  nil,
			want: "http://localhost:18080/download/PROBE_v1.19.0_windows_amd64.exe?token=secret-token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := s.downloadURL(tc.req, tc.override, "PROBE_v1.19.0_windows_amd64.exe")
			if got != tc.want {
				t.Fatalf("downloadURL = %q, want %q", got, tc.want)
			}
			if !strings.Contains(got, "?token=") {
				t.Fatalf("URL carries no token: %q", got)
			}
			if tc.req != nil || tc.override != "" {
				if strings.Contains(got, "localhost") {
					t.Fatalf("caller-reachable host substituted with localhost: %q", got)
				}
			}
		})
	}
}

// startDownloadListener serves handleFileDownload on a real TCP socket bound to
// 127.0.0.2, i.e. an address that is not "localhost" as far as the URL is
// concerned.
func startDownloadListener(t *testing.T, s *Server, addr string) (base string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(s.handleFileDownload)}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }
}

func TestDownloadURL_LiveDownloadRequiresToken(t *testing.T) {
	filesDir := t.TempDir()
	payload := []byte("P11-02 artifact payload: download-url correctness\n")
	name := "P11-02_artifact.bin"
	if err := os.WriteFile(filepath.Join(filesDir, name), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	wantSum := sha256.Sum256(payload)

	s := newDownloadURLTestServer(t, ":18080", "secret-token", filesDir)
	base, stop := startDownloadListener(t, s, "127.0.0.2:0")
	defer stop()

	// With ?token=: 200 and byte-identical body.
	withToken := base + "/download/" + name + "?token=secret-token"
	resp, err := http.Get(withToken)
	if err != nil {
		t.Fatalf("GET %s: %v", withToken, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token-bearing GET = %d, want 200 (body %q)", resp.StatusCode, body)
	}
	if gotSum := sha256.Sum256(body); gotSum != wantSum {
		t.Fatalf("downloaded sha256 = %x, want %x", gotSum, wantSum)
	}
	t.Logf("WITH token: GET %s -> %d, %d bytes, sha256 %s",
		withToken, resp.StatusCode, len(body), hex.EncodeToString(wantSum[:]))

	// Without ?token=: 401, unchanged.
	noToken := base + "/download/" + name
	resp, err = http.Get(noToken)
	if err != nil {
		t.Fatalf("GET %s: %v", noToken, err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("token-less GET = %d, want 401 (body %q)", resp.StatusCode, body)
	}
	t.Logf("WITHOUT token: GET %s -> %d", noToken, resp.StatusCode)
}

// TestDownloadURL_LiveCrossProcessProof is the same handler on a fixed
// 127.0.0.2 socket kept open for `curl` run by a separate shell, so the
// request/response pair is captured from another process. Skipped unless
// P11_02_LIVE=1.
func TestDownloadURL_LiveCrossProcessProof(t *testing.T) {
	if os.Getenv("P11_02_LIVE") != "1" {
		t.Skip("set P11_02_LIVE=1 to hold the listener open for curl")
	}
	filesDir := os.Getenv("P11_02_FILES_DIR")
	if filesDir == "" {
		t.Fatal("P11_02_FILES_DIR is required in live mode")
	}
	s := newDownloadURLTestServer(t, ":18080", "secret-token", filesDir)
	base, stop := startDownloadListener(t, s, "127.0.0.2:7791")
	defer stop()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/a1/redeploy", nil)
	req.Host = "127.0.0.2:7791"
	fmt.Printf("P11-02 URL returned for the build artifact:\n%s\n", s.downloadURL(nil, req.Host, "P11-02_artifact.bin"))

	os.WriteFile("/tmp/p11_02_live_ready", []byte(base), 0o644)
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat("/tmp/p11_02_live_stop"); err == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("live listener timed out waiting for /tmp/p11_02_live_stop")
}
