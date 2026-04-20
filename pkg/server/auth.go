package server

import (
	"context"
	"net/http"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const authHeader = "x-api-token"

func UnaryAuthInterceptor(token string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if token == "" || !requiresRPCAuth(info.FullMethod) {
			return handler(ctx, req)
		}
		if !hasAuthorizedMetadata(ctx, token) {
			return nil, status.Error(codes.Unauthenticated, "missing or invalid auth token")
		}
		return handler(ctx, req)
	}
}

func HTTPAuthMiddleware(next http.Handler, token string) http.Handler {
	if token == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requiresHTTPAuth(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !authorizedRequest(r, token) {
			http.Error(w, "missing or invalid auth token", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requiresRPCAuth(method string) bool {
	switch method {
	case "/pb.CacheService/Set",
		"/pb.CacheService/Del",
		"/pb.CacheService/ExpireKey",
		"/pb.CacheService/Reset",
		"/pb.CacheService/Dump",
		"/pb.CacheService/Load":
		return true
	default:
		return false
	}
}

func requiresHTTPAuth(r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/cluster/") {
		return true
	}
	switch r.URL.Path {
	case "/v1/load", "/v1/dump", "/v1":
		return true
	}
	return r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions
}

func hasAuthorizedMetadata(ctx context.Context, token string) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}
	for _, value := range md.Get(authHeader) {
		if value == token {
			return true
		}
	}
	for _, value := range md.Get("authorization") {
		if strings.TrimPrefix(value, "Bearer ") == token {
			return true
		}
	}
	return false
}

func authorizedRequest(r *http.Request, token string) bool {
	if r.Header.Get(authHeader) == token {
		return true
	}
	auth := r.Header.Get("Authorization")
	return strings.TrimPrefix(auth, "Bearer ") == token
}
