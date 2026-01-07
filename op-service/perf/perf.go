// Package perf provides utilities for measuring and reporting execution time
// of operations with configurable thresholds and structured logging.
//
// This package is designed for development and debugging purposes, helping
// developers identify performance bottlenecks and track execution times
// across the codebase.
//
// Example usage:
//
//	timer := perf.NewTimer("database_query")
//	defer timer.Done()
//	// ... perform operation
//
// Or with thresholds:
//
//	timer := perf.NewTimerWithThreshold("rpc_call", 100*time.Millisecond)
//	defer timer.Done() // Will log warning if exceeds 100ms
package perf

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/log"
)

// Timer measures the execution time of an operation
type Timer struct {
	name      string
	start     time.Time
	threshold time.Duration
	logger    log.Logger
	tags      map[string]string
	stopped   atomic.Bool
}

// TimerOption configures a Timer
type TimerOption func(*Timer)

// WithThreshold sets a warning threshold for the timer.
// If the operation takes longer than the threshold, a warning is logged.
func WithThreshold(d time.Duration) TimerOption {
	return func(t *Timer) {
		t.threshold = d
	}
}

// WithLogger sets a custom logger for the timer
func WithLogger(l log.Logger) TimerOption {
	return func(t *Timer) {
		t.logger = l
	}
}

// WithTag adds a tag to the timer for additional context in logs
func WithTag(key, value string) TimerOption {
	return func(t *Timer) {
		if t.tags == nil {
			t.tags = make(map[string]string)
		}
		t.tags[key] = value
	}
}

// NewTimer creates a new timer that starts immediately
func NewTimer(name string, opts ...TimerOption) *Timer {
	t := &Timer{
		name:   name,
		start:  time.Now(),
		logger: log.Root(),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// NewTimerWithThreshold creates a new timer with a warning threshold
func NewTimerWithThreshold(name string, threshold time.Duration) *Timer {
	return NewTimer(name, WithThreshold(threshold))
}

// Elapsed returns the duration since the timer started
func (t *Timer) Elapsed() time.Duration {
	return time.Since(t.start)
}

// Done stops the timer and logs the elapsed time.
// It is safe to call Done multiple times; only the first call has effect.
func (t *Timer) Done() time.Duration {
	if t.stopped.Swap(true) {
		return 0 // Already stopped
	}

	elapsed := t.Elapsed()

	// Build log context
	ctx := []interface{}{
		"operation", t.name,
		"duration_ms", elapsed.Milliseconds(),
		"duration", elapsed.String(),
	}

	// Add tags to context
	for k, v := range t.tags {
		ctx = append(ctx, k, v)
	}

	// Log based on threshold
	if t.threshold > 0 && elapsed > t.threshold {
		t.logger.Warn("Operation exceeded threshold",
			append(ctx, "threshold_ms", t.threshold.Milliseconds())...)
	} else {
		t.logger.Debug("Operation completed", ctx...)
	}

	return elapsed
}

// DoneWithResult stops the timer and returns both duration and any provided result
func (t *Timer) DoneWithResult(result interface{}) (time.Duration, interface{}) {
	return t.Done(), result
}

// Metrics provides aggregated timing statistics for operations
type Metrics struct {
	mu      sync.RWMutex
	entries map[string]*MetricEntry
	logger  log.Logger
}

// MetricEntry holds aggregated statistics for a single operation type
type MetricEntry struct {
	Name       string
	Count      int64
	TotalTime  time.Duration
	MinTime    time.Duration
	MaxTime    time.Duration
	LastTime   time.Duration
	Threshold  time.Duration
	Violations int64 // Number of times threshold was exceeded
}

// NewMetrics creates a new metrics collector
func NewMetrics() *Metrics {
	return &Metrics{
		entries: make(map[string]*MetricEntry),
		logger:  log.Root(),
	}
}

// SetLogger sets the logger for the metrics collector
func (m *Metrics) SetLogger(l log.Logger) {
	m.logger = l
}

// Record adds a timing measurement for the given operation
func (m *Metrics) Record(name string, duration time.Duration) {
	m.RecordWithThreshold(name, duration, 0)
}

// RecordWithThreshold adds a timing measurement with threshold checking
func (m *Metrics) RecordWithThreshold(name string, duration time.Duration, threshold time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	entry, exists := m.entries[name]
	if !exists {
		entry = &MetricEntry{
			Name:      name,
			MinTime:   duration,
			MaxTime:   duration,
			Threshold: threshold,
		}
		m.entries[name] = entry
	}

	entry.Count++
	entry.TotalTime += duration
	entry.LastTime = duration

	if duration < entry.MinTime {
		entry.MinTime = duration
	}
	if duration > entry.MaxTime {
		entry.MaxTime = duration
	}
	if threshold > 0 && duration > threshold {
		entry.Violations++
	}
}

// Get returns the metrics entry for the given operation name
func (m *Metrics) Get(name string) *MetricEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entries[name]
}

// All returns all recorded metrics entries
func (m *Metrics) All() map[string]*MetricEntry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make(map[string]*MetricEntry, len(m.entries))
	for k, v := range m.entries {
		// Return a copy to prevent races
		entryCopy := *v
		result[k] = &entryCopy
	}
	return result
}

// Reset clears all recorded metrics
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = make(map[string]*MetricEntry)
}

// Summary returns a human-readable summary of all metrics
func (m *Metrics) Summary() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.entries) == 0 {
		return "No metrics recorded"
	}

	var result string
	result += fmt.Sprintf("Performance Metrics Summary (%d operations)\n", len(m.entries))
	result += "============================================\n"

	for name, entry := range m.entries {
		avgTime := time.Duration(0)
		if entry.Count > 0 {
			avgTime = entry.TotalTime / time.Duration(entry.Count)
		}

		result += fmt.Sprintf("\n%s:\n", name)
		result += fmt.Sprintf("  Count:    %d\n", entry.Count)
		result += fmt.Sprintf("  Avg:      %v\n", avgTime)
		result += fmt.Sprintf("  Min:      %v\n", entry.MinTime)
		result += fmt.Sprintf("  Max:      %v\n", entry.MaxTime)
		result += fmt.Sprintf("  Total:    %v\n", entry.TotalTime)

		if entry.Threshold > 0 {
			result += fmt.Sprintf("  Threshold: %v (violated %d times)\n",
				entry.Threshold, entry.Violations)
		}
	}

	return result
}

// LogSummary logs the metrics summary
func (m *Metrics) LogSummary() {
	for name, entry := range m.All() {
		avgTime := time.Duration(0)
		if entry.Count > 0 {
			avgTime = entry.TotalTime / time.Duration(entry.Count)
		}

		m.logger.Info("Performance metric",
			"operation", name,
			"count", entry.Count,
			"avg_ms", avgTime.Milliseconds(),
			"min_ms", entry.MinTime.Milliseconds(),
			"max_ms", entry.MaxTime.Milliseconds(),
			"total_ms", entry.TotalTime.Milliseconds(),
			"violations", entry.Violations,
		)
	}
}

// Average returns the average duration for the given operation
func (e *MetricEntry) Average() time.Duration {
	if e.Count == 0 {
		return 0
	}
	return e.TotalTime / time.Duration(e.Count)
}

// Global metrics instance for convenience
var globalMetrics = NewMetrics()

// GlobalMetrics returns the global metrics instance
func GlobalMetrics() *Metrics {
	return globalMetrics
}

// RecordGlobal records a timing to the global metrics instance
func RecordGlobal(name string, duration time.Duration) {
	globalMetrics.Record(name, duration)
}

// TrackContext creates a timer that can be used with context cancellation
func TrackContext(ctx context.Context, name string, opts ...TimerOption) (*Timer, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	timer := NewTimer(name, opts...)

	// Create a wrapper cancel that also stops the timer
	wrappedCancel := func() {
		timer.Done()
		cancel()
	}

	return timer, wrappedCancel
}

// MeasureFunc wraps a function and returns its execution time
func MeasureFunc[T any](name string, fn func() T, opts ...TimerOption) (T, time.Duration) {
	timer := NewTimer(name, opts...)
	result := fn()
	elapsed := timer.Done()
	return result, elapsed
}

// MeasureFuncErr wraps a function that returns an error and measures its execution time
func MeasureFuncErr[T any](name string, fn func() (T, error), opts ...TimerOption) (T, error, time.Duration) {
	timer := NewTimer(name, opts...)
	result, err := fn()
	elapsed := timer.Done()
	return result, err, elapsed
}
