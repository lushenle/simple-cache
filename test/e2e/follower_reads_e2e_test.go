//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/client"
	"github.com/lushenle/simple-cache/pkg/common"
	"github.com/lushenle/simple-cache/pkg/config"
	"github.com/stretchr/testify/require"
)

// TestFollowerReadsE2E verifies that with read_policy=follower, a follower
// serves linearizable reads directly (via the ReadIndex protocol) instead of
// rejecting them.
func TestFollowerReadsE2E(t *testing.T) {
	root := repoRoot(t)
	baseDir := t.TempDir()

	grpc1, http1, raft1, metrics1 := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	grpc2, http2, raft2, metrics2 := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)
	grpc3, http3, raft3, metrics3 := freeAddr(t), freeAddr(t), freeAddr(t), freeAddr(t)

	peers := []string{
		"http://" + raft1,
		"http://" + raft2,
		"http://" + raft3,
	}
	peerAddrs := map[string]string{
		"fr-n1": grpc1,
		"fr-n2": grpc2,
		"fr-n3": grpc3,
	}

	newCfg := func(nodeID, grpc, httpAddr, raftAddr, metricsAddr, dataDir string) *config.Config {
		return &config.Config{
			Mode:              common.ModeDistributed,
			NodeID:            nodeID,
			GRPCAddr:          grpc,
			HTTPAddr:          httpAddr,
			RaftHTTPAddr:      raftAddr,
			MetricsAddr:       metricsAddr,
			Peers:             peers,
			PeerAddresses:     peerAddrs,
			HeartbeatMS:       200,
			ElectionMS:        1200,
			HotReload:         false,
			LoadOnStartup:     false,
			DumpOnShutdown:    false,
			DumpFormat:        common.DumpFormatBinary,
			DataDir:           dataDir,
			SnapshotEnabled:   true,
			SnapshotThreshold: 8,
			ReadPolicy:        "follower",
		}
	}

	nodes := []*nodeProcess{
		startNodeProcess(t, root, nodeConfig{name: "fr-n1", cfg: newCfg("fr-n1", grpc1, http1, raft1, metrics1, baseDir+"/n1")}),
		startNodeProcess(t, root, nodeConfig{name: "fr-n2", cfg: newCfg("fr-n2", grpc2, http2, raft2, metrics2, baseDir+"/n2")}),
		startNodeProcess(t, root, nodeConfig{name: "fr-n3", cfg: newCfg("fr-n3", grpc3, http3, raft3, metrics3, baseDir+"/n3")}),
	}

	for _, node := range nodes {
		waitHTTPReady(t, node, "/healthz", http.StatusOK, distributedWaitTimeout)
	}

	leader := waitLeader(t, nodes, distributedWaitTimeout)
	require.NotNil(t, leader)

	var follower *nodeProcess
	for _, node := range nodes {
		if node != leader {
			follower = node
			break
		}
	}
	require.NotNil(t, follower)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	leaderClient := newE2EClient(t, leader.cfg.GRPCAddr)
	require.NoError(t, leaderClient.Set(ctx, "fr:key1", "value-1", 0))

	// Reads served directly by the follower must see the replicated value.
	followerClient := newE2EClient(t, follower.cfg.GRPCAddr)
	require.Eventually(t, func() bool {
		val, found, err := followerClient.Get(ctx, "fr:key1")
		return err == nil && found && val == "value-1"
	}, 5*time.Second, 50*time.Millisecond, "follower must serve the replicated read")

	// Writes on the follower must still be rejected.
	require.Error(t, followerClient.Set(ctx, "fr:bad", "x", 0))

	// Linearizability spot check: write through the leader, immediately read
	// from the follower. The read index obtained from the leader must
	// already cover the write (it was applied before Set returned).
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("fr:lin:%d", i)
		require.NoError(t, leaderClient.Set(ctx, key, "v", 0))
		val, found, err := followerClient.Get(ctx, key)
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "v", val)
	}

	// Both followers serve reads (not just one).
	otherFollower := nodes[0]
	for _, node := range nodes {
		if node != leader && node != follower {
			otherFollower = node
			break
		}
	}
	if otherFollower != leader {
		otherClient := newE2EClient(t, otherFollower.cfg.GRPCAddr)
		require.Eventually(t, func() bool {
			val, found, err := otherClient.Get(ctx, "fr:key1")
			return err == nil && found && val == "value-1"
		}, 5*time.Second, 50*time.Millisecond, "second follower must also serve reads")
	}

	// Client-side read distribution: WithReadFromFollowers round-robins reads
	// over all nodes. Reads must keep succeeding even after a follower dies.
	clusterClient, err := client.NewCluster(ctx, []client.NodeSpec{
		{ID: "fr-n1", GRPCAddr: grpc1, RaftAddr: "http://" + raft1},
		{ID: "fr-n2", GRPCAddr: grpc2, RaftAddr: "http://" + raft2},
		{ID: "fr-n3", GRPCAddr: grpc3, RaftAddr: "http://" + raft3},
	}, client.WithReadFromFollowers())
	require.NoError(t, err)
	defer clusterClient.Close()

	require.NoError(t, clusterClient.Set(ctx, "fr:dist:key", "dist-value", 0))
	for i := 0; i < 20; i++ {
		val, found, err := clusterClient.Get(ctx, "fr:dist:key")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "dist-value", val)
	}

	// Kill one node; distributed reads keep working through the survivors.
	follower.stop(t)
	for i := 0; i < 20; i++ {
		val, found, err := clusterClient.Get(ctx, "fr:dist:key")
		require.NoError(t, err)
		require.True(t, found)
		require.Equal(t, "dist-value", val)
	}
}
