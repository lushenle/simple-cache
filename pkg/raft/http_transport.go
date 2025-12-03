package raft

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"

	"go.uber.org/zap"
)

type HTTPTransport struct {
	addr  string
	peers []string
	node  *Node
}

func NewHTTPTransport(addr string, peers []string) *HTTPTransport {
	return &HTTPTransport{addr: addr, peers: peers}
}

func (t *HTTPTransport) Start(node *Node) {
	t.node = node
	mux := http.NewServeMux()
	mux.HandleFunc("/raft/append", func(w http.ResponseWriter, r *http.Request) {
		var req AppendEntriesReq
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if node.logger != nil {
			node.logger.Debug("http recv append", zap.String("node", node.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term))
		}
		resp := node.onAppendEntries(req)
		out, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})
	mux.HandleFunc("/raft/vote", func(w http.ResponseWriter, r *http.Request) {
		var req RequestVoteReq
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if node.logger != nil {
			node.logger.Debug("http recv vote", zap.String("node", node.id), zap.String("candidate", req.CandidateID), zap.Uint64("term", req.Term))
		}
		resp := node.onRequestVote(req)
		out, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})
	go http.ListenAndServe(t.addr, mux)
}

func (t *HTTPTransport) broadcastAppend(req AppendEntriesReq) int {
	ok := 1
	for _, p := range t.peers {
		if t.isSelf(p) {
			continue
		}
		b, _ := json.Marshal(req)
		resp, err := http.Post(p+"/raft/append", "application/json", bytes.NewReader(b))
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			ok++
		}
	}
	return ok
}

func (t *HTTPTransport) AddPeer(addr string) bool {
	for _, p := range t.peers {
		if p == addr {
			return false
		}
	}
	t.peers = append(t.peers, addr)
	return true
}

func (t *HTTPTransport) RemovePeer(addr string) bool {
	idx := -1
	for i, p := range t.peers {
		if p == addr {
			idx = i
			break
		}
	}
	if idx == -1 {
		return false
	}
	t.peers = append(t.peers[:idx], t.peers[idx+1:]...)
	return true
}

func (t *HTTPTransport) broadcastVote(req RequestVoteReq) int {
	granted := 0
	for _, p := range t.peers {
		if t.isSelf(p) {
			continue
		}
		b, _ := json.Marshal(req)
		resp, err := http.Post(p+"/raft/vote", "application/json", bytes.NewReader(b))
		if err != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			var out RequestVoteResp
			body, _ := io.ReadAll(resp.Body)
			_ = json.Unmarshal(body, &out)
			if out.VoteGranted {
				granted++
			}
		}
	}
	return granted
}

func (t *HTTPTransport) isSelf(peer string) bool {
	u, err := url.Parse(peer)
	if err != nil {
		return false
	}
	host := u.Host
	_, peerPort, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	// t.addr could be ":9090" or "0.0.0.0:9090" or "127.0.0.1:9090"
	_, selfPort, err := net.SplitHostPort(t.addr)
	if err != nil {
		if len(t.addr) > 1 && t.addr[0] == ':' {
			selfPort = t.addr[1:]
		} else {
			return false
		}
	}
	return peerPort == selfPort
}
