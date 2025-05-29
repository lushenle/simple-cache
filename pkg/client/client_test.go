package client

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

const (
	bufSize     = 1024 * 1024
	testKey     = "test-key"
	testValue   = "test-value"
	testTTL     = 10 * time.Second
	testPattern = `^test-\d$`
)

var lis *bufconn.Listener

// init sets up a bufconn listener and a gRPC server for testing.
func init() {
	lis = bufconn.Listen(bufSize)
	s := grpc.NewServer()

	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	// Use a real cache for integration-like tests within the unit test file.
	// The cache.New parameters (e.g., cleanup interval) might be relevant for specific timing tests.
	cacheSrv := server.New(cache.New(10*time.Second, logger))
	pb.RegisterCacheServiceServer(s, cacheSrv)
	go func() {
		if err := s.Serve(lis); err != nil {
			logger.Error("failed to start test server", zap.Error(err))
			// In a test init, panicking is often acceptable if setup fails catastrophically.
			panic(err)
		}
	}()
}

// bufDialer is a custom dialer for bufconn.
func bufDialer(context.Context, string) (net.Conn, error) {
	return lis.Dial()
}

// newTestClient creates a new client connected to the test server.
func newTestClient(t *testing.T) *Client {
	// Use a short timeout for the test client connection itself, as it's connecting to a bufconn.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, "bufnet", // "bufnet" is a conventional name for bufconn
		grpc.WithContextDialer(bufDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(), // Ensure the connection is established before returning
	)
	require.NoError(t, err, "failed to dial bufnet")

	return &Client{
		conn:   conn,
		client: pb.NewCacheServiceClient(conn),
	}
}

func TestClient_New(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	tests := []struct {
		name          string
		address       string
		dialOpts      []grpc.DialOption
		timeout       time.Duration
		expectError   bool
		expectErrorIs error // Specific error to check with errors.Is
		checkClient   func(t *testing.T, cli *Client)
	}{
		{
			name:    "Success",
			address: "bufnet",
			dialOpts: []grpc.DialOption{
				grpc.WithContextDialer(bufDialer),
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
			timeout:     2 * time.Second,
			expectError: false,
			checkClient: func(t *testing.T, cli *Client) {
				assert.NotNil(t, cli)
				if cli != nil {
					assert.NoError(t, cli.Close())
				}
			},
		},
		{
			name:    "InvalidAddressFormat", // Changed from InvalidAddress for clarity
			address: "invalid-address-that-does-not-exist:12345",
			dialOpts: []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
			timeout:     500 * time.Millisecond,
			expectError: true,
			// Error might be context.DeadlineExceeded due to waitForConnectionReady or a direct dialing error.
			// For an invalid format, gRPC might fail fast. For a valid format but unreachable, it's more likely timeout.
			// Let's keep it general assert.Error for now, as specific error can be flaky depending on OS/network stack timing.
		},
		{
			name:    "UnreachableAddress",
			address: "192.0.2.1:12345", // TEST-NET-1, reserved for documentation
			dialOpts: []grpc.DialOption{
				grpc.WithTransportCredentials(insecure.NewCredentials()),
			},
			timeout:       500 * time.Millisecond,
			expectError:   true,
			expectErrorIs: context.DeadlineExceeded, // Expecting timeout from waitForConnectionReady
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()

			cli, err := New(ctx, tc.address, tc.dialOpts...)

			if tc.expectError {
				assert.Error(t, err)
				if tc.expectErrorIs != nil {
					assert.ErrorIs(t, err, tc.expectErrorIs)
				}
			} else {
				assert.NoError(t, err)
			}

			if tc.checkClient != nil {
				tc.checkClient(t, cli)
			} else if !tc.expectError && cli != nil { // Default close if not expecting error and client is not nil
				assert.NoError(t, cli.Close())
			}
		})
	}
}

// TestClient_NewDefault tests the NewDefault client constructor.
func TestClient_NewDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli, err := NewDefault("bufnet",
		grpc.WithContextDialer(bufDialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	assert.NoError(t, err)
	assert.NotNil(t, cli)
	if cli != nil {
		assert.NoError(t, cli.Close())
	}
}

func TestClient_GetSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

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
		_, found, err := cli.Get(ctx, "non-existent-key-getset")
		assert.NoError(t, err)
		assert.False(t, found)
	})

	t.Run("SetWithZeroTTL", func(t *testing.T) {
		key := "zero-ttl-key"
		err := cli.Set(ctx, key, "somevalue", 0) // 0 TTL means no expiration by default in cache
		assert.NoError(t, err)

		val, found, err := cli.Get(ctx, key)
		assert.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "somevalue", val)
	})

	t.Run("SetWithNegativeTTL", func(t *testing.T) {
		key := "negative-ttl-key"
		// Behavior of negative TTL depends on server's cache implementation (and formatTTL).
		// formatTTL turns negative into "", which cache might treat as no expiry.
		err := cli.Set(ctx, key, "somevalue", -5*time.Second)
		assert.NoError(t, err)

		val, found, err := cli.Get(ctx, key)
		assert.NoError(t, err)
		assert.True(t, found)
		assert.Equal(t, "somevalue", val)
	})
}

func TestClient_Del(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	t.Run("DeleteExisting", func(t *testing.T) {
		key := "del-existing-key"
		require.NoError(t, cli.Set(ctx, key, testValue, 0))

		existed, err := cli.Del(ctx, key)
		assert.NoError(t, err)
		assert.True(t, existed)

		_, found, err := cli.Get(ctx, key)
		assert.NoError(t, err)
		assert.False(t, found, "key should be deleted")
	})

	t.Run("DeleteNonExistent", func(t *testing.T) {
		existed, err := cli.Del(ctx, "non-existent-key-del")
		assert.NoError(t, err)
		assert.False(t, existed)
	})
}

func TestClient_Search(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	// Ensure a clean slate for search tests by resetting first, or ensure keys are unique to this test.
	_, err := cli.Reset(ctx)
	require.NoError(t, err)

	keysToSet := map[string]string{
		"search-test-1":  "val1",
		"search-test-2":  "val2",
		"search-other-3": "val3",
		"another-key":    "val4",
	}
	for k, v := range keysToSet {
		require.NoError(t, cli.Set(ctx, k, v, 0))
	}

	t.Run("WildcardSearch", func(t *testing.T) {
		results, err := cli.Search(ctx, "search-test-*", false)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"search-test-1", "search-test-2"}, results)
	})

	t.Run("RegexSearch", func(t *testing.T) {
		results, err := cli.Search(ctx, `^search-test-\d$`, true)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"search-test-1", "search-test-2"}, results)
	})

	t.Run("SearchNoMatch", func(t *testing.T) {
		results, err := cli.Search(ctx, "non-matching-pattern-*", false)
		assert.NoError(t, err)
		assert.Empty(t, results)
	})

	t.Run("SearchAllWithSpecificWildcard", func(t *testing.T) {
		results, err := cli.Search(ctx, "search-*", false)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"search-test-1", "search-test-2", "search-other-3"}, results)
	})
}

// TestClient_ExpireKey tests the ExpireKey client method.
func TestClient_ExpireKey(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	keyToExpire := "expire-me-key"
	keyNotToExpire := "dont-expire-me-key"

	// Set keys
	require.NoError(t, cli.Set(ctx, keyToExpire, "some value", 20*time.Second)) // Long TTL initially
	require.NoError(t, cli.Set(ctx, keyNotToExpire, "another value", 20*time.Second))

	t.Run("ExpireExistingKey", func(t *testing.T) {
		existed, err := cli.ExpireKey(ctx, keyToExpire)
		assert.NoError(t, err)
		assert.True(t, existed, "ExpireKey should return true for an existing key")

		// With bufconn, operations are typically synchronous enough that a sleep is not needed.
		// If this test becomes flaky, it might indicate an unexpected async behavior in the cache itself.
		_, found, errGet := cli.Get(ctx, keyToExpire)
		assert.NoError(t, errGet)
		assert.False(t, found, "Key should not be found after ExpireKey")
	})

	t.Run("ExpireNonExistingKey", func(t *testing.T) {
		existed, err := cli.ExpireKey(ctx, "non-existent-key-for-expire")
		assert.NoError(t, err)
		assert.False(t, existed, "ExpireKey should return false for a non-existing key")
	})

	t.Run("KeyNotExpiredRemains", func(t *testing.T) {
		// Ensure the other key was not affected by the previous ExpireKey call
		val, found, err := cli.Get(ctx, keyNotToExpire)
		assert.NoError(t, err)
		assert.True(t, found, "Other key should still exist")
		assert.Equal(t, "another value", val)
	})
}

// TestClient_Reset tests the Reset client method.
func TestClient_Reset(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	// Set some keys
	keysToSet := []string{"reset-key-1", "reset-key-2", "reset-key-3"}
	for i, k := range keysToSet {
		require.NoError(t, cli.Set(ctx, k, "value"+string(rune(i)), 0))
	}

	// Verify they are set
	for _, k := range keysToSet {
		_, found, err := cli.Get(ctx, k)
		require.NoError(t, err)
		require.True(t, found, "Key %s should exist before reset", k)
	}

	// Perform Reset
	clearedCount, err := cli.Reset(ctx)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, clearedCount, len(keysToSet), "Should clear at least the keys we set")

	// Verify keys are cleared
	for _, k := range keysToSet {
		_, found, err := cli.Get(ctx, k)
		assert.NoError(t, err)
		assert.False(t, found, "Key %s should not exist after reset", k)
	}

	// Test resetting an already empty cache (or after a reset)
	clearedCountAgain, err := cli.Reset(ctx)
	assert.NoError(t, err)
	assert.Equal(t, 0, clearedCountAgain, "Resetting an empty cache should clear 0 keys")
}

// TestClient_BatchSet tests the BatchSet client method.
func TestClient_BatchSet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	defer cli.Close()
	ctx := context.Background()

	// Ensure a clean state for batch set tests by resetting first.
	_, err := cli.Reset(ctx)
	require.NoError(t, err, "failed to reset cache before batch set test")

	t.Run("SetMultipleItems", func(t *testing.T) {
		itemsToSet := map[string]string{
			"batch-key-1": "batch-val-1",
			"batch-key-2": "batch-val-2",
			"batch-key-3": "batch-val-3",
		}
		testTTL := 5 * time.Second

		err := cli.BatchSet(ctx, itemsToSet, testTTL)
		assert.NoError(t, err)

		for k, expectedV := range itemsToSet {
			v, found, getErr := cli.Get(ctx, k)
			assert.NoError(t, getErr, "Error getting key %s", k)
			assert.True(t, found, "Key %s not found after batch set", k)
			assert.Equal(t, expectedV, v, "Value mismatch for key %s", k)
		}

		// Optional: Verify TTL if your cache and Get method can expose it or if behavior changes near expiry.
		// This is more complex and depends on cache's internal time handling vs. test execution time.
		// For now, we'll assume if it's set and retrievable, TTL was applied as per server logic.
	})

	t.Run("SetEmptyMap", func(t *testing.T) {
		err := cli.BatchSet(ctx, map[string]string{}, 5*time.Second)
		assert.NoError(t, err, "BatchSet with empty map should not error")
	})

	t.Run("SetWithExistingKeys", func(t *testing.T) {
		// Ensure a clean state for this sub-test
		_, resetErr := cli.Reset(ctx)
		require.NoError(t, resetErr)

		initialKey := "batch-existing-1"
		initialValue := "initial-value"
		require.NoError(t, cli.Set(ctx, initialKey, initialValue, 0)) // Set one key initially

		itemsToSet := map[string]string{
			initialKey:    "updated-value", // This key will be overwritten
			"batch-new-2": "new-value-2",
		}

		err := cli.BatchSet(ctx, itemsToSet, 10*time.Second)
		assert.NoError(t, err)

		// Check updated value
		v, found, getErr := cli.Get(ctx, initialKey)
		assert.NoError(t, getErr)
		assert.True(t, found)
		assert.Equal(t, "updated-value", v)

		// Check new value
		v2, found2, getErr2 := cli.Get(ctx, "batch-new-2")
		assert.NoError(t, getErr2)
		assert.True(t, found2)
		assert.Equal(t, "new-value-2", v2)
	})
}

// TestClient_Close tests the Close method.
func TestClient_Close(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode")
	}

	cli := newTestClient(t)
	assert.NoError(t, cli.Close())

	// Verify connection is closed
	_, _, err := cli.Get(context.Background(), testKey)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "connection is closing")
}
