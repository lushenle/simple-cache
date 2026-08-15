# Raft 模块修复方案（P0 / P1 / P2）

> 状态：**已实施**（分支 fix/raft-issues，S1–S8 逐阶段提交，全量测试与 go test -race 通过）。
> 范围：仅 pkg/raft 及相关调用点（pkg/cmd/main.go、pkg/server、pkg/config）。不修改协议 proto。
> 实施记录：
> - 45270eb S1+S2（基础设施 + P0-1/P1-5/P1-6）
> - de06997 S3（P0-2/P1-4）
> - 2f2d46e S4（P0-3/P2-12/P2-16）
> - 7238736 S5（P1-7/P1-8）
> - ff9095e S6（P2-9/P2-10）
> - 13b60e3, ce997c1 S7（P2-13/P2-14）
> - 5d71462 S8（P2-15）
> - 8b56317 Review 跟进 A/B/C（分块累积上限、流水线冲突退避、flushMeta 失败重试）

## 0. 修复原则

1. 先修安全属性，再修可用性/性能。P0 全部与 Raft 安全属性（不丢已提交数据、不服务过期读、多数派语义）相关，优先。
2. 统一而非打补丁：三个跨问题机制（锁模式、meta 持久化、FSM 屏障）一次建立：
   - 锁模式：所有 raft RPC 入口统一为「内部 *Locked 函数 + 外层锁获取/释放」，panic 不会泄漏 n.mu（修复 P0-1 锁死）。
   - meta 持久化：统一为 flushMeta()（同步、锁内）与 markMetaDirty()（异步、后台 flush），消除锁外保存旧 meta 覆盖新 meta 竞态（P1-7），并把高频 commitIdx 落盘移出 fsync 热路径（P2-11）。
   - FSM 屏障：新增 applyMu sync.Mutex，串行化 apply / snapshot / restore 三个 FSM 访问点（P1-5、P1-6）。
3. 不改接口协议（proto/JSON RPC 消息结构不破坏）；仅在结构体加可选字段（PreVote、Offset/Done），Go JSON 对未知字段默认忽略，同版本滚动升级安全。
4. 不放大既有问题：修复可能改变语义的，采用最保守取舍并显式记录（例如 leader 侧 commitIdx 仍同步落盘，避免放大 P3-20 过期重放问题）。

---

## 1. 全局基础设施改动

### 1.1 统一锁模式（所有 RPC 入口）

现状：onAppendEntries（node.go:416）手工 unlock，panic 即死锁；onInstallSnapshot（node.go:834）多段手工 unlock。统一模式：

    // 外部：锁的获取/释放集中于此，panic 也不会泄漏锁
    func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
        resp, applyNeeded := func() (AppendEntriesResp, bool) {
            n.mu.Lock()
            defer n.mu.Unlock()
            return n.appendEntriesLocked(req)
        }()
        if applyNeeded {
            _ = n.applyCommittedEntries() // 必须在锁外调用（内部要拿 n.mu / applyMu）
            n.maybeSnapshot()
        }
        return resp
    }

- appendEntriesLocked / requestVoteLocked / installSnapshotLocked 为纯内存 + 存储操作，锁内不回调任何会重入 n.mu 的逻辑（applyCommittedEntries、createSnapshot 全部移到锁外）。
- 存储写失败（WAL / meta / snapshot）在 *Locked 内直接返回失败响应并记日志，不 panic。
- 锁内禁止裸切片索引，全部经 offsetOfLocked（见 1.2）或显式边界检查，从源头消除越界 panic。

上游依赖：仅 raft 包内部重构，Node 对外方法签名不变。
风险：锁内代码量增加，但行为语义逐行保持；通过新增"快照后收增量"回归测试锁定。

### 1.2 日志偏移辅助函数

    // offsetOfLocked 返回日志号在 n.logs 切片中的偏移；越界返回 false。
    func (n *Node) offsetOfLocked(index uint64) (int, bool) {
        if index <= n.snapshotIndex || index > n.lastLogIndex {
            return 0, false
        }
        offset := index - n.snapshotIndex - 1
        if offset >= uint64(len(n.logs)) {
            return 0, false
        }
        return int(offset), true
    }

entryAtLocked / truncateLogFromLocked 复用该函数（删除各自重复偏移计算）。

### 1.3 meta 持久化统一

    // Node 新增字段
    metaDirty atomic.Bool

    // flushMeta：同步落盘当前 meta。要求调用者持有 n.mu。
    func (n *Node) flushMeta() {
        n.metaDirty.Store(false)
        if err := n.storage.SaveMeta(n.metaLocked()); err != nil {
            // 记日志；继续运行，下次脏标记再试
        }
    }
    // markMetaDirty：异步落盘（后台 flush）。
    func (n *Node) markMetaDirty() { n.metaDirty.Store(true) }

后台 flush 线程：复用 loop()（node.go:199），ticker 每 tick 无条件检查：

    if n.metaDirty.Load() {
        n.mu.Lock()
        n.flushMeta()
        n.mu.Unlock()
    }

同步/异步使用规则（决定每个调用点）：

| 场景 | 方式 | 理由 |
|---|---|---|
| term 变更、投票授予/撤回（选举路径） | 同步 flushMeta | Raft 安全性：term/votedFor 必须落盘后才能响应/发起 |
| snapshotIndex 变更（create/install） | 同步 flushMeta | 与快照文件一致性（低频） |
| peers 变更（applyPeerChange） | 同步 flushMeta | 成员配置安全（低频） |
| leader 侧 commitIdx 推进（advanceCommitLocked/Submit） | 同步 flushMeta | 保守：避免放大 Expire 重放问题（P3-20）；与现状等价 |
| follower 侧 commitIdx 推进（onAppendEntries） | 异步 markMetaDirty | 高频且重启后由 leader 重新告知，丢失无安全影响 |

所有保存点必须在 n.mu 内调用（消除锁外保存旧 meta 覆盖新 meta 的竞态，P1-7）。
风险：follower 崩溃可能丢失最近 commitIdx → 重启后重放范围略增（幂等），无安全影响；文档记录。

### 1.4 FSM 屏障 applyMu

    // Node 新增字段
    applyMu sync.Mutex     // 串行化 FSM 访问：apply 循环、createSnapshot、RestoreSnapshot
    applyErr atomic.Value  // 保存致命 apply 错误（nil 表示正常）

锁顺序约定：applyMu -> n.mu -> storage.mu（storage 自带锁）。任何路径不得在持有 n.mu 时获取 applyMu（applyCommittedEntries 入口先拿 applyMu 再进循环拿 n.mu；createSnapshot/onInstallSnapshot 在锁外拿 applyMu）。据此设计不存在环。

- applyCommittedEntries 整个循环被 applyMu.Lock() 包裹（详见 P1-5）。
- createSnapshot 的 Snapshot() 调用与 onInstallSnapshot 的 RestoreSnapshot() 调用都在 applyMu 内执行（详见 P1-6）。
- 副作用：大快照采集/恢复期间写入停顿。这是 FSM 一致性的必要取舍，文档记录。

---

## 2. P0 修复

### P0-1 快照后 follower 收增量日志 panic + 死锁（node.go:501）

根因：n.logs[oldLastLogIndex:] 用日志号直接索引以 snapshotIndex+1 偏移的切片，snapshotIndex > 0 时越界；且 onAppendEntries 无 defer unlock，panic 后 n.mu 永久锁死。

修复（结合 1.1/1.2）：
1. 追加循环改为收集实际新增条目，不再用裸切片：

    var appended []LogEntry
    for _, entry := range req.Entries {
        if entry.Index <= n.lastLogIndex {
            continue
        }
        n.logs = append(n.logs, entry)
        appended = append(appended, entry)
    }
    n.recomputeLastLogLocked()

2. WAL 持久化改用收集结果：changed（截断）→ storage.RewriteEntries(n.logs)；否则 len(appended)>0 → storage.AppendEntries(appended)。语义与原实现完全一致，且不再依赖任何偏移计算。
3. 采用 1.1 统一锁模式，panic 不再泄漏锁。
4. 截断/重写路径保持：冲突检测循环、changed 标志、recomputeLastLogLocked 顺序不变。

上游依赖：无。appendEntriesLocked 返回 (AppendEntriesResp, applyNeeded bool)；响应字段 Term/Success/MatchIndex/LastLogIndex 语义不变（MatchIndex = n.lastLogIndex）。
新增测试：TestNodeAppendAfterSnapshot——3 节点：等快照触发（threshold=2）→ 再提交 → 全部节点 apply 成功且无 panic（go test -race 与 recover 检查）。

### P0-2 heartbeatRound 忽略更高 term → 被废黜 leader 服务过期读（node.go:1132-1184）

根因：quorum 心跳只统计传输层 err==nil，不检查响应 resp.Term；leader 也不因更高 term 退位，ReadIndex 的角色复查形同虚设。

修复（重构 heartbeatRound）：

    type ack struct { term uint64; err error }
    ...
    case a := <-ch:
        if a.err != nil { continue }
        n.mu.Lock()
        if a.term > n.term {
            n.stepDownLocked(a.term)
            n.flushMeta()
            n.mu.Unlock()
            return ErrNotLeader{Leader: ""}
        }
        if a.term == n.term { votes++ }
        n.mu.Unlock()

- ack 计数条件收紧为「响应 term == 当前 term」。被更高 term 拒绝的响应立即触发退位并返回错误 → 读失败（server.checkLeaderRead 已映射为 FailedPrecondition，客户端可重定向）。
- follower 日志落后导致的 Success=false（term 相同）仍算 ack——正确，ReadIndex 只需确认 leader 未被废黜。

上游依赖：ReadIndex 调用方（server.Get/Search 的 checkLeaderRead）无需改动；错误语义从"返回过期数据"变为"返回 FailedPrecondition"，符合线性一致性预期。
新增测试：TestReadIndexRejectsDeposedLeader——3 节点选出 leader 后，手工抬高一个 follower 的 term（测试内直接改字段）并让其响应更高 term，断言 leader.ReadIndex 失败且角色退位。

### P0-3 isSelf 容器化识别失败 → 自投票双计 / 多数派抬升（http_transport.go:310-335）

根因：绑定 0.0.0.0/:: 时只把 loopback 认作自身；docker-compose/k8s 中 advertised 为 service 名/pod IP（非回环），节点不识别自己 → 自投票双计、自我复制产生幽灵 matchIndex，多数派被抬高 1。

修复：HTTPTransport 增加 selfAddr string，构造时显式计算：

    // findSelfAddr 在归一化 peers 中确定"哪个地址是我自己"。
    // 判定：端口与绑定地址相同，且
    //   - 绑定 host 显式时：host 相等（或 loopback 变体）；
    //   - 绑定 host 为空/0.0.0.0/:: 时：peer host 解析出的任一 IP 是 loopback 或本机网卡 IP。
    func findSelfAddr(bindAddr string, peers []string) string {
        bindHost, bindPort, err := net.SplitHostPort(bindAddr)
        if err != nil { return "" }
        localIPs := localInterfaceIPs() // net.InterfaceAddrs() 收集，失败返回 nil
        for _, p := range peers {
            u, err := url.Parse(p)
            if err != nil { continue }
            host, port, _ := net.SplitHostPort(u.Host)
            if port != bindPort { continue }
            if bindHost != "" && bindHost != "0.0.0.0" && bindHost != "::" {
                if strings.EqualFold(host, bindHost) { return p }
                continue
            }
            if hostIsLocal(host, localIPs) { return p } // loopback 或 DNS 解析含本机 IP
        }
        return ""
    }

    func (t *HTTPTransport) isSelf(peer string) bool {
        if t.selfAddr != "" && peer == t.selfAddr { return true }
        return t.legacyIsSelf(peer) // 原逻辑兜底（selfAddr 未匹配时）
    }

- NewHTTPTransport 内部调用 findSelfAddr；签名不变。
- 覆盖场景：本机 127.0.0.1（现有测试断言不变）、绑定 :9090 + advertised http://node1:9090（DNS 解析为本机 IP → self）。

上游依赖：无接口变化。TestIsSelf 现有用例在 selfAddr=="" 时走 legacy 兜底，断言不变；新增容器化用例（注入可解析主机名）。
风险：DNS 解析失败时兜底 legacy 逻辑（保持现状行为），不会更糟。findSelfAddr 只构造时执行一次，无热路径开销。
验证：docker-compose 3 节点冒烟：/cluster/peers 一致、/readyz 就绪、无自投票日志。

---

## 3. P1 修复

### P1-4 ReadIndex 不等待 lastApply >= commitIdx（node.go:1111-1128）

修复：quorum 心跳成功后，轮询等待 FSM 追平：

    deadline := time.Now().Add(time.Second)
    for {
        n.mu.Lock()
        applied, isLeader := n.lastApply, n.Role() == Leader
        n.mu.Unlock()
        if !isLeader { return 0, ErrNotLeader{Leader: n.LeaderID()} }
        if applied >= idx { return idx, nil }
        select {
        case <-ctx.Done(): return 0, ctx.Err()
        case <-time.After(10 * time.Millisecond):
        }
        if time.Now().After(deadline) { return 0, fmt.Errorf("read index apply lag") }
    }

上游依赖：server 侧读延迟增加（通常 <10ms，最大 1s）；checkLeaderRead 超时 500ms 需调大至 1s（server.go:275），否则最坏场景读会先被 ctx 掐断——这是唯一需要改 pkg/server 的点。
风险：等待期间 leader 退位 → 返回 ErrNotLeader；apply 积压（如恢复大快照）→ 最多 1s 后报错，不挂起。

### P1-5 applyCommittedEntries 先推进 lastApply、出错永久跳过、follower 吞错（node.go:913-952）

修复：
1. applyCommittedEntries 整体持 applyMu（1.4），同一时刻只有一个 goroutine 推进 apply。
2. 先 apply 成功、再推进 lastApply（配合 applyMu 保证不重复取条目）：

    func (n *Node) applyCommittedEntries() error {
        n.applyMu.Lock()
        defer n.applyMu.Unlock()
        for {
            n.mu.Lock()
            if n.lastApply >= n.commitIdx { n.mu.Unlock(); return nil }
            nextIndex := n.lastApply + 1
            if nextIndex <= n.snapshotIndex { n.lastApply = n.snapshotIndex; n.mu.Unlock(); continue }
            entry, ok := n.entryAtLocked(nextIndex)
            if !ok { n.mu.Unlock(); return n.failApply(errors.New("missing committed log entry")) }
            waiter := n.applyWaiter[entry.Index]
            if waiter != nil { delete(n.applyWaiter, entry.Index) }
            n.mu.Unlock()

            resp, err := n.applyEntry(entry)

            n.mu.Lock()
            if err != nil {
                n.mu.Unlock()
                n.failApply(err) // 记录致命错误并通知 waiter
                if waiter != nil { waiter <- applyResult{resp: resp, err: err}; close(waiter) }
                return err
            }
            n.lastApply = entry.Index
            metrics.SetRaftLastApplied(n.lastApply)
            n.mu.Unlock()

            if waiter != nil { waiter <- applyResult{resp: resp, err: err}; close(waiter) }
        }
    }

    // failApply 进入"apply 失败"致命态：后续 Submit/ReadIndex/apply 全部拒绝。
    func (n *Node) failApply(err error) error { n.applyErr.Store(err); return err }
    func (n *Node) applyFailed() bool         { return n.applyErr.Load() != nil }

3. 致命态检查点：Submit、ReadIndex、applyCommittedEntries 入口若 applyFailed() 直接返回错误。follower 侧错误不再 _ = 吞掉（调用方 onAppendEntries/replicatePeer 记录日志）。
4. onInstallSnapshot 成功 restore 后清除 applyErr（快照恢复视为 FSM 修复手段）。

上游依赖：Submit 的错误报告更准确（已提交但应用失败 → 如实返回错误）；server 层无需改动。
风险：致命态会拒绝一切写读直到快照恢复/重启——这是"状态机与应用日志不一致时停止服务"的正确取舍（etcd 等价实现是 panic）。文档记录运维恢复手段（重启 + 快照）。

### P1-6 onInstallSnapshot / createSnapshot 与 apply 无屏障（node.go:743-787, 834-887）

修复（基于 1.4）：
- createSnapshot：Snapshot() 调用放入 applyMu 临界区。同时把 maybeSnapshot 的触发点从 applyCommittedEntries 循环内部移到所有调用方在 apply 完成之后（Submit 单节点、replicatePeer、onAppendEntries），避免 applyMu 自死锁。
- onInstallSnapshot 重构（含 P2-14 分块，见后）：restore 阶段持 applyMu；防御性检查 req.LastIncludedIndex < n.lastApply 时拒绝（快照点落后于本地已应用点 = 协议异常），杜绝"FSM 被清空但 lastApply 保持高位"的状态丢失。

上游依赖：无。
风险：快照采集/恢复期间写入停顿（1.4 已记录）；防御检查正常情况下不会触发（leader 只在 follower 落后时发快照）。

### P1-7 meta 持久化竞态 + term 落盘缺口（node.go:518-541, 559-583 等）

修复（基于 1.3）：
1. 全部保存点收进锁内：onAppendEntries 的 meta 保存从锁外（541 行）移入 appendEntriesLocked；onInstallSnapshot 的保存移入锁内。
2. term 立即落盘：onRequestVote/onAppendEntries/onInstallSnapshot 观察到 req.Term > n.term 时，stepDownLocked 后立即 flushMeta()（即使后续因日志不够新而拒绝该请求）。stepDownLocked 本身不保存（保持单职责），由各调用点在需要时 flush。
3. 消除"锁外保存旧 meta 覆盖新 meta"：由于保存全部在锁内，meta 文件单调不回退。

上游依赖：无。
风险：锁内 fsync 增加持锁时间——term 变更低频（选举/投票/快照/成员变更），可接受；commitIdx 高频路径已异步化（1.3 规则表）。

### P1-8 成员变更无 joint consensus + 并发变更安全 + 单节点 join 永不提交

修复（务实版，文档化单成员变更模型）：
1. 未决成员变更互斥（同一时间最多一个未提交的 peer change 条目）：

    // SubmitPeerChange 锁内：
    if n.peerChangeInFlightLocked() { return ErrPeerChangeInFlight{} }

    func (n *Node) peerChangeInFlightLocked() bool {
        for idx := n.commitIdx + 1; idx <= n.lastLogIndex; idx++ {
            if e, ok := n.entryAtLocked(idx); ok && (e.Type == EntryTypeAddPeer || e.Type == EntryTypeRemovePeer) {
                return true
            }
        }
        return false
    }

   条目提交后 commitIdx 越过它 → 允许下一个；leader 变更后新 leader 同样能看到未提交条目而拒绝并发变更。配合单成员变更（论文 §6 替代方案），保证变更期间各节点多数派交集非空。
2. 修复现存 bug：单节点 SubmitPeerChange 从不提交（无 peer 复制、无人推进 commitIdx）→ 与 Submit 一致，单节点（majority==1）立即 commit + flush + 本地 apply。
3. join 引导约束文档化：新节点须以包含现有集群全部节点的 peers 启动，或启动后立即调 /cluster/join（窗口内不提供写）。新节点 solo 自选 leader 会在收到 leader 的 AppendEntries 后自动退位（req.Term >= n.term → follower），solo 日志由日志匹配截断——已知限制，文档记录，不做 PreVote 级别的引导改造。

上游依赖：新增错误类型 ErrPeerChangeInFlight；main.go 的 /cluster/join、/cluster/leave handler 的 switch 增加 409 映射（可选）。ErrPeerExists/ErrPeerNotFound 语义不变。
风险：扫描范围 = 未提交日志（通常很小）；锁内 O(小) 可接受。

---

## 4. P2 修复

### P2-9 WAL torn-write 恢复（storage.go:110-122）

修复：loadEntriesBinary 遇到 decode 失败（当前实现的所有失败均为"长度不足"类截断）时丢弃尾部，返回已解析条目，不再整体失败：

    for len(remaining) > 0 {
        entry, rest, err := decodeEntryBinary(remaining)
        if err != nil {
            // 崩溃残留的半条记录：截断丢弃（WAL 无校验和，此为业界标准做法）
            return entries, nil
        }
        entries = append(entries, entry)
        remaining = rest
    }

上游依赖：NewNode 启动不再因尾部残缺失败。
风险：磁盘随机损坏可能被"解析成合法 entry"（无 CRC 固有限制，P3 记录，不修）；LoadMeta/LoadSnapshot 的 JSON 损坏仍报错（必须完整，语义不变）。
新增测试：TestStorageLoadEntriesToleratesTornTail。

### P2-10 WAL 追加未 fsync 目录（storage.go:143-166）

修复：仅当 WAL 文件新建时 fsync 目录（已有文件 O_APPEND 追加不改目录项，无需每次同步）：

    _, statErr := os.Stat(s.path)
    isNew := os.IsNotExist(statErr)
    ... OpenFile ...
    if err := f.Sync(); err != nil { ... }
    if isNew { return syncDir(filepath.Dir(s.path)) }
    return nil

风险：无。目录 fsync 仅首次创建时一次。

### P2-11 fsync 压全局 n.mu（node.go 多处）

修复（1.3 规则表落地）：
- follower 侧 commitIdx 推进（appendEntriesLocked）改为 markMetaDirty()，由后台 flush（loop 内，默认 200ms 周期）落盘——高频 fsync 移出请求路径。
- leader 侧 commit 保存保持同步（保守，见 1.3）；term/votedFor/snapshot/peers 同步。
- WAL 追加 fsync 保留（Raft 提交前持久化要求，无法省）。

上游依赖：无接口变化；重启恢复语义不变（commitIdx 丢失仅导致重放范围略增，幂等）。
风险：如上；文档明确"完整异步持久化（独立写盘线程 + 批量合并）留待后续，本次只移除高频路径"。

### P2-12 无 PreVote → 分区节点 term 膨胀（node.go:260-307）

修复：实现 PreVote（候选人在自增 term 前先征求多数派意见）：

1. RequestVoteReq 增加 PreVote bool（json:"pre_vote,omitempty"）。
2. onRequestVote：req.PreVote 时只比较 term 与日志新鲜度，不修改 votedFor/term/定时器，直接返回是否同意：

    if req.PreVote {
        if req.Term < n.term { return RequestVoteResp{Term: n.term, VoteGranted: false} }
        if req.LastLogTerm < n.lastLogTerm ||
            (req.LastLogTerm == n.lastLogTerm && req.LastLogIndex < n.lastLogIndex) {
            return RequestVoteResp{Term: n.term, VoteGranted: false}
        }
        return RequestVoteResp{Term: n.term, VoteGranted: true}
    }

3. startElection 分两段：先以 nextTerm = n.term+1 广播 PreVote（不落盘、不改 votedFor）；获多数后才 term++、写 votedFor、flushMeta()、广播正式 RequestVote。

上游依赖：消息字段新增（JSON 向后兼容）；broadcastVote 透传 req 不变；TestRequestVoteLogComparison 现有用例 PreVote=false 走原路径，断言不变。
风险：PreVote 一轮多一次往返（选举耗时略增，测试 5s 窗口足够）；分区节点 term 不再膨胀，恢复后集群不受扰动。
新增测试：TestPreVoteDoesNotPersistOrAdvance（pre-vote 后 term/votedFor/meta 文件不变）。

### P2-13 raft HTTP 无鉴权/校验/body 上限（http_transport.go）

修复：
1. 共享 token 鉴权：NewHTTPTransport 与 NewNode 增加 authToken string 参数（main.go 传 cfg.AuthToken）。发送侧带 Authorization: Bearer <token>（空 token 不发）；接收侧统一入口校验：

    func (t *HTTPTransport) authorized(w http.ResponseWriter, r *http.Request) bool {
        if t.authToken == "" { return true } // 默认配置兼容，文档注明生产必须设置
        if r.Header.Get("Authorization") != "Bearer "+t.authToken {
            http.Error(w, "unauthorized", http.StatusUnauthorized)
            return false
        }
        return true
    }

2. body 上限：r.Body = http.MaxBytesReader(w, r.Body, maxBody)；/raft/append、/raft/vote 上限 1MiB；/raft/install_snapshot 上限 = 分块大小余量（8MiB，配合 P2-14 的 4MiB 分块）。
3. 请求体校验：LeaderID/CandidateID 非空、Term 非零（防御性，低价值但廉价）。

上游依赖：NewNode/NewHTTPTransport 签名变化 → main.go:108-119 传 cfg.AuthToken（config 已有该字段）。
风险：集群各节点 auth_token 必须一致，否则互连失败——文档与配置样例注明；默认空 token 保持现状行为（兼容既有部署）。

### P2-14 InstallSnapshot 单包全量传输（node.go:791-832, 834-887）

修复：顺序分块（4MiB/chunk），InstallSnapshotReq 增加 Offset uint64、Done bool：

1. leader 侧 installSnapshotToPeer 循环发送：for offset := 0; offset < len(data); offset += chunk，每 chunk 独立超时（deadline 内发不完则中止，下轮从 offset 0 重发——接收侧以 Offset==0 重置累积）。
2. follower 侧 installSnapshotLocked 维护 pending *pendingSnapshot（{lastIncludedIndex, lastIncludedTerm, buf []byte}，n.mu 保护）：
   - req.Offset == 0 || pending == nil || pending.lastIncludedIndex != req.LastIncludedIndex → 重置累积；
   - req.Offset != len(pending.buf) → 拒绝（顺序保证，乱序/重复即失败）；
   - req.Done → 进入 restore 阶段（applyMu 屏障 + 防御检查 + restore + SaveSnapshot + 更新状态 + flushMeta + CompactLog）。
3. 单节点/无快照场景行为不变。

上游依赖：消息字段新增（JSON 兼容）；TestNodeInstallSnapshotRestoresFollowerState 改为按分块发送（测试更新）。
风险：接收侧内存峰值 = 快照大小（与单包相同）；本次目标是单请求体上限与重试粒度，内存优化（临时文件累积）留待后续——文档记录。
新增测试：TestNodeInstallSnapshotChunked（大快照分块 + 中途重试）。

### P2-15 advanceCommitLocked O(n) + 无复制流水线（node.go:889-911, 645-725）

修复：
1. commit 推进改为"多数派第 majority 大 matchIndex"（O(P log P)，P=节点数）：

    func (n *Node) advanceCommitLocked() bool {
        peers := n.trans.Peers()
        majority := len(peers)/2 + 1
        matches := make([]uint64, 0, len(peers))
        matches = append(matches, n.lastLogIndex)
        for peer, matched := range n.matchIndex {
            if peer != n.id { matches = append(matches, matched) }
        }
        if len(matches) < majority { return false }
        sort.Slice(matches, func(i, j int) bool { return matches[i] > matches[j] })
        cand := matches[majority-1]
        if cand > n.lastLogIndex { cand = n.lastLogIndex }
        for idx := cand; idx > n.commitIdx; idx-- {
            if n.termAtLocked(idx) == n.term {   // 只提交当前 term 条目（Raft §5.4.2）
                n.commitIdx = idx
                metrics.SetRaftCommitIndex(n.commitIdx)
                n.flushMeta()
                return true
            }
        }
        return false
    }

2. 复制流水线 + per-peer 单飞行：
   - Node 增加 replicating map[string]bool（n.mu 保护）；replicateAllWithDeadline 跳过正在复制的 peer，启动 goroutine 时置位、defer 清除；
   - replicatePeer 改为循环：发送 → 处理响应 → 若 nextIndex[peer] <= lastLogIndex 且未到 deadline 则继续下一批（不再等下一个心跳 tick）。

上游依赖：无。advanceCommitLocked 返回值语义（是否需 apply）不变。
风险：流水线循环受 deadline 约束（每轮最多 1s），不会无限发送；per-peer 串行保证顺序；并发心跳被 replicating 跳过，避免双发。
新增测试：TestNodeFastCatchUp（让 follower 落后大量日志后追平，断言总耗时显著小于"每心跳一批"）。

### P2-16 当选后未追加 no-op（node.go:289-305）

修复：types.go 增加 EntryTypeNoop EntryType = "noop"；applyEntry 增加 case（返回 nil，不触 FSM）；当选 leader 后（锁内、n.term == term 确认后）追加一条 no-op 并立即 replicateAll（单节点则直接 commit + flush）：

    entry := LogEntry{Index: n.lastLogIndex + 1, Term: n.term, Type: EntryTypeNoop}
    if err := n.appendEntryLocked(entry); err == nil {
        n.matchIndex[n.id] = entry.Index
        n.nextIndex[n.id] = entry.Index + 1
        if n.majorityLocked() == 1 {
            n.commitIdx = entry.Index
            metrics.SetRaftCommitIndex(n.commitIdx)
            n.flushMeta()
        }
    }

上游依赖：applyEntry 的 default 分支已有"unsupported raft entry type"防护；旧日志不含 noop，无兼容问题。
风险：选举时一次 WAL 写入 + fsync（可接受）；no-op 也参与 commit 计数与快照点，语义与普通条目一致。

---

## 5. 实施顺序（依赖关系）

| 阶段 | 内容 | 依赖 |
|---|---|---|
| S1 | 基础设施：1.1 统一锁模式、1.2 offsetOfLocked、1.3 flushMeta/dirty、1.4 applyMu + failApply | 无，纯重构 |
| S2 | P0-1（onAppendEntries 重构）、P1-5（apply 循环）、P1-6（快照屏障） | S1 |
| S3 | P0-2（heartbeatRound）、P1-4（ReadIndex 等待 apply） | S1 |
| S4 | P0-3（selfAddr）、P2-12（PreVote）、P2-16（no-op） | S1 |
| S5 | P1-7（meta 保存点收口）、P2-11（follower 异步化）、P1-8（成员变更互斥 + 单节点修复） | S1、S3 |
| S6 | P2-9（WAL 截断）、P2-10（syncDir） | 无 |
| S7 | P2-13（鉴权/body）、P2-14（分块快照） | S2（installSnapshot 重构） |
| S8 | P2-15（commit 优化 + 流水线） | S3 |
| S9 | 全量测试、race、vet、docker-compose 冒烟 | 全部 |

每阶段独立可测、可回滚；S1 是纯行为等价重构，先提交并跑全量回归锁定基线。
**S1–S8 已完成并逐阶段提交（见头部实施记录）**；S9 的单元/集成测试、go vet、go test ./pkg/... 与 -race 已全部通过，剩余 docker-compose 多节点冒烟未做（需启动服务）。

**Review 跟进（8b56317）**：
- A：分块快照累积上限（1GiB），超限丢弃并失败，防止异常 leader 耗尽 follower 内存（P2-14 补充）
- B：复制流水线遇 term 冲突（无后向跳进）时退避到下一心跳轮，避免逐条递减的 RPC 风暴（P2-15 补充）
- C：flushMeta 失败保留 dirty 标记，后台 flusher 重试，不静默丢失 term/vote 落盘
- 新增测试：超大累积拒绝、分叉 follower 修复、flushMeta 失败保 dirty

## 6. 测试与验证计划

新增单元/集成测试：
1. TestNodeAppendAfterSnapshot（P0-1 回归）
2. TestReadIndexRejectsDeposedLeader（P0-2）
3. TestFindSelfAddr / TestIsSelfContainerAddr（P0-3）
4. TestReadIndexWaitsForApply（P1-4）
5. TestApplyErrorFailsFast（P1-5：注入失败 applier，断言后续 Submit/ReadIndex 拒绝、不重复 apply）
6. TestConcurrentPeerChangeRejected（P1-8）
7. TestSingleNodePeerChangeCommits（P1-8 单节点修复回归）
8. TestStorageLoadEntriesToleratesTornTail（P2-9）
9. TestPreVoteDoesNotPersistOrAdvance（P2-12）
10. TestNodeInstallSnapshotChunked（P2-14）
11. TestNodeFastCatchUp（P2-15）
12. TestLeaderAppendsNoop（P2-16）

回归：现有 pkg/raft 全部测试（更新 TestNodeInstallSnapshotRestoresFollowerState 为分块语义、TestIsSelf 补 selfAddr 用例）。
工具：go vet ./...、go test ./pkg/raft/... -race -count=1、go test ./pkg/... -count=1。
本环境已可用（Homebrew go 1.25.5 + 自动下载 1.26.4 工具链），运行时需将 GOPATH/GOCACHE 指到 workspace 内（沙箱外不可写）：

    export PATH=/opt/homebrew/bin:$PATH
    export GOPATH=/Users/bytedance/workspace/simple-cache/.gopath
    export GOCACHE=/Users/bytedance/workspace/simple-cache/.gocache

基线（修复前）：go test ./pkg/raft/... 通过（4.2s），go test ./pkg/... 全量基线见实施阶段记录。
冒烟：docker-compose 3 节点——正常写读、kill leader 后选主、快照触发后 follower 追平、/cluster/join|leave 往返、设置 auth_token 后互连。

## 7. 明确不做（本次范围外，防止蔓延）

- joint consensus：以"单成员变更 + 未决变更互斥"替代，文档化为已知限制（P1-8）。
- CheckQuorum：分区 leader 的读写已分别被 P0-2/P2-12 挡住，无需退位机制（P2-12 说明）。
- WAL 校验和 / CRC：修复 P2-9 截断但不加校验（无格式迁移成本收益比低）。
- 完整异步持久化（独立写盘线程 + 批量 fsync）：P2-11 只移除高频路径。
- 快照接收侧临时文件（内存峰值优化）：P2-14 只做分块。
- P3 各项：死代码清理、快照双写去重、Expire 绝对时间戳等，另行立项。
