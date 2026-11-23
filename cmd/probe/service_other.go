//go:build !windows

package main

// P8-10 (P8-02 §8.5) — non-Windows stub. The Windows service host is
// Windows-only by construction; on every other platform the flags exist (so a
// start script can pass them without a build-tag variant of the caller) and
// report the absence instead of pretending.

import (
	"flag"
	"fmt"
	"os"
)

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
	fs.BoolVar(&out.install, "install-service", false, "install the agent as a Windows service (Windows only)")
	fs.BoolVar(&out.uninstall, "uninstall-service", false, "remove the Windows service (Windows only)")
	fs.BoolVar(&out.status, "service-status", false, "print the service state (Windows only)")
	fs.BoolVar(&out.run, "service", false, "internal: run under the Service Control Manager (Windows only)")
	fs.StringVar(&out.config, "config", "", "config file for the installed service")
	_ = fs.Parse(args)
	return out
}

func handleServiceFlags(f serviceFlags) bool {
	if f.install || f.uninstall || f.status || f.run {
		fmt.Fprintln(os.Stderr, "Windows service mode is not available on this platform")
		os.Exit(1)
	}
	return false
}
