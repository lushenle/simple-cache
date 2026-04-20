//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/common"
	"github.com/lushenle/simple-cache/pkg/config"
	"github.com/stretchr/testify/require"
)

func TestDistributedModeE2E(t *testing.T) {
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

	cfg1 := &config.Config{
		Mode:              common.ModeDistributed,
		NodeID:            "n1-e2e",
		GRPCAddr:          grpc1,
		HTTPAddr:          http1,
		RaftHTTPAddr:      raft1,
		MetricsAddr:       metrics1,
		Peers:             peers,
		HeartbeatMS:       200,
		ElectionMS:        1200,
		HotReload:         false,
		LoadOnStartup:     false,
		DumpOnShutdown:    false,
		DumpFormat:        common.DumpFormatBinary,
		DataDir:           baseDir + "/n1",
		AuthToken:         "",
		EnableTLS:         false,
		TLSCertFile:       "",
		TLSKeyFile:        "",
		AllowedOrigins:    nil,
		SnapshotEnabled:   true,
		SnapshotThreshold: 8,
	}
	cfg2 := &config.Config{
		Mode:              common.ModeDistributed,
		NodeID:            "n2-e2e",
		GRPCAddr:          grpc2,
		HTTPAddr:          http2,
		RaftHTTPAddr:      raft2,
		MetricsAddr:       metrics2,
		Peers:             peers,
		HeartbeatMS:       200,
		ElectionMS:        1200,
		HotReload:         false,
		LoadOnStartup:     false,
		DumpOnShutdown:    false,
		DumpFormat:        common.DumpFormatBinary,
		DataDir:           baseDir + "/n2",
		AuthToken:         "",
		EnableTLS:         false,
		TLSCertFile:       "",
		TLSKeyFile:        "",
		AllowedOrigins:    nil,
		SnapshotEnabled:   true,
		SnapshotThreshold: 8,
	}
	cfg3 := &config.Config{
		Mode:              common.ModeDistributed,
		NodeID:            "n3-e2e",
		GRPCAddr:          grpc3,
		HTTPAddr:          http3,
		RaftHTTPAddr:      raft3,
		MetricsAddr:       metrics3,
		Peers:             peers,
		HeartbeatMS:       200,
		ElectionMS:        1200,
		HotReload:         false,
		LoadOnStartup:     false,
		DumpOnShutdown:    false,
		DumpFormat:        common.DumpFormatBinary,
		DataDir:           baseDir + "/n3",
		AuthToken:         "",
		EnableTLS:         false,
		TLSCertFile:       "",
		TLSKeyFile:        "",
		AllowedOrigins:    nil,
		SnapshotEnabled:   true,
		SnapshotThreshold: 8,
	}

	nodes := []*nodeProcess{
		startNodeProcess(t, root, nodeConfig{name: "n1", cfg: cfg1}),
		startNodeProcess(t, root, nodeConfig{name: "n2", cfg: cfg2}),
		startNodeProcess(t, root, nodeConfig{name: "n3", cfg: cfg3}),
	}

	for _, node := range nodes {
		health := waitHTTPReady(t, node, "/healthz", http.StatusOK, distributedWaitTimeout)
		require.Equal(t, common.ModeDistributed, health.Mode)
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
	waitHTTPReady(t, follower, "/readyz", http.StatusServiceUnavailable, distributedWaitTimeout)

	var peersResp []string
	httpGetJSON(t, "http://"+leader.cfg.HTTPAddr+"/cluster/peers", http.StatusOK, &peersResp)
	require.ElementsMatch(t, peers, peersResp)

	leaderClient := newE2EClient(t, leader.cfg.GRPCAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, leaderClient.Set(ctx, "dist:key1", "value-1", 0))
	val, found, err := leaderClient.Get(ctx, "dist:key1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "value-1", val)

	followerClient := newE2EClient(t, follower.cfg.GRPCAddr)
	_, _, err = followerClient.Get(ctx, "dist:key1")
	require.Error(t, err)
	require.Error(t, followerClient.Set(ctx, "dist:follower", "bad", 0))

	waitForReplication(t, distributedWaitTimeout, func() bool {
		replicatedFollowers := 0
		for _, node := range nodes {
			if node == leader {
				continue
			}
			health, statusCode, ok := tryProbe(node, "/healthz")
			if !ok || statusCode != http.StatusOK {
				continue
			}
			if detailAtLeastOne(health.Details, "commit_index") && detailAtLeastOne(health.Details, "last_applied") {
				replicatedFollowers++
			}
		}
		return replicatedFollowers >= 1
	}, nodes...)

	follower.stop(t)
	leaderReady := waitHTTPReady(t, leader, "/readyz", http.StatusOK, distributedWaitTimeout)
	require.True(t, leaderReady.Ready)

	require.NoError(t, leaderClient.Set(ctx, "dist:key2", "value-2", 0))
	waitForReplication(t, distributedWaitTimeout, func() bool {
		value, ok, getErr := leaderClient.Get(context.Background(), "dist:key2")
		return getErr == nil && ok && value == "value-2"
	}, leader)
}

func detailAtLeastOne(details map[string]any, key string) bool {
	raw, ok := details[key]
	if !ok {
		return false
	}
	value, ok := raw.(float64)
	return ok && value >= 1
}
