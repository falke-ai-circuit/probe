package relay

// P22-05 (F5) — a policy refusal is terminal, and the peer must see its code.
//
// The upstream server answers a refused relay_register with one typed
// forward_refused control frame carrying a FORWARD_* code (P21-04). Before
// this type, the relay read that frame as an opaque control message: the code
// was discarded, the link dropped with `websocket: close 1006`, and the
// watcher retried a refusal that can never succeed. ForwardRefusal carries
// the code and reason across the hop so the relay can
//
//   - report the named code in its log and status document, and
//   - classify the refusal as terminal: a policy denial is not fixed by
//     reconnecting, so it is not retried.
//
// Transport failures stay retryable; only codes in the policy family return
// Retryable() == false. The classification is by the wire code itself, not by
// matching on an error string.

// ForwardRefusal is a policy refusal received on the uplink, carrying the
// wire code and reason the upstream sent. It is an error so it flows through
// the same paths as transport failures and lands in recordUpstreamError.
type ForwardRefusal struct {
	Code   string
	Reason string
}

// Error implements error. The reason already begins with the code, because
// the server's ForwardRefusalError.Error() is `code + ": " + reason` and that
// full string is what travels the wire — do not print the code twice.
func (r *ForwardRefusal) Error() string { return r.Reason }

// Retryable reports whether reconnecting could ever change the outcome.
// FORWARD_MIRROR_HAS_NO_LIVE_UPSTREAM (an upstream-side mirror staleness) is
// not classified here: it is never sent on a relay registration, so a refusal
// carrying it is still a policy statement — terminal.
func (r *ForwardRefusal) Retryable() bool {
	return false
}

// IsForwardRefusalCode reports whether a wire code names a forwarding-path
// refusal. Single spelling shared across the hop; the FORWARD_ prefix is the
// family marker both sides already agree on (server.forward_policy refusal
// codes all carry it).
func IsForwardRefusalCode(code string) bool {
	return len(code) >= 8 && code[:8] == "FORWARD_"
}
