package server

import (
	"context"
	"strings"

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

type ProbeStatus struct {
	Status   common.ProbeState `json:"status"`
	Mode     common.Mode       `json:"mode"`
	Ready    bool              `json:"ready"`
	Role     string            `json:"role"`
	LeaderID string            `json:"leader_id,omitempty"`
	Details  map[string]any    `json:"details,omitempty"`
}

type CacheService struct {
	pb.UnimplementedCacheServiceServer
	fsm    *fsm.FSM
	node   *raft.Node
	nodeID string
}

func New(c *cache.Cache, nodeID string) *CacheService {
	return &CacheService{
		fsm:    fsm.New(c),
		nodeID: nodeID,
	}
}

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

func (s *CacheService) HealthStatus() ProbeStatus {
	status := ProbeStatus{
		Status: common.ProbeStateOK,
		Mode:   common.ModeSingle,
		Ready:  true,
		Role:   s.Role(),
	}
	if s.node != nil {
		status.Mode = common.ModeDistributed
		status.LeaderID = s.node.LeaderID()
		status.Details = s.node.Status()
	}
	return status
}

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

func (s *CacheService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	if s.node != nil && s.node.Role() != raft.Leader {
		return nil, status.Error(codes.FailedPrecondition, "not leader")
	}
	value, found := s.fsm.Cache.Get(req.Key)

	val, err := utils.ConvertToAnyPB(value)
	if err != nil {
		return &pb.GetResponse{Value: nil, Found: false}, status.Error(codes.InvalidArgument, err.Error())
	}

	return &pb.GetResponse{Value: val, Found: found}, nil
}

func (s *CacheService) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
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
	return resp.(*pb.SetResponse), nil
}

func (s *CacheService) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
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
	return resp.(*pb.DelResponse), nil
}

func (s *CacheService) ExpireKey(ctx context.Context, req *pb.ExpireKeyRequest) (*pb.ExpireKeyResponse, error) {
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
	return resp.(*pb.ExpireKeyResponse), nil
}

func (s *CacheService) Reset(ctx context.Context, req *pb.ResetRequest) (*pb.ResetResponse, error) {
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
	pattern := req.Pattern
	useRegex := strings.HasPrefix(pattern, "regex:")

	if useRegex {
		pattern = strings.TrimPrefix(pattern, "regex:")
	}

	cmd := &command.SearchCommand{
		Pattern:  pattern,
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

// NodeID returns the node ID for use in dump file naming.
func (s *CacheService) NodeID() string {
	return s.nodeID
}
