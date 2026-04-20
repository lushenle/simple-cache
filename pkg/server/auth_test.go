package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryAuthInterceptor(t *testing.T) {
	interceptor := UnaryAuthInterceptor("secret")
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return "ok", nil
	}

	t.Run("RejectsProtectedMethodWithoutToken", func(t *testing.T) {
		_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{
			FullMethod: "/pb.CacheService/Set",
		}, handler)
		assert.Error(t, err)
		st, ok := status.FromError(err)
		assert.True(t, ok)
		assert.Equal(t, codes.Unauthenticated, st.Code())
	})

	t.Run("AllowsProtectedMethodWithToken", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-token", "secret"))
		resp, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{
			FullMethod: "/pb.CacheService/Set",
		}, handler)
		assert.NoError(t, err)
		assert.Equal(t, "ok", resp)
	})
}

func TestHTTPAuthMiddleware(t *testing.T) {
	handler := HTTPAuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), "secret")

	t.Run("RejectsProtectedPathWithoutToken", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/cluster/join", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})

	t.Run("AllowsProtectedPathWithToken", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/cluster/join", nil)
		req.Header.Set("x-api-token", "secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("AllowsReadOnlyPathWithoutToken", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})
}
