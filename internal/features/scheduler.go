package features

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// TaskScheduler manages periodic background tasks.
type TaskScheduler struct {
	mu     sync.Mutex
	tasks  []ScheduledTask
	stopCh chan struct{}
	running bool
}

// ScheduledTask represents a recurring task.
type ScheduledTask struct {
	Name     string
	Interval time.Duration
	Function func() error
	LastRun  time.Time
	NextRun  time.Time
	RunCount int64
	LastError string
}

// NewScheduler creates a new task scheduler.
func NewScheduler() *TaskScheduler {
	return &TaskScheduler{
		stopCh: make(chan struct{}),
	}
}

// Register adds a task to the scheduler.
func (s *TaskScheduler) Register(name string, interval time.Duration, fn func() error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tasks = append(s.tasks, ScheduledTask{
		Name:     name,
		Interval: interval,
		Function: fn,
		NextRun:  time.Now().Add(interval),
	})
}

// Start begins executing scheduled tasks.
func (s *TaskScheduler) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.runDueTasks()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop halts the scheduler.
func (s *TaskScheduler) Stop() {
	s.mu.Lock()
	if s.running {
		s.running = false
		close(s.stopCh)
	}
	s.mu.Unlock()
}

// runDueTasks executes tasks whose next run time has passed.
func (s *TaskScheduler) runDueTasks() {
	s.mu.Lock()
	now := time.Now()
	var due []int
	for i := range s.tasks {
		if now.After(s.tasks[i].NextRun) || now.Equal(s.tasks[i].NextRun) {
			due = append(due, i)
		}
	}
	s.mu.Unlock()

	for _, idx := range due {
		s.mu.Lock()
		task := &s.tasks[idx]
		s.mu.Unlock()
		err := task.Function()
		s.mu.Lock()
		task.LastRun = now
		task.NextRun = now.Add(task.Interval)
		task.RunCount++
		if err != nil {
			task.LastError = err.Error()
		} else {
			task.LastError = ""
		}
		s.mu.Unlock()
	}
}

// GetTasks returns a copy of all scheduled tasks.
func (s *TaskScheduler) GetTasks() []ScheduledTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	tasks := make([]ScheduledTask, len(s.tasks))
	copy(tasks, s.tasks)
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].NextRun.Before(tasks[j].NextRun)
	})
	return tasks
}

// FormatSchedule returns the schedule as text.
func (s *TaskScheduler) FormatSchedule() string {
	tasks := s.GetTasks()
	var buf strings.Builder
	buf.WriteString("=== Scheduled Tasks ===\n")
	for _, t := range tasks {
		status := "waiting"
		if t.RunCount > 0 {
			status = fmt.Sprintf("ran %d times, last: %s", t.RunCount, t.LastRun.Format(time.RFC3339))
			if t.LastError != "" {
				status += fmt.Sprintf(" ERROR: %s", t.LastError)
			}
		}
		buf.WriteString(fmt.Sprintf("  %s (every %s): %s\n", t.Name, t.Interval, status))
	}
	return buf.String()
}

// ConfigManager handles application configuration with validation.
type ConfigManager struct {
	mu       sync.RWMutex
	data     map[string]interface{}
	filePath string
	modified time.Time
}

// NewConfigManager creates a new config manager.
func NewConfigManager(filePath string) *ConfigManager {
	return &ConfigManager{
		data:     make(map[string]interface{}),
		filePath: filePath,
	}
}

// Set stores a configuration value.
func (c *ConfigManager) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
	c.modified = time.Now()
}

// Get retrieves a configuration value.
func (c *ConfigManager) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	val, ok := c.data[key]
	return val, ok
}

// GetString retrieves a string configuration value with default.
func (c *ConfigManager) GetString(key, def string) string {
	val, ok := c.Get(key)
	if !ok {
		return def
	}
	s, ok := val.(string)
	if !ok {
		return def
	}
	return s
}

// GetInt retrieves an integer configuration value with default.
func (c *ConfigManager) GetInt(key string, def int) int {
	val, ok := c.Get(key)
	if !ok {
		return def
	}
	switch v := val.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return def
	}
}

// Validate checks that all required keys are present.
func (c *ConfigManager) Validate(required []string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, key := range required {
		if _, ok := c.data[key]; !ok {
			return fmt.Errorf("missing required config key: %s", key)
		}
	}
	return nil
}

// Save writes configuration to file.
func (c *ConfigManager) Save() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	dir := filepath.Dir(c.filePath)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}
	}
	var buf strings.Builder
	for key, val := range c.data {
		buf.WriteString(fmt.Sprintf("%s=%v\n", key, val))
	}
	return os.WriteFile(c.filePath, []byte(buf.String()), 0644)
}

// Load reads configuration from file.
func (c *ConfigManager) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			c.data[parts[0]] = parts[1]
		}
	}
	c.modified = time.Now()
	return nil
}

// GetModifiedTime returns when the config was last modified.
func (c *ConfigManager) GetModifiedTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.modified
}