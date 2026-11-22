package agent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falke-ai-circuit/probe/internal/platform"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// fsStubPlatform overrides only the fs methods the handlers under test call;
// the embedded nil interface panics on anything else, so an accidental call is
// loud rather than silent.
type fsStubPlatform struct {
	platform.Platform
	listFn func(string) ([]protocol.FSEntry, error)
	readFn func(string, int, int) (protocol.FSReadResult, error)
}

func (s *fsStubPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
	return s.listFn(path)
}

func (s *fsStubPlatform) ReadFile(path string, offset, limit int) (protocol.FSReadResult, error) {
	if s.readFn != nil {
		return s.readFn(path, offset, limit)
	}
	return protocol.FSReadResult{}, nil
}

func fsEnvelope(t *testing.T, typ, params string) protocol.Envelope {
	t.Helper()
	return protocol.Envelope{ID: "env-1", Type: typ, Params: json.RawMessage(params)}
}

// A3: a directory behind a stalled volume must return a typed TIMEOUT inside
// the agent, not stall until the server's 120 s wall.
func TestFSListReturnsTypedTimeout(t *testing.T) {
	oldList, oldRead := fsListDeadline, fsReadDeadline
	fsListDeadline = 25 * time.Millisecond
	defer func() { fsListDeadline, fsReadDeadline = oldList, oldRead }()

	a := &Agent{plat: &fsStubPlatform{listFn: func(string) ([]protocol.FSEntry, error) {
		time.Sleep(400 * time.Millisecond)
		return nil, nil
	}}}

	start := time.Now()
	resp := a.handleFSList(fsEnvelope(t, protocol.TypeFSList, `{"path":"\\\\fileserver\\mods"}`))
	elapsed := time.Since(start)

	if resp.Error == nil {
		t.Fatalf("stalled fs-list returned no error after %s", elapsed)
	}
	if resp.Error.Code != protocol.ErrTimeout {
		t.Fatalf("error code = %q, want %q (%s)", resp.Error.Code, protocol.ErrTimeout, resp.Error.Message)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("handler waited %s: the agent-side deadline did not fire", elapsed)
	}
	if !strings.Contains(resp.Error.Message, "fs-list") {
		t.Fatalf("message does not name the operation: %q", resp.Error.Message)
	}
}

// The deadline must not swallow ordinary failures: a missing path stays a
// normal typed error.
func TestFSListRealErrorIsNotTimeout(t *testing.T) {
	a := &Agent{plat: &fsStubPlatform{listFn: func(string) ([]protocol.FSEntry, error) {
		return nil, errors.New("The system cannot find the path specified.")
	}}}
	resp := a.handleFSList(fsEnvelope(t, protocol.TypeFSList, `{"path":"C:\\nope"}`))
	if resp.Error == nil || resp.Error.Code != protocol.ErrInternal {
		t.Fatalf("error = %+v, want INTERNAL_ERROR", resp.Error)
	}
}

func TestFSReadReturnsTypedTimeout(t *testing.T) {
	oldRead := fsReadDeadline
	fsReadDeadline = 25 * time.Millisecond
	defer func() { fsReadDeadline = oldRead }()

	a := &Agent{plat: &fsStubPlatform{readFn: func(string, int, int) (protocol.FSReadResult, error) {
		time.Sleep(400 * time.Millisecond)
		return protocol.FSReadResult{}, nil
	}}}
	start := time.Now()
	resp := a.handleFSRead(fsEnvelope(t, protocol.TypeFSRead, `{"path":"\\\\fileserver\\mods\\a.xml","offset":0,"limit":64}`))
	elapsed := time.Since(start)
	if resp.Error == nil || resp.Error.Code != protocol.ErrTimeout {
		t.Fatalf("error = %+v, want TIMEOUT", resp.Error)
	}
	if elapsed > 200*time.Millisecond {
		t.Fatalf("handler waited %s", elapsed)
	}
}

// boundedFS wraps the sentinel, so a caller can branch on the cause.
func TestBoundedFSErrorIsMatchable(t *testing.T) {
	_, err := boundedFS(10*time.Millisecond, "fs-read", "/tmp/x", func() (int, error) {
		time.Sleep(200 * time.Millisecond)
		return 1, nil
	})
	if !errors.Is(err, ErrFSDeadline) {
		t.Fatalf("err = %v, want ErrFSDeadline", err)
	}
}
