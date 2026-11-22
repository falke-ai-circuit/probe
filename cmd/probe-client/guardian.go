package main

import (
	"os"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// runGuardian is the probe-client half of the guardian entry point (P8-04).
// The field agent is this binary, so without it a detached exec has no
// executor and no cleanup. It delegates to the shared implementation so the
// two entrypoints cannot drift.
func runGuardian(args []string) {
	os.Exit(agent.RunGuardianCLI(args))
}
