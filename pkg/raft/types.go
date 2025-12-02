package raft

type Role string

const (
	Follower  Role = "follower"
	Candidate Role = "candidate"
	Leader    Role = "leader"
)

type AppendEntriesReq struct {
	Term      uint64
	LeaderID  string
	Entries   [][]byte
	CommitIdx uint64
}

type AppendEntriesResp struct {
	Term    uint64
	Success bool
}

type RequestVoteReq struct {
	Term        uint64
	CandidateID string
}

type RequestVoteResp struct {
	Term        uint64
	VoteGranted bool
}
