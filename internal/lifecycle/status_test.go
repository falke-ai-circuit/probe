package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeTempState writes a record into a fresh temp dir and returns its path.
func writeTempState(t *testing.T, st State) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, st); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return path
}

// selfState builds a record describing the test process, which is the only live
// process whose creation time the test can obtain portably.
func selfState(t *testing.T, now time.Time) State {
	t.Helper()
	started, ok := ProcessStartTime(os.Getpid())
	if !ok {
		t.Skip("process start time unavailable on this platform")
	}
	return State{
		PID:       os.Getpid(),
		Version:   "v9.9.9-test",
		Mode:      "outbound",
		Name:      "test-agent",
		StartedAt: started,
		UpdatedAt: now,
	}
}

// The record must survive a write/read round trip unchanged: the status command
// trusts these fields, so a dropped or mangled one is a lie about a live process.
func TestWriteReadRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	st := State{
		PID:        4242,
		Version:    "v1.19.1",
		Mode:       "outbound",
		Name:       "probe-V6049A",
		Hostname:   "host-a",
		StartedAt:  now.Add(-90 * time.Minute),
		UpdatedAt:  now,
		ConfigPath: "/etc/probe/agent.json",
		Service:    "systemd",
	}
	path := writeTempState(t, st)

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got == nil {
		t.Fatal("Read returned nil for a file that exists")
	}
	if got.PID != st.PID || got.Version != st.Version || got.Mode != st.Mode ||
		got.Name != st.Name || got.ConfigPath != st.ConfigPath || got.Service != st.Service {
		t.Fatalf("round trip lost fields: got %+v want %+v", *got, st)
	}
	if !got.StartedAt.Equal(st.StartedAt) || !got.UpdatedAt.Equal(st.UpdatedAt) {
		t.Fatalf("round trip shifted times: got %v/%v want %v/%v",
			got.StartedAt, got.UpdatedAt, st.StartedAt, st.UpdatedAt)
	}
}

// A missing record is "not_running" with a reason, never "running": the whole
// point of the surface is that an absent file cannot be read as a healthy agent.
func TestProbeMissingRecordIsNotRunning(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	rep := Probe(path, time.Now())
	if rep.State != StateNotRunning {
		t.Fatalf("state = %q, want %q", rep.State, StateNotRunning)
	}
	if rep.Reason == "" {
		t.Fatal("a not_running verdict must carry a reason")
	}
	if rep.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", rep.ExitCode())
	}
}

// This is the state that a kill -9 leaves behind: the file still names a PID
// that no longer exists. It must report not_running, not a constant "installed".
func TestProbeDeadPIDIsNotRunning(t *testing.T) {
	path := writeTempState(t, State{
		PID:       999999, // above any realistic pid_max on this host
		Version:   "v1.19.1",
		StartedAt: time.Now().Add(-time.Hour),
		UpdatedAt: time.Now().Add(-time.Minute),
	})
	rep := Probe(path, time.Now())
	if rep.State != StateNotRunning {
		t.Fatalf("state = %q, want %q (%s)", rep.State, StateNotRunning, rep.Reason)
	}
	if !strings.Contains(rep.Reason, "not alive") {
		t.Fatalf("reason should name the dead pid, got %q", rep.Reason)
	}
	if rep.ExitCode() != 3 {
		t.Fatalf("exit code = %d, want 3", rep.ExitCode())
	}
}

// A live PID with a creation time that disagrees with the record is a recycled
// PID (another process now owns the number). Reporting it as the agent would be
// the exact false positive the creation time exists to prevent.
func TestProbeRecycledPIDIsNotRunning(t *testing.T) {
	if _, ok := ProcessStartTime(os.Getpid()); !ok {
		t.Skip("process start time unavailable on this platform")
	}
	now := time.Now()
	st := selfState(t, now)
	st.StartedAt = st.StartedAt.Add(-10 * time.Minute) // same PID, different process
	path := writeTempState(t, st)

	rep := Probe(path, now)
	if rep.State != StateNotRunning {
		t.Fatalf("state = %q, want %q (%s)", rep.State, StateNotRunning, rep.Reason)
	}
	if !strings.Contains(rep.Reason, "recycled") {
		t.Fatalf("reason should name PID recycling, got %q", rep.Reason)
	}
}

// The live case: a record naming this test process, with a fresh heartbeat,
// must report running with the version from the record and a real uptime. The
// clock is moved forward rather than the record's StartedAt: the creation time
// has to keep matching the process or the recycled-PID check will (correctly)
// reject it.
func TestProbeLiveProcessReportsVersionAndUptime(t *testing.T) {
	started, ok := ProcessStartTime(os.Getpid())
	if !ok {
		t.Skip("process start time unavailable on this platform")
	}
	now := started.Add(95 * time.Second)
	st := State{
		PID:       os.Getpid(),
		Version:   "v9.9.9-test",
		Mode:      "outbound",
		StartedAt: started,
		UpdatedAt: now,
	}
	path := writeTempState(t, st)

	rep := Probe(path, now)
	if rep.State != StateRunning {
		t.Fatalf("state = %q, want %q (%s)", rep.State, StateRunning, rep.Reason)
	}
	if rep.Version != "v9.9.9-test" {
		t.Fatalf("version = %q, want the value from the record", rep.Version)
	}
	if got, want := rep.Uptime, 95*time.Second; got != want {
		t.Fatalf("uptime = %v, want %v", got, want)
	}
	if rep.Wedged {
		t.Fatal("a fresh heartbeat must not be reported as wedged")
	}
	if rep.ExitCode() != 0 {
		t.Fatalf("exit code = %d, want 0", rep.ExitCode())
	}
	line := rep.Line()
	for _, want := range []string{"state=running", "pid=", "version=v9.9.9-test", "uptime=1m35s"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q missing %q", line, want)
		}
	}
}

// A live process whose heartbeat stopped is still running, but the loop behind
// it is not making progress. Both facts must appear; reporting only "running"
// hides a wedged agent, reporting only "dead" would be false.
func TestProbeStaleHeartbeatSetsWedged(t *testing.T) {
	started, ok := ProcessStartTime(os.Getpid())
	if !ok {
		t.Skip("process start time unavailable on this platform")
	}
	now := started.Add(2 * time.Hour) // clock moved forward, record left stale
	st := State{
		PID:       os.Getpid(),
		StartedAt: started,
		UpdatedAt: started.Add(time.Second),
	}
	path := writeTempState(t, st)

	rep := Probe(path, now)
	if rep.State != StateRunning {
		t.Fatalf("state = %q, want %q (%s)", rep.State, StateRunning, rep.Reason)
	}
	if !rep.Wedged {
		t.Fatal("heartbeat older than HeartbeatStale must set wedged")
	}
	if !strings.Contains(rep.Line(), "wedged=true") {
		t.Fatalf("line must carry wedged=true, got %q", rep.Line())
	}
}

// A corrupt record must not be guessed at: it is "unknown" and exits 1, which
// keeps it distinct from "not running" (3) so a smoke test can tell a broken
// file from a stopped agent.
func TestProbeCorruptRecordIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := Probe(path, time.Now())
	if rep.State != StateUnknown {
		t.Fatalf("state = %q, want %q", rep.State, StateUnknown)
	}
	if rep.ExitCode() != 1 {
		t.Fatalf("exit code = %d, want 1", rep.ExitCode())
	}
}

// A clean shutdown removes the record only while it still describes this
// process; a record owned by a newer PID must be left for the new owner.
func TestRemoveIfOwned(t *testing.T) {
	path := writeTempState(t, State{PID: 111, UpdatedAt: time.Now()})
	RemoveIfOwned(path, 222)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("record of pid 111 was removed by pid 222: %v", err)
	}
	RemoveIfOwned(path, 111)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("own record was not removed: %v", err)
	}
}

// Resolution order is a contract: the agent and the status command must land on
// the same file. Explicit beats env beats state dir, and each source is named.
func TestResolvePathOrder(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.json")
	envFile := filepath.Join(dir, "env.json")
	stateDir := filepath.Join(dir, "statedir")

	t.Setenv(EnvStatusFile, envFile)
	t.Setenv(EnvStateDir, stateDir)

	got, src := ResolvePath(explicit)
	if got != explicit {
		t.Fatalf("explicit path: got %q want %q", got, explicit)
	}
	if !strings.Contains(src, "explicit") {
		t.Fatalf("source = %q, want it to name the explicit flag", src)
	}

	got, src = ResolvePath("")
	if got != envFile {
		t.Fatalf("env file: got %q want %q", got, envFile)
	}
	if !strings.Contains(src, EnvStatusFile) {
		t.Fatalf("source = %q, want it to name %s", src, EnvStatusFile)
	}

	t.Setenv(EnvStatusFile, "")
	got, src = ResolvePath("")
	want := filepath.Join(stateDir, FileName)
	if got != want {
		t.Fatalf("state dir: got %q want %q", got, want)
	}
	if !strings.Contains(src, EnvStateDir) {
		t.Fatalf("source = %q, want it to name %s", src, EnvStateDir)
	}
}

// FormatDuration is what the status line prints; sub-second noise must not leak
// into an operator-facing field.
func TestFormatDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                             "0s",
		1500 * time.Millisecond:       "1s",
		42 * time.Second:              "42s",
		62 * time.Second:              "1m2s",
		3*time.Hour + 4*time.Minute:   "3h4m0s",
		-5 * time.Second:              "0s",
	}
	for in, want := range cases {
		if got := FormatDuration(in); got != want {
			t.Errorf("FormatDuration(%v) = %q, want %q", in, got, want)
		}
	}
}

// The status line must stay one line and remain key=value parseable even when a
// reason carries spaces.
func TestLineIsSingleParseableLine(t *testing.T) {
	rep := Probe(filepath.Join(t.TempDir(), FileName), time.Now())
	line := rep.Line()
	if strings.Count(line, "\n") != 0 {
		t.Fatalf("status line contains a newline: %q", line)
	}
	if !strings.HasPrefix(line, "state=") {
		t.Fatalf("line must begin with state=, got %q", line)
	}
	for _, field := range []string{"status_file=", "source=", "reason="} {
		if !strings.Contains(line, field) {
			t.Fatalf("line %q missing %q", line, field)
		}
	}
}
