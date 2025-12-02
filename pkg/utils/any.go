package utils

import (
	"fmt"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func FromAnyPB(a *anypb.Any) (any, error) {
	if a == nil {
		return nil, nil
	}
	var sv wrapperspb.StringValue
	if err := a.UnmarshalTo(&sv); err == nil {
		return sv.Value, nil
	}
	var bv wrapperspb.BoolValue
	if err := a.UnmarshalTo(&bv); err == nil {
		return bv.Value, nil
	}
	var i64 wrapperspb.Int64Value
	if err := a.UnmarshalTo(&i64); err == nil {
		return i64.Value, nil
	}
	var f64 wrapperspb.DoubleValue
	if err := a.UnmarshalTo(&f64); err == nil {
		return f64.Value, nil
	}
	var bytes wrapperspb.BytesValue
	if err := a.UnmarshalTo(&bytes); err == nil {
		return bytes.Value, nil
	}
	var v structpb.Value
	if err := a.UnmarshalTo(&v); err == nil {
		switch v.Kind.(type) {
		case *structpb.Value_StringValue:
			return v.GetStringValue(), nil
		case *structpb.Value_NumberValue:
			return v.GetNumberValue(), nil
		case *structpb.Value_BoolValue:
			return v.GetBoolValue(), nil
		case *structpb.Value_NullValue:
			return nil, nil
		case *structpb.Value_StructValue:
			return v.GetStructValue(), nil
		case *structpb.Value_ListValue:
			return v.GetListValue(), nil
		}
	}
	return fmt.Sprintf("%v", a), nil
}
