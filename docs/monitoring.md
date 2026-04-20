# 监控指标规范

## 缓存指标
- 请求计数：`simple_cache_requests_total{op="get|set|del|search|expire|reset"}`
- 请求耗时：`simple_cache_request_duration_seconds_bucket{op=...}` 直方图
- 键总数：`simple_cache_keys_total`
- 过期队列大小：`simple_cache_expiration_heap_size`
- 操作耗时：`cache_operation_duration_seconds{operation=...}`
- 操作计数：`cache_operation_total{operation=...,status=...}`
- 缓存大小：`cache_size_bytes{type=...}`
- 锁等待时间：`cache_mutex_wait_seconds{op_type=...}`

## Raft 指标
- `raft_role{node_id}` 当前角色
- `raft_commit_index`、`raft_last_applied`
- `raft_leader_changes_total`
- `raft_append_entries_latency_seconds_bucket`
- `simple_cache_peers_total` 集群 Peer 数量

> 当前尚未暴露独立的 snapshot/compaction 专用指标；可通过 `raft_commit_index`、`raft_last_applied` 与日志观察恢复/压缩行为。

## 持久化指标
- `cache_persistence_op_total{op="dump|load",status="success|error"}` 持久化操作计数
- `cache_persistence_duration_seconds{op="dump|load"}` 持久化操作耗时直方图
- `cache_dump_keys_total` 最近一次 Dump 的 key 数
- `cache_load_keys_total{state="loaded|skipped"}` 最近一次 Load 的 key 数

## 系统指标
- `process_memory_bytes{state="alloc|total"}` 进程内存使用
