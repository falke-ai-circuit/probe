//go:build windows

package main

// P8-10 (P8-02 §8.5) — the agent as a Windows service.
//
// Today the agent runs as a foreground process: closing the RDP session kills
// it. This file hosts the same agent loop under the Service Control Manager so
// the process is owned by SCM, not by the interactive session, and SCM recovery
// restarts it if it exits (5 s / 10 s / 30 s).
//
// Cost (F15): golang.org/x/sys/windows/svc and svc/mgr are vendored from the
// already-pinned x/sys v0.20.0. go.mod/go.sum do not change.

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "PROBEA"

// procFreeConsole detaches the process from any console. The vendored x/sys
// v0.20.0 does not export FreeConsole (it is not in the pinned surface), so it
// is called through the same pinned kernel32 binding instead of adding a
// dependency or hand-rolling a syscall.
var procFreeConsole = windows.NewLazySystemDLL("kernel32.dll").NewProc("FreeConsole")

// serviceExitCode maps our stop reasons onto SCM's vocabulary.
type probeService struct {
	args []string // arguments passed to the service entrypoint (connect flags)
}

// Execute is the SCM handler. The agent runs in a goroutine; SCM stop requests
// are honoured by asking the same code path the console binary uses.
func (m *probeService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	const accepted = svc.AcceptStop | svc.AcceptShutdown
	changes <- svc.Status{State: svc.StartPending}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runConnect(m.args)
	}()

	changes <- svc.Status{State: svc.Running, Accepts: accepted}
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				changes <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				changes <- svc.Status{State: svc.StopPending}
				// runConnect returns when the agent's stopped channel closes;
				// a service host must not wait forever, so bound the wait and
				// report a clean stop either way.
				select {
				case <-done:
				case <-time.After(15 * time.Second):
					log.Printf("[service] agent did not stop within 15s; exiting")
				}
				return false, 0
			}
		case <-done:
			// The agent loop returned on its own (fatal config error). Exit
			// non-zero so SCM recovery restarts the service.
			return true, 1
		}
	}
}

// runServiceEntry is called from main when --service is present. It is the
// entrypoint SCM invokes for the installed service.
func runServiceEntry() {
	// SCM starts the binary with no useful argv; the connect flags were baked
	// into the service's ImagePath at install time and arrive via args.
	isService, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintf(os.Stderr, "service detection: %v\n", err)
		os.Exit(1)
	}
	if !isService {
		fmt.Fprintln(os.Stderr, "--service is only valid when launched by the Service Control Manager")
		os.Exit(1)
	}

	// P12-02: no console window, and no console as a control surface.
	//
	// This binary is built as a console-subsystem PE, so a process started by
	// SCM receives a console. Session 0 isolation (Vista+) keeps that console
	// off the interactive desktop, so nothing visibly pops for the logged-in
	// user — but the console object still exists, and a console control event
	// (Ctrl+C/Ctrl+Break delivered by anything holding that console) would reach
	// the Go runtime and exit the service with a console status instead of a
	// service stop. Detaching removes both problems. FreeConsole fails with
	// ERROR_INVALID_PARAMETER when the process has no console, which is exactly
	// the state we want, so the error is ignored by design.
	if err := procFreeConsole.Find(); err == nil {
		_, _, _ = procFreeConsole.Call()
	} else {
		// kernel32 always has FreeConsole; a missing proc means the binding is
		// broken, which must be visible rather than silently skipped.
		log.Printf("[service] kernel32!FreeConsole unavailable: %v", err)
	}
	// Mark the process as SCM-owned before the agent loop starts, so the
	// run-state record the status surface reads says how it was started.
	_ = os.Setenv("PROBE_RUN_AS_SERVICE", "windows-service")

	handler := &probeService{args: os.Args[1:]}
	if err := svc.Run(serviceName, handler); err != nil {
		log.Printf("[service] svc.Run: %v", err)
		os.Exit(1)
	}
}

// installService registers the binary with SCM. Needs administrator once; the
// service itself runs as LocalSystem afterwards.
func installService(binPath string, args []string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM (run as administrator): %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s is already installed", serviceName)
	}
	s, err = m.CreateService(serviceName, binPath, mgr.Config{
		DisplayName:  "PROBE Agent",
		Description:  "PROBE remote agent (survives RDP session close)",
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	}, args...)
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer s.Close()

	// SCM recovery: restart after 5 s, 10 s, 30 s; reset the counter daily.
	// This is the auto-restart the ship requirement names.
	recovery := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	_ = s.SetRecoveryActions(recovery, uint32((24 * time.Hour).Seconds()))
	return s.Start()
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %w", err)
	}
	defer s.Close()
	_, _ = s.Control(svc.Stop)
	return s.Delete()
}

// serviceStatus prints SCM's view of the service AND the live run state the
// agent publishes (PID, version, uptime). SCM can only say that the process
// exists; the record proves which build is running and for how long, which is
// what an operator asks after an in-place update.
//
// A service that is not installed is not an error: the agent may be running as
// a plain foreground process, and the lifecycle line still answers the real
// question. The exit code is the lifecycle verdict (0 running, 3 not running,
// 1 unknown) so a smoke test can gate on it.
func serviceStatus(statusFile string) int {
	manager := "scm:not-installed"
	m, err := mgr.Connect()
	if err == nil {
		if s, oerr := m.OpenService(serviceName); oerr == nil {
			if st, qerr := s.Query(); qerr == nil {
				manager = fmt.Sprintf("scm:%s", serviceStateString(st.State))
				fmt.Printf("service=%s state=%s process_id=%d accepts_stop=%v\n",
					serviceName, serviceStateString(st.State), st.ProcessId, st.Accepts&svc.AcceptStop != 0)
			} else {
				manager = "scm:query-failed"
			}
			s.Close()
		}
		m.Disconnect()
	} else {
		manager = "scm:unavailable"
	}

	rep := lifecycle.ProbeResolved(statusFile, time.Now())
	rep.Manager = manager
	fmt.Println(rep.Line())
	return rep.ExitCode()
}

func serviceStateString(s svc.State) string {
	switch s {
	case svc.Stopped:
		return "stopped"
	case svc.StartPending:
		return "start_pending"
	case svc.StopPending:
		return "stop_pending"
	case svc.Running:
		return "running"
	case svc.ContinuePending:
		return "continue_pending"
	case svc.PausePending:
		return "pause_pending"
	case svc.Paused:
		return "paused"
	}
	return fmt.Sprintf("state_%d", s)
}

// isElevated reports whether the current token is in the Administrators group.
// Install is refused early with a readable message instead of an opaque SCM
// access-denied.
func isElevated() bool {
	var sid *windows.SID
	err := windows.AllocateAndInitializeSid(&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID, windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid)
	if err != nil {
		return false
	}
	defer windows.FreeSid(sid)
	token := windows.Token(0)
	member, err := token.IsMember(sid)
	return err == nil && member
}

// serviceFlags is the flag surface shared by both build targets. On Windows it
// drives install/uninstall/status; elsewhere it reports that the feature does
// not exist on this platform.
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
	fs.BoolVar(&out.install, "install-service", false, "install the agent as a Windows service (admin, once)")
	fs.BoolVar(&out.uninstall, "uninstall-service", false, "remove the Windows service")
	fs.BoolVar(&out.status, "service-status", false, "print live run state: running/not-running, version, uptime")
	fs.BoolVar(&out.run, "service", false, "internal: run under the Service Control Manager")
	fs.StringVar(&out.config, "config", "", "config file for the installed service")
	fs.StringVar(&out.statusFile, "status-file", "", "explicit path for the run-state record")
	_ = fs.Parse(args)
	return out
}

// handleServiceFlags runs the requested service action. It returns true when
// the process must exit (the action was a one-shot SCM call).
func handleServiceFlags(f serviceFlags) bool {
	switch {
	case f.run:
		runServiceEntry()
		return true
	case f.install:
		if !isElevated() {
			fmt.Fprintln(os.Stderr, "install-service needs an elevated (administrator) shell")
			os.Exit(1)
		}
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintf(os.Stderr, "resolve executable: %v\n", err)
			os.Exit(1)
		}
		svcArgs := []string{"--service"}
		if f.config != "" {
			svcArgs = append(svcArgs, "--config", f.config)
		}
		if err := installService(exe, svcArgs); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("installed service %s (automatic, recovery 5s/10s/30s)\n", serviceName)
		os.Exit(serviceStatus(f.statusFile))
	case f.uninstall:
		if err := uninstallService(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("removed service %s\n", serviceName)
		return true
	case f.status:
		os.Exit(serviceStatus(f.statusFile))
	}
	return false
}
