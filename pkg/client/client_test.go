package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const (
	bufSize     = 1024 * 1024
	testKey     = "test-key"
	testValue   = "test-value"
	testTTL     = 10 * time.Minute
	testPattern = `^test-\d$`
)

var lis *bufconn.Listener

func init() {
	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer()
	cacheSrv := server.New(cache.New())
	pb.RegisterCacheServiceServer(s, cacheSrv)
	go func() {
		if err := s.Serve(lis); err != nil {
			panic("failed to start test server: " + err.Error())
		}
	}()
}

func bufDialer(context.Context, string) (net.Conn, error) {
	return lis.Dial()
}

func newTestClient(t *testing.T) *Client {
	conn, err := grpc.NewClient("bufnet",
		grpc.WithContextDialer(bufDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	return &Client{
		conn:   conn,
		client: pb.NewCacheServiceClient(conn),
	}
}

func TestClient_New(t *testing.T) {
	t.Run("Success", func(t *testing.T) {
		cli, err := New("bufnet",
			grpc.WithContextDialer(bufDialer),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		assert.NoError(t, err)
		assert.NotNil(t, cli)
		assert.NoError(t, cli.Close())
	})

	t.Run("InvalidAddress", func(t *testing.T) {
		_, err := New("invalid-address",
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)

		assert.Error(t, err)
	})
}

func TestClient_GetSet(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	t.Run("SetAndGet", func(t *testing.T) {
		err := cli.Set(ctx, testKey, testValue, testTTL)
		assert.NoError(t, err)

		val, found, err := cli.Get(ctx, testKey)
		assert.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, testValue, val)
	})

	t.Run("GetNonExistent", func(t *testing.T) {
		_, found, err := cli.Get(ctx, "non-existent-key")
		assert.NoError(t, err)
		assert.False(t, found)
	})
}

func TestClient_Del(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	t.Run("DeleteExisting", func(t *testing.T) {
		require.NoError(t, cli.Set(ctx, testKey, testValue, 0))

		existed, err := cli.Del(ctx, testKey)
		assert.NoError(t, err)
		assert.True(t, existed)
	})

	t.Run("DeleteNonExistent", func(t *testing.T) {
		existed, err := cli.Del(ctx, "non-existent-key")
		assert.NoError(t, err)
		assert.False(t, existed)
	})
}

func TestClient_Search(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	keys := []string{
		"test-1",
		"test-2",
		"other-key",
	}

	for _, key := range keys {
		require.NoError(t, cli.Set(ctx, key, "value", 0))
	}

	t.Run("WildcardSearch", func(t *testing.T) {
		results, err := cli.Search(ctx, "test-*", false)
		assert.NoError(t, err)
		assert.Len(t, results, 2)
		assert.Contains(t, results, "test-1")
		assert.Contains(t, results, "test-2")
	})

	t.Run("RegexSearch", func(t *testing.T) {
		results, err := cli.Search(ctx, testPattern, true)
		assert.NoError(t, err)
		assert.Len(t, results, 2)
	})
}

func TestClient_ExpireKey(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	t.Run("ExpireExisting", func(t *testing.T) {
		require.NoError(t, cli.Set(ctx, testKey, testValue, 0))

		existed, err := cli.ExpireKey(ctx, testKey)
		assert.NoError(t, err)
		assert.True(t, existed)

		_, found, _ := cli.Get(ctx, testKey)
		assert.False(t, found)
	})

	t.Run("ExpireNonExistent", func(t *testing.T) {
		existed, err := cli.ExpireKey(ctx, "non-existent-key")
		assert.NoError(t, err)
		assert.False(t, existed)
	})
}

func TestClient_Reset(t *testing.T) {
	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	// Setup initial data
	keys := []string{"key1", "key2", "key3"}
	for _, key := range keys {
		require.NoError(t, cli.Set(ctx, key, "value", 0))
	}

	t.Run("ResetCache", func(t *testing.T) {
		count, err := cli.Reset(ctx)
		assert.NoError(t, err)
		assert.Equal(t, len(keys), count)

		// Verify all keys are gone
		for _, key := range keys {
			_, found, _ := cli.Get(ctx, key)
			assert.False(t, found)
		}
	})
}

func TestClient_Close(t *testing.T) {
	cli := newTestClient(t)
	assert.NoError(t, cli.Close())

	// Verify connection is closed
	_, _, err := cli.Get(context.Background(), testKey)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection closing")
}
