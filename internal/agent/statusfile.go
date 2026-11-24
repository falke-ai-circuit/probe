package agent

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
	"github.com/falke-ai-circuit/probe/internal/version"
)

// statusPublisher owns the live run-state record. The heartbeat goroutine and
// any later identity correction (the canary promoting itself to canonical after
// a swap) both go through it, so the heartbeat cannot overwrite the corrected
// name/executable with the values captured at startup.
type statusPublisher struct {
	mu   sync.Mutex
	path string
	st   lifecycle.State
}

// identity rewrites the fields that describe *which agent, which file* and
// republishes the record immediately. It is the only write path that may change
// Name or Executable: everything else in the record is a fact fixed at startup.
func (p *statusPublisher) identity(name, executable string) error {
	p.mu.Lock()
	p.st.Name = name
	p.st.Executable = executable
	p.st.UpdatedAt = time.Now()
	st := p.st
	p.mu.Unlock()
	return lifecycle.Write(p.path, st)
}

// heartbeat refreshes UpdatedAt from the shared record.
func (p *statusPublisher) heartbeat(now time.Time) error {
	p.mu.Lock()
	p.st.UpdatedAt = now
	st := p.st
	p.mu.Unlock()
	return lifecycle.Write(p.path, st)
}

// startStatusFile publishes the running-state record that `probe
// --service-status` reads, and returns the function that retires it.
//
// Nothing in the record comes from configuration: PID, process start time,
// version, and mode are facts about this process. That is the difference between
// this file and a config file, and it is why the status command can answer
// "which version is actually running" after an in-place update replaced the
// binary on disk.
//
// A failure to publish is logged and does not stop the agent: a missing status
// file degrades the status command to "not running / no record", which is a
// smaller harm than refusing to serve because a state directory is read-only.
func (a *Agent) startStatusFile() func() {
	path, source := lifecycle.ResolvePath(a.cfg.StatusPath)

	started, ok := lifecycle.ProcessStartTime(os.Getpid())
	if !ok {
		started = time.Now()
	}
	host, _ := os.Hostname()
	exe, _ := os.Executable()

	pub := &statusPublisher{
		path: path,
		st: lifecycle.State{
			PID:        os.Getpid(),
			Version:    version.Display(),
			Mode:       a.cfg.Mode,
			Name:       a.cfg.Name,
			Hostname:   host,
			StartedAt:  started,
			UpdatedAt:  time.Now(),
			ConfigPath: a.cfg.ConfigPath,
			Executable: exe,
			Service:    lifecycle.ServiceMarker(),
		},
	}

	if err := lifecycle.Write(path, pub.st); err != nil {
		log.Printf("[status] cannot publish run state to %s: %v (the status command will report not_running/no record)", path, err)
		return func() {}
	}
	log.Printf("[status] run state published to %s (%s)", path, source)

	a.mu.Lock()
	a.statusPub = pub
	a.mu.Unlock()

	done := make(chan struct{})
	go func() {
		t := time.NewTicker(lifecycle.HeartbeatInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-a.stopped:
				return
			case now := <-t.C:
				if err := pub.heartbeat(now); err != nil {
					log.Printf("[status] heartbeat write failed: %v", err)
				}
			}
		}
	}()

	return func() {
		close(done)
		a.mu.Lock()
		if a.statusPub == pub {
			a.statusPub = nil
		}
		a.mu.Unlock()
		// Remove only while the record still names this process: after a canary
		// swap the file belongs to the newer PID and must be left alone.
		lifecycle.RemoveIfOwned(path, os.Getpid())
	}
}

// republishStatusIdentity corrects the published record after the process's
// identity changed: the canary that committed a swap is no longer
// "<name>-canary" and no longer runs from "<canonical>.next" (the rename moved
// that image to the canonical path, so os.Executable() now resolves there).
// Without this the P12-02 status surface contradicted the registry — name
// p1204-agent-canary, executable a .next that no longer existed (P12-04 F2).
//
// A record that was never published (read-only state dir) is a no-op: there is
// nothing on disk to correct, and the caller logs its own success line.
func (a *Agent) republishStatusIdentity(name, executable string) error {
	a.mu.Lock()
	pub := a.statusPub
	a.mu.Unlock()
	if pub == nil {
		return nil
	}
	return pub.identity(name, executable)
}
