# Simple-Cache 运维手册

本文档面向运维、值班和发布人员，描述当前 `simple-cache` 的上线检查、扩缩容、故障恢复与常见排障流程。

## 1. 适用范围

- single 模式：单节点缓存服务，支持 `Dump/Load` 持久化恢复。
- distributed 模式：基于自研 Raft 的多节点缓存服务，支持：
  - Leader 选举
  - WAL 持久化
  - snapshot / log compaction
  - `InstallSnapshot` 追平落后 follower

> 当前 distributed 模式下，读写请求都应命中 Leader。Follower 默认不对业务流量返回 ready。

## 2. 关键事实

### 2.1 关键端口

| 端口配置 | 用途 |
|---------|------|
| `grpc_addr` | gRPC 数据面 |
| `http_addr` | HTTP Gateway / 管理面 |
| `raft_http_addr` | Raft 节点间复制与选举通信 |
| `metrics_addr` | Prometheus 指标端口 |

### 2.2 关键数据文件

位于 `data_dir` 目录下：

| 文件 | 用途 |
|------|------|
| `raft-<node_id>.wal` | Raft 增量日志 |
| `raft-<node_id>.wal.meta` | 当前 term / vote / commit / snapshot 元数据 |
| `raft-<node_id>.wal.snapshot` | Raft snapshot |
| `cache-<node_id>.dump` | single 模式缓存 dump（二进制） |
| `cache-<node_id>.dump.json` | single 模式缓存 dump（JSON） |

### 2.3 探针语义

| 接口 | 含义 |
|------|------|
| `GET /healthz` | 服务进程与核心组件是否可用，返回 JSON |
| `GET /readyz` | 当前节点是否应该接业务流量，single 恒 ready；distributed 仅 Leader ready |

示例：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
```

single 模式示例：

```json
{"status":"ok","mode":"single","ready":true,"role":"single"}
```

distributed follower 示例（HTTP 503）：

```json
{"status":"not_ready","mode":"distributed","ready":false,"role":"follower","leader_id":"node-1"}
```

## 3. 上线前检查

### 3.1 single 模式

检查项：

- `config.yaml` 中 `mode=single`
- `grpc_addr` / `http_addr` / `metrics_addr` 无冲突
- `data_dir` 目录可读写
- 如启用自动恢复，确认 `load_on_startup=true`
- 如启用自动持久化，确认 `dump_on_shutdown=true`

建议检查：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:2112/metrics
```

### 3.2 distributed 模式

检查项：

- 所有节点 `mode=distributed`
- 所有节点 `peers` 列表一致
- 所有 `raft_http_addr` 都是互相可达的 `http(s)://host:port`
- 建议至少 3 节点
- `snapshot_enabled=true`
- `snapshot_threshold` 设定合理
- 如启用鉴权，所有管理操作使用统一 `auth_token`

建议检查：

```bash
curl http://<node-http>/healthz
curl http://<node-http>/readyz
curl http://<node-http>/cluster/peers
curl http://<node-metrics>/metrics
```

## 4. 日常巡检

建议巡检以下内容：

- `healthz` 是否为 `status=ok`
- `readyz` 是否只有一个 Leader 返回 `200`
- `cluster/peers` 是否与实际成员一致
- `raft_commit_index` 和 `raft_last_applied` 是否持续推进
- `raft_leader_changes_total` 是否异常频繁增加
- `simple_cache_peers_total` 是否与预期节点数一致
- snapshot 文件是否正常生成
- WAL 文件是否持续增长但会在 snapshot 后被压缩

常用命令：

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/cluster/peers
curl http://localhost:2112/metrics | grep -E 'raft_|simple_cache_peers_total'
ls -lh data/
```

## 5. 扩容流程（Add Node）

### 5.1 前提

- 当前集群健康
- Leader 明确
- 新节点具备独立端口与数据目录
- 新节点 `node_id` 唯一
- 新节点 `raft_http_addr` 对现有集群可达

### 5.2 配置新节点

示例：

```yaml
mode: distributed
node_id: n4
grpc_addr: ":5054"
http_addr: ":8083"
raft_http_addr: ":9093"
metrics_addr: ":2115"
peers:
  - "http://127.0.0.1:9090"
  - "http://127.0.0.1:9091"
  - "http://127.0.0.1:9092"
snapshot_enabled: true
snapshot_threshold: 1024
```

> 当前实现中，成员变更以 Leader 提交的配置日志为准；静态 `peers` 仍建议包含基础集群地址，方便节点首次加入时启动。

### 5.3 启动新节点

```bash
CONFIG_PATH=configs/node4.yaml go run pkg/cmd/main.go
```

初始状态下，新节点通常会是 `follower` 或 `candidate`，未加入前不应接业务流量。

### 5.4 由 Leader 执行加入

```bash
curl -X POST http://<leader-http>/cluster/join \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: <token>" \
  -d '{"id":"n4","addr":"http://127.0.0.1:9093"}'
```

### 5.5 验收

检查：

```bash
curl http://<leader-http>/cluster/peers
curl http://<node4-http>/healthz
curl http://<node4-http>/readyz
```

预期：

- `cluster/peers` 出现新节点
- 新节点成为 follower
- 如落后较多，Leader 会通过 `InstallSnapshot` 追平
- 新节点追平后，`raft_commit_index` / `raft_last_applied` 与 Leader 接近

## 6. 缩容流程（Remove Node）

### 6.1 前提

- 当前集群健康
- 待移除节点不是唯一可用节点
- 生产流量已经从该节点摘除

### 6.2 从 Leader 执行移除

```bash
curl -X POST http://<leader-http>/cluster/leave \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: <token>" \
  -d '{"addr":"http://127.0.0.1:9093"}'
```

### 6.3 停止节点

确认 `cluster/peers` 已不再包含该节点后，再停止进程：

```bash
pkill -f 'pkg/cmd/main.go'
```

或按实际部署方式关闭服务。

### 6.4 注意事项

- 不要直接先停节点再修改成员，避免一段时间内集群视图不一致。
- 不要移除当前唯一的 Leader 后再尝试执行管理操作，先确认新的 Leader。

## 7. single 模式故障恢复

### 7.1 进程重启恢复

适用条件：

- `load_on_startup=true`
- 之前存在 `cache-<node_id>.dump` 文件

恢复流程：

1. 启动进程
2. 服务自动从默认 dump 文件加载缓存
3. 用 `GET /healthz` 和业务 key 验证恢复结果

### 7.2 手动从 dump 文件恢复

```bash
curl -X POST http://localhost:8080/v1/load \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: <token>" \
  -d '{"path":"data/cache-node-1.dump"}'
```

> `Load` 仅 single 模式允许。distributed 模式不要使用 `Load` 做恢复。

## 8. distributed 模式故障恢复

### 8.1 单节点重启恢复

适用场景：

- follower 进程退出或宿主机重启
- Leader 仍在，集群仍有多数派

恢复流程：

1. 启动节点
2. 节点先恢复本地 snapshot
3. 再回放 snapshot 之后的 WAL 增量日志
4. 若仍落后于 Leader，继续通过 AppendEntries 或 InstallSnapshot 追平

检查：

```bash
curl http://<node-http>/healthz
curl http://<node-http>/readyz
curl http://<leader-http>/cluster/peers
curl http://<node-metrics>/metrics | grep -E 'raft_commit_index|raft_last_applied'
```

### 8.2 Leader 故障恢复

现象：

- 原 Leader 不可用
- 其他节点 `readyz` 短时间返回 `503`
- 随后新的 Leader 出现

处理流程：

1. 等待一个选举超时窗口
2. 在其余节点上轮询 `GET /readyz`
3. 找到返回 `200` 的新 Leader
4. 业务流量切到新 Leader
5. 原 Leader 恢复后会以 follower 身份重新加入

建议操作：

```bash
watch -n 1 'curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8080/readyz'
```

### 8.3 多节点故障 / 丢失多数派

现象：

- 所有节点 `readyz` 都不是 `200`
- 无法完成写入

原因：

- Raft 丢失多数派，集群不能提交新日志

处理原则：

1. 优先恢复原有节点，尽快恢复多数派
2. 不要在未恢复多数派前频繁做 `join/leave`
3. 不要尝试在 follower 上通过 `Load` 手工写入状态

### 8.4 节点严重落后

现象：

- follower 落后很多
- 普通 AppendEntries 已不足以追平

当前实现处理方式：

- 如果 `nextIndex <= snapshotIndex`，Leader 会发送 `InstallSnapshot`

运维动作：

1. 确认 Leader 正常
2. 确认 snapshot 文件存在
3. 观察 follower 是否逐步接近 Leader 的 `commit_index / last_applied`

## 9. 常见故障与处理

### 9.1 `/readyz` 长时间 503

排查顺序：

1. 看 `/healthz` 是否正常
2. 看是否有 Leader
3. 看 `cluster/peers` 是否正确
4. 看 `raft_leader_changes_total` 是否异常增加
5. 看 `raft_http_addr` 是否可达

### 9.2 `/cluster/join` 或 `/cluster/leave` 失败

常见原因：

- 请求没有打到 Leader
- 缺少 `auth_token`
- `addr` 不是规范化 `http(s)://host:port`
- 节点已存在或不存在

### 9.3 distributed 模式 `Load` 失败

这是正常行为：

- 当前实现明确禁止 distributed 模式通过 `Load` 修改本地状态
- 分布式恢复请依赖 `snapshot + WAL replay`

### 9.4 snapshot 未生成

检查：

- `snapshot_enabled=true`
- `snapshot_threshold` 是否过大
- 是否真的已有足够多的已应用日志
- `data_dir` 是否可写

### 9.5 WAL 持续增长

先确认：

- snapshot 是否启用
- snapshot 是否成功写入
- 节点是否有持续应用日志

如果长期无 snapshot 且 WAL 过大：

1. 检查 snapshot 配置
2. 检查日志中 snapshot/restore 相关错误
3. 必要时在业务低峰重启单个 follower 验证恢复链路

## 10. 推荐操作规范

- 管理接口统一打到当前 Leader
- 业务流量默认只打 Leader
- 所有 `/cluster/*`、写接口、`Dump/Load` 接口都带 token
- 每次扩缩容前先确认 `readyz` 和 `cluster/peers`
- distributed 模式不要人工操作 `cache.Load`
- 定期检查 `data_dir` 中 WAL 与 snapshot 文件增长情况

## 11. 值班常用命令

### 11.1 探针与节点状态

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8080/cluster/peers
```

### 11.2 管理接口

```bash
curl -X POST http://localhost:8080/cluster/join \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: <token>" \
  -d '{"id":"n4","addr":"http://127.0.0.1:9093"}'

curl -X POST http://localhost:8080/cluster/leave \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: <token>" \
  -d '{"addr":"http://127.0.0.1:9093"}'
```

### 11.3 观察数据文件

```bash
ls -lh data/
stat data/raft-*.wal*
```

### 11.4 指标观察

```bash
curl http://localhost:2112/metrics | grep -E 'raft_role|raft_commit_index|raft_last_applied|simple_cache_peers_total'
```
