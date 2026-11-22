//go:build !linux && !windows

package agent

import (
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// P8-04 remediation 2: the debugger has a Windows implementation
// (agent_debug.go) and a Linux implementation (agent_debug_linux.go, whose
// filename implies GOOS=linux). Nothing existed for other unix targets, so
// `GOOS=darwin go build ./cmd/probe-client/` failed with undefined symbols —
// a build-matrix gap, not a runtime one. This file keeps the matrix honest:
// the same surface exists everywhere, and the unsupported platforms refuse
// with a typed error instead of failing to compile.

type debugSession struct {
	id   string
	pid  int
	name string
	path string
}

type debugManager struct {
	sessions map[string]*debugSession
}

func newDebugManager() *debugManager {
	return &debugManager{sessions: make(map[string]*debugSession)}
}

func debugUnsupported(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"debugger is implemented on windows and linux only")
}

func (a *Agent) handleDebugAttach(env protocol.Envelope) protocol.Envelope {
	return debugUnsupported(env)
}

func (a *Agent) handleDebugDetach(env protocol.Envelope) protocol.Envelope {
	return debugUnsupported(env)
}

func (a *Agent) handleDebugReadMem(env protocol.Envelope) protocol.Envelope {
	return debugUnsupported(env)
}

func (a *Agent) handleDebugModules(env protocol.Envelope) protocol.Envelope {
	return debugUnsupported(env)
}

func (a *Agent) handleDebugMemQuery(env protocol.Envelope) protocol.Envelope {
	return debugUnsupported(env)
}

func (a *Agent) closeAllDebug() {}
