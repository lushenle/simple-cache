package raft

import (
	"encoding/json"
	"net"
	"path/filepath"
	"sync"
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

	n1 := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), applier1, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	n2 := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), applier2, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	n3 := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), applier3, 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	defer n1.Close()
	defer n2.Close()
	defer n3.Close()

	leader := waitForLeader(t, n1, n2, n3)
	_, err := leader.Submit(&command.SetCommand{Key: "k1", Value: "v1"})
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
	node := NewNode("node-1", addr, peers, NewStorage(walPath), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger)
	leader := waitForLeader(t, node)
	_, err := leader.Submit(&command.SetCommand{Key: "persisted", Value: "value"})
	require.NoError(t, err)
	waitForCondition(t, func() bool { return applier.Has("persisted") })
	node.Close()

	restarted := newFakeApplier()
	node2 := NewNode("node-1", addr, peers, NewStorage(walPath), restarted, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger)
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

	n1 := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	n2 := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	n3 := NewNode("n3", addr3, peers, NewStorage(filepath.Join(baseDir, "n3.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
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

	n1 := NewNode("n1", addr1, peers, NewStorage(filepath.Join(baseDir, "n1.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	n2 := NewNode("n2", addr2, peers, NewStorage(filepath.Join(baseDir, "n2.wal")), newFakeApplier(), 80*time.Millisecond, 180*time.Millisecond, true, 8, logger)
	defer n1.Close()
	defer n2.Close()

	leader := waitForLeader(t, n1, n2)
	start := time.Now()
	_, err := leader.Submit(&command.SetCommand{Key: "k-timeout", Value: "v"})
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
	node := NewNode("node-1", addr, peers, NewStorage(walPath), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger)
	leader := waitForLeader(t, node)

	_, err := leader.Submit(&command.SetCommand{Key: "k1", Value: "v1"})
	require.NoError(t, err)
	_, err = leader.Submit(&command.SetCommand{Key: "k2", Value: "v2"})
	require.NoError(t, err)
	waitForCondition(t, func() bool { return applier.Has("k1") && applier.Has("k2") })
	waitForCondition(t, func() bool { return node.storage.HasSnapshot() })
	node.Close()

	restarted := newFakeApplier()
	node2 := NewNode("node-1", addr, peers, NewStorage(walPath), restarted, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger)
	defer node2.Close()
	waitForCondition(t, func() bool { return restarted.Has("k1") && restarted.Has("k2") })
}

func TestNodeInstallSnapshotRestoresFollowerState(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}

	applier := newFakeApplier()
	node := NewNode("node-1", addr, peers, NewStorage(filepath.Join(baseDir, "node.wal")), applier, 80*time.Millisecond, 180*time.Millisecond, true, 2, logger)
	defer node.Close()

	resp := node.onInstallSnapshot(InstallSnapshotReq{
		Term:              2,
		LeaderID:          "leader-1",
		LastIncludedIndex: 5,
		LastIncludedTerm:  2,
		Data:              []byte(`{"snap":"value"}`),
	})
	require.True(t, resp.Success)
	waitForCondition(t, func() bool { return applier.Has("snap") })
	require.Equal(t, uint64(5), node.snapshotIndex)
	require.Equal(t, uint64(5), node.commitIdx)
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
