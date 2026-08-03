//go:build !windows

package agent

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

type debugSession struct {
	id             string
	pid            int
	name           string
	path           string
	baseAddr       uint64
	memFile        *os.File // /proc/{pid}/mem
	ptraceAttached bool     // whether ptrace attach succeeded
}

type debugManager struct {
	mu       sync.Mutex
	sessions map[string]*debugSession
	nextID   int
}

func newDebugManager() *debugManager {
	return &debugManager{sessions: make(map[string]*debugSession)}
}

// parseProcMapsLine parses a single line from /proc/{pid}/maps
// Format: start-end perms offset dev inode pathname
// Example: 00400000-00401000 r--p 00000000 fd:00 12345  /usr/bin/ls
func parseProcMapsLine(line string) (start, end uint64, perms string, offset uint64, pathname string) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, 0, "", 0, ""
	}
	addrRange := strings.Split(fields[0], "-")
	if len(addrRange) != 2 {
		return 0, 0, "", 0, ""
	}
	start, _ = strconv.ParseUint(addrRange[0], 16, 64)
	end, _ = strconv.ParseUint(addrRange[1], 16, 64)
	perms = fields[1]
	offset, _ = strconv.ParseUint(fields[2], 16, 64)
	if len(fields) >= 6 {
		pathname = strings.Join(fields[5:], " ")
	}
	return start, end, perms, offset, pathname
}

// parseProtFlags converts Linux permission string (rwxp) to Windows-style protect flags
// for compatibility with the existing protocol.DebugMemRegion struct.
func parseProtFlags(perms string) (state, protect, regionType uint32) {
	if strings.Contains(perms, "r") {
		state = 0x1000 // MEM_COMMIT equivalent
	}
	if strings.Contains(perms, "w") {
		protect |= 0x40 // PAGE_READWRITE
	} else if strings.Contains(perms, "x") {
		protect |= 0x20 // PAGE_EXECUTE
	} else {
		protect |= 0x02 // PAGE_READONLY
	}
	if strings.Contains(perms, "x") && strings.Contains(perms, "w") {
		protect = 0x80 // PAGE_EXECUTE_READWRITE
	}
	regionType = 0x1000000 // MEM_IMAGE equivalent
	if !strings.Contains(perms, "x") {
		regionType = 0x40000 // MEM_MAPPED
	}
	return state, protect, regionType
}

// handleDebugAttach attaches to a process by PID or name using ptrace on Linux.
// It opens /proc/{pid}/mem for memory reading and reads /proc/{pid}/maps for module info.
func (a *Agent) handleDebugAttach(env protocol.Envelope) protocol.Envelope {
	var params protocol.DebugAttachParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	pid := params.PID
	if pid == 0 && params.ProcessName != "" {
		found, err := findProcessByName(params.ProcessName)
		if err != nil {
			return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("find process failed: %v", err))
		}
		pid = found
	}
	if pid == 0 {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "pid or process_name required")
	}

	// Try ptrace attach (optional — allows controlling the process).
	// If ptrace fails (e.g. Yama ptrace_scope restriction), we can still
	// read /proc/{pid}/mem and /proc/{pid}/maps for inspection-only mode
	// when running as root or the same user.
	ptraceAttached := false
	if perr := syscall.PtraceAttach(pid); perr == nil {
		ptraceAttached = true
		// Wait for the process to stop (SIGSTOP delivered by PTRACE_ATTACH)
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	}

	// Open /proc/{pid}/mem for reading
	memPath := fmt.Sprintf("/proc/%d/mem", pid)
	memFile, err := os.Open(memPath)
	if err != nil {
		// mem not readable — still useful for modules/maps listing
		memFile = nil
	}

	a.debugMgr.mu.Lock()
	a.debugMgr.nextID++
	id := fmt.Sprintf("dbg-%d", a.debugMgr.nextID)
	a.debugMgr.mu.Unlock()

	session := &debugSession{
		id:             id,
		pid:            pid,
		memFile:        memFile,
		ptraceAttached: ptraceAttached,
	}

	// Read process name and exe path from /proc
	exePath, _ := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	session.path = exePath
	session.name = filepath.Base(exePath)

	// Read /proc/{pid}/maps to find base address (first executable mapping)
	mapsFile, err := os.Open(fmt.Sprintf("/proc/%d/maps", pid))
	if err == nil {
		scanner := bufio.NewScanner(mapsFile)
		for scanner.Scan() {
			line := scanner.Text()
			start, _, perms, _, pathname := parseProcMapsLine(line)
			if pathname != "" && strings.Contains(perms, "x") {
				// First executable mapping with a pathname = base address
				session.baseAddr = start
				break
			}
		}
		mapsFile.Close()
	}

	a.debugMgr.mu.Lock()
	a.debugMgr.sessions[id] = session
	a.debugMgr.mu.Unlock()

	return protocol.NewResult(env.ID, "debug_attached", map[string]interface{}{
		"debug_id":  id,
		"pid":       pid,
		"name":      session.name,
		"path":      session.path,
		"base_addr": session.baseAddr,
	})
}

// handleDebugReadMem reads memory from the attached process via /proc/{pid}/mem
func (a *Agent) handleDebugReadMem(env protocol.Envelope) protocol.Envelope {
	var params protocol.DebugReadMemParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.debugMgr.mu.Lock()
	session, ok := a.debugMgr.sessions[params.DebugID]
	a.debugMgr.mu.Unlock()
	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "debug session not found")
	}

	if session.memFile == nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, "memory file not available (check permissions)")
	}

	buf := make([]byte, params.Size)
	nRead, err := session.memFile.ReadAt(buf, int64(params.Address))
	if err != nil && nRead == 0 {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("mem read failed: %v", err))
	}

	data := buf[:nRead]
	b64 := base64.StdEncoding.EncodeToString(data)
	hexStr := hex.EncodeToString(data)

	return protocol.NewResult(env.ID, "debug_read_mem_result", map[string]interface{}{
		"data":     b64,
		"hex_data": hexStr,
		"size":     nRead,
		"address":  params.Address,
	})
}

// handleDebugModules lists loaded modules (shared libraries) from /proc/{pid}/maps
func (a *Agent) handleDebugModules(env protocol.Envelope) protocol.Envelope {
	var params protocol.DebugModulesParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.debugMgr.mu.Lock()
	session, ok := a.debugMgr.sessions[params.DebugID]
	a.debugMgr.mu.Unlock()
	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "debug session not found")
	}

	mapsFile, err := os.Open(fmt.Sprintf("/proc/%d/maps", session.pid))
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("read maps failed: %v", err))
	}
	defer mapsFile.Close()

	// Deduplicate by pathname — maps has multiple entries per library
	seen := make(map[string]bool)
	var moduleList []protocol.DebugModuleInfo

	scanner := bufio.NewScanner(mapsFile)
	for scanner.Scan() {
		line := scanner.Text()
		start, end, _, _, pathname := parseProcMapsLine(line)
		if pathname == "" || pathname[0] == '[' {
			continue // skip [heap], [stack], [anon] etc
		}
		if seen[pathname] {
			continue
		}
		seen[pathname] = true
		moduleList = append(moduleList, protocol.DebugModuleInfo{
			Name:     filepath.Base(pathname),
			BaseAddr: start,
			Size:     int(end - start),
			Path:     pathname,
		})
	}

	return protocol.NewResult(env.ID, "debug_modules_result", map[string]interface{}{
		"modules": moduleList,
	})
}

// handleDebugMemQuery queries memory region info at an address using /proc/{pid}/maps
func (a *Agent) handleDebugMemQuery(env protocol.Envelope) protocol.Envelope {
	var params protocol.DebugMemQueryParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.debugMgr.mu.Lock()
	session, ok := a.debugMgr.sessions[params.DebugID]
	a.debugMgr.mu.Unlock()
	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "debug session not found")
	}

	mapsFile, err := os.Open(fmt.Sprintf("/proc/%d/maps", session.pid))
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, fmt.Sprintf("read maps failed: %v", err))
	}
	defer mapsFile.Close()

	scanner := bufio.NewScanner(mapsFile)
	for scanner.Scan() {
		line := scanner.Text()
		start, end, perms, _, _ := parseProcMapsLine(line)
		if params.Address >= start && params.Address < end {
			state, protect, regionType := parseProtFlags(perms)
			return protocol.NewResult(env.ID, "debug_mem_query_result", map[string]interface{}{
				"region": protocol.DebugMemRegion{
					BaseAddress: start,
					Size:        end - start,
					State:       state,
					Protect:     protect,
					Type:        regionType,
				},
			})
		}
	}

	return protocol.NewError(env.ID, protocol.ErrNotFound, "no memory region found at address")
}

// handleDebugDetach detaches from the process via ptrace and closes mem file
func (a *Agent) handleDebugDetach(env protocol.Envelope) protocol.Envelope {
	var params protocol.DebugDetachParams
	if err := json.Unmarshal(env.Params, &params); err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}

	a.debugMgr.mu.Lock()
	session, ok := a.debugMgr.sessions[params.DebugID]
	if ok {
		delete(a.debugMgr.sessions, params.DebugID)
	}
	a.debugMgr.mu.Unlock()

	if !ok {
		return protocol.NewError(env.ID, protocol.ErrNotFound, "debug session not found")
	}

	if session.memFile != nil {
		session.memFile.Close()
	}
	// Only detach ptrace if we successfully attached
	if session.ptraceAttached {
		_ = syscall.PtraceDetach(session.pid)
	}

	return protocol.NewResult(env.ID, "debug_detached", map[string]interface{}{
		"detached": true,
		"debug_id": params.DebugID,
	})
}

func (a *Agent) closeAllDebug() {
	a.debugMgr.mu.Lock()
	defer a.debugMgr.mu.Unlock()
	for id, session := range a.debugMgr.sessions {
		if session.memFile != nil {
			session.memFile.Close()
		}
		if session.ptraceAttached {
			_ = syscall.PtraceDetach(session.pid)
		}
		delete(a.debugMgr.sessions, id)
	}
}

// findProcessByName finds a PID by process name by scanning /proc on Linux
func findProcessByName(name string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("cannot read /proc: %v", err)
	}

	name = strings.ToLower(name)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		// Read /proc/{pid}/cmdline
		cmdlineData, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue
		}

		// cmdline is null-byte separated; take the first arg (the executable)
		parts := strings.Split(string(cmdlineData), "\x00")
		if len(parts) == 0 || parts[0] == "" {
			// Try /proc/{pid}/exe symlink
			exePath, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
			if err != nil {
				continue
			}
			if strings.EqualFold(filepath.Base(exePath), name) {
				return pid, nil
			}
			continue
		}

		exeName := filepath.Base(parts[0])
		if strings.EqualFold(exeName, name) {
			return pid, nil
		}
	}

	return 0, fmt.Errorf("process %s not found", name)
}

// Ensure unsafe is used (for potential future raw ptrace calls)
var _ = unsafe.Sizeof(0)