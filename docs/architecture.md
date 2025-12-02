# 架构与数据流

```mermaid
flowchart LR
    subgraph Client
      CLI[Client]
    end
    subgraph Server
      GW[HTTP Gateway]
      GRPC[gRPC Server]
      CS[CacheService]
      FSM[FSM]
      CMD[Commands]
      C[Cache]
      METRICS[Prometheus Metrics]
      LOG[Logger]
    end

    CLI -->|gRPC| GRPC
    GRPC --> CS
    GW --> CS
    CS --> FSM
    FSM --> CMD
    CMD --> C
    C --> METRICS
    CS --> LOG
    GW --> METRICS
```

- 入口 `pkg/cmd/main.go` 负责初始化日志、指标、HTTP 网关与 gRPC 服务
- 服务层 `pkg/server/server.go` 将请求转为 `command.*` 并通过 `pkg/fsm` 应用到 `pkg/cache`
- 缓存层使用前缀树与小顶堆做键索引与过期管理
- 指标通过 `Prometheus` 暴露在 `:2112/metrics`

```mermaid
sequenceDiagram
  participant Client
  participant gRPC
  participant CacheService
  participant FSM
  participant Cache

  Client->>gRPC: Set(key,value,ttl)
  gRPC->>CacheService: SetRequest
  CacheService->>FSM: Apply(SetCommand)
  FSM->>Cache: Set
  Cache-->>CacheService: ok
  CacheService-->>Client: SetResponse

  Client->>gRPC: Get(key)
  gRPC->>CacheService: GetRequest
  CacheService->>Cache: Get
  Cache-->>CacheService: value,found
  CacheService-->>Client: GetResponse(Any)
```

## 模块边界
- 接口层：`pkg/pb` (Protobuf)、`pkg/server` (gRPC/HTTP)
- 领域层：`pkg/fsm`、`pkg/command`
- 基础设施层：`pkg/cache`、`pkg/log`、`pkg/metrics`、`pkg/utils`

## 现状评估
- 读写分离通过 `RWMutex` 与过期清理协程实现
- 关键路径日志较多，建议在生产环境降低等级
- 搜索支持前缀与正则，利用 Radix 前缀树提升效率
