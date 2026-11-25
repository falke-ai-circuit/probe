//go:build !linux

package agent

// P14-01 (§9.1), non-linux: this build has no geometry backend off X11. The
// Windows `wts` column is `[S]` in the §11 matrix (planned, unverified — W-4/W-5
// for WindowFromPoint), so a window-scoped input that needs a hit-test is a
// typed 501 refusal here, never an assumed "nothing on top".

import (
	"fmt"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// errNoGeometryBackend is the cause a non-X11 host reports.
var errNoGeometryBackend = fmt.Errorf("no geometry/hit-test backend for this platform in this build")

func defaultDisplayOpener(num int) (windowProbe, error) { return nil, errNoGeometryBackend }

func (a *Agent) openContextDisplay(env protocol.Envelope, primitive string, contextID *string, window string) (windowProbe, protocol.UITreeContextRef, *protocol.Envelope) {
	requested := ""
	if contextID != nil {
		requested = *contextID
	}
	envl := protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		fmt.Sprintf("%s: no geometry/hit-test backend on this platform in this build; nothing was injected", primitive),
		map[string]interface{}{
			"reason":  protocol.ReasonNoBackend,
			"context": requested,
			"window":  window,
			"ran":     false,
		})
	return nil, protocol.UITreeContextRef{}, &envl
}
