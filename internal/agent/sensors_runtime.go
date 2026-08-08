package agent

import (
	"runtime"
	"syscall"
	"time"
)

// memory_stats: returns process memory stats from runtime.ReadMemStats.
type memoryStatsSensor struct{}

func (memoryStatsSensor) Name() string        { return "memory_stats" }
func (memoryStatsSensor) Category() string    { return "process" }
func (memoryStatsSensor) Description() string { return "Go process memory stats: alloc, sys, heap, GC" }

func (memoryStatsSensor) Read(args map[string]string) (any, error) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return map[string]any{
		"alloc_bytes":       ms.Alloc,
		"total_alloc_bytes": ms.TotalAlloc,
		"sys_bytes":         ms.Sys,
		"heap_alloc_bytes":  ms.HeapAlloc,
		"heap_inuse_bytes":  ms.HeapInuse,
		"heap_objects":      ms.HeapObjects,
		"num_gc":            ms.NumGC,
		"gc_pause_total_ns": ms.PauseTotalNs,
		"next_gc_bytes":     ms.NextGC,
	}, nil
}

// disk_usage: per-path disk usage. Uses os.Statfs on Linux/macOS/Android
// and os.DiskUsage on Windows (Go 1.22+). Both functions exist in stdlib;
// we pick the right one at compile time via build constraints.
//
// The body is split between sensors_disk_unix.go and sensors_disk_windows.go
// to keep the platform-specific calls out of this file.
type diskUsageSensor struct{}

func (diskUsageSensor) Name() string        { return "disk_usage" }
func (diskUsageSensor) Category() string    { return "filesystem" }
func (diskUsageSensor) Description() string { return "Per-path disk usage (total/free/used bytes)" }

func (diskUsageSensor) Read(args map[string]string) (any, error) {
	path := args["path"]
	if path == "" {
		path = "/"
	}
	total, free, err := diskUsageFn(path)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":       path,
		"total":      total,
		"free":       free,
		"used":       total - free,
		"read_time":  time.Now().UTC(),
	}, nil
}

// env_vars: returns a safe allowlist of environment variables. Secrets
// (anything containing KEY, SECRET, TOKEN, PASSWORD in the name) are
// filtered out.
type envVarsSensor struct{}

func (envVarsSensor) Name() string        { return "env_vars" }
func (envVarsSensor) Category() string    { return "filesystem" }
func (envVarsSensor) Description() string { return "Filtered environment variables (secrets removed)" }

func (envVarsSensor) Read(args map[string]string) (any, error) {
	allow := map[string]bool{
		"PATH": true, "HOME": true, "USER": true,
		"TZ": true, "PWD": true, "SHELL": true, "TERM": true,
		"HOSTNAME": true, "EDITOR": true,
	}
	out := make(map[string]string)
	for _, kv := range envFn() {
		// env is []string of "KEY=VALUE"
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				k := kv[:i]
				v := kv[i+1:]
				if allow[k] {
					out[k] = v
				}
				break
			}
		}
	}
	return out, nil
}

// file_stat: stat a single path. Returns size, mode, mtime.
type fileStatSensor struct{}

func (fileStatSensor) Name() string        { return "file_stat" }
func (fileStatSensor) Category() string    { return "filesystem" }
func (fileStatSensor) Description() string { return "Stat a single file/dir (size, mode, mtime)" }

func (fileStatSensor) Read(args map[string]string) (any, error) {
	path := args["path"]
	if path == "" {
		return nil, syscall.EINVAL
	}
	return fileStatFn(path)
}
