// Package resolver implements a custom gRPC name resolver that discovers the
// current Raft leader among a static set of cluster peers (Phase 3).
//
// Usage:
//
//	import "github.com/lushenle/simple-cache/pkg/client/resolver"
//
//	conn, err := grpc.NewClient("simplecache:///node1:5051,node2:5051",
//	    grpc.WithResolvers(&gresolver.Builder{}),
//	    grpc.WithTransportCredentials(insecure.NewCredentials()),
//	)
//
// The resolver periodically probes each peer's /healthz endpoint to identify
// the current Raft leader and directs the gRPC connection to it.
package resolver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	gresolver "google.golang.org/grpc/resolver"
)

// Scheme is the URI scheme this resolver handles.
const Scheme = "simplecache"

// probeResponse mirrors server.ProbeStatus for decoding /healthz.
type probeResponse struct {
	LeaderID       string `json:"leader_id,omitempty"`
	LeaderGRPCAddr string `json:"leader_grpc_addr,omitempty"`
	Role           string `json:"role"`
}

// Builder implements gresolver.Builder for the "simplecache" scheme.
type Builder struct {
	// HTTPClient is used for health probes.  If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// ProbeInterval controls how often the resolver re-probes peers.
	// Default: 10 seconds.
	ProbeInterval time.Duration
}

// Build creates a new resolver for the given target.
func (b *Builder) Build(target gresolver.Target, cc gresolver.ClientConn, opts gresolver.BuildOptions) (gresolver.Resolver, error) {
	// Parse peer addresses from the target URL endpoint.
	// The endpoint format is "host1:port1,host2:port2,..."
	peers := parseTarget(target)
	if len(peers) == 0 {
		return nil, fmt.Errorf("simplecache resolver: no endpoints in target URI")
	}

	interval := b.ProbeInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}

	httpClient := b.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}

	r := &simpleCacheResolver{
		peers:    peers,
		cc:       cc,
		client:   httpClient,
		interval: interval,
		stopCh:   make(chan struct{}),
	}

	// Do an initial leader discovery synchronously.
	r.discoverAndUpdate()

	// Start periodic re-discovery in the background.
	go r.loop()
	return r, nil
}

// Scheme returns the URI scheme.
func (b *Builder) Scheme() string { return Scheme }

// simpleCacheResolver implements gresolver.Resolver.
type simpleCacheResolver struct {
	mu       sync.Mutex
	peers    []string // gRPC addresses of all known cluster nodes
	cc       gresolver.ClientConn
	client   *http.Client
	interval time.Duration
	stopCh   chan struct{}
}

// ResolveNow is a no-op; periodic discovery is handled by the background loop.
func (r *simpleCacheResolver) ResolveNow(o gresolver.ResolveNowOptions) {}

// Close stops the background discovery loop.
func (r *simpleCacheResolver) Close() {
	close(r.stopCh)
}

func (r *simpleCacheResolver) loop() {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-ticker.C:
			r.discoverAndUpdate()
		}
	}
}

// discoverAndUpdate probes peers to find the current leader and updates the
// gRPC resolver state.  If no leader can be identified it supplies all peers
// as fallback so the gRPC framework can attempt connections.
func (r *simpleCacheResolver) discoverAndUpdate() {
	leaderAddr := r.discoverLeader()
	r.mu.Lock()
	defer r.mu.Unlock()

	if leaderAddr != "" {
		r.cc.UpdateState(gresolver.State{
			Addresses: []gresolver.Address{{Addr: leaderAddr}},
		})
		return
	}

	// Fallback: supply all peers so gRPC can try each.
	addrs := make([]gresolver.Address, len(r.peers))
	for i, p := range r.peers {
		addrs[i] = gresolver.Address{Addr: p}
	}
	r.cc.UpdateState(gresolver.State{Addresses: addrs})
}

// discoverLeader probes each peer's /healthz looking for the Raft leader.
// It returns the leader's gRPC address or "" if none found.
func (r *simpleCacheResolver) discoverLeader() string {
	// First pass: try to find the leader via health endpoint.
	for _, peerAddr := range r.peers {
		// Derive the health HTTP URL from the gRPC address.
		// Convention: gRPC addr ":5051" → health URL "http://<host>:8080/healthz".
		// Since we don't have the HTTP port mapping, we try the gRPC address
		// as the health endpoint directly (the gateway is often on a different
		// port, so this is a best-effort approach).
		//
		// A more robust approach would include the HTTP address in the peer
		// config (see NodeSpec in the client package).
		healthURL := makeHealthURL(peerAddr)
		if healthURL == "" {
			continue
		}

		probe, err := r.probe(healthURL)
		if err != nil {
			continue
		}

		// Direct leader address from the probe response (Phase 2).
		if probe.LeaderGRPCAddr != "" {
			return probe.LeaderGRPCAddr
		}

		// The responding node is itself the leader.
		if probe.Role == "leader" {
			return peerAddr
		}
	}
	return ""
}

func (r *simpleCacheResolver) probe(url string) (*probeResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var p probeResponse
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// makeHealthURL attempts to derive a /healthz URL from a gRPC address.
// For well-known ports this provides a best-effort mapping.
func makeHealthURL(grpcAddr string) string {
	if grpcAddr == "" {
		return ""
	}
	// If the address already has a scheme, use it as-is.
	if strings.HasPrefix(grpcAddr, "http://") || strings.HasPrefix(grpcAddr, "https://") {
		return strings.TrimSuffix(grpcAddr, "/") + "/healthz"
	}
	// Bare "host:port" — assume the same host with the gRPC port.
	return "http://" + grpcAddr + "/healthz"
}

// parseTarget extracts peer addresses from the resolver target.
// Supported formats:
//
//	simplecache:///host1:port1,host2:port2
//	simplecache:///host1:port1
func parseTarget(target gresolver.Target) []string {
	endpoint := target.URL.Host
	if endpoint == "" {
		endpoint = target.Endpoint()
	}
	if endpoint == "" {
		return nil
	}
	// Split by comma for multiple addresses.
	parts := strings.Split(endpoint, ",")
	peers := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			peers = append(peers, p)
		}
	}
	return peers
}
