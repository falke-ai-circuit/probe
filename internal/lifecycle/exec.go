package lifecycle

import (
	"context"
	"os/exec"
	"time"
)

// execTimeout bounds a status probe's call into the service manager. A status
// command that blocks on a wedged systemctl is worse than one that reports
// "unavailable": the operator is asking precisely because something is wrong.
const execTimeout = 3 * time.Second

// execCommand runs a helper binary and returns its combined output. Exit status
// is returned as err; callers that treat non-zero output as an answer (systemctl
// is-active exits 3 for "inactive") inspect the output first.
func execCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}
