package command

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestEncodeDecodeSetCommand(t *testing.T) {
	value, err := anypb.New(wrapperspb.String("hello"))
	require.NoError(t, err)

	kind, payload, err := Encode(&SetCommand{
		Key:    "greeting",
		Value:  value,
		Expire: "1m",
	})
	require.NoError(t, err)
	require.Equal(t, TypeSet, kind)

	decoded, err := Decode(kind, payload)
	require.NoError(t, err)

	cmd := decoded.(*SetCommand)
	require.Equal(t, "greeting", cmd.Key)
	require.Equal(t, "1m", cmd.Expire)
	require.IsType(t, &anypb.Any{}, cmd.Value)
}

func TestEncodeDecodeDelCommand(t *testing.T) {
	kind, payload, err := Encode(&DelCommand{Key: "k1"})
	require.NoError(t, err)
	require.Equal(t, TypeDel, kind)

	decoded, err := Decode(kind, payload)
	require.NoError(t, err)
	require.Equal(t, "k1", decoded.(*DelCommand).Key)
}
