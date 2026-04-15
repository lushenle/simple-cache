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
  -d '{"value":{"@type":"type.googleapis.com/google.protobuf.StringValue","value":"test_value"}, "expire":"10m"}'
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
{"value":{"@type":"type.googleapis.com/google.protobuf.StringValue", "value":"test_value"}, "found":true}
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

### 4. 删除键值对 (Delete)

首先创建一个新的键值对：

```bash
curl -X POST http://localhost:8080/v1/delete_test \
  -H "Content-Type: application/json" \
  -d '{"value":{"@type":"type.googleapis.com/google.protobuf.StringValue","value":"to_be_deleted"}}'
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

前缀搜索：
```bash
curl -X GET "http://localhost:8080/v1/search?pattern=test_"
```

正则表达式搜索：
```bash
curl -X GET "http://localhost:8080/v1/search?pattern=regex:test.*&mode=REGEX"
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

## 集群状态

```bash
curl -X GET http://localhost:8080/readyz
```

```bash
curl -X GET http://localhost:8080/cluster/peers
```

## API 文档

浏览器访问 `http://localhost:8080/api/docs/` 查看完整 Swagger UI 文档。
