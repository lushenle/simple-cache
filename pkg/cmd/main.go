package main

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/lushenle/simple-cache/pkg/cache"
	"github.com/lushenle/simple-cache/pkg/log"
	"github.com/lushenle/simple-cache/pkg/metrics"
	"github.com/lushenle/simple-cache/pkg/pb"
	"github.com/lushenle/simple-cache/pkg/server"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/cors"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

//go:embed swagger/*
var content embed.FS

func main() {
	// Initialize Prometheus metrics
	metrics.Init()

	// Start Prometheus HTTP server
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(":2112", nil)

	plugin := log.NewStdoutPlugin(zapcore.DebugLevel)
	logger := log.NewLogger(plugin)

	// Create a new gRPC server and listen on port 50051
	c := cache.New(30*time.Second, logger)
	srv := server.New(c)

	ctx, cancel := context.WithCancel(context.Background())
	waitGroup, ctx := errgroup.WithContext(ctx)
	waitGroup.Go(func() error {
		runGatewayServer(ctx, waitGroup, srv, logger)
		return nil
	})
	waitGroup.Go(func() error {
		<-ctx.Done()
		cancel()
		return nil
	})

	// Start the gRPC server
	lis, _ := net.Listen("tcp", ":5051")
	grpcServer := grpc.NewServer()
	pb.RegisterCacheServiceServer(grpcServer, srv)
	grpcServer.Serve(lis)
}

func runGatewayServer(ctx context.Context, waitGroup *errgroup.Group, svr *server.CacheService, logger *zap.Logger) {
	jsonOption := runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
		MarshalOptions: protojson.MarshalOptions{
			UseProtoNames: true,
		},
		UnmarshalOptions: protojson.UnmarshalOptions{
			DiscardUnknown: true,
		},
	})

	grpcMux := runtime.NewServeMux(jsonOption)
	err := pb.RegisterCacheServiceHandlerServer(ctx, grpcMux, svr)
	if err != nil {
		logger.Fatal("failed to register gateway server", zap.Error(err))
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/", grpcMux)

	// Access the embedded 'swagger' folder.
	swaggerFS, err := fs.Sub(content, "swagger")
	if err != nil {
		logger.Fatal("failed to load swagger files", zap.Error(err))
		return
	}

	// Create a file server to serve the embedded content.
	fileServer := http.FileServer(http.FS(swaggerFS))
	mux.Handle("/swagger/", http.StripPrefix("/swagger", fileServer))

	c := cors.New(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{
			http.MethodHead,
			http.MethodOptions,
			http.MethodGet,
			http.MethodPost,
			http.MethodPut,
			http.MethodPatch,
			http.MethodDelete,
		},
		AllowedHeaders: []string{
			"Content-Type",
			"Authorization",
		},
		AllowCredentials: true,
	})
	handler := c.Handler(log.HttpLogger(mux, logger))

	httpServer := &http.Server{
		Handler: handler,
		Addr:    ":8080",
	}

	waitGroup.Go(func() error {
		logger.Info("start gateway server", zap.String("addr", httpServer.Addr))
		err = httpServer.ListenAndServe()
		if err != nil {
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			logger.Fatal("failed to start gateway server", zap.Error(err))
			return err
		}
		return nil
	})

	waitGroup.Go(func() error {
		<-ctx.Done()
		logger.Info("graceful shutdown HTTP gateway server", zap.String("addr", httpServer.Addr))

		err = httpServer.Shutdown(context.Background())
		if err != nil {
			if !errors.Is(err, http.ErrServerClosed) {
				logger.Fatal("failed to shutdown HTTP server", zap.Error(err))
				return err
			}
			return nil
		}

		logger.Info("HTTP gateway server is stopped", zap.String("addr", httpServer.Addr))
		return nil
	})
}
