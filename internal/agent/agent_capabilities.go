package agent

import (
	"strconv"
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// ---------------------------------------------------------------------------
// SOCKS5 Proxy — STUB (protocol defined, handler returns "not implemented")
// ---------------------------------------------------------------------------

func (a *Agent) handleSocks5Start(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"SOCKS5 proxy not yet implemented")
}

func (a *Agent) handleSocks5Stop(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"SOCKS5 proxy not yet implemented")
}

// ---------------------------------------------------------------------------
// Port Forwarding — STUB (protocol defined, handler returns "not implemented")
// ---------------------------------------------------------------------------

func (a *Agent) handlePortForward(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"Port forwarding not yet implemented")
}

// ---------------------------------------------------------------------------
// Port Scanning — fully implemented (TCP connect scan)
// ---------------------------------------------------------------------------

func (a *Agent) handlePortScan(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.PortScanParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if params.Host == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "host is required")
	}
	if len(params.Ports) == 0 {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "ports list is required")
	}

	timeout := time.Duration(params.Timeout) * time.Millisecond
	if timeout <= 0 {
		timeout = 1000 * time.Millisecond
	}

	result := protocol.PortScanResult{
		Host:    params.Host,
		Open:    []int{},
		Results: []protocol.PortScanEntry{},
	}

	for _, port := range params.Ports {
		entry := protocol.PortScanEntry{Port: port}
		addr := net.JoinHostPort(params.Host, strconv.Itoa(port))
		conn, err := net.DialTimeout("tcp", addr, timeout)
		if err != nil {
			entry.State = "closed"
		} else {
			entry.State = "open"
			result.Open = append(result.Open, port)
			// Try to read a banner (some services send one on connect)
			conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			buf := make([]byte, 256)
			n, _ := conn.Read(buf)
			if n > 0 {
				entry.Banner = strings.TrimSpace(string(buf[:n]))
			}
			conn.Close()
		}
		result.Results = append(result.Results, entry)
	}

	return protocol.NewResult(env.ID, protocol.TypePortScanResult, result)
}

// ---------------------------------------------------------------------------
// Net Connections — fully implemented (Linux /proc/net, Windows netstat fallback)
// ---------------------------------------------------------------------------

func (a *Agent) handleNetConnections(env protocol.Envelope) protocol.Envelope {
	var conns []protocol.NetConnectionEntry
	var err error

	switch runtime.GOOS {
	case "linux":
		conns, err = readProcNetConnections()
	case "windows":
		conns, err = readNetstatConnections()
	default:
		// Fallback: try /proc/net (works on Linux-like systems)
		conns, err = readProcNetConnections()
	}

	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInternal, err.Error())
	}

	return protocol.NewResult(env.ID, protocol.TypeNetConnectionsResult,
		protocol.NetConnectionsResult{Connections: conns})
}

// readProcNetConnections reads TCP and UDP connections from /proc/net on Linux.
func readProcNetConnections() ([]protocol.NetConnectionEntry, error) {
	var conns []protocol.NetConnectionEntry

	// Read TCP connections
	tcpConns, err := parseProcNetFile("/proc/net/tcp", "tcp")
	if err == nil {
		conns = append(conns, tcpConns...)
	}

	// Read TCP6 connections
	tcp6Conns, err := parseProcNetFile("/proc/net/tcp6", "tcp")
	if err == nil {
		conns = append(conns, tcp6Conns...)
	}

	// Read UDP connections
	udpConns, err := parseProcNetFile("/proc/net/udp", "udp")
	if err == nil {
		conns = append(conns, udpConns...)
	}

	// Read UDP6 connections
	udp6Conns, err := parseProcNetFile("/proc/net/udp6", "udp")
	if err == nil {
		conns = append(conns, udp6Conns...)
	}

	return conns, nil
}

// parseProcNetFile parses a /proc/net/tcp or /proc/net/udp format file.
func parseProcNetFile(path, proto string) ([]protocol.NetConnectionEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var conns []protocol.NetConnectionEntry
	scanner := bufio.NewScanner(f)
	firstLine := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if firstLine {
			firstLine = false
			continue // skip header
		}
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		// fields[1] = local_address:port (hex)
		// fields[2] = remote_address:port (hex)
		// fields[3] = state (hex)
		localAddr, localPort := parseHexAddrPort(fields[1])
		remoteAddr, remotePort := parseHexAddrPort(fields[2])
		state := parseTCPState(fields[3])

		conns = append(conns, protocol.NetConnectionEntry{
			Protocol:      proto,
			LocalAddress:  localAddr,
			LocalPort:     localPort,
			RemoteAddress: remoteAddr,
			RemotePort:    remotePort,
			State:         state,
		})
	}
	return conns, scanner.Err()
}

// parseHexAddrPort parses a hex address:port pair from /proc/net format.
// e.g. "0100007F:1F90" → "127.0.0.1", 8080
func parseHexAddrPort(s string) (string, int) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return s, 0
	}
	hexAddr := parts[0]
	hexPort := parts[1]

	// Parse port (hex)
	var port int
	fmt.Sscanf(hexPort, "%x", &port)

	// Parse address (hex, little-endian for IPv4)
	if len(hexAddr) == 8 {
		// IPv4: 4 bytes in hex, little-endian
		var b [4]byte
		for i := 0; i < 4; i++ {
			var v byte
			fmt.Sscanf(hexAddr[i*2:i*2+2], "%02x", &v)
			b[3-i] = v // reverse byte order
		}
		return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3]), port
	}

	// IPv6: 32 hex chars = 16 bytes, stored in little-endian groups of 4 bytes
	if len(hexAddr) == 32 {
		ip := make(net.IP, 16)
		for i := 0; i < 16; i++ {
			var v byte
			fmt.Sscanf(hexAddr[i*2:i*2+2], "%02x", &v)
			// /proc/net stores IPv6 in little-endian 32-bit words
			// Each group of 4 bytes is reversed
			groupIdx := i / 4
			posInGroup := i % 4
			reversedPos := groupIdx*4 + (3 - posInGroup)
			ip[reversedPos] = v
		}
		return ip.String(), port
	}

	return hexAddr, port
}

// parseTCPState converts a hex TCP state code to a human-readable string.
func parseTCPState(hexState string) string {
	stateMap := map[string]string{
		"01": "ESTABLISHED",
		"02": "SYN_SENT",
		"03": "SYN_RECV",
		"04": "FIN_WAIT1",
		"05": "FIN_WAIT2",
		"06": "TIME_WAIT",
		"07": "CLOSE",
		"08": "CLOSE_WAIT",
		"09": "LAST_ACK",
		"0A": "LISTEN",
		"0B": "CLOSING",
	}
	if state, ok := stateMap[hexState]; ok {
		return state
	}
	return hexState
}

// readNetstatConnections reads connections via the netstat command (Windows fallback).
func readNetstatConnections() ([]protocol.NetConnectionEntry, error) {
	// On Windows, we'd run `netstat -ano` and parse it.
	// This is a stub that returns an error since we're on Linux in tests.
	return nil, fmt.Errorf("netstat parsing not available on this platform")
}

// ---------------------------------------------------------------------------
// Autostart — STUB (protocol defined, handler returns "not implemented")
// ---------------------------------------------------------------------------

func (a *Agent) handleAutostartEnable(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"Autostart not yet implemented")
}

func (a *Agent) handleAutostartDisable(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"Autostart not yet implemented")
}

func (a *Agent) handleAutostartStatus(env protocol.Envelope) protocol.Envelope {
	return protocol.NewError(env.ID, protocol.ErrPlatformNotSupported,
		"Autostart not yet implemented")
}

// ---------------------------------------------------------------------------
// File Search — fully implemented (filepath.Walk + filepath.Match)
// ---------------------------------------------------------------------------

func (a *Agent) handleFileSearch(env protocol.Envelope) protocol.Envelope {
	params, err := protocol.ParseCommand[protocol.FileSearchParams](env)
	if err != nil {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, err.Error())
	}
	if params.RootPath == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "root_path is required")
	}
	if params.Pattern == "" {
		return protocol.NewError(env.ID, protocol.ErrInvalidParams, "pattern is required")
	}

	maxResults := params.MaxResults
	matches := []string{}

	walkErr := filepath.Walk(params.RootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip unreadable paths
		}
		if info.IsDir() {
			return nil
		}
		matched, _ := filepath.Match(params.Pattern, info.Name())
		if matched {
			matches = append(matches, path)
			if maxResults > 0 && len(matches) >= maxResults {
				return filepath.SkipDir
			}
		}
		return nil
	})

	if walkErr != nil && len(matches) == 0 {
		return protocol.NewError(env.ID, protocol.ErrInternal, walkErr.Error())
	}

	return protocol.NewResult(env.ID, protocol.TypeFileSearchResult,
		protocol.FileSearchResult{
			RootPath: params.RootPath,
			Pattern:  params.Pattern,
			Matches:  matches,
			Count:    len(matches),
		})
}

// ---------------------------------------------------------------------------
// System Info — fully implemented (Go stdlib)
// ---------------------------------------------------------------------------

func (a *Agent) handleSysInfo(env protocol.Envelope) protocol.Envelope {
	hostname, _ := os.Hostname()

	result := protocol.SysInfoResult{
		Hostname:      hostname,
		OS:            runtime.GOOS,
		Arch:          runtime.GOARCH,
		NumCPU:        runtime.NumCPU(),
		GoVersion:     runtime.Version(),
		Memory:        getSysMemory(),
		Disk:          getSysDisks(),
		Network:       getSysNetwork(),
		UptimeSeconds:  getUptimeSeconds(),
	}

	return protocol.NewResult(env.ID, protocol.TypeSysInfoResult, result)
}

// getSysMemory returns system memory info. On Linux reads /proc/meminfo.
func getSysMemory() protocol.SysMemInfo {
	var mem protocol.SysMemInfo

	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/meminfo")
		if err == nil {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				var key string
				var val uint64
				_, err := fmt.Sscanf(line, "%s %d", &key, &val)
				if err != nil {
					continue
				}
				switch key {
				case "MemTotal:":
					mem.TotalBytes = val * 1024 // /proc/meminfo is in kB
				case "MemFree:":
					mem.FreeBytes = val * 1024
				case "MemAvailable:":
					// Use MemAvailable if present (more accurate)
					mem.FreeBytes = val * 1024
				}
			}
			mem.UsedBytes = mem.TotalBytes - mem.FreeBytes
		}
	case "darwin":
		// On macOS, we could use syscall but that's more complex.
		// For now, leave as zeros (agent runs on Linux/Windows primarily).
	}

	return mem
}

// getSysDisks returns disk usage info for mounted filesystems.
func getSysDisks() []protocol.SysDiskInfo {
	var disks []protocol.SysDiskInfo

	switch runtime.GOOS {
	case "linux":
		f, err := os.Open("/proc/mounts")
		if err == nil {
			defer f.Close()
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if len(fields) < 3 {
					continue
				}
				mountPoint := fields[1]
				fsType := fields[2]
				// Only check real filesystems
				if strings.HasPrefix(fsType, "ext") || fsType == "btrfs" ||
					fsType == "xfs" || fsType == "zfs" || fsType == "f2fs" ||
					fsType == "ntfs" || fsType == "vfat" || fsType == "exfat" {
					disk := getDiskUsage(mountPoint)
					if disk.TotalBytes > 0 {
						disks = append(disks, disk)
					}
				}
			}
		}
	}

	return disks
}

// getDiskUsage returns disk usage for a given mount point path.
func getDiskUsage(path string) protocol.SysDiskInfo {
	return statfsDisk(path)
}

// getSysNetwork returns network interface info.
func getSysNetwork() []protocol.SysNetInfo {
	var nets []protocol.SysNetInfo

	ifaces, err := net.Interfaces()
	if err != nil {
		return nets
	}

	for _, iface := range ifaces {
		ni := protocol.SysNetInfo{
			Name: iface.Name,
			Up:   iface.Flags&net.FlagUp != 0,
		}
		if iface.HardwareAddr != nil {
			ni.MAC = iface.HardwareAddr.String()
		}
		addrs, err := iface.Addrs()
		if err == nil {
			for _, addr := range addrs {
				ni.IPAddresses = append(ni.IPAddresses, addr.String())
			}
		}
		nets = append(nets, ni)
	}

	return nets
}