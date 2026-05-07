package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NodeSpec describes a single cluster node.
type NodeSpec struct {
	ID       string `json:"id"`
	GRPCAddr string `json:"grpc_addr"`
	RaftAddr string `json:"raft_addr,omitempty"`
}

// healthProbe is the JSON shape returned by /healthz.
type healthProbe struct {
	Status         string         `json:"status"`
	Mode           string         `json:"mode"`
	Ready          bool           `json:"ready"`
	Role           string         `json:"role"`
	LeaderID       string         `json:"leader_id,omitempty"`
	LeaderGRPCAddr string         `json:"leader_grpc_addr,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
}

// Client is a gRPC client for the simple-cache service with automatic
// leader failover in distributed mode.
//
// Phase 1: Peer list + retry + background health check
// Phase 2: Leader discovery via health endpoint / gRPC redirect
// Phase 3: Optional gRPC name resolver (see resolver/ package)
type Client struct {
	mu     sync.Mutex
	closed bool

	// ---- cluster state ----
	nodes   []NodeSpec // all known nodes
	idx     int        // index of the connected node in nodes
	nodeMap map[string]int

	// ---- gRPC connection ----
	conn   *grpc.ClientConn
	client pb.CacheServiceClient

	// ---- http client for health probes ----
	httpClient *http.Client

	// ---- configuration ----
	retryCount    int
	checkInterval time.Duration
	dialOpts     []grpc.DialOption

	// ---- lifecycle ----
	stopCh chan struct{}
	wg     sync.WaitGroup
}

// ClientOption configures the cluster-aware client.
type ClientOption func(*clientOpts)

type clientOpts struct {
	retryCount    int
	checkInterval time.Duration
	dialOpts     []grpc.DialOption
	httpClient   *http.Client
}

func defaultOpts() *clientOpts {
	return &clientOpts{
		retryCount:    2,
		checkInterval: 10 * time.Second,
		dialOpts: []grpc.DialOption{
			grpc.WithConnectParams(grpc.ConnectParams{MinConnectTimeout: 3 * time.Second}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		},
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}
}

// WithRetryCount sets the number of automatic retries on leader-change / network errors.
func WithRetryCount(n int) ClientOption {
	return func(o *clientOpts) { o.retryCount = n }
}

// WithCheckInterval sets how often the background goroutine probes the cluster
// for the current leader.
func WithCheckInterval(d time.Duration) ClientOption {
	return func(o *clientOpts) { o.checkInterval = d }
}

// WithGRPCDialOptions sets custom gRPC dial options (e.g. TLS).
func WithGRPCDialOptions(opts ...grpc.DialOption) ClientOption {
	return func(o *clientOpts) { o.dialOpts = opts }
}

// ---------------------------------------------------------------------------
// Constructors
// ---------------------------------------------------------------------------

// New creates a single-node client (backwards compatible).
func New(ctx context.Context, addr string, opts ...grpc.DialOption) (*Client, error) {
	return NewCluster(ctx, []NodeSpec{{GRPCAddr: addr}}, WithGRPCDialOptions(opts...))
}

// NewDefault creates a single-node client with default options.
func NewDefault(addr string, opts ...grpc.DialOption) (*Client, error) {
	return New(context.Background(), addr, opts...)
}

// NewSecure creates a single-node TLS client.
func NewSecure(ctx context.Context, addr, certFile string, opts ...grpc.DialOption) (*Client, error) {
	pool := x509.NewCertPool()
	pemData, err := os.ReadFile(certFile)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(pemData) {
		return nil, fmt.Errorf("failed to append certs from %s", certFile)
	}
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    pool,
	}
	return New(ctx, addr, append([]grpc.DialOption{
		grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)),
	}, opts...)...)
}

// NewCluster creates a multi-node cluster-aware client.
//
// The nodes list tells the client about every peer in the cluster.  The client
// probes each node until it finds the current Raft leader and then establishes
// a gRPC connection to it.  A background goroutine periodically re-checks the
// leader; on leader changes the connection is automatically migrated.
func NewCluster(ctx context.Context, nodes []NodeSpec, opts ...ClientOption) (*Client, error) {
	o := defaultOpts()
	for _, fn := range opts {
		fn(o)
	}

	if len(nodes) == 0 {
		return nil, errors.New("at least one node is required")
	}

	c := &Client{
		nodes:         append([]NodeSpec(nil), nodes...),
		nodeMap:       make(map[string]int),
		retryCount:    o.retryCount,
		checkInterval: o.checkInterval,
		dialOpts:     o.dialOpts,
		httpClient:   o.httpClient,
		stopCh:       make(chan struct{}),
	}
	for i := range c.nodes {
		if c.nodes[i].ID != "" {
			c.nodeMap[c.nodes[i].ID] = i
		}
	}

	// Initial leader discovery: try nodes in order.
	if err := c.discoverLeader(ctx); err != nil {
		return nil, err
	}

	// Start background health checker.
	c.wg.Add(1)
	go c.healthCheckLoop()

	return c, nil
}

// Close shuts down the client and its background goroutine.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()

	if c.stopCh != nil {
		close(c.stopCh)
	}
	c.wg.Wait()

	c.mu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
	}
	c.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------
// Leader discovery & rebalancing (Phase 1 + Phase 2)
// ---------------------------------------------------------------------------

// discoverLeader tries each node in order until it finds one that is either
// the Leader or (failing that) any reachable node.
func (c *Client) discoverLeader(ctx context.Context) error {
	// Phase 2 fast path: query each node's /healthz to find who the leader is.
	for i, node := range c.nodes {
		if node.GRPCAddr == "" {
			continue
		}
		leaderAddr := c.probeLeaderGRPCAddr(ctx, node)
		if leaderAddr != "" {
			if err := c.connectTo(ctx, leaderAddr); err != nil {
				continue
			}
			return nil
		}
		// If the node itself is the leader, connect.
		if c.hasRole(ctx, node, "leader") {
			if err := c.connectTo(ctx, node.GRPCAddr); err != nil {
				continue
			}
			return nil
		}
		// If this is a follower that returned a leader_grpc_addr, use it.
		_ = i // fallback handled below
	}

	// Phase 1 fallback: connect to the first reachable node.
	for _, node := range c.nodes {
		if node.GRPCAddr == "" {
			continue
		}
		if err := c.connectTo(ctx, node.GRPCAddr); err == nil {
			return nil
		}
	}
	return fmt.Errorf("no reachable cluster node (tried %d nodes)", len(c.nodes))
}

// rebalance is called when the current connection's node is no longer the leader.
// It first tries the health-probe fast path (Phase 2), then falls back to probing
// every known peer (Phase 1).
func (c *Client) rebalance(ctx context.Context) error {
	// Phase 2: check each node's health endpoint for leader_grpc_addr.
	for _, node := range c.nodes {
		if node.GRPCAddr == "" {
			continue
		}
		leaderAddr := c.probeLeaderGRPCAddr(ctx, node)
		if leaderAddr != "" {
			c.mu.Lock()
			idx, ok := c.nodeMap[leaderAddr]
			c.mu.Unlock()
			if ok {
				return c.switchTo(ctx, idx)
			}
			// Leader addr not in our node list, connect directly.
			return c.connectTo(ctx, leaderAddr)
		}
	}

	// Phase 1 fallback: try each node, skip current.
	for i := 0; i < len(c.nodes); i++ {
		if i == c.idx {
			continue
		}
		node := c.nodes[i]
		if node.GRPCAddr == "" {
			continue
		}
		if err := c.connectTo(ctx, node.GRPCAddr); err == nil {
			c.mu.Lock()
			c.idx = i
			c.mu.Unlock()
			return nil
		}
	}
	return ErrNoLeader
}

// probeLeaderGRPCAddr calls /healthz on the node's HTTP server and returns
// the leader's gRPC address (Phase 2).  Returns "" on any error or if unknown.
func (c *Client) probeLeaderGRPCAddr(ctx context.Context, node NodeSpec) string {
	// We need the HTTP address.  If we only have the gRPC address we skip
	// the HTTP probe and fall through to the gRPC-level discovery.
	if node.RaftAddr == "" {
		return ""
	}
	// Map the Raft HTTP addr to an HTTP health endpoint.
	base := strings.TrimSuffix(node.RaftAddr, "/")
	base = strings.Replace(base, "http://", "http://", 1)
	url := base + "/healthz"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return ""
	}
	var probe healthProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}

	// The responding node told us the leader's gRPC address directly.
	if probe.LeaderGRPCAddr != "" {
		return probe.LeaderGRPCAddr
	}
	// Map leader_id to gRPC address via our node list.
	if probe.LeaderID != "" {
		c.mu.Lock()
		idx, ok := c.nodeMap[probe.LeaderID]
		c.mu.Unlock()
		if ok {
			return c.nodes[idx].GRPCAddr
		}
	}
	return ""
}

// hasRole checks whether a given node claims the given role in its /healthz.
func (c *Client) hasRole(ctx context.Context, node NodeSpec, role string) bool {
	if node.RaftAddr == "" {
		return false
	}
	base := strings.TrimSuffix(node.RaftAddr, "/")
	url := base + "/healthz"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	var probe healthProbe
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Role == role
}

// ---------------------------------------------------------------------------
// Connection management
// ---------------------------------------------------------------------------

// connectTo dials a gRPC address and replaces the current connection.
func (c *Client) connectTo(ctx context.Context, addr string) error {
	target := addr
	if !strings.Contains(addr, "://") {
		target = "passthrough:///" + addr
	}
	conn, err := grpc.NewClient(target, c.dialOpts...)
	if err != nil {
		return err
	}
	if err := ensureConnected(ctx, conn); err != nil {
		conn.Close()
		return err
	}

	c.mu.Lock()
	old := c.conn
	c.conn = conn
	c.client = pb.NewCacheServiceClient(conn)
	// Find the index in our nodes list.
	if idx, ok := c.nodeMap[addr]; ok {
		c.idx = idx
	} else {
		c.idx = 0
	}
	c.mu.Unlock()

	if old != nil {
		old.Close()
	}
	return nil
}

// switchTo dials the i-th known node and records it as the current connection.
func (c *Client) switchTo(ctx context.Context, i int) error {
	if i < 0 || i >= len(c.nodes) {
		return fmt.Errorf("node index %d out of range", i)
	}
	addr := c.nodes[i].GRPCAddr
	if err := c.connectTo(ctx, addr); err != nil {
		return err
	}
	c.mu.Lock()
	c.idx = i
	c.mu.Unlock()
	return nil
}

// isNotLeaderErr returns true when the gRPC error is "not leader" or the
// connection is unavailable (suggesting the node may have gone down).
func isNotLeaderErr(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return strings.Contains(err.Error(), "not leader")
	}
	if st.Code() == codes.FailedPrecondition && strings.Contains(st.Message(), "not leader") {
		return true
	}
	if st.Code() == codes.Unavailable || st.Code() == codes.DeadlineExceeded {
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Background health check (Phase 1)
// ---------------------------------------------------------------------------

func (c *Client) healthCheckLoop() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.checkLeader()
		}
	}
}

// checkLeader probes the current connection.  If the node is no longer the
// leader, it triggers a rebalance.
func (c *Client) checkLeader() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c.mu.Lock()
	cli := c.client
	c.mu.Unlock()

	if cli == nil {
		return
	}

	// Send a lightweight health check through gRPC (Get on a non-existent key).
	_, err := cli.Get(ctx, &pb.GetRequest{Key: "__simple_cache_health__"})
	if err == nil {
		return // node is reachable and (for leader) accepts reads
	}
	if !isNotLeaderErr(err) {
		return // a different error, skip to avoid aggressive rebalancing
	}

	// The current node is not the leader — rebalance.
	ctxReb, cancelReb := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReb()
	_ = c.rebalance(ctxReb)
}

// ---------------------------------------------------------------------------
// Retry framework (Phase 1)
// ---------------------------------------------------------------------------

// retryableCall wraps a single gRPC call with automatic leader redirection.
func (c *Client) retryableCall(ctx context.Context, fn func(pb.CacheServiceClient) error) error {
	var lastErr error
	attempts := 1 + c.retryCount

	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Brief pause before retry.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}

		c.mu.Lock()
		cli := c.client
		c.mu.Unlock()

		if cli == nil {
			return ErrNoClient
		}

		err := fn(cli)
		if err == nil {
			return nil
		}

		if isNotLeaderErr(err) {
			lastErr = err
			// Try to find the leader.
			reCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			rebalanceErr := c.rebalance(reCtx)
			cancel()
			if rebalanceErr != nil {
				return fmt.Errorf("%w (rebalance failed: %v)", err, rebalanceErr)
			}
			continue
		}

		// Non-retryable error.
		return err
	}
	return lastErr
}

// ---------------------------------------------------------------------------
// Public API methods
// ---------------------------------------------------------------------------

// Get retrieves a value by key.
func (c *Client) Get(ctx context.Context, key string) (any, bool, error) {
	var val any
	var found bool
	err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		resp, rpcErr := cli.Get(ctx, &pb.GetRequest{Key: key})
		if rpcErr != nil {
			return rpcErr
		}
		v, convErr := utils.FromAnyPB(resp.Value)
		if convErr != nil {
			return convErr
		}
		val = v
		found = resp.Found
		return nil
	})
	return val, found, err
}

// Set writes a key-value pair with an optional TTL.
func (c *Client) Set(ctx context.Context, key string, value any, ttl time.Duration) error {
	val, err := utils.ConvertToAnyPB(value)
	if err != nil {
		return err
	}
	return c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		_, rpcErr := cli.Set(ctx, &pb.SetRequest{
			Key:    key,
			Value:  val,
			Expire: formatTTL(ttl),
		})
		return rpcErr
	})
}

// Del deletes a key.
func (c *Client) Del(ctx context.Context, key string) (bool, error) {
	var existed bool
	err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		resp, rpcErr := cli.Del(ctx, &pb.DelRequest{Key: key})
		if rpcErr != nil {
			return rpcErr
		}
		existed = resp.Existed
		return nil
	})
	return existed, err
}

// Search finds keys matching the given pattern.
func (c *Client) Search(ctx context.Context, pattern string, isRegex bool) ([]string, error) {
	var mode pb.SearchRequest_MatchMode
	if isRegex {
		mode = pb.SearchRequest_REGEX
	} else {
		mode = pb.SearchRequest_WILDCARD
	}

	var keys []string
	err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		resp, rpcErr := cli.Search(ctx, &pb.SearchRequest{
			Pattern: pattern,
			Mode:    mode,
		})
		if rpcErr != nil {
			return rpcErr
		}
		keys = resp.Keys
		return nil
	})
	return keys, err
}

// ExpireKey sets/removes expiration on an existing key.
func (c *Client) ExpireKey(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	var existed bool
	err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		resp, rpcErr := cli.ExpireKey(ctx, &pb.ExpireKeyRequest{
			Key:    key,
			Expire: formatTTL(ttl),
		})
		if rpcErr != nil {
			return rpcErr
		}
		existed = resp.Existed
		return nil
	})
	return existed, err
}

// Reset clears all cache data.
func (c *Client) Reset(ctx context.Context) (int, error) {
	var cleared int
	err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
		resp, rpcErr := cli.Reset(ctx, &pb.ResetRequest{})
		if rpcErr != nil {
			return rpcErr
		}
		cleared = int(resp.KeysCleared)
		return nil
	})
	return cleared, err
}

// BatchSet sets multiple key-value pairs sequentially.
func (c *Client) BatchSet(ctx context.Context, items map[string]string, ttl time.Duration) error {
	if len(items) == 0 {
		return nil
	}
	formattedTTL := formatTTL(ttl)
	for key, value := range items {
		val, err := utils.ConvertToAnyPB(value)
		if err != nil {
			return err
		}
		if err := c.retryableCall(ctx, func(cli pb.CacheServiceClient) error {
			_, rpcErr := cli.Set(ctx, &pb.SetRequest{
				Key:    key,
				Value:  val,
				Expire: formattedTTL,
			})
			return rpcErr
		}); err != nil {
			return fmt.Errorf("failed to set key '%s': %w", key, err)
		}
	}
	return nil
}

// WithAuthToken attaches an auth token to the outgoing context.
func WithAuthToken(ctx context.Context, token string) context.Context {
	if token == "" {
		return ctx
	}
	return metadata.AppendToOutgoingContext(ctx, "x-api-token", token)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// formatTTL converts time.Duration to a string suitable for the protobuf field.
func formatTTL(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return d.String()
}

// ensureConnected blocks until the gRPC connection becomes READY.
func ensureConnected(ctx context.Context, conn *grpc.ClientConn) error {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	for {
		state := conn.GetState()
		if state == connectivity.Idle {
			conn.Connect()
		}
		if state == connectivity.Ready {
			return nil
		}
		if !conn.WaitForStateChange(ctx, state) {
			return ctx.Err()
		}
	}
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

var ErrNoLeader = errors.New("no cluster leader reachable")
var ErrNoClient = errors.New("client is not connected")
