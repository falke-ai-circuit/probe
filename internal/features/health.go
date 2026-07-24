package features

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// HealthMonitor tracks system health metrics and runs periodic checks.
type HealthMonitor struct {
	mu          sync.Mutex
	checks      []HealthCheck
	alerts      []Alert
	thresholds  map[string]float64
	lastResults map[string]float64
	startTime   time.Time
}

// HealthCheck represents a single health check function.
type HealthCheck struct {
	Name     string
	Function func() (float64, error)
	Warning  float64
	Critical float64
	Unit     string
}

// Alert represents a health alert.
type Alert struct {
	Timestamp time.Time
	Check     string
	Level     string
	Value     float64
	Message   string
}

// NewHealthMonitor creates a new health monitor.
func NewHealthMonitor() *HealthMonitor {
	return &HealthMonitor{
		thresholds:  make(map[string]float64),
		lastResults: make(map[string]float64),
		startTime:   time.Now(),
	}
}

// RegisterCheck adds a health check to the monitor.
func (h *HealthMonitor) RegisterCheck(name string, fn func() (float64, error), critical float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.checks = append(h.checks, HealthCheck{
		Name:     name,
		Function: fn,
		Critical: critical,
		Unit:     "",
	})
	h.thresholds[name] = critical
}

// RunChecks executes all registered health checks.
func (h *HealthMonitor) RunChecks() []Alert {
	h.mu.Lock()
	defer h.mu.Unlock()
	var newAlerts []Alert
	for _, check := range h.checks {
		value, err := check.Function()
		if err != nil {
			newAlerts = append(newAlerts, Alert{
				Timestamp: time.Now(),
				Check:     check.Name,
				Level:     "error",
				Value:     0,
				Message:   err.Error(),
			})
			continue
		}
		h.lastResults[check.Name] = value
		if value > check.Critical {
			newAlerts = append(newAlerts, Alert{
				Timestamp: time.Now(),
				Check:     check.Name,
				Level:     "critical",
				Value:     value,
				Message:   fmt.Sprintf("%s exceeded threshold: %.2f > %.2f", check.Name, value, check.Critical),
			})
		}
	}
	h.alerts = append(h.alerts, newAlerts...)
	return newAlerts
}

// GetStatus returns overall health status.
func (h *HealthMonitor) GetStatus() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	for name, value := range h.lastResults {
		if threshold, ok := h.thresholds[name]; ok && value > threshold {
			return "degraded"
		}
	}
	return "healthy"
}

// GetAlerts returns all alerts.
func (h *HealthMonitor) GetAlerts() []Alert {
	h.mu.Lock()
	defer h.mu.Unlock()
	alerts := make([]Alert, len(h.alerts))
	copy(alerts, h.alerts)
	return alerts
}

// FormatReport returns a health report as text.
func (h *HealthMonitor) FormatReport() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var buf strings.Builder
	buf.WriteString("=== Health Report ===\n")
	buf.WriteString(fmt.Sprintf("Status: %s\n", h.GetStatus()))
	buf.WriteString(fmt.Sprintf("Uptime: %s\n", time.Since(h.startTime).Round(time.Second)))
	buf.WriteString(fmt.Sprintf("Checks: %d\n", len(h.checks)))
	buf.WriteString(fmt.Sprintf("Alerts: %d\n", len(h.alerts)))
	buf.WriteString("\nCheck Results:\n")
	for name, value := range h.lastResults {
		threshold := h.thresholds[name]
		status := "OK"
		if value > threshold {
			status = "CRITICAL"
		}
		buf.WriteString(fmt.Sprintf("  %s: %.2f (threshold: %.2f) [%s]\n", name, value, threshold, status))
	}
	return buf.String()
}

// SystemInfo collects system information.
type SystemInfo struct {
	Hostname  string
	OS        string
	Arch      string
	CPUs      int
	GoVersion string
	Memory    uint64
}

// CollectSystemInfo gathers current system information.
func CollectSystemInfo() SystemInfo {
	hostname, _ := os.Hostname()
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	return SystemInfo{
		Hostname:  hostname,
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUs:      runtime.NumCPU(),
		GoVersion: runtime.Version(),
		Memory:    memStats.Sys,
	}
}

// FormatSystemInfo returns system info as text.
func (s SystemInfo) Format() string {
	return fmt.Sprintf("Hostname: %s\nOS: %s\nArch: %s\nCPUs: %d\nGo: %s\nMemory: %d bytes",
		s.Hostname, s.OS, s.Arch, s.CPUs, s.GoVersion, s.Memory)
}