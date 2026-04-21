package metrics

import (
	"runtime"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// stopCh is used to gracefully stop the memory usage goroutine.
var (
	stopCh   chan struct{}
	initOnce sync.Once
)

type InstrumentedRWMutex struct {
	mu sync.RWMutex
}

type OpType string

const (
	OpGet             OpType = "get"
	OpSet             OpType = "set"
	OpDel             OpType = "del"
	OpExpire          OpType = "expire"
	OpReset           OpType = "reset"
	OpSearch          OpType = "search"
	OpSearchWildcard  OpType = "search_wildcard"
	OpSearchRegex     OpType = "search_regex"
	OpSizeCalculation OpType = "size_calculation"
	OpCleanup         OpType = "cleanup"
)

type LockOp string

const (
	LockRead  LockOp = "read"
	LockWrite LockOp = "write"
)

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

	// Persistence metrics
	PersistenceOpTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "cache_persistence_op_total",
			Help: "Total number of persistence operations",
		},
		[]string{"op", "status"}, // op: dump/load, status: success/error
	)

	PersistenceDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "cache_persistence_duration_seconds",
			Help:    "Duration of persistence operations",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0, 5.0},
		},
		[]string{"op"}, // dump/load
	)

	DumpKeysGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "cache_dump_keys_total",
			Help: "Number of keys in the last dump",
		},
	)

	LoadKeysGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "cache_load_keys_total",
			Help: "Number of keys in the last load",
		},
		[]string{"state"}, // loaded/skipped
	)
)

func Init() {
	initOnce.Do(func() {
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
			PersistenceOpTotal,
			PersistenceDuration,
			DumpKeysGauge,
			LoadKeysGauge,
		)

		stopCh = make(chan struct{})
		go updateMemoryUsage()
	})
}

// Close stops the background metrics goroutines.
func Close() {
	if stopCh != nil {
		select {
		case <-stopCh:
			// Already closed
			return
		default:
			close(stopCh)
		}
	}
}

func updateMemoryUsage() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-stopCh:
			return
		case <-ticker.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			MemoryUsage.WithLabelValues("alloc").Set(float64(m.Alloc))
			MemoryUsage.WithLabelValues("total").Set(float64(m.Sys))
		}
	}
}

func ObserveOperation(duration time.Duration, opType OpType) {
	OperationDuration.WithLabelValues(string(opType)).Observe(duration.Seconds())
	RequestDuration.WithLabelValues(string(opType)).Observe(duration.Seconds())
}

func IncOperation(opType OpType, success bool) {
	status := "success"
	if !success {
		status = "failure"
	}

	OperationCount.WithLabelValues(string(opType), status).Inc()
	RequestsTotal.WithLabelValues(string(opType), status).Inc()
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

// Persistence metrics helpers
func IncPersistenceOp(op, status string) { PersistenceOpTotal.WithLabelValues(op, status).Inc() }
func ObservePersistenceDuration(op string, d float64) {
	PersistenceDuration.WithLabelValues(op).Observe(d)
}
func SetDumpKeys(n int64) { DumpKeysGauge.Set(float64(n)) }
func SetLoadKeys(loaded, skipped int64) {
	LoadKeysGauge.WithLabelValues("loaded").Set(float64(loaded))
	LoadKeysGauge.WithLabelValues("skipped").Set(float64(skipped))
}

func (m *InstrumentedRWMutex) Lock(opType LockOp) {
	start := time.Now()
	m.mu.Lock()
	duration := time.Since(start)
	MutexWait.WithLabelValues(string(opType)).Observe(duration.Seconds())
}

func (m *InstrumentedRWMutex) Unlock() {
	m.mu.Unlock()
}

func (m *InstrumentedRWMutex) RLock(opType LockOp) {
	start := time.Now()
	m.mu.RLock()
	duration := time.Since(start)
	MutexWait.WithLabelValues(string(opType)).Observe(duration.Seconds())
}

func (m *InstrumentedRWMutex) RUnlock() {
	m.mu.RUnlock()
}
