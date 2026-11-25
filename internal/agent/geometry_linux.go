//go:build linux

package agent

// P14-01 (§9.1), linux backend: which display a window-scoped input request
// resolves against, and the X connection that answers it.
//
// The resolution rule is the one the §7 tree already uses: an explicit `context`
// wins, otherwise the agent's own $DISPLAY. A context that is not an X display
// (wayland, uid) has no geometry backend in this build and is refused, not
// stubbed — the §11 matrix's answer for those kinds is a typed refusal, and
// inventing an absolute point there is exactly the class of lie this contract
// kills.

import (
	"fmt"
	"os"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// defaultDisplayOpener is x11Dial: the same connection the tree primitive opens,
// so a hit-test and the tree cannot disagree about the display.
func defaultDisplayOpener(num int) (windowProbe, error) { return x11Dial(num) }

// openContextDisplay resolves the context and opens its display. The returned
// ref echoes what was asked for and what it resolved to, so an OCCLUDED answer
// names the display it is about.
func (a *Agent) openContextDisplay(env protocol.Envelope, primitive string, contextID *string, window string) (windowProbe, protocol.UITreeContextRef, *protocol.Envelope) {
	requested := ""
	source := "agent_default"
	if contextID != nil {
		requested = *contextID
		source = "selector"
	} else if d := os.Getenv("DISPLAY"); d != "" {
		requested = d
	}
	num, ok := displayNum(requested)
	if !ok {
		envl := noBackendFor(env, primitive, requested, window)
		return nil, protocol.UITreeContextRef{}, &envl
	}
	ref := protocol.UITreeContextRef{
		Requested: requested,
		Resolved:  fmt.Sprintf("x11::%d", num),
		Kind:      protocol.ContextKindX11,
		User:      displayOwner(num),
		Source:    source,
	}
	opener := a.openDisplay
	if opener == nil {
		opener = defaultDisplayOpener
	}
	probe, err := opener(num)
	if err != nil {
		envl := protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
			fmt.Sprintf("display :%d: could not open an X connection: %v", num, err),
			map[string]interface{}{
				"reason":  protocol.ReasonNoBackend,
				"context": protocol.ContextRef{ID: ref.Resolved, Kind: protocol.ContextKindX11},
				"window":  window,
				"ran":     false,
			})
		return nil, ref, &envl
	}
	return probe, ref, nil
}

// noBackendFor is the §11 refusal for a context kind with no geometry backend:
// a wayland or uid selector cannot place a window-relative point in this build.
func noBackendFor(env protocol.Envelope, primitive, requested, window string) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		fmt.Sprintf("%s: context %q has no X display to place a window-relative point in; this build's geometry backend is X11 only", primitive, requested),
		map[string]interface{}{
			"reason":  protocol.ReasonNoBackend,
			"context": requested,
			"window":  window,
			"ran":     false,
		})
}
