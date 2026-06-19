# 发布说明 - v1.0.0

## Simple-Cache：生产可用的分布式内存缓存

这是 Simple-Cache 的第一个稳定版本，一个基于 Raft 共识、使用 Go 语言实现的分布式内存缓存系统。

---

## 功能特性

### 核心
- **Raft 共识** — 自研轻量级 Raft 实现：Leader 选举、日志复制、多数派提交、snapshot/compaction、InstallSnapshot 落后节点追平
- **双协议支持** — gRPC 原生接口 + REST HTTP（grpc-gateway 自动生成），内嵌 Swagger UI，访问 `/api/docs/` 即可查看
- **单机/分布式模式** — 一个配置字段即可切换，上层业务无感知
- **优雅关闭** — 9 步串联关闭流程（gRPC → Gateway → Raft → WatchService → Dump → Cache → Config → Metrics → Logger）

### 缓存引擎
- **HashMap + Radix Tree + 过期堆** — O(1) CRUD、O(k) 前缀搜索、O(log n) TTL 管理
- **TTL 双重过期策略** — 后台定时清理 Worker（带时间预算控制）+ Get 时惰性删除
- **前缀/通配符/正则搜索** — Radix 前缀树优化、`filepath.Match` 通配符、正则表达式支持
- **数据持久化** — 二进制和 JSON 双格式 Dump/Load，CRC32 完整性校验，原子写入

### 分布式模式
- **线性一致读（ReadIndex）** — Leader 通过 quorum 心跳确认身份后再服务读请求，彻底避免网络分区脏读
- **客户端自动切主** — `NewCluster` 客户端自动发现 Leader，遇到 `not leader` 错误自动重试并切换
- **gRPC Name Resolver** — 内置 `simplecache://` URI scheme 解析器，与 gRPC 框架原生集成
- **优雅领导权移交** — `POST /cluster/stepdown` 触发受控的 Leader 切换，避免运维重启导致选举风暴
- **动态成员管理** — 通过 HTTP API（`/cluster/join`、`/cluster/leave`）在运行时增删节点

### 生产加固
- **内存限制** — 可配置 `max_keys` 和 `max_value_size`，超出时返回类型化错误
- **LRU 淘汰策略** — 当 `eviction_policy: lru` 且缓存满时，自动淘汰最近最少使用的 key
- **速率限制** — 秒级令牌桶限流器（`max_qps`），覆盖所有操作
- **配置校验** — 启动时对所有配置项进行校验（端口格式、heartbeat < election、TLS 证书等）
- **环境变量覆盖** — 所有配置项可通过 `SIMPLE_CACHE_*` 环境变量覆盖，容器/K8s 部署友好

### 可观测性
- **Prometheus 指标** — 覆盖缓存操作、Raft 共识、持久化、内存使用等全面指标
- **pprof 端点** — Metrics 端口提供 `/debug/pprof/*`，支持运行时 CPU/Heap/Goroutine 分析（与数据面隔离）
- **结构化日志** — zap JSON 格式日志，集成 lumberjack 日志轮转（单文件 100MB、保留 7 天、自动压缩）
- **健康/就绪探针** — `/healthz` 和 `/readyz` 端点，适配容器编排系统

### 性能优化
- **WAL 二进制编码** — 紧凑二进制 WAL 替代 JSON，大 Value 场景写入吞吐提升 5-10 倍（自动兼容旧 JSON 文件）
- **gRPC 流式批量写入** — 基于 Client-Streaming 的 BatchSet 接口，批量写入性能相比逐一调用提升 10-100 倍
- **异步 Size Metrics** — 内存估算移至后台协程（30 秒间隔），消除每次 Set() 的写锁全表扫描

### 新增 API
- **Watch/Subscribe** — gRPC Server-Streaming 接口，支持订阅键变更事件（Set/Del/Expire），可通配符过滤
- **流式 BatchSet** — 基于 Client-Streaming 的高效批量写入接口

---

## 配置说明

`config.yaml` 新增字段：

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `peer_addresses` | `{}` | 节点 ID → gRPC 地址映射，用于快速 Leader 发现 |
| `max_keys` | `0` | 最大 key 数（0 = 不限） |
| `max_value_size` | `0` | 单个 value 最大字节数（0 = 不限） |
| `max_qps` | `0` | 全局每秒请求数上限（0 = 不限） |
| `eviction_policy` | `none` | 淘汰策略：`none`（满时拒绝）或 `lru`（淘汰最近最少使用） |

---

## 更新摘要

### Phase 1 — 核心 + P0 生产就绪
- 客户端自动切主与 Leader 发现（`NewCluster`）
- 配置校验与环境变量覆盖
- 内存限制和速率限制
- 优雅 Stepdown API
- pprof 性能分析端点

### Phase 2 — 读扩展性 + 可观测性
- ReadIndex 线性一致读协议
- 增强指标（待应用条目数、snapshot 年龄）

### Phase 3 — 性能与运维
- WAL 二进制编码（向后兼容）
- gRPC 流式 BatchSet
- 异步内存 Size Metrics

### Phase 4 — 高级功能
- LRU 淘汰策略（含 `cache_evictions_total` 指标）
- Watch/Subscribe 键变更通知

---

## 升级指南

### 从旧版本升级

1. **WAL 格式**：旧版 JSON 格式 WAL 文件会被自动检测并正常读取，下次 compaction 或 RewriteEntries 时会自动升级为二进制格式，无需手动迁移。

2. **配置文件**：新增配置项均为可选且有安全默认值，按需添加即可。

3. **客户端**：原有的 `New()` / `NewDefault()` / `NewSecure()` 构造函数完全向后兼容。升级到 `NewCluster()` 可获得自动切主能力。

### 不兼容变更

无。所有已有 API 和配置保持完全兼容。
