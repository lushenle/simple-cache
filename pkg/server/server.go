package server

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/common"
	"github.com/lushenle/simple-cache/pkg/fsm"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/raft"
	"github.com/lushenle/simple-cache/pkg/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ProbeStatus is returned by the health and readiness endpoints.
type ProbeStatus struct {
	Status         common.ProbeState `json:"status"`
	Mode           common.Mode       `json:"mode"`
	Ready          bool              `json:"ready"`
	Role           string            `json:"role"`
	LeaderID       string            `json:"leader_id,omitempty"`
	LeaderGRPCAddr string            `json:"leader_grpc_addr,omitempty"`
	GRPCAddr       string            `json:"grpc_addr,omitempty"`
	Details        map[string]any    `json:"details,omitempty"`
}

// GetLeaderResponse is the response for the GetLeader RPC.
type GetLeaderResponse struct {
	LeaderID       string
	LeaderGRPCAddr string
	IsLeader       bool
}

// simpleRateLimiter implements a token-bucket rate limiter per client.
type simpleRateLimiter struct {
	mu       sync.Mutex
	tokens   map[string]struct {
		count    int
		expireAt time.Time
	}
	maxQPS  int
}

func newSimpleRateLimiter(maxQPS int) *simpleRateLimiter {
	if maxQPS <= 0 {
		return nil
	}
	return &simpleRateLimiter{
		tokens:  make(map[string]struct {
			count    int
			expireAt time.Time
		}),
		maxQPS: maxQPS,
	}
}

func (rl *simpleRateLimiter) Allow(key string) bool {
	if rl == nil {
		return true
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	entry, ok := rl.tokens[key]
	if !ok || now.After(entry.expireAt) {
		rl.tokens[key] = struct {
			count    int
			expireAt time.Time
		}{count: 1, expireAt: now.Add(time.Second)}
		return true
	}
	if entry.count >= rl.maxQPS {
		return false
	}
	entry.count++
	rl.tokens[key] = entry
	return true
}

// CacheService implements the cache RPCs and cluster management.
type CacheService struct {
	pb.UnimplementedCacheServiceServer
	fsm      *fsm.FSM
	node     *raft.Node
	nodeID   string
	grpcAddr string                 // this node's gRPC address
	peerMap  map[string]string      // nodeID → gRPC address for all known peers
	rl       *simpleRateLimiter
	watchSvc *WatchService
}

// New creates a CacheService in single-node mode.
func New(c *cache.Cache, nodeID string) *CacheService {
	return &CacheService{
		fsm:     fsm.New(c),
		nodeID:  nodeID,
		peerMap: make(map[string]string),
	}
}

// SetRateLimiter enables rate limiting on the service.
func (s *CacheService) SetRateLimiter(maxQPS int) {
	s.rl = newSimpleRateLimiter(maxQPS)
}

// SetWatchService enables key change event publishing.
func (s *CacheService) SetWatchService(ws *WatchService) {
	s.watchSvc = ws
}

// SetGRPCAddr sets this node's gRPC address (used if NewWithCluster was not called).
func (s *CacheService) SetGRPCAddr(addr string) {
	s.grpcAddr = addr
}

// SetPeerMap sets the nodeID→gRPC address mapping.
func (s *CacheService) SetPeerMap(m map[string]string) {
	s.peerMap = m
}

// UseRaft attaches a Raft node for distributed mode.
func (s *CacheService) UseRaft(n *raft.Node) {
	s.node = n
}

func (s *CacheService) Apply(cmd interface{}) (interface{}, error) {
	return s.fsm.Apply(cmd)
}

func (s *CacheService) Snapshot(nodeID string) ([]byte, error) {
	return s.fsm.Snapshot(nodeID)
}

func (s *CacheService) RestoreSnapshot(nodeID string, data []byte) error {
	return s.fsm.RestoreSnapshot(nodeID, data)
}

func (s *CacheService) Role() string {
	if s.node == nil {
		return "single"
	}
	return string(s.node.Role())
}

// GetLeader returns the current leader's ID and gRPC address.
func (s *CacheService) GetLeader(ctx context.Context) (*GetLeaderResponse, error) {
	if s.node == nil {
		return &GetLeaderResponse{
			LeaderID:       s.nodeID,
			LeaderGRPCAddr: s.grpcAddr,
			IsLeader:       true,
		}, nil
	}
	leaderID := s.node.LeaderID()
	if leaderID == "" {
		return &GetLeaderResponse{}, nil
	}
	isLeader := leaderID == s.nodeID
	leaderAddr := s.peerMap[leaderID]
	if leaderAddr == "" && isLeader {
		leaderAddr = s.grpcAddr
	}
	return &GetLeaderResponse{
		LeaderID:       leaderID,
		LeaderGRPCAddr: leaderAddr,
		IsLeader:       isLeader,
	}, nil
}

// StepDown forces the current leader to step down.
func (s *CacheService) StepDown(ctx context.Context) error {
	if s.node == nil {
		return nil // single mode, no-op
	}
	return s.node.StepDown()
}

// HealthStatus returns the node's health.
func (s *CacheService) HealthStatus() ProbeStatus {
	status := ProbeStatus{
		Status:   common.ProbeStateOK,
		Mode:     common.ModeSingle,
		Ready:    true,
		Role:     s.Role(),
		GRPCAddr: s.grpcAddr,
	}
	if s.node != nil {
		status.Mode = common.ModeDistributed
		status.LeaderID = s.node.LeaderID()
		status.Details = s.node.Status()
		status.LeaderGRPCAddr = s.peerMap[status.LeaderID]
		if status.LeaderGRPCAddr == "" && s.Role() == "leader" {
			status.LeaderGRPCAddr = s.grpcAddr
		}
	}
	return status
}

// ReadinessStatus returns the node's readiness.
func (s *CacheService) ReadinessStatus() ProbeStatus {
	status := s.HealthStatus()
	if s.node == nil {
		status.Ready = true
		return status
	}

	switch s.node.Role() {
	case raft.Leader:
		status.Ready = true
	case raft.Follower, raft.Candidate:
		status.Ready = false
	default:
		status.Ready = false
	}
	if !status.Ready {
		status.Status = common.ProbeStateNotReady
	}
	return status
}

func (s *CacheService) AddPeer(addr string) error {
	if s.node == nil {
		return raft.ErrInvalidPeerChange{}
	}
	return s.node.AddPeer(addr)
}

func (s *CacheService) RemovePeer(addr string) error {
	if s.node == nil {
		return raft.ErrInvalidPeerChange{}
	}
	return s.node.RemovePeer(addr)
}

func (s *CacheService) Peers() []string {
	if s.node == nil {
		return nil
	}
	return s.node.Peers()
}

// checkLeaderRead returns nil if the node can safely serve reads.
// In distributed mode it runs the ReadIndex protocol to guarantee
// linearizable consistency.
func (s *CacheService) checkLeaderRead(ctx context.Context) error {
	if s.node == nil {
		return nil // single mode
	}
	riCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := s.node.ReadIndex(riCtx); err != nil {
		return status.Errorf(codes.FailedPrecondition, "read index check failed: %v", err)
	}
	return nil
}

func (s *CacheService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if err := s.checkLeaderRead(ctx); err != nil {
		return nil, err
	}
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	value, found := s.fsm.Cache.Get(req.Key)

	val, convErr := utils.ConvertToAnyPB(value)
	if convErr != nil {
		return &pb.GetResponse{Value: nil, Found: false}, status.Error(codes.InvalidArgument, convErr.Error())
	}

	return &pb.GetResponse{Value: val, Found: found}, nil
}

func (s *CacheService) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	cmd := &command.SetCommand{
		Key:    req.Key,
		Value:  req.Value,
		Expire: req.Expire,
	}
	var resp interface{}
	var err error
	if s.node != nil {
		resp, err = s.node.Submit(cmd)
	} else {
		resp, err = s.fsm.Apply(cmd)
	}
	if err != nil {
		return nil, err
	}
	if s.watchSvc != nil {
		s.watchSvc.PublishSet(ctx, req.Key, req.Value)
	}
	return resp.(*pb.SetResponse), nil
}

func (s *CacheService) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	cmd := &command.DelCommand{Key: req.Key}
	var resp interface{}
	var err error
	if s.node != nil {
		resp, err = s.node.Submit(cmd)
	} else {
		resp, err = s.fsm.Apply(cmd)
	}
	if err != nil {
		return nil, err
	}
	if s.watchSvc != nil && resp.(*pb.DelResponse).Existed {
		s.watchSvc.PublishDel(req.Key)
	}
	return resp.(*pb.DelResponse), nil
}

func (s *CacheService) ExpireKey(ctx context.Context, req *pb.ExpireKeyRequest) (*pb.ExpireKeyResponse, error) {
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	cmd := &command.ExpireKeyCommand{Key: req.Key, Expire: req.Expire}
	var resp interface{}
	var err error
	if s.node != nil {
		resp, err = s.node.Submit(cmd)
	} else {
		resp, err = s.fsm.Apply(cmd)
	}
	if err != nil {
		return nil, err
	}
	if s.watchSvc != nil && resp.(*pb.ExpireKeyResponse).Existed {
		s.watchSvc.PublishExpire(req.Key)
	}
	return resp.(*pb.ExpireKeyResponse), nil
}

func (s *CacheService) Reset(ctx context.Context, req *pb.ResetRequest) (*pb.ResetResponse, error) {
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	cmd := &command.ResetCommand{}
	var resp interface{}
	var err error
	if s.node != nil {
		resp, err = s.node.Submit(cmd)
	} else {
		resp, err = s.fsm.Apply(cmd)
	}
	if err != nil {
		return nil, err
	}
	return resp.(*pb.ResetResponse), nil
}

func (s *CacheService) Search(ctx context.Context, req *pb.SearchRequest) (*pb.SearchResponse, error) {
	if err := s.checkLeaderRead(ctx); err != nil {
		return nil, err
	}
	if !s.rl.Allow("") {
		return nil, status.Error(codes.ResourceExhausted, "rate limit exceeded")
	}

	cmd := &command.SearchCommand{
		Pattern:  req.Pattern,
		UseRegex: req.Mode == pb.SearchRequest_REGEX,
	}

	resp, err := s.fsm.Apply(cmd)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid pattern: %v", err)
	}

	return resp.(*pb.SearchResponse), nil
}

// Dump exports cache data to a file.
func (s *CacheService) Dump(ctx context.Context, req *pb.DumpRequest) (*pb.DumpResponse, error) {
	format := req.GetFormat()
	if format == "" {
		format = common.DumpFormatBinary.String()
	}

	result, err := s.fsm.Cache.Dump(s.NodeID(), format, req.GetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "dump failed: %v", err)
	}

	return &pb.DumpResponse{
		Success:    true,
		TotalKeys:  result.TotalKeys,
		FileSize:   result.FileSize,
		Path:       result.Path,
		Format:     result.Format,
		DurationMs: result.DurationMs,
	}, nil
}

// Load imports cache data from a file.
func (s *CacheService) Load(ctx context.Context, req *pb.LoadRequest) (*pb.LoadResponse, error) {
	if s.node != nil {
		return nil, status.Error(codes.FailedPrecondition, "load is disabled in distributed mode")
	}
	result, err := s.fsm.Cache.Load(s.NodeID(), req.GetPath())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load failed: %v", err)
	}

	return &pb.LoadResponse{
		Success:     true,
		TotalKeys:   result.TotalKeys,
		LoadedKeys:  result.LoadedKeys,
		SkippedKeys: result.SkippedKeys,
		Path:        result.Path,
		DurationMs:  result.DurationMs,
	}, nil
}

// BatchSet receives a stream of BatchSetRequest, processes them as individual
// Set operations, and returns the count of successes and failures.
func (s *CacheService) BatchSet(stream pb.CacheService_BatchSetServer) error {
	var successCount, errorCount int32
	var firstErr string
	for {
		req, err := stream.Recv()
		if err == io.EOF {
			return stream.SendAndClose(&pb.BatchSetResponse{
				SuccessCount: successCount,
				ErrorCount:   errorCount,
				FirstError:   firstErr,
			})
		}
		if err != nil {
			return err
		}
		if !s.rl.Allow("") {
			errorCount++
			if firstErr == "" {
				firstErr = "rate limit exceeded"
			}
			continue
		}
		cmd := &command.SetCommand{
			Key:    req.Key,
			Value:  req.Value,
			Expire: req.Expire,
		}
		var errApply error
		if s.node != nil {
			_, errApply = s.node.Submit(cmd)
		} else {
			_, errApply = s.fsm.Apply(cmd)
		}
		if errApply != nil {
			errorCount++
			if firstErr == "" {
				firstErr = errApply.Error()
			}
		} else {
			successCount++
		}
	}
}

// NodeID returns the node ID for use in dump file naming.
func (s *CacheService) NodeID() string {
	return s.nodeID
}
