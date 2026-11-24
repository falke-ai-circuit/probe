//go:build linux

package main

import (
	"fmt"

	"github.com/falke-ai-circuit/probe/internal/lifecycle"
)

// managerView returns systemd's own opinion of the unit, or "unavailable" when
// systemd is not running as PID 1 (containers, build hosts). The distinction
// matters: on a host without systemd the live run-state record is the only
// evidence, and the status line says so instead of implying a unit exists.
func managerView(unit string) string {
	state, ok := lifecycle.SystemdUnitActive(unit)
	if !ok {
		return "systemd:unavailable"
	}
	return fmt.Sprintf("systemd:%s", state)
}
