package agent

// P13-02 (contract P10-07 v3.1 §6): read-only context enumeration.
//
// The agent is the only party that can answer "what contexts exist on this
// host, and what can be done in each", so the enumeration lives here and the
// server relays it. Every row's addressability comes from
// protocol.ClassifyContext — the same predicate the selector gate below
// enforces through protocol.ResolveContext, which reads the row's verdict
// rather than recomputing it.
//
// Honesty rules this file obeys:
//   - no fabricated zero: `active` stays nil (JSON null) with active_known
//     false unless the platform actually answered, idle_s -1 and logon_time ""
//     where unanswerable;
//   - no fabricated row: `wts` never appears on linux, and a kind this build
//     cannot enumerate is absent rather than empty;
//   - features are per context: an x11 seat and the agent's own uid context in
//     the same answer carry different matrices.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// errNoContextBackend is the honest answer on a platform this build cannot
// enumerate (Windows is `[S]`): the route exists, the backend does not.
var errNoContextBackend = errors.New("no context enumeration backend on this platform")

// wmState is the answer to "does this X display publish an EWMH client list?".
// wmUnknown is a real answer too: `xprop` missing means we did not observe the
// absence of a window manager, only the absence of the probe.
type wmState int

const (
	wmUnknown wmState = iota
	wmPresent
	wmAbsent
)

// x11SocketInfo is one observed X display socket, with its owning uid taken
// from the socket inode (the X server's user), not from a guess.
type x11SocketInfo struct {
	Num        int
	OwnerUID   int
	OwnerKnown bool
}

// hostProbe is the observation set buildContexts may read. It is a value, not
// a set of syscalls, so the enumeration is testable without an X server: the
// tests construct one and assert the rows, the real probe fills it on linux.
type hostProbe struct {
	EUID           int
	Username       string
	AgentPID       int
	Privileged     bool                                   // euid 0: can enter another principal
	Display        string                                 // $DISPLAY, e.g. ":0"
	WaylandDisplay string                                 // $WAYLAND_DISPLAY
	XSockets       []x11SocketInfo                        // /tmp/.X11-unix/X*
	XInfo          func(num int) (wmState, *bool, string) // wm state, active window present, pixels
	Tools          map[string]bool                        // exec.LookPath results
	LookupUser     func(uid int) string                   // uid -> name, "" when unknown
}

// contextsCacheTTL bounds how stale an enumeration may be for the selector
// gate. Enumeration shells out to xprop/xdpyinfo; doing that on every UI call
// would put tens of milliseconds in front of each click. Five seconds keeps a
// selector honest about a display that just came up.
const contextsCacheTTL = 5 * time.Second

var contextsCache struct {
	mu  sync.Mutex
	at  time.Time
	res protocol.ContextsResult
	err error
}

// enumerateContextsCached answers from a short-lived cache so the selector gate
// and /contexts see the same facts. A failed probe is not cached past the TTL
// either: the host may have gained a display since.
func enumerateContextsCached() (protocol.ContextsResult, error) {
	contextsCache.mu.Lock()
	defer contextsCache.mu.Unlock()
	if !contextsCache.at.IsZero() && time.Since(contextsCache.at) < contextsCacheTTL {
		return contextsCache.res, contextsCache.err
	}
	res, err := enumerateContexts()
	contextsCache.at = time.Now()
	contextsCache.res = res
	contextsCache.err = err
	return res, err
}

// handleContexts answers the body-less `contexts` command.
func (a *Agent) handleContexts(env protocol.Envelope) protocol.Envelope {
	res, err := enumerateContextsCached()
	if err != nil {
		// No enumeration backend on this platform (Windows is `[S]`: unplanned).
		// The route exists; the backend does not, which is a 501, not a row.
		return noBackend(env, "contexts")
	}
	return protocol.NewResult(env.ID, protocol.TypeContextsResult, res)
}

// buildContexts turns observations into §6 rows. It is pure: no syscalls, no
// clock, so a test can state a host and read the rows back.
func buildContexts(hp hostProbe) protocol.ContextsResult {
	rows := make([]protocol.ContextRow, 0, len(hp.XSockets)+2)

	ownDisplay, ownDisplayOK := displayNumber(hp.Display)
	for _, sock := range hp.XSockets {
		row := protocol.ContextRow{
			ID:        fmt.Sprintf("x11::%d", sock.Num),
			Kind:      protocol.ContextKindX11,
			Name:      fmt.Sprintf(":%d", sock.Num),
			UID:       sock.OwnerUID,
			IdleS:     -1,
			LogonTime: "",
			Surface:   protocol.ContextSurface{Displays: 1},
		}
		if sock.OwnerKnown {
			row.User = hp.LookupUser(sock.OwnerUID)
		}
		state, active, pixels := hp.XInfo(sock.Num)
		row.Surface.Pixels = pixels
		if active != nil {
			row.Active = active
			row.ActiveKnown = true
		}
		row.AgentOwn = ownDisplayOK && sock.Num == ownDisplay
		facts := protocol.ContextFacts{
			Kind:        protocol.ContextKindX11,
			AgentOwn:    row.AgentOwn,
			HasUser:     sock.OwnerKnown,
			UID:         sock.OwnerUID,
			AgentUID:    hp.EUID,
			AgentPID:    hp.AgentPID,
			PID:         hp.AgentPID,
			Interactive: true,
			Privileged:  hp.Privileged,
			HasSurface:  true,
		}
		row.Addressable, row.AddressableReason = protocol.ClassifyContext(facts)
		row.Features, row.Refusals = x11Features(state, hp.Tools)
		rows = append(rows, row)
	}

	if hp.WaylandDisplay != "" {
		row := protocol.ContextRow{
			ID:                "wayland:" + hp.WaylandDisplay,
			Kind:              protocol.ContextKindWayland,
			Name:              hp.WaylandDisplay,
			User:              hp.Username,
			UID:               hp.EUID,
			AgentOwn:          true,
			ActiveKnown:       false, // the compositor is the only authority; we did not ask it
			IdleS:             -1,
			LogonTime:         "",
			Surface:           protocol.ContextSurface{Displays: 1},
			Features:          emptyFeatures(),
			Refusals:          waylandRefusals(),
			AddressableReason: "",
		}
		facts := protocol.ContextFacts{
			Kind:        protocol.ContextKindWayland,
			AgentOwn:    true,
			HasUser:     true,
			UID:         hp.EUID,
			AgentUID:    hp.EUID,
			AgentPID:    hp.AgentPID,
			PID:         hp.AgentPID,
			Interactive: true,
			Privileged:  hp.Privileged,
			HasSurface:  true,
		}
		row.Addressable, row.AddressableReason = protocol.ClassifyContext(facts)
		rows = append(rows, row)
	}

	// The agent's own process context. It is always enumerable and never has a
	// UI surface: it is the row that proves `active` must be nullable, because
	// a uid context cannot know the foreground activity.
	uidRow := protocol.ContextRow{
		ID:          fmt.Sprintf("uid:%d", hp.EUID),
		Kind:        protocol.ContextKindUID,
		Name:        hp.Username,
		User:        hp.Username,
		UID:         hp.EUID,
		AgentOwn:    true,
		ActiveKnown: false,
		IdleS:       -1,
		LogonTime:   "",
		Surface:     protocol.ContextSurface{},
		Features:    emptyFeatures(),
		Refusals:    []protocol.ContextRefusal{},
	}
	uidFacts := protocol.ContextFacts{
		Kind:        protocol.ContextKindUID,
		AgentOwn:    true,
		HasUser:     hp.Username != "",
		UID:         hp.EUID,
		AgentUID:    hp.EUID,
		AgentPID:    hp.AgentPID,
		PID:         hp.AgentPID,
		Interactive: false,
		Privileged:  hp.Privileged,
		HasSurface:  false, // a uid context has no screen, no input queue
	}
	uidRow.Addressable, uidRow.AddressableReason = protocol.ClassifyContext(uidFacts)
	rows = append(rows, uidRow)

	agentContext := protocol.ContextRef{ID: uidRow.ID, Kind: uidRow.Kind}
	for _, r := range rows {
		if r.Kind == protocol.ContextKindX11 && r.AgentOwn {
			agentContext = protocol.ContextRef{ID: r.ID, Kind: r.Kind}
			break
		}
	}
	if agentContext.Kind != protocol.ContextKindX11 && hp.WaylandDisplay != "" {
		agentContext = protocol.ContextRef{
			ID:   "wayland:" + hp.WaylandDisplay,
			Kind: protocol.ContextKindWayland,
		}
	}

	return protocol.ContextsResult{AgentContext: agentContext, Contexts: rows}
}

// emptyFeatures is the matrix of a context with no surface: every primitive
// false or empty, and empty slices rather than nil so the JSON is [] not null.
func emptyFeatures() protocol.ContextFeatures {
	return protocol.ContextFeatures{
		WindowControl: []string{},
		InputPointer:  []string{},
		InputText:     []string{},
	}
}

// waylandRefusals is what a Wayland seat honestly cannot do: the compositor
// does not publish a global window list (403, its own security model) and does
// not expose per-window capture (501, no backend).
func waylandRefusals() []protocol.ContextRefusal {
	return []protocol.ContextRefusal{
		{Feature: "window.tree", Code: protocol.ErrPrimitiveRefused, Reason: "NO_GLOBAL_WINDOW_LIST"},
		{Feature: "capture.window", Code: protocol.ErrPrimitiveUnsupported, Reason: "NOT_EXPOSED"},
	}
}

// x11Features derives the per-context matrix from what was observed, plus what
// this build can actually do. window.tree is the load-bearing case: with no
// window manager the context publishes no client list, and the caller learns
// that here (§7.1) instead of from a 501 mid-flow.
func x11Features(state wmState, tools map[string]bool) (protocol.ContextFeatures, []protocol.ContextRefusal) {
	f := emptyFeatures()
	refusals := make([]protocol.ContextRefusal, 0, 4)

	switch {
	case state == wmPresent && treeBackendPresent:
		f.WindowTree = true
	case state == wmAbsent:
		// Environmental: start a WM. One reason per cause (§7.6) — when the WM
		// is absent, the missing backend is not the caller's problem to fix.
		refusals = append(refusals, protocol.ContextRefusal{
			Feature: "window.tree", Code: protocol.ErrPrimitiveUnsupported, Reason: "NO_WINDOW_MANAGER",
		})
	default:
		refusals = append(refusals, protocol.ContextRefusal{
			Feature: "window.tree", Code: protocol.ErrPrimitiveUnsupported, Reason: "NO_BACKEND",
		})
	}

	// Window control ships in a later unit; advertising the six actions now
	// would be a claim the route answers 501 to.
	refusals = append(refusals, protocol.ContextRefusal{
		Feature: "window.control", Code: protocol.ErrPrimitiveUnsupported, Reason: "NO_BACKEND",
	})

	if tools["xdotool"] {
		f.InputPointer = []string{"click"}
		f.InputText = []string{"unicode"}
	} else {
		refusals = append(refusals, protocol.ContextRefusal{
			Feature: "input.pointer", Code: protocol.ErrPrimitiveUnsupported, Reason: "NO_BACKEND",
		})
	}

	// P14-03: §10 capture ships a real X11 backend — GetImage on the same
	// connection the tree and the hit-test use. It needs no shell tool (the
	// old import/scrot gate is gone with the stdout-parsing code it guarded),
	// and it answers window, region and full screen, so all three are
	// advertised here rather than refused with a NO_BACKEND that no longer
	// describes this build.
	f.CaptureRegion = true
	f.CaptureWindow = true

	return f, refusals
}

// treeBackendPresent says whether this build carries the §7 EWMH backend.
// P13-03 shipped it, so the constant is true and the per-context answer is
// decided by observation: a WM that publishes _NET_SUPPORTING_WM_CHECK gets
// window.tree:true, a bare X server (Xvfb) keeps window.tree:false with
// NO_WINDOW_MANAGER, and a display we could not ask stays NO_BACKEND. One
// constant, two facts: the features row never promises what ui-tree refuses.
const treeBackendPresent = true

// selectorBearing is the set of commands whose params may carry `context` or
// `session`. It is the §5.5 selector-bearing set; stream-stop is excluded
// because the server refuses its selector outright (§5.6).
var selectorBearing = map[string]bool{
	protocol.TypeExec:          true,
	protocol.TypeCapture:       true,
	protocol.TypeStreamBegin:   true,
	protocol.TypePointerMove:   true,
	protocol.TypePointerClick:  true,
	protocol.TypePointerScroll: true,
	protocol.TypePointerDrag:   true,
	protocol.TypeKeyPress:      true,
	protocol.TypeKeyCombo:      true,
	protocol.TypeTextInput:     true,
	protocol.TypeUITree:        true,
	protocol.TypeUIWindow:      true,
}

// selectorGate is the enforcement half of §6: before any command that names a
// context is dispatched, the agent resolves that context against the very rows
// /contexts reports and refuses an unaddressable one with the matching reason.
// A nil return means "no selector, or it resolved".
func (a *Agent) selectorGate(env protocol.Envelope) *protocol.Envelope {
	if !selectorBearing[env.Type] || len(env.Params) == 0 {
		return nil
	}
	var sel struct {
		Context *string `json:"context"`
		Session *int    `json:"session"`
	}
	if err := json.Unmarshal(env.Params, &sel); err != nil {
		return nil // the strict decoder owns malformed bodies
	}
	if sel.Context == nil && sel.Session == nil {
		return nil
	}
	if serr := protocol.ValidateSelector(sel.Context, sel.Session); serr != nil {
		ref := strictRefusal(env, serr)
		return &ref
	}
	res, err := enumerateContextsCached()
	if err != nil {
		// No enumeration backend: the capability gate already refuses a
		// selector-scoped call to such an agent. Inventing NOT_FOUND here
		// would be a fabricated answer.
		return nil
	}
	if sel.Session != nil {
		// `session` is the wts alias (contract §4). On a host with no wts
		// context it is not "missing", it is inapplicable.
		ref := protocol.NewErrorDetail(env.ID, protocol.ErrContextUnavailable,
			fmt.Sprintf("context selector session=%d is a wts alias; this host enumerates no wts contexts", *sel.Session),
			map[string]interface{}{
				"reason":        "KIND_NOT_APPLICABLE",
				"field":         "session",
				"ran":           false,
				"agent_context": res.AgentContext,
			})
		return &ref
	}
	requested := *sel.Context
	if u := protocol.ResolveContext(res.Contexts, requested); u != nil {
		detail := map[string]interface{}{
			"reason":        u.Reason,
			"field":         "context",
			"ran":           false,
			"agent_context": res.AgentContext,
		}
		if u.AddressableReason != "" {
			detail["addressable_reason"] = u.AddressableReason
		}
		ref := protocol.NewErrorDetail(env.ID, protocol.ErrContextUnavailable,
			fmt.Sprintf("context %q cannot be addressed (%s)", requested, u.Reason), detail)
		return &ref
	}
	return nil
}

// displayNumber parses the display index out of an X display string (":0",
// "host:9.0"). ok=false means no numeric display could be named, in which case
// the agent claims no context as its own rather than guessing :0.
func displayNumber(display string) (int, bool) {
	if display == "" {
		return 0, false
	}
	colon := strings.LastIndex(display, ":")
	if colon < 0 || colon == len(display)-1 {
		return 0, false
	}
	rest := display[colon+1:]
	if dot := strings.Index(rest, "."); dot >= 0 {
		rest = rest[:dot]
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}
