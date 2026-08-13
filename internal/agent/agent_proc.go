package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/obfs"
	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// handleProcList returns a list of running processes on the agent machine.
// Uses tasklist on Windows, ps on Linux/macOS.
func (a *Agent) handleProcList(env protocol.Envelope) protocol.Envelope {
	var procs []protocol.ProcessInfo
	var err error

	if runtime.GOOS == "windows" {
		procs, err = windowsProcessList()
	} else {
		procs, err = unixProcessList()
	}

	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}

	return protocol.NewResult(env.ID, protocol.TypeProcListResult, protocol.ProcessListResult{Processes: procs})
}

// handleProcKill kills a process by PID.
// In sandboxed mode, only processes started by this agent (via proc_start or exec)
// can be killed — protecting other system processes.
func (a *Agent) handleProcKill(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.TaskStopParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	// Sandbox check: in sandboxed mode, only kill PIDs this agent started.
	// Bypass flag (user-approved override) skips this check.
	if !env.Bypass && (a.cfg.Permissions == "sandboxed" || a.cfg.Permissions == "standard") {
		a.spawnedPIDMu.Lock()
		allowed := a.spawnedPIDs[params.PID]
		a.spawnedPIDMu.Unlock()
		if !allowed {
			return protocol.NewError(env.ID, "permission_denied",
				fmt.Sprintf("cannot kill PID %d: process was not started by this agent (sandboxed mode protects system processes)", params.PID))
		}
	}

	var killErr error
	if runtime.GOOS == "windows" {
		// taskkill /PID <pid> /F
		killErr = exec.Command("taskkill", "/PID", fmt.Sprintf("%d", params.PID), "/F").Run()
	} else {
		sig := params.Signal
		if sig == 0 {
			sig = 15 // SIGTERM
		}
		killErr = exec.Command("kill", "-"+fmt.Sprintf("%d", sig), fmt.Sprintf("%d", params.PID)).Run()
	}

	if killErr != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("kill failed: %v", killErr))
	}

	// Remove from tracked PIDs
	a.spawnedPIDMu.Lock()
	delete(a.spawnedPIDs, params.PID)
	a.spawnedPIDMu.Unlock()

	return protocol.NewResult(env.ID, protocol.TypeProcKillResult, protocol.TaskStopResult{Killed: true, PID: params.PID})
}

// handleProcStart starts a process. If background=true, uses start /b on Windows
// or & on Unix. Otherwise runs synchronously and returns output.
// PIDs of started processes are tracked so sandboxed mode can restrict
// task_stop/proc_kill to only processes this agent started.
func (a *Agent) handleProcStart(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.ProcStartParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	if params.Background {
		// Start in background — don't wait for output
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command(obfs.Str("\x39\x37\x3e"), obfs.Str("\x75\x39"), "start", "/b", params.Command)
		} else {
			cmd = exec.Command("sh", "-c", params.Command)
		}
		if params.WorkDir != "" {
			cmd.Dir = params.WorkDir
		}
		if err := cmd.Start(); err != nil {
			return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("start failed: %v", err))
		}
		pid := cmd.Process.Pid
		// Track PID for sandboxed mode
		a.spawnedPIDMu.Lock()
		a.spawnedPIDs[pid] = true
		a.spawnedPIDMu.Unlock()
		return protocol.NewResult(env.ID, protocol.TypeProcStartResult, protocol.ProcStartResult{
			PID:      pid,
			ExitCode: 0,
		})
	}

	// Foreground — run and capture output
	timeout := 60
	start := time.Now()
	result, execErr := a.plat.Exec(params.Command, timeout, params.WorkDir, params.Env)
	_ = start
	if execErr != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, execErr.Error())
	}
	return protocol.NewResult(env.ID, protocol.TypeProcStartResult, protocol.ProcStartResult{
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		ExitCode: result.ExitCode,
	})
}

// windowsProcessList runs tasklist and parses the output.
func windowsProcessList() ([]protocol.ProcessInfo, error) {
	out, err := exec.Command(obfs.Str("\x2e\x3b\x29\x31\x36\x33\x29\x2e"), "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, fmt.Errorf("process list failed: %v", err)
	}

	var procs []protocol.ProcessInfo
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// CSV format: "Name","PID","SessionName","Session#","MemUsage"
		// Simple CSV parse (handles quoted fields)
		fields := parseCSVLine(line)
		if len(fields) < 2 {
			continue
		}
		pid := 0
		fmt.Sscanf(fields[1], "%d", &pid)
		name := strings.Trim(fields[0], "\"")
		procs = append(procs, protocol.ProcessInfo{
			PID:  pid,
			Name: name,
		})
	}
	return procs, nil
}

// unixProcessList runs ps and parses the output.
func unixProcessList() ([]protocol.ProcessInfo, error) {
	out, err := exec.Command("ps", "-eo", "pid,comm,%cpu,%mem", "--no-headers").Output()
	if err != nil {
		return nil, fmt.Errorf("ps failed: %v", err)
	}

	var procs []protocol.ProcessInfo
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid := 0
		fmt.Sscanf(fields[0], "%d", &pid)
		cpu := 0.0
		fmt.Sscanf(fields[2], "%f", &cpu)
		mem := 0.0
		fmt.Sscanf(fields[3], "%f", &mem)
		procs = append(procs, protocol.ProcessInfo{
			PID:        pid,
			Name:       fields[1],
			CPUPercent: cpu,
			MemoryMB:   mem,
		})
	}
	return procs, nil
}

// parseCSVLine does a simple CSV line parse for fields that may contain
// quoted values with commas inside.
func parseCSVLine(line string) []string {
	var fields []string
	inQuote := false
	current := ""
	for _, ch := range line {
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if ch == ',' && !inQuote {
			fields = append(fields, current)
			current = ""
			continue
		}
		current += string(ch)
	}
	fields = append(fields, current)
	return fields
}