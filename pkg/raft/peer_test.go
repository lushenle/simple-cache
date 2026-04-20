package raft

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizePeerAddr(t *testing.T) {
	t.Run("NormalizesTrailingSlash", func(t *testing.T) {
		got, err := NormalizePeerAddr("HTTP://127.0.0.1:9090/")
		require.NoError(t, err)
		require.Equal(t, "http://127.0.0.1:9090", got)
	})

	t.Run("RejectsInvalidAddr", func(t *testing.T) {
		_, err := NormalizePeerAddr("file:///tmp/socket")
		require.Error(t, err)
	})
}
