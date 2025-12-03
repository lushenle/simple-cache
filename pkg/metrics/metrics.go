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
	// New metrics per docs/monitoring.md
	RequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "simple_cache_requests_total",
			Help: "Total number of cache requests",
		},
		[]string{"op", "status"},
	)

	RequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "simple_cache_request_duration_seconds",
			Help:    "Latency of cache requests",
			Buckets: prometheus.ExponentialBuckets(1e-5, 2, 15),
		},
		[]string{"op"},
	)

	KeysTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "simple_cache_keys_total",
			Help: "Total number of keys in cache",
		},
	)

	ExpirationHeapSize = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "simple_cache_expiration_heap_size",
			Help: "Number of entries in expiration heap",
		},
	)

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

	// Raft metrics
	RaftRole = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "raft_role",
			Help: "Current Raft role of node (label 'role' set to 1 for current role)",
		},
		[]string{"node_id", "role"},
	)

	RaftCommitIndex = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "raft_commit_index",
			Help: "Current Raft commit index",
		},
	)

	RaftLastApplied = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "raft_last_applied",
			Help: "Last applied log index",
		},
	)

	RaftLeaderChanges = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "raft_leader_changes_total",
			Help: "Total leader role changes",
		},
	)

	RaftAppendEntriesLatency = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "raft_append_entries_latency_seconds",
			Help:    "Latency of AppendEntries broadcast",
			Buckets: prometheus.ExponentialBuckets(1e-4, 2, 15),
		},
	)

	PeersTotal = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "simple_cache_peers_total",
			Help: "Number of peers in the raft cluster",
		},
	)
)

func Init() {
	prometheus.MustRegister(
		RequestsTotal,
		RequestDuration,
		KeysTotal,
		ExpirationHeapSize,
		OperationDuration,
		OperationCount,
		CacheSize,
		MemoryUsage,
		MutexWait,
		RaftRole,
		RaftCommitIndex,
		RaftLastApplied,
		RaftLeaderChanges,
		RaftAppendEntriesLatency,
		PeersTotal,
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
	RequestDuration.WithLabelValues(opType).Observe(duration.Seconds())
}

func IncOperation(opType string, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	OperationCount.WithLabelValues(opType, status).Inc()
	RequestsTotal.WithLabelValues(opType, status).Inc()
}

func UpdateKeysTotal(n int) {
	KeysTotal.Set(float64(n))
}

func UpdateExpirationHeapSize(n int) {
	ExpirationHeapSize.Set(float64(n))
}

// Raft helpers
func SetRaftRole(nodeID, role string) {
	// Set 1 for current role; other role series are not touched here
	RaftRole.WithLabelValues(nodeID, role).Set(1)
}

func SetRaftCommitIndex(v uint64)                 { RaftCommitIndex.Set(float64(v)) }
func SetRaftLastApplied(v uint64)                 { RaftLastApplied.Set(float64(v)) }
func IncRaftLeaderChanges()                       { RaftLeaderChanges.Inc() }
func ObserveAppendEntriesLatency(d time.Duration) { RaftAppendEntriesLatency.Observe(d.Seconds()) }
func SetPeersTotal(n int)                         { PeersTotal.Set(float64(n)) }

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
