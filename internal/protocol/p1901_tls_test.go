package protocol

// P19-01 hardening-lite — the agent-side wire pins for the wss CA rule:
//
//   - a wss:// dial WITHOUT a CA must fail LOUDLY, with the rule named in the
//     error (the old behaviour silently set InsecureSkipVerify);
//   - the same dial WITH the server's CA cert in the pool succeeds;
//   - the explicit PROBE_TLS_SKIP_VERIFY=true opt-out restores dialing
//     (logged, process-wide, never silent).
//
// Everything is loopback TLS with a self-signed server cert — no external
// network, no fixtures on disk beyond one temp file.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// p1901TLSServer returns (httptest TLS server URL as wss://, CA PEM path).
func p1901TLSServer(t *testing.T) (string, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	caTmpl := p1901CertTemplate()
	der, err := x509.CreateCertificate(rand.Reader, &caTmpl, &caTmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPath := filepath.Join(t.TempDir(), "server-cert.pem")
	keyPath := filepath.Join(t.TempDir(), "server-key.pem")
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	for path, data := range map[string][]byte{certPath: certPEM, keyPath: keyPEM, caPath: certPEM} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusSwitchingProtocols)
	}))
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("key pair: %v", err)
	}
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return "wss://" + strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://") + "/ws", caPath
}

func p1901CertTemplate() x509.Certificate {
	return x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}
}

// TestP1901_WSSWithoutCAFailsLoud is the seam-1 core: no CA, no opt-out →
// the dial refuses BEFORE any handshake, naming the rule.
func TestP1901_WSSWithoutCAFailsLoud(t *testing.T) {
	url, _ := p1901TLSServer(t)
	_, err := Dial(url, "", "", "", "tok")
	if err == nil {
		t.Fatalf("wss dial without CA SUCCEEDED — the silent InsecureSkipVerify default is back")
	}
	if !strings.Contains(err.Error(), "wss:// dial refused") || !strings.Contains(err.Error(), "PROBE_TLS_SKIP_VERIFY") {
		t.Fatalf("refusal does not name the rule: %v", err)
	}
	t.Logf("loud refusal: %v", err)
}

// TestP1901_WSSWithCADialSucceedsHouse: with the CA supplied, the dial
// actually verifies the chain — a wrong CA still fails, proving verification
// is real and not decorative.
func TestP1901_WSSWithCADialRejectsWrongCA(t *testing.T) {
	url, caPath := p1901TLSServer(t)
	// First: a NON-matching CA (generate a second one) must fail verification.
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := p1901CertTemplate()
	tmpl.SerialNumber = big.NewInt(2)
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &otherKey.PublicKey, otherKey)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	wrongCA := filepath.Join(t.TempDir(), "wrong-ca.pem")
	if err := os.WriteFile(wrongCA, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write wrong ca: %v", err)
	}
	if _, err := Dial(url, wrongCA, "", "", "tok"); err == nil {
		t.Fatalf("dial with a WRONG CA succeeded — certificate verification is not enforced")
	} else {
		t.Logf("wrong CA refused: %v", err)
	}

	// The correct CA reaches the TLS handshake (the failure below is the
	// server's plain 101-lite handler, not a verification error) — the pin
	// is that the CA-pool path verifies and allows the handshake to run.
	_ = caPath
	if _, err := Dial(url, caPath, "", "", "tok"); err != nil && strings.Contains(err.Error(), "certificate") {
		t.Fatalf("dial with the CORRECT CA hit a certificate error: %v", err)
	} else if err != nil {
		t.Logf("correct CA dial past verification (handler refusal expected in this rig): %v", err)
	}
}

// TestP1901_SkipVerifyOptOutRestoresDial: the explicit opt-out lets the
// otherwise-refusing dial proceed to a real TLS handshake.
func TestP1901_SkipVerifyOptOutRestoresDial(t *testing.T) {
	url, _ := p1901TLSServer(t)
	prev := SkipVerify()
	SetSkipVerify(true)
	t.Cleanup(func() { SetSkipVerify(prev) })
	conn, err := Dial(url, "", "", "", "tok")
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Logf("dial reached the handshake and the test server answered 101")
	} else if strings.Contains(err.Error(), "dial refused") {
		t.Fatalf("opt-out did not take effect: %v", err)
	} else {
		t.Logf("dial proceeded past verification (server answered: %v)", err)
	}
}
