# Project Roadmap

> Vision: A lightweight, production-grade distributed cache that is simple to deploy, self-healing, and observable.

## Current Status (Phase 1 + Phase 2 Core — Complete)

The project is a production-hardened Raft-based distributed cache with:
- Single-node and clustered modes
- gRPC + REST dual protocol
- Client auto-failover (NewCluster) + leader discovery
- Leader Lease → ReadIndex protocol for linearizable reads
- Memory limits (max_keys, max_value_size) + value size validation
- Configuration validation with env var overrides
- Graceful leadership transfer (POST /cluster/stepdown)
- Rate limiting (token-bucket per second)
- pprof endpoints on metrics port
- Prometheus monitoring with enhanced metrics
- Structured logging (zap + lumberjack)

### Known Limitations
- Follower reads not yet implemented (leader still single read bottleneck)
- No OpenTelemetry distributed tracing
- WAL uses JSON encoding (slow for large values)
- No eviction policy beyond TTL
- Single mutex serializes all Raft operations

## Roadmap

```
Phase 1     Phase 2.1            Phase 2.3            Phases 3-4
┌──────┐   ┌──────────┐       ┌──────────┐         ┌──────────┐
│ Core │──▶│ ReadIndex│──────▶│ Enhanced │────────▶│  Perf &  │
│ + P0 │   │ Protocol │       │ Metrics  │         │ Features │
│ + P1 │   └──────────┘       └──────────┘         └──────────┘
└──────┘
  Done            Done               Done              Next
```

---

## Phase 2: Read Scalability + Observability (3-5 weeks)

### 2.1 Follower Reads via ReadIndex

**Problem**: Currently all reads go to the leader. Adding followers does not increase read capacity.

**Solution**: Implement Raft's ReadIndex protocol:
- Leader tracks `commitIndex` at read time and sends a lightweight heartbeat to confirm leadership
- Once confirmed, leader returns `commitIndex` to the caller
- Follower waits until `lastApplied >= readIndex`, then serves the read locally

Implementation plan:
1. Add `ReadIndex(ctx) (uint64, error)` to Raft Node
2. Leader sends no-op heartbeat, advances commitIndex
3. Follower advances to ReadIndex, serves reads
4. Expose read policy: `leader` (current) or `follower` (new)

**Files**: `pkg/raft/node.go`, `pkg/server/server.go`, `pkg/proto/cache.proto`

**Impact**: Read throughput scales linearly with cluster size.

### 2.2 OpenTelemetry Tracing

**Problem**: No way to trace a request from client → gRPC → Raft → storage.

**Solution**: 
1. Add OpenTelemetry SDK dependency
2. Instrument gRPC interceptors (server + client)
3. Add spans for: Set/Get/Del, raft.Submit, raft replication, WAL append
4. Export traces via OTLP (configurable endpoint)

**Files**: `pkg/server/server.go`, `pkg/raft/node.go`, `pkg/cmd/main.go`, `go.mod`

**Impact**: Debug slow requests, identify bottlenecks, trace cross-node operations.

### 2.3 Enhanced Metrics

**Problem**: Some metric gaps and N+1 query problem.

**Solution**:
1. Add per-peer RTT histogram (`raft_peer_rtt_seconds`)
2. Add snapshot age gauge (`raft_snapshot_age_seconds`)
3. Add pending raft entries gauge (`raft_pending_entries`)
4. Add operation latency p50/p95/p99 via existing histograms
5. Add slow query counter (>100ms, >500ms, >1s buckets)

**Files**: `pkg/metrics/metrics.go`, `pkg/raft/node.go`, `pkg/server/server.go`

---

## Phase 3: Performance & Operations (3-4 weeks)

### 3.1 WAL Binary Encoding

**Problem**: WAL uses JSON encoding (slow, bloated for binary values).

**Solution**: 
1. Define compact binary WAL format (protobuf or flatbuffers)
2. Header: magic, version, crc32
3. Entries: length-prefixed protobuf messages
4. Backward compatibility: detect format on LoadEntries()

**Files**: `pkg/raft/storage.go`, `pkg/raft/types.go`

**Impact**: 5-10x WAL write throughput improvement for large values.

### 3.2 gRPC Streaming for Batch Operations

**Problem**: `BatchSet` sends N sequential unary RPCs.

**Solution**:
1. Add `BatchSet` streaming RPC to proto: `rpc BatchSet(stream BatchSetRequest) returns (BatchSetResponse)`
2. Client sends batched keys in a single stream
3. Server processes and acknowledges as a single raft entry (or batched entries)

**Files**: `pkg/proto/cache.proto`, `pkg/pb/`, `pkg/server/server.go`, `pkg/client/client.go`

**Impact**: 10-100x throughput improvement for batch writes.

### 3.3 Async Size Metrics

**Problem**: `updateSizeMetrics()` runs under write lock, blocking all operations.

**Solution**: 
1. Move memory estimation to background goroutine (every 30s)
2. Use atomic counters for approximate key count
3. Remove the write-lock scan

**Files**: `pkg/cache/metrics.go`, `pkg/cache/cache.go`

### 3.4 Raft Mutex Sharding

**Problem**: Single `sync.Mutex` serializes all Raft operations.

**Solution**: 
- Split into three mutexes:
  - `logMu` — protects log entries, commit/last applied indices
  - `metaMu` — protects term, votedFor, peer state
  - `waiterMu` — protects applyWaiter map

**Files**: `pkg/raft/node.go`

**Impact**: Higher write throughput, especially during replication.

### 3.5 Chaos Tests

**Problem**: No fault-injection testing.

**Solution**:
1. Network partition simulation (block specific peers)
2. Leader election under continuous load
3. Disk write failure simulation (WAL write errors)
4. Rolling restart validation (no data loss)

**Files**: `test/e2e/chaos_test.go` (new)

---

## Phase 4: Advanced Features (backlog)

### 4.1 Key Eviction Policies

**Problem**: Only TTL-based eviction. No LRU/LFU for memory management.

**Solution**: Add configurable eviction policy:
- `ttl-only` (current) — never evict until TTL expires
- `lru` — evict least recently used when at max_keys
- `lfu` — evict least frequently used when at max_keys

**Files**: `pkg/cache/eviction.go`, `pkg/cache/cache.go`

### 4.2 Namespacing / Multi-Tenancy

Allow multiple logical caches within a single cluster, isolated by key prefix:
- `namespace` config per application
- Metrics broken down by namespace
- Auth token per namespace

### 4.3 Watch / Subscribe

Clients can subscribe to key changes:
- gRPC server-streaming RPC: `rpc Watch(WatchRequest) returns (stream WatchEvent)`
- Server publishes events on Set/Del/Expire
- Useful for cache invalidation, event-driven architectures

### 4.4 CLI Tool

A command-line tool similar to Redis CLI:
```
simple-cache-cli set mykey myvalue --ttl 10m
simple-cache-cli get mykey
simple-cache-cli search "user:*"
```

---

## Effort Summary

| Phase | Items | Est. Time | Risk | Value |
|-------|-------|-----------|------|-------|
| **2** | Follower Reads + OTel + Better Metrics | 3-5 weeks | Medium (ReadIndex correctness) | High |
| **3** | WAL binary, gRPC streaming, async metrics, mutex split, chaos | 3-4 weeks | Low-Medium | Medium |
| **4** | Eviction, Namespacing, Watch, CLI | 4-6 weeks | Low | Medium |

## Recommendation

**Start with Phase 2**. Read scalability is the single biggest architectural limitation — without it, the cluster can't grow capacity by adding nodes. Observability (tracing + better metrics) is essential for operating in production, especially during the learning phase after initial deployment.

Phase 2 is also a prerequisite for Phase 3 — you can't optimize what you can't measure, and tracing will reveal which bottlenecks are worth optimizing first.
