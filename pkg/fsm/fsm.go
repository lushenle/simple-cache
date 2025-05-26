package fsm

import (
	"fmt"

	"github.com/lushenle/simple-cache/pkg/cache"
)

type Command interface {
	Apply(c *cache.Cache) (interface{}, error)
}

type FSM struct {
	Cache *cache.Cache
}

func New(c *cache.Cache) *FSM {
	return &FSM{Cache: c}
}

func (f *FSM) Apply(cmd interface{}) (interface{}, error) {
	if cmd == nil {
		return nil, fmt.Errorf("nil command")
	}

	realCmd, ok := cmd.(Command)
	if !ok {
		return nil, fmt.Errorf("invalid command type: %T", cmd)
	}

	return realCmd.Apply(f.Cache)
}
