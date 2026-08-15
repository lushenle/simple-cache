package raft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Applier interface {
	Apply(cmd interface{}) (interface{}, error)
}

type SnapshotProvider interface {
	Snapshot(nodeID string) ([]byte, error)
	RestoreSnapshot(nodeID string, data []byte) error
}

type applyResult struct {
	resp interface{}
	err  error
}

type commandEntryData struct {
	Kind    string `json:"kind"`
	Payload []byte `json:"payload"`
}

// pendingSnapshot is the in-progress accumulation of a chunked
// InstallSnapshot transfer.
type pendingSnapshot struct {
	lastIncludedIndex uint64
	lastIncludedTerm  uint64
	buf               []byte
}

// installSnapshotChunkSize bounds each InstallSnapshot chunk (P2-14).
const installSnapshotChunkSize = 4 << 20 // 4 MiB

// maxPendingSnapshotBytes caps the in-memory accumulation of a chunked
// snapshot so a misbehaving leader cannot exhaust the follower's memory.
// Variable (not const) so tests can shrink it.
var maxPendingSnapshotBytes = 1 << 30 // 1 GiB

type Node struct {
	mu sync.Mutex

	id       string
	role     atomic.Value
	term     uint64
	leaderID atomic.Value

	commitIdx     uint64
	lastApply     uint64
	lastLogIndex  uint64
	lastLogTerm   uint64
	snapshotIndex uint64
	snapshotTerm  uint64

	logs        []LogEntry
	nextIndex   map[string]uint64
	matchIndex  map[string]uint64
	applyWaiter map[uint64]chan applyResult
	// replicating tracks peers with an in-flight replication goroutine so a
	// heartbeat round never double-sends to the same peer (P2-15). Guarded
	// by n.mu.
	replicating map[string]bool

	// pendingSnapshot accumulates InstallSnapshot chunks before the final
	// restore. Guarded by n.mu.
	pendingSnapshot *pendingSnapshot

	storage *Storage
	trans   *HTTPTransport
	applier Applier

	hb                time.Duration
	el                time.Duration
	snapshotEnabled   bool
	snapshotThreshold uint64

	votedFor         string
	electionDeadline int64
	rnd              *rand.Rand
	rndMu            sync.Mutex

	stopCh chan struct{}
	wg     sync.WaitGroup
	close  sync.Once

	logger *zap.Logger

	// applyMu serializes FSM access: the apply loop, snapshot capture and
	// snapshot restore are mutually exclusive so the state machine never
	// observes interleaved operations. Lock order: applyMu -> n.mu -> storage.
	applyMu sync.Mutex
	// applyErr stores a fatal state-machine apply error (nil when healthy).
	applyErr atomic.Pointer[raftApplyError]
	// metaDirty marks that meta needs persisting asynchronously.
	metaDirty atomic.Bool
}

func NewNode(id string, addr string, peers []string, storage *Storage, applier Applier, heartbeat, election time.Duration, snapshotEnabled bool, snapshotThreshold uint64, logger *zap.Logger, authToken string) (*Node, error) {
	n := &Node{
		id:                id,
		storage:           storage,
		applier:           applier,
		hb:                heartbeat,
		el:                election,
		snapshotEnabled:   snapshotEnabled,
		snapshotThreshold: snapshotThreshold,
		logger:            logger,
		stopCh:            make(chan struct{}),
		nextIndex:         make(map[string]uint64),
		matchIndex:        make(map[string]uint64),
		applyWaiter:       make(map[uint64]chan applyResult),
		replicating:       make(map[string]bool),
	}
	n.role.Store(Follower)
	metrics.SetRaftRole(n.id, string(Follower))
	n.leaderID.Store("")

	meta, err := storage.LoadMeta()
	if err != nil {
		return nil, fmt.Errorf("load meta: %w", err)
	}
	if meta != nil {
		n.term = meta.CurrentTerm
		n.votedFor = meta.VotedFor
		n.commitIdx = meta.CommitIndex
		n.snapshotIndex = meta.SnapshotIndex
		n.snapshotTerm = meta.SnapshotTerm
		if len(meta.Peers) > 0 {
			peers = meta.Peers
		}
	}

	snapshotMeta, snapshotData, err := storage.LoadSnapshot()
	if err != nil {
		return nil, fmt.Errorf("load snapshot: %w", err)
	}
	if snapshotMeta != nil {
		n.snapshotIndex = snapshotMeta.LastIncludedIndex
		n.snapshotTerm = snapshotMeta.LastIncludedTerm
		if snapshotter, ok := applier.(SnapshotProvider); ok && len(snapshotData) > 0 {
			if err := snapshotter.RestoreSnapshot(id, snapshotData); err != nil {
				return nil, fmt.Errorf("restore snapshot: %w", err)
			}
		}
	}

	entries, err := storage.LoadEntries()
	if err != nil {
		return nil, fmt.Errorf("load entries: %w", err)
	}
	n.logs = append(n.logs, entries...)
	n.recomputeLastLogLocked()
	if n.commitIdx < n.snapshotIndex {
		n.commitIdx = n.snapshotIndex
	}
	if n.lastApply < n.snapshotIndex {
		n.lastApply = n.snapshotIndex
	}
	if n.commitIdx > n.lastLogIndex {
		n.commitIdx = n.lastLogIndex
	}

	trans, err := NewHTTPTransport(addr, peers, authToken, logger)
	if err != nil {
		return nil, fmt.Errorf("raft transport: %w", err)
	}
	n.trans = trans
	n.trans.Start(n)
	metrics.SetPeersTotal(len(n.trans.Peers()))

	seed := time.Now().UnixNano() ^ int64(len(peers))
	n.rnd = rand.New(rand.NewSource(seed))
	n.resetElectionDeadline()
	n.mu.Lock()
	n.resetLeaderProgressLocked()
	n.mu.Unlock()
	_ = n.applyCommittedEntries()
	n.maybeSnapshot()

	n.wg.Add(2)
	go n.loop()
	go n.electionLoop()
	return n, nil
}

// Close stops the raft node background goroutines and closes the transport.
func (n *Node) Close() {
	n.close.Do(func() {
		close(n.stopCh)
		n.wg.Wait()
		n.trans.Close()
	})
}

func (n *Node) Role() Role { return n.role.Load().(Role) }

func (n *Node) LeaderID() string {
	leader, _ := n.leaderID.Load().(string)
	return leader
}

func (n *Node) Status() map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	return map[string]any{
		"node_id":        n.id,
		"role":           n.Role(),
		"leader_id":      n.LeaderID(),
		"term":           n.term,
		"commit_index":   n.commitIdx,
		"last_applied":   n.lastApply,
		"last_log_index": n.lastLogIndex,
		"snapshot_index": n.snapshotIndex,
		"snapshot_term":  n.snapshotTerm,
		"peers_total":    len(n.trans.Peers()),
	}
}

func (n *Node) loop() {
	defer n.wg.Done()
	ticker := time.NewTicker(n.hb)
	defer ticker.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-ticker.C:
			if n.metaDirty.Load() {
				n.mu.Lock()
				n.flushMeta()
				n.mu.Unlock()
			}
			if n.Role() == Leader {
				start := time.Now()
				n.replicateAll()
				metrics.ObserveAppendEntriesLatency(time.Since(start))
			}
		}
	}
}

func (n *Node) electionLoop() {
	defer n.wg.Done()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-tick.C:
			if n.Role() == Leader {
				continue
			}
			if time.Now().UnixNano() > atomic.LoadInt64(&n.electionDeadline) {
				if n.logger != nil {
					n.mu.Lock()
					term := n.term
					n.mu.Unlock()
					n.logger.Info("election timeout", zap.String("node", n.id), zap.Uint64("term", term))
				}
				n.startElection()
			}
		}
	}
}

func (n *Node) resetElectionDeadline() {
	n.rndMu.Lock()
	jitter := time.Duration(n.rnd.Int63n(int64(n.el)))
	n.rndMu.Unlock()
	deadline := time.Now().Add(n.el + jitter).UnixNano()
	atomic.StoreInt64(&n.electionDeadline, deadline)
}

// resetElectionDeadlineLocked extends the deadline by the full election
// timeout without random jitter.  Used after granting a vote to prevent
// a voter from immediately starting its own election (quiet period).
func (n *Node) resetElectionDeadlineLocked() {
	// Use 2x the election timeout to give the new leader ample time
	// to send heartbeats and stabilise the cluster.
	deadline := time.Now().Add(2 * n.el).UnixNano()
	atomic.StoreInt64(&n.electionDeadline, deadline)
}

func (n *Node) startElection() {
	n.role.Store(Candidate)
	metrics.SetRaftRole(n.id, string(Candidate))

	// Phase 1: pre-vote (P2-12). Ask the majority whether they would grant a
	// vote at term+1 without touching term/votedFor, so a partitioned node
	// stops inflating its term while a healthy leader exists.
	n.mu.Lock()
	nextTerm := n.term + 1
	lastLogIndex := n.lastLogIndex
	lastLogTerm := n.lastLogTerm
	n.mu.Unlock()

	preReq := RequestVoteReq{
		Term:         nextTerm,
		CandidateID:  n.id,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
		PreVote:      true,
	}
	votes := 1 + n.trans.broadcastVote(preReq)
	total := len(n.trans.Peers())
	if votes < total/2+1 {
		// A healthy leader is likely present; retry after the timeout.
		n.resetElectionDeadline()
		return
	}

	// Phase 2: real election.
	n.mu.Lock()
	n.term++
	n.votedFor = n.id
	term := n.term
	lastLogIndex = n.lastLogIndex
	lastLogTerm = n.lastLogTerm
	n.flushMeta()
	n.mu.Unlock()

	req := RequestVoteReq{
		Term:         term,
		CandidateID:  n.id,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	if n.logger != nil {
		n.logger.Debug("start election", zap.String("candidate", n.id), zap.Uint64("term", term), zap.Int("peers", len(n.trans.Peers())))
	}
	votes = 1 + n.trans.broadcastVote(req)
	total = len(n.trans.Peers())
	if n.logger != nil {
		n.logger.Debug("vote result", zap.String("candidate", n.id), zap.Int("votes", votes), zap.Int("total", total), zap.Int("majority", total/2+1))
	}

	if votes >= (total/2 + 1) {
		n.mu.Lock()
		if n.term == term {
			n.role.Store(Leader)
			metrics.SetRaftRole(n.id, string(Leader))
			n.leaderID.Store(n.id)
			n.resetLeaderProgressLocked()
			// Append a no-op entry (P2-16) so entries from previous terms
			// become commit-able and the commit index advances promptly.
			entry := LogEntry{Index: n.lastLogIndex + 1, Term: n.term, Type: EntryTypeNoop}
			if err := n.appendEntryLocked(entry); err != nil {
				if n.logger != nil {
					n.logger.Warn("append noop failed", zap.String("node", n.id), zap.Error(err))
				}
			} else {
				n.matchIndex[n.id] = entry.Index
				n.nextIndex[n.id] = entry.Index + 1
				if n.majorityLocked() == 1 {
					n.commitIdx = entry.Index
					metrics.SetRaftCommitIndex(n.commitIdx)
					n.flushMeta()
				}
			}
			if n.logger != nil {
				n.logger.Info("become leader", zap.String("node", n.id), zap.Uint64("term", n.term))
			}
		}
		n.mu.Unlock()
		// Send an immediate fast heartbeat (100ms deadline) to suppress
		// follower elections before doing full log replication.
		n.fastHeartbeat()
		n.replicateAll()
	}
	n.resetElectionDeadline()
}

func (n *Node) Submit(cmd interface{}) (interface{}, error) {
	if n.Role() != Leader {
		return nil, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}

	entry, err := n.newCommandEntry(cmd)
	if err != nil {
		return nil, err
	}

	waiter := make(chan applyResult, 1)

	n.mu.Lock()
	if n.Role() != Leader {
		n.mu.Unlock()
		return nil, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	entry.Index = n.lastLogIndex + 1
	entry.Term = n.term
	if err := n.appendEntryLocked(entry); err != nil {
		n.mu.Unlock()
		return nil, err
	}
	n.applyWaiter[entry.Index] = waiter
	n.matchIndex[n.id] = entry.Index
	n.nextIndex[n.id] = entry.Index + 1
	if n.majorityLocked() == 1 {
		n.commitIdx = entry.Index
		metrics.SetRaftCommitIndex(n.commitIdx)
		n.flushMeta()
	}
	isSingle := n.majorityLocked() == 1
	n.mu.Unlock()

	if isSingle {
		if err := n.applyCommittedEntries(); err != nil {
			return nil, err
		}
		n.maybeSnapshot()
	}

	if err := n.replicateUntilCommitted(entry.Index); err != nil {
		n.mu.Lock()
		delete(n.applyWaiter, entry.Index)
		n.mu.Unlock()
		return nil, err
	}

	timer := time.NewTimer(2 * time.Second)
	select {
	case result := <-waiter:
		timer.Stop()
		return result.resp, result.err
	case <-timer.C:
		return nil, ErrCommit{}
	}
}

func (n *Node) SubmitPeerChange(addr string, remove bool) error {
	if n.Role() != Leader {
		return ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	normalizedAddr, err := NormalizePeerAddr(addr)
	if err != nil {
		return ErrInvalidPeerChange{}
	}

	entryIndex, isSingle, err := func() (uint64, bool, error) {
		n.mu.Lock()
		defer n.mu.Unlock()
		// Single-member-change model: reject a new change while an earlier
		// peer-change entry is still uncommitted (P1-8).
		if n.peerChangeInFlightLocked() {
			return 0, false, ErrPeerChangeInFlight{}
		}
		exists := containsPeer(n.trans.Peers(), normalizedAddr)
		if !remove && exists {
			return 0, false, ErrPeerExists{}
		}
		if remove && !exists {
			return 0, false, ErrPeerNotFound{}
		}
		if remove && n.trans.isSelf(normalizedAddr) {
			return 0, false, ErrInvalidPeerChange{}
		}
		payload, err := json.Marshal(PeerChange{Addr: normalizedAddr})
		if err != nil {
			return 0, false, err
		}
		entryType := EntryTypeAddPeer
		if remove {
			entryType = EntryTypeRemovePeer
		}
		entry := LogEntry{
			Index: n.lastLogIndex + 1,
			Term:  n.term,
			Type:  entryType,
			Data:  payload,
		}
		if err := n.appendEntryLocked(entry); err != nil {
			return 0, false, err
		}
		n.matchIndex[n.id] = entry.Index
		n.nextIndex[n.id] = entry.Index + 1
		// Single-node cluster: commit locally (previously nothing ever
		// advanced commitIdx for a peer change, so it always timed out).
		if n.majorityLocked() == 1 {
			n.commitIdx = entry.Index
			metrics.SetRaftCommitIndex(n.commitIdx)
			n.flushMeta()
		}
		return entry.Index, n.majorityLocked() == 1, nil
	}()
	if err != nil {
		return err
	}
	if isSingle {
		if err := n.applyCommittedEntries(); err != nil {
			return err
		}
		n.maybeSnapshot()
		return nil
	}
	return n.replicateUntilCommitted(entryIndex)
}

// peerChangeInFlightLocked reports whether an uncommitted peer-change entry
// exists in the log. Combined with the single-member-change model this keeps
// membership transitions safe: at most one change may be in flight at a time.
func (n *Node) peerChangeInFlightLocked() bool {
	for idx := n.commitIdx + 1; idx <= n.lastLogIndex; idx++ {
		if e, ok := n.entryAtLocked(idx); ok && (e.Type == EntryTypeAddPeer || e.Type == EntryTypeRemovePeer) {
			return true
		}
	}
	return false
}

// onAppendEntries handles an AppendEntries RPC. Lock acquisition is isolated
// so a panic in the handler can never leave n.mu locked; FSM work (apply /
// snapshot) is performed outside the lock.
func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
	resp, applyNeeded := func() (AppendEntriesResp, bool) {
		n.mu.Lock()
		defer n.mu.Unlock()
		return n.appendEntriesLocked(req)
	}()
	if applyNeeded {
		_ = n.applyCommittedEntries()
		n.maybeSnapshot()
	}
	return resp
}

func (n *Node) appendEntriesLocked(req AppendEntriesReq) (AppendEntriesResp, bool) {
	if req.Term < n.term {
		return AppendEntriesResp{Term: n.term, Success: false, LastLogIndex: n.lastLogIndex}, false
	}
	termChanged := req.Term > n.term
	if termChanged {
		n.stepDownLocked(req.Term)
	}

	// If this heartbeat is from ourselves (e.g. the leader sending to its
	// own Docker service name), just reset the deadline and keep the
	// current role.  Without this guard, the leader would set its own role
	// to Follower every heartbeat cycle.
	if req.LeaderID == n.id && req.Term == n.term {
		n.resetElectionDeadline()
		return AppendEntriesResp{Term: n.term, Success: true, LastLogIndex: n.lastLogIndex}, false
	}

	n.leaderID.Store(req.LeaderID)
	prev := n.Role()
	if prev != Follower {
		metrics.IncRaftLeaderChanges()
	}
	n.role.Store(Follower)
	metrics.SetRaftRole(n.id, string(Follower))
	n.resetElectionDeadline()

	if req.PrevLogIndex > 0 {
		if req.PrevLogIndex > n.lastLogIndex || n.termAtLocked(req.PrevLogIndex) != req.PrevLogTerm {
			return AppendEntriesResp{Term: n.term, Success: false, LastLogIndex: n.lastLogIndex}, false
		}
	}

	changed := false
	for _, entry := range req.Entries {
		if entry.Index <= n.snapshotIndex {
			continue
		}
		if entry.Index <= n.lastLogIndex {
			if local, ok := n.entryAtLocked(entry.Index); ok && local.Term != entry.Term {
				n.truncateLogFromLocked(entry.Index)
				changed = true
				break
			}
			continue
		}
	}
	if changed {
		n.recomputeLastLogLocked()
	}

	// Collect exactly the newly appended entries so WAL persistence never
	// depends on slice offsets relative to the snapshot index.
	var appended []LogEntry
	for _, entry := range req.Entries {
		if entry.Index <= n.lastLogIndex {
			continue
		}
		n.logs = append(n.logs, entry)
		appended = append(appended, entry)
	}
	n.recomputeLastLogLocked()

	if changed {
		if err := n.storage.RewriteEntries(n.logs); err != nil {
			return AppendEntriesResp{Term: n.term, Success: false, LastLogIndex: n.lastLogIndex}, false
		}
	} else if len(appended) > 0 {
		if err := n.storage.AppendEntries(appended); err != nil {
			return AppendEntriesResp{Term: n.term, Success: false, LastLogIndex: n.lastLogIndex}, false
		}
	}

	applyNeeded := false
	if req.CommitIdx > n.commitIdx {
		n.commitIdx = req.CommitIdx
		if n.commitIdx > n.lastLogIndex {
			n.commitIdx = n.lastLogIndex
		}
		metrics.SetRaftCommitIndex(n.commitIdx)
		applyNeeded = true
	}

	// Persist meta: a term change must hit disk before we respond (Raft
	// safety), while commit-index-only changes are persisted asynchronously
	// by the background flusher so a heartbeat is never blocked on fsync.
	// All saves happen under the lock so concurrent saves can never
	// overwrite a newer meta with a stale one.
	if termChanged {
		n.flushMeta()
	} else {
		n.markMetaDirty()
	}

	if n.logger != nil {
		n.logger.Debug("append entries received", zap.String("node", n.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term), zap.Uint64("commit_index", n.commitIdx))
	}
	return AppendEntriesResp{
		Term:         n.term,
		Success:      true,
		MatchIndex:   n.lastLogIndex,
		LastLogIndex: n.lastLogIndex,
	}, applyNeeded
}

func (n *Node) onRequestVote(req RequestVoteReq) RequestVoteResp {
	n.mu.Lock()
	defer n.mu.Unlock()

	if req.PreVote {
		// Pre-vote (P2-12): check term and log freshness without mutating
		// any state, so a partitioned node stops inflating its term while a
		// healthy leader exists.
		if req.Term < n.term {
			return RequestVoteResp{Term: n.term, VoteGranted: false}
		}
		if req.LastLogTerm < n.lastLogTerm ||
			(req.LastLogTerm == n.lastLogTerm && req.LastLogIndex < n.lastLogIndex) {
			return RequestVoteResp{Term: n.term, VoteGranted: false}
		}
		return RequestVoteResp{Term: n.term, VoteGranted: true}
	}

	if req.Term < n.term {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if req.Term > n.term {
		n.stepDownLocked(req.Term)
		n.flushMeta() // persist the higher term immediately (Raft safety)
	}
	// Raft §5.4.1: compare last log term first, then index
	if req.LastLogTerm < n.lastLogTerm {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if req.LastLogTerm == n.lastLogTerm && req.LastLogIndex < n.lastLogIndex {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if n.votedFor == "" || n.votedFor == req.CandidateID {
		n.votedFor = req.CandidateID
		n.flushMeta()
		n.resetElectionDeadlineLocked()
		return RequestVoteResp{Term: n.term, VoteGranted: true}
	}
	return RequestVoteResp{Term: n.term, VoteGranted: false}
}

func (n *Node) replicateUntilCommitted(index uint64) error {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if n.Role() != Leader {
			return ErrNotLeader{Leader: n.leaderID.Load().(string)}
		}

		n.replicateAllWithDeadline(deadline)

		n.mu.Lock()
		committed := n.commitIdx >= index
		n.mu.Unlock()
		if committed {
			return nil
		}

		select {
		case <-n.stopCh:
			return ErrCommit{}
		case <-time.After(20 * time.Millisecond):
		}
	}
	return ErrCommit{}
}

func (n *Node) replicateAll() {
	n.replicateAllWithDeadline(time.Now().Add(time.Second))
}

// fastHeartbeat sends an immediate lightweight AppendEntries to all peers
// with a short deadline. It is used right after a leader is elected so that
// followers reset their election timers before the leader starts full log
// replication.
func (n *Node) fastHeartbeat() {
	n.replicateAllWithDeadline(time.Now().Add(100 * time.Millisecond))
}

func (n *Node) replicateAllWithDeadline(deadline time.Time) {
	peers := n.trans.Peers()
	const maxConcurrentReplicas = 8
	sem := make(chan struct{}, maxConcurrentReplicas)
	var wg sync.WaitGroup
	for _, peer := range peers {
		if n.trans.isSelf(peer) {
			continue
		}
		if time.Now().After(deadline) {
			break
		}
		n.mu.Lock()
		if n.replicating[peer] {
			n.mu.Unlock()
			continue
		}
		n.replicating[peer] = true
		n.mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				n.mu.Lock()
				delete(n.replicating, target)
				n.mu.Unlock()
			}()
			n.replicatePeer(target, deadline)
		}(peer)
	}
	wg.Wait()
}

// replicatePeer sends log batches to one follower, pipelining subsequent
// batches within the round instead of waiting for the next heartbeat tick
// (P2-15). The loop is bounded by the round deadline and guarded by the
// per-peer single-flight flag in replicateAllWithDeadline.
func (n *Node) replicatePeer(peer string, deadline time.Time) {
	for {
		if time.Now().After(deadline) {
			return
		}
		n.mu.Lock()
		if n.Role() != Leader {
			n.mu.Unlock()
			return
		}
		next := n.nextIndex[peer]
		if next == 0 {
			next = n.lastLogIndex + 1
			n.nextIndex[peer] = next
		}
		if next <= n.snapshotIndex {
			n.mu.Unlock()
			n.installSnapshotToPeer(peer, deadline)
			return
		}
		prevIndex := next - 1
		req := AppendEntriesReq{
			Term:         n.term,
			LeaderID:     n.id,
			PrevLogIndex: prevIndex,
			PrevLogTerm:  n.termAtLocked(prevIndex),
			CommitIdx:    n.commitIdx,
		}
		if next <= n.lastLogIndex {
			offset, ok := n.offsetOfLocked(next)
			if !ok {
				n.mu.Unlock()
				return
			}
			req.Entries = append([]LogEntry(nil), n.logs[offset:]...)
		}
		n.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		resp, err := n.trans.sendAppend(ctx, peer, req)
		cancel()
		if err != nil {
			return
		}

		applyNeeded := false
		n.mu.Lock()
		if resp.Term > n.term {
			n.stepDownLocked(resp.Term)
			n.flushMeta()
			n.mu.Unlock()
			return
		}
		if n.Role() != Leader || req.Term != n.term {
			n.mu.Unlock()
			return
		}

		if resp.Success {
			match := resp.MatchIndex
			if match > n.lastLogIndex {
				match = n.lastLogIndex
			}
			n.matchIndex[peer] = match
			n.nextIndex[peer] = match + 1
			applyNeeded = n.advanceCommitLocked()
			more := n.nextIndex[peer] <= n.lastLogIndex
			n.mu.Unlock()

			if applyNeeded {
				_ = n.applyCommittedEntries()
				n.maybeSnapshot()
			}
			if !more {
				return
			}
			continue // pipeline the next batch in the same round
		}

		// Failure: the follower rejected the append. If its log is shorter
		// than what we sent, jump straight to its last index+1; otherwise
		// the mismatch is a term conflict, and retrying immediately would
		// decrement one index per round trip. Back off to the next heartbeat
		// round instead (P2-15).
		jumped := false
		if resp.LastLogIndex+1 < n.nextIndex[peer] {
			n.nextIndex[peer] = resp.LastLogIndex + 1
			jumped = true
		} else if n.nextIndex[peer] > 1 {
			n.nextIndex[peer]--
		}
		n.mu.Unlock()
		if !jumped {
			return
		}
	}
}

func (n *Node) maybeSnapshot() {
	if !n.snapshotEnabled {
		return
	}

	n.mu.Lock()
	shouldSnapshot := n.snapshotThreshold > 0 && n.lastApply > n.snapshotIndex && (n.lastApply-n.snapshotIndex) >= n.snapshotThreshold
	snapshotIndex := n.lastApply
	snapshotTerm := n.termAtLocked(snapshotIndex)
	n.mu.Unlock()
	if !shouldSnapshot {
		return
	}
	_ = n.createSnapshot(snapshotIndex, snapshotTerm)
}

func (n *Node) createSnapshot(index, term uint64) error {
	snapshotter, ok := n.applier.(SnapshotProvider)
	if !ok {
		return nil
	}
	// Freeze the FSM: no apply can interleave with snapshot capture, so the
	// snapshot data is guaranteed to cover exactly up to index.
	n.applyMu.Lock()
	defer n.applyMu.Unlock()
	data, err := snapshotter.Snapshot(n.id)
	if err != nil {
		return err
	}
	meta := SnapshotMeta{
		LastIncludedIndex: index,
		LastIncludedTerm:  term,
	}
	if err := n.storage.SaveSnapshot(meta, data); err != nil {
		return err
	}

	n.mu.Lock()
	if index <= n.snapshotIndex {
		n.mu.Unlock()
		return nil
	}
	n.snapshotIndex = index
	n.snapshotTerm = term
	metrics.SetRaftSnapshotAge(0)
	if n.lastApply < index {
		n.lastApply = index
	}
	if n.commitIdx < index {
		n.commitIdx = index
	}
	remaining := make([]LogEntry, 0, len(n.logs))
	for _, entry := range n.logs {
		if entry.Index > index {
			remaining = append(remaining, entry)
		}
	}
	n.logs = remaining
	n.recomputeLastLogLocked()
	n.flushMeta()
	n.mu.Unlock()

	return n.storage.CompactLog(index)
}

func (n *Node) installSnapshotToPeer(peer string, deadline time.Time) {
	meta, data, err := n.storage.LoadSnapshot()
	if err != nil || meta == nil {
		return
	}
	n.mu.Lock()
	term := n.term
	leaderID := n.id
	n.mu.Unlock()

	// Stream the snapshot in bounded chunks (P2-14). Each chunk has its own
	// timeout; if any chunk fails the follower keeps its partial state and
	// the next round retries from offset 0.
	for offset := 0; offset < len(data); offset += installSnapshotChunkSize {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		end := offset + installSnapshotChunkSize
		if end > len(data) {
			end = len(data)
		}
		ctx, cancel := context.WithTimeout(context.Background(), remaining)
		resp, err := n.trans.sendInstallSnapshot(ctx, peer, InstallSnapshotReq{
			Term:              term,
			LeaderID:          leaderID,
			LastIncludedIndex: meta.LastIncludedIndex,
			LastIncludedTerm:  meta.LastIncludedTerm,
			Data:              data[offset:end],
			Offset:            uint64(offset),
			Done:              end == len(data),
		})
		cancel()
		if err != nil {
			return
		}
		if resp.Term > term {
			n.mu.Lock()
			n.stepDownLocked(resp.Term)
			n.flushMeta()
			n.mu.Unlock()
			return
		}
		if !resp.Success {
			return
		}
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Role() == Leader && n.term == term {
		n.matchIndex[peer] = meta.LastIncludedIndex
		n.nextIndex[peer] = meta.LastIncludedIndex + 1
	}
}

// onInstallSnapshot handles a (possibly chunked) InstallSnapshot RPC. Lock
// acquisition is isolated (panic-safe); chunks accumulate in order under n.mu
// and, once Done, the FSM restore runs under applyMu so it can never
// interleave with a concurrent apply. A snapshot older than what was already
// applied is rejected.
func (n *Node) onInstallSnapshot(req InstallSnapshotReq) InstallSnapshotResp {
	// Phase 1: term/leader/role bookkeeping + chunk accumulation.
	var snap *pendingSnapshot
	resp, proceed := func() (InstallSnapshotResp, bool) {
		n.mu.Lock()
		defer n.mu.Unlock()
		if req.Term < n.term {
			return InstallSnapshotResp{Term: n.term, Success: false}, false
		}
		if req.Term > n.term {
			n.stepDownLocked(req.Term)
		}
		n.leaderID.Store(req.LeaderID)
		n.role.Store(Follower)
		metrics.SetRaftRole(n.id, string(Follower))
		n.resetElectionDeadline()

		if req.Offset == 0 || n.pendingSnapshot == nil || n.pendingSnapshot.lastIncludedIndex != req.LastIncludedIndex {
			// New transfer (or leader restarting from offset 0).
			n.pendingSnapshot = &pendingSnapshot{
				lastIncludedIndex: req.LastIncludedIndex,
				lastIncludedTerm:  req.LastIncludedTerm,
			}
		}
		if req.Offset != uint64(len(n.pendingSnapshot.buf)) {
			// Out-of-order, duplicate or corrupted chunk: reset so the leader
			// retries from offset 0.
			n.pendingSnapshot = nil
			return InstallSnapshotResp{Term: n.term, Success: false}, false
		}
		if uint64(len(n.pendingSnapshot.buf))+uint64(len(req.Data)) > uint64(maxPendingSnapshotBytes) {
			// The accumulated snapshot exceeds the cap: drop it and fail so
			// the leader retries from offset 0 (and never OOMs this node).
			n.pendingSnapshot = nil
			return InstallSnapshotResp{Term: n.term, Success: false}, false
		}
		n.pendingSnapshot.buf = append(n.pendingSnapshot.buf, req.Data...)
		if !req.Done {
			return InstallSnapshotResp{Term: n.term, Success: true}, false
		}
		snap = n.pendingSnapshot
		n.pendingSnapshot = nil
		return InstallSnapshotResp{}, true
	}()
	if !proceed {
		return resp
	}

	// Phase 2: restore the FSM under the apply barrier.
	n.applyMu.Lock()
	defer n.applyMu.Unlock()
	snapshotter, ok := n.applier.(SnapshotProvider)
	if !ok {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}
	n.mu.Lock()
	stale := snap.lastIncludedIndex < n.lastApply
	n.mu.Unlock()
	if stale {
		// Defensive: a snapshot behind the already-applied point would wipe
		// newer state; reject it and let the leader retry with its latest.
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}
	if err := snapshotter.RestoreSnapshot(n.id, snap.buf); err != nil {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}
	// A successful restore repairs any fatal apply error.
	n.applyErr.Store(nil)
	if err := n.storage.SaveSnapshot(SnapshotMeta{
		LastIncludedIndex: snap.lastIncludedIndex,
		LastIncludedTerm:  snap.lastIncludedTerm,
	}, snap.buf); err != nil {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}

	// Phase 3: update state under n.mu.
	n.mu.Lock()
	n.snapshotIndex = snap.lastIncludedIndex
	n.snapshotTerm = snap.lastIncludedTerm
	filtered := make([]LogEntry, 0, len(n.logs))
	for _, entry := range n.logs {
		if entry.Index > snap.lastIncludedIndex {
			filtered = append(filtered, entry)
		}
	}
	n.logs = filtered
	if n.commitIdx < snap.lastIncludedIndex {
		n.commitIdx = snap.lastIncludedIndex
	}
	if n.lastApply < snap.lastIncludedIndex {
		n.lastApply = snap.lastIncludedIndex
	}
	n.recomputeLastLogLocked()
	n.flushMeta()
	n.mu.Unlock()

	_ = n.storage.CompactLog(snap.lastIncludedIndex)
	return InstallSnapshotResp{Term: req.Term, Success: true}
}

// advanceCommitLocked advances commitIdx to the highest index replicated by
// a majority and belonging to the current term (Raft §5.4.2). It derives the
// candidate from the majority-th largest match index instead of scanning the
// whole log on every heartbeat (P2-15).
func (n *Node) advanceCommitLocked() bool {
	peers := n.trans.Peers()
	majority := len(peers)/2 + 1
	matches := make([]uint64, 0, len(peers))
	matches = append(matches, n.lastLogIndex) // self
	for peer, matched := range n.matchIndex {
		if peer != n.id {
			matches = append(matches, matched)
		}
	}
	if len(matches) < majority {
		return false
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i] > matches[j] })
	cand := matches[majority-1]
	if cand > n.lastLogIndex {
		cand = n.lastLogIndex
	}
	for idx := cand; idx > n.commitIdx; idx-- {
		if n.termAtLocked(idx) == n.term {
			n.commitIdx = idx
			metrics.SetRaftCommitIndex(n.commitIdx)
			n.flushMeta()
			return true
		}
	}
	return false
}

// applyCommittedEntries applies committed log entries to the FSM. The whole
// loop runs under applyMu so the state machine is never touched concurrently
// by another apply/snapshot/restore. lastApply is advanced only after a
// successful apply; an apply error is fatal (the node refuses further
// Submit/ReadIndex until a snapshot restore repairs the FSM).
func (n *Node) applyCommittedEntries() error {
	n.applyMu.Lock()
	defer n.applyMu.Unlock()
	for {
		n.mu.Lock()
		if n.applyFailed() {
			n.mu.Unlock()
			return errors.New("state machine apply failed")
		}
		if n.lastApply >= n.commitIdx {
			n.mu.Unlock()
			return nil
		}
		nextIndex := n.lastApply + 1
		if nextIndex <= n.snapshotIndex {
			n.lastApply = n.snapshotIndex
			n.mu.Unlock()
			continue
		}
		entry, ok := n.entryAtLocked(nextIndex)
		if !ok {
			n.mu.Unlock()
			return n.failApply(errors.New("missing committed log entry"))
		}
		waiter := n.applyWaiter[entry.Index]
		if waiter != nil {
			delete(n.applyWaiter, entry.Index)
		}
		n.mu.Unlock()

		resp, err := n.applyEntry(entry)

		n.mu.Lock()
		if err != nil {
			n.mu.Unlock()
			n.failApply(err)
			if waiter != nil {
				waiter <- applyResult{resp: resp, err: err}
				close(waiter)
			}
			return err
		}
		n.lastApply = entry.Index
		metrics.SetRaftLastApplied(n.lastApply)
		if n.commitIdx >= n.lastApply {
			metrics.SetRaftPendingEntries(int(n.commitIdx - n.lastApply))
		}
		n.mu.Unlock()

		if waiter != nil {
			waiter <- applyResult{resp: resp, err: err}
			close(waiter)
		}
	}
}

func (n *Node) applyEntry(entry LogEntry) (interface{}, error) {
	switch entry.Type {
	case EntryTypeCommand:
		cmd, err := n.decodeCommandEntry(entry)
		if err != nil {
			return nil, err
		}
		return n.applier.Apply(cmd)
	case EntryTypeAddPeer:
		return nil, n.applyPeerChange(entry, false)
	case EntryTypeRemovePeer:
		return nil, n.applyPeerChange(entry, true)
	case EntryTypeNoop:
		return nil, nil
	default:
		return nil, errors.New("unsupported raft entry type")
	}
}

func (n *Node) applyPeerChange(entry LogEntry, remove bool) error {
	var change PeerChange
	if err := json.Unmarshal(entry.Data, &change); err != nil {
		return err
	}
	if change.Addr == "" {
		return errors.New("peer change missing addr")
	}

	var updated bool
	if remove {
		updated = n.trans.RemovePeer(change.Addr)
	} else {
		updated = n.trans.AddPeer(change.Addr)
	}
	if updated {
		metrics.SetPeersTotal(len(n.trans.Peers()))
	}

	n.mu.Lock()
	if remove {
		delete(n.nextIndex, change.Addr)
		delete(n.matchIndex, change.Addr)
	} else {
		n.nextIndex[change.Addr] = n.lastLogIndex + 1
	}
	n.flushMeta()
	n.mu.Unlock()
	return nil
}

func (n *Node) newCommandEntry(cmd interface{}) (LogEntry, error) {
	kind, payload, err := command.Encode(cmd)
	if err != nil {
		return LogEntry{}, err
	}
	data, err := json.Marshal(commandEntryData{
		Kind:    kind,
		Payload: payload,
	})
	if err != nil {
		return LogEntry{}, err
	}
	return LogEntry{
		Type: EntryTypeCommand,
		Data: data,
	}, nil
}

func (n *Node) decodeCommandEntry(entry LogEntry) (interface{}, error) {
	var payload commandEntryData
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		return nil, err
	}
	return command.Decode(payload.Kind, payload.Payload)
}

func (n *Node) appendEntryLocked(entry LogEntry) error {
	if err := n.storage.AppendEntry(entry); err != nil {
		return err
	}
	n.logs = append(n.logs, entry)
	n.lastLogIndex = entry.Index
	n.lastLogTerm = entry.Term
	return nil
}

func (n *Node) recomputeLastLogLocked() {
	if len(n.logs) == 0 {
		n.lastLogIndex = n.snapshotIndex
		n.lastLogTerm = n.snapshotTerm
		return
	}
	last := n.logs[len(n.logs)-1]
	n.lastLogIndex = last.Index
	n.lastLogTerm = last.Term
}

func (n *Node) termAtLocked(index uint64) uint64 {
	if index == 0 {
		return 0
	}
	if index == n.snapshotIndex {
		return n.snapshotTerm
	}
	entry, ok := n.entryAtLocked(index)
	if !ok {
		return 0
	}
	return entry.Term
}

func (n *Node) resetLeaderProgressLocked() {
	peers := n.trans.Peers()
	for _, peer := range peers {
		n.nextIndex[peer] = n.lastLogIndex + 1
		n.matchIndex[peer] = 0
	}
	n.matchIndex[n.id] = n.lastLogIndex
	n.nextIndex[n.id] = n.lastLogIndex + 1
}

func (n *Node) majorityLocked() int {
	return len(n.trans.Peers())/2 + 1
}

func (n *Node) metaLocked() *Meta {
	return &Meta{
		CurrentTerm:   n.term,
		VotedFor:      n.votedFor,
		CommitIndex:   n.commitIdx,
		Peers:         n.trans.Peers(),
		SnapshotIndex: n.snapshotIndex,
		SnapshotTerm:  n.snapshotTerm,
	}
}

func (n *Node) stepDownLocked(term uint64) {
	n.term = term
	n.votedFor = ""
	n.role.Store(Follower)
	n.leaderID.Store("")
	metrics.SetRaftRole(n.id, string(Follower))
	// Notify and clean up any pending command waiters so they don't leak
	for idx, w := range n.applyWaiter {
		w <- applyResult{resp: nil, err: ErrNotLeader{Leader: ""}}
		close(w)
		delete(n.applyWaiter, idx)
		// Use the locked (no-jitter) deadline reset so this node waits the
		// full election timeout before starting a new election.  This gives
		// the new leader at least election_ms to send heartbeats and stabilise
		// the cluster, preventing the perpetual leapfrog cycle.
		n.resetElectionDeadlineLocked()
	}
}

// ReadIndex performs a quorum check and returns the current safe commit
// index.  It implements the Raft ReadIndex protocol: the leader records its
// commit index, sends a no-op heartbeat to confirm it is still the leader,
// and returns the commit index when a majority has acknowledged.
func (n *Node) ReadIndex(ctx context.Context) (uint64, error) {
	if n.Role() != Leader {
		return 0, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	// Record the current commit index.
	n.mu.Lock()
	idx := n.commitIdx
	n.mu.Unlock()
	// Perform a quorum heartbeat to confirm leadership.
	if err := n.heartbeatRound(ctx); err != nil {
		return 0, err
	}
	// Re-check role after the heartbeat round.
	if n.Role() != Leader {
		return 0, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	// Wait until the state machine has applied up to idx so the read cannot
	// miss committed-but-not-yet-applied entries (linearizability).
	deadline := time.Now().Add(time.Second)
	for {
		n.mu.Lock()
		applied := n.lastApply
		isLeader := n.Role() == Leader
		n.mu.Unlock()
		if !isLeader {
			return 0, ErrNotLeader{Leader: n.LeaderID()}
		}
		if applied >= idx {
			return idx, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("read index apply lag: applied=%d want=%d", applied, idx)
		}
	}
}

// heartbeatRound sends an empty AppendEntries (heartbeat) to all followers
// and waits for a majority to acknowledge within the context deadline.
func (n *Node) heartbeatRound(ctx context.Context) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, n.hb*2)
		defer cancel()
		deadline, _ = ctx.Deadline()
	}
	type ack struct {
		term uint64
		err  error
	}
	peers := n.trans.Peers()
	ch := make(chan ack, len(peers))
	// Send heartbeats concurrently.
	for _, peer := range peers {
		if n.trans.isSelf(peer) {
			continue
		}
		go func(p string) {
			n.mu.Lock()
			req := AppendEntriesReq{
				Term:         n.term,
				LeaderID:     n.id,
				PrevLogIndex: n.lastLogIndex,
				PrevLogTerm:  n.lastLogTerm,
				CommitIdx:    n.commitIdx,
			}
			n.mu.Unlock()
			pctx, cancel := context.WithDeadline(ctx, deadline)
			defer cancel()
			resp, err := n.trans.sendAppend(pctx, p, req)
			ch <- ack{term: resp.Term, err: err}
		}(peer)
	}
	// Count responses. An ack counts only when the follower answered with our
	// term; a higher-term response means we have been deposed and must step
	// down immediately so the read fails instead of serving stale data.
	votes := 1 // self-vote
	total := len(peers)
	for i := 0; i < total-1; i++ {
		select {
		case a := <-ch:
			if a.err != nil {
				continue
			}
			n.mu.Lock()
			if a.term > n.term {
				n.stepDownLocked(a.term)
				n.flushMeta()
				n.mu.Unlock()
				return ErrNotLeader{Leader: ""}
			}
			if a.term == n.term {
				votes++
			}
			n.mu.Unlock()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if votes >= total/2+1 {
		return nil
	}
	return fmt.Errorf("heartbeat round failed: got %d/%d votes", votes, total/2+1)
}

// StepDown forces the current leader to step down, incrementing the term
// so that a new leader election is triggered. Returns an error if the
// node is not the leader.
func (n *Node) StepDown() error {
	if n.Role() != Leader {
		return ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.Role() != Leader {
		return ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	n.term++
	n.votedFor = ""
	n.role.Store(Follower)
	n.leaderID.Store("")
	metrics.SetRaftRole(n.id, string(Follower))
	for idx, w := range n.applyWaiter {
		w <- applyResult{resp: nil, err: ErrNotLeader{Leader: ""}}
		close(w)
		delete(n.applyWaiter, idx)
	}
	n.flushMeta()
	return nil
}

type ErrNotLeader struct{ Leader string }

func (e ErrNotLeader) Error() string { return "not leader" }

// GRPCStatus implements the GRPCStatus interface so that gRPC serializes
// ErrNotLeader as codes.FailedPrecondition instead of codes.Unknown.
// The leader ID is included in the message for client-side redirection.
func (e ErrNotLeader) GRPCStatus() *status.Status {
	msg := "not leader"
	if e.Leader != "" {
		msg = fmt.Sprintf("not leader, leader is %s", e.Leader)
	}
	return status.New(codes.FailedPrecondition, msg)
}

type ErrCommit struct{}

func (e ErrCommit) Error() string { return "commit failed" }

type ErrPeerExists struct{}

func (e ErrPeerExists) Error() string { return "peer already exists" }

type ErrPeerNotFound struct{}

func (e ErrPeerNotFound) Error() string { return "peer not found" }

type ErrInvalidPeerChange struct{}

func (e ErrInvalidPeerChange) Error() string { return "invalid peer change" }

type ErrPeerChangeInFlight struct{}

func (e ErrPeerChangeInFlight) Error() string { return "peer change already in flight" }

func (n *Node) AddPeer(addr string) error {
	return n.SubmitPeerChange(addr, false)
}

func (n *Node) RemovePeer(addr string) error {
	return n.SubmitPeerChange(addr, true)
}

func (n *Node) Peers() []string {
	return n.trans.Peers()
}

func containsPeer(peers []string, addr string) bool {
	for _, peer := range peers {
		if peer == addr {
			return true
		}
	}
	return false
}

// offsetOfLocked returns the offset of a log index inside n.logs (which is
// indexed relative to snapshotIndex+1), or false when out of range.
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

func (n *Node) entryAtLocked(index uint64) (LogEntry, bool) {
	offset, ok := n.offsetOfLocked(index)
	if !ok {
		return LogEntry{}, false
	}
	return n.logs[offset], true
}

func (n *Node) truncateLogFromLocked(index uint64) {
	if index <= n.snapshotIndex {
		n.logs = nil
		n.recomputeLastLogLocked()
		return
	}
	offset, ok := n.offsetOfLocked(index)
	if !ok {
		return
	}
	n.logs = append([]LogEntry(nil), n.logs[:offset]...)
	n.recomputeLastLogLocked()
}

// flushMeta synchronously persists the current meta. Callers must hold n.mu.
// On failure the dirty flag is left set so the background flusher retries
// (a lost term/vote would be a safety violation, not just an optimisation).
func (n *Node) flushMeta() {
	if err := n.storage.SaveMeta(n.metaLocked()); err != nil {
		n.metaDirty.Store(true)
		if n.logger != nil {
			n.logger.Warn("raft persist meta failed", zap.String("node", n.id), zap.Error(err))
		}
		return
	}
	n.metaDirty.Store(false)
}

// markMetaDirty requests an asynchronous meta persist by the background flusher.
func (n *Node) markMetaDirty() { n.metaDirty.Store(true) }

// raftApplyError wraps a fatal state-machine apply error. It is stored in
// Node.applyErr (atomic.Pointer) so it can be cleared by storing nil.
type raftApplyError struct{ err error }

// failApply records a fatal state-machine apply error. Once set, the node
// refuses further Submit/ReadIndex/apply until a snapshot restore clears it.
func (n *Node) failApply(err error) error {
	n.applyErr.Store(&raftApplyError{err: err})
	return err
}

func (n *Node) applyFailed() bool { return n.applyErr.Load() != nil }
