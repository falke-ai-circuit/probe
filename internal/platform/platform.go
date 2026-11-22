package platform

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// readFileRange returns the bytes in [offset, offset+limit) of r, reading only
// that window (A5). Both platform ReadFile implementations use it so the range
// is applied at read time: the old path called os.ReadFile and sliced
// afterwards, which made a 1.5 MB file resident to answer a 4 KB request.
//
// Semantics are unchanged, so the wire contract does not move: a negative
// offset reads from the start, an offset at or past EOF returns no bytes, and
// limit <= 0 means "to the end of the file".
func readFileRange(r io.ReaderAt, size int64, offset, limit int) ([]byte, error) {
	if offset < 0 {
		offset = 0
	}
	if int64(offset) >= size {
		return nil, nil
	}
	n := size - int64(offset)
	if limit > 0 && int64(limit) < n {
		n = int64(limit)
	}
	if n <= 0 {
		return nil, nil
	}
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, int64(offset)); err != nil && err != io.EOF {
		return nil, err
	}
	return buf, nil
}

// fsReadResult assembles the read result for a window: the digest covers the
// bytes returned, totalSize is the file's size on disk, and truncated says
// whether the window ends before the end of the file. Every platform funnels
// through here so a partial read can never look like a complete one.
func fsReadResult(data []byte, totalSize int64, offset int) protocol.FSReadResult {
	sum := sha256.Sum256(data)
	return protocol.FSReadResult{
		Data:      base64Encode(data),
		Size:      int64(len(data)),
		TotalSize: totalSize,
		Offset:    offset,
		Truncated: int64(offset)+int64(len(data)) < totalSize,
		Sha256:    fmt.Sprintf("%x", sum),
		Encoding:  "base64",
	}
}

// Platform defines platform-specific operations.
type Platform interface {
	// Filesystem
	ListDir(path string) ([]protocol.FSEntry, error)
	FileStat(path string) (protocol.FSStatResult, error)
	ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error)
	WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error)
	DeleteFile(path string) (protocol.FSDeleteResult, error)
	MoveFile(from string, to string) (protocol.FSMoveResult, error)
	Mkdir(path string) (protocol.FSMkdirResult, error)

	// Shell
	Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error)

	// Screen (stubs for now)
	CaptureDisplay(display int, quality int) (protocol.CaptureResult, error)
	ScreenInfo() protocol.ScreenInfo
	ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error)
	ScreenStreamStop(streamID string) error

	// Input
	Click(x int, y int, button string) error
	TypeText(text string) error
	KeyPress(key string) error
	KeyCombo(keys []string) error

	// System
	Health(mode string) protocol.HealthResult
	ProcessList() ([]protocol.ProcessInfo, error)
	ProcessKill(pid int, signal int) error
	OpenURL(url string) error
	Notify(title string, body string, icon string) error
	ClipboardGet() (string, error)
	ClipboardSet(text string) error
}

var startTime = time.Now()
var agentHostname, _ = os.Hostname()

func BaseHealth(version string, mode string, connectedSince time.Time) protocol.HealthResult {
	return protocol.HealthResult{
		AgentVersion:   version,
		Hostname:       agentHostname,
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		UptimeSeconds:  int64(time.Since(startTime).Seconds()),
		Mode:           mode,
		ConnectedSince: connectedSince.Format(time.RFC3339),
	}
}

func BaseCaptureDisplay() (protocol.CaptureResult, error) {
	return protocol.CaptureResult{
		Format:    "png",
		Width:     0,
		Height:    0,
		Data:      "",
		SizeBytes: 0,
	}, fmt.Errorf("display capture not implemented on %s", runtime.GOOS)
}

func BaseScreenInfo() protocol.ScreenInfo {
	return protocol.ScreenInfo{Displays: []protocol.DisplayInfo{}}
}

func BaseClick(x int, y int, button string) error {
	return fmt.Errorf("input not implemented on %s", runtime.GOOS)
}

func BaseTypeText(text string) error {
	return fmt.Errorf("input not implemented on %s", runtime.GOOS)
}

func BaseKeyPress(key string) error {
	return fmt.Errorf("input not implemented on %s", runtime.GOOS)
}

func BaseKeyCombo(keys []string) error {
	return fmt.Errorf("input not implemented on %s", runtime.GOOS)
}

func BaseProcessList() ([]protocol.ProcessInfo, error) {
	return nil, fmt.Errorf("process list not implemented on %s", runtime.GOOS)
}

func BaseProcessKill(pid int, signal int) error {
	return fmt.Errorf("process kill not implemented on %s", runtime.GOOS)
}

func BaseOpenURL(url string) error {
	// Try xdg-open, open, start
	return nil
}

func BaseNotify(title string, body string, icon string) error {
	return nil
}

func BaseClipboardGet() (string, error) {
	return "", fmt.Errorf("clipboard not implemented on %s", runtime.GOOS)
}

func BaseClipboardSet(text string) error {
	return fmt.Errorf("clipboard not implemented on %s", runtime.GOOS)
}

func ExecCommand(command string, timeoutSec int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	// Generic exec using os/exec
	// To be overridden by platform-specific implementations
	return protocol.ExecResult{
		Stdout:   "",
		Stderr:   fmt.Sprintf("shell not implemented on %s/%s", runtime.GOOS, runtime.GOARCH),
		ExitCode: -1,
		TimedOut: false,
	}, nil
}

const (
	PlatformLinux  = "linux"
	PlatformDarwin = "darwin"
	PlatformWindows = "windows"
)

var currentPlatform Platform

func Get() Platform {
	if currentPlatform == nil {
		currentPlatform = &genericPlatform{}
	}
	return currentPlatform
}

type genericPlatform struct{}

func (p *genericPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
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

func (p *genericPlatform) FileStat(path string) (protocol.FSStatResult, error) {
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

func (p *genericPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	// A5: the same windowed read as the Windows path — no full-file buffer.
	data, err := readFileRange(f, info.Size(), offset, limit)
	if err != nil {
		return protocol.FSReadResult{}, err
	}
	if offset < 0 {
		offset = 0
	}
	return fsReadResult(data, info.Size(), offset), nil
}

func (p *genericPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
	// Create parent directories (handles both Unix and Windows path separators)
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return protocol.FSWriteResult{}, err
		}
	}
	fileMode := os.FileMode(0644)
	if mode != "" {
		// Parse octal mode string
		var m uint32
		if _, err := fmt.Sscanf(mode, "%o", &m); err == nil {
			fileMode = os.FileMode(m)
		}
	}
	err := os.WriteFile(path, data, fileMode)
	if err != nil {
		return protocol.FSWriteResult{}, err
	}
	return protocol.FSWriteResult{Written: len(data), Path: path}, nil
}

func (p *genericPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	err := os.Remove(path)
	if err != nil {
		return protocol.FSDeleteResult{Deleted: false, Path: path}, err
	}
	return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
}

func (p *genericPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	err := os.Rename(from, to)
	if err != nil {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, err
	}
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (p *genericPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	err := os.MkdirAll(path, 0755)
	if err != nil {
		return protocol.FSMkdirResult{Created: false, Path: path}, err
	}
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

func (p *genericPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	return ExecCommand(command, timeout, workDir, env)
}

func (p *genericPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	return BaseCaptureDisplay()
}

func (p *genericPlatform) ScreenInfo() protocol.ScreenInfo {
	return BaseScreenInfo()
}

func (p *genericPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	return protocol.ScreenStreamStartResult{}, fmt.Errorf("streaming not implemented on %s", runtime.GOOS)
}

func (p *genericPlatform) ScreenStreamStop(streamID string) error {
	return fmt.Errorf("streaming not implemented on %s", runtime.GOOS)
}

func (p *genericPlatform) Click(x int, y int, button string) error {
	return BaseClick(x, y, button)
}

func (p *genericPlatform) TypeText(text string) error {
	return BaseTypeText(text)
}

func (p *genericPlatform) KeyPress(key string) error {
	return BaseKeyPress(key)
}

func (p *genericPlatform) KeyCombo(keys []string) error {
	return BaseKeyCombo(keys)
}

func (p *genericPlatform) Health(mode string) protocol.HealthResult {
	return BaseHealth("0.1.0", mode, time.Now())
}

func (p *genericPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	return BaseProcessList()
}

func (p *genericPlatform) ProcessKill(pid int, signal int) error {
	return BaseProcessKill(pid, signal)
}

func (p *genericPlatform) OpenURL(url string) error {
	return BaseOpenURL(url)
}

func (p *genericPlatform) Notify(title string, body string, icon string) error {
	return BaseNotify(title, body, icon)
}

func (p *genericPlatform) ClipboardGet() (string, error) {
	return BaseClipboardGet()
}

func (p *genericPlatform) ClipboardSet(text string) error {
	return BaseClipboardSet(text)
}

func base64Encode(data []byte) string {
	return base64.StdEncoding.EncodeToString(data)
}
