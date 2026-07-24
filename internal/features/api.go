package features

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

// APIServer provides a REST API for diagnostics and monitoring.
type APIServer struct {
	mu        sync.RWMutex
	metrics   *MetricsCollector
	monitor   *HealthMonitor
	scheduler *TaskScheduler
	config    *ConfigManager
	server    *http.Server
	mux       *http.ServeMux
}

// NewAPIServer creates a new diagnostic API server.
func NewAPIServer(metrics *MetricsCollector, monitor *HealthMonitor, scheduler *TaskScheduler, config *ConfigManager) *APIServer {
	s := &APIServer{
		metrics:   metrics,
		monitor:   monitor,
		scheduler: scheduler,
		config:    config,
		mux:       http.NewServeMux(),
	}
	s.registerRoutes()
	return s
}

func (s *APIServer) registerRoutes() {
	s.mux.HandleFunc("/api/v1/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/v1/system", s.handleSystem)
	s.mux.HandleFunc("/api/v1/tasks", s.handleTasks)
	s.mux.HandleFunc("/api/v1/config", s.handleConfig)
	s.mux.HandleFunc("/api/v1/alerts", s.handleAlerts)
}

// Start begins serving the API.
func (s *APIServer) Start(addr string) error {
	s.mu.Lock()
	s.server = &http.Server{
		Addr:         addr,
		Handler:      s.mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}
	s.mu.Unlock()
	return s.server.ListenAndServe()
}

// Stop halts the API server.
func (s *APIServer) Stop() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

func (s *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := s.monitor.GetStatus()
	sysInfo := CollectSystemInfo()
	response := map[string]interface{}{
		"status":   status,
		"uptime":   time.Since(s.monitor.startTime).Seconds(),
		"hostname": sysInfo.Hostname,
		"goroutines": runtime.NumGoroutine(),
		"checks":   len(s.monitor.checks),
		"alerts":   len(s.monitor.GetAlerts()),
	}
	writeJSON(w, response)
}

func (s *APIServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	writeJSON(w, snap)
}

func (s *APIServer) handleSystem(w http.ResponseWriter, r *http.Request) {
	info := CollectSystemInfo()
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)
	response := map[string]interface{}{
		"hostname":   info.Hostname,
		"os":         info.OS,
		"arch":       info.Arch,
		"cpus":       info.CPUs,
		"go_version": info.GoVersion,
		"memory_alloc":   memStats.Alloc,
		"memory_sys":     memStats.Sys,
		"memory_total":   memStats.TotalAlloc,
		"gc_count":       memStats.NumGC,
		"goroutines":     runtime.NumGoroutine(),
	}
	writeJSON(w, response)
}

func (s *APIServer) handleTasks(w http.ResponseWriter, r *http.Request) {
	tasks := s.scheduler.GetTasks()
	writeJSON(w, tasks)
}

func (s *APIServer) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.config.mu.RLock()
		data := make(map[string]interface{}, len(s.config.data))
		for k, v := range s.config.data {
			data[k] = v
		}
		s.config.mu.RUnlock()
		writeJSON(w, data)
	}
}

func (s *APIServer) handleAlerts(w http.ResponseWriter, r *http.Request) {
	alerts := s.monitor.GetAlerts()
	writeJSON(w, alerts)
}

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		fmt.Fprintf(os.Stderr, "API encode error: %v\n", err)
	}
}

// StructuredLogger provides leveled logging with structured fields.
type StructuredLogger struct {
	mu       sync.Mutex
	level    LogLevel
	output   *os.File
	fields   map[string]string
}

// LogLevel represents logging severity levels.
type LogLevel int

const (
	LevelDebug LogLevel = iota
	LevelInfo
	LevelWarn
	LevelError
)

// NewLogger creates a new structured logger.
func NewLogger(level LogLevel) *StructuredLogger {
	return &StructuredLogger{
		level:  level,
		output: os.Stderr,
		fields: make(map[string]string),
	}
}

// WithField adds a persistent field to the logger.
func (l *StructuredLogger) WithField(key, value string) *StructuredLogger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fields[key] = value
	return l
}

// Log writes a log entry at the given level.
func (l *StructuredLogger) Log(level LogLevel, msg string, fields ...map[string]string) {
	if level < l.level {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("{\"time\":\"%s\",\"level\":\"%s\",\"msg\":%q", time.Now().Format(time.RFC3339), levelString(level), msg))
	for k, v := range l.fields {
		buf.WriteString(fmt.Sprintf(",\"%s\":%q", k, v))
	}
	for _, f := range fields {
		for k, v := range f {
			buf.WriteString(fmt.Sprintf(",\"%s\":%q", k, v))
		}
	}
	buf.WriteString("}\n")
	fmt.Fprint(l.output, buf.String())
}

func levelString(level LogLevel) string {
	switch level {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// Info logs an info message.
func (l *StructuredLogger) Info(msg string, fields ...map[string]string) {
	l.Log(LevelInfo, msg, fields...)
}

// Warn logs a warning message.
func (l *StructuredLogger) Warn(msg string, fields ...map[string]string) {
	l.Log(LevelWarn, msg, fields...)
}

// Error logs an error message.
func (l *StructuredLogger) Error(msg string, fields ...map[string]string) {
	l.Log(LevelError, msg, fields...)
}

// Debug logs a debug message.
func (l *StructuredLogger) Debug(msg string, fields ...map[string]string) {
	l.Log(LevelDebug, msg, fields...)
}