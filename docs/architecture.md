# 架构与数据流

![当前架构总览](./assets/current-architecture-overview.svg)

```mermaid
flowchart LR
    subgraph Client
      CLI[Client / SDK]
    end
    subgraph Server
      GW[HTTP Gateway + Swagger]
      GRPC[gRPC Server]
      CS[CacheService]
      FSM[FSM]
      CMD[Commands]
      C[Cache]
      RAFT[Raft Node]
      STORE[WAL + Snapshot Storage]
      PERSIST[Cache Persistence<br/>Dump/Load]
      AUTH[Auth / TLS / CORS]
      METRICS[Prometheus Metrics]
      LOG[Logger]
      CFG[Config]
    end

    CLI -->|gRPC| GRPC
    CLI -->|REST| GW
    GW -->|转发| GRPC
    GW --> AUTH
    GRPC --> AUTH
    AUTH --> CS
    CS -->|写操作| RAFT
    RAFT -->|WAL / Snapshot| STORE
    RAFT -->|提交并应用| FSM
    RAFT -->|InstallSnapshot| RAFT
    CS -->|单机写/读| FSM
    CS -->|分布式读仅 Leader| C
    FSM --> CMD
    CMD --> C
    CS -->|Dump/Load| PERSIST
    PERSIST -->|磁盘| DATA[data/]
    C --> METRICS
    CS --> LOG
    GW --> METRICS
    CFG -->|热加载| CS
```

- 入口 `pkg/cmd/main.go` 负责初始化日志、配置、TLS/鉴权、HTTP 网关、gRPC 服务和 metrics
- 服务层 `pkg/server/server.go` 将请求转为 `command.*` 并通过 `pkg/fsm` 应用到 `pkg/cache`
- 分布式模式下，写操作通过 `pkg/raft` 实现共识、WAL 持久化、snapshot/compaction，再应用到 FSM
- 分布式读当前采用 Leader 线性一致读，Follower 直接返回 `FailedPrecondition`
- 缓存层使用 HashMap + Radix Tree + Min-Heap/ExpirationIndex 维护读写、搜索与 TTL
- `pkg/cache/persistence.go` 的 Dump/Load 主要用于 single 模式缓存持久化；distributed 模式恢复依赖 Raft snapshot + WAL replay
- 配置管理 `pkg/config/config.go` 支持 YAML 加载、原子配置与有限热重载
- 指标通过 `Prometheus` 暴露在独立 Metrics 端口

```mermaid
sequenceDiagram
  participant Client
  participant Gateway as HTTP/gRPC
  participant CacheService
  participant RaftLeader
  participant Storage as WAL/Snapshot
  participant FSM
  participant Cache

  Client->>Gateway: Set(key,value,ttl)
  Gateway->>CacheService: SetRequest
  alt 单机模式
    CacheService->>FSM: Apply(SetCommand)
    FSM->>Cache: Set
  else 分布式模式
    CacheService->>RaftLeader: Submit(SetCommand)
    RaftLeader->>Storage: Append WAL
    RaftLeader-->>CacheService: 多数派确认
    RaftLeader->>FSM: Apply(SetCommand)
    FSM->>Cache: Set
  end
  Cache-->>CacheService: ok
  CacheService-->>Client: SetResponse / not leader

  Client->>Gateway: Get(key)
  Gateway->>CacheService: GetRequest
  CacheService->>Cache: Leader local read
  Cache-->>CacheService: value, found
  CacheService-->>Client: GetResponse(Any)

  Client->>Gateway: 节点恢复
  alt distributed 模式
    Gateway->>CacheService: 节点启动
    CacheService->>Storage: Load snapshot
    Storage-->>CacheService: snapshot + WAL delta
    CacheService->>FSM: Restore + replay
  else single 模式
    Gateway->>CacheService: LoadRequest
    CacheService->>Cache: Load(path)
  end
```

## 模块边界
- 接口层：`pkg/pb` (Protobuf)、`pkg/server` (gRPC/HTTP，含 `auth.go` 认证中间件)
- 领域层：`pkg/fsm`、`pkg/command` (含 `codec.go` 命令序列化/反序列化)
- 共识层：`pkg/raft` (Raft 选举、日志复制、snapshot/compaction、InstallSnapshot，含 `peer.go` 地址规范化)
- 缓存引擎：`pkg/cache` (CRUD、TTL 过期、前缀/正则搜索、single 模式 Dump/Load 持久化)
- 基础设施层：`pkg/config` (配置管理)、`pkg/log` (日志)、`pkg/metrics` (指标)、`pkg/utils` (工具)
- 客户端：`pkg/client` (gRPC 客户端 SDK)

## 现状评估
- 读写分离通过 `InstrumentedRWMutex` 与过期清理协程实现
- 搜索支持前缀与正则，利用 Radix 前缀树提升效率
- 持久化支持二进制和 JSON 双格式，原子写入保证数据安全；Raft 侧额外支持 snapshot 与 WAL compaction
- 优雅关闭 8 步流程：gRPC → Gateway → Raft → Dump → Cache → Config → Metrics → Logger
