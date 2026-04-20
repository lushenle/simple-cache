package command

import (
	"fmt"

	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/pb"
)

type SetCommand struct {
	Key    string
	Value  any
	Expire string
}

func (c *SetCommand) Apply(cache *cache.Cache) (interface{}, error) {
	if err := validateKey(c.Key); err != nil {
		return &pb.SetResponse{Success: false}, err
	}
	if err := cache.Set(c.Key, c.Value, c.Expire); err != nil {
		return &pb.SetResponse{Success: false}, err
	}
	return &pb.SetResponse{Success: true}, nil
}

type DelCommand struct {
	Key string
}

func (c *DelCommand) Apply(cache *cache.Cache) (interface{}, error) {
	if err := validateKey(c.Key); err != nil {
		return &pb.DelResponse{Success: false, Existed: false}, err
	}
	existed := cache.Del(c.Key)
	return &pb.DelResponse{Success: true, Existed: existed}, nil
}

type ExpireKeyCommand struct {
	Key    string
	Expire string
}

func (c *ExpireKeyCommand) Apply(cache *cache.Cache) (interface{}, error) {
	if err := validateKey(c.Key); err != nil {
		return &pb.ExpireKeyResponse{Success: false, Existed: false}, err
	}
	existed := cache.SetExpiration(c.Key, c.Expire)
	return &pb.ExpireKeyResponse{Success: true, Existed: existed}, nil
}

type ResetCommand struct{}

func (c *ResetCommand) Apply(cache *cache.Cache) (interface{}, error) {
	cleared := cache.Reset()
	return &pb.ResetResponse{
		Success:     true,
		KeysCleared: int32(cleared),
	}, nil
}

type SearchCommand struct {
	Pattern  string
	UseRegex bool
}

func (c *SearchCommand) Apply(cache *cache.Cache) (interface{}, error) {
	keys, err := cache.Search(c.Pattern, c.UseRegex)
	if err != nil {
		return nil, err
	}
	return &pb.SearchResponse{Keys: keys}, nil
}

func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("key must not be empty")
	}
	return nil
}
