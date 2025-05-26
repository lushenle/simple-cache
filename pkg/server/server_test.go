package server

import (
	"context"
	"testing"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/stretchr/testify/assert"
)

func TestGRPCServer(t *testing.T) {
	c := cache.New()
	srv := New(c)

	t.Run("SetGet", func(t *testing.T) {
		_, err := srv.Set(context.Background(), &pb.SetRequest{
			Key:   "test",
			Value: "value",
		})
		assert.Nil(t, err)

		resp, err := srv.Get(context.Background(), &pb.GetRequest{Key: "test"})
		assert.Nil(t, err)
		assert.Equal(t, "value", resp.Value)
	})

	t.Run("Search", func(t *testing.T) {
		srv.Set(context.Background(), &pb.SetRequest{Key: "user:100", Value: "data"})
		resp, err := srv.Search(context.Background(), &pb.SearchRequest{
			Pattern: "user:*",
		})
		assert.Nil(t, err)
		assert.Contains(t, resp.Keys, "user:100")
	})
}
