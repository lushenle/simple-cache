package raft

type Role string

const (
	Follower  Role = "follower"
	Candidate Role = "candidate"
	Leader    Role = "leader"
)

type EntryType string

const (
	EntryTypeCommand    EntryType = "command"
	EntryTypeAddPeer    EntryType = "add_peer"
	EntryTypeRemovePeer EntryType = "remove_peer"
)

type LogEntry struct {
	Index uint64    `json:"index"`
	Term  uint64    `json:"term"`
	Type  EntryType `json:"type"`
	Data  []byte    `json:"data"`
}

type PeerChange struct {
	Addr string `json:"addr"`
}

type AppendEntriesReq struct {
	Term         uint64
	LeaderID     string
	PrevLogIndex uint64
	PrevLogTerm  uint64
	Entries      []LogEntry
	CommitIdx    uint64
}

type AppendEntriesResp struct {
	Term         uint64
	Success      bool
	MatchIndex   uint64
	LastLogIndex uint64
}

type InstallSnapshotReq struct {
	Term              uint64
	LeaderID          string
	LastIncludedIndex uint64
	LastIncludedTerm  uint64
	Data              []byte
}

type InstallSnapshotResp struct {
	Term    uint64
	Success bool
}

type RequestVoteReq struct {
	Term         uint64
	CandidateID  string
	LastLogIndex uint64
	LastLogTerm  uint64
}

type RequestVoteResp struct {
	Term        uint64
	VoteGranted bool
}

type SnapshotMeta struct {
	LastIncludedIndex uint64 `json:"last_included_index"`
	LastIncludedTerm  uint64 `json:"last_included_term"`
}
