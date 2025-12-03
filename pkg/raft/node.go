package raft

import (
	"encoding/json"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/lushenle/simple-cache/pkg/metrics"
	"go.uber.org/zap"
)

type Applier interface {
	Apply(cmd interface{}) (interface{}, error)
}

type Node struct {
	id               string
	role             atomic.Value
	term             uint64
	leaderID         atomic.Value
	commitIdx        uint64
	lastApply        uint64
	storage          *Storage
	trans            *HTTPTransport
	applier          Applier
	hb               time.Duration
	el               time.Duration
	votedFor         string
	electionDeadline time.Time
	rnd              *rand.Rand
	logger           *zap.Logger
}

func NewNode(id string, addr string, peers []string, storage *Storage, applier Applier, heartbeat, election time.Duration, logger *zap.Logger) *Node {
	n := &Node{id: id, storage: storage, applier: applier, hb: heartbeat, el: election, logger: logger}
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
	go n.loop()
	go n.electionLoop()
	return n
}

func (n *Node) Role() Role { return n.role.Load().(Role) }

func (n *Node) loop() {
	ticker := time.NewTicker(n.hb)
	defer ticker.Stop()
	for range ticker.C {
		if n.Role() == Leader {
			start := time.Now()
			req := AppendEntriesReq{Term: n.term, LeaderID: n.id, CommitIdx: n.commitIdx}
			n.trans.broadcastAppend(req)
			metrics.ObserveAppendEntriesLatency(time.Since(start))
			if n.logger != nil {
				n.logger.Debug("raft heartbeat", zap.String("node", n.id), zap.Uint64("term", n.term), zap.Uint64("commit_index", n.commitIdx))
			}
		}
	}
}

func (n *Node) electionLoop() {
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for range tick.C {
		if n.Role() == Leader {
			continue
		}
		if time.Now().After(n.electionDeadline) {
			if n.logger != nil {
				n.logger.Info("election timeout", zap.String("node", n.id), zap.Uint64("term", n.term))
			}
			n.startElection()
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
	n.term++
	_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	n.votedFor = n.id
	_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	votes := 1
	req := RequestVoteReq{Term: n.term, CandidateID: n.id, LastLogIndex: n.commitIdx, LastLogTerm: n.term}
	if n.logger != nil {
		n.logger.Debug("start election", zap.String("candidate", n.id), zap.Uint64("term", n.term), zap.Int("peers", len(n.trans.peers)))
	}
	votes += n.trans.broadcastVote(req)
	total := len(n.trans.peers)
	if n.logger != nil {
		n.logger.Debug("vote result", zap.String("candidate", n.id), zap.Int("votes", votes), zap.Int("total", total), zap.Int("majority", total/2+1))
	}
	if votes >= (total/2 + 1) {
		n.role.Store(Leader)
		metrics.SetRaftRole(n.id, string(Leader))
		n.leaderID.Store(n.id)
		if n.logger != nil {
			n.logger.Info("become leader", zap.String("node", n.id), zap.Uint64("term", n.term))
		}
	}
	n.resetElectionDeadline()
}

func (n *Node) Submit(cmd interface{}) (interface{}, error) {
	if n.Role() != Leader {
		return nil, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	b, _ := json.Marshal(cmd)
	if err := n.storage.Append(b); err != nil {
		return nil, err
	}
	start := time.Now()
	ack := n.trans.broadcastAppend(AppendEntriesReq{Term: n.term, LeaderID: n.id, Entries: [][]byte{b}, CommitIdx: n.commitIdx + 1})
	metrics.ObserveAppendEntriesLatency(time.Since(start))
	if ack < 1 {
		return nil, ErrCommit{}
	}
	n.commitIdx++
	metrics.SetRaftCommitIndex(n.commitIdx)
	_ = n.storage.SaveMeta(&Meta{CurrentTerm: n.term, VotedFor: n.votedFor})
	return n.applier.Apply(cmd)
}

func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
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
	for _, e := range req.Entries {
		_ = n.storage.Append(e)
	}
	n.commitIdx = req.CommitIdx
	metrics.SetRaftCommitIndex(n.commitIdx)
	metrics.SetRaftLastApplied(n.commitIdx)
	n.resetElectionDeadline()
	if n.logger != nil {
		n.logger.Debug("append entries received", zap.String("node", n.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term), zap.Uint64("commit_index", n.commitIdx))
	}
	return AppendEntriesResp{Term: n.term, Success: true}
}

func (n *Node) onRequestVote(req RequestVoteReq) RequestVoteResp {
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
	// up-to-date check: candidate's last log index must be >= ours
	if req.LastLogIndex < n.commitIdx {
		if n.logger != nil {
			n.logger.Debug("vote denied: log not up-to-date", zap.String("node", n.id), zap.Uint64("last_log_index", n.commitIdx), zap.String("candidate", req.CandidateID), zap.Uint64("candidate_last_log_index", req.LastLogIndex))
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
		metrics.SetPeersTotal(len(n.trans.peers))
	}
	return ok
}

func (n *Node) RemovePeer(addr string) bool {
	ok := n.trans.RemovePeer(addr)
	if ok {
		metrics.SetPeersTotal(len(n.trans.peers))
	}
	return ok
}

func (n *Node) Peers() []string {
	out := make([]string, len(n.trans.peers))
	copy(out, n.trans.peers)
	return out
}
