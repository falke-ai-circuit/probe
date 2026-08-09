//go:build darwin

package agent

import (
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// readActiveWindowImpl uses NSWorkspace via osascript. The agent process
// only needs the user's display server access (default for any process
// running in the user's session).
func readActiveWindowImpl() (string, error) {
	out, err := exec.Command("osascript", "-e",
		`tell application "System Events" to get name of first application process whose frontmost is true`).Output()
	if err != nil {
		return "", fmt.Errorf("osascript failed (System Events access required): %v", err)
	}
	appName := strings.TrimSpace(string(out))
	// Then get the window title of the frontmost app
	out2, err := exec.Command("osascript", "-e", fmt.Sprintf(
		`tell application "System Events" to tell process "%s" to get name of front window`, appName)).Output()
	if err != nil {
		// Some apps don't have a window title; return just the app name
		return appName, nil
	}
	title := strings.TrimSpace(string(out2))
	if title == "" {
		return appName, nil
	}
	return fmt.Sprintf("%s - %s", appName, title), nil
}

// readClipboardImpl uses pbpaste (built-in on macOS).
func readClipboardImpl() (string, error) {
	out, err := exec.Command("pbpaste", "-pre", "general").Output()
	if err == nil {
		return string(out), nil
	}
	// Fallback: osascript
	out, err = exec.Command("osascript", "-e", "the clipboard").Output()
	if err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	return "", fmt.Errorf("clipboard read failed (try pbpaste or osascript)")
}

// readBrowserHistoryImpl uses sqlite3 (same as Linux/Windows) or falls
// back to the built-in `defaults` for some Safari cases.
func readBrowserHistoryImpl(limit int) (any, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return nil, fmt.Errorf("sqlite3 CLI not found; install via 'brew install sqlite3' to enable browser_history sensor")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}

	// Safari uses ~/Library/Safari/History.db
	// Chrome/Edge/Brave use ~/Library/Application Support/{Vendor}/...
	candidates := []struct {
		name string
		path string
	}{
		{"safari", filepath.Join(home, "Library", "Safari", "History.db")},
		{"chrome", filepath.Join(home, "Library", "Application Support", "Google", "Chrome", "Default", "History")},
		{"edge", filepath.Join(home, "Library", "Application Support", "Microsoft Edge", "Default", "History")},
		{"brave", filepath.Join(home, "Library", "Application Support", "BraveSoftware", "Brave-Browser", "Default", "History")},
		{"firefox", filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles", "")},
	}

	for _, c := range candidates {
		if strings.HasSuffix(c.path, "firefox") && c.path != "" {
			entries, err := readFirefoxHistory(c.path, limit)
			if err == nil {
				return map[string]any{"browser": "firefox", "entries": entries}, nil
			}
			continue
		}
		// Safari uses a different table layout (history_items)
		if c.name == "safari" {
			entries, err := readSafariHistory(c.path, limit)
			if err == nil {
				return map[string]any{"browser": "safari", "entries": entries}, nil
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

func readChromeHistory(path string, limit int) ([]map[string]any, error) {
	return readChromeHistoryCli(path, limit, "SELECT url, title, visit_count, last_visit_time FROM urls ORDER BY last_visit_time DESC LIMIT %d")
}

// readChromeHistoryCli is the platform-agnostic sqlite3+csv parser.
// Defined here (vs. duplicated) because darwin has its own path expansion.
// Use a shared helper if you refactor later.
func readChromeHistoryCli(path, limit int, queryTemplate string) ([]map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	tmp, err := copyFileLocked(path)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	out, err := exec.Command("sqlite3", "-readonly", "-header", "-csv",
		tmp, fmt.Sprintf(queryTemplate, limit)).Output()
	if err != nil {
		return nil, err
	}
	return parseHistoryCSV(string(out), "last_visit_time", 1000000, 11644473600)
}

func readSafariHistory(path string, limit int) ([]map[string]any, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	tmp, err := copyFileLocked(path)
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp)
	out, err := exec.Command("sqlite3", "-readonly", "-header", "-csv",
		tmp, fmt.Sprintf("SELECT url, title, visit_count, last_visit_time FROM history_items ORDER BY last_visit_time DESC LIMIT %d", limit)).Output()
	if err != nil {
		return nil, err
	}
	// Safari uses Core Data timestamp (seconds since 2001-01-01)
	return parseHistoryCSV(string(out), "last_visit_time", 1, 978307200)
}

func parseHistoryCSV(csvData, timeColumn string, divisor, epochDiff int64) ([]map[string]any, error) {
	rows, err := csv.NewReader(strings.NewReader(csvData)).ReadAll()
	if err != nil || len(rows) < 2 {
		return nil, fmt.Errorf("no rows or parse error")
	}
	header := rows[0]
	out := make([]map[string]any, 0, len(rows)-1)
	for _, r := range rows[1:] {
		if len(r) != len(header) {
			continue
		}
		entry := make(map[string]any, len(header))
		for i, h := range header {
			entry[h] = r[i]
		}
		if ts, ok := entry[timeColumn].(string); ok {
			var raw int64
			fmt.Sscan(ts, &raw)
			if raw > 0 {
				entry["last_visit"] = time.Unix(raw/divisor-epochDiff, 0).UTC().Format(time.RFC3339)
			}
		}
		out = append(out, entry)
	}
	return out, nil
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

// readKeypressWindowImpl: macOS doesn't support background keylog without
// the Accessibility permission + a system extension. We refuse explicitly
// rather than fail silently.
func readKeypressWindowImpl(seconds int, maxKeys int) (any, error) {
	return nil, fmt.Errorf("keypress_capture not supported on macOS (requires accessibility permission + system extension)")
}

// enumerateKeypressDevices for macOS: denied. The keypress_window
// sensor's readKeypressWindowImpl returns an explicit error.
func enumerateKeypressDevices() []string {
	return nil
}


// captureKeypressDevice for macOS: not implemented. The keypress_window
// sensor's readKeypressWindowImpl returns an explicit error.
func captureKeypressDevice(dev string) {
	// no-op
}
