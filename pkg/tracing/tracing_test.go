package tracing

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInitDisabledIsNoOp(t *testing.T) {
	shutdown, err := Init(context.Background(), false, "localhost:4317", "simple-cache")
	require.NoError(t, err)
	require.NotNil(t, shutdown)
	require.NoError(t, shutdown(context.Background()))
}
