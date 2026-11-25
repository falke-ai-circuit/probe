package protocol

// P13-02 — contract P10-07 v3.1 §6: read-only context enumeration.
//
// This file is the contract's §6 wire shape and the ONE addressability
// predicate. Both surfaces that need the answer call ClassifyContext:
//   - enumeration (the agent builds every row and stores the result in it),
//   - selector enforcement (ResolveContext reads the same row, and the row's
//     verdict was produced by ClassifyContext — it is never recomputed, so
//     discovery and enforcement cannot drift).
// A second predicate anywhere is a defect by definition: the contract's whole
// claim is that /contexts and the selector agree by construction.

// Context kinds this build can enumerate. `wts` is deliberately absent: it is
// `[S]` (Windows, unplanned) and a fabricated row is worse than an empty list.
const (
	ContextKindX11     = "x11"
	ContextKindWayland = "wayland"
	ContextKindUID     = "uid"
)

// Wire commands. TypeContexts is a body-less command: the agent enumerates and
// answers from its own host. There is no selector to carry — a GET enumerates,
// it does not act.
const (
	TypeContexts       = "contexts"
	TypeContextsResult = "contexts_result"
)

// ErrContextUnavailable is the §5.4 refusal class a selector earns when the
// context exists (or does not) but cannot be addressed. It maps to HTTP 409.
const ErrContextUnavailable = "CONTEXT_UNAVAILABLE"

// The §6 addressable_reason enum, closed. Every value has a producer in
// ClassifyContext; a reason that is not listed here is a defect, not an
// extension, and the test enumerates this slice.
const (
	AddrAgentOwn            = "agent_own"
	AddrServicePrincipal    = "service_principal"
	AddrSameUIDOtherProcess = "same_uid_other_process"
	AddrOtherUser           = "other_user"
	AddrNoUser              = "no_user"
	AddrNotInteractive      = "not_interactive"
	AddrPrivilegeNotHeld    = "privilege_not_held"
	AddrKindHasNoSurface    = "kind_has_no_surface"
)

// AddressableReasons is the §6 closed enum, verbatim, in contract order.
var AddressableReasons = []string{
	AddrAgentOwn, AddrServicePrincipal, AddrSameUIDOtherProcess,
	AddrOtherUser, AddrNoUser, AddrNotInteractive,
	AddrPrivilegeNotHeld, AddrKindHasNoSurface,
}

// ContextRef identifies one context: an opaque id plus the platform kind, so a
// caller can dispatch on the kind without parsing the id.
type ContextRef struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

// ContextSurface is what the context can draw on. Unknown values stay at their
// zero (`""`, 0, 0) — a fabricated resolution is the same class of lie as a
// fabricated `active`.
type ContextSurface struct {
	Pixels   string  `json:"pixels"`
	Displays int     `json:"displays"`
	DPIScale float64 `json:"dpi_scale"`
}

// ContextFeatures is the per-context primitive matrix (§6, §13 layer 3). It is
// a property of the context, not of the agent: one agent may serve a full X11
// display and a windowless uid context at the same time.
type ContextFeatures struct {
	WindowTree    bool     `json:"window.tree"`
	WindowControl []string `json:"window.control"`
	InputPointer  []string `json:"input.pointer"`
	InputText     []string `json:"input.text"`
	CaptureWindow bool     `json:"capture.window"`
	CaptureRegion bool     `json:"capture.region"`
}

// ContextRefusal is one §13 entry: "{feature, code, reason} the agent knows it
// cannot do here". Advertising it up front is worth more than a fast failure —
// a flow routes around it before it starts.
type ContextRefusal struct {
	Feature string `json:"feature"`
	Code    string `json:"code"`
	Reason  string `json:"reason"`
}

// ContextRow is one §6 row, field for field. Active is a *bool on purpose:
// §6 requires `null` + active_known:false when the platform cannot answer, and
// a plain bool cannot express "unknown" without inventing false.
type ContextRow struct {
	ID                string           `json:"id"`
	Kind              string           `json:"kind"`
	Name              string           `json:"name"`
	User              string           `json:"user"`
	UID               int              `json:"uid"`
	Active            *bool            `json:"active"`
	ActiveKnown       bool             `json:"active_known"`
	AgentOwn          bool             `json:"agent_own"`
	Addressable       bool             `json:"addressable"`
	AddressableReason string           `json:"addressable_reason"`
	IdleS             int              `json:"idle_s"`
	LogonTime         string           `json:"logon_time"`
	Surface           ContextSurface   `json:"surface"`
	Features          ContextFeatures  `json:"features"`
	Refusals          []ContextRefusal `json:"refusals"`
}

// ContextsResult is the agent→server payload: the agent's own context plus the
// contexts it enumerated.
type ContextsResult struct {
	AgentContext ContextRef   `json:"agent_context"`
	Contexts     []ContextRow `json:"contexts"`
}

// ContextsResponse is the §6 server envelope payload. generated_at_utc is the
// server's clock, so two agents answering at different times cannot each
// publish their own idea of "now".
type ContextsResponse struct {
	AgentContext   ContextRef   `json:"agent_context"`
	GeneratedAtUTC string       `json:"generated_at_utc"`
	Contexts       []ContextRow `json:"contexts"`
}

// ContextFacts is everything classification is allowed to look at. It carries
// observations, never conclusions: the caller says what the platform answered,
// and ClassifyContext is the only place that turns that into addressability.
type ContextFacts struct {
	Kind             string // x11 | wayland | uid
	AgentOwn         bool   // this context is the one the agent itself serves
	ServicePrincipal bool   // a service/session-0 principal, not a seat
	HasUser          bool   // the platform named an owning user for the context
	UID              int    // the context's user id
	AgentUID         int    // the agent's effective uid
	PID              int    // the process that owns the context (0 unknown)
	AgentPID         int    // the agent process
	Interactive      bool   // the context has a seat/display a human drives
	Privileged       bool   // the agent can enter another principal's context
	AccessDenied     bool   // connect/probe returned EACCES for this context
	HasSurface       bool   // the kind has a UI surface at all (uid has none)
}

// ClassifyContext is the ONE addressability predicate (§6). It returns whether
// a selector can address the context and the §6 reason that explains the
// answer. Reasons are ordered by cause: a context with no surface is not
// "other_user", it is "kind_has_no_surface".
func ClassifyContext(f ContextFacts) (bool, string) {
	switch {
	case !f.HasSurface:
		return false, AddrKindHasNoSurface
	case f.ServicePrincipal:
		return false, AddrServicePrincipal
	case !f.HasUser:
		return false, AddrNoUser
	case f.UID != f.AgentUID:
		// Another principal's context. Addressable only when the agent holds
		// the privilege to enter it; the reason names the relationship either
		// way, because that is what the operator has to act on.
		return f.Privileged, AddrOtherUser
	case f.AccessDenied:
		return false, AddrPrivilegeNotHeld
	case f.AgentOwn && f.PID == f.AgentPID:
		if !f.Interactive {
			return false, AddrNotInteractive
		}
		return true, AddrAgentOwn
	case !f.Interactive:
		return false, AddrNotInteractive
	default:
		return true, AddrSameUIDOtherProcess
	}
}

// ContextUnavailableReason maps the §6 reason onto the §5.4 refusal code. The
// mapping is total: agent_own and same_uid_other_process describe addressable
// contexts (a row that carries them is never refused), so they fall through to
// NOT_FOUND rather than inventing a code.
func ContextUnavailableReason(reason string) string {
	switch reason {
	case AddrNoUser:
		return "NO_USER"
	case AddrNotInteractive:
		return "NOT_INTERACTIVE"
	case AddrOtherUser:
		return "OTHER_USER"
	case AddrPrivilegeNotHeld, AddrServicePrincipal:
		return "PRIVILEGE_NOT_HELD"
	case AddrKindHasNoSurface:
		return "KIND_NOT_APPLICABLE"
	default:
		return "NOT_FOUND"
	}
}

// ContextUnavailable is a refused selector: the §5.4 code, the §6 reason that
// produced it (so a caller sees the same word /contexts printed), and the id
// that was asked for.
type ContextUnavailable struct {
	Reason            string
	AddressableReason string
	Requested         string
	Row               *ContextRow
}

// ResolveContext is the enforcement half of the predicate: it looks the
// requested id up in the enumerated rows and refuses an unaddressable one. It
// reads the row's verdict; it does not re-derive it, which is what makes
// discovery and enforcement one predicate instead of two.
func ResolveContext(rows []ContextRow, requested string) *ContextUnavailable {
	for i := range rows {
		if rows[i].ID != requested {
			continue
		}
		row := &rows[i]
		if row.Addressable {
			return nil
		}
		return &ContextUnavailable{
			Reason:            ContextUnavailableReason(row.AddressableReason),
			AddressableReason: row.AddressableReason,
			Requested:         requested,
			Row:               row,
		}
	}
	return &ContextUnavailable{Reason: "NOT_FOUND", Requested: requested}
}
