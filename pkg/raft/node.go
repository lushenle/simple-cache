package raft

import (
	"encoding/json"
	"sync/atomic"
	"time"
)

type Applier interface {
	Apply(cmd interface{}) (interface{}, error)
}

type Node struct {
	id        string
	role      atomic.Value
	term      uint64
	leaderID  atomic.Value
	commitIdx uint64
	lastApply uint64
	storage   *Storage
	trans     *HTTPTransport
	applier   Applier
	hb        time.Duration
	el        time.Duration
}

func NewNode(id string, addr string, peers []string, storage *Storage, applier Applier, heartbeat, election time.Duration) *Node {
	n := &Node{id: id, storage: storage, applier: applier, hb: heartbeat, el: election}
	n.role.Store(Leader)
	n.leaderID.Store("")
	n.trans = NewHTTPTransport(addr, peers)
	n.trans.Start(n)
	go n.loop()
	return n
}

func (n *Node) Role() Role { return n.role.Load().(Role) }

func (n *Node) loop() {
	ticker := time.NewTicker(n.hb)
	defer ticker.Stop()
	for range ticker.C {
		if n.Role() == Leader {
			req := AppendEntriesReq{Term: n.term, LeaderID: n.id, CommitIdx: n.commitIdx}
			n.trans.broadcastAppend(req)
		}
	}
}

func (n *Node) Submit(cmd interface{}) (interface{}, error) {
	if n.Role() != Leader {
		return nil, ErrNotLeader{Leader: n.leaderID.Load().(string)}
	}
	b, _ := json.Marshal(cmd)
	if err := n.storage.Append(b); err != nil {
		return nil, err
	}
	ack := n.trans.broadcastAppend(AppendEntriesReq{Term: n.term, LeaderID: n.id, Entries: [][]byte{b}, CommitIdx: n.commitIdx + 1})
	if ack < 1 {
		return nil, ErrCommit{}
	}
	n.commitIdx++
	return n.applier.Apply(cmd)
}

func (n *Node) onAppendEntries(req AppendEntriesReq) AppendEntriesResp {
	n.leaderID.Store(req.LeaderID)
	n.role.Store(Follower)
	for _, e := range req.Entries {
		_ = n.storage.Append(e)
	}
	n.commitIdx = req.CommitIdx
	return AppendEntriesResp{Term: n.term, Success: true}
}

func (n *Node) onRequestVote(req RequestVoteReq) RequestVoteResp {
	if req.Term >= n.term {
		n.term = req.Term
		return RequestVoteResp{Term: n.term, VoteGranted: true}
	}
	return RequestVoteResp{Term: n.term, VoteGranted: false}
}

type ErrNotLeader struct{ Leader string }

func (e ErrNotLeader) Error() string { return "not leader" }

type ErrCommit struct{}

func (e ErrCommit) Error() string { return "commit failed" }
