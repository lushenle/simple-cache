package server

import (
	"context"
	"strings"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/command"
	"github.com/lushenle/simple-cache/pkg/fsm"
	"github.com/lushenle/simple-cache/pkg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CacheService struct {
	pb.UnimplementedCacheServiceServer
	fsm *fsm.FSM
}

func New(c *cache.Cache) *CacheService {
	return &CacheService{
		fsm: fsm.New(c),
	}
}

func (s *CacheService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	value, found := s.fsm.Cache.Get(req.Key)
	return &pb.GetResponse{Value: value, Found: found}, nil
}

func (s *CacheService) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	cmd := &command.SetCommand{
		Key:    req.Key,
		Value:  req.Value,
		Expire: req.Expire,
	}
	resp, err := s.fsm.Apply(cmd)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.SetResponse), nil
}

func (s *CacheService) Del(ctx context.Context, req *pb.DelRequest) (*pb.DelResponse, error) {
	cmd := &command.DelCommand{Key: req.Key}
	resp, err := s.fsm.Apply(cmd)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.DelResponse), nil
}

func (s *CacheService) ExpireKey(ctx context.Context, req *pb.ExpireKeyRequest) (*pb.ExpireKeyResponse, error) {
	cmd := &command.ExpireKeyCommand{Key: req.Key}
	resp, err := s.fsm.Apply(cmd)
	if err != nil {
		return nil, err
	}
	return resp.(*pb.ExpireKeyResponse), nil
}

func (s *CacheService) Reset(ctx context.Context, req *pb.ResetRequest) (*pb.ResetResponse, error) {
	cmd := &command.ResetCommand{}
	resp, err := s.fsm.Apply(cmd)
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
