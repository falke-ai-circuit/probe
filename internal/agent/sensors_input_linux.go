//go:build linux

package agent

import (
	"encoding/binary"
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// readActiveWindowImpl returns the title of the currently-focused window.
// Uses xdotool (most common X11 tool). Falls back to xprop + _NET_WM_NAME.
// On Wayland, requires xdotool running under XWayland.
func readActiveWindowImpl() (string, error) {
	if out, err := exec.Command("xdotool", "getactivewindow", "getwindowname").Output(); err == nil {
		title := strings.TrimSpace(string(out))
		if title != "" {
			return title, nil
		}
	}
	// xprop fallback
	if out, err := exec.Command("xprop", "-root", "_NET_ACTIVE_WINDOW", "-notype").Output(); err == nil {
		parts := strings.Fields(string(out))
		for i, p := range parts {
			if i+1 < len(parts) && p == "id" && strings.HasPrefix(parts[i+1], "0x") {
				winID := parts[i+1]
				if nameOut, err := exec.Command("xprop", "-id", winID, "_NET_WM_NAME", "-notype").Output(); err == nil {
					if i := strings.Index(string(nameOut), "= "); i >= 0 {
						return strings.TrimSpace(string(nameOut)[i+2:]), nil
					}
				}
			}
		}
	}
	return "", fmt.Errorf("cannot read active window: xdotool/xprop not available")
}

// readClipboardImpl reads the X11 clipboard via xclip or xsel.
func readClipboardImpl() (string, error) {
	for _, cmd := range [][]string{
		{"xclip", "-selection", "clipboard", "-o"},
		{"xsel", "--clipboard", "--output"},
		{"wl-paste"},
	} {
		out, err := exec.Command(cmd[0], cmd[1:]...).Output()
		if err == nil {
			return string(out), nil
		}
	}
	return "", fmt.Errorf("no clipboard tool available (need xclip, xsel, or wl-paste)")
}

// readBrowserHistoryImpl opens Chrome/Chromium/Edge/Firefox SQLite history
// databases and returns the most recent limit rows. Uses the sqlite3 CLI
// (no new Go deps). If sqlite3 is not installed, the sensor returns an
// error explaining how to install it.
func readBrowserHistoryImpl(limit int) (any, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 CLI not found; install sqlite3 package to enable browser_history sensor")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	candidates := []struct {
		name string
		path string
	}{
		{"chrome", filepath.Join(home, ".config", "google-chrome", "Default", "History")},
		{"chromium", filepath.Join(home, ".config", "chromium", "Default", "History")},
		{"edge", filepath.Join(home, ".config", "microsoft-edge", "Default", "History")},
		{"brave", filepath.Join(home, ".config", "BraveSoftware", "Brave-Browser", "Default", "History")},
		{"firefox", filepath.Join(home, ".mozilla", "firefox", "")},
	}

	for _, c := range candidates {
		if strings.HasSuffix(c.path, "firefox") && c.path != "" {
			entries, err := readFirefoxHistory(c.path, limit)
			if err == nil {
				return map[string]any{"browser": "firefox", "entries": entries}, nil
			}
			continue
		}
		entries, err := readChromeHistory(c.path, limit)
		if err == nil {
			return map[string]any{"browser": c.name, "entries": entries}, nil
		}
	}
	return nil, fmt.Errorf("no browser history found")
}

func copyFileLocked(src string) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "browser_history_*.db")
	if err != nil {
		return "", err
	}
	defer tmp.Close()
	if _, err := tmp.Write(data); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func readChromeHistory(path string, limit int) ([]map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	tmp, err := copyFileLocked(path)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	out, err := exec.Command("sqlite3", "-readonly", "-header", "-csv",
		tmp, fmt.Sprintf("SELECT url, title, visit_count, last_visit_time FROM urls ORDER BY last_visit_time DESC LIMIT %d", limit)).Output()
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("no rows or parse error")
	}
	header := rows[0]
	out2 := make([]map[string]any, 0, len(rows)-1)
	for _, r := range rows[1:] {
		if len(r) != len(header) {
			continue
		}
		entry := make(map[string]any, len(header))
		for i, h := range header {
			entry[h] = r[i]
		}
		if ts, ok := entry["last_visit_time"].(string); ok {
			var chromeTime int64
			fmt.Sscan(ts, &chromeTime)
			if chromeTime > 0 {
				entry["last_visit"] = time.Unix(chromeTime/1000000-11644473600, 0).UTC().Format(time.RFC3339)
			}
		}
		out2 = append(out2, entry)
	}
	return out2, nil
}

func readFirefoxHistory(profilesDir string, limit int) ([]map[string]any, error) {
	iniPath := filepath.Join(profilesDir, "profiles.ini")
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return nil, err
	}
	var profilePath string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "Path=") {
			profilePath = filepath.Join(profilesDir, strings.TrimPrefix(line, "Path="))
			break
		}
	}
	if profilePath == "" {
		return nil, fmt.Errorf("no firefox profile found")
	}
	placesPath := filepath.Join(profilePath, "places.sqlite")
	if _, err := os.Stat(placesPath); err != nil {
		return nil, err
	}
	tmp, err := copyFileLocked(placesPath)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	out, err := exec.Command("sqlite3", "-readonly", "-header", "-csv",
		tmp, fmt.Sprintf("SELECT url, title, visit_count, last_visit_date FROM moz_places ORDER BY last_visit_date DESC LIMIT %d", limit)).Output()
	if err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(strings.NewReader(string(out))).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("no rows or parse error")
	}
	header := rows[0]
	out2 := make([]map[string]any, 0, len(rows)-1)
	for _, r := range rows[1:] {
		if len(r) != len(header) {
			continue
		}
		entry := make(map[string]any, len(header))
		for i, h := range header {
			entry[h] = r[i]
		}
		if ts, ok := entry["last_visit_date"].(string); ok {
			var unixTime int64
			fmt.Sscan(ts, &unixTime)
			if unixTime > 0 {
				entry["last_visit"] = time.Unix(unixTime/1e6, 0).UTC().Format(time.RFC3339)
			}
		}
		out2 = append(out2, entry)
	}
	return out2, nil
}

// readKeypressWindowImpl returns the rolling keypress buffer.
func readKeypressWindowImpl(seconds int, maxKeys int) (any, error) {
	// Lazy-start the capture goroutine on first read
	ensureKeypressCapture()
	events := snapshotKeypressWindow(seconds, maxKeys)
	return map[string]any{
		"window_seconds": seconds,
		"event_count":    len(events),
		"events":         events,
	}, nil
}



func isKeyboardDevice(dev string) bool {
	// EVIOCGBIT(0, bits) — check for EV_KEY (bit 1)
	fd, err := os.Open(dev)
	if err != nil {
		return false
	}
	defer fd.Close()
	var bits [4]byte
	_, _, errno := syscall.Syscall6(
		syscall.SYS_IOCTL,
		fd.Fd(),
		uintptr(0x20), // EVIOCGBIT
		uintptr(0),
		uintptr(unsafe.Pointer(&bits)),
		0, 0,
	)
	if errno != 0 {
		return false
	}
	// bit 1 = EV_KEY
	return bits[0]&0x02 != 0
}

func captureKeypressDevice(dev string) {
	fd, err := os.OpenFile(dev, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return
	}
	defer fd.Close()
	// Each input_event is 24 bytes on 64-bit Linux:
	// time (16 bytes: tv_sec, tv_usec) + type (2) + code (2) + value (4)
	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(int(fd.Fd()), buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			return
		}
		if n < 24 {
			continue
		}
		processKeypressBytes(buf[:n])
	}
}

func processKeypressBytes(buf []byte) {
	offset := 0
	for offset+24 <= len(buf) {
		sec := int64(binary.LittleEndian.Uint32(buf[offset:]))
		usec := int64(binary.LittleEndian.Uint32(buf[offset+4:]))
		evType := binary.LittleEndian.Uint16(buf[offset+16:])
		code := binary.LittleEndian.Uint16(buf[offset+18:])
		value := int32(binary.LittleEndian.Uint32(buf[offset+20:]))
		offset += 24

		// Only key events (type 1) with value 1 (key down)
		if evType != 1 || value != 1 {
			continue
		}
		ts := time.Unix(sec, usec*1000)
		appendKeypressEvent(keypressEvent{
			Timestamp: ts,
			Code:      code,
			Name:      linuxKeyName(code),
		})
	}
}

// linuxKeyName resolves Linux keycodes (set 1) to human names. Only the
// most common keys are mapped; the code is always returned even when
// Name is empty.
var linuxKeyNames = map[uint16]string{
	1: "ESC", 2: "1", 3: "2", 4: "3", 5: "4", 6: "5", 7: "6", 8: "7", 9: "8",
	10: "9", 11: "0", 14: "BACKSPACE", 15: "TAB", 16: "Q", 17: "W", 18: "E",
	19: "R", 20: "T", 21: "Y", 22: "U", 23: "I", 24: "O", 25: "P", 28: "ENTER",
	29: "LCTRL", 30: "A", 31: "S", 32: "D", 33: "F", 34: "G", 35: "H", 36: "J",
	37: "K", 38: "L", 42: "LSHIFT", 44: "Z", 45: "X", 46: "C", 47: "V", 48: "B",
	49: "N", 50: "M", 54: "RSHIFT", 56: "LALT", 57: "SPACE", 58: "CAPSLOCK",
	59: "F1", 60: "F2", 61: "F3", 62: "F4", 63: "F5", 64: "F6", 65: "F7",
	66: "F8", 67: "F9", 68: "F10", 87: "F11", 88: "F12", 96: "ENTER",
	97: "RCTRL", 100: "RALT", 102: "HOME", 103: "UP", 104: "PAGEUP",
	105: "LEFT", 106: "RIGHT", 108: "END", 109: "DOWN", 110: "PAGEDOWN",
	111: "INSERT", 119: "DELETE",
}

func linuxKeyName(code uint16) string {
	if n, ok := linuxKeyNames[code]; ok {
		return n
	}
	return fmt.Sprintf("KEY_%d", code)
}

// enumerateKeypressDevices for Linux: scan /dev/input/eventN for
// devices that report EV_KEY (keyboard capability).
func enumerateKeypressDevices() []string {
	devs := make([]string, 0, 4)
	for n := 0; n < 20; n++ {
		dev := fmt.Sprintf("/dev/input/event%d", n)
		if isKeyboardDevice(dev) {
			devs = append(devs, dev)
		}
	}
	return devs
}
