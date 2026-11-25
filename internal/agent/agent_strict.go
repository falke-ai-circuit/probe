package agent

// P13-01 (§5.5 E3b, rows 6, 8, 9, 13, 14). The five new selector-bearing
// commands are parsed strictly on the agent too, so a server that is old,
// misconfigured or bypassed cannot smuggle a field past the wire. Rows 6, 8, 9
// and 14 still have no platform backend in this build (contract §11: phases
// 4-5), so their honest answer after a clean parse is PRIMITIVE_UNSUPPORTED /
// NO_BACKEND — never a fabricated success. Row 13 (ui-tree) was given its §7
// backend by P13-03 and dispatches to it here.

import (
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// noBackend is the refusal a primitive with no implementation returns. It is a
// 501 class answer: the code is not there, which is fixable by shipping it —
// distinct from a policy refusal and from an observed failure.
func noBackend(env protocol.Envelope, primitive string) protocol.Envelope {
	return protocol.NewErrorDetail(env.ID, protocol.ErrPrimitiveUnsupported,
		primitive+": no platform backend in this build; nothing was injected or read",
		map[string]interface{}{
			"reason": "NO_BACKEND",
			"ran":    false,
		})
}

func (a *Agent) handlePointerMove(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.PointerMoveParams](env, protocol.AllowPointerMove)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	_ = params
	return noBackend(env, "pointer-move")
}

func (a *Agent) handlePointerDrag(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.PointerDragParams](env, protocol.AllowPointerDrag)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	_ = params
	return noBackend(env, "pointer-drag")
}

func (a *Agent) handleUITree(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.UITreeParams](env, protocol.AllowUITree)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	return a.uiTree(env, params)
}

func (a *Agent) handleUIWindow(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.UIWindowParams](env, protocol.AllowUIWindow)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	_ = params
	return noBackend(env, "ui-window")
}

// strictRefusal renders an E3b failure as a typed agent error. The reason and
// field travel in detail so the server (and the caller) can see which key was
// refused instead of a free-text decoder message.
func strictRefusal(env protocol.Envelope, serr *protocol.StrictError) protocol.Envelope {
	detail := map[string]interface{}{
		"reason": serr.Reason,
		"ran":    false,
	}
	if serr.Field != "" {
		detail["field"] = serr.Field
	}
	if len(serr.Allowed) > 0 {
		detail["allowed"] = serr.Allowed
	}
	if serr.Want != "" {
		detail["want"] = serr.Want
	}
	if serr.Got != "" {
		detail["got"] = serr.Got
	}
	return protocol.NewErrorDetail(env.ID, protocol.ErrInvalidParams, serr.Error(), detail)
}
