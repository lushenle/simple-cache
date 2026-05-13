# Simple Cache API 测试指南

本文档提供了使用curl命令测试Simple Cache HTTP API的方法和示例。

## 前提条件

确保Simple Cache服务已启动，默认HTTP服务地址为 `http://localhost:8080`。

## 基本操作测试

### 1. 设置键值对 (Set)

使用POST请求设置键值对：

```bash
curl -X POST http://localhost:8080/v1/test_key \
  -H "Content-Type: application/json" \
  -d '{"value": "test_value", "expire": "10m"}'
```

成功响应：
```json
{"success":true}
```

### 2. 获取键值对 (Get)

使用GET请求获取键值对：

```bash
curl -X GET http://localhost:8080/v1/test_key
```

成功响应：
```json
{"value":"test_value", "found":true}
```

### 3. 设置键过期 (Expire)

使用POST请求使键过期：

```bash
curl -X POST http://localhost:8080/v1/test_key/expire \
  -H "Content-Type: application/json" \
  -d '{"expire":"30s"}'
```

成功响应：
```json
{"success":true, "existed":true}
```

移除过期时间（设为永不过期）：
```bash
curl -X POST http://localhost:8080/v1/test_key/expire \
  -H "Content-Type: application/json" \
  -d '{"expire":""}'
```

### 4. 删除键值对 (Delete)

首先创建一个新的键值对：

```bash
curl -X POST http://localhost:8080/v1/delete_test \
  -H "Content-Type: application/json" \
  -d '{"value": "to_be_deleted"}'
```

然后使用DELETE请求删除：

```bash
curl -X DELETE http://localhost:8080/v1/delete_test
```

成功响应：
```json
{"success":true, "existed":true}
```

## 其他API操作

### 重置缓存

```bash
curl -X DELETE http://localhost:8080/v1
```

### 搜索键

前缀搜索（路径参数方式）：
```bash
curl -X GET "http://localhost:8080/v1/search/test_*"
```

正则表达式搜索（路径参数方式）：
```bash
curl -X GET "http://localhost:8080/v1/search/test.*/REGEX"
```

> **注意**：路径参数中的 `mode` 值由网关按 proto enum 名称解析，必须使用大写枚举名（如 `REGEX`）或对应数字（如 `1`），小写值（如 `regex`）将导致 `InvalidArgument` 错误。

也支持 query string 方式：
```bash
# 通配符搜索
curl -X GET "http://localhost:8080/v1/search?pattern=test_*"

# 正则搜索（mode 参数接受 "REGEX" 或 "1"）
curl -X GET "http://localhost:8080/v1/search?pattern=test.*&mode=REGEX"
```

## 数据持久化

### 导出缓存数据 (Dump)

导出为二进制格式（默认）：
```bash
curl -X POST http://localhost:8080/v1/dump \
  -H "Content-Type: application/json" \
  -d '{}'
```

导出为 JSON 格式：
```bash
curl -X POST http://localhost:8080/v1/dump \
  -H "Content-Type: application/json" \
  -d '{"format": "json"}'
```

成功响应：
```json
{"success":true, "total_keys":2, "file_size":256, "path":"data/cache-node-1.dump", "format":"binary", "duration_ms":1.23}
```

### 导入缓存数据 (Load)

> `Load` 仅 single 模式允许；distributed 模式会返回 `FailedPrecondition`。

从默认路径加载（自动检测 binary/json）：
```bash
curl -X POST http://localhost:8080/v1/load \
  -H "Content-Type: application/json" \
  -d '{}'
```

从指定路径加载：
```bash
curl -X POST http://localhost:8080/v1/load \
  -H "Content-Type: application/json" \
  -d '{"path": "/tmp/my-cache.dump.json"}'
```

成功响应：
```json
{"success":true, "total_keys":2, "loaded_keys":2, "skipped_keys":0, "path":"data/cache-node-1.dump", "duration_ms":0.85}
```

## 健康检查

```bash
curl -X GET http://localhost:8080/healthz
```

single 模式示例响应：
```json
{"status":"ok","mode":"single","ready":true,"role":"single","grpc_addr":":5051"}
```

distributed 模式示例响应：
```json
{"status":"ok","mode":"distributed","ready":true,"role":"leader","leader_id":"n1","leader_grpc_addr":":5051","grpc_addr":":5051","details":{...}}
```

## 集群状态

```bash
curl -X GET http://localhost:8080/readyz
```

single 模式示例响应（HTTP 200）：
```json
{"status":"ok","mode":"single","ready":true,"role":"single","grpc_addr":":5051"}
```

distributed follower 示例响应（HTTP 503）：
```json
{"status":"not_ready","mode":"distributed","ready":false,"role":"follower","leader_id":"n1","leader_grpc_addr":":5051","grpc_addr":":5052"}
```

```bash
curl -X GET http://localhost:8080/cluster/peers
```

### 集群管理

```bash
# 加入节点
curl -X POST http://localhost:8080/cluster/join \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: your-token" \
  -d '{"id":"n4","addr":"http://127.0.0.1:9093"}'

# 移除节点
curl -X POST http://localhost:8080/cluster/leave \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: your-token" \
  -d '{"addr":"http://127.0.0.1:9093"}'

# Leader 主动退位
curl -X POST http://localhost:8080/cluster/stepdown \
  -H "X-Api-Token: your-token"
```

> 以上管理接口同时支持 `X-Api-Token` 和 `Authorization: Bearer <token>` 两种认证头格式。

### 流式批量写入

```bash
# 批量写入（gRPC streaming，REST 网关自动映射）
# 写入时需携带 auth token（如已配置）
curl -X POST http://localhost:8080/v1/batch-set \
  -H "Content-Type: application/json" \
  -H "X-Api-Token: your-token" \
  -d '{"key":"batch:1","value":"v1","expire":"10m"}
{"key":"batch:2","value":"v2","expire":"10m"}'
```

### 键变更订阅

```bash
# 订阅所有 key 的变更事件（gRPC server-streaming）
# REST 网关映射为 SSE 风格的长连接
curl -X GET "http://localhost:8080/v1/watch?pattern=*"
```

> 如配置了 `auth_token`，请为写接口、`/cluster/*` 以及 dump/load 请求加上 `X-Api-Token` 或 `Authorization: Bearer <token>` 头。

## 管理后台

### 访问管理后台

```bash
# 浏览器打开管理后台
open http://localhost:8080/admin/
```

### 认证

首次访问时输入 `auth_token`（对应 `config.yaml` 中的 `auth_token` 配置项）。Token 存入浏览器 `localStorage`，后续请求自动携带。

### Admin API 测试

```bash
# 聚合节点状态
curl -X GET http://localhost:8080/admin/api/status \
  -H "x-api-token: your-token"

# 指标摘要
curl -X GET http://localhost:8080/admin/api/metrics/summary \
  -H "x-api-token: your-token"

# 当前配置（不含 token）
curl -X GET http://localhost:8080/admin/api/config \
  -H "x-api-token: your-token"

# 活跃订阅列表
curl -X GET http://localhost:8080/admin/api/subscriptions \
  -H "x-api-token: your-token"

# 终止订阅
curl -X DELETE http://localhost:8080/admin/api/subscriptions/<subscription-id> \
  -H "x-api-token: your-token"

# 集群节点详情
curl -X GET http://localhost:8080/admin/api/cluster/nodes \
  -H "x-api-token: your-token"

# Raft 状态
curl -X GET http://localhost:8080/admin/api/raft \
  -H "x-api-token: your-token"

# Key 统计
curl -X GET http://localhost:8080/admin/api/keys/stats \
  -H "x-api-token: your-token"

# Plain-value Set（无需感知 protobuf Any 编码）
curl -X POST http://localhost:8080/admin/api/set/mykey \
  -H "Content-Type: application/json" \
  -H "x-api-token: your-token" \
  -d '{"value": "hello", "expire": "10m"}'

# Watch SSE 流（token 通过 query param 传递）
curl -X GET "http://localhost:8080/admin/api/watch?pattern=*&token=your-token"
```

### 语言切换

管理后台默认根据浏览器语言自动选择中文/英文。可手动切换：

```javascript
// 在浏览器 Console 中
localStorage.setItem('locale', 'zh');  // 切换中文
localStorage.setItem('locale', 'en');  // 切换英文
location.reload();
```

## API 文档

浏览器访问 `http://localhost:8080/api/docs/` 查看完整 Swagger UI 文档。
