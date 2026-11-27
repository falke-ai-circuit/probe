package relay

// P19-01 hardening-lite — wire-level pins for the security seams this unit
// flips:
//
//   - TestP1901_EmptyAgentTokensRefusesToStart: a relay that LISTENS
//     downstream with no agent tokens and no explicit opt-out must refuse at
//     New/Run — accept-all downstream is no longer a silent default.
//   - TestP1901_AllowAnyExplicitOptOutStarts: the opt-out is honoured and the
//     listener binds (the compat path is explicit, not silent).
//   - TestP1901_WrongTokenDownstreamRefused: with tokens configured, a
//     downstream agent presenting the wrong bearer is refused (401), a
//     correct-token agent is admitted.
//   - TestP1901_WSSUpstreamWithoutCAFailsLoud: a wss:// upstream candidate
//     without a CA fails the candidate with a LOUD refusal naming the rule —
//     never a silent skip-verify dial.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// p1901WriteTempPEM writes a self-signed CA cert to a temp dir and returns the
// path (the relay config only needs a readable PEM file for the loud-failure
// test; the actual TLS handshake is never reached because the candidate dial
// refuses before it).
func p1901WriteTempPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "p1901-test-ca"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		IsCA:         true,
		KeyUsage:     x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write ca pem: %v", err)
	}
	return path
}

// TestP1901_EmptyAgentTokensRefusesToStart is the seam-2 acceptance: an empty
// -agent-tokens with a downstream listener must not start unless the explicit
// opt-out is passed.
func TestP1901_EmptyAgentTokensRefusesToStart(t *testing.T) {
	up := p2109StartCandidate(t, "127.0.0.1:0")
	t.Cleanup(func() { up.kill(t) })
	listen := p2103FreePort(t)

	rl := New(Config{
		UpstreamURLs: []string{"ws://" + up.addr + "/ws"},
		ListenAddr:   listen,
		Token:        "relay-token",
		// AgentTokens: empty — the old behaviour accepted everyone.
	})
	if err := rl.Run(); err == nil {
		t.Fatalf("relay with empty agent-tokens STARTED — the P19-01 refusal did not fire")
	} else if !strings.Contains(err.Error(), "--allow-any-downstream") {
		t.Fatalf("refusal does not name the opt-out flag: %v", err)
	} else {
		t.Logf("refusal as returned: %v", err)
	}
}

// TestP1901_AllowAnyExplicitOptOutStarts proves the explicit opt-out still
// binds the listener (the wire-compat path, deliberately loud-only at the CLI
// level).
func TestP1901_AllowAnyExplicitOptOutStarts(t *testing.T) {
	up := p2109StartCandidate(t, "127.0.0.1:0")
	t.Cleanup(func() { up.kill(t) })
	listen := p2103FreePort(t)

	rl := New(Config{
		UpstreamURLs:       []string{"ws://" + up.addr + "/ws"},
		ListenAddr:         listen,
		Token:              "relay-token",
		AllowAnyDownstream: true,
	})
	go func() { _ = rl.Run() }()
	t.Cleanup(rl.Stop)
	p2203WaitListening(t, listen)
}

// TestP1901_WrongTokenDownstreamRefused drives the real relay (explicit
// tokens configured) and asserts the admission decision on the wire.
func TestP1901_WrongTokenDownstreamRefused(t *testing.T) {
	up := p2109StartCandidate(t, "127.0.0.1:0")
	t.Cleanup(func() { up.kill(t) })
	listen := p2103FreePort(t)

	rl := New(Config{
		UpstreamURLs: []string{"ws://" + up.addr + "/ws"},
		ListenAddr:   listen,
		Token:        "relay-token",
		AgentTokens:  "tok-good",
	})
	go func() { _ = rl.Run() }()
	t.Cleanup(rl.Stop)
	p2203WaitListening(t, listen)

	// Wrong token → 401 before upgrade.
	h := http.Header{}
	h.Set("Authorization", "Bearer tok-wrong")
	c, resp, err := websocket.DefaultDialer.Dial("ws://"+listen+"/ws", h)
	if err == nil {
		c.Close()
		t.Fatalf("agent with WRONG token was admitted — downstream auth is broken")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-token dial: status=%v, want 401", resp)
	}
	t.Logf("wrong-token refused with HTTP %d", resp.StatusCode)

	// Correct token → upgrade + channel alloc.
	h2 := http.Header{}
	h2.Set("Authorization", "Bearer tok-good")
	c2, resp2, err := websocket.DefaultDialer.Dial("ws://"+listen+"/ws", h2)
	if err != nil {
		st := 0
		if resp2 != nil {
			st = resp2.StatusCode
		}
		t.Fatalf("agent with CORRECT token refused: %v (status=%d)", err, st)
	}
	c2.Close()
}

// TestP1901_WSSUpstreamWithoutCAFailsLoud pins seam 1 on the relay's upstream
// path: a wss:// candidate without a CA must fail the candidate loudly with
// the rule in the message — never dial with InsecureSkipVerify.
func TestP1901_WSSUpstreamWithoutCAFailsLoud(t *testing.T) {
	listen := p2103FreePort(t)

	rl := New(Config{
		UpstreamURLs: []string{"wss://127.0.0.1:1/ws"}, // port 1: unreachable, but the refusal fires BEFORE any dial
		ListenAddr:   listen,
		Token:        "relay-token",
		AgentTokens:  "tok", // keep Validate quiet: this pin is the wss/no-CA refusal, not admission
	})
	t.Cleanup(rl.Stop)
	err := rl.Run()
	if err == nil {
		t.Fatalf("wss upstream without CA dialled anyway — the loud refusal did not fire")
	}
	if !strings.Contains(err.Error(), "wss upstream without CA") || !strings.Contains(err.Error(), "PROBE_TLS_SKIP_VERIFY") {
		t.Fatalf("refusal does not name the rule: %v", err)
	}
	t.Logf("loud refusal: %v", err)
}

// TestP1901_ValidateTTLGuard drives the shared token-ttl rule the two server
// entrypoints import — it lives beside the rule in internal/server
// (p1901_security_test.go there); the relay package has no server import.
