# K8s Operator 实施清单

> 基于 [k8s-operator-design.md](./k8s-operator-design.md) 方案，按顺序实施，每项完成后勾选。

---

## P0：脚手架与 CRD 定义（1 周）

### 0.1 项目初始化

- [ ] 安装 kubebuilder：`brew install kubebuilder`
- [ ] 在仓库根目录创建 operator 模块
  ```bash
  mkdir -p operator
  cd operator
  go mod init github.com/lushenle/simple-cache/operator
  kubebuilder init --domain shenle.lu --owner "Shenle Lu"
  ```
- [ ] 确认生成的 `PROJECT`、`Makefile`、`Dockerfile` 文件结构正确
- [ ] 运行 `make` 验证脚手架编译通过

### 0.2 CRD 类型定义

- [ ] 创建 API
  ```bash
  kubebuilder create api --group cache --version v1 --kind CacheCluster --resource --controller
  ```
- [ ] 在 `api/cache/v1/cachecluster_types.go` 中定义 `CacheClusterSpec`：
  - [ ] `Replicas int`（校验：奇数）
  - [ ] `Image string` + `ImagePullPolicy corev1.PullPolicy`
  - [ ] `Cache CacheConfig`（maxKeys, maxValueSize, evictionPolicy, maxQPS）
  - [ ] `Raft RaftConfig`（heartbeatMs, electionMs, snapshotEnabled, snapshotThreshold）
  - [ ] `Persistence PersistenceConfig`（dumpOnShutdown, loadOnStartup, dumpFormat, dataDir）
  - [ ] `Storage StorageConfig`（size, storageClassName）
  - [ ] `Resources corev1.ResourceRequirements`
  - [ ] `Auth AuthConfig`（enabled, tokenSecretRef）
  - [ ] `TLS TLSConfig`（enabled, certSecretRef, certKey, keyKey）
  - [ ] `Monitoring MonitoringConfig`（serviceMonitor enabled/interval/labels）
  - [ ] `Affinity`, `Tolerations`, `NodeSelector`, `TopologySpreadConstraints`
  - [ ] `TerminationGracePeriodSeconds int64`
  - [ ] `ExtraEnv []corev1.EnvVar`
- [ ] 在 `api/cache/v1/cachecluster_types.go` 中定义 `CacheClusterStatus`：
  - [ ] `Conditions []metav1.Condition`（Available, Progressing, Degraded）
  - [ ] `Leader LeaderStatus`（nodeId, podName）
  - [ ] `Nodes []NodeStatus`（nodeId, podName, role, grpcAddr, ready）
  - [ ] `Replicas int32`
  - [ ] `ReadyReplicas int32`
  - [ ] `ObservedGeneration int64`
- [ ] 实现 `GetConditions()` / `SetConditions()` 方法（满足 `conditions.Getter/Setter` 接口）
- [ ] 运行 `make generate` 生成 deepcopy 和 CRD YAML
- [ ] 检查 `config/crd/bases/cache.shenle.lu_cacheclusters.yaml` 生成正确
- [ ] 补全 `+kubebuilder:printcolumn` 注解（NAME / REPLICAS / LEADER / AGE）

### 0.3 基础 Controller

- [ ] 在 `internal/controller/cachecluster_controller.go` 中搭建 Reconcile 骨架：
  - [ ] 获取 CacheCluster CR
  - [ ] 设置 finalizer（`cache.shenle.lu/finalizer`）
  - [ ] 如果是 delete 事件：清理子资源，移除 finalizer
  - [ ] 委托到 `reconcileResources()` 方法
- [ ] 在 `cmd/main.go` 中：
  - [ ] 注册 Scheme（corev1, apps/v1, cache/v1）
  - [ ] 设置 Controller 的 `SetupWithManager`，添加 `Owns(&appsv1.StatefulSet{})` watch
  - [ ] 添加 `Owns(&corev1.Service{})` 和 `Owns(&corev1.ConfigMap{})` watch
  - [ ] 添加 healthz/readyz endpoint
- [ ] 配置 RBAC 注解（`+kubebuilder:rbac`）覆盖所需权限：
  - [ ] StatefulSet, Service, ConfigMap 的 CRUD
  - [ ] Pod list/watch/get
  - [ ] Secret get（读取 auth token / TLS cert）
  - [ ] CacheCluster 的 CRUD + status 更新
- [ ] 运行 `make manifests` 生成 RBAC YAML
- [ ] 本地 envtest 验证 Controller 启动不报错

---

## P1：StatefulSet 与 Service 编排（1.5 周）

### 1.1 Headless Service

- [ ] 创建 `internal/controller/service.go`
- [ ] 实现 `buildHeadlessService(cachecluster *v1.CacheCluster) *corev1.Service`：
  - [ ] `clusterIP: None`
  - [ ] `publishNotReadyAddresses: true`
  - [ ] ports: grpc(5051), http(8080), raft(9090), metrics(2112)
  - [ ] labels/selector：`app.kubernetes.io/name: simple-cache`, `app.kubernetes.io/instance: <cr-name>`, `app.kubernetes.io/component: cache-node`
  - [ ] ownerReference 指向 CR
- [ ] Reconcile 中调用 `controllerutil.CreateOrUpdate` 管理 Headless Service

### 1.2 Client Service

- [ ] 实现 `buildClientService(cachecluster *v1.CacheCluster) *corev1.Service`：
  - [ ] type 默认 ClusterIP（可扩展 LoadBalancer）
  - [ ] `publishNotReadyAddresses: true`
  - [ ] ports: grpc(5051), http(8080)
  - [ ] 命名后缀 `-client`
- [ ] Reconcile 中调用 `controllerutil.CreateOrUpdate` 管理 Client Service

### 1.3 ConfigMap 生成

- [ ] 创建 `internal/controller/configmap.go`
- [ ] 实现 `buildConfigMap(cachecluster *v1.CacheCluster) *corev1.ConfigMap`：
  - [ ] 生成 init 脚本（shell）：
    - [ ] 根据 replicas 构建 peers 列表（`http://<pod>-<i>.<svc>.<ns>.svc.cluster.local:9090`）
    - [ ] 根据 replicas 构建 peer_addresses（nodeID → gRPC addr）
    - [ ] 合并 CR 中的静态配置（cache/raft/persistence 参数）
    - [ ] 输出 YAML 到 `/etc/simple-cache/config.yaml`
  - [ ] 静态配置以 key 形式注入：
    - [ ] `cache.max_keys`, `cache.max_value_size`, `cache.eviction_policy`, `cache.max_qps`
    - [ ] `raft.heartbeat_ms`, `raft.election_ms`, `raft.snapshot_enabled`, `raft.snapshot_threshold`
    - [ ] `persistence.dump_on_shutdown`, `persistence.load_on_startup`, `persistence.dump_format`, `persistence.data_dir`
- [ ] Reconcile 中调用 `controllerutil.CreateOrUpdate` 管理 ConfigMap

### 1.4 StatefulSet 创建

- [ ] 创建 `internal/controller/statefulset.go`
- [ ] 实现 `buildStatefulSet(cachecluster *v1.CacheCluster) *appsv1.StatefulSet`：
  - [ ] **Pod 模板**：
    - [ ] 主容器 `simple-cache`，image 从 CR 读取
    - [ ] 启动命令：`/bin/sh /init/init.sh`
    - [ ] 挂载 ConfigMap 的 init 脚本到 `/init/init.sh`（subPath, defaultMode 0755）
    - [ ] ports: grpc(5051), http(8080), raft(9090), metrics(2112)
    - [ ] env: `SIMPLE_CACHE_NODE_ID` → `metadata.name` (valueFrom fieldRef), `SIMPLE_CACHE_MODE` → `distributed`, `CONFIG_PATH` → `/etc/simple-cache/config.yaml`
    - [ ] 合并 `spec.extraEnv`
  - [ ] **volumeClaimTemplate**：
    - [ ] `volumeMode: Filesystem`, `accessModes: [ReadWriteOnce]`
    - [ ] `resources.requests.storage` 从 `spec.storage.size`
    - [ ] `storageClassName` 从 `spec.storage.storageClassName`
  - [ ] **auth**：
    - [ ] 若 `spec.auth.enabled`：从 Secret 注入 `SIMPLE_CACHE_AUTH_TOKEN` env
  - [ ] **TLS**：
    - [ ] 若 `spec.tls.enabled`：挂载 cert Secret to `/etc/simple-cache/tls`
    - [ ] 注入 env：`SIMPLE_CACHE_ENABLE_TLS=true`, `SIMPLE_CACHE_TLS_CERT_FILE=/etc/simple-cache/tls/<certKey>`, `SIMPLE_CACHE_TLS_KEY_FILE=/etc/simple-cache/tls/<keyKey>`
  - [ ] **探针**：
    - [ ] liveness: HTTP GET `/healthz:8080`, initialDelay=10s, period=10s
    - [ ] readiness: HTTP GET `/readyz:8080`, initialDelay=5s, period=5s
  - [ ] **资源**：`spec.resources`
  - [ ] **调度**：affinity, tolerations, nodeSelector, topologySpreadConstraints
  - [ ] **优雅停机**：terminationGracePeriodSeconds
  - [ ] **labels**：与 Headless Service 一致
  - [ ] **ownerReference**：指向 CR
  - [ ] **副本数**：`spec.replicas`
  - [ ] **serviceName**：Headless Service 名称
- [ ] Reconcile 中调用 `controllerutil.CreateOrUpdate` 管理 StatefulSet

### 1.5 端到端验证

- [ ] 手动创建 CR 实例，验证 Operator 在 minikube/kind 中正确创建以下子资源：
  - [ ] Headless Service 存在且 clusterIP=None
  - [ ] Client Service 存在且 endpoint 包含所有 Pod
  - [ ] ConfigMap 中 peers 列表正确
  - [ ] StatefulSet 启动 3 个 Pod 并全部 Ready
- [ ] 进入 Pod-0 执行 gRPC Set/Get 验证缓存读写正常
- [ ] 使用 `kubectl port-forward` 访问 Admin UI 和 Swagger

---

## P2：状态采集、扩缩容与滚动更新（1 周）

### 2.1 Status 采集

- [ ] 创建 `internal/controller/status.go`
- [ ] 实现 `collectStatus(ctx, cachecluster, client) (*v1.CacheClusterStatus, error)`：
  - [ ] List 所有 Pod（label selector 匹配）
  - [ ] 对每个 Pod 执行 HTTP GET `<pod-ip>:8080/healthz`
  - [ ] 解析响应中的 `role`, `leader_id`, `ready`
  - [ ] 构建 `nodes[]` 列表
  - [ ] 识别 leader 填充 `leader` 字段
  - [ ] 计算 `readyReplicas`
- [ ] 更新 Conditions：
  - [ ] `Available: True` 当 `readyReplicas == replicas` 且有 leader
  - [ ] `Progressing: True` 当 StatefulSet.UpdateRevision != CurrentRevision
  - [ ] `Degraded: True` 当任何节点不健康超过 2 分钟
- [ ] 在 Reconcile 末尾调用 `collectStatus` 并 `r.Status().Update()`

### 2.2 扩缩容

- [ ] 验证 Controller 的 spec.replicas 变更触发 reconcile
- [ ] 扩容：
  - [ ] 修改 `spec.replicas: 5`
  - [ ] 验证 ConfigMap 中 peers 列表包含 5 个节点
  - [ ] 验证新 Pod 启动后通过 `/cluster/join` 加入（手动或通过 Controller 调用）
  - [ ] 验证 Raft log 同步
- [ ] 缩容：
  - [ ] 修改 `spec.replicas: 3`
  - [ ] Controller 先调用 Leader 的 `/cluster/leave` 移除节点（优雅操作，下讨论）
  - [ ] StatefulSet 缩容（序号最大的 Pod 先终止）
  - [ ] 验证剩余节点正常运行
- [ ] 缩容安全校验：
  - [ ] `spec.replicas` 必须为 1/3/5（奇数）
  - [ ] 若 replicas 为偶数，Reconcile 返回 error 并记录 Event

### 2.3 滚动更新

- [ ] 验证 StatefulSet `updateStrategy.type: RollingUpdate`
- [ ] 修改 `spec.image: ishenle/simple-cache:v0.3`，触发更新
- [ ] 验证 Pod 从高序号到低序号逐个重建
- [ ] 验证每个 Pod 重建后 Raft 恢复，集群无数据丢失
- [ ] 测试 `partition` 金丝雀：
  - [ ] 设置 `partition: 2`（仅更新序号 >= 2 的 Pod）
  - [ ] 验证 Pod-2 更新，Pod-0/Pod-1 不变
  - [ ] 确认无问题后设 `partition: 0` 全量更新

---

## P3：Helm Chart 与 CI（0.5 周）

### 3.1 Helm Chart 编写

- [ ] 创建 `operator/helm/simple-cache-operator/` 目录结构：
  ```
  simple-cache-operator/
  ├── Chart.yaml
  ├── values.yaml
  ├── templates/
  │   ├── crd.yaml                # CRD 定义（从 config/crd/bases 复制）
  │   ├── deployment.yaml         # Operator Deployment
  │   ├── rbac.yaml               # ClusterRole + ClusterRoleBinding
  │   ├── leader-election.yaml    # Leader election ConfigMap + Role
  │   └── servicemonitor.yaml     # 可选
  └── README.md
  ```
- [ ] `Chart.yaml`：
  - [ ] `name: simple-cache-operator`
  - [ ] `apiVersion: v2`
  - [ ] `type: application`
  - [ ] `appVersion` 跟随 operator 镜像 tag
- [ ] `values.yaml` 完整覆盖 operator 部署参数和默认集群参数（见设计方案 7.2）
- [ ] `templates/deployment.yaml`：
  - [ ] 指向 operator 镜像
  - [ ] RBAC 由 ServiceAccount 承载
  - [ ] liveness/readiness 探针指向 `/healthz` `/readyz`
- [ ] Helm lint：`helm lint operator/helm/simple-cache-operator/`
- [ ] 本地安装测试：`helm install test-operator operator/helm/simple-cache-operator/`

### 3.2 GitHub Actions 自动发布

- [ ] 创建 `.github/workflows/helm-release.yml`：
  - [ ] trigger: push 到 master 且路径匹配 `operator/helm/**`
  - [ ] 使用 `helm/chart-releaser-action@v1`
  - [ ] `charts_dir: operator/helm`
  - [ ] `charts_repo_url: https://lushenle.github.io/simple-cache`
- [ ] 仓库 Settings → Pages：
  - [ ] Source: `Deploy from a branch`
  - [ ] Branch: `gh-pages`, `/ (root)`
- [ ] 首次发布：修改 Chart.yaml version 为 `0.1.0`，push 触发 CI

### 3.3 用户安装文档

- [ ] 更新 `operator/helm/simple-cache-operator/README.md`：
  - [ ] 安装步骤：`helm repo add` + `helm install`
  - [ ] 创建 CR 的最小示例
  - [ ] values.yaml 关键参数说明表

---

## P4：测试与文档（1 周）

### 4.1 envtest 单元测试

- [ ] 安装 envtest 二进制：`make envtest`（或手动下载 kubebuilder test tools）
- [ ] `internal/controller/cachecluster_controller_test.go`：
  - [ ] `TestReconcile_Defaults`：创建最小 CR，验证 StatefulSet replicas=3、Service 存在
  - [ ] `TestReconcile_ConfigMap`：验证 ConfigMap 中 peers 数为 3
  - [ ] `TestReconcile_ScaleUp`：replicas 3→5，验证 StatefulSet 更新、ConfigMap peers 为 5
  - [ ] `TestReconcile_InvalidReplicas`：replicas=2，验证 reconcile 返回 error
  - [ ] `TestReconcile_Status`：mock health probe，验证 status.nodes 和 conditions 正确
  - [ ] `TestReconcile_Deletion`：删除 CR，验证 finalizer 清理、子资源级联删除
  - [ ] `TestReconcile_SecretRef`：引用不存在的 Secret，验证 status 包含 Degraded=True
- [ ] 运行 `make test` 确保所有测试通过

### 4.2 kind e2e 测试

- [ ] 创建 `operator/test/e2e/` 目录
- [ ] 编写 e2e 测试脚本（Go test 或 bash）：
  - [ ] `kind create cluster` 创建测试集群
  - [ ] `make docker-build && kind load docker-image` 加载 operator 镜像
  - [ ] `kubectl apply -f config/crd/bases/` 安装 CRD
  - [ ] `kubectl apply -f config/manager/` 部署 operator
  - [ ] 创建 CacheCluster CR，等待 readyReplicas=3
  - [ ] port-forward 后执行 Set/Get
  - [ ] 删除 Pod 验证 Raft 自动恢复
  - [ ] 清理：删除 CR → 删除 kind 集群
- [ ] 可选：将 e2e 加入 GitHub Actions CI（`kind-action`）

### 4.3 文档

- [ ] 更新项目根 `README.md`，添加 "K8s Operator" 一节，链接到 operator 目录
- [ ] 确认 `docs/k8s-operator-design.md` 与最终实现一致

---

## 附录：文件变更清单

| 操作 | 文件 |
|------|------|
| 新增 | `operator/go.mod` |
| 新增 | `operator/go.sum` |
| 新增 | `operator/Makefile` |
| 新增 | `operator/Dockerfile` |
| 新增 | `operator/PROJECT` |
| 新增 | `operator/cmd/main.go` |
| 新增 | `operator/api/cache/v1/cachecluster_types.go` |
| 新增 | `operator/api/cache/v1/groupversion_info.go` |
| 新增 | `operator/api/cache/v1/zz_generated.deepcopy.go` |
| 新增 | `operator/internal/controller/cachecluster_controller.go` |
| 新增 | `operator/internal/controller/cachecluster_controller_test.go` |
| 新增 | `operator/internal/controller/service.go` |
| 新增 | `operator/internal/controller/configmap.go` |
| 新增 | `operator/internal/controller/statefulset.go` |
| 新增 | `operator/internal/controller/status.go` |
| 新增 | `operator/config/crd/bases/cache.shenle.lu_cacheclusters.yaml` |
| 新增 | `operator/config/rbac/role.yaml` |
| 新增 | `operator/config/rbac/role_binding.yaml` |
| 新增 | `operator/config/rbac/leader_election_role.yaml` |
| 新增 | `operator/config/rbac/leader_election_role_binding.yaml` |
| 新增 | `operator/config/manager/manager.yaml` |
| 新增 | `operator/config/samples/cache_v1_cachecluster.yaml` |
| 新增 | `operator/helm/simple-cache-operator/Chart.yaml` |
| 新增 | `operator/helm/simple-cache-operator/values.yaml` |
| 新增 | `operator/helm/simple-cache-operator/templates/crd.yaml` |
| 新增 | `operator/helm/simple-cache-operator/templates/deployment.yaml` |
| 新增 | `operator/helm/simple-cache-operator/templates/rbac.yaml` |
| 新增 | `operator/helm/simple-cache-operator/templates/leader-election.yaml` |
| 新增 | `operator/helm/simple-cache-operator/templates/servicemonitor.yaml` |
| 新增 | `operator/helm/simple-cache-operator/README.md` |
| 新增 | `operator/test/e2e/operator_test.go` |
| 新增 | `.github/workflows/helm-release.yml` |
| 修改 | `README.md`（添加 K8s Operator 章节链接） |
