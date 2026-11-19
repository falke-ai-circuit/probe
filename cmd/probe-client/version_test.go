package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/agent"
	"github.com/falke-ai-circuit/probe/internal/protocol"
	"github.com/falke-ai-circuit/probe/internal/version"
	"github.com/gorilla/websocket"
)

// captureUsage exercises the real banner path (printUsage) and returns the text
// a user would see on stderr.
func captureUsage(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	old := os.Stderr
	os.Stderr = w
	printUsage()
	os.Stderr = old
	if err := w.Close(); err != nil {
		t.Fatalf("close pipe: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// bannerVersion extracts the version token from the first banner line,
// e.g. "PROBE Client v1.18.6" -> "v1.18.6".
func bannerVersion(t *testing.T, out string) string {
	t.Helper()
	line := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	fields := strings.Fields(line)
	if len(fields) < 3 {
		t.Fatalf("unexpected banner line %q", line)
	}
	return fields[2]
}

// reportedVersion connects a real agent to a throwaway websocket server and
// returns the Version field of the agent_info message the agent actually sent.
// The Config is built the same way runClient builds it, so this exercises the
// production reporting path (handleConnection -> protocol.AgentInfo), not a
// copy of it.
func reportedVersion(t *testing.T, cfgVersion string) string {
	t.Helper()

	infoCh := make(chan protocol.AgentInfo, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var env protocol.Envelope
			if err := json.Unmarshal(msg, &env); err != nil {
				continue
			}
			if env.Type != "agent_info" {
				continue
			}
			var info protocol.AgentInfo
			if err := json.Unmarshal(env.Result, &info); err != nil {
				return
			}
			select {
			case infoCh <- info:
			default:
			}
			// Keep the connection open until the client stops; the agent only
			// sends agent_info once per connection.
			<-r.Context().Done()
			return
		}
	}))
	defer srv.Close()

	cfg := agent.Config{
		Mode:       "outbound",
		URL:        "ws" + strings.TrimPrefix(srv.URL, "http"),
		Token:      "test-token",
		Name:       "version-test",
		MaxRetries: 1,
		BackoffMin: 100 * time.Millisecond,
		BackoffMax: 200 * time.Millisecond,
		Version:    cfgVersion,
	}
	ag := agent.New(cfg)
	done := make(chan struct{})
	go func() {
		_ = ag.Run()
		close(done)
	}()
	defer func() {
		ag.Stop()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()

	select {
	case info := <-infoCh:
		return info.Version
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent_info from the connected agent")
		return ""
	}
}

// checkVersionSurfaces returns an error when the printed version and the
// version the agent reports in agent_info disagree. It returns instead of
// calling t.Errorf so TestDivergenceIsDetected can prove it is able to fail.
func checkVersionSurfaces(banner, reported string) error {
	if version.Normalize(banner) != version.Normalize(reported) {
		return fmt.Errorf("version surfaces diverge: banner prints %q but agent_info reports %q", banner, reported)
	}
	return nil
}

// TestClientVersionSurfacesAgree is the anti-drift gate: the version the
// binary prints must be the version its agent reports to the server. It reads
// the real banner and the real agent_info message exchanged over a websocket.
func TestClientVersionSurfacesAgree(t *testing.T) {
	banner := bannerVersion(t, captureUsage(t))
	reported := reportedVersion(t, appVersion)

	if err := checkVersionSurfaces(banner, reported); err != nil {
		t.Fatal(err)
	}
	// Wire-format stability: the server displays "1.18.6", so the reported
	// value must never carry the display "v" prefix.
	if strings.HasPrefix(reported, "v") {
		t.Fatalf("agent_info version %q must be the bare wire form", reported)
	}
}

// TestDivergenceIsDetected proves the gate above is able to fail: a deliberately
// divergent pair must be rejected, and a matching pair must pass.
func TestDivergenceIsDetected(t *testing.T) {
	if err := checkVersionSurfaces("v1.18.7", "1.18.6"); err == nil {
		t.Fatal("checkVersionSurfaces accepted a divergent pair (v1.18.7 vs 1.18.6) - the gate is tautological")
	}
	if err := checkVersionSurfaces("v1.18.6", "1.18.6"); err != nil {
		t.Fatalf("checkVersionSurfaces rejected a matching pair: %v", err)
	}
}

// TestConfigVersionIsUsedForInfo guards the single source of truth: the value
// injected into Config is what gets reported, normalised to wire form.
func TestConfigVersionIsUsedForInfo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", agent.Version},
		{"v1.18.6", "1.18.6"},
		{"1.18.6", "1.18.6"},
		{"v1.18.7", "1.18.7"},
	}
	for _, c := range cases {
		if got := agent.VersionForInfo(agent.Config{Version: c.in}); got != c.want {
			t.Errorf("VersionForInfo(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// The package default must be the same build version the banner derives
	// from, i.e. both read internal/version.
	if agent.Version != version.Wire() {
		t.Fatalf("agent.Version = %q, want derived value %q", agent.Version, version.Wire())
	}
}
