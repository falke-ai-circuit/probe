package main

import (
	"os"

	"github.com/falke-ai-circuit/probe/internal/agent"
)

// runGuardian runs the detached-job guardian (P8-04). It delegates to the
// shared CLI implementation in internal/agent so cmd/probe and cmd/probe-client
// cannot drift: the field agent is probe-client, and a missing guardian there
// means a detached exec with no executor.
func runGuardian(args []string) {
	os.Exit(agent.RunGuardianCLI(args))
}
