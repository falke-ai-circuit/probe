//go:build linux

package agent

// P13-03 (contract P10-07 v3.1 §7/§7.1), linux backend: the X11 EWMH window
// tree. Wayland's foreign_toplevel and the Windows `wts` column are separate
// units; a context this file cannot enumerate is refused, never stubbed.

import (
	"fmt"
	"os"
	"os/user"
	"strconv"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// uiTree answers §7 on linux. The context is resolved the same way the
// selector gate resolved it: an explicit `context` wins, otherwise the agent's
// own $DISPLAY. The result echoes both, so a caller can see which display
// answered.
func (a *Agent) uiTree(env protocol.Envelope, params protocol.UITreeParams) protocol.Envelope {
	requested := ""
	source := "agent_default"
	if params.Context != nil {
		requested = *params.Context
		source = "selector"
	} else if d := os.Getenv("DISPLAY"); d != "" {
		requested = d
	}
	num, ok := displayNum(requested)
	if !ok {
		// A wayland or uid context: this backend enumerates X displays only,
		// and a stub row would be the fabricated empty list §7.1 forbids.
		return noBackend(env, "ui-tree")
	}
	resolved := fmt.Sprintf("x11::%d", num)
	display := ":" + strconv.Itoa(num)

	d, err := x11Dial(num)
	if err != nil {
		return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
			fmt.Sprintf("display %s: could not open an X connection: %v", display, err),
			map[string]interface{}{
				"reason":                protocol.ReasonNoBackend,
				"context":               protocol.ContextRef{ID: resolved, Kind: protocol.ContextKindX11},
				"display":               display,
				"tree_source_attempted": protocol.TreeSourceEWMHClientList,
				"ran":                   false,
			})
	}
	defer d.Close()

	ctxRef := protocol.UITreeContextRef{
		Requested: requested,
		Resolved:  resolved,
		Kind:      protocol.ContextKindX11,
		User:      displayOwner(num),
		Source:    source,
	}

	visibleOnly := true
	if params.VisibleOnly != nil {
		visibleOnly = *params.VisibleOnly
	}

	res, refusal := buildUITree(d, ctxRef, visibleOnly, params.Max)
	if refusal != nil {
		detail := map[string]interface{}{
			"reason":                refusal.Reason,
			"context":               protocol.ContextRef{ID: resolved, Kind: protocol.ContextKindX11},
			"display":               display,
			"tree_source_attempted": protocol.TreeSourceEWMHClientList,
			"ran":                   false,
		}
		msg := fmt.Sprintf("display %s: %s", display, refusal.Message)
		if refusal.Reason == protocol.ReasonNoWindowManager {
			msg = fmt.Sprintf("display %s has no EWMH window manager: %s", display, refusal.Message)
			detail["remediation"] = noWMRemediation
		}
		return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported, msg, detail)
	}
	return protocol.NewResult(env.ID, protocol.TypeUITreeResult, res)
}

// displayOwner names the user the X server runs as, from the socket inode. It
// is the same observation the §6 enumeration makes; an unknown owner stays ""
// rather than borrowing the agent's own name.
func displayOwner(num int) string {
	for _, s := range scanX11Sockets() {
		if s.Num == num && s.OwnerKnown {
			if u, err := user.LookupId(strconv.Itoa(s.OwnerUID)); err == nil {
				return u.Username
			}
		}
	}
	return ""
}
