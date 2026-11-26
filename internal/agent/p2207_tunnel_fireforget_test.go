package agent

// P22-07 — agent-side tunnel data contract: fire-and-forget.
//
// The defect this pins: handleTunnelData ACKed a successful write with a
// Result envelope echoing env.ID. The server's read loop matched that ACK
// against pendingReqs — evicting a pending tunnel_open confirmation or, on
// match-miss, falling through as a tunnel_data frame with empty params
// ("unexpected end of JSON input" in every failing live log of P23-11 cell 4).
// A successful tunnel_data must send NOTHING.

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// p2207NopConn absorbs writes (a stand-in for a live target connection).
type p2207NopConn struct{}

func (p2207NopConn) Read(b []byte) (int, error)          { return 0, io.EOF }
func (p2207NopConn) Write(b []byte) (int, error)         { return len(b), nil }
func (p2207NopConn) Close() error                        { return nil }
func (p2207NopConn) LocalAddr() net.Addr                 { return p2207Addr{} }
func (p2207NopConn) RemoteAddr() net.Addr                { return p2207Addr{} }
func (p2207NopConn) SetDeadline(t time.Time) error       { return nil }
func (p2207NopConn) SetReadDeadline(t time.Time) error   { return nil }
func (p2207NopConn) SetWriteDeadline(t time.Time) error  { return nil }

type p2207Addr struct{}

func (p2207Addr) Network() string { return "nop" }
func (p2207Addr) String() string  { return "nop" }

func TestP2207_TunnelDataIsFireAndForget(t *testing.T) {
	a := New(Config{
		Mode:        "outbound",
		URL:         "ws://127.0.0.1:1/ws", // never dialed in these tests
		Token:       "test-token",
		Name:        "p2207-handler-test",
		Permissions: "full",
	})
	tun := &agentTunnel{
		connID: "tun-ff-c1",
		target: &p2207NopConn{},
	}
	a.tunnelMgr.mu.Lock()
	a.tunnelMgr.tunnels["tun-ff-c1"] = tun
	a.tunnelMgr.mu.Unlock()

	env := protocol.Envelope{
		ID:     "tunnel_data-1",
		Type:   protocol.TypeTunnelData,
		Params: []byte(`{"tunnel_id":"tun-ff-c1","direction":"client→target","data":"cHJvb2U="}`),
	}
	resp := a.handleTunnelData(env)
	if resp.Type != "" || resp.ID != "" || resp.Result != nil || resp.Error != nil {
		t.Fatalf("successful tunnel_data produced a reply envelope: %+v", resp)
	}
	// Errors DO reply — the server must still learn a failed write.
	badEnv := protocol.Envelope{
		ID:     "tunnel_data-2",
		Type:   protocol.TypeTunnelData,
		Params: []byte(`{"tunnel_id":"tun-missing","direction":"client→target","data":"cHJvb2U="}`),
	}
	resp = a.handleTunnelData(badEnv)
	if resp.Error == nil {
		t.Fatalf("tunnel_data for a missing tunnel must carry an error, got %+v", resp)
	}
}
