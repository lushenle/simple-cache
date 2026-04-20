package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/common"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/raft"
	"github.com/lushenle/simple-cache/pkg/utils"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPCServer(t *testing.T) {
	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	c := cache.New(time.Second*3, logger)
	srv := New(c, "test-node")

	t.Run("SetGet", func(t *testing.T) {
		val, err := utils.ConvertToAnyPB("value")
		assert.Nil(t, err)

		_, err = srv.Set(context.Background(), &pb.SetRequest{
			Key:   "test",
			Value: val,
		})
		assert.Nil(t, err)

		resp, err := srv.Get(context.Background(), &pb.GetRequest{Key: "test"})
		assert.Nil(t, err)
		got, convErr := utils.FromAnyPB(resp.Value)
		assert.Nil(t, convErr)
		assert.Equal(t, "value", got)
	})

	t.Run("Search", func(t *testing.T) {
		val, err := utils.ConvertToAnyPB("data")
		assert.Nil(t, err)

		srv.Set(context.Background(), &pb.SetRequest{Key: "user:100", Value: val})
		resp, err := srv.Search(context.Background(), &pb.SearchRequest{
			Pattern: "user:*",
		})
		assert.Nil(t, err)
		assert.Contains(t, resp.Keys, "user:100")
	})

	t.Run("LoadDisabledInDistributedMode", func(t *testing.T) {
		transportAddr := "127.0.0.1:0"
		node := raft.NewNode(
			"test-node",
			transportAddr,
			[]string{"http://" + transportAddr},
			raft.NewStorage(filepath.Join(t.TempDir(), "raft.wal")),
			srv,
			50*time.Millisecond,
			120*time.Millisecond,
			true,
			8,
			logger,
		)
		defer node.Close()
		srv.UseRaft(node)

		_, err := srv.Load(context.Background(), &pb.LoadRequest{})
		assert.Error(t, err)
		st, ok := status.FromError(err)
		assert.True(t, ok)
		assert.Equal(t, codes.FailedPrecondition, st.Code())
	})

	t.Run("ProbeStatusSingleMode", func(t *testing.T) {
		singleSrv := New(cache.New(time.Second*3, logger), "single-node")
		health := srv.HealthStatus()
		ready := srv.ReadinessStatus()
		health = singleSrv.HealthStatus()
		ready = singleSrv.ReadinessStatus()
		assert.Equal(t, common.ProbeStateOK, health.Status)
		assert.Equal(t, common.ModeSingle, health.Mode)
		assert.True(t, health.Ready)
		assert.True(t, ready.Ready)
	})

	t.Run("ProbeStatusDistributedFollowerNotReady", func(t *testing.T) {
		transportAddr := "127.0.0.1:0"
		node := raft.NewNode(
			"test-node-follower",
			transportAddr,
			[]string{"http://" + transportAddr, "http://" + transportAddr},
			raft.NewStorage(filepath.Join(t.TempDir(), "raft-ready.wal")),
			srv,
			50*time.Millisecond,
			500*time.Millisecond,
			true,
			8,
			logger,
		)
		defer node.Close()
		srv.UseRaft(node)

		health := srv.HealthStatus()
		ready := srv.ReadinessStatus()
		assert.Equal(t, common.ModeDistributed, health.Mode)
		assert.Equal(t, common.ProbeStateOK, health.Status)
		assert.False(t, ready.Ready)
		assert.Equal(t, common.ProbeStateNotReady, ready.Status)
	})
}
