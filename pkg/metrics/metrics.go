package metrics

import (
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type InstrumentedRWMutex struct {
	mu sync.RWMutex
}

var (
	// OperationDuration latency metrics
	OperationDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_operation_duration_seconds",
			Help:    "Time spent executing cache operations",
			Buckets: []float64{0.0001, 0.0005, 0.001, 0.005, 0.01, 0.05},
		},
		[]string{"operation"},
	)

	// OperationCount qps metrics
	OperationCount = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_operation_total",
			Help: "Total number of cache operations",
		},
		[]string{"operation", "status"},
	)

	CacheSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cache_size_bytes",
			Help: "Total memory used by the cache",
		},
		[]string{"type"}, // memory/item_count
	)

	MemoryUsage = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "process_memory_bytes",
			Help: "Process memory usage in bytes",
		},
		[]string{"state"}, // 标签名称
	)

	MutexWait = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_mutex_wait_seconds",
			Help:    "Mutex acquisition latency",
			Buckets: prometheus.ExponentialBuckets(1e-6, 2, 20), // 1μs ~ 1s
		},
		[]string{"op_type"}, // read/write
	)
)

func Init() {
	prometheus.MustRegister(
		OperationDuration,
		OperationCount,
		CacheSize,
		MemoryUsage,
		MutexWait,
	)

	go updateMemoryUsage()
}

func updateMemoryUsage() {
	for {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		MemoryUsage.WithLabelValues("alloc").Set(float64(m.Alloc))
		MemoryUsage.WithLabelValues("total").Set(float64(m.Sys))

		time.Sleep(5 * time.Second)
	}
}

func ObserveOperation(duration time.Duration, opType string) {
	OperationDuration.WithLabelValues(opType).Observe(duration.Seconds())
}

func IncOperation(opType string, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	OperationCount.WithLabelValues(opType, status).Inc()
}

func (m *InstrumentedRWMutex) Lock(opType string) {
	start := time.Now()
	m.mu.Lock()
	duration := time.Since(start)
	MutexWait.WithLabelValues(opType).Observe(duration.Seconds())
}

func (m *InstrumentedRWMutex) Unlock() {
	m.mu.Unlock()
}

func (m *InstrumentedRWMutex) RLock(opType string) {
	start := time.Now()
	m.mu.RLock()
	duration := time.Since(start)
	MutexWait.WithLabelValues(opType).Observe(duration.Seconds())
}

func (m *InstrumentedRWMutex) RUnlock() {
	m.mu.RUnlock()
}
