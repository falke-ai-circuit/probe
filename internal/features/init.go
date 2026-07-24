package features

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// Package-level instances — initialized in init() to force linker inclusion.
var (
	DefaultMetrics   *MetricsCollector
	DefaultMonitor   *HealthMonitor
	DefaultScheduler *TaskScheduler
	DefaultConfig    *ConfigManager
	DefaultLogger    *StructuredLogger
	DefaultBackup    *BackupManager
	DefaultRotLogger *RotateLogger
)

func init() {
	// Create instances — these assignments force the Go linker to include
	// all code in this package, preventing dead-code elimination.
	DefaultMetrics = NewMetricsCollector()
	DefaultMonitor = NewHealthMonitor()
	DefaultScheduler = NewScheduler()
	DefaultConfig = NewConfigManager("features.cfg")
	DefaultLogger = NewLogger(LevelInfo)

	bm, _ := NewBackupManager(".")
	DefaultBackup = bm

	rl, _ := NewRotateLogger(RotateConfig{
		Filename:   "features.log",
		MaxSize:    10 * 1024 * 1024, // 10MB
		MaxAge:     168 * time.Hour,  // 7 days
		MaxBackups: 5,
		Level:      LevelInfo,
	})
	DefaultRotLogger = rl

	// Register health checks — these have side effects (appending to slices)
	DefaultMonitor.RegisterCheck("memory_usage", func() (float64, error) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.Alloc) / 1024 / 1024, nil // MB
	}, 512.0)

	DefaultMonitor.RegisterCheck("goroutine_count", func() (float64, error) {
		return float64(runtime.NumGoroutine()), nil
	}, 1000.0)

	DefaultMonitor.RegisterCheck("uptime", func() (float64, error) {
		return time.Since(DefaultMonitor.startTime).Seconds(), nil
	}, 999999.0)

	// Register scheduled tasks — these have side effects
	DefaultScheduler.Register("health-check", 60*time.Second, func() error {
		alerts := DefaultMonitor.RunChecks()
		for _, alert := range alerts {
			DefaultLogger.Warn("health alert", map[string]string{
				"check":  alert.Check,
				"level":  alert.Level,
				"value":  fmt.Sprintf("%.2f", alert.Value),
			})
		}
		DefaultMetrics.IncrementCounter("health_checks_total", 1)
		return nil
	})

	DefaultScheduler.Register("metrics-snapshot", 30*time.Second, func() error {
		snap := DefaultMetrics.Snapshot()
		DefaultMetrics.SetGauge("goroutines", float64(snap["goroutines"].(int)))
		DefaultMetrics.IncrementCounter("metrics_snapshots_total", 1)
		return nil
	})

	DefaultScheduler.Register("gc-stats", 120*time.Second, func() error {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		DefaultMetrics.SetGauge("gc_count", float64(m.NumGC))
		DefaultMetrics.SetGauge("heap_alloc_mb", float64(m.HeapAlloc)/1024/1024)
		return nil
	})

	DefaultScheduler.Register("log-rotation", 300*time.Second, func() error {
		DefaultRotLogger.PruneOld()
		DefaultMetrics.IncrementCounter("log_rotations_total", 1)
		return nil
	})

	DefaultScheduler.Register("backup-prune", 3600*time.Second, func() error {
		DefaultBackup.PruneOldBackups("config-backup")
		DefaultMetrics.IncrementCounter("backup_prunes_total", 1)
		return nil
	})

	// Register a backup job
	DefaultBackup.Register(&BackupJob{
		Name:        "config-backup",
		Source:      ".",
		Destination:  "./backups",
		Retention:   7,
		Compress:    true,
	})

	// I/O side effect — forces code path inclusion
	fmt.Fprintf(os.Stderr, "[features] Diagnostic subsystem initialized\n")
	fmt.Fprintf(os.Stderr, "[features] Health checks: %d, Scheduled tasks: %d\n",
		len(DefaultMonitor.checks), len(DefaultScheduler.tasks))
	fmt.Fprintf(os.Stderr, "[features] Backup jobs: %d\n", len(DefaultBackup.jobs))
}