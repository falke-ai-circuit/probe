package agent

import (
	"fmt"
	"os"
)

// fileReadSensor reads the contents of a file on disk. Normal, boring file
// I/O — the building block the flow poller composes into monitoring.
// Args: path (required), max_bytes (default 65536).
type fileReadSensor struct{}

func (fileReadSensor) Name() string        { return "file_read" }
func (fileReadSensor) Category() string    { return "filesystem" }
func (fileReadSensor) Description() string { return "Read the contents of a file (path + optional max_bytes)" }

func (fileReadSensor) Read(args map[string]string) (any, error) {
	path := args["path"]
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	maxBytes := 64 * 1024
	if m, ok := args["max_bytes"]; ok {
		var v int
		if _, err := fmt.Sscan(m, &v); err == nil && v > 0 {
			maxBytes = v
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	truncated := false
	if len(data) > maxBytes {
		data = data[:maxBytes]
		truncated = true
	}
	return map[string]any{
		"path":      path,
		"size":      len(data),
		"content":   string(data),
		"truncated": truncated,
	}, nil
}

// fileWriteSensor writes data to a file on disk. Normal file I/O.
// Args: path (required), content (required), append (optional, default false).
type fileWriteSensor struct{}

func (fileWriteSensor) Name() string        { return "file_write" }
func (fileWriteSensor) Category() string    { return "filesystem" }
func (fileWriteSensor) Description() string { return "Write data to a file (path + content, optional append)" }

func (fileWriteSensor) Read(args map[string]string) (any, error) {
	path := args["path"]
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	content := args["content"]
	appendMode := args["append"] == "true"

	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	n, err := f.WriteString(content)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":          path,
		"bytes_written": n,
		"append":        appendMode,
	}, nil
}
