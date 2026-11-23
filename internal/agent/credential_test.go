package agent

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

const fixtureSecret = "FIXTURE-SW-PASSWORD-7c1f"

// A4: the in-memory buffer is overwritten before it is released, and a test
// asserts the bytes are gone rather than trusting the comment.
func TestZeroCredential_OverwritesBuffer(t *testing.T) {
	buf := []byte(fixtureSecret)
	var seen []byte
	credentialZeroHook = func(b []byte) {
		seen = append([]byte(nil), b...)
	}
	defer func() { credentialZeroHook = nil }()

	zeroCredential(buf)
	for i, b := range seen {
		if b != 0 {
			t.Fatalf("byte %d still %q after zeroing", i, b)
		}
	}
	if len(seen) != len(fixtureSecret) {
		t.Fatalf("hook saw %d bytes, want %d", len(seen), len(fixtureSecret))
	}
	for i := range buf {
		if buf[i] != 0 {
			t.Fatalf("live buffer byte %d not zeroed", i)
		}
	}
}

// The handle unseals only for the agent it was minted for.
func TestHandleCredentialDeliver_SealsToAgent(t *testing.T) {
	a := &Agent{cfg: Config{Token: "shared"}}
	sealed, err := sealForAgentTest("shared", "agent-1", fixtureSecret)
	if err != nil {
		t.Fatal(err)
	}
	delivery := protocol.CredentialDelivery{
		Handle:       "cred_abc",
		Name:         "sw-enable",
		AgentID:      "agent-1",
		Mode:         "stdin",
		Sealed:       base64.StdEncoding.EncodeToString(sealed),
		ExpiresAtUTC: time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
	}
	raw, _ := jsonMarshal(delivery)
	resp := a.handleCredentialDeliver(protocol.Envelope{ID: "x", Type: protocol.TypeCredentialDeliver, Params: raw})
	if resp.Error != nil {
		t.Fatalf("delivery refused: %+v", resp.Error)
	}
	pc, expired := a.takeCredential("sw-enable")
	if pc == nil || expired || string(pc.value) != fixtureSecret {
		t.Fatalf("pending credential not usable: %+v expired=%v", pc, expired)
	}
	zeroCredential(pc.value)

	// A different agent id must not unseal the same payload.
	b := &Agent{cfg: Config{Token: "shared"}}
	delivery.AgentID = "agent-2"
	raw2, _ := jsonMarshal(delivery)
	resp2 := b.handleCredentialDeliver(protocol.Envelope{ID: "y", Type: protocol.TypeCredentialDeliver, Params: raw2})
	if resp2.Error == nil {
		t.Fatal("agent-2 accepted a handle sealed for agent-1")
	}
}

// A5: banner handling, '>' vs '#' discrimination, stdin scripting and
// per-command exit codes — against a real SSH server on loopback.
func TestSSHHelper_AgainstLoopbackFixture(t *testing.T) {
	addr, stop := startFixtureSSH(t, fixtureSecret)
	defer stop()
	host, port, _ := net.SplitHostPort(addr)
	script := filepath.Join(t.TempDir(), "script.txt")
	os.WriteFile(script, []byte("show version\nbogus command\n"), 0600)

	var out, errb bytes.Buffer
	args := []string{"--host", host, "--port", port, "--user", "enable",
		"--credential", "sw-enable", "--script", script, "--timeout", "5"}
	code := RunSSH(args, strings.NewReader(fixtureSecret+"\n"), &out, &errb)

	got := out.String()
	if !strings.Contains(got, "SwitchOS") {
		t.Fatalf("banner missing from output: %q (stderr=%q)", got, errb.String())
	}
	if !strings.Contains(got, "# ") {
		t.Fatalf("never reached the '#' prompt: %q (stderr=%q)", got, errb.String())
	}
	if !strings.Contains(got, "ok: show version") {
		t.Fatalf("scripted command did not run: %q", got)
	}
	if code != 1 {
		t.Fatalf("invalid command did not set exit code 1 (got %d) stderr=%q", code, errb.String())
	}
	if strings.Contains(got, fixtureSecret) {
		t.Fatal("helper echoed the credential")
	}

	// --exec propagates the remote exit status verbatim.
	var out2, err2 bytes.Buffer
	code2 := RunSSH([]string{"--host", host, "--port", port, "--user", "enable",
		"--exec", "exit 7", "--timeout", "5"},
		strings.NewReader(fixtureSecret+"\n"), &out2, &err2)
	if code2 != 7 {
		t.Fatalf("remote exit status not propagated: got %d (stderr=%q)", code2, err2.String())
	}
}

// --- fixture SSH server -----------------------------------------------------

// startFixtureSSH runs a minimal device: it prints a banner, answers '>' for
// user exec, asks for a password on `enable`, then answers '#'.
func startFixtureSSH(t *testing.T, password string) (string, func()) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == "enable" && string(pass) == password {
				return nil, nil
			}
			return nil, fmt.Errorf("authentication failed")
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFixture(conn, cfg)
		}
	}()
	return ln.Addr().String(), func() { ln.Close(); close(done) }
}

func serveFixture(conn net.Conn, cfg *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			for req := range requests {
				switch req.Type {
				case "pty-req":
					req.Reply(true, nil)
				case "shell":
					req.Reply(true, nil)
					go fixtureShell(ch)
				case "exec":
					req.Reply(true, nil)
					var payload struct{ Command string }
					ssh.Unmarshal(req.Payload, &payload)
					fixtureExec(ch, payload.Command)
				default:
					req.Reply(false, nil)
				}
			}
		}()
	}
}

func fixtureShell(ch ssh.Channel) {
	defer ch.Close()
	fmt.Fprint(ch, "SwitchOS 9.2\r\n> ")
	buf := make([]byte, 1)
	var line strings.Builder
	for {
		n, err := ch.Read(buf)
		if err != nil || n == 0 {
			return
		}
		if buf[0] == '\n' {
			cmd := strings.TrimRight(line.String(), "\r")
			line.Reset()
			switch {
			case cmd == "enable":
				fmt.Fprint(ch, "Password: ")
				pw := readLine(ch)
				if pw != fixtureSecret {
					fmt.Fprint(ch, "\r\n% Access denied\r\n> ")
					continue
				}
				fmt.Fprint(ch, "\r\n# ")
			case cmd == "exit":
				fmt.Fprint(ch, "bye\r\n")
				return
			case cmd == "":
				fmt.Fprint(ch, "# ")
			case strings.HasPrefix(cmd, "show"):
				fmt.Fprintf(ch, "ok: %s\r\n# ", cmd)
			default:
				fmt.Fprintf(ch, "%% Invalid input detected\r\n# ")
			}
			continue
		}
		line.WriteByte(buf[0])
	}
}

func readLine(ch ssh.Channel) string {
	var b strings.Builder
	buf := make([]byte, 1)
	for {
		n, err := ch.Read(buf)
		if err != nil || n == 0 {
			return b.String()
		}
		if buf[0] == '\n' {
			return strings.TrimRight(b.String(), "\r")
		}
		b.WriteByte(buf[0])
	}
}

func fixtureExec(ch ssh.Channel, command string) {
	defer ch.Close()
	status := uint32(0)
	if strings.HasPrefix(command, "exit ") {
		fmt.Sscanf(command, "exit %d", &status)
	}
	fmt.Fprintf(ch, "bye %d\r\n", status)
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
}

// --- tiny helpers so the test file needs no extra imports elsewhere --------

func jsonMarshal(v interface{}) ([]byte, error) { return json.Marshal(v) }

// sealForAgentTest mirrors the server's sealing so the agent-side opener is
// exercised against the real derivation, not a stub.
func sealForAgentTest(token, agentID, value string) ([]byte, error) {
	key := sha256.Sum256([]byte(token + "|" + agentID))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, []byte(value), nil)...), nil
}

// binary is referenced by the exit-status marshal above.
var _ = binary.BigEndian
