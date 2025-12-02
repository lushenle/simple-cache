package raft

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
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
		resp := node.onAppendEntries(req)
		out, _ := json.Marshal(resp)
		w.WriteHeader(http.StatusOK)
		w.Write(out)
	})
	mux.HandleFunc("/raft/vote", func(w http.ResponseWriter, r *http.Request) {
		var req RequestVoteReq
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
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
		if p == t.addr {
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
