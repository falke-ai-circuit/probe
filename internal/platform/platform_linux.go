//go:build linux

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// New creates a platform with the given agent name.
func New(name string) Platform {
	p := &linuxPlatform{name: name}
	return p
}

type linuxPlatform struct {
	name string
}

func (p *linuxPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
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

func (p *linuxPlatform) FileStat(path string) (protocol.FSStatResult, error) {
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

func (p *linuxPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
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

func (p *linuxPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
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

func (p *linuxPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	if err := os.Remove(path); err != nil {
		return protocol.FSDeleteResult{Deleted: false, Path: path}, err
	}
	return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
}

func (p *linuxPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	if err := os.Rename(from, to); err != nil {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, err
	}
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (p *linuxPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	if err := os.MkdirAll(path, 0755); err != nil {
		return protocol.FSMkdirResult{Created: false, Path: path}, err
	}
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

func (p *linuxPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	cmd := exec.Command("sh", "-c", command)
	if workDir != "" {
		cmd.Dir = workDir
	}
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
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

func (p *linuxPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	// Try import (ImageMagick)
	cmd := exec.Command("import", "-window", "root", "-")
	out, err := cmd.Output()
	if err != nil {
		// Try scrot
		cmd2 := exec.Command("scrot", "-")
		out, err = cmd2.Output()
		if err != nil {
			return protocol.CaptureResult{}, fmt.Errorf("display capture failed (import/scrot not found): %w", err)
		}
	}
	return protocol.CaptureResult{
		Format:    "png",
		Width:     0,
		Height:    0,
		Data:      base64Encode(out),
		SizeBytes: int64(len(out)),
	}, nil
}

func (p *linuxPlatform) ScreenInfo() protocol.ScreenInfo {
	return protocol.ScreenInfo{Displays: []protocol.DisplayInfo{{ID: 0, Width: 0, Height: 0, Scale: 1.0, IsPrimary: true}}}
}

func (p *linuxPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	// Check if ffmpeg is available for x11grab-based streaming
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return protocol.ScreenStreamStartResult{}, fmt.Errorf("streaming requires ffmpeg (not found)")
	}
	if fps <= 0 {
		fps = 10
	}
	streamID := fmt.Sprintf("stream-%d", time.Now().UnixNano())
	// The actual frame capture is handled by the agent's streamFrames goroutine
	// which calls CaptureDisplay. We just validate that capture is possible.
	return protocol.ScreenStreamStartResult{
		StreamID: streamID,
	}, nil
}

func (p *linuxPlatform) ScreenStreamStop(streamID string) error {
	// Frame capture is stopped by the agent's stream manager
	return nil
}

func (p *linuxPlatform) Click(x int, y int, button string) error	{ return osExec("xdotool", "mousemove", fmt.Sprint(x), fmt.Sprint(y), "click", "1") }
func (p *linuxPlatform) TypeText(text string) error				{ return osExec("xdotool", "type", text) }
func (p *linuxPlatform) KeyPress(key string) error 				{ return osExec("xdotool", "key", key) }
func (p *linuxPlatform) KeyCombo(keys []string) error {
	args := append([]string{"key"}, keys...)
	return osExec("xdotool", args...)
}

func (p *linuxPlatform) Health(mode string) protocol.HealthResult {
	hostname, _ := os.Hostname()
	return protocol.HealthResult{
		AgentVersion: "0.1.0",
		Hostname:     hostname,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Mode:         mode,
	}
}

func (p *linuxPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	cmd := exec.Command("ps", "-eo", "pid,comm,pcpu,rss", "--no-headers")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := splitLines(string(out))
	result := make([]protocol.ProcessInfo, 0, len(lines))
	for _, line := range lines {
		var pid int
		var name string
		var cpu, mem float64
		fmt.Sscanf(line, "%d %s %f %f", &pid, &name, &cpu, &mem)
		result = append(result, protocol.ProcessInfo{
			PID:        pid,
			Name:       name,
			CPUPercent: cpu,
			MemoryMB:   mem / 1024,
		})
	}
	return result, nil
}

func (p *linuxPlatform) ProcessKill(pid int, signal int) error {
	if signal == 0 {
		signal = 15
	}
	return syscall.Kill(pid, syscall.Signal(signal))
}

func (p *linuxPlatform) OpenURL(url string) error {
	return osExec("xdg-open", url)
}

func (p *linuxPlatform) Notify(title string, body string, icon string) error {
	return osExec("notify-send", title, body)
}

func (p *linuxPlatform) ClipboardGet() (string, error) {
	cmd := exec.Command("xclip", "-selection", "clipboard", "-o")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func (p *linuxPlatform) ClipboardSet(text string) error {
	cmd := exec.Command("xclip", "-selection", "clipboard")
	stdin, _ := cmd.StdinPipe()
	cmd.Start()
	stdin.Write([]byte(text))
	stdin.Close()
	return cmd.Wait()
}

func osExec(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

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
