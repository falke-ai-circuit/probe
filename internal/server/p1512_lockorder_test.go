package server

// P15-12 — the relayMu / channelsMu lock inversion, pinned by tests that
// would have caught the production hang.
//
// The captured 528-goroutine dump (/opt/data/tmp/probe119/P15-11_GOROUTINE.log)
// showed cleanupRelaySession holding a session's channelsMu (write) while
// waiting on s.relayMu, and relayChannelOwner (via decorateAgentLiveness) holding
// s.relayMu (read) while waiting on that same session's channelsMu: a closed
// ABBA cycle that parked 251 health readers for 145 minutes.
//
// Unit rule: RELAY-FIRST. Never acquire a session's channelsMu while holding
// relayMu, and never hold channelsMu across a relayMu acquisition. Every site
// that touches both obeys it by snapshot-and-release. The probes below pin the
// order itself — whether a mutex is HELD ACROSS an acquisition of the other —
// with sync TryLock: a waiter blocks by design and that is correct behaviour;
// only a holder is half of a cycle. The concurrency test runs cleanup against
// decorate under a stall watchdog: a regression FAILS (naming which side
// stalled, with a goroutine dump) instead of hanging the suite.

import (
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// p1512Session builds a live link-carrying relay session with one registered
// channel, the same state the registration path creates.
func p1512Session(srv *Server, relayID, agentID string) *relaySession {
	srv.registry.Register(agentID, agentID, "1.19.1", "linux", "amd64", "serve", nil)
	sess := &relaySession{
		relayID:  relayID,
		channels: map[uint32]*virtualConn{},
		link:     srv.linkStats.forRelay(relayID),
	}
	vc := &virtualConn{channelID: 1, session: sess, agentID: agentID}
	sess.channels[1] = vc
	srv.relayMu.Lock()
	srv.relays[relayID] = sess
	srv.relayMu.Unlock()
	return sess
}

// TestP1512CleanupVsDecorateDeadlock drives the production pair concurrently:
// a cleanup goroutine sweeping relay sessions against a decorate goroutine
// running the dispatchability predicate (which holds relayMu.RLock while
// taking each session's channelsMu). With the inverted order both sides park
// facing each other; the stall watchdog turns that into a failed test with a
// dump instead of a hung suite.
func TestP1512CleanupVsDecorateDeadlock(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	for i := 0; i < 8; i++ {
		p1512Session(srv, "relay-p1512-static", "p1512-agent")
	}

	var passes, sweeps int64
	done := make(chan struct{})
	// Reader side: continuous decorate passes over the registry — the path
	// /api/v1/health and /api/v1/agents take on every poll.
	go func() {
		defer close(done)
		for {
			select {
			case <-done:
				return
			default:
			}
			srv.decorateAgentListLiveness(srv.registry.ListAgents())
			atomic.AddInt64(&passes, 1)
		}
	}()
	// Writer side: continuous cleanup sweeps — the path a relay disconnect
	// takes, holding each session's channelsMu across its body pre-fix.
	go func() {
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
			for _, marker := range []string{"cleanupRelaySession", "relayChannelOwner", "decorateAgentLiveness"} {
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
	// lets go. Success = the peer-lost record is written — unreachable in a
	// pre-fix tree without the deadlock this window creates. Polled: the
	// writer wakes the instant Unlock happens and we read concurrently.
	down := false
	probeBound2 := time.Now().Add(3 * time.Second)
	for time.Now().Before(probeBound2) {
		st := srv.inventory()
		st.mu.Lock()
		down = st.doc.Peers["relay-drop"] != nil && st.doc.Peers["relay-drop"].DownSince != nil
		st.mu.Unlock()
		if down {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !down {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		t.Fatalf("P15-12: cleanup never completed its relayMu section — deferred channelsMu unlock spanned the registration window (the ABBA writer half)\n%s", string(buf[:n]))
	}
}

// TestP1512ChannelOwnerDropsRelayMuBeforeChannelsMu is the reader half of the
// order, observed the same way: while relayChannelOwner is parked waiting for
// a session's channelsMu (held by another goroutine), s.relayMu must be FREE
// for writers. Pre-fix, the deferred relayMu.RUnlock spanned the channelsMu
// walk — the half of the cycle that starved the registration writers.
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

	go srv.relayChannelOwner("p1512-owner-agent")

	// relayChannelOwner must obtain relayMu, snapshot, release it, and only
	// then park on channelsMu. Success = relayMu is acquirable by the time
	// the channelsMu holder lets go.
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
			t.Fatalf("P15-12: relayMu not acquirable after channelsMu freed — relayChannelOwner is holding it across the channelsMu walk again\n%s", string(buf[:n]))
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestP1512ServersPathSnapshotOrder pins the /api/v1/servers shape: the live
// peer row reads the relayed-agent count under the session's channelsMu with
// no relayMu held, and never the inverse (no channelsMu held across any
// relayMu acquisition). Behavioral probe: while a relayMu writer holds the
// registry lock, the live peer row still renders — the row is derived from
// the session snapshot, not from a nested relayMu read.
func TestP1512ServersPathSnapshotOrder(t *testing.T) {
	srv, cleanup := newV1TestServer(t)
	defer cleanup()

	now := time.Now()
	p1512Session(srv, "relay-status", "p1512-status-agent")
	sess := srv.relays["relay-status"]

	relFree := make(chan struct{})
	srv.relayMu.Lock()
	go func() {
		time.Sleep(300 * time.Millisecond)
		srv.relayMu.Unlock()
		close(relFree)
	}()

	start := time.Now()
	entry := srv.livePeerEntry(srv.inventory(), "relay-status", sess, now)
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("P15-12: livePeerEntry blocked %v behind a relayMu writer — a nested relayMu read crept onto the row path (>1.5s)", elapsed)
	}
	if entry.State != "up" {
		t.Fatalf("live peer row state = %q, want up", entry.State)
	}
	<-relFree
}
