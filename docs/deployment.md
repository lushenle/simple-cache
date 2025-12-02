# 部署指南

## 配置
- 文件 `config.yaml`
- 关键项：
  - `mode`: `single` 或 `distributed`
  - `node_id`: 节点唯一标识
  - `grpc_addr`: gRPC 地址，默认 `:5051`
  - `http_addr`: HTTP 网关地址，默认 `:8080`
  - `raft_http_addr`: Raft 传输地址，默认 `:9090`
  - `peers`: 同集群节点列表
  - `heartbeat_ms`, `election_ms`: 心跳与选举参数
  - `hot_reload`: 是否开启热加载

## 单机模式
- 启动 `main` 即可，所有组件在本进程内

## 分布式模式
- 配置 `peers` 列表或通过 HTTP Admin 动态加入：
  - `POST /cluster/join {"id":"n2","addr":"http://host:9090"}`
  - `POST /cluster/leave {"id":"n2"}`

## 健康检查
- `GET /healthz` 返回 200
- `GET /readyz` 返回当前角色 `leader|follower|candidate`

## 监控
- `GET /metrics` 暴露 Prometheus 指标
