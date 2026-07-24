package features

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// RotateLogger provides structured logging with automatic file rotation
// based on size and age. It reuses the LogLevel type and constants
// (LevelDebug, LevelInfo, LevelWarn, LevelError) defined in api.go.
type RotateLogger struct {
	mu         sync.Mutex
	filename   string
	file       *os.File
	maxSize    int64 // bytes
	maxAge     time.Duration
	maxBackups int
	level      LogLevel
}

// LogEntry represents a single structured log record.
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Level     string            `json:"level"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// RotateConfig holds configuration for creating a RotateLogger.
type RotateConfig struct {
	Filename   string
	MaxSize    int64         // max file size in bytes before rotation
	MaxAge     time.Duration // max age of backup files
	MaxBackups int           // max number of old backup files to keep
	Level      LogLevel
}

// NewRotateLogger creates a rotating logger from a RotateConfig.
func NewRotateLogger(cfg RotateConfig) (*RotateLogger, error) {
	if cfg.Filename == "" {
		return nil, fmt.Errorf("log filename cannot be empty")
	}
	if cfg.MaxSize <= 0 {
		cfg.MaxSize = 10 * 1024 * 1024 // 10 MB default
	}
	if cfg.MaxAge <= 0 {
		cfg.MaxAge = 7 * 24 * time.Hour // 7 days default
	}
	if cfg.MaxBackups <= 0 {
		cfg.MaxBackups = 5
	}

	dir := filepath.Dir(cfg.Filename)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}

	f, err := os.OpenFile(cfg.Filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	return &RotateLogger{
		filename:   cfg.Filename,
		file:       f,
		maxSize:    cfg.MaxSize,
		maxAge:     cfg.MaxAge,
		maxBackups: cfg.MaxBackups,
		level:      cfg.Level,
	}, nil
}

// Write writes a LogEntry to the log file, rotating if necessary.
func (rl *RotateLogger) Write(entry LogEntry) error {
	levelVal := parseLevelString(entry.Level)
	if levelVal < rl.level {
		return nil // filtered out
	}

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	var line string
	if len(entry.Fields) > 0 {
		line = rl.FormatJSON(entry)
	} else {
		line = rl.FormatText(entry)
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	if rl.needsRotation() {
		if err := rl.rotate(); err != nil {
			return fmt.Errorf("rotate before write: %w", err)
		}
	}

	_, err := io.WriteString(rl.file, line+"\n")
	return err
}

// WriteLevel writes a message at the given level with optional fields.
func (rl *RotateLogger) WriteLevel(level LogLevel, msg string, fields map[string]string) error {
	return rl.Write(LogEntry{
		Timestamp: time.Now(),
		Level:     levelString(level),
		Message:   msg,
		Fields:    fields,
	})
}

// Rotate forces immediate rotation of the current log file.
func (rl *RotateLogger) Rotate() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.rotate()
}

// rotate closes the current file, renames it with a timestamp, and opens a new one.
func (rl *RotateLogger) rotate() error {
	if rl.file != nil {
		rl.file.Close()
	}

	ext := filepath.Ext(rl.filename)
	base := strings.TrimSuffix(rl.filename, ext)
	suffix := time.Now().Format("20060102_150405")
	backupName := fmt.Sprintf("%s_%s%s", base, suffix, ext)

	if err := os.Rename(rl.filename, backupName); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("rename log file: %w", err)
	}

	f, err := os.OpenFile(rl.filename, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("reopen log file: %w", err)
	}
	rl.file = f

	// Prune old backups after rotation.
	rl.pruneOld()

	return nil
}

// PruneOld removes backup files older than maxAge or exceeding maxBackups.
func (rl *RotateLogger) PruneOld() int {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	return rl.pruneOld()
}

// pruneOld is the internal, non-locking version of PruneOld.
func (rl *RotateLogger) pruneOld() int {
	dir := filepath.Dir(rl.filename)
	base := strings.TrimSuffix(filepath.Base(rl.filename), filepath.Ext(rl.filename))
	ext := filepath.Ext(rl.filename)

	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	type backupFile struct {
		path  string
		mtime time.Time
	}

	var backups []backupFile
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, base+"_") || !strings.HasSuffix(name, ext) {
			continue
		}
		if name == filepath.Base(rl.filename) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, backupFile{
			path:  filepath.Join(dir, name),
			mtime: info.ModTime(),
		})
	}

	sort.Slice(backups, func(i, j int) bool {
		return backups[i].mtime.After(backups[j].mtime)
	})

	pruned := 0
	now := time.Now()

	for i, bf := range backups {
		tooOld := now.Sub(bf.mtime) > rl.maxAge
		tooMany := i >= rl.maxBackups
		if tooOld || tooMany {
			if err := os.Remove(bf.path); err == nil {
				pruned++
			}
		}
	}

	return pruned
}

// needsRotation checks whether the current file exceeds the max size.
func (rl *RotateLogger) needsRotation() bool {
	stat, err := rl.file.Stat()
	if err != nil {
		return false
	}
	return stat.Size() >= rl.maxSize
}

// FormatJSON renders a LogEntry as a JSON string.
func (rl *RotateLogger) FormatJSON(entry LogEntry) string {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Sprintf(`{"timestamp":"%s","level":"error","message":"log marshal failed: %s"}`,
			time.Now().Format(time.RFC3339), err)
	}
	return string(data)
}

// FormatText renders a LogEntry as a human-readable line.
func (rl *RotateLogger) FormatText(entry LogEntry) string {
	var sb strings.Builder
	sb.WriteString(entry.Timestamp.Format(time.RFC3339))
	sb.WriteString(" [")
	sb.WriteString(strings.ToUpper(entry.Level))
	sb.WriteString("] ")
	sb.WriteString(entry.Message)
	for k, v := range entry.Fields {
		sb.WriteString(fmt.Sprintf(" %s=%s", k, v))
	}
	return sb.String()
}

// Close flushes and closes the underlying file.
func (rl *RotateLogger) Close() error {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.file != nil {
		return rl.file.Close()
	}
	return nil
}

// SetLevel changes the minimum log level.
func (rl *RotateLogger) SetLevel(level LogLevel) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.level = level
}

// parseLevelString converts a level name string to a LogLevel value.
func parseLevelString(s string) LogLevel {
	switch strings.ToLower(s) {
	case "debug":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}