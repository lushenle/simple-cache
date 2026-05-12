# 代码质量评估报告

- 静态分析：`go vet ./...` 无告警
- 测试结果：`go test -race ./...` 全部通过
- CI: `make ci` 包含 vet + test + test-race，通过 GitHub Actions 在 push/PR 时自动执行

## 模块概览

| 模块 | 路径 | 职责 |
|------|------|------|
| `cache` | `pkg/cache/` | 缓存引擎核心：CRUD、TTL、LRU 淘汰、前缀/正则搜索、Dump/Load 持久化 |
| `server` | `pkg/server/` | gRPC 服务层 + 认证中间件 + WatchService 发布/订阅 |
| `raft` | `pkg/raft/` | 自研 Raft：选举、日志复制、ReadIndex、snapshot/compaction、成员变更 |
| `fsm` | `pkg/fsm/` | 有限状态机：Apply/Snapshot/RestoreSnapshot |
| `command` | `pkg/command/` | 命令定义 + codec 编解码 |
| `client` | `pkg/client/` | gRPC 客户端 SDK：自动 Leader 发现/切主/重试、Watch 自动重连、BatchSetStream |
| `config` | `pkg/config/` | YAML 加载、AtomicConfig、Watcher 热重载 |
| `metrics` | `pkg/metrics/` | Prometheus 指标 + InstrumentedRWMutex |
| `log` | `pkg/log/` | zap 日志 + lumberjack 轮转 + gRPC/HTTP 中间件日志 |
| `resolver` | `pkg/client/resolver/` | gRPC Name Resolver (`simplecache://` 协议) |

## 并发热点

- `pkg/cache/*` 的 `InstrumentedRWMutex` 是主要竞争点，写操作持有写锁期间会阻塞所有读写
- `cache_mutex_wait_seconds` 指标已覆盖，可实时监控锁等待
- 过期清理 Worker 使用时间预算（`cleanupInterval / 20`）避免长时间持锁
- Get 操作的惰性删除需要"释放读锁 → 获取写锁"升级，存在双重检查逻辑

## 已知风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| Dump 操作持有写锁，大缓存可能导致读写延迟 | `cache_dump_keys_total` / `cache_persistence_duration_seconds` 监控；考虑后续增量快照 |
| 客户端 `BatchSet` 逐条调用，大 batch 可能产生大量网络往返 | 已提供 `BatchSetStream`（gRPC streaming）备选方案 |
| Watch 订阅者消费过慢导致事件丢失 | buffer 64 条，`droppedEvents` 原子计数器追踪；调用方需及时消费 |
| Raft snapshot 生成时可能影响写入延迟 | `raft_snapshot_age_seconds` 监控 snapshot 时效性 |

## 测试覆盖

- 单元测试：`pkg/cache/` (cache_test.go, persistence_test.go, benchmark_test.go)
- 基准测试：`pkg/cache/` + `bench/`
- E2E 测试：`test/e2e/`（需 `-tags=e2e` 构建标签）
- Race 检测：`make test-race` 在 CI 中自动运行

## 性能瓶颈初判

- 大量键（百万级）时过期堆维护（O(log n)）与前缀树插入为写入侧热点
- 全树遍历搜索（通配符/正则不使用前缀优化时）为 O(n)，大缓存下延迟较高
- Dump 全量导出对大缓存可能产生较大延迟和内存峰值
- LRU 淘汰需额外 `lruMu` 锁 + `container/list` 操作，频繁淘汰时有额外开销

## 改进建议

- 降低日志级别或添加采样以减少生产环境日志量
- 补充并发与大数据集场景的测试覆盖
- 考虑为 Dump 添加增量快照支持以优化大缓存场景
- Search 可考虑添加 limit 参数限制返回结果数量
