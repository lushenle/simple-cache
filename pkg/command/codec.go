package command

import (
	"encoding/json"
	"fmt"

	"github.com/lushenle/simple-cache/pkg/utils"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	TypeSet       = "set"
	TypeDel       = "del"
	TypeExpireKey = "expire_key"
	TypeReset     = "reset"
)

type encodedSetCommand struct {
	Key    string `json:"key"`
	Value  []byte `json:"value"`
	Expire string `json:"expire,omitempty"`
}

type encodedDelCommand struct {
	Key string `json:"key"`
}

type encodedExpireKeyCommand struct {
	Key    string `json:"key"`
	Expire string `json:"expire,omitempty"`
}

// Encode serializes a replicated command into a stable type name and payload.
func Encode(cmd interface{}) (string, []byte, error) {
	switch c := cmd.(type) {
	case *SetCommand:
		value, err := normalizeAnyValue(c.Value)
		if err != nil {
			return "", nil, err
		}
		payload, err := json.Marshal(encodedSetCommand{
			Key:    c.Key,
			Value:  value,
			Expire: c.Expire,
		})
		if err != nil {
			return "", nil, err
		}
		return TypeSet, payload, nil
	case *DelCommand:
		payload, err := json.Marshal(encodedDelCommand{Key: c.Key})
		if err != nil {
			return "", nil, err
		}
		return TypeDel, payload, nil
	case *ExpireKeyCommand:
		payload, err := json.Marshal(encodedExpireKeyCommand{
			Key:    c.Key,
			Expire: c.Expire,
		})
		if err != nil {
			return "", nil, err
		}
		return TypeExpireKey, payload, nil
	case *ResetCommand:
		return TypeReset, []byte("{}"), nil
	default:
		return "", nil, fmt.Errorf("unsupported replicated command type: %T", cmd)
	}
}

// Decode reconstructs a command instance from replicated payload.
func Decode(kind string, payload []byte) (interface{}, error) {
	switch kind {
	case TypeSet:
		var in encodedSetCommand
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, err
		}
		value := &anypb.Any{}
		if len(in.Value) > 0 {
			if err := proto.Unmarshal(in.Value, value); err != nil {
				return nil, err
			}
		}
		return &SetCommand{
			Key:    in.Key,
			Value:  value,
			Expire: in.Expire,
		}, nil
	case TypeDel:
		var in encodedDelCommand
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, err
		}
		return &DelCommand{Key: in.Key}, nil
	case TypeExpireKey:
		var in encodedExpireKeyCommand
		if err := json.Unmarshal(payload, &in); err != nil {
			return nil, err
		}
		return &ExpireKeyCommand{
			Key:    in.Key,
			Expire: in.Expire,
		}, nil
	case TypeReset:
		return &ResetCommand{}, nil
	default:
		return nil, fmt.Errorf("unsupported replicated command kind: %s", kind)
	}
}

func normalizeAnyValue(v any) ([]byte, error) {
	if a, ok := v.(*anypb.Any); ok {
		return proto.Marshal(a)
	}
	a, err := utils.ConvertToAnyPB(v)
	if err != nil {
		return nil, err
	}
	return proto.Marshal(a)
}
