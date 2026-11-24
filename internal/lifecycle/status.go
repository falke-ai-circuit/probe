// Package lifecycle publishes and reads the agent's *live* run state.
//
// A service manager (systemd, the Windows SCM) can only report the state it
// thinks a unit is in, and a config file can only report what the operator
// intended to start. Neither answers "is the agent running right now, which
// version, and for how long". This package answers that from a status record
// the running process rewrites every HeartbeatInterval, cross-checked against
// the live process table:
//
//   - the record names the PID and the process creation time, so a recycled
//     PID cannot be mistaken for the agent (that is the whole reason the
//     creation time is stored, not just the PID);
//   - the record is only trusted when the named process is alive and its
//     creation time matches within startTimeSlack;
//   - a record left behind by a killed process is reported as not_running with
//     the reason, never as running and never deleted silently by the reader.
//
// The record is written atomically (temp file + rename in the same directory),
// so a reader never observes a half-written file even while the agent updates
// it every 15 s.
package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// FileName is the status record's name inside the resolved state directory.
const FileName = "status.json"

// HeartbeatInterval is how often a running agent refreshes UpdatedAt. It is
// the only liveness signal the record carries beyond the process table, so it
// is deliberately much shorter than any service manager's restart delay.
const HeartbeatInterval = 15 * time.Second

// HeartbeatStale is the age at which a heartbeat stops proving that the agent
// loop is making progress. Past this threshold the process still exists (so
// the state stays "running") but Report.Wedged is set: a live PID with a dead
// loop is exactly the case a process-existence check alone would hide.
const HeartbeatStale = 3 * HeartbeatInterval

// startTimeSlack is how far the process creation time computed at read time may
// differ from the value recorded at write time and still count as the same
// process. It only absorbs clock-tick rounding, never PID recycling: a recycled
// PID differs by whole seconds or more.
const startTimeSlack = 2 * time.Second

// EnvStatusFile names an explicit status record path, overriding resolution.
// EnvStateDir is honoured next so a unit file can set StateDirectory once and
// have both the agent and the status command agree on where the record lives.
const (
	EnvStatusFile = "PROBE_STATUS_FILE"
	EnvStateDir   = "PROBE_STATE_DIR"
)

// State is the record a running agent publishes. Every field is written by the
// process itself at startup and refreshed on each heartbeat; nothing here is
// copied from configuration, because configuration describes intent and this
// file has to describe the process that is actually running.
type State struct {
	PID        int       `json:"pid"`
	Version    string    `json:"version"`
	Mode       string    `json:"mode"`
	Name       string    `json:"name"`
	Hostname   string    `json:"hostname,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	ConfigPath string    `json:"config_path,omitempty"`
	Executable string    `json:"executable,omitempty"`
	// Service records how the process believes it was started:
	// "windows-service" when the SCM launched it, "systemd" when INVOCATION_ID
	// is set, empty otherwise. It is evidence, not a claim: the status command
	// reports the service manager's own view separately.
	Service string `json:"service,omitempty"`
}

// Report is the answer a status command prints. State is one of "running",
// "not_running" or "unknown"; Reason is always populated so a not-running
// verdict says why instead of leaving the reader to guess.
type Report struct {
	Path         string
	Source       string
	State        string
	Reason       string
	Version      string
	PID          int
	StartedAt    time.Time
	Uptime       time.Duration
	HeartbeatAge time.Duration
	Wedged       bool
	// Manager is filled by the platform caller with the service manager's own
	// view ("systemd: inactive (dead)" / "scm: running"), so one line carries
	// both what the manager thinks and what the process table proves.
	Manager string
}

// Report states. They are constants because they are parsed by operators and
// by the unit's own smoke test.
const (
	StateRunning    = "running"
	StateNotRunning = "not_running"
	StateUnknown    = "unknown"
)

// ResolvePath returns the status record path this host must use and names the
// rule that matched, in a fixed order:
//
//  1. explicit path (--status-file), made absolute
//  2. $PROBE_STATUS_FILE
//  3. $PROBE_STATE_DIR/status.json
//  4. the platform's shared state directory, when it can be created/written:
//     /var/lib/probe on Linux, %ProgramData%\PROBE on Windows
//  5. <executable dir>/state/status.json
//  6. <user cache dir>/probe/status.json
//
// The order is identical in the agent and in the status command so the two
// cannot disagree about which file they mean. When they still could (a status
// command run as a different user, say), both print source= so the mismatch is
// visible instead of silent.
func ResolvePath(explicit string) (path, source string) {
	if p := strings.TrimSpace(explicit); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, "explicit --status-file"
		}
		return p, "explicit --status-file"
	}
	if p := strings.TrimSpace(os.Getenv(EnvStatusFile)); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, "$" + EnvStatusFile
		}
		return p, "$" + EnvStatusFile
	}
	if dir := strings.TrimSpace(os.Getenv(EnvStateDir)); dir != "" {
		return filepath.Join(dir, FileName), "$" + EnvStateDir
	}
	if dir, ok := sharedDir(); ok {
		return filepath.Join(dir, FileName), dir + " (shared)"
	}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Join(filepath.Dir(exe), "state")
		if err := os.MkdirAll(dir, 0o755); err == nil && writableDir(dir) {
			return filepath.Join(dir, FileName), "<exe dir>/state"
		}
	}
	if cache, err := os.UserCacheDir(); err == nil {
		dir := filepath.Join(cache, "probe")
		return filepath.Join(dir, FileName), "<user cache dir>/probe"
	}
	return filepath.Join(".", FileName), "current working directory"
}

// sharedDir returns the platform's system-wide directory when it already exists
// or can be created and written by this user.
func sharedDir() (string, bool) {
	var dir string
	switch runtime.GOOS {
	case "windows":
		dir = filepath.Join(os.Getenv("ProgramData"), "PROBE")
		if os.Getenv("ProgramData") == "" {
			return "", false
		}
	case "linux":
		dir = "/var/lib/probe"
	default:
		return "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false
	}
	if !writableDir(dir) {
		return "", false
	}
	return dir, true
}

// writableDir reports whether a file can be created in dir right now. It tests
// the property instead of reading permission bits because the agent may run
// under a different uid than the one that owns the directory.
func writableDir(dir string) bool {
	f, err := os.CreateTemp(dir, ".wtest-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Write atomically replaces the record at path. The rename makes a concurrent
// reader see either the previous complete record or the new one, never a
// truncated file — which is what lets the status command read without a lock.
func Write(path string, st State) error {
	if path == "" {
		return errors.New("lifecycle: empty status path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create status dir: %w", err)
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
	if err != nil {
		return fmt.Errorf("create status temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write status temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync status temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close status temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("chmod status temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename status into place: %w", err)
	}
	return nil
}

// Read parses the record at path. A missing file is reported as (nil, nil) so
// callers can distinguish "never started" from a corrupt record.
func Read(path string) (*State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &st, nil
}

// RemoveIfOwned deletes the record only when it still names pid. A clean
// shutdown must not delete the record of a newer process that has already
// taken over the same path (canary swap, supervisor restart).
func RemoveIfOwned(path string, pid int) {
	st, err := Read(path)
	if err != nil || st == nil || st.PID != pid {
		return
	}
	os.Remove(path)
}

// Probe reads the record at path and verifies it against the live process
// table. now is injected so the verdict is testable without sleeping.
//
// "running" requires all three: the file exists, the named PID is alive, and
// the PID's process creation time matches StartedAt. Anything less is
// "not_running" with the reason spelled out; a corrupt record is "unknown".
func Probe(path string, now time.Time) Report {
	st, err := Read(path)
	rep := Report{Path: path, Source: "resolved"}
	if err != nil {
		rep.State = StateUnknown
		rep.Reason = err.Error()
		return rep
	}
	if st == nil {
		rep.State = StateNotRunning
		rep.Reason = "no status record (never started on this host, or the last run removed it on clean shutdown)"
		return rep
	}

	rep.PID = st.PID
	rep.Version = st.Version
	rep.StartedAt = st.StartedAt
	rep.HeartbeatAge = now.Sub(st.UpdatedAt)

	switch {
	case st.PID <= 0:
		rep.State = StateNotRunning
		rep.Reason = "status record has no pid"
		return rep
	case !ProcessAlive(st.PID):
		rep.State = StateNotRunning
		rep.Reason = fmt.Sprintf("pid %d is not alive (record left by a killed or exited process)", st.PID)
		return rep
	}

	started, ok := ProcessStartTime(st.PID)
	if !ok {
		// The process exists but this platform cannot prove its identity. Report
		// running (the PID is live) and say the identity check was unavailable
		// rather than inventing certainty.
		rep.State = StateRunning
		rep.Reason = "process creation time unavailable on this platform; PID existence only"
	} else {
		if diff := started.Sub(st.StartedAt); diff > startTimeSlack || diff < -startTimeSlack {
			rep.State = StateNotRunning
			rep.Reason = fmt.Sprintf("pid %d was recycled (creation time %s, record says %s)",
				st.PID, started.UTC().Format(time.RFC3339), st.StartedAt.UTC().Format(time.RFC3339))
			return rep
		}
		rep.State = StateRunning
	}
	rep.Uptime = now.Sub(st.StartedAt)
	if rep.Uptime < 0 {
		rep.Uptime = 0
	}
	if rep.HeartbeatAge > HeartbeatStale {
		rep.Wedged = true
	}
	return rep
}

// ProbeResolved resolves the record path with ResolvePath and probes it, so the
// printed line names the rule that produced the file it read. Both platforms'
// status commands go through here: one resolution rule, one verdict.
func ProbeResolved(explicit string, now time.Time) Report {
	path, source := ResolvePath(explicit)
	rep := Probe(path, now)
	rep.Source = source
	return rep
}

// Line renders the report as one key=value line. Key=value (rather than prose)
// is what makes "did the state actually change?" checkable by a script.
func (r Report) Line() string {
	parts := []string{"state=" + r.State}
	if r.Manager != "" {
		parts = append(parts, "manager="+quoteIfNeeded(r.Manager))
	}
	if r.State == StateRunning {
		parts = append(parts,
			"pid="+strconv.Itoa(r.PID),
			"version="+orUnknown(r.Version),
			"uptime="+FormatDuration(r.Uptime),
			"heartbeat_age="+FormatDuration(r.HeartbeatAge))
		if r.Wedged {
			parts = append(parts, "wedged=true")
		}
		if !r.StartedAt.IsZero() {
			parts = append(parts, "started="+r.StartedAt.UTC().Format(time.RFC3339))
		}
	} else {
		if r.PID > 0 {
			parts = append(parts, "last_pid="+strconv.Itoa(r.PID))
		}
		if !r.StartedAt.IsZero() {
			parts = append(parts, "last_started="+r.StartedAt.UTC().Format(time.RFC3339))
		}
		if r.Version != "" {
			parts = append(parts, "last_version="+r.Version)
		}
	}
	parts = append(parts, "status_file="+r.Path, "source="+quoteIfNeeded(r.Source))
	line := strings.Join(parts, " ")
	if r.Reason != "" && r.State != StateRunning {
		line += " reason=" + quoteIfNeeded(r.Reason)
	} else if r.Reason != "" {
		line += " note=" + quoteIfNeeded(r.Reason)
	}
	return line
}

// ExitCode is 0 running, 3 not running, 1 unknown. Three distinct values let a
// unit's ExecStartPost fail loudly on "installed but not running" without
// conflating it with "the status command itself broke".
func (r Report) ExitCode() int {
	switch r.State {
	case StateRunning:
		return 0
	case StateNotRunning:
		return 3
	default:
		return 1
	}
}

// FormatDuration renders a duration at second resolution: "0s", "42s", "1m2s",
// "3h4m5s". Deliberately not time.Duration.String(), which prints "1m2.345s" —
// a status line should not look like it is measuring, only reporting.
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return d.Truncate(time.Second).String()
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}

// quoteIfNeeded keeps a value containing spaces on one parseable line.
func quoteIfNeeded(s string) string {
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t") {
		return strconv.Quote(s)
	}
	return s
}
