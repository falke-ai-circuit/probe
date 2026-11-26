package relay

// P17-02 (spec P17-01 §1, §2, FM-1.1/1.5) — the importable uplink component
// the peer backend (internal/server peerManager) runs, one instance per
// configured default_peer.
//
// Division of ownership, binding for callers:
//   - the RELAY owns one dial+registration+dispatch cycle (RunUplinkOnly);
//   - the PEER MANAGER owns the retry loop (§4: backoff 1s exponential, cap
//     30s, reset on success), the state machine, and live apply — it calls
//     RunUplinkOnly, reacts to its return, and respawns;
//   - Config.Listen is only used when the operator configured a nested-relay
//     peer (listen set). Then StartDownstreamListener binds it — a peer that
//     is configured to accept agents must actually bind the socket. The
//     hard acceptance line's defect class ("returns a port, binds nothing")
//     is refused by construction for the nested case; a pure uplink binds
//     nothing, by design, and says so.
//
// RunUplinkOnly is re-runnable: each call links the dispatch loop's exit to
// a fresh per-run channel, so a manager retry loop can drive it again after
// a drop. It returns when the uplink drops after being up, when Stop is
// called, or with the typed *ForwardRefusal when the upstream refused the
// registration (P22-05 shape, Retryable()==false). A panic inside an uplink
// loop is converted to link-down + the FM-1.1 panic callback — it never
// crosses the component boundary.

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// uplinkSignalsInit lazily initialises the stop channel so Relay literals
// constructed without New (existing tests) stay valid.
func (r *Relay) uplinkSignalsInit() {
	r.signalMu.Lock()
	defer r.signalMu.Unlock()
	if r.stopCh == nil {
		r.stopCh = make(chan struct{})
	}
}

func (r *Relay) signalStop() {
	r.uplinkSignalsInit()
	r.stopOnce.Do(func() { close(r.stopCh) })
}

// setLinkDown installs the per-run exit hook and clears the fired flag. The
// previous run's hook has already fired before this runs (RunUplinkOnly
// returns only after close(down)), so the reset cannot race a live writer.
func (r *Relay) setLinkDown(fn func()) {
	r.signalMu.Lock()
	r.linkDownFired = false
	r.linkDownCb = fn
	r.signalMu.Unlock()
}

// signalUplinkDown fires the per-run link-down hook exactly once. Called
// from dispatchFromServer's exit path, which also runs in full relay mode
// (hook nil, flag set, nothing happens beyond the flag).
func (r *Relay) signalUplinkDown() {
	r.signalMu.Lock()
	if r.linkDownFired {
		r.signalMu.Unlock()
		return
	}
	r.linkDownFired = true
	cb := r.linkDownCb
	r.signalMu.Unlock()
	if cb != nil {
		cb()
	}
}

// RunUplinkOnly runs the relay without the downstream listener. Blocks until
// the uplink drops, Stop is called, or registration is refused (typed
// *ForwardRefusal return). Re-runnable after a link drop.
func (r *Relay) RunUplinkOnly() error {
	r.uplinkSignalsInit()
	err := r.connectUpstream()
	if err != nil {
		return err
	}
	r.uplinkUpOnce.Do(func() {
		r.uplinkUpMu.RLock()
		fn := r.uplinkUpCB
		r.uplinkUpMu.RUnlock()
		if fn != nil {
			fn()
		}
	})
	down := make(chan struct{})
	r.setLinkDown(func() { close(down) })
	go r.dispatchFromServer()
	select {
	case <-down:
		return nil // link lost after being up — the caller owns the retry (§4)
	case <-r.stopCh:
		return nil
	}
}

// Stop gracefully shuts down the relay: closes the upstream connection (which
// ends the blocked dispatch read and fires the link-down hook) and stops any
// downstream listener. Releases RunUplinkOnly via stopCh. Safe to call any
// number of times, in any mode. Declared here because it is the dial-only
// surface's control point; relay.go's original body is folded in.
//
// NOTE: this method REPLACES the one in relay.go (single declaration kept in
// uplink_only.go per the P17-02 patch set).
func (r *Relay) Stop() {
	r.signalStop()
	r.upstreamMu.Lock()
	if r.upstream != nil {
		r.upstream.Close()
		r.upstream = nil
	}
	r.upstreamMu.Unlock()
	if r.httpSrv != nil {
		r.httpSrv.Close()
	}
}

// SetUplinkUpCallback installs the hook fired when a registration completes
// (peer_hello is consumed inside connectUpstream before it returns). The
// peer backend maps this to the `connected` transition (§4) and the
// peer.registered audit event.
func (r *Relay) SetUplinkUpCallback(fn func()) {
	r.uplinkUpMu.Lock()
	r.uplinkUpCB = fn
	r.uplinkUpMu.Unlock()
}

// UplinkConnected reports whether the upstream socket is up (registration
// completed and no read/write failure since).
func (r *Relay) UplinkConnected() bool { return r.upstreamOK.Load() }

// PeerHelloSnapshot returns the last peer_hello the upstream pushed down,
// or nil when none has arrived. A copy: the caller cannot race the writer.
func (r *Relay) PeerHelloSnapshot() *PeerHello {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()
	if r.uplink.peer == nil {
		return nil
	}
	cp := *r.uplink.peer
	return &cp
}

// LastRefusal returns the last policy refusal the upstream named on the wire
// (P22-05), or nil. Retained until the next successful registration clears it.
func (r *Relay) LastRefusal() *ForwardRefusal { return r.refusalSnapshot() }

// SetPanicCallback installs the hook fired when an uplink loop recovers a
// panic (FM-1.1). The peer backend wires its audit logger here, so the panic
// becomes an audit event, a link-down and a retry — never a process crash.
func (r *Relay) SetPanicCallback(fn func(msg string)) {
	r.panicMu.Lock()
	r.panicCB = fn
	r.panicMu.Unlock()
}

func (r *Relay) uplinkPanicked(msg string) {
	r.statusMu.Lock()
	r.uplink.lastErr = "panic recovered on uplink loop: " + msg
	r.statusMu.Unlock()
	r.upstreamOK.Store(false)
	r.panicMu.RLock()
	fn := r.panicCB
	r.panicMu.RUnlock()
	if fn != nil {
		fn(msg)
	}
}

// recoverUplink is deferred inside every uplink goroutine (FM-1.1): a panic
// in relay code converts to link-down + retrying at the peer backend instead
// of taking the server process down.
func (r *Relay) recoverUplink() {
	if p := recover(); p != nil {
		if s, ok := p.(string); ok {
			r.uplinkPanicked(s)
			return
		}
		b, _ := json.Marshal(p)
		r.uplinkPanicked(string(b))
	}
}

// downstreamMux is the downstream listener's route set (extracted from Run;
// Run and StartDownstreamListener share it so the two entry points cannot
// diverge).
func (r *Relay) downstreamMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", r.handleDownstream)
	mux.HandleFunc("/health", r.handleHealth)
	mux.HandleFunc("GET /api/v1/relay/status", r.handleStatus)
	return mux
}

// StartDownstreamListener binds Config.Listen and serves downstream agents
// (nested-relay peers, FM-1.5). Returns an error when no listen is
// configured (pure uplink — the caller skips it) or when the listener exits.
// Blocking, like ListenAndServe.
func (r *Relay) StartDownstreamListener() error {
	if r.cfg.ListenAddr == "" {
		return fmt.Errorf("no listen configured: pure uplink peer")
	}
	r.httpSrv = &http.Server{
		Addr:    r.cfg.ListenAddr,
		Handler: r.downstreamMux(),
	}
	if r.cfg.CertFile != "" && r.cfg.KeyFile != "" {
		return r.httpSrv.ListenAndServeTLS(r.cfg.CertFile, r.cfg.KeyFile)
	}
	return r.httpSrv.ListenAndServe()
}
