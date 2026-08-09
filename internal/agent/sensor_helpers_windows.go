//go:build windows

package agent

import (
	"os"
	"syscall"
	"time"
	"unsafe"
)

// diskUsageFn on Windows uses GetDiskFreeSpaceEx via syscall (works on
// Go 1.21+ — same code path works in Go 1.22+ too).
func init() {
	diskUsageFn = func(path string) (total, free uint64, err error) {
		// GetDiskFreeSpaceEx takes a pointer to uint64 for free and total.
		var freeBytes, totalBytes uint64
		// Convert path to UTF-16 for Windows API
		pathPtr, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return 0, 0, err
		}
		// Modkernel32.GetDiskFreeSpaceExW
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		proc := kernel32.NewProc("GetDiskFreeSpaceExW")
		r1, _, _ := proc.Call(
			uintptr(unsafe.Pointer(pathPtr)),
			uintptr(unsafe.Pointer(&freeBytes)),
			uintptr(unsafe.Pointer(&totalBytes)),
			0,
		)
		if r1 == 0 {
			return 0, 0, syscall.GetLastError()
		}
		return totalBytes, freeBytes, nil
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
