package raft

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func chaosCluster(t *testing.T, snapshotThreshold uint64) (nodes []*Node, appliers []*fakeApplier, peers []string, baseDir string) {
	t.Helper()
	logger := zap.NewNop()
	baseDir = t.TempDir()

	addrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	peers = []string{"http://" + addrs[0], "http://" + addrs[1], "http://" + addrs[2]}
	for i := 0; i < 3; i++ {
		appliers = append(appliers, newFakeApplier())
		n, err := NewNode(
			fmt.Sprintf("cn%d", i+1),
			addrs[i],
			peers,
			NewStorage(filepath.Join(baseDir, fmt.Sprintf("n%d.wal", i+1))),
			appliers[i],
			200*time.Millisecond,
			500*time.Millisecond,
			true,
			snapshotThreshold,
			logger,
			"",
		)
		require.NoError(t, err)
		nodes = append(nodes, n)
	}
	t.Cleanup(func() {
		for _, n := range nodes {
			n.Close()
		}
	})
	return nodes, appliers, peers, baseDir
}

func applyAllHave(appliers []*fakeApplier, key string) bool {
	for _, a := range appliers {
		if !a.Has(key) {
			return false
		}
	}
	return true
}

// submitToCluster submits to whichever node is the current leader, tolerating
// leadership changes mid-flight (the command may have committed even when the
// call reports not-leader; Set is idempotent so a retry is safe).
func submitToCluster(t *testing.T, nodes []*Node, cmd interface{}) interface{} {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		leader := waitForLeader(t, nodes...)
		resp, err := leader.Submit(context.Background(), cmd)
		if err == nil {
			return resp
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("submit never succeeded, last error: %v", lastErr)
	return nil
}

// TestPartitionHealRepairsFollower isolates one follower, commits a write on
// the remaining quorum, then heals the partition and verifies the follower
// catches up (log repair) and the cluster converges.
func TestPartitionHealRepairsFollower(t *testing.T) {
	nodes, appliers, peers, _ := chaosCluster(t, 1024)

	leader := waitForLeader(t, nodes...)
	submitStable(t, leader, &command.SetCommand{Key: "p0", Value: "v"})
	waitForCondition(t, func() bool { return applyAllHave(appliers, "p0") })

	var follower *Node
	for _, n := range nodes {
		if n != leader {
			follower = n
			break
		}
	}
	require.NotNil(t, follower)

	// Full partition between leader and the chosen follower.
	leader.trans.blockPeer(follower.trans.selfAddr)
	follower.trans.blockPeer(leader.trans.selfAddr)
	defer func() {
		leader.trans.unblockPeer(follower.trans.selfAddr)
		follower.trans.unblockPeer(leader.trans.selfAddr)
	}()

	// Committed via leader + the other follower (quorum of 2).
	submitStable(t, leader, &command.SetCommand{Key: "p1", Value: "v"})

	// Heal: the isolated follower must catch up via log repair.
	leader.trans.unblockPeer(follower.trans.selfAddr)
	follower.trans.unblockPeer(leader.trans.selfAddr)
	waitForCondition(t, func() bool { return applyAllHave(appliers, "p1") })

	leader.logMu.Lock()
	li, lt := leader.lastLogIndex, leader.lastLogTerm
	leader.logMu.Unlock()
	follower.logMu.Lock()
	fi, ft := follower.lastLogIndex, follower.lastLogTerm
	follower.logMu.Unlock()
	require.Equal(t, li, fi)
	require.Equal(t, lt, ft)

	require.Len(t, peers, 3, "sanity: cluster membership unchanged")
}

// TestPartitionMinorityLeaderStepsDown isolates the leader from the quorum.
// Writes on the isolated leader must fail, the majority must elect a new
// leader, and after healing there must be exactly one leader with all data
// converged (no split brain, no lost committed entries).
func TestPartitionMinorityLeaderStepsDown(t *testing.T) {
	nodes, appliers, _, _ := chaosCluster(t, 1024)

	leader := waitForLeader(t, nodes...)
	submitStable(t, leader, &command.SetCommand{Key: "iso0", Value: "v"})
	waitForCondition(t, func() bool { return applyAllHave(appliers, "iso0") })

	var majority []*Node
	for _, n := range nodes {
		if n != leader {
			majority = append(majority, n)
		}
	}
	require.Len(t, majority, 2)

	// Isolate the leader in both directions.
	for _, m := range majority {
		leader.trans.blockPeer(m.trans.selfAddr)
		m.trans.blockPeer(leader.trans.selfAddr)
	}
	heal := func() {
		for _, m := range majority {
			leader.trans.unblockPeer(m.trans.selfAddr)
			m.trans.unblockPeer(leader.trans.selfAddr)
		}
	}
	defer heal()

	// The isolated leader cannot commit: Submit must fail (either it steps
	// down immediately, or replication times out).
	start := time.Now()
	_, err := leader.Submit(context.Background(), &command.SetCommand{Key: "iso-lost", Value: "v"})
	require.Error(t, err)
	require.Less(t, time.Since(start), 5*time.Second)

	// The majority elects a new leader and keeps serving writes.
	newLeader := waitForLeader(t, majority...)
	require.NotSame(t, leader, newLeader, "a node from the majority must take over")
	submitStable(t, newLeader, &command.SetCommand{Key: "iso1", Value: "v"})

	// Heal: the deposed leader rejoins and catches up.
	heal()
	waitForCondition(t, func() bool { return applyAllHave(appliers, "iso1") })

	// Exactly one leader remains, and the isolated write was never committed.
	leaders := 0
	for _, n := range nodes {
		if n.Role() == Leader {
			leaders++
		}
	}
	require.Equal(t, 1, leaders)
	require.False(t, applyAllHave(appliers, "iso-lost"))
}

// TestLeaderKillStorm repeatedly kills and restarts the leader, verifying
// the cluster re-elects a single leader, no data is lost, and the restarted
// node replays its WAL and catches up.
func TestLeaderKillStorm(t *testing.T) {
	logger := zap.NewNop()
	baseDir := t.TempDir()

	addrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	peers := []string{"http://" + addrs[0], "http://" + addrs[1], "http://" + addrs[2]}

	type nodeSpec struct {
		id      string
		addr    string
		storage *Storage
	}
	specs := []nodeSpec{
		{"sn1", addrs[0], NewStorage(filepath.Join(baseDir, "n1.wal"))},
		{"sn2", addrs[1], NewStorage(filepath.Join(baseDir, "n2.wal"))},
		{"sn3", addrs[2], NewStorage(filepath.Join(baseDir, "n3.wal"))},
	}
	appliers := []*fakeApplier{newFakeApplier(), newFakeApplier(), newFakeApplier()}

	nodes := make([]*Node, 3)
	for i, s := range specs {
		n, err := NewNode(s.id, s.addr, peers, s.storage, appliers[i], 200*time.Millisecond, 500*time.Millisecond, true, 16, logger, "")
		require.NoError(t, err)
		nodes[i] = n
	}
	closeAll := func() {
		for _, n := range nodes {
			if n != nil {
				n.Close()
			}
		}
	}
	defer closeAll()

	var keys []string
	for round := 0; round < 3; round++ {
		leader := waitForLeader(t, nodes...)
		key := fmt.Sprintf("storm-%d", round)
		submitToCluster(t, nodes, &command.SetCommand{Key: key, Value: "v"})
		keys = append(keys, key)
		waitForCondition(t, func() bool { return applyAllHave(appliers, key) })

		// Kill the leader and restart it on the same storage.
		leaderIdx := -1
		for i, n := range nodes {
			if n == leader {
				leaderIdx = i
				break
			}
		}
		require.GreaterOrEqual(t, leaderIdx, 0)
		leader.Close()
		appliers[leaderIdx] = newFakeApplier()
		restarted, err := NewNode(specs[leaderIdx].id, specs[leaderIdx].addr, peers, specs[leaderIdx].storage, appliers[leaderIdx], 200*time.Millisecond, 500*time.Millisecond, true, 16, logger, "")
		require.NoError(t, err)
		nodes[leaderIdx] = restarted

		// The restarted node must replay its WAL and converge with the rest.
		// A spurious election right after the restart can churn leadership,
		// so give convergence a generous window.
		waitForConditionTimeout(t, 10*time.Second, func() bool {
			for _, k := range keys {
				if !applyAllHave(appliers, k) {
					return false
				}
			}
			return true
		})

		// The cluster converges to exactly one leader (election may still be
		// in flight right after the restart).
		waitForConditionTimeout(t, 10*time.Second, func() bool {
			leaders := 0
			for _, n := range nodes {
				if n.Role() == Leader {
					leaders++
				}
			}
			return leaders == 1
		})
	}
}

// TestDiskWriteFailure verifies that WAL write failures surface as submit
// errors without corrupting node state: after the disk becomes writable
// again the same node keeps working.
func TestDiskWriteFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based disk failure cannot be simulated as root")
	}
	logger := zap.NewNop()
	baseDir := t.TempDir()
	addr := freeAddr(t)
	peers := []string{"http://" + addr}
	applier := newFakeApplier()

	walPath := filepath.Join(baseDir, "n1.wal")
	n, err := NewNode("disk-1", addr, peers, NewStorage(walPath), applier, 200*time.Millisecond, 500*time.Millisecond, true, 1024, logger, "")
	require.NoError(t, err)
	defer n.Close()

	leader := waitForLeader(t, n)
	submitStable(t, leader, &command.SetCommand{Key: "d0", Value: "v"})

	// Make the WAL file itself read-only: appending must fail (a read-only
	// directory would not block appends to existing files).
	require.NoError(t, os.Chmod(walPath, 0o400))
	_, err = leader.Submit(context.Background(), &command.SetCommand{Key: "d-fail", Value: "v"})
	require.Error(t, err, "submit must fail when the WAL cannot be written")

	require.NoError(t, os.Chmod(walPath, 0o644))
	submitStable(t, leader, &command.SetCommand{Key: "d1", Value: "v"})
	waitForCondition(t, func() bool { return applier.Has("d0") && applier.Has("d1") })
	require.False(t, applier.Has("d-fail"))
}
