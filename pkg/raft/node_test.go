package raft

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/utils"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/anypb"
)

type fakeApplier struct {
	mu    sync.Mutex
	items map[string]any
}

func newFakeApplier() *fakeApplier {
	return &fakeApplier{items: make(map[string]any)}
}

func (f *fakeApplier) Apply(cmd interface{}) (interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	switch c := cmd.(type) {
	case *command.SetCommand:
		val := c.Value
		if anyValue, ok := c.Value.(*anypb.Any); ok {
			decoded, err := utils.FromAnyPB(anyValue)
			if err == nil {
				val = decoded
			}
		}
		f.items[c.Key] = val
	case *command.DelCommand:
		delete(f.items, c.Key)
	case *command.ResetCommand:
		f.items = make(map[string]any)
	}
	return nil, nil
}

func (f *fakeApplier) Has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.items[key]
	return ok
}

func (f *fakeApplier) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.items)
}

func (f *fakeApplier) Snapshot(nodeID string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return json.Marshal(f.items)
}

func (f *fakeApplier) RestoreSnapshot(nodeID string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(data) == 0 {
		f.items = make(map[string]any)
		return nil
	}
	restored := make(map[string]any)
	if err := json.Unmarshal(data, &restored); err != nil {
		return err
	}
	f.items = restored
	return nil
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func waitForLeader(t *testing.T, nodes ...*Node) *Node {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, node := range nodes {
			if node != nil && node.Role() == Leader {
				return node
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("leader not elected")
	return nil
}

func waitForCondition(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestNodeReplicationAndFailover(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	applier1 := newFakeApplier()
	applier2 := newFakeApplier()
	applier3 := newFakeApplier()

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), applier1, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), applier2, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), applier3, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)
	_, err = leader.Submit(&command.SetCommand{Key: "k1", Value: "v1"})
	require.NoError(t, err)

	waitForCondition(t, func() bool {
		return applier1.Has("k1") && applier2.Has("k1") && applier3.Has("k1")
	})

	leader.Close()
	var survivors []*Node
	switch leader {
	case n1:
		survivors = []*Node{n2, n3}
	case n2:
		survivors = []*Node{n1, n3}
	default:
		survivors = []*Node{n1, n2}
	}

	newLeader := waitForLeader(t, survivors...)
	_, err = newLeader.Submit(&command.SetCommand{Key: "k2", Value: "v2"})
	require.NoError(t, err)

	waitForCondition(t, func() bool {
		ok := true
		for _, node := range survivors {
			switch node {
			case n1:
				ok = ok && applier1.Has("k2")
			case n2:
				ok = ok && applier2.Has("k2")
			case n3:
				ok = ok && applier3.Has("k2")
			}
		}
		return ok
	})
}

func TestNodeReplayCommittedEntriesOnRestart(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	walPath := filepath.Join(baseDir, "node.wal")
	node, err := NewNode("node-1", addr, peers, NewStorage(walPath), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	leader := waitForLeader(t, node)
	_, err = leader.Submit(&command.SetCommand{Key: "persisted", Value: "value"})
	require.NoError(t, err)
	waitForCondition(t, func() bool { return applier.Has("persisted") })
	node.Close()

	restarted := newFakeApplier()
	node2, err := NewNode("node-1", addr, peers, NewStorage(walPath), restarted, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer node2.Close()

	waitForCondition(t, func() bool { return restarted.Has("persisted") })
}

func TestNodeReplicatesPeerChange(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	ghost := "http://" + freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)
	require.NoError(t, leader.AddPeer(ghost))

	waitForCondition(t, func() bool {
		return contains(n1.Peers(), ghost) && contains(n2.Peers(), ghost) && contains(n3.Peers(), ghost)
	})

	require.NoError(t, leader.RemovePeer(ghost))
	waitForCondition(t, func() bool {
		return !contains(n1.Peers(), ghost) && !contains(n2.Peers(), ghost) && !contains(n3.Peers(), ghost)
	})
}

func TestNodeSubmitWithUnreachablePeerDoesNotBlockTooLong(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	ghost := "http://" + freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		ghost,
	}

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()

	leader := waitForLeader(t, n1, n2)
	start := time.Now()
	_, err = leader.Submit(&command.SetCommand{Key: "k-timeout", Value: "v"})
	duration := time.Since(start)

	require.NoError(t, err)
	require.Less(t, duration, 1500*time.Millisecond)
}

func TestNodeCreatesSnapshotAndRecoversOnRestart(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	walPath := filepath.Join(baseDir, "node.wal")
	node, err := NewNode("node-1", addr, peers, NewStorage(walPath), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	leader := waitForLeader(t, node)

	_, err = leader.Submit(&command.SetCommand{Key: "k1", Value: "v1"})
	require.NoError(t, err)
	_, err = leader.Submit(&command.SetCommand{Key: "k2", Value: "v2"})
	require.NoError(t, err)
	waitForCondition(t, func() bool { return applier.Has("k1") && applier.Has("k2") })
	waitForCondition(t, func() bool { return node.storage.HasSnapshot() })
	node.Close()

	restarted := newFakeApplier()
	node2, err := NewNode("node-1", addr, peers, NewStorage(walPath), restarted, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer node2.Close()
	waitForCondition(t, func() bool { return restarted.Has("k1") && restarted.Has("k2") })
}

func TestNodeInstallSnapshotRestoresFollowerState(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	node, err := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer node.Close()

	resp := node.onInstallSnapshot(InstallSnapshotReq{
		Term:              2,
		LeaderID:          "leader-1",
		LastIncludedIndex: 5,
		LastIncludedTerm:  2,
		Data:              []byte(`{"snap":"value"}`),
		Done:              true,
	})
	require.True(t, resp.Success)
	waitForCondition(t, func() bool { return applier.Has("snap") })
	require.Equal(t, uint64(5), node.snapshotIndex)
	require.Equal(t, uint64(5), node.commitIdx)
}

// TestNodeInstallSnapshotRejectsOversizedAccumulation verifies the pending
// snapshot buffer cap (A): a leader that keeps sending chunks must not be
// able to exhaust the follower's memory; exceeding the cap drops the
// accumulation and fails the chunk.
func TestNodeInstallSnapshotRejectsOversizedAccumulation(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	node, err := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer node.Close()

	old := maxPendingSnapshotBytes
	maxPendingSnapshotBytes = 64 * 1024 // 64 KiB
	defer func() { maxPendingSnapshotBytes = old }()

	resp := node.onInstallSnapshot(InstallSnapshotReq{
		Term: 2, LeaderID: "leader-1", LastIncludedIndex: 5, LastIncludedTerm: 2,
		Data: bytes.Repeat([]byte("x"), 40*1024), Offset: 0, Done: false,
	})
	require.True(t, resp.Success)

	// This chunk would push the accumulation past the cap.
	resp = node.onInstallSnapshot(InstallSnapshotReq{
		Term: 2, LeaderID: "leader-1", LastIncludedIndex: 5, LastIncludedTerm: 2,
		Data: bytes.Repeat([]byte("y"), 40*1024), Offset: 40 * 1024, Done: false,
	})
	require.False(t, resp.Success)
	node.mu.Lock()
	require.Nil(t, node.pendingSnapshot, "oversized accumulation must be dropped")
	node.mu.Unlock()
}

// TestReplicateRepairsDivergentFollower verifies that pipelined replication
// (B) repairs a follower whose log diverges (a term conflict at the same
// index) and converges back to the leader's log.
func TestReplicateRepairsDivergentFollower(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	applier1 := newFakeApplier()
	applier2 := newFakeApplier()
	applier3 := newFakeApplier()

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), applier1, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), applier2, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), applier3, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)
	_, err = leader.Submit(&command.SetCommand{Key: "k0", Value: "v"})
	require.NoError(t, err)
	waitForCondition(t, func() bool {
		return applier1.Has("k0") && applier2.Has("k0") && applier3.Has("k0")
	})

	// Corrupt one follower's last log entry term to create a divergence that
	// the leader's log-matching logic must repair.
	var follower *Node
	switch leader {
	case n1:
		follower = n2
	case n2:
		follower = n1
	default:
		follower = n1
	}
	follower.mu.Lock()
	require.NotEmpty(t, follower.logs)
	follower.logs[len(follower.logs)-1].Term++
	follower.recomputeLastLogLocked()
	follower.mu.Unlock()

	// The next write must drive the follower back to consensus.
	_, err = leader.Submit(&command.SetCommand{Key: "k1", Value: "v"})
	require.NoError(t, err)
	waitForCondition(t, func() bool {
		return applier1.Has("k1") && applier2.Has("k1") && applier3.Has("k1")
	})

	leader.mu.Lock()
	wantIndex, wantTerm := leader.lastLogIndex, leader.lastLogTerm
	leader.mu.Unlock()
	follower.mu.Lock()
	gotIndex, gotTerm := follower.lastLogIndex, follower.lastLogTerm
	follower.mu.Unlock()
	require.Equal(t, wantIndex, gotIndex)
	require.Equal(t, wantTerm, gotTerm)
}

// TestFlushMetaKeepsDirtyOnFailure verifies (C) that a failed meta persist
// keeps the dirty flag set so the background flusher retries instead of
// silently dropping a term/vote update.
func TestFlushMetaKeepsDirtyOnFailure(t *testing.T) {
	dir := t.TempDir()
	st := NewStorage(filepath.Join(dir, "raft.wal"))
	// Collide the meta path with a directory so the final rename fails.
	require.NoError(t, os.MkdirAll(st.metaPath(), 0o755))

	n := &Node{
		storage:   st,
		trans:     &HTTPTransport{peers: []string{}},
		metaDirty: atomic.Bool{},
	}
	n.metaDirty.Store(false)
	n.mu.Lock()
	n.flushMeta()
	n.mu.Unlock()
	require.True(t, n.metaDirty.Load(), "failed flush must keep the dirty flag")
}

// TestNodeFastCatchUp exercises the pipelined replication path (P2-15): a
// burst of writes must be replicated and applied promptly rather than one
// batch per heartbeat tick.
func TestNodeFastCatchUp(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	applier1 := newFakeApplier()
	applier2 := newFakeApplier()
	applier3 := newFakeApplier()

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), applier1, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), applier2, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), applier3, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)

	start := time.Now()
	for i := 0; i < 50; i++ {
		_, err = leader.Submit(&command.SetCommand{Key: fmt.Sprintf("fast-%d", i), Value: "v"})
		require.NoError(t, err)
	}
	waitForCondition(t, func() bool {
		return applier1.Has("fast-49") && applier2.Has("fast-49") && applier3.Has("fast-49")
	})
	// Loose upper bound: well below the one-batch-per-heartbeat lower bound.
	require.Less(t, time.Since(start), 3*time.Second)
}

// TestNodeInstallSnapshotChunked verifies the chunked InstallSnapshot path
// (P2-14): chunks accumulate in order and the FSM is restored only on the
// final chunk; an out-of-order chunk is rejected.
func TestNodeInstallSnapshotChunked(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	node, err := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer node.Close()

	// Build a >4KiB payload that the fake applier can restore (valid JSON).
	big := map[string]any{}
	for i := 0; i < 300; i++ {
		big[fmt.Sprintf("key-%d", i)] = string(bytes.Repeat([]byte("v"), 16))
	}
	data, err := json.Marshal(big)
	require.NoError(t, err)

	// The chunk size is a sender-side parameter; use a small chunk here so
	// the test exercises the multi-chunk accumulation path cheaply.
	chunk := 4096
	var offset int
	for offset < len(data) {
		end := offset + chunk
		if end > len(data) {
			end = len(data)
		}
		resp := node.onInstallSnapshot(InstallSnapshotReq{
			Term:              2,
			LeaderID:          "leader-1",
			LastIncludedIndex: 5,
			LastIncludedTerm:  2,
			Data:              data[offset:end],
			Offset:            uint64(offset),
			Done:              end == len(data),
		})
		require.True(t, resp.Success)
		offset = end
	}
	require.True(t, applier.Has("key-0"))
	require.True(t, applier.Has("key-299"))
	require.Equal(t, uint64(5), node.snapshotIndex)

	// Out-of-order chunks must be rejected.
	resp := node.onInstallSnapshot(InstallSnapshotReq{
		Term:              3,
		LeaderID:          "leader-1",
		LastIncludedIndex: 6,
		LastIncludedTerm:  3,
		Data:              []byte("bogus"),
		Offset:            100,
	})
	require.False(t, resp.Success)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestRequestVoteLogComparison(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	node, err := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), applier, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer node.Close()

	waitForLeader(t, node)

	// Submit a few entries so the node has some log state.
	_, err = node.Submit(&command.SetCommand{Key: "a", Value: "1"})
	require.NoError(t, err)
	_, err = node.Submit(&command.SetCommand{Key: "b", Value: "2"})
	require.NoError(t, err)
	waitForCondition(t, func() bool { return applier.Has("a") && applier.Has("b") })

	cases := []struct {
		name        string
		buildReq    func() RequestVoteReq
		wantGranted bool
	}{
		{
			name: "candidate with higher term but shorter log",
			buildReq: func() RequestVoteReq {
				return RequestVoteReq{
					Term:         node.term + 1,
					CandidateID:  "candidate-1",
					LastLogTerm:  node.lastLogTerm + 1,
					LastLogIndex: node.lastLogIndex - 1,
				}
			},
			wantGranted: true,
		},
		{
			name: "candidate with lower term",
			buildReq: func() RequestVoteReq {
				return RequestVoteReq{
					Term:         node.term + 1,
					CandidateID:  "candidate-2",
					LastLogTerm:  node.lastLogTerm - 1,
					LastLogIndex: node.lastLogIndex + 10,
				}
			},
			wantGranted: false,
		},
		{
			name: "candidate with same term but shorter log",
			buildReq: func() RequestVoteReq {
				return RequestVoteReq{
					Term:         node.term + 1,
					CandidateID:  "candidate-3",
					LastLogTerm:  node.lastLogTerm,
					LastLogIndex: node.lastLogIndex - 1,
				}
			},
			wantGranted: false,
		},
		{
			name: "candidate with same term and longer log",
			buildReq: func() RequestVoteReq {
				return RequestVoteReq{
					Term:         node.term + 1,
					CandidateID:  "candidate-4",
					LastLogTerm:  node.lastLogTerm,
					LastLogIndex: node.lastLogIndex + 1,
				}
			},
			wantGranted: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.buildReq()
			resp := node.onRequestVote(req)
			require.Equal(t, tc.wantGranted, resp.VoteGranted)
		})
	}
}

// TestNodeAppendAfterSnapshot reproduces the P0-1 regression: a follower that
// has installed a snapshot must keep accepting incremental AppendEntries
// without panicking or corrupting its WAL (previously the append path indexed
// the log slice by log number and panicked once snapshotIndex > 0, leaving
// the node's mutex locked forever).
func TestNodeAppendAfterSnapshot(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	applier1 := newFakeApplier()
	applier2 := newFakeApplier()
	applier3 := newFakeApplier()

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), applier1, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), applier2, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), applier3, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)

	// Drive enough entries for every node to create a snapshot (threshold 2).
	for i := 0; i < 4; i++ {
		_, err = leader.Submit(&command.SetCommand{Key: fmt.Sprintf("k%d", i), Value: "v"})
		require.NoError(t, err)
	}
	waitForCondition(t, func() bool {
		return applier1.Has("k3") && applier2.Has("k3") && applier3.Has("k3")
	})
	waitForCondition(t, func() bool {
		return n1.storage.HasSnapshot() && n2.storage.HasSnapshot() && n3.storage.HasSnapshot()
	})

	// Followers now have snapshotIndex > 0; incremental appends must work.
	for i := 4; i < 8; i++ {
		_, err = leader.Submit(&command.SetCommand{Key: fmt.Sprintf("k%d", i), Value: "v"})
		require.NoError(t, err)
	}
	waitForCondition(t, func() bool {
		return applier1.Has("k7") && applier2.Has("k7") && applier3.Has("k7")
	})

	// Persisted WALs must stay loadable after the incremental appends, and the
	// background compaction must eventually drop every entry at or below the
	// snapshot point.
	waitForCondition(t, func() bool {
		for i, node := range []*Node{n1, n2, n3} {
			entries, err := node.storage.LoadEntries()
			if err != nil {
				return false
			}
			for _, entry := range entries {
				if entry.Index <= node.snapshotIndex {
					return false
				}
			}
			_ = i
		}
		return true
	})
}

// TestPreVoteDoesNotPersistOrAdvance verifies the PreVote protocol (P2-12):
// a pre-vote request must not advance the term, record a vote, or touch the
// persisted meta, even when granted.
func TestPreVoteDoesNotPersistOrAdvance(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	node, err := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer node.Close()
	waitForLeader(t, node)

	node.mu.Lock()
	beforeTerm := node.term
	beforeVotedFor := node.votedFor
	lastLogIndex := node.lastLogIndex
	lastLogTerm := node.lastLogTerm
	node.mu.Unlock()
	metaBefore, err := node.storage.LoadMeta()
	require.NoError(t, err)

	// A pre-vote at term+1 with an equally fresh log must be granted...
	resp := node.onRequestVote(RequestVoteReq{
		Term:         beforeTerm + 1,
		CandidateID:  "candidate-x",
		LastLogIndex: lastLogIndex,
		LastLogTerm:  lastLogTerm,
		PreVote:      true,
	})
	require.True(t, resp.VoteGranted)

	// ...but must not mutate any state.
	node.mu.Lock()
	require.Equal(t, beforeTerm, node.term, "pre-vote must not advance term")
	require.Equal(t, beforeVotedFor, node.votedFor, "pre-vote must not record votedFor")
	node.mu.Unlock()

	metaAfter, err := node.storage.LoadMeta()
	require.NoError(t, err)
	require.Equal(t, metaBefore.CurrentTerm, metaAfter.CurrentTerm, "pre-vote must not persist a term change")
	require.Equal(t, metaBefore.VotedFor, metaAfter.VotedFor, "pre-vote must not persist a vote")
}

// TestSingleNodePeerChangeCommits verifies the P1-8 regression fix: a peer
// change on a single-node cluster must commit locally (previously nothing
// advanced commitIdx for peer changes, so they always timed out).
func TestSingleNodePeerChangeCommits(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	node, err := NewNode("single", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer node.Close()
	waitForLeader(t, node)

	ghost := "http://" + freeAddr(t)
	require.NoError(t, node.AddPeer(ghost))
	require.True(t, containsPeer(node.Peers(), ghost))

	// The membership entry must be committed, not just appended.
	node.mu.Lock()
	require.Equal(t, node.lastLogIndex, node.commitIdx)
	node.mu.Unlock()
}

// TestConcurrentPeerChangeRejected verifies the P1-8 single-member-change
// guard: while a peer-change entry is uncommitted, a new change is rejected.
func TestConcurrentPeerChangeRejected(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
	}

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()

	leader := waitForLeader(t, n1, n2)

	// Isolate the leader from its only follower: with 2 members the majority
	// (2 of 2) can never be met, so the first change stays uncommitted.
	var follower *Node
	if leader == n1 {
		follower = n2
	} else {
		follower = n1
	}
	follower.Close()

	ghost1 := "http://" + freeAddr(t)
	ghost2 := "http://" + freeAddr(t)
	err = leader.AddPeer(ghost1)
	require.Error(t, err) // cannot commit: no quorum

	// The uncommitted add_peer entry must block the next change immediately.
	err = leader.AddPeer(ghost2)
	require.Error(t, err)
	var inFlight ErrPeerChangeInFlight
	require.ErrorAs(t, err, &inFlight)
}

// TestReadIndexRejectsDeposedLeader reproduces the P0-2 regression: a leader
// whose followers report a higher term must fail its ReadIndex (and step
// down) instead of serving reads from a stale state machine.
func TestReadIndexRejectsDeposedLeader(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addr1 := freeAddr(t)
	addr2 := freeAddr(t)
	addr3 := freeAddr(t)
	peers := []string{
		"http://" + addr1,
		"http://" + addr2,
		"http://" + addr3,
	}

	n1, err := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n2, err := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	n3, err := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger, "")
	require.NoError(t, err)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)

	// Depose the leader: give one follower a higher term (as after a new
	// leader was elected on the majority side of a partition).
	var deposed *Node
	switch leader {
	case n1:
		deposed = n2
	case n2:
		deposed = n1
	default:
		deposed = n1
	}
	deposed.mu.Lock()
	deposed.term = leader.term + 1
	deposed.votedFor = ""
	deposed.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err = leader.ReadIndex(ctx)
	require.Error(t, err)
	// The deposed leader must have stepped down.
	require.NotEqual(t, Leader, leader.Role())
}
