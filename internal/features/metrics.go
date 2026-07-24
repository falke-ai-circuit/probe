package features

import (
	"fmt"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

// MetricsCollector tracks application metrics: counters, gauges, histograms.
type MetricsCollector struct {
	mu         sync.RWMutex
	counters   map[string]*int64
	gauges     map[string]*uint64
	histograms map[string]*Histogram
	startTime  time.Time
}

// Histogram tracks value distribution with configurable buckets.
type Histogram struct {
	mu      sync.Mutex
	buckets []float64
	counts  []int64
	sum     float64
	count   int64
	min     float64
	max     float64
}

// NewMetricsCollector creates a new metrics collector.
func NewMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		counters:   make(map[string]*int64),
		gauges:     make(map[string]*uint64),
		histograms: make(map[string]*Histogram),
		startTime:  time.Now(),
	}
}

// IncrementCounter atomically increments a named counter.
func (m *MetricsCollector) IncrementCounter(name string, delta int64) {
	m.mu.RLock()
	ptr, ok := m.counters[name]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		ptr, ok = m.counters[name]
		if !ok {
			var v int64
			ptr = &v
			m.counters[name] = ptr
		}
		m.mu.Unlock()
	}
	atomic.AddInt64(ptr, delta)
}

// SetGauge sets a named gauge to a specific value.
func (m *MetricsCollector) SetGauge(name string, value float64) {
	m.mu.RLock()
	ptr, ok := m.gauges[name]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		ptr, ok = m.gauges[name]
		if !ok {
			var v uint64
			ptr = &v
			m.gauges[name] = ptr
		}
		m.mu.Unlock()
	}
	atomic.StoreUint64(ptr, math.Float64bits(value))
}

// ObserveHistogram records a value in a histogram.
func (m *MetricsCollector) ObserveHistogram(name string, value float64, buckets []float64) {
	m.mu.RLock()
	h, ok := m.histograms[name]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		h, ok = m.histograms[name]
		if !ok {
			h = NewHistogram(buckets)
			m.histograms[name] = h
		}
		m.mu.Unlock()
	}
	h.Observe(value)
}

// NewHistogram creates a histogram with the given bucket boundaries.
func NewHistogram(buckets []float64) *Histogram {
	return &Histogram{
		buckets: buckets,
		counts:  make([]int64, len(buckets)+1),
		min:     math.MaxFloat64,
		max:     0,
	}
}

// Observe records a value.
func (h *Histogram) Observe(value float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sum += value
	h.count++
	if value < h.min {
		h.min = value
	}
	if value > h.max {
		h.max = value
	}
	for i, bound := range h.buckets {
		if value <= bound {
			h.counts[i]++
			return
		}
	}
	h.counts[len(h.buckets)]++
}

// GetCounter returns the current value of a counter.
func (m *MetricsCollector) GetCounter(name string) int64 {
	m.mu.RLock()
	ptr, ok := m.counters[name]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return atomic.LoadInt64(ptr)
}

// GetGauge returns the current value of a gauge.
func (m *MetricsCollector) GetGauge(name string) float64 {
	m.mu.RLock()
	ptr, ok := m.gauges[name]
	m.mu.RUnlock()
	if !ok {
		return 0
	}
	return math.Float64frombits(atomic.LoadUint64(ptr))
}

// Snapshot returns a map of all current metric values.
func (m *MetricsCollector) Snapshot() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]interface{})
	result["uptime_seconds"] = time.Since(m.startTime).Seconds()
	result["goroutines"] = runtime.NumGoroutine()
	for name, ptr := range m.counters {
		result["counter_"+name] = atomic.LoadInt64(ptr)
	}
	for name, ptr := range m.gauges {
		result["gauge_"+name] = math.Float64frombits(atomic.LoadUint64(ptr))
	}
	for name, h := range m.histograms {
		h.mu.Lock()
		result["histogram_"+name] = map[string]interface{}{
			"count": h.count,
			"sum":   h.sum,
			"min":   h.min,
			"max":   h.max,
			"avg":   h.sum / math.Max(1, float64(h.count)),
		}
		h.mu.Unlock()
	}
	return result
}

// FormatMetrics returns metrics as formatted text.
func (m *MetricsCollector) FormatMetrics() string {
	snap := m.Snapshot()
	var buf string
	buf = fmt.Sprintf("=== Metrics Snapshot ===\n")
	buf += fmt.Sprintf("Uptime: %.0f seconds\n", snap["uptime_seconds"])
	buf += fmt.Sprintf("Goroutines: %d\n", snap["goroutines"])
	for name, ptr := range m.counters {
		buf += fmt.Sprintf("counter %s: %d\n", name, atomic.LoadInt64(ptr))
	}
	for name, ptr := range m.gauges {
		buf += fmt.Sprintf("gauge %s: %.2f\n", name, math.Float64frombits(atomic.LoadUint64(ptr)))
	}
	return buf
}