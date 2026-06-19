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
	var i32 wrapperspb.Int32Value
	if err := a.UnmarshalTo(&i32); err == nil {
		return int64(i32.Value), nil
	}
	var u64 wrapperspb.UInt64Value
	if err := a.UnmarshalTo(&u64); err == nil {
		return u64.Value, nil
	}
	var u32 wrapperspb.UInt32Value
	if err := a.UnmarshalTo(&u32); err == nil {
		return uint64(u32.Value), nil
	}
	var f64 wrapperspb.DoubleValue
	if err := a.UnmarshalTo(&f64); err == nil {
		return f64.Value, nil
	}
	var f32 wrapperspb.FloatValue
	if err := a.UnmarshalTo(&f32); err == nil {
		return float64(f32.Value), nil
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
