package agent

import (
	"log"
	"os"
	"time"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
	"github.com/falke-ai-circuit/probe/internal/version"
)

// startStatusFile publishes the running-state record that `probe
// --service-status` reads, and returns the function that retires it.
//
// Nothing in the record comes from configuration: PID, process start time,
// version and mode are facts about this process. That is the difference between
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

	st := lifecycle.State{
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
	}

	if err := lifecycle.Write(path, st); err != nil {
		log.Printf("[status] cannot publish run state to %s: %v (the status command will report not_running/no record)", path, err)
		return func() {}
	}
	log.Printf("[status] run state published to %s (%s)", path, source)

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
				st.UpdatedAt = now
				if err := lifecycle.Write(path, st); err != nil {
					log.Printf("[status] heartbeat write failed: %v", err)
				}
			}
		}
	}()

	return func() {
		close(done)
		// Remove only while the record still names this process: after a canary
		// swap the file belongs to the newer PID and must be left alone.
		lifecycle.RemoveIfOwned(path, os.Getpid())
	}
}
