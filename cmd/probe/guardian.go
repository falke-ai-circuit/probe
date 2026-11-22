package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// runGuardian runs the detached-job guardian for one job directory. It is
// spawned by a detached exec (same binary, `guardian --job-dir <dir>`) and is
// expected to outlive the agent that spawned it.
func runGuardian(args []string) {
	fs := flag.NewFlagSet("guardian", flag.ExitOnError)
	jobDir := fs.String("job-dir", "", "detached job spool directory")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "guardian: %v\n", err)
		os.Exit(2)
	}
	if *jobDir == "" {
		fmt.Fprintln(os.Stderr, "guardian: --job-dir is required")
		os.Exit(2)
	}
	os.Exit(agent.RunGuardian(*jobDir))
}
