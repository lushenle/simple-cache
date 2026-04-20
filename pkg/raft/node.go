package raft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
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
}

func NewNode(id string, addr string, peers []string, storage *Storage, applier Applier, heartbeat, election time.Duration, snapshotEnabled bool, snapshotThreshold uint64, logger *zap.Logger) (*Node, error) {
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

	trans, err := NewHTTPTransport(addr, peers, logger)
	if err != nil {
		return nil, fmt.Errorf("raft transport: %w", err)
	}
	n.trans = trans
	n.trans.Start(n)
	metrics.SetPeersTotal(len(n.trans.Peers()))

	seed := time.Now().UnixNano() ^ int64(len(peers))
	n.rnd = rand.New(rand.NewSource(seed))
	n.resetElectionDeadline()
	n.resetLeaderProgressLocked()
	_ = n.applyCommittedEntries()

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

func (n *Node) startElection() {
	n.role.Store(Candidate)
	metrics.SetRaftRole(n.id, string(Candidate))

	n.mu.Lock()
	n.term++
	n.votedFor = n.id
	term := n.term
	lastLogIndex := n.lastLogIndex
	lastLogTerm := n.lastLogTerm
	_ = n.storage.SaveMeta(n.metaLocked())
	n.mu.Unlock()

	votes := 1
	req := RequestVoteReq{
		Term:         term,
		CandidateID:  n.id,
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
	}
	if n.logger != nil {
		n.logger.Debug("start election", zap.String("candidate", n.id), zap.Uint64("term", term), zap.Int("peers", len(n.trans.Peers())))
	}
	votes += n.trans.broadcastVote(req)
	total := len(n.trans.Peers())
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
			if n.logger != nil {
				n.logger.Info("become leader", zap.String("node", n.id), zap.Uint64("term", n.term))
			}
		}
		n.mu.Unlock()
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
		_ = n.storage.SaveMeta(n.metaLocked())
	}
	n.mu.Unlock()

	if n.majorityLocked() == 1 {
		if err := n.applyCommittedEntries(); err != nil {
			return nil, err
		}
	}

	if err := n.replicateUntilCommitted(entry.Index); err != nil {
		n.mu.Lock()
		delete(n.applyWaiter, entry.Index)
		n.mu.Unlock()
		return nil, err
	}

	select {
	case result := <-waiter:
		return result.resp, result.err
	case <-time.After(2 * time.Second):
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

	n.mu.Lock()
	exists := containsPeer(n.trans.Peers(), normalizedAddr)
	if !remove && exists {
		n.mu.Unlock()
		return ErrPeerExists{}
	}
	if remove && !exists {
		n.mu.Unlock()
		return ErrPeerNotFound{}
	}
	if remove && n.trans.isSelf(normalizedAddr) {
		n.mu.Unlock()
		return ErrInvalidPeerChange{}
	}
	payload, err := json.Marshal(PeerChange{Addr: normalizedAddr})
	if err != nil {
		n.mu.Unlock()
		return err
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
		n.mu.Unlock()
		return err
	}
	n.matchIndex[n.id] = entry.Index
	n.nextIndex[n.id] = entry.Index + 1
	n.mu.Unlock()

	return n.replicateUntilCommitted(entry.Index)
}

func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
	n.mu.Lock()

	if req.Term < n.term {
		resp := AppendEntriesResp{
			Term:         n.term,
			Success:      false,
			LastLogIndex: n.lastLogIndex,
		}
		n.mu.Unlock()
		return resp
	}

	if req.Term > n.term {
		n.stepDownLocked(req.Term)
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
		if req.PrevLogIndex > n.lastLogIndex {
			resp := AppendEntriesResp{
				Term:         n.term,
				Success:      false,
				LastLogIndex: n.lastLogIndex,
			}
			n.mu.Unlock()
			return resp
		}
		if n.termAtLocked(req.PrevLogIndex) != req.PrevLogTerm {
			resp := AppendEntriesResp{
				Term:         n.term,
				Success:      false,
				LastLogIndex: n.lastLogIndex,
			}
			n.mu.Unlock()
			return resp
		}
	}

	changed := false
	oldLastLogIndex := n.lastLogIndex
	for _, entry := range req.Entries {
		if entry.Index <= n.snapshotIndex {
			continue
		}
		if entry.Index <= n.lastLogIndex {
			local, ok := n.entryAtLocked(entry.Index)
			if ok && local.Term != entry.Term {
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
	for _, entry := range req.Entries {
		if entry.Index <= n.lastLogIndex {
			continue
		}
		n.logs = append(n.logs, entry)
	}
	n.recomputeLastLogLocked()
	appendEntries := make([]LogEntry, 0)
	if !changed && n.lastLogIndex > oldLastLogIndex {
		appendEntries = append(appendEntries, n.logs[oldLastLogIndex:]...)
	}
	rewriteNeeded := changed
	var rewriteSnapshot []LogEntry
	if rewriteNeeded {
		rewriteSnapshot = append(rewriteSnapshot, n.logs...)
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
	meta := n.metaLocked()
	matchIndex := n.lastLogIndex
	term := n.term
	commitIdx := n.commitIdx
	n.mu.Unlock()

	if rewriteNeeded {
		if err := n.storage.RewriteEntries(rewriteSnapshot); err != nil {
			return AppendEntriesResp{
				Term:         term,
				Success:      false,
				LastLogIndex: matchIndex,
			}
		}
	} else if len(appendEntries) > 0 {
		if err := n.storage.AppendEntries(appendEntries); err != nil {
			return AppendEntriesResp{
				Term:         term,
				Success:      false,
				LastLogIndex: matchIndex,
			}
		}
	}
	_ = n.storage.SaveMeta(meta)

	if applyNeeded {
		_ = n.applyCommittedEntries()
	}
	n.maybeSnapshot()

	if n.logger != nil {
		n.logger.Debug("append entries received", zap.String("node", n.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term), zap.Uint64("commit_index", commitIdx))
	}
	return AppendEntriesResp{
		Term:         term,
		Success:      true,
		MatchIndex:   matchIndex,
		LastLogIndex: matchIndex,
	}
}

func (n *Node) onRequestVote(req RequestVoteReq) RequestVoteResp {
	n.mu.Lock()
	defer n.mu.Unlock()

	if req.Term < n.term {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if req.Term > n.term {
		n.stepDownLocked(req.Term)
	}
	if req.LastLogIndex < n.lastLogIndex {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if req.LastLogIndex == n.lastLogIndex && req.LastLogTerm < n.lastLogTerm {
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if n.votedFor == "" || n.votedFor == req.CandidateID {
		n.votedFor = req.CandidateID
		_ = n.storage.SaveMeta(n.metaLocked())
		n.resetElectionDeadline()
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
		wg.Add(1)
		sem <- struct{}{}
		go func(target string) {
			defer wg.Done()
			defer func() { <-sem }()
			n.replicatePeer(target, deadline)
		}(peer)
	}
	wg.Wait()
}

func (n *Node) replicatePeer(peer string, deadline time.Time) {
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
		offset := next - n.snapshotIndex - 1
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
		_ = n.storage.SaveMeta(n.metaLocked())
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
	} else {
		if resp.LastLogIndex+1 < n.nextIndex[peer] {
			n.nextIndex[peer] = resp.LastLogIndex + 1
		} else if n.nextIndex[peer] > 1 {
			n.nextIndex[peer]--
		}
	}
	n.mu.Unlock()

	if applyNeeded {
		_ = n.applyCommittedEntries()
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
	metaState := n.metaLocked()
	n.mu.Unlock()

	if err := n.storage.CompactLog(index); err != nil {
		return err
	}
	return n.storage.SaveMeta(metaState)
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
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return
	}
	if remaining > time.Second {
		remaining = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), remaining)
	defer cancel()

	resp, err := n.trans.sendInstallSnapshot(ctx, peer, InstallSnapshotReq{
		Term:              term,
		LeaderID:          leaderID,
		LastIncludedIndex: meta.LastIncludedIndex,
		LastIncludedTerm:  meta.LastIncludedTerm,
		Data:              data,
	})
	if err != nil {
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if resp.Term > n.term {
		n.stepDownLocked(resp.Term)
		_ = n.storage.SaveMeta(n.metaLocked())
		return
	}
	if resp.Success {
		n.matchIndex[peer] = meta.LastIncludedIndex
		n.nextIndex[peer] = meta.LastIncludedIndex + 1
	}
}

func (n *Node) onInstallSnapshot(req InstallSnapshotReq) InstallSnapshotResp {
	n.mu.Lock()
	if req.Term < n.term {
		resp := InstallSnapshotResp{Term: n.term, Success: false}
		n.mu.Unlock()
		return resp
	}
	if req.Term > n.term {
		n.stepDownLocked(req.Term)
	}
	n.leaderID.Store(req.LeaderID)
	n.role.Store(Follower)
	metrics.SetRaftRole(n.id, string(Follower))
	n.resetElectionDeadline()
	n.mu.Unlock()

	snapshotter, ok := n.applier.(SnapshotProvider)
	if !ok {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}
	if err := snapshotter.RestoreSnapshot(n.id, req.Data); err != nil {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}
	if err := n.storage.SaveSnapshot(SnapshotMeta{
		LastIncludedIndex: req.LastIncludedIndex,
		LastIncludedTerm:  req.LastIncludedTerm,
	}, req.Data); err != nil {
		return InstallSnapshotResp{Term: req.Term, Success: false}
	}

	n.mu.Lock()
	n.snapshotIndex = req.LastIncludedIndex
	n.snapshotTerm = req.LastIncludedTerm
	filtered := make([]LogEntry, 0, len(n.logs))
	for _, entry := range n.logs {
		if entry.Index > req.LastIncludedIndex {
			filtered = append(filtered, entry)
		}
	}
	n.logs = filtered
	if n.commitIdx < req.LastIncludedIndex {
		n.commitIdx = req.LastIncludedIndex
	}
	if n.lastApply < req.LastIncludedIndex {
		n.lastApply = req.LastIncludedIndex
	}
	n.recomputeLastLogLocked()
	meta := n.metaLocked()
	n.mu.Unlock()

	_ = n.storage.CompactLog(req.LastIncludedIndex)
	_ = n.storage.SaveMeta(meta)
	return InstallSnapshotResp{Term: req.Term, Success: true}
}

func (n *Node) advanceCommitLocked() bool {
	for idx := n.lastLogIndex; idx > n.commitIdx; idx-- {
		if n.termAtLocked(idx) != n.term {
			continue
		}
		votes := 1
		for peer, matched := range n.matchIndex {
			if peer == n.id {
				continue
			}
			if matched >= idx {
				votes++
			}
		}
		if votes >= n.majorityLocked() {
			n.commitIdx = idx
			metrics.SetRaftCommitIndex(n.commitIdx)
			_ = n.storage.SaveMeta(n.metaLocked())
			return true
		}
	}
	return false
}

func (n *Node) applyCommittedEntries() error {
	for {
		n.mu.Lock()
		if n.lastApply >= n.commitIdx {
			n.mu.Unlock()
			n.maybeSnapshot()
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
			return errors.New("missing committed log entry")
		}
		n.lastApply = nextIndex
		metrics.SetRaftLastApplied(n.lastApply)
		waiter := n.applyWaiter[entry.Index]
		if waiter != nil {
			delete(n.applyWaiter, entry.Index)
		}
		n.mu.Unlock()

		resp, err := n.applyEntry(entry)
		if waiter != nil {
			waiter <- applyResult{resp: resp, err: err}
			close(waiter)
		}
		if err != nil {
			return err
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
	meta := n.metaLocked()
	n.mu.Unlock()
	return n.storage.SaveMeta(meta)
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
}

type ErrNotLeader struct{ Leader string }

func (e ErrNotLeader) Error() string { return "not leader" }

type ErrCommit struct{}

func (e ErrCommit) Error() string { return "commit failed" }

type ErrPeerExists struct{}

func (e ErrPeerExists) Error() string { return "peer already exists" }

type ErrPeerNotFound struct{}

func (e ErrPeerNotFound) Error() string { return "peer not found" }

type ErrInvalidPeerChange struct{}

func (e ErrInvalidPeerChange) Error() string { return "invalid peer change" }

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

func (n *Node) entryAtLocked(index uint64) (LogEntry, bool) {
	if index <= n.snapshotIndex || index > n.lastLogIndex {
		return LogEntry{}, false
	}
	offset := index - n.snapshotIndex - 1
	if offset >= uint64(len(n.logs)) {
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
	offset := index - n.snapshotIndex - 1
	if offset >= uint64(len(n.logs)) {
		return
	}
	n.logs = append([]LogEntry(nil), n.logs[:offset]...)
	n.recomputeLastLogLocked()
}
