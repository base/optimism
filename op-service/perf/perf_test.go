package perf

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewTimer(t *testing.T) {
	timer := NewTimer("test_operation")
	require.NotNil(t, timer)
	assert.Equal(t, "test_operation", timer.name)
	assert.False(t, timer.stopped.Load())
}

func TestTimerElapsed(t *testing.T) {
	timer := NewTimer("test_operation")
	time.Sleep(10 * time.Millisecond)
	elapsed := timer.Elapsed()
	assert.GreaterOrEqual(t, elapsed, 10*time.Millisecond)
}

func TestTimerDone(t *testing.T) {
	timer := NewTimer("test_operation")
	time.Sleep(5 * time.Millisecond)
	elapsed := timer.Done()

	assert.GreaterOrEqual(t, elapsed, 5*time.Millisecond)
	assert.True(t, timer.stopped.Load())
}

func TestTimerDoneIdempotent(t *testing.T) {
	timer := NewTimer("test_operation")
	first := timer.Done()
	second := timer.Done()
	third := timer.Done()

	assert.NotZero(t, first)
	assert.Zero(t, second, "Second Done() should return zero")
	assert.Zero(t, third, "Third Done() should return zero")
}

func TestTimerWithThreshold(t *testing.T) {
	threshold := 50 * time.Millisecond
	timer := NewTimerWithThreshold("test_operation", threshold)

	assert.Equal(t, threshold, timer.threshold)
}

func TestTimerWithOptions(t *testing.T) {
	logger := log.New()
	timer := NewTimer("test_operation",
		WithThreshold(100*time.Millisecond),
		WithLogger(logger),
		WithTag("key1", "value1"),
		WithTag("key2", "value2"),
	)

	assert.Equal(t, 100*time.Millisecond, timer.threshold)
	assert.Equal(t, logger, timer.logger)
	assert.Equal(t, "value1", timer.tags["key1"])
	assert.Equal(t, "value2", timer.tags["key2"])
}

func TestDoneWithResult(t *testing.T) {
	timer := NewTimer("test_operation")
	result := "test_result"

	duration, res := timer.DoneWithResult(result)

	assert.NotZero(t, duration)
	assert.Equal(t, "test_result", res)
}

func TestMetrics(t *testing.T) {
	m := NewMetrics()

	// Record some timings
	m.Record("operation_a", 10*time.Millisecond)
	m.Record("operation_a", 20*time.Millisecond)
	m.Record("operation_a", 30*time.Millisecond)
	m.Record("operation_b", 5*time.Millisecond)

	// Check operation_a metrics
	entryA := m.Get("operation_a")
	require.NotNil(t, entryA)
	assert.Equal(t, int64(3), entryA.Count)
	assert.Equal(t, 60*time.Millisecond, entryA.TotalTime)
	assert.Equal(t, 10*time.Millisecond, entryA.MinTime)
	assert.Equal(t, 30*time.Millisecond, entryA.MaxTime)
	assert.Equal(t, 30*time.Millisecond, entryA.LastTime)
	assert.Equal(t, 20*time.Millisecond, entryA.Average())

	// Check operation_b metrics
	entryB := m.Get("operation_b")
	require.NotNil(t, entryB)
	assert.Equal(t, int64(1), entryB.Count)

	// Check non-existent operation
	entryC := m.Get("operation_c")
	assert.Nil(t, entryC)
}

func TestMetricsWithThreshold(t *testing.T) {
	m := NewMetrics()

	threshold := 15 * time.Millisecond

	// Record some timings with threshold
	m.RecordWithThreshold("operation", 10*time.Millisecond, threshold) // Under
	m.RecordWithThreshold("operation", 20*time.Millisecond, threshold) // Over
	m.RecordWithThreshold("operation", 12*time.Millisecond, threshold) // Under
	m.RecordWithThreshold("operation", 25*time.Millisecond, threshold) // Over

	entry := m.Get("operation")
	require.NotNil(t, entry)
	assert.Equal(t, int64(4), entry.Count)
	assert.Equal(t, int64(2), entry.Violations)
}

func TestMetricsAll(t *testing.T) {
	m := NewMetrics()

	m.Record("op1", 10*time.Millisecond)
	m.Record("op2", 20*time.Millisecond)
	m.Record("op3", 30*time.Millisecond)

	all := m.All()

	assert.Len(t, all, 3)
	assert.NotNil(t, all["op1"])
	assert.NotNil(t, all["op2"])
	assert.NotNil(t, all["op3"])
}

func TestMetricsReset(t *testing.T) {
	m := NewMetrics()

	m.Record("operation", 10*time.Millisecond)
	assert.NotNil(t, m.Get("operation"))

	m.Reset()
	assert.Nil(t, m.Get("operation"))
	assert.Len(t, m.All(), 0)
}

func TestMetricsSummary(t *testing.T) {
	m := NewMetrics()

	// Empty metrics
	summary := m.Summary()
	assert.Equal(t, "No metrics recorded", summary)

	// With some data
	m.Record("test_op", 100*time.Millisecond)
	summary = m.Summary()
	assert.Contains(t, summary, "Performance Metrics Summary")
	assert.Contains(t, summary, "test_op")
	assert.Contains(t, summary, "Count:")
}

func TestMetricsConcurrency(t *testing.T) {
	m := NewMetrics()
	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			m.Record("operation", time.Duration(i)*time.Millisecond)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.Get("operation")
			_ = m.All()
		}()
	}

	wg.Wait()

	entry := m.Get("operation")
	require.NotNil(t, entry)
	assert.Equal(t, int64(100), entry.Count)
}

func TestMetricEntryAverage(t *testing.T) {
	// Test zero count
	entry := &MetricEntry{Count: 0, TotalTime: 0}
	assert.Zero(t, entry.Average())

	// Test with data
	entry = &MetricEntry{Count: 4, TotalTime: 100 * time.Millisecond}
	assert.Equal(t, 25*time.Millisecond, entry.Average())
}

func TestGlobalMetrics(t *testing.T) {
	// Reset global metrics first
	GlobalMetrics().Reset()

	RecordGlobal("global_op", 50*time.Millisecond)
	RecordGlobal("global_op", 100*time.Millisecond)

	entry := GlobalMetrics().Get("global_op")
	require.NotNil(t, entry)
	assert.Equal(t, int64(2), entry.Count)
	assert.Equal(t, 150*time.Millisecond, entry.TotalTime)
}

func TestTrackContext(t *testing.T) {
	ctx := context.Background()
	timer, cancel := TrackContext(ctx, "context_op")

	require.NotNil(t, timer)
	require.NotNil(t, cancel)

	// Simulate some work
	time.Sleep(5 * time.Millisecond)

	// Cancel should stop the timer
	cancel()
	assert.True(t, timer.stopped.Load())
}

func TestMeasureFunc(t *testing.T) {
	result, duration := MeasureFunc("test_func", func() string {
		time.Sleep(5 * time.Millisecond)
		return "result"
	})

	assert.Equal(t, "result", result)
	assert.GreaterOrEqual(t, duration, 5*time.Millisecond)
}

func TestMeasureFuncErr(t *testing.T) {
	// Test successful function
	result, err, duration := MeasureFuncErr("test_func", func() (int, error) {
		time.Sleep(5 * time.Millisecond)
		return 42, nil
	})

	assert.Equal(t, 42, result)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, duration, 5*time.Millisecond)

	// Test function with error
	_, errResult, _ := MeasureFuncErr("test_func_err", func() (int, error) {
		return 0, assert.AnError
	})

	assert.Error(t, errResult)
}

// Benchmark tests
func BenchmarkTimerCreation(b *testing.B) {
	for i := 0; i < b.N; i++ {
		timer := NewTimer("benchmark_op")
		timer.Done()
	}
}

func BenchmarkMetricsRecord(b *testing.B) {
	m := NewMetrics()
	duration := 10 * time.Millisecond

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Record("benchmark_op", duration)
	}
}

func BenchmarkMetricsRecordConcurrent(b *testing.B) {
	m := NewMetrics()
	duration := 10 * time.Millisecond

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			m.Record("benchmark_op", duration)
		}
	})
}
