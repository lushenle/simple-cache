package raft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// raftHTTPClient is a shared HTTP client with timeout and connection pooling.
var raftHTTPClient = &http.Client{
	Timeout: 10 * time.Second,
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

func NewHTTPTransport(addr string, peers []string, logger *zap.Logger) (*HTTPTransport, error) {
	normalized := make([]string, 0, len(peers))
	for _, peer := range peers {
		value, err := NormalizePeerAddr(peer)
		if err != nil {
			return nil, fmt.Errorf("invalid peer %q: %w", peer, err)
		}
		if containsPeer(normalized, value) {
			if logger != nil {
				logger.Warn("duplicate peer ignored", zap.String("peer", peer))
			}
			continue
		}
		normalized = append(normalized, value)
	}
	return &HTTPTransport{addr: addr, peers: normalized}, nil
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
	mux.HandleFunc("/raft/install_snapshot", func(w http.ResponseWriter, r *http.Request) {
		var req InstallSnapshotReq
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &req)
		if node.logger != nil {
			node.logger.Debug("http recv install snapshot", zap.String("node", node.id), zap.String("leader", req.LeaderID), zap.Uint64("term", req.Term), zap.Uint64("index", req.LastIncludedIndex))
		}
		resp := node.onInstallSnapshot(req)
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

func (t *HTTPTransport) sendAppend(ctx context.Context, peer string, req AppendEntriesReq) (AppendEntriesResp, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return AppendEntriesResp{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, peer+"/raft/append", bytes.NewReader(b))
	if err != nil {
		return AppendEntriesResp{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := raftHTTPClient.Do(httpReq)
	if err != nil {
		if t.node != nil && t.node.logger != nil {
			t.node.logger.Debug("send append failed", zap.String("peer", peer), zap.Error(err))
		}
		return AppendEntriesResp{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return AppendEntriesResp{}, fmt.Errorf("append request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return AppendEntriesResp{}, err
	}

	var out AppendEntriesResp
	if err := json.Unmarshal(body, &out); err != nil {
		return AppendEntriesResp{}, err
	}
	return out, nil
}

func (t *HTTPTransport) sendInstallSnapshot(ctx context.Context, peer string, req InstallSnapshotReq) (InstallSnapshotResp, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return InstallSnapshotResp{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, peer+"/raft/install_snapshot", bytes.NewReader(b))
	if err != nil {
		return InstallSnapshotResp{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := raftHTTPClient.Do(httpReq)
	if err != nil {
		if t.node != nil && t.node.logger != nil {
			t.node.logger.Debug("send install snapshot failed", zap.String("peer", peer), zap.Error(err))
		}
		return InstallSnapshotResp{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return InstallSnapshotResp{}, fmt.Errorf("install snapshot request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return InstallSnapshotResp{}, err
	}
	var out InstallSnapshotResp
	if err := json.Unmarshal(body, &out); err != nil {
		return InstallSnapshotResp{}, err
	}
	return out, nil
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
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err := t.sendVote(ctx, p, req)
		cancel()
		if err != nil {
			continue
		}
		if out.VoteGranted {
			granted++
		}
	}
	return granted
}

func (t *HTTPTransport) sendVote(ctx context.Context, peer string, req RequestVoteReq) (RequestVoteResp, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return RequestVoteResp{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, peer+"/raft/vote", bytes.NewReader(b))
	if err != nil {
		return RequestVoteResp{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := raftHTTPClient.Do(httpReq)
	if err != nil {
		if t.node != nil && t.node.logger != nil {
			t.node.logger.Debug("send vote failed", zap.String("peer", peer), zap.Error(err))
		}
		return RequestVoteResp{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return RequestVoteResp{}, fmt.Errorf("vote request failed with status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return RequestVoteResp{}, err
	}
	var out RequestVoteResp
	if err := json.Unmarshal(body, &out); err != nil {
		return RequestVoteResp{}, err
	}
	return out, nil
}

func (t *HTTPTransport) isSelf(peer string) bool {
	peerURL, err := url.Parse(peer)
	if err != nil {
		return false
	}

	selfURL, err := url.Parse("http://" + t.addr)
	if err != nil {
		return false
	}

	peerHost, peerPort, _ := net.SplitHostPort(peerURL.Host)
	selfHost, selfPort, _ := net.SplitHostPort(selfURL.Host)

	if peerPort != selfPort {
		return false
	}

	// When the bind address has no explicit host (e.g. ":9090"), it listens
	// on all interfaces, so any loopback peer with the same port is self.
	if selfHost == "" || selfHost == "0.0.0.0" || selfHost == "::" {
		return isLoopback(peerHost)
	}

	return strings.EqualFold(peerHost, selfHost)
}

// isLoopback reports whether host is a loopback address or "localhost".
func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
