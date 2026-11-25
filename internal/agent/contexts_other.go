//go:build !linux

package agent

// P13-02 (contract P10-07 v3.1 §6): no enumeration backend on this platform.
// Windows is `[S]` — source-planned, unverified — so the route exists and the
// command answers PRIMITIVE_UNSUPPORTED/NO_BACKEND. An empty contexts list or
// an invented `wts` row would be a fabricated answer to a question this build
// cannot ask.

import "github.com/falke-ai-circuit/probe/internal/protocol"

func enumerateContexts() (protocol.ContextsResult, error) {
	return protocol.ContextsResult{}, errNoContextBackend
}
