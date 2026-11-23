package server

// P9-08 — update-path hardening. Two measured defects in
// internal/server/server_update.go, both reproduced live on 23.11 against
// v1.19.0:
//
//   A. hashing params.BinaryPath unconditionally made a request carrying only
//      ws_remote_path fail with "hash binary failed: open : no such file or
//      directory", so the documented ws_remote_path mode could not work.
//   B. after a successful promotion the "<agent>-canary" registry row was
//      never removed (measured: TEST-OLD-1186=1.19.0 and
//      TEST-OLD-1186-canary=1.19.0), so a completed update looked like a live
//      second agent.
//
// These tests exercise the request contract and the registry cleanup directly.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// updateMockAgent records every agent_update the server sends and answers it,
// so the handler returns without waiting on the 30s forward timeout.
type updateMockAgent struct {
	srv *Server
	mu  sync.Mutex
	got []protocol.AgentUpdateParams
}

func (m *updateMockAgent) WriteMessage(int, []byte) error { return nil }
func (m *updateMockAgent) Close() error                   { return nil }

func (m *updateMockAgent) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	if env.Type != protocol.TypeAgentUpdate {
		return nil
	}
	var p protocol.AgentUpdateParams
	_ = json.Unmarshal(env.Params, &p)
	m.mu.Lock()
	m.got = append(m.got, p)
	m.mu.Unlock()

	m.srv.pendingMu.Lock()
	ch, ok := m.srv.pendingReqs[env.ID]
	m.srv.pendingMu.Unlock()
	if ok {
		ch <- protocol.NewResult(env.ID, protocol.TypeAgentUpdate+"_result",
			map[string]interface{}{"success": true, "old_pid": 100, "new_pid": 200})
	}
	return nil
}

func (m *updateMockAgent) last() (protocol.AgentUpdateParams, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.got) == 0 {
		return protocol.AgentUpdateParams{}, false
	}
	return m.got[len(m.got)-1], true
}

func newUpdateTestServer(t *testing.T, agentID string) (*Server, *updateMockAgent) {
	t.Helper()
	dir := t.TempDir()
	srv := NewServer("", "test-token", filepath.Join(dir, "registry.json"))
	srv.mux = http.NewServeMux()
	srv.registerRoutes()
	agent := &updateMockAgent{srv: srv}
	srv.mu.Lock()
	srv.conns[agentID] = agent
	srv.connWriteMu[agentID] = &sync.Mutex{}
	srv.mu.Unlock()
	srv.registry.Register(agentID, agentID, "1.18.6", "windows", "amd64", "silent",
		[]string{"exec", "filesystem", "update"})
	t.Cleanup(func() { srv.registry.Stop() })
	return srv, agent
}

// postUpdate calls the legacy update handler the way the v1 route does.
func postUpdate(srv *Server, agentID string, body interface{}) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agents/"+agentID+"/update", &buf)
	rec := httptest.NewRecorder()
	srv.handleAgentUpdate(rec, req, agentID)
	return rec
}

func shaOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TestP908UpdateWSRemotePathContract: ws_remote_path mode needs a caller-
// supplied digest, carries it through to the agent, and never touches the
// local filesystem. Before the fix the handler hashed an empty binary_path and
// returned 500 "hash binary failed" for every one of these requests.
func TestP908UpdateWSRemotePathContract(t *testing.T) {
	const agentID = "TEST-OLD-1186"
	remote := `C:\Probe\probe-client.exe.next`
	digest := shaOf([]byte("pre-staged binary bytes"))

	cases := []struct {
		name       string
		body       map[string]interface{}
		wantCode   int
		wantSubstr string
		wantSHA    string
	}{
		{
			name:       "missing sha256 is a 400",
			body:       map[string]interface{}{"ws_remote_path": remote, "version": "1.19.0"},
			wantCode:   http.StatusBadRequest,
			wantSubstr: "sha256 is required with ws_remote_path",
		},
		{
			name:     "ws_remote_path with sha256 reaches the agent",
			body:     map[string]interface{}{"ws_remote_path": remote, "sha256": digest, "version": "1.19.0"},
			wantCode: http.StatusOK,
			wantSHA:  digest,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, agent := newUpdateTestServer(t, agentID)
			rec := postUpdate(srv, agentID, tc.body)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantSubstr != "" && !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
			if strings.Contains(rec.Body.String(), "hash binary failed") {
				t.Fatalf("ws_remote_path mode still hashes binary_path: %s", rec.Body.String())
			}
			if tc.wantSHA != "" {
				got, ok := agent.last()
				if !ok {
					t.Fatal("agent received no agent_update message")
				}
				if got.SHA256 != tc.wantSHA {
					t.Fatalf("agent sha256 = %q, want %q", got.SHA256, tc.wantSHA)
				}
				if got.WSPath != remote {
					t.Fatalf("agent ws_path = %q, want %q", got.WSPath, remote)
				}
				if got.DownloadURL != "" {
					t.Fatalf("agent download_url = %q, want empty for ws mode", got.DownloadURL)
				}
			}
		})
	}
}

// TestP908UpdateBinaryPathSHA256: binary_path keeps its computed-hash
// behaviour, and a caller-supplied sha256 becomes a confirmation — it must
// match, or the request is refused before anything reaches the agent.
func TestP908UpdateBinaryPathSHA256(t *testing.T) {
	const agentID = "TEST-OLD-1187"
	dir := t.TempDir()
	content := []byte("server-local binary payload")
	binPath := filepath.Join(dir, fmt.Sprintf("p908-%d.bin", time.Now().UnixNano()))
	if err := os.WriteFile(binPath, content, 0644); err != nil {
		t.Fatalf("write fixture binary: %v", err)
	}
	correct := shaOf(content)
	wrong := shaOf([]byte("some other binary"))

	cases := []struct {
		name       string
		body       map[string]interface{}
		wantCode   int
		wantSubstr string
	}{
		{
			name:       "wrong sha256 is a 400 mismatch",
			body:       map[string]interface{}{"binary_path": binPath, "sha256": wrong, "version": "1.19.0"},
			wantCode:   http.StatusBadRequest,
			wantSubstr: "sha256 mismatch",
		},
		{
			name:     "correct sha256 proceeds",
			body:     map[string]interface{}{"binary_path": binPath, "sha256": correct, "version": "1.19.0"},
			wantCode: http.StatusOK,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, agent := newUpdateTestServer(t, agentID)
			rec := postUpdate(srv, agentID, tc.body)

			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body=%s)", rec.Code, tc.wantCode, rec.Body.String())
			}
			if tc.wantSubstr != "" && !strings.Contains(rec.Body.String(), tc.wantSubstr) {
				t.Fatalf("body = %q, want substring %q", rec.Body.String(), tc.wantSubstr)
			}
			if tc.wantCode == http.StatusBadRequest {
				if _, ok := agent.last(); ok {
					t.Fatal("refused request still reached the agent")
				}
				return
			}
			got, ok := agent.last()
			if !ok {
				t.Fatal("agent received no agent_update message")
			}
			if got.SHA256 != correct {
				t.Fatalf("agent sha256 = %q, want %q", got.SHA256, correct)
			}
		})
	}
}

// TestP908CanaryRegistryCleanup: a completed update leaves exactly the
// canonical row. The canary row goes, the canonical row is never touched, and
// a second run is a no-op.
func TestP908CanaryRegistryCleanup(t *testing.T) {
	const agentID = "TEST-OLD-1186"
	srv, _ := newUpdateTestServer(t, agentID)

	// The measured post-update registry state: canonical + canary, same version.
	srv.registry.Register(agentID, agentID, "1.19.0", "windows", "amd64", "silent", nil)
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix, "1.19.0", "windows", "amd64", "silent", nil)

	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err != nil {
		t.Fatalf("fixture canary row missing: %v", err)
	}

	if !srv.purgeCanaryRegistryRow(agentID) {
		t.Fatal("completion did not remove the canary row")
	}
	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err == nil {
		t.Fatal("canary row survived the completion")
	}
	canonical, err := srv.registry.GetHealth(agentID)
	if err != nil {
		t.Fatalf("canonical row was removed by the cleanup: %v", err)
	}
	if canonical.Version != "1.19.0" {
		t.Fatalf("canonical version = %q, want 1.19.0", canonical.Version)
	}

	// Idempotent: second run removes nothing.
	if srv.purgeCanaryRegistryRow(agentID) {
		t.Fatal("second cleanup reported a removal")
	}
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("canonical row lost on the second cleanup: %v", err)
	}
}

// TestP908UpdateStartDropsStaleCanary: an accepted update start removes a
// pre-existing canary row for the same agent and leaves the canonical row.
func TestP908UpdateStartDropsStaleCanary(t *testing.T) {
	const agentID = "TEST-OLD-1188"
	srv, _ := newUpdateTestServer(t, agentID)
	srv.registry.Register(agentID+canaryRegistrySuffix, agentID+canaryRegistrySuffix, "1.18.6", "windows", "amd64", "silent", nil)

	content := []byte("binary for the start-path cleanup")
	binPath := filepath.Join(t.TempDir(), fmt.Sprintf("p908-start-%d.bin", time.Now().UnixNano()))
	if err := os.WriteFile(binPath, content, 0644); err != nil {
		t.Fatalf("write fixture binary: %v", err)
	}

	rec := postUpdate(srv, agentID, map[string]interface{}{"binary_path": binPath, "version": "1.19.0"})
	if rec.Code != http.StatusOK {
		t.Fatalf("update start status = %d (body=%s)", rec.Code, rec.Body.String())
	}
	if _, err := srv.registry.GetHealth(agentID + canaryRegistrySuffix); err == nil {
		t.Fatal("stale canary row survived an accepted update start")
	}
	if _, err := srv.registry.GetHealth(agentID); err != nil {
		t.Fatalf("canonical row removed by the start-path cleanup: %v", err)
	}
}
