package utils

import (
	"fmt"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func ConvertToAnyPB(v any) (*anypb.Any, error) {
	if a, ok := v.(*anypb.Any); ok {
		return a, nil
	}
	msg, err := ConvertAnyToProtoMessage(v)
	if err != nil {
		return nil, fmt.Errorf("failed to convert to proto message: %w", err)
	}
	return anypb.New(msg)
}

func ConvertAnyToProtoMessage(v any) (proto.Message, error) {
	switch t := v.(type) {
	case proto.Message:
		return t, nil
	case string:
		return wrapperspb.String(t), nil
	case bool:
		return wrapperspb.Bool(t), nil
	case int:
		return wrapperspb.Int64(int64(t)), nil
	case int32:
		return wrapperspb.Int32(t), nil
	case int64:
		return wrapperspb.Int64(t), nil
	case uint:
		return wrapperspb.UInt64(uint64(t)), nil
	case uint32:
		return wrapperspb.UInt32(t), nil
	case uint64:
		return wrapperspb.UInt64(t), nil
	case float32:
		return wrapperspb.Float(t), nil
	case float64:
		return wrapperspb.Double(t), nil
	case []byte:
		return wrapperspb.Bytes(t), nil
	case nil:
		return structpb.NewNullValue(), nil
	default:
		return wrapperspb.String(fmt.Sprintf("%v", v)), nil
	}
}
