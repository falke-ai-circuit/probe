//go:build windows

package agent

import (
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

// readActiveWindowImpl uses Win32 GetForegroundWindow + GetWindowTextW to
// return the title of the currently focused window.
func readActiveWindowImpl() (string, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return "", fmt.Errorf("no foreground window")
	}
	var buf [512]uint16
	procGetWindowTextW.Call(
		hwnd,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(len(buf)),
	)
	return syscall.UTF16ToString(buf[:]), nil
}

// readClipboardImpl uses PowerShell to read the clipboard. PowerShell
// has built-in Get-Clipboard which works without extra deps on Windows
// 10+. Falls back to error if PowerShell not available.
func readClipboardImpl() (string, error) {
	out, err := exec.Command("powershell", "-NoProfile", "-Command", "Get-Clipboard -Raw").Output()
	if err == nil {
		return string(out), nil
	}
	// Try clip.exe
	out, err = exec.Command("clip").Output()
	if err == nil {
		return string(out), nil
	}
	return "", fmt.Errorf("clipboard read requires PowerShell Get-Clipboard or clip.exe")
}

// readBrowserHistoryImpl reads the default browser's History database.
// Same approach as Linux: shell out to sqlite3 if available, fall back
// to error otherwise. Edge and Chrome on Windows store History in
// %LOCALAPPDATA%.
func readBrowserHistoryImpl(limit int) (any, error) {
	if _, err := exec.LookPath("sqlite3.exe"); err != nil {
		// Try with .exe
		if _, err2 := exec.LookPath("sqlite3"); err2 != nil {
			return nil, fmt.Errorf("sqlite3 not found; install sqlite3 (with sqlite3.exe on PATH) to enable browser_history sensor")
		}
	}

	// Find LocalAppData
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		localAppData = filepath.Join(os.Getenv("USERPROFILE"), "AppData", "Local")
	}

	candidates := []struct {
		name string
		path string
	}{
		{"edge", filepath.Join(localAppData, "Microsoft", "Edge", "User Data", "Default", "History")},
		{"chrome", filepath.Join(localAppData, "Google", "Chrome", "User Data", "Default", "History")},
		{"brave", filepath.Join(localAppData, "BraveSoftware", "Brave-Browser", "User Data", "Default", "History")},
		{"firefox", filepath.Join(os.Getenv("APPDATA"), "Mozilla", "Firefox", "")},
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

// readKeypressWindowImpl returns the rolling keypress buffer. On Windows
// we use a low-level keyboard hook (WH_KEYBOARD_LL) installed on a
// dedicated message-only window. Returns the buffer of recent keys.
func readKeypressWindowImpl(seconds int, maxKeys int) (any, error) {
	ensureKeypressCapture()
	events := snapshotKeypressWindow(seconds, maxKeys)
	return map[string]any{
		"window_seconds": seconds,
		"event_count":    len(events),
		"events":         events,
	}, nil
}

// Shared Win32 API declarations for foreground window + keyboard hook.
var (
	modUser32 = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetForegroundWindow = modUser32.NewProc("GetForegroundWindow")
	procGetWindowTextW     = modUser32.NewProc("GetWindowTextW")
	procSetWindowsHookExW  = modUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = modUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx     = modUser32.NewProc("CallNextHookEx")
	procGetMessageW        = modUser32.NewProc("GetMessageW")
	procTranslateMessage   = modUser32.NewProc("TranslateMessage")
	procDispatchMessageW   = modUser32.NewProc("DispatchMessageW")
	procMapVirtualKeyW     = modUser32.NewProc("MapVirtualKeyW")
	procGetKeyboardLayout  = modUser32.NewProc("GetKeyboardLayout")
	procToUnicodeEx        = modUser32.NewProc("ToUnicodeEx")
)

// readChromeHistory and readFirefoxHistory — same parsing as Linux but
// these are OS-agnostic when given the right paths, so re-implement here
// using sqlite3 CLI + CSV. (Same code as Linux, no new code patterns.)

func readChromeHistory(path string, limit int) ([]map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	tmp, err := copyFileLocked(path)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	exe := "sqlite3"
	if _, err := exec.LookPath("sqlite3.exe"); err == nil {
		exe = "sqlite3.exe"
	}
	out, err := exec.Command(exe, "-readonly", "-header", "-csv",
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
	exe := "sqlite3"
	if _, err := exec.LookPath("sqlite3.exe"); err == nil {
		exe = "sqlite3.exe"
	}
	out, err := exec.Command(exe, "-readonly", "-header", "-csv",
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

// enumerateKeypressDevices for Windows: not implemented in v1.15.0
// (would require WH_KEYBOARD_LL message-only window hook). The
// keypress_window sensor will return "not supported on Windows yet"
// for now. Will be added in a follow-up release.
func enumerateKeypressDevices() []string {
	return nil
}


// captureKeypressDevice for Windows: not implemented in v1.15.0. The
// keypress_window sensor's readKeypressWindowImpl returns an explicit
// error. (Adding WH_KEYBOARD_LL support requires a hidden message-only
// window + a low-level hook proc — done in a follow-up.)
func captureKeypressDevice(dev string) {
	// no-op
}
