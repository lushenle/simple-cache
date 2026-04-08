package raft

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"go.uber.org/zap"
)

// raftHTTPClient is a shared HTTP client with timeout and connection pooling.
var raftHTTPClient = &http.Client{
	Timeout: 5 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	},
}

type HTTPTransport struct {
	addr    string
	mu      sync.RWMutex
	peers   []string
	node    *Node
	httpSrv *http.Server
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
	t.httpSrv = &http.Server{Addr: t.addr, Handler: mux}
	go func() {
		if err := t.httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			if node.logger != nil {
				node.logger.Error("raft http transport failed", zap.Error(err))
			}
		}
	}()
}

// Close gracefully shuts down the raft HTTP server.
func (t *HTTPTransport) Close() {
	if t.httpSrv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = t.httpSrv.Shutdown(ctx)
	}
}

func (t *HTTPTransport) broadcastAppend(req AppendEntriesReq) int {
	peers := t.Peers()
	ok := 1
	for _, p := range peers {
		if t.isSelf(p) {
			continue
		}
		b, _ := json.Marshal(req)
		resp, err := raftHTTPClient.Post(p+"/raft/append", "application/json", bytes.NewReader(b))
		if err != nil {
			if t.node.logger != nil {
				t.node.logger.Debug("broadcast append failed", zap.String("peer", p), zap.Error(err))
			}
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			ok++
		}
	}
	return ok
}

func (t *HTTPTransport) AddPeer(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.peers {
		if p == addr {
			return false
		}
	}
	t.peers = append(t.peers, addr)
	return true
}

func (t *HTTPTransport) RemovePeer(addr string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
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

// Peers returns a copy of the current peer list (thread-safe).
func (t *HTTPTransport) Peers() []string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	out := make([]string, len(t.peers))
	copy(out, t.peers)
	return out
}

func (t *HTTPTransport) broadcastVote(req RequestVoteReq) int {
	peers := t.Peers()
	granted := 0
	for _, p := range peers {
		if t.isSelf(p) {
			continue
		}
		b, _ := json.Marshal(req)
		resp, err := raftHTTPClient.Post(p+"/raft/vote", "application/json", bytes.NewReader(b))
		if err != nil {
			if t.node.logger != nil {
				t.node.logger.Debug("broadcast vote failed", zap.String("peer", p), zap.Error(err))
			}
			continue
		}
		if resp.StatusCode == http.StatusOK {
			var out RequestVoteResp
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			_ = json.Unmarshal(body, &out)
			if out.VoteGranted {
				granted++
			}
		} else {
			resp.Body.Close()
		}
	}
	return granted
}

func (t *HTTPTransport) isSelf(peer string) bool {
	peerURL, err := url.Parse(peer)
	if err != nil {
		return false
	}
	peerHost := peerURL.Host

	selfURL, err := url.Parse("http://" + t.addr)
	if err != nil {
		return false
	}
	selfHost := selfURL.Host

	// Normalize: if no IP specified, use localhost
	if _, _, err := net.SplitHostPort(peerHost); err != nil {
		peerHost = "localhost" + peerHost
	}
	if _, _, err := net.SplitHostPort(selfHost); err != nil {
		selfHost = "localhost" + selfHost
	}

	return peerHost == selfHost
}
