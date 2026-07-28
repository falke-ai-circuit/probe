package protocol

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  65536,
	WriteBufferSize: 65536,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// Dial connects to a remote WebSocket server. certPath is the optional CA cert
// used to verify the server's TLS certificate (for wss:// URLs). When
// clientCertFile and clientKeyFile are both non-empty they are loaded as a
// client certificate for TLS mutual authentication.
func Dial(rawURL string, certPath string, clientCertFile string, clientKeyFile string, token string) (*websocket.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   65536,
		WriteBufferSize:  65536,
	}

	// Only apply TLS config for wss:// URLs; ws:// uses plain HTTP
	if u.Scheme == "wss" {
		dialer.TLSClientConfig = &tls.Config{
			MinVersion: tls.VersionTLS13,
		}
		// Server certificate verification.
		if certPath != "" {
			certPool := x509.NewCertPool()
			certPEM, err := os.ReadFile(certPath)
			if err != nil {
				return nil, fmt.Errorf("read cert: %w", err)
			}
			if !certPool.AppendCertsFromPEM(certPEM) {
				return nil, fmt.Errorf("failed to parse cert")
			}
			dialer.TLSClientConfig.RootCAs = certPool
		} else {
			dialer.TLSClientConfig.InsecureSkipVerify = true
		}
		// Client certificate for TLS mutual authentication (mTLS).
		if clientCertFile != "" && clientKeyFile != "" {
			cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
			if err != nil {
				return nil, fmt.Errorf("load client cert: %w", err)
			}
			dialer.TLSClientConfig.Certificates = []tls.Certificate{cert}
		}
	}

	header := http.Header{}
	header.Set("Authorization", "Bearer "+token)
	conn, _, err := dialer.Dial(u.String(), header)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	return conn, nil
}

// Listen starts a WebSocket server. certFile/keyFile are the server's TLS
// certificate and key (required for TLS). clientCAFile, when non-empty,
// enables TLS mutual authentication: client certificates are loaded into a
// pool and tls.RequireAndVerifyClientCert is set so the server rejects any
// client that does not present a valid certificate signed by the CA.
func Listen(addr string, certFile string, keyFile string, clientCAFile string) (*http.Server, *http.ServeMux, error) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS13,
	}

	// mTLS: require and verify client certificates when a client CA is provided.
	if clientCAFile != "" {
		caCert, err := os.ReadFile(clientCAFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read client CA: %w", err)
		}
		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, nil, fmt.Errorf("failed to parse client CA")
		}
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		tlsConfig.ClientCAs = caCertPool
	}

	if certFile != "" {
		fingerprint, err := certFingerprint(certFile)
		if err != nil {
			return nil, nil, fmt.Errorf("cert fingerprint: %w", err)
		}
		fmt.Fprintf(os.Stderr, "TLS Certificate SHA256: %s\n", fingerprint)
	}

	mux := http.NewServeMux()
	server := &http.Server{
		Addr:      addr,
		TLSConfig: tlsConfig,
		Handler:   mux,
	}

	return server, mux, nil
}

func certFingerprint(certFile string) (string, error) {
	certPEM, err := os.ReadFile(certFile)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return "", fmt.Errorf("failed to parse certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(cert.Raw)), nil
}

func WriteMessage(conn *websocket.Conn, v interface{}) error {
	return conn.WriteJSON(v)
}

func ReadMessage(conn *websocket.Conn, v interface{}) error {
	return conn.ReadJSON(v)
}

func ReadRawMessage(conn *websocket.Conn) (json.RawMessage, error) {
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(msg), nil
}

func GetUpgrader() *websocket.Upgrader {
	return &upgrader
}
