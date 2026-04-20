# 部署指南

## 配置
- 文件 `config.yaml`，可通过环境变量 `CONFIG_PATH` 指定路径
- 关键项：

| 配置项 | 类型 | 默认值 | 说明 |
|--------|------|--------|------|
| `mode` | string | `single` | 运行模式：`single` 或 `distributed` |
| `node_id` | string | `node-1` | 节点唯一标识 |
| `grpc_addr` | string | `:5051` | gRPC 服务地址 |
| `http_addr` | string | `:8080` | HTTP 网关地址 |
| `raft_http_addr` | string | `:9090` | Raft 传输地址 |
| `metrics_addr` | string | `:2112` | Prometheus 指标地址 |
| `peers` | []string | `[]` | 同集群节点 Raft HTTP 地址列表 |
| `heartbeat_ms` | int | `200` | Leader 心跳间隔（毫秒） |
| `election_ms` | int | `1200` | 选举超时基准值（毫秒） |
| `hot_reload` | bool | `false` | 是否开启配置文件热加载 |
| `load_on_startup` | bool | `true` | 启动时是否自动加载缓存数据 |
| `dump_on_shutdown` | bool | `true` | 关闭时是否自动导出缓存数据 |
| `dump_format` | string | `binary` | 默认导出格式：`binary` 或 `json` |
| `data_dir` | string | `data` | 数据文件存储目录（Dump 文件 + Raft WAL） |
| `auth_token` | string | `""` | 管理接口与写接口鉴权 token |
| `enable_tls` | bool | `false` | 是否为 gRPC 服务启用 TLS |
| `tls_cert_file` | string | `""` | gRPC TLS 证书路径 |
| `tls_key_file` | string | `""` | gRPC TLS 私钥路径 |
| `allowed_origins` | []string | `[]` | HTTP CORS 白名单；为空时不放开跨域 |
| `snapshot_enabled` | bool | `true` | distributed 模式下是否启用 Raft snapshot |
| `snapshot_threshold` | uint64 | `1024` | 触发 snapshot 与 log compaction 的已应用日志阈值 |

## 单机模式
- 启动 `main` 即可，所有组件在本进程内
- 启动时自动从 `{data_dir}/cache-{node_id}.dump` 加载缓存数据（如文件存在）
- 关闭时自动将缓存数据导出到 `{data_dir}/cache-{node_id}.dump`

## 分布式模式
- 配置 `peers` 列表或通过 HTTP Admin 动态加入：
  - `POST /cluster/join {"id":"n2","addr":"http://host:9090"}`
  - `POST /cluster/leave {"id":"n2"}`
- 每个节点独立管理自己的 Dump 文件
- distributed 模式禁用启动自动 `Load` 与手动 `Load`，恢复依赖 WAL replay
- distributed 模式下启用 snapshot 后，恢复会优先加载 snapshot，再回放其后的增量 WAL
- 管理接口和写接口建议统一携带 `X-Api-Token` 或 `Authorization: Bearer <token>`

## 数据持久化
- 导出缓存数据：`POST /v1/dump`（支持 `binary` 和 `json` 格式）
- 导入缓存数据：`POST /v1/load`（仅 single 模式允许，自动检测格式，跳过已过期 key）
- 详细设计参见 [cache-persistence.md](cache-persistence.md)

## 健康检查
- `GET /healthz` 返回 JSON，表示服务进程与核心组件是否可用
- `GET /readyz` 返回 JSON：
  - `single` 模式恒为 `200 OK`
  - `distributed` 模式下仅 Leader 返回 `200 OK`
  - Follower / Candidate 返回 `503 Service Unavailable`

## 监控
- `GET /metrics` 暴露 Prometheus 指标

## API 文档
- `GET /api/docs/` 内嵌 Swagger UI
