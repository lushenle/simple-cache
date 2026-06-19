# Simple-Cache Kubernetes Operator 设计方案

## 1. 目标

通过 Kubernetes Operator 模式实现 Simple-Cache 分布式缓存集群的自动化部署、扩缩容和自愈管理。用户只需声明一个 `CacheCluster` CR，Operator 即可在数秒内创建出一个可用的 Raft 缓存集群。

## 2. 架构概览

```
┌──────────────────────────────────────────────────────────────────┐
│                        Kubernetes Cluster                         │
│                                                                    │
│  ┌─────────────────────────────────────┐                         │
│  │          Operator (Deployment)       │                         │
│  │  ┌─────────────────────────────┐    │                         │
│  │  │  CacheCluster Controller    │    │    ┌──────────────────┐ │
│  │  │  (reconcile loop)           │────┼───▶│  Prometheus      │ │
│  │  └─────────────────────────────┘    │    └──────────────────┘ │
│  └─────────────────────────────────────┘                         │
│                  │                                                │
│                  │ watches & manages                              │
│                  ▼                                                │
│  ┌─────────────────────────────────────┐                         │
│  │      CacheCluster CR Instance       │                         │
│  │  (spec: replicas, storage, ...)     │                         │
│  └─────────────────────────────────────┘                         │
│                  │                                                │
│     ┌────────────┼────────────┐                                  │
│     ▼            ▼            ▼                                  │
│  ┌──────┐   ┌──────┐    ┌──────┐                                │
│  │ Pod-0│   │ Pod-1│    │ Pod-2│   StatefulSet                   │
│  │Leader│   │Follower│  │Follower│                               │
│  └──────┘   └──────┘    └──────┘                                │
│     │            │            │                                   │
│     └────────────┼────────────┘                                  │
│                  │                                                │
│     ┌────────────┴────────────┐                                  │
│     ▼                         ▼                                  │
│  ┌──────────┐           ┌──────────┐                            │
│  │Headless  │           │  Client  │                            │
│  │Service   │           │ Service  │                            │
│  │(peer)    │           │(gRPC+HTTP)│                            │
│  └──────────┘           └──────────┘                            │
└──────────────────────────────────────────────────────────────────┘
```

## 3. CRD 设计

### 3.1 GVK

| 字段 | 值 |
|------|-----|
| Group | `cache.shenle.lu` |
| Version | `v1` |
| Kind | `CacheCluster` |
| Scope | Namespaced |

### 3.2 Spec

```yaml
apiVersion: cache.shenle.lu/v1
kind: CacheCluster
metadata:
  name: my-cache
spec:
  # 副本数，Raft 要求奇数（3 / 5）
  replicas: 3

  # 容器镜像
  image: ishenle/simple-cache:v0.2
  imagePullPolicy: IfNotPresent

  # 缓存配置
  cache:
    maxKeys: 0              # 0 = 不限
    maxValueSize: 0         # 0 = 不限
    evictionPolicy: none    # none | lru
    maxQPS: 0               # 0 = 不限

  # Raft 配置
  raft:
    heartbeatMs: 200
    electionMs: 5000
    snapshotEnabled: true
    snapshotThreshold: 1024

  # 持久化配置
  persistence:
    dumpOnShutdown: true
    loadOnStartup: false    # 分布式模式依赖 Raft snapshot 恢复
    dumpFormat: binary      # binary | json
    dataDir: /data

  # 存储配置（每个 Pod 的持久卷）
  storage:
    size: 10Gi
    storageClassName: ""    # 空 = 使用默认 StorageClass

  # 计算资源
  resources:
    requests:
      cpu: 100m
      memory: 128Mi
    limits:
      cpu: 1000m
      memory: 2Gi

  # 鉴权
  auth:
    enabled: false
    tokenSecretRef:
      name: ""
      key: token

  # TLS
  tls:
    enabled: false
    certSecretRef:
      name: ""
    # cert 文件在 secret 中的 key 名
    certKey: tls.crt
    keyKey: tls.key

  # Prometheus ServiceMonitor
  monitoring:
    serviceMonitor:
      enabled: false
      interval: 30s
      labels: {}

  # Pod 调度策略
  affinity: {}
  tolerations: []
  nodeSelector: {}
  topologySpreadConstraints: []

  # 优雅停机超时
  terminationGracePeriodSeconds: 60

  # 额外环境变量（常用于注入 Secret）
  extraEnv: []
  # - name: GOMEMLIMIT
  #   value: "1GiB"
```

### 3.3 Status

```yaml
status:
  # 标准 K8s conditions
  conditions:
  - type: Available
    status: "True"
    lastTransitionTime: "2026-05-13T10:00:00Z"
    reason: ClusterReady
    message: "3/3 nodes ready, leader: my-cache-0"
  - type: Progressing
    status: "False"
    lastTransitionTime: "2026-05-13T09:55:00Z"
  - type: Degraded
    status: "False"

  # 当前 Leader
  leader:
    nodeId: my-cache-0
    podName: my-cache-0

  # 集群拓扑
  nodes:
  - nodeId: my-cache-0
    podName: my-cache-0
    role: leader
    grpcAddr: my-cache-0.my-cache.default.svc.cluster.local:5051
    ready: true
  - nodeId: my-cache-1
    podName: my-cache-1
    role: follower
    grpcAddr: my-cache-1.my-cache.default.svc.cluster.local:5051
    ready: true
  - nodeId: my-cache-2
    podName: my-cache-2
    role: follower
    grpcAddr: my-cache-2.my-cache.default.svc.cluster.local:5051
    ready: true

  # 副本状态
  replicas: 3
  readyReplicas: 3
  observedGeneration: 1
```

## 4. Controller 设计

### 4.1 Reconcile 流程

```
┌──────────────────────────────────────┐
│  CacheCluster 事件                   │
│  (create / update / delete)          │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  1. 校验 Spec                        │
│     - replicas 为奇数 (3/5)          │
│     - 引用的 Secret 存在             │
│     - storage size > 0               │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  2. 创建/更新 Headless Service       │
│     - 用于 Pod 间 Raft 通信          │
│     - clusterIP: None                │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  3. 创建/更新 Client Service         │
│     - 暴露 gRPC (5051) + HTTP (8080) │
│     - 可选 LoadBalancer / ClusterIP  │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  4. 创建/更新 ConfigMap              │
│     - 注入 cluster 拓扑信息          │
│     - 生成每个节点的配置             │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  5. 创建/更新 StatefulSet            │
│     - 挂载 PVC（数据持久化）         │
│     - 注入配置、密钥                 │
│     - 设置探针                       │
└────────────────┬─────────────────────┘
                 │
                 ▼
┌──────────────────────────────────────┐
│  6. 轮询节点状态，更新 Status        │
│     - 查询每个 Pod 的 /healthz       │
│     - 识别当前 Leader                │
│     - 更新 conditions                │
└──────────────────────────────────────┘
```

### 4.2 关键实现细节

**泛型 Reconcile 模式**：使用 controllerutil.CreateOrUpdate 避免重复代码，对每个子资源执行幂等创建/更新。

**finalizer**：CR 删除时清理关联资源后移除 finalizer。

**Owner Reference**：所有子资源 (Service, ConfigMap, StatefulSet) 均设置 ownerReference，级联删除。

**Status 更新**：每次 reconcile 结束时通过 health probe 收集集群状态。

### 4.3 配置生成逻辑

Operator 启动时无法预知 Pod IP，但 StatefulSet 提供稳定的 DNS 名称。配置通过启动脚本动态生成：

```yaml
# ConfigMap 中的启动脚本
init.sh: |
  #!/bin/sh
  # 根据 StatefulSet 副本数动态构建 peers 列表
  REPLICAS={{ .Spec.Replicas }}
  SERVICE="{{ .Name }}.{{ .Namespace }}.svc.cluster.local"
  NODE_INDEX=${HOSTNAME##*-}

  cat > /etc/simple-cache/config.yaml <<EOF
  mode: distributed
  node_id: ${HOSTNAME}
  grpc_addr: ":5051"
  http_addr: ":8080"
  raft_http_addr: ":9090"
  metrics_addr: ":2112"
  data_dir: /data
  peers:
  $(for i in $(seq 0 $((REPLICAS-1))); do
    echo "  - http://${SERVICE%.*.*}-${i}.${SERVICE}:9090"
  done)
  peer_addresses:
  $(for i in $(seq 0 $((REPLICAS-1))); do
    echo "    ${SERVICE%.*.*}-${i}: \":5051\""
  done)
  # ... 其他配置从 ConfigMap 合并
  EOF

  exec simple-cache
```

### 4.4 健康探针

```yaml
livenessProbe:
  httpGet:
    path: /healthz
    port: 8080
  initialDelaySeconds: 10
  periodSeconds: 10

readinessProbe:
  httpGet:
    path: /readyz
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 5
```

`/readyz` 在 Raft 分布式模式下，只有 Leader 返回 ready=true，Follower 返回 not_ready。因此 readinessProbe 不能直接用在 Service 的 endpoint 选择上——需要针对不同端口的 Service 使用不同的策略：
- **gRPC/HTTP Service**：应仅将 Leader Pod 作为 ready endpoint，或通过 client-side 的 leader discovery 自动路由
- **Raft Service**：所有 Pod 的 9090 端口都应可达

**方案**：gRPC 写请求通过 client leader discovery 自动路由到 Leader；读请求可通过 ReadIndex 在 Follower 上执行（未来）。因此 Client Service 的 `publishNotReadyAddresses: true` 配合 client-side 智能路由是最佳实践。

### 4.5 滚动更新安全策略

StatefulSet 默认 `podManagementPolicy: OrderedReady`，更新时从最高序号开始逐个重建。

Raft 约束：
- 一次只允许一个 Pod 重启（`maxUnavailable: 1`，StatefulSet 默认行为）
- 必须在选举超时内完成重启（默认 electionMs: 5000，足够）
- 重启后 WAL + snapshot 保证数据不丢失

```yaml
updateStrategy:
  type: RollingUpdate
  rollingUpdate:
    partition: 0    # 0 = 全部更新, N = 仅更新序号 >= N 的 Pod
```

`partition` 可用于金丝雀发布：先设 partition=2 更新一个 Pod 验证，确认无误后改为 0 全量更新。

### 4.6 扩缩容

**扩容**：
1. 修改 `spec.replicas` 从 3 → 5
2. Operator 更新 StatefulSet replicas 和 ConfigMap（peers 列表）
3. 新 Pod 启动后通过 `/cluster/join` 加入集群
4. Raft 自动同步 log + snapshot

**缩容**：
1. 修改 `spec.replicas` 从 5 → 3
2. Operator 先调用 Leader 的 `/cluster/leave` 移除多余节点
3. 缩容 StatefulSet replicas
4. 注意：缩容后仍需保持奇数副本

Controller 中增加校验：`replicas` 必须为奇数（1, 3, 5），单节点模式下 replicas=1 无 Raft。

## 5. 网络拓扑

### 5.1 Headless Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-cache                # 与 CR 同名
  namespace: default
spec:
  clusterIP: None               # Headless
  publishNotReadyAddresses: true
  ports:
  - name: grpc
    port: 5051
    targetPort: 5051
  - name: http
    port: 8080
    targetPort: 8080
  - name: raft
    port: 9090
    targetPort: 9090
  - name: metrics
    port: 2112
    targetPort: 2112
  selector:
    app.kubernetes.io/name: simple-cache
    app.kubernetes.io/instance: my-cache
```

### 5.2 Client Service

```yaml
apiVersion: v1
kind: Service
metadata:
  name: my-cache-client
  namespace: default
spec:
  type: ClusterIP        # 可改为 LoadBalancer 对外暴露
  publishNotReadyAddresses: true   # 允许 follower 也在 endpoint 中
  ports:
  - name: grpc
    port: 5051
    targetPort: 5051
  - name: http
    port: 8080
    targetPort: 8080
  selector:
    app.kubernetes.io/name: simple-cache
    app.kubernetes.io/instance: my-cache
```

### 5.3 DNS 命名规则

| 组件 | DNS 名称 |
|------|----------|
| Pod-0 | `my-cache-0.my-cache.default.svc.cluster.local` |
| Pod-1 | `my-cache-1.my-cache.default.svc.cluster.local` |
| Headless Service | `my-cache.default.svc.cluster.local`（返回所有 Pod IP） |
| Client Service | `my-cache-client.default.svc.cluster.local` |

## 6. 项目结构

```
operator/
├── Dockerfile                         # 多阶段构建
├── Makefile                           # build / test / deploy
├── go.mod
├── go.sum
├── PROJECT                            # kubebuilder 元数据
│
├── cmd/
│   └── main.go                        # 入口：启动 controller-manager
│
├── api/
│   └── cache/
│       └── v1/
│           ├── cachecluster_types.go   # CRD Go 类型定义
│           ├── cachecluster_webhook.go # 校验 Webhook（可选）
│           ├── groupversion_info.go
│           └── zz_generated.deepcopy.go
│
├── internal/
│   └── controller/
│       ├── cachecluster_controller.go  # Reconcile 主逻辑
│       ├── cachecluster_controller_test.go
│       ├── service.go                  # Headless + Client Service 构建
│       ├── configmap.go               # ConfigMap 构建
│       ├── statefulset.go             # StatefulSet 构建
│       └── status.go                  # Status 采集与更新
│
├── config/
│   ├── crd/
│   │   └── bases/
│   │       └── cache.shenle.lu_cacheclusters.yaml
│   ├── rbac/
│   │   ├── role.yaml
│   │   ├── role_binding.yaml
│   │   ├── leader_election_role.yaml
│   │   └── leader_election_role_binding.yaml
│   ├── manager/
│   │   └── manager.yaml               # Operator Deployment
│   ├── default/
│   │   ├── kustomization.yaml
│   │   └── webhookcainjection_patch.yaml
│   └── samples/
│       └── cache_v1_cachecluster.yaml
│
├── helm/
│   └── simple-cache-operator/
│       ├── Chart.yaml
│       ├── values.yaml
│       ├── templates/
│       │   ├── crd.yaml
│       │   ├── operator-deployment.yaml
│       │   ├── rbac.yaml
│       │   └── servicemonitor.yaml
│       └── README.md
│
└── test/
    └── e2e/
        └── operator_test.go            # envtest 集成测试
```

## 7. Helm Chart 设计

### 7.1 GitHub Pages 托管

Chart 源码放在仓库 `operator/helm/simple-cache-operator/` 目录，通过 GitHub Actions 自动打包并发布到 GitHub Pages。

```
仓库结构（gh-pages 分支）：
  index.yaml          # Helm repo 索引（由 chart-releaser 自动生成）
  simple-cache-operator-0.1.0.tgz
  simple-cache-operator-0.2.0.tgz
  ...
```

**GitHub Pages URL**：`https://lushenle.github.io/simple-cache/`

### 7.2 CI 自动发布流程

```yaml
# .github/workflows/helm-release.yml
name: Release Helm Chart

on:
  push:
    branches: [master]
    paths:
      - 'operator/helm/simple-cache-operator/**'

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Configure Git
        run: |
          git config user.name "github-actions[bot]"
          git config user.email "github-actions[bot]@users.noreply.github.com"

      - name: Install Helm
        uses: azure/setup-helm@v4

      - name: Run chart-releaser
        uses: helm/chart-releaser-action@v1
        with:
          charts_dir: operator/helm
          charts_repo_url: https://lushenle.github.io/simple-cache
        env:
          CR_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

触发条件：
- Chart `version` 或 `appVersion` 变更时，CI 自动打包 `.tgz`、更新 `index.yaml`，推送到 `gh-pages` 分支
- 手动打 tag（如 `helm-v0.2.0`）也会触发

### 7.3 GitHub Pages 配置

仓库 Settings → Pages → Source 选择 `gh-pages` 分支，根目录 (`/`)。发布后 Helm repo URL 为：

```
https://lushenle.github.io/simple-cache
```

### 7.4 用户安装

```bash
# 添加 Helm 仓库
helm repo add simple-cache https://lushenle.github.io/simple-cache
helm repo update

# 安装 Operator + CRD
helm install simple-cache-operator simple-cache/simple-cache-operator

# 创建集群实例
kubectl apply -f - <<EOF
apiVersion: cache.shenle.lu/v1
kind: CacheCluster
metadata:
  name: my-cache
spec:
  replicas: 3
  storage:
    size: 10Gi
EOF
```

### 7.5 values.yaml 关键参数

```yaml
# Operator 配置
operator:
  replicas: 1
  image:
    repository: ishenle/simple-cache-operator
    tag: v0.1.0
  resources:
    requests:
      cpu: 100m
      memory: 64Mi
    limits:
      cpu: 500m
      memory: 256Mi

# 是否自动创建默认集群
createDefaultCluster: true
defaultCluster:
  replicas: 3
  storage:
    size: 10Gi
    storageClassName: ""

# RBAC 创建
rbac:
  create: true

# ServiceMonitor
serviceMonitor:
  enabled: false
```

## 8. 监控与可观测性

### 8.1 Operator 指标

| 指标名 | 类型 | 说明 |
|--------|------|------|
| `simplecache_operator_reconcile_total` | Counter | Reconcile 总次数 |
| `simplecache_operator_reconcile_duration_seconds` | Histogram | Reconcile 耗时 |
| `simplecache_operator_reconcile_errors_total` | Counter | Reconcile 错误数 |
| `simplecache_operator_clusters_total` | Gauge | 管理的集群数 |
| `simplecache_operator_nodes_total` | Gauge | 管理的节点总数 |

### 8.2 Simple-Cache 自身指标

已内置 Prometheus 指标（`metrics.go`），通过 ServiceMonitor 接入 Prometheus：

```yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: my-cache
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: simple-cache
      app.kubernetes.io/instance: my-cache
  endpoints:
  - port: metrics
    interval: 30s
```

### 8.3 关键告警规则

```yaml
groups:
- name: simple-cache
  rules:
  - alert: CacheClusterLeaderElection
    expr: increase(raft_leader_changes_total[5m]) > 3
    annotations:
      summary: "集群 {{ $labels.instance }} Leader 频繁切换"

  - alert: CacheClusterNodeDown
    expr: simple_cache_peers_total < 3
    for: 2m

  - alert: CacheClusterHighEviction
    expr: rate(cache_evictions_total[5m]) > 100
```

## 9. 开发计划

| 阶段 | 内容 | 预估 |
|------|------|------|
| **P0** | kubebuilder 脚手架 + CRD 定义 + Controller 骨架 | 1 周 |
| **P1** | StatefulSet 创建、Service 管理、ConfigMap 动态生成 | 1.5 周 |
| **P2** | Status 采集、探针、滚动更新、扩缩容 | 1 周 |
| **P3** | Helm Chart、ServiceMonitor、告警规则 | 0.5 周 |
| **P4** | e2e 测试（envtest + kind）、文档 | 1 周 |

**技术选型**：
- **框架**：kubebuilder v4（Kubernetes 官方推荐，比 operator-sdk 更轻量）
- **Go**：1.24（与项目主代码一致）
- **测试**：envtest（controller-runtime 内置，无需真实集群）+ kind（e2e）
- **部署**：Helm Chart
```

## 10. 关键技术决策

### 10.1 为什么用 StatefulSet 而不是 Deployment？

| 需求 | StatefulSet | Deployment |
|------|-------------|------------|
| 稳定网络标识 | `pod-{0..N-1}.svc` | 随机 pod name |
| 持久存储 | volumeClaimTemplate 自动为每个 Pod 创建独立 PVC | 需手动管理 |
| 有序启停 | 0 → N-1 启动，N-1 → 0 停止 | 随机 |
| 滚动更新粒度 | partition 可精确控制 | 无细粒度控制 |

Raft 集群中每个节点都有持久化状态（WAL + snapshot），且需要稳定地址用于 peer 通信，StatefulSet 是唯一正确选择。

### 10.2 为什么用 Headless Service？

Headless Service (`clusterIP: None`) 为每个 Pod 创建独立 DNS A 记录，这在 Raft 场景下是刚需：
- Peer 通过稳定 DNS 名互连（`pod-0.svc.ns.cluster.local`）
- 重启后 DNS 名不变，Raft 可无缝恢复

### 10.3 为什么不在 ConfigMap 中硬编码 peers？

ConfigMap 在 Pod 启动时可能尚未包含新加入节点的信息（扩容场景）。通过 init 脚本动态生成 peers 列表，确保任何时候启动都能感知到正确的集群拓扑。

### 10.4 Operator 异常时集群状态保持

Operator 负责声明式管理，但集群运行时由 Raft 自身保证。Operator 宕机不影响已有集群的读写服务，只影响扩缩容和自动修复能力。

---

*Generated for simple-cache feat/k8s-operator branch*
