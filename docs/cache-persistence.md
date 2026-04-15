# 缓存持久化功能设计文档

## 1. 背景与目标

### 1.1 背景

当前 Simple-Cache 的缓存数据完全存储在内存中，进程退出后数据丢失。虽然分布式模式下 Raft WAL 会记录写操作日志，但 WAL 是追加写入的日志流，不适合直接用于快速恢复缓存状态。在以下场景中，缓存持久化是必要的：

- **进程重启**：部署更新、配置变更、宿主机重启后，需要快速恢复缓存数据
- **灾难恢复**：节点故障恢复后，无需从上游数据源重新预热缓存
- **数据迁移**：将一个节点的缓存数据导出并导入到另一个节点

### 1.2 目标

| 目标         | 描述                                                                 |
| ------------ | -------------------------------------------------------------------- |
| Dump（导出） | 将内存中的缓存数据序列化到本地磁盘文件                               |
| Load（导入） | 从磁盘文件反序列化数据并加载到缓存中                                 |
| 双格式支持   | JSON 格式（人类可读，便于调试）+ 二进制格式（紧凑高效）              |
| 自动触发     | 进程优雅关闭时自动 Dump                                              |
| 手动触发     | 通过 gRPC/REST API 手动触发 Dump 和 Load                             |
| 过期兼容     | Load 时正确处理已过期的 key（直接丢弃或正常过期）                    |
| 原子写入     | Dump 文件写入采用"写临时文件 + rename"策略，避免写入中断导致文件损坏 |

### 1.3 非目标

- 不实现增量快照（每次 Dump 都是全量）
- 不实现分布式快照同步（每个节点独立管理自己的快照文件）
- 不实现定时自动 Dump（仅关闭时自动 + API 手动触发）

---

## 2. 整体设计

### 2.1 架构位置

持久化模块位于缓存引擎层，与 Cache 结构紧密集成：

```
┌─────────────────────────────────────────────────┐
│                   CacheService                  │
│  ┌─────────┐  ┌─────────┐  ┌──────────────────┐ │
│  │   FSM   │  │  Raft   │  │  Persistence     │ │
│  │  Apply  │  │  Node   │  │  (Dump/Load)     │ │
│  └────┬────┘  └─────────┘  └───────┬──────────┘ │
│       │                          │              │
│  ┌────▼──────────────────────────▼───────────┐  │
│  │              Cache Engine                 │  │
│  │  ┌──────┐ ┌──────────┐ ┌───────────────┐  │  │
│  │  │ Map  │ │RadixTree │ │ ExpirationHeap│  │  │
│  │  └──────┘ └──────────┘ └───────────────┘  │  │
│  └───────────────────────────────────────────┘  │
└─────────────────────────────────────────────────┘
         │                              │
         ▼                              ▼
    ┌─────────┐                    ┌──────────┐
    │  data/  │                    │  data/   │
    │ *.wal   │                    │ *.dump   │
    └─────────┘                    └──────────┘
    (Raft WAL)                  (Cache Snapshot)
```

### 2.2 文件命名

```
data/cache-{node_id}.dump        # 二进制格式（默认）
data/cache-{node_id}.dump.json   # JSON 格式
data/cache-{node_id}.dump.tmp    # 写入中的临时文件
```

---

## 3. 数据格式定义

### 3.1 二进制格式

采用自定义二进制格式，兼顾紧凑性和可扩展性：

```
┌──────────────────────────────────────────┐
│              File Header (16 bytes)       │
├──────────────────────────────────────────┤
│ Magic    [4 bytes] "SCDF"                │  文件魔数
│ Version  [4 bytes] uint32 = 1            │  格式版本
│ Count    [4 bytes] uint32                │  key-value 条目数
│ Flags    [4 bytes] uint32                │  保留标志位
├──────────────────────────────────────────┤
│           Entry Section                   │
├──────────────────────────────────────────┤
│ KeyLen   [4 bytes] uint32                │  key 长度
│ Key      [KeyLen bytes]                  │  key 数据
│ ValLen   [4 bytes] uint32                │  value 长度
│ Value    [ValLen bytes]                  │  value 数据
│ ExpUnix  [8 bytes] int64                 │  过期时间 Unix 纳秒 (0=永不过期)
│ HasExp   [1 byte]  bool                  │  是否有过期时间
├──────────────────────────────────────────┤
│           ... more entries ...            │
├──────────────────────────────────────────┤
│              Footer (8 bytes)             │
├──────────────────────────────────────────┤
│ CRC32    [4 bytes] uint32                │  Header + Entries 的 CRC32 校验
│ Padding  [4 bytes]                        │  对齐填充
└──────────────────────────────────────────┘
```

### 3.2 JSON 格式

```json
{
  "version": 1,
  "node_id": "node-1",
  "dumped_at": "2026-04-09T10:30:00Z",
  "total_keys": 1000,
  "expired_keys": 50,
  "entries": [
    {
      "key": "user:1001",
      "value": "Alice",
      "value_type": "string",
      "expiration": "2026-04-09T11:30:00Z",
      "has_expiration": true
    },
    {
      "key": "config:app",
      "value": "{\"debug\":true}",
      "value_type": "string",
      "expiration": null,
      "has_expiration": false
    }
  ]
}
```

**字段说明**：

| 字段                       | 类型              | 说明                                          |
| -------------------------- | ----------------- | --------------------------------------------- |
| `version`                  | int               | 格式版本号，当前为 1                          |
| `node_id`                  | string            | 产生快照的节点 ID                             |
| `dumped_at`                | string (ISO 8601) | 快照生成时间                                  |
| `total_keys`               | int               | 快照中的总 key 数                             |
| `expired_keys`             | int               | 快照时已过期但仍被包含的 key 数               |
| `entries[].key`            | string            | 缓存 key                                      |
| `entries[].value`          | string            | 缓存 value（序列化为字符串）                  |
| `entries[].value_type`     | string            | 原始 value 类型（`string`、`[]byte`、`json`） |
| `entries[].expiration`     | string/null       | 过期时间（ISO 8601），null 表示永不过期       |
| `entries[].has_expiration` | bool              | 是否有过期时间                                |

### 3.3 Value 序列化策略

由于 `Item.value` 类型为 `any`，需要统一的序列化/反序列化策略：

| 原始类型                    | 序列化方式             | value_type | 反序列化后类型                        |
| --------------------------- | ---------------------- | ---------- | ------------------------------------- |
| `string`                    | 直接存储               | `"string"` | `string`                              |
| `[]byte`                    | `string(val)` 直接转换 | `"bytes"`  | `[]byte`（注意：非 UTF-8 字节会丢失） |
| `json.Marshal` 可处理的类型 | JSON 编码              | `"json"`   | `string`（JSON 字符串）               |
| 其他类型                    | `fmt.Sprintf("%v", v)` | `"other"`  | `string`                              |

---

## 4. Dump 流程

### 4.1 API 手动触发

```
Client → gRPC/REST API → CacheService.Dump() → Cache.Dump(format, path)
                                              │
                                              ├─ 1. 获取写锁
                                              ├─ 2. 遍历 items map
                                              ├─ 3. 序列化每个 entry
                                              ├─ 4. 写入临时文件 (.tmp)
                                              ├─ 5. fsync 刷盘
                                              ├─ 6. rename 为正式文件名
                                              └─ 7. 释放写锁
```

### 4.2 自动触发（优雅关闭）

在 `main.go` 的优雅关闭流程中，**在 `c.Close()` 之前**执行自动 Dump：

```
SIGINT/SIGTERM
    │
    ├─ 1. grpcServer.GracefulStop()
    ├─ 2. cancel() → HTTP Gateway 停止
    ├─ 3. waitGroup.Wait()
    ├─ 4. raftNode.Close()（如果有）
    ├─ 4.5 ★ c.DumpToDefault() ★  ← 新增：自动 Dump
    ├─ 5. c.Close()
    ├─ 6. close(stop) → Config Watcher 停止
    └─ 7. metricsServer.Shutdown()
```

**自动 Dump 的配置控制**：

- `dump_on_shutdown: true`（默认）时执行自动 Dump
- `dump_on_shutdown: false` 时跳过
- 自动 Dump 使用配置中指定的默认格式（`dump_format`）
- 自动 Dump 失败时记录日志告警，**不阻止关闭流程**

---

## 5. Load 流程

### 5.1 启动时自动加载

在 `main.go` 中，**Cache 创建之后、Server 启动之前**执行自动 Load：

```
main()
    │
    ├─ metrics.Init()
    ├─ log.NewLogger()
    ├─ config.Load()
    ├─ cache.New()
    ├─ ★ c.LoadFromDefault() ★  ← 新增：自动 Load
    ├─ server.New(c, nodeID)
    ├─ [分布式模式] raft.NewNode()
    └─ 启动 gRPC + HTTP Server
```

**Load 流程**：

```
Cache.Load(path)
    │
    ├─ 1. 检查文件是否存在
    │     └─ 不存在 → 跳过（首次启动或无快照）
    ├─ 2. 读取文件内容
    ├─ 3. 解析文件头/JSON 元数据
    ├─ 4. 遍历 entries
    │     ├─ 检查是否已过期（time.Now().After(expiration)）
    │     │   ├─ 已过期 → 跳过，expired_count++
    │     │   └─ 未过期 → 反序列化 value，调用 setInternal()
    │     └─ 更新 prefixTree 和 expirationHeap
    ├─ 5. 记录加载指标（总条目数、跳过数、耗时）
    └─ 6. 返回 LoadResult{Total, Loaded, Skipped, Error}
```

**过期处理策略**：Load 时检查每个 key 的过期时间，如果已过期则直接跳过不加载。对于即将过期（剩余时间 < 1 秒）的 key，仍然加载但会很快被 cleanupWorker 清理。

### 5.2 API 手动触发

```
Client → gRPC/REST API → CacheService.Load() → Cache.Load(path)
```

手动 Load 会先清空当前缓存（内联 Reset，清空 items/prefixTree/expirationHeap/expirationIndex），然后从文件加载。

---

## 6. API 设计

### 6.1 gRPC 接口

在 `CacheService` 中新增两个 RPC 方法：

```protobuf
// Dump 缓存数据到文件
rpc Dump(DumpRequest) returns (DumpResponse) {}

// 从文件加载缓存数据
rpc Load(LoadRequest) returns (LoadResponse) {}
```

### 6.2 请求/响应类型

```protobuf
message DumpRequest {
    string format = 1;    // "json" 或 "binary"，默认 "binary"
    string path = 2;      // 文件路径，为空则使用默认路径
}

message DumpResponse {
    bool success = 1;
    int32 total_keys = 2;     // 导出的 key 总数
    int32 file_size = 3;       // 文件大小（字节）
    string path = 4;           // 实际文件路径
    string format = 5;         // 实际使用的格式
    double duration_ms = 6;    // 耗时（毫秒）
}

message LoadRequest {
    string path = 1;      // 文件路径，为空则自动检测默认路径
}

message LoadResponse {
    bool success = 1;
    int32 total_keys = 2;     // 文件中的 key 总数
    int32 loaded_keys = 3;    // 成功加载的 key 数
    int32 skipped_keys = 4;   // 跳过的 key 数（已过期）
    string path = 5;           // 实际文件路径
    double duration_ms = 6;    // 耗时（毫秒）
}
```

### 6.3 REST 接口

通过 grpc-gateway 自动映射：

| HTTP 方法 | 路径       | 说明         |
| --------- | ---------- | ------------ |
| `POST`    | `/v1/dump` | 导出缓存数据 |
| `POST`    | `/v1/load` | 导入缓存数据 |

**示例**：

```bash
# 导出为二进制格式（默认）
curl -X POST http://localhost:8080/v1/dump \
  -H "Content-Type: application/json" \
  -d '{}'

# 导出为 JSON 格式
curl -X POST http://localhost:8080/v1/dump \
  -H "Content-Type: application/json" \
  -d '{"format": "json"}'

# 从默认路径加载
curl -X POST http://localhost:8080/v1/load \
  -H "Content-Type: application/json" \
  -d '{}'

# 从指定路径加载
curl -X POST http://localhost:8080/v1/load \
  -H "Content-Type: application/json" \
  -d '{"path": "/tmp/my-cache.dump.json"}'
```

---

## 7. 配置项

在 `Config` 结构体中新增以下字段：

| 配置项         | YAML 字段          | 类型   | 默认值     | 说明                             |
| -------------- | ------------------ | ------ | ---------- | -------------------------------- |
| 启动时自动加载 | `load_on_startup`  | bool   | `true`     | 启动时是否自动从默认路径加载     |
| 关闭时自动导出 | `dump_on_shutdown` | bool   | `true`     | 关闭时是否自动导出到默认路径     |
| 默认导出格式   | `dump_format`      | string | `"binary"` | 默认导出格式：`binary` 或 `json` |
| 数据目录       | `data_dir`         | string | `"data"`   | Dump 文件存储目录                |

**默认文件路径推导**：

```
{data_dir}/cache-{node_id}.dump        # 二进制格式
{data_dir}/cache-{node_id}.dump.json   # JSON 格式
```

**配置示例**：

```yaml
mode: single
node_id: node-1
load_on_startup: true
dump_on_shutdown: true
dump_format: binary
data_dir: data
```

---

## 8. 监控指标

新增以下 Prometheus 指标：

| 指标名                               | 类型      | 标签                                       | 说明                    |
| ------------------------------------ | --------- | ------------------------------------------ | ----------------------- |
| `cache_persistence_op_total`         | Counter   | `op` (dump/load), `status` (success/error) | 持久化操作总数          |
| `cache_persistence_duration_seconds` | Histogram | `op` (dump/load)                           | 持久化操作耗时          |
| `cache_dump_keys_total`              | Gauge     | —                                          | 最近一次 Dump 的 key 数 |
| `cache_load_keys_total`              | Gauge     | `state` (loaded/skipped)                   | 最近一次 Load 的 key 数 |

---

## 9. 错误处理

| 场景                           | 处理方式                                         |
| ------------------------------ | ------------------------------------------------ |
| Dump 时文件写入失败            | 返回错误，记录日志，不影响缓存运行               |
| Dump 时磁盘空间不足            | 返回错误，清理临时文件                           |
| Load 时文件不存在              | 跳过加载，记录 Info 日志（首次启动的正常情况）   |
| Load 时文件格式损坏            | 返回错误，记录 Warn 日志，缓存保持空状态         |
| Load 时部分 entry 反序列化失败 | 跳过该 entry，继续加载后续 entry，记录 Warn 日志 |
| 自动 Dump 失败（关闭时）       | 记录 Warn 日志，**不阻止关闭流程**               |
| 自动 Load 失败（启动时）       | 记录 Warn 日志，使用空缓存继续启动               |

---

## 10. 文件变更清单

| 操作     | 文件路径                        | 说明                                                                   |
| -------- | ------------------------------- | ---------------------------------------------------------------------- |
| **新建** | `docs/cache-persistence.md`     | 本设计文档                                                             |
| **新建** | `pkg/proto/dump.proto`          | Dump/Load 的 protobuf 定义                                             |
| **新建** | `pkg/pb/dump.pb.go`             | 生成的 Go 代码                                                         |
| **新建** | `pkg/cache/persistence.go`      | 核心序列化/反序列化逻辑                                                |
| **新建** | `pkg/cache/persistence_test.go` | 持久化单元测试                                                         |
| **修改** | `pkg/proto/cache.proto`         | 添加 Dump/Load RPC 方法和 HTTP annotation                              |
| **修改** | `pkg/server/server.go`          | 新增 Dump()、Load()、NodeID() gRPC 方法；CacheService 增加 nodeID 字段 |
| **修改** | `pkg/config/config.go`          | 新增配置字段                                                           |
| **修改** | `pkg/cmd/main.go`               | 启动时 Load + 关闭时 Dump                                              |
| **修改** | `pkg/metrics/metrics.go`        | 新增持久化相关指标                                                     |
| **修改** | `pkg/client/client_test.go`     | 更新 server.New() 调用签名                                             |
| **修改** | `config.example.yaml`           | 新增配置项示例                                                         |
| **修改** | `README.md`                     | 新增持久化功能说明                                                     |
