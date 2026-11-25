package agent

// P13-01 (§5.5 E3b, rows 6, 8, 9, 13, 14). The five new selector-bearing
// commands are parsed strictly on the agent too, so a server that is old,
// misconfigured or bypassed cannot smuggle a field past the wire. The platform
// backends for these primitives do not exist in this build (contract §11:
// phase 3-5), so the honest answer after a clean parse is a typed
// PRIMITIVE_UNSUPPORTED/NO_BACKEND — never a fabricated success.

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

func (a *Agent) handlePointerScroll(env protocol.Envelope) protocol.Envelope {
	params, serr := protocol.ParseCommandStrict[protocol.PointerScrollParams](env, protocol.AllowPointerScroll)
	if serr != nil {
		return strictRefusal(env, serr)
	}
	_ = params
	return noBackend(env, "pointer-scroll")
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
	_ = params
	return noBackend(env, "ui-tree")
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
