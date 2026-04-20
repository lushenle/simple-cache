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

func TestIsSelf(t *testing.T) {
	tests := []struct {
		name string
		addr string // bind address (t.addr)
		peer string // peer URL
		want bool
	}{
		{"BindAllMatchesLoopback", ":9090", "http://127.0.0.1:9090", true},
		{"BindAllMatchesLocalhost", ":9090", "http://localhost:9090", true},
		{"BindAllMatchesIPv6Loopback", ":9090", "http://[::1]:9090", true},
		{"BindAllDifferentPort", ":9090", "http://127.0.0.1:9091", false},
		{"BindAllNonLoopback", ":9090", "http://10.0.0.1:9090", false},
		{"ExplicitMatchesSame", "127.0.0.1:9090", "http://127.0.0.1:9090", true},
		{"ExplicitDifferentHost", "127.0.0.1:9090", "http://10.0.0.2:9090", false},
		{"ExplicitDifferentPort", "127.0.0.1:9090", "http://127.0.0.1:8080", false},
		{"ZeroIPv4MatchesLoopback", "0.0.0.0:9090", "http://127.0.0.1:9090", true},
		{"ZeroIPv6MatchesLoopback", "[::]:9090", "http://[::1]:9090", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &HTTPTransport{addr: tt.addr}
			require.Equal(t, tt.want, tr.isSelf(tt.peer))
		})
	}
}
