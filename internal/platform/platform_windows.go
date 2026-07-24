//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// New creates a Windows platform with the given agent name.
func New(name string) Platform {
	p := &windowsPlatform{name: name}
	return p
}

type windowsPlatform struct {
	name string
}

// Filesystem — use generic Go stdlib (works cross-platform)
func (p *windowsPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
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

func (p *windowsPlatform) FileStat(path string) (protocol.FSStatResult, error) {
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

func (p *windowsPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	if offset > 0 && offset < len(raw) {
		raw = raw[offset:]
	} else if offset >= len(raw) {
		raw = nil
	}
	if limit > 0 && limit < len(raw) {
		raw = raw[:limit]
	}
	size := int64(len(raw))
	return protocol.FSReadResult{Data: base64Encode(raw), Size: size, Encoding: "base64"}, nil
}

func (p *windowsPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
	dir := path
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '\\' || dir[i] == '/' {
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

func (p *windowsPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	if err := os.Remove(path); err != nil {
		return protocol.FSDeleteResult{Deleted: false, Path: path}, err
	}
	return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
}

func (p *windowsPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	if err := os.Rename(from, to); err != nil {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, err
	}
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (p *windowsPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return protocol.FSMkdirResult{Created: false, Path: path}, err
	}
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

// Shell — Windows cmd.exe
func (p *windowsPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	cmd := exec.Command("cmd", "/c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{}

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
			cmd.Process.Kill()
			<-done
		}
	} else {
		execErr = <-done
	}

	exitCode := 0
	if execErr != nil {
		if exitErr, ok := execErr.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	_ = start
	return protocol.ExecResult{
		Stdout:   string(output),
		Stderr:   "",
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

// Display and input features are not compiled into the Windows binary
// to avoid AV heuristic flags. The agent functions as a remote terminal
// and file manager. These return "not available" if called.

func (p *windowsPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	// Capture screen using PowerShell + System.Drawing.
	// This encodes the captured screen as JPEG and outputs base64 to stdout.
	if quality <= 0 {
		quality = 80
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Drawing,System.Windows.Forms
$bounds = [System.Windows.Forms.SystemInformation]::VirtualScreen
$bmp = New-Object System.Drawing.Bitmap($bounds.Width, $bounds.Height)
$graphics = [System.Drawing.Graphics]::FromImage($bmp)
$graphics.CopyFromScreen($bounds.X, $bounds.Y, 0, 0, $bmp.Size)
$ms = New-Object System.IO.MemoryStream
$encoder = [System.Drawing.Imaging.ImageCodecInfo]::GetImageEncoders() | Where-Object { $_.MimeType -eq 'image/jpeg' }
$params = New-Object System.Drawing.Imaging.EncoderParameters(1)
$params.Param[0] = New-Object System.Drawing.Imaging.EncoderParameter([System.Drawing.Imaging.Encoder]::Quality, [long]%d)
$bmp.Save($ms, $encoder, $params)
[Convert]::ToBase64String($ms.ToArray())
$graphics.Dispose()
$bmp.Dispose()
$ms.Dispose()`, quality)

	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	out, err := cmd.Output()
	if err != nil {
		return protocol.CaptureResult{}, fmt.Errorf("screen capture failed: %w", err)
	}
	// Trim trailing whitespace/newlines from PowerShell output
	data := strings.TrimSpace(string(out))
	bounds := getWindowsScreenBounds()
	return protocol.CaptureResult{
		Format:    "jpeg",
		Width:     bounds.Width,
		Height:    bounds.Height,
		Data:      data,
		SizeBytes: int64(len(data)),
	}, nil
}

// screenBounds holds the virtual screen dimensions.
type screenBounds struct {
	Width  int
	Height int
}

// getWindowsScreenBounds returns the virtual screen dimensions via PowerShell.
// Falls back to 1920x1080 if the query fails.
func getWindowsScreenBounds() screenBounds {
	script := `Add-Type -AssemblyName System.Windows.Forms
$b = [System.Windows.Forms.SystemInformation]::VirtualScreen
"$($b.Width)x$($b.Height)"`
	out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return screenBounds{Width: 1920, Height: 1080}
	}
	var w, h int
	fmt.Sscanf(strings.TrimSpace(string(out)), "%dx%d", &w, &h)
	if w <= 0 || h <= 0 {
		return screenBounds{Width: 1920, Height: 1080}
	}
	return screenBounds{Width: w, Height: h}
}

func (p *windowsPlatform) ScreenInfo() protocol.ScreenInfo {
	return protocol.ScreenInfo{Displays: []protocol.DisplayInfo{}}
}

func (p *windowsPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	// Screen streaming on Windows uses PowerShell + System.Drawing to capture
	// frames. The actual frame capture loop runs in the agent's streamFrames
	// goroutine, which calls CaptureDisplay repeatedly. We validate that
	// PowerShell is available and return a stream ID.
	if _, err := exec.LookPath("powershell"); err != nil {
		return protocol.ScreenStreamStartResult{}, fmt.Errorf("streaming requires powershell (not found)")
	}
	if fps <= 0 {
		fps = 10
	}
	streamID := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	return protocol.ScreenStreamStartResult{
		StreamID: streamID,
	}, nil
}

func (p *windowsPlatform) ScreenStreamStop(streamID string) error {
	// Frame capture is stopped by the agent's stream manager
	return nil
}

func (p *windowsPlatform) Click(x int, y int, button string) error {
	if button == "" {
		button = "left"
	}
	// Use Win32 API via PowerShell Add-Type for mouse events
	script := fmt.Sprintf(`Add-Type @"
using System;
using System.Runtime.InteropServices;
public class MouseInput {
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern void mouse_event(uint dwFlags, uint dx, uint dy, uint dwData, IntPtr dwExtraInfo);
    public const uint MOVE = 0x0001;
    public const uint LEFTDOWN = 0x0002;
    public const uint LEFTUP = 0x0004;
    public const uint RIGHTDOWN = 0x0008;
    public const uint RIGHTUP = 0x0010;
    public const uint MIDDLEDOWN = 0x0020;
    public const uint MIDDLEUP = 0x0040;
    public static void Click(int x, int y, string button) {
        SetCursorPos(x, y);
        System.Threading.Thread.Sleep(20);
        if (button == "right") {
            mouse_event(RIGHTDOWN | RIGHTUP, 0, 0, 0, IntPtr.Zero);
        } else if (button == "middle") {
            mouse_event(MIDDLEDOWN | MIDDLEUP, 0, 0, 0, IntPtr.Zero);
        } else {
            mouse_event(LEFTDOWN | LEFTUP, 0, 0, 0, IntPtr.Zero);
        }
    }
}
"@
[MouseInput]::Click(%d, %d, "%s")`, x, y, button)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}
func (p *windowsPlatform) TypeText(text string) error {
	// Send text via SendKeys, escaping special characters
	escaped := strings.ReplaceAll(text, "{", "{{")
	escaped = strings.ReplaceAll(escaped, "}", "}}")
	escaped = strings.ReplaceAll(escaped, "+", "{+}")
	escaped = strings.ReplaceAll(escaped, "^", "{^}")
	escaped = strings.ReplaceAll(escaped, "%", "{%%}")
	escaped = strings.ReplaceAll(escaped, "~", "{~}")
	escaped = strings.ReplaceAll(escaped, "(", "{(}")
	escaped = strings.ReplaceAll(escaped, ")", "{)}")
	escaped = strings.ReplaceAll(escaped, "[", "{[}")
	escaped = strings.ReplaceAll(escaped, "]", "{]}")
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait("%s")`, escaped)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}
func (p *windowsPlatform) KeyPress(key string) error {
	// Map common key names to SendWait codes
	keyMap := map[string]string{
		"Enter": "{ENTER}", "Return": "{ENTER}",
		"Tab": "{TAB}", "Escape": "{ESC}", "Esc": "{ESC}",
		"Backspace": "{BACKSPACE}", "Delete": "{DELETE}",
		"Space": " ", "Up": "{UP}", "Down": "{DOWN}",
		"Left": "{LEFT}", "Right": "{RIGHT}",
		"Home": "{HOME}", "End": "{END}",
		"PageUp": "{PGUP}", "PageDown": "{PGDN}",
		"F1": "{F1}", "F2": "{F2}", "F3": "{F3}", "F4": "{F4}",
		"F5": "{F5}", "F6": "{F6}", "F7": "{F7}", "F8": "{F8}",
		"F9": "{F9}", "F10": "{F10}", "F11": "{F11}", "F12": "{F12}",
	}
	code, ok := keyMap[key]
	if !ok {
		// Single character — send as-is
		if len(key) == 1 {
			code = key
		} else {
			code = "{" + key + "}"
		}
	}
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait("%s")`, code)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}
func (p *windowsPlatform) KeyCombo(keys []string) error {
	// Build SendWait combo string: Ctrl+C → ^c
	if len(keys) == 0 {
		return fmt.Errorf("no keys specified")
	}
	// Map modifier names to SendKeys prefix
	modifierMap := map[string]string{
		"Ctrl": "^", "Control": "^",
		"Alt": "%", "Shift": "+",
		"Win": "{WIN}",
	}
	prefix := ""
	lastKey := ""
	for _, k := range keys {
		if m, ok := modifierMap[k]; ok {
			prefix += m
		} else {
			lastKey = k
		}
	}
	if lastKey == "" {
		return fmt.Errorf("no non-modifier key in combo")
	}
	// Map last key to code
	keyMap := map[string]string{
		"Enter": "{ENTER}", "Tab": "{TAB}", "Escape": "{ESC}",
		"Space": " ", "Up": "{UP}", "Down": "{DOWN}",
		"Left": "{LEFT}", "Right": "{RIGHT}",
		"F1": "{F1}", "F2": "{F2}", "F3": "{F3}", "F4": "{F4}",
		"F5": "{F5}", "F6": "{F6}", "F7": "{F7}", "F8": "{F8}",
		"F9": "{F9}", "F10": "{F10}", "F11": "{F11}", "F12": "{F12}",
	}
	code, ok := keyMap[lastKey]
	if !ok {
		if len(lastKey) == 1 {
			code = lastKey
		} else {
			code = "{" + lastKey + "}"
		}
	}
	combo := prefix + code
	script := fmt.Sprintf(`Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.SendKeys]::SendWait("%s")`, combo)
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	return cmd.Run()
}

// System
func (p *windowsPlatform) Health(mode string) protocol.HealthResult {
	hostname, _ := os.Hostname()
	return protocol.HealthResult{
		AgentVersion: "0.1.0",
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Mode:         mode,
	}
}

// ProcessList, ProcessKill use tasklist/taskkill on Windows.
// Previously stubbed for AV heuristic avoidance, now implemented via exec
// to support remote process management.
func (p *windowsPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	out, err := exec.Command("tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return nil, fmt.Errorf("tasklist failed: %v", err)
	}

	var procs []protocol.ProcessInfo
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Simple CSV parse: "Name","PID",...
		fields := strings.Split(line, ",")
		if len(fields) < 2 {
			continue
		}
		pid := 0
		name := strings.Trim(fields[0], "\"")
		fmt.Sscanf(strings.Trim(fields[1], "\""), "%d", &pid)
		procs = append(procs, protocol.ProcessInfo{
			PID:  pid,
			Name: name,
		})
	}
	return procs, nil
}

func (p *windowsPlatform) ProcessKill(pid int, signal int) error {
	return exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/F").Run()
}

func (p *windowsPlatform) OpenURL(url string) error {
	return fmt.Errorf("url opening not available")
}

func (p *windowsPlatform) Notify(title string, body string, icon string) error {
	return fmt.Errorf("notifications not available")
}

func (p *windowsPlatform) ClipboardGet() (string, error) {
	return "", fmt.Errorf("clipboard not available")
}

func (p *windowsPlatform) ClipboardSet(text string) error {
	return fmt.Errorf("clipboard not available")
}


