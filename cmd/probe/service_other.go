//go:build !windows

package main

// P12-02 — non-Windows service surface.
//
// The Windows service host (service_windows.go) is SCM-specific. On other
// platforms the flag names exist so a start script can pass them without a
// build-tag variant of the caller — but they now do real work: --service-status
// reports the live run state (PID, version, uptime) from the record the running
// agent publishes, cross-checked against the process table, plus the service
// manager's own view when one is present. Install/uninstall defer to the
// packaging script (packaging/systemd/install.sh) instead of pretending a
// binary can install a unit without root and a daemon-reload.

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
)

// agentUnitName is the unit the packaging artefacts install. It is one string so
// the status command, the unit file and install.sh cannot drift apart.
const agentUnitName = "probe-agent"

type serviceFlags struct {
	install    bool
	uninstall  bool
	status     bool
	run        bool
	config     string
	statusFile string
}

func parseServiceFlags(args []string) serviceFlags {
	var out serviceFlags
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	fs.BoolVar(&out.install, "install-service", false, "install the agent as a service (on this platform: prints the systemd install path)")
	fs.BoolVar(&out.uninstall, "uninstall-service", false, "remove the service")
	fs.BoolVar(&out.status, "service-status", false, "print live run state: running/not-running, version, uptime")
	fs.BoolVar(&out.run, "service", false, "internal: run under a service manager (Windows SCM only)")
	fs.StringVar(&out.config, "config", "", "config file for the installed service")
	fs.StringVar(&out.statusFile, "status-file", "", "explicit path for the run-state record")
	_ = fs.Parse(args)
	return out
}

// handleServiceFlags runs the requested service action. It returns true when the
// process must exit (the action was a one-shot status query).
func handleServiceFlags(f serviceFlags) bool {
	switch {
	case f.run:
		fmt.Fprintln(os.Stderr, "--service is the Windows SCM entrypoint; on this platform start the agent with 'probe connect' or via the unit file")
		os.Exit(1)
	case f.install:
		fmt.Fprintf(os.Stderr, "systemd units are installed by the packaging script, not by the binary (it needs root and a daemon-reload):\n"+
			"  sudo packaging/systemd/install.sh\n"+
			"See packaging/systemd/README.md. Unit: %s.service\n", agentUnitName)
		os.Exit(2)
	case f.uninstall:
		fmt.Fprintf(os.Stderr, "remove the unit with:\n  sudo systemctl disable --now %s\n  sudo rm -f /etc/systemd/system/%s.service && sudo systemctl daemon-reload\n",
			agentUnitName, agentUnitName)
		os.Exit(2)
	case f.status:
		rep := lifecycle.ProbeResolved(f.statusFile, time.Now())
		rep.Manager = managerView(agentUnitName)
		fmt.Println(rep.Line())
		os.Exit(rep.ExitCode())
	}
	return false
}
