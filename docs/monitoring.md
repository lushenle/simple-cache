# 监控指标规范

- 请求计数：`simple_cache_requests_total{op="get|set|del|search|expire|reset"}`
- 请求耗时：`simple_cache_request_duration_seconds_bucket{op=...}` 直方图
- 键总数：`simple_cache_keys_total`
- 过期队列大小：`simple_cache_expiration_heap_size`
- Raft：
  - `raft_role{node_id}` 当前角色
  - `raft_commit_index`、`raft_last_applied`
  - `raft_leader_changes_total`
  - `raft_append_entries_latency_seconds_bucket`
