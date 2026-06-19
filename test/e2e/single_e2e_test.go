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

func TestSingleModeE2E(t *testing.T) {
	root := repoRoot(t)
	dataDir := t.TempDir()

	cfg := &config.Config{
		Mode:              common.ModeSingle,
		NodeID:            "single-e2e",
		GRPCAddr:          freeAddr(t),
		HTTPAddr:          freeAddr(t),
		RaftHTTPAddr:      freeAddr(t),
		MetricsAddr:       freeAddr(t),
		Peers:             nil,
		HeartbeatMS:       200,
		ElectionMS:        1200,
		HotReload:         false,
		LoadOnStartup:     true,
		DumpOnShutdown:    true,
		DumpFormat:        common.DumpFormatBinary,
		DataDir:           dataDir,
		AuthToken:         "",
		EnableTLS:         false,
		TLSCertFile:       "",
		TLSKeyFile:        "",
		AllowedOrigins:    nil,
		SnapshotEnabled:   true,
		SnapshotThreshold: 8,
	}

	node := startNodeProcess(t, root, nodeConfig{name: "single", cfg: cfg})
	health := waitHTTPReady(t, node, "/healthz", http.StatusOK, singleWaitTimeout)
	ready := waitHTTPReady(t, node, "/readyz", http.StatusOK, singleWaitTimeout)
	require.Equal(t, common.ModeSingle, health.Mode)
	require.Equal(t, common.ModeSingle, ready.Mode)
	require.True(t, ready.Ready)

	cli := newE2EClient(t, cfg.GRPCAddr)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	require.NoError(t, cli.Set(ctx, "single:key", "value-1", 0))
	val, found, err := cli.Get(ctx, "single:key")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "value-1", val)

	results, err := cli.Search(ctx, "single:*", false)
	require.NoError(t, err)
	require.Contains(t, results, "single:key")

	existedOnDelete, err := cli.Del(ctx, "single:key")
	require.NoError(t, err)
	require.True(t, existedOnDelete)
	_, found, err = cli.Get(ctx, "single:key")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, cli.Set(ctx, "ttl:key", "ttl-value", 150*time.Millisecond))
	time.Sleep(300 * time.Millisecond)
	_, found, err = cli.Get(ctx, "ttl:key")
	require.NoError(t, err)
	require.False(t, found)

	require.NoError(t, cli.Set(ctx, "ttl:keep", "keep-value", 120*time.Millisecond))
	existed, err := cli.ExpireKey(ctx, "ttl:keep", 0)
	require.NoError(t, err)
	require.True(t, existed)
	time.Sleep(250 * time.Millisecond)
	val, found, err = cli.Get(ctx, "ttl:keep")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "keep-value", val)

	require.NoError(t, cli.Set(ctx, "persist:key", "persist-value", 0))
	node.stop(t)

	restarted := startNodeProcess(t, root, nodeConfig{name: "single-restart", cfg: cfg})
	waitHTTPReady(t, restarted, "/healthz", http.StatusOK, singleWaitTimeout)
	waitHTTPReady(t, restarted, "/readyz", http.StatusOK, singleWaitTimeout)

	cli2 := newE2EClient(t, cfg.GRPCAddr)
	val, found, err = cli2.Get(ctx, "persist:key")
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, val)
}
