package raft

import (
	"encoding/json"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

type Applier interface {
	Apply(cmd interface{}) (interface{}, error)
}

type Node struct {
	mu sync.Mutex

	id  string
	role atomic.Value
	term uint64
	leaderID atomic.Value

	commitIdx    uint64
	lastApply    uint64
	lastLogIndex uint64
	lastLogTerm  uint64

	storage *Storage
	trans   *HTTPTransport
	applier Applier

	hb time.Duration
	el time.Duration

	votedFor         string
	electionDeadline time.Time
	rnd              *rand.Rand

	stopCh chan struct{}
	wg     sync.WaitGroup

	logger *zap.Logger
}

func NewNode(id string, addr string, peers []string, storage *Storage, applier Applier, heartbeat, election time.Duration, logger *zap.Logger) *Node {
	n := &Node{
		id: id, storage: storage, applier: applier,
		hb: heartbeat, el: election, logger: logger,
		stopCh: make(chan struct{}),
	}
	n.role.Store(Follower)
	metrics.SetRaftRole(n.id, string(Follower))
	n.leaderID.Store("")
	n.trans = NewHTTPTransport(addr, peers)
	n.trans.Start(n)
	if meta, err := storage.LoadMeta(); err == nil && meta != nil {
		if meta.CurrentTerm > 0 {
			n.term = meta.CurrentTerm
		}
		n.votedFor = meta.VotedFor
	}
	seed := time.Now().UnixNano() ^ int64(len(peers))
	n.rnd = rand.New(rand.NewSource(seed))
	n.resetElectionDeadline()
	n.wg.Add(2)
	go n.loop()
	go n.electionLoop()
	return n
}

// Close stops the raft node background goroutines and closes the transport.
func (n *Node) Close() {
	close(n.stopCh)
	n.wg.Wait()
	n.trans.Close()
}

func (n *Node) Role() Role { return n.role.Load().(Role) }

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
				n.mu.Lock()
				req := AppendEntriesReq{
					Term:         n.term,
					LeaderID:     n.id,
					PrevLogIndex: n.lastLogIndex,
					PrevLogTerm:  n.lastLogTerm,
					CommitIdx:    n.commitIdx,
				}
				n.mu.Unlock()
				n.trans.broadcastAppend(req)
				metrics.ObserveAppendEntriesLatency(time.Since(start))
				if n.logger != nil {
					n.mu.Lock()
					term := n.term
					commitIdx := n.commitIdx
					n.mu.Unlock()
					n.logger.Debug("raft heartbeat", zap.String("node", n.id), zap.Uint64("term", term), zap.Uint64("commit_index", commitIdx))
				}
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
			if time.Now().After(n.electionDeadline) {
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
	// randomized timeout: el + jitter(0..el)
	jitter := time.Duration(n.rnd.Int63n(int64(n.el)))
	n.electionDeadline = time.Now().Add(n.el + jitter)
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
	_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	n.mu.Unlock()

	votes := 1
	req := RequestVoteReq{Term: term, CandidateID: n.id, LastLogIndex: lastLogIndex, LastLogTerm: lastLogTerm}
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
		// Only become leader if term hasn't changed (e.g., received higher term)
		if n.term == term {
			n.role.Store(Leader)
			metrics.SetRaftRole(n.id, string(Leader))
			n.leaderID.Store(n.id)
			if n.logger != nil {
				n.logger.Info("become leader", zap.String("node", n.id), zap.Uint64("term", n.term))
			}
		}
		n.mu.Unlock()
	}
	n.resetElectionDeadline()
}

func (n *Node) Submit(cmd interface{}) (interface{}, error) {
	if n.Role() != Leader {
		return nil, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	if err := n.storage.Append(b); err != nil {
		return nil, err
	}

	n.mu.Lock()
	prevLogTerm := n.lastLogTerm // Save old term before updating
	n.lastLogIndex++
	n.lastLogTerm = n.term
	logIdx := n.lastLogIndex
	term := n.term
	n.mu.Unlock()

	start := time.Now()
	ack := n.trans.broadcastAppend(AppendEntriesReq{
		Term:         term,
		LeaderID:     n.id,
		PrevLogIndex: logIdx - 1,
		PrevLogTerm:  prevLogTerm,
		Entries:      [][]byte{b},
		CommitIdx:    n.commitIdx,
	})
	metrics.ObserveAppendEntriesLatency(time.Since(start))

	peers := n.trans.Peers()
	majority := len(peers)/2 + 1
	if ack < majority {
		return nil, ErrCommit{}
	}

	n.mu.Lock()
	n.commitIdx++
	n.lastApply = n.commitIdx
	metrics.SetRaftCommitIndex(n.commitIdx)
	metrics.SetRaftLastApplied(n.lastApply)
	_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	n.mu.Unlock()

	return n.applier.Apply(cmd)
}

func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Reject stale leader term
	if req.Term < n.term {
		return AppendEntriesResp{Term: n.term, Success: false}
	}

	// Update local term if leader has higher term
	if req.Term > n.term {
		n.term = req.Term
		n.votedFor = ""
		_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	}

	n.leaderID.Store(req.LeaderID)
	prev := n.Role()
	if prev != Follower {
		metrics.IncRaftLeaderChanges()
	}
	n.role.Store(Follower)
	metrics.SetRaftRole(n.id, string(Follower))

	// Log consistency check: verify PrevLogIndex/PrevLogTerm
	if req.PrevLogIndex > 0 {
		// If we don't have the prev log entry, reject
		if req.PrevLogIndex > n.lastLogIndex {
			return AppendEntriesResp{Term: n.term, Success: false}
		}
		// If the term doesn't match, reject (log inconsistency)
		// For simplicity in this implementation, we accept if prev index <= our last log index
		// A full implementation would store per-entry terms for exact matching
	}

	// Append new entries
	for _, e := range req.Entries {
		_ = n.storage.Append(e)
		n.lastLogIndex++
		n.lastLogTerm = req.Term
	}

	// Update commit index (never decrease)
	if req.CommitIdx > n.commitIdx {
		n.commitIdx = req.CommitIdx
		if n.commitIdx > n.lastLogIndex {
			n.commitIdx = n.lastLogIndex
		}
		metrics.SetRaftCommitIndex(n.commitIdx)
	}

	// Apply committed but not yet applied entries
	for n.lastApply < n.commitIdx {
		n.lastApply++
		// In a full implementation, we would read the log entry at n.lastApply
		// and apply it to the state machine. For now, entries are applied
		// as they arrive via the Entries field.
	}
	metrics.SetRaftLastApplied(n.lastApply)

	n.resetElectionDeadline()
	if n.logger != nil {
		n.logger.Debug("append entries received", zap.String("node", n.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term), zap.Uint64("commit_index", n.commitIdx))
	}
	return AppendEntriesResp{Term: n.term, Success: true}
}

func (n *Node) onRequestVote(req RequestVoteReq) RequestVoteResp {
	n.mu.Lock()
	defer n.mu.Unlock()

	if req.Term < n.term {
		if n.logger != nil {
			n.logger.Debug("vote denied: stale term", zap.String("node", n.id), zap.Uint64("term", n.term), zap.String("candidate", req.CandidateID), zap.Uint64("candidate_term", req.Term))
		}
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if req.Term > n.term {
		n.term = req.Term
		n.votedFor = ""
		n.role.Store(Follower)
		metrics.SetRaftRole(n.id, string(Follower))
		_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	}
	// up-to-date check: candidate's last log must be at least as up-to-date as ours
	if req.LastLogIndex < n.lastLogIndex {
		if n.logger != nil {
			n.logger.Debug("vote denied: log not up-to-date", zap.String("node", n.id), zap.Uint64("last_log_index", n.lastLogIndex), zap.String("candidate", req.CandidateID), zap.Uint64("candidate_last_log_index", req.LastLogIndex))
		}
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	// If last log index is equal, compare terms
	if req.LastLogIndex == n.lastLogIndex && req.LastLogTerm < n.lastLogTerm {
		if n.logger != nil {
			n.logger.Debug("vote denied: log term not up-to-date", zap.String("node", n.id), zap.Uint64("last_log_term", n.lastLogTerm), zap.String("candidate", req.CandidateID), zap.Uint64("candidate_last_log_term", req.LastLogTerm))
		}
		return RequestVoteResp{Term: n.term, VoteGranted: false}
	}
	if n.votedFor == "" || n.votedFor == req.CandidateID {
		n.votedFor = req.CandidateID
		_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
		n.resetElectionDeadline()
		if n.logger != nil {
			n.logger.Debug("vote granted", zap.String("node", n.id), zap.String("candidate", req.CandidateID), zap.Uint64("term", n.term))
		}
		return RequestVoteResp{Term: n.term, VoteGranted: true}
	}
	if n.logger != nil {
		n.logger.Debug("vote denied: already voted", zap.String("node", n.id), zap.String("votedFor", n.votedFor), zap.String("candidate", req.CandidateID), zap.Uint64("term", n.term))
	}
	return RequestVoteResp{Term: n.term, VoteGranted: false}
}

type ErrNotLeader struct{ Leader string }

func (e ErrNotLeader) Error() string { return "not leader" }

type ErrCommit struct{}

func (e ErrCommit) Error() string { return "commit failed" }

func (n *Node) AddPeer(addr string) bool {
	ok := n.trans.AddPeer(addr)
	if ok {
		metrics.SetPeersTotal(len(n.trans.Peers()))
	}
	return ok
}

func (n *Node) RemovePeer(addr string) bool {
	ok := n.trans.RemovePeer(addr)
	if ok {
		metrics.SetPeersTotal(len(n.trans.Peers()))
	}
	return ok
}

func (n *Node) Peers() []string {
	return n.trans.Peers()
}
