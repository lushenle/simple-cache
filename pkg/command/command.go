package command

import (
	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/utils"
)

type Command interface {
	Apply(c *cache.Cache) (interface{}, error)
}

type SetCommand struct {
	Key    string
	Value  any
	Expire string
}

func (c *SetCommand) Apply(cache *cache.Cache) (interface{}, error) {
	err := cache.Set(c.Key, c.Value, c.Expire)
	return &pb.SetResponse{Success: err == nil}, err
}

type GetCommand struct{ Key string }

func (c *GetCommand) Apply(cache *cache.Cache) (interface{}, error) {
	value, found := cache.Get(c.Key)

	val, err := utils.ConvertToAnyPB(value)
	if err != nil {
		return &pb.GetResponse{Value: nil, Found: false}, err
	}

	return &pb.GetResponse{Value: val, Found: found}, nil
}

type DelCommand struct{ Key string }

func (c *DelCommand) Apply(cache *cache.Cache) (interface{}, error) {
	existed := cache.Del(c.Key)
	return &pb.DelResponse{Success: true, Existed: existed}, nil
}

type ExpireKeyCommand struct{ Key string }

func (c *ExpireKeyCommand) Apply(cache *cache.Cache) (interface{}, error) {
	existed := cache.Del(c.Key)
	return &pb.ExpireKeyResponse{Success: true, Existed: existed}, nil
}

type ResetCommand struct{}

func (c *ResetCommand) Apply(cache *cache.Cache) (interface{}, error) {
	count := cache.Reset()
	return &pb.ResetResponse{Success: true, KeysCleared: int32(count)}, nil
}

type SearchCommand struct {
	Pattern  string
	UseRegex bool
}

func (c *SearchCommand) Apply(cache *cache.Cache) (interface{}, error) {
	keys, err := cache.Search(c.Pattern, c.UseRegex)
	if err != nil {
		return &pb.SearchResponse{Keys: nil}, err
	}
	return &pb.SearchResponse{Keys: keys}, nil
}
