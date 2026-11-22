//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// New creates a macOS platform with the given agent name.
func New(name string) Platform {
	p := &darwinPlatform{name: name}
	return p
}

type darwinPlatform struct {
	name string
}

// --- Filesystem --- identical to linuxPlatform (Go stdlib os package, cross-platform)

func (p *darwinPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]protocol.FSEntry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, protocol.FSEntry{
			Name:    e.Name(),
			Size:    info.Size(),
			Mode:    info.Mode().String(),
			ModTime: info.ModTime().Unix(),
			IsDir:   e.IsDir(),
		})
	}
	return result, nil
}

func (p *darwinPlatform) FileStat(path string) (protocol.FSStatResult, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return protocol.FSStatResult{Exists: false}, nil
		}
		return protocol.FSStatResult{}, err
	}
	return protocol.FSStatResult{
		Size:    info.Size(),
		Mode:    info.Mode().String(),
		ModTime: info.ModTime().Unix(),
		IsDir:   info.IsDir(),
		Exists:  true,
	}, nil
}

func (p *darwinPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	total := int64(len(raw))
	if offset < 0 {
		offset = 0
	}
	if offset > 0 && offset < len(raw) {
		raw = raw[offset:]
	} else if offset >= len(raw) {
		raw = nil
	}
	if limit > 0 && limit < len(raw) {
		raw = raw[:limit]
	}
	return fsReadResult(raw, total, offset), nil
}

func (p *darwinPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
	dir := path
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' {
			dir = dir[:i]
			break
		}
	}
	if dir != "" {
		os.MkdirAll(dir, 0755)
	}
	perm := os.FileMode(0644)
	if mode != "" {
		var m uint32
		if _, err := fmt.Sscanf(mode, "%o", &m); err == nil {
			perm = os.FileMode(m)
		}
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return protocol.FSWriteResult{}, err
	}
	return protocol.FSWriteResult{Written: len(data), Path: path}, nil
}

func (p *darwinPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	if err := os.Remove(path); err != nil {
		return protocol.FSDeleteResult{Deleted: false, Path: path}, err
	}
	return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
}

func (p *darwinPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	if err := os.Rename(from, to); err != nil {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, err
	}
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (p *darwinPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return protocol.FSMkdirResult{Created: false, Path: path}, err
	}
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

// --- Shell --- macOS default shell via bash

func (p *darwinPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	cmd := exec.Command("bash", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}

	start := time.Now()
	done := make(chan error, 1)
	var output []byte
	go func() {
		out, err := cmd.CombinedOutput()
		output = out
		done <- err
	}()

	timedOut := false
	var execErr error
	if timeout > 0 {
		timer := time.NewTimer(time.Duration(timeout) * time.Second)
		defer timer.Stop()
		select {
		case execErr = <-done:
		case <-timer.C:
			timedOut = true
			if cmd.Process != nil {
				cmd.Process.Kill()
			}
			<-done
		}
	} else {
		execErr = <-done
	}

	exitCode := 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = ws.ExitStatus()
			} else {
				exitCode = -1
			}
		} else {
			exitCode = -1
		}
	}
	_ = start
	stdout := string(output)
	stderr := ""
	return protocol.ExecResult{
		Stdout:   stdout,
		Stderr:   stderr,
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

// --- Screen ---

// CaptureDisplay uses the built-in screencapture tool.
// `screencapture -x -t png -` writes a PNG to stdout (-x suppresses the
// shutter sound; -t png forces PNG format; - writes to stdout).
func (p *darwinPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	// `screencapture -x -t png -` writes a PNG to stdout:
	//   -x suppresses the shutter sound
	//   -t png forces PNG format
	//   - writes to stdout
	args := []string{"-x", "-t", "png", "-"}
	if display > 0 {
		// -D<id> selects a specific display by its display ID.
		args = []string{"-x", "-D", strconv.Itoa(display), "-t", "png", "-"}
	}
	cmd := exec.Command("screencapture", args...)
	out, err := cmd.Output()
	if err != nil {
		return protocol.CaptureResult{}, fmt.Errorf("screencapture failed: %w", err)
	}
	return protocol.CaptureResult{
		Format:    "png",
		Width:     0,
		Height:    0,
		Data:      base64Encode(out),
		SizeBytes: int64(len(out)),
	}, nil
}

// ScreenInfo parses display resolutions from system_profiler.
func (p *darwinPlatform) ScreenInfo() protocol.ScreenInfo {
	cmd := exec.Command("system_profiler", "SPDisplaysDataType")
	out, err := cmd.Output()
	if err != nil {
		return protocol.ScreenInfo{Displays: []protocol.DisplayInfo{{ID: 0, Width: 0, Height: 0, Scale: 1.0, IsPrimary: true}}}
	}
	var displays []protocol.DisplayInfo
	for _, line := range splitLines(string(out)) {
		if strings.Contains(line, "Resolution:") {
			fields := strings.Fields(line)
			// Expected: "Resolution: 1920 x 1080" or "Resolution: 1920 x 1080 (Retina)"
			if len(fields) >= 4 {
				w, _ := strconv.Atoi(fields[1])
				h, _ := strconv.Atoi(fields[3])
				if w > 0 && h > 0 {
					displays = append(displays, protocol.DisplayInfo{
						ID:        len(displays),
						Width:     w,
						Height:    h,
						Scale:     1.0,
						IsPrimary: len(displays) == 0,
					})
				}
			}
		}
	}
	if len(displays) == 0 {
		displays = []protocol.DisplayInfo{{ID: 0, Width: 0, Height: 0, Scale: 1.0, IsPrimary: true}}
	}
	return protocol.ScreenInfo{Displays: displays}
}

// ScreenStreamStart is deferred to Phase F.
func (p *darwinPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	return protocol.ScreenStreamStartResult{}, fmt.Errorf("streaming not implemented")
}

// ScreenStreamStop is deferred to Phase F.
func (p *darwinPlatform) ScreenStreamStop(streamID string) error {
	return fmt.Errorf("streaming not implemented")
}

// --- Input --- via osascript (AppleScript / System Events)

// Click moves the cursor and performs a click via AppleScript.
func (p *darwinPlatform) Click(x int, y int, button string) error {
	// Move cursor to {x, y}, then click. button is currently ignored (always left)
	// since System Events' `click at` is a left click.
	_ = button
	script := fmt.Sprintf(`tell application "System Events" to click at {%d, %d}`, x, y)
	return exec.Command("osascript", "-e", script).Run()
}

// TypeText types a string via System Events keystroke.
func (p *darwinPlatform) TypeText(text string) error {
	escaped := strings.ReplaceAll(text, `"`, `\"`)
	escaped = strings.ReplaceAll(escaped, `\`, `\\`)
	script := fmt.Sprintf(`tell application "System Events" to keystroke "%s"`, escaped)
	return exec.Command("osascript", "-e", script).Run()
}

// keyPressMap maps common key names to macOS key codes.
var keyPressMap = map[string]int{
	"return":    36,
	"enter":     36,
	"tab":       48,
	"space":     49,
	"delete":    51,
	"backspace": 51,
	"escape":    53,
	"esc":       53,
	"left":      123,
	"right":     124,
	"down":      125,
	"up":        126,
	"f1":  122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7":  98,  "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
	"home":      115,
	"end":       119,
	"pageup":    116,
	"pagedown":  121,
}

// KeyPress sends a single key press via System Events key code.
func (p *darwinPlatform) KeyPress(key string) error {
	code, ok := keyPressMap[strings.ToLower(key)]
	if !ok {
		// Fallback: try to type the literal character
		return p.TypeText(key)
	}
	script := fmt.Sprintf(`tell application "System Events" to key code %d`, code)
	return exec.Command("osascript", "-e", script).Run()
}

// KeyCombo sends a key combination with modifiers via System Events.
func (p *darwinPlatform) KeyCombo(keys []string) error {
	var modifiers []string
	var mainKey string
	for _, k := range keys {
		switch strings.ToLower(k) {
		case "ctrl", "control":
			modifiers = append(modifiers, "control down")
		case "alt", "option":
			modifiers = append(modifiers, "option down")
		case "shift":
			modifiers = append(modifiers, "shift down")
		case "cmd", "command", "win", "super", "meta":
			modifiers = append(modifiers, "command down")
		default:
			mainKey = k
		}
	}
	if mainKey == "" {
		return fmt.Errorf("key combo requires a non-modifier key")
	}
	modifierStr := strings.Join(modifiers, ", ")
	script := fmt.Sprintf(`tell application "System Events" to keystroke "%s" using {%s}`, mainKey, modifierStr)
	return exec.Command("osascript", "-e", script).Run()
}

// --- System ---

// Health returns agent health info.
func (p *darwinPlatform) Health(mode string) protocol.HealthResult {
	hostname, _ := os.Hostname()
	return protocol.HealthResult{
		AgentVersion: "0.1.0",
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Mode:         mode,
	}
}

// ProcessList lists processes via the BSD ps command.
// `ps -eo pid,comm,%cpu,%mem` with --no-headers is the BSD/macOS form.
func (p *darwinPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	// macOS ps uses -o with headers by default; suppress with =header (BSD) or
	// the common form ps -axo pid,comm,pcpu,rss (rss is in KB → /1024 for MB).
	cmd := exec.Command("ps", "-axo", "pid,comm,pcpu,rss")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(out))
	result := make([]protocol.ProcessInfo, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		// comm may contain spaces; join from field[1] but take the last two as pcpu/rss.
		// Re-split more carefully: pid | rest(comm) | pcpu | rss
		// ps -axo pid,comm,pcpu,rss → pid and rss are numeric, pcpu is float.
		// Parse from the end.
		rss, err2 := strconv.ParseFloat(fields[len(fields)-1], 64)
		if err2 != nil {
			continue
		}
		pcpu, err3 := strconv.ParseFloat(fields[len(fields)-2], 64)
		if err3 != nil {
			continue
		}
		name := strings.Join(fields[1:len(fields)-2], " ")
		result = append(result, protocol.ProcessInfo{
			PID:        pid,
			Name:       name,
			CPUPercent: pcpu,
			MemoryMB:   rss / 1024,
		})
	}
	return result, nil
}

// ProcessKill sends a signal to a process via syscall.Kill.
func (p *darwinPlatform) ProcessKill(pid int, signal int) error {
	if signal == 0 {
		signal = 15 // SIGTERM
	}
	return syscall.Kill(pid, syscall.Signal(signal))
}

// OpenURL opens a URL in the default browser via the built-in `open` command.
func (p *darwinPlatform) OpenURL(url string) error {
	return exec.Command("open", url).Start()
}

// Notify shows a macOS notification via osascript display notification.
func (p *darwinPlatform) Notify(title string, body string, icon string) error {
	_ = icon // icon is not used by display notification
	escapedBody := strings.ReplaceAll(body, `"`, `\"`)
	escapedTitle := strings.ReplaceAll(title, `"`, `\"`)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, escapedBody, escapedTitle)
	return exec.Command("osascript", "-e", script).Run()
}

// ClipboardGet reads the clipboard via the built-in pbpaste.
func (p *darwinPlatform) ClipboardGet() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// ClipboardSet writes to the clipboard via the built-in pbcopy (stdin pipe).
func (p *darwinPlatform) ClipboardSet(text string) error {
	cmd := exec.Command("pbcopy")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := stdin.Write([]byte(text)); err != nil {
		return err
	}
	stdin.Close()
	return cmd.Wait()
}

// splitLines splits a string on newlines, dropping empty lines.
func splitLines(s string) []string {
	var result []string
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			if len(current) > 0 {
				result = append(result, current)
				current = ""
			}
		} else {
			current += string(ch)
		}
	}
	if len(current) > 0 {
		result = append(result, current)
	}
	return result
}