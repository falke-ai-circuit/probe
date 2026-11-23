package server

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-20 — regression guard for P8-19 §C1: next_offset on a pre-1.19 agent.
//
// A v1.18.6 agent does not echo the request `offset`, so the server has to
// carry the caller's offset into the fallback. Before this fix the fallback
// started from 0, which made next_offset = len(window) for every read: a
// consumer following it re-read from byte 0 forever on any file larger than
// one window, and the re-read looked like a continuation.
//
// These tests are wire-level on purpose: they exercise the same HTTP surface
// the reviewer measured and they compile against the pre-fix tree, so the
// failure they produce is an assertion failure and not a build error.

const p820WindowLen = 100

func p820Window() []byte {
	b := make([]byte, p820WindowLen)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// TestP820OldAgentNextOffsetUsesRequestOffset (A1) — a 1.18.6-shaped response
// (no `offset` key) must yield next = request_offset + len(decoded) for zero
// and non-zero offsets.
func TestP820OldAgentNextOffsetUsesRequestOffset(t *testing.T) {
	window := p820Window()
	for _, off := range []int{0, 1000, 8192} {
		t.Run(map[int]string{0: "off0", 1000: "off1000", 8192: "off8192"}[off], func(t *testing.T) {
			agent := &oldAgent{window: window}
			srv := newFsFallbackServer(t, agent)
			agent.srv = srv

			rec := doJSON(srv, "POST", "/api/v1/agents/old/fs-read", map[string]interface{}{
				"path": "big.bin", "offset": off, "limit": p820WindowLen,
			})
			if rec.Code != 200 {
				t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
			}
			var env struct {
				Data map[string]interface{} `json:"data"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("response is not JSON: %s", rec.Body.String())
			}
			wire, _ := json.Marshal(env.Data)
			t.Logf("old1186 off=%d wire=%s", off, wire)

			if _, echoed := env.Data["offset"]; echoed {
				t.Fatalf("the simulated 1.18.6 agent must not echo offset: %s", wire)
			}
			if got, want := env.Data["size"], float64(p820WindowLen); got != want {
				t.Fatalf("size = %v, want %v (decoded window length)", got, want)
			}
			next, ok := env.Data["next_offset"].(float64)
			if !ok {
				t.Fatalf("next_offset absent from wire: %s", wire)
			}
			// Pre-fix this was 100 for every row: the request offset never
			// reached the fallback.
			if want := float64(off + p820WindowLen); next != want {
				t.Fatalf("next_offset = %v, want %v (request_offset %d + %d decoded bytes) — wire=%s",
					next, want, off, p820WindowLen, wire)
			}
		})
	}
}

// echoOffsetAgent models a v1.19.0 agent: it echoes the offset it was asked
// for and reports its own next_offset/total_size/truncated.
type echoOffsetAgent struct {
	srv     *Server
	lastOff int
}

func (e *echoOffsetAgent) WriteMessage(int, []byte) error { return nil }
func (e *echoOffsetAgent) Close() error                   { return nil }

func (e *echoOffsetAgent) WriteJSON(v interface{}) error {
	raw, _ := json.Marshal(v)
	var env protocol.Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil
	}
	var params protocol.FSParams
	_ = json.Unmarshal(env.Params, &params)
	e.lastOff = params.Offset
	res := map[string]interface{}{
		"data":         base64.StdEncoding.EncodeToString(p820Window()),
		"encoding":     "base64",
		"size":         p820WindowLen,
		"offset":       params.Offset,
		"next_offset":  params.Offset + p820WindowLen,
		"total_size":   10240,
		"truncated":    true,
		"sha256":       "agent-own-digest",
		"sha256_scope": "window",
	}
	e.srv.pendingMu.Lock()
	ch, ok := e.srv.pendingReqs[env.ID]
	e.srv.pendingMu.Unlock()
	if ok {
		ch <- protocol.NewResult(env.ID, env.Type+"_result", res)
	}
	return nil
}

// TestP820NewAgentNextOffsetUnchanged (A2) — a 1.19.0 agent that echoes the
// offset keeps its own next_offset; the fallback must not shift it.
func TestP820NewAgentNextOffsetUnchanged(t *testing.T) {
	agent := &echoOffsetAgent{}
	srv := newFsFallbackServer(t, agent)
	agent.srv = srv

	rec := doJSON(srv, "POST", "/api/v1/agents/old/fs-read", map[string]interface{}{
		"path": "big.bin", "offset": 1000, "limit": p820WindowLen,
	})
	var env struct {
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	wire, _ := json.Marshal(env.Data)
	t.Logf("new119 off=1000 wire=%s", wire)
	if agent.lastOff != 1000 {
		t.Fatalf("agent received offset %d, want 1000", agent.lastOff)
	}
	if got := env.Data["next_offset"]; got != float64(1100) {
		t.Fatalf("next_offset = %v, want 1100 — wire=%s", got, wire)
	}
	if got := env.Data["offset"]; got != float64(1000) {
		t.Fatalf("1.19.0 offset key was overwritten: %v", got)
	}
}
