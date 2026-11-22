package testutil

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/falke-ai-circuit/probe/internal/protocol"
)

// MockPlatform implements platform.Platform with an in-memory filesystem
// and canned exec results. It is safe for concurrent use.
type MockPlatform struct {
	mu       sync.Mutex
	files    map[string][]byte // path → content
	dirs     map[string]bool   // path → exists
	execResult protocol.ExecResult
	execErr   error
	execCalls  []string // record of commands passed to Exec
}

// NewMockPlatform creates a MockPlatform with a single root directory.
func NewMockPlatform(rootDir string) *MockPlatform {
	return &MockPlatform{
		files: make(map[string][]byte),
		dirs: map[string]bool{
			rootDir:            true,
			filepath.Clean(rootDir): true,
		},
	}
}

// SetExecResult configures the canned result returned by Exec.
func (m *MockPlatform) SetExecResult(result protocol.ExecResult, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execResult = result
	m.execErr = err
}

// ExecCalls returns a copy of all commands passed to Exec.
func (m *MockPlatform) ExecCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.execCalls))
	copy(out, m.execCalls)
	return out
}

// --- Filesystem methods ---

func (m *MockPlatform) ListDir(path string) ([]protocol.FSEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	var entries []protocol.FSEntry
	// Find all files/dirs that are direct children of path
	for fp, content := range m.files {
		dir := filepath.Dir(fp)
		if dir == cleanPath {
			entries = append(entries, protocol.FSEntry{
				Name:    filepath.Base(fp),
				Size:    int64(len(content)),
				Mode:    "-rw-r--r--",
				ModTime: 0,
				IsDir:   false,
			})
		}
	}
	for dp := range m.dirs {
		if dp == cleanPath {
			continue
		}
		dir := filepath.Dir(dp)
		if dir == cleanPath {
			entries = append(entries, protocol.FSEntry{
				Name:    filepath.Base(dp),
				Size:    0,
				Mode:    "drwxr-xr-x",
				ModTime: 0,
				IsDir:   true,
			})
		}
	}
	return entries, nil
}

func (m *MockPlatform) FileStat(path string) (protocol.FSStatResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	if content, ok := m.files[cleanPath]; ok {
		return protocol.FSStatResult{
			Size:    int64(len(content)),
			Mode:    "-rw-r--r--",
			ModTime: 0,
			IsDir:   false,
			Exists:  true,
		}, nil
	}
	if m.dirs[cleanPath] {
		return protocol.FSStatResult{
			Size:    0,
			Mode:    "drwxr-xr-x",
			ModTime: 0,
			IsDir:   true,
			Exists:  true,
		}, nil
	}
	return protocol.FSStatResult{Exists: false}, nil
}

func (m *MockPlatform) ReadFile(path string, offset int, limit int) (protocol.FSReadResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	content, ok := m.files[cleanPath]
	if !ok {
		return protocol.FSReadResult{}, fmt.Errorf("file not found: %s", path)
	}
	data := content
	if offset > 0 {
		if offset >= len(data) {
			data = nil
		} else {
			data = data[offset:]
		}
	}
	if limit > 0 && limit < len(data) {
		data = data[:limit]
	}
	// P8-05: the window carries total_size/truncated/digest so a caller can
	// tell a partial read from a complete one.
	total := int64(len(content))
	if offset < 0 {
		offset = 0
	}
	sum := sha256.Sum256(data)
	return protocol.FSReadResult{
		Data:      base64.StdEncoding.EncodeToString(data),
		Size:      int64(len(data)),
		TotalSize: total,
		Offset:    offset,
		Truncated: int64(offset)+int64(len(data)) < total,
		Sha256:    fmt.Sprintf("%x", sum),
		Encoding:  "base64",
	}, nil
}

func (m *MockPlatform) WriteFile(path string, data []byte, mode string) (protocol.FSWriteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	m.files[cleanPath] = data
	// Ensure parent dir exists
	dir := filepath.Dir(cleanPath)
	if dir != "." && dir != "/" {
		m.dirs[dir] = true
	}
	return protocol.FSWriteResult{Written: len(data), Path: path}, nil
}

func (m *MockPlatform) DeleteFile(path string) (protocol.FSDeleteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	if _, ok := m.files[cleanPath]; ok {
		delete(m.files, cleanPath)
		return protocol.FSDeleteResult{Deleted: true, Path: path}, nil
	}
	return protocol.FSDeleteResult{Deleted: false, Path: path}, fmt.Errorf("file not found: %s", path)
}

func (m *MockPlatform) MoveFile(from string, to string) (protocol.FSMoveResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanFrom := filepath.Clean(from)
	cleanTo := filepath.Clean(to)
	data, ok := m.files[cleanFrom]
	if !ok {
		return protocol.FSMoveResult{Moved: false, From: from, To: to}, fmt.Errorf("file not found: %s", from)
	}
	delete(m.files, cleanFrom)
	m.files[cleanTo] = data
	return protocol.FSMoveResult{Moved: true, From: from, To: to}, nil
}

func (m *MockPlatform) Mkdir(path string) (protocol.FSMkdirResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	m.dirs[cleanPath] = true
	return protocol.FSMkdirResult{Created: true, Path: path}, nil
}

// --- Shell ---

func (m *MockPlatform) Exec(command string, timeout int, workDir string, env map[string]string) (protocol.ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, command)
	return m.execResult, m.execErr
}

// --- Screen (stubs) ---

func (m *MockPlatform) CaptureDisplay(display int, quality int) (protocol.CaptureResult, error) {
	return protocol.CaptureResult{}, fmt.Errorf("not implemented")
}

func (m *MockPlatform) ScreenInfo() protocol.ScreenInfo {
	return protocol.ScreenInfo{}
}

func (m *MockPlatform) ScreenStreamStart(display int, fps int, quality int) (protocol.ScreenStreamStartResult, error) {
	return protocol.ScreenStreamStartResult{}, fmt.Errorf("not implemented")
}

func (m *MockPlatform) ScreenStreamStop(streamID string) error {
	return fmt.Errorf("not implemented")
}

// --- Input (stubs) ---

func (m *MockPlatform) Click(x int, y int, button string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPlatform) TypeText(text string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPlatform) KeyPress(key string) error {
	return fmt.Errorf("not implemented")
}

func (m *MockPlatform) KeyCombo(keys []string) error {
	return fmt.Errorf("not implemented")
}

// --- System ---

func (m *MockPlatform) Health(mode string) protocol.HealthResult {
	return protocol.HealthResult{
		AgentVersion: "test-0.1",
		Hostname:     "mock",
		OS:           "linux",
		Arch:         "amd64",
		Mode:         mode,
	}
}

func (m *MockPlatform) ProcessList() ([]protocol.ProcessInfo, error) {
	return []protocol.ProcessInfo{{PID: 1, Name: "init"}}, nil
}

func (m *MockPlatform) ProcessKill(pid int, signal int) error {
	return nil
}

func (m *MockPlatform) OpenURL(url string) error {
	return nil
}

func (m *MockPlatform) Notify(title string, body string, icon string) error {
	return nil
}

func (m *MockPlatform) ClipboardGet() (string, error) {
	return "mock-clipboard", nil
}

func (m *MockPlatform) ClipboardSet(text string) error {
	return nil
}

// --- Helpers ---

// AddFile adds a file to the mock filesystem.
func (m *MockPlatform) AddFile(path string, content []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cleanPath := filepath.Clean(path)
	m.files[cleanPath] = content
	dir := filepath.Dir(cleanPath)
	if dir != "." && dir != "/" {
		m.dirs[dir] = true
	}
}

// AddDir adds a directory to the mock filesystem.
func (m *MockPlatform) AddDir(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.dirs[filepath.Clean(path)] = true
}

// base64Encode is a minimal base64 encoder to avoid importing encoding/base64
// in a way that creates circular deps. Actually there's no circular dep risk;
// use stdlib.
func base64Encode(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	// Use stdlib
	const table = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result strings.Builder
	for i := 0; i < len(data); i += 3 {
		b0 := data[i]
		b1 := byte(0)
		b2 := byte(0)
		if i+1 < len(data) {
			b1 = data[i+1]
		}
		if i+2 < len(data) {
			b2 = data[i+2]
		}
		result.WriteByte(table[b0>>2])
		result.WriteByte(table[((b0&0x03)<<4)|(b1>>4)])
		if i+1 < len(data) {
			result.WriteByte(table[((b1&0x0f)<<2)|(b2>>6)])
		} else {
			result.WriteByte('=')
		}
		if i+2 < len(data) {
			result.WriteByte(table[b2&0x3f])
		} else {
			result.WriteByte('=')
		}
	}
	return result.String()
}

