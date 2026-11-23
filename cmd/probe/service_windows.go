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

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "PROBEA"

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

// serviceStatus prints SCM's view of the service — the console requirement.
func serviceStatus() error {
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
	st, err := s.Query()
	if err != nil {
		return err
	}
	fmt.Printf("service=%s state=%s process_id=%d accepts_stop=%v\n",
		serviceName, serviceStateString(st.State), st.ProcessId, st.Accepts&svc.AcceptStop != 0)
	return nil
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
	install   bool
	uninstall bool
	status    bool
	run       bool
	config    string
}

func parseServiceFlags(args []string) serviceFlags {
	var out serviceFlags
	fs := flag.NewFlagSet("service", flag.ContinueOnError)
	fs.BoolVar(&out.install, "install-service", false, "install the agent as a Windows service (admin, once)")
	fs.BoolVar(&out.uninstall, "uninstall-service", false, "remove the Windows service")
	fs.BoolVar(&out.status, "service-status", false, "print the service state from SCM")
	fs.BoolVar(&out.run, "service", false, "internal: run under the Service Control Manager")
	fs.StringVar(&out.config, "config", "", "config file for the installed service")
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
		_ = serviceStatus()
		return true
	case f.uninstall:
		if err := uninstallService(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("removed service %s\n", serviceName)
		return true
	case f.status:
		if err := serviceStatus(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return true
	}
	return false
}
