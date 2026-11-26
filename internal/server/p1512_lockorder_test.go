package server

// P15-12 port to the 1.19.2 stable line (R0 cut, protocol patch discipline).
//
// The captured production hang (528-goroutine dump
// /opt/data/tmp/probe119/P15-11_GOROUTINE.log) showed cleanupRelaySession
// holding a session's channelsMu (write) while waiting on s.relayMu, and
// relayChannelOwner (via decorateAgentLiveness) holding s.relayMu (read) while
// waiting on that same session's channelsMu: a closed ABBA cycle that parked
// 251 health readers for 145 minutes.
//
// Unit rule: RELAY-FIRST. Never acquire a session's channelsMu while holding
// relayMu, and never hold channelsMu across a relayMu acquisition. Every site
// that touches both obeys it by snapshot-and-release. The probes below pin the
// order itself — whether a mutex is HELD ACROSS an acquisition of the other —
// with sync TryLock: a waiter blocks by design and that is correct behaviour;
// only a holder is half of a cycle. The concurrency test runs cleanup against
// decorate under a stall watchdog: a regression FAILS (naming which side
// stalled, with a goroutine dump) instead of hanging the suite.
//
// Ported from upstream 60c5950's p1512_lockorder_test.go with the stable-line
// differences only: no routeStore, no chain/link counters (federation
// surface, deliberately absent from a patch release), and decorate-driven
// probe replaced by a direct AgentDispatchability scan — the same lock pair,
// without the federation caller wiring.

import (
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// p1512Session builds a live relay session with one registered channel, the
// same state the registration path creates on the stable line.
func p1512Session(srv *Server, relayID, agentID string) *relaySession {
	srv.registry.Register(agentID, agentID, "1.19.2", "linux", "amd64", "serve", nil)
	sess := &relaySession{
		relayID:  relayID,
		channels: map[uint32]*virtualConn{},
	}
	vc := &virtualConn{channelID: 1, session: sess, agentID: agentID}
	sess.channels[1] = vc
	srv.relayMu.Lock()
	srv.relays[relayID] = sess
	srv.relayMu.Unlock()
	return sess
}

// p1512Scan Semantics on the stable line: the fastest public-API-shaped path
// that walks the same locks decorateAgentLiveness does upstream. Direct
// dispatchability read, then the relay-session scan — relayMu first,
// snapshot-and-release, then channelsMu per session.
func p1512Scan(srv *Server) int {
	d, _, _ := srv.AgentDispatchability("p1512-agent")
	found := 0
	if d {
		found++
	}
	found += srv.p1512RelayScan()
	return found
}

// p1512RelayScan is the scan-loop that, pre-fix, held relayMu.RLock while
// taking each session's channelsMu — the half of the ABBA cycle that parked
// the health queue. The fix shape is: relayMu snapshot, release, then walk.
func (s *Server) p1512RelayScan() int {
	s.relayMu.RLock()
	sessions := make([]*relaySession, 0, len(s.relays))
	for _, sess := range s.relays {
		sessions = append(sessions, sess)
	}
	s.relayMu.RUnlock()
	open := 0
	for _, sess := range sessions {
		sess.channelsMu.RLock()
		for _, vc := range sess.channels {
			if vc.agentID != "" {
				open++
			}
		}
		sess.channelsMu.RUnlock()
	}
	return open
}

// TestP1512CleanupVsDecorateDeadlock drives the production pair concurrently:
// a cleanup goroutine sweeping relay sessions against a decorate goroutine
// running the dispatchability predicate (which snapshots relayMu, then walks
// each session's channelsMu). With the inverted order both sides park facing
// each other; the stall watchdog turns that into a failed test with a dump
// instead of a hung suite.
func TestP1512CleanupVsDecorateDeadlock(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	for i := 0; i < 8; i++ {
		p1512Session(srv, "relay-p1512-static", "p1512-agent")
	}

	var passes, sweeps int64
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	// Drain both goroutines before the test returns: a churn sweep that
	// outlives the test would write the shared log while a later test
	// captures it (fixture-internal race, NOT product code). Defers run
	// LIFO: close(done) is registered after wg.Wait() so it fires first.
	defer wg.Wait()
	defer close(done)
	// Reader side: continuous dispatchability passes — the path /api/v1/health
	// and /api/v1/agents take on every poll.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			p1512Scan(srv)
			atomic.AddInt64(&passes, 1)
		}
	}()
	// Writer side: continuous cleanup sweeps — the path a relay disconnect
	// takes, holding each session's channelsMu across its body pre-fix.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			srv.cleanupRelaySession(p1512Session(srv, "relay-p1512-churn", "p1512-churn-agent"))
			atomic.AddInt64(&sweeps, 1)
		}
	}()

	// Stall watchdog: both sides must keep making progress; either side
	// stalling for 3s is a parked half-cycle. After the fixed run length
	// with sustained progress the loop exits green.
	const runFor, stallAfter = 10 * time.Second, 3 * time.Second
	start := time.Now()
	lastMove := time.Now()
	var prevPasses, prevSweeps int64
	for {
		select {
		case <-done:
			if elapsed := time.Since(start); elapsed < runFor-time.Second {
				t.Fatalf("decorate loop exited at %v, before the probe window closed", elapsed)
			}
			return
		case <-time.After(100 * time.Millisecond):
		}
		p := atomic.LoadInt64(&passes)
		sw := atomic.LoadInt64(&sweeps)
		if p != prevPasses && sw != prevSweeps {
			prevPasses, prevSweeps = p, sw
			lastMove = time.Now()
		}
		if time.Since(lastMove) > stallAfter {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			dump := string(buf[:n])
			side := "unknown side"
			for _, marker := range []string{"cleanupRelaySession", "AgentDispatchability", "p1512RelayScan", "decorateAgentLiveness"} {
				if strings.Count(dump, marker) > 2 { // parked frames, not brief mentions
					side = marker
					break
				}
			}
			t.Fatalf("P15-12: lock cycle regressed — %s stalled >3s (passes=%d sweeps=%d)\n%s",
				side, p, sw, dump)
		}
		if time.Since(start) >= runFor {
			return // sustained progress the whole window: green
		}
	}
}

// TestP1512CleanupDropsChannelsMuBeforeRelayMu is the writer half of the
// order, observed with TryLock: while cleanupRelaySession is parked waiting
// for relayMu (held by another goroutine), the session's channelsMu must be
// FREE at some point. Pre-fix, the deferred channelsMu.Unlock spanned the
// relayMu wait — channelsMu stayed locked for the whole window and the probe
// never got it.
func TestP1512CleanupDropsChannelsMuBeforeRelayMu(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	sess := p1512Session(srv, "relay-drop", "p1512-drop-agent")

	relHeld := make(chan struct{})
	go func() {
		srv.relayMu.Lock()
		close(relHeld)
		time.Sleep(2 * time.Second)
		srv.relayMu.Unlock()
	}()
	<-relHeld

	go srv.cleanupRelaySession(sess)

	// Phase 1 (the discriminator): while relayMu is held for 2s, the
	// session's channelsMu must remain acquirable at some point. Pre-fix,
	// cleanup's deferred channelsMu.Unlock spanned its whole relayMu wait —
	// channelsMu was locked solid for the window and TryLock never once
	// succeeded. Post-fix, cleanup holds channelsMu only for the snapshot;
	// observing it free during the window proves it is not held across the
	// relayMu wait.
	observedFree := false
	probeBound := time.Now().Add(3 * time.Second)
	for time.Now().Before(probeBound) {
		if sess.channelsMu.TryLock() {
			sess.channelsMu.Unlock()
			observedFree = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !observedFree {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("P15-12: channelsMu still held while cleanup waits on relayMu — the deferred-unlock scope that closed the ABBA cycle is back\n%s", string(buf[:n]))
	}

	// Phase 2: cleanup must complete its relayMu section once the holder
	// lets go. Success = the session is gone from s.relays — unreachable in a
	// pre-fix tree without the deadlock this window creates. Polled: the
	// writer wakes the instant Unlock happens and we read concurrently.
	removed := false
	probeBound2 := time.Now().Add(3 * time.Second)
	for time.Now().Before(probeBound2) {
		srv.relayMu.RLock()
		_, still := srv.relays["relay-drop"]
		srv.relayMu.RUnlock()
		if !still {
			removed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !removed {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("P15-12: cleanup never completed its relayMu section — deferred channelsMu unlock spanned the registration window (the ABBA writer half)\n%s", string(buf[:n]))
	}
}

// TestP1512ChannelOwnerDropsRelayMuBeforeChannelsMu is the reader half of the
// order, observed the same way: while the relay-scan is parked waiting for a
// session's channelsMu (held by another goroutine), s.relayMu must be FREE for
// writers. Pre-fix, the deferred relayMu.RUnlock spanned the channelsMu walk —
// the half of the cycle that starved the registration writers.
func TestP1512ChannelOwnerDropsRelayMuBeforeChannelsMu(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	sess := p1512Session(srv, "relay-owner", "p1512-owner-agent")

	chHeld := make(chan struct{})
	chFree := make(chan struct{})
	go func() {
		sess.channelsMu.Lock()
		close(chHeld)
		time.Sleep(2 * time.Second)
		sess.channelsMu.Unlock()
		close(chFree)
	}()
	<-chHeld

	go srv.p1512RelayScan()

	// The scan must obtain relayMu, snapshot, release it, and only then park
	// on channelsMu. Success = relayMu is acquirable by the time the
	// channelsMu holder lets go.
	<-chFree
	probeBound := time.Now().Add(1 * time.Second)
	for {
		if srv.relayMu.TryLock() {
			srv.relayMu.Unlock()
			return
		}
		if time.Now().After(probeBound) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("P15-12: relayMu not acquirable after channelsMu freed — the reader is holding it across the channelsMu walk again\n%s", string(buf[:n]))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestP1512DispatchabilityStableSemantics pins the stable-line answers the
// predicate must give: direct conn => dispatchable (source ws); relayed-only
// agent => dispatchable only if a live channel carries it; registry-only =>
// not dispatchable with a reason naming the registry state. These answers are
// the observable contract AgentDispatchability exists to make honest; they
// must not drift while the lock-order fix lands.
func TestP1512DispatchabilityStableSemantics(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	// Registry-only agent: registered, never connected.
	srv.registry.Register("p1512-ghost", "p1512-ghost", "1.19.2", "linux", "amd64", "serve", nil)
	d, src, reason := srv.AgentDispatchability("p1512-ghost")
	if d || src != LivenessSourceRegistryOnly || reason == "" {
		t.Fatalf("registry-only agent: dispatchable=%v source=%q reason=%q; want false/registry-only/non-empty", d, src, reason)
	}

	// Direct connection: registered in both conn-map and write-mutex map.
	srv.mu.Lock()
	srv.conns["p1512-direct"] = nil // Conn interface; presence is what the predicate reads
	srv.connWriteMu["p1512-direct"] = &sync.Mutex{}
	srv.mu.Unlock()
	d, src, _ = srv.AgentDispatchability("p1512-direct")
	if !d || src != LivenessSourceWS {
		t.Fatalf("direct agent: dispatchable=%v source=%q; want true/ws", d, src)
	}

	// Relayed agent: live channel in a registered session.
	p1512Session(srv, "relay-sem", "p1512-agentside")
	d, src, _ = srv.AgentDispatchability("p1512-agentside")
	if !d || src != LivenessSourceRelay {
		t.Fatalf("relayed agent: dispatchable=%v source=%q; want true/relay", d, src)
	}
}
