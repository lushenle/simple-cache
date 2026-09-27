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

// TestRollingRestartE2E restarts every node process one at a time and
// verifies the cluster keeps serving and no committed data is lost.
func TestRollingRestartE2E(t *testing.T) {
	root := repoRoot(t)
	baseDir := t.TempDir()

	grpcAddrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	httpAddrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	raftAddrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}
	metricsAddrs := []string{freeAddr(t), freeAddr(t), freeAddr(t)}

	peers := []string{
		"http://" + raftAddrs[0],
		"http://" + raftAddrs[1],
		"http://" + raftAddrs[2],
	}
	peerAddrs := map[string]string{
		"rr-n1": grpcAddrs[0],
		"rr-n2": grpcAddrs[1],
		"rr-n3": grpcAddrs[2],
	}

	newCfg := func(i int) *config.Config {
		return &config.Config{
			Mode:              common.ModeDistributed,
			NodeID:            fmt.Sprintf("rr-n%d", i+1),
			GRPCAddr:          grpcAddrs[i],
			HTTPAddr:          httpAddrs[i],
			RaftHTTPAddr:      raftAddrs[i],
			MetricsAddr:       metricsAddrs[i],
			Peers:             peers,
			PeerAddresses:     peerAddrs,
			HeartbeatMS:       200,
			ElectionMS:        1200,
			HotReload:         false,
			LoadOnStartup:     false,
			DumpOnShutdown:    false,
			DumpFormat:        common.DumpFormatBinary,
			DataDir:           fmt.Sprintf("%s/n%d", baseDir, i+1),
			SnapshotEnabled:   true,
			SnapshotThreshold: 8,
		}
	}

	start := func(i int) *nodeProcess {
		return startNodeProcess(t, root, nodeConfig{name: fmt.Sprintf("rr-n%d", i+1), cfg: newCfg(i)})
	}

	processes := []*nodeProcess{start(0), start(1), start(2)}
	for _, p := range processes {
		waitHTTPReady(t, p, "/healthz", http.StatusOK, distributedWaitTimeout)
	}
	require.NotNil(t, waitLeader(t, processes, distributedWaitTimeout))

	clusterClient, err := client.NewCluster(context.Background(), []client.NodeSpec{
		{ID: "rr-n1", GRPCAddr: grpcAddrs[0], RaftAddr: "http://" + raftAddrs[0]},
		{ID: "rr-n2", GRPCAddr: grpcAddrs[1], RaftAddr: "http://" + raftAddrs[1]},
		{ID: "rr-n3", GRPCAddr: grpcAddrs[2], RaftAddr: "http://" + raftAddrs[2]},
	}, client.WithReadFromFollowers())
	require.NoError(t, err)
	defer clusterClient.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for i := 0; i < 10; i++ {
		require.NoError(t, clusterClient.Set(ctx, fmt.Sprintf("rr:%d", i), fmt.Sprintf("v%d", i), 0))
	}

	// Restart each node in turn; after each restart the cluster must
	// re-elect (if needed) and keep serving all previously committed data.
	for i := 0; i < 3; i++ {
		processes[i].stop(t)
		processes[i] = start(i)
		waitHTTPReady(t, processes[i], "/healthz", http.StatusOK, distributedWaitTimeout)
		require.NotNil(t, waitLeader(t, processes, distributedWaitTimeout))

		for k := 0; k < 10; k++ {
			ok := false
			for attempt := 0; attempt < 40; attempt++ {
				val, found, err := clusterClient.Get(ctx, fmt.Sprintf("rr:%d", k))
				if err == nil && found && val == fmt.Sprintf("v%d", k) {
					ok = true
					break
				}
				time.Sleep(250 * time.Millisecond)
			}
			require.True(t, ok, "key rr:%d must survive restart of node %d", k, i+1)
		}
	}
}
