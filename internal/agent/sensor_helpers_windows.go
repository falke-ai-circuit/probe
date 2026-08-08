//go:build windows

package agent

import (
	"os"
	"time"
)

// diskUsageFn on Windows uses os.DiskUsage (Go 1.22+, stdlib).
func init() {
	diskUsageFn = func(path string) (total, free uint64, err error) {
		du, err := os.DiskUsage(path)
		if err != nil {
			return 0, 0, err
		}
		return du.Total, du.Free, nil
	}
}

// fileStatFn on Windows uses os.Stat (same as Unix; the stat result
// fields are uniform across platforms in Go's os package).
func init() {
	fileStatFn = func(path string) (any, error) {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"path":     path,
			"size":     info.Size(),
			"mode":     info.Mode().String(),
			"mod_time": info.ModTime().UTC().Format(time.RFC3339),
			"is_dir":   info.IsDir(),
		}, nil
	}
}
