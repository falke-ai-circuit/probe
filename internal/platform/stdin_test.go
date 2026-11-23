//go:build linux

package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A6: the secret travels on stdin, so it is absent from the child's argv.
// This runs the production function (ExecWithStdin) against a real child and
// reads /proc/<pid>/cmdline while the child is alive.
func TestExecWithStdin_SecretNotInArgv(t *testing.T) {
	const secret = "FIXTURE-SW-PASSWORD-7c1f"
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	outFile := filepath.Join(dir, "out")

	// The child records its pid, waits long enough to be observed, then drains
	// stdin into a file (proof the value really arrived over the pipe).
	script := "echo $$ > " + pidFile + "; sleep 1; cat > " + outFile

	done := make(chan struct{})
	var resultCode int
	go func() {
		res, err := ExecWithStdin(script, 10, dir, nil, []byte(secret))
		if err != nil {
			t.Errorf("exec: %v", err)
		}
		resultCode = res.ExitCode
		close(done)
	}()

	var pid int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(pidFile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(raw)))
			if pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("child never reported its pid")
	}
	cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		t.Fatalf("read cmdline: %v", err)
	}
	argv := strings.ReplaceAll(string(cmdline), "\x00", " ")
	t.Logf("A6 observed cmdline: %s", argv)
	if strings.Contains(argv, secret) {
		t.Fatalf("secret is in argv: %s", argv)
	}

	<-done
	if resultCode != 0 {
		t.Fatalf("child exited %d", resultCode)
	}
	raw, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("stdin was not delivered: %v", err)
	}
	if string(raw) != secret {
		t.Fatalf("stdin payload = %q", string(raw))
	}
}
