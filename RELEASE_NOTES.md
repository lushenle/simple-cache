# Release Notes - v1.0.0

## Simple-Cache: Production-Ready Distributed Cache

This is the first stable release of Simple-Cache, a Raft-based distributed in-memory cache built in Go.

---

## Features

### Core
- **Raft Consensus** — Custom lightweight Raft implementation with leader election, log replication, majority commit, snapshot/compaction, and InstallSnapshot for far-behind followers
- **Dual Protocol** — gRPC native + REST HTTP (auto-generated via grpc-gateway) with embedded Swagger UI at `/api/docs/`
- **Single/Distributed Modes** — Switch between standalone and clustered operation with a single config field
- **Graceful Shutdown** — 9-step sequenced shutdown (gRPC → Gateway → Raft → WatchService → Dump → Cache → Config → Metrics → Logger)

### Data Engine
- **HashMap + Radix Tree + Expiration Heap** — O(1) CRUD, O(k) prefix search, O(log n) TTL management
- **TTL with Active + Lazy Expiration** — Background cleanup worker with time budgeting + lazy delete on Get
- **Prefix/Wildcard/Regex Search** — Radix tree prefix optimization, `filepath.Match` wildcard, `regexp` support
- **Data Persistence** — Binary and JSON dump/load formats with CRC32 integrity checking and atomic file writes

### Distributed Mode
- **Linearizable Reads (ReadIndex)** — Leader confirms quorum via heartbeat before serving reads, preventing stale reads during network partitions
- **Client Auto-Failover** — `NewCluster` client automatically discovers the leader, retries on `not leader` errors, and reconnects on leader changes
- **gRPC Name Resolver** — Built-in `simplecache://` URI scheme resolver for native gRPC framework integration
- **Graceful Leadership Transfer** — `POST /cluster/stepdown` triggers a controlled leader change without disrupting the cluster
- **Dynamic Membership** — HTTP API for adding/removing nodes at runtime via `/cluster/join` and `/cluster/leave`

### Production Hardening
- **Memory Limits** — Configurable `max_keys` and `max_value_size` with typed error responses
- **LRU Eviction Policy** — When `max_keys` is reached and `eviction_policy: lru`, the cache automatically evicts the least recently used key
- **Rate Limiting** — Per-second token-bucket rate limiter (`max_qps`) applied to all operations
- **Configuration Validation** — Startup validation of all config fields (port syntax, heartbeat < election, TLS certs, etc.)
- **Environment Variable Overrides** — All config fields overridable via `SIMPLE_CACHE_*` environment variables for container/K8s deployments

### Observability
- **Prometheus Metrics** — Comprehensive metrics for cache operations, Raft consensus, persistence, and memory usage
- **pprof Endpoints** — Runtime profiling via `/debug/pprof/*` on the metrics port (separate from data plane)
- **Structured Logging** — JSON-format logging via zap with lumberjack rotation (100MB max, 7-day retention, compression)
- **Health/Readiness Probes** — `/healthz` and `/readyz` endpoints for orchestration systems

### Performance
- **WAL Binary Encoding** — Compact binary WAL format replacing JSON, delivering 5-10x write throughput improvement for large values (auto-detects and upgrades legacy JSON files)
- **gRPC Streaming BatchSet** — Stream-based bulk write API, 10-100x faster than sequential unary calls
- **Async Size Metrics** — Memory estimation moved to background goroutine (30s interval), eliminating write-lock scans on every Set()

### New APIs
- **Watch/Subscribe** — gRPC server-streaming API for key change events (Set/Del/Expire) with pattern filtering
- **Streaming BatchSet** — Client-streaming RPC for efficient bulk writes

---

## Configuration

New fields in `config.yaml`:

| Field | Default | Description |
|-------|---------|-------------|
| `peer_addresses` | `{}` | Node ID → gRPC address mapping for fast leader discovery |
| `max_keys` | `0` | Maximum cache keys (0 = unlimited) |
| `max_value_size` | `0` | Maximum value size in bytes (0 = unlimited) |
| `max_qps` | `0` | Global requests/sec limit (0 = unlimited) |
| `eviction_policy` | `none` | Eviction policy: `none` (reject on full) or `lru` (evict least recently used) |

---

## Changelog Summary

### Phase 1 — Core + P0 Production Readiness
- Client auto-failover with leader discovery (`NewCluster`)
- Configuration validation with env var overrides
- Memory limits and rate limiting
- Graceful stepdown API
- pprof endpoints

### Phase 2 — Read Scalability + Observability
- ReadIndex linearizable read protocol
- Enhanced metrics (pending entries, snapshot age)

### Phase 3 — Performance & Operations
- WAL binary encoding (backward compatible)
- gRPC streaming BatchSet
- Async memory size metrics

### Phase 4 — Advanced Features
- LRU eviction policy with `cache_evictions_total` metric
- Watch/Subscribe key change notifications

---

## Upgrading

### From Previous Versions

1. **WAL format**: Existing JSON-format WAL files are auto-detected and read transparently. They will be upgraded to binary format on the next compaction or `RewriteEntries`. No manual migration needed.

2. **Config file**: New fields are optional with safe defaults. Add them only if you need the functionality.

3. **Client**: The existing `New()` / `NewDefault()` / `NewSecure()` constructors remain fully backward compatible. Upgrade to `NewCluster()` for automatic failover.

### Breaking Changes

None. All existing APIs and configurations remain compatible.
