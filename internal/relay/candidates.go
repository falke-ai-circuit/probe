package relay

// P21-09 — the upstream candidate list.
//
// A relay used to carry one upstream target. It now carries an ordered list
// and dials it from the top: the first candidate that completes the WebSocket
// upgrade wins, and every candidate that failed keeps the error that made the
// relay skip it. The list is re-walked from the top after a drop, not just the
// URL that died, because the surviving candidate is the one that can come back.
//
// The legacy single-string config is one-element list; there is no second code
// path for it.

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// defaultDialTimeout bounds one candidate's upgrade attempt. It is deliberately
// short: a candidate that accepts TCP and never answers must not stall the walk
// for every other candidate behind it.
const defaultDialTimeout = 5 * time.Second

// candidate is one resolved upstream target. port is what the operator needs to
// read back as the transport; 0 means the URL carried no resolvable port.
type candidate struct {
	url  string
	port int
}

// candidateState is what the relay remembers about one candidate across walks:
// how many times it was dialled and why the last attempt failed. attempts > 0
// with an unchanged order is the observable proof that a reconnect re-entered
// the list from the top rather than retrying only the dead URL.
type candidateState struct {
	state    string // "untried" | "up" | "down"
	attempts int
	lastErr  string
}

// resolveCandidates normalises both config forms into one ordered list.
// cfg.UpstreamURLs wins when set; otherwise the legacy UpstreamURL becomes a
// one-element list. Blank entries and exact duplicates are dropped, order is
// preserved.
func resolveCandidates(cfg Config) []candidate {
	raw := cfg.UpstreamURLs
	if len(raw) == 0 {
		raw = []string{cfg.UpstreamURL}
	}
	out := make([]candidate, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for _, u := range raw {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, candidate{url: u, port: upstreamPort(u)})
	}
	return out
}

// dialTimeout returns the per-candidate bound in force.
func (r *Relay) dialTimeout() time.Duration {
	if r.cfg.DialTimeout > 0 {
		return r.cfg.DialTimeout
	}
	return defaultDialTimeout
}

// dial opens one candidate. The returned error is exactly what the operator
// will read back in last_error for that candidate.
func (r *Relay) dial(c candidate) (*websocket.Conn, error) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+r.Token())

	d := *websocket.DefaultDialer
	d.HandshakeTimeout = r.dialTimeout()
	conn, _, err := d.Dial(c.url, headers)
	return conn, err
}

// candidateURLs returns the configured order as plain URLs, for logs.
func (r *Relay) candidateURLs() []string {
	urls := make([]string, 0, len(r.candidates))
	for _, c := range r.candidates {
		urls = append(urls, c.url)
	}
	return urls
}

// noteAttempt records that a dial was started for the candidate at idx. It is
// incremented before the dial so a candidate that hangs still shows the
// attempt; the acceptance reads this to prove the walk restarted at the top.
func (r *Relay) noteAttempt(idx int) {
	r.statusMu.Lock()
	if idx >= 0 && idx < len(r.candState) {
		r.candState[idx].attempts++
	}
	r.statusMu.Unlock()
}

// recordCandidateFailure retains why one candidate was skipped.
func (r *Relay) recordCandidateFailure(idx int, err error) {
	if err == nil {
		return
	}
	r.statusMu.Lock()
	if idx >= 0 && idx < len(r.candState) {
		r.candState[idx].state = "down"
		r.candState[idx].lastErr = err.Error()
	}
	r.statusMu.Unlock()
}

// markCandidateUp records the candidate that won and the transport it won on.
// transport is derived from the port actually dialled, never from the config
// order: it is what the operator can point a firewall rule at.
func (r *Relay) markCandidateUp(idx int) {
	r.statusMu.Lock()
	for i := range r.candState {
		if i == idx {
			r.candState[i].state = "up"
			r.candState[i].lastErr = ""
			continue
		}
		// A candidate that is simply further down the list was never tried by
		// this walk. Leave its record alone rather than calling it dead.
	}
	if idx >= 0 && idx < len(r.candidates) {
		r.uplink.activeURL = r.candidates[idx].url
		r.uplink.activePort = r.candidates[idx].port
	}
	r.statusMu.Unlock()
}

// recordCandidateLinkLoss marks the candidate that was carrying the uplink as
// down when the socket dies. The transport is retained: it is the last
// transport that actually won, which is the one to go and look at.
func (r *Relay) recordCandidateLinkLoss(err error) {
	if err == nil {
		return
	}
	r.statusMu.Lock()
	for i := range r.candState {
		if r.candState[i].state == "up" {
			r.candState[i].state = "down"
			r.candState[i].lastErr = err.Error()
		}
	}
	r.statusMu.Unlock()
}

// candidatesSnapshot copies the ordered list and its per-candidate state out for
// the status document.
func (r *Relay) candidatesSnapshot() []CandidateStatus {
	r.statusMu.RLock()
	defer r.statusMu.RUnlock()

	if len(r.candidates) == 0 {
		return nil
	}
	out := make([]CandidateStatus, 0, len(r.candidates))
	for i, c := range r.candidates {
		cs := CandidateStatus{URL: c.url, Port: c.port, State: "untried"}
		if i < len(r.candState) && r.candState[i].attempts > 0 {
			cs.Attempts = r.candState[i].attempts
			cs.LastError = r.candState[i].lastErr
			if r.candState[i].state != "" {
				cs.State = r.candState[i].state
			}
		}
		out = append(out, cs)
	}
	return out
}

// transportName is the reported transport: the port a candidate actually won
// on, as a string. Empty means no candidate has completed an upgrade yet.
func transportName(port int) string {
	if port <= 0 {
		return ""
	}
	return fmt.Sprint(port)
}
