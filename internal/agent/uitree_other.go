//go:build !linux

package agent

// P13-03: no window-tree backend on this platform. Windows is `[S]` (the `wts`
// column is unplanned) and a non-linux host has no EWMH display, so the route
// refuses with §11's NO_BACKEND rather than answering an empty list that would
// mean "this desktop has no windows".

import "github.com/falke-ai-circuit/probe/internal/protocol"

func (a *Agent) uiTree(env protocol.Envelope, params protocol.UITreeParams) protocol.Envelope {
	return noBackend(env, "ui-tree")
}
