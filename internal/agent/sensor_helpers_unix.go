//go:build !windows

package agent

import (
	"os"
	"syscall"
	"time"
)

// diskUsageFn returns total and free bytes for the given path on Unix
// (Linux, macOS, Android, *BSD). Uses syscall.Statfs which is portable
// across all Unix-like systems supported by Go.
func init() {
	diskUsageFn = func(path string) (total, free uint64, err error) {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(path, &stat); err != nil {
			return 0, 0, err
		}
		// On Linux, Bsize is the optimal transfer block size. On macOS,
		// it's the filesystem block size. Both work as a multiplier.
		total = stat.Blocks * uint64(stat.Bsize)
		free = stat.Bavail * uint64(stat.Bsize)
		return total, free, nil
	}
}

// fileStatFn returns file info for the given path on Unix.
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
