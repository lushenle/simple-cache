# 优化方案（Optimization Plan）

> 基于 master（6731fed）代码核实，2026-09-26 更新。
> 本文档取代 ROADMAP.md 中 Phase 2/3 的未完成项，给出具体实施方案。

## 现状核实

| 项 | 状态 | 证据 |
|---|---|---|
| ReadIndex 协议（leader 侧） | ✅ 已实现 | `pkg/raft/node.go:1353` |
| LRU 淘汰 | ✅ 已实现 | `pkg/cache/cache.go:42-43`（lruMu + container/list）、`pkg/config/config.go:41` |
| Watch 订阅（服务端+客户端） | ✅ 已实现 | `pkg/server/watch.go`（192 行完整实现） |
| BatchSetStream | ✅ 已实现 | quality_report 确认客户端 SDK 支持 |
| 异步 Size Metrics | ✅ 已实现 | `pkg/cache/cache.go:257`（30s 后台 goroutine） |
| 增强 Metrics | ⚠️ 大部分完成 | 缺 per-peer RTT、slow query 计数 |
| Follower Reads | ❌ 未实现 | 所有读走 leader（`pkg/server/server.go:285` `checkLeaderRead`） |
| OpenTelemetry 追踪 | ❌ 未实现 | go.mod 无 otel 依赖 |
| WAL 二进制编码 | ✅ 已实现 | `pkg/raft/storage.go`（#14 引入，追加/加载/重写均二进制，兼容旧 JSON） |
| 快照文件编码 | ⚠️ 部分遗留 | `SaveSnapshot` 用 JSON，Data 大数组被 base64 膨胀 ~33% |
| Raft 锁拆分 | ❌ 未实现 | `pkg/raft/node.go:57` 单 `mu sync.Mutex` |
| Chaos 测试 | ❌ 未实现 | `test/e2e/` 无 chaos 测试 |

---

## 1. Follower Reads（读扩展性）— 最高优先级

**问题**：所有读请求必须经过 leader（`checkLeaderRead`），加节点不能提升读吞吐。这是唯一的架构级瓶颈。

**方案**：
1. `pkg/config` 增加 `read_policy: leader | follower`（默认 `leader`，向后兼容）
2. `pkg/proto/cache.proto` 增加 `ReadIndex` RPC：follower 向 leader 请求安全 readIndex
3. 复用现有 `Node.ReadIndex()`（quorum 确认 + 轻量心跳，已在 `node.go:1353`）
4. follower 收到 readIndex 后等待 `lastApplied >= readIndex`，再从本地 FSM 读
5. `pkg/client` 直连 follower 的地址选择（已有 `simplecache://` resolver 基础）

**涉及文件**：`pkg/raft/node.go`、`pkg/server/server.go`、`pkg/proto/cache.proto`、`pkg/client/`、`pkg/config/config.go`

**验收标准**：
- 3 节点集群下 follower 读与 leader 读结果一致（线性一致性测试）
- 读吞吐随节点数线性增长（benchmark 对比）
- leader 变更窗口内读请求不返回过期数据

**风险**：正确性是核心（readIndex 确认后本地 applied 检查、leader 切换窗口）。预计 1-1.5 周。

---

## 2. OpenTelemetry 分布式追踪

**问题**：无法追踪请求路径 client → gRPC → Raft → WAL，生产排障困难。

**方案**：
1. 引入 otel SDK（`go.opentelemetry.io/otel` + grpc/otlp exporter）
2. gRPC 拦截器（server + client）自动生成 span
3. 关键路径手动埋点：Set/Get/Del、`raft.Submit`、日志复制、WAL append、snapshot
4. OTLP endpoint 可配置（默认关闭，零开销）

**涉及文件**：`go.mod`、`pkg/server/server.go`、`pkg/raft/node.go`、`pkg/cmd/main.go`、`pkg/config/config.go`

**验收标准**：配置 OTLP endpoint 后，在 Jaeger/Tempo 中看到跨节点完整 trace（含 raft 内部阶段）。预计 1 周。

---

## 3. WAL 二进制编码 ✅ (Already Implemented)

**Status**: 已实现（`3e84a78 feat/production readiness (#14)`）——WAL 追加（`appendEntriesBinary`）、加载（`loadEntriesBinary`）、重写（`RewriteEntries`）均为二进制格式，`LoadEntries()` 通过首字节（`{` = JSON）自动兼容旧格式。原先方案中的判断有误：`storage.go:293-315` 的 JSON 是 `.meta`/`.snapshot` 持久化文件，不是 WAL。

**剩余遗留（移入 Quick Wins）**：`SaveSnapshot` 用 JSON 序列化 `snapshotFile{Data}`，大 FSM dump 被 base64 膨胀 ~33% 且多一次编解码。可改为 `[JSON meta 头][原始二进制 data]` 布局。

---

## 4. Raft 锁拆分

**问题**：`node.go:57` 单 `mu sync.Mutex` 串行化所有操作（log 追加、状态读写、waiter 注册）。

**方案**：拆为三把锁——
- `logMu`：保护 log entries、commitIndex、lastApplied
- `metaMu`：保护 term、votedFor、peer 状态
- `waiterMu`：保护 applyWaiter map

**涉及文件**：`pkg/raft/node.go`

**验收标准**：`make test-race` 通过；锁顺序文档化（logMu → metaMu，禁止反向）；写吞吐 benchmark 提升。预计 3-5 天。

---

## 5. Chaos 测试

**问题**：无故障注入验证，Raft 正确性仅靠单元测试。

**方案**（`test/e2e/chaos_test.go`，`-tags=e2e`）：
1. 网络分区：阻断指定 peer 连接，验证 quorum 正确性、分区恢复后数据一致
2. 选举风暴：高频 kill leader，验证无脑裂、无数据丢失
3. 磁盘故障：WAL 写入失败注入，验证错误处理与恢复
4. 滚动重启：逐节点重启，验证无数据丢失

**验收标准**：全部测试通过；可选接入 CI（kind-action）。预计 1 周。

---

## 6. 快速修复项（Quick Wins）

| 项 | 问题 | 方案 |
|---|---|---|
| 快照文件 base64 膨胀 | `SaveSnapshot` JSON 序列化大 Data 数组，膨胀 ~33% + 编解码开销 | 改为 `[JSON meta 头][原始二进制 data]` 布局，兼容旧格式读取 |
| 补充 2 个缺失指标 | ROADMAP 2.3 遗留 | `raft_peer_rtt_seconds` 直方图；slow query 计数器（>100ms/500ms/1s 桶） |
| Dump 持写锁 | 大缓存 Dump 阻塞读写（quality_report 已知风险） | 增量快照或写时复制，Dump 期间不阻塞 Set/Get |
| 正则搜索 O(n) | 非前缀模式全树遍历（`pkg/cache/search.go:70`） | 已确认前缀模式走 `WalkPrefix` 优化；正则模式文档化限制即可，暂不改 |

---

## 实施顺序与预估

| 优先级 | 项 | 状态 | 工作量 | 风险 | 依赖 |
|---|---|---|---|---|---|
| P0 | 1. Follower Reads | ✅ 已合并 #23 | 1-1.5 周 | 中（正确性） | 无 |
| P1 | 2. OTel 追踪 | ✅ 已合并 #24 | 1 周 | 低 | 无 |
| P1 | 3. WAL 二进制编码 | ✅ 早已实现（#14） | - | - | - |
| P2 | 5. Chaos 测试 | ⬜ 进行中 | 1 周 | 低 | 建议先于 4（先有测试再改锁） |
| P2 | 4. Raft 锁拆分 | ⬜ | 3-5 天 | 中（锁顺序） | 建议在 Chaos 测试后 |
| P3 | 6. Quick Wins | ⬜ | 2-3 天 | 低 | 无 |

建议顺序：**1 → 2 → 5 → 4 → 6**。Chaos 测试先于锁拆分，为并发重构提供安全网。

## 本分支同步的文档更新

- `ROADMAP.md`：
  - Known Limitations 移除已过时的 eviction 项，补充 chaos 测试缺口
  - 3.3 Async Size Metrics、4.2 Watch 标记为已实现
  - 2.3 Enhanced Metrics 标注剩余两项并指向本文档
