package server

import (
	"context"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/utils"
	"github.com/stretchr/testify/assert"
	"go.uber.org/zap/zapcore"
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
}
